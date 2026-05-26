---
phase: 05-pool-stateful-sessions
plan: 01
subsystem: pool
tags:
  - pool
  - acp
  - dead-slot
  - exit-watcher
  - POOL-01
  - POOL-04
  - D-01
  - D-02
  - D-03
  - D-15
dependency_graph:
  requires:
    - internal/acp.Client (Phase 1 / 1.1)
    - internal/pool.Pool (Phase 2; warm pool + Codex M-2 fake-factory + Codex M-3 release races)
    - internal/config (Phase 1; env-var loaders)
  provides:
    - acp.Client.Done() <-chan struct{} (push exit signal for dead-slot detection)
    - pool.Slot.dead bool flag (guarded by p.mu)
    - pool.Pool.closing chan struct{} (closed by Close() for watcher exit)
    - pool.Pool.respawnSlot / removeSlot / slotAlive helpers
    - pool.Pool.startExitWatcher per-slot goroutine
    - pool.AgentSlot row shape + pool.Pool.Detail() consumer hook for /health/agents (plan 05-03)
    - POOL_SIZE env default = 4 (POOL-01 Node parity)
  affects:
    - cmd/otto-gateway/main.go (will consume Detail in plan 05-03)
    - internal/server (will wire /health/agents in plan 05-03)
tech-stack:
  added: []
  patterns:
    - "Push-based exit signal via context.Context.Done() exposed as <-chan struct{} accessor (Phase 5 D-01)"
    - "Two-branch select goroutine: <-slot.Client.Done() vs <-p.closing for clean shutdown"
    - "Close-old-then-spawn-new ordering in respawnSlot — old watcher exits via its own Done branch (Pitfall 2)"
    - "Short critical sections under p.mu — no client method calls while holding the lock"
    - "Snapshot-then-iterate for Detail() to avoid aliasing internal state"
    - "JSON-tag-locked wire contract validated via reflect.Type tag inspection (D-15)"
key-files:
  created:
    - internal/pool/exit_watcher.go
    - internal/pool/exit_watcher_test.go
    - internal/pool/detail.go
  modified:
    - internal/acp/client.go (Done() accessor)
    - internal/acp/client_test.go (4 Done() tests)
    - internal/pool/pool.go (Slot.dead, Pool.closing, respawnSlot, removeSlot, slotAlive, NewSession dead-slot branch, initSlot watcher spawn, Close ordering)
    - internal/pool/config.go (PoolClient.Done() interface method)
    - internal/pool/export_test.go (SlotAlive, AllSlotsSnapshot, ClosingChan accessors)
    - internal/pool/pool_test.go (12 new tests + fakeClient.Done()/fireDone + scriptedFailingFactory + ctxGatingFactory)
    - internal/config/config.go (POOL_SIZE env default 1→4)
    - internal/config/config_test.go (TestLoad_PoolSize_Default updated to expect 4)
decisions:
  - "Done() is a one-line accessor over the existing private clientCtx — no new fields, no new goroutines, no new supply-chain surface."
  - "Slot.dead is guarded by p.mu (lowercase package-internal). The exit-watcher acquires p.mu ONLY for the assignment; no slot.Client calls under p.mu (anti-pattern per 05-RESEARCH.md)."
  - "close(p.closing) is the FIRST line of Pool.Close, BEFORE closeAll. This deterministically lets watcher goroutines win the select against <-slot.Client.Done() that would otherwise fire from closeAll's slot.Client.Close() calls."
  - "POOL_SIZE env default flipped 1→4 ONLY at the env-load layer (internal/config/config.go). Package-level default in internal/pool/config.go applyDefaults stays at 1 because pool tests construct pool.Config{} directly and expect Size=1 when unset."
  - "respawnSlot closes the OLD client FIRST (Pitfall 2), then spawns+initializes the NEW client (honoring ctx for D-02), then swaps under p.mu, then spawns a fresh watcher. Old watcher exits via its <-slot.Client.Done() branch as the OLD Close cascades into Done()."
  - "On respawn failure removeSlot drops the slot from p.all (D-03 — pool effective size shrinks). Loud single-request failure preferred over silent capacity loss."
  - "AgentSlot.CurrentSessionID is *string (not string) so an idle slot renders as `\"current_session_id\": null` (D-15 example shape) instead of `\"\"`."
  - "Pool.Detail() empty pool returns empty slice (not nil) so the downstream handler encodes `\"slots\": []` rather than `\"slots\": null`."
