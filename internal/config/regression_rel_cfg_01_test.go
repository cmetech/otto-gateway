// Phase 14 Plan 14-04 Task 2 — Regression test for REL-CFG-01 (C-1 Medium).
//
// Finding C-1: POOL_SIZE, SESSION_MAX, SESSION_TTL_MS, SESSION_TICK_INTERVAL_MS,
// and CHAT_TRACE_MAX_AGE_DAYS are silently coerced when set to negative/zero
// values rather than producing a loud boot error naming the variable.
//
// This test is a direct template of TestLoad_StreamIdleTimeoutSec_Negative
// (config_test.go:655-667). Each subtest sets one env var to "-5" and asserts
// config.Load() returns an error mentioning the variable name and "must be >= 0".
//
// Pre-fix observable: Load() returns nil (or an error that doesn't mention the
// variable name) for these vars, because the sign is never checked — the value
// is silently coerced by pool.Config.applyDefaults (Size <= 0 → 1) and
// session.Config.applyDefaults (TTL ≤ 0 → 30m, tick ≤ 0 → 60s, max ≤ 0 → 32),
// or passed through as a negative int (CHAT_TRACE_MAX_AGE_DAYS).
//
// Post-fix (Phase 16): each var produces a boot error matching
// STREAM_IDLE_TIMEOUT_SEC's posture (config.go:366-368).
package config_test

import (
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

// TestRegression_REL_CFG_01_NegativeZeroEnvCoercion verifies that negative
// values for the five pool/session/trace config env vars produce a named
// boot error from config.Load(). This is a direct copy of the
// TestLoad_StreamIdleTimeoutSec_Negative pattern from config_test.go:655-667.
func TestRegression_REL_CFG_01_NegativeZeroEnvCoercion(t *testing.T) {
	t.Skip("REL-CFG-01 (C-1): regression test — unskip in Phase 16 fix commit")

	cases := []struct {
		name    string
		envVar  string
		value   string
		wantMsg string
	}{
		{
			name:    "POOL_SIZE negative",
			envVar:  "POOL_SIZE",
			value:   "-5",
			wantMsg: "POOL_SIZE",
		},
		{
			name:    "SESSION_MAX negative",
			envVar:  "SESSION_MAX",
			value:   "-5",
			wantMsg: "SESSION_MAX",
		},
		{
			name:    "SESSION_TTL_MS negative",
			envVar:  "SESSION_TTL_MS",
			value:   "-5",
			wantMsg: "SESSION_TTL_MS",
		},
		{
			name:    "SESSION_TICK_INTERVAL_MS negative",
			envVar:  "SESSION_TICK_INTERVAL_MS",
			value:   "-5",
			wantMsg: "SESSION_TICK_INTERVAL_MS",
		},
		{
			name:    "CHAT_TRACE_MAX_AGE_DAYS negative",
			envVar:  "CHAT_TRACE_MAX_AGE_DAYS",
			value:   "-5",
			wantMsg: "CHAT_TRACE_MAX_AGE_DAYS",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envVar, tc.value)
			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load() should return an error for %s=%s, got nil", tc.envVar, tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error should mention %s, got: %v", tc.wantMsg, err)
			}
			if !strings.Contains(err.Error(), "must be >= 0") {
				t.Errorf("error should explain the constraint (must be >= 0), got: %v", err)
			}
		})
	}
}
