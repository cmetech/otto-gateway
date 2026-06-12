// Package admin — regression for REL-HTTP-07 admin-tailer panic recovery.
//
// Pre-fix: a panic anywhere inside the Tailer.run() goroutine would
// propagate out of the runtime and crash the gateway (net/http's
// per-handler recover does NOT cover background goroutines).
//
// Post-fix: the run() body has a defer-recover that emits exactly one
// slog.Error("goroutine panic recovered", site="admin-tailer", ...)
// with the panic value and a runtime/debug.Stack() snapshot.
//
// Phase 18 Plan 02 — Task 2 Part B Site (a).
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuf is a goroutine-safe wrapper around bytes.Buffer for slog
// handlers — slog writes from background goroutines while the test
// goroutine reads buf.String(), which trips the race detector without
// this guard.
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

// findPanicRecord returns the first decoded slog record with
// msg="goroutine panic recovered" and the matching site, or nil.
func findPanicRecord(t *testing.T, buf *syncBuf, site string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode slog record %q: %v", line, err)
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

// TestRegression_REL_HTTP_07_AdminTailer drives the adminTailerPanicProbe
// seam and asserts the structured Error record is emitted with site
// byte-exact "admin-tailer".
func TestRegression_REL_HTTP_07_AdminTailer(t *testing.T) {
	buf := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Install the probe; restore on test exit.
	restore := SetAdminTailerPanicProbeForTest(func() { panic("test-18-02-admin-tailer") })
	t.Cleanup(restore)

	tail := NewTailer("/nonexistent/path/never-opens", logger)

	// Subscribe lazy-starts the goroutine; the probe fires; the
	// defer-recover swallows; the goroutine exits.
	sub := tail.Subscribe(context.Background())
	t.Cleanup(func() { tail.Unsubscribe(sub) })

	// Poll for the panic-recovered record.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec := findPanicRecord(t, buf, "admin-tailer"); rec != nil {
			if lvl, _ := rec["level"].(string); lvl != "ERROR" {
				t.Errorf("level = %q, want ERROR", lvl)
			}
			if p, _ := rec["panic"].(string); p == "" || !strings.Contains(p, "test-18-02-admin-tailer") {
				t.Errorf("panic field = %q, want substring %q", p, "test-18-02-admin-tailer")
			}
			if s, _ := rec["stack"].(string); s == "" {
				t.Errorf("stack field empty; want runtime/debug.Stack output")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no 'goroutine panic recovered' record with site=admin-tailer within 2s; buf=%s", buf.String())
}

// TestRegression_REL_HTTP_07_AdminTailer_LazyRestartAfterPanic asserts the
// CR-01 fix: after the run() goroutine recovers from a panic, t.running
// must be reset to false so a subsequent Subscribe lazy-starts a fresh
// tailer goroutine — per the docstring at run()'s defer-recover
// ("a subsequent Subscribe will lazy-start a fresh tailer goroutine.
// No restart / spin loop.").
//
// Pre-fix: t.running stays true forever after a panic; a subsequent
// Subscribe appends to t.subscribers but never spawns a broadcaster, so
// the admin Log Tail panel goes permanently dark until gateway restart.
func TestRegression_REL_HTTP_07_AdminTailer_LazyRestartAfterPanic(t *testing.T) {
	buf := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// First Subscribe: install a one-shot panic probe so only the FIRST
	// run() goroutine panics. The probe restore inside the swap below is
	// invoked synchronously inside the goroutine, but we replace the
	// probe with a no-op BEFORE the second Subscribe so the second
	// goroutine runs cleanly.
	restore := SetAdminTailerPanicProbeForTest(func() { panic("test-18-CR-01-lazy-restart") })
	t.Cleanup(restore)

	tail := NewTailer("/nonexistent/path/never-opens", logger)

	// First Subscribe — lazy-starts the goroutine; the probe fires; the
	// defer-recover swallows; the goroutine exits.
	sub1 := tail.Subscribe(context.Background())

	// Wait for the panic-recovered record before swapping the probe so
	// we know the first goroutine has returned (the panic-recovered log
	// is emitted inside the defer, immediately before the goroutine's
	// final return).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if findPanicRecord(t, buf, "admin-tailer") != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if findPanicRecord(t, buf, "admin-tailer") == nil {
		t.Fatalf("first Subscribe did not produce a 'goroutine panic recovered' record within 2s; buf=%s", buf.String())
	}

	// Defensive: give the deferred t.running reset a moment to complete
	// after the slog.Error write. Both happen in the same defer; reading
	// t.running here without yielding could race with the defer's
	// t.mu.Unlock. Subscribe acquires t.mu so it will serialize naturally,
	// but a tiny sleep makes the intent obvious without busy-loop noise.
	time.Sleep(20 * time.Millisecond)

	// CRITICAL: do NOT Unsubscribe sub1 before the second Subscribe.
	// Unsubscribe also resets t.running=false when subscribers drop to 0,
	// which would mask the CR-01 bug. The bug is that after a panic, with
	// the first subscriber still attached, t.running stays true forever
	// and any subsequent Subscribe silently appends to t.subscribers
	// without spawning a new broadcaster. This test deliberately keeps
	// sub1 attached so the second Subscribe path exercises the
	// post-panic state directly.
	t.Cleanup(func() { tail.Unsubscribe(sub1) })

	// Second Subscribe — this is the load-bearing assertion. If
	// t.running was not reset in the deferred recover, Subscribe will
	// see running=true, append to t.subscribers, and NOT spawn a new
	// goroutine. We assert the second goroutine actually starts by
	// confirming the probe fires a second time — which it can only do
	// if the goroutine runs.
	//
	// We detect the second probe fire by swapping in a channel-signaling
	// probe just before Subscribe.
	probeFired := make(chan struct{}, 1)
	restore3 := SetAdminTailerPanicProbeForTest(func() {
		select {
		case probeFired <- struct{}{}:
		default:
		}
	})
	t.Cleanup(restore3)

	sub2 := tail.Subscribe(context.Background())
	t.Cleanup(func() { tail.Unsubscribe(sub2) })

	select {
	case <-probeFired:
		// Pass: the second Subscribe lazy-started a fresh goroutine that
		// invoked the probe, proving t.running was reset in the deferred
		// recover.
	case <-time.After(2 * time.Second):
		t.Fatalf("second Subscribe did not spawn a new tailer goroutine within 2s; "+
			"t.running likely stayed true after the panic-recover (CR-01 regressed); buf=%s",
			buf.String())
	}
}
