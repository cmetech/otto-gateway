// Package session_test — regression reproducer for REL-POOL-05 (P-5, Medium).
// Test is permanently t.Skip()'d during Phase 14. Phase 16's fix commit removes
// the t.Skip line in the same atomic commit as the registry.go/entry_acp.go fix.
package session_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"

	"go.uber.org/goleak"
)

// TestRegression_REL_POOL_05_LastUsedRace reproduces Medium finding P-5:
// Entry.LastUsed is written under different locks in different places,
// creating a data race that go test -race detects:
//
//   - Registry.Get writes e.LastUsed = time.Now() at registry.go:206 under r.mu only
//   - Entry.MarkUsed writes e.LastUsed = time.Now() at entry_acp.go:77-79 under e.Mu only
//   - registry.go:358 reads e.LastUsed in watchEntry with NO lock
//
// When Registry.Get (holding r.mu) and MarkUsed (holding e.Mu) execute
// concurrently on the same entry, there is a write/write race on the
// multi-word time.Time value. The reaper can observe a torn value and
// mis-evaluate LastUsed.Before(cutoff), potentially reaping a just-used
// session and killing the dedicated kiro-cli subprocess.
//
// Pre-fix observable: `go test -race` reports a DATA RACE on Entry.LastUsed
// when Get and MarkUsed execute concurrently on the same session ID.
//
// Post-fix expectation (Phase 16): LastUsed stored as atomic.Int64 of unix nanos
// (or all sites locked under e.Mu consistently) so all four read/write sites
// are race-free.
func TestRegression_REL_POOL_05_LastUsedRace(t *testing.T) {
	t.Skip("REL-POOL-05 (P-5): regression test — unskip in Phase 16 fix commit")

	defer goleak.VerifyNone(t)

	// Build a registry matching the reaper_test.go template (TTL=200ms,
	// TickInterval=50ms, MaxSessions=32).
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

	// Create an entry to race on.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e, err := r.Get(ctx, "race-sid", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Fire N goroutines tight-looping Get + MarkUsed concurrently on the
	// same session ID. Under go test -race, this surfaces the DATA RACE
	// on e.LastUsed (write/write race between registry.go:206 under r.mu
	// and entry_acp.go:77-79 under e.Mu).
	const goroutines = 64
	const iterations = 50

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Registry.Get writes e.LastUsed under r.mu.
				_, _ = r.Get(ctx, "race-sid", "/tmp")
				// MarkUsed writes e.LastUsed under e.Mu.
				e.MarkUsed()
			}
		}()
	}
	wg.Wait()

	// The test itself asserts nothing beyond "no DATA RACE under -race".
	// The pre-fix observable is a race report from go test -race; the
	// post-fix invariant is clean -race output (no DATA RACE line).
}
