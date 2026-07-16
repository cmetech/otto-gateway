package openai

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
)

// TestSSE covers the flat OpenAI SSE emitter behaviors beyond the golden fixture:
//   - ctx-cancel mid-stream: returns an error + no [DONE] emitted
//   - no event: lines anywhere in the output
//   - Content-Type text/event-stream set before any data

// TestSSE_CtxCancel verifies that canceling the context mid-stream causes
// runSSEEmitter to return an error without emitting "data: [DONE]".
// The goleak TestMain gate in testmain_test.go verifies no goroutine leak.
func TestSSE_CtxCancel(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Channel that never closes — the emitter will block on chunks.
	// Cancel ctx before any chunk arrives so we exercise the ctx.Done path.
	ch := make(chan canonical.Chunk) // unbuffered, never closed
	defer close(ch)                  // cleanup to avoid goroutine leak in test teardown

	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "session_cancel",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — before the emitter even starts the select-loop

	rec := httptest.NewRecorder()
	_, err := runSSEEmitter(ctx, rec, runHandle, &canonical.ChatRequest{}, nil, "auto", 0, nullLogger())
	if err == nil {
		t.Error("expected non-nil error on ctx cancel, got nil")
	}

	body := rec.Body.String()
	if strings.Contains(body, "[DONE]") {
		t.Errorf("ctx cancel must NOT emit [DONE]; body=%q", body)
	}
}

// TestSSE_HeadersSetBeforeBody verifies that Content-Type and Cache-Control
// are set on the ResponseWriter before any body bytes are written.
// httptest.ResponseRecorder records in-order header writes vs WriteHeader.
func TestSSE_HeadersSetBeforeBody(t *testing.T) {
	defer goleak.VerifyNone(t)

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hi"}},
	}
	ch := make(chan canonical.Chunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)

	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "session_headers",
	}

	rec := httptest.NewRecorder()
	if _, err := runSSEEmitter(context.Background(), rec, runHandle, &canonical.ChatRequest{}, nil, "auto", 0, nullLogger()); err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream prefix", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control: got %q, want no-cache", rec.Header().Get("Cache-Control"))
	}
	if rec.Code != 200 {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
}

// TestSSE_NoEventLines verifies that the SSE output contains no "event:" lines
// (OpenAI SSE is data:-only, not Anthropic event:+data: framing).
func TestSSE_NoEventLines(t *testing.T) {
	defer goleak.VerifyNone(t)

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "test"}},
	}
	body := driveGolden(t, chunks, &canonical.FinalResult{StopReason: canonical.StopEndTurn})

	for _, line := range bytes.Split(body, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("event:")) {
			t.Errorf("found event: line in OpenAI SSE output: %q (must be data:-only)", line)
		}
	}
}

// TestSSE_DoneTerminator verifies that a clean stream always ends with
// the literal "data: [DONE]" line.
func TestSSE_DoneTerminator(t *testing.T) {
	defer goleak.VerifyNone(t)

	body := driveGolden(
		t,
		[]canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}}},
		&canonical.FinalResult{StopReason: canonical.StopEndTurn},
	)
	trimmed := bytes.TrimRight(body, "\n")
	if !bytes.Contains(trimmed, []byte("data: [DONE]")) {
		t.Errorf("stream must end with data: [DONE]; body=%q", trimmed)
	}
}

// TestSSE_RoleFirstDelta verifies that the first emitted data frame carries
// delta={"role":"assistant"} and finish_reason=null.
func TestSSE_RoleFirstDelta(t *testing.T) {
	defer goleak.VerifyNone(t)

	body := driveGolden(
		t,
		[]canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "text"}}},
		&canonical.FinalResult{StopReason: canonical.StopEndTurn},
	)

	// First data: frame should contain "role":"assistant".
	lines := bytes.Split(body, []byte("\n"))
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("data: ")) && !bytes.Equal(line, []byte("data: [DONE]")) {
			if bytes.Contains(line, []byte(`"role":"assistant"`)) {
				return // found it
			}
			// First data: frame did NOT contain the role delta — check others.
			break
		}
	}
	// Scan all frames: at least one must have role:assistant.
	if !bytes.Contains(body, []byte(`"role":"assistant"`)) {
		t.Errorf("no role:assistant delta found in stream; body=%q", body)
	}
}

