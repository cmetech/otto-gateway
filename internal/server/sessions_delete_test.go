package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/server"
	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// fakeSessionDeleter records every Delete(sid) call and returns the
// preconfigured returnErr. Goroutine-safe so it survives -race on the
// parallel-test paths.
type fakeSessionDeleter struct {
	mu        sync.Mutex
	calls     []string
	returnErr error
}

func (f *fakeSessionDeleter) Delete(sid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sid)
	return f.returnErr
}

func (f *fakeSessionDeleter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSessionDeleter) firstCall() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[0]
}

// chiRouterWithSessionsRouter builds a chi.Router with the SessionsRouter
// mounted at /v1, skipping auth — used for unit-level tests that focus
// on the handler logic without exercising the full auth chain.
func chiRouterWithSessionsRouter(t *testing.T, deleter server.SessionDeleter) http.Handler {
	t.Helper()
	sr := &server.SessionsRouter{Registry: deleter, Logger: testutil.Logger(t)}
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		sr.RegisterRoutes(r)
	})
	return r
}

// TestSessionsRouter_Delete_KnownSid_Returns200WithDeleted — happy path.
func TestSessionsRouter_Delete_KnownSid_Returns200WithDeleted(t *testing.T) {
	fake := &fakeSessionDeleter{}
	h := chiRouterWithSessionsRouter(t, fake)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/v1/sessions/abc-123", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["deleted"] != "abc-123" {
		t.Errorf("body[deleted]: got %q, want %q", body["deleted"], "abc-123")
	}
}

// TestSessionsRouter_Delete_UnknownSid_Returns404 — sentinel error path.
func TestSessionsRouter_Delete_UnknownSid_Returns404(t *testing.T) {
	fake := &fakeSessionDeleter{returnErr: session.ErrSessionNotFound}
	h := chiRouterWithSessionsRouter(t, fake)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/v1/sessions/does-not-exist", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404; body=%s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "unknown session" {
		t.Errorf("body[error]: got %q, want %q", body["error"], "unknown session")
	}
}

// TestSessionsRouter_Delete_InternalError_Returns500 — generic error path.
func TestSessionsRouter_Delete_InternalError_Returns500(t *testing.T) {
	fake := &fakeSessionDeleter{returnErr: errors.New("kiro exploded")}
	h := chiRouterWithSessionsRouter(t, fake)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/v1/sessions/abc-123", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500; body=%s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "delete failed" {
		t.Errorf("body[error]: got %q, want %q", body["error"], "delete failed")
	}
}

// TestSessionsRouter_Delete_MissingSid — chi's URL pattern requires {id}.
// A request to /v1/sessions/ (trailing slash, no id) yields chi's own
// 404 before the handler runs. This test confirms behavior matches chi's
// routing default and that the handler is not invoked for that case.
func TestSessionsRouter_Delete_MissingSid(t *testing.T) {
	fake := &fakeSessionDeleter{}
	h := chiRouterWithSessionsRouter(t, fake)

	// DELETE /v1/sessions/ — no {id} parameter. chi routes this to its
	// internal 404 (NotFoundHandler) because the {id} parameter is
	// required by the chi route pattern.
	r := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/v1/sessions/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 404 or 405 (chi defensive routing)", w.Code)
	}
	if fake.callCount() != 0 {
		t.Errorf("Registry.Delete should NOT be called for empty {id}; got %d calls", fake.callCount())
	}
}

// TestSessionsRouter_Delete_RequiresAuth — when mounted via
// server.NewFromConfig under a prefix with AuthTokens set, DELETE
// without a bearer header returns 401. Proves the route is NOT
// auth-exempt (D-18 — only /health and /health/agents are exempt).
func TestSessionsRouter_Delete_RequiresAuth(t *testing.T) {
	fake := &fakeSessionDeleter{}
	srv := newFromConfigForTest(t, server.Config{
		AuthTokens: []string{"secret"},
		Surfaces: []server.SurfaceMount{
			{Prefix: "/v1", Router: &server.SessionsRouter{Registry: fake, Logger: testutil.Logger(t)}},
		},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/v1/sessions/abc-123", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /v1/sessions/abc-123 without bearer: got %d, want 401", w.Code)
	}
	if fake.callCount() != 0 {
		t.Errorf("Registry.Delete should NOT be called when auth fails; got %d calls", fake.callCount())
	}
}

// TestSessionsRouter_Delete_AcceptsValidAuth — same setup as the auth
// gate test, but with the correct bearer. Asserts 200 happy path.
func TestSessionsRouter_Delete_AcceptsValidAuth(t *testing.T) {
	fake := &fakeSessionDeleter{}
	srv := newFromConfigForTest(t, server.Config{
		AuthTokens: []string{"secret"},
		Surfaces: []server.SurfaceMount{
			{Prefix: "/v1", Router: &server.SessionsRouter{Registry: fake, Logger: testutil.Logger(t)}},
		},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/v1/sessions/abc-123", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("DELETE /v1/sessions/abc-123 with bearer: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if fake.callCount() != 1 {
		t.Errorf("Registry.Delete should be called exactly once; got %d", fake.callCount())
	}
}

// TestSessionsRouter_Delete_CallsRegistryDelete — Registry.Delete must be
// called with the exact sid from the URL.
func TestSessionsRouter_Delete_CallsRegistryDelete(t *testing.T) {
	fake := &fakeSessionDeleter{}
	h := chiRouterWithSessionsRouter(t, fake)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/v1/sessions/sess-X", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if got := fake.callCount(); got != 1 {
		t.Errorf("Registry.Delete call count: got %d, want 1", got)
	}
	if got := fake.firstCall(); got != "sess-X" {
		t.Errorf("Registry.Delete sid: got %q, want %q", got, "sess-X")
	}
}
