// Package session — whitebox test-only export file.
// Standard Go test-export pattern: a file ending in _test.go in the
// production package (NOT package session_test) that exposes
// unexported internals for test consumption. The file is only compiled
// when `go test` runs, so production code never sees these accessors.
package session

// SessionCount returns the current number of entries under the
// registry mutex (read lock). Used by reaper tests to assert that
// expired entries have been reaped without grepping internals.
func (r *Registry) SessionCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// ForceEntry inserts e at entries[sid] under the registry mutex.
// Used by tests that want to set up a specific Entry state without
// going through the lazy-create path. Overwrites any existing entry
// for sid.
func (r *Registry) ForceEntry(sid string, e *Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[sid] = e
}

// GetClosing returns the registry's closing channel so reaper tests
// can assert on its state (e.g., select-default to verify it has not
// been closed yet, or select-receive to verify it has).
func (r *Registry) GetClosing() <-chan struct{} {
	return r.closing
}

// ReapOnceForTest invokes reapOnce synchronously so tests can verify
// reaper behaviour without depending on ticker timing. Used by the
// TestReaper_HandlesMultipleEntries-style tests in Task 2.
func (r *Registry) ReapOnceForTest() {
	r.reapOnce()
}

// IsClosed reports whether Close has been called. Used by tests
// asserting that subsequent Get calls return ErrRegistryClosed.
func (r *Registry) IsClosed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.closed
}
