# Phase 15: Fix Critical + High — Pattern Map

**Mapped:** 2026-06-11
**Files analyzed:** 15 production files + 9 regression test files
**Analogs found:** 15 / 15

---

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|---|---|---|---|---|
| `internal/pool/pool.go` | pool/scheduler | CRUD + timeout | self (existing WR-07 re-queue at `:525-532`) | self-extension |
| `internal/pool/config.go` | config | — | self (existing `Size`, `PingInterval` fields) | self-extension |
| `internal/acp/client.go` | pool-internal / ACP | event-driven | self (existing mutex+stream pattern at `:868-896`) | self-extension |
| `cmd/otto-gateway/main.go` | entrypoint | request-response | self (existing `defer cleanup()` at `:127`) | self-extension |
| `internal/server/server.go` | HTTP server lifecycle | request-response | self (existing `RegisterOnShutdown` / `shutdownCtx` at `:377-381`) | self-extension |
| `internal/admin/sse.go` | SSE handler | streaming | self (existing `sseLoop` select at `:184-202`) | self-extension |
| `internal/adapter/openai/sse.go` | HTTP adapter / SSE emitter | streaming | `internal/adapter/ollama/ndjson.go` idle-timeout path at `:430-452` | exact (sibling surface) |
| `internal/adapter/ollama/ndjson.go` | HTTP adapter / NDJSON emitter | streaming | self (`finalizeNDJSON` idle-timeout path at `:437-452`) | self-extension |
| `cmd/otto-tray/tray.go` | tray UI / FSM | event-driven | self (`applyState` at `:175-201`, `makeProbe` at `:140-163`) | self-extension |
| `cmd/otto-tray/uihelpers_darwin.go` | platform UI helper | event-driven | self (`setIcon` at `:19`, `notify` at `:43-48`) | self-extension |
| `cmd/otto-tray/uihelpers_windows.go` | platform UI helper | event-driven | self (`setIcon` at `:17`, `notify` at `:49-68`) | self-extension |
| `cmd/otto-tray/pidfile_darwin.go` | OS process check | request-response | self (`processAlive` at `:11-19`) | self-extension |
| `cmd/otto-tray/pidfile_windows.go` | OS process check | request-response | self (`processAlive` via `windows.OpenProcess` at `:18-33`) | self-extension |
| `scripts/otto-gw` | bash wrapper | request-response | self (`stop()` kill block at `:775-798`) | self-extension |
| `scripts/otto-gw.ps1` | PowerShell wrapper | request-response | self (`Stop-Gateway` at `:559-581`, bash `status_out` pattern at otto-gw:`:1838-1840`) | self-extension |
| `cmd/otto-tray/icon/icon_darwin.go` | embedded asset | — | self (`:1-17`) | self-extension |
| `cmd/otto-tray/icon/icon_windows.go` | embedded asset | — | self (`:1-21`) | self-extension |

---

## Pattern Assignments

### P-1: `internal/pool/pool.go` — bounded acquire + re-queue on transient error

**Bug site:**
```go
// pool.go:491-505 — current select (no timeout arm)
select {
case slot = <-p.slots:
case <-ctx.Done():
    return "", fmt.Errorf("pool: acquire cancelled: %w", ctx.Err())
case <-p.closing:
    return "", errors.New("pool: closed")
}
// pool.go:534 — removes slot permanently on ANY non-ctx error
p.removeSlot(slot)
return "", fmt.Errorf("pool: respawn slot %s: %w", slot.Label, err)
```

**Existing re-queue analog to copy** (pool.go:525-532, ctx-cancel branch — already correct):
```go
if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
    select {
    case p.slots <- slot:
        p.debugLog("pool.respawn.deferred", "slot", slot.Label, "err", err.Error())
    case <-p.closing:
        // Pool shutting down — Close drains p.all itself.
    }
    return "", fmt.Errorf("pool: respawn slot %s deferred: %w", slot.Label, err)
}
```
Copy the `select { case p.slots <- slot: ...; case <-p.closing: }` block verbatim for the transient non-ctx error arm at `:534`.

