# tests/scripts/test-support-bundle.ps1 — integration smoke for
# `otto-gw.ps1 support`. Mirrors tests/scripts/test-support-bundle.sh:
# builds a fake install root with a synthetic log file containing known
# secret literals, runs the subcommand, then asserts:
#   - exit 0
#   - bundle path printed on stdout
#   - zip contains MANIFEST.txt + the six required trees
#   - none of the synthetic secret literals appear in the extracted tree
#   - health/health.json contains the "unreachable:" sentinel
#
# Plain pwsh harness — no Pester dependency (design constraint).

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$RepoRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$Wrapper = Join-Path $RepoRoot 'scripts\otto-gw.ps1'
if (-not (Test-Path $Wrapper)) {
    Write-Error "FATAL: $Wrapper not found"
    exit 1
}

$script:Pass = 0
$script:Fail = 0
$script:FakeRoot = $null
$script:ExtractDir = $null

function Cleanup {
    if ($script:FakeRoot -and (Test-Path $script:FakeRoot)) {
        Remove-Item -Recurse -Force $script:FakeRoot -ErrorAction SilentlyContinue
    }
    if ($script:ExtractDir -and (Test-Path $script:ExtractDir)) {
        Remove-Item -Recurse -Force $script:ExtractDir -ErrorAction SilentlyContinue
    }
}

function Ok($Label) {
    $script:Pass++
    Write-Host "  ok: $Label"
}

function FailWith($Msg) {
    $script:Fail++
    Write-Host "FAIL: $Msg"
}

