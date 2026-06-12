//go:build darwin || windows

package main

// State is the displayable status of the gateway as seen by the tray.
type State string

const (
	StateUnknown  State = "unknown"
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateDegraded State = "degraded"
	StateError    State = "error"
)

// stateInput is the raw evidence collected per poll. computeState
// is pure — same input always yields same output — so unit tests
// are trivial and side effects (logging, notifications, UI) live
// in the poller and tray loop.
type stateInput struct {
	PIDAlive       bool
	HealthOK       bool
	HealthFailures int      // consecutive failures while PID is alive
	StartingBudget bool     // true if we're inside the 30s post-start window
	Snapshot       Snapshot // populated only when HealthOK
	// ConfigError is the first line (≤200 bytes) of the wrapper-written
	// sentinel file at $HOME/.otto-gw/.config-error. Non-empty means
	// the wrapper's dotenv parser hit a malformed line; the FSM
	// short-circuits to StateError so the tray surfaces the parse
	// error instead of polling the wrong port and showing "stopped".
	// Populated by the poller (D-18-09 / REL-TRAY-08).
	ConfigError string
}

// stateOutput pairs the resolved state with a short human-readable
// detail line for the menu header.
type stateOutput struct {
	State  State
	Detail string
}

// computeState applies the 6-state mapping. Order matters: a non-empty
// ConfigError (sentinel from the wrapper) short-circuits BEFORE
// anything else — operator's .env is broken, so PID/health probes are
// meaningless until they fix it. Then stopped short-circuits the rest;
// degraded only fires when a pool is configured (PoolSize > 0) and has
// zero ready slots.
func computeState(in stateInput) stateOutput {
	// REL-TRAY-08 (D-18-09): wrapper sentinel wins over every other
	// signal. Reuses StateError — no new FSM state per CONTEXT.md.
	if in.ConfigError != "" {
		return stateOutput{State: StateError, Detail: "config error: " + in.ConfigError}
	}
	if !in.PIDAlive {
		return stateOutput{State: StateStopped}
	}
	if !in.HealthOK {
		if in.StartingBudget {
			return stateOutput{State: StateStarting, Detail: "warming up"}
		}
		if in.HealthFailures >= 3 {
			return stateOutput{State: StateError, Detail: "/health unreachable"}
		}
		return stateOutput{State: StateStarting, Detail: "warming up"}
	}
	if in.Snapshot.PoolSize > 0 && in.Snapshot.PoolAlive == 0 {
		return stateOutput{State: StateDegraded, Detail: "pool empty"}
	}
	// REL-TRAY-05 (T-5) fix: consume the pool.status enum surfaced by
	// /health (Plan 16-02 — D-05). The server-side rule already handles
	// the "busy-but-not-serving" wedge (Busy==Alive==Size && stale
	// LastProgressAt) and the exhausted-slot case; we just light up
	// StateDegraded when the enum reports either. Empty status
	// (degraded-mode boot / pre-Plan-16-02 build) falls through to the
	// happy path, which is the right default — the existing Alive==0
	// rule above still catches real failures on older builds.
	switch in.Snapshot.Pool.Status {
	case "degraded":
		return stateOutput{State: StateDegraded, Detail: "pool stalled"}
	case "exhausted":
		return stateOutput{State: StateDegraded, Detail: "pool exhausted"}
	}
	for _, h := range in.Snapshot.Hooks {
		if h.Enabled && h.LastError != "" {
			return stateOutput{State: StateDegraded, Detail: "hook " + h.Name + ": " + h.LastError}
		}
	}
	return stateOutput{State: StateRunning}
}
