package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"loop24-gateway/internal/canonical"
	"loop24-gateway/internal/engine"
	"loop24-gateway/internal/pool"
	"loop24-gateway/internal/testutil"
)

// resolveKiroCLI gates integration tests on (1) LOOP24_INTEGRATION=1 in
// the env AND (2) either LOOP24_KIRO_BIN pointing at a kiro-cli binary
// or kiro-cli being discoverable on PATH. Mirrors the Phase 1
// internal/acp/integration_test.go pattern AND the Phase 2 Ollama
// integration_test.go pattern verbatim (Phase 1.1 D-17).
//
// CI default: LOOP24_INTEGRATION=0 → t.Skip with a clear reason. No
// false failures on developer machines without kiro-cli.
func resolveKiroCLI(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("LOOP24_KIRO_BIN"); bin != "" {
		return bin
	}
	if os.Getenv("LOOP24_INTEGRATION") != "1" {
		t.Skip("set LOOP24_INTEGRATION=1 to run integration tests")
	}
	p, err := exec.LookPath("kiro-cli")
	if err != nil {
		t.Skip("kiro-cli not on PATH (set LOOP24_KIRO_BIN to override)")
	}
	return p
}

// kiroSetup spawns a real kiro-cli pool of size 1, warms it up,
// constructs the engine + adapter, and returns the running httptest
// server plus a teardown closure. Test callers MUST defer the closure
// to free the pool subprocess.
//
// On warmup failure (typically: kiro-cli auth-not-refreshed in dev
// environments), the helper calls t.Skipf — the gateway is functional
// but the developer's local kiro-cli is in a state we cannot fix from
// here. CI runs with refreshed credentials and the skip path will not
// fire.
func kiroSetup(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	bin := resolveKiroCLI(t)
	logger := testutil.Logger(t)

	p := pool.New(pool.Config{
		Logger:       logger,
		Size:         1,
		KiroCmd:      bin,
		KiroArgs:     []string{"acp"},
		PingInterval: 10 * time.Minute, // disable periodic ping during test
	})

	warmCtx, cancelWarm := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelWarm()
	if err := p.Warmup(warmCtx); err != nil {
		_ = p.Close()
		t.Skipf("pool.Warmup failed (likely kiro-cli auth-not-refreshed): %v", err)
	}

	eng := engine.New(engine.Config{
		Logger: logger,
		ACP:    p,
	})

	// engineAdapter bridges *engine.Engine → anthropic.Engine. The
	// concrete *engine.Run / *engine.Stream values are structurally
	// compatible with anthropic.RunHandle / anthropic.Stream — but
	// Go's interface-return-type rule means the wrapper has to convert
	// at the call site. Mirrors cmd/loop24-gateway/main.go's
	// anthropicEngineAdapter (kept local to the test so the test file
	// stays self-contained).
	a := New(Config{
		Logger: logger,
		Engine: realEngineAdapter{engine: eng},
	})

	srv := httptest.NewServer(a.ProtectedRouter())
	return srv, func() {
		srv.Close()
		if err := p.Close(); err != nil {
			t.Logf("pool.Close (expected non-zero exit): %v", err)
		}
	}
}

// realEngineAdapter wraps *engine.Engine to satisfy anthropic.Engine.
// Test-local equivalent of cmd/main.go's anthropicEngineAdapter — kept
// here so the test file stays self-contained and the production main.go
// shim does not become a load-bearing test dependency.
type realEngineAdapter struct{ engine *engine.Engine }

func (r realEngineAdapter) Collect(
	ctx context.Context, req *canonical.ChatRequest,
) (*canonical.ChatResponse, error) {
	resp, err := r.engine.Collect(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("integration collect: %w", err)
	}
	return resp, nil
}

func (r realEngineAdapter) Run(
	ctx context.Context, req *canonical.ChatRequest,
) (RunHandle, error) {
	run, err := r.engine.Run(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("integration run: %w", err)
	}
	return realRunHandle{run: run}, nil
}

type realRunHandle struct{ run *engine.Run }

func (h realRunHandle) Stream() Stream    { return h.run.Stream() }
func (h realRunHandle) SessionID() string { return h.run.SessionID() }

