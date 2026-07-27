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
        [AllowEmptyString()][string]$HermesHome
    )
    $env:HOME = $HomeFixture
    $env:USERPROFILE = $HomeFixture
    $env:GW_HOME = $GatewayHome
    $env:GW_BIN = if ($RunningOnWindows) { 'cmd.exe' } else { '/usr/bin/true' }
    $env:GW_STATE_DIR = Join-Path $GatewayHome 'state'
    $env:GW_PID = Join-Path $GatewayHome 'state\gateway.pid'
    $env:GW_LOG = $GatewayLog
    $env:GW_LOGOUT = $GatewayBoot
    $env:GW_LOGERR = Join-Path $GatewayHome 'logs\unused-boot-err.log'
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
}

function Invoke-SupportRun {
    param(
        [string]$OutDir,
        [int]$MaxMb = 50,
        [AllowEmptyString()][string]$ExplicitCoworker = ''
    )
    $null = New-Item -ItemType Directory -Path $OutDir -Force
    $stderr = "$OutDir.stderr"
    $args = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $Wrapper, 'support', '-Out', $OutDir, '-MaxMb', "$MaxMb", '-LogDays', '9999')
    if ($ExplicitCoworker) { $args += @('-CoworkerHome', $ExplicitCoworker) }
    $stdout = @(& $PowerShellExecutable @args 2> $stderr)
    $rc = $LASTEXITCODE
    return [pscustomobject]@{
        ExitCode = $rc
        Bundle = if ($stdout.Count -gt 0) { [string]$stdout[-1] } else { '' }
        Stderr = if (Test-Path -LiteralPath $stderr) { Get-Content -LiteralPath $stderr -Raw } else { '' }
    }
}

