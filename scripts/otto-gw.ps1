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
#   -IdleTimeout INT   STREAM_IDLE_TIMEOUT_SEC (default 30; 0 disables idle watchdog)
#   -EnvFile PATH      Override default .env search
#   -ShowSecrets       (env subcommand only) print unmasked secrets
#
# .env loader (laptop-friendly persistence):
#   Loads the first match of:  .\.env.otto-gw  →  $HOME\.otto-gw.env
#   Then chains the first match of:
#     .\.otto-gw.overrides.env  →  $HOME\.otto-gw.overrides.env
#   Override either with -EnvFile PATH / -OverridesFile PATH, or with
#   $env:OTTO_ENV_FILE / $env:OTTO_OVERRIDES_FILE.
#   The overrides file is loaded SECOND; same-key values win (two-file model).
#   CLI flags WIN over overrides; overrides WIN over .env.

param(
    [Parameter(Position=0)][string]$Command = "help",
    [string]$Pii,
    [string]$HashKey,
    [string]$Entities,
    [string]$Hooks,
    [string]$Auth,
    [int]$IdleTimeout = -1,
    [switch]$Trace,
    [string]$EnvFile,
    [string]$OverridesFile,
    [string]$Template,
    [switch]$ShowSecrets,
    [switch]$Follow,
    [switch]$DryRun,
    [switch]$Yes,
    # init subcommand flags:
    [string]$Dest,
    [string]$OverridesDest,
    [switch]$Here,
    [switch]$Force,
    [switch]$NonInteractive,
    [switch]$AuthEnabled,
    [switch]$ChatTrace,
    [switch]$RegenerateSecrets,
    [string]$AuthToken,
    [string]$Kiro,
    [string]$Addr
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# -Debug is PowerShell's reserved common parameter (this is an advanced function
# because of [Parameter()] above), so we can't declare our own. Detect whether
# the caller passed it, then neutralize $DebugPreference so it never prompts —
# we only use -Debug as a boolean to flip the gateway into DEBUG logging.
$DebugRequested = $PSBoundParameters.ContainsKey('Debug')
$DebugPreference = 'SilentlyContinue'

# Resolve this script's own install root. The one-liner installer exposes
# otto-gw via $OTTO_HOME\scripts on PATH (otto-gw.bat -> this .ps1), so the
# wrapper may run from any cwd. Default paths anchor to the install root (the
# dir above scripts\), NOT the caller's cwd — env overrides below still win.
# This also matches the legacy "cd into the extracted folder, run it" flow,
# where install root == cwd. $PSScriptRoot is the dir containing this .ps1.
$InstallRoot = Split-Path -Parent $PSScriptRoot

$BinPath    = if ($env:OTTO_BIN)    { $env:OTTO_BIN }    else { Join-Path $InstallRoot 'bin\otto-gateway.exe' }
# PID file lives under .otto\gw\ (install-root-local) rather than %TEMP%. Some
# locked-down Windows environments (Group Policy, AppLocker, mapped
# network temp) make %TEMP% unreliable. The .otto\ namespace is shared
# with the OTTER client; we nest under gw\ to avoid collisions.
$StateDir   = if ($env:OTTO_STATE_DIR) { $env:OTTO_STATE_DIR } else { Join-Path $InstallRoot '.otto\gw' }
$PidFile    = if ($env:OTTO_PID)    { $env:OTTO_PID }    else { "$StateDir\otto-gateway.pid" }
# $LogFile = structured rotated log the gateway owns via timberjack
# (LOG_FILE env, daily rotation, 7-day retention).
$LogFile    = if ($env:OTTO_LOG)    { $env:OTTO_LOG }    else { Join-Path $InstallRoot 'logs\otto-gateway.log' }
# Start-Process requires separate files for stdout / stderr redirection
# AND cannot share a single file across the two streams. Both sidecars
# here capture only pre-logger / kiro-cli / crash output; stdout sidecar
# stays essentially empty in normal operation since LOG_FILE routes all
# structured slog output to $LogFile.
$LogBootOut = if ($env:OTTO_LOGOUT) { $env:OTTO_LOGOUT } else { [System.IO.Path]::ChangeExtension($LogFile, '.boot-out.log') }
$LogBootErr = if ($env:OTTO_LOGERR) { $env:OTTO_LOGERR } else { [System.IO.Path]::ChangeExtension($LogFile, '.boot-err.log') }
# Health-check base URL (scheme + host:port). Distinct from the -Addr init
# param (a bare host:port for HTTP_ADDR) — they MUST NOT share a name, or this
# line clobbers the param and init writes HTTP_ADDR with an http:// scheme,
# which Go's net.Listen rejects ("too many colons in address").
$HealthUrl  = if ($env:OTTO_ADDR)   { $env:OTTO_ADDR }   else { "http://127.0.0.1:18080" }

$DefaultEnvPaths = @(".\.env.otto-gw", "$env:USERPROFILE\.otto-gw.env")
# Two-file model (locked in 260531-tl1 CONTEXT.md Decision 1): the overrides
# file is loaded SECOND so its keys win for any shared key. The .env-file and
# overrides-file flags are DECOUPLED — setting -EnvFile does NOT auto-resolve
# a sibling overrides file. Resolution always walks the explicit chain.
$DefaultOverridesPaths = @(".\.otto-gw.overrides.env", "$env:USERPROFILE\.otto-gw.overrides.env")

function Resolve-EnvFile {
    if ($EnvFile)              { return $EnvFile }
    if ($env:OTTO_ENV_FILE)    { return $env:OTTO_ENV_FILE }
    foreach ($p in $DefaultEnvPaths) {
        if (Test-Path $p) { return $p }
    }
    return $null
}

# Resolve-OverridesFile mirrors Resolve-EnvFile's shape for the .otto-gw.overrides.env
# layer. Precedence: -OverridesFile > $env:OTTO_OVERRIDES_FILE > project-local
# > per-user. Returns $null on miss (Initialize-Config gates on that).
function Resolve-OverridesFile {
    if ($OverridesFile)             { return $OverridesFile }
    if ($env:OTTO_OVERRIDES_FILE)   { return $env:OTTO_OVERRIDES_FILE }
    foreach ($p in $DefaultOverridesPaths) {
        if (Test-Path $p) { return $p }
    }
    return $null
}

# Resolve-TemplateFile returns the path of .env.otto-gw.example (single source
# of truth for keys + defaults + docs). Precedence: -Template > $env:OTTO_TEMPLATE_FILE
# > sibling to this script. Returns $null when the resolved path doesn't exist.
function Resolve-TemplateFile {
    $candidate = $null
    if ($Template) {
        $candidate = $Template
    } elseif ($env:OTTO_TEMPLATE_FILE) {
        $candidate = $env:OTTO_TEMPLATE_FILE
    } else {
        $candidate = Join-Path $PSScriptRoot '.env.otto-gw.example'
    }
    if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) { return $null }
    return $candidate
}

# Get-TemplateKeys emits the ordered list of KEY names found in $Path
# (template OR any env file), one per line. Matches commented and
# uncommented forms. Defensive: keys must be shell-identifier-shaped so
# prose comments containing '=' don't get caught as fake keys.
function Get-TemplateKeys {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return @() }
    $out = New-Object System.Collections.Generic.List[string]
    Get-Content -LiteralPath $Path | ForEach-Object {
        $line = $_.TrimStart()
        if (-not $line) { return }
        if ($line.StartsWith('#')) {
            $line = $line.Substring(1).TrimStart()
            if (-not $line) { return }
        }
        if ($line -match '^\s*export\s+') { $line = $line -replace '^\s*export\s+', '' }
        if ($line -notmatch '=') { return }
        $key = ($line -split '=', 2)[0]
        if (-not $key) { return }
        # Identifier-shaped only (letters, underscore prefix).
        if ($key -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') { return }
        [void]$out.Add($key)
    }
    return ,$out.ToArray()
}

