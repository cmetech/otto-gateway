---
phase: 05-pool-stateful-sessions
reviewed: 2026-05-26T00:00:00Z
depth: standard
files_reviewed: 37
files_reviewed_list:
  - cmd/otto-gateway/app_test.go
  - cmd/otto-gateway/main.go
  - internal/adapter/anthropic/adapter.go
  - internal/adapter/anthropic/handlers_session_test.go
  - internal/adapter/anthropic/handlers.go
  - internal/adapter/ollama/adapter.go
  - internal/adapter/ollama/handlers_session_test.go
  - internal/adapter/ollama/handlers.go
  - internal/adapter/openai/adapter.go
  - internal/adapter/openai/handlers_session_test.go
  - internal/adapter/openai/handlers.go
  - internal/config/config_test.go
  - internal/config/config.go
  - internal/pool/detail.go
  - internal/pool/exit_watcher_test.go
  - internal/pool/exit_watcher.go
  - internal/pool/export_test.go
  - internal/pool/pool.go
  - internal/server/agents_test.go
  - internal/server/agents.go
  - internal/server/health.go
  - internal/server/server.go
  - internal/server/sessions_delete_test.go
  - internal/server/sessions_delete.go
  - internal/session/config.go
  - internal/session/doc.go
  - internal/session/entry_acp.go
  - internal/session/export_test.go
  - internal/session/reaper_test.go
  - internal/session/reaper.go
  - internal/session/registry_test.go
  - internal/session/registry.go
  - internal/session/stats.go
  - internal/session/testhelpers.go
  - internal/session/testmain_test.go
  - tests/e2e/pool_sessions_e2e_test.go
findings:
  critical: 4
  warning: 7
  info: 4
  total: 15
status: issues_found
---

# Phase 5: Code Review Report

**Reviewed:** 2026-05-26T00:00:00Z
**Depth:** standard
**Files Reviewed:** 37
**Status:** issues_found

## Summary

Phase 5 wires pool dead-slot detection, a dedicated-session registry with reaper, and HTTP routing for stateful sessions across three adapters. The architecture is thoughtful — the Codex M-3 map-delete-first pattern, the snapshot-then-iterate reaper, and the structural-typing TRST-04 boundary all hold up. However, several defects in the concurrency story put the load-bearing "stateful sessions never reaped while in use" and "in-flight stream survives idle reaper" properties at risk.

The most serious finding is a **defer LIFO ordering bug across all five surface handler sites**: `defer entry.MarkUsed()` runs AFTER `defer entry.Mu.Unlock()` in current code, which (a) creates a data race on `Entry.LastUsed` against the reaper, and (b) opens a logic window where the reaper can kill a session whose stream just completed but whose `LastUsed` is still the pre-stream value. The comment in the code asserts this ordering is "semantically equivalent" — it is not.

Two other concurrency bugs are present in `session.Registry`: the `select-default close(e.ready)` pattern in `Delete` and `Close` can double-close the same channel under concurrent createEntry/Delete/Close races, which panics the process. And several writes to `Entry.Dead` and `Entry.LastModel` happen outside any mutex used by their readers — race-detector hazards even when behaviour is empirically correct.

Findings below are grouped by severity.

## Critical Issues

### CR-01: Defer LIFO ordering puts entry.MarkUsed() OUTSIDE the mutex; reaper can kill just-completed sessions

**File:** `internal/adapter/ollama/handlers.go:65-66`, `internal/adapter/ollama/handlers.go:165-166`, `internal/adapter/openai/handlers.go:62-63`, `internal/adapter/openai/handlers.go:158-159`, `internal/adapter/anthropic/handlers.go:94-95`

**Issue:** All five session-bound handler sites use this pattern:

```go
entry.Mu.Lock()
defer entry.MarkUsed()   // registered first → runs LAST in LIFO
defer entry.Mu.Unlock()  // registered second → runs FIRST in LIFO
```

