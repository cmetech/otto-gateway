# Phase 2: Ollama End-to-End - Context

**Gathered:** 2026-05-23
**Status:** Ready for planning — **but blocked on Phase 1.1 (ACP Wire Alignment) being inserted, planned, and executed first.**

<domain>
## Phase Boundary

Phase 2 delivers the first true end-to-end vertical slice — an existing
LangFlow flow pointing at `POST http://localhost:11434/api/chat` reaches
a real `kiro-cli` subprocess through the gateway and gets back a correct
Ollama-shaped response, sourced from a single canonical engine call. It
establishes the **adapter-over-canonical layout**, the
**`internal/engine` orchestrator with a Phase-8 hook seam**, the
**`internal/pool` warm-pool (size=1 default in Phase 2; Phase 5 bumps
to 4)**, the **forward-designed `canonical.ChatRequest`/`ChatResponse`
types**, and the **auth + IP allowlist HTTP middleware** that every
later surface phase reuses.

**Deliverables (per ROADMAP.md Phase 2 success criteria):**

1. `curl -X POST http://localhost:11434/api/chat -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'`
   returns an Ollama-compatible JSON response sourced from a real
   `kiro-cli` subprocess (non-streaming path, single canonical engine call).
2. An existing LangFlow flow whose model component already points at
   `http://localhost:11434/api/chat` completes a chat invocation with
   zero reconfiguration.
3. `GET /api/tags`, `POST /api/show`, `GET /api/ps`, `GET /api/version`
   return Ollama-compatible shapes; `POST /api/pull`/`push`/`create`/`copy`
   and `DELETE /api/delete` return success stubs.
4. Bearer-token auth and IP-allowlist middleware reject unauthorized
   requests while exempting `/`, `/api/version`, and `/health`.
5. Per-request `cwd` is derived from longest common parent of
   `resource_link` block URIs, with `KIRO_CWD` fallback and
   `X-Working-Dir` header override, verified by handler-level tests.

**Requirements covered:** SURF-01, SURF-03, SURF-05, SURF-07, ACP-07,
AUTH-01, AUTH-02, AUTH-03, OBSV-01. **Plus** (shifted from Phase 5):
POOL-01 (size=1 part), POOL-02 (warmup before listen), POOL-03 (channel
of free slots).

**Hard dependency on Phase 1.1:** The Phase 1 ACP wire-shape implementation
has 10 confirmed defects vs `kiro-cli` 2.4.1 — every byte of
`session/prompt` would arrive empty, every chunk would be silently
dropped, and `session/request_permission` would deadlock the subprocess.
Phase 1.1 (ACP Wire Alignment) closes the gap, adds a real-kiro
`session/prompt` round-trip integration test, and unblocks Phase 2. See
`docs/reference/acp_wire_shapes.md` for the full defect list.

**Explicitly NOT in Phase 2:**

- Streaming (NDJSON, SSE) — Phase 4. Phase 2's `/api/chat` only honors
  `stream:false`.
- OpenAI surface — Phase 3.
- Anthropic surface — Phase 3.1.
- Warm pool with `POOL_SIZE > 1` default, dead-slot detection, stateful
  sessions (`X-Session-Id`), `/health/agents` — Phase 5.
- Tool-call rendering (`tool_calls` in response messages), `coerceToolCall`
  fallback, tool spec normalization — Phase 6.
- Embedding endpoints — Phase 7.
- `PreHook`/`PostHook` chain implementations (RequestIDHook, AuthHook,
  LoggingHook) — Phase 8. (The engine has empty hook-slice fields as a
  Phase 2 deliverable per D-04, but no hook impls land.)
- Cross-compile CI matrix gating merges — Phase 9.

</domain>

<decisions>
## Implementation Decisions

### Engine surface (`internal/engine`)

- **D-01: Channel + Chat helper.** `engine.Run(ctx, *canonical.ChatRequest)
  (*engine.Run, error)` returns a handle with `Chunks <-chan canonical.Chunk`
  and `Result() (*canonical.ChatResponse, error)`. A thin
  `engine.Collect(ctx, req) (*canonical.ChatResponse, error)` helper in
  the engine package wraps `Run` and ranges Chunks to completion. Phase 2
  adapter calls `Collect` (non-streaming). Phase 4 SSE/NDJSON adapter
  ranges over `Chunks` directly. Mirrors Phase 1's `acp.Client.Prompt`
  → `*acp.Stream` pattern.

