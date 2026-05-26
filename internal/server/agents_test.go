package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/server"
	"otto-gateway/internal/testutil"
)

// fakePoolDetailSource satisfies server.PoolDetailSource with a fixed
// []AgentSlot. Used to inject a known per-slot detail vector into the
// agentsHandler unit tests.
type fakePoolDetailSource struct {
	slots []server.AgentSlot
}

func (f fakePoolDetailSource) Detail() []server.AgentSlot { return f.slots }

// fakeRegistryStatsSource satisfies server.RegistryStatsSource with a fixed
// Stats + Detail vector. Tests use this to drive the /health and
// /health/agents sessions sub-trees deterministically.
type fakeRegistryStatsSource struct {
	stats   server.SessionStats
	details []server.AgentSession
}

func (f fakeRegistryStatsSource) Stats() server.SessionStats { return f.stats }
func (f fakeRegistryStatsSource) Detail() []server.AgentSession {
	return f.details
}

// TestAgentsHandler_EmptyPoolAndRegistry — nil pool detail source and nil
// registry produce a 200 with the canonical empty shape: pool zero-value
// and Sessions nil (encoded as `null`).
func TestAgentsHandler_EmptyPoolAndRegistry(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/agents", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /health/agents: want 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}

	var body server.AgentsResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pool.Size != 0 || body.Pool.Alive != 0 || body.Pool.Busy != 0 {
		t.Errorf("pool zero-value: got %+v", body.Pool)
	}
	if body.Pool.Slots != nil {
		t.Errorf("pool.slots: got %+v, want nil", body.Pool.Slots)
	}
	if body.Sessions != nil {
		t.Errorf("sessions: got %+v, want nil", body.Sessions)
	}
}

// TestAgentsHandler_PopulatedPool — a non-nil PoolDetailSource returning
// 4 slots renders Size=4, Alive=4 (all alive), Busy=0 (none busy), and
// 4 rows in pool.slots.
func TestAgentsHandler_PopulatedPool(t *testing.T) {
	slots := []server.AgentSlot{
		{Label: "slot-0", Alive: true, Busy: false},
		{Label: "slot-1", Alive: true, Busy: false},
		{Label: "slot-2", Alive: true, Busy: false},
		{Label: "slot-3", Alive: true, Busy: false},
	}
	srv := newFromConfigForTest(t, server.Config{
		PoolDetail: fakePoolDetailSource{slots: slots},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/agents", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /health/agents: want 200, got %d", w.Code)
	}
	var body server.AgentsResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pool.Size != 4 {
		t.Errorf("pool.size: got %d, want 4", body.Pool.Size)
	}
	if body.Pool.Alive != 4 {
		t.Errorf("pool.alive: got %d, want 4", body.Pool.Alive)
	}
	if body.Pool.Busy != 0 {
		t.Errorf("pool.busy: got %d, want 0", body.Pool.Busy)
	}
	if len(body.Pool.Slots) != 4 {
		t.Fatalf("pool.slots: got %d rows, want 4", len(body.Pool.Slots))
	}
	for i, sl := range body.Pool.Slots {
		wantLabel := "slot-" + string(rune('0'+i))
		if sl.Label != wantLabel {
			t.Errorf("slot[%d].label: got %q, want %q", i, sl.Label, wantLabel)
		}
		if sl.CurrentSessionID != nil {
			t.Errorf("slot[%d].current_session_id: got %v, want nil", i, sl.CurrentSessionID)
		}
	}
}

// TestAgentsHandler_PopulatedSessions — a non-nil RegistryStatsSource
// returning 2 session rows is rendered verbatim in body.Sessions.
func TestAgentsHandler_PopulatedSessions(t *testing.T) {
	modelA := "claude-3-opus"
	t0 := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 5, 26, 10, 5, 0, 0, time.UTC)
	details := []server.AgentSession{
		{ID: "sess-A", Alive: true, Busy: false, LastUsed: t0, Model: &modelA},
		{ID: "sess-B", Alive: true, Busy: true, LastUsed: t1, Model: nil},
	}
	srv := newFromConfigForTest(t, server.Config{
		Registry: fakeRegistryStatsSource{
			stats:   server.SessionStats{Active: 2},
			details: details,
		},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/agents", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /health/agents: want 200, got %d", w.Code)
	}
	var body server.AgentsResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sessions) != 2 {
		t.Fatalf("sessions: got %d rows, want 2", len(body.Sessions))
	}
	if body.Sessions[0].ID != "sess-A" || body.Sessions[1].ID != "sess-B" {
		t.Errorf("session ids: got %+v", body.Sessions)
	}
	if body.Sessions[0].Model == nil || *body.Sessions[0].Model != "claude-3-opus" {
		t.Errorf("sessions[0].model: want claude-3-opus, got %v", body.Sessions[0].Model)
	}
	if body.Sessions[1].Model != nil {
		t.Errorf("sessions[1].model: want nil, got %v", body.Sessions[1].Model)
	}
	if !body.Sessions[1].Busy {
		t.Errorf("sessions[1].busy: want true, got false")
	}
	if !body.Sessions[0].LastUsed.Equal(t0) {
		t.Errorf("sessions[0].last_used: got %v, want %v", body.Sessions[0].LastUsed, t0)
	}
}

