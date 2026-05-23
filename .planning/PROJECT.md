# Loop24 Gateway

## What This Is

Loop24 Gateway is a Go-based LLM gateway that exposes both OpenAI-
and Ollama-compatible HTTP APIs on a single port and routes every
inbound request through a configurable guardrails chain to a pool
of `kiro-cli` ACP worker subprocesses. It replaces an existing
Node.js Ollama proxy (`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server`)
with a single statically-linked cross-platform binary that adds an
OpenAI surface alongside the existing Ollama one. Primary clients
are a Pi-SDK-based chat CLI (OpenAI shape) and an internal LangFlow
deployment (Ollama shape).

## Core Value

**Both API surfaces serve their respective clients without those
clients knowing kiro-cli exists, with one place to enforce policy.**

If everything else fails, this must hold: a LangFlow flow pointing
at `/api/chat` and a Pi-SDK CLI pointing at `/v1/chat/completions`
both receive correct streamed responses, and any guardrail (auth,
rate-limit, content moderation, schema validation, audit) defined
once on the canonical request type applies uniformly to both. The
gateway being faster than Node and shipping as one binary is bonus —
the surface compatibility and the single governance surface are
the load-bearing properties.

## Requirements

### Validated

<!-- Shipped and confirmed valuable. -->

(None yet — ship to validate. The existing Node implementation is
referenced as the spec, not as validated state of this codebase.)

### Active

<!-- Current scope. Building toward these. -->

Surface compatibility:
- [ ] **REQ-OLLAMA-01**: Existing LangFlow flows pointing at `/api/chat`, `/api/generate`, `/api/embed`, `/api/embeddings`, `/api/tags`, `/api/show`, `/api/ps`, `/api/version` keep working with zero reconfiguration.
- [ ] **REQ-OPENAI-01**: Pi SDK chat CLI (and any OpenAI-shaped client) can POST `/v1/chat/completions` with `Authorization: Bearer …` and receive an OpenAI-compatible response.
- [ ] **REQ-OPENAI-02**: `/v1/completions`, `/v1/embeddings`, `/v1/models` are served with OpenAI-compatible shapes.
- [ ] **REQ-SURFACE-01**: Both surfaces share one process, one port, one pool, one canonical request/response type. `ENABLED_SURFACES` env var disables either at deploy time.

Behavioral parity with the Node reference (`docs/reference/acp_server_node_reference.md`):
- [ ] **REQ-STREAM-01**: Ollama paths default to `stream: true` and emit NDJSON (`application/x-ndjson`).
- [ ] **REQ-STREAM-02**: OpenAI paths default to `stream: true` and emit SSE (`text/event-stream` with `data: [DONE]` terminator).
- [ ] **REQ-STREAM-03**: Client disconnect (HTTP request canceled) cancels the in-flight `session/prompt` via `session/cancel`.
- [ ] **REQ-TOOLS-01**: Tool calls returned in canonical form, then encoded per surface — JSON-string `arguments` for OpenAI, plain-object `arguments` for Ollama.
- [ ] **REQ-TOOLS-02**: `coerceToolCall` behavior preserved — plain-JSON-as-text + bare or markdown-fenced JSON converts to a synthetic tool call when `tools` is provided.
- [ ] **REQ-ACP-01**: `session/request_permission` from `kiro-cli` is auto-granted with `optionId: 'allow_always', granted: true`.
- [ ] **REQ-ACP-02**: 60s ping heartbeat; dead subprocesses are killed and replaced.
- [ ] **REQ-CWD-01**: Per-request `cwd` derived from longest common parent of `resource_link` block URIs in the prompt, with `KIRO_CWD` fallback and `X-Working-Dir` header override.
- [ ] **REQ-IMAGE-01**: Inline base64 images (Ollama `images: []` or OpenAI content-parts with `image_url`) translate to ACP `image` blocks with sniffed MIME type.

Engine + pool:
- [ ] **REQ-POOL-01**: Fixed-size pool of warm `kiro-cli` subprocesses (default `POOL_SIZE=4`); ready before HTTP listener accepts.
- [ ] **REQ-SESSION-01**: `X-Session-Id` header opts requests into a stateful session via `SessionRegistry`; idle entries reaped after `SESSION_TTL_MS` (default 30 min).
- [ ] **REQ-SESSION-02**: `DELETE /v1/sessions/:id` tears down a stateful session.

