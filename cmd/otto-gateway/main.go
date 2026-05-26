// Package main is the entry point for the OTTO Gateway binary.
//
// Wiring order (Plan 06 POOL-02):
//
//  1. Load config from env vars.
//  2. Build structured logger.
//  3. If KIRO_CMD is set: construct pool + Warmup (blocking) BEFORE the
//     HTTP server starts accepting. Warmup failure aborts startup
//     non-zero (POOL-02 + RESEARCH.md Pitfall 4 + threat T-02-36).
//  4. If pool is up: construct engine wired to the pool (the pool
//     satisfies engine.ACPClient).
//  5. Construct the Ollama adapter (always — handles /api/version + stubs
//     even in the degraded "KIRO_CMD unset" mode; chat/generate handlers
//     return 503 when engine is nil).
//  6. Construct the server via server.NewFromConfig with the full Phase 2
//     wiring (auth tokens + IP allowlist + AuthTrustXFF + Ollama protected
//     router + outer-router /api/version exempt handler + pool stats source).
//  7. RunUntilSignal — block until SIGINT/SIGTERM.
//  8. Graceful shutdown: server.Shutdown happens inside Run; pool.Close
//     fires via the deferred cleanup closure returned by newApp.
//
// D-22: the binary stays foreground-only. start/stop/status are owned by
// scripts/otto (POSIX) and scripts/otto.ps1 (PowerShell). Never add
// lifecycle subcommands to the binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"time"

	"otto-gateway/internal/adapter/anthropic"
	"otto-gateway/internal/adapter/ollama"
	"otto-gateway/internal/adapter/openai"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/config"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/server"
	"otto-gateway/internal/session"
	"otto-gateway/internal/version"
)

// warmupDeadline bounds pool.Warmup so a hung kiro-cli Initialize cannot
// stall startup forever (threat T-02-36). 30s is generous — typical
// warmup is <1s.
const warmupDeadline = 30 * time.Second

func main() {
	cfg, err := config.LoadArgs(os.Args[1:])
	// Meta-flag exit-0 cases handled BEFORE the error→exit(1) path. main owns
	// process exit; the config package NEVER calls os.Exit.
	if errors.Is(err, config.ErrVersionRequested) {
		fmt.Println(version.Version)
		os.Exit(0)
	}
	var helpErr *config.HelpRequested
	if errors.As(err, &helpErr) {
		// --help: print the captured usage to stdout (GNU convention) and exit 0.
		fmt.Print(helpErr.Usage)
		os.Exit(0)
	}
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("config load failed", "err", err)
		os.Exit(1)
	}

	logger := buildLogger(cfg)

	// Auth-mode startup log line (T-02-31 mitigation + Codex H-7
	// surfaces XFF trust mode so operators see it).
	logger.Info(
		"auth mode",
		"enabled", len(cfg.AuthToken) > 0,
		"ip_allowlist", len(cfg.AllowedIPs) > 0,
		"trust_xff", cfg.AuthTrustXFF,
	)

	app, cleanup, err := newApp(context.Background(), cfg, logger)
	if err != nil {
		logger.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer cleanup()

	if err := app.srv.RunUntilSignal(context.Background()); err != nil {
		logger.Error("server stopped with error", "err", err)
		os.Exit(1)
	}
}

// app bundles the runtime objects newApp constructs. Keeping them on
// one struct lets main_test.go assert on warmup-before-listen invariants
// without copy-pasting the wiring graph.
type app struct {
	cfg      config.Config
	logger   *slog.Logger
	pool     *pool.Pool     // nil when KIRO_CMD unset
	engine   *engine.Engine // nil when pool is nil
	registry *session.Registry // nil when KIRO_CMD unset; constructed alongside pool
	srv      *server.Server
}

