$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$Wrapper = Join-Path $RepoRoot 'scripts\gw.ps1'
$FixtureRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('gw-privacy-cli-pwsh-' + [guid]::NewGuid().ToString('N'))
$HomeFixture = Join-Path $FixtureRoot 'home'
$ServerScript = Join-Path $FixtureRoot 'server.py'
$StateFile = Join-Path $FixtureRoot 'state'
$RequestLog = Join-Path $FixtureRoot 'requests.log'
$ProxyLog = Join-Path $FixtureRoot 'proxy-requests.log'
$Token = 'override-triage-token-7788'

function Fail-With([string]$Message) { throw "FAIL: $Message" }
function Invoke-Privacy([string]$Name, [string[]]$PrivacyArgs) {
    $output = & pwsh -NoProfile -File $Wrapper privacy @PrivacyArgs 2>&1 | Out-String
    $code = $LASTEXITCODE
    $path = Join-Path $FixtureRoot "$Name.out"
    [System.IO.File]::WriteAllText($path, $output, (New-Object System.Text.UTF8Encoding($false)))
    return [pscustomobject]@{ ExitCode = $code; Output = $output }
}
function Assert-Contains([string]$Text, [string]$Needle, [string]$Message) {
    if (-not $Text.Contains($Needle)) { Fail-With $Message }
}
function Set-State([string]$State) { [System.IO.File]::WriteAllText($StateFile, $State) }

