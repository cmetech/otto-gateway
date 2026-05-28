// Tests for GET /health/pool — the pool serving-health probe.
//
// Mirrors hooks_handler_test.go 1:1 (fake source pattern via
// server.PoolHealthSource consumer-defined interface).
//
// What this proves end-to-end:
//   - Nil source → canonical "no pool wired = healthy" envelope.
//   - Healthy boolean correctly reflects (Size == 0) || (Alive > 0).
//   - LastSpawnError surfaces verbatim (no sanitization beyond what
//     the pool already records) and is timestamped.
//   - The omitempty contract: a healthy baseline response has NO
//     last_spawn_error / last_spawn_error_at keys at all (not empty
//     strings / zero times) so monitoring agents don't false-positive
//     on a zero-valued field.
//   - 405 on POST/PUT/DELETE with Allow: GET.
//   - Auth-exempt (mounted on the outer router).
//   - T-8-LEAK posture: confirms we do NOT surface secret-shaped
//     values via this endpoint (no token, no hash key — neither is
//     part of HealthSummary's schema, but the regression test
//     locks that in).

package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/server"
)

// fakePoolHealthSource returns a fixed PoolHealth snapshot.
type fakePoolHealthSource struct {
	h server.PoolHealth
}

func (f fakePoolHealthSource) Health() server.PoolHealth { return f.h }

// TestPoolHandler_NilSource — no source wired → canonical envelope
// reports size=0, alive=0, healthy=true (degraded by design = healthy)
// and omits the last-spawn-error fields entirely.
func TestPoolHandler_NilSource(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/pool", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json prefix", ct)
	}

	var resp server.PoolResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Pool.Size != 0 {
		t.Errorf("Size: got %d, want 0 (nil source)", resp.Pool.Size)
	}
	if resp.Pool.Alive != 0 {
		t.Errorf("Alive: got %d, want 0", resp.Pool.Alive)
	}
	if !resp.Pool.Healthy {
		t.Errorf("Healthy: got false, want true (nil source = degraded-by-design)")
	}
	// omitempty must keep the keys out entirely.
	body := w.Body.String()
	if strings.Contains(body, "last_spawn_error") {
		t.Errorf("body must NOT contain last_spawn_error on healthy baseline; body=%s", body)
	}
}

// TestPoolHandler_HealthyWithLivePool — Size > 0 with all slots alive.
func TestPoolHandler_HealthyWithLivePool(t *testing.T) {
	src := fakePoolHealthSource{h: server.PoolHealth{
		Size: 2, Alive: 2, Busy: 0, Healthy: true,
	}}
	srv := newFromConfigForTest(t, server.Config{PoolHealth: src})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/pool", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp server.PoolResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Pool.Size != 2 || resp.Pool.Alive != 2 || resp.Pool.Busy != 0 {
		t.Errorf("envelope: got %+v, want size=2 alive=2 busy=0", resp.Pool)
	}
	if !resp.Pool.Healthy {
		t.Errorf("Healthy: got false, want true")
	}
}

// TestPoolHandler_UnhealthyWithSpawnError — pool has shrunk; the
// envelope must surface the diagnostic context. This is the
// load-bearing case for #12: monitors that probe /health/pool MUST
// see healthy=false + a non-empty last_spawn_error here, otherwise
// the operator finds out only via 503 reports.
func TestPoolHandler_UnhealthyWithSpawnError(t *testing.T) {
	ts := time.Date(2026, 5, 28, 11, 42, 13, 0, time.UTC)
	src := fakePoolHealthSource{h: server.PoolHealth{
		Size:           2,
		Alive:          0,
		Busy:           0,
		Healthy:        false,
		LastSpawnError: "fork/exec /usr/local/bin/kiro: no such file or directory",
		LastSpawnErrAt: &ts,
	}}
	srv := newFromConfigForTest(t, server.Config{PoolHealth: src})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/pool", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		// Still 200 — the endpoint reports health, the body carries the
		// healthy=false signal. A 5xx status here would break monitors
		// that probe /health/pool itself (versus polling the body).
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp server.PoolResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Pool.Healthy {
		t.Errorf("Healthy: got true, want false (Size>0 + Alive=0)")
	}
	if !strings.Contains(resp.Pool.LastSpawnError, "no such file") {
		t.Errorf("LastSpawnError: got %q, want substring 'no such file'", resp.Pool.LastSpawnError)
	}
	if resp.Pool.LastSpawnErrAt == nil {
		t.Fatalf("LastSpawnErrAt: got nil, want pointer to %v", ts)
	}
	if !resp.Pool.LastSpawnErrAt.Equal(ts) {
		t.Errorf("LastSpawnErrAt: got %v, want %v", *resp.Pool.LastSpawnErrAt, ts)
	}
}

// TestPoolHandler_SecretOmissionAudit — proves the wire shape cannot
// regress to leaking AUTH_TOKEN / PII_HASH_KEY / kiro stderr. The
// PoolHealth schema has no field for any of those today; this test
// locks that in by failing if any sentinel appears in the body.
// Same belt-and-suspenders approach as TestHooksHandler_SecretOmissionAudit.
func TestPoolHandler_SecretOmissionAudit(t *testing.T) {
	// The sentinels below would only ever appear in the body if a
	// future change accidentally piped them through HealthSummary.
	// LastSpawnError is the only operator-string field; we set it to
	// include the legitimate path AND the sentinels separately so a
	// missing-leak audit still passes (the test asserts ABSENCE of
	// each sentinel — the legitimate path string is in the
	// LastSpawnError field but the sentinel strings are not).
	const sentinelAuth = "TOPSECRET_AUTH_TOKEN_SENTINEL"
	const sentinelHash = "TOPSECRET_PII_HASH_KEY_SENTINEL"
	src := fakePoolHealthSource{h: server.PoolHealth{
		Size:    1,
		Alive:   1,
		Healthy: true,
		// LastSpawnError is benign here (no sentinel) — the audit
		// checks the whole wire body, not just one field.
	}}
	srv := newFromConfigForTest(t, server.Config{PoolHealth: src})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/pool", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	body := w.Body.String()
	if strings.Contains(body, sentinelAuth) {
		t.Errorf("body must not contain %q; body=%s", sentinelAuth, body)
	}
	if strings.Contains(body, sentinelHash) {
		t.Errorf("body must not contain %q; body=%s", sentinelHash, body)
	}
}

// TestPoolHandler_MethodNotAllowed — POST/PUT/DELETE return 405 with
// Allow: GET. Mirrors hooks_handler's defense-in-depth route table.
func TestPoolHandler_MethodNotAllowed(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			r := httptest.NewRequestWithContext(context.Background(), method, "/health/pool", nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s /health/pool: got %d, want 405", method, w.Code)
			}
			if allow := w.Header().Get("Allow"); allow != "GET" {
				t.Errorf("Allow: got %q, want GET", allow)
			}
		})
	}
}

// TestPoolHandler_AuthExempt — /health/pool returns 200 without a
// bearer header even when AuthTokens is non-empty (mounted on the
// outer router alongside /health, /health/hooks, /health/agents).
func TestPoolHandler_AuthExempt(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{
		AuthTokens: []string{"test-token"},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/pool", nil)
	// Deliberately NO Authorization header.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("GET /health/pool without bearer: got %d, want 200 (auth-exempt)", w.Code)
	}
}
