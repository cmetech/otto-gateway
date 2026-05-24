# Phase 3: OpenAI Surface - Context

**Gathered:** 2026-05-24
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 3 brings the **third adapter** (`internal/adapter/openai`, today an
empty `.gitkeep` dir) online, sharing the same canonical engine as Phase 2's
Ollama adapter and Phase 3.1's Anthropic adapter. A Pi-SDK chat CLI configured
with `base_url=http://localhost:11434/v1` and a bearer token completes an
end-to-end chat against `kiro-cli` and receives an OpenAI-compatible response —
**including SSE streaming day-one** (see D-02). This phase proves the
adapter-over-canonical layout cleanly supports three surfaces and forces the
deferred `/v1` co-mount refactor (Phase 3.1 D-17) to be solved.

**Deliverables (per ROADMAP.md Phase 3 success criteria):**

1. `curl -X POST http://localhost:11434/v1/chat/completions -H 'Authorization: Bearer …' -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'`
   returns an OpenAI-compatible JSON response sourced from the same canonical
   engine that serves `/api/chat` and `/v1/messages`.
2. A Pi-SDK CLI configured with `base_url=http://localhost:11434/v1` and a
   bearer token completes a chat round-trip with zero SDK modification —
   **both `stream:false` JSON and `stream:true` SSE** (D-02).
3. `GET /v1/models` and `POST /v1/completions` return OpenAI-compatible shapes;
   `/v1/models` and `/api/tags` reflect the same underlying model set.
4. `ENABLED_SURFACES` extends to accept `openai`; default becomes
   `ollama,anthropic,openai`. `ENABLED_SURFACES=ollama` (or any subset omitting
   `openai`) disables the OpenAI surface without code changes;
   `OPENAI_PATH_PREFIX` / `OLLAMA_PATH_PREFIX` overridable.
5. Architectural boundary check passes: `internal/adapter/openai`,
   `internal/adapter/ollama`, and `internal/adapter/anthropic` import only
   `internal/canonical` + `internal/plugin`; none import `internal/engine`.

**Requirements covered:** SURF-02, SURF-04, SURF-06.

**Explicitly NOT in Phase 3:**

- Explicit `session/cancel` on client-disconnect — Phase 4 (STRM-04). Phase 3
  relies on `r.Context()` propagation only (mirror 3.1 D-06); the underlying
  `ACPClient.Prompt` ctx unwinds naturally and the SSE select-loop returns on
  `ctx.Done`.
- Ollama NDJSON default streaming — Phase 4 (STRM-01). Phase 3 only adds the
  OpenAI SSE shape.
- Tool dispatch / execution and `coerceToolCall` — Phase 6 (TOOL-01..03). Phase
  3 renders tool calls in OpenAI's native shape (JSON-**string** `arguments`)
  if/when the engine yields tool-call chunks, but does NOT coerce JSON-as-text
  or execute tools.
- `/v1/completions` advanced legacy params (`logprobs`, `echo`, `suffix`,
  `best_of`, `n>1`) — minimal shim only (D-03). The kiro-cli backend can't
  honor them and modern clients don't use them.
- Embeddings (`/v1/embeddings`) — Phase 7.
- Real token counts in `usage` — kiro-cli reports none; return honest zeros
  (carry-forward 3.1 D-12).
- Real warm pool (`POOL_SIZE > 1` default), dead-slot detection, stateful
  sessions — Phase 5.
- Hook chain implementations (RequestID/Auth/Logging hooks) — Phase 8. The
  empty `PreHook`/`PostHook` seam carries forward unchanged.

</domain>

<decisions>
## Implementation Decisions

### Routing / server composition (the load-bearing Phase 3 decision)

