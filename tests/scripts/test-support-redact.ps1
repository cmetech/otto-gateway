# tests/scripts/test-support-redact.ps1 — unit tests for
# scripts/lib/redact.ps1. Plain pwsh harness (no Pester) mirroring
# tests/scripts/test-support-redact.sh — manual pass/fail counters,
# exit non-zero on any failure. Design says no new dependencies.

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$RepoRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')
. (Join-Path $RepoRoot 'scripts\lib\redact.ps1')

$script:Pass = 0
$script:Fail = 0

function Assert-Eq {
    param([string]$Expected, [string]$Actual, [string]$Label)
    if ($Expected -ceq $Actual) {
        $script:Pass++
        Write-Host "  ok: $Label"
    } else {
        $script:Fail++
        Write-Host "FAIL: ${Label}: expected [$Expected], got [$Actual]"
    }
}

function Assert-Contains {
    param([string]$Haystack, [string]$Needle, [string]$Label)
    if ($Haystack -cmatch [regex]::Escape($Needle)) {
        $script:Pass++
        Write-Host "  ok: $Label"
    } else {
        $script:Fail++
        Write-Host "FAIL: ${Label}: [$Needle] not found"
    }
}

function Assert-NotContains {
    param([string]$Haystack, [string]$Needle, [string]$Label)
    if ($Haystack -cmatch [regex]::Escape($Needle)) {
        $script:Fail++
        Write-Host "FAIL: ${Label}: forbidden [$Needle] present"
    } else {
        $script:Pass++
        Write-Host "  ok: $Label"
    }
}

function Assert-True {
    param([bool]$Cond, [string]$Label)
    if ($Cond) {
        $script:Pass++
        Write-Host "  ok: $Label"
    } else {
        $script:Fail++
        Write-Host "FAIL: $Label"
    }
}

Write-Host "== Invoke-RedactStream =="

$fixture = @(
    'Bearer eyJabc.def-ghi_jkl',
    'AUTH_TOKEN=supersecretvalue',
    'PII_HASH_KEY=anotherSecret',
    'PII_ENCRYPT_KEY=thirdSecret',
    'Authorization: Bearer foo',
    'x-api-key: bar',
    'X-API-KEY: BAZ',
    'hello world'
)
$redacted = ($fixture | Invoke-RedactStream) -join "`n"

Assert-Contains $redacted 'Bearer [REDACTED]' 'Bearer token rewritten'
Assert-Contains $redacted 'AUTH_TOKEN=[REDACTED]' 'AUTH_TOKEN= line rewritten'
Assert-Contains $redacted 'PII_HASH_KEY=[REDACTED]' 'PII_HASH_KEY= line rewritten'
Assert-Contains $redacted 'PII_ENCRYPT_KEY=[REDACTED]' 'PII_ENCRYPT_KEY= line rewritten'
Assert-Contains $redacted 'Authorization: [REDACTED]' 'Authorization header rewritten'
Assert-Contains $redacted 'x-api-key: [REDACTED]' 'x-api-key (lower) rewritten'
Assert-Contains $redacted 'X-API-KEY: [REDACTED]' 'X-API-KEY (upper) rewritten'
Assert-Contains $redacted 'hello world' 'control line preserved'

Assert-NotContains $redacted 'supersecretvalue' 'AUTH_TOKEN secret absent'
Assert-NotContains $redacted 'anotherSecret' 'PII_HASH_KEY secret absent'
Assert-NotContains $redacted 'thirdSecret' 'PII_ENCRYPT_KEY secret absent'
Assert-NotContains $redacted 'eyJabc.def-ghi_jkl' 'Bearer token secret absent'

# Idempotency.
$redacted2 = ($redacted -split "`n" | Invoke-RedactStream) -join "`n"
Assert-Eq $redacted $redacted2 'Invoke-RedactStream is idempotent'

Write-Host "== Mask-EnvValue =="

Assert-Eq "abcd$([char]0x2026)(12 chars)" (Mask-EnvValue 'abcd1234efgh') '12-char value masked'
Assert-Eq '' (Mask-EnvValue '') 'empty value yields empty'
Assert-Eq "abc$([char]0x2026)(3 chars)" (Mask-EnvValue 'abc') '3-char value (shorter than prefix) handled'
Assert-Eq "abcd$([char]0x2026)(4 chars)" (Mask-EnvValue 'abcd') '4-char value (exact prefix) handled'
$masked = Mask-EnvValue 'supersecretvalue'
Assert-NotContains $masked 'supersecretvalue' 'full secret literal absent from mask'

Write-Host "== Test-IsSecretKey =="

foreach ($k in @('AUTH_TOKEN','PII_HASH_KEY','PII_ENCRYPT_KEY','MY_PASSWORD','WEBHOOK_SECRET','API_KEY','MY_TOKEN','PASSPHRASE_FOO','auth_token')) {
    Assert-True (Test-IsSecretKey $k) "Test-IsSecretKey($k) -> true"
}
foreach ($k in @('HTTP_ADDR','POOL_SIZE','OTTO_ADDR','DEBUG','ENABLED_HOOKS','PII_REDACTION_MODE')) {
    Assert-True (-not (Test-IsSecretKey $k)) "Test-IsSecretKey($k) -> false"
}
Assert-True (-not (Test-IsSecretKey '')) 'Test-IsSecretKey(empty) -> false'

Write-Host ""
Write-Host "== SUMMARY =="
Write-Host "passed: $script:Pass"
Write-Host "failed: $script:Fail"
if ($script:Fail -gt 0) { exit 1 }
exit 0
