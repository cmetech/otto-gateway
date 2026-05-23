# Phase 1: Foundations - Pattern Map

**Mapped:** 2026-05-23
**Files analyzed:** 21 new/modified files
**Analogs found:** 7 / 21 (the remaining 14 are greenfield Go with no in-repo or Bifrost analog; canonical Go stdlib/community patterns referenced instead)

---

## Greenfield Notice

This is a greenfield Go project. `internal/` directories contain only `.gitkeep` files. The only existing Go source is `cmd/loop24-gateway/main.go` (30 lines). All patterns are drawn from:

1. **In-repo files** — `main.go` stub, `Makefile`, `.golangci.yml`, `.pre-commit-config.yaml`, `scripts/setup-dev.sh`, `scripts/setup-dev.ps1`
2. **Bifrost reference** — `../bifrost/transports/bifrost-http/server/server.go` (signal handling, graceful shutdown shape); `../bifrost/transports/bifrost-http/main.go` (env-loading anti-pattern to diverge from); `../bifrost/cli/internal/config/config.go` (struct + error-wrapping style)
3. **Go stdlib** — `net/rpc/jsonrpc` (pending-map pattern), `bufio.Scanner` docs, `os/exec` docs, `log/slog` docs, `sync` docs
4. **RESEARCH.md** — verified code excerpts for every pattern (treat as authoritative where no codebase analog exists)

---

## File Classification

| New / Modified File | Role | Data Flow | Closest Analog | Match Quality |
|---------------------|------|-----------|----------------|---------------|
| `internal/acp/framer.go` | utility (I/O framer) | streaming / stdio | Go stdlib `bufio.Scanner` docs + RESEARCH.md Pattern 1 | no in-repo analog |
| `internal/acp/dispatcher.go` | utility (correlation) | request-response | Go stdlib `net/rpc/jsonrpc` pending-map + RESEARCH.md Pattern 2 | no in-repo analog |
| `internal/acp/client.go` | service / transport | request-response + streaming | Bifrost `server/server.go` (signal/goroutine lifecycle shape) | partial-match |
| `internal/acp/translate.go` | utility (transform) | transform | RESEARCH.md Pattern 5 + ACP protocol reference | no in-repo analog |
| `internal/acp/framer_test.go` | test (whitebox unit) | — | RESEARCH.md Pattern 10 + Pattern 11 | no in-repo analog |
| `internal/acp/dispatcher_test.go` | test (whitebox unit) | — | RESEARCH.md Pattern 10 + Pattern 11 | no in-repo analog |
| `internal/acp/client_test.go` | test (whitebox unit) | — | RESEARCH.md Pattern 10 + Pattern 11 | no in-repo analog |
| `internal/acp/integration_test.go` | test (blackbox integration) | — | RESEARCH.md D-17 skip pattern | no in-repo analog |
| `internal/acp/testmain_test.go` | test (goleak gate) | — | RESEARCH.md Pattern 10 | no in-repo analog |
| `internal/canonical/chunk.go` | model (domain type) | transform | RESEARCH.md Canonical Chunk Translation table | no in-repo analog |
| `internal/config/config.go` | config | — | `../bifrost/cli/internal/config/config.go` (struct+error style); diverges to env-var loading | partial-match |
| `internal/config/config_test.go` | test | — | RESEARCH.md Pattern 8 + testutil | no in-repo analog |
| `internal/server/server.go` | transport (HTTP) | request-response | Bifrost `server/server.go` (signal handling); diverges to `chi` + `net/http` | partial-match |
| `internal/server/health.go` | model (response type) | request-response | RESEARCH.md D-12 JSON shape | no in-repo analog |
| `internal/server/server_test.go` | test | — | RESEARCH.md Pattern 11 + `net/http/httptest` | no in-repo analog |
| `internal/testutil/testutil.go` | utility (test helper) | — | RESEARCH.md Pattern 11 | no in-repo analog |
| `internal/version/version.go` | utility | — | `cmd/loop24-gateway/main.go` lines 17, 21-28 (ldflags + ReadBuildInfo) | exact |
| `cmd/loop24-gateway/main.go` | entrypoint (wiring) | — | existing stub `cmd/loop24-gateway/main.go`; Bifrost `main.go` (wiring shape) | role-match |
| `Makefile` | config / build | — | existing `Makefile` (add `ci`, `start`, `stop`, `status` targets) | exact (additive) |
| `scripts/loop24` | utility (lifecycle) | — | `scripts/setup-dev.sh` (bash conventions: `set -euo pipefail`, `command -v`) | role-match |
| `scripts/loop24.ps1` | utility (lifecycle) | — | `scripts/setup-dev.ps1` (PS conventions: `Set-StrictMode`, `$ErrorActionPreference`) | role-match |
| `docs/operating.md` | doc | — | `DEVELOPERS.md` (doc prose style) | role-match |
| `.go-arch-lint.yml` | config | — | RESEARCH.md Pattern 12 | no in-repo analog |

---

## Pattern Assignments

### `internal/acp/framer.go` (utility, streaming/stdio)

**Analog:** No in-repo analog. Use RESEARCH.md Pattern 1 verbatim.

**Source:** RESEARCH.md §"Pattern 1: ACP Framer" + Go stdlib `bufio.Scanner` docs

**Imports pattern:**
```go
import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "sync"
)
```

