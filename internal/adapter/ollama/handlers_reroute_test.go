// Package ollama — T-5b adapter handler re-route test.
//
// When a Pre hook (e.g., the PII encrypt Pre hook) flips req.Stream=false
// during eng.Run, handleChat must abandon the NDJSON streaming branch and
// route the already-running ACP session through eng.CollectFromRun,
// rendering via chatResponseToWire (the Ollama non-streaming response
// shape).
package ollama

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
// CollectFromRun re-route branch instead of runNDJSONEmitter.
type rerouteFakeEngine struct {
	collectFromRunResp *canonical.ChatResponse
	collectFromRunErr  error

	collectFromRunCalled bool
	sawStreamTrueAtRun   bool
}

func (e *rerouteFakeEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil
}

func (e *rerouteFakeEngine) Run(_ context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	if req.Stream {
		e.sawStreamTrueAtRun = true
	}
	req.Stream = false
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	return newFakeRunHandle([]canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "irrelevant"}},
	}, final, nil), nil
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

// TestHandleChat_StreamReroute_OnPreHookStreamDisable asserts that a Pre
// hook flipping req.Stream=false during eng.Run causes the /api/chat
// handler to call eng.CollectFromRun (NOT runNDJSONEmitter) and render
// via the Ollama non-streaming chat shape (ollamaChatResponse with
// done:true on a single object, NOT a stream of NDJSON records).
// Closes the PII encrypt round-trip for streaming Ollama clients
// (LangFlow / any stream:true /api/chat caller).
func TestHandleChat_StreamReroute_OnPreHookStreamDisable(t *testing.T) {
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
	a := newTestAdapter(eng, nil)
	// stream:true on the wire — handler must observe it at decode time,
	// then take the re-route branch after Run flips it off.
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := doPost(t, a, "/chat", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want prefix application/json (no NDJSON leak on re-route)", ct)
	}

	// Single-object non-streaming Ollama shape. The streaming path
	// would emit MULTIPLE NDJSON records — assert that decoding yields
	// ONE complete object with done:true.
	var resp ollamaChatResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode ollamaChatResponse: %v; body=%s", err, w.Body.String())
	}
	if resp.Message.Role != "assistant" {
		t.Errorf("message.role: got %q, want assistant", resp.Message.Role)
	}
	if resp.Message.Content != "decrypted-response" {
		t.Errorf("message.content: got %q, want 'decrypted-response'", resp.Message.Content)
	}
	if !resp.Done {
		t.Error("done: got false, want true (non-streaming terminal record)")
	}

	if !eng.collectFromRunCalled {
		t.Error("CollectFromRun was not called — handler took the NDJSON streaming branch (regression: T-5b re-route guard missing)")
	}
	if !eng.sawStreamTrueAtRun {
		t.Error("rerouteFakeEngine.Run did not observe Stream=true on inbound req — wire-decode broken")
	}

	// A streaming response would contain MULTIPLE JSON objects separated
	// by newlines (NDJSON). Re-route MUST produce exactly one decodable
	// object — assert the body has at most one newline-trailing terminator.
	bodyStr := w.Body.String()
	// Count NDJSON record terminators (newlines between objects). A
	// well-formed single JSON response will have at most ONE trailing
	// newline (from json.Encoder.Encode); the streaming path emits N+1.
	nlCount := strings.Count(bodyStr, "\n")
	if nlCount > 1 {
		t.Errorf("body contains %d newlines — suggests NDJSON record stream leak on re-route; body=%q", nlCount, bodyStr)
	}
}
