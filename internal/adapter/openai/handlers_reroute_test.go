// Package openai — T-5b adapter handler re-route test.
//
// When a Pre hook (e.g., the PII encrypt Pre hook) flips req.Stream=false
// during eng.Run, handleChatCompletions must abandon the SSE branch and
// route the already-running ACP session through eng.CollectFromRun,
// rendering via chatResponseToCompletion (the OpenAI non-streaming
// response shape).
package openai

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
)

// rerouteFakeEngine simulates a PII-encrypt-style Pre hook by flipping
// req.Stream=false inside its Run method, BEFORE returning the
// RunHandle. The handler's post-Run req.Stream check then takes the
// CollectFromRun re-route branch instead of runSSEEmitter.
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

// TestHandleChatCompletions_StreamReroute_OnPreHookStreamDisable asserts
// that a Pre hook flipping req.Stream=false during eng.Run causes the
// /v1/chat/completions handler to call eng.CollectFromRun (NOT
// runSSEEmitter) and render the OpenAI non-streaming chat.completion JSON
// shape. Closes the PII encrypt round-trip for streaming OpenAI clients
// (Pi-SDK).
func TestHandleChatCompletions_StreamReroute_OnPreHookStreamDisable(t *testing.T) {
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
	a := New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
		Engine: eng,
	})

	// Mount the adapter under /v1 (Plan 03-01 D-01 SurfaceMount).
	root := chi.NewRouter()
	root.Route("/v1", func(sub chi.Router) {
		a.RegisterRoutes(sub)
	})

	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	root.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want prefix text/event-stream (synthetic SSE)", ct)
	}

	bodyStr := w.Body.String()
	// Synthetic SSE must emit role-marker frame, content frame with the
	// decrypted text, final finish_reason frame, and [DONE] terminator.
	wantSubstrings := []string{
		`"role":"assistant"`,
		`"content":"decrypted-response"`,
		`"finish_reason":"stop"`,
		"data: [DONE]",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(bodyStr, s) {
			t.Errorf("body missing expected substring %q; body=%q", s, bodyStr)
		}
	}
	// Every data line must carry a chat.completion.chunk object.
	if !strings.Contains(bodyStr, `"object":"chat.completion.chunk"`) {
		t.Errorf("body missing chat.completion.chunk object marker; body=%q", bodyStr)
	}

	if !eng.collectFromRunCalled {
		t.Error("CollectFromRun was not called — handler took the real-SSE branch (regression: T-5b re-route guard missing)")
	}
	if !eng.sawStreamTrueAtRun {
		t.Error("rerouteFakeEngine.Run did not observe Stream=true on inbound req — wire-decode broken")
	}
}
