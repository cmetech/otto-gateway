#Requires -Version 5.1
# scripts/otto.ps1 - PowerShell lifecycle manager for otto-gateway on Windows.
# Subcommands: start | stop | status | restart | logs | run
# Env overrides: $env:OTTO_BIN, $env:OTTO_PID, $env:OTTO_LOG, $env:OTTO_ADDR

param([string]$Command = "help")

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$BinPath    = if ($env:OTTO_BIN)  { $env:OTTO_BIN }  else { ".\bin\otto-gateway.exe" }
$PidFile    = if ($env:OTTO_PID)  { $env:OTTO_PID }  else { "$env:TEMP\otto-gateway.pid" }
$LogFile    = if ($env:OTTO_LOG)  { $env:OTTO_LOG }  else { "$env:TEMP\otto-gateway.log" }
# stdout and stderr MUST be separate files: Start-Process cannot redirect both to the same file.
$LogErrFile = if ($env:OTTO_LOGERR) { $env:OTTO_LOGERR } else { "$env:TEMP\otto-gateway-err.log" }
$Addr       = if ($env:OTTO_ADDR) { $env:OTTO_ADDR } else { "http://localhost:11435" }

function Start-Gateway {
    if (Test-Path $PidFile) {
        $existingPid = [int](Get-Content $PidFile -Raw)
        if (Get-Process -Id $existingPid -ErrorAction SilentlyContinue) {
            Write-Error "otto-gateway is already running (PID $existingPid)"
            exit 1
        }
        Remove-Item $PidFile
    }
    # Gateway env vars are inherited from the current environment
    # automatically — Start-Process inherits parent env by default.
    # Documented set (Plan 02-06 wrapper expansion):
    #   KIRO_CMD, KIRO_ARGS, KIRO_CWD     — kiro-cli subprocess wiring
    #   DEBUG, HTTP_ADDR, PING_INTERVAL  — runtime knobs
    #   AUTH_TOKEN, ALLOWED_IPS           — auth + IP allowlist (Phase 2)
    #   AUTH_TRUST_XFF                    — opt-in XFF trust (Codex H-7)
    #   POOL_SIZE                         — warm-pool size (Phase 2)
    #   OLLAMA_PATH_PREFIX                — Ollama route prefix (default /api)
    #   OPENAI_PATH_PREFIX                — OpenAI route prefix (Phase 3 forward seam)
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
    # Foreground execution — inherits all env vars from the calling shell.
    & $BinPath
}

function Show-Usage {
    Write-Host "Usage: .\scripts\otto.ps1 <command>"
    Write-Host ""
    Write-Host "Commands:"
    Write-Host "  start     Start gateway in background"
    Write-Host "  stop      Stop background gateway"
    Write-Host "  status    Show gateway status and health"
    Write-Host "  restart   Stop then start"
    Write-Host "  logs      Tail both stdout and stderr log files"
    Write-Host "  run       Run gateway in foreground"
    exit 1
}

switch ($Command) {
    "start"   { Start-Gateway }
    "stop"    { Stop-Gateway }
    "status"  { Get-GatewayStatus }
    "restart" { Restart-Gateway }
    "logs"    { Get-Logs }
    "run"     { Invoke-Run }
    default   { Show-Usage }
}
