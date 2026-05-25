# Phase 4: Streaming - Context

**Gathered:** 2026-05-24
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 4 makes **all three surfaces stream by default off the same canonical
chunk channel** (`engine.Run().Stream().Chunks()`) and adds **explicit
`session/cancel` on client disconnect** (STRM-04) for every surface at once.

**Important reframing — the ROADMAP goal predates the Anthropic insertion.**
Two of the three surfaces already stream as of Phase 3 / 3.1:

- **OpenAI SSE** — shipped in Phase 3 (`internal/adapter/openai/sse.go`,
  flat `data:` frames + `data: [DONE]`), consumes `engine.Run().Stream().Chunks()`.
- **Anthropic SSE** — shipped day-one in Phase 3.1 (`internal/adapter/anthropic/sse.go`,
  event-named frames + ping ticker + block state machine), same channel.

So Phase 4's actual **net-new** work is:

1. **Ollama NDJSON streaming (STRM-01).** Today the Ollama handlers
   force-downgrade `stream:true → false` (`internal/adapter/ollama/handlers.go:42-45,82-84`)
   and route through `engine.Collect`. Phase 4 flips the default to
   `stream:true` (Node parity) and adds a new NDJSON emitter that consumes
   the same canonical chunk channel.
2. **Disconnect → `session/cancel` (STRM-04).** Explicitly deferred from
   Phase 3.1 (see that phase's D-06 and Deferred Ideas). The plumbing
   exists (`acp.Client.Cancel` sends a `session/cancel` notification;
   `engine.ACPClient.Cancel(sid)` is wired), but **no surface currently
   triggers it on disconnect** — the SSE emitters only return `ctx.Err()`.
   Phase 4 adds the engine-owned watchdog that actually fires it.
3. **Ratify + regression-test (STRM-02/03/05).** Prove all three surfaces
   consume one canonical channel, both SSE shapes stay correct, and
   `stream:false` still returns a single JSON body unchanged.

**Requirements covered:** STRM-01, STRM-02, STRM-03, STRM-04, STRM-05.

**Explicitly NOT in Phase 4:**

- Real warm pool (`POOL_SIZE > 1`), dead-slot detection, stateful sessions
  (`X-Session-Id`) — Phase 5. Phase 4 runs on the pool-of-1 from Phase 2.
  SC4's "without crashing the slot" must hold on the pool-of-1; full
  slot-release-on-cancel semantics harden in Phase 5.
- Tool-call streaming (`input_json_delta` accumulation, `coerceToolCall`)
  — Phase 6. Phase 4 streams text + thinking chunks only.
- Hook chain implementations — Phase 8 (the empty Pre/Post seam carries
  forward unchanged).
- Embeddings — Phase 7.

</domain>

<decisions>
## Implementation Decisions

### Ollama NDJSON (STRM-01)

- **D-01: One NDJSON line per canonical chunk.** Emit one
  `application/x-ndjson` object per `canonical.Chunk` received from
  `engine.Run().Stream().Chunks()` — same cadence the OpenAI/Anthropic SSE
  emitters already use, and faithful to the Node reference's per-`session/update`
  emission. No timer-based coalescing.
- **D-02: Partial lines carry no stats; the final `done:true` line carries
  the full stats block.** Intermediate lines: `{model, created_at,
  message:{role:"assistant", content:"<delta>"}, done:false}` for `/api/chat`
  (and `{...,response:"<delta>",done:false}` for `/api/generate`). Final line:
  `{..., done:true, done_reason, total_duration, prompt_eval_count,
  eval_count, eval_duration, ...}` — reuse the existing `ollamaChatResponse` /
  `ollamaGenerateResponse` structs and the `mapStopReason` / `estimateTokens`
  helpers in `render.go`.
- **D-03: Default flips to `stream:true` (Node parity).** Remove the
  `wire.Stream = false` downgrade in `handlers.go`. Absent/`true` → NDJSON
  stream via `engine.Run`; explicit `stream:false` → existing `engine.Collect`
  single-JSON path (unchanged). `/api/generate` mirrors `/api/chat`.
- **D-04: Thought chunks stream as `message.thinking` deltas.** Partial lines
  for `ChunkKindThought` carry `{message:{role:"assistant", thinking:"<delta>"}}`,
  mirroring how non-streaming `chatResponseToWire` already populates the
  `omitempty` `thinking` field (Phase 3.1 D-02). Keeps stream and non-stream
  consistent. (`/api/generate` has no thinking field — thoughts are dropped
  there, matching its non-stream shape.)
- **D-05: Mid-stream errors truncate + debug-log — no error frame.** Once
  NDJSON headers are written, a terminal engine/ACP error stops line emission
  and is debug-logged, exactly like `openai/sse.go` `finalizeSSE` (Pitfall 3).
  No `{done:true,error:...}` line (non-standard for Ollama NDJSON; risks
  echoing internal error text per T-02-33). Client sees a truncated stream.

### Disconnect cancellation (STRM-04)

- **D-06: Engine-owned watchdog fires `session/cancel`.** `engine.Run`
  spawns a watchdog bound to the request ctx; on ctx termination **before**
  the stream completes normally, it calls `ACPClient.Cancel(sid)`. Adapters
  need **zero new cancel code** — all three surfaces (and any future surface)
  get disconnect-cancel for free. This realizes the core-value "one place to
  enforce policy" and cashes the Phase 3.1 "watchdog" deferral note.
  - **Leak constraint (load-bearing):** the watchdog MUST be torn down when
    the stream completes normally, or it leaks a goroutine per request. The
    `goleak` gate on handler/engine tests is the guard. Planner/researcher
    design the teardown signal (e.g., watchdog selects on `ctx.Done()` vs a
    completion channel the Run closes).
- **D-07: Both ctx-cancellation AND frame-write failure trigger cancel.**
  A failed write/Flush to the `ResponseWriter` (broken pipe) means the client
  is gone even if `ctx` hasn't fired yet. On a write error, the adapter
  cancels the request ctx it passed to `engine.Run` (so the D-06 watchdog
  observes it) — the adapter does not call `Cancel(sid)` directly. This keeps
  the single cancel path (the watchdog) authoritative while letting the
  adapter surface a disconnect it detected first.

### Stream code organization

- **D-08: Three independent emitters — no shared stream driver.** Add
  `internal/adapter/ollama/ndjson.go` modeled on the existing `sse.go`
  siblings; leave `openai/sse.go` and `anthropic/sse.go` as-is. The wire
  shapes genuinely diverge (Anthropic: ping ticker + block state machine +
  multiple events per chunk; OpenAI: role-first delta + `[DONE]`; Ollama:
  `done:true` line). The D-06 engine watchdog already centralizes the cancel
  *policy*, so the duplicated `select { ctx.Done / chunks }` skeleton is
  low-stakes — not worth refactoring two shipped, tested surfaces behind a
  leaky abstraction. ("Add the shared driver when needed" — speculative DRY.)

### Verification

- **D-09: Automated E2E per surface — no HUMAN-UAT gate.** Extend
  `tests/e2e/` with real-binary streaming round-trips for all three surfaces
  (Ollama NDJSON, OpenAI SSE, Anthropic SSE) plus a `stream:false` regression
  per surface. Matches the standing preference and the sibling surfaces'
  existing E2E coverage; no auth needed (`AUTH_TOKEN` unset).
  - **Flip the existing downgrade guard:** `tests/e2e/ollama_e2e_test.go`
    (from quick-task 260524-pyd) currently asserts `stream:true` is
    downgraded. That guard MUST be replaced with a streaming-NDJSON assertion
    now that D-03 flips the default.
- **D-10: Prove `session/cancel` via fake-ACP frame assertion + real-binary
  disconnect smoke.** The deterministic unit of STRM-04 is "a `session/cancel`
  JSON-RPC notification is emitted after the request ctx is canceled
  mid-stream" — assert this against the existing fake-ACP server
  (`internal/acp/fakeacp_test.go` shapes), where the frame is observable on
  the fake wire. Add a real-binary E2E that starts a stream, drops the client
  mid-stream, and asserts the slot survives + no goroutine leak. (Real-kiro
  can't easily expose its wire to assert the exact frame, so it is not the
  primary proof.)

### Claude's Discretion

The planner/researcher have latitude on:

- The exact watchdog teardown mechanism (D-06) — completion channel,
  `context.AfterFunc`, or a Run-lifecycle hook — provided `goleak` passes.
- Whether the watchdog lives in `engine.Run` directly or in a small
  `acp_adapter.go`-adjacent helper, as long as adapters stay cancel-free.
- File split inside `internal/adapter/ollama/` — likely a new `ndjson.go`
  emitter + small additions to `handlers.go`; whether NDJSON tests fold into
  `handlers_test.go` or a new `ndjson_test.go`.
- Whether `engine.Run`'s ctx-cancel watchdog distinguishes
  `context.Canceled` (disconnect) from `context.DeadlineExceeded` (timeout)
  — both should cancel; the log line may differ.
- Exact assertion mechanics for the fake-ACP `session/cancel` test (channel
  signal vs recorded-frames slice).

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project context (must-read)

- `.planning/PROJECT.md` — Core value ("one place to enforce policy"),
  constraints, Key Decisions table. Specifically the **"Anthropic surface
  ships SSE day-one (Phase 3.1, not deferred to Phase 4)"** decision which
  states *"Phase 4 retroactively ratifies all three formats off one canonical
  channel"* — that ratification is STRM-02/03 in this phase.
- `.planning/REQUIREMENTS.md` — STRM-01..05 (lines 33-37) are the phase's
  requirement set; also STRM behavioral-parity context.
- `.planning/ROADMAP.md` §"Phase 4: Streaming" — phase goal + 5 success
  criteria. (Goal text says "both surfaces" — predates Anthropic; treat as
  "all three" per this CONTEXT's reframing.)
- `.planning/phases/03.1-anthropic-surface/03.1-CONTEXT.md` — **D-06**
  (relied on ctx propagation, deferred explicit `session/cancel` to Phase 4),
  **D-04/D-05** (Anthropic SSE block state machine + single select-loop),
  and its Deferred Ideas ("Explicit `session/cancel` on client disconnect —
  Phase 4 adds the explicit `engine.Cancel(sid)` watchdog"). This is the
  direct upstream for STRM-04.
- `.planning/phases/03-openai-surface/03-CONTEXT.md` — OpenAI SSE emitter
  decisions (flat `data:` frames + `[DONE]`, Pitfall 2/3 handling).
- `.planning/phases/02-ollama-end-to-end/02-CONTEXT.md` — Ollama adapter
  shape, `engine.Run`/`engine.Collect` split (D-01), pickCwd, body caps,
  `writeError` helper, the non-streaming `chatResponseToWire` parity rules.

### Behavioral parity (must-read for Ollama NDJSON)

- `docs/reference/acp_server_node_reference.md` §"Things that are easy to
  get wrong" (lines 286-288) — **"Ollama streams by default"**: both
  `/api/chat` and `/api/generate` default to `stream:true` and respond with
  `application/x-ndjson`; `stream:false` → single JSON body. Endpoint table
  lines 270-271. This is the ground truth for D-01..D-05.
- `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server` (`acp-ollama-server.js`)
  — the Node source itself; the per-`session/update` NDJSON emission loop
  and `makeStats` timing split (15%/85%) the Go `render.go` already mirrors.

### ACP wire shapes (must-read for STRM-04)

- `docs/reference/acp_wire_shapes.md` — authoritative ACP JSON-RPC wire
  shapes; the `session/cancel` notification shape is the frame D-10 asserts.
- `internal/acp/client.go:759,800-805` — existing `Cancel(sessionID)` →
  `session/cancel` notification (best-effort, no response). The watchdog
  bottoms out here.

### Reference architecture (read as needed)

- `~/Projects/repos/local/bifrost/core/providers/anthropic/anthropic.go`
  and `.../openai/` — SSE-emission and stream-cancellation patterns for
  cross-reference (we do not copy fasthttp specifics).

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- `internal/engine/engine.go` — `Engine.Run` returns `*Run` exposing
  `Stream()` (`<-chan canonical.Chunk` via `Stream().Chunks()`) and
  `SessionID()`. **STRM-03 (one canonical channel) is already true for the
  two SSE surfaces.** `ACPClient.Cancel(sid)` is on the interface (line 47).
  The D-06 watchdog is added around the `Run` path here.
- `internal/engine/collect.go` — `engine.Collect` is the non-streaming
  aggregator used by the `stream:false` path; STRM-05 keeps it unchanged.
- `internal/adapter/openai/sse.go` — `runSSEEmitter(ctx, w, run, model, logger)`:
  single `select { <-ctx.Done() / <-chunks }` loop, Flusher assertion before
  headers, `finalizeSSE` truncate-on-error. **The model `ndjson.go` is built
  from.** Note its `ctx.Done` branch currently only returns `ctx.Err()` —
  D-06 means it no longer needs to (the watchdog handles cancel), but the
  write-error path (D-07) should cancel the request ctx.
- `internal/adapter/anthropic/sse.go` — event-named SSE + ping ticker + block
  state machine (Phase 3.1 D-04/D-05). Left as-is (D-08).
- `internal/adapter/ollama/handlers.go` — `handleChat` / `handleGenerate`
  currently force `wire.Stream = false` (lines 42-45, 82-84) and call
  `engine.Collect`. **D-03 removes the downgrade and branches on `stream`.**
- `internal/adapter/ollama/render.go` — `chatResponseToWire` /
  `generateResponseToWire` (final-line stats), `mapStopReason`,
  `estimateTokens`, `joinTextContent`, `joinThinkingContent`. Reuse these for
  the D-02 final `done:true` line and per-delta content extraction.
- `internal/acp/client.go` — `Cancel` → `session/cancel`; `internal/acp/fakeacp_test.go`
  — the fake ACP server D-10's frame-assertion test extends.

### Established Patterns

- **`engine.Run` = streaming path, `engine.Collect` = non-streaming sugar**
  (Phase 2 D-01 / Phase 3.1 D-01). Every adapter branches on the surface's
  `stream` flag between these two.
- **Single-goroutine writer for streaming** (Phase 3.1 D-05) — the
  ResponseWriter/Flusher is touched from exactly one goroutine per request;
  no mutex. The NDJSON emitter follows the same rule.
- **Flusher assertion + headers-before-WriteHeader(200)** (Pitfall 2) — set
  `Content-Type` before the first write; fall back to JSON 500 if the writer
  isn't a Flusher.
- **Truncate-on-post-header-error** (Pitfall 3 / A5) — once streaming headers
  are sent, terminal errors debug-log and stop; no JSON 500.
- **`goleak` gate on handler/engine tests** — the D-06 watchdog teardown is
  validated here.
- **`context.Context`-first cancellation** (Phase 1 D-02) — the request ctx
  threads from `r.Context()` → `engine.Run` → ACP `Prompt`.

### Integration Points

- `engine.Run` gains the disconnect-cancel watchdog (D-06); adapters are
  unchanged on the cancel axis except the D-07 write-error → ctx-cancel hook.
- `ollama.handleChat` / `handleGenerate` branch to a new `ndjson.go` emitter
  on `stream` (default true); `stream:false` keeps the `engine.Collect` path.
- `tests/e2e/` (`ollama_e2e_test.go`, `openai_e2e_test.go`, plus an Anthropic
  E2E) gain streaming round-trips; the Ollama stream-downgrade guard is
  replaced (D-09).

</code_context>

<specifics>
## Specific Ideas

- **The Ollama stream-downgrade guard is a known landmine.** Quick-task
  260524-pyd added an E2E assertion that `stream:true` gets downgraded to a
  single JSON body. D-03 inverts that behavior, so that specific assertion
  MUST be found and flipped — leaving it in place will fail CI once Ollama
  streams.
- **`session/cancel` is best-effort and unobservable on real kiro.** The
  `acp.Client.Cancel` notification expects no response. That's why D-10 puts
  the authoritative assertion on the fake-ACP server (observable wire) rather
  than a real-kiro test.
- **SC4 "without crashing the slot" on the pool-of-1.** Phase 4 still runs
  the Phase 2 pool-of-1. Cancel must leave the single slot reusable for the
  next request; the full slot-release-on-cancel/`sync.Once` hardening is
  Phase 5, but Phase 4 must not deadlock or kill the slot on a mid-stream
  cancel.
- **STRM-02/03 are mostly ratification, not new code.** Be explicit in plans
  that the OpenAI/Anthropic SSE work is "verify + regression-test," to avoid
  re-implementing shipped emitters.

</specifics>

<deferred>
## Deferred Ideas

- **Full slot-release-on-cancel semantics + `POOL_SIZE > 1`** — Phase 5.
  Phase 4 only needs cancel to not crash the pool-of-1 slot.
- **Tool-call streaming (`input_json_delta` deltas, `coerceToolCall`)** —
  Phase 6. Phase 4 streams text + thinking only.
- **Shared stream-driver abstraction across surfaces** — explicitly rejected
  for now (D-08). Revisit only if a 4th surface or a real DRY pain emerges.
- **Real token counts in streaming stats** — Phase 7+ (kiro-cli doesn't
  report tokens; `estimateTokens` parity stands).
- **`signature_delta` for Anthropic thinking blocks** — carried from Phase
  3.1 deferred; not a Phase 4 concern.

</deferred>

---

*Phase: 4-Streaming*
*Context gathered: 2026-05-24*
