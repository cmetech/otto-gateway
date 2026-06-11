# Phase 16: Fix Mediums — Pattern Map

**Mapped:** 2026-06-11
**Files analyzed:** 18 production files + 14 regression test files
**Analogs found:** 18 / 18 (all self-extensions or sibling-surface analogs)

---

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|---|---|---|---|---|
| `internal/pool/pool.go` | pool/scheduler | event-driven + CRUD | self (Phase 15 re-queue at `:609-621`, O-1 acquire select at `:556-573`) | self-extension |
| `internal/acp/stream.go` | event-driven stream | event-driven | self (`push` at `:105-122` — context param change) | self-extension |
| `internal/acp/client.go` | ACP client | event-driven | self (`pingLoop` at `:507-530`, `handleNotification` dispatch at `:1097-1098`) | self-extension |
| `internal/acp/pool_pgid_windows.go` | platform helper | request-response | self (`killProcessGroup` stub at `:21`) + `pool_pgid_unix.go:47-52` (real impl analog) | exact (stub→impl pattern) |
| `internal/server/server.go` | HTTP server lifecycle | request-response | self (Phase 15 `RegisterOnShutdown`/`shutdownCh` at `:395-408`) | self-extension |
| `internal/server/health.go` | health handler | request-response | self (`PoolStats` struct at `:28-35`; `healthHandler` at `:61-84`) | self-extension |
| `internal/admin/tail.go` | SSE tailer | streaming | self (`TailerMaxLineBytes` constant at `:55-57`; `readLines` cap at `:402-406`) | self-extension |
| `internal/engine/collect.go` | collect / PostHook | event-driven | self (PostHook traversal at `:187-191`; `cancel` call pattern at `:164-165`) | self-extension |
| `internal/adapter/anthropic/collect.go` | collect / PostHook | event-driven | self (`RunPostHooks` at `:207-209`; `cancel` at `:170`) | self-extension |
| `internal/plugin/logging.go` | PostHook | event-driven | self (`startTimes sync.Map` at `:87`; `LoadAndDelete` pattern in `After`) | self-extension |
| `internal/plugin/trace.go` | PostHook | event-driven | `internal/plugin/logging.go` (same `sync.Map` leak pattern) | exact (sibling hook) |
| `internal/config/config.go` | config loader | request-response | self (`STREAM_IDLE_TIMEOUT_SEC` sign-check at `:366-368`; `PING_INTERVAL` load at `:295`) | self-extension |
| `cmd/otto-gateway/main.go` | entrypoint | request-response | self (Phase 15 boot-WARN pattern; `C-3` env check before goroutine start) | self-extension |
| `CLAUDE.md` | project docs | — | self (backward-compat env-var list) | self-extension |
| `cmd/otto-tray/uihelpers_windows.go` | platform UI helper | event-driven | self (`notify` at `:74-94`; `setIconForState` at `:24-33`) | self-extension |
| `cmd/otto-tray/uihelpers_darwin.go` | platform UI helper | event-driven | self (same shape as windows; Phase 15 `:setIconForState`) | self-extension |
| `cmd/otto-tray/tray.go` | tray FSM / poller | event-driven | self (`applyState` at `:178-210`; `makeProbe` at `:140-165`) | self-extension |
| `scripts/otto-gw.ps1` | PowerShell wrapper | batch / request-response | self (Phase 15 `Invoke-Support` pattern; `Get-GatewayStatus` pscustomobject) | self-extension |
| `internal/session/registry.go` | session registry | CRUD | self (`e.LastUsed = time.Now()` write under `r.mu` at `:206`) | self-extension |
| `internal/session/entry_acp.go` | session entry | CRUD | self (`e.LastUsed = time.Now()` at `MarkUsed:78` — unguarded site) | self-extension |

---

## Pattern Assignments

---

### Plan 16-01: Pool / ACP — P-4, P-5, P-6, O-1

---

#### P-4 (REL-POOL-04): `internal/acp/stream.go` — per-request ctx for `push`

**Finding:** `stream.push` at `:105-122` always blocks on `c.clientCtx` (the client lifetime context). When the consumer stalls (SSE write blocked), push blocks the readLoop goroutine — starving ping dispatch and triggering spurious `acp.ping.escalated_to_close` escalations.

**Bug site** (`internal/acp/stream.go:105-122`):
```go
func (s *Stream) push(ctx context.Context, ch canonical.Chunk) error {
    s.sendMu.RLock()
    defer s.sendMu.RUnlock()
    if s.closed {
        return errPushAfterClose
    }
    select {
    case s.chunks <- ch:
        ...
        return nil
    case <-ctx.Done():
        return fmt.Errorf("acp: stream push cancelled: %w", ctx.Err())
    case <-s.done:
        return errPushAfterClose
    }
}
```

**Caller site** (`internal/acp/client.go:1097-1098`) passes `c.clientCtx`:
```go
// push with client lifetime context for backpressure (D-03 + REVIEW FIX).
if err := s.push(c.clientCtx, chunk); err != nil {
```

