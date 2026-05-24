---
phase: 02-ollama-end-to-end
plan: 05
subsystem: pool
tags: [pool, warmup, acp-slot, channel-of-slots, model-catalog, slot-release, codex-h6, codex-m2, codex-m3]

# Dependency graph
requires:
  - phase: 01-acp-client-foundation
    provides: "*acp.Client (Initialize/NewSession/SetModel/Prompt/Cancel/Close/AvailableModels) + acp.Stream (Chunks field + Result/CloseForTest)"
  - phase: 02-ollama-end-to-end
    provides: "Plan 01 canonical types (Block/Chunk/ModelInfo/FinalResult/StopReason); Plan 04 engine.ACPClient + engine.Stream interfaces; Plan 1.1 ACP wire alignment with D-12 NewSession-populates-models semantic"
provides:
  - "internal/pool: fixed-size warm pool of kiro-cli slots satisfying engine.ACPClient"
  - "Pool.Warmup with sequential D-07a fail-fast partial-cleanup + Codex H-6 model-catalog capture via slot-0 NewSession"
  - "ClientFactory + PoolClient interface seam (Codex M-2) — fake-injection for unit tests without real kiro-cli"
  - "poolStreamWrapper with sync.Once-guarded release on three terminal paths (Codex M-3): Result drained, ctx cancelled via watch goroutine, engine.Cancel called"
  - "Stats() snapshot {Size,Alive,Busy} for Plan 06 /health endpoint (OBSV-01)"
  - "Models() defensive-copy accessor backing Plan 06 /api/tags and /api/show"
  - "acp.NewStreamForTest + PushForTest + CloseForTest exported helpers (cross-package test seam)"
affects: ["02-06 server wiring", "phase 04 streaming adapters", "phase 05 session registry + dead-slot detection"]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Channel-of-slots pool (chan *Slot buffered to cfg.Size) per POOL-03 — Acquire = <-p.slots, Release = p.slots <- slot"
    - "ClientFactory interface + acpClientFactory default (production) + fakeClientFactory (tests) — Codex M-2 fake-injection seam"
    - "PoolClient interface mirrors *acp.Client subset; production *acp.Client satisfies structurally via compile-time `var _ PoolClient = (*acp.Client)(nil)`"
    - "Session→slot map (sessionSlots) with mu released BEFORE slot.Client method calls (never deadlock-serialise)"
    - "sync.Once-guarded release closure + map-delete-first race resolution across Result/ctx-cancel/Cancel terminal paths"
    - "Test-only accessors via internal/pool/export_test.go (`package pool` file compiled only under `go test`)"

key-files:
  created:
    - "internal/pool/config.go (Config + ClientFactory + PoolClient interfaces + acpClientFactory default + applyDefaults)"
    - "internal/pool/stats.go (Stats{Size,Alive,Busy})"
    - "internal/pool/pool.go (Pool + Slot types + Warmup + ACPClient surface + poolStreamWrapper)"
    - "internal/pool/pool_test.go (16 tests; M-2 fake-factory harness; M-3 multi-path release proof; H-6 model-capture proof)"
    - "internal/pool/export_test.go (WaitForSlotRelease/PutSlotBack/SessionSlotsLen test-only accessors)"
    - "internal/pool/testmain_test.go (goleak.VerifyTestMain gate)"
    - "internal/acp/stream_testhelpers.go (NewStreamForTest/PushForTest/CloseForTest cross-package test seam)"
  modified: []

