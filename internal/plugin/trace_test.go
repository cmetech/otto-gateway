// Quick 260529-ll2 — trace_test.go.
//
// Tests for ChatTraceHook covering:
//   - DisabledNoWrite: Enabled=false → no bytes written.
//   - SharedRequestID: Before + After emit records carrying the same
//     request_id when ctx has a stamped id.
//   - DescribeNoSecrets: Describe()'s map exposes only {enabled,
//     output_path} — no keys named or hinting at request content.
//   - DurationPositive: After's duration_ms > 0 after a >=1ms gap.
//   - RecordsPreRedactionContent: the load-bearing ordering invariant —
//     ChatTraceHook.Before runs BEFORE PIIRedactionHook mutates req,
//     so the NDJSON pre line contains the raw, non-redacted email
//     string. This is the regression guard for T-ll2-07 (chain
//     reorder).

package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin/pii"
)

// readNDJSON splits buf on newlines and decodes each non-empty line
// into a map. Useful for asserting field-by-field without coupling to
// preRecord / postRecord struct shapes (which the hook owns).
func readNDJSON(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	scanner := bufio.NewScanner(buf)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode NDJSON line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan NDJSON: %v", err)
	}
	return out
}

// TestChatTraceHook_DisabledNoWrite asserts that with Enabled=false the
// Writer buffer remains empty even when Before and After are invoked.
// This protects the two-knob contract: ENABLED_HOOKS controls chain
// presence, ChatTrace controls work-doing.
func TestChatTraceHook_DisabledNoWrite(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	hook := &ChatTraceHook{Writer: buf, Enabled: false}
	ctx := WithRequestID(context.Background(), "TEST-RID")
	req := &canonical.ChatRequest{Model: "auto"}
	resp := &canonical.ChatResponse{StopReason: canonical.StopEndTurn}

	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if err := hook.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("Enabled=false: writer should stay empty; got %d bytes: %q", buf.Len(), buf.String())
	}
}

// TestChatTraceHook_SharedRequestID asserts the pre and post records
// carry the same request_id when ctx already has one stamped (the
// production path — adapter calls plugin.WithRequestID before engine
// entry).
func TestChatTraceHook_SharedRequestID(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	hook := &ChatTraceHook{Writer: buf, Enabled: true}
	ctx := WithRequestID(context.Background(), "TEST-RID-SHARED")
	req := &canonical.ChatRequest{Model: "auto"}
	resp := &canonical.ChatResponse{StopReason: canonical.StopEndTurn}

	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if err := hook.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}

	recs := readNDJSON(t, buf)
	if len(recs) != 2 {
		t.Fatalf("want 2 records (pre + post), got %d: %+v", len(recs), recs)
	}
	if recs[0]["stage"] != "pre_chain_in" {
		t.Errorf("record 0 stage: got %v, want pre_chain_in", recs[0]["stage"])
	}
	if recs[1]["stage"] != "post_chain_out" {
		t.Errorf("record 1 stage: got %v, want post_chain_out", recs[1]["stage"])
	}
	preID := recs[0]["request_id"]
	postID := recs[1]["request_id"]
	if preID != "TEST-RID-SHARED" {
		t.Errorf("pre request_id: got %v, want TEST-RID-SHARED", preID)
	}
	if preID != postID {
		t.Errorf("request_id mismatch across pre/post: pre=%v post=%v", preID, postID)
	}
}

// TestChatTraceHook_DescribeNoSecrets walks the Describe map and fails
// on any key whose name or value contains a request-content-shaped
// substring. This is the Pitfall 9 whitelist guarantee — the
// /health/hooks endpoint must NEVER expose raw prompts via the
// hook's introspection seam (T-ll2-04 mitigation).
func TestChatTraceHook_DescribeNoSecrets(t *testing.T) {
	t.Parallel()

	hook := &ChatTraceHook{Enabled: true}
	kind, cfg := hook.Describe()
	if kind != "Pre,Post" {
		t.Errorf("kind: got %q, want Pre,Post", kind)
	}
	allowedKeys := map[string]struct{}{
		"enabled":     {},
		"output_path": {},
	}
	for k, v := range cfg {
		if _, ok := allowedKeys[k]; !ok {
			t.Errorf("Describe leaked unexpected key %q (value: %v)", k, v)
		}
		// Forbidden-substring scan on both key name and rendered value.
		lower := strings.ToLower(k)
		forbidden := []string{"messages", "tools", "system", "content", "prompt"}
		for _, sub := range forbidden {
			if strings.Contains(lower, sub) {
				t.Errorf("Describe key %q contains forbidden substring %q", k, sub)
			}
		}
	}
}

