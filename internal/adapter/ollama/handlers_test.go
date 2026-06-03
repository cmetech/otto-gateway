package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin/jsonformat"
	"otto-gateway/internal/testutil"
)

// fakeEngine is the whitebox test double for the consumer-defined
// Engine interface declared in adapter.go. Whitebox is locked: the
// fakeEngine satisfies the package-private interface without
// redeclaring it (which would risk drift).
type fakeEngine struct {
	resp *canonical.ChatResponse
	err  error
	// lastReq captures the canonical request the handler synthesized
	// so tests can assert wire→canonical translation passed through.
	lastReq *canonical.ChatRequest

	// runChunks is the set of canonical.Chunk values Engine.Run sends during
	// streaming tests. Nil means the stream closes immediately (done:true only).
	runChunks []canonical.Chunk
	// runErr, if non-nil, is returned directly by Engine.Run before any streaming.
	runErr error

	// Quick 260530-df2 — RunPostHooks observation. postN counts
	// invocations (per-surface double-fire guard). lastPostResp
	// captures the resp the hook chain was called with so tests can
	// assert aggregator content shape. postErr (default nil) is the
	// error returned to the caller — handlers swallow + WARN-log on
	// streaming, but the test can verify the swallow contract.
	postN        int
	lastPostResp *canonical.ChatResponse
	postErr      error
}

// RunPostHooks records the invocation. Quick 260530-df2 wired the
// streaming branches of handleChat / handleGenerate to call this after
// runNDJSONEmitter returns.
func (f *fakeEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	f.postN++
	f.lastPostResp = resp
	return f.postErr
}

