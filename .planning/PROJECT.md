# OTTO Gateway

## What This Is

OTTO Gateway is a Go-based LLM gateway that exposes OpenAI-,
Ollama-, and Anthropic-compatible HTTP APIs on a single port and
routes every inbound request through a configurable guardrails
chain to a pool of `kiro-cli` ACP worker subprocesses. It replaces
an existing Node.js Ollama proxy
(`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server`) with a
single statically-linked cross-platform binary that adds OpenAI
and Anthropic surfaces alongside the existing Ollama one. Primary
clients are a Pi-SDK-based chat CLI (OpenAI shape), an internal
LangFlow deployment (Ollama shape), and loop24-client / GSD Pi
(Anthropic shape, via `ANTHROPIC_BASE_URL`).

## Core Value

**All three API surfaces serve their respective clients without
those clients knowing kiro-cli exists, with one place to enforce
policy.**

If everything else fails, this must hold: a LangFlow flow pointing
at `/api/chat`, a Pi-SDK CLI pointing at `/v1/chat/completions`,
and loop24-client with `ANTHROPIC_BASE_URL=http://localhost:11434`
calling `/v1/messages` all receive correct streamed responses, and
any guardrail (auth, rate-limit, content moderation, schema
validation, audit) defined once on the canonical request type
applies uniformly to all three. The gateway being faster than Node
and shipping as one binary is bonus — the surface compatibility
and the single governance surface are the load-bearing properties.

## Requirements

### Validated

<!-- Shipped and confirmed valuable. -->

**v1.5 shipped 2026-06-04** — all 63 traceable requirements satisfied. Full per-requirement evidence is archived at `.planning/milestones/v1.5-REQUIREMENTS.md`. Summary by category:

- ✓ **Surface compatibility** (REQ-OLLAMA-01, REQ-OPENAI-01/02, REQ-ANTH-01..07, REQ-SURFACE-01) — All three API surfaces serving real clients on one port via one canonical type. REQ-OLLAMA-01's LangFlow zero-reconfig is implicitly verified by Phase 8.2 (`format` parity ships against LangFlow's wire shape); formal operator smoke remains in `02-HUMAN-UAT.md` as operator-deferred.
- ✓ **Streaming + behavioral parity** (REQ-STREAM-01/02/03, REQ-TOOL-01/02/03, REQ-CWD-01, REQ-IMAGE-01) — NDJSON (Ollama) + SSE (OpenAI/Anthropic) off canonical chunks; client disconnect cancels via `session/cancel`; `coerceToolCall` and per-surface tool-args encoding; longest-common-parent `cwd` derivation.
- ✓ **ACP wire correctness** (REQ-ACP-01..07) — `session/request_permission` auto-grant; 60s ping heartbeat with dead-process replacement; Phase 1.1 closed 10 wire-shape defects.
- ✓ **Pool + sessions** (REQ-POOL-01..04, REQ-SESS-01..03) — `POOL_SIZE=4` warm pool ready before listener; `X-Session-Id` opts into stateful sessions with `SESSION_TTL_MS` idle-reap.
- ✓ **Plugin chain + PII** (REQ-PLUG-01..06, REQ-AUTH-01..03, PII-01) — `PreHook`/`PostHook` over canonical types; RequestID, Auth, Logging, PII redaction (11 entity types including US Address triad with overlap-arbiter NER suppression) registered as day-one hooks.
- ✓ **Distribution + trust gates** (REQ-BUILD-01, REQ-TRST-01..08, REQ-CI-01..06) — Single static binary cross-compiled to darwin-arm64/amd64, linux-amd64, windows-amd64 from macOS; trust-gate suite (gofumpt → vet → build → golangci-lint → govulncheck → `go test -race ./...`) runs on every PR + nightly.
- ✓ **Observability** (REQ-OBSERV-01..04) — `/health` + `/health/agents` per-slot; structured slog logging; admin observability UI at `/admin`.

**Outstanding operator-deferred smoke items (not blocking — code paths verified by automated + integration tests):**

- `02-HUMAN-UAT.md`: 3 items (real-kiro round-trip with operator auth, LangFlow zero-reconfig, auth posture smoke) — implicitly exercised by Phases 8.2 + 8.4 in production
- `08-HUMAN-UAT.md`: 7-step operator protocol on live binary

### Active

<!-- Current scope for v1.6. -->

v1.6 milestone scope — to be defined via `/gsd-new-milestone v1.6`. Carried-forward items recommended as the v1.6 first phase:

