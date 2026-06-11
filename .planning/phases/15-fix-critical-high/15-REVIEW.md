---
phase: 15-fix-critical-high
reviewed: 2026-06-11T00:00:00Z
depth: standard
files_reviewed: 34
files_reviewed_list:
  - cmd/otto-gateway/main.go
  - cmd/otto-tray/icon/icon_darwin.go
  - cmd/otto-tray/icon/icon_windows.go
  - cmd/otto-tray/pidfile_darwin.go
  - cmd/otto-tray/pidfile_windows.go
  - cmd/otto-tray/regression_rel_tray_01_test.go
  - cmd/otto-tray/regression_rel_tray_02_test.go
  - cmd/otto-tray/regression_rel_tray_03_test.go
  - cmd/otto-tray/tray.go
  - cmd/otto-tray/uihelpers_darwin.go
  - cmd/otto-tray/uihelpers_windows.go
  - internal/acp/client.go
  - internal/adapter/anthropic/handlers.go
  - internal/adapter/anthropic/sse.go
  - internal/adapter/ollama/handlers.go
  - internal/adapter/ollama/ndjson_test.go
  - internal/adapter/ollama/ndjson.go
  - internal/adapter/ollama/regression_rel_http_03_test.go
  - internal/adapter/openai/handlers.go
  - internal/adapter/openai/regression_rel_http_02_test.go
  - internal/adapter/openai/regression_rel_http_03_test.go
  - internal/adapter/openai/sse.go
  - internal/admin/admin.go
  - internal/admin/sse_test.go
  - internal/admin/sse.go
  - internal/pool/config.go
  - internal/pool/pool_test.go
  - internal/pool/pool.go
  - internal/pool/regression_rel_pool_01_test.go
  - internal/pool/regression_rel_pool_02_test.go
  - internal/pool/regression_rel_pool_03_test.go
  - internal/server/regression_rel_http_01_test.go
  - internal/server/server.go
  - scripts/otto-gw.ps1
findings:
  critical: 2
  warning: 8
  info: 5
  total: 15
status: issues_found
---

# Phase 15: Code Review Report

**Reviewed:** 2026-06-11
**Depth:** standard
**Files Reviewed:** 34
**Status:** issues_found

## Summary

Phase 15 is a reliability-hardening phase covering three plans: pool concurrency
(REL-POOL-01/02/03), HTTP streaming lifecycle (REL-HTTP-01/02/03), and tray
identity verification (REL-TRAY-01/02/03). The implementation is largely sound,
with thoughtful comments documenting the load-bearing invariants, but I found
defects in three load-bearing areas:

1. A **pool deadlock on closed channel** when `respawnSlot` re-queue runs into
   `Close()` racing it — `p.slots <- slot` will panic ("send on closed channel")
   because `closeAll` does not close `p.slots`, and the existing `<-p.closing`
   select arm is read-not-write. Actually `p.slots` is never closed, so the
   panic risk is different: `slot.Client.Close()` from `closeAll` may run on a
   slot that was simultaneously re-queued for transient-respawn retry, and
   the next acquirer will pick a dead slot.

2. A **slot orphaning** race on the WR-07 transient re-queue path when
   `closeAll` has already moved `p.all = nil`. The slot is re-queued into
   `p.slots`, but `closeAll`'s slice snapshot has already been taken without
   it (or it was on `p.all` but its new replacement client was never spawned,
   so it never had a Client to close).

3. **Mid-stream worker-death terminal frames bypass PostHooks** for partial
   aggregated state on the Ollama/OpenAI surface. In `finalizeSSE` /
   `finalizeNDJSON` the `rerr != nil` path returns an `emptyResp` built from
   the **post-aggregation** state — but the aggregator state already has
   `aggregatedText` populated from earlier chunks. This is OK, but the
   variable name `emptyResp` is misleading; more importantly, the synthetic
   PII decrypt sweep will run on `aggregatedText` that was captured BEFORE
   the worker died, so the response may end up with partially-decrypted
   prose and an `upstream_disconnect` error frame side-by-side.

