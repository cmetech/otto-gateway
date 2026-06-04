# Roadmap: OTTO Gateway

## Overview

OTTO Gateway is a from-scratch Go port of an existing Node.js Ollama
proxy, expanding the surface to also expose an OpenAI-compatible API on
the same port. The roadmap follows the M0–M9 milestone plan from
`docs/briefs/go_port_brief.md` §5, with M0 and M1 collapsed into a
single foundations phase. Each phase from Phase 2 onward delivers a
runnable, end-to-end vertical slice: Phase 2 is the first time a real
client gets a real response from `kiro-cli` through the gateway
(Ollama), Phase 3 brings the OpenAI surface online, and subsequent
phases layer streaming, the warm pool, tool calls,
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
- [x] **Phase 3: OpenAI Surface** - Pi-SDK `POST /v1/chat/completions` shares the same canonical engine (completed 2026-05-25)
- [x] **Phase 3.1: Anthropic Surface** *(INSERTED)* - loop24-client (GSD Pi) `POST /v1/messages` with Anthropic SSE shares the same canonical engine (completed 2026-05-24)
- [x] **Phase 4: Streaming** - NDJSON (Ollama) and SSE (OpenAI + Anthropic) off one canonical chunk channel, with disconnect cancellation (completed 2026-05-25)
- [x] **Phase 5: Pool + Stateful Sessions** - Warm `POOL_SIZE` pool plus `X-Session-Id` registry, both visible on `/health/agents` (plans 3/3 shipped 2026-05-26; verification gaps_found — gap-closure plans 05-04 (SC3 root-cause + fix) + 05-05 (PHASE5-PERF.md skeleton + manual gates) appended 2026-05-26) (completed 2026-05-26)
- [x] **Phase 6: Tool-Call Path** - Canonical tool calls rendered per-surface, with `coerceToolCall` for plain-JSON-as-text (completed 2026-05-27)
- [x] **Phase 6.1: Admin Observability UI** *(INSERTED)* - Dark-mode `/admin` page rendering `/health` + `/health/agents` with the OTTO brand palette; auto-refresh polling; nice-to-have live log tail (completed 2026-05-28)
- [x] **Phase 8: Plugin Hook Chain** - `PreHook`/`PostHook` over canonical types, with RequestID, Auth, Logging registered (completed 2026-05-28)
- [x] **Phase 8.2: Ollama `format` Parity** *(INSERTED)* - LangFlow `format:"json"` / `format:<schema>` requests are steered via a canonical `PreHook` (GEN_RULES block) and the response is fence-stripped before render — Node-shim parity for the v1 replacement goal (completed 2026-06-03)
- [x] **Phase 8.3: ACP Prompt() Non-Blocking Refactor** *(INSERTED)* - `acp.Client.Prompt()` blocks until the final `session/prompt` response arrives, but `engine.Run` calls it synchronously and the 64-slot chunk buffer overflows on any non-trivial response — gateway deadlocks with worker still streaming. Refactor `Prompt()` to return the `*Stream` as soon as the request is accepted; move final-response handling into a goroutine that finalizes via `stream.close()`. `Stream.Result()` becomes the new sync point. (completed 2026-06-03)
- [x] **Phase 9: Distribution** - Cross-compile Linux+Windows from macOS, full trust-gate CI matrix gating merges (completed 2026-05-28)

## Phase Details

### Phase 1: Foundations

**Goal:** A scaffolded Go project with the architectural boundaries, trust-gate tooling, and ACP JSON-RPC client in place so subsequent phases have a runnable skeleton plus a working `kiro-cli` subprocess client.
**Mode:** mvp
**Depends on:** Nothing (first phase)
**Requirements:** ACP-01, ACP-02, ACP-03, ACP-04, ACP-05, ACP-06, BLD-01, TRST-01, TRST-02, TRST-03, TRST-08
**Success Criteria** (what must be TRUE):

  1. `make build` produces a runnable host binary at `bin/otto-gateway` that starts an HTTP server on `:11434` and serves `GET /health` returning empty pool/registry/embedding stats.
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

**Plans:** 4/4 plans complete

Plans:
**Wave 1**

- [x] 03-01-PLAN.md — Foundation + reachability seam: config D-05 (ENABLED_SURFACES widen + validateEnabledSurfaces), server D-01 SurfaceMount refactor + co-mount regression test, openai adapter skeleton (interfaces + RegisterRoutes + decode.go + goleak testmain), .go-arch-lint.yml adapter_openai boundary

**Wave 2** *(blocked on Wave 1)*

- [x] 03-02-PLAN.md — Thinnest end-to-end slice (SC1 + SC2/Pi): chat/completions wire decode + render + flat SSE emitter + error envelope + handler (stream + non-stream) + golden fixtures + httptest round-trip

**Wave 3** *(blocked on Wave 2 — shares handlers/render/wire)*

- [x] 03-03-PLAN.md — /v1/models (pool catalog, SC3) + /v1/completions legacy shim (D-03)

**Wave 4** *(blocked on Waves 1-3 — Phase 3 acceptance)*

- [x] 03-04-PLAN.md — Integration: main.go ENABLED_SURFACES gating + engine bridge + SurfaceMount list wiring, real-kiro round-trip (stream + non-stream), make ci/arch-lint gate (SC5), Pi-SDK HUMAN-UAT (SC2/SURF-06)

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

**Plans:** 6/6 plans complete

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

**Plans:** 4/4 plans complete

Plans:
**Wave 1**

- [x] 04-01-PLAN.md — Atomic watchdog + all-surface teardown: engine.Run AfterFunc watchdog + StopWatchdog accessor, collect.go natural-completion stop(), ollama wire.go *bool + streamEnabled, ALL three RunHandle interface extensions + stop() call sites in finalizeSSE/finalizeStream, D-07 derived ctx in openai/anthropic handlers, ollamaEngineAdapter + ollamaRunHandleAdapter shims in main.go, openai/anthropic RunHandleAdapter StopWatchdog() — zero spurious-cancel window from first watchdog commit

**Wave 2** *(blocked on Wave 1 completion — plans run in parallel)*

- [x] 04-02-PLAN.md — Ollama NDJSON streaming: goleak gate (testmain_test.go), ndjson.go emitter (cancelFn + StopWatchdog teardown), handlers.go streaming branch, Chat_Streaming + Generate_Streaming + Chat_DisconnectSmoke E2E (pool-alive health-check assertion for slot-survival)
- [x] 04-03-PLAN.md — Watchdog correctness tests: engine watchdog unit tests (channel-based CancelOnCtxDone + stop-then-cancel StopPreventsCancel), ACP cancel frame integration test (cancelSeen channel in fakeACPServer)

**Wave 3** *(blocked on Waves 1-2 completion — Phase 4 acceptance)*

- [x] 04-04-PLAN.md — Ratification E2E: OpenAI SSE streaming + non-streaming regression, Anthropic SSE streaming + non-streaming regression; documents bool-default-streaming semantics for both surfaces and Anthropic D-05 event:error exemption

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

**Plans:** 5/5 plans complete

Plans:
**Wave 1**

- [x] 05-01-PLAN.md — Slice A: Pool dead-slot detection — acp.Client.Done() push-exit signal, per-slot exit-watcher goroutine, lazy synchronous re-spawn at Pool.NewSession (D-01/D-02), pool-shrink on respawn failure (D-03), Pool.Detail() per-slot rows for /health/agents (D-15), POOL_SIZE env default flip to 4 (POOL-01 Node parity). Closes POOL-01..04.