**Core framer pattern** (from RESEARCH.md Pattern 1):
```go
type framer struct {
    scanner *bufio.Scanner // reads from subprocess stdout
    enc     *json.Encoder  // writes to subprocess stdin
    mu      sync.Mutex     // protects enc (single writer)
}

func newFramer(r io.Reader, w io.Writer) *framer {
    sc := bufio.NewScanner(r)
    sc.Buffer(make([]byte, 64*1024), 1024*1024) // 1 MB max — ACP frames can exceed 64 KB default
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
    copy(raw, f.scanner.Bytes()) // CRITICAL: scanner reuses its buffer; must copy before next Scan()
    return json.RawMessage(raw), nil
}

func (f *framer) writeFrame(v any) error {
    f.mu.Lock()
    defer f.mu.Unlock()
    return f.enc.Encode(v) // json.Encoder appends \n automatically — NDJSON compliant
}
```

**Critical pitfalls to copy-guard:**
- `sc.Buffer(...)` call is mandatory — default 64 KB limit is too small for ACP frames
- `copy(raw, f.scanner.Bytes())` is mandatory — scanner reuses buffer on next `Scan()`

---

### `internal/acp/dispatcher.go` (utility, request-response)

**Analog:** No in-repo analog. Pattern derived from Go stdlib `net/rpc/jsonrpc` client source.

**Source:** RESEARCH.md §"Pattern 2: ACP Dispatcher" + `go.dev/src/net/rpc/jsonrpc/client.go`

**Imports pattern:**
```go
import (
    "encoding/json"
    "sync"
)
```

**Core dispatcher pattern** (from RESEARCH.md Pattern 2):
```go
type rpcFrame struct {
    ID     *uint64         `json:"id,omitempty"`   // nil = notification (no id field)
    Method string          `json:"method,omitempty"`
    Result json.RawMessage `json:"result,omitempty"`
    Error  *rpcError       `json:"error,omitempty"`
    Params json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
}

type dispatcher struct {
    mu      sync.Mutex
    pending map[uint64]chan<- rpcFrame
    onNotif func(rpcFrame) // handles session/update, session/request_permission
}

// register creates a buffered-1 channel for a pending request ID.
// Buffered-1 ensures route() never blocks even if caller context-cancelled.
func (d *dispatcher) register(id uint64) <-chan rpcFrame {
    ch := make(chan rpcFrame, 1)
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

// route is called by the reader goroutine. Check ID nil FIRST — notifications
// never enter the pending map. Dropping session/request_permission here causes
// kiro-cli to block forever waiting for session/grant_permission.
func (d *dispatcher) route(frame rpcFrame) {
    if frame.ID == nil {
        d.onNotif(frame) // notification path — session/update, session/request_permission
        return
    }
    d.mu.Lock()
    ch, ok := d.pending[*frame.ID]
    if ok {
        delete(d.pending, *frame.ID) // read + delete must be atomic under the same lock
    }
    d.mu.Unlock()
    if ok {
        ch <- frame // non-blocking because chan is buffered-1
    }
    // Not found = stale response (caller context-cancelled). Drop silently.
}
```

**sync.Mutex vs sync.Map decision:** Use `sync.Mutex` + `map[uint64]chan<- rpcFrame`. `sync.Map` is optimized for append-mostly patterns; the dispatcher add+delete cycle on every request/response pair makes plain mutex faster and clearer.

---

### `internal/acp/client.go` (service/transport, request-response + streaming)

**Analog (partial):** Bifrost `../bifrost/transports/bifrost-http/server/server.go` — provides the goroutine + signal + shutdown shape. Diverges significantly because this is a subprocess client, not an HTTP server.

**Source:** RESEARCH.md Patterns 3, 4, 5, 6 + Bifrost `server.go` lines 1565-1630 (signal/goroutine lifecycle shape)

**Imports pattern:**
```go
import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "os"
    "os/exec"
    "sync"
    "sync/atomic"
    "time"

    "loop24-gateway/internal/canonical"
)
```

**Config struct pattern** (D-05: stdlib-style config struct, not functional options):
```go
// Config holds all configuration for the ACP client.
// Required field: Logger. All others have zero-value defaults.
type Config struct {
    Logger       *slog.Logger
    Command      string        // defaults to "kiro-cli"
    Args         []string      // defaults to ["acp"]
    Cwd          string        // working directory for subprocess
    Env          []string      // additional env vars; appended to os.Environ()
    PingInterval time.Duration // defaults to 60s
}
```

**Client struct + dual-constructor pattern** (D-06):
```go
type Client struct {
    cfg       Config
    framer    *framer
    disp      *dispatcher
    wg        sync.WaitGroup
    cancel    context.CancelFunc
    closeOnce sync.Once
    stdin     io.WriteCloser // subprocess stdin pipe; nil if NewWithConn
    cmd       *exec.Cmd     // non-nil only when New() spawned it
    nextID    atomic.Uint64
    // activeStream holds the current in-flight stream (Phase 1: one at a time)
    activeStream *Stream
    streamMu     sync.Mutex
}

// New spawns a kiro-cli subprocess and returns a connected Client.
// Ownership of the subprocess belongs to the Client; Close() reaps it.
func New(cfg Config) (*Client, error) { ... }

// NewWithConn accepts a pre-built io.ReadWriteCloser (Phase 5: pool owns *exec.Cmd).
func NewWithConn(rwc io.ReadWriteCloser, cfg Config) *Client { ... }

// Shared internals — called by both constructors.
func newClient(rwc io.ReadWriteCloser, cfg Config) *Client { ... }
```

