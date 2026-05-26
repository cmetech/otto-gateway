# Phase 5: Pool + Stateful Sessions - Context

**Gathered:** 2026-05-26
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 5 delivers three load-bearing capabilities:

1. **A real warm pool.** Today `POOL_SIZE` defaults to 1 and the pool's
   happy path is the only path exercised. Phase 5 raises the default to
   `POOL_SIZE=4` (Node parity) and **adds dead-slot detection with lazy
   re-spawn** (POOL-04). The slot-release-on-cancel hardening explicitly
   deferred from Phase 4 lands here.

2. **Stateful sessions via `X-Session-Id`.** A new
   `internal/session/registry.go` package owns dedicated `acp.Client`
   subprocesses **outside the warm pool**, keyed by client-supplied
   `X-Session-Id`. A 60s reaper closes sessions idle longer than
   `SESSION_TTL_MS` (default 30 min); `DELETE /v1/sessions/:id` tears one
   down on demand.

3. **`GET /health/agents` detail view.** A new endpoint exposes per-slot
   and per-session detail (`alive`, `busy`, `label`, `last_used`, etc.)
   alongside the existing `/health` summary — so operators can see which
   slot is serving which session and which sessions are about to be
   reaped.

**Requirements covered:** POOL-01, POOL-02, POOL-03, POOL-04, SESS-01,
SESS-02, SESS-03, OBSV-02.

**Explicitly NOT in Phase 5:**

- Tool-call rendering (`coerceToolCall`, per-surface tool_calls shape) —
  Phase 6.
- Embeddings — Phase 7.
- Hook chain implementations (Auth/RateLimit/Audit hooks) — Phase 8. The
  empty Pre/Post seam carries forward unchanged; sessions and pool route
  through `engine.Run` so any future hook applies uniformly.
- Cross-compile + CI matrix — Phase 9.
- Real-token counts in `/health/agents` session rows (kiro-cli doesn't
  report them; reuse Phase 2's `estimateTokens` only inside response
  bodies).

</domain>

<decisions>
## Implementation Decisions

### Dead-slot detection & lazy re-spawn (POOL-04)

- **D-01: Push-based exit detection.** Each slot has a per-slot
  exit-watcher goroutine that subscribes to the `acp.Client`'s existing
  60s heartbeat / subprocess-exit signal (`internal/acp/client.go` —
  ping loop already kills the subprocess on a failed ping that isn't
  "method not found"). When the client signals death, the watcher
  marks the slot dead **immediately** — detection is pre-Acquire, not
  lazy on the next request.
  - **Goroutine accounting:** the watcher MUST exit on `Pool.Close` /
    successful re-spawn; `goleak` is the gate (same discipline as the
    Phase 4 D-06 watchdog).

