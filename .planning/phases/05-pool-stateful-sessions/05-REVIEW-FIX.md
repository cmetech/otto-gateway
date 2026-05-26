---
phase: 05-pool-stateful-sessions
fixed_at: 2026-05-26T00:00:00Z
review_path: .planning/phases/05-pool-stateful-sessions/05-REVIEW.md
iteration: 1
findings_in_scope: 11
fixed: 10
skipped: 1
status: partial
---

# Phase 5: Code Review Fix Report

**Fixed at:** 2026-05-26T00:00:00Z
**Source review:** `.planning/phases/05-pool-stateful-sessions/05-REVIEW.md`
**Iteration:** 1

**Summary:**
- Findings in scope (Critical + Warning): 11
- Fixed: 10
- Skipped: 1 (WR-02 — design decision out of scope for this fix pass)
- All fixed commits passed `go vet ./...` + `go test -race -timeout 180s ./...`

## Fixed Issues

### CR-01: Defer LIFO ordering puts entry.MarkUsed() OUTSIDE the mutex

**Files modified:** `internal/adapter/ollama/handlers.go`, `internal/adapter/openai/handlers.go`, `internal/adapter/anthropic/handlers.go`
**Commit:** `8c1e88a`
**Applied fix:** Swapped the defer registration order at all five session-bound handler sites so `defer entry.Mu.Unlock()` is registered FIRST (runs LAST in LIFO) and `defer entry.MarkUsed()` is registered SECOND (runs FIRST, still under the mutex). Replaced the misleading "semantically equivalent" comment with one documenting the load-bearing invariant. Closes the reaper-races-MarkUsed data race and the logic window where the reaper could kill a session whose stream had just completed.

### CR-02: select-default close(e.ready) double-closes under race

**Files modified:** `internal/session/registry.go`
**Commit:** `142cd63`
**Applied fix:** Added `readyOnce sync.Once` field to `Entry` plus an idempotent `closeReady()` helper using `readyOnce.Do(close)`. Replaced all five `close(e.ready)` call sites (createEntry success, createEntry publishError, createEntry concurrent-removal, Delete mid-creation branch, Close mid-creation branch) with the helper. Eliminates the "close of closed channel" panic that the racy select-default idiom could produce when concurrent createEntry/Delete/Close paths interleaved.

### CR-03: pool.Stats.Alive does not reflect slot.dead

**Files modified:** `internal/pool/pool.go`
**Commit:** `67fa14a`
**Applied fix:** Added `!s.dead` to the Stats() loop's alive-count predicate so it matches the `!slot.dead` test that `Detail()` already uses for `/health/agents`. The two endpoints now consistently report degraded pool capacity when slots are dead awaiting lazy respawn. Comment updated to reference the CR-03 finding and explain why s.Client stays populated on a dead slot until respawnSlot swaps it.

### CR-04: Unsynchronised writes to Entry.Dead and Entry.LastModel

**Files modified:** `internal/session/reaper.go`, `internal/session/stats.go`
**Commit:** `6a28f0d`
**Applied fix (Option A from REVIEW.md, plus the LastModel TryLock path):**
- Moved the `e.Dead = true` write INSIDE the `r.mu.Lock` block in reapOnce, conditional on the same `cur == es.entry` defensive check that guards the map delete. Writer and both readers (Registry.Get under r.mu.Lock, Detail under r.mu.RLock) now share r.mu.
- Moved the `e.LastModel` and `e.LastUsed` reads in `Detail()` INSIDE the `e.Mu.TryLock` critical section so they share the per-entry mutex with their writers (SetModel, MarkUsed). On TryLock failure (busy stream) Detail renders Model=nil and LastUsed=zero — point-in-time observability is fine.

**Note:** This fix may need human verification — the change to render `LastUsed=zero` on `e.Mu.TryLock` failure is a small observable-semantic shift in the /health/agents response shape for busy entries (previously the field would always carry the last write, possibly torn). All session-package tests pass under `-race`; the existing `Detail` tests do not appear to assert on the busy-entry LastUsed contents, but operator dashboards reading `/health/agents` may observe the new behaviour.

### WR-01: respawnSlot has a latent race on slot.Client.Done() capture

