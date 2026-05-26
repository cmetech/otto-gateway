---
phase: 05-pool-stateful-sessions
plan: 02
subsystem: session
tags: [session, registry, reaper, acp, kiro-cli, goleak, sync-mutex, trylock]

# Dependency graph
requires:
  - phase: 05-pool-stateful-sessions/05-01
    provides: "acp.Client.Done() <-chan struct{}; PoolClient.Done() interface contract — both already in tree before this plan; this plan consumes them via the duplicated session.PoolClient interface."
  - phase: 04-streaming
    provides: "engine.ACPClient interface + engine.Stream + engine.Run watchdog (Phase 4 D-06). *session.Entry satisfies engine.ACPClient via the compile-time gate so plan 05-03 can pass *Entry to engine.Run unchanged."
provides:
  - "internal/session package: Registry, Entry, Config, Stats, SessionDetail, error vars (ErrSessionNotFound, ErrSessionMaxExceeded, ErrRegistryClosed), goleak gate"
  - "Lazy session creation keyed by client-supplied sid with Pitfall 4 race resolution (creation sentinel + ready chan)"
  - "Reaper goroutine reaping idle entries via TryLock + snapshot-then-iterate (D-10/D-11/D-12) with bounded shutdown (Pitfall 5)"
  - "Codex M-3 map-delete-first Delete semantics returning ErrSessionNotFound for unknown sids"
  - "Entry.SetModel D-09 diff-skip + Entry.MarkUsed D-11 lifecycle"
  - "SESSION_TTL_MS (Node parity) and SESSION_MAX env-loading in internal/config/config.go + --session-ttl + --session-max CLI flags"
  - "session.NewEntryForTest cross-package construction seam for plan 05-03 surface-handler adapter tests"
affects:
  - "05-03 (gateway wiring): consumes the registry via main.go; surface handlers in internal/adapter/{ollama,openai,anthropic} consume *Entry as engine.ACPClient through engine.Run; DELETE /v1/sessions/:id wires to Registry.Delete; /health/agents wires to Registry.Detail()"

# Tech tracking
tech-stack:
  added: []  # no new deps — pure stdlib + already-vendored goleak
  patterns:
    - "Pitfall 4 creation-sentinel discipline (placeholder Entry with creating=true + ready chan, slow Spawn outside r.mu)"
    - "Codex M-3 map-delete-first + snapshot-then-iterate (reuse of pool.go discipline in registry/reaper)"
    - "Reaper TryLock skip-in-flight (D-12) + per-entry mutex serialization (D-07)"
    - "session.NewEntryForTest cross-package test seam in non-_test.go file"
    - "Compile-time interface assertions as load-bearing gates (var _ engine.ACPClient = (*Entry)(nil))"

key-files:
  created:
    - "internal/session/testmain_test.go (goleak gate)"
    - "internal/session/doc.go (package documentation incl. race diagram + lock ordering)"
    - "internal/session/config.go (Config + applyDefaults + ClientFactory + acpClientFactory + PoolClient interface)"
    - "internal/session/registry.go (Registry, Entry, error vars, New, Start, Get with Pitfall 4 sentinel, Delete with Codex M-3, Close with bounded shutdown, createEntry)"
    - "internal/session/reaper.go (reaperLoop + reapOnce — ticker + snapshot + TryLock)"
    - "internal/session/stats.go (Stats + SessionDetail D-16 row shape, Stats/Detail methods)"
    - "internal/session/entry_acp.go (Entry methods satisfying engine.ACPClient: NewSession/SetModel/Prompt/Cancel/MarkUsed + acpStreamShim; load-bearing var _ engine.ACPClient assertion)"
    - "internal/session/testhelpers.go (NewEntryForTest cross-package seam)"
    - "internal/session/export_test.go (whitebox accessors: SessionCount, ForceEntry, GetClosing, ReapOnceForTest, IsClosed)"
    - "internal/session/registry_test.go (11 tests: lazy-create, racing-same-sid, SESSION_MAX, Delete-known/unknown/in-flight, SetModel diff-skip, MarkUsed, Stats, Detail, spawn-fails, after-Close)"
    - "internal/session/reaper_test.go (8 tests: real-time D-13, skip-in-flight D-12, not-reap-recent D-11, cancel+close, multiple entries via ReapOnceForTest, exit-on-Close P5, deadlock-free reverse-lock-order, skip-during-creation)"
  modified:
    - "internal/config/config.go (added SessionTTL + SessionMax fields; SESSION_TTL_MS env via getEnvDuration; SESSION_MAX env via getEnvInt; --session-ttl + --session-max CLI flags)"

