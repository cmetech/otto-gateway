// Package pool — regression for REL-HTTP-07 (D-18-07) at the pool's
// two background goroutine sites:
//   - "pool-ctx-watcher" — internal/pool/pool.go per-session ctx-watcher
//     goroutine spawned inside Prompt (~line 886).
//   - "pool-exit-watcher" — internal/pool/exit_watcher.go startExitWatcher
//     goroutine body.
//
// Pre-fix: a panic in either goroutine would propagate out of the
// runtime and crash the gateway. Post-fix: a defer-recover converts the
// panic into a single slog.Error("goroutine panic recovered", site=...).
//
// Whitebox: tests live in package pool (not pool_test) so they can set
// the package-private probe seams.
//
// Phase 18 Plan 02 — Task 2 Part B Sites (b) and (c).
package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
)

// syncBuf is a goroutine-safe slog destination — slog handlers may
// write from a panicking goroutine while the test goroutine reads
// .String(), which trips the race detector without this guard.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// findRecord_REL_HTTP_07 returns the first slog record with
// msg="goroutine panic recovered" and matching site, or nil.
func findRecord_REL_HTTP_07(t *testing.T, buf *syncBuf, site string) map[string]any { //nolint:revive // underscore matches REL-HTTP-07 id
	t.Helper()
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		if msg, _ := rec["msg"].(string); msg != "goroutine panic recovered" {
			continue
		}
		if s, _ := rec["site"].(string); s == site {
			return rec
		}
	}
	return nil
}

// awaitPanicRecord polls buf until a matching panic-recovered record
// appears or 2s elapses.
func awaitPanicRecord(t *testing.T, buf *syncBuf, site string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec := findRecord_REL_HTTP_07(t, buf, site); rec != nil {
			return rec
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no panic-recovered record with site=%q within 2s; buf=%s", site, buf.String())
	return nil
}

// assertPanicShape checks level=ERROR, panic substring, stack non-empty.
func assertPanicShape(t *testing.T, rec map[string]any, wantSubstr string) {
	t.Helper()
	if lvl, _ := rec["level"].(string); lvl != "ERROR" {
		t.Errorf("level = %q, want ERROR", lvl)
	}
	if p, _ := rec["panic"].(string); !strings.Contains(p, wantSubstr) {
		t.Errorf("panic = %q, want substring %q", p, wantSubstr)
	}
	if s, _ := rec["stack"].(string); s == "" {
		t.Errorf("stack field empty; want debug.Stack output")
	}
}

// TestRegression_REL_HTTP_07_PoolCtxWatcher exercises the per-session
// ctx-watcher goroutine spawned inside Pool.Prompt. The test:
//  1. Installs ctxWatcherPanicProbe.
//  2. Warms a Size:1 pool with a fakeWatcherTestClient.
//  3. Calls Prompt to spawn the watcher.
//  4. Asserts the panic-recovered log appears.
//
// Whitebox so it can both set the probe and call internal Prompt/Warmup.
func TestRegression_REL_HTTP_07_PoolCtxWatcher(t *testing.T) {
	buf := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	restore := SetCtxWatcherPanicProbeForTest(func() { panic("test-18-02-pool-ctx-watcher") })
	t.Cleanup(restore)

	// Build a minimal PoolClient whose Prompt returns a closed stream.
	wc := newPanicTestClient()
	p := New(Config{
		Logger:  logger,
		Size:    1,
		Factory: &panicTestFactory{client: wc},
	})
	defer func() { _ = p.Close() }()

	warmCtx, warmCancel := context.WithTimeout(context.Background(), time.Second)
	defer warmCancel()
	if err := p.Warmup(warmCtx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_, err = p.Prompt(ctx, sid, []canonical.Block{{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "hi"}}})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	rec := awaitPanicRecord(t, buf, "pool-ctx-watcher")
	assertPanicShape(t, rec, "test-18-02-pool-ctx-watcher")
}

// TestRegression_REL_HTTP_07_PoolExitWatcher drives the exit-watcher
// goroutine directly (no Pool plumbing needed beyond startExitWatcher).
func TestRegression_REL_HTTP_07_PoolExitWatcher(t *testing.T) {
	buf := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	restore := SetExitWatcherPanicProbeForTest(func() { panic("test-18-02-pool-exit-watcher") })
	t.Cleanup(restore)

	p := New(Config{
		Logger:  logger,
		Size:    1,
		Factory: nil, // not used; we construct the slot manually.
	})
	defer func() { _ = p.Close() }()

	wc := newPanicTestClient()
	slot := &Slot{Label: "panic-test-0", Client: wc}
	p.startExitWatcher(slot, wc, wc.Done())

	rec := awaitPanicRecord(t, buf, "pool-exit-watcher")
	assertPanicShape(t, rec, "test-18-02-pool-exit-watcher")
}

// ---------------------------------------------------------------------------
// panicTestClient / panicTestFactory — minimal PoolClient + ClientFactory
// implementations for the REL-HTTP-07 whitebox tests. Mirrors the
// watcherTestClient pattern from exit_watcher_test.go (Done() + no-op
// everything else) but adds a non-nil Prompt return so the ctx-watcher
// goroutine actually launches.
// ---------------------------------------------------------------------------

type panicTestClient struct {
	doneCh chan struct{}
}

func newPanicTestClient() *panicTestClient {
	return &panicTestClient{doneCh: make(chan struct{})}
}

func (p *panicTestClient) Initialize(_ context.Context) error { return nil }
func (p *panicTestClient) NewSession(_ context.Context, _ string) (string, error) {
	return "panic-test-sess", nil
}
func (p *panicTestClient) SetModel(_ context.Context, _, _ string) error { return nil }

// Prompt returns a freshly-closed *acp.Stream so the per-session
// ctx-watcher goroutine in Pool.Prompt has something to bind to.
func (p *panicTestClient) Prompt(_ context.Context, sid string, _ []canonical.Block) (*acp.Stream, error) {
	s := acp.NewStreamForTest(sid)
	s.CloseForTest(&acp.FinalResult{StopReason: canonical.StopEndTurn}, nil)
	return s, nil
}
func (p *panicTestClient) Cancel(_ string)                        {}
func (p *panicTestClient) Close() error                           { return nil }
func (p *panicTestClient) AvailableModels() []canonical.ModelInfo { return nil }
func (p *panicTestClient) Done() <-chan struct{}                  { return p.doneCh }
func (p *panicTestClient) Pid() int                               { return 0 }

type panicTestFactory struct {
	client PoolClient
}

func (f *panicTestFactory) Spawn(_ context.Context, _ acp.Config) (PoolClient, error) {
	return f.client, nil
}
