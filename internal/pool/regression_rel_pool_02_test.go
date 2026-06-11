// Package pool_test — regression reproducer for REL-POOL-02 (P-2, High).
// Test is permanently t.Skip()'d during Phase 14. Phase 15's fix commit removes
// the t.Skip line in the same atomic commit as the main.go source fix.
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

// blockingPromptClient is a fakeClient whose Prompt blocks until unblocked,
// simulating an in-flight long generation. Used to represent a session that
// was mid-stream when Ctrl-C arrived.
type blockingPromptClient struct {
	fakeClient
	gate chan struct{} // close to unblock Prompt
}

// newBlockingPromptClient builds a blockingPromptClient whose NewSession
// returns a per-instance unique session ID — the shared default fakeClient
// behaviour returns "fake-sess" for every call, which causes both clients
// in this test to register the SAME sid in pool.sessionSlots; the second
// NewSession overwrites the first entry and both subsequent Prompt calls
// route to whichever client won the overwrite race (deviation Rule 1 fix,
// Plan 17-02 — exposed by iter1 resultWg.Wait() draining surfacing the
// pre-existing degenerate sessionSlots collapse via the WR-04 assertion).
// idTag is appended to "fake-sess-" so each instance produces a stable,
// distinguishable session ID across the test's two concurrent Prompts.
func newBlockingPromptClient(idTag string) *blockingPromptClient {
	c := &blockingPromptClient{gate: make(chan struct{})}
	sid := "fake-sess-" + idTag
	c.newSessionFn = func(_ context.Context, _ string) (string, error) {
		return sid, nil
	}
	c.promptFn = func(ctx context.Context, sid string, blocks []canonical.Block) (*acp.Stream, error) {
		s := acp.NewStreamForTest(sid)
		go func() {
			select {
			case <-c.gate:
				// Unblocked — close the stream normally.
				s.CloseForTest(&acp.FinalResult{StopReason: canonical.StopEndTurn}, nil)
			case <-ctx.Done():
				s.CloseForTest(nil, ctx.Err())
			}
		}()
		return s, nil
	}
	return c
}