**Files modified:** `internal/pool/exit_watcher.go`, `internal/pool/pool.go`, `internal/pool/exit_watcher_test.go`
**Commit:** `d41885c`
**Applied fix:** Changed `startExitWatcher` signature to take an explicit `done <-chan struct{}` parameter captured by the caller. Updated `initSlot` (capture from the freshly-created slot, no mutex needed — no other goroutine has a reference) and `respawnSlot` (capture under p.mu in the same critical section that swaps slot.Client). Updated the two whitebox test call sites in `exit_watcher_test.go`. Removes the goroutine-startup-timing dependency that could have misbound the OLD watcher to the NEW client.

### WR-03: NewFromConfig nil-logger panic surface

**Files modified:** `internal/server/server.go`
**Commit:** `b22f529`
**Applied fix:** In `NewFromConfig`, when `cfg.Logger == nil` install a discard logger (slog over a no-op writer) and use the local `logger` var both for `s.logger` and for the middleware/auth configs. Added a small `serverDiscardWriter` type local to the package, mirroring the discardWriter pattern already used by the adapters. Production code path is unchanged (main.go always passes a non-nil logger).

### WR-04: Pool.Cancel reads slot.Client without p.mu

**Files modified:** `internal/pool/pool.go`
**Commit:** `3ba5c9c`
**Applied fix:** Captured `slot.Client` into a local `client` variable under `p.mu` in the same critical section as the `sessionSlots` lookup. The Cancel call runs after the unlock against the captured pointer. Added a defensive nil check so a future race that resolves to a nil Client is a silent no-op rather than a panic.

### WR-05: Registry.Close shutdown semantics documentation

**Files modified:** `internal/session/doc.go`
**Commit:** `96d3e19`
**Applied fix (Option A from REVIEW.md — documentation):** Added a "Shutdown semantics" section to the session package doc.go explaining the by-design trade-off: bounded shutdown vs. possible truncated response on a misconfigured slow handler that exceeds the 30s `http.Server.Shutdown` deadline. No behaviour change. Option B (grace-period TryLock) was not adopted — the by-design behaviour matches `pool.closeAll` and the reviewer flagged this as Warning-tier (not Critical).

### WR-06: cleanup() ordering documentation

**Files modified:** `cmd/otto-gateway/main.go`, `internal/pool/pool.go`
**Commit:** `73c0401`
**Applied fix:** Tightened the cleanup-closure comment in `cmd/otto-gateway/main.go` to explicitly document the registry-after-pool construction dependency and the "Warmup-failure cleanup may observe a drained pool" invariant. Tightened the `closeAll` doc comment in `internal/pool/pool.go` to document the load-bearing `p.all = nil` line that makes closeAll idempotent across repeated calls. No behaviour change.

### WR-07: respawnSlot returns ctx-cancelled as spawn failures

**Files modified:** `internal/pool/pool.go`
**Commit:** `5c6a164`
**Applied fix:** Added `errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)` checks on both the Factory.Spawn and client.Initialize error paths in `respawnSlot`. Ctx-cancellation now produces `"pool: respawn slot %s aborted: %w"` so operator logs distinguish caller-disconnect aborts (D-02 normal path) from genuine spawn/initialize failures.

## Skipped Issues

### WR-02: /health/agents leaks session IDs and pool topology without authentication

**File:** `internal/server/server.go:188-192`, `internal/server/agents.go:83-107`
**Reason:** skipped: design decision out of scope for this fix pass

The reviewer's recommended fixes were either (a) put auth on `/health/agents` or (b) split the endpoint into a public summary and a protected detail. Both options materially change the surface contract:
- Option (a) contradicts the explicit D-18 design decision in the planning artifacts ("`/health/agents` is registered on the OUTER (auth-exempt) router by design").
- Option (b) requires API-shape changes that downstream operator dashboards (which the planning artifacts mention but do not enumerate) may already depend on.

The reviewer also recommended documenting the decision in `docs/operating.md`, which does not yet exist. Per the GSD fix_strategy, this finding requires human design input rather than autonomous fix application. Recommend re-discussing in a follow-up planning session and addressing in a dedicated phase.

**Original issue:** /health/agents echoes operator-supplied X-Session-Id verbatim and discloses pool topology. With HTTP_ADDR=:11435 this is network-exposed; with HTTP_ADDR=127.0.0.1:11435 it is loopback-only. No auth or IP allowlist applies because D-18 placed the route on the auth-exempt outer router intentionally.

---

_Fixed: 2026-05-26T00:00:00Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