// newApp performs the Phase 2 wiring sequence and returns:
//   - the assembled *app
//   - a cleanup func the caller MUST defer (closes pool + logs errors)
//   - any startup error (wraps Warmup failures with context.WithTimeout
//     so a hung kiro-cli does not stall the process forever)
//
// Tests use newApp directly (not main) so they can drive the wiring with
// t.Setenv-controlled config + assert on app.pool.Stats() etc.
//
//nolint:cyclop,gocyclo // wiring graph is intentionally linear; refactoring obscures the order
func newApp(ctx context.Context, cfg config.Config, logger *slog.Logger) (*app, func(), error) {
	a := &app{cfg: cfg, logger: logger}

	// Cleanup closure — invoked once via the returned func. Plan 05-03
	// shutdown ordering (Pitfall 5): registry.Close FIRST, pool.Close
	// SECOND. Both are nil-safe. The reaper goroutine teardown completes
	// in bounded time per plan 05-02 — Registry.Close blocks at most
	// TickInterval + worst-case reapOnce iteration before wg.Wait
	// returns. Pool.Close runs unconditionally even if registry.Close
	// errored (resolved Open Question 3 from 05-CONTEXT.md).
	cleanup := func() {
		if a.registry != nil {
			if err := a.registry.Close(); err != nil {
				logger.Error("session: registry close", "err", err)
			}
		}
		if a.pool != nil {
			if err := a.pool.Close(); err != nil {
				logger.Error("pool: close", "err", err)
			}
		}
	}

	if cfg.KiroCmd != "" {
		a.pool = pool.New(pool.Config{
			Logger:       logger,
			Size:         cfg.PoolSize,
			KiroCmd:      cfg.KiroCmd,
			KiroArgs:     cfg.KiroArgs,
			KiroCWD:      cfg.KiroCWD,
			PingInterval: cfg.PingInterval,
		})

		// POOL-02: warmup BEFORE the HTTP listener accepts traffic.
		// Bound it with warmupDeadline so a hung Initialize cannot
		// stall the process forever (threat T-02-36).
		warmCtx, cancel := context.WithTimeout(ctx, warmupDeadline)
		defer cancel()
		if err := a.pool.Warmup(warmCtx); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("pool warmup: %w", err)
		}

		a.engine = engine.New(engine.Config{
			Logger:     logger,
			ACP:        a.pool,
			DefaultCWD: cfg.KiroCWD,
		})

		// Plan 05-03: construct the dedicated-session registry alongside
		// the pool. The reaper is started here (rather than at Warmup)
		// so the goroutine is spawned BEFORE any HTTP request can reach
		// the X-Session-Id branch. Both lifecycles share KIRO_CMD /
		// KIRO_ARGS / KIRO_CWD / PingInterval per the backward-compat
		// env-var contract; SessionTTL + SessionMax are Phase 5 additions
		// loaded by internal/config.
		a.registry = session.New(session.Config{
			Logger:       logger,
			TTL:          cfg.SessionTTL,
			MaxSessions:  cfg.SessionMax,
			KiroCmd:      cfg.KiroCmd,
			KiroArgs:     cfg.KiroArgs,
			KiroCWD:      cfg.KiroCWD,
			PingInterval: cfg.PingInterval,
			// TickInterval left zero → applyDefaults uses 60s (Node parity).
		})
		a.registry.Start(context.Background())
	}

	// Adapter construction is gated by cfg.EnabledSurfaces (D-16
	// fail-fast in config.Load already rejects unknown names). Each
	// adapter tolerates nil Engine / nil ModelCatalog — degraded
	// "KIRO_CMD unset" mode returns 503 from the chat handlers while
	// keeping /, /health, and /api/version (Ollama only) alive.
	//
	// engineForAdapter is computed once and reused so both surfaces
	// share the same canonical engine handle (D-17 — one engine, many
	// surfaces).
	var engineForAdapter ollama.Engine
	if a.engine != nil {
		engineForAdapter = ollamaEngineAdapter{engine: a.engine}
	}
	var catalogForAdapter ollama.ModelCatalog
	if a.pool != nil {
		catalogForAdapter = a.pool
	}

	// Plan 05-03: build per-adapter EngineForSession factory closures.
	// Each closure constructs a fresh *engine.Engine bound to the
	// supplied *session.Entry (which satisfies engine.ACPClient via the
	// compile-time gate in internal/session/entry_acp.go). The returned
	// engine is wrapped in the adapter's surface-specific run-handle
	// adapter so adapters never import internal/engine (TRST-04
	// boundary preserved).
	var ollamaEngineForSession ollama.EngineForSessionFunc
	var openaiEngineForSession openai.EngineForSessionFunc
	var anthropicEngineForSession anthropic.EngineForSessionFunc
	var registryForAdapters *session.Registry
	if a.registry != nil {
		registryForAdapters = a.registry
		ollamaEngineForSession = func(entry *session.Entry) ollama.Engine {
			return ollamaEngineAdapter{engine: engine.New(engine.Config{
				Logger:     logger,
				ACP:        entry,
				DefaultCWD: cfg.KiroCWD,
			})}
		}
		openaiEngineForSession = func(entry *session.Entry) openai.Engine {
			return openaiEngineAdapter{engine: engine.New(engine.Config{
				Logger:     logger,
				ACP:        entry,
				DefaultCWD: cfg.KiroCWD,
			})}
		}
		anthropicEngineForSession = func(entry *session.Entry) anthropic.Engine {
			return anthropicEngineAdapter{engine: engine.New(engine.Config{
				Logger:     logger,
				ACP:        entry,
				DefaultCWD: cfg.KiroCWD,
			})}
		}
	}

	var ollamaAdapter *ollama.Adapter
	if slices.Contains(cfg.EnabledSurfaces, "ollama") {
		ollamaAdapter = ollama.New(ollama.Config{
			Logger:           logger,
			Engine:           engineForAdapter,
			ModelCatalog:     catalogForAdapter,
			Version:          version.Version,
			Commit:           version.Commit(),
			Registry:         registryForAdapters,
			EngineForSession: ollamaEngineForSession,
			KiroCWD:          cfg.KiroCWD,
		})
	}

	var anthropicAdapter *anthropic.Adapter
	if slices.Contains(cfg.EnabledSurfaces, "anthropic") {
		// anthropic.Engine is satisfied via anthropicEngineAdapter
		// (below) — *engine.Engine.Run returns the concrete *engine.Run
		// while anthropic.Engine.Run wants anthropic.RunHandle, so a
		// thin wrapper is required (Go structural typing is strict
		// about return types).
		//
		// When a.engine is nil (degraded mode) eng stays nil and the
		// nil-engine guard in anthropic.handleMessages returns 503
		// (Plan 02 Task 3 line 35).
		var eng anthropic.Engine
		if a.engine != nil {
			eng = anthropicEngineAdapter{engine: a.engine}
		}
		anthropicAdapter = anthropic.New(anthropic.Config{
			Logger:           logger,
			Engine:           eng,
			Registry:         registryForAdapters,
			EngineForSession: anthropicEngineForSession,
			KiroCWD:          cfg.KiroCWD,
		})
	}

	var openaiAdapter *openai.Adapter
	if slices.Contains(cfg.EnabledSurfaces, "openai") {
		// openai.Engine is satisfied via openaiEngineAdapter (below) —
		// same Go return-type-invariance rationale as the anthropic bridge.
		// When a.engine is nil (degraded mode) eng stays nil and the
		// nil-engine guard in openai.handleChatCompletions returns 503.
		var eng openai.Engine
		if a.engine != nil {
			eng = openaiEngineAdapter{engine: a.engine}
		}
		// Pass the same pool as ModelCatalog so /v1/models reflects live
		// kiro-cli models (same pool used for ollama at catalogForAdapter).
		// When pool is nil (KIRO_CMD unset), openai.handleModels returns
		// only the synthetic "auto" entry.
		var cat openai.ModelCatalog
		if a.pool != nil {
			cat = a.pool
		}
		openaiAdapter = openai.New(openai.Config{
			Logger:           logger,
			Engine:           eng,
			ModelCatalog:     cat,
			Registry:         registryForAdapters,
			EngineForSession: openaiEngineForSession,
			KiroCWD:          cfg.KiroCWD,
		})
	}

	// Build the SurfaceMount list (D-01). Each enabled adapter contributes
	// a SurfaceMount entry; adapters that are nil (disabled or degraded)
	// are skipped. The server groups entries by prefix so Anthropic and
	// OpenAI can share "/v1" without triggering chi's double-Mount panic.
	var surfaces []server.SurfaceMount
	var ollamaVersionHandler http.HandlerFunc
	if ollamaAdapter != nil {
		surfaces = append(surfaces, server.SurfaceMount{
			Prefix: cfg.OllamaPathPrefix,
			Router: ollamaAdapter,
		})
		ollamaVersionHandler = ollamaAdapter.HandleVersion()
	}
	if anthropicAdapter != nil {
		surfaces = append(surfaces, server.SurfaceMount{
			Prefix: cfg.AnthropicPathPrefix,
			Router: anthropicAdapter,
		})
	}
	if openaiAdapter != nil {
		surfaces = append(surfaces, server.SurfaceMount{
			Prefix: cfg.OpenAIPathPrefix,
			Router: openaiAdapter,
		})
	}

	// Plan 05-03: mount the SessionsRouter on the OpenAI path prefix
	// (default /v1) so DELETE /v1/sessions/:id sits behind the same
	// auth.Bearer + auth.IPAllowlist chain as the other /v1 surfaces.
	// SessionsRouter satisfies server.RouteRegistrar; its SessionDeleter
	// is satisfied by *session.Registry's Delete(sid) error method.
	if a.registry != nil {
		surfaces = append(surfaces, server.SurfaceMount{
			Prefix: cfg.OpenAIPathPrefix,
			Router: &server.SessionsRouter{
				Registry: a.registry,
				Logger:   logger,
			},
		})
	}

	if len(surfaces) == 0 {
		// Defensive: an operator-supplied empty list (e.g.,
		// ENABLED_SURFACES=" ,") shouldn't reach this path because
		// config.Load injects the default when the env value resolves
		// to an empty slice. If it does (programmatic Config literals
		// in tests, for example), keep serving the exempt routes
		// (/, /health) and log a warning — do NOT exit non-zero.
		// D-16 fail-fast is reserved for unknown surface names.
		logger.Warn("no surfaces enabled; serving exempt routes only",
			"enabled_surfaces", cfg.EnabledSurfaces)
	}

	var poolForServer server.PoolStatsSource
	if a.pool != nil {
		poolForServer = poolStatsAdapter{pool: a.pool}
	}
	// Plan 05-03: build the PoolDetailSource + RegistryStatsSource
	// bridges for /health/agents (D-14/D-15/D-16). Both are nil-safe —
	// when KIRO_CMD is unset the pool / registry are also nil and the
	// agentsHandler renders empty rows for the corresponding sub-tree.
	var poolDetailForServer server.PoolDetailSource
	if a.pool != nil {
		poolDetailForServer = poolDetailAdapter{pool: a.pool}
	}
	var registryForServer server.RegistryStatsSource
	if a.registry != nil {
		registryForServer = registryStatsAdapter{reg: a.registry}
	}

	// Boot log surfaces the resolved surface set so operators see
	// what's actually mounted (closes a Phase 2 → Phase 3.1 ops gap).
	// openai_mounted reflects whether the /v1 OpenAI surface is wired
	// (T-03-30 observability — Runtime State Inventory).
	logger.Info(
		"server: enabled surfaces",
		"enabled_surfaces", cfg.EnabledSurfaces,
		"ollama_mounted", ollamaAdapter != nil,
		"anthropic_mounted", anthropicAdapter != nil,
		"openai_mounted", openaiAdapter != nil,
	)

	a.srv = server.NewFromConfig(server.Config{
		Logger:               logger,
		Version:              version.Version,
		Commit:               version.Commit(),
		HTTPAddr:             cfg.HTTPAddr,
		AuthTokens:           cfg.AuthToken,
		AllowedPrefixes:      cfg.AllowedIPs,
		AuthTrustXFF:         cfg.AuthTrustXFF, // Codex H-7 wiring path complete
		OllamaVersionPath:    cfg.OllamaPathPrefix + "/version",
		OllamaVersionHandler: ollamaVersionHandler, // Codex M-4 split accessor
		Surfaces:             surfaces,
		Pool:                 poolForServer,
		PoolDetail:           poolDetailForServer, // Plan 05-03 D-15
		Registry:             registryForServer,   // Plan 05-03 D-14/D-16
	})

	return a, cleanup, nil
}

