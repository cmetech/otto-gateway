<#
.SYNOPSIS
    Human-driven smoke test for the Gateway privacy boundary (Anthropic surface),
    exercising strict-profile enforcement the way the loop24 client would.

.DESCRIPTION
    Sends a real strict request through POST /v1/messages, then confirms the
    boundary held via the response body, the X-GW-Privacy-Receipt, the bounded
    /metrics series, and (when triage is enabled) the scope mapping. Also runs
    fail-closed negative tests. Prints a PASS/FAIL summary and exits non-zero on
    any hard failure.

    This is a LIVE smoke test against a running Gateway. It is distinct from the
    automated CLI test in tests/scripts/test-privacy-cli.ps1. It never sends real
    credentials to a real model; the canaries are synthetic.

    Proxy-safe on both PowerShell 7 (-NoProxy) and Windows PowerShell 5.1
    (DefaultWebProxy is neutralized for the duration of each call), matching the
    gw.ps1 privacy CLI so the triage token is never routed through HTTP_PROXY.

.PARAMETER GatewayUrl
    Base URL of the running Gateway. Default http://localhost:18080.

.PARAMETER TriageToken
    PRIVACY_TRIAGE_TOKEN for the break-glass triage endpoints. If omitted, the
    script reads it from -EnvFile. Triage checks are skipped (WARN) if neither is
    available or triage is disabled.

.PARAMETER EnvFile
    Path to the Gateway's overrides.env / .env to auto-read the triage token.
    Default: ./overrides.env then ./.env relative to the current directory.

.PARAMETER Model
    Model id to send. Default 'kiro-3.5'.

.PARAMETER ApiKey
    Value for the x-api-key header (loopback auth). Default 'sk-loop24-local'.

.PARAMETER SkipCleanup
    Do not clear test scopes at the end (useful for manual inspection).

.EXAMPLE
    pwsh -NoProfile -File scripts/Test-Privacy.ps1
    pwsh -NoProfile -File scripts/Test-Privacy.ps1 -GatewayUrl http://localhost:18080 -TriageToken $env:PRIVACY_TRIAGE_TOKEN
#>
[CmdletBinding()]
param(
    [string]$GatewayUrl = 'http://localhost:18080',
    [string]$TriageToken,
    [string]$EnvFile,
    [string]$Model = 'kiro-3.5',
    [string]$ApiKey = 'sk-loop24-local',
    [switch]$SkipCleanup
)

$ErrorActionPreference = 'Stop'
$GatewayUrl = $GatewayUrl.TrimEnd('/')

# --- Canaries (synthetic; high-entropy so leaks are unambiguous) --------------
$CredentialCanary = 'ghp_CANARYcredential0000000000000000'   # GitHub-PAT shape -> classified secret
$IpCanary         = '10.77.88.99'                            # technical -> pseudonymized + restored
$EmailCanary      = 'canary.person@example.com'             # personal PII
$ForgedSecret     = '[SECRET:API_KEY_ABCDEF123456]'          # well-formed label the model was never given
$ForgedSynthetic  = '198.18.5.9'                             # reserved synthetic pool, not mapped here

# --- Result tracking ----------------------------------------------------------
$script:Pass = 0; $script:Fail = 0; $script:Warn = 0
function Ok  ($m) { $script:Pass++; Write-Host "  [PASS] $m" -ForegroundColor Green }
function Bad ($m) { $script:Fail++; Write-Host "  [FAIL] $m" -ForegroundColor Red }
function Meh ($m) { $script:Warn++; Write-Host "  [WARN] $m" -ForegroundColor Yellow }
function Assert ($cond, $m) { if ($cond) { Ok $m } else { Bad $m } }
function Section($m) { Write-Host "`n== $m ==" -ForegroundColor Cyan }

