// Quick 260530-df2 — NDJSON streaming PostHook invocation tests for
// /api/chat AND /api/generate. Mirrors the anthropic SSE PostHook
// tests but tunes for ollama's NDJSON wire shape + the
// streaming-coerce buffering decision.

package ollama

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// nilLoggerJSON returns a slog logger writing JSON records into buf.
// Tests use this when asserting the WARN-and-swallow contract on
// PostHook errors.
func nilLoggerJSON(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// runNDJSONEmitterAndPostHooks drives the NDJSON emitter against the
// supplied chunks/FinalResult and, on completion, fires
// eng.RunPostHooks with the aggregated response. Mirrors handlers.go's
// call sequence (handleChat / handleGenerate after Task 3 step 2).
func runNDJSONEmitterAndPostHooks(t *testing.T, ctx context.Context, eng Engine, req *canonical.ChatRequest, chunks []canonical.Chunk, isChat bool, final *canonical.FinalResult, finalErr error, logger *slog.Logger) (*httptest.ResponseRecorder, *canonical.ChatResponse, error) {
	t.Helper()
	ch := make(chan canonical.Chunk, len(chunks)+1)
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	run := &fakeRunHandle{
		stream:    &fakeStream{ch: ch, result: final, err: finalErr},
		sessionID: "session_posthook",
	}
	rec := httptest.NewRecorder()
	resp, err := runNDJSONEmitter(ctx, noopCancelFn, rec, run, "auto", isChat, time.Now(), logger, req)
	if resp != nil {
		if pErr := eng.RunPostHooks(ctx, req, resp); pErr != nil {
			// Streaming WARN-and-swallow contract — log via test logger.
			logger.Warn("ollama: posthook error after streaming completion", "err", pErr)
		}
	}
	return rec, resp, err
}

// TestOllamaNDJSON_Chat_PostHooksFireAfterStreamCompletes drives a chat
// stream with text + thought chunks. Asserts:
//   - RunPostHooks fires exactly once
//   - The captured resp.Message.Content[0].Text is the concatenated
//     assistant text (NOT empty — the load-bearing aggregator
//     correctness invariant)
//   - The thinking part appears at Content[1] when applicable.
func TestOllamaNDJSON_Chat_PostHooksFireAfterStreamCompletes(t *testing.T) {
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello "}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "pondering"}},
	}
	req := &canonical.ChatRequest{Model: "auto"}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runNDJSONEmitterAndPostHooks(t, context.Background(), eng, req, chunks, true, final, nil, nilLogger())
	if err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1 (PostHook fires once per streaming request)", eng.postN)
	}
	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil, want aggregated response")
	}
	if len(resp.Message.Content) < 1 || resp.Message.Content[0].Text != "Hello world" {
		t.Errorf("Content[0].Text: got %v, want 'Hello world' (concatenated)", resp.Message.Content)
	}
	var sawThinking bool
	for _, p := range resp.Message.Content {
		if p.Kind == canonical.ContentKindThinking && p.Text == "pondering" {
			sawThinking = true
		}
	}
	if !sawThinking {
		t.Errorf("Content: missing ContentKindThinking 'pondering' part; got %+v", resp.Message.Content)
	}
}

// TestOllamaNDJSON_Generate_PostHooksFireAfterStreamCompletes — same
// shape for the /api/generate (handleGenerate) call site. isChat=false.
// Thinking is dropped on the wire per D-04 but text aggregation still
// works.
func TestOllamaNDJSON_Generate_PostHooksFireAfterStreamCompletes(t *testing.T) {
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "gen text"}},
	}
	req := &canonical.ChatRequest{Model: "auto"}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runNDJSONEmitterAndPostHooks(t, context.Background(), eng, req, chunks, false, final, nil, nilLogger())
	if err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1", eng.postN)
	}
	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil")
	}
	if len(resp.Message.Content) < 1 || resp.Message.Content[0].Text != "gen text" {
		t.Errorf("Content[0].Text: got %v, want 'gen text'", resp.Message.Content)
	}
}

// TestOllamaNDJSON_PostHooksFireOnPartialStreamError — Result()-error
// path still returns the partial aggregated response so PostHooks fire
// for forensics + duration_ms (T-df2-03 sync.Map leak mitigation).
// Same intent as the anthropic-side test.
func TestOllamaNDJSON_PostHooksFireOnPartialStreamError(t *testing.T) {
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "partial"}},
	}
	req := &canonical.ChatRequest{Model: "auto"}

	_, resp, err := runNDJSONEmitterAndPostHooks(t, context.Background(), eng, req, chunks, true, nil, errors.New("upstream blew up"), nilLogger())
	if err == nil {
		t.Fatal("runNDJSONEmitter: got nil err, want propagated stream error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response on Result() error")
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1 (PostHook fires on partial — forensics)", eng.postN)
	}
	if eng.lastPostResp == nil || len(eng.lastPostResp.Message.Content) < 1 ||
		eng.lastPostResp.Message.Content[0].Text != "partial" {
		t.Errorf("lastPostResp.Content: want first part 'partial', got %+v", eng.lastPostResp)
	}
}

