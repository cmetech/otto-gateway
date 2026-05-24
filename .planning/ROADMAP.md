# Roadmap: Loop24 Gateway

## Overview

Loop24 Gateway is a from-scratch Go port of an existing Node.js Ollama
proxy, expanding the surface to also expose an OpenAI-compatible API on
the same port. The roadmap follows the M0–M9 milestone plan from
`docs/briefs/go_port_brief.md` §5, with M0 and M1 collapsed into a
single foundations phase. Each phase from Phase 2 onward delivers a
runnable, end-to-end vertical slice: Phase 2 is the first time a real
client gets a real response from `kiro-cli` through the gateway
(Ollama), Phase 3 brings the OpenAI surface online, and subsequent
phases layer streaming, the warm pool, tool calls, embeddings,
guardrails, and finally the cross-compile / CI distribution story. The
adapter-over-canonical layout (brief §3.13) and trust-gate suite (brief
§3.12) are established in Phase 1 and enforced from then on.

## Phases

**Phase Numbering:**

- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [x] **Phase 1: Foundations** - Scaffold, trust-gate suite, ACP JSON-RPC client over `kiro-cli` stdio (completed 2026-05-23)
- [x] **Phase 1.1: ACP Wire Alignment** *(INSERTED)* - Fix 10 Phase 1 wire-shape defects vs the working Node impl + live ACP spec; add real-kiro `session/prompt` round-trip integration test (completed 2026-05-23)
- [x] **Phase 2: Ollama End-to-End** - First runnable slice — LangFlow `POST /api/chat` reaches real `kiro-cli` (completed 2026-05-24)
- [ ] **Phase 3: OpenAI Surface** - Pi-SDK `POST /v1/chat/completions` shares the same canonical engine
- [x] **Phase 3.1: Anthropic Surface** *(INSERTED)* - loop24-client (GSD Pi) `POST /v1/messages` with Anthropic SSE shares the same canonical engine (completed 2026-05-24)
- [ ] **Phase 4: Streaming** - NDJSON (Ollama) and SSE (OpenAI + Anthropic) off one canonical chunk channel, with disconnect cancellation
- [ ] **Phase 5: Pool + Stateful Sessions** - Warm `POOL_SIZE` pool plus `X-Session-Id` registry, both visible on `/health/agents`
- [ ] **Phase 6: Tool-Call Path** - Canonical tool calls rendered per-surface, with `coerceToolCall` for plain-JSON-as-text
- [ ] **Phase 7: Embeddings** - Local BGE/E5 embeddings on three endpoints, independent of `kiro-cli`
- [ ] **Phase 8: Plugin Hook Chain** - `PreHook`/`PostHook` over canonical types, with RequestID, Auth, Logging registered
- [ ] **Phase 9: Distribution** - Cross-compile Linux+Windows from macOS, full trust-gate CI matrix gating merges

## Phase Details

### Phase 1: Foundations

**Goal:** A scaffolded Go project with the architectural boundaries, trust-gate tooling, and ACP JSON-RPC client in place so subsequent phases have a runnable skeleton plus a working `kiro-cli` subprocess client.
**Mode:** mvp
**Depends on:** Nothing (first phase)
**Requirements:** ACP-01, ACP-02, ACP-03, ACP-04, ACP-05, ACP-06, BLD-01, TRST-01, TRST-02, TRST-03, TRST-08
**Success Criteria** (what must be TRUE):

  1. `make build` produces a runnable host binary at `bin/loop24-gateway` that starts an HTTP server on `:11434` and serves `GET /health` returning empty pool/registry/embedding stats.
  2. `make lint` runs `golangci-lint` with the strict config (errcheck, errorlint, gosec, staticcheck, revive, wrapcheck, etc.) and passes with zero findings on the scaffold.
  3. `make test-race` runs `go test -race ./...` and passes; `govulncheck` runs clean in CI.
  4. A standalone integration test spawns `kiro-cli acp`, completes JSON-RPC `initialize` + `session/new`, sends a `ping`, auto-grants a `session/request_permission`, and translates a `session/update` into a typed chunk — all without leaking goroutines or hanging on subprocess exit.
  5. Pre-commit hooks (`gitleaks`, `golangci-lint`, `go mod tidy`) are installed and block bad commits locally.