key-decisions:
  - "Codex H-6 fix: model catalog captured AFTER slot 0 NewSession (not post-Initialize). Phase 1.1 D-12 populates client.models inside NewSession; AvailableModels returns nil until that runs. Warmup calls NewSession on slot 0 only, captures models under mu, then Cancel(sid) best-effort cleans up the warmup session — engine.Run creates fresh sessions per request."
  - "Codex M-2 ClientFactory + PoolClient interface seam: pool slot lifecycle (Warmup, Prompt routing, Cancel routing, Result release) previously untestable because *acp.Client is concrete. Now fakeClient implements pool.PoolClient and fakeClientFactory implements pool.ClientFactory, driving the pool through every lifecycle path without a real kiro-cli subprocess."
  - "Codex M-3 multi-path slot release: poolStreamWrapper holds a sync.Once-guarded releaseOnce that fires on (a) Result() drained — happy path; (b) ctx cancellation observed by a Prompt-spawned watch goroutine — Codex M-3 ctx-cancel path; (c) Pool.Cancel(sid) — Codex M-3 explicit-cancel path. Map-delete-first pattern in the release closure plus releaseSlotForSession in Cancel together guarantee exactly-one-release across the Cancel-vs-Result race."
  - "Pool imports internal/engine ONLY for the `var _ engine.ACPClient = (*Pool)(nil)` interface assertion. engine does NOT import pool, so no cycle (architectural-boundary safe per REQ-CI-04)."
  - "Slot.Client is typed as pool.PoolClient (interface), not *acp.Client (concrete). This is the load-bearing M-2 change — flips slot ownership from concrete to interface so tests can inject fakes without compromising production (the default factory still returns *acp.Client)."
  - "acp.NewStreamForTest + PushForTest + CloseForTest exported helpers (named to advertise test-only intent). Required because pool_test.go is blackbox (`package pool_test`) and cannot reach acp's unexported newStream. Pattern mirrors acp.NewWithConn (existing test-friendly constructor)."
  - "PoolClient name retained over revive's stutter rule (//nolint:revive justified inline): the naked `Client` name would collide visually with acp.Client in fake-harness tests, where `pool.Client` and `acp.Client` would read ambiguously."

patterns-established:
  - "ClientFactory interface for test-injection: any package that wraps a concrete subprocess client should expose a Factory seam if its lifecycle is non-trivially testable. Phase 5 dead-slot detection will benefit directly."
  - "Map-delete-first race resolution for shared-resource cleanup: when multiple terminal paths can race to free a resource, the first one to delete the lookup-key wins; the loser finds an empty entry and skips the cleanup (idempotent without locking the full critical section across the cleanup itself)."
  - "Stream-wrapper with ctx-watcher goroutine: when a caller-supplied ctx must release a resource even if the caller never drains the stream, spawn a goroutine that selects on ctx.Done vs a doneCh closed by the canonical release path. goleak gates catch leaks."
  - "Cross-package test-only constructors via name convention: `NewXForTest` / `PushForTest` etc. exported in non-_test.go files when blackbox tests need to drive internals. Less invasive than refactoring production to use interfaces purely for testability."

requirements-completed: [POOL-01, POOL-02, POOL-03, OBSV-01, SURF-03]

# Metrics
duration: 53min
completed: 2026-05-24
---

# Phase 02 Plan 05: Pool Skeleton + ACPClient Surface + Codex H-6/M-2/M-3 Fixes Summary

**Fixed-size warm pool of kiro-cli slots satisfying engine.ACPClient, with channel-of-slots semantics (POOL-03), NewSession-driven model-catalog capture per Codex H-6, ClientFactory fake-injection seam per Codex M-2, and exactly-once slot release across three terminal paths per Codex M-3.**

## Performance

- **Duration:** ~53 min
- **Started:** 2026-05-24T00:33Z (approximate, from worktree spawn)
- **Completed:** 2026-05-24T01:26:47Z
- **Tasks:** 3 (all auto, all green)
- **Files created:** 7 (6 under `internal/pool/`, 1 under `internal/acp/`)
- **Files modified:** 0
- **Lines added:** ~1,550

## Accomplishments

- **`internal/pool` package compiles, vets clean, lints clean, and tests green under `-race` with goleak gate** — 16 tests pass (1 skipped: integration test gated by `LOOP24_INTEGRATION=1` + kiro-cli on PATH).
- **`*pool.Pool` satisfies `engine.ACPClient`** via compile-time assertions in both production (`pool.go`) and test (`pool_test.go`) code; engine does NOT import pool (REQ-CI-04 architectural boundary preserved).
- **Codex H-6 model-catalog capture** lands as designed: Warmup calls `slot.Client.NewSession(ctx, cfg.KiroCWD)` on slot 0 only, captures `AvailableModels()` AFTER that NewSession populates the cached models (per Phase 1.1 D-12), then best-effort `Cancel(sid)` cleans up the warmup session. Verified by `TestPool_Warmup_CapturesModels`.
- **Codex M-2 ClientFactory + PoolClient seams** make pool lifecycle testable without spawning real `kiro-cli`. Production uses the default `acpClientFactory` (which calls `acp.New`); tests inject a `fakeClientFactory` returning `fakeClient`s implementing `pool.PoolClient`.
- **Codex M-3 multi-path slot release** verified at every terminal path: Result drained (`TestPool_Result_ReleasesSlot`), ctx cancelled (`TestPool_ContextCancel_ReleasesSlot`), engine-driven Cancel (`TestPool_Cancel_ReleasesSlot`), and Release-without-Result (`TestPool_StreamCloseWithoutResult_ReleasesSlot`). Double-release detection via channel-empty assertions confirms `sync.Once` + map-delete-first pattern resolves the Cancel-vs-Result race.

