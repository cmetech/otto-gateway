// Phase 18 Plan 18-01 Task 1 — Regression test for REL-CFG-05 (D-18-01).
//
// Finding REL-CFG-05: ALLOWED_IPS / AUTH_TOKEN containing only whitespace
// or only CSV delimiters (",", ",,", " , ") silently disable auth /
// allowlist with no operator visibility. Pre-fix observable: config.Load()
// emits no Warn record naming the degenerate variable; cfg.AuthToken /
// cfg.AllowedIPs are returned as empty slices and the auth-state INFO line
// at main.go:115-120 prints `enabled=false ip_allowlist=false` — the
// operator who set the var intending a value sees no signal that their
// input was unusable.
//
// Post-fix (D-18-01 locked decision): config.Load() emits a slog.Warn
// record from slog.Default() naming the degenerate env var and treats it
// as unset (auth off / allowlist off). This preserves the documented
// CLAUDE.md "no auth if env unset" posture (no fail-fast) while making the
// silent-disable loud in the logs.
//
// The new Warn lines are byte-exact:
//   - "AUTH_TOKEN looks degenerate (no entries after trim+CSV split); treating as unset"
//   - "ALLOWED_IPS looks degenerate (no entries after trim+CSV split); treating as unset"
//
// This test reuses captureSlogDefault + decodeLogRecords from
// regression_rel_cfg_03_test.go (same package).
package config_test

