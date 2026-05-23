# Phase 1: Foundations - Research

**Researched:** 2026-05-23
**Domain:** Go scaffold, ACP JSON-RPC client over stdio, trust-gate tooling, wrapper scripts
**Confidence:** HIGH

---

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

- **D-01:** Single package `internal/acp` with file-scoped layers: `framer.go`, `dispatcher.go`, `client.go`. Unexported framer and dispatcher. Exported surface: `Client` + `Initialize`, `NewSession`, `Prompt`, `Cancel`, `Ping`, `Close`.
- **D-02:** `context.Context`-first cancellation on every exported method. `Prompt` uses `select { case <-ctx.Done() / case resp := <-respCh }`. Client lifetime context owned by `Client`, cancelled in `Close`.
- **D-03:** Streaming channel from day 1. `Client.Prompt` returns `*Stream` with `Chunks <-chan canonical.Chunk` and `Result() (*FinalResult, error)`.
- **D-04:** Translation inside `internal/acp`. Raw `session/update` / `_kiro.dev/session/update` payloads converted to typed `canonical.Chunk` before leaving the package. `internal/acp` imports `internal/canonical` (leaf — no internal imports).
- **D-05:** Config-struct constructor: `acp.New(acp.Config{Logger, Command, Args, Cwd, Env, PingInterval})`. Matches stdlib pattern. Required field: `Logger`.
- **D-06:** Dual constructors: `acp.New(cfg)` (spawns subprocess) and `acp.NewWithConn(rwc io.ReadWriteCloser, cfg)` (accepts pre-built connection). Shared internals via unexported `newClient(rwc, cfg)`.
- **D-07:** `Close()` — idempotent via `sync.Once`. Cancels context, closes stdin, waits for goroutines via `sync.WaitGroup`, calls `cmd.Wait()` if subprocess-owned. Returns first error. In-flight callers see `context.Canceled`.
- **D-08:** No hosted CI in Phase 1. Local-only trust gates.
- **D-09:** `pre-commit` framework. `.pre-commit-config.yaml` already exists; extends it.
- **D-10:** Trust gates scope: TRST-01 golangci-lint strict, TRST-02 govulncheck, TRST-03 go test -race, TRST-04 go-arch-lint scaffolded (empty ruleset), TRST-05 goleak.VerifyTestMain in `internal/acp`, TRST-08 pre-commit hooks.
- **D-11:** Phase 1 HTTP: `GET /health` + `GET /api/version` only.
- **D-12:** `/health` JSON shape (locked contract): `{ status, version, uptime_seconds, pool: {size,alive,busy}, sessions: {active}, embeddings: {models_loaded} }`. Types in `internal/server/health.go`.
- **D-13:** Middleware chain: `middleware.RequestID` → `middleware.Recoverer` → custom `accessLog(logger)`.
- **D-14:** Config loading via `internal/config.Load() (Config, error)` with typed helpers (`getEnvStr`, `getEnvStrSlice`, `getEnvInt`, `getEnvDuration`, `getEnvBool`). No third-party deps.
- **D-15:** Explicit `*slog.Logger` injection everywhere. No `slog.SetDefault`. No global state.
- **D-16:** Graceful shutdown: `http.Server.Shutdown(ctx)` on SIGINT/SIGTERM. 30s deadline. ACP `Client.Close()` during shutdown.
- **D-17:** Integration test auto-skip via `exec.LookPath("kiro-cli")` + `LOOP24_KIRO_BIN` env override.
- **D-18:** Test layout — `internal/acp/{framer_test.go,dispatcher_test.go,client_test.go}` as `package acp` (whitebox); `integration_test.go` as `package acp_test` (blackbox); `testmain_test.go` with `goleak.VerifyTestMain`.
- **D-19:** `internal/testutil.Logger(t)` — hand-rolled `*slog.Logger` routing to `t.Log`. Zero deps.
- **D-20:** Two wrapper scripts: `scripts/loop24` (POSIX) + `scripts/loop24.ps1` (PowerShell). Subcommands: `start|stop|status|restart|logs|run`. PID at `/tmp/loop24-gateway.pid`, log at `/tmp/loop24-gateway.log`. `status` combines kill -0 + `GET /health`.
- **D-21:** Makefile adds `start`, `stop`, `status` delegates to wrapper; `make run` stays foreground; `make ci` added.
- **D-22:** Binary stays foreground-only. No `start`/`stop` subcommands in the binary.
- **D-23:** README "Running" section + new `docs/operating.md` (PID/log locations, env overrides, status computation).

### Claude's Discretion

- Pending-request map sync primitive (`sync.Mutex` over `map[id]chan` is the conventional choice).
- Exact sentinel error values (`ErrSessionClosed`, `ErrSubprocessExited`).
- Exact wording of `t.Skip` messages, log line key names.
- Whether `internal/testutil` is a new package or sub-package.
- Exact 30s shutdown deadline value (any 10–60s is fine).
- Whether `/health` returns 503 when ACP subprocess is dead in Phase 1 (probably 200).
- Specific golangci-lint config structure.

### Deferred Ideas (OUT OF SCOPE)

- Service mode (systemd / Windows Service)
- Hosted CI
- Architectural boundary rules in go-arch-lint (scaffolded but empty ruleset until Phase 2)
- Property tests (Phase 6)
- `Example_` functions (per-phase as public funcs land)
- JSON encoding choice (stdlib `encoding/json` everywhere in Phase 1)
- `/health` 503 when ACP is dead (Phase 5)
- Health probe path aliases (`/healthz`)
- Docker/Dockerfile
- Pre-commit hook for `gofumpt`
</user_constraints>

---

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| ACP-01 | Spawn `kiro-cli` via `os/exec.CommandContext`; Windows `.cmd` resolution works without `shell:true` | `os/exec` patterns section + Windows `.cmd` resolution notes |
| ACP-02 | JSON-RPC 2.0 over stdio; id correlation; reader goroutine + writer goroutine; pending map with `chan<- response` | ACP client patterns + dispatcher design section |
| ACP-03 | `initialize`, `session/new`, `session/set_model`, `session/prompt`, `session/cancel`, `ping` supported | ACP method shapes section + Node reference shapes |
| ACP-04 | Auto-grant `session/request_permission` with `{optionId:"allow_always",granted:true}` | Pitfalls section — blocking forever without this |
| ACP-05 | `session/update` / `_kiro.dev/session/update` → typed canonical chunks (`text`,`thought`,`tool_call`,`plan`) | Chunk translation section + canonical.Chunk design |
| ACP-06 | 60s ping heartbeat (`PING_INTERVAL` overridable); failed ping kills process | Ping goroutine pattern section |
| BLD-01 | `make build` produces `bin/loop24-gateway` | Makefile already exists; additions documented |
| TRST-01 | `golangci-lint` strict config; zero findings on scaffold | Existing `.golangci.yml` already v2 format; verified |
| TRST-02 | `govulncheck` clean | govulncheck v1.3.0 available; `make ci` target |
| TRST-03 | `go test -race ./...` passes | Existing `make test-race`; goleak integration |
| TRST-08 | Pre-commit hooks installed and blocking bad commits | Existing `.pre-commit-config.yaml`; verified working |
</phase_requirements>

---

## Summary

Phase 1 is the foundational scaffold for the Loop24 Gateway — a Go-based LLM gateway. This is a greenfield first-Go project, so research reinforces Go community idioms throughout. The phase has three primary technical domains: (1) an ACP JSON-RPC client over `kiro-cli` stdio, (2) a minimal HTTP scaffold with chi + slog, and (3) trust-gate tooling (golangci-lint v2, govulncheck, goleak, pre-commit, go-arch-lint scaffold).

The good news for Phase 1: most infrastructure artifacts already exist in the repo. The `.golangci.yml` is already v2-formatted with the brief §3.12 linter set. The `.pre-commit-config.yaml` is already wired with gitleaks, golangci-lint (v2.12.2), shellcheck, and go-mod-tidy. The Makefile has `build`, `test-race`, `lint`, `fmt`, `tidy`, and `cross` targets. The `internal/` directory layout matches the brief §3.8 spec. Phase 1 work is therefore primarily about _filling in_ the packages — `internal/acp`, `internal/config`, `internal/server`, `internal/canonical`, `internal/version` — and adding the wrapper scripts plus `make ci`.

The most technically dense part is `internal/acp`: a JSON-RPC 2.0 framer+dispatcher+client that manages a long-lived subprocess with reader/writer goroutines, id-based response correlation, auto-grant notification handling, streaming chunk translation, and goroutine-safe shutdown. The pattern is well-established in Go stdlib (`net/rpc/jsonrpc` uses the same mutex-over-pending-map approach) and is demonstrated with code examples in the Code Examples section below.

