//go:build darwin || windows

package main

import "testing"

// TestRegression_REL_TRAY_07_SupportBundleBounds is the discoverability
// stub for REL-TRAY-07. The failure mode and the fix are both
// PowerShell-only:
//
// Pre-fix observable (three failure modes — see 14-FINDING-T-7.md):
//
//  1. Size cap exemption: live current-day logs (otto-gateway.log,
//     chat-trace.log, boot stdout/stderr) were copied unconditionally;
//     only rotated .log.gz files passed through the --max-mb cap loop.
//     A 200MB log day blew past any cap.
//
//  2. No overall timeout with staging cleanup; a hung kiro-cli or a
//     stalled redaction stream could park the operator's tray
//     indefinitely.
//
//  3. Progress lines on stdout polluted the archive-path channel.
//
// Phase 16 fix shipped in scripts/otto-gw.ps1 Invoke-Support:
//
//   - $MaxMb default 50 -> 512; new $Timeout param default 180s.
//   - Live logs tail-trimmed to the cap on copy (newline-aligned), so
//     each individual log enters the bundle at or below cap.
//   - Belt-and-suspenders cap-loop drops oldest logs (rotated first,
//     then live) if the bundle aggregate still exceeds cap.
//   - System.Diagnostics.Stopwatch + Test-Deadline helper bounds the
//     entire assembly. On overrun, throw enters the try/finally
//     cleanup path that removes the staging dir.
//   - Progress lines routed through Write-Stderr so the archive path
//     remains the SOLE stdout line (also T-6 fix dependency).
//
// Test classification: permanent-skip stub. The full reproducer
// requires invoking the real PowerShell wrapper with a large fake log
// dir on Windows. Linux CI cannot run pwsh + the Windows-specific
// directory layout. Following the same Phase 15 T-2/T-3 +
// Phase 16 T-6 precedent for Windows-only PS1-resident fixes.
//
// Manual reproducer: tests/reliability/manual/REL-TRAY-07-repro.ps1
func TestRegression_REL_TRAY_07_SupportBundleBounds(t *testing.T) {
	t.Skip("REL-TRAY-07 (T-7): manual validation required — run tests/reliability/manual/REL-TRAY-07-repro.ps1 on Windows; fix shipped in scripts/otto-gw.ps1 Invoke-Support ($MaxMb=512, $Timeout=180s, live-log cap on copy, try/finally staging cleanup, Write-Stderr progress)")
}
