#Requires -Version 5.1
# scripts/otto-gw.ps1 - PowerShell lifecycle manager for otto-gateway on Windows.
# Renamed from scripts/otto.ps1 to avoid collision with the otto CLI binary.
#
# Subcommands:
#   start | stop | status | restart | logs | run | env
#
# Wrapper env overrides (where logs/pids/binary live):
#   $env:OTTO_BIN, $env:OTTO_PID, $env:OTTO_LOG, $env:OTTO_STATE_DIR, $env:OTTO_ADDR
#
# Gateway config flags (for start | restart | run | env):
#   -Pii MODE          off | replace | mask | hash | drop
#                        off → PII_REDACTION_ENABLED=false
#                        others → PII_REDACTION_ENABLED=true PII_REDACTION_MODE=MODE
#   -HashKey KEY       PII_HASH_KEY (required when -Pii hash)
#   -Entities LIST     PII_ENABLED_ENTITIES (comma list)
#   -Hooks LIST        ENABLED_HOOKS (comma list, empty = all)
#   -Auth TOKEN        AUTH_TOKEN
#   -EnvFile PATH      Override default .env search
#   -ShowSecrets       (env subcommand only) print unmasked secrets
#
# .env loader (laptop-friendly persistence):
#   Loads the first match of:  .\.env.otto-gw  →  $HOME\.otto-gw.env
#   Override with -EnvFile PATH or $env:OTTO_ENV_FILE=PATH.
#   CLI flags WIN over .env values.

