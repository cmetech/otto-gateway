---
finding: P-2
severity: H
rel_id: REL-POOL-02
status: confirmed
target_phase: 15
verified_at: 2026-06-11
---

# Finding P-2: os.Exit(1) on Shutdown-Grace Expiry Skips cleanup() — kiro-cli Process Trees Orphaned

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §1 P-2 (High):

> **Files:** `cmd/otto-gateway/main.go:127` (`defer cleanup()`), `cmd/otto-gateway/main.go:129-132` (`os.Exit(1)` on `RunUntilSignal` error), `internal/server/server.go:377-381` (`srv.Shutdown` 30s deadline).
>
> **Failure scenario:** User hits Ctrl-C while a long LLM generation is streaming (routinely > 30s). `http.Server.Shutdown` does NOT cancel in-flight request contexts, and the stream is healthy, so neither the engine watchdog nor the 30s `StreamIdleTimeout` fires. The shutdown deadline expires, `Run` returns an error, and `main` calls `os.Exit(1)` — bypassing `defer cleanup()`, so `registry.Close()`/`pool.Close()` never run. Because `applyPgidAttr` puts every kiro-cli in its own process group, the terminal's SIGINT never reached them and parent death does not kill them: up to `POOL_SIZE` + active-session kiro-cli trees keep running, reparented to init.

## Current-source check

**Current file:line:** `cmd/otto-gateway/main.go:127` (deferred cleanup), `cmd/otto-gateway/main.go:129-132` (os.Exit(1) on RunUntilSignal error), `internal/server/server.go:377-381` (30s shutdown deadline).

The failure path is intact in current main. The exact flow:

1. `cmd/otto-gateway/main.go:127`: `defer cleanup()` is registered.
2. `cmd/otto-gateway/main.go:129`: `app.srv.RunUntilSignal(bootCtx)` is called.
3. `internal/server/server.go:377-381`: `srv.Shutdown(shutdownCtx)` with a 30s `context.WithTimeout`. When long-running SSE streams are present, Shutdown blocks until the deadline and returns `DeadlineExceeded`.
4. `cmd/otto-gateway/main.go:130-132`: the returned error is non-nil, so `os.Exit(1)` is called — this is Go's explicit process termination that **does not run deferred functions** (`defer cleanup()` is skipped).
5. `cleanup()` never runs; `pool.Close()` never runs; `registry.Close()` never runs; kiro-cli process groups are orphaned.

The deferred cleanup at line 127 covers the GRACEFUL shutdown path (RunUntilSignal returns nil), but NOT the error path at line 131. The fix described in the review (replace `os.Exit(1)` with `cleanup(); closeLogger(); os.Exit(1)`) has NOT been applied to current main.

## Evidence

Regression test: `internal/pool/regression_rel_pool_02_test.go::TestRegression_REL_POOL_02_CtrlCOrphansChildren`

Skip string: `t.Skip("REL-POOL-02 (P-2): regression test — unskip in Phase 15 fix commit")`

The reproducer builds a size-2 pool with two `blockingPromptClient` instances whose `Prompt` blocks until unblocked (simulating long in-flight generations). It demonstrates that Cancel is not called on either session UNLESS `pool.Close()` is explicitly invoked — reproducing the pre-fix state where `os.Exit(1)` skips cleanup. The post-fix assertion (Phase 15) inverts this: after the fix, the shutdown path must issue Cancel on all in-flight sessions before exiting.

## Verdict

**confirmed**

The `os.Exit(1)` at `cmd/otto-gateway/main.go:131` bypasses `defer cleanup()` on the shutdown-grace-expiry path. This is the most common shutdown path: Ctrl-C during a long generation routinely exceeds the 30s grace period. The deferred cleanup is NOT unconditional — it only fires on clean-exit paths. Per D-11 (confirmed bias): the cite is intact, the bypass is by Go's language semantics (`os.Exit` skips defers), and no alternative cleanup invocation has been added on the error branch.