metrics:
  duration: ~25 minutes (single execution wave)
  completed_date: 2026-05-26
  tasks_completed: 3
  files_created: 3
  files_modified: 8
  tests_added: 16
---

# Phase 05 Plan 01: Dead-Slot Detection + Lazy Synchronous Respawn Summary

Slice A end-to-end: a per-slot exit watcher observes acp.Client.Done(); Pool.NewSession's dead-slot branch synchronously respawns or shrinks the pool; Pool.Detail() emits the D-15 row shape for the upcoming /health/agents handler in plan 05-03. POOL_SIZE env default flipped 1→4 for Node parity (POOL-01).

## Task Outcomes

### Task 1 — acp.Client.Done() + dead-slot scaffolding + POOL_SIZE flip

- `acp.Client.Done() <-chan struct{}` added as a sibling to `Close()`; one-line accessor over `c.clientCtx.Done()`. No new fields or goroutines. The channel closes via Close() step 1's `c.cancel()` AND via readLoop's `defer c.cancel()` on EOF — so both Close() and subprocess-crash paths fire Done().
- 4 tests added (`TestClient_Done_FiresOnClose`, `TestClient_Done_FiresOnPingLoopKill`, `TestClient_Done_DoesNotFireBeforeClose`, `TestClient_Done_IdempotentMultipleReaders`). All pass with -race and goleak.
- `Slot.dead bool` added to internal/pool/pool.go (guarded by p.mu) with doc-comment naming the watcher and consumer call sites.
- `Pool.closing chan struct{}` added; allocated in `New()`; `close(p.closing)` is the FIRST line of `Close()`'s closeOnce body (BEFORE closeAll) — the Pattern 2 ordering from 05-PATTERNS.md §Insertion 3.
- `internal/config/config.go`: `getEnvInt("POOL_SIZE", 1)` → `getEnvInt("POOL_SIZE", 4)` with a multi-line comment citing POOL-01 + Node parity + the rationale that pool/config.go's package default stays at 1.
- `internal/config/config_test.go`: `TestLoad_PoolSize_Default` updated to assert PoolSize == 4 (was 1).
- Commit: `08bdac3 feat(05-01): add acp.Client.Done() exit signal + dead-slot scaffolding + POOL_SIZE=4 default`

### Task 2 — Per-slot exit watcher + dead-slot detection + lazy synchronous respawn