- **D-01: Refactor `server.Config` to a generic `Surfaces []SurfaceMount` list
  and group mounts by shared prefix.** This is the Phase 3.1 D-17 deferred
  decision, now cashed. The chi router panics on a duplicate `r.Route(prefix,…)`
  AND on a duplicate `r.Mount("/", …)` within one block — so OpenAI and
  Anthropic (both default `/v1`) cannot use separate Route blocks, and a naive
  "mount both routers at `/`" also panics. The fix: a `SurfaceMount{Prefix
  string, Router chi.Router}` list; `NewFromConfig` groups entries by prefix
  and, for each unique prefix, opens **one** auth-wrapped `r.Route(prefix, …)`
  block onto which all surfaces sharing that prefix register their routes.
  Anthropic's `POST /messages` and OpenAI's `POST /chat/completions` +
  `POST /completions` + `GET /models` don't collide, so one `/v1` router hosts
  both. Ollama (`/api`) is just another entry in the list. Rationale: aligns
  with the user's seam-now preference ([[feedback_locked_design_seams]]), 4th+
  surfaces and Phase 5 work plug into the list with zero new bespoke branches,
  and it removes the per-surface parallel-fields pattern (`OllamaPath` /
  `AnthropicPath` / …) that doesn't scale.
  - **Planner's discretion on composition mechanics:** the cleanest way for two
    adapters to share one prefix-router (each adapter exposing a
    `RegisterRoutes(r chi.Router)` method vs the server copying a returned
    subrouter's routes vs `r.Mount` at distinct non-`/` subpaths) is a planning
    decision — the SurfaceMount grouping is the locked contract. Existing
    `ProtectedRouter() chi.Router` accessors (Ollama, Anthropic) may be kept and
    adapted, or replaced with a register-onto pattern; either is acceptable as
    long as the chi double-mount trap is avoided and the auth chain wraps the
    whole prefix once.
  - The `OllamaVersionHandler` exempt-route mechanism (registered on the OUTER
    router, auth-exempt per AUTH-03) stays as-is; OpenAI has no `/version`
    equivalent to expose.

### OpenAI adapter streaming (`internal/adapter/openai`)

- **D-02: OpenAI ships SSE day-one alongside non-streaming — NOT deferred to
  Phase 4.** *(User override of the non-streaming-only recommendation.)* The
  handler branches on the request's `stream` field: `stream:true` → SSE via
  `engine.Run(ctx, req)` ranging `run.Stream().Chunks()`; `stream:false` → JSON
  via `engine.Collect`. SSE emits OpenAI's `chat.completion.chunk` frames as
  `data: <json>\n\n` lines terminated by a literal `data: [DONE]\n\n` frame
  (structurally simpler than Anthropic's event-named sequence — no `event:`
  lines, no per-block start/stop, no ping ticker required by the OpenAI SDK).
  First delta carries `role:"assistant"`; the final pre-`[DONE]` chunk carries
  `finish_reason`. Single select-loop in the handler goroutine touches the
  writer (mirror 3.1 D-05): `select { case c := <-chunks: writeData(...); case
  <-ctx.Done(): return }`. Phase 4 then refactors/ratifies that all three
  surfaces (Ollama NDJSON, Anthropic SSE, OpenAI SSE) render off the **one**
  `engine.Run` chunk channel — it no longer has to *introduce* OpenAI SSE.
  - **Disconnect handling: ctx-propagation-only** (mirror 3.1 D-06). On
    `r.Context()` cancel the select-loop's `ctx.Done` fires and the handler
    returns; `engine.Run` was called with that ctx so the ACP prompt unwinds.
    A debug log line on `ctx.Done` is welcome (free observability). Explicit
    `session/cancel` over JSON-RPC is Phase 4 (STRM-04).

### Endpoint scope (`/v1/models`, `/v1/completions`)

- **D-03: `/v1/models` full; `/v1/completions` minimal shim.** `/v1/models`
  reflects the pool `ModelCatalog` (same source `/api/tags` uses) so the two
  lists match per SC3 — see D-04. `/v1/completions` (legacy text-completion) is
  a thin shim: map the `prompt` string (or array → joined) to a single
  canonical user `Message`, run the engine, render the OpenAI **completion**
  shape (`object:"text_completion"`, `choices[].text`, `choices[].finish_reason`,
  `usage` zeros). Advanced params (`logprobs`, `echo`, `suffix`, `best_of`,
  `n>1`) are accepted-and-ignored (mirror the Phase 2 `KeepAlive`/`Options`
  accept-and-ignore pattern) — kiro-cli can't honor them and Pi uses
  `/chat/completions` anyway. Whether `/v1/completions` also honors `stream`
  is planner's discretion (chat-completions SSE is the load-bearing path;
  completions SSE is optional).

