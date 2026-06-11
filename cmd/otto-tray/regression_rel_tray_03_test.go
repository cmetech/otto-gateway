//go:build darwin || windows

package main

import "testing"

// TestRegression_REL_TRAY_03_MacosSilentGatewayDeath is a discoverability stub
// for REL-TRAY-03. The failure mode (gateway death produces no visible signal
// in the macOS menu bar because notify() is osascript display notification
// which silently no-ops for LSUIElement agents without notification permission,
// and setIcon/SetTooltip are called only once in onReady) requires a real macOS
// GUI session and cannot be exercised in CI.
//
// Manual reproducer: tests/reliability/manual/REL-TRAY-03-repro.sh
// Run on macOS with the tray app active to observe pre-fix behavior.
func TestRegression_REL_TRAY_03_MacosSilentGatewayDeath(t *testing.T) {
	t.Skip("REL-TRAY-03 (T-3): manual validation required — run tests/reliability/manual/REL-TRAY-03-repro.sh on macOS GUI session; REL-TRAY-03 fix shipped in this commit")
}