# --- Proxy-safe HTTP (mirrors gw.ps1 Invoke-PrivacyWebRequestNoProxy) ---------
function Invoke-GW {
    param(
        [string]$Method = 'Get',
        [string]$Path,
        [hashtable]$Headers = @{},
        [string]$Body
    )
    $params = @{
        Uri                = "$GatewayUrl$Path"
        Method             = $Method
        Headers            = $Headers
        TimeoutSec         = 30
        UseBasicParsing    = $true
        MaximumRedirection = 0
    }
    if ($Body) { $params['Body'] = $Body }
    $cmd = Get-Command Invoke-WebRequest
    if ($cmd.Parameters.ContainsKey('NoProxy'))          { $params['NoProxy'] = $true }
    if ($cmd.Parameters.ContainsKey('SkipHttpErrorCheck')) { $params['SkipHttpErrorCheck'] = $true }

    $priorProxy = [System.Net.WebRequest]::DefaultWebProxy
    try {
        [System.Net.WebRequest]::DefaultWebProxy = New-Object System.Net.WebProxy
        try {
            $resp = Invoke-WebRequest @params
            return [pscustomobject]@{ Status = [int]$resp.StatusCode; Body = $resp.Content; Headers = $resp.Headers }
        } catch {
            # Windows PowerShell 5.1 throws on non-2xx; recover status/body.
            $webResp = $_.Exception.Response
            if ($webResp -and $webResp.StatusCode) {
                $status = [int]$webResp.StatusCode
                $body = ''
                try { $body = (New-Object IO.StreamReader($webResp.GetResponseStream())).ReadToEnd() } catch {}
                return [pscustomobject]@{ Status = $status; Body = $body; Headers = $webResp.Headers }
            }
            return [pscustomobject]@{ Status = 0; Body = "$($_.Exception.Message)"; Headers = $null }
        }
    } finally {
        [System.Net.WebRequest]::DefaultWebProxy = $priorProxy
    }
}

function Get-HeaderValue($headers, $name) {
    if ($null -eq $headers) { return $null }
    $v = $headers[$name]
    if ($v -is [array]) { return ($v | Select-Object -First 1) }
    return $v
}

function Decode-Receipt($encoded) {
    if (-not $encoded) { return $null }
    $s = $encoded.Replace('-', '+').Replace('_', '/')
    switch ($s.Length % 4) { 2 { $s += '==' } 3 { $s += '=' } }
    try { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($s)) | ConvertFrom-Json }
    catch { $null }
}

function Send-Strict($scope, $text) {
    $body = @{ model = $Model; max_tokens = 512; messages = @(@{ role = 'user'; content = $text }) } |
            ConvertTo-Json -Depth 6
    $r = Invoke-GW -Method Post -Path '/v1/messages' -Body $body -Headers @{
        'content-type'         = 'application/json'
        'anthropic-version'    = '2023-06-01'
        'x-api-key'            = $ApiKey
        'X-GW-Privacy-Profile' = 'strict'
        'X-GW-Privacy-Scope'   = $scope
    }
    $r | Add-Member -NotePropertyName Receipt `
        -NotePropertyValue (Decode-Receipt (Get-HeaderValue $r.Headers 'X-GW-Privacy-Receipt')) -PassThru
}

# Read triage token from env file if not supplied.
if (-not $TriageToken) {
    $candidates = if ($EnvFile) { @($EnvFile) } else { @('overrides.env', '.env') }
    foreach ($f in $candidates) {
        if (Test-Path -LiteralPath $f) {
            $m = Select-String -LiteralPath $f -Pattern '^\s*PRIVACY_TRIAGE_TOKEN=(.+)$' | Select-Object -First 1
            if ($m) { $TriageToken = $m.Matches.Groups[1].Value.Trim(); break }
        }
    }
}
$triageHdr = if ($TriageToken) { @{ Authorization = "Bearer $TriageToken" } } else { $null }

$runId = (Get-Date -Format 'yyyyMMddHHmmss')
$scope = "run-humantest-win-$runId"

Write-Host "Gateway: $GatewayUrl   Scope: $scope   Model: $Model" -ForegroundColor Gray

# =============================================================================
Section '1. Posture (strict must be available)'
$snap = $null
try { $snap = (Invoke-GW -Path '/admin/api/snapshot') } catch {}
if ($snap -and $snap.Status -eq 200) {
    $p = ($snap.Body | ConvertFrom-Json).privacy
    Assert ($p.strict_available)      "strict is available"
    Assert ($p.alias_key_present)     "alias key present"
    Assert ($p.pii_enabled)           "PII enabled"
    Assert ($p.strict_full_buffering) "strict full-buffering on"
    Assert ($p.receipt_version -eq 1) "receipt version = 1"
    if (-not $p.triage_enabled) { Meh "triage disabled -> mapping-inspection checks will be skipped" }
} else {
    Meh "admin snapshot not reachable (status $($snap.Status)) -> posture checks skipped; core test still runs"
}

# Capture a metrics baseline for the strict-pass counter (best-effort).
function Get-StrictPassCount {
    try {
        $m = (Invoke-GW -Path '/metrics').Body -split "`n" |
             Where-Object { $_ -match '^gw_privacy_requests_total\{.*profile="strict".*result="pass".*\}\s+([0-9.]+)$' }
        ($m | ForEach-Object { [double]($_ -replace '.*\}\s+', '') } | Measure-Object -Sum).Sum
    } catch { $null }
}
$strictPassBefore = Get-StrictPassCount