### Model identity (`/v1/models` + inbound `model` field)

- **D-04: `/v1/models` mirrors the pool `ModelCatalog`; inbound `model` handled
  like Anthropic 3.1.** Expose kiro-cli `availableModels` from the pool
  `ModelCatalog` in OpenAI shape (`{object:"list", data:[{id, object:"model",
  created, owned_by}]}`). SC3 requires `/v1/models` and `/api/tags` reflect the
  same underlying set, so a static synthetic list is rejected. Inbound
  `model:"auto"` or empty → skip `engine.SetModel`; any other string →
  `SetModel` (consistent with how Anthropic and Ollama treat it). The OpenAI
  adapter needs a `ModelCatalog`-equivalent dependency injected the same way
  Ollama's adapter receives `pool` as its catalog.

### Config / env (mostly forward-locked by 3.1 D-16)

- **D-05: `ENABLED_SURFACES` default widens to `ollama,anthropic,openai` and
  `openai` joins the `validateEnabledSurfaces` allow-list.** Phase 3.1 D-16
  explicitly forward-designed this. Update the default slice in `config.Load`,
  add `"openai"` to the allowed-names set in `validateEnabledSurfaces`, and
  update the error message ("allowed: ollama, anthropic, openai"). Unknown
  surface names still fail-fast at boot. `OPENAI_PATH_PREFIX` already exists in
  config (default `/v1`) and is already CLI-flag-wired — no new config field
  needed for the prefix.

### Carry-forward decisions (locked by precedent, not re-discussed)

- **engine surface unchanged** — `engine.Run` (stream) + `engine.Collect`
  (non-stream); no new engine methods, no `engine.Cancel` in Phase 3
  (3.1 D-01, D-06).
- **`auth.Bearer` already dual-header** — reads both `Authorization: Bearer`
  and `x-api-key` (3.1 D-15). OpenAI clients send `Authorization: Bearer`;
  works unchanged. Same `AUTH_TOKEN`, same IP-allowlist middleware on the `/v1`
  prefix.
- **`usage` honest zeros** — kiro-cli reports no token counts (3.1 D-12).
- **adapter-local `writeError` helper** — OpenAI error envelope is
  `{"error":{"message":"…","type":"…","param":null,"code":null}}` with HTTP
  status mapping (400 invalid_request_error, 401 invalid/missing key when
  AUTH_TOKEN set, 404 not_found, 413 body cap, 500 engine/pool error). Mirror
  the Ollama/Anthropic `writeError` pattern; envelope shape is OpenAI's, per
  the public spec.
- **`decodeJSONBody` + body-cap** — reuse the Phase 2/3.1 4 MiB pattern.
- **message `id` generation** — opaque to clients; reuse the
  `chatcmpl-<unix-nano>` pattern (Phase 2 style) or a UUID — planner picks.

### Claude's Discretion

- Composition mechanics for two adapters sharing one prefix-router (D-01) —
  `RegisterRoutes(r)` vs adapted `ProtectedRouter()` vs distinct-subpath mounts.
- Whether to keep or replace the existing per-surface `ProtectedRouter()`
  accessors when introducing `SurfaceMount` (D-01).
- File-scoped split inside `internal/adapter/openai/`: likely `adapter.go`,
  `handlers.go`, `wire.go` (decode), `render.go` (non-streaming encode),
  `sse.go` (streaming emitter), `errors.go` (envelope helper) — mirror the
  Anthropic layout; no `stub.go` (OpenAI has no Ollama-style stub endpoints).
- Whether `/v1/completions` honors `stream` (D-03).
- `message id` generation strategy (chatcmpl-<nano> vs UUID).
- SSE test approach (httptest + bufio.Scanner over the event-stream framing vs
  fake client) — httptest likely sufficient; real Pi-SDK round-trip is
  HUMAN-UAT.
