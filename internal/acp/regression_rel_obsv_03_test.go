// Package acp — regression for REL-OBSV-03 (D-18-04).
//
// Pre-fix: acp.Client wired `cmd.Stderr = os.Stderr` so kiro-cli stderr
// bypassed the gateway's structured logger and went straight to the
// process's standard error stream. Operators tailing the JSON log got
// nothing when kiro-cli emitted a warning or error.
//
// Post-fix: a dedicated goroutine reads kiro-cli stderr line by line via
// bufio.Reader.ReadString('\n') (NOT bufio.Scanner — Scanner stops on
// ErrTooLong). Each non-empty line is byte-capped at 1MB and emitted as
//
//	slog.Warn("kiro-cli stderr", "worker_pid", <pid>, "line", <text>)
//
// goleak.VerifyNone confirms the goroutine exits cleanly when the pipe
// closes (subprocess exit / Close()).
//
// Phase 18 Plan 02 — Task 1 Part A.
package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// fakeKiroCmd returns acp.Config args that invoke /bin/sh to emit the
// supplied stderr payload then exit 0. Mirrors the surrounding
// integration_test.go pattern of spawning a small shell subprocess in
// lieu of a real kiro-cli.
func fakeKiroCmd(script string) (string, []string) {
	return "/bin/sh", []string{"-c", script}
}

// newBufferLogger returns a logger writing JSON records to buf at the
// supplied level. Mirrors plugin.captureSlog.
func newBufferLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
}

// decodeStderrRecords splits buf into one JSON record per line and
// returns only records with msg=="kiro-cli stderr". Skips other records
// (the client emits debug/info around startup and teardown).
func decodeStderrRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
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
		if msg, _ := rec["msg"].(string); msg == "kiro-cli stderr" {
			out = append(out, rec)
		}
	}
	return out
}

// startAndDrain spawns an acp.Client against the supplied (cmd, args),
// waits for the subprocess to exit (acp.Client.Done() fires), then
// closes the client and returns the captured stderr records. The fake
// commands here never speak ACP — Initialize will not complete — but
// the stderr goroutine wiring is independent of the JSON-RPC dance.
func startAndDrain(t *testing.T, cmd string, args []string, level slog.Level) []map[string]any {
	t.Helper()
	buf := &bytes.Buffer{}
	cfg := Config{
		Logger:       newBufferLogger(buf, level),
		Command:      cmd,
		Args:         args,
		PingInterval: time.Hour, // suppress ping noise; test exits before tick.
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("acp.New: %v", err)
	}

	// Wait for the subprocess to exit (Done fires when the read/write
	// goroutines tear down after EOF). Bound by an outer deadline so
	// a hung fake does not stall CI.
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatalf("subprocess did not exit within 3s")
	}

	if err := c.Close(); err != nil {
		t.Logf("Close: %v (expected on fake-subprocess teardown)", err)
	}

	// Allow the stderr goroutine a beat to drain anything still buffered.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	for ctx.Err() == nil {
		recs := decodeStderrRecords(t, buf)
		if len(recs) > 0 {
			return recs
		}
		time.Sleep(20 * time.Millisecond)
	}
	return decodeStderrRecords(t, buf)
}