**Wave 2** *(blocked on Wave 1 — config.go overlap)*

- [x] 05-02-PLAN.md — Slice B: Session registry + reaper — new internal/session package (Registry + Entry + per-entry sync.Mutex), goleak gate from day one (Wave 0), Get with Pitfall-4 race resolution + SESSION_MAX=32 cap (D-04/D-05/D-06), Delete with Codex M-3 map-delete-first (D-08), SetModel diff-skip (D-09), reaper loop with TryLock skip-in-flight + snapshot-then-iterate (D-10/D-11/D-12/D-13), SESSION_TTL_MS + SESSION_MAX env-loading. Closes SESS-01, SESS-02, registry side of SESS-03.

**Wave 3** *(blocked on Waves 1+2 — Phase 5 acceptance)*

- [x] 05-03-PLAN.md — Slice C: DELETE + /health/agents + main.go wiring — RegistryStatsSource interface + agentsHandler + /health/agents exempt route (D-14/D-15/D-16/D-17/D-18), DELETE /v1/sessions/:id via SessionsRouter (D-08 HTTP side), X-Session-Id branch in all three surface handlers (Ollama + OpenAI + Anthropic) with per-entry mutex + MarkUsed defer, cmd/otto-gateway/main.go wiring (registry construction + Registry.Start + ordered shutdown registry-before-pool), blocking human-verify SC1..SC5 against real kiro-cli. Closes OBSV-02 + HTTP side of SESS-03.

**Wave 4** *(GAP CLOSURE — blocked on Wave 3; addresses 05-VERIFICATION.md gaps 1+2)*

- [x] 05-04-PLAN.md — SC3 root-cause + fix: wire-trace both kiro-cli paths (working pool vs broken session), diff transcripts to identify protocol divergence, encode fix in `internal/session/entry_acp.go`, ACP-fake unit test, strengthen DeleteSession_CancelsInFlight (≥1 chunk before DELETE assertion), full e2e suite green. Closes SESS-01/SESS-02/SESS-03 PARTIAL → SATISFIED.

**Wave 5** *(GAP CLOSURE — blocked on Wave 4; addresses 05-VERIFICATION.md gap 3)*

- [x] 05-05-PLAN.md — PHASE5-PERF.md skeleton + manual measurement gates: autonomous report skeleton, human-action latency (wrk 4×8×30s Node↔Go), human-action SESSION_MAX RSS sanity (8 sessions, per-child RSS), sign-off + VERIFICATION.md re-stamp. Closes CLAUDE.md non-functional perf constraint gate.

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

**Plans:** 5/5 plans complete

Plans:
**Wave 1** *(cross-cutting foundation; blocks Waves 2-3)*