**New timeout arm pattern** (from RESEARCH.md fix sketch — no existing codebase analog):
```go
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
    return "", ErrPoolExhausted
}
```

**New sentinel** (add near top of pool.go, after imports):
```go
// ErrPoolExhausted is returned by NewSession when all slots are busy
// and the acquire-timeout (POOL_ACQUIRE_TIMEOUT_MS) elapses. Adapters
// map errors.Is(err, ErrPoolExhausted) to a 503 response.
var ErrPoolExhausted = errors.New("pool: all workers busy; retry in 5s")
```

---

### P-1: `internal/pool/config.go` — new `AcquireTimeout` field

**Existing env-var parsing pattern** (config.go:119-126, `applyDefaults`):
```go
func (c *Config) applyDefaults() {
    if c.Size <= 0 {
        c.Size = 1
    }
    if c.Factory == nil {
        c.Factory = acpClientFactory{}
    }
}
```
The `Config` struct (config.go:91-114) adds fields of type `time.Duration` parsed from env. Follow the existing `PingInterval time.Duration` field pattern. Add:
```go
// AcquireTimeout is the maximum time NewSession will block waiting
// for a free slot. Zero means use the default (30s). Env: POOL_ACQUIRE_TIMEOUT_MS.
AcquireTimeout time.Duration
```
and parse in `applyDefaults` using `os.Getenv("POOL_ACQUIRE_TIMEOUT_MS")` + `strconv.ParseInt` + `time.Duration(n) * time.Millisecond`, defaulting to `30 * time.Second` when absent/zero.

---

### P-2: `cmd/otto-gateway/main.go` — remove os.Exit bypass of deferred cleanup

**Bug site** (main.go:127-132):
```go
defer cleanup()

if err := app.srv.RunUntilSignal(bootCtx); err != nil {
    logger.Error("server stopped with error", "err", err)
    os.Exit(1)        // ← skips defer cleanup()
}
```

**Fix pattern** (replace os.Exit with explicit calls):
```go
defer cleanup()   // keeps working for normal return

if err := app.srv.RunUntilSignal(bootCtx); err != nil {
    logger.Error("server stopped with error", "err", err)
    cleanup()      // explicit: pool.Close + registry.Close + logger sync
    os.Exit(1)     // now safe: cleanup already ran
}
```
Note: `defer cleanup()` on line 127 stays to handle normal (nil-error) returns; the explicit call before `os.Exit(1)` handles the error path. The `cleanup` func must be idempotent (use `sync.Once`) — verify it already is before removing the Once or adding one.

---

### P-2 + H-1: `internal/server/server.go` — shutdown channel + second-SIGINT handler

**CRITICAL:** Both P-2 (in-flight cancel during 30s grace) and H-1 (admin SSE unwind) require edits to `Run()` at `:346-383` and `RunUntilSignal()` at `:388-406`. These MUST land in the same commit.

**Current `Run()` block** (server.go:346-383 — construct point for `http.Server`):
```go
func (s *Server) Run(ctx context.Context) error {
    srv := &http.Server{
        Addr:              s.addr,
        Handler:           s.router,
        ReadHeaderTimeout: 10 * time.Second,
        IdleTimeout:       120 * time.Second,
    }
    ...
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    if err := srv.Shutdown(shutdownCtx); err != nil {
        return fmt.Errorf("server: shutdown: %w", err)
    }
    return nil
}
```

**Current `RunUntilSignal()`** (server.go:388-406 — single-signal goroutine that exits after first signal):
```go
go func() {
    select {
    case <-sigCh:
        s.logger.Info("shutdown signal received")
        cancel()
    case <-derivedCtx.Done():
    }
}()
```

