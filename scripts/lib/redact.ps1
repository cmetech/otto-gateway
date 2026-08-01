# scripts/lib/redact.ps1 — shared pwsh redaction primitives for the support
# bundle subcommand. Dot-sourced (not executed); requires PowerShell 5.1+
# (Compatible with Windows PowerShell 5.x AND PowerShell 7+).
#
# Surface (per docs/superpowers/specs/2026-06-08-support-bundle-design.md):
#   - Invoke-RedactStream     pipeline filter backed by Gateway's classifier
#   - Mask-EnvValue VALUE     returns "<first4>…(<N> chars)"
#   - Test-IsSecretKey KEY    returns $true if KEY names a secret env var
#
# Invoke-RedactStream buffers the bounded support artifact, sends it to the
# Gateway's hidden redact-support utility once, and emits output only after the
# classifier exits successfully. Callers publish from their own temporary file,
# so a missing or failed classifier cannot expose a partially redacted artifact.
function Invoke-RedactStream {
    [CmdletBinding()]
    param(
        [Parameter(ValueFromPipeline = $true)]
        [AllowNull()]
        [AllowEmptyString()]
        [string]$Line
    )
    begin {
        $records = New-Object System.Collections.Generic.List[string]
    }
    process {
        if ($null -ne $Line) { $records.Add($Line) }
    }
    end {
        $redactor = if ($env:GW_SUPPORT_REDACTOR_BIN) { $env:GW_SUPPORT_REDACTOR_BIN } elseif ($env:GW_BIN) { $env:GW_BIN } else { $null }
        if (-not $redactor -or -not (Test-Path -LiteralPath $redactor -PathType Leaf)) {
            throw 'support redactor unavailable'
        }
        $startInfo = New-Object System.Diagnostics.ProcessStartInfo
        $startInfo.FileName = $redactor
        $startInfo.Arguments = 'redact-support'
        $startInfo.UseShellExecute = $false
        $startInfo.RedirectStandardInput = $true
        $startInfo.RedirectStandardOutput = $true
        $startInfo.RedirectStandardError = $true
        $startInfo.CreateNoWindow = $true
        $process = New-Object System.Diagnostics.Process
        $process.StartInfo = $startInfo
        if (-not $process.Start()) { throw 'support redactor unavailable' }
        $inputText = $records -join [Environment]::NewLine
        $process.StandardInput.Write($inputText)
        $process.StandardInput.Close()
        $output = $process.StandardOutput.ReadToEnd()
        $null = $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        if ($process.ExitCode -ne 0) { throw 'support redaction failed' }
        if ($output.Length -gt 0) {
            $outputLines = @($output -split "\r?\n")
            $lineCount = $outputLines.Count
            if ($lineCount -gt 0 -and $outputLines[$lineCount - 1] -eq '' -and
                ($output.EndsWith("`n") -or $output.EndsWith("`r"))) {
                $lineCount--
            }
            for ($lineIndex = 0; $lineIndex -lt $lineCount; $lineIndex++) {
                $outputLines[$lineIndex]
            }
        }
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
    if ($up -in @('AUTH_TOKEN', 'PII_HASH_KEY', 'PII_ENCRYPT_KEY', 'GW_METRICS_REMOTE_WRITE_TOKEN')) { return $true }
    if ($up -match 'TOKEN|KEY|SECRET|PASSWORD|PASSPHRASE') { return $true }
    return $false
}
