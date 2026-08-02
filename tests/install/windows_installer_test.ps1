$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$Installer = Join-Path $RepoRoot 'scripts\install.ps1'
$FixtureRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('gw-windows-installer-' + [guid]::NewGuid().ToString('N'))
$ArchiveRoot = Join-Path $FixtureRoot 'archive-root'
$ReleaseRoot = Join-Path $FixtureRoot 'vtest'
$ArchivePath = Join-Path $ReleaseRoot 'otto_gateway-windows-amd64-vtest.zip'
$ChecksumPath = Join-Path $ReleaseRoot 'SHA256SUMS-vtest.txt'
$InstallDir = Join-Path $FixtureRoot 'install'
$GwHome = Join-Path $FixtureRoot 'home'
$OutputPath = Join-Path $FixtureRoot 'installer.out'
$InitMarkerPath = Join-Path $FixtureRoot 'init.called'
$priorUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$server = $null

function Fail-With([string]$Message) { throw "FAIL: $Message" }

$installerBytes = [System.IO.File]::ReadAllBytes($Installer)
$installerText = (New-Object System.Text.UTF8Encoding($false, $true)).GetString($installerBytes)
$installerFirstLine = ($installerText -split "`r?`n", 2)[0]
try {
    Invoke-Expression $installerFirstLine
} catch {
    Fail-With "downloaded installer cannot be piped to Invoke-Expression: $($_.Exception.Message)"
}
Write-Host 'ok: downloaded installer first line is Invoke-Expression compatible'

New-Item -ItemType Directory -Path (Join-Path $ArchiveRoot 'otto_gateway\scripts') -Force | Out-Null
New-Item -ItemType Directory -Path $ReleaseRoot -Force | Out-Null
New-Item -ItemType Directory -Path $GwHome -Force | Out-Null
@'
@echo off
if /I "%~1"=="init" (
  type nul > "%GW_TEST_INIT_MARKER%"
  exit /b 23
)
exit /b 0
'@ | Set-Content -LiteralPath (Join-Path $ArchiveRoot 'otto_gateway\scripts\gw.bat') -Encoding ASCII
'HTTP_ADDR=127.0.0.1:18080' | Set-Content -LiteralPath (Join-Path $GwHome '.env') -Encoding ASCII
Compress-Archive -Path (Join-Path $ArchiveRoot 'otto_gateway') -DestinationPath $ArchivePath
$archiveHash = (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
"$archiveHash  $(Split-Path -Leaf $ArchivePath)" | Set-Content -LiteralPath $ChecksumPath -Encoding ASCII

try {
    $python = Get-Command python.exe -ErrorAction SilentlyContinue
    if (-not $python) { $python = Get-Command python -ErrorAction Stop }
    $portProbe = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $portProbe.Start()
    $port = ([System.Net.IPEndPoint]$portProbe.LocalEndpoint).Port
    $portProbe.Stop()
    $server = Start-Process -FilePath $python.Source -ArgumentList @(
        '-m', 'http.server', "$port", '--bind', '127.0.0.1', '--directory', $FixtureRoot
    ) -WindowStyle Hidden -PassThru
    $ready = $false
    for ($attempt = 0; $attempt -lt 50; $attempt++) {
        if ($server.HasExited) { Fail-With 'local fixture HTTP server exited before the installer ran' }
        $client = New-Object System.Net.Sockets.TcpClient
        try {
            $client.Connect('127.0.0.1', $port)
            $ready = $true
            break
        } catch {
            Start-Sleep -Milliseconds 100
        } finally {
            $client.Dispose()
        }
    }
    if (-not $ready) { Fail-With 'local fixture HTTP server did not become ready' }

    # Keep the test installer from changing the real user PATH if the broken
    # implementation continues after the injected init failure.
    $scriptsPath = Join-Path $InstallDir 'scripts'
    $testUserPath = if ($priorUserPath) { $priorUserPath.TrimEnd(';') + ';' + $scriptsPath } else { $scriptsPath }
    [Environment]::SetEnvironmentVariable('Path', $testUserPath, 'User')

    $prior = @{
        GW_VERSION = $env:GW_VERSION
        GW_BASE_URL = $env:GW_BASE_URL
        GW_INSTALL_DIR = $env:GW_INSTALL_DIR
        GW_HOME = $env:GW_HOME
        GW_TEST_INIT_MARKER = $env:GW_TEST_INIT_MARKER
    }
    try {
        $env:GW_VERSION = 'vtest'
        $env:GW_BASE_URL = "http://127.0.0.1:$port"
        $env:GW_INSTALL_DIR = $InstallDir
        $env:GW_HOME = $GwHome
        $env:GW_TEST_INIT_MARKER = $InitMarkerPath
        # A failing child writes to stderr. Capture that output for the
        # assertions without allowing this test process's Stop preference to
        # turn the native stderr record into a premature test failure.
        $savedErrorActionPreference = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            $output = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $Installer 2>&1 | Out-String
            $exitCode = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $savedErrorActionPreference
        }
        [System.IO.File]::WriteAllText($OutputPath, $output, (New-Object System.Text.UTF8Encoding($false)))
    } finally {
        foreach ($entry in $prior.GetEnumerator()) {
            [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
        }
    }

    if (-not (Test-Path -LiteralPath $InitMarkerPath)) {
        Fail-With "fixture did not reach gw init; installer output:`n$output"
    }
    if ($exitCode -eq 0) { Fail-With 'installer returned success after gw init exited 23' }
    if ($output -match '\[ok\]\s+Gateway vtest installed') {
        Fail-With 'installer reported success after gw init failed'
    }
    if ($output -notmatch 'config initialization failed.*exit code 23') {
        Fail-With "installer did not report the gw init failure; output:`n$output"
    }
    Write-Host 'PASS: Windows installer fails closed when generated config initialization fails'
} finally {
    if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue }
    [Environment]::SetEnvironmentVariable('Path', $priorUserPath, 'User')
    Remove-Item -LiteralPath $FixtureRoot -Recurse -Force -ErrorAction SilentlyContinue
}
