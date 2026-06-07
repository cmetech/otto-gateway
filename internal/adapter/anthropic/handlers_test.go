package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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

	// Quick 260530-df2 — RunPostHooks observation. postErr is returned
	// to the caller (the handler / CollectAnthropicChat) so tests can
	// exercise the WARN-and-swallow streaming path AND the
	// propagate-as-error non-streaming path. lastPostResp captures the
	// resp pointer the hook chain was called with so tests can assert
	// aggregator content shape. postN counts invocations for double-
	// fire guards.
	postErr      error
	lastPostResp *canonical.ChatResponse
	postN        int
}

func (f *fakeEngine) Collect(_ context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	f.collectN++
	f.lastReq = req
	if f.collectErr != nil {
		return nil, f.collectErr
	}
	return f.collectResp, nil
}

// RunPostHooks records the invocation and returns the scripted error
// (default nil). Quick 260530-df2: streaming adapters (and the
// non-streaming Anthropic CollectAnthropicChat path) call this method
// after aggregation. Tests assert postN == 1 for the per-surface
// double-fire guards and capture lastPostResp to assert aggregator
// content shape.
func (f *fakeEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	f.postN++
	f.lastPostResp = resp
	return f.postErr
}

// CollectFromRun satisfies the T-5b seam on the Engine interface. The
// fake aggregates by returning collectResp (the same response the
// synthesized RunHandle's chunk stream would aggregate to via
// CollectAnthropicChat / engine.CollectFromRun in production). Tests that
// drive the T-5b re-route path (stream flipped to false by a Pre hook
// post-Run) can set collectResp and assert the handler renders the
// non-streaming JSON shape.
func (f *fakeEngine) CollectFromRun(_ context.Context, _ RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
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
	if f.runHandle != nil {
		return f.runHandle, nil
	}
	// Phase 6 Plan 04: the non-streaming handler now routes through
	// CollectAnthropicChat → eng.Run instead of eng.Collect. Tests
	// that only set collectResp (the legacy convention) keep working
	// by synthesizing a single-shot RunHandle from collectResp:
	// replay each ContentPart as a chunk (text → ChunkKindText,
	// thinking → ChunkKindThought, tool_use → ChunkKindToolCall),
	// then close so CollectAnthropicChat's aggregator reassembles
	// the same ChatResponse.
	if f.collectResp != nil {
		return synthesizeRunHandleFromCollectResp(f.collectResp, f.collectErr), nil
	}
	if f.collectErr != nil {
		// No canned response but an error: surface via Stream.Result().
		ch := make(chan canonical.Chunk)
		close(ch)
		return &fakeRunHandle{stream: &fakeStream{chunks: ch, err: f.collectErr}}, nil
	}
	return nil, nil
}

// synthesizeRunHandleFromCollectResp converts a *canonical.ChatResponse
// (the legacy fakeEngine.collectResp shape) into a single-shot
// RunHandle so the Phase 6 Plan 04 CollectAnthropicChat aggregator
// reproduces the same response from the chunk stream. Preserves
// per-test scripted error: passing a non-nil err propagates via
// Stream.Result() so the handler's error branch still fires.
func synthesizeRunHandleFromCollectResp(resp *canonical.ChatResponse, scriptedErr error) *fakeRunHandle {
	var chunks []canonical.Chunk
	for _, part := range resp.Message.Content {
		switch part.Kind {
		case canonical.ContentKindText:
			chunks = append(chunks, canonical.Chunk{
				Kind: canonical.ChunkKindText,
				Text: &canonical.TextChunk{Content: part.Text},
			})
		case canonical.ContentKindThinking:
			chunks = append(chunks, canonical.Chunk{
				Kind:    canonical.ChunkKindThought,
				Thought: &canonical.ThoughtChunk{Content: part.Text},
			})
		case canonical.ContentKindToolUse:
			if part.ToolUse != nil {
				chunks = append(chunks, canonical.Chunk{
					Kind: canonical.ChunkKindToolCall,
					ToolCall: &canonical.ToolCallChunk{
						ID:   part.ToolUse.ID,
						Name: part.ToolUse.Name,
						Args: part.ToolUse.Input,
					},
				})
			}
		}
	}
	// WR-04 (Phase 6 review): do NOT synthesize ChunkKindToolCall from
	// resp.Message.ToolCalls. The Phase 6 contract is that ToolCalls
	// gets populated by the CollectAnthropicChat aggregator (from
	// kiro-native ChunkKindToolCall chunks), so synthesizing chunks
	// from ToolCalls AND from ContentKindToolUse content parts would
	// double-count any future fixture that sets both. Tests that need
	// pre-populated ToolCalls must construct their own RunHandle
	// directly (e.g., set runHandle on fakeEngine) rather than route
	// through this synthesizer.
	if len(resp.Message.ToolCalls) > 0 {
		// Surface this as a test-author error eagerly. The synthesizer
		// contract is one-way: Content -> chunks. Pre-populated
		// ToolCalls on the synthesizer input is a fixture mistake.
		panic("synthesizeRunHandleFromCollectResp: resp.Message.ToolCalls must be empty; populate via ContentKindToolUse parts and let CollectAnthropicChat re-derive ToolCalls (WR-04)")
	}
	ch := make(chan canonical.Chunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: resp.StopReason},
			err:    scriptedErr,
		},
		sessionID: "synthesized",
	}
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
//
// scResp is the synthetic ShortCircuitResponse return value used by Plan 02
// (Phase 08.1 INTEG-01) handler short-circuit tests. Default zero-value nil
// preserves every pre-08.1 test's behavior: ShortCircuitResponse() returns
// nil → handler falls through to runSSEEmitter as before. Tests that need to
// exercise the streaming short-circuit guard at handlers.go
// (handleMessages ~187-199) set scResp to a non-nil *canonical.ChatResponse
// before calling the handler.
type fakeRunHandle struct {
	stream    Stream
	sessionID string
	scResp    *canonical.ChatResponse
}

