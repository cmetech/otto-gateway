# Phase 5: Pool + Stateful Sessions - Pattern Map

**Mapped:** 2026-05-26
**Files analyzed:** 22 files (8 NEW packages/files, 14 MODIFY)
**Analogs found:** 22 / 22 — every new file has a concrete in-repo analog (Phase 5 is wiring + one new package + one new endpoint; no greenfield)

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `internal/session/registry.go` (NEW) | subprocess-lifecycle owner / registry | CRUD + event-driven | `internal/pool/pool.go` | exact (slot-owner ↔ entry-owner) |
| `internal/session/reaper.go` (NEW) | background goroutine + ticker | event-driven (ticker) | `internal/acp/client.go` pingLoop (lines 433-453) | role-match (ticker + ctx-cancel + Logger.Warn) |
| `internal/session/config.go` (NEW) | config struct + applyDefaults | config | `internal/pool/config.go` | exact |
| `internal/session/stats.go` (NEW) | per-session detail rendering | request-response (read-side) | `internal/pool/stats.go` + Code Examples block 4 in RESEARCH | exact |
| `internal/session/entry_acp.go` (NEW) | `engine.ACPClient` adapter | request-response | `internal/engine/acp_adapter.go` (acpClientAdapter + acpStreamShim) | exact |
| `internal/session/testmain_test.go` (NEW) | goleak gate | test infra | `internal/pool/testmain_test.go` | exact |
| `internal/session/registry_test.go` (NEW) | unit tests (Get/Delete/lazy-create race) | test | `internal/pool/pool_test.go` (fakeClient + fakeClientFactory + warmedPoolWithFakes) | exact |
| `internal/session/reaper_test.go` (NEW) | real-time short-TTL test | test | RESEARCH §Code Examples block 5 + `internal/pool/pool_test.go` patterns | exact (sketch) |
| `internal/session/export_test.go` (NEW) | whitebox test-only accessors | test infra | `internal/pool/export_test.go` | exact |
| `internal/acp/client.go` (MODIFY) | add `Done() <-chan struct{}` | infra (push exit signal) | self — one-line accessor over existing `clientCtx` (line 246) | exact (new method on existing struct) |
| `internal/acp/client_test.go` (MODIFY) | Done() exit-signal test | test | existing `internal/acp/client_test.go` Close-paths tests | role-match |
| `internal/pool/pool.go` (MODIFY) | dead-slot detection + lazy re-spawn + exit watcher | event-driven + CRUD | self — `Pool.NewSession` (lines 263-284), `releaseSlotForSession` (398-408), `closeAll` (216-238) | exact (extension) |
| `internal/pool/exit_watcher.go` (NEW) | per-slot watcher goroutine | event-driven | `internal/acp/client.go` pingLoop (433-453) + Pool.closeAll | role-match |
| `internal/pool/pool_test.go` (MODIFY) | dead-slot detection + respawn tests | test | existing TestPool_Cancel_RoutesToCorrectSlot + WaitForSlotRelease | exact |
| `internal/pool/exit_watcher_test.go` (NEW) | goleak watcher-teardown test | test | `internal/pool/testmain_test.go` + Pool.Close tests | role-match |
| `internal/pool/config.go` (MODIFY — package default stays at 1) | no change needed; the env default flips in `internal/config/config.go` | config | self | exact |
| `internal/server/server.go` (MODIFY) | add `RegistryStatsSource` interface + `/health/agents` route + DELETE wire | HTTP wiring | self — `PoolStatsSource` (25-31) + exempt routes (164-172) + `NewFromConfig` (150-212) | exact (parallel surface) |
| `internal/server/health.go` (MODIFY) | populate `HealthResponse.Sessions.Active` from registry | request-response | self — `healthHandler` (lines 57-75) | exact |
| `internal/server/agents.go` (NEW) | `agentsHandler` + JSON wire types | request-response | `internal/server/health.go` (healthHandler lines 57-75) | exact |
| `internal/server/agents_test.go` (NEW) | wire-shape tests | test | `internal/server/server_test.go` patterns + RESEARCH Code Examples block 4 | role-match |
| `internal/server/sessions_delete.go` (NEW) | `DELETE /v1/sessions/:id` handler | CRUD | RESEARCH §Pattern 5 + `internal/server/health.go` writeJSON / writeError patterns | role-match |
| `internal/server/sessions_delete_test.go` (NEW) | 200 + 404 + cancels-in-flight | test | `internal/adapter/anthropic/handlers_test.go` style | role-match |
| `internal/engine/engine.go` (REVIEW only) | confirm `*session.Entry` satisfies `ACPClient` | interface | self — `ACPClient` (34-50) | exact (compile-time assertion via `var _ engine.ACPClient = (*session.Entry)(nil)`) |
| `internal/engine/acp_adapter.go` (REVIEW only) | confirm acpStreamShim pattern transfers | interface | self — acpStreamShim (66-90) | exact |
| `internal/config/config.go` (MODIFY) | `SESSION_TTL_MS`, `SESSION_MAX`, flip env default of `POOL_SIZE` to 4 | config | self — `getEnvInt` / `getEnvDuration` helpers (lines 381-456), `Load` (103-168) | exact |
| `cmd/otto-gateway/main.go` (MODIFY) | wire registry; start reaper; ordered shutdown | wiring | self — `newApp` (117-302), `cleanup` closure (122-128), `poolStatsAdapter` (309-316) | exact |
| `internal/adapter/{ollama,openai,anthropic}/handlers.go` (MODIFY each) | read `X-Session-Id`; route via registry vs pool | request-response | self — existing handlers per surface; surface-aware engine dispatch already in place | exact |

## Pattern Assignments

---

### `internal/session/registry.go` (NEW — Registry + Entry + Get + Delete + Close)

**Analog:** `internal/pool/pool.go`
**Why:** Both packages own kiro-cli subprocess lifecycle keyed by an id (slot label vs `sid`), both expose an interface satisfied by their primary handle, and both must coordinate terminal-path races via the Codex M-3 map-delete-first pattern.

**Imports pattern** (copy verbatim — pool.go lines 1-11):
```go
package session

import (
    "context"
    "errors"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "otto-gateway/internal/acp"
    "otto-gateway/internal/canonical"
    "otto-gateway/internal/engine"
)
```

**Struct layout pattern** (model from pool.go lines 37-66 — fields + invariants + guarding comments):
```go
// From pool.go:37-66 — adapt the comment shape to the registry domain.
type Pool struct {
    cfg Config
    slots chan *Slot                  // → not present in registry; entries are map-only
    all []*Slot                       // → Registry.entries map[string]*Entry
    sessionSlots map[string]*Slot     // → Registry.entries directly
    mu sync.Mutex                     // → registry uses sync.RWMutex (reaper iterates read-only; writes are Get/Delete)
    closed bool                       // → Registry.closed flag
    closeOnce sync.Once               // → mirror exactly
}
```

The Registry struct adds (vs Pool):
- `closing chan struct{}` — closed by `Close()` so reaper exits via `<-r.closing`
- `wg sync.WaitGroup` — `wg.Wait()` in `Close()` to ensure reaper goroutine has exited (goleak gate)

**`New` constructor pattern** (copy from pool.go lines 71-78):
```go
// pool.go:71-78
func New(cfg Config) *Pool {
    cfg.applyDefaults()
    return &Pool{
        cfg:          cfg,
        slots:        make(chan *Slot, cfg.Size),
        sessionSlots: make(map[string]*Slot),
    }
}
```
Mirror in registry: allocate `entries` map and `closing` channel; call `cfg.applyDefaults()`.