try {
    $SecretToken   = 'realsupersecretXYZ'
    $SecretBearer  = 'realtoken1234deadbeef'
    $SecretHash    = 'realHashKeyABC987'
    $SecretEncrypt = 'realEncryptKey555'

    $script:FakeRoot = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
    $null = New-Item -ItemType Directory -Path (Join-Path $script:FakeRoot 'logs') -Force
    $null = New-Item -ItemType Directory -Path (Join-Path $script:FakeRoot '.otto\gw') -Force
    $null = New-Item -ItemType Directory -Path (Join-Path $script:FakeRoot '.otto\tray') -Force

    @(
        '2026-06-08T00:00:00Z info gateway boot ok',
        "2026-06-08T00:00:01Z info AUTH_TOKEN=$SecretToken was loaded",
        "2026-06-08T00:00:02Z info Authorization: Bearer $SecretBearer on inbound",
        "2026-06-08T00:00:03Z info x-api-key: $SecretBearer on inbound",
        "2026-06-08T00:00:04Z info PII_HASH_KEY=$SecretHash active",
        "2026-06-08T00:00:05Z info PII_ENCRYPT_KEY=$SecretEncrypt active",
        '2026-06-08T00:00:06Z info routine traffic'
    ) | Set-Content -Path (Join-Path $script:FakeRoot 'logs\otto-gateway.log') -Encoding UTF8

    'boot ok'  | Set-Content -Path (Join-Path $script:FakeRoot 'logs\otto-gateway.boot-out.log')
    'boot err' | Set-Content -Path (Join-Path $script:FakeRoot 'logs\otto-gateway.boot-err.log')
    'running'  | Set-Content -Path (Join-Path $script:FakeRoot '.otto\tray\state')

    $script:ExtractDir = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
    $OutDir = Join-Path $script:ExtractDir 'out'
    $null = New-Item -ItemType Directory -Path $OutDir -Force

    # Set env overrides so all wrapper-resolved paths point at the fake root.
    $env:OTTO_INSTALL_ROOT = $script:FakeRoot
    $env:OTTO_BIN          = 'cmd.exe'  # exists on PATH but --version will fail -- exercises the error path
    $env:OTTO_STATE_DIR    = Join-Path $script:FakeRoot '.otto\gw'
    $env:OTTO_PID          = Join-Path $script:FakeRoot '.otto\gw\otto-gateway.pid'
    $env:OTTO_LOG          = Join-Path $script:FakeRoot 'logs\otto-gateway.log'
    $env:OTTO_ADDR         = 'http://127.0.0.1:1'  # unreachable on purpose
    $env:AUTH_TOKEN        = $SecretToken
    $env:PII_HASH_KEY      = $SecretHash
    $env:PII_ENCRYPT_KEY   = $SecretEncrypt
    $env:HTTP_ADDR         = '127.0.0.1:18080'
    $env:OTTO_ENV_FILE     = 'NUL'      # neutralize project-local .env discovery
    $env:OTTO_OVERRIDES_FILE = 'NUL'

    Write-Host "== running otto-gw.ps1 support =="
    $stdoutFile = [System.IO.Path]::GetTempFileName()
    $stderrFile = [System.IO.Path]::GetTempFileName()
    $proc = Start-Process -FilePath (Get-Command pwsh -ErrorAction SilentlyContinue).Source `
        -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-File',$Wrapper,'support','-Out',$OutDir) `
        -RedirectStandardOutput $stdoutFile -RedirectStandardError $stderrFile -PassThru -Wait -WindowStyle Hidden
    if (-not $proc) {
        # Fallback: pwsh not on PATH (test runner is Windows PowerShell 5)
        & powershell -NoProfile -ExecutionPolicy Bypass -File $Wrapper support -Out $OutDir `
            > $stdoutFile 2> $stderrFile
        $rc = $LASTEXITCODE
    } else {
        $rc = $proc.ExitCode
    }

    if ($rc -eq 0) { Ok 'support exit 0' } else { FailWith "support exit $rc (stderr: $(Get-Content $stderrFile -Raw))" }

    $bundlePath = (Get-Content $stdoutFile | Select-Object -Last 1).Trim()
    if ($bundlePath -and (Test-Path $bundlePath)) {
        Ok "bundle path printed and file exists: $bundlePath"
    } else {
        FailWith "bundle path missing or file does not exist: [$bundlePath]"
        Write-Host "passed: $script:Pass, failed: $script:Fail"
        exit 1
    }

    if (Test-Path (Join-Path $OutDir 'latest.zip')) { Ok 'latest.zip alias exists' } else { FailWith 'latest.zip alias missing' }

    # Extract and inspect.
    $ExTree = Join-Path $script:ExtractDir 'extracted'
    $null = New-Item -ItemType Directory -Path $ExTree -Force
    Expand-Archive -Path $bundlePath -DestinationPath $ExTree -Force

    Write-Host "== zip contents =="
    $bundleDirs = Get-ChildItem -Directory -Path $ExTree | Where-Object { $_.Name -like 'otto-support-*' }
    if (-not $bundleDirs) {
        FailWith "extracted bundle root not found under $ExTree"
        Write-Host "passed: $script:Pass, failed: $script:Fail"
        exit 1
    }
    $BundleRoot = $bundleDirs[0].FullName

    foreach ($required in @('MANIFEST.txt','env','health','logs','system','tray')) {
        if (Test-Path (Join-Path $BundleRoot $required)) {
            Ok "bundle contains $required"
        } else {
            FailWith "bundle missing $required"
        }
    }

    Write-Host "== secret-leak grep =="
    foreach ($needle in @($SecretToken, $SecretBearer, $SecretHash, $SecretEncrypt)) {
        $leakFiles = Get-ChildItem -Path $BundleRoot -Recurse -File -ErrorAction SilentlyContinue |
            Where-Object { (Select-String -Path $_.FullName -SimpleMatch -Pattern $needle -Quiet -ErrorAction SilentlyContinue) }
        if ($leakFiles) {
            FailWith "synthetic secret leaked into bundle: $needle"
            $leakFiles | ForEach-Object { Write-Host "    leak: $($_.FullName)" }
        } else {
            Ok "synthetic secret absent from bundle: $needle"
        }
    }

    $healthFile = Join-Path $BundleRoot 'health\health.json'
    if ((Test-Path $healthFile) -and (Select-String -Path $healthFile -SimpleMatch -Pattern 'unreachable:' -Quiet)) {
        Ok 'health\health.json captured unreachable sentinel'
    } else {
        FailWith 'health\health.json did not capture unreachable sentinel'
    }

    $envFile = Join-Path $BundleRoot 'env\effective.env'
    if ((Test-Path $envFile) -and (Select-String -Path $envFile -Pattern '^AUTH_TOKEN=real' -Quiet)) {
        Ok 'env\effective.env masked AUTH_TOKEN'
    } else {
        FailWith 'env\effective.env did not capture AUTH_TOKEN (or did not mask correctly)'
    }
    if ((Test-Path $envFile) -and (Select-String -Path $envFile -SimpleMatch -Pattern "AUTH_TOKEN=$SecretToken" -Quiet)) {
        FailWith 'env\effective.env LEAKED the full AUTH_TOKEN literal'
    }

    $manifestFile = Join-Path $BundleRoot 'MANIFEST.txt'
    if ((Test-Path $manifestFile) -and (Select-String -Path $manifestFile -SimpleMatch -Pattern 'Redaction notice' -Quiet)) {
        Ok 'MANIFEST.txt has redaction notice'
    } else {
        FailWith 'MANIFEST.txt missing redaction notice'
    }

    Remove-Item -Force $stdoutFile, $stderrFile -ErrorAction SilentlyContinue
} finally {
    Cleanup
}

Write-Host ""
Write-Host "== SUMMARY =="
Write-Host "passed: $script:Pass"
Write-Host "failed: $script:Fail"
if ($script:Fail -gt 0) { exit 1 }
exit 0
