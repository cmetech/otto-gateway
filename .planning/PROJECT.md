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

## Current State

**Shipped:** v1.9 Reliability Hardening (2026-06-11)
- 23 reliability findings closed (1 Critical + 8 High + 14 Medium); `-race` trust gate restored via REL-POOL-05 atomic.Int64 LastUsed; pool lifecycle hardened on Linux/darwin/Windows; mid-stream death surfaced honestly to OpenAI + Ollama + Anthropic clients; tray honest on macOS + Windows; config fail-closed.
- Audit: [milestones/v1.9-MILESTONE-AUDIT.md](milestones/v1.9-MILESTONE-AUDIT.md) — 27/27 requirements, 8/8 cross-phase seams WIRED, 26/26 threats CLOSED.

## Next Milestone Goals

**v1.10 (planned):** Close the 12 Low-severity findings deferred from v1.9 (P-7, P-8, H-6, H-7, T-8, T-9, C-4, C-5, C-6, O-2, O-3, O-4). Carryover candidates: Phase 08.3.1 ACP Per-Session Stream Demux (awaits multi-tenant deployment driver), Windows Authenticode code-signing (awaits cert procurement). Run `/gsd-new-milestone` to scope and define requirements.

<details>
<summary>v1.9 Reliability Hardening — full scope (collapsed)</summary>

**Goal:** Drive the 23 Critical/High/Medium findings from `docs/reviews/2026-06-11-reliability-review.md` to closure — kill silent-failure modes and orphaned-process paths under the everyday laptop-shutdown / sleep-wake / mid-stream-disconnect scenarios.

**Target work (23 findings, scoped from a 35-finding review at commit `9212d5b`):**
- **Pool / ACP lifecycle (6):** P-1 (Critical: pool→0 then every request hangs), P-2 (Ctrl-C orphans kiro-cli trees), P-3 (stale `awaitPromptResult` clobbers next stream → silent empty 200), P-4 (slow consumer blocks readLoop → ping SIGKILLs healthy worker), P-5 (data race on `Entry.LastUsed`), P-6 (Windows pgid kill is a no-op)
- **HTTP surface (5):** H-1 (shutdown blocks 30s on admin SSE → exit 1), H-2 (OpenAI idle-timeout returns hung worker to free pool without cancel), H-3 (silent mid-stream truncation on OpenAI + Ollama), H-4 (no body-read deadline → hour-scale stalls), H-5 (admin tailer 1 MB cap bypassed by newline-terminated lines)
- **Goroutine / hooks (1):** G-1 (non-streaming aggregation skips PostHooks → unbounded `sync.Map` leak in LoggingHook/ChatTraceHook)
- **Tray / UI (7):** T-1 (PID identity unchecked → Stop/Restart can kill innocent recycled PID), T-2 (Windows support-bundle `exit 1` aborts collection when gateway is down), T-3 (gateway death effectively invisible on macOS), T-4 (Windows `notify()` is a blocking modal on the uiLoop), T-5 (tray shows running while pool wedged), T-6 (Windows bundle-path stdout pollution), T-7 (support-bundle size/time unbounded → SIGKILL + leaked staging dir)
- **Config / observability (4):** C-1 (silent coercion of zero/negative pool/session knobs), C-2 (`PING_INTERVAL<0` crashes with raw goroutine panic, not config error), C-3 (`EMBEDDING_MODEL_DEFAULT` documented but never read), O-1 (pool exhaustion completely silent at default log level)

**Phase structure (by severity):**
- **Phase 14: Verify reliability findings** — Audit each of the 23 against current source. Tag confirmed / false-positive / needs-deeper-investigation. No fix work. Gate that fix phases consume.
- **Phase 15: Fix Critical + High** (9 findings: P-1, P-2, P-3, H-1, H-2, H-3, T-1, T-2, T-3) — Load-bearing failure paths only.
- **Phase 16: Fix Mediums** (14 findings: P-4, P-5, P-6, H-4, H-5, G-1, T-4, T-5, T-6, T-7, C-1, C-2, C-3, O-1) — Includes P-5 (the `-race` regression) so trust-gate posture is restored.

