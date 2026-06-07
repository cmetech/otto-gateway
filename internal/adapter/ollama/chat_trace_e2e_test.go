// Quick 260530-df2 Task 5 — end-to-end ChatTraceHook integration +
// double-fire guard for the Ollama surface.

package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin"
)

// chatTraceFakeEngine drives a real ChatTraceHook chain on the
// production path for /api/chat + /api/generate streaming.
type chatTraceFakeEngine struct {
	chunks      []canonical.Chunk
	final       *canonical.FinalResult
	preHooks    []engine.PreHook
	postHooks   []engine.PostHook
	postCounter *atomic.Int64
}

// Collect mirrors engine.Collect for the non-streaming double-fire
// guard: iterate PreHooks, then PostHooks via the same code path the
// real engine uses. The ollama handlers route stream:false through
// eng.Collect (NOT through RunPostHooks).
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
	// Build a minimal canonical response — text-only.
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
		stream:    &fakeStream{ch: ch, result: e.final},
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

// TestChatTrace_E2E_OllamaStreaming drives a streaming /api/chat
// request through runNDJSONEmitter + handler-shaped RunPostHooks call.
// Asserts the pre_chain_in / post_chain_out pair appears in the
// ChatTraceHook buffer with matching request_id + non-empty content[].
func TestChatTrace_E2E_OllamaStreaming(t *testing.T) {
	var buf bytes.Buffer
	hook := &plugin.ChatTraceHook{Writer: &buf, Enabled: true}
	eng := &chatTraceFakeEngine{
		chunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "stream"}},
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
	ctx = plugin.WithSurface(ctx, "ollama")

	// Drive Run (fires Pre) + emitter + RunPostHooks (fires Post).
	run, err := eng.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, _, _ = runNDJSONEmitterDirect(t, ctx, run, true, req)
	// Manually invoke RunPostHooks the way handlers.go does after
	// runNDJSONEmitter returns.
	resp := &canonical.ChatResponse{
		Model: req.Model,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "stream"}},
		},
		StopReason: canonical.StopEndTurn,
	}
	if err := eng.RunPostHooks(ctx, req, resp); err != nil {
		t.Fatalf("RunPostHooks: %v", err)
	}

	records := parseOllamaNDJSONRecords(t, buf.Bytes())
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

// TestOllama_NoDoublePostHookFire — counter PostHook fires exactly
// once per request whether the path is non-streaming (eng.Collect) or
// streaming (eng.Run + eng.RunPostHooks).
func TestOllama_NoDoublePostHookFire(t *testing.T) {
	var counter atomic.Int64
	eng := &chatTraceFakeEngine{
		chunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}},
		},
		final:       &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		postCounter: &counter,
	}
	req := &canonical.ChatRequest{Model: "auto"}

	// Non-streaming: eng.Collect path (fires the counter via the
	// chatTraceFakeEngine.Collect implementation above).
	if _, err := eng.Collect(context.Background(), req); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("after non-streaming: counter=%d, want 1", got)
	}

	// Streaming: simulate handlers.go's pattern — eng.Run + emitter +
	// eng.RunPostHooks.
	run, err := eng.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, _, _ = runNDJSONEmitterDirect(t, context.Background(), run, true, req)
	resp := &canonical.ChatResponse{
		Model: req.Model,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "x"}},
		},
	}
	if err := eng.RunPostHooks(context.Background(), req, resp); err != nil {
		t.Fatalf("RunPostHooks: %v", err)
	}
	if got := counter.Load(); got != 2 {
		t.Fatalf("after streaming: counter=%d, want 2 (one per request)", got)
	}
}

// runNDJSONEmitterDirect drives runNDJSONEmitter with the supplied
// RunHandle. Returns the (resp, body, err) tuple. Test-helper that
// honors the wide signature without the PostHook wrapper that
// sse_posthook_test.go's helper uses (we drive Post ourselves above).
func runNDJSONEmitterDirect(t *testing.T, ctx context.Context, run RunHandle, isChat bool, req *canonical.ChatRequest) (*canonical.ChatResponse, []byte, error) { //nolint:unparam // helper-pair symmetry with runNDJSONEmitterAndPostHooks
	t.Helper()
	w := newDiscardFlusher()
	resp, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", isChat, time.Now(), nilLogger(), req, 0)
	return resp, w.bytes(), err
}

// discardFlusher is a minimal http.ResponseWriter + http.Flusher used
// when the e2e test only cares about the post-stream PostHook
// invocation, not the wire bytes.
type discardFlusher struct {
	hdr http.Header
	buf bytes.Buffer
}

func newDiscardFlusher() *discardFlusher { return &discardFlusher{hdr: http.Header{}} }

func (d *discardFlusher) Header() http.Header { return d.hdr }
func (d *discardFlusher) Write(p []byte) (int, error) {
	return d.buf.Write(p)
}
func (d *discardFlusher) WriteHeader(int) {}
func (d *discardFlusher) Flush()          {}
func (d *discardFlusher) bytes() []byte   { return d.buf.Bytes() }

// parseOllamaNDJSONRecords decodes ChatTraceHook NDJSON records from
// the buffer. (Mirrors anthropic's parseNDJSONRecords — duplicated to
// keep the test file self-contained.)
func parseOllamaNDJSONRecords(t *testing.T, raw []byte) []map[string]any {
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
