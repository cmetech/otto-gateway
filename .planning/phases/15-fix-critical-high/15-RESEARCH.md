# Phase 15: Fix Critical + High — Research

**Researched:** 2026-06-11
**Domain:** Go concurrency, HTTP server lifecycle, PowerShell script control flow, macOS systray
**Confidence:** HIGH (all findings cross-referenced against live source)

---

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

- **D-01:** 3 parallel plans by subsystem (15-01 Pool/ACP, 15-02 HTTP surface, 15-03 Tray/wrapper).
- **D-01a:** Cross-plan boundary: if `internal/server/server.go` is edited by both P-2 (Plan 15-01) and H-1 (Plan 15-02), planner must either merge P-2+H-1 into the same plan OR declare `15-02 depends_on: ["15-01"]`.
- **D-03:** Each fix = atomic commit: production source change + t.Skip() removal + regression test green in same commit.
- **D-04:** New env var `POOL_ACQUIRE_TIMEOUT_MS` default 30000 for P-1 bounded acquire.
- **D-05:** P-2 shutdown grace REUSES existing 30s at server.go:377-381 — no new env var.
- **D-07/D-08:** P-1 surface-native 503 envelopes + Retry-After:5; internal sentinel `pool.ErrPoolExhausted`.
- **D-09/D-10:** H-3 surface-native terminal frames + WARN log (session_id, worker_pid, kiro_exit_code) — non-negotiable.
- **D-11/D-12/D-13:** T-3 applyState() extends to setIcon + SetTooltip on every FSM transition; same pattern on Windows.
- **D-15:** T-2 Get-GatewayStatus returns result object, no script-terminating exit 1.
- **D-16:** T-1 PID-identity check (process name matches "otto-gateway").

### Claude's Discretion

- Plan-internal task order: each plan starts with highest-severity finding.
- Worktree merge order: standard gsd-executor flow.
- No new dependencies — all 9 fixes achievable with stdlib + existing deps.
- Anthropic surface for H-3: fold in if same truncation path confirmed; document asymmetry if already correct.
- CLAUDE.md update: add POOL_ACQUIRE_TIMEOUT_MS to env-var list in phase-close commit.
- REQUIREMENTS.md: flip REL-POOL-01..03, REL-HTTP-01..03, REL-TRAY-01..03 to `fulfilled` at phase close.
- PII bracket shape: `[...]` not `<...>` for all log markers/test fixtures.

### Deferred Ideas (OUT OF SCOPE)

- terminal-notifier integration for macOS Notification Center
- Bouncing dock icon (NSApp requestUserAttention) on Error
- POOL_ACQUIRE_TIMEOUT_MS lower bound enforcement
- Per-surface error code dictionary in PROJECT.md
- Two-tier shutdown grace (5s soft + 25s tail)
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| REL-POOL-01 | Pool acquire bounded; no permanent slot removal on transient respawn failure; typed 503 | P-1 fix: re-queue slot on non-ctx error + select timeout arm + ErrPoolExhausted sentinel |
| REL-POOL-02 | Ctrl-C never orphans kiro-cli trees; deferred cleanup runs on all exit paths; 2nd SIGINT force-exits | P-2 fix: replace os.Exit(1) at main.go:131; cancel in-flight via BaseContext or RegisterOnShutdown; second-signal handler |
| REL-POOL-03 | Stale awaitPromptResult CAS-guarded; recycled slot cannot receive nil activeStream | P-3 fix: identity check before nil (`if c.activeStream == stream`) in both arms |
| REL-HTTP-01 | Graceful shutdown does not block 30s on open admin SSE; unwinds cleanly | H-1 fix: wire shutdown ctx into sseLoop via srv.RegisterOnShutdown or server.shutdownCh |
| REL-HTTP-02 | OpenAI idle-timeout and write-error paths issue explicit ACP Cancel before returning slot | H-2 fix: remove or invert StopWatchdog call on idleC branch; explicit Cancel before stop |
| REL-HTTP-03 | Mid-stream worker death emits surface-native terminal error frame + WARN log on all surfaces | H-3 fix: emit error frame + DONE in finalizeSSE, done:true error line in finalizeNDJSON |
| REL-TRAY-01 | Stop/Restart verifies PID identity (process name) before kill | T-1 fix: verifyGatewayIdentity() in bash stop, ps1 Stop-Gateway, tray makeProbe |
| REL-TRAY-02 | Windows support bundle completes when gateway is down | T-2 fix: refactor Get-GatewayStatus to return object, not exit 1 |
| REL-TRAY-03 | Gateway death visible via icon/tooltip on macOS (and Windows for parity) | T-3 fix: applyState calls setIcon + systray.SetTooltip on every FSM transition |
</phase_requirements>

---

## Summary

Phase 15 fixes exactly 9 confirmed findings (1 Critical, 8 High) across 3 subsystems. All regression test files were pre-written by Phase 14 with `t.Skip("REL-<ID>: regression test — unskip in Phase 15 fix commit")`. The planner's job is to write tasks that: (a) make the production fix, (b) remove the Skip, and (c) prove the test goes from red to green under `go test -race ./...`.

The single biggest planning risk is the **server.go overlap**: both P-2 (Plan 15-01) and H-1 (Plan 15-02) edit `internal/server/server.go` at adjacent functions (`RunUntilSignal`/`Run`). These edits are not independent — the shutdown context that H-1 needs propagated into SSE handlers is the same context that P-2 needs to cancel in-flight streams during grace. Separating them into two parallel plans touching the same file is a merge conflict waiting to happen. The safe choice is to put P-2 and H-1 in the same plan (collapse 15-01 and 15-02 for the server.go work), OR to serialize 15-02 `depends_on: ["15-01"]`.

The second cross-cutting risk is the **openai/sse.go overlap**: H-2 (idle-timeout/write-error cancel) and H-3 (mid-stream terminal frame) both edit `finalizeSSE` and `runSSEEmitter`. They are adjacent code paths in the same function, not the same lines — H-2 touches the `idleC` and `applyChunk` arms of the loop; H-3 touches `finalizeSSE`'s `rerr != nil` branch. They can be fixed sequentially in the same task or in sequential tasks within the same plan without conflict.

**Primary recommendation:** Collapse P-2 + H-1 into a single sub-task within Plan 15-01 (server.go shutdown plumbing), or put all server.go edits in Plan 15-01 and make Plan 15-02 depend on it. All other plan boundaries are clean.

---

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Slot acquire timeout + ErrPoolExhausted | Pool layer (`internal/pool/pool.go`) | Per-surface adapters (map error to 503) | Pool owns slot lifecycle; adapters own wire format |
| Graceful shutdown / in-flight cancel | HTTP server layer (`internal/server/server.go`) | Pool layer (`pool.Close`) | Server controls shutdown signalling; pool controls subprocess cleanup |
| ACP Cancel on idle-timeout | Per-surface adapter (`internal/adapter/openai/sse.go`) | Engine watchdog (`internal/engine/engine.go`) | Adapter is responsible for invoking Cancel before releasing slot |
| Mid-stream terminal error frames | Per-surface adapter (openai/sse.go, ollama/ndjson.go) | — | Adapter owns wire format; pool/engine have no HTTP writer |
| PID identity verification | Wrapper scripts (bash + ps1) + tray (`cmd/otto-tray/`) | — | PID-file trust is in the scripts/tray probe layer |
| Tray icon/tooltip state | Tray UI layer (`cmd/otto-tray/tray.go` + uihelpers_darwin.go) | — | systray API is only available on the main goroutine context owned by tray |

