# Finding ID: T-6
# REL-* ID: REL-TRAY-06
# Target phase: 14 (verify) / 16 (fix)
# Target OS: Windows
# Expected pre-fix behavior: tray's revealBundle opens the wrapper's first stdout line (e.g. 'loaded env file: ...') instead of the .zip archive path
# Expected post-fix behavior: tray opens the actual .zip archive at the last non-empty stdout line
# Run instructions: 1) Set GW_ENV_FILE to a real .env file path (or ensure GW_HOME points to a real install). 2) Run 'scripts/gw.ps1 support' and capture stdout. 3) Inspect the first vs last non-empty lines to see the path discrepancy.

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Continue'

# Resolve paths
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = Resolve-Path (Join-Path $ScriptDir '..\..\..') -ErrorAction Stop
$Wrapper = Join-Path $RepoRoot 'scripts\gw.ps1'

if (-not (Test-Path $Wrapper)) {
    Write-Error "FATAL: wrapper not found at $Wrapper"
    exit 1
}

Write-Host ""
Write-Host "REL-TRAY-06 reproducer — Windows bundle path stdout pollution"
Write-Host "================================================================"
Write-Host "Wrapper: $Wrapper"
Write-Host ""

# Run the wrapper and capture all stdout lines
$stdoutFile = Join-Path $env:TEMP 'rel-tray-06-stdout.txt'
$stderrFile = Join-Path $env:TEMP 'rel-tray-06-stderr.txt'

$exitCode = 0
try {
    $psExe = if (Get-Command pwsh -ErrorAction SilentlyContinue) { 'pwsh' } else { 'powershell.exe' }
    $proc = Start-Process -FilePath $psExe `
        -ArgumentList @('-NoProfile', '-File', $Wrapper, 'support') `
        -Wait -PassThru -NoNewWindow `
        -RedirectStandardOutput $stdoutFile `
        -RedirectStandardError  $stderrFile `
        -ErrorAction Stop
    $exitCode = $proc.ExitCode
} catch {
    Write-Host "ERROR running wrapper: $_"
    exit 1
}

$allLines = if (Test-Path $stdoutFile) { Get-Content $stdoutFile -ErrorAction SilentlyContinue } else { @() }
$nonEmptyLines = $allLines | Where-Object { $_.Trim() -ne '' }

Write-Host "Exit code: $exitCode"
Write-Host ""
Write-Host "All stdout lines ($($allLines.Count) total, $($nonEmptyLines.Count) non-empty):"
$allLines | ForEach-Object { Write-Host "  [$($allLines.IndexOf($_))] $_" }
Write-Host ""

if ($nonEmptyLines.Count -eq 0) {
    Write-Host "INCONCLUSIVE: no stdout output"
    exit 0
}

$firstNonEmpty = $nonEmptyLines | Select-Object -First 1
$lastNonEmpty  = $nonEmptyLines | Select-Object -Last 1

Write-Host "First non-empty stdout line: [$firstNonEmpty]"
Write-Host "Last  non-empty stdout line: [$lastNonEmpty]"
Write-Host ""

# tray.go:296 uses strings.TrimSpace(res.Stdout) = entire stdout trimmed as one string.
# On Windows this includes all Write-Host chatter before the final bundle path.
$trayWouldOpen = ($allLines -join "`n").Trim()
$firstLineOfWhatTrayOpens = ($trayWouldOpen -split "`n")[0].Trim()

Write-Host "What tray.go:296 would use (TrimSpace of all stdout):"
Write-Host "  First line: [$firstLineOfWhatTrayOpens]"
Write-Host "  (tray passes this whole string to revealBundle / open)"
Write-Host ""

# Determine pre-fix vs post-fix
if ($firstNonEmpty -ne $lastNonEmpty -and $firstNonEmpty -notmatch '\.zip$|\.tar\.gz$') {
    Write-Host "VERDICT: PRE-FIX CONFIRMED — first stdout line is NOT the archive path"
    Write-Host "  First line (chatter): [$firstNonEmpty]"
    Write-Host "  Last  line (path):    [$lastNonEmpty]"
    Write-Host "  tray.go:296 would pass the chatter as the path to revealBundle"
} elseif ($lastNonEmpty -match '\.zip$|\.tar\.gz$') {
    Write-Host "VERDICT: POST-FIX CONFIRMED OR SINGLE LINE — last non-empty line is the archive path"
    Write-Host "  Archive path: [$lastNonEmpty]"
    if ($firstNonEmpty -eq $lastNonEmpty) {
        Write-Host "  (only one non-empty line — no pollution observed in this run)"
    }
} else {
    Write-Host "VERDICT: AMBIGUOUS — inspect lines above manually"
}
