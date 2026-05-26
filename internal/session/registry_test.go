// Package session_test — blackbox test file (D-18 pattern).
// Tests drive the registry through its exported surface PLUS the
// test-only accessors in export_test.go.
package session_test

import "testing"

// TestRegistry_Get_LazyCreate — SESS-01 + D-05. Task 0 STUB; full
// body lands in Task 1 (verifies first Get spawns subprocess + caches,
// second Get returns cached entry without re-spawning).
func TestRegistry_Get_LazyCreate(t *testing.T) {
	t.Skip("pending Task 1 implementation")
}

// TestRegistry_Get_RacingSameSid_NoDoubleSpawn — Pitfall 4. Task 0
// STUB; full body lands in Task 1 (two concurrent same-sid Gets
// observe a single Spawn call; both receive the SAME *Entry pointer).
func TestRegistry_Get_RacingSameSid_NoDoubleSpawn(t *testing.T) {
	t.Skip("pending Task 1 implementation")
}

// TestRegistry_Get_SessionMaxExceeded — D-06. Task 0 STUB; full body
// lands in Task 1 (with MaxSessions=2, third Get with new sid returns
// ErrSessionMaxExceeded; existing sid still works).
func TestRegistry_Get_SessionMaxExceeded(t *testing.T) {
	t.Skip("pending Task 1 implementation")
}

// TestRegistry_Delete_UnknownSid_ReturnsErrSessionNotFound — D-08 404
// path. Task 0 STUB; full body lands in Task 1 (Delete("nonexistent")
// returns ErrSessionNotFound; errors.Is matches).
func TestRegistry_Delete_UnknownSid_ReturnsErrSessionNotFound(t *testing.T) {
	t.Skip("pending Task 1 implementation")
}
