---
phase: "04-streaming"
plan: "03"
subsystem: "engine + acp (test-only)"
tags: ["watchdog", "strm-04", "cancel", "goleak", "integration-test", "whitebox-test"]
dependency_graph:
  requires:
    - "04-01 (engine.Run AfterFunc watchdog + StopWatchdog accessor)"
  provides:
    - "TestWatchdog_CancelOnCtxDone — engine-level STRM-04 proof (channel-based)"
    - "TestWatchdog_StopPreventsCancel_OnNormalCompletion — D-06 stop→cancel ordering"
    - "TestIntegration_CancelFrame — ACP-wire STRM-04 proof (session/cancel notification)"
    - "fakeACPServer.cancelSeen + lastCancelSID extension"
  affects:
    - "internal/engine/watchdog_test.go"
    - "internal/acp/fakeacp_test.go"
    - "internal/acp/cancel_test.go"
tech_stack:
  added: []
  patterns:
    - "select-with-deadline for absence-of-cancel assertion (5ms poll + 2s deadline)"
    - "stop()→cancelFn() ordering with 50ms sleep for absence-of-event assertion"
    - "idempotent channel close via select pattern (cancelSeen mirrors permissionResponseReceived)"
    - "fakeACPServer cancelSeen chan struct{} + lastCancelSID string extension"
key_files:
  created:
    - "internal/engine/watchdog_test.go — TestWatchdog_CancelOnCtxDone + TestWatchdog_StopPreventsCancel_OnNormalCompletion"
    - "internal/acp/cancel_test.go — TestIntegration_CancelFrame"
  modified:
    - "internal/acp/fakeacp_test.go — cancelSeen + lastCancelSID fields + session/cancel dispatch case"
decisions:
  - "Empty for-range body uses '_ = c' assignment to satisfy revive linter (empty-block rule) — minimal deviation from plan's bare range"
  - "cancel_test.go uses goleak.VerifyNone(t) in defer alongside client.Close() to confirm no goroutine leak per test (belt-and-suspenders beyond testmain_test.go gate)"
metrics:
  duration: "~15 minutes"
  completed_date: "2026-05-25"
  tasks: 2
  files_modified: 3
---

# Phase 04 Plan 03: STRM-04 Watchdog + Cancel Frame Tests Summary

STRM-04 (disconnect → session/cancel) proved at two observable levels: engine-unit (channel-based watchdog assertion) and ACP-wire (fake-server session/cancel notification).

## What Was Built

### Task 1: Engine watchdog unit tests (internal/engine/watchdog_test.go)

**TestWatchdog_CancelOnCtxDone** — Proves the AfterFunc watchdog fires `Cancel(sid)` after ctx cancellation:
- `fakeACP` with `newSessionID: "watchdog-cancel-sid"` and no chunks (stream closes immediately)
- `newTestEngine` + `eng.Run(ctx, req)` using cancelable ctx
- Stream drained with `for c := range run.Stream().Chunks() { _ = c }` + `Result()` call
- `cancelFn()` triggers the AfterFunc goroutine
- Select-with-deadline (5ms poll + 2s deadline) waits for `cancelCalls` to become non-empty — no bare sleep (REVIEWS.md LOW fix)
- Asserts `cancelCalls[0] == "watchdog-cancel-sid"`

**TestWatchdog_StopPreventsCancel_OnNormalCompletion** — Proves stop()→cancelFn() ordering prevents spurious cancel:
- `fakeACP` with one text chunk; cancelable ctx
- Stream drained naturally; `run.StopWatchdog()` called FIRST
- `cancelFn()` called AFTER stop() — AfterFunc goroutine already deregistered
- 50ms sleep (justified: asserting absence of event, no channel to wait on)
- Asserts `len(cancelCalls) == 0`

Both tests are package `engine` (whitebox) — reuse `fakeACP`, `newTestEngine`, `simpleUserReq` from `engine_test.go` without redeclaration. Goleak gate in `testmain_test.go` covers both.

### Task 2: ACP cancel frame wire integration test

**fakeacp_test.go extensions:**
- Added `cancelSeen chan struct{}` and `lastCancelSID string` to `fakeACPServer` struct
- Initialized `cancelSeen: make(chan struct{})` in `newFakeACPServer()`
- Added `case "session/cancel":` in `serve()` dispatch switch:
  - Unmarshals params to extract `sessionId` (exact JSON field name from `cancelParams` in `client.go`)
  - Sets `f.lastCancelSID = params.SessionID`
  - Closes `f.cancelSeen` idempotently using the same `select { case <-f.cancelSeen: default: close(...) }` pattern as `permissionResponseReceived`
  - No response sent (notifications have no id, no response expected)

**cancel_test.go (package acp_test):**
- `TestIntegration_CancelFrame`: connects real `*acp.Client` to `fakeACPServer` via `NewWithConn`
- Calls `client.Initialize(ctx)` (ACP handshake required first)
- Calls `client.Cancel("test-cancel-sid")` — fire-and-forget notification
- `select { case <-fake.cancelSeen: ... case <-time.After(2 * time.Second): t.Fatal(...) }` — 2s deadline
- Asserts `fake.lastCancelSID == "test-cancel-sid"` after channel fires
- `goleak.VerifyNone(t)` in defer alongside `client.Close()` — belt-and-suspenders goroutine leak check

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Empty for-range body triggered revive linter**
- **Found during:** Task 1 — first commit attempt
- **Issue:** `for range run.Stream().Chunks() {}` flagged by revive as `empty-block: this block is empty, you can remove it`; golangci-lint pre-commit hook blocked the commit
- **Fix:** Changed both empty-body ranges to `for c := range run.Stream().Chunks() { _ = c }` — satisfies revive while preserving drain semantics
- **Files modified:** internal/engine/watchdog_test.go
- **Commit:** fixed inline before 51a3658

## Verification

```
go test -race ./internal/engine/... ./internal/acp/... — PASS (both packages)
```

- TestWatchdog_CancelOnCtxDone: PASS — Cancel called with "watchdog-cancel-sid" via channel-based wait
- TestWatchdog_StopPreventsCancel_OnNormalCompletion: PASS — no Cancel after stop()→cancelFn() sequence
- TestIntegration_CancelFrame: PASS — session/cancel notification with correct sessionId observed on fake wire
- All pre-existing engine and acp tests: PASS
- goleak gate clean in both packages (VerifyTestMain)

## Known Stubs

None — this plan is test-only. No production stubs exist. All test assertions are wired to real production code paths (engine.Run AfterFunc watchdog and acp.Client.Cancel sendNotification).

## Threat Flags

None — all changes are test files. No new network endpoints, auth paths, or schema changes introduced.

## Self-Check: PASSED
