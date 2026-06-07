package session_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// eventually polls predicate every poll-interval until it returns true
// or timeout elapses. Returns true on observed-true, false on timeout.
// Lightweight stand-in for require.Eventually (no testify dependency
// — testify is not vendored).
func eventually(t *testing.T, predicate func() bool, timeout, interval time.Duration, msg string) { //nolint:unparam // timeout param kept polymorphic for future polling tests
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatalf("eventually: %s (timed out after %v)", msg, timeout)
}

// TestReaper_ReapsIdleSessionInRealTime — D-13. TTL=200ms +
// TickInterval=50ms; entry reaped within 1s after becoming idle.
// Real time, no fake clock. This is the deterministic SESS-02
// acceptance test.
func TestReaper_ReapsIdleSessionInRealTime(t *testing.T) {
	fc := &fakeClient{}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc}}
	r := session.New(session.Config{
		Logger:       testutil.Logger(t),
		Factory:      ff,
		TTL:          200 * time.Millisecond,
		TickInterval: 50 * time.Millisecond,
		MaxSessions:  32,
	})
	r.Start(context.Background())
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	e, err := r.Get(ctx, "sid-1", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Backdate LastUsed past the cutoff so the next reaper tick fires.
	// In production MarkUsed runs in a defer at response complete; this
	// test setup matches "entry created, response just finished".
	// Write under e.Mu to match the reaper's TryLock-then-read discipline
	// (D-11/D-12) — without the lock the race detector flags the
	// concurrent test-write vs reaper-read.
	e.Mu.Lock()
	e.LastUsed = time.Now().Add(-500 * time.Millisecond)
	e.Mu.Unlock()

	eventually(t, func() bool {
		return r.SessionCount() == 0
	}, 1*time.Second, 25*time.Millisecond, "expected session to be reaped")

	// fakeClient saw exactly one Cancel + one Close from the reaper.
	if got := fc.cancelCallList(); len(got) != 1 {
		t.Errorf("Cancel calls after reap = %v; want 1", got)
	}
	if got := fc.closeCallCount(); got != 1 {
		t.Errorf("Close calls after reap = %d; want 1", got)
	}
}

// TestReaper_SkipsInFlightSession — D-12. While the entry's Mu is
// locked (in-flight stream), the reaper TryLocks, fails, and skips —
// entry survives the tick. After Mu is released, the next tick reaps
// it (if also past TTL).
func TestReaper_SkipsInFlightSession(t *testing.T) {
	fc := &fakeClient{}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc}}
	r := session.New(session.Config{
		Logger:       testutil.Logger(t),
		Factory:      ff,
		TTL:          100 * time.Millisecond,
		TickInterval: 25 * time.Millisecond,
	})
	r.Start(context.Background())
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	e, err := r.Get(ctx, "sid-busy", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Acquire Mu then backdate — keep the write under the same lock
	// the reaper uses for reads, satisfying the race detector.
	e.Mu.Lock()
	e.LastUsed = time.Now().Add(-500 * time.Millisecond)

	// Hold Mu for 300ms — that's ~12 ticks. The entry must survive
	// every tick during the hold.
	holdStart := time.Now()
	for time.Since(holdStart) < 300*time.Millisecond {
		time.Sleep(25 * time.Millisecond)
		if r.SessionCount() != 1 {
			e.Mu.Unlock()
			t.Fatalf("entry reaped while Mu was held (D-12 broken)")
		}
	}
	e.Mu.Unlock()

	// Now the entry is past TTL and Mu is free: next tick reaps it.
	eventually(t, func() bool {
		return r.SessionCount() == 0
	}, 1*time.Second, 25*time.Millisecond, "entry should be reaped after Mu released")
}

