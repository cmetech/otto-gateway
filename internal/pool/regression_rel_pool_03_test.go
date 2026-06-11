// Package pool_test — regression reproducer for REL-POOL-03 (P-3, High).
// Test is permanently t.Skip()'d during Phase 14. Phase 15's fix commit removes
// the t.Skip line in the same atomic commit as the client.go source fix.
package pool_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"

	"go.uber.org/goleak"
)

// TestRegression_REL_POOL_03_StaleActiveStreamClobber reproduces High finding P-3:
// the stale awaitPromptResult goroutine from prompt A unconditionally nils
// c.activeStream even after the slot has been recycled and prompt B has installed
// a new stream. Every subsequent session/update chunk for B hits the s == nil
// branch in handleNotification and is dropped — B's response completes with zero
// content (silent empty 200).
//
// The race is enabled by the pool releasing a slot back to p.slots BEFORE the
// slot's previous awaitPromptResult goroutine has exited. On ctx-cancel or
// idle-timeout, Pool.Cancel returns the slot to the free queue concurrently
// while awaitPromptResult(A) is still parked on its ctx.Done() arm.
//
// Pre-fix observable: prompt B (on the recycled slot) returns a non-nil stream
// but the stream drains with zero content (FinalResult.ChunkCount == 0 AND
// stop_reason suggests completion, but no actual chunks were delivered).
// The test asserts this incorrect behavior to demonstrate the bug.
//
// Post-fix expectation (Phase 15): CAS guard in awaitPromptResult — only nil
// c.activeStream when activeStream == stream (the stream currently being closed).
func TestRegression_REL_POOL_03_StaleActiveStreamClobber(t *testing.T) {
	defer goleak.VerifyNone(t)

	const iterations = 20

	// successfulCompletions counts how many times Prompt B's stream completed
	// with the expected stop reason (not an error). Pre-fix: a stale
	// awaitPromptResult goroutine for A nils c.activeStream after B installs
	// its stream, causing B's session/prompt result frame to be lost —
	// B's stream never closes and Result() hangs or returns with an unexpected
	// error. Post-fix: identity guard prevents the clobber so all B calls
	// complete cleanly.
	var successfulCompletions int64

	// Build a size-1 pool. The single slot is reused across prompt A and
	// prompt B — this is the recycled-slot scenario from the finding.
	//
	// Prompt A: cancel ctx mid-stream to trigger the stale awaitPromptResult
	// goroutine path (ctx.Done() arm in client.go unconditionally nils
	// c.activeStream before the fix).
	//
	// Prompt B: immediately after slot release, acquire the same slot and
	// assert that Result() completes without error (not silently dropped).
	var promptCallsMu sync.Mutex
	promptCalls := 0
	// raceGate is closed once — when Prompt A's goroutine has started.
	// All subsequent iterations reuse the already-closed channel (immediate read).
	raceGate := make(chan struct{})
	var raceGateOnce sync.Once

	cf := &fakeClientFactory{
		clients: []pool.PoolClient{&fakeClient{
			promptFn: func(ctx context.Context, sid string, blocks []canonical.Block) (*acp.Stream, error) {
				promptCallsMu.Lock()
				call := promptCalls
				promptCalls++
				promptCallsMu.Unlock()

				s := acp.NewStreamForTest(sid)
				if call%2 == 0 {
					// Even calls = Prompt A: signal the race gate, then block until
					// ctx is cancelled. A's awaitPromptResult goroutine will try to
					// nil c.activeStream on ctx.Done() — the identity guard (post-fix)
					// prevents this from clobbering B's stream.
					capturedCtx := ctx
					go func() {
						raceGateOnce.Do(func() { close(raceGate) })
						<-capturedCtx.Done()
						s.CloseForTest(nil, capturedCtx.Err())
					}()
				} else {
					// Odd calls = Prompt B: close the stream immediately (before
					// promptFn returns) with a successful result so Result() finds
					// the stream already closed and races with nothing.
					// Note: CloseForTest must complete BEFORE we return from
					// promptFn so the test's streamB.Result() call is race-free.
					s.CloseForTest(&acp.FinalResult{
						StopReason: canonical.StopEndTurn,
					}, nil)
				}
				return s, nil
			},
		}},
	}

	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: cf,
	})

	warmCtx, warmCancel := context.WithTimeout(context.Background(), time.Second)
	defer warmCancel()
	if err := p.Warmup(warmCtx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	defer func() { _ = p.Close() }()

	for i := 0; i < iterations; i++ {
		// Acquire slot for Prompt A with a cancellable ctx.
		ctxA, cancelA := context.WithCancel(context.Background())

		sidA, err := p.NewSession(ctxA, "")
		if err != nil {
			cancelA()
			t.Fatalf("iter %d: NewSession A: %v", i, err)
		}

		streamA, err := p.Prompt(ctxA, sidA, nil)
		if err != nil {
			cancelA()
			t.Fatalf("iter %d: Prompt A: %v", i, err)
		}

		// Wait for A's prompt goroutine to have started.
		<-raceGate
		// Cancel A's ctx — this triggers the stale awaitPromptResult nil of
		// c.activeStream concurrently with Prompt B's stream install.
		cancelA()

		// Race window: give A's awaitPromptResult time to run its ctx.Done() arm
		// (which would have nilled c.activeStream before the fix).
		time.Sleep(time.Millisecond)

		// Acquire slot for Prompt B on the recycled slot.
		ctxB := context.Background()
		sidB, err := p.NewSession(ctxB, "")
		if err != nil {
			t.Fatalf("iter %d: NewSession B: %v", i, err)
		}

		streamB, err := p.Prompt(ctxB, sidB, nil)
		if err != nil {
			t.Fatalf("iter %d: Prompt B: %v", i, err)
		}

		// Consume A's result concurrently (it errors with context.Canceled — that's fine).
		go func() { _, _ = streamA.Result() }()

		// B's stream should complete with StopEndTurn without hanging.
		resultB, errB := streamB.Result()
		if errB == nil && resultB != nil && resultB.StopReason == canonical.StopEndTurn {
			successfulCompletions++
		}
	}

	// POST-FIX ASSERTION: all iterations of Prompt B must complete cleanly.
	// The identity guard in awaitPromptResult ensures A's stale goroutine
	// does NOT nil out B's activeStream, so B's result frame is properly
	// routed and Result() returns the expected FinalResult.
	if successfulCompletions != iterations {
		t.Errorf("post-fix: successfulCompletions = %d; want %d — "+
			"stale awaitPromptResult goroutines may be clobbering B's activeStream",
			successfulCompletions, iterations)
	}
}
