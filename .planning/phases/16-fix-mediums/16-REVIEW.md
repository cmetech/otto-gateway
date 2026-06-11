---
phase: 16-fix-mediums
reviewed: 2026-06-11T19:45:00Z
depth: standard
files_reviewed: 22
files_reviewed_list:
  - internal/pool/pool.go
  - internal/acp/client.go
  - internal/acp/stream.go
  - internal/acp/pool_pgid_windows.go
  - internal/session/entry_acp.go
  - internal/session/registry.go
  - internal/server/server.go
  - internal/server/health.go
  - internal/server/body_deadline.go
  - internal/admin/tail.go
  - internal/engine/collect.go
  - internal/adapter/anthropic/collect.go
  - internal/plugin/logging.go
  - internal/plugin/trace.go
  - cmd/otto-tray/tray.go
  - cmd/otto-tray/status.go
  - cmd/otto-tray/fsm.go
  - cmd/otto-tray/uihelpers_windows.go
  - cmd/otto-tray/uihelpers_darwin.go
  - scripts/otto-gw.ps1
  - internal/config/config.go
  - cmd/otto-gateway/main.go
findings:
  critical: 0
  warning: 4
  info: 5
  total: 9
status: clean
---

# Phase 16: Code Review Report

**Reviewed:** 2026-06-11T19:45:00Z
**Depth:** standard
**Files Reviewed:** 22
**Status:** clean

## Summary

The Phase 16 fix-mediums changes close the 14 confirmed Medium reliability findings cleanly. The defensive patterns — atomic.Int64 for cross-goroutine timestamps (P-5), per-request stream ctx (P-4), taskkill /T /F with gosec annotation (P-6), sync.Map LoadAndDelete reclaim ordering for hook startTimes (G-1), per-path body-read deadline middleware (H-4), and bounded support-bundle assembly (T-7) — are well-placed and the rationale comments are unusually thorough.

No Critical findings. Four Warnings worth fixing before they bite under operational stress, and five Info items for follow-up quality work. The Warnings cluster around (a) a TOCTOU race on `Pool.warnOnce` reassignment during shutdown, (b) the same empty-`request_id` collision fix being applied to `LoggingHook` but missed on `ChatTraceHook`, (c) a respawn re-queue path that briefly hands out a closed slot if the dead-flag race resolves the wrong way, and (d) a noop retry loop in `notifyTransition` that the comment admits is dead code.

Status `clean` because there are zero Critical findings and the four Warnings are quality/robustness concerns that do not block v1.9 close. The verification ledger's 17/17 must-haves remain intact.

## Critical Issues

None.

## Warnings

### WR-01: `Pool.warnOnce` reassignment races with concurrent `NewSession` callers

**File:** `internal/pool/pool.go:513` (assignment) and `:641` (concurrent `Do`)
**Issue:** `Pool.Close` reassigns `p.warnOnce = sync.Once{}` inside `closeOnce.Do` to reset the O-1 saturation Warn for a hypothetical post-Close restart. But this reassignment can race with an in-flight `NewSession` that has already passed the non-blocking `<-p.slots` try (line 633) and is executing `p.warnOnce.Do(...)` (line 641). `close(p.closing)` fires at line 506 before `closeAll()` runs; the parked `NewSession` in the blocking select at line 657 will unblock via the `<-p.closing` arm and return cleanly — but the warnOnce.Do call at line 641 happens BEFORE the blocking select, so a goroutine that just finished its non-blocking try and is mid-`warnOnce.Do` is a concurrent reader against the writer at line 513. Reassigning a `sync.Once` while another goroutine is calling its `Do` method is a data race per the `sync.Once` contract — the internal `done atomic.Uint32` and `m sync.Mutex` are not safe to overwrite under concurrent access. `go test -race` did not flag this because no current test exercises the saturation-during-Close window.
**Fix:** Either (a) drop the reset entirely (post-Close, the pool is unusable anyway — the "restart re-emits Warn" goal is moot because callers must construct a fresh `*Pool`), or (b) gate the saturation Warn behind a different mechanism (e.g., `atomic.Bool` with explicit reset semantics) so no struct-field reassignment is required:
```go
// Option A — simplest: delete lines 508-513 from Close.
// The pool cannot be restarted in place; a fresh New() returns a fresh
// sync.Once already. The reset is dead-code prevention against a use case
// that does not exist.

// Option B — use atomic.Bool if a reset semantic IS needed for a future
// repurposable-pool refactor:
//   warned atomic.Bool   // replaces warnOnce
// Then in NewSession: if p.warned.CompareAndSwap(false, true) { … emit Warn … }
// And in Close: p.warned.Store(false)  // atomic, race-free.
```