// TestReaper_DoesNotReapRecentlyUsed — D-11. An entry whose LastUsed
// is within the TTL window survives. After waiting past the TTL, it
// gets reaped.
func TestReaper_DoesNotReapRecentlyUsed(t *testing.T) {
	fc := &fakeClient{}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc}}
	r := session.New(session.Config{
		Logger:       testutil.Logger(t),
		Factory:      ff,
		TTL:          200 * time.Millisecond,
		TickInterval: 25 * time.Millisecond,
	})
	r.Start(context.Background())
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	e, err := r.Get(ctx, "sid-recent", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// LastUsed set to now (fresh). Wrap MarkUsed in Mu.Lock/Unlock so
	// the race detector accepts the write against the running reaper's
	// TryLock-then-read.
	e.Mu.Lock()
	e.MarkUsed()
	e.Mu.Unlock()

	// Wait ~100ms (less than TTL): entry survives.
	time.Sleep(100 * time.Millisecond)
	if r.SessionCount() != 1 {
		t.Errorf("entry reaped before TTL elapsed; SessionCount = %d", r.SessionCount())
	}

	// Wait past TTL: entry reaped.
	eventually(t, func() bool {
		return r.SessionCount() == 0
	}, 1*time.Second, 25*time.Millisecond, "entry should be reaped after TTL elapsed")
}

// TestReaper_CancelsAndClosesOnReap — Reaper's terminal sequence:
// Cancel + Close.
func TestReaper_CancelsAndClosesOnReap(t *testing.T) {
	fc := &fakeClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "kiro-cancel-test", nil
		},
	}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc}}
	r := session.New(session.Config{
		Logger:       testutil.Logger(t),
		Factory:      ff,
		TTL:          50 * time.Millisecond,
		TickInterval: 25 * time.Millisecond,
	})
	r.Start(context.Background())
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	e, err := r.Get(ctx, "sid-cc", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Backdate so the next tick reaps. Write under e.Mu so the race
	// detector is happy (reaper reads LastUsed under TryLock).
	e.Mu.Lock()
	e.LastUsed = time.Now().Add(-1 * time.Second)
	e.Mu.Unlock()

	eventually(t, func() bool {
		return r.SessionCount() == 0
	}, 1*time.Second, 25*time.Millisecond, "session should be reaped")

	calls := fc.cancelCallList()
	if len(calls) != 1 || calls[0] != "kiro-cancel-test" {
		t.Errorf("Cancel calls = %v; want [kiro-cancel-test]", calls)
	}
	if got := fc.closeCallCount(); got != 1 {
		t.Errorf("Close calls = %d; want 1", got)
	}
}

// TestReaper_HandlesMultipleEntries — Reaper iterates the snapshot
// correctly: only expired entries are reaped, fresh ones survive.
func TestReaper_HandlesMultipleEntries(t *testing.T) {
	const n = 5
	clients := make([]session.PoolClient, n)
	for i := 0; i < n; i++ {
		clients[i] = &fakeClient{}
	}
	ff := &fakeClientFactory{clients: clients}
	r := session.New(session.Config{
		Logger:       testutil.Logger(t),
		Factory:      ff,
		TTL:          200 * time.Millisecond,
		TickInterval: 1 * time.Hour, // ticker effectively disabled — drive via ReapOnceForTest
	})
	// We do NOT call Start — we drive reapOnce synchronously via the
	// export_test seam.
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	entries := make([]*session.Entry, n)
	for i := 0; i < n; i++ {
		e, err := r.Get(ctx, sidName(i), "/tmp")
		if err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
		entries[i] = e
	}
	// Backdate the first 3 entries past the TTL cutoff; leave the
	// last 2 fresh.
	pastCutoff := time.Now().Add(-1 * time.Second)
	freshNow := time.Now()
	for i := 0; i < 3; i++ {
		entries[i].LastUsed = pastCutoff
	}
	for i := 3; i < n; i++ {
		entries[i].LastUsed = freshNow
	}

	r.ReapOnceForTest()

	if got := r.SessionCount(); got != 2 {
		t.Errorf("SessionCount after one reapOnce = %d; want 2 (3 expired, 2 fresh)", got)
	}
}

// sidName generates a deterministic session id from an index.
func sidName(i int) string { return "sid-" + string(rune('A'+i)) }

