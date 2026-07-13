//go:build darwin || windows

package main

import (
	"context"
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

func TestRunDesktopPoller_EmitsOnTick(t *testing.T) {
	tick := make(chan time.Time, 1)
	out := make(chan DesktopState, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runDesktopPoller(ctx, func() desktopInput { return desktopInput{Installed: true, Running: true} }, tick, out)
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
