#Requires -Version 5.1
<#
  scripts/install.ps1 — one-liner installer for Gateway (Windows).
    irm https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.ps1 | iex

  Layout (post de-brand relayout):
    GW_INSTALL_DIR  code (binaries, scripts) — replaceable on upgrade.
      default $env:LOCALAPPDATA\Gateway
    GW_HOME         config (.env, logs, state) — precious, never overwritten.
      default $env:USERPROFILE\.gw

  Env overrides:
    GW_INSTALL_DIR install dir             (default $env:LOCALAPPDATA\Gateway)
    GW_HOME        config dir              (default $env:USERPROFILE\.gw)
    GW_VERSION     release tag             (default: latest GitHub release)
    GW_BASE_URL    release asset base      (default GitHub releases download URL)
    GW_API_URL     latest-release API URL  (default GitHub API)
#>
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo       = 'cmetech/otto-gateway'
$BaseUrl    = if ($env:GW_BASE_URL)     { $env:GW_BASE_URL }     else { "https://github.com/$Repo/releases/download" }
$ApiUrl     = if ($env:GW_API_URL)      { $env:GW_API_URL }      else { "https://api.github.com/repos/$Repo/releases/latest" }
$GwHome     = if ($env:GW_HOME)         { $env:GW_HOME }         else { Join-Path $env:USERPROFILE '.gw' }
$InstallDir = if ($env:GW_INSTALL_DIR)  { $env:GW_INSTALL_DIR }  else { Join-Path $env:LOCALAPPDATA 'Gateway' }

function Info($m) { Write-Host "  $m" }
function Ok($m)   { Write-Host "[ok] $m" -ForegroundColor Green }
function Warn($m) { Write-Warning $m }
function Die($m)  { Write-Error $m; exit 1 }

# Arch — only amd64 Windows builds are published today.
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -ne 'AMD64') { Die "unsupported arch '$arch'. Only amd64 Windows builds are published." }
$platform = 'windows-amd64'

# Version.
if ($env:GW_VERSION) {
    $version = $env:GW_VERSION
} else {
    $rel = Invoke-RestMethod -UseBasicParsing -Uri $ApiUrl
    $version = if ($rel.PSObject.Properties['tag_name']) { $rel.tag_name } else { $null }
    if (-not $version) { Die "could not resolve latest release from $ApiUrl (set GW_VERSION to override)." }
}

