// Phase 8 Plan 08-03 Task 3 — Wave 0 scaffold for LoggingHook.
//
// These tests exercise the contract the Task 4 implementation must satisfy:
//   - Pre emits a "plugin.before" slog record carrying request_id, model,
//     message_count.
//   - Post emits a "plugin.after" slog record carrying request_id,
//     duration_ms (computed from a ctx-bridge stash set by Pre), and
//     stop_reason.
//   - Post is nil-response-safe (Pitfall 8 from 08-RESEARCH).
//   - Nil Logger falls back to slog.Default() without panic
//     (T-8-LEAK-3 / Pitfall 5).
//   - When pii.SummaryFromContext returns a populated Summary, Post emits
//     it as a structured "redacted" attr.
//   - When no Summary is on ctx (slice 4 not yet wired OR PII disabled),
//     Post OMITS the "redacted" attr (graceful degradation).
//   - Source audit: logging.go contains NO slog.Any("messages", req...)
//     pattern (T-8-PII enforcement at source level).
//   - Name() reports "LoggingHook".
//   - Describe() reports kind "Pre,Post" with a "level" config field.
//
// All 9 tests must fail before Task 4 (logging.go) lands.

package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin/pii"
)

// captureSlog returns a JSON-handler-backed *slog.Logger and the buffer it
// writes to. Tests use this to assert exact attribute layout via JSON
// decode rather than string-matching.
func captureSlog(_ *testing.T) (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// decodeRecords splits the buffer into one JSON record per line and
// returns the decoded maps. Skips blank lines.
func decodeRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
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

// findRecord returns the first record with msg == name, or fails the test.
func findRecord(t *testing.T, recs []map[string]any, name string) map[string]any {
	t.Helper()
	for _, r := range recs {
		if r["msg"] == name {
			return r
		}
	}
	t.Fatalf("no slog record with msg=%q; got %d records: %+v", name, len(recs), recs)
	return nil
}

// TestLoggingHook_Before_EmitsCorrelatedRecord asserts the Pre record
// shape: msg=plugin.before, request_id, model, message_count.
func TestLoggingHook_Before_EmitsCorrelatedRecord(t *testing.T) {
	logger, buf := captureSlog(t)
	hook := &LoggingHook{Logger: logger}
	ctx := WithRequestID(context.Background(), "TEST-RID")
	req := &canonical.ChatRequest{
		Model: "auto",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser},
			{Role: canonical.RoleUser},
		},
	}

	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: unexpected err: %v", err)
	}

	recs := decodeRecords(t, buf)
	rec := findRecord(t, recs, "plugin.before")
	if rec["request_id"] != "TEST-RID" {
		t.Errorf("request_id: got %v, want TEST-RID", rec["request_id"])
	}
	if rec["model"] != "auto" {
		t.Errorf("model: got %v, want auto", rec["model"])
	}
	// JSON numbers decode to float64.
	if mc, _ := rec["message_count"].(float64); mc != 2 {
		t.Errorf("message_count: got %v, want 2", rec["message_count"])
	}
}

// TestLoggingHook_After_EmitsDurationAndStopReason asserts the Post record
// includes request_id, duration_ms ≥ sleep window, and stop_reason.
func TestLoggingHook_After_EmitsDurationAndStopReason(t *testing.T) {
	logger, buf := captureSlog(t)
	hook := &LoggingHook{Logger: logger}
	ctx := WithRequestID(context.Background(), "TEST-RID")
	req := &canonical.ChatRequest{Model: "auto"}
	resp := &canonical.ChatResponse{StopReason: canonical.StopEndTurn}

	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := hook.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}

	recs := decodeRecords(t, buf)
	rec := findRecord(t, recs, "plugin.after")
	if rec["request_id"] != "TEST-RID" {
		t.Errorf("request_id: got %v, want TEST-RID", rec["request_id"])
	}
	// duration_ms is JSON number (float64 after decode); lower-bound to
	// 4ms (slop) so the test isn't flaky on slow CI.
	dur, _ := rec["duration_ms"].(float64)
	if dur < 4 {
		t.Errorf("duration_ms: got %v, want >= 4 (slept 5ms)", rec["duration_ms"])
	}
	// StopReason is canonical.StopReason (int). The hook may render it as
	// the int value or via String() — assert it is present and non-empty.
	if _, ok := rec["stop_reason"]; !ok {
		t.Errorf("stop_reason missing from record: %+v", rec)
	}
}

// TestLoggingHook_After_NilResponseSafe asserts Post does not panic when
// resp is nil (Pitfall 8 from 08-RESEARCH: PostHook MAY see nil resp on
// engine error paths even though Codex H-5 says assembled response).
func TestLoggingHook_After_NilResponseSafe(t *testing.T) {
	logger, buf := captureSlog(t)
	hook := &LoggingHook{Logger: logger}
	ctx := WithRequestID(context.Background(), "TEST-RID")
	req := &canonical.ChatRequest{Model: "auto"}

	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("After(_, _, nil) panicked: %v", r)
		}
	}()
	if err := hook.After(ctx, req, nil); err != nil {
		t.Errorf("After(_, _, nil): unexpected err: %v", err)
	}

	recs := decodeRecords(t, buf)
	rec := findRecord(t, recs, "plugin.after")
	if rec == nil {
		t.Fatal("plugin.after record missing")
	}
}

