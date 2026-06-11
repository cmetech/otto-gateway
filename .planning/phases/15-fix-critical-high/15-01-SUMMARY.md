---
phase: 15-fix-critical-high
plan: "01"
subsystem: pool,acp,server,admin,adapters
tags: [reliability, shutdown, pool-exhaustion, stale-stream, sse]
dependency_graph:
  requires: []
  provides: [REL-POOL-01, REL-POOL-02, REL-POOL-03, REL-HTTP-01]
  affects: [internal/pool, internal/acp, internal/server, internal/admin, internal/adapter/openai, internal/adapter/ollama, internal/adapter/anthropic, cmd/otto-gateway]
tech_stack:
  added: []
  patterns: [bounded-channel-acquire, shutdown-channel, identity-guarded-nil, two-signal-sigint]
key_files:
  created: []
  modified:
    - internal/pool/pool.go
    - internal/pool/config.go
    - internal/acp/client.go
    - internal/server/server.go
    - internal/admin/admin.go
    - internal/admin/sse.go
    - internal/admin/sse_test.go
    - internal/adapter/openai/sse.go
    - internal/adapter/openai/handlers.go
    - internal/adapter/ollama/ndjson.go
    - internal/adapter/ollama/handlers.go
    - internal/adapter/anthropic/sse.go
    - internal/adapter/anthropic/handlers.go
    - cmd/otto-gateway/main.go
    - internal/pool/regression_rel_pool_01_test.go
    - internal/pool/regression_rel_pool_02_test.go
    - internal/pool/regression_rel_pool_03_test.go
    - internal/pool/pool_test.go
    - internal/server/regression_rel_http_01_test.go
decisions:
  - "Re-queue transient respawn failures instead of removeSlot: preserves pool effective size on transient failures, avoids permanent shrink under laptop sleep / fd exhaustion scenarios (REL-POOL-01 D-08)"
  - "closeAll() cancels in-flight sessions before Client.Close(): ensures kiro-cli receives cancel signal for mid-generation sessions on pool shutdown (REL-POOL-02)"
  - "sharedShutdownCh wired via main.go local, not a global: avoids import cycle between admin and server packages"
  - "identity guard c.activeStream == stream (not atomic.Pointer): existing streamMu is sufficient; atomic.Pointer would be over-engineering for the current Phase 1 single-session-per-slot invariant"
metrics:
  duration: "~4 hours (across 2 sessions)"
  completed: "2026-06-11"
  tasks_completed: 3
  files_modified: 19
  test_commits: 3
---

# Phase 15 Plan 01: Fix 4 Critical/High Reliability Findings Summary

Bounded pool acquire + ErrPoolExhausted sentinel, shutdown channel propagation with two-signal SIGINT and admin SSE unwind, identity-guarded activeStream nil to prevent stale goroutine stream clobber.

## Objective

Fix 4 confirmed reliability findings: P-1 (Critical), P-2 (High), P-3 (High), H-1 (High).
Close the load-bearing pool exhaustion + orphaned-process + stale-stream + SSE-shutdown-block
failure modes that make the gateway unreliable under everyday Ctrl-C and pool-pressure scenarios.

## Tasks Completed

| Task | Finding | Commit | Files |
|------|---------|--------|-------|
| 1 | REL-POOL-01 (P-1): bounded acquire + ErrPoolExhausted + transient re-queue | 4fd879b | pool.go, config.go, 3 adapter sse/ndjson files, regression_rel_pool_01_test.go |
| 2 | REL-POOL-02 (P-2) + REL-HTTP-01 (H-1): shutdown plumbing | 4f33e89 | main.go, server.go, admin.go, sse.go, sse_test.go, pool.go, pool_test.go, 2 regression tests |
| 3 | REL-POOL-03 (P-3): identity-guarded activeStream nil | fcf9f3c | acp/client.go, regression_rel_pool_03_test.go |

## What Was Built

### REL-POOL-01 (P-1): Bounded Pool Acquire