func (f *fakeEngine) Collect(_ context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// CollectFromRun satisfies the T-5b seam. Returns f.resp (the same
// response the streaming aggregator would produce for the canned
// runChunks). Tests exercising the T-5b re-route path (stream flipped to
// false by a Pre hook post-Run) set f.resp and assert the handler
// renders the non-streaming JSON shape.
func (f *fakeEngine) CollectFromRun(_ context.Context, _ RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// runChunks is the set of canonical.Chunk values fakeEngine.Run sends for
// streaming tests. If nil, an empty channel is used (stream closes immediately).
// runErr is returned by Engine.Run itself (before any streaming begins).
// streamErr is the error returned by fakeStream.Result() at stream end.

// Run builds a fakeRunHandle populated with runChunks and closes the channel,
// enabling streaming handler tests. runErr, if non-nil, causes Run to return
// an error directly (before streaming begins).
func (f *fakeEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	if f.runErr != nil {
		return nil, f.runErr
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	return newFakeRunHandle(f.runChunks, final, nil), nil
}

// fakeCatalog implements ModelCatalog with a fixed []canonical.ModelInfo.
type fakeCatalog struct {
	models []canonical.ModelInfo
}

func (f *fakeCatalog) Models() []canonical.ModelInfo { return f.models }

// helper: build an Adapter wired to the test doubles.
func newTestAdapter(eng Engine, cat ModelCatalog) *Adapter {
	return New(Config{
		Engine:       eng,
		ModelCatalog: cat,
		Version:      "0.0.0-test",
		Commit:       "abc1234",
	})
}

// helper: POST JSON body to the protected router.
func doPost(t *testing.T, a *Adapter, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)
	return w
}

func doGet(t *testing.T, a *Adapter, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)
	return w
}

// ----------------------------------------------------------------------------
// /api/chat
// ----------------------------------------------------------------------------

func TestHandleChat_Happy(t *testing.T) {
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello!"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := doPost(t, a, "/chat", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}

	var resp ollamaChatResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Message.Content != "Hello!" {
		t.Errorf("message.content: got %q, want Hello!", resp.Message.Content)
	}
	if resp.Done != true {
		t.Error("done: got false, want true")
	}
	if resp.DoneReason != "stop" {
		t.Errorf("done_reason: got %q, want stop", resp.DoneReason)
	}
}

func TestHandleChat_EmptyMessages_400(t *testing.T) {
	a := newTestAdapter(&fakeEngine{}, nil)
	body := `{"model":"auto","messages":[]}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

// TestHandleChat_Streaming: absent/true stream field routes to Engine.Run
// and the response Content-Type is application/x-ndjson.
func TestHandleChat_Streaming(t *testing.T) {
	eng := &fakeEngine{
		runChunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello"}},
		},
	}
	a := newTestAdapter(eng, nil)
	// Absent stream field (default = stream:true per streamEnabled).
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Errorf("Content-Type: got %q, want application/x-ndjson prefix (streaming branch)", ct)
	}
}

// TestHandleChat_StreamFalse_NonStreaming: explicit stream:false still routes
// to Engine.Collect and returns a single application/json object.
func TestHandleChat_StreamFalse_NonStreaming(t *testing.T) {
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json prefix (non-streaming)", ct)
	}
	// Engine.Collect was called (not Run).
	if eng.lastReq == nil {
		t.Fatal("engine.Collect not called for stream:false")
	}
}

// TestHandleChat_EngineError_500: stream:false + Collect error → 500.
func TestHandleChat_EngineError_500(t *testing.T) {
	a := newTestAdapter(&fakeEngine{err: errors.New("kiro exploded")}, nil)
	// Use stream:false so the test exercises Engine.Collect (not Engine.Run).
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", w.Code)
	}
	// T-02-33: error string must not echo the request body content.
	if strings.Contains(w.Body.String(), "hi") {
		t.Errorf("error body leaks request content: %q", w.Body.String())
	}
	// CR-01 (Phase 6 review): the wire-visible body must be the
	// neutral generic message — never echo the raw engine error.
	if !strings.Contains(w.Body.String(), "internal error") {
		t.Errorf("error body missing generic message: %q", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "kiro exploded") {
		t.Errorf("error body leaks raw engine error string: %q", w.Body.String())
	}
}

// TestHandleChat_RunError_500: streaming path (default absent stream) +
// Engine.Run error → 500 before any NDJSON is written.
func TestHandleChat_RunError_500(t *testing.T) {
	a := newTestAdapter(&fakeEngine{runErr: errors.New("kiro exploded on Run")}, nil)
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", w.Code)
	}
}

func TestHandleChat_EngineNil_503(t *testing.T) {
	a := newTestAdapter(nil, nil)
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503 (engine nil = KIRO_CMD unset)", w.Code)
	}
}

// ----------------------------------------------------------------------------
// /api/generate
// ----------------------------------------------------------------------------

func TestHandleGenerate_Happy(t *testing.T) {
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "blue"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","prompt":"why is the sky blue?","stream":false}`
	w := doPost(t, a, "/generate", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ollamaGenerateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Note: /api/generate carries `response`, not `message: {...}`.
	if resp.Response != "blue" {
		t.Errorf("response: got %q, want blue", resp.Response)
	}
	if resp.DoneReason != "stop" {
		t.Errorf("done_reason: got %q, want stop", resp.DoneReason)
	}
}

func TestHandleGenerate_EmptyPrompt_400(t *testing.T) {
	a := newTestAdapter(&fakeEngine{}, nil)
	body := `{"model":"auto","prompt":""}`
	w := doPost(t, a, "/generate", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

// ----------------------------------------------------------------------------
// /api/tags
// ----------------------------------------------------------------------------

func TestHandleTags_PrependsAuto(t *testing.T) {
	cat := &fakeCatalog{
		models: []canonical.ModelInfo{
			{ID: "claude-sonnet-4-7", Name: "Claude Sonnet 4.7"},
			{ID: "claude-opus-4-7", Name: "Claude Opus 4.7"},
		},
	}
	a := newTestAdapter(nil, cat)
	w := doGet(t, a, "/tags")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp ollamaTagsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Models) != 3 {
		t.Fatalf("models len: got %d, want 3 (auto + 2 catalog)", len(resp.Models))
	}
	if resp.Models[0].Name != "auto" || resp.Models[0].Model != "auto" {
		t.Errorf("models[0]: got %+v, want auto-first (Node parity)", resp.Models[0])
	}
	if resp.Models[1].Name != "claude-sonnet-4-7" {
		t.Errorf("models[1]: got %q, want claude-sonnet-4-7", resp.Models[1].Name)
	}
	// Field shape: details.format=gguf, family=kiro, parameter_size=unknown.
	if resp.Models[0].Details.Format != "gguf" {
		t.Errorf("models[0].details.format: got %q, want gguf", resp.Models[0].Details.Format)
	}
}

func TestHandleTags_NilCatalog_OnlyAuto(t *testing.T) {
	a := newTestAdapter(nil, nil)
	w := doGet(t, a, "/tags")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp ollamaTagsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Models) != 1 || resp.Models[0].Name != "auto" {
		t.Errorf("models: got %+v, want [auto]", resp.Models)
	}
}

// ----------------------------------------------------------------------------
// /api/show
// ----------------------------------------------------------------------------

func TestHandleShow_Found(t *testing.T) {
	cat := &fakeCatalog{models: []canonical.ModelInfo{{ID: "claude-sonnet-4-7"}}}
	a := newTestAdapter(nil, cat)
	w := doPost(t, a, "/show", `{"model":"claude-sonnet-4-7"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ollamaShowResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Model != "claude-sonnet-4-7" {
		t.Errorf("model: got %q", resp.Model)
	}
	if len(resp.Capabilities) == 0 {
		t.Error("capabilities: empty")
	}
}

func TestHandleShow_NotFound_404(t *testing.T) {
	a := newTestAdapter(nil, &fakeCatalog{})
	w := doPost(t, a, "/show", `{"model":"does-not-exist"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestHandleShow_AutoAlwaysExists(t *testing.T) {
	a := newTestAdapter(nil, nil)
	w := doPost(t, a, "/show", `{"model":"auto"}`)
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (auto always present)", w.Code)
	}
}

// ----------------------------------------------------------------------------
// /api/ps
// ----------------------------------------------------------------------------

func TestHandlePS_SyntheticEntry(t *testing.T) {
	a := newTestAdapter(nil, nil)
	w := doGet(t, a, "/ps")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp ollamaPSResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Models) != 1 {
		t.Errorf("models len: got %d, want 1 (synthetic single entry)", len(resp.Models))
	}
	if resp.Models[0].Name != "auto" {
		t.Errorf("models[0].name: got %q, want auto", resp.Models[0].Name)
	}
	if resp.Models[0].ExpiresAt == "" {
		t.Error("expires_at: empty (Node parity broken)")
	}
}

// ----------------------------------------------------------------------------
// /api/version (exposed via HandleVersion accessor)
// ----------------------------------------------------------------------------

func TestHandleVersion_ReturnsVersionAndCommit(t *testing.T) {
	a := newTestAdapter(nil, nil)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/version", nil)
	w := httptest.NewRecorder()
	a.HandleVersion()(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ollamaVersionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Version != "0.0.0-test" {
		t.Errorf("version: got %q, want 0.0.0-test", resp.Version)
	}
	if resp.Commit != "abc1234" {
		t.Errorf("commit: got %q, want abc1234", resp.Commit)
	}
}

// TestProtectedRouter_DoesNotRegisterVersion asserts Codex M-4: the
// adapter's protected router does NOT match /version (the OUTER router
// owns /api/version exclusively).
func TestProtectedRouter_DoesNotRegisterVersion(t *testing.T) {
	a := newTestAdapter(nil, nil)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/version", nil)
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 404 or 405 — adapter's protected router must NOT register /version (Codex M-4)", w.Code)
	}
}

// TestHandleChat_RequestBodyTooLarge proves the Codex M-5 path: a 5 MiB
// body is rejected with 413 even though the JSON would otherwise parse.
func TestHandleChat_RequestBodyTooLarge(t *testing.T) {
	a := newTestAdapter(&fakeEngine{}, nil)
	// Build a 5 MiB request body with valid JSON envelope.
	pad := strings.Repeat("x", 5<<20)
	body := `{"messages":[{"role":"user","content":"` + pad + `"}]}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413 (Codex M-5 cap)", w.Code)
	}
}

// TestHandlers_DefaultStreamOmitted_GoesToStreaming locks the Ollama default:
// when the `stream` field is OMITTED from the request body, the streaming
// branch must take over (Content-Type: application/x-ndjson). Per Phase 4
// D-05 + streamEnabled(s *bool): nil stream → true.
//
// Three subtests cover all three forms: omitted, explicit true, explicit false.
// (REVIEW HIGH #1 — default-omitted test addition.)
func TestHandlers_DefaultStreamOmitted_GoesToStreaming(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantStream  bool
		wantContent string
	}{
		{
			name:        "omitted_defaults_to_streaming",
			body:        `{"model":"auto","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}]}`,
			wantStream:  true,
			wantContent: "application/x-ndjson",
		},
		{
			name:        "explicit_true_streams",
			body:        `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantStream:  true,
			wantContent: "application/x-ndjson",
		},
		{
			name:        "explicit_false_non_streaming",
			body:        `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`,
			wantStream:  false,
			wantContent: "application/json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &fakeEngine{
				resp: &canonical.ChatResponse{
					Message: canonical.Message{
						Role:    canonical.RoleAssistant,
						Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
					},
					StopReason: canonical.StopEndTurn,
				},
				runChunks: []canonical.Chunk{
					{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "ok"}},
				},
			}
			a := newTestAdapter(eng, nil)
			w := doPost(t, a, "/chat", tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
			}
			ct := w.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, tc.wantContent) {
				t.Errorf("Content-Type: got %q, want prefix %q", ct, tc.wantContent)
			}
		})
	}
}

// TestHandleChat_NonStreaming_CoerceFires proves the Phase 6 D-01 hook-in:
// when /api/chat is invoked with tools[] and the engine returns a
// JSON-shaped text body, the non-streaming branch invokes
// engine.CoerceToolCall after engine.Collect and the response carries
// message.tool_calls[] with plain-object arguments — NOT a JSON-string.
//
// The fakeEngine.Collect returns text `{"location":"NYC"}` which matches
// the get_weather tool's properties. CoerceToolCall must rewrite the
// response in place (Pitfall 6 — pointer-direct, no pre-copy).
func TestHandleChat_NonStreaming_CoerceFires(t *testing.T) {
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: `{"location":"NYC"}`},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","messages":[{"role":"user","content":"weather?"}],"stream":false,"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}]}`
	w := doPost(t, a, "/chat", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// Byte-level wire-shape canary: arguments serialize as a plain JSON
	// object (Ollama D-04), NOT an OpenAI-style JSON-encoded string.
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, `"arguments":{"location":"NYC"}`) {
		t.Errorf("wire-shape canary FAILED — expected plain-object arguments in response; got: %s", bodyStr)
	}
	if strings.Contains(bodyStr, `"arguments":"`) {
		t.Errorf("wire-shape canary FAILED — found JSON-string arguments form (OpenAI shape); got: %s", bodyStr)
	}

	var resp ollamaChatResponse
	if err := json.NewDecoder(strings.NewReader(bodyStr)).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("Message.ToolCalls len: got %d, want 1 (coerce must have fired); body=%s", len(resp.Message.ToolCalls), bodyStr)
	}
	if resp.Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("ToolCalls[0].Function.Name: got %q, want get_weather", resp.Message.ToolCalls[0].Function.Name)
	}
	if resp.Message.ToolCalls[0].Function.Arguments["location"] != "NYC" {
		t.Errorf("ToolCalls[0].Function.Arguments[location]: got %v, want NYC", resp.Message.ToolCalls[0].Function.Arguments["location"])
	}
	// Step 8: text content is cleared after coerce.
	if resp.Message.Content != "" {
		t.Errorf("Message.Content: got %q, want empty (coerce clears the matched text)", resp.Message.Content)
	}
}

// TestHandleChat_NonStreaming_KiroNativeNarration_NoCoerce locks the
// iteration-3 interaction: when engine.Collect produces `[tool: <name>]\n`
// narration text (from the 06-01 aggregator), CoerceToolCall sees the
// narration as non-JSON text, Step 3 + Step 4 both fail, Step 5 returns
// false, and the narration text passes through to message.content
// unchanged. No tool_calls field is populated.
func TestHandleChat_NonStreaming_KiroNativeNarration_NoCoerce(t *testing.T) {
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "[tool: get_weather]\n"},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","messages":[{"role":"user","content":"weather?"}],"stream":false,"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}]}`
	w := doPost(t, a, "/chat", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	bodyStr := w.Body.String()
	if strings.Contains(bodyStr, `"tool_calls"`) {
		t.Errorf("non-streaming kiro-native narration must NOT produce tool_calls; body=%s", bodyStr)
	}
	if !strings.Contains(bodyStr, `[tool: get_weather]`) {
		t.Errorf("narration text missing from response; body=%s", bodyStr)
	}
}

// fencedJSONFixture is the fake-engine response text used in Phase 08.2
// fence-strip tests. Matches the integration test fixture from CONTEXT.md.
const fencedJSONFixture = "```json\n[\n  {\"name\": \"Item 1\", \"id\": 1},\n  {\"name\": \"Item 2\", \"id\": 2}\n]\n```"

// fencedJSONStripped is the expected content after StripFences removes the fence.
const fencedJSONStripped = "[\n  {\"name\": \"Item 1\", \"id\": 1},\n  {\"name\": \"Item 2\", \"id\": 2}\n]"

// Test_handleChat_NonStreaming_FormatJSON_StripsFences: fake engine returns
// fenced JSON, request has format:"json", assert message.content is stripped.
func Test_handleChat_NonStreaming_FormatJSON_StripsFences(t *testing.T) {
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: fencedJSONFixture},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","messages":[{"role":"user","content":"list items"}],"format":"json","stream":false}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ollamaChatResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Message.Content != fencedJSONStripped {
		t.Errorf("message.content: fences not stripped\ngot:  %q\nwant: %q", resp.Message.Content, fencedJSONStripped)
	}
}

