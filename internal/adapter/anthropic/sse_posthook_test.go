// Quick 260530-df2 — SSE streaming PostHook invocation tests.
//
// Covers the streaming branch's RunPostHooks call site (handlers.go after
// runSSEEmitter returns). The plan's load-bearing risk is aggregator
// richness — these tests pin both the invocation AND the content shape
// of the resp the PostHook sees (text + thinking + tool_use, plus
// Message.ToolCalls per D-07).

package anthropic

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

// runSSEEmitterAndPostHooks drives runSSEEmitter against the supplied
// chunks + FinalResult and, on completion, calls eng.RunPostHooks with
// the aggregated response. Mirrors handlers.go's streaming branch shape
// (handlers.go:202-212 after Task 2 step 3) so tests exercise the same
// call sequence the production path does.
//
// model is "auto" so it matches existing golden conventions; req is
// supplied by the caller so tool_use tests can attach tools[].
func runSSEEmitterAndPostHooks(t *testing.T, ctx context.Context, eng Engine, req *canonical.ChatRequest, chunks []canonical.Chunk, final *canonical.FinalResult, finalErr error, logger *slog.Logger) (*httptest.ResponseRecorder, *canonical.ChatResponse, error) {
	t.Helper()
	ch := make(chan canonical.Chunk, len(chunks)+1)
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  final,
			err:    finalErr,
		},
		sessionID: "session_posthook",
	}
	rec := httptest.NewRecorder()
	resp, err := runSSEEmitter(ctx, rec, runHandle, "auto", 0, logger)
	if resp != nil {
		if pErr := eng.RunPostHooks(ctx, req, resp); pErr != nil {
			// Streaming WARN-and-swallow contract — log via the test
			// logger; mirror handlers.go production behavior.
			logger.Warn("anthropic: posthook error after streaming completion", "err", pErr)
		}
	}
	return rec, resp, err
}

// TestAnthropicSSE_PostHooksFireAfterStreamCompletes drives a stream of
// text + thought + tool_use chunks, then asserts:
//   - RunPostHooks was called exactly once (postN == 1)
//   - The captured resp.Message.Content has the concatenated text part,
//     the thinking part, AND the tool_use part with ID+Name+Input matching
//     the chunks emitted.
//   - resp.StopReason matches the FinalResult's StopReason.
//   - resp.Message.ToolCalls is populated per the D-07 Anthropic
//     exception (mirrors CollectAnthropicChat shape).
func TestAnthropicSSE_PostHooksFireAfterStreamCompletes(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello "}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "ponder"}},
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_01",
			Name: "read",
			Args: map[string]any{"filePath": "CLAUDE.md"},
		}},
	}
	req := &canonical.ChatRequest{Model: "auto"}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, req, chunks, final, nil, nullLogger())
	if err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}

	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1 (exactly one PostHook fire per streaming request)", eng.postN)
	}

	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil, want aggregated response")
	}
	// Text part is always at Content[0] per assembleAnthropicChatResponse.
	if len(resp.Message.Content) < 1 {
		t.Fatalf("Content: got %v, want at least 1 part (text)", resp.Message.Content)
	}
	if resp.Message.Content[0].Kind != canonical.ContentKindText {
		t.Errorf("Content[0].Kind: got %v, want ContentKindText", resp.Message.Content[0].Kind)
	}
	if resp.Message.Content[0].Text != "Hello world" {
		t.Errorf("Content[0].Text: got %q, want %q (concatenated deltas)", resp.Message.Content[0].Text, "Hello world")
	}

	// Look for thinking + tool_use parts. Order: text, thinking, tool_use.
	var sawThinking, sawToolUse bool
	for _, p := range resp.Message.Content {
		if p.Kind == canonical.ContentKindThinking && p.Text == "ponder" {
			sawThinking = true
		}
		if p.Kind == canonical.ContentKindToolUse && p.ToolUse != nil &&
			p.ToolUse.ID == "toolu_01" && p.ToolUse.Name == "read" {
			if fp, _ := p.ToolUse.Input["filePath"].(string); fp == "CLAUDE.md" {
				sawToolUse = true
			}
		}
	}
	if !sawThinking {
		t.Errorf("Content: missing ContentKindThinking 'ponder' part; got %+v", resp.Message.Content)
	}
	if !sawToolUse {
		t.Errorf("Content: missing ContentKindToolUse with id=toolu_01 name=read input.filePath=CLAUDE.md; got %+v", resp.Message.Content)
	}

	// D-07 exception: Message.ToolCalls populated alongside Content[].
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls: got %d, want 1", len(resp.Message.ToolCalls))
	}
	if resp.Message.ToolCalls[0].ID != "toolu_01" {
		t.Errorf("ToolCalls[0].ID: got %q, want %q", resp.Message.ToolCalls[0].ID, "toolu_01")
	}

	if resp.StopReason != canonical.StopEndTurn {
		t.Errorf("StopReason: got %v, want StopEndTurn", resp.StopReason)
	}
}

