---
quick_id: 260721-ovm
mode: quick
status: complete
---

# Per-slot TURNS and UP stat cells on admin dashboard slot cards

Add per-slot TURNS (turn count vs recycle threshold) and UP (uptime since
worker spawn) stat cells to the admin dashboard's slot cards, so operators can
watch worker recycling happen live. Plus: no-cache headers on admin static
assets so dashboard JS/CSS changes are picked up without a hard refresh.

## Task 1: Pool — track spawnedAt, extend AgentSlot wire shape

**Files:**
- Modify: `internal/pool/pool.go` (`Slot` struct, `initSlot`, `respawnSlot` step 4)
- Modify: `internal/pool/detail.go` (`AgentSlot`, `Detail()`)
- Modify: `internal/pool/pool_test.go` (`TestPool_Detail_FieldShape_MatchesD15` field-shape lock, `TestPool_Detail_HealthyPool` turns/spawned_at assertions)
- Modify: `internal/pool/worker_recycle_test.go` (`TestPool_WorkerRecycleAtThreshold` — assert Detail() turns==0 and spawned_at advanced after recycle)

**Action:** Add `spawnedAt time.Time` to `Slot` (guarded by `p.mu`), set in
`initSlot` at slot construction and in `respawnSlot`'s step-4 swap critical
section (same statement block that resets `slot.turns = 0`). Add `Turns int`
(`json:"turns"`) and `SpawnedAt *time.Time` (`json:"spawned_at"`) to
`AgentSlot`; populate both in `Detail()` under `p.mu`, using the same
defensive-copy convention as `CurrentSessionID` (null when zero). Update the
D-15 field-shape lock test to expect the two new tags — this is an
intentional, additive wire-shape change to the D-15 `/health/agents` contract
per this feature's explicit requirement (task item 1); `internal/server`'s own
`AgentSlot` type / D-14 contract is untouched (out of scope).

**Verify:** `go test -race ./internal/pool -count=1`

**Done:** Detail() rows carry Turns (warmup probe counted) and non-nil
SpawnedAt after warmup; after a threshold recycle completes, the recycled
slot's Turns == 0 and SpawnedAt is strictly later than its pre-recycle value.

## Task 2: Admin wire — SnapshotSlot/SnapshotPool + main.go adapter

**Files:**
- Modify: `internal/admin/snapshot.go` (`SnapshotSlot`, `SnapshotPool`, `snapshotHandler`)
- Modify: `internal/admin/snapshot_test.go` (extend `TestAdmin_SnapshotHandler` or add a case asserting `max_turns` + per-slot `turns`/`spawned_at`)
- Modify: `cmd/otto-gateway/main.go` (`adminPoolDetailAdapter`)

**Action:** Add `Turns int` (`json:"turns"`) and `SpawnedAt *time.Time`
(`json:"spawned_at"`) to `SnapshotSlot`; add `MaxTurns int` (`json:"max_turns"`)
to `SnapshotPool`, filled in `snapshotHandler` from the already-wired
`h.deps.KiroWorkerMaxTurns`. In `main.go`, `adminPoolDetailAdapter` currently
sources rows from `server.PoolDetailSource` (`[]server.AgentSlot`), which
lacks Turns/SpawnedAt (that's the separate, untouched D-14 contract). Switch
`adminPoolDetailAdapter.Detail()` to read directly from `a.pool.Detail()`
(`[]pool.AgentSlot`, already extended by Task 1) instead of `a.src.Detail()`,
copying Turns/SpawnedAt through; drop the now-unused `src` field from the
adapter struct and its construction site. Additive JSON only on
`SnapshotSlot`/`SnapshotPool` — no renames/reorders of existing fields.

**Verify:** `go test ./internal/admin ./cmd/otto-gateway -count=1`

**Done:** `GET /admin/api/snapshot` response JSON contains `pool.max_turns`
and per-slot `turns`/`spawned_at`, verified via a fake `PoolDetailSource` with
a distinctive `MaxTurns` value.

## Task 3: Dashboard JS — TURNS/UP cells + no-cache static assets

**Files:**
- Modify: `internal/admin/static/js/admin.js` (`buildSlotPerf`, `slotCardChildren`, `buildSlotCard`, `updateSlotCard`, `renderSlots`, `fetchSnapshot` call site; new duration formatter)
- Modify: `internal/admin/admin.go` (`/static/*` handler — set `Cache-Control: no-cache`)
- Modify: `internal/admin/handlers_test.go` (`TestAdmin_StaticServes_JS` — assert `Cache-Control: no-cache`)

**Action:** In `admin.js`, thread `maxTurns` down the same parameter chain
`poolFailed` already uses (`renderSlots` → `buildSlotCard`/`updateSlotCard` →
`slotCardChildren` → `buildSlotPerf`). In `buildSlotPerf`, after the
CPU/Mem cells (inside the `if (slot.stat_ok)` branch AND in the `else`
"perf n/a" branch — the two new cells must render for every live card
regardless of `stat_ok`), append two `perfStat`-built cells with no
sparklines: `TURNS` (text `N / MAX` when `maxTurns > 0`, else `N`) and `UP`
(a new compact duration formatter — NOT `relativeTime`, which is
"ago"-suffixed — showing time since `slot.spawned_at`, computed as
`Date.now() - Date.parse(slot.spawned_at)`; renders `n/a` when
`spawned_at` is null/absent). Vacant cards stay unchanged (perf block
already omitted by `slotCardChildren`'s `if (!slot.vacant)` guard). Pass
`snap.pool.max_turns` from `fetchSnapshot` into `renderSlots`. In
`internal/admin/admin.go`'s `/static/*` handler, set
`w.Header().Set("Cache-Control", "no-cache")` before `http.ServeFileFS` — do
not touch the SSE/snapshot handlers' existing headers.

**Verify:** `go test ./internal/admin -count=1`; manual read-through of the
new JS for ES5 compliance (var/function only, no arrow functions/const/let).

**Done:** Every live slot card (busy, idle, recovering/failed) shows TURNS and
UP cells that update each poll; vacant cards unchanged; `GET
/admin/static/js/admin.js` responds with `Cache-Control: no-cache`.