Guardrails (the plugin chain):
- [ ] **REQ-PLUGIN-01**: `PreHook` / `PostHook` interface operates on canonical types; short-circuit return from `PreHook` skips the engine call.
- [ ] **REQ-PLUGIN-02**: Day-one hooks: RequestID (Pre), Auth bearer-token (Pre), structured logging (Pre+Post).
- [ ] **REQ-PLUGIN-03**: `ENABLED_HOOKS` env var (or equivalent config) enables/disables hooks per deployment.

Embeddings:
- [ ] **REQ-EMBED-01**: `/api/embed`, `/api/embeddings`, `/v1/embeddings` serve BGE/E5 model embeddings locally, independent of `kiro-cli`.

Distribution + operations:
- [ ] **REQ-BUILD-01**: Single statically-linked binary for `linux/amd64` and `windows/amd64`, cross-compiled from macOS dev box with `CGO_ENABLED=0`.
- [ ] **REQ-OBSERV-01**: `/health` returns pool + registry + embedding stats; `/health/agents` returns per-slot detail. Bearer-auth and IP allowlist exempt these paths and `/api/version`.

Trust gates (per `docs/briefs/go_port_brief.md` §3.12 — non-negotiable):
- [ ] **REQ-CI-01**: `golangci-lint` strict config with `errcheck`, `errorlint`, `gosec`, `staticcheck`, `revive`, `wrapcheck` and others; warnings are CI hard failures.
- [ ] **REQ-CI-02**: `govulncheck` runs in CI on every PR + nightly on `main`.
- [ ] **REQ-CI-03**: `go test -race` always on in CI.
- [ ] **REQ-CI-04**: Architectural boundaries enforced (`internal/adapter/*` cannot import `internal/engine`; `internal/canonical` imports nothing under `internal/`).
- [ ] **REQ-CI-05**: Goroutine-leak detection (`go.uber.org/goleak`) on handler-level tests.
- [ ] **REQ-CI-06**: Property tests (`pgregory.net/rapid` or `testing/quick`) cover the Ollama↔canonical and OpenAI↔canonical translation functions and `coerceToolCall`.

### Out of Scope

