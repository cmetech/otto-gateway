// Phase 14 Plan 14-04 Task 1 — Regression test for REL-HOOKS-01 (G-1 Medium).
//
// Finding G-1: Non-streaming aggregation error paths skip PostHooks, causing
// LoggingHook.startTimes and ChatTraceHook.startTimes sync.Map entries to leak
// unboundedly on every idle-timeout or Result() error.
//
// Phase 16 fix (this commit): in engine.Collect and
// adapter/anthropic.CollectAnthropicChat, invoke the PostHook chain with a
// nil resp on the idle-timeout and Result()-error returns before propagating
// the error (mirrors the streaming discipline). LoggingHook.After and
// ChatTraceHook.After both nil-guard resp so they do not panic when called
// with nil — and they still LoadAndDelete the startTimes entry so the
// per-request bookkeeping is reclaimed on every code path.
package plugin

import (
	"bytes"
	"context"
	"io"
	"testing"

	"otto-gateway/internal/canonical"
)

// TestRegression_REL_HOOKS_01_StartTimesLeak asserts the post-fix behavior
// of the After() methods on both Pre+Post hooks (LoggingHook and
// ChatTraceHook): when After() is invoked with a nil resp (the shape used
// by engine.Collect / anthropic.CollectAnthropicChat on the idle-timeout
// and Result()-error error paths), the startTimes sync.Map entry is
// reclaimed and no panic occurs.
//
// This is the regression-locking shape of the G-1 fix. Pre-fix, the
// error-path return at engine/collect.go:165, :171, :174 (and
// anthropic/collect.go:177, :184) skipped the PostHook traversal entirely,
// so startTimes entries leaked one-per-failed-request. Post-fix the
// adapter loop calls callPostHookSafe(ctx, h, req, nil) before returning,
// which is what this test exercises directly against the hooks.
func TestRegression_REL_HOOKS_01_StartTimesLeak(t *testing.T) {
	t.Run("LoggingHook reclaims startTimes on nil resp", func(t *testing.T) {
		logger, buf := captureSlog(t)
		hook := &LoggingHook{Logger: logger}
		ctx := WithRequestID(context.Background(), "TEST-REL-HOOKS-01-LOG")
		req := &canonical.ChatRequest{
			Model: "auto",
			Messages: []canonical.Message{
				{Role: canonical.RoleUser},
			},
		}

		if _, err := hook.Before(ctx, req); err != nil {
			t.Fatalf("Before: unexpected err: %v", err)
		}
		// Sanity: Before stored the entry.
		if _, ok := hook.startTimes.Load("TEST-REL-HOOKS-01-LOG"); !ok {
			t.Fatalf("startTimes entry not present after Before — test scaffold drift")
		}

		// Post-fix engine error path calls After with nil resp. Must
		// not panic and must reclaim the entry via LoadAndDelete.
		if err := hook.After(ctx, req, nil); err != nil {
			t.Fatalf("After(nil resp): unexpected err: %v", err)
		}

		if _, ok := hook.startTimes.Load("TEST-REL-HOOKS-01-LOG"); ok {
			t.Errorf("startTimes entry leaked after After(nil resp) — G-1 regression")
		}

		// Sanity: After did emit a plugin.after record (so observability
		// is preserved on the error path).
		recs := decodeRecords(t, buf)
		sawAfter := false
		for _, r := range recs {
			if r["msg"] == "plugin.after" {
				sawAfter = true
				// stop_reason MUST be absent when resp is nil — the After
				// implementation must guard the attr append on resp != nil.
				if _, present := r["stop_reason"]; present {
					t.Errorf("plugin.after carries stop_reason when resp was nil: %+v", r)
				}
			}
		}
		if !sawAfter {
			t.Errorf("expected plugin.after record on error path After(nil resp); got: %+v", recs)
		}
		buf.Reset()
	})

	t.Run("ChatTraceHook reclaims startTimes on nil resp", func(t *testing.T) {
		var w bytes.Buffer
		hook := &ChatTraceHook{Writer: &w, Enabled: true}
		ctx := WithRequestID(context.Background(), "TEST-REL-HOOKS-01-TRACE")
		req := &canonical.ChatRequest{
			Model: "auto",
			Messages: []canonical.Message{
				{Role: canonical.RoleUser},
			},
		}

		if _, err := hook.Before(ctx, req); err != nil {
			t.Fatalf("Before: unexpected err: %v", err)
		}
		if _, ok := hook.startTimes.Load("TEST-REL-HOOKS-01-TRACE"); !ok {
			t.Fatalf("startTimes entry not present after Before — test scaffold drift")
		}

		// Post-fix engine error path calls After with nil resp. Must
		// not panic and must reclaim the entry via LoadAndDelete.
		if err := hook.After(ctx, req, nil); err != nil {
			t.Fatalf("After(nil resp): unexpected err: %v", err)
		}

		if _, ok := hook.startTimes.Load("TEST-REL-HOOKS-01-TRACE"); ok {
			t.Errorf("startTimes entry leaked after After(nil resp) — G-1 regression")
		}

		// Sanity: trace writer received the post_chain_out record (so
		// chat-trace.log is complete for failed requests too).
		if w.Len() == 0 {
			t.Errorf("expected chat-trace post_chain_out write on After(nil resp); got empty buffer")
		}
		// Discard writer contents to avoid bleed across subtests.
		_, _ = io.Copy(io.Discard, &w)
	})
}