- **`internal/pool/exit_watcher.go` (NEW):** `startExitWatcher(slot *Slot)` per-slot goroutine with two-branch select on `<-slot.Client.Done()` (mark dead under p.mu + log) vs `<-p.closing` (return cleanly). Logs `"pool: slot died"` with `slot.Label`. The watcher holds p.mu ONLY for the slot.dead assignment.
- **`PoolClient` interface (config.go):** gains `Done() <-chan struct{}` so `*acp.Client` and the test `fakeClient` both satisfy it.
- **`Pool.respawnSlot(ctx, slot) error`:** close OLD client → Spawn NEW client (honors ctx for D-02) → Initialize → swap under p.mu (resetting `slot.dead = false`) → spawn fresh watcher. Returns wrapped error on any failure.
- **`Pool.removeSlot(slot)`:** splices slot out of p.all under p.mu (D-03 — pool effective size shrinks).
- **`Pool.slotAlive(slot) bool`:** short critical-section read of `!slot.dead`.
- **`Pool.NewSession` modification:** inserts dead-slot branch between Acquire and the existing `slot.Client.NewSession` call: `if !p.slotAlive(slot) { if err := p.respawnSlot(ctx, slot); err != nil { p.removeSlot(slot); return "", fmt.Errorf("pool: respawn slot %s: %w", slot.Label, err) } }`.
- **`Pool.initSlot`:** spawns initial-warmup watcher at end via `p.startExitWatcher(slot)` so the first Spawn-Initialize pair has a watcher from the moment the slot is returned.
- **`internal/pool/export_test.go`:** adds `SlotAlive(label) (alive, found bool)`, `AllSlotsSnapshot() []*Slot`, and `ClosingChan() <-chan struct{}`.
- **`internal/pool/exit_watcher_test.go` (NEW, whitebox):** `TestExitWatcher_FiresOnClientDone` + `TestExitWatcher_ExitsOnPoolClose` using a minimal `watcherTestClient` to drive the goroutine directly.
- **`internal/pool/pool_test.go`:** `fakeClient` gains `doneCh chan struct{}` (lazily allocated under doneMu) + `Done()` + `fireDone()` (idempotent close). 5 new dead-slot tests (`TestPool_DeadSlot_LazyRespawn`, `TestPool_DeadSlot_RespawnFailure_PoolShrinks`, `TestPool_DeadSlot_RespawnRespectsCtxCancel`, `TestPool_DeadSlot_ConcurrentAcquiresUnaffected`, `TestPool_ExitWatcher_RespawnSpawnsNewWatcher`). Two new factory helpers: `scriptedFailingFactory` (succeeds N times then errors) and `ctxGatingFactory` (gates Spawn on a channel + caller ctx for D-02 testing).
- Commit: `02041f1 feat(05-01): per-slot exit watcher + dead-slot lazy synchronous respawn (POOL-04, D-01..D-03)`

### Task 3 — Pool.Detail() per-slot rows for /health/agents (D-15)

- **`internal/pool/detail.go` (NEW):**
  - `type AgentSlot struct { Label string `json:"label"`; Alive bool `json:"alive"`; Busy bool `json:"busy"`; CurrentSessionID *string `json:"current_session_id"` }` — the D-15 row shape verbatim.
  - `func (p *Pool) Detail() []AgentSlot` — snapshots under p.mu; inverts `p.sessionSlots` (sid→slot) into `slotToSID` (slot→sid) so each row's Busy + CurrentSessionID can render. Defensive sid copy per row so concurrent loop iterations don't alias the loop variable. Empty pool returns empty slice (not nil).
- **`internal/pool/pool_test.go`:** 5 new tests:
  - `TestPool_Detail_HealthyPool` (4 alive idle slots → 4 rows, all `{Alive:true, Busy:false, CurrentSessionID:nil}`)
  - `TestPool_Detail_OneBusyOneDead` (slot-0 holds session "sess-X", slot-1 dead; assertions on Busy + Alive + CurrentSessionID)
  - `TestPool_Detail_AfterShrinkOnRespawnFailure` (pre-shrink len=1, post-shrink len=0 — D-03 verification)
  - `TestPool_Detail_NilSafeOnEmptyPool` (Detail() before Warmup returns `[]AgentSlot{}`, not nil)
  - `TestPool_Detail_FieldShape_MatchesD15` (reflect-based assertion on the JSON tags — locks the D-15 wire contract at compile-test boundary)
- Commit: `839173a feat(05-01): Pool.Detail() AgentSlot rows for /health/agents (D-15)`

## Verification Results

```
go test ./internal/acp/... -count=1 -timeout=60s -race      → ok
go test ./internal/pool/... -count=1 -timeout=120s -race    → ok (goleak gate intact)
go test ./internal/config/... -count=1 -timeout=30s         → ok
go test ./... -count=1 -timeout=180s                        → ok (all 11 packages green)
go vet ./...                                                → clean
```

All grep gates pass:

