//go:build darwin || windows

package main

import (
	"context"
	"testing"
	"time"
)

type fakeProbe struct {
	pidAlive bool
	healthOK bool
	snap     Snapshot
}

func (f *fakeProbe) probe() (bool, bool, Snapshot) { return f.pidAlive, f.healthOK, f.snap }

func TestPoller_EmitsStateOnEachTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe := &fakeProbe{pidAlive: false, healthOK: false}
	tick := make(chan time.Time, 4)
	out := make(chan stateOutput, 4)

	startedAt := time.Now().Add(-1 * time.Hour)
	go runPoller(ctx, probe.probe, tick, out, &startedAt)

	tick <- time.Now()
	select {
	case s := <-out:
		if s.State != StateStopped {
			t.Fatalf("first emit: got %s, want %s", s.State, StateStopped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first emit")
	}

	probe.pidAlive = true
	probe.healthOK = true
	probe.snap = Snapshot{PoolAlive: 4, PoolSize: 4}
	tick <- time.Now()
	select {
	case s := <-out:
		if s.State != StateRunning {
			t.Fatalf("second emit: got %s, want %s", s.State, StateRunning)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second emit")
	}
}

func TestPoller_ExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	probe := &fakeProbe{}
	tick := make(chan time.Time)
	out := make(chan stateOutput, 1)
	startedAt := time.Now()

	done := make(chan struct{})
	go func() {
		runPoller(ctx, probe.probe, tick, out, &startedAt)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not exit on ctx cancel")
	}
}
