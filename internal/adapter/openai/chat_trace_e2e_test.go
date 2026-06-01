// Quick 260530-df2 Task 5 — end-to-end ChatTraceHook integration +
// double-fire guard for the OpenAI surface.

package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin"
)

// chatTraceFakeEngine drives a real ChatTraceHook chain on the
// production path for /v1/chat/completions streaming.
type chatTraceFakeEngine struct {
	chunks      []canonical.Chunk
	final       *canonical.FinalResult
	preHooks    []engine.PreHook
	postHooks   []engine.PostHook
	postCounter *atomic.Int64
}

// Collect mirrors engine.Collect for the non-streaming double-fire
// guard.
func (e *chatTraceFakeEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	for _, h := range e.preHooks {
		resp, err := h.Before(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp != nil {
			return resp, nil
		}
	}
	resp := &canonical.ChatResponse{
		Model: req.Model,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
		},
		StopReason: canonical.StopEndTurn,
	}
	if e.postCounter != nil {
		e.postCounter.Add(1)
	}
	for _, h := range e.postHooks {
		if err := h.After(ctx, req, resp); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (e *chatTraceFakeEngine) Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	for _, h := range e.preHooks {
		if _, err := h.Before(ctx, req); err != nil {
			return nil, err
		}
	}
	ch := make(chan canonical.Chunk, len(e.chunks)+1)
	for _, c := range e.chunks {
		ch <- c
	}
	close(ch)
	return &fakeRunHandle{
		stream:    &fakeStream{chunks: ch, final: e.final},
		sessionID: "session_e2e",
	}, nil
}

func (e *chatTraceFakeEngine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	if e.postCounter != nil {
		e.postCounter.Add(1)
	}
	for _, h := range e.postHooks {
		if err := h.After(ctx, req, resp); err != nil {
			return err
		}
	}
	return nil
}

// CollectFromRun is unused in chat-trace e2e tests; satisfy T-5b interface.
func (e *chatTraceFakeEngine) CollectFromRun(_ context.Context, _ RunHandle, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil
}

// TestChatTrace_E2E_OpenAIStreaming drives a streaming
// /v1/chat/completions request and asserts the pre_chain_in /
// post_chain_out pair appears in the ChatTraceHook buffer with
// matching request_id + non-empty content[].
func TestChatTrace_E2E_OpenAIStreaming(t *testing.T) {
	var buf bytes.Buffer
	hook := &plugin.ChatTraceHook{Writer: &buf, Enabled: true}
	eng := &chatTraceFakeEngine{
		chunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "streaming"}},
		},
		final:     &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		preHooks:  []engine.PreHook{hook, &plugin.RequestIDHook{}},
		postHooks: []engine.PostHook{hook},
	}
	req := &canonical.ChatRequest{
		Model: "auto",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "hi"},
			}},
		},
	}
	ctx := plugin.WithRequestID(context.Background(), plugin.NewRequestID())
	ctx = plugin.WithSurface(ctx, "openai")

	// Run Pre via eng.Run, then drive the SSE emitter, then fire Post
	// the way handlers.go does.
	_, err := eng.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// We already exercised PreHooks via Run above. Now drive the
	// emitter on a fresh RunHandle (with chunks) and then trigger
	// Post via eng.RunPostHooks.
	_, _, sseErr := runSSEEmitterAndPostHooks(t, ctx, eng, req,
		[]canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "streaming"}},
		},
		&canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil, nullLogger())
	if sseErr != nil {
		t.Fatalf("runSSEEmitter: %v", sseErr)
	}

	records := parseOpenAINDJSONRecords(t, buf.Bytes())
	if len(records) != 2 {
		t.Fatalf("NDJSON records: got %d, want 2; buf=%s", len(records), buf.String())
	}
	var pre, post map[string]any
	for _, r := range records {
		switch r["stage"] {
		case "pre_chain_in":
			pre = r
		case "post_chain_out":
			post = r
		}
	}
	if pre == nil || post == nil {
		t.Fatalf("missing pre/post pair; records=%+v", records)
	}
	if pre["request_id"] == "" || pre["request_id"] != post["request_id"] {
		t.Errorf("request_id mismatch: pre=%v post=%v", pre["request_id"], post["request_id"])
	}
	if content, _ := post["content"].([]any); len(content) < 1 {
		t.Errorf("post_chain_out content[]: empty (load-bearing aggregator richness)")
	}
}

// TestOpenAI_NoDoublePostHookFire — counter PostHook fires exactly
// once per request whether streaming or non-streaming.
func TestOpenAI_NoDoublePostHookFire(t *testing.T) {
	var counter atomic.Int64
	eng := &chatTraceFakeEngine{
		chunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}},
		},
		final:       &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		postCounter: &counter,
	}
	req := &canonical.ChatRequest{Model: "auto"}

	if _, err := eng.Collect(context.Background(), req); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("after non-streaming: counter=%d, want 1", got)
	}

	if _, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, req,
		[]canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}}},
		&canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil, nullLogger()); err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}
	if got := counter.Load(); got != 2 {
		t.Fatalf("after streaming: counter=%d, want 2 (one per request)", got)
	}
}

// parseOpenAINDJSONRecords decodes ChatTraceHook NDJSON records from
// the buffer. Duplicated locally to keep the test file self-contained.
func parseOpenAINDJSONRecords(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			break
		}
		out = append(out, rec)
	}
	return out
}
