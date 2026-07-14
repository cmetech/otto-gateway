//go:build darwin || windows

package main

import (
	"context"
	"testing"
	"time"
)

// TestRegression_REL_TRAY_05_DegradedWhenPoolWedged demonstrates that the tray
// must surface a pool wedged in the "busy-but-not-serving" state as
// StateDegraded, not StateRunning.
//
// The /health JSON pool.status enum (Plan 16-02 — D-05) is the canonical
// source: the server-side rule fires "degraded" when Size > 0 AND
// Busy == Alive == Size AND time.Since(LastProgressAt()) > 30s. The tray
// consumes the enum directly without re-deriving from raw fields.
//
// Pre-fix observable: the FSM only checks PoolAlive==0 && PoolSize>0.
// A pool with all slots busy but workers nominally alive (Busy=Size=Alive=4,
// PoolAlive=4) returns StateRunning despite the wedge.
//
// Post-fix (T-5 fix): the FSM also checks Snapshot.Pool.Status; "degraded"
// or "exhausted" maps to StateDegraded.
func TestRegression_REL_TRAY_05_DegradedWhenPoolWedged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// All 4 pool slots are busy-but-not-serving. Workers are nominally
	// alive (no crash), so PoolAlive=4 — the legacy Alive==0 rule does
	// NOT fire. The /health server has emitted Pool.Status="degraded"
	// because LastProgressAt has not advanced in > 30s.
	probe := &fakeProbe{
		pidAlive: true,
		healthOK: true,
		snap: Snapshot{
			PoolAlive: 4,
			PoolSize:  4,
		},
	}
	probe.snap.Pool.Size = 4
	probe.snap.Pool.Alive = 4
	probe.snap.Pool.Busy = 4
	probe.snap.Pool.Status = "degraded"

	tick := make(chan time.Time, 4)
	out := make(chan stateOutput, 4)

	// startedAt one hour ago: the StartingBudget window has long expired
	// so the FSM does not mask a degraded signal as "warming up".
	startedAt := time.Now().Add(-1 * time.Hour)
	go runPoller(ctx, probe.probe, tick, out, func() time.Time { return startedAt }, "")

	tick <- time.Now()

	select {
	case s := <-out:
		if s.State != StateDegraded {
			t.Fatalf("FSM returned %q on Pool.Status=%q (Busy=Alive=Size=4); want StateDegraded — pre-fix observable (FSM does not consume Pool.Status enum)",
				s.State, probe.snap.Pool.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for poller output")
	}
}

// TestRegression_REL_TRAY_05_DegradedWhenPoolExhausted covers the
// "exhausted" enum value. Same shape as the degraded case — fsm.go must
// map both "degraded" and "exhausted" to StateDegraded.
func TestRegression_REL_TRAY_05_DegradedWhenPoolExhausted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe := &fakeProbe{
		pidAlive: true,
		healthOK: true,
		snap: Snapshot{
			PoolAlive: 4,
			PoolSize:  4,
		},
	}
	probe.snap.Pool.Size = 4
	probe.snap.Pool.Alive = 4
	probe.snap.Pool.Busy = 4
	probe.snap.Pool.Status = "exhausted"

	tick := make(chan time.Time, 4)
	out := make(chan stateOutput, 4)

	startedAt := time.Now().Add(-1 * time.Hour)
	go runPoller(ctx, probe.probe, tick, out, func() time.Time { return startedAt }, "")

	tick <- time.Now()

	select {
	case s := <-out:
		if s.State != StateDegraded {
			t.Fatalf("FSM returned %q on Pool.Status=%q; want StateDegraded",
				s.State, probe.snap.Pool.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for poller output")
	}
}
