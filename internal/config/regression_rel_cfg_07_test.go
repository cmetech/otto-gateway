// Phase 18 Plan 18-01 Task 3 — Regression test for REL-CFG-07 (D-18-03).
//
// Finding REL-CFG-07: HTTP_ADDR already-bound by another process surfaces
// 5–10s after boot, inside server.ListenAndServe, after the gateway has
// already paid the kiro-cli pool warmup cost. The error does not name
// HTTP_ADDR — operators see a raw net.OpError.
//
// Post-fix (D-18-03): config.Load() does a bind-then-close TCP probe of
// HTTP_ADDR; on bind failure it appends a config-named, wrapped error
// to the errs accumulator so the operator gets a single errors.Join
// surface naming HTTP_ADDR and the offending address — within
// milliseconds, BEFORE pool warmup.
//
// Error format (byte-substring): `config: HTTP_ADDR ("<addr>"): bind probe failed: <underlying>`
// TOCTOU window between probe close and the real ListenAndServe bind is
// acceptable per CONTEXT.md and not retried.
package config_test

import (
	"net"
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

// silenceForCFG07 sets the minimum env state required for config.Load()
// to reach the HTTP_ADDR probe without unrelated errors poisoning the
// assertion.
func silenceForCFG07(t *testing.T) {
	t.Helper()
	t.Setenv("PII_REDACTION_MODE", "replace")
	t.Setenv("AUTH_TOKEN", "")
	t.Setenv("ALLOWED_IPS", "")
	t.Setenv("KIRO_CMD", "go")
	t.Setenv("KIRO_CWD", "")
}

// TestRegression_REL_CFG_07 covers two cases per the plan:
//
//	A: HTTP_ADDR is already bound by a pre-existing listener → config.Load
//	   returns an error containing "HTTP_ADDR (", "bind probe failed", and
//	   the address string.
//	B: HTTP_ADDR=127.0.0.1:0 (kernel-assigned, always bindable) → no
//	   HTTP_ADDR-related error.
func TestRegression_REL_CFG_07(t *testing.T) {
	t.Run("A_HTTP_ADDR_already_bound_emits_named_error", func(t *testing.T) {
		silenceForCFG07(t)

		// Pre-bind a listener on a kernel-assigned port; the address we
		// hand to config.Load() is the address THIS listener owns, so
		// the probe MUST fail with EADDRINUSE.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("pre-bind listener: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		addr := ln.Addr().String()
		t.Setenv("HTTP_ADDR", addr)

		_, loadErr := config.Load()
		if loadErr == nil {
			t.Fatalf("expected named HTTP_ADDR bind-probe error, got nil")
		}
		msg := loadErr.Error()
		if !strings.Contains(msg, "HTTP_ADDR (") {
			t.Errorf("error should name HTTP_ADDR with quoted value, got: %v", loadErr)
		}
		if !strings.Contains(msg, "bind probe failed") {
			t.Errorf("error should contain 'bind probe failed' phrasing, got: %v", loadErr)
		}
		if !strings.Contains(msg, addr) {
			t.Errorf("error should contain the offending address %q, got: %v", addr, loadErr)
		}
	})

	t.Run("B_HTTP_ADDR_kernel_assigned_no_error", func(t *testing.T) {
		silenceForCFG07(t)
		t.Setenv("HTTP_ADDR", "127.0.0.1:0")

		_, loadErr := config.Load()
		if loadErr != nil && strings.Contains(loadErr.Error(), "HTTP_ADDR") {
			t.Errorf("did not expect HTTP_ADDR-related error for 127.0.0.1:0, got: %v", loadErr)
		}
	})
}
