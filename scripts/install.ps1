#Requires -Version 5.1
<#
  scripts/install.ps1 — one-liner installer for OTTO Gateway (Windows).
    irm https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.ps1 | iex

  Env overrides:
    OTTO_HOME      install dir            (default $env:USERPROFILE\.otto-gw)
    OTTO_VERSION   release tag            (default: latest GitHub release)
    OTTO_BASE_URL  release asset base     (default GitHub releases download URL)
    OTTO_API_URL   latest-release API URL (default GitHub API)
#>
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo     = 'cmetech/otto-gateway'
$BaseUrl  = if ($env:OTTO_BASE_URL) { $env:OTTO_BASE_URL } else { "https://github.com/$Repo/releases/download" }
$ApiUrl   = if ($env:OTTO_API_URL)  { $env:OTTO_API_URL }  else { "https://api.github.com/repos/$Repo/releases/latest" }
$OttoHome = if ($env:OTTO_HOME)     { $env:OTTO_HOME }     else { Join-Path $env:USERPROFILE '.otto-gw' }

function Info($m) { Write-Host "  $m" }
function Ok($m)   { Write-Host "[ok] $m" -ForegroundColor Green }
function Warn($m) { Write-Warning $m }
function Die($m)  { Write-Error $m; exit 1 }

# Arch — only amd64 Windows builds are published today.
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -ne 'AMD64') { Die "unsupported arch '$arch'. Only amd64 Windows builds are published." }
$platform = 'windows-amd64'

# Version.
if ($env:OTTO_VERSION) {
    $version = $env:OTTO_VERSION
} else {
    $rel = Invoke-RestMethod -UseBasicParsing -Uri $ApiUrl
    $version = if ($rel.PSObject.Properties['tag_name']) { $rel.tag_name } else { $null }
    if (-not $version) { Die "could not resolve latest release from $ApiUrl (set OTTO_VERSION to override)." }
}

$archive = "otto_gateway-$platform-$version.zip"
$sums    = "SHA256SUMS-$version.txt"
Write-Host "Installing OTTO Gateway $version ($platform) -> $OttoHome`n"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("otto-install-" + [guid]::NewGuid())
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

    $bat = Join-Path $OttoHome 'scripts\otto-gw.bat'
    if (Test-Path $bat) {
        Info "Stopping running gateway (if any) ..."
        # Best-effort, like the POSIX 'stop ... || true'. The wrapper writes to
        # stderr when there's no PID file; under $ErrorActionPreference='Stop'
        # that surfaces as a terminating NativeCommandError, so swallow it.
        try { & $bat stop 2>&1 | Out-Null } catch { }
        $global:LASTEXITCODE = 0
    }

    # Expand-Archive has no strip-components: expand to temp, move inner folder up.
    Info "Extracting to $OttoHome ..."
    $exdir = Join-Path $tmp 'extract'
    Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $exdir -Force
    $inner = Join-Path $exdir 'otto_gateway'
    New-Item -ItemType Directory -Path $OttoHome -Force | Out-Null
    Copy-Item -Path (Join-Path $inner '*') -Destination $OttoHome -Recurse -Force

    # The two setup.bat jobs: strip Mark-of-the-Web, then make CurrentUser policy permissive.
    Get-ChildItem -Path $OttoHome -Recurse -File | Unblock-File
    $permissive = @('RemoteSigned','Unrestricted','Bypass')
    if ($permissive -notcontains (Get-ExecutionPolicy)) {
        try {
            Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force
            Ok "set CurrentUser execution policy to RemoteSigned"
        } catch {
            Warn "could not set execution policy (Group Policy?). The otto-gw.bat command bypasses this per-invocation."
        }
    }

    if (Get-Command kiro-cli -ErrorAction SilentlyContinue) {
        Ok "kiro-cli found at $((Get-Command kiro-cli).Source)"
    } else {
        Warn "kiro-cli not found on PATH. The gateway returns 503 on chat requests until it is installed (or set KIRO_CMD in your .env)."
    }

    $userEnv = Join-Path $env:USERPROFILE '.otto-gw.env'
    $projEnv = Join-Path $OttoHome '.env.otto-gw'
    if ((Test-Path $userEnv) -or (Test-Path $projEnv)) {
        Info "Existing config found — preserving it (skipping init)."
    } else {
        Info "Writing default config (no auth, 127.0.0.1:18080, all hooks, PII redaction=hash, chat-trace off) ..."
        & $bat init -NonInteractive
    }

    # PATH: expose otto-gw.bat (PATHEXT prioritizes .bat over .ps1, so 'otto-gw' resolves to the dispatcher).
    $scripts = Join-Path $OttoHome 'scripts'
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $userPath) { $userPath = '' }
    if (($userPath -split ';') -notcontains $scripts) {
        $newPath = if ($userPath) { $userPath.TrimEnd(';') + ';' + $scripts } else { $scripts }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Ok "added $scripts to your user PATH (open a new terminal to pick it up)"
    }

    Write-Host ""
    Ok "OTTO Gateway $version installed to $OttoHome"
    Write-Host "`nNext steps (new terminal):"
    Write-Host "  otto-gw start     # launch the gateway"
    Write-Host "  otto-gw status    # verify it is up"
    Write-Host "  Invoke-RestMethod http://127.0.0.1:18080/health"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