key-decisions:
  - "Pitfall 4 race resolution via creation-sentinel (creating bool + ready chan), NOT sync.Once. Sentinel discipline is symmetric with the rest of the registry's r.mu-guarded state and lets concurrent waiters see the placeholder under the same mutex they read entries through."
  - "session.PoolClient interface duplicates pool.PoolClient verbatim (rather than re-exporting). Disjoint at the type level so the two lifecycles cannot be conflated by sharing a type alias."
  - "Reaper deadlock avoidance via snapshot-then-iterate: r.mu.RLock just long enough to copy entries; iteration takes per-entry Mu.TryLock independently. Lock layers never held simultaneously."
  - "NewEntryForTest placed in a non-_test.go file (testhelpers.go) instead of export_test.go so plan 05-03 Task 3 adapter tests in other packages can construct *Entry without depending on session_test fakes."
  - "Reaper-during-creation: e.creating==true entries are skipped (snapshot filter) even when their zero LastUsed would trip the cutoff. Creation is bounded by Spawn+Initialize+NewSession duration, well under any reasonable TTL."
  - "Entry.SetModel cache update happens AFTER successful RPC (per resolved Open Question 1: SetModel failure surfaces 4xx, entry stays alive — failed RPC does not poison the LastModel cache)."

patterns-established:
  - "Cross-package test-helper seam: NewEntryForTest in a non-_test.go file lives in production code but is documented as test-only. Cheaper than a separate fixtures package, exported for tests in sibling packages."
  - "Lock-layer discipline: never hold r.mu and Entry.Mu simultaneously. Documented as load-bearing in doc.go; verified by TestReaper_DeadlockFree_ReverseLockOrder."
  - "Compile-time interface gates as defense-in-depth: var _ engine.ACPClient = (*Entry)(nil) AND var _ engine.Stream = (*acpStreamShim)(nil) — build fails immediately if either interface drifts from the *Entry method set."

requirements-completed: [SESS-01, SESS-02, SESS-03]

# Metrics
duration: 13min
completed: 2026-05-26
---

# Phase 05 Plan 02: internal/session Registry + Reaper Summary

**`internal/session` package shipping the X-Session-Id-keyed kiro-cli registry with lazy creation, SESSION_MAX cap, Codex M-3 Delete semantics, per-entry mutex serialization (D-07), and a TryLock-skip-in-flight reaper (D-10/D-11/D-12) — all behind a goleak gate, all `-race` clean.**

## Performance

- **Duration:** ~13 min (planning-loaded context; no deviations)
- **Started:** 2026-05-26T13:29:56Z
- **Completed:** 2026-05-26T13:43:39Z
- **Tasks:** 3 (Task 0 scaffolding + goleak gate, Task 1 Get/Delete/SetModel/Stats, Task 2 reaper)
- **Files created:** 11 (10 in internal/session + 1 modified in internal/config)
- **Tests added:** 20 across registry_test.go + reaper_test.go

## Accomplishments

