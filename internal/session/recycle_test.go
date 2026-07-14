// Package session_test — blackbox tests for Track 2 proactive context recycle
// and the created/recycled counters (kiro usage-metrics parity build).
package session_test

import (
	"context"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// recycleRegistry builds a registry scripted with the given clients and a
// recycle threshold. Reuses the fakeClient/fakeClientFactory from registry_test.go.
func recycleRegistry(t *testing.T, pct float64, clients ...session.PoolClient) (*session.Registry, *fakeClientFactory) {
	t.Helper()
	ff := &fakeClientFactory{clients: clients}
	r := session.New(session.Config{
		Logger:     testutil.Logger(t),
		Factory:    ff,
		RecyclePct: pct,
	})
	t.Cleanup(func() { _ = r.Close() })
	return r, ff
}

func newFake(sid string) *fakeClient {
	return &fakeClient{newSessionFn: func(_ context.Context, _ string) (string, error) { return sid, nil }}
}

// TestRegistry_Get_RecyclesAtThreshold: an entry at/above CTX_RECYCLE_PCT is
// recycled on the next Get — old client closed, a fresh session created, the
// recycled counter incremented, and a NEW *Entry starting at ctxPct 0.
func TestRegistry_Get_RecyclesAtThreshold(t *testing.T) {
	fc1, fc2 := newFake("kiro-1"), newFake("kiro-2")
	r, ff := recycleRegistry(t, 80, fc1, fc2)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	e1, err := r.Get(ctx, "sid", "/tmp")
	if err != nil {
		t.Fatalf("Get #1: %v", err)
	}
	if r.Created() != 1 {
		t.Errorf("Created after first Get = %d; want 1", r.Created())
	}

	e1.SetCtxPctForTest(85) // above the 80 threshold

	e2, err := r.Get(ctx, "sid", "/tmp")
	if err != nil {
		t.Fatalf("Get #2 (recycle): %v", err)
	}
	if e2 == e1 {
		t.Fatal("Get #2 returned the SAME entry; recycle did not fire")
	}
	if e2.SessionID != "kiro-2" {
		t.Errorf("recycled SessionID = %q; want kiro-2 (fresh session)", e2.SessionID)
	}
	if fc1.closeCallCount() != 1 {
		t.Errorf("old client Close calls = %d; want 1", fc1.closeCallCount())
	}
	if ff.spawnCount() != 2 {
		t.Errorf("spawn calls = %d; want 2 (original + recycled)", ff.spawnCount())
	}
	if r.Recycled() != 1 {
		t.Errorf("Recycled = %d; want 1", r.Recycled())
	}
	if r.Created() != 2 {
		t.Errorf("Created after recycle = %d; want 2", r.Created())
	}
	if e2.CtxPctForTest() != 0 {
		t.Errorf("recycled entry ctxPct = %v; want 0 (fresh)", e2.CtxPctForTest())
	}

	// One-shot: the fresh entry is at 0, so the immediate next Get does not
	// recycle again (returns the same e2).
	e3, err := r.Get(ctx, "sid", "/tmp")
	if err != nil {
		t.Fatalf("Get #3: %v", err)
	}
	if e3 != e2 {
		t.Error("Get #3 recycled again; guard is not one-shot")
	}
	if r.Recycled() != 1 {
		t.Errorf("Recycled after one-shot check = %d; want 1", r.Recycled())
	}
}

// TestRegistry_Get_NoRecycleBelowThreshold: below the threshold the cached
// entry is returned unchanged.
func TestRegistry_Get_NoRecycleBelowThreshold(t *testing.T) {
	fc1 := newFake("kiro-1")
	r, _ := recycleRegistry(t, 80, fc1)

	ctx := context.Background()
	e1, err := r.Get(ctx, "sid", "/tmp")
	if err != nil {
		t.Fatalf("Get #1: %v", err)
	}
	e1.SetCtxPctForTest(50) // below 80

	e2, err := r.Get(ctx, "sid", "/tmp")
	if err != nil {
		t.Fatalf("Get #2: %v", err)
	}
	if e2 != e1 {
		t.Error("Get #2 recycled below threshold; must reuse the entry")
	}
	if fc1.closeCallCount() != 0 {
		t.Errorf("client closed below threshold: %d", fc1.closeCallCount())
	}
	if r.Recycled() != 0 {
		t.Errorf("Recycled = %d; want 0", r.Recycled())
	}
}

// TestRegistry_Get_RecycleDisabled: CTX_RECYCLE_PCT=0 disables recycle even at
// 99% context (fast-path preserved).
func TestRegistry_Get_RecycleDisabled(t *testing.T) {
	fc1 := newFake("kiro-1")
	r, _ := recycleRegistry(t, 0, fc1)

	ctx := context.Background()
	e1, err := r.Get(ctx, "sid", "/tmp")
	if err != nil {
		t.Fatalf("Get #1: %v", err)
	}
	e1.SetCtxPctForTest(99)

	e2, err := r.Get(ctx, "sid", "/tmp")
	if err != nil {
		t.Fatalf("Get #2: %v", err)
	}
	if e2 != e1 {
		t.Error("recycle fired while disabled (CTX_RECYCLE_PCT=0)")
	}
	if r.Recycled() != 0 {
		t.Errorf("Recycled = %d; want 0 (disabled)", r.Recycled())
	}
}

// TestRegistry_Created_IncrementsPerDistinctSid: Created counts each created
// session (two distinct sids → 2).
func TestRegistry_Created_IncrementsPerDistinctSid(t *testing.T) {
	r, _ := recycleRegistry(t, 80, newFake("kiro-a"), newFake("kiro-b"))
	ctx := context.Background()
	if _, err := r.Get(ctx, "sid-a", "/tmp"); err != nil {
		t.Fatalf("Get a: %v", err)
	}
	if _, err := r.Get(ctx, "sid-b", "/tmp"); err != nil {
		t.Fatalf("Get b: %v", err)
	}
	if r.Created() != 2 {
		t.Errorf("Created = %d; want 2", r.Created())
	}
}

// TestRegistry_ContextHook_UpdatesEntryAndRecorder: createEntry wires the acp
// OnContextPct hook so a context frame updates the entry's lastCtxPct AND
// forwards to the recorder. Uses a capturing factory to invoke the hook.
func TestRegistry_ContextHook_UpdatesEntryAndRecorder(t *testing.T) {
	rec := &fakeSessRecorder{}
	var capturedCfg acp.Config
	cf := &capturingFactory{
		cfgSink: &capturedCfg,
		client:  newFake("kiro-1"),
	}
	r := session.New(session.Config{
		Logger:     testutil.Logger(t),
		Factory:    cf,
		RecyclePct: 80,
		Metrics:    rec,
	})
	t.Cleanup(func() { _ = r.Close() })

	ctx := context.Background()
	e, err := r.Get(ctx, "sid", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if capturedCfg.OnContextPct == nil {
		t.Fatal("createEntry did not wire OnContextPct on the acp.Config")
	}

	capturedCfg.OnContextPct(77)
	if e.CtxPctForTest() != 77 {
		t.Errorf("entry ctxPct = %v; want 77 after OnContextPct", e.CtxPctForTest())
	}
	if rec.lastPct != 77 || rec.pctN != 1 {
		t.Errorf("recorder RecordContextPct not forwarded: pct=%v n=%d", rec.lastPct, rec.pctN)
	}

	// OnTurnMeter / OnMCPInit forward straight to the recorder.
	if capturedCfg.OnTurnMeter == nil || capturedCfg.OnMCPInit == nil {
		t.Fatal("createEntry did not wire OnTurnMeter/OnMCPInit")
	}
	capturedCfg.OnTurnMeter(0.5, 900)
	capturedCfg.OnMCPInit("fs", true)
	if rec.credits != 0.5 || rec.turns != 1 {
		t.Errorf("RecordTurnMeter not forwarded: credits=%v turns=%d", rec.credits, rec.turns)
	}
	if len(rec.mcp) != 1 || rec.mcp[0].server != "fs" || !rec.mcp[0].ok {
		t.Errorf("RecordMCPInit not forwarded: %+v", rec.mcp)
	}
}

// --- test doubles for the hook-wiring test ---

type fakeSessRecorder struct {
	credits float64
	turns   int
	lastPct float64
	pctN    int
	mcp     []struct {
		server string
		ok     bool
	}
}

func (r *fakeSessRecorder) RecordTurnMeter(credits float64, _ int64) { r.credits += credits; r.turns++ }
func (r *fakeSessRecorder) RecordContextPct(pct float64)             { r.lastPct = pct; r.pctN++ }
func (r *fakeSessRecorder) RecordMCPInit(server string, ok bool) {
	r.mcp = append(r.mcp, struct {
		server string
		ok     bool
	}{server, ok})
}

// capturingFactory records the acp.Config it is handed so the test can drive
// the wired hooks, and returns a single pre-built client.
type capturingFactory struct {
	cfgSink *acp.Config
	client  session.PoolClient
}

func (c *capturingFactory) Spawn(_ context.Context, cfg acp.Config) (session.PoolClient, error) {
	*c.cfgSink = cfg
	return c.client, nil
}