# Get-EnvKeysPresent is a thin alias for Get-TemplateKeys — the name reads
# more naturally at the upgrade-env diff calculation site.
function Get-EnvKeysPresent {
    param([Parameter(Mandatory)][string]$Path)
    return Get-TemplateKeys -Path $Path
}

# Get-DefaultValue returns the literal default value the template declares
# for KEY (commented or not). Strips ONE layer of surrounding quotes.
# Returns $null on miss. Used by migrate-to-overrides to decide which
# operator values differ from the template default.
function Get-DefaultValue {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Key
    )
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    foreach ($raw in Get-Content -LiteralPath $Path) {
        $line = $raw.TrimStart()
        if (-not $line) { continue }
        if ($line.StartsWith('#')) {
            $line = $line.Substring(1).TrimStart()
            if (-not $line) { continue }
        }
        if ($line -match '^\s*export\s+') { $line = $line -replace '^\s*export\s+', '' }
        if ($line -notmatch '=') { continue }
        $k, $v = $line -split '=', 2
        if ($k.Trim() -ne $Key) { continue }
        $val = $v.Trim()
        if (($val.StartsWith('"') -and $val.EndsWith('"')) -or `
            ($val.StartsWith("'") -and $val.EndsWith("'"))) {
            $val = $val.Substring(1, $val.Length - 2)
        }
        return $val
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
    # Quick 260531-ruv: -IdleTimeout INT -> STREAM_IDLE_TIMEOUT_SEC.
    # Sentinel -1 = "flag not passed"; 0 is the explicit-disable value.
    if ($IdleTimeout -ge 0) { $env:STREAM_IDLE_TIMEOUT_SEC = $IdleTimeout.ToString() }
    if ($DebugRequested) { $env:DEBUG          = 'true' }
    # -Trace implies -Debug plus chat-trace: one switch for full observability.
    # config.Load auto-prepends ChatTraceHook to ENABLED_HOOKS at runtime when
    # CHAT_TRACE=true, so the wrapper only needs to set the two env vars.
    if ($Trace) { $env:DEBUG = 'true'; $env:CHAT_TRACE = 'true' }
}

$script:DeprecationWarnEmitted = $false

# Test-SingleFileModel — mirror of bash detect_single_file_model. Emits a
# one-line deprecation WARN when the operator is running on the legacy
# single-file install model (no .otto-gw.overrides.env, .otto-gw.env carries
# uncommented operator values). The WARN is written via Write-Warning so it
# goes to the warning stream (functionally stderr) and stays out of normal
# stdout capture.
#
# Detection mirrors the bash heuristic:
#   1. No overrides file resolved.
#   2. .otto-gw.env (or resolved env file) exists.
#   3. At least one of AUTH_TOKEN / PII_HASH_KEY / HTTP_ADDR is uncommented
#      with a non-placeholder value, OR KIRO_CMD is uncommented and
#      non-empty.
function Test-SingleFileModel {
    if ($script:DeprecationWarnEmitted) { return }
    $envFile = Resolve-EnvFile
    $overridesFile = Resolve-OverridesFile
    if (-not $envFile -or -not (Test-Path -LiteralPath $envFile)) { return }
    if ($overridesFile -and (Test-Path -LiteralPath $overridesFile)) { return }

    $hasOperatorValue = $false
    $placeholders = @('', 'replace-me', 'replace-with-32-byte-secret-key-here')
    foreach ($k in @('AUTH_TOKEN', 'PII_HASH_KEY', 'HTTP_ADDR')) {
        if (Test-EnvKeyUncommented -Path $envFile -Key $k) {
            $v = Get-DefaultValue -Path $envFile -Key $k
            if ($v -and ($placeholders -notcontains $v)) {
                $hasOperatorValue = $true; break
            }
        }
    }
    if (-not $hasOperatorValue) {
        if (Test-EnvKeyUncommented -Path $envFile -Key 'KIRO_CMD') {
            $v = Get-DefaultValue -Path $envFile -Key 'KIRO_CMD'
            if ($v) { $hasOperatorValue = $true }
        }
    }
    if (-not $hasOperatorValue) { return }

    $script:DeprecationWarnEmitted = $true
    Write-Warning "otto-gw: legacy single-file .env model detected -- run ``otto-gw migrate-to-overrides`` to split secrets/overrides into .otto-gw.overrides.env. Single-file support will be removed in v1.7."
}

function Initialize-Config {
    # Two-file model (locked in 260531-tl1 CONTEXT.md Decision 1):
    #   1. .otto-gw.env (generated, byte-for-byte template copy, never edited).
    #   2. .otto-gw.overrides.env (operator-owned, loaded SECOND).
    # Set-Item env:KEY is last-write-wins for environment variables (same
    # semantics as bash export), so loading overrides second is the override
    # mechanism.
    $envFilePath = Resolve-EnvFile
    if ($envFilePath) {
        Import-DotEnv -Path $envFilePath
        Write-Host "loaded env file: $envFilePath" -ForegroundColor DarkGray
        # Surface the resolved path to the gateway so it can log at INFO
        # which file the wrapper actually used. The binary reads this from
        # os.Getenv("OTTO_ENV_FILE_LOADED") at startup.
        $env:OTTO_ENV_FILE_LOADED = $envFilePath
    }
    $overridesPath = Resolve-OverridesFile
    if ($overridesPath -and (Test-Path $overridesPath)) {
        Import-DotEnv -Path $overridesPath
        Write-Host "loaded overrides:  $overridesPath" -ForegroundColor DarkGray
        $env:OTTO_OVERRIDES_FILE_LOADED = $overridesPath
    }
    # One-shot legacy-model deprecation WARN.
    Test-SingleFileModel
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

# Wait-UntilReady polls $HealthUrl/health up to $TimeoutSec; returns $true on
# first 2xx, $false on timeout or persistent failure.
function Wait-UntilReady {
    param([int]$TimeoutSec = 5)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $r = Invoke-WebRequest -Uri "$HealthUrl/health" -UseBasicParsing -TimeoutSec 1 -ErrorAction Stop
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
        Write-Host "  ready:    $HealthUrl/health"
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

# Stop-GatewayByName — fallback when the PID file can't drive the stop (older
# wrapper wrote it elsewhere, or the gateway was launched in the foreground via
# 'run'). Matches the binary name (otto-gateway), which never collides with this
# wrapper (otto-gw). Get-Process is native, so no pgrep/grep dependency here.
# Returns $true if it killed at least one process, $false if none were found.
# Repair-KiroOrphans — 260531-ra6 RA6-03 (Windows mirror of bash
# reap_kiro_orphans). Belt-and-suspenders cleanup for hard-crash paths
# (segfault, OOM, Stop-Process -Force of the gateway) that bypass the
# normal subprocess teardown. Called from the tail of Stop-Gateway and
# Stop-GatewayByName; $script:KiroReapDone keeps it idempotent within
# a single invocation.
#
# Safety: matches strictly against the resolved $env:KIRO_CMD absolute
# path via Win32_Process.ExecutablePath (with a CommandLine -like
# secondary check when ExecutablePath is null). Never matches by name
# substring — a kiro-cli the operator runs OUTSIDE the gateway is
# untouched. Also refuses to signal our own pid ($PID) or the parent
# session pid.
$script:KiroReapDone = $false
function Repair-KiroOrphans {
    if ($script:KiroReapDone) { return }
    $script:KiroReapDone = $true

    # Stop-Gateway does NOT call Initialize-Config; load .env locally so
    # $env:KIRO_CMD is in scope without changing Stop-Gateway's startup
    # behaviour. Same defensive pattern as the POSIX side.
    $envFilePath = Resolve-EnvFile
    if ($envFilePath) { Import-DotEnv -Path $envFilePath }

    if (-not $env:KIRO_CMD) { return }

    # Resolve $env:KIRO_CMD to an absolute path so the Win32_Process
    # ExecutablePath comparison is exact. Bare names go through
    # Get-Command which honors $env:PATH; already-absolute paths fall
    # through unchanged.
    $kiroPath = $null
    $cmdInfo  = Get-Command $env:KIRO_CMD -ErrorAction SilentlyContinue
    if ($cmdInfo -and $cmdInfo.Source) { $kiroPath = $cmdInfo.Source }
    if (-not $kiroPath) { $kiroPath = $env:KIRO_CMD }

    # 2s grace for any pending teardown signals to deliver and exit
    # children cleanly before we start swinging.
    Start-Sleep -Seconds 2

    # Primary match: ExecutablePath -ieq $kiroPath (case-insensitive
    # equality; Windows file paths are case-insensitive). Secondary
    # match: ExecutablePath -eq $null with CommandLine -like the
    # resolved path (some kernel-mode processes hide ExecutablePath).
    $procsPrimary = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue `
        | Where-Object { $_.ExecutablePath -and ($_.ExecutablePath -ieq $kiroPath) })
    $procsFallback = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue `
        | Where-Object { (-not $_.ExecutablePath) -and $_.CommandLine -and ($_.CommandLine -like "*$kiroPath*") })

    # Union + de-dupe by ProcessId.
    $byPid = @{}
    foreach ($p in $procsPrimary)  { if ($p) { $byPid[[int]$p.ProcessId] = $p } }
    foreach ($p in $procsFallback) { if ($p) { $byPid[[int]$p.ProcessId] = $p } }

    # Refuse to signal our own pid or session ancestors (best-effort:
    # $PID is the wrapper; the parent shell's pid would be a deeper
    # caller — out of scope for the lightweight ancestry check).
    $ourPid = $PID
    $procs  = @($byPid.Values | Where-Object { [int]$_.ProcessId -ne $ourPid })

    if ($procs.Count -eq 0) { return }

    $pidList = ($procs | ForEach-Object { $_.ProcessId }) -join ' '
    Write-Host "otto-gw: reaping stray kiro-cli orphans: $pidList"

    # SIGTERM-equivalent: Stop-Process without -Force (cooperative).
    foreach ($p in $procs) {
        try { Stop-Process -Id ([int]$p.ProcessId) -ErrorAction SilentlyContinue } catch { }
    }
    Start-Sleep -Seconds 2

    # Re-scan; force-kill any survivors.
    $survivors = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue `
        | Where-Object {
            ($_.ExecutablePath -and ($_.ExecutablePath -ieq $kiroPath)) -or `
            ((-not $_.ExecutablePath) -and $_.CommandLine -and ($_.CommandLine -like "*$kiroPath*"))
        } | Where-Object { [int]$_.ProcessId -ne $ourPid })
    foreach ($s in $survivors) {
        try { Stop-Process -Id ([int]$s.ProcessId) -Force -ErrorAction SilentlyContinue } catch { }
    }
    Write-Host "otto-gw: kiro-cli orphans reaped"
}

function Stop-GatewayByName {
    param([string]$Reason)
    $name = [System.IO.Path]::GetFileNameWithoutExtension($BinPath)
    $procs = @(Get-Process -Name $name -ErrorAction SilentlyContinue)
    if ($procs.Count -eq 0) { return $false }
    Write-Host "otto-gateway: $Reason; stopping running process(es) by name"
    foreach ($p in $procs) {
        try { $p.Kill(); $p.WaitForExit(10000) | Out-Null } catch { }
    }
    Write-Host "otto-gateway stopped"
    Repair-KiroOrphans
    return $true
}

function Stop-Gateway {
    if (Test-Path $PidFile) {
        $storedPid = [int](Get-Content $PidFile -Raw)
        $proc = Get-Process -Id $storedPid -ErrorAction SilentlyContinue
        if ($proc) {
            $proc.Kill()
            $proc.WaitForExit(10000) | Out-Null  # wait up to 10s for clean exit
            Remove-Item $PidFile -ErrorAction SilentlyContinue
            Write-Host "otto-gateway stopped"
            Repair-KiroOrphans
            return
        }
        # Stale file: a live instance may still be running without it.
        Remove-Item $PidFile -ErrorAction SilentlyContinue
        if (Stop-GatewayByName 'stale PID') { return }
        Write-Host "otto-gateway: stopped (stale PID)"
        Repair-KiroOrphans
        return
    }
    # No PID file at all — try to match the running binary by name.
    if (Stop-GatewayByName 'no PID file') { return }
    Write-Error "otto-gateway is not running (no PID file)"
    exit 1
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
        # Invoke-RestMethod returns an already-parsed object (native, no jq).
        # Format a compact listing; embeddings intentionally omitted.
        $h = Invoke-RestMethod -Uri "$HealthUrl/health" -TimeoutSec 3
        $up = [int]$h.uptime_seconds
        $upStr = if ($up -ge 3600) { "{0}h {1}m {2}s" -f [int]($up/3600), [int](($up%3600)/60), ($up%60) }
                 elseif ($up -ge 60) { "{0}m {1}s" -f [int]($up/60), ($up%60) }
                 else { "{0}s" -f $up }
        Write-Host ("  status:   {0}" -f $h.status)
        Write-Host ("  version:  {0}" -f $h.version)
        Write-Host ("  uptime:   {0}" -f $upStr)
        Write-Host ("  pool:     size={0}, alive={1}, busy={2}" -f $h.pool.size, $h.pool.alive, $h.pool.busy)
        Write-Host ("  sessions: active={0}" -f $h.sessions.active)
    } catch {
        Write-Host "  (health check unreachable at $HealthUrl/health)"
    }
    # Feature-flag enablement comes from the admin snapshot, NOT /health (which
    # is D-12 byte-shape locked). The admin endpoint is auth-exempt. Wrapped in
    # its own try/catch so an unreachable admin endpoint does not blank out the
    # health lines already printed above. Invoke-RestMethod returns a parsed
    # object, so debug/chat_trace are read directly as booleans.
    try {
        $snap = Invoke-RestMethod -Uri "$HealthUrl/admin/api/snapshot" -TimeoutSec 3
        $dbg   = if ($snap.debug)      { 'on' } else { 'off' }
        $trace = if ($snap.chat_trace) { 'on' } else { 'off' }
        Write-Host ("  debug:      {0}" -f $dbg)
        Write-Host ("  chat-trace: {0}" -f $trace)
    } catch {
        # Best-effort: admin snapshot unreachable — skip the flag lines silently.
    }
}

function Restart-Gateway {
    # Best-effort stop, mirroring the POSIX 'stop || true; start': a gateway
    # that isn't running is fine — restart should still start it. When there's
    # no PID file we stop by name directly so Stop-Gateway's fatal no-PID 'exit'
    # can't abort the restart.
    if (Test-Path $PidFile) {
        Stop-Gateway
    } else {
        Stop-GatewayByName 'no PID file' | Out-Null
    }
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

function Read-DotEnvAsHashtable {
    # Returns @{ KEY = value; ... } for every KEY=value line in $Path,
    # including commented (# KEY=value) lines. Used by init -Force to
    # recover existing values from a prior install. Same parser shape as
    # Import-DotEnv, but no $env: mutation.
    param([Parameter(Mandatory)][string]$Path)
    $result = @{}
    if (-not (Test-Path $Path)) { return $result }
    Get-Content -LiteralPath $Path | ForEach-Object {
        $line = $_.TrimStart()
        if (-not $line) { return }
        if ($line.StartsWith('#')) {
            $line = $line.Substring(1).TrimStart()
            if (-not $line) { return }
        }
        if ($line -match '^\s*export\s+') { $line = $line -replace '^\s*export\s+', '' }
        if ($line -notmatch '=') { return }
        $key, $val = $line -split '=', 2
        $val = $val.Trim()
        if (($val.StartsWith('"') -and $val.EndsWith('"')) -or `
            ($val.StartsWith("'") -and $val.EndsWith("'"))) {
            $val = $val.Substring(1, $val.Length - 2)
        }
        $result[$key.Trim()] = $val
    }
    return $result
}

