# Phase 5: Pool + Stateful Sessions - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-26
**Phase:** 5-pool-stateful-sessions
**Areas discussed:** Dead-slot detection & lazy re-spawn (POOL-04); Session-to-subprocess ownership model (SESS-01); Reaper cadence + last_used timestamp (SESS-02); /health/agents shape & endpoint placement (OBSV-02)

---

## Dead-slot detection & lazy re-spawn (POOL-04)

### Q1: How should a dead slot be detected?

| Option | Description | Selected |
|--------|-------------|----------|
| Subscribe to acp.Client ping/exit signal | Each slot has a goroutine watching the client's existing 60s heartbeat / subprocess exit channel. When the client signals death, the slot is marked dead immediately — detection is push-based and pre-Acquire. | ✓ |
| Lazy on next Acquire — cheap liveness check | Acquire pulls a slot then sends a synchronous Ping (or no-op JSON-RPC) before handing it out. Simpler (no extra goroutine) but adds latency per request and only catches death at acquire time. | |
| Mark dead on Prompt/NewSession error | Slot is only marked dead reactively when a real request fails. Zero overhead in the happy path, but the failing request takes the hit — a doomed slot can serve N broken requests before being noticed under high traffic. | |

**User's choice:** Push-based via existing acp.Client ping/exit signal.

### Q2: When a dead slot is detected, how should the replacement spawn?

| Option | Description | Selected |
|--------|-------------|----------|
| Lazy on Acquire: pop dead slot, spawn synchronously, hand to caller | Acquire that pulls a slot marked dead spawns a fresh subprocess in-line before returning. Other Acquires keep flowing through healthy slots. Matches Node parity; one caller pays the warmup cost; no background goroutines hold the pool. | ✓ |
| Eager background re-spawn | When the ping/exit goroutine detects death, immediately spawn a replacement in the background and re-add to p.slots when ready. Faster recovery but adds an always-on supervisor goroutine and concurrent-Initialize race surface. | |
| Refuse to hand out dead slots; block until any other slot frees | Pop and discard dead slots in Acquire until a healthy one is found. Pool effectively shrinks on death — no re-spawn at all. Simpler but degrades capacity until restart. | |

**User's choice:** Lazy synchronous re-spawn at Acquire (Node parity).

### Q3: If lazy re-spawn fails (kiro-cli won't start), what should the caller see?

| Option | Description | Selected |
|--------|-------------|----------|
| Surface a typed error; caller's request fails with 503 | Pool.NewSession returns a wrapped pool-spawn error; adapters render 503 Service Unavailable. The dead slot is dropped from p.all (pool shrinks). Loud failure mode is the right default — silent capacity loss is worse than a single-request error. | ✓ |
| Retry once with the next slot, then 503 | If spawn fails, try popping another slot from p.slots; if that also fails, then 503. Hides transient flakes but multiplies latency on a real outage. | |
| Block caller until a healthy slot frees | Treat spawn failure like "no slots available" — keep waiting. Dangerous: if every slot is dead, caller hangs forever (only the ctx deadline saves them). | |

**User's choice:** Surface typed error → 503; pool shrinks visibly.

---

## Session-to-subprocess ownership model (SESS-01)

### Q1: Where does the kiro-cli subprocess for a stateful session live?

| Option | Description | Selected |
|--------|-------------|----------|
| Separate SessionRegistry with its own spawned acp.Clients | SessionRegistry holds map[sid]*sessionEntry where each entry has its own spawned acp.Client — entirely outside the warm pool. Pool size stays at POOL_SIZE; sessions add headroom on top. /health/agents shows them as a separate list. Matches Node reference exactly. | ✓ |
| Sticky slot pulled from the pool for the session's lifetime | When X-Session-Id is first seen, Acquire pulls a slot and keeps it bound to that sid until reaper / DELETE. Pool effectively shrinks while sessions live. Reuses pool plumbing but couples capacities and risks starvation of stateless traffic. | |
| Hybrid: session entries spawn fresh kiro-cli but reuse Pool.initSlot factory | SessionRegistry is its own struct (separate from Pool) but uses the same SlotFactory / acp.Config so spawn ergonomics are shared. Capacities decoupled like option 1; code reuse like option 2. | |

**User's choice:** Separate SessionRegistry, Node parity exact.

