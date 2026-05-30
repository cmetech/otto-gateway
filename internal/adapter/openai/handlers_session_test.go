package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

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

// sessionEngine delegates Collect/Run to inner and records that
// EngineForSession was called.
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

// RunPostHooks delegates to the inner Engine.
func (s *sessionEngine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	return s.inner.RunPostHooks(ctx, req, resp)
}

// newSessionTestAdapter wraps newFakeAdapter with the session wiring.
func newSessionTestAdapter(poolEng *fakeEngine, reg SessionRegistry, sessionEng *sessionEngine) *Adapter {
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	return New(Config{
		Logger:   logger,
		Engine:   poolEng,
		Registry: reg,
		EngineForSession: func(_ *session.Entry) Engine {
			sessionEng.callCount.Add(1)
			return sessionEng
		},
	})
}

func mountedSessionAdapter(a *Adapter) *httptest.Server {
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		a.RegisterRoutes(sub)
	})
	return httptest.NewServer(r)
}

func doChatCompletions(t *testing.T, srv *httptest.Server, sid, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if sid != "" {
		req.Header.Set("X-Session-Id", sid)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// TestOpenAIHandleChatCompletions_NoXSessionId_RoutesToPool — empty
// X-Session-Id header → pool path; registry never consulted.
func TestOpenAIHandleChatCompletions_NoXSessionId_RoutesToPool(t *testing.T) {
	poolEng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "Hello!"},
				},
			},
		},
	}
	reg := &fakeSessionRegistry{}
	sessionEng := &sessionEngine{inner: poolEng}
	a := newSessionTestAdapter(poolEng, reg, sessionEng)
	srv := mountedSessionAdapter(a)
	defer srv.Close()

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp := doChatCompletions(t, srv, "", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if reg.callCount() != 0 {
		t.Errorf("Registry.Get should NOT be called; got %d", reg.callCount())
	}
	if sessionEng.callCount.Load() != 0 {
		t.Errorf("EngineForSession should NOT be called; got %d", sessionEng.callCount.Load())
	}
}

// TestOpenAIHandleChatCompletions_WithXSessionId_RoutesToRegistry —
// X-Session-Id → registry path; EngineForSession invoked.
func TestOpenAIHandleChatCompletions_WithXSessionId_RoutesToRegistry(t *testing.T) {
	poolEng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "Hello!"},
				},
			},
		},
	}
	entry := session.NewEntryForTest(fakeACPClient{}, "sid-O")
	reg := &fakeSessionRegistry{entry: entry}
	sessionEng := &sessionEngine{inner: poolEng}
	a := newSessionTestAdapter(poolEng, reg, sessionEng)
	srv := mountedSessionAdapter(a)
	defer srv.Close()

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp := doChatCompletions(t, srv, "sid-O", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if reg.callCount() != 1 {
		t.Fatalf("Registry.Get call count: got %d, want 1", reg.callCount())
	}
	if got := reg.firstSid(); got != "sid-O" {
		t.Errorf("Registry.Get sid: got %q, want %q", got, "sid-O")
	}
	if sessionEng.callCount.Load() != 1 {
		t.Errorf("EngineForSession should be called exactly once; got %d", sessionEng.callCount.Load())
	}
}

// TestOpenAIHandleChatCompletions_SessionMaxExceeded_Returns503 —
// ErrSessionMaxExceeded → 503 with OpenAI error envelope.
func TestOpenAIHandleChatCompletions_SessionMaxExceeded_Returns503(t *testing.T) {
	reg := &fakeSessionRegistry{getErr: session.ErrSessionMaxExceeded}
	sessionEng := &sessionEngine{inner: &fakeEngine{}}
	a := newSessionTestAdapter(&fakeEngine{}, reg, sessionEng)
	srv := mountedSessionAdapter(a)
	defer srv.Close()

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp := doChatCompletions(t, srv, "sid-overflow", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 503; body=%s", resp.StatusCode, raw)
	}
	// Verify OpenAI error envelope shape.
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope: missing top-level error object; got %+v", env)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "capacity") {
		t.Errorf("error.message: got %q, want containing 'capacity'", msg)
	}
}

// TestOpenAIHandleChatCompletions_TakesEntryMutex — D-11 verification.
func TestOpenAIHandleChatCompletions_TakesEntryMutex(t *testing.T) {
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
	srv := mountedSessionAdapter(a)
	defer srv.Close()

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp := doChatCompletions(t, srv, "sid-mtx", body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
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

// TestOpenAIHandleChatCompletions_GenericRegistryError_Returns500.
func TestOpenAIHandleChatCompletions_GenericRegistryError_Returns500(t *testing.T) {
	reg := &fakeSessionRegistry{getErr: errors.New("kiro exploded")}
	sessionEng := &sessionEngine{inner: &fakeEngine{}}
	a := newSessionTestAdapter(&fakeEngine{}, reg, sessionEng)
	srv := mountedSessionAdapter(a)
	defer srv.Close()

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp := doChatCompletions(t, srv, "sid-X", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", resp.StatusCode)
	}
}

// TestOpenAIHandleCompletions_WithXSessionId_RoutesToRegistry — same
// X-Session-Id contract on /v1/completions.
func TestOpenAIHandleCompletions_WithXSessionId_RoutesToRegistry(t *testing.T) {
	poolEng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
			},
		},
	}
	entry := session.NewEntryForTest(fakeACPClient{}, "sid-cmpl")
	reg := &fakeSessionRegistry{entry: entry}
	sessionEng := &sessionEngine{inner: poolEng}
	a := newSessionTestAdapter(poolEng, reg, sessionEng)
	srv := mountedSessionAdapter(a)
	defer srv.Close()

	body := `{"model":"auto","prompt":"hi"}`
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/v1/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-Id", "sid-cmpl")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if reg.callCount() != 1 {
		t.Errorf("Registry.Get call count: got %d, want 1", reg.callCount())
	}
}