function Test-EnvKeyUncommented {
    # Returns $true when KEY= appears uncommented in $Path. Used to derive
    # auth_enabled / chat_trace_enabled state without conflating
    # "value present but disabled" with "value present and active".
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Key)
    if (-not (Test-Path $Path)) { return $false }
    $pattern = "^\s*(export\s+)?${Key}="
    return ((Select-String -Path $Path -Pattern $pattern -Quiet) -eq $true)
}

function Set-OverridesLine {
    # Mirror of bash set_overrides_line. Writes or updates KEY=Value in the
    # .otto-gw.overrides.env file. Contract:
    #   - Always writes UNCOMMENTED (absence == "not customized").
    #   - If FilePath does not exist, creates it with a header explaining
    #     the load order and the never-overwritten contract.
    #   - If KEY already exists (commented or not), rewrites the line in
    #     place. Otherwise appends KEY=Value at end.
    #   - Best-effort permission restriction (Windows doesn't have a 0600
    #     equivalent; we use the same ACL trick Invoke-Init uses).
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string]$Key,
        [Parameter(Mandatory)][string]$Value
    )
    if (-not (Test-Path -LiteralPath $FilePath)) {
        $ts = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
        $header = @(
            "# Generated by 'otto-gw init' / 'otto-gw migrate-to-overrides' on $ts",
            "# Operator customizations + secrets. Loaded AFTER .otto-gw.env, so values here WIN.",
            "# Safe to hand-edit. Will NEVER be overwritten by 'otto-gw upgrade-env'.",
            ""
        )
        Set-Content -LiteralPath $FilePath -Value $header -Encoding UTF8
        # Best-effort ACL hardening; same pattern as Invoke-Init.
        try {
            $acl = Get-Acl $FilePath
            $acl.SetAccessRuleProtection($true, $false)
            $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
                [System.Security.Principal.WindowsIdentity]::GetCurrent().Name,
                "FullControl", "Allow"
            )
            $acl.AddAccessRule($rule)
            Set-Acl $FilePath $acl
        } catch { }
    }
    $pattern = "^\s*#?\s*${Key}="
    $content = Get-Content -LiteralPath $FilePath
    if ($content | Where-Object { $_ -match $pattern }) {
        $updated = $content | ForEach-Object {
            if ($_ -match $pattern) { "${Key}=${Value}" } else { $_ }
        }
        Set-Content -LiteralPath $FilePath -Value $updated -Encoding UTF8
    } else {
        Add-Content -LiteralPath $FilePath -Value "${Key}=${Value}"
    }
}

