package anthropic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loop24-gateway/internal/canonical"
)

// notImplementedEngine is the smallest possible Engine satisfier — both
// methods return a sentinel error. Used by Task 1 router-wiring tests
// where we only care that the route reaches the handler.
type notImplementedEngine struct{}

func (notImplementedEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, errors.New("not implemented")
}

func (notImplementedEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	return nil, errors.New("not implemented")
}

// TestNew_DefaultsNilLogger asserts that constructing with a zero-valued
// Config (no Logger) does not panic and the resulting Adapter has a
// usable defensive logger. (Engine remains nil — that's intentional;
// handleMessages returns 503 in that case.)
func TestNew_DefaultsNilLogger(t *testing.T) {
	a := New(Config{})
	if a == nil {
		t.Fatal("New returned nil")
	}
	if a.cfg.Logger == nil {
		t.Error("Logger: still nil after defensive replacement")
	}
	if a.protectedRouter == nil {
		t.Error("protectedRouter: nil after New")
	}
	if a.ProtectedRouter() == nil {
		t.Error("ProtectedRouter(): returned nil")
	}
}

// TestProtectedRouter_MessagesRoute asserts that POST /messages is
// registered on the protected router and reaches the handler. We use
// a not-yet-implemented Engine — the handler will respond with a
// validation/auth/decode error or a 5xx, but it MUST NOT respond with
// the default chi 404, which would prove the route is missing.
func TestProtectedRouter_MessagesRoute(t *testing.T) {
	a := New(Config{Engine: notImplementedEngine{}})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/messages", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("anthropic-version", "2023-06-01")
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)

	// The exact body and status depend on Task 3 handler behavior — but
	// 404 here would prove the route is missing.
	if w.Code == http.StatusNotFound {
		t.Errorf("POST /messages: got 404 — route not registered; body=%s", w.Body.String())
	}
}

// TestProtectedRouter_UnknownRoute_Default404 asserts that routes other
// than POST /messages on the protected router fall through to the
// default chi 404 (NOT the Anthropic error envelope). Outer 404 handling
// is server.go's concern (consistent with Phase 2 Ollama — the adapter
// owns only its declared routes).
func TestProtectedRouter_UnknownRoute_Default404(t *testing.T) {
	a := New(Config{Engine: notImplementedEngine{}})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/nonexistent", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("POST /nonexistent: got %d, want 404 (default chi)", w.Code)
	}
	// The body should NOT be the Anthropic error envelope — chi's
	// default 404 is "404 page not found\n" plain text.
	if strings.Contains(w.Body.String(), `"type":"error"`) {
		t.Errorf("body: got Anthropic envelope %q, want chi default 404", w.Body.String())
	}
}

// TestProtectedRouter_MessagesGET_405 asserts GET /messages returns
// 405 Method Not Allowed (chi default — the route is registered only
// for POST).
func TestProtectedRouter_MessagesGET_405(t *testing.T) {
	a := New(Config{Engine: notImplementedEngine{}})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/messages", nil)
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /messages: got %d, want 405", w.Code)
	}
}
