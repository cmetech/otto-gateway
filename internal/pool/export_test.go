// Package pool — whitebox test-only export file.
// This is the standard Go test-export pattern: a file ending in
// _test.go in the production package (NOT package pool_test) that
// exposes unexported internals for test consumption. The file is
// only compiled when `go test` runs, so production code never sees
// these accessors.
package pool

import "time"

// WaitForSlotRelease blocks up to timeout waiting for a slot to be
// returned to the free queue. It's used by the slot-release tests
// in pool_test.go (TestPool_Prompt_ErrorReleasesSlot,
// TestPool_ContextCancel_ReleasesSlot, TestPool_Cancel_ReleasesSlot,
// TestPool_StreamCloseWithoutResult_ReleasesSlot) to assert that a
// terminal path returns the slot promptly without racing on a busy
// poll.
//
// Returns true when a slot was observed in the channel within
// timeout; false otherwise. The slot is NOT returned to the channel
// — the caller can re-send it via PutSlotBack if it needs to reuse
// the pool afterwards.
func (p *Pool) WaitForSlotRelease(timeout time.Duration) (*Slot, bool) {
	select {
	case s := <-p.slots:
		return s, true
	case <-time.After(timeout):
		return nil, false
	}
}

// PutSlotBack re-sends slot into the free queue. Used by tests that
// observe a release via WaitForSlotRelease and want to leave the pool
// in a usable state for follow-up acquires.
func (p *Pool) PutSlotBack(slot *Slot) {
	p.slots <- slot
}

// SessionSlotsLen returns the current size of the sessionSlots map
// (held briefly under mu). Used by tests asserting that Cancel /
// release cleans up the session-tracking entry.
func (p *Pool) SessionSlotsLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessionSlots)
}
