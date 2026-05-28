// Phase 8 Plan 08-05 Task 1 — Wave 0 boot-validation tests for the
// five Phase 8 env keys (ENABLED_HOOKS, PII_REDACTION_ENABLED,
// PII_ENABLED_ENTITIES, PII_REDACTION_MODE, PII_HASH_KEY) plus the
// two boot-error refusal contracts (D-05 + Pitfall 6, Pitfall 7).
//
// All tests use t.Setenv so they cannot run in parallel. This mirrors
// the existing config_test.go t.Setenv discipline.
//
// Test count: 11 (per plan acceptance criteria).
package config_test

import (
	"reflect"
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

// TestLoad_EnabledHooks_Parsing — ENABLED_HOOKS comma-split + default
// empty / unset → nil slice (default-permissive per D-02).
func TestLoad_EnabledHooks_Parsing(t *testing.T) {
	t.Setenv("ENABLED_HOOKS", "RequestIDHook,LoggingHook")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"RequestIDHook", "LoggingHook"}
	if !reflect.DeepEqual(cfg.EnabledHooks, want) {
		t.Errorf("EnabledHooks: got %v, want %v", cfg.EnabledHooks, want)
	}

	// Unset variant covered by TestLoadDefaults indirectly; verify the
	// empty-string explicit case here.
	t.Setenv("ENABLED_HOOKS", "")
	cfg2, err := config.Load()
	if err != nil {
		t.Fatalf("Load (empty): %v", err)
	}
	if len(cfg2.EnabledHooks) != 0 {
		t.Errorf("EnabledHooks (empty): got %v, want empty/nil", cfg2.EnabledHooks)
	}
}

// TestLoad_PIIRedactionEnabled_Default — unset PII_REDACTION_ENABLED
// → false (operator must opt in per D-02 composition rule).
func TestLoad_PIIRedactionEnabled_Default(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PIIRedactionEnabled {
		t.Errorf("PIIRedactionEnabled default: got true, want false (operator opt-in only)")
	}
}

// TestLoad_PIIRedactionEnabled_TrueValue — PII_REDACTION_ENABLED=true
// → cfg.PIIRedactionEnabled == true. Reuses getEnvBool semantics
// (accepted truthy: "1", "true", "TRUE", "True"; falsy: "0", "false",
// "FALSE"). Unknown values are a boot error via getEnvBool.
func TestLoad_PIIRedactionEnabled_TrueValue(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"1", true},
		{"TRUE", true},
		{"false", false},
		{"0", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("PII_REDACTION_ENABLED", tc.val)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load PII_REDACTION_ENABLED=%q: %v", tc.val, err)
			}
			if cfg.PIIRedactionEnabled != tc.want {
				t.Errorf("PIIRedactionEnabled: got %v, want %v", cfg.PIIRedactionEnabled, tc.want)
			}
		})
	}
}

// TestLoad_PIIEnabledEntities_Parsing — comma-split allowlist.
func TestLoad_PIIEnabledEntities_Parsing(t *testing.T) {
	t.Setenv("PII_ENABLED_ENTITIES", "Email,SSN")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"Email", "SSN"}
	if !reflect.DeepEqual(cfg.PIIEnabledEntities, want) {
		t.Errorf("PIIEnabledEntities: got %v, want %v", cfg.PIIEnabledEntities, want)
	}
}

// TestLoad_PIIEnabledEntities_UnknownNameError — unknown entity name →
// boot error containing both PII_ENABLED_ENTITIES and the offending
// name (typo-fail-fast per D-02-style discipline applied to PII).
func TestLoad_PIIEnabledEntities_UnknownNameError(t *testing.T) {
	t.Setenv("PII_ENABLED_ENTITIES", "Email,BadEntity")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected boot error for unknown PII entity")
	}
	msg := err.Error()
	if !strings.Contains(msg, "PII_ENABLED_ENTITIES") {
		t.Errorf("error should mention PII_ENABLED_ENTITIES; got %v", err)
	}
	if !strings.Contains(msg, "BadEntity") {
		t.Errorf("error should mention BadEntity; got %v", err)
	}
}

