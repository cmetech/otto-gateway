---
finding: G-1
severity: M
rel_id: REL-HOOKS-01
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# Finding G-1: Non-streaming aggregation error paths skip PostHooks

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §3 (Goroutine and resource discipline):

> **[Medium] G-1: Non-streaming aggregation error paths skip PostHooks — `LoggingHook.startTimes` and `ChatTraceHook.startTimes` sync.Map entries leak unboundedly**
>
> **Files:** `internal/engine/collect.go:150-166` (idle-timeout return) and `:167-171` (`Result()` error return) — both return before the PostHook traversal at `:187-191`; `internal/adapter/anthropic/collect.go:169-180` and `:182-185` — both return before `RunPostHooks` at `:207`. Leaked state: `internal/plugin/logging.go:186` (`startTimes.Store`, reclaimed only by `:215` `LoadAndDelete` in `After`); `internal/plugin/trace.go:222` (reclaimed only by `:248`).
>
> **Failure scenario:** Every non-streaming request that dies in aggregation (idle timeout → 504, stream `Result()` error → 500) permanently strands one entry per stateful hook, keyed by a never-repeating ULID. kiro-cli wedges while LangFlow polls `/api/chat` non-streaming on retry: every request leaks two sync.Map entries forever — slow, invisible memory growth on an unattended laptop. Secondary silent failure: `chat-trace.log` never gets its `post_chain_out` record for failed requests.

## Current-source check

Verified against current source (commit `3a72d03`):

**`internal/engine/collect.go` (non-streaming path):**
- Line 150: `RangeChunksWithIdleTimeout` call; if `rangeErr != nil` (idle-timeout path)
- Line 165: `return nil, fmt.Errorf("engine: collect: %w", rangeErr)` — returns BEFORE PostHook traversal
- Line 167: `final, rerr := run.stream.Result()`; if `rerr != nil`
- Line 171: `return nil, fmt.Errorf("engine: collect result: %w", rerr)` — returns BEFORE PostHook traversal
- Line 187: PostHook traversal loop (`for _, h := range e.cfg.PostHooks`) — only reached on success

**`internal/adapter/anthropic/collect.go` (non-streaming Anthropic path):**
- Line 177: `return nil, loopErr` (idle-timeout path) — returns BEFORE `RunPostHooks`
- Line 184: `return nil, fmt.Errorf("anthropic: collect result: %w", rerr)` — returns BEFORE `RunPostHooks`
- Line 207: `eng.RunPostHooks(ctx, req, resp)` — only reached on success

**`internal/plugin/logging.go`:**
- Line 186: `h.startTimes.Store(rid, time.Now())` — stores entry in Before
- Line 215: `h.startTimes.LoadAndDelete(rid)` — ONLY called from After (PostHook path)

**`internal/plugin/trace.go`:**
- Line 222: `h.startTimes.Store(rid, time.Now())` — stores entry in Before
- Line 248: `h.startTimes.LoadAndDelete(rid)` — ONLY called from After (PostHook path)

**Conclusion:** The failure path still exists exactly as cited. On non-streaming error paths, `LoadAndDelete` is never invoked, leaving sync.Map entries orphaned for the process lifetime.

## Evidence

This is a Medium finding per D-02 (code-walk + t.Skip'd regression test).

**Go regression test:** `internal/plugin/regression_rel_hooks_01_test.go`
- Function: `TestRegression_REL_HOOKS_01_NonStreamingPostHookSkip`
- Pre-fix observable: after `Before` populates `startTimes` and the error path is taken without calling `After`, `hook.startTimes.Range` returns count > 0 (leaked entries).
- The test calls `Before` (which stores the entry), then simulates the engine error path by NOT calling `After` — directly asserting the sync.Map leak.

**Code-walk summary:** `engine.Collect`'s `RangeChunksWithIdleTimeout` error-return (line 165) and `Result()` error-return (line 171) both precede the PostHook loop at line 187. The two hooks that stash state (`LoggingHook` and `ChatTraceHook`) each have `startTimes.Store` in their `Before` method and `startTimes.LoadAndDelete` only in their `After` method. When `After` is never called (the PostHook loop is skipped), every entry stored by `Before` leaks. On a retry storm (LangFlow hitting a wedged kiro-cli at `/api/chat` non-streaming), this means O(requests) orphaned entries per process restart — invisible in any health endpoint, observable only via pprof `runtime.MemStats`.

## Verdict

**confirmed** — The cite is intact. Both error-return sites in `engine/collect.go` and `anthropic/collect.go` demonstrably skip the PostHook traversal. No mitigation has been added since the review. The fix belongs in Phase 16 (Medium scope): invoke the PostHook chain with a nil or partial response on the idle-timeout and Result()-error returns, mirroring the streaming discipline already present in those same files.