- **D-02: Block flattening inside engine package.** `engine.buildBlocks(*canonical.ChatRequest)
  []canonical.Block` is the single source of truth for the
  bracketed-section ACP text format (`[System]/[User]/[Assistant tool
  call: <name>]/[Tool result (id: ...)]/[Available tools]/[Reasoning]/[Output
  format]`). All three adapters (Phase 2 Ollama, Phase 3 OpenAI, Phase
  3.1 Anthropic) translate their native JSON to `canonical.ChatRequest`
  and let engine produce the ACP blocks. Forces canonical request type
  to normalize System / Tools / Think / Format scalars (see D-08).

- **D-03: Engine constructor — concrete struct + consumer-defined
  `engine.ACPClient` interface.** `engine.Engine` is a concrete struct.
  It depends on a local `engine.ACPClient` interface (NewSession +
  SetModel + Prompt + Cancel) that `*acp.Client` and `*pool.Pool` both
  satisfy structurally. Engine tests inject fake ACPClients. Phase 8
  adds `preHooks`/`postHooks` fields to the same struct. Constructor:
  `engine.New(engine.Config{Logger, ACP, ...})` — stdlib pattern, matches
  acp.Client and acp.NewWithConn from Phase 1.

- **D-04: Phase 8 hook chain seam present, empty in Phase 2.** Engine
  has `preHooks []PreHook` + `postHooks []PostHook` fields (empty in
  Phase 2). `PreHook` and `PostHook` interfaces are defined now (shape
  locked in PLUG-01..05 + PROJECT.md Key Decisions). `Run` ranges over
  both (no-op in Phase 2 since slices are empty); a `PreHook` returning
  non-nil response short-circuits the engine via the response-collected-from
  helper. Phase 8 just registers hook impls; engine.Run body unchanged.
  (Revised from initial "no seam" recommendation after user feedback —
  see `feedback_locked_design_seams` memory and `docs/reference/acp_wire_shapes.md`.)

- **D-05: Session lifecycle — new ACP session per Run.** Each `engine.Run`
  calls `acp.NewSession(ctx, cwd)` → optional `SetModel(ctx, sid,
  req.Model)` (skipped when `req.Model` is `""` or `"auto"`) → `Prompt(ctx,
  sid, blocks)` → stream chunks. `cwd` is derived per request via
  `engine.pickCwd(req)` — longest common parent of `resource_link`
  block URIs → `req.WorkingDirOverride` (from `X-Working-Dir` header)
  → `KIRO_CWD` env var → `os.Getwd()` ultimate fallback. Matches Node's
  stateless behavior; Phase 5 pool slot inherits the pattern.

### ACP concurrency model (`internal/pool`)

- **D-06: Pool-of-1 in `internal/pool` now.** Phase 2 creates the
  `internal/pool` package with size=1 default. `*pool.Pool` satisfies
  `engine.ACPClient` interface (routes NewSession / SetModel / Prompt /
  Cancel through an acquired slot). Channel-of-slots design per POOL-03.
  Phase 5 bumps the default to 4 and adds dead-slot detection (POOL-04)
  + session registry (SESS-*) + `/health/agents` (OBSV-02). Zero refactor
  of pool API between phases. (Revised from initial "wrapper" recommendation
  after user feedback — POOL-03 channel-of-slots is project-of-record per
  `feedback_locked_design_seams` rule.)

- **D-07: `POOL_SIZE` default = 1 in Phase 2.** Env var override still
  works (someone can set `POOL_SIZE=4` for concurrency testing). Cheap
  boot (one `kiro-cli` subprocess), modest memory. Phase 5 bumps default
  to 4. Matches roadmap framing of Phase 2 as "single-session" and Phase
  5 as "real warm pool."

