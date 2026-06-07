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

## Milestones

- ✅ **v1.5 audit WARNINGs** — Phases 1, 1.1, 2, 3, 3.1, 4, 5, 6, 6.1, 8, 8.1, 8.2, 8.3, 8.4, 9 (shipped 2026-06-04). [Archive](milestones/v1.5-ROADMAP.md)
- ✅ **v1.6 Tooling Cleanup** — Phases 10, 11 (shipped 2026-06-07). golangci-lint v2 baseline drained from 49→0, CI lint gate restored, gofumpt clean tree-wide, pre-commit hook enabled. [Archive](milestones/v1.6-ROADMAP.md) · [Audit](milestones/v1.6-MILESTONE-AUDIT.md)
- 📋 **v1.7 (Planned)** — Go stdlib CVE backlog (unmasked by Phase 10 govulncheck) + Phase 08.3.1 ACP per-session stream demux + Nyquist coverage uplift + Windows Authenticode code-signing. Scope finalized via `/gsd-new-milestone v1.7`.

## Phases

<details>
<summary>✅ v1.5 audit WARNINGs — SHIPPED 2026-06-04 (13 phases, 57 plans)</summary>

- [x] **Phase 1: Foundations** — Scaffold, trust-gate suite, ACP JSON-RPC client over `kiro-cli` stdio (2026-05-23)
- [x] **Phase 1.1: ACP Wire Alignment** *(INSERTED)* — Fix 10 Phase 1 wire-shape defects vs the working Node impl + live ACP spec (2026-05-23)
- [x] **Phase 2: Ollama End-to-End** — First runnable slice: LangFlow `POST /api/chat` reaches real `kiro-cli` (2026-05-24)
- [x] **Phase 3: OpenAI Surface** — Pi-SDK `POST /v1/chat/completions` shares the same canonical engine (2026-05-25)
- [x] **Phase 3.1: Anthropic Surface** *(INSERTED)* — loop24-client `POST /v1/messages` with Anthropic SSE shares the same canonical engine (2026-05-24)
- [x] **Phase 4: Streaming** — NDJSON (Ollama) and SSE (OpenAI + Anthropic) off one canonical chunk channel, with disconnect cancellation (2026-05-25)
- [x] **Phase 5: Pool + Stateful Sessions** — Warm `POOL_SIZE` pool plus `X-Session-Id` registry, both visible on `/health/agents` (2026-05-26)
- [x] **Phase 6: Tool-Call Path** — Canonical tool calls rendered per-surface, with `coerceToolCall` for plain-JSON-as-text (2026-05-27)
- [x] **Phase 6.1: Admin Observability UI** *(INSERTED)* — Dark-mode `/admin` page rendering `/health` + `/health/agents` with brand palette (2026-05-28)
- [x] **Phase 8: Plugin Hook Chain** — `PreHook`/`PostHook` over canonical types, with RequestID, Auth, Logging, PII redaction (2026-05-28)
- [x] **Phase 8.1: Close gap INTEG-01 + v1.5 audit WARNINGs** *(INSERTED)* — Streaming-mode PreHook short-circuit fix + auth posture docs + REQUIREMENTS.md traceability fixes (2026-05-30)
- [x] **Phase 8.2: Ollama `format` Parity** *(INSERTED)* — LangFlow `format:"json"` / `format:<schema>` steered via canonical PreHook (GEN_RULES block); response fence-stripped (2026-06-03)
- [x] **Phase 8.3: ACP Prompt() Non-Blocking Refactor** *(INSERTED)* — Closes 64-slot chunk-buffer-overflow deadlock; `Prompt()` returns *Stream early, finalize via goroutine (2026-06-03)
- [x] **Phase 8.4: US Address PII Coverage** *(INSERTED)* — Three regex recognizers (USAddress, USState, USZIP) + validateUSZIPRange ahead of NER; PII-01 added; v1.10.0 released (2026-06-04)
- [x] **Phase 9: Distribution** — Cross-compile Linux+Windows+darwin from macOS, full trust-gate CI matrix gating merges (2026-05-28)

**Reverted (kept for history):**
- Phase 08.3.2 (PII Smoke Test Methodology Fix) — superseded by prompt-only fix in `scripts/test-pii.{ps1,sh}` (REVERTED 2026-06-04, commit `ff10594`)

**Deferred to v1.6/v1.7:**
- Phase 08.3.1 (ACP Per-Session Stream Demux) — WR-04 cross-session leak race not exploitable under v1's POOL_SIZE=4 pool model (each `acp.Client` bound to one worker slot). Re-scoped to v1.7 per v1.6 narrow-scope decision.

Full per-phase detail: [v1.5-ROADMAP.md archive](milestones/v1.5-ROADMAP.md)

</details>

<details>
<summary>✅ v1.6 Tooling Cleanup — SHIPPED 2026-06-07 (2 phases, 5 plans)</summary>

- [x] **Phase 10: golangci-lint v2 cleanup + re-gate** — Drain the 49-issue v2 baseline to zero across 3 waves of category-grouped fixes, then remove `continue-on-error: true` and prove the gate fires via negative-test PR #1. Wave 4's "single ci.yml edit" expanded into a 5-commit closeout of latent v2-schema migration rot (gofumpt drift + action v6→v7 + version v2.1.6→v2.12.2 + wrapcheck.ignoreSigs→extra-ignore-sigs). LINT-01/02/03. (2026-06-07)
- [x] **Phase 11: gofumpt tree-wide cleanup + pre-commit gate** — FMT-01 already at 0 thanks to Phase 10 work; verified. FMT-02 §3.12 sequence exits 0 minus the v1.7-routed govulncheck step. CI-01 added gofumpt hook to existing `.pre-commit-config.yaml` (via `scripts/pre-commit-gofumpt.sh` shell delegate per D-11-03) + enablement docs in `docs/operating.md`. (2026-06-07)

Full per-phase detail: [v1.6-ROADMAP.md archive](milestones/v1.6-ROADMAP.md) · audit: [v1.6-MILESTONE-AUDIT.md](milestones/v1.6-MILESTONE-AUDIT.md)

</details>

### 📋 v1.7 (Planned)

To be scoped via `/gsd-new-milestone v1.7`. Carried-forward candidates:

- [ ] **Go stdlib CVE backlog (unmasked by Phase 10).** `govulncheck ./...` fails on multiple Go stdlib CVEs (GO-2026-5039, -5037, -4982, -4980, -4971, -4947, -4946, -4870, …). Pre-existed v1.6 but were hidden behind the failing lint step. Starting move: bump Go toolchain pin to a patched 1.25.x or 1.26.x release.
- [ ] **Phase 08.3.1: ACP Per-Session Stream Demux** (carried from v1.5, re-deferred from v1.6). Replace single-slot `c.activeStream` with per-sessionID map; closes WR-04 silent cross-session leak race.
- [ ] **Nyquist coverage uplift.** 3/11 v1.5 phases fully compliant.
- [ ] **Windows Authenticode code-signing.** Seed `001-authenticode-code-signing-windows-distribution`.

## Progress

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1, 1.1, 2, 3, 3.1, 4, 5, 6, 6.1, 8, 8.1, 8.2, 8.3, 8.4, 9 | v1.5 | 57/57 | Complete | 2026-06-04 |
| 10, 11 | v1.6 | 5/5 | Complete | 2026-06-07 |
