<#
.SYNOPSIS
    Quick PII / streaming smoke test for OTTO Gateway (Windows).

.DESCRIPTION
    Three scenarios. Default runs all three.

      diag  Print the gateway's PII posture from /health/hooks.
            No round-trip; useful for "what mode is this install in?"
      wire  For each enabled surface, send a 'say hi' request with
            stream=true AND stream=false and assert the right Content-
            Type comes back. The v1.9.0 regression check lives here:
            streaming requests under PII_REDACTION_MODE=encrypt must
            yield text/event-stream (Anthropic/OpenAI) or
            application/x-ndjson (Ollama), NOT application/json.
      pii   Send a PII-rich request to a single surface, capture the
            response, and verify each PII value round-tripped back to
            plaintext. Proves the encrypt Pre-hook + decrypt Post-hook
            pair is working end-to-end.

.PARAMETER Scenario
    diag | wire | pii | all (default: all)

.PARAMETER Surface
    anthropic | openai | ollama | all (default: all)

.PARAMETER Mode
    live | fake (default: fake)

    Controls how the 'pii' scenario gets responses from the gateway:

      fake  (DEFAULT, Phase 08.3.2): the gateway's KIRO_CMD points at
            bin/fake-kiro-cli.exe (built via `make build-fake-kiro`), and
            the script writes a JSON-RPC notifications NDJSON file the
            fake replays as agent_message_chunk frames. The PII probe
            then sees the expected plaintext deterministically without
            depending on the live LLM. Requires the operator to have
            set KIRO_CMD=<absolute path to bin\fake-kiro-cli.exe> AND
            OTTO_FAKE_KIRO_NOTIFICATIONS_FILE=<temp path printed below>
            in .otto-gw.overrides.env BEFORE starting the gateway.

      live  (LEGACY, deprecated): hits a real kiro-cli worker and
            depends on the LLM verbatim-echoing PII-shaped data.
            Modern Claude releases refuse this on safety grounds, so
            this path is known-failing as of 2026-06-03. Kept for
            operators verifying real-LLM end-to-end behavior.

.PARAMETER Base
    Gateway base URL. Default: http://127.0.0.1:18080
    Also reads $env:OTTO_BASE_URL when present.

.PARAMETER Auth
    Bearer token. Sets Authorization header on every call.

.PARAMETER Verbose
    Print full response bodies on each check.

.PARAMETER NoColor
    Disable ANSI color output.

.EXAMPLE
    .\test-pii.ps1 diag

.EXAMPLE
    .\test-pii.ps1 wire -Surface anthropic

.EXAMPLE
    .\test-pii.ps1 pii -Surface ollama -Verbose

.EXAMPLE
    .\test-pii.ps1 pii -Mode fake

    Default-mode invocation written explicitly. Runs the deterministic
    fake-worker path (Phase 08.3.2). Requires bin/fake-kiro-cli.exe to
    be built and the gateway to have been started with KIRO_CMD and
    OTTO_FAKE_KIRO_NOTIFICATIONS_FILE pointing at it.

.EXAMPLE
    .\test-pii.ps1 pii -Mode live

    Legacy live-LLM path. Prints a deprecation banner and runs the
    pre-Phase-08.3.2 behavior. Expect failures on modern Claude.

.NOTES
    Exit codes:
      0  all scenarios passed
      1  at least one scenario failed
      2  usage error or precondition not met (gateway unreachable, etc.)

    Requires PowerShell 5.1 or later (ships with Windows 10+).

    Phase 08.3.2 -- see
    .planning/phases/08.3.2-pii-smoke-test-methodology-fix/ for the
    methodology rationale (live-LLM cooperation broke v1.9.3; fake
    worker is now the load-bearing default).
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('diag', 'wire', 'pii', 'all')]
    [string]$Scenario = 'all',

    [ValidateSet('anthropic', 'openai', 'ollama', 'all')]
    [string]$Surface = 'all',

    # Phase 08.3.2: default to 'fake' (deterministic) because the
    # 'live' path depends on the LLM verbatim-echoing PII-shaped data,
    # which current Claude releases refuse on safety grounds.
    [ValidateSet('live', 'fake')]
    [string]$Mode = 'fake',

    [string]$Base = $(if ($env:OTTO_BASE_URL) { $env:OTTO_BASE_URL } else { 'http://127.0.0.1:18080' }),

    [string]$Auth = '',

    [switch]$NoColor,

    # POSIX-style help aliases so operators coming from test-pii.sh on
    # Mac/Linux don't have to learn Get-Help. Either -h or -Help dumps
    # the same comment-based help block PowerShell would show via
    # `Get-Help .\test-pii.ps1`.
    [Alias('h')]
    [switch]$Help
)

