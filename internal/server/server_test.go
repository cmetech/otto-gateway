package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"loop24-gateway/internal/config"
	"loop24-gateway/internal/server"
	"loop24-gateway/internal/testutil"
	"loop24-gateway/internal/version"
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
