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
- ✅ **v1.8 Nyquist Coverage Uplift** — Phase 13 (shipped 2026-06-07). Flipped the 6 remaining v1.5 phase VALIDATION.md docs from `nyquist_compliant: false` to `nyquist_compliant: true` (compliance ratio 7/13 → 13/13). Zero production source edits. 3 inherited operator-deferred UAT items tracked in 13-HUMAN-UAT.md. [Archive](milestones/v1.8-ROADMAP.md) · [Audit](milestones/v1.8-MILESTONE-AUDIT.md)
- ✅ **v1.9 Reliability Hardening** — Phases 14, 15, 16 (shipped 2026-06-11). 23 reliability findings (1 Critical + 8 High + 14 Mediums) closed; `-race` trust gate restored via REL-POOL-05 atomic.Int64 LastUsed; pool lifecycle hardened on all 3 OSes; mid-stream death surfaced honestly to clients; tray honest on macOS + Windows. 12 Lows deferred to v1.10.3. [Archive](milestones/v1.9-ROADMAP.md) · [Audit](milestones/v1.9-MILESTONE-AUDIT.md)
- ✅ **v1.9.1 Trust-Gate Restoration** *(folded into v1.10.2 release tag)* — Phase 17 (shipped 2026-06-11). `make ci` exit 0 end-to-end restored; arch-lint TRST-04 boundary preserved via `canonical.ErrPoolExhausted`; REL-POOL-02 goleak flake closed (60/60 race-clean); gosec G301/G306 + gofmt drift + dead-code closed. Release: [v1.10.2](https://github.com/cmetech/otto-gateway/releases/tag/v1.10.2).
- 🚧 **v1.10.3 Reliability Closeout** — Phases 18, 19, 20 (opened 2026-06-11). Close the 8 deferred Low-severity findings from the 2026-06-11 reliability review (C-4/5/6, H-6/7, O-2/3/4, T-8/9) + 1 production race surfaced by Phase 17 (acp.Stream s.mu ordering, REL-ACP-01) + 6 Info-level code review cleanups (QUAL-01..06). No new features; no surface expansion.

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

<details>
<summary>✅ v1.8 Nyquist Coverage Uplift — SHIPPED 2026-06-07 (1 phase, 6 plans)</summary>

- [x] **Phase 13: Nyquist coverage uplift** — Cross-cutting sweep flipping the 6 v1.5 phase VALIDATION.md docs with `nyquist_compliant: false` (phases 02, 03, 06, 06.1, 08, 08.4) to `nyquist_compliant: true` (compliance ratio 7/13 → 13/13). 6 independent plans run as a single parallel wave via the `gsd-nyquist-auditor` agent. NYQ-02 / NYQ-03 / NYQ-06 / NYQ-06.1 / NYQ-08 / NYQ-08.4 / NYQ-ALL. Zero production source edits. (2026-06-07)

Full per-phase detail: [v1.8-ROADMAP.md archive](milestones/v1.8-ROADMAP.md) · audit: [v1.8-MILESTONE-AUDIT.md](milestones/v1.8-MILESTONE-AUDIT.md)

**Re-deferred to v1.9+ (out of v1.8 scope per opening decision):**

- Phase 08.3.1 (ACP Per-Session Stream Demux) — awaits a real multi-tenant deployment driver.
- Windows Authenticode code-signing — awaits code-signing certificate procurement.

</details>

<details>
<summary>✅ v1.9 Reliability Hardening — SHIPPED 2026-06-11 (3 phases, 12 plans, 23 findings closed)</summary>

- [x] **Phase 14: Verify Reliability Findings** — Read-only audit of all 23 Critical/High/Medium findings against current `main`. Produced `14-VERIFICATION-LEDGER.md` (all `confirmed`, `needs_investigation_count: 0`). Zero production source edits.
- [x] **Phase 15: Fix Critical + High** — REL-POOL-01..03 (bounded acquire + cleanup + CAS-guarded activeStream); REL-HTTP-01..03 (shutdownCh + StopWatchdog removal + surface-native terminal frames); REL-TRAY-01..03 (PID identity + Windows support-bundle + macOS icon/tooltip).
- [x] **Phase 16: Fix Mediums** — REL-POOL-04..06 (per-request stream ctx + atomic LastUsed + taskkill /T /F); REL-HTTP-04..05 (body-read deadline + tailer cap); REL-HOOKS-01 (PostHook on error paths); REL-TRAY-04..07 (non-blocking notify + status enum consumer + bundle bounds); REL-CFG-01..04 (fail-fast boot validation + warnOnce park log).

Full details: [milestones/v1.9-ROADMAP.md](milestones/v1.9-ROADMAP.md)

</details>

<details open>
<summary>🚧 v1.10.3 Reliability Closeout — IN PROGRESS (opened 2026-06-11, 3 phases planned)</summary>

- [ ] **Phase 18: Reliability long-tail** — Close 10 of the 11 deferred Low-severity reliability findings as a single phase with 3 parallel plans. Config hardening (REL-CFG-05/06/07): degenerate ALLOWED_IPS/AUTH_TOKEN env values rejected loudly, KIRO_CMD/KIRO_CWD errors named, port-in-use discovered pre-warmup. Observability symmetry (REL-OBSV-02/03/04, REL-HTTP-06/07): worker recovery logged, kiro-cli stderr routed to structured log file, admin log-tail path single-sourced, Ollama streaming error WARN-logged, panic recovery on tailer/watchdog/ctx-watcher goroutines. Tray honesty (REL-TRAY-08/09): dotenv read errors loud, macOS tray diagnostics either correct or removed.
- [x] **Phase 19: acp.Stream concurrency fix** — REL-ACP-01: `acp.Stream.Result` copies `*s.result` under `s.mu` instead of racing close(s.done) against the StopReason write. Single-plan phase. After landing, the Phase 17 test-side drain-Chunks-then-Result workaround in `regression_rel_pool_02_test.go` can be reverted (verified by 60/60 race-clean iterations).
- [x] **Phase 20: Code-review backlog burn-down** — Single-plan mechanical batch closing 6 Info-level findings deferred from Phase 16/17 reviews: escapeApplescript newline/control-char escape (QUAL-01), tooltipForState shared build-tag dedup (QUAL-02), forceCloseCh contract documented or relocated (QUAL-03), tailLines O(n²) prepend replaced with collect-then-reverse (QUAL-04), dead sessions/sessionsMu vars removed from REL-POOL-02 test (QUAL-05), stale removeSlot comment fixed in respawn_ctx_cancel_test (QUAL-06). Refactor-only; no behavior change. (completed 2026-06-12)

</details>

## Phase Details

v1.9 phase details archived to [milestones/v1.9-ROADMAP.md](milestones/v1.9-ROADMAP.md).
v1.10.3 phase details captured in REQUIREMENTS.md (15 REQ-IDs traced to Phases 18/19/20).
(Prior milestones' details retained in their respective `milestones/v{ver}-ROADMAP.md` archives.)

## Progress

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1, 1.1, 2, 3, 3.1, 4, 5, 6, 6.1, 8, 8.1, 8.2, 8.3, 8.4, 9 | v1.5 | 57/57 | Complete | 2026-06-04 |
| 10, 11 | v1.6 | 5/5 | Complete | 2026-06-07 |
| 12 | v1.7 | 1/1 | Complete | 2026-06-07 |
| 13 | v1.8 | 6/6 | Complete    | 2026-06-07 |
| 14 | v1.9 | 4/4 | Complete    | 2026-06-11 |
| 15 | v1.9 | 3/3 | Complete   | 2026-06-11 |
| 16 | v1.9 | 5/5 | Complete   | 2026-06-11 |

### Phase 17: Trust-Gate Restoration

**Goal:** Restore `make ci` to clean exit-0 end-to-end so the v1.9.1 release tag can ship from a build-green baseline. Close six trust-gate items surfaced at v1.9 milestone close: TRST-04 arch-lint boundary violation (adapters importing pool), REL-POOL-02 goleak flake (~1/8 fail rate), gofmt drift in server.go, gofumpt drift in two regression test files, gosec G301+G306 in tray.go support-bundle path, and dead-code removal of Pool.removeSlot.
**Requirements**: TRST-04-RESTORE, REL-POOL-01-RELOCATE, REL-POOL-02-DEFLAKE, REL-FMT-GOFMT, REL-FMT-GOFUMPT, REL-LINT-G301, REL-LINT-G306, REL-LINT-UNUSED
**Depends on:** Phase 16
**Plans:** 3/3 plans executed

Plans:

- [x] 17-01-PLAN.md — Move ErrPoolExhausted to canonical package, restore TRST-04 boundary (D-17-01) [2026-06-11 — f727b24]
- [x] 17-02-PLAN.md — Deflake TestRegression_REL_POOL_02_CtrlCOrphansChildren to 20/20 under -race (D-17-04) [2026-06-11 — ca258f9]
- [x] 17-03-PLAN.md — Mechanical batch: gofmt + gofumpt + gosec G301/G306 + Pool.removeSlot dead-code removal (D-17-02) [2026-06-11 — b78fd09]

### Phase 18: Reliability long-tail

**Goal:** Close 10 of the 11 deferred Low-severity reliability findings from the 2026-06-11 audit (the 11th, REL-ACP-01, is Phase 19) as three loosely-coupled fix areas with zero file overlap: config hardening (loud Warn on degenerate ALLOWED_IPS/AUTH_TOKEN, named KIRO_CMD/KIRO_CWD errors with `~` expansion, bind-then-close port probe pre-warmup), observability symmetry (kiro-cli stderr → structured slog, worker recovery INFO mirroring death log, Ollama eng.Run failure WARN, panic-recover at 4 goroutine/callback sites, single Config.AdminTailPath source for writer + tailer), and tray honesty (sentinel-driven StateError for dotenv parse failures, remove two broken macOS support-bundle rows).
**Requirements**: REL-CFG-05, REL-CFG-06, REL-CFG-07, REL-HTTP-06, REL-HTTP-07, REL-OBSV-02, REL-OBSV-03, REL-OBSV-04, REL-TRAY-08, REL-TRAY-09
**Depends on:** Phase 17
**Plans:** 3/3 plans complete
Plans:

- [x] 18-01-PLAN.md — Config hardening (REL-CFG-05/06/07; D-18-01/02/03)
- [x] 18-02-PLAN.md — Observability symmetry + HTTP error logging (REL-HTTP-06/07, REL-OBSV-02/03/04; D-18-04/05/06/07/08)
- [x] 18-03-PLAN.md — Tray honesty (REL-TRAY-08/09; D-18-09/10)

### Phase 19: acp.Stream concurrency fix

**Goal:** Close REL-ACP-01 — the production race in `acp.Stream.Result` flagged by Phase 17 (17-02-SUMMARY Threat Flags). `Result()` copies `*s.result` into a stack-local under `s.mu` and returns the snapshot pointer; signature `(*FinalResult, error)` unchanged. New 60-iteration race-loop regression test at `internal/acp/regression_rel_acp_01_test.go`. Phase 17 test-side drain-Chunks-then-Result workaround in `internal/pool/regression_rel_pool_02_test.go` surgically reverted (workaround was load-bearing only for the race this phase closes).
**Requirements**: REL-ACP-01
**Depends on:** Phase 18
**Plans:** 1/1 plans complete
Plans:

- [x] 19-01-PLAN.md — Result() copy-under-lock + RED race-loop test + surgical revert of Phase 17 workaround (D-19-01 + D-19-02 + D-19-03)

### Phase 20: Code-review backlog burn-down

**Goal:** Close 6 Info-level findings deferred from Phase 16/17 code reviews as a single mechanical-refactor batch: QUAL-01 escapeApplescript escape-set expansion + unit tests; QUAL-02 tooltipForState dedup into a shared build-tag file; QUAL-03 forceCloseCh allocation relocated to RunUntilSignal (nil-channel select-never idiom); QUAL-04 tailLines O(n²) prepend replaced with collect-then-reverse; QUAL-05 dead sessions/sessionsMu vars removed from REL-POOL-02 test; QUAL-06 stale removeSlot comment fixed in respawn_ctx_cancel_test.
**Requirements**: QUAL-01, QUAL-02, QUAL-03, QUAL-04, QUAL-05, QUAL-06
**Depends on:** Phase 19
**Plans:** 1/1 plans complete
Plans:

- [x] 20-01-PLAN.md — Close QUAL-01..06 as 6 atomic refactor commits (D-20-01..09)