// Test_handleChat_NonStreaming_FormatJSON_NoFencesNoStripNoOp: engine returns
// clean JSON, no double-strip artifacts.
func Test_handleChat_NonStreaming_FormatJSON_NoFencesNoStripNoOp(t *testing.T) {
	clean := `{"a":1}`
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: clean},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"format":"json","stream":false}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ollamaChatResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Message.Content != clean {
		t.Errorf("message.content: unexpected mutation\ngot:  %q\nwant: %q", resp.Message.Content, clean)
	}
}

// Test_handleChat_NonStreaming_NoFormat_PreservesFences: no format field,
// fences must be preserved verbatim.
func Test_handleChat_NonStreaming_NoFormat_PreservesFences(t *testing.T) {
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: fencedJSONFixture},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ollamaChatResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Message.Content != fencedJSONFixture {
		t.Errorf("message.content: fences stripped without format field\ngot:  %q\nwant: %q", resp.Message.Content, fencedJSONFixture)
	}
}

// Test_handleGenerate_NonStreaming_FormatJSON_StripsFences mirrors the chat
// test but for /api/generate, where the text lives in `response` not `message.content`.
func Test_handleGenerate_NonStreaming_FormatJSON_StripsFences(t *testing.T) {
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: fencedJSONFixture},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","prompt":"list items","format":"json","stream":false}`
	w := doPost(t, a, "/generate", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ollamaGenerateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Response != fencedJSONStripped {
		t.Errorf("response: fences not stripped\ngot:  %q\nwant: %q", resp.Response, fencedJSONStripped)
	}
}