- `internal/pool/config.go`: Added `AcquireTimeout time.Duration` field parsed from `POOL_ACQUIRE_TIMEOUT_MS` env var (default 30s).
- `internal/pool/pool.go`: Added `ErrPoolExhausted` sentinel. `NewSession` now has a `timeoutC` arm in the acquire select that returns `ErrPoolExhausted` after `AcquireTimeout`. Transient respawn failures re-queue the slot instead of calling `removeSlot` — prevents permanent pool size reduction.
- `internal/adapter/openai/sse.go`: `writePoolExhaustedOpenAI` helper — HTTP 503 + `Retry-After: 5` + D-07 OpenAI error body `{"error":{"type":"server_error","code":"pool_exhausted",...}}`.
- `internal/adapter/ollama/ndjson.go`: `writePoolExhaustedOllama` helper — HTTP 503 + D-07 Ollama body `{"error":"pool_exhausted: ..."}`.
- `internal/adapter/anthropic/sse.go`: `writePoolExhaustedAnthropic` helper — HTTP 503 + D-07 Anthropic body `{"type":"error","error":{"type":"overloaded_error",...}}`.
- Adapters (`handlers.go` for each surface): `errors.Is(err, pool.ErrPoolExhausted)` check calling the respective helper.

### REL-POOL-02 (P-2): Shutdown Cleanup

- `cmd/otto-gateway/main.go`: Explicit `cleanup()` before `os.Exit(1)` in the non-nil error path so pool.Close() always runs.
- `internal/pool/pool.go` (`closeAll`): Now iterates `p.sessionSlots` and calls `client.Cancel(sid)` for each in-flight session before calling `client.Close()` — ensures kiro-cli receives cancel signal for mid-generation sessions.

### REL-HTTP-01 (H-1): Admin SSE Shutdown Unwind

- `internal/server/server.go`: Added `shutdownCh chan struct{}` field. `Run()` calls `srv.RegisterOnShutdown` to close `shutdownCh` when HTTP server shutdown begins. `RunUntilSignal()` rewritten with two-signal pattern: first SIGINT = graceful cancel, second SIGINT = force exit via `forceErrCh`.
- `internal/admin/admin.go`: Added `ShutdownCh <-chan struct{}` to `Deps`.
- `internal/admin/sse.go`: `sseLoop` gains `shutdownCh <-chan struct{}` parameter. New `case <-shutdownCh: return errors.New("admin: gateway shutting down")` arm exits the SSE loop within 1s of shutdown signal.
- `cmd/otto-gateway/main.go`: `sharedShutdownCh` local wired to both `admin.Deps.ShutdownCh` and `server.Config.ShutdownCh`.

### REL-POOL-03 (P-3): Identity-Guarded activeStream Nil

