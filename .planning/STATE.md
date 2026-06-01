---
gsd_state_version: 1.0
milestone: v1.5
milestone_name: audit WARNINGs
status: executing
stopped_at: context exhaustion at 82% (2026-05-31)
last_updated: "2026-05-31T15:20:00.000Z"
last_activity: 2026-05-31 -- quick-260531-oax completed: add -Trace switch to otto-gw.ps1 (Windows parity)
progress:
  total_phases: 12
  completed_phases: 11
  total_plans: 54
  completed_plans: 54
  percent: 92
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-05-23)

**Core value:** All three API surfaces (OpenAI for Pi SDK, Ollama for LangFlow, Anthropic for loop24-client/GSD Pi) serve their respective clients without those clients knowing kiro-cli exists, with one place to enforce policy.
**Current focus:** Phase 08.1 — close-gap-integ-01-streaming-mode-prehook-short-circuit-v1-5

## Current Position

Phase: 08.1 (close-gap-integ-01-streaming-mode-prehook-short-circuit-v1-5) — EXECUTING
Plan: 1 of 5
Status: Executing Phase 08.1
Next: `/gsd-discuss-phase 8.1` to gather context, then `/gsd-plan-phase 8.1`, then `/gsd-execute-phase 8.1`
Last activity: 2026-05-31 - Completed quick task 260531-ra6: kiro-lifecycle hygiene (slot-release regression test + pgid + wrapper reap)

Progress: [░░░░░░░░░░] 0%

## Performance Metrics

**Velocity:**

- Total plans completed: 37
- Average duration: n/a
- Total execution time: 0 hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| 01 | 5 | - | - |
| 01.1 | 5 | - | - |
| 02 | 6 | - | - |
| 03.1 | 6 | - | - |
| 05 | 5 | - | - |
| 6 | 5 | - | - |

**Recent Trend:**

- Last 5 plans: n/a (no plans executed yet)
- Trend: n/a

*Updated after each plan completion*

## Accumulated Context

### Roadmap Evolution

- Phase 3.1 inserted after Phase 3: Anthropic Surface — adapter/anthropic for Messages API at /v1/messages with SSE streaming day-one (loop24-client / GSD Pi via ANTHROPIC_BASE_URL). Promotes SURF-V2-01 to v1; adds ANTH-01..07 + SURF-08. (URGENT)
- Phase 1.1 inserted after Phase 1: ACP Wire Alignment — fix 10 wire-shape defects discovered during Phase 2 discuss; add real-kiro session/prompt round-trip integration test as Phase 2 unblock gate (URGENT)
- Phase 8 edited: edited fields: goal, requirements (+PLUG-06), success_criteria (+SC6 PIIRedactionHook)
- Phase 8 edited: edited fields: requirements (+OBSV-04), success_criteria (+SC7 GET /health/hooks view-only)
- Phase 6.1 inserted after Phase 6: Admin Observability UI — dark-mode /admin page rendering /health + /health/agents with brand palette; auto-refresh polling; live log tail nice-to-have (URGENT)
- Phase 8.1 inserted after Phase 8: Close gap: INTEG-01 streaming-mode PreHook short-circuit + v1.5 audit WARNINGs (URGENT)

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- Pre-Phase 1: Go over Rust (cross-compile triviality, first systems project for author)
- Pre-Phase 1: Dual API surface (OpenAI + Ollama) on one binary, one canonical engine
- Pre-Phase 1: Adapter-over-canonical layout — `internal/adapter/{ollama,openai}` ↔ `internal/canonical` ↔ `internal/engine`
- Pre-Phase 1: stdlib `net/http` + `chi` (reject `fasthttp`)
- Pre-Phase 1: Trust-gate suite required from day one (Phase 1 establishes lint/test/security baseline)
- 2026-05-27: Embeddings (Phase 7) cut from milestone — `/api/embed`, `/api/embeddings`, `/v1/embeddings` will not be implemented in v1. Provisional sidecar decision now moot.

### Pending Todos

None yet.

### Blockers/Concerns

- Pi SDK env var / config key for setting OpenAI base URL needs verification before Phase 3 starts (per PROJECT.md "Context — Clients" — open verification item).

### Quick Tasks Completed

