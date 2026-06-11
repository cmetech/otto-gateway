---
finding: H-2
severity: H
rel_id: REL-HTTP-02
status: confirmed
target_phase: 15
verified_at: "2026-06-11"
---

# H-2: OpenAI SSE Idle-Timeout Stops Engine Watchdog Without Issuing ACP Cancel

## Review Citation

**Source:** `docs/reviews/2026-06-11-reliability-review.md` §H-2 (section "2. HTTP surface reliability")

> The D-06 watchdog (`context.AfterFunc` → `ACP.Cancel(sid)`) is the only mechanism that sends `session/cancel` when an adapter abandons a stream. On the OpenAI `idleC` branch, `run.StopWatchdog()()` unregisters the AfterFunc while the ctx is still live — and no explicit Cancel is issued. The comment claims it suppresses a "redundant" cancel; that's only true on the `ctx.Done` branch. The idle case is exactly backwards: the timeout fires *because* kiro is wedged.

Cited source file:lines: `internal/adapter/openai/sse.go:460-462` (idle-timeout branch) and `:482-484` (applyChunk write-error branch).

## Current-Source Check

**`internal/adapter/openai/sse.go:454-474`** — idle-timeout branch verified:

```go
case <-idleC:
    if stop := run.StopWatchdog(); stop != nil {
        stop()  // ← line 461: stops the AfterFunc that would call ACP.Cancel
    }
    e.logger.Warn("stream.idle_timeout", ...)
    _, _ = fmt.Fprintf(w, "data: {\"error\":...}\n\n")
    _, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
    e.flusher.Flush()
    return e.aggregatedResponse(canonical.StopUnknown, nil),
        fmt.Errorf("openai: sse %w", canonical.ErrStreamIdleTimeout)
```

Confirmed at line 460-462: `run.StopWatchdog()()` is called. The `StopWatchdog()` function returns the AfterFunc stop function registered by `context.AfterFunc` inside `engine.go`. Calling `stop()` deregisters the AfterFunc — meaning `ACP.Cancel(sid)` will never fire on this path. There is no compensating explicit `Cancel` call anywhere in the idle branch.

**`internal/adapter/openai/sse.go:481-484`** — applyChunk write-error branch:

```go
if err := e.applyChunk(c); err != nil {
    if stop := run.StopWatchdog(); stop != nil {
        stop()  // ← line 483: same pattern; also suppresses Cancel
    }
    return e.aggregatedResponse(canonical.StopUnknown, nil), err
}
```

Same pattern: StopWatchdog stops the AfterFunc without issuing Cancel.

**Comparison with correct sibling paths:**

- `internal/engine/collect.go:159-165` (idle-timeout in non-streaming aggregation): does NOT call StopWatchdog on the idle path — Cancel fires naturally via the AfterFunc. This is the correct pattern.
- `internal/adapter/ollama/ndjson.go:420-452` (Ollama idle-timeout): does NOT call StopWatchdog — leaves the AfterFunc intact to fire Cancel. Correct.
- `internal/adapter/anthropic/sse.go` (Anthropic error paths): leaves Cancel intact. Correct.

Only the OpenAI SSE adapter suppresses the AfterFunc on the idle path without compensation.

## Evidence

Regression test file: `internal/adapter/openai/regression_rel_http_02_test.go`
Function: `TestRegression_REL_HTTP_02_IdleTimeoutReturnsHungWorker`

The test drives `runSSEEmitter` with a `trackingRunHandle` whose `StopWatchdog()` records when it is called and when the returned stop function fires. With a 100ms idle timeout and a never-producing chunks channel, the `idleC` branch fires and `StopWatchdog()` is called. Pre-fix observable: `watchdogCalled=true` and `watchdogStopped=true` — the AfterFunc was suppressed without any compensating Cancel. The test is currently `t.Skip`'d per D-12 and will be unskipped in the Phase 15 fix commit.

The pool's ctx-watcher (`internal/pool/pool.go:614-635`) still releases the slot when the streaming handler's derived context is cancelled (after `runSSEEmitter` returns). The slot enters the free pool with a still-generating kiro-cli session. The next `Pool.NewSession` call acquires that slot mid-abandoned-prompt.

## Verdict

**CONFIRMED** — The cited code pattern is present in current source exactly as described. `StopWatchdog()()` is called on the `idleC` branch at `sse.go:460-462` without a prior or subsequent explicit `ACP.Cancel(sid)` call. The correct siblings (`collect.go`, `ndjson.go`) confirm the intended pattern is to leave the AfterFunc intact (or call Cancel explicitly before stopping it).

Assigned to Phase 15 for fix. Fix options per review: (1) Don't call `stop()` on the idle path — let the deferred `cancelFn` in the handler trigger the watchdog naturally; (2) Call an explicit `Cancel(sid)` before `stop()`, like `CollectFromRun` does in the non-streaming path.