**Plans:** 5/5 plans complete

Plans:
**Wave 1**

- [x] 01-01-PLAN.md — Walking skeleton: canonical+config+version+testutil packages, chi HTTP server, GET /health (D-12), GET /api/version, middleware chain, wrapper scripts, Makefile extensions

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 01-02-PLAN.md — ACP client core: framer+dispatcher+client+translate+stream, all unit and integration tests, goleak gate, main.go wired with acp.New

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 01-03-PLAN.md — Trust gates: go-arch-lint install (SUS checkpoint), .go-arch-lint.yml scaffold, make ci (lint+test-race+govulncheck), pre-commit run --all-files verification
- [x] 01-04-PLAN.md — Docs: docs/operating.md (PID/log locations, env overrides, status computation) + README Running section

### Phase 1.1: ACP Wire Alignment (INSERTED)

**Goal:** Bring the Phase 1 ACP JSON-RPC client into compliance with the live ACP spec and the working Node implementation, so Phase 2 can invoke `session/prompt` against real `kiro-cli` and consume `session/update` notifications without empty prompts, dropped chunks, or permission-grant deadlocks. The phase ships with a real-kiro `session/prompt` round-trip integration test as the verification gate that Phase 1's smoke test stopped short of.
**Mode:** mvp
**Depends on:** Phase 1
**Requirements:** ACP-01, ACP-02, ACP-03, ACP-04, ACP-05 (re-validation against the live spec — no NEW requirements added; this is a correctness/alignment fix). `canonical.ResourceLinkBlock` gains a required `Name` field per ACP spec; `canonical` adds the request/response types from Phase 2's discuss (D-08..D-10) — `canonical.ResourceLinkBlock.Name` is the only Phase-1.1-specific canonical addition needed for the wire-alignment work.
**Success Criteria** (what must be TRUE):

  1. Spec-compliant `initialize` request: `params` includes `protocolVersion: 1`, `clientInfo`, and `clientCapabilities` (with `fs.{readTextFile,writeTextFile}` + `terminal` flags). Response's `agentCapabilities.promptCapabilities` is captured and accessible to the caller for downstream image/audio/embedded-context gating.
  2. Spec-compliant `session/new`: params include `mcpServers: []`. Result reading handles both `sessionId` and `id` fallback. The `result.models.availableModels[]` array is extracted and surfaced as `[]canonical.ModelInfo{ID, Name}` accessible via the ACP client API (for Phase 2's `/api/tags` and `/api/show`).
  3. Spec-compliant `session/prompt`: params field name is `prompt` (with `content` alias sent defensively for older kiro-cli versions). Block wire shape matches the spec: text uses field `text` (not `content`); `resource_link` includes required `name`; `image` block construction is wired (used in Phase 2 for Ollama `images: [b64]` arrays). Result's `stopReason` is parsed and surfaced on `Stream.Result()` as a `canonical.StopReason` enum value.
  4. Spec-compliant `session/update` consumption: accepts `session/update` OR `session/notification` OR `_kiro.dev/session/update` method names; unwraps `params.update` defensively; reads `sessionUpdate` field (with `type` fallback); handles `agent_message_chunk`, `agent_thought_chunk`, `tool_call`, `tool_call_chunk`, `tool_call_update`, `plan` discriminators (plus CamelCase aliases like `AgentMessageChunk`); content extraction uses `body.content?.text ?? body.content ?? body.text` fallback chain.
  5. `session/request_permission` is handled as a REQUEST (responds to the original frame `id` with `{result:{optionId:"allow_always", granted:true}}`); the separate `session/grant_permission` request path from Phase 1 is removed.
  6. New integration test in `internal/acp/integration_test.go`: gated on real `kiro-cli` (D-17 pattern); spawns the subprocess, completes `Initialize → NewSession → Prompt("hi")`, drains `stream.Chunks`, asserts at least one `ChunkKindText` chunk arrives with non-empty content, asserts `Stream.Result()` returns with a non-error `StopReason` (typically `StopEndTurn`). `goleak.VerifyNone(t)` passes. **This is the verification gate that unblocks Phase 2.**

**Plans:** 5/5 plans complete

Plans:
**Wave 1**

- [x] 01.1-01-PLAN.md — Canonical types: add StopReason enum (D-02), ModelInfo struct (D-03), PromptCapabilities struct (D-03), Name field on ResourceLinkBlock (D-04). Leaf-package additions only; foundation for Plans 02-04.

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 01.1-02-PLAN.md — Initialize handshake + accessors: spec-compliant initializeParams (D-08), capture agentCapabilities.promptCapabilities (D-09), add stateMu + caps + models fields (D-06), PromptCapabilities() + AvailableModels() accessors (D-05). Paired with whitebox test that asserts the capture via the fake-conn pattern.

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 01.1-03-PLAN.md — Session/new + prompt wire shape + stop reason: mcpServers:[] (D-10), sessionId/id fallback (D-11), extract availableModels (D-12), promptParams Prompt+Content defensive duplicate (D-13), wireBlock new fields with resource_link Name path.Base fallback (D-14, D-04), parseStopReason helper + Stream.Result returns StopReason (D-02, D-07). Paired with whitebox tests for parseStopReason, translateBlock resource_link Name fallback, and Prompt round-trip surfacing StopReason via Stream.Result.

**Wave 4** *(blocked on Wave 3 completion)*

- [x] 01.1-04-PLAN.md — Notification parsing variance + permission RESPONSE + fake-server rewrite: three notification method names (D-16), tolerant sessionUpdateParams with json.RawMessage (D-17), content extraction fallback chain (D-18), normalizeUpdateType + new switch with spec-compliant discriminators (D-19), session/request_permission RESPONSE on original frame id + delete session/grant_permission send path (D-20). Paired with TestTranslateUpdate_VarianceMatrix (D-22), TestNormalizeUpdateType, rewritten fakeacp_test.go with spec-compliant shapes (D-23), consolidated TestIntegration_FakeACP_E2E_MixedVariants (D-23), updated TestAutoGrantPermission. Threat model included (defensive parsing of untrusted JSON from kiro-cli stdout + permission response correctness).

**Wave 5** *(blocked on Wave 4 completion — Phase 2 unblock gate)*

- [x] 01.1-05-PLAN.md — Real-kiro round-trip integration test (D-24): TestIntegration_RealKiroCLI_PromptRoundTrip runs Initialize → NewSession → Prompt(hi) → drain chunks → Result against real kiro-cli 2.4.1; asserts PromptCapabilities non-zero, AvailableModels non-empty, ≥1 ChunkKindText with non-empty content, StopReason is non-error (typically StopEndTurn). Includes a blocking human-verify checkpoint that confirms the test passed against the local kiro-cli — **this is the Phase 2 unblock signal**.

**Canonical ref:** `docs/reference/acp_wire_shapes.md` (created during Phase 2 discuss) is the authoritative spec for the 10 wire-shape defects and the target shapes.

### Phase 2: Ollama End-to-End

**Goal:** The first true end-to-end vertical slice — an existing LangFlow flow pointing at `http://localhost:11434/api/chat` reaches a real `kiro-cli` subprocess through the gateway and gets back a correct Ollama-shaped response. Establishes the canonical-engine / adapter pattern that every other surface phase builds on.
**Mode:** mvp
**Depends on:** Phase 1.1
**Requirements:** SURF-01, SURF-03, SURF-05, SURF-07, ACP-07, AUTH-01, AUTH-02, AUTH-03, OBSV-01, POOL-01, POOL-02, POOL-03
**Success Criteria** (what must be TRUE):

  1. `curl -X POST http://localhost:11434/api/chat -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'` returns an Ollama-compatible JSON response sourced from a real `kiro-cli` subprocess (non-streaming path, single canonical engine call).
  2. An existing LangFlow flow whose model component already points at `http://localhost:11434/api/chat` completes a chat invocation with zero reconfiguration.
  3. `GET /api/tags`, `POST /api/show`, `GET /api/ps`, `GET /api/version` return Ollama-compatible shapes; `POST /api/pull`/`push`/`create`/`copy` and `DELETE /api/delete` return success stubs.
  4. Bearer-token auth and IP-allowlist middleware reject unauthorized requests while exempting `/`, `/api/version`, and `/health`.
  5. Per-request `cwd` is derived from longest common parent of `resource_link` block URIs, with `KIRO_CWD` fallback and `X-Working-Dir` header override, verified by handler-level tests.

**Plans:** 6/6 plans complete

Plans:
**Wave 1** *(no shared files — run in parallel)*

- [x] 02-01-PLAN.md — Canonical chat types (D-08/D-09/D-10/D-11): ChatRequest, ChatResponse, Message, ContentPart, ToolCall, ToolSpec, Usage, MessageRole, ContentKind + Wave 0 test scaffold
- [x] 02-02-PLAN.md — internal/auth package: Bearer (constant-time compare) + IPAllowlist (netip + XFF + ::ffff: strip) middlewares + tests
- [x] 02-03-PLAN.md — config.go extensions: AuthToken, AllowedIPs, PoolSize, OllamaPathPrefix, OpenAIPathPrefix + getEnvStrSliceComma + getEnvInt + parseCIDRs

**Wave 2** *(depends on Wave 1)*

- [x] 02-04-PLAN.md — internal/engine package: ACPClient + Stream interfaces, PreHook/PostHook seam (D-04), Engine.Run + Engine.Collect, pickCwd (D-16 Windows-safe), buildBlocks (D-02), property + golden + Example tests + goleak gate

**Wave 3** *(depends on Wave 2)*

- [x] 02-05-PLAN.md — internal/pool package: channel-of-slots Pool satisfying engine.ACPClient (D-06), Warmup (D-07a fail-fast sequential), Models capture from first slot (D-13), Stats for /health, session→slot map with sync.Once-guarded slot release on stream close

**Wave 4** *(depends on Wave 3 — Phase 2 acceptance)*

- [x] 02-06-PLAN.md — Ollama adapter (wire + render + handlers + stubs + 8 unit-test files) + server wiring (chi sub-router + exempt routes per D-14/AUTH-03) + main.go (pool→engine→ollama→server with Warmup-before-listen per POOL-02) + wrapper scripts + real-kiro integration test + LangFlow zero-reconfig human-verify checkpoint

### Phase 3: OpenAI Surface

**Goal:** Bring a third adapter online on the same port, sharing the same canonical engine — Pi SDK with `base_url=http://localhost:11434/v1` completes an end-to-end chat against `kiro-cli` and gets back an OpenAI-compatible response. Validates that the adapter-over-canonical layout cleanly supports three surfaces (Ollama + Anthropic + OpenAI).
**Mode:** mvp
**Depends on:** Phase 3.1
**Requirements:** SURF-02, SURF-04, SURF-06
**Success Criteria** (what must be TRUE):

  1. `curl -X POST http://localhost:11434/v1/chat/completions -H 'Authorization: Bearer …' -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'` returns an OpenAI-compatible JSON response sourced from the same canonical engine that serves `/api/chat` and `/v1/messages`.
  2. A Pi-SDK CLI configured with `base_url=http://localhost:11434/v1` and a bearer token completes a chat round-trip without modification to the SDK.
  3. `GET /v1/models` and `POST /v1/completions` return OpenAI-compatible shapes; the model list at `/v1/models` and `/api/tags` reflect the same underlying set.
  4. `ENABLED_SURFACES` (introduced in Phase 3.1) extends to accept `openai`; default becomes `ollama,anthropic,openai` enabling all three. Setting `ENABLED_SURFACES=ollama` (or any subset omitting `openai`) at deploy time disables the OpenAI surface without code changes; `OPENAI_PATH_PREFIX` and `OLLAMA_PATH_PREFIX` are overridable.
  5. Architectural boundary check passes: `internal/adapter/openai`, `internal/adapter/ollama`, and `internal/adapter/anthropic` all import only `internal/canonical` + `internal/plugin`; none import `internal/engine`.

**Plans:** TBD

### Phase 3.1: Anthropic Surface (INSERTED)

**Goal:** Bring the second adapter online on the same port, sharing the same canonical engine — loop24-client (GSD Pi CLI) configured with `ANTHROPIC_BASE_URL=http://localhost:11434` completes an end-to-end Messages-API chat against `kiro-cli` and gets back an Anthropic-compatible response, **including SSE streaming day-one** because `@anthropic-ai/sdk`'s `messages.stream()` is the dominant call site in the client. This phase proves the adapter-over-canonical layout supports a second surface alongside Ollama and validates that streaming is a first-class concern for the Anthropic shape (it can't be deferred to Phase 4 the way Ollama streaming can). Phase 3 (OpenAI) will subsequently add the third surface.
**Mode:** mvp
**Depends on:** Phase 2 (canonical types locked + engine + auth middleware in place — sibling adapter to Phase 3, sequenced first because loop24-client is the dominant client and streaming is non-deferrable for the Anthropic shape)
**Requirements:** ANTH-01, ANTH-02, ANTH-03, ANTH-04, ANTH-05, ANTH-06, ANTH-07, SURF-08
**Success Criteria** (what must be TRUE):

  1. `curl -X POST http://localhost:11434/v1/messages -H 'x-api-key: …' -H 'anthropic-version: 2023-06-01' -d '{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}'` returns an Anthropic-compatible JSON response (top-level `id`, `type:"message"`, `role:"assistant"`, `content:[{type:"text",text:"…"}]`, `stop_reason`, `usage`) sourced from the same canonical engine that serves `/api/chat`.
  2. loop24-client (`../loop24-client`) configured with `ANTHROPIC_BASE_URL=http://localhost:11434` and a valid `ANTHROPIC_API_KEY` completes both a non-streaming `messages.create({stream:false})` AND a streaming `messages.stream({...})` round-trip end-to-end against the gateway, with zero changes to loop24-client code.
  3. Streaming emits the full Anthropic SSE event sequence: `message_start` → (`content_block_start` → `content_block_delta`+ → `content_block_stop`)+ → `message_delta` → `message_stop`, with `event:` and `data:` framing and `ping` keepalives every ~15s, all sourced from the same `<-chan canonical.Chunk` produced by `engine.Run(ctx, req)`.
  4. Tool-call requests round-trip: `tool_use` content blocks emitted in canonical shape and rendered with `input` as an object (Anthropic-style; OpenAI's JSON-string rendering lands in Phase 3); `tool_result` blocks accepted on the inbound side.
  5. `ENABLED_SURFACES` env var is introduced in this phase with default `ollama,anthropic` enabling both surfaces. Setting `ENABLED_SURFACES=ollama` (the Phase 2 default) disables the Anthropic adapter at boot; Phase 3 will subsequently extend the default to `ollama,anthropic,openai`. `internal/adapter/anthropic` imports only `internal/canonical` + `internal/plugin`; the `.go-arch-lint.yml` boundary check passes.
  6. Header contract enforced: missing `anthropic-version` returns a canonical `invalid_request_error` rendered in Anthropic's `{"type":"error","error":{"type":"…","message":"…"}}` shape; the gateway accepts both `x-api-key` and `Authorization: Bearer …` auth modes (loop24-client uses both depending on provider).

**Plans:** 4/4 plans complete

Plans:
**Wave 1**

- [x] 03.1-01-PLAN.md — Foundation: auth.Bearer dual-header (D-15), config ENABLED_SURFACES + ANTHROPIC_PATH_PREFIX (D-16/D-19), server.Config parallel Anthropic mount (D-17), engine.Collect thought aggregation (D-02), engine.buildBlocks [Reasoning] section (D-11), .go-arch-lint.yml adapter_anthropic + adapter_ollama + engine + pool boundaries

**Wave 2** *(blocked on Wave 1)*

- [x] 03.1-02-PLAN.md — Anthropic adapter non-streaming vertical slice: adapter.go + decode.go + errors.go + wire.go + render.go + handlers.go (non-streaming branch) + full whitebox test suite. Closes ANTH-01, ANTH-05, ANTH-06; partial ANTH-03 (tool_use object render), ANTH-04 (version header + beta accept-and-ignore), ANTH-07 (inbound thinking)

**Wave 3** *(blocked on Wave 2)*

- [x] 03.1-03-PLAN.md — SSE streaming vertical slice: sse.go state machine + select-loop + ping ticker + handler streaming branch + golden fixtures (sse_text_only, sse_text_then_thinking, sse_message_start). Closes ANTH-02; completes ANTH-07 outbound thinking via thinking_delta

**Wave 4** *(blocked on Wave 3 — Phase 3.1 acceptance)*

- [x] 03.1-04-PLAN.md — Integration: cmd/main.go wire anthropic adapter with ENABLED_SURFACES gating, integration_test.go real-kiro round-trip (stream + non-stream), HUMAN-UAT checkpoint against loop24-client `messages.stream()` and `messages.create({stream:false})`. Closes ANTH-04 + SURF-08 acceptance bars

### Phase 4: Streaming

**Goal:** Both surfaces stream by default off the same canonical chunk channel from the engine — Ollama emits NDJSON, OpenAI emits SSE with `data: [DONE]`. Client disconnect cancels the in-flight `session/prompt`. This is the high-fidelity behavior most clients actually use.
**Mode:** mvp
**Depends on:** Phase 3
**Requirements:** STRM-01, STRM-02, STRM-03, STRM-04, STRM-05
**Success Criteria** (what must be TRUE):

  1. `POST /api/chat` and `POST /api/generate` default to `stream: true` and emit `Content-Type: application/x-ndjson` with one JSON object per line; the final object has `done: true`.
  2. `POST /v1/chat/completions` defaults to streaming and emits `Content-Type: text/event-stream` with `data: ` prefixes and a terminating `data: [DONE]` frame.
  3. Both surfaces also honor `stream: false` and return a single JSON body (regression-tested against Phase 2/3 behavior).
  4. Killing the HTTP request mid-stream (canceling `r.Context()`) issues a `session/cancel` over JSON-RPC and the `kiro-cli` subprocess stops emitting chunks for that request without crashing the slot.
  5. Both adapters consume the same `chan canonical.ChatChunk` from `engine.Run(ctx, req)` — verified by reading the engine signature and adapter pump tests.

**Plans:** TBD

### Phase 5: Pool + Stateful Sessions

**Goal:** Replace the single-session engine with a real warm pool (`POOL_SIZE=4` default) and add stateful sessions keyed by `X-Session-Id` via a registry with idle reaping. Observable via `/health/agents`.
**Mode:** mvp
**Depends on:** Phase 4
**Requirements:** POOL-01, POOL-02, POOL-03, POOL-04, SESS-01, SESS-02, SESS-03, OBSV-02
**Success Criteria** (what must be TRUE):

  1. Pool warmup completes before `http.Server.ListenAndServe()` accepts connections; on cold boot, the second request after startup completes in roughly the same time as the tenth (no warmup latency on the user's first real call).
  2. Under N concurrent stateless `/api/chat` requests (N ≤ `POOL_SIZE`), each gets a distinct `kiro-cli` slot; with N > `POOL_SIZE`, excess callers block on `Acquire` and then proceed once a slot frees.
  3. Sending two requests with the same `X-Session-Id` header routes both to the same dedicated `kiro-cli` subprocess (verified by per-slot label in `/health/agents`); requests without the header use the warm pool.
  4. An idle session is reaped after `SESSION_TTL_MS` (default 30 min) — verified with a shortened TTL in a test — and `DELETE /v1/sessions/:id` immediately tears one down and returns `{deleted: "<id>"}`.
  5. `GET /health/agents` returns per-pool-slot detail (`alive`, `busy`, `label`) and per-session detail (`alive`, `last_used`); dead slots are detected and lazily re-spawned without blocking other acquires.

**Plans:** TBD

### Phase 6: Tool-Call Path

**Goal:** Tool calls flow correctly in both directions, in both surfaces' native shapes, including the `coerceToolCall` fallback for models that emit plain JSON (or markdown-fenced JSON) as text. This is the load-bearing LangChain-compat behavior from the Node reference.
**Mode:** mvp
**Depends on:** Phase 5
**Requirements:** TOOL-01, TOOL-02, TOOL-03
**Success Criteria** (what must be TRUE):

  1. A `/v1/chat/completions` request with `tools: [...]` that yields a tool-call from `kiro-cli` returns `choices[].message.tool_calls[].function.arguments` as a JSON **string** (OpenAI convention).
  2. A `/api/chat` request with `tools: [...]` that yields a tool-call returns `message.tool_calls[].function.arguments` as a plain **object** (Ollama convention).
  3. When `tools` is provided and the model returns bare JSON or markdown-fenced JSON as text, `coerceToolCall` converts it to a synthetic `tool_calls` entry — best tool selected via property-overlap scoring — and the surface adapter renders it in its native shape.
  4. Tool definitions from both request shapes (OpenAI `tools[].function`, Ollama tool spec) are normalized into one canonical tool spec consumed by the engine.
  5. Property tests (`pgregory.net/rapid` or `testing/quick`) cover `coerceToolCall` round-trip + never-panic invariants and the canonical-tool-spec translator for both surfaces.

**Plans:** TBD

### Phase 7: Embeddings

**Goal:** Local embedding endpoints serve BGE-Small EN-V1.5 (default) and gated additional models on three endpoints — `/api/embed`, `/api/embeddings`, `/v1/embeddings` — without ever calling `kiro-cli`. Embedding backend follows brief §3.4 Option C (out-of-process sidecar, provisional) unless plan-phase flips the decision.
**Mode:** mvp
**Depends on:** Phase 6
**Success Criteria** (what must be TRUE):

  1. `POST /api/embed` with `{"model":"bge-small-en-v1.5","input":"hello"}` (or `input: [...]`) returns one embedding per input; `POST /api/embeddings` with a single `prompt` returns a single flat vector (legacy shape).
  2. `POST /v1/embeddings` returns `{object:"list", data:[{object:"embedding", embedding:[…], index:N}]}` matching the OpenAI shape.
  3. BGE-Small EN-V1.5 is warmed at startup (visible in `/health` embedding stats); additional models gated by `EMBEDDING_MODELS_ENABLED` env var.
  4. Submitting more than `EMBEDDING_MAX_INPUTS` (default 2048) inputs in a single request returns HTTP 400.
  5. Tracing/log inspection confirms embedding requests never invoke `kiro-cli` — they are served by the local backend (sidecar process or in-process backend, per the §3.4 decision logged in PROJECT.md during plan-phase).

**Plans:** TBD

### Phase 8: Plugin Hook Chain

**Goal:** `PreHook` / `PostHook` interfaces operate on canonical request/response types, with day-one hooks registered: RequestID, Auth (refactored from middleware), and structured Logging. Short-circuit return from `PreHook` skips the engine. This establishes the seams for future guardrails (moderation, budget, schema, cache, audit) without rewriting handlers.
**Mode:** mvp
**Depends on:** Phase 7
**Requirements:** PLUG-01, PLUG-02, PLUG-03, PLUG-04, PLUG-05, OBSV-03
**Success Criteria** (what must be TRUE):

  1. The engine calls `chain.Pre(ctx, canonicalReq)` before any ACP call and `chain.Post(ctx, canonicalReq, canonicalResp)` after; a `PreHook` returning a non-nil `*canonical.ChatResponse` short-circuits the engine and the adapter renders that response in its native shape.
  2. `RequestIDHook` generates an `X-Request-Id` (or honors an inbound one) and that ID appears in every `slog` record across pre-hook, engine, ACP, and post-hook spans for the same request.
  3. `AuthHook` (Pre) validates bearer tokens from `AUTH_TOKEN` and short-circuits with a canonical auth-error response that the adapter renders correctly for both surfaces (OpenAI `{error:{...}}` vs Ollama `{error:"..."}`).
  4. `LoggingHook` emits a structured `Pre` log line on request entry and a `Post` log line on response with timing — both via `log/slog`.
  5. `ENABLED_HOOKS` env var enables/disables registered hooks at boot; hooks execute in registration order and the first non-nil short-circuit wins on the Pre chain.

**Plans:** TBD

### Phase 9: Distribution

**Goal:** A single static binary cross-compiles cleanly from macOS to `linux/amd64` and `windows/amd64`, with the full trust-gate suite running in CI on every PR + nightly on main. This is the headline value of the Go port — one binary, no cgo, no platform-specific build tooling.
**Mode:** mvp
**Depends on:** Phase 8
**Requirements:** BLD-02, BLD-03, BLD-04, TRST-04, TRST-05, TRST-06, TRST-07
**Success Criteria** (what must be TRUE):

  1. `make cross` on a macOS dev box produces `bin/loop24-gateway-linux-amd64` and `bin/loop24-gateway-windows-amd64.exe` from vanilla `go build` + `GOOS`/`GOARCH`, with `CGO_ENABLED=0` and `-ldflags="-s -w"`; both binaries are statically linked and ≤25 MB.
  2. Each binary embeds its version via `-ldflags="-X main.version=$VERSION"`; `curl /api/version` against either binary returns the embedded value.
  3. The CI pipeline runs (and gates merges on) `gofumpt -d` → `go vet` → `go build` → `golangci-lint run` → `govulncheck` → `go test -race ./...` → `go test -run Example` → property tests → cross-compile smoke; all stages block the next on failure.
  4. `go-arch-lint` (or equivalent) enforces in CI that `internal/adapter/*` does not import `internal/engine` and `internal/canonical` imports nothing under `internal/`.
  5. `goleak.VerifyTestMain` is wired into handler-level test packages; `Example_` functions document and validate `coerceToolCall`, `pickCwd`, and `buildAcpBlocks`.

**Plans:** TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 1.1 → 2 → 3 → 3.1 → 4 → 5 → 6 → 7 → 8 → 9

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundations | 5/5 | Complete   | 2026-05-23 |
| 1.1. ACP Wire Alignment (INSERTED) | 5/5 | Complete   | 2026-05-23 |
| 2. Ollama End-to-End | 6/6 | Complete   | 2026-05-24 |
| 3. OpenAI Surface | 0/TBD | Not started | - |
| 4. Streaming | 0/TBD | Not started | - |
| 5. Pool + Stateful Sessions | 0/TBD | Not started | - |
| 6. Tool-Call Path | 0/TBD | Not started | - |
| 7. Embeddings | 0/TBD | Not started | - |
| 8. Plugin Hook Chain | 0/TBD | Not started | - |
| 9. Distribution | 0/TBD | Not started | - |
