//go:build darwin || windows

package main

import (
	"context"
	"time"
)

type DesktopState string

const (
	DesktopNotInstalled DesktopState = "not-installed"
	DesktopStopped      DesktopState = "stopped"
	DesktopRunning      DesktopState = "running"
	DesktopInstalling   DesktopState = "installing"
)

// desktopInput is the raw per-tick evidence. computeDesktopState is pure.
type desktopInput struct {
	Installed  bool
	Running    bool
	Installing bool // overlaid by the install handler while a run is in-flight
}

// computeDesktopState: installing wins (transient), then not-installed,
// then running vs stopped.
func computeDesktopState(in desktopInput) DesktopState {
	if in.Installing {
		return DesktopInstalling
	}
	if !in.Installed {
		return DesktopNotInstalled
	}
	if in.Running {
		return DesktopRunning
	}
	return DesktopStopped
}

// runDesktopPoller mirrors runPoller's shape but for the desktop: each tick it
// calls probe and emits the computed state. Independent of the gateway FSM.
func runDesktopPoller(ctx context.Context, probe func() desktopInput, tick <-chan time.Time, out chan<- DesktopState) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			s := computeDesktopState(probe())
			select {
			case out <- s:
			case <-ctx.Done():
				return
			}
		}
	}
}