// TestRegression_REL_OBSV_03 covers four cases:
//
//	A1: single stderr line → exactly one "kiro-cli stderr" record.
//	A2: three stderr lines → three records, in order.
//	A3: goleak.VerifyNone after Close() — the goroutine exited.
//	A4: a >1MB un-newlined line is byte-capped at 1MB but the FOLLOWING
//	    tail line still appears (proves ReadString didn't stop on
//	    ErrTooLong as bufio.Scanner would have).
func TestRegression_REL_OBSV_03(t *testing.T) {
	t.Run("A1_single_line", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		cmd, args := fakeKiroCmd("printf 'test-stderr-line-18-02\\n' 1>&2")
		recs := startAndDrain(t, cmd, args, slog.LevelWarn)
		if len(recs) != 1 {
			t.Fatalf("got %d kiro-cli stderr records, want exactly 1; recs=%+v", len(recs), recs)
		}
		if recs[0]["line"] != "test-stderr-line-18-02" {
			t.Errorf("line field = %q, want %q", recs[0]["line"], "test-stderr-line-18-02")
		}
		if pid, ok := recs[0]["worker_pid"].(float64); !ok || pid <= 0 {
			t.Errorf("worker_pid field = %v (%T), want positive integer", recs[0]["worker_pid"], recs[0]["worker_pid"])
		}
		if lvl, _ := recs[0]["level"].(string); lvl != "WARN" {
			t.Errorf("level = %q, want WARN", lvl)
		}
	})

	t.Run("A2_three_lines", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		cmd, args := fakeKiroCmd("printf 'l1\\nl2\\nl3\\n' 1>&2")
		recs := startAndDrain(t, cmd, args, slog.LevelWarn)
		if len(recs) != 3 {
			t.Fatalf("got %d records, want 3; recs=%+v", len(recs), recs)
		}
		want := []string{"l1", "l2", "l3"}
		for i, w := range want {
			if recs[i]["line"] != w {
				t.Errorf("rec[%d].line = %q, want %q", i, recs[i]["line"], w)
			}
		}
	})

	t.Run("A4_line_too_long_followed_by_tail", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		// Emit ~2 MB of 'X' (no newline), then a newline, then a tail line.
		// bufio.Scanner would have stopped on ErrTooLong; ReadString
		// continues, so the tail line MUST still arrive.
		script := `awk 'BEGIN{
			for (i = 0; i < 2*1024*1024; i++) printf "X";
			printf "\ntail-line\n";
		}' 1>&2`
		cmd, args := fakeKiroCmd(script)
		recs := startAndDrain(t, cmd, args, slog.LevelWarn)
		if len(recs) < 2 {
			t.Fatalf("got %d records, want >= 2; recs (truncated)=%+v", len(recs), summarizeRecords(recs))
		}
		// First record: 1MB-capped huge line.
		first, _ := recs[0]["line"].(string)
		if got, want := len(first), 1024*1024; got != want {
			t.Errorf("huge line length = %d, want %d (1 MB byte-cap)", got, want)
		}
		// Last record: the tail-line, untouched.
		last, _ := recs[len(recs)-1]["line"].(string)
		if last != "tail-line" {
			t.Errorf("last line = %q, want %q", last, "tail-line")
		}
	})

	// A5 regression for WR-09: when the WR-04 UTF-8 walk-back fires
	// (any time the 1MB cap truncates a stderr line), the slog record
	// MUST include `truncated: true` and a positive `dropped_bytes`
	// field so the operator has telemetry on the silent truncation.
	// The pre-fix WR-04 path dropped bytes invisibly — in the
	// pathological "all continuation bytes" case (invalid UTF-8), the
	// walk-back can land on n==0 and surface a "line": "" record with
	// no signal at all. The base D-18-04 field set (worker_pid, line)
	// MUST remain present alongside the new fields.
	t.Run("A5_truncation_telemetry_wr_09", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		// Emit ~2 MB of 'X' (no newline) then a newline. The cap fires;
		// `dropped_bytes` should be ~1 MB; `truncated` should be true.
		script := `awk 'BEGIN{
			for (i = 0; i < 2*1024*1024; i++) printf "X";
			printf "\n";
		}' 1>&2`
		cmd, args := fakeKiroCmd(script)
		recs := startAndDrain(t, cmd, args, slog.LevelWarn)
		if len(recs) < 1 {
			t.Fatalf("got %d records, want >= 1; recs=%+v", len(recs), summarizeRecords(recs))
		}
		rec := recs[0]
		// Base D-18-04 fields preserved.
		if _, ok := rec["worker_pid"]; !ok {
			t.Errorf("worker_pid field missing — D-18-04 contract regressed: rec=%+v", summarizeRecords([]map[string]any{rec}))
		}
		if _, ok := rec["line"]; !ok {
			t.Errorf("line field missing — D-18-04 contract regressed: rec=%+v", summarizeRecords([]map[string]any{rec}))
		}
		// WR-09: telemetry fields present and meaningful.
		truncated, ok := rec["truncated"].(bool)
		if !ok || !truncated {
			t.Errorf("truncated field = %v (%T), want true; rec=%+v", rec["truncated"], rec["truncated"], summarizeRecords([]map[string]any{rec}))
		}
		dropped, ok := rec["dropped_bytes"].(float64)
		if !ok || dropped <= 0 {
			t.Errorf("dropped_bytes field = %v (%T), want positive number; rec=%+v", rec["dropped_bytes"], rec["dropped_bytes"], summarizeRecords([]map[string]any{rec}))
		}
		// The producer emitted ~2 MB; the cap is 1 MB; so dropped ≈ 1 MB.
		// Allow some slack for the walk-back (≤ 3 bytes) and producer
		// counter-vs-actual-bytes drift.
		if dropped < 1000*1000 {
			t.Errorf("dropped_bytes = %v, want >= 1_000_000 (producer emitted 2MB, cap is 1MB)", dropped)
		}
	})

	// A6 regression for WR-09: when the line is short enough that no
	// truncation fires, the new telemetry fields MUST NOT be present.
	// The D-18-04 base field set is the steady-state contract; adding
	// `truncated`/`dropped_bytes` to every record would bloat logs and
	// break operators who pattern-match on field cardinality.
	t.Run("A6_no_truncation_no_telemetry_fields", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		cmd, args := fakeKiroCmd("printf 'short-line\\n' 1>&2")
		recs := startAndDrain(t, cmd, args, slog.LevelWarn)
		if len(recs) != 1 {
			t.Fatalf("got %d records, want 1; recs=%+v", len(recs), recs)
		}
		rec := recs[0]
		if _, ok := rec["truncated"]; ok {
			t.Errorf("truncated field present on non-truncated line — should be absent; rec=%+v", rec)
		}
		if _, ok := rec["dropped_bytes"]; ok {
			t.Errorf("dropped_bytes field present on non-truncated line — should be absent; rec=%+v", rec)
		}
	})
}

// summarizeRecords produces a compact view of records that does not
// dump megabytes when an oversized line gets logged. Used only in test
// failure messages.
func summarizeRecords(recs []map[string]any) []map[string]any {
	out := make([]map[string]any, len(recs))
	for i, r := range recs {
		clone := make(map[string]any, len(r))
		for k, v := range r {
			if s, ok := v.(string); ok && len(s) > 80 {
				clone[k] = s[:80] + "...(truncated)"
			} else {
				clone[k] = v
			}
		}
		out[i] = clone
	}
	return out
}
