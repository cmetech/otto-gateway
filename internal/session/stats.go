package session

import "time"

// Stats is a point-in-time snapshot of registry occupancy returned by
// Registry.Stats. The /health endpoint (plan 05-03) consumes only the
// Active field — Active is len(entries) under r.mu.RLock.
//
// No Size field: the registry has no fixed capacity. D-06 enforces
// SESSION_MAX at admission time via ErrSessionMaxExceeded, not via a
// pre-allocated slot count. plan 05-03 wires server.SessionStats from
// Stats.Active directly.
type Stats struct {
	// Active is the count of entries currently in the map (alive or
	// in-creation). Equals len(r.entries) under r.mu.RLock.
	Active int
}

// SessionDetail is the per-session row shape locked by D-16 for the
// /health/agents endpoint (plan 05-03). The JSON tags are the wire
// contract — changing them is a breaking change for operator dashboards.
//
// Model is nullable (*string, encoded as null when LastModel == "")
// because a freshly-created entry has no model bound until the first
// SetModel call.
type SessionDetail struct { //nolint:revive // SessionDetail kept to avoid clash with (*Registry).Detail() method and adapter shim in cmd/otto-gateway/main.go
	// ID is the client-supplied sid (the X-Session-Id header value).
	ID string `json:"id"`
	// Alive is true when !Entry.Dead.
	Alive bool `json:"alive"`
	// Busy is true when Entry.Mu is currently locked (a stream is
	// in-flight). Read via Mu.TryLock from the observer; TryLock
	// failure → Busy=true. The observer immediately unlocks on success
	// so the read does not interfere with surface handlers.
	Busy bool `json:"busy"`
	// LastUsed is the wall-clock timestamp from Entry.LastUsed.
	// Stdlib's default time.Time MarshalJSON emits RFC 3339.
	LastUsed time.Time `json:"last_used"`
	// Model is the current model id, or nil when no SetModel has run
	// successfully on this entry yet.
	Model *string `json:"model"`
}

// Stats returns a point-in-time snapshot of registry occupancy.
func (r *Registry) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Stats{Active: len(r.entries)}
}

// Detail returns the per-session detail rows for /health/agents (D-16).
//
// Implementation notes:
//   - Snapshot taken under r.mu.RLock so concurrent Get/Delete are not
//     blocked. Reading Entry.Dead under r.mu is safe (CR-04 fix: the
//     reaper writes Dead UNDER r.mu now, so this reader and that writer
//     share r.mu).
//   - Busy is computed via e.Mu.TryLock() per entry: failure means a
//     surface handler is mid-Prompt under e.Mu, set Busy=true. On
//     success we immediately Unlock — the observation is point-in-time.
//   - Model is *string: nil when LastModel=="", otherwise a pointer to
//     a copy so JSON encodes null vs a quoted string per D-16. CR-04
//     fix: LastModel is read INSIDE the e.Mu.TryLock critical section
//     so it shares the same mutex as Entry.SetModel's writer. On
//     TryLock failure (stream in flight) we surface Model=nil — point-
//     in-time observability is fine, and a busy stream's model is
//     captured by the *previous* observation tick anyway.
//   - LastUsed is read in the same way (it's also written by MarkUsed
//     under e.Mu after CR-01); under TryLock failure LastUsed reads as
//     the zero value so callers see an "in-flight, point-in-time
//     unknown" row.
//   - Entries still in-creation (e.creating==true) are included with
//     Alive=!Dead, Busy=true (their Mu is effectively locked by
//     createEntry's spawn path), LastUsed=zero, Model=nil. Operators
//     reading /health/agents see them as transient.
//
// Returns an empty (non-nil) slice when the registry is empty so the
// handler encodes "sessions": [] rather than null.
func (r *Registry) Detail() []SessionDetail {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rows := make([]SessionDetail, 0, len(r.entries))
	for sid, e := range r.entries {
		if e == nil {
			continue
		}
		busy := true
		var modelPtr *string
		var lastUsed time.Time
		if !e.creating {
			if e.Mu.TryLock() {
				busy = false
				// CR-04 fix: read LastModel + LastUsed INSIDE the
				// e.Mu critical section so we synchronise with the
				// writers (SetModel, MarkUsed) on the same mutex.
				if e.LastModel != "" {
					m := e.LastModel
					modelPtr = &m
				}
				lastUsed = e.LastUsed()
				e.Mu.Unlock()
			}
		}
		rows = append(rows, SessionDetail{
			ID:       sid,
			Alive:    !e.Dead,
			Busy:     busy,
			LastUsed: lastUsed,
			Model:    modelPtr,
		})
	}
	return rows
}
