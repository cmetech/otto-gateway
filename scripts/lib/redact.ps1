# scripts/lib/redact.ps1 — shared pwsh redaction primitives for the support
# bundle subcommand. Dot-sourced (not executed); requires PowerShell 5.1+
# (Compatible with Windows PowerShell 5.x AND PowerShell 7+).
#
# Surface (per docs/superpowers/specs/2026-06-08-support-bundle-design.md):
#   - Invoke-RedactStream     pipeline filter applying log-scrub rules
#   - Mask-EnvValue VALUE     returns "<first4>…(<N> chars)"
#   - Test-IsSecretKey KEY    returns $true if KEY names a secret env var
#
# Regex rules MUST be byte-equivalent with scripts/lib/redact.sh so behavior
# does not drift between OS wrappers.
#
# Log scrub rules (applied in order):
#   1. Authorization:<space>.*       -> Authorization: [REDACTED]
#   2. x-api-key:<space>.*           -> x-api-key: [REDACTED]   (case-insensitive)
#   3. Bearer <hex/url-safe-base64>  -> Bearer [REDACTED]
#   4. (^|[^A-Za-z0-9_])(AUTH_TOKEN|PII_HASH_KEY|PII_ENCRYPT_KEY)=<value>
#                                     -> KEY=[REDACTED]
#
# Rule (4) intentionally matches mid-line — slog log entries embed the
# pattern as `... msg=AUTH_TOKEN=...`. The leading negative-char-class
# guards against false-matching `MY_AUTH_TOKEN=foo`.
#
# Header rules run BEFORE the Bearer rule so `Authorization: Bearer foo`
# collapses to `Authorization: [REDACTED]` (no leftover "Bearer" token).

# Invoke-RedactStream — pipeline filter. Reads one line per pipeline element
# (the canonical Get-Content invocation streams line-by-line), applies the
# four log-scrub rules, emits the redacted line. Idempotent for the headers.
function Invoke-RedactStream {
    [CmdletBinding()]
    param(
        [Parameter(ValueFromPipeline = $true)]
        [AllowNull()]
        [AllowEmptyString()]
        [string]$Line
    )
    process {
        if ($null -eq $Line) { return }
        $out = $Line
        $out = $out -replace '(Authorization:\s*).*', '$1[REDACTED]'
        # (?i) inline regex flag makes the x-api-key match case-insensitive.
        $out = $out -replace '(?i)(x-api-key:\s*).*', '$1[REDACTED]'
        $out = $out -replace 'Bearer [A-Za-z0-9._\-]+', 'Bearer [REDACTED]'
        $out = $out -replace '(^|[^A-Za-z0-9_])(AUTH_TOKEN|PII_HASH_KEY|PII_ENCRYPT_KEY)=\S+', '$1$2=[REDACTED]'
        $out
    }
}

# Mask-EnvValue VALUE — returns "<first 4 chars>…(<N> chars)". Empty input
# returns empty string (caller decides whether that's an error). The literal
# value is never written.
function Mask-EnvValue {
    param([Parameter(Position = 0)][AllowEmptyString()][string]$Value = "")
    if ([string]::IsNullOrEmpty($Value)) { return "" }
    $prefixLen = [Math]::Min(4, $Value.Length)
    $prefix = $Value.Substring(0, $prefixLen)
    return "$prefix" + [char]0x2026 + "($($Value.Length) chars)"
}

# Test-IsSecretKey KEY — returns $true if KEY identifies a secret env var.
# Matches an explicit allowlist OR the substring patterns
# *TOKEN* / *KEY* / *SECRET* / *PASSWORD* / *PASSPHRASE* (case-insensitive).
function Test-IsSecretKey {
    param([Parameter(Position = 0)][AllowEmptyString()][string]$Key = "")
    if ([string]::IsNullOrEmpty($Key)) { return $false }
    $up = $Key.ToUpperInvariant()
    if ($up -in @('AUTH_TOKEN', 'PII_HASH_KEY', 'PII_ENCRYPT_KEY')) { return $true }
    if ($up -match 'TOKEN|KEY|SECRET|PASSWORD|PASSPHRASE') { return $true }
    return $false
}