## Task Commits

Each task was committed atomically:

1. **Task 1: Pool skeleton — config + stats + pool.go core + testmain** — `106cb77` (`feat(02-05): pool skeleton + ClientFactory/PoolClient seams + Warmup model capture`)
2. **Task 2: ACPClient surface (NewSession/SetModel/Prompt/Cancel) + poolStreamWrapper + session→slot map** — `3d6265b` (`feat(02-05): pool ACPClient surface + poolStreamWrapper with multi-path slot release`)
3. **Task 3: pool_test.go + export_test.go + acp.NewStreamForTest cross-package helper** — `34ac7c7` (`test(02-05): pool_test.go + export_test.go + acp.NewStreamForTest helper`)

## Files Created

- `internal/pool/config.go` (128 lines) — `Config` struct (Logger/Size/KiroCmd/KiroArgs/KiroCWD/PingInterval/Factory) + `applyDefaults` (Size floors to 1, Factory defaults to `acpClientFactory{}`) + `PoolClient` interface (subset of `*acp.Client` methods) + `ClientFactory` interface + `acpClientFactory` default impl + compile-time `var _ PoolClient = (*acp.Client)(nil)`.
- `internal/pool/stats.go` (18 lines) — `Stats{Size, Alive, Busy}` struct.
- `internal/pool/pool.go` (470 lines) — `Slot` (Label + Client PoolClient) + `Pool` (cfg, slots chan, all, models, sessionSlots, mu, closed, closeOnce) + `New` + `Warmup` (Codex H-6) + `initSlot` + `Models` + `Stats` + `Close` + `closeAll` + `NewSession` (ctx-aware acquire, error-path release) + `SetModel` + `Prompt` (poolStreamWrapper + ctx-watcher goroutine) + `Cancel` (Codex M-3 release-on-cancel) + `releaseSlotForSession` + `poolStreamWrapper` (sync.Once releaseOnce + Chunks/Result/Release methods) + `var _ engine.ACPClient = (*Pool)(nil)` + `var _ engine.Stream = (*poolStreamWrapper)(nil)`.
- `internal/pool/pool_test.go` (814 lines) — 16 tests (15 passing + 1 skipped without LOOP24_INTEGRATION); `fakeClient`/`fakeClientFactory` Codex M-2 harness; `drainChunks` helper; `warmedPoolWithFakes` setup; covers compile-time interface assertion, Config defaults, Warmup happy + fail-fast paths, real-kiro soft-integration gate, Close idempotency, Stats race-freedom, NewSession-without-Warmup ctx-cancel, model-catalog capture, session→slot routing, all four M-3 release paths, double-release detection.
- `internal/pool/export_test.go` (46 lines) — `Pool.WaitForSlotRelease` / `Pool.PutSlotBack` / `Pool.SessionSlotsLen` test-only accessors (standard Go test-export pattern; `package pool` file compiled only under `go test`).
- `internal/pool/testmain_test.go` (20 lines) — `goleak.VerifyTestMain` gate (verbatim copy from `internal/acp/testmain_test.go` with package rename).
- `internal/acp/stream_testhelpers.go` (51 lines) — `NewStreamForTest(sessionID)` + `(*Stream).PushForTest(chunk)` + `(*Stream).CloseForTest(result, err)` exported helpers. Named to advertise test-only intent. Required because `pool_test.go` is blackbox (`package pool_test`) and cannot reach acp's unexported `newStream`.

## Decisions Made

All seven `key-decisions` listed in the frontmatter; the four load-bearing ones recapped:

