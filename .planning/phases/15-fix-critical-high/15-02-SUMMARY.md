---
phase: 15-fix-critical-high
plan: "02"
subsystem: http-adapter
tags: [reliability, openai, ollama, sse, ndjson, watchdog, mid-stream-death]
dependency_graph:
  requires: []
  provides: [REL-HTTP-02, REL-HTTP-03]
  affects: [internal/adapter/openai/sse.go, internal/adapter/ollama/ndjson.go]
tech_stack:
  added: [os/exec]
  patterns: [errors.As for exit code extraction, slog structured Warn, surface-native terminal frames]
key_files:
  created: []
  modified:
    - internal/adapter/openai/sse.go
    - internal/adapter/ollama/ndjson.go
    - internal/adapter/openai/regression_rel_http_02_test.go
    - internal/adapter/openai/regression_rel_http_03_test.go
    - internal/adapter/ollama/regression_rel_http_03_test.go
    - internal/adapter/ollama/ndjson_test.go
decisions:
  - "H-2: remove StopWatchdog from idleC and applyChunk arms; let watchdog AfterFunc fire Cancel naturally via deferred cancelFn"
  - "H-3: finalizeSSE/finalizeNDJSON emit surface-native terminal error frames on rerr != nil; WARN log with D-09/D-10 fields"
  - "worker_pid logs 0 (placeholder) — RunHandle interface does not expose PID; bytes_streamed logs 0 — no counter wired in emitters"
  - "kiro_exit_code logged only when errors.As(rerr, &exitErr) succeeds; field omitted (not zero-padded) when chain has no *exec.ExitError"
  - "Anthropic surface unchanged — internal/adapter/anthropic/sse.go:785-795 already emits event: error via writeSSEError on rerr != nil"
  - "TestNDJSON_StreamResultError updated (Rule 1): pre-existing test was asserting old buggy no-done:true behavior; updated to assert post-fix done:true"
requirements_completed: [REL-HTTP-02, REL-HTTP-03]
metrics:
  duration: "~25 minutes"
  completed_date: "2026-06-11"
  tasks_completed: 2
  files_changed: 6
---

# Phase 15 Plan 02: HTTP Surface H-2 + H-3 Fix Summary

One-liner: Removed StopWatchdog suppression from OpenAI idle-timeout path (H-2) and added surface-native terminal error frames + WARN logging on mid-stream worker death to OpenAI SSE and Ollama NDJSON (H-3).

## Tasks Completed

| Task | Name | Commit | Files Changed |
|------|------|--------|---------------|
| 1 | H-2: Remove StopWatchdog from OpenAI idle-timeout path (REL-HTTP-02) | a8df54e | sse.go, regression_rel_http_02_test.go |
| 2 | H-3: Surface-native terminal error frames + WARN log on mid-stream worker death (REL-HTTP-03) | 3db0ae8 | sse.go, ndjson.go, regression_rel_http_03_test.go (x2), ndjson_test.go |

## What Was Fixed

### H-2 (REL-HTTP-02) — OpenAI Idle-Timeout ACP Cancel Suppression

**Bug:** The `idleC` arm in `runSSEEmitter` called `run.StopWatchdog()` before returning. The watchdog's AfterFunc carries the ACP Cancel mechanism. Calling `StopWatchdog()` stopped the AfterFunc — meaning `session/cancel` was never issued to kiro-cli. The hung worker was returned to the free pool mid-abandoned-prompt. The same `StopWatchdog` call also appeared in the `applyChunk` write-error arm.

**Fix (lines ~460-490 in sse.go):** Removed the `if stop := run.StopWatchdog(); stop != nil { stop() }` block from both the `idleC` arm and the `applyChunk` write-error arm. The deferred `cancelFn` (set by the handler on handler return) now triggers the watchdog AfterFunc naturally so ACP Cancel fires. This mirrors the Ollama NDJSON idle-timeout path which already omitted `StopWatchdog()` on the idleC arm.

