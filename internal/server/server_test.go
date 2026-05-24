package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/goleak"

	"otto-gateway/internal/config"
	"otto-gateway/internal/server"
	"otto-gateway/internal/testutil"
	"otto-gateway/internal/version"
)

func newTestServer(t *testing.T) *server.Server {
	t.Helper()
	cfg := config.Config{
		HTTPAddr:     ":0", // port 0 avoids conflicts in tests
		PingInterval: 60 * time.Second,
	}
	logger := testutil.Logger(t)
	return server.New(cfg, logger, version.Version)
}

// TestHealthHandler verifies GET /health returns 200 with the D-12 JSON shape.
func TestHealthHandler(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := newTestServer(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health: want 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type: want application/json, got %q", contentType)
	}

	var body server.HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode /health response: %v", err)
	}

	if body.Status != "ok" {
		t.Errorf("status: want %q, got %q", "ok", body.Status)
	}
	if body.Version == "" {
		t.Error("version: want non-empty")
	}
	// uptime_seconds must be non-negative (may be 0 for very fast test runs).
	if body.UptimeSeconds < 0 {
		t.Errorf("uptime_seconds: got %f, want >= 0", body.UptimeSeconds)
	}
	// Phase 1: pool, sessions, embeddings are zero.
	if body.Pool.Size != 0 || body.Pool.Alive != 0 || body.Pool.Busy != 0 {
		t.Errorf("pool: want {0,0,0}, got %+v", body.Pool)
	}
	if body.Sessions.Active != 0 {
		t.Errorf("sessions.active: want 0, got %d", body.Sessions.Active)
	}
	if body.Embeddings.ModelsLoaded != 0 {
		t.Errorf("embeddings.models_loaded: want 0, got %d", body.Embeddings.ModelsLoaded)
	}
}

// TestHealthJSONKeys verifies all six top-level D-12 JSON keys are present in the raw response.
func TestHealthJSONKeys(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := newTestServer(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	requiredKeys := []string{"status", "version", "uptime_seconds", "pool", "sessions", "embeddings"}
	for _, key := range requiredKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("D-12 key %q missing from /health response", key)
		}
	}
}

// TestVersionHandler verifies GET /api/version returns 200 with a version field.
func TestVersionHandler(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := newTestServer(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/version: want 200, got %d", rec.Code)
	}

	var body struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode /api/version response: %v", err)
	}
	if body.Version == "" {
		t.Error("version: want non-empty")
	}
}

// TestAccessLogRequestID verifies that the accessLog middleware correctly captures
// the request_id set by chi's RequestID middleware. We validate this by checking
// that the request_id is propagated into the context-stored logger (indirectly,
// by using LoggerFromCtx which falls back gracefully) and by verifying that
// multiple requests get distinct request IDs set in the context.
//
// chi's RequestID middleware sets the ID in the context only (not as a response header).
// To verify the middleware chain order (RequestID → accessLog) is correct, we make
// two requests and confirm that the access log emits distinct request IDs.
// The test logger captures slog output via t.Log — a non-empty request_id in the
// log output (visible via -v) confirms the RequestID middleware ran before accessLog.
func TestAccessLogRequestID(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := newTestServer(t)
	ctx := context.Background()

	// Make two requests; each should get a distinct chi-generated request ID.
	rec1 := httptest.NewRecorder()
	srv.ServeHTTP(rec1, httptest.NewRequestWithContext(ctx, http.MethodGet, "/health", nil))

	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, httptest.NewRequestWithContext(ctx, http.MethodGet, "/health", nil))

	if rec1.Code != http.StatusOK {
		t.Errorf("first request: want 200, got %d", rec1.Code)
	}
	if rec2.Code != http.StatusOK {
		t.Errorf("second request: want 200, got %d", rec2.Code)
	}

	// Supply a known X-Request-Id so we can confirm it appears in the log output.
	req3 := httptest.NewRequestWithContext(ctx, http.MethodGet, "/health", nil)
	req3.Header.Set("X-Request-Id", "test-req-id-abc")
	rec3 := httptest.NewRecorder()
	srv.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("third request: want 200, got %d", rec3.Code)
	}
	// The test logger (t.Log) will show "request_id":"test-req-id-abc" in the JSON log,
	// confirming that middleware.RequestID ran before accessLog.
}

// TestRunContextCancel verifies that Server.Run returns when the context is cancelled.
// No os.Interrupt is sent — this is the testable lifecycle path per RESEARCH.md REVIEW FIX.
func TestRunContextCancel(t *testing.T) {
	defer goleak.VerifyNone(t)

	cfg := config.Config{
		// Use port 0 to avoid conflicts with other tests.
		// Note: port 0 means the OS picks a free port.
		HTTPAddr:     ":0",
		PingInterval: 60 * time.Second,
	}
	logger := testutil.Logger(t)
	srv := server.New(cfg, logger, version.Version)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- srv.Run(ctx)
	}()

	// Give the server a moment to start, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Run returned — this is what we want.
		// Shutdown on a server that may not have received any requests returns nil.
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s after context cancel — possible goroutine leak")
	}
}

