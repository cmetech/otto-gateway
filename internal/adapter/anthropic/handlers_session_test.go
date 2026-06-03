package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/session"
)

// fakeACPClient is a minimal engine.ACPClient used by NewEntryForTest.
type fakeACPClient struct{}

func (fakeACPClient) NewSession(_ context.Context, _ string) (string, error) {
	return "test-session", nil
}
func (fakeACPClient) SetModel(_ context.Context, _, _ string) error { return nil }
func (fakeACPClient) Prompt(_ context.Context, _ string, _ []canonical.Block) (engine.Stream, error) {
	return nil, errors.New("fakeACPClient.Prompt should not be called in adapter session tests")
}
func (fakeACPClient) Cancel(_ string) {}

// fakeSessionRegistry records every Get(ctx, sid, cwd) call and returns
// a pre-built *session.Entry.
type fakeSessionRegistry struct {
	mu     sync.Mutex
	calls  []registryGetCall
	entry  *session.Entry
	getErr error
}

type registryGetCall struct {
	sid string
	cwd string
}

func (f *fakeSessionRegistry) Get(_ context.Context, sid, cwd string) (*session.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, registryGetCall{sid: sid, cwd: cwd})
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.entry, nil
}

func (f *fakeSessionRegistry) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSessionRegistry) firstSid() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[0].sid
}

type sessionEngine struct {
	inner     Engine
	callCount atomic.Int32
}

func (s *sessionEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return s.inner.Collect(ctx, req)
}

func (s *sessionEngine) Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	return s.inner.Run(ctx, req)
}

// RunPostHooks delegates to the inner Engine so the session-aware tests
// observe the same PostHook chain behavior as the pool path.
func (s *sessionEngine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	return s.inner.RunPostHooks(ctx, req, resp)
}

// CollectFromRun delegates to the inner Engine (T-5b seam).
func (s *sessionEngine) CollectFromRun(ctx context.Context, run RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return s.inner.CollectFromRun(ctx, run, req)
}

func newSessionTestAdapter(poolEng *fakeEngine, reg SessionRegistry, sessionEng *sessionEngine) *Adapter {
	return New(Config{
		Engine:   poolEng,
		Registry: reg,
		EngineForSession: func(_ *session.Entry) Engine {
			sessionEng.callCount.Add(1)
			return sessionEng
		},
	})
}

// doPostWithSid is doPost with a configurable X-Session-Id header (and
// the required anthropic-version header).
func doPostWithSid(t *testing.T, a *Adapter, sid, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/messages", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("anthropic-version", "2023-06-01")
	if sid != "" {
		r.Header.Set("X-Session-Id", sid)
	}
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)
	return w
}

// TestAnthropicHandleMessages_NoXSessionId_RoutesToPool — empty
// X-Session-Id header → pool path; registry never consulted.
func TestAnthropicHandleMessages_NoXSessionId_RoutesToPool(t *testing.T) {
	poolEng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello!"}},
			},
		},
	}
	reg := &fakeSessionRegistry{}
	sessionEng := &sessionEngine{inner: poolEng}
	a := newSessionTestAdapter(poolEng, reg, sessionEng)

	body := `{"model":"claude-3-opus","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	w := doPostWithSid(t, a, "", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if reg.callCount() != 0 {
		t.Errorf("Registry.Get should NOT be called when X-Session-Id empty; got %d", reg.callCount())
	}
	if sessionEng.callCount.Load() != 0 {
		t.Errorf("EngineForSession should NOT be called; got %d", sessionEng.callCount.Load())
	}
}

// TestAnthropicHandleMessages_WithXSessionId_RoutesToRegistry.
func TestAnthropicHandleMessages_WithXSessionId_RoutesToRegistry(t *testing.T) {
	poolEng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello!"}},
			},
		},
	}
	entry := session.NewEntryForTest(fakeACPClient{}, "sid-A")
	reg := &fakeSessionRegistry{entry: entry}
	sessionEng := &sessionEngine{inner: poolEng}
	a := newSessionTestAdapter(poolEng, reg, sessionEng)

	body := `{"model":"claude-3-opus","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	w := doPostWithSid(t, a, "sid-A", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if reg.callCount() != 1 {
		t.Fatalf("Registry.Get call count: got %d, want 1", reg.callCount())
	}
	if got := reg.firstSid(); got != "sid-A" {
		t.Errorf("Registry.Get sid: got %q, want %q", got, "sid-A")
	}
	if sessionEng.callCount.Load() != 1 {
		t.Errorf("EngineForSession should be called exactly once; got %d", sessionEng.callCount.Load())
	}
}

// TestAnthropicHandleMessages_SessionMaxExceeded_Returns503 — must use
// the Anthropic error envelope with type=overloaded_error.
func TestAnthropicHandleMessages_SessionMaxExceeded_Returns503(t *testing.T) {
	reg := &fakeSessionRegistry{getErr: session.ErrSessionMaxExceeded}
	sessionEng := &sessionEngine{inner: &fakeEngine{}}
	a := newSessionTestAdapter(&fakeEngine{}, reg, sessionEng)

	body := `{"model":"claude-3-opus","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	w := doPostWithSid(t, a, "sid-overflow", body)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503; body=%s", w.Code, w.Body.String())
	}

	// Anthropic error envelope: {"type":"error","error":{"type":"overloaded_error","message":"..."}}
	var env struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Type != "error" {
		t.Errorf("top-level type: got %q, want %q", env.Type, "error")
	}
	if env.Error.Type != "overloaded_error" {
		t.Errorf("error.type: got %q, want %q", env.Error.Type, "overloaded_error")
	}
	if !strings.Contains(env.Error.Message, "capacity") {
		t.Errorf("error.message: got %q, want containing 'capacity'", env.Error.Message)
	}
}

// TestAnthropicHandleMessages_TakesEntryMutex — D-11 verification.
func TestAnthropicHandleMessages_TakesEntryMutex(t *testing.T) {
	poolEng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
			},
		},
	}
	entry := session.NewEntryForTest(fakeACPClient{}, "sid-mtx")
	before := entry.LastUsed
	reg := &fakeSessionRegistry{entry: entry}
	sessionEng := &sessionEngine{inner: poolEng}
	a := newSessionTestAdapter(poolEng, reg, sessionEng)

	body := `{"model":"claude-3-opus","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	w := doPostWithSid(t, a, "sid-mtx", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	if !entry.Mu.TryLock() {
		t.Error("entry.Mu still locked — defer Unlock did not fire")
	} else {
		entry.Mu.Unlock()
	}
	if !entry.LastUsed.After(before) {
		t.Errorf("entry.LastUsed not advanced: before=%v, after=%v", before, entry.LastUsed)
	}
}

// TestAnthropicHandleMessages_GenericRegistryError_Returns500.
func TestAnthropicHandleMessages_GenericRegistryError_Returns500(t *testing.T) {
	reg := &fakeSessionRegistry{getErr: errors.New("kiro exploded")}
	sessionEng := &sessionEngine{inner: &fakeEngine{}}
	a := newSessionTestAdapter(&fakeEngine{}, reg, sessionEng)

	body := `{"model":"claude-3-opus","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	w := doPostWithSid(t, a, "sid-err", body)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", w.Code)
	}
}
