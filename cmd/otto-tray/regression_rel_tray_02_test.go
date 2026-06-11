//go:build darwin || windows

package main

import "testing"

// TestRegression_REL_TRAY_02_WindowsBundleExitOne is a discoverability stub for
// REL-TRAY-02. The failure mode (Get-GatewayStatus using exit 1 mid-collection
// which terminates the whole support bundle script when the gateway is down) is
// PowerShell-only and cannot be exercised in go test on Linux CI.
//
// Manual reproducer: tests/reliability/manual/REL-TRAY-02-repro.ps1
// Run on Windows with the gateway stopped to observe pre-fix behavior.
func TestRegression_REL_TRAY_02_WindowsBundleExitOne(t *testing.T) {
	t.Skip("REL-TRAY-02 (T-2): manual reproducer — see tests/reliability/manual/REL-TRAY-02-repro.ps1 for Windows-only reproducer")
}