// TestIntegration_RealKiroCLI_NonStreaming — Plan 03.1-04 Task 2.
//
// Exercises the full /v1/messages stream:false path against real
// kiro-cli: pool warmup → engine → adapter → httptest server → POST →
// assert response shape (200 + non-empty content[0].text + non-nil
// stop_reason). Whitebox so it uses anthropicMessage from render.go
// directly.
func TestIntegration_RealKiroCLI_NonStreaming(t *testing.T) {
	srv, teardown := kiroSetup(t)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	body := []byte(`{"model":"auto","max_tokens":128,"stream":false,"messages":[{"role":"user","content":"say the word ping"}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json prefix", ct)
	}

	var msg anthropicMessage
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if msg.Type != "message" {
		t.Errorf("type: got %q, want message", msg.Type)
	}
	if msg.Role != "assistant" {
		t.Errorf("role: got %q, want assistant", msg.Role)
	}
	if msg.StopReason == nil {
		t.Error("stop_reason: got nil, want non-nil")
	}
	if len(msg.Content) == 0 {
		t.Fatal("content: empty (kiro-cli did not return any blocks)")
	}
	if msg.Content[0].Type != "text" {
		t.Errorf("content[0].type: got %q, want text", msg.Content[0].Type)
	}
	if msg.Content[0].Text == "" {
		t.Error("content[0].text: empty")
	}
	t.Logf("integration non-streaming response: %.80s (stop_reason=%v)",
		msg.Content[0].Text, deref(msg.StopReason))
}

// TestIntegration_RealKiroCLI_Streaming — Plan 03.1-04 Task 2 +
// W10 strict SSE byte-framing.
//
// POST /v1/messages with stream:true and read the response body
// line-by-line, enforcing the strict state machine:
//
//	expectingEvent → expectingData → expectingBlank → expectingEvent
//
// where each transition is gated by exact byte prefixes ("event: ",
// "data: ", "") and any deviation fails the test with the offending
// line index.
//
// The strict framing assertion is the load-bearing differentiator
// vs. the existing golden-fixture tests in sse_golden_test.go: it
// runs against real kiro-cli output, so it catches any drift between
// the gateway's emitter and the actual chunk sequence kiro-cli emits.
//
// Also asserts the canonical event sequence: first event is
// message_start, at least one content_block_delta with text_delta is
// observed, and the final event is message_stop. No `error` event is
// permitted on the happy path.
func TestIntegration_RealKiroCLI_Streaming(t *testing.T) {
	srv, teardown := kiroSetup(t)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	body := []byte(`{"model":"auto","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"say the word ping"}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream prefix", ct)
	}

	// knownEvents covers every event name the spec permits on the
	// /v1/messages SSE channel — `ping` (15s keepalive) and `error`
	// are valid but the happy path should never emit `error`.
	knownEvents := map[string]struct{}{
		"message_start":       {},
		"content_block_start": {},
		"content_block_delta": {},
		"content_block_stop":  {},
		"message_delta":       {},
		"message_stop":        {},
		"ping":                {},
		"error":               {},
	}

	type frameState int
	const (
		expectingEvent frameState = iota
		expectingData
		expectingBlank
	)
	state := expectingEvent

	var (
		events       []string
		sawTextDelta bool
		sawError     bool
	)

	scanner := bufio.NewScanner(resp.Body)
	// SSE frames are typically small but content_block_delta payloads
	// can carry larger text chunks; bump the buffer to 1 MiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var currentEvent string
	lineIdx := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineIdx++

		switch state {
		case expectingEvent:
			if !strings.HasPrefix(line, "event: ") {
				t.Fatalf("strict framing violation at line %d: state=expectingEvent, got=%q",
					lineIdx, line)
			}
			name := strings.TrimPrefix(line, "event: ")
			if _, ok := knownEvents[name]; !ok {
				t.Fatalf("strict framing violation at line %d: unknown event name %q",
					lineIdx, name)
			}
			currentEvent = name
			events = append(events, name)
			if name == "error" {
				sawError = true
			}
			state = expectingData

		case expectingData:
			if !strings.HasPrefix(line, "data: ") {
				t.Fatalf("strict framing violation at line %d: state=expectingData (after event %q), got=%q",
					lineIdx, currentEvent, line)
			}
			payload := strings.TrimPrefix(line, "data: ")
			// JSON validity check — any deviation fails framing.
			var anyVal any
			if err := json.Unmarshal([]byte(payload), &anyVal); err != nil {
				t.Fatalf("strict framing violation at line %d: data payload not valid JSON: %v; payload=%s",
					lineIdx, err, payload)
			}
			// W10 sub-check: for content_block_delta carrying
			// text_delta, record sighting so the post-loop
			// assertion can verify text streaming actually happened.
			if currentEvent == "content_block_delta" {
				var asMap map[string]any
				if json.Unmarshal([]byte(payload), &asMap) == nil {
					if delta, ok := asMap["delta"].(map[string]any); ok {
						if dt, _ := delta["type"].(string); dt == "text_delta" {
							sawTextDelta = true
						}
					}
				}
			}
			state = expectingBlank

		case expectingBlank:
			if line != "" {
				t.Fatalf("strict framing violation at line %d: state=expectingBlank (after data of event %q), got=%q",
					lineIdx, currentEvent, line)
			}
			state = expectingEvent
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}

	// At end of stream, state should be expectingEvent (full frame
	// just completed) — any other state means the last frame was
	// truncated mid-frame.
	if state != expectingEvent {
		t.Errorf("stream ended mid-frame: final state=%d (want expectingEvent=%d)", state, expectingEvent)
	}

	// Canonical sequence assertions.
	if len(events) == 0 {
		t.Fatal("no events received")
	}
	if events[0] != "message_start" {
		t.Errorf("first event: got %q, want message_start", events[0])
	}
	if events[len(events)-1] != "message_stop" {
		t.Errorf("last event: got %q, want message_stop", events[len(events)-1])
	}
	if !sawTextDelta {
		t.Error("no content_block_delta with text_delta observed (kiro-cli emitted no text)")
	}
	if sawError {
		t.Error("error event observed on happy path — kiro-cli or adapter regression")
	}
	t.Logf("integration streaming: %d frames, sequence=%v", len(events), events)
}