// TestAgentsHandler_RowShapeMatchesD15D16 — decode the response into a
// raw map and assert the exact key set for pool/slot/session shapes
// (D-14/D-15/D-16 verbatim).
func TestAgentsHandler_RowShapeMatchesD15D16(t *testing.T) {
	sid := "sess-X"
	slots := []server.AgentSlot{
		{Label: "slot-0", Alive: true, Busy: true, CurrentSessionID: &sid},
	}
	model := "auto"
	details := []server.AgentSession{
		{ID: sid, Alive: true, Busy: true, LastUsed: time.Now(), Model: &model},
	}
	srv := newFromConfigForTest(t, server.Config{
		PoolDetail: fakePoolDetailSource{slots: slots},
		Registry: fakeRegistryStatsSource{
			stats:   server.SessionStats{Active: 1},
			details: details,
		},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/agents", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	// Decode as raw to assert keys at the wire-shape level.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("decode top-level: %v", err)
	}
	if _, ok := raw["pool"]; !ok {
		t.Error("missing top-level key: pool")
	}
	if _, ok := raw["sessions"]; !ok {
		t.Error("missing top-level key: sessions")
	}

	// pool sub-keys.
	var poolRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw["pool"], &poolRaw); err != nil {
		t.Fatalf("decode pool: %v", err)
	}
	wantPoolKeys := []string{"size", "alive", "busy", "slots"}
	for _, k := range wantPoolKeys {
		if _, ok := poolRaw[k]; !ok {
			t.Errorf("pool missing key %q", k)
		}
	}

	// slot row sub-keys.
	var slotRows []map[string]json.RawMessage
	if err := json.Unmarshal(poolRaw["slots"], &slotRows); err != nil {
		t.Fatalf("decode pool.slots: %v", err)
	}
	if len(slotRows) != 1 {
		t.Fatalf("pool.slots: got %d, want 1", len(slotRows))
	}
	wantSlotKeys := []string{"label", "alive", "busy", "current_session_id"}
	for _, k := range wantSlotKeys {
		if _, ok := slotRows[0][k]; !ok {
			t.Errorf("slot row missing key %q", k)
		}
	}

	// session row sub-keys.
	var sessRows []map[string]json.RawMessage
	if err := json.Unmarshal(raw["sessions"], &sessRows); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(sessRows) != 1 {
		t.Fatalf("sessions: got %d, want 1", len(sessRows))
	}
	wantSessKeys := []string{"id", "alive", "busy", "last_used", "model"}
	for _, k := range wantSessKeys {
		if _, ok := sessRows[0][k]; !ok {
			t.Errorf("session row missing key %q", k)
		}
	}
}

// TestAgentsHandler_NoAuthRequired (D-18) — /health/agents must be
// exempt from auth even when AUTH_TOKEN is set. No bearer header
// provided → 200.
func TestAgentsHandler_NoAuthRequired(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{
		AuthTokens: []string{"secret"},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/agents", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("GET /health/agents without bearer: got %d, want 200 (D-18 exempt)", w.Code)
	}
}

// TestHealthHandler_PopulatesSessionsActive — Phase 5: /health populates
// sessions.active from the configured RegistryStatsSource.
func TestHealthHandler_PopulatesSessionsActive(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{
		Registry: fakeRegistryStatsSource{
			stats: server.SessionStats{Active: 3},
		},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /health: want 200, got %d", w.Code)
	}
	var body server.HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Sessions.Active != 3 {
		t.Errorf("sessions.active: got %d, want 3", body.Sessions.Active)
	}
}

// TestAgentsHandler_LastUsedIsRFC3339 — sessions[].last_used must parse
// cleanly via time.Parse(time.RFC3339Nano, value). Stdlib's default
// time.Time MarshalJSON emits RFC 3339 with sub-second precision.
func TestAgentsHandler_LastUsedIsRFC3339(t *testing.T) {
	now := time.Now().UTC()
	details := []server.AgentSession{
		{ID: "s1", Alive: true, LastUsed: now},
	}
	srv := newFromConfigForTest(t, server.Config{
		Registry: fakeRegistryStatsSource{details: details},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/agents", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	// Decode as raw to grab the raw RFC 3339 string.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var sessions []map[string]json.RawMessage
	if err := json.Unmarshal(raw["sessions"], &sessions); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions: got %d, want 1", len(sessions))
	}
	var lastUsedStr string
	if err := json.Unmarshal(sessions[0]["last_used"], &lastUsedStr); err != nil {
		t.Fatalf("decode last_used: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, lastUsedStr)
	if err != nil {
		t.Fatalf("time.Parse(RFC3339Nano, %q): %v", lastUsedStr, err)
	}
	// Verify the parsed time round-trips to within micro precision of the original.
	if parsed.Sub(now).Abs() > time.Microsecond {
		t.Errorf("round-trip drift: parsed=%v, original=%v", parsed, now)
	}
}

// TestAgentsHandler_TypesAreReachable — sanity check that the exported
// type set (AgentsResponse, AgentsPool, AgentSlot, AgentSession,
// PoolDetailSource, RegistryStatsSource) is publicly referenced from
// tests. Catches compile-time drift if any field/JSON-tag is renamed.
func TestAgentsHandler_TypesAreReachable(t *testing.T) {
	_ = server.AgentsResponse{Pool: server.AgentsPool{Slots: []server.AgentSlot{{Label: "x"}}}}
	_ = server.AgentSession{Model: nil}
	var _ server.PoolDetailSource = fakePoolDetailSource{}
	var _ server.RegistryStatsSource = fakeRegistryStatsSource{}
	// Use testutil to silence its unused-import lint when only referenced indirectly.
	_ = testutil.Logger(t)
	// Use reflect to verify the JSON tag on CurrentSessionID is the expected snake_case.
	rt := reflect.TypeOf(server.AgentSlot{})
	f, ok := rt.FieldByName("CurrentSessionID")
	if !ok {
		t.Fatal("AgentSlot.CurrentSessionID field missing")
	}
	if got := f.Tag.Get("json"); got != "current_session_id" {
		t.Errorf("AgentSlot.CurrentSessionID json tag: got %q, want %q", got, "current_session_id")
	}
}
