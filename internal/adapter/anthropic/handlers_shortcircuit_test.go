// Phase 08.1 Plan 02 — Pattern E adapter-level streaming short-circuit test
// for the Anthropic surface (D-11). Single test because handleMessages is
// the ONLY Anthropic streaming branch (adapter.go:137 — only POST /messages
// is registered, and only when wire.Stream is true does the SSE branch run
// at handlers.go:170-211).
//
// What this test proves:
//
//   1. When fakeRunHandle.ShortCircuitResponse() returns a non-nil
//      *canonical.ChatResponse, the handler's pre-emitter short-circuit
//      guard at handlers.go:187-199 fires:
//        - status 401 (D-04: hard-coded http.StatusUnauthorized);
//        - Content-Type: application/json (NOT text/event-stream — the
//          load-bearing wire-shape canary against header-leak regressions
//          per Pitfall 3 + threat T-08.1-HEADER-LEAK);
//        - body decodes as the Anthropic-native double-wrapped envelope
//          {"type":"error","error":{"type":"authentication_error",
//          "message":"<non-empty>"}}. The inner `type` field MUST equal
//          errAuthentication (Pitfall 5 + threat T-08.1-WIRE-TYPE-DRIFT —
//          @anthropic-ai/sdk and loop24-client key on this for
//          AuthenticationError exception classification);
//        - body MUST NOT contain ANY SSE marker — no "event: " (Anthropic
//          SSE opens with `event: message_start\n` per RESEARCH.md, the
//          load-bearing negative marker for this surface), no
//          "event: message_start", no "data: ".
//
//   2. The fakeRunHandle.scResp default zero-value (nil) preserves all
//      pre-Plan-02 test behavior — every existing test in this package
//      that constructs a fakeRunHandle without setting scResp still passes
//      runSSEEmitter as before.
//
// Helpers reused from this package's whitebox test files:
//   - newTestAdapter (handlers_test.go:167) — builds *Adapter.
//   - doPost (handlers_test.go:176) — POSTs JSON with required
//     anthropic-version header to the protected router.
//   - fakeStream / fakeRunHandle (handlers_test.go:143,152) — RunHandle
//     fake (now carries scResp field).
//
// Pattern E (08.1-PATTERNS.md): synthetic scResp + custom fakeEngine
// returning a fakeRunHandle whose ShortCircuitResponse() yields a fabricated
// auth-error response — sub-100ms feedback complementary to the binary-boot
// E2E rows added by Plan 02 Task 2.
//
// Goleak gating inherits from testmain_test.go.

package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"otto-gateway/internal/canonical"
)

// newAuthErrorResp builds the synthetic auth-error *canonical.ChatResponse
// that AuthHook.synthesizeAuthError would produce on a bad-bearer
// PreHook short-circuit. Same shape as the Ollama and OpenAI siblings —
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
// pre-built RunHandle on Run(). The existing fakeEngine in handlers_test.go
// has rich Collect/Run logic (with synthesizeRunHandleFromCollectResp
// helper, etc.); this minimal fake bypasses that flow to inject a custom
// RunHandle whose ShortCircuitResponse() is non-nil. Collect is unused in
// the streaming path.
type shortCircuitFakeEngine struct {
	runHandle RunHandle
}

func (f *shortCircuitFakeEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil
}

func (f *shortCircuitFakeEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	return f.runHandle, nil
}

// RunPostHooks is a no-op for the short-circuit tests. The short-circuit
// guard at handlers.go fires BEFORE runSSEEmitter opens headers, so the
// streaming PostHook call site (Task 2 step 3) is unreachable here.
func (f *shortCircuitFakeEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, _ *canonical.ChatResponse) error {
	return nil
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

// TestAnthropic_StreamingShortCircuit_Messages exercises handleMessages'
// Plan 01 short-circuit guard at handlers.go:187-199. POSTs /messages with
// stream:true (and the required anthropic-version header via doPost) and a
// fakeEngine whose Run returns a fakeRunHandle with scResp = synthetic
// auth-error response. The handler MUST detect ShortCircuitResponse()
// before runSSEEmitter opens SSE headers and respond with 401 +
// application/json + Anthropic double-wrapped envelope (Error.Type ==
// "authentication_error") + zero SSE byte markers.
func TestAnthropic_StreamingShortCircuit_Messages(t *testing.T) {
	eng := &shortCircuitFakeEngine{runHandle: newShortCircuitRunHandle()}
	a := newTestAdapter(eng)
	body := `{"model":"auto","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := doPost(t, a, "/messages", body)
	respBody := w.Body.Bytes()

	// Wire-invariant 1: status 401 (D-04 hard-coded literal).
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401; body=%s", w.Code, respBody)
	}
	// Wire-invariant 2: Content-Type: application/json (Pitfall 3 /
	// T-08.1-HEADER-LEAK regression detector — text/event-stream MUST NOT
	// leak from runSSEEmitter).
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want exactly %q (text/event-stream leak == header-leak regression)",
			ct, "application/json")
	}
	// Wire-invariant 3: body decodes as Anthropic double-wrapped envelope;
	// outer type == "error", inner Error.Type == "authentication_error"
	// (T-08.1-WIRE-TYPE-DRIFT regression detector — @anthropic-ai/sdk keys
	// on this constant).
	var env struct {
		Type  string `json:"type"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		t.Fatalf("decode Anthropic envelope: %v; body=%s", err, respBody)
	}
	if env.Type != "error" {
		t.Errorf("envelope.type: got %q, want \"error\" (outer discriminator); body=%s", env.Type, respBody)
	}
	if env.Error == nil {
		t.Fatalf("envelope: missing nested \"error\" object; body=%s", respBody)
	}
	if env.Error.Type != errAuthentication {
		t.Errorf("envelope.error.type: got %q, want %q (Pitfall 5 / T-08.1-WIRE-TYPE-DRIFT — @anthropic-ai/sdk keys on this constant); body=%s",
			env.Error.Type, errAuthentication, respBody)
	}
	if env.Error.Message == "" {
		t.Errorf("envelope.error.message: empty (T-08.1-EMPTY-BODY); body=%s", respBody)
	}
	// Negative byte-marker assertions — the strongest regression detectors
	// against T-08.1-HEADER-LEAK on the Anthropic streaming surface. Per
	// RESEARCH.md "Wave 0 Gaps", the Anthropic SSE stream opens with
	// `event: message_start\n` so the bare "event: " prefix is the
	// strongest catch ("the load-bearing assertion for that surface").
	if bytes.Contains(respBody, []byte("event: ")) {
		t.Errorf("body contains SSE marker \"event: \" — T-08.1-HEADER-LEAK regression (load-bearing for Anthropic); body=%s", respBody)
	}
	if bytes.Contains(respBody, []byte("event: message_start")) {
		t.Errorf("body contains SSE marker \"event: message_start\" — T-08.1-HEADER-LEAK regression; body=%s", respBody)
	}
	if bytes.Contains(respBody, []byte("data: ")) {
		t.Errorf("body contains SSE marker \"data: \" — T-08.1-HEADER-LEAK regression; body=%s", respBody)
	}
}