Defer LIFO means `entry.Mu.Unlock()` fires first, then `entry.MarkUsed()` runs AFTER the mutex is released. Two consequences:

1. **Data race.** `MarkUsed` writes `Entry.LastUsed` without holding `Entry.Mu`. The reaper (`internal/session/reaper.go:79`) reads `LastUsed` under `entry.Mu.TryLock`. The Go race detector will flag this on any test that interleaves a reaper tick with handler completion. The reaper tests work around this by manually wrapping their LastUsed writes in `e.Mu.Lock()/Unlock()` (see `registry_test.go:60-62`, `reaper_test.go:102-115`), which proves the production path violates the same invariant.

2. **Logic bug.** Between handler `Unlock` and handler `MarkUsed`, the reaper can `TryLock` successfully, read the *stale* `LastUsed` (the value set by `createEntry` or by a previous MarkUsed long ago), find it past the cutoff, and reap a session whose stream literally just finished successfully. With a short TTL (e.g., the e2e suite's `SESSION_TTL_MS=500` and a 5-second streamed response), this is a reproducible kill of a healthy session. The comment in `ollama/handlers.go:62-64` claims the orderings are "semantically equivalent" because Mu is never touched by Result/Stream — that misses that the reaper is *another* goroutine taking Mu.

**Fix:** Swap the registration order so MarkUsed runs UNDER the mutex:

```go
entry.Mu.Lock()
defer entry.Mu.Unlock() // registered first → runs LAST
defer entry.MarkUsed()  // registered second → runs FIRST (still under Mu)
```

Apply at all five sites. Also update the comment in `ollama/handlers.go:62-64`.

---

### CR-02: select-default close(e.ready) double-closes the channel under Delete/createEntry race

**File:** `internal/session/registry.go:294-301`, `internal/session/registry.go:336-342`

**Issue:** `Registry.Delete` closes a mid-creation entry's `ready` channel with this idiom:

```go
if e.creating && e.ready != nil {
    select {
    case <-e.ready:
        // already closed (createEntry finished)
    default:
        close(e.ready)
    }
}
```

`Registry.Close`'s `closeAll` block uses the identical pattern. This idiom is fundamentally racy for a single-writer "close once" guarantee: between evaluating the select cases and executing the `close`, another goroutine can close the channel, and the subsequent `close()` panics with "close of closed channel".

Concrete reproducer:
1. Goroutine A: `Registry.Get(ctx, "abc", cwd)` → installs placeholder, drops r.mu, enters `createEntry`. Spawn+Initialize succeed, NewSession fails. `createEntry`'s `publishError` path takes r.mu, deletes `entries[sid]`, releases r.mu, then runs `close(e.ready)` (registry.go:213, unguarded).
2. Goroutine B: `Registry.Delete("abc")` runs concurrently. It takes r.mu, finds entries[sid] still present (A hasn't deleted yet), deletes, releases. It then runs the select-default block at registry.go:294-301.
3. If A's `close(e.ready)` lands between B's select-case evaluation and B's `default: close(e.ready)`, the second close panics.

`Registry.Close`'s path is even more exposed because it can fire while a fresh createEntry is still running (the closed flag is set under r.mu but createEntry already passed the check).

A bare `close()` paired with a select-default does NOT make the close idempotent — it makes the close non-blocking. Idempotent close requires `sync.Once` or coordination through a mutex that *both* close sites hold.

**Fix:** Either gate `close(e.ready)` behind a per-entry `sync.Once`:

```go
type Entry struct {
    // ...
    readyOnce sync.Once
}

func (e *Entry) closeReady() {
    e.readyOnce.Do(func() { close(e.ready) })
}
```

…and replace every `close(e.ready)` call site (createEntry success, createEntry publishError, createEntry concurrent-removal, Delete mid-creation branch, Close mid-creation branch) with `e.closeReady()`. Or restructure so only one site is ever responsible for closing — e.g., createEntry always owns the close, and Delete/Close set a flag that createEntry reads on completion.

---

### CR-03: pool.Stats.Alive does not reflect slot.dead — dead slots reported alive

**File:** `internal/pool/pool.go:284-289`

**Issue:** `Pool.Stats` computes:

```go
alive := 0
for _, s := range p.all {
    if s != nil && s.Client != nil {
        alive++
    }
}
```

This counts a slot as alive whenever `s.Client != nil`. But the Phase 5 dead-slot detection writes `slot.dead = true` (exit_watcher.go:30) WITHOUT clearing `s.Client`. The OLD client field stays populated until `respawnSlot` swaps it (only happens when a caller acquires the dead slot from the channel) or until `removeSlot` drops it on respawn failure.

Consequence: `/health` reports `pool.alive == size` even when N slots are dead and waiting to be lazily respawned. The `/health/agents` per-slot detail correctly uses `!slot.dead` (detail.go:47), so the two endpoints disagree. Operator dashboards reading `/health` will not see degraded pool capacity.

**Fix:** Use the same `!slot.dead` test that Detail() uses:

```go
for _, s := range p.all {
    if s != nil && s.Client != nil && !s.dead {
        alive++
    }
}
```

---

### CR-04: Unsynchronised writes to Entry.Dead and Entry.LastModel race their readers

**File:** `internal/session/reaper.go:94`, `internal/session/entry_acp.go:41`

**Issue:** Two `*Entry` fields are written outside any mutex held by their readers:

1. `e.Dead = true` (reaper.go:94) runs while the reaper holds `entry.Mu` but AFTER releasing `r.mu`. Readers in `Registry.Get` (registry.go:158: `if e.Dead`) read it under `r.mu.Lock()` only. Readers in `stats.go:90` (Detail's `!e.Dead`) read it under `r.mu.RLock()` only. No common mutex synchronises writer and readers.
2. `e.LastModel = modelID` (entry_acp.go:41) is written by `Entry.SetModel` which the caller is expected to invoke while holding `entry.Mu`. `stats.go:84-87` reads `e.LastModel` under `r.mu.RLock()` (NOT `entry.Mu`). Same race shape.

The Go memory model says reads of values written by other goroutines without synchronisation may observe stale or torn values. For `bool` and `string` reads on amd64/arm64 the practical risk is observing a stale value (logic skew, not corruption), but the race detector will fire and `-race` CI gates fail.

**Fix:** For `Entry.Dead`, either (a) move the write into the same r.mu block where the reaper deletes the entry (so writers and Get both synchronise on r.mu), or (b) make `Dead` an `atomic.Bool`. For `Entry.LastModel`, change Detail() to acquire `entry.Mu.TryLock()` (it already does for Busy) and read LastModel inside that critical section; on TryLock failure, render Model as nil (it's an observability surface — point-in-time is fine).

```go
// Option A: shift Dead write under r.mu (preferred — composes with the
// map-delete-first invariant).
r.mu.Lock()
if cur, ok := r.entries[es.sid]; ok && cur == es.entry {
    delete(r.entries, es.sid)
    es.entry.Dead = true
}
r.mu.Unlock()
```

## Warnings

### WR-01: respawnSlot has a latent race on slot.Client.Done() capture by the OLD watcher

**File:** `internal/pool/pool.go:214-247`, `internal/pool/exit_watcher.go:22-42`

**Issue:** `respawnSlot` closes the OLD client first, then later (under p.mu) assigns `slot.Client = newClient`. The OLD exit-watcher goroutine evaluates the expression `slot.Client.Done()` at the moment its `select` statement first runs. If the OLD watcher goroutine was spawned but had not yet entered its select when `respawnSlot` reaches step 4 (`slot.Client = newClient`), the OLD watcher will read `slot.Client.Done()` against the NEW client. When the NEW client eventually exits, the OLD watcher will flip `slot.dead = true` on a NEW client that has its own NEW watcher — double-marking, observability skew. The doc comment at exit_watcher.go:16-21 implicitly assumes the OLD watcher has already reached its select before respawnSlot runs, which Go's scheduler does not guarantee.

In practice the OLD client's `Close()` takes milliseconds (subprocess teardown), giving the OLD watcher ample wall-clock time to schedule and enter its select. But this is fragile — a cold goroutine scheduling under load could trigger the misbinding.

**Fix:** Capture the Done channel by value in `startExitWatcher`:

```go
func (p *Pool) startExitWatcher(slot *Slot) {
    done := slot.Client.Done() // capture at spawn site under p.mu
    go func() {
        select {
        case <-done:
            // ... old behaviour
        case <-p.closing:
            return
        }
    }()
}
```

The call site already holds p.mu when slot.Client is being assigned (in respawnSlot step 4 and in initSlot), so capturing the channel reference at the spawn site removes the goroutine-startup-timing dependency.

---

### WR-02: /health/agents leaks session IDs and pool topology without authentication

**File:** `internal/server/server.go:188-192`, `internal/server/agents.go:83-107`

**Issue:** `/health/agents` is registered on the OUTER (auth-exempt) router by design (D-18). The response includes:
- Per-pool-slot label and current session id (verbatim from `X-Session-Id`)
- Per-session id, last-used timestamp, current model

The wire shape echoes operator-supplied `X-Session-Id` values. If clients use sensitive identifiers (email, UID, conversation id), `/health/agents` discloses them to anyone who can reach the gateway. With `HTTP_ADDR=127.0.0.1:11435` the exposure is loopback-only, but `HTTP_ADDR=:11435` (allowed by config) makes it network-exposed.

The same endpoint also discloses pool size, slot labels, and busy counts — useful to an attacker probing for backpressure-attack timing.

**Fix:** Either (a) honour the same auth.Bearer chain as the protected routes — operator dashboards can include the bearer; the cost is one header — or (b) split `/health/agents` into a public liveness summary (counts only) and a protected detail endpoint that includes session IDs. Document the decision in `docs/operating.md`.

---

### WR-03: agentsHandler/health/sessionDeleter use nil-able cfg.Logger paths that can panic

**File:** `internal/server/agents.go:83-107`, `internal/server/health.go:61-84`, `internal/server/sessions_delete.go:64-84`

**Issue:** `LoggerFromCtx(r.Context(), s.logger)` is called with `s.logger` as the fallback. If `Server.logger` is nil (a `Config{}` literal with no Logger set — which `agents_test.go` and `sessions_delete_test.go` use), the function's behaviour depends on `LoggerFromCtx`'s nil tolerance. The middleware path always installs a logger in the context for HTTP-served requests, but the test helpers exercise the handler without the middleware chain. Defensive callers should guard.

Separately, the production code path in `cmd/otto-gateway/main.go:84-89` constructs a real logger before calling `newApp`, so the production risk is bounded. The race is more about future test paths that construct `Server` directly with a zero-value Config.

**Fix:** In `NewFromConfig`, when `cfg.Logger == nil`, install a discard logger:

```go
logger := cfg.Logger
if logger == nil {
    logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
s.logger = logger
```

This matches the discardWriter pattern already used in the ollama/openai/anthropic adapters.

---

### WR-04: Pool.Cancel forwards to slot.Client.Cancel without holding p.mu — slot may be respawning

**File:** `internal/pool/pool.go:500-513`

**Issue:** `Pool.Cancel` reads `slot` from `sessionSlots`, releases r.mu, then calls `slot.Client.Cancel(sid)`. The slot's Client field is read without any mutex. If `respawnSlot` concurrently replaces `slot.Client = newClient`, the Cancel call could land on the wrong client.

In practice this race window is narrow: respawn only fires from `NewSession`, which only acquires from the free queue. The slot in `sessionSlots` is checked out by a session, so it isn't simultaneously being respawned by `NewSession`. But the invariant ("only NewSession respawns checked-out-via-free-queue slots") is not enforced in code — a future refactor could introduce a code path that respawns a session-bound slot.

**Fix:** Document the invariant explicitly on the Slot type ("slot.Client may only be mutated by the caller that holds the slot via p.slots receipt, never while sessionSlots maps to this slot"), or capture the client pointer under p.mu:

```go
func (p *Pool) Cancel(sid string) {
    p.mu.Lock()
    slot, ok := p.sessionSlots[sid]
    var client PoolClient
    if ok {
        client = slot.Client
    }
    p.mu.Unlock()
    if !ok {
        return
    }
    client.Cancel(sid)
    p.releaseSlotForSession(sid)
}
```

---

### WR-05: Registry.Close calls e.Client.Close() without holding entry.Mu — pulls rug out of in-flight handler if HTTP server shutdown deadline expired

**File:** `internal/session/registry.go:317-352`

**Issue:** `Registry.Close` iterates the snapshot of entries and calls `e.Client.Cancel + Close` without acquiring `entry.Mu`. The doc comment at registry.go:312-316 says this is intentional (anti-pattern from pool.closeAll: never hold r.mu across slow Client.Close). But it also means that if an HTTP handler is still mid-stream when Registry.Close runs (HTTP server Shutdown deadline expired, in-flight handler not drained), the Close pulls the rug out from under it.

The normal teardown ordering in `cmd/otto-gateway/main.go` is:
1. SIGINT → cancel context → `srv.Shutdown(30s timeout)` returns
2. `cleanup()` defer fires → `registry.Close()` → `pool.Close()`

If step 1 times out (some handler exceeded 30s), step 2 races against the still-running handler. The handler holds `entry.Mu` while streaming; Close ignores `entry.Mu`, closes the client. The handler's next stream-channel read returns EOF — the stream aborts. The handler then runs `defer entry.MarkUsed()` (no panic — just writes time.Now) and returns. The HTTP response is truncated.

This is by-design for shutdown but worth documenting more loudly than the brief comment. A misconfigured slow handler could cause partial client-visible state inconsistencies.

**Fix:** Either (a) document the shutdown semantics in the package doc.go, or (b) add a brief grace period in Close that attempts `entry.Mu.TryLock` and waits up to ~100ms before forcibly Close'ing the client, so the typical "handler completing within 100ms of Shutdown timeout" case drains cleanly.

---

### WR-06: cleanup() in newApp's Warmup-fails path is called twice if pool partially constructed

**File:** `cmd/otto-gateway/main.go:155-160`

**Issue:** When `pool.Warmup` returns an error:

```go
if err := a.pool.Warmup(warmCtx); err != nil {
    cleanup()
    return nil, func() {}, fmt.Errorf("pool warmup: %w", err)
}
```

The function returns `cleanup = func(){}` (no-op) so the caller's `defer cleanup()` does nothing — good. But Warmup itself already calls `_ = p.closeAll()` internally on slot-spawn failure (pool.go:122, 136). So when the caller's `cleanup()` invokes `a.pool.Close()`, Close runs `closeOnce.Do(close(closing) + closeAll())`. closeAll on a Pool whose `p.all` was already nilled by Warmup's earlier closeAll is a no-op iteration, so this is benign — but it's a load-bearing benign. Any change to closeAll that depends on `p.all` being non-nil-after-first-call would crash here. Worth tightening.

Additionally: the cleanup closure also doesn't ensure `registry.Close()` runs in this path because `a.registry` is constructed AFTER Warmup. So a Warmup failure leaves `a.registry == nil`, the cleanup nil-checks it, and skips it — correct, but only because of ordering. Document the dependency.

**Fix:** Add a comment to `closeAll` noting it tolerates being called twice (idempotent via map-nil-then-iterate-empty); add a comment to the cleanup closure noting it assumes registry construction follows pool construction.

---

### WR-07: respawnSlot returns ctx-cancelled errors that look like spawn failures

**File:** `internal/pool/pool.go:222-237`

**Issue:** When the caller's ctx cancels mid-respawn, `Factory.Spawn(ctx, ...)` returns `ctx.Err()` (or a wrapped version). respawnSlot wraps this as `"pool: respawn slot %s: spawn: %w"`. The caller (NewSession) wraps again as `"pool: respawn slot %s: %w"`. Operators reading logs will see "respawn failed" even though the actual failure was caller-disconnect.

Same shape for the Initialize step.

**Fix:** Detect ctx errors explicitly:

```go
newClient, err := p.cfg.Factory.Spawn(ctx, ...)
if err != nil {
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return fmt.Errorf("pool: respawn slot %s aborted: %w", slot.Label, err)
    }
    return fmt.Errorf("pool: respawn slot %s: spawn: %w", slot.Label, err)
}
```

## Info

### IN-01: Stats() computes busy via len(p.slots) outside p.mu — race window on the count

**File:** `internal/pool/pool.go:290`

**Issue:** `busy := len(p.all) - len(p.slots)` reads `p.all` under p.mu but `p.slots` (a channel) is queried for length without p.mu. Channel length reads are atomic but the two snapshots are not coherent — a concurrent acquire that takes a slot from p.slots between the two reads inflates `busy` by 1. The `busy < 0` guard at line 291 prevents negative values, but `busy > len(p.all)` could also briefly occur in the opposite direction.

This is an observability surface; the value reaches `/health` and `/health/agents`. Minor cosmetic issue.

**Fix:** No change required for correctness. If preferred, document the inherent race in the Stats doc comment, or move to a `busy` counter incremented/decremented under p.mu in acquire/release paths.

---

### IN-02: respawnSlot's "slot died" log message fires during normal respawn — misleading

**File:** `internal/pool/exit_watcher.go:32-34`

**Issue:** When `respawnSlot` closes the OLD client (step 1), the OLD exit-watcher takes the `<-slot.Client.Done()` branch and logs `"pool: slot died"`. From an operator's perspective this looks like an unexpected subprocess crash. It is in fact a controlled respawn driven by Phase 5's lazy-respawn flow.

**Fix:** Either (a) tag the log line with a context bit indicating expected vs unexpected death — e.g., set a `slot.respawning` flag under p.mu before closing the OLD client and skip the log when that flag is set — or (b) at minimum lower the log level to Debug for the death path that watchers observe, leaving Info for the pre-respawn pool failures.

---

### IN-03: pool.Detail's slotToSID inversion silently drops collisions

**File:** `internal/pool/detail.go:35-38`

**Issue:** `slotToSID := make(map[*Slot]string)` then `for sid, slot := range p.sessionSlots { slotToSID[slot] = sid }`. If `p.sessionSlots` had two entries pointing at the same slot (which the comment at detail.go:32-34 says "is not supported by the slot-stateless semantics"), the second write silently overwrites the first. A future refactor that breaks this invariant would silently drop a CurrentSessionID row instead of failing loudly.

**Fix:** Add a defensive `if existing, dup := slotToSID[slot]; dup { /* log + continue */ }` so violation surfaces in logs. Cheap insurance.

---

### IN-04: e2e test reuses pgrep -n kiro-cli — fragile when other kiro-cli processes are running

**File:** `tests/e2e/pool_sessions_e2e_test.go:466-480`

**Issue:** `TestE2E_PoolSessions/DeadSlotLazyRespawn` runs `pgrep -n kiro-cli` to find a kiro-cli child to SIGKILL. `-n` returns the newest process, which is presumed to be the gateway's child. On a developer laptop with other kiro-cli activity running (interactive shell, other test runs), this can kill the wrong process. The test then asserts the gateway respawned a slot, but the killed process was unrelated and the gateway's slots are all still alive — the test passes "for the wrong reason".

**Fix:** Identify the gateway's child process via parent-PID lookup instead. Capture the gateway's PID from bootGateway (Cmd.Process.Pid) and use `pgrep -P <gateway-pid>` to get only that parent's children.

---

_Reviewed: 2026-05-26T00:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