param(
    [Parameter(Position=0)][string]$Command = "help",
    [string]$Pii,
    [string]$HashKey,
    [string]$Entities,
    [string]$Hooks,
    [string]$Auth,
    [string]$EnvFile,
    [switch]$ShowSecrets,
    [switch]$Follow,
    # init subcommand flags:
    [string]$Dest,
    [switch]$Here,
    [switch]$Force,
    [switch]$NonInteractive,
    [string]$AuthToken,
    [string]$Kiro,
    [string]$Addr
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$BinPath    = if ($env:OTTO_BIN)    { $env:OTTO_BIN }    else { ".\bin\otto-gateway.exe" }
# PID file lives under .otto\gw\ (project-local) rather than %TEMP%. Some
# locked-down Windows environments (Group Policy, AppLocker, mapped
# network temp) make %TEMP% unreliable. The .otto\ namespace is shared
# with the OTTER client; we nest under gw\ to avoid collisions.
$StateDir   = if ($env:OTTO_STATE_DIR) { $env:OTTO_STATE_DIR } else { ".\.otto\gw" }
$PidFile    = if ($env:OTTO_PID)    { $env:OTTO_PID }    else { "$StateDir\otto-gateway.pid" }
# $LogFile = structured rotated log the gateway owns via timberjack
# (LOG_FILE env, daily rotation, 7-day retention).
$LogFile    = if ($env:OTTO_LOG)    { $env:OTTO_LOG }    else { ".\logs\otto-gateway.log" }
# Start-Process requires separate files for stdout / stderr redirection
# AND cannot share a single file across the two streams. Both sidecars
# here capture only pre-logger / kiro-cli / crash output; stdout sidecar
# stays essentially empty in normal operation since LOG_FILE routes all
# structured slog output to $LogFile.
$LogBootOut = if ($env:OTTO_LOGOUT) { $env:OTTO_LOGOUT } else { [System.IO.Path]::ChangeExtension($LogFile, '.boot-out.log') }
$LogBootErr = if ($env:OTTO_LOGERR) { $env:OTTO_LOGERR } else { [System.IO.Path]::ChangeExtension($LogFile, '.boot-err.log') }
$Addr       = if ($env:OTTO_ADDR)   { $env:OTTO_ADDR }   else { "http://127.0.0.1:18080" }

$DefaultEnvPaths = @(".\.env.otto-gw", "$env:USERPROFILE\.otto-gw.env")

function Resolve-EnvFile {
    if ($EnvFile)              { return $EnvFile }
    if ($env:OTTO_ENV_FILE)    { return $env:OTTO_ENV_FILE }
    foreach ($p in $DefaultEnvPaths) {
        if (Test-Path $p) { return $p }
    }
    return $null
}

function Import-DotEnv {
    param([string]$Path)
    if (-not (Test-Path $Path)) { return }
    Get-Content $Path | ForEach-Object {
        $line = $_.TrimStart()
        if (-not $line)            { return }
        if ($line.StartsWith('#')) { return }
        if ($line -match '^\s*export\s+') {
            $line = $line -replace '^\s*export\s+', ''
        }
        if ($line -notmatch '=') { return }
        $key, $val = $line -split '=', 2
        $val = $val.Trim()
        # Strip one layer of surrounding single or double quotes.
        if (($val.StartsWith('"') -and $val.EndsWith('"')) -or `
            ($val.StartsWith("'") -and $val.EndsWith("'"))) {
            $val = $val.Substring(1, $val.Length - 2)
        }
        Set-Item -Path "env:$($key.Trim())" -Value $val
    }
}

function Apply-CliFlags {
    if ($Pii) {
        if ($Pii -eq 'off') {
            $env:PII_REDACTION_ENABLED = 'false'
        } else {
            $env:PII_REDACTION_ENABLED = 'true'
            $env:PII_REDACTION_MODE    = $Pii
        }
    }
    if ($HashKey)  { $env:PII_HASH_KEY         = $HashKey }
    if ($Entities) { $env:PII_ENABLED_ENTITIES = $Entities }
    if ($Hooks)    { $env:ENABLED_HOOKS        = $Hooks }
    if ($Auth)     { $env:AUTH_TOKEN           = $Auth }
}

function Initialize-Config {
    $envFilePath = Resolve-EnvFile
    if ($envFilePath) {
        Import-DotEnv -Path $envFilePath
        Write-Host "loaded env file: $envFilePath" -ForegroundColor DarkGray
        # Surface the resolved path to the gateway so it can log at INFO
        # which file the wrapper actually used. The binary reads this from
        # os.Getenv("OTTO_ENV_FILE_LOADED") at startup.
        $env:OTTO_ENV_FILE_LOADED = $envFilePath
    }
    Apply-CliFlags
}

# Preflight-Kiro resolves KIRO_CMD before the gateway launches:
#   1. If unset → auto-detect 'kiro-cli' on PATH and silently set
#      $env:KIRO_CMD. Surfaces a brief "auto-detected" line.
#   2. If set but doesn't resolve → warn (don't abort).
#   3. If set and resolves → silent (happy path).
# Degraded boot is non-fatal — /health endpoints work without kiro-cli.
function Preflight-Kiro {
    $kiro = $env:KIRO_CMD
    if (-not $kiro) {
        $cmd = Get-Command kiro-cli -ErrorAction SilentlyContinue
        $found = if ($cmd) { $cmd.Source } else { $null }
        if ($found) {
            $env:KIRO_CMD = $found
            Write-Host "  ✓  KIRO_CMD auto-detected: $found" -ForegroundColor DarkGray
            return
        }
        Write-Host "  ⚠  KIRO_CMD is unset and 'kiro-cli' is not on PATH — gateway will boot but chat requests will return 503." -ForegroundColor Yellow
        Write-Host "     Install kiro-cli OR set KIRO_CMD in your .env (or shell)." -ForegroundColor Yellow
        return
    }
    $resolved = $false
    if (Test-Path $kiro -PathType Leaf) { $resolved = $true }
    elseif (Get-Command $kiro -ErrorAction SilentlyContinue) { $resolved = $true }
    if (-not $resolved) {
        Write-Host "  ⚠  KIRO_CMD=`"$kiro`" does not resolve to an executable." -ForegroundColor Yellow
        Write-Host "     Gateway will boot but chat requests will return 503 until this is fixed." -ForegroundColor Yellow
    }
}

# Wait-UntilReady polls $Addr/health up to $TimeoutSec; returns $true on
# first 2xx, $false on timeout or persistent failure.
function Wait-UntilReady {
    param([int]$TimeoutSec = 5)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $r = Invoke-WebRequest -Uri "$Addr/health" -UseBasicParsing -TimeoutSec 1 -ErrorAction Stop
            if ($r.StatusCode -ge 200 -and $r.StatusCode -lt 300) { return $true }
        } catch {
            # health not up yet — also bail if the process died.
            if (Test-Path $PidFile) {
                $p = [int](Get-Content $PidFile -Raw)
                if (-not (Get-Process -Id $p -ErrorAction SilentlyContinue)) { return $false }
            }
        }
        Start-Sleep -Milliseconds 250
    }
    return $false
}

