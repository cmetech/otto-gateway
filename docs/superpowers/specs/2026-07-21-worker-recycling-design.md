# Pool Worker Recycling + Desktop Pool Defaults + Vacant Slot Tiles — Design

**Date:** 2026-07-21
**Status:** Rev 2 — adversarial review findings folded in (see §6)
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

The catalog paths accumulate the same way (review finding M-4): the warmup
catalog capture and every lazy self-heal probe (`probeCatalogOnce`,
`pool.go:316`) each run a real `session/new` whose retained state `Cancel`
does not free.

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

### Part A — Env template defaults (config-only)

In `scripts/.env.example`:

- Activate `POOL_SIZE=2` (currently present as a commented suggestion).
  A single desktop user needs at most 2 concurrent slots; this halves both
  the idle baseline (~200 MB → ~100 MB) and the number of workers that can
  bloat.
- Add `KIRO_WORKER_MAX_TURNS=20` (see Part B).

**Rollout posture (review finding M-5, explicitly accepted):**
`scripts/.env.example` is the single wrapper template, and `gw upgrade-env`
regenerates `.env` byte-for-byte from it — so **every** script-based
deployment receives these values on its next `upgrade-env`, not only desktop
installs. This is accepted deliberately: the gw-script distribution of this
gateway is laptop-oriented, and any shared/server deployment that needs a
larger pool already has the supported mechanism for it — `overrides.env`,
which `upgrade-env` never touches and which wins over `.env`. The template
comments must state this explicitly next to the two keys ("sized for
single-user laptops; raise via overrides.env for shared hosts").

The binary defaults are unchanged (`POOL_SIZE=4`, recycling disabled), so
deployments that bypass the gw scripts see zero behavior change.

`scripts/gw` / `scripts/gw.ps1` env-key lists (~gw:1805/1904,
~gw.ps1:1554/1619) do **not** gate loading — they drive `gw env` display and
support-bundle reporting (review confirmed). `KIRO_WORKER_MAX_TURNS` is
still added to both lists so it shows up in diagnostics.

### Part B — Turn-count worker recycling (Go)

**New env knob:** `KIRO_WORKER_MAX_TURNS` — int ≥ 0, default **0 =
disabled**. Parsed in `internal/config/config.go` with the same fail-fast
validation style as `POOL_SIZE` (reject negative; sanity-cap at 10_000).
Threaded to `pool.Config.MaxWorkerTurns` and wired in
`cmd/otto-gateway/main.go`.

**Turn counting:** `Slot` gains a `turns int` field, guarded by `p.mu`
(same discipline as `slot.dead`). A "turn" is **every successful
`session/new` executed on a pool worker** — the event that accumulates
memory — regardless of which path issued it (review finding M-4):

- `Pool.NewSession`: increment inside the existing `p.mu` critical section
  that records `p.sessionSlots[sid] = slot`. Failed `session/new` does not
  increment.
- Catalog probes: `probeCatalogOnce` takes the `*Slot` (not the bare
  `PoolClient`) and increments `slot.turns` under `p.mu` on successful
  `session/new`. This covers both the warmup retry loop
  (`captureCatalogWithRetry`) and the lazy self-heal path
  (`selfHealCatalog`). The self-heal path already holds the slot
  exclusively (dequeued from `p.slots`), and warmup owns the freshly-built
  slot; neither needs new synchronization beyond the counter's `p.mu`.
  Self-heal returns its slot via the same `releaseOrRecycle` helper below,
  so a probe-bloated worker is also recycled rather than exempt.

**Recycle trigger:** a new helper `releaseOrRecycle(slot)` replaces the bare
`p.slots <- slot` on the post-serving release paths:

1. the `release()` closure inside `Pool.Prompt` (pool.go:1077),
2. `releaseSlotForSession` (pool.go:1191 — Prompt-error path and
   `Pool.Cancel`), and
3. the self-heal probe's deferred slot return (pool.go:375).

(The `NewSession`-error release at pool.go:1023 keeps the direct push: no
session was created there, and any prior threshold crossing was already
handled at that turn's own release.)

**Commit-to-recycle is a single `p.mu` critical section (review finding
H-1).** Under `p.mu`, `releaseOrRecycle`:

1. reads `slot.turns`;
2. if `p.closed` → **DROP** the slot (`return`, no requeue — `closeAll` owns
   cleanup via the `p.all` snapshot; a post-close push would feed a closed
   client to a racing fast-path acquire, surfacing a confusing 500 instead of
   the clean `pool: closed`);
3. else if `MaxWorkerTurns == 0 || turns < MaxWorkerTurns` → push to `p.slots`
   (the buffered send cannot block — capacity equals slot count);
4. otherwise **commits**: `p.recycleWG.Add(1)` *inside this critical
   section*, then unlock and launch the goroutine.

Because `closeAll` sets `p.closed` under the same mutex, every accepted
`Add` is ordered before `Pool.Close`'s `recycleWG.Wait()` — the
Add-after-Wait escape is structurally impossible. (The existing `probeWG`
pattern checks `p.closing` and `Add`s in separate steps and is NOT a safe
template; as an adjacent hardening, `selfHealCatalog`'s `Add` moves under
the same discipline while we are in `Close`.)

**Eager background respawn goroutine:**

```
go func() {
    defer p.recycleWG.Done()
    select { case <-p.closing:  // shutdown won the race after commit:
        return                   // DROP the slot — do not push. closeAll
    default: }                   // finds the old client via p.all. (H-1)

    log INFO "pool: slot recycling" {label, turns}
    ctx := context.WithTimeout(Background, recycleRespawnTimeout /* 30s */)
    err := p.respawnSlot(ctx, slot, respawnCauseRecycle)

    p.mu.Lock()
    if p.closed {
        // closeAll may have snapshotted before our swap: capture the
        // current client UNDER p.mu and close it ourselves. Double-close
        // with closeAll's copy is safe (Close is idempotent). (H-2)
        c := slot.Client
        p.mu.Unlock()
        if err == nil && c != nil { _ = c.Close() }
        return                   // do not push; the pool is gone
    }
    if err != nil { slot.dead = true }  // lazy path retries at next acquire
    p.slots <- slot              // guarded non-blocking send, same
    p.mu.Unlock()                // pattern as pool.go:970-981
}()
```

**Post-shutdown drop symmetry (hardening added post-review).** The recycle
goroutine's `<-p.closing` branch above drops the slot rather than pushing it.
`releaseOrRecycle` ALSO drops (returns without requeue) when it observes
`p.closed` under `p.mu` — checked *first*, before the below-threshold push and
the recycle commit. Previously this path pushed the slot back (matching the
pre-recycling release behavior), but a post-close push lets a racing fast-path
`NewSession` (the non-blocking `<-p.slots` try) dequeue a closed client and
surface a confusing 500 instead of the clean "pool: closed" the `<-p.closing`
acquire arm returns. Dropping is safe: `closeAll` owns teardown via its `p.all`
`{label, client}` snapshot.

**Respawn cause parameter (review findings M-3 + M-6):** `respawnSlot`
gains an explicit cause — `respawnCauseLazy` (today's dequeue-time path) vs
`respawnCauseRecycle`. The cause controls three things:

1. **Error classification:** the WR-07 "ctx-cancel is a benign caller
   disconnect" suppression applies **only** to the lazy cause. On the
   recycle cause there is no caller to disconnect — a
   `context.DeadlineExceeded` from the 30 s budget is a genuine failure
   and goes through `recordSpawnErr` so `LastSpawnError` /
   `gw_pool_spawn_failing` see it. **Scope of the 30 s budget (Finding 4):**
   the factory `Spawn` (`acpClientFactory.Spawn`) discards its `ctx`, so the
   budget bounds only the ctx-aware, RPC-level `Initialize` step — it cannot
   interrupt a process exec start that blocks inside `Spawn`. This is a
   pre-existing property of every spawn path (warmup, lazy, recycle), not
   specific to recycling; making exec ctx-aware is out of scope.
2. **Log reason:** the success INFO log emits
   `reason="lazy-respawn-success"` (byte-stable, unchanged) or
   `reason="recycle-respawn-success"` respectively, so downstream log
   consumers can distinguish crash recovery from scheduled recycling.
3. **Counter:** the lazy cause increments the existing `respawns`
   counter (`gw_pool_slot_respawns_total`, help text stays truthful:
   "Total lazy slot respawns"); the recycle cause increments a new
   `recycles atomic.Uint64` surfaced as `gw_pool_slot_recycles_total`
   ("Total scheduled worker recycles (KIRO_WORKER_MAX_TURNS)").

**`closeAll` data-race fix (review finding H-2, pre-existing but made
routine by background recycling):** today `closeAll` snapshots `[]*Slot`
under `p.mu` but reads `s.Client` *after* unlocking (pool.go:826), while
`respawnSlot` writes `slot.Client` under `p.mu` (pool.go:503) —
an unsynchronized interface-value read/write. Fix: under `p.mu`, snapshot
immutable `{label, client}` pairs (the discipline `closeAll`'s own
inflight-cancel block already uses at pool.go:786-795), then close the
captured clients after unlocking. Any client swapped in *after* that
snapshot is owned by the recycle goroutine's `p.closed` branch above —
between the two, every client is closed exactly ≥ once and none leaks.

**Other properties (unchanged from rev 1):**

- **No request latency:** requests never wait on a spawn; during the ~1-2 s
  respawn the pool has one fewer free slot, bounded by the existing
  `AcquireTimeout` → 503 path.
- **Capacity invariant:** the goroutine owns the slot exclusively (never
  entered the free channel), so the slot re-enters `p.slots` exactly once —
  or is deliberately dropped on shutdown where `closeAll` owns cleanup.
- **Counter reset:** `respawnSlot` resets `slot.turns = 0` inside its
  existing `p.mu` swap block — covering the eager path and the pre-existing
  lazy dead-slot path.
- **Non-interactions:** stateful session registry untouched; ping/exit-
  watcher paths unchanged (a worker dying mid-recycle surfaces as a failed
  respawn → dead-marked → lazy retry).

### Part C — Dashboard vacant slot tiles (static JS/CSS only)

Requirement: the slot grid always renders at least 4 tiles so the layout
stays balanced, with unprovisioned slots clearly marked.

- `renderSlots` (`internal/admin/static/js/admin.js:339`) pads its logical
  list: after the real `pool.slots[]` rows, append `4 - len` placeholder
  objects `{vacant: true, label: "slot-N"}` (continuing the index) when
  `0 < len < 4`. Pools of size ≥ 4 render exactly as today. A size-0 pool
  keeps today's empty-state message (no padding).
- Vacant cards: muted `VACANT` badge, meta text
  `Not provisioned (POOL_SIZE=N)` (N = `snap.pool.size`), no CPU/Mem perf
  block, no sparkline sampling.
- **Stale-class prevention by construction (review finding L-1):** card
  state classes are not incrementally added/removed. A single
  `slotCardClass(slot, poolFailed)` helper computes the full class string
  (`gw-slot-card` + `is-vacant` / `is-dead` / `is-recovering`), and BOTH
  `buildSlotCard` and `updateSlotCard` assign `article.className`
  wholesale. A pool-size change across restarts (2 → 3 with the grid
  length pinned at 4, which takes the in-place `updateSlotCard` path)
  therefore cannot retain `is-vacant` on a now-real slot, or vice versa.
- CSS: `.gw-slot-card.is-vacant` — dashed border, reduced opacity, muted
  badge color, honoring light/dark themes like existing badge tiers.
- **No wire change:** `/admin/api/snapshot` shape is untouched; padding is
  purely client-side derived from `pool.size`.
- Automated DOM testing was considered and declined: the repo has no JS
  test infrastructure, and a node/jsdom harness for one transition
  assertion is disproportionate given the wholesale-className design
  removes the stale-class failure mode structurally. The 2→3 transition is
  part of the documented manual test matrix instead (§4).

---

## 3. Touchpoints (complete file list)

| File | Change |
|---|---|
| `internal/config/config.go` | Parse + validate `KIRO_WORKER_MAX_TURNS` (default 0) |
| `internal/pool/config.go` | `MaxWorkerTurns` field |
| `internal/pool/pool.go` | `Slot.turns`; increment in `NewSession` + `probeCatalogOnce` (takes `*Slot`); `releaseOrRecycle` on all three release paths; recycle goroutine + `recycleWG` (Add under `p.mu`); `respawnSlot` cause param + `turns` reset; `recycles` counter; `closeAll` `{label, client}` snapshot fix; `selfHealCatalog` Add-ordering hardening; `Close` drains `recycleWG` |
| `internal/pool/stats.go` / `internal/metrics/collector.go` (+ registration) | `Recycles()` accessor; `gw_pool_slot_recycles_total` |
| `cmd/otto-gateway/main.go` | Wire config → pool.Config |
| `internal/admin/static/js/admin.js` | Vacant-tile padding; `slotCardClass` wholesale className |
| `internal/admin/static/css/admin.css` | `.is-vacant` styling |
| `scripts/.env.example` | `POOL_SIZE=2` active, `KIRO_WORKER_MAX_TURNS=20`, rollout comment |
| `scripts/gw`, `scripts/gw.ps1` | Add `KIRO_WORKER_MAX_TURNS` to diagnostics/env-report key lists |
| docs env table (admin docs template / endpoint reference) | Document the new knob + new metric |
| `CLAUDE.md` env list | Note `KIRO_WORKER_MAX_TURNS` (net-new) |

---

## 4. Testing plan

Go (all `-race`, goleak-gated via existing testmain):

1. Config: default 0; valid value; negative → fail-fast error; cap.
2. Turn counting: N successful `NewSession` calls → `turns == N`; failed
   `NewSession` does not increment; **catalog probes (warmup + self-heal)
   increment** (M-4).
3. Recycle trigger: with `MaxWorkerTurns=2` and a fake factory, the slot is
   NOT returned to the free channel at the threshold release; a replacement
   client is spawned; the slot re-enters the free channel with `turns == 0`;
   old client's `Close` was called; `recycles` counter == 1 and `respawns`
   unchanged (M-6).
4. Disabled (0): threshold never trips; existing tests keep passing.
5. Respawn failure: factory error → slot re-enters channel dead; next
   `NewSession` takes the lazy path. **Deadline on the recycle cause is
   recorded via `recordSpawnErr`** (M-3); ctx-cancel on the lazy cause
   remains suppressed (WR-07 regression guard).
6. Shutdown races (H-1/H-2), using a test seam that pauses between
   commit-to-recycle and goroutine launch, and a factory that blocks in
   Spawn:
   a. Close after commit, before goroutine start → `Wait` blocks until the
      goroutine's closing-branch drop; no push, no leak.
   b. Close mid-spawn → fresh client closed by the goroutine's `p.closed`
      branch; goleak clean.
   c. Close after swap, before push → no leak; `closeAll`'s captured-pair
      snapshot + goroutine branch cover both orders.
   d. `-race` on concurrent `closeAll` + `respawnSlot` (the H-2 fix's
      regression test — fails on the current unsynchronized `s.Client`
      read if reverted).
7. Release paths: recycle triggers from the `Prompt` happy-path release,
   `Pool.Cancel`'s `releaseSlotForSession`, and the self-heal probe return.

Dashboard — manual matrix against a live gateway (no JS test infra; see
Part C rationale): `POOL_SIZE=2` → 2 real + 2 vacant; `POOL_SIZE=4` →
unchanged; `POOL_SIZE=0` → empty-state unchanged; restart 2→3 → tile 3
transitions vacant→real with no `is-vacant` residue (in-place update
path). Existing admin regression tests stay green.

Lint gates before push: `golangci-lint` (v2.12.2) + `gofumpt` via `go run`,
per CI parity.

---

## 5. Risks / open items

1. **Thundering respawn:** ~~with `POOL_SIZE=2`, both workers can recycle
   concurrently, briefly leaving zero free slots.~~ **Mitigated:** a
   single-recycle-in-flight guard (`Pool.recyclesInFlight`, an int under
   `p.mu` decided atomically with the `recycleWG.Add` commit in
   `releaseOrRecycle`) caps concurrent scheduled recycles at one, so at most
   one worker is ever down for maintenance at a time and a `POOL_SIZE=2` pool
   always keeps a live worker. Deferral is lossless: a worker crossing the
   threshold while a recycle is already in flight is returned to the free
   queue and re-trips at its next release (serving a few turns past the soft
   budget is harmless).
2. **Turn ≠ memory:** a 5-token turn and a 200 k-token workflow turn count
   equally. Default 20 is a tunable guess; observe dashboard RSS to tune.
   RSS trigger remains a possible v2.
3. **Template rollout is fleet-wide for script installs** (M-5): accepted
   and documented in Part A; shared hosts pin via `overrides.env`.
4. **Vacant tile count hard-coded at 4:** per product decision (UI
   balance); pools > 4 render all slots, padding only applies below 4.

---

## 6. Adversarial review resolution log (rev 1 → rev 2)

| Finding | Severity | Resolution |
|---|---|---|
| `recycleWG.Add` orderable after `Close.Wait` | HIGH | Add moved inside the `p.mu` commit-to-recycle critical section; early-closing branch drops the slot instead of pushing; `selfHealCatalog` hardened to same discipline |
| `closeAll` unsynchronized `s.Client` read vs `respawnSlot` write | HIGH | `closeAll` snapshots `{label, client}` pairs under `p.mu`; recycle goroutine's `p.closed` branch captures+closes under the same discipline; dedicated `-race` regression test |
| 30 s recycle deadline mis-classified as benign by WR-07 | MEDIUM | `respawnSlot` cause parameter; deadline on recycle cause → `recordSpawnErr` |
| Catalog probes create uncounted sessions | MEDIUM | Every successful `session/new` on a worker counts; `probeCatalogOnce` takes `*Slot`; self-heal returns via `releaseOrRecycle` |
| Template rollout not desktop-scoped | MEDIUM | Accepted + documented (Part A); `overrides.env` is the shared-host escape hatch; key added to gw/gw.ps1 diagnostic lists |
| `reason="lazy-respawn-success"` / respawns counter semantically false for recycles | MEDIUM | Cause-driven log reason + separate `gw_pool_slot_recycles_total`; existing metric help stays truthful |
| Vacant-card stale classes on in-place update | LOW | Wholesale `className` assignment via shared `slotCardClass` (stale classes impossible by construction); automated JS test declined as disproportionate — documented manual matrix instead |