func (f *fakeRunHandle) Stream() Stream                                { return f.stream }
func (f *fakeRunHandle) SessionID() string                             { return f.sessionID }
func (f *fakeRunHandle) StopWatchdog() func() bool                     { return nil }
func (f *fakeRunHandle) ShortCircuitResponse() *canonical.ChatResponse { return f.scResp }

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
	// Phase 6 Plan 04 Task 2: the non-streaming handler now routes
	// through CollectAnthropicChat -> eng.Run (NOT eng.Collect). The
	// invocation count moved from collectN to runN. Both assertions
	// belt-and-suspenders: collectN must stay 0 to prove the swap is
	// in place; runN must be 1 to prove the new path is exercised.
	if eng.collectN != 0 {
		t.Errorf("collectN: got %d, want 0 (handler routes through eng.Run, not eng.Collect)", eng.collectN)
	}
	if eng.runN != 1 {
		t.Errorf("runN: got %d, want 1 (CollectAnthropicChat invokes eng.Run)", eng.runN)
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

// ----------------------------------------------------------------------------
// Phase 6 Plan 04 Task 4 — NO-coerce asymmetry locked (D-01 / D-17 scenario 5)
//
// Anthropic does NOT call engine.CoerceToolCall. Running coerce here
// would silently rewrite messages.stream() consumers' assistant text
// into synthesized tool_use blocks — a wire-shape forgery that
// surprises loop24-client and any other Anthropic-native client that
// emits JSON-shaped assistant text legitimately.
//
// Two regression guards:
//   - TestAnthropic_NoCoerce_Behavioral (REVIEW LOW #9 promoted to
//     required, the primary guard) — drives a fake engine that
//     produces bare JSON text + the request carries a tools[] catalog
//     with a matching tool. A future bug that adds CoerceToolCall to
//     handlers.go would coerce the text into a tool_use block; this
//     test asserts that does NOT happen — text preserved verbatim, no
//     tool_use synthesized, stop_reason stays end_turn.
//   - TestAnthropic_DoesNotCallCoerceToolCall (belt-and-suspenders,
//     static-source assertion) — reads handlers.go and asserts the
//     `engine.CoerceToolCall` symbol does NOT appear. Catches a refactor
//     at compile-time before the behavioral test even runs.
// ----------------------------------------------------------------------------

// TestAnthropic_NoCoerce_Behavioral is the primary REVIEW LOW #9
// regression guard. It drives a fake engine that emits a single text
// chunk whose payload is bare JSON (the LangChain-style "JSON-as-text"
// shape that engine.CoerceToolCall is built to handle on
// Ollama/OpenAI). With a matching tools[] catalog, an Ollama or
// OpenAI handler would coerce the text into a synthetic tool_use
// block and set stop_reason:"tool_use". The Anthropic handler MUST
// NOT — it preserves the text verbatim, emits no tool_use, and the
// stop_reason stays end_turn. This is locked by D-01 + D-17
// scenario 5.
func TestAnthropic_NoCoerce_Behavioral(t *testing.T) {
	bareJSONText := `{"location":"NYC"}`
	eng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: bareJSONText},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng)
	body := `{
		"model": "auto",
		"max_tokens": 256,
		"messages": [{"role":"user","content":"what's the weather in NYC?"}],
		"tools": [{
			"name": "get_weather",
			"description": "look up weather",
			"input_schema": {
				"type": "object",
				"properties": {
					"location": {"type": "string"}
				},
				"required": ["location"]
			}
		}]
	}`
	w := doPost(t, a, "/messages", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp anthropicMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}

	// (a) The assistant text MUST be preserved verbatim. Coerce would
	// have cleared the text and emitted a synthetic tool_use block;
	// the Anthropic surface MUST NOT do that.
	if len(resp.Content) == 0 {
		t.Fatal("D-01 violation: response has no content blocks; expected preserved bare-JSON text")
	}
	sawText := false
	for _, b := range resp.Content {
		if b.Type == "text" && b.Text == bareJSONText {
			sawText = true
		}
	}
	if !sawText {
		t.Errorf("D-01 violation: bare-JSON assistant text not preserved verbatim; content=%+v", resp.Content)
	}

	// (b) NO tool_use content block synthesized. This is what would
	// happen if engine.CoerceToolCall ran here.
	for _, b := range resp.Content {
		if b.Type == "tool_use" {
			t.Errorf("D-01 violation: synthesized tool_use block on Anthropic surface; content=%+v (engine.CoerceToolCall was called?)", resp.Content)
		}
	}

	// (c) stop_reason stays end_turn (NOT "tool_use"). The Task 3
	// render override fires only when a ContentKindToolUse part
	// exists — which it MUST NOT here.
	if resp.StopReason == nil {
		t.Fatal("stop_reason: nil; want pointer to \"end_turn\"")
	}
	if *resp.StopReason != "end_turn" {
		t.Errorf("D-01 violation: stop_reason got %q, want \"end_turn\" (no tool_use synthesized -> no override)", *resp.StopReason)
	}
}