### Q2: How does a session come into existence on the gateway side?

| Option | Description | Selected |
|--------|-------------|----------|
| Lazy create on first request with new X-Session-Id (Node parity) | First /api/chat or /v1/messages with an unrecognized X-Session-Id spawns the dedicated subprocess, stores the entry, then proceeds. No explicit create endpoint. Zero client-side ceremony — the SDKs / LangFlow just set the header. | ✓ |
| Explicit POST /v1/sessions creates; request with unknown sid returns 404 | Clients must create a session first, then send X-Session-Id. Cleaner contract but breaks the Node SDK shape and adds a round-trip for every new conversation. | |
| Lazy create + ID generation | Gateway-driven IDs guarantee uniqueness but the SDKs we target (Pi SDK, @anthropic-ai/sdk, LangFlow) don't read response headers for session continuity — client-provided IDs match how they work. | |

**User's choice:** Lazy create on first-seen X-Session-Id.

### Q3: Is there a hard cap on concurrent stateful sessions?

| Option | Description | Selected |
|--------|-------------|----------|
| Env-driven cap (SESSION_MAX, default 32) — exceeded requests get 503 | Each session is a kiro-cli subprocess (heavy: RAM + FDs). An unbounded map is an OOM vector if a misbehaving client churns through unique X-Session-Id values. Bounded with a high default keeps prod safe; loud failure beats silent OOM. | ✓ |
| Unbounded — trust the reaper (Node parity) | Node has no cap and relies solely on SESSION_TTL_MS to bound resident set. Simpler, matches reference exactly, but vulnerable to burst churn within a TTL window. | |
| Same cap as POOL_SIZE | Conflates two different resources. POOL_SIZE is tuned for stateless concurrency; session count should scale with conversation cardinality, which can be much larger. | |

**User's choice:** Env-driven cap (SESSION_MAX, default 32); 503 on overflow.

### Q4: How should a session handle concurrent requests with the same X-Session-Id?

| Option | Description | Selected |
|--------|-------------|----------|
| Serialize per-session: second request blocks until first stream completes | kiro-cli session/prompt is inherently single-conversation — two parallel prompts on the same session corrupt the message history. A per-entry mutex serializes requests; second caller waits or hits an acquire timeout. | ✓ |
| Reject with 409 Conflict when busy | Loud, immediate signal that the client is misusing the session. Safer-by-default but breaks legitimate retry patterns and the LangFlow / GSD Pi flows that might double-fire on reconnect. | |
| Spawn a transient second subprocess for the overlapping request | Highest throughput but the second request gets no conversation history, defeating the purpose of X-Session-Id. Confusing surface behavior. | |

**User's choice:** Serialize per-session via per-entry mutex.

### Q5: What should DELETE /v1/sessions/:id do if there's an in-flight stream?

| Option | Description | Selected |
|--------|-------------|----------|
| Cancel the in-flight prompt (session/cancel), close the subprocess, return {deleted: id} | Mirrors the disconnect-cancel watchdog from Phase 4 — in-flight stream cleanly aborts, kiro-cli is closed, entry removed. Predictable for callers and reuses ACPClient.Cancel + Close paths. | ✓ |
| Wait for stream to drain, then close | Polite but blocks the DELETE caller for an unbounded duration. Mid-stream cancel is the explicit point of disconnect-cancel — DELETE should be no slower than client disconnect. | |
| 409 Conflict while in-flight — caller must retry | Forces the caller to coordinate, but no real client does this. The kiro-cli will keep running on a session the caller can never reach until TTL fires. | |

**User's choice:** Cancel in-flight + close + return {deleted: id}.

### Q6: When does the session subprocess call SetModel?

| Option | Description | Selected |
|--------|-------------|----------|
| Per request — SetModel before every Prompt if the request's model differs from the last | Matches stateless engine behavior. OpenAI and Ollama clients send the model on every request; honoring it lets users switch mid-conversation. Cached comparison avoids redundant set_model calls. | ✓ |
| Once on session creation — first request's model is sticky until DELETE | Simpler but surprising: a second request with a different model gets silently ignored or rejected. None of the target SDKs document this behavior. | |
| Reject mismatch with 409 — session model is immutable | Loud failure on switch, but breaks the natural OpenAI/Anthropic shape where model is a per-request field. | |

