---
quick_id: 260721-ovm
mode: quick
status: complete
completed: 2026-07-21
---

# Per-slot TURNS and UP stat cells on admin dashboard slot cards — summary

Added per-slot TURNS (turn count vs `KIRO_WORKER_MAX_TURNS`) and UP (uptime
since worker spawn) stat cells to the admin dashboard's slot cards, so
operators can watch worker recycling happen live — turns climb toward the
threshold, then both reset together when a recycle completes. Plus a related
fix: `Cache-Control: no-cache` on `/admin/static/*` so dashboard JS/CSS
changes are picked up without a hard refresh.

## What changed

- `internal/pool/pool.go`: `Slot.spawnedAt time.Time`, set in `initSlot` and
  reset in `respawnSlot`'s step-4 swap critical section (same statement block
  as the `turns = 0` reset).
- `internal/pool/detail.go`: `AgentSlot` (D-15 `/health/agents` wire shape)
  gains `Turns int` (`json:"turns"`) and `SpawnedAt *time.Time`
  (`json:"spawned_at"`, null-when-zero, defensive-copied like
  `CurrentSessionID`); `Detail()` populates both under `p.mu`. This is an
  intentional, additive change to the D-15 contract per this feature's
  explicit requirement — `internal/server`'s separate D-14 `AgentSlot` type
  is untouched.
- `internal/admin/snapshot.go`: `SnapshotSlot` += `Turns`/`SpawnedAt`;
  `SnapshotPool` += `MaxTurns int` (`json:"max_turns"`), filled in
  `snapshotHandler` from the already-wired `Deps.KiroWorkerMaxTurns`.
