// Package pool — whitebox tests for the exit-watcher goroutine (D-01).
// Lives in `package pool` (not pool_test) so it can construct *Slot and
// invoke startExitWatcher directly without round-tripping through Warmup.
package pool

import (
	"context"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/testutil"
)

// watcherTestClient is a minimal PoolClient implementation for whitebox
// exit-watcher tests. Only Done() and Close() are exercised; the rest
// short-circuit to default-zero values so the watcher's two-branch
// select is the entire surface under test.
type watcherTestClient struct {
	doneCh chan struct{}
}

func newWatcherTestClient() *watcherTestClient {
	return &watcherTestClient{doneCh: make(chan struct{})}
}

func (w *watcherTestClient) Initialize(_ context.Context) error { return nil }
func (w *watcherTestClient) NewSession(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (w *watcherTestClient) SetModel(_ context.Context, _, _ string) error { return nil }
func (w *watcherTestClient) Prompt(_ context.Context, _ string, _ []canonical.Block) (*acp.Stream, error) {
	return nil, nil
}
func (w *watcherTestClient) Cancel(_ string)                        {}
func (w *watcherTestClient) Close() error                           { return nil }
func (w *watcherTestClient) AvailableModels() []canonical.ModelInfo { return nil }
func (w *watcherTestClient) Done() <-chan struct{}                  { return w.doneCh }
func (w *watcherTestClient) Pid() int                               { return 0 }

// TestExitWatcher_FiresOnClientDone — D-01: closing the slot's client
// Done() channel must flip slot.dead to true within 100ms.
func TestExitWatcher_FiresOnClientDone(t *testing.T) {
	p := New(Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: nil, // unused — we construct the slot manually
	})
	defer func() { _ = p.Close() }()

	wc := newWatcherTestClient()
	slot := &Slot{Label: "watcher-test-0", Client: wc}

	// WR-01: callers now capture Done() at spawn time. Recycle-race fix:
	// callers also pass the watched client for the planned-teardown identity
	// check; here the slot's client and the watched client are the same.
	p.startExitWatcher(slot, wc, wc.Done())

	// Trigger client death.
	close(wc.doneCh)

	// Wait up to 100ms for slot.dead to flip.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		dead := slot.dead
		p.mu.Unlock()
		if dead {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("slot.dead did not flip to true within 100ms after Done() fired")
}

// TestExitWatcher_ExitsOnPoolClose — D-01 invariant: the watcher
// goroutine MUST exit cleanly when Pool.Close fires (close(p.closing)).
// goleak (testmain_test.go) enforces no-leak globally; this test also
// asserts that slot.dead does NOT flip on the close path.
func TestExitWatcher_ExitsOnPoolClose(t *testing.T) {
	p := New(Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: nil,
	})

	wc := newWatcherTestClient()
	slot := &Slot{Label: "watcher-close-0", Client: wc}

	// WR-01: callers now capture Done() at spawn time. Recycle-race fix: pass
	// the watched client for the planned-teardown identity check.
	p.startExitWatcher(slot, wc, wc.Done())

	// Trigger pool shutdown; watcher should pick <-p.closing branch.
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Give the goroutine 50ms to exit; the goleak check at suite teardown
	// is the authoritative leak detector.
	time.Sleep(50 * time.Millisecond)

	// slot.dead should NOT have flipped — the watcher exited via
	// <-p.closing without touching the field.
	p.mu.Lock()
	if slot.dead {
		t.Error("slot.dead flipped to true on Pool.Close path — watcher took the wrong branch")
	}
	p.mu.Unlock()
}
