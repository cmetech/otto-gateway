package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"otto-gateway/internal/testutil"
)

// stubProc satisfies ProcSampler for tests.
type stubProc struct {
	self    ProcSample
	workers map[string]ProcSample
}

func (s stubProc) Self() ProcSample               { return s.self }
func (s stubProc) Workers() map[string]ProcSample { return s.workers }

// TestAdmin_Snapshot_ProcMerge: the gateway process fields are populated from
// Proc.Self, and each slot's CPU/RSS is merged from Proc.Workers keyed by label.
// A slot with no matching worker sample (or an !OK one) keeps StatOK=false.
func TestAdmin_Snapshot_ProcMerge(t *testing.T) {
	sid := "sess-1"
	deps := Deps{
		Logger:  testutil.Logger(t),
		Version: "1.2.3",
		Commit:  "abc1234",
		PoolDetail: &stubPool{
			slots: []SnapshotSlot{
				{Label: "slot-0", Alive: true},
				{Label: "slot-1", Alive: true, Busy: true, CurrentSessionID: &sid},
				{Label: "slot-2", Alive: true}, // no worker sample → stays n/a
			},
		},
		Proc: stubProc{
			self: ProcSample{CPUSeconds: 42.5, RSSBytes: 84 << 20, OK: true},
			workers: map[string]ProcSample{
				"slot-0": {CPUSeconds: 10, RSSBytes: 200 << 20, OK: true},
				"slot-1": {CPUSeconds: 3, RSSBytes: 190 << 20, OK: true},
				"slot-2": {OK: false}, // unreadable → must not populate
			},
		},
	}
	h := Handler(deps)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	var snap Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Gateway process fields.
	if !snap.ProcessStatOK {
		t.Error("process_stat_ok: want true")
	}
	if snap.ProcessCPUSeconds != 42.5 {
		t.Errorf("process_cpu_seconds: want 42.5, got %v", snap.ProcessCPUSeconds)
	}
	if snap.ProcessRSSBytes != 84<<20 {
		t.Errorf("process_rss_bytes: want %d, got %d", uint64(84<<20), snap.ProcessRSSBytes)
	}

	byLabel := map[string]SnapshotSlot{}
	for _, s := range snap.Pool.Slots {
		byLabel[s.Label] = s
	}
	if s := byLabel["slot-0"]; !s.StatOK || s.CPUSeconds != 10 || s.RSSBytes != 200<<20 {
		t.Errorf("slot-0 merge wrong: %+v", s)
	}
	if s := byLabel["slot-1"]; !s.StatOK || s.CPUSeconds != 3 || s.RSSBytes != 190<<20 {
		t.Errorf("slot-1 merge wrong: %+v", s)
	}
	// slot-2's worker sample was !OK → fields stay zero, StatOK false.
	if s := byLabel["slot-2"]; s.StatOK || s.CPUSeconds != 0 || s.RSSBytes != 0 {
		t.Errorf("slot-2 should be unpopulated (n/a): %+v", s)
	}
}

// TestAdmin_Snapshot_ProcNilSafe: a nil Proc leaves process fields zeroed with
// ProcessStatOK=false and never panics.
func TestAdmin_Snapshot_ProcNilSafe(t *testing.T) {
	deps := Deps{
		Logger:     testutil.Logger(t),
		Version:    "1.2.3",
		Commit:     "abc1234",
		PoolDetail: &stubPool{slots: []SnapshotSlot{{Label: "slot-0", Alive: true}}},
		Proc:       nil,
	}
	h := Handler(deps)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	var snap Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.ProcessStatOK {
		t.Error("process_stat_ok: want false with nil Proc")
	}
	if snap.Pool.Slots[0].StatOK {
		t.Error("slot stat_ok: want false with nil Proc")
	}
}