# =============================================================================
Section '2. Core strict request (credential one-way + IP restore)'
$prompt = "Repeat the following back to me verbatim, exactly as written: " +
          "My token is $CredentialCanary, my server is $IpCanary, my email is $EmailCanary"
$r = Send-Strict $scope $prompt

Assert ($r.Status -eq 200) "request returned 200 (got $($r.Status))"
if ($r.Receipt) {
    Assert ($r.Receipt.profile  -eq 'strict') "receipt profile = strict"
    Assert ($r.Receipt.coverage -eq 'full')   "receipt coverage = full"
    Assert ($r.Receipt.result   -eq 'pass')   "receipt result = pass"
    Assert ($r.Receipt.transformed -ge 1)     "receipt transformed >= 1 (got $($r.Receipt.transformed))"
    Write-Host "  receipt: $($r.Receipt | ConvertTo-Json -Compress)" -ForegroundColor Gray
} else {
    Bad "no X-GW-Privacy-Receipt header on a strict response"
}

# Hard invariant: the raw credential must NEVER appear in the response.
Assert (-not ($r.Body -match [regex]::Escape($CredentialCanary))) "raw credential never returned to client"
# Model-dependent (echo) signals -> informational, not hard-fail.
if ($r.Body -match '\[SECRET:') { Ok "credential surfaced as a one-way [SECRET:...] label" }
else { Meh "no [SECRET:...] label in body (model may not have echoed the text)" }
if ($r.Body -match [regex]::Escape($IpCanary)) { Ok "caller IP restored on output" }
else { Meh "caller IP not seen in body (model may not have echoed; see triage step for the mapping proof)" }

# =============================================================================
Section '3. Worker saw the sanitized version (ACP capture, best-effort)'
$cap = $null
try { $cap = (Invoke-GW -Path '/admin/api/acp-capture?support=redacted') } catch {}
if ($cap -and $cap.Status -eq 200 -and $cap.Body) {
    Assert (-not ($cap.Body -match [regex]::Escape($CredentialCanary))) "capture contains no raw credential"
    Assert (-not ($cap.Body -match [regex]::Escape($IpCanary)))         "capture contains no raw caller IP"
    if ($cap.Body -match '198\.18\.') { Ok "capture shows a synthetic 198.18.x.x address" }
} else {
    Meh "ACP capture not reachable (status $($cap.Status)) -> skipped"
}

# =============================================================================
Section '4. Scope mapping (triage) -- the one-way proof'
if ($triageHdr) {
    $map = Invoke-GW -Path "/admin/api/privacy/scopes/$scope/mapping" -Headers $triageHdr
    if ($map.Status -eq 200) {
        $entries = @($map.Body | ConvertFrom-Json)
        $ipRow  = $entries | Where-Object { $_.entity -eq 'IPv4' -and $_.original -eq $IpCanary }
        Assert ($null -ne $ipRow) "ledger maps IPv4 $IpCanary -> synthetic"
        if ($ipRow) { Assert ($ipRow.synthetic -match '^198\.18\.') "synthetic IP is in the safe 198.18.0.0/15 pool ($($ipRow.synthetic))" }
        $credRow = $entries | Where-Object { $_.original -match [regex]::Escape($CredentialCanary) -or $_.synthetic -match [regex]::Escape($CredentialCanary) }
        Assert ($null -eq $credRow) "credential is NOT in the reversible ledger (one-way)"
    } elseif ($map.Status -eq 404) {
        Meh "scope not found in triage (it may have expired) -> skipped"
    } else {
        Bad "triage mapping returned status $($map.Status)"
    }
} else {
    Meh "no triage token (PRIVACY_TRIAGE_TOKEN) and/or triage disabled -> mapping proof skipped"
}