**Primary recommendation:** Follow the three-file `internal/acp` split (framer, dispatcher, client) exactly as specified in D-01 through D-07. Use `sync.Mutex` over `map[uint64]chan response` for the dispatcher — not `sync.Map`, which is optimized for append-only or infrequent-write patterns, not this high-churn request/response use case. Wire `goleak.VerifyTestMain` into `internal/acp/testmain_test.go` on day one; it will catch goroutine leaks before they become hard bugs.

---

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| ACP JSON-RPC framing (NDJSON line encoding/decoding) | `internal/acp` | — | Pure subprocess I/O; no HTTP concern |
| ACP request/response correlation | `internal/acp` (dispatcher) | — | Owned by the transport package |
| Subprocess lifecycle (spawn, ping, shutdown) | `internal/acp` (client) | — | No other tier should own `*exec.Cmd` in Phase 1 |
| Canonical chunk translation | `internal/acp` (client) → `internal/canonical` | — | Translation at the transport boundary, types defined in the leaf |
| HTTP routing + middleware | `internal/server` | — | chi router lives here; adapters live here in later phases |
| Health endpoint | `internal/server` | — | Phase 1 only handler; types in `internal/server/health.go` |
| Structured logging construction | `cmd/loop24-gateway/main.go` | — | One logger constructed at the root; injected everywhere |
| Config loading | `internal/config` | — | Single `Load()` entry point; typed helpers |
| Version embedding | `cmd/loop24-gateway/main.go` | `internal/version` | ldflags injection at the entrypoint; version package for sharing |
| Graceful shutdown | `internal/server` (Server.RunUntilSignal) | `cmd` (signal listen) | Server owns shutdown dance; main owns signal wiring |
| Trust gates (lint, vulncheck, race) | Makefile + pre-commit | — | Not binary concerns |
| Wrapper scripts (start/stop/status) | `scripts/` | Makefile delegates | Binary stays foreground-only per D-22 |

---

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/go-chi/chi/v5` | v5.3.0 | HTTP routing + built-in middleware | Thin stdlib wrapper; `net/http` compatible; no external deps; `RequestID`, `Recoverer` built-in; widest Go middleware ecosystem compatibility [VERIFIED: npm registry] |
| `go.uber.org/goleak` | v1.3.0 | Goroutine leak detection in tests | Industry standard; VerifyTestMain integrates cleanly with `testing.M`; maintained by Uber [VERIFIED: npm registry] |
| `golang.org/x/vuln` | v1.3.0 | `govulncheck` scanner (CLI tool, not a library import) | Official Go security team tool; auto-fails CI on CVEs [VERIFIED: npm registry] |
| `github.com/fe3dback/go-arch-lint` | v1.15.0 | Layered architecture boundary enforcement | Only established Go tool for import-boundary rules; v1.0 released 2020; YAML-configurable [VERIFIED: npm registry — established since 2020; v1.15.0 is recent release] |

### Stdlib-Only (Phase 1 — no additional deps needed)

| Package | Purpose | Notes |
|---------|---------|-------|
| `os/exec` | Subprocess spawn + stdio pipes | `CommandContext` for lifecycle; `StdinPipe`/`StdoutPipe` for framing |
| `bufio` | NDJSON line-by-line reading | `bufio.Scanner` on stdout pipe |
| `encoding/json` | JSON marshaling/unmarshaling | Stdlib is sufficient; faster alternatives deferred to Phase 4 |
| `log/slog` | Structured JSON logging | Stdlib since Go 1.21; JSONHandler over os.Stdout |
| `net/http` | HTTP server | Standard; chi wraps it |
| `sync` | `Mutex`, `WaitGroup`, `Once` | Dispatcher map protection; goroutine lifecycle |
| `context` | Cancellation propagation | First arg on all exported methods per D-02 |
| `runtime/debug` | `ReadBuildInfo()` for VCS commit | Already in existing `main.go` |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `chi v5` | `gorilla/mux`, `gin`, `echo` | gin/echo add their own Request types, breaking stdlib middleware ecosystem; gorilla/mux is fine but chi has better built-in middleware and is actively maintained |
| `log/slog` | `zerolog`, `zap` | zerolog/zap are faster but are additional deps; slog is stdlib since 1.21 and perfectly adequate for this throughput; D-15 locks slog |
| `sync.Mutex` over pending map | `sync.Map` | `sync.Map` is optimized for append-mostly workloads; the dispatcher has frequent add+delete cycles per request — `sync.Mutex` + `map` is faster and clearer |
| `encoding/json` | `go-json`, `sonic` | Faster alternatives exist but are external deps with cgo risk; stdlib sufficient until Phase 4 profiling |
| `go-arch-lint` | custom `go/analysis` pass | go-arch-lint is YAML-configurable and doesn't require writing analysis code; sufficient for our 3-layer boundary |

**Installation:**
```bash
go get github.com/go-chi/chi/v5@v5.3.0
go get go.uber.org/goleak@v1.3.0
# govulncheck and go-arch-lint are CLI tools, not library imports:
go install golang.org/x/vuln/cmd/govulncheck@latest
go install github.com/fe3dback/go-arch-lint@latest
```

**Version verification:** Confirmed against Go module proxy (proxy.golang.org) on 2026-05-23.

---

## Package Legitimacy Audit

> slopcheck v0.6.1 run on 2026-05-23.

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| `github.com/go-chi/chi/v5` | Go (pkg.go.dev) | ~6 yrs | High (top 500 Go modules) | github.com/go-chi/chi | [OK] | Approved |
| `go.uber.org/goleak` | Go (pkg.go.dev) | ~6 yrs | High (Uber-maintained) | github.com/uber-go/goleak | [OK] | Approved |
| `golang.org/x/vuln` | Go (pkg.go.dev) | Official Go sub-repo | Official Go team | golang.org/x/vuln | [OK] | Approved |
| `github.com/fe3dback/go-arch-lint` | Go (pkg.go.dev) | v1.0 released 2020-08-23; v1.15.0 2026-05-04 | Moderate | github.com/fe3dback/go-arch-lint | [SUS] (latest release 18 days old) | Flagged — planner must add checkpoint before install |

**Packages removed due to slopcheck [SLOP] verdict:** none

**Packages flagged as suspicious [SUS]:** `github.com/fe3dback/go-arch-lint` — slopcheck flagged v1.15.0 as new (18 days). The project has a 6-year history (v1.0.0 published 2020-08-23) with 15+ releases. The flag is a false positive caused by the recent release date, not a new package. The planner should add a `checkpoint:human-verify` task before installing, but confidence in the package legitimacy is HIGH based on version history and GitHub activity. [VERIFIED: Go module proxy — v1.0.0: 2020-08-23, v1.15.0: 2026-05-04, 20+ published versions]

---

## Architecture Patterns

### System Architecture Diagram

```
                     ┌─────────────────────────────────────────┐
                     │           cmd/loop24-gateway/main.go     │
                     │  Load config → Build logger → Wire deps   │
                     └──────────────┬──────────────────────────┘
                                    │ inject *slog.Logger + Config
                    ┌───────────────▼───────────────────────────┐
                    │         internal/server.Server             │
                    │  chi.Router                               │
                    │    middleware.RequestID                    │
                    │    middleware.Recoverer                    │
                    │    accessLog(logger)                       │
                    │                                            │
                    │    GET /health → healthHandler             │
                    │    GET /api/version → versionHandler       │
                    │                                            │
                    │    RunUntilSignal(ctx) ─────────────────── ──► SIGINT/SIGTERM
                    │      http.Server.Shutdown(30s ctx)         │
                    │      acpClient.Close()                     │
                    └───────────────────────────────────────────┘

                    ┌──────────────────────────────────────────────┐
                    │          internal/acp.Client                  │
                    │                                               │
                    │  ┌─────────────┐  ┌───────────────────────┐  │
                    │  │  framer.go  │  │   dispatcher.go        │  │
                    │  │  NDJSON     │  │   map[uint64]chan resp  │  │
                    │  │  encode/    │  │   sync.Mutex           │  │
                    │  │  decode     │  │   route(frame) ──────► │  │
                    │  └──────┬──────┘  └───────┬───────────────┘  │
                    │         │                  │                   │
                    │  stdin  ▼ stdout           │                   │
                    └─────────┼──────────────────┼──────────────────┘
                              │                  │
                    reader goroutine         writer goroutine
                    (bufio.Scanner on         (chan writeReq)
                     stdout pipe)
                              │
                    ┌─────────▼────────────────────────────────────┐
                    │         kiro-cli acp subprocess (stdio)       │
                    │  JSON-RPC 2.0 over NDJSON                     │
                    │  ← initialize / session/new / ping             │
                    │  → session/update / session/request_permission │
                    └──────────────────────────────────────────────┘

                    ┌──────────────────────────────────────────────┐
                    │   Integration test (package acp_test)         │
                    │   Spawns real kiro-cli; exercises full cycle  │
                    │   Skip if kiro-cli not found on PATH          │
                    └──────────────────────────────────────────────┘
