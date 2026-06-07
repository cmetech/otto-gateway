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
- 📋 **v1.6 Tooling Cleanup (Planned)** — drain the trust-gate violation backlog (golangci-lint v2 + gofumpt) and restore lint as a merge gate. Narrow-scope, ship-fast milestone.

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

### 📋 v1.6 Tooling Cleanup (Planned)

- [x] **Phase 10: golangci-lint v2 cleanup + re-gate** — drain the 49-issue v2 baseline to zero, then remove `continue-on-error: true` so lint failures block merges. (completed 2026-06-07)
- [ ] **Phase 11: gofumpt tree-wide cleanup + pre-commit gate** — flush `gofumpt -d .` to zero diffs and add a pre-commit gate (hook or `make pre-commit` target) so lint+fmt regressions cannot land silently again.

## Phase Details

### Phase 10: golangci-lint v2 cleanup + re-gate
**Goal**: `golangci-lint run` exits 0 on `main` and CI lint failures block merges.
**Depends on**: Phase 9 (v1.5 trust-gate baseline)
**Requirements**: LINT-01, LINT-02, LINT-03
**Success Criteria** (what must be TRUE):
  1. `~/go/bin/golangci-lint run --timeout=5m` against `.golangci.yml` (v2 schema, pin v2.1.6) exits 0 on a clean checkout of `main`.
  2. `.github/workflows/ci.yml`'s golangci-lint step has no `continue-on-error: true` and the TODO comment introduced in commit `f3a70fc` is gone; a CI run with a deliberately-introduced lint violation fails the job.
  3. Every linter category from the baseline (wrapcheck, unparam, revive, gosec, unused, noctx, staticcheck, bodyclose, nilerr) has a per-category decision record in the phase's PLAN.md or SUMMARY.md stating fix policy, rule disable, or exemption pattern with rationale.
  4. Any `//nolint:linter` directive added during the phase carries a `// <rationale>` comment in the diff that introduces it.
**Plans**: 4 plans
Plans:
- [x] 10-01-PLAN.md — Wave 1 mechanical drain: staticcheck QF1001 (3) + unused (4) + revive redefines-builtin-id (3) + gosec G301 (2) + noctx (4); per-category decision record for these 5 categories.
- [x] 10-02-PLAN.md — Wave 2 wrapcheck (9) + unparam (13) drain; production fixes for 2 unparam sites; 11 scoped //nolint:unparam exemptions with rationale; per-category decision record.
- [x] 10-03-PLAN.md — Wave 3 real review: gosec G703 (1) + gosec G705 (2) + bodyclose (1) + nilerr (1) + revive remainder (6: 3 stutters + 2 unexported-return + 1 godoc).
- [x] 10-04-PLAN.md — Wave 4 re-gate: remove continue-on-error + TODO from .github/workflows/ci.yml; negative-test PR proves lint job blocks merges.

### Phase 11: gofumpt tree-wide cleanup + pre-commit gate
**Goal**: `gofumpt -d .` reports no diffs on `main` and operators can't push lint/fmt regressions without surfacing them locally.
**Depends on**: Phase 10
**Requirements**: FMT-01, FMT-02, CI-01
**Success Criteria** (what must be TRUE):
  1. `gofumpt -d .` from a clean clone of `main` prints nothing and exits 0, including across `cmd/` and `internal/adapter/*` where the Phase 2/3.1/8 drift lives.
  2. `make ci` on a clean checkout runs the full brief §3.12 sequence (gofumpt → vet → build → lint → test-race → arch-lint → examples → govulncheck → cross) and exits 0 end-to-end.
  3. A pre-commit hook OR an explicit `make pre-commit` target invokes `gofumpt -l .` and `golangci-lint run` against staged files and blocks the commit/exits non-zero when violations are present; the hook-vs-make-target choice and rationale are recorded in the phase's PLAN.md.
  4. Documentation (operator-quickstart.md or DEVELOPERS.md) tells a fresh contributor how to enable the pre-commit gate.
**Plans**: TBD

## Progress

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1, 1.1, 2, 3, 3.1, 4, 5, 6, 6.1, 8, 8.1, 8.2, 8.3, 8.4, 9 | v1.5 | 57/57 | Complete | 2026-06-04 |
| 10 | v1.6 | 4/4 | Complete   | 2026-06-07 |
| 11 | v1.6 | 0/0 | Not started | — |
