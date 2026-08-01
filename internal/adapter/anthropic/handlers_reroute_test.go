// Package anthropic — T-5b adapter handler re-route test.
//
// When a Pre hook (e.g., the PII encrypt Pre hook) flips req.Stream=false
// during eng.Run, handleMessages must abandon the real SSE branch and
// route the already-running ACP session through the Anthropic-owned
// from-run collector.
// Because the CLIENT wire originally had stream=true, the response must
// still be a text/event-stream — emitted synthetically from the
// aggregated result via runSyntheticSSEFromResponse.
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
// Anthropic collector re-route branch instead of runSSEEmitter.
type rerouteFakeEngine struct {
	// observation: did the handler call the generic CollectFromRun?
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
	ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "encrypted-response"}}
	close(ch)
	return &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "session_reroute",
	}, nil
}

func (e *rerouteFakeEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	e.postHookCalls++
	if resp != nil && len(resp.Message.Content) > 0 {
		resp.Message.Content[0].Text = "decrypted-response"
	}
	return nil
}

func (e *rerouteFakeEngine) CollectFromRun(_ context.Context, _ RunHandle, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	e.collectFromRunCalled = true
	return nil, nil
}

// TestHandleMessages_StreamReroute_OnPreHookStreamDisable asserts that a
// Pre hook flipping req.Stream=false during eng.Run causes the handler
// to:
//   - aggregate through the Anthropic-owned from-run collector
//   - run the PostHook chain exactly once before response bytes
//   - respond with synthetic text/event-stream output
//   - avoid the generic eng.CollectFromRun path
//
// This pins the T-5b re-route for the Anthropic surface — the load-bearing
// behavior for the PII encrypt round-trip on streaming Anthropic clients.
func TestHandleMessages_StreamReroute_OnPreHookStreamDisable(t *testing.T) {
	eng := &rerouteFakeEngine{}
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

	// (3) Handler used the Anthropic-owned from-run collector, not the
	// generic engine collector. The decrypted output above proves that
	// the already-started run was drained and its PostHook chain ran.
	if eng.collectFromRunCalled {
		t.Error("generic CollectFromRun was called; want Anthropic-owned from-run collector")
	}

	// (4) Run observed Stream=true on the inbound wire request — proves
	// the re-route branch fired AFTER Run, not before.
	if !eng.sawStreamTrueAtRun {
		t.Error("rerouteFakeEngine.Run did not observe Stream=true on inbound req — wire-decode broken")
	}

	// (5) The Anthropic-owned collector fires the complete PostHook chain;
	// the handler must not fire it a second time. A second call corrupts
	// PII decrypt and double-logs.
	if eng.postHookCalls != 1 {
		t.Errorf("RunPostHooks calls=%d on synthetic-SSE re-route; want exactly 1", eng.postHookCalls)
	}
}
