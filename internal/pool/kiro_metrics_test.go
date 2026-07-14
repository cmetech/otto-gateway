// Whitebox: package pool (not pool_test) so the test can call the unexported
// acpSlotConfig() and assert the kiro-usage hooks forward to Config.Metrics.
package pool

import "testing"

type fakeRecorder struct {
	credits float64
	turnMs  int64
	turns   int
	ctxPct  float64
	hasCtx  bool
	mcp     []struct {
		server string
		ok     bool
	}
}

func (r *fakeRecorder) RecordTurnMeter(credits float64, turnMs int64, ctxPct float64, hasCtxPct bool) {
	r.credits += credits
	r.turnMs = turnMs
	r.ctxPct = ctxPct
	r.hasCtx = hasCtxPct
	r.turns++
}

func (r *fakeRecorder) RecordMCPInit(server string, ok bool) {
	r.mcp = append(r.mcp, struct {
		server string
		ok     bool
	}{server, ok})
}

// TestAcpSlotConfig_ForwardsKiroHooksToRecorder: when Config.Metrics is set,
// each slot's acp.Config carries the per-turn usage hooks that forward to the
// recorder. Pool slots are stateless, so OnContextPct is NOT wired — the ctx
// histogram is observed once per turn from OnTurnMeter's end-of-turn ctx.
func TestAcpSlotConfig_ForwardsKiroHooksToRecorder(t *testing.T) {
	rec := &fakeRecorder{}
	p := New(Config{Metrics: rec})

	cfg := p.acpSlotConfig()
	if cfg.OnTurnMeter == nil || cfg.OnMCPInit == nil {
		t.Fatal("acpSlotConfig must wire OnTurnMeter/OnMCPInit when Metrics is set")
	}
	if cfg.OnContextPct != nil {
		t.Error("pool must NOT wire OnContextPct — no per-slot recycle, ctx is per-turn")
	}

	cfg.OnTurnMeter(0.75, 1500, 42.0, true)
	cfg.OnMCPInit("filesystem", true)

	if rec.turns != 1 || rec.credits != 0.75 || rec.turnMs != 1500 {
		t.Errorf("OnTurnMeter not forwarded: turns=%d credits=%v turnMs=%d", rec.turns, rec.credits, rec.turnMs)
	}
	if !rec.hasCtx || rec.ctxPct != 42.0 {
		t.Errorf("end-of-turn ctx not forwarded: has=%v pct=%v", rec.hasCtx, rec.ctxPct)
	}
	if len(rec.mcp) != 1 || rec.mcp[0].server != "filesystem" || !rec.mcp[0].ok {
		t.Errorf("OnMCPInit not forwarded: %+v", rec.mcp)
	}
}

// TestAcpSlotConfig_NilRecorderLeavesHooksUnset: with no recorder the kiro hooks
// are nil (acp.handleNotification no-ops), so a bare pool stays hook-free.
func TestAcpSlotConfig_NilRecorderLeavesHooksUnset(t *testing.T) {
	p := New(Config{})
	cfg := p.acpSlotConfig()
	if cfg.OnTurnMeter != nil || cfg.OnContextPct != nil || cfg.OnMCPInit != nil {
		t.Error("kiro hooks must be nil when Config.Metrics is unset")
	}
}
