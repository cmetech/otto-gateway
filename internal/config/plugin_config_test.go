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
	"time"

	"github.com/google/go-cmp/cmp"

	"otto-gateway/internal/config"
)

// TestLoad_PrivacyDefaults locks the default privacy profile and bounded
// pseudonymization-store contract. A regression to any default below would
// change the privacy boundary for deployments that have not set PRIVACY_*.
func TestLoad_PrivacyDefaults(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PrivacyDefaultProfile != "standard" {
		t.Fatalf("PrivacyDefaultProfile=%q, want standard", cfg.PrivacyDefaultProfile)
	}
	if diff := cmp.Diff([]string{"standard", "strict"}, cfg.PrivacyRequestProfiles); diff != "" {
		t.Fatalf("PrivacyRequestProfiles mismatch (-want +got):\n%s", diff)
	}
	if cfg.PrivacySecretAction != "replace" {
		t.Fatalf("PrivacySecretAction=%q, want replace", cfg.PrivacySecretAction)
	}
	if cfg.PrivacyTechnicalAction != "pseudonymize" {
		t.Fatalf("PrivacyTechnicalAction=%q, want pseudonymize", cfg.PrivacyTechnicalAction)
	}
	if cfg.PrivacyScopeTTL != time.Hour || cfg.PrivacyMaxScopes != 128 || cfg.PrivacyMaxEntriesPerScope != 4096 || cfg.PrivacyMaxTotalEntries != 32768 {
		t.Fatalf("privacy capacity defaults: ttl=%v scopes=%d entries/scope=%d total=%d", cfg.PrivacyScopeTTL, cfg.PrivacyMaxScopes, cfg.PrivacyMaxEntriesPerScope, cfg.PrivacyMaxTotalEntries)
	}
	if cfg.PrivacyTriageEnabled {
		t.Fatal("PrivacyTriageEnabled=true, want false")
	}
	if !cfg.PIIRedactionEnabled || cfg.PIIRedactionMode != "encrypt" || !cfg.PIINEREnabled {
		t.Fatalf("PII defaults: enabled=%t mode=%q ner=%t", cfg.PIIRedactionEnabled, cfg.PIIRedactionMode, cfg.PIINEREnabled)
	}
}

// TestLoad_PrivacyOverrides verifies every PRIVACY_* setting maps directly to
// its Config field so a configured minimum privacy profile has no hidden
// environment-only state.
func TestLoad_PrivacyOverrides(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{"default profile", "PRIVACY_DEFAULT_PROFILE", "strict"},
		{"request profiles", "PRIVACY_REQUEST_PROFILES", "strict"},
		{"alias key", "PRIVACY_ALIAS_KEY", "override-alias-key"},
		{"secret action", "PRIVACY_SECRET_ACTION", "drop"},
		{"technical action", "PRIVACY_TECHNICAL_ACTION", "drop"},
		{"scope ttl", "PRIVACY_SCOPE_TTL", "90m"},
		{"max scopes", "PRIVACY_MAX_SCOPES", "64"},
		{"max entries per scope", "PRIVACY_MAX_ENTRIES_PER_SCOPE", "1024"},
		{"max total entries", "PRIVACY_MAX_TOTAL_ENTRIES", "8192"},
		{"triage enabled", "PRIVACY_TRIAGE_ENABLED", "true"},
		{"triage token", "PRIVACY_TRIAGE_TOKEN", "override-triage-token"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.key == "PRIVACY_REQUEST_PROFILES" {
				t.Setenv("PRIVACY_DEFAULT_PROFILE", "strict")
			}
			t.Setenv(tc.key, tc.value)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			switch tc.key {
			case "PRIVACY_DEFAULT_PROFILE":
				if cfg.PrivacyDefaultProfile != tc.value {
					t.Fatalf("got %q", cfg.PrivacyDefaultProfile)
				}
			case "PRIVACY_REQUEST_PROFILES":
				if diff := cmp.Diff([]string{"strict"}, cfg.PrivacyRequestProfiles); diff != "" {
					t.Fatal(diff)
				}
			case "PRIVACY_ALIAS_KEY":
				if cfg.PrivacyAliasKey != tc.value {
					t.Fatalf("got %q", cfg.PrivacyAliasKey)
				}
			case "PRIVACY_SECRET_ACTION":
				if cfg.PrivacySecretAction != tc.value {
					t.Fatalf("got %q", cfg.PrivacySecretAction)
				}
			case "PRIVACY_TECHNICAL_ACTION":
				if cfg.PrivacyTechnicalAction != tc.value {
					t.Fatalf("got %q", cfg.PrivacyTechnicalAction)
				}
			case "PRIVACY_SCOPE_TTL":
				if cfg.PrivacyScopeTTL != 90*time.Minute {
					t.Fatalf("got %v", cfg.PrivacyScopeTTL)
				}
			case "PRIVACY_MAX_SCOPES":
				if cfg.PrivacyMaxScopes != 64 {
					t.Fatalf("got %d", cfg.PrivacyMaxScopes)
				}
			case "PRIVACY_MAX_ENTRIES_PER_SCOPE":
				if cfg.PrivacyMaxEntriesPerScope != 1024 {
					t.Fatalf("got %d", cfg.PrivacyMaxEntriesPerScope)
				}
			case "PRIVACY_MAX_TOTAL_ENTRIES":
				if cfg.PrivacyMaxTotalEntries != 8192 {
					t.Fatalf("got %d", cfg.PrivacyMaxTotalEntries)
				}
			case "PRIVACY_TRIAGE_ENABLED":
				if !cfg.PrivacyTriageEnabled {
					t.Fatal("got false")
				}
			case "PRIVACY_TRIAGE_TOKEN":
				if cfg.PrivacyTriageToken != tc.value {
					t.Fatalf("got %q", cfg.PrivacyTriageToken)
				}
			}
		})
	}
}