$archive = "otto_gateway-$platform-$version.zip"
$sums    = "SHA256SUMS-$version.txt"
Write-Host "Installing Gateway $version ($platform) -> $InstallDir`n"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("gateway-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
    Info "Downloading $archive ..."
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/$version/$archive" -OutFile (Join-Path $tmp $archive)
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/$version/$sums"    -OutFile (Join-Path $tmp $sums)

    Info "Verifying checksum ..."
    $expected = Select-String -Path (Join-Path $tmp $sums) -Pattern ([regex]::Escape($archive)) |
                ForEach-Object { ($_.Line -split '\s+')[0] } | Select-Object -First 1
    if (-not $expected) { Die "no checksum row for $archive in $sums." }
    $actual = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $archive)).Hash.ToLower()
    if ($expected.ToLower() -ne $actual) { Die "checksum mismatch for $archive (expected $expected, got $actual)." }
    Ok "checksum verified"

    $bat = Join-Path $InstallDir 'scripts\gw.bat'
    if (Test-Path $bat) {
        Info "Stopping running gateway (if any) ..."
        # Best-effort, like the POSIX 'stop ... || true'. The wrapper writes to
        # stderr when there's no PID file; under $ErrorActionPreference='Stop'
        # that surfaces as a terminating NativeCommandError, so swallow it.
        try { & $bat stop 2>&1 | Out-Null } catch { }
        $global:LASTEXITCODE = 0
    }

    # Stop a running gateway-tray before extraction — the .exe is locked by
    # the running process otherwise and Copy-Item fails with "in use".
    Info "Stopping running tray (if any) ..."
    Get-Process gateway-tray -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 500

    # Expand-Archive has no strip-components: expand to temp, move inner folder up.
    Info "Extracting to $InstallDir ..."
    $exdir = Join-Path $tmp 'extract'
    Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $exdir -Force
    $inner = Join-Path $exdir 'otto_gateway'
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Copy-Item -Path (Join-Path $inner '*') -Destination $InstallDir -Recurse -Force

    # The two setup.bat jobs: strip Mark-of-the-Web, then make CurrentUser policy permissive.
    Get-ChildItem -Path $InstallDir -Recurse -File | Unblock-File
    $permissive = @('RemoteSigned','Unrestricted','Bypass')
    if ($permissive -notcontains (Get-ExecutionPolicy)) {
        try {
            Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force
            Ok "set CurrentUser execution policy to RemoteSigned"
        } catch {
            Warn "could not set execution policy (Group Policy?). The gw.bat command bypasses this per-invocation."
        }
    }

    # Run the config migration right after extraction (so the shipped
    # lib/migrate.ps1 is available to dot-source) and before the
    # config-existence check just below, so a migrated $GwHome\.env is
    # already in place when we decide whether `gw init` needs to run.
    # Mirrors install.sh's migrate_legacy_config: try a repo-local copy
    # first (present when this script is run from a checked-out repo),
    # falling back to the copy inside the just-extracted install dir
    # (present on every real release install). Either miss is silently
    # tolerated — migration is advisory only, never required for a
    # fresh install.
    if ($PSScriptRoot) {
        $localMigrate = Join-Path $PSScriptRoot 'lib\migrate.ps1'
        if (Test-Path -LiteralPath $localMigrate) {
            try { . $localMigrate } catch { }
        }
    }
    $extractedMigrate = Join-Path $InstallDir 'scripts\lib\migrate.ps1'
    if (Test-Path -LiteralPath $extractedMigrate) {
        try { . $extractedMigrate } catch { }
    }
    if (Get-Command Invoke-GwMigration -ErrorAction SilentlyContinue) {
        Invoke-GwMigration
    }

    if (Get-Command kiro-cli -ErrorAction SilentlyContinue) {
        Ok "kiro-cli found at $((Get-Command kiro-cli).Source)"
    } else {
        Warn "kiro-cli not found on PATH. The gateway returns 503 on chat requests until it is installed (or set KIRO_CMD in your .env)."
    }

    $preservedEnv = $false
    if (Test-Path (Join-Path $GwHome '.env')) {
        Info "Existing config found — preserving it (skipping init)."
        $preservedEnv = $true
    } else {
        Info "Writing default config (no auth, 127.0.0.1:18080, all hooks, PII redaction=encrypt, NER=on, chat-trace off) ..."
        & $bat init -NonInteractive
    }

    # PATH: expose gw.bat (PATHEXT prioritizes .bat over .ps1, so 'gw' resolves to the dispatcher).
    $scripts = Join-Path $InstallDir 'scripts'
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $userPath) { $userPath = '' }
    $pathEntries = $userPath -split ';'

    # Legacy cleanup: drop any stale `.otto-gw\scripts` PATH entry —
    # superseded by $scripts above. Matches case-insensitively (Windows
    # PATH semantics) and by suffix so it works regardless of which
    # legacy install dir the entry pointed at.
    $legacyEntries = $pathEntries | Where-Object { $_ -and ($_ -like '*\.otto-gw\scripts*') }
    if ($legacyEntries) {
        $pathEntries = $pathEntries | Where-Object { -not ($_ -and ($_ -like '*\.otto-gw\scripts*')) }
        $userPath = ($pathEntries -join ';').Trim(';')
        [Environment]::SetEnvironmentVariable('Path', $userPath, 'User')
        Ok "removed legacy $($legacyEntries -join ', ') from your user PATH"
        $pathEntries = $userPath -split ';'
    }

    if ($pathEntries -notcontains $scripts) {
        $newPath = if ($userPath) { $userPath.TrimEnd(';') + ';' + $scripts } else { $scripts }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Ok "added $scripts to your user PATH (open a new terminal to pick it up)"
    }

    # Legacy cleanup: remove the old OttoTray HKCU Run-key autostart entry —
    # superseded by the GatewayTray value the tray itself registers. Autostart
    # is opt-in via the tray, so we do not install a replacement here — just
    # unset the stale one so operators upgrading from an OTTO Gateway install
    # don't end up with a dead Run entry pointing at a binary path that no
    # longer exists.
    $runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
    try {
        $legacyRun = Get-ItemProperty -Path $runKey -Name 'OttoTray' -ErrorAction SilentlyContinue
        if ($legacyRun -and $legacyRun.PSObject.Properties['OttoTray']) {
            Remove-ItemProperty -Path $runKey -Name 'OttoTray' -ErrorAction SilentlyContinue
            Ok "removed legacy OttoTray autostart entry"
        }
    } catch { }

    Write-Host ""
    Ok "Gateway $version installed to $InstallDir"
    Write-Host "`nNext steps (new terminal):"
    if ($preservedEnv) {
        Write-Host "  gw upgrade-env -DryRun   # check for new env keys this build added (then run without -DryRun to apply)"
    }
    Write-Host "  gw start     # launch the gateway"
    Write-Host "  gw status    # verify it is up"
    $trayExe = Join-Path $InstallDir 'bin\gateway-tray.exe'
    if (Test-Path $trayExe) {
        Write-Host "  Start-Process `"$trayExe`"   # or, launch the tray app"
    }
    Write-Host "  Invoke-RestMethod http://127.0.0.1:18080/health"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
