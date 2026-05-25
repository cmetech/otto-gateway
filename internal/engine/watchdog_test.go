// Package engine — whitebox watchdog tests (D-06 / STRM-04).
//
// These tests reuse the fakeACP harness (engine_test.go) and newTestEngine
// helper directly — both are package engine (whitebox), so no redeclaration
// is needed. The goleak gate in testmain_test.go applies automatically.
//
// Test 1: TestWatchdog_CancelOnCtxDone — context cancellation fires
// Cancel(sid) via the AfterFunc watchdog goroutine.
//
// Test 2: TestWatchdog_StopPreventsCancel_OnNormalCompletion — stop() called
// BEFORE ctx cancellation; the AfterFunc goroutine is deregistered so Cancel
// is never observed (Codex-strengthened stop→cancel ordering; D-06 D-12).
package engine

import (
	"context"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// TestWatchdog_CancelOnCtxDone verifies that the context.AfterFunc watchdog
// calls Cancel(sid) when the request ctx is canceled mid-stream.
//
// Uses a channel-based wait (select-with-deadline) rather than a bare sleep
// (REVIEWS.md §"LOW: Timing-based watchdog tests may be flaky" — fix).
func TestWatchdog_CancelOnCtxDone(t *testing.T) {
	const sid = "watchdog-cancel-sid"

	// fakeACP with the watchdog session id; no chunks — stream closes immediately
	// after Prompt so no goroutine is left draining.
	ack := &fakeACP{
		newSessionID: sid,
		chunksToEmit: nil, // channel is created, buffered at 0, and closed immediately
	}
	eng := newTestEngine(t, ack)

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	req := simpleUserReq("watchdog cancel test", "claude-sonnet-4-7")
	run, err := eng.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Drain the stream — required to release any goroutines blocked in fakeACP
	// and to ensure the stream is in a well-defined state before cancellation.
	for c := range run.Stream().Chunks() {
		_ = c
	}
	// Call Result to drain the Result side as well (belt-and-suspenders).
	_, _ = run.Stream().Result()

	// Trigger the AfterFunc watchdog by canceling the ctx.
	// Do NOT call StopWatchdog — the point of this test is that the goroutine fires.
	cancelFn()

	// Channel-based wait: spin until Cancel is observed or the deadline fires.
	// 2-second deadline is generous — AfterFunc goroutines are scheduled immediately
	// on ctx cancellation but may lag behind the GMP scheduler.
	deadline := time.After(2 * time.Second)
	for {
		ack.mu.Lock()
		n := len(ack.cancelCalls)
		ack.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("watchdog: Cancel not called within 2s after ctx cancellation")
		case <-time.After(5 * time.Millisecond):
			// retry
		}
	}

	// Assert the correct sid was passed to Cancel.
	ack.mu.Lock()
	calls := make([]string, len(ack.cancelCalls))
	copy(calls, ack.cancelCalls)
	ack.mu.Unlock()

	if len(calls) == 0 {
		t.Fatal("watchdog: cancelCalls is empty — should have been caught above")
	}
	if calls[0] != sid {
		t.Errorf("watchdog: Cancel called with %q, want %q", calls[0], sid)
	}
}

// TestWatchdog_StopPreventsCancel_OnNormalCompletion verifies the D-06
// Codex-strengthened ordering: stop() FIRST → cancelFn() THEN → no Cancel.
//
// Sequence:
//  1. Run with cancelable ctx.
//  2. Drain stream to natural completion.
//  3. FIRST: call stop() via run.StopWatchdog() — deregisters the AfterFunc goroutine.
//  4. THEN: call cancelFn() — would trigger the goroutine if it were still registered.
//  5. Brief wait (50ms is justified: AfterFunc goroutine has 50ms to NOT execute).
//  6. Assert cancelCalls is empty.
func TestWatchdog_StopPreventsCancel_OnNormalCompletion(t *testing.T) {
	const sid = "watchdog-stop-sid"

	ack := &fakeACP{
		newSessionID: sid,
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "done"}},
		},
	}
	eng := newTestEngine(t, ack)

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	req := simpleUserReq("watchdog stop test", "claude-sonnet-4-7")
	run, err := eng.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Drain the stream to natural completion.
	for c := range run.Stream().Chunks() {
		_ = c
	}
	_, _ = run.Stream().Result()

	// FIRST: stop the watchdog — deregisters the AfterFunc goroutine.
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}

	// THEN: cancel the ctx — should NOT trigger Cancel(sid) because stop() ran first.
	cancelFn()

	// Brief wait: AfterFunc goroutine has 50ms to (not) execute.
	// A bare sleep is justified here because we are asserting ABSENCE of an event;
	// there is no channel signal to wait on. 50ms is sufficient for the scheduler
	// to run any pending goroutine.
	time.Sleep(50 * time.Millisecond)

	ack.mu.Lock()
	calls := make([]string, len(ack.cancelCalls))
	copy(calls, ack.cancelCalls)
	ack.mu.Unlock()

	if len(calls) > 0 {
		t.Errorf("watchdog: unexpected Cancel calls after stop() + cancelFn(): %v", calls)
	}
}