**Test:** `TestRegression_REL_HTTP_02_IdleTimeoutReturnsHungWorker` — unskipped, assertion flipped from `watchdogCalled==true` (pre-fix bug observable) to `watchdogCalled==false` (post-fix: watchdog not suppressed).

### H-3 (REL-HTTP-03) — Silent Mid-Stream Worker Death

**Bug:** When `run.Stream().Result()` returned a non-nil error in `finalizeSSE` (OpenAI) and `finalizeNDJSON` (Ollama), both logged at Debug and returned without emitting a terminal frame. Clients received HTTP 200 + partial content + clean TCP close — a half-finished answer presented as complete. LangFlow's NDJSON aggregator never saw `done:true`.

**Fix — OpenAI `finalizeSSE` (sse.go ~lines 552-590):**
- Changed `logger.Debug` to `e.logger.Warn("openai: sse worker terminated mid-stream", ...)`
- Added D-09/D-10 fields: `session_id` (run.SessionID()), `worker_pid` (0 — not yet wired), `bytes_streamed` (0 — not yet wired), `err` (rerr), and `kiro_exit_code` (via `errors.As(rerr, &exitErr)` — emitted only when chain contains `*exec.ExitError`)
- Added `fmt.Fprintf(e.w, "data: {\"error\":{\"type\":\"server_error\",\"code\":\"upstream_disconnect\",\"message\":\"worker terminated mid-stream\"}}\n\n")`
- Added `fmt.Fprintf(e.w, "data: [DONE]\n\n")` + `e.flusher.Flush()`
- Added `os/exec` import

**Fix — Ollama `finalizeNDJSON` (ndjson.go ~lines 542-584):**
- Changed `logger.Debug` to `logger.Warn("ollama: ndjson worker terminated mid-stream", ...)`
- Same D-09/D-10 field expansion as OpenAI fix
- Added isChat/!isChat branch mirroring the idle-timeout arm: builds `aggregateOllamaResponse`, calls `chatResponseToWire`/`generateResponseToWire`, sets `frame.Done = true`, `frame.DoneReason = "error"`, `frame.Error = "upstream_disconnect: worker terminated mid-stream"`, calls `marshalAndWrite`
- Added `os/exec` import

**Tests:** Both `TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent` (openai and ollama packages) — unskipped in same commit (D-03 compliance), assertions flipped from NOT-contains to DOES-contain.

### Anthropic Surface Asymmetry (Confirmed Unchanged)

`internal/adapter/anthropic/sse.go` lines 783-795 already emits `event: error` via `writeSSEError(e.w, e.flusher, errAPI, "stream terminated")` when `rerr != nil`. The Anthropic path also correctly does NOT call `StopWatchdog()` on the error path (only on the success path at line 804), so the watchdog fires Cancel naturally. No change needed for the Anthropic surface.

## Test Output Snippets

```
--- PASS: TestRegression_REL_HTTP_02_IdleTimeoutReturnsHungWorker (0.10s)
    post-fix verified: watchdogCalled=false watchdogStopped=false — ACP Cancel fires naturally via watchdog AfterFunc; no suppression on idle path
ok  otto-gateway/internal/adapter/openai  1.456s

--- PASS: TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent (0.00s)
    post-fix verified: error frame and [DONE] present in body after mid-stream worker death
ok  otto-gateway/internal/adapter/openai  2.909s

--- PASS: TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent (0.00s)
    post-fix verified: done:true and done_reason:error present in Ollama body after mid-stream worker death
ok  otto-gateway/internal/adapter/ollama  1.915s

ok  otto-gateway/internal/adapter/anthropic  2.093s
go build ./... OK
```

## WARN Log Field Coverage (D-09/D-10 Compliance)

| Field | OpenAI finalizeSSE | Ollama finalizeNDJSON |
|-------|-------------------|----------------------|
| session_id | run.SessionID() | run.SessionID() |
| worker_pid | 0 (placeholder — RunHandle interface lacks PID accessor) | 0 (same) |
| kiro_exit_code | errors.As(*exec.ExitError).ExitCode() — omitted if not available | same |
| bytes_streamed | 0 (placeholder — no counter in sseEmitter) | 0 (same) |
| err | rerr | rerr |

