// Package admin — regression for REL-OBSV-04 (D-18-08): tailer
// open-failure logs at WARN (not DEBUG).
//
// Pre-fix: when the log file pointed at by t.path did not exist (e.g.,
// pre-LOG_FILE-rotation or operator misconfigured the path) the Tailer
// emitted a DEBUG record — invisible at INFO+ — leaving operators with
// an empty Log Tail panel and no diagnostic.
//
// Post-fix: the open-failure log is promoted to WARN with the resolved
// path field so operators see exactly which path missed. The Config-side
// half (Config.AdminTailPath) is covered by the config-package
// regression test.
//
// Phase 18 Plan 02 — Task 3 (admin half).
package admin

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRegression_REL_OBSV_04_TailerOpenFailureWarn covers C4: with a
// path that does not exist, the Tailer's reopen() emits a WARN log
// (not Debug) with the resolved path field.
func TestRegression_REL_OBSV_04_TailerOpenFailureWarn(t *testing.T) {
	buf := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	missing := filepath.Join(t.TempDir(), "never-written.log")
	tail := NewTailer(missing, logger)

	sub := tail.Subscribe(context.Background())
	t.Cleanup(func() { tail.Unsubscribe(sub) })

	// Poll up to 2s for a Warn-level record with msg matching the
	// open-failure pattern and a path field equal to `missing`.
	deadline := time.Now().Add(2 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(buf.String(), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if !strings.Contains(line, "tailer cannot open log") {
				continue
			}
			// Require WARN level — DEBUG is the pre-fix behavior we're
			// closing out.
			if !strings.Contains(line, `"level":"WARN"`) {
				continue
			}
			if !strings.Contains(line, `"path":"`+missing+`"`) {
				continue
			}
			found = true
			break
		}
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("no WARN-level 'admin: tailer cannot open log' record with path=%q within 2s; buf=%s", missing, buf.String())
	}

	// Several identical 250ms retries remain within the limiter interval and
	// must not generate one warning per poll.
	time.Sleep(3 * TailPollInterval)
	if got := strings.Count(buf.String(), "tailer cannot open log"); got != 1 {
		t.Fatalf("open warning count = %d, want 1 after repeated polls; buf=%s", got, buf.String())
	}

	if err := os.WriteFile(missing, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	recoveryDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(recoveryDeadline) {
		if strings.Count(buf.String(), "tailer log source recovered") == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("tailer did not log one recovery after file creation; buf=%s", buf.String())
}