In addition there are several smaller defects (race on `app.cfg`/`PII`
fallthrough in `main.go`, unnecessary leak of registry on partial pool warmup
abort, and one tray test that asserts identity behaviour that doesn't fully
exercise the spec). Detailed findings below.

## Critical Issues

### CR-01: respawn-failure re-queue can race with `Close()` and panic on send

**File:** `internal/pool/pool.go:570-590`
**Issue:** Both the WR-07 ctx-cancel and transient-failure branches re-queue
the slot into `p.slots` with the pattern:

```go
select {
case p.slots <- slot:
    p.debugLog("pool.respawn.deferred", ...)
case <-p.closing:
    // Pool shutting down — Close drains p.all itself.
}
```

The select cures the case where `p.closing` is closed first. But there is no
synchronisation between `p.closing` close and the channel send: if `Close()`
fires `close(p.closing)` in another goroutine **between** the
`case p.slots <- slot` arm being selected by Go's runtime and the send actually
taking place, the channel `p.slots` is open (no one closes it) so no panic —
**but the slot is now re-queued into `p.slots` while `closeAll` has already
set `p.all = nil` and called `slot.Client.Close()`**.

The next time a caller calls `NewSession`, the `case slot = <-p.slots` arm
returns this dead slot. `slotAlive(slot)` reads `slot.dead` — which is
**still false** because `respawnSlot` returned an error before the WR-01
step-4 write set `slot.dead = false` (it was already false before the
respawn attempt: the dead-slot detection branch only entered because
`!p.slotAlive(slot)` was true initially; but then `respawnSlot` *does*
acquire `p.mu` and write `slot.dead = false` only on success — on failure
`slot.dead` remains true). So `slotAlive` returns false, the caller enters
the respawn path again, calls `slot.Client.Close()` again (idempotent
through `sync.Once` in acp.Client — OK), and then calls
`p.cfg.Factory.Spawn(ctx, ...)`. After Close, the factory still spawns
a new subprocess — but **the pool is closed** and `Initialize/NewSession`
will then send on `clientCtx.Done()` paths that nobody is draining.

The fix: in both re-queue branches, check `p.closing` BEFORE sending,
or drop the re-queue entirely once `p.closing` is closed. Today the
race window is small (one runtime scheduling tick between select arm
selection and send) but real, and shutdown timing is the hottest place
for race-condition surprises.

**Fix:**
```go
// Re-queue under p.mu so close-then-requeue is serialised.
p.mu.Lock()
if p.closed {
    p.mu.Unlock()
    return "", fmt.Errorf("pool: closed during respawn")
}
// Use a non-blocking send: if no consumer is parked, slot is dropped
// (callers will re-acquire any remaining alive slot).
select {
case p.slots <- slot:
default:
}
p.mu.Unlock()
```

Or — simpler and safer — record the slot as "needs re-queue" under `p.mu`
and let `Close()` see it via `p.all` (since the slot is still in `p.all`
on the failure path per WR-07).

### CR-02: REL-HTTP-02 fix removed `StopWatchdog()` on idle-timeout but watchdog only fires `Cancel` via the deferred `cancelFn` — and the deferred `cancelFn` does **not** trigger the watchdog AfterFunc immediately

**File:** `internal/adapter/openai/sse.go:461-481` (idle-timeout arm in `runSSEEmitter`)
**Issue:** The H-2 fix-comment claims:

> Let the deferred cancelFn (set by the handler on handler return) trigger
> the watchdog AfterFunc naturally so Cancel fires.

But `runSSEEmitter` is called as `runSSEEmitter(streamCtx, w, runHandle, ...)`
where `streamCtx` was derived from `ctx` and the handler calls
`defer cancelFn()` (handlers.go:151). When the idle-timeout arm fires:

