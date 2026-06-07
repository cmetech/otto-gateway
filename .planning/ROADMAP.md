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
- 📋 **v1.7 Go Stdlib CVE Cleanup (Active)** — Phase 12. Drain the Go stdlib CVE backlog (`govulncheck` failures unmasked by v1.6 Phase 10's lint-gate restoration) so `make ci` exits 0 end-to-end without the v1.6 carve-out. Carryover items (Phase 08.3.1 ACP demux, Nyquist uplift, Windows Authenticode) explicitly re-deferred to v1.8.

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

### 📋 v1.7 Go Stdlib CVE Cleanup (Active)

- [ ] **Phase 12: Go toolchain CVE remediation** — Bump the `go` directive in `go.mod` (and any toolchain pin) to a patched 1.25.x / 1.26.x release that resolves the govulncheck-flagged stdlib CVE chain (GO-2026-5039, -5037, -4982, -4980, -4971, -4947, -4946, -4870, …), refresh transitive deps via `go mod tidy`, address residual application-level taints (fix or document unreachability), and confirm CI's Vulnerability scan step passes on `main`. Closes the v1.6 Phase 11 D-11-01 carve-out so `make ci` exits 0 end-to-end. CVE-01, CVE-02, CVE-03, CI-02.

## Phase Details

### Phase 12: Go toolchain CVE remediation
**Goal**: `make ci` exits 0 end-to-end on a clean checkout of `main` because the Go toolchain pin is on a patched release and `govulncheck ./...` reports zero findings (or only residuals with documented unreachability).
**Depends on**: v1.6 Phase 11 (lint + fmt gates already restored — Vulnerability scan is now the only red CI step)
**Requirements**: CVE-01, CVE-02, CVE-03, CI-02
**Success Criteria** (what must be TRUE):
  1. `govulncheck ./...` from a clean checkout of `main` exits 0 (or every residual finding is annotated with a written unreachability rationale in the phase's PLAN.md / SUMMARY.md).
  2. The `go` directive in `go.mod` is on a patched Go release (≥ the minimum patch level that closes the CVE-01 list); `go mod tidy` is clean and the commit message records the chosen version + the headline CVE it patches.
  3. CI's `Vulnerability scan` step (under the `lint + test-race + arch-lint + govulncheck` job in `.github/workflows/ci.yml`) is green on the milestone-closing commit on `main` — verifiable by run URL in the phase summary.
  4. `make ci` (gofumpt → vet → build → lint → test-race → arch-lint → examples → govulncheck → cross) exits 0 end-to-end on a clean checkout of `main` — closes the v1.6 Phase 11 D-11-01 `(govulncheck routed to v1.7)` carve-out.
  5. No opportunistic changes outside CVE scope: `git diff main..HEAD -- ':!go.mod' ':!go.sum' ':!.planning/**'` is empty of non-CVE-justified edits (residual taint fixes count as in-scope; refactors do not).
**Plans**: 1 plan
- [ ] 12-01-PLAN.md — Bump Go toolchain to a patched release (drains 23 stdlib CVEs); rerun govulncheck; close v1.6 D-11-01 carve-out; verify make ci exits 0 end-to-end (CVE-01, CVE-02, CVE-03, CI-02)
**Status**: Pending

## Progress

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1, 1.1, 2, 3, 3.1, 4, 5, 6, 6.1, 8, 8.1, 8.2, 8.3, 8.4, 9 | v1.5 | 57/57 | Complete | 2026-06-04 |
| 10, 11 | v1.6 | 5/5 | Complete | 2026-06-07 |
| 12 | v1.7 | 0/1 | In progress | — |