---

## Standard Stack

No new packages. All fixes use stdlib or packages already in go.mod.

| Package | Already in go.mod | Purpose in Phase 15 |
|---------|------------------|----------------------|
| `sync/atomic` | stdlib (Go 1.23+) | P-3 CAS guard — `sync/atomic.Pointer[T].CompareAndSwap` NOT used; mutex+identity check pattern used instead (see Architecture Patterns) |
| `os/signal` | stdlib | P-2 second-SIGINT handler |
| `context` | stdlib | P-2 `server.BaseContext` / `RegisterOnShutdown` cancel |
| `golang.org/x/sys/windows` | `go.mod` (indirect via systray) | T-1 Windows process name query via `QueryFullProcessImageNameW` |
| `github.com/energye/systray` | `go.mod` v1.0.3 | T-3 `systray.SetIcon` / `systray.SetTooltip` already in API |

**No `go.mod` changes are expected.** [VERIFIED: codebase grep of go.mod]

---

## Architecture Patterns

### System Architecture Diagram

```
[Ctrl-C / SIGINT]
       │
       ▼
RunUntilSignal (server.go)
       │ cancels derivedCtx
       ▼
  Run (server.go) ──── shutdownCtx 30s ────► srv.Shutdown()
       │                                            │
       │ (H-1 fix) BaseContext / RegisterOnShutdown │
       ▼                                            ▼
  sseLoop (admin/sse.go)              in-flight HTTP handlers
  selects on shutdownCh               ctx derived from BaseContext
  → exits cleanly                     → cancel propagates to streams
       │
       ▼
 main.go: deferred cleanup() runs pool.Close()
 (P-2 fix: os.Exit replaced with explicit cleanup + closeLogger)
       │
       ▼
 pool.Close() → Cancel() all in-flight sessions → SIGKILL kiro-cli pgroups
```

```
[NewSession acquire path — P-1 fix]
                        ┌─────────────────────────────────┐
select {                │                                 │
  case slot = <-p.slots │  ← slot available               │
  case <-ctx.Done()     │  ← caller cancelled             │
  case <-p.closing      │  ← pool shut down               │
  case <-time.After(T)  │  ← POOL_ACQUIRE_TIMEOUT_MS (new)│
    → return ErrPoolExhausted                             │
}                       └─────────────────────────────────┘
    ↓
respawnSlot fails with non-ctx error
    ↓ (P-1 fix: mirrors ctx-cancel re-queue at pool.go:525-532)
select {
  case p.slots <- slot  ← re-queue (pool recovers)
  case <-p.closing      ← pool shutting down
}
return error (caller sees 503 via ErrPoolExhausted or slot-respawn error)
```

### Recommended Project Structure (no new files except icons)

```
internal/pool/
├── pool.go              # P-1: acquire timeout arm + re-queue on non-ctx error
                         # P-1: ErrPoolExhausted sentinel (var declaration)
├── config.go            # P-1: AcquireTimeout field + POOL_ACQUIRE_TIMEOUT_MS parse
internal/acp/
└── client.go            # P-3: CAS guard at :868-870 and :894-896

cmd/otto-gateway/
└── main.go              # P-2: os.Exit(1) → explicit cleanup() + closeLogger() + os.Exit(1)

internal/server/
└── server.go            # P-2 + H-1: BaseContext/RegisterOnShutdown shutdown ctx wiring

internal/admin/
└── sse.go               # H-1: sseLoop selects on shutdown channel

internal/adapter/openai/
└── sse.go               # H-2: remove/reorder StopWatchdog on idleC and applyChunk paths
                         # H-3: finalizeSSE emits error frame + [DONE] + WARN log

internal/adapter/ollama/
└── ndjson.go            # H-3: finalizeNDJSON emits done:true + done_reason:error + WARN log

cmd/otto-tray/
├── tray.go              # T-1: makeProbe identity check; T-3: applyState calls setIcon+SetTooltip
├── uihelpers_darwin.go  # T-3: state-aware icon helpers (iconForState, tooltipForState)
├── uihelpers_windows.go # T-3: same icon/tooltip pattern on Windows
├── pidfile_darwin.go    # T-1: verifyGatewayIdentity(pid, binPath) using ps -p
├── pidfile_windows.go   # T-1: verifyGatewayIdentity(pid, binPath) via QueryFullProcessImageNameW
└── icon/
    ├── icon_darwin.go   # extend to embed template_running.png, template_warning.png, template_error.png
    ├── icon_windows.go  # extend to embed running.ico, warning.ico, error.ico
    ├── template_running.png  (new — green variant)
    ├── template_warning.png  (new — yellow variant)
    └── template_error.png    (new — red variant)

scripts/
├── otto-gw              # T-1: identity check before kill at :782-784; T-2: N/A (bash already correct)
└── otto-gw.ps1          # T-1: identity check before $proc.Kill() at :560-566
                         # T-2: Get-GatewayStatus → return [pscustomobject] not exit 1
```

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Bounded channel select with timeout | Custom timer loop | `select { case <-p.slots: ...; case <-time.After(T): }` | stdlib select is atomic; a loop introduces TOCTOU |
| Shutdown context propagation to handlers | Custom middleware | `srv.BaseContext` / `srv.RegisterOnShutdown` | stdlib http.Server already owns this surface |
| Windows process name from PID | Custom PE header reader | `windows.QueryFullProcessImageNameW` via `golang.org/x/sys/windows` (already in go.mod) | One syscall; cross-version compatible |
| macOS process name from PID | Custom procinfo syscall | `ps -p $pid -o comm=` (bash) or `proc.ProcessName()` via syscall sysctl | Simpler and already safe for our trust model |

---

## Detailed Findings: Code Shapes and Fix Patterns

### P-1: Pool Bounded Acquire + Re-queue on Transient Respawn Failure

**Current shape** (pool.go:490-535, VERIFIED from source read):

```go
// CURRENT — blocks indefinitely; removeSlot on non-ctx errors
func (p *Pool) NewSession(ctx context.Context, cwd string) (string, error) {
    var slot *Slot
    select {
    case slot = <-p.slots:
    case <-ctx.Done():
        return "", fmt.Errorf("pool: acquire cancelled: %w", ctx.Err())
    case <-p.closing:
        return "", errors.New("pool: closed")
    }
    // ... dead-slot detection ...
    if err := p.respawnSlot(ctx, slot); err != nil {
        if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
            // re-queue — correct
            select { case p.slots <- slot: ...; case <-p.closing: }
            return "", ...
        }
        p.removeSlot(slot)   // ← BUG: permanent shrink on transient error
        return "", ...
    }
```

**Fix pattern** (D-04, D-07, D-08):