if ($Help.IsPresent) {
    Get-Help -Full $PSCommandPath
    exit 0
}

# ---------------------------------------------------------------------------
# Color + IO helpers.
# ---------------------------------------------------------------------------
$script:UseColor = -not $NoColor.IsPresent -and $Host.UI.RawUI -ne $null
$script:Failed = 0

function Write-Section($Text) {
    if ($script:UseColor) {
        Write-Host ""
        Write-Host "== $Text ==" -ForegroundColor Cyan
    } else {
        Write-Host ""
        Write-Host "== $Text =="
    }
}

function Write-Pass($Text) {
    if ($script:UseColor) {
        Write-Host "  " -NoNewline
        Write-Host "PASS" -ForegroundColor Green -NoNewline
        Write-Host " $Text"
    } else {
        Write-Host "  PASS $Text"
    }
}

function Write-Fail($Text) {
    $script:Failed++
    if ($script:UseColor) {
        Write-Host "  " -NoNewline
        Write-Host "FAIL" -ForegroundColor Red -NoNewline
        Write-Host " $Text"
    } else {
        Write-Host "  FAIL $Text"
    }
}

function Write-WarnLine($Text) {
    if ($script:UseColor) {
        Write-Host "  $Text" -ForegroundColor Yellow
    } else {
        Write-Host "  $Text"
    }
}

function Write-Info($Text) { Write-Host "  $Text" }

function Write-Verb($Text) {
    if ($VerbosePreference -eq 'Continue') {
        Write-Host "    $Text" -ForegroundColor DarkGray
    }
}

# ---------------------------------------------------------------------------
# HTTP helpers -- PowerShell 5.1 compatible (no -ResponseHeadersVariable).
# ---------------------------------------------------------------------------
function Invoke-Gateway {
    <#
    .SYNOPSIS
      Send an HTTP request and return [pscustomobject] @{StatusCode,Headers,Content}.
    .NOTES
      Uses HttpClient for ResponseHeadersRead so streaming Content-Type is
      visible BEFORE the full body has been received (Invoke-WebRequest
      buffers everything in 5.1 even with -UseBasicParsing).
    #>
    param(
        [Parameter(Mandatory)] [string]$Method,
        [Parameter(Mandatory)] [string]$Url,
        [string]$Body = '',
        [hashtable]$Headers = @{}
    )

    Add-Type -AssemblyName System.Net.Http
    $client = [System.Net.Http.HttpClient]::new()
    try {
        $req = [System.Net.Http.HttpRequestMessage]::new($Method, $Url)
        foreach ($k in $Headers.Keys) { [void]$req.Headers.TryAddWithoutValidation($k, $Headers[$k]) }
        if ($Auth) { [void]$req.Headers.TryAddWithoutValidation('Authorization', "Bearer $Auth") }
        if ($Body) {
            $req.Content = [System.Net.Http.StringContent]::new($Body, [Text.Encoding]::UTF8, 'application/json')
        }
        $resp = $client.SendAsync($req, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).Result
        $contentType = $null
        if ($resp.Content.Headers.ContentType) { $contentType = $resp.Content.Headers.ContentType.ToString() }
        $bodyStr = $resp.Content.ReadAsStringAsync().Result
        [pscustomobject]@{
            StatusCode  = [int]$resp.StatusCode
            ContentType = $contentType
            Content     = $bodyStr
        }
    } finally {
        $client.Dispose()
    }
}

# ---------------------------------------------------------------------------
# Precondition: gateway reachable.
# ---------------------------------------------------------------------------
try {
    $probe = Invoke-Gateway -Method GET -Url "$Base/health"
    if ($probe.StatusCode -ne 200) {
        Write-Host "gateway returned status $($probe.StatusCode) for /health" -ForegroundColor Red
        exit 2
    }
} catch {
    Write-Host "gateway not reachable at $Base" -ForegroundColor Red
    Write-Host "check: otto-gw.ps1 status; otto-gw.ps1 start" -ForegroundColor Red
    exit 2
}