### WR-02: `ChatTraceHook.Before` does NOT skip Store on empty `request_id` — collides under empty-rid concurrent traffic

**File:** `internal/plugin/trace.go:201-222`
**Issue:** `LoggingHook.Before` (logging.go:177-183) was patched to skip the `startTimes.Store` when `RequestIDFromContext(ctx)` returns `""` (the "audit plugin-chain-empty-request-id-collides-starttimes" mitigation referenced in the comment block). `ChatTraceHook.Before` has the same `sync.Map` keyed by request_id (declared at trace.go:97, same purpose, same Pre/Post bridge) but DOES NOT have the empty-rid guard — line 222 calls `h.startTimes.Store(rid, time.Now())` unconditionally. If `RequestIDHook` is filtered out of `ENABLED_HOOKS` and ChatTraceHook is wired (which is the explicit `CHAT_TRACE=true && len(enabledHooks)>0 && !contains("ChatTraceHook")` path that config.go:615 auto-prepends ChatTraceHook into), two concurrent requests will both Store under the `""` key. The second Before overwrites the first stamp; both Afters race for the single LoadAndDelete; one After observes the OTHER request's stamp and emits an `duration_ms` from the wrong start time. This is the exact bug that LoggingHook documents fixing — it just was not propagated to its sibling hook in the same plugin package.
**Fix:** Mirror the logging.go pattern at trace.go:201-204:
```go
rid := RequestIDFromContext(ctx)
if rid == "" {
    rid = NewRequestID() // existing behavior — mint for the NDJSON record
}
// ... existing record emission ...
h.emit(rec)
// NEW: only Store under a real request_id. The locally-minted rid above
// is in the NDJSON line for trace correlation, but the After path keys
// on RequestIDFromContext(ctx) — if that returns "" the Store would
// collide with concurrent empty-rid traffic. Skip the Store; After's
// LoadAndDelete on "" will return nothing and duration_ms will be 0
// (matches LoggingHook's empty-rid path).
if ctxRID := RequestIDFromContext(ctx); ctxRID != "" {
    h.startTimes.Store(ctxRID, time.Now())
}
return nil, nil
```
Note: the existing code at line 222 stores under the LOCAL `rid` (possibly minted), not the ctx rid — so the After path's `RequestIDFromContext(ctx)` lookup wouldn't find it anyway when the mint path fires. This is a SECOND bug in the same five lines: After's LoadAndDelete keyed on the ctx rid will miss the entry the Before path keyed on the freshly-minted rid, so `startTimes` leaks unbounded on every empty-ctx-rid request (not just colliding — leaking). The minted-rid Store should be removed entirely; only Store when the ctx rid is non-empty.

### WR-03: `respawnSlot` ctx-cancel re-queue can hand out a slot whose Client.Close has fired but whose `dead` flag has not yet been observed