**Out of scope (rolled to v1.10 backlog):** All 12 Low findings from the review (P-7, P-8, H-6, H-7, T-8, T-9, C-4, C-5, C-6, O-2, O-3, O-4). Carryover candidates from v1.8: Phase 08.3.1 ACP Per-Session Stream Demux (awaits multi-tenant deployment driver), Windows Authenticode code-signing (awaits cert procurement).

</details>

## Previous Milestone: v1.8 Nyquist Coverage Uplift (SHIPPED 2026-06-07)

<details>
<summary>v1.8 SHIPPED — 1 phase, 6 plans, 36 commits, 7/7 REQ-IDs (zero carve-outs)</summary>

**Delivered:** 6 v1.5 phase VALIDATION.md docs flipped from `nyquist_compliant: false` to `nyquist_compliant: true` — phases 02 (Ollama E2E), 03 (OpenAI Surface), 06 (Tool-Call Path), 06.1 (Admin Obs UI), 08 (Plugin Hook Chain), 08.4 (US Address PII Coverage). Milestone-wide compliance ratio: **7/13 → 13/13**. Each target VALIDATION.md now carries a complete per-task verification map (4–26 task rows), Wave 0 fixtures, manual-only rationales, and all 6 sign-off boxes ticked.

**Production diff:** Zero non-test source edits across all 6 plans (read-only-implementation rule held milestone-wide). `git diff main...HEAD -- ':!*_test.go' ':!*VALIDATION.md' ':!testdata/' ':!.planning/'` empty.

**Execution shape:** 6 plans dispatched as a single parallel wave via the `gsd-nyquist-auditor` agent (6 git worktrees). Phase verification returned `human_needed` with 3 pre-existing operator-deferred UAT items (Phase 08.4 PII smoke, loop24-client tool-call UAT, Phase 06 `TestE2E_Tools_Cancel`); user approved and the items persist in 13-HUMAN-UAT.md.

**Notable workflow note:** Two stray untracked files (`13-01-SUMMARY.md`, `13-04-GAPS.txt`) appeared in the main worktree before the SDK cleanup-wave merge; verified byte-identical to the worktree-committed versions and recovered via manual `git merge --no-ff` per worktree. Root cause likely the cleanup helper's pre-merge check leaking files — manual merge path worked cleanly. Tracking-debt observation: SUMMARY.md `requirements_completed` frontmatter empty for 4 of 6 plans (substantively complete via PLAN frontmatter + shell-criterion verification).

**Archived:** [`milestones/v1.8-ROADMAP.md`](milestones/v1.8-ROADMAP.md) · [`milestones/v1.8-REQUIREMENTS.md`](milestones/v1.8-REQUIREMENTS.md) · [`milestones/v1.8-MILESTONE-AUDIT.md`](milestones/v1.8-MILESTONE-AUDIT.md) (audit verdict: passed, 7/7 requirements satisfied)

</details>

## Earlier: v1.7 Go Stdlib CVE Cleanup (SHIPPED 2026-06-07)

<details>
<summary>v1.7 SHIPPED — 1 phase, 1 plan, 8 commits, 4/4 REQ-IDs (zero carve-outs)</summary>

**Delivered:** `go.mod`'s `go` directive bumped from `1.25.0` to `1.26.4` (two-step: 1.26.3 → tighten to 1.26.4 after Wave 1 surfaced 2 reachable residuals). All 23 baseline stdlib CVEs (GO-2026-5039 through GO-2025-4007) drained to zero. `make ci` exits 0 end-to-end for the first time since v1.5 shipped — closes v1.6 Phase 11 D-11-01 carve-out. CI run [27081876026](https://github.com/cmetech/otto-gateway/actions/runs/27081876026) reports all 3 jobs green (lint+test-race+arch-lint+govulncheck, publish-dry-run, cross-compile).

**Production diff:** `go.mod | 2 +-` (one line). Scope discipline held: zero opportunistic edits.

**Notable decision D-12-01:** Two-step Go bump (1.25.0 → 1.26.3 → 1.26.4). The executor initially targeted 1.26.3 to match the developer's local toolchain. Wave 1's govulncheck run found 2 reachable residuals (GO-2026-5039 in net/http, GO-2026-5037 in x509). Rather than `//nolint:gosec` them, tightening to 1.26.4 (minimum patch level that closes all 23 CVEs) was the cleaner outcome — zero residual taints, zero exemptions.

