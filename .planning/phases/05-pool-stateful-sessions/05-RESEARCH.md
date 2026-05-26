# Phase 5: Pool + Stateful Sessions - Research

**Researched:** 2026-05-26
**Domain:** Go concurrency (pools, registries, reapers, exit watchers), HTTP observability, ACP subprocess lifecycle
**Confidence:** HIGH

## Summary

Phase 5 is **integration work over an already-load-bearing concurrency surface**.
The pool, the engine, the per-request watchdog, and the slot-release race
resolution all exist and ship in Phase 2 / Phase 4. Phase 5 adds:

1. A per-slot exit watcher backed by a new push-based signal on `acp.Client`
   (`Done() <-chan struct{}` derived from `clientCtx`) [VERIFIED: codebase
   grep — `clientCtx` is private at `internal/acp/client.go:246`, must be
   surfaced].
2. Lazy synchronous re-spawn at acquire — a single-caller cost, no supervisor.
3. A new `internal/session/registry.go` package — registry + reaper +
   per-entry mutex + bounded `SESSION_MAX` — wired into `engine.ACPClient`
   so surface handlers reach it through the same `engine.Run` path that
   already carries the Phase 4 watchdog.
4. A new `GET /health/agents` endpoint exposing per-slot + per-session
   detail, mounted on the same auth-exempt outer router as `/health`.
5. Bounded-time shutdown ordering in `cmd/otto-gateway/main.go`.

**Primary recommendation:** Reuse the Phase 2 Codex M-3 slot-release pattern
(`sync.Once` + map-delete-first + closure-based release) **verbatim** for
the registry-entry race between Reaper / DELETE / disconnect-cancel /
Result. Every D-07/D-08/D-12 race in CONTEXT.md collapses onto this prior
art — and the prior art has a passing `goleak` gate in `internal/pool/`.
The only genuinely new mechanic is the `acp.Client.Done()` channel; build
it once, gate it with `goleak`, and Phase 5 is a structurally smaller
phase than Phase 4.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

**Dead-slot detection & lazy re-spawn (POOL-04):**

- **D-01: Push-based exit detection.** Each slot has a per-slot exit-watcher
  goroutine that subscribes to the `acp.Client`'s existing 60s heartbeat /
  subprocess-exit signal. When the client signals death, the watcher marks
  the slot dead **immediately** — detection is pre-Acquire, not lazy on the
  next request. The watcher MUST exit on `Pool.Close` / successful
  re-spawn; `goleak` is the gate.
- **D-02: Lazy synchronous re-spawn at Acquire.** When `Acquire` pops a slot
  marked dead, spawn a fresh `acp.Client` in-line **before** handing the
  slot to the caller. Matches Node parity. One caller pays warmup latency;
  no always-on supervisor.
- **D-03: Spawn-failure surfaces as 503; pool shrinks.** If lazy re-spawn
  fails, `Pool.NewSession` returns a wrapped typed error → 503 Service
  Unavailable. The dead slot is **dropped from `p.all`** — pool effective
  size decreases. Operator sees the shrink in `/health/agents`.

**Session-to-subprocess ownership (SESS-01):**

- **D-04: Separate `SessionRegistry`, not a pool slot.** New
  `internal/session/registry.go`, keyed by `sid`, owns dedicated
  `acp.Client`s **outside** the warm pool.
- **D-05: Lazy create on first request with new `X-Session-Id`.** No
  explicit `POST /v1/sessions` endpoint.
- **D-06: Env-driven `SESSION_MAX=32` cap; overflow → 503.** New env var
  (not in Node reference).
- **D-07: Per-session mutex serializes concurrent requests on the same
  `sid`.** Second concurrent request blocks until first stream completes
  (or its ctx cancels). No 409 surface.
- **D-08: `DELETE /v1/sessions/:id` cancels in-flight, closes, returns
  `{deleted: id}`.** Calls `entry.Client.Cancel(sid)` then
  `entry.Client.Close()` then removes the map entry. Unknown sid → 404.
- **D-09: `SetModel` is per-request, only on diff.** Session entry caches
  the last-set model; only calls `SetModel` if request model differs.

**Reaper mechanics (SESS-02):**

- **D-10: 60s ticker, `SESSION_TTL_MS` default 1,800,000 (30 min) — Node
  parity.** Single reaper goroutine on `registry.Start(ctx)`. Both env
  names match the brief's backward-compat contract exactly.
- **D-11: `last_used` updated at response complete.** TTL measures "time
  since session was last actively serving traffic." Touched **after** the
  stream's `Result()` returns. Two layers of protection against mid-stream
  reap (D-11 + D-12).
- **D-12: Reaper takes the per-entry mutex; skips in-flight.** Reaper
  attempts `TryLock` on each entry's per-session mutex. If locked (=
  stream in flight), skip. If `TryLock` succeeds and `now - last_used >
  TTL`, defensively call `Cancel(sid)` then `Close()`, then delete the
  map entry.
- **D-13: `ttl + tickInterval` are constructor params, not globals.** Tests
  pass `TTL: 200*time.Millisecond, TickInterval: 50*time.Millisecond` for
  a real-time SESS-02 test that completes in <1s.

**`/health/agents` shape (OBSV-02):**

- **D-14: New endpoint `GET /health/agents`, separate from `/health`.**
- **D-15: Per-slot row shape:**
  ```json
  { "label": "slot-0", "alive": true, "busy": false,
    "current_session_id": null }
  ```
- **D-16: Per-session row shape:**
  ```json
  { "id": "sess-abc123", "alive": true, "busy": false,
    "last_used": "2026-05-26T14:32:18Z", "model": "claude-sonnet-4-7" }
  ```
  `model` is nullable; `last_used` is RFC 3339 / ISO 8601.
- **D-17: Full session ids verbatim — no redaction.**
- **D-18: `/health/agents` is auth-exempt — same as `/health`.**

### Claude's Discretion

- Whether the per-slot exit-watcher (D-01) lives inside `acp.Client` itself
  (as a `Done()` channel) or as a new goroutine in `internal/pool/`
  watching a client-exposed signal — provided `goleak` passes.
- Concrete struct layout of `internal/session/registry.go` (Entry vs
  Session naming, where the mutex lives, whether `Acquire`/`Release`
  methods exist or surface handlers call `Prompt` directly on the entry).
- How `/health/agents` discovers the registry — passed into
  `server.NewFromConfig` as a new `RegistryStatsSource` interface (mirror
  of `PoolStatsSource`), or via a single combined `AgentDetailSource`.
- Whether `Pool.Close` waits for in-flight registry sessions to drain or
  fires them in parallel — provided `goleak` passes and shutdown
  completes in bounded time.
- The exact wire shape of `/health/agents` (object with `{pool: {...},
  sessions: [...]}` vs flat `{slots: [...], sessions: [...]}`).

### Deferred Ideas (OUT OF SCOPE)

- PID, started_at, error_count in `/health/agents` — Phase 9.
- Real token counts in session rows — kiro-cli doesn't report them.
- Per-session metrics: message_count, total_tokens, last_model history.
- `HEALTH_AGENTS_AUTH=required` knob — reverse-proxy in front instead.
- POST `/v1/sessions` explicit-create endpoint.
- Adaptive reaper cadence (ticker = TTL/4).
- Hash/truncate `X-Session-Id` in logs and `/health/agents`.
- Cross-session model affinity.

</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| POOL-01 | Fixed-size pool (default `POOL_SIZE=4`) of warm `kiro-cli` subprocesses. | §Standard Stack — confirm `internal/config/config.go` env default flips from 1 to 4; pool's `applyDefaults` in `internal/pool/config.go` already supports any size. |
| POOL-02 | Pool warmup completes before `http.Server.ListenAndServe()` accepts connections. | Already wired in `cmd/otto-gateway/main.go:140-148` — Phase 5 inherits unchanged. |
| POOL-03 | `Acquire` returns the first free slot or blocks on a buffered channel of free slots. `Release` returns the slot to the channel. | Already implemented in `internal/pool/pool.go:263-284` (`select` on `<-p.slots` vs `ctx.Done()`). Phase 5 adds the dead-slot detection branch (D-02) inside the same `select` path. |
| POOL-04 | Dead slots are detected and re-spawned lazily without blocking other acquires. | §Architecture Patterns Pattern 1 (`Pool.NewSession` dead-slot loop) + §Architecture Patterns Pattern 2 (per-slot exit watcher) — D-01..D-03 in CONTEXT.md. |
| SESS-01 | Requests with `X-Session-Id` header use a dedicated `kiro-cli` subprocess via `SessionRegistry`, not the warm pool. | §Architecture Patterns Pattern 3 (`internal/session.Registry`) + §Architecture Patterns Pattern 6 (surface-handler routing on header). |
| SESS-02 | Idle sessions reaped after `SESSION_TTL_MS` (default 1,800,000 = 30 min). Reaper runs every 60s. | §Architecture Patterns Pattern 4 (reaper loop) — Node parity verified at `acp-ollama-server.js:378,402-407`. |
| SESS-03 | `DELETE /v1/sessions/:id` tears down a stateful session immediately and returns `{deleted: "<id>"}`. | §Architecture Patterns Pattern 5 (DELETE handler) + §Common Pitfalls Pitfall 1 (DELETE-during-stream race). |
| OBSV-02 | `GET /health/agents` returns per-pool-slot detail (`alive`, `busy`, `label`) and per-session detail (`alive`, `last_used`). | §Architecture Patterns Pattern 7 (`agentsHandler`) + §Code Examples block 4 — full wire shape per D-15/D-16. |