**Subprocess spawn pattern** (from RESEARCH.md Pattern 6):
```go
func spawnSubprocess(cfg Config) (*exec.Cmd, io.ReadCloser, io.WriteCloser, error) {
    cmd := exec.Command(cfg.Command, cfg.Args...)
    // exec.LookPath on Windows auto-resolves .cmd/.exe via PATHEXT — no shell:true needed
    cmd.Dir = cfg.Cwd
    cmd.Env = append(os.Environ(), cfg.Env...)
    cmd.Stderr = os.Stderr // forward subprocess stderr to gateway stderr

    stdin, err := cmd.StdinPipe()
    if err != nil {
        return nil, nil, nil, fmt.Errorf("acp: stdin pipe: %w", err)
    }
    stdout, err := cmd.StdoutPipe()
    if err != nil {
        return nil, nil, nil, fmt.Errorf("acp: stdout pipe: %w", err)
    }
    if err := cmd.Start(); err != nil {
        return nil, nil, nil, fmt.Errorf("acp: start %q: %w", cfg.Command, err)
    }
    return cmd, stdout, stdin, nil
}
```

**gosec G204 annotation** — required on `exec.Command` call:
```go
//nolint:gosec // G204: kiro-cli command is env-var config; not user-controlled HTTP input
cmd := exec.Command(cfg.Command, cfg.Args...)
```

**Goroutine lifecycle pattern** (from RESEARCH.md Pattern 3):
```go
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
            return // EOF = subprocess exited or stdin closed by Close()
        }
        var f rpcFrame
        if err := json.Unmarshal(frame, &f); err != nil {
            c.cfg.Logger.Warn("acp: malformed frame", "err", err)
            continue // log-and-continue; don't kill session on parse error
        }
        c.disp.route(f)
    }
}
```

**Close() shutdown order** (from RESEARCH.md D-07 + Pitfall 2 + Pitfall 7):
```go
func (c *Client) Close() error {
    var firstErr error
    c.closeOnce.Do(func() { // idempotent via sync.Once
        c.cancel()          // 1. signal goroutines to stop
        if c.stdin != nil {
            if err := c.stdin.Close(); err != nil { // 2. EOF to subprocess
                firstErr = err
            }
        }
        c.wg.Wait()         // 3. wait for readLoop + pingLoop (BEFORE cmd.Wait)
        if c.cmd != nil {
            if err := c.cmd.Wait(); err != nil && firstErr == nil {
                firstErr = err // 4. reap subprocess
            }
        }
    })
    return firstErr
}
```

**Prompt + context-cancel pattern** (from RESEARCH.md Pattern 4):
```go
func (c *Client) Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (*Stream, error) {
    id := c.nextID.Add(1)
    respCh := c.disp.register(id)

    if err := c.framer.writeFrame(rpcRequest{
        JSONRPC: "2.0", ID: id, Method: "session/prompt",
        Params: promptParams{SessionID: sessionID, Blocks: blocks},
    }); err != nil {
        c.disp.cancel(id)
        return nil, fmt.Errorf("acp: prompt write: %w", err)
    }

    select {
    case <-ctx.Done():
        c.disp.cancel(id)
        _ = c.framer.writeFrame(rpcNotification{ // best-effort cancel; no id = notification
            JSONRPC: "2.0", Method: "session/cancel",
            Params: cancelParams{SessionID: sessionID},
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

**Notification handler pattern** (auto-grant + chunk translation, from RESEARCH.md Pattern 5):
```go
func (c *Client) handleNotification(frame rpcFrame) {
    switch frame.Method {
    case "session/request_permission":
        // CRITICAL: Must auto-grant immediately or kiro-cli blocks forever (ACP-04).
        var params permissionParams
        if err := json.Unmarshal(frame.Params, &params); err != nil {
            c.cfg.Logger.Warn("acp: malformed permission request", "err", err)
            return
        }
        _ = c.framer.writeFrame(rpcRequest{
            JSONRPC: "2.0", ID: c.nextID.Add(1),
            Method: "session/grant_permission",
            Params: grantParams{RequestID: params.RequestID, OptionID: "allow_always", Granted: true},
        })

    case "session/update", "_kiro.dev/session/update":
        var update sessionUpdateParams
        if err := json.Unmarshal(frame.Params, &update); err != nil {
            c.cfg.Logger.Warn("acp: malformed session update", "err", err)
            return
        }
        chunk := translateUpdate(update) // internal/acp/translate.go
        c.streamMu.Lock()
        s := c.activeStream
        c.streamMu.Unlock()
        if s != nil {
            s.push(chunk)
        }
    }
}
```

---

### `internal/acp/translate.go` (utility/transform)

**Analog:** No in-repo analog. Pure translation function, no external dependencies.

**Source:** RESEARCH.md §"Canonical Chunk Translation" table + ACP protocol reference

**Core translation pattern:**
```go
// translateUpdate converts a raw session/update or _kiro.dev/session/update
// notification params into a typed canonical.Chunk.
func translateUpdate(u sessionUpdateParams) canonical.Chunk {
    switch u.Type {
    case "text":
        return canonical.Chunk{Kind: canonical.ChunkKindText,
            Text: &canonical.TextChunk{Content: u.Content}}
    case "thought":
        return canonical.Chunk{Kind: canonical.ChunkKindThought,
            Thought: &canonical.ThoughtChunk{Content: u.Content}}
    case "tool_call":
        return canonical.Chunk{Kind: canonical.ChunkKindToolCall,
            ToolCall: &canonical.ToolCallChunk{Name: u.ToolName, Args: u.Args}}
    case "plan":
        return canonical.Chunk{Kind: canonical.ChunkKindPlan,
            Plan: &canonical.PlanChunk{Content: u.Content}}
    default:
        // Unknown chunk type — return as text to avoid data loss
        return canonical.Chunk{Kind: canonical.ChunkKindText,
            Text: &canonical.TextChunk{Content: u.Content}}
    }
}
```

**Note on open question (RESEARCH.md OQ-1):** The exact JSON field names for `_kiro.dev/session/update` vs `session/update` are unconfirmed. Both are assumed to have identical params shapes (Assumption A2). The integration test must log raw frames to verify. The `sessionUpdateParams` struct fields should be confirmed during Wave 0 integration test run with `DEBUG=1 kiro-cli acp`.

---

### `internal/acp/testmain_test.go` (test, goleak gate)

**Analog:** No in-repo analog. Direct from RESEARCH.md Pattern 10.

**Source:** RESEARCH.md §"Pattern 10: goleak.VerifyTestMain"

**Full file pattern:**
```go
package acp  // whitebox package — covers both whitebox and blackbox files in this directory