// TestOllamaNDJSON_PostHookErrorLoggedNotPropagated — when RunPostHooks
// returns an error, runNDJSONEmitter has already written bytes to the
// wire. The handler logs at WARN and continues. T-df2-02.
func TestOllamaNDJSON_PostHookErrorLoggedNotPropagated(t *testing.T) {
	eng := &fakeEngine{postErr: errors.New("posthook failed")}
	var buf bytes.Buffer
	logger := nilLoggerJSON(&buf)

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "ok"}},
	}
	_, _, err := runNDJSONEmitterAndPostHooks(t, context.Background(), eng, &canonical.ChatRequest{Model: "auto"}, chunks, true, &canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil, logger)
	if err != nil {
		t.Fatalf("runNDJSONEmitter: got %v, want nil (PostHook error must NOT propagate)", err)
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1", eng.postN)
	}
	logged := buf.String()
	if !strings.Contains(logged, "posthook") {
		t.Errorf("slog log: missing 'posthook' substring; got %q", logged)
	}
	if !strings.Contains(logged, `"level":"WARN"`) {
		t.Errorf("slog level: missing WARN record for posthook error; got %q", logged)
	}
}

// TestOllamaNDJSON_PostHooksFireOnClientDisconnect — ctx-cancel mid-
// stream still returns the partial aggregation so PostHooks fire.
func TestOllamaNDJSON_PostHooksFireOnClientDisconnect(t *testing.T) {
	eng := &fakeEngine{}

	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}}
	// Channel intentionally NOT closed so the loop blocks after the chunk.
	defer close(ch)
	run := &fakeRunHandle{
		stream:    &fakeStream{ch: ch, result: &canonical.FinalResult{StopReason: canonical.StopUnknown}},
		sessionID: "disconnect",
	}

	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	rec := httptest.NewRecorder()
	resp, err := runNDJSONEmitter(ctx, noopCancelFn, rec, run, "auto", true, time.Now(), nilLogger(), &canonical.ChatRequest{Model: "auto"})
	if err == nil {
		t.Fatal("runNDJSONEmitter: got nil err, want ctx-cancel error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response on disconnect")
	}
	if pErr := eng.RunPostHooks(ctx, &canonical.ChatRequest{Model: "auto"}, resp); pErr != nil {
		t.Fatalf("RunPostHooks: %v", pErr)
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1 (PostHook fires on disconnect)", eng.postN)
	}
}

// TestOllamaNDJSON_ChatPostHookSeesToolCalls — when stream emits a
// kiro-native ChunkKindToolCall on /api/chat, the narration goes into
// Content[0].Text and sawKiroNativeToolCall flips on. The done:true
// line carries NO tool_calls (HIGH #2 two-path rule). Likewise the
// post-stream aggregated response carries the narration text and no
// Message.ToolCalls — exactly what the production streaming-coerce
// path already builds (the aggregator does NOT re-derive ToolCalls
// independently; it reuses the same final canonical shape). This is
// observational, not functional.
func TestOllamaNDJSON_ChatPostHookSeesToolCalls(t *testing.T) {
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_01",
			Name: "read",
			Args: map[string]any{"filePath": "a.md"},
		}},
	}
	req := &canonical.ChatRequest{
		Model: "auto",
		Tools: []canonical.ToolSpec{{Name: "read", Description: "stub"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runNDJSONEmitterAndPostHooks(t, context.Background(), eng, req, chunks, true, final, nil, nilLogger())
	if err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1", eng.postN)
	}
	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil")
	}
	// Narration text appears in Content[0].Text.
	if len(resp.Message.Content) < 1 || !strings.Contains(resp.Message.Content[0].Text, "[tool: read]") {
		t.Errorf("Content[0].Text: missing '[tool: read]' narration; got %+v", resp.Message.Content)
	}
	// HIGH #2 two-path rule: kiro-native → narration ONLY; ToolCalls
	// stays empty on the aggregator path. Coerce is skipped because
	// sawKiroNativeToolCall fired.
	if len(resp.Message.ToolCalls) != 0 {
		t.Errorf("ToolCalls: got %d, want 0 (kiro-native is narration-only per HIGH #2 two-path rule)", len(resp.Message.ToolCalls))
	}
}
