<#
.SYNOPSIS
    Quick PII / streaming smoke test for Gateway (Windows).

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

.PARAMETER Base
    Gateway base URL. Default: http://127.0.0.1:18080
    Also reads $env:GW_BASE_URL when present.

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

.NOTES
    Exit codes:
      0  all scenarios passed
      1  at least one scenario failed
      2  usage error or precondition not met (gateway unreachable, etc.)

    Requires PowerShell 5.1 or later (ships with Windows 10+).
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('diag', 'wire', 'pii', 'all')]
    [string]$Scenario = 'all',

    [ValidateSet('anthropic', 'openai', 'ollama', 'all')]
    [string]$Surface = 'all',

    [string]$Base = $(if ($env:GW_BASE_URL) { $env:GW_BASE_URL } else { 'http://127.0.0.1:18080' }),

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
    Write-Host "check: gw.ps1 status; gw.ps1 start" -ForegroundColor Red
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
$script:PIIPrompt = @'
Please help me draft a brief on-call handover note.

Customer profile:
- Customer name: John Smith
- City: Boston, Massachusetts
- Site address: 1111 Main Street, Austin, TX 27584
- Email on file: corey@cmetech.io
- Phone on file: (415) 555-2671
- Office GPS: 42.3601 N, 71.0589 W
- Affected source IPv4: 192.168.1.42
- Affected source IPv6: 2001:db8::1

For account verification only (do not reference these in the handover):
- SSN: 123-45-6789
- Card on file: 4111-1111-1111-1111

Write a short handover note for the on-call engineer that:
1. Greets the on-call engineer.
2. States the customer name (John Smith), city (Boston, Massachusetts), AND on-site address (1111 Main Street, Austin, TX 27584) so the on-call engineer knows who is affected AND where to dispatch a tech.
3. Lists both the IPv4 (192.168.1.42) and IPv6 (2001:db8::1) source addresses so the on-call engineer can grep logs on either stack.
4. Includes the office GPS 42.3601 N, 71.0589 W so the field team can route to the right office if a site visit is needed.
5. Provides the email (corey@cmetech.io) and phone ((415) 555-2671) for direct callback.
6. Ends with: "Customer is awaiting an update; please reach out directly."

Keep the handover under 150 words. Do not reference the SSN or the
card on file in your reply.
'@

# Items the response MUST contain for round-trip success.
# Mix of regex recognizers (Email, USPhone, COORDINATES, IPv4, IPv6) and
# NER recognizers (PERSON via 'John Smith', LOCATION via 'Boston').
# SSN + CreditCard are present in the prompt to exercise the redactor on
# the encrypt side but are NOT asserted on the response side -- the prompt
# explicitly tells the LLM not to repeat them, so the response should not
# include them (verified instead by the "no ciphertext leak" assertion
# below: if redaction missed them, the raw plaintext would appear and
# the ciphertext-leak check would not fire, but the cipher-side test
# already proves redaction works via TestPIIRedactionHook_NEREncryptRoundTrip).
$script:PIIExpected = @(
    'John Smith',          # NER PERSON
    'Boston',              # NER LOCATION (substring of 'Boston, Massachusetts' -- robust)
    'corey@cmetech.io',    # Email
    '(415) 555-2671',      # USPhone (prompt asks for this exact format)
    '42.3601',             # COORDINATES (substring of '42.3601 N, 71.0589 W' -- robust to formatting drift)
    '192.168.1.42',        # IPv4
    '2001:db8::1',         # IPv6
    # Phase 08.4 PII-01: US address coverage.
    '1111 Main Street',    # USAddress -- exact captured span
    'TX',                  # USState -- needle on raw code (recognizer match span includes leading ", ")
    '27584'                # USZIP -- validator accepts non-all-same-digit
)

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

    # Entity group is [A-Za-z0-9]+ (NOT just letters) so this catches
    # IPv4 / IPv6 / IMEI / IMSI / MSISDN tokens -- entity names that
    # contain digits. Matches the gateway-side decryptTokenRe at
    # internal/plugin/pii/pii.go which had the same letters-only bug
    # silently dropping these tokens; both fixed in v1.9.5.
    if ($text -match '\[PII:[A-Za-z0-9]+:[A-Za-z0-9_-]+\]') {
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

function Invoke-Scenario-Pii {
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
