package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"otto-gateway/internal/config"
	"otto-gateway/internal/testutil"
)

// TestApp_NoKiroCmd_StartsHealthOnly — when KIRO_CMD is empty, newApp
// succeeds with pool == nil and the server is constructable. /health
// serves 200 with the zero PoolStats envelope. Proves the Phase 1
// review-fix posture (gateway boots without kiro-cli installed).
func TestApp_NoKiroCmd_StartsHealthOnly(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:         ":0",
		KiroCmd:          "", // explicit — Phase 1 review-fix branch
		PoolSize:         1,
		PingInterval:     60 * time.Second,
		OllamaPathPrefix: "/api",
	}
	logger := testutil.Logger(t)

	a, cleanup, err := newApp(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()

	if a.pool != nil {
		t.Errorf("a.pool: got non-nil, want nil (KIRO_CMD unset)")
	}
	if a.engine != nil {
		t.Errorf("a.engine: got non-nil, want nil (no pool means no engine)")
	}
	if a.srv == nil {
		t.Fatal("a.srv: nil — server must be constructable in degraded mode")
	}

	// /health must serve 200 even without a pool.
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	a.srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("/health status: got %d, want 200 (degraded mode)", w.Code)
	}
}

// TestNewApp_SurfaceGating — Phase 3.1 Plan 04 Task 1 (B3 closure).
//
// Verifies that ENABLED_SURFACES controls which adapter routes the
// gateway mounts. Three env permutations are exercised under the
// degraded `KIRO_CMD=""` posture (pool + engine are nil, warmup is
// skipped entirely — mirrors TestApp_NoKiroCmd_StartsHealthOnly):
//
//   - Default (ENABLED_SURFACES unset → ollama,anthropic): both
//     /api/chat AND /v1/messages MUST be mounted (probe returns
//     non-404 — in degraded mode, the nil-engine guard returns 503).
//   - OllamaOnly (ENABLED_SURFACES=ollama): /api/chat mounted,
//     /v1/messages route is absent and chi returns 404.
//   - AnthropicOnly (ENABLED_SURFACES=anthropic): /v1/messages
//     mounted, /api/chat absent (404).
//
// The test uses t.Setenv → config.Load → newApp → a.srv.ServeHTTP so
// the env-resolved cfg drives the wiring. AUTH_TOKEN is cleared so
// auth-protected routes return 503 (nil-engine) instead of 401
// (auth-fail), which would also be non-404 but would obscure whether
// the route was actually mounted.
//
// Threat mitigation: T-3.1-WIRE — closes the verification gap that
// previously would only have surfaced in HUMAN-UAT.
func TestNewApp_SurfaceGating(t *testing.T) {
	subtests := []struct {
		name                 string
		enabledSurfaces      string // "" sentinel meaning "do not set ENABLED_SURFACES"
		expectOllamaRoute    bool
		expectAnthropicRoute bool
	}{
		{"Default", "", true, true},
		{"OllamaOnly", "ollama", true, false},
		{"AnthropicOnly", "anthropic", false, true},
	}

	for _, tc := range subtests {
		t.Run(tc.name, func(t *testing.T) {
			// Degraded mode: pool + engine stay nil. Warmup is skipped.
			t.Setenv("KIRO_CMD", "")
			// Defensive — ephemeral port; the test never actually listens.
			t.Setenv("HTTP_ADDR", ":0")
			// No-auth so route probes are not blocked by 401 before
			// reaching the chi router's 404 handler.
			t.Setenv("AUTH_TOKEN", "")
			if tc.enabledSurfaces != "" {
				t.Setenv("ENABLED_SURFACES", tc.enabledSurfaces)
			}

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load: %v", err)
			}
			// Force degraded mode AFTER config.Load: getEnvStr("KIRO_CMD",
			// "kiro-cli") falls back to the default when the env value
			// is empty, so we cannot disable pool construction via env
			// alone. Overriding the resolved Config field is the
			// supported degraded-mode entrypoint (mirrors how
			// TestApp_NoKiroCmd_StartsHealthOnly builds its cfg literal).
			cfg.KiroCmd = ""
			logger := testutil.Logger(t)

			a, cleanup, err := newApp(context.Background(), cfg, logger)
			if err != nil {
				t.Fatalf("newApp: %v", err)
			}
			defer cleanup()
			if a == nil || a.srv == nil {
				t.Fatalf("newApp: nil app or srv")
			}

			probes := []struct {
				path     string
				expected bool // true = mounted (non-404), false = absent (404)
			}{
				{"/api/chat", tc.expectOllamaRoute},
				{"/v1/messages", tc.expectAnthropicRoute},
			}
			for _, p := range probes {
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, p.path, nil)
				w := httptest.NewRecorder()
				a.srv.ServeHTTP(w, req)

				if p.expected {
					// Route mounted — any non-404 proves the path
					// is registered (503 nil-engine, 405 method-not-
					// allowed on GET, etc. all qualify).
					if w.Code == http.StatusNotFound {
						t.Errorf("path %s: got 404, want non-404 (route should be mounted under %s)",
							p.path, tc.name)
					}
				} else {
					// Route absent — chi returns 404 for unmatched paths.
					if w.Code != http.StatusNotFound {
						t.Errorf("path %s: got %d, want 404 (route should NOT be mounted under %s)",
							p.path, w.Code, tc.name)
					}
				}
			}
		})
	}
}

// TestApp_WarmupBeforeListen — when KIRO_CMD is set to a binary that
// CANNOT speak ACP (e.g., /bin/true exits 0 immediately), Warmup MUST
// fail and newApp MUST return an error WITHOUT constructing the
// server. Proves POOL-02 ordering AND the warmup-deadline guard
// (threat T-02-36).
func TestApp_WarmupBeforeListen(t *testing.T) {
	if _, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil); err != nil {
		// Defensive — should not happen, but keep the test honest.
		t.Fatalf("http.NewRequestWithContext: %v", err)
	}

	cfg := config.Config{
		HTTPAddr:         ":0",
		KiroCmd:          "/usr/bin/true", // exists on macOS + Linux; speaks no ACP
		KiroArgs:         []string{},
		PoolSize:         1,
		PingInterval:     60 * time.Second,
		OllamaPathPrefix: "/api",
	}
	logger := testutil.Logger(t)

	// Use a short ctx (5s) so the test bounds itself even if a
	// pathological /usr/bin/true variant somehow accepts the stdin
	// pipe — Initialize will still fail (no agentInitialize response
	// will come back) and pool.Warmup will return an error.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a, cleanup, err := newApp(ctx, cfg, logger)
	defer cleanup()
	if err == nil {
		t.Fatal("newApp returned nil error; expected pool.Warmup to fail against /usr/bin/true (POOL-02 ordering broken)")
	}
	if a != nil {
		t.Error("newApp returned non-nil app on Warmup failure; the server MUST NOT be constructed when warmup fails")
	}
}
