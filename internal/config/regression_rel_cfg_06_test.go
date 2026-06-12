// Phase 18 Plan 18-01 Task 2 — Regression test for REL-CFG-06 (D-18-02).
//
// Finding REL-CFG-06: KIRO_CMD and KIRO_CWD env vars are not validated at
// config.Load() time. Misconfiguration surfaces 5–10s later as a raw
// exec.ErrNotFound / os.Stat ENOENT error from inside the kiro-cli
// subprocess spawn path — without naming the offending env var.
//
// Post-fix (D-18-02): config.Load() validates each var with a named,
// actionable error AND expands KIRO_CWD's leading ~ via os.UserHomeDir.
// KIRO_CMD does NOT get tilde expansion (binary names resolve via PATH).
//
// Named error strings (byte-exact):
//   - `config: KIRO_CMD ("<value>"): not found in PATH or unreadable`
//   - `config: KIRO_CWD ("<value>"): directory does not exist`
//   - `config: KIRO_CWD ("<value>"): not a directory`
//
// Tilde expansion: `~` or `~/sub` expanded via os.UserHomeDir +
// filepath.Join during config.Load(); expanded path stored in
// Config.KiroCWD.
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

// silenceForCFG06 sets the minimum env state required for config.Load()
// to reach the KIRO_CMD/KIRO_CWD validation branches without unrelated
// errors blocking the assertions. KIRO_CMD defaults to a known-present
// binary EXCEPT in subtests that deliberately set a bad value.
func silenceForCFG06(t *testing.T) {
	t.Helper()
	t.Setenv("PII_REDACTION_MODE", "replace")
	t.Setenv("AUTH_TOKEN", "")
	t.Setenv("ALLOWED_IPS", "")
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
}

