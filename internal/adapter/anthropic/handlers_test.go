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

	"otto-gateway/internal/auth"
	"otto-gateway/internal/canonical"
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
	lastReq  *canonical.ChatRequest
	collectN int
	runN     int
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

func (f *fakeStream) Chunks() <-chan canonical.Chunk          { return f.chunks }
func (f *fakeStream) Result() (*canonical.FinalResult, error) { return f.final, f.err }

// fakeRunHandle wraps fakeStream.
type fakeRunHandle struct {
	stream    Stream
	sessionID string
}

func (f *fakeRunHandle) Stream() Stream    { return f.stream }
func (f *fakeRunHandle) SessionID() string { return f.sessionID }

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
// Streaming end-to-end (B4 part 2 — Plan 02 placeholder REPLACED with real
// SSE handler tests that exercise sse.go runSSEEmitter end-to-end).
//
// Plan 02's forward-compatible placeholder test (which asserted only
// Content-Type: text/event-stream and the first event line) was DELETED
// in Plan 03.1-03 along with the runSSEEmitterStub it covered. The
// minimal handler-visible properties it asserted are now covered by the
// three tests below:
//
//   - TestHandleMessages_StreamingEndToEnd — asserts status 200,
//     Content-Type, and at least one data: frame. Full event-sequence
//     verification is delegated to sse_golden_test.go.
//   - TestHandleMessages_StreamingEngineRunError — engine.Run error BEFORE
//     SSE headers → JSON 500 envelope.
//   - TestHandleMessages_StreamingResultError — engine.Stream().Result()
//     error mid-stream → final SSE frame is event: error.
// ----------------------------------------------------------------------------

// TestHandleMessages_StreamingEndToEnd drives the real handler with a
// fakeEngine whose Run returns a fakeRunHandle producing a single text
// chunk. Asserts response status 200, Content-Type: text/event-stream,
// and at least one `data:` line present in the body. The full event
// sequence is covered by TestSSESequenceGolden_* in sse_golden_test.go;
// this handler-level test stays thin to avoid duplication.
func TestHandleMessages_StreamingEndToEnd(t *testing.T) {
	chunks := make(chan canonical.Chunk, 1)
	chunks <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello!"}}
	close(chunks)
	eng := &fakeEngine{
		runHandle: &fakeRunHandle{
			stream: &fakeStream{
				chunks: chunks,
				final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
			},
			sessionID: "session_e2e",
		},
	}
	a := newTestAdapter(eng)
	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := doPost(t, a, "/messages", body)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}
	if !strings.Contains(w.Body.String(), "\ndata: ") && !strings.HasPrefix(w.Body.String(), "data: ") {
		// Look for at least one data: line (either at start or after a newline).
		t.Errorf("body: no data: frames found; body=%q", w.Body.String())
	}
	// Engine.Run was called; engine.Collect was NOT.
	if eng.runN != 1 {
		t.Errorf("runN: got %d, want 1", eng.runN)
	}
	if eng.collectN != 0 {
		t.Errorf("collectN: got %d, want 0 (stream:true must not call Collect)", eng.collectN)
	}
}

// TestHandleMessages_StreamingEngineRunError covers Engine.Run
// returning an error BEFORE SSE headers were written — the response
// is a normal JSON 500 envelope, NOT an SSE error frame.
func TestHandleMessages_StreamingEngineRunError(t *testing.T) {
	eng := &fakeEngine{runErr: errors.New(`run failed for "hi"`)}
	a := newTestAdapter(eng)
	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := doPost(t, a, "/messages", body)
	assertErrorEnvelope(t, w, http.StatusInternalServerError, errAPI, "internal error")
	// The response is JSON 500, NOT SSE.
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json (no SSE headers on pre-stream error)", ct)
	}
	// T-02-33: no leak.
	if strings.Contains(w.Body.String(), `"hi"`) {
		t.Errorf("error body leaks request content: %q", w.Body.String())
	}
}

// TestHandleMessages_StreamingResultError covers Engine.Run succeeding
// but the fake stream's Result() returns an error — the emitter must
// emit a final `event: error` frame mid-stream and return. Verified by
// reading the response body and asserting the final non-empty SSE
// event frame is `event: error`.
func TestHandleMessages_StreamingResultError(t *testing.T) {
	chunks := make(chan canonical.Chunk)
	close(chunks)
	eng := &fakeEngine{
		runHandle: &fakeRunHandle{
			stream: &fakeStream{
				chunks: chunks,
				final:  nil,
				err:    errors.New(`mid-stream failure for "hi"`),
			},
			sessionID: "session_result_err",
		},
	}
	a := newTestAdapter(eng)
	body := `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := doPost(t, a, "/messages", body)

	// The emitter wrote SSE headers BEFORE the error surfaced, so
	// response is 200 + text/event-stream. The error appears
	// mid-stream.
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (SSE headers were written before Result error)", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}

	// Walk the event lines; the final SSE event MUST be `event: error`.
	bodyStr := w.Body.String()
	var events []string
	scanner := bufio.NewScanner(strings.NewReader(bodyStr))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}
	if len(events) == 0 {
		t.Fatalf("body has no event: lines; body=%q", bodyStr)
	}
	if events[len(events)-1] != "error" {
		t.Errorf("final event: got %q, want %q; events=%v", events[len(events)-1], "error", events)
	}
	for _, ev := range events {
		if ev == "message_stop" || ev == "message_delta" {
			t.Errorf("found %q event AFTER error frame — SDK treats error as terminal", ev)
		}
	}
	// T-02-33: the SSE error frame must NOT echo request content or
	// the underlying err string.
	if strings.Contains(bodyStr, `"hi"`) {
		t.Errorf("SSE body leaks request content: %q", bodyStr)
	}
	if strings.Contains(bodyStr, "mid-stream failure") {
		t.Errorf("SSE body leaks raw err string: %q", bodyStr)
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
