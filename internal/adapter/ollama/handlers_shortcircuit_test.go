// Phase 08.1 Plan 02 — Pattern E adapter-level streaming short-circuit tests
// for the Ollama surface (D-11 + Pitfall 6). Two independent tests because
// handleChat and handleGenerate are independently registered chi routes
// (adapter.go:179-180) and each carries the Plan 01 short-circuit guard at a
// different line (handlers.go:193-207 for chat, handlers.go:331-343 for
// generate). A future contributor fixing one but missing the other fails
// only the corresponding test — Pitfall 6 in the phase RESEARCH.md is
// exactly this scenario.
//
// What these tests prove:
//
//   1. When fakeRunHandle.ShortCircuitResponse() returns a non-nil
//      *canonical.ChatResponse (mirroring AuthHook's synthesizeAuthError
//      output), the handler's pre-emitter short-circuit guard fires:
//        - status 401 (D-04: hard-coded http.StatusUnauthorized);
//        - Content-Type: application/json (NOT application/x-ndjson —
//          the load-bearing wire-shape canary against header-leak
//          regressions per Pitfall 3 + threat T-08.1-HEADER-LEAK);
//        - body decodes as the Ollama-native flat error envelope
//          {"error":"<non-empty>"};
//        - body MUST NOT contain ANY NDJSON/SSE marker — no "data: ",
//          no "event: ", no `"done":true`. These negative byte-marker
//          assertions are the strongest catch for any future contributor
//          who inadvertently moves the guard after a header write.
//
//   2. The fakeRunHandle.scResp default zero-value (nil) preserves all
//      pre-Plan-02 test behavior. Every existing test in this package
//      that constructs a fakeRunHandle without setting scResp continues
//      to fall through to runNDJSONEmitter as before — verified by the
//      green test suite at `go test -race ./internal/adapter/ollama/...`.
//
// Helpers reused from this package's whitebox test files:
//   - newTestAdapter (handlers_test.go:65) — builds *Adapter.
//   - doPost (handlers_test.go:75) — POSTs JSON to the protected router.
//   - fakeStream / fakeRunHandle (ndjson_test.go:26,38) — RunHandle fake.
//
// Pattern E (08.1-PATTERNS.md): synthetic scResp + custom fakeEngine
// returning a fakeRunHandle whose ShortCircuitResponse() yields a fabricated
// auth-error response — sub-100ms feedback complementary to the binary-boot
// E2E rows added by Plan 02 Task 2.
//
// Goleak gating inherits from testmain_test.go (per RESEARCH.md
// §"Claude's Discretion / Goleak gating").

package ollama

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
// PreHook short-circuit. Shape mirrors the contract documented at
// canonical/stop_reason.go:27-33 and Plan 01's writeError call sites:
// Role=Assistant, Content[0] = text "Invalid or missing API key",
// StopReason=StopError. The shortCircuitMessage helper in handlers.go:24-37
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
// pre-built RunHandle on Run(). Used by the streaming short-circuit tests to
// inject a fakeRunHandle with scResp pre-populated, bypassing the existing
// fakeEngine.Run flow (handlers_test.go:49-55) which only honors runChunks.
//
// Collect is unused in the streaming path and returns nil/nil.
type shortCircuitFakeEngine struct {
	runHandle RunHandle
}

func (f *shortCircuitFakeEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil
}

func (f *shortCircuitFakeEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	return f.runHandle, nil
}

// RunPostHooks is a no-op for the short-circuit tests. The short-
// circuit guard fires BEFORE runNDJSONEmitter opens headers, so the
// streaming PostHook call site (Task 3 step 2) is unreachable here.
func (f *shortCircuitFakeEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, _ *canonical.ChatResponse) error {
	return nil
}

// newShortCircuitRunHandle constructs a fakeRunHandle whose
// ShortCircuitResponse() yields newAuthErrorResp(). The stream channel is
// closed (empty) — when the short-circuit guard fires, the emitter is never
// reached, so the channel contents are irrelevant. FinalResult is StopError
// to mirror the engine.Run-on-short-circuit contract documented at
// canonical/stop_reason.go:27-33 (though the handler never inspects it on
// this path either; ShortCircuitResponse is the only discriminator).
func newShortCircuitRunHandle() *fakeRunHandle {
	ch := make(chan canonical.Chunk)
	close(ch)
	return &fakeRunHandle{
		stream: &fakeStream{
			ch:     ch,
			result: &canonical.FinalResult{StopReason: canonical.StopError},
		},
		sessionID: "shortcircuit_no_session",
		scResp:    newAuthErrorResp(),
	}
}

