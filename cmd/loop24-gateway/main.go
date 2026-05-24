// Package main is the entry point for the Loop24 Gateway binary.
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
// scripts/loop24 (POSIX) and scripts/loop24.ps1 (PowerShell). Never add
// lifecycle subcommands to the binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"loop24-gateway/internal/adapter/anthropic"
	"loop24-gateway/internal/adapter/ollama"
	"loop24-gateway/internal/canonical"
	"loop24-gateway/internal/config"
	"loop24-gateway/internal/engine"
	"loop24-gateway/internal/pool"
	"loop24-gateway/internal/server"
	"loop24-gateway/internal/version"
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
	if errors.Is(err, flag.ErrHelp) {
		// Usage was already printed by the FlagSet; treat --help as success.
		os.Exit(0)
	}
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("config load failed", "err", err)
		os.Exit(1)
	}

	logger := buildLogger(cfg)

	// Auth-mode startup log line (T-02-31 mitigation + Codex H-7
	// surfaces XFF trust mode so operators see it).
	logger.Info("auth mode",
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
	cfg    config.Config
	logger *slog.Logger
	pool   *pool.Pool   // nil when KIRO_CMD unset
	engine *engine.Engine // nil when pool is nil
	srv    *server.Server
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

	// Cleanup closure — invoked once via the returned func. Closes the
	// pool best-effort; safe to call when pool is nil.
	cleanup := func() {
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
		engineForAdapter = a.engine
	}
	var catalogForAdapter ollama.ModelCatalog
	if a.pool != nil {
		catalogForAdapter = a.pool
	}

	var ollamaAdapter *ollama.Adapter
	if slices.Contains(cfg.EnabledSurfaces, "ollama") {
		ollamaAdapter = ollama.New(ollama.Config{
			Logger:       logger,
			Engine:       engineForAdapter,
			ModelCatalog: catalogForAdapter,
			Version:      version.Version,
			Commit:       version.Commit(),
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
			Logger: logger,
			Engine: eng,
		})
	}

	// Compose protected-router accessors with nil-safety. When a
	// surface is disabled the corresponding field stays nil and the
	// server.NewFromConfig mount block skips it.
	var (
		ollamaProtectedRouter    chi.Router
		ollamaVersionHandler     http.HandlerFunc
		anthropicProtectedRouter chi.Router
	)
	if ollamaAdapter != nil {
		ollamaProtectedRouter = ollamaAdapter.ProtectedRouter()
		ollamaVersionHandler = ollamaAdapter.HandleVersion()
	}
	if anthropicAdapter != nil {
		anthropicProtectedRouter = anthropicAdapter.ProtectedRouter()
	}

	if ollamaAdapter == nil && anthropicAdapter == nil {
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

	// Boot log surfaces the resolved surface set so operators see
	// what's actually mounted (closes a Phase 2 → Phase 3.1 ops gap).
	logger.Info("server: enabled surfaces",
		"enabled_surfaces", cfg.EnabledSurfaces,
		"ollama_mounted", ollamaAdapter != nil,
		"anthropic_mounted", anthropicAdapter != nil,
	)

	a.srv = server.NewFromConfig(server.Config{
		Logger:                   logger,
		Version:                  version.Version,
		Commit:                   version.Commit(),
		HTTPAddr:                 cfg.HTTPAddr,
		AuthTokens:               cfg.AuthToken,
		AllowedPrefixes:          cfg.AllowedIPs,
		AuthTrustXFF:             cfg.AuthTrustXFF, // Codex H-7 wiring path complete
		OllamaPath:               cfg.OllamaPathPrefix,
		OllamaProtectedRouter:    ollamaProtectedRouter,
		OllamaVersionHandler:     ollamaVersionHandler, // Codex M-4 split accessor
		AnthropicPath:            cfg.AnthropicPathPrefix,
		AnthropicProtectedRouter: anthropicProtectedRouter,
		Pool:                     poolForServer,
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
