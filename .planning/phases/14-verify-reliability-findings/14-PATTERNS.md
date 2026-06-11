# Phase 14: Verify Reliability Findings — Pattern Map

**Mapped:** 2026-06-11
**Files analyzed:** 23 new Go test files + 23 evidence markdown files + 1 master ledger + ≤4 manual reproducer scripts
**Analogs found:** 21 / 23 Go reproducer tests have strong analogs in tree; 2 (manual-reproducer-only T-3, T-6) need scratch + script

This is a read-only verification phase. The "files modified" set is test files (`t.Skip`'d regression reproducers), per-finding evidence markdown, the master ledger, and a few manual reproducer scripts. No production source edits.

---

## File Classification

| Target test file (to create) | Role | Data flow | Closest analog | Match quality |
|---|---|---|---|---|
| **Plan 14-01 Pool/ACP** | | | | |
| `internal/pool/regression_rel_pool_01_test.go` | regression-test | event-driven (slot lifecycle) | `internal/pool/respawn_ctx_cancel_test.go` | exact |
| `internal/pool/regression_rel_pool_02_test.go` | regression-test | event-driven (signal handling) | `internal/pool/respawn_ctx_cancel_test.go` + `cmd/otto-gateway/main.go` shutdown path | role-match |
| `internal/pool/regression_rel_pool_03_test.go` | regression-test | event-driven (race) | `internal/acp/stream_race_test.go` + `internal/pool/pool_test.go` fakeClient | role-match |
| `internal/pool/regression_rel_pool_04_test.go` | regression-test | streaming (chunk drain) | `internal/pool/pool_test.go` `drainChunks` helper + ping-escalation logic | role-match |
| `internal/session/regression_rel_pool_05_test.go` | regression-test | CRUD (concurrent map) | `internal/session/registry_test.go` (race-sensitive) + `internal/session/reaper_test.go` | exact |
| `tests/reliability/manual/REL-POOL-06-repro.go` + skipped test stub at `internal/acp/regression_rel_pool_06_test.go` | manual-script | OS-specific (Windows pgid) | `internal/acp/pool_pgid_windows.go` (cite target) + `tests/scripts/test-support-bundle.sh` (script shape) | partial — no Windows test infra exists in tree |
| **Plan 14-02 HTTP** | | | | |
| `internal/server/regression_rel_http_01_test.go` | regression-test | streaming (SSE shutdown) | `internal/server/server_test.go` + `internal/admin/sse_test.go` | exact |
| `internal/adapter/openai/regression_rel_http_02_test.go` | regression-test | streaming (idle-timeout) | `internal/adapter/openai/sse_test.go` (`TestSSE_CtxCancel`) | exact |
| `internal/adapter/openai/regression_rel_http_03_test.go` + `internal/adapter/ollama/regression_rel_http_03_test.go` | regression-test | streaming (mid-stream truncation) | `internal/adapter/openai/sse_test.go` + `internal/adapter/ollama/ndjson_test.go` | exact |
| `internal/server/regression_rel_http_04_test.go` | regression-test | request-response (body read deadline) | `internal/server/server_test.go` `newTestServer` + `internal/server/server_admin_test.go` | role-match |
| `internal/admin/regression_rel_http_05_test.go` | regression-test | streaming (line-cap) | `internal/admin/tail_test.go` + `internal/admin/sse_test.go` | exact |
| **Plan 14-03 Tray** | | | | |
| `cmd/otto-tray/regression_rel_tray_01_test.go` | regression-test | OS check (PID identity) | `cmd/otto-tray/pidfile_test.go` (PID round-trip pattern) | partial |
| `cmd/otto-tray/regression_rel_tray_02_test.go` + `tests/reliability/manual/REL-TRAY-02-repro.ps1` | manual-script | Windows PowerShell behavior | `tests/scripts/test-support-bundle.ps1` (script shape) | partial — needs new PS test |
| `cmd/otto-tray/regression_rel_tray_03_test.go` + `tests/reliability/manual/REL-TRAY-03-repro.sh` | manual-script | macOS GUI / LSUIElement | `cmd/otto-tray/autostart_darwin_test.go` (darwin build-tag) | partial — no GUI test infra |
| `cmd/otto-tray/regression_rel_tray_04_test.go` | regression-test | event-driven (uiLoop blocking) | `cmd/otto-tray/fsm_test.go` (computeState pattern) | partial |
| `cmd/otto-tray/regression_rel_tray_05_test.go` | regression-test | request-response (health probe) | `cmd/otto-tray/poller_test.go` (probe + tick + state output) | exact |
| `cmd/otto-tray/regression_rel_tray_06_test.go` + `tests/reliability/manual/REL-TRAY-06-repro.ps1` | manual-script | Windows stdout parsing | `tests/scripts/test-support-bundle.ps1` (script shape) | partial |
| `cmd/otto-tray/regression_rel_tray_07_test.go` | regression-test | file-I/O (bundle bounds) | `tests/scripts/test-support-bundle.sh` (bundle structure asserts) + `cmd/otto-tray/runner_test.go` (wrapper path resolution) | partial |
| **Plan 14-04 Config / Hooks / Obs** | | | | |
| `internal/plugin/regression_rel_hooks_01_test.go` | regression-test | event-driven (PostHook chain on error path) | `internal/plugin/logging_test.go` (captureSlog + decodeRecords) + `internal/plugin/trace_test.go` (readNDJSON) | exact |
| `internal/config/regression_rel_cfg_01_test.go` | regression-test | config validation | `internal/config/config_test.go` lines 615-682 (`TestLoad_StreamIdleTimeoutSec_Negative` pattern) | **exact — direct template** |
| `internal/config/regression_rel_cfg_02_test.go` | regression-test | config validation (PING_INTERVAL) | `internal/config/config_test.go` lines 615-682 | **exact — direct template** |
| `internal/config/regression_rel_cfg_03_test.go` | regression-test | config validation (EMBEDDING_MODEL_DEFAULT warn) | `internal/config/config_test.go` (env-set + Load() + slog capture) + `internal/plugin/logging_test.go` `captureSlog` | role-match |
| `internal/engine/regression_rel_cfg_04_test.go` or `internal/pool/regression_rel_cfg_04_test.go` | regression-test | log line emission (pool exhaustion warn) | `internal/plugin/logging_test.go` `captureSlog` + `internal/pool/pool_test.go` fakeClient gate | role-match |

---

## Pattern Assignments

### Shared building blocks (apply to ALL 9 Critical+High reproducers)

#### `t.Skip` directive shape (CONTEXT.md D-12 — exact format required)
```go
func TestRegression_REL_POOL_01_PoolShrinksToZero(t *testing.T) {
    t.Skip("REL-POOL-01 (P-1): regression test — unskip in Phase 15 fix commit")
    // ... actual reproducer body ...
}
```
All 9 C+H reproducers use this exact `t.Skip()` line. The 4 manual-reproducer cases (T-2, T-3, T-6, P-6) also ship the skipped test stub with the skip reason adjusted to reference the script path.

#### `goleak` gate posture
- Every test package already runs `goleak.VerifyTestMain(m)` in `testmain_test.go`. New regression test files inherit this automatically — no new TestMain needed in any package that already has one.
- Per-test `defer goleak.VerifyNone(t)` is the convention for HTTP/SSE/streaming tests (see `internal/adapter/openai/sse_test.go:26`, `internal/admin/sse_test.go:27`, `internal/server/server_test.go:34`).
- **Apply to:** all 9 C+H reproducers and the 6 Medium goroutine-touching reproducers.

---

### Plan 14-01: Pool / ACP (6 findings)

#### `internal/pool/regression_rel_pool_01_test.go` — REL-POOL-01 / P-1 (Critical)
**Analog:** `internal/pool/respawn_ctx_cancel_test.go` (whole file)
**Pattern signature:** Construct pool with a custom `ctxBlockingFactory` whose `Spawn` blocks on a gate, drive a slot through "warmup → kill → cancel-during-respawn", assert `p.Stats().Size` stays put (this is the v1.8 fix; the v1.9 P-1 reproducer flips the assertion to demonstrate the *pre-fix* shrink behavior on transient errors).
**Code excerpt** (`respawn_ctx_cancel_test.go:54-92`):
```go
func TestPool_RespawnCtxCancel_DoesNotShrinkPool(t *testing.T) {
    t.Parallel()
    fc0 := &fakeClient{}
    fc1 := &fakeClient{}
    cf := &ctxBlockingFactory{clients: []pool.PoolClient{fc0, fc1}, gate: make(chan struct{})}
    p := pool.New(pool.Config{Logger: testutil.Logger(t), Size: 1, Factory: cf})
    warmCtx, warmCancel := context.WithTimeout(context.Background(), time.Second)
    defer warmCancel()
    if err := p.Warmup(warmCtx); err != nil { t.Fatalf("Warmup: %v", err) }
    defer func() { _ = p.Close() }()
    // Kill warmup client → exit_watcher flips slot.dead.
    fc0.fireDone()
```
**Reads first:** `internal/pool/pool_test.go` (lines 48-192 for the `fakeClient` + `fakeClientFactory` harness).

---

#### `internal/pool/regression_rel_pool_02_test.go` — REL-POOL-02 / P-2 (High, Ctrl-C orphans)
**Analog:** `internal/pool/respawn_ctx_cancel_test.go` (factory shape) + `cmd/otto-gateway/main.go` shutdown handler (cite target)
**Pattern signature:** Test that the `defer cleanup()` shutdown path actually runs on SIGINT exit codes. Since this targets `main.go` shutdown wiring, the reproducer drives `pool.Close()` from a goroutine simulating signal handler and asserts pool's `cancelCallList()` for every in-flight session.
**Code excerpt** (use the `fakeClient.cancelCallList()` helper from `pool_test.go:162-168`):
```go
func (f *fakeClient) cancelCallList() []string {
    f.mu.Lock()
    defer f.mu.Unlock()
    out := make([]string, len(f.cancelCalls))
    copy(out, f.cancelCalls)
    return out
}
```
**Reads first:** `cmd/otto-gateway/main.go` (locate the existing signal-handler path that the finding cites).

---

#### `internal/pool/regression_rel_pool_03_test.go` — REL-POOL-03 / P-3 (High, stale activeStream nil)
**Analog:** `internal/acp/stream_race_test.go` (race-narrowing pattern) + `internal/pool/pool_test.go` fakeClient
**Pattern signature:** Two-goroutine race: goroutine A holds a stale `awaitPromptResult` reference, goroutine B acquires the recycled slot. Use `sync.WaitGroup` + tight loop + `-race` flag. The test asserts that the new prompt returns a non-nil result (i.e. the silent empty 200 doesn't happen).
**Code excerpt** (the `fakeClient.promptFn` injection point from `pool_test.go:128-136`):
```go
func (f *fakeClient) Prompt(ctx context.Context, sid string, blocks []canonical.Block) (*acp.Stream, error) {
    if f.promptFn != nil {
        return f.promptFn(ctx, sid, blocks)
    }
    s := acp.NewStreamForTest(sid)
    s.CloseForTest(&acp.FinalResult{StopReason: canonical.StopEndTurn}, nil)
    return s, nil
}
```

---

#### `internal/pool/regression_rel_pool_04_test.go` — REL-POOL-04 / P-4 (Medium, slow consumer starvation)
**Analog:** `internal/pool/pool_test.go` `drainChunks` helper + ping-escalation logic in `internal/acp/client.go`
**Pattern signature:** Stall the chunk consumer (don't drain `<-chan canonical.Chunk`) and assert the readLoop continues to signal liveness independently. The pre-fix reproducer asserts SIGKILL fires; post-fix asserts it does not.
**Code excerpt** (`pool_test.go:35-39`):
```go
func drainChunks(ch <-chan canonical.Chunk) {
    for range ch { //nolint:revive
        _ = struct{}{}
    }
}
```

---

#### `internal/session/regression_rel_pool_05_test.go` — REL-POOL-05 / P-5 (Medium, LastUsed race)
**Analog:** `internal/session/registry_test.go` + `internal/session/reaper_test.go`
**Pattern signature:** Run `Registry.Get` and `MarkUsed` concurrently against the same entry under `-race`. The whole test is for `go test -race ./...` to surface the data race; the assertion is "test passes under -race after fix; before fix it fails with a DATA RACE report."
**Code excerpt** (`reaper_test.go:34-47`):
```go
func TestReaper_ReapsIdleSessionInRealTime(t *testing.T) {
    fc := &fakeClient{}
    ff := &fakeClientFactory{clients: []session.PoolClient{fc}}
    r := session.New(session.Config{
        Logger:       testutil.Logger(t),
        Factory:      ff,
        TTL:          200 * time.Millisecond,
        TickInterval: 50 * time.Millisecond,
        MaxSessions:  32,
    })
    r.Start(context.Background())
    defer func() { _ = r.Close() }()
```

---

#### `tests/reliability/manual/REL-POOL-06-repro.go` + skipped stub `internal/acp/regression_rel_pool_06_test.go` — REL-POOL-06 / P-6 (Medium, Windows pgid no-op)
**Analog:** `internal/acp/pool_pgid_windows.go` (cite target — the file the finding points at)
**Pattern signature:** Manual reproducer + skipped Go stub. Stub is just `t.Skip("REL-POOL-06 (P-6): regression test — see tests/reliability/manual/REL-POOL-06-repro.go for Windows-only reproducer")`. The runnable .go script (under `tests/reliability/manual/`) is a standalone `main` that spawns a kiro-cli child with grandchildren and asserts the kill-group leaves no orphans. **No existing Windows test infrastructure in tree — create from scratch following the script header convention below.**

**Script header convention** (from CONTEXT.md `<specifics>` block — required for all 4 manual reproducers):
```
# Finding ID: P-6
# REL-* ID: REL-POOL-06
# Target phase: 15 (verify) / 16 (fix)
# Target OS: Windows
# Expected pre-fix behavior: grandchild kiro-cli processes survive cmd.Cancel()
# Expected post-fix behavior: full process group dies within 2s of cmd.Cancel()
# Run instructions: ...
```

---

### Plan 14-02: HTTP surface (5 findings)

#### `internal/server/regression_rel_http_01_test.go` — REL-HTTP-01 / H-1 (High, shutdown blocks on SSE)
**Analog:** `internal/server/server_test.go` (`newTestServer` helper) + `internal/admin/sse_test.go`
**Pattern signature:** Open a long-lived admin log-tail SSE via `httptest.NewServer`, then call `srv.Shutdown(ctx)` and assert it returns within < 5s (not the 30s grace ceiling). Pre-fix reproducer asserts shutdown blocks; post-fix asserts it doesn't.
**Code excerpt** (`server_test.go:22-30`):
```go
func newTestServer(t *testing.T) *server.Server {
    t.Helper()
    cfg := config.Config{
        HTTPAddr:     ":0", // port 0 avoids conflicts in tests
        PingInterval: 60 * time.Second,
    }
    logger := testutil.Logger(t)
    return server.New(cfg, logger, version.Version)
}
```

---

#### `internal/adapter/openai/regression_rel_http_02_test.go` — REL-HTTP-02 / H-2 (High, idle-timeout returns hung worker)
**Analog:** `internal/adapter/openai/sse_test.go` `TestSSE_CtxCancel` (lines 25-54)
**Pattern signature:** `fakeRunHandle` + unbuffered `chan canonical.Chunk`, cancel ctx, assert `runSSEEmitter` errors AND that `Cancel(sid)` was called on the fake pool client before the slot returns.
**Code excerpt** (`sse_test.go:25-54`):
```go
func TestSSE_CtxCancel(t *testing.T) {
    defer goleak.VerifyNone(t)
    ch := make(chan canonical.Chunk) // unbuffered, never closed
    defer close(ch)
    runHandle := &fakeRunHandle{
        stream: &fakeStream{
            chunks: ch,
            final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
        },
        sessionID: "session_cancel",
    }
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    rec := httptest.NewRecorder()
    _, err := runSSEEmitter(ctx, rec, runHandle, &canonical.ChatRequest{}, "auto", 0, nullLogger())
```
**Reads first:** `internal/adapter/openai/sse_test.go` (lines 1-80) and `internal/adapter/ollama/ndjson_test.go` (lines 25-60).

---

#### `internal/adapter/openai/regression_rel_http_03_test.go` + `internal/adapter/ollama/regression_rel_http_03_test.go` — REL-HTTP-03 / H-3 (High, mid-stream truncation silent)
**Analog:** `internal/adapter/openai/sse_test.go` + `internal/adapter/ollama/ndjson_test.go` `fakeStream` patterns
**Pattern signature:** `fakeStream` with `result.err = errors.New("worker died")`, drive emitter, assert OpenAI body contains `data: {"error":...}` followed by `[DONE]`; Ollama body contains `done:true,done_reason:"error"`.
**Code excerpt** (`ndjson_test.go:46-55`):
```go
type fakeRunHandle struct {
    stream    *fakeStream
    sessionID string
    scResp    *canonical.ChatResponse
}
func (h *fakeRunHandle) Stream() Stream                                { return h.stream }
func (h *fakeRunHandle) SessionID() string                             { return h.sessionID }
func (h *fakeRunHandle) StopWatchdog() func() bool                     { return func() bool { return true } }
func (h *fakeRunHandle) ShortCircuitResponse() *canonical.ChatResponse { return h.scResp }
```

---

#### `internal/server/regression_rel_http_04_test.go` — REL-HTTP-04 / H-4 (Medium, body read deadline)
**Analog:** `internal/server/server_test.go` + `internal/server/server_admin_test.go`
**Pattern signature:** Slow-write request body via `iotest.HalfReader` or custom stalled `io.Reader`, assert the handler returns within bounded time (not parks for hours). Use `httptest.NewRecorder` + `srv.ServeHTTP`.
**Code excerpt** (use the `newTestServer` helper above and the `httptest.NewRequestWithContext` shape from `server_test.go:38-41`).

---

#### `internal/admin/regression_rel_http_05_test.go` — REL-HTTP-05 / H-5 (Medium, line-cap)
**Analog:** `internal/admin/tail_test.go` (RingBuffer + Tailer + subscriber harness) + `internal/admin/sse_test.go`
**Pattern signature:** Append a single multi-MB newline-terminated line to the tailer source, assert the line emitted via subscriber channel AND via `writeSSELine` is truncated at the per-line cap. Pre-fix: full multi-MB line; post-fix: capped.
**Code excerpt** (`tail_test.go:38-54`):
```go
func waitLines(ch <-chan string, n int, timeout time.Duration) []string {
    deadline := time.After(timeout)
    var result []string
    for i := 0; i < n; i++ {
        select {
        case line, ok := <-ch:
            if !ok { return result }
            result = append(result, line)
        case <-deadline:
            return result
        }
    }
    return result
}
```

---

### Plan 14-03: Tray / wrapper (7 findings)

#### `cmd/otto-tray/regression_rel_tray_01_test.go` — REL-TRAY-01 / T-1 (High, PID identity unchecked)
**Analog:** `cmd/otto-tray/pidfile_test.go` (PID round-trip + processAlive patterns)
**Pattern signature:** Write a pidfile containing `os.Getpid()`, assert the current pidfile-trust path treats it as "the gateway" (pre-fix) even though the process is `go test`. Post-fix: trust check verifies process name/cmdline, rejects the test binary.
**Code excerpt** (`pidfile_test.go:49-71`):
```go
func TestProcessAlive_SelfIsAlive(t *testing.T) {
    if !processAlive(os.Getpid()) {
        t.Fatalf("our own pid (%d) should report alive", os.Getpid())
    }
}
func TestReadPIDFile_RoundTripsOwnPID(t *testing.T) {
    tmp := t.TempDir()
    path := filepath.Join(tmp, "gw.pid")
    if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil { t.Fatal(err) }
```
**Build tag:** All tray tests are `//go:build darwin || windows` — the regression test inherits that.

---

#### `cmd/otto-tray/regression_rel_tray_02_test.go` + `tests/reliability/manual/REL-TRAY-02-repro.ps1` — REL-TRAY-02 / T-2 (High, Windows support bundle `exit 1`)
**Analog:** `tests/scripts/test-support-bundle.ps1` (script shape — read the existing file for header + asserts convention)
**Pattern signature:** Manual PowerShell reproducer. Drive `scripts/otto-gw.ps1 support` with the gateway down; pre-fix: `Get-GatewayStatus`'s `exit 1` aborts the script mid-collection; post-fix: bundle completes with `unreachable:` sentinel in `health/health.json`. The skipped Go stub points at the script.
**No existing Go-side analog** — the failure path is PowerShell-only. Use the manual-script header convention from CONTEXT.md.

---

#### `cmd/otto-tray/regression_rel_tray_03_test.go` + `tests/reliability/manual/REL-TRAY-03-repro.sh` — REL-TRAY-03 / T-3 (High, macOS silent death)
**Analog:** `cmd/otto-tray/autostart_darwin_test.go` (`//go:build darwin` shape)
**Pattern signature:** Manual macOS shell reproducer. Kill the gateway out from under a running tray, observe icon/tooltip in a real GUI session. The skipped Go stub points at the script. **No GUI test infra exists in the tree** — create from scratch.

---

#### `cmd/otto-tray/regression_rel_tray_04_test.go` — REL-TRAY-04 / T-4 (Medium, Windows notify blocking)
**Analog:** `cmd/otto-tray/fsm_test.go` (`computeState` table-driven shape)
**Pattern signature:** Inject a fake `notify` that blocks for 30s, call `applyState`, assert the call returns within < 100ms (post-fix) or hangs (pre-fix). Use a `chan struct{}` gate to simulate the blocking modal.
**Code excerpt** (`fsm_test.go:7-12`):
```go
func TestComputeState_StoppedWhenNoPIDAndNoHealth(t *testing.T) {
    got := computeState(stateInput{PIDAlive: false, HealthOK: false})
    if got.State != StateStopped {
        t.Fatalf("no pid, no health → want %s, got %s", StateStopped, got.State)
    }
}
```

---

#### `cmd/otto-tray/regression_rel_tray_05_test.go` — REL-TRAY-05 / T-5 (Medium, degraded on wedged pool)
**Analog:** `cmd/otto-tray/poller_test.go` `TestPoller_EmitsStateOnEachTick`
**Pattern signature:** **Direct template — match exactly.** Inject a `fakeProbe` whose `Snapshot.PoolAlive == 0 && PoolSize == 4 && PoolBusy == 4` (busy-but-not-serving), assert emitted state is `StateDegraded`. Pre-fix: zero-value-treated-as-healthy; post-fix: degraded-unknown.
**Code excerpt** (`poller_test.go:11-52`):
```go
type fakeProbe struct {
    pidAlive bool
    healthOK bool
    snap     Snapshot
}
func (f *fakeProbe) probe() (bool, bool, Snapshot) { return f.pidAlive, f.healthOK, f.snap }

func TestPoller_EmitsStateOnEachTick(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    probe := &fakeProbe{pidAlive: false, healthOK: false}
    tick := make(chan time.Time, 4)
    out := make(chan stateOutput, 4)
    startedAt := time.Now().Add(-1 * time.Hour)
    go runPoller(ctx, probe.probe, tick, out, func() time.Time { return startedAt })
```

---

#### `cmd/otto-tray/regression_rel_tray_06_test.go` + `tests/reliability/manual/REL-TRAY-06-repro.ps1` — REL-TRAY-06 / T-6 (Medium, Windows bundle path stdout pollution)
**Analog:** `tests/scripts/test-support-bundle.ps1` (script shape) + `cmd/otto-tray/runner_test.go` (wrapper invocation)
**Pattern signature:** Manual PowerShell reproducer — wrapper writes config chatter to stdout before the bundle path; pre-fix: tray's `revealBundle` opens the first stdout line (a log message); post-fix: tray correctly parses the actual archive path.

---

#### `cmd/otto-tray/regression_rel_tray_07_test.go` — REL-TRAY-07 / T-7 (Medium, support bundle bounds)
**Analog:** `tests/scripts/test-support-bundle.sh` (bundle structure asserts) + `cmd/otto-tray/runner_test.go` (wrapper command resolution)
**Pattern signature:** Go test asserts size/time budgets on the bundle output. The `test-support-bundle.sh` excerpt below shows the existing structure-assertion convention to copy. Manual cross-platform variant may also live under `tests/reliability/manual/REL-TRAY-07-repro.sh`.
**Code excerpt** (`tests/scripts/test-support-bundle.sh:14-40`):
```bash
set -euo pipefail
REPO_ROOT="$(cd -P "$(dirname "$0")/../.." >/dev/null 2>&1 && pwd)"
WRAPPER="$REPO_ROOT/scripts/otto-gw"
PASS=0; FAIL=0
FAKE_ROOT=""; EXTRACT_DIR=""
cleanup() {
    [[ -n "$FAKE_ROOT" && -d "$FAKE_ROOT" ]] && rm -rf "$FAKE_ROOT"
    [[ -n "$EXTRACT_DIR" && -d "$EXTRACT_DIR" ]] && rm -rf "$EXTRACT_DIR"
}
trap cleanup EXIT
```

---

### Plan 14-04: Config / Hooks / Observability (5 findings)

#### `internal/plugin/regression_rel_hooks_01_test.go` — REL-HOOKS-01 / G-1 (Medium, PostHook on error paths)
**Analog:** `internal/plugin/logging_test.go` `captureSlog` + `internal/plugin/trace_test.go` `readNDJSON`
**Pattern signature:** Drive a non-streaming aggregation error path (idle-timeout 504, `Result()`-error 500), assert via slog/NDJSON capture that `LoggingHook.Post` ran AND `ChatTraceHook.Post` ran AND no `startTimes` `sync.Map` entry leaked.
**Code excerpt** (`logging_test.go:43-47`):
```go
func captureSlog(_ *testing.T) (*slog.Logger, *bytes.Buffer) {
    buf := &bytes.Buffer{}
    h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
    return slog.New(h), buf
}
```
**Reads first:** `internal/plugin/logging_test.go` (lines 40-80) and `internal/plugin/trace_test.go` (lines 30-60).

---

#### `internal/config/regression_rel_cfg_01_test.go` — REL-CFG-01 / C-1 (Medium, POOL_SIZE etc. negative/zero rejection)
**Analog:** `internal/config/config_test.go` lines 615-682 (`TestLoad_StreamIdleTimeoutSec_Negative` family)
**Pattern signature:** **Direct template — exact match.** One sub-test per env var (POOL_SIZE, SESSION_MAX, SESSION_TTL_MS, SESSION_TICK_INTERVAL_MS, CHAT_TRACE_MAX_AGE_DAYS), each `t.Setenv(NAME, "-5")` → `config.Load()` → assert error mentions the variable name. Pre-fix: most are silently coerced; post-fix: loud boot error.
**Code excerpt** (`config_test.go:655-667`):
```go
func TestLoad_StreamIdleTimeoutSec_Negative(t *testing.T) {
    t.Setenv("STREAM_IDLE_TIMEOUT_SEC", "-5")
    _, err := config.Load()
    if err == nil {
        t.Fatal("Load() should return an error for STREAM_IDLE_TIMEOUT_SEC=-5, got nil")
    }
    if !strings.Contains(err.Error(), "STREAM_IDLE_TIMEOUT_SEC") {
        t.Errorf("error should mention STREAM_IDLE_TIMEOUT_SEC, got: %v", err)
    }
    if !strings.Contains(err.Error(), "must be >= 0") {
        t.Errorf("error should explain the constraint, got: %v", err)
    }
}
```

---

#### `internal/config/regression_rel_cfg_02_test.go` — REL-CFG-02 / C-2 (Medium, PING_INTERVAL panic)
**Analog:** `internal/config/config_test.go` lines 655-667 (same as C-1) — **direct template**
**Pattern signature:** `t.Setenv("PING_INTERVAL", "0s")` → `config.Load()` → assert named error (not `time.NewTicker` panic later). Pre-fix: Load succeeds, ticker panics at runtime; post-fix: Load returns "PING_INTERVAL must be > 0".

---

#### `internal/config/regression_rel_cfg_03_test.go` — REL-CFG-03 / C-3 (Medium, EMBEDDING_MODEL_DEFAULT warn)
**Analog:** `internal/config/config_test.go` env-set pattern + `internal/plugin/logging_test.go` `captureSlog`
**Pattern signature:** `t.Setenv("EMBEDDING_MODEL_DEFAULT", "qwen3-embed")`, intercept startup slog via JSON-handler buffer, assert a `Warn` record mentions the var name.

---

#### `internal/engine/regression_rel_cfg_04_test.go` (or `internal/pool/...`) — REL-CFG-04 / O-1 (Medium, pool exhaustion warn)
**Analog:** `internal/pool/pool_test.go` `fakeClient` gate + `internal/plugin/logging_test.go` `captureSlog`
**Pattern signature:** Build a pool of size 1 with a `fakeClient` whose `Prompt` blocks indefinitely, fire a second concurrent acquire, assert exactly one `Warn("pool: waiting for free slot", ...)` slog record on first park.

---

## Shared Patterns

### Pattern A — `goleak` posture
**Sources:**
- `internal/pool/testmain_test.go` (lines 1-21)
- `internal/acp/testmain_test.go`, `internal/server/testmain_test.go`, `internal/session/testmain_test.go`, `internal/engine/testmain_test.go`, `internal/plugin/testmain_test.go`, `internal/config/testmain_test.go`

**Apply to:** All Plan 14-01/02/04 reproducers. Plan 14-03 (`cmd/otto-tray/`) currently has no `testmain_test.go` with goleak — verify before adding (likely fine without it since tray tests are state-machine / pure-func style).

```go
func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```

### Pattern B — slog capture for log-line assertions
**Source:** `internal/plugin/logging_test.go:43-65`
**Apply to:** REL-HOOKS-01, REL-CFG-03, REL-CFG-04 — any reproducer whose pre/post-fix assertion is "this log line was/wasn't emitted".

```go
func captureSlog(_ *testing.T) (*slog.Logger, *bytes.Buffer) {
    buf := &bytes.Buffer{}
    h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
    return slog.New(h), buf
}

func decodeRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
    t.Helper()
    var out []map[string]any
    for _, line := range strings.Split(buf.String(), "\n") {
        if strings.TrimSpace(line) == "" { continue }
        var rec map[string]any
        if err := json.Unmarshal([]byte(line), &rec); err != nil {
            t.Fatalf("decode slog record %q: %v", line, err)
        }
        out = append(out, rec)
    }
    return out
}
```

### Pattern C — `t.Setenv` + `config.Load()` boot-error assertion
**Source:** `internal/config/config_test.go:655-667` (lines noted above)
**Apply to:** REL-CFG-01, REL-CFG-02, REL-CFG-03 — direct copy/parameterize per env var.

### Pattern D — `fakeClient` + `fakeClientFactory` for pool / session reproducers
**Source:** `internal/pool/pool_test.go:48-192` (fakeClient + fakeClientFactory + ctxBlockingFactory variants) and `internal/session/registry_test.go:23-101` (parallel fakeClient in session package)
**Apply to:** All Plan 14-01 reproducers.

### Pattern E — `httptest.NewRecorder` + `fakeRunHandle` for adapter reproducers
**Sources:**
- `internal/adapter/openai/sse_test.go:25-54` (`TestSSE_CtxCancel`)
- `internal/adapter/ollama/ndjson_test.go:25-60` (`fakeStream` + `fakeRunHandle`)
**Apply to:** REL-HTTP-02, REL-HTTP-03 — direct copy.

### Pattern F — Manual reproducer script header (per CONTEXT.md `<specifics>`)
**Apply to:** All 4 manual scripts (`tests/reliability/manual/REL-{POOL-06,TRAY-02,TRAY-03,TRAY-06}-repro.{sh,ps1,go}`)
Required header block: finding ID, REL-* ID, target phase, target OS, expected pre-fix behavior, expected post-fix behavior, step-by-step run instructions.

### Pattern G — `//go:build darwin || windows` build tag for tray tests
**Source:** `cmd/otto-tray/fsm_test.go:1`, `cmd/otto-tray/runner_test.go:1`, `cmd/otto-tray/pidfile_test.go:1`, `cmd/otto-tray/poller_test.go:1`
**Apply to:** All Plan 14-03 Go regression test files. Linux CI skips tray tests by build constraint.

---

## No Analog Found

Strict "no analog at all in tree" cases — planner should use the manual-reproducer-script header convention and a skipped Go stub:

| File | Finding | Reason |
|---|---|---|
| `tests/reliability/manual/REL-POOL-06-repro.go` | P-6 | No Windows pgid test infra exists in the tree; `pool_pgid_windows.go` has no `_test.go` sibling. |
| `tests/reliability/manual/REL-TRAY-02-repro.ps1` | T-2 | No PowerShell unit-test framework in tree; `test-support-bundle.ps1` is integration-shaped, the closest convention to copy. |
| `tests/reliability/manual/REL-TRAY-03-repro.sh` | T-3 | macOS GUI / LSUIElement behavior requires a real session bus; no in-tree analog. |
| `tests/reliability/manual/REL-TRAY-06-repro.ps1` | T-6 | Same as T-2 — PowerShell-only failure mode, no in-tree unit-test analog. |

Partial-analog cases (test infra exists but not for the specific failure pattern):

| File | Finding | Reason |
|---|---|---|
| `cmd/otto-tray/regression_rel_tray_01_test.go` | T-1 | `pidfile_test.go` covers PID round-trip but not name/cmdline identity check — extend the pattern. |
| `cmd/otto-tray/regression_rel_tray_04_test.go` | T-4 | `fsm_test.go` covers `computeState`, not `applyState`'s notify call path — extend the pattern. |

The 23 Medium findings other than C+H reproducers (per D-02) don't need test files at all — they get 2-4 sentence code-walks in their `14-FINDING-<ID>.md` evidence files. Per-finding files have no Go-analog requirement.

---

## Metadata

**Analog search scope:**
- `internal/pool/`, `internal/acp/`, `internal/session/` (Plan 14-01)
- `internal/server/`, `internal/admin/`, `internal/adapter/openai/`, `internal/adapter/ollama/`, `internal/adapter/anthropic/` (Plan 14-02)
- `cmd/otto-tray/`, `scripts/`, `tests/scripts/` (Plan 14-03)
- `internal/config/`, `internal/plugin/`, `internal/engine/` (Plan 14-04)

**Files scanned (test files):** ~50 `*_test.go` files plus `tests/scripts/test-support-bundle.{sh,ps1}` and `scripts/test-pii.{sh,ps1}`.

**Pattern extraction date:** 2026-06-11