import (
    "os"
    "testing"

    "go.uber.org/goleak"
)

func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
    // If Phase 1 produces unexpected goroutine leaks, use:
    // goleak.VerifyTestMain(m, goleak.IgnoreTopFunction("...fully.qualified.func..."))
    // Do NOT suppress blindly — diagnose and fix the leak first.
}
```

**Note:** `goleak.VerifyTestMain` in `package acp` (not `package acp_test`) covers goroutines from both the whitebox and blackbox test files in the same directory (Assumption A5 in RESEARCH.md — low risk).

---

### `internal/acp/integration_test.go` (test, blackbox)

**Analog:** No in-repo analog. Pattern from RESEARCH.md D-17 (auto-skip) and D-18 (blackbox package).

**Key patterns:**

**Auto-skip at test start** (D-17):
```go
package acp_test // blackbox — exercises only exported API

import (
    "os"
    "os/exec"
    "testing"
)

func resolveKiroCLI(t *testing.T) string {
    t.Helper()
    if bin := os.Getenv("LOOP24_KIRO_BIN"); bin != "" {
        return bin
    }
    path, err := exec.LookPath("kiro-cli")
    if err != nil {
        t.Skip("kiro-cli not found on PATH; set LOOP24_KIRO_BIN to override")
    }
    return path
}

func TestIntegration_InitializeAndPing(t *testing.T) {
    bin := resolveKiroCLI(t) // t.Skip fires here if kiro-cli not found
    // ... test body using bin
}
```

---

### `internal/canonical/chunk.go` (model, domain types)

**Analog:** No in-repo analog. Leaf package — imports nothing under `internal/`.

**Source:** RESEARCH.md §"Canonical Chunk Translation" + D-04

**Full file pattern:**
```go
// Package canonical defines the typed chunk types that flow through
// the Loop24 gateway. This package imports nothing under internal/.
package canonical

// ChunkKind is the discriminator for a Chunk value.
type ChunkKind int

const (
    ChunkKindText     ChunkKind = iota
    ChunkKindThought
    ChunkKindToolCall
    ChunkKindPlan
)

// Chunk is a discriminated-union value produced by the ACP client and
// consumed by HTTP adapters. Exactly one of the pointer fields is non-nil,
// selected by Kind.
type Chunk struct {
    Kind     ChunkKind
    Text     *TextChunk
    Thought  *ThoughtChunk
    ToolCall *ToolCallChunk
    Plan     *PlanChunk
}

// TextChunk carries a text fragment from kiro-cli.
type TextChunk struct{ Content string }

// ThoughtChunk carries an internal reasoning fragment (not shown to end-users by default).
type ThoughtChunk struct{ Content string }

// ToolCallChunk carries a tool invocation from kiro-cli.
type ToolCallChunk struct {
    Name string
    Args map[string]any
}

// PlanChunk carries a plan fragment from kiro-cli.
type PlanChunk struct{ Content string }
```

---

### `internal/config/config.go` (config)

**Analog (partial):** `../bifrost/cli/internal/config/config.go` — provides the struct-plus-error-wrapping style. Diverges to env-var loading (not disk-based) per D-14.

**Source:** Bifrost config style (lines 1-10, 18-48: package declaration, imports, const block, struct) + RESEARCH.md Pattern 8

**Bifrost style reference** (`../bifrost/cli/internal/config/config.go` lines 1-10, style only):
```go
package config

import (
    "errors"
    "fmt"
    "os"
    // NOTE: We do NOT use sonic or any third-party JSON here — stdlib only per Phase 1 decisions
)
```

**Config struct and Load() pattern** (from RESEARCH.md Pattern 8 — use verbatim):
```go
// Config holds all gateway configuration loaded from environment variables.
// Phase 1 reads a subset; later phases add fields without changing Load()'s signature.
type Config struct {
    HTTPAddr     string
    KiroCmd      string
    KiroArgs     []string
    KiroCWD      string
    Debug        bool
    PingInterval time.Duration
}

