---
finding: P-4
severity: M
rel_id: REL-POOL-04
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# Finding P-4: Slow/Stalled Chunk Consumer Blocks readLoop — Ping Escalation SIGKILLs Healthy Worker

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §1 P-4 (Medium):

> **Files:** `internal/acp/stream.go:105-122` (`push` blocks the caller — the readLoop — when the 64-chunk buffer is full), `internal/acp/client.go:1085` (push called from `handleNotification`, on the readLoop goroutine), `internal/acp/client.go:503-526` (pingLoop: a ping that times out after 10s with `DeadlineExceeded` escalates to `c.cancel()` and kills the subprocess).
>
> **Failure scenario:** A client laptop lid closes mid-stream. The SSE write stalls, the handler stops draining `Chunks`, the 64-slot buffer fills, and `push` blocks the readLoop — the only goroutine that dispatches inbound frames, including ping responses. The next ping times out and pingLoop SIGKILLs a perfectly healthy kiro-cli.

## Current-source check

**Current file:line:** `internal/acp/stream.go:105-122` (push blocks caller with `select` on `s.chunks <- ch`), `internal/acp/client.go:1085` (push called on the readLoop goroutine using `c.clientCtx`), `internal/acp/client.go:503-526` (pingLoop escalation to `c.cancel()`).

The failure path is intact in current main. At `stream.go:111-121`, the `push` method blocks on:

```go
select {
case s.chunks <- ch:
    ...
case <-ctx.Done():
    return fmt.Errorf(...)
case <-s.done:
    return errPushAfterClose
}
```

When called from `handleNotification` at `client.go:1085`, the `ctx` argument is `c.clientCtx` (the client lifetime context) — NOT the per-request context. When the 64-chunk buffer fills and the consumer stalls, `push` blocks the readLoop goroutine until: (a) the consumer drains the buffer, (b) the client closes (`c.clientCtx` cancels), or (c) the stream closes (`s.done` fires). Since (b) and (c) don't happen while the consumer is merely stalled (lid closed, not disconnected), the readLoop is stuck for as long as the consumer stays stalled.

The readLoop is the only goroutine that processes inbound frames, including ping responses. The pingLoop at `client.go:503-526` sends a ping every `PingInterval` with a 10s timeout. With the readLoop stuck, the ping response frame never arrives, the 10s deadline fires, and `c.cfg.Logger.Warn("acp.ping.escalated_to_close", ...)` is emitted followed by `c.cancel()` — which kills the kiro-cli subprocess via WaitDelay → SIGKILL.

No new guard distinguishing "consumer stalled" from "worker dead" has been added since the review.

## Evidence

Regression test: `internal/pool/regression_rel_pool_04_test.go::TestRegression_REL_POOL_04_ConsumerBlockedReadLoop`

Skip string: `t.Skip("REL-POOL-04 (P-4): regression test — unskip in Phase 16 fix commit")`

The reproducer demonstrates the structural cause: `push` at `stream.go:111` uses the client lifetime context (`c.clientCtx`), not the per-request ctx, so a stalled consumer blocks the readLoop indefinitely rather than failing its own request. The post-fix path (Phase 16) bounds push with the per-request ctx or introduces a liveness-independent ping dispatch path.

## Verdict

**confirmed**

The cited `stream.go:105-122` push implementation uses the client lifetime context (`c.clientCtx`) at `client.go:1085`, making the readLoop's liveness directly coupled to consumer drain speed. The pingLoop at `client.go:503-526` has no mechanism to distinguish "worker dead" (true escalation case) from "readLoop busy blocked on consumer" (false escalation case). No fix has been applied. Per D-11 bias: confirmed.