// poolStatsAdapter shapes pool.Stats into server.PoolStats. The two
// packages declare structurally identical types intentionally — server
// owns the JSON-tagged shape (/health surface), pool owns the runtime
// type. This adapter is the one-line bridge between them; lives here in
// main rather than in either package to keep the boundary clean.
type poolStatsAdapter struct {
	pool *pool.Pool
}

func (p poolStatsAdapter) Stats() server.PoolStats {
	s := p.pool.Stats()
	return server.PoolStats{Size: s.Size, Alive: s.Alive, Busy: s.Busy}
}

// poolDetailAdapter wraps *pool.Pool to satisfy server.PoolDetailSource.
// pool.AgentSlot (plan 05-01) and server.AgentSlot (plan 05-03) are
// structurally identical wire types declared in separate packages to
// keep the engine boundary clean; this adapter does the per-row copy.
// Stays here in main rather than in either package — same one-line
// bridge pattern as poolStatsAdapter.
type poolDetailAdapter struct {
	pool *pool.Pool
}

func (p poolDetailAdapter) Detail() []server.AgentSlot {
	slots := p.pool.Detail()
	out := make([]server.AgentSlot, 0, len(slots))
	for _, sl := range slots {
		out = append(out, server.AgentSlot{
			Label:            sl.Label,
			Alive:            sl.Alive,
			Busy:             sl.Busy,
			CurrentSessionID: sl.CurrentSessionID,
		})
	}
	return out
}