- New `internal/session` package owns the dedicated kiro-cli session lifecycle keyed by client-supplied sid, fully outside the warm pool (D-04). 
- Registry.Get lazy-creates entries with **Pitfall 4 creation-sentinel** discipline: a placeholder Entry with `creating=true` + `ready chan` is installed under `r.mu`; concurrent same-sid callers drop the registry lock, wait on `<-ready`, then re-lookup — proving via `TestRegistry_Get_RacingSameSid_NoDoubleSpawn` that two concurrent same-sid Gets observe exactly one Spawn call.
- SESSION_MAX cap (D-06) enforced at admission time via `ErrSessionMaxExceeded`; plan 05-03's surface adapters render as 503.
- Registry.Delete uses the **Codex M-3 map-delete-first** pattern: lookup → `delete(r.entries, sid)` → drop `r.mu` → defensive `Cancel` → `Close`. `TestRegistry_Delete_CancelsInFlight` proves Delete returns within 100ms even when the Entry's Mu is held by a simulated in-flight stream.
- Reaper goroutine ticks every `cfg.TickInterval` (default 60s; tests inject 50ms for D-13) with **snapshot-then-iterate** discipline — r.mu.RLock just long enough to copy entries, then iterate with per-entry `Mu.TryLock()` (D-12). Lock-layer discipline verified by `TestReaper_DeadlockFree_ReverseLockOrder` (750ms of Get/Delete/Mu.Lock contention with reaper ticking every 5ms, no hang).
- D-11 `LastUsed` only advances at response complete (via `Entry.MarkUsed`); never at request start. Combined with D-12 TryLock skip, continuously-streaming sessions are mathematically un-reapable while the stream is active.
- D-09 `SetModel` diff-skip implemented with cache-after-success: `if modelID == e.LastModel return nil`, otherwise forward to Client and update cache ONLY on success.
- Pitfall 5 bounded shutdown: `Close()` runs `close(r.closing) + r.wg.Wait()`, bounded by `TickInterval + worst-case reapOnce`. `TestReaper_ExitsOnRegistryClose` with `TickInterval=1h` confirms Close returns <100ms.
- Goleak gate (`testmain_test.go`) green across all 20 tests — reaper goroutine accounting (wg.Add(1) + defer wg.Done() + close(closing) + wg.Wait()) is correct.

## Registry / Entry struct fields shipped

**`Registry`** (in `internal/session/registry.go`):

| Field | Visibility | Purpose |
|-------|------------|---------|
| `cfg` | unexported | Config (after applyDefaults) |
| `mu sync.RWMutex` | unexported | Guards `entries` + `closed` |
| `entries map[string]*Entry` | unexported | sid → Entry; plain map (not sync.Map per anti-pattern doc) |
| `closed bool` | unexported | Set by Close to fail subsequent Get with ErrRegistryClosed |
| `closing chan struct{}` | unexported | Closed by Close to signal reaper exit (Pitfall 5) |
| `wg sync.WaitGroup` | unexported | Tracks reaper goroutine for goleak-clean shutdown |
| `closeOnce sync.Once` | unexported | Guards re-entry of the shutdown sequence |

**`Entry`** (in `internal/session/registry.go`):

| Field | Visibility | Purpose | Read by |
|-------|------------|---------|---------|
| `Mu sync.Mutex` | **exported** | D-07 per-session serialization | Surface handler (Lock around Prompt), Reaper (TryLock) |
| `Client PoolClient` | **exported** | Dedicated *acp.Client (interface-typed for tests) | Entry methods, Delete, Reaper |
| `SessionID string` | **exported** | Cached ACP session id | Entry.NewSession, Cancel/Close paths |
| `LastUsed time.Time` | **exported** | D-10/D-11/D-12 reap cutoff | Reaper, Detail, MarkUsed |
| `LastModel string` | **exported** | D-09 diff-skip cache | Entry.SetModel, Detail |
| `Dead bool` | **exported** | Treat-as-not-present signal | Get, Detail |
| `creating bool` | unexported | Pitfall 4 sentinel under r.mu | Get, reapOnce snapshot filter |
| `ready chan struct{}` | unexported | Closed on create-complete | Same-sid Get waiters |

## Pitfall 4 race-resolution mechanism

**Mechanism chosen:** creation-sentinel (creating bool + ready chan), NOT `sync.Once`.

**Why:** Symmetry with the rest of the registry's `r.mu`-guarded state. Two concurrent same-sid Gets resolve like so:

1. Caller A: `r.mu.Lock()` → no entry → install placeholder with `creating=true` + fresh `ready := make(chan struct{})` → `r.mu.Unlock()` → run slow Spawn+Initialize+NewSession.
2. Caller B (arrives milliseconds later): `r.mu.Lock()` → observes the placeholder with `creating==true` → captures `ready := e.ready` → `r.mu.Unlock()` → `<-ready` (also honors ctx cancellation + registry-closing).
3. Caller A finishes Spawn: re-acquires `r.mu` → fills `e.Client`/`e.SessionID`/`e.LastUsed` → sets `creating=false` → `r.mu.Unlock()` → `close(e.ready)`.
4. Caller B unblocks on `<-ready` → loops to top of `Get` → re-reads `entries[sid]` → observes alive entry → returns it.

**Failure path:** if A's Spawn errors, A removes the placeholder under `r.mu` and `close(e.ready)`. B unblocks, re-reads `entries[sid]`, observes absence, and re-enters the lazy-create path (which may produce a fresh placeholder).

