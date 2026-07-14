//go:build darwin || windows

package main

import "testing"

// TestRegression_REL_TRAY_09_BundleRowRemoval is the discoverability
// stub for REL-TRAY-09. The bundle row emission lives in
// scripts/otto-gw (bash) — not in any Go code on the macOS path — so
// the regression assertion is shell-side.
//
// Pre-fix observable (D-18-10):
//
//  1. scripts/otto-gw emitted tray/tray-state.txt by cat-ing
//     $OTTO_INSTALL_ROOT/.otto/tray/state, a file the tray has never
//     written. Every bundle ever produced showed "(unavailable: ...
//     does not exist)" for this row.
//
//  2. scripts/otto-gw emitted tray/autostart.txt by probing
//     $HOME/Library/LaunchAgents/com.otto.tray.plist, but the actual
//     plist label is io.cmetech.gateway-tray (see
//     cmd/otto-tray/autostart_darwin.go:15). The row always reported
//     the LaunchAgent absent.
//
// Phase 18-03 fix removes both row-emitting blocks from
// scripts/otto-gw. The Windows bundle path in scripts/otto-gw.ps1 is
// untouched (Run-key probe is correct).
//
// Test classification: permanent-skip stub. Following the same
// precedent as TestRegression_REL_TRAY_07_SupportBundleBounds for
// wrapper-only fixes. The real assertion is in
// tests/scripts/test-support-bundle-rel-tray-09.sh which actually
// runs the wrapper, extracts the archive, and asserts the absence of
// the two rows (plus a positive control on tray/pidfile.txt).
func TestRegression_REL_TRAY_09_BundleRowRemoval(t *testing.T) {
	t.Skip("REL-TRAY-09 (D-18-10): regression is bash-side. Run tests/scripts/test-support-bundle-rel-tray-09.sh — asserts tray/tray-state.txt and tray/autostart.txt are absent from the bundle; tray/pidfile.txt is the positive control.")
}
