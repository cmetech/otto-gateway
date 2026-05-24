package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"loop24-gateway/internal/config"
	"loop24-gateway/internal/testutil"
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