```go
// AFTER — config.go adds:
// AcquireTimeout time.Duration  // from POOL_ACQUIRE_TIMEOUT_MS, default 30s

// pool.go: add to select
var timeoutC <-chan time.Time
if p.cfg.AcquireTimeout > 0 {
    timer := time.NewTimer(p.cfg.AcquireTimeout)
    defer timer.Stop()
    timeoutC = timer.C
}
select {
case slot = <-p.slots:
case <-ctx.Done():
    return "", fmt.Errorf("pool: acquire cancelled: %w", ctx.Err())
case <-p.closing:
    return "", errors.New("pool: closed")
case <-timeoutC:
    return "", ErrPoolExhausted   // ← new sentinel; adapters map to 503
}

// Respawn failure: re-queue instead of removeSlot for non-ctx errors
if err := p.respawnSlot(ctx, slot); err != nil {
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        select { case p.slots <- slot: ...; case <-p.closing: }
        return "", ...
    }
    // Non-ctx transient error: re-queue so pool can recover
    select {
    case p.slots <- slot:
        p.debugLog("pool.respawn.transient_requeue", "slot", slot.Label, "err", err.Error())
    case <-p.closing:
    }
    return "", fmt.Errorf("pool: respawn slot %s (transient, slot re-queued): %w", slot.Label, err)
}
```

**ErrPoolExhausted sentinel** (new in pool.go):
```go
var ErrPoolExhausted = errors.New("pool: all workers busy; retry in 5s")
```

**Per-surface 503 mapping** — adapters use `errors.Is(err, pool.ErrPoolExhausted)` to emit D-07 bodies + `Retry-After: 5`. The acquire-timeout error wraps ErrPoolExhausted so `errors.Is` traverses the chain.

**Test pre-assertion flip** (regression_rel_pool_01_test.go:115): Change `want 0` to `want 1` after removing `t.Skip`.

### P-2: os.Exit Replacement + In-Flight Cancel

**Current shape** (main.go:127-132, VERIFIED):

```go
defer cleanup()                         // line 127 — only runs on nil-return
if err := app.srv.RunUntilSignal(bootCtx); err != nil {
    logger.Error("server stopped with error", "err", err)
    os.Exit(1)                          // line 131 — skips defer cleanup()
}
```

**RunUntilSignal** (server.go:388-406, VERIFIED): single-signal handler. After first SIGINT cancels `derivedCtx`, the signal goroutine exits (`<-derivedCtx.Done()`) — a second SIGINT is not caught and hits the default OS disposition (terminate, which also bypasses pool.Close since the process dies).

**Fix pattern** (D-05):

Step 1 — replace os.Exit at main.go:131:
```go
if err := app.srv.RunUntilSignal(bootCtx); err != nil {
    logger.Error("server stopped with error", "err", err)
    cleanup()      // explicit: pool.Close + registry.Close + logger close
    closeLogger()
    os.Exit(1)
}
```

Step 2 — cancel in-flight streams during grace (server.go). Two approaches; pick the one that doesn't break existing tests:

Option A — `srv.BaseContext` (requires Go 1.13+, already satisfied; passes a cancel-able ctx to every new connection/request):
```go
shutdownCancel := func() {}
srv := &http.Server{
    BaseContext: func(_ net.Listener) context.Context {
        baseCtx, bc := context.WithCancel(context.Background())
        shutdownCancel = bc   // captured for use below
        _ = bc
        return baseCtx
    },
    ...
}
// When shutting down:
shutdownCancel()   // cancels all in-flight request contexts
srv.Shutdown(shutdownCtx)
```

Option B — `srv.RegisterOnShutdown` (simpler for SSE-specific case; see H-1 below):
```go
srv.RegisterOnShutdown(func() { shutdownCancel() })
```

**Note:** Option B is simpler and handles both P-2 and H-1 with one mechanism — strongly preferred. RegisterOnShutdown callbacks run at the beginning of Shutdown, before it waits for connections to drain. This lets SSE handlers select on a shutdown channel and exit voluntarily before the 30s deadline.

Step 3 — second SIGINT during grace window:
```go
// RunUntilSignal: after first signal, keep listening for second
go func() {
    select {
    case <-sigCh:
        s.logger.Info("second shutdown signal received; force-exiting")
        cancel()
        // Caller (main) will call cleanup() before os.Exit(130) — enforced
        // by the pattern in step 1 above.
        // Force-signal the main goroutine via a done channel:
        forceCh <- struct{}{}
    case <-derivedCtx.Done():
    }
}()
```

**server.go overlap with H-1 is REAL**: RegisterOnShutdown is the mechanism for both. The planner MUST put P-2 and H-1 in the same plan or serialize them. See Critical Finding below.

**Test assertion flip** (regression_rel_pool_02_test.go): The pre-fix assertion checks `cancelsBefore == 0`. The post-fix assertion (after removing t.Skip) should verify `cancelsAfter >= 2` (pool.Close was called).

### P-3: CAS Guard on activeStream Nil

**Current shape** (client.go:867-896, VERIFIED):

Both arms (`ctx.Done()` at :868-870 and `frame` at :894-896) unconditionally:
```go
c.streamMu.Lock()
c.activeStream = nil    // ← no identity check
c.streamMu.Unlock()
```

**Fix pattern** (D-08) — identity-guarded nil, NOT sync/atomic (mutex already in place):
```go
// In awaitPromptResult, both arms:
c.streamMu.Lock()
if c.activeStream == stream {   // ← only nil if WE are the current owner
    c.activeStream = nil
}
c.streamMu.Unlock()
```

`stream` is the parameter passed to `awaitPromptResult` — it's the specific `*Stream` this goroutine is managing. If B has already installed `streamB`, `c.activeStream != streamA`, so A's late goroutine is a no-op.

**Why mutex+identity, not atomic.Pointer.CompareAndSwap**: The existing field `c.activeStream` is typed `*Stream` and protected by `c.streamMu` (verified: both arms lock streamMu before write). Converting to `atomic.Pointer[Stream]` would require touching every read site under streamMu to use Load() — a larger refactor. The mutex identity check is the minimal, safe fix. [ASSUMED — would need to audit all activeStream read sites to confirm this; the fix description says the stream is already under streamMu]

**Test assertion flip**: The pre-fix test shows `chunksDelivered < iterations` (some iterations got empty content). Post-fix: assert `chunksDelivered == iterations`.

### H-1: Admin SSE Shutdown Wiring

**Current shape** (server.go:377-381, admin/sse.go:167-203, VERIFIED):

`sseLoop` blocks on `r.Context().Done()` which `srv.Shutdown` does NOT cancel. No BaseContext, no RegisterOnShutdown.

**Fix pattern** — add a server-level shutdown channel and wire it through:

```go
// In Server struct:
shutdownCh chan struct{}   // closed when shutdown begins

// In Run(), before srv.Shutdown:
srv.RegisterOnShutdown(func() {
    select {
    case <-s.shutdownCh:   // already closed — idempotent
    default:
        close(s.shutdownCh)
    }
})

// In sseHandler (calls sseLoop):
sseLoop(ctx, w, flusher, sub, tickerC, snapshot, s.shutdownCh)

// In sseLoop signature:
func sseLoop(..., shutdownCh <-chan struct{}) error {
    for {
        select {
        case <-ctx.Done(): ...
        case <-shutdownCh:   // ← NEW: gateway shutting down
            return errors.New("admin: gateway shutting down")
        case <-tickerC: ...
        case line, ok := <-sub.C: ...
        }
    }
}
```

**Alternative**: Pass `s.shutdownCh` as a third ctx to sseLoop — simpler signature but same semantics.