// TestIntegration_RealKiroCLI_RedactedThinkingFollowUp — Plan 03.1-04
// Task 2 (W5 part 2 — D-13 verification).
//
// Two POSTs against real kiro-cli:
//
//  1. First turn: standard user message → assert 200 + capture text.
//  2. Second turn: messages array contains user1 + assistant
//     (synthetic redacted_thinking content block + text from turn 1) +
//     user2 → assert 200 + non-empty content[0].text.
//
// Load-bearing assertion: the SECOND turn does NOT error. wire.go
// drops the redacted_thinking block at decode time (D-13), so by the
// time kiro-cli sees the canonical transcript the redacted_thinking
// block is gone — leaving a kiro-cli-safe message history.
//
// If this test ever fails with a 500, the D-13 drop-at-wire-decode
// policy is wrong and needs revisiting (per Plan 04 plan_text).
func TestIntegration_RealKiroCLI_RedactedThinkingFollowUp(t *testing.T) {
	srv, teardown := kiroSetup(t)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// First POST.
	firstBody := []byte(`{"model":"auto","max_tokens":128,"stream":false,"messages":[{"role":"user","content":"say hi"}]}`)
	firstResp := postJSON(ctx, t, srv, firstBody)
	defer func() { _ = firstResp.Body.Close() }()
	if firstResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(firstResp.Body)
		t.Fatalf("first POST status: got %d, want 200; body=%s", firstResp.StatusCode, raw)
	}
	var first anthropicMessage
	if err := json.NewDecoder(firstResp.Body).Decode(&first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if len(first.Content) == 0 || first.Content[0].Text == "" {
		t.Fatal("first response has empty text — cannot proceed to follow-up turn")
	}
	firstResponseText := first.Content[0].Text

	// SECOND POST — reconstruct the assistant turn with a synthetic
	// redacted_thinking content block prepended. The wire decoder
	// (wire.go D-13) drops it; kiro-cli sees a clean transcript.
	//
	// json.Marshal the assistant text so embedded quotes / newlines
	// in firstResponseText do not break the JSON literal.
	assistantTextJSON, err := json.Marshal(firstResponseText)
	if err != nil {
		t.Fatalf("marshal first response text: %v", err)
	}
	secondBody := []byte(fmt.Sprintf(
		`{"model":"auto","max_tokens":128,"stream":false,"messages":[`+
			`{"role":"user","content":"say hi"},`+
			`{"role":"assistant","content":[`+
			`{"type":"redacted_thinking","data":"REDACTED_TEST_BLOB_OPAQUE"},`+
			`{"type":"text","text":%s}`+
			`]},`+
			`{"role":"user","content":"say bye"}`+
			`]}`, string(assistantTextJSON)))

	secondResp := postJSON(ctx, t, srv, secondBody)
	defer func() { _ = secondResp.Body.Close() }()
	if secondResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(secondResp.Body)
		t.Fatalf("second POST status: got %d, want 200 (D-13 drop-at-wire-decode broken?); body=%s",
			secondResp.StatusCode, raw)
	}

	var second anthropicMessage
	if err := json.NewDecoder(secondResp.Body).Decode(&second); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if len(second.Content) == 0 {
		t.Fatal("second response: empty content (kiro-cli did not respond to follow-up turn)")
	}
	if second.Content[0].Type != "text" {
		t.Errorf("second response content[0].type: got %q, want text", second.Content[0].Type)
	}
	if second.Content[0].Text == "" {
		t.Error("second response content[0].text: empty")
	}
	t.Logf("integration follow-up: first=%.40s, second=%.40s",
		firstResponseText, second.Content[0].Text)
}

// postJSON is the integration tests' standard POST helper.
func postJSON(ctx context.Context, t *testing.T, srv *httptest.Server, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// deref returns the value of a *string for log formatting, or
// "<nil>" when the pointer is nil. Avoids the noisy `%v` output
// "0xc000...".
func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