**Codex M-3 map-delete-first pattern** (copy from pool.go lines 326-344 — THIS IS THE LOAD-BEARING PATTERN for the registry's Reaper/DELETE/disconnect/Result race):
```go
// pool.go:326-344 — verbatim
release := func() {
    p.mu.Lock()
    s, stillOwned := p.sessionSlots[sid]
    if stillOwned {
        delete(p.sessionSlots, sid)
    }
    p.mu.Unlock()
    if !stillOwned {
        // Another terminal path (Cancel) won the race and already
        // deleted the entry — that path will (or already did) send
        // to p.slots. Skip the send to avoid double-release.
        return
    }
    p.slots <- s
}
```

Registry's `Delete` body MUST follow the same shape (lock → check → delete-first → unlock → only then call slow `Close()`):
```go
// registry.go — mirror of pool.go:326-344 for DELETE /v1/sessions/:id
func (r *Registry) Delete(sid string) error {
    r.mu.Lock()
    e, ok := r.entries[sid]
    if !ok {
        r.mu.Unlock()
        return ErrSessionNotFound  // → 404
    }
    delete(r.entries, sid)  // map-delete FIRST — future Get(sid) returns "not found"
    r.mu.Unlock()
    e.Client.Cancel(e.SessionID)   // cancel in-flight prompt (D-08)
    return e.Client.Close()
}
```

**Critical anti-pattern from pool.go (do NOT violate):** never hold `r.mu` across `e.Client.Close()` — `Close()` waits on the subprocess WaitGroup (acp/client.go:947) and can block on EOF. Pool's `closeAll` at lines 216-238 demonstrates the snapshot-then-act discipline:
```go
// pool.go:216-238 — copy this discipline for registry shutdown.
func (p *Pool) closeAll() error {
    p.mu.Lock()
    slots := p.all
    p.all = nil
    p.closed = true
    p.mu.Unlock()
    // ... iterate slots, call Close() WITHOUT holding p.mu
}
```

**`Get` lazy-create race resolution (Pitfall 4):**
The naive "check map, if absent spawn, write map" is racy. Use a creating-sentinel under the registry mutex (RESEARCH §Pattern 3 + Pitfall 4). Two concurrent same-sid requests must NOT both spawn a kiro-cli — Spawn happens outside the lock but a placeholder entry under the lock serializes them.

**Error types** (define alongside Registry):
```go
var (
    ErrSessionNotFound     = errors.New("session: not found")
    ErrSessionMaxExceeded  = errors.New("session: max sessions exceeded")  // D-06 → 503
)
```

---

### `internal/session/reaper.go` (NEW — single goroutine ticker loop)

**Analog:** `internal/acp/client.go` pingLoop (lines 433-453) — same shape (ticker + ctx cancellation + structured Logger.Warn on terminal events).

**Ticker + ctx-cancel pattern** (copy from acp/client.go:433-453 — note the `defer ticker.Stop()` and the two-branch select):
```go
// acp/client.go:433-453 — pingLoop
func (c *Client) pingLoop(ctx context.Context) {
    defer c.wg.Done()
    ticker := time.NewTicker(c.cfg.PingInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
            if err := c.Ping(pingCtx); err != nil {
                cancel()
                if errors.Is(err, context.Canceled) || errors.Is(err, ErrClientClosed) {
                    return // expected on Close()
                }
                c.cfg.Logger.Warn("acp: ping failed", "err", err)
                return
            }
            cancel()
        case <-ctx.Done():
            return
        }
    }
}
```

Mirror in `reaper.go`:
```go
// registry-reaper — same shape; r.closing replaces the pingLoop's ctx.Done in the second branch.
func (r *Registry) reaperLoop() {
    defer r.wg.Done()
    ticker := time.NewTicker(r.cfg.TickInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            r.reapOnce()
        case <-r.closing:
            return
        }
    }
}
```

**`reapOnce` snapshot-then-iterate** (lock-order discipline — RESEARCH §Pattern 4 verbatim, lines 535-569):
- Take `r.mu.RLock()` ONLY to copy `entries` into a slice
- Release the registry mutex
- Iterate the snapshot, calling `e.Mu.TryLock()` per entry
- If TryLock succeeds AND `time.Since(e.LastUsed) > cfg.TTL`: defensively `Cancel()`, `Close()`, then map-delete-first under `r.mu.Lock()`

**Logging hook** (slog pattern from acp/client.go — `Logger.Warn` for unexpected, `Logger.Info` for expected lifecycle):
```go
r.cfg.Logger.Info("session: reaped", "sid", es.sid, "idle_for", now.Sub(es.entry.LastUsed))
```

---

### `internal/session/config.go` (NEW — Config + applyDefaults)

**Analog:** `internal/pool/config.go` (lines 1-128) — copy verbatim, swap pool-specific fields for session-specific fields.

**Imports pattern** (copy from pool/config.go lines 1-19):
```go
// Package session implements a registry of dedicated kiro-cli sessions keyed
// by client-supplied X-Session-Id. Lives entirely outside the warm pool
// (Phase 5 D-04). Sessions are lazy-created on first request, capped at
// SESSION_MAX, and reaped after SESSION_TTL_MS of idleness.
package session

import (
    "context"
    "log/slog"
    "time"

    "otto-gateway/internal/acp"
    "otto-gateway/internal/canonical"
)
```

**Config struct + applyDefaults** (mirror pool/config.go:87-122):
```go
// pool/config.go:87-122 — the model.
type Config struct {
    Logger       *slog.Logger
    Size         int               // → not in registry
    KiroCmd      string
    KiroArgs     []string
    KiroCWD      string
    PingInterval time.Duration
    Factory      ClientFactory
}

func (c *Config) applyDefaults() {
    if c.Size <= 0 {
        c.Size = 1                   // → in registry: MaxSessions default 32
    }
    if c.Factory == nil {
        c.Factory = acpClientFactory{}
    }
}
```

Session Config fields (per CONTEXT D-06, D-10, D-13):
```go
type Config struct {
    Logger       *slog.Logger
    Factory      ClientFactory       // copy from pool/config.go:60-62
    TTL          time.Duration       // D-10 default 30min; D-13 tests inject 200ms
    TickInterval time.Duration       // D-10 default 60s; D-13 tests inject 50ms
    MaxSessions  int                 // D-06 default 32
    KiroCmd      string              // forwarded to acp.Config
    KiroArgs     []string
    KiroCWD      string              // default cwd for sessions
    PingInterval time.Duration
}

func (c *Config) applyDefaults() {
    if c.TTL <= 0 {
        c.TTL = 30 * time.Minute      // SESSION_TTL_MS=1_800_000 — Node parity
    }
    if c.TickInterval <= 0 {
        c.TickInterval = 60 * time.Second
    }
    if c.MaxSessions <= 0 {
        c.MaxSessions = 32
    }
    if c.Factory == nil {
        c.Factory = acpClientFactory{}
    }
}
```

**ClientFactory + acpClientFactory** (copy from pool/config.go:60-82 verbatim — the interface seam letting tests inject fake clients without spawning real kiro-cli):
```go
// pool/config.go:60-82 — reuse identical seam in internal/session.
type ClientFactory interface {
    Spawn(ctx context.Context, cfg acp.Config) (PoolClient, error)
}

type acpClientFactory struct{}

func (acpClientFactory) Spawn(_ context.Context, cfg acp.Config) (PoolClient, error) {
    c, err := acp.New(cfg)
    if err != nil {
        return nil, err //nolint:wrapcheck
    }
    return c, nil
}
```

Note: planner may either re-export `pool.PoolClient` and reuse `pool.ClientFactory`, OR define a separate `session.ClientFactory` with the same interface for cleaner package boundaries. Either works; pool/config.go is the model.

---

### `internal/session/stats.go` (NEW — per-session detail rendering)

**Analog:** `internal/pool/stats.go` (lines 1-19) — shape only; registry needs richer rows (D-16) than pool's `Stats`.

**Pattern** (copy struct doc-comment style + tags from pool/stats.go):
```go
// pool/stats.go:1-19 — the model.
type Stats struct {
    Size  int
    Alive int
    Busy  int
}
```

Registry's `Detail()` returns the D-16 row shape (RESEARCH §Code Examples block 4):
```go
// Same JSON-tag discipline used by server/health.go:
type SessionDetail struct {
    ID       string    `json:"id"`
    Alive    bool      `json:"alive"`
    Busy     bool      `json:"busy"`         // mirrors slot.busy; true while Mu locked
    LastUsed time.Time `json:"last_used"`    // RFC 3339 via stdlib MarshalJSON
    Model    *string   `json:"model"`        // nullable per D-16 — *string omits when unset
}

func (r *Registry) Detail() []SessionDetail { /* snapshot under RLock */ }
func (r *Registry) Stats() Stats { /* SessionStats.Active counter */ }
```

The pool also needs an extended detail surface (D-15 — `current_session_id` per slot). Place that in `internal/pool/detail.go` (NEW) following the same pattern; pool already has `p.sessionSlots` keyed in reverse (slot → multiple sids historically — read the current shape carefully). Per Pool's existing layout (`sessionSlots map[string]*Slot`), invert via a Stats-time loop.

---

### `internal/session/entry_acp.go` (NEW — `*Entry` satisfies `engine.ACPClient`)

**Analog:** `internal/engine/acp_adapter.go` (lines 1-99) — the EXACT same shim pattern: wrap a single `*acp.Client`, delegate `NewSession`/`SetModel`/`Prompt`/`Cancel`, return a `acpStreamShim`-style stream.

**Adapter struct** (model from acp_adapter.go:25-32):
```go
// internal/engine/acp_adapter.go:25-32
func NewACPClientAdapter(client *acp.Client) ACPClient {
    return &acpClientAdapter{client: client}
}

type acpClientAdapter struct {
    client *acp.Client
}
```

**Mirror in session/entry_acp.go** (or as methods on Entry itself per RESEARCH Code Examples block 3):
```go
// Entry implements engine.ACPClient — surface handlers pass *Entry as the
// engine.Run client when X-Session-Id is set (vs *pool.Pool when absent).

func (e *Entry) NewSession(ctx context.Context, cwd string) (string, error) {
    // The entry's ACP session was already created in registry.createEntry —
    // return the cached SessionID. This is different from acpClientAdapter
    // which forwards to acp.Client.NewSession every call.
    return e.SessionID, nil
}

func (e *Entry) SetModel(ctx context.Context, sessionID, modelID string) error {
    // D-09: skip the RPC if model is unchanged.
    if modelID == e.LastModel {
        return nil
    }
    if err := e.Client.SetModel(ctx, sessionID, modelID); err != nil {
        return fmt.Errorf("session: set-model: %w", err)
    }
    e.LastModel = modelID
    return nil
}

func (e *Entry) Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (engine.Stream, error) {
    // Caller holds e.Mu (D-07).
    raw, err := e.Client.Prompt(ctx, sessionID, blocks)
    if err != nil {
        return nil, fmt.Errorf("session: prompt: %w", err)
    }
    return &acpStreamShim{s: raw}, nil  // copy from engine/acp_adapter.go:66-90
}

func (e *Entry) Cancel(sessionID string) {
    e.Client.Cancel(sessionID)
}
```

**acpStreamShim — copy verbatim from engine/acp_adapter.go:63-90:**
```go
// engine/acp_adapter.go:63-90 — same shim works here.
type acpStreamShim struct { s *acp.Stream }
func (a *acpStreamShim) Chunks() <-chan canonical.Chunk { return a.s.Chunks }
func (a *acpStreamShim) Result() (*canonical.FinalResult, error) {
    fr, err := a.s.Result()
    if fr == nil { return nil, err }
    return &canonical.FinalResult{
        SessionID: fr.SessionID, ChunkCount: fr.ChunkCount, StopReason: fr.StopReason,
    }, err
}
```

**Compile-time interface assertion** (model from pool.go:469-472 + acp_adapter.go:95-98):
```go
var (
    _ engine.ACPClient = (*Entry)(nil)
    _ engine.Stream    = (*acpStreamShim)(nil)
)
```

---

### `internal/session/testmain_test.go` (NEW — goleak gate)

**Analog:** `internal/pool/testmain_test.go` (lines 1-20) — copy verbatim.

**Pattern** (copy from pool/testmain_test.go:1-20 with package rename only):
```go
// Package session — whitebox test file.
package session

import (
    "testing"

    "go.uber.org/goleak"
)

func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```

---

### `internal/session/registry_test.go` (NEW — Get/Delete/race tests)

**Analog:** `internal/pool/pool_test.go` (lines 40-155) — `fakeClient` + `fakeClientFactory` + `warmedPoolWithFakes` are the harness templates.

**fakeClient pattern** (copy from pool_test.go:47-116 — same PoolClient interface, same hook fields):
```go
// pool_test.go:47-116 — copy the entire fakeClient struct + methods.
type fakeClient struct {
    initializeFn func(ctx context.Context) error
    newSessionFn func(ctx context.Context, cwd string) (string, error)
    setModelFn   func(ctx context.Context, sid, m string) error
    promptFn     func(ctx context.Context, sid string, blocks []canonical.Block) (*acp.Stream, error)
    models       []canonical.ModelInfo
    mu              sync.Mutex
    initializeCalls int
    newSessionCalls int
    cancelCalls     []string
    closeCalls      int
}
// + all the methods Initialize, NewSession, SetModel, Prompt, Cancel, Close, AvailableModels
```

**fakeClientFactory** (copy from pool_test.go:133-155):
```go
// pool_test.go:133-155
type fakeClientFactory struct {
    clients []pool.PoolClient
    mu      sync.Mutex
    idx     int
    spawnErr error
}

func (ff *fakeClientFactory) Spawn(_ context.Context, _ acp.Config) (pool.PoolClient, error) {
    if ff.spawnErr != nil { return nil, ff.spawnErr }
    ff.mu.Lock()
    defer ff.mu.Unlock()
    if ff.idx >= len(ff.clients) {
        return nil, errors.New("fakeClientFactory: no more clients in script")
    }
    c := ff.clients[ff.idx]; ff.idx++
    return c, nil
}
```

**Required test list** (per RESEARCH Wave 0 + Pitfalls):
1. `TestRegistry_Get_LazyCreate` — first call spawns, second returns cached.
2. `TestRegistry_Get_RacingSameSid_NoDoubleSpawn` — Pitfall 4; two goroutines, same sid → factory called exactly once.
3. `TestRegistry_Get_SessionMaxExceeded` — 33rd Get with cap=32 returns `ErrSessionMaxExceeded`.
4. `TestRegistry_Delete_KnownSid_Returns200Worthy` — Delete returns nil; subsequent Get returns lazy-create (new entry).
5. `TestRegistry_Delete_UnknownSid_ReturnsErrSessionNotFound` — D-08 404 path.
6. `TestRegistry_Delete_CancelsInFlight` — concurrent Prompt + Delete; Pitfall 1 race resolution.

---

### `internal/session/reaper_test.go` (NEW — real-time D-13 short-TTL)

**Analog:** RESEARCH §Code Examples block 5 (sketch) + `internal/pool/pool_test.go` `WaitForSlotRelease` polling pattern.

**Pattern** (copy from RESEARCH block 5):
```go
// RESEARCH 05-RESEARCH.md Code Examples block 5
func TestReaper_ReapsIdleSessionInRealTime(t *testing.T) {
    r := session.New(session.Config{
        Logger:       testutil.Logger(t),
        Factory:      fakeFactory,
        TTL:          200 * time.Millisecond,
        TickInterval: 50 * time.Millisecond,
        MaxSessions:  32,
    })
    ctx, cancel := context.WithCancel(context.Background())
    t.Cleanup(cancel)
    r.Start(ctx)
    t.Cleanup(func() { _ = r.Close() })

    e, err := r.Get(ctx, "sid-1", "/tmp")
    require.NoError(t, err)
    e.LastUsed = time.Now()
    e.Mu.Unlock()  // simulate "no in-flight stream"

    require.Eventually(t, func() bool {
        return r.SessionCount() == 0
    }, 1*time.Second, 25*time.Millisecond, "session should be reaped")
}
```

Additional required test: `TestReaper_SkipsInFlightSession` — entry's `e.Mu.Lock()` held for the full ticker window; assert entry survives. D-12 verification.

---

### `internal/session/export_test.go` (NEW — whitebox accessors)

**Analog:** `internal/pool/export_test.go` (lines 1-47) — copy exactly the same pattern (test-only methods on the production package).

**Pattern** (copy from export_test.go:1-47):
```go
// internal/pool/export_test.go:1-47
package pool

func (p *Pool) WaitForSlotRelease(timeout time.Duration) (*Slot, bool) {
    select {
    case s := <-p.slots: return s, true
    case <-time.After(timeout): return nil, false
    }
}

func (p *Pool) SessionSlotsLen() int {
    p.mu.Lock()
    defer p.mu.Unlock()
    return len(p.sessionSlots)
}
```

Mirror in session/export_test.go:
```go
package session

func (r *Registry) SessionCount() int {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return len(r.entries)
}

func (r *Registry) ForceEntry(sid string, e *Entry) { /* test seam */ }
```

---

### `internal/acp/client.go` (MODIFY — add `Done()` accessor)

**Analog:** self — one-line accessor over the existing `clientCtx` field (line 246).

**Insertion point:** anywhere in the exported method block after the `Cancel`/`Close` definitions (e.g., after line 807 where `Cancel` ends, or as a sibling to `Close` at line 919).

**Code excerpt** (RESEARCH §Code Examples block 1 verbatim):
```go
// Done returns a channel that is closed when the client's subprocess has
// exited (either via Close() or via the ping loop killing on a failed
// ping that isn't "method not found"). The channel is closed exactly once.
//
// Done is the push-based exit signal added in Phase 5 (D-01) for the
// per-slot exit-watcher in internal/pool. It is intentionally a
// receive-only chan struct{} (no error payload).
//
// The channel is derived from the existing private clientCtx — Close()
// step 1 (line 925) cancels clientCtx, so Done() fires for the same
// teardown paths that already fire ErrClientClosed for in-flight callers.
func (c *Client) Done() <-chan struct{} {
    return c.clientCtx.Done()
}
```

**Verification:** No new fields, no new goroutines. Already-passing `goleak` gate in `internal/acp/testmain_test.go` continues unchanged.

---

### `internal/pool/pool.go` (MODIFY — dead-slot detection + lazy re-spawn)

**Analog:** self — `NewSession` (lines 263-284), `initSlot` (145-161), `closeAll` (216-238).

**Insertion 1 — dead-slot branch in `NewSession`** (mirror RESEARCH §Pattern 1 + reuse existing select-on-ctx idiom from pool.go:263-271):
```go
// pool.go:263-284 — current shape:
func (p *Pool) NewSession(ctx context.Context, cwd string) (string, error) {
    var slot *Slot
    select {
    case slot = <-p.slots:
        // acquired
    case <-ctx.Done():
        return "", fmt.Errorf("pool: acquire cancelled: %w", ctx.Err())
    }

    // ★ NEW Phase 5 D-01/D-02 — insert here, BEFORE the existing slot.Client.NewSession call:
    if !p.slotAlive(slot) {
        if err := p.respawnSlot(ctx, slot); err != nil {
            p.removeSlot(slot)   // D-03: drop dead slot, pool shrinks
            return "", fmt.Errorf("pool: respawn slot %s: %w", slot.Label, err)
        }
    }

    // existing path — unchanged from current pool.go:272-283
    sid, err := slot.Client.NewSession(ctx, cwd)
    if err != nil {
        p.slots <- slot
        return "", fmt.Errorf("pool: new-session: %w", err)
    }
    p.mu.Lock()
    p.sessionSlots[sid] = slot
    p.mu.Unlock()
    return sid, nil
}
```

**Insertion 2 — `respawnSlot` helper** (mirror `initSlot` lines 145-161 + ctx honor per RESEARCH Pitfall 2):
```go
// pool.go:145-161 — initSlot is the template
func (p *Pool) initSlot(ctx context.Context, label string) (*Slot, error) {
    client, err := p.cfg.Factory.Spawn(ctx, acp.Config{...})
    if err != nil { return nil, fmt.Errorf("pool: spawn %s: %w", label, err) }
    if err := client.Initialize(ctx); err != nil {
        _ = client.Close()
        return nil, fmt.Errorf("pool: initialize %s: %w", label, err)
    }
    return &Slot{Label: label, Client: client}, nil
}
```

`respawnSlot` reuses the spawn logic but mutates the existing slot in place (per RESEARCH Pattern 1):
- Call `slot.Client.Close()` on the old client FIRST (Pitfall 2 — this makes the old exit-watcher's `<-slot.Client.Done()` fire, watcher exits)
- Then call `p.cfg.Factory.Spawn(ctx, ...)` for the new client
- Replace `slot.Client` under `p.mu.Lock()`
- Spawn a fresh exit-watcher for the new client via `p.startExitWatcher(slot)`

**Insertion 3 — `closing` channel + Close() ordering** (RESEARCH §Pattern 2):
```go
// pool.go:203-209 — current Close
func (p *Pool) Close() error {
    var firstErr error
    p.closeOnce.Do(func() {
        firstErr = p.closeAll()
    })
    return firstErr
}

// ★ NEW Phase 5 — add closing channel close at the top of the closeOnce body:
func (p *Pool) Close() error {
    var firstErr error
    p.closeOnce.Do(func() {
        close(p.closing)         // NEW: signal watchers to exit
        firstErr = p.closeAll()
    })
    return firstErr
}
```

The struct gains:
```go
// Pool struct addition:
closing chan struct{}   // NEW Phase 5; closed by Close to signal exit-watchers
```

---

### `internal/pool/exit_watcher.go` (NEW — per-slot watcher)

**Analog:** RESEARCH §Pattern 2 + acp/client.go pingLoop (lines 433-453).

**Pattern** (RESEARCH §Pattern 2 verbatim):
```go
// internal/pool/exit_watcher.go
package pool

func (p *Pool) startExitWatcher(slot *Slot) {
    go func() {
        select {
        case <-slot.Client.Done():
            // acp.Client tore down its subprocess (ping failure or readLoop EOF).
            // Mark the slot dead — Pool.NewSession's dead-slot branch picks it up.
            p.mu.Lock()
            slot.dead = true
            p.mu.Unlock()
            p.cfg.Logger.Info("pool: slot died", "label", slot.Label)
        case <-p.closing:
            // Pool.Close fired — exit cleanly so goleak passes.
            return
        }
    }()
}
```

**Slot struct gains** a `dead bool` field (guarded by `p.mu`); `p.slotAlive(slot)` checks it:
```go
// pool.go:16-22 — current Slot:
type Slot struct {
    Label string
    Client PoolClient
    // ★ NEW: dead flag; guarded by p.mu
    dead bool
}
```

`startExitWatcher(slot)` is invoked from:
1. `initSlot` (end of, just before returning) — initial warmup watchers
2. `respawnSlot` (end of, after new client is in place) — re-spawn watchers

Both call sites mirror — search `initSlot` line 161 for the insertion point.

---

### `internal/pool/pool_test.go` (MODIFY — dead-slot tests)

**Analog:** existing tests in pool_test.go — particularly `TestPool_Cancel_RoutesToCorrectSlot` (lines 555-608), `TestPool_Prompt_ErrorReleasesSlot` (lines 452-488), and `WaitForSlotRelease` pattern.

**Test pattern — using fakeClient to simulate death:**
```go
// Extend fakeClient with a Done() channel so tests can fire "death":
type fakeClient struct {
    // ... existing fields ...
    doneCh chan struct{}  // NEW: closed to simulate subprocess exit
}

func (f *fakeClient) Done() <-chan struct{} {
    if f.doneCh == nil { f.doneCh = make(chan struct{}) }
    return f.doneCh
}

// Test:
func TestPool_DeadSlot_LazyRespawn(t *testing.T) {
    fc0 := &fakeClient{ /* dies via Done */ }
    fc1Replacement := &fakeClient{ /* the respawn */ }
    ff := &fakeClientFactory{clients: []pool.PoolClient{fc0, fc1Replacement}}
    p := pool.New(pool.Config{ Size: 1, Factory: ff, ... })
    require.NoError(t, p.Warmup(ctx))

    // Kill the slot
    close(fc0.doneCh)

    // Wait for watcher to mark dead (eventually-style — same as WaitForSlotRelease)
    // Next NewSession should respawn synchronously
    sid, err := p.NewSession(ctx, "")
    require.NoError(t, err)
    require.Equal(t, 1, fc1Replacement.newSessionCount())   // respawn fired
}
```

Required tests per RESEARCH Wave 0:
- `TestPool_DeadSlot_LazyRespawn` (POOL-04 happy path)
- `TestPool_DeadSlot_RespawnFailure_PoolShrinks` (D-03)
- `TestPool_ExitWatcher_GoroutineExitsOnClose` (goleak gate covers, but explicit assertion via `runtime.NumGoroutine` is a nice-to-have)
- `TestPoolDetail_CurrentSessionID` (D-15 wire shape)

**Helper patterns to reuse from pool_test.go:**
- `WaitForSlotRelease` (export_test.go:23-30) for poll-with-timeout
- `warmedPoolWithFakes` (pool_test.go:347-366) for harness construction
- `makeStatefulNewSession` pattern (pool_test.go:391-400) for distinguishing warmup vs run sids

---

### `internal/server/server.go` (MODIFY — add RegistryStatsSource, /health/agents, DELETE wire)

**Analog:** self — `PoolStatsSource` interface (lines 25-31), exempt-route list (164-172), `NewFromConfig` (150-212).

**Pattern 1 — new interface mirroring PoolStatsSource:**
```go
// server.go:25-31 — current PoolStatsSource:
type PoolStatsSource interface {
    Stats() PoolStats
}

// ★ NEW Phase 5 — parallel interface for registry detail:
type RegistryStatsSource interface {
    Stats() SessionStats              // for /health (SessionStats.Active)
    Detail() []SessionDetail          // for /health/agents (D-16 rows)
}

// Either two interfaces (RegistryStatsSource above + the existing
// PoolStatsSource — RESEARCH §Standard Stack alternatives "two interfaces"
// decision, mirrors the established pattern), OR a single combined
// AgentDetailSource interface. The two-interfaces choice mirrors
// PoolStatsSource verbatim; choose this for consistency with Phase 2.
```

**Pattern 2 — exempt route registration:**
```go
// server.go:164-172 — current exempt routes:
s.router.Get("/", s.rootHandler)
s.router.Get("/health", s.healthHandler)
if cfg.OllamaVersionHandler != nil && cfg.OllamaVersionPath != "" {
    s.router.Get(cfg.OllamaVersionPath, cfg.OllamaVersionHandler)
}

// ★ NEW Phase 5 D-18 — add /health/agents to the exempt list:
s.router.Get("/health/agents", s.agentsHandler)
```

**Pattern 3 — Config struct gains Registry source:**
```go
// server.go:75-87 — Config struct:
type Config struct {
    Logger               *slog.Logger
    Version              string
    // ...existing fields...
    Pool                 PoolStatsSource

    // ★ NEW Phase 5:
    Registry RegistryStatsSource
}

// Server struct:
type Server struct {
    cfg     config.Config
    logger  *slog.Logger
    router  chi.Router
    version string
    commit  string
    start   time.Time
    pool    PoolStatsSource

    // ★ NEW Phase 5:
    registry RegistryStatsSource
    agents   AgentDetailSource     // optional combined source (alternative shape)
}
```

**Pattern 4 — DELETE route wiring** (D-08 — auth-PROTECTED; goes inside the `/v1` Route block, NOT the exempt list):
```go
// server.go:195-208 — current per-prefix Route block:
for _, prefix := range prefixes {
    mounts := byPrefix[prefix]
    p := prefix
    s.router.Route(p, func(r chi.Router) {
        r.Use(auth.Bearer(...))
        r.Use(auth.IPAllowlist(...))
        for _, sm := range mounts {
            sm.Router.RegisterRoutes(r)
        }
    })
}

// ★ NEW Phase 5 — DELETE /v1/sessions/:id is auth-protected. Two routes:
// Either (a) register inside the existing /v1 Route block, OR
// (b) introduce a new SessionsRouter that implements RouteRegistrar and is
//     added to the cfg.Surfaces list at the /v1 prefix. Option (b) is
//     consistent with the existing surface-mount pattern.
```

Recommendation: Option (b) — introduce a `SessionsRouter` (in `internal/server/sessions_delete.go` or a new package) that satisfies `server.RouteRegistrar`. Pass it as a SurfaceMount at the `/v1` prefix from `cmd/otto-gateway/main.go`.

---

### `internal/server/health.go` (MODIFY — populate Sessions.Active)

**Analog:** self — `healthHandler` (lines 57-75).

**Modification** (mirror the existing Pool nil-safety pattern at lines 58-61):
```go
// health.go:57-75 — current:
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
    var ps PoolStats
    if s.pool != nil {
        ps = s.pool.Stats()
    }
    resp := HealthResponse{
        Status:        "ok",
        Version:       s.version,
        UptimeSeconds: time.Since(s.start).Seconds(),
        Pool:          ps,
        // Sessions, Embeddings are zero-value — Phase 5 / 7 surfaces.
    }
    // ...
}

// ★ NEW Phase 5 — add registry nil-safe Stats() call:
var ss SessionStats
if s.registry != nil {
    ss = s.registry.Stats()
}
resp := HealthResponse{
    // ...
    Sessions: ss,
}
```

`SessionStats` (line 39-42) already declared in Phase 1 — populate `Active` field from `registry.Stats().Active`.

---

### `internal/server/agents.go` (NEW — agentsHandler)

**Analog:** `internal/server/health.go` healthHandler (lines 57-75) — literal template.

**Pattern** (mirror healthHandler line-by-line):
```go
// health.go:57-75 — the template:
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
    var ps PoolStats
    if s.pool != nil { ps = s.pool.Stats() }
    resp := HealthResponse{
        Status:        "ok",
        Version:       s.version,
        UptimeSeconds: time.Since(s.start).Seconds(),
        Pool:          ps,
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    if err := json.NewEncoder(w).Encode(resp); err != nil {
        LoggerFromCtx(r.Context(), s.logger).Error("health encode", "err", err)
    }
}
```

**Mirror exactly** (RESEARCH §Code Examples block 4):
```go
// internal/server/agents.go
package server

import (
    "encoding/json"
    "net/http"
    "time"
)

type AgentsResponse struct {
    Pool     AgentsPool      `json:"pool"`
    Sessions []AgentSession  `json:"sessions"`
}

type AgentsPool struct {
    Size  int           `json:"size"`
    Alive int           `json:"alive"`
    Busy  int           `json:"busy"`
    Slots []AgentSlot   `json:"slots"`
}

type AgentSlot struct {
    Label              string  `json:"label"`
    Alive              bool    `json:"alive"`
    Busy               bool    `json:"busy"`
    CurrentSessionID   *string `json:"current_session_id"`  // nullable per D-15
}

type AgentSession struct {
    ID       string    `json:"id"`
    Alive    bool      `json:"alive"`
    Busy     bool      `json:"busy"`
    LastUsed time.Time `json:"last_used"`        // default JSON encoding → RFC 3339
    Model    *string   `json:"model"`             // nullable per D-16
}

func (s *Server) agentsHandler(w http.ResponseWriter, r *http.Request) {
    resp := AgentsResponse{}
    // Nil-safe: source-driven, mirrors healthHandler.
    if s.poolDetail != nil {
        resp.Pool = s.poolDetail.PoolDetail()
    }
    if s.registry != nil {
        resp.Sessions = s.registry.Detail()
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    if err := json.NewEncoder(w).Encode(resp); err != nil {
        LoggerFromCtx(r.Context(), s.logger).Error("agents encode", "err", err)
    }
}
```

Note the **JSON-tag discipline** and the **nil-safety idiom** — both copied verbatim from healthHandler.

---

### `internal/server/sessions_delete.go` (NEW — DELETE /v1/sessions/:id)

**Analog:** RESEARCH §Pattern 5 + `internal/server/health.go` writeJSON/writeError style + `internal/adapter/ollama/handlers.go:304-319` writeJSON/writeError.

**Pattern** (RESEARCH Pattern 5 verbatim, with project's writeError style):
```go
// internal/server/sessions_delete.go
package server

import (
    "encoding/json"
    "errors"
    "net/http"

    "github.com/go-chi/chi/v5"
    "otto-gateway/internal/session"
)

// SessionsRouter satisfies server.RouteRegistrar and mounts DELETE /v1/sessions/:id.
// Added to cfg.Surfaces at the /v1 prefix from cmd/otto-gateway/main.go so it
// gets the existing auth.Bearer + auth.IPAllowlist wrapping (no separate auth
// wiring needed).
type SessionsRouter struct {
    Registry SessionDeleter   // narrow interface — Delete(sid) error
    Logger   *slog.Logger
}

type SessionDeleter interface {
    Delete(sid string) error
}

func (sr *SessionsRouter) RegisterRoutes(r chi.Router) {
    r.Delete("/sessions/{id}", sr.handleDelete)
}

func (sr *SessionsRouter) handleDelete(w http.ResponseWriter, r *http.Request) {
    sid := chi.URLParam(r, "id")
    if sid == "" {
        writeError(w, http.StatusBadRequest, "missing session id")
        return
    }
    if err := sr.Registry.Delete(sid); err != nil {
        if errors.Is(err, session.ErrSessionNotFound) {
            writeError(w, http.StatusNotFound, "unknown session")
            return
        }
        sr.Logger.Error("session: delete failed", "sid", sid, "err", err)
        writeError(w, http.StatusInternalServerError, "delete failed")
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(map[string]string{"deleted": sid})
}
```

`writeError` already exists in `internal/adapter/ollama/handlers.go:315-319` — pattern (mirror it in the server package, or use a small shared helper):
```go
// ollama/handlers.go:315-319 — the template
func writeError(w http.ResponseWriter, status int, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
```

---

### `internal/config/config.go` (MODIFY — SESSION_TTL_MS, SESSION_MAX, POOL_SIZE env default)

**Analog:** self — existing `getEnvInt` (lines 381-391) and `getEnvDuration` (lines 442-456) helpers; `Load` function (103-168).

**Insertion 1 — env-default POOL_SIZE flips from 1 to 4** (CONTEXT D-10 caveat — only at env-load layer; package default in pool/config.go stays at 1):
```go
// config.go:129 — current:
poolSize, err := getEnvInt("POOL_SIZE", 1)

// ★ NEW Phase 5:
poolSize, err := getEnvInt("POOL_SIZE", 4)   // D-10 Node parity
```

**Insertion 2 — SESSION_TTL_MS via getEnvDuration** (pattern from PING_INTERVAL line 116):
```go
// config.go:116-119 — current PING_INTERVAL pattern:
pingInterval, err := getEnvDuration("PING_INTERVAL", 60*time.Second)
if err != nil {
    errs = append(errs, err)
}

// ★ NEW Phase 5:
sessionTTL, err := getEnvDuration("SESSION_TTL_MS", 30*time.Minute)
if err != nil {
    errs = append(errs, err)
}
```

(getEnvDuration handles both ms-integers and Go duration strings — see lines 442-456; Node parity works out-of-the-box.)

**Insertion 3 — SESSION_MAX via getEnvInt** (pattern from POOL_SIZE line 129):
```go
// ★ NEW Phase 5 D-06:
sessionMax, err := getEnvInt("SESSION_MAX", 32)
if err != nil {
    errs = append(errs, err)
}
```

**Config struct gains** (lines 34-90):
```go
type Config struct {
    // ...existing fields...

    // ★ NEW Phase 5:
    SessionTTL  time.Duration   // SESSION_TTL_MS; default 30 min (Node parity)
    SessionMax  int             // SESSION_MAX; default 32 (Phase 5 addition, no Node equivalent)
}
```

**`Load()` returns** (lines 152-168):
```go
return Config{
    // ...existing fields...
    SessionTTL: sessionTTL,
    SessionMax: sessionMax,
}, nil
```

**`LoadArgs` flag bindings** (lines 204-219): add `--session-ttl` and `--session-max` flags following the existing `--pool-size` pattern at line 211.

---

### `cmd/otto-gateway/main.go` (MODIFY — wire registry, ordered shutdown)

**Analog:** self — `newApp` (lines 117-302) + cleanup closure (122-128) + `poolStatsAdapter` (309-316).

**Insertion 1 — Registry construction** (after pool warmup at lines 130-155):
```go
// main.go:130-155 — current pool setup:
if cfg.KiroCmd != "" {
    a.pool = pool.New(pool.Config{...})
    if err := a.pool.Warmup(warmCtx); err != nil { ... }
    a.engine = engine.New(engine.Config{Logger: logger, ACP: a.pool, ...})
}

// ★ NEW Phase 5 — construct and start registry after pool warmup:
if cfg.KiroCmd != "" {
    a.registry = session.New(session.Config{
        Logger:       logger,
        TTL:          cfg.SessionTTL,
        TickInterval: 60 * time.Second,          // production fixed
        MaxSessions:  cfg.SessionMax,
        KiroCmd:      cfg.KiroCmd,
        KiroArgs:     cfg.KiroArgs,
        KiroCWD:      cfg.KiroCWD,
        PingInterval: cfg.PingInterval,
    })
    a.registry.Start(context.Background())   // reaper goroutine
}
```

**Insertion 2 — shutdown ordering in cleanup closure** (lines 122-128):
```go
// main.go:122-128 — current cleanup:
cleanup := func() {
    if a.pool != nil {
        if err := a.pool.Close(); err != nil {
            logger.Error("pool: close", "err", err)
        }
    }
}

// ★ NEW Phase 5 — drain registry BEFORE pool.Close (CONTEXT.md Claude's Discretion + Pitfall 5):
cleanup := func() {
    // Drain registry first — its reaper holds entries with their own
    // subprocesses, which are NOT in p.all and must be closed independently.
    // Bounded-time: r.Close() returns within (TickInterval + worst-case close-all time).
    if a.registry != nil {
        if err := a.registry.Close(); err != nil {
            logger.Error("registry: close", "err", err)
        }
    }
    if a.pool != nil {
        if err := a.pool.Close(); err != nil {
            logger.Error("pool: close", "err", err)
        }
    }
}
```

**Insertion 3 — `app` struct gains a registry field** (line 99-105):
```go
type app struct {
    cfg    config.Config
    logger *slog.Logger
    pool   *pool.Pool
    engine *engine.Engine
    srv    *server.Server

    // ★ NEW Phase 5:
    registry *session.Registry
}
```

**Insertion 4 — registryStatsAdapter** (mirror `poolStatsAdapter` at lines 309-316):
```go
// main.go:309-316 — the template:
type poolStatsAdapter struct { pool *pool.Pool }
func (p poolStatsAdapter) Stats() server.PoolStats {
    s := p.pool.Stats()
    return server.PoolStats{Size: s.Size, Alive: s.Alive, Busy: s.Busy}
}

// ★ NEW Phase 5 — bridge session.Registry to server.RegistryStatsSource:
type registryStatsAdapter struct { reg *session.Registry }
func (r registryStatsAdapter) Stats() server.SessionStats {
    s := r.reg.Stats()
    return server.SessionStats{Active: s.Active}
}
func (r registryStatsAdapter) Detail() []server.AgentSession {
    return r.reg.Detail()   // assuming AgentSession shape matches session.SessionDetail
}
```

**Insertion 5 — server.NewFromConfig wires registry + sessions surface** (lines 287-299):
```go
// main.go:287-299 — current:
a.srv = server.NewFromConfig(server.Config{
    Logger:               logger,
    // ...existing fields...
    Surfaces:             surfaces,
    Pool:                 poolForServer,
})

// ★ NEW Phase 5:
var registryForServer server.RegistryStatsSource
if a.registry != nil {
    registryForServer = registryStatsAdapter{reg: a.registry}
}

// Also append a SessionsRouter to surfaces at the /v1 prefix:
if a.registry != nil {
    surfaces = append(surfaces, server.SurfaceMount{
        Prefix: cfg.OpenAIPathPrefix,   // /v1
        Router: &server.SessionsRouter{Registry: a.registry, Logger: logger},
    })
}

a.srv = server.NewFromConfig(server.Config{
    // ...existing fields...
    Registry: registryForServer,
})
```

---

### Surface handlers — `internal/adapter/{ollama,openai,anthropic}/handlers.go` (MODIFY each)

**Analog (Ollama):** `internal/adapter/ollama/handlers.go` handleChat (lines 24-73).
**Analog (OpenAI):** `internal/adapter/openai/handlers.go` handleChatCompletions (lines 26-86).
**Analog (Anthropic):** `internal/adapter/anthropic/handlers.go` handleMessages (lines 35-122).

**Pattern — Anthropic stream branch** (lines 83-110 is the model — context.WithCancel for D-07 cancellation, engine.Run, runSSEEmitter):
```go
// anthropic/handlers.go:83-110 — current shape:
if wire.Stream {
    ctx, cancelFn := context.WithCancel(r.Context())
    defer cancelFn()
    runHandle, err := a.cfg.Engine.Run(ctx, req)
    if err != nil { ... }
    if err := runSSEEmitter(ctx, w, runHandle, wire.Model, a.cfg.Logger); err != nil { ... }
    return
}
```

**Phase 5 modification — insert X-Session-Id branch BEFORE engine.Run** (mirror RESEARCH §Pattern 6):
```go
// ★ NEW Phase 5 — pseudocode (apply to all three adapters' stream + non-stream paths):
sid := r.Header.Get("X-Session-Id")
var engineToUse anthropic.Engine = a.cfg.Engine  // pool path (existing)

if sid != "" && a.cfg.Registry != nil {
    entry, err := a.cfg.Registry.Get(r.Context(), sid, a.cfg.KiroCWD)
    if err != nil {
        if errors.Is(err, session.ErrSessionMaxExceeded) {
            writeError(w, http.StatusServiceUnavailable, errAPI, "session capacity exceeded")
            return
        }
        a.cfg.Logger.Error("session: get failed", "sid", sid, "err", err)
        writeError(w, http.StatusInternalServerError, errAPI, "internal error")
        return
    }
    entry.Mu.Lock()
    defer entry.Mu.Unlock()         // D-07: serializes per-sid
    defer entry.MarkUsed()           // D-11: update LastUsed AFTER stream completes
    engineToUse = registryEngineAdapter{entry}  // a cmd-level adapter — or pre-build at adapter.New time
}

// existing engine.Run / engine.Collect path — UNCHANGED, just uses engineToUse:
runHandle, err := engineToUse.Run(ctx, req)
```

**Critical:** the engine boundary stays narrow. `*session.Entry` implements `engine.ACPClient` via the adapter pattern (Pattern 3 above). The `cmd/otto-gateway/main.go` already has `anthropicEngineAdapter` / `openaiEngineAdapter` / `ollamaEngineAdapter` (lines 327-486) — extend each to also wrap "engine + registry" in a function that returns the right Engine implementation given a sid.

Actual mechanics for the adapter layer are a planning detail (RESEARCH calls out "shared middleware would couple the surfaces; each adapter handles its own"). The recommendation: pass the registry into each adapter's Config (mirror `cfg.Engine`), and let the adapter wire engine-per-request internally.

---

## Shared Patterns

### slog Structured Logging

**Source:** `internal/acp/client.go` lines 397, 414, 446 — the project's `log/slog` discipline.
**Apply to:** every new file with a `*slog.Logger` (Registry, Reaper, exit watcher, handlers).
**Excerpt:**
```go
// acp/client.go:446 — terminal-event Warn:
c.cfg.Logger.Warn("acp: ping failed", "err", err)

// acp/client.go:912 — debug-level no-op:
c.cfg.Logger.Debug("acp: unhandled notification", "method", frame.Method)

// pool exit watcher example (Phase 5 D-01):
p.cfg.Logger.Info("pool: slot died", "label", slot.Label)
```

**Discipline (from CLAUDE.md + Phase 1 D-15):**
- Logger is always injected via Config; **never** call `slog.SetDefault`.
- Use `key, value` pairs (not pre-formatted strings).
- Prefix every log line with the package name (`"session: reaped"`, `"pool: slot died"`, `"acp: ..."`).

---

### Per-request logger from context

**Source:** `internal/server/middleware.go` lines 22-46 (accessLog + LoggerFromCtx).
**Apply to:** every server-package handler (healthHandler, agentsHandler, handleDelete).
**Excerpt:**
```go
// server/middleware.go:50-55
func LoggerFromCtx(ctx context.Context, fallback *slog.Logger) *slog.Logger {
    if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
        return l
    }
    return fallback
}

// Usage at server/health.go:73:
LoggerFromCtx(r.Context(), s.logger).Error("health encode", "err", err)
```

Mirror in `agentsHandler` and `handleDelete`.

---

### Error wrapping convention

**Source:** `internal/pool/pool.go` lines 154, 158, 234, 277, 297, 323 + RESEARCH-confirmed.
**Apply to:** all error returns in `internal/session/*` and the modified pool/server files.
**Excerpts:**
```go
// pool.go:154 — package-prefixed, %w-wrapped:
return nil, fmt.Errorf("pool: spawn %s: %w", label, err)

// pool.go:323 — context-shape wrap:
return nil, fmt.Errorf("pool: prompt: %w", err)
```

**Discipline:**
- Every error returned from a package method MUST be wrapped with the package name (`session:`, `pool:`, `registry:`, etc.).
- Use `%w` so `errors.Is` / `errors.As` continue to work.
- Pure delegation paths use `//nolint:wrapcheck` (see `internal/engine/acp_adapter.go:38`).

---

### Compile-time interface satisfaction

**Source:** `internal/pool/pool.go` lines 469-472 + `internal/engine/acp_adapter.go` lines 95-98 + `internal/pool/config.go` line 128.
**Apply to:** `session.Entry` → `engine.ACPClient`, `session.Registry` → `server.RegistryStatsSource`, etc.
**Excerpt:**
```go
// pool.go:469-472
var (
    _ engine.ACPClient = (*Pool)(nil)
    _ engine.Stream    = (*poolStreamWrapper)(nil)
)
```

Build failure here means the implementation drifted from the interface; this is the cheapest defense-in-depth check.

---

### Config struct + applyDefaults

**Source:** `internal/pool/config.go` lines 87-122 (Config + applyDefaults).
**Apply to:** `session.Config.applyDefaults()`.
**Excerpt:**
```go
// pool/config.go:115-122
func (c *Config) applyDefaults() {
    if c.Size <= 0 {
        c.Size = 1
    }
    if c.Factory == nil {
        c.Factory = acpClientFactory{}
    }
}
```

---

### goleak gate

**Source:** `internal/pool/testmain_test.go` lines 1-20.
**Apply to:** `internal/session/testmain_test.go` (NEW); reaffirmed for `internal/pool/*` (the existing gate covers exit_watcher.go automatically).
**Excerpt:** (full file, copy verbatim with package rename):
```go
package session

import (
    "testing"

    "go.uber.org/goleak"
)

func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```

---

### Codex M-3 slot-release race (the load-bearing prior art)

**Source:** `internal/pool/pool.go` lines 326-344 + 398-408 + 451-464.
**Apply to:** `session.Registry.Delete`, the reaper's per-entry cleanup, and any future race-prone teardown path in `internal/session/*`.
**Pattern in two parts:**

1. **Map-delete-first under the registry mutex, then act:**
```go
// pool.go:398-408 — releaseSlotForSession
p.mu.Lock()
slot, ok := p.sessionSlots[sid]
if ok {
    delete(p.sessionSlots, sid)
}
p.mu.Unlock()
if ok {
    p.slots <- slot
}
```

2. **`sync.Once` + closure-coordinated release** (RESEARCH §Code Examples block 2):
```go
// pool.go:451-464 — releaseOnce
func (w *poolStreamWrapper) releaseOnce() {
    w.released.Do(func() {
        if w.cancelWatch != nil {
            w.cancelWatch()
        }
        close(w.doneCh)
        w.release()
    })
}
```

For the registry, the per-entry `sync.Mutex` (D-07) serves the same purpose as the pool's `sync.Once` — both ensure exactly-one terminal path wins.

---

## No Analog Found

**None.** Every new file in Phase 5 has a direct in-repo analog:

| Concern | Why no analog needed |
|---------|----------------------|
| `acp.Client.Done()` | One-line accessor over existing `clientCtx.Done()` — no new pattern, just exposure. |
| Reaper goroutine | `acp/client.go` pingLoop (433-453) is structurally identical. |
| Per-entry mutex with TryLock | Standard stdlib `sync.Mutex` (Go 1.18+); no project pattern needed beyond doc-comments. |
| Lazy-create sentinel | New mechanic but the placeholder-under-lock pattern is small + documented in RESEARCH Pitfall 4. |

## Metadata

**Analog search scope:**
- `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/internal/pool/`
- `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/internal/server/`
- `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/internal/acp/`
- `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/internal/engine/`
- `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/internal/config/`
- `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/internal/adapter/{ollama,openai,anthropic}/`
- `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/cmd/otto-gateway/`

**Files scanned (read in full or via targeted excerpts):**
- `internal/pool/pool.go` (473 lines — full)
- `internal/pool/config.go` (128 lines — full)
- `internal/pool/stats.go` (19 lines — full)
- `internal/pool/testmain_test.go` (20 lines — full)
- `internal/pool/export_test.go` (47 lines — full)
- `internal/pool/pool_test.go` (650/820 lines — targeted)
- `internal/server/server.go` (283 lines — full)
- `internal/server/health.go` (94 lines — full)
- `internal/server/middleware.go` (55 lines — full)
- `internal/acp/client.go` (970 lines — targeted excerpts at 230-280, 433-453, 800-970)
- `internal/engine/engine.go` (273 lines — full)
- `internal/engine/acp_adapter.go` (99 lines — full)
- `internal/config/config.go` (456 lines — full)
- `internal/adapter/ollama/handlers.go` (319 lines — full)
- `internal/adapter/openai/handlers.go` (166 lines — full)
- `internal/adapter/anthropic/handlers.go` (122 lines — full)
- `internal/adapter/{openai,anthropic}/adapter.go` (RegisterRoutes excerpts)
- `cmd/otto-gateway/main.go` (486 lines — full)

**Pattern extraction date:** 2026-05-26
