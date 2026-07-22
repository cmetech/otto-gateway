//go:build darwin || windows

package main

import (
	"context"
	"time"
)

type DesktopState string

const (
	DesktopDetecting      DesktopState = "detecting"
	DesktopNotInstalled   DesktopState = "not-installed"
	DesktopStopped        DesktopState = "stopped"
	DesktopRunning        DesktopState = "running"
	DesktopInstalling     DesktopState = "installing"
	DesktopAmbiguous      DesktopState = "ambiguous"
	DesktopDetectionError DesktopState = "detection-error"
)

type desktopOutput struct {
	State     DesktopState
	Candidate *desktopCandidate
	Detail    string
}

// runDesktopPoller serializes periodic and manual probes in this goroutine.
// Manual refresh clears the trusted UI state before gathering fresh evidence.
func runDesktopPoller(
	ctx context.Context,
	probe func() desktopOutput,
	tick <-chan time.Time,
	refresh <-chan struct{},
	out chan<- desktopOutput,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			if !sendDesktopOutput(ctx, out, probe()) {
				return
			}
		case <-refresh:
			if !sendDesktopOutput(ctx, out, desktopOutput{State: DesktopDetecting}) {
				return
			}
			if !sendDesktopOutput(ctx, out, probe()) {
				return
			}
		}
	}
}

func sendDesktopOutput(ctx context.Context, out chan<- desktopOutput, output desktopOutput) bool {
	select {
	case out <- output:
		return true
	case <-ctx.Done():
		return false
	}
}

func requestDesktopRefresh(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
