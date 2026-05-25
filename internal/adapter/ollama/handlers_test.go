package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
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
}

func (f *fakeEngine) Collect(_ context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
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