// Test_handleGenerate_NonStreaming_FormatJSON_NoFencesNoStripNoOp: no double-strip.
func Test_handleGenerate_NonStreaming_FormatJSON_NoFencesNoStripNoOp(t *testing.T) {
	clean := `{"a":1}`
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: clean},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","prompt":"hi","format":"json","stream":false}`
	w := doPost(t, a, "/generate", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ollamaGenerateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Response != clean {
		t.Errorf("response: unexpected mutation\ngot:  %q\nwant: %q", resp.Response, clean)
	}
}

// Test_handleGenerate_NonStreaming_NoFormat_PreservesFences: no format, fences preserved.
func Test_handleGenerate_NonStreaming_NoFormat_PreservesFences(t *testing.T) {
	eng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: fencedJSONFixture},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := newTestAdapter(eng, nil)
	body := `{"model":"auto","prompt":"hi","stream":false}`
	w := doPost(t, a, "/generate", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ollamaGenerateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Response != fencedJSONFixture {
		t.Errorf("response: fences stripped without format field\ngot:  %q\nwant: %q", resp.Response, fencedJSONFixture)
	}
}

// ----------------------------------------------------------------------------
// Integration tests — Phase 08.2: full chain (JSONFormatSteering hooked in
// engine.Engine) + fake ACP returning fenced JSON. These tests exercise the
// three coordinated changes end-to-end: wire-decode → steering hook → fence-strip.
// ----------------------------------------------------------------------------

