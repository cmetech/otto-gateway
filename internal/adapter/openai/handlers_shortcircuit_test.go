// Phase 08.1 Plan 02 — Pattern E adapter-level streaming short-circuit test
// for the OpenAI surface (D-11). Single test because handleChatCompletions
// is the ONLY OpenAI streaming branch — handleCompletions (handlers.go:209+)
// silently downgrades stream:true to false (RESEARCH.md "OpenAI has only
// ONE streaming branch") so there is no second short-circuit site here.
//
// What this test proves:
//
//   1. When fakeRunHandle.ShortCircuitResponse() returns a non-nil
//      *canonical.ChatResponse, the handler's pre-emitter short-circuit
//      guard at handlers.go:142-156 fires:
//        - status 401 (D-04: hard-coded http.StatusUnauthorized);
//        - Content-Type: application/json (NOT text/event-stream — the
//          load-bearing wire-shape canary against header-leak regressions
//          per Pitfall 3 + threat T-08.1-HEADER-LEAK);
//        - body decodes as the OpenAI-native nested error envelope
//          {"error":{"message":"<non-empty>","type":"authentication_error"}}.
//          The `type` field MUST equal errAuthentication (Pitfall 5 + threat
//          T-08.1-WIRE-TYPE-DRIFT — Pi SDK and openai SDK use this field
//          for AuthenticationError exception classification);
//        - body MUST NOT contain ANY SSE marker — no "data: ",
//          no "data: [DONE]", no "event: ".
//
//   2. The fakeRunHandle.scResp default zero-value (nil) preserves all
//      pre-Plan-02 test behavior — every existing test in this package
//      that constructs a fakeRunHandle without setting scResp still passes
//      runSSEEmitter as before.
//
// Helpers reused from this package's whitebox test files:
//   - mountedAdapter (integration_test.go:169) — mounts adapter under /v1
//     on an httptest.Server.
//   - newFakeAdapter — not used here because the openai fakeEngine
//     (integration_test.go:132) only honors runChunks; this test introduces
//     shortCircuitFakeEngine to inject a custom RunHandle.
//   - fakeStream / fakeRunHandle (sse_golden_test.go:93,108) — RunHandle fake.
//
// Pattern E (08.1-PATTERNS.md): synthetic scResp + custom fakeEngine
// returning a fakeRunHandle whose ShortCircuitResponse() yields a fabricated
// auth-error response — sub-100ms feedback complementary to the binary-boot
// E2E rows added by Plan 02 Task 2.
//
// Goleak gating inherits from testmain_test.go.

package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/goleak"
	"net/http/httptest"

	"otto-gateway/internal/canonical"
)

// newAuthErrorResp builds the synthetic auth-error *canonical.ChatResponse
// that AuthHook.synthesizeAuthError would produce on a bad-bearer
// PreHook short-circuit. Same shape as the Ollama and Anthropic siblings —
// Role=Assistant, Content[0] = text "Invalid or missing API key",
// StopReason=StopError. The shortCircuitMessage helper in handlers.go
// extracts Message.Content[0].Text as the user-facing message.
func newAuthErrorResp() *canonical.ChatResponse {
	return &canonical.ChatResponse{
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Invalid or missing API key"},
			},
		},
		StopReason: canonical.StopError,
	}
}

// shortCircuitFakeEngine is a minimal whitebox Engine fake that returns a
// pre-built RunHandle on Run(). Used by the streaming short-circuit test to
// inject a fakeRunHandle with scResp pre-populated, bypassing the existing
// fakeEngine.Run flow (integration_test.go:144-160) which only honors
// runChunks. Collect is unused in the streaming path.
type shortCircuitFakeEngine struct {
	runHandle RunHandle
}

func (f *shortCircuitFakeEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil
}

func (f *shortCircuitFakeEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	return f.runHandle, nil
}