**Fix pattern — add `shutdownCh` to Server struct and wire RegisterOnShutdown:**
```go
// In Server struct (add field):
shutdownCh chan struct{}   // closed when Shutdown begins; H-1 SSE loop selects on it

// In Run(), after srv construction and before errCh select:
srv.RegisterOnShutdown(func() {
    select {
    case <-s.shutdownCh:   // idempotent: already closed
    default:
        close(s.shutdownCh)
    }
})

// In RunUntilSignal(), replace single-signal goroutine with two-signal version:
go func() {
    select {
    case <-sigCh:
        s.logger.Info("shutdown signal received")
        cancel()
    case <-derivedCtx.Done():
        return
    }
    // Second SIGINT during grace window → force exit.
    select {
    case <-sigCh:
        s.logger.Info("second shutdown signal; force-exiting")
        cancel()
        // Caller (main) sees non-nil error from Run; cleanup() runs explicitly.
    case <-derivedCtx.Done():
    }
}()
```

---

### H-1: `internal/admin/sse.go` — sseLoop selects on shutdownCh

**Current `sseLoop` signature** (admin/sse.go:167-174):
```go
func sseLoop(
    ctx context.Context,
    w io.Writer,
    flusher http.Flusher,
    sub *subscriber,
    tickerC <-chan time.Time,
    snapshot []string,
) error {
```

**Current select block** (admin/sse.go:184-202):
```go
for {
    select {
    case <-ctx.Done():
        return fmt.Errorf("admin.sse: ctx done: %w", ctx.Err())
    case <-tickerC:
        writeSSELine(w, "ping", "")
        flusher.Flush()
    case line, ok := <-sub.C:
        if !ok {
            return errors.New("admin: tailer closed subscriber channel")
        }
        writeSSELine(w, "log", line)
        flusher.Flush()
    }
}
```

**Fix pattern** — add `shutdownCh <-chan struct{}` parameter and new select arm:
```go
// Add to signature:
func sseLoop(..., shutdownCh <-chan struct{}) error {
    ...
    for {
        select {
        case <-ctx.Done():
            return fmt.Errorf("admin.sse: ctx done: %w", ctx.Err())
        case <-shutdownCh:                              // ← NEW
            return errors.New("admin: gateway shutting down")
        case <-tickerC: ...
        case line, ok := <-sub.C: ...
        }
    }
}
```
Update the `sseLoop` call site in the same file to pass `s.shutdownCh` (the field added in server.go).

---

### H-2: `internal/adapter/openai/sse.go` — don't suppress watchdog on idle path

**Bug site** (openai/sse.go:460-462 — idleC arm):
```go
case <-idleC:
    if stop := run.StopWatchdog(); stop != nil {
        stop()       // ← suppresses watchdog; ACP Cancel never fires
    }
    ...
```

**Bug site** (openai/sse.go:482-484 — applyChunk error arm):
```go
if err := e.applyChunk(c); err != nil {
    if stop := run.StopWatchdog(); stop != nil {
        stop()       // ← same issue
    }
```