function Start-SupportRun {
    param([string]$OutDir, [int]$MaxMb = 50)
    $null = New-Item -ItemType Directory -Path $OutDir -Force
    $stdout = "$OutDir.stdout"
    $stderr = "$OutDir.stderr"
    $arguments = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $Wrapper, 'support', '-Out', $OutDir, '-MaxMb', "$MaxMb", '-LogDays', '9999')
    $quotedArguments = @($arguments | ForEach-Object { '"' + ([string]$_).Replace('"', '\"') + '"' }) -join ' '
    $process = Start-Process -FilePath $PowerShellExecutable -ArgumentList $quotedArguments `
        -RedirectStandardOutput $stdout -RedirectStandardError $stderr -PassThru
    return [pscustomobject]@{ Process = $process; Stdout = $stdout; Stderr = $stderr; OutDir = $OutDir }
}

function Complete-SupportRun($Run) {
    $Run.Process.WaitForExit()
    $stdout = if (Test-Path -LiteralPath $Run.Stdout) { @(Get-Content -LiteralPath $Run.Stdout) } else { @() }
    return [pscustomobject]@{
        ExitCode = $Run.Process.ExitCode
        Bundle = if ($stdout.Count -gt 0) { [string]$stdout[-1] } else { '' }
        Stderr = if (Test-Path -LiteralPath $Run.Stderr) { Get-Content -LiteralPath $Run.Stderr -Raw } else { '' }
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
    $ServerScript = Join-Path $script:FixtureRoot 'server.py'
    'enabled' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    @'
import http.server, json, sys
port_file, request_log, mode_file = sys.argv[1:]
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
        if self.path == "/metrics": self.send_body(200, "text/plain", "# HELP gateway_up fixture\ngateway_up 1\n")
        elif self.path == "/admin/api/acp-capture?support=redacted":
            mode = open(mode_file, encoding="utf-8-sig").read().strip()
            if mode == "enabled":
                self.send_body(200, "application/json", json.dumps({"enabled": True, "allowRuntimeToggle": True, "count": 1, "size": 8, "frames": [{"seq": 7, "method": "session/update", "params": "{\"safe\":\"capture safe\",\"token\":\"[REDACTED]\"}", "bytes": 64}]}))
            elif mode == "disabled": self.send_body(200, "application/json", '{"enabled":false,"frames":[]}')
            elif mode == "wrongtype": self.send_body(200, "application/json", '{"enabled":"true","frames":[]}')
            else: self.send_body(200, "application/json", '{"enabled":true')
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
    $script:HttpProcess = Start-Process -FilePath $Python -ArgumentList @($ServerScript, $PortFile, $RequestLog, $ModeFile) -PassThru
    foreach ($attempt in 1..50) {
        if (Test-Path -LiteralPath $PortFile) { break }
        Start-Sleep -Milliseconds 100
    }
    if (-not (Test-Path -LiteralPath $PortFile)) { throw 'deterministic HTTP endpoint did not start' }
    $HttpPort = (Get-Content -LiteralPath $PortFile -Raw).Trim()
    @("request = POST", "url = http://127.0.0.1:$HttpPort/mutate") | Set-Content -LiteralPath (Join-Path $HomeFixture '.curlrc') -Encoding ASCII

    $GatewayLog = Join-Path $GatewayHome 'logs\gateway.log'
    $GatewayBoot = Join-Path $GatewayHome 'logs\gateway-boot.log'
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome

    Write-Host '== enabled support bundle =='
    $main = Invoke-SupportRun (Join-Path $ExtractRoot 'main-out')
    Assert-True ($main.ExitCode -eq 0) "support exits zero with HERMES_HOME fallback: $($main.Stderr)"
    Assert-File $main.Bundle 'bundle path is printed and exists'
    Assert-File (Join-Path $ExtractRoot 'main-out\latest.zip') 'latest.zip copy exists'
    $mainRoot = Expand-SupportBundle $main.Bundle (Join-Path $ExtractRoot 'main-tree')
    foreach ($section in @('env','health','logs','system','tray')) { Assert-Directory (Join-Path $mainRoot $section) "standard $section section exists" }
    $logNames = @((Get-ChildItem -LiteralPath (Join-Path $mainRoot 'logs') -Directory | Sort-Object Name | ForEach-Object Name)) -join ','
    Assert-True ($logNames -ceq 'co-worker,gateway,kiro') "logs contains exactly application directories (got $logNames)"
    Assert-Absent (Join-Path $mainRoot 'logs\gateway.log') 'flat Gateway layout is absent'

    foreach ($relative in @('gateway.log','gateway-boot.log','gateway-chat-trace.log','gateway-20200101.log.gz','gateway-20260101.log.gz')) {
        Assert-File (Join-Path $mainRoot "logs\gateway\$relative") "Gateway artifact $relative is organized"
    }
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
    Assert-True ($cap.ExitCode -eq 0) "support exits zero with explicit Co-worker home: $($cap.Stderr)"
    $capRoot = Expand-SupportBundle $cap.Bundle (Join-Path $ExtractRoot 'cap-tree')
    foreach ($relative in @('logs\gateway\gateway.log','logs\kiro\kiro-chat.log','logs\co-worker\agent.log','MANIFEST.txt')) { Assert-File (Join-Path $capRoot $relative) "cap preserves protected $relative" }
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
    Assert-True ($overheadCap.ExitCode -eq 0) "near-cap support exits zero: $($overheadCap.Stderr)"
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
    Assert-True ($disabled.ExitCode -eq 0) 'support continues without a Co-worker home'
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $disabledRoot 'logs\gateway') -Filter 'acp-capture-*.json').Count -eq 0) 'disabled capture creates no export'
    Assert-Contains (Join-Path $disabledRoot 'MANIFEST.txt') 'capture: disabled' 'manifest records disabled capture'
    Assert-Contains (Join-Path $disabledRoot 'MANIFEST.txt') 'Co-worker logs unavailable' 'manifest records missing Co-worker home'

    'wrongtype' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    $invalid = Invoke-SupportRun (Join-Path $ExtractRoot 'invalid-out')
    $invalidRoot = Expand-SupportBundle $invalid.Bundle (Join-Path $ExtractRoot 'invalid-tree')
    Assert-True ($invalid.ExitCode -eq 0) 'support tolerates non-boolean capture state'
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $invalidRoot 'logs\gateway') -Filter 'acp-capture-*.json').Count -eq 0) 'non-boolean capture creates no export'
    Assert-Contains (Join-Path $invalidRoot 'MANIFEST.txt') 'capture: unavailable' 'manifest records non-boolean capture as unavailable'

    Write-Host '== fail-closed safe-open and atomic snapshot publish =='
    'enabled' | Set-Content -LiteralPath $ModeFile -Encoding ASCII
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
    $env:GW_SUPPORT_TEST_DISABLE_SAFE_OPEN = 'true'
    $noSafeOpen = Invoke-SupportRun (Join-Path $ExtractRoot 'no-safe-open-out')
    $noSafeOpenRoot = Expand-SupportBundle $noSafeOpen.Bundle (Join-Path $ExtractRoot 'no-safe-open-tree')
    Assert-True ($noSafeOpen.ExitCode -eq 0) 'support continues when native safe-open is unavailable'
    Assert-Absent (Join-Path $noSafeOpenRoot 'logs\gateway\gateway.log') 'Gateway source is omitted without safe-open'
    Assert-Absent (Join-Path $noSafeOpenRoot 'logs\kiro\kiro-chat.log') 'Kiro source is omitted without safe-open'
    Assert-Absent (Join-Path $noSafeOpenRoot 'logs\co-worker\agent.log') 'Co-worker source is omitted without safe-open'
    foreach ($warning in @('Gateway current log safe-open unavailable','Kiro current log safe-open unavailable','Co-worker agent.log safe-open unavailable')) {
        Assert-Line (Join-Path $noSafeOpenRoot 'MANIFEST.txt') "WARNING: $warning" "manifest records $warning"
    }

    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
    $env:GW_SUPPORT_TEST_FAIL_PUBLISH = 'metrics,capture'
    $publishFailure = Invoke-SupportRun (Join-Path $ExtractRoot 'publish-failure-out')
    $publishFailureRoot = Expand-SupportBundle $publishFailure.Bundle (Join-Path $ExtractRoot 'publish-failure-tree')
    Assert-True ($publishFailure.ExitCode -eq 0) 'support continues after forced live-snapshot publish failures'
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $publishFailureRoot 'logs\gateway') -Filter 'metrics-snapshot-*.prom').Count -eq 0) 'failed metrics publish leaves no partial artifact'
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $publishFailureRoot 'logs\gateway') -Filter 'acp-capture-*.json').Count -eq 0) 'failed capture publish leaves no partial artifact'
    Assert-Line (Join-Path $publishFailureRoot 'MANIFEST.txt') 'WARNING: Metrics snapshot unavailable: atomic publish failed' 'manifest records metrics publish failure'
    Assert-Line (Join-Path $publishFailureRoot 'MANIFEST.txt') 'WARNING: ACP capture unavailable: atomic publish failed' 'manifest records capture publish failure'
    Assert-NoSupportTemporaryArtifacts $publishFailureRoot 'metrics/capture failure bundle has no partial artifacts'
    Assert-NoSupportTemporaryArtifacts (Join-Path $ExtractRoot 'publish-failure-out') 'metrics/capture failure output has no partial or staging artifacts'

    Write-Host '== atomic publication cleanup for every artifact kind =='
    Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
    $env:GW_SUPPORT_TEST_FAIL_PUBLISH = 'plain,gzip,metrics,capture'
    $leafPublishFailure = Invoke-SupportRun (Join-Path $ExtractRoot 'leaf-publish-failure-out')
    Assert-True ($leafPublishFailure.ExitCode -eq 0) 'support continues after forced plain/gzip/metrics/capture publication failures'
    $leafPublishFailureRoot = Expand-SupportBundle $leafPublishFailure.Bundle (Join-Path $ExtractRoot 'leaf-publish-failure-tree')
    Assert-Absent (Join-Path $leafPublishFailureRoot 'logs\gateway\gateway.log') 'forced plain publication failure leaves no final plain log'
    Assert-Absent (Join-Path $leafPublishFailureRoot 'logs\gateway\gateway-20260101.log.gz') 'forced gzip publication failure leaves no final gzip log'
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $leafPublishFailureRoot 'logs\gateway') -Filter 'metrics-snapshot-*').Count -eq 0) 'forced metrics publication failure leaves no final or partial snapshot'
    Assert-True (@(Get-ChildItem -LiteralPath (Join-Path $leafPublishFailureRoot 'logs\gateway') -Filter 'acp-capture-*').Count -eq 0) 'forced capture publication failure leaves no final or partial snapshot'
    Assert-Line (Join-Path $leafPublishFailureRoot 'MANIFEST.txt') 'WARNING: Gateway current log redaction failed' 'manifest proves the forced plain publication failure path ran'
    Assert-Line (Join-Path $leafPublishFailureRoot 'MANIFEST.txt') 'WARNING: Gateway rotation gateway-20260101.log.gz archive publish failed' 'manifest proves the forced gzip publication failure path ran'
    Assert-NoSupportTemporaryArtifacts $leafPublishFailureRoot 'plain/gzip/metrics/capture failure bundle has no partial artifacts'
    Assert-NoSupportTemporaryArtifacts (Join-Path $ExtractRoot 'leaf-publish-failure-out') 'plain/gzip/metrics/capture failure output has no partial or staging artifacts'

    foreach ($failureKind in @('manifest','archive','latest')) {
        Set-SupportEnvironment $GatewayLog $GatewayBoot $KiroCwdFixture 'native/kiro-current.log' $CoworkerHome
        $env:GW_SUPPORT_TEST_FAIL_PUBLISH = $failureKind
        $failureOut = Join-Path $ExtractRoot "$failureKind-publish-failure-out"
        $terminalPublishFailure = Invoke-SupportRun $failureOut
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
    Assert-True ($missing.ExitCode -eq 0) 'support continues when configured sources are missing'
    foreach ($warning in @('Gateway current log missing','Gateway boot log missing','Kiro current log missing')) { Assert-Contains (Join-Path $missingRoot 'MANIFEST.txt') $warning "manifest records $warning" }

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
            Assert-True ($unreadable.ExitCode -eq 0) 'support continues when configured sources are unreadable'
            foreach ($warning in @('Gateway current log unreadable','Kiro current log unreadable','Co-worker agent.log unreadable')) {
                Assert-Line (Join-Path $unreadableRoot 'MANIFEST.txt') "WARNING: $warning" "manifest records $warning"
            }
        } finally {
            & chmod 600 $UnreadableGateway $UnreadableKiro $UnreadableCoworker
        }
    } else {
        Write-Host '  skip: unreadable-source warning fixture requires Unix chmod semantics'
    }

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
    Assert-True $regularRaceExecuted 'regular source is replaced between identity inspection and safe-open'
    Assert-True ($raceResult.ExitCode -eq 0) "support continues after deterministic regular replacement: $($raceResult.Stderr)"
    $raceBundleRoot = Expand-SupportBundle $raceResult.Bundle (Join-Path $ExtractRoot 'regular-race-tree')
    Assert-Absent (Join-Path $raceBundleRoot 'logs\gateway\gateway.log') 'regular replacement is rejected from archive'
    Assert-Line (Join-Path $raceBundleRoot 'MANIFEST.txt') 'WARNING: Gateway current log rejected: source replaced before safe-open' 'manifest records regular identity replacement'
    Assert-NoSecretInTree $raceBundleRoot @('replacement external secret 8811') 'regular replacement bundle'

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
    $heldRun = Start-SupportRun (Join-Path $ExtractRoot 'held-rename-out')
    foreach ($attempt in 1..100) {
        if (Test-Path -LiteralPath $HeldReady) { break }
        if ($heldRun.Process.HasExited) { break }
        Start-Sleep -Milliseconds 50
    }
    $heldBarrierReached = Test-Path -LiteralPath $HeldReady
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
    Assert-True $heldBarrierReached 'collection pauses only after the identity-validated source handle is open'
    Assert-True $heldRenameSucceeded "open source permits rename with replacement while held: $heldRenameError"
    Assert-True ($heldResult.ExitCode -eq 0) "support continues from a renamed open source: $($heldResult.Stderr)"
    $heldBundleRoot = Expand-SupportBundle $heldResult.Bundle (Join-Path $ExtractRoot 'held-rename-tree')
    Assert-Contains (Join-Path $heldBundleRoot 'logs\gateway\gateway.log') 'authorized held-handle content' 'renamed open source snapshots the validated handle content'
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
        Assert-True $deleteBarrierReached 'Windows collection pauses after the identity-validated source handle is open for delete'
        Assert-True $deleteSucceeded "Windows safe-open sharing permits source deletion while held: $deleteError"
        Assert-True ($deleteResult.ExitCode -eq 0) "Windows support continues from a delete-pending source: $($deleteResult.Stderr)"
        Assert-Absent $DeleteGateway 'delete-pending source is removed after collection closes its handle'
        $deleteBundleRoot = Expand-SupportBundle $deleteResult.Bundle (Join-Path $ExtractRoot 'held-delete-tree')
        Assert-Contains (Join-Path $deleteBundleRoot 'logs\gateway\gateway.log') 'authorized delete-pending content' 'deleted open source snapshots the validated handle content'
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
        Assert-True ([bool]$ancestorRaceExecuted) 'ancestor directory is swapped to a junction between inspect and open'
        Assert-True ($ancestorResult.ExitCode -eq 0) "support continues after ancestor junction swap: $($ancestorResult.Stderr)"
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
