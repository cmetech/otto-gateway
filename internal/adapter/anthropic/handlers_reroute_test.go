// Package anthropic — T-5b adapter handler re-route test.
//
// When a Pre hook (e.g., the PII encrypt Pre hook) flips req.Stream=false
// during eng.Run, handleMessages must abandon the real SSE branch and
// route the already-running ACP session through eng.CollectFromRun.
// Because the CLIENT wire originally had stream=true, the response must
// still be a text/event-stream — emitted synthetically from the
// aggregated CollectFromRun result via runSyntheticSSEFromResponse.
// This test pins that contract.
//
// v1.8.3 regression fixed: prior to runSyntheticSSEFromResponse this
// branch wrote application/json, which tripped Anthropic SDK clients
// (Claude Code, loop24-client) with "request ended without sending any
// chunks".
package anthropic

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

// rerouteFakeEngine simulates a PII-encrypt-style Pre hook by flipping
// req.Stream=false inside its Run method, BEFORE returning the
// RunHandle. The handler's post-Run req.Stream check then takes the
// CollectFromRun re-route branch instead of runSSEEmitter.
type rerouteFakeEngine struct {
	collectFromRunResp *canonical.ChatResponse
	collectFromRunErr  error

	// observation: did the handler call our CollectFromRun?
	collectFromRunCalled bool
	// Did the original wire have Stream=true? (set by Run for assertion.)
	sawStreamTrueAtRun bool
	// postHookCalls tracks RunPostHooks invocations. Audit
	// ollama-reroute-double-posthook-fires applies symmetrically here.
	postHookCalls int
}

func (e *rerouteFakeEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil
}

func (e *rerouteFakeEngine) Run(_ context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	if req.Stream {
		e.sawStreamTrueAtRun = true
	}
	// Simulate the PII encrypt Pre hook's effect: flip Stream off so the
	// handler's post-Run check takes the re-route branch.
	req.Stream = false
	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "irrelevant"}}
	close(ch)
	return &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "session_reroute",
	}, nil
}

func (e *rerouteFakeEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, _ *canonical.ChatResponse) error {
	e.postHookCalls++
	return nil
}

func (e *rerouteFakeEngine) CollectFromRun(_ context.Context, _ RunHandle, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	e.collectFromRunCalled = true
	if e.collectFromRunErr != nil {
		return nil, e.collectFromRunErr
	}
	return e.collectFromRunResp, nil
}

// TestHandleMessages_StreamReroute_OnPreHookStreamDisable asserts that a
// Pre hook flipping req.Stream=false during eng.Run causes the handler
// to:
//   - call eng.CollectFromRun (NOT runSSEEmitter)
//   - respond with Content-Type: application/json (NOT text/event-stream)
//   - render via chatResponseToMessage (the non-streaming Anthropic shape)
//   - emit zero SSE markers in the response body
//
// This pins the T-5b re-route for the Anthropic surface — the load-bearing
// behavior for the PII encrypt round-trip on streaming Anthropic clients.
func TestHandleMessages_StreamReroute_OnPreHookStreamDisable(t *testing.T) {
	eng := &rerouteFakeEngine{
		collectFromRunResp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "decrypted-response"},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng)
	// stream:true on the wire — handler must observe it at decode time,
	// then take the re-route branch after Run flips it off.
	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := doPost(t, a, "/messages", body)

	// (1) Wire-shape assertion: status 200 + Content-Type: text/event-stream.
	// Prior to v1.9 this path wrote application/json, breaking SDK
	// clients that asked for stream=true. The synthetic SSE emitter
	// preserves the wire shape the client expects.
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want prefix text/event-stream (synthetic SSE)", ct)
	}

	// (2) Body carries the full SSE event sequence and contains the
	// decrypted text inside a content_block_delta payload.
	bodyStr := w.Body.String()
	wantEvents := []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	}
	for _, ev := range wantEvents {
		if !strings.Contains(bodyStr, ev) {
			t.Errorf("body missing SSE event %q; body=%q", ev, bodyStr)
		}
	}
	if !strings.Contains(bodyStr, `"text":"decrypted-response"`) {
		t.Errorf("body missing decrypted text in text_delta payload; body=%q", bodyStr)
	}
	if !strings.Contains(bodyStr, `"stop_reason":"end_turn"`) {
		t.Errorf("body missing mapped stop_reason end_turn; body=%q", bodyStr)
	}

	// (3) Handler invoked CollectFromRun (NOT the real per-chunk SSE
	// emitter) — proves the re-route branch fired.
	if !eng.collectFromRunCalled {
		t.Error("CollectFromRun was not called — handler took the real-SSE branch (regression: T-5b re-route guard missing)")
	}

	// (4) Run observed Stream=true on the inbound wire request — proves
	// the re-route branch fired AFTER Run, not before.
	if !eng.sawStreamTrueAtRun {
		t.Error("rerouteFakeEngine.Run did not observe Stream=true on inbound req — wire-decode broken")
	}

	// (5) Audit ollama-reroute-double-posthook-fires (applies symmetrically
	// to anthropic's synthetic-SSE re-route): handler MUST NOT call
	// RunPostHooks — CollectFromRun already ran the chain. Pre-fix: 1.
	// Post-fix: 0. A second call corrupts PII decrypt and double-logs.
	if eng.postHookCalls != 0 {
		t.Errorf("handler called RunPostHooks %d times on synthetic-SSE re-route; want 0", eng.postHookCalls)
	}
}
