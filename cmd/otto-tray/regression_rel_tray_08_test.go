//go:build darwin || windows

package main

import (
	"strings"
	"testing"
)

// TestRegression_REL_TRAY_08_ConfigErrorShortCircuit is the REL-TRAY-08
// regression test (D-18-09). The wrapper script writes a sentinel file
// at $HOME/.otto-gw/.config-error when dotenv parse fails; the tray
// poller reads it each tick and populates stateInput.ConfigError. The
// FSM must short-circuit at the top of computeState — sentinel content
// wins over PID/health probes — surfacing StateError + a "config error:"
// detail so the operator sees the parse error inline instead of a
// confusing "stopped" icon while polling the wrong port.
//
// Five cases enumerated in 18-03-PLAN.md Task 1 <behavior>:
//
//   A1 — sentinel content present       → StateError + "config error: " prefix
//   A2 — sentinel absent (empty field)  → not StateError (normal FSM path)
//   A3 — sentinel content > 200 bytes   → Detail capped to ≤ 200 bytes (after the prefix)
//   A4 — sentinel content multi-line    → only first line surfaces
//   A5 — sentinel + pidAlive + healthOK → sentinel wins (StateError, not Running)
//
// Cases A3 and A4 model what the poller does before assigning to
// stateInput.ConfigError (first-line trim + 200-byte cap per D-18-09 /
// CONTEXT.md PII minimization). The FSM under test only sees the
// already-trimmed string, so the test feeds pre-trimmed content directly
// to computeState. Sentinel-file read + trim + cap is exercised by the
// poller-side integration once the poller wiring lands; this test
// pins the FSM contract.
func TestRegression_REL_TRAY_08_ConfigErrorShortCircuit(t *testing.T) {
	t.Run("A1_SentinelPresent_StateError", func(t *testing.T) {
		got := computeState(stateInput{
			ConfigError: "syntax error on line 3: missing quote",
		})
		if got.State != StateError {
			t.Fatalf("ConfigError set → want %s, got %s", StateError, got.State)
		}
		if !strings.HasPrefix(got.Detail, "config error: ") {
			t.Fatalf("Detail must start with %q, got %q", "config error: ", got.Detail)
		}
		if !strings.Contains(got.Detail, "syntax error on line 3: missing quote") {
			t.Fatalf("Detail must contain sentinel text; got %q", got.Detail)
		}
	})

	t.Run("A2_SentinelAbsent_NotError", func(t *testing.T) {
		// PID dead → StateStopped (matches existing TestComputeState_StoppedWhenNoPIDAndNoHealth).
		got := computeState(stateInput{ConfigError: "", PIDAlive: false})
		if got.State == StateError {
			t.Fatalf("empty ConfigError must NOT short-circuit to StateError; got %s", got.State)
		}
		if got.State != StateStopped {
			t.Fatalf("empty ConfigError + no pid → want %s, got %s", StateStopped, got.State)
		}
	})

	t.Run("A3_DetailTruncated", func(t *testing.T) {
		// Simulate poller-side truncation: feed a 200-byte (max-after-trim)
		// payload and assert the full Detail (prefix + payload) is bounded
		// to "config error: " + 200 bytes.
		payload := strings.Repeat("x", 200)
		got := computeState(stateInput{ConfigError: payload})
		// Prefix is the fixed "config error: " literal (15 chars); the
		// trimmed sentinel payload is appended verbatim — the FSM does not
		// re-trim. Total Detail length is bounded above by
		// len("config error: ") + 200 = 215.
		if got.State != StateError {
			t.Fatalf("ConfigError truncated payload → want %s, got %s", StateError, got.State)
		}
		const maxDetailLen = len("config error: ") + 200
		if len(got.Detail) > maxDetailLen {
			t.Fatalf("Detail too long: %d > %d (sentinel must be capped at 200 bytes BEFORE assignment)", len(got.Detail), maxDetailLen)
		}
	})

	t.Run("A4_FirstLineOnly", func(t *testing.T) {
		// Simulate poller-side trim: feed only the first line (poller takes
		// strings.SplitN(content, "\n", 2)[0] before assigning); assert FSM
		// surfaces it verbatim without leaking any newline / "line2".
		got := computeState(stateInput{ConfigError: "line1"})
		if !strings.Contains(got.Detail, "line1") {
			t.Fatalf("Detail must contain first line; got %q", got.Detail)
		}
		if strings.Contains(got.Detail, "line2") || strings.Contains(got.Detail, "\n") {
			t.Fatalf("Detail must NOT contain subsequent lines or newlines; got %q", got.Detail)
		}
	})

	t.Run("A5_SentinelWinsOverPIDAndHealth", func(t *testing.T) {
		// PIDAlive + HealthOK + healthy snapshot would normally → StateRunning.
		// The sentinel must win — operator's config is broken, period.
		got := computeState(stateInput{
			ConfigError: "syntax error on line 3",
			PIDAlive:    true,
			HealthOK:    true,
			Snapshot:    Snapshot{PoolAlive: 4, PoolSize: 4},
		})
		if got.State != StateError {
			t.Fatalf("sentinel must win over PID/health probes; want %s, got %s", StateError, got.State)
		}
		if !strings.HasPrefix(got.Detail, "config error: ") {
			t.Fatalf("Detail prefix wrong: %q", got.Detail)
		}
	})
}
