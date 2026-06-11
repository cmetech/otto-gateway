//go:build darwin || windows

package main

import (
	"testing"
	"time"
)

// TestRegression_REL_TRAY_04_WindowsNotifyBlocking demonstrates that the
// running→error/stopped notify dispatch on the uiLoop goroutine MUST NOT
// block on the platform notify implementation. On Windows, notify() spawns
// a foreground MessageBox via PowerShell that blocks up to 30 seconds until
// the user clicks OK; during that block, stateCh (cap 4) fills and the
// poller goroutine stalls — polling and menu updates freeze.
//
// Pre-fix observable: notifyTransition calls notifyFn synchronously, so the
// call site blocks for the duration of the platform notify.
//
// Post-fix (T-4 fix): notifyTransition wraps the notifyFn call in a
// goroutine; the call site returns immediately regardless of how long
// notifyFn takes.
//
// Test strategy: inject a blocking notifyFn via the package-level seam,
// call notifyTransition with a StateRunning → StateStopped transition, and
// measure elapsed time. The test is OS-independent in logic but is gated
// to darwin || windows because notifyFn / trayState only compile on those
// platforms.
func TestRegression_REL_TRAY_04_WindowsNotifyBlocking(t *testing.T) {
	// blockGate simulates a Windows MessageBox that blocks indefinitely
	// until signaled (or until the deferred close fires on test exit).
	blockGate := make(chan struct{})
	defer close(blockGate) // ensure gate is closed on test exit so the goroutine drains

	// Injection seam (REL-TRAY-04 / T-4 fix in uihelpers_windows.go and
	// uihelpers_darwin.go): notifyFn is a package-level var pointing at
	// notifyImpl by default. The test swaps it for a blocking stub that
	// holds until blockGate closes.
	oldNotify := notifyFn
	notifyFn = func(title, body string) {
		<-blockGate
	}
	defer func() { notifyFn = oldNotify }()

	// trayState is the smallest fixture that exposes notifyTransition. The
	// fields applyState normally touches (miHeader, miSubheader, etc) are
	// not exercised by notifyTransition so a zero-value trayState is safe.
	s := &trayState{
		current: StateRunning,
	}

	// Pre-fix path would call notifyFn synchronously here; with the
	// blocking stub injected, notifyTransition would never return until
	// the test exited and the deferred close drained the goroutine.
	// Post-fix path dispatches notifyFn in a goroutine; notifyTransition
	// returns immediately and the elapsed time is small.
	start := time.Now()
	s.notifyTransition(StateRunning, StateStopped)
	elapsed := time.Since(start)

	// 100ms is generous: the post-fix path does a `go func()` which on
	// modern Go runtimes takes single-digit microseconds. Anything in the
	// >100ms range means the synchronous blocking notifyFn is still on
	// the call path — uiLoop frozen (pre-fix observable).
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("notifyTransition blocked %v on notifyFn — uiLoop frozen (pre-fix observable)", elapsed)
	}
}
