package pool

// startExitWatcher spawns a per-slot goroutine that observes the
// provided `done` channel (captured by the caller from the OLD or NEW
// slot.Client at spawn time) and marks the slot dead when it fires.
// This is the Phase 5 D-01 push-based dead-slot detection mechanism.
//
// The watcher exits cleanly on Pool.Close via the <-p.closing branch.
// goleak gate in testmain_test.go enforces clean exit on every
// pool-package test run.
//
// Concurrency contract: the watcher holds p.mu ONLY for the slot.dead
// assignment — no slot.Client method calls under p.mu (anti-pattern per
// 05-RESEARCH.md §Anti-Patterns to Avoid).
//
// WR-01 fix: `done` is captured by the caller at the spawn site (under
// p.mu in respawnSlot; under the just-created slot in initSlot where
// no other goroutine has a reference) and passed by value. Previously
// the goroutine evaluated `slot.Client.Done()` lazily when the select
// first ran — a goroutine that hadn't yet scheduled when respawnSlot
// reached step 4 would have read the NEW client's Done() channel,
// silently misbinding the OLD watcher to the NEW client.
//
// Lifecycle ordering with respawnSlot (Task 2): respawnSlot closes the
// OLD client FIRST, which makes the OLD client's Done() fire. The OLD
// watcher's <-done branch wins (now bound to the OLD client's Done
// channel because we captured it before swapping slot.Client), marks
// slot.dead = true transiently, and exits. respawnSlot then resets
// slot.dead = false under p.mu and spawns a FRESH watcher with the
// NEW client's done channel.
func (p *Pool) startExitWatcher(slot *Slot, done <-chan struct{}) {
	go func() {
		select {
		case <-done:
			// acp.Client tore down its subprocess (Close, ping failure,
			// or readLoop EOF). Mark the slot dead — Pool.NewSession's
			// dead-slot branch picks it up on the next Acquire.
			p.mu.Lock()
			slot.dead = true
			p.mu.Unlock()
			if p.cfg.Logger != nil {
				p.cfg.Logger.Info("pool: slot died", "label", slot.Label)
			}
		case <-p.closing:
			// Pool.Close fired — exit cleanly. The pool's closeAll path
			// will tear down all clients; we do not need to flip
			// slot.dead because no further Acquire will succeed.
			return
		}
	}()
}
