# Integration coverage for `gw.ps1 support`. The real wrapper runs against
# controlled Gateway, Kiro, and Co-worker homes plus deterministic HTTP
# endpoints. Plain PowerShell harness: no Pester dependency.

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$RepoRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$Wrapper = Join-Path $RepoRoot 'scripts\gw.ps1'
$SafeOpenLibrary = Join-Path $RepoRoot 'scripts\lib\support-safe-open.ps1'
$RunningOnWindows = [System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT
$PowerShellExecutable = if ($RunningOnWindows) {
    (Get-Command powershell.exe -ErrorAction Stop).Source
} else {
    (Get-Command pwsh -ErrorAction Stop).Source
}
$PythonCommand = Get-Command python3 -ErrorAction SilentlyContinue
if (-not $PythonCommand) { $PythonCommand = Get-Command python -ErrorAction Stop }
$Python = $PythonCommand.Source
$script:Pass = 0
$script:Fail = 0
$script:FixtureRoot = $null
$script:HttpProcess = $null
$script:WindowsJunctions = New-Object System.Collections.Generic.List[string]
$ExtractRoot = $null

function Ok([string]$Label) {
    $script:Pass++
    Write-Host "  ok: $Label"
}

function Fail-With([string]$Message) {
    $script:Fail++
    Write-Host "FAIL: $Message"
}

function Assert-True([bool]$Condition, [string]$Label) {
    if ($Condition) { Ok $Label } else { Fail-With $Label }
}

function Assert-File([string]$Path, [string]$Label) {
    Assert-True ((Test-Path -LiteralPath $Path -PathType Leaf)) "$Label (missing: $Path)"
}

function Assert-Directory([string]$Path, [string]$Label) {
    Assert-True ((Test-Path -LiteralPath $Path -PathType Container)) "$Label (missing: $Path)"
}

function Assert-Absent([string]$Path, [string]$Label) {
    Assert-True (-not (Test-Path -LiteralPath $Path)) "$Label (forbidden: $Path)"
}

function Assert-Contains([string]$Path, [string]$Needle, [string]$Label) {
    $found = (Test-Path -LiteralPath $Path -PathType Leaf) -and
        (Select-String -LiteralPath $Path -SimpleMatch -Pattern $Needle -Quiet -ErrorAction SilentlyContinue)
    Assert-True $found "$Label (missing [$Needle] in $Path)"
}

function Assert-Line([string]$Path, [string]$Expected, [string]$Label) {
    $found = (Test-Path -LiteralPath $Path -PathType Leaf) -and
        (@(Get-Content -LiteralPath $Path) -ccontains $Expected)
    Assert-True $found "$Label (missing exact line [$Expected] in $Path)"
}

function Assert-CurrentLogContains {
    param(
        [string]$Path,
        [string]$Manifest,
        [AllowEmptyString()][string]$Needle,
        [string]$Label
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        $warnings = @()
        if (Test-Path -LiteralPath $Manifest -PathType Leaf) {
            $warnings = @(Get-Content -LiteralPath $Manifest |
                Where-Object { $_ -like 'WARNING: *current log*' })
        }
        $warningText = if ($warnings.Count -gt 0) { $warnings -join '; ' } else { '(none)' }
        Fail-With "$Label (missing: $Path; manifest current-log warnings: $warningText)"
        return
    }
    if ($Needle) {
        Assert-Contains $Path $Needle $Label
    } else {
        Ok $Label
    }
}

function Format-SupportResult($Result) {
    $exitCode = if ($null -eq $Result.ExitCode) { '<null>' } else { [string]$Result.ExitCode }
    return "exit code $exitCode; stderr: $($Result.Stderr)"
}

function Assert-NoUtf8Bom([string]$Path, [string]$Label) {
    $hasBom = $false
    if (Test-Path -LiteralPath $Path -PathType Leaf) {
        $bytes = [System.IO.File]::ReadAllBytes($Path)
        $hasBom = $bytes.Length -ge 3 -and
            $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF
    }
    Assert-True ((Test-Path -LiteralPath $Path -PathType Leaf) -and -not $hasBom) "$Label (unexpected UTF-8 BOM: $Path)"
}

function Assert-NoSupportTemporaryArtifacts([string]$Root, [string]$Label) {
    $artifacts = @()
    if ($Root -and (Test-Path -LiteralPath $Root)) {
        $artifacts = @(Get-ChildItem -LiteralPath $Root -Recurse -Force -ErrorAction SilentlyContinue |
            Where-Object {
                $_.Name -match '(?i)\.partial-' -or
                $_.Name -like '.gw-support-staging-*'
            })
    }
    $found = @($artifacts | ForEach-Object FullName) -join ', '
    Assert-True ($artifacts.Count -eq 0) "$Label (temporary artifacts: $found)"
}

function Get-SupportStagingDirectories([string]$TempRoot) {
    if (-not $TempRoot -or -not (Test-Path -LiteralPath $TempRoot -PathType Container)) {
        return @()
    }
    return @(Get-ChildItem -LiteralPath $TempRoot -Directory -Force -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -like '.gw-support-staging-*' } |
        ForEach-Object { [System.IO.Path]::GetFullPath($_.FullName) })
}

function New-SupportStagingSnapshot([string]$TempRoot) {
    return [pscustomobject]@{
        TempRoot = [System.IO.Path]::GetFullPath($TempRoot)
        Entries = [string[]]@(Get-SupportStagingDirectories $TempRoot)
    }
}

function Get-NewSupportStagingDirectories($Snapshot) {
    $comparison = if ($RunningOnWindows) {
        [System.StringComparison]::OrdinalIgnoreCase
    } else {
        [System.StringComparison]::Ordinal
    }
    $newEntries = New-Object System.Collections.Generic.List[string]
    foreach ($current in @(Get-SupportStagingDirectories $Snapshot.TempRoot)) {
        $alreadyPresent = $false
        foreach ($existing in @($Snapshot.Entries)) {
            if ([string]::Equals($current, $existing, $comparison)) {
                $alreadyPresent = $true
                break
            }
        }
        if (-not $alreadyPresent) { $newEntries.Add($current) | Out-Null }
    }
    return @($newEntries)
}

function Assert-NoNewSupportStaging($Snapshot, [string]$Label) {
    $newEntries = @(Get-NewSupportStagingDirectories $Snapshot)
    Assert-True ($newEntries.Count -eq 0) "$Label (new global staging: $($newEntries -join ', '))"
}

function Wait-HttpPortFile {
    param(
        [Parameter(Mandatory)][string]$Path,
        $Process,
        [int]$TimeoutMilliseconds = 5000,
        [int]$PollMilliseconds = 100
    )
    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    $poll = [Math]::Max(1, $PollMilliseconds)

    while ($true) {
        if ($null -ne $Process -and [bool]$Process.HasExited) {
            throw 'deterministic HTTP endpoint exited before publishing a valid port'
        }

        if (Test-Path -LiteralPath $Path -PathType Leaf) {
            $rawPort = $null
            try {
                $rawPort = Get-Content -LiteralPath $Path -Raw -ErrorAction Stop
            } catch {
                # The fixture may still be creating or replacing the file. Retry until
                # it publishes a complete value or the bounded wait expires.
            }

            if ($null -ne $rawPort) {
                $port = 0
                if ([int]::TryParse(([string]$rawPort).Trim(), [ref]$port) -and
                    $port -ge 1 -and $port -le 65535) {
                    return [int]$port
                }
            }
        }

        if ($watch.ElapsedMilliseconds -ge $TimeoutMilliseconds) {
            throw "deterministic HTTP endpoint did not publish a valid port within ${TimeoutMilliseconds}ms"
        }

        $remaining = $TimeoutMilliseconds - $watch.ElapsedMilliseconds
        Start-Sleep -Milliseconds ([Math]::Min($poll, [Math]::Max(1, $remaining)))
    }
}

function Get-GzipText([string]$Path) {
    $input = [System.IO.File]::OpenRead($Path)
    try {
        $gzip = New-Object System.IO.Compression.GzipStream($input, [System.IO.Compression.CompressionMode]::Decompress)
        try {
            $reader = New-Object System.IO.StreamReader($gzip)
            try { return $reader.ReadToEnd() } finally { $reader.Dispose() }
        } finally { $gzip.Dispose() }
    } finally { $input.Dispose() }
}

function Write-GzipText([string]$Path, [string]$Text) {
    $output = [System.IO.File]::Create($Path)
    try {
        $gzip = New-Object System.IO.Compression.GzipStream($output, [System.IO.Compression.CompressionMode]::Compress)
        try {
            $writer = New-Object System.IO.StreamWriter($gzip)
            try { $writer.Write($Text) } finally { $writer.Dispose() }
        } finally { $gzip.Dispose() }
    } finally { $output.Dispose() }
}

function Write-RandomFile([string]$Path, [int]$Bytes, [switch]$Gzip) {
    $data = New-Object byte[] $Bytes
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($data) } finally { $rng.Dispose() }
    $output = [System.IO.File]::Create($Path)
    try {
        if ($Gzip) {
            $compressionStream = New-Object System.IO.Compression.GzipStream($output, [System.IO.Compression.CompressionMode]::Compress)
            try { $compressionStream.Write($data, 0, $data.Length) } finally { $compressionStream.Dispose() }
        } else {
            $output.Write($data, 0, $data.Length)
        }
    } finally { $output.Dispose() }
}

function Write-GzipBase64Random([string]$Path, [int]$Bytes) {
    $data = New-Object byte[] $Bytes
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($data) } finally { $rng.Dispose() }
    Write-GzipText $Path ([Convert]::ToBase64String($data))
}

function Expand-SupportBundle([string]$Bundle, [string]$Destination) {
    $null = New-Item -ItemType Directory -Path $Destination -Force
    Expand-Archive -LiteralPath $Bundle -DestinationPath $Destination -Force
    $root = Get-ChildItem -LiteralPath $Destination -Directory |
        Where-Object { $_.Name -like 'gateway-support-*' } | Select-Object -First 1
    if (-not $root) { throw "archive root missing under $Destination" }
    return $root.FullName
}

function Set-SupportEnvironment {
    param(
        [string]$GatewayLog,
        [string]$GatewayBoot,
        [AllowEmptyString()][string]$KiroCwd,
        [string]$KiroLog,
        [AllowEmptyString()][string]$HermesHome,
        [AllowEmptyString()][string]$GatewayBootError = ''
    )
    $env:HOME = $HomeFixture
    $env:USERPROFILE = $HomeFixture
    $env:GW_HOME = $GatewayHome
    $env:GW_BIN = if ($RunningOnWindows) { 'cmd.exe' } else { '/usr/bin/true' }
    $env:GW_STATE_DIR = Join-Path $GatewayHome 'state'
    $env:GW_PID = Join-Path $GatewayHome 'state\gateway.pid'
    $env:GW_LOG = $GatewayLog
    $env:GW_LOGOUT = $GatewayBoot
    $env:GW_LOGERR = if ($GatewayBootError) { $GatewayBootError } else { Join-Path $GatewayHome 'logs\unused-boot-err.log' }
    $env:GW_ADDR = "http://127.0.0.1:$HttpPort"
    $env:KIRO_CWD = $KiroCwd
    $env:KIRO_CHAT_LOG_FILE = $KiroLog
    $env:HERMES_HOME = $HermesHome
    $env:AUTH_TOKEN = $SecretToken
    $env:PII_HASH_KEY = $SecretHash
    $env:PII_ENCRYPT_KEY = $SecretEncrypt
    $env:GW_METRICS_REMOTE_WRITE_URL = 'https://metrics.example.test/api/prom/push'
    $env:GW_METRICS_REMOTE_WRITE_USER = 'fixture-user'
    $env:GW_METRICS_REMOTE_WRITE_TOKEN = $SecretRemote
    $env:GW_METRICS_REMOTE_WRITE_INTERVAL_SEC = '45'
    $env:HTTP_ADDR = '127.0.0.1:18080'
    $env:CHAT_TRACE = 'true'
    $env:KIRO_WORKER_MAX_TURNS = '20'
    $env:GW_ENV_FILE = Join-Path $script:FixtureRoot 'missing.env'
    $env:GW_OVERRIDES_FILE = Join-Path $script:FixtureRoot 'missing-overrides.env'
    $env:GW_SUPPORT_TEST_DISABLE_SAFE_OPEN = ''
    $env:GW_SUPPORT_TEST_BARRIER_SOURCE = ''
    $env:GW_SUPPORT_TEST_BARRIER_READY = ''
    $env:GW_SUPPORT_TEST_BARRIER_CONTINUE = ''
    $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_SOURCE = ''
    $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_READY = ''
    $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_CONTINUE = ''
    $env:GW_SUPPORT_TEST_FAIL_PUBLISH = ''
    $env:GW_SUPPORT_TEST_TIMESTAMP = ''
    $env:GW_SUPPORT_TEST_REPLACE_BARRIER_DESTINATION = ''
    $env:GW_SUPPORT_TEST_REPLACE_BARRIER_READY = ''
    $env:GW_SUPPORT_TEST_REPLACE_BARRIER_CONTINUE = ''
    $env:GW_SUPPORT_TEST_DEADLINE_STAGE = ''
    $env:GW_SUPPORT_TEST_DEADLINE_DELAY_MS = ''
    $env:GW_SUPPORT_TEST_DEADLINE_READY_FILE = ''
    $env:GW_SUPPORT_TEST_COMPRESSION_KIND = ''
    $env:GW_SUPPORT_TEST_COMPRESSION_DELAY_MS = ''
    $env:GW_SUPPORT_TEST_COMPRESSION_PID_FILE = ''
    $env:GW_SUPPORT_TEST_BLOCKING_KIND = ''
    $env:GW_SUPPORT_TEST_BLOCKING_DELAY_MS = ''
    $env:GW_SUPPORT_TEST_BLOCKING_PID_FILE = ''
    $env:GW_SUPPORT_TEST_BLOCKING_READY_FILE = ''
}

function ConvertTo-NativeProcessArgument {
    param([AllowEmptyString()][string]$Argument)
    if ($Argument.Length -gt 0 -and $Argument -notmatch '[\s"]') {
        return $Argument
    }

    # ProcessStartInfo.ArgumentList is absent from .NET Framework/Windows
    # PowerShell 5.1. Apply the CommandLineToArgvW-compatible escaping rules
    # explicitly so paths with spaces, empty values, quotes, and trailing
    # backslashes survive identically on both supported PowerShell editions.
    $quoted = New-Object System.Text.StringBuilder
    $backslash = [char]92
    $quote = [char]34
    $null = $quoted.Append($quote)
    $pendingBackslashes = 0
    foreach ($character in $Argument.ToCharArray()) {
        if ($character -eq $backslash) {
            $pendingBackslashes++
            continue
        }
        $copies = if ($character -eq $quote) {
            ($pendingBackslashes * 2) + 1
        } else {
            $pendingBackslashes
        }
        for ($index = 0; $index -lt $copies; $index++) {
            $null = $quoted.Append($backslash)
        }
        $pendingBackslashes = 0
        $null = $quoted.Append($character)
    }
    for ($index = 0; $index -lt ($pendingBackslashes * 2); $index++) {
        $null = $quoted.Append($backslash)
    }
    $null = $quoted.Append($quote)
    return $quoted.ToString()
}

function Start-CapturedNativeCommand {
    param(
        [string]$FilePath,
        [string[]]$ArgumentList
    )
    $startInfo = New-Object System.Diagnostics.ProcessStartInfo
    $startInfo.FileName = $FilePath
    $startInfo.Arguments = @($ArgumentList | ForEach-Object {
        ConvertTo-NativeProcessArgument ([string]$_)
    }) -join ' '
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true

    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) { throw "failed to start native command: $FilePath" }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        return [pscustomobject]@{
            Process = $process
            ProcessId = $process.Id
            StdoutTask = $stdoutTask
            StderrTask = $stderrTask
        }
    } catch {
        $process.Dispose()
        throw
    }
}

function Complete-CapturedNativeCommand($Run) {
    try {
        $Run.Process.WaitForExit()
        $stdout = $Run.StdoutTask.GetAwaiter().GetResult()
        $stderr = $Run.StderrTask.GetAwaiter().GetResult()
        return [pscustomobject]@{
            ExitCode = [int]$Run.Process.ExitCode
            Stdout = [string]$stdout
            Stderr = [string]$stderr
        }
    } finally {
        $Run.Process.Dispose()
    }
}

function Invoke-CapturedNativeCommand {
    param([string]$FilePath, [string[]]$ArgumentList)
    return Complete-CapturedNativeCommand (
        Start-CapturedNativeCommand -FilePath $FilePath -ArgumentList $ArgumentList)
}

function Get-LastNativeOutputLine([string]$Stdout) {
    $lines = @($Stdout -split '\r?\n' | Where-Object { $_.Length -gt 0 })
    if ($lines.Count -gt 0) { return [string]$lines[-1] }
    return ''
}

function Invoke-SupportRun {
    param(
        [string]$OutDir,
        [int]$MaxMb = 50,
        [AllowEmptyString()][string]$ExplicitCoworker = '',
        [int]$TimeoutSec = 180
    )
    $null = New-Item -ItemType Directory -Path $OutDir -Force
    $args = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $Wrapper, 'support', '-Out', $OutDir, '-MaxMb', "$MaxMb", '-Timeout', "$TimeoutSec", '-LogDays', '9999')
    if ($ExplicitCoworker) { $args += @('-CoworkerHome', $ExplicitCoworker) }
    $capture = Invoke-CapturedNativeCommand -FilePath $PowerShellExecutable -ArgumentList $args
    return [pscustomobject]@{
        ExitCode = $capture.ExitCode
        Bundle = Get-LastNativeOutputLine $capture.Stdout
        Stderr = $capture.Stderr
    }
}

function Start-SupportRun {
    param([string]$OutDir, [int]$MaxMb = 50, [int]$TimeoutSec = 180)
    $null = New-Item -ItemType Directory -Path $OutDir -Force
    $arguments = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $Wrapper, 'support', '-Out', $OutDir, '-MaxMb', "$MaxMb", '-Timeout', "$TimeoutSec", '-LogDays', '9999')
    $run = Start-CapturedNativeCommand -FilePath $PowerShellExecutable -ArgumentList $arguments
    return [pscustomobject]@{
        Process = $run.Process
        ProcessId = $run.ProcessId
        StdoutTask = $run.StdoutTask
        StderrTask = $run.StderrTask
        OutDir = $OutDir
    }
}

function Wait-SupportMarkerOrExit {
    param($Run, [string]$Marker, [int]$Attempts = 600)
    foreach ($attempt in 1..$Attempts) {
        if (Test-Path -LiteralPath $Marker) { return $true }
        if ($Run.Process.HasExited) { break }
        Start-Sleep -Milliseconds 50
    }
    return Test-Path -LiteralPath $Marker
}