// registryStatsAdapter wraps *session.Registry to satisfy
// server.RegistryStatsSource. session.Stats has one field (Active);
// session.SessionDetail (D-16 wire shape) is structurally identical
// to server.AgentSession — both share the JSON tag set
// {id, alive, busy, last_used, model} — so the field copy is
// straightforward. Like poolDetailAdapter, lives in main to keep
// the boundary clean.
type registryStatsAdapter struct {
	reg *session.Registry
}

func (r registryStatsAdapter) Stats() server.SessionStats {
	s := r.reg.Stats()
	return server.SessionStats{Active: s.Active}
}

func (r registryStatsAdapter) Detail() []server.AgentSession {
	details := r.reg.Detail()
	out := make([]server.AgentSession, 0, len(details))
	for _, d := range details {
		out = append(out, server.AgentSession{
			ID:       d.ID,
			Alive:    d.Alive,
			Busy:     d.Busy,
			LastUsed: d.LastUsed,
			Model:    d.Model,
		})
	}
	return out
}

// buildLogger constructs the root *slog.Logger from the loaded config.
// D-15: never call slog.SetDefault. Logger is constructed once here and
// injected everywhere via package Config structs.
func buildLogger(cfg config.Config) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel(),
	}))
}

// anthropicEngineAdapter wraps a concrete *engine.Engine and adapts
// its Run signature to anthropic.Engine. The Collect method passes
// through unchanged. This is the cmd-level seam that keeps the
// internal/adapter/anthropic package free of any internal/engine
// import (TRST-04 boundary — enforced by .go-arch-lint.yml).
//
// Why a wrapper instead of structural satisfaction:
// *engine.Engine.Run returns (*engine.Run, error); anthropic.Engine.Run
// wants (anthropic.RunHandle, error). Go interfaces require the
// return type to match exactly, and Go does not auto-promote the
// concrete *engine.Run to an interface even when the concrete type
// satisfies the target interface — the conversion has to happen at
// the call site. anthropicRunHandleAdapter is that conversion.
type anthropicEngineAdapter struct {
	engine *engine.Engine
}