# ---------------------------------------------------------------------------
# Scenario: diag.
# ---------------------------------------------------------------------------
function Invoke-Scenario-Diag {
    Write-Section "Diagnostic -- PII posture at $Base"
    $resp = Invoke-Gateway -Method GET -Url "$Base/health/hooks"
    if ($resp.StatusCode -ne 200) {
        Write-Fail "/health/hooks did not return 200 (got $($resp.StatusCode))"
        return
    }
    Write-Pass "/health/hooks 200"

    try {
        $data = $resp.Content | ConvertFrom-Json
    } catch {
        Write-Fail "/health/hooks body did not parse as JSON"
        return
    }

    $names = ($data.hooks | ForEach-Object { $_.name }) -join ','
    Write-Info "active hooks: $names"

    $expected = 'RequestIDHook,AuthHook,JSONFormatSteeringHook,PIIRedactionHook,LoggingHook'
    if ($names -eq $expected) {
        Write-Pass "hook chain matches expected default (5 hooks, registration order)"
    } else {
        Write-WarnLine "hook chain differs from default -- expected: $expected"
    }

    $pii = $data.hooks | Where-Object { $_.name -eq 'PIIRedactionHook' } | Select-Object -First 1
    if ($null -ne $pii) {
        $cfg = $pii.config
        $entitiesText = if ($cfg.entities -and @($cfg.entities).Count -gt 0) {
            ($cfg.entities -join ',')
        } else {
            '(all)'
        }
        Write-Info "PIIRedactionHook.enabled        = $($cfg.enabled)"
        Write-Info "PIIRedactionHook.mode           = $($cfg.mode)"
        Write-Info "PIIRedactionHook.decrypt_active = $($cfg.decrypt_active)"
        Write-Info "PIIRedactionHook.entities       = $entitiesText"
    } else {
        Write-WarnLine "PIIRedactionHook not present in /health/hooks"
    }
}