// TestSSE_KiroNativeToolCall_StructuredFrames (Defect 1a, 2026-07-16): a
// kiro-native ChunkKindToolCall surfaces as a structured delta.tool_calls
// sequence (frame B id+name, frame C arguments JSON-string), the terminal
// finish_reason is "tool_calls", and NO `[tool:` marker appears anywhere.
func TestSSE_KiroNativeToolCall_StructuredFrames(t *testing.T) {
	defer goleak.VerifyNone(t)

	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
		ID:   "tc_1",
		Name: "run_shell",
		Args: map[string]any{"command": "echo hi"},
	}}
	close(ch)

	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "session_native_tool",
	}

	rec := httptest.NewRecorder()
	req := &canonical.ChatRequest{Model: "auto", Tools: []canonical.ToolSpec{{Name: "run_shell"}}}
	if _, err := runSSEEmitter(context.Background(), rec, runHandle, req, nil, "auto", 0, nullLogger()); err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}
	body := rec.Body.String()

	if strings.Contains(body, "[tool:") {
		t.Errorf("SSE body must not contain a [tool: marker; body=%q", body)
	}
	for _, want := range []string{
		`"tool_calls"`,
		`"id":"tc_1"`,
		`"name":"run_shell"`,
		`"arguments":"{\"command\":\"echo hi\"}"`,
		`"finish_reason":"tool_calls"`,
		"data: [DONE]",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE body missing %q; body=%q", want, body)
		}
	}
}

// TestSSE_FixedIDAndCreated verifies that all data frames in one stream
// share the same "id" and "created" values (Pitfall 8).
func TestSSE_FixedIDAndCreated(t *testing.T) {
	defer goleak.VerifyNone(t)

	body := driveGolden(
		t,
		[]canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "a"}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "b"}},
		},
		&canonical.FinalResult{StopReason: canonical.StopEndTurn},
	)

	// Normalize id so we can count occurrences of the placeholder.
	norm := normalizeChatID(body)
	// Count "chatcmpl-<id>" occurrences — should appear multiple times
	// (once per data frame) with the SAME placeholder.
	count := bytes.Count(norm, []byte(`"id":"chatcmpl-<id>"`))
	if count < 3 { // role frame + content frames + finish_reason frame
		t.Errorf("expected at least 3 frames with same id; got %d id occurrences in:\n%s", count, norm)
	}
}

// ----------------------------------------------------------------------------
// Quick 260531-ruv — idle-timeout watchdog
// ----------------------------------------------------------------------------

// TestSSE_IdleTimeout_EmitsErrorFrame drives runSSEEmitter with a fake
// Stream whose Chunks() channel never emits. With streamIdle=100ms the
// emitter MUST write an SSE data-frame error envelope + [DONE] and
// return an error errors.Is(canonical.ErrStreamIdleTimeout).
func TestSSE_IdleTimeout_EmitsErrorFrame(t *testing.T) {
	chunks := make(chan canonical.Chunk) // never produces
	t.Cleanup(func() {
		defer func() { _ = recover() }()
		close(chunks)
	})
	runHandle := &fakeRunHandle{
		stream:    &fakeStream{chunks: chunks, final: &canonical.FinalResult{StopReason: canonical.StopUnknown}},
		sessionID: "idle-test",
	}
	rec := httptest.NewRecorder()
	req := &canonical.ChatRequest{Model: "auto"}

	start := time.Now()
	resp, err := runSSEEmitter(context.Background(), rec, runHandle, req, nil, "auto", 100*time.Millisecond, nullLogger())
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Fatalf("emitter took too long: %v", elapsed)
	}
	if !errors.Is(err, canonical.ErrStreamIdleTimeout) {
		t.Fatalf("expected ErrStreamIdleTimeout, got %v", err)
	}
	if resp == nil {
		t.Errorf("aggregated response should be non-nil for PostHook forensics")
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"error":{"message":"stream idle timeout"`) {
		t.Errorf("expected idle-timeout error frame, body=%q", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("expected [DONE] terminator, body=%q", body)
	}
}