</phase_requirements>

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Stateless-request slot acquisition + dead-slot detection | `internal/pool` | `internal/acp` (via `Done()` exit signal) | Pool owns slot lifecycle; ACP owns subprocess lifecycle. The push-based exit signal is the one new ACP responsibility (Done channel). |
| Per-slot exit watcher goroutine | `internal/pool` | — | Watcher is pool-scoped (one per slot, spawned by `initSlot`, torn down by `Pool.Close` or successful re-spawn). Lives next to the slot it watches. |
| Stateful-session entry storage | `internal/session` | — | New package. Registry owns `map[sid]*Entry`. Disjoint from pool's slot lifecycle. |
| Reaper goroutine | `internal/session` | — | Single goroutine started by `Registry.Start(ctx)`. Touches only registry state (entry mutex + map). |
| Per-session mutex (D-07 / D-12) | `internal/session` | — | Lives on the `Entry` struct. Used by both surface handlers (before `Prompt`) and reaper (`TryLock`). |
| `X-Session-Id` extraction + routing | `internal/adapter/{ollama,openai,anthropic}` | `internal/engine` (consumer) | Header parsing is surface-aware; the resulting `engine.ACPClient` (Pool vs Session entry) is the routing decision. |
| `/health/agents` JSON rendering | `internal/server` | `internal/pool` + `internal/session` (data source) | Server already owns `/health` rendering; `/health/agents` is the new sibling handler. |
| `DELETE /v1/sessions/:id` | `internal/server` (handler) | `internal/session` (impl) | Handler is auth-protected (lives behind the `/v1` prefix's auth wrapper); calls `registry.Delete(sid)`. |
| Shutdown ordering | `cmd/otto-gateway` | — | `main.go`'s `newApp` cleanup closure already owns this seam; Phase 5 extends it with registry teardown before pool close. |

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go stdlib `sync` | 1.23+ | `sync.Mutex` (TryLock added in Go 1.18), `sync.Once`, `sync.Map` (avoided — see Pitfall 5) | Phase 2/4 already use this pattern; `TryLock` is the load-bearing primitive for D-12 reaper. [VERIFIED: Go 1.18+ release notes] |
| Go stdlib `context` | 1.23+ | `context.AfterFunc` (Phase 4 watchdog already uses this), `context.WithCancel`, `context.WithTimeout` | Phase 4 D-06 already proves the pattern in `internal/engine/engine.go:196`. `context.AfterFunc` documented contract: returned `stop()` returning false means f already started; caller coordinates completion explicitly. [CITED: pkg.go.dev/context — fetched 2026-05-26] |
| Go stdlib `time` | 1.23+ | `time.Ticker` for reaper (D-10) — 60s default | Node parity (`setInterval(..., 60_000)` at `acp-ollama-server.js:378`). |
| `go.uber.org/goleak` | v1.3.0 | Goroutine leak detection in `TestMain` | Already in `go.mod`; `internal/pool/testmain_test.go` and `internal/engine/testmain_test.go` enforce. `internal/session/testmain_test.go` ships day one. [VERIFIED: go.sum + pkg.go.dev/go.uber.org/goleak] |
| `github.com/go-chi/chi/v5` | v5.3.0 | HTTP routing — `DELETE /v1/sessions/:id` URL param via `chi.URLParam` | Already in `go.mod`; same router server uses. [VERIFIED: go.sum] |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `encoding/json` (stdlib) | 1.23+ | `time.Time.MarshalJSON` emits RFC 3339 by default → D-16 `last_used` field needs no custom marshalling | Use the default `Time` JSON encoding for `LastUsed`. |
| `log/slog` (stdlib) | 1.23+ | Structured logging on reaper events + DELETE + dead-slot transitions | Project standard (Phase 1 D-15 — never `slog.SetDefault`; logger injected via `Config`). |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Buffered chan size 1 as per-entry "mutex" | `sync.Mutex` with `TryLock` | Mutex is clearer; `TryLock` documented since Go 1.18; matches the D-12 "Reaper attempts a `TryLock`" wording verbatim. Channel works but obscures intent and complicates the reaper's "skip if locked" branch. **Decision: `sync.Mutex` with `TryLock`.** |
| `sync.Map` for `map[sid]*Entry` | `map[string]*Entry` + `sync.RWMutex` | `sync.Map` is optimised for read-mostly key sets that grow without bound — wrong shape for the registry (the reaper iterates the whole map every 60s; surface handlers do single-key lookups). Plain map + RWMutex matches `internal/pool/pool.go:55-66` (`sessionSlots` is exactly this shape and was deliberately chosen over `sync.Map`). **Decision: `map` + `sync.RWMutex`.** |
| Always-on supervisor goroutine for dead-slot re-spawn | Lazy re-spawn at acquire (D-02) | Supervisor adds a goroutine, a wake mechanism, and a race against the slot returning to `p.slots`. Node reference uses lazy (`acquire()` re-inits on `!slot.client?.alive` at `acp-ollama-server.js:352`). **Decision: lazy — locked by D-02.** |
| Adaptive reaper cadence (`tickInterval = TTL / 4`) | Fixed 60s default | Adds a coupling between TTL and tick rate that the Node code doesn't have. Tests inject explicit `TickInterval` via D-13 so prod stays at the fixed 60s. **Decision: fixed; deferred per CONTEXT.md.** |
| Single combined `AgentDetailSource` interface | Two interfaces: `PoolStatsSource` (already exists) + new `RegistryStatsSource` | Phase 2's `PoolStatsSource` is the established pattern; adding a parallel `RegistryStatsSource` mirrors it 1:1. A combined interface couples the two for no callsite benefit (`agentsHandler` reads both anyway). **Decision: two interfaces.** Discretion item — planner may flip if a strong reason emerges. |

**Installation:** No new module dependencies needed. Phase 5 is pure stdlib + already-vendored deps.

**Version verification:**
```bash
go list -m go.uber.org/goleak    # → v1.3.0 [VERIFIED]
go list -m github.com/go-chi/chi/v5  # → v5.3.0 [VERIFIED]
go version                            # → go1.26.3 darwin/arm64 (project requires 1.23+) [VERIFIED]
```

## Package Legitimacy Audit

> Phase 5 introduces **zero new external dependencies**. The Standard Stack
> draws exclusively from Go stdlib + packages already pinned in `go.mod`
> and `go.sum` (vendored during Phase 1 / Phase 2 / Phase 4 milestones).

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| `go.uber.org/goleak` | proxy.golang.org | v1.3.0 (Oct 2023, stable) | tens-of-millions/yr | github.com/uber-go/goleak | N/A (Go module, not npm/PyPI) | Approved — already pinned in `go.sum` |
| `github.com/go-chi/chi/v5` | proxy.golang.org | v5.3.0 | tens-of-millions/yr | github.com/go-chi/chi | N/A | Approved — already pinned in `go.sum` |

**Packages removed due to slopcheck [SLOP] verdict:** none — no new installs proposed.
**Packages flagged as suspicious [SUS]:** none.

*Rationale: slopcheck targets npm/PyPI typosquats. Go modules use a different
supply chain (Go module proxy + checksum database), and both packages above
were vetted into `go.sum` during Phase 1. Slopcheck does not apply to this
phase.*

## Architecture Patterns

### System Architecture Diagram

```
                                  ┌────────────────────────────┐
HTTP request                      │  X-Session-Id header set?  │
─────────────────────────────────▶│                            │
                                  └─────┬─────────────────┬────┘
                                        │ no              │ yes
                                        ▼                 ▼
                          ┌─────────────────────┐  ┌──────────────────────────────┐
                          │  engine.Run(pool)   │  │  registry.Get(sid, cwd)      │
                          │  (stateless path)   │  │  → lazy-create if absent     │
                          │                     │  │  → SESSION_MAX check         │
                          └──────┬──────────────┘  │  → Entry.Mu.Lock (D-07)      │
                                 │                 └────────┬─────────────────────┘
                                 │                          │
                                 ▼                          ▼
                          ┌─────────────────────┐  ┌──────────────────────────────┐
                          │  Pool.NewSession    │  │  engine.Run(entry-as-ACPClient)│
                          │   acquire slot      │  │   (stateful path; reuses same │
                          │   if dead → respawn │  │    Phase 4 watchdog for ctx-  │
                          │   (D-01/D-02/D-03)  │  │    cancel → session/cancel)   │
                          └──────┬──────────────┘  └────────┬─────────────────────┘
                                 │                          │
                                 ├──────────────┬───────────┘
                                 ▼              ▼
                          ┌─────────────────────────────────────┐
                          │  acp.Client (kiro-cli subprocess)   │
                          │   + Done() exit signal (new — D-01) │
                          │   + 60s ping loop kills on failure  │
                          └─────────────────────────────────────┘

         ┌─────────────────────────────────┐                ┌─────────────────────────────────┐
         │  Per-slot exit-watcher (NEW)    │                │  Reaper goroutine (NEW)         │
         │   for slot := range p.all:      │                │   ticker := time.NewTicker(60s) │
         │     go func() {                 │                │   for tick := range ticker.C {  │
         │       select {                  │                │     for sid, e := range r.m {   │
         │         <-slot.Client.Done():   │                │       if e.Mu.TryLock() {       │
         │           markDead(slot)        │                │         if now - e.LastUsed >   │
         │         <-p.closing:            │                │             ttl {               │
         │           return                │                │           Cancel; Close; del    │
         │       }                         │                │         }                       │
         │     }()                         │                │         e.Mu.Unlock             │
         └─────────────────────────────────┘                │       }                         │
                                                            │     }                           │
                                                            │   }                             │
                                                            └─────────────────────────────────┘

         GET /health/agents (NEW, OBSV-02)                  DELETE /v1/sessions/:id (NEW, SESS-03)
         ┌─────────────────────────────────┐                ┌─────────────────────────────────┐
         │  pool.Detail() → per-slot rows  │                │  Cancel in-flight → Close →     │
         │  registry.Detail() → per-sess   │                │   delete entry → 200            │
         │  → render JSON (D-14/D-15/D-16) │                │   unknown sid → 404             │
         └─────────────────────────────────┘                └─────────────────────────────────┘
```

### Recommended Project Structure

```
internal/
├── pool/
│   ├── pool.go              # existing — gets dead-slot branch in NewSession + exit-watcher hook in initSlot
│   ├── config.go            # existing — Size default stays 1; env default flips to 4 in internal/config/config.go
│   ├── stats.go             # existing — extended with SlotDetail type (or new detail.go)
│   ├── detail.go            # NEW — per-slot detail rendering for /health/agents (D-15)
│   ├── exit_watcher.go      # NEW — per-slot exit-watcher goroutine (D-01)
│   ├── exit_watcher_test.go # NEW — goleak gate on watcher teardown
│   └── pool_test.go         # existing — extended with dead-slot detection tests
├── session/                 # NEW PACKAGE
│   ├── registry.go          # Registry + Entry + Config (D-04, D-13)
│   ├── reaper.go            # Reaper loop (D-10, D-12)
│   ├── detail.go            # Per-session detail for /health/agents (D-16)
│   ├── doc.go               # Package godoc with the load-bearing race-resolution diagram
│   ├── registry_test.go     # Black/whitebox tests
│   ├── reaper_test.go       # Real-time short-TTL reaper test (D-13)
│   └── testmain_test.go     # goleak.VerifyTestMain gate (matches internal/pool/testmain_test.go)
├── server/
│   ├── server.go            # existing — NewFromConfig gets Registry + new exempt route /health/agents
│   ├── health.go            # existing — extended; new agents.go handler
│   └── agents.go            # NEW — agentsHandler + AgentsResponse type (D-14)
├── acp/
│   └── client.go            # existing — adds Done() <-chan struct{} accessor (D-01 push signal)
└── adapter/
    └── {ollama,openai,anthropic}/handlers.go  # existing — each adds X-Session-Id branch (engine.Run(registry-entry-as-ACPClient) vs engine.Run(pool))

cmd/otto-gateway/main.go     # existing — newApp wires Registry + reaper start; cleanup drains registry before pool.Close
```

### Pattern 1: Lazy synchronous dead-slot re-spawn (POOL-04, D-01..D-03)

**What:** When the per-slot exit-watcher marks a slot dead, `Pool.NewSession`
discovers the dead slot when it pops from `p.slots`, spawns a fresh client
synchronously in-line, and either returns the new slot to the caller or
returns a typed error (→ 503) if re-spawn fails.

**When to use:** Inside `Pool.NewSession`'s existing `select { case <-p.slots; case <-ctx.Done() }`
block — after the slot is acquired and before the existing `slot.Client.NewSession`
call.

**Example:**
```go
// Source: D-02 / D-03 in 05-CONTEXT.md + Node parity acp-ollama-server.js:352
func (p *Pool) NewSession(ctx context.Context, cwd string) (string, error) {
    var slot *Slot
    select {
    case slot = <-p.slots:
        // acquired
    case <-ctx.Done():
        return "", fmt.Errorf("pool: acquire cancelled: %w", ctx.Err())
    }

    // D-01/D-02: dead-slot check + lazy re-spawn (NEW IN PHASE 5).
    // If the exit-watcher (Pattern 2) marked this slot dead while it was
    // sitting in p.slots, spawn a fresh client BEFORE handing it to the caller.
    if !p.slotAlive(slot) {
        if err := p.respawnSlot(ctx, slot); err != nil {
            // D-03: drop the dead slot from p.all so the pool's effective
            // size shrinks; do NOT return it to p.slots (caller would just
            // pop a dead slot again next acquire).
            p.removeSlot(slot)
            return "", fmt.Errorf("pool: respawn slot %s: %w", slot.Label, err)
        }
    }

    // existing path — unchanged
    sid, err := slot.Client.NewSession(ctx, cwd)
    if err != nil {
        p.slots <- slot
        return "", fmt.Errorf("pool: new-session: %w", err)
    }
    // ... rest of existing NewSession body unchanged
}
```

**Critical invariants:**
- `respawnSlot` MUST honor `ctx` (D-02 specifics item #2 from CONTEXT.md). A
  hung kiro-cli spawn cannot block a ctx-cancelled caller.
- The new `acp.Client` replaces `slot.Client` in place. The slot label and
  position in `p.all` survive (so `/health/agents` slot-N labels stay stable).
- After successful re-spawn, spawn a fresh exit-watcher for the new client
  (the old watcher exited when its old `Done()` fired).

### Pattern 2: Per-slot exit-watcher goroutine (POOL-04, D-01)

**What:** One goroutine per slot, spawned by `initSlot`, that selects on
`acp.Client.Done()` (new — see Code Examples block 1) and the pool's
shutdown signal. On `Done()` firing, the watcher marks the slot dead via
the same `p.mu`-guarded path the rest of the pool uses.

**When to use:** Every slot at every spawn time — initial warmup + every
lazy re-spawn must spawn a fresh watcher.

**Example:**
```go
// Source: D-01 in 05-CONTEXT.md
// Lives in internal/pool/exit_watcher.go.
func (p *Pool) startExitWatcher(slot *Slot) {
    go func() {
        select {
        case <-slot.Client.Done():
            // acp.Client tore down its subprocess (ping failure or readLoop EOF).
            // Mark the slot dead — Pattern 1 picks it up at next Acquire.
            p.mu.Lock()
            slot.dead = true
            p.mu.Unlock()
            p.cfg.Logger.Info("pool: slot died", "label", slot.Label)
        case <-p.closing:
            // Pool.Close fired — exit cleanly so goleak passes.
            return
        }
    }()
}
```

**`p.closing` is a new `chan struct{}` closed by `Pool.Close` (BEFORE the
existing `closeAll` body) so every watcher's `<-p.closing` branch fires
deterministically.** The shutdown order changes minimally:

```go
func (p *Pool) Close() error {
    p.closeOnce.Do(func() {
        close(p.closing)         // NEW: signal watchers to exit
        firstErr = p.closeAll()   // existing
    })
    return firstErr
}
```

### Pattern 3: Session registry with per-entry mutex (SESS-01, D-04, D-07)

**What:** A new `internal/session` package owning a `map[sid]*Entry` guarded
by `sync.RWMutex`. Each `Entry` carries its own `sync.Mutex` that the
surface handler acquires before `Prompt` (D-07) and the reaper acquires
via `TryLock` (D-12).

**When to use:** Every stateful request — surface handler does
`entry, err := registry.Get(ctx, sid, cwd)` then `entry.Mu.Lock()` then
`engine.Run(ctx, entry)`.

**Example:**
```go
// Source: D-04 + D-07 + D-13 in 05-CONTEXT.md
package session

type Entry struct {
    Mu        sync.Mutex          // D-07: serializes per-sid requests; D-12: TryLock in reaper
    Client    *acp.Client          // dedicated kiro-cli for this sid
    SessionID string               // ACP session id from session/new
    LastUsed  time.Time            // D-11: updated at response complete (NOT at request start)
    LastModel string               // D-09: avoid redundant SetModel calls
    Dead      bool                 // set by exit-watcher / DELETE / reaper; checked under registry mutex
}

type Config struct {
    Logger       *slog.Logger
    Factory      ClientFactory     // injected for tests; production uses acp.New
    TTL          time.Duration     // D-10 default 30min; D-13 tests inject 200ms
    TickInterval time.Duration     // D-10 default 60s; D-13 tests inject 50ms
    MaxSessions  int               // D-06 default 32
    KiroCWD      string            // default cwd for sessions
}

type Registry struct {
    cfg     Config
    mu      sync.RWMutex
    entries map[string]*Entry
    closing chan struct{}          // signals reaper to exit (Pattern 4)
    wg      sync.WaitGroup         // waits for reaper on Close
}

func New(cfg Config) *Registry { ... }

// Start spawns the reaper goroutine. ctx termination triggers reaper exit
// (in addition to the explicit Close path).
func (r *Registry) Start(ctx context.Context) { ... }

// Get returns the existing entry or lazy-creates one. SESSION_MAX gate
// surfaces a typed error (→ 503 by surface adapters).
func (r *Registry) Get(ctx context.Context, sid, cwd string) (*Entry, error) {
    r.mu.Lock()
    if e, ok := r.entries[sid]; ok && !e.Dead {
        r.mu.Unlock()
        return e, nil
    }
    // D-06: SESSION_MAX gate
    if len(r.entries) >= r.cfg.MaxSessions {
        r.mu.Unlock()
        return nil, ErrSessionMaxExceeded
    }
    r.mu.Unlock()
    return r.createEntry(ctx, sid, cwd)  // lock dropped during slow spawn
}

// Delete tears down a session synchronously. D-08 semantics.
func (r *Registry) Delete(sid string) error {
    r.mu.Lock()
    e, ok := r.entries[sid]
    if !ok {
        r.mu.Unlock()
        return ErrSessionNotFound  // → 404
    }
    delete(r.entries, sid)  // map-delete FIRST (Codex M-3 pattern)
    r.mu.Unlock()
    e.Client.Cancel(e.SessionID)  // cancel in-flight prompt (D-08)
    return e.Client.Close()
}

func (r *Registry) Close() error {
    close(r.closing)        // signal reaper exit
    r.wg.Wait()             // bounded by tick interval
    // tear down all entries — bounded-time per CONTEXT.md Claude's Discretion
    r.mu.Lock()
    entries := r.entries
    r.entries = nil
    r.mu.Unlock()
    var firstErr error
    for _, e := range entries {
        if err := e.Client.Close(); err != nil && firstErr == nil { firstErr = err }
    }
    return firstErr
}
```

### Pattern 4: Reaper with TryLock skip-in-flight (SESS-02, D-10, D-11, D-12)

**What:** Single goroutine, `time.NewTicker(cfg.TickInterval)`. On every
tick walk the map; on each entry try `e.Mu.TryLock()`. If locked, skip.
If acquired and `time.Since(e.LastUsed) > cfg.TTL`, cancel + close + delete.

**Example:**
```go
// Source: D-10/D-11/D-12 in 05-CONTEXT.md; Node parity acp-ollama-server.js:402-407
func (r *Registry) reaperLoop() {
    defer r.wg.Done()
    ticker := time.NewTicker(r.cfg.TickInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            r.reapOnce()
        case <-r.closing:
            return
        }
    }
}

func (r *Registry) reapOnce() {
    // Snapshot the entry list under the registry mutex so the inner loop
    // can release the registry mutex before touching per-entry mutexes.
    // (Holding both locks invites the reverse-lock-order deadlock against
    // surface handlers that take the entry mutex first then need registry
    // mutex on map-delete.)
    r.mu.RLock()
    snapshot := make([]*entryAndSID, 0, len(r.entries))
    for sid, e := range r.entries {
        snapshot = append(snapshot, &entryAndSID{sid: sid, entry: e})
    }
    r.mu.RUnlock()

    now := time.Now()
    cutoff := now.Add(-r.cfg.TTL)
    for _, es := range snapshot {
        if !es.entry.Mu.TryLock() {
            continue  // D-12: in-flight stream, skip this tick
        }
        // D-11: LastUsed is updated at response complete, so an active
        // stream's entry will not yet show as expired (defense in depth
        // against mid-stream reap).
        if es.entry.LastUsed.Before(cutoff) {
            // Defensively cancel the (presumed-idle) session before close.
            es.entry.Client.Cancel(es.entry.SessionID)
            _ = es.entry.Client.Close()
            r.mu.Lock()
            delete(r.entries, es.sid)
            r.mu.Unlock()
            r.cfg.Logger.Info("session: reaped", "sid", es.sid,
                              "idle_for", now.Sub(es.entry.LastUsed))
        }
        es.entry.Mu.Unlock()
    }
}
```

### Pattern 5: DELETE /v1/sessions/:id (SESS-03, D-08)

**What:** Auth-protected handler under the OpenAI/Anthropic `/v1` prefix.
Calls `registry.Delete(sid)`; renders `{"deleted": "<id>"}` (200) on success
or `{"type":"error","error":{...}}` (404) for unknown sid.

**When to use:** Mounted on the `/v1` prefix's protected sub-router (NOT
auth-exempt — operator-only path) via a new `SessionRouter` `RouteRegistrar`
or directly inside the OpenAI surface mount block.

**Example:**
```go
// Source: D-08 in 05-CONTEXT.md
func (h *SessionsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
    sid := chi.URLParam(r, "id")
    if sid == "" {
        writeError(w, http.StatusBadRequest, "missing session id")
        return
    }
    if err := h.registry.Delete(sid); err != nil {
        if errors.Is(err, session.ErrSessionNotFound) {
            writeError(w, http.StatusNotFound, "unknown session")
            return
        }
        writeError(w, http.StatusInternalServerError, "delete failed")
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(map[string]string{"deleted": sid})
}
```

### Pattern 6: Surface-handler routing on X-Session-Id (SESS-01)

**What:** Each adapter handler reads `r.Header.Get("X-Session-Id")` BEFORE
calling `engine.Run`. Empty → pool path (current behavior, unchanged).
Non-empty → resolve via registry, take per-entry mutex, then call engine
with the entry-as-ACPClient.

**Critical invariant:** Both branches go through `engine.Run`. The Phase 4
D-06 watchdog therefore applies to stateful sessions too — disconnect
cancellation is free.

**Example (Ollama; OpenAI + Anthropic mirror this):**
```go
// Source: SESS-01 + Phase 4 D-06 carry-forward
func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
    sid := r.Header.Get("X-Session-Id")
    var client engine.ACPClient
    if sid != "" {
        entry, err := h.registry.Get(r.Context(), sid, h.cfg.KiroCWD)
        if err != nil {
            if errors.Is(err, session.ErrSessionMaxExceeded) {
                writeError(w, http.StatusServiceUnavailable, "session capacity exceeded")
                return
            }
            // ... other errors → 5xx
            return
        }
        entry.Mu.Lock()
        defer entry.Mu.Unlock()      // D-07: serializes per-sid
        defer entry.MarkUsed()         // D-11: update LastUsed AFTER stream completes
        client = entry  // *session.Entry satisfies engine.ACPClient — see Code Examples block 3
    } else {
        client = h.pool
    }
    // engine.Run already gets the Phase 4 D-06 watchdog applied to its ctx.
    run, err := h.engine.RunWithACP(r.Context(), canonReq, client)
    // ... rest unchanged
}
```

### Pattern 7: `/health/agents` handler (OBSV-02, D-14..D-18)

**What:** New `agentsHandler` mounted on the OUTER router (exempt routes),
mirroring `/health`. Reads `pool.Detail()` and `registry.Detail()` (or
equivalent via the new interface) and renders the locked D-15/D-16 shapes.

**Wire shape (recommended — discretion item in CONTEXT.md):**
```json
{
  "pool": {
    "size": 4,
    "alive": 4,
    "busy": 1,
    "slots": [
      { "label": "slot-0", "alive": true, "busy": true,  "current_session_id": null },
      { "label": "slot-1", "alive": true, "busy": false, "current_session_id": null },
      { "label": "slot-2", "alive": true, "busy": false, "current_session_id": null },
      { "label": "slot-3", "alive": true, "busy": false, "current_session_id": null }
    ]
  },
  "sessions": [
    { "id": "sess-abc123", "alive": true,  "busy": false,
      "last_used": "2026-05-26T14:32:18Z", "model": "claude-sonnet-4-7" }
  ]
}
```

**Why object-keyed (`{pool, sessions}`) instead of flat (`{slots, sessions}`):**
- Additive-friendly per CONTEXT.md Specific Ideas. Adding `embeddings: {...}`
  in Phase 7 just adds a sibling object.
- Reuses the same key (`pool`) that `/health` already exposes for the
  summary shape — operators read both endpoints; consistent key names
  reduce confusion.

### Anti-Patterns to Avoid

- **Holding the registry mutex across `acp.Client.Close()`.** Close calls
  involve waiting on the subprocess `wg` (line 947 of `acp/client.go`) —
  potentially blocking on EOF. Lock ordering must be: (1) acquire
  registry mutex, (2) snapshot or delete entry, (3) drop registry mutex,
  (4) call Close. `Pool.closeAll` already follows this discipline at
  `internal/pool/pool.go:216-238`.
- **Holding per-entry mutex during slow `Initialize`/`NewSession`.** Lazy
  creation in `Registry.Get` MUST happen WITHOUT holding the entry mutex
  (the entry doesn't exist yet anyway). The registry mutex protects the
  "is this sid already creating?" check; the slow spawn happens outside
  any lock. Use a per-sid creation `sync.Once` keyed off the registry
  mutex, or a "creating" sentinel pattern, to avoid two concurrent
  requests with the same new sid both spawning a subprocess.
- **Reaper holding both mutexes simultaneously.** See Pattern 4 — the
  reaper snapshots under the registry mutex, RELEASES it, then iterates.
  Otherwise a surface handler holding the entry mutex and trying to
  delete from the registry (e.g., on a kiro-cli death mid-stream)
  deadlocks against the reaper.
- **Using `sync.Map` for `entries`.** `sync.Map` is documented for
  read-mostly workloads where keys are written once and read many times.
  The registry writes every entry once and the reaper iterates the whole
  map every 60s — wrong shape. Use plain map + RWMutex.
- **Adding a `Detail()` method to `engine.ACPClient`.** CONTEXT.md
  Specific Ideas item #6 calls this out explicitly. The agent-detail
  source is a separate interface that `Pool` and `Registry` both
  satisfy; keep `engine.ACPClient` narrow (the engine boundary doesn't
  need observability).
- **Coupling `last_used` to request-start.** Node's reference does this
  (`entry.lastUsed = Date.now()` at `acp-ollama-server.js:394`, inside
  `acquire`). D-11 deviates intentionally — see Pitfall 3.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Cooperative goroutine teardown | Custom done-channel + `sync.Once` plumbing | Existing `context.WithCancel` + `chan struct{}` patterns from `internal/pool/pool.go` | Phase 2/4 already proved the pattern; reuse exactly. |
| Subprocess-death detection | A custom signal type + reflection over `os.Process.State` | `acp.Client.Done() <-chan struct{}` derived from the existing private `clientCtx` | The ping loop already kills the subprocess on failed pings (`internal/acp/client.go:439-453`); piggyback on its existing cancellation. |
| Per-entry "I'm in flight" tracking | Atomic bool + retry loop | `sync.Mutex.TryLock` | D-12 wording maps 1:1 onto TryLock; Go 1.18+ ships it; reaper test path is dead-simple. |
| RFC 3339 time formatting for JSON | `time.Time.Format(time.RFC3339)` plus custom MarshalJSON | Default `time.Time.MarshalJSON` (already RFC 3339) | Stdlib's default JSON encoding for `time.Time` is RFC 3339 — no custom marshaller needed for D-16. |
| Sequential reaper iteration with lock-held delete | Iterate while holding the registry write lock | Snapshot-then-iterate pattern (Pattern 4) | Holding the registry lock across `acp.Client.Close()` (which blocks on the subprocess WaitGroup) is a documented anti-pattern; `Pool.closeAll` already shows the snapshot+release+act discipline. |
| Slot-release race resolution | Roll a new lock + condition variable | `sync.Once` + map-delete-first + closure-based release | Codex M-3 already shipped this at `internal/pool/pool.go:415-464` — every D-08 / D-12 race in Phase 5 collapses onto this prior art. |
| Backwards-compat env var parsing | Roll new getEnv* helpers | `config.getEnvInt` / `config.getEnvDuration` already exist | `internal/config/config.go:381-456` handles both Go duration strings ("60s") and millisecond integers ("60000") for Node parity — `SESSION_TTL_MS` will go straight through. |
| HTTP path-param extraction | Manual `strings.Split` of `r.URL.Path` | `chi.URLParam(r, "id")` | chi already mounted; same pattern Phase 2 uses for `/api/show`. |

**Key insight:** Phase 5 is structurally smaller than Phase 4 because every
load-bearing concurrency primitive — slot-release race, watchdog, exit
handling on Close — already ships and is goleak-gated. The phase is wiring
+ one new package + one new endpoint. The single genuinely new mechanic is
the push-based exit signal on `acp.Client`, and that's a one-line accessor
on top of an existing private field.

## Runtime State Inventory

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | None — Phase 5 is in-memory state only (registry map cleared at process exit). No databases involved. | None. |
| Live service config | None — kiro-cli subprocesses are managed by the gateway process; no external service holds Phase 5 state. | None. |
| OS-registered state | None — POSIX wrapper script (`scripts/otto`) and PowerShell wrapper (`scripts/otto.ps1`) start the gateway as a foreground process; no Task Scheduler / systemd / launchd registrations carry Phase 5 state. | None. |
| Secrets/env vars | New env: `SESSION_MAX` (default 32, D-06) — must be documented in DEVELOPERS.md / scripts/otto help text. Existing env reused verbatim: `SESSION_TTL_MS`, `POOL_SIZE`, `PING_INTERVAL`, `KIRO_CMD`, `KIRO_CWD`. | Add `SESSION_MAX` to env contract docs; flip `POOL_SIZE` env default in `internal/config/config.go:129` from 1 to 4 (the package-level default in `internal/pool/config.go:117` STAYS at 1 for tests). |
| Build artifacts / installed packages | None — Phase 5 adds no compiled binaries or static assets. The single Go binary remains the only deliverable. | None. |

**The canonical question — *After every file in the repo is updated, what runtime systems still have the old string cached, stored, or registered?* — Phase 5 has no such surface.** This is a feature-addition phase with no rename/refactor element.

## Common Pitfalls

### Pitfall 1: DELETE-during-stream race (Codex M-3 prior art)

**What goes wrong:** Client A starts a stream against `sid=abc`. Client B
fires `DELETE /v1/sessions/abc` mid-stream. If DELETE blindly closes
`entry.Client` without taking the entry mutex, the in-flight Prompt's
readLoop crashes with `io.EOF` and the stream's channel closes mid-flight
— but the surface handler is still ranging over chunks and may have
already written headers, leaving the response truncated with no
diagnostic.

**Why it happens:** Three terminal paths now race for `entry.Client`:
Result-drain, ctx-cancel (Phase 4 watchdog), Reaper (D-12), DELETE (D-08).
Pool's Codex M-3 had three; registry has four. Without coordination, any
two firing in close succession produce undefined behavior.

**How to avoid:** Reuse the Codex M-3 pattern verbatim — map-delete-first
under the registry mutex, then close. The DELETE handler's `Registry.Delete`
implementation in Pattern 3 already does this:

```go
r.mu.Lock()
delete(r.entries, sid)  // map-delete FIRST — future Get(sid) returns "not found"
r.mu.Unlock()
e.Client.Cancel(e.SessionID)
return e.Client.Close()  // the in-flight Prompt's readLoop sees EOF, the
                          // Phase 4 watchdog also fires, but Done() is
                          // idempotent and the stream's sync.Once
                          // (acp/stream.go:106) coordinates the close
                          // exactly once
```

**Warning signs:** Test flakes that report "stream closed with empty
result" only when DELETE and a streaming request hit at the same instant.

### Pitfall 2: Per-slot exit watcher leaks on re-spawn

**What goes wrong:** Pattern 1's lazy re-spawn creates a new
`*acp.Client`, but the OLD client's exit-watcher goroutine is still
running, blocked on the OLD `client.Done()` (which will never fire
because the new client owns a different `clientCtx`). Result:
goroutine-per-respawn leak; `goleak` fails.

**Why it happens:** Easy to forget that the watcher is per-Client, not
per-slot. Re-spawn replaces the Client but not the watcher.

**How to avoid:** Before re-spawn, signal the old watcher to exit. The
cleanest mechanism: the old `acp.Client.Done()` fires when `clientCtx`
is cancelled — which it IS (by `acp.Client.Close()` step 1 at
`internal/acp/client.go:925`). So if `respawnSlot` calls `oldClient.Close()`
on the old client BEFORE constructing the new one, the old watcher's
`<-slot.Client.Done()` branch wins and the watcher exits cleanly. Then
spawn the new client, replace `slot.Client`, then spawn a fresh watcher.

**Warning signs:** `goleak` reports a goroutine blocked in `select` over
a Done channel after `TestRespawnLazy` finishes.

### Pitfall 3: TTL is a lower bound, not an upper bound (D-12 + D-11 combined)

**What goes wrong:** Operator sees `SESSION_TTL_MS=600000` (10 min) and
assumes a session WILL be reaped 10 min after the last user message.
Actually:
- D-11: `LastUsed` only updates on response complete.
- D-12: Reaper skips entries with locked mutex (in-flight stream).
- Result: an active session that streams continuously will never be
  reaped, no matter how long it's been alive.

**Why it happens:** The combination of D-11 (lower-bound update) and
D-12 (skip in-flight) is correct (active sessions are NOT idle), but
the naming "TTL" + 30-min default implies an upper bound.

**How to avoid:** Document this prominently in the SC4 verification
("idle session reaped after SESSION_TTL_MS") with a "truly idle" qualifier.
Reaper test (D-13) must use a session with NO in-flight stream and
verify the reap.

**Warning signs:** Operator file an issue "I set TTL=60s but my chat
session has been alive for 4 hours."

### Pitfall 4: Lazy-create double-spawn under racing same-sid requests

**What goes wrong:** Two concurrent requests with the same new
`X-Session-Id=newsid` both enter `Registry.Get`, both see no existing
entry, both pass the SESSION_MAX gate, both call the factory, both spawn
a `kiro-cli` subprocess, and one wins the map-write race — leaving an
orphan kiro-cli subprocess running until process exit.

**Why it happens:** D-04/D-05 don't say anything about concurrent
lazy-create. The naive "check map, if absent spawn, then write to map"
is racy.

**How to avoid:** Hold the registry mutex across the
"check-and-create-placeholder" step. The placeholder is a partial
`*Entry` with a `creating` sentinel; subsequent same-sid requests block
on the placeholder's mutex. The slow spawn happens with the registry
mutex released but the placeholder's mutex held. Alternative:
per-sid `sync.Once` keyed off the registry mutex. Tests must exercise
the racing-same-sid case (Verification section).

**Warning signs:** `/health/agents` shows two entries claiming the same
sid; or `lsof` shows two kiro-cli children of the gateway PID with
matching tty.

### Pitfall 5: Reaper-vs-Close goroutine leak on shutdown

**What goes wrong:** `cmd/otto-gateway/main.go` cleanup calls
`registry.Close()` but the reaper is mid-iteration when `closing` fires;
it doesn't see the signal until the next `ticker.C` tick (up to 60s
later). `Close()` blocks on `wg.Wait()` for those 60s, exceeding the
gateway's expected shutdown deadline.

**Why it happens:** Reaper's outer select only checks `closing` between
ticks. A reap that's mid-iteration won't notice for the rest of the tick.

**How to avoid:** Either (a) keep the inner iteration fast enough that
the worst-case delay is bounded by the iteration time (acceptable —
each iteration is one `TryLock` + at most one `Cancel`+`Close`), or (b)
add a second `select { <-r.closing }` check inside the inner loop
between entries. Phase 4 watchdog teardown faced the same shape and
chose (a) — the iteration body is provably bounded. Document the
shutdown deadline (Pattern 4: cleanup blocks up to `TickInterval` + worst-case
close-all time).

**Warning signs:** Integration test that calls `Close()` and reports a
20s+ shutdown when it should be sub-second.

### Pitfall 6: `/health/agents` exposing stale slot.dead state

**What goes wrong:** Exit-watcher marks slot dead at T+0. `/health/agents`
called at T+1ms — before Pattern 1's lazy re-spawn — reports
`{"alive": false, "busy": false}`. Operator dashboards interpret as "pool
shrunk." But the slot will re-spawn on the next Acquire.

**Why it happens:** "Dead" in Pattern 1 means "needs re-spawn on next
Acquire", not "permanently gone." The wire shape doesn't disambiguate.

**How to avoid:** Two readings — both valid:
- (a) Report `alive: false` honestly. An operator who needs to know "is
  the gateway serving traffic right now" reads the SUMMARY (`pool.alive`
  goes to 3, `pool.size` stays at 4). Drilling into per-slot detail
  exposes the lazy-respawn state machine accurately.