- **Multi-provider routing.** We have one backend (`kiro-cli`). No fallback chains, weighted load balancing, virtual keys, customer/team hierarchies. (Bifrost has these; we don't need them.)
- **A web UI / status dashboard.** `/health` JSON is enough for v1.
- **MCP, WebSocket, or WebRTC realtime API surfaces.** Maybe later; not v1.
- **macOS or ARM deployment targets.** Dev runs on macOS but deployments are x86_64 Linux + Windows. ARM/macOS-as-deploy is a nice-to-have, not blocking.
- **Replacing `kiro-cli`.** The whole point is to proxy it faithfully.
- **Backward compat with the Node `.env` file format** beyond reusing env-var names.
- **In-process ONNX embeddings via cgo** — sacrificing the trivial cross-compile story is too expensive. Default to an out-of-process sidecar (per brief §3.4 Option C); brainstorm may reconsider.
- **Hot config reload / dynamic plugin registration.** Plugins are Go types registered at boot. Restart to change config.
- **Rust port.** Considered (`docs/briefs/rust_port_brief.md`) and rejected on cross-compile friction + first-Go-project pragmatism.

## Context

**Reference implementation.** The Node.js implementation we're porting lives at `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server`. Its full architecture is captured in `docs/reference/acp_server_node_reference.md`. The Go port must preserve every behavior in that doc's "Things that must survive the port" section — `coerceToolCall`, NDJSON default streaming, auto-grant permissions, pool warmup before listen, client-disconnect cancellation, longest-common-parent cwd derivation.

**Design briefs.** `docs/briefs/go_port_brief.md` is the spec of record. ~1000 lines covering clients, dual API surface, layered architecture, plugin hooks, trust gates, milestone plan (M0–M9), and Bifrost as the reference architecture. `docs/briefs/rust_port_brief.md` is the rejected sibling — kept for the trade-off rationale, not as a build target.

**Reference architecture.** Bifrost (`~/Projects/repos/local/bifrost`, `docs.getbifrost.ai`) is a production Go LLM gateway with ~50× our scope. We borrow the shape (`core/transports/plugins/framework` split, OpenAI-compatible integration packages, `HTTPTransportPreHook`/`PostHook` interfaces, `ChainMiddlewares` helper) without copying surface area we don't need (`fasthttp`, virtual keys, multi-provider routing, MCP/realtime, embedded UI).

**Clients.**
- *Pi SDK* (`https://pi.dev`, `@earendil-works/pi-ai`) — multi-provider LLM harness. Configured to use the OpenAI provider with a custom `base_url`. Open verification item: confirm Pi's exact env var / config key for setting the OpenAI base URL.
- *LangFlow* — running locally with flows whose model components already point at `http://localhost:11434/api/chat`. Zero reconfiguration required.

**First Go project for the author.** Author is senior in JS / Python / shell with some Go familiarity but no greenfield Go experience. This shapes decisions toward stdlib + chi over fasthttp, `log/slog` over zerolog/zap, simple env-var config over viper. AI-assisted development is expected and the trust-gate suite is sized accordingly.

**Local-only repo at boot.** `~/Projects/repos/local/loop24-gateway`. Module name `loop24-gateway` (bare) to defer the hosting decision. Bare module name will be revisited before the first remote push.

## Constraints

- **Tech stack**: Go 1.23+ — Required for `log/slog` ergonomics and post-1.22 `net/http` routing patterns. No cgo in the main binary (preserves trivial cross-compile).
- **Tech stack**: stdlib `net/http` + `chi` for routing — Rejected `fasthttp` (faster but breaks the `http.Handler` ecosystem; not worth it at our throughput).
- **Compatibility**: Ollama API endpoints and request/response shapes are fixed by existing LangFlow flows. Breaking changes there require a flow migration we're not paying for.
- **Compatibility**: OpenAI API shapes follow public OpenAI spec for the endpoints we serve. Pi SDK will fail on shape drift.
- **Distribution**: Single static binary per OS/arch. Cross-compile from macOS dev box must work with vanilla `go build` plus `GOOS`/`GOARCH` env vars. The instant cgo enters the picture (e.g. in-process ONNX), this collapses — explicit decision in `docs/briefs/go_port_brief.md` §3.4 to avoid that.
- **Performance**: Must not be slower than the Node implementation under concurrent load. Tail latency should improve. Hard numbers: TBD; pre-implementation baseline measurement is in the milestone plan.
- **Security**: Bearer-token auth + IP allowlist, both env-driven. Same defaults as Node version (no auth if env unset). Subprocess spawn is the highest-risk surface — `gosec` G204 and friends required to flag any tainted-input regressions.
- **Backward compat**: Environment variable names must match the Node version (`KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `POOL_SIZE`, `SESSION_TTL_MS`, `AUTH_TOKEN`, `ALLOWED_IPS`, `DEBUG`, `EMBEDDING_MODEL_DEFAULT`, etc.) so deployments can swap binaries.
- **Trust gates**: The lint/test/audit set in `docs/briefs/go_port_brief.md` §3.12 is non-negotiable from day one, not bolted on later. AI-assisted code without these guardrails generates plausible-looking-but-wrong async code patterns.

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go over Rust | Cross-compile triviality is the headline; first systems project for author; embeddings story acceptable via sidecar; Bifrost validates the pattern. Full trade-off in `docs/briefs/go_port_brief.md` §2 and `docs/briefs/rust_port_brief.md`. | — Pending |
| Dual API surface (OpenAI + Ollama) on one binary | LangFlow already configured for Ollama; Pi SDK uses OpenAI shape. Splitting into two services adds deployment complexity for zero benefit; sharing one engine forces a cleanly separated canonical layer that's strictly better architecture. | — Pending |
| Adapter-over-canonical layout | `internal/adapter/{ollama,openai}` translate native ↔ canonical; `internal/engine` consumes canonical only. Mirrors Bifrost's `transports/integrations/` pattern. | — Pending |
| `PreHook`/`PostHook` plugin chain for guardrails | Hooks on canonical types means one moderation/auth/budget rule covers both surfaces. Bifrost-inspired. Day-one footprint is small (RequestID, Auth, Logging); the seams allow content moderation, schema validation, budget, semantic cache as later additions without rewriting handlers. | — Pending |
| stdlib `net/http` + `chi` (reject `fasthttp`) | Bifrost uses fasthttp for throughput; our bottleneck is `kiro-cli` subprocess latency, not HTTP parsing. fasthttp breaks `http.Handler` ecosystem (testing, middleware, `r.Context()`). Not worth it for our scale. | — Pending |
| Trust-gate suite required from day one | AI-assisted development on a first-Go project. Strict `golangci-lint`, `gosec`, `govulncheck`, `-race`, `goleak`, property tests, architectural boundary linting. Derived from "Making AI-Generated Rust Code Trustworthy" (Garcia) adapted to Go tooling. | — Pending |
| Out-of-process embeddings sidecar (provisional) | Preserves trivial cross-compile by avoiding cgo. Subject to revisit if `fastembed-rs`-equivalent pure-Go option matures or if cgo deployment friction proves acceptable. | — Pending |
| Bare Go module name `loop24-gateway` | Local-only at boot; defer hosting decision until first remote push. | — Pending |

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd:complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-05-23 after initialization*
