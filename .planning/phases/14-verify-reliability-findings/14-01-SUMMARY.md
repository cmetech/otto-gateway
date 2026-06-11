---
phase: 14-verify-reliability-findings
plan: "01"
subsystem: pool-acp
tags: [reliability, regression-tests, evidence, verification]
dependency_graph:
  requires: [docs/reviews/2026-06-11-reliability-review.md]
  provides:
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-1.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-2.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-3.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-4.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-5.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-6.md
    - internal/pool/regression_rel_pool_01_test.go
    - internal/pool/regression_rel_pool_02_test.go
    - internal/pool/regression_rel_pool_03_test.go
    - internal/pool/regression_rel_pool_04_test.go
    - internal/session/regression_rel_pool_05_test.go
    - internal/acp/regression_rel_pool_06_test.go
    - tests/reliability/manual/REL-POOL-06-repro.go
    - .planning/phases/14-verify-reliability-findings/14-LEDGER-FRAGMENT-01.md
  affects: [Phase 15 (P-1/P-2/P-3 fixes), Phase 16 (P-4/P-5/P-6 fixes)]
tech_stack:
  added: []
  patterns: [t.Skip regression test, Pattern F manual reproducer header, ledger fragment]
key_files:
  created:
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-1.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-2.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-3.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-4.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-5.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-P-6.md
    - internal/pool/regression_rel_pool_01_test.go
    - internal/pool/regression_rel_pool_02_test.go
    - internal/pool/regression_rel_pool_03_test.go
    - internal/pool/regression_rel_pool_04_test.go
    - internal/session/regression_rel_pool_05_test.go
    - internal/acp/regression_rel_pool_06_test.go
    - tests/reliability/manual/REL-POOL-06-repro.go
    - .planning/phases/14-verify-reliability-findings/14-LEDGER-FRAGMENT-01.md
  modified: []
decisions:
  - All 6 Pool/ACP findings confirmed — no false-positives or needs-investigation
  - P-1 (Critical) confirmed: removeSlot at pool.go:534 called unconditionally for genuine errors; ctx-cancel guard (WR-07) does not cover transient spawn failures
  - P-2 (High) confirmed: os.Exit(1) at main.go:131 skips defer cleanup(); deferred cleanup only fires on nil-return (clean shutdown) path
  - P-3 (High) confirmed: both client.go:868-870 and :894-896 unconditionally nil c.activeStream; no CAS guard (D-09 bar not met)
  - P-4 (Medium) confirmed: stream.push uses c.clientCtx at client.go:1085; stalled consumer blocks readLoop; pingLoop then SIGKILLs healthy worker
  - P-5 (Medium) confirmed: Entry.LastUsed written under r.mu at registry.go:206 and under e.Mu at entry_acp.go:77; no atomic.Int64 guard
  - P-6 (Medium) confirmed: pool_pgid_windows.go:15 and :21 are no-ops; job object machinery in comment does not exist in repo
metrics:
  duration: ~9 minutes
  completed_date: 2026-06-11
  tasks_completed: 7
  files_created: 14
---

# Phase 14 Plan 01: Pool/ACP Reliability Findings Verification Summary

6 Pool/ACP reliability findings verified against current main; all confirmed with t.Skip'd regression tests, evidence files, and ledger fragment.

## Verdicts by Finding

| Finding | Severity | REL-* ID | Verdict | Target Phase |
|---|---|---|---|---|
| P-1 | Critical | REL-POOL-01 | confirmed | 15 |
| P-2 | High | REL-POOL-02 | confirmed | 15 |
| P-3 | High | REL-POOL-03 | confirmed | 15 |
| P-4 | Medium | REL-POOL-04 | confirmed | 16 |
| P-5 | Medium | REL-POOL-05 | confirmed | 16 |
| P-6 | Medium | REL-POOL-06 | confirmed | 16 |

**Counts:** 6 confirmed / 0 false-positive / 0 needs-investigation

Phase 14 close gate contribution from this plan: `needs_investigation_count == 0` (all 6 rows resolve to confirmed).

## Master Ledger Status

Plan 14-01 wrote `14-LEDGER-FRAGMENT-01.md` with 6 rows (P-1..P-6). Fragments 02/03/04 were not present on disk at plan close — merge-owner responsibility falls to whichever of the parallel plans finishes last. Plan 14-01 is designated merge-owner if all 4 finish simultaneously.

## False-Positive REL-* IDs Dropped from Phase 15/16 Scope

None — all 6 findings are confirmed. Phase 15 scope retains REL-POOL-01, REL-POOL-02, REL-POOL-03. Phase 16 scope retains REL-POOL-04, REL-POOL-05, REL-POOL-06.

## Read-Only Implementation Verification

`git diff main...HEAD -- ':!*_test.go' ':!.planning/' ':!docs/' ':!tests/reliability/'` returned empty. No production source files were modified.

## Trust Gate Results

Per-plan trust gates (after all 7 tasks):
- `gofumpt -l` on all 6 new `*_test.go` files: no diff (clean)
- `go vet ./internal/pool/... ./internal/session/... ./internal/acp/...`: clean
- `go build ./...`: clean
- `go test -race ./internal/pool/... ./internal/session/... ./internal/acp/...`: all pass; all 6 new TestRegression_REL_POOL_* tests run as `--- SKIP`

## Deviations from Plan

None — plan executed exactly as written. All 6 findings verified and confirmed per D-11 bias. All t.Skip strings match verbatim D-12 format. All evidence files have the required D-08 frontmatter + 4 sections.

## Known Stubs

No data stubs. All evidence files contain real code-walk analysis against current source. Regression test bodies are genuine reproducers (not mock assertions) — the t.Skip prevents them from running in CI per D-12.

## Self-Check: PASSED

Files created (spot checks):
- `internal/pool/regression_rel_pool_01_test.go` — exists, contains `t.Skip("REL-POOL-01 (P-1): regression test — unskip in Phase 15 fix commit")`
- `.planning/phases/14-verify-reliability-findings/14-FINDING-P-1.md` — exists, has frontmatter `status: confirmed`
- `tests/reliability/manual/REL-POOL-06-repro.go` — exists, has Pattern F header with all 7 required fields
- `.planning/phases/14-verify-reliability-findings/14-LEDGER-FRAGMENT-01.md` — exists, 6 data rows, D-07 column header

Commits verified in git log:
- `db4ccd3` test(14-01): add REL-POOL-01 regression test + P-1 evidence
- `477fcdc` test(14-01): add REL-POOL-02 regression test + P-2 evidence
- `bc63b46` test(14-01): add REL-POOL-03 regression test + P-3 evidence
- `dfbd01a` test(14-01): add REL-POOL-04 regression test + P-4 evidence
- `2335609` test(14-01): add REL-POOL-05 regression test + P-5 evidence
- `efff2af` test(14-01): add REL-POOL-06 stub + manual reproducer + P-6 evidence
- `a62b197` docs(14-01): write ledger fragment 01 (P-1..P-6)