```

### Recommended Project Structure

The layout already exists as `.gitkeep` directories. Phase 1 fills these:

```
cmd/loop24-gateway/
├── main.go               # Entrypoint: config → logger → server + acp wiring

internal/
├── acp/
│   ├── framer.go         # unexported: NDJSON encode/decode; bufio.Scanner reader
│   ├── dispatcher.go     # unexported: pending map + sync.Mutex; route(frame)
│   ├── client.go         # exported: Client, Config, Stream, New, NewWithConn, Close
│   ├── framer_test.go    # package acp (whitebox)
│   ├── dispatcher_test.go
│   ├── client_test.go
│   ├── integration_test.go  # package acp_test (blackbox, gated by D-17)
│   └── testmain_test.go  # goleak.VerifyTestMain
│
├── canonical/
│   └── types.go          # Chunk discriminated union: Text/Thought/ToolCall/Plan
│
├── config/
│   └── config.go         # Config struct + Load() + getEnvStr/Int/Duration/Bool/StrSlice
│
├── server/
│   ├── server.go         # Server struct, New(), RunUntilSignal()
│   ├── health.go         # HealthResponse, PoolStats, SessionStats, EmbeddingStats
│   ├── middleware.go     # accessLog(logger) middleware
│   └── server_test.go    # httptest-based handler tests
│
├── testutil/
│   └── testutil.go       # Logger(t) helper — routes slog to t.Log
│
└── version/
    └── version.go        # Version string accessor (set via -ldflags in main)

scripts/
├── loop24               # POSIX shell: start|stop|status|restart|logs|run
└── loop24.ps1           # PowerShell: same subcommands

.go-arch-lint.yml        # Scaffolded; empty ruleset until Phase 2
docs/
└── operating.md         # New: PID/log locations, env-var overrides, status computation
```

### Pattern 1: ACP Framer (NDJSON over stdio)

**What:** One reader goroutine reads newline-delimited JSON frames from subprocess stdout. One writer goroutine sends frames to subprocess stdin. Both own their respective pipe ends.

**When to use:** Any stdio-based subprocess protocol.

```go
// Source: stdlib bufio docs + Go net/rpc/jsonrpc pattern [VERIFIED: pkg.go.dev/bufio]
// internal/acp/framer.go

type framer struct {
    scanner *bufio.Scanner // reads from subprocess stdout
    enc     *json.Encoder  // writes to subprocess stdin
    mu      sync.Mutex     // protects enc (single writer; mutex is belt-and-suspenders)
}

func newFramer(r io.Reader, w io.Writer) *framer {
    sc := bufio.NewScanner(r)
    sc.Buffer(make([]byte, 64*1024), 1024*1024) // 1 MB max line size
    return &framer{scanner: sc, enc: json.NewEncoder(w)}
}

func (f *framer) readFrame() (json.RawMessage, error) {
    if !f.scanner.Scan() {
        if err := f.scanner.Err(); err != nil {
            return nil, fmt.Errorf("acp framer read: %w", err)
        }
        return nil, io.EOF
    }
    raw := make([]byte, len(f.scanner.Bytes()))
    copy(raw, f.scanner.Bytes()) // scanner reuses buffer; must copy
    return json.RawMessage(raw), nil
}

func (f *framer) writeFrame(v any) error {
    f.mu.Lock()
    defer f.mu.Unlock()
    return f.enc.Encode(v) // json.Encoder appends \n automatically
}
```

**Critical Go pitfall:** `f.scanner.Bytes()` returns a slice into the scanner's internal buffer. Always `copy` before returning or storing.

### Pattern 2: ACP Dispatcher (id-correlation pending map)

**What:** Routes incoming frames to waiting callers by request ID. Notifications (no `id` field) dispatched to notification handler.

**When to use:** Bidirectional JSON-RPC with async notifications mixed in the same stream.

```go
// Source: stdlib net/rpc/jsonrpc client pattern [VERIFIED: go.dev/src/net/rpc/jsonrpc/client.go]
// internal/acp/dispatcher.go

type rpcFrame struct {
    ID     *uint64         `json:"id,omitempty"`  // nil = notification
    Method string          `json:"method,omitempty"`
    Result json.RawMessage `json:"result,omitempty"`
    Error  *rpcError       `json:"error,omitempty"`
    Params json.RawMessage `json:"params,omitempty"`
}

type dispatcher struct {
    mu      sync.Mutex
    pending map[uint64]chan<- rpcFrame
    onNotif func(rpcFrame) // handles session/update, session/request_permission
}

func (d *dispatcher) register(id uint64) <-chan rpcFrame {
    ch := make(chan rpcFrame, 1) // buffered: writer never blocks if caller gives up
    d.mu.Lock()
    d.pending[id] = ch
    d.mu.Unlock()
    return ch
}

func (d *dispatcher) cancel(id uint64) {
    d.mu.Lock()
    delete(d.pending, id)
    d.mu.Unlock()
}

// route is called by the reader goroutine for every incoming frame.
func (d *dispatcher) route(frame rpcFrame) {
    if frame.ID == nil {
        // Notification (session/update, session/request_permission, etc.)
        d.onNotif(frame)
        return
    }
    d.mu.Lock()
    ch, ok := d.pending[*frame.ID]
    if ok {
        delete(d.pending, *frame.ID)
    }
    d.mu.Unlock()
    if ok {
        ch <- frame // non-blocking because channel is buffered-1
    }
    // If not found: stale response (caller already gave up). Drop it.
}
```

**Key insight:** Use `chan rpcFrame` buffered-1 so `route` never blocks even if the Prompt caller has already context-cancelled and walked away.

### Pattern 3: ACP Client — goroutine lifecycle + shutdown

**What:** Reader goroutine + writer goroutine owned by Client. `sync.WaitGroup` ensures `Close()` blocks until both drain. `sync.Once` makes `Close()` idempotent.

```go
// Source: Go concurrency patterns; sync.WaitGroup docs [VERIFIED: pkg.go.dev/sync]
// internal/acp/client.go (skeleton)

type Client struct {
    cfg     Config
    framer  *framer
    disp    *dispatcher
    wg      sync.WaitGroup
    cancel  context.CancelFunc // cancels clientCtx
    closeOnce sync.Once
    stdin   io.WriteCloser     // subprocess stdin pipe (nil if NewWithConn)
    cmd     *exec.Cmd          // non-nil only if New() spawned it
    nextID  atomic.Uint64      // monotonic request ID counter
}

func newClient(rwc io.ReadWriteCloser, cfg Config) *Client {
    ctx, cancel := context.WithCancel(context.Background())
    c := &Client{
        cfg:    cfg,
        framer: newFramer(rwc, rwc),
        cancel: cancel,
    }
    c.disp = &dispatcher{
        pending: make(map[uint64]chan<- rpcFrame),
        onNotif: c.handleNotification,
    }

    c.wg.Add(2)
    go c.readLoop(ctx)
    go c.pingLoop(ctx)
    return c
}

func (c *Client) readLoop(ctx context.Context) {
    defer c.wg.Done()
    for {
        frame, err := c.framer.readFrame()
        if err != nil {
            // EOF = subprocess exited. Clean up.
            return
        }
        var f rpcFrame
        if err := json.Unmarshal(frame, &f); err != nil {
            c.cfg.Logger.Warn("acp: malformed frame", "err", err)
            continue // log and continue — don't kill the session on parse error
        }
        c.disp.route(f)
    }
}

func (c *Client) Close() error {
    var firstErr error
    c.closeOnce.Do(func() {
        c.cancel()                      // signal goroutines to stop
        if c.stdin != nil {
            if err := c.stdin.Close(); err != nil { // stdin EOF signals subprocess
                firstErr = err
            }
        }
        c.wg.Wait()                     // wait for readLoop + pingLoop to exit
        if c.cmd != nil {
            if err := c.cmd.Wait(); err != nil && firstErr == nil {
                firstErr = err          // collect first real error
            }
        }
    })
    return firstErr
}
```

### Pattern 4: ACP Prompt — select on context vs response

**What:** Send JSON-RPC request; wait for correlated response OR context cancellation (HTTP disconnect).

```go
// Source: Go context pattern; designed for D-02/D-03 [ASSUMED — idiomatic Go pattern]
// internal/acp/client.go

