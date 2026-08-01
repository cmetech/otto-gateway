// Quick 260529-ll2 — trace_test.go.
//
// Tests for ChatTraceHook covering:
//   - DisabledNoWrite: Enabled=false → no bytes written.
//   - SharedRequestID: Before + After emit records carrying the same
//     request_id when ctx has a stamped id.
//   - DescribeNoSecrets: Describe()'s map exposes only {enabled,
//     output_path} — no keys named or hinting at request content.
//   - DurationPositive: After's duration_ms > 0 after a >=1ms gap.
//   - RecordsPreRedactionContent: the standard-profile ordering invariant —
//     ChatTraceHook.Before runs BEFORE PIIRedactionHook mutates req,
//     so the standard NDJSON pre line contains the raw, non-redacted email
//     string. This is the regression guard for T-ll2-07 (chain
//     reorder).

package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin/pii"
)

type chatTracePrivacyStub struct {
	allow   bool
	summary map[string]any
}

func (s chatTracePrivacyStub) AllowSensitiveTrace(context.Context) bool { return s.allow }

func (s chatTracePrivacyStub) TraceSummary(context.Context) map[string]any { return s.summary }

var standardTracePrivacy = chatTracePrivacyStub{allow: true}

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
	hook := &ChatTraceHook{Writer: buf, Enabled: true, Privacy: standardTracePrivacy}
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
	hook := &ChatTraceHook{Writer: buf, Enabled: true, Privacy: standardTracePrivacy}
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
	chatTrace := &ChatTraceHook{Writer: buf, Enabled: true, Privacy: standardTracePrivacy}
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

func TestChatTracePrivacy_StandardPreservesRawShape(t *testing.T) {
	buf := &bytes.Buffer{}
	hook := &ChatTraceHook{Writer: buf, Enabled: true, Privacy: standardTracePrivacy}
	ctx := WithSurface(WithRequestID(context.Background(), "standard-rid"), "openai")
	req := &canonical.ChatRequest{
		Model: "standard-model-canary",
		Messages: []canonical.Message{{
			Role:    canonical.RoleUser,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "standard-request-canary"}},
		}},
		System: "standard-system-canary",
		Tools:  []canonical.ToolSpec{{Name: "standard-tool-canary"}},
	}
	resp := &canonical.ChatResponse{
		Message:    canonical.Message{Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "standard-response-canary"}}},
		StopReason: canonical.StopEndTurn,
	}
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := hook.After(ctx, req, resp); err != nil {
		t.Fatal(err)
	}

	raw := buf.String()
	records := readNDJSON(t, bytes.NewBufferString(raw))
	if len(records) != 2 {
		t.Fatalf("records=%d, want 2", len(records))
	}
	wantPreKeys := []string{"message_count", "messages", "model", "request_id", "stage", "surface", "system", "tools", "ts"}
	wantPostKeys := []string{"content", "duration_ms", "request_id", "stage", "stop_reason", "surface", "ts"}
	for index, want := range [][]string{wantPreKeys, wantPostKeys} {
		got := make([]string, 0, len(records[index]))
		for key := range records[index] {
			got = append(got, key)
		}
		slices.Sort(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("record %d keys=%v, want existing raw shape %v", index, got, want)
		}
	}
	for _, canary := range []string{
		"standard-model-canary", "standard-request-canary", "standard-system-canary",
		"standard-tool-canary", "standard-response-canary",
	} {
		if !strings.Contains(raw, canary) {
			t.Errorf("standard trace lost raw value %q: %s", canary, raw)
		}
	}
}

func TestChatTracePrivacy_UnsafePosturesEmitSummaryOnly(t *testing.T) {
	tests := []struct {
		name    string
		privacy ChatTracePrivacy
		result  string
	}{
		{name: "strict unresolved", privacy: chatTracePrivacyStub{summary: map[string]any{
			"surface": "anthropic", "workload": "network-hardening", "profile": "strict",
			"coverage": "unresolved", "result": "unresolved",
			"transformed": 0, "restored": 0, "blocked": 0,
			"entity": "Email", "value": "summary-value-canary", "request": map[string]any{"body": "summary-body-canary"},
		}}, result: "unresolved"},
		{name: "strict blocked", privacy: chatTracePrivacyStub{summary: map[string]any{
			"surface": "openai", "workload": "audit", "profile": "strict",
			"coverage": "input", "result": "block", "transformed": 2, "restored": 0, "blocked": 1,
		}}, result: "block"},
		{name: "strict errored", privacy: chatTracePrivacyStub{summary: map[string]any{
			"surface": "ollama", "workload": "triage", "profile": "strict",
			"coverage": "full", "result": "error", "transformed": 2, "restored": 1, "blocked": 0,
		}}, result: "error"},
		{name: "strict pass", privacy: chatTracePrivacyStub{summary: map[string]any{
			"surface": "openai", "workload": "safe-pass", "profile": "strict",
			"coverage": "full", "result": "pass", "transformed": 2, "restored": 2, "blocked": 0,
		}}, result: "pass"},
		{name: "missing policy", privacy: nil, result: "unresolved"},
	}
	allowed := map[string]struct{}{
		"ts": {}, "stage": {}, "request_id": {}, "surface": {}, "workload": {},
		"profile": {}, "coverage": {}, "result": {}, "transformed": {}, "restored": {}, "blocked": {},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			hook := &ChatTraceHook{Writer: buf, Enabled: true, Privacy: tc.privacy}
			ctx := WithSurface(WithRequestID(context.Background(), "safe-rid"), "context-surface")
			req := &canonical.ChatRequest{
				Model: "unsafe-model-canary",
				Messages: []canonical.Message{{
					Role:    canonical.RoleUser,
					Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "unsafe-request-canary"}},
				}},
				System: "unsafe-system-canary",
				Tools:  []canonical.ToolSpec{{Name: "unsafe-tool-canary"}},
			}
			resp := &canonical.ChatResponse{
				Message:    canonical.Message{Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "unsafe-response-canary"}}},
				StopReason: canonical.StopEndTurn,
			}
			wantReq := *req
			wantReq.Messages = append([]canonical.Message(nil), req.Messages...)
			wantReq.Messages[0].Content = append([]canonical.ContentPart(nil), req.Messages[0].Content...)
			wantResp := *resp
			wantResp.Message.Content = append([]canonical.ContentPart(nil), resp.Message.Content...)

			if _, err := hook.Before(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := hook.After(ctx, req, resp); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(wantReq, *req); diff != "" {
				t.Fatalf("Before mutated request (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(wantResp, *resp); diff != "" {
				t.Fatalf("After mutated response (-want +got):\n%s", diff)
			}

			records := readNDJSON(t, buf)
			if len(records) != 2 {
				t.Fatalf("records=%d, want 2", len(records))
			}
			for index, record := range records {
				for key := range record {
					if _, ok := allowed[key]; !ok {
						t.Errorf("record %d leaked non-summary key %q: %+v", index, key, record)
					}
				}
				if record["result"] != tc.result {
					t.Errorf("record %d result=%v, want %q", index, record["result"], tc.result)
				}
			}
			raw := buf.String()
			for _, canary := range []string{
				"unsafe-model-canary", "unsafe-request-canary", "unsafe-system-canary", "unsafe-tool-canary",
				"unsafe-response-canary", "summary-value-canary", "summary-body-canary", "Email",
			} {
				if strings.Contains(raw, canary) {
					t.Errorf("unsafe trace leaked %q: %s", canary, raw)
				}
			}
		})
	}
}