1. `runSSEEmitter` returns its error.
2. The handler logs at debug and falls through to PostHooks.
3. The handler returns.
4. `defer cancelFn()` fires, which cancels `streamCtx`.
5. The watchdog's `context.AfterFunc` (registered on the engine's `runCtx`,
   which is `streamCtx` per engine.go) now fires → sends `session/cancel`.

This is **correct in the happy path**, but on the idle-timeout path the
worker is still generating. Between steps 1 and 5 (typically <1ms but
unbounded if the runtime is contended or if PostHooks block), the worker
is uncancelled. More importantly, the pool slot is **not released** until
the underlying `*engine.Run`'s `StopWatchdog()` *return value* actually
fires `Cancel` on the ACPClient (which is `*pool.Pool`), and Cancel is what
calls `releaseSlotForSession(sid)`.

The current finalizeSSE codepath omits `StopWatchdog()` on the idle path —
but **the watchdog `AfterFunc` exists to send Cancel on ctx-cancel**, and
when handlers.go's `defer cancelFn()` fires it will trigger Cancel. So the
slot is released… **but only after the SSE handler has returned.** That
means a follow-up request that comes in microseconds later via the same
HTTP connection sees Busy=1 in /admin/api/snapshot for the duration of
PostHooks. Acceptable.

The actual defect: the comment says "the deferred cancelFn (set by the
handler on handler return) trigger the watchdog AfterFunc". This is true
for `streamCtx` cancellation, but PostHooks run **before** `cancelFn`
fires (PostHooks call `eng.RunPostHooks(streamCtx, req, resp)` —
`streamCtx` is still alive). If PostHook PII-decrypt takes >1s on a large
buffered response, the worker continues generating for >1s — and on a
1-slot pool the next request *waits* in NewSession's bounded acquire
for the full PostHook latency, then times out with `ErrPoolExhausted`.

**Pre-existing risk** (also affects the writeError path), but the fix
should be: call `StopWatchdog()` only *after* PostHooks complete, OR
invoke Cancel directly in the idle-timeout arm and accept that
StopWatchdog will see the AfterFunc fired (which is idempotent because
ACPClient.Cancel is itself idempotent).

**Fix:** Either (a) invoke `cancelFn()` explicitly before the return in
the idle-timeout arm so Cancel fires before PostHooks see Engine doing
anything; or (b) keep `StopWatchdog()` AND explicitly issue
`engineForAdapter.Cancel(sid)` in the idle arm — the comment says the
old code path "suppressed Cancel" but actually Cancel was just deferred
to handler exit. Document the trade-off and pick one.

## Warnings

### WR-01: `closeAll` in-flight Cancel iterates `p.sessionSlots` AFTER `p.all = nil` — but `sessionSlots` still references the same slot pointers, so Cancel fires on slots whose Client field was just freed by an earlier Close on the same client

**File:** `internal/pool/pool.go:449-490`
**Issue:** Order is:
1. Snapshot `p.all` into `slots`
2. `p.all = nil`
3. Snapshot `p.sessionSlots` into `inflight`
4. Release `p.mu`
5. For each inflight: `e.client.Cancel(sid)`
6. For each slot in `slots`: `s.Client.Close()`

If a request finalises between (4) and (5), its release path calls
`releaseSlotForSession` which takes `p.mu`, deletes `sessionSlots[sid]`,
and tries `p.slots <- slot`. The `p.slots <- slot` blocks (`p.slots` is
not closed). Meanwhile (6) calls `slot.Client.Close()` which cancels
`clientCtx` — and Cancel's `sendNotification` in (5) selects on
`c.clientCtx.Done()` to drop the notification. So (5) can land
**after** the slot.Client has been closed, in which case the cancel is
silently dropped. The REL-POOL-02 regression test asserts at least 2
cancels were issued — but with the size-2 pool both slots' clients are
closed AFTER cancel-send-attempt, so this works. On a 16-slot pool with
unfortunate scheduling, some cancels may be dropped.