**Overlap with P-2**: `RegisterOnShutdown` is the mechanism for both. They share a `shutdownCh` field on the Server struct. The same task in Plan 15-01 (or a pre-task in 15-02) adds this field and wires it.

**Test assertion flip** (regression_rel_http_01_test.go): Pre-fix asserts `elapsed >= 2s`. Post-fix: assert `elapsed < 1s`.

### H-2: ACP Cancel Before Slot Return on Idle-Timeout

**Current shape** (openai/sse.go:460-462, 482-484, VERIFIED):

Both `idleC` arm and `applyChunk` error arm call `run.StopWatchdog()()` which cancels the AfterFunc that would have fired `ACP.Cancel(sid)`. No compensating explicit Cancel call.

**Fix pattern** (D-06 inverse — do not suppress watchdog on idle path):

```go
// idleC arm — AFTER fix (option A: don't stop watchdog, let it fire):
case <-idleC:
    // Do NOT call run.StopWatchdog() here — let the AfterFunc fire Cancel.
    // The watchdog was registered with context.AfterFunc(ctx, ...) where
    // ctx will be cancelled when this function returns and the handler calls
    // cancelFn. This is the pattern the ollama ndjson.go idle path uses.
    e.logger.Warn("stream.idle_timeout", ...)
    _, _ = fmt.Fprintf(w, "data: {\"error\":...}\n\n")
    _, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
    e.flusher.Flush()
    return e.aggregatedResponse(canonical.StopUnknown, nil),
        fmt.Errorf("openai: sse %w", canonical.ErrStreamIdleTimeout)
```

```go
// applyChunk write-error arm — AFTER fix (option B: explicit Cancel before stop):
if err := e.applyChunk(c); err != nil {
    if cancel := run.ExplicitCancel(); cancel != nil {   // or: engine.Cancel(sessionID)
        cancel()
    }
    if stop := run.StopWatchdog(); stop != nil {
        stop()
    }
    return e.aggregatedResponse(canonical.StopUnknown, nil), err
}
```

The simpler option is A (remove `StopWatchdog()` from idleC arm entirely), mirroring the ollama ndjson.go correct behavior. Option B adds complexity. The CONTEXT.md states "Don't call `stop()` (let the deferred `cancelFn` fire the watchdog like ollama does)" as the preferred fix — use Option A.

**Test assertion flip** (regression_rel_http_02_test.go): Pre-fix asserts `watchdogCalled==true && watchdogStopped==true`. Post-fix: assert `watchdogCalled==false` (StopWatchdog never called on idle path).

### H-3: Surface-Native Terminal Error Frames

**Current shape** (openai/sse.go:543-557, ollama/ndjson.go:541-549, VERIFIED):

Both `finalizeSSE` and `finalizeNDJSON` log at Debug and return bare errors with no terminal frame when `rerr != nil`.

**Anthropic surface confirmed correct** (anthropic/sse.go:785-795, VERIFIED): Already emits `event: error` via `writeSSEError(e.w, e.flusher, errAPI, "stream terminated")`. No change needed for Anthropic. Document asymmetry in SUMMARY.

**Fix pattern for OpenAI** (mirror idle-timeout path at :470-472):
```go
// finalizeSSE — AFTER fix
if rerr != nil {
    if stop := run.StopWatchdog(); stop != nil {
        stop()
    }
    // Per D-09: WARN log with session_id, worker_pid (best-effort from slot),
    // kiro_exit_code (if available from rerr chain), bytes_streamed (from e.bytesStreamed).
    e.logger.Warn("openai: sse worker terminated mid-stream",
        "session_id", run.SessionID(),
        "err", rerr,
    )
    // Surface-native terminal error frame (per D-09):
    _, _ = fmt.Fprintf(w, "data: {\"error\":{\"type\":\"server_error\","+
        "\"code\":\"upstream_disconnect\","+
        "\"message\":\"worker terminated mid-stream\",\"param\":null}}\n\n")
    _, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
    e.flusher.Flush()
    return e.aggregatedResponse(canonical.StopUnknown, nil),
        fmt.Errorf("openai: sse stream result: %w", rerr)
}
```

**Fix pattern for Ollama** (mirror idle-timeout path at :437-450):
```go
// finalizeNDJSON — AFTER fix
if rerr != nil {
    logger.Warn("ollama: ndjson worker terminated mid-stream",
        "session_id", run.SessionID(),
        "err", rerr,
    )
    emptyResp := aggregateOllamaResponse(req, state, canonical.StopUnknown)
    if isChat {
        frame := chatResponseToWire(emptyResp, start, model)
        frame.Done = true
        frame.DoneReason = "error"
        frame.Error = "upstream_disconnect: worker terminated mid-stream"
        _ = marshalAndWrite(w, flusher, frame, cancelFn)
    } else {
        frame := generateResponseToWire(emptyResp, start, model)
        frame.Done = true
        frame.DoneReason = "error"
        frame.Error = "upstream_disconnect: worker terminated mid-stream"
        _ = marshalAndWrite(w, flusher, frame, cancelFn)
    }
    return emptyResp, fmt.Errorf("ollama: ndjson stream result: %w", rerr)
}
```

