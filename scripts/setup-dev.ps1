#Requires -Version 5.1
# Gateway — Windows dev environment bootstrap.
# Idempotent: safe to re-run. Skips already-installed tools.
# See DEVELOPERS.md for the manual equivalent and gotchas.
#
# Does NOT require admin elevation. winget and scoop both install per-user.
# Does NOT change the execution policy — if PowerShell blocks the script,
# invoke as: powershell -ExecutionPolicy Bypass -File .\scripts\setup-dev.ps1

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

Write-Host 'Gateway — Windows dev environment bootstrap'
Write-Host '(idempotent — safe to re-run)'
Write-Host ''

# --- Pre-flight: detect package manager ---------------------------------------

$pm = $null
if (Get-Command winget -ErrorAction SilentlyContinue) {
    $pm = 'winget'
} elseif (Get-Command scoop -ErrorAction SilentlyContinue) {
    $pm = 'scoop'
} else {
    Write-Error @'
No supported package manager found.
Install winget (ships with Windows 10/11) or scoop (https://scoop.sh),
then re-run this script. See DEVELOPERS.md → Manual setup for the
fully manual path.
'@
    exit 1
}

Write-Host ("[info] package manager: {0}" -f $pm)
Write-Host ''

# --- Install-Tool helper ------------------------------------------------------
# Probes for the tool via Get-Command. If present, prints [skip] + version.
# Otherwise prints [install] and installs via:
#   1. winget (if $pm is 'winget' and $WingetId is non-empty)
#   2. scoop  (if $pm is 'scoop'  and $ScoopName is non-empty)
#   3. `go install $GoInstallPath` as a fallback (requires Go to be installed)
#   4. Hard error pointing at DEVELOPERS.md otherwise.

function Install-Tool {
    param(
        [Parameter(Mandatory)] [string] $ToolName,
        [string] $WingetId = '',
        [string] $ScoopName = '',
        [Parameter(Mandatory)] [scriptblock] $VersionProbe,
        [string] $GoInstallPath = ''
    )

    if (Get-Command $ToolName -ErrorAction SilentlyContinue) {
        $versionOutput = & $VersionProbe 2>&1 | Select-Object -First 1
        Write-Host ("[skip] {0} already installed: {1}" -f $ToolName, $versionOutput)
        return
    }

    Write-Host ("[install] {0}" -f $ToolName)

    if ($pm -eq 'winget' -and $WingetId -ne '') {
        winget install --id $WingetId --silent --accept-package-agreements --accept-source-agreements
    } elseif ($pm -eq 'scoop' -and $ScoopName -ne '') {
        scoop install $ScoopName
    } elseif ($GoInstallPath -ne '') {
        if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
            Write-Error ("Cannot install {0} via 'go install' — Go is not on PATH. See DEVELOPERS.md → Manual setup." -f $ToolName)
            exit 1
        }
        go install $GoInstallPath
    } else {
        Write-Error ("No install path available for {0} on {1}. See DEVELOPERS.md → Manual setup." -f $ToolName, $pm)
        exit 1
    }
}

# --- Tool install order -------------------------------------------------------
# Go FIRST — later tools (gosec, gofumpt) may fall back to `go install`.

Install-Tool -ToolName 'go' `
    -WingetId 'GoLang.Go' `
    -ScoopName 'main/go' `
    -VersionProbe { go version }

Install-Tool -ToolName 'golangci-lint' `
    -WingetId 'golangci-lint.golangci-lint' `
    -ScoopName 'main/golangci-lint' `
    -VersionProbe { golangci-lint --version }

# pre-commit: no first-class winget or scoop package — use pip.
if (Get-Command pre-commit -ErrorAction SilentlyContinue) {
    Write-Host ("[skip] pre-commit already installed: {0}" -f (pre-commit --version))
} else {
    Write-Host '[install] pre-commit (via pip install --user)'
    if (-not (Get-Command pip -ErrorAction SilentlyContinue)) {
        Write-Error @'
pip is not on PATH. Install Python first:
    winget install --id Python.Python.3.12 --silent --accept-package-agreements --accept-source-agreements
then re-run this script. See DEVELOPERS.md → Manual setup.
'@
        exit 1
    }
    pip install --user pre-commit
}

Install-Tool -ToolName 'gosec' `
    -WingetId '' `
    -ScoopName 'main/gosec' `
    -VersionProbe { gosec --version } `
    -GoInstallPath 'github.com/securego/gosec/v2/cmd/gosec@latest'

Install-Tool -ToolName 'gofumpt' `
    -WingetId '' `
    -ScoopName 'main/gofumpt' `
    -VersionProbe { gofumpt --version } `
    -GoInstallPath 'mvdan.cc/gofumpt@latest'

Install-Tool -ToolName 'gitleaks' `
    -WingetId 'gitleaks.gitleaks' `
    -ScoopName 'main/gitleaks' `
    -VersionProbe { gitleaks version }

Install-Tool -ToolName 'shellcheck' `
    -WingetId 'koalaman.shellcheck' `
    -ScoopName 'main/shellcheck' `
    -VersionProbe { shellcheck --version }

# --- Versions summary ---------------------------------------------------------

Write-Host ''
Write-Host '==== Installed versions ===='
go version
golangci-lint --version
pre-commit --version
gosec --version
gofumpt --version
gitleaks version
shellcheck --version

# --- Next steps ---------------------------------------------------------------

Write-Host ''
Write-Host '==== Next steps ===='
Write-Host 'Run these yourself — this script does NOT auto-run them:'
Write-Host ''
Write-Host '  pre-commit install'
Write-Host '    wires the git pre-commit hook so lint/secret-scan/format'
Write-Host '    checks run on every commit.'
Write-Host ''
Write-Host '  make help'
Write-Host '    list available make targets. If `make` is missing, install via'
Write-Host '    `winget install ezwinports.make` or `scoop install main/make`.'
Write-Host ''
Write-Host '  make build; make test; make lint'
Write-Host '    verify the toolchain end-to-end (PowerShell uses ; not &&).'
Write-Host ''
Write-Host "Heads-up: on a fresh clone, the 'go mod tidy' pre-commit hook will"
Write-Host 'fail until the first dependency is added. See DEVELOPERS.md ->'
Write-Host 'Known issues for the workaround.'
