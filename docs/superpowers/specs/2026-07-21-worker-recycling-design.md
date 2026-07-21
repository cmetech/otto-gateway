# Pool Worker Recycling + Desktop Pool Defaults + Vacant Slot Tiles — Design

**Date:** 2026-07-21
**Status:** Draft — pending adversarial review
**Scope:** otto-gateway (Go), admin dashboard (static JS/CSS), desktop env template

---

## 1. Problem

### 1.1 Observed symptom

On a desktop deployment (single user, hermes-agent desktop → gateway), the
admin dashboard shows one pool worker at ~255 MB RSS after the first chat
request, while the three idle workers sit at ~50 MB each. Worker memory never
comes back down. The numbers are per-worker RSS of the `kiro-cli` subprocess,
sampled by PID via `internal/procstat` and merged into the snapshot by slot
label (`internal/admin/snapshot.go:179`).

### 1.2 Root cause — pool sessions are created but never destroyed

The gateway's default (stateless pool) request path is:

1. Acquire a warm slot (`internal/pool/pool.go` — `Pool.NewSession`).
2. `session/new` → a **brand-new kiro session** on that worker.
3. `session/prompt` with the client's transcript embedded.
4. Stream the reply, release the slot.

Nothing ever ends the kiro session: `session/cancel` fires only on
error/disconnect/watchdog (`internal/engine/engine.go:249-315`), ACP has no
session-delete method, and the gateway does not use kiro's
`_kiro.dev/clear` / compaction extensions. kiro-cli keeps each session's
conversation state in process memory (and persists it under
`~/.kiro/sessions/cli/`). Pool workers live for the gateway's entire lifetime
— they are only respawned when the process dies or the ping watchdog
escalates.

**Consequence:** every request appends another abandoned session's state to
whichever worker served it. Worker RSS is monotonic with request count.

### 1.3 Why the primary client makes this worse

hermes-agent (the desktop client) is a stateless Chat Completions client: it
re-sends the **full conversation transcript on every turn**
(`hermes-agent/agent/conversation_loop.py:675-739` builds `api_messages` from
the entire history; there is no `X-Session-Id` usage anywhere in that
codebase). So turn N of a workflow creates a kiro session containing N turns
of history, on top of the abandoned sessions from turns 1..N-1. Long
workflows are the worst case.

### 1.4 Existing mechanisms that do NOT cover this

- `SESSION_TTL_MS` reaping and `CTX_RECYCLE_PCT` recycling both kill whole
  subprocesses and reclaim memory — but they apply **only** to the
  `X-Session-Id` stateful registry path (`internal/session/registry.go`),
  which hermes does not use.
- The pool has dead-slot detection + lazy synchronous respawn
  (`slot.dead` flag, `respawnSlot`, `pool.go:926`) — but nothing marks a
  *healthy* bloated worker for respawn.

### 1.5 Rejected alternative: stateful kiro sessions for hermes

Considered and rejected. Stateful (`X-Session-Id`) sessions only help when
the client sends delta turns. hermes re-sends the whole transcript, so a
stateful kiro session would accumulate the full transcript **again on every
turn** into the same session — quadratic context growth, faster memory
growth, early `CTX_RECYCLE_PCT` trips. Changing hermes to send deltas is a
major client rework and out of scope.

### 1.6 Rejected alternative: RSS-threshold recycle trigger

`procstat` returns no data on darwin (Linux/Windows only), so an RSS trigger
would silently never fire on macOS. Turn-count is deterministic,
platform-independent, and directly proportional to the actual leak (sessions
accumulated). RSS trigger can be added later if turn-count proves
insufficient.

---

## 2. Proposed solution (three parts)

### Part A — Desktop env template defaults (config-only)

In `scripts/.env.example`:

- Activate `POOL_SIZE=2` (currently present as a commented suggestion).
  A single desktop user needs at most 2 concurrent slots; this halves both
  the idle baseline (~200 MB → ~100 MB) and the number of workers that can
  bloat.
- Add `KIRO_WORKER_MAX_TURNS=20` (see Part B).

Existing installs pick both up via `gw upgrade-env` (the template is the
upgrade source — `scripts/gw:138-150`). The **binary defaults are
unchanged** (`POOL_SIZE=4`, recycling disabled), so server/LangFlow
deployments that don't use the template see zero behavior change.

Implementation must also check the env-var allowlists in `scripts/gw`
(~line 1805/1904) and `scripts/gw.ps1` (~line 1554/1619) — if those lists
gate which keys are loaded/exported, `KIRO_WORKER_MAX_TURNS` must be added.

### Part B — Turn-count worker recycling (Go)

**New env knob:** `KIRO_WORKER_MAX_TURNS` — int ≥ 0, default **0 =
disabled**. Parsed in `internal/config/config.go` with the same fail-fast
validation style as `POOL_SIZE` (reject negative; sanity-cap large values,
e.g. max 10_000). Threaded to `pool.Config.MaxWorkerTurns` and wired in
`cmd/otto-gateway/main.go`.

