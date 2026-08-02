$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
    Write-Host 'SKIP: non-administrator Windows ACL test requires Windows'
    exit 0
}

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$suffix = [guid]::NewGuid().ToString('N').Substring(0, 8)
$userName = "gwacl$suffix"
$password = 'Gw!' + [guid]::NewGuid().ToString('N') + 'a1'
$publicFixture = Join-Path $env:PUBLIC "gw-acl-$suffix"
$helper = Join-Path $publicFixture 'run.ps1'
$stdoutPath = Join-Path $publicFixture 'stdout.log'
$stderrPath = Join-Path $publicFixture 'stderr.log'

New-Item -ItemType Directory -Path $publicFixture -Force | Out-Null
$null = Copy-Item -LiteralPath (Join-Path $RepoRoot 'scripts') -Destination $publicFixture -Recurse
$Wrapper = Join-Path $publicFixture 'scripts\gw.ps1'
$Template = Join-Path $publicFixture 'scripts\.env.example'
$escapedWrapper = $Wrapper.Replace("'", "''")
$escapedTemplate = $Template.Replace("'", "''")
$helperSource = @"
`$ErrorActionPreference = 'Stop'
`$homePath = Join-Path `$env:USERPROFILE '.gw'
`$envPath = Join-Path `$homePath '.env'
New-Item -ItemType Directory -Path `$homePath -Force | Out-Null
& powershell.exe -NoProfile -ExecutionPolicy Bypass -File '$escapedWrapper' init -Dest `$envPath -Template '$escapedTemplate' -AuthEnabled -NonInteractive
if (`$LASTEXITCODE -ne 0) { exit `$LASTEXITCODE }
& powershell.exe -NoProfile -ExecutionPolicy Bypass -File '$escapedWrapper' init -Dest `$envPath -Template '$escapedTemplate' -AuthEnabled -NonInteractive -Force
exit `$LASTEXITCODE
"@
[System.IO.File]::WriteAllText($helper, $helperSource, (New-Object System.Text.UTF8Encoding($false)))

$created = $false
try {
    $createOutput = & net.exe user $userName $password /add /passwordchg:no 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) { throw "FAIL: could not create non-administrator fixture account: $createOutput" }
    $created = $true
    $securePassword = ConvertTo-SecureString $password -AsPlainText -Force
    $credential = New-Object System.Management.Automation.PSCredential(".\$userName", $securePassword)
    $process = Start-Process -FilePath 'powershell.exe' -Credential $credential -LoadUserProfile `
        -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $helper) `
        -WorkingDirectory $publicFixture -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath `
        -Wait -PassThru
    $output = ((Get-Content -LiteralPath $stdoutPath -Raw -ErrorAction SilentlyContinue) +
        (Get-Content -LiteralPath $stderrPath -Raw -ErrorAction SilentlyContinue))
    if ($process.ExitCode -ne 0) {
        throw "FAIL: non-administrator managed-secret re-init exited $($process.ExitCode): $output"
    }
    Write-Host 'PASS: non-administrator managed-secret re-init requires no elevated security privilege'
} finally {
    if ($created) { & net.exe user $userName /delete 2>&1 | Out-Null }
    Remove-Item -LiteralPath $publicFixture -Recurse -Force -ErrorAction SilentlyContinue
}
