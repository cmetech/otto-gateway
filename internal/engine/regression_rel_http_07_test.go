// Package engine — regression for REL-HTTP-07 (D-18-07) at the
// "engine-after-func" site: the context.AfterFunc callback at
// engine.go:255-265 that fires the engine-owned watchdog.
//
// Pre-fix: a panic inside the AfterFunc callback would propagate out
// of the Go runtime's AfterFunc machinery and crash the gateway.
//
// Post-fix: a defer-recover at the top of the callback body converts
// the panic into exactly one slog.Error("goroutine panic recovered",
// site="engine-after-func", panic=..., stack=...).
//
// Phase 18 Plan 02 — Task 2 Part B Site (d).
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// TestRegression_REL_HTTP_07_EngineAfterFunc installs the
// afterFuncPanicProbe, builds an Engine + fakeACP harness, calls Run to
// register the AfterFunc, then cancels the request ctx to fire the
// callback. The probe panics inside the callback; the defer-recover
// emits the structured Error log.
func TestRegression_REL_HTTP_07_EngineAfterFunc(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	prev := afterFuncPanicProbe
	t.Cleanup(func() { afterFuncPanicProbe = prev })
	afterFuncPanicProbe = func() { panic("test-18-02-engine-after-func") }

	ack := &fakeACP{}
	e := New(Config{
		Logger:     logger,
		ACP:        ack,
		DefaultCWD: "/test/cwd",
	})

	ctx, cancel := context.WithCancel(context.Background())
	req := &canonical.ChatRequest{
		Model: "auto",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "hi"}}},
		},
	}
	run, err := e.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer func() {
		if stop := run.StopWatchdog(); stop != nil {
			stop()
		}
	}()

	// Cancel to fire the AfterFunc callback (and the panic probe).
	cancel()

	// Poll for the panic-recovered record.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(buf.String(), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var rec map[string]any
			if jerr := json.Unmarshal([]byte(line), &rec); jerr != nil {
				continue
			}
			if msg, _ := rec["msg"].(string); msg != "goroutine panic recovered" {
				continue
			}
			if s, _ := rec["site"].(string); s != "engine-after-func" {
				continue
			}
			if lvl, _ := rec["level"].(string); lvl != "ERROR" {
				t.Errorf("level = %q, want ERROR", lvl)
			}
			if p, _ := rec["panic"].(string); !strings.Contains(p, "test-18-02-engine-after-func") {
				t.Errorf("panic = %q, want substring 'test-18-02-engine-after-func'", p)
			}
			if s, _ := rec["stack"].(string); s == "" {
				t.Errorf("stack field empty; want debug.Stack output")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no panic-recovered record with site=engine-after-func within 2s; buf=%s", buf.String())
}