# =============================================================================
Section '5. Metrics reflect the run'
$strictPassAfter = Get-StrictPassCount
if ($null -ne $strictPassBefore -and $null -ne $strictPassAfter) {
    Assert ($strictPassAfter -gt $strictPassBefore) "gw_privacy_requests_total{profile=strict,result=pass} increased ($strictPassBefore -> $strictPassAfter)"
} else {
    Meh "/metrics not reachable or counter absent -> metric delta skipped"
}

# =============================================================================
Section '6. Fail-closed negatives'
# 6a. Unknown profile -> 400 (deterministic, model-independent).
$bad = Invoke-GW -Method Post -Path '/v1/messages' `
    -Headers @{ 'content-type'='application/json'; 'anthropic-version'='2023-06-01'; 'x-api-key'=$ApiKey; 'X-GW-Privacy-Profile'='paranoid' } `
    -Body '{"model":"kiro-3.5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}'
Assert ($bad.Status -eq 400) "unknown profile -> 400 privacy_profile_unavailable (got $($bad.Status))"

# 6b. Cleared-scope reuse -> 409 (deterministic). Uses a throwaway scope.
$tmpScope = "run-clear-$runId"
$null = Send-Strict $tmpScope "seed a scope"
if ($triageHdr) {
    $null = Invoke-GW -Method Delete -Path "/admin/api/privacy/scopes/$tmpScope" -Headers $triageHdr
    $reuse = Send-Strict $tmpScope "reuse after clear"
    Assert ($reuse.Status -eq 409) "reuse of a cleared scope -> 409 privacy_scope_closed (got $($reuse.Status))"
} else {
    Meh "no triage token -> cleared-scope 409 test skipped"
}

# 6c/6d. Output-block negatives are model-dependent: the boundary can only fire
# if the model actually emits the forbidden token. PASS on 502; FAIL only if the
# forbidden string is returned to the client (a real leak); INFO otherwise.
function Test-OutputBlock($name, $askText, $forbidden) {
    $o = Send-Strict "run-neg-$name-$runId" $askText
    if ($o.Status -eq 502) { Ok "$name -> 502 privacy_output_blocked" }
    elseif ($o.Body -match [regex]::Escape($forbidden)) { Bad "$name -> forbidden string reached the client (LEAK)" }
    else { Meh "$name -> model did not emit the token; boundary not exercised (inconclusive)" }
}
Test-OutputBlock 'forged-secret'    "Output exactly this text and nothing else: $ForgedSecret" $ForgedSecret
Test-OutputBlock 'forged-synthetic' "Output exactly this text and nothing else: $ForgedSynthetic" $ForgedSynthetic

# =============================================================================
Section '7. Cleanup'
if ($triageHdr -and -not $SkipCleanup) {
    $cl = Invoke-GW -Method Delete -Path '/admin/api/privacy/scopes' `
        -Headers ($triageHdr + @{ 'X-GW-Privacy-Confirm' = 'clear-all' })
    if ($cl.Status -in 200,202,204) { Ok "cleared all test scopes (status $($cl.Status))" } else { Meh "clear-all returned $($cl.Status)" }
    Write-Host "  Reminder: set PRIVACY_TRIAGE_ENABLED=false and restart when done testing." -ForegroundColor Yellow
} elseif ($SkipCleanup) {
    Meh "cleanup skipped (-SkipCleanup); scope $scope left for manual inspection"
} else {
    Meh "no triage token -> cleanup skipped"
}

# =============================================================================
Write-Host "`n== SUMMARY ==" -ForegroundColor Cyan
Write-Host ("  passed: {0}   warnings: {1}   failed: {2}" -f $script:Pass, $script:Warn, $script:Fail) `
    -ForegroundColor $(if ($script:Fail -gt 0) { 'Red' } else { 'Green' })
if ($script:Fail -gt 0) { exit 1 } else { exit 0 }