// Load reads environment variables and returns a validated Config.
// Returns an error only if a required variable is malformed (not missing — all have defaults).
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
```

**Helper functions** (from RESEARCH.md Pattern 8):
```go
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
    return strings.Fields(v) // splits on whitespace; KIRO_ARGS="acp --some-flag"
}

func getEnvBool(key string, def bool) bool {
    v := strings.TrimSpace(os.Getenv(key))  // TrimSpace handles Windows trailing-space bug
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
    // Accept milliseconds for Node compat (PING_INTERVAL=60000 means 60s)
    if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
        return time.Duration(ms) * time.Millisecond
    }
    if d, err := time.ParseDuration(v); err == nil {
        return d
    }
    return def
}
```

**LogLevel helper** (used by main.go):
```go
// LogLevel returns the slog.Level implied by the Debug flag.
func (c Config) LogLevel() slog.Level {
    if c.Debug {
        return slog.LevelDebug
    }
    return slog.LevelInfo
}
```

---

### `internal/server/server.go` (transport/HTTP)

**Analog (partial):** Bifrost `server/server.go` lines 1565-1630 — signal handling + graceful shutdown shape. Diverges to `chi` + stdlib `net/http` (Bifrost uses `fasthttp` with no chi).

**Source:** RESEARCH.md Pattern 9 (graceful shutdown) + Pattern 7 (slog + middleware)

**Imports pattern:**
```go
import (
    "context"
    "errors"
    "log/slog"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/go-chi/chi/v5"
    "github.com/go-chi/chi/v5/middleware"

    "loop24-gateway/internal/config"
)
```

**Server struct + constructor:**
```go
// Server wraps the chi router and HTTP server with structured logging.
type Server struct {
    cfg     config.Config
    logger  *slog.Logger
    router  chi.Router
    version string
    start   time.Time
}

// New constructs a Server, registers middleware and routes, and returns it ready to serve.
func New(cfg config.Config, logger *slog.Logger, version string) *Server {
    s := &Server{cfg: cfg, logger: logger, version: version, start: time.Now()}
    s.router = chi.NewRouter()

    // Middleware order matters — RequestID must come before accessLog (Pitfall 5 in RESEARCH.md).
    s.router.Use(middleware.RequestID)
    s.router.Use(middleware.Recoverer)
    s.router.Use(accessLog(logger))

    s.router.Get("/health", s.healthHandler)
    s.router.Get("/api/version", s.versionHandler)
    return s
}
```

**Graceful shutdown pattern** (from RESEARCH.md Pattern 9 + Bifrost server.go lines 1571-1602):
```go
// RunUntilSignal starts the HTTP server and blocks until SIGINT, SIGTERM, or ctx cancellation.
// The 30s shutdown deadline is intentional (D-16).
func (s *Server) RunUntilSignal(ctx context.Context) error {
    srv := &http.Server{
        Addr:    s.cfg.HTTPAddr,
        Handler: s.router,
    }

    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    defer signal.Stop(sigCh)

    errCh := make(chan error, 1)
    go func() {
        s.logger.Info("listening", "addr", s.cfg.HTTPAddr)
        if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
            errCh <- err
        }
    }()

    select {
    case err := <-errCh:
        return err
    case <-sigCh:
        s.logger.Info("shutdown signal received")
    case <-ctx.Done():
        s.logger.Info("context cancelled; shutting down")
    }

    shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    return srv.Shutdown(shutdownCtx)
}
```

**accessLog middleware** (from RESEARCH.md Pattern 7):
```go
type loggerKey struct{}

func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            reqID := middleware.GetReqID(r.Context())
            reqLogger := logger.With("request_id", reqID)
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

// LoggerFromCtx returns the per-request logger stored by accessLog,
// or the fallback if not present.
func LoggerFromCtx(ctx context.Context, fallback *slog.Logger) *slog.Logger {
    if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
        return l
    }
    return fallback
}
```

---

### `internal/server/health.go` (model, response types)

**Analog:** No in-repo analog. Types derived from D-12 locked JSON contract.

**Source:** RESEARCH.md D-12 JSON shape

**Full type definitions** (locked contract — additive only across phases):
```go
// HealthResponse is the JSON body returned by GET /health.
// Shape is locked per D-12; future phases add fields to sub-structs only.
type HealthResponse struct {
    Status        string        `json:"status"`
    Version       string        `json:"version"`
    UptimeSeconds float64       `json:"uptime_seconds"`
    Pool          PoolStats     `json:"pool"`
    Sessions      SessionStats  `json:"sessions"`
    Embeddings    EmbeddingStats `json:"embeddings"`
}

// PoolStats reports ACP worker subprocess pool state.
// Populated by Phase 5; zero values correct for Phase 1.
type PoolStats struct {
    Size  int `json:"size"`
    Alive int `json:"alive"`
    Busy  int `json:"busy"`
}

// SessionStats reports active ACP session state.
// Populated by Phase 5; zero values correct for Phase 1.
type SessionStats struct {
    Active int `json:"active"`
}

