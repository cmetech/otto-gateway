// Package ollama — regression for REL-HTTP-06 (D-18-06).
//
// Pre-fix: when engine.Run returned an error on the streaming /api/chat or
// /api/generate path, the Ollama adapter wrote a 500 response with the
// raw error string but emitted NO structured log line. The REL-HTTP-03
// site (finalizeNDJSON's mid-stream worker-death path) had a symmetric
// WARN already; this site did not.
//
// Post-fix: a slog.Warn("ollama: streaming eng.Run failed", ...) is emitted
// BEFORE writeError. Field set mirrors REL-HTTP-03 (ndjson.go:567-578):
//
//	session_id, worker_pid (0 placeholder — RunHandle exposes no Pid),
//	bytes_streamed (0 — Run failed before any chunk), request_id, err,
//	kiro_exit_code (ONLY when errors.As reveals an *exec.ExitError).
//
// Phase 18 Plan 02 — Task 1 Part B.
package ollama

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// newCapturingLogger returns a JSON-handler-backed *slog.Logger plus the
// buffer it writes to. Mirrors plugin.captureSlog (this package can't
// import internal/plugin's test scope).
func newCapturingLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// decodeRecordsByMsg returns all decoded slog records whose msg matches
// want, in order.
func decodeRecordsByMsg(t *testing.T, buf *bytes.Buffer, want string) []map[string]any {
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
		if msg, _ := rec["msg"].(string); msg == want {
			out = append(out, rec)
		}
	}
	return out
}

// realExitError produces a real *exec.ExitError (exit code 1) by running
// `sh -c 'exit 1'` and reading its Wait result. Used by B2 to assert
// kiro_exit_code is reported when errors.As finds an *exec.ExitError in
// the chain. Uses /bin/sh -c rather than /bin/false because the latter
// lives at /usr/bin/false on darwin and is absent from some minimal CI
// images.
func realExitError(t *testing.T) error {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit 1")
	err := cmd.Run()
	if err == nil {
		t.Fatal("`sh -c 'exit 1'` unexpectedly succeeded; cannot build a real *exec.ExitError")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *exec.ExitError, got %T (%v)", err, err)
	}
	return err
}

// TestRegression_REL_HTTP_06 covers three cases:
//
//	B1 (generic error): eng.Run returns errors.New("synthetic"). Adapter
//	    emits exactly one slog.Warn("ollama: streaming eng.Run failed", ...)
//	    BEFORE the 500 response. kiro_exit_code is NOT present.
//	B2 (exit error):    eng.Run returns a real *exec.ExitError. Warn record
//	    includes kiro_exit_code with the exit code.
//	B3 (non-exit err, alias of B1): kiro_exit_code field is omitted.
func TestRegression_REL_HTTP_06(t *testing.T) {
	t.Run("B1_generic_error_no_exit_code", func(t *testing.T) {
		logger, buf := newCapturingLogger()
		eng := &fakeEngine{runErr: errors.New("synthetic engine run failure")}
		a := New(Config{Engine: eng, Logger: logger})

		body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
		w := postToProtected(t, a, "/chat", body)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("status: got %d, want 500", w.Code)
		}

		recs := decodeRecordsByMsg(t, buf, "ollama: streaming eng.Run failed")
		if len(recs) != 1 {
			t.Fatalf("got %d warn records, want exactly 1; buf=%s", len(recs), buf.String())
		}
		r := recs[0]
		if lvl, _ := r["level"].(string); lvl != "WARN" {
			t.Errorf("level = %q, want WARN", lvl)
		}
		for _, key := range []string{"session_id", "worker_pid", "bytes_streamed", "request_id", "err"} {
			if _, ok := r[key]; !ok {
				t.Errorf("missing field %q in record %+v", key, r)
			}
		}
		if got, want := r["worker_pid"], float64(0); got != want {
			t.Errorf("worker_pid = %v, want %v (placeholder per RESEARCH.md A4)", got, want)
		}
		if got, want := r["bytes_streamed"], float64(0); got != want {
			t.Errorf("bytes_streamed = %v, want %v (placeholder)", got, want)
		}
		if _, present := r["kiro_exit_code"]; present {
			t.Errorf("kiro_exit_code should be ABSENT for generic error; got %v", r["kiro_exit_code"])
		}
	})

	t.Run("B2_exit_error_includes_exit_code", func(t *testing.T) {
		logger, buf := newCapturingLogger()
		exitErr := realExitError(t)
		// Wrap so errors.As must walk the chain (closer to real production
		// path where engine code wraps the underlying acp/exec error).
		eng := &fakeEngine{runErr: errors.Join(errors.New("engine: prompt"), exitErr)}
		a := New(Config{Engine: eng, Logger: logger})

		body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
		w := postToProtected(t, a, "/chat", body)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("status: got %d, want 500", w.Code)
		}

		recs := decodeRecordsByMsg(t, buf, "ollama: streaming eng.Run failed")
		if len(recs) != 1 {
			t.Fatalf("got %d warn records, want exactly 1; buf=%s", len(recs), buf.String())
		}
		r := recs[0]
		code, present := r["kiro_exit_code"]
		if !present {
			t.Fatalf("kiro_exit_code MUST be present when err chain contains *exec.ExitError; got record %+v", r)
		}
		// /bin/false exits 1.
		if got, want := code, float64(1); got != want {
			t.Errorf("kiro_exit_code = %v, want %v", got, want)
		}
	})

	t.Run("B3_non_exit_error_omits_exit_code", func(t *testing.T) {
		// B3 == B1 in shape but explicit about the contract.
		logger, buf := newCapturingLogger()
		eng := &fakeEngine{runErr: errors.New("not an exit error")}
		a := New(Config{Engine: eng, Logger: logger})

		body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
		_ = postToProtected(t, a, "/chat", body)

		recs := decodeRecordsByMsg(t, buf, "ollama: streaming eng.Run failed")
		if len(recs) != 1 {
			t.Fatalf("got %d records, want 1", len(recs))
		}
		if _, present := recs[0]["kiro_exit_code"]; present {
			t.Errorf("kiro_exit_code should be ABSENT; got %v", recs[0]["kiro_exit_code"])
		}
	})
}

// postToProtected posts JSON to the adapter's protected router.
func postToProtected(t *testing.T, a *Adapter, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(rec, req)
	return rec
}