// TestRegression_REL_CFG_06 covers the seven cases from the plan:
//
//	A: KIRO_CMD = bogus binary name → named "not found" error
//	B: KIRO_CMD = "go" (known-present) → no KIRO_CMD error
//	C: KIRO_CWD = "/nonexistent/path/zzz-18-01" → "directory does not exist"
//	D: KIRO_CWD = <path-to-a-regular-file> → "not a directory"
//	E: KIRO_CWD = "~/sub" with $HOME set and ~/sub created → no error,
//	   cfg.KiroCWD equals filepath.Join(home, "sub")
//	F: KIRO_CWD unset, KIRO_CMD = "go" → no KIRO_CWD error (Cwd is optional)
//	G: KIRO_CWD = "~" alone → expands to HOME exactly
func TestRegression_REL_CFG_06(t *testing.T) {
	// Case A
	t.Run("A_KIRO_CMD_bogus_named_error", func(t *testing.T) {
		silenceForCFG06(t)
		t.Setenv("KIRO_CMD", "definitely-not-a-real-binary-zzz")
		t.Setenv("KIRO_CWD", "")
		_, err := config.Load()
		if err == nil {
			t.Fatalf("expected named KIRO_CMD error, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, `config: KIRO_CMD ("definitely-not-a-real-binary-zzz")`) {
			t.Errorf("expected error to name KIRO_CMD and the offending value, got: %v", err)
		}
		if !strings.Contains(msg, "not found in PATH or unreadable") {
			t.Errorf("expected error to explain the failure, got: %v", err)
		}
	})

	// Case B
	t.Run("B_KIRO_CMD_known_present", func(t *testing.T) {
		silenceForCFG06(t)
		t.Setenv("KIRO_CMD", "go")
		t.Setenv("KIRO_CWD", "")
		_, err := config.Load()
		if err != nil && strings.Contains(err.Error(), "KIRO_CMD") {
			t.Errorf("did not expect KIRO_CMD error for 'go', got: %v", err)
		}
	})

	// Case C
	t.Run("C_KIRO_CWD_missing_named_error", func(t *testing.T) {
		silenceForCFG06(t)
		t.Setenv("KIRO_CMD", "go")
		t.Setenv("KIRO_CWD", "/nonexistent/path/zzz-18-01")
		_, err := config.Load()
		if err == nil {
			t.Fatalf("expected named KIRO_CWD error, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, `config: KIRO_CWD ("/nonexistent/path/zzz-18-01")`) {
			t.Errorf("expected error to name KIRO_CWD and the offending value, got: %v", err)
		}
		if !strings.Contains(msg, "directory does not exist") {
			t.Errorf("expected 'directory does not exist' phrasing, got: %v", err)
		}
	})

	// Case D — KIRO_CWD points at a regular file
	t.Run("D_KIRO_CWD_not_a_directory", func(t *testing.T) {
		silenceForCFG06(t)
		t.Setenv("KIRO_CMD", "go")
		tmp := t.TempDir()
		filePath := filepath.Join(tmp, "file")
		if err := os.WriteFile(filePath, []byte("hello"), 0o600); err != nil {
			t.Fatalf("setup file: %v", err)
		}
		t.Setenv("KIRO_CWD", filePath)

		_, err := config.Load()
		if err == nil {
			t.Fatalf("expected named KIRO_CWD error, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, `config: KIRO_CWD (`) || !strings.Contains(msg, filePath) {
			t.Errorf("expected error to name KIRO_CWD and the offending value, got: %v", err)
		}
		if !strings.Contains(msg, "not a directory") {
			t.Errorf("expected 'not a directory' phrasing, got: %v", err)
		}
	})

	// Case E — ~/sub expansion
	t.Run("E_KIRO_CWD_tilde_subdir_expansion", func(t *testing.T) {
		silenceForCFG06(t)
		t.Setenv("KIRO_CMD", "go")
		home := t.TempDir()
		t.Setenv("HOME", home)
		sub := filepath.Join(home, "sub")
		if err := os.MkdirAll(sub, 0o750); err != nil {
			t.Fatalf("mkdir sub: %v", err)
		}
		t.Setenv("KIRO_CWD", "~/sub")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("expected no error after ~ expansion, got: %v", err)
		}
		want := filepath.Join(home, "sub")
		if cfg.KiroCWD != want {
			t.Errorf("expected cfg.KiroCWD=%q after ~ expansion, got %q", want, cfg.KiroCWD)
		}
	})

	// Case F — KIRO_CWD unset / empty is the default and optional
	t.Run("F_KIRO_CWD_empty_is_optional", func(t *testing.T) {
		silenceForCFG06(t)
		t.Setenv("KIRO_CMD", "go")
		t.Setenv("KIRO_CWD", "")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("expected no error with empty KIRO_CWD, got: %v", err)
		}
		if cfg.KiroCWD != "" {
			t.Errorf("expected cfg.KiroCWD to remain empty, got %q", cfg.KiroCWD)
		}
	})

	// Case G — bare ~ expands to HOME
	t.Run("G_KIRO_CWD_bare_tilde_expands_to_HOME", func(t *testing.T) {
		silenceForCFG06(t)
		t.Setenv("KIRO_CMD", "go")
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("KIRO_CWD", "~")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("expected no error after bare ~ expansion, got: %v", err)
		}
		if cfg.KiroCWD != home {
			t.Errorf("expected cfg.KiroCWD=%q after bare ~ expansion, got %q", home, cfg.KiroCWD)
		}
	})

	// Case H (WR-02) — `~/..` style path escape is rejected with a
	// named error. Pre-fix: filepath.Join cleans the `..` and resolves
	// to the parent of HOME, which then passes the stat check (since
	// the parent is a real directory). Post-fix: any `..` segment in
	// the post-`~/` portion is refused.
	t.Run("H_KIRO_CWD_tilde_dotdot_rejected", func(t *testing.T) {
		silenceForCFG06(t)
		t.Setenv("KIRO_CMD", "go")
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("KIRO_CWD", "~/..")

		_, err := config.Load()
		if err == nil {
			t.Fatalf("expected KIRO_CWD ~/.. to be rejected, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, `config: KIRO_CWD ("~/..")`) {
			t.Errorf("expected error to name KIRO_CWD and the offending value, got: %v", err)
		}
		if !strings.Contains(msg, "'..' segments not permitted") {
			t.Errorf("expected error to explain '..' is not allowed, got: %v", err)
		}
	})

	// Case I (WR-02) — embedded `..` segment also rejected. The
	// segment-by-segment check must catch `..` even when it appears
	// between legitimate path components, e.g. `~/foo/../bar`.
	t.Run("I_KIRO_CWD_tilde_embedded_dotdot_rejected", func(t *testing.T) {
		silenceForCFG06(t)
		t.Setenv("KIRO_CMD", "go")
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("KIRO_CWD", "~/foo/../bar")

		_, err := config.Load()
		if err == nil {
			t.Fatalf("expected KIRO_CWD ~/foo/../bar to be rejected, got nil")
		}
		if !strings.Contains(err.Error(), "'..' segments not permitted") {
			t.Errorf("expected error to flag '..' segment, got: %v", err)
		}
	})

	// Case J (WR-02 boundary) — paths with `..` as substring of a real
	// segment (e.g. `~/foo..bar`) must NOT be rejected. The strict
	// segment match (seg == "..") is what makes this safe.
	t.Run("J_KIRO_CWD_dotdot_substring_in_segment_allowed", func(t *testing.T) {
		silenceForCFG06(t)
		t.Setenv("KIRO_CMD", "go")
		home := t.TempDir()
		t.Setenv("HOME", home)
		legitName := "foo..bar"
		legit := filepath.Join(home, legitName)
		if err := os.MkdirAll(legit, 0o750); err != nil {
			t.Fatalf("mkdir legit: %v", err)
		}
		t.Setenv("KIRO_CWD", "~/"+legitName)

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("expected no error for legitimate '..' substring, got: %v", err)
		}
		if cfg.KiroCWD != legit {
			t.Errorf("expected cfg.KiroCWD=%q, got %q", legit, cfg.KiroCWD)
		}
	})
}
