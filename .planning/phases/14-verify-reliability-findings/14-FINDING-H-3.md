---
finding: H-3
severity: H
rel_id: REL-HTTP-03
status: confirmed
target_phase: 15
verified_at: "2026-06-11"
---

# H-3: Mid-Stream Worker Death Is Silent Truncation on OpenAI and Ollama Surfaces

## Review Citation

**Source:** `docs/reviews/2026-06-11-reliability-review.md` §H-3 (section "2. HTTP surface reliability")

> kiro-cli dies mid-stream; the chunk channel closes and `Result()` returns an error. OpenAI logs at *debug* and returns — no error data-frame, no `finish_reason`, no `[DONE]`. Ollama logs at debug and returns — no `done:true` line. The client sees a 200 with partial deltas and a clean TCP close. Pi-SDK (hard-coded `stream:true`) and most OpenAI SDKs end iteration on stream close — a half-finished answer is presented to the user as if it completed.

Cited source file:lines: `internal/adapter/openai/sse.go:543-557` (finalizeSSE `rerr != nil`), `internal/adapter/ollama/ndjson.go:541-549` (finalizeNDJSON `rerr != nil`).

## Current-Source Check

**`internal/adapter/openai/sse.go:543-558`** — `finalizeSSE` error path verified:

```go
func finalizeSSE(e *sseEmitter, run RunHandle) (*canonical.ChatResponse, error) {
    final, rerr := run.Stream().Result()
    if rerr != nil {
        // Mid-stream / terminal engine error after headers: cannot send JSON 500.
        // Log at debug (not error — the stream just cut off; the client-side
        // will see a truncated stream, which is acceptable per A5).
        if stop := run.StopWatchdog(); stop != nil {
            stop()
        }
        e.logger.Debug("openai: sse stream result error", "err", rerr)
        return e.aggregatedResponse(canonical.StopUnknown, nil), fmt.Errorf("openai: sse stream result: %w", rerr)
    }
    // ...
}
```

Confirmed: when `rerr != nil`, the function stops the watchdog, logs at **Debug**, and returns an error. No `data: {"error":...}` frame is written, no `data: [DONE]` is emitted. The SSE stream ends with the last partial text delta, a clean TCP close, and no terminal signal.

Compare the idle-timeout path at `sse.go:470-472` which DOES emit both:
```go
_, _ = fmt.Fprintf(w, "data: {\"error\":{\"message\":\"stream idle timeout\",\"type\":\"api_error\"}}\n\n")
_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
```

**`internal/adapter/ollama/ndjson.go:541-549`** — `finalizeNDJSON` error path verified:

```go
func finalizeNDJSON(...) (*canonical.ChatResponse, error) {
    final, rerr := run.Stream().Result()
    if rerr != nil {
        // Mid-stream / terminal engine error after headers: cannot send JSON 500.
        // Debug-log and return; no done:true line (D-05 truncation).
        logger.Debug("ollama: ndjson stream result error", "err", rerr)
        return aggregateOllamaResponse(req, state, canonical.StopUnknown), fmt.Errorf("ollama: ndjson stream result: %w", rerr)
    }
    // ...
}
```

Confirmed: when `rerr != nil`, the function logs at **Debug** and returns. No `{"done":true,"done_reason":"error"}` terminal line is emitted. LangFlow consumers that wait for `done:true` will never see end-of-stream.

Compare the idle-timeout path at `ndjson.go:437-450` which DOES emit full terminal envelope with `done:true` and `done_reason:"error"`.

**Note:** The Anthropic surface (`internal/adapter/anthropic/sse.go`) handles this correctly — it emits `event: error` on the error path.

## Evidence

Two regression test files are produced for this finding because H-3 spans both surfaces:

**OpenAI variant:**
- Regression test file: `internal/adapter/openai/regression_rel_http_03_test.go`
- Function: `TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent`
- Pre-fix observable: body does NOT contain `data: {"error":` and does NOT contain `data: [DONE]`

**Ollama variant:**
- Regression test file: `internal/adapter/ollama/regression_rel_http_03_test.go`
- Function: `TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent`
- Pre-fix observable: body does NOT contain `"done_reason":"error"` and does NOT contain `"done":true`

Both tests drive their respective emitters with a `fakeStream` whose `Result()` returns `errors.New("worker died")`. Both tests confirm the pre-fix silent truncation behavior and are currently `t.Skip`'d per D-12. Both will be unskipped in the Phase 15 fix commit with assertions flipped to confirm error frames are present.

## Verdict

**CONFIRMED** — Both `finalizeSSE` (`openai/sse.go:543-557`) and `finalizeNDJSON` (`ollama/ndjson.go:541-549`) silently truncate on the `rerr != nil` path in current source. The idle-timeout paths in both files already demonstrate the correct pattern (terminal error frame + done marker). The fix is to apply the same frame emission to the `Result()` error path in both functions, and log at WARN instead of Debug.

Assigned to Phase 15 for fix. Phase 15 must unskip both `TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent` files atomically (one OpenAI, one Ollama) in a single fix commit.
