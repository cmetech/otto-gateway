$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$Wrapper = Join-Path $RepoRoot 'scripts\gw.ps1'
$FixtureRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('gw-privacy-cli-pwsh-' + [guid]::NewGuid().ToString('N'))
$HomeFixture = Join-Path $FixtureRoot 'home'
$ServerScript = Join-Path $FixtureRoot 'server.py'
$StateFile = Join-Path $FixtureRoot 'state'
$RequestLog = Join-Path $FixtureRoot 'requests.log'
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
    $python = @'
import http.server, os, pathlib, sys
port = int(sys.argv[1]); state_path = pathlib.Path(sys.argv[2]); log_path = pathlib.Path(sys.argv[3]); token = os.environ['GW_TEST_EXPECT_TOKEN']
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def handle_one(self):
        state = state_path.read_text().strip()
        auth = self.headers.get('Authorization', '')
        with log_path.open('a') as f: f.write(f'{self.command} {self.path} auth={auth == "Bearer " + token} confirm={self.headers.get("X-GW-Privacy-Confirm", "")}\n')
        if self.path == '/admin/api/snapshot':
            triage = 'false' if state == 'disabled' else 'true'
            self.send_response(200); self.end_headers(); self.wfile.write(f'{{"privacy":{{"default_profile":"strict","strict_available":true,"triage_enabled":{triage},"active_scopes":2,"mapping_entries":4}}}}'.encode()); return
        if auth != 'Bearer ' + token:
            self.send_response(401); self.end_headers(); self.wfile.write(b'{"error":{"code":"unauthorized"}}'); return
        if state == 'redirect':
            self.send_response(302); self.send_header('Location', 'http://192.0.2.1/privacy-escaped'); self.end_headers(); return
        if self.command == 'GET' and self.path.endswith('/mapping'):
            self.send_response(200); self.end_headers(); self.wfile.write(b'[{"entity":"IPv4","original":"10.0.0.8","synthetic":"198.18.0.8","provenance":"input"}]'); return
        if self.command == 'GET':
            self.send_response(200); self.end_headers(); self.wfile.write(b'[{"id":"scope-safe","profile":"strict","state":"active","entries":2,"in_flight":0}]'); return
        if self.path == '/admin/api/privacy/scopes':
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
    $server = Start-Process python3 -ArgumentList @($ServerScript, "$port", $StateFile, $RequestLog) -PassThru -NoNewWindow
    try {
        $env:GW_HOME = $HomeFixture
        $env:GW_ADDR = "http://127.0.0.1:$port"
        for ($attempt = 0; $attempt -lt 40; $attempt++) {
            try { $null = Invoke-RestMethod -Uri "$($env:GW_ADDR)/admin/api/snapshot" -TimeoutSec 1; break } catch { Start-Sleep -Milliseconds 50 }
        }

        $status = Invoke-Privacy 'status' @('status')
        if ($status.ExitCode -ne 0) { Fail-With "privacy status failed: $($status.Output)" }
        Assert-Contains $status.Output 'profile: strict' 'status did not render safe profile'
        Assert-Contains $status.Output 'triage: enabled' 'status did not render enabled triage'

        Set-State 'disabled'
        $disabled = Invoke-Privacy 'disabled' @('status')
        Assert-Contains $disabled.Output 'triage: disabled' 'status did not render disabled triage'
        Set-State 'enabled'

        $scopes = Invoke-Privacy 'scopes' @('scopes')
        if ($scopes.ExitCode -ne 0) { Fail-With 'scopes failed' }
        Assert-Contains $scopes.Output 'scope-safe' 'scopes response missing'
        $inspect = Invoke-Privacy 'inspect' @('inspect','scope-safe')
        Assert-Contains $inspect.Output '198.18.0.8' 'inspect response missing'
        $closing = Invoke-Privacy 'closing' @('clear','scope-safe')
        Assert-Contains $closing.Output 'closing' 'closing state missing'
        $clearAll = Invoke-Privacy 'clear-all' @('clear','--all','--yes')
        Assert-Contains $clearAll.Output 'cleared' "clear-all state missing: $($clearAll.Output)"

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
    }
} finally {
    Remove-Item -LiteralPath $FixtureRoot -Recurse -Force -ErrorAction SilentlyContinue
}