// TestLoggingHook_NilLogger_FallsBackToDefault asserts a nil Logger does
// not NPE; records go to slog.Default(). We do not assert capture; only
// no-panic (T-8-LEAK-3 mitigation — never slog.SetDefault, but nil-safe
// fallback to slog.Default is fine).
func TestLoggingHook_NilLogger_FallsBackToDefault(t *testing.T) {
	hook := &LoggingHook{Logger: nil}
	ctx := WithRequestID(context.Background(), "TEST-RID")
	req := &canonical.ChatRequest{Model: "auto"}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil-Logger Before panicked: %v", r)
		}
	}()
	if _, err := hook.Before(ctx, req); err != nil {
		t.Errorf("nil-Logger Before: %v", err)
	}
}

// TestLoggingHook_EmitsRedactedSummary_WhenPresent asserts the D-04 seam
// integration: a Summary stamped on ctx becomes the "redacted" attr on
// the Post record.
func TestLoggingHook_EmitsRedactedSummary_WhenPresent(t *testing.T) {
	logger, buf := captureSlog(t)
	hook := &LoggingHook{Logger: logger}
	s := pii.NewSummary()
	s.Add("Email")
	s.Add("Email")
	s.Add("SSN")
	ctx := WithRequestID(context.Background(), "TEST-RID")
	ctx = pii.WithSummary(ctx, s)
	req := &canonical.ChatRequest{Model: "auto"}
	resp := &canonical.ChatResponse{StopReason: canonical.StopEndTurn}

	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if err := hook.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}

	recs := decodeRecords(t, buf)
	rec := findRecord(t, recs, "plugin.after")
	redacted, ok := rec["redacted"].(map[string]any)
	if !ok {
		t.Fatalf("redacted attr missing or wrong type: %+v", rec["redacted"])
	}
	if email, _ := redacted["Email"].(float64); email != 2 {
		t.Errorf("redacted.Email: got %v, want 2", redacted["Email"])
	}
	if ssn, _ := redacted["SSN"].(float64); ssn != 1 {
		t.Errorf("redacted.SSN: got %v, want 1", redacted["SSN"])
	}
}

// TestLoggingHook_OmitsRedactedSummary_WhenAbsent asserts graceful
// degradation before slice 4 wires PIIRedactionHook: no Summary on ctx →
// no "redacted" attr on the Post record.
func TestLoggingHook_OmitsRedactedSummary_WhenAbsent(t *testing.T) {
	logger, buf := captureSlog(t)
	hook := &LoggingHook{Logger: logger}
	ctx := WithRequestID(context.Background(), "TEST-RID")
	req := &canonical.ChatRequest{Model: "auto"}
	resp := &canonical.ChatResponse{StopReason: canonical.StopEndTurn}

	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if err := hook.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}

	recs := decodeRecords(t, buf)
	rec := findRecord(t, recs, "plugin.after")
	if _, present := rec["redacted"]; present {
		t.Errorf("redacted attr should be absent when no Summary on ctx; got %+v", rec["redacted"])
	}
}

// TestLoggingHook_SourceAudit_NoRawContent enforces T-8-PII at the source
// level: logging.go MUST NOT pass req.Messages (or req.Messages[i].Content)
// to any slog call. The threat is "raw PII leaks into log records because
// the implementer serialized the whole request"; this test makes the
// regression detectable without running the hook against real PII.
//
// The audit reads the source file and rejects forbidden patterns. If
// logging.go does not exist yet (pre-Task-4 RED state), the test SKIPS;
// Task 4 brings it into scope.
func TestLoggingHook_SourceAudit_NoRawContent(t *testing.T) {
	src, err := os.ReadFile("logging.go")
	if err != nil {
		t.Skipf("logging.go not present yet (pre-Task-4 RED state): %v", err)
		return
	}
	forbidden := []*regexp.Regexp{
		// Any slog call that passes the request, its Messages slice, or
		// a Content field as an argument.
		regexp.MustCompile(`slog\.(?:Any|Group)\([^,]*,\s*req\)`),
		regexp.MustCompile(`slog\.(?:Any|Group)\([^,]*,\s*req\.Messages`),
		regexp.MustCompile(`slog\.(?:Any|Group)\([^,]*,\s*req\.Messages\[`),
		regexp.MustCompile(`\.Content\)\s*$`),
		regexp.MustCompile(`slog\.Any\([^,]*,\s*[^)]*\.Content`),
	}
	for _, re := range forbidden {
		if loc := re.FindIndex(src); loc != nil {
			t.Errorf("T-8-PII violation: logging.go matches forbidden pattern %q at byte %d", re, loc[0])
		}
	}
	// Also enforce T-8-LEAK-3 — no slog.SetDefault.
	if bytes.Contains(src, []byte("slog.SetDefault")) {
		t.Error("T-8-LEAK-3 violation: logging.go must not call slog.SetDefault")
	}
}

// TestLoggingHook_Name asserts the filter-discovery name.
func TestLoggingHook_Name(t *testing.T) {
	got := (&LoggingHook{}).Name()
	if got != "LoggingHook" {
		t.Errorf("Name: got %q, want %q", got, "LoggingHook")
	}
}

// TestLoggingHook_Describe asserts kind reports both Pre+Post and config
// contains a safe-to-publish "level" field (no secrets).
func TestLoggingHook_Describe(t *testing.T) {
	logger, _ := captureSlog(t)
	hook := &LoggingHook{Logger: logger}
	kind, cfg := hook.Describe()
	if kind != "Pre,Post" {
		t.Errorf("kind: got %q, want %q", kind, "Pre,Post")
	}
	if _, ok := cfg["level"]; !ok {
		t.Errorf("config missing 'level': %+v", cfg)
	}
	// Sanity: no secret-shaped key names exposed.
	for k := range cfg {
		lower := strings.ToLower(k)
		if strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "secret") {
			t.Errorf("Describe config exposes suspicious key %q (T-8-PII safety)", k)
		}
	}
}
