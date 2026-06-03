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
	if !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Errorf("Content-Type: got %q, want prefix application/x-ndjson (synthetic NDJSON)", ct)
	}

	// Synthetic NDJSON: one done:false line carrying the decrypted text,
	// followed by one done:true terminal line. Both must decode as
	// independent JSON objects on separate lines.
	bodyStr := w.Body.String()
	lines := strings.Split(strings.TrimRight(bodyStr, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines (done:false chunk + done:true terminal); got %d; body=%q",
			len(lines), bodyStr)
	}

	var chunk ndjsonChatLine
	if err := json.Unmarshal([]byte(lines[0]), &chunk); err != nil {
		t.Fatalf("decode ndjsonChatLine[0]: %v; line=%q", err, lines[0])
	}
	if chunk.Done {
		t.Errorf("lines[0].done: got true, want false (intermediate chunk)")
	}
	if chunk.Message.Content != "decrypted-response" {
		t.Errorf("lines[0].message.content: got %q, want 'decrypted-response'", chunk.Message.Content)
	}
	if chunk.Message.Role != "assistant" {
		t.Errorf("lines[0].message.role: got %q, want assistant", chunk.Message.Role)
	}

	var terminal ollamaChatResponse
	if err := json.Unmarshal([]byte(lines[1]), &terminal); err != nil {
		t.Fatalf("decode ollamaChatResponse[1]: %v; line=%q", err, lines[1])
	}
	if !terminal.Done {
		t.Error("lines[1].done: got false, want true (terminal record)")
	}

	if !eng.collectFromRunCalled {
		t.Error("CollectFromRun was not called — handler took the real NDJSON branch (regression: T-5b re-route guard missing)")
	}
	if !eng.sawStreamTrueAtRun {
		t.Error("rerouteFakeEngine.Run did not observe Stream=true on inbound req — wire-decode broken")
	}
}
