package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/goleak"

	"otto-gateway/internal/server"
	"otto-gateway/internal/testutil"
)

// stubAdminHandler is a minimal http.Handler used in admin-mount tests.
// It writes 200 OK with body "admin-stub" so tests can confirm the mount
// routed the request to the admin handler (not to some other handler).
var stubAdminHandler http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("admin-stub"))
}

// newAdminTestServer constructs a server.Config with sensible defaults for
// admin-mount tests: a stub Ollama surface on /api, a stub /api/version
// handler, and a logger wired from t. callers supply the partial override.
func newAdminTestServer(t *testing.T, cfg server.Config) *server.Server {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = testutil.Logger(t)
	}
	if cfg.Version == "" {
		cfg.Version = "test"
	}
	if len(cfg.Surfaces) == 0 {
		cfg.Surfaces = []server.SurfaceMount{
			{Prefix: "/api", Router: stubOllamaRegistrar()},
		}
	}
	if cfg.OllamaVersionHandler == nil {
		cfg.OllamaVersionHandler = stubVersionHandler()
		cfg.OllamaVersionPath = "/api/version"
	}
	return server.NewFromConfig(cfg)
}

// TestServer_AdminMountedAtRoot verifies that when AdminHandler is set,
// GET /admin routes to the stub and GET /admin/anything also reaches it.
func TestServer_AdminMountedAtRoot(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := newAdminTestServer(t, server.Config{
		AdminHandler: stubAdminHandler,
	})

	// GET /admin should route to the admin handler.
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("GET /admin: want 200, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "admin-stub") {
		t.Errorf("GET /admin: body want 'admin-stub', got %q", body)
	}

	// GET /admin/anything should also be handled by the admin sub-router.
	r2 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/anything", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, r2)
	// stubAdminHandler serves everything, so it should respond (not 404).
	if w2.Code == http.StatusNotFound {
		t.Errorf("GET /admin/anything: got 404, want admin handler to respond")
	}
}

// TestServer_AdminNilHandlerDoesNotPanic verifies that nil AdminHandler is
// safe and GET /admin returns 404 without panicking.
func TestServer_AdminNilHandlerDoesNotPanic(t *testing.T) {
	defer goleak.VerifyNone(t)

	// This must NOT panic.
	srv := newAdminTestServer(t, server.Config{
		AdminHandler: nil,
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /admin with nil handler: want 404, got %d", w.Code)
	}
}

// TestServer_AdminMountDoesNotPerturbExistingRoutes is the D-15/D-16 regression
// invariant: mounting /admin must NOT alter the status code, content-type, or
// body shape of any pre-existing exempt route.
func TestServer_AdminMountDoesNotPerturbExistingRoutes(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Baseline server: no AdminHandler.
	base := newAdminTestServer(t, server.Config{
		AdminHandler: nil,
	})

	// Admin-mounted server: same config but with AdminHandler set.
	withAdmin := newAdminTestServer(t, server.Config{
		AdminHandler: stubAdminHandler,
	})

	exemptRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/"},
		{http.MethodGet, "/health"},
		{http.MethodGet, "/health/agents"},
		{http.MethodGet, "/api/version"},
	}

	for _, route := range exemptRoutes {
		t.Run(route.path, func(t *testing.T) {
			// Request through baseline server.
			rBase := httptest.NewRequestWithContext(context.Background(), route.method, route.path, nil)
			wBase := httptest.NewRecorder()
			base.ServeHTTP(wBase, rBase)

			// Request through admin-mounted server.
			rWith := httptest.NewRequestWithContext(context.Background(), route.method, route.path, nil)
			wWith := httptest.NewRecorder()
			withAdmin.ServeHTTP(wWith, rWith)

			// Status codes must match.
			if wBase.Code != wWith.Code {
				t.Errorf("%s %s: baseline status %d != admin-mounted status %d (D-16 regression)",
					route.method, route.path, wBase.Code, wWith.Code)
			}

			// Content-Type must match.
			baseContentType := wBase.Header().Get("Content-Type")
			withContentType := wWith.Header().Get("Content-Type")
			if baseContentType != withContentType {
				t.Errorf("%s %s: baseline Content-Type %q != admin-mounted %q",
					route.method, route.path, baseContentType, withContentType)
			}

			// For JSON endpoints (/health, /health/agents, /api/version),
			// assert structural equality of the decoded body.
			if strings.HasPrefix(baseContentType, "application/json") {
				var baseBody, withBody map[string]json.RawMessage
				if err := json.NewDecoder(wBase.Body).Decode(&baseBody); err != nil {
					t.Fatalf("decode baseline %s body: %v", route.path, err)
				}
				if err := json.NewDecoder(wWith.Body).Decode(&withBody); err != nil {
					t.Fatalf("decode admin-mounted %s body: %v", route.path, err)
				}
				// Top-level JSON keys must be identical.
				for k := range baseBody {
					if _, ok := withBody[k]; !ok {
						t.Errorf("%s %s: key %q present in baseline but missing from admin-mounted response",
							route.method, route.path, k)
					}
				}
				for k := range withBody {
					if _, ok := baseBody[k]; !ok {
						t.Errorf("%s %s: key %q present in admin-mounted but missing from baseline response",
							route.method, route.path, k)
					}
				}
			}
		})
	}
}

// TestServer_AdminAuthExempt verifies D-01: GET /admin succeeds WITHOUT
// an Authorization header even when AUTH_TOKEN is set on the server.
func TestServer_AdminAuthExempt(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := newAdminTestServer(t, server.Config{
		AuthTokens:   []string{"secret"},
		AdminHandler: stubAdminHandler,
	})

	// Request WITHOUT any authorization header.
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("GET /admin without auth header: want 200, got %d (should be auth-exempt per D-01)",
			w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "admin-stub") {
		t.Errorf("GET /admin: body want 'admin-stub', got %q", body)
	}
}

// TestServer_AdminDoesNotInterceptSurfaces verifies that with both AdminHandler
// and a /v1 SurfaceMount registered, POST /v1/chat/completions routes to the
// surface, not the admin handler.
func TestServer_AdminDoesNotInterceptSurfaces(t *testing.T) {
	defer goleak.VerifyNone(t)

	// A stub surface that 200s with "surface-stub" body.
	surfaceStub := stubRouteRegistrar{fn: func(r chi.Router) {
		r.Post("/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("surface-stub"))
		})
	}}

	srv := server.NewFromConfig(server.Config{
		Logger:  testutil.Logger(t),
		Version: "test",
		Surfaces: []server.SurfaceMount{
			{Prefix: "/api", Router: stubOllamaRegistrar()},
			{Prefix: "/v1", Router: surfaceStub},
		},
		OllamaVersionHandler: stubVersionHandler(),
		OllamaVersionPath:    "/api/version",
		AdminHandler:         stubAdminHandler,
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("POST /v1/chat/completions: want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if body != "surface-stub" {
		t.Errorf("POST /v1/chat/completions: body want 'surface-stub', got %q (admin must NOT intercept surfaces)", body)
	}
}