// TestLoad_PIIRedactionMode_Default — unset PII_REDACTION_MODE →
// "replace" (D-05 default).
func TestLoad_PIIRedactionMode_Default(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PIIRedactionMode != "replace" {
		t.Errorf("PIIRedactionMode default: got %q, want %q", cfg.PIIRedactionMode, "replace")
	}
}

// TestLoad_PIIRedactionMode_ValidModes — every documented mode is
// accepted. PII_HASH_KEY is set alongside to bypass the mode=hash
// requires-key validation.
func TestLoad_PIIRedactionMode_ValidModes(t *testing.T) {
	for _, mode := range []string{"replace", "mask", "hash", "drop"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PII_REDACTION_MODE", mode)
			t.Setenv("PII_HASH_KEY", "test-key-32-bytes-padding-here!!")
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load PII_REDACTION_MODE=%q: %v", mode, err)
			}
			if cfg.PIIRedactionMode != mode {
				t.Errorf("PIIRedactionMode: got %q, want %q", cfg.PIIRedactionMode, mode)
			}
		})
	}
}

// TestLoad_PIIRedactionMode_UnknownModeError — unknown mode value →
// boot error naming both PII_REDACTION_MODE and the offending value.
func TestLoad_PIIRedactionMode_UnknownModeError(t *testing.T) {
	t.Setenv("PII_REDACTION_MODE", "bogus")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected boot error for unknown PII_REDACTION_MODE")
	}
	msg := err.Error()
	if !strings.Contains(msg, "PII_REDACTION_MODE") {
		t.Errorf("error should mention PII_REDACTION_MODE; got %v", err)
	}
	if !strings.Contains(msg, "bogus") {
		t.Errorf("error should mention bogus; got %v", err)
	}
}

// TestLoad_PIIHashModeRequiresKey — PII_REDACTION_MODE=hash without
// PII_HASH_KEY → boot error naming PII_HASH_KEY (D-05 + Pitfall 6,
// T-8-HASH-BOOT mitigation; no silent unkeyed fallback).
func TestLoad_PIIHashModeRequiresKey(t *testing.T) {
	t.Setenv("PII_REDACTION_MODE", "hash")
	// PII_HASH_KEY deliberately not set.
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected boot error when PII_REDACTION_MODE=hash with no PII_HASH_KEY")
	}
	if !strings.Contains(err.Error(), "PII_HASH_KEY") {
		t.Errorf("error should mention PII_HASH_KEY; got %v", err)
	}
}

// TestLoad_PIIHashModeWithKey_NoError — mode=hash + key set → boot
// succeeds.
func TestLoad_PIIHashModeWithKey_NoError(t *testing.T) {
	t.Setenv("PII_REDACTION_MODE", "hash")
	t.Setenv("PII_HASH_KEY", "test-key-32-bytes-padding-here!!")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PIIRedactionMode != "hash" {
		t.Errorf("PIIRedactionMode: got %q, want hash", cfg.PIIRedactionMode)
	}
	if cfg.PIIHashKey != "test-key-32-bytes-padding-here!!" {
		t.Errorf("PIIHashKey: got %q, want test-key-…", cfg.PIIHashKey)
	}
}

// TestLoad_NonHashModeWithoutKey_NoError — keyless modes (replace /
// mask / drop) don't need PII_HASH_KEY (Pitfall 6 last paragraph).
func TestLoad_NonHashModeWithoutKey_NoError(t *testing.T) {
	t.Setenv("PII_REDACTION_MODE", "replace")
	// PII_HASH_KEY deliberately not set.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PIIRedactionMode != "replace" {
		t.Errorf("PIIRedactionMode: got %q, want replace", cfg.PIIRedactionMode)
	}
}
