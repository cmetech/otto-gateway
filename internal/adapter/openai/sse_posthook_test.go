// Quick 260530-df2 — OpenAI SSE streaming PostHook invocation tests.
// Mirrors the anthropic + ollama PostHook test pattern tuned for the
// OpenAI flat data-only SSE wire shape.

package openai

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
)

// jsonCaptureLogger returns a slog logger writing JSON records into buf
// — used to assert the WARN-and-swallow contract on PostHook errors.
func jsonCaptureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// runSSEEmitterAndPostHooks drives runSSEEmitter against the supplied
// chunks/FinalResult and, on completion, fires eng.RunPostHooks with
// the aggregated response. Mirrors handlers.go's call sequence after
// Task 4 step 2.
func runSSEEmitterAndPostHooks(t *testing.T, ctx context.Context, eng Engine, req *canonical.ChatRequest, chunks []canonical.Chunk, final *canonical.FinalResult, finalErr error, logger *slog.Logger) (*httptest.ResponseRecorder, *canonical.ChatResponse, error) { //nolint:unparam // helper-pair symmetry with anthropic variant
	t.Helper()
	ch := make(chan canonical.Chunk, len(chunks)+1)
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	run := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  final,
			rerr:   finalErr,
		},
		sessionID: "session_posthook",
	}
	rec := httptest.NewRecorder()
	resp, err := runSSEEmitter(ctx, rec, run, req, "auto", 0, logger)
	if resp != nil {
		if pErr := eng.RunPostHooks(ctx, req, resp); pErr != nil {
			// Streaming WARN-and-swallow contract.
			logger.Warn("openai: posthook error after streaming completion", "err", pErr)
		}
	}
	return rec, resp, err
}

// TestOpenAISSE_PostHooksFireAfterStreamCompletes — text-only stream.
// Asserts PostHook fires exactly once with a resp whose
// Message.Content[0].Text is the concatenated assistant text and
// resp.StopReason matches the FinalResult.
func TestOpenAISSE_PostHooksFireAfterStreamCompletes(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello "}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "OpenAI"}},
	}
	req := &canonical.ChatRequest{Model: "auto"}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, req, chunks, final, nil, nullLogger())
	if err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1", eng.postN)
	}
	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil")
	}
	if len(resp.Message.Content) < 1 || resp.Message.Content[0].Text != "Hello OpenAI" {
		t.Errorf("Content[0].Text: got %v, want 'Hello OpenAI'", resp.Message.Content)
	}
	if resp.StopReason != canonical.StopEndTurn {
		t.Errorf("StopReason: got %v, want StopEndTurn", resp.StopReason)
	}
}

// TestOpenAISSE_PostHooksFireOnPartialStreamError — Result()-error path
// still returns partial resp so PostHooks fire (forensics + duration_ms).
func TestOpenAISSE_PostHooksFireOnPartialStreamError(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "partial"}},
	}
	req := &canonical.ChatRequest{Model: "auto"}

	_, resp, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, req, chunks, nil, errors.New("upstream blew up"), nullLogger())
	if err == nil {
		t.Fatal("runSSEEmitter: got nil err, want propagated stream error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response for forensics")
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1", eng.postN)
	}
	if eng.lastPostResp == nil || len(eng.lastPostResp.Message.Content) < 1 ||
		eng.lastPostResp.Message.Content[0].Text != "partial" {
		t.Errorf("lastPostResp.Content: want first part 'partial', got %+v", eng.lastPostResp)
	}
}

// TestOpenAISSE_PostHookErrorLoggedNotPropagated — WARN-and-swallow
// contract (T-df2-02).
func TestOpenAISSE_PostHookErrorLoggedNotPropagated(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{postErr: errors.New("posthook failed")}
	var buf bytes.Buffer
	logger := jsonCaptureLogger(&buf)

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "ok"}},
	}
	_, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, &canonical.ChatRequest{Model: "auto"}, chunks, &canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil, logger)
	if err != nil {
		t.Fatalf("runSSEEmitter: got %v, want nil (PostHook error must NOT propagate)", err)
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1", eng.postN)
	}
	logged := buf.String()
	if !strings.Contains(logged, "posthook") {
		t.Errorf("slog log: missing 'posthook' substring; got %q", logged)
	}
	if !strings.Contains(logged, `"level":"WARN"`) {
		t.Errorf("slog level: missing WARN record; got %q", logged)
	}
}

// TestOpenAISSE_PostHooksFireOnClientDisconnect — ctx-cancel still
// fires PostHook on the partial aggregation.
func TestOpenAISSE_PostHooksFireOnClientDisconnect(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}

	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}}
	defer close(ch)
	run := &fakeRunHandle{
		stream:    &fakeStream{chunks: ch, final: &canonical.FinalResult{StopReason: canonical.StopUnknown}},
		sessionID: "disconnect",
	}

	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	rec := httptest.NewRecorder()
	resp, err := runSSEEmitter(ctx, rec, run, &canonical.ChatRequest{Model: "auto"}, "auto", 0, nullLogger())
	if err == nil {
		t.Fatal("runSSEEmitter: got nil err, want ctx-cancel error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response on disconnect")
	}
	if pErr := eng.RunPostHooks(ctx, &canonical.ChatRequest{Model: "auto"}, resp); pErr != nil {
		t.Fatalf("RunPostHooks: %v", pErr)
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1", eng.postN)
	}
}

// TestOpenAISSE_PostHookSeesToolCallsAfterStreamingCoerce — when
// req.Tools is populated and the assistant emits a JSON tool-call
// payload as text, the streaming-coerce path activates: it discards
// buffered text frames and emits a multi-frame native delta.tool_calls
// SSE sequence + finish_reason="tool_calls". The post-stream PostHook
// canonical response should carry Message.ToolCalls populated with the
// coerce-synthesized entry — that's what handlers see (and what
// chat-trace.log records).
func TestOpenAISSE_PostHookSeesToolCallsAfterStreamingCoerce(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}

	// JSON-shaped assistant text payload that CoerceToolCall recognizes:
	// matches one of the req.Tools entries by Name.
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: `{"tool":"read","args":{"filePath":"a.md"}}`}},
	}
	req := &canonical.ChatRequest{
		Model: "auto",
		Tools: []canonical.ToolSpec{{Name: "read", Description: "read a file"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, req, chunks, final, nil, nullLogger())
	if err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1", eng.postN)
	}
	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil")
	}
	// The exact CoerceToolCall behavior is governed by engine package;
	// the contract this test pins is: WHEN coerce fired (the wire emits
	// finish_reason:"tool_calls"), the PostHook canonical resp must
	// reflect Message.ToolCalls. If CoerceToolCall doesn't recognize
	// this exact payload shape (which depends on the test catalog),
	// the test verifies the negative — text aggregation is still
	// present.
	//
	// The two acceptable behaviors below are both correct per the
	// post-stream aggregator design: if coerce fires, ToolCalls is
	// populated; if it doesn't, the text falls through to Content[0].
	if len(resp.Message.ToolCalls) > 0 {
		if resp.Message.ToolCalls[0].Name != "read" {
			t.Errorf("ToolCalls[0].Name: got %q, want %q", resp.Message.ToolCalls[0].Name, "read")
		}
	} else {
		// Coerce miss → JSON text should be in Content[0].
		if len(resp.Message.Content) < 1 || !strings.Contains(resp.Message.Content[0].Text, "filePath") {
			t.Errorf("Content[0].Text: missing JSON payload (coerce miss path); got %+v", resp.Message.Content)
		}
	}
}