New-Item -ItemType Directory -Path $HomeFixture -Force | Out-Null
try {
    @('HTTP_ADDR=127.0.0.1:18080','PRIVACY_TRIAGE_ENABLED=true','PRIVACY_TRIAGE_TOKEN=base-token-must-not-win') | Set-Content -LiteralPath (Join-Path $HomeFixture '.env') -Encoding UTF8
    @('# overrides win',"PRIVACY_TRIAGE_TOKEN=$Token") | Set-Content -LiteralPath (Join-Path $HomeFixture 'overrides.env') -Encoding UTF8

    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
    $proxyListener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $proxyListener.Start()
    $proxyPort = ([System.Net.IPEndPoint]$proxyListener.LocalEndpoint).Port
    $proxyListener.Stop()
    $python = @'
import http.server, os, pathlib, sys, urllib.parse
port = int(sys.argv[1]); state_path = pathlib.Path(sys.argv[2]); log_path = pathlib.Path(sys.argv[3]); kind = sys.argv[4]; token = os.environ['GW_TEST_EXPECT_TOKEN']
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def handle_one(self):
        state = state_path.read_text().strip()
        auth = self.headers.get('Authorization', '')
        raw_path = self.path
        path = urllib.parse.urlsplit(raw_path).path
        with log_path.open('a') as f: f.write(f'{kind} {self.command} {raw_path} auth={auth == "Bearer " + token} confirm={self.headers.get("X-GW-Privacy-Confirm", "")}\n')
        if path == '/admin/api/snapshot':
            if state == 'status-unauthorized':
                self.send_response(401); self.end_headers(); self.wfile.write(b'{"error":{"code":"unauthorized"}}'); return
            triage = 'false' if state == 'disabled' else 'true'
            self.send_response(200); self.end_headers(); self.wfile.write(f'{{"privacy":{{"default_profile":"strict","strict_available":true,"triage_enabled":{triage},"active_scopes":2,"mapping_entries":4}}}}'.encode()); return
        if auth != 'Bearer ' + token:
            self.send_response(401); self.end_headers(); self.wfile.write(b'{"error":{"code":"unauthorized"}}'); return
        if state == 'redirect':
            self.send_response(302); self.send_header('Location', 'http://192.0.2.1/privacy-escaped'); self.end_headers(); return
        if state == 'empty' and self.command == 'GET' and path == '/admin/api/privacy/scopes':
            self.send_response(200); self.end_headers(); self.wfile.write(b'[]'); return
        if self.command == 'GET' and path.endswith('/mapping'):
            self.send_response(200); self.end_headers(); self.wfile.write(b'[{"entity":"IPv4","original":"10.0.0.8","synthetic":"198.18.0.8","provenance":"input"}]'); return
        if self.command == 'GET':
            self.send_response(200); self.end_headers(); self.wfile.write(b'[{"id":"scope-safe","profile":"strict","state":"active","entries":2,"in_flight":0}]'); return
        if path == '/admin/api/privacy/scopes':
            if self.headers.get('X-GW-Privacy-Confirm') != 'clear-all':
                self.send_response(400); self.end_headers(); self.wfile.write(b'{"error":{"code":"confirmation_required"}}'); return
            self.send_response(204); self.end_headers(); return
        self.send_response(202); self.end_headers(); self.wfile.write(b'{"state":"closing"}')
    do_GET = handle_one
    do_DELETE = handle_one
http.server.ThreadingHTTPServer(('127.0.0.1', port), Handler).serve_forever()
'@
    [System.IO.File]::WriteAllText($ServerScript, $python, (New-Object System.Text.UTF8Encoding($false)))
    Set-State 'enabled'
    $env:GW_TEST_EXPECT_TOKEN = $Token
    $server = Start-Process python3 -ArgumentList @($ServerScript, "$port", $StateFile, $RequestLog, 'target') -PassThru -NoNewWindow
    $proxyServer = Start-Process python3 -ArgumentList @($ServerScript, "$proxyPort", $StateFile, $ProxyLog, 'proxy') -PassThru -NoNewWindow
    try {
        $env:GW_HOME = $HomeFixture
        $env:GW_ADDR = "http://127.0.0.1:$port"
        for ($attempt = 0; $attempt -lt 40; $attempt++) {
            try { $null = Invoke-RestMethod -Uri "$($env:GW_ADDR)/admin/api/snapshot" -TimeoutSec 1; break } catch { Start-Sleep -Milliseconds 50 }
        }
        $proxyReady = $false
        for ($attempt = 0; $attempt -lt 40; $attempt++) {
            try {
                $null = Invoke-RestMethod -Uri "http://127.0.0.1:$proxyPort/admin/api/snapshot" -TimeoutSec 1
                $proxyReady = $true
                break
            } catch { Start-Sleep -Milliseconds 50 }
        }
        if (-not $proxyReady) { Fail-With 'proxy fixture did not become ready' }
        Remove-Item -LiteralPath $ProxyLog -Force -ErrorAction SilentlyContinue

        $status = Invoke-Privacy 'status' @('status')
        if ($status.ExitCode -ne 0) { Fail-With "privacy status failed: $($status.Output)" }
        Assert-Contains $status.Output 'profile: strict' 'status did not render safe profile'
        Assert-Contains $status.Output 'triage: enabled' 'status did not render enabled triage'

        Set-State 'disabled'
        $disabled = Invoke-Privacy 'disabled' @('status')
        Assert-Contains $disabled.Output 'triage: disabled' 'status did not render disabled triage'
        Set-State 'status-unauthorized'
        $statusUnauthorized = Invoke-Privacy 'status-unauthorized' @('status')
        if ($statusUnauthorized.ExitCode -ne 1) { Fail-With "unauthorized status returned $($statusUnauthorized.ExitCode), expected operation exit code 1" }
        Assert-Contains $statusUnauthorized.Output 'HTTP 401' 'unauthorized status did not surface its HTTP status'
        Set-State 'enabled'

        $scopes = Invoke-Privacy 'scopes' @('scopes')
        if ($scopes.ExitCode -ne 0) { Fail-With 'scopes failed' }
        Assert-Contains $scopes.Output 'scope-safe' 'scopes response missing'
        Set-State 'empty'
        $emptyScopes = Invoke-Privacy 'empty-scopes' @('scopes')
        if ($emptyScopes.ExitCode -ne 0) { Fail-With "empty scopes failed: $($emptyScopes.Output)" }
        if (@($emptyScopes.Output -split '\r?\n') -cnotcontains '[]') { Fail-With "empty scopes rendered incorrectly: $($emptyScopes.Output)" }
        if ($emptyScopes.Output.Contains('privacy: cleared')) { Fail-With 'empty scopes was misreported as a clear operation' }
        Set-State 'enabled'
        $inspect = Invoke-Privacy 'inspect' @('inspect','scope-safe')
        Assert-Contains $inspect.Output '198.18.0.8' 'inspect response missing'
        $closing = Invoke-Privacy 'closing' @('clear','scope-safe')
        Assert-Contains $closing.Output 'closing' 'closing state missing'
        $clearAll = Invoke-Privacy 'clear-all' @('clear','--all','--yes')
        Assert-Contains $clearAll.Output 'cleared' "clear-all state missing: $($clearAll.Output)"

        $proxyVariables = @('HTTP_PROXY','HTTPS_PROXY','ALL_PROXY','http_proxy','https_proxy','all_proxy','NO_PROXY','no_proxy')
        $savedProxyEnvironment = @{}
        foreach ($name in $proxyVariables) { $savedProxyEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
        try {
            foreach ($name in @('HTTP_PROXY','HTTPS_PROXY','ALL_PROXY','http_proxy','https_proxy','all_proxy')) {
                [Environment]::SetEnvironmentVariable($name, "http://127.0.0.1:$proxyPort", 'Process')
            }
            [Environment]::SetEnvironmentVariable('NO_PROXY', '', 'Process')
            [Environment]::SetEnvironmentVariable('no_proxy', '', 'Process')

            $proxyScopes = Invoke-Privacy 'proxy-scopes' @('scopes')
            $proxyInspect = Invoke-Privacy 'proxy-inspect' @('inspect','scope-safe')
            $proxyStatus = Invoke-Privacy 'proxy-status' @('status')
            if ($proxyScopes.ExitCode -ne 0 -or $proxyInspect.ExitCode -ne 0 -or $proxyStatus.ExitCode -ne 0) {
                Fail-With 'proxy-confinement requests did not reach the direct loopback target'
            }
            Assert-Contains $proxyScopes.Output 'scope-safe' 'proxy-confinement scopes response missing'
            Assert-Contains $proxyInspect.Output '198.18.0.8' 'proxy-confinement inspect response missing'
            Assert-Contains $proxyStatus.Output 'profile: strict' 'proxy-confinement status response missing'
            if (Test-Path -LiteralPath $ProxyLog) {
                Fail-With "privacy commands contacted the configured proxy: $(Get-Content -LiteralPath $ProxyLog -Raw)"
            }
        } finally {
            foreach ($name in $proxyVariables) {
                [Environment]::SetEnvironmentVariable($name, $savedProxyEnvironment[$name], 'Process')
            }
        }

        $invalidConfirmations = @(
            @{ Name = 'missing-yes'; Args = @('clear','--all') },
            @{ Name = 'abbreviated-y'; Args = @('clear','--all','-Y') },
            @{ Name = 'single-dash-yes'; Args = @('clear','--all','-Yes') },
            @{ Name = 'wrong-case-yes'; Args = @('clear','--all','--Yes') },
            @{ Name = 'reordered-yes'; Args = @('clear','--yes','--all') },
            @{ Name = 'extra-after-yes'; Args = @('clear','--all','--yes','extra') }
        )
        foreach ($case in $invalidConfirmations) {
            $before = if (Test-Path $RequestLog) { @(Get-Content $RequestLog).Count } else { 0 }
            $rejected = Invoke-Privacy $case.Name $case.Args
            if ($rejected.ExitCode -eq 0) { Fail-With "$($case.Name) clear-all confirmation unexpectedly succeeded" }
            if ($rejected.ExitCode -ne 2) { Fail-With "$($case.Name) returned $($rejected.ExitCode), expected usage exit code 2" }
            Assert-Contains $rejected.Output 'requires exact --yes confirmation' "$($case.Name) did not report exact confirmation requirement"
            $after = @(Get-Content $RequestLog).Count
            if ($before -ne $after) { Fail-With "$($case.Name) contacted API" }
        }

        @('# overrides win','PRIVACY_TRIAGE_TOKEN=wrong-token') | Set-Content -LiteralPath (Join-Path $HomeFixture 'overrides.env') -Encoding UTF8
        $unauthorized = Invoke-Privacy 'unauthorized' @('scopes')
        if ($unauthorized.ExitCode -eq 0) { Fail-With 'unauthorized request exited zero' }
        Assert-Contains $unauthorized.Output 'unauthorized' 'unauthorized state missing'
        @('# overrides win',"PRIVACY_TRIAGE_TOKEN=$Token") | Set-Content -LiteralPath (Join-Path $HomeFixture 'overrides.env') -Encoding UTF8

        Set-State 'redirect'
        $redirect = Invoke-Privacy 'redirect' @('scopes')
        if ($redirect.ExitCode -eq 0) { Fail-With 'redirect response was followed or treated as success' }
        Set-State 'enabled'

        $unsafe = Invoke-Privacy 'unsafe' @('inspect','../escape')
        if ($unsafe.ExitCode -eq 0) { Fail-With 'unsafe scope identifier was accepted' }
        Assert-Contains $unsafe.Output 'invalid scope' 'unsafe scope rejection missing'

        $canonicalAddress = $env:GW_ADDR
        foreach ($addressCase in @(
            @{ Name = 'short-ipv4'; Address = "http://127.1:$port" },
            @{ Name = 'integer-ipv4'; Address = "http://2130706433:$port" },
            @{ Name = 'mapped-ipv6'; Address = "http://[::ffff:127.0.0.1]:$port" },
            @{ Name = 'uppercase-scheme'; Address = "HTTP://127.0.0.1:$port" }
        )) {
            $before = @(Get-Content -LiteralPath $RequestLog).Count
            $env:GW_ADDR = $addressCase.Address
            $addressResult = Invoke-Privacy $addressCase.Name @('scopes')
            if ($addressResult.ExitCode -eq 0) { Fail-With "$($addressCase.Name) non-canonical loopback address was accepted" }
            Assert-Contains $addressResult.Output 'must be loopback' "$($addressCase.Name) did not report the common loopback grammar"
            if (@(Get-Content -LiteralPath $RequestLog).Count -ne $before) { Fail-With "$($addressCase.Name) contacted the API" }
        }
        $env:GW_ADDR = $canonicalAddress

        $server.Kill(); $server.WaitForExit()
        $unavailable = Invoke-Privacy 'unavailable' @('scopes')
        if ($unavailable.ExitCode -eq 0) { Fail-With 'unavailable request exited zero' }
        Assert-Contains $unavailable.Output 'unavailable' "unavailable state missing: $($unavailable.Output)"

        $requestCount = @(Get-Content -LiteralPath $RequestLog).Count
        $env:GW_ADDR = 'https://privacy.example.test'
        $remoteTarget = Invoke-Privacy 'remote-target' @('scopes')
        if ($remoteTarget.ExitCode -eq 0) { Fail-With 'non-loopback privacy target was accepted' }
        Assert-Contains $remoteTarget.Output 'must be loopback' 'non-loopback privacy target was not rejected explicitly'
        if (@(Get-Content -LiteralPath $RequestLog).Count -ne $requestCount) { Fail-With 'non-loopback privacy target contacted the API' }

        foreach ($file in Get-ChildItem -LiteralPath $FixtureRoot -Filter '*.out') {
            if ((Get-Content -LiteralPath $file.FullName -Raw).Contains($Token)) { Fail-With "triage token leaked in $($file.Name)" }
        }
        $requestText = Get-Content -LiteralPath $RequestLog -Raw
        Assert-Contains $requestText 'auth=True' 'effective overrides token was not sent in-process'
        Assert-Contains $requestText 'confirm=clear-all' 'clear-all confirmation header missing'
        Write-Host 'PASS: PowerShell privacy CLI'
    } finally {
        if ($server -and -not $server.HasExited) { $server.Kill(); $server.WaitForExit() }
        if ($proxyServer -and -not $proxyServer.HasExited) { $proxyServer.Kill(); $proxyServer.WaitForExit() }
    }
} finally {
    Remove-Item -LiteralPath $FixtureRoot -Recurse -Force -ErrorAction SilentlyContinue
}
