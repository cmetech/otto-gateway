#Requires -Version 5.1
# scripts/otto-gw.ps1 - PowerShell lifecycle manager for otto-gateway on Windows.
# Renamed from scripts/otto.ps1 to avoid collision with the otto CLI binary.
#
# Subcommands:
#   start | stop | status | restart | logs | run | env
#
# Wrapper env overrides (where logs/pids/binary live):
#   $env:OTTO_BIN, $env:OTTO_PID, $env:OTTO_LOG, $env:OTTO_ADDR
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
    [switch]$Follow
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$BinPath    = if ($env:OTTO_BIN)    { $env:OTTO_BIN }    else { ".\bin\otto-gateway.exe" }
$PidFile    = if ($env:OTTO_PID)    { $env:OTTO_PID }    else { "$env:TEMP\otto-gateway.pid" }
$LogFile    = if ($env:OTTO_LOG)    { $env:OTTO_LOG }    else { "$env:TEMP\otto-gateway.log" }
# stdout and stderr MUST be separate files: Start-Process cannot redirect both to the same file.
$LogErrFile = if ($env:OTTO_LOGERR) { $env:OTTO_LOGERR } else { "$env:TEMP\otto-gateway-err.log" }
$Addr       = if ($env:OTTO_ADDR)   { $env:OTTO_ADDR }   else { "http://localhost:18080" }

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
    }
    Apply-CliFlags
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
    # -NoNewWindow: prevents a console popup (Pitfall 8 in RESEARCH.md).
    # -PassThru: returns the Process object so we can capture its PID.
    $proc = Start-Process `
        -FilePath $BinPath `
        -RedirectStandardOutput $LogFile `
        -RedirectStandardError $LogErrFile `
        -NoNewWindow `
        -PassThru
    $proc.Id | Set-Content $PidFile
    Write-Host "otto-gateway started (PID $($proc.Id))"
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
    # Tail BOTH stdout and stderr simultaneously (Gemini MEDIUM fix / consensus).
    # Start-Process cannot redirect stdout and stderr to the same file, so they
    # are in separate files. Use background jobs to tail both concurrently.
    $j1 = Start-Job { Get-Content -Wait $using:LogFile }
    $j2 = Start-Job { Get-Content -Wait $using:LogErrFile }
    try {
        Wait-Job $j1, $j2 | Receive-Job -Wait
    } finally {
        Remove-Job $j1, $j2 -Force
    }
}

function Invoke-Run {
    Initialize-Config
    # Foreground execution — inherits all env vars from the calling shell.
    & $BinPath
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
    "start"   { Start-Gateway }
    "stop"    { Stop-Gateway }
    "status"  { Get-GatewayStatus }
    "restart" { Restart-Gateway }
    "logs"    { Get-Logs }
    "run"     { Invoke-Run }
    "env"     { Show-Env }
    default   { Show-Usage }
}
