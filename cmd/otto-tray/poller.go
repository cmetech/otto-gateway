//go:build darwin || windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// configErrorSentinelMaxBytes caps the sentinel content surfaced via
// stateInput.ConfigError. 200 bytes is enough for "parse error at
// line N: <key>=<value>" without leaking long secrets that may have
// appeared after `=` on the malformed line (D-18-09 threat T-18-03-I).
const configErrorSentinelMaxBytes = 200

// readConfigErrorSentinel reads the wrapper-written sentinel at
// $HOME/.otto-gw/.config-error and returns its first line trimmed of
// surrounding whitespace and capped at configErrorSentinelMaxBytes.
// Returns "" when the file is absent (the happy path — wrapper
// deletes the sentinel on parse success) OR when $HOME cannot be
// resolved (degraded mode — safe default is no config error).
//
// The Wave 0 audit of $HOME / sentinel-file races (CONTEXT.md
// "Risks" row D-18-09) accepts a ~3s flap window during config
// reloads — the poll cadence is 3s; worst case the tray shows
// StateError momentarily, then recovers on the next tick.
func readConfigErrorSentinel() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	path := filepath.Join(home, ".otto-gw", ".config-error")
	data, err := os.ReadFile(path) //nolint:gosec // fixed path under user home; sentinel is operator-written diagnostic content
	if err != nil {
		return ""
	}
	// First line only (PII / secret minimization per D-18-09).
	first := strings.SplitN(string(data), "\n", 2)[0]
	first = strings.TrimSpace(first)
	if len(first) > configErrorSentinelMaxBytes {
		first = first[:configErrorSentinelMaxBytes]
	}
	return first
}

// probeFunc returns the raw evidence one tick observes: PID alive,
// /health OK, and the snapshot (zero value when health is not OK).
type probeFunc func() (pidAlive, healthOK bool, snap Snapshot)

// startingBudgetWindow bounds how long a 'starting' state persists
// after a freshly-issued start before the FSM is allowed to fall
// through to 'error'. Matches the wrapper's wait_until_ready budget.
const startingBudgetWindow = 30 * time.Second

// runPoller blocks until ctx is cancelled. Each tick it calls probe,
// composes a stateInput, computes a state, and emits on out. The
// caller owns the ticker (passes its C channel in) and a getter for
// the started-at timestamp (so the start/restart button can refresh
// the budget without sharing a struct field across goroutines).
//
// `tick` is a channel rather than a *time.Ticker so tests can inject
// ticks deterministically. `startedAt` is a getter so the caller can
// back it with atomic.Pointer / mutex / a literal — the poller does
// not care.
func runPoller(ctx context.Context, probe probeFunc, tick <-chan time.Time, out chan<- stateOutput, startedAt func() time.Time) {
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
			started := time.Time{}
			if startedAt != nil {
				started = startedAt()
			}
			inBudget := !started.IsZero() && time.Since(started) < startingBudgetWindow
			in := stateInput{
				PIDAlive:       pidAlive,
				HealthOK:       healthOK,
				HealthFailures: consecutiveFailures,
				StartingBudget: inBudget,
				Snapshot:       snap,
				// REL-TRAY-08 (D-18-09): wrapper sentinel surfaces
				// dotenv parse errors as StateError. Read each tick
				// — wrapper writes on parse failure, deletes on
				// success, so liveness tracks with config reloads.
				ConfigError: readConfigErrorSentinel(),
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