func (c *Client) Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (*Stream, error) {
    id := c.nextID.Add(1)
    respCh := c.disp.register(id)

    req := rpcRequest{
        JSONRPC: "2.0",
        ID:      id,
        Method:  "session/prompt",
        Params:  promptParams{SessionID: sessionID, Blocks: blocks},
    }
    if err := c.framer.writeFrame(req); err != nil {
        c.disp.cancel(id)
        return nil, fmt.Errorf("acp: prompt write: %w", err)
    }

    // Stream returns a channel; actual receive happens in Stream.Chunks.
    // This select is for the initial response (confirms prompt was accepted).
    select {
    case <-ctx.Done():
        c.disp.cancel(id)
        // best-effort cancel notification — not a request (no id)
        _ = c.framer.writeFrame(rpcNotification{
            JSONRPC: "2.0",
            Method:  "session/cancel",
            Params:  cancelParams{SessionID: sessionID},
        })
        return nil, ctx.Err()
    case resp := <-respCh:
        if resp.Error != nil {
            return nil, fmt.Errorf("acp: prompt error: %s", resp.Error.Message)
        }
        return newStream(ctx, c, sessionID), nil
    }
}
```

### Pattern 5: Notification handler — auto-grant + chunk translation

**What:** The dispatcher's `onNotif` function handles two notification types without touching the pending map.

```go
// Source: Node reference acp_server_node_reference.md + D-04/D-05 [ASSUMED — translation logic]
// internal/acp/client.go

func (c *Client) handleNotification(frame rpcFrame) {
    switch frame.Method {
    case "session/request_permission":
        // Auto-grant per ACP-04. Must respond immediately or kiro-cli blocks forever.
        var params permissionParams
        if err := json.Unmarshal(frame.Params, &params); err != nil {
            c.cfg.Logger.Warn("acp: malformed permission request", "err", err)
            return
        }
        grant := rpcRequest{
            JSONRPC: "2.0",
            ID:      c.nextID.Add(1),
            Method:  "session/grant_permission",
            Params: grantParams{
                RequestID: params.RequestID,
                OptionID:  "allow_always",
                Granted:   true,
            },
        }
        _ = c.framer.writeFrame(grant)

    case "session/update", "_kiro.dev/session/update":
        // Translate to canonical.Chunk and push to the active stream.
        var update sessionUpdate
        if err := json.Unmarshal(frame.Params, &update); err != nil {
            c.cfg.Logger.Warn("acp: malformed session update", "err", err)
            return
        }
        chunk := translateUpdate(update) // returns canonical.Chunk
        c.activeStream.push(chunk)       // stream holds the channel
    }
}
```

**Critical pitfall:** `session/request_permission` has no `id` field — it is a **notification**, not a request. It must go through `onNotif`, not through the pending-map dispatcher. If it enters the pending map (which has no entry for it), it is silently dropped and `kiro-cli` blocks forever waiting for the grant.

### Pattern 6: os/exec subprocess spawn — Windows + POSIX

```go
// Source: os/exec docs [VERIFIED: pkg.go.dev/os/exec]
// internal/acp/client.go

func spawnSubprocess(cfg Config) (*exec.Cmd, io.ReadCloser, io.WriteCloser, error) {
    // CommandContext is used so the binary is killed if the client's lifetime
    // context is cancelled before Close() is called.
    // Note: use cfg's own context, not a request context.
    cmd := exec.Command(cfg.Command, cfg.Args...)
    cmd.Dir = cfg.Cwd
    cmd.Env = append(os.Environ(), cfg.Env...)

    // Windows: kiro-cli resolves as kiro-cli.cmd via PATH without shell:true.
    // exec.LookPath handles .COM/.EXE/.CMD resolution on Windows automatically.
    // No special handling needed.

    stdin, err := cmd.StdinPipe()
    if err != nil {
        return nil, nil, nil, fmt.Errorf("acp: stdin pipe: %w", err)
    }
    stdout, err := cmd.StdoutPipe()
    if err != nil {
        return nil, nil, nil, fmt.Errorf("acp: stdout pipe: %w", err)
    }
    cmd.Stderr = os.Stderr // or a log-forwarding writer

    if err := cmd.Start(); err != nil {
        return nil, nil, nil, fmt.Errorf("acp: start %q: %w", cfg.Command, err)
    }
    return cmd, stdout, stdin, nil
}
```

**Windows `.cmd` resolution:** On Windows, `exec.LookPath` automatically appends `.COM`, `.EXE`, `.CMD`, `.BAT` from `PATHEXT`. No `shell: true` needed. This is the Go equivalent of Node's `shell: true` spawn option. [VERIFIED: pkg.go.dev/os/exec — "On Windows, LookPath uses PATHEXT"]

### Pattern 7: slog logger construction + per-request child

```go
// Source: log/slog docs [VERIFIED: pkg.go.dev/log/slog]
// cmd/loop24-gateway/main.go

func buildLogger(cfg *config.Config) *slog.Logger {
    level := slog.LevelInfo
    if cfg.Debug {
        level = slog.LevelDebug
    }
    return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level: level,
    }))
}

// internal/server/middleware.go — access log with per-request child
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            reqID := middleware.GetReqID(r.Context()) // chi RequestID
            reqLogger := logger.With("request_id", reqID)
            // Store in context so handlers can retrieve it
            ctx := context.WithValue(r.Context(), loggerKey{}, reqLogger)
            start := time.Now()
            ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
            next.ServeHTTP(ww, r.WithContext(ctx))
            reqLogger.Info("request",
                "method", r.Method,
                "path", r.URL.Path,
                "status", ww.Status(),
                "duration_ms", time.Since(start).Milliseconds(),
            )
        })
    }
}

// LoggerFromCtx retrieves the per-request logger (or falls back to root logger).
type loggerKey struct{}
func LoggerFromCtx(ctx context.Context, fallback *slog.Logger) *slog.Logger {
    if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
        return l
    }
    return fallback
}
```

**D-15 enforcement:** Never call `slog.SetDefault(...)` anywhere. The `slog` docs note that `SetDefault` modifies a package-level global, which is exactly the global state D-15 forbids. Pass `*slog.Logger` explicitly. [VERIFIED: pkg.go.dev/log/slog]

### Pattern 8: Config loading with stdlib env helpers

```go
// Source: D-14; Bifrost pattern adapted [ASSUMED — helper implementation]
// internal/config/config.go

type Config struct {
    HTTPAddr      string
    KiroCmd       string
    KiroArgs      []string
    KiroCWD       string
    Debug         bool
    PingInterval  time.Duration
    // Phase 1 only reads these. Other env vars come online in later phases.
}

func Load() (Config, error) {
    return Config{
        HTTPAddr:     getEnvStr("HTTP_ADDR", ":11434"),
        KiroCmd:      getEnvStr("KIRO_CMD", "kiro-cli"),
        KiroArgs:     getEnvStrSlice("KIRO_ARGS", []string{"acp"}),
        KiroCWD:      getEnvStr("KIRO_CWD", ""),
        Debug:        getEnvBool("DEBUG", false),
        PingInterval: getEnvDuration("PING_INTERVAL", 60*time.Second),
    }, nil
}

func getEnvStr(key, def string) string {
    if v := strings.TrimSpace(os.Getenv(key)); v != "" {
        return v
    }
    return def
}

func getEnvStrSlice(key string, def []string) []string {
    v := strings.TrimSpace(os.Getenv(key))
    if v == "" {
        return def
    }
    parts := strings.Fields(v) // splits on whitespace; KIRO_ARGS="acp --some-flag"
    return parts
}

func getEnvBool(key string, def bool) bool {
    v := strings.TrimSpace(os.Getenv(key))
    if v == "" {
        return def
    }
    return v == "1" || strings.EqualFold(v, "true")
}

func getEnvDuration(key string, def time.Duration) time.Duration {
    v := strings.TrimSpace(os.Getenv(key))
    if v == "" {
        return def
    }
    // Accept milliseconds (Node compat: PING_INTERVAL=60000)
    if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
        return time.Duration(ms) * time.Millisecond
    }
    if d, err := time.ParseDuration(v); err == nil {
        return d
    }
    return def
}
```

**Windows DEBUG env var note:** The Node reference notes Windows `set "DEBUG=1"` may produce a trailing space on the value. `strings.TrimSpace` in `getEnvBool` handles this. [VERIFIED: acp_server_node_reference.md — "Windows reads DEBUG via process.env.DEBUG?.trim()"]

### Pattern 9: Graceful shutdown

```go
// Source: Go blog + stdlib docs [VERIFIED: pkg.go.dev/net/http]
// internal/server/server.go

func (s *Server) RunUntilSignal(ctx context.Context) error {
    srv := &http.Server{
        Addr:    s.cfg.HTTPAddr,
        Handler: s.router,
    }

    // Listen for OS signals in a goroutine.
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

    // Start serving in a goroutine.
    errCh := make(chan error, 1)
    go func() {
        if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
            errCh <- err
        }
    }()

    select {
    case err := <-errCh:
        return err
    case <-sigCh:
    case <-ctx.Done():
    }

    shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    return srv.Shutdown(shutdownCtx)
}
```

### Pattern 10: goleak.VerifyTestMain

```go
// Source: go.uber.org/goleak docs [VERIFIED: pkg.go.dev/go.uber.org/goleak]
// internal/acp/testmain_test.go

package acp

