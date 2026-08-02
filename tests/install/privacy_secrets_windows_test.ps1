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
function Get-StoredEnvValue([string]$Path, [string]$Key) {
    $pattern = '^\s*#?\s*' + [regex]::Escape($Key) + '='
    $line = @(Get-Content -LiteralPath $Path | Where-Object { $_ -cmatch $pattern }) | Select-Object -First 1
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

function Invoke-FixtureInitExpectFailure([string]$OutputPath) {
    $priorHome = $env:GW_HOME
    $priorFailure = $env:GW_TEST_MANAGED_SECRET_REPLACE_FAILURE
    try {
        $env:GW_HOME = $HomeFixture
        $env:GW_TEST_MANAGED_SECRET_REPLACE_FAILURE = 'true'
        $output = & pwsh -NoProfile -File $Wrapper init -Dest $EnvPath -Template $Template `
            -AuthEnabled -NonInteractive -Force -RegenerateSecrets 2>&1 | Out-String
        [System.IO.File]::WriteAllText($OutputPath, $output, (New-Object System.Text.UTF8Encoding($false)))
        return $LASTEXITCODE
    } finally {
        $env:GW_HOME = $priorHome
        $env:GW_TEST_MANAGED_SECRET_REPLACE_FAILURE = $priorFailure
    }
}

function Assert-ManagedSecretsAcl([string]$Path) {
    if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
        Write-Host 'skip: real-Windows managed-secret DACL assertion unavailable on this host'
        return
    }
    $acl = Get-Acl -LiteralPath $Path
    if (-not $acl.AreAccessRulesProtected) { Fail-With 'managed-secret DACL still inherits parent access rules' }
    $currentSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $allowSids = @($acl.Access | Where-Object { $_.AccessControlType -eq 'Allow' } | ForEach-Object {
        $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
    } | Sort-Object -Unique)
    if ($allowSids.Count -ne 1 -or $allowSids[0] -cne $currentSid) {
        Fail-With "managed-secret DACL grants access beyond the current SID: $($allowSids -join ',')"
    }
    Write-Host 'ok: real Windows managed-secret DACL is protected and grants only the current SID'
}

function Invoke-PrePrivacyUpgradeFlow(
    [string]$Name,
    [string]$ExistingAuth,
    [string]$ExistingHash,
    [string]$ExistingEncrypt
) {
    $installHome = Join-Path $FixtureRoot $Name
    $envFile = Join-Path $installHome '.env'
    $overridesFile = Join-Path $installHome 'overrides.env'
    $outputFile = Join-Path $FixtureRoot "$Name.out"
    $null = New-Item -ItemType Directory -Path $installHome -Force
    @(
        'HTTP_ADDR=127.0.0.1:18080',
        'PII_REDACTION_ENABLED=true',
        'PII_REDACTION_MODE=encrypt'
    ) | Set-Content -LiteralPath $envFile -Encoding UTF8
    @(
        "AUTH_TOKEN=$ExistingAuth",
        "PII_HASH_KEY=$ExistingHash",
        "PII_ENCRYPT_KEY=$ExistingEncrypt"
    ) | Set-Content -LiteralPath $overridesFile -Encoding UTF8

    $priorHome = $env:GW_HOME
    $priorTemplate = $env:GW_TEMPLATE_FILE
    $priorUpgradeLog = $env:GW_UPGRADE_LOG
    try {
        $env:GW_HOME = $installHome
        $env:GW_TEMPLATE_FILE = $Template
        $env:GW_UPGRADE_LOG = Join-Path $installHome 'upgrade.log'
        $allOutput = & pwsh -NoProfile -File $Wrapper upgrade-env -DryRun -Dest $envFile -Template $Template 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0) { Fail-With "$Name upgrade preview failed: $allOutput" }
        $applyOutput = & pwsh -NoProfile -File $Wrapper upgrade-env -Yes -Dest $envFile -Template $Template 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0) { Fail-With "$Name upgrade apply failed: $applyOutput" }
        $allOutput += $applyOutput
        if ((Get-EnvValue $envFile 'PRIVACY_ALIAS_KEY') -cne '<generated-by-gw-init>') { Fail-With "$Name upgrade did not apply the shipped alias placeholder" }
        if ((Get-EnvValue $envFile 'PRIVACY_TRIAGE_TOKEN') -cne '<generated-by-gw-init>') { Fail-With "$Name upgrade did not apply the shipped triage placeholder" }
        $initOutput = & pwsh -NoProfile -File $Wrapper init -Dest $envFile -OverridesDest $overridesFile `
            -Template $Template -Force -NonInteractive 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0) { Fail-With "$Name normal re-init failed: $initOutput" }
        $allOutput += $initOutput
        [System.IO.File]::WriteAllText($outputFile, $allOutput, (New-Object System.Text.UTF8Encoding($false)))
    } finally {
        $env:GW_HOME = $priorHome
        $env:GW_TEMPLATE_FILE = $priorTemplate
        $env:GW_UPGRADE_LOG = $priorUpgradeLog
    }

    if ((Get-EnvValue $overridesFile 'AUTH_TOKEN') -cne $ExistingAuth) { Fail-With "$Name rotated existing AUTH_TOKEN" }
    if ((Get-EnvValue $overridesFile 'PII_HASH_KEY') -cne $ExistingHash) { Fail-With "$Name rotated existing PII_HASH_KEY" }
    if ((Get-EnvValue $overridesFile 'PII_ENCRYPT_KEY') -cne $ExistingEncrypt) { Fail-With "$Name rotated existing PII_ENCRYPT_KEY" }
    $aliasValue = Get-EnvValue $overridesFile 'PRIVACY_ALIAS_KEY'
    $triageValue = Get-EnvValue $overridesFile 'PRIVACY_TRIAGE_TOKEN'
    if ($aliasValue -cnotmatch '^[0-9a-f]{64}$') { Fail-With "$Name preserved the shipped alias placeholder" }
    if ($triageValue -cnotmatch '^[0-9a-f]{64}$') { Fail-With "$Name preserved the shipped triage placeholder" }
    if ($aliasValue -ceq $triageValue) { Fail-With "$Name minted identical privacy secrets" }
    $outputText = Get-Content -LiteralPath $outputFile -Raw
    foreach ($secret in @($ExistingAuth,$ExistingHash,$ExistingEncrypt,$aliasValue,$triageValue)) {
        if ($outputText.Contains($secret)) { Fail-With "$Name printed a managed secret" }
    }
    Assert-ManagedSecretsAcl $overridesFile
    if (@(Get-ChildItem -LiteralPath $installHome -Force | Where-Object { $_.Name -like '.managed-secrets-*' -or $_.Name -like 'overrides.env.tmp.*' }).Count -ne 0) {
        Fail-With "$Name left a secret temporary"
    }
    return [pscustomobject]@{ Alias = $aliasValue; Triage = $triageValue }
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
    Assert-ManagedSecretsAcl $OverridesPath
    Write-Host 'ok: cold init generates five managed secrets without exposing privacy values'

    $beforeForcedFailure = [System.IO.File]::ReadAllText($OverridesPath)
    $failureOutput = Join-Path $FixtureRoot 'replace-failure.out'
    $failureExit = Invoke-FixtureInitExpectFailure $failureOutput
    if ($failureExit -eq 0) { Fail-With 'forced atomic replacement failure unexpectedly succeeded' }
    if ([System.IO.File]::ReadAllText($OverridesPath) -cne $beforeForcedFailure) {
        Fail-With 'forced atomic replacement failure changed the prior overrides file'
    }
    if (@(Get-ChildItem -LiteralPath $HomeFixture -Force -Filter '.managed-secrets-*.tmp').Count -ne 0) {
        Fail-With 'forced atomic replacement failure left a managed-secret temporary file'
    }
    Write-Host 'ok: failed atomic replacement preserves the prior file and removes its protected temporary'

    $parseErrors = $null
    $tokens = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($Wrapper, [ref]$tokens, [ref]$parseErrors)
    if ($parseErrors.Count -ne 0) { Fail-With 'gw.ps1 does not parse for managed-secret publication inspection' }
    $managedFunction = @($ast.FindAll({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -ceq 'Set-ManagedSecrets' }, $true)) | Select-Object -First 1
    if (-not $managedFunction) { Fail-With 'Set-ManagedSecrets function is missing' }
    $managedText = $managedFunction.Extent.Text
    $aclIndex = $managedText.IndexOf('Protect-ManagedSecretTemporary')
    $writeIndex = $managedText.IndexOf('WriteAllLines')
    $publishIndex = $managedText.IndexOf('Publish-SupportFileAtomically')
    if ($aclIndex -lt 0 -or $writeIndex -lt 0 -or $aclIndex -ge $writeIndex) {
        Fail-With 'managed-secret temporary ACL is not established before secret values are written'
    }
    if ($publishIndex -lt 0 -or $managedText -cmatch '(?m)^\s*Remove-Item\s+[^\r\n]*\$FilePath') {
        Fail-With 'managed-secret replacement does not use the shared no-delete atomic publication primitive'
    }
    Write-Host 'ok: managed-secret writer protects its sibling temporary before values and uses no-delete atomic publication'

    $overridesFunction = @($ast.FindAll({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -ceq 'Set-OverridesLine' }, $true)) | Select-Object -First 1
    if (-not $overridesFunction) { Fail-With 'Set-OverridesLine function is missing' }
    $overridesText = $overridesFunction.Extent.Text
    if ($overridesText -cnotmatch 'Publish-SupportFileAtomically' -or
        $overridesText -cmatch '(?m)^\s*(Set-Content|Add-Content)\b') {
        Fail-With 'operator override updates can expose truncated managed-secret contents'
    }
    Write-Host 'ok: operator override updates use no-delete atomic publication'

    if ([System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT) {
        $readerReady = Join-Path $FixtureRoot 'reader.ready'
        $readerStop = Join-Path $FixtureRoot 'reader.stop'
        $oldComplete = [System.IO.File]::ReadAllText($OverridesPath)
        $readerJob = Start-Job -ScriptBlock {
            param($Path, $ReadyPath, $StopPath)
            $observed = New-Object 'System.Collections.Generic.HashSet[string]'
            [System.IO.File]::WriteAllText($ReadyPath, 'ready')
            while (-not [System.IO.File]::Exists($StopPath)) {
                try {
                    $bytes = [System.Text.Encoding]::UTF8.GetBytes([System.IO.File]::ReadAllText($Path))
                    $null = $observed.Add([Convert]::ToBase64String($bytes))
                } catch {
                    $null = $observed.Add('READ-ERROR:' + $_.Exception.GetType().FullName)
                }
                Start-Sleep -Milliseconds 1
            }
            return @($observed)
        } -ArgumentList $OverridesPath, $readerReady, $readerStop
        try {
            $deadline = [DateTime]::UtcNow.AddSeconds(10)
            while (-not (Test-Path -LiteralPath $readerReady)) {
                if ([DateTime]::UtcNow -ge $deadline) { Fail-With 'continuous reader did not become ready' }
                Start-Sleep -Milliseconds 10
            }
            $readerRotationOutput = Join-Path $FixtureRoot 'reader-rotation.out'
            Invoke-FixtureInit $readerRotationOutput -Force -RegenerateSecrets
            $newComplete = [System.IO.File]::ReadAllText($OverridesPath)
        } finally {
            [System.IO.File]::WriteAllText($readerStop, 'stop')
            $null = Wait-Job -Job $readerJob -Timeout 10
            $observations = @(Receive-Job -Job $readerJob)
            Remove-Job -Job $readerJob -Force -ErrorAction SilentlyContinue
        }
        $oldEncoded = [Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($oldComplete))
        $newEncoded = [Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($newComplete))
        if ($observations.Count -lt 1) { Fail-With 'continuous reader observed no managed-secret contents' }
        foreach ($observation in $observations) {
            if ($observation -cne $oldEncoded -and $observation -cne $newEncoded) {
                if ($observation.StartsWith('READ-ERROR:')) {
                    Fail-With "continuous reader failed while secrets were replaced: $observation"
                }
                $observedLength = ([Convert]::FromBase64String($observation)).Length
                Fail-With "continuous reader observed an unexpected $observedLength-byte managed-secret snapshot"
            }
        }
        Write-Host 'ok: real Windows readers observe only the old complete set or new complete set'
    } else {
        Write-Host 'skip: real-Windows continuous replacement assertion unavailable on this host'
    }

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

    $commentedLines = @(Get-Content -LiteralPath $OverridesPath | ForEach-Object {
        if ($_ -cmatch '^PRIVACY_(?:ALIAS_KEY|TRIAGE_TOKEN)=') { '# ' + $_ } else { $_ }
    })
    [System.IO.File]::WriteAllLines($OverridesPath, $commentedLines, (New-Object System.Text.UTF8Encoding($false)))
    $commentedPreserveOutput = Join-Path $FixtureRoot 'commented-preserve.out'
    Invoke-FixtureInit $commentedPreserveOutput -Force
    $commentedContent = @(Get-Content -LiteralPath $OverridesPath)
    if ($commentedContent -cnotcontains '# PRIVACY_ALIAS_KEY=override-alias-value') { Fail-With 're-init enabled a deliberately commented privacy alias key' }
    if ($commentedContent -cnotcontains '# PRIVACY_TRIAGE_TOKEN=override-triage-value') { Fail-With 're-init enabled a deliberately commented privacy triage token' }
    if (@($commentedContent | Where-Object { $_ -cmatch '^PRIVACY_(?:ALIAS_KEY|TRIAGE_TOKEN)=' }).Count -ne 0) {
        Fail-With 're-init wrote an enabled duplicate of a deliberately commented privacy secret'
    }
    Write-Host 'ok: normal re-init preserves deliberately commented privacy secrets'
    $uncommentedLines = @($commentedContent | ForEach-Object { $_ -replace '^#\s+(PRIVACY_(?:ALIAS_KEY|TRIAGE_TOKEN)=)', '$1' })
    [System.IO.File]::WriteAllLines($OverridesPath, $uncommentedLines, (New-Object System.Text.UTF8Encoding($false)))

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

    $disabledHome = Join-Path $FixtureRoot 'disabled-home'
    $disabledEnv = Join-Path $disabledHome '.env'
    $disabledOverrides = Join-Path $disabledHome 'overrides.env'
    $null = New-Item -ItemType Directory -Path $disabledHome -Force
    $disabledKeys = @('AUTH_TOKEN','PII_HASH_KEY','PII_ENCRYPT_KEY','PRIVACY_ALIAS_KEY','PRIVACY_TRIAGE_TOKEN')
    $disabledBefore = @{}
    $priorHome = $env:GW_HOME
    try {
        $env:GW_HOME = $disabledHome
        $disabledColdOutput = & pwsh -NoProfile -File $Wrapper init -Dest $disabledEnv -Template $Template -NonInteractive 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0) { Fail-With "disabled-auth cold init failed: $disabledColdOutput" }
        foreach ($key in $disabledKeys) {
            $value = Get-StoredEnvValue $disabledOverrides $key
            if ($value -cnotmatch '^[0-9a-f]{64}$') { Fail-With "disabled-auth cold init did not store managed $key" }
            $disabledBefore[$key] = $value
            if ($disabledColdOutput.Contains($value)) { Fail-With "disabled-auth cold init printed $key" }
        }
        if (-not (@(Get-Content -LiteralPath $disabledOverrides | Where-Object { $_ -cmatch '^#\s*AUTH_TOKEN=[0-9a-f]{64}$' }).Count -eq 1)) {
            Fail-With 'disabled-auth token is not stored exactly once as a comment'
        }
        if (@(Get-Content -LiteralPath $disabledOverrides | Where-Object { $_ -cmatch '^AUTH_TOKEN=' }).Count -ne 0) {
            Fail-With 'disabled-auth cold init enabled authentication'
        }

        $disabledRotateOutput = & pwsh -NoProfile -File $Wrapper init -Dest $disabledEnv -Template $Template -NonInteractive -Force -RegenerateSecrets 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0) { Fail-With "disabled-auth regeneration failed: $disabledRotateOutput" }
        foreach ($key in $disabledKeys) {
            $value = Get-StoredEnvValue $disabledOverrides $key
            if ($value -cnotmatch '^[0-9a-f]{64}$') { Fail-With "disabled-auth regeneration did not store managed $key" }
            if ($value -ceq $disabledBefore[$key]) { Fail-With "disabled-auth regeneration did not rotate $key" }
            if ($disabledRotateOutput.Contains($disabledBefore[$key]) -or $disabledRotateOutput.Contains($value)) {
                Fail-With "disabled-auth regeneration printed $key"
            }
        }
        if (-not (@(Get-Content -LiteralPath $disabledOverrides | Where-Object { $_ -cmatch '^#\s*AUTH_TOKEN=[0-9a-f]{64}$' }).Count -eq 1)) {
            Fail-With 'disabled-auth regenerated token is not stored exactly once as a comment'
        }
        if (@(Get-Content -LiteralPath $disabledOverrides | Where-Object { $_ -cmatch '^AUTH_TOKEN=' }).Count -ne 0) {
            Fail-With 'disabled-auth regeneration enabled authentication'
        }
        $disabledWarningIndex = [regex]::Match($disabledRotateOutput, '(?im)^.*mapping.*(loss|invalid).*$').Index
        $disabledWriteIndex = [regex]::Match($disabledRotateOutput, '(?im)^.*wrote .*overrides.*$').Index
        if ($disabledWarningIndex -lt 0 -or $disabledWriteIndex -lt 0 -or $disabledWarningIndex -ge $disabledWriteIndex) {
            Fail-With 'disabled-auth mapping-loss warning was not printed before mutation'
        }
        Write-Host 'ok: disabled auth stores and atomically rotates all five secrets without enabling auth or printing values'
    } finally {
        $env:GW_HOME = $priorHome
    }

    $legacyAuthOne = '1111111111111111111111111111111111111111111111111111111111111111'
    $legacyHashOne = '2222222222222222222222222222222222222222222222222222222222222222'
    $legacyEncryptOne = '3333333333333333333333333333333333333333333333333333333333333333'
    $legacyAuthTwo = '4444444444444444444444444444444444444444444444444444444444444444'
    $legacyHashTwo = '5555555555555555555555555555555555555555555555555555555555555555'
    $legacyEncryptTwo = '6666666666666666666666666666666666666666666666666666666666666666'
    $upgradedOne = Invoke-PrePrivacyUpgradeFlow 'preprivacy-one' $legacyAuthOne $legacyHashOne $legacyEncryptOne
    $upgradedTwo = Invoke-PrePrivacyUpgradeFlow 'preprivacy-two' $legacyAuthTwo $legacyHashTwo $legacyEncryptTwo
    foreach ($left in @($upgradedOne.Alias,$upgradedOne.Triage)) {
        foreach ($right in @($upgradedTwo.Alias,$upgradedTwo.Triage)) {
            if ($left -ceq $right) { Fail-With 'independent upgraded installs reused a privacy secret' }
        }
    }
    Write-Host 'ok: pre-privacy upgrade and normal re-init mint per-install privacy secrets while preserving the existing three'

    $templateText = Get-Content -LiteralPath $Template -Raw
    if ($templateText -cnotmatch '(?m)^PRIVACY_ALIAS_KEY=(<[^>]+>|replace-|)\r?$') { Fail-With '.env.example alias key is not a placeholder' }
    if ($templateText -cnotmatch '(?m)^PRIVACY_TRIAGE_TOKEN=(<[^>]+>|replace-|)\r?$') { Fail-With '.env.example triage token is not a placeholder' }
    Write-Host 'PASS: PowerShell managed privacy secrets'
} finally {
    Remove-Item -LiteralPath $FixtureRoot -Recurse -Force -ErrorAction SilentlyContinue
}
