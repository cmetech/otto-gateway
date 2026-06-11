---
phase: 14-verify-reliability-findings
plan: "02"
subsystem: http-surface
tags:
  - reliability
  - regression-tests
  - http-surface
  - sse
  - ndjson
  - shutdown
  - idle-timeout
dependency_graph:
  requires: []
  provides:
    - REL-HTTP-01 verdict + regression test
    - REL-HTTP-02 verdict + regression test
    - REL-HTTP-03 verdict + regression tests (2 files)
    - REL-HTTP-04 verdict + regression test
    - REL-HTTP-05 verdict + regression test
    - 14-LEDGER-FRAGMENT-02.md (5 rows, D-07 column order)
  affects:
    - internal/server/
    - internal/adapter/openai/
    - internal/adapter/ollama/
    - internal/admin/
tech_stack:
  added: []
  patterns:
    - D-12 t.Skip regression test shape (verbatim skip string)
    - fakeRunHandle/fakeStream test doubles (existing package-internal types)
    - stalledReader io.Reader for body-read stall simulation
    - trackingRunHandle for watchdog interaction observation
key_files:
  created:
    - internal/server/regression_rel_http_01_test.go
    - internal/adapter/openai/regression_rel_http_02_test.go
    - internal/adapter/openai/regression_rel_http_03_test.go
    - internal/adapter/ollama/regression_rel_http_03_test.go
    - internal/server/regression_rel_http_04_test.go
    - internal/admin/regression_rel_http_05_test.go
    - .planning/phases/14-verify-reliability-findings/14-FINDING-H-1.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-H-2.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-H-3.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-H-4.md
    - .planning/phases/14-verify-reliability-findings/14-FINDING-H-5.md
    - .planning/phases/14-verify-reliability-findings/14-LEDGER-FRAGMENT-02.md
  modified: []
decisions:
  - "H-3 spans two surfaces (OpenAI + Ollama) → two test files per plan spec"
  - "H-2 reproducer uses trackingRunHandle rather than fakePoolClient because RunHandle has no Cancel method; watchdog suppression is the observable pre-fix signal"
  - "Merge to master ledger deferred to plan 14-01 (sibling fragments 01/03/04 absent; merge condition not met)"
metrics:
  duration: 11m
  completed_date: "2026-06-11"
  tasks_completed: 6
  files_created: 12
---

# Phase 14 Plan 02: HTTP Surface Reliability Findings Summary

Verified all 5 HTTP-surface reliability findings from the 2026-06-11 reliability review. Produced 5 evidence files, 6 Go regression test files, and 1 ledger fragment.

## Verdicts by Finding

| Finding | Severity | REL-* ID | Status | Target |
|---|---|---|---|---|
| H-1: Shutdown blocks on admin SSE | High | REL-HTTP-01 | **confirmed** | Phase 15 |
| H-2: Idle-timeout returns hung worker | High | REL-HTTP-02 | **confirmed** | Phase 15 |
| H-3: Mid-stream truncation silent | High | REL-HTTP-03 | **confirmed** | Phase 15 |
| H-4: No body-read deadline | Medium | REL-HTTP-04 | **confirmed** | Phase 16 |
| H-5: Tailer line-cap bypassed | Medium | REL-HTTP-05 | **confirmed** | Phase 16 |

**Counts:** 3 confirmed High, 2 confirmed Medium, 0 false-positive, 0 needs-investigation.

## Per-Finding Verification Evidence

### H-1 (REL-HTTP-01, High) — Confirmed
- **Cite verified:** `internal/server/server.go:346-383` — `http.Server` constructed without `BaseContext`, `RegisterOnShutdown`, or `ConnContext`. `Shutdown` has 30s grace with no mechanism to cancel in-flight SSE handler contexts.
- **`internal/admin/sse.go:167-203`** — `sseLoop` blocks on `r.Context().Done()` indefinitely; no shutdown signal wired.
- **Test:** `TestRegression_REL_HTTP_01_ShutdownBlocksOnAdminSSE` in `internal/server/regression_rel_http_01_test.go`. Opens real SSE connection via `httptest.NewServer`, calls `srv.Shutdown`, asserts block > 2s.

### H-2 (REL-HTTP-02, High) — Confirmed
- **Cite verified:** `internal/adapter/openai/sse.go:460-462` — idle-timeout branch calls `run.StopWatchdog()()`, suppressing the `context.AfterFunc` that carries `ACP.Cancel(sid)`. No explicit Cancel issued. Same pattern at `:482-484` (write-error path).
- **Correct siblings verified:** `engine/collect.go:159-165` and `ollama/ndjson.go:420-452` both leave AfterFunc intact on idle path — only OpenAI SSE suppresses it without compensation.
- **Test:** `TestRegression_REL_HTTP_02_IdleTimeoutReturnsHungWorker` in `internal/adapter/openai/regression_rel_http_02_test.go`. Custom `trackingRunHandle` records watchdog suppression: `watchdogCalled=true`, `watchdogStopped=true` (pre-fix observable: Cancel suppressed).