function Set-EnvLine {
    # Rewrites the KEY= line in FilePath (in-place). If Commented is $true,
    # writes:  # KEY=Value. If $false, writes: KEY=Value.
    # Matches both commented and uncommented forms of the key, but only when
    # KEY= is the sole content of the line (not embedded in prose comments).
    param(
        [string]$FilePath,
        [string]$Key,
        [string]$Value,
        [bool]$Commented
    )
    $prefix  = if ($Commented) { '# ' } else { '' }
    $newLine = "${prefix}${Key}=${Value}"
    $content = Get-Content $FilePath
    $updated = $content | ForEach-Object {
        # Match only lines where KEY= is the sole assignment (no trailing prose).
        if ($_ -match "^\s*#?\s*${Key}=[^\s]*$") { $newLine }
        else { $_ }
    }
    Set-Content -Path $FilePath -Value $updated -Encoding UTF8
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

    # Resolve template — single source of truth for the key list. Honors
    # -Template if the operator overrode it.
    $templateFile = Resolve-TemplateFile
    if (-not $templateFile) {
        $sought = if ($Template) { $Template } `
                  elseif ($env:OTTO_TEMPLATE_FILE) { $env:OTTO_TEMPLATE_FILE } `
                  else { Join-Path $PSScriptRoot '.env.otto-gw.example' }
        Write-Error "ERROR: init template not found: $sought"
        Write-Error "       The file scripts\.env.otto-gw.example must ship alongside otto-gw.ps1."
        exit 1
    }

    # Pre-resolve overrides path so existing-value lookup can read from BOTH
    # dest AND overrides (overrides wins on conflict — matches runtime loader).
    $overridesDestPath = $OverridesDest
    if (-not $overridesDestPath) {
        $destDir0 = Split-Path -Parent $destPath
        $overridesDestPath = Join-Path $destDir0 '.otto-gw.overrides.env'
    }

    # Re-init detection: -Force on an existing dest. Parse existing values so
    # they can serve as prompt/non-interactive defaults (CLI flag wins;
    # existing-file value next; cold-start default last). Secrets reused
    # unless -RegenerateSecrets.
    $reinit = $false
    $existing = @{}
    $existingAuthOn = $null
    $existingChatTraceOn = $null
    $existingIdleTimeout = ''
    if ($Force -and (Test-Path $destPath)) {
        try {
            $existing = Read-DotEnvAsHashtable -Path $destPath
        } catch {
            Write-Error "ERROR: $destPath exists but could not be parsed: $_"
            exit 1
        }
        # Layer in overrides values: read overrides into a hashtable and
        # let those keys override dest-derived ones (matches runtime loader).
        if (Test-Path -LiteralPath $overridesDestPath) {
            try {
                $existingOverrides = Read-DotEnvAsHashtable -Path $overridesDestPath
                foreach ($k in $existingOverrides.Keys) {
                    if ($existingOverrides[$k]) { $existing[$k] = $existingOverrides[$k] }
                }
            } catch {
                Write-Warning "could not parse $overridesDestPath ; proceeding with dest values only"
            }
        }
        $reinit = $true
        # "Uncommented in EITHER file" semantics for the state-derivation keys.
        $existingAuthOn = (Test-EnvKeyUncommented -Path $destPath -Key 'AUTH_TOKEN') -or `
            ((Test-Path -LiteralPath $overridesDestPath) -and (Test-EnvKeyUncommented -Path $overridesDestPath -Key 'AUTH_TOKEN'))
        $chatUncommented = (Test-EnvKeyUncommented -Path $destPath -Key 'CHAT_TRACE') -or `
            ((Test-Path -LiteralPath $overridesDestPath) -and (Test-EnvKeyUncommented -Path $overridesDestPath -Key 'CHAT_TRACE'))
        if ($chatUncommented) {
            $existingChatTraceOn = ($existing['CHAT_TRACE'] -match '^(true|1|yes)$')
        } else {
            $existingChatTraceOn = $false
        }
        # STREAM_IDLE_TIMEOUT_SEC only counts as "operator-set" when it's
        # uncommented in EITHER file. Without this gate the commented "30"
        # default in the template would be parsed and then written back as
        # uncommented on re-init (Rule-1 bug fixed in Task 3 bash side).
        $idleUncommented = (Test-EnvKeyUncommented -Path $destPath -Key 'STREAM_IDLE_TIMEOUT_SEC') -or `
            ((Test-Path -LiteralPath $overridesDestPath) -and (Test-EnvKeyUncommented -Path $overridesDestPath -Key 'STREAM_IDLE_TIMEOUT_SEC'))
        if ($idleUncommented) {
            $existingIdleTimeout = $existing['STREAM_IDLE_TIMEOUT_SEC']
        } else {
            $existingIdleTimeout = ''
        }
        Write-Host "re-init detected: preserving existing values where unchanged"
        if ($RegenerateSecrets) {
            Write-Host "regenerating AUTH_TOKEN and PII_HASH_KEY (existing values discarded)"
        } else {
            Write-Host "(use -RegenerateSecrets to mint new AUTH_TOKEN / PII_HASH_KEY)"
        }
    }

    # Generate / preserve secrets — flag > existing (re-init, unless
    # -RegenerateSecrets) > fresh random.
    $authTokenPreserved = $false
    $hashKeyPreserved   = $false
    if ($AuthToken) {
        $authTokenValue = $AuthToken
    } elseif ($reinit -and -not $RegenerateSecrets -and $existing.ContainsKey('AUTH_TOKEN') -and $existing['AUTH_TOKEN']) {
        $authTokenValue     = $existing['AUTH_TOKEN']
        $authTokenPreserved = $true
    } else {
        $authTokenValue = New-RandomHex 32
    }
    if ($HashKey) {
        $hashKeyValue = $HashKey
    } elseif ($reinit -and -not $RegenerateSecrets -and $existing.ContainsKey('PII_HASH_KEY') -and $existing['PII_HASH_KEY']) {
        $hashKeyValue     = $existing['PII_HASH_KEY']
        $hashKeyPreserved = $true
    } else {
        $hashKeyValue = New-RandomHex 32
    }

    # Resolve KIRO_CMD -- flag > existing > prompt with Get-Command suggestion.
    $kiroValue = $Kiro
    if (-not $kiroValue) {
        if ($existing.ContainsKey('KIRO_CMD') -and $existing['KIRO_CMD']) {
            if ($NonInteractive) {
                $kiroValue = $existing['KIRO_CMD']
            } else {
                $entered   = Read-Host "  kiro-cli path [$($existing['KIRO_CMD'])]"
                $kiroValue = if ($entered) { $entered } else { $existing['KIRO_CMD'] }
            }
        } elseif (-not $NonInteractive) {
            $cmd = Get-Command kiro-cli -ErrorAction SilentlyContinue
            $kiroDefault = if ($cmd) { $cmd.Source } else { $null }
            $prompt = if ($kiroDefault) { "  kiro-cli path [$kiroDefault]" } else { "  kiro-cli path [press Enter to leave unset]" }
            $entered = Read-Host $prompt
            $kiroValue = if ($entered) { $entered } else { $kiroDefault }
        }
    }

    # Resolve HTTP_ADDR -- flag > existing > prompt > default.
    $addrValue = $Addr
    if (-not $addrValue) {
        if ($existing.ContainsKey('HTTP_ADDR') -and $existing['HTTP_ADDR']) {
            if ($NonInteractive) {
                $addrValue = $existing['HTTP_ADDR']
            } else {
                $entered   = Read-Host "  HTTP_ADDR [$($existing['HTTP_ADDR'])]"
                $addrValue = if ($entered) { $entered } else { $existing['HTTP_ADDR'] }
            }
        } elseif (-not $NonInteractive) {
            Write-Host "  HTTP listen address -- default 127.0.0.1:18080 (safe, no collision)."
            Write-Host "  Set to 127.0.0.1:11434 if migrating from the Node Ollama proxy."
            $entered = Read-Host "  HTTP_ADDR [127.0.0.1:18080]"
            $addrValue = if ($entered) { $entered } else { "127.0.0.1:18080" }
        }
    }
    if (-not $addrValue) { $addrValue = "127.0.0.1:18080" }

    # Resolve PII mode -- flag > existing > prompt > "off".
    # Two-knob model: a disabled file is "off" regardless of the stored mode,
    # so re-init preserves "off" instead of resurrecting the placeholder mode
    # we write when disabled.
    $existingPiiMode = $null
    if ($existing.ContainsKey('PII_REDACTION_MODE') -and $existing['PII_REDACTION_MODE']) {
        $existingPiiMode = $existing['PII_REDACTION_MODE']
    }
    if ($existing.ContainsKey('PII_REDACTION_ENABLED') -and $existing['PII_REDACTION_ENABLED'] -match '^(false|FALSE|False|0)$') {
        $existingPiiMode = 'off'
    }
    $piiValue = $Pii
    if (-not $piiValue) {
        if ($existingPiiMode) {
            if ($NonInteractive) {
                $piiValue = $existingPiiMode
            } else {
                $entered  = Read-Host "  PII mode [$existingPiiMode]"
                $piiValue = if ($entered) { $entered } else { $existingPiiMode }
            }
        } elseif (-not $NonInteractive) {
            Write-Host "  PII redaction -- off | replace | mask | hash | drop."
            Write-Host "  'hash' (default) correlates values across logs; 'replace' is human-readable; 'off' disables."
            $entered = Read-Host "  PII mode [hash]"
            $piiValue = if ($entered) { $entered } else { "hash" }
        }
    }
    # Default is hash: redaction ON with per-install HMAC tags so the same
    # value correlates across log lines (PII_HASH_KEY is auto-generated above).
    if (-not $piiValue) { $piiValue = "hash" }
    $piiEnabled = if ($piiValue -eq "off") { "false" } else { "true" }
    # PII_REDACTION_MODE must be a valid mode (replace|mask|hash|drop) even when
    # redaction is disabled -- config.Load validates it unconditionally. "off"
    # is expressed via PII_REDACTION_ENABLED=false, so write a harmless valid
    # placeholder for the mode when off.
    $piiModeValue = if ($piiValue -eq "off") { "replace" } else { $piiValue }

    # Resolve AUTH state -- flag > existing-file state (uncommented = on) > prompt > off.
    $authOn = $AuthEnabled.IsPresent -or ($AuthToken -ne $null -and $AuthToken -ne "")
    if (-not $authOn -and $existingAuthOn -ne $null) {
        $authOn = $existingAuthOn
    } elseif (-not $authOn -and -not $NonInteractive) {
        Write-Host "  Bearer-token auth -- when enabled, every request must carry"
        Write-Host "  'Authorization: Bearer <token>'. Off is fine for local/laptop use."
        $entered = Read-Host "  Enable auth? [y/N]"
        if ($entered -match '^(y|yes)$') { $authOn = $true }
    }
    # Resolve CHAT_TRACE state -- flag > existing > prompt > off.
    $chatOn = $ChatTrace.IsPresent
    if (-not $chatOn -and $existingChatTraceOn -ne $null) {
        $chatOn = $existingChatTraceOn
    } elseif (-not $chatOn -and -not $NonInteractive) {
        Write-Host "  Chat-trace -- records raw user content (pre-redaction) to a"
        Write-Host "  separate chat-trace.log for debugging. Sensitive: 0600 mode,"
        Write-Host "  3-day default retention. Off is fine for normal use."
        $entered = Read-Host "  Enable chat-trace? [y/N]"
        if ($entered -match '^(y|yes)$') { $chatOn = $true }
    }
    $chatValue = if ($chatOn) { "true" } else { "false" }

    # Quick 260531-t8a — resolve STREAM_IDLE_TIMEOUT_SEC. Precedence:
    # -IdleTimeout flag (>= 0) > existing-file value > default "30" commented.
    # CLI/existing values are written uncommented (operator tuning preserved);
    # the default stays commented so it's discoverable without overriding
    # the binary fallback.
    if ($IdleTimeout -ge 0) {
        $idleValue = $IdleTimeout.ToString()
        $idleCommented = $false
    } elseif (-not [string]::IsNullOrEmpty($existingIdleTimeout)) {
        $idleValue = $existingIdleTimeout
        $idleCommented = $false
    } else {
        $idleValue = "30"
        $idleCommented = $true
    }

    # Mirror bash: when chat-trace is enabled, ChatTraceHook must be in
    # ENABLED_HOOKS or chain.Filter strips it. Prepend to preserve the
    # "first in Pre" invariant. config.Load also enforces this at runtime,
    # but writing the accurate list to disk keeps the file honest.
    $enabledHooksValue = if ($chatOn) {
        "ChatTraceHook,RequestIDHook,AuthHook,PIIRedactionHook,LoggingHook"
    } else {
        "RequestIDHook,AuthHook,PIIRedactionHook,LoggingHook"
    }

    # Ensure parent dir exists.
    $destDir = Split-Path -Parent $destPath
    if ($destDir -and -not (Test-Path $destDir)) {
        New-Item -ItemType Directory -Force -Path $destDir | Out-Null
    }

    # Two-file model contract (260531-tl1 CONTEXT.md Decisions 1 + 5):
    #   - .otto-gw.env is ALWAYS a byte-for-byte template copy. No
    #     Set-EnvLine writes happen against it.
    #   - .otto-gw.overrides.env carries every operator customization
    #     (uncommented). Secrets always; other knobs only when set.

    # Pre-flight migration: re-init on a legacy single-file install. If
    # -Force AND dest exists AND overrides does NOT exist AND dest has
    # uncommented operator values ⇒ run a silent migration BEFORE the
    # normal init flow. Emits one INFO line so the auto-migrate is never
    # invisible.
    if ($reinit -and -not (Test-Path -LiteralPath $overridesDestPath)) {
        $preMigrateNeeded = $false
        foreach ($k in @('AUTH_TOKEN', 'KIRO_CMD', 'PII_HASH_KEY', 'HTTP_ADDR')) {
            if (Test-EnvKeyUncommented -Path $destPath -Key $k) { $preMigrateNeeded = $true; break }
        }
        if ($preMigrateNeeded) {
            Write-Host "note: detected pre-overrides install -- migrating to overrides model"
            $tsPre = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
            $preBackup = "${destPath}.pre-migrate.${tsPre}"
            Copy-Item -LiteralPath $destPath -Destination $preBackup -Force
            foreach ($pk in (Get-EnvKeysPresent -Path $destPath)) {
                if (-not (Test-EnvKeyUncommented -Path $destPath -Key $pk)) { continue }
                $pv = if ($existing.ContainsKey($pk)) { $existing[$pk] } else { '' }
                $pd = Get-DefaultValue -Path $templateFile -Key $pk
                if ($pd -eq $null) { $pd = '<MISSING>' }
                if ($pv -ne $pd) {
                    Set-OverridesLine -FilePath $overridesDestPath -Key $pk -Value $pv
                }
            }
            # Always preserve the secrets across the auto-migration.
            foreach ($sk in @('AUTH_TOKEN', 'PII_HASH_KEY')) {
                if (Test-EnvKeyUncommented -Path $destPath -Key $sk) {
                    $sv = if ($existing.ContainsKey($sk)) { $existing[$sk] } else { '' }
                    if ($sv) { Set-OverridesLine -FilePath $overridesDestPath -Key $sk -Value $sv }
                }
            }
            Write-Host "  pre-init backup: $preBackup"
            Write-Host "  overrides:       $overridesDestPath"
        }
    }

    # Step 1: regenerate dest as a byte-for-byte template copy.
    Copy-Item -LiteralPath $templateFile -Destination $destPath -Force

    # Step 2: write each operator customization to the overrides file.
    # Rules mirror the bash side (Q3 + Decision 5):
    #   - PII_HASH_KEY ALWAYS lands in overrides.
    #   - AUTH_TOKEN lands when auth is enabled.
    #   - PII_REDACTION_ENABLED + PII_REDACTION_MODE always (operator decisions
    #     even at defaults).
    #   - Other knobs (HTTP_ADDR, KIRO_CMD, CHAT_TRACE, ENABLED_HOOKS,
    #     STREAM_IDLE_TIMEOUT_SEC) only when set to non-default / non-empty.
    if ($authOn) {
        Set-OverridesLine -FilePath $overridesDestPath -Key 'AUTH_TOKEN' -Value $authTokenValue
    }
    Set-OverridesLine -FilePath $overridesDestPath -Key 'PII_HASH_KEY'          -Value $hashKeyValue
    Set-OverridesLine -FilePath $overridesDestPath -Key 'PII_REDACTION_ENABLED' -Value $piiEnabled
    Set-OverridesLine -FilePath $overridesDestPath -Key 'PII_REDACTION_MODE'    -Value $piiModeValue
    if ($kiroValue) {
        Set-OverridesLine -FilePath $overridesDestPath -Key 'KIRO_CMD' -Value $kiroValue
    }
    if ($addrValue) {
        Set-OverridesLine -FilePath $overridesDestPath -Key 'HTTP_ADDR' -Value $addrValue
    }
    if ($chatOn) {
        Set-OverridesLine -FilePath $overridesDestPath -Key 'CHAT_TRACE'    -Value $chatValue
        Set-OverridesLine -FilePath $overridesDestPath -Key 'ENABLED_HOOKS' -Value $enabledHooksValue
    }
    if (-not $idleCommented) {
        Set-OverridesLine -FilePath $overridesDestPath -Key 'STREAM_IDLE_TIMEOUT_SEC' -Value $idleValue
    }

    Write-Host "✓ wrote $destPath                  (template copy)"
    Write-Host "✓ wrote $overridesDestPath  (operator config + secrets)"
    if ($authOn) {
        if ($authTokenPreserved) {
            Write-Host "  AUTH:          enabled (token preserved from prior install)"
        } else {
            Write-Host "  AUTH:          enabled (AUTH_TOKEN=$($authTokenValue.Substring(0,8))… in overrides)"
        }
    } else {
        Write-Host "  AUTH:          disabled (no AUTH_TOKEN in overrides; commented placeholder in .otto-gw.env)"
    }
    if ($hashKeyPreserved) {
        Write-Host "  PII_HASH_KEY:  preserved (existing key reused)"
    } else {
        Write-Host "  PII_HASH_KEY:  $($hashKeyValue.Substring(0,8))…(generated)"
    }
    if ($kiroValue) { Write-Host "  KIRO_CMD:      $kiroValue" } else { Write-Host "  KIRO_CMD:      (unset -- chat will 503 until you set it)" }
    Write-Host "  HTTP_ADDR:     $addrValue"
    Write-Host "  PII:           $piiValue"
    if ($chatOn) {
        Write-Host "  CHAT_TRACE:    enabled (raw content to chat-trace.log -- sensitive)"
    } else {
        Write-Host "  CHAT_TRACE:    disabled"
    }
    Write-Host ""
    Write-Host "Next: .\scripts\otto-gw.ps1 start"
}

function Invoke-UpgradeEnv {
    # Mirror of upgrade_env_cmd in scripts/otto-gw. Regenerates the
    # generated .otto-gw.env from the latest .env.otto-gw.example template,
    # reporting which keys would be added / orphaned / left unchanged.
    # Operator customizations live in .otto-gw.overrides.env (loaded last by
    # Initialize-Config) and are NEVER touched here.
    $templatePath = Resolve-TemplateFile
    if (-not $templatePath) {
        $sought = if ($Template) { $Template } `
                  elseif ($env:OTTO_TEMPLATE_FILE) { $env:OTTO_TEMPLATE_FILE } `
                  else { Join-Path $PSScriptRoot '.env.otto-gw.example' }
        Write-Error "upgrade-env template not found. Looked at: $sought"
        exit 1
    }
    # -Dest overrides the resolved .otto-gw.env path; cold-init fallback
    # is project-local (first entry in $DefaultEnvPaths).
    $destPath = $Dest
    if (-not $destPath) { $destPath = Resolve-EnvFile }
    if (-not $destPath) { $destPath = $DefaultEnvPaths[0] }

    # Build sorted unique key sets for the diff. Compare-Object is the
    # PowerShell idiom for set ops; SideIndicator => is right-only,
    # <= is left-only, == is both.
    $tKeys = @(Get-TemplateKeys -Path $templatePath | Sort-Object -Unique)
    $cKeys = if (Test-Path -LiteralPath $destPath) {
        @(Get-EnvKeysPresent -Path $destPath | Sort-Object -Unique)
    } else { @() }

    $added = @()
    $orphaned = @()
    $unchanged = @()
    if ($tKeys.Count -gt 0 -or $cKeys.Count -gt 0) {
        $diff = Compare-Object -ReferenceObject $tKeys -DifferenceObject $cKeys -IncludeEqual
        foreach ($d in $diff) {
            switch ($d.SideIndicator) {
                '<=' { $added += $d.InputObject }      # in template, not in dest
                '=>' { $orphaned += $d.InputObject }   # in dest, not in template
                '==' { $unchanged += $d.InputObject }
            }
        }
    }

    Write-Host "otto-gw upgrade-env:"
    Write-Host "  template: $templatePath"
    Write-Host "  dest:     $destPath"
    if ($added.Count -gt 0) {
        Write-Host ("  added:     {0} ({1})" -f $added.Count, ($added -join ' '))
    } else { Write-Host "  added:     0" }
    if ($orphaned.Count -gt 0) {
        Write-Host ("  orphaned:  {0} ({1})" -f $orphaned.Count, ($orphaned -join ' '))
    } else { Write-Host "  orphaned:  0" }
    Write-Host ("  unchanged: {0}" -f $unchanged.Count)

    if ($DryRun) {
        Write-Host "(dry-run; nothing written)"
        return
    }

    # Confirm overwrite (skip when -Yes). A non-existent dest is a cold
    # init — nothing to destroy, no confirm needed.
    if (-not $Yes -and (Test-Path -LiteralPath $destPath)) {
        $reply = Read-Host "Overwrite $destPath with current template? [y/N]"
        if ($reply -notmatch '^(y|yes)$') {
            Write-Error "cancelled"
            exit 1
        }
    }

    # Orphan log default is per-user; $env:OTTO_UPGRADE_LOG overrides for
    # CI installs without USERPROFILE. Never silently swallow the log —
    # fall back to project-local on miss.
    $upgradeLog = $null
    if ($env:OTTO_UPGRADE_LOG) {
        $upgradeLog = $env:OTTO_UPGRADE_LOG
    } elseif ($env:USERPROFILE) {
        $upgradeLog = Join-Path $env:USERPROFILE '.otto-gw.upgrade.log'
    } else {
        $upgradeLog = '.\.otto-gw.upgrade.log'
    }
    if ($orphaned.Count -gt 0) {
        $ts = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
        $existingMap = Read-DotEnvAsHashtable -Path $destPath
        $lines = New-Object System.Collections.Generic.List[string]
        [void]$lines.Add("# $ts upgrade-env removed orphaned keys from $destPath")
        foreach ($k in $orphaned) {
            $v = if ($existingMap.ContainsKey($k)) { $existingMap[$k] } else { '<unset>' }
            [void]$lines.Add("$k=$v")
        }
        [void]$lines.Add("# ---")
        Add-Content -LiteralPath $upgradeLog -Value $lines
        Write-Host "  orphan values logged to: $upgradeLog"
    }

    # Byte-for-byte template copy. Copy-Item is generally faithful on
    # Windows; if line endings ever drift we can switch to
    # Set-Content -Value (Get-Content $template -Raw) -NoNewline.
    Copy-Item -LiteralPath $templatePath -Destination $destPath -Force
    Write-Host "✓ wrote $destPath"
}

function Invoke-MigrateToOverrides {
    # Mirror of bash migrate_to_overrides_cmd. One-time migration from the
    # legacy single-file model: extract every uncommented key in .otto-gw.env
    # whose value differs from the template default, write those keys to
    # .otto-gw.overrides.env, back up the original dest, regenerate dest from
    # the template (pure copy under the new contract).
    #
    # Idempotency (locked in 260531-tl1 CONTEXT.md Decision 4): re-running on
    # an already-migrated install is a no-op (overrides exists AND dest is
    # byte-identical to the template ⇒ nothing to do).
    $templatePath = Resolve-TemplateFile
    if (-not $templatePath) {
        $sought = if ($Template) { $Template } `
                  elseif ($env:OTTO_TEMPLATE_FILE) { $env:OTTO_TEMPLATE_FILE } `
                  else { Join-Path $PSScriptRoot '.env.otto-gw.example' }
        Write-Error "migrate-to-overrides template not found. Looked at: $sought"
        exit 1
    }
    $destPath = $Dest
    if (-not $destPath) { $destPath = Resolve-EnvFile }
    if (-not $destPath -or -not (Test-Path -LiteralPath $destPath)) {
        Write-Error "migrate-to-overrides: no .otto-gw.env found at '$destPath'. Run 'otto-gw.ps1 init' first, or pass -Dest PATH explicitly."
        exit 1
    }
    $overridesDestPath = $OverridesDest
    if (-not $overridesDestPath) {
        $destDir = Split-Path -Parent $destPath
        $overridesDestPath = Join-Path $destDir '.otto-gw.overrides.env'
    }

    # Idempotency: overrides exists AND dest matches template byte-for-byte.
    if ((Test-Path -LiteralPath $overridesDestPath)) {
        $destHash     = (Get-FileHash -LiteralPath $destPath -Algorithm SHA256).Hash
        $templateHash = (Get-FileHash -LiteralPath $templatePath -Algorithm SHA256).Hash
        if ($destHash -eq $templateHash) {
            Write-Host "already migrated (no-op)"
            Write-Host "  dest:      $destPath"
            Write-Host "  overrides: $overridesDestPath"
            return
        }
    }

    # Build the migration list. Every uncommented key in dest whose value
    # differs from the template default goes to overrides.
    $migrations = New-Object System.Collections.Generic.List[string]
    $destMap     = Read-DotEnvAsHashtable -Path $destPath
    foreach ($k in (Get-EnvKeysPresent -Path $destPath)) {
        if (-not (Test-EnvKeyUncommented -Path $destPath -Key $k)) { continue }
        $cur = if ($destMap.ContainsKey($k)) { $destMap[$k] } else { '' }
        $def = Get-DefaultValue -Path $templatePath -Key $k
        if ($def -eq $null) { $def = '<MISSING>' }
        if ($cur -ne $def) {
            if (-not $migrations.Contains($k)) { [void]$migrations.Add($k) }
        }
    }
    # Always carry secrets across when uncommented in source, regardless of
    # whether the value coincidentally matches the template placeholder.
    foreach ($k in @('AUTH_TOKEN', 'PII_HASH_KEY')) {
        if (Test-EnvKeyUncommented -Path $destPath -Key $k) {
            if (-not $migrations.Contains($k)) { [void]$migrations.Add($k) }
        }
    }

    if ($migrations.Count -eq 0) {
        Write-Host "migrate-to-overrides: nothing to migrate (no operator-set keys differ from template defaults)."
        Write-Host "  dest:      $destPath"
        Write-Host "  overrides: $overridesDestPath"
        return
    }

    Write-Host "otto-gw migrate-to-overrides:"
    Write-Host "  dest:      $destPath"
    Write-Host "  overrides: $overridesDestPath"
    Write-Host ("  would migrate: {0}" -f ($migrations -join ' '))

    if ($DryRun) {
        Write-Host "(dry-run; no changes written)"
        return
    }

    if (-not $Yes) {
        $reply = Read-Host "Backup $destPath and regenerate from template? [y/N]"
        if ($reply -notmatch '^(y|yes)$') {
            Write-Error "cancelled"
            exit 1
        }
    }

    # Backup BEFORE any disk mutation so a hard interrupt leaves the operator
    # with both files intact + a clear recovery path.
    $ts = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
    $backupPath = "${destPath}.pre-migrate.${ts}"
    Copy-Item -LiteralPath $destPath -Destination $backupPath -Force
    Write-Host "  backup: $backupPath"

    # Write migrations to overrides.
    foreach ($k in $migrations) {
        $v = if ($destMap.ContainsKey($k)) { $destMap[$k] } else { '' }
        Set-OverridesLine -FilePath $overridesDestPath -Key $k -Value $v
    }

    # Regenerate dest as a pure template copy.
    Copy-Item -LiteralPath $templatePath -Destination $destPath -Force

    Write-Host ("✓ migrated {0} key(s) to {1}" -f $migrations.Count, $overridesDestPath)
    Write-Host "✓ regenerated $destPath from template"
    Write-Host "✓ backup at  $backupPath"
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
  stop                Stop background gateway (also reaps any stray $env:KIRO_CMD subprocesses)
  status              Show gateway status and health
  restart [flags]     Stop then start (re-applies flags / .env)
  logs                Tail both stdout and stderr log files
  run [flags]         Run gateway in foreground
  env [-ShowSecrets]  Print the resolved gateway env that would be passed
  upgrade-env         Regenerate .otto-gw.env from the latest .env.otto-gw.example
                      template. Operator customizations in .otto-gw.overrides.env
                      are NEVER touched. -DryRun shows the added / orphaned /
                      unchanged keys without writing.
  migrate-to-overrides
                      One-time migration from the legacy single-file model:
                      extract non-default values from .otto-gw.env into
                      .otto-gw.overrides.env, back up the original, then
                      regenerate .otto-gw.env from the template. Idempotent.
  version             Print the gateway binary version (delegates to bin\otto-gateway --version)

Gateway config flags (for start | restart | run | env):
  -Pii MODE           off | replace | mask | hash | drop
  -HashKey KEY        PII_HASH_KEY (required when -Pii hash)
  -Entities LIST      PII_ENABLED_ENTITIES (comma list)
  -Hooks LIST         ENABLED_HOOKS allowlist (comma list, empty = all)
  -Auth TOKEN         AUTH_TOKEN
  -Debug              DEBUG=true (debug-level logging) for start | restart | run
  -Trace              DEBUG=true + CHAT_TRACE=true (debug + chat-trace NDJSON) for start | restart | run
  -EnvFile PATH       Override the default .env search
  -OverridesFile PATH Override the default .otto-gw.overrides.env search

upgrade-env / migrate-to-overrides flags:
  -Template PATH      Override the .env.otto-gw.example resolution
  -Dest PATH          Override the resolved .otto-gw.env target
  -OverridesDest PATH (migrate-to-overrides) override the resolved overrides
                      target (default: sibling of -Dest)
  -DryRun             Print added / orphaned / unchanged keys; write nothing
  -Yes                Skip the overwrite confirmation prompt (CI-friendly)

init flags (for the 'init' subcommand):
  -Dest PATH          where to write the .env (default `$env:USERPROFILE\.otto-gw.env)
  -Here               shortcut for -Dest .\.env.otto-gw
  -Force              overwrite if dest exists (on re-init: existing values
                      preserved as defaults; secrets reused unchanged unless
                      -RegenerateSecrets)
  -Kiro PATH          skip the KIRO_CMD prompt
  -Addr ADDR          skip the HTTP_ADDR prompt (default 127.0.0.1:18080)
  -Pii MODE           skip the PII prompt (default off)
  -AuthEnabled        enable bearer-token auth (default off; AUTH_TOKEN line
                      pregenerated but commented when disabled)
  -AuthToken TOK      use TOK instead of generating (implies -AuthEnabled)
  -ChatTrace          enable chat-trace NDJSON tracer (default off; records
                      raw user content -- sensitive)
  -RegenerateSecrets  on re-init, mint fresh AUTH_TOKEN + PII_HASH_KEY.
                      Default preserves existing values to avoid invalidating
                      clients / breaking hash-mode log correlation.
  -HashKey KEY        use KEY instead of generating
  -NonInteractive     don't prompt; use defaults for unspecified values

.env auto-load search (loaded FIRST):
  1. -EnvFile PATH                    (CLI override)
  2. `$env:OTTO_ENV_FILE              (env override)
  3. .\.env.otto-gw                   (project-local)
  4. `$env:USERPROFILE\.otto-gw.env   (per-user)

.otto-gw.overrides.env auto-load search (loaded SECOND; values win on conflict):
  1. -OverridesFile PATH              (CLI override)
  2. `$env:OTTO_OVERRIDES_FILE        (env override)
  3. .\.otto-gw.overrides.env         (project-local)
  4. `$env:USERPROFILE\.otto-gw.overrides.env (per-user)

Precedence (highest first):
  CLI flag → .otto-gw.overrides.env → .otto-gw.env → inherited shell env.

See scripts\.env.otto-gw.example for a starter template.
"@ | Write-Host
    exit 1
}

switch ($Command) {
    "init"             { Invoke-Init }
    "start"            { Start-Gateway }
    "stop"             { Stop-Gateway }
    "status"           { Get-GatewayStatus }
    "restart"          { Restart-Gateway }
    "logs"             { Get-Logs }
    "run"              { Invoke-Run }
    "env"              { Show-Env }
    "upgrade-env"      { Invoke-UpgradeEnv }
    "migrate-to-overrides" { Invoke-MigrateToOverrides }
    "version"          { Show-Version }
    default            { Show-Usage }
}
