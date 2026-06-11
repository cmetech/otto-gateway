package server_test

// Status-enum coverage for PoolStats.Status (D-05 / Plan 16-02 Task 3).
//
// healthHandler computes Status using the priority order from
// 16-CONTEXT.md / 16-PATTERNS.md:
//   1. "exhausted" — pool.IsExhausted() returns true (D-05b)
//   2. "degraded" — Busy == Alive == Size AND time.Since(LastProgressAt) > 30s (D-05a)
//   3. "ok"       — all other cases
//
// Plan 16-04 Task 2 (T-5) consumes this enum via the /health JSON;
// the tray-side regression test for T-5 lives there. This file proves
// the Status field renders correctly on the server side.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"otto-gateway/internal/server"
)

// statusPoolSource is a configurable PoolStatsSource for status-enum
// coverage. Each field maps directly to one of the three status rules.
type statusPoolSource struct {
	stats          server.PoolStats
	exhausted      bool
	lastProgressAt time.Time
}

func (s statusPoolSource) Stats() server.PoolStats   { return s.stats }
func (s statusPoolSource) IsExhausted() bool         { return s.exhausted }
func (s statusPoolSource) LastProgressAt() time.Time { return s.lastProgressAt }

// healthRes parses just the PoolStats sub-tree from GET /health so the
// test does not depend on every other field of HealthResponse.
type healthRes struct {
	Pool server.PoolStats `json:"pool"`
}

func getHealthPool(t *testing.T, srv *server.Server) server.PoolStats {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/health status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var hr healthRes
	if err := json.NewDecoder(w.Body).Decode(&hr); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	return hr.Pool
}

// TestHealth_PoolStatus_Exhausted — when IsExhausted() returns true the
// Status is "exhausted" regardless of Busy/Alive/Size or LastProgressAt.
func TestHealth_PoolStatus_Exhausted(t *testing.T) {
	src := statusPoolSource{
		stats:          server.PoolStats{Size: 4, Alive: 4, Busy: 4},
		exhausted:      true,
		lastProgressAt: time.Now(), // recent progress — must be overridden
	}
	srv := newFromConfigForTest(t, server.Config{Pool: src})
	got := getHealthPool(t, srv)
	if got.Status != "exhausted" {
		t.Errorf("Status: got %q, want %q", got.Status, "exhausted")
	}
}

// TestHealth_PoolStatus_Degraded — Busy == Alive == Size AND last
// progress > 30s ago → "degraded".
func TestHealth_PoolStatus_Degraded(t *testing.T) {
	src := statusPoolSource{
		stats:          server.PoolStats{Size: 4, Alive: 4, Busy: 4},
		exhausted:      false,
		lastProgressAt: time.Now().Add(-31 * time.Second),
	}
	srv := newFromConfigForTest(t, server.Config{Pool: src})
	got := getHealthPool(t, srv)
	if got.Status != "degraded" {
		t.Errorf("Status: got %q, want %q", got.Status, "degraded")
	}
}

// TestHealth_PoolStatus_OK_RecentProgress — Busy == Alive == Size but
// progress is recent → "ok".
func TestHealth_PoolStatus_OK_RecentProgress(t *testing.T) {
	src := statusPoolSource{
		stats:          server.PoolStats{Size: 4, Alive: 4, Busy: 4},
		exhausted:      false,
		lastProgressAt: time.Now().Add(-5 * time.Second),
	}
	srv := newFromConfigForTest(t, server.Config{Pool: src})
	got := getHealthPool(t, srv)
	if got.Status != "ok" {
		t.Errorf("Status: got %q, want %q", got.Status, "ok")
	}
}

// TestHealth_PoolStatus_OK_HasFreeSlot — Busy < Size → "ok" even when
// progress is stale (degraded rule requires fully saturated pool).
func TestHealth_PoolStatus_OK_HasFreeSlot(t *testing.T) {
	src := statusPoolSource{
		stats:          server.PoolStats{Size: 4, Alive: 4, Busy: 2},
		exhausted:      false,
		lastProgressAt: time.Now().Add(-5 * time.Minute), // stale, but pool has free slots
	}
	srv := newFromConfigForTest(t, server.Config{Pool: src})
	got := getHealthPool(t, srv)
	if got.Status != "ok" {
		t.Errorf("Status: got %q, want %q", got.Status, "ok")
	}
}

// TestHealth_PoolStatus_NilPool — when no pool is wired the Status is
// the zero value "" (empty string). Backward-compatible — existing
// callers reading the legacy {size,alive,busy} shape are unaffected.
func TestHealth_PoolStatus_NilPool(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{Pool: nil})
	got := getHealthPool(t, srv)
	if got.Status != "" {
		t.Errorf("Status (nil pool): got %q, want \"\"", got.Status)
	}
}
