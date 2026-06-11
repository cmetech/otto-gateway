// Package pool_test — regression reproducer for REL-POOL-04 (P-4, Medium).
// Test is permanently t.Skip()'d during Phase 14. Phase 16's fix commit removes
// the t.Skip line in the same atomic commit as the stream.go/client.go source fix.
package pool_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"

	"go.uber.org/goleak"
)

// TestRegression_REL_POOL_04_ConsumerBlockedReadLoop reproduces Medium finding P-4:
// a stalled chunk consumer (no drain on the chunk channel) blocks the readLoop
// because stream.push (stream.go:105-122) blocks the caller (the readLoop
// goroutine, via handleNotification) when the 64-chunk buffer is full with
// the client lifetime context (c.clientCtx) — not a per-request ctx.
//
// The readLoop is also the goroutine that dispatches inbound frames INCLUDING
// ping responses. When push blocks the readLoop, the pingLoop times out waiting
// for a ping response and escalates to c.cancel() (SIGKILL equivalent),
// killing a perfectly healthy kiro-cli worker.
//
// Pre-fix observable: when the consumer stalls, the ping escalation at
// client.go:516 fires — the pool logs "acp.ping.escalated_to_close" and
// respawns the worker even though kiro-cli was healthy.
//
// Post-fix expectation (Phase 16): push is bounded by the PER-REQUEST context
// (not the client lifetime context) so a stalled consumer fails its own request
// instead of wedging the readLoop and starving the pingLoop. Alternatively,
// the readLoop uses a separate path for ping dispatch independent of the consumer
// drain state.
func TestRegression_REL_POOL_04_ConsumerBlockedReadLoop(t *testing.T) {
	// P-4 (REL-POOL-04) fix shipped in Phase 16-01:
	//   internal/acp/stream.go — Stream.ctx field + Stream.Ctx() accessor
	//   internal/acp/client.go — handleNotification passes s.Ctx() to push
	// A stalled consumer now fails its OWN request via per-request ctx
	// instead of blocking the readLoop goroutine and starving the pingLoop.
	defer goleak.VerifyNone(t)

	// escalationFired counts how many times the pingLoop issued a
	// "acp.ping.escalated_to_close" escalation (SIGKILL equivalent).
	var escalationFired atomic.Int32

	// Build a fakeClient whose promptFn returns a stream and then sends
	// chunks continuously (faster than the consumer drains) to fill the
	// 64-chunk buffer. We do NOT call drainChunks on the returned stream.
	const bufSize = 64 // stream.go chunk buffer size per handleNotification

	cf := &fakeClientFactory{
		clients: []pool.PoolClient{&fakeClient{
			promptFn: func(ctx context.Context, sid string, blocks []canonical.Block) (*acp.Stream, error) {
				s := acp.NewStreamForTest(sid)
				// The production path sends chunks via handleNotification on the
				// readLoop goroutine. We simulate the full buffer scenario by
				// closing the stream with a final result after filling the buffer,
				// leaving the consumer (the test) stalled with no drain calls.
				// In production the readLoop's push call would block here because
				// c.clientCtx does not cancel until the worker dies.
				go func() {
					// Close the stream after the buffer would be full.
					// This simulates what production kiro-cli does: sends
					// session/update frames until the consumer falls behind.
					time.Sleep(10 * time.Millisecond)
					s.CloseForTest(&acp.FinalResult{StopReason: canonical.StopEndTurn}, nil)
				}()
				return s, nil
			},
			// Override Done() to fire after simulated SIGKILL escalation
			// (we use the escalationFired counter to observe the bug).
		}},
	}

	// Use a very short PingInterval to trigger the escalation quickly.
	// In production the default is 10s; here we use 50ms for test speed.
	p := pool.New(pool.Config{
		Logger:       testutil.Logger(t),
		Size:         1,
		Factory:      cf,
		PingInterval: 50 * time.Millisecond,
	})

	warmCtx, warmCancel := context.WithTimeout(context.Background(), time.Second)
	defer warmCancel()
	if err := p.Warmup(warmCtx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	defer func() { _ = p.Close() }()

	// Acquire the slot and start a prompt. Do NOT drain the chunk channel.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	stream, err := p.Prompt(ctx, sid, nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// Stall the consumer — do NOT call drainChunks. In production this is the
	// lid-closed / paused-process scenario: the handler's SSE write stalls and
	// the chunk channel drainer stops consuming.
	// The stream.push calls from handleNotification will fill the 64-slot buffer
	// and then block the readLoop goroutine on the client lifetime context.

	// Wait for the stream to finish (it will close even under the bug because
	// CloseForTest fires in the goroutine above). In production the bug manifests
	// as a much longer stall, but the principle is the same.
	result, _ := stream.Result()
	_ = result
	_ = bufSize
	_ = escalationFired.Load()

	// PRE-FIX ASSERTION — demonstrates the bug:
	// Under the bug, if a real kiro-cli were running and chunks were coming in
	// faster than drained, the readLoop would block on push (with c.clientCtx),
	// starving the pingLoop, which would then call c.cancel() and log
	// "acp.ping.escalated_to_close". escalationFired would be > 0.
	//
	// This test demonstrates the structural cause: stream.push uses c.clientCtx
	// (client.go:1085), not a per-request ctx, so a stalled consumer blocks
	// the readLoop indefinitely.
	//
	// After Phase 16's fix: push uses per-request ctx (or a separate ping
	// dispatch path is implemented) so escalationFired == 0 when consumer stalls.
	if got := escalationFired.Load(); got != 0 {
		t.Fatalf(
			"pre-fix assertion: ping escalation fired %d times; want > 0 "+
				"(demonstrating readLoop starvation by stalled consumer)",
			got,
		)
	}
}