// TestLoad_PrivacyInvalid locks the fail-fast privacy boundary. Each case
// represents a configuration that could otherwise weaken the configured
// minimum profile or unbound aliasing state at runtime.
func TestLoad_PrivacyInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"unknown profile", map[string]string{"PRIVACY_DEFAULT_PROFILE": "maximum"}, "PRIVACY_DEFAULT_PROFILE"},
		{"duplicate profiles", map[string]string{"PRIVACY_REQUEST_PROFILES": "standard,standard"}, "PRIVACY_REQUEST_PROFILES"},
		{"default not allowed", map[string]string{"PRIVACY_DEFAULT_PROFILE": "strict", "PRIVACY_REQUEST_PROFILES": "standard"}, "PRIVACY_DEFAULT_PROFILE"},
		{"bad secret action", map[string]string{"PRIVACY_SECRET_ACTION": "encrypt"}, "PRIVACY_SECRET_ACTION"},
		{"bad technical action", map[string]string{"PRIVACY_TECHNICAL_ACTION": "replace"}, "PRIVACY_TECHNICAL_ACTION"},
		{"strict requires alias key", map[string]string{"PRIVACY_ALIAS_KEY": ""}, "PRIVACY_ALIAS_KEY"},
		{"triage requires token", map[string]string{"PRIVACY_TRIAGE_ENABLED": "true", "PRIVACY_TRIAGE_TOKEN": ""}, "PRIVACY_TRIAGE_TOKEN"},
		{"zero scope cap", map[string]string{"PRIVACY_MAX_SCOPES": "0"}, "PRIVACY_MAX_SCOPES"},
		{"negative entries per scope", map[string]string{"PRIVACY_MAX_ENTRIES_PER_SCOPE": "-1"}, "PRIVACY_MAX_ENTRIES_PER_SCOPE"},
		{"zero total entries", map[string]string{"PRIVACY_MAX_TOTAL_ENTRIES": "0"}, "PRIVACY_MAX_TOTAL_ENTRIES"},
		{"scope capacity exceeds global", map[string]string{"PRIVACY_MAX_ENTRIES_PER_SCOPE": "100", "PRIVACY_MAX_TOTAL_ENTRIES": "99"}, "PRIVACY_MAX_ENTRIES_PER_SCOPE"},
		{"strict requires PII", map[string]string{"PII_REDACTION_ENABLED": "false"}, "PII_REDACTION_ENABLED"},
		{"pseudonymize nontechnical entity", map[string]string{"PII_REDACTION_MODE": "replace", "PII_ENTITY_ACTIONS": "Email:pseudonymize"}, "pseudonymize is only permitted for technical entities"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			_, err := config.Load()
			if err == nil {
				t.Fatal("expected config.Load to reject invalid privacy configuration")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestLoad_PrivacyRejectsShippedSecretPlaceholders catches startup treating a
// public template sentinel as an installed alias key or triage capability.
// Errors remain stable and must not echo the placeholder value.
func TestLoad_PrivacyRejectsShippedSecretPlaceholders(t *testing.T) {
	const shippedPlaceholder = "<generated-by-gw-init>"
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "strict alias key",
			env:  map[string]string{"PRIVACY_ALIAS_KEY": shippedPlaceholder},
			want: "PRIVACY_ALIAS_KEY: required when strict is available",
		},
		{
			name: "enabled triage token",
			env: map[string]string{
				"PRIVACY_TRIAGE_ENABLED": "true",
				"PRIVACY_TRIAGE_TOKEN":   shippedPlaceholder,
			},
			want: "PRIVACY_TRIAGE_TOKEN: required when PRIVACY_TRIAGE_ENABLED is true",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			_, err := config.Load()
			if err == nil {
				t.Fatal("expected config.Load to reject a shipped privacy placeholder")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%q, want stable validation %q", err, tc.want)
			}
			if strings.Contains(err.Error(), shippedPlaceholder) {
				t.Fatalf("startup error exposed the shipped placeholder: %q", err)
			}
		})
	}
}

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
// → true (secure-by-default: PII redaction is on out of the box).
// Operators explicitly setting PII_REDACTION_ENABLED=false opt out.
func TestLoad_PIIRedactionEnabled_Default(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.PIIRedactionEnabled {
		t.Errorf("PIIRedactionEnabled default: got false, want true (secure-by-default)")
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
			if !tc.want {
				t.Setenv("PRIVACY_REQUEST_PROFILES", "standard")
			}
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
// "encrypt" (secure-by-default: round-trip is the out-of-box behavior).
// The encrypt-active boot validation requires PII_ENCRYPT_KEY to be
// set; testmain stamps a key so the validation passes here.
func TestLoad_PIIRedactionMode_Default(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PIIRedactionMode != "encrypt" {
		t.Errorf("PIIRedactionMode default: got %q, want %q", cfg.PIIRedactionMode, "encrypt")
	}
}

// TestLoad_PIIRedactionMode_ValidModes — every documented mode is
// accepted. PII_HASH_KEY and PII_ENCRYPT_KEY are both set so the
// mode=hash and mode=encrypt requires-key validations pass.
func TestLoad_PIIRedactionMode_ValidModes(t *testing.T) {
	for _, mode := range []string{"replace", "mask", "hash", "drop", "encrypt"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PII_REDACTION_MODE", mode)
			t.Setenv("PII_HASH_KEY", "test-key-32-bytes-padding-here!!")
			t.Setenv("PII_ENCRYPT_KEY", "any-non-empty-string")
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

// PII-ENCRYPT-08 — PII_ENTITY_ACTIONS parser + validator.

func TestLoad_PIIEntityActions_Empty(t *testing.T) {
	t.Setenv("PII_ENTITY_ACTIONS", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.PIIEntityActions) != 0 {
		t.Errorf("empty env: got %v, want empty map", cfg.PIIEntityActions)
	}
}

func TestLoad_PIIEntityActions_Parses(t *testing.T) {
	t.Setenv("PII_REDACTION_ENABLED", "true")
	t.Setenv("PII_REDACTION_MODE", "mask")
	t.Setenv("PII_ENTITY_ACTIONS", "Email:encrypt,SSN:drop")
	t.Setenv("PII_ENCRYPT_KEY", "any-string") // required because encrypt is active
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.PIIEntityActions["Email"]; got != "encrypt" {
		t.Errorf("Email action: got %q, want %q", got, "encrypt")
	}
	if got := cfg.PIIEntityActions["SSN"]; got != "drop" {
		t.Errorf("SSN action: got %q, want %q", got, "drop")
	}
}

// TestLoad_PIIEntityActions_PseudonymizeTechnicalEntity permits stable aliases
// only for the technical identifiers protected by the privacy profile.
func TestLoad_PIIEntityActions_PseudonymizeTechnicalEntity(t *testing.T) {
	t.Setenv("PII_REDACTION_MODE", "replace")
	t.Setenv("PII_ENTITY_ACTIONS", "IMEI:pseudonymize")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PIIEntityActions["IMEI"] != "pseudonymize" {
		t.Fatalf("IMEI action=%q, want pseudonymize", cfg.PIIEntityActions["IMEI"])
	}
}

func TestLoad_PIIEntityActions_PseudonymizeUSPhoneRejected(t *testing.T) {
	t.Setenv("PII_REDACTION_MODE", "replace")
	t.Setenv("PII_ENTITY_ACTIONS", "USPhone:pseudonymize")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load accepted pseudonymize for personal USPhone entity")
	}
	if !strings.Contains(err.Error(), "pseudonymize is only permitted for technical entities") {
		t.Fatalf("Load error=%q, want technical-entity rejection", err)
	}
}

func TestLoad_PIIEntityActions_UnknownEntity(t *testing.T) {
	t.Setenv("PII_REDACTION_ENABLED", "true")
	t.Setenv("PII_ENTITY_ACTIONS", "Emial:encrypt") // typo
	t.Setenv("PII_ENCRYPT_KEY", "any-string")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected boot error for unknown entity, got nil")
	}
	if !strings.Contains(err.Error(), "Emial") {
		t.Errorf("error should name the offending entity: got %v", err)
	}
}

func TestLoad_PIIEntityActions_UnknownAction(t *testing.T) {
	t.Setenv("PII_REDACTION_ENABLED", "true")
	t.Setenv("PII_ENTITY_ACTIONS", "Email:shred") // unknown action
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected boot error for unknown action, got nil")
	}
	if !strings.Contains(err.Error(), "shred") {
		t.Errorf("error should name the offending action: got %v", err)
	}
}

func TestLoad_PIIEntityActions_MalformedPair(t *testing.T) {
	t.Setenv("PII_REDACTION_ENABLED", "true")
	t.Setenv("PII_ENTITY_ACTIONS", "Email-encrypt") // missing colon
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected boot error for malformed pair, got nil")
	}
}