- [ ] **gofumpt tree-wide cleanup** — resolve pre-existing drift across `cmd/` + `internal/adapter/*` so `make ci` passes locally at `fmt-check`. Documented in `.planning/milestones/v1.5-phases/08.1-…/deferred-items.md` (when phase archival runs).
- [ ] **go.mod 1.23 → 1.24 bump** (or Go-1.23 rewrite of `internal/admin/tail_test.go`) — `testing.Context()` requires Go 1.24.
- [ ] **Phase 08.3.1: ACP Per-Session Stream Demux** — deferred from v1.5. Replace single-slot `c.activeStream *Stream` with per-sessionID map; closes WR-04 silent cross-session leak race. Required only for future multi-tenant gateway scenarios.
- [ ] **Nyquist coverage uplift** — 3/11 phases fully compliant in v1.5. Bring older phases up to the post-08.1 validation standard.
- [ ] **Authenticode code-signing for Windows** — seed `001-authenticode-code-signing-windows-distribution` in `.planning/seeds/` documents the rationale; v1.6 candidate for distribution-trust improvement.

### Out of Scope

- **Multi-provider routing.** We have one backend (`kiro-cli`). No fallback chains, weighted load balancing, virtual keys, customer/team hierarchies. (Bifrost has these; we don't need them.)
- **A web UI / status dashboard.** `/health` JSON is enough for v1.
- **MCP, WebSocket, or WebRTC realtime API surfaces.** Maybe later; not v1.
- **macOS or ARM deployment targets.** Dev runs on macOS but deployments are x86_64 Linux + Windows. ARM/macOS-as-deploy is a nice-to-have, not blocking.
- **Replacing `kiro-cli`.** The whole point is to proxy it faithfully.
- **Backward compat with the Node `.env` file format** beyond reusing env-var names.
- **Embeddings of any kind in v1** — `/api/embed`, `/api/embeddings`, `/v1/embeddings` will not be served. Decided 2026-05-27 to drop Phase 7 from the milestone. The downstream "in-process ONNX via cgo" and "out-of-process sidecar" trade-offs are now moot since neither is being built.
- **Hot config reload / dynamic plugin registration.** Plugins are Go types registered at boot. Restart to change config.
- **Rust port.** Considered (`docs/briefs/rust_port_brief.md`) and rejected on cross-compile friction + first-Go-project pragmatism.

## Context

**Reference implementation.** The Node.js implementation we're porting lives at `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server`. Its full architecture is captured in `docs/reference/acp_server_node_reference.md`. The Go port must preserve every behavior in that doc's "Things that must survive the port" section — `coerceToolCall`, NDJSON default streaming, auto-grant permissions, pool warmup before listen, client-disconnect cancellation, longest-common-parent cwd derivation.

**Design briefs.** `docs/briefs/go_port_brief.md` is the spec of record. ~1000 lines covering clients, dual API surface, layered architecture, plugin hooks, trust gates, milestone plan (M0–M9), and Bifrost as the reference architecture. `docs/briefs/rust_port_brief.md` is the rejected sibling — kept for the trade-off rationale, not as a build target.

**Reference architecture.** Bifrost (`~/Projects/repos/local/bifrost`, `docs.getbifrost.ai`) is a production Go LLM gateway with ~50× our scope. We borrow the shape (`core/transports/plugins/framework` split, OpenAI-compatible integration packages, `HTTPTransportPreHook`/`PostHook` interfaces, `ChainMiddlewares` helper) without copying surface area we don't need (`fasthttp`, virtual keys, multi-provider routing, MCP/realtime, embedded UI).

**Clients.**
- *Pi SDK* (`https://pi.dev`, `@earendil-works/pi-ai`) — multi-provider LLM harness. Configured to use the OpenAI provider with a custom `base_url`. Open verification item: confirm Pi's exact env var / config key for setting the OpenAI base URL.
- *LangFlow* — running locally with flows whose model components already point at `http://localhost:11434/api/chat`. Zero reconfiguration required.
- *loop24-client* / *GSD Pi* (`../loop24-client`, npm `@loop24/client` v1.0.1) — TypeScript Node CLI that calls `@anthropic-ai/sdk` (v0.90.x) and honors `ANTHROPIC_BASE_URL` for transport redirection. Uses both non-streaming `messages.create()` and streaming `messages.stream()` heavily; sends `x-api-key` or `Authorization: Bearer` auth plus the required `anthropic-version` header. Tool-use (`tool_use` content blocks with object `input`) and `thinking` blocks are first-class. Gateway integration verified by setting `ANTHROPIC_BASE_URL=http://localhost:11434` in the client environment.

**First Go project for the author.** Author is senior in JS / Python / shell with some Go familiarity but no greenfield Go experience. This shapes decisions toward stdlib + chi over fasthttp, `log/slog` over zerolog/zap, simple env-var config over viper. AI-assisted development is expected and the trust-gate suite is sized accordingly.

**Local-only repo at boot.** `~/Projects/repos/local/loop24-gateway`. Module name `otto-gateway` (bare) to defer the hosting decision. Bare module name will be revisited before the first remote push. (Working-directory rename `loop24-gateway/` → `otto-gateway/` is a deferred Tier 3 step.)

## Constraints

- **Tech stack**: Go 1.23+ — Required for `log/slog` ergonomics and post-1.22 `net/http` routing patterns. No cgo in the main binary (preserves trivial cross-compile).
- **Tech stack**: stdlib `net/http` + `chi` for routing — Rejected `fasthttp` (faster but breaks the `http.Handler` ecosystem; not worth it at our throughput).
- **Compatibility**: Ollama API endpoints and request/response shapes are fixed by existing LangFlow flows. Breaking changes there require a flow migration we're not paying for.
- **Compatibility**: OpenAI API shapes follow public OpenAI spec for the endpoints we serve. Pi SDK will fail on shape drift.
- **Compatibility**: Anthropic Messages API shapes follow the public Anthropic spec (`docs.anthropic.com/en/api/messages`) — including SSE event names, content block discriminators, tool-use `input` as object (not string), `anthropic-version` header requirement, and error envelope shape. `@anthropic-ai/sdk` will fail on shape drift; loop24-client uses both `x-api-key` and `Authorization: Bearer` auth paths so both must work.
- **Distribution**: Single static binary per OS/arch. Cross-compile from macOS dev box must work with vanilla `go build` plus `GOOS`/`GOARCH` env vars. The instant cgo enters the picture (e.g. in-process ONNX), this collapses — explicit decision in `docs/briefs/go_port_brief.md` §3.4 to avoid that.
- **Performance**: Must not be slower than the Node implementation under concurrent load. Tail latency should improve. Hard numbers: TBD; pre-implementation baseline measurement is in the milestone plan.
- **Security**: Bearer-token auth + IP allowlist, both env-driven. Same defaults as Node version (no auth if env unset). Subprocess spawn is the highest-risk surface — `gosec` G204 and friends required to flag any tainted-input regressions.
- **Security carve-outs**: Two intentional bearer-auth exemptions in v1; operators MUST be aware before exposing the gateway on untrusted networks.
  1. **`/admin` observability UI is auth-exempt by design** (Phase 6.1 D-01). The operator binds the gateway to localhost or fronts it with reverse-proxy auth (nginx `auth_basic`, oauth2-proxy, Cloudflare Access). See `docs/operating.md` section `### v1 no-auth posture` for the canonical rationale and operator guidance, and the `### Auth posture quick reference` index near the top of that doc.
  2. **Ollama list-mode stubs bypass `AuthHook`** (`/api/tags`, `/api/ps`, `/api/show`, `/api/copy`, `/api/delete`, `/api/pull`, `/api/push`, `/api/create`) because they do not route through the canonical engine. The IP allowlist (`ALLOWED_IPS`) still applies. These endpoints have no model-execution surface — accepted v1 risk. See `docs/operating.md` section `#### Accepted v1 risks` (under `### Phase 8 — Plugin chain (hooks)`) for the durable annotation.
- **Backward compat**: Environment variable names must match the Node version (`KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `POOL_SIZE`, `SESSION_TTL_MS`, `AUTH_TOKEN`, `ALLOWED_IPS`, `DEBUG`, `EMBEDDING_MODEL_DEFAULT`, etc.) so deployments can swap binaries.
- **Trust gates**: The lint/test/audit set in `docs/briefs/go_port_brief.md` §3.12 is non-negotiable from day one, not bolted on later. AI-assisted code without these guardrails generates plausible-looking-but-wrong async code patterns.

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go over Rust | Cross-compile triviality is the headline; first systems project for author; embeddings story acceptable via sidecar; Bifrost validates the pattern. Full trade-off in `docs/briefs/go_port_brief.md` §2 and `docs/briefs/rust_port_brief.md`. | — Pending |
| Triple API surface (OpenAI + Ollama + Anthropic) on one binary | LangFlow already configured for Ollama; Pi SDK uses OpenAI shape; loop24-client (GSD Pi) calls `@anthropic-ai/sdk` and supports `ANTHROPIC_BASE_URL` redirection. Splitting into three services adds deployment complexity for zero benefit; sharing one engine forces a cleanly separated canonical layer that's strictly better architecture. Anthropic added 2026-05-23 (this entry supersedes the original OpenAI+Ollama-only decision). | — Pending |
| Adapter-over-canonical layout | `internal/adapter/{ollama,openai,anthropic}` translate native ↔ canonical; `internal/engine` consumes canonical only. Mirrors Bifrost's `transports/integrations/` pattern. | — Pending |
| Anthropic surface ships SSE day-one (Phase 3.1, not deferred to Phase 4) | loop24-client's primary call path is `@anthropic-ai/sdk`'s `messages.stream()` — non-streaming `/v1/messages` alone is not a useful integration. Anthropic SSE has a different event structure than OpenAI (`message_start`/`content_block_*`/`message_delta`/`message_stop` instead of `data: <chunk>` + `data: [DONE]`); pushing it into Phase 4 would mean Phase 3.1 ships with no useful client integration. Decision: Phase 3.1 owns Anthropic SSE; Phase 4 retroactively ratifies all three formats off one canonical channel. | — Pending |
| `PreHook`/`PostHook` plugin chain for guardrails | Hooks on canonical types means one moderation/auth/budget rule covers both surfaces. Bifrost-inspired. Day-one footprint is small (RequestID, Auth, Logging); the seams allow content moderation, schema validation, budget, semantic cache as later additions without rewriting handlers. | — Pending |
| stdlib `net/http` + `chi` (reject `fasthttp`) | Bifrost uses fasthttp for throughput; our bottleneck is `kiro-cli` subprocess latency, not HTTP parsing. fasthttp breaks `http.Handler` ecosystem (testing, middleware, `r.Context()`). Not worth it for our scale. | — Pending |
| Trust-gate suite required from day one | AI-assisted development on a first-Go project. Strict `golangci-lint`, `gosec`, `govulncheck`, `-race`, `goleak`, property tests, architectural boundary linting. Derived from "Making AI-Generated Rust Code Trustworthy" (Garcia) adapted to Go tooling. | — Pending |
| Embeddings cut from v1 (2026-05-27) | Phase 7 removed from the milestone; `/api/embed`, `/api/embeddings`, `/v1/embeddings` are out of scope. Prior provisional "out-of-process sidecar" decision is now moot. Original LangFlow flows that use Ollama embeddings will need to retain access to a separate embeddings server. | — Final |
| Bare Go module name `otto-gateway` | Local-only at boot; defer hosting decision until first remote push. | — Pending |

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
*Last updated: 2026-06-04 — Milestone v1.5 "audit WARNINGs" SHIPPED. 13 phases done (01, 01.1, 02, 03, 03.1, 04, 05, 06, 06.1, 08, 08.1, 08.2, 08.3, 08.4, 9), 57 plans, 63/63 requirements satisfied including the v1.5-closing PII-01 (US Address PII Coverage). All three API surfaces (Ollama `/api/chat`, OpenAI `/v1/chat/completions`, Anthropic `/v1/messages`) routing through one canonical engine to a warm `kiro-cli` pool with the plugin guardrails chain enforcing auth, request-id, structured logging, and PII redaction. Single static binary cross-compiles to darwin-arm64/amd64, linux-amd64, windows-amd64 from macOS — v1.10.0 published to GitHub Releases with operator HUMAN-UAT confirming 33/33 needle checks across all 3 surfaces on the Windows splunk box. One phase (08.3.1 ACP Per-Session Stream Demux) deferred to v1.6 as multi-tenant concurrency hardening; not exploitable under v1's POOL_SIZE=4 model. Reverted 08.3.2 (fake-kiro-cli machinery) in favor of a prompt-only PII smoke fix. Carried v1.6 first-phase candidates: tree-wide gofumpt cleanup + go.mod 1.23 → 1.24 bump + Phase 08.3.1 bundle. Milestone artifacts archived at `.planning/milestones/v1.5-{ROADMAP,REQUIREMENTS,MILESTONE-AUDIT}.md`. Full per-phase history in `.planning/MILESTONES.md`.*
