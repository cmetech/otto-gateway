---
finding: H-1
severity: H
rel_id: REL-HTTP-01
status: confirmed
target_phase: 15
verified_at: "2026-06-11"
---

# H-1: Graceful Shutdown Blocks 30s on Open Admin SSE Connection

## Review Citation

**Source:** `docs/reviews/2026-06-11-reliability-review.md` §H-1 (section "2. HTTP surface reliability")

> `http.Server.Shutdown` waits for active connections; it does not cancel request contexts, and no server-shutdown signal is wired into them (no `BaseContext` cancel, no `RegisterOnShutdown`). The admin `/admin/logs/stream` handler loops forever on `r.Context()` — indefinite by design. Operator has the Log Tail tab open and hits Ctrl-C or tray "stop": `Run` waits the full 30s, `Shutdown` returns `DeadlineExceeded`, and `main.go:129-132` logs "server stopped with error" and exits 1.

Cited source file:lines: `internal/server/server.go:346-383`, `internal/admin/sse.go:167-203`.

## Current-Source Check

**`internal/server/server.go:346-383`** — `Run()` method verified at commit `9212d5b`:

```go
func (s *Server) Run(ctx context.Context) error {
    srv := &http.Server{
        Addr:              s.addr,
        Handler:           s.router,
        ReadHeaderTimeout: 10 * time.Second,
        IdleTimeout:       120 * time.Second,
    }
    // ... ListenAndServe goroutine ...
    select {
    case err := <-errCh:
        return err
    case <-ctx.Done():
        s.logger.Info("context cancelled; shutting down")
    }

    shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    if err := srv.Shutdown(shutdownCtx); err != nil {
        return fmt.Errorf("server: shutdown: %w", err)
    }
    return nil
}
```

Confirmed: no `srv.BaseContext`, no `srv.RegisterOnShutdown`, no custom `ConnContext`. The `http.Server` is constructed at line 347 with only `ReadHeaderTimeout` and `IdleTimeout`.

**`internal/admin/sse.go:167-203`** — `sseLoop()` verified:

```go
func sseLoop(ctx context.Context, w io.Writer, flusher http.Flusher,
    sub *subscriber, tickerC <-chan time.Time, snapshot []string) error {
    // ...
    for {
        select {
        case <-ctx.Done():
            return fmt.Errorf("admin.sse: ctx done: %w", ctx.Err())
        case <-tickerC:
            // keepalive ping
            // ...
        case line, ok := <-sub.C:
            // live line delivery
            // ...
        }
    }
}
```

Confirmed: `sseLoop` exits only when `ctx.Done()` fires. The `ctx` passed is `r.Context()`, which is the request context. Go's `http.Server.Shutdown` drains connections gracefully by waiting for in-flight handlers to complete, but it does NOT cancel request contexts — only the `BaseContext` cancel or `RegisterOnShutdown` callbacks can push a cancellation into the request context. Neither is wired in this codebase.

**Net result:** A long-lived `/admin/logs/stream` connection with a client that keeps reading will hold the server open for the full 30s shutdown grace period. The gate-channel in `sseHandler` (via the 15s keepalive ticker) keeps pings flowing — the handler never returns on its own until ctx is cancelled.

## Evidence

Regression test file: `internal/server/regression_rel_http_01_test.go`
Function: `TestRegression_REL_HTTP_01_ShutdownBlocksOnAdminSSE`

The test opens a real SSE connection via `httptest.NewServer`, waits for the handler to confirm it is streaming, then calls `srv.Shutdown(ctx)` with a 6s deadline. Pre-fix observable: elapsed time of `Shutdown` call exceeds 2s (the SSE handler is alive and holding the connection). The test is currently `t.Skip`'d per D-12 and will be unskipped in the Phase 15 fix commit.

The finding interacts with **P-2** (`os.Exit(1)` skips `defer cleanup()`): if shutdown times out and returns `DeadlineExceeded`, `main.go` calls `os.Exit(1)`, bypassing `pool.Close()` and orphaning kiro-cli process trees.

## Verdict

**CONFIRMED** — The failure path described in the review is present in current source. Both cited file:line addresses are accurate. The Go stdlib `http.Server.Shutdown` behavior (no request-context cancellation) is working as designed; the gap is the missing wiring layer (no `BaseContext`, no `RegisterOnShutdown`) that would propagate the shutdown signal into the SSE handler's blocking loop.

Assigned to Phase 15 for fix. Fix options per review: `srv.RegisterOnShutdown(cancelStreams)` plus `sseLoop` selecting on a shutdown channel; or `BaseContext`/`ConnContext` with a cancel invoked before `srv.Shutdown`; or calling `srv.Close()` after `Shutdown` times out (treating timeout as clean exit).