- (b) Add a `dead_pending_respawn: true` field to the slot row.
  Speculative DRY — defer until an operator complains.

**Recommendation:** Ship (a) — D-15 already locks the row shape; (b) is
deferred per Specific Ideas item #4 (deferred fields under operational
need).

**Warning signs:** Alerting fires `pool.alive < pool.size` on every kiro-cli
auto-respawn, generating noise.

### Pitfall 7: `SESSION_MAX` env-var collision with future Node deployments

**What goes wrong:** Operator running the Node implementation alongside
otto-gateway during cutover sets `SESSION_MAX=32` on the host. Node ignores
it (the var doesn't exist there). Otto-gateway honors it. Operator
forgets which behavior is which.

**Why it happens:** D-06 introduces a NEW env var that doesn't have a
Node equivalent. The brief's backward-compat contract is for env names
to MATCH the Node version — a new var doesn't violate the contract but
is easy to confuse.

**How to avoid:** Document `SESSION_MAX` prominently in the env-var
table in `docs/operating.md` with a "**NEW IN OTTO** — no Node
equivalent" annotation. Same convention used for any future env var
additions.

**Warning signs:** Cutover-period support tickets confused about pool
sizing limits.

## Code Examples

Verified patterns from the existing codebase, ready to extend.

### Block 1: Adding `Done()` to `acp.Client` (D-01 push signal)

```go
// internal/acp/client.go — additions

// Done returns a channel that is closed when the client's subprocess has
// exited (either via Close() or via the ping loop killing on a failed
// ping that isn't "method not found"). Multiple readers may select on
// the returned channel; the channel is closed exactly once.
//
// Done is the push-based exit signal added in Phase 5 (D-01) for the
// per-slot exit-watcher in internal/pool. It is intentionally a
// receive-only chan struct{} (no error payload) so the watcher's select
// branch reads like the canonical context-cancellation pattern.
//
// The channel is derived from the existing private clientCtx — Close()
// step 1 (line 925) cancels clientCtx, so Done() fires for the same
// teardown paths that already fire ErrClientClosed for in-flight callers.
func (c *Client) Done() <-chan struct{} {
    return c.clientCtx.Done()
}
```

**Verification:**
- This is a one-line accessor; no new fields, no new goroutines.
- The signal fires for every existing teardown path: explicit Close(),
  ping-loop kill, readLoop EOF (which calls c.cancel() via the CR-03
  defer at line 376).
- `goleak` already gates the acp package's TestMain
  (`internal/acp/testmain_test.go`) — exposing `Done()` introduces no
  new goroutine, so no new ignore is needed.

### Block 2: Pool's existing slot-release race pattern (Codex M-3) — reuse in registry

```go
// internal/pool/pool.go:330-368 — verbatim from current codebase

release := func() {
    p.mu.Lock()
    s, stillOwned := p.sessionSlots[sid]
    if stillOwned {
        delete(p.sessionSlots, sid)
    }
    p.mu.Unlock()
    if !stillOwned {
        // Another terminal path (Cancel) won the race and already
        // deleted the entry — that path will (or already did) send
        // to p.slots. Skip the send to avoid double-release.
        return
    }
    p.slots <- s
}
```

**Apply this to the registry's Reaper/DELETE/Result race:** the
`Registry.Delete` body in Pattern 3 already mirrors the `delete(map, sid)
then act` ordering. The session entry's per-entry mutex (D-07) serves
the same purpose as the pool's `sync.Once` — both ensure exactly-one of
the racing terminal paths wins.

### Block 3: `*session.Entry` satisfying `engine.ACPClient`

The cleanest wiring lets surface handlers pass the entry directly to
`engine.Run` (well, the engine bridge — see Pattern 6). The entry must
implement the engine.ACPClient interface (declared at
`internal/engine/engine.go:34-50`):

```go
// internal/session/entry_acp.go

// engine.ACPClient surface — Entry routes through its dedicated
// *acp.Client. Compare with *pool.Pool's surface (Pool.NewSession et al.
// at internal/pool/pool.go:263-392) — both implementations of the same
// interface; Entry is the per-session, Pool is the per-slot.

func (e *Entry) NewSession(ctx context.Context, cwd string) (string, error) {
    // The entry already has a long-lived ACP session from createEntry's
    // call to Client.NewSession. engine.Run expects a session id back —
    // return the cached one. (The session id is the SAME entity that
    // X-Session-Id maps to — kiro-cli's session id, not the HTTP sid.)
    return e.SessionID, nil
}

func (e *Entry) SetModel(ctx context.Context, sessionID, modelID string) error {
    // D-09: only call kiro-cli if model differs from cached.
    if modelID == e.LastModel {
        return nil
    }
    if err := e.Client.SetModel(ctx, sessionID, modelID); err != nil {
        return fmt.Errorf("session: set-model: %w", err)
    }
    e.LastModel = modelID
    return nil
}

func (e *Entry) Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (engine.Stream, error) {
    // Caller holds e.Mu (D-07) for the lifetime of the stream.
    // engine.Run's Phase 4 D-06 watchdog fires session/cancel on ctx
    // termination — works exactly the same way as pool. e.MarkUsed
    // (D-11) is called by the surface handler AFTER stream.Result()
    // returns.
    raw, err := e.Client.Prompt(ctx, sessionID, blocks)
    if err != nil {
        return nil, fmt.Errorf("session: prompt: %w", err)
    }
    // Use the same acpStreamShim pattern from engine/acp_adapter.go:66
    // — Entry doesn't need pool's slot-release wrapper because the
    // entry IS the slot for the duration of the session.
    return &acpStreamShim{s: raw}, nil
}

func (e *Entry) Cancel(sessionID string) {
    e.Client.Cancel(sessionID)
    // Note: NOT calling Mu.Unlock here — the caller (surface handler or
    // Phase 4 watchdog) owns the unlock. Cancel is best-effort.
}

// MarkUsed implements D-11: update LastUsed AFTER response completes.
// Called by the surface handler in a defer AFTER stream.Result() returns.
func (e *Entry) MarkUsed() {
    e.LastUsed = time.Now()
}
```

### Block 4: `agentsHandler` rendering (OBSV-02)

```go
// internal/server/agents.go — new file

// AgentsResponse is the JSON body returned by GET /health/agents (D-14).
type AgentsResponse struct {
    Pool     AgentsPool      `json:"pool"`
    Sessions []AgentSession  `json:"sessions"`
}

type AgentsPool struct {
    Size  int           `json:"size"`
    Alive int           `json:"alive"`
    Busy  int           `json:"busy"`
    Slots []AgentSlot   `json:"slots"`
}

// AgentSlot is the per-slot row shape locked by D-15.
type AgentSlot struct {
    Label              string  `json:"label"`
    Alive              bool    `json:"alive"`
    Busy               bool    `json:"busy"`
    CurrentSessionID   *string `json:"current_session_id"`  // nullable per D-15
}

// AgentSession is the per-session row shape locked by D-16.
type AgentSession struct {
    ID       string    `json:"id"`
    Alive    bool      `json:"alive"`
    Busy     bool      `json:"busy"`
    LastUsed time.Time `json:"last_used"`        // default JSON encoding → RFC 3339
    Model    *string   `json:"model"`             // nullable per D-16
}

type AgentDetailSource interface {
    // PoolDetail returns the per-slot view + summary.
    PoolDetail() AgentsPool
    // SessionsDetail returns the per-session view.
    SessionsDetail() []AgentSession
}

// agentsHandler handles GET /health/agents (auth-exempt per D-18).
func (s *Server) agentsHandler(w http.ResponseWriter, r *http.Request) {
    resp := AgentsResponse{}
    if s.agents != nil {
        resp.Pool = s.agents.PoolDetail()
        resp.Sessions = s.agents.SessionsDetail()
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    if err := json.NewEncoder(w).Encode(resp); err != nil {
        LoggerFromCtx(r.Context(), s.logger).Error("agents encode", "err", err)
    }
}
```

Wire into `NewFromConfig` next to the existing `/health` registration:
```go
// internal/server/server.go — additions near line 166-172
s.router.Get("/health", s.healthHandler)
s.router.Get("/health/agents", s.agentsHandler)  // NEW — D-14 + D-18 exempt
```

### Block 5: Real-time short-TTL reaper test (D-13)

```go
// internal/session/reaper_test.go — sketch
func TestReaper_ReapsIdleSessionInRealTime(t *testing.T) {
    // D-13: real-time test using injected TTL + TickInterval.
    // No fake clock. Total wall time: ~300ms.
    r := session.New(session.Config{
        Logger:       testutil.Logger(t),
        Factory:      fakeFactory,
        TTL:          200 * time.Millisecond,
        TickInterval: 50 * time.Millisecond,
        MaxSessions:  32,
    })
    ctx, cancel := context.WithCancel(context.Background())
    t.Cleanup(cancel)
    r.Start(ctx)
    t.Cleanup(func() { _ = r.Close() })

    e, err := r.Get(ctx, "sid-1", "/tmp")
    require.NoError(t, err)
    e.LastUsed = time.Now()  // explicit; production sets this in MarkUsed
    e.Mu.Unlock()  // simulate "no in-flight stream"

    // Wait for reaper to observe the entry as expired.
    require.Eventually(t, func() bool {
        return r.SessionCount() == 0
    }, 1*time.Second, 25*time.Millisecond, "session should be reaped")
}
```

This test is the deterministic SESS-02 acceptance — completes in <1s,
no env mutation, no `time.Now()` mocking.

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Channel-of-`*Session` pool (Node) | Channel-of-`*Slot` Pool (Phase 2) where Slot wraps PoolClient | Phase 2 | Pool stays size=1 default; Phase 5 raises env default to 4 (D-10 in Phase 5 CONTEXT). |
| `setInterval(reap, 60_000)` (Node) | `time.NewTicker(cfg.TickInterval)` + injected tick interval (D-13) | Phase 5 | Tests don't need fake clocks; production stays at 60s. |
| `entry.lastUsed = Date.now()` at request start (Node) | `entry.MarkUsed()` at response complete (D-11) | Phase 5 | Mid-stream reaps now impossible (combined with D-12 TryLock). |
| Unbounded `sessionSlots` map (Node) | `SESSION_MAX=32` env cap (D-06) | Phase 5 | OOM vector under churn closed; overflow → 503 (clear signal). |
| Slot release via promise queue (Node) | Slot release via `sync.Once` + map-delete-first + closure (Codex M-3) | Phase 2 | Phase 5 reuses this exact pattern in the registry's entry-lifecycle race. |
| Implicit ack of subprocess death (Node — `client.once('dead', ...)`) | Explicit push-based `acp.Client.Done()` channel (D-01) | Phase 5 | Per-slot watcher detects death immediately; lazy re-spawn happens BEFORE the next Acquire returns a dead slot. |

**Deprecated/outdated:**
- Phase 2's "size=1 only happy path tested" Pool. Phase 5 raises the bar
  to "POOL_SIZE > 1 + dead-slot detection" — but the underlying
  channel-of-slots design is unchanged.
- Phase 4 D-06 watchdog ALREADY HARDENED slot-release-on-disconnect — so
  the "slot survives mid-stream cancel" property holds today on
  pool-of-1; Phase 5 inherits and extends to pool-of-4.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Adding `Done() <-chan struct{}` to `acp.Client` is a non-breaking accessor that does not introduce new goroutines or change existing behavior. | Code Examples block 1 | [LOW] If the planner discovers the ping loop's "method not found" tolerance interacts with Done() differently than expected (e.g., a "method not found" was treated as live-but-degraded), the dead-slot detection latency widens — but never produces wrong behavior. Verifiable by reading `pingLoop` at `internal/acp/client.go:439-453`. |
| A2 | `SESSION_MAX=32` is a sufficient heuristic for the loop24-client + Pi SDK chat workload. | §Standard Stack alternatives | [LOW] If real chat workload routinely hits the cap, operators raise the env var; the fix is a config change, not a code change. Documentation in `docs/operating.md` should explain the heuristic. |
| A3 | `agentsHandler` should live in `internal/server/agents.go` (next to `health.go`), not in a new package. | Pattern 7 | [LOW] If the planner has a strong reason to colocate with the registry (e.g., the AgentDetailSource interface ends up living in `internal/server`), this is a file-organization preference — no functional difference. |
| A4 | Adapter handlers (ollama/openai/anthropic) each read `X-Session-Id` and branch independently — there is no shared middleware doing the routing. | Pattern 6 | [LOW] Each adapter already handles its own request decoding; adding `r.Header.Get("X-Session-Id")` as one more decoding step keeps the surface boundary clean. A shared middleware would couple the surfaces. |
| A5 | RFC 3339 from `time.Time.MarshalJSON` is acceptable for the D-16 `last_used` field — D-16's example `"2026-05-26T14:32:18Z"` is RFC 3339 / ISO 8601. | §Don't Hand-Roll | [LOW] Verified: stdlib emits RFC 3339 with sub-second precision by default. If sub-second precision is undesired, planner can MarshalJSON with `Format(time.RFC3339)` to strip subseconds. |
| A6 | Combined `AgentDetailSource` interface (both pool + registry data) vs two separate interfaces (`PoolStatsSource` already exists + new `RegistryStatsSource`). | §Standard Stack alternatives | [LOW] Both work; preference depends on whether the planner wants `agentsHandler` to take one source or two. CONTEXT.md explicitly leaves this to discretion. |
| A7 | Lazy-create double-spawn is real and must be prevented (Pitfall 4). | §Common Pitfalls | [MEDIUM] If two same-sid requests interleave at the boundary of `Registry.Get`, one kiro-cli will leak. The fix is straightforward (creation sentinel) but easy to forget. Reaper-vs-creation race is a separate concern; verified test coverage needed. |

**If this table is empty:** All claims in this research were verified or
cited — no user confirmation needed. **(Not empty — A2/A7 should be
surfaced during planning.)**

## Open Questions

1. **Should `SetModel` failures kill the session entry?**
   - What we know: `acp.Client.SetModel` can fail (e.g., invalid model
     name). D-09 says "call SetModel before Prompt if model differs."
   - What's unclear: If SetModel fails, do we tear down the entry
     (forcing a re-create) or surface a 4xx and leave the entry alive?
   - Recommendation: Surface 4xx and leave the entry alive — the
     subsequent request with a valid model will succeed. Tear-down on
     SetModel failure is a sledgehammer.

2. **Should DELETE return 200 or 204 on success?**
   - What we know: D-08 says "returns `{deleted: id}`" — implies 200
     with a body.
   - What's unclear: HTTP semantics suggest 204 (No Content) for
     DELETE, but Node parity is 200 with body.
   - Recommendation: 200 with body per Node parity (D-08 wording). The
     body is useful for clients confirming which sid was deleted.

3. **Does the registry survive a `Pool.Close` failure?**
   - What we know: `cmd/otto-gateway/main.go`'s cleanup closure calls
     `pool.Close()`. Phase 5 adds `registry.Close()` to the same
     cleanup.
   - What's unclear: If registry.Close fails (e.g., one entry's client
     panics on Close), should pool.Close still fire?
   - Recommendation: Always run pool.Close (best-effort), log the
     registry error. Matches `Pool.closeAll`'s "log first error,
     continue on subsequent errors" discipline at
     `internal/pool/pool.go:228-236`.

