# Ping suspend-guard + dashboard slot-health tiers — design

**Date:** 2026-07-14
**Status:** Approved (design)
**Scope:** One implementation plan.

## Problem

On a laptop, the admin dashboard intermittently shows a pool slot as
`DEAD` / `Dead — respawning…` (stark red). Investigation traced this to the
ACP liveness ping, not a real fault:

- `internal/acp/client.go:pingLoop` pings each kiro-cli worker every
  `PingInterval` (default 60s) with a hardcoded 10s timeout. On failure it
  logs `WARN acp.ping.escalated_to_close` and calls `c.cancel()`, tearing the
  worker down so the pool lazily respawns a fresh one. This is by design
  ("crash-and-replace" liveness).
- After the laptop sleeps, the worker process and the ping timer are frozen.
  On resume the ticker fires against a 10s deadline that has effectively
  already elapsed, so a **healthy** worker is closed. The code comment at
  `client.go:611-613` names "laptop sleep/wake" as a known trigger, and the
  comment at `client.go:1214` acknowledges the ping can `SIGKILL … a healthy
  worker`. (A second historical false-positive cause — a stalled SSE consumer
  starving the ping loop — was already fixed under REL-POOL-04, so sleep/wake
  is the remaining gap.)

The behavior is not a regression from the v2.4.0 de-brand: `git diff` shows the
ping/spawn code is byte-identical to v2.3.0. But it produces alarming red
"DEAD" boxes on a normal self-healing event, which reads to end users as a
fault and generates support calls.

Two changes address this:

1. **Ping suspend-guard** — stop closing a healthy worker on the first
   post-resume tick.
2. **Dashboard slot-health tiers** — render a transient recovering slot as a
   calm yellow "Recovering…" and reserve red for a genuine failure.

## Non-goals

- No change to the ping cadence default (`PING_INTERVAL` already tunes it) or
  the 10s ping timeout.
- No new environment variable.
- No change to the `AgentSlot` snake_case wire contract (additive only).
- No change to the lazy-respawn mechanism itself.

## Part A — Ping suspend-guard (`internal/acp/client.go`)

### Behavior

`pingLoop` tracks the wall-clock timestamp of the previous tick. On each tick:

```
gap = now() - lastTick
lastTick = now()
if gap > suspendThreshold:              // machine was suspended/frozen
    log INFO "acp.ping.skipped_after_resume" (gap=..., interval=...)
    continue                            // skip liveness this cycle
ping (10s timeout)
if ping fails (not Canceled/ClientClosed):
    log WARN "acp.ping.escalated_to_close"
    c.cancel(); return                  // unchanged for genuine hangs
```

- `suspendThreshold = 2 × PingInterval`, an unexported constant/derived value.
  A tick that arrives ≥2 intervals late in wall-clock terms means at least one
  full interval was lost to suspension (normal ticker jitter is
  sub-millisecond).
- `lastTick` is initialized to `now()` at loop entry, so the first real tick
  (~one interval later) has a normal gap.
- On a suspend-detected tick the ping is **skipped entirely** (no spurious 10s
  timeout right after resume). The guard is **one-shot**: the very next tick is
  a normal cycle, so a worker that is genuinely hung is caught within one
  interval. A worker that actually *exited* during sleep still fires `Done()`
  and is detected by the exit-watcher independently of the ping, so nothing
  truly dead is masked.

### Testability seam

Add `Now func() time.Time` to `acp.Config` (defaults to `time.Now` when nil),
matching the existing config-injection pattern (`PingInterval`, factory seams).
`pingLoop` reads the wall clock through `c.cfg.Now()`.

### Tests (TDD)

1. **Normal gap, failing ping → escalates.** Preserves existing behavior:
   `Done()` fires, `acp.ping.escalated_to_close` logged. (Guards against the
   suspend logic weakening genuine-hang detection.)
2. **Suspend gap, failing ping → not escalated.** With `Now` advanced past
   `2 × PingInterval` across a tick, the worker survives (`Done()` does not
   fire), and `acp.ping.skipped_after_resume` is logged.
3. **Suspend cycle then normal cycle, failing ping → escalates.** Confirms the
   guard is one-shot, not sticky: after one skipped cycle a subsequent normal
   tick with a failing ping still closes the worker.

## Part B — Dashboard slot-health tiers (`internal/admin`)

