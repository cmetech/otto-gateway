package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"loop24-gateway/internal/auth"
	"loop24-gateway/internal/canonical"
)

// ----------------------------------------------------------------------------
// Test fakes — fakeEngine + fakeRunHandle + fakeStream
// ----------------------------------------------------------------------------

// fakeEngine is the whitebox test double for the consumer-defined
// Engine interface declared in adapter.go. Whitebox is locked: the
// fakeEngine satisfies the package-private interface without
// redeclaring it (mirrors the Phase 2 Ollama pattern).
type fakeEngine struct {
	collectResp *canonical.ChatResponse
	collectErr  error
	runHandle   RunHandle
	runErr      error
	// lastReq captures the canonical request the handler synthesized
	// so tests can assert wire→canonical translation passed through.
	lastReq    *canonical.ChatRequest
	collectN   int
	runN       int
}

func (f *fakeEngine) Collect(_ context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	f.collectN++
	f.lastReq = req
	if f.collectErr != nil {
		return nil, f.collectErr
	}
	return f.collectResp, nil
}

func (f *fakeEngine) Run(_ context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	f.runN++
	f.lastReq = req
	if f.runErr != nil {
		return nil, f.runErr
	}
	return f.runHandle, nil
}

// fakeStream is a minimal Stream — chunk channel closes immediately,
// Result returns canonical end-of-turn FinalResult.
type fakeStream struct {
	chunks <-chan canonical.Chunk
	final  *canonical.FinalResult
	err    error
}

func (f *fakeStream) Chunks() <-chan canonical.Chunk           { return f.chunks }
func (f *fakeStream) Result() (*canonical.FinalResult, error) { return f.final, f.err }

// fakeRunHandle wraps fakeStream.
type fakeRunHandle struct {
	stream    Stream
	sessionID string
}

func (f *fakeRunHandle) Stream() Stream    { return f.stream }
func (f *fakeRunHandle) SessionID() string { return f.sessionID }

// makeClosedChunkChan returns an already-closed empty chunk channel
// suitable for the streaming-stub test (no chunks to deliver).
func makeClosedChunkChan() <-chan canonical.Chunk {
	ch := make(chan canonical.Chunk)
	close(ch)
	return ch
}

// ----------------------------------------------------------------------------
// HTTP helpers
// ----------------------------------------------------------------------------

func newTestAdapter(eng Engine) *Adapter {
	return New(Config{Engine: eng})
}

// doPost posts a JSON body to the protected router at the given path
// (always "/messages" today — left parameterized for callers that may
// hit other adapter routes in future plans).
//
//nolint:unparam // path is parameterized for future routes
func doPost(t *testing.T, a *Adapter, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("anthropic-version", "2023-06-01")
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)
	return w
}

func doPostNoVersion(t *testing.T, a *Adapter, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)
	return w
}

func doPostWithHeader(t *testing.T, a *Adapter, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)
	return w
}

// decodeEnvelope decodes the response body into an errorEnvelope and
// asserts the expected type+message fields.
func assertErrorEnvelope(t *testing.T, w *httptest.ResponseRecorder, wantStatus int, wantType, wantMessageContains string) {
	t.Helper()
	if w.Code != wantStatus {
		t.Errorf("status: got %d, want %d; body=%s", w.Code, wantStatus, w.Body.String())
	}
	var env errorEnvelope
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, w.Body.String())
	}
	if env.Type != "error" || env.Error.Type != wantType {
		t.Errorf("envelope type: got {%q,%q}, want {error,%q}", env.Type, env.Error.Type, wantType)
	}
	if wantMessageContains != "" && !strings.Contains(env.Error.Message, wantMessageContains) {
		t.Errorf("envelope message: got %q, want contains %q", env.Error.Message, wantMessageContains)
	}
}

// ----------------------------------------------------------------------------
// Nil-engine 503
// ----------------------------------------------------------------------------

func TestHandleMessages_EngineNil_503(t *testing.T) {
	a := newTestAdapter(nil)
	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`
	w := doPost(t, a, "/messages", body)
	assertErrorEnvelope(t, w, http.StatusServiceUnavailable, errAPI, "kiro-cli not configured")
}

// ----------------------------------------------------------------------------
// D-07: anthropic-version header validation
// ----------------------------------------------------------------------------

func TestHandleMessages_MissingVersion_400(t *testing.T) {
	eng := &fakeEngine{}
	a := newTestAdapter(eng)
	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`
	w := doPostNoVersion(t, a, "/messages", body)
	assertErrorEnvelope(t, w, http.StatusBadRequest, errInvalidRequest, "anthropic-version header is required")
	if eng.lastReq != nil {
		t.Error("engine.Collect was called despite missing header — D-07 gate broken")
	}
}