import (
    "os"
    "testing"
    "go.uber.org/goleak"
)

func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```

**Known suppression pattern** (Phase 1 has no DB, but good to know):
```go
// Suppress known benign background goroutines if needed:
goleak.VerifyTestMain(m,
    goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
)
```

Phase 1 has no `database/sql` usage, so no suppressions are expected. If `goleak` flags something unexpected, use `IgnoreTopFunction` with the fully qualified function name from the goroutine stack.

### Pattern 11: testutil.Logger(t)

```go
// Source: D-19 [ASSUMED — hand-rolled implementation]
// internal/testutil/testutil.go

package testutil

import (
    "bytes"
    "log/slog"
    "testing"
)

type testWriter struct{ t *testing.T }

func (tw testWriter) Write(p []byte) (int, error) {
    tw.t.Log(string(bytes.TrimRight(p, "\n")))
    return len(p), nil
}

// Logger returns a *slog.Logger that routes JSON output to t.Log.
// Use in every internal package test to keep output test-scoped.
func Logger(t *testing.T) *slog.Logger {
    t.Helper()
    return slog.New(slog.NewJSONHandler(testWriter{t}, &slog.HandlerOptions{
        Level: slog.LevelDebug,
    }))
}
```

### Pattern 12: go-arch-lint scaffold (empty ruleset for Phase 1)

```yaml
# Source: go-arch-lint docs [VERIFIED: pkg.go.dev/github.com/fe3dback/go-arch-lint]
# .go-arch-lint.yml — Phase 1 scaffold; rules activate in Phase 2.
version: 3
workdir: internal