- **D-07a: Fail-fast warmup, sequential.** `Pool.Warmup(ctx)` spawns +
  `Initialize` each slot sequentially. Any failure → close partially-built
  slots, return error, `main.go` exits non-zero. Phase 5 keeps fail-fast
  at warmup but adds lazy re-spawn for slots dying later (POOL-04).
  Sequential is fine for size=1; Phase 5 may switch to parallel
  (`errgroup`) when size=4 makes serial spawn visibly slow.

### Canonical types (`internal/canonical`)

- **D-08: `canonical.ChatRequest` is tri-surface forward-designed.**
  Fields: `Model string`, `System string`, `Messages []Message`, `Tools
  []ToolSpec`, `ToolChoice *ToolChoice`, `MaxTokens int`, `Temperature
  *float64`, `TopP *float64`, `StopSequences []string`, `Stream bool`,
  `Think bool`, `Format *Format`, `Metadata map[string]any`,
  `WorkingDirOverride string` (from `X-Working-Dir` header). Phase 2 only
  populates Model + System + Messages + Options-derived fields + WorkingDirOverride;
  the rest are zero-valued seams. Phase 3 / 3.1 / 4 / 6 activate dormant
  fields with zero canonical-type churn.

  > **D-08 footnote (2026-05-23 — Codex review H-2, user-directed):** ChatRequest
  > also gains `ResourceLinks []ResourceLinkBlock` as a Phase 2 forward-design
  > seam (zero-valued in Ollama; populated by Phase 3.1 Anthropic resource_link
  > blocks). This is necessary so `engine.pickCwd`'s longest-common-parent
  > derivation (D-16 step 2) has a sourceable field — the Phase 2 cwd-from-
  > resource_link unit test populates this field directly to satisfy SC #5.
  > Without it, SC #5 is structurally unsatisfiable because no `ContentPart`
  > kind carries resource_link URIs.

- **D-09: `canonical.Message` uses `[]ContentPart` from day one.**
  ```go
  type Message struct {
      Role       MessageRole    // system | user | assistant | tool
      Content    []ContentPart  // discriminated; single text = len=1
      ToolCalls  []ToolCall     // assistant with tool calls
      ToolCallID string         // role=tool result
  }

  type ContentPart struct {
      Kind       ContentKind  // text | image | tool_use | tool_result | thinking
      Text       string
      Image      *ImagePart
      ToolUse    *ToolUsePart
      ToolResult *ToolResultPart
  }
  ```
  Phase 2 only populates `Kind == Text` (single part) and `Kind == Image`
  (from Ollama `messages[].images: [b64]`). Other kinds wait for Phase
  3.1 / 6. Single-package — `internal/canonical` imports nothing under
  `internal/` (D-04 from Phase 1 still holds).

  > **D-09 footnote (2026-05-23 — Codex review M-1, user-directed):** Phase 2
  > also extends `canonical.Block` with `BlockKindImage` plus an
  > `ImageBlock{ Source, MIMEType, Data }` variant, and the engine's
  > `buildBlocks` emits a `BlockKindImage` block for every
  > `ContentKindImage` part it encounters. Without this, Ollama
  > `messages[].images: [b64]` would round-trip through canonical only to be
  > silently dropped at the ACP block boundary. The Ollama adapter
  > base64-decodes the wire image and populates `ContentPart{Kind:
  > ContentKindImage, Image: &ImagePart{...}}`.