import (
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

const (
	wantAuthDegenerateMsg    = "AUTH_TOKEN looks degenerate (no entries after trim+CSV split); treating as unset"
	wantAllowedDegenerateMsg = "ALLOWED_IPS looks degenerate (no entries after trim+CSV split); treating as unset"
)

// silenceConfigLoadSideEffects sets the minimum env state required for
// config.Load() to succeed without firing unrelated validation errors
// (PII_ENCRYPT_KEY required because default PII_REDACTION_MODE=encrypt).
// Tasks 2 and 3 of this plan will introduce KIRO_CMD / HTTP_ADDR validation
// in the same config.Load(); set those here too so Task 1 subtests do not
// become brittle once those branches land.
func silenceConfigLoadSideEffects(t *testing.T) {
	t.Helper()
	t.Setenv("PII_REDACTION_MODE", "replace")
	t.Setenv("KIRO_CMD", "go") // guaranteed in PATH on dev + CI
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
}

// findRecordWithMsg returns the first decoded record whose msg field
// equals want (exact match). Returns nil if not found.
func findRecordWithMsg(recs []map[string]any, want string) map[string]any {
	for _, r := range recs {
		if msg, _ := r["msg"].(string); msg == want {
			return r
		}
	}
	return nil
}

// TestRegression_REL_CFG_05 covers six cases per the plan:
//
//	A: AUTH_TOKEN=" , "  → Warn emitted; auth disabled
//	B: ALLOWED_IPS=",,  ," → Warn emitted; allowlist disabled
//	C: AUTH_TOKEN="real-token" → no degenerate Warn; auth enabled
//	D: ALLOWED_IPS="127.0.0.1/32" → no degenerate Warn; allowlist populated
//	E: AUTH_TOKEN and ALLOWED_IPS both unset → no degenerate Warns
//	F: AUTH_TOKEN="   " (whitespace only) → Warn emitted; auth disabled
func TestRegression_REL_CFG_05(t *testing.T) {
	cases := []struct {
		name                  string
		setEnv                map[string]string
		unsetEnv              []string
		wantAuthDegenerate    bool
		wantAllowedDegenerate bool
		wantAuthEmpty         bool
		wantAllowedEmpty      bool
	}{
		{
			name:               "A_AUTH_TOKEN_only_delimiters_and_whitespace",
			setEnv:             map[string]string{"AUTH_TOKEN": " , "},
			unsetEnv:           []string{"ALLOWED_IPS"},
			wantAuthDegenerate: true,
			wantAuthEmpty:      true,
			wantAllowedEmpty:   true,
		},
		{
			name:                  "B_ALLOWED_IPS_only_delimiters_and_whitespace",
			setEnv:                map[string]string{"ALLOWED_IPS": ",,  ,"},
			unsetEnv:              []string{"AUTH_TOKEN"},
			wantAllowedDegenerate: true,
			wantAuthEmpty:         true,
			wantAllowedEmpty:      true,
		},
		{
			name:             "C_AUTH_TOKEN_real_value",
			setEnv:           map[string]string{"AUTH_TOKEN": "real-token"},
			unsetEnv:         []string{"ALLOWED_IPS"},
			wantAuthEmpty:    false,
			wantAllowedEmpty: true,
		},
		{
			name:             "D_ALLOWED_IPS_real_value",
			setEnv:           map[string]string{"ALLOWED_IPS": "127.0.0.1/32"},
			unsetEnv:         []string{"AUTH_TOKEN"},
			wantAuthEmpty:    true,
			wantAllowedEmpty: false,
		},
		{
			name:             "E_both_unset_is_normal_case",
			unsetEnv:         []string{"AUTH_TOKEN", "ALLOWED_IPS"},
			wantAuthEmpty:    true,
			wantAllowedEmpty: true,
		},
		{
			name:               "F_AUTH_TOKEN_whitespace_only",
			setEnv:             map[string]string{"AUTH_TOKEN": "   "},
			unsetEnv:           []string{"ALLOWED_IPS"},
			wantAuthDegenerate: true,
			wantAuthEmpty:      true,
			wantAllowedEmpty:   true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			buf := captureSlogDefault(t)
			silenceConfigLoadSideEffects(t)
			// t.Setenv with "" sets the env var to the empty string. For
			// config.go's getEnvStr / getEnvStrSliceComma path this is
			// indistinguishable from "unset" because TrimSpace("") == "".
			for _, k := range tc.unsetEnv {
				t.Setenv(k, "")
			}
			for k, v := range tc.setEnv {
				t.Setenv(k, v)
			}

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load() unexpected error: %v", err)
			}

			recs := decodeLogRecords(t, buf)

			authRec := findRecordWithMsg(recs, wantAuthDegenerateMsg)
			allowedRec := findRecordWithMsg(recs, wantAllowedDegenerateMsg)

			if tc.wantAuthDegenerate {
				if authRec == nil {
					t.Errorf("expected AUTH_TOKEN degenerate Warn record %q, got none. records=%+v", wantAuthDegenerateMsg, recs)
				} else if lvl, _ := authRec["level"].(string); !strings.EqualFold(lvl, "warn") {
					t.Errorf("expected AUTH_TOKEN degenerate record at WARN level, got level=%q", lvl)
				}
			} else if authRec != nil {
				t.Errorf("did NOT expect AUTH_TOKEN degenerate Warn, got %+v", authRec)
			}

			if tc.wantAllowedDegenerate {
				if allowedRec == nil {
					t.Errorf("expected ALLOWED_IPS degenerate Warn record %q, got none. records=%+v", wantAllowedDegenerateMsg, recs)
				} else if lvl, _ := allowedRec["level"].(string); !strings.EqualFold(lvl, "warn") {
					t.Errorf("expected ALLOWED_IPS degenerate record at WARN level, got level=%q", lvl)
				}
			} else if allowedRec != nil {
				t.Errorf("did NOT expect ALLOWED_IPS degenerate Warn, got %+v", allowedRec)
			}

			if tc.wantAuthEmpty && len(cfg.AuthToken) != 0 {
				t.Errorf("expected cfg.AuthToken to be empty, got %+v", cfg.AuthToken)
			}
			if !tc.wantAuthEmpty && len(cfg.AuthToken) == 0 {
				t.Errorf("expected cfg.AuthToken to be populated, got empty")
			}
			if tc.wantAllowedEmpty && len(cfg.AllowedIPs) != 0 {
				t.Errorf("expected cfg.AllowedIPs to be empty, got %+v", cfg.AllowedIPs)
			}
			if !tc.wantAllowedEmpty && len(cfg.AllowedIPs) == 0 {
				t.Errorf("expected cfg.AllowedIPs to be populated, got empty")
			}
		})
	}
}