- [x] 06-01-PLAN.md — Foundation: canonical.ToolCallChunk.ID (D-08), engine.coerce.go (D-01/D-09/D-10/D-11/D-12), engine.Collect aggregates kiro-native ChunkKindToolCall into `[tool: <name>]\n` narration text (iteration-3 fix to HIGH #1 — restores non-streaming Ollama/OpenAI rendering), per-surface Message.ToolCalls population contract (iteration-3 HIGH #3 reworded — Ollama/OpenAI via CoerceToolCall, Anthropic via 06-04 D-07 exception, generic engine.Collect populates none), engine.buildBlocks [Available tools] JSON catalog with debug-log fallback (D-16), acp/translate.go tool_call/tool_call_chunk → ChunkKindToolCall (D-03 canonical extraction), BLOCKING Node byte-fidelity checkpoint at end of slice (moved from 06-05 per REVIEW HIGH #3)

**Wave 2** *(per-surface vertical slices — run in parallel; blocked on Wave 1 including the Node fidelity checkpoint)*

- [x] 06-02-PLAN.md — Ollama vertical slice: wire.go ollamaChatResponseMessage.ToolCalls, handlers.go CoerceToolCall hook-in (non-streaming) + default-omitted stream-field test, render.go plain-object args (SC #2), ndjson.go kiro-native [tool: <name>]\n thought-text ONLY (two-path rule) + STREAMING COERCE on buffered text at stream end (REVIEW HIGH #1) + sawKiroNativeToolCall skip-or-coerce-or-flush logic (iteration-3 fix to HIGH #2 — no double-fire after kiro-native)
- [x] 06-03-PLAN.md — OpenAI vertical slice: wire.go []json.RawMessage per-entry decode for mixed-validity tools tolerance (iteration-3 fix to MEDIUM #4) + openAIToolSpec + ToolChoice decode (D-13), handlers.go CoerceToolCall hook-in (non-streaming), render.go JSON-string args + finish_reason post-fixup (SC #1), sse.go kiro-native ChunkKindToolCall as text-delta narration (two-path rule) + STREAMING COERCE multi-frame native tool_calls at stream end (REVIEW HIGH #1) + sawKiroNativeToolCall skip-or-coerce-or-flush logic (iteration-3 fix to HIGH #2) + role-emit-once-with-tool-call-first golden fixture (REVIEW LOW #8)
- [x] 06-04-PLAN.md — Anthropic vertical slice (D-07 EXCEPTION to per-surface Message.ToolCalls contract — kiro-native renders as native tool_use blocks): wire.go anthropicToolSpec → canonical.ToolSpec + tool_choice decode (D-14 closes TODO), NEW anthropic/collect.go local aggregator (D-07 exception) WITH parity test suite vs engine.Collect (iteration-3 fix to MEDIUM #5), sse.go applyChunk tool_use block sequence with CR-01 + block-index discipline + stop_reason finalize override, render.go non-streaming stop_reason override to "tool_use" (REVIEW MEDIUM #4), handlers_test.go BOTH behavioral (REVIEW LOW #9 required) AND static-source NO-coerce assertions (D-01 belt-and-suspenders)

**Wave 3** *(cross-surface integration + UAT checkpoint; blocked on Waves 1-2)*

- [x] 06-05-PLAN.md — Cross-surface E2E: NEW tests/e2e/cmd/fake-kiro-cli/main.go binary supporting full ACP method set (initialize/session/new/session/set_model/session/prompt/session/cancel/ping per REVIEW HIGH #5), NEW tests/e2e/tools_testmain_test.go TestMain compiles binary at package init with per-pid os.TempDir() path (iteration-3 fix to MEDIUM #6 — binary lifetime is package-scoped, not per-test t.TempDir), tools_fixtures.go with new FakeKiro(t,script) (cmd, env) API reading package-level fakeKiroBinaryPath var (REVIEW HIGH #5), `go vet -tags e2e ./tests/e2e/...` (iteration-3 fix to MEDIUM #7), tools_{ollama,openai,anthropic,cancel}_test.go full D-17 12-scenario matrix including REVIEW HIGH #1/#2/MEDIUM #4 + iteration-3 HIGH #1/#2 E2E verifications, scenario 12 mid-stream cancel with frame-log assertion, blocking HUMAN-UAT checkpoint for loop24-client messages.stream() conformance (Node byte-fidelity checkpoint MOVED to 06-01 per REVIEW HIGH #3)

### Phase 6.1: Admin Observability UI (INSERTED)

**Goal:** Lean, dark-mode admin page served at `GET /admin` that renders `/health` + `/health/agents` JSON in a styled UI using the OTTO brand palette. Self-refreshes via lightweight client-side polling (5s default) so operators see live pool/session/process state without manual refresh. Pure consumer of existing observability endpoints — no new gateway request-path logic, no new ACP plumbing. Single static binary preserved via `embed.FS` for HTML template + CSS + minimal JS (no node toolchain, no cgo).

**Mode:** mvp
**Depends on:** Phase 6
**Requirements:** OBSV-* (no new IDs — surfaces existing `/health`, `/health/agents` data through HTML/CSS)

**Brand palette** (from `oscar-adminui/src/layouts/UserThemeOptions.js` `customColors`):

- Primary accent: `#FAD22D` (brand yellow/gold)
- Body background: `#28243D` (dark purple-navy)
- Card/paper surface: `#3A3A3A`
- Foreground text: `#FAFAFA` (brandWhite) / muted `#A0A0A0` (brandGray3)
- Borders: `#4A4A4A` (brandGray1c)
- Status — healthy: `#0FC373` (brandGreen) / warning: `#FF8C0A` (brandOrange) / failed: `#FF3232` (brandRed)
- Activity / live indicator: `#AF78D2` (brandPurple)

**Success Criteria** (what must be TRUE):

  1. `GET /admin` returns a styled HTML page rendering the JSON contents of `/health` (overall status, pool size, alive, busy, sessions active) and `/health/agents` (per-slot detail: alive, busy, label; per-session detail: alive, last_used) using the brand palette above. Page is responsive at 1024px+.
  2. The page auto-refreshes the displayed metrics every 5s by polling `/health` + `/health/agents` from client-side JS, with a visible "last updated" timestamp and a paused-state badge (`#FF8C0A`) when a poll fails. Does NOT add a new gateway-side polling loop or background goroutine.
  3. The route registration is additive: `/admin` is wired alongside existing surfaces; surface-gating (`ENABLED_SURFACES`) does not affect it; mounting `/admin` does not alter any existing `/api/*`, `/v1/*`, or `/health*` behavior. Verified via existing route tests + a new admin-route smoke.
  4. Auth + IP-allowlist behavior matches the auth middleware contract — `/admin` is auth-protected when `AUTH_TOKEN` is set; allowed (parallels `/health`) when `AUTH_TOKEN` is unset (dev mode).
  5. Templates + CSS + JS ship as compiled-in assets via `embed.FS` — `go build` from a fresh checkout produces one binary; no external file dependencies at runtime; cross-compile from macOS to `linux/amd64` and `windows/amd64` still succeeds with `CGO_ENABLED=0`.
  6. **Nice-to-have (deferrable):** live log tail at `/admin/logs` via SSE that streams new `/tmp/otto-gateway.log` lines as they appear. Must not hold an exclusive file handle, must tolerate log rotation (re-open on read error), and must be implementable without altering any existing log-writing path. If this can't be done cleanly inside the slice's budget, defer to a follow-up.

**Plans:** 4/4 plans complete

Plans:
**Wave 1**

- [x] 06.1-01-PLAN.md — Slice A vertical: snapshot endpoint + minimal page shell — internal/admin package (admin.go, assets.go, snapshot.go), embed.FS templates/CSS/JS, summary-strip-only template + 30s polling JS, server.Config.AdminHandler mount, cmd/main.go wiring, .go-arch-lint.yml admin component, non-interference smoke (D-15/D-16)

**Wave 2** *(blocked on Wave 1)*

- [x] 06.1-02-PLAN.md — Slice B additive UI: Pool Slots 3-col responsive grid + Active Sessions table (template + CSS + JS hydration; dead-slot 2px red border per CONTEXT specifics; relative-time formatter; empty-state swap)

**Wave 3** *(blocked on Waves 1+2)*

- [x] 06.1-03-PLAN.md — Slice C vertical: log tail panel via SSE — tail.go (shared singleton Tailer + RingBuffer per D-09/D-10/D-11), sse.go (sseHandler mirroring anthropic SSE D-05 single-goroutine invariant), /admin/logs/stream wired, UI controls (level/grep/pause/resume/N-new badge) per UI-SPEC

**Wave 4** *(blocked on Waves 1+2+3 — phase exit)*

- [x] 06.1-04-PLAN.md — Slice D verification: make cross green (CGO_ENABLED=0 linux/amd64 + windows/amd64.exe), binary-size delta ≤30KB, full manual UAT in browser (all 6 ROADMAP SC bars including dead-slot visual contract + log rotation), docs/operating.md Admin UI section

### Phase 8: Plugin Hook Chain

**Goal:** `PreHook` / `PostHook` interfaces operate on canonical request/response types, with day-one hooks registered: RequestID, Auth (refactored from middleware), structured Logging, and PII Redaction. Short-circuit return from `PreHook` skips the engine. The PII hook ships with an extensible regex+validator recognizer registry — six built-in entities (Email, IPv4, IPv6, SSN, Credit Card with Luhn check, US Phone) and a one-struct addition path for new entities — so future guardrails (moderation, budget, schema, cache, audit) and new PII recognizers land without touching the hook engine.
**Mode:** mvp
**Depends on:** Phase 6
**Requirements:** PLUG-01, PLUG-02, PLUG-03, PLUG-04, PLUG-05, PLUG-06, OBSV-03, OBSV-04
**Success Criteria** (what must be TRUE):

  1. The engine calls `chain.Pre(ctx, canonicalReq)` before any ACP call and `chain.Post(ctx, canonicalReq, canonicalResp)` after; a `PreHook` returning a non-nil `*canonical.ChatResponse` short-circuits the engine and the adapter renders that response in its native shape.
  2. `RequestIDHook` generates an `X-Request-Id` (or honors an inbound one) and that ID appears in every `slog` record across pre-hook, engine, ACP, and post-hook spans for the same request.
  3. `AuthHook` (Pre) validates bearer tokens from `AUTH_TOKEN` and short-circuits with a canonical auth-error response that the adapter renders correctly for both surfaces (OpenAI `{error:{...}}` vs Ollama `{error:"..."}`).
  4. `LoggingHook` emits a structured `Pre` log line on request entry and a `Post` log line on response with timing — both via `log/slog`.
  5. `ENABLED_HOOKS` env var enables/disables registered hooks at boot; hooks execute in registration order and the first non-nil short-circuit wins on the Pre chain.
  6. `PIIRedactionHook` (Pre) walks every `canonical.ChatRequest.Messages[].ContentParts[].Text` and applies a registered set of `Recognizer{Name, Pattern *regexp.Regexp, Validate func(string) bool}` entries — six built-in recognizers (Email, IPv4, IPv6 via `net.ParseIP`, SSN with range filter, Credit Card with Luhn check, US Phone) — replacing matches with `<ENTITY>` tokens (or counter-suffixed `<EMAIL_1>` form to preserve referential identity within a prompt). All patterns are compiled at package init (no per-request compile). Env knobs: `PII_REDACTION_ENABLED` (bool, default off), `PII_ENABLED_ENTITIES` (comma list of entity names, default all six), `PII_REDACTION_MODE` (`replace|mask|hash|drop`, default `replace`). Adding a seventh recognizer requires only appending one `Recognizer{}` entry to the registry — no changes to the hook itself, the chain runner, or any caller. Pure-Go, no cgo, no external deps.
  7. `GET /health/hooks` (read-only, exempt from auth like `/health` and `/health/agents`) returns the registered chain in registration order as JSON: each entry includes `name`, `kind` (`Pre`, `Post`, or `Pre,Post`), `enabled` (bool reflecting `ENABLED_HOOKS`), and an optional `config` object exposing safe-to-publish settings (e.g. `PIIRedactionHook` exposes `entities` and `mode`; `AuthHook` exposes no secrets). The endpoint is view-only — there is no runtime mutate path in v1; configuration changes require a restart.

**Plans:** 5/5 plans complete

### Phase 08.1: Close gap: INTEG-01 streaming-mode PreHook short-circuit + v1.5 audit WARNINGs (CI sequence, /admin auth posture, T-8-AUTH-BYPASS stub coverage, REQUIREMENTS.md traceability) (INSERTED)

**Goal:** Close the v1.5 milestone audit's BLOCKER and four WARNINGs in one phase: (a) Slice A — fix INTEG-01 streaming-mode PreHook short-circuit so bad-bearer `stream:true` requests return pre-header 401 + native JSON envelope across all three surfaces (Ollama, OpenAI, Anthropic) instead of empty 200 SSE/NDJSON streams; (b) Slice B — close WARNING-01 (CI/Makefile explicit trust-gate sequence per brief §3.12), WARNING-02 (`/admin` auth-exempt-by-design annotation in PROJECT.md + REQUIREMENTS.md + docs/operating.md), WARNING-03 (REQUIREMENTS.md traceability refresh: add PLUG-06 + OBSV-04 rows, correct "58 total" → "62 total", flip ~50 checkboxes per audit Final Status), WARNING-04 (T-8-AUTH-BYPASS list-mode-stub bypass annotation in REQUIREMENTS.md AUTH-01/02 + docs/operating.md).
**Requirements**: PLUG-02, PLUG-04, PLUG-06, STRM-01, STRM-02, ANTH-01, ANTH-02, AUTH-01, AUTH-02, OBSV-04, TRST-01, TRST-02, TRST-03, TRST-04, TRST-05, TRST-06, TRST-07, TRST-08, BLD-03
**Depends on:** Phase 8
**Plans:** 5/5 plans complete
Plans:
**Wave 1**

- [x] 08.1-01-PLAN.md — Slice A integration: add ShortCircuitResponse to ollama+openai RunHandle interfaces, forward through main.go shims, propagate test-fake stubs, insert short-circuit guards in all four streaming handler sites (atomic commit)

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 08.1-02-PLAN.md — Slice A tests: four adapter-level streaming short-circuit tests (Ollama×2 per Pitfall 6, OpenAI×1, Anthropic×1) plus three new stream:true rows on TestE2E_BadBearer_AllThreeSurfaces

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 08.1-03-PLAN.md — Slice B WARNING-01: explicit fmt-check/vet/build/examples targets in Makefile and the canonical brief §3.12 sequence in .github/workflows/ci.yml
- [x] 08.1-04-PLAN.md — Slice B WARNING-02 + WARNING-04 docs: Security carve-outs annotation in PROJECT.md and Auth posture quick reference subsection in docs/operating.md

**Wave 4** *(blocked on Wave 3 completion)*

- [x] 08.1-05-PLAN.md — Slice B WARNING-03 broad refresh: add PLUG-06 + OBSV-04 rows, correct coverage comment, flip ~50 checkboxes per audit Final Status, annotate AUTH-01/02 with carve-out language (D-15 + D-17)

### Phase 8.2: Ollama `format` Parity (INSERTED)

**Goal:** Close the Node-shim parity gap on Ollama's `format` field so LangFlow flows with `format:"json"` or `format:<JSON schema>` continue to work when pointed at otto-gateway. Three coordinated changes: (a) wire-decode `format` on `/api/chat` and `/api/generate` into the dormant `canonical.ChatRequest.Format` seam (`internal/canonical/chat.go:245`); (b) implement a surface-agnostic `JSONFormatSteeringHook` (`PreHook`) that appends the verbatim Node-shim GEN_RULES block to the canonical system prompt whenever `Format != nil`, plus a schema description when `Format.Type == "json_schema"`; (c) wire the existing conservative `stripFences` helper (`internal/engine/coerce.go:231`) into the Ollama non-streaming render path so a model response wrapped in ```` ```json … ``` ```` is unwrapped before client return. This is the "one place to enforce policy" thesis applied to JSON-format steering: the hook fires on the canonical request, so any future OpenAI/Anthropic adapter that populates `Format` inherits the steering for free.

**Mode:** mvp
**Depends on:** Phase 8 (PreHook chain), Phase 6 (`stripFences` helper landed during tool-call work)
**Requirements:** REQ-OLLAMA-01 (parity closure), PLUG-01 (PreHook contract reuse) — no new requirement IDs introduced; this is a correctness/parity fix against the Node implementation that otto-gateway is replacing.
**Success Criteria** (what must be TRUE):

  1. `POST /api/chat` with `"format": "json"` populates `canonical.ChatRequest.Format = &canonical.Format{Type: "json"}`; with a JSON-object value populates `Format{Type: "json_schema", Schema: <decoded map>}`. `POST /api/generate` does the same. Wire-decode is null-safe: omitted `format` leaves `Format == nil`; empty string `""` is treated as omitted.
  2. With `JSONFormatSteeringHook` registered, a canonical request with `Format != nil` returns from `Pre` with the verbatim GEN_RULES block (matching the Node shim's text exactly) appended to `System`. The block instructs: complete output for every item, no summarize/truncate/abbreviate, no prose/preamble/commentary/follow-up questions, no offers to save/export, no markdown fences.
  3. When `Format.Type == "json_schema"` AND `Format.Schema != nil`, the appended block additionally includes a serialized "The output must match this JSON schema: <compact JSON>" line.
  4. The hook is a no-op when `Format == nil` (request unmodified, byte-identical `System`).
  5. The Ollama non-streaming response path applies `stripFences` to the assembled assistant text when `req.Format != nil`. A fake-engine returning ```` ```json\n{"a":1}\n``` ```` produces a `{"a":1}` payload at the wire. The fence-strip is conservative: it only fires when the **entire trimmed text** is one fenced block; inline fences in legitimate prose are preserved (Pitfall mirrors the Node fix's regression test).
  6. `JSON_FORMAT_STEERING_ENABLED=false` (env, default `true`) keeps the hook registered but disabled — `Pre` becomes a pass-through. Mirrors the `PII_REDACTION_ENABLED` pattern from Phase 8.
  7. Existing non-`format` requests are byte-identical at the canonical layer and the wire (no GEN_RULES, no fence-strip). Existing `format:"json"` tool-call coerce path (`engine.coerce.go`) is unchanged.
  8. Integration test: LangFlow-shape `/api/chat` request with `format:"json"`, streaming disabled, against a fake engine that returns fenced text, asserts the client receives unfenced JSON. Plus a happy-path test where the model returns clean JSON (no fence) — confirms the strip is a no-op.

**Plans:** 1/1 plans complete
**Out of scope (locked):**

- **Streaming + `format:"json"` fence-stripping.** The Node fix did not strip mid-stream either — fences only land mid-delta in rare cases and stripping them requires buffered re-emission that breaks the streaming contract for clients that don't care about JSON. Almost all LangFlow `format:"json"` flows are non-streaming. Tracked in Deferred Ideas; revisit if a streaming consumer reports the issue.
- **OpenAI / Anthropic `response_format` wiring.** Both surfaces have native sampler-level JSON modes via Claude through kiro-cli, and the OpenAI/Anthropic adapters do not currently populate `canonical.Format`. The hook is built surface-agnostic so the day those adapters wire up `Format`, steering applies automatically — but the wiring itself is a separate phase.
- **Sampler-level JSON constraint at the upstream.** kiro-cli (ACP) does not expose a `max_tokens` or JSON-mode sampler knob. Prompt-only steering is the v1 lever; this is the same constraint the Node shim operates under.
- **Schema validation of model output.** The steering is best-effort; we do not introspect the returned text against `Format.Schema` and reject mismatches. Downstream consumers (LangFlow components, application code) own validation.
- **New requirement IDs.** This is parity closure against the Node shim that otto-gateway is replacing — no new REQ-* IDs.

Plans:
**Wave 1**

- [x] 08.2-01-PLAN.md — Single atomic slice: Ollama `format` wire-decode (`/api/chat` + `/api/generate`) populates `canonical.ChatRequest.Format`, new `internal/plugin/jsonformat/` package with `JSONFormatSteeringHook` (Pre) registered in `main.go` behind `JSON_FORMAT_STEERING_ENABLED` (default true), Ollama non-streaming render applies `stripFences` when `Format != nil`, integration test against fake-engine with LangFlow-shape `format:"json"` request.

### Phase 8.3: ACP Prompt() Non-Blocking Refactor (INSERTED)

**Goal:** Eliminate the chunk-buffer-overflow deadlock in `internal/acp/client.go:Prompt()` that wedges the gateway when kiro-cli emits more than 64 `session/update` chunks before its `session/prompt` response. Today `Prompt()` blocks until the final response, but its caller (`engine.Run`) cannot drain `Stream.Chunks` until `Prompt()` returns — so any response that overflows the 64-slot channel deadlocks the readLoop, the ping reply, and the final response in one shot. Symptom on Windows (v1.9.2): a PII-encrypt round-trip against Anthropic `/v1/messages` produces `engine.new_session.ok` then 100 seconds of silence then `request POST /v1/messages 500 100000ms`, while kiro-cli the worker burns CPU + grows ~22 MB of memory streaming into a wedged pipe. The fix: `Prompt()` returns the `*Stream` as soon as the send is accepted; a per-prompt goroutine waits on the dispatcher response channel and finalizes the stream via `s.close(...)`. `Stream.Result()` becomes the single sync point for the final `StopReason` / error. The synchronous-Prompt deadlock is documented verbatim in the existing `client.go:701-725` docstring as a known footgun ("Calling Prompt synchronously and draining Chunks afterward only works when the total chunk count fits in the 64-slot buffer") — this phase removes the footgun.

**Mode:** mvp
**Depends on:** Phase 1.1 (ACP wire alignment — establishes `Stream.Result()` + `parseStopReason` + dispatcher lifecycle), Phase 8.1 (current streaming short-circuit semantics that the new flow must preserve)
**Requirements:** ACP-02, ACP-03 (re-validation — no NEW requirement IDs; this is a concurrency-contract correctness fix against the live ACP spec). No canonical-type changes.
**Success Criteria** (what must be TRUE):

  1. `acp.Client.Prompt(ctx, sid, blocks)` returns `(*Stream, error)` as soon as the `session/prompt` request has been accepted by `c.send(...)` — it no longer blocks waiting for the corresponding response frame. The `engine.prompt.sent` debug line in `internal/engine/engine.go:208` fires within milliseconds of the send completing, regardless of how many chunks kiro-cli will subsequently stream back.
  2. A per-prompt goroutine owns the response wait: it selects on `respCh`, `ctx.Done()`, and the client lifetime context, parses `result.stopReason` via `parseStopReason`, clears `c.activeStream` under `c.streamMu`, and finalizes via `stream.close(&FinalResult{StopReason: stop}, nil)`. RPC errors propagate via `stream.close(nil, err)`; the `closeSentinelCode` path surfaces `ErrClientClosed`. Context cancellation sends the same best-effort `session/cancel` notification as the current synchronous path.
  3. The dispatcher entry for the prompt id is owned by the goroutine (registered before `c.send`, cancelled exactly once on goroutine exit). No `disp.cancel(id)` race when Close() runs concurrently with a still-in-flight prompt; `failPending` continues to unblock the goroutine via `respCh` carrying the close-sentinel error.
  4. Engine and adapter call sites are minimally updated: `engine.Run` (engine.go:203) returns the `*Run` handle as soon as `Prompt()` returns (no longer waiting for the full response); existing callers continue to consume `Stream.Chunks` then `Stream.Result()` and observe identical semantics on the happy path. The Anthropic streaming-disabled re-route (`internal/adapter/anthropic/handlers.go:217-264`) still drains chunks via `CollectFromRun` and synthesizes SSE from the aggregated response.
  5. The chunk-buffer-overflow deadlock is gone: a test that drives kiro-cli (or a fake-acp emitting >64 chunks before the prompt response) completes successfully without ever hitting `STREAM_IDLE_TIMEOUT_SEC`. The `TestIntegration_FakeACP_E2E_MixedVariants` family is updated as needed but its "Prompt-on-one-goroutine, drain-on-another" pattern remains valid.
  6. Stream godoc + Prompt godoc are updated to remove the "callers MUST drain concurrently with Prompt-waiting goroutine" warning (it is no longer required for correctness, only for throughput). The new concurrency contract is documented: `Prompt()` returns immediately, callers drive the stream however they like, `Stream.Result()` is the sync point.
  7. `make ci` is green: `gofumpt -d` → `go vet` → `go build` → `golangci-lint run` → `govulncheck` → `go test -race ./...` all pass. `goleak.VerifyNone` continues to pass in the ACP package (the new per-prompt goroutine exits on every termination path).
  8. The Windows v1.9.2 PII smoke-test regression is fixed: `.\scripts\test-pii.ps1 pii` against a `PII_REDACTION_MODE=encrypt` gateway round-trips correctly through `/v1/messages` and `/v1/chat/completions` and `/api/chat`, with all three "expected plaintext present + no [PII:Entity:...] ciphertext leaked" assertions passing.

**Out of scope (locked):**

- **Changing the 64-slot buffer size.** Buffer-resize would mask the deadlock but not fix it; this phase removes the deadlock at the API level so the buffer can stay at 64 (or be tuned for throughput, separately).
- **Other ACP RPCs (`Initialize`, `NewSession`, `SetModel`, `Ping`).** Those remain synchronous — they're small-response request/reply pairs where the synchronous wait is appropriate. Only `Prompt()` participates in the chunk-stream contract and only `Prompt()` has the deadlock.
- **The "OpenAI stream=false hangs at session/new" symptom from the v1.9.2 wire-test run.** That request never reaches `Prompt()` — it hangs before `engine.new_session.ok` is logged. Tracked as a separate worker-state defect; this phase does not investigate or fix it.
- **A surface-level retry on `Prompt()` failure.** Existing engine.Run callers handle Prompt errors via the watchdog + pool slot release; this phase preserves that path but does not add new retry semantics.
- **Buffering chunks into an unbounded queue to mask backpressure.** Backpressure on `Stream.Chunks` remains a correctness property (the readLoop is the producer, slow consumers correctly slow it down). What changes is that backpressure no longer cascades into the response-wait path.

**Plans:** 1/1 plans complete

Plans:
**Wave 1**

- [x] 08.3-01-PLAN.md — Single atomic vertical slice: refactor acp.Client.Prompt() to non-blocking + add awaitPromptResult goroutine (registered with c.wg) + engine.prompt.completed DEBUG emission + Stream docstring update (remove MUST-drain-concurrently footgun); add TestIntegration_Prompt_OverflowsBuffer_DoesNotDeadlock (≥128 chunks before response, 100ms return deadline) + four whitebox tests (dispatcher lifecycle, ctx-cancel during in-flight, Close during in-flight, engine.prompt.completed log emission); preserve TestIntegration_FakeACP_E2E_MixedVariants unchanged for backward-compat; goleak deliberate-leak verification; operator-Windows test-pii.ps1 sign-off (blocking human-verify).

### Phase 8.4: US Address PII Coverage (INSERTED)

**Goal:** Add regex-based US-address coverage to the PII redactor so encrypt-mode tokenizes street addresses, state codes, and ZIP codes end-to-end. Surfaced by the 2026-06-04 splunk-box probe against v1.9.7 of the prose-v2 NER: the current LOCATION recognizer catches popular city names (Austin, Boston, Cupertino, Washington) but misses state abbreviations (TX, CA, DC), ZIP codes (27584, 20500), street numbers (1111, 1600), and street names entirely — AND emits harmful false positives where street names get tagged as PERSON ("Main Street" → PERSON, "Pennsylvania Avenue" → PERSON, "Apple Park" → PERSON) which would be encrypted as someone's name on round-trip. For a PHI/PII gateway, partial-address coverage with PERSON false positives is worse than no coverage at all. This phase adds dedicated regex recognizers that take precedence over NER per the existing overlap arbitration at `pii.go:277` ("Skip NER candidates that overlap any regex span"), eliminating both the gap and the false-positive class in one stroke.

**Mode:** mvp
**Depends on:** Phase 8 (PII redactor architecture + ApplyMode pipeline), Phase 8.3 (concurrency-correctness baseline)
**Requirements:** New requirement to be introduced — provisional ID `PII-08` or similar (planner picks during planning; the existing TRST-* and TEST-* neighborhoods don't fit). The new requirement is "US address PII (street, state, ZIP) is captured and tokenized in encrypt mode with byte-for-byte round-trip fidelity."
**Success Criteria** (what must be TRUE):

  1. New `USZIP` recognizer regex `\b\d{5}(?:-\d{4})?\b` plus a context guard (e.g., preceded by `, ` after a known state code or city, OR validator that rejects obvious non-ZIPs like 00000, 99999) tokenizes both 5-digit and ZIP+4 forms. Round-trip byte-for-byte across all formats (`27584`, `20500-1234`).
  2. New `USState` recognizer regex with alternation of all 50 USPS two-letter codes + DC + territories (`AL|AK|AZ|...|WY|DC|AS|GU|MP|PR|VI`), anchored to the `, ST` context after a city or after a comma+space to prevent mid-sentence matches against common abbreviations (`OR`, `IN`, `OK`, `HI`). Round-trip byte-for-byte.
  3. New `USAddress` recognizer regex of shape `\d+\s+[A-Z][\w\s]+?\s+(?:St|Street|Ave|Avenue|Blvd|Boulevard|Rd|Road|Dr|Drive|Ln|Lane|Way|Pl|Place|Ct|Court|Pkwy|Parkway|Cir|Circle|Ter|Terrace)\b` with a street-suffix vocabulary. Catches the common `<number> <name> <suffix>` pattern. Round-trip byte-for-byte.
  4. The three new regex recognizers run **before** the NER stage so they win the overlap arbitration at `pii.go:277` — the previously-observed false positives `Main Street → PERSON`, `Pennsylvania Avenue → PERSON`, `Apple Park → PERSON` are silenced (the address regex's span covers those tokens, so the NER candidates are dropped).
  5. New `TestUSZIPRecognizer_CapturedSpan`, `TestUSStateRecognizer_CapturedSpan`, `TestUSAddressRecognizer_CapturedSpan` follow the FindString-based span-assertion pattern established by v1.9.7's `TestUSPhoneRecognizer_CapturedSpan` (assert exact captured spans, NOT just match-yes/no). Plus an integration test against a realistic address fixture like `1111 Main Street, Austin, TX 27584` that verifies all four recognizers fire and no PERSON false positive survives.
  6. `scripts/test-pii.ps1` and `scripts/test-pii.sh` are updated with an address-bearing fixture and assertions for the captured spans. The operator HUMAN-UAT proves on a Windows box that `1111 Main Street, Austin, TX 27584` round-trips byte-for-byte through encrypt mode.
  7. `make ci` is green (gofumpt → vet → build → golangci-lint → govulncheck → `go test -race ./...`). `goleak.VerifyTestMain` continues clean.
  8. The v1.5 milestone REQUIREMENTS.md gains the new requirement under a clear category; coverage count is updated; Traceability table includes the new entry.

**Out of scope (locked):**

- **Replacing prose v2 with a larger / cgo-bound NER model.** Phase 9 (Distribution) locks in the no-cgo, single-binary constraint. A transformer-based NER would in principle do all this in one pass but break that property. Not worth it.
- **International addresses.** US/NANP only this phase. International (UK postcodes, Canadian postal codes, EU street formats) is a separate scope.
- **Apartment / suite / PO Box parsing.** Common shapes (`Apt 5`, `Suite 200`, `PO Box 1234`) often appear adjacent to addresses but have their own recognizer requirements. Defer to a follow-up phase if needed.
- **City-name disambiguation.** The existing LOCATION NER will still catch most cities. This phase does NOT add a city-name regex recognizer (false-positive risk too high — `Boston` as a surname, etc.).
- **PERSON recognizer tuning.** The PERSON false positives on street names are silenced by overlap arbitration, not by changing the prose model. Tuning the model itself is a separate concern.

**Plans:** 1/1 plans complete
**Source:** Discovered during 2026-06-04 v1.9.7 splunk-box probe of the prose-v2 NER's full-address coverage. Probe documented in conversation; key findings: prose catches city names only, misses street + state abbreviations + ZIP, emits PERSON false positives on street names. Cross-reference: the probe was performed via an ad-hoc `TestProbe_LOCATION_AddressCoverage` test against `internal/plugin/pii.NewNEREngine().Detect(...)` and discarded after observation.

Plans:
**Wave 1**

- [x] 08.4-01-PLAN.md — Single atomic vertical slice (RED → GREEN → REFACTOR + Task H HUMAN-UAT): introduce PII-01 in REQUIREMENTS.md (new ### PII — Recognizer coverage section), add usAddressRe/usStateRe/usZIPRe regex literals + validateUSZIPRange validator + three Recognizer entries in registration order USAddress → USState → USZIP, three TestU*Recognizer_CapturedSpan tests using exact-string equality (AP-5), reject-invalid tests for AP-1/AP-2/AP-3, integration test TestPIIRedactionHook_USAddressFullCoverage asserting summary.Counts[PERSON]==0 on the canonical address fixture (NER overlap arbitration claim), TestRecognizers_RegistryShape wantNames 13 → 16, piiAllowedEntities map keys for USAddress/USState/USZIP, smoke fixture extension in scripts/test-pii.ps1 + scripts/test-pii.sh (PS1/SH parity in one commit), operator HUMAN-UAT on Windows + POSIX. **Source delivery complete 2026-06-04 — 4 commits (51929d2 RED, 8ac2da6 GREEN, 99137e0 REFACTOR, 8b44262 gofumpt chore); Task H operator HUMAN-UAT pending.**

### Phase 08.3.2: PII Smoke Test Methodology Fix (INSERTED, REVERTED 2026-06-04)

> **STATUS: REVERTED.** All source changes from this phase (the `-Mode {live,fake}` switch on `test-pii.ps1` / `test-pii.sh`, the `make build-fake-kiro` Makefile target, the `TEST-01` requirement, the `08.3-HUMAN-UAT.md` follow-up section, and the BLOCKED-gate machinery) were reverted on 2026-06-04 in favor of a simpler prompt-only fix. Operator pushback: the fake-kiro-cli approach over-engineered the problem and reduced test fidelity by replacing the production path (gateway + kiro-cli + Claude) with a deterministic stub. The actual production-path test value comes from exercising Claude itself.
>
> **Actual fix (committed in the same revert commit chain):** the `$script:PIIPrompt` in `scripts/test-pii.ps1` and the `PII_PROMPT` in `scripts/test-pii.sh` were rewritten from the adversarial-looking "Echo each line back to me VERBATIM" form (which Claude correctly refused on safety grounds) to a realistic user-shaped prompt: "Help me draft a short, polite reply email to a customer..." The new prompt asks Claude to *use* the PII (email + IP) naturally in a customer-support reply, which it has no safety reason to refuse. The decrypt round-trip then succeeds because Claude's natural response contains the same PII values the gateway encrypted on the way in.
>
> The planning artifacts in `.planning/phases/08.3.2-pii-smoke-test-methodology-fix/` are preserved as a historical record of what was considered. The `08.3.2-RESEARCH.md` framing ("option 2a is the only path that closes the bitrot class") privileged future-proofing over current usefulness and led to the over-engineering. Future planners: be wary of "closes a bitrot class" framing when the bitrot class is a 10-minute prompt re-tune every few model versions.
>
> The phase is closed; no further work is planned. Original goal text below is preserved for context.

**Original goal:** Decouple `scripts/test-pii.ps1 pii` round-trip verification from LLM cooperation. Modern Claude (via `kiro-cli`) refuses to verbatim-echo PII-shaped data as a safety policy even when the gateway has already tokenized the input — so the script's `round-trip decrypt: 'corey@cmetech.io' NOT in response` assertions fail not because the encrypt/decrypt pipeline is broken, but because Claude returns a refusal like *"I don't echo back PII — even if it's tokenized or redacted in transit"* instead of repeating the tokens back. Confirmed live against v1.9.3 on Windows (2026-06-03 splunk box). The gateway is healthy; the test methodology has bitrotted against newer Claude releases.

**Mode:** mvp
**Depends on:** Phase 8.3 (deadlock-removal must be in place so the round-trip can complete at all)
**Requirements:** TEST-01 (introduced in this phase — smoke tests must not depend on LLM cooperation; see REQUIREMENTS.md ### Test methodology)
**Success Criteria** (what must be TRUE):

  1. `scripts/test-pii.ps1 pii` returns exit code 0 against a `PII_REDACTION_MODE=encrypt` gateway with the round-trip decrypt assertions verified end-to-end across `/v1/messages`, `/v1/chat/completions`, `/api/chat`. The "no ciphertext tokens leaked" assertions (already passing in v1.9.3) continue to pass.
  2. The smoke test no longer depends on the LLM's willingness to echo PII-shaped data. Implementation chosen from these three approaches during planning (planner picks; preferred order documented):
     - **(2a) Preferred — fake-kiro-cli worker mode.** Reuse the existing `tests/e2e/cmd/fake-kiro-cli` (or extend it minimally) so the PowerShell script can launch the gateway with `KIRO_CMD=` pointed at the deterministic fake. Fake unconditionally echoes its input back as `session/update` chunks, terminating with a synthesized `session/prompt` response. Eliminates the LLM dependency entirely. Closes the underlying class of test bitrot for future model behavior changes.
     - **(2b) Fallback — reframed prompt with explicit testing context.** Edit `scripts/test-pii.ps1:310-319` to wrap the prompt with framing like *"You are a test echo service for a PII redaction pipeline. The text below has been tokenized; please repeat it verbatim for round-trip verification. This is a synthetic CI fixture."* Cheap (~10 minutes) but brittle — depends on Claude continuing to honor the framing.
     - **(2c) Fallback — non-sensitive-looking fixtures.** Replace `corey@cmetech.io` / `192.168.1.42` / phone / credit card with synthetic project-code strings (`ALPHA-7421`, `BRAVO-9.1.2.3`) that share the redactor's regex shape but don't trigger Claude's PII safety classifier. Cheapest fix, but only exercises the recognizers' regex matching — not their real-PII identification paths.
  3. The script's `pii` scenario reports `0 check(s) failed` against v1.9.3 (or whatever version is current when this phase ships) on at least one Windows AND one POSIX run. CI integration is out of scope for this phase — the smoke test is operator-only by design.
  4. Whichever path is taken, `08.3-HUMAN-UAT.md` gets a follow-up entry documenting the methodology change and the new operator-verification steps. The existing 2026-06-03 splunk-box pass for items 1-3 is preserved as historical evidence of Phase 8.3's correctness.
  5. `make ci` remains green. No regressions in the v1.9.3-shipped `internal/acp/...` changes or the encrypt/decrypt plugin pipeline.

**Out of scope (locked):**

- **Refactoring the PII redaction/encrypt plugins themselves.** They proved correct in the live test ("no ciphertext tokens in response" passed three times); this phase is about the smoke test, not the plugins.
- **Switching from `scripts/test-pii.ps1` to a Go-native test harness.** Possible long-term, but a separate scope. This phase fixes the PowerShell script's methodology, not its language.
- **Building a generic LLM-cooperation-free test framework.** This phase fixes the PII smoke test specifically. A broader test-framework change is a milestone-level decision.
- **Investigating other scenarios in `test-pii.ps1`** (modes other than `pii`, e.g., `wire-shape`, `health`). Only the `pii` round-trip scenario is in scope.

**Plans:** 3/3 plans complete
**Source:** Discovered during v1.9.3 Phase 8.3 HUMAN-UAT operator verification on 2026-06-03 (Windows splunk box). Confirmed all Phase 8.3 success criteria pass; only the LLM-cooperation-dependent script assertions fail. Cross-reference: `08.3-HUMAN-UAT.md` `## Gaps` section.

Plans:
**Wave 1**

- [x] 08.3.2-01-PLAN.md — Foundation: introduce TEST-01 in REQUIREMENTS.md (smoke tests must not depend on LLM cooperation), correct ROADMAP Requirements typo (TRST-01/02 → TEST-01), add Makefile build-fake-kiro target producing bin/fake-kiro-cli[.exe]

**Wave 2** *(blocked on Wave 1 — depends on TEST-01 existing + fake binary buildable)*

- [x] 08.3.2-02-PLAN.md — PowerShell vertical slice: add -Mode {live,fake} parameter (default fake) to scripts/test-pii.ps1, build no-BOM UTF-8 notifications NDJSON, precondition-check gateway KiroCmd points at fake, deprecation banner on -Mode live; Windows operator HUMAN-UAT proves 0 check(s) failed

**Wave 3** *(blocked on Wave 2 — POSIX sibling must mirror PS surface to prevent f7ccd40-style drift)*

- [x] 08.3.2-03-PLAN.md — POSIX sibling: mirror --mode flag in scripts/test-pii.sh, append Phase 08.3.2 follow-up to 08.3-HUMAN-UAT.md, flip TEST-01 to Complete in REQUIREMENTS.md after Windows + POSIX HUMAN-UAT sign-off (completed 2026-06-04)

### Phase 08.3.1: ACP Per-Session Stream Demux (INSERTED)

**Goal:** Replace the single-slot `c.activeStream *Stream` in `internal/acp/client.go:262` with a per-session map keyed by `sessionID`, and route every `session/update` notification by inspecting the wire frame's `params.sessionId` field in `handleNotification` (`client.go:909`) instead of pushing to whichever stream is currently in the slot. Closes the WR-04 race surfaced by the Phase 8.3 code review (`08.3-REVIEW.md`): a late `session/update` for a cancelled session can land on the *next* prompt's stream when the same `Client` is reused across prompts, causing silent cross-session content leakage in a multi-tenant LLM gateway. The misroute is silent (no log line fires because `activeStream` is non-nil at handler time) and can also re-open a narrower version of the Phase 8.3 deadlock if the stale chunk fills the new stream's 64-slot buffer before its consumer attaches, blocking `readLoop` from parsing the new prompt's response frame.

**Mode:** mvp
**Depends on:** Phase 8.3 (`awaitPromptResult` goroutine + `c.streamMu` discipline must be in place before the demux refactor)
**Requirements:** ACP-02, ACP-03 (re-validation — same correctness fix surface as Phase 8.3, no new requirement IDs)
**Success Criteria** (what must be TRUE):

  1. `c.activeStream *Stream` is replaced by a per-session structure (`sync.Map` OR `map[string]*Stream` guarded by `c.streamMu` — planner's call). `Prompt()` registers the new stream under its `sessionID` key before `c.send`; `awaitPromptResult` clears the same key on every termination arm (ctx-cancel, RPC error, close-sentinel, happy path).
  2. `handleNotification` at `internal/acp/client.go:909` extracts `sessionID` from the incoming `session/update` frame's `params.sessionId` field and looks up the matching stream. Updates for an unknown `sessionID` log `acp: session/update for unknown session — dropped` (the existing warning string at line 989, now firing on actual unknown-session — not "activeStream is nil") and return without pushing.
  3. A new integration test `TestIntegration_LateUpdateFromCancelledSession_DoesNotLeakToNewPrompt` drives: (a) start Prompt A on session S1 → cancel ctx → `awaitPromptResult` sends `session/cancel` and finalizes; (b) fakeacp emits one final delayed `session/update` for S1 after the cancel; (c) start Prompt B on the same `Client` for session S2; (d) drain Prompt B's `Stream.Chunks` and assert NO chunk carrying S1's payload appears. The test FAILS against the current single-slot `activeStream` and PASSES after the refactor.
  4. The existing Phase 8.3 tests (`TestIntegration_Prompt_OverflowsBuffer_DoesNotDeadlock`, the three whitebox tests for close-race / ctx-cancel / `engine.prompt.completed` field shape) continue to pass unchanged — the demux is additive at the routing layer, not a public-API change.
  5. `make ci` is green (gofumpt → vet → build → golangci-lint → govulncheck → `go test -race ./...`). `goleak.VerifyTestMain` continues to report zero leaks (the per-session map cleanup runs on every `awaitPromptResult` termination path so no goroutine or map entry outlives its prompt).
  6. The Stream godoc (`internal/acp/stream.go:32-45`, refreshed in Phase 8.3) is updated to note that `Client` now supports multiple in-flight prompts demuxed by `sessionID` — and the "one at a time in Phase 1" comment at `client.go:260` is updated.

**Out of scope (locked):**

- **Pipelining multiple concurrent prompts from `engine.Run` on the same `Client`.** The demux unlocks this architecturally, but the engine and pool still serialize one prompt per worker — exercising pipelined prompts is a separate observability + flow-control question for a later phase.
- **Changing the `Stream.Chunks` buffer size or backpressure semantics.** Phase 8.3 settled this; not revisited here.
- **A wire-level `session_id` sanity check on the response frame itself.** The dispatcher routes responses by JSON-RPC `id`, which is independent of `sessionID`; correlating the two is a defense-in-depth layer that doesn't belong in this phase.
- **The `_ = ctx` dead-weight at `client.go:407`** (`IN-02` from Phase 8.3 review) — a one-line cleanup unrelated to the demux refactor; track separately or fold into the same commit if trivial.

**Plans:** 0 plans
**Source:** WR-04 from `08.3-REVIEW.md` (commit `0af613e`), deferred by Phase 8.3 code-review-fix as out-of-scope (`08.3-REVIEW-FIX.md`, commit `1e65e56`).

Plans:
- [ ] TBD (run /gsd-plan-phase 08.3.1 to break down)

### Phase 9: Distribution

**Goal:** A single static binary cross-compiles cleanly from macOS to `linux/amd64` and `windows/amd64`, with the full trust-gate suite running in CI on every PR + nightly on main. This is the headline value of the Go port — one binary, no cgo, no platform-specific build tooling.
**Mode:** mvp
**Depends on:** Phase 8
**Requirements:** BLD-02, BLD-03, BLD-04, TRST-04, TRST-05, TRST-06, TRST-07
**Success Criteria** (what must be TRUE):

  1. `make cross` on a macOS dev box produces `bin/otto-gateway-linux-amd64` and `bin/otto-gateway-windows-amd64.exe` from vanilla `go build` + `GOOS`/`GOARCH`, with `CGO_ENABLED=0` and `-ldflags="-s -w"`; both binaries are statically linked and ≤25 MB.
  2. Each binary embeds its version via `-ldflags="-X main.version=$VERSION"`; `curl /api/version` against either binary returns the embedded value.
  3. The CI pipeline runs (and gates merges on) `gofumpt -d` → `go vet` → `go build` → `golangci-lint run` → `govulncheck` → `go test -race ./...` → `go test -run Example` → property tests → cross-compile smoke; all stages block the next on failure.
  4. `go-arch-lint` (or equivalent) enforces in CI that `internal/adapter/*` does not import `internal/engine` and `internal/canonical` imports nothing under `internal/`.
  5. `goleak.VerifyTestMain` is wired into handler-level test packages; `Example_` functions document and validate `coerceToolCall`, `pickCwd`, and `buildAcpBlocks`.

**Plans:** Closed out via `/gsd-quick` (260528-d84) rather than a full phase plan, because cross-compile, version embedding, packaging, codesign, and arch-lint enforcement were already shipped as side-quests during Phases 6.1 / 8 closeout work. The quick close-out filled the three remaining trust-gate items (TRST-05 goleak coverage in `auth` / `config` / `cmd`; TRST-06 property tests for `buildBlocks` + `CoerceToolCall`; TRST-07 `Example_buildBlocks` confirmed pre-existing) and landed the `.github/workflows/ci.yml` merge gate (SC3) — the headline deliverable that bound the phase together.

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 1.1 → 2 → 3 → 3.1 → 4 → 5 → 6 → 7 → 8 → 9

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundations | 5/5 | Complete   | 2026-05-23 |
| 1.1. ACP Wire Alignment (INSERTED) | 5/5 | Complete   | 2026-05-23 |
| 2. Ollama End-to-End | 6/6 | Complete   | 2026-05-24 |
| 3. OpenAI Surface | 4/4 | Complete   | 2026-05-25 |
| 4. Streaming | 4/4 | Complete   | 2026-05-25 |
| 5. Pool + Stateful Sessions | 5/5 | Complete    | 2026-05-26 |
| 6. Tool-Call Path | 5/5 | Complete    | 2026-05-27 |
| 6.1. Admin Observability UI (INSERTED) | 4/4 | Complete   | 2026-05-28 |
| 8. Plugin Hook Chain | 5/5 | Complete   | 2026-05-28 |
| 8.2. Ollama `format` Parity (INSERTED) | 1/1 | Complete   | 2026-06-03 |
| 9. Distribution | n/a (via /gsd-quick 260528-d84) | Complete | 2026-05-28 |