function Complete-SupportRun($Run) {
    $capture = Complete-CapturedNativeCommand $Run
    return [pscustomobject]@{
        ExitCode = $capture.ExitCode
        Bundle = Get-LastNativeOutputLine $capture.Stdout
        Stderr = $capture.Stderr
    }
}

function Assert-NoSecretInTree([string]$Root, [string[]]$Needles, [string]$LabelPrefix) {
    foreach ($needle in $Needles) {
        $found = $false
        foreach ($file in Get-ChildItem -LiteralPath $Root -Recurse -File) {
            if ($file.Extension -eq '.gz') {
                try { if ((Get-GzipText $file.FullName).Contains($needle)) { $found = $true; break } } catch {}
            } elseif (Select-String -LiteralPath $file.FullName -SimpleMatch -Pattern $needle -Quiet -ErrorAction SilentlyContinue) {
                $found = $true; break
            }
        }
        Assert-True (-not $found) "$LabelPrefix excludes synthetic secret $needle"
    }
}

try {
    Write-Host '== native command capture contract =='
    $capture = Invoke-CapturedNativeCommand -FilePath $PowerShellExecutable -ArgumentList @(
        '-NoProfile', '-Command', "Write-Output 'native-stdout'; [Console]::Error.WriteLine('native-stderr'); exit 7"
    )
    Assert-True ($capture.ExitCode -eq 7) 'native capture preserves a nonzero exit code'
    Assert-True (($capture.Stdout -is [string]) -and
        $capture.Stdout -ceq "native-stdout$([Environment]::NewLine)") 'native capture preserves raw stdout bytes as text'
    Assert-True ($capture.Stderr -ceq "native-stderr$([Environment]::NewLine)") 'native capture preserves raw stderr without NativeCommandError serialization'
    Assert-True ($ErrorActionPreference -ceq 'Stop') 'native capture leaves strict error handling unchanged'

    $argumentScript = Join-Path ([System.IO.Path]::GetTempPath()) (
        [System.IO.Path]::GetRandomFileName() + '.ps1')
    try {
        [System.IO.File]::WriteAllText($argumentScript, 'Write-Output ($args -join ''|'')')
        $argumentCapture = Invoke-CapturedNativeCommand -FilePath $PowerShellExecutable -ArgumentList @(
            '-NoProfile', '-File', $argumentScript, 'space value', '', 'quote"value', 'trailing\'
        )
        Assert-True ($argumentCapture.ExitCode -eq 0) "native capture quoting fixture exits zero: $($argumentCapture.Stderr)"
        Assert-True ($argumentCapture.Stdout.TrimEnd() -ceq 'space value||quote"value|trailing\') 'native capture preserves cross-version argument boundaries'
    } finally {
        Remove-Item -LiteralPath $argumentScript -Force -ErrorAction SilentlyContinue
    }

    Write-Host '== deterministic HTTP port readiness contract =='
    $portReadinessRoot = Join-Path ([System.IO.Path]::GetTempPath()) (
        'gw-port-readiness-' + [System.IO.Path]::GetRandomFileName())
    $null = New-Item -ItemType Directory -Path $portReadinessRoot -Force
    $liveProcess = [pscustomobject]@{ HasExited = $false }
    try {
        foreach ($case in @(
            @{ Name = 'missing'; Content = $null },
            @{ Name = 'empty'; Content = '' },
            @{ Name = 'non-numeric'; Content = 'not-a-port' },
            @{ Name = 'zero'; Content = '0' },
            @{ Name = 'above-range'; Content = '65536' }
        )) {
            $casePath = Join-Path $portReadinessRoot "$($case.Name).port"
            if ($null -ne $case.Content) {
                [System.IO.File]::WriteAllText($casePath, [string]$case.Content)
            }
            $caseThrew = $false
            $caseMessage = ''
            try {
                $null = Wait-HttpPortFile -Path $casePath -Process $liveProcess `
                    -TimeoutMilliseconds 40 -PollMilliseconds 5
            } catch {
                $caseThrew = $true
                $caseMessage = $_.Exception.Message
            }
            Assert-True $caseThrew "port readiness rejects $($case.Name) content"
            Assert-True ($caseMessage -ceq 'deterministic HTTP endpoint did not publish a valid port within 40ms') "port readiness times out deterministically for $($case.Name) content"
        }

        $validPortPath = Join-Path $portReadinessRoot 'valid.port'
        [System.IO.File]::WriteAllText($validPortPath, " 18080`r`n")
        $validPort = Wait-HttpPortFile -Path $validPortPath -Process $liveProcess `
            -TimeoutMilliseconds 100 -PollMilliseconds 5
        Assert-True (($validPort -is [int]) -and $validPort -eq 18080) 'port readiness returns a parsed in-range integer'

        $delayedPortPath = Join-Path $portReadinessRoot 'delayed.port'
        [System.IO.File]::WriteAllText($delayedPortPath, '')
        $delayedWriter = Start-Job -ScriptBlock {
            param($Path)
            Start-Sleep -Milliseconds 100
            [System.IO.File]::WriteAllText($Path, '18081')
        } -ArgumentList $delayedPortPath
        try {
            $delayedPort = Wait-HttpPortFile -Path $delayedPortPath -Process $liveProcess `
                -TimeoutMilliseconds 5000 -PollMilliseconds 10
            Assert-True (($delayedPort -is [int]) -and $delayedPort -eq 18081) 'port readiness waits past an existing empty file until valid digits are readable'
        } finally {
            $null = Wait-Job -Job $delayedWriter -Timeout 5 -ErrorAction SilentlyContinue
            Remove-Job -Job $delayedWriter -Force -ErrorAction SilentlyContinue
        }

        $earlyExitPath = Join-Path $portReadinessRoot 'early-exit.port'
        $earlyExitProcess = [pscustomobject]@{ HasExited = $true }
        $earlyExitWatch = [System.Diagnostics.Stopwatch]::StartNew()
        $earlyExitMessage = ''
        try {
            $null = Wait-HttpPortFile -Path $earlyExitPath -Process $earlyExitProcess `
                -TimeoutMilliseconds 1000 -PollMilliseconds 10
        } catch {
            $earlyExitMessage = $_.Exception.Message
        } finally {
            $earlyExitWatch.Stop()
        }
        Assert-True ($earlyExitMessage -ceq 'deterministic HTTP endpoint exited before publishing a valid port') 'port readiness reports early server exit'
        Assert-True ($earlyExitWatch.ElapsedMilliseconds -lt 500) 'port readiness observes early server exit before its timeout'
    } finally {
        Remove-Item -LiteralPath $portReadinessRoot -Recurse -Force -ErrorAction SilentlyContinue
    }

    Write-Host '== per-request timeout budget contract =='
    $wrapperSource = Get-Content -LiteralPath $Wrapper -Raw
    $wrapperTokens = $null
    $wrapperParseErrors = $null
    $wrapperAst = [System.Management.Automation.Language.Parser]::ParseInput(
        $wrapperSource, [ref]$wrapperTokens, [ref]$wrapperParseErrors)
    $requestTimeoutAst = $wrapperAst.Find({
        param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
            $node.Name -ceq 'Get-SupportRequestTimeout'
    }, $true)
    Assert-True ($wrapperParseErrors.Count -eq 0) 'PowerShell wrapper parses for request-timeout contract extraction'
    Assert-True ($null -ne $requestTimeoutAst) 'request-timeout helper is present in the support collector'
    if ($null -ne $requestTimeoutAst) {
        $requestTimeoutResults = @(& {
            param([string]$Definition)
            . ([scriptblock]::Create($Definition))
            function Test-Deadline { param([string]$Stage) }
            function Throw-SupportTimeout { param([string]$Stage) throw "expired:$Stage" }

            foreach ($case in @(
                @{ Name = 'caps large remaining budget'; Timeout = 10.0; Elapsed = 0.0; Want = 5; WantThrow = $false },
                @{ Name = 'rounds fractional remaining budget down'; Timeout = 10.0; Elapsed = 5.25; Want = 4; WantThrow = $false },
                @{ Name = 'rejects subsecond remaining budget'; Timeout = 10.0; Elapsed = 9.25; Want = 0; WantThrow = $true },
                @{ Name = 'rejects expired budget'; Timeout = 10.0; Elapsed = 10.25; Want = 0; WantThrow = $true }
            )) {
                $timeoutSec = $case.Timeout
                $deadlineStopwatch = [pscustomobject]@{
                    Elapsed = [pscustomobject]@{ TotalSeconds = $case.Elapsed }
                }
                try {
                    $actual = Get-SupportRequestTimeout 'fixture-request'
                    [pscustomobject]@{
                        Name = $case.Name; Actual = $actual; Want = $case.Want
                        Threw = $false; WantThrow = $case.WantThrow
                    }
                } catch {
                    [pscustomobject]@{
                        Name = $case.Name; Actual = 0; Want = $case.Want
                        Threw = $true; WantThrow = $case.WantThrow
                    }
                }
            }
        } $requestTimeoutAst.Extent.Text)
        foreach ($result in $requestTimeoutResults) {
            Assert-True ($result.Threw -eq $result.WantThrow) "request timeout $($result.Name)"
            if (-not $result.WantThrow) {
                Assert-True ($result.Actual -eq $result.Want) "request timeout $($result.Name) returns $($result.Want)s"
            }
        }
    }
    foreach ($requestStage in @(
        'health-request', 'admin-snapshot-request', 'metrics-request', 'capture-request'
    )) {
        $requestCallPattern = "Get-SupportRequestTimeout\s+'$([regex]::Escape($requestStage))'"
        Assert-True ([regex]::Matches($wrapperSource, $requestCallPattern).Count -eq 1) "request site $requestStage uses the bounded timeout helper"
    }

    Write-Host '== support exception diagnostic contract =='
    $diagnosticAst = $wrapperAst.Find({
        param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
            $node.Name -ceq 'Format-SupportExceptionDiagnostic'
    }, $true)
    $copyRedactedLogAst = $wrapperAst.Find({
        param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
            $node.Name -ceq 'Copy-RedactedSupportLog'
    }, $true)
    Assert-True ($null -ne $diagnosticAst) 'support exception diagnostic helper is present'
    Assert-True ($null -ne $copyRedactedLogAst) 'plain redaction control flow is present for stage extraction'

    Write-Host '== plain redaction worker compatibility =='
    $plainRedactionAssignmentAst = if ($null -ne $copyRedactedLogAst) {
        $copyRedactedLogAst.Find({
            param($node)
            $node -is [System.Management.Automation.Language.AssignmentStatementAst] -and
                $node.Left.Extent.Text -ceq '$plainRedactionWork'
        }, $true)
    } else { $null }
    Assert-True ($null -ne $plainRedactionAssignmentAst) 'plain redaction deadline worker is present'
    $deadlineContractAsts = @(
        'Throw-SupportTimeout',
        'Get-SupportTestWorkStage',
        'Get-SupportTestWorkPidFile',
        'Test-Deadline',
        'Invoke-SupportDeadlineJob'
    ) | ForEach-Object {
        $functionName = $_
        $wrapperAst.Find({
            param($node)
            $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $node.Name -ceq $functionName
        }, $true)
    }
    Assert-True ($deadlineContractAsts.Count -eq 5) 'plain worker production deadline helpers are present'
    $plainCopyBoundaryAsts = @(
        'Test-SupportForcedPublishFailure',
        'Publish-SupportArtifact',
        'New-SafeSourceSnapshot'
    ) | ForEach-Object {
        $functionName = $_
        $wrapperAst.Find({
            param($node)
            $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $node.Name -ceq $functionName
        }, $true)
    }
    Assert-True ($plainCopyBoundaryAsts.Count -eq 3) 'plain copy production snapshot and publication helpers are present'
    if ($null -ne $plainRedactionAssignmentAst -and $deadlineContractAsts.Count -eq 5) {
        $plainWorkerTempRoot = [System.IO.Path]::GetTempPath()
        if (-not $RunningOnWindows -and $plainWorkerTempRoot.StartsWith('/var/')) {
            $plainWorkerTempRoot = '/private' + $plainWorkerTempRoot
        }
        $plainWorkerRoot = Join-Path $plainWorkerTempRoot (
            'gw plain worker with spaces ' + [System.IO.Path]::GetRandomFileName())
        $null = New-Item -ItemType Directory -Path $plainWorkerRoot -Force
        $plainWorkerJob = $null
        $savedPlainWorkerEnvironment = @{}
        try {
            foreach ($name in @(
                'TEMP', 'TMP', 'TMPDIR',
                'GW_SUPPORT_TEST_COMPRESSION_KIND',
                'GW_SUPPORT_TEST_BLOCKING_KIND',
                'GW_SUPPORT_TEST_BLOCKING_PID_FILE',
                'GW_SUPPORT_TEST_BLOCKING_READY_FILE',
                'GW_SUPPORT_TEST_DEADLINE_STAGE',
                'GW_SUPPORT_TEST_FAIL_PUBLISH',
                'GW_SUPPORT_TEST_REPLACE_BARRIER_DESTINATION',
                'GW_SUPPORT_TEST_DISABLE_SAFE_OPEN'
            )) {
                $savedPlainWorkerEnvironment[$name] = [Environment]::GetEnvironmentVariable(
                    $name, 'Process')
            }
            $env:TEMP = $plainWorkerRoot
            $env:TMP = $plainWorkerRoot
            if (-not $RunningOnWindows) { $env:TMPDIR = $plainWorkerRoot }
            $env:GW_SUPPORT_TEST_COMPRESSION_KIND = ''
            $env:GW_SUPPORT_TEST_BLOCKING_KIND = ''
            $env:GW_SUPPORT_TEST_BLOCKING_PID_FILE = ''
            $env:GW_SUPPORT_TEST_BLOCKING_READY_FILE = ''
            $env:GW_SUPPORT_TEST_DEADLINE_STAGE = ''
            $env:GW_SUPPORT_TEST_FAIL_PUBLISH = ''
            $env:GW_SUPPORT_TEST_REPLACE_BARRIER_DESTINATION = ''
            $env:GW_SUPPORT_TEST_DISABLE_SAFE_OPEN = ''

            $plainWorkerSource = Join-Path $plainWorkerRoot 'source-snapshots\source-1'
            $plainWorkerTemporary = Join-Path $plainWorkerRoot (
                'gateway-support-host-20260728-000000Z\logs\gateway\gateway.log.partial-direct')
            $null = New-Item -ItemType Directory -Path (
                Split-Path -Parent $plainWorkerSource) -Force
            $null = New-Item -ItemType Directory -Path (
                Split-Path -Parent $plainWorkerTemporary) -Force
            $plainWorkerSecret = 'plain-worker-remote-secret-7711'
            $plainWorkerText = @(
                'gateway current safe',
                'AUTH_TOKEN=plain-worker-auth-secret-1122',
                'Authorization: Bearer plain-worker-bearer-secret-2233',
                'x-api-key: plain-worker-api-secret-3344',
                'PII_HASH_KEY=plain-worker-hash-secret-4455',
                'PII_ENCRYPT_KEY=plain-worker-encrypt-secret-5566',
                "GW_METRICS_REMOTE_WRITE_TOKEN=$plainWorkerSecret"
            ) -join "`r`n"
            $plainWorkerText += "`r`n"
            $plainWorkerPayload = [System.Text.Encoding]::UTF8.GetBytes($plainWorkerText)
            $plainWorkerBytes = New-Object byte[] (3 + $plainWorkerPayload.Length)
            $plainWorkerBytes[0] = 0xEF
            $plainWorkerBytes[1] = 0xBB
            $plainWorkerBytes[2] = 0xBF
            [System.Array]::Copy(
                $plainWorkerPayload, 0, $plainWorkerBytes, 3, $plainWorkerPayload.Length)
            [System.IO.File]::WriteAllBytes($plainWorkerSource, $plainWorkerBytes)

            $plainRedactionWork = $plainRedactionAssignmentAst.Right.Expression.ScriptBlock.GetScriptBlock()
            $plainWorkerJob = Start-Job -ScriptBlock $plainRedactionWork -ArgumentList @(
                $plainWorkerSource,
                $plainWorkerTemporary,
                (Join-Path $RepoRoot 'scripts\lib\redact.ps1'),
                '', 0, '', ''
            )
            $null = Wait-Job -Job $plainWorkerJob -Timeout 10
            $plainWorkerFailure = ''
            if ($plainWorkerJob.State -eq 'Completed') {
                try {
                    $null = @(Receive-Job -Job $plainWorkerJob -ErrorAction Stop)
                } catch {
                    $plainWorkerFailure = $_.Exception.GetType().FullName
                }
            } else {
                $reason = @($plainWorkerJob.ChildJobs | ForEach-Object {
                    $_.JobStateInfo.Reason
                } | Where-Object { $null -ne $_ } | Select-Object -First 1)
                $plainWorkerFailure = if ($reason.Count -gt 0) {
                    $reason[0].GetType().FullName
                } else { [string]$plainWorkerJob.State }
            }

            Assert-True (
                $plainWorkerJob.State -eq 'Completed' -and -not $plainWorkerFailure
            ) "direct plain worker handles production content and space-containing TEMP (failure: $plainWorkerFailure)"
            $plainWorkerOutput = if (Test-Path -LiteralPath $plainWorkerTemporary -PathType Leaf) {
                [System.IO.File]::ReadAllText($plainWorkerTemporary)
            } else { '' }
            $plainWorkerExpected = @(
                'gateway current safe',
                'AUTH_TOKEN=[REDACTED]',
                'Authorization: [REDACTED]',
                'x-api-key: [REDACTED]',
                'PII_HASH_KEY=[REDACTED]',
                'PII_ENCRYPT_KEY=[REDACTED]',
                'GW_METRICS_REMOTE_WRITE_TOKEN=[REDACTED]'
            ) -join [Environment]::NewLine
            $plainWorkerExpected += [Environment]::NewLine
            Assert-True ($plainWorkerOutput -ceq $plainWorkerExpected) 'plain worker preserves BOM detection and line-oriented redaction'
            Assert-True (-not $plainWorkerOutput.Contains($plainWorkerSecret)) 'plain worker excludes the original remote-write token'
            Assert-NoUtf8Bom $plainWorkerTemporary 'plain worker publishes UTF-8 without a BOM'

            $plainHelperTemporary = Join-Path $plainWorkerRoot (
                'gateway-support-host-20260728-000000Z\logs\gateway\gateway.log.partial-helper')
            $deadlineContractDefinitions = @($deadlineContractAsts |
                ForEach-Object { $_.Extent.Text }) -join [Environment]::NewLine
            $plainHelperResult = & {
                param(
                    [string]$Definitions,
                    [scriptblock]$Work,
                    [string]$SnapshotPath,
                    [string]$TemporaryPath,
                    [string]$RedactLibrary
                )
                . ([scriptblock]::Create($Definitions))
                $timeoutSec = 180
                $deadlineStopwatch = [System.Diagnostics.Stopwatch]::StartNew()
                $deadlineInjectedStages = @{}
                $deadlineWorkState = @{ Armed = $false }
                try {
                    $null = @(Invoke-SupportDeadlineJob -Work $Work -Arguments @(
                        $SnapshotPath, $TemporaryPath, $RedactLibrary,
                        '', '', '', ''
                    ) -Stage 'plain-redaction')
                    [pscustomobject]@{
                        Success = $true
                        Failure = ''
                    }
                } catch {
                    $baseException = $_.Exception
                    while ($baseException.InnerException) {
                        $baseException = $baseException.InnerException
                    }
                    [pscustomobject]@{
                        Success = $false
                        Failure = '{0}/0x{1:X8}/{2}/{3}' -f @(
                            $baseException.GetType().FullName,
                            [int]$baseException.HResult,
                            $_.FullyQualifiedErrorId,
                            $_.InvocationInfo.ScriptLineNumber)
                    }
                }
            } $deadlineContractDefinitions $plainRedactionWork `
                $plainWorkerSource $plainHelperTemporary `
                (Join-Path $RepoRoot 'scripts\lib\redact.ps1')
            Assert-True $plainHelperResult.Success "deadline helper completes the same plain worker (failure: $($plainHelperResult.Failure))"
            $plainHelperOutput = if (Test-Path -LiteralPath $plainHelperTemporary -PathType Leaf) {
                [System.IO.File]::ReadAllText($plainHelperTemporary)
            } else { '' }
            Assert-True ($plainHelperOutput -ceq $plainWorkerExpected) 'deadline helper preserves the production plain-redaction output'
            Assert-True (-not $plainHelperOutput.Contains($plainWorkerSecret)) 'deadline helper excludes the original remote-write token'
            Assert-NoUtf8Bom $plainHelperTemporary 'deadline helper publishes UTF-8 without a BOM'

            if ($plainCopyBoundaryAsts.Count -eq 3 -and
                $null -ne $diagnosticAst -and $null -ne $copyRedactedLogAst) {
                $plainCopyRoot = Join-Path $plainWorkerRoot 'actual copy boundary'
                $plainCopySource = Join-Path $plainCopyRoot 'configured gateway.log'
                $plainCopyRelative = 'logs\gateway\gateway.log'
                $plainCopyDefinitionsPath = Join-Path $plainCopyRoot 'production-copy-definitions.ps1'
                $plainCopyProcessScript = Join-Path $plainCopyRoot 'fresh-copy-process.ps1'
                $null = New-Item -ItemType Directory -Path $plainCopyRoot -Force
                [System.IO.File]::WriteAllBytes($plainCopySource, $plainWorkerBytes)

                $plainCopyDefinitions = @($deadlineContractAsts |
                    ForEach-Object { $_.Extent.Text })
                $plainCopyDefinitions += @($plainCopyBoundaryAsts |
                    ForEach-Object { $_.Extent.Text })
                $productionDiagnosticDefinition = $diagnosticAst.Extent.Text.Replace(
                    'function Format-SupportExceptionDiagnostic',
                    'function Format-ProductionSupportExceptionDiagnostic')
                $productionCopyDefinition = $copyRedactedLogAst.Extent.Text.Replace(
                    '$PSScriptRoot', '$RepoScriptsRoot')
                $plainCopyEncoding = New-Object System.Text.UTF8Encoding($false)
                [System.IO.File]::WriteAllText(
                    $plainCopyDefinitionsPath,
                    ((@($plainCopyDefinitions) + @(
                        $productionDiagnosticDefinition,
                        $productionCopyDefinition
                    )) -join [Environment]::NewLine),
                    $plainCopyEncoding)

                $plainCopyProcessSource = @'
param(
    [string]$DefinitionsPath,
    [string]$SafeOpenLibraryPath,
    [string]$RepoScriptsRoot,
    [string]$Source,
    [string]$RelativePath,
    [string]$Root,
    [string]$ExpectedBase64,
    [string]$Secret
)
$ErrorActionPreference = 'Stop'
try {
    . $SafeOpenLibraryPath
    . $DefinitionsPath

    $failureState = @{
                        Type = ''
                        HResult = 0
                        FullyQualifiedErrorId = ''
                        ScriptLineNumber = 0
                    }
                    function Format-SupportExceptionDiagnostic {
                        param($ErrorRecord)
                        try {
                            $baseException = $ErrorRecord.Exception
                            while ($baseException.InnerException) {
                                $baseException = $baseException.InnerException
                            }
                            $failureState.Type = $baseException.GetType().FullName
                            $failureState.HResult = [int]$baseException.HResult
                            $failureState.FullyQualifiedErrorId = [string]$ErrorRecord.FullyQualifiedErrorId
                            $failureState.ScriptLineNumber = [int]$ErrorRecord.InvocationInfo.ScriptLineNumber
                        } catch {}
                        return Format-ProductionSupportExceptionDiagnostic $ErrorRecord
                    }

                    $timeoutSec = 180
                    $deadlineStopwatch = [System.Diagnostics.Stopwatch]::StartNew()
                    $deadlineInjectedStages = @{}
                    $deadlineWorkState = @{ Armed = $false }
                    $collectionWarnings = New-Object System.Collections.Generic.List[string]
                    $snapshotState = @{ Sequence = 0 }
                    $staging = Join-Path $Root 'staging'
                    $bundleRoot = Join-Path $Root 'bundle'
                    $null = New-Item -ItemType Directory -Path $staging,$bundleRoot -Force
                    $safeOpenAvailable = $false
                    try {
                        Initialize-SupportSafeOpen
                        $safeOpenAvailable = $true
                    } catch {
                        $safeOpenAvailable = $false
                    }

                    # This is deliberately the fresh process's first source
                    # snapshot and first Start-Job invocation.
                    $returned = Copy-RedactedSupportLog `
                        $Source $RelativePath 'Gateway current log' $true
                    $destination = Join-Path $bundleRoot $RelativePath
                    $expected = [System.Text.Encoding]::UTF8.GetString(
                        [Convert]::FromBase64String($ExpectedBase64))
                    $output = if (Test-Path -LiteralPath $destination -PathType Leaf) {
                        [System.IO.File]::ReadAllText($destination)
                    } else { '' }
                    $hasBom = $false
                    if (Test-Path -LiteralPath $destination -PathType Leaf) {
                        $bytes = [System.IO.File]::ReadAllBytes($destination)
                        $hasBom = $bytes.Length -ge 3 -and
                            $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and
                            $bytes[2] -eq 0xBF
                    }
                    $result = [pscustomobject]@{
                        HarnessFailure = $false
                        Returned = $returned -eq $true
                        DestinationExists = Test-Path -LiteralPath $destination -PathType Leaf
                        OutputMatches = $output -ceq $expected
                        SecretAbsent = -not $output.Contains($Secret)
                        HasBom = $hasBom
                        PartialCount = @(Get-ChildItem -LiteralPath $bundleRoot `
                            -Filter '*.partial-*' -Recurse -ErrorAction SilentlyContinue).Count
                        Warning = @($collectionWarnings) -join '; '
                        Failure = if ($failureState.Type) {
                            '{0}/0x{1:X8}/{2}/{3}' -f @(
                                $failureState.Type,
                                $failureState.HResult,
                                $failureState.FullyQualifiedErrorId,
                                $failureState.ScriptLineNumber)
                        } else { '' }
                    }
} catch {
    $baseException = $_.Exception
    while ($baseException.InnerException) {
        $baseException = $baseException.InnerException
    }
    $result = [pscustomobject]@{
        HarnessFailure = $true
        Returned = $false
        DestinationExists = $false
        OutputMatches = $false
        SecretAbsent = $true
        HasBom = $false
        PartialCount = -1
        Warning = ''
        Failure = '{0}/0x{1:X8}/{2}/{3}' -f @(
            $baseException.GetType().FullName,
            [int]$baseException.HResult,
            $_.FullyQualifiedErrorId,
            $_.InvocationInfo.ScriptLineNumber)
    }
}
$result | ConvertTo-Json -Compress
'@
                [System.IO.File]::WriteAllText(
                    $plainCopyProcessScript, $plainCopyProcessSource,
                    $plainCopyEncoding)
                $plainCopyExpectedBase64 = [Convert]::ToBase64String(
                    [System.Text.Encoding]::UTF8.GetBytes($plainWorkerExpected))
                $plainCopyCapture = Invoke-CapturedNativeCommand `
                    -FilePath $PowerShellExecutable -ArgumentList @(
                        '-NoProfile', '-ExecutionPolicy', 'Bypass',
                        '-File', $plainCopyProcessScript,
                        '-DefinitionsPath', $plainCopyDefinitionsPath,
                        '-SafeOpenLibraryPath', $SafeOpenLibrary,
                        '-RepoScriptsRoot', (Join-Path $RepoRoot 'scripts'),
                        '-Source', $plainCopySource,
                        '-RelativePath', $plainCopyRelative,
                        '-Root', $plainCopyRoot,
                        '-ExpectedBase64', $plainCopyExpectedBase64,
                        '-Secret', $plainWorkerSecret)
                $plainCopyResult = $null
                try {
                    $plainCopyResult = Get-LastNativeOutputLine `
                        $plainCopyCapture.Stdout | ConvertFrom-Json -ErrorAction Stop
                } catch {}
                $plainCopyProcessFailure = if ($null -eq $plainCopyResult) {
                    'no-result'
                } elseif ($plainCopyResult.HarnessFailure) {
                    [string]$plainCopyResult.Failure
                } else { '' }
                Assert-True (
                    $plainCopyCapture.ExitCode -eq 0 -and -not $plainCopyProcessFailure
                ) "fresh plain-copy process initializes the production boundary (failure: $plainCopyProcessFailure)"
                if ($null -ne $plainCopyResult) {
                    $plainCopyContext = 'warning [{0}], failure [{1}]' -f @(
                        $plainCopyResult.Warning,
                        $plainCopyResult.Failure)
                    Assert-True $plainCopyResult.Returned "first-job production copy returns success ($plainCopyContext)"
                    Assert-True $plainCopyResult.DestinationExists "first-job production copy publishes its destination ($plainCopyContext)"
                    Assert-True $plainCopyResult.OutputMatches 'first-job production copy preserves expected redaction output'
                    Assert-True $plainCopyResult.SecretAbsent 'first-job production copy excludes the original remote-write token'
                    Assert-True (-not $plainCopyResult.HasBom) 'first-job production copy publishes UTF-8 without a BOM'
                    Assert-True ($plainCopyResult.PartialCount -eq 0) "first-job production copy leaves no partial artifact ($plainCopyContext)"
                    Assert-True (-not $plainCopyResult.Warning) "first-job production copy emits no warning ($plainCopyContext)"
                } else {
                    foreach ($label in @(
                        'returns success',
                        'publishes its destination',
                        'preserves expected redaction output',
                        'excludes the original remote-write token',
                        'publishes UTF-8 without a BOM',
                        'leaves no partial artifact',
                        'emits no warning'
                    )) {
                        Fail-With "first-job production copy $label (fresh process returned no diagnostic result)"
                    }
                }
            }
        } finally {
            if ($null -ne $plainWorkerJob) {
                if ($plainWorkerJob.State -in @('NotStarted','Running','Blocked')) {
                    Stop-Job -Job $plainWorkerJob -ErrorAction SilentlyContinue
                }
                Remove-Job -Job $plainWorkerJob -Force -ErrorAction SilentlyContinue
            }
            foreach ($name in $savedPlainWorkerEnvironment.Keys) {
                [Environment]::SetEnvironmentVariable(
                    $name, $savedPlainWorkerEnvironment[$name], 'Process')
            }
            Remove-Item -LiteralPath $plainWorkerRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
    if ($null -ne $diagnosticAst) {
        $diagnosticCases = @(& {
            param([string]$Definition)
            . ([scriptblock]::Create($Definition))
            $messageSecret = 'diagnostic-message-secret-5511'
            $windowsPath = 'C:\diagnostic-private\gateway.log'
            $unixPath = '/tmp/diagnostic-private/gateway.log'
            $logContent = 'AUTH_TOKEN=diagnostic-log-secret-6622'

            $invalidOperationRecord = $null
            try {
                throw [System.InvalidOperationException]::new(
                    "$messageSecret $windowsPath $unixPath $logContent")
            } catch { $invalidOperationRecord = $_ }

            $nestedWin32 = [System.Exception]::new(
                "$messageSecret outer $windowsPath",
                [System.ComponentModel.Win32Exception]::new(
                    32, "$messageSecret inner $unixPath $logContent"))
            $zeroHresult = [System.Runtime.InteropServices.ExternalException]::new(
                "$messageSecret $windowsPath $logContent", 0)
            $bareException = [System.Exception]::new(
                "$messageSecret $unixPath $logContent")

            foreach ($case in @(
                @{ Name = 'negative HRESULT error record'; Input = $invalidOperationRecord; Expected = 'System.InvalidOperationException, HRESULT 0x80131509' },
                @{ Name = 'deepest Win32 exception'; Input = $nestedWin32; Expected = 'System.ComponentModel.Win32Exception, HRESULT 0x80004005, native error 32' },
                @{ Name = 'zero HRESULT'; Input = $zeroHresult; Expected = 'System.Runtime.InteropServices.ExternalException, HRESULT 0x00000000' },
                @{ Name = 'bare exception'; Input = $bareException; Expected = 'System.Exception, HRESULT 0x80131500' },
                @{ Name = 'null record'; Input = $null; Expected = 'unknown exception, HRESULT 0x00000000' },
                @{ Name = 'null exception'; Input = [pscustomobject]@{ Exception = $null }; Expected = 'unknown exception, HRESULT 0x00000000' },
                @{ Name = 'missing exception property'; Input = [pscustomobject]@{ Value = 7 }; Expected = 'unknown exception, HRESULT 0x00000000' },
                @{ Name = 'non-exception shape'; Input = [pscustomobject]@{ Exception = [pscustomobject]@{ InnerException = 'not-an-exception' } }; Expected = 'unknown exception, HRESULT 0x00000000' }
            )) {
                $threw = $false
                $actual = ''
                try { $actual = Format-SupportExceptionDiagnostic $case.Input } catch { $threw = $true }
                [pscustomobject]@{
                    Name = $case.Name
                    Actual = $actual
                    Expected = $case.Expected
                    Threw = $threw
                }
            }
        } $diagnosticAst.Extent.Text)
        $diagnosticForbidden = @(
            'diagnostic-message-secret-5511', 'C:\diagnostic-private\gateway.log',
            '/tmp/diagnostic-private/gateway.log', 'AUTH_TOKEN=diagnostic-log-secret-6622'
        )
        foreach ($case in $diagnosticCases) {
            Assert-True (-not $case.Threw) "diagnostic formatter is total for $($case.Name)"
            Assert-True ($case.Actual -ceq $case.Expected) "diagnostic formatter emits exact allowed shape for $($case.Name)"
            Assert-True ($case.Actual -cmatch '^(?:unknown exception|[A-Za-z0-9_.+`]+), HRESULT 0x[0-9A-F]{8}(?:, native error [0-9]+)?$') "diagnostic formatter emits an exact type/HRESULT/native-code grammar for $($case.Name)"
            foreach ($needle in $diagnosticForbidden) {
                Assert-True (-not $case.Actual.Contains($needle)) "diagnostic formatter excludes protected text for $($case.Name)"
            }
        }
    }

    if ($null -ne $diagnosticAst -and $null -ne $copyRedactedLogAst) {
        $diagnosticControlRoot = Join-Path ([System.IO.Path]::GetTempPath()) (
            'gw-diagnostic-control-' + [System.IO.Path]::GetRandomFileName())
        $stageResults = @(& {
            param([string]$FormatterDefinition, [string]$CopyDefinition, [string]$Root)
            . ([scriptblock]::Create($FormatterDefinition))
            # ScriptBlock::Create has no script-file anchor. Substitute only
            # that automatic path with the controlled harness root; all
            # production control flow remains the extracted function text.
            . ([scriptblock]::Create($CopyDefinition.Replace('$PSScriptRoot', '$Root')))
            foreach ($case in @(
                @{ Stage = 'plain-redaction'; Type = 'System.InvalidOperationException'; HResult = '80131509' },
                @{ Stage = 'plain-publish'; Type = 'System.InvalidOperationException'; HResult = '80131509' },
                @{ Stage = 'plain-metadata'; Type = 'System.IO.IOException'; HResult = '80131620' }
            )) {
                $stageState = @{ Stage = $case.Stage }
                $caseRoot = Join-Path $Root $case.Stage
                $null = New-Item -ItemType Directory -Path $caseRoot -Force
                $bundleRoot = $caseRoot
                $collectionWarnings = New-Object System.Collections.Generic.List[string]
                $snapshotPath = Join-Path $caseRoot 'collector-owned.snapshot'
                [System.IO.File]::WriteAllText($snapshotPath, 'safe snapshot bytes')
                $caseMessage = "diagnostic-stage-secret-7733 C:\private\source.log /tmp/private.partial AUTH_TOKEN=raw-stage-secret-8844"

                function New-SafeSourceSnapshot {
                    param($Source, $Label, $WarnIfMissing)
                    [pscustomobject]@{
                        Path = $snapshotPath
                        LastWriteTimeUtc = [DateTime]::UtcNow
                    }
                }
                function Invoke-SupportDeadlineJob {
                    param($Work, $Arguments, $Stage)
                    [System.IO.File]::WriteAllText([string]$Arguments[1], 'redacted temporary')
                    if ($stageState.Stage -ceq 'plain-redaction') {
                        throw [System.InvalidOperationException]::new($caseMessage)
                    }
                }
                function Test-SupportForcedPublishFailure { param($Kind); return $false }
                function Publish-SupportArtifact {
                    param($Temporary, $Destination, $FailureKind)
                    if ($stageState.Stage -ceq 'plain-publish') {
                        throw [System.InvalidOperationException]::new($caseMessage)
                    }
                    [System.IO.File]::Move($Temporary, $Destination)
                }
                function Get-Item {
                    param($LiteralPath)
                    throw [System.IO.IOException]::new($caseMessage)
                }

                $returned = Copy-RedactedSupportLog $snapshotPath 'gateway.log' 'Gateway current log' $true
                $warning = if ($collectionWarnings.Count -eq 1) {
                    [string]$collectionWarnings[0]
                } else { '' }
                [pscustomobject]@{
                    Stage = $case.Stage
                    Expected = "Gateway current log redaction failed at $($case.Stage) ($($case.Type), HRESULT 0x$($case.HResult))"
                    Warning = $warning
                    Returned = $returned
                    PartialCount = @(Get-ChildItem -LiteralPath $caseRoot -Filter '*.partial-*' -ErrorAction SilentlyContinue).Count
                    DestinationExists = [System.IO.File]::Exists((Join-Path $caseRoot 'gateway.log'))
                }
            }
        } $diagnosticAst.Extent.Text $copyRedactedLogAst.Extent.Text $diagnosticControlRoot)
        foreach ($case in $stageResults) {
            Assert-True ($case.Returned -eq $false) "$($case.Stage) diagnostic keeps the failure best effort"
            Assert-True ($case.Warning -ceq $case.Expected) "$($case.Stage) diagnostic selects the exact lifecycle stage and exception shape"
            Assert-True ($case.PartialCount -eq 0) "$($case.Stage) diagnostic removes sibling partial artifacts"
            foreach ($needle in @(
                'diagnostic-stage-secret-7733', 'C:\private\source.log',
                '/tmp/private.partial', 'AUTH_TOKEN=raw-stage-secret-8844'
            )) {
                Assert-True (-not $case.Warning.Contains($needle)) "$($case.Stage) diagnostic excludes protected failure text"
            }
        }
        $redactionStage = @($stageResults | Where-Object Stage -eq 'plain-redaction')[0]
        $publishStage = @($stageResults | Where-Object Stage -eq 'plain-publish')[0]
        $metadataStage = @($stageResults | Where-Object Stage -eq 'plain-metadata')[0]
        Assert-True (-not $redactionStage.DestinationExists) 'plain-redaction failure publishes no destination'
        Assert-True (-not $publishStage.DestinationExists) 'plain-publish failure publishes no destination'
        Assert-True $metadataStage.DestinationExists 'plain-metadata failure preserves the already-published redacted destination'

        $failSafeResults = @(& {
            param([string]$CopyDefinition, [string]$Root)
            . ([scriptblock]::Create($CopyDefinition.Replace('$PSScriptRoot', '$Root')))
            foreach ($timeoutCase in @($false, $true)) {
                $caseName = if ($timeoutCase) { 'timeout' } else { 'format-failure' }
                $caseRoot = Join-Path $Root $caseName
                $null = New-Item -ItemType Directory -Path $caseRoot -Force
                $bundleRoot = $caseRoot
                $collectionWarnings = New-Object System.Collections.Generic.List[string]
                $state = @{
                    TimeoutCase = $timeoutCase
                    FormatterCalled = $false
                    FormatterSawPartial = $false
                }
                $snapshotPath = Join-Path $caseRoot 'collector-owned.snapshot'
                [System.IO.File]::WriteAllText($snapshotPath, 'safe snapshot bytes')

                function New-SafeSourceSnapshot {
                    param($Source, $Label, $WarnIfMissing)
                    [pscustomobject]@{ Path = $snapshotPath; LastWriteTimeUtc = [DateTime]::UtcNow }
                }
                function Invoke-SupportDeadlineJob {
                    param($Work, $Arguments, $Stage)
                    [System.IO.File]::WriteAllText([string]$Arguments[1], 'partial bytes')
                    if ($state.TimeoutCase) {
                        throw 'support bundle: timed out after 1 seconds at stage ''plain-redaction''; staging will be cleaned'
                    }
                    throw [System.InvalidOperationException]::new('formatter-failure-secret-9955')
                }
                function Format-SupportExceptionDiagnostic {
                    $state.FormatterCalled = $true
                    $state.FormatterSawPartial = @(
                        Get-ChildItem -LiteralPath $caseRoot -Filter '*.partial-*' -ErrorAction SilentlyContinue
                    ).Count -gt 0
                    throw 'diagnostic formatter failed'
                }
                function Test-SupportForcedPublishFailure { param($Kind); return $false }
                function Publish-SupportArtifact { throw 'unexpected publication' }

                $escaped = $false
                $escapedMessage = ''
                $returned = $null
                try {
                    $returned = Copy-RedactedSupportLog $snapshotPath 'gateway.log' 'Gateway current log' $true
                } catch {
                    $escaped = $true
                    $escapedMessage = $_.Exception.Message
                }
                [pscustomobject]@{
                    Name = $caseName
                    Escaped = $escaped
                    EscapedMessage = $escapedMessage
                    Returned = $returned
                    FormatterCalled = $state.FormatterCalled
                    FormatterSawPartial = $state.FormatterSawPartial
                    PartialCount = @(Get-ChildItem -LiteralPath $caseRoot -Filter '*.partial-*' -ErrorAction SilentlyContinue).Count
                    Warning = if ($collectionWarnings.Count -eq 1) { [string]$collectionWarnings[0] } else { '' }
                }
            }
        } $copyRedactedLogAst.Extent.Text $diagnosticControlRoot)
        $formatFailure = @($failSafeResults | Where-Object Name -eq 'format-failure')[0]
        Assert-True (-not $formatFailure.Escaped) 'diagnostic formatting failure does not replace an ordinary best-effort failure'
        Assert-True ($formatFailure.Returned -eq $false) 'diagnostic formatting failure preserves the false result'
        Assert-True $formatFailure.FormatterCalled 'ordinary failure attempts best-effort diagnostic formatting'
        Assert-True (-not $formatFailure.FormatterSawPartial) 'ordinary failure removes its partial before diagnostic formatting'
        Assert-True ($formatFailure.PartialCount -eq 0) 'ordinary formatter failure leaves no partial artifact'
        Assert-True ($formatFailure.Warning -ceq 'Gateway current log redaction failed at plain-redaction (unknown exception, HRESULT 0x00000000)') 'ordinary formatter failure uses the fixed content-free fallback'

        $timeoutFailure = @($failSafeResults | Where-Object Name -eq 'timeout')[0]
        Assert-True $timeoutFailure.Escaped 'support timeout remains terminating'
        Assert-True ($timeoutFailure.EscapedMessage -like 'support bundle: timed out*') 'support timeout preserves the original timeout exception'
        Assert-True (-not $timeoutFailure.FormatterCalled) 'support timeout rethrows before diagnostic formatting'
        Assert-True ($timeoutFailure.PartialCount -eq 0) 'support timeout removes its partial before rethrow'
        Assert-True (-not $timeoutFailure.Warning) 'support timeout emits no best-effort redaction warning'
        Remove-Item -LiteralPath $diagnosticControlRoot -Recurse -Force -ErrorAction SilentlyContinue
    }

    Write-Host '== native safe-open ABI selection =='
    if (Test-Path -LiteralPath $SafeOpenLibrary -PathType Leaf) {
        . $SafeOpenLibrary
        Initialize-SupportSafeOpen
        foreach ($case in @(
            @{ OS = 'Darwin'; Architecture = 'X64'; Expected = 'darwin-x64' },
            @{ OS = 'Darwin'; Architecture = 'Arm64'; Expected = 'darwin-arm64' },
            @{ OS = 'Linux'; Architecture = 'X64'; Expected = 'linux-x64' },
            @{ OS = 'Linux'; Architecture = 'Arm64'; Expected = 'linux-arm64' }
        )) {
            Assert-True ((Get-SupportUnixAbiLayout $case.OS $case.Architecture) -ceq $case.Expected) "native layout selects $($case.Expected)"
        }

        $heldReadTempRoot = [System.IO.Path]::GetTempPath()
        if (-not $RunningOnWindows -and $heldReadTempRoot.StartsWith('/var/')) {
            $heldReadTempRoot = '/private' + $heldReadTempRoot
        }
        $heldReadFixture = Join-Path $heldReadTempRoot ([System.IO.Path]::GetRandomFileName())
        $heldReadRenamed = "$heldReadFixture.renamed"
        $heldReadOriginal = 'validated handle bytes'
        $heldReadReplacement = 'replacement path bytes'
        $heldRead = $null
        try {
            [System.IO.File]::WriteAllText($heldReadFixture, $heldReadOriginal)
            $heldReadMetadata = [GatewaySupport.SafeFile]::InspectRegularNoFollow($heldReadFixture)
            $heldRead = [GatewaySupport.SafeFile]::OpenRegularNoFollow(
                $heldReadFixture, $heldReadMetadata.Identity)
            Move-Item -LiteralPath $heldReadFixture -Destination $heldReadRenamed -ErrorAction Stop
            [System.IO.File]::WriteAllText($heldReadFixture, $heldReadReplacement)
            $heldReadBuffer = New-Object byte[] 65536
            $heldReadCount = $heldRead.Read($heldReadBuffer, $heldReadBuffer.Length)
            $heldReadText = [System.Text.Encoding]::UTF8.GetString(
                $heldReadBuffer, 0, $heldReadCount)
            Assert-True ($heldReadText -ceq $heldReadOriginal) 'safe-open reads the identity-validated handle after its source path is replaced'
            Assert-True (-not $heldReadText.Contains($heldReadReplacement)) 'safe-open never reopens the replaced configured source path'
        } catch {
            Fail-With "safe-open held-handle read boundary is usable: $($_.Exception.Message)"
            Fail-With 'safe-open never reopens the replaced configured source path'
        } finally {
            if ($heldRead) { $heldRead.Dispose() }
            Remove-Item -LiteralPath $heldReadFixture,$heldReadRenamed -Force -ErrorAction SilentlyContinue
        }

        $noBomFixture = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
        try {
            try {
                Write-SupportUtf8NoBom -Path $noBomFixture -Value '{"strict":true}'
                Assert-NoUtf8Bom $noBomFixture 'support UTF-8 writer emits no BOM on every PowerShell version'
                $strictObject = [System.Text.Encoding]::UTF8.GetString([System.IO.File]::ReadAllBytes($noBomFixture)) | ConvertFrom-Json
                Assert-True (($strictObject.strict -is [bool]) -and $strictObject.strict) 'support UTF-8 writer preserves strict JSON bytes'
            } catch {
                Fail-With "support UTF-8 writer is available and usable: $($_.Exception.Message)"
            }
        } finally {
            Remove-Item -LiteralPath $noBomFixture -Force -ErrorAction SilentlyContinue
        }
    } else {
        foreach ($abi in @('darwin-x64','darwin-arm64','linux-x64','linux-arm64')) {
            Fail-With "native layout helper missing for $abi"
        }
    }

    $SecretToken = 'realsupersecretXYZ'
    $SecretBearer = 'realtoken1234deadbeef'
    $SecretHash = 'realHashKeyABC987'
    $SecretEncrypt = 'realEncryptKey555'
    $SecretRemote = 'remotesecretvalue987'
    $ExternalSecret = 'external-reparse-secret-4455'
    $ExcludedSecret = 'excluded-curator-secret-7788'
    $DecoySecret = 'wrong-hermes-home-secret-9911'
    $SuffixDecoySecret = 'multi-component-rotation-secret-6633'
    $UnicodeDigitSecret = 'unicode-digit-rotation-secret-7744'

    $TestTempRoot = [System.IO.Path]::GetTempPath()
    if (-not $RunningOnWindows -and $TestTempRoot.StartsWith('/var/')) {
        # macOS /var is a symlink to /private/var. Use the canonical spelling
        # so the all-component no-follow walker does not correctly reject the
        # test harness's own temporary root.
        $TestTempRoot = '/private' + $TestTempRoot
    }
    $script:FixtureRoot = Join-Path $TestTempRoot ([System.IO.Path]::GetRandomFileName())
    $ExtractRoot = Join-Path $TestTempRoot ([System.IO.Path]::GetRandomFileName())
    $GatewayHome = Join-Path $script:FixtureRoot 'gateway-home'
    $KiroCwdFixture = Join-Path $script:FixtureRoot 'kiro-cwd'
    $CoworkerHome = Join-Path $script:FixtureRoot 'co-worker-home'
    $DecoyHermesHome = Join-Path $script:FixtureRoot 'decoy-hermes-home'
    $HomeFixture = Join-Path $script:FixtureRoot 'user-home'
    foreach ($dir in @(
        (Join-Path $GatewayHome 'logs'), (Join-Path $GatewayHome 'state'),
        (Join-Path $KiroCwdFixture 'native'), (Join-Path $CoworkerHome 'logs\curator'),
        (Join-Path $CoworkerHome 'profiles\work\logs'), (Join-Path $DecoyHermesHome 'logs'),
        $HomeFixture, $ExtractRoot
    )) { $null = New-Item -ItemType Directory -Path $dir -Force }

    # Force the child wrapper's process-global temp root to a literal path
    # containing spaces. Keep one unrelated pre-existing staging directory so
    # every baseline/delta assertion proves it ignores concurrent prior state.
    $SupportGlobalTemp = Join-Path $script:FixtureRoot 'support global temp with spaces'
    $null = New-Item -ItemType Directory -Path $SupportGlobalTemp -Force
    $env:TEMP = $SupportGlobalTemp
    $env:TMP = $SupportGlobalTemp
    if (-not $RunningOnWindows) { $env:TMPDIR = $SupportGlobalTemp }
    $null = New-Item -ItemType Directory -Path (Join-Path $SupportGlobalTemp '.gw-support-staging-unrelated existing') -Force

    @(
        'gateway current safe',
        "AUTH_TOKEN=$SecretToken",
        "Authorization: Bearer $SecretBearer",
        "x-api-key: $SecretBearer",
        "PII_HASH_KEY=$SecretHash",
        "PII_ENCRYPT_KEY=$SecretEncrypt",
        "GW_METRICS_REMOTE_WRITE_TOKEN=$SecretRemote"
    ) | Set-Content -LiteralPath (Join-Path $GatewayHome 'logs\gateway.log') -Encoding UTF8
    'gateway boot safe' | Set-Content -LiteralPath (Join-Path $GatewayHome 'logs\gateway-boot.log') -Encoding UTF8
    'gateway boot stderr safe' | Set-Content -LiteralPath (Join-Path $GatewayHome 'logs\gateway-boot-stderr-source.log') -Encoding UTF8
    'gateway trace safe' | Set-Content -LiteralPath (Join-Path $GatewayHome 'logs\gateway-chat-trace.log') -Encoding UTF8
    Write-RandomFile (Join-Path $GatewayHome 'logs\gateway-20200101.log.gz') (1600KB) -Gzip
    Write-GzipText (Join-Path $GatewayHome 'logs\gateway-20260101.log.gz') "gateway compressed safe`nGW_METRICS_REMOTE_WRITE_TOKEN=$SecretRemote`n"
    'not a gzip stream' | Set-Content -LiteralPath (Join-Path $GatewayHome 'logs\gateway-corrupt.log.gz') -Encoding ASCII
    (Get-Item -LiteralPath (Join-Path $GatewayHome 'logs\gateway-20200101.log.gz')).LastWriteTimeUtc = [datetime]'2020-01-01Z'
    (Get-Item -LiteralPath (Join-Path $GatewayHome 'logs\gateway-20260101.log.gz')).LastWriteTimeUtc = [datetime]'2026-01-01Z'

    @('kiro current safe', "AUTH_TOKEN=$SecretToken") | Set-Content -LiteralPath (Join-Path $KiroCwdFixture 'native\kiro-current.log') -Encoding UTF8
    Write-RandomFile (Join-Path $KiroCwdFixture 'native\kiro-current.log.1') (2100KB)
    'kiro newest rotation safe' | Set-Content -LiteralPath (Join-Path $KiroCwdFixture 'native\kiro-current.log.2') -Encoding UTF8
    $SuffixDecoySecret | Set-Content -LiteralPath (Join-Path $KiroCwdFixture 'native\kiro-current.log.backup.99') -Encoding UTF8
    Write-GzipText (Join-Path $KiroCwdFixture 'native\kiro-current.log.3.gz') 'compressed Kiro excluded'
    $UnicodeDigitSecret | Set-Content -LiteralPath (Join-Path $KiroCwdFixture "native\kiro-current.log.$([char]0x0661)") -Encoding UTF8
    (Get-Item -LiteralPath (Join-Path $KiroCwdFixture 'native\kiro-current.log.1')).LastWriteTimeUtc = [datetime]'2021-01-01Z'
    (Get-Item -LiteralPath (Join-Path $KiroCwdFixture 'native\kiro-current.log.2')).LastWriteTimeUtc = [datetime]'2023-01-01Z'

    $ApprovedLogs = @('agent.log','errors.log','gateway.log','gui.log','desktop.log','mcp-stderr.log','gateway-shutdown-watchdog.log','dashboard-auth.log','container-boot.log','tool_calls.log')
    foreach ($name in $ApprovedLogs) { "$name safe" | Set-Content -LiteralPath (Join-Path $CoworkerHome "logs\$name") -Encoding UTF8 }
    @("AUTH_TOKEN=$SecretToken", "GW_METRICS_REMOTE_WRITE_TOKEN=$SecretRemote") | Add-Content -LiteralPath (Join-Path $CoworkerHome 'logs\agent.log') -Encoding UTF8
    Write-RandomFile (Join-Path $CoworkerHome 'logs\agent.log.1') (2100KB)
    'co-worker newest rotation safe' | Set-Content -LiteralPath (Join-Path $CoworkerHome 'logs\errors.log.2') -Encoding UTF8
    $SuffixDecoySecret | Set-Content -LiteralPath (Join-Path $CoworkerHome 'logs\agent.log.private.99') -Encoding UTF8
    Write-GzipText (Join-Path $CoworkerHome 'logs\errors.log.3.gz') 'compressed Co-worker excluded'
    $UnicodeDigitSecret | Set-Content -LiteralPath (Join-Path $CoworkerHome "logs\agent.log.$([char]0x0661)") -Encoding UTF8
    (Get-Item -LiteralPath (Join-Path $CoworkerHome 'logs\agent.log.1')).LastWriteTimeUtc = [datetime]'2022-01-01Z'
    (Get-Item -LiteralPath (Join-Path $CoworkerHome 'logs\errors.log.2')).LastWriteTimeUtc = [datetime]'2024-01-01Z'
    'profile errors safe' | Set-Content -LiteralPath (Join-Path $CoworkerHome 'profiles\work\logs\errors.log') -Encoding UTF8
    'profile rotation safe' | Set-Content -LiteralPath (Join-Path $CoworkerHome 'profiles\work\logs\errors.log.1') -Encoding UTF8
    $ExcludedSecret | Set-Content -LiteralPath (Join-Path $CoworkerHome 'logs\unrelated.log') -Encoding UTF8
    $ExcludedSecret | Set-Content -LiteralPath (Join-Path $CoworkerHome 'logs\curator\agent.log') -Encoding UTF8
    $DecoySecret | Set-Content -LiteralPath (Join-Path $DecoyHermesHome 'logs\agent.log') -Encoding UTF8

    $Outside = Join-Path $script:FixtureRoot 'outside-secret.log'
    $ExternalSecret | Set-Content -LiteralPath $Outside -Encoding UTF8
    $ReparseCreated = $false
    try {
        $null = New-Item -ItemType SymbolicLink -Path (Join-Path $CoworkerHome 'logs\agent.log.9') -Target $Outside -ErrorAction Stop
        $ReparseCreated = $true
    } catch {
        Write-Host "  skip: reparse fixture unavailable on this host: $($_.Exception.Message)"
    }

    $PortFile = Join-Path $script:FixtureRoot 'http-port'
    $RequestLog = Join-Path $script:FixtureRoot 'http-requests.log'
    $ModeFile = Join-Path $script:FixtureRoot 'http-mode'
    $StallAcceptedFile = Join-Path $script:FixtureRoot 'http-stall-health.accepted'
    $ServerScript = Join-Path $script:FixtureRoot 'server.py'
    'enabled' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    @'
import http.server, json, sys, time
port_file, request_log, mode_file, stall_accepted_file = sys.argv[1:]
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args): pass
    def send_body(self, status, content_type, body):
        encoded = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)
    def record(self):
        with open(request_log, "a", encoding="utf-8") as out: out.write(f"{self.command} {self.path}\n")
    def do_GET(self):
        self.record()
        mode = open(mode_file, encoding="utf-8-sig").read().strip()
        if mode == "stall-health" and self.path == "/health":
            # Accept the TCP connection but send no headers. This distinguishes
            # the wrapper's per-request deadline from a fast connection error.
            with open(stall_accepted_file, "w", encoding="ascii") as out: out.write("accepted")
            time.sleep(30)
            return
        if self.path == "/metrics": self.send_body(200, "text/plain", "# HELP gateway_up fixture\ngateway_up 1\n")
        elif self.path == "/admin/api/acp-capture?support=redacted":
            if mode in ("enabled", "stall-health"):
                self.send_body(200, "application/json", json.dumps({"enabled": True, "allowRuntimeToggle": True, "count": 1, "size": 8, "frames": [{"seq": 7, "method": "session/update", "params": "{\"safe\":\"capture safe\",\"token\":\"[REDACTED]\"}", "bytes": 64}]}))
            elif mode == "disabled": self.send_body(200, "application/json", '{"enabled":false,"frames":[]}')
            elif mode == "wrongtype": self.send_body(200, "application/json", '{"enabled":"true","frames":[]}')
            elif mode == "invalid": self.send_body(200, "application/json", '{"enabled":true')
            else: self.send_body(503, "text/plain", 'capture unavailable')
        elif self.path == "/health": self.send_body(200, "application/json", '{"status":"ok"}')
        elif self.path == "/admin/api/snapshot": self.send_body(200, "application/json", '{"fixture":"snapshot"}')
        else: self.send_body(404, "text/plain", "not found")
    def do_POST(self):
        self.record()
        self.send_body(405, "text/plain", "read only")
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
open(port_file, "w", encoding="ascii").write(str(server.server_address[1]))
server.serve_forever()
'@ | Set-Content -LiteralPath $ServerScript -Encoding UTF8
    $script:HttpProcess = Start-Process -FilePath $Python -ArgumentList @(
        $ServerScript, $PortFile, $RequestLog, $ModeFile, $StallAcceptedFile) -PassThru
    $HttpPort = Wait-HttpPortFile -Path $PortFile -Process $script:HttpProcess
    @("request = POST", "url = http://127.0.0.1:$HttpPort/mutate") | Set-Content -LiteralPath (Join-Path $HomeFixture '.curlrc') -Encoding ASCII

    $GatewayLog = Join-Path $GatewayHome 'logs\gateway.log'
    $GatewayBoot = Join-Path $GatewayHome 'logs\gateway-boot.log'
    $GatewayBootError = Join-Path $GatewayHome 'logs\gateway-boot-stderr-source.log'
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome $GatewayBootError

    Write-Host '== enabled support bundle =='
    $mainStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
    $main = Invoke-SupportRun (Join-Path $ExtractRoot 'main-out')
    Assert-NoNewSupportStaging $mainStagingBefore 'corrupt gzip run removes its global staging directory'
    Assert-True ($main.ExitCode -eq 0) "support exits zero with HERMES_HOME fallback: $(Format-SupportResult $main)"
    Assert-File $main.Bundle 'bundle path is printed and exists'
    Assert-File (Join-Path $ExtractRoot 'main-out\latest.zip') 'latest.zip copy exists'
    $mainRoot = Expand-SupportBundle $main.Bundle (Join-Path $ExtractRoot 'main-tree')
    foreach ($section in @('env','health','logs','system','tray')) { Assert-Directory (Join-Path $mainRoot $section) "standard $section section exists" }
    $logNames = @((Get-ChildItem -LiteralPath (Join-Path $mainRoot 'logs') -Directory | Sort-Object Name | ForEach-Object Name)) -join ','
    Assert-True ($logNames -ceq 'co-worker,gateway,kiro') "logs contains exactly application directories (got $logNames)"
    Assert-Absent (Join-Path $mainRoot 'logs\gateway.log') 'flat Gateway layout is absent'

    $mainManifest = Join-Path $mainRoot 'MANIFEST.txt'
    foreach ($relative in @('gateway.log','gateway-boot-stdout.log','gateway-boot-stderr.log','gateway-chat-trace.log','gateway-20200101.log.gz','gateway-20260101.log.gz')) {
        if ($relative -ceq 'gateway.log') {
            Assert-CurrentLogContains (Join-Path $mainRoot "logs\gateway\$relative") $mainManifest '' "Gateway artifact $relative is organized"
        } else {
            Assert-File (Join-Path $mainRoot "logs\gateway\$relative") "Gateway artifact $relative is organized"
        }
    }
    Assert-Contains (Join-Path $mainRoot 'logs\gateway\gateway-boot-stdout.log') 'gateway boot safe' 'Gateway boot stdout sidecar content is preserved'
    Assert-Contains (Join-Path $mainRoot 'logs\gateway\gateway-boot-stderr.log') 'gateway boot stderr safe' 'Gateway boot stderr sidecar content is preserved'
    $gatewayGzipText = Get-GzipText (Join-Path $mainRoot 'logs\gateway\gateway-20260101.log.gz')
    Assert-True ($gatewayGzipText.Contains('GW_METRICS_REMOTE_WRITE_TOKEN=[REDACTED]')) 'Gateway gzip rotation is decompressed and redacted'
    Assert-True (-not $gatewayGzipText.Contains($SecretRemote)) 'Gateway gzip rotation excludes raw remote-write token'
    Assert-Contains (Join-Path $mainRoot 'logs\gateway\gateway.log') 'GW_METRICS_REMOTE_WRITE_TOKEN=[REDACTED]' 'Gateway current remote-write assignment is redacted'
    Assert-Absent (Join-Path $mainRoot 'logs\gateway\gateway-corrupt.log.gz') 'corrupt Gateway gzip leaves no partial archive artifact'
    Assert-NoSupportTemporaryArtifacts $mainRoot 'corrupt gzip bundle has no partial artifacts'
    Assert-NoSupportTemporaryArtifacts (Join-Path $ExtractRoot 'main-out') 'corrupt gzip output has no partial or staging artifacts'

    foreach ($relative in @('kiro-chat.log','kiro-chat.log.1','kiro-chat.log.2')) { Assert-File (Join-Path $mainRoot "logs\kiro\$relative") "Kiro artifact $relative is retained" }
    Assert-Absent (Join-Path $mainRoot 'logs\kiro\kiro-chat.log.3.gz') 'compressed Kiro rotation is excluded'
    Assert-Absent (Join-Path $mainRoot 'logs\kiro\kiro-chat.log.99') 'multi-component Kiro suffix is excluded'
    Assert-Absent (Join-Path $mainRoot "logs\kiro\kiro-chat.log.$([char]0x0661)") 'Unicode-digit Kiro suffix is excluded'
    Assert-Contains (Join-Path $mainRoot 'logs\kiro\kiro-chat.log') 'AUTH_TOKEN=[REDACTED]' 'relative Kiro current log is found and redacted'

    foreach ($name in $ApprovedLogs) { Assert-File (Join-Path $mainRoot "logs\co-worker\$name") "approved Co-worker $name is retained" }
    foreach ($relative in @('agent.log.1','errors.log.2','profiles\work\logs\errors.log','profiles\work\logs\errors.log.1')) { Assert-File (Join-Path $mainRoot "logs\co-worker\$relative") "Co-worker artifact $relative is retained" }
    foreach ($relative in @('errors.log.3.gz','unrelated.log','curator','agent.log.99')) { Assert-Absent (Join-Path $mainRoot "logs\co-worker\$relative") "unapproved Co-worker artifact $relative is excluded" }
    Assert-Absent (Join-Path $mainRoot "logs\co-worker\agent.log.$([char]0x0661)") 'Unicode-digit Co-worker suffix is excluded'
    if ($ReparseCreated) { Assert-Absent (Join-Path $mainRoot 'logs\co-worker\agent.log.9') 'matching reparse point is rejected' }
    Assert-Contains (Join-Path $mainRoot 'logs\co-worker\agent.log') 'GW_METRICS_REMOTE_WRITE_TOKEN=[REDACTED]' 'Co-worker remote-write assignment is redacted'

    $metrics = @(Get-ChildItem -LiteralPath (Join-Path $mainRoot 'logs\gateway') -File -Filter 'metrics-snapshot-*.prom')
    $captures = @(Get-ChildItem -LiteralPath (Join-Path $mainRoot 'logs\gateway') -File -Filter 'acp-capture-*.json')
    Assert-True ($metrics.Count -eq 1) 'one timestamped metrics snapshot is archived'
    Assert-True ($captures.Count -eq 1) 'one timestamped enabled capture is archived'
    if ($metrics.Count -eq 1 -and $captures.Count -eq 1) {
        $snapshotTs = $metrics[0].BaseName.Substring('metrics-snapshot-'.Length)
        Assert-True ($snapshotTs -match '^\d{8}-\d{6}Z$') 'snapshot timestamp uses UTC YYYYMMDD-HHMMSSZ'
        Assert-True ($captures[0].Name -ceq "acp-capture-$snapshotTs.json") 'metrics and capture share one UTC timestamp'
        Assert-Contains $metrics[0].FullName 'gateway_up 1' 'metrics content is preserved'
        Assert-NoUtf8Bom $metrics[0].FullName 'metrics snapshot starts with Prometheus text, not a BOM'
        Assert-NoUtf8Bom $captures[0].FullName 'capture snapshot starts with JSON, not a BOM'
        $captureJson = Get-Content -LiteralPath $captures[0].FullName -Raw | ConvertFrom-Json
        Assert-True (($captureJson.enabled -is [bool]) -and $captureJson.enabled -and $captureJson.frames[0].seq -eq 7) 'capture is valid enabled JSON'
    }
    $envFile = Join-Path $mainRoot 'env\effective.env'
    Assert-Contains $envFile 'GW_METRICS_REMOTE_WRITE_URL=https://metrics.example.test/api/prom/push' 'effective env includes remote-write URL'
    Assert-Contains $envFile 'GW_METRICS_REMOTE_WRITE_USER=fixture-user' 'effective env includes remote-write user'
    Assert-Contains $envFile 'GW_METRICS_REMOTE_WRITE_INTERVAL_SEC=45' 'effective env includes remote-write interval'
    Assert-Contains $envFile "GW_METRICS_REMOTE_WRITE_TOKEN=remo$([char]0x2026)(20 chars)" 'effective env masks remote-write token'
    $manifest = Join-Path $mainRoot 'MANIFEST.txt'
    Assert-Contains $manifest 'metrics: captured' 'manifest records captured metrics'
    Assert-Contains $manifest 'capture: captured' 'manifest records captured capture'
    Assert-Contains $manifest 'sensitive user content' 'manifest warns about capture content'
    Assert-Contains $manifest 'review before sharing' 'manifest tells operator to review bundle'
    Assert-Line $manifest 'WARNING: Gateway rotation gateway-corrupt.log.gz decompression or redaction failed' 'manifest records corrupt gzip failure after cleanup'
    if ($ReparseCreated) { Assert-Contains $manifest 'Co-worker agent.log.9 rejected: reparse point' 'manifest records reparse rejection' }
    Assert-NoSecretInTree $mainRoot @($SecretToken,$SecretBearer,$SecretHash,$SecretEncrypt,$SecretRemote,$ExternalSecret,$ExcludedSecret,$DecoySecret,$SuffixDecoySecret,$UnicodeDigitSecret) 'enabled bundle'

    Write-Host '== global cap and explicit Co-worker precedence =='
    $env:HERMES_HOME = $DecoyHermesHome
    $cap = Invoke-SupportRun (Join-Path $ExtractRoot 'cap-out') 3 $CoworkerHome
    Assert-True ($cap.ExitCode -eq 0) "support exits zero with explicit Co-worker home: $(Format-SupportResult $cap)"
    $capRoot = Expand-SupportBundle $cap.Bundle (Join-Path $ExtractRoot 'cap-tree')
    Assert-CurrentLogContains (Join-Path $capRoot 'logs\gateway\gateway.log') (Join-Path $capRoot 'MANIFEST.txt') '' 'cap preserves protected logs\gateway\gateway.log'
    foreach ($relative in @('logs\kiro\kiro-chat.log','logs\co-worker\agent.log','MANIFEST.txt')) { Assert-File (Join-Path $capRoot $relative) "cap preserves protected $relative" }
    Assert-Contains (Join-Path $capRoot 'logs\co-worker\agent.log') 'agent.log safe' 'explicit Co-worker home wins over HERMES_HOME'
    Assert-Absent (Join-Path $capRoot 'logs\gateway\gateway-20200101.log.gz') 'cap drops oldest Gateway rotation first'
    Assert-Absent (Join-Path $capRoot 'logs\kiro\kiro-chat.log.1') 'cap drops next-oldest Kiro rotation'
    Assert-File (Join-Path $capRoot 'logs\co-worker\agent.log.1') 'cap keeps newer Co-worker rotation once under cap'
    Assert-Contains (Join-Path $capRoot 'MANIFEST.txt') 'DROPPED FOR SIZE: logs' 'manifest accounts for global cap omissions with relative paths'

    Write-Host '== final-archive cap accounting =='
    $OverheadGateway = Join-Path $script:FixtureRoot 'overhead-gateway'
    $OverheadKiro = Join-Path $script:FixtureRoot 'overhead-kiro'
    $null = New-Item -ItemType Directory -Path $OverheadGateway -Force
    $null = New-Item -ItemType Directory -Path $OverheadKiro -Force
    'overhead current Gateway' | Set-Content -LiteralPath (Join-Path $OverheadGateway 'gateway.log') -Encoding UTF8
    'overhead boot Gateway' | Set-Content -LiteralPath (Join-Path $OverheadGateway 'gateway-boot.log') -Encoding UTF8
    'overhead current Kiro' | Set-Content -LiteralPath (Join-Path $OverheadKiro 'kiro.log') -Encoding UTF8
    $OverheadOldest = Join-Path $OverheadGateway 'gateway-overhead.log.gz'
    Write-GzipBase64Random $OverheadOldest (1015KB)
    (Get-Item -LiteralPath $OverheadOldest).LastWriteTimeUtc = [datetime]'2019-01-01Z'
    foreach ($rotation in 100..180) {
        "manifest row $rotation" | Set-Content -LiteralPath (Join-Path $OverheadKiro "kiro.log.$rotation") -Encoding UTF8
        (Get-Item -LiteralPath (Join-Path $OverheadKiro "kiro.log.$rotation")).LastWriteTimeUtc = [datetime]'2025-01-01Z'
    }
    Set-SupportEnvironment (Join-Path $OverheadGateway 'gateway.log') (Join-Path $OverheadGateway 'gateway-boot.log') $OverheadKiro 'kiro.log' ''
    $overheadCap = Invoke-SupportRun (Join-Path $ExtractRoot 'overhead-cap-out') 1
    Assert-True ($overheadCap.ExitCode -eq 0) "near-cap support exits zero: $(Format-SupportResult $overheadCap)"
    Assert-True ((Get-Item -LiteralPath $overheadCap.Bundle).Length -le 1MB) 'actual final zip including manifest and metadata satisfies 1MB cap'
    $overheadRoot = Expand-SupportBundle $overheadCap.Bundle (Join-Path $ExtractRoot 'overhead-cap-tree')
    Assert-Absent (Join-Path $overheadRoot 'logs\gateway\gateway-overhead.log.gz') 'final-archive sizing drops oldest near-cap rotation'
    Assert-File (Join-Path $overheadRoot 'logs\kiro\kiro-chat.log.180') 'final-archive sizing preserves newer rotation once under cap'
    Assert-Line (Join-Path $overheadRoot 'MANIFEST.txt') 'DROPPED FOR SIZE: logs/gateway/gateway-overhead.log.gz' 'manifest accounts for overhead-driven omission'

    Write-Host '== capture states and Kiro cwd semantics =='
    'disabled' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    $env:HERMES_HOME = ''
    $disabled = Invoke-SupportRun (Join-Path $ExtractRoot 'disabled-out')
    $disabledRoot = Expand-SupportBundle $disabled.Bundle (Join-Path $ExtractRoot 'disabled-tree')
    Assert-True ($disabled.ExitCode -eq 0) "support continues without a Co-worker home: $(Format-SupportResult $disabled)"
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $disabledRoot 'logs\gateway') -Filter 'acp-capture-*.json').Count -eq 0) 'disabled capture creates no export'
    Assert-Contains (Join-Path $disabledRoot 'MANIFEST.txt') 'capture: disabled' 'manifest records disabled capture'
    Assert-Contains (Join-Path $disabledRoot 'MANIFEST.txt') 'Co-worker logs unavailable' 'manifest records missing Co-worker home'

    'wrongtype' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    $invalid = Invoke-SupportRun (Join-Path $ExtractRoot 'invalid-out')
    $invalidRoot = Expand-SupportBundle $invalid.Bundle (Join-Path $ExtractRoot 'invalid-tree')
    Assert-True ($invalid.ExitCode -eq 0) "support tolerates non-boolean capture state: $(Format-SupportResult $invalid)"
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $invalidRoot 'logs\gateway') -Filter 'acp-capture-*.json').Count -eq 0) 'non-boolean capture creates no export'
    Assert-Contains (Join-Path $invalidRoot 'MANIFEST.txt') 'capture: unavailable' 'manifest records non-boolean capture as unavailable'
    Assert-Line (Join-Path $invalidRoot 'MANIFEST.txt') 'WARNING: ACP capture unavailable: response state was invalid' 'manifest identifies invalid capture state or type'

    'invalid' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    $malformed = Invoke-SupportRun (Join-Path $ExtractRoot 'malformed-capture-out')
    $malformedRoot = Expand-SupportBundle $malformed.Bundle (Join-Path $ExtractRoot 'malformed-capture-tree')
    Assert-True ($malformed.ExitCode -eq 0) "support tolerates malformed capture JSON: $(Format-SupportResult $malformed)"
    Assert-Line (Join-Path $malformedRoot 'MANIFEST.txt') 'WARNING: ACP capture unavailable: response was not valid JSON' 'manifest identifies malformed capture JSON'

    'http-error' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    $httpFailure = Invoke-SupportRun (Join-Path $ExtractRoot 'capture-http-failure-out')
    $httpFailureRoot = Expand-SupportBundle $httpFailure.Bundle (Join-Path $ExtractRoot 'capture-http-failure-tree')
    Assert-True ($httpFailure.ExitCode -eq 0) "support tolerates capture HTTP failure: $(Format-SupportResult $httpFailure)"
    Assert-Line (Join-Path $httpFailureRoot 'MANIFEST.txt') 'WARNING: ACP capture unavailable: request failed' 'manifest identifies capture transport or HTTP failure'

    Write-Host '== stalled request keeps the five-second per-request budget =='
    'stall-health' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    $StalledLogs = Join-Path $script:FixtureRoot 'stalled-request-logs'
    $null = New-Item -ItemType Directory -Path $StalledLogs -Force
    'stalled request gateway safe' | Set-Content -LiteralPath (Join-Path $StalledLogs 'gateway.log') -Encoding UTF8
    'stalled request boot safe' | Set-Content -LiteralPath (Join-Path $StalledLogs 'gateway-boot.log') -Encoding UTF8
    'stalled request Kiro safe' | Set-Content -LiteralPath (Join-Path $StalledLogs 'kiro.log') -Encoding UTF8
    Set-SupportEnvironment (Join-Path $StalledLogs 'gateway.log') (Join-Path $StalledLogs 'gateway-boot.log') $StalledLogs 'kiro.log' ''
    'stall-health' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    Clear-Content -LiteralPath $RequestLog
    Remove-Item -LiteralPath $StallAcceptedFile -Force -ErrorAction SilentlyContinue
    $stalledRequestHandle = Start-SupportRun (Join-Path $ExtractRoot 'stalled-request-out') 50 15
    $stalledRequestAccepted = Wait-SupportMarkerOrExit $stalledRequestHandle $StallAcceptedFile
    $stalledRequestWatch = [System.Diagnostics.Stopwatch]::StartNew()
    $stalledRequest = Complete-SupportRun $stalledRequestHandle
    $stalledRequestWatch.Stop()
    Assert-True $stalledRequestAccepted 'stalled-request timer starts only after the server accepts the health request'
    Assert-True ($stalledRequest.ExitCode -eq 0) "accepted-but-stalled request remains best effort: $(Format-SupportResult $stalledRequest)"
    Assert-True ($stalledRequestWatch.Elapsed.TotalSeconds -ge 4 -and $stalledRequestWatch.Elapsed.TotalSeconds -lt 12) "stalled request uses the five-second cap instead of the remaining 15-second bundle budget (elapsed $($stalledRequestWatch.Elapsed.TotalSeconds)s)"
    foreach ($path in @('/health','/admin/api/snapshot','/metrics','/admin/api/acp-capture?support=redacted')) {
        Assert-Contains $RequestLog "GET $path" "collection continues to $path after an earlier accepted connection stalls"
    }
    if ($stalledRequest.ExitCode -eq 0 -and (Test-Path -LiteralPath $stalledRequest.Bundle -PathType Leaf)) {
        $stalledRequestRoot = Expand-SupportBundle $stalledRequest.Bundle (Join-Path $ExtractRoot 'stalled-request-tree')
        Assert-Contains (Join-Path $stalledRequestRoot 'health\health.json') 'unreachable:' 'stalled health request is recorded as unavailable'
        Assert-Contains (Join-Path $stalledRequestRoot 'health\snapshot.json') '"fixture":"snapshot"' 'best-effort collection preserves the later admin snapshot'
        Assert-Contains (Join-Path $stalledRequestRoot 'MANIFEST.txt') 'metrics: captured' 'best-effort collection preserves the later metrics snapshot'
        Assert-Contains (Join-Path $stalledRequestRoot 'MANIFEST.txt') 'capture: captured' 'best-effort collection preserves the later capture snapshot'
    } else {
        Fail-With 'stalled request run did not publish a bundle for best-effort artifact assertions'
    }
    'enabled' | Set-Content -LiteralPath $ModeFile -Encoding ASCII

    Write-Host '== effective snapshot address resolution =='
    $AddressEnv = Join-Path $script:FixtureRoot 'address.env'
    $AddressOverrides = Join-Path $script:FixtureRoot 'address-overrides.env'

    # An HTTP_ADDR that exists only in the loaded .env must drive every live
    # support request; the pre-load default port is intentionally unreachable.
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' ''
    "HTTP_ADDR=127.0.0.1:$HttpPort" | Set-Content -LiteralPath $AddressEnv -Encoding ASCII
    $env:GW_ADDR = ''
    $env:GW_ENV_FILE = $AddressEnv
    $env:GW_OVERRIDES_FILE = Join-Path $script:FixtureRoot 'missing-address-overrides.env'
    Clear-Content -LiteralPath $RequestLog
    $envAddress = Invoke-SupportRun (Join-Path $ExtractRoot 'env-address-out')
    Assert-True ($envAddress.ExitCode -eq 0) "support uses .env-only HTTP_ADDR: $(Format-SupportResult $envAddress)"
    foreach ($path in @('/health','/admin/api/snapshot','/metrics','/admin/api/acp-capture?support=redacted')) {
        Assert-Contains $RequestLog "GET $path" ".env-only HTTP_ADDR requests $path on the custom port"
    }

    # The second dotenv layer wins for HTTP_ADDR exactly as it does for the
    # gateway process itself.
    'HTTP_ADDR=127.0.0.1:1' | Set-Content -LiteralPath $AddressEnv -Encoding ASCII
    "HTTP_ADDR=127.0.0.1:$HttpPort" | Set-Content -LiteralPath $AddressOverrides -Encoding ASCII
    $env:GW_ADDR = ''
    $env:GW_ENV_FILE = $AddressEnv
    $env:GW_OVERRIDES_FILE = $AddressOverrides
    Clear-Content -LiteralPath $RequestLog
    $overrideAddress = Invoke-SupportRun (Join-Path $ExtractRoot 'override-address-out')
    Assert-True ($overrideAddress.ExitCode -eq 0) "support uses overrides HTTP_ADDR precedence: $(Format-SupportResult $overrideAddress)"
    foreach ($path in @('/health','/admin/api/snapshot','/metrics','/admin/api/acp-capture?support=redacted')) {
        Assert-Contains $RequestLog "GET $path" "overrides HTTP_ADDR requests $path on the custom port"
    }

    # GW_ADDR remains the explicit wrapper probe override even when effective
    # HTTP_ADDR points somewhere else.
    'HTTP_ADDR=127.0.0.1:1' | Set-Content -LiteralPath $AddressEnv -Encoding ASCII
    'HTTP_ADDR=127.0.0.1:2' | Set-Content -LiteralPath $AddressOverrides -Encoding ASCII
    $env:GW_ADDR = "http://127.0.0.1:$HttpPort/"
    $env:GW_ENV_FILE = $AddressEnv
    $env:GW_OVERRIDES_FILE = $AddressOverrides
    Clear-Content -LiteralPath $RequestLog
    $explicitAddress = Invoke-SupportRun (Join-Path $ExtractRoot 'explicit-address-out')
    Assert-True ($explicitAddress.ExitCode -eq 0) "support honors explicit GW_ADDR precedence: $(Format-SupportResult $explicitAddress)"
    foreach ($path in @('/health','/admin/api/snapshot','/metrics','/admin/api/acp-capture?support=redacted')) {
        Assert-Contains $RequestLog "GET $path" "explicit GW_ADDR requests $path without a double slash"
    }

    Write-Host '== fail-closed safe-open and atomic snapshot publish =='
    'enabled' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
    $env:GW_SUPPORT_TEST_DISABLE_SAFE_OPEN = 'true'
    $noSafeOpenStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
    $noSafeOpen = Invoke-SupportRun (Join-Path $ExtractRoot 'no-safe-open-out')
    Assert-NoNewSupportStaging $noSafeOpenStagingBefore 'safe-open unavailable run removes its global staging directory'
    $noSafeOpenRoot = Expand-SupportBundle $noSafeOpen.Bundle (Join-Path $ExtractRoot 'no-safe-open-tree')
    Assert-True ($noSafeOpen.ExitCode -eq 0) "support continues when native safe-open is unavailable: $(Format-SupportResult $noSafeOpen)"
    Assert-Absent (Join-Path $noSafeOpenRoot 'logs\gateway\gateway.log') 'Gateway source is omitted without safe-open'
    Assert-Absent (Join-Path $noSafeOpenRoot 'logs\kiro\kiro-chat.log') 'Kiro source is omitted without safe-open'
    Assert-Absent (Join-Path $noSafeOpenRoot 'logs\co-worker\agent.log') 'Co-worker source is omitted without safe-open'
    foreach ($warning in @('Gateway current log safe-open unavailable','Kiro current log safe-open unavailable','Co-worker agent.log safe-open unavailable')) {
        Assert-Line (Join-Path $noSafeOpenRoot 'MANIFEST.txt') "WARNING: $warning" "manifest records $warning"
    }

    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
    $env:GW_SUPPORT_TEST_FAIL_PUBLISH = 'metrics,capture'
    $publishStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
    $publishFailure = Invoke-SupportRun (Join-Path $ExtractRoot 'publish-failure-out')
    Assert-NoNewSupportStaging $publishStagingBefore 'metrics/capture failures remove their global staging directory'
    $publishFailureRoot = Expand-SupportBundle $publishFailure.Bundle (Join-Path $ExtractRoot 'publish-failure-tree')
    Assert-True ($publishFailure.ExitCode -eq 0) "support continues after forced live-snapshot publish failures: $(Format-SupportResult $publishFailure)"
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $publishFailureRoot 'logs\gateway') -Filter 'metrics-snapshot-*.prom').Count -eq 0) 'failed metrics publish leaves no partial artifact'
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $publishFailureRoot 'logs\gateway') -Filter 'acp-capture-*.json').Count -eq 0) 'failed capture publish leaves no partial artifact'
    Assert-Line (Join-Path $publishFailureRoot 'MANIFEST.txt') 'WARNING: Metrics snapshot unavailable: atomic publish failed' 'manifest records metrics publish failure'
    Assert-Line (Join-Path $publishFailureRoot 'MANIFEST.txt') 'WARNING: ACP capture unavailable: atomic publish failed' 'manifest records capture publish failure'
    Assert-NoSupportTemporaryArtifacts $publishFailureRoot 'metrics/capture failure bundle has no partial artifacts'
    Assert-NoSupportTemporaryArtifacts (Join-Path $ExtractRoot 'publish-failure-out') 'metrics/capture failure output has no partial or staging artifacts'

    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
    $env:GW_SUPPORT_TEST_FAIL_PUBLISH = 'capture-write'
    $captureWriteFailure = Invoke-SupportRun (Join-Path $ExtractRoot 'capture-write-failure-out')
    $captureWriteFailureRoot = Expand-SupportBundle $captureWriteFailure.Bundle (Join-Path $ExtractRoot 'capture-write-failure-tree')
    Assert-True ($captureWriteFailure.ExitCode -eq 0) "support continues after capture write failure: $(Format-SupportResult $captureWriteFailure)"
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $captureWriteFailureRoot 'logs\gateway') -Filter 'acp-capture-*.json').Count -eq 0) 'capture write failure leaves no capture artifact'
    Assert-Line (Join-Path $captureWriteFailureRoot 'MANIFEST.txt') 'WARNING: ACP capture unavailable: publication failed' 'manifest identifies capture publication or write failure'
    Assert-NoSupportTemporaryArtifacts $captureWriteFailureRoot 'capture write failure bundle has no partial artifacts'

    Write-Host '== atomic publication cleanup for every artifact kind =='
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
    $env:GW_SUPPORT_TEST_FAIL_PUBLISH = 'plain,gzip,metrics,capture'
    $leafStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
    $leafPublishFailure = Invoke-SupportRun (Join-Path $ExtractRoot 'leaf-publish-failure-out')
    Assert-NoNewSupportStaging $leafStagingBefore 'plain/gzip/metrics/capture failures remove their global staging directory'
    Assert-True ($leafPublishFailure.ExitCode -eq 0) "support continues after forced plain/gzip/metrics/capture publication failures: $(Format-SupportResult $leafPublishFailure)"
    $leafPublishFailureRoot = Expand-SupportBundle $leafPublishFailure.Bundle (Join-Path $ExtractRoot 'leaf-publish-failure-tree')
    Assert-Absent (Join-Path $leafPublishFailureRoot 'logs\gateway\gateway.log') 'forced plain publication failure leaves no final plain log'
    Assert-Absent (Join-Path $leafPublishFailureRoot 'logs\gateway\gateway-20260101.log.gz') 'forced gzip publication failure leaves no final gzip log'
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $leafPublishFailureRoot 'logs\gateway') -Filter 'metrics-snapshot-*').Count -eq 0) 'forced metrics publication failure leaves no final or partial snapshot'
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $leafPublishFailureRoot 'logs\gateway') -Filter 'acp-capture-*').Count -eq 0) 'forced capture publication failure leaves no final or partial snapshot'
    Assert-Line (Join-Path $leafPublishFailureRoot 'MANIFEST.txt') 'WARNING: Gateway current log redaction failed at plain-publish (System.Management.Automation.RuntimeException, HRESULT 0x80131501)' 'manifest records the exact content-free forced plain publication diagnostic'
    Assert-Line (Join-Path $leafPublishFailureRoot 'MANIFEST.txt') 'WARNING: Gateway rotation gateway-20260101.log.gz archive publish failed' 'manifest proves the forced gzip publication failure path ran'
    Assert-NoSupportTemporaryArtifacts $leafPublishFailureRoot 'plain/gzip/metrics/capture failure bundle has no partial artifacts'
    Assert-NoSupportTemporaryArtifacts (Join-Path $ExtractRoot 'leaf-publish-failure-out') 'plain/gzip/metrics/capture failure output has no partial or staging artifacts'

    foreach ($failureKind in @('manifest','archive','latest')) {
        Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
        $env:GW_SUPPORT_TEST_FAIL_PUBLISH = $failureKind
        $failureOut = Join-Path $ExtractRoot "$failureKind-publish-failure-out"
        $terminalStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
        $terminalPublishFailure = Invoke-SupportRun $failureOut
        Assert-NoNewSupportStaging $terminalStagingBefore "forced $failureKind failure removes its global staging directory"
        Assert-True ($terminalPublishFailure.ExitCode -ne 0) "forced $failureKind publication failure exits nonzero"
        Assert-NoSupportTemporaryArtifacts $failureOut "forced $failureKind failure output has no partial or staging artifacts"
        $publishedArchives = @(Get-ChildItem -LiteralPath $failureOut -Filter 'gateway-support-*.zip' -File -ErrorAction SilentlyContinue)
        if ($failureKind -eq 'latest') {
            Assert-True ($publishedArchives.Count -eq 1) 'latest failure preserves the already-published timestamped archive'
            Assert-Absent (Join-Path $failureOut 'latest.zip') 'latest publication failure leaves no final latest.zip'
            if ($publishedArchives.Count -eq 1) {
                $latestFailureRoot = Expand-SupportBundle $publishedArchives[0].FullName (Join-Path $ExtractRoot 'latest-publish-failure-tree')
                Assert-NoSupportTemporaryArtifacts $latestFailureRoot 'latest failure preserved archive contains no partial artifacts'
            }
        } else {
            Assert-True ($publishedArchives.Count -eq 0) "forced $failureKind failure leaves no final timestamped archive"
        }
    }

    Write-Host '== replacement-safe timestamped and latest publication =='
    $ExistingZipInput = Join-Path $script:FixtureRoot 'existing-zip-input'
    $null = New-Item -ItemType Directory -Path $ExistingZipInput -Force
    'prior valid archive artifact' | Set-Content -LiteralPath (Join-Path $ExistingZipInput 'prior.txt') -Encoding ASCII

    $FixedArchiveTimestamp = '20000101-010203Z'
    $ArchiveReplaceOut = Join-Path $ExtractRoot 'archive-replacement-out'
    $null = New-Item -ItemType Directory -Path $ArchiveReplaceOut -Force
    $ExistingTimestamped = Join-Path $ArchiveReplaceOut ("gateway-support-{0}-{1}.zip" -f ([System.Net.Dns]::GetHostName()), $FixedArchiveTimestamp)
    Compress-Archive -Path $ExistingZipInput -DestinationPath $ExistingTimestamped -Force
    $TimestampedPriorHash = (Get-FileHash -LiteralPath $ExistingTimestamped -Algorithm SHA256).Hash
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' ''
    $env:GW_SUPPORT_TEST_TIMESTAMP = $FixedArchiveTimestamp
    $env:GW_SUPPORT_TEST_FAIL_PUBLISH = 'archive-replace'
    $archiveReplace = Invoke-SupportRun $ArchiveReplaceOut
    Assert-True ($archiveReplace.ExitCode -ne 0) 'injected timestamped replacement failure exits nonzero'
    Assert-File $ExistingTimestamped 'timestamped replacement failure preserves prior valid destination'
    if (Test-Path -LiteralPath $ExistingTimestamped) {
        Assert-True (((Get-FileHash -LiteralPath $ExistingTimestamped -Algorithm SHA256).Hash) -ceq $TimestampedPriorHash) 'timestamped replacement failure preserves prior destination bytes'
    }

    $LatestReplaceOut = Join-Path $ExtractRoot 'latest-replacement-out'
    $null = New-Item -ItemType Directory -Path $LatestReplaceOut -Force
    $ExistingLatest = Join-Path $LatestReplaceOut 'latest.zip'
    Compress-Archive -Path $ExistingZipInput -DestinationPath $ExistingLatest -Force
    $LatestPriorHash = (Get-FileHash -LiteralPath $ExistingLatest -Algorithm SHA256).Hash
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' ''
    $env:GW_SUPPORT_TEST_TIMESTAMP = '20000101-010204Z'
    $env:GW_SUPPORT_TEST_FAIL_PUBLISH = 'latest-replace'
    $latestReplace = Invoke-SupportRun $LatestReplaceOut
    Assert-True ($latestReplace.ExitCode -ne 0) 'injected latest replacement failure exits nonzero'
    Assert-File $ExistingLatest 'latest replacement failure preserves prior valid destination'
    if (Test-Path -LiteralPath $ExistingLatest) {
        Assert-True (((Get-FileHash -LiteralPath $ExistingLatest -Algorithm SHA256).Hash) -ceq $LatestPriorHash) 'latest replacement failure preserves prior destination bytes'
    }

    # Pause immediately before the replacement primitive. The old archive
    # must remain continuously observable at the barrier; publishing the new
    # one is a single replace operation after release.
    $BarrierReplaceOut = Join-Path $ExtractRoot 'latest-replacement-barrier-out'
    $null = New-Item -ItemType Directory -Path $BarrierReplaceOut -Force
    $BarrierLatest = Join-Path $BarrierReplaceOut 'latest.zip'
    Compress-Archive -Path $ExistingZipInput -DestinationPath $BarrierLatest -Force
    $BarrierPriorHash = (Get-FileHash -LiteralPath $BarrierLatest -Algorithm SHA256).Hash
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' ''
    $ReplaceReady = Join-Path $script:FixtureRoot 'replace.ready'
    $ReplaceContinue = Join-Path $script:FixtureRoot 'replace.continue'
    $env:GW_SUPPORT_TEST_TIMESTAMP = '20000101-010205Z'
    $env:GW_SUPPORT_TEST_REPLACE_BARRIER_DESTINATION = $BarrierLatest
    $env:GW_SUPPORT_TEST_REPLACE_BARRIER_READY = $ReplaceReady
    $env:GW_SUPPORT_TEST_REPLACE_BARRIER_CONTINUE = $ReplaceContinue
    $replaceRun = Start-SupportRun $BarrierReplaceOut
    foreach ($attempt in 1..600) {
        if (Test-Path -LiteralPath $ReplaceReady) { break }
        if ($replaceRun.Process.HasExited) { break }
        Start-Sleep -Milliseconds 50
    }
    $replaceBarrierReached = Test-Path -LiteralPath $ReplaceReady
    $oldVisibleAtBarrier = $replaceBarrierReached -and (Test-Path -LiteralPath $BarrierLatest -PathType Leaf) -and
        (((Get-FileHash -LiteralPath $BarrierLatest -Algorithm SHA256).Hash) -ceq $BarrierPriorHash)
    if ($replaceBarrierReached) { 'continue' | Set-Content -LiteralPath $ReplaceContinue -Encoding ASCII }
    $replaceResult = Complete-SupportRun $replaceRun
    Assert-True $replaceBarrierReached 'latest publisher reaches deterministic pre-replacement barrier'
    Assert-True $oldVisibleAtBarrier 'reader observes prior latest.zip until the atomic replacement call'
    Assert-True ($replaceResult.ExitCode -eq 0) "barriered latest replacement succeeds: $(Format-SupportResult $replaceResult)"
    Assert-File $BarrierLatest 'latest.zip remains continuously present after replacement'
    if (Test-Path -LiteralPath $BarrierLatest) {
        Assert-True (((Get-FileHash -LiteralPath $BarrierLatest -Algorithm SHA256).Hash) -cne $BarrierPriorHash) 'successful replacement publishes new latest.zip bytes'
    }

    'enabled' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    $SmallLogs = Join-Path $script:FixtureRoot 'small-logs'
    $null = New-Item -ItemType Directory -Path $SmallLogs -Force
    'small gateway' | Set-Content -LiteralPath (Join-Path $SmallLogs 'gateway.log') -Encoding UTF8
    'small boot' | Set-Content -LiteralPath (Join-Path $SmallLogs 'gateway-boot.log') -Encoding UTF8
    'default Kiro cwd safe' | Set-Content -LiteralPath (Join-Path $GatewayHome 'default-relative-kiro.log') -Encoding UTF8
    Set-SupportEnvironment (Join-Path $SmallLogs 'gateway.log') (Join-Path $SmallLogs 'gateway-boot.log') '' 'default-relative-kiro.log' ''
    $defaultCwd = Invoke-SupportRun (Join-Path $ExtractRoot 'default-cwd-out')
    $defaultRoot = Expand-SupportBundle $defaultCwd.Bundle (Join-Path $ExtractRoot 'default-cwd-tree')
    Assert-Contains (Join-Path $defaultRoot 'logs\kiro\kiro-chat.log') 'default Kiro cwd safe' 'empty KIRO_CWD resolves relative Kiro log from GW_HOME'

    $tildeDir = Join-Path $HomeFixture 'kiro-tilde\native'
    $null = New-Item -ItemType Directory -Path $tildeDir -Force
    'tilde Kiro cwd safe' | Set-Content -LiteralPath (Join-Path $tildeDir 'tilde-kiro.log') -Encoding UTF8
    Set-SupportEnvironment (Join-Path $SmallLogs 'gateway.log') (Join-Path $SmallLogs 'gateway-boot.log') '~/kiro-tilde' 'native/tilde-kiro.log' ''
    $tilde = Invoke-SupportRun (Join-Path $ExtractRoot 'tilde-out')
    $tildeRoot = Expand-SupportBundle $tilde.Bundle (Join-Path $ExtractRoot 'tilde-tree')
    Assert-Contains (Join-Path $tildeRoot 'logs\kiro\kiro-chat.log') 'tilde Kiro cwd safe' 'tilde KIRO_CWD expands before resolving relative Kiro log'

    Write-Host '== configured-source warnings and GET-only probes =='
    Set-SupportEnvironment (Join-Path $script:FixtureRoot 'missing\gateway.log') (Join-Path $script:FixtureRoot 'missing\gateway-boot.log') (Join-Path $script:FixtureRoot 'missing') 'kiro.log' ''
    $missing = Invoke-SupportRun (Join-Path $ExtractRoot 'missing-out')
    $missingRoot = Expand-SupportBundle $missing.Bundle (Join-Path $ExtractRoot 'missing-tree')
    Assert-True ($missing.ExitCode -eq 0) "support continues when configured sources are missing: $(Format-SupportResult $missing)"
    foreach ($warning in @('Gateway current log missing','Gateway boot stdout log missing','Gateway boot stderr log missing','Kiro current log missing')) { Assert-Contains (Join-Path $missingRoot 'MANIFEST.txt') $warning "manifest records $warning" }

    if (-not $RunningOnWindows -and (Get-Command chmod -ErrorAction SilentlyContinue)) {
        $FailureRoot = Join-Path $script:FixtureRoot 'unreadable-sources'
        foreach ($dir in @((Join-Path $FailureRoot 'gateway'), (Join-Path $FailureRoot 'kiro'), (Join-Path $FailureRoot 'co-worker\logs'))) {
            $null = New-Item -ItemType Directory -Path $dir -Force
        }
        $UnreadableGateway = Join-Path $FailureRoot 'gateway\gateway.log'
        $ReadableBoot = Join-Path $FailureRoot 'gateway\gateway-boot.log'
        $UnreadableKiro = Join-Path $FailureRoot 'kiro\kiro.log'
        $UnreadableCoworker = Join-Path $FailureRoot 'co-worker\logs\agent.log'
        'unreadable Gateway' | Set-Content -LiteralPath $UnreadableGateway -Encoding UTF8
        'readable boot' | Set-Content -LiteralPath $ReadableBoot -Encoding UTF8
        'unreadable Kiro' | Set-Content -LiteralPath $UnreadableKiro -Encoding UTF8
        'unreadable Co-worker' | Set-Content -LiteralPath $UnreadableCoworker -Encoding UTF8
        & chmod 000 $UnreadableGateway $UnreadableKiro $UnreadableCoworker
        try {
            Set-SupportEnvironment $UnreadableGateway $ReadableBoot (Join-Path $FailureRoot 'kiro') 'kiro.log' (Join-Path $FailureRoot 'co-worker')
            $unreadable = Invoke-SupportRun (Join-Path $ExtractRoot 'unreadable-out')
            $unreadableRoot = Expand-SupportBundle $unreadable.Bundle (Join-Path $ExtractRoot 'unreadable-tree')
            Assert-True ($unreadable.ExitCode -eq 0) "support continues when configured sources are unreadable: $(Format-SupportResult $unreadable)"
            foreach ($warning in @('Gateway current log unreadable','Kiro current log unreadable','Co-worker agent.log unreadable')) {
                Assert-Line (Join-Path $unreadableRoot 'MANIFEST.txt') "WARNING: $warning" "manifest records $warning"
            }
        } finally {
            & chmod 600 $UnreadableGateway $UnreadableKiro $UnreadableCoworker
        }
    } else {
        Write-Host '  skip: unreadable-source warning fixture requires Unix chmod semantics'
    }

    Write-Host '== per-item support deadline enforcement =='
    foreach ($deadlineStage in @(
        'source-snapshot-chunk',
        'gateway-rotation-item',
        'kiro-rotation-item',
        'coworker-log-item',
        'profile-log-item',
        'metrics-request',
        'capture-request',
        'archive-cap-item'
    )) {
        Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
        $env:GW_SUPPORT_TEST_DEADLINE_STAGE = $deadlineStage
        $env:GW_SUPPORT_TEST_DEADLINE_DELAY_MS = '1250'
        $deadlineReady = Join-Path $script:FixtureRoot ("deadline-{0}.ready" -f $deadlineStage)
        $env:GW_SUPPORT_TEST_DEADLINE_READY_FILE = $deadlineReady
        $deadlineOut = Join-Path $ExtractRoot ("deadline-{0}-out" -f $deadlineStage)
        $deadlineStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
        $deadlineHandle = Start-SupportRun $deadlineOut 50 1
        $deadlineReached = Wait-SupportMarkerOrExit $deadlineHandle $deadlineReady
        $deadlineWatch = [System.Diagnostics.Stopwatch]::StartNew()
        $deadlineRun = Complete-SupportRun $deadlineHandle
        $deadlineWatch.Stop()
        Assert-True $deadlineReached "deadline test reaches $deadlineStage before measuring cancellation"
        Assert-True ($deadlineRun.ExitCode -ne 0) "deadline expires inside $deadlineStage"
        # PowerShell's renderer prefixes wrapped exception continuation lines
        # with a `|`; remove that presentation gutter before matching the
        # underlying message.
        $normalizedDeadlineError = ($deadlineRun.Stderr -replace '\s*\|\s*', ' ') -replace '\s+', ' '
        Assert-True ($normalizedDeadlineError -match [regex]::Escape("timed out after 1 seconds at stage '$deadlineStage'")) "deadline failure identifies the exact one-second stage contract for $deadlineStage"
        Assert-True ($deadlineWatch.Elapsed.TotalSeconds -lt 4) "deadline $deadlineStage returns promptly from its stage barrier (elapsed $($deadlineWatch.Elapsed.TotalSeconds)s)"
        Assert-NoNewSupportStaging $deadlineStagingBefore "deadline $deadlineStage removes global staging"
        Assert-NoSupportTemporaryArtifacts $deadlineOut "deadline $deadlineStage leaves no output partials"
    }

    Write-Host '== cancellable plain redaction and diagnostic probes =='
    $VersionProbe = Join-Path $script:FixtureRoot 'version-probe.ps1'
    "param([string]`$VersionArg)`n'fixture version'" | Set-Content -LiteralPath $VersionProbe -Encoding ASCII
    foreach ($blockingCase in @(
        @{ Kind = 'plain'; Stage = 'plain-redaction' },
        @{ Kind = 'gateway-version'; Stage = 'gateway-version-probe' },
        @{ Kind = 'install-tree'; Stage = 'install-tree-probe' }
    )) {
        Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
        if ($blockingCase.Kind -ceq 'gateway-version') { $env:GW_BIN = $VersionProbe }
        $env:GW_SUPPORT_TEST_DEADLINE_STAGE = $blockingCase.Stage
        $env:GW_SUPPORT_TEST_BLOCKING_KIND = $blockingCase.Kind
        $env:GW_SUPPORT_TEST_BLOCKING_DELAY_MS = '5000'
        $blockingPidFile = Join-Path $script:FixtureRoot ("{0}.pid" -f $blockingCase.Kind)
        $env:GW_SUPPORT_TEST_BLOCKING_PID_FILE = $blockingPidFile
        $blockingReadyFile = Join-Path $script:FixtureRoot ("{0}.ready" -f $blockingCase.Kind)
        if ($blockingCase.Kind -ceq 'plain') {
            $env:GW_SUPPORT_TEST_BLOCKING_READY_FILE = $blockingReadyFile
        }
        $blockingOut = Join-Path $ExtractRoot ("blocking-{0}-out" -f $blockingCase.Kind)
        $priorLatest = Join-Path $blockingOut 'latest.zip'
        $priorFinal = Join-Path $blockingOut 'prior-support.zip'
        if ($blockingCase.Kind -ceq 'plain') {
            $null = New-Item -ItemType Directory -Path $blockingOut -Force
            [System.IO.File]::WriteAllText($priorLatest, 'prior latest bytes')
            [System.IO.File]::WriteAllText($priorFinal, 'prior final bytes')
            $priorLatestHash = (Get-FileHash -LiteralPath $priorLatest -Algorithm SHA256).Hash
            $priorFinalHash = (Get-FileHash -LiteralPath $priorFinal -Algorithm SHA256).Hash
        }
        $blockingStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
        $blockingHandle = Start-SupportRun $blockingOut 50 1
        $blockingMarker = if ($blockingCase.Kind -ceq 'plain') { $blockingReadyFile } else { $blockingPidFile }
        $blockingReached = Wait-SupportMarkerOrExit $blockingHandle $blockingMarker
        $blockingWatch = [System.Diagnostics.Stopwatch]::StartNew()
        $blockingRun = Complete-SupportRun $blockingHandle
        $blockingWatch.Stop()
        Assert-True $blockingReached "$($blockingCase.Kind) worker reaches its deterministic cancellation barrier"
        Assert-True ($blockingRun.ExitCode -ne 0) "$($blockingCase.Kind) work is cancelled at the support deadline"
        $normalizedBlockingError = ($blockingRun.Stderr -replace '\s*\|\s*', ' ') -replace '\s+', ' '
        Assert-True ($normalizedBlockingError -match [regex]::Escape("timed out after 1 seconds at stage '$($blockingCase.Stage)'")) "$($blockingCase.Kind) timeout identifies its blocking stage"
        Assert-True ($blockingWatch.Elapsed.TotalSeconds -lt 4) "$($blockingCase.Kind) timeout returns promptly from its worker barrier (elapsed $($blockingWatch.Elapsed.TotalSeconds)s)"
        Assert-File $blockingPidFile "$($blockingCase.Kind) worker exposes its PID for cancellation verification"
        if (Test-Path -LiteralPath $blockingPidFile) {
            $blockingPid = [int](Get-Content -LiteralPath $blockingPidFile -Raw)
            if ($blockingCase.Kind -ceq 'plain') {
                Assert-True ($blockingPid -ne $blockingHandle.ProcessId) 'plain redaction publishes an independent worker PID, not the wrapper PID'
            }
            Start-Sleep -Milliseconds 100
            Assert-True ($null -eq (Get-Process -Id $blockingPid -ErrorAction SilentlyContinue)) "timed-out $($blockingCase.Kind) worker is terminated"
        }
        if ($blockingCase.Kind -ceq 'plain') {
            Assert-True ((Get-FileHash -LiteralPath $priorLatest -Algorithm SHA256).Hash -ceq $priorLatestHash) 'plain redaction timeout preserves prior latest.zip bytes'
            Assert-True ((Get-FileHash -LiteralPath $priorFinal -Algorithm SHA256).Hash -ceq $priorFinalHash) 'plain redaction timeout preserves prior final archive bytes'
            Assert-True (@(Get-ChildItem -LiteralPath $blockingOut -Filter 'gateway-support-*.zip' -File -ErrorAction SilentlyContinue).Count -eq 0) 'plain redaction timeout publishes no new final archive'
        }
        Assert-NoNewSupportStaging $blockingStagingBefore "$($blockingCase.Kind) timeout removes global staging"
        Assert-NoSupportTemporaryArtifacts $blockingOut "$($blockingCase.Kind) timeout leaves no output partials"
    }

    Write-Host '== killable gzip compression deadline =='
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
    $GzipPidFile = Join-Path $script:FixtureRoot 'gzip-compression.pid'
    $env:GW_SUPPORT_TEST_COMPRESSION_KIND = 'gzip'
    $env:GW_SUPPORT_TEST_COMPRESSION_DELAY_MS = '5000'
    $env:GW_SUPPORT_TEST_COMPRESSION_PID_FILE = $GzipPidFile
    $gzipDeadlineOut = Join-Path $ExtractRoot 'gzip-compression-deadline-out'
    $gzipStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
    $gzipHandle = Start-SupportRun $gzipDeadlineOut 50 1
    $gzipReached = Wait-SupportMarkerOrExit $gzipHandle $GzipPidFile
    $gzipWatch = [System.Diagnostics.Stopwatch]::StartNew()
    $gzipDeadline = Complete-SupportRun $gzipHandle
    $gzipWatch.Stop()
    Assert-True $gzipReached 'gzip compression child reaches its deterministic cancellation barrier'
    Assert-True ($gzipDeadline.ExitCode -ne 0) 'gzip compression is cancelled at the support deadline'
    Assert-True ($gzipWatch.Elapsed.TotalSeconds -lt 4) "gzip compression timeout returns promptly from its child barrier (elapsed $($gzipWatch.Elapsed.TotalSeconds)s)"
    Assert-File $GzipPidFile 'gzip compression child exposes its PID for cancellation verification'
    if (Test-Path -LiteralPath $GzipPidFile) {
        $GzipPid = [int](Get-Content -LiteralPath $GzipPidFile -Raw)
        Start-Sleep -Milliseconds 100
        Assert-True ($null -eq (Get-Process -Id $GzipPid -ErrorAction SilentlyContinue)) 'timed-out gzip compression child is terminated'
    }
    Assert-NoNewSupportStaging $gzipStagingBefore 'gzip compression timeout removes global staging'
    Assert-NoSupportTemporaryArtifacts $gzipDeadlineOut 'gzip compression timeout leaves no partial archive'

    Write-Host '== killable archive compression deadline =='
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
    $CompressionPidFile = Join-Path $script:FixtureRoot 'archive-compression.pid'
    $env:GW_SUPPORT_TEST_COMPRESSION_KIND = 'archive'
    $env:GW_SUPPORT_TEST_COMPRESSION_DELAY_MS = '5000'
    $env:GW_SUPPORT_TEST_COMPRESSION_PID_FILE = $CompressionPidFile
    $compressionDeadlineOut = Join-Path $ExtractRoot 'archive-compression-deadline-out'
    $compressionStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
    $compressionHandle = Start-SupportRun $compressionDeadlineOut 50 1
    $compressionReached = Wait-SupportMarkerOrExit $compressionHandle $CompressionPidFile
    $compressionWatch = [System.Diagnostics.Stopwatch]::StartNew()
    $compressionDeadline = Complete-SupportRun $compressionHandle
    $compressionWatch.Stop()
    Assert-True $compressionReached 'archive compression child reaches its deterministic cancellation barrier'
    Assert-True ($compressionDeadline.ExitCode -ne 0) 'archive compression is cancelled at the support deadline'
    Assert-True ($compressionWatch.Elapsed.TotalSeconds -lt 4) "archive compression timeout returns promptly from its child barrier (elapsed $($compressionWatch.Elapsed.TotalSeconds)s)"
    Assert-File $CompressionPidFile 'archive compression child exposes its PID for cancellation verification'
    if (Test-Path -LiteralPath $CompressionPidFile) {
        $CompressionPid = [int](Get-Content -LiteralPath $CompressionPidFile -Raw)
        Start-Sleep -Milliseconds 100
        Assert-True ($null -eq (Get-Process -Id $CompressionPid -ErrorAction SilentlyContinue)) 'timed-out archive compression child is terminated'
    }
    Assert-NoNewSupportStaging $compressionStagingBefore 'archive compression timeout removes global staging'
    Assert-NoSupportTemporaryArtifacts $compressionDeadlineOut 'archive compression timeout leaves no partial archive'

    Write-Host '== deterministic safe-open replacement races =='
    $RaceRoot = Join-Path $script:FixtureRoot 'safe-open-races'
    $null = New-Item -ItemType Directory -Path (Join-Path $RaceRoot 'regular') -Force
    $RaceGateway = Join-Path $RaceRoot 'regular\gateway.log'
    $RaceBoot = Join-Path $RaceRoot 'regular\gateway-boot.log'
    $RaceExternal = Join-Path $RaceRoot 'external-regular.log'
    'authorized pre-open content' | Set-Content -LiteralPath $RaceGateway -Encoding UTF8
    'race boot' | Set-Content -LiteralPath $RaceBoot -Encoding UTF8
    'replacement external secret 8811' | Set-Content -LiteralPath $RaceExternal -Encoding UTF8
    Set-SupportEnvironment $RaceGateway $RaceBoot (Join-Path $RaceRoot 'missing-kiro') 'kiro.log' ''
    $RaceReady = Join-Path $RaceRoot 'regular.ready'
    $RaceContinue = Join-Path $RaceRoot 'regular.continue'
    $env:GW_SUPPORT_TEST_BARRIER_SOURCE = $RaceGateway
    $env:GW_SUPPORT_TEST_BARRIER_READY = $RaceReady
    $env:GW_SUPPORT_TEST_BARRIER_CONTINUE = $RaceContinue
    $raceStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
    $raceRun = Start-SupportRun (Join-Path $ExtractRoot 'regular-race-out')
    foreach ($attempt in 1..100) {
        if (Test-Path -LiteralPath $RaceReady) { break }
        if ($raceRun.Process.HasExited) { break }
        Start-Sleep -Milliseconds 50
    }
    $regularRaceExecuted = Test-Path -LiteralPath $RaceReady
    if ($regularRaceExecuted) {
        Move-Item -LiteralPath $RaceGateway -Destination "$RaceGateway.before-swap"
        Copy-Item -LiteralPath $RaceExternal -Destination $RaceGateway
        'continue' | Set-Content -LiteralPath $RaceContinue -Encoding ASCII
    }
    $raceResult = Complete-SupportRun $raceRun
    Assert-NoNewSupportStaging $raceStagingBefore 'pre-open regular replacement removes its global staging directory'
    Assert-True $regularRaceExecuted 'regular source is replaced between identity inspection and safe-open'
    Assert-True ($raceResult.ExitCode -eq 0) "support continues after deterministic regular replacement: $(Format-SupportResult $raceResult)"
    $raceBundleRoot = Expand-SupportBundle $raceResult.Bundle (Join-Path $ExtractRoot 'regular-race-tree')
    Assert-Absent (Join-Path $raceBundleRoot 'logs\gateway\gateway.log') 'regular replacement is rejected from archive'
    Assert-Line (Join-Path $raceBundleRoot 'MANIFEST.txt') 'WARNING: Gateway current log rejected: source replaced before safe-open' 'manifest records regular identity replacement'
    Assert-NoSecretInTree $raceBundleRoot @('replacement external secret 8811') 'regular replacement bundle'

    Write-Host '== after-open barrier failure disposes source handle =='
    $BarrierFailureRoot = Join-Path $RaceRoot 'after-open-barrier-failure'
    $null = New-Item -ItemType Directory -Path $BarrierFailureRoot -Force
    $BarrierFailureGateway = Join-Path $BarrierFailureRoot 'gateway.log'
    $BarrierFailureBoot = Join-Path $BarrierFailureRoot 'gateway-boot.log'
    'barrier failure gateway content' | Set-Content -LiteralPath $BarrierFailureGateway -Encoding UTF8
    'barrier failure boot content' | Set-Content -LiteralPath $BarrierFailureBoot -Encoding UTF8
    Set-SupportEnvironment $BarrierFailureGateway $BarrierFailureBoot (Join-Path $RaceRoot 'missing-kiro') 'kiro.log' ''
    $InvalidAfterOpenReady = Join-Path $BarrierFailureRoot 'ready-is-a-directory'
    $null = New-Item -ItemType Directory -Path $InvalidAfterOpenReady -Force
    $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_SOURCE = $BarrierFailureGateway
    $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_READY = $InvalidAfterOpenReady
    $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_CONTINUE = Join-Path $BarrierFailureRoot 'unused.continue'
    $NextSourceReady = Join-Path $BarrierFailureRoot 'next-source.ready'
    $NextSourceContinue = Join-Path $BarrierFailureRoot 'next-source.continue'
    $env:GW_SUPPORT_TEST_BARRIER_SOURCE = $BarrierFailureBoot
    $env:GW_SUPPORT_TEST_BARRIER_READY = $NextSourceReady
    $env:GW_SUPPORT_TEST_BARRIER_CONTINUE = $NextSourceContinue
    $barrierFailureRun = Start-SupportRun (Join-Path $ExtractRoot 'after-open-barrier-failure-out')
    foreach ($attempt in 1..100) {
        if (Test-Path -LiteralPath $NextSourceReady) { break }
        if ($barrierFailureRun.Process.HasExited) { break }
        Start-Sleep -Milliseconds 50
    }
    $nextBarrierReached = Test-Path -LiteralPath $NextSourceReady
    $handleCheckAvailable = $false
    $sourceHandleStillOpen = $false
    if ($nextBarrierReached -and $RunningOnWindows) {
        $handleCheckAvailable = $true
        try {
            $exclusive = [System.IO.File]::Open($BarrierFailureGateway, [System.IO.FileMode]::Open,
                [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
            $exclusive.Dispose()
        } catch {
            $sourceHandleStillOpen = $true
        }
    } elseif ($nextBarrierReached) {
        $lsof = Get-Command lsof -ErrorAction SilentlyContinue
        if ($lsof) {
            $handleCheckAvailable = $true
            $openRows = @(& $lsof.Source -a -p $barrierFailureRun.Process.Id -- $BarrierFailureGateway 2>$null)
            $sourceHandleStillOpen = $openRows.Count -gt 0
        }
    }
    if ($nextBarrierReached) { 'continue' | Set-Content -LiteralPath $NextSourceContinue -Encoding ASCII }
    $barrierFailureResult = Complete-SupportRun $barrierFailureRun
    Assert-True $nextBarrierReached 'collection reaches the next-source barrier after forced after-open barrier failure'
    Assert-True ($barrierFailureResult.ExitCode -eq 0) "support continues after forced after-open barrier failure: $(Format-SupportResult $barrierFailureResult)"
    if ($handleCheckAvailable) {
        Assert-True (-not $sourceHandleStillOpen) 'failed after-open barrier disposes the acquired source handle before collection continues'
    } else {
        Write-Host '  skip: live source-handle inspection unavailable on this host'
    }

    Write-Host '== deterministic safe-open held-handle rename =='
    $HeldRoot = Join-Path $RaceRoot 'held-rename'
    $null = New-Item -ItemType Directory -Path $HeldRoot -Force
    $HeldGateway = Join-Path $HeldRoot 'gateway.log'
    $HeldRenamed = Join-Path $HeldRoot 'gateway-renamed.log'
    $HeldBoot = Join-Path $HeldRoot 'gateway-boot.log'
    $HeldExternalSecret = 'post-open replacement external secret 4422'
    'authorized held-handle content' | Set-Content -LiteralPath $HeldGateway -Encoding UTF8
    'held-handle boot' | Set-Content -LiteralPath $HeldBoot -Encoding UTF8
    Set-SupportEnvironment $HeldGateway $HeldBoot (Join-Path $RaceRoot 'missing-kiro') 'kiro.log' ''
    $HeldReady = Join-Path $HeldRoot 'after-open.ready'
    $HeldContinue = Join-Path $HeldRoot 'after-open.continue'
    $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_SOURCE = $HeldGateway
    $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_READY = $HeldReady
    $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_CONTINUE = $HeldContinue
    $heldStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
    $heldRun = Start-SupportRun (Join-Path $ExtractRoot 'held-rename-out')
    foreach ($attempt in 1..100) {
        if (Test-Path -LiteralPath $HeldReady) { break }
        if ($heldRun.Process.HasExited) { break }
        Start-Sleep -Milliseconds 50
    }
    $heldBarrierReached = Test-Path -LiteralPath $HeldReady
    $heldActiveStaging = @(Get-NewSupportStagingDirectories $heldStagingBefore)
    Assert-True ($heldActiveStaging.Count -eq 1) 'held-handle rename exposes exactly one prefixed staging directory in global temp'
    $heldRenameSucceeded = $false
    $heldRenameError = ''
    if ($heldBarrierReached) {
        try {
            Move-Item -LiteralPath $HeldGateway -Destination $HeldRenamed -ErrorAction Stop
            $HeldExternalSecret | Set-Content -LiteralPath $HeldGateway -Encoding UTF8
            $heldRenameSucceeded = $true
        } catch {
            $heldRenameError = $_.Exception.Message
        }
        'continue' | Set-Content -LiteralPath $HeldContinue -Encoding ASCII
    }
    $heldResult = Complete-SupportRun $heldRun
    Assert-NoNewSupportStaging $heldStagingBefore 'held-handle rename removes its global staging directory'
    Assert-True $heldBarrierReached 'collection pauses only after the identity-validated source handle is open'
    Assert-True $heldRenameSucceeded "open source permits rename with replacement while held: $heldRenameError"
    Assert-True ($heldResult.ExitCode -eq 0) "support continues from a renamed open source: $(Format-SupportResult $heldResult)"
    $heldBundleRoot = Expand-SupportBundle $heldResult.Bundle (Join-Path $ExtractRoot 'held-rename-tree')
    Assert-CurrentLogContains (Join-Path $heldBundleRoot 'logs\gateway\gateway.log') (Join-Path $heldBundleRoot 'MANIFEST.txt') 'authorized held-handle content' 'renamed open source snapshots the validated handle content'
    Assert-NoSecretInTree $heldBundleRoot @($HeldExternalSecret) 'held-handle rename bundle'
    Assert-NoSupportTemporaryArtifacts $heldBundleRoot 'held-handle rename bundle has no partial artifacts'
    Assert-NoSupportTemporaryArtifacts (Join-Path $ExtractRoot 'held-rename-out') 'held-handle rename output has no partial or staging artifacts'

    if ($RunningOnWindows) {
        $DeleteRoot = Join-Path $RaceRoot 'held-delete'
        $null = New-Item -ItemType Directory -Path $DeleteRoot -Force
        $DeleteGateway = Join-Path $DeleteRoot 'gateway.log'
        $DeleteBoot = Join-Path $DeleteRoot 'gateway-boot.log'
        'authorized delete-pending content' | Set-Content -LiteralPath $DeleteGateway -Encoding UTF8
        'delete-pending boot' | Set-Content -LiteralPath $DeleteBoot -Encoding UTF8
        Set-SupportEnvironment $DeleteGateway $DeleteBoot (Join-Path $RaceRoot 'missing-kiro') 'kiro.log' ''
        $DeleteReady = Join-Path $DeleteRoot 'after-open.ready'
        $DeleteContinue = Join-Path $DeleteRoot 'after-open.continue'
        $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_SOURCE = $DeleteGateway
        $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_READY = $DeleteReady
        $env:GW_SUPPORT_TEST_AFTER_OPEN_BARRIER_CONTINUE = $DeleteContinue
        $deleteStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
        $deleteRun = Start-SupportRun (Join-Path $ExtractRoot 'held-delete-out')
        foreach ($attempt in 1..100) {
            if (Test-Path -LiteralPath $DeleteReady) { break }
            if ($deleteRun.Process.HasExited) { break }
            Start-Sleep -Milliseconds 50
        }
        $deleteBarrierReached = Test-Path -LiteralPath $DeleteReady
        $deleteSucceeded = $false
        $deleteError = ''
        if ($deleteBarrierReached) {
            try {
                Remove-Item -LiteralPath $DeleteGateway -Force -ErrorAction Stop
                $deleteSucceeded = $true
            } catch {
                $deleteError = $_.Exception.Message
            }
            'continue' | Set-Content -LiteralPath $DeleteContinue -Encoding ASCII
        }
        $deleteResult = Complete-SupportRun $deleteRun
        Assert-NoNewSupportStaging $deleteStagingBefore 'held-handle delete removes its global staging directory'
        Assert-True $deleteBarrierReached 'Windows collection pauses after the identity-validated source handle is open for delete'
        Assert-True $deleteSucceeded "Windows safe-open sharing permits source deletion while held: $deleteError"
        Assert-True ($deleteResult.ExitCode -eq 0) "Windows support continues from a delete-pending source: $(Format-SupportResult $deleteResult)"
        Assert-Absent $DeleteGateway 'delete-pending source is removed after collection closes its handle'
        $deleteBundleRoot = Expand-SupportBundle $deleteResult.Bundle (Join-Path $ExtractRoot 'held-delete-tree')
        Assert-CurrentLogContains (Join-Path $deleteBundleRoot 'logs\gateway\gateway.log') (Join-Path $deleteBundleRoot 'MANIFEST.txt') 'authorized delete-pending content' 'deleted open source snapshots the validated handle content'
        Assert-NoSupportTemporaryArtifacts $deleteBundleRoot 'held-handle delete bundle has no partial artifacts'
        Assert-NoSupportTemporaryArtifacts (Join-Path $ExtractRoot 'held-delete-out') 'held-handle delete output has no partial or staging artifacts'

        $AncestorRaceRoot = Join-Path $RaceRoot 'ancestor'
        $AncestorLive = Join-Path $AncestorRaceRoot 'live'
        $AncestorOriginal = Join-Path $AncestorRaceRoot 'live-original'
        $null = New-Item -ItemType Directory -Path $AncestorLive -Force
        $AncestorGateway = Join-Path $AncestorLive 'gateway.log'
        $AncestorBoot = Join-Path $AncestorLive 'gateway-boot.log'
        'ancestor authorized content' | Set-Content -LiteralPath $AncestorGateway -Encoding UTF8
        'ancestor boot' | Set-Content -LiteralPath $AncestorBoot -Encoding UTF8
        Set-SupportEnvironment $AncestorGateway $AncestorBoot (Join-Path $RaceRoot 'missing-kiro') 'kiro.log' ''
        $AncestorReady = Join-Path $RaceRoot 'ancestor.ready'
        $AncestorContinue = Join-Path $RaceRoot 'ancestor.continue'
        $env:GW_SUPPORT_TEST_BARRIER_SOURCE = $AncestorGateway
        $env:GW_SUPPORT_TEST_BARRIER_READY = $AncestorReady
        $env:GW_SUPPORT_TEST_BARRIER_CONTINUE = $AncestorContinue
        $ancestorStagingBefore = New-SupportStagingSnapshot $SupportGlobalTemp
        $ancestorRun = Start-SupportRun (Join-Path $ExtractRoot 'ancestor-race-out')
        foreach ($attempt in 1..100) {
            if (Test-Path -LiteralPath $AncestorReady) { break }
            if ($ancestorRun.Process.HasExited) { break }
            Start-Sleep -Milliseconds 50
        }
        $ancestorRaceExecuted = $false
        if (Test-Path -LiteralPath $AncestorReady) {
            Move-Item -LiteralPath $AncestorLive -Destination $AncestorOriginal
            & cmd.exe /d /c "mklink /J `"$AncestorLive`" `"$AncestorOriginal`"" | Out-Null
            $ancestorRaceExecuted = (Test-Path -LiteralPath $AncestorLive) -and
                ((Get-Item -LiteralPath $AncestorLive).Attributes -band [System.IO.FileAttributes]::ReparsePoint)
            if ($ancestorRaceExecuted) { $script:WindowsJunctions.Add($AncestorLive) | Out-Null }
            'continue' | Set-Content -LiteralPath $AncestorContinue -Encoding ASCII
        }
        $ancestorResult = Complete-SupportRun $ancestorRun
        Assert-NoNewSupportStaging $ancestorStagingBefore 'ancestor junction replacement removes its global staging directory'
        Assert-True ([bool]$ancestorRaceExecuted) 'ancestor directory is swapped to a junction between inspect and open'
        Assert-True ($ancestorResult.ExitCode -eq 0) "support continues after ancestor junction swap: $(Format-SupportResult $ancestorResult)"
        $ancestorBundleRoot = Expand-SupportBundle $ancestorResult.Bundle (Join-Path $ExtractRoot 'ancestor-race-tree')
        Assert-Absent (Join-Path $ancestorBundleRoot 'logs\gateway\gateway.log') 'ancestor junction replacement is rejected from archive'
        Assert-Line (Join-Path $ancestorBundleRoot 'MANIFEST.txt') 'WARNING: Gateway current log rejected: source replaced by reparse point' 'manifest records ancestor junction rejection'
        if (Test-Path -LiteralPath $AncestorLive) {
            & cmd.exe /d /c "rmdir `"$AncestorLive`"" | Out-Null
            $null = $script:WindowsJunctions.Remove($AncestorLive)
        }
    } else {
        Write-Host '  skip: ancestor junction swap requires native Windows filesystem semantics'
    }

    $requests = Get-Content -LiteralPath $RequestLog -Raw
    Assert-True ($requests.Contains('GET /metrics')) 'support requests metrics with GET'
    Assert-True ($requests.Contains('GET /admin/api/acp-capture?support=redacted')) 'support requests redacted capture with GET'
    Assert-True (-not $requests.Contains('POST ')) 'support never sends POST or mutates capture state'
    Assert-True (-not $requests.Contains('/mutate')) 'support ignores hostile curl configuration'
} finally {
    if ($script:HttpProcess -and -not $script:HttpProcess.HasExited) {
        Stop-Process -Id $script:HttpProcess.Id -Force -ErrorAction SilentlyContinue
        $script:HttpProcess.WaitForExit()
    }
    if ($RunningOnWindows) {
        foreach ($junction in @($script:WindowsJunctions)) {
            if (Test-Path -LiteralPath $junction) { & cmd.exe /d /c "rmdir `"$junction`"" | Out-Null }
        }
    }
    foreach ($path in @($script:FixtureRoot, $ExtractRoot)) {
        if ($path -and (Test-Path -LiteralPath $path)) { Remove-Item -LiteralPath $path -Recurse -Force -ErrorAction SilentlyContinue }
    }
}

Write-Host ''
Write-Host '== SUMMARY =='
Write-Host "passed: $script:Pass"
Write-Host "failed: $script:Fail"
if ($script:Fail -gt 0) { exit 1 }
exit 0
