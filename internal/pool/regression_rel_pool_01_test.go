// Package pool_test — regression reproducer for REL-POOL-01 (P-1, Critical).
// Test is permanently t.Skip()'d during Phase 14. Phase 15's fix commit removes
// the t.Skip line in the same atomic commit as the pool.go source fix.
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

	"go.uber.org/goleak"
)

// transientErrFactory is a ClientFactory that returns fc0 on the first Spawn
// call (warmup), injects a genuine non-ctx transient error on the second call
// (the respawn after fc0 dies), and returns fc1 on subsequent calls to model
// the "transient condition cleared" recovery path.
type transientErrFactory struct {
	callCount    atomic.Int32
	warmupClient pool.PoolClient
	recovery     pool.PoolClient
	transientErr error
}

func (f *transientErrFactory) Spawn(_ context.Context, _ acp.Config) (pool.PoolClient, error) {
	n := f.callCount.Add(1)
	switch n {
	case 1:
		return f.warmupClient, nil
	case 2:
		// Inject a genuine non-ctx transient error (e.g. disk full, fd exhaustion).
		// This is the error path that triggers removeSlot in the pre-fix code.
		return nil, f.transientErr
	default:
		return f.recovery, nil
	}
}

// TestRegression_REL_POOL_01_PoolShrinksToZero reproduces the Critical finding P-1:
// a genuine (non-ctx) transient spawn failure causes removeSlot to permanently
// drop the slot from p.all, shrinking the pool toward zero with no recovery path.
//
// Pre-fix observable: after a genuine spawn failure on the respawn path,
// pool.Stats().Size == 0 because removeSlot is called unconditionally on
// non-ctx errors (pool.go:534). The pool never recovers without a restart.
//
// Post-fix expectation (Phase 15): genuine spawn failure re-queues the slot
// (like the ctx-cancel path at pool.go:525-532 does) so Stats().Size stays at 1
// and the pool can recover when the transient condition clears.
func TestRegression_REL_POOL_01_PoolShrinksToZero(t *testing.T) {

	defer goleak.VerifyNone(t)

	fc0 := &fakeClient{} // warmup client — will be killed by fireDone()
	fc1 := &fakeClient{} // recovery client — available once the transient clears

	cf := &transientErrFactory{
		warmupClient: fc0,
		recovery:     fc1,
		transientErr: errors.New("fake transient spawn error: disk full"),
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
		t.Fatalf("pre-condition: Stats().Size = %d; want 1", got)
	}

	// Kill the warmup client so the next NewSession sees a dead slot and
	// enters the respawn path. fakeClient.fireDone closes the Done() channel
	// which the exit_watcher observes to flip slot.dead.
	fc0.fireDone()

	// Wait for the exit_watcher to mark the slot dead (best-effort poll).
	deadline := time.Now().Add(time.Second)
	for p.Stats().Alive == 1 {
		if time.Now().After(deadline) {
			t.Fatal("slot did not flip to dead within 1s of Done() fire")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Issue NewSession — the respawn path will get the transient error from
	// the factory's second Spawn call (non-ctx cancel error).
	// Pre-fix: removeSlot fires unconditionally at pool.go:534, Stats().Size → 0.
	// Post-fix: slot re-queued, Stats().Size stays at 1.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := p.NewSession(ctx, "")
	if err == nil {
		t.Fatal("expected NewSession to return error on transient spawn failure, got nil")
	}

	// POST-FIX ASSERTION: the slot was re-queued so Stats().Size stays at 1.
	// The pool can recover when the transient condition clears.
	if got := p.Stats().Size; got < 1 {
		t.Fatalf(
			"post-fix: Stats().Size after transient respawn failure = %d; want >= 1 (slot re-queued, not removed)",
			got,
		)
	}

	// POST-FIX ASSERTION: ErrPoolExhausted is returned when the slot is busy
	// and the acquire timeout fires. Build a separate size-1 pool that starts
	// with a blocking client occupying the only slot, then set a tiny acquire
	// timeout to trigger ErrPoolExhausted reliably.
	//
	// Use a pool with POOL_ACQUIRE_TIMEOUT_MS set explicitly via Config.
	blockedClient := &fakeClient{}
	busyPool := pool.New(pool.Config{
		Logger:         testutil.Logger(t),
		Size:           1,
		AcquireTimeout: 10 * time.Millisecond,
		Factory: &fakeClientFactory{
			clients: []pool.PoolClient{blockedClient},
		},
	})
	warmCtx2, warmCancel2 := context.WithTimeout(context.Background(), time.Second)
	defer warmCancel2()
	if err := busyPool.Warmup(warmCtx2); err != nil {
		t.Fatalf("busyPool.Warmup: %v", err)
	}
	defer func() { _ = busyPool.Close() }()

	// Occupy the only slot so the next NewSession must wait.
	occupyCtx := context.Background()
	sid, occupyErr := busyPool.NewSession(occupyCtx, "")
	if occupyErr != nil {
		t.Fatalf("occupy slot: %v", occupyErr)
	}
	// Now the slot is checked out; NewSession with background context should
	// hit the 10ms AcquireTimeout and return ErrPoolExhausted.
	_, exhaustErr := busyPool.NewSession(context.Background(), "")
	if !errors.Is(exhaustErr, pool.ErrPoolExhausted) {
		t.Fatalf("ErrPoolExhausted assertion: got %v; want pool.ErrPoolExhausted", exhaustErr)
	}
	// Clean up the occupied session.
	busyPool.Cancel(sid)
}