The post-fix REL-POOL-02 test asserts `cancelsAfter < 2` is a failure
— it doesn't assert all sessions were cancelled. Tighten the test to
assert all N sessions saw their Cancel notification.

**Fix:** Either send cancel BEFORE acquiring `p.mu` in (1), or insert a
small `time.Sleep` / WaitGroup so cancels drain before Close. The
"best-effort" comment in `closeAll` acknowledges this — but the WARN
log on dropped cancels would be a useful operator signal.

### WR-02: REL-HTTP-03 mid-stream worker-death emits an Ollama terminal frame using `aggregateOllamaResponse(req, state, canonical.StopUnknown)` for the post-hook input, but the variable is named `emptyResp` even though it carries the full `aggregatedText` accumulated from chunks that arrived BEFORE the worker died

**File:** `internal/adapter/ollama/ndjson.go:571` and `internal/adapter/openai/sse.go:586`
**Issue:** Misleading variable name (`emptyResp` for a non-empty response) and
a subtle correctness gap: `aggregatedText` may contain partially-encrypted
PII (the prefix of the response before the worker died). The PII decrypt
PostHook then runs on truncated ciphertext, which may decrypt to garbage
or trigger a decrypt-error. The error envelope already says
"upstream_disconnect", so this is observable, but the log line will show
"PostHook PII decrypt: invalid ciphertext" without correlation.

**Fix:** Rename `emptyResp` to `partialResp` and add a TODO to guard the
PII decrypt hook against truncated ciphertext (probably already does this
defensively; verify).

### WR-03: `respawnSlot` step 4 sets `slot.dead = false` BEFORE step 5 spawns the exit-watcher — a death between step 4 unlock and step 5 spawn is unobservable

**File:** `internal/pool/pool.go:288-295`
**Issue:** The respawn sequence:
```go
p.mu.Lock()
slot.Client = newClient
slot.dead = false
newDone := newClient.Done()
p.mu.Unlock()
p.startExitWatcher(slot, newDone)
```

If `newClient` dies between `p.mu.Unlock()` and `p.startExitWatcher`,
no one is listening on `newDone`. The next `NewSession` reads
`!slot.dead` as true (alive), hands the slot out, and the caller's
`slot.Client.NewSession(ctx, cwd)` returns `ErrClientClosed`. The
calling code in `NewSession` (line 595) catches the error and releases
the slot, but the slot still has `dead=false` and the next caller hits
the same trap.

**Fix:** Reorder so `startExitWatcher` runs UNDER `p.mu` (acceptable
because exit watcher just listens on a channel and doesn't take p.mu
in its hot path). Or add a tighter loop: when `slot.Client.NewSession`
returns `ErrClientClosed`, set `slot.dead = true` explicitly under
`p.mu` so the next caller respawns instead of repeating the failure.

### WR-04: `TestRegression_REL_POOL_02_CtrlCOrphansChildren` only asserts `cancelsAfter >= 2` — passes even if cancels race-drop on a busy goroutine schedule

**File:** `internal/pool/regression_rel_pool_02_test.go:137-146`
**Issue:** Test creates 2 in-flight sessions and asserts at least 2 cancels
were issued. With 2 sessions on 2 fakeClients, the assertion is exact
parity — but the test could be made stricter: assert each fakeClient saw
EXACTLY ONE Cancel for its session_id. As written, a bug where one
client saw both cancels and the other saw none would pass.

**Fix:**
```go
if len(bc0.cancelCallList()) == 0 || len(bc1.cancelCallList()) == 0 {
    t.Fatalf("expected each fake client to receive Cancel; bc0=%v bc1=%v",
        bc0.cancelCallList(), bc1.cancelCallList())
}
```

### WR-05: REL-HTTP-01 test assertion has a 1-second tolerance that is too generous to detect partial regressions