- **D-10: `canonical.ChatResponse` is tri-surface forward-designed,
  symmetric to ChatRequest.** Fields: `ID string`, `Model string`,
  `Message Message` (role=assistant), `StopReason StopReason` (enum:
  `StopEndTurn` / `StopMaxTokens` / `StopSequenceMatch` / `StopToolUse`
  / `StopError`), `Usage Usage` (InputTokens, OutputTokens,
  CacheCreationInputTokens, CacheReadInputTokens). Phase 2 populates ID
  + Model + Message.Content[text] + StopReason (mapped from
  `session/prompt` response's `stopReason` field per Phase 1.1).

- **D-11: No JSON tags on `canonical` types.** Canonical is wire-agnostic.
  All wire-format translation lives in adapters (`internal/adapter/<surface>/wire.go`).
  When you see a JSON tag in this codebase, you're in an adapter or
  ACP-wire helper — never in canonical. Tests use cmp.Diff or hand-rolled
  assertions, not JSON round-trip. Matches Bifrost's discipline.

### HTTP surface (`internal/server`, `internal/adapter/ollama`)

- **D-12: `/api/version` moves from `internal/server` to
  `internal/adapter/ollama`** per Phase 1 D-11. Ollama-shape JSON:
  `{version: "<embedded>", commit: "<vcs.revision>"}`. Phase 2 also adds:
  `/api/chat` (POST), `/api/generate` (POST, single-turn), `/api/tags`
  (GET), `/api/show` (POST), `/api/ps` (GET), plus success stubs for
  `/api/pull` `/api/push` `/api/create` `/api/copy` `DELETE /api/delete`.

- **D-13: Model catalog source — capture once at pool warmup.** During
  `pool.Warmup(ctx)`, the first slot's `session/new` response yields
  `result.models.availableModels[]` (per Phase 1.1 wire-alignment work).
  Pool stores the resulting `[]ModelInfo{ID, Name}` once — same per
  `kiro-cli` install. `/api/tags` returns it in Ollama shape with sensible
  defaults for the metadata kiro doesn't provide (`size:0`, `digest:"sha256:placeholder"`,
  `modified_at: now`, `details:{family:"kiro", parameter_size:"unknown"}`).
  `/api/show` looks up by name; returns the same shape plus stub
  `template`/`parameters`/`license`/`modelfile`. Refresh requires gateway
  restart.

- **D-14: Auth + IP allowlist — exempt-route subrouter (Claude's discretion
  on exact chi composition).** Two chi middlewares: `auth.Bearer(token)`
  validates `AUTH_TOKEN` (per Node defaults — empty token means no auth);
  `auth.IPAllowlist(cidrs)` validates `ALLOWED_IPS` (empty means
  allow-all). Apply via a chi sub-router mounted at `/api` + `/v1`, with
  the exempt paths (`/`, `/api/version`, `/health`) registered on the
  outer router. Order: `RequestID` → `Recoverer` → `accessLog` →
  `(Auth + IPAllowlist)` on the inner router. AccessLog runs BEFORE auth
  so denied requests are still logged. Phase 8 will later refactor auth
  out of HTTP middleware into the engine hook chain (AuthHook); for now
  auth lives at the HTTP layer.

- **D-15: Stub endpoints return Ollama-shape success responses** (not
  empty bodies or 501). Most stubs return `{status: "success"}`. The
  Node reference's exact shapes are the parity target — planner verifies
  with `curl` against the running Node version where ambiguous. LangFlow
  may dispatch `/api/pull` during model setup; success-shape parity is
  load-bearing for SURF-05 (zero reconfig).

- **D-16: Per-request cwd derivation — strict port from Node reference
  semantics, idiomatic Go implementation.** Algorithm: (1) if
  `X-Working-Dir` header is set, use it verbatim; (2) else collect all
  `resource_link.URI` values from canonical.ChatRequest, parse with
  `net/url`, keep only `file://` URIs, find longest common parent via
  `filepath.Dir` walk; (3) else fall back to `KIRO_CWD` env var; (4)
  else `os.Getwd()`. Lives in `internal/engine/pickcwd.go` as a pure
  function with property tests. Tests cover: no URIs, mixed file://+http://,
  Windows-style paths under `file:///C:/...`, single URI, multi-URI
  divergent paths.

### Claude's Discretion

The planner has latitude on:

- Exact chi sub-router composition for D-14 (multiple valid chi
  patterns exist).
- Exact Ollama metadata default values in D-13 (`modified_at`, `digest`,
  `size`, `family`) — match Node defaults where Node provides them,
  otherwise pick sane stubs.
- Whether `internal/pool.Config` is `Config` or `*Config` parameter type
  — pick one and be consistent.
- Whether `engine.ChatResponse` assembly lives in `engine/assemble.go`
  or inline in `engine/collect.go`.
- Test package strategy per file (whitebox vs blackbox) — D-18 from
  Phase 1 generalizes: integration tests blackbox, unit tests whitebox.