### H-3 (REL-HTTP-03, High) — Confirmed (two surfaces)
- **OpenAI cite verified:** `internal/adapter/openai/sse.go:543-557` — `finalizeSSE` on `rerr != nil` logs at Debug and returns; no error frame, no `[DONE]`.
- **Ollama cite verified:** `internal/adapter/ollama/ndjson.go:541-549` — `finalizeNDJSON` on `rerr != nil` logs at Debug and returns; no `done:true` terminal line.
- **Tests:** Two separate test files, both named `TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent`:
  - `internal/adapter/openai/regression_rel_http_03_test.go` — asserts no `data: {"error":` and no `data: [DONE]`
  - `internal/adapter/ollama/regression_rel_http_03_test.go` — asserts no `"done_reason":"error"` and no `"done":true`

### H-4 (REL-HTTP-04, Medium) — Confirmed
- **Cite verified:** `internal/server/server.go:347-360` — `http.Server` has `ReadHeaderTimeout: 10s` and `IdleTimeout: 120s` but no `ReadTimeout`; no handler calls `SetReadDeadline` around `decodeJSONBody`.
- **Test:** `TestRegression_REL_HTTP_04_BodyReadDeadlineMissing` in `internal/server/regression_rel_http_04_test.go`. `stalledReader` blocks all `Read()` calls; 3s watchdog confirms handler has not returned (pre-fix observable).

### H-5 (REL-HTTP-05, Medium) — Confirmed
- **Cite verified:** `internal/admin/tail.go:402` — cap check `len(current) > TailerMaxLineBytes && !strings.HasSuffix(current, "\n")`. The `!strings.HasSuffix` guard exempts all newline-terminated lines from truncation; `bufio.Reader.ReadString('\n')` returns full lines of any size.
- **Test:** `TestRegression_REL_HTTP_05_AdminTailerLineCapBypass` in `internal/admin/regression_rel_http_05_test.go`. Appends 5 MB newline-terminated line; subscriber receives line with `len > TailerMaxLineBytes` (pre-fix observable).

## Ledger Fragment

`14-LEDGER-FRAGMENT-02.md` written with 5 rows in D-07 column order:
`| Finding ID | Sev | REL-* ID | Status | File:line | Evidence | Target phase |`

## Master Ledger Merge

**Not executed.** Merge condition not met: sibling fragments 01, 03, 04 were absent on disk when Task 6 Step 2 ran (parallel execution). Plan 14-01 owns the merge step and will execute it when all 4 fragments are present.

## Read-Only-Implementation Rule

`git diff main...HEAD -- ':!*_test.go' ':!.planning/' ':!docs/' ':!tests/reliability/'` returned **0 lines** — no production source was modified. Only test files and planning artifacts were created.

## False-Positive REL-* IDs

None. All 5 HTTP-surface findings are **confirmed**. No IDs should be dropped from Phase 15/16 scope.

## Deviations from Plan

None — plan executed exactly as written. H-2 used a `trackingRunHandle` (not `fakePoolClient` as suggested in plan patterns) because `RunHandle` has no `Cancel` method; the watchdog suppression is observable via `StopWatchdog` recording, which correctly demonstrates the pre-fix state.

## Self-Check: PASSED

**Files exist:**
- `internal/server/regression_rel_http_01_test.go` ✓
- `internal/adapter/openai/regression_rel_http_02_test.go` ✓
- `internal/adapter/openai/regression_rel_http_03_test.go` ✓
- `internal/adapter/ollama/regression_rel_http_03_test.go` ✓
- `internal/server/regression_rel_http_04_test.go` ✓
- `internal/admin/regression_rel_http_05_test.go` ✓
- `.planning/phases/14-verify-reliability-findings/14-FINDING-H-1.md` ✓
- `.planning/phases/14-verify-reliability-findings/14-FINDING-H-2.md` ✓
- `.planning/phases/14-verify-reliability-findings/14-FINDING-H-3.md` ✓
- `.planning/phases/14-verify-reliability-findings/14-FINDING-H-4.md` ✓
- `.planning/phases/14-verify-reliability-findings/14-FINDING-H-5.md` ✓
- `.planning/phases/14-verify-reliability-findings/14-LEDGER-FRAGMENT-02.md` ✓

**Commits exist:** ad5e152, 8925a48, df2b170, 9cf0dd2, d7883eb, d8015af ✓

**All 6 tests SKIP, none PASS, none FAIL** ✓

**go vet clean, go build clean, read-only diff empty** ✓
