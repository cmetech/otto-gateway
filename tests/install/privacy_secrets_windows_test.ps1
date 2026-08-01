$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$FixtureRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('gw-privacy-secrets-windows-' + [guid]::NewGuid().ToString('N'))
$HomeFixture = Join-Path $FixtureRoot 'home'
$EnvPath = Join-Path $HomeFixture '.env'
$OverridesPath = Join-Path $HomeFixture 'overrides.env'
$Wrapper = Join-Path $RepoRoot 'scripts\gw.ps1'
$Template = Join-Path $RepoRoot 'scripts\.env.example'

function Fail-With([string]$Message) { throw "FAIL: $Message" }
function Get-EnvValue([string]$Path, [string]$Key) {
    $line = @(Get-Content -LiteralPath $Path | Where-Object { $_ -cmatch ('^' + [regex]::Escape($Key) + '=') }) | Select-Object -First 1
    if (-not $line) { return '' }
    return $line.Substring($line.IndexOf('=') + 1)
}
function Assert-ManagedSecret([string]$Path, [string]$Key) {
    $value = Get-EnvValue $Path $Key
    if ($value -cnotmatch '^[0-9a-f]{64}$') { Fail-With "$Key was not generated as 32 cryptographic bytes" }
    return $value
}
function Invoke-FixtureInit([string]$OutputPath, [switch]$Force, [switch]$RegenerateSecrets) {
    $priorHome = $env:GW_HOME
    try {
        $env:GW_HOME = $HomeFixture
        $args = @('-NoProfile', '-File', $Wrapper, 'init', '-Dest', $EnvPath, '-Template', $Template, '-AuthEnabled', '-NonInteractive')
        if ($Force) { $args += '-Force' }
        if ($RegenerateSecrets) { $args += '-RegenerateSecrets' }
        $output = & pwsh @args 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0) { Fail-With "gw.ps1 init failed: $output" }
        [System.IO.File]::WriteAllText($OutputPath, $output, (New-Object System.Text.UTF8Encoding($false)))
    } finally {
        $env:GW_HOME = $priorHome
    }
}