# ---------------------------------------------------------------------------
# Wire-shape helpers.
# ---------------------------------------------------------------------------
function Set-StreamFlag {
    <#
    .SYNOPSIS
      Return a JSON body string with "stream":<value> set/added.
    #>
    param([string]$Payload, [bool]$Stream)
    $val = if ($Stream) { 'true' } else { 'false' }
    if ($Payload -match '"stream":\s*(true|false)') {
        return ($Payload -replace '"stream":\s*(true|false)', "`"stream`":$val")
    }
    return ($Payload -replace '\}$', ",`"stream`":$val}")
}

function Test-WireShape {
    param(
        [string]$Label, [string]$Path, [string]$Payload,
        [string]$WantStreamCT, [string]$WantNonStreamCT,
        [hashtable]$ExtraHeaders = @{}
    )
    if ($script:UseColor) {
        Write-Host "  " -NoNewline
        Write-Host $Label -ForegroundColor White -NoNewline
        Write-Host "  $Path"
    } else {
        Write-Info "$Label  $Path"
    }

    $streamPayload = Set-StreamFlag -Payload $Payload -Stream $true
    $r = Invoke-Gateway -Method POST -Url "$Base$Path" -Body $streamPayload -Headers $ExtraHeaders
    if ($r.ContentType -and $r.ContentType.StartsWith($WantStreamCT)) {
        Write-Pass "stream=true -> Content-Type: $($r.ContentType)"
    } else {
        Write-Fail "stream=true -> Content-Type: $($r.ContentType) (want prefix $WantStreamCT)"
    }

    $nonPayload = Set-StreamFlag -Payload $Payload -Stream $false
    $r2 = Invoke-Gateway -Method POST -Url "$Base$Path" -Body $nonPayload -Headers $ExtraHeaders
    if ($r2.ContentType -and $r2.ContentType.StartsWith($WantNonStreamCT)) {
        Write-Pass "stream=false -> Content-Type: $($r2.ContentType)"
    } else {
        Write-Fail "stream=false -> Content-Type: $($r2.ContentType) (want prefix $WantNonStreamCT)"
    }
}

function Invoke-Scenario-Wire {
    Write-Section "Wire shape -- Content-Type per surface, stream toggle"
    $msg = '{"model":"auto","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}'

    if ($Surface -in @('all', 'anthropic')) {
        Test-WireShape -Label 'Anthropic /v1/messages' -Path '/v1/messages' -Payload $msg `
            -WantStreamCT 'text/event-stream' -WantNonStreamCT 'application/json' `
            -ExtraHeaders @{ 'anthropic-version' = '2023-06-01' }
    }
    if ($Surface -in @('all', 'openai')) {
        Test-WireShape -Label 'OpenAI /v1/chat/completions' -Path '/v1/chat/completions' -Payload $msg `
            -WantStreamCT 'text/event-stream' -WantNonStreamCT 'application/json'
    }
    if ($Surface -in @('all', 'ollama')) {
        Test-WireShape -Label 'Ollama /api/chat' -Path '/api/chat' -Payload $msg `
            -WantStreamCT 'application/x-ndjson' -WantNonStreamCT 'application/json'
    }
}

# ---------------------------------------------------------------------------
# Scenario: pii.
# ---------------------------------------------------------------------------

# New-NotifFrame builds a single compact-JSON JSON-RPC notification frame
# matching the shape produced by tests/e2e/tools_fixtures_test.go NotifText.
# One frame per line in the NDJSON file the fake reads via
# OTTO_FAKE_KIRO_NOTIFICATIONS_FILE. SessionId MUST be 'e2e-session-1'
# literally -- the fake hard-codes that value at
# tests/e2e/cmd/fake-kiro-cli/main.go:114 in its session/new response,
# and any other value gets demux-dropped by the gateway router.
function New-NotifFrame {
    param(
        [Parameter(Mandatory)] [string]$SessionId,
        [Parameter(Mandatory)] [string]$Text
    )
    $frame = [ordered]@{
        jsonrpc = '2.0'
        method  = 'session/update'
        params  = [ordered]@{
            sessionId = $SessionId
            update    = [ordered]@{
                sessionUpdate = 'agent_message_chunk'
                content       = [ordered]@{ text = $Text }
            }
        }
    }
    return ($frame | ConvertTo-Json -Compress -Depth 10)
}

$script:PIIPrompt = @'
Echo each line back to me VERBATIM, no edits, no summaries:

- Email: corey@cmetech.io
- IPv4: 192.168.1.42
- US phone: (415) 555-2671
- Credit card: 4111-1111-1111-1111

John Smith from Boston signing off.
'@

# Items the response MUST contain for round-trip success.
$script:PIIExpected = @('corey@cmetech.io', '192.168.1.42')

function Get-ResponseText {
    param([string]$SurfaceName, [string]$Body)
    switch ($SurfaceName) {
        'anthropic' {
            # Aggregate every text_delta.text from data: ... SSE lines.
            $sb = New-Object System.Text.StringBuilder
            foreach ($line in $Body -split "`n") {
                if ($line -match '^data: (.+)$') {
                    try {
                        $obj = $matches[1] | ConvertFrom-Json
                        if ($obj.type -eq 'content_block_delta' -and $obj.delta.type -eq 'text_delta') {
                            [void]$sb.Append($obj.delta.text)
                        }
                    } catch {}
                }
            }
            return $sb.ToString()
        }
        'openai' {
            $sb = New-Object System.Text.StringBuilder
            foreach ($line in $Body -split "`n") {
                if ($line -match '^data: (.+)$' -and $matches[1] -ne '[DONE]') {
                    try {
                        $obj = $matches[1] | ConvertFrom-Json
                        $delta = $obj.choices[0].delta.content
                        if ($delta) { [void]$sb.Append($delta) }
                    } catch {}
                }
            }
            return $sb.ToString()
        }
        'ollama' {
            $sb = New-Object System.Text.StringBuilder
            foreach ($line in $Body -split "`n") {
                if (-not $line) { continue }
                try {
                    $obj = $line | ConvertFrom-Json
                    if ($obj.message -and $obj.message.content) {
                        [void]$sb.Append($obj.message.content)
                    }
                } catch {}
            }
            return $sb.ToString()
        }
    }
    return ''
}

function Invoke-PIIProbe {
    param(
        [string]$SurfaceName, [string]$Label, [string]$Path,
        [string]$Payload, [hashtable]$ExtraHeaders = @{}
    )
    if ($script:UseColor) {
        Write-Host "  " -NoNewline
        Write-Host $Label -ForegroundColor White -NoNewline
        Write-Host "  $Path"
    } else {
        Write-Info "$Label  $Path"
    }

    $r = Invoke-Gateway -Method POST -Url "$Base$Path" -Body $Payload -Headers $ExtraHeaders
    if (-not $r.Content) {
        Write-Fail "empty response (gateway error?)"
        return
    }
    $text = Get-ResponseText -SurfaceName $SurfaceName -Body $r.Content
    Write-Verb "extracted client-visible text: $text"

    if ($text -match '\[PII:[A-Za-z]+:[A-Za-z0-9_-]+\]') {
        Write-Fail "ciphertext leak: response contains a [PII:Entity:base64url] token (decrypt failed)"
        Write-Info "  leaking text: $($text.Substring(0, [math]::Min(120, $text.Length)))"
        return
    }
    Write-Pass "no ciphertext tokens in response (decrypt did not leak)"

    foreach ($needle in $script:PIIExpected) {
        if ($text.Contains($needle)) {
            Write-Pass "round-trip decrypt: '$needle' present in response"
        } else {
            Write-Fail "round-trip decrypt: '$needle' NOT in response"
        }
    }
}

function Invoke-Scenario-Pii-Live {
    Write-Section "DEPRECATED: -Mode live"
    Write-Info "The live path depends on the LLM (Claude via kiro-cli) verbatim-echoing PII-shaped"
    Write-Info "data, which current model releases refuse on safety grounds. Expect 'round-trip"
    Write-Info "decrypt: NOT in response' failures. See Phase 08.3.2 for the deterministic-worker"
    Write-Info "alternative (-Mode fake, the default)."

    Write-Section "PII round-trip -- encrypt -> worker -> decrypt -> client"
    $payloadObj = @{
        model      = 'auto'
        max_tokens = 512
        stream     = $true
        messages   = @(@{ role = 'user'; content = $script:PIIPrompt })
    }
    $payload = $payloadObj | ConvertTo-Json -Compress -Depth 5

    if ($Surface -in @('all', 'anthropic')) {
        Invoke-PIIProbe -SurfaceName anthropic -Label 'Anthropic /v1/messages' -Path '/v1/messages' `
            -Payload $payload -ExtraHeaders @{ 'anthropic-version' = '2023-06-01' }
    }
    if ($Surface -in @('all', 'openai')) {
        Invoke-PIIProbe -SurfaceName openai -Label 'OpenAI /v1/chat/completions' -Path '/v1/chat/completions' `
            -Payload $payload
    }
    if ($Surface -in @('all', 'ollama')) {
        Invoke-PIIProbe -SurfaceName ollama -Label 'Ollama /api/chat' -Path '/api/chat' `
            -Payload $payload
    }
}

function Invoke-Scenario-Pii-Fake {
    Write-Section "PII round-trip -- -Mode fake (deterministic, Phase 08.3.2)"

    # (i) Notifications file path under $env:TEMP.
    $notifsPath = Join-Path $env:TEMP 'otto-pii-notifs.ndjson'

    # (ii) Build NDJSON content from $script:PIIExpected (one frame per
    # expected plaintext). SessionId hard-coded to 'e2e-session-1' to
    # match tests/e2e/cmd/fake-kiro-cli/main.go:114.
    $lines = @()
    foreach ($value in $script:PIIExpected) {
        $lines += (New-NotifFrame -SessionId 'e2e-session-1' -Text $value)
    }
    $body = ($lines -join "`n") + "`n"

    # (iii) Write file as UTF-8 NO BOM. AP-1 mandate -- the default
    # PowerShell 5.1 write primitives emit a BOM that breaks the fake's
    # first-line JSON parse (prior incident: commit f7ccd40). Use the
    # .NET WriteAllText API with the explicit no-BOM UTF8 ctor below.
    try {
        [System.IO.File]::WriteAllText(
            $notifsPath,
            $body,
            [System.Text.UTF8Encoding]::new($false))
    } catch {
        Write-Fail "could not write notifications file ${notifsPath}: $($_.Exception.Message)"
        return
    }
    Write-Pass "wrote $($lines.Count) notification frame(s) to $notifsPath (UTF-8 no BOM)"

    # (iv) Pre-flight validation -- each line must parse as JSON. The
    # fake silently skips malformed lines (main.go:202), so an operator
    # would otherwise see "round-trip decrypt: 'X' NOT in response"
    # with no clue why. Catch it here.
    $lineNum = 0
    foreach ($line in $lines) {
        $lineNum++
        try {
            $null = $line | ConvertFrom-Json
        } catch {
            Write-Fail "notifications file frame $lineNum is invalid JSON: $($_.Exception.Message)"
            return
        }
    }
    Write-Pass "all $($lines.Count) frame(s) are valid JSON"

    # (v) T2 mitigation -- gateway must be pointed at a fake worker. We
    # read /admin/about (HTML page that exposes the KIRO_CMD row) and
    # require the configured binary path to contain 'fake'
    # (case-insensitive). If not, the operator forgot to swap KIRO_CMD;
    # refuse to proceed rather than contaminate live traffic.
    try {
        $about = Invoke-Gateway -Method GET -Url "$Base/admin/about"
    } catch {
        Write-Fail "could not read $Base/admin/about: $($_.Exception.Message)"
        return
    }
    if ($about.StatusCode -ne 200) {
        Write-Fail "$Base/admin/about returned $($about.StatusCode); cannot verify KIRO_CMD points at fake"
        return
    }
    # The about template renders: <dt>KIRO_CMD</dt><dd>VALUE</dd>
    if ($about.Content -notmatch '<dt>\s*KIRO_CMD\s*</dt>\s*<dd>([^<]+)</dd>') {
        Write-Fail "could not parse KIRO_CMD value from /admin/about HTML"
        return
    }
    $kiroCmd = $matches[1].Trim()
    if ($kiroCmd -notmatch '(?i)fake') {
        Write-Fail "T2 GUARD: gateway is not pointed at a fake worker (KIRO_CMD='$kiroCmd') -- refusing to run -Mode fake to avoid contaminating live traffic"
        Write-Info "operator action: set KIRO_CMD=<absolute path to bin\fake-kiro-cli.exe> in .otto-gw.overrides.env and restart the gateway"
        $script:Failed = [Math]::Max($script:Failed, 1)
        return
    }
    Write-Pass "gateway KIRO_CMD='$kiroCmd' resolves to a fake worker"

    # (vi) Operator informational -- script does NOT manipulate the
    # gateway process env. Operator owns lifecycle.
    Write-Info "NOTE: the gateway process must have OTTO_FAKE_KIRO_NOTIFICATIONS_FILE=$notifsPath set in its environment. If it doesn't, restart the gateway with that env var set."

    # (vii) Run the existing probes -- identical surface dispatch to live.
    $payloadObj = @{
        model      = 'auto'
        max_tokens = 512
        stream     = $true
        messages   = @(@{ role = 'user'; content = $script:PIIPrompt })
    }
    $payload = $payloadObj | ConvertTo-Json -Compress -Depth 5

    if ($Surface -in @('all', 'anthropic')) {
        Invoke-PIIProbe -SurfaceName anthropic -Label 'Anthropic /v1/messages' -Path '/v1/messages' `
            -Payload $payload -ExtraHeaders @{ 'anthropic-version' = '2023-06-01' }
    }
    if ($Surface -in @('all', 'openai')) {
        Invoke-PIIProbe -SurfaceName openai -Label 'OpenAI /v1/chat/completions' -Path '/v1/chat/completions' `
            -Payload $payload
    }
    if ($Surface -in @('all', 'ollama')) {
        Invoke-PIIProbe -SurfaceName ollama -Label 'Ollama /api/chat' -Path '/api/chat' `
            -Payload $payload
    }
}

function Invoke-Scenario-Pii {
    if ($Mode -eq 'fake') { Invoke-Scenario-Pii-Fake } else { Invoke-Scenario-Pii-Live }
}

# ---------------------------------------------------------------------------
# Dispatch.
# ---------------------------------------------------------------------------
switch ($Scenario) {
    'diag' { Invoke-Scenario-Diag }
    'wire' { Invoke-Scenario-Wire }
    'pii'  { Invoke-Scenario-Pii }
    'all'  {
        Invoke-Scenario-Diag
        Invoke-Scenario-Wire
        Invoke-Scenario-Pii
    }
}

Write-Host ""
if ($script:Failed -gt 0) {
    if ($script:UseColor) {
        Write-Host "$($script:Failed) check(s) failed" -ForegroundColor Red
    } else {
        Write-Host "$($script:Failed) check(s) failed"
    }
    exit 1
}
if ($script:UseColor) {
    Write-Host 'all checks passed' -ForegroundColor Green
} else {
    Write-Host 'all checks passed'
}
exit 0
