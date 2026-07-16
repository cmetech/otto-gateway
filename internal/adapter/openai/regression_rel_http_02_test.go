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
// StopWatchdog() was called. Post-fix: StopWatchdog is NOT called on the idleC
// path, letting the watchdog AfterFunc fire Cancel naturally via the deferred
// cancelFn on handler return.
//
// H-2 fix (REL-HTTP-02): unskipped in Phase 15. Assertion flipped from pre-fix
// "watchdogCalled==true" to post-fix "watchdogCalled==false".

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

// TestRegression_REL_HTTP_02_IdleTimeoutReturnsHungWorker verifies that
// the idle-timeout branch in runSSEEmitter does NOT call StopWatchdog().
// Post-fix: the watchdog AfterFunc fires Cancel naturally via the deferred
// cancelFn on handler return — no slot is returned with a still-generating
// kiro-cli worker.
func TestRegression_REL_HTTP_02_IdleTimeoutReturnsHungWorker(t *testing.T) {
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
	// fires quickly. Post-fix: the idleC branch must NOT call StopWatchdog().
	const idleTimeout = 100 * time.Millisecond
	_, err := runSSEEmitter(ctx, rec, run, &canonical.ChatRequest{}, nil, "auto", idleTimeout, nullLogger())

	// Expect an idle-timeout error.
	if !errors.Is(err, canonical.ErrStreamIdleTimeout) {
		t.Fatalf("expected ErrStreamIdleTimeout, got %v", err)
	}

	// Post-fix assertion: StopWatchdog was NOT called on the idle path.
	// The watchdog AfterFunc must fire Cancel naturally (via deferred cancelFn
	// on handler return), not be suppressed by an explicit StopWatchdog() call.
	if run.watchdogCalled {
		t.Error("post-fix regression: StopWatchdog() was called on idle-timeout path; " +
			"this suppresses ACP Cancel and returns a hung worker to the free pool (H-2 bug)")
	}

	// Corollary: if watchdogCalled is false, watchdogStopped must also be false.
	if run.watchdogStopped {
		t.Error("post-fix regression: watchdog stop function was invoked; " +
			"StopWatchdog() must not be called on the idle-timeout path")
	}

	t.Logf("post-fix verified: watchdogCalled=%v watchdogStopped=%v — "+
		"ACP Cancel fires naturally via watchdog AfterFunc; no suppression on idle path",
		run.watchdogCalled, run.watchdogStopped)
}
