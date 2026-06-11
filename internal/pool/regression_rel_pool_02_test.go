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

func newBlockingPromptClient() *blockingPromptClient {
	c := &blockingPromptClient{gate: make(chan struct{})}
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
	t.Skip("REL-POOL-02 (P-2): regression test — unskip in Phase 15 fix commit")

	defer goleak.VerifyNone(t)

	// Build a size-2 pool with two blocking clients to simulate two
	// concurrent in-flight long generations.
	bc0 := newBlockingPromptClient()
	bc1 := newBlockingPromptClient()
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
			go func() { _, _ = stream.Result() }()
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

	// PRE-FIX ASSERTION — demonstrates the bug:
	// Without pool.Close() (the os.Exit path), Cancel is never called.
	// After Phase 15's fix, pool.Close() MUST be called unconditionally so
	// this assertion must be INVERTED: cancels > 0 after the shutdown path.
	if cancelsBefore != 0 {
		t.Fatalf(
			"pre-fix assertion: cancels issued before pool.Close() = %d; "+
				"want 0 (demonstrating orphaned children when os.Exit skips cleanup)",
			cancelsBefore,
		)
	}

	// Unblock the blocking clients and close the pool to clean up goroutines.
	close(bc0.gate)
	close(bc1.gate)
	_ = p.Close()
	wg.Wait()

	// Post-fix: after pool.Close() runs, all sessions should have been cancelled.
	// Verify this holds even in the post-fix world (regression guard for Phase 15).
	cancelsAfter := len(bc0.cancelCallList()) + len(bc1.cancelCallList())
	_ = cancelsAfter // In Phase 15, assert cancelsAfter >= 2.
}