// TestAnthropic_DoesNotCallCoerceToolCall is the belt-and-suspenders
// static-source guard. Cheap to run, fail-fast on the symbol —
// catches refactors that add CoerceToolCall to handlers.go before the
// behavioral test even runs. Primary regression guard is
// TestAnthropic_NoCoerce_Behavioral above. Per CONTEXT D-01 / D-17
// scenario 5, the Anthropic surface intentionally does NOT call
// engine.CoerceToolCall.
func TestAnthropic_DoesNotCallCoerceToolCall(t *testing.T) {
	// G304 silenced: handlers.go is a known sibling source file,
	// never user input.
	src, err := os.ReadFile("handlers.go") //nolint:gosec // sibling source file; constant path
	if err != nil {
		t.Fatalf("ReadFile handlers.go: %v", err)
	}
	// Strip Go line + block comments so the PHASE 6 INVARIANT
	// documentation block (which mentions engine.CoerceToolCall to
	// EXPLAIN the absence) does not trip a false positive. We want
	// to assert that the SYMBOL is not referenced in actual code.
	stripped := stripGoComments(string(src))
	if strings.Contains(stripped, "engine.CoerceToolCall") {
		t.Errorf("D-01 violation: handlers.go references engine.CoerceToolCall in non-comment code. "+
			"Per CONTEXT D-01 + D-17 scenario 5, the Anthropic surface intentionally "+
			"does NOT call CoerceToolCall — running coerce would silently rewrite "+
			"messages.stream() consumers' assistant text into synthesized tool_use "+
			"blocks (wire-shape forgery). Remove the reference and rely on the "+
			"per-surface contract: kiro-native ChunkKindToolCall is aggregated by "+
			"CollectAnthropicChat (the D-07 exception); bare-JSON assistant text is "+
			"preserved verbatim. See TestAnthropic_NoCoerce_Behavioral for the "+
			"primary behavioral regression guard. stripped source:\n%s", stripped)
	}
}

// stripGoComments removes Go `//` line comments and `/* ... */` block
// comments from src so static-source assertions can distinguish
// documentation mentions of a symbol from real code references.
// Naive byte-walk implementation — does NOT attempt to handle
// `//`-in-string-literals as a comment (that would be a defect-in-
// the-source-anyway scenario). Sufficient for the tightly-scoped
// PHASE 6 INVARIANT check.
func stripGoComments(src string) string {
	var sb strings.Builder
	sb.Grow(len(src))
	i := 0
	n := len(src)
	for i < n {
		// Line comment.
		if i+1 < n && src[i] == '/' && src[i+1] == '/' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		// Block comment.
		if i+1 < n && src[i] == '/' && src[i+1] == '*' {
			i += 2
			for i+1 < n && (src[i] != '*' || src[i+1] != '/') {
				i++
			}
			if i+1 < n {
				i += 2 // skip closing */
			}
			continue
		}
		// String literal — skip past so a "//" or "/*" inside a
		// string does not get treated as a comment opening.
		if src[i] == '"' {
			sb.WriteByte(src[i])
			i++
			for i < n && src[i] != '"' {
				if src[i] == '\\' && i+1 < n {
					sb.WriteByte(src[i])
					sb.WriteByte(src[i+1])
					i += 2
					continue
				}
				sb.WriteByte(src[i])
				i++
			}
			if i < n {
				sb.WriteByte(src[i])
				i++
			}
			continue
		}
		// Raw string literal (backticks).
		if src[i] == '`' {
			sb.WriteByte(src[i])
			i++
			for i < n && src[i] != '`' {
				sb.WriteByte(src[i])
				i++
			}
			if i < n {
				sb.WriteByte(src[i])
				i++
			}
			continue
		}
		sb.WriteByte(src[i])
		i++
	}
	return sb.String()
}