- Error message wording, log key naming (consistent with Phase 1's
  conventions).
- The exact 5-tuple HTTP status codes returned for various error
  conditions — Phase 2's adapter renders errors in Ollama's `{"error":
  "..."}` shape; HTTP codes follow Ollama's conventions.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### ACP wire shapes (load-bearing — read FIRST)

- `docs/reference/acp_wire_shapes.md` — **MANDATORY.** Authoritative
  ground-truth reference for ACP JSON-RPC wire shapes. Documents 10
  Phase 1 defects to fix in Phase 1.1 plus the spec-compliant shapes
  for `initialize`, `session/new`, `session/prompt`, `session/cancel`,
  every `session/update` notification variant, and the
  `session/request_permission` request/response pattern. Created during
  Phase 2 discuss after wire-shape gap was discovered.
- `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-ollama-server.js`
  — The Node implementation that is proven working against `kiro-cli`
  2.4.1. Lines 217–254 contain the canonical session/update dispatch
  logic; lines 257–267 contain the correct initialize params; lines
  296–303 contain the correct session/prompt params with `prompt`+`content`
  defensive duplication.
- `docs/reference/acp_server_node_reference.md` — Narrative reference
  for the Node implementation. **CAUTION:** parts of this doc are
  generic/imprecise (it described `session/update` types as
  `text`/`thought`/`tool_call`/`plan` which is what Phase 1 implemented,
  but the working Node CODE actually accepts the spec-compliant
  `agent_message_chunk`/`agent_thought_chunk`/etc. types). Use this doc
  for high-level behavior (request lifecycle, `buildAcpBlocks` semantics,
  `coerceToolCall` rationale, pool/registry behavior) — but trust the
  Node source code over this doc for wire-format specifics.
- https://agentclientprotocol.com/protocol/initialization.md — ACP spec
  for the `initialize` handshake including `protocolVersion`,
  `clientCapabilities`, and the `agentCapabilities.promptCapabilities`
  flags (`image`, `audio`, `embeddedContext`).
- https://agentclientprotocol.com/protocol/content.md — ACP spec for
  ContentBlock shapes (`text`, `image`, `audio`, `resource`,
  `resource_link`) with required/optional fields per variant.
- https://agentclientprotocol.com/protocol/prompt-turn.md — ACP spec
  for `session/prompt` and the notification turn lifecycle including
  the `stopReason` enum (`end_turn`/`max_tokens`/`max_turn_requests`/`refusal`/`cancelled`).
- https://kiro.dev/docs/cli/acp/ — Kiro CLI's per-CLI documentation.
  **CAUTION:** contradicts the upstream spec on notification method name
  (says `session/notification` vs spec's `session/update`) and casing
  (CamelCase vs snake_case discriminators). Treat as informational only;
  the Node source's defensive parsing (`session/update` OR
  `session/notification` OR `_kiro.dev/session/update`) is the right
  pattern.

### Spec of record (must-read)

- `docs/briefs/go_port_brief.md` — full design brief. Sections especially
  relevant to Phase 2:
  - §3.4: Embeddings backend (informs why Phase 7 is later).
  - §3.8: Architectural layer invariants (drives TRST-04 ruleset — Phase
    2 activates the `go-arch-lint` rules scaffolded in Phase 1).
  - §3.12: Trust gates — non-negotiable; warnings are CI hard failures.
  - §3.13: **Adapter-over-canonical layout** (`internal/adapter/{ollama,openai,anthropic}`
    ↔ `internal/canonical` ↔ `internal/engine`) — the load-bearing
    architecture this phase establishes.
  - §5: M0–M9 milestone plan. Phase 2 corresponds to M2 + part of M3.

### Planning context (must-read)

- `.planning/PROJECT.md` — Loop24 Gateway project overview, constraints,
  Key Decisions table.
- `.planning/REQUIREMENTS.md` — v1 requirements. Phase 2 covers SURF-01,
  SURF-03, SURF-05, SURF-07, ACP-07, AUTH-01..03, OBSV-01, plus POOL-01
  (size=1 part) + POOL-02 + POOL-03 (shifted from Phase 5).