**File:** `internal/server/regression_rel_http_01_test.go:152-156`
**Issue:** The assertion is `elapsed >= 1*time.Second` is a failure. The pre-fix
behaviour was 30s; the post-fix expectation is <1s. But realistic ms-level
shutdown should be under 50ms (the test reads "Shutdown completed in %v").
If a future regression makes Shutdown take 800ms (e.g., the SSE handler
spawns a 750ms-blocking PostHook drain), the test still passes. A tighter
bound (100ms or 250ms) would catch partial regressions.

**Fix:** Tighten to `>= 250*time.Millisecond` or measure the time the SSE
handler took to exit and assert it separately from total Shutdown.

### WR-06: Tray-01 regression test asserts only the `verifyGatewayIdentity` reject path on darwin/windows but the test binary's `comm` actually includes a `.test` suffix on darwin so the suffix check works trivially

**File:** `cmd/otto-tray/regression_rel_tray_01_test.go:51`
**Issue:** On darwin, `ps -o comm=` returns the binary path basename of the
running test binary, which is `otto-tray.test`. The check is
`strings.HasSuffix(comm, "otto-gateway") || comm == "otto-gateway"`, so
the test binary `otto-tray.test` is rejected (good). But what about
`my-otto-gateway-fork`? Or a symlinked `otto-gateway`? The
HasSuffix check accepts `whatever-otto-gateway` as a valid gateway
identity. A malicious operator running a binary named
`fake-otto-gateway` from `/tmp` and writing its PID into the pidfile
would pass identity check.

Pidfile path is install-root-local (`.otto/gw/otto-gateway.pid`), so the
attack surface is "operator can plant arbitrary binary at install path"
— but operators with that capability can do anything already. Acceptable.
The fix would be to compare against the full expected binary path
(install_root + "/bin/otto-gateway") from `verifyGatewayIdentity`'s
`expectedPath` parameter — but the parameter is currently `_` (ignored)
on both darwin and windows. The test passes `"/any/path/otto-gateway"`
indicating the parameter was originally intended to carry the path —
unused now.

**Fix:** Either use the `expectedPath` parameter to compare full paths,
or rename it to `_string` if the path-check is intentionally not in v1.
Also: extend the test to cover the `HasSuffix` ambiguity (e.g., a process
named `not-otto-gateway` should be rejected, not accepted).

### WR-07: `Get-GatewayStatus` in `otto-gw.ps1` swallows admin snapshot errors silently — `chat_trace` and `debug` lines simply vanish when the admin endpoint is unreachable, but the operator may not realise these flags are unknown

**File:** `scripts/otto-gw.ps1:626-635`
**Issue:** The try/catch around `Invoke-RestMethod "$HealthUrl/admin/api/snapshot"`
catches every exception and prints nothing. Operators running `otto-gw status`
on a degraded gateway (admin unmounted, port closed, etc.) get partial
output without any indication that admin probing failed. Minor UX
defect, not a correctness bug.

**Fix:** On catch, print `  debug:      (unknown — admin endpoint unreachable)`
so the operator knows the data is missing rather than absent.

### WR-08: Two-stage signal handler in `RunUntilSignal` races between `cancel()` and `srv.Shutdown` completion — second-SIGINT path closes `s.shutdownCh` again, but the deferred `cancel()` may race the explicit close on `s.shutdownCh`

**File:** `internal/server/server.go:430-478`
**Issue:** When second Ctrl-C arrives:
1. Goroutine sends `forceErrCh <- errors.New("force-exit: second signal")`.
2. RunUntilSignal receives `forceErrCh`, enters the `case err := <-forceErrCh` arm.
3. Closes `s.shutdownCh` (idempotent via select-then-close).
4. Returns the error to main.