- `cmd/otto-gateway/main.go`: `adminPoolDetailAdapter` now reads
  `a.pool.Detail()` (`[]pool.AgentSlot`) directly instead of going through
  `server.PoolDetailSource` (`[]server.AgentSlot`, the separate locked D-14
  contract that doesn't carry these fields) — dropped the now-unused `src`
  field.
- `internal/admin/static/js/admin.js`: `buildSlotPerf` appends TURNS ("N /
  MAX" or "N") and UP (new `formatUptime` duration formatter — distinct from
  `relativeTime`'s "ago" suffix — rendering "n/a" when `spawned_at` is absent)
  cells, unconditionally (even when `stat_ok` is false, so a recovering/dead
  card's row is never empty). `maxTurns` threaded down the same parameter
  chain `poolFailed` already uses: `fetchSnapshot` → `renderSlots` →
  `buildSlotCard`/`updateSlotCard` → `slotCardChildren` → `buildSlotPerf`. No
  sparklines on the new cells (`perfStat` called with a null values array,
  which `buildSparkline` already renders as an empty, invisible `<svg>`).
  Vacant cards unchanged (perf block already gated on `!slot.vacant`). ES5
  only (var/function, no arrows/let/const) — verified with `node -c` and a
  manual grep.
- `internal/admin/admin.go`: the `/static/*` handler now sets
  `Cache-Control: no-cache` before `http.ServeFileFS`; SSE/snapshot handler
  headers untouched.

## RED/GREEN evidence

RED (pre-implementation, confirmed failing/not-compiling before the
corresponding production code existed):

```
$ go vet ./internal/pool/...
vet: internal/pool/pool_test.go:1332:10: row.Turns undefined (type pool.AgentSlot has no field or method Turns)

$ go vet ./internal/admin/...
internal/admin/snapshot_test.go:34:72: unknown field Turns in struct literal of type SnapshotSlot
internal/admin/snapshot_test.go:103:15: snap.Pool.MaxTurns undefined (type SnapshotPool has no field or method MaxTurns)
... (truncated — all new fields/assertions failed to compile)

$ go test ./internal/admin -run TestAdmin_StaticServes_JS -count=1 -v
--- FAIL: TestAdmin_StaticServes_JS (0.00s)
    handlers_test.go:309: Cache-Control: want "no-cache", got ""
```

GREEN (post-implementation):

```
$ go test -race ./internal/pool -count=1
ok  	otto-gateway/internal/pool	36.398s

$ go test ./internal/admin ./internal/metrics ./cmd/otto-gateway -count=1
ok  	otto-gateway/internal/admin	17.413s
ok  	otto-gateway/internal/metrics	0.420s
ok  	otto-gateway/cmd/otto-gateway	0.496s

$ go test ./... -count=1
(all packages ok — full-repo regression sweep)

$ go run mvdan.cc/gofumpt@latest -l internal cmd
(empty)

$ go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...
0 issues.
```

## Test coverage added

- `TestPool_Detail_HealthyPool` (internal/pool/pool_test.go): asserts
  post-warmup rows carry `Turns` (only slot-0's D-13 catalog probe counts —
  fixed the fake client to return a non-empty catalog on the first attempt so
  the assertion is deterministic instead of racing the warmup ctx deadline
  against `defaultCatalogRetry`) and non-nil, non-zero `SpawnedAt`.
- `TestPool_Detail_FieldShape_MatchesD15`: updated the exact-field-shape lock
  to include the two new tags — this is a deliberate wire-shape change, not a
  stale assertion (documented inline).
- `TestPool_WorkerRecycleAtThreshold` (internal/pool/worker_recycle_test.go):
  extended to capture pre-recycle `SpawnedAt` via `Detail()`, then after the
  existing `pollUntil`-style `Recycles() == 1` wait, assert `Turns == 0` and
  `SpawnedAt` strictly later than the pre-recycle value.
- `TestAdmin_SnapshotHandler` (internal/admin/snapshot_test.go): extended the
  existing fake `PoolDetailSource` case with `KiroWorkerMaxTurns: 8177` (a
  distinctive value) and per-slot `Turns`/`SpawnedAt`, asserting the decoded
  response carries `pool.max_turns` and both fields per slot (including the
  null-when-absent case for slot-1).
- `TestAdmin_StaticServes_JS` (internal/admin/handlers_test.go): extended
  with a `Cache-Control: no-cache` assertion.

## Self-review

- Traced the full pool.AgentSlot → admin.SnapshotSlot chain and found it
  actually passes through THREE types (pool.AgentSlot → server.AgentSlot →
  admin.SnapshotSlot), not the two implied by the task brief's line-number
  pointer. server.AgentSlot is the separate, locked D-14/D-15 `/health/agents`
  contract; rather than extend that locked contract or add a redundant
  parallel field-copy path, `adminPoolDetailAdapter` was changed to read
  `pool.Detail()` directly (main.go already imports `internal/pool` for other
  adapters, so this doesn't cross the admin package's TRST-04 boundary — only
  `internal/admin` itself must avoid importing `internal/pool`/`internal/server`).
- The D-15 field-shape lock test (`TestPool_Detail_FieldShape_MatchesD15`) had
  to be updated to accept the new fields — flagged inline as a deliberate
  contract change per this feature's explicit requirement, not a silently
  broken lock.
- `TestPool_Detail_HealthyPool`'s original fake client setup didn't provide a
  model catalog, so the D-13 catalog-probe retry loop (`defaultCatalogRetry`)
  fired 3 times before the 1s warmup ctx deadline cut it off, making
  `Turns` == 3 rather than the intended-and-documented "1 probe per slot-0
  warmup" semantics. Fixed by giving the fake client a non-empty catalog so
  the probe succeeds on the first attempt — this makes the test deterministic
  and matches operator intuition (warmup contributes exactly one turn),
  rather than encoding incidental retry-timing behavior into the assertion.
- Confirmed `go test -race` stays goleak-clean (enforced globally via
  `testmain_test.go`) and a full `go test ./... -count=1` sweep shows no
  regressions in any other package.
- Manually verified the new JS is ES5-only (`node -c` syntax check + grep for
  `=>`/`let`/`const`) since there is no browser test harness in this repo per
  CONTEXT.md (documented dashboard-matrix convention).
