package session_test

import (
	"context"
	"testing"
	"time"

	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// TestRegistry_WatcherFlipsDeadOnDoneFire verifies the per-Entry watcher
// added for finding session-subprocess-crash-leaves-orphan-entry. Before
// the fix, the registry did not observe PoolClient.Done(); a crashed
// kiro-cli subprocess left the Entry returning 500 to every retry of its
// sid for up to the full TTL window (default 30 min).
//
// After the fix, the watcher fires on Client.Done() and marks the entry
// Dead so the next Get on the same sid lazy-creates a fresh subprocess.
func TestRegistry_WatcherFlipsDeadOnDoneFire(t *testing.T) {
	t.Parallel()

	fc1 := &fakeClient{}
	fc2 := &fakeClient{}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc1, fc2}}
	r := session.New(session.Config{
		Logger:  testutil.Logger(t),
		Factory: ff,
	})
	r.Start(context.Background())
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Establish the entry.
	e1, err := r.Get(ctx, "sid-crashy", "/tmp")
	if err != nil {
		t.Fatalf("Get #1: %v", err)
	}
	if e1.Dead {
		t.Fatal("entry Dead immediately after Get")
	}

	// Simulate subprocess crash by firing Done() on the underlying fake.
	// The watcher (spawned by createEntry) should observe Done() and flip
	// e1.Dead under r.mu.
	fc1.doneMu.Lock()
	if fc1.doneCh == nil {
		fc1.doneCh = make(chan struct{})
	}
	close(fc1.doneCh)
	fc1.doneMu.Unlock()

	// Wait for the watcher to react. Bounded poll — race detector friendly.
	deadline := time.Now().Add(time.Second)
	for !entryDeadOrRemoved(t, r, "sid-crashy") {
		if time.Now().After(deadline) {
			t.Fatal("watcher did not flip Dead / remove entry within 1s of Done() fire")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Next Get on the same sid must lazy-create a fresh entry (Dead/removed
	// entries are treated as not-present by Get).
	e2, err := r.Get(ctx, "sid-crashy", "/tmp")
	if err != nil {
		t.Fatalf("Get #2 after crash: %v", err)
	}
	if e2 == e1 {
		t.Fatal("expected fresh entry after crash, got the dead one back")
	}
	if e2.Dead {
		t.Fatal("fresh entry is Dead")
	}

	// fc2 should have been spawned (the second client in the factory script).
	if got := ff.spawnCount(); got != 2 {
		t.Fatalf("spawn count = %d; want 2 (initial + post-crash respawn)", got)
	}
}

// entryDeadOrRemoved peeks at the registry through the Detail() snapshot
// to determine whether the entry is gone or marked Dead. We can't read
// e.Dead directly because the watcher writes it under r.mu and we are
// the racing reader.
func entryDeadOrRemoved(_ *testing.T, r *session.Registry, sid string) bool {
	for _, d := range r.Detail() {
		if d.ID == sid {
			return !d.Alive
		}
	}
	return true
}