// EmbeddingStats reports loaded embedding model state.
// Populated by Phase 7; zero values correct for Phase 1.
type EmbeddingStats struct {
    ModelsLoaded int `json:"models_loaded"`
}
```

**healthHandler** (Phase 1 always returns 200, always zero sub-stats per D-12 / Deferred):
```go
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    resp := HealthResponse{
        Status:        "ok",
        Version:       s.version,
        UptimeSeconds: time.Since(s.start).Seconds(),
        // Pool, Sessions, Embeddings are zero-value — correct for Phase 1
    }
    if err := json.NewEncoder(w).Encode(resp); err != nil {
        s.logger.Error("health encode", "err", err)
    }
}
```

---

### `internal/testutil/testutil.go` (utility, test helper)

**Analog:** No in-repo analog. Full pattern from RESEARCH.md Pattern 11 — use verbatim.

**Source:** RESEARCH.md §"Pattern 11: testutil.Logger(t)"

**Full file pattern:**
```go
// Package testutil provides shared test helpers for internal packages.
// Zero external dependencies — only stdlib + testing.
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
// Output is test-scoped: visible only when the test fails or -v is set.
// Use in every internal package test to avoid global slog state (D-15).
func Logger(t *testing.T) *slog.Logger {
    t.Helper()
    return slog.New(slog.NewJSONHandler(testWriter{t}, &slog.HandlerOptions{
        Level: slog.LevelDebug,
    }))
}
```

---

### `internal/version/version.go` (utility)

**Analog (exact):** `cmd/loop24-gateway/main.go` lines 17-28 — the existing stub already demonstrates the ldflags pattern and `debug.ReadBuildInfo()` idiom.

**Source:** `cmd/loop24-gateway/main.go` lines 17-28

**Existing pattern to extract** (from `cmd/loop24-gateway/main.go` lines 17-28):
```go
// version is overridden at build time via -ldflags="-X main.version=...".
var version = "0.0.0-dev"

// in main():
commit := "unknown"
if info, ok := debug.ReadBuildInfo(); ok {
    for _, s := range info.Settings {
        if s.Key == "vcs.revision" && len(s.Value) >= 7 {
            commit = s.Value[:7]
            break
        }
    }
}
```

**Adaptation for `internal/version/version.go`:**
```go
// Package version exposes build-time version information embedded via -ldflags.
package version

import "runtime/debug"

// Version is set at build time: -ldflags="-X loop24-gateway/internal/version.Version=1.2.3"
// Falls back to "0.0.0-dev" for local builds without -ldflags.
var Version = "0.0.0-dev"

// Commit returns the first 7 characters of the VCS commit hash from build metadata.
// Returns "unknown" if build info is unavailable (e.g., `go run` without a module).
func Commit() string {
    if info, ok := debug.ReadBuildInfo(); ok {
        for _, s := range info.Settings {
            if s.Key == "vcs.revision" && len(s.Value) >= 7 {
                return s.Value[:7]
            }
        }
    }
    return "unknown"
}
```

**Makefile ldflags update required:** Change `-X main.version=` to `-X loop24-gateway/internal/version.Version=` in `LDFLAGS`.

---

### `cmd/loop24-gateway/main.go` (entrypoint, wiring)

**Analog (role-match):** Existing stub `cmd/loop24-gateway/main.go` (lines 1-30) + Bifrost `transports/bifrost-http/main.go` (lines 89-162) for wiring shape.

**Source:** Existing `main.go` (all 30 lines) + Bifrost `main.go` wiring shape (lines 148-162)

**Bifrost wiring shape reference** (lines 148-162, adapted):
```go
// Bifrost pattern: Bootstrap → Start; loop24 equivalent: Load → Build → Wire → Run
ctx := context.Background()
err := server.Bootstrap(ctx)
if err != nil {
    logger.Error("failed to bootstrap server: %v", err)
    os.Exit(1)
}
err = server.Start()
```

**Target main.go wiring** (from CONTEXT.md §Integration Points):
```go
package main

import (
    "log/slog"
    "os"

    "loop24-gateway/internal/acp"
    "loop24-gateway/internal/config"
    "loop24-gateway/internal/server"
    "loop24-gateway/internal/version"
)

func main() {
    cfg, err := config.Load()
    if err != nil {
        slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("config load failed", "err", err)
        os.Exit(1)
    }

    logger := buildLogger(cfg)

    acpCfg := acp.Config{
        Logger:       logger,
        Command:      cfg.KiroCmd,
        Args:         cfg.KiroArgs,
        Cwd:          cfg.KiroCWD,
        PingInterval: cfg.PingInterval,
    }
    acpClient, err := acp.New(acpCfg)
    if err != nil {
        logger.Error("acp client init failed", "err", err)
        os.Exit(1)
    }
    defer func() {
        if err := acpClient.Close(); err != nil {
            logger.Error("acp client close", "err", err)
        }
    }()

    srv := server.New(cfg, logger, version.Version)
    if err := srv.RunUntilSignal(context.Background()); err != nil {
        logger.Error("server stopped with error", "err", err)
        os.Exit(1)
    }
}

func buildLogger(cfg config.Config) *slog.Logger {
    return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level: cfg.LogLevel(),
    }))
}
```

**D-15 enforcement:** No `slog.SetDefault(...)` anywhere. Logger constructed once in `main()` and injected.

---

### `Makefile` (config/build — additive changes)

**Analog (exact):** Existing `Makefile` (all 57 lines) — add targets to it.

**Source:** Existing `Makefile` + RESEARCH.md §"govulncheck invocation for make ci"

**Targets to add:**

```makefile
.PHONY: ci start stop status