- `.planning/ROADMAP.md` §"Phase 2: Ollama End-to-End" — phase goal,
  mode (mvp), depends-on, success criteria. **Note: Phase 2's
  `Depends on` must be updated to "Phase 1.1" after the roadmap edit.**
- `.planning/STATE.md` — current project state.
- `.planning/phases/01-foundations/01-CONTEXT.md` — Phase 1 context with
  all D-01..D-23 implementation decisions still in force.

### Reference architecture (read as needed)

- `../bifrost/transports/bifrost-http/integrations/openai/main.go` —
  Bifrost's OpenAI integration package. Validates the adapter-over-canonical
  layout for HTTP surface; their `requestParser` + `responseGenerator`
  pattern maps cleanly to our `adapter/<surface>/{wire.go, handlers.go}`.
- `../bifrost/core/providers/ollama/ollama.go` — Bifrost's Ollama
  provider for shape parity reference, especially the `/api/tags` and
  `/api/chat` response struct field tags.

### Phase 2 wire-format external (look up via researcher, not local files)

- Ollama API reference (current): `/api/chat`, `/api/generate`, `/api/tags`,
  `/api/show`, `/api/ps`, `/api/version` request and response shapes.
- chi router middleware composition for sub-router with selective
  middleware application (the `chi.Router.With()` + `chi.Router.Group()`
  patterns).
- `net/url` + `path/filepath` idioms for the longest-common-parent
  computation in `pickCwd`.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- `internal/acp/` — full ACP JSON-RPC client from Phase 1 (`*acp.Client`,
  `acp.New`, `acp.NewWithConn`, `Stream` with `Chunks <-chan canonical.Chunk`).
  **CAUTION:** Phase 1.1 will fix 10 wire-shape defects in this package
  before Phase 2 consumes it. Phase 2's pool wraps the post-1.5 ACP
  client.
- `internal/canonical/chunk.go` — `Chunk`, `Block`, `TextChunk`,
  `ThoughtChunk`, `ToolCallChunk`, `PlanChunk`, `TextBlock`,
  `ResourceLinkBlock` types are in place. Phase 2 ADDS `ChatRequest`,
  `ChatResponse`, `Message`, `ContentPart`, `ToolCall`, `ToolSpec`,
  `ToolChoice`, `Format`, `Usage`, `MessageRole`, `ContentKind`,
  `StopReason` per D-08, D-09, D-10. **`canonical.ResourceLinkBlock`
  needs a `Name string` field added** per Phase 1.1 wire alignment (spec
  says `name` is REQUIRED).
- `internal/config/config.go` — `Config.Load()` already reads env vars
  via the `getEnvStr/getEnvBool/getEnvInt/getEnvDuration/getEnvStrSlice`
  helpers (D-14 from Phase 1). Phase 2 extends with: `AuthToken []string`
  (from `AUTH_TOKEN`, comma-split), `AllowedIPs []string` (from
  `ALLOWED_IPS`, comma-split), `PoolSize int` (from `POOL_SIZE`, default
  1), `OllamaPathPrefix string` (from `OLLAMA_PATH_PREFIX`, default
  `/api`), `OpenAIPathPrefix string` (from `OPENAI_PATH_PREFIX`, default
  `/v1`, **unused in Phase 2** but env-readable for forward design).
- `internal/server/server.go` — chi router constructor + middleware
  chain (`RequestID` + `Recoverer` + custom `accessLog`) from Phase 1.
  Phase 2 ADDS the auth + IP allowlist middlewares and mounts the
  Ollama adapter's router under `/api`.
- `internal/server/health.go` — `HealthResponse`/`PoolStats`/`SessionStats`/`EmbeddingStats`
  Go types and the `/health` handler. Phase 2 wires `PoolStats` from the
  new `internal/pool.Pool.Stats()` method (`size`, `alive`, `busy`).
- `internal/version/version.go` — `Version` const populated via
  `-ldflags` at build time. Phase 2 uses for the `/api/version` shape.
- `internal/testutil/testutil.go` — `Logger(t)` helper from Phase 1.
  Reused for engine + pool + adapter tests.
