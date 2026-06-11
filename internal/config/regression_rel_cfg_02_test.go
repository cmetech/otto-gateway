// Phase 14 Plan 14-04 Task 3 — Regression test for REL-CFG-02 (C-2 Medium).
//
// Finding C-2: A negative or zero PING_INTERVAL passes config.Load() unchecked.
// When the value reaches internal/acp/client.go:505, time.NewTicker panics with
// "non-positive interval for NewTicker" in a goroutine with no recovery — the
// process dies with a raw panic stack on stderr rather than a named config error.
//
// This test is a direct template of TestLoad_StreamIdleTimeoutSec_Negative
// (config_test.go:655-667), same pattern used for C-1.
//
// Pre-fix observable: Load() returns nil for PING_INTERVAL="0s" and "-60000ms"
// because config.go:295 calls getEnvDuration("PING_INTERVAL", 60*time.Second)
// and negative/zero durations are syntactically valid; acp/client.go:59 only
// fills the default when PingInterval == 0 (exactly), so a value like "-1m"
// passes through.
//
// Post-fix (Phase 16): config.Load() returns a named error mentioning
// PING_INTERVAL and "must be > 0" before any subprocess spawn occurs.
package config_test

import (
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

// TestRegression_REL_CFG_02_PingIntervalPanic verifies that PING_INTERVAL <= 0
// produces a named boot error from config.Load() rather than causing a runtime
// panic in time.NewTicker inside the ACP client's pingLoop goroutine.
//
// Two subtests cover the two common operator mistakes:
//   - "0s" (trying to disable pings)
//   - "-60000ms" (negative millisecond integer — Node compat format)
func TestRegression_REL_CFG_02_PingIntervalPanic(t *testing.T) {
	t.Skip("REL-CFG-02 (C-2): regression test — unskip in Phase 16 fix commit")

	cases := []struct {
		name  string
		value string
	}{
		{
			name:  "PING_INTERVAL zero duration",
			value: "0s",
		},
		{
			name:  "PING_INTERVAL negative milliseconds",
			value: "-60000",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PING_INTERVAL", tc.value)
			_, err := config.Load()
			// Pre-fix: Load() succeeds (nil error); the panic happens later in
			// acp/client.go:505 (time.NewTicker with non-positive interval).
			// Post-fix: Load() returns an error mentioning PING_INTERVAL.
			if err == nil {
				t.Fatalf("Load() should return an error for PING_INTERVAL=%s, got nil", tc.value)
			}
			if !strings.Contains(err.Error(), "PING_INTERVAL") {
				t.Errorf("error should mention PING_INTERVAL, got: %v", err)
			}
			if !strings.Contains(err.Error(), "must be > 0") {
				t.Errorf("error should explain the constraint (must be > 0), got: %v", err)
			}
		})
	}
}
