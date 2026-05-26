package pool

// startExitWatcher spawns a per-slot goroutine that observes the slot's
// client Done() channel and marks the slot dead when it fires. This is
// the Phase 5 D-01 push-based dead-slot detection mechanism.
//
// The watcher exits cleanly on Pool.Close via the <-p.closing branch.
// goleak gate in testmain_test.go enforces clean exit on every
// pool-package test run.
//
// Concurrency contract: the watcher holds p.mu ONLY for the slot.dead
// assignment — no slot.Client method calls under p.mu (anti-pattern per
// 05-RESEARCH.md §Anti-Patterns to Avoid).
//
// Lifecycle ordering with respawnSlot (Task 2): respawnSlot closes the
// OLD client FIRST, which makes the OLD client's Done() fire. The OLD
// watcher's <-slot.Client.Done() branch wins, marks slot.dead = true
// transiently, and exits. respawnSlot then resets slot.dead = false
// under p.mu and spawns a FRESH watcher for the new client. The
// transient dead=true write is harmless because respawnSlot holds the
// caller's slot exclusively (it was just received from p.slots).
func (p *Pool) startExitWatcher(slot *Slot) {
	go func() {
		select {
		case <-slot.Client.Done():
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
