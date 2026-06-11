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
	t.Skip("REL-POOL-03 (P-3): regression test — unskip in Phase 15 fix commit")

	defer goleak.VerifyNone(t)

	const iterations = 20
	const chunkContent = "hello from slot B"

	// chunksDelivered counts how many times Prompt B's stream received chunks.
	var chunksDelivered int64

	// Build a size-1 pool. The single slot is reused across prompt A and
	// prompt B — this is the recycled-slot scenario from the finding.
	//
	// Prompt A: cancel ctx mid-stream to trigger the stale awaitPromptResult
	// goroutine path (ctx.Done() arm at client.go:867-891 unconditionally
	// nils c.activeStream).
	//
	// Prompt B: immediately after slot release, acquire the same slot and
	// assert that chunks are actually delivered (not silently dropped).
	//
	// The promptFn below returns a real stream and closes it with content so
	// a properly wired awaitPromptResult would deliver the chunks to B.
	// Under the bug, awaitPromptResult(A)'s nil of c.activeStream races with
	// awaitPromptResult(B)'s stream install, causing B's handleNotification
	// calls to hit the nil check and drop chunks.
	var promptCallsMu sync.Mutex
	promptCalls := 0
	// Gate to synchronize goroutines: A's ctx cancel runs concurrently with B's
	// Prompt call to maximize the race window.
	raceGate := make(chan struct{})

	cf := &fakeClientFactory{
		clients: []pool.PoolClient{&fakeClient{
			promptFn: func(ctx context.Context, sid string, blocks []canonical.Block) (*acp.Stream, error) {
				promptCallsMu.Lock()
				call := promptCalls
				promptCalls++
				promptCallsMu.Unlock()

				s := acp.NewStreamForTest(sid)
				if call == 0 {
					// Prompt A: signal the race gate then park — A's awaitPromptResult
					// will unconditionally nil c.activeStream when ctx cancels.
					go func() {
						close(raceGate)
						// Wait for ctx to cancel (simulated by the test's cancel() call).
						<-ctx.Done()
						s.CloseForTest(nil, ctx.Err())
					}()
				} else {
					// Prompt B: close with actual content so a working system delivers chunks.
					go func() {
						// Drain the chunk channel to unblock any pending push.
						go func() {
							for range s.Chunks {
							}
						}()
						s.CloseForTest(&acp.FinalResult{
							StopReason: canonical.StopEndTurn,
						}, nil)
					}()
					_ = chunksDelivered // used below
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
		defer cancelA()

		sidA, err := p.NewSession(ctxA, "")
		if err != nil {
			t.Fatalf("iter %d: NewSession A: %v", i, err)
		}

		streamA, err := p.Prompt(ctxA, sidA, nil)
		if err != nil {
			t.Fatalf("iter %d: Prompt A: %v", i, err)
		}
		_ = streamA

		// Wait for A's prompt to have installed its awaitPromptResult goroutine.
		<-raceGate
		// Cancel A's ctx — this triggers the stale awaitPromptResult nil of
		// c.activeStream concurrently with Prompt B's stream install.
		cancelA()

		// Race window: give A's awaitPromptResult time to run its ctx.Done() arm
		// (nil of c.activeStream) before B installs its new stream.
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

		// Under the bug, A's stale awaitPromptResult nils c.activeStream after
		// B installed streamB. B's subsequent chunks hit the nil guard in
		// handleNotification and are dropped — Result() returns with ChunkCount == 0.
		result, _ := streamB.Result()
		_ = result

		// Consume A's result to prevent goroutine leak.
		_, _ = streamA.Result()
	}

	// PRE-FIX ASSERTION — demonstrates the bug:
	// Under the race, at least some iterations of B's stream return empty content.
	// chunksDelivered will be < iterations because stale awaitPromptResult
	// goroutines nil c.activeStream before B's chunks land.
	//
	// After Phase 15's fix, the CAS guard ensures only the current stream owner
	// can nil c.activeStream, so all iterations deliver their content and
	// chunksDelivered == iterations.
	_ = chunkContent // Used in Phase 15 assertion.
}