// formatIntegrationACP is a fake engine.ACPClient that returns a single
// text chunk containing fencedJSONFixture followed by a closed channel.
// It is intentionally local to the integration tests so it does not pollute
// the broader fakeEngine harness.
type formatIntegrationACP struct {
	// lastPromptBlocks captures the blocks passed to Prompt so tests can
	// inspect the system-prompt segment the steering hook injected.
	lastPromptBlocks []canonical.Block
}

func (f *formatIntegrationACP) NewSession(_ context.Context, _ string) (string, error) {
	return "format-integ-sid", nil
}
func (f *formatIntegrationACP) SetModel(_ context.Context, _, _ string) error { return nil }
func (f *formatIntegrationACP) Prompt(_ context.Context, _ string, blocks []canonical.Block) (engine.Stream, error) {
	f.lastPromptBlocks = blocks
	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: fencedJSONFixture}}
	close(ch)
	return &formatIntegStream{ch: ch}, nil
}
func (f *formatIntegrationACP) Cancel(_ string) {}

type formatIntegStream struct{ ch chan canonical.Chunk }

func (s *formatIntegStream) Chunks() <-chan canonical.Chunk { return s.ch }
func (s *formatIntegStream) Result() (*canonical.FinalResult, error) {
	return &canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil
}

// buildFormatIntegAdapter constructs the full chain engine + adapter for
// the format-parity integration tests. The jsonformat hook is wired with
// enabled=true, matching the production default.
func buildFormatIntegAdapter(t *testing.T, acp *formatIntegrationACP) (*Adapter, *formatIntegrationACP) {
	t.Helper()
	logger := testutil.Logger(t)
	eng := engine.New(engine.Config{
		Logger:   logger,
		ACP:      acp,
		PreHooks: []engine.PreHook{jsonformat.New(true)},
	})
	adapter := New(Config{
		Logger:  logger,
		Engine:  testEngineAdapter{eng: eng},
		Version: "test",
		Commit:  "integ",
	})
	return adapter, acp
}

