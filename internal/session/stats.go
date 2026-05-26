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
type SessionDetail struct {
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

// Stats returns a point-in-time snapshot of registry occupancy. Task 0
// STUB: full implementation arrives in Task 1.
func (r *Registry) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Stats{Active: len(r.entries)}
}

// Detail returns the per-session detail rows for /health/agents (D-16).
// Task 0 STUB: returns an empty (but non-nil) slice; full implementation
// arrives in Task 1.
func (r *Registry) Detail() []SessionDetail {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return make([]SessionDetail, 0, len(r.entries))
}