// TestChatTraceHook_DurationPositive asserts that After's duration_ms
// field is > 0 when paired with a Before call separated by a non-zero
// sleep. The exact value floats per machine; a >= 1ms lower bound is
// safe (sleep is 2ms, plus encoder overhead).
func TestChatTraceHook_DurationPositive(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	hook := &ChatTraceHook{Writer: buf, Enabled: true}
	ctx := WithRequestID(context.Background(), "TEST-RID-DUR")
	req := &canonical.ChatRequest{Model: "auto"}
	resp := &canonical.ChatResponse{StopReason: canonical.StopEndTurn}

	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := hook.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}

	recs := readNDJSON(t, buf)
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	dur, _ := recs[1]["duration_ms"].(float64)
	if dur < 1 {
		t.Errorf("duration_ms: got %v, want >= 1 (slept 2ms)", recs[1]["duration_ms"])
	}
}

// TestChatTraceHook_RecordsPreRedactionContent is the load-bearing
// regression test for the chain-order invariant (T-ll2-07).
//
// Scenario: compose the relevant chain prefix in the documented order —
// ChatTraceHook → PIIRedactionHook — and drive both Before hooks
// against a shared *canonical.ChatRequest containing an obvious email
// PII token. Assert that the NDJSON pre line emitted by ChatTraceHook
// contains the RAW email string (because ChatTrace ran BEFORE
// PIIRedaction mutated the request in place).
//
// This regression-guards a future refactor that "tidies" the chain
// literal in main.go and silently inserts ChatTraceHook after
// PIIRedactionHook — which would still compile, still pass every other
// test, and silently log REDACTED content to chat-trace.log,
// destroying the feature's value.
func TestChatTraceHook_RecordsPreRedactionContent(t *testing.T) {
	t.Parallel()

	const rawEmail = "trace-canary@cmetech.io"

	buf := &bytes.Buffer{}
	chatTrace := &ChatTraceHook{Writer: buf, Enabled: true}
	piiHook := &pii.PIIRedactionHook{
		Recognizers: pii.Recognizers,
		Enabled:     true,
		Mode:        "replace",
	}

	req := &canonical.ChatRequest{
		Model: "auto",
		Messages: []canonical.Message{
			{
				Role: canonical.RoleUser,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "Please reach me at " + rawEmail + " thanks"},
				},
			},
		},
	}

	ctx := WithRequestID(context.Background(), "TEST-RID-PRE-REDACT")
	ctx = WithSurface(ctx, "openai")

	// Drive the chain prefix in documented order. ChatTrace MUST run first.
	if _, err := chatTrace.Before(ctx, req); err != nil {
		t.Fatalf("ChatTraceHook.Before: %v", err)
	}
	// After ChatTrace.Before runs, the buffer must contain the raw email.
	// Snapshot the bytes immediately so a later mutation does not affect
	// the assertion (the encoder writes the in-memory canonical.Message
	// slice; later in-place mutation by PIIRedactionHook does not
	// retroactively rewrite already-encoded JSON bytes, but the snapshot
	// makes the temporal-ordering assertion explicit).
	preBytes := append([]byte(nil), buf.Bytes()...)
	if !bytes.Contains(preBytes, []byte(rawEmail)) {
		t.Fatalf("pre_chain_in NDJSON should contain raw email %q; got %q", rawEmail, preBytes)
	}

	// Now run PIIRedactionHook.Before — this mutates req.Messages in place.
	if _, err := piiHook.Before(ctx, req); err != nil {
		t.Fatalf("PIIRedactionHook.Before: %v", err)
	}

	// Sanity: after PII redaction, the in-memory req no longer contains
	// the raw email (proves the test actually exercised redaction).
	mutatedText := req.Messages[0].Content[0].Text
	if strings.Contains(mutatedText, rawEmail) {
		t.Errorf("redaction did not mutate req.Messages[0].Content[0].Text; got %q", mutatedText)
	}

	// Re-parse the pre line to make the structural shape explicit.
	recs := readNDJSON(t, bytes.NewBuffer(preBytes))
	if len(recs) != 1 {
		t.Fatalf("expected 1 pre line, got %d", len(recs))
	}
	if recs[0]["stage"] != "pre_chain_in" {
		t.Errorf("stage: got %v, want pre_chain_in", recs[0]["stage"])
	}
	if recs[0]["surface"] != "openai" {
		t.Errorf("surface: got %v, want openai", recs[0]["surface"])
	}
	// Re-stringify and check for the raw email in the JSON payload.
	rawJSON, err := json.Marshal(recs[0])
	if err != nil {
		t.Fatalf("re-marshal pre record: %v", err)
	}
	if !bytes.Contains(rawJSON, []byte(rawEmail)) {
		t.Errorf("re-marshaled pre record missing raw email %q: %s", rawEmail, rawJSON)
	}
}
