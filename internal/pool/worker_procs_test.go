package pool_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

// TestPool_WorkerProcs_HealthyPool: every live slot yields one (label, pid)
// row, labelled slot-0.. in warmup order and carrying that client's pid.
func TestPool_WorkerProcs_HealthyPool(t *testing.T) {
	clients := []*fakeClient{
		{pid: 1001, newSessionFn: makeWarmupOnly("warm-0", "run-0")},
		{pid: 1002, newSessionFn: makeWarmupOnly("warm-1", "run-1")},
		{pid: 1003, newSessionFn: makeWarmupOnly("warm-2", "run-2")},
	}
	p := warmedPoolWithFakes(t, clients)
	defer func() { _ = p.Close() }()

	got := map[string]int{}
	for _, w := range p.WorkerProcs() {
		got[w.Label] = w.Pid
	}
	want := map[string]int{"slot-0": 1001, "slot-1": 1002, "slot-2": 1003}
	if len(got) != len(want) {
		t.Fatalf("WorkerProcs() len = %d; want %d (%v)", len(got), len(want), got)
	}
	for label, pid := range want {
		if got[label] != pid {
			t.Errorf("%s pid = %d; want %d", label, got[label], pid)
		}
	}
}

// TestPool_WorkerProcs_SkipsDead: a dead slot contributes no row.
func TestPool_WorkerProcs_SkipsDead(t *testing.T) {
	clients := []*fakeClient{
		{pid: 1001, newSessionFn: makeWarmupOnly("warm-0", "run-0")},
		{pid: 1002, newSessionFn: makeWarmupOnly("warm-1", "run-1")},
		{pid: 1003, newSessionFn: makeWarmupOnly("warm-2", "run-2")},
	}
	p := warmedPoolWithFakes(t, clients)
	defer func() { _ = p.Close() }()

	clients[1].fireDone()
	if !waitForSlotDead(p, "slot-1", 200*time.Millisecond) {
		t.Fatal("slot-1 did not become dead within 200ms")
	}

	got := map[string]int{}
	for _, w := range p.WorkerProcs() {
		got[w.Label] = w.Pid
	}
	if _, dead := got["slot-1"]; dead {
		t.Errorf("dead slot-1 should be skipped; got %v", got)
	}
	if len(got) != 2 {
		t.Errorf("WorkerProcs() len = %d; want 2 (%v)", len(got), got)
	}
}

// TestPool_WorkerProcs_SkipsZeroPid: a client whose pid is <= 0 (fake, or a
// process not yet started) is excluded — the caller only sees readable pids.
func TestPool_WorkerProcs_SkipsZeroPid(t *testing.T) {
	clients := []*fakeClient{
		{pid: 0, newSessionFn: makeWarmupOnly("warm-0", "run-0")},
		{pid: 2002, newSessionFn: makeWarmupOnly("warm-1", "run-1")},
	}
	p := warmedPoolWithFakes(t, clients)
	defer func() { _ = p.Close() }()

	rows := p.WorkerProcs()
	if len(rows) != 1 {
		t.Fatalf("WorkerProcs() len = %d; want 1 (pid<=0 skipped): %v", len(rows), rows)
	}
	if rows[0].Label != "slot-1" || rows[0].Pid != 2002 {
		t.Errorf("row = %+v; want {slot-1 2002}", rows[0])
	}
}

// TestPool_WorkerProcs_SlowNewSessionReportsZeroIdle protects the checkout
// window between taking a free slot and registering the returned session id.
// The worker is already busy in that window, so an earlier release must not be
// projected as current idle time.
func TestPool_WorkerProcs_SlowNewSessionReportsZeroIdle(t *testing.T) {
	now := time.Date(2026, 8, 4, 16, 0, 0, 0, time.UTC)
	newSessionEntered := make(chan struct{})
	allowNewSession := make(chan struct{})
	var releaseGate sync.Once
	var calls atomic.Int32
	client := &fakeClient{
		models: []canonical.ModelInfo{{ID: "auto"}},
		pid:    3001,
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			call := calls.Add(1)
			if call == 3 {
				close(newSessionEntered)
				<-allowNewSession
			}
			return "session-" + string(rune('0'+call)), nil
		},
	}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:     func() time.Time { return now },
	})
	t.Cleanup(func() {
		releaseGate.Do(func() { close(allowNewSession) })
		_ = p.Close()
	})
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(5 * time.Minute)

	type sessionResult struct {
		sid string
		err error
	}
	sessionDone := make(chan sessionResult, 1)
	go func() {
		sid, err := p.NewSession(context.Background(), "")
		sessionDone <- sessionResult{sid: sid, err: err}
	}()
	<-newSessionEntered

	procs := p.WorkerProcs()
	if len(procs) != 1 || procs[0].UserRequestsSinceSpawn != 1 || procs[0].IdleSeconds != 0 {
		t.Fatalf("WorkerProcs during session/new = %+v, want count=1 idle=0", procs)
	}

	releaseGate.Do(func() { close(allowNewSession) })
	result := <-sessionDone
	if result.err != nil {
		t.Fatalf("NewSession: %v", result.err)
	}
	p.Cancel(result.sid)
}
