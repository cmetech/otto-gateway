//go:build darwin || windows

package main

import (
	"testing"
	"time"
)

// TestRegression_REL_TRAY_04_WindowsNotifyBlocking demonstrates that applyState
// calls notify() synchronously on the uiLoop goroutine. On Windows, notify()
// launches a foreground-stealing MessageBox (via PowerShell) that blocks for up
// to 30 seconds until the user clicks OK. During that block, stateCh (cap 4)
// fills and the poller goroutine blocks on send — polling and menu updates freeze.
//
// Pre-fix observable: applyState blocks for the duration of the notify() call
// when transitioning from StateRunning to StateError/StateStopped.
// Post-fix: notify is dispatched off the uiLoop (go notify(...)) so applyState
// returns immediately.
//
// Note: this test uses a fake notify injection that blocks on an unsignaled
// channel, simulating the Windows MessageBox behavior without actually
// spawning a PowerShell process. The test itself is OS-independent in logic
// but is gated to darwin || windows because the uiLoop and applyState are
// only compiled on those platforms.
func TestRegression_REL_TRAY_04_WindowsNotifyBlocking(t *testing.T) {
	t.Skip("REL-TRAY-04 (T-4): regression test — unskip in Phase 16 fix commit")

	// blockGate simulates a Windows MessageBox that blocks indefinitely
	// until signaled (or until the test timeout cancels it).
	blockGate := make(chan struct{})
	defer close(blockGate) // ensure gate is closed on test exit

	// Record how long applyState takes to return.
	// Pre-fix: applyState calls notify() synchronously, so it blocks here
	// for as long as blockGate remains unsignaled.
	// Post-fix: applyState launches notify() in a goroutine and returns
	// immediately regardless of how long notify() takes.
	start := time.Now()

	// To inject the fake blocking notify, Phase 16 must expose a seam
	// (e.g., a package-level var notifyFn = notify, swappable in tests).
	// Pre-fix: no seam exists; applyState directly calls the package-level
	// notify() function. This comment documents the required injection point.
	//
	// The assertion below documents the EXPECTED post-fix behavior:
	//   oldNotify := notifyFn
	//   notifyFn = func(title, body string) { <-blockGate }
	//   defer func() { notifyFn = oldNotify }()
	//
	//   s := &trayState{...}
	//   s.current = StateRunning
	//   s.applyState(stateOutput{State: StateStopped})
	//   elapsed := time.Since(start)
	//   if elapsed > 100*time.Millisecond {
	//       t.Fatalf("applyState blocked %v on notify — uiLoop frozen (pre-fix observable)", elapsed)
	//   }

	// Pre-fix assertion (documents the broken behavior):
	// applyState DOES call notify() synchronously; on the real Windows path
	// this blocks the uiLoop for ~30s. Since we cannot inject without the
	// seam, we document elapsed time from test start as a no-op placeholder
	// that will be replaced when the seam lands in Phase 16.
	elapsed := time.Since(start)
	_ = elapsed // silence unused-variable lint; real assertion is above
}
