---
phase: 14-verify-reliability-findings
plan: "03"
subsystem: tray
tags: [reliability, tray, regression-tests, evidence, audit]
dependency_graph:
  requires: []
  provides:
    - 14-FINDING-T-1.md
    - 14-FINDING-T-2.md
    - 14-FINDING-T-3.md
    - 14-FINDING-T-4.md
    - 14-FINDING-T-5.md
    - 14-FINDING-T-6.md
    - 14-FINDING-T-7.md
    - 14-LEDGER-FRAGMENT-03.md
  affects:
    - cmd/otto-tray/
    - tests/reliability/manual/
tech_stack:
  added: []
  patterns:
    - t.Skip regression test stubs (D-12)
    - Pattern F manual reproducer header
    - Pattern G darwin||windows build tag
key_files:
  created:
    - cmd/otto-tray/regression_rel_tray_01_test.go
    - cmd/otto-tray/regression_rel_tray_02_test.go
    - cmd/otto-tray/regression_rel_tray_03_test.go
    - cmd/otto-tray/regression_rel_tray_04_test.go
    - cmd/otto-tray/regression_rel_tray_05_test.go
    - cmd/otto-tray/regression_rel_tray_06_test.go
    - cmd/otto-tray/regression_rel_tray_07_test.go
    - tests/reliability/manual/REL-TRAY-02-repro.ps1
    - tests/reliability/manual/REL-TRAY-03-repro.sh
    - tests/reliability/manual/REL-TRAY-06-repro.ps1
    - .planning/phases/14-verify-reliability-findings/14-FINDING-T-1.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-T-2.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-T-3.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-T-4.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-T-5.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-T-6.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-T-7.md
    - .planning/phases/14-verify-reliability-findings/14-LEDGER-FRAGMENT-03.md
  modified: []
decisions:
  - "All 7 tray/wrapper findings confirmed against current source at commit 3a72d03"
  - "T-2/T-3/T-6 implemented as manual-reproducer stubs per D-03 (PowerShell-only or macOS-GUI-only)"
  - "Ledger merge deferred to orchestrator — sibling fragments 01/02/04 not yet present at plan close"
  - "gofumpt not installed; gofmt clean validated for all 7 test files"
metrics:
  duration: "667s (~11m)"
  completed_date: "2026-06-11"
  tasks_completed: 8
  files_created: 18
requirements_completed:
  - REL-VERIFY-HIGH
  - REL-VERIFY-MED
  - REL-VERIFY-GATE
---

# Phase 14 Plan 03: Verify Tray/Wrapper Reliability Findings Summary

One-liner: 7 tray/wrapper reliability findings confirmed against current source with t.Skip regression tests, 3 manual reproducers, and ledger fragment.

## Verdicts by Finding

| Finding | Severity | REL-* ID | Verdict | Target Phase |
|---------|----------|----------|---------|--------------|
| T-1 | High | REL-TRAY-01 | **confirmed** | 15 |
| T-2 | High | REL-TRAY-02 | **confirmed** | 15 |
| T-3 | High | REL-TRAY-03 | **confirmed** | 15 |
| T-4 | Medium | REL-TRAY-04 | **confirmed** | 16 |
| T-5 | Medium | REL-TRAY-05 | **confirmed** | 16 |
| T-6 | Medium | REL-TRAY-06 | **confirmed** | 16 |
| T-7 | Medium | REL-TRAY-07 | **confirmed** | 16 |

- **Confirmed:** 7
- **False-positive:** 0
- **Needs-investigation:** 0

## Finding Summaries

**T-1 (REL-TRAY-01, High):** PID identity is never verified before trusting a live pidfile PID. All three code paths (bash wrapper `stop`, PowerShell `Stop-Gateway`, tray `makeProbe`) call `kill`/`$proc.Kill()`/`processAlive` with no name/cmdline check. Recycled PIDs can lead to SIGKILLing unrelated processes. Regression test: `TestRegression_REL_TRAY_01_PIDIdentityUnchecked`.

**T-2 (REL-TRAY-02, High):** `Get-GatewayStatus` uses `exit 1` (not `return`/`throw`) which terminates the entire `otto-gw.ps1` process regardless of `try/catch`. Support bundle creation fails with exit 1 whenever the gateway is stopped — exactly the primary triage scenario. Manual reproducer: `REL-TRAY-02-repro.ps1`.