ci: lint test-race ## Full CI gate (lint + race-tests + vuln scan)
	$(shell go env GOPATH)/bin/govulncheck ./...

start: ## Start gateway in background (wrapper script)
	@scripts/loop24 start

stop: ## Stop background gateway (wrapper script)
	@scripts/loop24 stop

status: ## Show gateway status (wrapper script)
	@scripts/loop24 status
```

**Pattern to follow from existing Makefile** (lines 5-9, 43-53): `GOPATH`-relative tool paths; `@` prefix to suppress command echo; `## comment` for `make help` output.

**govulncheck path note:** `govulncheck` is at `$(go env GOPATH)/bin/govulncheck`, not on PATH in this environment. The `$(shell ...)` expansion in the recipe (not in the variable definition) evaluates at recipe time — correct for this use case.

---

### `scripts/loop24` (POSIX shell lifecycle script)

**Analog (role-match):** `scripts/setup-dev.sh` (lines 1-7: shebang + `set -euo pipefail` + comments) and RESEARCH.md Pattern §"POSIX shell (`scripts/loop24`)"

**Shebang + safety pattern** (from `scripts/setup-dev.sh` lines 1-6):
```bash
#!/usr/bin/env bash
set -euo pipefail
```

**Core start/stop/status functions** (from RESEARCH.md §"Wrapper Scripts Reference"):
```bash
LOOP24_BIN="${LOOP24_BIN:-./bin/loop24-gateway}"
LOOP24_PID="${LOOP24_PID:-/tmp/loop24-gateway.pid}"
LOOP24_LOG="${LOOP24_LOG:-/tmp/loop24-gateway.log}"
LOOP24_ADDR="${LOOP24_ADDR:-http://localhost:11434}"

start() {
    if [[ -f "$LOOP24_PID" ]]; then
        local pid; pid=$(cat "$LOOP24_PID")
        if kill -0 "$pid" 2>/dev/null; then
            echo "loop24-gateway is already running (PID $pid)" >&2; exit 1
        fi
        rm -f "$LOOP24_PID"
    fi
    nohup "$LOOP24_BIN" >> "$LOOP24_LOG" 2>&1 &
    echo $! > "$LOOP24_PID"
    echo "loop24-gateway started (PID $(cat "$LOOP24_PID"))"
}

stop() {
    if [[ ! -f "$LOOP24_PID" ]]; then
        echo "loop24-gateway is not running (no PID file)" >&2; exit 1
    fi
    local pid; pid=$(cat "$LOOP24_PID")
    if ! kill -0 "$pid" 2>/dev/null; then
        echo "loop24-gateway: stopped (stale PID)"; rm -f "$LOOP24_PID"; exit 1
    fi
    kill "$pid" && rm -f "$LOOP24_PID"
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
    if command -v curl >/dev/null 2>&1; then
        curl -sf "${LOOP24_ADDR}/health" 2>/dev/null | python3 -m json.tool 2>/dev/null || true
    fi
}
```

**shellcheck compliance required:** The pre-commit `shellcheck` hook will run on this file. Key rules: quote all variable expansions (`"$pid"`, `"$LOOP24_BIN"`), use `local` for function-scoped variables, use `[[ ]]` for tests (not `[ ]`), avoid unquoted `$!` before redirect.

---

### `scripts/loop24.ps1` (PowerShell lifecycle script)

**Analog (role-match):** `scripts/setup-dev.ps1` (lines 1-10: requires version, strict mode, error preference) and RESEARCH.md §"PowerShell (`scripts/loop24.ps1`)"

**Header pattern** (from `scripts/setup-dev.ps1` lines 1-9):
```powershell
#Requires -Version 5.1
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
```

**Core pattern** (from RESEARCH.md §"PowerShell" section):
```powershell
param([string]$Command = "help")

$BinPath = if ($env:LOOP24_BIN)  { $env:LOOP24_BIN }  else { ".\bin\loop24-gateway.exe" }
$PidFile = if ($env:LOOP24_PID)  { $env:LOOP24_PID }  else { "$env:TEMP\loop24-gateway.pid" }
$LogFile = if ($env:LOOP24_LOG)  { $env:LOOP24_LOG }  else { "$env:TEMP\loop24-gateway.log" }
$LogErrFile = "$env:TEMP\loop24-gateway-err.log"  # separate file — Start-Process can't share stdout/stderr
$Addr    = if ($env:LOOP24_ADDR) { $env:LOOP24_ADDR } else { "http://localhost:11434" }

function Start-Gateway {
    if (Test-Path $PidFile) {
        $existingPid = [int](Get-Content $PidFile)
        if (Get-Process -Id $existingPid -ErrorAction SilentlyContinue) {
            Write-Error "loop24-gateway is already running (PID $existingPid)"; exit 1
        }
        Remove-Item $PidFile
    }
    $proc = Start-Process -FilePath $BinPath `
        -RedirectStandardOutput $LogFile `
        -RedirectStandardError  $LogErrFile `
        -NoNewWindow -PassThru   # NoNewWindow: no console popup (Pitfall 8 in RESEARCH.md)
    $proc.Id | Set-Content $PidFile
    Write-Host "loop24-gateway started (PID $($proc.Id))"
}
```

**Key divergence from `setup-dev.ps1`:** lifecycle script uses `param()` for subcommand dispatch; setup script does not. Use `switch ($Command)` at the bottom to dispatch to functions.

---

### `.golangci.yml` (config — verify, no changes expected)

**Analog (exact):** Existing `.golangci.yml` (all 68 lines) — RESEARCH.md confirms it already matches brief §3.12.

**Verification task:** Run `golangci-lint run ./...` against the Phase 1 scaffold once `internal/acp`, `internal/config`, and `internal/server` are written. The `unused` linter may flag `version.Version` in the stub `main.go` until it's referenced in the health handler. This is expected and will self-resolve.

**No changes required** unless a new linter finding is discovered.

---

### `.go-arch-lint.yml` (config — new file, scaffolded)

**Analog:** No in-repo analog. Full pattern from RESEARCH.md Pattern 12.

**Full file pattern** (from RESEARCH.md §"Pattern 12: go-arch-lint scaffold"):
```yaml
# .go-arch-lint.yml — Phase 1 scaffold; dependency rules activate in Phase 2.
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