// assertOllamaShortCircuitInvariants asserts the five Plan 02 D-10 wire
// invariants for an Ollama short-circuit response:
//   1. status 401;
//   2. Content-Type starts with "application/json" (NOT
//      application/x-ndjson — the load-bearing header-leak canary);
//   3. body decodes as Ollama-native flat envelope {"error":"<non-empty>"};
//   4. body MUST NOT contain "data: " (SSE leak);
//   5. body MUST NOT contain "event: " (SSE leak);
//   6. body MUST NOT contain `"done":true` (NDJSON leak).
func assertOllamaShortCircuitInvariants(t *testing.T, body []byte, status int, ct string) {
	t.Helper()
	if status != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401; body=%s", status, body)
	}
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want exactly %q (Pitfall 3 / T-08.1-HEADER-LEAK regression detector — NDJSON header MUST NOT leak)",
			ct, "application/json")
	}
	var env struct {
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode Ollama envelope: %v; body=%s", err, body)
	}
	if env.Error == nil {
		t.Errorf("envelope: missing top-level \"error\" string field; body=%s", body)
	} else if *env.Error == "" {
		t.Errorf("envelope: \"error\" field is empty (T-08.1-EMPTY-BODY); body=%s", body)
	}
	// Negative byte-marker assertions — the strongest regression detectors
	// per RESEARCH.md "Wave 0 Gaps" sampling-rate analysis: each surface's
	// streaming format has a unique opening marker that MUST NOT appear on
	// the short-circuit path. All three are checked simultaneously because
	// a header-leak regression on this site could leak ANY of the three.
	if bytes.Contains(body, []byte("data: ")) {
		t.Errorf("body contains SSE marker \"data: \" — T-08.1-HEADER-LEAK regression; body=%s", body)
	}
	if bytes.Contains(body, []byte("event: ")) {
		t.Errorf("body contains SSE marker \"event: \" — T-08.1-HEADER-LEAK regression; body=%s", body)
	}
	if bytes.Contains(body, []byte(`"done":true`)) {
		t.Errorf("body contains NDJSON marker `\"done\":true` — T-08.1-HEADER-LEAK regression; body=%s", body)
	}
}

// TestOllama_StreamingShortCircuit_Chat exercises handleChat's Plan 01
// short-circuit guard at handlers.go:193-207. POSTs /chat with stream:true
// and a fakeEngine whose Run returns a fakeRunHandle with scResp = the
// synthetic auth-error response. The handler MUST detect the
// ShortCircuitResponse() before runNDJSONEmitter opens NDJSON headers and
// respond with 401 + application/json + Ollama flat envelope + zero
// SSE/NDJSON byte markers.
func TestOllama_StreamingShortCircuit_Chat(t *testing.T) {
	eng := &shortCircuitFakeEngine{runHandle: newShortCircuitRunHandle()}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := doPost(t, a, "/chat", body)

	assertOllamaShortCircuitInvariants(t, w.Body.Bytes(), w.Code, w.Header().Get("Content-Type"))
}

// TestOllama_StreamingShortCircuit_Generate exercises handleGenerate's Plan
// 01 short-circuit guard at handlers.go:331-343. POSTs /generate with
// stream:true and a fakeEngine whose Run returns the same fakeRunHandle as
// above. Per Pitfall 6, handleGenerate is an INDEPENDENT chi route from
// handleChat with its own copy of the short-circuit guard at a different
// line — both routes must carry the guard, and both must have their own
// test. A future contributor fixing only handleChat fails this test.
func TestOllama_StreamingShortCircuit_Generate(t *testing.T) {
	eng := &shortCircuitFakeEngine{runHandle: newShortCircuitRunHandle()}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","prompt":"hi","stream":true}`
	w := doPost(t, a, "/generate", body)

	assertOllamaShortCircuitInvariants(t, w.Body.Bytes(), w.Code, w.Header().Get("Content-Type"))
}