4. **Does `/health/agents` need rate-limiting?**
   - What we know: D-18 says it's auth-exempt. The handler reads pool
     stats + registry stats, both O(slots + sessions).
   - What's unclear: Could an operator looping `curl /health/agents`
     starve the registry's reaper of the registry mutex?
   - Recommendation: No — the handler takes the RLock briefly (snapshot
     pattern, same as Pattern 4). Concurrent reads are fine.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | `go build` / `go test` | ✓ | 1.26.3 darwin/arm64 (project requires 1.23+) | — |
| `kiro-cli` | Real-kiro integration tests (Phase 5 SESS-* end-to-end test) | ✓ | `/Users/coreyellis/.local/bin/kiro-cli` (version reportable via `kiro-cli --version`) | Tests gate on PATH lookup (existing pattern from Phase 1.1 D-17 / Phase 2 integration test); skip cleanly if absent. |
| `golangci-lint` | CI lint gate | ✗ | — | Already required by `make ci`; install per `DEVELOPERS.md`. Phase 5 does not add new lint requirements. |
| `goleak` | `internal/session/testmain_test.go` | ✓ | v1.3.0 (already pinned in `go.sum`) | — |
| `chi/v5` | `internal/server/agents.go` + `DELETE /v1/sessions/:id` mount | ✓ | v5.3.0 (already pinned in `go.sum`) | — |
| `go-arch-lint` | Architecture boundary check | ✗ (not installed in dev env) | — | Same as Phase 1; CI runs it via `make arch-lint`. No Phase 5 boundary changes — `internal/session` follows the same import rules as `internal/pool`. |
| `govulncheck` | CI vuln scan | ✗ (not installed in dev env) | — | Same as Phase 1; CI runs via `make ci`. Phase 5 adds no new deps so no new findings. |