function Start-Gateway {
    if (Test-Path $PidFile) {
        $existingPid = [int](Get-Content $PidFile -Raw)
        if (Get-Process -Id $existingPid -ErrorAction SilentlyContinue) {
            Write-Error "otto-gateway is already running (PID $existingPid)"
            exit 1
        }
        Remove-Item $PidFile
    }
    Initialize-Config
    # Ensure log + state directories exist before launch.
    $logDir = Split-Path -Parent $LogFile
    if ($logDir -and -not (Test-Path $logDir)) {
        New-Item -ItemType Directory -Force -Path $logDir | Out-Null
    }
    $stateDir = Split-Path -Parent $PidFile
    if ($stateDir -and -not (Test-Path $stateDir)) {
        New-Item -ItemType Directory -Force -Path $stateDir | Out-Null
    }
    # Gateway env vars are inherited from the current environment
    # automatically — Start-Process inherits parent env by default.
    # Documented set:
    #   KIRO_CMD, KIRO_ARGS, KIRO_CWD           — kiro-cli subprocess wiring
    #   DEBUG, HTTP_ADDR, PING_INTERVAL         — runtime knobs
    #   AUTH_TOKEN, ALLOWED_IPS                 — auth + IP allowlist (Phase 2)
    #   AUTH_TRUST_XFF                          — opt-in XFF trust (Codex H-7)
    #   POOL_SIZE                               — warm-pool size (Phase 2)
    #   OLLAMA_PATH_PREFIX, OPENAI_PATH_PREFIX  — route prefixes
    #   ENABLED_HOOKS, PII_REDACTION_ENABLED,   — Phase 8 hook chain knobs
    #   PII_ENABLED_ENTITIES, PII_REDACTION_MODE,
    #   PII_HASH_KEY
    #   LOG_FILE                                — daily-rotated log path (timberjack)
    # LOG_FILE points the gateway's structured slog output at the rotated
    # log. The Start-Process sidecars then capture only stderr / pre-
    # logger output / crash trails.
    $env:LOG_FILE = $LogFile
    Preflight-Kiro
    # -NoNewWindow: prevents a console popup (Pitfall 8 in RESEARCH.md).
    # -PassThru: returns the Process object so we can capture its PID.
    $proc = Start-Process `
        -FilePath $BinPath `
        -RedirectStandardOutput $LogBootOut `
        -RedirectStandardError $LogBootErr `
        -NoNewWindow `
        -PassThru
    $proc.Id | Set-Content $PidFile
    Write-Host "otto-gateway starting (PID $($proc.Id))…"
    Write-Host "  log:      $LogFile (rotated daily, 7d retention)"
    Write-Host "  boot/err: $LogBootErr"
    if (Wait-UntilReady 10) {
        Write-Host "  ready:    $Addr/health"
    } else {
        Write-Host "  ❌  gateway did NOT become ready within 10s." -ForegroundColor Red
        # Prefer the structured log (where slog calls go when LOG_FILE
        # is set) and fall back to the boot sidecar.
        $sourceLog = $null
        if ((Test-Path $LogFile) -and (Get-Item $LogFile).Length -gt 0) {
            $sourceLog = $LogFile
        } elseif ((Test-Path $LogBootErr) -and (Get-Item $LogBootErr).Length -gt 0) {
            $sourceLog = $LogBootErr
        }
        if ($sourceLog) {
            Write-Host "     Last 20 lines of ${sourceLog}:" -ForegroundColor Red
            Get-Content -Tail 20 $sourceLog | ForEach-Object { Write-Host "       $_" -ForegroundColor Red }
        } else {
            Write-Host "     (both log files are empty — likely a hung warmup; check KIRO_CMD)" -ForegroundColor Red
        }
        exit 1
    }
}

function Stop-Gateway {
    if (-not (Test-Path $PidFile)) {
        Write-Error "otto-gateway is not running (no PID file)"
        exit 1
    }
    $storedPid = [int](Get-Content $PidFile -Raw)
    $proc = Get-Process -Id $storedPid -ErrorAction SilentlyContinue
    if (-not $proc) {
        Write-Host "otto-gateway: stopped (stale PID)"
        Remove-Item $PidFile -ErrorAction SilentlyContinue
        exit 0
    }
    $proc.Kill()
    $proc.WaitForExit(10000) | Out-Null  # wait up to 10s for clean exit
    Remove-Item $PidFile -ErrorAction SilentlyContinue
    Write-Host "otto-gateway stopped"
}

function Get-GatewayStatus {
    if (-not (Test-Path $PidFile)) {
        Write-Host "otto-gateway: stopped"
        exit 1
    }
    $storedPid = [int](Get-Content $PidFile -Raw)
    $proc = Get-Process -Id $storedPid -ErrorAction SilentlyContinue
    if (-not $proc) {
        Write-Host "otto-gateway: stopped (stale PID)"
        exit 1
    }
    Write-Host "otto-gateway: running (PID $storedPid)"
    try {
        $health = Invoke-RestMethod -Uri "$Addr/health" -TimeoutSec 3
        $health | ConvertTo-Json -Depth 5
    } catch {
        Write-Host "(health check failed: $_)"
    }
}

function Restart-Gateway {
    Stop-Gateway
    Start-Gateway
}

function Get-Logs {
    # Tail the structured rotated log. The two boot sidecars are
    # diagnostics-only (crash + kiro-cli stderr) — operators rarely
    # need to watch them live; surface their paths instead.
    Write-Host "(boot sidecars: $LogBootOut, $LogBootErr)" -ForegroundColor DarkGray
    Get-Content -Wait $LogFile
}

function Invoke-Run {
    Initialize-Config
    # Foreground execution — inherits all env vars from the calling shell.
    & $BinPath
}

# Show-Version delegates to the binary's --version handler so the wrapper
# and the binary cannot disagree. The version string is the same one
# baked into the binary at build time via the Makefile's `-X` ldflag.
function Show-Version {
    if (-not (Test-Path $BinPath -PathType Leaf)) {
        Write-Error "$BinPath not found."
        exit 1
    }
    & $BinPath --version
}

function New-RandomHex {
    param([int]$Bytes = 32)
    $buf = New-Object byte[] $Bytes
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($buf)
    -join ($buf | ForEach-Object { $_.ToString('x2') })
}

function Invoke-Init {
    # Default dest = $HOME\.otto-gw.env unless -Here or -Dest overrides.
    $destPath = $Dest
    if ($Here) { $destPath = ".\.env.otto-gw" }
    if (-not $destPath) { $destPath = Join-Path $env:USERPROFILE ".otto-gw.env" }

    if ((Test-Path $destPath) -and (-not $Force)) {
        Write-Error "ERROR: $destPath already exists. Re-run with -Force to overwrite."
        exit 1
    }

    # Generate secrets unless overridden.
    $authTokenValue = if ($AuthToken) { $AuthToken } else { New-RandomHex 32 }
    $hashKeyValue   = if ($HashKey)   { $HashKey }   else { New-RandomHex 32 }

    # Resolve KIRO_CMD.
    $kiroValue = $Kiro
    if (-not $kiroValue -and -not $NonInteractive) {
        $cmd = Get-Command kiro-cli -ErrorAction SilentlyContinue
        $kiroDefault = if ($cmd) { $cmd.Source } else { $null }
        $prompt = if ($kiroDefault) { "  kiro-cli path [$kiroDefault]" } else { "  kiro-cli path [press Enter to leave unset]" }
        $entered = Read-Host $prompt
        $kiroValue = if ($entered) { $entered } else { $kiroDefault }
    }

    # Resolve HTTP_ADDR.
    $addrValue = $Addr
    if (-not $addrValue -and -not $NonInteractive) {
        Write-Host "  HTTP listen address — default 127.0.0.1:18080 (safe, no collision)."
        Write-Host "  Set to 127.0.0.1:11434 if migrating from the Node Ollama proxy."
        $entered = Read-Host "  HTTP_ADDR [127.0.0.1:18080]"
        $addrValue = if ($entered) { $entered } else { "127.0.0.1:18080" }
    }
    if (-not $addrValue) { $addrValue = "127.0.0.1:18080" }

    # Resolve PII mode.
    $piiValue = $Pii
    if (-not $piiValue -and -not $NonInteractive) {
        Write-Host "  PII redaction — off | replace | mask | hash | drop."
        Write-Host "  Pick 'replace' for human-readable redaction; 'hash' for log correlation."
        $entered = Read-Host "  PII mode [off]"
        $piiValue = if ($entered) { $entered } else { "off" }
    }
    if (-not $piiValue) { $piiValue = "off" }
    $piiEnabled = if ($piiValue -eq "off") { "false" } else { "true" }

    # Ensure parent dir exists.
    $destDir = Split-Path -Parent $destPath
    if ($destDir -and -not (Test-Path $destDir)) {
        New-Item -ItemType Directory -Force -Path $destDir | Out-Null
    }

    # Build the .env content. Here-string with $-vars expanded.
    $ts = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    $kiroLine = if ($kiroValue) { "KIRO_CMD=$kiroValue" } else { "KIRO_CMD=" }
    $content = @"
# Generated by 'otto-gw init' on $ts
# Edit any value; restart the gateway to apply.

# Required — bearer token clients must send as Authorization: Bearer <token>
AUTH_TOKEN=$authTokenValue

# kiro-cli subprocess wiring. Without this set, chat requests return 503.
$kiroLine

# HTTP listen address. Default 127.0.0.1:18080; use :11434 for Ollama compat.
HTTP_ADDR=$addrValue

# ─── Phase 8 hook chain ─────────────────────────────────────────────────────
# Empty ENABLED_HOOKS = all hooks run in registration order. Comma-list to
# allowlist (unknown names cause boot failure — typo protection).
# ENABLED_HOOKS=RequestIDHook,AuthHook,PIIRedactionHook,LoggingHook

PII_REDACTION_ENABLED=$piiEnabled
PII_REDACTION_MODE=$piiValue

# HMAC-SHA256 key for hash mode. Pre-generated for you; rotate to break
# attacker log correlation if you suspect a key leak.
PII_HASH_KEY=$hashKeyValue

# Empty = all six recognizers (Email, IPv4, IPv6, SSN, CreditCard, USPhone).
# PII_ENABLED_ENTITIES=Email,SSN,CreditCard

# ─── Misc ────────────────────────────────────────────────────────────────────
# DEBUG=true
# ALLOWED_IPS=127.0.0.1,::1
# AUTH_TRUST_XFF=false
# POOL_SIZE=2
"@
    Set-Content -Path $destPath -Value $content -Encoding UTF8

    # Best-effort restrict permissions (Windows doesn't have a 0600 equivalent,
    # but we can at least ACL the file to the current user only). Optional.
    try {
        $acl = Get-Acl $destPath
        $acl.SetAccessRuleProtection($true, $false)
        $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
            [System.Security.Principal.WindowsIdentity]::GetCurrent().Name,
            "FullControl", "Allow"
        )
        $acl.AddAccessRule($rule)
        Set-Acl $destPath $acl
    } catch {
        # ACL hardening best-effort only; fall back to default permissions.
    }

    Write-Host "✓ wrote $destPath"
    Write-Host "  AUTH_TOKEN:    $($authTokenValue.Substring(0,8))…(generated)"
    Write-Host "  PII_HASH_KEY:  $($hashKeyValue.Substring(0,8))…(generated)"
    if ($kiroValue) { Write-Host "  KIRO_CMD:      $kiroValue" } else { Write-Host "  KIRO_CMD:      (unset — chat will 503 until you set it)" }
    Write-Host "  HTTP_ADDR:     $addrValue"
    Write-Host "  PII:           $piiValue"
    Write-Host ""
    Write-Host "Next: .\scripts\otto-gw.ps1 start"
}

function Show-Env {
    Initialize-Config
    $keys = @(
        'ENABLED_HOOKS','PII_REDACTION_ENABLED','PII_REDACTION_MODE',
        'PII_ENABLED_ENTITIES','PII_HASH_KEY','AUTH_TOKEN','ALLOWED_IPS',
        'AUTH_TRUST_XFF','HTTP_ADDR','KIRO_CMD','KIRO_ARGS','KIRO_CWD',
        'POOL_SIZE','DEBUG'
    )
    foreach ($k in $keys) {
        $v = [Environment]::GetEnvironmentVariable($k, 'Process')
        if (-not $v) { continue }
        if (-not $ShowSecrets -and ($k -eq 'AUTH_TOKEN' -or $k -eq 'PII_HASH_KEY')) {
            $masked = "$($v.Substring(0, [Math]::Min(4, $v.Length)))…($($v.Length) chars)"
            Write-Output "$k=$masked"
        } else {
            Write-Output "$k=$v"
        }
    }
}

function Show-Usage {
    @"
Usage: .\scripts\otto-gw.ps1 <command> [flags]

Commands:
  start [flags]       Start gateway in background
  stop                Stop background gateway
  status              Show gateway status and health
  restart [flags]     Stop then start (re-applies flags / .env)
  logs                Tail both stdout and stderr log files
  run [flags]         Run gateway in foreground
  env [-ShowSecrets]  Print the resolved gateway env that would be passed
  version             Print the gateway binary version (delegates to bin\otto-gateway --version)

Gateway config flags (for start | restart | run | env):
  -Pii MODE           off | replace | mask | hash | drop
  -HashKey KEY        PII_HASH_KEY (required when -Pii hash)
  -Entities LIST      PII_ENABLED_ENTITIES (comma list)
  -Hooks LIST         ENABLED_HOOKS allowlist (comma list, empty = all)
  -Auth TOKEN         AUTH_TOKEN
  -EnvFile PATH       Override the default .env search

.env auto-load search:
  1. -EnvFile PATH                    (CLI override)
  2. `$env:OTTO_ENV_FILE              (env override)
  3. .\.env.otto-gw                   (project-local)
  4. `$env:USERPROFILE\.otto-gw.env   (per-user)

Precedence (highest first): CLI flag → .env file → inherited shell env.

See scripts\.env.otto-gw.example for a starter template.
"@ | Write-Host
    exit 1
}

switch ($Command) {
    "init"    { Invoke-Init }
    "start"   { Start-Gateway }
    "stop"    { Stop-Gateway }
    "status"  { Get-GatewayStatus }
    "restart" { Restart-Gateway }
    "logs"    { Get-Logs }
    "run"     { Invoke-Run }
    "env"     { Show-Env }
    "version" { Show-Version }
    default   { Show-Usage }
}