- **Codex H-6** — model catalog captured via slot-0 NewSession (not post-Initialize). Phase 1.1 D-12 populates `client.models` inside `NewSession`; `AvailableModels` returns nil until that runs. Warmup body explicitly orders `NewSession → AvailableModels → Cancel(sid)` on slot 0 only.
- **Codex M-2** — `ClientFactory` + `PoolClient` interfaces in `config.go`. Production uses default `acpClientFactory` wrapping `acp.New`. Tests inject `fakeClientFactory` returning `fakeClient`s. `Slot.Client` is `PoolClient` (interface), not `*acp.Client` (concrete). Compile-time `var _ PoolClient = (*acp.Client)(nil)` proves production parity.
- **Codex M-3** — `poolStreamWrapper` with sync.Once-guarded `releaseOnce` + ctx-watcher goroutine + `Release()` method. Slot release fires on Result drained / ctx cancelled / `Pool.Cancel` called — exactly once across all three paths via map-delete-first race resolution.
- **Engine boundary direction** — `internal/pool` imports `internal/engine` only for the compile-time interface assertion. `internal/engine` does NOT import `internal/pool`, preserving REQ-CI-04 architectural-boundary direction.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking lint] errcheck on `p.closeAll()` discarded return value**
- **Found during:** Task 1 commit (pre-commit golangci-lint failure)
- **Issue:** The plan's Warmup body has `p.closeAll()` without checking the return value. golangci-lint's `errcheck` linter (enabled per `docs/briefs/go_port_brief.md` §3.12 trust gates) treats this as a hard failure.
- **Fix:** Explicit discard via `_ = p.closeAll()` at both call sites in Warmup, with a comment justifying that closeAll errors during partial-cleanup are intentionally subordinate to the primary Warmup error.
- **Files modified:** `internal/pool/pool.go`
- **Verification:** golangci-lint exits 0
- **Committed in:** `106cb77` (Task 1)

**2. [Rule 3 — Blocking lint] revive `exported: type name will be used as pool.PoolClient by other packages, and that stutters`**
- **Found during:** Task 1 commit (pre-commit golangci-lint failure)
- **Issue:** revive's exported-naming rule treats `pool.PoolClient` as stutter and recommends renaming to `pool.Client`.
- **Fix:** Retained the `PoolClient` name (the plan explicitly mandates it) and added `//nolint:revive` with an inline justification: a naked `Client` name would collide visually with `*acp.Client` in fake-harness tests, where `pool.Client` and `acp.Client` would read ambiguously. Documented in the type's godoc.
- **Files modified:** `internal/pool/config.go`
- **Verification:** golangci-lint exits 0
- **Committed in:** `106cb77` (Task 1)

**3. [Rule 2 — Missing critical functionality] `acp.NewStreamForTest` / `PushForTest` / `CloseForTest` cross-package test seam**
- **Found during:** Task 3 (writing pool_test.go)
- **Issue:** The plan's pool_test.go is `package pool_test` (blackbox per D-18) and needs to drive `*acp.Stream` instances inside `fakeClient.Prompt`. acp's `newStream` is unexported; no existing test-only exported constructor in the acp package. The plan's fallback note suggested skipping deep stream tests if neither path existed.
- **Fix:** Added `internal/acp/stream_testhelpers.go` exporting `NewStreamForTest(sessionID)` + `(*Stream).PushForTest(chunk)` + `(*Stream).CloseForTest(result, err)`. Named to advertise test-only intent (mirrors `acp.NewWithConn` precedent). Allowed the M-3 slot-release tests to fire as designed instead of skipping them.
- **Files modified:** `internal/acp/stream_testhelpers.go` (new)
- **Verification:** All M-3 release tests pass under `-race`; goleak gate clean; acp's own tests unaffected.
- **Committed in:** `34ac7c7` (Task 3)

**4. [Rule 1 — Test logic bug] Three failing tests on first run, all in pool_test.go**
- **Found during:** Task 3 (initial test run)
- **Issue:**
  - `TestPool_Cancel_RoutesToCorrectSlot` asserted total Cancel calls == 1 but didn't account for the warmup-time `Cancel("sess-warmup")` that fires on slot 0 (visible as `fc0=2, fc1=0` in the failure).
  - `TestPool_StreamCloseWithoutResult_ReleasesSlot` + `TestPool_Cancel_ReleasesSlot` both used a `PutSlotBack(slot)` → `WaitForSlotRelease(extra)` pattern to assert no double-release, but the put-back made the channel non-empty so the "extra" check fired falsely.