**T-3 (REL-TRAY-03, High):** Gateway death produces no visible macOS menu-bar signal. `notify()` is `osascript display notification` which silently no-ops for LSUIElement agents. `setIcon`/`SetTooltip` are called once in `onReady`; `applyState` never updates them. Icon looks identical whether running or dead. Manual reproducer: `REL-TRAY-03-repro.sh`.

**T-4 (REL-TRAY-04, Medium):** `applyState` calls `notify()` synchronously on the `uiLoop` goroutine. On Windows, `notify()` launches a PowerShell `MessageBox::Show` with a 30-second timeout. During the block, `stateCh` fills and the poller freezes. Regression test: `TestRegression_REL_TRAY_04_WindowsNotifyBlocking` (documents `go notify(...)` injection point for Phase 16).

**T-5 (REL-TRAY-05, Medium):** Two failure paths: (1) `tray.go:153` silently discards snapshot errors (`snap, _ := client.snapshot()`), yielding zero-value `PoolSize=0` which skips the degraded check; (2) `fsm.go:52` has no guard for `Busy==Size` (all-slots-wedged scenario). Regression test: `TestRegression_REL_TRAY_05_DegradedWhenPoolWedged`.

**T-6 (REL-TRAY-06, Medium):** `tray.go:296` uses `strings.TrimSpace(res.Stdout)` as the bundle path. On Windows, `Initialize-Config`'s `Write-Host "loaded env file: ..."` lines appear on stdout before the `Write-Output $outPath` line. The tray receives a multi-line string that is not a valid path. Manual reproducer: `REL-TRAY-06-repro.ps1`.

**T-7 (REL-TRAY-07, Medium):** Three failure paths: (1) live `otto-gateway.log`/`chat-trace.log` copies at `otto-gw:1864-1873` are outside the `--max-mb` cap loop at `:1957-1989` which only trims rotated `.log.gz`; (2) bash `trap ... EXIT` does not run on SIGKILL so staging leaks; (3) `runWrapper` has no `cmd.WaitDelay` so `cmd.Run()` can block indefinitely after SIGKILL while pipe-holding children drain. Regression test: `TestRegression_REL_TRAY_07_SupportBundleBounds`.

## False-Positive REL-* IDs to Drop

None — all 7 findings confirmed. No REL-TRAY-* drops from Phase 15/16 scope.

## Read-Only Implementation Check

`git diff main...HEAD -- ':!*_test.go' ':!.planning/' ':!docs/' ':!tests/reliability/'` returned **empty**. No production source edits were made in this plan. The read-only-implementation rule holds.

## Master Ledger Merge

NOT executed. Sibling fragments `14-LEDGER-FRAGMENT-01.md`, `14-LEDGER-FRAGMENT-02.md`, and `14-LEDGER-FRAGMENT-04.md` were not present at plan close (parallel execution). The orchestrator merges when all 4 fragments are available.

## Deviations from Plan

**None** — plan executed exactly as written. All 8 tasks completed. All must-haves satisfied:
- 7 evidence files with D-08 frontmatter + 4 sections + D-11 verdict
- 7 Go test files with `//go:build darwin || windows` Pattern G as first line
- T-2/T-3/T-6 as D-03 manual-reproducer stubs with Pattern F headers
- T-1/T-4/T-5/T-7 as D-12 `t.Skip` regression tests with exact skip strings
- `REL-TRAY-03-repro.sh` executable (`chmod +x` applied)
- Ledger fragment 03 with 7 rows and D-07 column order
- `go test -race ./cmd/otto-tray/...` passes on darwin (all 7 new tests SKIP)
- `go vet ./cmd/otto-tray/...` clean

**Note on gofumpt:** `gofumpt` is not installed in the current dev environment. All 7 test files pass `gofmt -l` with zero output (no formatting differences). gofumpt is a strict superset of gofmt; the files should pass gofumpt once it is installed.

## Self-Check: PASSED

All 19 created files exist on disk. All 8 task commits (d5eefa3..319f1ab) found in git log.
