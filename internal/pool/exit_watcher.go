package pool

import (
	"fmt"
	"runtime/debug"
)

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
// Lifecycle ordering with respawnSlot (recycle-race fix): respawnSlot
// closes the OLD client FIRST, which makes the OLD client's Done() fire
// while the OLD watcher is still parked on it. That Done firing is a
// PLANNED teardown, not a crash, so the OLD watcher must NOT dead-mark the
// slot — respawnSlot is about to swap in a fresh, healthy worker. Two
// guards, checked under p.mu in the <-done branch, distinguish planned
// teardown from a genuine crash:
//   - slot.respawning: set true by respawnSlot before it closes the OLD
//     client, cleared in the swap critical section. Covers the window where
//     the OLD watcher wakes BEFORE the swap.
//   - slot.Client != watchedClient: the identity check. Covers the window
//     where the OLD watcher wakes AFTER the swap (respawning already
//     cleared) — slot.Client now points at the NEW client, so an OLD-client
//     watcher sees a mismatch. Pointer/interface identity only; no client
//     method is ever called under p.mu.
//
// Either guard tripping means "planned teardown": skip the dead-mark and
// emit a Debug line instead of the operator-facing "pool: slot died" INFO.
// Genuine crash teardowns (ping-escalation, readLoop EOF) and the ordinary
// lazy path have respawning==false AND a matching client, so they take the
// original dead-marking behavior unchanged.
//
// watchedClient is the client whose Done() the caller captured for `done`;
// both call sites (initSlot, respawnSlot) pass the same client.
func (p *Pool) startExitWatcher(slot *Slot, watchedClient PoolClient, done <-chan struct{}) {
	// D-18-07 REL-HTTP-07: capture logger BEFORE goroutine launch.
	exitWatcherLogger := p.cfg.Logger
	go func() {
		// D-18-07 REL-HTTP-07: defense-in-depth panic recovery. Site
		// name "pool-exit-watcher" is byte-exact per CONTEXT.md
		// §D-18-07. Recover, log once, exit cleanly — no auto-restart.
		// If this fires in production the slot.dead flip below will
		// not run; the slot will be retried via the lazy-respawn path
		// on the next NewSession.
		defer func() {
			if r := recover(); r != nil && exitWatcherLogger != nil {
				exitWatcherLogger.Error(
					"goroutine panic recovered",
					"site", "pool-exit-watcher",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()),
				)
			}
		}()
		// Test-only seam: tests install via SetExitWatcherPanicProbeForTest
		// to drive the defer-recover branch. Default nil → no-op in
		// production. Goes through firePanicProbe so the race detector
		// sees the happens-before relationship.
		firePanicProbe(&exitWatcherPanicProbe)
		select {
		case <-done:
			// acp.Client tore down its subprocess (Close, ping failure,
			// or readLoop EOF). Decide crash-vs-planned under p.mu using the
			// two guards, then emit AFTER unlock (never log under the lock).
			p.mu.Lock()
			planned := slot.respawning || slot.Client != watchedClient
			if !planned {
				// Genuine crash: mark the slot dead — Pool.NewSession's
				// dead-slot branch picks it up on the next Acquire.
				slot.dead = true
			}
			label := slot.Label
			p.mu.Unlock()
			if p.cfg.Logger != nil {
				if planned {
					// Recycle/respawn closed the OLD client deliberately; this
					// is not a crash. Debug-only so operators alerting on
					// "pool: slot died" get no false crash signal per recycle.
					p.cfg.Logger.Debug("pool: watcher observed planned teardown", "label", label)
				} else {
					p.cfg.Logger.Info("pool: slot died", "label", label)
				}
			}
		case <-p.closing:
			// Pool.Close fired — exit cleanly. The pool's closeAll path
			// will tear down all clients; we do not need to flip
			// slot.dead because no further Acquire will succeed.
			return
		}
	}()
}
