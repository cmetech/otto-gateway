#Requires -Version 5.1
# scripts/loop24.ps1 - PowerShell lifecycle manager for loop24-gateway on Windows.
# Subcommands: start | stop | status | restart | logs | run
# Env overrides: $env:LOOP24_BIN, $env:LOOP24_PID, $env:LOOP24_LOG, $env:LOOP24_ADDR

param([string]$Command = "help")

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$BinPath    = if ($env:LOOP24_BIN)  { $env:LOOP24_BIN }  else { ".\bin\loop24-gateway.exe" }
$PidFile    = if ($env:LOOP24_PID)  { $env:LOOP24_PID }  else { "$env:TEMP\loop24-gateway.pid" }
$LogFile    = if ($env:LOOP24_LOG)  { $env:LOOP24_LOG }  else { "$env:TEMP\loop24-gateway.log" }
# stdout and stderr MUST be separate files: Start-Process cannot redirect both to the same file.
$LogErrFile = if ($env:LOOP24_LOGERR) { $env:LOOP24_LOGERR } else { "$env:TEMP\loop24-gateway-err.log" }
$Addr       = if ($env:LOOP24_ADDR) { $env:LOOP24_ADDR } else { "http://localhost:11434" }

function Start-Gateway {
    if (Test-Path $PidFile) {
        $existingPid = [int](Get-Content $PidFile -Raw)
        if (Get-Process -Id $existingPid -ErrorAction SilentlyContinue) {
            Write-Error "loop24-gateway is already running (PID $existingPid)"
            exit 1
        }
        Remove-Item $PidFile
    }
    # Gateway env vars (KIRO_CMD, KIRO_ARGS, etc.) are inherited from the current
    # environment automatically — Start-Process inherits parent env by default.
    # -NoNewWindow: prevents a console popup (Pitfall 8 in RESEARCH.md).
    # -PassThru: returns the Process object so we can capture its PID.
    $proc = Start-Process `
        -FilePath $BinPath `
        -RedirectStandardOutput $LogFile `
        -RedirectStandardError $LogErrFile `
        -NoNewWindow `
        -PassThru
    $proc.Id | Set-Content $PidFile
    Write-Host "loop24-gateway started (PID $($proc.Id))"
}

function Stop-Gateway {
    if (-not (Test-Path $PidFile)) {
        Write-Error "loop24-gateway is not running (no PID file)"
        exit 1
    }
    $storedPid = [int](Get-Content $PidFile -Raw)
    $proc = Get-Process -Id $storedPid -ErrorAction SilentlyContinue
    if (-not $proc) {
        Write-Host "loop24-gateway: stopped (stale PID)"
        Remove-Item $PidFile -ErrorAction SilentlyContinue
        exit 0
    }
    $proc.Kill()
    $proc.WaitForExit(10000) | Out-Null  # wait up to 10s for clean exit
    Remove-Item $PidFile -ErrorAction SilentlyContinue
    Write-Host "loop24-gateway stopped"
}

function Get-GatewayStatus {
    if (-not (Test-Path $PidFile)) {
        Write-Host "loop24-gateway: stopped"
        exit 1
    }
    $storedPid = [int](Get-Content $PidFile -Raw)
    $proc = Get-Process -Id $storedPid -ErrorAction SilentlyContinue
    if (-not $proc) {
        Write-Host "loop24-gateway: stopped (stale PID)"
        exit 1
    }
    Write-Host "loop24-gateway: running (PID $storedPid)"
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
    Write-Host "Usage: .\scripts\loop24.ps1 <command>"
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