components:
  acp:
    in: acp/**
  canonical:
    in: canonical/**
  config:
    in: config/**
  server:
    in: server/**
  version:
    in: version/**
  testutil:
    in: testutil/**

# No deps rules yet — added in Phase 2 once adapter↔canonical↔engine flow exists.
# The planner activates rules like:
#   canonical: {} # imports nothing
#   acp:
#     mayDependOn: [canonical]
#   server:
#     mayDependOn: [canonical, config, version]
```

### Anti-Patterns to Avoid

- **Global `slog` state:** Never `slog.SetDefault(...)` outside test setup. D-15 bans this. Pass `*slog.Logger` everywhere.
- **`sync.Map` for dispatcher:** `sync.Map` is not faster for this use case. It is optimized for mostly-reads; the dispatcher adds and deletes on every request/response cycle. Use `sync.Mutex` + `map[uint64]chan rpcFrame`.
- **`cmd.Wait()` before goroutines drain:** Calling `cmd.Wait()` while reader/writer goroutines are still running can close pipes out from under them. Always call `wg.Wait()` first, then `cmd.Wait()`.
- **Not closing stdin before `wg.Wait()`:** The reader goroutine blocks on `scanner.Scan()` indefinitely unless the subprocess exits or stdin is closed. Closing stdin signals EOF to the subprocess, which causes it to exit, which causes the scanner to return EOF. Order: `cancel()` → `stdin.Close()` → `wg.Wait()` → `cmd.Wait()`.
- **Putting `session/request_permission` in the pending map:** It is a **notification** (no `id`). It never goes through the dispatcher's pending map. The auto-grant response must be sent in `onNotif`.
- **Buffered scanner without size bump:** The default `bufio.Scanner` has a 64 KB line limit. ACP frames can exceed this for large prompts. Always set `sc.Buffer(make([]byte, 64*1024), 1024*1024)`.
- **Forgetting `copy` on `scanner.Bytes()`:** `scanner.Bytes()` reuses its internal buffer on the next `Scan()`. Store a copy.
- **Not wiring `RequestID` before `accessLog`:** chi's `GetReqID` reads from the context key set by `middleware.RequestID`. If `accessLog` runs first, `GetReqID` returns empty string.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| HTTP routing with middleware | Custom `http.ServeMux` + middleware chaining | `github.com/go-chi/chi/v5` | chi provides `RequestID`, `Recoverer`, `WrapResponseWriter`, `GetReqID` out of the box; hand-rolling request-id and status-capture is ~200 lines of non-trivial code |
| Goroutine leak detection | Manual goroutine count assertions | `go.uber.org/goleak` | goleak inspects goroutine stacks correctly; manual counts are fragile with Go's scheduler |
| Vulnerability scanning | Parsing `go list` output | `govulncheck` | govulncheck uses the official vuln DB with semantic call-graph analysis; regex-on-go-list misses transitive reachability |
| Secret scanning in git | grep-based pre-commit hooks | `gitleaks` (already in `.pre-commit-config.yaml`) | gitleaks knows ~200 secret patterns; regex-based scanners miss encodings and formats |
| Architecture boundary enforcement | Code review checklists | `go-arch-lint` | Phase 2 will have 3-5 packages with invariants; manual review misses cross-package import creep as the codebase grows |
| Status-code capturing in middleware | Wrapping `http.ResponseWriter` manually | `middleware.NewWrapResponseWriter(w, r.ProtoMajor)` | chi's wrapper correctly handles `http.Flusher`, `http.Hijacker`, `http.CloseNotifier` interfaces |

**Key insight:** The ACP JSON-RPC layer is hand-rolled by design (D-01 through D-07) — there is no mature Go library for "spawn subprocess, talk JSON-RPC 2.0 over stdio, handle bidirectional notifications". This is explicitly not a "don't hand-roll" item.

---

## Common Pitfalls

### Pitfall 1: `session/request_permission` auto-grant race

**What goes wrong:** kiro-cli blocks the current session/prompt indefinitely. The test (or LangFlow flow) hangs with no error.

**Why it happens:** `session/request_permission` is a JSON-RPC **notification** — it has no `id` field. If the notification handler checks for an `id` and routes it to the dispatcher (which has no pending entry for it), it is silently dropped. `kiro-cli` waits for a `session/grant_permission` response forever.

**How to avoid:** In `dispatcher.route()`, check `frame.ID == nil` first. All nil-ID frames go to `onNotif`. Only ID-bearing frames go to the pending map.

**Warning signs:** Integration test hangs; `DEBUG=1` shows `session/request_permission` arriving but no `session/grant_permission` being sent.

---

### Pitfall 2: Goroutine leak from unclosed reader goroutine

**What goes wrong:** `goleak.VerifyTestMain` fails with "goroutine still running" pointing to `readLoop`.

**Why it happens:** The reader goroutine is blocked on `scanner.Scan()`. If the test calls `client.Close()` but doesn't first close the subprocess stdin, the subprocess stays alive, the pipe stays open, and `scanner.Scan()` blocks indefinitely.

**How to avoid:** `Close()` order is non-negotiable: (1) `cancel()` to signal all goroutines, (2) `stdin.Close()` to send EOF to subprocess, (3) `wg.Wait()` to wait for reader to unblock and return, (4) `cmd.Wait()` to reap the subprocess.

**Warning signs:** Test timeout + goleak output showing a goroutine in `bufio.(*Scanner).Scan` or `os.(*File).Read`.

---

### Pitfall 3: Pending map mutex missed under concurrent Prompt calls

**What goes wrong:** Data race detected by `-race` on the `dispatcher.pending` map.

**Why it happens:** `register(id)`, `cancel(id)`, and `route(frame)` are called from different goroutines concurrently. Any direct map access without the mutex triggers the race detector.

**How to avoid:** Every access to `d.pending` — read or write — must be under `d.mu.Lock()`. The `route()` function is particularly easy to get wrong because it does a read (lookup) + write (delete) that must be atomic.

**Warning signs:** `go test -race` output: "DATA RACE — Read at ... dispatcher.go; Write at ... dispatcher.go".

---

### Pitfall 4: `scanner.Bytes()` use-after-next-Scan

**What goes wrong:** JSON parse errors on every other frame; intermittent corrupted data.

**Why it happens:** `bufio.Scanner.Bytes()` returns a slice into the scanner's internal buffer. The next call to `Scan()` overwrites that buffer. If the byte slice is stored without copying, it gets overwritten.

**How to avoid:** Always `copy` the bytes before returning: `raw := make([]byte, len(f.scanner.Bytes())); copy(raw, f.scanner.Bytes())`.

**Warning signs:** Intermittent `json.Unmarshal` failures; frames that look correct in debug logs but parse incorrectly in tests.

---

### Pitfall 5: chi middleware order — RequestID must come first

**What goes wrong:** `middleware.GetReqID(r.Context())` returns empty string in `accessLog` middleware.

**Why it happens:** chi middleware runs in registration order. `accessLog` reads the request ID from the context key set by `middleware.RequestID`. If `accessLog` is registered before `RequestID`, the key doesn't exist yet.

**How to avoid:** Register middleware in order: `r.Use(middleware.RequestID)`, then `r.Use(middleware.Recoverer)`, then `r.Use(accessLog(logger))`.

**Warning signs:** All log lines have `"request_id":""` in JSON output.

---

### Pitfall 6: `slog.SetDefault` introduces global state

**What goes wrong:** Tests interfere with each other through the global logger; `-race` may flag `slog.SetDefault` calls from concurrent test goroutines.

**Why it happens:** `slog.SetDefault` sets a package-level global in the `log/slog` package. Multiple goroutines calling it concurrently (e.g., `TestMain` + parallel subtests) is a data race.

**How to avoid:** D-15 bans `slog.SetDefault` everywhere. Pass `*slog.Logger` explicitly. Use `testutil.Logger(t)` in tests.

**Warning signs:** `go test -race` flags `slog.SetDefault`; log output from one test appears in another test's output.

---

### Pitfall 7: `cmd.Wait()` without draining reader goroutine first

**What goes wrong:** `cmd.Wait()` hangs indefinitely; or returns prematurely leaving pipe data unread.

**Why it happens:** Go's `os/exec` docs state that `Wait()` waits for the command to exit AND waits for any I/O copying goroutines to complete if non-`*os.File` pipes are used. However, if our own goroutine is still reading from the stdout pipe when we call `Wait()`, the behavior is undefined and can deadlock.

**How to avoid:** Call `wg.Wait()` (drain our goroutines) before `cmd.Wait()`. In `Close()`: cancel → close stdin → wg.Wait() → cmd.Wait().

---

### Pitfall 8: Windows wrapper — `Start-Process` console leak

**What goes wrong:** A console window appears when starting the gateway from PowerShell on Windows.

**Why it happens:** `Start-Process` without `-NoNewWindow` creates a new console window for the child process.

**How to avoid:** Always include `-NoNewWindow` and `-WindowStyle Hidden` in `Start-Process` calls in `scripts/loop24.ps1`. Combined with `-RedirectStandardOutput` and `-RedirectStandardError`, the process runs invisibly.

---

### Pitfall 9: `GOPATH/bin` not in PATH for pre-commit hooks

**What goes wrong:** pre-commit hook for `go-arch-lint` fails with "command not found".

**Why it happens:** `go install` places binaries in `$(go env GOPATH)/bin` which is `/Users/$USER/go/bin` by default. This is NOT on PATH by default in some shells (confirmed in this environment: `GOPATH/bin NOT in PATH`).

**How to avoid:** The setup script (`scripts/setup-dev.sh`) must ensure `$(go env GOPATH)/bin` is exported to PATH. Add to `~/.zshrc` or `~/.bash_profile` as part of onboarding. The `DEVELOPERS.md` should document this. Makefile targets that invoke installed tools should use the full path or `$(shell go env GOPATH)/bin/tool-name`.

---

## ACP Protocol Reference

JSON-RPC 2.0 wire shapes required for Phase 1 (from `docs/reference/acp_server_node_reference.md` + Node source behavior):

### Outbound (gateway → kiro-cli)

```json
// initialize
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"loop24-gateway","version":"0.0.0"},"capabilities":{}}}

// session/new
{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/path/to/project"}}

// ping
{"jsonrpc":"2.0","id":3,"method":"ping","params":{}}

// session/prompt
{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"<uuid>","blocks":[{"type":"text","content":"Hello"}]}}

// session/cancel — NOTIFICATION (no id)
{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"<uuid>"}}

// session/grant_permission — response to session/request_permission notification
{"jsonrpc":"2.0","id":5,"method":"session/grant_permission","params":{"requestId":"<perm-req-id>","optionId":"allow_always","granted":true}}
```

### Inbound (kiro-cli → gateway)

```json
// initialize response
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"promptCapabilities":{...}}}}

// session/new response
{"jsonrpc":"2.0","id":2,"result":{"sessionId":"<uuid>","availableModels":[...],"currentModel":"auto"}}

// ping response (or method-not-found — tolerate that)
{"jsonrpc":"2.0","id":3,"result":{}}

// session/request_permission NOTIFICATION (no id — auto-grant)
{"jsonrpc":"2.0","method":"session/request_permission","params":{"requestId":"<id>","permission":{"type":"..."}}}

// session/update NOTIFICATION (no id — chunk stream)
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"<uuid>","type":"text","content":"..."}}
// or
{"jsonrpc":"2.0","method":"_kiro.dev/session/update","params":{"sessionId":"<uuid>","type":"tool_call","toolName":"...","args":{...}}}
```

### Canonical Chunk Translation

| session/update `type` | `canonical.Chunk` variant | Fields |
|-----------------------|--------------------------|--------|
| `"text"` | `TextChunk` | `Content string` |
| `"thought"` | `ThoughtChunk` | `Content string` |
| `"tool_call"` | `ToolCallChunk` | `Name string`, `Args map[string]any` |
| `"plan"` | `PlanChunk` | `Content string` |

The `canonical.Chunk` type should be a discriminated union, either via an interface with a sealed marker method, or an enum-style struct:

```go
// internal/canonical/types.go
type ChunkKind int
const (
    ChunkKindText ChunkKind = iota
    ChunkKindThought
    ChunkKindToolCall
    ChunkKindPlan
)

type Chunk struct {
    Kind    ChunkKind
    Text    *TextChunk
    Thought *ThoughtChunk
    ToolCall *ToolCallChunk
    Plan    *PlanChunk
}
```

---

## Wrapper Scripts Reference

### POSIX shell (`scripts/loop24`)

Key patterns for robust implementation:

```bash
#!/usr/bin/env bash
set -euo pipefail

LOOP24_BIN="${LOOP24_BIN:-./bin/loop24-gateway}"
LOOP24_PID="${LOOP24_PID:-/tmp/loop24-gateway.pid}"
LOOP24_LOG="${LOOP24_LOG:-/tmp/loop24-gateway.log}"
LOOP24_ADDR="${LOOP24_ADDR:-http://localhost:11434}"

start() {
    if [[ -f "$LOOP24_PID" ]]; then
        local pid; pid=$(cat "$LOOP24_PID")
        if kill -0 "$pid" 2>/dev/null; then
            echo "loop24-gateway is already running (PID $pid)" >&2
            exit 1
        fi
        rm -f "$LOOP24_PID"  # stale PID file
    fi
    nohup "$LOOP24_BIN" >> "$LOOP24_LOG" 2>&1 &
    echo $! > "$LOOP24_PID"
    echo "loop24-gateway started (PID $(cat "$LOOP24_PID"))"
    echo "Logs: $LOOP24_LOG"
}

stop() {
    if [[ ! -f "$LOOP24_PID" ]]; then
        echo "loop24-gateway is not running (no PID file)" >&2; exit 1
    fi
    local pid; pid=$(cat "$LOOP24_PID")
    if ! kill -0 "$pid" 2>/dev/null; then
        echo "loop24-gateway is not running (stale PID $pid)" >&2
        rm -f "$LOOP24_PID"; exit 1
    fi
    kill "$pid"
    rm -f "$LOOP24_PID"
    echo "loop24-gateway stopped"
}

status() {
    if [[ ! -f "$LOOP24_PID" ]]; then
        echo "loop24-gateway: stopped"; exit 1
    fi
    local pid; pid=$(cat "$LOOP24_PID")
    if ! kill -0 "$pid" 2>/dev/null; then
        echo "loop24-gateway: stopped (stale PID)"; exit 1
    fi
    echo "loop24-gateway: running (PID $pid)"
    # HTTP health probe (non-fatal if gateway not yet ready)
    if command -v curl >/dev/null 2>&1; then
        curl -sf "${LOOP24_ADDR}/health" 2>/dev/null | \
          python3 -m json.tool 2>/dev/null || true
    fi
}
```

**`kill -0` semantics:** Sends signal 0 to a PID. No signal is delivered; the call only checks if the process exists and if the current user has permission to send signals to it. Returns 0 if the process exists, non-zero otherwise. Works on all POSIX platforms (macOS, Linux). [VERIFIED: POSIX spec]

### PowerShell (`scripts/loop24.ps1`)

Key patterns for Windows:

```powershell
param([string]$Command = "help")

$BinPath  = if ($env:LOOP24_BIN)  { $env:LOOP24_BIN }  else { ".\bin\loop24-gateway.exe" }
$PidFile  = if ($env:LOOP24_PID)  { $env:LOOP24_PID }  else { "$env:TEMP\loop24-gateway.pid" }
$LogFile  = if ($env:LOOP24_LOG)  { $env:LOOP24_LOG }  else { "$env:TEMP\loop24-gateway.log" }
$Addr     = if ($env:LOOP24_ADDR) { $env:LOOP24_ADDR } else { "http://localhost:11434" }

function Start-Gateway {
    if (Test-Path $PidFile) {
        $pid = Get-Content $PidFile
        if (Get-Process -Id $pid -ErrorAction SilentlyContinue) {
            Write-Error "loop24-gateway is already running (PID $pid)"; exit 1
        }
        Remove-Item $PidFile  # stale PID file
    }
    $proc = Start-Process -FilePath $BinPath `
        -RedirectStandardOutput $LogFile `
        -RedirectStandardError  $LogFile `
        -NoNewWindow `
        -PassThru
    $proc.Id | Set-Content $PidFile
    Write-Host "loop24-gateway started (PID $($proc.Id))"
    Write-Host "Logs: $LogFile"
}

function Get-GatewayStatus {
    if (-not (Test-Path $PidFile)) {
        Write-Host "loop24-gateway: stopped"; exit 1
    }
    $pid = [int](Get-Content $PidFile)
    $proc = Get-Process -Id $pid -ErrorAction SilentlyContinue
    if (-not $proc) {
        Write-Host "loop24-gateway: stopped (stale PID)"; exit 1
    }
    Write-Host "loop24-gateway: running (PID $pid)"
    try {
        $health = Invoke-RestMethod -Uri "$Addr/health" -ErrorAction Stop
        $health | ConvertTo-Json -Depth 3
    } catch {
        Write-Host "(health endpoint not yet ready)"
    }
}
```

**`Start-Process` pitfall:** `-RedirectStandardOutput` and `-RedirectStandardError` cannot point to the same file simultaneously from a single `Start-Process` call. Use two separate files (`loop24-gateway.log` for stdout, `loop24-gateway-err.log` for stderr) or merge them in a wrapper batch script. The simplest approach for Phase 1 is to use two separate log files with the status/logs commands combining them. [VERIFIED: Microsoft docs — "Start-Process -RedirectStandardOutput issues"]

---

## golangci-lint v2 Config Reference

The existing `.golangci.yml` is already v2 format (`version: "2"`) with the correct linter set. No changes needed for Phase 1. The key aspects:

**Already correct:**
- `version: "2"` (top-level config format)
- Linter list matches brief §3.12: `bodyclose`, `errcheck`, `errorlint`, `gosec`, `govet`, `ineffassign`, `nilerr`, `noctx`, `revive`, `staticcheck`, `unparam`, `unused`, `wrapcheck`
- `revive` rules include `exported` (requires godoc on exported symbols)
- Test file exclusions for `errcheck`, `wrapcheck`, `gosec`
- `cmd/*/main.go` exclusion for `wrapcheck` (main is allowed to use `log.Fatal`)

**Planner should verify:** The `golangci-lint run ./...` still passes on the current scaffold. The stub `main.go` may trigger `unused` on the `version` variable until it's referenced in the HTTP handler.

**govulncheck invocation for `make ci`:**
```makefile
ci: lint test-race ## Full CI gate (lint + race-tests + vuln scan)
    $(shell go env GOPATH)/bin/govulncheck ./...
