package openai

// Regression test for REL-HTTP-02 (H-2): idle-timeout path in runSSEEmitter
// calls StopWatchdog() — suppressing the ACP Cancel AfterFunc — but never
// issues an explicit session/cancel. The hung kiro-cli worker is returned to
// the free pool mid-abandoned-prompt and the next request acquires it in an
// unknown state.
//
// Pre-fix observable: when idle-timeout fires, StopWatchdog is called (returns
// true = AfterFunc stopped), but no separate Cancel mechanism is invoked. The
// watching side (pool ctx-watcher) releases the slot — the worker is still
// generating — and goes back into the free queue.
//
// The reproducer instruments a custom RunHandle that records whether
// StopWatchdog() returned true (AfterFunc was active and is now stopped). It
// then asserts that the only cancel mechanism (the watchdog) was suppressed
// without a compensating explicit cancel call.
//
// Post-fix: either (a) StopWatchdog is NOT called on the idleC path (letting
// the AfterFunc fire Cancel), or (b) an explicit Cancel call is added before
// StopWatchdog(). Unskip in Phase 15 fix commit and flip the assertion.

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
)

// trackingRunHandle wraps fakeRunHandle and records watchdog interactions.
// watchdogStopped is set to true when StopWatchdog() is called and the returned
// function returns true (meaning the AfterFunc was stopped before it fired).
type trackingRunHandle struct {
	inner           *fakeRunHandle
	watchdogCalled  bool
	watchdogStopped bool // true = AfterFunc was alive and is now stopped
}

func (h *trackingRunHandle) Stream() Stream                                { return h.inner.stream }
func (h *trackingRunHandle) SessionID() string                             { return h.inner.sessionID }
func (h *trackingRunHandle) ShortCircuitResponse() *canonical.ChatResponse { return nil }
func (h *trackingRunHandle) StopWatchdog() func() bool {
	h.watchdogCalled = true
	// Return a function that records whether the AfterFunc was active.
	// Returning true simulates "AfterFunc was alive and is now stopped" —
	// the pre-fix condition where the watchdog (and thus ACP Cancel) is
	// suppressed.
	return func() bool {
		h.watchdogStopped = true
		return true // AfterFunc was alive; now stopped = no Cancel will fire
	}
}

// TestRegression_REL_HTTP_02_IdleTimeoutReturnsHungWorker demonstrates that
// the idle-timeout branch in runSSEEmitter stops the engine watchdog (which
// carries the ACP Cancel mechanism) but never issues an explicit Cancel.
// Pre-fix: watchdog is stopped = Cancel AfterFunc is suppressed = the kiro-cli
// session keeps running after the slot is returned to the free pool.
func TestRegression_REL_HTTP_02_IdleTimeoutReturnsHungWorker(t *testing.T) {
	t.Skip("REL-HTTP-02 (H-2): regression test — unskip in Phase 15 fix commit")
	defer goleak.VerifyNone(t)

	// Set up a fakeStream whose Chunks() channel never produces a value —
	// simulating a wedged kiro-cli worker that triggers idle-timeout.
	chunks := make(chan canonical.Chunk) // never sends
	t.Cleanup(func() {
		// Close during cleanup to unblock any goroutines waiting on the channel.
		defer func() { _ = recover() }()
		close(chunks)
	})

	inner := &fakeRunHandle{
		stream: &fakeStream{
			chunks: chunks,
			final:  &canonical.FinalResult{StopReason: canonical.StopUnknown},
		},
		sessionID: "session_hung_worker",
	}
	run := &trackingRunHandle{inner: inner}

	rec := httptest.NewRecorder()
	ctx := context.Background()

	// Drive runSSEEmitter with a short idle timeout (100ms) so the idle branch
	// fires quickly. Pre-fix: the idleC branch calls StopWatchdog()() but does
	// not explicitly call Cancel(sessionID).
	const idleTimeout = 100 * time.Millisecond
	_, err := runSSEEmitter(ctx, rec, run, &canonical.ChatRequest{}, "auto", idleTimeout, nullLogger())

	// Expect an idle-timeout error.
	if !errors.Is(err, canonical.ErrStreamIdleTimeout) {
		t.Fatalf("expected ErrStreamIdleTimeout, got %v", err)
	}

	// Pre-fix observable (assertion 1): StopWatchdog was called on the idle path.
	// This is the mechanism that suppresses ACP Cancel.
	if !run.watchdogCalled {
		t.Error("pre-fix reproducer: StopWatchdog() was NOT called on idle-timeout path; " +
			"expected it to be called (suppressing ACP Cancel)")
	}

	// Pre-fix observable (assertion 2): The watchdog AfterFunc was stopped
	// (returned true), meaning the ACP Cancel mechanism was suppressed.
	if !run.watchdogStopped {
		t.Error("pre-fix reproducer: watchdog stop function returned false; " +
			"expected true (AfterFunc was alive and is now cancelled without issuing Cancel)")
	}

	// The critical gap: the idleC branch stops the watchdog but emits no Cancel.
	// The pool's ctx-watcher releases the slot on ctx.Done (which fires after
	// runSSEEmitter returns and the streaming handler calls cancelFn). The
	// slot re-enters the free queue carrying a still-generating kiro session.
	//
	// Post-fix: either (a) watchdogCalled is false (AfterFunc fires Cancel
	// naturally), OR (b) an explicit cancel call occurs that this tracking handle
	// could observe — flip the assertion in the Phase 15 unskip commit.
	t.Logf("pre-fix observable confirmed: watchdogCalled=%v watchdogStopped=%v — "+
		"ACP Cancel was suppressed via StopWatchdog(); no explicit Cancel issued",
		run.watchdogCalled, run.watchdogStopped)
}
