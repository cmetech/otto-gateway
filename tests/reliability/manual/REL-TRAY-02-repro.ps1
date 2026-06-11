# Finding ID: T-2
# REL-* ID: REL-TRAY-02
# Target phase: 14 (verify) / 15 (fix)
# Target OS: Windows
# Expected pre-fix behavior: scripts/otto-gw.ps1 support with gateway stopped exits 1 before creating bundle
# Expected post-fix behavior: bundle is created on disk with health/health.json containing unreachable: sentinel
# Run instructions: 1) Ensure gateway is stopped (scripts/otto-gw.ps1 stop). 2) Run this script from repo root or any directory. 3) Inspect $LASTEXITCODE and whether a bundle path was printed on stdout.

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Continue'

# Resolve the wrapper relative to this script's location (tests/reliability/manual/)
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = Resolve-Path (Join-Path $ScriptDir '..\..\..') -ErrorAction Stop
$Wrapper = Join-Path $RepoRoot 'scripts\otto-gw.ps1'

if (-not (Test-Path $Wrapper)) {
    Write-Error "FATAL: wrapper not found at $Wrapper — run from repo root"
    exit 1
}

Write-Host ""
Write-Host "REL-TRAY-02 reproducer — pre-fix: support exits 1 when gateway is down"
Write-Host "==========================================================================="
Write-Host "Wrapper: $Wrapper"
Write-Host ""

# Capture stdout + stderr; we want to see what the wrapper emits
$stdoutLines = @()
$stderrLines = @()
$exitCode = 0

try {
    $proc = Start-Process -FilePath (Get-Command pwsh -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Source -ErrorAction SilentlyContinue) `
        -ArgumentList @('-NoProfile', '-File', $Wrapper, 'support') `
        -Wait -PassThru -NoNewWindow `
        -RedirectStandardOutput "$env:TEMP\rel-tray-02-stdout.txt" `
        -RedirectStandardError  "$env:TEMP\rel-tray-02-stderr.txt" `
        -ErrorAction Stop
    $exitCode = $proc.ExitCode
} catch {
    # Fallback: powershell.exe (PS 5.x)
    $proc = Start-Process -FilePath 'powershell.exe' `
        -ArgumentList @('-NoProfile', '-File', $Wrapper, 'support') `
        -Wait -PassThru -NoNewWindow `
        -RedirectStandardOutput "$env:TEMP\rel-tray-02-stdout.txt" `
        -RedirectStandardError  "$env:TEMP\rel-tray-02-stderr.txt" `
        -ErrorAction Stop
    $exitCode = $proc.ExitCode
}

if (Test-Path "$env:TEMP\rel-tray-02-stdout.txt") {
    $stdoutLines = Get-Content "$env:TEMP\rel-tray-02-stdout.txt" -ErrorAction SilentlyContinue
}
if (Test-Path "$env:TEMP\rel-tray-02-stderr.txt") {
    $stderrLines = Get-Content "$env:TEMP\rel-tray-02-stderr.txt" -ErrorAction SilentlyContinue
}

Write-Host "Exit code: $exitCode"
Write-Host ""
Write-Host "Stdout lines ($($stdoutLines.Count)):"
$stdoutLines | ForEach-Object { Write-Host "  $_" }
Write-Host ""
Write-Host "Stderr lines ($($stderrLines.Count)):"
$stderrLines | ForEach-Object { Write-Host "  $_" }
Write-Host ""

# Determine verdict
if ($exitCode -ne 0) {
    Write-Host "VERDICT: PRE-FIX CONFIRMED — exit $exitCode when gateway is down (Get-GatewayStatus exit 1 aborted the run)"
    Write-Host "  Expected: exit 0 + bundle path on stdout"
    Write-Host "  Got: exit $exitCode + no bundle"
} else {
    $bundlePath = ($stdoutLines | Where-Object { $_.Trim() -ne '' } | Select-Object -Last 1)
    if ($bundlePath -and (Test-Path $bundlePath)) {
        Write-Host "VERDICT: POST-FIX CONFIRMED — bundle created at: $bundlePath"
        # Check for unreachable sentinel in health.json
        try {
            Add-Type -AssemblyName System.IO.Compression.FileSystem -ErrorAction SilentlyContinue
            $zip = [System.IO.Compression.ZipFile]::OpenRead($bundlePath)
            $healthEntry = $zip.Entries | Where-Object { $_.FullName -match 'health.json' } | Select-Object -First 1
            if ($healthEntry) {
                $reader = [System.IO.StreamReader]::new($healthEntry.Open())
                $content = $reader.ReadToEnd()
                $reader.Close()
                if ($content -match 'unreachable') {
                    Write-Host "  health.json contains 'unreachable:' sentinel — correct post-fix behavior"
                } else {
                    Write-Host "  WARNING: health.json does not contain 'unreachable:' sentinel"
                }
            }
            $zip.Dispose()
        } catch {
            Write-Host "  (could not inspect bundle zip: $_)"
        }
    } else {
        Write-Host "VERDICT: AMBIGUOUS — exit 0 but no bundle path on stdout"
    }
}
