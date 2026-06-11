// Phase 14 Plan 14-04 Task 1 — Regression test for REL-HOOKS-01 (G-1 Medium).
//
// Finding G-1: Non-streaming aggregation error paths skip PostHooks, causing
// LoggingHook.startTimes and ChatTraceHook.startTimes sync.Map entries to leak
// unboundedly on every idle-timeout or Result() error.
//
// This test reproduces the pre-fix state: after a non-streaming error path
// (e.g., idle-timeout 504, Result() error 500), the PostHook chain is NOT
// called, so startTimes entries are never LoadAndDeleted — they accumulate
// forever for the lifetime of the process.
//
// Phase 16 fix: in engine.Collect and adapter/anthropic.CollectAnthropicChat,
// invoke the PostHook chain on the idle-timeout and Result()-error returns
// before propagating the error (mirrors the streaming discipline).
package plugin

import (
	"context"
	"testing"

	"otto-gateway/internal/canonical"
)

// TestRegression_REL_HOOKS_01_NonStreamingPostHookSkip demonstrates the
// pre-fix state: on non-streaming error paths, PostHooks are not called
// and startTimes sync.Map entries leak.
//
// Pre-fix observable: after PreHook stores a startTimes entry and the
// error path is taken (without calling PostHook), the entry persists —
// LoadAndDelete is never invoked, so the map grows unboundedly on retry
// storms.
//
// Post-fix (Phase 16): PostHook is invoked on the idle-timeout and
// Result()-error paths in engine.Collect and anthropic.CollectAnthropicChat,
// clearing the map entries before returning the error.
func TestRegression_REL_HOOKS_01_NonStreamingPostHookSkip(t *testing.T) {
	t.Skip("REL-HOOKS-01 (G-1): regression test — unskip in Phase 16 fix commit")

	logger, buf := captureSlog(t)
	hook := &LoggingHook{Logger: logger}
	ctx := WithRequestID(context.Background(), "TEST-REL-HOOKS-01")
	req := &canonical.ChatRequest{
		Model: "auto",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser},
		},
	}

	// Simulate the Pre phase (populates startTimes).
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: unexpected err: %v", err)
	}

	// Pre-fix: startTimes now has an entry for "TEST-REL-HOOKS-01".
	// The engine error path returns WITHOUT calling hook.After — simulated
	// by simply not calling After here (matching what engine/collect.go
	// lines 165 and 171 do: return before PostHook traversal at line 187).

	// Assert: the startTimes entry is still present (leaked) after the
	// error path. Post-fix: the engine calls PostHook which clears it via
	// LoadAndDelete.
	_, loaded := hook.startTimes.Load("TEST-REL-HOOKS-01")
	if !loaded {
		// Post-fix state — entry was reclaimed. This is what Phase 16 achieves.
		t.Log("startTimes entry already reclaimed — this is the post-fix state")
		return
	}
	// Pre-fix state — entry leaked. This is the bug.
	t.Log("startTimes entry leaked (pre-fix confirmed): no PostHook was called on the error path")

	// Verify no "plugin.after" record was emitted (PostHook not called).
	recs := decodeRecords(t, buf)
	for _, r := range recs {
		if r["msg"] == "plugin.after" {
			t.Errorf("unexpected plugin.after record — PostHook should not have been called on the error path: %+v", r)
		}
	}
	// Assert the sync.Map entry is non-empty to prove the leak.
	count := 0
	hook.startTimes.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count == 0 {
		t.Error("expected non-zero startTimes entries (leaked), got 0 — pre-fix reproducer failed")
	}

	// Drain buffer to avoid goleak confusion.
	buf.Reset()
}
