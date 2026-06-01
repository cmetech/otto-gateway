package ollama

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

// fakeACPClient is a minimal engine.ACPClient used by NewEntryForTest
// in Plan 05-03 Task 3 session-handler tests. It is a no-op stub —
// adapter tests inject a separate fakeEngine to drive the
// Collect/Run paths; the entry-bound engine path uses sessionEngine
// (below) which routes back to the same fakeEngine.
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
// a pre-built *session.Entry. It is the locked test seam from Plan 05-03
// Task 3 — adapter tests do NOT stand up a real session.Registry.
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

// sessionEngine records that EngineForSession was called with a non-nil
// *session.Entry and delegates Collect/Run to the supplied inner Engine.
// Used by the X-Session-Id tests to assert that the registry path takes
// the EngineForSession factory closure and not the cfg.Engine pool
// engine.
type sessionEngine struct {
	inner       Engine
	callCount   atomic.Int32
	lastEntryID atomic.Pointer[string]
}

func (s *sessionEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return s.inner.Collect(ctx, req)
}

func (s *sessionEngine) Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	return s.inner.Run(ctx, req)
}

// RunPostHooks delegates to the inner Engine so session tests observe
// the same PostHook chain behavior as the pool path.
func (s *sessionEngine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	return s.inner.RunPostHooks(ctx, req, resp)
}

// CollectFromRun delegates to the inner Engine (T-5b seam).
func (s *sessionEngine) CollectFromRun(ctx context.Context, run RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return s.inner.CollectFromRun(ctx, run, req)
}

// newTestAdapterWithSession returns an Adapter wired with a pool engine,
// a registry, and an EngineForSession factory that returns the supplied
// sessionEng for any *session.Entry. (Test seam — production wiring
// constructs a fresh engine.Engine per request.)
func newTestAdapterWithSession(poolEng Engine, reg SessionRegistry, sessionEng *sessionEngine) *Adapter {
	return New(Config{
		Engine:   poolEng,
		Registry: reg,
		EngineForSession: func(entry *session.Entry) Engine {
			id := entry.SessionID
			sessionEng.lastEntryID.Store(&id)
			sessionEng.callCount.Add(1)
			return sessionEng
		},
		Version: "0.0.0-test",
		Commit:  "abc1234",
	})
}

// doPostWithSid is doPost with a configurable X-Session-Id header.
func doPostWithSid(t *testing.T, a *Adapter, path, sid, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if sid != "" {
		r.Header.Set("X-Session-Id", sid)
	}
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)
	return w
}

// TestOllamaHandleChat_NoXSessionId_RoutesToPool — request with no
// X-Session-Id header routes through cfg.Engine (the pool engine), and
// the registry is never consulted.
func TestOllamaHandleChat_NoXSessionId_RoutesToPool(t *testing.T) {
	poolEng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello!"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	reg := &fakeSessionRegistry{}
	sessionEng := &sessionEngine{inner: poolEng}
	a := newTestAdapterWithSession(poolEng, reg, sessionEng)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := doPostWithSid(t, a, "/chat", "", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if reg.callCount() != 0 {
		t.Errorf("Registry.Get should NOT be called when X-Session-Id is empty; got %d", reg.callCount())
	}
	if sessionEng.callCount.Load() != 0 {
		t.Errorf("EngineForSession should NOT be called; got %d", sessionEng.callCount.Load())
	}
}

// TestOllamaHandleChat_WithXSessionId_RoutesToRegistry — request with
// X-Session-Id="sid-X" causes the handler to call Registry.Get with
// "sid-X" and route through EngineForSession (sessionEng), not the
// pool engine directly.
func TestOllamaHandleChat_WithXSessionId_RoutesToRegistry(t *testing.T) {
	poolEng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello!"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	entry := session.NewEntryForTest(fakeACPClient{}, "sid-X")
	reg := &fakeSessionRegistry{entry: entry}
	sessionEng := &sessionEngine{inner: poolEng}
	a := newTestAdapterWithSession(poolEng, reg, sessionEng)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := doPostWithSid(t, a, "/chat", "sid-X", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if reg.callCount() != 1 {
		t.Fatalf("Registry.Get call count: got %d, want 1", reg.callCount())
	}
	if got := reg.firstSid(); got != "sid-X" {
		t.Errorf("Registry.Get sid: got %q, want %q", got, "sid-X")
	}
	if sessionEng.callCount.Load() != 1 {
		t.Errorf("EngineForSession should be called exactly once; got %d", sessionEng.callCount.Load())
	}
}

// TestOllamaHandleChat_SessionMaxExceeded_Returns503 — registry returns
// ErrSessionMaxExceeded → 503 with Ollama-shaped error envelope.
func TestOllamaHandleChat_SessionMaxExceeded_Returns503(t *testing.T) {
	reg := &fakeSessionRegistry{getErr: session.ErrSessionMaxExceeded}
	sessionEng := &sessionEngine{inner: &fakeEngine{}}
	a := newTestAdapterWithSession(&fakeEngine{}, reg, sessionEng)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := doPostWithSid(t, a, "/chat", "sid-overflow", body)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503; body=%s", w.Code, w.Body.String())
	}
	var body503 map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body503); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body503["error"], "capacity") {
		t.Errorf("error body: got %q, want containing 'capacity'", body503["error"])
	}
}

