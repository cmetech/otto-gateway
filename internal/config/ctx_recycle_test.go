package config_test

import (
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

// TestLoad_CtxRecyclePctDefault: CTX_RECYCLE_PCT defaults to 80 (percent). The
// kiro contextUsagePercentage is 0–100, so the recycle threshold is a percent —
// diverging from the Node 0.8 default value (which is mis-scaled against kiro).
//
// HTTP_ADDR is pinned to an OS-assigned free port so config.Load's bind probe
// cannot collide with the other default-addr parallel tests on 127.0.0.1:18080.
func TestLoad_CtxRecyclePctDefault(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.RecyclePct != 80 {
		t.Errorf("RecyclePct default: got %v, want 80", cfg.RecyclePct)
	}
}

// TestLoad_CtxRecyclePctOverride: a valid percent override is threaded through.
func TestLoad_CtxRecyclePctOverride(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("CTX_RECYCLE_PCT", "72.5")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.RecyclePct != 72.5 {
		t.Errorf("RecyclePct: got %v, want 72.5", cfg.RecyclePct)
	}
}

// TestLoad_CtxRecyclePctZeroDisables: 0 is valid (disables recycle).
func TestLoad_CtxRecyclePctZeroDisables(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("CTX_RECYCLE_PCT", "0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.RecyclePct != 0 {
		t.Errorf("RecyclePct: got %v, want 0 (disabled)", cfg.RecyclePct)
	}
}

// TestLoad_CtxRecyclePctNegativeRejected: a negative percent is a boot error
// (fail-fast, matching the STREAM_IDLE_TIMEOUT_SEC posture).
func TestLoad_CtxRecyclePctNegativeRejected(t *testing.T) {
	t.Setenv("CTX_RECYCLE_PCT", "-5")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "CTX_RECYCLE_PCT") {
		t.Fatalf("expected CTX_RECYCLE_PCT validation error, got %v", err)
	}
}

// TestLoad_CtxRecyclePctNonNumericRejected: a non-numeric value is a boot error.
func TestLoad_CtxRecyclePctNonNumericRejected(t *testing.T) {
	t.Setenv("CTX_RECYCLE_PCT", "eighty")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "CTX_RECYCLE_PCT") {
		t.Fatalf("expected CTX_RECYCLE_PCT parse error, got %v", err)
	}
}

// TestLoad_CtxRecyclePctOutOfRangeRejected: NaN, Inf, and values above 100 boot
// successfully today but make normal 0–100 context values unable to trip
// recycling. All must be rejected (percent domain is [0,100]).
func TestLoad_CtxRecyclePctOutOfRangeRejected(t *testing.T) {
	for _, val := range []string{"NaN", "Inf", "+Inf", "-Inf", "101", "100.1", "1e9"} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("CTX_RECYCLE_PCT", val)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), "CTX_RECYCLE_PCT") {
				t.Fatalf("CTX_RECYCLE_PCT=%q must be rejected, got err=%v", val, err)
			}
		})
	}
}

// TestLoad_CtxRecyclePct100Accepted: exactly 100 is a valid (never-recycle-until-
// full) threshold, so the upper bound is inclusive.
func TestLoad_CtxRecyclePct100Accepted(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("CTX_RECYCLE_PCT", "100")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("CTX_RECYCLE_PCT=100 should be valid, got %v", err)
	}
	if cfg.RecyclePct != 100 {
		t.Errorf("RecyclePct: got %v, want 100", cfg.RecyclePct)
	}
}