// newShortCircuitRunHandle constructs a fakeRunHandle whose
// ShortCircuitResponse() yields newAuthErrorResp(). The stream channel is
// closed (empty) — when the short-circuit guard fires, the emitter is never
// reached. FinalResult is StopError to mirror the engine.Run-on-short-
// circuit contract documented at canonical/stop_reason.go:27-33.
func newShortCircuitRunHandle() *fakeRunHandle {
	ch := make(chan canonical.Chunk)
	close(ch)
	return &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: canonical.StopError},
		},
		sessionID: "shortcircuit_no_session",
		scResp:    newAuthErrorResp(),
	}
}

// mountShortCircuitAdapter builds an *Adapter wired to the
// shortCircuitFakeEngine and mounts it under /v1 on an httptest.Server.
// Mirrors integration_test.go's mountedAdapter + newFakeAdapter without
// going through the openai package's fakeEngine (which can't inject a
// custom RunHandle).
func mountShortCircuitAdapter(t *testing.T) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	a := New(Config{
		Logger: logger,
		Engine: &shortCircuitFakeEngine{runHandle: newShortCircuitRunHandle()},
	})
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		a.RegisterRoutes(sub)
	})
	return httptest.NewServer(r)
}

// TestOpenAI_StreamingShortCircuit_ChatCompletions exercises
// handleChatCompletions' Plan 01 short-circuit guard at handlers.go:142-156.
// POSTs /v1/chat/completions with stream:true and a fakeEngine whose Run
// returns a fakeRunHandle with scResp = synthetic auth-error response. The
// handler MUST detect the ShortCircuitResponse() before runSSEEmitter opens
// SSE headers and respond with 401 + application/json + OpenAI nested
// envelope (Type == "authentication_error") + zero SSE byte markers.
func TestOpenAI_StreamingShortCircuit_ChatCompletions(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := mountShortCircuitAdapter(t)
	defer srv.Close()

	body := strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/v1/chat/completions", body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// Wire-invariant 1: status 401 (D-04 hard-coded literal).
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401; body=%s", resp.StatusCode, respBody)
	}
	// Wire-invariant 2: Content-Type: application/json (Pitfall 3 /
	// T-08.1-HEADER-LEAK regression detector — text/event-stream MUST
	// NOT leak from runSSEEmitter).
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json prefix (text/event-stream leak == header-leak regression)", ct)
	}
	// Wire-invariant 3: body decodes as OpenAI nested envelope; Error.Type
	// == "authentication_error" (T-08.1-WIRE-TYPE-DRIFT regression detector
	// — Pi SDK / openai SDK key on this constant for AuthenticationError
	// classification).
	var env struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		t.Fatalf("decode OpenAI envelope: %v; body=%s", err, respBody)
	}
	if env.Error == nil {
		t.Fatalf("envelope: missing top-level \"error\" object; body=%s", respBody)
	}
	if env.Error.Type != errAuthentication {
		t.Errorf("envelope.error.type: got %q, want %q (Pitfall 5 / T-08.1-WIRE-TYPE-DRIFT — Pi SDK keys on this constant); body=%s",
			env.Error.Type, errAuthentication, respBody)
	}
	if env.Error.Message == "" {
		t.Errorf("envelope.error.message: empty (T-08.1-EMPTY-BODY); body=%s", respBody)
	}
	// Negative byte-marker assertions — the strongest regression detectors
	// against T-08.1-HEADER-LEAK on the OpenAI streaming surface.
	if bytes.Contains(respBody, []byte("data: ")) {
		t.Errorf("body contains SSE marker \"data: \" — T-08.1-HEADER-LEAK regression; body=%s", respBody)
	}
	if bytes.Contains(respBody, []byte("data: [DONE]")) {
		t.Errorf("body contains SSE terminator \"data: [DONE]\" — T-08.1-HEADER-LEAK regression; body=%s", respBody)
	}
	if bytes.Contains(respBody, []byte("event: ")) {
		t.Errorf("body contains SSE marker \"event: \" — T-08.1-HEADER-LEAK regression; body=%s", respBody)
	}
}