All 4 required D-09/D-10 fields are present in each WARN call. `worker_pid` and `bytes_streamed` are logged as 0 with code comments noting they are placeholders — the RunHandle interface and emitter structs would need extension to carry real values. `kiro_exit_code` is only logged when the error chain contains `*exec.ExitError`, preserving the "omit if not available" semantic from the plan.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Updated TestNDJSON_StreamResultError in ndjson_test.go**

- **Found during:** Task 2 — full adapter test suite run
- **Issue:** `TestNDJSON_StreamResultError` (line 302 in ndjson_test.go) was an existing test that asserted `done:true` was NOT emitted on the `Stream().Result()` error path. After the H-3 fix, the behavior changed and this test failed with: `stream error path: emitted done:true line, want none`.
- **Fix:** Updated the test to assert the post-fix behavior — `done:true` and `done_reason:error` MUST be present in the response body. The test was documenting the old buggy behavior; updated to document correct behavior.
- **Files modified:** `internal/adapter/ollama/ndjson_test.go`
- **Commit:** 3db0ae8 (included in Task 2 atomic commit)

## Anthropic Asymmetry Note

The Anthropic SSE adapter (`internal/adapter/anthropic/sse.go`) already handles mid-stream worker death correctly at the time of this fix:
- Line 784: `final, rerr := run.Stream().Result()`
- Lines 784-795: When `rerr != nil`, calls `writeSSEError(e.w, e.flusher, errAPI, "stream terminated")` — emits `event: error` with `{"type":"error","error":{"type":"overloaded_error","message":"stream terminated"}}`
- StopWatchdog is correctly called only on the success path (line 804), NOT on the error path — so Cancel fires naturally on error
- No change required; no regression introduced

This asymmetry is intentional and documented in the Anthropic spec: `event: error` is the correct terminal frame for the Anthropic SSE surface.

## Known Stubs

- `worker_pid: 0` in both WARN log calls — RunHandle interface has no PID accessor. The underlying `engine.Run` struct and `acp.Client` do have access to `cmd.Process.Pid` but it is not surfaced through the RunHandle interface. This is a tracked placeholder; a future phase could add `WorkerPID() int` to RunHandle if operators need precise PID in the WARN log.
- `bytes_streamed: 0` in both WARN log calls — neither `sseEmitter` nor the NDJSON emitter track a bytes-written counter. Future phase could add a counter to the emitter structs.

Neither stub prevents the plan's goal from being achieved — the WARN log and terminal frames are both present and correct. The stubs are observability placeholders only.

## Threat Flags

None. Error messages are static strings (`"upstream_disconnect: worker terminated mid-stream"`) — no kiro-cli internal error detail is echoed to clients. No new network endpoints, auth paths, or trust boundary crossings introduced. T-15-04 (Information Disclosure) disposition confirmed: `accept` — static error string, no internal detail leak.

## Self-Check: PASSED

Files exist:
- internal/adapter/openai/sse.go: FOUND
- internal/adapter/ollama/ndjson.go: FOUND
- internal/adapter/openai/regression_rel_http_02_test.go: FOUND
- internal/adapter/openai/regression_rel_http_03_test.go: FOUND
- internal/adapter/ollama/regression_rel_http_03_test.go: FOUND
- internal/adapter/ollama/ndjson_test.go: FOUND

Commits exist:
- a8df54e (Task 1: H-2): FOUND in git log
- 3db0ae8 (Task 2: H-3): FOUND in git log

Tests pass:
- TestRegression_REL_HTTP_02: PASS
- TestRegression_REL_HTTP_03 (openai): PASS
- TestRegression_REL_HTTP_03 (ollama): PASS
- Full adapter suite (openai + ollama + anthropic): PASS
- go build ./...: OK