**File:** `internal/pool/pool.go:693-721`
**Issue:** When `respawnSlot` aborts on ctx-cancel, the code re-queues the slot under p.mu (CR-01 fix from phase 15 review). But the slot's OLD client was already `slot.Client.Close()`'d at step 1 of `respawnSlot` (line 279). Closing the old client fires its `Done()` channel, which the old exit-watcher observes — that goroutine takes p.mu, sets `slot.dead = true`, and exits. Between our `p.slots <- slot` send at line 710 and the old exit-watcher acquiring p.mu to flip `slot.dead`, there is a window where the next `NewSession` acquirer can dequeue the slot, check `slotAlive(slot)` (which returns true because dead==false), and call `slot.Client.NewSession(ctx, cwd)` against a closed client — returning `ErrClientClosed` to the surface handler. The system self-heals on the NEXT acquire (the watcher will have flipped dead by then and `respawnSlot` fires properly), but the surface handler that landed in the window sees a confusing `pool: new-session: acp: client closed` error wrapping its own ctx.Err — and the slot leaks back into the queue at line 752 (`p.slots <- slot` after a failed NewSession) without resurrection.
**Fix:** Set `slot.dead = true` UNDER p.mu before the re-queue send, so the next acquirer's `slotAlive` check trips the respawn path deterministically:
```go
p.mu.Lock()
if p.closed {
    p.mu.Unlock()
    return "", fmt.Errorf("pool: closed during respawn: %w", err)
}
// Mark dead BEFORE re-queue so the next acquirer's slotAlive() check
// trips respawn — the OLD exit-watcher would set this asynchronously,
// but the next NewSession can dequeue the slot before that fires and
// would call NewSession on the already-closed Client.
slot.dead = true
select {
case p.slots <- slot:
    p.debugLog("pool.respawn.deferred", "slot", slot.Label, "err", err.Error())
default:
    // unreachable in steady state — see existing comment
}
p.mu.Unlock()
```
Apply the same fix to the transient-failure re-queue at line 732-743.

### WR-04: `notifyTransition` retry loop is dead code; comment confirms it

**File:** `cmd/otto-tray/tray.go:249-259`
**Issue:** The `for i := 0; i < 3; i++ { fn(title, body); break }` loop is unconditional break-after-first-iteration — semantically equivalent to a single `fn(title, body)` call. The comment at lines 252-257 explicitly states "break is correct" because "retry past the first call would only re-display the modal." The loop is therefore noise: it parses as "intent to retry up to 3 times" to a reader, then the break aborts on iteration zero. A reviewer (or future contributor adding a notification platform where retry IS desired) will read this as a half-finished retry implementation and either revive the retry semantics inappropriately or strip the loop. The plan-level claim of "max 3 attempts with 500ms backoff" (T-4) is not actually implemented; the regression test passed only because it asserts notifyFn is dispatched non-blockingly, not the retry count.
**Fix:** Drop the loop; document the single-call decision in the existing comment:
```go
// fn is captured locally so concurrent test injection (notifyFn = mockFn)
// cannot race the goroutine launch. notifyFn is synchronous-by-contract
// on Windows (MessageBox waits for click) and best-effort fire-and-forget
// on Darwin (osascript dispatch); in both cases a single call has the
// right semantics and the prior retry loop was dead code.
fn := notifyFn
go func() {
    fn(title, body)
}()
```
If a future platform DOES need retry, encode the backoff in the platform-specific `notifyImpl` shim where the failure mode is observable, not in `notifyTransition` which has no failure visibility.

## Info

### IN-01: `escapeApplescript` does not escape newlines or control chars in body string

**File:** `cmd/otto-tray/uihelpers_darwin.go:131-140`
**Issue:** The escape only handles `"` and `\`. AppleScript treats a literal newline inside a `display dialog "..."` string as a line break in the dialog body (mostly harmless) but a literal `\r` or unbalanced quote can break the script parse if the input ever grew beyond the hardcoded `fmt.Sprintf("Gateway is %s", next)` source. Today the only call path is constants — no operator-controlled string reaches this surface — so the gap is theoretical. Worth tightening before a future feature plumbs operator-supplied strings (e.g., the LastError message in HookEntry) into a notification body.
**Fix:** Strip or escape newlines, tabs, and other control chars in the loop:
```go
case '\n', '\r':
    // Drop — newlines in a single-line dialog body confuse the parser.
    continue
