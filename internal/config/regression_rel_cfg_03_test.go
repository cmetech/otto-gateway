// Phase 14 Plan 14-04 Task 4 — Regression test for REL-CFG-03 (C-3 Medium).
//
// Finding C-3: EMBEDDING_MODEL_DEFAULT is documented in CLAUDE.md as a
// backward-compat env var but is never read anywhere in the codebase. A
// deployment that sets this var gets no indication it is silently ignored.
//
// Pre-fix observable: t.Setenv("EMBEDDING_MODEL_DEFAULT", "qwen3-embed"),
// then config.Load() succeeds, and the default slog logger emits NO Warn
// record mentioning the variable name — the var is silently ignored.
//
// Post-fix (Phase 16): config.Load() or the boot sequence emits a startup
// Warn logging "EMBEDDING_MODEL_DEFAULT set but embeddings are not
// implemented" (D-decision per CONTEXT.md success criterion 13) so operators
// are informed their config key is unused.
package config_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

// captureSlogDefault installs a JSON handler as slog.Default and returns
// the buffer. Defers restoration of the previous default. Used by C-3 and
// O-1 tests where config.Load() (which takes no logger arg) must be
// observed through slog.Default().
func captureSlogDefault(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// decodeLogRecords splits the buffer into one JSON record per line and
// returns the decoded maps. Skips blank lines.
func decodeLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode slog record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// TestRegression_REL_CFG_03_EmbeddingModelDefaultUnimplemented verifies that
// setting EMBEDDING_MODEL_DEFAULT produces a startup Warn log record mentioning
// the variable name (post-fix), rather than being silently ignored (pre-fix).
func TestRegression_REL_CFG_03_EmbeddingModelDefaultUnimplemented(t *testing.T) {
	buf := captureSlogDefault(t)
	t.Setenv("EMBEDDING_MODEL_DEFAULT", "qwen3-embed")

	_, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	// Pre-fix: no Warn record mentioning EMBEDDING_MODEL_DEFAULT exists
	// because the var is never read anywhere in the codebase (confirmed by
	// repo-wide grep: no occurrences in internal/ or cmd/).
	//
	// Post-fix: config.Load() or boot sequence emits a Warn record with
	// msg containing "EMBEDDING_MODEL_DEFAULT" or the message text from
	// CONTEXT.md success criterion 13.
	recs := decodeLogRecords(t, buf)
	for _, r := range recs {
		level, _ := r["level"].(string)
		msg, _ := r["msg"].(string)
		if strings.EqualFold(level, "warn") &&
			strings.Contains(msg, "EMBEDDING_MODEL_DEFAULT") {
			// Post-fix state: Warn record found.
			t.Logf("post-fix: found Warn record for EMBEDDING_MODEL_DEFAULT: %+v", r)
			return
		}
	}

	// Pre-fix state: no Warn record emitted. This is the bug.
	t.Log("pre-fix confirmed: EMBEDDING_MODEL_DEFAULT silently ignored (no Warn record)")
	t.Errorf("expected a Warn slog record mentioning EMBEDDING_MODEL_DEFAULT, got none (pre-fix state)")
}