// TestIntegration_OllamaChat_FormatJSON_NodeShimParity exercises the three
// coordinated Phase 08.2 changes end-to-end on /api/chat:
//   1. wire-decode: Format field is populated from format:"json" in the request
//   2. steering hook: GEN_RULES text is injected into the system prompt
//   3. fence-strip: the response body is unwrapped from the markdown fence
func TestIntegration_OllamaChat_FormatJSON_NodeShimParity(t *testing.T) {
	acp := &formatIntegrationACP{}
	adapter, _ := buildFormatIntegAdapter(t, acp)

	srv := httptest.NewServer(adapter.ProtectedRouter())
	defer srv.Close()

	body := []byte(`{"model":"claude-sonnet-4-7","messages":[{"role":"user","content":"list items"}],"format":"json","stream":false}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/chat", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Assertion 1: HTTP 200
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	var out ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Assertion 2: fences stripped — content is the raw JSON array, no ```json wrapper.
	if out.Message.Content != fencedJSONStripped {
		t.Errorf("message.content: fences not stripped\ngot:  %q\nwant: %q", out.Message.Content, fencedJSONStripped)
	}

	// Assertion 3: steering hook fired — GEN_RULES marker present in the
	// ACP prompt text. The engine encodes req.System as [System]\n<text>\n\n
	// in the first text block; the marker must appear in that text.
	const genRulesMarker = "Generate the COMPLETE result"
	promptText := ""
	for _, b := range acp.lastPromptBlocks {
		if b.Kind == canonical.BlockKindText && b.Text != nil {
			promptText += b.Text.Content
		}
	}
	if !strings.Contains(promptText, genRulesMarker) {
		t.Errorf("GEN_RULES not injected; prompt text=%q", promptText)
	}
}

// TestIntegration_OllamaChat_NoFormat_NoSteeringNoStrip is the sibling
// negative test: without format, neither steering nor fence-strip should
// fire. The fenced fixture must reach the client unchanged.
func TestIntegration_OllamaChat_NoFormat_NoSteeringNoStrip(t *testing.T) {
	acp := &formatIntegrationACP{}
	adapter, _ := buildFormatIntegAdapter(t, acp)

	srv := httptest.NewServer(adapter.ProtectedRouter())
	defer srv.Close()

	// No "format" field in the request body.
	body := []byte(`{"model":"claude-sonnet-4-7","messages":[{"role":"user","content":"list items"}],"stream":false}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/chat", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Assertion 1: HTTP 200
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	var out ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Assertion 2: fences preserved — no format means no strip.
	if out.Message.Content != fencedJSONFixture {
		t.Errorf("message.content: fences stripped without format\ngot:  %q\nwant: %q", out.Message.Content, fencedJSONFixture)
	}

	// Assertion 3: no steering — prompt text must NOT contain GEN_RULES.
	const genRulesMarker = "Generate the COMPLETE result"
	for _, b := range acp.lastPromptBlocks {
		if b.Kind == canonical.BlockKindText && b.Text != nil && strings.Contains(b.Text.Content, genRulesMarker) {
			t.Errorf("GEN_RULES injected without format field; prompt text=%q", b.Text.Content)
			break
		}
	}
}
