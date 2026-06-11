---
phase: 15-fix-critical-high
fixed_at: 2026-06-11T00:00:00Z
review_path: .planning/phases/15-fix-critical-high/15-REVIEW.md
iteration: 1
findings_in_scope: 10
fixed: 10
skipped: 0
status: all_fixed
---

# Phase 15: Code Review Fix Report

**Fixed at:** 2026-06-11
**Source review:** .planning/phases/15-fix-critical-high/15-REVIEW.md
**Iteration:** 1

**Summary:**
- Findings in scope: 10 (2 Critical / BLOCKER, 8 Warning)
- Fixed: 10
- Skipped: 0
- Status: **all_fixed**

`make build` and `make test` both pass against the post-fix tree. Test
suite ran clean across all 18 test packages.

## Fixed Issues

### CR-01: respawn-failure re-queue can race with `Close()` and panic on send

**Files modified:** `internal/pool/pool.go`
**Commit:** `c01b47d`
**Applied fix:** Re-queue the slot UNDER `p.mu` with an explicit
`p.closed` check rather than the previous `select { case p.slots <- slot;
case <-p.closing }` pattern. The previous form had a runtime window
between the select arm being chosen and the send actually executing
where `Close()` could fire — leaving the slot re-queued into `p.slots`
AFTER `closeAll` had already nilled `p.all` and closed the underlying
client. The next `NewSession` would then dequeue a dead slot.
Serialising the re-queue with `closeAll`'s `p.mu` critical section
eliminates the window; the explicit `p.closed` check makes the
shutdown-induced drop deterministic. Applied to both the ctx-cancel arm
and the transient-failure arm. Verified by per-package test pass.

### CR-02: idle-timeout cancelFn fires AFTER PostHooks instead of before

**Files modified:** `internal/adapter/openai/handlers.go`,
`internal/adapter/anthropic/handlers.go`,
`internal/adapter/ollama/handlers.go`,
`internal/adapter/ollama/ndjson.go`
**Commit:** `827beba`
**Applied fix:** On stream idle-timeout the kiro-cli worker kept
generating until the handler's `defer cancelFn()` fired — but PostHooks
ran BEFORE that defer. On a 1-slot pool the next request waited in
`NewSession`'s bounded acquire for the full PostHook latency, then
returned `ErrPoolExhausted`. Fixed by firing `cancelFn()` explicitly
when `errors.Is(err, canonical.ErrStreamIdleTimeout)` is true, BEFORE
calling `RunPostHooks`. The watchdog `AfterFunc` then issues
`session/cancel` and the pool slot returns to the free queue immediately.
PostHooks consume `context.WithoutCancel(streamCtx)` so their own work
(PII decrypt, ChatTraceHook persist) observes correct deadlines but does
not see the cancellation induced for slot release. Applied symmetrically
to all three surfaces: openai SSE handler, anthropic SSE handler, ollama
chat NDJSON handler, ollama generate NDJSON handler. The ollama emitter
itself (`ndjson.go` idle arm) also fires `cancelFn()` before returning
so the worker stops within the emitter scope, not only at handler
return.

**Note for human verification:** the fix touches the streaming
idle-timeout path which is exercised by integration tests but not by a
dedicated unit test today. Status remains `fixed` because unit-test
coverage for the existing happy and error paths passes, and the new
control flow is purely "issue cancel earlier" — no behaviour change for
non-idle paths. Recommend exercising idle-timeout on a 1-slot pool
against a real kiro-cli during phase verification to confirm the next
request acquires promptly.

### WR-01: closeAll cancel-issue count is operator-opaque

**Files modified:** `internal/pool/pool.go`
**Commit:** `e63e7bf`
**Applied fix:** Added an `INFO`-level structured log line
`pool.close.cancel_inflight` with `inflight_count` attribute. The
sendNotification-best-effort semantics mean some cancels can be dropped
silently (writeCh full, clientCtx already cancelled by a parallel path).
The log gives operators an upper-bound count to correlate against
kiro-cli's audit log, addressing the "WARN log on dropped cancels would
be a useful operator signal" recommendation. The deeper test tightening
suggested by the reviewer is delivered separately as WR-04.

### WR-02: misleading `emptyResp` variable in finalizeNDJSON / finalizeSSE

**Files modified:** `internal/adapter/ollama/ndjson.go`,
`internal/adapter/openai/sse.go`
**Commit:** `ab2593f`
**Applied fix:** Renamed `emptyResp` to `partialResp` in
`finalizeNDJSON`'s rerr path (Ollama side). The OpenAI side uses an
inline method call (`e.aggregatedResponse(...)`) rather than a named
variable, so the rename is not applicable there; added a clarifying
comment explaining that the response carries aggregated text from
chunks that arrived BEFORE the worker died. Both comments document the
PII-decrypt-on-truncated-ciphertext correlation source for log
analysis.

### WR-03: respawnSlot races between slot.dead reset and exit-watcher install