- `func (c *Client) Done() <-chan struct{}` in `internal/acp/client.go`
- `dead bool` in `internal/pool/pool.go`
- `closing chan struct{}` in `internal/pool/pool.go`
- `close(p.closing)` inside `Close()` body
- `getEnvInt("POOL_SIZE", 4)` in `internal/config/config.go`
- `func (p *Pool) startExitWatcher(slot *Slot)` in `internal/pool/exit_watcher.go`
- `<-slot.Client.Done()` and `<-p.closing` branches in exit_watcher.go
- `func (p *Pool) respawnSlot` + `func (p *Pool) removeSlot` in `internal/pool/pool.go`
- Two `p.startExitWatcher(slot)` invocation sites (initSlot + respawnSlot)
- `if !p.slotAlive(slot)` inside NewSession body
- `func (p *Pool) Detail() []AgentSlot` in `internal/pool/detail.go`
- All four JSON tags (`label`, `alive`, `busy`, `current_session_id`) present

## Deviations from Plan

None — plan executed exactly as written. All three tasks followed 05-PATTERNS.md verbatim. The PoolClient interface gained a `Done()` method (documented in the plan via the implicit need for fakeClient.Done() to satisfy the watcher) and `internal/config/config_test.go` needed a one-line test-value update (1 → 4) to track the env-default flip — both expected ripple effects, not deviations.

## Success Criteria

- ✅ POOL-01: POOL_SIZE env default = 4 (grep + config tests)
- ✅ POOL-02: Pool warmup behavior unchanged (existing TestPool_Warmup_* tests green)
- ✅ POOL-03: Acquire path unchanged when slot is alive (existing TestPool_NewSession_* green)
- ✅ POOL-04: Dead slots detected push-side (TestExitWatcher_FiresOnClientDone) AND lazy-respawned synchronously (TestPool_DeadSlot_LazyRespawn) AND failure shrinks pool (TestPool_DeadSlot_RespawnFailure_PoolShrinks) AND concurrent acquires unblocked (TestPool_DeadSlot_ConcurrentAcquiresUnaffected)
- ✅ D-01 invariant: goleak passes after Pool.Close (TestExitWatcher_ExitsOnPoolClose) and after respawn (TestPool_ExitWatcher_RespawnSpawnsNewWatcher)
- ✅ D-15 producer side: Pool.Detail() emits the locked row shape (TestPool_Detail_FieldShape_MatchesD15)
- ✅ Race-detector green on `./internal/pool/...`
- ✅ No new lint findings (`go vet ./...` clean)
- ✅ Phase 2 + Phase 4 regression check: all packages pass

## Threat Mitigations Applied

- **T-05-06-A (goroutine leak):** per-slot watcher exits on `<-p.closing` OR successful re-spawn. goleak gate in `internal/pool/testmain_test.go` enforces. TestExitWatcher_ExitsOnPoolClose + TestPool_ExitWatcher_RespawnSpawnsNewWatcher cover both paths.
- **T-05-07 (subprocess re-spawn under load):** documented acceptance; respawn synchronous so one caller pays the warmup latency. Pool.Detail() (this plan) gives operators visibility into pool shrink in plan 05-03's /health/agents endpoint.
- **T-05-DOS-RESPAWN-FAILURE:** mitigation applied — D-03 path drops the slot from p.all and surfaces a wrapped typed error suitable for 503 rendering. TestPool_DeadSlot_RespawnFailure_PoolShrinks covers.
- **T-POOL-CTX-IGNORE:** mitigation applied — respawnSlot honors ctx via the ctxGatingFactory test (TestPool_DeadSlot_RespawnRespectsCtxCancel). Caller ctx-cancel returns within 500ms even when Spawn is blocked.
- **T-ACP-DONE-SC:** accepted — one-line accessor adds zero supply-chain surface.

## What Plan 05-02 / 05-03 Consume

- Plan 05-02 (session registry): not directly dependent on Slice A; runs in the same wave.
- Plan 05-03 (/health/agents handler): consumes `pool.AgentSlot` rows via `pool.Pool.Detail()`. The compile-time assertion that `*pool.Pool` satisfies a new `server.AgentDetailSource` interface lives in plan 05-03 — this plan ships the producer side only.

## Self-Check: PASSED

All 8 claimed files exist; all 3 task commits (08bdac3, 02041f1, 839173a) found in git log.