**User's choice:** Per-request SetModel, cached diff only.

---

## Reaper cadence + last_used timestamp (SESS-02)

### Q1: What is the reaper cadence and TTL default?

| Option | Description | Selected |
|--------|-------------|----------|
| 60s ticker, SESSION_TTL_MS default 1,800,000 (30 min) — Node parity | Exact match to the Node reference; env names match the brief's backward-compat contract. 60s is fine-grained enough that real-world latency on the close is bounded; 30 min TTL handles "user stepped away from chat" without aggressive churn. | ✓ |
| 30s ticker, same TTL | Faster reaping at the cost of more wakeups. Marginal win — the reaper does basically nothing 99% of the time. | |
| Adaptive: ticker fires at TTL/4 (computed from SESSION_TTL_MS) | Self-tunes for short TTLs in tests. Elegant but adds complexity and the test-only fast path is better solved by a test-only override. | |

**User's choice:** 60s ticker + 30min default TTL (Node parity).

### Q2: What counts as last_used for TTL?

| Option | Description | Selected |
|--------|-------------|----------|
| Updated at response complete (after Stream.Result returns or stream closes) | TTL measures "time since session was last actively serving traffic." A long-running stream (5min response) doesn't get reaped mid-stream, and TTL starts counting from when the conversation actually went idle. | ✓ |
| Updated at request start | Simpler but a 35-min stream against a 30-min TTL would race against the reaper. Also lets a chatty client extend a session indefinitely with rapid no-op requests. | |
| Updated on every chunk written | Most precise but adds per-chunk overhead to the hot path. The improvement over "response complete" is negligible for any realistic stream length. | |

**User's choice:** last_used updated at response complete.

### Q3: What does the reaper do to a session whose last_used > TTL?

| Option | Description | Selected |
|--------|-------------|----------|
| Take per-entry mutex; if no in-flight stream, cancel + close + remove | Reaper coordinates with the session-concurrency mutex — if a stream is active (mutex held), skip this entry and try again next tick. If idle, cancel any orphan prompt (defensive), close the subprocess, remove the map entry. No race with serving traffic. | ✓ |
| Just call Cancel+Close — in-flight stream gets aborted mid-response | Simpler logic but a user mid-response when TTL fires gets a truncated stream. Surprising and breaks the contract that last_used = response complete. | |
| Schedule expiry on a timer set when last_used updates | No periodic scan — each session owns its own time.AfterFunc. More precise reaping but the goroutine accounting is harder. | |

**User's choice:** TryLock per-entry mutex; skip in-flight, otherwise cancel+close+remove.

### Q4: How is the reaper unit-tested without waiting 30 minutes?

| Option | Description | Selected |
|--------|-------------|----------|
| Constructor takes ttl + tickInterval as params; tests pass 200ms / 50ms | Standard Go pattern — production calls registry.New(cfg) where cfg reads env, tests call registry.New(Config{TTL: 200*time.Millisecond, TickInterval: 50*time.Millisecond}). No globals, no env mutation, no fakeClock needed. | ✓ |
| Inject a clock interface (clockwork or stdlib-equivalent) | Fake-clock lets tests advance time deterministically without real sleeps. More robust but pulls in a clock abstraction the codebase doesn't use elsewhere. | |
| t.Setenv("SESSION_TTL_MS", "200") in tests | Reuses production config plumbing but pollutes the env, doesn't help with tick interval, and forces config reload paths to be test-aware. | |

**User's choice:** Constructor-injected TTL + TickInterval params.

---

## /health/agents shape & endpoint placement (OBSV-02)

### Q1: Where does the new agent-detail endpoint live?

| Option | Description | Selected |
|--------|-------------|----------|
| New endpoint at GET /health/agents — detail view | Matches the ROADMAP success criterion 5 verbatim. /health stays as the cheap LB-friendly summary (current shape); /health/agents adds the verbose per-slot + per-session detail. Both auth-exempt. | ✓ |
| Extend /health response with full agents object | One endpoint, but every probe (LB, k8s liveness) now pays for per-slot serialization. /health is currently flat and small — keeping it that way is operationally cleaner. | |
| /health/agents AND extend /health with a compact slots[] / sessions[] summary | Both endpoints. More to maintain and the duplication invites drift between summary fields and detail fields. | |

