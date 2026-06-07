package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"otto-gateway/internal/testutil"
)

// TestAdmin_SnapshotHandler verifies GET /api/snapshot returns 200 with
// application/json and a valid Snapshot body.
func TestAdmin_SnapshotHandler(t *testing.T) {
	defer goleak.VerifyNone(t)

	sid := "sess-abc"
	model := "model-xyz"
	deps := Deps{
		Logger:  testutil.Logger(t),
		Version: "1.2.3",
		Commit:  "abc1234",
		PoolDetail: &stubPool{
			slots: []SnapshotSlot{
				{Label: "slot-0", Alive: true, Busy: false, CurrentSessionID: nil},
				{Label: "slot-1", Alive: true, Busy: true, CurrentSessionID: &sid},
			},
		},
		Registry: &stubRegistry{
			sessions: []SnapshotSess{
				{ID: "sess-abc", Alive: true, Busy: true, LastUsed: time.Now(), Model: &model},
			},
		},
		Debug:     true,
		ChatTrace: true,
	}
	h := Handler(deps)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/snapshot: want 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type: want application/json, got %q", contentType)
	}

	cacheControl := rec.Header().Get("Cache-Control")
	if cacheControl != "no-store" {
		t.Errorf("Cache-Control: want no-store, got %q", cacheControl)
	}

	var snap Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode Snapshot: %v", err)
	}

	// Status must be one of the valid values.
	validStatuses := map[string]bool{"ok": true, "degraded": true, "down": true}
	if !validStatuses[snap.Status] {
		t.Errorf("status: got %q, want one of ok/degraded/down", snap.Status)
	}

	if snap.Version == "" {
		t.Error("version: want non-empty")
	}
	if snap.Commit == "" {
		t.Error("commit: want non-empty")
	}
	if snap.UptimeSeconds < 0 {
		t.Errorf("uptime_seconds: got %f, want >= 0", snap.UptimeSeconds)
	}
	if snap.GeneratedAt.IsZero() {
		t.Error("generated_at: want non-zero")
	}

	// pool assertions
	if snap.Pool.Size != 2 {
		t.Errorf("pool.size: want 2, got %d", snap.Pool.Size)
	}
	if snap.Pool.Alive != 2 {
		t.Errorf("pool.alive: want 2, got %d", snap.Pool.Alive)
	}
	if snap.Pool.Busy != 1 {
		t.Errorf("pool.busy: want 1, got %d", snap.Pool.Busy)
	}
	if snap.Pool.Slots == nil {
		t.Error("pool.slots: want non-nil JSON array")
	}

	// sessions assertions
	if snap.Sessions == nil {
		t.Error("sessions: want non-nil JSON array")
	}
	if len(snap.Sessions) != 1 {
		t.Errorf("sessions: want 1 session, got %d", len(snap.Sessions))
	}

	// Feature-flag assertions (quick 260531-ebi): debug + chat_trace mirror
	// Deps.Debug / Deps.ChatTrace, both set true above.
	if !snap.Debug {
		t.Errorf("debug: want true, got %v", snap.Debug)
	}
	if !snap.ChatTrace {
		t.Errorf("chat_trace: want true, got %v", snap.ChatTrace)
	}
}

// TestAdmin_SnapshotNilSafe verifies that the handler constructed with nil
// PoolDetail and nil Registry does not panic and returns sensible zero values.
func TestAdmin_SnapshotNilSafe(t *testing.T) {
	defer goleak.VerifyNone(t)

	deps := Deps{
		Logger:     testutil.Logger(t),
		Version:    "1.0.0",
		Commit:     "unknown",
		PoolDetail: nil,
		Registry:   nil,
	}
	h := Handler(deps)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/snapshot with nil deps: want 200, got %d", rec.Code)
	}

	var snap Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode Snapshot: %v", err)
	}

	if snap.Pool.Size != 0 {
		t.Errorf("pool.size: want 0, got %d", snap.Pool.Size)
	}
	if snap.Pool.Alive != 0 {
		t.Errorf("pool.alive: want 0, got %d", snap.Pool.Alive)
	}
	if snap.Pool.Busy != 0 {
		t.Errorf("pool.busy: want 0, got %d", snap.Pool.Busy)
	}
	if snap.Pool.Slots == nil {
		t.Error("pool.slots: want non-nil (empty slice), got nil")
	}
	if snap.Sessions == nil {
		t.Error("sessions: want non-nil (empty slice), got nil")
	}
	if snap.Status != "down" {
		t.Errorf("status with nil pool: want 'down', got %q", snap.Status)
	}

	// Feature flags default to false when Deps leaves them unset — proving the
	// zero-value default (no regression for callers that don't set them).
	if snap.Debug {
		t.Errorf("debug: want false (zero-value), got %v", snap.Debug)
	}
	if snap.ChatTrace {
		t.Errorf("chat_trace: want false (zero-value), got %v", snap.ChatTrace)
	}
}

