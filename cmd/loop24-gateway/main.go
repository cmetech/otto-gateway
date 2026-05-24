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
	"fmt"
	"log/slog"
	"os"
	"time"

	"loop24-gateway/internal/adapter/ollama"
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
	cfg, err := config.Load()
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

	// adapter.New tolerates nil Engine / nil ModelCatalog — the
	// degraded mode keeps /, /health, /api/version, and the stub
	// endpoints serving while chat/generate return 503.
	var engineForAdapter ollama.Engine
	if a.engine != nil {
		engineForAdapter = a.engine
	}
	var catalogForAdapter ollama.ModelCatalog
	if a.pool != nil {
		catalogForAdapter = a.pool
	}
	adapter := ollama.New(ollama.Config{
		Logger:       logger,
		Engine:       engineForAdapter,
		ModelCatalog: catalogForAdapter,
		Version:      version.Version,
		Commit:       version.Commit(),
	})

	var poolForServer server.PoolStatsSource
	if a.pool != nil {
		poolForServer = poolStatsAdapter{pool: a.pool}
	}

	a.srv = server.NewFromConfig(server.Config{
		Logger:                logger,
		Version:               version.Version,
		Commit:                version.Commit(),
		HTTPAddr:              cfg.HTTPAddr,
		AuthTokens:            cfg.AuthToken,
		AllowedPrefixes:       cfg.AllowedIPs,
		AuthTrustXFF:          cfg.AuthTrustXFF, // Codex H-7 wiring path complete
		OllamaPath:            cfg.OllamaPathPrefix,
		OllamaProtectedRouter: adapter.ProtectedRouter(),
		OllamaVersionHandler:  adapter.HandleVersion(), // Codex M-4 split accessor
		Pool:                  poolForServer,
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

// keep errors import live for callers that want to inspect typed errors
// returned by newApp's pool.Warmup wrapping.
var _ = errors.Is