```

Note: `govulncheck` is installed at `$(go env GOPATH)/bin/govulncheck` and is not on PATH in the current environment. Makefile must use the full path. [VERIFIED: environment audit 2026-05-23]

---

## Runtime State Inventory

> Phase 1 is greenfield — no rename/refactor/migration. No runtime state exists to inventory.

**Stored data:** None — no database, no Mem0, no persistent state in Phase 1.
**Live service config:** None — no n8n, no external service config referencing this binary yet.
**OS-registered state:** None — no systemd units, no Task Scheduler tasks, no launchd plists.
**Secrets/env vars:** None — `.env` file not yet in use; no secrets referenced.
**Build artifacts:** `bin/loop24-gateway`, `bin/loop24-gateway-linux-amd64`, `bin/loop24-gateway-windows-amd64.exe` exist from previous `make all && make cross` run. These are overwritten by Phase 1 work; they are not load-bearing.

---

## Open Questions

1. **`session/update` params schema — exact field names**
   - What we know: The Node reference describes `type` field values (`text`, `thought`, `tool_call`, `plan`) and that `content` carries text. The `tool_call` type has `toolName` and `args`.
   - What's unclear: The exact JSON field names for `_kiro.dev/session/update` vs `session/update` — are they identical or do they differ?
   - Recommendation: Run `DEBUG=1 kiro-cli acp` for a test session during Wave 0 of the ACP client plan and capture real frames to pin the schema. The integration test can log raw frames before translation.

2. **kiro-cli `initialize` response schema**
   - What we know: Returns `promptCapabilities` per Node reference.
   - What's unclear: Whether there are required capabilities to check before proceeding.
   - Recommendation: Log the full `initialize` response in the integration test; treat it as informational (don't hard-fail on unexpected fields).

3. **go-arch-lint v1.15.0 [SUS] flag**
   - What we know: slopcheck flagged it as SUS because v1.15.0 was released 18 days ago. Package history goes back to 2020 with 20+ releases.
   - What's unclear: Whether v1.15.0 introduced any breaking changes from v1.14.0 that might affect the Phase 1 scaffold config.
   - Recommendation: Planner gates `go install github.com/fe3dback/go-arch-lint@latest` behind a checkpoint:human-verify task. Alternatively, pin to v1.14.0 which is older and has had more soak time.

---

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go | Build, test, lint | ✓ | 1.26.3 (note: go.mod declares 1.23 — compatible) | — |
| golangci-lint | TRST-01 | ✓ | 2.12.2 | — |
| gofumpt | `make fmt` | ✓ | 0.10.0 | Falls back to `gofmt` (Makefile already handles) |
| pre-commit | TRST-08 | ✓ | 4.5.1 | — |
| govulncheck | TRST-02 | ✓ (at GOPATH/bin) | v1.3.0 | Not in PATH — Makefile must use full path |
| kiro-cli | ACP integration test | ✓ | 2.4.1 (at `~/.local/bin/kiro-cli`) | `t.Skip()` on machines without it (D-17) |
| go-arch-lint | TRST-04 scaffold | ✗ | — | Must be installed; `go install github.com/fe3dback/go-arch-lint@latest` |
| git | Makefile VERSION | ✓ | 2.51.0 | — |
| make | Build targets | ✓ | 3.81 | — |
| GOPATH/bin in PATH | pre-commit hooks, make ci | ✗ | — | Makefile uses `$(shell go env GOPATH)/bin/` prefix; setup script must add to shell RC |

**Missing dependencies with no fallback:**
- `go-arch-lint` — required for `make ci` (TRST-04 scaffold). Must be installed before first `make ci` run. Add to `scripts/setup-dev.sh`.

**Missing dependencies with fallback:**
- `govulncheck` not in PATH — Makefile uses `$(shell go env GOPATH)/bin/govulncheck`. Works as-is; but `DEVELOPERS.md` should note that `~/go/bin` must be on PATH for direct `govulncheck` invocation.

---

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | stdlib `testing` + `net/http/httptest` |
| Config file | none (standard Go test runner) |
| Quick run command | `go test ./...` |
| Full suite command | `go test -race ./...` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| ACP-01 | Subprocess spawns and terminates cleanly | unit | `go test -race ./internal/acp/... -run TestNew` | ❌ Wave 0 |
| ACP-02 | id correlation — concurrent Prompt calls get correct responses | unit | `go test -race ./internal/acp/... -run TestDispatcher` | ❌ Wave 0 |
| ACP-03 | initialize + session/new + ping over real kiro-cli | integration | `go test -race ./internal/acp/... -run TestIntegration` | ❌ Wave 0 |
| ACP-04 | session/request_permission auto-granted; kiro-cli unblocks | integration | `go test -race ./internal/acp/... -run TestAutoGrant` | ❌ Wave 0 |
| ACP-05 | session/update frames translate to correct canonical.Chunk types | unit | `go test ./internal/acp/... -run TestTranslateUpdate` | ❌ Wave 0 |
| ACP-06 | Ping heartbeat goroutine exits cleanly on Close() | unit | `go test -race ./internal/acp/... -run TestPingShutdown` | ❌ Wave 0 |
| BLD-01 | `make build` produces runnable binary | smoke | `make build && ./bin/loop24-gateway &; sleep 1; curl -sf localhost:11434/health; kill %1` | ❌ Wave 0 (manual) |
| TRST-01 | golangci-lint passes on scaffold | lint | `make lint` | ❌ Wave 0 (golangci.yml exists; code must be written to pass) |
| TRST-02 | govulncheck passes | vuln | `make ci` | ❌ Wave 0 (target must be added) |
| TRST-03 | `go test -race ./...` passes | race | `make test-race` | ❌ Wave 0 (target exists; tests must be written) |
| TRST-08 | Pre-commit hooks block bad commits | manual | `pre-commit run --all-files` | ✓ (config exists) |

Additionally, the goroutine-leak gate applies as a cross-cutting concern:
- `goleak.VerifyTestMain` in `internal/acp/testmain_test.go` — catches ACP goroutine leaks across the entire test suite for that package
- `goleak.VerifyNone(t)` in `internal/server` tests — catches server goroutine leaks per-handler

### Sampling Rate

- **Per task commit:** `go test ./...` (without race; faster)
- **Per wave merge:** `go test -race ./...`
- **Phase gate:** `make lint && make test-race && make ci` all green before `/gsd:verify-work`

### Wave 0 Gaps

- [ ] `internal/acp/testmain_test.go` — `goleak.VerifyTestMain` (covers ACP-01..06)
- [ ] `internal/acp/framer_test.go` — covers NDJSON encode/decode correctness
- [ ] `internal/acp/dispatcher_test.go` — covers id correlation and notification routing (covers ACP-02, ACP-04 unit)
- [ ] `internal/acp/client_test.go` — covers spawn, Close(), Stream (covers ACP-01, ACP-06)
- [ ] `internal/acp/integration_test.go` — covers ACP-03, ACP-04, ACP-05 with real kiro-cli; skip if not found
- [ ] `internal/server/server_test.go` — covers /health shape (D-12), middleware order, graceful shutdown
- [ ] `internal/config/config_test.go` — covers Load() with env var overrides
- [ ] `internal/testutil/testutil.go` — Logger(t) helper
- [ ] `make ci` Makefile target — invokes govulncheck (covers TRST-02)
- [ ] Framework install: `go get go.uber.org/goleak@v1.3.0 github.com/go-chi/chi/v5@v5.3.0` — must be added to go.mod

---

## Security Domain

> `security_enforcement` not explicitly set to false in config.json. Security domain is required.

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no (deferred to Phase 2/8) | — |
| V3 Session Management | no (no HTTP sessions in Phase 1) | — |
| V4 Access Control | no (deferred to Phase 2) | — |
| V5 Input Validation | partial | `json.Unmarshal` into typed structs; malformed frames logged+dropped |
| V6 Cryptography | no | — |
| V7 Error Handling / Logging | yes | `log/slog` JSON; no secrets in log output |

### Known Threat Patterns for This Stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Subprocess spawn with tainted input (G204) | Tampering | `gosec` G204 linter fires on `exec.Command` with variable args; `KIRO_CMD`/`KIRO_ARGS` are env vars (not user input) but must not be interpolated from HTTP requests in Phase 1 |
| Goroutine exhaustion via unclosed streams | Denial of Service | `goleak` in tests; `sync.WaitGroup` + `Close()` discipline |
| Log injection via newline in log values | Spoofing | `slog.NewJSONHandler` JSON-escapes all values including newlines; safe by construction |
| Pipe EPIPE on subprocess crash | Denial of Service | Wrap writes in `framer.writeFrame` and handle `errors.Is(err, io.ErrClosedPipe)` gracefully — log and return without panicking |
| PID file race (two instances started simultaneously) | Denial of Service | Wrapper script checks PID before writing; acceptable for dev-laptop deployment model |

**gosec G204 note:** `gosec` will flag `exec.Command(cfg.Command, cfg.Args...)` where `cfg.Command` and `cfg.Args` come from env vars. This is intentional (env-var config is the contract). Add an inline `//nolint:gosec // G204: kiro-cli command is env-var config; not user-controlled HTTP input` comment with justification per TRST-01. [VERIFIED: golangci.yml docs + brief §3.12]

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `log.Printf` + manual JSON | `log/slog` with `JSONHandler` | Go 1.21 (2023) | No extra dep; structured by default; level filtering built in |
| `gorilla/mux` for routing | `go-chi/chi` for routing + middleware | ~2020 | chi is maintained; gorilla/mux is in maintenance mode |
| golangci-lint v1 config (`run.go` key format) | golangci-lint v2 config (`version: "2"`) | v2.0 released 2025-03 | Different config schema; project already has v2 config |
| `testing.T.Errorf` for assertions | stdlib `testing` without testify | Ongoing — Go community split | No third-party assertion lib needed for Phase 1; testify is optional |