**Verification:** `TestRegistry_Get_RacingSameSid_NoDoubleSpawn` releases two concurrent Gets, gates Spawn on a channel, asserts `factory.spawnCount() == 1` and that both Gets return the SAME `*Entry` pointer.

## Reaper lock-order discipline + bounded-shutdown demonstration

**Lock order is one-at-a-time** — never both `r.mu` and `Entry.Mu` simultaneously:

```text
reapOnce():
  r.mu.RLock()                                  ──┐
    snapshot := []entryAndSID{...}                │ R-lock layer
  r.mu.RUnlock()                                ──┘

  for es := range snapshot:
    es.entry.Mu.TryLock()                       ──┐
      if expired:                                 │
        es.entry.Client.Cancel(...)               │ Entry-mu layer
        es.entry.Client.Close()                   │ (TryLock — never blocks)
        r.mu.Lock(); delete(...); r.mu.Unlock() ──┤  brief r.mu re-acquire
      es.entry.Dead = true                        │  for map-delete only
    es.entry.Mu.Unlock()                        ──┘
```

The brief re-acquire of `r.mu` for `delete(...)` is held only across the map operation — no slow Client work crosses it.

**Bounded shutdown:**

```go
// Registry.Close():
r.closeOnce.Do(func() {
    r.mu.Lock(); r.closed = true; entries := r.entries; r.entries = nil; r.mu.Unlock()
    close(r.closing)   // signals reaper exit
    r.wg.Wait()        // bounded by TickInterval + worst-case reapOnce
    // tear down snapshot entries WITHOUT r.mu held
})
```

`TestReaper_ExitsOnRegistryClose`: TickInterval=1h, Close returns within <100ms (asserted) — proves outer `select { case <-r.closing: return }` wins over the unreachable `case <-ticker.C`.

## SESSION_TTL_MS + SESSION_MAX env-var defaults

| Env Var | Default | Mechanism | Node Parity |
|---------|---------|-----------|-------------|
| `SESSION_TTL_MS` | 30m (1,800,000 ms) | `getEnvDuration` — accepts both Go duration strings ("30m") AND millisecond integers ("1800000") | YES (existing Node env var) |
| `SESSION_MAX` | 32 | `getEnvInt` | **NEW** — no Node equivalent; documented in `docs/operating.md` (D-06) |

CLI flags added (mirror `--pool-size`): `--session-ttl` (Duration), `--session-max` (Int). Both feed `cfg.SessionTTL` / `cfg.SessionMax` via `fs.Visit` (flag-wins-over-env).

## Goleak + race-detector status across all tests

- **goleak gate:** `internal/session/testmain_test.go` invokes `goleak.VerifyTestMain(m)`. All 20 tests pass cleanly with no leaked goroutines:
  - Reaper goroutine: `wg.Add(1)` in `Start`, `defer wg.Done()` in `reaperLoop`, exited via `<-r.closing`.
  - Creation-sentinel waiters: every code path that creates `e.ready` ALSO `close(e.ready)` on success / error / Close.
  - Test goroutines simulating in-flight Prompt always release `e.Mu.Unlock()` via `close(muRelease)` before test return.
- **Race detector:** `go test ./internal/session/... -count=1 -race -timeout=120s` exits 0. All test-side writes to `e.LastUsed` are wrapped in `e.Mu.Lock/Unlock` so the race detector sees the same lock the reaper holds during its read.
- **Re-run stability:** `-count=2` passes. The real-time D-13 test (TTL=200ms, TickInterval=50ms) completes in ~250ms; the deadlock-free test runs Get/Delete/Mu.Lock contention for 750ms without hanging.

## Pointer for plan 05-03

**What 05-03 wires:**