**User's choice:** New /health/agents endpoint, /health stays summary.

### Q2: Per-slot row contents

| Option | Description | Selected |
|--------|-------------|----------|
| {label, alive, busy, current_session_id?} | Matches ROADMAP — alive, busy, label. current_session_id (nullable) is a nice-to-have for debugging "which slot is serving sid X" — cheap to populate from sessionSlots map. | ✓ |
| Minimal: {label, alive, busy} | ROADMAP-exact minimum. Loses the slot-to-session linkage that helps operators debug a stuck slot. | |
| Maximal: includes pid, started_at, last_request_at, error_count | Operator-friendly but needs new bookkeeping in acp.Client. Better to land minimal first and add fields as operational needs surface. | |

**User's choice:** {label, alive, busy, current_session_id?}.

### Q3: Per-session row contents

| Option | Description | Selected |
|--------|-------------|----------|
| {id, alive, last_used, busy, model?} | ROADMAP requires alive + last_used; id is obviously needed; busy mirrors the slot field so operators can quickly see which sessions are currently serving traffic; model (nullable) helps identify what kind of conversation it is. | ✓ |
| Minimal: {id, alive, last_used} | Exact ROADMAP minimum. No way to tell whether a session is mid-stream or idle. | |
| Maximal: created_at, message_count, total_tokens, last_model, current_request_id | Helpful for analytics but adds bookkeeping outside Phase 5 scope. | |

**User's choice:** {id, alive, last_used, busy, model?}.

### Q4: Should /health/agents redact the session id?

| Option | Description | Selected |
|--------|-------------|----------|
| Return full id verbatim | X-Session-Id is supplied by the client — it's not a secret on the gateway's side. Behind AUTH_TOKEN the endpoint is gated; without it, the same exposure applies as /health pool stats. Truncation just hinders debugging. | ✓ |
| Truncate to first 8 chars | Defensive against accidental log leaks. Trade-off is operator-DX. | |
| Make redaction env-driven (HEALTH_REDACT_IDS=1) | A knob nobody will tune. Defer until a real operator request shows up. | |

**User's choice:** Full id verbatim.

### Q5: Auth-exempt or AUTH_TOKEN-gated?

| Option | Description | Selected |
|--------|-------------|----------|
| Auth-exempt like /health | Matches Phase 2 — /health, /api/version, /, /health/agents all bypass auth. LB probes and operator curl-from-jumphost work without managing tokens. | ✓ |
| Require AUTH_TOKEN when set | Session detail is more sensitive than pool stats. But operators rely on auth-exempt /health* for monitoring; consistency win outweighs marginal info leak. | |
| Configurable: HEALTH_AGENTS_AUTH=required env knob | A knob for an undecided question. Pick a default and let operators reverse-proxy if they disagree. | |

**User's choice:** Auth-exempt, same exempt list as /health.

---

## Claude's Discretion

Areas where the planner/researcher have latitude (captured in CONTEXT.md's Claude's Discretion section):

- Whether the per-slot exit-watcher (D-01) lives inside `acp.Client` itself (as a `Done()` channel) or as a new goroutine in `internal/pool/`.
- Concrete struct layout of `internal/session/registry.go` (Entry vs Session naming, mutex placement, Acquire/Release vs direct Prompt-on-entry).
- How `/health/agents` discovers the registry (separate `RegistryStatsSource` interface vs combined `AgentDetailSource`).
- Whether `Pool.Close` waits for in-flight registry sessions to drain or fires them in parallel — provided `goleak` passes.
- Exact wire shape of `/health/agents` JSON (object-keyed vs flat), as long as it carries the D-15 + D-16 fields.

## Deferred Ideas

Captured in CONTEXT.md under Deferred Ideas — summary:

- PID, started_at, error_count in /health/agents — defer to Phase 9 if needed.
- Real token counts in session rows — Phase 7+.
- Per-session metrics (message_count, total_tokens, last_model history) — until real observability need shows up.
- HEALTH_AGENTS_AUTH knob — reverse-proxy is the answer if operator disagrees with default.
- Explicit POST /v1/sessions endpoint — Node parity is lazy-create.
- Adaptive reaper cadence — production stays at 60s.
- Hash/truncate X-Session-Id — defer until a real leak or compliance ask.
- Cross-session model affinity — out of scope.