- `internal/acp/client.go`: Both arms of `awaitPromptResult` (ctx.Done() and frame received) now guard `c.activeStream = nil` with identity check: `if c.activeStream == stream { c.activeStream = nil }`. Prevents stale goroutine from clobbering a newer Prompt call's stream pointer after slot recycling. Also applied to the readLoop's defer for consistency. Two sites total.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] closeAll() missing in-flight session Cancel**
- **Found during:** Task 2 (REL-POOL-02 regression test failure)
- **Issue:** `closeAll()` called `client.Close()` on all slots but never called `client.Cancel(sid)` for in-flight sessions stored in `p.sessionSlots`. The test asserted `cancelsAfter >= 2` but only got 1 (the warmup session cancel from slot-0's warmup NewSession).
- **Fix:** Added iteration over `p.sessionSlots` in `closeAll()`, calling `client.Cancel(sid)` for each in-flight session before calling `client.Close()`.
- **Files modified:** `internal/pool/pool.go`
- **Commit:** 4f33e89

**2. [Rule 1 - Bug] Admin SSE test sseLoop signature mismatch**
- **Found during:** Task 2 (full test suite run after adding shutdownCh parameter)
- **Issue:** `internal/admin/sse_test.go` called `sseLoop` with 6 args but the updated signature requires 7 (added `shutdownCh <-chan struct{}`).
- **Fix:** Added `nil` as the 7th argument at both call sites in `sse_test.go` (nil channel is safe — never selected).
- **Files modified:** `internal/admin/sse_test.go`
- **Commit:** 4f33e89

**3. [Rule 1 - Bug] D-03 shrink tests broken by Task 1's re-queue change**
- **Found during:** Task 2 (full test suite run)
- **Issue:** `TestPool_DeadSlot_RespawnFailure_PoolShrinks` and `TestPool_Detail_AfterShrinkOnRespawnFailure` asserted `p.all` size == 0 after respawn failure (old D-03 shrink behavior). Task 1 intentionally changed this to re-queue — `p.all` stays at size 1.
- **Fix:** Updated both tests to assert the new correct behavior: size == 1 (slot re-queued, pool size preserved). Updated docstrings and test names to reflect REL-POOL-01 D-08 behavior.
- **Files modified:** `internal/pool/pool_test.go`
- **Commit:** 4f33e89

**4. [Rule 1 - Bug] REL-POOL-03 test chunksDelivered never incremented**
- **Found during:** Task 3 (implementing the test assertion flip)
- **Issue:** The original test had `var chunksDelivered int64` but never incremented it anywhere in the loop body. The plan's instruction to flip the assertion to `chunksDelivered == iterations` would have created `0 == 20` which is always false.
- **Fix:** Replaced dead-code `chunksDelivered` with `successfulCompletions int64` tracked by checking that `streamB.Result()` returns a non-nil `FinalResult` with `StopReason == canonical.StopEndTurn`. Also fixed B's promptFn goroutine to call `CloseForTest` synchronously (not in a goroutine) to avoid a pre-existing race in `acp.Stream` between `close(s.done)` and `s.result.StopReason` write.
- **Files modified:** `internal/pool/regression_rel_pool_03_test.go`
- **Commit:** fcf9f3c

## Test Results

All 4 regression tests pass under `-race`:

```
--- PASS: TestRegression_REL_POOL_01_PoolShrinksToZero (0.02s)
--- PASS: TestRegression_REL_POOL_02_CtrlCOrphansChildren (0.15s)
--- PASS: TestRegression_REL_POOL_03_StaleActiveStreamClobber (0.03s)
--- PASS: TestRegression_REL_HTTP_01_ShutdownBlocksOnAdminSSE (0.00s)
```

Full suite: `go test -race ./...` — all packages PASS.

## Acceptance Criteria Verification

| Check | Value | Status |
|-------|-------|--------|
| `grep -c 'ErrPoolExhausted' pool.go` | 5 | PASS |
| `grep -c 'pool_exhausted' adapter/openai/sse.go` | 2 | PASS |
| `grep -c 'pool_exhausted:' adapter/ollama/ndjson.go` | 2 | PASS |
| `grep -c 'overloaded_error' adapter/anthropic/sse.go` | 2 | PASS |
| `grep -c 'shutdownCh' server/server.go` | 14 (>= 3) | PASS |
| `grep -c 'shutdownCh' admin/sse.go` | 4 (>= 2) | PASS |
| `grep -c 'cleanup()' main.go` | 6 (>= 2) | PASS |
| `grep -c 'c.activeStream == stream' acp/client.go` | 2 | PASS |
| `go build ./...` | exit 0 | PASS |

## Known Stubs

None. All implemented functionality is fully wired.

## Threat Flags

None. No new network endpoints, auth paths, file access patterns, or schema changes introduced.

## Self-Check: PASSED

Commits verified:
- 4fd879b: Task 1 (REL-POOL-01)
- 4f33e89: Task 2 (REL-POOL-02 + REL-HTTP-01)
- fcf9f3c: Task 3 (REL-POOL-03)

All modified files exist in the worktree.
