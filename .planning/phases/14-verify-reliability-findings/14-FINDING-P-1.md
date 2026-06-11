---
finding: P-1
severity: C
rel_id: REL-POOL-01
status: confirmed
target_phase: 15
verified_at: 2026-06-11
---

# Finding P-1: Pool Shrinks Permanently to Zero on Transient Spawn Failure

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §1 P-1 (Critical):

> **Files:** `internal/pool/pool.go:534` (`removeSlot` on genuine respawn failure), `internal/pool/pool.go:491-505` (slot acquire has no timeout and no empty-pool check), `internal/pool/pool.go:297-306` (`removeSlot` — nothing ever re-adds a slot; `Warmup` at pool.go:137 is the only producer).
>
> **Failure scenario:** Disk fills up (or brew/npm replaces `kiro-cli` mid-upgrade, or fd exhaustion, or OOM makes `fork/exec` fail). A worker dies; the next request hits the dead-slot branch in `NewSession`, `respawnSlot` fails with a genuine (non-ctx) error, and `removeSlot` drops the slot from `p.all` — permanently. This repeats once per slot until the pool is empty. From then on every `Pool.NewSession` blocks on `<-p.slots`, a channel that will never receive again, until the client disconnects.

## Current-source check

**Current file:line:** `internal/pool/pool.go:534` (removeSlot on genuine respawn failure path), `internal/pool/pool.go:491-505` (blocking acquire with no timeout), `internal/pool/pool.go:297-306` (removeSlot implementation).

The failure path is intact in current main. At `pool.go:514-535`, `NewSession` calls `respawnSlot` when a dead slot is detected. On genuine (non-ctx) spawn failure, the code at line 525 checks for `errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)` — only ctx-cancel errors route through the re-queue path at lines 526-532. Any other error (disk full, binary replaced, fd exhaustion) falls through to `p.removeSlot(slot)` at line 534, permanently dropping the slot from `p.all`. Because `p.slots` is the only channel that feeds slot acquisition and `removeSlot` never sends back to it, the pool effective size permanently decrements.

The ctx-cancel protection (WR-07 fix) explicitly documents in its comment block that it handles "ctx-cancel only" — the genuine error path at line 534 is the bug. No new guard was added for genuine transient errors. The acquire path at lines 491-505 has a `<-ctx.Done()` arm and a `<-p.closing` arm but no timeout or empty-pool check.

## Evidence

Regression test: `internal/pool/regression_rel_pool_01_test.go::TestRegression_REL_POOL_01_PoolShrinksToZero`

Skip string: `t.Skip("REL-POOL-01 (P-1): regression test — unskip in Phase 15 fix commit")`

The reproducer constructs a `transientErrFactory` that returns a genuine non-ctx error (`errors.New("fake transient spawn error: disk full")`) on the second Spawn call (the respawn after warmup). After `fc0.fireDone()` triggers the dead-slot path, `NewSession` hits the transient error, `removeSlot` fires, and `p.Stats().Size` drops to 0. The pre-fix assertion verifies `Size == 0` (demonstrating the bug); Phase 15's fix inverts this to `Size == 1`.

## Verdict

**confirmed**

The cited `removeSlot` call at `pool.go:534` is intact and unchanged. The ctx-cancel re-queue guard (WR-07) at lines 525-532 explicitly protects only `context.Canceled` and `context.DeadlineExceeded` — there is no analogous guard for genuine transient spawn errors. Per D-11 (bias toward confirmed): the cite is intact, no concrete mitigation is visible, and "transient errors probably don't trigger in practice" is not a false-positive. The failure path requires a genuine spawn error (disk full, binary replaced, fd exhaustion) which is a realistic operational scenario on an unattended laptop.