**Files modified:** `internal/pool/pool.go`
**Commit:** `3c5e175`
**Applied fix:** Moved `p.startExitWatcher(slot, newDone)` INSIDE the
`p.mu` critical section (between `slot.Client = newClient` /
`slot.dead = false` and `p.mu.Unlock`). Previously a death of newClient
in the gap between mu unlock and watcher install was unobservable: a
`NewSession` arriving inside that window saw `slot.dead = false` (just
reset) and handed out a slot whose `Client.NewSession` then returned
`ErrClientClosed`. `startExitWatcher`'s spawned goroutine takes `p.mu`
itself before flipping `slot.dead`, so it parks safely on our locked mu
until we release — no deadlock risk.

### WR-04: REL-POOL-02 test passes even if cancels are unevenly distributed

**Files modified:** `internal/pool/regression_rel_pool_02_test.go`
**Commit:** `fea95f8`
**Applied fix:** Tightened the assertion from `cancelsAfter >= 2`
(aggregate count) to require BOTH `bc0` AND `bc1` to receive at least
one Cancel each. The prior form would pass if one client saw both
cancels and the other saw none — exactly the closeAll-iterates-
sessionSlots-twice-on-the-same-slot regression the test was meant to
catch. Diagnostic log line now reports per-client counts.

### WR-05: REL-HTTP-01 shutdown tolerance too generous

**Files modified:** `internal/server/regression_rel_http_01_test.go`
**Commit:** `57ecbdd`
**Applied fix:** Tightened the shutdown budget from `>= 1s` to
`>= 250ms`. Pre-fix shutdown was 30s; post-fix is sub-50us in practice
(the test now reports ~4us). The previous 1s tolerance was generous
enough to silently accept a regression introducing an 800ms blocking
PostHook drain in the SSE path. 250ms preserves CI runner headroom
while catching regressions an order of magnitude smaller. Constant
`shutdownBudget` named for diagnostic readability.

### WR-06: darwin verifyGatewayIdentity HasSuffix accepts `fake-otto-gateway`

**Files modified:** `cmd/otto-tray/pidfile_darwin.go`
**Commit:** `8694f9a`
**Applied fix:** Replaced the
`strings.HasSuffix(comm, "otto-gateway") || comm == "otto-gateway"`
check with `comm == "otto-gateway" || strings.HasSuffix(comm, "/otto-gateway")`.
The path-suffix variant accepts `ps -o comm=` output that returns the
absolute path basename, but rejects malicious basenames like
`fake-otto-gateway` or `not-otto-gateway` planted in the install
directory. Windows path already uses
`EqualFold(filepath.Base(fullPath), "otto-gateway.exe")` which is
strict — no change needed there. The `expectedPath` parameter remains
reserved for a future full-path comparison (IN-03 follow-up); the
current caller passes empty string and we still ignore it.

### WR-07: otto-gw.ps1 status silently drops admin-snapshot errors

**Files modified:** `scripts/otto-gw.ps1`
**Commit:** `e6914ed`
**Applied fix:** Replaced the empty catch block with explicit
`debug:      (unknown -- admin endpoint unreachable)` and
`chat-trace: (unknown -- admin endpoint unreachable)` lines so a
degraded gateway state (admin endpoint unmounted, port closed,
mid-restart) is visible at a glance instead of leaving the flag lines
blank. Used `[ConsoleColor]::DarkYellow` to distinguish from the live
health rows.

### WR-08: force-exit leaks Run goroutine blocked in srv.Shutdown

**Files modified:** `internal/server/server.go`
**Commit:** `0d8ca72`
**Applied fix:** Added a `forceCloseCh chan struct{}` field on
`Server`. The two-stage signal handler's force-exit arm closes
`forceCloseCh` (in addition to `shutdownCh`); `Run()` now wraps
`srv.Shutdown` in a goroutine and selects on
`shutdownErrCh` vs `forceCloseCh`. On force-close it calls
`srv.Close()` which terminates in-flight requests immediately, then
drains `shutdownErrCh` so the Shutdown goroutine exits cleanly. The
`RunUntilSignal` force arm also awaits `runErrCh` after closing
`forceCloseCh` so the Run goroutine is drained before
`RunUntilSignal` returns — closing the goroutine-leak path that was
previously masked only by `main.go`'s `os.Exit(1)`.

## Skipped Issues

None — all 10 in-scope findings (2 Critical / BLOCKER + 8 Warning) were
fixed and committed. Info findings (IN-01..IN-05) were out of scope
per `fix_scope: critical_warning`; review them separately if a future
phase elevates them.

## Verification

- `make build` passes (commit-pinned binary at
  `bin/otto-gateway`, version `v2.0.13-82-ge63e7bf`).
- `make test` passes across all 18 test packages.
- Per-package focused tests run during each fix:
  - `go test ./internal/pool/` — all CR-01, WR-01, WR-03, WR-04 paths.
  - `go test ./internal/server/ -run TestRegression_REL_HTTP_01` — WR-05.
  - `go test ./cmd/otto-tray/ -run TestRegression_REL_TRAY_01` — WR-06.
  - `go build ./...` after each handler-package edit (CR-02 variants).

---

_Fixed: 2026-06-11_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