But `cancel()` was already called on the first signal (line 446); the
inner `s.Run(derivedCtx)` saw ctx.Done and called `srv.Shutdown(30s)`,
which fires the RegisterOnShutdown callback that ALSO tries to close
`s.shutdownCh`. The select-guard prevents the panic, but the
RegisterOnShutdown closure may still be running when the explicit
`close(s.shutdownCh)` in the force-exit arm fires. Both call sites use
the same select-then-close pattern, so this is race-safe.

The real concern: `s.Run` is still blocked inside `srv.Shutdown(30s)` —
the force-exit path returns to main without unblocking it. `s.Run`'s
goroutine leaks. `main.go:139` calls `os.Exit(1)` which kills it, but
on a clean shutdown path this is a goroutine leak observable via goleak.

**Fix:** Force-exit should call `srv.Close()` (not Shutdown) to terminate
in-flight requests immediately. The current path leaves Run() blocked on
Shutdown() and only os.Exit ends it.

## Info

### IN-01: Comment in `closeAll` is wrong about WR-06 idempotency

**File:** `internal/pool/pool.go:438-448`
**Issue:** The comment claims `closeAll` is idempotent because the first call
nils `p.all`. But the first call also nils `p.sessionSlots` — wait, it
doesn't. `p.sessionSlots` is never reset. A second call to `closeAll`
re-reads `p.sessionSlots` and re-issues Cancel notifications to
already-closed clients. Each `Cancel` is silently dropped by `sendNotification`
selecting on `clientCtx.Done()`, so no panic, but the second-call workload
is `O(len(sessionSlots))` instead of zero.

**Fix:** Either also `p.sessionSlots = nil` in closeAll (after copying for
inflight), or note in the comment that the second-call cost is negligible
because cancels short-circuit immediately.

### IN-02: Magic number `30 * time.Second` (warmupDeadline) is only documented in one place

**File:** `cmd/otto-gateway/main.go:62`
**Issue:** Acceptable design, but the rationale ("typical warmup is <1s") is
in a comment. If kiro-cli warmup takes longer due to model-catalog
populating, operators have no env knob to extend this. A future change
should make it configurable via `WARMUP_DEADLINE_SEC`.

**Fix:** Add a TODO referencing the env knob; not in scope for Phase 15.

### IN-03: `verifyGatewayIdentity` ignores the second parameter on both platforms

**File:** `cmd/otto-tray/pidfile_darwin.go:31` and `pidfile_windows.go:43`
**Issue:** Both functions accept `(pid int, _ string)` where the string is
unused. This is intentional per the comment ("expectedPath param kept
for future use") but linters may flag as dead code. Removing the parameter
breaks `verifyGatewayIdentity` calls if the parameter is removed inconsistently.

**Fix:** Either use the parameter (compare full path) or remove it from
the signature entirely. Half-implemented signatures are confusing.

### IN-04: `chunkCountUnderMu` in `acp/client.go` is called three times inside `awaitPromptResult` but the chunk count is read for logging only — under heavy mutex contention this could starve other readers

**File:** `internal/acp/client.go:963-967`
**Issue:** Negligible in practice but the read-under-mutex-just-to-log pattern
is fragile. If a future log handler is slow, the lock is held longer than
needed.

**Fix:** Use `atomic.Int64` for the chunk counter instead of mu-guarding it
through a method.

### IN-05: REL-POOL-03 regression test uses a `time.Sleep(time.Millisecond)` to create a "race window"

**File:** `internal/pool/regression_rel_pool_03_test.go:141`
**Issue:** Time-based test for a race condition is inherently flaky. The
1ms sleep was chosen to let A's awaitPromptResult fire its ctx.Done arm
before B's Prompt installs activeStream. On a heavily loaded CI machine,
this 1ms may not be enough, and the test could spuriously pass even when
the regression returns. The 20-iteration loop is the mitigation.

**Fix:** Replace the sleep with a `chan struct{}` synchronisation point that
A's awaitPromptResult signals when it has entered its select-loop. Time-
based tests for races should be a last resort.

---

_Reviewed: 2026-06-11_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
