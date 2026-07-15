package config_test

import (
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

// TestLoad_MaxToolDenialsDefault: MAX_TOOL_DENIALS defaults to 4 (Track 3a
// circuit-breaker threshold). HTTP_ADDR is pinned to an OS-assigned free port
// so config.Load's bind probe cannot collide with other default-addr parallel
// tests on 127.0.0.1:18080.
func TestLoad_MaxToolDenialsDefault(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.MaxToolDenials != 4 {
		t.Errorf("MaxToolDenials default: got %d, want 4", cfg.MaxToolDenials)
	}
}

// TestLoad_MaxToolDenialsOverride: a valid override is threaded through.
func TestLoad_MaxToolDenialsOverride(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("MAX_TOOL_DENIALS", "7")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.MaxToolDenials != 7 {
		t.Errorf("MaxToolDenials: got %d, want 7", cfg.MaxToolDenials)
	}
}

// TestLoad_MaxToolDenialsZeroRejected: zero is rejected (fail-fast).
func TestLoad_MaxToolDenialsZeroRejected(t *testing.T) {
	t.Setenv("MAX_TOOL_DENIALS", "0")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "MAX_TOOL_DENIALS") {
		t.Fatalf("expected MAX_TOOL_DENIALS validation error, got %v", err)
	}
}

// TestLoad_MaxToolDenialsNegativeRejected: a negative value is a boot error.
func TestLoad_MaxToolDenialsNegativeRejected(t *testing.T) {
	t.Setenv("MAX_TOOL_DENIALS", "-1")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "MAX_TOOL_DENIALS") {
		t.Fatalf("expected MAX_TOOL_DENIALS validation error, got %v", err)
	}
}

// TestLoad_MaxToolDenialsNonNumericRejected: a non-numeric value is a boot error.
func TestLoad_MaxToolDenialsNonNumericRejected(t *testing.T) {
	t.Setenv("MAX_TOOL_DENIALS", "not_a_number")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "MAX_TOOL_DENIALS") {
		t.Fatalf("expected MAX_TOOL_DENIALS parse error, got %v", err)
	}
}