func TestHandleMessages_VersionAnyValueAccepted(t *testing.T) {
	eng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng)
	// D-07: any non-empty value accepted.
	w := doPostWithHeader(t, a, "/messages",
		`{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"anthropic-version": "2025-01-01"})
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (any version accepted); body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// D-10: anthropic-beta accept-and-ignore
// ----------------------------------------------------------------------------

func TestHandleMessages_AnthropicBetaAcceptedAndIgnored(t *testing.T) {
	eng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng)
	w := doPostWithHeader(t, a, "/messages",
		`{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"anthropic-beta": "fine-grained-tool-streaming-2025-05-14"})
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (anthropic-beta must NOT 400); body=%s", w.Code, w.Body.String())
	}
	// The header value must NOT reach canonical.ChatRequest. There's
	// no direct field for it; verify Metadata stays nil.
	if eng.lastReq != nil && eng.lastReq.Metadata != nil {
		t.Errorf("canonical.Metadata: got %+v, want nil (anthropic-beta must NOT persist)", eng.lastReq.Metadata)
	}
}

// ----------------------------------------------------------------------------
// Body decode + validation
// ----------------------------------------------------------------------------

func TestHandleMessages_BadJSON_400(t *testing.T) {
	a := newTestAdapter(&fakeEngine{})
	w := doPost(t, a, "/messages", `{not-valid-json`)
	assertErrorEnvelope(t, w, http.StatusBadRequest, errInvalidRequest, "invalid JSON")
	// T-02-33: body does NOT echo the literal request content.
	if strings.Contains(w.Body.String(), "not-valid-json") {
		t.Errorf("error body leaks request content: %q", w.Body.String())
	}
}

func TestHandleMessages_BodyTooLarge_413(t *testing.T) {
	a := newTestAdapter(&fakeEngine{})
	// Build a 5 MiB request body — exceeds 4 MiB cap.
	pad := strings.Repeat("x", 5<<20)
	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"` + pad + `"}]}`
	w := doPost(t, a, "/messages", body)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413", w.Code)
	}
	// T-02-33: response does NOT echo the padding content.
	if strings.Contains(w.Body.String(), pad[:100]) {
		t.Errorf("error body leaks padding content (first 100 chars)")
	}
}

func TestHandleMessages_MissingModel_400(t *testing.T) {
	a := newTestAdapter(&fakeEngine{})
	body := `{"max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`
	w := doPost(t, a, "/messages", body)
	assertErrorEnvelope(t, w, http.StatusBadRequest, errInvalidRequest, "`model` is required")
}

func TestHandleMessages_MissingMaxTokens_400(t *testing.T) {
	a := newTestAdapter(&fakeEngine{})
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	w := doPost(t, a, "/messages", body)
	assertErrorEnvelope(t, w, http.StatusBadRequest, errInvalidRequest, "`max_tokens` is required and must be > 0")
}

func TestHandleMessages_EmptyMessages_400(t *testing.T) {
	a := newTestAdapter(&fakeEngine{})
	body := `{"model":"auto","max_tokens":256,"messages":[]}`
	w := doPost(t, a, "/messages", body)
	assertErrorEnvelope(t, w, http.StatusBadRequest, errInvalidRequest, "`messages` is required and must be a non-empty array")
}

// ----------------------------------------------------------------------------
// Non-streaming happy path (ANTH-01)
// ----------------------------------------------------------------------------