// TestRegression_REL_POOL_02_CtrlCOrphansChildren reproduces High finding P-2:
// when RunUntilSignal returns an error (30s grace period expires with in-flight
// streams), main.go calls os.Exit(1) which SKIPS the deferred cleanup() at
// main.go:127. Therefore pool.Close() never runs and Cancel() is never called
// on any in-flight session — kiro-cli children are orphaned.
//
// The reproducer drives pool.Close() from a goroutine simulating the signal
// handler path and asserts that Cancel() was called on all in-flight sessions.
//
// Pre-fix observable: when the shutdown path bypasses pool.Close() (os.Exit),
// cancelCallList() returns empty — no Cancel calls were issued.
//
// Post-fix expectation (Phase 15): pool.Close() runs on ALL exit paths
// (deferred cleanup is unconditional, or main.go:131's os.Exit is replaced
// with cleanup(); closeLogger(); os.Exit(1)).
func TestRegression_REL_POOL_02_CtrlCOrphansChildren(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Build a size-2 pool with two blocking clients to simulate two
	// concurrent in-flight long generations.
	bc0 := newBlockingPromptClient("bc0")
	bc1 := newBlockingPromptClient("bc1")
	cf := &fakeClientFactory{
		clients: []pool.PoolClient{bc0, bc1},
	}

	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    2,
		Factory: cf,
	})

	warmCtx, warmCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer warmCancel()
	if err := p.Warmup(warmCtx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}

	// Start two concurrent in-flight Prompt calls that block (simulating
	// long LLM generations running when Ctrl-C arrives).
	var wg sync.WaitGroup
	// resultWg tracks the orphan stream.Result() goroutines spawned per
	// session below. Without this WaitGroup, those goroutines block in
	// acp.(*Stream).Result on the stream's done channel; under -race the
	// outer wg.Wait() can complete while these goroutines are still pending
	// stream-close, causing the deferred goleak.VerifyNone(t) at line 62 to
	// fail with a "chan receive" leak (~17/17 iterations under -count=20).
	// Plan 17-02 / D-17-04 iter 1.
	var resultWg sync.WaitGroup
	sessions := make([]string, 0, 2)
	var sessionsMu sync.Mutex

	for range []*blockingPromptClient{bc0, bc1} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			sid, err := p.NewSession(ctx, "")
			if err != nil {
				t.Errorf("NewSession: %v", err)
				return
			}
			sessionsMu.Lock()
			sessions = append(sessions, sid)
			sessionsMu.Unlock()
			stream, err := p.Prompt(ctx, sid, nil)
			if err != nil {
				t.Errorf("Prompt: %v", err)
				return
			}
			// Drain Chunks first, THEN call Result. The drain blocks until
			// acp.Stream.close's `close(s.chunks)` runs at stream.go:186 —
			// which is AFTER the StopReason write at stream.go:182 and
			// after s.mu.Unlock() at stream.go:185. Once Chunks observes
			// channel close, the close() body has fully executed (write
			// barrier on chan-close). Calling Result after the drain
			// avoids the close-vs-read race that triggered when Result
			// was called with only <-s.done synchronization (s.done is
			// closed BEFORE s.mu, so Result waiters can return the
			// FinalResult pointer before close writes StopReason —
			// poolStreamWrapper.Result then reads StopReason at pool.go:959
			// while close is still mutating it; `go test -race` flags this).
			// Result still fires the wrapper's releaseOnce → cancelWatch
			// → close(doneCh) so the ctx-watcher goroutine spawned at
			// pool.go:859 exits cleanly. Plan 17-02 / D-17-04 iter 1.
			resultWg.Add(1)
			go func() {
				defer resultWg.Done()
				for range stream.Chunks() {
					// drain; producer closes on stream close. The for-range
					// exit is the synchronization edge with close()'s body
					// completion — StopReason is now safely readable.
				}
				_, _ = stream.Result()
			}()
		}()
	}

	// Wait for both sessions to be established.
	time.Sleep(100 * time.Millisecond)

	// The pre-fix behaviour: os.Exit(1) at main.go:131 skips deferred cleanup(),
	// so pool.Close() is never called. We simulate this by checking what would
	// have happened to cancelCallList() WITHOUT calling pool.Close().
	// Under the bug, at this point bc0.cancelCallList() and bc1.cancelCallList()
	// are both empty (no Cancel was issued to either client).
	cancelsBefore := len(bc0.cancelCallList()) + len(bc1.cancelCallList())

	// The pre-fix path (os.Exit skips cleanup) would leave cancelsBefore == 0.
	// We record it for diagnostic output but do NOT assert on it here —
	// the post-fix assertion (cancelsAfter >= 2) is the load-bearing check.
	t.Logf("cancels before pool.Close(): %d (expected 0 on both pre- and post-fix paths)", cancelsBefore)

	// Simulate the shutdown path: call pool.Close() explicitly (as the
	// post-fix main.go does before os.Exit(1) via explicit cleanup()).
	// Close cancels all in-flight sessions by calling Cancel on each
	// slot's client via closeAll.
	_ = p.Close()

	// Give sessions a moment to receive the Cancel signal.
	time.Sleep(50 * time.Millisecond)

	// POST-FIX ASSERTION: pool.Close() must have issued Cancel to both clients.
	cancelsAfter := len(bc0.cancelCallList()) + len(bc1.cancelCallList())
	if cancelsAfter < 2 {
		t.Fatalf(
			"post-fix: cancels after pool.Close() = %d; want >= 2 "+
				"(pool.Close must cancel all in-flight sessions)",
			cancelsAfter,
		)
	}
	// WR-04 fix (phase 15 review): tighten the assertion to require EACH
	// client received its OWN Cancel. The pre-WR-04 form (cancelsAfter >= 2)
	// would pass if one client saw both cancels and the other saw none —
	// a regression where closeAll iterates sessionSlots in a way that
	// double-cancels the same session would not be caught.
	bc0Cancels := bc0.cancelCallList()
	bc1Cancels := bc1.cancelCallList()
	if len(bc0Cancels) == 0 || len(bc1Cancels) == 0 {
		t.Fatalf(
			"post-fix: expected each fake client to receive at least one Cancel; "+
				"bc0=%v bc1=%v",
			bc0Cancels, bc1Cancels,
		)
	}
	t.Logf("cancels after pool.Close(): %d (bc0=%d bc1=%d, both must be > 0)",
		cancelsAfter, len(bc0Cancels), len(bc1Cancels))

	// Unblock the blocking clients so goroutines can exit cleanly.
	// Order matters for goleak: gates → wg.Wait drains the outer session
	// loop, then resultWg.Wait drains the orphan stream.Result() goroutines
	// AFTER each stream has fully closed (either via gate-path FinalResult
	// or via ctx.Done from p.Close above). Only then does the deferred
	// goleak.VerifyNone(t) fire on a clean goroutine set. Plan 17-02 / D-17-04.
	close(bc0.gate)
	close(bc1.gate)
	wg.Wait()
	resultWg.Wait()
}
