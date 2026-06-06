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
}

// stateOutput pairs the resolved state with a short human-readable
// detail line for the menu header.
type stateOutput struct {
	State  State
	Detail string
}

// computeState applies the 6-state mapping. Order matters: stopped
// short-circuits everything else; degraded only fires when a pool
// is configured (PoolSize > 0) and has zero ready slots.
func computeState(in stateInput) stateOutput {
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
	return stateOutput{State: StateRunning}
}