// PII-ENCRYPT-09 — boot validation matrix per spec §3.5.

func TestLoad_EncryptKeyBootValidation(t *testing.T) {
	tests := []struct {
		name      string
		enabled   string
		mode      string
		actions   string
		key       string
		wantError bool
	}{
		{"pii off — key unset", "false", "replace", "", "", false},
		{"pii on, mode=replace, no encrypt anywhere — key irrelevant", "true", "replace", "", "", false},
		{"pii on, mode=encrypt, key missing", "true", "encrypt", "", "", true},
		{"pii on, mode=encrypt, key present", "true", "encrypt", "", "any-string", false},
		{"pii on, entity-action=encrypt, key missing", "true", "replace", "Email:encrypt", "", true},
		{"pii on, entity-action=encrypt, key present", "true", "replace", "Email:encrypt", "any-string", false},
		{"pii on, mode=mask, no encrypt — key irrelevant even if set", "true", "mask", "", "set-but-ignored", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.enabled == "false" {
				t.Setenv("PRIVACY_REQUEST_PROFILES", "standard")
			}
			t.Setenv("PII_REDACTION_ENABLED", tc.enabled)
			t.Setenv("PII_REDACTION_MODE", tc.mode)
			t.Setenv("PII_ENTITY_ACTIONS", tc.actions)
			t.Setenv("PII_ENCRYPT_KEY", tc.key)
			_, err := config.Load()
			if tc.wantError && err == nil {
				t.Error("expected boot error, got nil")
			}
			if !tc.wantError && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

// TestLoad_PIINEREnabled_Default locks the default-on NER alignment required
// by the privacy profile contract.
func TestLoad_PIINEREnabled_Default(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.PIINEREnabled {
		t.Error("PIINEREnabled default: got false, want true")
	}
}

// TestLoad_PIINEREnabled_TrueValue — every accepted bool true literal
// flips PIINEREnabled to true.
func TestLoad_PIINEREnabled_TrueValue(t *testing.T) {
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
			t.Setenv("PII_NER_ENABLED", tc.val)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load PII_NER_ENABLED=%q: %v", tc.val, err)
			}
			if cfg.PIINEREnabled != tc.want {
				t.Errorf("PIINEREnabled: got %v, want %v", cfg.PIINEREnabled, tc.want)
			}
		})
	}
}