// TestAnthropicSSE_PostHooksFireOnPartialStreamError exercises the
// Result()-error path. The plan explicitly favors returning the
// (partial) aggregated response on rerr != nil so operators get
// forensics + duration_ms on terminal stream failures. The streaming
// PostHook fires on the partial — chat-trace.log gets a
// post_chain_out record with whatever content arrived before the
// error. (The handler's eng.Run-fails-pre-headers path, in contrast,
// returns at handlers.go:179-187 BEFORE runSSEEmitter is called, so
// no PostHook fires there — verified by the handler integration
// tests, not by this emitter-level test.)
func TestAnthropicSSE_PostHooksFireOnPartialStreamError(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "partial"}},
	}
	// Result() returns error → finalizeStream returns (partialResp, err).
	_, resp, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, &canonical.ChatRequest{Model: "auto"}, chunks, nil, errors.New("upstream blew up"), nullLogger())
	if err == nil {
		t.Fatalf("runSSEEmitter: got nil err, want propagated stream error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response on Result() error (operators want forensics)")
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1 (PostHook fires on partial — forensics + duration_ms)", eng.postN)
	}
	// The partial aggregator should carry the text that did arrive.
	if eng.lastPostResp == nil || len(eng.lastPostResp.Message.Content) < 1 ||
		eng.lastPostResp.Message.Content[0].Text != "partial" {
		t.Errorf("lastPostResp.Content: want first part text 'partial', got %+v", eng.lastPostResp)
	}
}

// TestAnthropicSSE_PostHookErrorDoesNotFailResponse — when RunPostHooks
// returns an error, the SSE emitter has already written bytes to the
// wire. The client has received their stream successfully. The
// production handler logs the error at WARN and returns normally; tests
// assert (a) runSSEEmitter still returned nil (no propagation), (b) a
// WARN slog record carrying "anthropic: posthook" was emitted.
func TestAnthropicSSE_PostHookErrorDoesNotFailResponse(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{postErr: errors.New("posthook failed")}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "ok"}},
	}
	_, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, &canonical.ChatRequest{Model: "auto"}, chunks, &canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil, logger)
	if err != nil {
		t.Fatalf("runSSEEmitter: got %v, want nil (PostHook error must NOT propagate to client)", err)
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

// TestAnthropicSSE_PostHooksFireOnClientDisconnect — when streamCtx is
// canceled mid-stream, runSSEEmitter returns ctx.Err() but the aggregator
// is preserved on the sseEmitter so the handler can still call
// RunPostHooks on the partial aggregation (operators want forensics +
// duration_ms even on disconnect).
//
// Task 2 spec: on the disconnect path, runSSEEmitter MUST still return
// the (partial) aggregated response alongside the error. The helper
// then fires RunPostHooks because resp != nil.
func TestAnthropicSSE_PostHooksFireOnClientDisconnect(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}

	// Channel that delivers one chunk then never closes. Cancel ctx
	// after first chunk is consumed to force the ctx.Done path.
	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "partial"}}
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: canonical.StopUnknown},
		},
		sessionID: "session_disconnect",
	}
	defer close(ch)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so the loop processes the chunk first.
	go func() {
		// Wait until the chunk has been observed by polling the recorder.
		// Implementation detail: cancel as soon as we're scheduled — the
		// loop is fast enough that the chunk lands first under normal
		// conditions. If not, the test still proves the contract: on any
		// disconnect, PostHook fires with whatever was aggregated.
		cancel()
	}()
	rec := httptest.NewRecorder()
	resp, err := runSSEEmitter(ctx, rec, runHandle, "auto", 0, nullLogger())
	if err == nil {
		t.Fatalf("runSSEEmitter: got nil err, want ctx-cancel error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response on disconnect")
	}
	if pErr := eng.RunPostHooks(ctx, &canonical.ChatRequest{}, resp); pErr != nil {
		t.Fatalf("RunPostHooks: %v", pErr)
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1 (PostHook must fire on disconnect with partial aggregation)", eng.postN)
	}
}
