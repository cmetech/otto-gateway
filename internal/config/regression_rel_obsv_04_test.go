// Package config — regression for REL-OBSV-04 (D-18-08):
// Config.AdminTailPath as the single source of truth for the log-tail
// path.
//
// Pre-fix: the writer (chat-trace rotator at main.go:302) read from
// cfg.ChatTraceFile while the tailer (admin.NewTailer) derived its path
// from a parallel chain in main.go (LOG_FILE / GW_LOG / OTTO_LOG / default). The
// two chains could diverge if an operator set CHAT_TRACE_FILE but not
// LOG_FILE.
//
// Post-fix: Config.AdminTailPath is populated in config.Load via the
// SAME deriveChatTraceFile call used for ChatTraceFile, so the two
// fields hold the same string by construction. Both the writer and
// tailer downstream consumers read from this contract.
//
// Phase 18 Plan 02 — Task 3 (config half).
package config_test

import (
	"path/filepath"
	"testing"

	"otto-gateway/internal/config"
)

// TestRegression_REL_OBSV_04 covers C1/C2/C3 — the C4 (WARN on open
// failure) lives in internal/admin/regression_rel_obsv_04_test.go.
func TestRegression_REL_OBSV_04(t *testing.T) {
	t.Run("C1_default_derive_matches_ChatTraceFile", func(t *testing.T) {
		buf := captureSlogDefault(t)
		_ = buf
		silenceConfigLoadSideEffects(t)
		t.Setenv("LOG_FILE", "")
		t.Setenv("CHAT_TRACE_FILE", "")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.AdminTailPath == "" {
			t.Fatalf("AdminTailPath empty; want default-derived path")
		}
		if cfg.AdminTailPath != cfg.ChatTraceFile {
			t.Errorf("AdminTailPath=%q != ChatTraceFile=%q (must share deriveChatTraceFile source)",
				cfg.AdminTailPath, cfg.ChatTraceFile)
		}
	})

	t.Run("C2_explicit_CHAT_TRACE_FILE_honored", func(t *testing.T) {
		buf := captureSlogDefault(t)
		_ = buf
		silenceConfigLoadSideEffects(t)
		explicit := filepath.Join(t.TempDir(), "test-18-02-tail.log")
		t.Setenv("CHAT_TRACE_FILE", explicit)

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.AdminTailPath != explicit {
			t.Errorf("AdminTailPath=%q, want %q", cfg.AdminTailPath, explicit)
		}
		if cfg.ChatTraceFile != explicit {
			t.Errorf("ChatTraceFile=%q, want %q", cfg.ChatTraceFile, explicit)
		}
	})

	t.Run("C3_writer_tailer_parity", func(t *testing.T) {
		// C3 contract: cfg.AdminTailPath == cfg.ChatTraceFile in every
		// case so the writer (reads ChatTraceFile at main.go:302) and
		// tailer (reads AdminTailPath at the wiring site) cannot diverge.
		buf := captureSlogDefault(t)
		_ = buf
		silenceConfigLoadSideEffects(t)
		// Vary the LOG_FILE to exercise the derive branch.
		t.Setenv("LOG_FILE", "/var/log/otto/test-18-02-base.log")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.AdminTailPath != cfg.ChatTraceFile {
			t.Errorf("AdminTailPath=%q != ChatTraceFile=%q; D-18-08 single-source contract broken",
				cfg.AdminTailPath, cfg.ChatTraceFile)
		}
	})
}