| # | Description | Date | Commit | Directory |
|---|-------------|------|--------|-----------|
| 260523-gna | DEVELOPERS.md + idempotent macOS/Windows setup scripts | 2026-05-23 | 84562ce | [260523-gna-create-developers-md-with-step-by-step-d](./quick/260523-gna-create-developers-md-with-step-by-step-d/) |
| 260524-ldx | CLI flag support (flag-wins-over-env) for gateway binary | 2026-05-24 | f325d04 | [260524-ldx-cli-flags](./quick/260524-ldx-cli-flags/) |
| 260524-md7 | Rebrand loop24-gateway → OTTO Gateway (Tier 2 full code rebrand; dir rename deferred to Tier 3) | 2026-05-24 | e89cbf3 | [260524-md7-otto-rebrand](./quick/260524-md7-otto-rebrand/) |
| 260524-pee | E2E suite (real-binary boot + kiro, markdown report) automating HUMAN-UAT 1/2/3/6; opt-in Node SDK harness for 4/5 | 2026-05-24 | a57cbf5 | [260524-pee-e2e-suite](./quick/260524-pee-e2e-suite/) |
| 260524-pyd | E2E Ollama contract coverage (LangFlow surface): version/auth/tags/chat/generate + stream-downgrade guard; live 18 pass/0 fail/0 skip | 2026-05-24 | 49fb09e | [260524-pyd-ollama-e2e](./quick/260524-pyd-ollama-e2e/) |
| 260528-ng1 | otto-gateway publish script: scripts/publish.sh (754L, shellcheck clean) + Layer-1 dry-run harness (9/9 pass) + CI publish-dry-run job + operator-quickstart `## Publishing a build` section; lean rewrite of oscar bulk_upload_verify per docs/superpowers/specs/2026-05-28 spec | 2026-05-28 | 795f99b | [260528-ng1-implement-otto-gateway-publish-script-pe](./quick/260528-ng1-implement-otto-gateway-publish-script-pe/) |
| 260528-qe9 | Wrapper naming + probe consistency: bash wrapper OTTO_ADDR default localhost→127.0.0.1 (matches 7185293 PS fix) + gateway KIRO_CMD default kiro-cli→kiro (matches what wrappers auto-detect via `command -v kiro`); 2 atomic commits, build + race tests clean | 2026-05-28 | 0770c79 | [260528-qe9-wrapper-naming-probe-consistency-cleanup](./quick/260528-qe9-wrapper-naming-probe-consistency-cleanup/) |
| 260528-rom | Windows operator .bat surface: setup.bat (one-time MOTW strip + execution policy fix) + otto-gw.bat (cmd.exe dispatcher) + start/stop/status.bat (Explorer shortcuts); Makefile + operator-quickstart.md updated; bats shipped in all 4 packages (ASCII no-BOM); single atomic commit | 2026-05-28 | 86b76ac | [260528-rom-windows-operator-bat-surface](./quick/260528-rom-windows-operator-bat-surface/) |
| 260528-tbg | docs/INSTALL.md install/upgrade reference (481 lines, UTF-8 no BOM, TOC + per-OS checklists + .env load order + wrapper tradeoff table + upgrade behavior + pitfalls + verification commands); Makefile PKG_INSTALL declaration; operator-quickstart.md cross-reference; shipped in all 4 dist archives | 2026-05-28 | 07f4f46 | [260528-tbg-ship-docs-install-md-as-part-of-every-di](./quick/260528-tbg-ship-docs-install-md-as-part-of-every-di/) |
| 260528-d84 | Phase 9 close-out: goleak coverage gaps + property tests for buildBlocks/CoerceToolCall + .github/workflows/ci.yml + ROADMAP/REQUIREMENTS housekeeping; v1.5 milestone closed | 2026-05-28 | 91dd162 | [260528-d84-phase-9-closeout-goleak-property-tests-e](./quick/260528-d84-phase-9-closeout-goleak-property-tests-e/) |
| 260529-f8r | Admin Log Tail panel → 4-column CSS-grid table (Time/Level/Source/Message) with sticky header, level chips, wrapping message cells, dataset.raw grep plumbing, full-width fallback row; pool-slot grid bumped to 4 columns at desktop | 2026-05-29 | 4d8c147 | [260529-f8r-convert-log-tail-panel-to-css-grid-table](./quick/260529-f8r-convert-log-tail-panel-to-css-grid-table/) |
| 260531-ebi | Surface DEBUG + chat-trace enablement: debug/chat_trace booleans cfg→admin.Deps→snapshot JSON + HTML summary strip; otto-gw status (POSIX + PowerShell) reads /admin/api/snapshot (not D-12-locked /health); BSD-sed portability fix for bool parse | 2026-05-31 | eb639ef | [260531-ebi-surface-debug-and-chat-trace-enablement-](./quick/260531-ebi-surface-debug-and-chat-trace-enablement-/) |
| 260531-f1i | Align scripts/.env.otto-gw.example with OOB install defaults so it is the golden-copy config reference (active-key set verified identical to `init --non-interactive`); foundation for env-merge-on-upgrade | 2026-05-31 | 645a79a | [260531-f1i-align-env-template-golden-copy](./quick/260531-f1i-align-env-template-golden-copy/) |
| 260531-fba | Refactor init_cmd() heredoc + PS1 @"..."@ to template-copy + set_env_line: eliminate dual key-list drift; set_env_line helper is building block for env-merge-on-upgrade; shellcheck clean; 7-variant diff clean | 2026-05-31 | de28247 | [260531-fba-refactor-otto-gw-init-to-render-the-env-](./quick/260531-fba-refactor-otto-gw-init-to-render-the-env-/) |
| 260531-o4s | Add --trace flag to scripts/otto-gw: parse_flags + apply_cli_flags + usage(); exports DEBUG=true + CHAT_TRACE=true for full observability in one flag; shellcheck clean | 2026-05-31 | 7ec090b | [260531-o4s-add-trace-flag-to-otto-gw-enabling-debug](./quick/260531-o4s-add-trace-flag-to-otto-gw-enabling-debug/) |
| 260531-oax | Add -Trace switch to scripts/otto-gw.ps1 (Windows parity with bash --trace): [switch]$Trace → $env:DEBUG=true + $env:CHAT_TRACE=true; .bat needs no change (pass-through %*); pwsh parse unverified (not installed on macOS) | 2026-05-31 | 1d68a7f | [260531-oax-add-trace-switch-to-otto-gw-ps1-mirrorin](./quick/260531-oax-add-trace-switch-to-otto-gw-ps1-mirrorin/) |
| 260531-oox | Positive-signal DEBUG markers to localize PII-redaction request stalls: pii.redact.done (Logger field added to PIIRedactionHook), engine.new_session.ok, engine.prompt.sent, anthropic.sse.first_chunk (one-shot), pool.acquire/release (nil-safe debugLog helper); 3 atomic commits, go build + go vet clean (vet finds 11 pre-existing unrelated findings) | 2026-05-31 | 4269f9c | [260531-oox-add-positive-signal-debug-logs-to-locali](./quick/260531-oox-add-positive-signal-debug-logs-to-locali/) |
| 260531-pt8 | Fix PII redaction sentinel hang: change <EMAIL_1>/<EMAIL>/<EMAIL:h-...> → [EMAIL_1]/[EMAIL]/[EMAIL:h-...] in internal/plugin/pii/modes.go so kiro-cli/Claude no longer treats the sentinel as an opening XML tag (was causing engine.ACP.Prompt to never return → 120s client timeout). Confirmed live: replace mode hung; mask mode (asterisks) returned correctly. Added TestApplyMode_NoAngleBrackets_RegressionForKiroHang. 3 atomic commits, go build ./... + go test ./internal/plugin/pii/... clean | 2026-05-31 | 0a65e28 | [260531-pt8-change-pii-redaction-sentinel-from-angle](./quick/260531-pt8-change-pii-redaction-sentinel-from-angle/) |
| 260531-ra6 | Kiro-lifecycle hygiene: (1) regression test TestPool_Cancel_ReleasesSlot_WithoutResultDrain (Pool.Cancel already releases — defense-in-depth lock); (2) spawn kiro-cli in own pgrp on darwin/linux via build-tagged internal/acp/pool_pgid_{unix,windows}.go so SIGTERM cascades to children; (3) scripts/otto-gw + .ps1 stop-time reap of stray $KIRO_CMD orphans (EXACT path match, never by name). 3 atomic commits, go build ./... + GOOS=windows go build ./... + shellcheck + tests clean. Note: stuck-slot symptom seen in live testing has a different root cause (watchdog likely not firing on silent kiro) — follow-up investigation needed | 2026-05-31 | 0123225 | [260531-ra6-pool-slot-release-on-cancel-kiro-process](./quick/260531-ra6-pool-slot-release-on-cancel-kiro-process/) |

## Deferred Items

Items acknowledged and carried forward from previous milestone close:

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| *(none)* | | | |

## Session Continuity

Last session: 2026-05-31T12:40:59.803Z
Stopped at: context exhaustion at 82% (2026-05-31)
Resume file: None
