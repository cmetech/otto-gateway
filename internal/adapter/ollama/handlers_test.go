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
}

func (f *fakeEngine) Collect(_ context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// Run is a compile stub so adapter.go's Engine interface is satisfied after
// Plan 01. Plan 03 Task 2 replaces this with a real fake RunHandle for
// streaming tests.
func (f *fakeEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	return nil, errors.New("fakeEngine.Run: not implemented until Plan 03 wires the real fake — streaming tests use Plan 03")
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

func TestHandleChat_StreamTrue_SilentDowngrade(t *testing.T) {
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
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	// Engine was called — proves we did NOT branch into streaming.
	if eng.lastReq == nil {
		t.Fatal("engine not called — silent downgrade broken")
	}
	// canonical.Stream should be false in the request (the adapter
	// flipped wire.Stream to false before translating).
	if eng.lastReq.Stream {
		t.Error("canonical Stream: got true, want false (silent downgrade did not propagate)")
	}
}

func TestHandleChat_EngineError_500(t *testing.T) {
	a := newTestAdapter(&fakeEngine{err: errors.New("kiro exploded")}, nil)
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	w := doPost(t, a, "/chat", body)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", w.Code)
	}
	// T-02-33: error string must not echo the request body content.
	if strings.Contains(w.Body.String(), "hi") {
		t.Errorf("error body leaks request content: %q", w.Body.String())
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
