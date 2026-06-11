---
finding: P-3
severity: H
rel_id: REL-POOL-03
status: confirmed
target_phase: 15
verified_at: 2026-06-11
---

# Finding P-3: Stale awaitPromptResult Unconditionally Nils activeStream — Silent Empty 200

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §1 P-3 (High):

> **Files:** `internal/acp/client.go:868-870` (ctx-cancel arm: `c.activeStream = nil` with no identity check; same pattern at client.go:894-896), `internal/pool/pool.go:618-635` (ctx-watcher releases the slot on the same cancel, concurrently), `internal/acp/client.go:795-798` (next `Prompt` installs the new stream).
>
> **Failure scenario:** The enabling condition is by design: the pool releases a slot back to `p.slots` BEFORE the slot's previous `awaitPromptResult` goroutine has run. On stream-idle timeout, `ACP.Cancel(sid)` → `Pool.Cancel` returns the slot to the free queue while `awaitPromptResult(A)` is still parked. A queued request B then acquires the same slot, `NewSession` + `Prompt` set `c.activeStream = streamB` — and A's late goroutine runs `c.activeStream = nil`. B's prompt response still arrives via `respCh`, the stream closes cleanly with zero content.

## Current-source check

**Current file:line:** `internal/acp/client.go:868-870` (ctx-cancel arm unconditionally nils activeStream), `internal/acp/client.go:894-896` (frame arm unconditionally nils activeStream), `internal/pool/pool.go:618-635` (ctx-watcher releases slot concurrently).

The failure path is intact in current main. Both arms of `awaitPromptResult` unconditionally nil `c.activeStream`:

- **ctx.Done() arm** at `client.go:867-891`: `c.streamMu.Lock(); c.activeStream = nil; c.streamMu.Unlock()` — no identity check against the current `c.activeStream` value. If B has already installed `streamB` into `c.activeStream`, A's late goroutine overwrites it with nil.
- **frame received arm** at `client.go:893-896`: same unconditional pattern `c.streamMu.Lock(); c.activeStream = nil; c.streamMu.Unlock()`.

**D-09 false-positive bar explicitly checked:** There is NO CAS guard at either site. The fix described in the review (`if c.activeStream == stream { c.activeStream = nil }`) has NOT been applied. Both sites write nil unconditionally, regardless of which stream is currently installed.

The concurrent slot release at `pool.go:618-635` (ctx-watcher goroutine) creates the race window: the slot returns to `p.slots` while `awaitPromptResult(A)` is still parked, enabling B to acquire the slot and install `streamB` before A's goroutine runs its terminal branch.

## Evidence

Regression test: `internal/pool/regression_rel_pool_03_test.go::TestRegression_REL_POOL_03_StaleActiveStreamClobber`

Skip string: `t.Skip("REL-POOL-03 (P-3): regression test — unskip in Phase 15 fix commit")`

The reproducer uses a size-1 pool with a `promptFn` that: for Prompt A, cancels its ctx mid-stream (triggering A's `awaitPromptResult` ctx.Done() arm); for Prompt B on the recycled slot, closes the stream with content. The race window is explicit via a `raceGate` channel and a `time.Sleep(1ms)` gap to let A's stale goroutine run between slot release and B's stream install.

Under the pre-fix code, A's `awaitPromptResult` goroutine nils `c.activeStream` after B has installed `streamB`, causing B's subsequent `handleNotification` calls to hit the nil guard and drop chunks — B returns a zero-content result. Phase 15's CAS guard (`if c.activeStream == stream { c.activeStream = nil }`) prevents this by ensuring only the goroutine managing the current stream can nil the field.

## Verdict

**confirmed**

Both cited lines (`client.go:868-870` and `client.go:894-896`) perform unconditional `c.activeStream = nil` with no identity check — the D-09 false-positive bar (requires a concrete added guard citable at file:line) is not met. The race window is structurally enabled by the pool's slot-release-before-goroutine-exit design (by design, per the review). Independently confirmed by two separate review passes, as noted in the review document. Per D-11 bias: confirmed.
