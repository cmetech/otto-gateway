package config_test

import (
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

// TestLoad_CtxRecyclePctDefault: CTX_RECYCLE_PCT defaults to 80 (percent). The
// kiro contextUsagePercentage is 0–100, so the recycle threshold is a percent —
// diverging from the Node 0.8 default value (which is mis-scaled against kiro).
func TestLoad_CtxRecyclePctDefault(t *testing.T) {
	t.Parallel()
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