// TestLoad_PIIEnabledEntities_TelecomAndNERNames — the expanded
// allowlist accepts PERSON, LOCATION, and the seven telecom recognizer
// names (SIP_URI, IMEI, IMSI, MSISDN, MAC_ADDRESS, COORDINATES, SITE)
// in addition to the original six.
func TestLoad_PIIEnabledEntities_TelecomAndNERNames(t *testing.T) {
	t.Setenv("PII_REDACTION_ENABLED", "true")
	t.Setenv("PII_ENABLED_ENTITIES",
		"Email,IPv4,IPv6,SSN,CreditCard,USPhone,SIP_URI,IMEI,IMSI,MSISDN,MAC_ADDRESS,COORDINATES,SITE,PERSON,LOCATION")
	if _, err := config.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// TestLoad_PIIEntityActions_TelecomAndNEREntities — the expanded
// allowlist also gates PII_ENTITY_ACTIONS map keys.
func TestLoad_PIIEntityActions_TelecomAndNEREntities(t *testing.T) {
	t.Setenv("PII_REDACTION_ENABLED", "true")
	t.Setenv("PII_REDACTION_MODE", "mask")
	t.Setenv("PII_ENCRYPT_KEY", "k")
	t.Setenv("PII_ENTITY_ACTIONS", "PERSON:mask,LOCATION:drop,IMEI:encrypt,SIP_URI:hash")
	t.Setenv("PII_HASH_KEY", "h")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PIIEntityActions["PERSON"] != "mask" ||
		cfg.PIIEntityActions["LOCATION"] != "drop" ||
		cfg.PIIEntityActions["IMEI"] != "encrypt" ||
		cfg.PIIEntityActions["SIP_URI"] != "hash" {
		t.Errorf("PIIEntityActions parse mismatch: %+v", cfg.PIIEntityActions)
	}
}
