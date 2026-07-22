//go:build darwin || windows

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestComputeDesktopState(t *testing.T) {
	cases := []struct {
		in   desktopInput
		want DesktopState
	}{
		{desktopInput{Installing: true}, DesktopInstalling},
		{desktopInput{Installed: false}, DesktopNotInstalled},
		{desktopInput{Installed: true, Running: false}, DesktopStopped},
		{desktopInput{Installed: true, Running: true}, DesktopRunning},
		// installing wins even if already installed
		{desktopInput{Installed: true, Running: true, Installing: true}, DesktopInstalling},
	}
	for _, c := range cases {
		if got := computeDesktopState(c.in); got != c.want {
			t.Errorf("computeDesktopState(%+v)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestRunLegacyDesktopPoller_EmitsOnTick(t *testing.T) {
	tick := make(chan time.Time, 1)
	out := make(chan DesktopState, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runLegacyDesktopPoller(ctx, func() desktopInput { return desktopInput{Installed: true, Running: true} }, tick, out)
	tick <- time.Now()
	select {
	case s := <-out:
		if s != DesktopRunning {
			t.Fatalf("got %q", s)
		}
	case <-time.After(time.Second):
		t.Fatal("no emission")
	}
}

func TestRunDesktopPollerTimerAndRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tick := make(chan time.Time, 1)
	refresh := make(chan struct{}, 1)
	out := make(chan desktopOutput, 4)
	var calls atomic.Int32
	probe := func() desktopOutput {
		calls.Add(1)
		return desktopOutput{State: DesktopRunning, Candidate: &desktopCandidate{Slug: "loop24"}}
	}
	go runDesktopPoller(ctx, probe, tick, refresh, out)
	tick <- time.Now()
	if got := receiveDesktopOutput(t, out); got.State != DesktopRunning {
		t.Fatalf("timer = %+v", got)
	}
	refresh <- struct{}{}
	if got := receiveDesktopOutput(t, out); got.State != DesktopDetecting {
		t.Fatalf("refresh first = %+v", got)
	}
	if got := receiveDesktopOutput(t, out); got.State != DesktopRunning {
		t.Fatalf("refresh result = %+v", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestRequestDesktopRefresh_CoalescesWithoutBlocking(t *testing.T) {
	refresh := make(chan struct{}, 1)

	requestDesktopRefresh(refresh)
	requestDesktopRefresh(refresh)

	if got := len(refresh); got != 1 {
		t.Fatalf("queued refreshes = %d, want 1", got)
	}
}

func receiveDesktopOutput(t *testing.T, out <-chan desktopOutput) desktopOutput {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for desktop output")
		return desktopOutput{}
	}
}