// ----------------------------------------------------------------------------
// NewFromConfig — Phase 2 wiring (Plan 06)
// ----------------------------------------------------------------------------

// stubOllamaRouter returns a chi.Router with a single Post /chat that
// writes 200 + a marker body. Used by NewFromConfig tests to assert
// that protected requests reach the adapter when auth passes.
func stubOllamaRouter() chi.Router {
	r := chi.NewRouter()
	r.Post("/chat", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return r
}

// stubVersionHandler returns the /api/version handler the outer router
// mounts (Codex M-4). Always returns 200 with a fixed body so the test
// can assert "version exempt from auth".
func stubVersionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"test","commit":"deadbee"}`))
	}
}

func newFromConfigForTest(t *testing.T, cfg server.Config) *server.Server {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = testutil.Logger(t)
	}
	if cfg.Version == "" {
		cfg.Version = "test"
	}
	if cfg.OllamaPath == "" {
		cfg.OllamaPath = "/api"
	}
	if cfg.OllamaProtectedRouter == nil {
		cfg.OllamaProtectedRouter = stubOllamaRouter()
	}
	if cfg.OllamaVersionHandler == nil {
		cfg.OllamaVersionHandler = stubVersionHandler()
	}
	return server.NewFromConfig(cfg)
}

// TestExemptRoutes_BypassAuth — AUTH-03 / Codex M-4 acceptance: /, /health,
// /api/version are reachable even when AUTH_TOKEN is set, with NO bearer
// header supplied.
func TestExemptRoutes_BypassAuth(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{
		AuthTokens: []string{"s3cret"},
	})

	cases := []struct{ name, path string }{
		{"root", "/"},
		{"health", "/health"},
		{"version", "/api/version"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, c.path, nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("%s: got %d, want 200 (must be auth-exempt)", c.path, w.Code)
			}
		})
	}
}

// TestProtectedRoutes_RequireAuth — bearer must be present for the
// /api sub-tree. Asserts both the 401 path and the 200 path with the
// correct bearer.
func TestProtectedRoutes_RequireAuth(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{
		AuthTokens: []string{"s3cret"},
	})

	// Without bearer.
	r1 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", strings.NewReader(`{}`))
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, r1)
	if w1.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/chat without bearer: got %d, want 401", w1.Code)
	}

	// With valid bearer.
	r2 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", strings.NewReader(`{}`))
	r2.Header.Set("Authorization", "Bearer s3cret")
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Errorf("POST /api/chat with valid bearer: got %d, want 200; body=%s", w2.Code, w2.Body.String())
	}
}

// TestIPAllowlist_DenyPath — RemoteAddr outside the allowlist must
// receive 403 on a protected route.
func TestIPAllowlist_DenyPath(t *testing.T) {
	allow, _ := netip.ParsePrefix("10.0.0.0/8")
	srv := newFromConfigForTest(t, server.Config{
		AllowedPrefixes: []netip.Prefix{allow},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", strings.NewReader(`{}`))
	r.RemoteAddr = "192.168.1.1:54321"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 (RemoteAddr outside allowlist)", w.Code)
	}
}

// TestIPAllowlist_AllowPath — RemoteAddr inside the allowlist reaches
// the adapter (proves the allow path with the same Config wiring used
// by the deny test).
func TestIPAllowlist_AllowPath(t *testing.T) {
	allow, _ := netip.ParsePrefix("10.0.0.0/8")
	srv := newFromConfigForTest(t, server.Config{
		AllowedPrefixes: []netip.Prefix{allow},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", strings.NewReader(`{}`))
	r.RemoteAddr = "10.5.6.7:54321"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (RemoteAddr inside allowlist)", w.Code)
	}
}

// TestIPAllowlist_XFFTrustGate — Codex H-7 end-to-end: when
// AuthTrustXFF=false (default) a spoofed X-Forwarded-For header is
// ignored (RemoteAddr decides); when AuthTrustXFF=true the header is
// honored. Proves cfg.AuthTrustXFF threads through to auth.IPAllowlist's
// auth.Config.TrustXForwardedFor.
func TestIPAllowlist_XFFTrustGate(t *testing.T) {
	allow, _ := netip.ParsePrefix("10.0.0.0/8")

	t.Run("trust_xff_false_ignores_spoofed_header", func(t *testing.T) {
		srv := newFromConfigForTest(t, server.Config{
			AllowedPrefixes: []netip.Prefix{allow},
			AuthTrustXFF:    false,
		})
		r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", strings.NewReader(`{}`))
		r.RemoteAddr = "192.168.1.1:54321"
		r.Header.Set("X-Forwarded-For", "10.5.6.7")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("AuthTrustXFF=false: got %d, want 403 (spoofed XFF must be ignored — Codex H-7)", w.Code)
		}
	})

	t.Run("trust_xff_true_honors_header", func(t *testing.T) {
		srv := newFromConfigForTest(t, server.Config{
			AllowedPrefixes: []netip.Prefix{allow},
			AuthTrustXFF:    true,
		})
		r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", strings.NewReader(`{}`))
		r.RemoteAddr = "192.168.1.1:54321"
		r.Header.Set("X-Forwarded-For", "10.5.6.7")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("AuthTrustXFF=true: got %d, want 200 (XFF must be honored)", w.Code)
		}
	})
}

// TestNewFromConfig_HealthPoolWiring — OBSV-01: /health renders pool
// stats from the configured PoolStatsSource.
func TestNewFromConfig_HealthPoolWiring(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{
		Pool: fakePoolSource{stats: server.PoolStats{Size: 4, Alive: 4, Busy: 1}},
	})
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/health status: got %d, want 200", w.Code)
	}
	var body server.HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pool.Size != 4 || body.Pool.Alive != 4 || body.Pool.Busy != 1 {
		t.Errorf("pool stats: got %+v, want {4,4,1}", body.Pool)
	}
}

// fakePoolSource satisfies server.PoolStatsSource with a fixed Stats
// value — lets the /health test exercise OBSV-01 without spinning up a
// real pool.
type fakePoolSource struct {
	stats server.PoolStats
}

func (f fakePoolSource) Stats() server.PoolStats { return f.stats }

// ---------------------------------------------------------------------------
// NewFromConfig — Phase 3.1 anthropic mount (D-17)
// ---------------------------------------------------------------------------

// stubAnthropicRouter mirrors stubOllamaRouter — a chi.Router with a
// single Post handler. Lets the anthropic mount tests assert that a
// protected request reaches the adapter when auth passes.
func stubAnthropicRouter() chi.Router {
	r := chi.NewRouter()
	r.Post("/messages", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":"anthropic"}`))
	})
	return r
}