1. **`cmd/otto-gateway/main.go`** — construct `session.New(session.Config{...})` after pool warmup; `r.Start(context.Background())` to spawn the reaper; add `r.Close()` to the cleanup closure BEFORE `pool.Close()` (Pitfall 5 ordering); per resolved Open Question 3, always run pool.Close even if registry.Close errored.
2. **`registryStatsAdapter`** (new in main.go) — bridges `*session.Registry` to whatever `server.RegistryStatsSource` shape 05-03 introduces. The producer side is `Registry.Stats() Stats` (returns `{Active: int}`) and `Registry.Detail() []SessionDetail`. The wire shape `{id, alive, busy, last_used, model}` is already locked in D-16 by `SessionDetail`'s JSON tags.
3. **`SessionsRouter`** (new in `internal/server/sessions_delete.go`) — `RouteRegistrar` mounting `DELETE /v1/sessions/:id` on the auth-protected `/v1` prefix. Body returns `200 {"deleted": "<id>"}` per resolved Open Question 2 (Node parity); `ErrSessionNotFound` → 404; other errors → 500.
4. **Surface handler routing on `X-Session-Id`** — each adapter reads the header BEFORE `engine.Run`; empty → existing pool path unchanged; non-empty → `registry.Get(ctx, sid, cwd)`, `entry.Mu.Lock()` + `defer entry.Mu.Unlock()` + `defer entry.MarkUsed()`, pass `*Entry` as `engine.ACPClient` to `engine.Run` (compile-time gate already verifies the interface).
5. **`session.NewEntryForTest`** is the canonical Entry-construction seam for adapter tests in 05-03 Task 3 — `session.NewEntryForTest(fakeACPClient, sid)` returns a fully-constructed Entry without standing up a Registry.

**What 05-03 does NOT need to do:** add `Detail()` to `engine.ACPClient` (anti-pattern explicitly called out in 05-RESEARCH); the agent-detail source is a separate interface that Pool and Registry both satisfy.

## Task Commits

1. **Task 0: scaffold internal/session package + env vars** — `431d186` (feat)
2. **Task 1: Registry.Get/Delete + Entry.SetModel + Stats/Detail** — `068233e` (feat)
3. **Task 2: reaper loop with TryLock skip + snapshot-then-iterate** — `7df44ee` (feat)

## Files Created/Modified

**Created:**
- `internal/session/testmain_test.go` — goleak gate (TestMain installs VerifyTestMain)
- `internal/session/doc.go` — package documentation with race-resolution diagram + lock-ordering invariant
- `internal/session/config.go` — Config + applyDefaults + PoolClient interface + ClientFactory + acpClientFactory; compile-time `var _ PoolClient = (*acp.Client)(nil)`
- `internal/session/registry.go` — Registry, Entry, error vars, New, Start, Get (Pitfall 4 sentinel), Delete (Codex M-3), Close (bounded shutdown), createEntry (slow Spawn outside lock)
- `internal/session/reaper.go` — reaperLoop + reapOnce (ticker + snapshot + TryLock + lock-order discipline)
- `internal/session/stats.go` — Stats{Active int} + SessionDetail D-16 row shape with JSON tags + Stats/Detail methods
- `internal/session/entry_acp.go` — Entry methods (NewSession/SetModel/Prompt/Cancel/MarkUsed) + acpStreamShim; load-bearing `var _ engine.ACPClient = (*Entry)(nil)` and `var _ engine.Stream = (*acpStreamShim)(nil)`
- `internal/session/testhelpers.go` — `NewEntryForTest` cross-package construction seam + private acpClientForTestAdapter
- `internal/session/export_test.go` — whitebox accessors (SessionCount, ForceEntry, GetClosing, ReapOnceForTest, IsClosed)
- `internal/session/registry_test.go` — 11 tests (lazy-create, racing-same-sid, SESSION_MAX, Delete-known/unknown/in-flight, SetModel diff-skip, MarkUsed monotonicity, Stats active count, Detail row shape, spawn-fails, after-Close)
- `internal/session/reaper_test.go` — 8 tests (real-time D-13, skip-in-flight D-12, not-reap-recent D-11, cancel+close, multiple-entries via ReapOnceForTest, exit-on-Close P5, deadlock-free reverse-lock-order, skip-during-creation)

**Modified:**
- `internal/config/config.go` — added `SessionTTL time.Duration` + `SessionMax int` fields to Config; SESSION_TTL_MS env via getEnvDuration (Node parity — accepts ms-integer OR Go duration); SESSION_MAX env via getEnvInt; `--session-ttl` + `--session-max` CLI flags with `fs.Visit` overlay.

## Decisions Made