- **Fix:**
  - The Cancel-routing test now snapshots `cancelCallList()` BEFORE the test Cancel and compares the diff so warmup-time Cancels are excluded.
  - The two double-release tests now leave the slot OUT of the channel during the post-second-terminal-path assertion, so a real double-release would be visible as a slot landing in an empty channel. The slot is put back at the end for clean shutdown.
- **Files modified:** `internal/pool/pool_test.go`
- **Verification:** All 16 tests pass under `-race`; goleak gate clean; lint clean.
- **Committed in:** `34ac7c7` (Task 3)

**5. [Rule 3 — Blocking lint] Six golangci-lint findings on pool_test.go (errcheck on Close, unused-parameter, empty-block)**
- **Found during:** Task 3 (post-test golangci-lint pass)
- **Issue:** `defer p.Close()` discarded error (errcheck, 3 occurrences); `fakeClientFactory.Spawn` had unused `ctx` parameter (revive); two `for range stream.Chunks() {}` empty-block bodies (revive).
- **Fix:** `defer func() { _ = p.Close() }()`; renamed `ctx` → `_` in Spawn; extracted `drainChunks(ch)` helper with single nolint-justified empty body.
- **Files modified:** `internal/pool/pool_test.go`
- **Verification:** golangci-lint exits 0 on full project.
- **Committed in:** `34ac7c7` (Task 3)

---

**Total deviations:** 5 auto-fixed (3 Rule 3 blocking lint, 1 Rule 2 missing test seam, 1 Rule 1 test logic bug).
**Impact on plan:** All deviations were necessary corrections — three were lint/build blockers, one added the cross-package test seam the plan's fallback note explicitly anticipated, and one was a test-logic bug discovered on first run and fixed in the same task. No scope creep; no production code changes beyond what the plan specified.

## Issues Encountered

None beyond the deviations above. The H-6 / M-2 / M-3 design instructions in the plan were detailed enough that the production code landed on the first attempt; the test-logic bugs (item 4 above) were caught by `go test` on the first run and fixed immediately.

## User Setup Required

None — no external service configuration required. The pool's only external dependency is `kiro-cli`, and that is exercised only when `LOOP24_INTEGRATION=1` is set in the environment (gated test `TestPool_Warmup_SkipsWithoutKiroBinary`).

## Next Phase Readiness

- Plan 06 (`02-06`) can now wire `pool.New(...)` + `pool.Warmup(ctx)` into `main.go` BEFORE `srv.RunUntilSignal(...)` to satisfy POOL-02 (warmup-before-listen). Plan 06's main_test should assert that ordering at the harness level.
- Plan 06 can expose `pool.Models()` to the Ollama `/api/tags` and `/api/show` handlers via the ModelCatalog interface (D-13 model surface).
- Plan 06 can call `pool.Stats()` from the `/health` endpoint (OBSV-01).
- Phase 5 dead-slot detection + session registry can extend `internal/pool/pool.go` by adding a per-slot `pingLoop` goroutine and a `SessionRegistry` (or by promoting `sessionSlots` to a TTL-aware map). The current `ClientFactory` seam supports a future `respawn` path without changing the production interface.

## Self-Check: PASSED

**Created files (all present):**
- `internal/pool/config.go`
- `internal/pool/stats.go`
- `internal/pool/pool.go`
- `internal/pool/pool_test.go`
- `internal/pool/export_test.go`
- `internal/pool/testmain_test.go`
- `internal/acp/stream_testhelpers.go`

**Commits (all present in git log):**
- `106cb77` Task 1 — pool skeleton
- `3d6265b` Task 2 — ACPClient surface
- `34ac7c7` Task 3 — pool_test.go + export_test.go + acp.NewStreamForTest

**Verification results:**
- `go build ./internal/pool/...` → exits 0
- `go test -race -count=1 ./internal/pool/...` → 16 PASS, 1 SKIP (integration gated), 0 FAIL, 0 DATA RACE
- `go test -race -count=1 ./...` → all packages green
- `golangci-lint run ./...` → 0 issues
- Compile-time interface assertions: `var _ engine.ACPClient = (*Pool)(nil)` (production, pool.go) + `var _ engine.ACPClient = (*pool.Pool)(nil)` (test, pool_test.go) + `var _ PoolClient = (*acp.Client)(nil)` (compile-time structural check, config.go) + `var _ engine.Stream = (*poolStreamWrapper)(nil)` (production, pool.go) — all four landing.

---
*Phase: 02-ollama-end-to-end*
*Completed: 2026-05-24*