# No deps rules in Phase 1. Phase 2 activates:
#   canonical: {}           # imports nothing under internal/
#   acp:
#     mayDependOn: [canonical]
#   server:
#     mayDependOn: [canonical, config, version]
```

**Checkpoint required:** `github.com/fe3dback/go-arch-lint@latest` is flagged SUS by slopcheck (v1.15.0 released 18 days ago). Install must be gated behind a `checkpoint:human-verify` task. Alternatively pin to `@v1.14.0` until v1.15.0 has more soak time. See RESEARCH.md §"Package Legitimacy Audit".

---

### `docs/operating.md` (doc)

**Analog (role-match):** `DEVELOPERS.md` — prose style, section structure, env-var table format.

**Structure to follow** (from `DEVELOPERS.md`): H2 sections, env-var tables with `| Variable | Default | Description |` header, code blocks for commands, callout blocks for pitfalls. Content driven by D-23 scope: PID/log file locations, env-var overrides, status computation description.

---

## Shared Patterns

### Structured logging — explicit injection (D-15)

**Source:** RESEARCH.md Pattern 7 + `cmd/loop24-gateway/main.go`

**Apply to:** All packages that need a logger — `internal/acp`, `internal/server`, test files

```go
// Construction (main.go only):
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel()}))

// Injection into packages via Config struct:
acp.Config{Logger: logger, ...}
server.New(cfg, logger, version.Version)

// In tests (every _test.go file in internal/):
logger := testutil.Logger(t)

// NEVER:
slog.SetDefault(logger) // D-15 — banned
```

### Error wrapping with fmt.Errorf

**Source:** Bifrost `cli/internal/config/config.go` line 74 (`fmt.Errorf("read config file: %w", err)`) + `wrapcheck` linter requirement

**Apply to:** All functions returning errors from other packages

```go
// CORRECT — %w wraps for errors.Is/errors.As:
return nil, fmt.Errorf("acp: start %q: %w", cfg.Command, err)

// WRONG — wrapcheck linter will flag this:
return nil, err
```

### Context as first argument (D-02)

**Apply to:** Every exported function that does I/O, blocks, or calls into a subprocess

```go
// CORRECT:
func (c *Client) Initialize(ctx context.Context) error { ... }
func (c *Client) Prompt(ctx context.Context, sid string, blocks []canonical.Block) (*Stream, error) { ... }

// WRONG (and noctx linter will flag HTTP handlers that lack ctx propagation):
func (c *Client) Ping() error { ... }
```

### No global state

**Apply to:** All packages

```go
// NEVER in any internal package:
var defaultClient *acp.Client  // global singleton
var logger = slog.Default()    // global logger

// ALWAYS: pass via Config struct or function argument
```

---

## No Analog Found

Files with no close match in this codebase or the Bifrost reference (planner must rely on RESEARCH.md patterns and stdlib docs):

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `internal/acp/framer.go` | utility | streaming/stdio | No subprocess I/O client exists in this repo or Bifrost |
| `internal/acp/dispatcher.go` | utility | request-response | No JSON-RPC dispatcher exists in this repo or Bifrost |
| `internal/acp/translate.go` | utility | transform | No ACP-to-canonical translator exists anywhere |
| `internal/acp/*_test.go` | tests | — | No existing Go tests to copy from |
| `internal/canonical/chunk.go` | model | — | New domain type; no analog |
| `internal/server/health.go` | model | — | New response type; no analog |
| `internal/testutil/testutil.go` | utility | — | New test helper; no analog |
| `.go-arch-lint.yml` | config | — | New tool; no prior usage in repo |

For all files in this table: use the RESEARCH.md code excerpts as the authoritative pattern source. They were verified against official package documentation on 2026-05-23.

---

## Metadata

**Analog search scope:** `cmd/`, `internal/`, `scripts/`, `Makefile`, `.golangci.yml`, `.pre-commit-config.yaml` (this repo) + `../bifrost/transports/bifrost-http/` and `../bifrost/cli/internal/config/` (Bifrost reference)
**Files scanned:** 12 (this repo) + 3 (Bifrost)
**Pattern extraction date:** 2026-05-23
