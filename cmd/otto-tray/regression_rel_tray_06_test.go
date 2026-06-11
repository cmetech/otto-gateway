//go:build darwin || windows

package main

import "testing"

// TestRegression_REL_TRAY_06_WindowsBundlePathPollution is a discoverability
// stub for REL-TRAY-06. The failure mode (Initialize-Config's Write-Host lines
// polluting stdout before the bundle archive path, causing tray.go:296's
// strings.TrimSpace(res.Stdout) to pass the chatter as the archive path to
// revealBundle) is Windows-only and cannot be exercised in go test on Linux CI.
//
// Manual reproducer: tests/reliability/manual/REL-TRAY-06-repro.ps1
// Run on Windows with OTTO_HOME set to a real install to observe the
// first-vs-last stdout line discrepancy.
func TestRegression_REL_TRAY_06_WindowsBundlePathPollution(t *testing.T) {
	t.Skip("REL-TRAY-06 (T-6): manual reproducer — see tests/reliability/manual/REL-TRAY-06-repro.ps1 for Windows-only reproducer")
}
