package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"otto-gateway/internal/server"
)

// stubMetricsHandler stands in for the real /metrics handler so these tests
// exercise ROUTING + the allowlist gate, not exposition content.
var stubMetricsHandler http.HandlerFunc = func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("metrics-stub"))
}

// TestServer_MetricsRoute_Served: with MetricsHandler set and no allowlist,
// GET /metrics routes to the handler.
func TestServer_MetricsRoute_Served(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := newAdminTestServer(t, server.Config{MetricsHandler: stubMetricsHandler})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /metrics: want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "metrics-stub") {
		t.Errorf("GET /metrics: body want 'metrics-stub', got %q", w.Body.String())
	}
}

// TestServer_MetricsRoute_AllowlistGates: /metrics sits behind auth.IPAllowlist —
// a request from a non-allowlisted IP is rejected (403), proving it is NOT
// fully-exempt like /health.
func TestServer_MetricsRoute_AllowlistGates(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := newAdminTestServer(t, server.Config{
		MetricsHandler:  stubMetricsHandler,
		AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	})

	// httptest default RemoteAddr is 192.0.2.1 — outside 10.0.0.0/8.
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET /metrics from disallowed IP: want 403, got %d", w.Code)
	}

	// A health endpoint remains exempt (precedent contrast): still 200.
	rh := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	wh := httptest.NewRecorder()
	srv.ServeHTTP(wh, rh)
	if wh.Code != http.StatusOK {
		t.Errorf("GET /health should stay exempt (200), got %d", wh.Code)
	}
}

// TestServer_MetricsRoute_NilHandlerUnrouted: when MetricsHandler is nil,
// /metrics is not registered (404) — opt-in, like AdminHandler.
func TestServer_MetricsRoute_NilHandlerUnrouted(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := newAdminTestServer(t, server.Config{})
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /metrics with nil MetricsHandler: want 404, got %d", w.Code)
	}
}