- Wrapper scripts (`scripts/loop24`, `scripts/loop24.ps1`) — already
  pass through `KIRO_CMD`/`KIRO_ARGS`/`DEBUG`/`HTTP_ADDR` env vars.
  Phase 2 ADDS `AUTH_TOKEN`/`ALLOWED_IPS`/`POOL_SIZE`/`KIRO_CWD` to the
  pass-through list.

### Established Patterns

- **Adapter-over-canonical layout** — locked by PROJECT.md + brief §3.13
  + Phase 1 PATTERNS.md. `internal/adapter/ollama` translates
  Ollama-wire JSON ↔ `canonical.ChatRequest`/`ChatResponse`. Imports only
  `internal/canonical` + `internal/plugin` (Phase 8). Never imports
  `internal/engine` — TRST-04 enforced.
- **Single-package per concern with file-scoped layers** — Phase 1 D-01
  established this for `internal/acp`. Phase 2 follows for
  `internal/engine` (`engine.go`, `build_acp.go`, `pickcwd.go`,
  `collect.go`, `hooks.go`), `internal/pool` (`pool.go`, `config.go`,
  `stats.go`), `internal/adapter/ollama` (`wire.go`, `handlers.go`,
  `render.go`, `stub.go`).
- **`context.Context`-first cancellation** — Phase 1 D-02. Every
  engine/pool exported method takes `ctx` as first arg.
- **Config-struct constructors** — Phase 1 D-05. `engine.New(engine.Config{...})`,
  `pool.New(pool.Config{...})`, `auth.New(auth.Config{...})`.
- **`*slog.Logger` propagation via Config** — Phase 1 D-15. No global
  `slog.SetDefault`. Engine, pool, and adapters all take logger via
  their Config.
- **Per-Node env-var contract** — `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`,
  `POOL_SIZE`, `AUTH_TOKEN`, `ALLOWED_IPS`, `DEBUG`. New: `OLLAMA_PATH_PREFIX`,
  `OPENAI_PATH_PREFIX`. All defaults match Node version.

### Integration Points

- `cmd/loop24-gateway/main.go` becomes:
  ```go
  cfg := mustLoadConfig()
  logger := buildLogger(cfg)
  pool, _ := mustNewPool(cfg, logger)        // size 1 default; warmup-before-listen
  defer pool.Close()
  engine := engine.New(engine.Config{Logger: logger, ACP: pool})
  ollama := ollama.New(ollama.Config{Logger: logger, Engine: engine, ModelCatalog: pool})
  srv := server.New(server.Config{Logger: logger, OllamaRouter: ollama.Router(), AuthToken: cfg.AuthToken, AllowedIPs: cfg.AllowedIPs, ...})
  srv.RunUntilSignal(ctx)                    // graceful shutdown → engine.Close → pool.Close
  ```
- `internal/server.New(...)` constructs the chi router; mounts the
  Ollama router under `OLLAMA_PATH_PREFIX` (default `/api`); applies
  auth + IP allowlist as middleware on the sub-router; keeps `/`,
  `/api/version`, `/health` on the outer router for exemption.
- `internal/engine.New(...)` takes an `engine.ACPClient` (the pool
  satisfies this); exposes `Run` + `Collect`.
- `internal/pool.New(...)` constructs the pool; `pool.Warmup(ctx)`
  spawns + initialize + session/new (to capture availableModels) for
  each slot; `pool.Models()` returns the cached catalog;
  `pool.NewSession/SetModel/Prompt/Cancel` route through an acquired
  slot.
- `internal/adapter/ollama.New(...)` constructs the adapter's chi
  sub-router with `/api/chat`, `/api/generate`, `/api/tags`, `/api/show`,
  `/api/ps`, `/api/version`, plus the stub endpoints. Handlers translate
  Ollama-wire JSON → `canonical.ChatRequest`, call `engine.Collect`,
  translate `canonical.ChatResponse` → Ollama-wire JSON.

</code_context>

<specifics>
## Specific Ideas