// TestOllamaHandleChat_TakesEntryMutex — after the handler returns,
// entry.Mu must be unlocked (TryLock succeeds) and entry.LastUsed must
// have been advanced via MarkUsed (D-11).
func TestOllamaHandleChat_TakesEntryMutex(t *testing.T) {
	poolEng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	entry := session.NewEntryForTest(fakeACPClient{}, "sid-Y")
	before := entry.LastUsed
	reg := &fakeSessionRegistry{entry: entry}
	sessionEng := &sessionEngine{inner: poolEng}
	a := newTestAdapterWithSession(poolEng, reg, sessionEng)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := doPostWithSid(t, a, "/chat", "sid-Y", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	// After the handler returns, entry.Mu should be unlocked.
	if !entry.Mu.TryLock() {
		t.Error("entry.Mu still locked after handler return — defer Unlock did not fire")
	} else {
		entry.Mu.Unlock()
	}
	// MarkUsed must have advanced LastUsed.
	if !entry.LastUsed.After(before) {
		t.Errorf("entry.LastUsed not advanced after handler: before=%v, after=%v", before, entry.LastUsed)
	}
}

// TestOllamaHandleChat_GenericRegistryError_Returns500 — any non-sentinel
// registry error → 500.
func TestOllamaHandleChat_GenericRegistryError_Returns500(t *testing.T) {
	reg := &fakeSessionRegistry{getErr: errors.New("kiro exploded")}
	sessionEng := &sessionEngine{inner: &fakeEngine{}}
	a := newTestAdapterWithSession(&fakeEngine{}, reg, sessionEng)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := doPostWithSid(t, a, "/chat", "sid-X", body)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", w.Code)
	}
}

// TestOllamaHandleGenerate_WithXSessionId_RoutesToRegistry — same
// behavior on /generate.
func TestOllamaHandleGenerate_WithXSessionId_RoutesToRegistry(t *testing.T) {
	poolEng := &fakeEngine{
		resp: &canonical.ChatResponse{
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello!"}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	entry := session.NewEntryForTest(fakeACPClient{}, "sid-gen")
	reg := &fakeSessionRegistry{entry: entry}
	sessionEng := &sessionEngine{inner: poolEng}
	a := newTestAdapterWithSession(poolEng, reg, sessionEng)

	body := `{"model":"auto","prompt":"hi","stream":false}`
	w := doPostWithSid(t, a, "/generate", "sid-gen", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if reg.callCount() != 1 {
		t.Errorf("Registry.Get call count: got %d, want 1", reg.callCount())
	}
}