// Collect satisfies anthropic.Engine.Collect by delegating verbatim.
// Error wrapping uses fmt.Errorf("anthropic engine collect: %w", err)
// so wrapcheck is satisfied while preserving errors.Is/As semantics.
func (a anthropicEngineAdapter) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	resp, err := a.engine.Collect(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("anthropic engine collect: %w", err)
	}
	return resp, nil
}

// Run satisfies anthropic.Engine.Run by wrapping the concrete *engine.Run
// in anthropicRunHandleAdapter.
func (a anthropicEngineAdapter) Run(ctx context.Context, req *canonical.ChatRequest) (anthropic.RunHandle, error) {
	run, err := a.engine.Run(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("anthropic engine run: %w", err)
	}
	return anthropicRunHandleAdapter{run: run}, nil
}

// anthropicRunHandleAdapter adapts *engine.Run to anthropic.RunHandle.
// engine.Run.Stream() returns engine.Stream (interface); anthropic
// declares its own Stream interface that is structurally compatible
// — the concrete chunk channel + Result method match. The pass-through
// works because Go IS willing to assign one interface value to another
// when the source's method set is a superset of the destination's.
type anthropicRunHandleAdapter struct {
	run *engine.Run
}

func (h anthropicRunHandleAdapter) Stream() anthropic.Stream {
	// *engine.Run.Stream() returns engine.Stream; anthropic.Stream's
	// method set (Chunks() <-chan canonical.Chunk + Result()
	// (*canonical.FinalResult, error)) is structurally identical, so
	// assigning the concrete value to the anthropic.Stream interface
	// variable works.
	return h.run.Stream()
}