- Whether the OpenAI chunk channel needs a keepalive comment frame — OpenAI SDK
  does not require pings (unlike Anthropic), so default to none unless research
  shows Pi needs one.

### Folded Todos

No pending todos matched Phase 3 (`gsd-sdk query todo.match-phase` returned
`todo_count: 0`).

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### OpenAI API spec (load-bearing — read FIRST)

- https://platform.openai.com/docs/api-reference/chat/create — **MANDATORY**
  for the `/v1/chat/completions` request/response shape, the `choices[]`
  envelope, `finish_reason` values, and the `usage` object.
- https://platform.openai.com/docs/api-reference/chat-streaming — **MANDATORY**
  for the SSE `chat.completion.chunk` frame shape, the `delta` object
  (first-chunk `role`, subsequent `content`), the `finish_reason`-bearing final
  chunk, and the `data: [DONE]` terminator (D-02).
- https://platform.openai.com/docs/api-reference/completions/create — for the
  legacy `/v1/completions` shape (`object:"text_completion"`, `choices[].text`)
  needed by the D-03 minimal shim.
- https://platform.openai.com/docs/api-reference/models/list — for the
  `/v1/models` list shape (`{object:"list", data:[{id, object:"model",
  created, owned_by}]}`) (D-04).
- **Pi SDK base-URL / streaming-default verification (OPEN ITEM):** STATE.md
  flags "Pi SDK env var / config key for setting the OpenAI base URL needs
  verification before Phase 3 starts." The Phase 3 researcher MUST confirm
  (a) Pi's exact config key for the OpenAI provider base URL, and (b) whether
  Pi's chat path defaults to streaming — D-02 ships SSE regardless, but this
  confirms which path SC2 exercises. Pi: https://pi.dev,
  `@earendil-works/pi-ai`. loop24-client / Pi installed locally.

### Loop24 Gateway project context (must-read)

- `.planning/PROJECT.md` — project overview, constraints, Key Decisions table
  (esp. the triple-surface and "Anthropic SSE day-one" decisions, which the
  Phase 3 OpenAI-SSE-day-one choice now parallels).