**Fix pattern — pass per-request ctx instead of client lifetime ctx.** The stream already receives its per-prompt context in `newStream` (look up `newStream` construction in `client.go`'s `Prompt`). The `handleNotification` dispatch site at `:1097-1098` must pass the stream's own per-request context so a stalled consumer only cancels its own request, not the readLoop.

**Option B (simpler — separate ping dispatch path):** Move ping response handling out of `handleNotification` into a dedicated path that does not hold the readLoop goroutine. Check Phase 16 plans for the chosen approach.

**Unskip:** `internal/pool/regression_rel_pool_04_test.go` — remove `t.Skip("REL-POOL-04 (P-4): ...")` in the same commit.

---

#### P-5 (REL-POOL-05): `internal/session/registry.go` + `entry_acp.go` — `LastUsed` race fix

**Finding:** `Entry.LastUsed` is written at `registry.go:206` under `r.mu` and at `entry_acp.go:MarkUsed:78` without any lock. `registry.go:358` reads `e.LastUsed` without `r.mu`. Under `-race`, this is a data race.

**Bug site** (`internal/session/entry_acp.go:77-79`):
```go
func (e *Entry) MarkUsed() {
    e.LastUsed = time.Now()   // ← no mutex; concurrent read at registry.go:358
}
```

**Bug site** (`internal/session/registry.go:358`):
```go
r.cfg.Logger.Warn("session: subprocess exited unexpectedly",
    "sid", sid,
    "idle_for", time.Since(e.LastUsed))  // ← read without r.mu
```

**Fix pattern — use `sync/atomic` or guard with the entry's own mutex.** The closest existing pattern in the codebase is `p.mu.Lock()` wrapping `e.LastUsed = time.Now()` in `registry.go:206`. Options:
1. Convert `Entry.LastUsed` to `atomic.Int64` (UnixNano), matching the D-05a `last_progress_at` field added by the same plan — consistent atomic integer pattern.
2. Require `r.mu.Lock()` on all `LastUsed` writes and reads.

**Preferred (atomic)** — matches D-05a's `last_progress_at` which uses `atomic.Int64`. Change `Entry.LastUsed time.Time` to `lastUsedNs atomic.Int64`, expose `LastUsed() time.Time` accessor, and update all call sites.

**Analog (atomic pool field in same codebase):** Plan 16-01 is adding `atomic.Int64` for `last_progress_at` in `internal/pool/pool.go` — use that pattern. The `atomic.Int64` approach compiles without any import beyond `sync/atomic` and is already established by `s.startedAt.Load()` in `cmd/otto-tray/tray.go:215`.

**Unskip:** No dedicated regression file for P-5 listed separately; the race is caught by `go test -race ./...`. The `regression_rel_pool_04_test.go` file covers the broader pool liveness concern; success criterion #1 for Phase 16 is `-race` clean tree-wide.

---

#### P-6 (REL-POOL-06): `internal/acp/pool_pgid_windows.go` — Windows process-tree kill

**Finding:** `killProcessGroup` at `:21` returns `nil` unconditionally on Windows — kiro-cli child processes are orphaned on gateway shutdown on Windows.

**Bug site** (`internal/acp/pool_pgid_windows.go:15-21`):
```go
func applyPgidAttr(cmd *exec.Cmd) {}

func killProcessGroup(pid int, sig syscall.Signal) error { return nil }
```

**Unix analog to copy** (`internal/acp/pool_pgid_unix.go:28-52`):
```go
//go:build darwin || linux

func applyPgidAttr(cmd *exec.Cmd) {
    if cmd == nil {
        return
    }
    if cmd.SysProcAttr == nil {
        cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
        return
    }
    cmd.SysProcAttr.Setpgid = true
}

func killProcessGroup(pid int, sig syscall.Signal) error {
    if err := syscall.Kill(-pid, sig); err != nil {
        return fmt.Errorf("acp.pool.pgid: kill pgroup: %w", err)
    }
    return nil
}
```

**Windows implementation pattern** — replace the stubs with job-object or `taskkill /T /F`. Two concrete approaches:

**Option A (taskkill — no syscall, simpler):**
```go
//go:build windows

func applyPgidAttr(cmd *exec.Cmd) {} // job object assigned after spawn

func killProcessGroup(pid int, _ syscall.Signal) error {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    cmd := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)) //nolint:gosec // args are static flags + integer pid
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("acp.pool.pgid: taskkill /T /F: %w", err)
    }
    return nil
}
```

**Option B (Job Object — pure syscall, preferred by Go style):** Use `golang.org/x/sys/windows` `CreateJobObject` + `AssignProcessToJobObject` + `TerminateJobObject`. The pattern for `windows.OpenProcess` already exists in `cmd/otto-tray/pidfile_windows.go:18-33` — use the same import and handle-management discipline:
```go
defer func() { _ = windows.CloseHandle(h) }()
```

**gosec annotation:** Both options require `//nolint:gosec // args are static flags + integer pid` on the `exec.CommandContext` call (matches the pidfile_darwin.go pattern from Phase 15).

**Build tag:** `//go:build windows` at line 1 — already present in the file, keep it.

**Unskip:** `internal/acp/regression_rel_pool_06_test.go` — remove `t.Skip("REL-POOL-06 (P-6): ...")` in the same commit.

---

#### O-1 (REL-CFG-04): `internal/pool/pool.go` — throttled Warn on first park

**Finding:** The acquire select at `pool.go:556-573` parks silently when all slots are busy — no Warn is emitted at default log level, so operators see "gateway silently stopped answering" with zero diagnostic signal.

**Bug site** (`internal/pool/pool.go:556-573`):
```go
select {
case slot = <-p.slots:
    // acquired
case <-ctx.Done():
    return "", fmt.Errorf("pool: acquire cancelled: %w", ctx.Err())
case <-p.closing:
    return "", errors.New("pool: closed")
case <-timeoutC:
    return "", ErrPoolExhausted
}
```

**Existing Warn pattern in the same file** (Phase 15 `acp.ping.escalated_to_close` at `client.go:520-522`):
```go
c.cfg.Logger.Warn("acp.ping.escalated_to_close",
    "err", err)
```

**Fix pattern (per D-discretion):** Emit once per pool generation using a `sync.Once` (reset on `Close()`). Before the acquire select, attempt a non-blocking receive on `p.slots`. If it fails (pool is full), emit the Warn once and then fall through to the blocking select:

```go
// O-1 fix: emit Warn on first park per pool generation (D-discretion).
// Non-blocking try: if this fails, all slots are currently held.
select {
case slot = <-p.slots:
    goto acquired
default:
    // All slots busy — emit one Warn per saturation episode then park.
    p.warnOnce.Do(func() {
        if p.cfg.Logger != nil {
            p.cfg.Logger.Warn("pool: waiting for free slot",
                "busy", len(p.sessionSlots),
                "size", len(p.all))
        }
    })
}
// Blocking acquire (unchanged).
select {
case slot = <-p.slots:
case <-ctx.Done():
    return "", fmt.Errorf("pool: acquire cancelled: %w", ctx.Err())
case <-p.closing:
    return "", errors.New("pool: closed")
case <-timeoutC:
    return "", ErrPoolExhausted
}
acquired:
```

`p.warnOnce sync.Once` is a new field on `Pool`. Reset it in `Pool.Close()` (or in a `resetWarnOnce()` helper called after each exhaustion-to-available transition). The CONTEXT.md D-discretion says "subsequent parks during the same saturation episode emit Debug only" — the `sync.Once` pattern above achieves this because subsequent parks do not re-enter the `Do`.

**Key-value field names:** `"busy"` and `"size"` match the `PoolStats` field names (slog convention: lower-snake matching JSON keys).

**Unskip:** `internal/pool/regression_rel_cfg_04_test.go` — remove `t.Skip("REL-CFG-04 (O-1): ...")` in the same commit. The test already asserts on the `"pool: waiting for free slot"` Warn message.

---

### Plan 16-02: HTTP Surface — H-4, H-5

---

#### H-4 (REL-HTTP-04): `internal/server/server.go` — per-request body-read deadline

**Finding:** Body-reading POST handlers (`/v1/chat/completions`, `/v1/messages`, `/api/chat`, etc.) have no deadline on the `io.ReadAll` / JSON decode phase. A stalled upload parks the handler goroutine indefinitely.

**Existing `shutdownCh` / `RegisterOnShutdown` pattern** (`internal/server/server.go:379-408`) shows the Phase 15 deadline-enforce idiom: close a channel to unblock a goroutine. H-4 uses a similar "close body to unblock reader" approach.

**Config pattern to copy** (`internal/config/config.go:358-368` — `STREAM_IDLE_TIMEOUT_SEC`):
```go
// Non-integer values bubble up from getEnvInt as a wrapped error.
streamIdleTimeoutSec, err := getEnvInt("STREAM_IDLE_TIMEOUT_SEC", 30)
if err != nil {
    errs = append(errs, err)
}
if streamIdleTimeoutSec < 0 {
    errs = append(errs, fmt.Errorf("STREAM_IDLE_TIMEOUT_SEC: must be >= 0, got %d", streamIdleTimeoutSec))
}
```

**New env var `HTTP_BODY_READ_TIMEOUT_SEC`** (D-04): follow the `_SEC` suffix convention. Add to `config.Load()` using `getEnvInt` with default `30`. Validation: `<= 0` is a boot error (D-04 "matches C-1/C-2 fail-fast posture"):
```go
bodyReadTimeoutSec, err := getEnvInt("HTTP_BODY_READ_TIMEOUT_SEC", 30)
if err != nil {
    errs = append(errs, err)
}
if bodyReadTimeoutSec <= 0 {
    errs = append(errs, fmt.Errorf("HTTP_BODY_READ_TIMEOUT_SEC: must be > 0, got %d", bodyReadTimeoutSec))
}
```

**Body-read wrapper pattern** (D-04b — `time.AfterFunc` + `r.Body.Close()`): Wrap body reading in the chat-body POST handlers using a deadline that calls `r.Body.Close()` after `HTTP_BODY_READ_TIMEOUT_SEC`. The `time.AfterFunc` approach avoids modifying `http.Server.ReadTimeout` (which would break SSE response writes). Example seam in handler:

```go
// Fire-and-forget deadline on body read phase only.
bodyTimer := time.AfterFunc(s.cfg.BodyReadTimeout, func() {
    _ = r.Body.Close()  // unblocks json.Decode / io.ReadAll on the handler goroutine
})
defer bodyTimer.Stop()

var req SomeRequestType
if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
    // includes the "use of closed network connection" error on timeout
    http.Error(w, `{"error":{"type":"invalid_request_error","message":"body read timeout"}}`,
        http.StatusRequestEntityTooLarge)
    return
}
bodyTimer.Stop()
```

The `s.cfg.BodyReadTimeout time.Duration` field mirrors `s.cfg.StreamIdleTimeout` already on `server.Config` — follow the same struct-field pattern.

**Apply to:** `/v1/chat/completions`, `/v1/messages`, `/v1/completions`, `/api/chat`, `/api/generate`, `/api/embed`, `/api/embeddings`, `/v1/embeddings` POST handlers only (D-04a). Do NOT apply to admin POSTs.

**PoolStats.Status field** (D-05 — adds `Status string` to `internal/server/health.go`): see Plan 16-01 / 16-04 cross-plan dependency note below.

**Unskip:** `internal/server/regression_rel_http_04_test.go` — remove `t.Skip(...)` in same commit.

---

#### H-5 (REL-HTTP-05): `internal/admin/tail.go` — newline-terminated line cap

**Finding:** The `TailerMaxLineBytes` constant at `tail.go:57` is set to `1024 * 1024` (1 MB) but the truncation check at `:402` only fires when there is no trailing newline (`!strings.HasSuffix(current, "\n")`). A log line that is exactly at the cap and ends with `\n` escapes the cap. Additionally `TailerMaxLineBytes` is not enforced on the SSE fan-out side.

**Existing cap pattern** (`internal/admin/tail.go:395-418`):
```go
func (t *Tailer) readLines(r *bufio.Reader, carry string) (string, error) {
    current := carry
    for {
        chunk, err := r.ReadString('\n')
        if len(chunk) > 0 {
            current += chunk
            if len(current) > TailerMaxLineBytes && !strings.HasSuffix(current, "\n") {
                t.logger.Debug("admin: tailer line exceeds max",
                    "bytes", len(current), "max", TailerMaxLineBytes)
                t.broadcast(current[:TailerMaxLineBytes])
                current = ""
                continue
            }
            if strings.HasSuffix(current, "\n") {
                line := strings.TrimSuffix(current, "\n")
                line = strings.TrimSuffix(line, "\r")
                t.broadcast(line)
                current = ""
            }
        }
        ...
    }
}
```

**Fix pattern:** Remove the `!strings.HasSuffix(current, "\n")` condition from the cap check so the cap enforces on ALL lines regardless of newline termination. Also enforce the cap on the post-newline emit path:
```go
if len(current) > TailerMaxLineBytes && !strings.HasSuffix(current, "\n") {
    // ← change: drop the !HasSuffix condition, or check BOTH paths
```

After the `strings.HasSuffix` branch, add cap enforcement before broadcast:
```go
if strings.HasSuffix(current, "\n") {
    line := strings.TrimSuffix(current, "\n")
    line = strings.TrimSuffix(line, "\r")
    if len(line) > TailerMaxLineBytes {
        t.logger.Debug("admin: tailer line truncated at cap",
            "bytes", len(line), "max", TailerMaxLineBytes)
        line = line[:TailerMaxLineBytes]
    }
    t.broadcast(line)
    current = ""
}
```

**Constant rename (optional):** CONTEXT.md mentions `TailerMaxLineBytes` enforced via `"TailerMaxLineBytes"` — if the planner adds a new `SSEMaxLineBytes` exported constant or renames, follow the existing `TailerSubChanBuffer = 16` / `TailerMaxLineBytes = 1024*1024` naming style at `:50-57`.

**Unskip:** `internal/admin/regression_rel_http_05_test.go` — remove `t.Skip(...)` in same commit.

---

### Cross-plan dependency: `internal/server/health.go` — `PoolStats.Status` (D-05, owned by Plan 16-02)

**Current `PoolStats`** (`internal/server/health.go:28-35`):
```go
type PoolStats struct {
    Size  int `json:"size"`
    Alive int `json:"alive"`
    Busy  int `json:"busy"`
}
```

**Fix pattern (D-05):** Add `Status string` field:
```go
type PoolStats struct {
    Size   int    `json:"size"`
    Alive  int    `json:"alive"`
    Busy   int    `json:"busy"`
    Status string `json:"status"`  // "ok" | "degraded" | "exhausted" | "unknown"
}
```

`Status` computation lives in `healthHandler` at `:61-84`. The server calls `s.pool.Stats()` which returns `pool.Stats{Size, Alive, Busy}`. Status policy (D-05a/D-05b):
- `"exhausted"`: `pool.IsExhausted()` helper (new — check if all slots are busy AND an acquire-timeout has recently fired)
- `"degraded"`: `pool.Stats().Busy == pool.Stats().Alive && pool.Stats().Busy == pool.Stats().Size && time.Since(pool.LastProgressAt()) > 30*time.Second`
- `"ok"`: default

`pool.LastProgressAt() time.Time` is a new method on `*pool.Pool` exposing an `atomic.Int64` (UnixNano) that is advanced on: every streamed chunk emit, every ping ack, every slot release. Plan 16-01 owns plumbing it into `internal/pool/pool.go` and exposing the API; Plan 16-02 owns calling it in `healthHandler`.

**Pattern for `pool.LastProgressAt()`** — follows the `atomic.Int64` pattern established by `s.startedAt.Store(&now)` in `cmd/otto-tray/tray.go:225`:
```go
// In pool.Pool struct:
lastProgressAt atomic.Int64  // UnixNano; advanced on chunk, ping ack, slot release

// Accessor:
func (p *Pool) LastProgressAt() time.Time {
    return time.Unix(0, p.lastProgressAt.Load())
}
```

**Plan 16-04 (Tray T-5)** consumes `PoolStats.Status` directly from the `/health` JSON. It reads `snap.Pool.Status` (after Phase 16-04 adds the field to `Snapshot` in `status.go`). Plan 16-04 depends on Plan 16-02 landing first.

---

### Plan 16-03: Hooks — G-1

---

#### G-1 (REL-HOOKS-01): `internal/engine/collect.go` + `internal/adapter/anthropic/collect.go` — PostHook on error paths

**Finding:** `engine.Collect` returns early at `:165` (idle-timeout) and `:171` (Result error) BEFORE the PostHook loop at `:187-191`. `anthropic.CollectAnthropicChat` returns early at `:177-179` (idle-timeout) and `:184` (Result error) BEFORE `RunPostHooks` at `:207-209`. `LoggingHook.startTimes` and `ChatTraceHook.startTimes` entries leak on every error path.

**Bug site** (`internal/engine/collect.go:165-171`):
```go
if loopErr != nil {
    if errors.Is(loopErr, canonical.ErrStreamIdleTimeout) {
        e.cfg.ACP.Cancel(run.sessionID)
        return nil, fmt.Errorf("engine: collect: %w", rangeErr)  // ← returns before PostHook at :187
    }
    return nil, loopErr  // ← returns before PostHook at :187
}
final, rerr := run.stream.Result()
if rerr != nil {
    e.cfg.ACP.Cancel(run.sessionID)
    return nil, fmt.Errorf("engine: collect result: %w", rerr)  // ← returns before PostHook at :187
}
```

**Existing PostHook traversal pattern** (`internal/engine/collect.go:187-193`):
```go
for _, h := range e.cfg.PostHooks {
    if hookErr := e.callPostHookSafe(ctx, h, req, resp); hookErr != nil {
        return nil, fmt.Errorf("engine: posthook: %w", hookErr)
    }
}
return resp, nil
```

**Fix pattern — call PostHooks with a nil resp on error paths.** Mirror the non-streaming Anthropic path (which already runs `RunPostHooks` on the happy path). On the error paths, call `callPostHookSafe` before returning:
```go
if loopErr != nil {
    if errors.Is(loopErr, canonical.ErrStreamIdleTimeout) {
        e.cfg.ACP.Cancel(run.sessionID)
    }
    // G-1 fix: run PostHooks even on error so startTimes entries are cleared.
    for _, h := range e.cfg.PostHooks {
        _ = e.callPostHookSafe(ctx, h, req, nil)  // nil resp — hook must tolerate it
    }
    return nil, fmt.Errorf("engine: collect: %w", loopErr)
}
```

**Same fix in `internal/adapter/anthropic/collect.go`:** at the `return nil, loopErr` (`:177-179`) and `return nil, fmt.Errorf("anthropic: collect result: ...")` (`:184`) sites, call `eng.RunPostHooks(ctx, req, nil)` before return.

**`LoggingHook.After` nil-resp guard:** `LoggingHook.After` (in `internal/plugin/logging.go`) must handle `resp == nil` gracefully (no panic on `resp.StopReason`). Check the current `After` implementation — if it dereferences `resp`, add a nil guard:
```go
func (h *LoggingHook) After(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
    start, _ := h.startTimes.LoadAndDelete(plugin.RequestIDFromContext(ctx))
    // nil resp is valid on error paths (G-1 fix) — still reclaim the startTimes entry.
    if resp == nil {
        return nil
    }
    ...
}
```

**ChatTraceHook** (`internal/plugin/trace.go`) has the same `startTimes sync.Map` leak — apply the same `LoadAndDelete` guard for nil resp.

**Unskip:** `internal/plugin/regression_rel_hooks_01_test.go` — remove `t.Skip("REL-HOOKS-01 (G-1): ...")` in the same commit. The test already exercises `hook.startTimes.Load` directly.

---

### Plan 16-04: Tray — T-4, T-5, T-6, T-7

---

#### T-4 (REL-TRAY-04): `cmd/otto-tray/uihelpers_windows.go` — non-blocking notify

**Finding:** `notify()` at `:74-94` runs a blocking PowerShell `MessageBox` synchronously on the uiLoop goroutine. When `StateRunning → StateError/StateStopped` fires, `applyState` at `tray.go:207-209` blocks the uiLoop for up to 30s (MessageBox waits for user click), freezing polling and menu updates.

**Current `notify` pattern** (`cmd/otto-tray/uihelpers_windows.go:74-94`):
```go
func notify(title, body string) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    script := "..."
    cmd := exec.CommandContext(ctx, powershellExe(), "-NoProfile", "-Command", script)
    cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
    _ = cmd.Run()  // ← blocks for up to 30s
}
```

**`applyState` caller** (`cmd/otto-tray/tray.go:207-209`):
```go
if prev == StateRunning && (out.State == StateError || out.State == StateStopped) {
    notify("OTTO Gateway", fmt.Sprintf("Gateway is %s", out.State))
}
```

**Fix pattern (D-discretion):** Introduce a package-level `notifyFn` variable (testability seam required by regression test) and dispatch notify off-uiLoop with bounded retry:
```go
// uihelpers.go (non-platform common):
var notifyFn = notifyImpl  // swappable in tests

// tray.go: replace direct notify() call with:
if prev == StateRunning && (out.State == StateError || out.State == StateStopped) {
    title := "OTTO Gateway"
    body := fmt.Sprintf("Gateway is %s", out.State)
    go func() {
        for i := 0; i < 3; i++ {  // max 3 attempts, 500ms backoff (D-discretion)
            notifyFn(title, body)
            // If notify succeeded (or always on Windows MessageBox which is blocking)
            // break after first attempt.
            break
        }
    }()
}
```

**Apply to both platforms (D-discretion):** even though T-4 is Windows-specific in the review, the non-blocking-notify pattern lands on `uihelpers_darwin.go` too for symmetry (one-line cost). On Darwin, `notify` already uses a best-effort fire-and-forget approach (NSUserNotification via subprocess) so the change is minimal.

**Test seam:** `regression_rel_tray_04_test.go` at `:44-50` documents the required injection:
```go
// oldNotify := notifyFn
// notifyFn = func(title, body string) { <-blockGate }
// defer func() { notifyFn = oldNotify }()
```
This means `notifyFn` must be an exported-or-accessible package-level var. In the `main` package it can be an unexported package-level `var notifyFn = notify`.

**Unskip:** `cmd/otto-tray/regression_rel_tray_04_test.go` — remove `t.Skip("REL-TRAY-04 (T-4): ...")` in same commit.

---

#### T-5 (REL-TRAY-05): `cmd/otto-tray/tray.go` + `internal/server/health.go` — status probe consumes pool.status enum

**Finding:** `tray.go:153` (now `:152` in makeProbe) swallows snapshot errors: `snap, _ = client.snapshot()`. When `/health` returns an error, `snap` is zero-value — `PoolSize=0` → degraded check skipped → `StateRunning` despite wedged pool. Additionally, the `Busy==Size` wedged-but-alive scenario is not a distinct `StateDegraded` signal.

**Bug site** (`cmd/otto-tray/tray.go:155-156` approximately):
```go
ok := client.healthOK()
if !ok {
    return true, false, Snapshot{}
}
snap, _ := client.snapshot()  // ← error swallowed; zero-value snap on failure
```

**Fix pattern (D-05):** Consume `PoolStats.Status` enum from `/health` JSON (added by Plan 16-02). Update `Snapshot` in `cmd/otto-tray/status.go` to include `Pool.Status string`. Update `makeProbe` to surface snapshot errors:
```go
snap, err := client.snapshot()
if err != nil {
    // Cannot get pool status — treat as unknown/degraded probe.
    return true, false, Snapshot{}
    // OR: return true, true, Snapshot{} with a synthetic "unknown" status
}
```

**FSM extension** (`cmd/otto-tray/fsm.go:52-54`):
```go
// Current (only fires on Alive==0):
if in.Snapshot.PoolSize > 0 && in.Snapshot.PoolAlive == 0 {
    return stateOutput{State: StateDegraded, Detail: "pool empty"}
}

// Add: also fire on status=="degraded" from /health:
if in.Snapshot.Pool.Status == "degraded" {
    return stateOutput{State: StateDegraded, Detail: "pool stalled"}
}
if in.Snapshot.Pool.Status == "exhausted" {
    return stateOutput{State: StateDegraded, Detail: "pool exhausted"}
}
```

**`status.go` Snapshot extension:** Add `Pool struct { Status string }` to `Snapshot` (mirrors the `/health` JSON `pool.status` field added by Plan 16-02).

**Unskip:** `cmd/otto-tray/regression_rel_tray_05_test.go` — remove `t.Skip("REL-TRAY-05 (T-5): ...")` in same commit.

---

#### T-6 (REL-TRAY-06): `scripts/otto-gw.ps1` — bundle path parses last non-empty line

**Finding:** `tray.go:304` does `strings.TrimSpace(res.Stdout)`. If the PS1 wrapper's `Initialize-Config` emits `Write-Host` chatter before the archive path, the full stdout (chatter + path) is passed to `revealBundle` — path lookup fails.

**Current pattern** (`cmd/otto-tray/tray.go:304-307`):
```go
path := strings.TrimSpace(res.Stdout)
if path == "" {
    path = filepath.Join(s.installRoot, "support", "latest"+bundleExt())
}
```

**Fix pattern — two parts:**

Part A: Go-side `tray.go` — parse last non-empty line:
```go
// T-6 fix: wrapper may emit informational Write-Host lines before the
// archive path. Take the last non-empty stdout line (the archive path).
lastLine := ""
for _, line := range strings.Split(res.Stdout, "\n") {
    if strings.TrimSpace(line) != "" {
        lastLine = strings.TrimSpace(line)
    }
}
path := lastLine
if path == "" {
    path = filepath.Join(s.installRoot, "support", "latest"+bundleExt())
}
```

Part B: PS1-side `scripts/otto-gw.ps1` — ensure bundle archive path is the LAST non-empty stdout line (write informational output to stderr, not stdout):
```powershell
# T-6 fix: informational output goes to stderr so stdout is clean for the archive path.
Write-Host "Creating support bundle..." -ForegroundColor Cyan 2>&1 | Out-Null  # move to stderr:
Write-Error "Creating support bundle..."  # OR redirect Write-Host to stderr
# Archive path emitted to stdout (only line):
Write-Output $archivePath
```

**Unskip:** `cmd/otto-tray/regression_rel_tray_06_test.go` remains a permanent `t.Skip` manual stub (per Phase 15 T-2/T-3 pattern). The fix lives in PS1 + Go-side path parsing; automated test verification is Windows-only manual (`tests/reliability/manual/REL-TRAY-06-repro.ps1`).

---

#### T-7 (REL-TRAY-07): `scripts/otto-gw.ps1` (and `scripts/otto-gw`) — bundle size/time bounds + cleanup

**Finding:** Live log copies in the support bundle script bypass the `--max-mb` cap loop (only rotated `.log.gz` files are trimmed). No overall timeout with staging cleanup.

**Fix pattern (D-discretion):**

- `--max-mb` default **512MB**; verb timeout default **180s** (both CLI-overridable per D-discretion).
- Apply the cap to ALL collected files including live logs.
- Add `defer os.RemoveAll(stagingDir)` analog in PS1: `try { ... } finally { Remove-Item $stagingDir -Recurse -Force -ErrorAction SilentlyContinue }`.
- Progress to stderr (not stdout) so the archive path remains the sole stdout line.

**PS1 timeout pattern** (mirrors the Phase 15 `runWrapper` pattern in `cmd/otto-tray/runner.go:29-33` which uses `context.WithTimeout`):
```powershell
# T-7 fix: overall timeout with staging cleanup.
$job = Start-Job -ScriptBlock { ... }
if (-not (Wait-Job $job -Timeout $timeoutSec)) {
    Stop-Job $job
    Remove-Item $stagingDir -Recurse -Force -ErrorAction SilentlyContinue
    Write-Error "support bundle: timed out after $timeoutSec seconds; staging cleaned"
    exit 1
}
```

**Bash analog** (`scripts/otto-gw:1864-1873`) — same cap enforcement: all files including live logs pass through the size-check loop before archiving.

**Unskip:** `cmd/otto-tray/regression_rel_tray_07_test.go` — remove `t.Skip("REL-TRAY-07 (T-7): ...")` in same commit (the test body documents the assertion shape against a fake large log dir).

---

### Plan 16-05: Config — C-1, C-2, C-3

---

#### C-1 (REL-CFG-01): `internal/config/config.go` — fail-fast on negative/zero pool/session/trace vars

**Finding:** `POOL_SIZE`, `SESSION_MAX`, `SESSION_TTL_MS`, `SESSION_TICK_INTERVAL_MS`, `CHAT_TRACE_MAX_AGE_DAYS` are silently coerced when negative/zero (by `pool.Config.applyDefaults` and `session.Config.applyDefaults`). No named boot error.

**Closest analog — `STREAM_IDLE_TIMEOUT_SEC` pattern** (`internal/config/config.go:362-368`):
```go
streamIdleTimeoutSec, err := getEnvInt("STREAM_IDLE_TIMEOUT_SEC", 30)
if err != nil {
    errs = append(errs, err)
}
if streamIdleTimeoutSec < 0 {
    errs = append(errs, fmt.Errorf("STREAM_IDLE_TIMEOUT_SEC: must be >= 0, got %d", streamIdleTimeoutSec))
}
```

**Fix pattern — copy this block verbatim for each of the 5 vars**, adjusting the constraint message. Per D-discretion: `POOL_SIZE` error fires on `<= 0`; upper bound sanity cap at **256** (also a boot error: `> 256`):
```go
// C-1 fix: fail-fast for POOL_SIZE
if poolSize <= 0 {
    errs = append(errs, fmt.Errorf("POOL_SIZE: must be >= 1, got %d", poolSize))
}
if poolSize > 256 {
    errs = append(errs, fmt.Errorf("POOL_SIZE: sanity cap exceeded (max 256), got %d", poolSize))
}

// C-1 fix: SESSION_MAX, SESSION_TTL_MS, SESSION_TICK_INTERVAL_MS, CHAT_TRACE_MAX_AGE_DAYS
// All follow "must be >= 0" pattern (zero may be meaningful for some; planner confirms).
if sessionMax <= 0 {
    errs = append(errs, fmt.Errorf("SESSION_MAX: must be >= 1, got %d", sessionMax))
}
// SESSION_TTL_MS / SESSION_TICK_INTERVAL_MS are time.Duration — check <= 0:
if sessionTTL <= 0 {
    errs = append(errs, fmt.Errorf("SESSION_TTL_MS: must be > 0, got %v", sessionTTL))
}
if sessionTickInterval <= 0 {
    errs = append(errs, fmt.Errorf("SESSION_TICK_INTERVAL_MS: must be > 0, got %v", sessionTickInterval))
}
if chatTraceMaxAgeDays <= 0 {
    errs = append(errs, fmt.Errorf("CHAT_TRACE_MAX_AGE_DAYS: must be >= 1, got %d", chatTraceMaxAgeDays))
}
```

**Placement:** Insert immediately after each `getEnvInt` / `getEnvDuration` call — same placement as `STREAM_IDLE_TIMEOUT_SEC`'s sign check at `:366-368`.

**Regression test:** `internal/config/regression_rel_cfg_01_test.go` — the test already uses `"must be >= 0"` as the expected message fragment. Match that string exactly in the error message.

**Unskip:** `internal/config/regression_rel_cfg_01_test.go` — remove `t.Skip("REL-CFG-01 (C-1): ...")`.

---

#### C-2 (REL-CFG-02): `internal/config/config.go` — `PING_INTERVAL <= 0` boot error

**Finding:** `PING_INTERVAL` is parsed by `getEnvDuration("PING_INTERVAL", 60*time.Second)` at `:295`. Negative and zero durations are syntactically valid — they pass through `config.Load()` and reach `time.NewTicker` in `acp/client.go:509` which panics with "non-positive interval for NewTicker".

**Bug site** (`internal/config/config.go:295-298`):
```go
pingInterval, err := getEnvDuration("PING_INTERVAL", 60*time.Second)
if err != nil {
    errs = append(errs, err)
}
// ← no sign check follows; negative/zero passes through
```

**ACP client fallback** (`internal/acp/client.go:59`) only fills the default when `PingInterval == 0` (exactly), so `-1m` passes through to `time.NewTicker`.

**Fix pattern — same as C-1 `STREAM_IDLE_TIMEOUT_SEC`** but with `> 0` constraint (pings cannot be zero):
```go
pingInterval, err := getEnvDuration("PING_INTERVAL", 60*time.Second)
if err != nil {
    errs = append(errs, err)
}
if pingInterval <= 0 {
    errs = append(errs, fmt.Errorf("PING_INTERVAL: must be > 0, got %v", pingInterval))
}
```

**Placement:** Immediately after `:297-298` (the existing `err` append). Matches the regression test assertion: `strings.Contains(err.Error(), "PING_INTERVAL")` and `strings.Contains(err.Error(), "must be > 0")`.

**D-discretion (defensive panic in ACP client):** The `time.NewTicker` call in `acp/client.go:509` keeps its `defer recover()` posture so any future regression lands in slog rather than crashing silently. No change needed to client.go if the config boot-error fires before spawn.

**Unskip:** `internal/config/regression_rel_cfg_02_test.go` — remove `t.Skip("REL-CFG-02 (C-2): ...")`.

---

#### C-3 (REL-CFG-03): `internal/config/config.go` + `cmd/otto-gateway/main.go` + `CLAUDE.md` — stub + boot WARN + doc-fix

**Finding:** `EMBEDDING_MODEL_DEFAULT` is documented in `CLAUDE.md`'s backward-compat env list but is never read anywhere in `internal/` or `cmd/`. Operators setting it get no feedback.

**Boot WARN pattern** (`cmd/otto-gateway/main.go`) — copy from existing boot-WARN discipline. Phase 15 SUMMARY-01 shows the pattern for boot-time Warn:
```go
// From acp/client.go:520-522 (same logger/level posture):
c.cfg.Logger.Warn("acp.ping.escalated_to_close", "err", err)
```

**Fix for `main.go`** (D-03 spec):
```go
// C-3 fix (D-03): emit one WARN if EMBEDDING_MODEL_DEFAULT is set.
// The embedding surface is not implemented in v1.9 (docs/briefs/go_port_brief.md §3.4).
if v := os.Getenv("EMBEDDING_MODEL_DEFAULT"); v != "" {
    logger.Warn("embedding surface is not implemented; EMBEDDING_MODEL_DEFAULT will be ignored",
        "value", v)
}
```

**Placement in `main.go`:** Before any goroutine starts (per D-discretion C-2 validation note: "validate in config.Load BEFORE any goroutine starts"). The embedding WARN is NOT a boot error (D-03 "no fail-fast") — it is a one-shot informational Warn after successful `config.Load()`.

**`CLAUDE.md` doc-fix** (single line edit in "Backward compat" env-var list):
- Change: `EMBEDDING_MODEL_DEFAULT`
- To: `EMBEDDING_MODEL_DEFAULT (reserved, not yet implemented)`

**`CLAUDE.md` addition for H-4:** Add `HTTP_BODY_READ_TIMEOUT_SEC (net-new in v1.9)` to the same backward-compat list.

**Regression test** (`internal/config/regression_rel_cfg_03_test.go`) — verify that `EMBEDDING_MODEL_DEFAULT` does NOT cause a boot error (Load() succeeds):
```go
// Unskip and add assertion:
t.Setenv("EMBEDDING_MODEL_DEFAULT", "all-MiniLM-L6-v2")
_, err := config.Load()
if err != nil {
    t.Fatalf("EMBEDDING_MODEL_DEFAULT should not cause a boot error, got: %v", err)
}
// The WARN is emitted by main.go at boot, not by config.Load() — test only
// verifies Load() succeeds.
```

**Unskip:** `internal/config/regression_rel_cfg_03_test.go` — remove `t.Skip("REL-CFG-03 (C-3): ...")`.

---

## Shared Patterns

### Unskip-in-same-commit (Phase 14 D-12/D-13, Phase 15 D-02/D-03)
**Source:** All Phase 14 regression test files (`internal/pool/regression_rel_pool_04_test.go:41`, `internal/config/regression_rel_cfg_01_test.go:33`, etc.)
**Apply to:** All 14 regression test files — remove the `t.Skip("REL-*: regression test — unskip in Phase 16 fix commit")` line in the SAME atomic commit as the production source change.
**Pattern:**
```go
// BEFORE:
func TestRegression_REL_POOL_04_... (t *testing.T) {
    t.Skip("REL-POOL-04 (P-4): regression test — unskip in Phase 16 fix commit")
    ...
}

// AFTER (remove t.Skip entirely; flip pre-fix assertions to post-fix assertions):
func TestRegression_REL_POOL_04_... (t *testing.T) {
    defer goleak.VerifyNone(t)
    ...
}
```

### Negative/zero env-var validation (STREAM_IDLE_TIMEOUT_SEC posture)
**Source:** `internal/config/config.go:362-368`
**Apply to:** C-1 (POOL_SIZE, SESSION_MAX, SESSION_TTL_MS, SESSION_TICK_INTERVAL_MS, CHAT_TRACE_MAX_AGE_DAYS); C-2 (PING_INTERVAL); D-04 (HTTP_BODY_READ_TIMEOUT_SEC)
```go
if someVar <= 0 {
    errs = append(errs, fmt.Errorf("SOME_VAR: must be > 0, got %d", someVar))
}
```
Error accumulation via `errs = append(errs, ...)` and `errors.Join(errs...)` at `:502-504` — emit one error per bad var, all collected before return.

### Structured slog Warn
**Source:** `internal/acp/client.go:520-522`, `internal/adapter/openai/sse.go:463-469` (Phase 15)
**Apply to:** O-1 pool exhaustion Warn; C-3 embedding WARN; P-4 ping escalation log
```go
logger.Warn("event.dot.notation",
    "key1", val1,
    "key2", val2,
)
```
Key-value pairs only; no `fmt.Sprintf` in the message string; event names use dot notation.

### gosec G204 nolint annotation
**Source:** `cmd/otto-tray/uihelpers_darwin.go:24` — `//nolint:gosec // url is operator-configured`; `cmd/otto-tray/pidfile_darwin.go` — `//nolint:gosec // args are static strings + strconv.Itoa(int)` (Phase 15)
**Apply to:** P-6 `taskkill` exec.CommandContext call if Option A chosen
```go
cmd := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)) //nolint:gosec // args are static flags + integer pid
```

### Platform build tags
**Source:** `internal/acp/pool_pgid_unix.go:1` (`//go:build darwin || linux`), `internal/acp/pool_pgid_windows.go:1` (`//go:build windows`)
**Apply to:** P-6 Windows implementation — keep existing `//go:build windows` tag at line 1 of `pool_pgid_windows.go`

### sync.Once per pool generation
**Source:** `internal/pool/pool.go` closeOnce at `acp/client.go:1138` (`c.closeOnce.Do(func() {...})`); `cmd/otto-tray/tray.go:215` (`s.startedAt.Store`)
**Apply to:** O-1 `p.warnOnce sync.Once` field — reset on `Pool.Close()` via reassignment or a dedicated reset helper

### atomic.Int64 for shared timestamps
**Source:** `cmd/otto-tray/tray.go:215-225` (`s.startedAt atomic.Pointer[time.Time]`)
**Apply to:** D-05a `last_progress_at atomic.Int64` (UnixNano) in `internal/pool/pool.go`; P-5 `Entry.LastUsed` conversion to atomic
```go
var ts atomic.Int64
ts.Store(time.Now().UnixNano())
// Accessor:
time.Unix(0, ts.Load())
```

### pscustomobject return (Phase 15 T-2 pattern)
**Source:** `scripts/otto-gw.ps1` `Get-GatewayStatus` — Phase 15 replaced `exit 1` with `return [pscustomobject]@{ Status = 'stopped'; Message = '...' }` (15-03-SUMMARY.md:126-130)
**Apply to:** T-6 / T-7 PS1 wrapper — any new output should go to stderr (`Write-Error` or `2>&1` redirect); only the archive path goes to stdout

### Platform-symmetric helpers
**Source:** Phase 15 decision: "non-blocking-notify pattern lands on `uihelpers_darwin.go` too for symmetry. Cost is one line" (15-CONTEXT.md D-discretion)
**Apply to:** T-4 — add `notifyFn` var and goroutine dispatch to BOTH `uihelpers_windows.go` AND `uihelpers_darwin.go`

---

## Cross-Plan Data Flow Map

Three plans chain for D-05 (T-5 tray probe):

```
Plan 16-01 (pool.go)
  └── adds pool.LastProgressAt() atomic.Int64 API
       └── Plan 16-02 (health.go)
             └── adds PoolStats.Status string field
                  └── Plan 16-04 (tray.go + status.go)
                        └── consumes pool.status enum via /health JSON
```

Plans 16-03 (Hooks) and 16-05 (Config) are independent of this chain.

| Plan | depends_on |
|---|---|
| 16-01 | — (Wave 1) |
| 16-02 | — (Wave 1) |
| 16-03 | — (Wave 1) |
| 16-05 | — (Wave 1) |
| 16-04 | 16-01, 16-02 (Wave 2) |

**`files_modified` disjoint check:**
- `internal/pool/pool.go` — Plan 16-01 only (G-1 is in adapters/engine, not pool.go)
- `internal/server/health.go` — Plan 16-02 only (Plan 16-04 reads via /health JSON, does not modify the server file)
- `internal/acp/stream.go` — Plan 16-01 only
- `internal/engine/collect.go` — Plan 16-03 only
- `internal/adapter/anthropic/collect.go` — Plan 16-03 only
- `internal/config/config.go` — Plan 16-05 only

---

## No Analog Found

All files have analogs (self-extensions or sibling-surface analogs). The only truly novel patterns (no codebase precedent) are:

| File / Pattern | Why novel |
|---|---|
| `internal/acp/pool_pgid_windows.go` — job object or taskkill | No Windows process-tree kill exists in the codebase today; stub→impl transition |
| `internal/pool/pool.go` — `sync.Once` reset per pool generation (O-1) | No per-generation Once reset anywhere in pool.go today |
| `internal/server/health.go` — `PoolStats.Status` enum computation from `pool.LastProgressAt()` | First status-enum derived from an atomic timestamp in this codebase |
| `cmd/otto-tray/uihelpers_windows.go` — `notifyFn` package-level var seam | No existing testability seam on notify; Phase 15 dispatched notify directly |

For these, CONTEXT.md D-discretion patterns (reproduced in Pattern Assignments above) are the reference.

---

## Metadata

**Analog search scope:** `internal/pool/`, `internal/acp/`, `internal/server/`, `internal/admin/`, `internal/engine/`, `internal/adapter/anthropic/`, `internal/plugin/`, `internal/config/`, `internal/session/`, `cmd/otto-gateway/`, `cmd/otto-tray/`, `scripts/`
**Files read:** 28 source files + 4 Phase 15 planning documents
**Pattern extraction date:** 2026-06-11