### States

Split today's single dead-slot rendering into the states the pool already
distinguishes:

| State | Condition (existing signals) | Style | Badge / meta text |
|---|---|---|---|
| Alive | `slot.alive` | green (unchanged) | `ALIVE` / `Idle` / `BUSY` |
| Recovering | dead, pool still serving, no *current* failure | yellow outline + yellow text | `RECOVERING` / `Recovering…` |
| Failed | dead **and** a *current* genuine failure | red (reserved) | `FAILED` / `Failed — check logs` |

**Genuine-failure (red) trigger** keys off *current* pool state, not the sticky
historical error field:

- pool snapshot `status == "down"` (all slots dead — the pool cannot serve
  right now), OR
- `spawn_failing` — a genuine respawn failure that happened *recently*.

`spawn_failing` must be a **current** signal. `Pool.HealthSummary.LastSpawnError`
is deliberately sticky ("NEVER cleared on a subsequent success" — operators read
`LastSpawnErrAt` for recency), so its mere presence must NOT drive red or slots
would stay red forever after a single historical failure. Instead the backend
computes:

```
spawn_failing = LastSpawnError != "" && now - LastSpawnErrAt < recencyWindow
```

with `recencyWindow = 2 × PingInterval` (a genuine spawn failure within roughly
the last liveness cycle). `recordSpawnErr` is not called on ctx-cancel, so
`LastSpawnError` already excludes laptop-reconnect artifacts.

Any other dead slot is **Recovering** (yellow).

### Backend

- The pool already exposes `HealthSummary{ Healthy, LastSpawnError,
  LastSpawnErrAt, ... }` via `/health/pool`, and the admin snapshot already
  computes a pool `status` (`ok` / `degraded` / `down`). The recency check lives
  in the pool, where the fields and `PingInterval` already are: `HealthSummary`
  gains a computed `SpawnFailing bool` (same `p.mu` critical section). The admin
  snapshot forwards it as an additive **boolean** JSON field `spawn_failing` on
  the pool section. Existing fields untouched. The dashboard receives a clean
  current-health flag and never sees the (potentially long/sensitive) raw error
  string.
- The per-slot `AgentSlot` / `SnapshotSlot` wire shape is unchanged; the
  yellow-vs-red decision is made in the renderer from the pool-level signal +
  the slot's existing `alive` flag.

### Frontend (`internal/admin/static/js/admin.js` + `css/admin.css`)

- `buildSlotBadges` / `buildSlotMeta`: when a slot is not alive, choose the
  recovering (yellow) vs failed (red) variant from the snapshot's pool
  `status` / `spawn_failing`.
- CSS: add a `is-recovering` (warning/yellow) variant for the slot box outline,
  badge, and meta text. Keep the existing red `is-dead` styling but apply it
  only in the Failed condition.

### Tests

- Pool: `HealthSummary.SpawnFailing` is true only for a *recent* genuine spawn
  error and false once older than `recencyWindow` (guards against the sticky
  `LastSpawnError` driving a permanent red).
- Admin snapshot / rendering: assert the recovering-vs-failed classification
  from the two conditions (pool serving + dead slot → recovering; pool `down`
  or `spawn_failing` → failed).

## Files touched (anticipated)

- `internal/acp/client.go` — suspend-guard in `pingLoop`; `Config.Now` seam.
- `internal/acp/client_test.go` (or a focused new test file) — the three ping tests.
- `internal/pool/pool.go` — `HealthSummary.SpawnFailing` (recency-bounded).
- `internal/admin/snapshot.go` — forward `spawn_failing` on the pool section.
- `internal/admin/static/js/admin.js` — recovering-vs-failed slot rendering.
- `internal/admin/static/css/admin.css` — yellow recovering variant.
- pool + admin snapshot/render tests — `SpawnFailing` recency + classification coverage.

## Verification

- `go test ./internal/acp/... ./internal/admin/...` green, including the three
  new ping tests and the slot-classification tests.
- Manual: with the gateway running, a slot that recycles (or is forced dead)
  shows yellow "Recovering…"; a forced genuine spawn failure (e.g. bad
  `KIRO_CMD`) shows red "Failed — check logs".
- Full gate suite (`go build ./...`, `go vet`, gofumpt, `GOOS=windows` tray
  build, golangci-lint on touched packages) green.