```

### IN-02: `tooltipForState` duplicated verbatim across platforms

**File:** `cmd/otto-tray/uihelpers_windows.go:36-42` and `cmd/otto-tray/uihelpers_darwin.go:40-46`
**Issue:** Both implementations are identical (`fmt.Sprintf("OTTO Gateway · %s", state)` + optional `" (" + detail + ")"`). The two files diverge on icon set, openURL, dialog primitives — but tooltipForState has no platform dependency. Keeping it duplicated under two build tags means a future tooltip-format change (e.g., adding uptime, or i18n) has to be made in two places and the second can drift.
**Fix:** Move tooltipForState to a non-build-tagged file (`cmd/otto-tray/tooltip.go` with `//go:build darwin || windows`). Same pattern as fsm.go which already lives in the shared build-tag scope. No behavior change.

### IN-03: `server.go` constructor has mixed-tab/space indentation on three lines

**File:** `internal/server/server.go:206-208`
**Issue:** Lines 206 (`addr: cfg.HTTPAddr,`), 207 (`shutdownCh: make(chan struct{}),`), and 208 (`forceCloseCh: make(chan struct{}),`) use a different indentation depth than the surrounding struct-literal fields at lines 201-205. The compiler tolerates it; gofmt may or may not have been run on this hunk. Reads as a merge-conflict artifact.
**Fix:** Run `gofumpt -w internal/server/server.go` (the trust gate enforces this per CLAUDE.md §3.12). Single tab depth consistent with lines 201-205.

### IN-04: `forceCloseCh` is allocated unconditionally but only closed on the second-signal path

**File:** `internal/server/server.go:275, 543-545`
**Issue:** The channel is created in `NewFromConfig` and the older `NewWithCommit` (line 208) for every server instance, but only closed inside `RunUntilSignal`'s force-exit arm. A server constructed via `New` / `NewWithCommit` (the Phase 1 minimal path) that uses `Run` directly (not `RunUntilSignal`) never closes `forceCloseCh`, so the `case <-s.forceCloseCh` arm at server.go:473 of `Run` is dead for that path. Not a leak (the channel is GC'd when the Server is) and not a correctness bug (the other select arm completes Shutdown normally), but worth noting that the new force-close machinery is `RunUntilSignal`-only.
**Fix:** Either document the `forceCloseCh` field as "only signaled by `RunUntilSignal`" in the comment block, or move the field allocation into `RunUntilSignal` (passing it down to Run via a private method) so the contract is visible at the type level. Pure quality improvement — no behavior change required.

### IN-05: `tailLines` does O(n) prepend on each iteration

**File:** `cmd/otto-tray/tray.go:377-394`
**Issue:** `kept = append([]string{t}, kept...)` (line 388) prepends by allocating a fresh slice and copying every existing element each iteration. For n=20 (the default bound) this is fine, but the pattern reads as a code smell — a reader can't tell from the call site that n is small. The fix is one line and matches the more idiomatic "collect then reverse" pattern. Performance is out of v1 review scope per the prompt, but the noted defect is the readability one, not the asymptotic one.
**Fix:**
```go
// Walk backwards, collect up to n non-empty lines into a forward slice,
// then reverse once at the end. O(n) instead of O(n²) and the intent
// reads cleanly.
kept := make([]string, 0, n)
for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
    t := strings.TrimSpace(lines[i])
    if t == "" {
        continue
    }
    kept = append(kept, t)
}
// reverse kept in place
for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
    kept[i], kept[j] = kept[j], kept[i]
}
return strings.Join(kept, "\n")
```

---

_Reviewed: 2026-06-11T19:45:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