**Analog: Ollama ndjson.go idle-timeout path (correct behavior)** (ndjson.go:430-452):
```go
case <-idleC:
    logger.Warn(
        "stream.idle_timeout",
        "surface", "ollama",
        "session_id", run.SessionID(),
        ...
    )
    emptyResp := aggregateOllamaResponse(req, state, canonical.StopUnknown)
    if isChat {
        frame := chatResponseToWire(emptyResp, start, model)
        frame.Done = true
        frame.DoneReason = "error"
        frame.Error = "stream idle timeout"
        _ = marshalAndWrite(w, flusher, frame, cancelFn)
    }
    ...
    return emptyResp, fmt.Errorf("ollama: ndjson %w", canonical.ErrStreamIdleTimeout)
```
Ollama does NOT call `StopWatchdog()` on idle — the watchdog fires naturally when `cancelFn` (the handler's context cancel) runs on return. Mirror this: remove the `StopWatchdog()` call from the `idleC` arm in openai/sse.go. The existing WARN log + error frame at `:463-474` stays unchanged.

---

### H-3: `internal/adapter/openai/sse.go` — terminal error frame in finalizeSSE

**Bug site** (openai/sse.go:543-557 — `finalizeSSE` rerr branch):
```go
if rerr != nil {
    if stop := run.StopWatchdog(); stop != nil {
        stop()
    }
    e.logger.Debug("openai: sse stream result error", "err", rerr)   // ← Debug only; no frame
    return e.aggregatedResponse(canonical.StopUnknown, nil),
        fmt.Errorf("openai: sse stream result: %w", rerr)
}
```

**Analog: openai/sse.go idle-timeout path at :463-474** (already emits frame + WARN):
```go
e.logger.Warn(
    "stream.idle_timeout",
    "surface", "openai",
    "session_id", run.SessionID(),
    "elapsed_ms", streamIdle.Milliseconds(),
    "request_id", plugin.RequestIDFromContext(ctx),
)
_, _ = fmt.Fprintf(w, "data: {\"error\":{\"message\":\"stream idle timeout\",\"type\":\"api_error\"}}\n\n")
_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
e.flusher.Flush()
```

**Fix pattern** — mirror idle-timeout pattern but with `upstream_disconnect` message (per D-09):
```go
if rerr != nil {
    if stop := run.StopWatchdog(); stop != nil {
        stop()
    }
    e.logger.Warn("openai: sse worker terminated mid-stream",
        "session_id", run.SessionID(),
        "err", rerr,
    )
    _, _ = fmt.Fprintf(w, "data: {\"error\":{\"type\":\"server_error\","+
        "\"code\":\"upstream_disconnect\","+
        "\"message\":\"worker terminated mid-stream\",\"param\":null}}\n\n")
    _, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
    e.flusher.Flush()
    return e.aggregatedResponse(canonical.StopUnknown, nil),
        fmt.Errorf("openai: sse stream result: %w", rerr)
}
```
The `e.flusher` field is already available in `sseEmitter`; `e.logger.Warn` key-value style matches all existing Warn calls in the file.

---

### H-3: `internal/adapter/ollama/ndjson.go` — terminal frame in finalizeNDJSON

**Bug site** (ndjson.go:541-549):
```go
if rerr != nil {
    logger.Debug("ollama: ndjson stream result error", "err", rerr)  // ← Debug only; no frame
    return aggregateOllamaResponse(req, state, canonical.StopUnknown),
        fmt.Errorf("ollama: ndjson stream result: %w", rerr)
}
```

**Analog: ndjson.go idle-timeout path at :437-452** (already emits done:true frame):
```go
emptyResp := aggregateOllamaResponse(req, state, canonical.StopUnknown)
if isChat {
    frame := chatResponseToWire(emptyResp, start, model)
    frame.Done = true
    frame.DoneReason = "error"
    frame.Error = "stream idle timeout"
    _ = marshalAndWrite(w, flusher, frame, cancelFn)
} else {
    frame := generateResponseToWire(emptyResp, start, model)
    frame.Done = true
    frame.DoneReason = "error"
    frame.Error = "stream idle timeout"
    _ = marshalAndWrite(w, flusher, frame, cancelFn)
}
```

**Fix pattern** — copy exactly; change `Error` string to `"upstream_disconnect: worker terminated mid-stream"` and prepend `logger.Warn`:
```go
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
`cancelFn` is already in scope in `finalizeNDJSON` — it is the parameter passed in.

---

### P-3: `internal/acp/client.go` — identity-guarded activeStream nil

**Bug sites** (client.go:868-870 ctx arm and :894-896 frame arm):
```go
// ctx arm (line 868-870):
c.streamMu.Lock()
c.activeStream = nil   // ← unconditional: wipes B's stream if A's goroutine is late
c.streamMu.Unlock()

// frame arm (line 894-896):
c.streamMu.Lock()
c.activeStream = nil   // ← same bug
c.streamMu.Unlock()
```

**Fix pattern** — identity check under existing mutex (no type change, no atomic):
```go
// Both arms — same change:
c.streamMu.Lock()
if c.activeStream == stream {   // only nil if this goroutine is still the owner
    c.activeStream = nil
}
c.streamMu.Unlock()
```
`stream` is the `*Stream` parameter already in scope in `awaitPromptResult`. No other changes required.

---

### T-1: `cmd/otto-tray/pidfile_darwin.go` — add verifyGatewayIdentity

**Existing analog** (pidfile_darwin.go:11-19 — `processAlive` using `os.FindProcess` + `Signal(0)`):
```go
//go:build darwin

func processAlive(pid int) bool {
    if pid <= 0 {
        return false
    }
    p, err := os.FindProcess(pid)
    if err != nil {
        return false
    }
    return p.Signal(syscall.Signal(0)) == nil
}
```

**New function to add in the same file** (follows same import style — add `os/exec`, `strconv`, `strings`, `context`, `time`):
```go
// verifyGatewayIdentity returns true if the process at pid has a
// command name ending in "otto-gateway". Conservative: returns false
// on any error so a non-verifiable PID is never killed.
// gosec G204: args are static strings + strconv.Itoa(int); no tainted input.
func verifyGatewayIdentity(pid int, _ string) bool {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output() //nolint:gosec // args are static+integer
    if err != nil {
        return false
    }
    comm := strings.TrimSpace(string(out))
    return strings.HasSuffix(comm, "otto-gateway") || comm == "otto-gateway"
}
```

**Wire into makeProbe** (tray.go:144-145 — current `alive` check):
```go
// Current:
alive := pid > 0 && processAlive(pid)

// After fix:
alive := pid > 0 && processAlive(pid)
if alive {
    alive = verifyGatewayIdentity(pid, s.cfg.BinPath)
}
```

---

### T-1: `cmd/otto-tray/pidfile_windows.go` — add verifyGatewayIdentity

**Existing analog** (pidfile_windows.go:18-33 — `processAlive` using `windows.OpenProcess`):
```go
//go:build windows

func processAlive(pid int) bool {
    ...
    h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
    if err != nil {
        return false
    }
    defer func() { _ = windows.CloseHandle(h) }()
    var exitCode uint32
    if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
        return true
    }
    return exitCode == stillActive
}
```

**New function to add in the same file** (same `windows.OpenProcess` + `windows.CloseHandle` pattern):
```go
// verifyGatewayIdentity returns true if the process at pid has an
// executable name ending in "otto-gateway.exe". Uses the same
// PROCESS_QUERY_LIMITED_INFORMATION handle already used by processAlive.
func verifyGatewayIdentity(pid int, _ string) bool {
    h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
    if err != nil {
        return false
    }
    defer func() { _ = windows.CloseHandle(h) }()
    buf := make([]uint16, windows.MAX_PATH)
    n := uint32(len(buf))
    if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
        return false
    }
    fullPath := windows.UTF16ToString(buf[:n])
    return strings.EqualFold(filepath.Base(fullPath), "otto-gateway.exe")
}
```
**Verify first:** `grep -r "QueryFullProcessImageName" $(go env GOPATH)/pkg/mod/golang.org/x/sys@v0.45.0/windows/` — if absent fall back to `windows.GetModuleFileNameEx`.

---

### T-1: `scripts/otto-gw` — PID identity check before kill

**Current bug** (otto-gw:776-784 — kills any PID that passes `kill -0` without name check):
```bash
if kill -0 "$pid" 2>/dev/null; then
    kill "$pid"
    _wait_for_exit "$pid"
    rm -f "$OTTO_PID"