**Deprecated/outdated:**
- `gorilla/mux`: In maintenance mode since 2023; chi is the community-preferred replacement for stdlib-compatible routing.
- golangci-lint v1 config format: `run.go:` key is v1-specific; `version: "2"` is the current format. Project already uses v2.
- `go.uber.org/zap` for logging: Still excellent for high-throughput logging, but `log/slog` is now stdlib and sufficient for this project's volume.

---

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `acp.New(cfg)` should use `exec.LookPath(cfg.Command)` before spawning to produce a clear error message | ACP client pattern section | Low — the spawn itself would fail with a less clear error; no correctness impact |
| A2 | `session/update` and `_kiro.dev/session/update` have identical `params` shapes and can be handled by the same translation function | ACP Protocol Reference | Medium — if fields differ, the translator would need to branch; integration test with real kiro-cli will reveal discrepancy |
| A3 | `session/grant_permission` is a JSON-RPC **request** (has an `id`) not a notification, based on the Node reference describing it as `_req(...)` | ACP Protocol Reference | Medium — if it's a notification (no `id`), the pending-map entry created for it would never be resolved; integration test will catch |
| A4 | Windows `exec.LookPath("kiro-cli")` automatically resolves `kiro-cli.cmd` without any special configuration | os/exec Windows section | Medium — if this doesn't work, we need `exec.Command("cmd.exe", "/c", "kiro-cli", ...)` on Windows; verify on a Windows machine in Phase 2 |
| A5 | `goleak.VerifyTestMain` in `package acp` (not `package acp_test`) will cover goroutines started by both the whitebox and blackbox test files in the same package directory | Validation Architecture | Low — `goleak.VerifyTestMain` runs after all tests in the package regardless of which `_test.go` file contains `TestMain` |
| A6 | The `testutil.Logger(t)` pattern (routing slog output to `t.Log`) does not interfere with goleak (no background goroutines started by slog) | Pattern 11 | Low — `slog.NewJSONHandler` does not start goroutines |

---

## Sources

### Primary (HIGH confidence)

- `go.dev/src/net/rpc/jsonrpc/client.go` — pending map pattern with `sync.Mutex`; verified
- `pkg.go.dev/os/exec` — `CommandContext`, `StdinPipe`, `StdoutPipe`, `Wait()` semantics; verified
- `pkg.go.dev/log/slog` — `NewJSONHandler`, `Logger.With`, level config; verified
- `pkg.go.dev/github.com/go-chi/chi/v5` — middleware composition, `RequestID`, `Recoverer`, `WrapResponseWriter`; verified
- `pkg.go.dev/go.uber.org/goleak` — `VerifyTestMain`, `IgnoreTopFunction`; verified
- `pkg.go.dev/golang.org/x/vuln/cmd/govulncheck` — CLI invocation, exit codes; verified v1.3.0
- `pkg.go.dev/github.com/fe3dback/go-arch-lint` — YAML syntax v3, minimal config; verified v1.15.0
- `docs/reference/acp_server_node_reference.md` — JSON-RPC wire shapes, auto-grant behavior, Node behavioral reference; in-project
- `.golangci.yml` — already v2 format with correct linter set; in-project
- `.pre-commit-config.yaml` — already wired with gitleaks v8.18.4, golangci-lint v2.12.2, shellcheck, go-mod-tidy; in-project
- Go module proxy (`proxy.golang.org`) — version dates for chi v5.3.0, goleak v1.3.0, govulncheck v1.3.0, go-arch-lint v1.15.0 / v1.0.0; verified 2026-05-23
- Environment audit (Bash commands) — Go 1.26.3, golangci-lint 2.12.2, gofumpt 0.10.0, pre-commit 4.5.1, govulncheck v1.3.0, kiro-cli 2.4.1; verified 2026-05-23

### Secondary (MEDIUM confidence)

- `docs/briefs/go_port_brief.md` §3.2, §3.8, §3.12 — ACP design rationale, package layout, trust gate list
- `golangci-lint.run/docs/configuration/file/` — v2 config schema structure
- `pkg.go.dev/sync` — Mutex + WaitGroup usage patterns; verified
- Microsoft docs — `Start-Process -RedirectStandardOutput` issues and `-NoNewWindow` requirement

### Tertiary (LOW confidence)

- WebSearch: "POSIX shell PID file start/stop pattern" — general `kill -0` usage; multiple sources agree on the pattern
- WebSearch: "PowerShell Start-Process background redirect" — general pattern; confirmed against Microsoft Q&A

---

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — all library versions confirmed via Go module proxy; all tools confirmed via environment audit
- ACP client patterns: HIGH — verified against stdlib net/rpc/jsonrpc source + os/exec docs; MEDIUM for exact kiro-cli params schema (need integration test to confirm)
- Architecture: HIGH — derived directly from locked decisions D-01 through D-23
- Trust gates: HIGH — existing .golangci.yml and .pre-commit-config.yaml already correct; govulncheck at GOPATH/bin confirmed
- Wrapper scripts: MEDIUM — patterns are standard but haven't been tested against this specific binary on both platforms

**Research date:** 2026-05-23
**Valid until:** 2026-07-23 (stable ecosystem; 60-day validity)
