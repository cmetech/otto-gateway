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
- ✅ **v1.7 Go Stdlib CVE Cleanup** — Phase 12 (shipped 2026-06-07). `go.mod` bumped 1.25.0 → 1.26.4; govulncheck 23 → 0; `make ci` exits 0 end-to-end without carve-outs for the first time since v1.5. [Archive](milestones/v1.7-ROADMAP.md) · [Audit](milestones/v1.7-MILESTONE-AUDIT.md)
- 🚧 **v1.8 Nyquist Coverage Uplift** — Phase 13 (active; opened 2026-06-07). Flip the 6 remaining v1.5 phase VALIDATION.md docs from `nyquist_compliant: false` to `nyquist_compliant: true` (compliance ratio 7/13 → 13/13) via the `gsd-nyquist-auditor` agent run in parallel across 6 independent target phases. Phase 08.3.1 ACP demux + Windows Authenticode re-deferred to v1.9+ per v1.8-opens narrow-scope decision.

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

<details>
<summary>✅ v1.7 Go Stdlib CVE Cleanup — SHIPPED 2026-06-07 (1 phase, 1 plan)</summary>

- [x] **Phase 12: Go toolchain CVE remediation** — Bumped `go.mod`'s `go` directive from `1.25.0` to `1.26.4` (two-step: 1.26.3 → tighten to 1.26.4 after Wave 1 surfaced 2 reachable residuals). Drained all 23 baseline stdlib CVEs (GO-2026-5039 through GO-2025-4007) to zero. `make ci` exits 0 end-to-end for the first time since v1.5 — closes v1.6 Phase 11 D-11-01 carve-out. CI run [27081876026](https://github.com/cmetech/otto-gateway/actions/runs/27081876026) all 3 jobs green. Production diff: `go.mod | 2 +-`. (2026-06-07)

Full per-phase detail: [v1.7-ROADMAP.md archive](milestones/v1.7-ROADMAP.md) · audit: [v1.7-MILESTONE-AUDIT.md](milestones/v1.7-MILESTONE-AUDIT.md)

</details>

### 🚧 v1.8 Nyquist Coverage Uplift (Active — opened 2026-06-07)

- [x] **Phase 13: Nyquist coverage uplift** — Cross-cutting sweep flipping the 6 v1.5 phase VALIDATION.md docs with `nyquist_compliant: false` (phases 02, 03, 06, 06.1, 08, 08.4) up to the post-08.1 validation standard. 6 independent plans (one per target phase) run as a single parallel wave via the `gsd-nyquist-auditor` agent; each plan owns one target phase's VALIDATION.md and any new `*_test.go` files needed to fill the per-task verification map. NYQ-02 / NYQ-03 / NYQ-06 / NYQ-06.1 / NYQ-08 / NYQ-08.4 / NYQ-ALL. (completed 2026-06-07)

**Re-deferred to v1.9+ (out of v1.8 scope per opening decision):**
- Phase 08.3.1 (ACP Per-Session Stream Demux) — awaits a real multi-tenant deployment driver.
- Windows Authenticode code-signing — awaits code-signing certificate procurement.

## Phase Details

### Phase 13: Nyquist coverage uplift
**Goal**: Flip the 6 v1.5 phase VALIDATION.md docs currently marked `nyquist_compliant: false` (phases 02, 03, 06, 06.1, 08, 08.4) to `nyquist_compliant: true` so the milestone-wide compliance ratio goes from 7/13 to 13/13 with no implementation changes — every shipped phase carries a complete per-task verification map, Wave 0 fixtures section, and manual-only written rationales.
**Depends on**: Phase 12 (v1.7 shipped — `make ci` exits 0 end-to-end; clean baseline so any new `*_test.go` files land on a green tree)
**Requirements**: NYQ-02, NYQ-03, NYQ-06, NYQ-06.1, NYQ-08, NYQ-08.4, NYQ-ALL
**Success Criteria** (what must be TRUE):
  1. `grep -l 'nyquist_compliant: true' .planning/phases/*/[0-9]*-VALIDATION.md | wc -l` reports **13** (was 7 at v1.8 open); `grep -l 'nyquist_compliant: false' .planning/phases/*/[0-9]*-VALIDATION.md | wc -l` reports **0**.
  2. For each of the 6 target VALIDATION.md docs (02, 03, 06, 06.1, 08, 08.4), the "Per-Task Verification Map" table contains one row for every task ID present in the corresponding PLAN.md — every row has either an automated `<verify>` command, a Wave 0 fixture reference, or an explicit manual-only rationale (no blank "Test Type" or "Automated Command" cells outside the manual-only column).
  3. For each of the 6 target VALIDATION.md docs, the "Validation Sign-Off" checklist has all six boxes ticked (no 3 consecutive tasks without automated verify, no watch-mode flags, Wave 0 covers all MISSING references, feedback latency under per-phase budget, frontmatter flipped).
  4. The `gsd-nyquist-auditor` agent's read-only-implementation rule held milestone-wide: `git diff main...HEAD -- ':!*_test.go' ':!*VALIDATION.md' ':!testdata/'` reports zero production-source edits attributable to Phase 13 plans (any ESCALATEd implementation bug landed as a separate post-Phase-13 phase).
  5. The v1.8 milestone audit (`.planning/milestones/v1.8-MILESTONE-AUDIT.md` once written) records verdict **passed** with NYQ-ALL marked Satisfied — confirmed by the same two `grep` commands from criterion 1 plus the per-target sign-off checklists from criterion 3.
**Plans**: 6 plans (single parallel wave; planned 2026-06-07)
- [x] 13-01-PLAN.md — NYQ-08.4 (Phase 08.4 US Address PII Coverage; 5 tasks; smallest target)
- [x] 13-02-PLAN.md — NYQ-06.1 (Phase 06.1 Admin Observability UI; 14 tasks; UI-shaped)
- [x] 13-03-PLAN.md — NYQ-03 (Phase 03 OpenAI Surface; 15 tasks; adapter pattern)
- [x] 13-04-PLAN.md — NYQ-02 (Phase 02 Ollama End-to-End; 23 tasks; first runnable slice)
- [x] 13-05-PLAN.md — NYQ-06 (Phase 06 Tool-Call Path; 23 tasks; engine + jsonformat)
- [x] 13-06-PLAN.md — NYQ-08 (Phase 08 Plugin Hook Chain; 31 tasks; largest target)

NYQ-ALL is satisfied automatically at milestone close once all 6 plans complete; no 7th plan.

## Progress

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1, 1.1, 2, 3, 3.1, 4, 5, 6, 6.1, 8, 8.1, 8.2, 8.3, 8.4, 9 | v1.5 | 57/57 | Complete | 2026-06-04 |
| 10, 11 | v1.6 | 5/5 | Complete | 2026-06-07 |
| 12 | v1.7 | 1/1 | Complete | 2026-06-07 |
| 13 | v1.8 | 6/6 | Complete   | 2026-06-07 |