// TestReaper_ExitsOnRegistryClose — Pitfall 5. Reaper with a very
// long TickInterval (1h) must still exit promptly on Close — the
// outer select-on-closing branch.
func TestReaper_ExitsOnRegistryClose(t *testing.T) {
	r := session.New(session.Config{
		Logger:       testutil.Logger(t),
		TickInterval: 1 * time.Hour, // production-ish; would never tick during test
		TTL:          1 * time.Hour,
	})
	r.Start(context.Background())

	closeDone := make(chan error, 1)
	t0 := time.Now()
	go func() {
		closeDone <- r.Close()
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned err: %v", err)
		}
		if elapsed := time.Since(t0); elapsed > 100*time.Millisecond {
			t.Errorf("Close took %v; want <100ms (Pitfall 5 bounded shutdown)", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close did not return within 500ms — reaper goroutine leak (Pitfall 5)")
	}
}

// TestReaper_DeadlockFree_ReverseLockOrder — anti-pattern verification.
// One goroutine holds entry.Mu and calls Registry.Delete in a loop;
// another goroutine calls Registry.Get repeatedly; the reaper ticks
// every 5ms. After 1s of contention, the test must complete without
// hanging — proving the snapshot-then-iterate discipline prevents the
// reverse-lock-order deadlock.
func TestReaper_DeadlockFree_ReverseLockOrder(t *testing.T) {
	const numClients = 50
	clients := make([]session.PoolClient, numClients)
	for i := 0; i < numClients; i++ {
		clients[i] = &fakeClient{}
	}
	ff := &fakeClientFactory{clients: clients}
	r := session.New(session.Config{
		Logger:       testutil.Logger(t),
		Factory:      ff,
		TTL:          5 * time.Millisecond,
		TickInterval: 5 * time.Millisecond,
		MaxSessions:  100,
	})
	r.Start(context.Background())
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	var wg sync.WaitGroup
	var stop atomic.Bool

	// goroutine 1: surface-handler-style — Lock entry.Mu, then call
	// Registry.Delete. Mimics the "Delete while mid-stream" race.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			e, err := r.Get(ctx, "sid-fight", "/tmp")
			if err != nil {
				return
			}
			e.Mu.Lock()
			_ = r.Delete("sid-fight")
			e.Mu.Unlock()
		}
	}()

	// goroutine 2: repeated Get/Delete on a different sid.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			_, _ = r.Get(ctx, "sid-other", "/tmp")
			_ = r.Delete("sid-other")
		}
	}()

	// Run for 750ms then signal stop.
	go func() {
		time.Sleep(750 * time.Millisecond)
		stop.Store(true)
		close(done)
	}()

	select {
	case <-done:
		wg.Wait()
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock detected — Reaper/Get/Delete contention did not complete in 2s")
	}
}

// TestReaper_DoesNotReapDuringCreation — entries with creating=true
// have no Client yet and a zero LastUsed; they must NOT be reaped by
// the snapshot iteration. createEntry-in-flight is bounded by the
// Spawn duration which is < TickInterval in practice.
func TestReaper_DoesNotReapDuringCreation(t *testing.T) {
	// Use a slow Initialize to keep the entry in the creating state
	// across at least one tick.
	released := make(chan struct{})
	fc := &fakeClient{
		initializeFn: func(_ context.Context) error {
			<-released
			return nil
		},
	}
	ff := &fakeClientFactory{clients: []session.PoolClient{fc}}
	r := session.New(session.Config{
		Logger:       testutil.Logger(t),
		Factory:      ff,
		TTL:          10 * time.Millisecond, // very aggressive
		TickInterval: 5 * time.Millisecond,
	})
	r.Start(context.Background())
	defer func() {
		// Release Initialize so Get unblocks before Close.
		select {
		case <-released:
		default:
			close(released)
		}
		_ = r.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	getDone := make(chan struct{})
	go func() {
		_, _ = r.Get(ctx, "sid-creating", "/tmp")
		close(getDone)
	}()

	// Wait long enough that several reaper ticks fire while Initialize
	// is blocked. The placeholder entry has zero LastUsed which would
	// trigger the cutoff — but creating=true skips it.
	time.Sleep(50 * time.Millisecond)
	if r.SessionCount() != 1 {
		t.Errorf("SessionCount during create = %d; want 1 (placeholder preserved)", r.SessionCount())
	}

	close(released) // let createEntry finish
	<-getDone
}