New-Item -ItemType Directory -Path $HomeFixture -Force | Out-Null
try {
    $coldOutput = Join-Path $FixtureRoot 'cold.out'
    Invoke-FixtureInit $coldOutput
    if (-not (Test-Path -LiteralPath $OverridesPath)) { Fail-With 'cold init did not create overrides.env' }

    $authBefore = Assert-ManagedSecret $OverridesPath 'AUTH_TOKEN'
    $hashBefore = Assert-ManagedSecret $OverridesPath 'PII_HASH_KEY'
    $encryptBefore = Assert-ManagedSecret $OverridesPath 'PII_ENCRYPT_KEY'
    $aliasBefore = Assert-ManagedSecret $OverridesPath 'PRIVACY_ALIAS_KEY'
    $triageBefore = Assert-ManagedSecret $OverridesPath 'PRIVACY_TRIAGE_TOKEN'
    $coldText = Get-Content -LiteralPath $coldOutput -Raw
    if ($coldText.Contains($aliasBefore) -or $coldText.Contains($triageBefore)) { Fail-With 'privacy secret appeared in init output' }
    Write-Host 'ok: cold init generates five managed secrets without exposing privacy values'

    Add-Content -LiteralPath $OverridesPath -Value @('', '# operator comment must survive', 'UNRELATED_OPERATOR_SETTING=keep-me', 'PRIVACY_ALIAS_KEY=override-alias-value', 'PRIVACY_TRIAGE_TOKEN=override-triage-value')
    $preserveOutput = Join-Path $FixtureRoot 'preserve.out'
    Invoke-FixtureInit $preserveOutput -Force
    if ((Get-EnvValue $OverridesPath 'PRIVACY_ALIAS_KEY') -cne 'override-alias-value') { Fail-With 'overrides alias key did not win on re-init' }
    if ((Get-EnvValue $OverridesPath 'PRIVACY_TRIAGE_TOKEN') -cne 'override-triage-value') { Fail-With 'overrides triage token did not win on re-init' }
    $preservedContent = Get-Content -LiteralPath $OverridesPath -Raw
    if (-not $preservedContent.Contains('# operator comment must survive')) { Fail-With 're-init removed an operator comment' }
    if (-not $preservedContent.Contains('UNRELATED_OPERATOR_SETTING=keep-me')) { Fail-With 're-init removed an unrelated setting' }
    $preserveText = Get-Content -LiteralPath $preserveOutput -Raw
    if ($preserveText.Contains('override-alias-value') -or $preserveText.Contains('override-triage-value')) { Fail-With 'preserved privacy secret appeared in output' }
    Write-Host 'ok: normal upgrade preserves effective overrides and unrelated content'

    $authBefore = Get-EnvValue $OverridesPath 'AUTH_TOKEN'
    $hashBefore = Get-EnvValue $OverridesPath 'PII_HASH_KEY'
    $encryptBefore = Get-EnvValue $OverridesPath 'PII_ENCRYPT_KEY'
    $aliasBefore = Get-EnvValue $OverridesPath 'PRIVACY_ALIAS_KEY'
    $triageBefore = Get-EnvValue $OverridesPath 'PRIVACY_TRIAGE_TOKEN'
    $rotateOutput = Join-Path $FixtureRoot 'rotate.out'
    Invoke-FixtureInit $rotateOutput -Force -RegenerateSecrets
    foreach ($key in @('AUTH_TOKEN','PII_HASH_KEY','PII_ENCRYPT_KEY','PRIVACY_ALIAS_KEY','PRIVACY_TRIAGE_TOKEN')) { $null = Assert-ManagedSecret $OverridesPath $key }
    if ((Get-EnvValue $OverridesPath 'AUTH_TOKEN') -ceq $authBefore) { Fail-With 'AUTH_TOKEN did not rotate' }
    if ((Get-EnvValue $OverridesPath 'PII_HASH_KEY') -ceq $hashBefore) { Fail-With 'PII_HASH_KEY did not rotate' }
    if ((Get-EnvValue $OverridesPath 'PII_ENCRYPT_KEY') -ceq $encryptBefore) { Fail-With 'PII_ENCRYPT_KEY did not rotate' }
    if ((Get-EnvValue $OverridesPath 'PRIVACY_ALIAS_KEY') -ceq $aliasBefore) { Fail-With 'PRIVACY_ALIAS_KEY did not rotate' }
    if ((Get-EnvValue $OverridesPath 'PRIVACY_TRIAGE_TOKEN') -ceq $triageBefore) { Fail-With 'PRIVACY_TRIAGE_TOKEN did not rotate' }
    $rotateText = Get-Content -LiteralPath $rotateOutput -Raw
    if ($rotateText -cnotmatch '(?i)mapping.*(loss|invalid)') { Fail-With 'rotation did not warn about mapping loss' }
    if ($rotateText -cnotmatch '(?i)restart') { Fail-With 'rotation did not warn that restart is required' }
    $warningIndex = [regex]::Match($rotateText, '(?im)^.*mapping.*(loss|invalid).*$').Index
    $writeIndex = [regex]::Match($rotateText, '(?im)^.*wrote .*overrides.*$').Index
    if ($warningIndex -lt 0 -or $writeIndex -lt 0 -or $warningIndex -ge $writeIndex) { Fail-With 'mapping-loss warning was not printed before config mutation was reported' }
    foreach ($secret in @($aliasBefore, $triageBefore, (Get-EnvValue $OverridesPath 'PRIVACY_ALIAS_KEY'), (Get-EnvValue $OverridesPath 'PRIVACY_TRIAGE_TOKEN'))) {
        if ($rotateText.Contains($secret)) { Fail-With 'privacy secret appeared in rotation output' }
    }
    $rotatedContent = Get-Content -LiteralPath $OverridesPath -Raw
    if (-not $rotatedContent.Contains('# operator comment must survive') -or -not $rotatedContent.Contains('UNRELATED_OPERATOR_SETTING=keep-me')) { Fail-With 'rotation removed unrelated operator content' }
    Write-Host 'ok: explicit rotation atomically replaces all five secrets and warns safely'

    $templateText = Get-Content -LiteralPath $Template -Raw
    if ($templateText -cnotmatch '(?m)^PRIVACY_ALIAS_KEY=(<[^>]+>|replace-|)$') { Fail-With '.env.example alias key is not a placeholder' }
    if ($templateText -cnotmatch '(?m)^PRIVACY_TRIAGE_TOKEN=(<[^>]+>|replace-|)$') { Fail-With '.env.example triage token is not a placeholder' }
    Write-Host 'PASS: PowerShell managed privacy secrets'
} finally {
    Remove-Item -LiteralPath $FixtureRoot -Recurse -Force -ErrorAction SilentlyContinue
}