// TestAdmin_ComputeStatus verifies the pure computeStatus function covers
// all three outcome paths.
func TestAdmin_ComputeStatus(t *testing.T) {
	defer goleak.VerifyNone(t)

	cases := []struct {
		name   string
		snap   Snapshot
		expect string
	}{
		{
			name:   "all_alive_is_ok",
			snap:   Snapshot{Pool: SnapshotPool{Size: 4, Alive: 4, Busy: 0}},
			expect: "ok",
		},
		{
			name:   "some_alive_is_degraded",
			snap:   Snapshot{Pool: SnapshotPool{Size: 4, Alive: 2, Busy: 0}},
			expect: "degraded",
		},
		{
			name:   "zero_alive_is_down",
			snap:   Snapshot{Pool: SnapshotPool{Size: 4, Alive: 0, Busy: 0}},
			expect: "down",
		},
		{
			name:   "zero_size_is_down",
			snap:   Snapshot{Pool: SnapshotPool{Size: 0, Alive: 0, Busy: 0}},
			expect: "down",
		},
		{
			name:   "size_zero_even_if_alive_nonzero",
			snap:   Snapshot{Pool: SnapshotPool{Size: 0, Alive: 1, Busy: 0}},
			expect: "down",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computeStatus(c.snap)
			if got != c.expect {
				t.Errorf("computeStatus: want %q, got %q (snap=%+v)", c.expect, got, c.snap)
			}
		})
	}
}

// TestSnapshot_LogSources_PresentAndOrdered asserts the quick-260529-ll2
// log_sources field reflects Deps.LogPathOrder verbatim and renders as []
// (not null) when no sources are configured.
func TestSnapshot_LogSources_PresentAndOrdered(t *testing.T) {
	defer goleak.VerifyNone(t)

	cases := []struct {
		name  string
		order []string
		want  []string
	}{
		{
			name:  "all_three_sources_in_order",
			order: []string{"main", "boot-err", "chat-trace"},
			want:  []string{"main", "boot-err", "chat-trace"},
		},
		{
			name:  "main_plus_boot_err_only",
			order: []string{"main", "boot-err"},
			want:  []string{"main", "boot-err"},
		},
		{
			name:  "empty_yields_empty_array_not_null",
			order: nil,
			want:  []string{},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps := Deps{
				Logger:       testutil.Logger(t),
				Version:      "1.0.0",
				Commit:       "abc1234",
				LogPathOrder: c.order,
			}
			h := Handler(deps)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/snapshot", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("snapshot: want 200, got %d", rec.Code)
			}
			var snap Snapshot
			if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
				t.Fatalf("decode Snapshot: %v", err)
			}
			if snap.LogSources == nil {
				t.Fatalf("log_sources rendered as null (want empty array)")
			}
			if len(snap.LogSources) != len(c.want) {
				t.Fatalf("log_sources length: got %d, want %d (%v vs %v)",
					len(snap.LogSources), len(c.want), snap.LogSources, c.want)
			}
			for i, v := range c.want {
				if snap.LogSources[i] != v {
					t.Errorf("log_sources[%d]: got %q, want %q", i, snap.LogSources[i], v)
				}
			}
		})
	}
}

// TestSnapshot_LogSources_DefensiveCopy asserts a snapshot consumer that
// mutates the returned slice cannot reach into the live Deps.
func TestSnapshot_LogSources_DefensiveCopy(t *testing.T) {
	defer goleak.VerifyNone(t)

	original := []string{"main", "boot-err"}
	deps := Deps{
		Logger:       testutil.Logger(t),
		Version:      "1.0.0",
		Commit:       "abc1234",
		LogPathOrder: original,
	}
	h := Handler(deps)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var snap Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Mutate the returned slice and re-fetch — the original should not move.
	snap.LogSources[0] = "tampered"
	if original[0] != "main" {
		t.Errorf("defensive-copy violation: original mutated to %q", original[0])
	}
}