```

**Analog: bash subcommand pattern** (otto-gw uses `$(...)` for capture elsewhere):

**Fix — insert identity check between `kill -0` and `kill`:**
```bash
if kill -0 "$pid" 2>/dev/null; then
    actual_comm=$(ps -p "$pid" -o comm= 2>/dev/null || true)
    if [[ "$actual_comm" != *otto-gateway* ]]; then
        log_warn "stop: PID $pid is alive but comm='$actual_comm' — stale PID file"
        rm -f "$OTTO_PID"
        stop_by_name "stale recycled PID" && return 0
        return 0
    fi
    kill "$pid"
    _wait_for_exit "$pid"
    rm -f "$OTTO_PID"
```

---

### T-1: `scripts/otto-gw.ps1` — PID identity check before Kill()

**Current bug** (otto-gw.ps1:559-566 — kills `$proc` without checking `.Path`):
```powershell
function Stop-Gateway {
    if (Test-Path $PidFile) {
        $storedPid = [int](Get-Content $PidFile -Raw)
        $proc = Get-Process -Id $storedPid -ErrorAction SilentlyContinue
        if ($proc) {
            $proc.Kill()
```

**Fix — add `.Path` check before `$proc.Kill()`:**
```powershell
if ($proc) {
    $procPath = try { $proc.MainModule.FileName } catch { '' }
    if ($procPath -and -not ($procPath -like '*otto-gateway*')) {
        Write-Warning "stop: PID $storedPid is alive but path='$procPath' — treating as stale PID"
        Remove-Item $PidFile -ErrorAction SilentlyContinue
        if (Stop-GatewayByName 'stale recycled PID') { return }
        return
    }
    $proc.Kill()
```

---

### T-2: `scripts/otto-gw.ps1` — Get-GatewayStatus returns object, no exit 1

**Bug site** (otto-gw.ps1:584-593):
```powershell
function Get-GatewayStatus {
    if (-not (Test-Path $PidFile)) {
        Write-Host "otto-gateway: stopped"
        exit 1           # ← process-terminating; try/catch at :1464 cannot intercept
    }
    $storedPid = [int](Get-Content $PidFile -Raw)
    $proc = Get-Process -Id $storedPid -ErrorAction SilentlyContinue
    if (-not $proc) {
        Write-Host "otto-gateway: stopped (stale PID)"
        exit 1           # ← same bug
    }
```

**Analog: bash subshell capture pattern** (otto-gw:1838-1840):
```bash
local status_out
status_out=$( ( status 2>&1 ) || true )
printf '%s\n' "$status_out" > "$bundle_root/health/status.txt"
```

**Fix — return `[pscustomobject]` instead of `exit 1`:**
```powershell
function Get-GatewayStatus {
    if (-not (Test-Path $PidFile)) {
        Write-Host "otto-gateway: stopped"
        return [pscustomobject]@{ Status = 'stopped'; Message = 'otto-gateway: stopped (no PID file)' }
    }
    $storedPid = [int](Get-Content $PidFile -Raw)
    $proc = Get-Process -Id $storedPid -ErrorAction SilentlyContinue
    if (-not $proc) {
        Remove-Item $PidFile -ErrorAction SilentlyContinue
        Write-Host "otto-gateway: stopped (stale PID)"
        return [pscustomobject]@{ Status = 'stopped'; Message = 'otto-gateway: stopped (stale PID)' }
    }
    # ... existing health probe logic ...
    return [pscustomobject]@{ Status = 'running'; Message = "otto-gateway: running (PID $storedPid)" }
}
```

**Update Invoke-Support call site** (otto-gw.ps1:1464 — current broken `try { Get-GatewayStatus 2>&1 | Out-String } catch { ... }`):
```powershell
# Current (broken — exit 1 is not catchable):
$statusOut = try { Get-GatewayStatus 2>&1 | Out-String } catch { "(status failed: $($_.Exception.Message))" }

# After fix:
$gwStatus = Get-GatewayStatus
$statusOut = $gwStatus.Message
# Gateway-down branch: still write status; bundle proceeds
Set-Content -Path (Join-Path $bundleRoot 'health\status.txt') -Value $statusOut -Encoding UTF8
```

**Note:** The top-level `status` CLI dispatch arm (if any) may still call `exit 1` for user-facing output — only change the `Invoke-Support` usage path.

---

### T-3: `cmd/otto-tray/tray.go` — applyState calls setIcon + SetTooltip

**Bug site** (tray.go:175-201 — applyState only calls SetTitle; notify only on Running→Stopped):
```go
func (s *trayState) applyState(out stateOutput) {
    s.mu.Lock()
    prev := s.current
    s.current = out.State
    s.mu.Unlock()

    header := fmt.Sprintf("OTTO Gateway · %s", out.State)
    ...
    s.miHeader.SetTitle(header)
    s.miSubheader.SetTitle(s.dashboardURL)
    ...
    if prev == StateRunning && (out.State == StateError || out.State == StateStopped) {
        notify("OTTO Gateway", fmt.Sprintf("Gateway is %s", out.State))
    }
}
```

**Fix — add icon + tooltip calls at the top of applyState (before menu-item updates):**
```go
func (s *trayState) applyState(out stateOutput) {
    s.mu.Lock()
    prev := s.current
    s.current = out.State
    s.mu.Unlock()

    // T-3 fix: always-visible state signal (D-11).
    setIconForState(out.State)
    systray.SetTooltip(tooltipForState(out.State, out.Detail))

    // existing menu-item updates unchanged below ...
```
`setIconForState` and `tooltipForState` are new helpers defined in `uihelpers_darwin.go` / `uihelpers_windows.go` (see below).

---

### T-3: `cmd/otto-tray/uihelpers_darwin.go` — state-aware icon helpers

**Current `setIcon`** (uihelpers_darwin.go:19):
```go
func setIcon(b []byte) { systray.SetTemplateIcon(b, b) }
```

**New helpers to add** (follow existing function style in this file — `context.WithTimeout` + 5s, best-effort):
```go
// setIconForState updates the menu-bar icon to reflect the current FSM state.
// Running uses SetTemplateIcon (adapts to dark/light bar); Starting/Stopping/
// Error/Stopped use SetIcon with colored PNGs because SetTemplateIcon strips color.
// Icon assets: cmd/otto-tray/icon/{Running,Warning,Error}.png (embedded).
func setIconForState(state TrayState) {
    switch state {
    case StateRunning:
        systray.SetTemplateIcon(icon.Running, icon.Running)
    case StateStarting, StateStopping:
        systray.SetIcon(icon.Warning)
    default: // StateError, StateStopped, StateUnknown
        systray.SetIcon(icon.Error)
    }
}

// tooltipForState returns the tray tooltip string for a given FSM state.
func tooltipForState(state TrayState, detail string) string {
    s := fmt.Sprintf("OTTO Gateway · %s", state)
    if detail != "" {
        s += " (" + detail + ")"
    }
    return s
}
```

**Update `notify` docstring** (uihelpers_darwin.go:51-58 — demote to secondary signal per D-12):
Add comment: `// As of v1.9, icon/tooltip via setIconForState is the primary state signal; // notification banners are a secondary best-effort signal (LSUIElement agents // may not receive notification permission).`

---

### T-3: `cmd/otto-tray/uihelpers_windows.go` — state-aware icon helpers (parity)

**Current `setIcon`** (uihelpers_windows.go:17):
```go
func setIcon(b []byte) { systray.SetIcon(b) }
```

**New helpers to add** (mirror darwin shape exactly, using `.ico` assets):
```go
func setIconForState(state TrayState) {
    switch state {
    case StateRunning:
        systray.SetIcon(icon.Running)     // running.ico — green
    case StateStarting, StateStopping:
        systray.SetIcon(icon.Warning)     // warning.ico — yellow
    default:
        systray.SetIcon(icon.Error)       // error.ico — red
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

---

### T-3: `cmd/otto-tray/icon/icon_darwin.go` — extend to embed 3 state icons

**Current** (icon_darwin.go:1-17 — single `Template []byte`):
```go
//go:embed template.png
var Template []byte
```

**Fix — add 3 new embed vars** (follow same `//go:embed` + `var` pattern):
```go
//go:embed template.png
var Template []byte

//go:embed template_running.png
var Running []byte

//go:embed template_warning.png
var Warning []byte

//go:embed template_error.png
var Error []byte
```
The three PNG files (`template_running.png`, `template_warning.png`, `template_error.png`) must be added to `cmd/otto-tray/icon/` as a pre-task before T-3's main commit.

---

### T-3: `cmd/otto-tray/icon/icon_windows.go` — extend to embed 3 state ICO files

**Current** (icon_windows.go:1-21 — single `Template []byte`):
```go
//go:embed template.ico
var Template []byte
```

**Fix — add 3 new embed vars:**
```go
//go:embed template.ico
var Template []byte

//go:embed running.ico
var Running []byte

//go:embed warning.ico
var Warning []byte

//go:embed error.ico
var Error []byte
```

---

## Shared Patterns

### Structured slog logging
**Source:** `internal/adapter/openai/sse.go:463-469` (Warn call in idleC arm)
**Apply to:** All new `logger.Warn` calls in H-3 (openai/sse.go, ollama/ndjson.go) and P-1 (pool.go)
```go
e.logger.Warn(
    "stream.idle_timeout",
    "surface", "openai",
    "session_id", run.SessionID(),
    "elapsed_ms", streamIdle.Milliseconds(),
    "request_id", plugin.RequestIDFromContext(ctx),
)
```
Key-value pairs only; no fmt.Sprintf in the message string; event name uses dot-notation.

### gosec G204 nolint comment
**Source:** `cmd/otto-tray/uihelpers_darwin.go:24` — `//nolint:gosec // url is operator-configured`
**Apply to:** `verifyGatewayIdentity` in `pidfile_darwin.go` where `exec.CommandContext` is called with a PID arg. Use: `//nolint:gosec // args are static strings + strconv.Itoa(int)`

### Platform build tags
**Source:** `cmd/otto-tray/pidfile_darwin.go:1` — `//go:build darwin`; `cmd/otto-tray/pidfile_windows.go:1` — `//go:build windows`
**Apply to:** Any new per-OS files (new `uihelpers_*` additions already have these; new `verifyGatewayIdentity` goes in the existing per-OS `pidfile_*.go` files, so build tags are already present).

### go:embed pattern
**Source:** `cmd/otto-tray/icon/icon_darwin.go:12-17`
**Apply to:** New `Running`, `Warning`, `Error` embed vars in both `icon_darwin.go` and `icon_windows.go`
```go
import _ "embed"

//go:embed <filename>
var <VarName> []byte
```

### Re-queue select pattern
**Source:** `internal/pool/pool.go:526-531` (ctx-cancel re-queue — the WR-07 fix)
**Apply to:** P-1 transient-error re-queue at `:534`
```go
select {
case p.slots <- slot:
    p.debugLog("pool.respawn.deferred", "slot", slot.Label, "err", err.Error())
case <-p.closing:
    // Pool shutting down — Close drains p.all itself.
}
```

---

## No Analog Found

All files have analogs (self-extensions or sibling-surface analogs). The only truly novel patterns (no codebase precedent) are:

| File | Pattern | Why novel |
|---|---|---|
| `internal/pool/pool.go` (timeout arm) | `case <-timeoutC:` arm in acquire select | No existing acquire timeout anywhere in pool.go |
| `internal/server/server.go` (second-SIGINT) | Two-stage signal goroutine | Current goroutine exits after first signal; two-stage is new |
| `cmd/otto-tray/icon/` new PNG/ICO assets | New binary assets | No existing color-state icon variants |

For these, RESEARCH.md fix patterns (reproduced in the Pattern Assignments above) are the reference.

---

## Metadata

**Analog search scope:** `internal/pool/`, `internal/acp/`, `internal/server/`, `internal/admin/`, `internal/adapter/openai/`, `internal/adapter/ollama/`, `cmd/otto-gateway/`, `cmd/otto-tray/`, `scripts/`
**Files read:** 18 source files
**Pattern extraction date:** 2026-06-11
