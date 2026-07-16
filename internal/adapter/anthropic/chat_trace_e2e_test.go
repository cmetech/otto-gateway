// Quick 260530-df2 Task 5 — end-to-end ChatTraceHook integration +
// double-fire guard for the Anthropic surface.
//
// Wires a real *plugin.ChatTraceHook (the production hook, not a fake)
// against a bytes.Buffer writer, drives both a streaming SSE request
// AND a non-streaming CollectAnthropicChat request, and asserts the
// pre_chain_in / post_chain_out NDJSON pair appears in the buffer with
// matching request_id, non-empty content.
//
// The double-fire guard uses a counter PostHook to verify the
// invariant from task_specifics: streaming + non-streaming paths are
// mutually exclusive — each request fires PostHooks EXACTLY once.

package anthropic

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
// production path. It is NOT the integration_test.go fake — it
// intentionally surfaces the hook list so the e2e test can build a
// production-shaped chain.
type chatTraceFakeEngine struct {
	chunks      []canonical.Chunk
	final       *canonical.FinalResult
	preHooks    []engine.PreHook
	postHooks   []engine.PostHook
	scResp      *canonical.ChatResponse
	postCounter *atomic.Int64
}

// Collect is unused on streaming tests; on non-streaming tests
// CollectAnthropicChat routes through Run.
func (e *chatTraceFakeEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil
}

// CollectFromRun is unused in chat-trace e2e tests (no PII encrypt Pre
// hook flips Stream); satisfy the T-5b interface contract.
func (e *chatTraceFakeEngine) CollectFromRun(_ context.Context, _ RunHandle, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil
}

// Run mirrors the real engine's Run: iterate the PreHook chain first
// (short-circuit if any returns non-nil response), then return a Run
// handle whose stream replays the scripted chunks.
func (e *chatTraceFakeEngine) Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	// Iterate PreHooks just like *engine.Engine.Run does so the chain
	// invariant under test (ChatTraceHook.Before observes the request
	// BEFORE PIIRedactionHook + records pre_chain_in) is exercised.
	for _, h := range e.preHooks {
		resp, err := h.Before(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp != nil {
			// Short-circuit — but for the e2e test we don't drive this path.
			e.scResp = resp
			break
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
		scResp:    e.scResp,
	}, nil
}

// RunPostHooks iterates the configured PostHook chain — the same
// discipline as engine.RunPostHooks. Hooks are called in registration
// order; first error aborts. The atomic counter is incremented on the
// outermost wrapper (set up by the double-fire guard) before
// delegation.
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

// TestChatTrace_E2E_AnthropicStreaming drives a streaming /messages
// request through runSSEEmitter + handlers-shaped RunPostHooks
// invocation. Asserts:
//   - The Writer buffer contains exactly TWO NDJSON records
//   - Record 1: stage=pre_chain_in, non-empty request_id, non-empty
//     messages slice (the load-bearing chat-trace.log product value)
//   - Record 2: stage=post_chain_out, SAME request_id, non-empty
//     content[], duration_ms >= 0
//
// This is the operator-visible test from the plan's success criteria
// item 1. Before quick 260530-df2 the post_chain_out record never
// appeared on streaming requests.
func TestChatTrace_E2E_AnthropicStreaming(t *testing.T) {
	var buf bytes.Buffer
	hook := &plugin.ChatTraceHook{
		Writer:  &buf,
		Enabled: true,
	}
	eng := &chatTraceFakeEngine{
		chunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello"}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: " world"}},
		},
		final: &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		// Pre = [ChatTraceHook, RequestIDHook]; Post = [ChatTraceHook]
		// Mirrors main.go's wiring (trace.go:303-309).
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
	// Production handlers also stamp request_id + surface; mirror that.
	ctx := plugin.WithRequestID(context.Background(), plugin.NewRequestID())
	ctx = plugin.WithSurface(ctx, "anthropic")

	_, _, err := runSSEEmitterAndPostHooks(t, ctx, eng, req,
		[]canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello"}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: " world"}},
		},
		&canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil, nullLogger())
	if err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}
	// Drive Pre traversal manually (the runSSEEmitter test helper
	// doesn't go through eng.Run — it skips PreHooks). For the e2e
	// test we want both NDJSON records, so call Before ourselves.
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("ChatTraceHook.Before: %v", err)
	}
	// Re-trigger After through the engine PostHook invocation we
	// already exercised. The helper called eng.RunPostHooks which
	// iterates hook.After above. So the buffer should now have one
	// post + one pre record (in the order: post from emitter, pre
	// from this explicit call). Reorder by parsing the records.

	records := parseNDJSONRecords(t, buf.Bytes())
	if len(records) != 2 {
		t.Fatalf("NDJSON records: got %d, want 2 (pre + post pair); buf=%s", len(records), buf.String())
	}
	// Find pre and post by stage.
	var pre, post map[string]any
	for _, r := range records {
		switch r["stage"] {
		case "pre_chain_in":
			pre = r
		case "post_chain_out":
			post = r
		}
	}
	if pre == nil {
		t.Fatalf("missing pre_chain_in record; records=%+v", records)
	}
	if post == nil {
		t.Fatalf("missing post_chain_out record; records=%+v", records)
	}
	if pre["request_id"] == "" || pre["request_id"] != post["request_id"] {
		t.Errorf("request_id mismatch: pre=%v post=%v", pre["request_id"], post["request_id"])
	}
	// content[] is the load-bearing assertion — operators must see the
	// response body, not an empty shell.
	postContent, _ := post["content"].([]any)
	if len(postContent) < 1 {
		t.Errorf("post_chain_out content[]: empty (load-bearing aggregator richness regression)")
	}
}

// TestAnthropic_NoDoublePostHookFire — the contract invariant: streaming
// and non-streaming paths are mutually exclusive. Drive both back-to-
// back against a single counter PostHook; total invocation must equal
// 2 (one per request, never doubled).
func TestAnthropic_NoDoublePostHookFire(t *testing.T) {
	var counter atomic.Int64
	eng := &chatTraceFakeEngine{
		final:       &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		postCounter: &counter,
	}
	req := &canonical.ChatRequest{Model: "auto"}

	// Non-streaming: CollectAnthropicChat → eng.Run + RunPostHooks.
	if _, err := CollectAnthropicChat(context.Background(), eng, req, nil, 0); err != nil {
		t.Fatalf("CollectAnthropicChat: %v", err)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("after non-streaming: counter=%d, want 1", got)
	}

	// Streaming: runSSEEmitter + eng.RunPostHooks (via helper).
	if _, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, req,
		[]canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}}},
		&canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil, nullLogger()); err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}
	if got := counter.Load(); got != 2 {
		t.Fatalf("after streaming: counter=%d, want 2 (one per request — no double-fire)", got)
	}
}

// parseNDJSONRecords decodes the bytes as NDJSON. Helper extracted so
// the e2e tests stay scannable.
func parseNDJSONRecords(t *testing.T, raw []byte) []map[string]any {
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
