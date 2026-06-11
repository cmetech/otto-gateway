---
phase: 14-verify-reliability-findings
plan: "04"
subsystem: config-hooks-observability
tags: [reliability, verification, regression-tests, phase-14, read-only]
dependency_graph:
  requires: []
  provides:
    - 14-FINDING-G-1.md
    - 14-FINDING-C-1.md
    - 14-FINDING-C-2.md
    - 14-FINDING-C-3.md
    - 14-FINDING-O-1.md
    - 14-LEDGER-FRAGMENT-04.md
  affects:
    - Phase 16 Medium fix scope (all 5 findings confirmed)
tech_stack:
  added: []
  patterns:
    - t.Skip regression test pattern (direct template of TestLoad_StreamIdleTimeoutSec_Negative)
    - captureSlog / decodeRecords Pattern B from logging_test.go
    - captureSlogDefault via slog.SetDefault intercept for config.Load()
key_files:
  created:
    - internal/plugin/regression_rel_hooks_01_test.go
    - internal/config/regression_rel_cfg_01_test.go
    - internal/config/regression_rel_cfg_02_test.go
    - internal/config/regression_rel_cfg_03_test.go
    - internal/pool/regression_rel_cfg_04_test.go
    - .planning/phases/14-verify-reliability-findings/14-FINDING-G-1.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-C-1.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-C-2.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-C-3.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-O-1.md
    - .planning/phases/14-verify-reliability-findings/14-LEDGER-FRAGMENT-04.md
  modified: []
decisions:
  - "All 5 findings confirmed as-is; no false-positives"
  - "O-1 test placed in internal/pool/ (not internal/config/) because failure is at pool-acquire site"
  - "Master ledger merge deferred — sibling fragments 01/02/03 not yet present at task 6 execution time"
  - "captureSlogDefault helper introduced in config_test package to intercept slog.Default() since config.Load() takes no logger arg"
metrics:
  duration: "10m 7s"
  completed: "2026-06-11T13:15:21Z"
  tasks: 6
  files: 11
requirements_completed:
  - REL-VERIFY-MED
  - REL-VERIFY-GATE
---

# Phase 14 Plan 04: Config / Hooks / Observability Verification Summary

Verified 5 reliability findings (G-1, C-1, C-2, C-3, O-1 — all Medium) against current source. All 5 are confirmed. Shipped 5 t.Skip'd Go regression tests + 5 evidence files + 1 ledger fragment.

## Verdicts

| Finding | Severity | REL-* ID | Status | Target Phase |
|---------|----------|----------|--------|-------------|
| G-1 | M | REL-HOOKS-01 | **confirmed** | 16 |
| C-1 | M | REL-CFG-01 | **confirmed** | 16 |
| C-2 | M | REL-CFG-02 | **confirmed** | 16 |
| C-3 | M | REL-CFG-03 | **confirmed** | 16 |
| O-1 | M | REL-CFG-04 | **confirmed** | 16 |

**Confirmed:** 5 / **False-positive:** 0 / **Needs-investigation:** 0

## Finding Details

**G-1 (REL-HOOKS-01) — Non-streaming PostHook skip:** `engine/collect.go:165,171` return before the PostHook loop at `:187`. `anthropic/collect.go:177,184` return before `RunPostHooks` at `:207`. `LoggingHook.startTimes` and `ChatTraceHook.startTimes` sync.Map entries are stored in Before but only reclaimed in After — never called on error paths. Confirmed leak path.

**C-1 (REL-CFG-01) — Negative/zero env coercion:** All 5 vars (POOL_SIZE, SESSION_MAX, SESSION_TTL_MS, SESSION_TICK_INTERVAL_MS, CHAT_TRACE_MAX_AGE_DAYS) pass `config.Load()` with negative values without error. Silently coerced by `pool.Config.applyDefaults` and `session.Config.applyDefaults`. Contrast: `STREAM_IDLE_TIMEOUT_SEC` already has the sign check at config.go:366-368.

**C-2 (REL-CFG-02) — PING_INTERVAL panic:** `config.go:295` accepts negative durations; `acp/client.go:59` only defaults when `== 0` (not `<= 0`); `acp/client.go:505` calls `time.NewTicker(cfg.PingInterval)` which panics for non-positive values in a goroutine with no recover.

**C-3 (REL-CFG-03) — EMBEDDING_MODEL_DEFAULT never read:** Grep across all production Go files in `internal/` and `cmd/` returns zero results for `EMBEDDING_MODEL_DEFAULT`. The var is documented in CLAUDE.md but never parsed, stored, or logged by any production code. Embeddings (Phase 7) was cut from the milestone scope.

**O-1 (REL-CFG-04) — Pool exhaustion silent:** `pool.go:490-505` — the `select` blocks on `<-p.slots` with NO log emitted before parking. `p.debugLog("pool.acquire", ...)` at line 506 fires only after successful acquisition and only at Debug level. No Warn-level signal exists during the wait period.

## Read-only-implementation Confirmation

```
git diff main...HEAD -- ':!*_test.go' ':!.planning/' ':!docs/' ':!tests/reliability/'
```

**Result: empty** — zero production source edits across all 6 tasks.

## Master Ledger Merge

**NOT executed.** At Task 6 execution time, sibling fragments 01/02/03 were all absent (other parallel plan agents had not yet written their fragments). Per plan Task 6 Step 2: merge condition not met. This plan wrote only `14-LEDGER-FRAGMENT-04.md`. The orchestrator or the last-finishing parallel agent will execute the merge when all 4 fragments exist.

## False-positive REL-* IDs to Drop from Phase 16

**None.** All 5 findings are confirmed. No REL-* IDs should be dropped from Phase 16 scope based on this plan's verdicts.

## Deviations from Plan

### None

Plan executed exactly as written with one minor deviation:

**[Rule 3 - Blocking] canonical.Block struct correction:** The initial `TestRegression_REL_CFG_04_PoolExhaustionSilent` used `canonical.Block{Kind: canonical.BlockKindText, Text: "ping"}` — but `Block.Text` is `*TextBlock`, not a string. Fixed to `Text: &canonical.TextBlock{Content: "ping"}` before commit. Build error caught before any commit; no test behavior changed.

## Commits

| Task | Commit | Description |
|------|--------|-------------|
| Task 1 (G-1) | b5055bb | test(14-04): verify G-1 REL-HOOKS-01 non-streaming PostHook skip |
| Task 2 (C-1) | f7a4678 | test(14-04): verify C-1 REL-CFG-01 negative/zero env coercion |
| Task 3 (C-2) | 05cd918 | test(14-04): verify C-2 REL-CFG-02 PING_INTERVAL panic |
| Task 4 (C-3) | 3cf538d | test(14-04): verify C-3 REL-CFG-03 EMBEDDING_MODEL_DEFAULT unimplemented |
| Task 5 (O-1) | 858d383 | test(14-04): verify O-1 REL-CFG-04 pool exhaustion silent |
| Task 6 (ledger) | 08f92a9 | docs(14-04): add ledger fragment 04 (G-1, C-1, C-2, C-3, O-1) |

## Self-Check: PASSED

All 11 created files found on disk. All 6 task commits verified in git log.
