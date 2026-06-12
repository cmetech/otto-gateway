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