**Archived:** [`milestones/v1.7-ROADMAP.md`](milestones/v1.7-ROADMAP.md) · [`milestones/v1.7-REQUIREMENTS.md`](milestones/v1.7-REQUIREMENTS.md) · [`milestones/v1.7-MILESTONE-AUDIT.md`](milestones/v1.7-MILESTONE-AUDIT.md) (audit verdict: passed, zero warnings)

</details>

## Earlier: v1.6 Tooling Cleanup (SHIPPED 2026-06-07)

<details>
<summary>v1.6 SHIPPED — 2 phases, 5 plans, 31 commits, 6/6 REQ-IDs (1 with documented v1.7 carve-out)</summary>

**Delivered:** golangci-lint v2 baseline drained from 49 violations to 0; `.github/workflows/ci.yml` lint step is once again a hard merge gate (negative-test PR #1 confirmed); `gofumpt -d .` clean tree-wide; `.pre-commit-config.yaml` extended with a gofumpt hook + `scripts/pre-commit-gofumpt.sh` shell delegate; `docs/operating.md` documents `pre-commit install` enablement for fresh contributors.

**Key wins:** Phase 10 Wave 4 caught 5 layers of latent CI-config rot the gate's absence had been hiding (gofumpt drift in request_id.go; `golangci-lint-action@v6` incompatible with v2.x → bump to `@v7`; `v2.1.6` built with Go 1.24 vs go.mod 1.25.0 → bump to v2.12.2; `wrapcheck.ignoreSigs` was v1 schema → migrate to v2 `extra-ignore-sigs`). Phase 11 was kept narrow per CLAUDE.md discipline — single plan, four tasks.

**Out-of-scope carryover to v1.7:** Phase 10's gate restoration unmasked Go stdlib CVE failures in `govulncheck`. Captured in [`10-04-SUMMARY.md`](phases/10-golangci-lint-v2-cleanup-re-gate/10-04-SUMMARY.md) "Unmasked follow-up".

**Archived:** [`milestones/v1.6-ROADMAP.md`](milestones/v1.6-ROADMAP.md) · [`milestones/v1.6-REQUIREMENTS.md`](milestones/v1.6-REQUIREMENTS.md) · [`milestones/v1.6-MILESTONE-AUDIT.md`](milestones/v1.6-MILESTONE-AUDIT.md)

</details>

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

<!-- Current scope for v1.9 Reliability Hardening. -->

v1.9 ("Reliability Hardening") milestone scope — opened 2026-06-11 via `/gsd-new-milestone v1.9`. Detailed REQ-ID list in `.planning/REQUIREMENTS.md`. High-level categories:

- [ ] **REL-VERIFY-\***: Phase 14 — confirm each of the 23 review findings is real against current source before any fix work
- [ ] **REL-POOL-\***: Subprocess pool / ACP lifecycle reliability (no permanent pool shrink; no orphaned process trees; no silent stream-clobber on slot reuse; correct Windows process-tree kill; data-race-free session metadata)
- [ ] **REL-HTTP-\***: HTTP surface reliability (graceful shutdown that doesn't hang on long-lived SSE; idle-timeout that cancels the hung worker; mid-stream worker death surfaces as an explicit error frame on all three surfaces; bounded request-body read; admin tailer line cap enforced)
- [ ] **REL-HOOKS-\***: PostHook discipline on non-streaming error paths (no `sync.Map` leaks, no missing chat-trace records on failed requests)
- [ ] **REL-TRAY-\***: Tray + wrapper reliability (PID-identity check before stop/restart; macOS gateway-death surfacing; non-modal Windows notify; tray probe uses `/health/pool`; bundle-path parsing tolerant of stdout chatter; bounded bundle size/time)
- [ ] **REL-CFG-\***: Config + observability (fail-fast on negative/zero pool/session/ping knobs; pool-wait diagnostic visible at default log level; degenerate `EMBEDDING_MODEL_DEFAULT` documented or removed)

Source of truth for the underlying findings: [`docs/reviews/2026-06-11-reliability-review.md`](../docs/reviews/2026-06-11-reliability-review.md).

**Deferred to v1.7 (explicitly out of v1.6 scope):**

- **Go stdlib CVE backlog (unmasked by Phase 10)** — `govulncheck ./...` fails on `main` with multiple Go stdlib CVEs (GO-2026-5039, -5037, -4982, -4980, -4971, -4947, -4946, -4870, …). These pre-existed v1.6 but were hidden because Phase 10's lint step always failed first; Phase 10 Wave 4's gate restoration exposed them. v1.7 starting move: bump the Go toolchain pin to the latest 1.25.x or 1.26.x patch series, re-run `govulncheck`, and clean up any remaining application-level taints. Captured in `.planning/phases/10-golangci-lint-v2-cleanup-re-gate/10-04-SUMMARY.md` "Unmasked follow-up".
- **Phase 08.3.1: ACP Per-Session Stream Demux** — deferred from v1.5. Replace single-slot `c.activeStream *Stream` with per-sessionID map; closes WR-04 silent cross-session leak race. Needed only for multi-tenant scenarios v1 does not run.
- **Nyquist coverage uplift** — 3/11 phases fully compliant in v1.5. Bring older phases up to the post-08.1 validation standard.
- **Authenticode code-signing for Windows** — seed `001-authenticode-code-signing-windows-distribution` documents the rationale.
- **go.mod 1.23 → 1.24 bump** — **resolved**: go.mod was already advanced to `go 1.25.0` during v1.5 work; this carryover item is moot.

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
*Last updated: 2026-06-11 — Milestone v1.9 "Reliability Hardening" OPENED. Driver: `docs/reviews/2026-06-11-reliability-review.md` (35 findings; 23 in scope as Critical/High/Medium, 12 Lows deferred to v1.10). Phase shape: 14 (Verify) → 15 (Critical+High fixes, 9) → 16 (Medium fixes, 14). Phase 16 includes P-5 (`Entry.LastUsed` data race) so trust-gate `-race` posture is restored. Earlier: Milestone v1.8 "Nyquist Coverage Uplift" SHIPPED. 1 phase (Phase 13), 6 plans, 7/7 REQ-IDs (NYQ-02/03/06/06.1/08/08.4/ALL) satisfied, zero carve-outs. Single-day milestone. 36 commits, 32 files changed, +3113/-239 LOC. Compliance ratio flipped 7/13 → 13/13 with zero production source edits. 3 inherited operator-deferred UAT items tracked in `13-HUMAN-UAT.md` (Phase 08.4 PII smoke, loop24-client tool-call UAT, Phase 06 `TestE2E_Tools_Cancel`). Milestone audit verdict: passed, zero blockers. Carryover to next milestone: Phase 08.3.1 ACP demux + Windows Authenticode (both await external triggers). Earlier: Milestone v1.7 "Go Stdlib CVE Cleanup" SHIPPED. 1 phase (Phase 12), 1 plan, 4/4 REQ-IDs (CVE-01/02/03 + CI-02) satisfied, zero carve-outs. Single-day milestone. 8 commits, 9 files changed, +892/-34 LOC (production diff: `go.mod | 2 +-`). `go.mod` bumped 1.25.0 → 1.26.4 (two-step per D-12-01: 1.26.3 surfaced 2 reachable residuals in net/http and x509, tightened to 1.26.4 — minimum patch level that closes all 23 baseline stdlib CVEs from GO-2026-5039 through GO-2025-4007). `govulncheck ./...` clean. `make ci` exits 0 end-to-end for the first time since v1.5 shipped — closes v1.6 Phase 11 D-11-01 carve-out. CI run 27081876026 confirms all 3 jobs green (lint+test-race+arch-lint+govulncheck, publish-dry-run, cross-compile). Audit verdict: passed, zero warnings. v1.8 backlog: Phase 08.3.1 ACP demux (re-re-deferred) + Nyquist coverage uplift (6 of 13 v1.5 phases non-compliant) + Windows Authenticode signing (cert procurement). Milestone artifacts archived at `.planning/milestones/v1.7-{ROADMAP,REQUIREMENTS,MILESTONE-AUDIT}.md`. Full per-phase history in `.planning/MILESTONES.md`.*