func TestHandleMessages_Happy_NonStreaming(t *testing.T) {
	eng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello!"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng)
	body := `{"model":"claude-sonnet-4","max_tokens":256,"messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := doPost(t, a, "/messages", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	var resp anthropicMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Type != "message" || resp.Role != "assistant" {
		t.Errorf("type/role: got %q/%q, want message/assistant", resp.Type, resp.Role)
	}
	if resp.Model != "claude-sonnet-4" {
		t.Errorf("model: got %q, want %q (echo requestedModel)", resp.Model, "claude-sonnet-4")
	}
	if !strings.HasPrefix(resp.ID, "msg_01") {
		t.Errorf("id: got %q, want prefix msg_01", resp.ID)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "Hello!" {
		t.Errorf("content: got %+v, want one text block 'Hello!'", resp.Content)
	}
	if resp.StopReason == nil || *resp.StopReason != "end_turn" {
		t.Errorf("stop_reason: got %v, want pointer to 'end_turn'", resp.StopReason)
	}
	if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
		t.Errorf("usage: got %+v, want zeros (D-12)", resp.Usage)
	}
	if eng.collectN != 1 {
		t.Errorf("collectN: got %d, want 1", eng.collectN)
	}
}

// TestHandleMessages_EngineError_500 — T-02-33: engine error is not
// echoed in the response body even when err string contains request
// content.
func TestHandleMessages_EngineError_500(t *testing.T) {
	eng := &fakeEngine{collectErr: errors.New(`upstream failure for prompt "hi"`)}
	a := newTestAdapter(eng)
	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`
	w := doPost(t, a, "/messages", body)
	assertErrorEnvelope(t, w, http.StatusInternalServerError, errAPI, "internal error")
	// T-02-33: error message must NOT contain request content or err string.
	if strings.Contains(w.Body.String(), `"hi"`) {
		t.Errorf("error body leaks request content: %q", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "upstream failure") {
		t.Errorf("error body leaks raw err string: %q", w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Streaming branch placeholder (B4 part 1 — forward-compatible asserts only)
// ----------------------------------------------------------------------------

// TestHandleMessages_StreamingBranchPlaceholder is the load-bearing
// forward-compatibility test for the Plan 03.1-02 SSE stub. ASSERTS
// ONLY:
//
//   - Content-Type: text/event-stream
//   - First emitted SSE event line equals exactly "event: message_start"
//
// Plan 03.1-03 deletes runSSEEmitterStub and replaces with the real
// emitter. Plan 03's real emitter ALSO emits message_start first, so
// this test continues to pass byte-for-byte. The test name signals
// the swap site — Plan 03 may delete this test if it asserts
// additional Plan-03-specific behavior in its own test file.
func TestHandleMessages_StreamingBranchPlaceholder(t *testing.T) {
	eng := &fakeEngine{
		runHandle: &fakeRunHandle{
			stream: &fakeStream{
				chunks: makeClosedChunkChan(),
				final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
			},
			sessionID: "session_stub",
		},
	}
	a := newTestAdapter(eng)
	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := doPost(t, a, "/messages", body)

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}

	scanner := bufio.NewScanner(w.Body)
	if !scanner.Scan() {
		t.Fatal("body: no lines emitted; want first line 'event: message_start'")
	}
	first := scanner.Text()
	if first != "event: message_start" {
		t.Errorf("first SSE line: got %q, want %q", first, "event: message_start")
	}

	// Engine.Run was called; engine.Collect was NOT.
	if eng.runN != 1 {
		t.Errorf("runN: got %d, want 1", eng.runN)
	}
	if eng.collectN != 0 {
		t.Errorf("collectN: got %d, want 0 (stream:true must not call Collect)", eng.collectN)
	}
}

// TestHandleMessages_StreamingEngineRunError_500 covers Engine.Run
// returning an error BEFORE SSE headers were written — the response
// is a normal JSON 500 envelope, NOT an SSE error frame.
func TestHandleMessages_StreamingEngineRunError_500(t *testing.T) {
	eng := &fakeEngine{runErr: errors.New(`run failed for "hi"`)}
	a := newTestAdapter(eng)
	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := doPost(t, a, "/messages", body)
	assertErrorEnvelope(t, w, http.StatusInternalServerError, errAPI, "internal error")
	// T-02-33: no leak.
	if strings.Contains(w.Body.String(), `"hi"`) {
		t.Errorf("error body leaks request content: %q", w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Auth 401 envelope (W6 — documented exception)
// ----------------------------------------------------------------------------

// TestAuth_401_OnMessages — W6 + RESEARCH.md §Pattern 3 option 2.
// Wraps the adapter's protected router with auth.Bearer (with at
// least one token configured) and POSTs without credentials. Asserts
// the 401 response uses the OLLAMA envelope ({"error":"Invalid or
// missing API key"}), NOT the Anthropic envelope. This is the
// documented exception — Phase 8 lifts auth into a surface-aware
// hook chain that will render Anthropic-shape 401 for /v1/*.
func TestAuth_401_OnMessages(t *testing.T) {
	a := newTestAdapter(&fakeEngine{})

	// Construct an outer router with auth.Bearer middleware in front
	// of the adapter's protected router — mirrors the Phase 3.1 D-17
	// server mount.
	outer := chi.NewRouter()
	outer.Route("/v1", func(r chi.Router) {
		r.Use(auth.Bearer(auth.Config{Tokens: []string{"good"}}))
		r.Mount("/", a.ProtectedRouter())
	})

	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("anthropic-version", "2023-06-01")
	// NO Authorization or x-api-key headers.
	w := httptest.NewRecorder()
	outer.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401; body=%s", w.Code, w.Body.String())
	}
	// Documented exception: Ollama envelope, not Anthropic envelope.
	// Phase 8 hook chain will lift this into a surface-aware
	// auth-error renderer per RESEARCH.md §Pattern 3 option 2.
	if !strings.Contains(w.Body.String(), `Invalid or missing API key`) {
		t.Errorf("body: got %q, want contains 'Invalid or missing API key' (Ollama envelope)", w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// wire→canonical translation passthrough (ANTH-05 + ANTH-07 inbound)
// ----------------------------------------------------------------------------

// TestHandleMessages_SystemAndThinking_ReachCanonical exercises that
// the full request→canonical translation path runs from the handler
// (covered in isolation by wire_test.go; this test proves the
// integration).
func TestHandleMessages_SystemAndThinking_ReachCanonical(t *testing.T) {
	eng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng)
	body := `{
		"model": "auto",
		"max_tokens": 256,
		"system": "be helpful",
		"messages": [
			{"role":"assistant","content":[{"type":"thinking","thinking":"reasoning..."}]},
			{"role":"user","content":"hi"}
		]
	}`
	w := doPost(t, a, "/messages", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if eng.lastReq.System != "be helpful" {
		t.Errorf("canonical.System: got %q, want %q", eng.lastReq.System, "be helpful")
	}
	// Two messages: the thinking-only assistant message and the user message.
	if len(eng.lastReq.Messages) != 2 {
		t.Fatalf("canonical.Messages: got %d, want 2; %+v", len(eng.lastReq.Messages), eng.lastReq.Messages)
	}
}
