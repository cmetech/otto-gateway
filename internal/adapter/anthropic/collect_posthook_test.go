// Quick 260530-df2 — CollectAnthropicChat PostHook invocation tests.
//
// Closes the pre-existing non-streaming Anthropic PostHook gap.
// CollectAnthropicChat (collect.go) is the D-07 exception path that
// bypasses engine.Collect's PostHook traversal — it had its own
// aggregator that returned without firing PostHooks. Task 2 step 4
// wires the call at the tail (and on the short-circuit branch).

package anthropic

import (
	"context"
	"testing"

	"otto-gateway/internal/canonical"
)

// posthookFakeEngine is the smallest Engine satisfier that drives
// CollectAnthropicChat through Run + (the new) RunPostHooks call. It
// scripts a chunk list, a FinalResult, and tracks PostHook invocation.
type posthookFakeEngine struct {
	chunks  []canonical.Chunk
	final   *canonical.FinalResult
	runErr  error
	postN   int
	postErr error
	// lastResp captures the resp pointer the PostHook chain was called
	// with so tests can assert content shape.
	lastResp *canonical.ChatResponse
	// shortCircuit, when non-nil, makes Run return a RunHandle whose
	// ShortCircuitResponse() yields it — CollectAnthropicChat short-
	// circuit branch returns it verbatim and (after Task 2 step 4) also
	// fires PostHooks on it.
	shortCircuit *canonical.ChatResponse
}

func (p *posthookFakeEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil // unused — CollectAnthropicChat routes through Run.
}

func (p *posthookFakeEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	if p.runErr != nil {
		return nil, p.runErr
	}
	ch := make(chan canonical.Chunk, len(p.chunks)+1)
	for _, c := range p.chunks {
		ch <- c
	}
	close(ch)
	return &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  p.final,
		},
		sessionID: "session_posthook_collect",
		scResp:    p.shortCircuit,
	}, nil
}

func (p *posthookFakeEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	p.postN++
	p.lastResp = resp
	return p.postErr
}

// TestCollectAnthropicChat_PostHooksFire — non-streaming path. After
// aggregation, RunPostHooks is called exactly once with the fully-
// assembled response.
func TestCollectAnthropicChat_PostHooksFire(t *testing.T) {
	eng := &posthookFakeEngine{
		chunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello"}},
		},
		final: &canonical.FinalResult{StopReason: canonical.StopEndTurn},
	}
	req := &canonical.ChatRequest{Model: "auto"}

	resp, err := CollectAnthropicChat(context.Background(), eng, req)
	if err != nil {
		t.Fatalf("CollectAnthropicChat: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1 (closes pre-existing non-streaming PostHook gap)", eng.postN)
	}
	if eng.lastResp == nil {
		t.Fatal("lastResp: got nil, want aggregated response")
	}
	if len(resp.Message.Content) < 1 || resp.Message.Content[0].Text != "hello" {
		t.Errorf("Content[0].Text: got %v, want 'hello'", resp.Message.Content)
	}
	// PostHook saw the same pointer the caller received (mutation
	// contract — Codex H-5 carried forward).
	if eng.lastResp != resp {
		t.Errorf("PostHook resp pointer differs from returned response (Codex H-5 mutation contract broken)")
	}
}

// TestCollectAnthropicChat_PostHooksFireOnShortCircuit — when a PreHook
// short-circuited Run, the returned RunHandle.ShortCircuitResponse() is
// non-nil. CollectAnthropicChat returns it verbatim. After Task 2 step
// 4, PostHooks ALSO fire on the short-circuit response (mirrors
// engine.Collect at collect.go:114-122 where PostHooks run on short-
// circuit responses too — Codex H-5).
func TestCollectAnthropicChat_PostHooksFireOnShortCircuit(t *testing.T) {
	hookResp := &canonical.ChatResponse{
		StopReason: canonical.StopError,
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "request rejected"},
			},
		},
	}
	eng := &posthookFakeEngine{
		shortCircuit: hookResp,
		final:        &canonical.FinalResult{StopReason: canonical.StopError},
	}
	req := &canonical.ChatRequest{Model: "auto"}

	resp, err := CollectAnthropicChat(context.Background(), eng, req)
	if err != nil {
		t.Fatalf("CollectAnthropicChat: %v", err)
	}
	if resp != hookResp {
		t.Errorf("returned resp: got %p, want hook's pointer %p (short-circuit body must be preserved verbatim)", resp, hookResp)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1 (PostHooks must fire on short-circuit response — Codex H-5)", eng.postN)
	}
	if eng.lastResp != hookResp {
		t.Errorf("PostHook lastResp: got %p, want %p (PostHook should observe the short-circuit body)", eng.lastResp, hookResp)
	}
}