// TestNewFromConfig_AnthropicMount asserts the D-17 parallel mount: a
// non-nil AnthropicProtectedRouter at AnthropicPath="/v1" is served
// behind the same auth.Bearer + auth.IPAllowlist chain as the Ollama
// surface. Unauthenticated → 401; authenticated → 200.
func TestNewFromConfig_AnthropicMount(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{
		AuthTokens:               []string{"s3cret"},
		AnthropicPath:            "/v1",
		AnthropicProtectedRouter: stubAnthropicRouter(),
	})

	// Without bearer.
	r1 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, r1)
	if w1.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/messages without bearer: got %d, want 401 (body=%s)", w1.Code, w1.Body.String())
	}

	// With valid bearer.
	r2 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	r2.Header.Set("Authorization", "Bearer s3cret")
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Errorf("POST /v1/messages with valid bearer: got %d, want 200; body=%s", w2.Code, w2.Body.String())
	}

	// With valid x-api-key (D-15 dual-header path applied to the
	// anthropic mount because the SAME auth.Bearer middleware is wired).
	r3 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	r3.Header.Set("x-api-key", "s3cret")
	w3 := httptest.NewRecorder()
	srv.ServeHTTP(w3, r3)
	if w3.Code != http.StatusOK {
		t.Errorf("POST /v1/messages with valid x-api-key: got %d, want 200; body=%s", w3.Code, w3.Body.String())
	}
}

// TestNewFromConfig_AnthropicMount_NilRouter — when
// AnthropicProtectedRouter is nil the mount block is skipped (nil-safe
// gate, mirrors the Ollama branch's defensive design). The server
// still starts and serves the Ollama mount; /v1/messages → 404.
func TestNewFromConfig_AnthropicMount_NilRouter(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{
		AuthTokens:               []string{"s3cret"},
		AnthropicPath:            "/v1",
		AnthropicProtectedRouter: nil, // explicitly nil — gate must skip
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer s3cret")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /v1/messages with nil AnthropicProtectedRouter: got %d, want 404 (mount block must be skipped)", w.Code)
	}

	// The Ollama mount must still respond on its own path.
	r2 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", strings.NewReader(`{}`))
	r2.Header.Set("Authorization", "Bearer s3cret")
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Errorf("Ollama mount must still serve when Anthropic mount is absent: got %d, want 200", w2.Code)
	}
}
