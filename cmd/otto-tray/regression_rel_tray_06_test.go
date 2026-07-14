//go:build darwin || windows

package main

import "testing"

// TestRegression_REL_TRAY_06_WindowsBundlePathPollution is the
// discoverability stub for REL-TRAY-06. The failure mode
// (Initialize-Config's Write-Host lines polluting stdout before the
// bundle archive path, causing tray.go's strings.TrimSpace(res.Stdout)
// to pass chatter to revealBundle) is Windows-only and cannot be
// exercised in `go test` on Linux CI.
//
// Phase 16 fix shipped in:
//   - cmd/otto-tray/tray.go: revealBundle now parses the LAST non-empty
//     stdout line, not the whole TrimSpace'd blob.
//   - scripts/gw.ps1: Write-Stderr helper + redirect of
//     Initialize-Config "loaded env file" / "loaded overrides" lines
//     and the "Note: gateway not running" branch in Invoke-Support to
//     stderr. Stdout from the support verb now contains only the
//     archive path as the final line.
//
// Manual reproducer: tests/reliability/manual/REL-TRAY-06-repro.ps1
// Run on Windows with GW_HOME set to a real install; the wrapper
// stdout must contain only the archive path.
func TestRegression_REL_TRAY_06_WindowsBundlePathPollution(t *testing.T) {
	t.Skip("REL-TRAY-06 (T-6): manual validation required — run tests/reliability/manual/REL-TRAY-06-repro.ps1 on Windows with env file present; fix shipped in tray.go revealBundle (last-non-empty-line parse) and gw.ps1 (Write-Stderr informational redirect)")
}