- **D-02: Lazy synchronous re-spawn at Acquire.** When `Acquire` (i.e.,
  `Pool.NewSession` and any future Acquire-equivalent) pops a slot
  marked dead, it spawns a fresh `acp.Client` in-line **before** handing
  the slot to the caller. Other Acquires keep flowing through healthy
  slots. Matches the Node reference (`acp_server_node_reference.md` §
  `ACPPool`: "Dead slots are re-spawned lazily inside `acquire()` /
  `release()`"). One caller pays the warmup latency; no always-on
  supervisor goroutine.

- **D-03: Spawn-failure surfaces as 503; pool shrinks.** If the lazy
  re-spawn fails (kiro-cli won't start), `Pool.NewSession` returns a
  wrapped typed error; adapters render `503 Service Unavailable`. The
  dead slot is **dropped from `p.all`** — the pool's effective size
  decreases. This is intentional: silent capacity loss is worse than
  loud single-request failure, and an operator can see the shrink in
  `/health/agents` (D-12).

### Session-to-subprocess ownership (SESS-01)

- **D-04: Separate `SessionRegistry`, not a pool slot.** Stateful
  sessions live in a new `internal/session/registry.go` package, with
  its own map keyed by `sid` and its own spawned `acp.Client`s —
  **entirely outside the warm pool**. Pool size stays at `POOL_SIZE`;
  sessions add headroom on top. Exact Node parity (`SessionRegistry`
  in `acp_server_node_reference.md` §`SessionRegistry`: "owns its own
  dedicated `ACPSession` (not from the pool)"). Reuses the same
  spawn ergonomics as `Pool.initSlot` via the shared `acp.Config`
  shape — but is a distinct struct, not a pool method.

- **D-05: Lazy create on first request with new `X-Session-Id`.** No
  explicit `POST /v1/sessions` endpoint. The first `/api/chat`,
  `/v1/chat/completions`, or `/v1/messages` request with an
  unrecognized `X-Session-Id` spawns the dedicated subprocess, calls
  `Initialize` + `NewSession`, stores the entry, then proceeds. Matches
  the Node reference and is zero-ceremony for Pi SDK / LangFlow /
  `@anthropic-ai/sdk`.

- **D-06: Env-driven `SESSION_MAX=32` cap; overflow → 503.** Each
  session is a kiro-cli subprocess (RAM + FDs + open pipes); an
  unbounded `sessionSlots` map is an OOM vector under churn. `SESSION_MAX`
  is a new env var (defaults to 32). Lazy-create that would exceed the
  cap returns a `pool: session-max exceeded` error → 503. Honors the
  Node parity preference while closing the unbounded-growth hole.
  - **Naming:** `SESSION_MAX` is new (not in `acp_server_node_reference.md`);
    document it in the Node-parity env table during planning.

- **D-07: Per-session mutex serializes concurrent requests on the same
  `sid`.** `session/prompt` is inherently single-conversation — two
  parallel prompts corrupt the message history. Each `SessionEntry`
  carries a mutex (or buffered chan of size 1) that the surface
  handler acquires before `Prompt`. Second concurrent request blocks
  until the first stream completes (or its ctx cancels). No 409
  surface — real chat clients double-fire on reconnect and the
  block-and-wait behavior matches what they expect.

- **D-08: `DELETE /v1/sessions/:id` cancels in-flight, closes, returns
  `{deleted: id}`.** Mirrors the Phase 4 disconnect-cancel watchdog —
  the DELETE handler calls `entry.Client.Cancel(sid)` to abort any
  in-flight prompt, then `entry.Client.Close()` to tear down the
  subprocess, then removes the map entry. Status `200 {deleted: "<id>"}`
  (Node parity). Unknown sid → `404` (silent removal is wrong; the
  caller deserves to know).
  - **Reuses ACP path:** `acp.Client.Cancel` and `acp.Client.Close` are
    existing (`internal/acp/client.go`); no new ACP work.

- **D-09: `SetModel` is per-request, only on diff.** Surface handlers
  pass the request's `model` through to the session. The session entry
  caches the last-set model; if the new model differs, call
  `SetModel(ctx, sid, model)` **before** `Prompt`. Matches the
  stateless engine's behavior (Phase 2 D-09 routes model through every
  Prompt) and lets users switch model mid-conversation without
  surprising 409s.

### Reaper mechanics (SESS-02)

- **D-10: 60s ticker, `SESSION_TTL_MS` default 1,800,000 (30 min) —
  Node parity.** A single reaper goroutine on `registry.Start(ctx)`
  wakes every `TickInterval` (constructor param; default 60s) and walks
  the map. Both env names match the brief's backward-compat contract
  exactly (`docs/briefs/go_port_brief.md`).

- **D-11: `last_used` updated at response complete.** TTL measures
  "time since session was last actively serving traffic." The session
  entry's `LastUsed` field is touched **after** the stream's
  `Result()` returns (or the stream-write loop exits, whichever path
  the surface uses). A long-running streamed response (e.g., 5 min of
  reasoning) cannot be reaped mid-stream because `last_used` doesn't
  advance until the stream closes — combined with D-12's "skip
  in-flight" rule this is two layers of protection. Also prevents a
  chatty no-op client from rapid-fire extending TTL with empty
  requests, because request-start no longer counts.

- **D-12: Reaper takes the per-entry mutex; skips in-flight.** The
  reaper attempts a `TryLock` on each entry's per-session mutex
  (the same one from D-07). If locked (= stream in flight), skip this
  entry — the **next** tick (within 60s) will retry once the stream
  closes and `last_used` updates. If `TryLock` succeeds and
  `now - last_used > TTL`, defensively call `Cancel(sid)` then
  `Close()`, then delete the map entry. No race with serving traffic;
  no surprise mid-stream truncations.

- **D-13: `ttl + tickInterval` are constructor params, not globals.**
  `session.New(Config{TTL, TickInterval, MaxSessions, Logger, ...})`.
  Production wires from env via `internal/config` (existing pattern).
  Tests pass `TTL: 200*time.Millisecond, TickInterval: 50*time.Millisecond`
  for a real-time SESS-02 test that completes in <1s without env
  mutation or a fake-clock dependency. Matches the established
  no-globals pattern in `internal/pool/config.go`.

### `/health/agents` shape (OBSV-02)

- **D-14: New endpoint `GET /health/agents`, separate from `/health`.**
  `/health` stays as the cheap, flat, LB-friendly summary
  (`internal/server/health.go` — `HealthResponse` keeps current shape).
  `/health/agents` is a new handler with the verbose per-slot +
  per-session detail. Matches ROADMAP success criterion 5 verbatim.

- **D-15: Per-slot row shape:**
  ```json
  { "label": "slot-0", "alive": true, "busy": false,
    "current_session_id": null }
  ```
  `current_session_id` is nullable; populated from `Pool.sessionSlots`
  (already exists) so it costs one map lookup per row. Helps operators
  debug "which slot is serving sid X" without grepping logs.

- **D-16: Per-session row shape:**
  ```json
  { "id": "sess-abc123", "alive": true, "busy": false,
    "last_used": "2026-05-26T14:32:18Z", "model": "claude-sonnet-4-7" }
  ```
  `model` is nullable — present once `SetModel` has been called on the
  session. `last_used` is RFC 3339 / ISO 8601 (Go's default
  `time.Time.MarshalJSON`). `busy` mirrors the slot field so operators
  can quickly see which sessions are mid-stream.

- **D-17: Full session ids verbatim — no redaction.** `X-Session-Id`
  is client-supplied, not a gateway secret. Truncating would just
  hinder operator-DX (matching truncated IDs back to client logs).
  Behind `AUTH_TOKEN` the endpoint is auth-gated by D-18; without it,
  the same exposure applies as `/health`'s pool stats.

- **D-18: `/health/agents` is auth-exempt — same as `/health`.** Added
  to the same exempt-routes list in `internal/server/server.go`
  (Phase 2 OBSV-01: exempts `/`, `/api/version`, `/health`). LB probes
  and operator-curl-from-jumphost continue to work without token
  management. The session-id exposure is consistent with `/health`
  exposing pool occupancy.

### Claude's Discretion

The planner/researcher have latitude on:

- Whether the per-slot exit-watcher (D-01) lives inside `acp.Client`
  itself (as a `Done()` channel) or as a new goroutine in
  `internal/pool/` watching a client-exposed signal — provided
  `goleak` passes. The contract is "death is observable push-side";
  the mechanism is open.
- Concrete struct layout of `internal/session/registry.go` (Entry
  vs Session naming, where the mutex lives, whether
  `Acquire`/`Release` methods exist or surface handlers call
  `Prompt` directly on the entry). Constraint: tests must be able to
  construct a registry with injected `TTL` + `TickInterval` (D-13)
  and observe reap events deterministically.
- How `/health/agents` discovers the registry — passed into
  `server.NewFromConfig` as a new `RegistryStatsSource` interface
  (mirror of `PoolStatsSource` from Phase 2), or via a single
  combined `AgentDetailSource`. Either is fine; consistency with the
  Phase 2 pattern is the bar.
- Whether `Pool.Close` waits for in-flight registry sessions to drain
  or fires them in parallel — provided `goleak` passes and the
  shutdown completes in bounded time.
- The exact wire shape of `/health/agents` (object with
  `{pool: {...}, sessions: [...]}` vs flat `{slots: [...], sessions: [...]}`),
  as long as it carries the D-15 + D-16 fields.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project context (must-read)

- `.planning/PROJECT.md` — Core value ("one place to enforce policy"),
  Validated REQ-POOL-01 ("baseline; Phase 5 raises `POOL_SIZE` default"),
  Key Decisions table.
- `.planning/REQUIREMENTS.md` — POOL-01..04, SESS-01..03, OBSV-02 are
  the phase's requirement set. Env-var contract (`POOL_SIZE`,
  `SESSION_TTL_MS`, `KIRO_*`) is locked by Phase-Map and Backward Compat
  rules.
- `.planning/ROADMAP.md` §"Phase 5: Pool + Stateful Sessions" — phase
  goal + 5 success criteria. SC5 is the authoritative `/health/agents`
  contract.
- `.planning/phases/04-streaming/04-CONTEXT.md` — **D-06** (engine-owned
  watchdog fires `session/cancel` on disconnect — Phase 5 must keep
  that contract while hardening slot release in a multi-slot world),
  **Deferred Ideas: "Full slot-release-on-cancel semantics +
  POOL_SIZE > 1 — Phase 5"** (this is what D-01..D-03 cash).
- `.planning/phases/02-ollama-end-to-end/02-CONTEXT.md` — Pool's
  current `engine.ACPClient` contract (D-07/D-08), `Pool.Stats` shape,
  the existing `PoolStatsSource` plumbing through `server.NewFromConfig`
  (the model for D-14's `RegistryStatsSource`).

### Behavioral parity (must-read for SessionRegistry + Reaper)

- `docs/reference/acp_server_node_reference.md` §`ACPPool` (lines
  120-128) — "Dead slots are re-spawned lazily inside `acquire()` /
  `release()`" is the ground truth for D-02.
- `docs/reference/acp_server_node_reference.md` §`SessionRegistry`
  (lines 130-135) — "owns its own dedicated `ACPSession` (not from the
  pool)", "Reaped every 60s; entries older than `SESSION_TTL_MS` are
  closed", `DELETE /v1/sessions/:id` semantics — ground truth for
  D-04..D-12.
- `docs/reference/acp_server_node_reference.md` §"Configuration"
  (lines 236-265) — env-var contract (`POOL_SIZE=4`,
  `SESSION_TTL_MS=1800000`, `PING_INTERVAL=60000`), `X-Session-Id`
  header semantics.
- `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server` —
  the Node source itself; check `acp-ollama-server.js` for the
  reaper interval (`setInterval(...60000)`) and `acquire()` /
  `release()` dead-slot handling.

### ACP wire shapes (must-read for D-08 DELETE + D-12 reap)

- `docs/reference/acp_wire_shapes.md` — `session/cancel` notification
  shape (no-response, best-effort); `session/new` response shape (sid
  + models).
- `internal/acp/client.go` — existing `Initialize`, `NewSession`,
  `SetModel`, `Prompt`, `Cancel`, `Close`. **The 60s ping loop**
  (lines around `runPingLoop`) is where D-01's exit signal is
  exposed; the planner may add a `Done() <-chan struct{}` or similar.

### Existing pool internals (must-read for D-01..D-03)

- `internal/pool/pool.go` — current Pool implementation. `Warmup`
  (lines 96-140), `initSlot` (145-161), `Stats` (180-198), the
  `engine.ACPClient` surface (`NewSession` 263-284, `SetModel`
  289-300, `Prompt` 310-369, `Cancel` 379-392) — **all of these
  need to integrate dead-slot detection without breaking the Phase 2
  slot-release-on-cancel guarantees (Codex M-3 race resolution).**
- `internal/pool/config.go` — current `Config` struct + `applyDefaults`
  (currently `Size=1` default; **Phase 5 D-10 raises the env default
  to 4 via `internal/config/config.go`** while keeping the package
  default at 1 for tests that don't override).
- `internal/pool/stats.go` — current `Stats` struct (Size, Alive,
  Busy). `/health/agents` rendering may extend this or add a
  parallel `DetailStats` shape.

### Observability path (must-read for D-14..D-18)

- `internal/server/health.go` — current `HealthResponse`, `PoolStats`,
  `SessionStats`, `EmbeddingStats`. `healthHandler` (lines 57-75) is
  the pattern `agentsHandler` follows.
- `internal/server/server.go` — `NewFromConfig` wiring, exempt-route
  list, `PoolStatsSource` interface (the model for the new
  `RegistryStatsSource` / `AgentDetailSource`).

### Engine integration (must-read)

- `internal/engine/engine.go` — `Engine.Run` and the `ACPClient`
  interface (line 47 area). The session registry must satisfy a
  similar shape so surface handlers can route stateful requests
  through `engine.Run` and get the Phase 4 watchdog for free.
- `internal/engine/acp_adapter.go` — current acp adapter (single-slot
  engine.Run path); Phase 5 may add a registry-adapter alongside
  this.

### Pi SDK / Anthropic SDK headers (must-read for `X-Session-Id` plumbing)

- `docs/briefs/go_port_brief.md` (lines 160-170) — header semantics:
  `X-Session-Id`, `X-Working-Dir`, `X-Request-Id`. `X-Working-Dir`
  already handled (REQ-CWD-01); `X-Session-Id` is the Phase 5
  addition.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- `internal/pool/pool.go` — `Pool.NewSession`/`SetModel`/`Prompt`/`Cancel`
  already implement `engine.ACPClient` (Phase 2 D-07). Phase 5 layers
  dead-slot detection **into the Acquire path** without rewriting the
  Codex M-3 slot-release race resolution. The `poolStreamWrapper` +
  `sync.Once` + map-delete-first pattern (lines 415-464) is the model
  for how `SessionRegistry` coordinates Cancel-vs-Reaper-vs-Result
  releases on a session entry.
- `internal/pool/config.go` — `Config` shape with `applyDefaults` is
  the model for `internal/session/config.go` (TTL, TickInterval,
  MaxSessions, Factory).
- `internal/acp/client.go` — `Initialize`, `NewSession`, `SetModel`,
  `Prompt`, `Cancel`, `Close` are the full set the registry needs.
  The 60s ping loop already kills the subprocess on a real ping
  failure (not "method not found") — D-01's exit signal taps that.
- `internal/server/health.go` — `healthHandler` is the template for
  `agentsHandler` (D-14). `PoolStatsSource` interface in
  `server.go` is the template for the new agent-detail source.
- `internal/config/config.go` — `getEnvInt` + `getEnvDuration` helpers
  already exist (POOL_SIZE, KIRO_CWD, KIRO_CMD). `SESSION_TTL_MS`
  and `SESSION_MAX` slot into the same pattern.
- `internal/engine/engine.go` — `Engine.Run` accepts any
  `ACPClient` — the registry's session-entry can implement the same
  interface, letting the surface handlers route stateful requests
  through `engine.Run` unchanged (and get the Phase 4 D-06 watchdog
  for free).

### Established Patterns

- **No globals; constructor takes a Config struct.** `internal/pool`,
  `internal/server`, `internal/engine` all use this pattern.
  `internal/session` follows.
- **`engine.ACPClient` is the boundary.** Anything that owns a
  kiro-cli subprocess satisfies this interface. Surface handlers
  switch between `Pool` (stateless) and `SessionRegistry.Entry`
  (stateful) at the surface level based on `X-Session-Id` presence
  — neither implementation leaks past the engine boundary.
- **`goleak` gate on package-level tests.** `internal/pool/testmain_test.go`
  and `internal/engine/testmain_test.go` enforce this. `internal/session`
  ships the same gate from day one — the reaper goroutine and per-slot
  exit-watcher (D-01) must tear down cleanly on every terminal path.
- **Codex M-3 slot-release pattern** — `sync.Once` + map-delete-first
  + closure-based release. Phase 5 reuses this exactly inside the
  session-entry race resolution between Reaper / DELETE / ctx-cancel.
- **Auth-exempt route list lives in `server.NewFromConfig`.** D-18
  adds `/health/agents` to it.
- **Env-name backward-compat is locked.** `POOL_SIZE`, `SESSION_TTL_MS`
  are reused verbatim from Node (`go_port_brief.md` constraint).
  `SESSION_MAX` is new (D-06) — must be documented as a Phase 5
  addition, not a renamed Node var.

### Integration Points

- `internal/pool/pool.go` — `initSlot` gains an exit-watcher spawn;
  `Acquire`-equivalent (currently inline `<-p.slots`) gains dead-slot
  detection + lazy re-spawn (D-01..D-03).
- `internal/session/registry.go` — **new package**. Owns
  `Registry`, `Config`, `Entry`. Reaper goroutine started by
  `Registry.Start(ctx)`. Tests via injected `TTL` + `TickInterval`.
- `internal/server/server.go` — new `agentsHandler`; new
  `RegistryStatsSource` interface; `NewFromConfig` accepts a
  registry; exempt-route list grows.
- `internal/server/health.go` — `HealthResponse.Sessions.Active`
  populated from `registry.Stats()` (D-12 in Phase 1 already
  scaffolded the field). New `AgentsResponse` type for D-15/D-16.
- `cmd/otto-gateway/main.go` — `newApp` wires the registry alongside
  the pool, starts the reaper, and adds shutdown ordering (drain
  in-flight registry sessions before pool Close).
- Surface handlers — `internal/adapter/{ollama,openai,anthropic}/handlers.go`
  each read `X-Session-Id` and route to `engine.Run(ctx, registry.Entry(sid))`
  vs `engine.Run(ctx, pool)`. Single switch point per surface;
  identical canonical engine downstream.

</code_context>

<specifics>
## Specific Ideas

- **Exit-watcher signal must come from `acp.Client`.** The watcher
  cannot poll the subprocess's `os.Process` directly because the
  ping loop already owns that — racing with it would deadlock on
  the same subprocess teardown. The planner should add a
  `Done() <-chan struct{}` (or equivalent) to `acp.Client` that
  closes when the ping loop kills the subprocess.

- **`Pool.NewSession`'s ctx-aware acquire (lines 263-271) must survive
  the dead-slot path.** The current code does
  `select { case slot = <-p.slots; case <-ctx.Done(): ... }`. The
  new path that detects a dead slot and re-spawns inline MUST keep
  honoring ctx so a ctx-cancelled caller doesn't block on a slow
  kiro-cli spawn. Re-spawn happens in a helper that takes ctx.

- **The "skip in-flight" reaper rule (D-12) means TTL is a lower bound,
  not an upper bound.** A session that's continuously serving traffic
  will never be reaped, no matter how long it's been alive. This is
  correct (active sessions are not idle), but should be called out
  in the planner's verification work so SC4 ("idle session reaped
  after SESSION_TTL_MS") is tested with a truly idle session.

- **`SESSION_MAX=32` is a soft heuristic.** Pick the default during
  planning by considering: typical loop24-client + Pi-SDK chat session
  cardinality (low), kiro-cli RSS per session (TBD — research),
  default ulimit -n on Linux (1024 default; each session is ~2-3 FDs
  for stdin/stdout/stderr pipes + sockets). 32 is well within ulimit
  but high enough that real chat usage doesn't hit it.

- **`/health/agents` JSON wire shape — explicitly version-tolerant.**
  Surface adapters are wire-locked by external SDKs, but
  `/health/agents` is an operator-facing tool. Adding fields is fine;
  removing them is a breaking change. Plan/researcher should pick a
  shape that's additive-friendly (object-keyed, not positional).

- **No new `engine.ACPClient` method.** Resist the urge to add a
  `Detail()` method to the interface. The agent-detail source is a
  **separate** interface that `Pool` and `SessionRegistry` happen to
  satisfy — keep the engine interface narrow.

</specifics>

<deferred>
## Deferred Ideas

- **PID, started_at, error_count in `/health/agents`** — operator-
  friendly but require new bookkeeping in `acp.Client`. Add as
  follow-up when operational needs surface (Phase 9 distribution work
  may want them).
- **Real token counts in session rows** — kiro-cli doesn't report
  tokens; `estimateTokens` already used inside response bodies.
  Phase 7+ (embeddings) may revisit if a tokenizer is available
  in-process.
- **Per-session metrics: message_count, total_tokens, last_model
  history** — analytics-flavored, not load-bearing. Defer until a
  real observability need shows up.
- **`HEALTH_AGENTS_AUTH=required` knob** — `/health/agents` is
  auth-exempt (D-18) by default. If an operator deployment wants it
  gated, reverse-proxy in front. Avoid adding a config knob for an
  undecided question.
- **POST `/v1/sessions` explicit-create endpoint** — Node parity is
  lazy-create (D-05). If a client ever shows up that needs a
  pre-create handshake (e.g., to set a model before sending a
  prompt), add it as an additive endpoint — don't replace lazy-create.
- **Adaptive reaper cadence (ticker = TTL/4)** — tests use explicit
  `TickInterval` constructor param (D-13); production stays at 60s.
  If short-TTL prod use cases emerge, revisit.
- **Hash/truncate `X-Session-Id` in logs and `/health/agents`** —
  defer until a real leak incident or compliance ask shows up
  (D-17).
- **Cross-session model affinity** — none. Each session is
  independent; `SetModel` per-session per-request (D-09). If a
  global "default model per surface" knob shows up, it's a different
  phase.

</deferred>

---

*Phase: 5-Pool-Stateful-Sessions*
*Context gathered: 2026-05-26*
