// Package anthropic — T-5b adapter handler re-route test.
//
// When a Pre hook (e.g., the PII encrypt Pre hook) flips req.Stream=false
// during eng.Run, handleMessages must abandon the SSE branch and route
// the already-running ACP session through eng.CollectFromRun, rendering
// via the surface's non-streaming JSON response shape
// (chatResponseToMessage). This test pins that contract.
package anthropic

import (
	"context"
	"encoding/json"
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

	// (1) Wire-shape assertion: status 200 + Content-Type: application/json
	// (NOT text/event-stream — the regression detector for the SSE
	// header leak on the re-route path).
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want prefix application/json (no SSE leak on re-route)", ct)
	}

	// (2) Body decodes as the Anthropic non-streaming response shape.
	var resp anthropicMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode anthropicMessage: %v; body=%s", err, w.Body.String())
	}
	if resp.Type != "message" || resp.Role != "assistant" {
		t.Errorf("type/role: got %q/%q, want message/assistant", resp.Type, resp.Role)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "decrypted-response" {
		t.Errorf("content: got %+v, want one text block 'decrypted-response'", resp.Content)
	}

	// (3) Handler invoked CollectFromRun (NOT the SSE emitter).
	if !eng.collectFromRunCalled {
		t.Error("CollectFromRun was not called — handler took the SSE branch (regression: T-5b re-route guard missing)")
	}

	// (4) Run observed Stream=true on the inbound wire request — proves
	// the re-route branch fired AFTER Run, not before.
	if !eng.sawStreamTrueAtRun {
		t.Error("rerouteFakeEngine.Run did not observe Stream=true on inbound req — wire-decode broken")
	}

	// (5) No SSE markers in the body — the SSE emitter MUST NOT have run.
	bodyStr := w.Body.String()
	for _, marker := range []string{"event: ", "event: message_start", "data: "} {
		if strings.Contains(bodyStr, marker) {
			t.Errorf("body contains SSE marker %q — T-5b re-route did not bypass runSSEEmitter; body=%q", marker, bodyStr)
		}
	}
}