**Note on H-3 test**: Two separate regression test files must BOTH be unskipped in the same commit: `internal/adapter/openai/regression_rel_http_03_test.go` and `internal/adapter/ollama/regression_rel_http_03_test.go`. The CONTEXT.md canonical_refs notes both, and D-03 requires both in the same atomic commit for H-3. [ASSUMED: whether the ollama test's `run.StopWatchdog()` interaction also needs cleanup — inspect the ollama ndjson.go idle path already calls stop() correctly, so H-3 for Ollama is only the finalizeNDJSON change]

**WARN log slog pattern** (from codebase verification): All structured log calls use field-style:
```go
logger.Warn("event.name", "key1", val1, "key2", val2)
```
Match this pattern exactly. [VERIFIED: grep of openai/sse.go logger.Warn calls]

### T-1: PID Identity Check

**Current shape** (tray.go:144-145, VERIFIED): `processAlive(pid)` returns true for ANY live PID. No name/cmdline check.

**Fix: new `verifyGatewayIdentity(pid int, binPath string) bool` per OS:**

macOS (`pidfile_darwin.go`):
```go
func verifyGatewayIdentity(pid int, binPath string) bool {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
    if err != nil {
        return false   // conservative: process not verifiable = not ours
    }
    comm := strings.TrimSpace(string(out))
    return strings.HasSuffix(comm, "otto-gateway") || comm == "otto-gateway"
}
```

Windows (`pidfile_windows.go`): use `QueryFullProcessImageNameW` via `golang.org/x/sys/windows` (already in go.mod as indirect via energye/systray):
```go
// golang.org/x/sys/windows.QueryFullProcessImageName is available in the module.
// Signature: windows.QueryFullProcessImageName(handle Handle, flags uint32, buf *uint16, size *uint32) error
func verifyGatewayIdentity(pid int, binPath string) bool {
    h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
    if err != nil {
        return false
    }
    defer windows.CloseHandle(h)
    buf := make([]uint16, windows.MAX_PATH)
    n := uint32(len(buf))
    if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
        return false
    }
    fullPath := windows.UTF16ToString(buf[:n])
    return strings.EqualFold(filepath.Base(fullPath), "otto-gateway.exe")
}
```

**Note**: `windows.QueryFullProcessImageName` must be verified available in `golang.org/x/sys/windows` at the version in go.mod (`v0.45.0`). [ASSUMED — function exists in x/sys/windows; needs runtime import verification. Alternative if absent: call `GetModuleFileNameExW` via syscall.NewProc, which is available in all x/sys versions.]

**Wire into makeProbe** (tray.go:144-145):
```go
func (s *trayState) makeProbe() probeFunc {
    pidPath := installRootPIDFile(s.installRoot)
    client := newStatusClient(s.dashboardURL, 1*time.Second)
    return func() (bool, bool, Snapshot) {
        pid, _ := readPIDFile(pidPath)
        alive := pid > 0 && processAlive(pid)
        if alive {
            alive = verifyGatewayIdentity(pid, s.cfg.BinPath)  // ← NEW
        }
        if !alive {
            return false, false, Snapshot{}
        }
        ...
    }
}
```

**Wire into bash wrapper** (otto-gw:782): Insert identity check between `kill -0` and `kill`:
```bash
if kill -0 "$pid" 2>/dev/null; then
    # T-1 fix: verify PID belongs to otto-gateway, not a recycled process
    actual_comm=$(ps -p "$pid" -o comm= 2>/dev/null || true)
    if [[ "$actual_comm" != *otto-gateway* ]]; then
        log_warn "stop: PID $pid is alive but comm is '$actual_comm' — treating as stale"
        rm -f "$OTTO_PID"
        stop_by_name "stale recycled PID" && return 0
        return 0
    fi
    kill "$pid"
    ...
```

**Wire into PS1** (otto-gw.ps1:560-566): Add `.Path` comparison before `$proc.Kill()`.

**Test assertion flip** (regression_rel_tray_01_test.go): Uncomment the `verifyGatewayIdentity` call; assert it returns `false` for the test binary PID.

### T-2: PowerShell Get-GatewayStatus Refactor

**Current shape** (otto-gw.ps1:581-593, VERIFIED): `Get-GatewayStatus` calls `exit 1` at lines 581 and 593 when PID file is missing or stale. `Invoke-Support` wraps it in `try/catch` at :1464, but PowerShell `exit` is process-terminating — `try/catch` cannot intercept it.

**Fix pattern** (D-15 — return object, not exit 1):
```powershell
function Get-GatewayStatus {
    if (-not (Test-Path $PidFile)) {
        return [pscustomobject]@{ Status = 'stopped'; Message = 'otto-gateway: stopped (no PID file)' }
    }
    $storedPid = [int](Get-Content $PidFile -Raw)
    $proc = Get-Process -Id $storedPid -ErrorAction SilentlyContinue
    if (-not $proc) {
        Remove-Item $PidFile -ErrorAction SilentlyContinue
        return [pscustomobject]@{ Status = 'stopped'; Message = 'otto-gateway: stopped (stale PID)' }
    }
    Write-Host "otto-gateway: running (PID $storedPid)"
    # ... existing health probe logic ...
    return [pscustomobject]@{ Status = 'running'; Message = "..." }
}
```

The `status` dispatch arm (top-level CLI) still calls `exit 1` for the user-facing case — that's correct. Only `Invoke-Support`'s usage path needs the object return.

`Invoke-Support` becomes:
```powershell
$gwStatus = Get-GatewayStatus
Set-Content -Path ... -Value $gwStatus.Message
if ($gwStatus.Status -ne 'running') {
    # Include "gateway not running at bundle-time" note but CONTINUE bundle assembly
}
```

**Bash equivalent** (already correct per otto-gw:1838-1840): The bash wrapper uses `status_out=$( ( status 2>&1 ) || true )` — the subshell + `|| true` pattern prevents `set -e` propagation. No change needed for bash T-2.

**Test**: regression_rel_tray_02_test.go is a discoverability stub only (`t.Skip` with pointer to manual reproducer). The manual reproducer at `tests/reliability/manual/REL-TRAY-02-repro.ps1` is the validation path. The Go test remains permanently skipped (it's a discoverability stub, not a runnable test).

### T-3: macOS (and Windows) Icon/Tooltip on Every FSM Transition

**Current shape** (tray.go:74-75, :199-201, VERIFIED):
- `setIcon(icon.Template)` and `systray.SetTooltip("OTTO Gateway")` called once in `onReady`.
- `applyState()` only calls `s.miHeader.SetTitle(...)` and `s.miSubheader.SetTitle(...)`.
- `notify()` called only on Running→Stopped/Error transitions — and is silently no-op on macOS for LSUIElement agents without notification permission.

**energye/systray API** (VERIFIED from module source at v1.0.3):
- macOS: `systray.SetTemplateIcon([]byte, []byte)` — system-adaptive dark/light PNG template
- macOS: `systray.SetTooltip(string)` — tray item tooltip
- Windows: `systray.SetIcon([]byte)` — ICO bytes
- Windows: `systray.SetTooltip(string)` — tray tooltip via `setTooltip` internal

Both platforms expose `systray.SetIcon` / `systray.SetTooltip` as package-level functions callable from any goroutine (the lib dispatches internally). [VERIFIED: module source]

**Icon assets** (VERIFIED from filesystem): Currently only `template.png` (PNG, 179 bytes) and `template.ico` (ICO, 1.1KB) exist. The fix requires 3 state variants (Running=green, Starting/Stopping=yellow, Error/Stopped=red). These must be added as embedded assets.

**Simplest icon strategy** (no external tooling):
- Check in 3 PNGs (`template_running.png`, `template_warning.png`, `template_error.png`) and 3 ICOs for Windows.
- Update `icon_darwin.go` to embed all 3; update `icon_windows.go` similarly.
- Brand palette from existing single asset: the current `template.png` is monochrome (template image auto-tints on macOS). For color state icons, PNG must be a non-template colorized image (use `systray.SetIcon` not `systray.SetTemplateIcon` for colored state icons on macOS — template images render as grayscale). [ASSUMED — standard macOS behavior; worth confirming with actual PNG content]

**uihelpers_darwin.go additions**:
```go
// iconForState returns the icon bytes for the given tray state.
// Uses state-specific colored icons rather than the template image
// because template images are always rendered monochrome (system-tinted).
func setIconForState(state TrayState) {
    switch state {
    case StateRunning:
        systray.SetTemplateIcon(icon.Running, icon.Running)
    case StateStarting, StateStopping:
        systray.SetIcon(icon.Warning)   // yellow — colored, not template
    default:
        systray.SetIcon(icon.Error)     // red — colored, not template
    }
}

func tooltipForState(state TrayState, detail string) string {
    s := fmt.Sprintf("OTTO Gateway · %s", state)
    if detail != "" {
        s += " (" + detail + ")"
    }
    return s
}
```

**applyState additions** (tray.go:175-202):
```go
func (s *trayState) applyState(out stateOutput) {
    s.mu.Lock()
    prev := s.current
    s.current = out.State
    s.mu.Unlock()

    // T-3 fix: update icon and tooltip on every FSM transition
    setIconForState(out.State)
    systray.SetTooltip(tooltipForState(out.State, out.Detail))

    // existing menu-item updates ...
    header := fmt.Sprintf("OTTO Gateway · %s", out.State)
    ...
}
```

**Test**: regression_rel_tray_03_test.go is a discoverability stub (permanently skipped, points to manual reproducer). The manual reproducer at `tests/reliability/manual/REL-TRAY-03-repro.sh` is the validation path. The Go test stub remains; no assertion flip needed.

---

## Package Legitimacy Audit

No new packages are installed in Phase 15. All fixes use stdlib and packages already in go.mod. [VERIFIED: go.mod read]

| Package | Status | Note |
|---------|--------|------|
| `golang.org/x/sys/windows` | Already in go.mod (indirect) | Used for T-1 Windows identity check — no new install |
| All stdlib packages | In-tree | No registry check needed |

**No packages added, removed, or modified in go.mod.**

---

## Common Pitfalls

### Pitfall 1: P-1 Regression Test Pre-Assertion Inversion
**What goes wrong:** The Phase 14 regression test for P-1 asserts `Stats().Size == 0` (demonstrating the bug). After removing `t.Skip`, the test will PASS pre-fix (demonstrating the bug) but FAIL post-fix unless the assertion is also changed to `Stats().Size == 1`.
**Why it happens:** Phase 14 wrote the test to demonstrate the pre-fix observable, not the post-fix green.
**How to avoid:** The fix commit must BOTH remove `t.Skip` AND invert the size assertion from `want 0` to `want 1`.
**Warning signs:** Test passes immediately after removing t.Skip but before making the source fix.

### Pitfall 2: P-2 Test Is a Pool-Level Reproducer, Not a main.go Test
**What goes wrong:** The P-2 regression test (`regression_rel_pool_02_test.go`) tests `pool.Close()` cancellation behavior — it does NOT test `main.go`'s `os.Exit` bypass directly. The pool-level test will pass as long as `pool.Close()` issues Cancel calls. The actual main.go fix (replacing `os.Exit(1)`) is not directly covered by the regression test.
**Why it happens:** `cmd/otto-gateway/main.go` is hard to unit-test; the reproducer captures the observable symptom (Cancel not issued) at pool level.
**How to avoid:** The P-2 fix has two parts: (a) change main.go:131 — verified by code review; (b) wire in-flight cancel during grace — tested by the pool test. Both parts are required for the fix; only (b) is in the regression test.

### Pitfall 3: server.go Edited by Both P-2 and H-1
**What goes wrong:** Plan 15-01 edits `Run()` and `RunUntilSignal()` for P-2; Plan 15-02 edits `Run()` for H-1 shutdown channel. If run in parallel on separate worktrees, merge will conflict.
**Why it happens:** Both fixes require touching the `http.Server{}` construction block in `Run()`.
**How to avoid:** Put the `shutdownCh` + `RegisterOnShutdown` wiring in Plan 15-01 as the P-2 in-flight cancel mechanism, and let H-1 (Plan 15-02) only touch `sseLoop` + `sseHandler`. The `shutdownCh` close becomes the signal that both P-2 (in-flight stream cancel) and H-1 (SSE handler exit) consume.
**Warning signs:** Both plans list `internal/server/server.go` in their `files_modified`.

### Pitfall 4: openai/sse.go Edited by Both H-2 and H-3
**What goes wrong:** H-2 edits the `idleC` case (around line 460) and `applyChunk` error case (around line 482); H-3 edits `finalizeSSE` (around line 543). These are different code regions in the same file.
**Why it happens:** Same file, different functions.
**How to avoid:** They are NOT conflicting — fix H-2 first (remove StopWatchdog from idleC arm), then H-3 (add error frame to finalizeSSE). Sequential tasks in Plan 15-02.

### Pitfall 5: P-3 CAS — `atomic.Pointer` vs Mutex+Identity Check
**What goes wrong:** Using `atomic.Pointer[Stream].CompareAndSwap` requires converting the field type and touching every read site. The existing `streamMu` mutex already serializes access — the identity check (`if c.activeStream == stream`) under the existing mutex is the minimal safe fix.
**Why it happens:** Review mentions CAS; Go 1.23 has `atomic.Pointer[T]`; temptation to use the modern API.
**How to avoid:** Stay with the mutex+identity-check pattern. No field type change, no read-site audit needed.

### Pitfall 6: T-1 Windows — QueryFullProcessImageName Availability
**What goes wrong:** `windows.QueryFullProcessImageName` may not be exported in `golang.org/x/sys/windows@v0.45.0`.
**Why it happens:** The x/sys/windows API surface evolves; lower versions may use different export names.
**How to avoid:** Verify at task time with `grep -r "QueryFullProcessImageName" $(go env GOPATH)/pkg/mod/golang.org/x/sys@v0.45.0/windows/`. Fallback: use `windows.GetModuleFileNameEx` or parse the `PROCESS_EXTENDED_BASIC_INFORMATION.ImageFileName` via `NtQueryInformationProcess`. The fallback still uses only `golang.org/x/sys/windows` — no new dep.

### Pitfall 7: T-3 Template Icon vs Color Icon on macOS
**What goes wrong:** Using `SetTemplateIcon` with a colored PNG results in a monochrome (grayscale) icon — macOS renders template images as system-tinted. For green/yellow/red state icons, the PNG must be used via `SetIcon` (non-template) to preserve color.
**Why it happens:** The existing icon uses `SetTemplateIcon` for dark/light adaptation, which strips color.
**How to avoid:** Use `SetTemplateIcon` for the "running" (green = could be monochrome, just needs to look healthy) state if a monochrome adaptation is acceptable, but use `SetIcon` for warning/error states where color is the primary signal.

### Pitfall 8: Phase 14 WR-01 Dead Assertion in regression_rel_pool_04_test.go
**What goes wrong:** Phase 14 review flagged `regression_rel_pool_04_test.go:134` has a dead assertion. This file is Phase 16 scope (REL-POOL-04). Do NOT touch it in Phase 15.
**How to avoid:** Only unskip and modify the 9 regression test files for Phase 15 findings. Phase 16 files must stay skipped and unchanged.

---

## Runtime State Inventory

Phase 15 is a code-and-script fix phase, not a rename/refactor. The only new persistent state is:

| Category | Items | Action Required |
|----------|-------|-----------------|
| Stored data | None | — |
| Live service config | `POOL_ACQUIRE_TIMEOUT_MS` env var is net-new (D-04) | Deployments pick up the default (30s) automatically; no migration needed |
| OS-registered state | None | — |
| Secrets/env vars | `POOL_ACQUIRE_TIMEOUT_MS` — new env key with default; no existing deployment sets it | Add to CLAUDE.md env-var table in phase-close commit |
| Build artifacts | None | — |

**Nothing found in stored data / live service / OS-registered / secrets / build artifacts categories that requires migration.**

---

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go 1.24 (go.mod declares 1.26.4) | All Go fixes | Yes | 1.26.4 (from go.mod) | — |
| golang.org/x/sys/windows@v0.45.0 | T-1 Windows identity check | Already in go.mod | v0.45.0 | syscall.NewProc fallback |
| github.com/energye/systray@v1.0.3 | T-3 setIcon/SetTooltip | Already in go.mod | v1.0.3 | — |
| macOS system tray (GUI session) | T-3 validation | macOS dev box | darwin 25.5.0 | Manual reproducer (REL-TRAY-03-repro.sh) |
| Windows PowerShell | T-2 validation | NOT available on macOS dev box | — | Manual reproducer (REL-TRAY-02-repro.ps1) |

**Missing dependencies with no fallback:** None.

**Missing dependencies with fallback:**
- Windows (for T-2/T-3 validation): Manual reproducers in `tests/reliability/manual/` serve as the operator-run validation path. Go tests for T-2/T-3 are discoverability stubs that remain permanently skipped.

---

## Validation Architecture

`nyquist_validation: true` in `.planning/config.json` — this section is required.

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go testing + goleak (goroutine leak) + `-race` |
| Config file | none (go test via Makefile / `go test ./...`) |
| Quick run command | `go test -race ./internal/pool/... ./internal/server/... ./internal/adapter/openai/... ./internal/adapter/ollama/... ./cmd/otto-tray/...` |
| Full suite command | `go test -race ./...` |

### Phase Requirements Validation Map

| REQ-ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| REL-POOL-01 | Pool re-queues on transient spawn failure; size stays at 1; acquire times out with ErrPoolExhausted | unit (race) | `go test -race ./internal/pool/ -run TestRegression_REL_POOL_01` | YES — `internal/pool/regression_rel_pool_01_test.go` |
| REL-POOL-02 | pool.Close() cancels all in-flight sessions (proxy for: cleanup runs on all shutdown paths) | unit (race) | `go test -race ./internal/pool/ -run TestRegression_REL_POOL_02` | YES — `internal/pool/regression_rel_pool_02_test.go` |
| REL-POOL-03 | CAS guard: B's stream receives content even after A's stale goroutine runs | unit (race) | `go test -race ./internal/pool/ -run TestRegression_REL_POOL_03` | YES — `internal/pool/regression_rel_pool_03_test.go` |
| REL-HTTP-01 | srv.Shutdown returns in < 1s with SSE connection open | unit (real HTTP) | `go test -race ./internal/server/ -run TestRegression_REL_HTTP_01` | YES — `internal/server/regression_rel_http_01_test.go` |
| REL-HTTP-02 | idleC arm does NOT suppress watchdog (or cancels explicitly before stopping) | unit | `go test -race ./internal/adapter/openai/ -run TestRegression_REL_HTTP_02` | YES — `internal/adapter/openai/regression_rel_http_02_test.go` |
| REL-HTTP-03 (OpenAI) | finalizeSSE emits error frame + [DONE] on rerr != nil | unit | `go test -race ./internal/adapter/openai/ -run TestRegression_REL_HTTP_03` | YES — `internal/adapter/openai/regression_rel_http_03_test.go` |
| REL-HTTP-03 (Ollama) | finalizeNDJSON emits done:true + done_reason:error on rerr != nil | unit | `go test -race ./internal/adapter/ollama/ -run TestRegression_REL_HTTP_03` | YES — `internal/adapter/ollama/regression_rel_http_03_test.go` |
| REL-TRAY-01 | verifyGatewayIdentity rejects non-gateway PID | unit (`//go:build darwin\|\|windows`) | `go test -race ./cmd/otto-tray/ -run TestRegression_REL_TRAY_01` | YES — `cmd/otto-tray/regression_rel_tray_01_test.go` |
| REL-TRAY-02 | Get-GatewayStatus returns object; Invoke-Support completes | manual-only (PowerShell) | `tests/reliability/manual/REL-TRAY-02-repro.ps1` on Windows | YES — stub + manual repro |
| REL-TRAY-03 | applyState calls setIcon + SetTooltip; icon changes on death | manual-only (macOS GUI) | `tests/reliability/manual/REL-TRAY-03-repro.sh` on macOS | YES — stub + manual repro |

### Validation Approach by Finding

**REL-POOL-01 (P-1):**
- Pre-fix assertion: `Stats().Size == 0` after transient respawn failure.
- Post-fix assertion: `Stats().Size == 1` (slot re-queued).
- Also: NewSession with expired context returns error wrapping `ErrPoolExhausted`.
- Proven green by: `go test -race ./internal/pool/ -run TestRegression_REL_POOL_01 -v`

**REL-POOL-02 (P-2):**
- Pre-fix assertion: `cancelsBefore == 0` (no cancel without pool.Close).
- Post-fix assertion: `cancelsAfter >= 2` (pool.Close ran, both sessions cancelled).
- Note: The main.go os.Exit fix is verified by code review — no automated test covers main.go directly.
- Proven green by: `go test -race ./internal/pool/ -run TestRegression_REL_POOL_02 -v`

**REL-POOL-03 (P-3):**
- Pre-fix assertion: some iterations of B return empty content (chunksDelivered < iterations).
- Post-fix assertion: all iterations deliver content (chunksDelivered == iterations).
- Proven green by: `go test -race ./internal/pool/ -run TestRegression_REL_POOL_03 -v`

**REL-HTTP-01 (H-1):**
- Pre-fix assertion: Shutdown blocks > 2s (elapsed >= minBlockDuration).
- Post-fix assertion: Shutdown returns in < 1s (elapsed < 1s).
- Proven green by: `go test -race ./internal/server/ -run TestRegression_REL_HTTP_01 -v`

**REL-HTTP-02 (H-2):**
- Pre-fix assertion: `watchdogCalled==true && watchdogStopped==true` (AfterFunc suppressed).
- Post-fix assertion: `watchdogCalled==false` (StopWatchdog not called on idleC path).
- Proven green by: `go test -race ./internal/adapter/openai/ -run TestRegression_REL_HTTP_02 -v`

**REL-HTTP-03 (H-3):**
- Pre-fix OpenAI: body does NOT contain `data: {"error":` and NOT `data: [DONE]`.
- Post-fix OpenAI: body DOES contain both.
- Pre-fix Ollama: body does NOT contain `"done_reason":"error"` and NOT `"done":true`.
- Post-fix Ollama: body DOES contain both.
- Both tests must be unskipped in the same commit.
- Proven green by: `go test -race ./internal/adapter/openai/ ./internal/adapter/ollama/ -run TestRegression_REL_HTTP_03 -v`

**REL-TRAY-01 (T-1):**
- Pre-fix: `processAlive(os.Getpid())` returns true with no identity rejection.
- Post-fix: `verifyGatewayIdentity(os.Getpid(), "/any/path/otto-gateway")` returns false.
- Platform gate: `//go:build darwin || windows` — runs on macOS dev box.
- Proven green by: `go test -race ./cmd/otto-tray/ -run TestRegression_REL_TRAY_01 -v` (on macOS)

**REL-TRAY-02 (T-2):**
- Go test is a permanently-skipped discoverability stub. Validation is manual-only.
- Operator validation: Run `tests/reliability/manual/REL-TRAY-02-repro.ps1` on Windows with gateway stopped. Post-fix: script completes, zip file is created, exit 0.
- Deferred to operator: document in Phase 15 SUMMARY as "manual validation required on Windows."

**REL-TRAY-03 (T-3):**
- Go test is a permanently-skipped discoverability stub. Validation is manual-only.
- Operator validation: Run `tests/reliability/manual/REL-TRAY-03-repro.sh` on macOS with tray running. Post-fix: menu bar icon changes to red/error state when gateway is killed; `kill -9 <gw_pid>` produces visible icon change within 6s (next poller tick).
- Deferred to operator: document in Phase 15 SUMMARY as "manual validation required on macOS GUI session."

### Sampling Rate

- **Per task commit:** `go test -race ./internal/pool/ ./internal/server/ ./internal/adapter/openai/ ./internal/adapter/ollama/ ./cmd/otto-tray/`
- **Per wave merge:** `go test -race ./...`
- **Phase gate:** Full suite green + REL-TRAY-02 and REL-TRAY-03 operator-validated before `/gsd-verify-work`

### Wave 0 Gaps

None — all 9 regression test files already exist from Phase 14. No new test scaffolding needed.

---

## Security Domain

`security_enforcement` is not explicitly disabled in config.json (absent = enabled).

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no | No auth surface changes in Phase 15 |
| V3 Session Management | no | Session cancel is an internal pool operation |
| V4 Access Control | no | No permission model changes |
| V5 Input Validation | no | No new user input surfaces; pool config validated at Load() |
| V6 Cryptography | no | No crypto changes |

**gosec G204 review required** for T-1: `ps -p $pid -o comm=` is called via `exec.CommandContext` with `pid` coming from a file we read. The PID is an int (parsed via `strconv.Atoi` in `readPIDFile`) — no tainted string injection. The command arguments are static strings + a `strconv.Itoa(pid)` integer. `//nolint:gosec` with a comment is appropriate if gosec flags G204 on this specific call, mirroring the existing `//nolint:gosec // url is operator-configured` pattern in the codebase.

**SIGKILL prevention**: T-1 fix prevents killing unrelated processes — this is the primary security benefit of Phase 15's tray fixes.

---

## Cross-Cutting Risks Summary

| Risk | Severity | Evidence | Resolution |
|------|----------|----------|------------|
| server.go edited by P-2 AND H-1 | HIGH | Both CONTEXT.md code_context and source confirm same file | Put both in Plan 15-01 OR serialize 15-02 depends_on 15-01 |
| openai/sse.go edited by H-2 AND H-3 | LOW | Different functions in same file; adjacent not overlapping | Sequential tasks within Plan 15-02; no merge conflict |
| P-1 test pre-assertion needs inversion | MEDIUM | Test asserts `want 0` (bug); must change to `want 1` (fix) | Fix commit must flip assertion in same atomic change |
| H-3 requires TWO test files unskipped atomically | MEDIUM | One commit must unskip openai AND ollama tests | Use single commit touching both files |
| T-1 Windows QueryFullProcessImageName availability | LOW | x/sys/windows@v0.45.0 — function exists in modern versions but not verified at this exact version | Verify at task time; syscall.NewProc fallback ready |
| Icon assets don't exist | MEDIUM | Only template.png + template.ico exist | Add 3 PNG variants + 3 ICO variants as pre-task in Plan 15-03 |
| T-2/T-3 require operator-run manual validation | LOW | Go tests are permanently-skipped stubs by design | Document as operator-deferred in Phase 15 SUMMARY |

---

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | P-3 fix uses mutex+identity check, not `atomic.Pointer.CompareAndSwap` — no activeStream read-site audit needed | P-3 fix pattern | If other goroutines read activeStream without streamMu, CAS would be needed; audit activeStream reads in client.go before coding |
| A2 | `windows.QueryFullProcessImageName` is exported in `golang.org/x/sys/windows@v0.45.0` | T-1 Windows | If absent, use `syscall.NewProc("QueryFullProcessImageNameW")` fallback |
| A3 | H-3 Ollama finalizeNDJSON doesn't need a separate `run.StopWatchdog()` interaction fix — only the missing terminal frame | H-3 Ollama pattern | If watchdog suppression is also wrong in Ollama's finalizeNDJSON, both fixes needed; check ndjson.go:554 |
| A4 | `systray.SetIcon` / `systray.SetTooltip` are goroutine-safe in energye/systray@v1.0.3 | T-3 | If they must be called on the systray event thread, applyState needs to post to a channel; check energye source for thread-safety guarantees |
| A5 | The bash wrapper's T-2 is already correct and needs no change (subshell capture pattern at otto-gw:1838-1840) | T-2 | If the bash status() also exits 1 in a non-subshell context, verify bash behavior under `set -e` |
| A6 | Template PNG color vs grayscale behavior on macOS: colored PNGs passed to SetTemplateIcon render monochrome | T-3 icon strategy | If Apple's template image rendering preserves color under certain conditions, the SetIcon vs SetTemplateIcon split is unnecessary |

---

## Sources

### Primary (HIGH confidence — verified from live source)
- `internal/pool/pool.go:490-535` — verified NewSession acquire + respawn paths
- `internal/acp/client.go:855-896` — verified awaitPromptResult both arms
- `cmd/otto-gateway/main.go:127-132` — verified os.Exit bypass of deferred cleanup
- `internal/server/server.go:346-406` — verified Run() + RunUntilSignal() + shutdown shapes
- `internal/admin/sse.go:167-203` — verified sseLoop blocking select
- `internal/adapter/openai/sse.go:454-485, 535-558` — verified idleC arm, applyChunk arm, finalizeSSE
- `internal/adapter/ollama/ndjson.go:425-452, 540-549` — verified finalizeNDJSON + idle-timeout path
- `internal/adapter/anthropic/sse.go:785-806` — verified Anthropic already emits error frame
- `cmd/otto-tray/tray.go:72-202` — verified applyState, onReady, makeProbe
- `cmd/otto-tray/uihelpers_darwin.go` — verified notify is osascript display notification
- `cmd/otto-tray/uihelpers_windows.go` — verified Windows systray SetIcon API
- `cmd/otto-tray/pidfile_darwin.go`, `pidfile_windows.go` — verified processAlive implementations
- `scripts/otto-gw:776-795` — verified bash stop function PID kill without identity check
- `scripts/otto-gw.ps1:558-600, 1455-1470` — verified Stop-Gateway + Get-GatewayStatus + Invoke-Support
- `go.mod` — verified dependency set; no new packages needed
- All 9 regression test files — verified skip strings, pre-fix assertions, post-fix expectations
- `/Users/coreyellis/go/pkg/mod/github.com/energye/systray@v1.0.3/systray_darwin.go` — verified `SetTemplateIcon`, `SetIcon`, `SetTooltip` API signatures
- `.planning/phases/14-verify-reliability-findings/14-FINDING-{P-1..T-3}.md` — confirmed failure paths

### Secondary (MEDIUM confidence)
- `.planning/phases/14-verify-reliability-findings/14-VERIFICATION-LEDGER.md` — confirms all 9 findings at file:line
- `docs/reviews/2026-06-11-reliability-review.md` — fix sketches for all 9 findings
- `15-CONTEXT.md` — locked decisions D-01 through D-16

---

## Metadata

**Confidence breakdown:**
- Pool/ACP fixes (P-1, P-2, P-3): HIGH — live source verified; test files read; fix patterns match existing WR-07 re-queue precedent
- HTTP fixes (H-1, H-2, H-3): HIGH — live source verified; idle-timeout sibling paths already prove the correct patterns exist
- Tray fixes (T-1, T-2, T-3): HIGH for T-1/T-2 (live source verified, clear fix pattern); MEDIUM for T-3 (icon behavior on macOS template rendering is ASSUMED)
- Cross-plan overlap analysis: HIGH — both server.go and openai/sse.go overlap confirmed from source

**Research date:** 2026-06-11
**Valid until:** 2026-07-11 (stable Go codebase; no external API dependencies)
