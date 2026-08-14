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
		p.markSlotCheckedOut(s)
		return s, true
	case <-time.After(timeout):
		return nil, false
	}
}

// PutSlotBack re-sends slot into the free queue. Used by tests that
// observe a release via WaitForSlotRelease and want to leave the pool
// in a usable state for follow-up acquires.
func (p *Pool) PutSlotBack(slot *Slot) {
	p.mu.Lock()
	returned := p.tryReturnSlotLocked(slot)
	p.mu.Unlock()
	if !returned {
		panic("pool test: free queue full while returning slot")
	}
}

// TakeSlotIfAvailable removes and returns a free slot without waiting. Tests
// use it after consuming the expected release to detect a duplicate return.
func (p *Pool) TakeSlotIfAvailable() (*Slot, bool) {
	select {
	case slot := <-p.slots:
		p.markSlotCheckedOut(slot)
		return slot, true
	default:
		return nil, false
	}
}

// SessionSlotsLen returns the current size of the sessionSlots map
// (held briefly under mu). Used by tests asserting that Cancel /
// release cleans up the session-tracking entry.
func (p *Pool) SessionSlotsLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessionSlots)
}

// SessionHoldsLen returns the number of sessions with prompt-sequence state.
// The state remains present with zero holds while a prompt is active so its
// terminal wrapper can perform the deferred release.
func (p *Pool) SessionHoldsLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessionSequences)
}

// SlotAlive returns whether the slot with the given Label is alive
// (slot.dead == false). Returns (false, false) when no slot matches.
// Phase 5 D-01: test accessor for the dead-slot detection path; lets
// tests assert state without grepping struct internals.
func (p *Pool) SlotAlive(label string) (alive, found bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.all {
		if s != nil && s.Label == label {
			return !s.dead, true
		}
	}
	return false, false
}

// AllSlotsSnapshot returns a defensive copy of p.all for tests that need
// to assert pool effective size after a respawn-failure shrink (D-03).
func (p *Pool) AllSlotsSnapshot() []*Slot {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Slot, len(p.all))
	copy(out, p.all)
	return out
}

// ClosingChan returns p.closing for tests that want to assert clean
// watcher teardown via the Pool.Close path.
func (p *Pool) ClosingChan() <-chan struct{} {
	return p.closing
}

// RecordSpawnErrForTesting is the test seam for the otherwise-internal
// recordSpawnErr capture path. Lets HealthSummary tests assert the
// surfaced wire shape without standing up a fake factory that drives a
// real respawn failure end-to-end.
func (p *Pool) RecordSpawnErrForTesting(err error) {
	p.recordSpawnErr(err)
}

// SetCatalogRetryForTesting overrides the model-catalog warmup retry/backoff
// schedule so the resilient-discovery tests don't sleep real time. Must be
// called before Warmup. A nil/empty schedule means "one attempt, no retries".
func (p *Pool) SetCatalogRetryForTesting(schedule []time.Duration) {
	p.catalogRetry = schedule
}

// SetCatalogRefreshTicksForTesting replaces the production catalog ticker.
// It must be called before Warmup starts the scheduler.
func (p *Pool) SetCatalogRefreshTicksForTesting(ticks <-chan time.Time) {
	p.catalogRefreshTicks = ticks
}

// SetCatalogSchedulerTimingInitializedHookForTesting installs a barrier after
// the production timing source exists and before its first deadline is
// published. It must be called before Warmup.
func (p *Pool) SetCatalogSchedulerTimingInitializedHookForTesting(hook func(time.Time)) {
	p.catalogSchedulerTimingInitializedHook = hook
}

// SetCatalogNowForTesting replaces the catalog lifecycle wall clock.
func (p *Pool) SetCatalogNowForTesting(now func() time.Time) {
	p.catalogNow = now
}

// SetCatalogManualCooldownHookForTesting installs a barrier immediately after
// a manual caller reads cooldown state and before it attempts single-flight
// admission.
func (p *Pool) SetCatalogManualCooldownHookForTesting(hook func()) {
	p.catalogManualCooldownHook = hook
}

// SetCatalogSchedulerParkedForTesting installs a handshake emitted whenever
// the scheduler reaches its wait-for-next-tick state.
func (p *Pool) SetCatalogSchedulerParkedForTesting(parked chan<- struct{}) {
	p.catalogSchedulerParked = parked
}

// SetSpawnErrForTesting places the recorded spawn-error fields at a
// controlled wall-clock instant so SpawnFailing recency tests can exercise
// both the recent (red) and stale (not-red) branches without waiting real
// time. Mirrors recordSpawnErr's critical section.
func (p *Pool) SetSpawnErrForTesting(msg string, at time.Time) {
	p.mu.Lock()
	p.lastSpawnErr = msg
	p.lastSpawnErrAt = at
	p.mu.Unlock()
}

// SlotTurns returns the turn count for the slot with the given label (held
// under p.mu). The bool is false when no slot matches. Task 3 accessor for the
// turn-accounting tests.
func (p *Pool) SlotTurns(label string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, slot := range p.all {
		if slot != nil && slot.Label == label {
			return slot.turns, true
		}
	}
	return 0, false
}

// SlotRespawning returns the respawning flag for the slot with the given label
// (held under p.mu). The bool is false when no slot matches. Finding 2
// (round-3) accessor: lets a test assert the terminal (dead=true,
// respawning=false) invariant after a failed background recycle.
func (p *Pool) SlotRespawning(label string) (respawning, found bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, slot := range p.all {
		if slot != nil && slot.Label == label {
			return slot.respawning, true
		}
	}
	return false, false
}

// SetRecycleLaunchHookForTesting installs the test-only seam fired by
// releaseOrRecycle after recycle admission and before the goroutine launch.
// Task 3 shutdown-interleaving tests use it to wedge Close between the
// commit-to-recycle and the recycle goroutine start.
func (p *Pool) SetRecycleLaunchHookForTesting(hook func()) {
	p.mu.Lock()
	p.recycleLaunchHook = hook
	p.mu.Unlock()
}

// SweepIdleWorkersForTesting runs one synchronous idle-memory sweep.
func (p *Pool) SweepIdleWorkersForTesting() { p.sweepIdleWorkers() }

// WaitForRecyclesForTesting joins all scheduled recycle goroutines.
func (p *Pool) WaitForRecyclesForTesting() { p.recycleWG.Wait() }

// SetIdleSweepTicksForTesting replaces the production ticker before Warmup.
func (p *Pool) SetIdleSweepTicksForTesting(ticks <-chan time.Time) {
	p.idleSweepTicks = ticks
}

// SetIdleSweepLifecycleHookForTesting installs the final Warmup-to-start seam.
func (p *Pool) SetIdleSweepLifecycleHookForTesting(hook func(stage string)) {
	p.idleSweepLifecycleHook = hook
}

// WaitForIdleSweepForTesting joins the idle sweep loop after lifecycle tests.
func (p *Pool) WaitForIdleSweepForTesting() { p.idleSweepWG.Wait() }

// IdleSweepCadenceForTesting exposes the bounded cadence calculation.
func IdleSweepCadenceForTesting(after time.Duration) time.Duration {
	return idleSweepCadence(after)
}
