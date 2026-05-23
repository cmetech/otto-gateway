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
- [ ] **Phase 2: Ollama End-to-End** - First runnable slice — LangFlow `POST /api/chat` reaches real `kiro-cli`
- [ ] **Phase 3: OpenAI Surface** - Pi-SDK `POST /v1/chat/completions` shares the same canonical engine
- [ ] **Phase 4: Streaming** - NDJSON (Ollama) and SSE (OpenAI) off one canonical chunk channel, with disconnect cancellation
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

### Phase 2: Ollama End-to-End

**Goal:** The first true end-to-end vertical slice — an existing LangFlow flow pointing at `http://localhost:11434/api/chat` reaches a real `kiro-cli` subprocess through the gateway and gets back a correct Ollama-shaped response. Establishes the canonical-engine / adapter pattern that every other surface phase builds on.
**Mode:** mvp
**Depends on:** Phase 1
**Requirements:** SURF-01, SURF-03, SURF-05, SURF-07, ACP-07, AUTH-01, AUTH-02, AUTH-03, OBSV-01
**Success Criteria** (what must be TRUE):

  1. `curl -X POST http://localhost:11434/api/chat -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'` returns an Ollama-compatible JSON response sourced from a real `kiro-cli` subprocess (non-streaming path, single canonical engine call).
  2. An existing LangFlow flow whose model component already points at `http://localhost:11434/api/chat` completes a chat invocation with zero reconfiguration.
  3. `GET /api/tags`, `POST /api/show`, `GET /api/ps`, `GET /api/version` return Ollama-compatible shapes; `POST /api/pull`/`push`/`create`/`copy` and `DELETE /api/delete` return success stubs.
  4. Bearer-token auth and IP-allowlist middleware reject unauthorized requests while exempting `/`, `/api/version`, and `/health`.
  5. Per-request `cwd` is derived from longest common parent of `resource_link` block URIs, with `KIRO_CWD` fallback and `X-Working-Dir` header override, verified by handler-level tests.

**Plans:** TBD

### Phase 3: OpenAI Surface

**Goal:** Bring the second adapter online on the same port, sharing the same canonical engine — Pi SDK with `base_url=http://localhost:11434/v1` completes an end-to-end chat against `kiro-cli` and gets back an OpenAI-compatible response. Validates that the adapter-over-canonical layout cleanly supports two surfaces.
**Mode:** mvp
**Depends on:** Phase 2
**Requirements:** SURF-02, SURF-04, SURF-06
**Success Criteria** (what must be TRUE):

  1. `curl -X POST http://localhost:11434/v1/chat/completions -H 'Authorization: Bearer …' -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'` returns an OpenAI-compatible JSON response sourced from the same canonical engine that serves `/api/chat`.
  2. A Pi-SDK CLI configured with `base_url=http://localhost:11434/v1` and a bearer token completes a chat round-trip without modification to the SDK.
  3. `GET /v1/models` and `POST /v1/completions` return OpenAI-compatible shapes; the model list at `/v1/models` and `/api/tags` reflect the same underlying set.
  4. Setting `ENABLED_SURFACES=ollama` (or `openai`) at deploy time disables the other surface without code changes; `OPENAI_PATH_PREFIX` and `OLLAMA_PATH_PREFIX` are overridable.
  5. Architectural boundary check passes: `internal/adapter/openai` and `internal/adapter/ollama` import only `internal/canonical` + `internal/plugin`; neither imports `internal/engine`.

**Plans:** TBD

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
**Requirements:** EMBD-01, EMBD-02, EMBD-03, EMBD-04, EMBD-05, EMBD-06
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
Phases execute in numeric order: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundations | 5/5 | Complete   | 2026-05-23 |
| 2. Ollama End-to-End | 0/TBD | Not started | - |
| 3. OpenAI Surface | 0/TBD | Not started | - |
| 4. Streaming | 0/TBD | Not started | - |
| 5. Pool + Stateful Sessions | 0/TBD | Not started | - |
| 6. Tool-Call Path | 0/TBD | Not started | - |
| 7. Embeddings | 0/TBD | Not started | - |
| 8. Plugin Hook Chain | 0/TBD | Not started | - |
| 9. Distribution | 0/TBD | Not started | - |