**Missing dependencies with no fallback:** none — Phase 5 is pure Go stdlib + already-vendored deps.

**Missing dependencies with fallback:** dev-environment lint tools — fallback is "ship in CI, not in local dev loop"; matches Phase 1..Phase 4 behavior.

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` (project standard since Phase 1) |
| Config file | None — `go.mod` declares the module; per-package `testmain_test.go` files install `goleak` gates |
| Quick run command | `go test ./internal/pool/... ./internal/session/... ./internal/server/... ./internal/acp/... -race` |
| Full suite command | `make test-race` (= `go test -race ./...`) |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| POOL-01 | Pool default size is 4 (env-driven) | unit | `go test ./internal/config/... -run TestPoolSizeDefault -race` | ❌ Wave 0 (`internal/config/config_test.go` — assertion against the new default) |
| POOL-02 | Warmup completes before listener accepts (already wired in Phase 2) | unit/integration | `go test ./cmd/otto-gateway/... -run TestWarmupBeforeListen -race` | ✅ existing (`cmd/otto-gateway/main_test.go`) — re-run unchanged |
| POOL-03 | Acquire blocks on full pool; releases on slot return | unit | `go test ./internal/pool/... -run TestAcquireBlocksWhenAllBusy -race` | ❌ Wave 0 (`internal/pool/pool_test.go` extension) |
| POOL-04 | Dead slot is detected pre-Acquire and lazily re-spawned at next Acquire | unit | `go test ./internal/pool/... -run TestDeadSlotLazyRespawn -race` | ❌ Wave 0 (`internal/pool/respawn_test.go`) |
| POOL-04 | Exit-watcher goroutine exits on Pool.Close (goleak gate) | unit | `go test ./internal/pool/... -race` (testmain enforces) | ✅ existing (`internal/pool/testmain_test.go`) |
| SESS-01 | X-Session-Id routes to dedicated subprocess (not pool slot) | integration | `go test ./internal/adapter/ollama/... -run TestStatefulSessionRoutesToRegistry -race` | ❌ Wave 0 (adapter-level test with fake registry + pool) |
| SESS-01 | Two requests with same sid hit the same Entry | integration | `go test ./internal/session/... -run TestSameSidReusesEntry -race` | ❌ Wave 0 |
| SESS-02 | Idle session reaped after TTL (real-time, short TTL) | unit | `go test ./internal/session/... -run TestReaper_ReapsIdleSessionInRealTime -race` | ❌ Wave 0 (Pattern 5 sketch in Code Examples block 5) |
| SESS-02 | Active session (locked mutex) is NOT reaped | unit | `go test ./internal/session/... -run TestReaper_SkipsInFlightSession -race` | ❌ Wave 0 |
| SESS-03 | DELETE /v1/sessions/:id returns `{deleted: id}` on success | integration | `go test ./internal/server/... -run TestDeleteSession -race` | ❌ Wave 0 (`internal/server/sessions_test.go`) |
| SESS-03 | DELETE unknown sid returns 404 | integration | `go test ./internal/server/... -run TestDeleteSession_Unknown -race` | ❌ Wave 0 |
| SESS-03 | DELETE cancels in-flight stream | integration | `go test ./internal/session/... -run TestDelete_CancelsInFlight -race` | ❌ Wave 0 |
| OBSV-02 | GET /health/agents returns D-14/D-15/D-16 shape with non-empty pool+registry | integration | `go test ./internal/server/... -run TestHealthAgents_Shape -race` | ❌ Wave 0 (`internal/server/agents_test.go`) |
| OBSV-02 | /health/agents is auth-exempt | integration | `go test ./internal/server/... -run TestHealthAgents_AuthExempt -race` | ❌ Wave 0 |
| OBSV-02 | Per-slot `current_session_id` populated from `Pool.sessionSlots` | unit | `go test ./internal/pool/... -run TestPoolDetail_CurrentSessionID -race` | ❌ Wave 0 |
| All | No goroutine leaks across pool + session + acp + engine | gate | testmain enforces | ✅ existing in pool/engine/acp; ❌ Wave 0 for session |

### Sampling Rate

- **Per task commit:** `go test ./internal/<touched-pkg>/... -race` (sub-second to single-digit-seconds typical)
- **Per wave merge:** `make test-race` (full suite, ~30-60s typical)
- **Phase gate:** `make ci` (lint + race-tests + arch-lint + govulncheck) green before `/gsd-verify-work`

### Wave 0 Gaps

- [ ] `internal/session/testmain_test.go` — installs `goleak.VerifyTestMain` for the new package. Pattern lifted verbatim from `internal/pool/testmain_test.go`.
- [ ] `internal/session/registry_test.go` — base black/whitebox tests for `Get` / `Delete` / lifecycle.
- [ ] `internal/session/reaper_test.go` — D-13 real-time short-TTL test (Code Examples block 5).
- [ ] `internal/server/agents_test.go` — `/health/agents` wire-shape assertions.
- [ ] `internal/server/sessions_test.go` — `DELETE /v1/sessions/:id` handler tests.
- [ ] `internal/pool/respawn_test.go` — dead-slot detection + lazy re-spawn coverage (uses the existing `ClientFactory` seam from `internal/pool/config.go:60-62` — inject a factory that returns dying clients).
- [ ] `internal/pool/exit_watcher_test.go` — goroutine teardown gates.
- [ ] Test helper to build a Registry with a fake `ClientFactory` (mirrors `internal/pool/export_test.go` pattern — exposes test-only fields).

## Security Domain

> `security_enforcement` is not explicitly set in `.planning/config.json`,
> treated as enabled per researcher contract. Phase 5 surface is small;
> most security work was front-loaded into Phase 1 / Phase 2.

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | `auth.Bearer` middleware already gates the `/v1` prefix where `DELETE /v1/sessions/:id` lives (Phase 2). Phase 5 inherits unchanged. |
| V3 Session Management | yes | `X-Session-Id` is client-supplied, NOT a gateway secret. D-17 explicitly does not redact ids in `/health/agents` (matching `/health`'s pool occupancy exposure). Auth gate on the `/v1` prefix is the primary control. |
| V4 Access Control | yes | `/health/agents` is auth-exempt (D-18); `DELETE /v1/sessions/:id` is auth-required (via `/v1` prefix mount). Operator-only DELETE; LB-probe-friendly health check. |
| V5 Input Validation | yes | `X-Session-Id` header content goes straight to a map key + slot label — no SQL, no command injection surface. `chi.URLParam` is the standard `:id` extractor. Empty/whitespace ids should 400. |
| V6 Cryptography | no | No new crypto in Phase 5. |
| V7 Error Handling | yes | DELETE 404 leaks "session not found" semantically but not session ids (caller already supplied the id). Pattern is identical to `/api/show` for unknown models. |
| V11 Business Logic | yes | `SESSION_MAX` cap (D-06) protects against resource-exhaustion DOS via unbounded session creation. The cap is a business-logic control, not a network-level rate limit. |

### Known Threat Patterns for Phase 5 (Go HTTP + subprocess)

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Subprocess fork bomb via `X-Session-Id` flood | DoS | `SESSION_MAX=32` env cap (D-06) returns 503 on overflow rather than spawning unboundedly. |
| Same-sid lazy-create races spawning duplicate subprocesses | DoS (leak) | Pitfall 4 prevention — creation sentinel under registry mutex; test coverage in Wave 0. |
| Header smuggling via `X-Session-Id` with control characters | Tampering | `chi.URLParam` and `r.Header.Get` already normalize per Go's `net/http` parsing — but the value flows into `slog` and `/health/agents` JSON encoding. Encoding/JSON escapes control chars; `slog` JSON handler does the same. Both safe. Validate in the registry: reject empty after `TrimSpace`. |
| DELETE flood targeting in-flight sessions | DoS via cancel-storm | The DELETE handler is auth-protected. Repeated unauthorized DELETEs 401 before reaching the registry. Authorized DELETEs are operator actions — accept the cost. |
| `kiro-cli` death used as an oracle for session existence | Information Disclosure | DELETE and `/health/agents` both expose presence/absence of sids — but the requesting client provided the id, so no escalation. Acceptable per D-17. |
| Resource exhaustion via slow `acp.Client.Close()` during shutdown | DoS (graceful-shutdown bypass) | Pitfall 5 — bounded by tick interval + worst-case close-all time. Document the deadline in `docs/operating.md`. |

## Sources

### Primary (HIGH confidence)

- `internal/pool/pool.go` — current Pool impl, including `Warmup` (lines
  96-140), `initSlot` (145-161), `Stats` (180-198), `NewSession`
  (263-284), `releaseSlotForSession` (398-408), poolStreamWrapper
  (415-464). [Read directly during research]
- `internal/pool/config.go` — `Config` + `applyDefaults`, `PoolClient`
  interface, `ClientFactory` seam. [Read directly]
- `internal/pool/stats.go` — `Stats` struct as the model for `SlotDetail`.
  [Read directly]
- `internal/acp/client.go` — `Client` struct (line 239), `clientCtx`
  (line 246), `pingLoop` (433-453), `Close` (919-968), `Cancel` (801-806).
  Confirms Done() can be a one-line accessor over `clientCtx.Done()`.
  [Read directly]
- `internal/acp/stream.go` — `Stream` with `Chunks` field + 64-slot
  buffer + `sync.Once`-guarded close. Confirms the existing race-safe
  close discipline transfers to the registry. [Read directly]
- `internal/server/server.go` — `NewFromConfig` (lines 150-212),
  `PoolStatsSource` (25-31), `SurfaceMount` (46-52), exempt-route list
  (165-172). Pattern for the new `RegistryStatsSource` and
  `/health/agents` exempt route. [Read directly]
- `internal/server/health.go` — `HealthResponse` (lines 11-24),
  `PoolStats` (27-35), `healthHandler` (57-75). Template for
  `agentsHandler`. [Read directly]
- `internal/engine/engine.go` — `ACPClient` (34-50), `Stream` (53-64),
  `Engine.Run` Phase 4 watchdog (196-206). Confirms surface handlers
  route both stateless and stateful through one path. [Read directly]
- `internal/engine/acp_adapter.go` — `acpStreamShim` (66-90). The
  exact shim Pattern 3's `Entry.Prompt` reuses. [Read directly]
- `internal/config/config.go` — `getEnvInt` / `getEnvDuration` helpers
  + `Config` struct + `Load` (381-456). `SESSION_TTL_MS` /
  `SESSION_MAX` slot in here. [Read directly]
- `cmd/otto-gateway/main.go` — `newApp` wiring (117-302) +
  cleanup-closure (122-128). Phase 5 extends both. [Read directly]
- `.planning/phases/04-streaming/04-CONTEXT.md` — D-06 watchdog
  contract; "Deferred Ideas: Full slot-release-on-cancel semantics
  + POOL_SIZE > 1 — Phase 5" (line 303). [Read directly]
- `.planning/phases/02-ollama-end-to-end/02-CONTEXT.md` — Pool's
  `engine.ACPClient` contract (D-06/D-07) + `PoolStatsSource` model.
  [Read directly]
- `.planning/REQUIREMENTS.md` — POOL-01..04 (lines 57-60), SESS-01..03
  (64-66), OBSV-02 (91). [Read directly]
- `.planning/ROADMAP.md` Phase 5 (lines 243-256) — SC1..SC5
  authoritative. [Read directly]
- `docs/reference/acp_server_node_reference.md` — `ACPPool` (lines
  120-128), `SessionRegistry` (130-135), Configuration (236-265).
  Node parity ground truth. [Read directly]
- `docs/reference/acp_wire_shapes.md` — `session/cancel` (lines
  204-218), `session/new` (92-129). The shapes the DELETE handler
  and registry reuse. [Read directly]
- `acp-ollama-server.js` (Node source at
  `/Volumes/CMETECH/repos/.../acp_server/acp-ollama-server.js`) —
  `ACPPool` (lines 330-373), `SessionRegistry` (375-409), reaper at
  line 378 / line 402-407. **Confirms `setInterval(..., 60_000)` is
  the Node reaper cadence and `entry.lastUsed = Date.now()` is set
  at acquire (line 394) — Phase 5 D-11 deviates intentionally.** [Grep
  + read directly]

### Secondary (MEDIUM confidence)

- pkg.go.dev/context — `context.AfterFunc` contract for the watchdog
  stop()-vs-fire race documentation. [Fetched 2026-05-26 via WebFetch]
- pkg.go.dev/go.uber.org/goleak — v1.3.0 confirmation. [Fetched
  2026-05-26 via WebFetch]
- pkg.go.dev/sync — `sync.Mutex.TryLock` introduced in Go 1.18.
  [Verified against Go 1.18 release notes by reference]
- Go stdlib `time.Time.MarshalJSON` documentation — RFC 3339 default.
  [Verified by reference to stdlib godoc]

### Tertiary (LOW confidence)

- None — every claim in this research is traceable to a primary or
  secondary source.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — All deps already pinned in `go.sum`; no new
  installs. Stdlib primitives (sync.Mutex.TryLock, context.AfterFunc,
  time.Ticker) are battle-tested.
- Architecture: HIGH — Every pattern is a direct re-use of an existing
  Phase 2 / Phase 4 pattern (slot-release race, watchdog, exempt-route
  list, PoolStatsSource). The one genuinely new mechanic (acp.Client.Done)
  is a one-line accessor over a verified existing field.
- Pitfalls: HIGH — Pitfalls 1, 5, 6 are direct extensions of known race
  classes Phase 2 / Phase 4 already documented. Pitfalls 2, 3, 4 are
  novel-to-Phase-5 and have concrete mitigation paths.
- Node parity: HIGH — Reference Node code grep'd and read directly;
  every D-* in CONTEXT.md cross-referenced to a Node code line.
- Security: MEDIUM — ASVS mapping is straightforward but Phase 5 lacks a
  formal threat model. Sub-spawn DoS via SESSION_MAX overflow is
  mitigated; same-sid race is the most subtle remaining concern
  (Pitfall 4).

**Research date:** 2026-05-26
**Valid until:** 2026-06-25 (30 days — stable stack, no fast-moving deps)
