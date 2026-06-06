//go:build darwin || windows

package main

import (
	"context"
	"time"
)

// probeFunc returns the raw evidence one tick observes: PID alive,
// /health OK, and the snapshot (zero value when health is not OK).
type probeFunc func() (pidAlive, healthOK bool, snap Snapshot)

// startingBudgetWindow bounds how long a 'starting' state persists
// after a freshly-issued start before the FSM is allowed to fall
// through to 'error'. Matches the wrapper's wait_until_ready budget.
const startingBudgetWindow = 30 * time.Second

// runPoller blocks until ctx is cancelled. Each tick it calls probe,
// composes a stateInput, computes a state, and emits on out. The
// caller owns the ticker (passes its C channel in) and the startedAt
// pointer (so the start/restart button can refresh the budget).
//
// `tick` is a channel rather than a *time.Ticker so tests can inject
// ticks deterministically.
func runPoller(ctx context.Context, probe probeFunc, tick <-chan time.Time, out chan<- stateOutput, startedAt *time.Time) {
	consecutiveFailures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			pidAlive, healthOK, snap := probe()
			if !healthOK && pidAlive {
				consecutiveFailures++
			} else {
				consecutiveFailures = 0
			}
			inBudget := startedAt != nil && !startedAt.IsZero() && time.Since(*startedAt) < startingBudgetWindow
			in := stateInput{
				PIDAlive:       pidAlive,
				HealthOK:       healthOK,
				HealthFailures: consecutiveFailures,
				StartingBudget: inBudget,
				Snapshot:       snap,
			}
			s := computeState(in)
			select {
			case out <- s:
			case <-ctx.Done():
				return
			}
		}
	}
}