- `.planning/REQUIREMENTS.md` — Phase 3 covers SURF-02, SURF-04, SURF-06;
  cross-check SURF-08 (endpoint-level `/v1` disambiguation) and STRM-02/03
  (which Phase 3's SSE choice partially front-runs — flag for Phase 4).
- `.planning/ROADMAP.md` §"Phase 3: OpenAI Surface" — goal, mode (mvp),
  depends-on Phase 3.1, 5 success criteria.
- `.planning/STATE.md` — current state + the Pi SDK open verification item.
- `.planning/phases/03.1-anthropic-surface/03.1-CONTEXT.md` — **closest analog.**
  D-01 (engine.Run/Collect), D-04/D-05 (per-request SSE emitter, single
  writer-goroutine select-loop), D-06 (ctx-propagation disconnect), D-12 (usage
  zeros), D-15 (dual-header auth), D-16 (ENABLED_SURFACES), D-17 (the deferred
  Surfaces refactor cashed here as D-01), D-20 (error envelope helper pattern)
  all carry forward or directly inform Phase 3.
- `.planning/phases/02-ollama-end-to-end/02-CONTEXT.md` — Phase 2 adapter
  patterns (wire/render/handlers split, writeError, body-cap, ModelCatalog via
  pool, server `r.Route` mount mechanics being refactored in D-01).

### Spec of record (must-read)

- `docs/briefs/go_port_brief.md` — full design brief. Esp:
  - §3.8: architectural layer invariants (TRST-04 — Phase 3 activates the rule
    for `internal/adapter/openai`).
  - §3.12: trust gates — `gosec`, `errcheck`, `errorlint`, property tests,
    `goleak` all apply to the OpenAI SSE handler.
  - §3.13: **adapter-over-canonical layout** — Phase 3 is the third instance.
  - §5: M0–M9 milestone plan (OpenAI surface ≈ M3).

### ACP wire shapes (carry-forward)

- `docs/reference/acp_wire_shapes.md` — authoritative ACP JSON-RPC wire shapes;
  still load-bearing for how the engine builds ACP blocks the OpenAI adapter's
  canonical requests feed into.
- `docs/reference/acp_server_node_reference.md` — narrative Node reference.
  Note: the Node impl is **Ollama-only**; OpenAI has no Node parity precedent
  (same situation Anthropic 3.1 was in). Behavior is governed by the public
  OpenAI spec, not Node.

### Reference architecture (read as needed)

- `~/Projects/repos/local/bifrost/transports/bifrost-http/integrations/openai/`
  — Bifrost's OpenAI integration package; validates the adapter-over-canonical
  layout and the chat-completions request/response + SSE translation patterns
  for OpenAI specifically.
- `~/Projects/repos/local/bifrost/core/providers/openai/` — Bifrost's OpenAI
  provider for the `chat.completion.chunk` SSE-emission reference.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- `internal/adapter/anthropic/` — **the closest template.** `adapter.go`
  (`New(cfg)` + `ProtectedRouter()`), `handlers.go` (stream/non-stream branch on
  the wire `stream` field), `wire.go`/`decode.go` (canonical request build),
  `render.go` (non-streaming encode), `sse.go` (per-request SSE emitter +
  single-goroutine select-loop), `errors.go` (`writeError` envelope helper),
  plus the test layout (`*_test.go`, `sse_golden_test.go`, `integration_test.go`,
  `testmain_test.go` with `goleak.VerifyTestMain`). The OpenAI adapter mirrors
  this minus `event:`-line / ping-ticker complexity.
- `internal/adapter/ollama/` — second template; `wire.go`/`render.go`/
  `handlers.go` and the `pool`-as-`ModelCatalog` injection pattern for
  `/api/tags` (directly reused for `/v1/models`, D-04).
- `internal/canonical/chat.go` + `chunk.go` — ALL needed types exist (Phase 2/3.1
  forward design): `ChatRequest`, `ChatResponse`, `Message`, `ContentPart`,
  `ToolCall`, `ToolSpec`, `Usage`, `StopReason`, `Chunk`/`ChunkKind`, etc. Phase
  3 expects **zero canonical-type churn**.
- `internal/engine/` — `engine.Run` + `engine.Collect` consumed as-is (D-01
  carry-forward). No engine changes for Phase 3.
- `internal/auth/bearer.go` — already dual-header (3.1 D-15); reused on the `/v1`
  prefix unchanged. `internal/auth/ipallowlist.go` likewise.
- `internal/config/config.go` — `OpenAIPathPrefix` field + CLI flag already
  present; only the `ENABLED_SURFACES` default + `validateEnabledSurfaces`
  allow-list need editing (D-05).
- `internal/server/server.go` — `NewFromConfig` is the **refactor target** for
  D-01 (`Surfaces []SurfaceMount`). Current parallel `Ollama*`/`Anthropic*`
  fields + two `r.Route` blocks get replaced by the grouped-by-prefix list.
- `internal/pool/` — `*pool.Pool` already satisfies `engine.ACPClient` +
  Ollama's `ModelCatalog`; the OpenAI adapter receives the same catalog for
  `/v1/models`.

### Established Patterns

- **Adapter-over-canonical layout** — `internal/adapter/openai` imports ONLY
  `internal/canonical` + `internal/plugin`. `.go-arch-lint.yml` needs an
  `adapter_openai` entry (planner adds; mirror the `adapter_anthropic` entry).
- **Single-package per concern, file-scoped layers** — adapter file split as
  above.
- **`context.Context`-first** — SSE handler uses `r.Context()` directly.
- **Config-struct constructors** — `openai.New(openai.Config{Logger, Engine,
  ModelCatalog})`.
- **`*slog.Logger` via Config** — no `slog.SetDefault`.
- **Per-Node env-var contract** — `ENABLED_SURFACES` / `OPENAI_PATH_PREFIX`
  defaults preserve prior behavior; widened default is the only change (D-05).

### Integration Points

- `cmd/otto-gateway/main.go` `newApp` — add an `if
  slices.Contains(cfg.EnabledSurfaces, "openai")` branch constructing
  `openai.New(...)`, and build the `[]SurfaceMount` list (Ollama `/api`,
  Anthropic `/v1`, OpenAI `/v1`) passed to `server.NewFromConfig` (D-01).
- `internal/server.NewFromConfig` — refactored to group `Surfaces` by prefix and
  open one auth-wrapped `r.Route(prefix,…)` per unique prefix (D-01).
- `internal/engine` — consumed unchanged.

</code_context>

<specifics>
## Specific Ideas

- **Pi-SDK (`@earendil-works/pi-ai`, https://pi.dev) is the SURF-06 acceptance
  bar.** Configure it with the OpenAI provider + `base_url=…/v1` + bearer token
  and round-trip against the gateway. The exact Pi config key for base URL and
  whether Pi defaults to streaming are open research items (STATE.md) — confirm
  early.
- **OpenAI SSE is structurally simpler than Anthropic's.** `data: <chunk>\n\n`
  lines + terminal `data: [DONE]\n\n`; no `event:` names, no per-block
  start/stop, no mandatory ping keepalive. The OpenAI SDK does not impose the
  Anthropic SDK's idle-ping requirement. Do not over-build the emitter.
- **OpenAI ships SSE day-one — user override, consistent with Anthropic 3.1.**
  The user prefers each surface to ship its streaming path in its own phase
  rather than deferring to the catch-all streaming phase. See
  [[feedback_streaming_day_one_per_surface]]. This narrows Phase 4 to Ollama
  NDJSON + client-disconnect cancellation + one-channel ratification.
- **`/v1` now hosts two surfaces.** Phase 3 is the first time the co-mount
  exists; the `SurfaceMount` grouping (D-01) is the mechanism. `OPENAI_PATH_PREFIX`
  and `ANTHROPIC_PATH_PREFIX` both default `/v1`; an operator can split them
  (`ANTHROPIC_PATH_PREFIX=/anthropic/v1`) and the grouping degrades to two
  single-surface prefixes naturally.

</specifics>

<deferred>
## Deferred Ideas

- **Ollama NDJSON default streaming** — Phase 4 (STRM-01).
- **Explicit `session/cancel` on client disconnect** — Phase 4 (STRM-04).
  Phase 3 relies on ctx propagation (D-02 / 3.1 D-06).
- **Phase 4 scope is now narrowed** — with Anthropic SSE (3.1) and OpenAI SSE
  (Phase 3) both shipped in their surface phases, Phase 4 = Ollama NDJSON +
  disconnect cancellation watchdog + proving all three surfaces render off the
  single `engine.Run` canonical chunk channel. Flag this for the Phase 4
  discuss/plan: it is no longer "introduce SSE."
- **Tool dispatch / `coerceToolCall` / OpenAI JSON-string tool-call rendering**
  — Phase 6 (TOOL-01..03). Phase 3 renders the OpenAI tool-call shape if chunks
  arrive but does not coerce or execute.
- **`/v1/completions` advanced params** (`logprobs`, `echo`, `suffix`,
  `best_of`, `n>1`) — accepted-and-ignored in Phase 3 (D-03); implement only if
  a real client needs them.
- **`/v1/embeddings`** — Phase 7.
- **Real token counting in `usage`** — Phase 7+ / whenever kiro-cli reports
  counts. Honest zeros for now.
- **Generic `SurfaceMount` extension to Phase 5 pool/session mounts** — D-01's
  list is the seam; `DELETE /v1/sessions/:id` (SESS-03) slots in as an OpenAI/
  shared `/v1` route in Phase 5.

### Reviewed Todos (not folded)

None — `todo.match-phase` returned `todo_count: 0`.

</deferred>

---

*Phase: 3-OpenAI Surface*
*Context gathered: 2026-05-24*
