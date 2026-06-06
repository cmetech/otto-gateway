package pool_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

// ctxBlockingFactory is a fakeClientFactory variant whose Spawn blocks
// on a gating channel until released, observing ctx for cancellation in
// between. It lets the test simulate a long-running kiro-cli spawn that
// the caller's ctx interrupts mid-flight.
type ctxBlockingFactory struct {
	clients     []pool.PoolClient
	warmupCount atomic.Int32 // pre-warmup Spawns dispense without blocking
	postIdx     atomic.Int32 // post-warmup Spawn index into clients[1:]
	gate        chan struct{} // closed to release post-warmup Spawn
}

func (cf *ctxBlockingFactory) Spawn(ctx context.Context, _ acp.Config) (pool.PoolClient, error) {
	// First call (Warmup) returns immediately so the pool boots cleanly.
	if cf.warmupCount.Add(1) == 1 {
		return cf.clients[0], nil
	}
	// Subsequent calls (the respawn under test) block on the gate OR ctx.
	// A ctx-cancelled call returns without consuming a script entry so the
	// recovery call still has a client available.
	select {
	case <-cf.gate:
		i := int(cf.postIdx.Add(1))
		if i >= len(cf.clients) {
			return nil, errors.New("ctxBlockingFactory: script exhausted")
		}
		return cf.clients[i], nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestPool_RespawnCtxCancel_DoesNotShrinkPool verifies the fix for
// pool-respawn-ctx-cancel-shrinks-pool-permanently. Before the fix,
// caller-disconnect during a dead-slot respawn dropped the slot from
// p.all forever; repeated disconnect-during-respawn walked the pool
// 4→3→2→1→0 with no recovery short of a process restart.
//
// After the fix, ctx-cancellation routes through a re-queue path so the
// slot stays in p.all and the next acquirer retries the respawn.
func TestPool_RespawnCtxCancel_DoesNotShrinkPool(t *testing.T) {
	t.Parallel()

	fc0 := &fakeClient{} // warmup client (will be killed below)
	fc1 := &fakeClient{} // post-respawn replacement (used after recovery)
	cf := &ctxBlockingFactory{
		clients: []pool.PoolClient{fc0, fc1},
		gate:    make(chan struct{}),
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

	if got := p.Stats().Size; got != 1 {
		t.Fatalf("pre-condition: Size = %d; want 1", got)
	}

	// Kill the warmup client so the next NewSession sees a dead slot and
	// enters the respawn path. fakeClient.fireDone closes the Done()
	// channel which the exit_watcher observes to flip slot.dead.
	fc0.fireDone()

	// Wait for exit_watcher to mark the slot dead (best-effort poll).
	deadline := time.Now().Add(time.Second)
	for p.Stats().Alive == 1 {
		if time.Now().After(deadline) {
			t.Fatal("slot did not flip to dead within 1s of Done() fire")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Issue a NewSession with a ctx that we cancel mid-Spawn (simulating
	// HTTP client disconnect during respawn).
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := p.NewSession(ctx, "")
		errCh <- err
	}()
	// Give NewSession time to enter the Spawn block.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("NewSession returned nil err; want ctx-cancellation")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("NewSession err = %v; want errors.Is(context.Canceled)", err)
		}
	case <-time.After(time.Second):
		t.Fatal("NewSession did not return within 1s of cancel")
	}

	// CRITICAL ASSERTION — pool size must be unchanged. Before the fix,
	// Size would now be 0 because NewSession's error branch unconditionally
	// called removeSlot.
	if got := p.Stats().Size; got != 1 {
		t.Fatalf("Stats().Size after disconnect-mid-respawn = %d; want 1 (slot must be re-queued)", got)
	}

	// Recovery: a fresh NewSession with a non-cancelled ctx should pick
	// up the same (still-dead) slot, run the respawn through, and succeed.
	close(cf.gate) // release future Spawn calls
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	sid, err := p.NewSession(ctx2, "")
	if err != nil {
		t.Fatalf("recovery NewSession: %v", err)
	}
	if sid == "" {
		t.Fatal("recovery NewSession: empty sid")
	}
}