func (h anthropicRunHandleAdapter) SessionID() string {
	return h.run.SessionID()
}

func (h anthropicRunHandleAdapter) StopWatchdog() func() bool {
	return h.run.StopWatchdog()
}

// openaiEngineAdapter wraps a concrete *engine.Engine and adapts its Run
// signature to openai.Engine. Mirrors anthropicEngineAdapter exactly —
// same Go return-type-invariance rationale (cmd-level seam, TRST-04).
type openaiEngineAdapter struct {
	engine *engine.Engine
}

// Collect satisfies openai.Engine.Collect by delegating verbatim.
func (a openaiEngineAdapter) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	resp, err := a.engine.Collect(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("openai engine collect: %w", err)
	}
	return resp, nil
}

// Run satisfies openai.Engine.Run by wrapping the concrete *engine.Run
// in openaiRunHandleAdapter.
func (a openaiEngineAdapter) Run(ctx context.Context, req *canonical.ChatRequest) (openai.RunHandle, error) {
	run, err := a.engine.Run(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("openai engine run: %w", err)
	}
	return openaiRunHandleAdapter{run: run}, nil
}

// ollamaEngineAdapter wraps a concrete *engine.Engine and adapts its Run
// signature to ollama.Engine. Mirrors anthropicEngineAdapter exactly —
// same Go return-type-invariance rationale: *engine.Engine.Run returns
// (*engine.Run, error) while ollama.Engine.Run wants (ollama.RunHandle, error).
// This is the cmd-level seam that keeps internal/adapter/ollama free of any
// internal/engine import (TRST-04 boundary — enforced by .go-arch-lint.yml).
//
// When a.engine is nil (degraded mode) engineForAdapter stays nil and the
// nil-engine guard in ollama.handleChat/handleGenerate returns 503.
type ollamaEngineAdapter struct {
	engine *engine.Engine
}

// Collect satisfies ollama.Engine.Collect by delegating verbatim.
func (a ollamaEngineAdapter) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	resp, err := a.engine.Collect(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ollama engine collect: %w", err)
	}
	return resp, nil
}

// Run satisfies ollama.Engine.Run by wrapping the concrete *engine.Run
// in ollamaRunHandleAdapter.
func (a ollamaEngineAdapter) Run(ctx context.Context, req *canonical.ChatRequest) (ollama.RunHandle, error) {
	run, err := a.engine.Run(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ollama engine run: %w", err)
	}
	return ollamaRunHandleAdapter{run: run}, nil
}

// ollamaRunHandleAdapter adapts *engine.Run to ollama.RunHandle.
// Mirrors anthropicRunHandleAdapter — same structural-compatibility
// reasoning for engine.Stream → ollama.Stream assignment.
type ollamaRunHandleAdapter struct {
	run *engine.Run
}

func (h ollamaRunHandleAdapter) Stream() ollama.Stream {
	return h.run.Stream()
}

func (h ollamaRunHandleAdapter) SessionID() string {
	return h.run.SessionID()
}

func (h ollamaRunHandleAdapter) StopWatchdog() func() bool {
	return h.run.StopWatchdog()
}

// openaiRunHandleAdapter adapts *engine.Run to openai.RunHandle.
// Mirrors anthropicRunHandleAdapter — same structural-compatibility
// reasoning for engine.Stream → openai.Stream assignment.
type openaiRunHandleAdapter struct {
	run *engine.Run
}

func (h openaiRunHandleAdapter) Stream() openai.Stream {
	return h.run.Stream()
}

func (h openaiRunHandleAdapter) SessionID() string {
	return h.run.SessionID()
}

func (h openaiRunHandleAdapter) StopWatchdog() func() bool {
	return h.run.StopWatchdog()
}