- **Pitfall 4 mechanism: creation-sentinel (NOT sync.Once).** The placeholder discipline is symmetric with the rest of r.mu-guarded state; concurrent waiters observe the placeholder through the same RWMutex the lookup uses. sync.Once would split the synchronization story across two primitives.
- **Duplicate PoolClient interface (NOT re-export from internal/pool).** Disjoint at the type level so the two lifecycles (warm-pool slot vs. dedicated session) cannot be conflated by sharing a type alias. Documented in config.go's doc-comment.
- **NewEntryForTest in non-_test.go file.** Plan 05-03 Task 3's adapter tests are in OTHER packages, so the helper must be exported across package boundaries. The minor cost (one tiny exported helper in production code) is cheaper than a separate `internal/sessiontest` fixtures package; the function is documented as "Test-only helper."
- **Reaper skips e.creating==true entries.** Placeholders have a zero LastUsed which would trip the cutoff. Snapshot filter excludes them — createEntry is bounded by Spawn+Initialize+NewSession duration, well under any reasonable TTL.
- **SetModel cache-after-success (NOT cache-then-RPC).** Per resolved Open Question 1: SetModel failure surfaces 4xx, entry stays alive — a failed RPC must not poison the LastModel cache so the next request can retry the same model id.

## Deviations from Plan

**None — plan executed exactly as written.**

The plan's `<read_first>` lists were honored before edits; all five Task 1 test names and one Task 2 test name landed verbatim, and the additional tests (TestRegistry_Get_FactorySpawnFails, TestRegistry_Get_AfterClose_ReturnsErrRegistryClosed, TestReaper_DoesNotReapDuringCreation) are scope-expansions covering edge cases the plan's `<acceptance_criteria>` and `<threat_model>` (T-05-CTX-LEAK) call out implicitly. No `Rule N` auto-fixes were necessary; no architectural changes (Rule 4) were considered.

## Issues Encountered

**Race detector flagged test-side LastUsed writes.** Initial reaper-test implementations wrote `e.LastUsed = time.Now().Add(-500*time.Millisecond)` directly from the test goroutine while the running reaper read `e.LastUsed` under `TryLock`. Resolution: wrap all test-side writes to `e.LastUsed` in `e.Mu.Lock()` / `e.Mu.Unlock()` so the race detector sees the same lock the reaper holds during its read. This matches production semantics (the surface handler holds e.Mu around Prompt + MarkUsed). Fixed in Task 2's reaper_test.go before commit `7df44ee`.

## User Setup Required

None — no external service configuration required for this plan. `SESSION_MAX` will need documentation in `docs/operating.md` (deferred to plan 05-03 / operator-doc tasks).

## Self-Check: PASSED

**File-existence verification:**

- FOUND: internal/session/testmain_test.go
- FOUND: internal/session/doc.go
- FOUND: internal/session/config.go
- FOUND: internal/session/registry.go
- FOUND: internal/session/reaper.go
- FOUND: internal/session/stats.go
- FOUND: internal/session/entry_acp.go
- FOUND: internal/session/testhelpers.go
- FOUND: internal/session/export_test.go
- FOUND: internal/session/registry_test.go
- FOUND: internal/session/reaper_test.go
- FOUND: internal/config/config.go (modified — SessionTTL/SessionMax added)

**Commit-existence verification:**

- FOUND: 431d186 (Task 0 — feat(05-02): scaffold internal/session package + env vars)
- FOUND: 068233e (Task 1 — feat(05-02): Registry.Get/Delete + Entry.SetModel + Stats/Detail)
- FOUND: 7df44ee (Task 2 — feat(05-02): reaper loop with TryLock skip + snapshot-then-iterate)

**Verification suite:**

- `go build ./internal/session/...` exits 0
- `go test ./internal/session/... -count=1 -race -timeout=120s` exits 0 (20 tests pass)
- `go vet ./internal/session/... ./internal/config/...` exits 0
- `go test ./...` exits 0 (full project — no regressions)
- All Task 0 / Task 1 / Task 2 grep gates from the plan's `<verify>` blocks resolve to existing source code.

## Next Phase Readiness

**Plan 05-03 unblocked.** The compile-time gate `var _ engine.ACPClient = (*Entry)(nil)` proves *Entry satisfies the engine.ACPClient interface — surface handlers can pass *Entry to engine.Run unchanged. Registry.Stats / Registry.Detail expose the producer side of D-15/D-16. session.NewEntryForTest is exported so plan 05-03 Task 3 adapter tests can construct entries without standing up a Registry. No blockers; no concerns.

---
*Phase: 05-pool-stateful-sessions*
*Plan: 02*
*Completed: 2026-05-26*