**Turn counting:** `Slot` gains a `turns int` field, guarded by `p.mu`
(same discipline as `slot.dead`). Incremented in `Pool.NewSession` inside
the existing `p.mu.Lock()` critical section that records
`p.sessionSlots[sid] = slot` — i.e. a "turn" is a successfully created kiro
session, which is exactly the event that accumulates memory in the worker.
A failed `session/new` does not increment.

**Recycle trigger:** a new helper `releaseOrRecycle(slot)` replaces the bare
`p.slots <- slot` on the two post-serving release paths:

1. the `release()` closure inside `Pool.Prompt` (pool.go:1077), and
2. `releaseSlotForSession` (pool.go:1191, used by the Prompt-error path and
   `Pool.Cancel`).

Under `p.mu` it reads `slot.turns`; if `MaxWorkerTurns > 0 && turns >=
MaxWorkerTurns && !p.closed`, the slot takes the recycle path; otherwise it
is pushed to `p.slots` exactly as today. (The `NewSession`-error release at
pool.go:1023 keeps the direct push: no session was created there, and any
prior threshold crossing was already handled at that turn's own release.)

**Eager background respawn:** the recycle path hands the slot to a
goroutine tracked by a new `recycleWG sync.WaitGroup` (mirroring the
existing `probeWG` pattern):

```
go func() {
    defer recycleWG.Done()
    if <-p.closing already closed → push slot back, exit   // closeAll owns cleanup
    ctx := context.WithTimeout(Background, recycleRespawnTimeout /* 30s */)
    err := p.respawnSlot(ctx, slot)          // existing battle-tested path:
                                             // close OLD client (memory reclaimed),
                                             // spawn+Initialize NEW, swap under p.mu,
                                             // fresh exit-watcher, INFO log, respawns++
    under p.mu:
        if p.closed:
            if err == nil → _ = slot.Client.Close()   // don't leak the fresh process
            exit                                       // do NOT push; pool is gone
        if err != nil → slot.dead = true               // lazy path retries at next acquire
        push slot to p.slots (non-blocking send, same
            guarded pattern as pool.go:970-981)
}()
```

Key properties:

- **No request latency:** requests never wait on a spawn. During the ~1-2 s
  respawn the pool simply has one fewer free slot; a concurrent burst that
  needs the slot parks in the existing bounded-acquire path
  (`AcquireTimeout` → 503) exactly as if the slot were busy.
- **Capacity invariant:** the goroutine has exclusive ownership of the slot
  (it was never returned to the free channel), so the slot re-enters
  `p.slots` exactly once — on success, or dead-marked on failure so the
  existing lazy respawn retries. The pool never shrinks.
- **Counter reset:** `respawnSlot` resets `slot.turns = 0` inside its
  existing `p.mu` swap block — covering both the eager path and the
  pre-existing lazy dead-slot path (a crashed worker's replacement starts
  at zero).
- **Shutdown safety:** `Pool.Close` adds `recycleWG.Wait()` after
  `closeAll()` (alongside `probeWG.Wait()`). Interleavings:
  - Swap happens **before** `closeAll`'s snapshot iteration → `closeAll`
    closes the NEW client (slot is in `p.all` throughout). No leak.
  - Swap happens **after** → the goroutine's `p.closed` check closes the
    NEW client itself. No leak. (Double-close is safe; `Client.Close` is
    idempotent.)
  - Goroutine parked before respawn when `p.closing` fires → pushes the
    slot back untouched and exits; `closeAll` owns the old client.
- **Observability:** `respawnSlot`'s existing INFO log fires
  (`pool: slot recovered`, previous_pid → new_pid) and the existing
  `respawns` counter increments. Additionally the recycle path logs
  `pool: slot recycled` at INFO with `label` and `turns` before invoking
  respawn, so operators can distinguish recycles from crash recoveries.

**Explicit non-interactions:**

- The stateful session registry is untouched (dedicated clients, not pool
  slots).
- The warmup catalog probe (`probeCatalogOnce`) creates one throwaway
  session on slot 0 via `slot.Client.NewSession` directly — it does not go
  through `Pool.NewSession`, so it does not count as a turn.
- The Track 4b ping-escalation and exit-watcher paths are unchanged; a
  worker that dies mid-recycle-respawn surfaces as a failed respawn
  (dead-marked, lazy retry).

### Part C — Dashboard vacant slot tiles (static JS/CSS only)

Requirement: the slot grid should always render at least 4 tiles so the
layout stays balanced, with unprovisioned slots clearly marked.

- `renderSlots` (`internal/admin/static/js/admin.js:339`) pads its logical
  list: after the real `pool.slots[]` rows, append `4 - len` placeholder
  objects `{vacant: true, label: "slot-N"}` (continuing the index) when
  `0 < len < 4`. Pools of size ≥ 4 render exactly as today. A size-0 pool
  keeps today's empty-state message (no padding).
- `buildSlotCard` / `updateSlotCard` branch on `slot.vacant`: card class
  `is-vacant`, muted `VACANT` badge, meta text
  `Not provisioned (POOL_SIZE=N)` (N = `snap.pool.size`), no CPU/Mem perf
  block, no sparkline sampling.
- CSS: `.gw-slot-card.is-vacant` — dashed border, reduced opacity, muted
  badge color, honoring both light/dark themes like existing badge tiers.
- The in-place DOM-patch diff (`grid.children.length !== slots.length`)
  compares against the **padded** list, so tile count is stable at ≥ 4 and
  in-place updates keep working.
- **No wire change:** `/admin/api/snapshot` shape is untouched; padding is
  purely client-side derived from `pool.size`.

---

## 3. Touchpoints (complete file list)

| File | Change |
|---|---|
| `internal/config/config.go` | Parse + validate `KIRO_WORKER_MAX_TURNS` (default 0) |
| `internal/pool/config.go` | `MaxWorkerTurns` field |
| `internal/pool/pool.go` | `Slot.turns`, increment in `NewSession`, `releaseOrRecycle` helper on both release paths, recycle goroutine + `recycleWG`, `Close` drain, `respawnSlot` resets `turns` |
| `cmd/otto-gateway/main.go` | Wire config → pool.Config |
| `internal/admin/static/js/admin.js` | Vacant-tile padding + rendering |
| `internal/admin/static/css/admin.css` | `.is-vacant` styling |
| `scripts/.env.example` | `POOL_SIZE=2` active, `KIRO_WORKER_MAX_TURNS=20` |
| `scripts/gw`, `scripts/gw.ps1` | Add `KIRO_WORKER_MAX_TURNS` to env-key lists (verify whether lists gate loading) |
| docs env table (admin docs template / endpoint reference) | Document the new knob |
| `CLAUDE.md` env list | Note `KIRO_WORKER_MAX_TURNS` (net-new) |

---

## 4. Testing plan

Go (all `-race`, goleak-gated via existing testmain):

1. Config: default 0; valid value; negative → fail-fast error; cap.
2. Turn counting: N successful `NewSession` calls on a slot → `turns == N`;
   failed `NewSession` does not increment.
3. Recycle trigger: with `MaxWorkerTurns=2` and a fake factory, the slot is
   NOT returned to the free channel at the threshold release; a replacement
   client is spawned; the slot re-enters the free channel with `turns == 0`;
   old client's `Close` was called.
4. Disabled (0): threshold never trips, zero behavioral diff (existing
   tests keep passing).
5. Respawn failure: factory returns error → slot re-enters channel dead;
   next `NewSession` takes the existing lazy-respawn path.
6. Shutdown races: `Close` during in-flight recycle (before spawn, after
   spawn/before swap, after swap) — no goroutine leak, no client leak,
   `Close` returns.
7. Release paths: recycle triggers from both the `Prompt` happy-path
   release and `Pool.Cancel`'s `releaseSlotForSession`.

Dashboard: manual verification against a live `POOL_SIZE=2` gateway
(2 real + 2 vacant tiles; ≥ 4 slots unchanged; size-0 empty state
unchanged). Existing admin regression tests must stay green.

Lint gates before push: `golangci-lint` (v2.12.2) + `gofumpt` via `go run`,
per CI parity.

---

## 5. Risks / open questions for review

1. **Thundering respawn:** with `POOL_SIZE=2` and both workers crossing the
   threshold on consecutive releases, both could respawn concurrently,
   briefly leaving zero free slots. Acceptable for desktop (bounded by
   `AcquireTimeout` 503 + retry); worth confirming for shared deployments —
   mitigation if needed later: stagger via a singleflight or jittered
   threshold, deliberately NOT in v1.
2. **Turn ≠ memory:** a 5-token turn and a 200 k-token workflow turn count
   equally. Turn-count is a proxy; the default (20) is a guess to be tuned
   with dashboard RSS observation. RSS trigger remains a possible v2.
3. **respawnSlot reuse:** the eager path calls `respawnSlot` from a
   goroutine while the slot is NOT in the free channel — same exclusive-
   ownership precondition as today's caller (`NewSession` after dequeue).
   Reviewers should confirm no hidden assumption that the caller holds a
   request context (we pass a background ctx + 30 s timeout; the WR-07
   ctx-cancel special-casing in `respawnSlot` becomes dead code on this
   path, which is fine).
4. **Vacant tile count hard-coded at 4:** per product decision (UI balance).
   If `POOL_SIZE` ever exceeds 4 the grid simply shows them all; padding
   only applies below 4.