- **`kiro-cli` 2.4.1 is the ground-truth target.** Symlinked from
  `/Applications/Kiro CLI.app/Contents/MacOS/kiro-cli` to
  `~/.local/bin/kiro-cli`. The Node implementation in
  `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-ollama-server.js`
  is proven working against this version. Where the live ACP spec and
  Node code differ on a wire shape, **the Node code wins for Phase 2**
  because it's empirically validated.
- **The Node code's defensive triple-acceptance of notification methods
  (`session/update` OR `session/notification` OR `_kiro.dev/session/update`)
  is intentional** and should be preserved in our Go port. Same for the
  defensive `prompt`+`content` duplication in `session/prompt` params
  and the `body.content?.text ?? body.content ?? body.text` fallback
  chain in content extraction. kiro-cli versions vary in wire shape;
  defensive parsing absorbs the variance.
- **The fake-server integration test pattern was load-bearing wrong in
  Phase 1.** Our fake emitted what our code expected — implementation-implementation
  symmetry, not spec compliance. Phase 1.1 fixes the fake and adds a
  real-kiro `session/prompt` round-trip test. Phase 2's adapter-handler
  tests should use a fake engine (controllable `chan canonical.Chunk`),
  not a fake ACP — engine is the seam that the adapter trusts.
- **LangFlow's call pattern is the SURF-05 acceptance bar.** When in
  doubt about Ollama-shape fidelity (e.g., `/api/tags` metadata
  defaults), run LangFlow against the Node version first, then verify
  our Go version matches. The user has LangFlow running locally.
- **Pi-SDK on `/v1/chat/completions` is Phase 3** — Phase 2 does not
  need to support OpenAI's shape. But `OPENAI_PATH_PREFIX` and the
  ENABLED_SURFACES env var should be readable in Phase 2 config so
  Phase 3 doesn't need to touch the config loader.

</specifics>

<deferred>
## Deferred Ideas

- **Streaming for `/api/chat`** — Phase 4. Phase 2 only honors
  `stream:false`; clients sending `stream:true` get a 4xx error or
  silent downgrade (planner picks; Node version silently downgrades).
- **Dead-slot detection + lazy re-spawn** — Phase 5 (POOL-04). Phase 2's
  pool fail-fasts at warmup; if a slot dies later, the request blocks
  forever on the channel. Acceptable for Phase 2's size=1; Phase 5
  fixes for multi-slot.
- **Parallel warmup** — Phase 5. Sequential is fine for size=1.
- **Stateful sessions via `X-Session-Id`** — Phase 5 (SESS-*). Phase 2
  doesn't read this header. The `internal/session` package doesn't
  exist yet.
- **`/health/agents`** — Phase 5 (OBSV-02). Phase 2 has `/health` with
  pool size/alive/busy counts via the existing `PoolStats` struct.
- **`coerceToolCall`** — Phase 6 (TOOL-02). Phase 2 doesn't run it.
- **Tool-call rendering in responses** — Phase 6 (TOOL-01). Phase 2's
  ChatResponse always has `Message.ToolCalls == nil`.
- **Tool spec normalization** — Phase 6 (TOOL-03). Phase 2's
  ChatRequest carries `Tools []ToolSpec` but it's always empty.
- **`AuthHook` (engine plugin)** — Phase 8 (PLUG-04). Phase 2 puts auth
  in HTTP middleware; Phase 8 refactors into the engine hook chain.
- **Property tests for `pickCwd`** (TRST-06) — happens incrementally
  starting Phase 2 (planner judgment); brief intent is "as relevant
  functions land."
- **`Example_` documentation functions** (TRST-07) — added for
  `pickCwd`, `buildAcpBlocks`, `coerceToolCall` as they land. Phase 2
  adds for `pickCwd` and `buildAcpBlocks`; Phase 6 adds for
  `coerceToolCall`.
- **OpenAI surface enabling/disabling at boot** — `ENABLED_SURFACES`
  env var (SURF-02). Phase 2 reads but only honors the `ollama` part;
  Phase 3 honors `openai`; Phase 3.1 honors `anthropic`.

</deferred>

---

*Phase: 2-Ollama End-to-End*
*Context gathered: 2026-05-23*
