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
// scripts/otto-gw (POSIX) and scripts/otto-gw.ps1 (PowerShell). Never add
// lifecycle subcommands to the binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/DeRuina/timberjack"

	"otto-gateway/internal/adapter/anthropic"
	"otto-gateway/internal/adapter/ollama"
	"otto-gateway/internal/adapter/openai"
	"otto-gateway/internal/admin"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/config"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin"
	"otto-gateway/internal/plugin/pii"
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

	logger, closeLogger := buildLogger(cfg)
	defer closeLogger()

	// Surface which env file (if any) the wrapper sourced. The bash and
	// PowerShell wrappers export OTTO_ENV_FILE_LOADED with the resolved
	// path so operators can confirm in the structured log which file is
	// actually in effect (project-local vs per-user vs CLI override).
	// Empty when the binary is started without a wrapper.
	if envFile := os.Getenv("OTTO_ENV_FILE_LOADED"); envFile != "" {
		logger.Info("env file loaded", "path", envFile)
	} else {
		logger.Info("env file loaded", "path", "(none — inherited environment only)")
	}

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
	//
	// WR-06 ordering dependency: a.registry is constructed AFTER
	// a.pool below. On a pool.Warmup failure the early-return path
	// below calls this closure with a.registry == nil; that path is
	// nil-safe via the registry != nil guard. On a successful Warmup
	// followed by some later failure (e.g., server build), both
	// a.pool and a.registry are non-nil and both Close calls run.
	//
	// pool.Close is internally idempotent (closeOnce + closeAll
	// nils p.all so a second call iterates an empty slice). Warmup's
	// own partial-failure path calls closeAll directly — this cleanup
	// closure may therefore observe an already-drained pool. That is
	// load-bearing benign; do not "optimise" by skipping the second
	// Close.
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

	// Phase 8 D-01 — hardcoded plugin chain literal (no Register/factory
	// indirection). Adding a 5th hook = one line in this slice. The
	// PRE chain runs in registration order: RequestID → Auth → PII →
	// Logging (D-04). The POST chain has LoggingHook as the only entry
	// (RequestIDHook + AuthHook + PIIRedactionHook are Pre-only;
	// LoggingHook bridges Pre→Post via its sync.Map keyed by
	// request_id per slice 3's design).
	//
	// LoggingHook is intentionally reused as the SAME instance for
	// both Pre (last) and Post (only) — its per-instance sync.Map
	// bridges Pre→Post timings via request_id. /health/hooks dedup
	// (slice 5 Task 4) elides the duplicate row.
	loggingHook := &plugin.LoggingHook{Logger: logger}
	chain := plugin.Chain{
		Pre: []engine.PreHook{
			&plugin.RequestIDHook{Logger: logger},
			&plugin.AuthHook{Tokens: cfg.AuthToken},
			&pii.PIIRedactionHook{
				Recognizers:     filterRecognizers(pii.Recognizers, cfg.PIIEnabledEntities),
				Enabled:         cfg.PIIRedactionEnabled,
				Mode:            cfg.PIIRedactionMode,
				HashKey:         []byte(cfg.PIIHashKey),
				EnabledEntities: cfg.PIIEnabledEntities,
			},
			loggingHook,
		},
		Post: []engine.PostHook{
			loggingHook,
		},
	}
	// D-02 typo-fail-fast — Filter validates the allowlist names
	// against the runtime chain. Unknown names produce a boot error
	// containing literal substring "unknown hook" naming each offender.
	filteredChain, filterErr := chain.Filter(cfg.EnabledHooks)
	if filterErr != nil {
		return nil, func() {}, fmt.Errorf("chain filter: %w", filterErr)
	}
	chain = filteredChain

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
			PreHooks:   chain.Pre,
			PostHooks:  chain.Post,
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
			TickInterval: cfg.SessionTickInterval,
			MaxSessions:  cfg.SessionMax,
			KiroCmd:      cfg.KiroCmd,
			KiroArgs:     cfg.KiroArgs,
			KiroCWD:      cfg.KiroCWD,
			PingInterval: cfg.PingInterval,
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
				PreHooks:   chain.Pre,  // Phase 8 — per-session chain
				PostHooks:  chain.Post, // Phase 8 — per-session chain
			})}
		}
		openaiEngineForSession = func(entry *session.Entry) openai.Engine {
			return openaiEngineAdapter{engine: engine.New(engine.Config{
				Logger:     logger,
				ACP:        entry,
				DefaultCWD: cfg.KiroCWD,
				PreHooks:   chain.Pre,  // Phase 8
				PostHooks:  chain.Post, // Phase 8
			})}
		}
		anthropicEngineForSession = func(entry *session.Entry) anthropic.Engine {
			return anthropicEngineAdapter{engine: engine.New(engine.Config{
				Logger:     logger,
				ACP:        entry,
				DefaultCWD: cfg.KiroCWD,
				PreHooks:   chain.Pre,  // Phase 8
				PostHooks:  chain.Post, // Phase 8
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
	// /health/pool — pool serving-health probe. Nil-safe: when the pool
	// is unwired (KIRO_CMD unset → degraded mode), the handler renders
	// the canonical "no pool wired = healthy" envelope from a nil source.
	var poolHealthForServer server.PoolHealthSource
	if a.pool != nil {
		poolHealthForServer = cmdPoolHealthAdapter{pool: a.pool}
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

	// Phase 6.1: build the admin observability handler.
	//
	// admin owns its own SnapshotSlot/SnapshotSess wire types (Plan 01 Task 1,
	// option (b) — TRST-04 strict: admin must not import internal/server).
	// adminPoolDetailAdapter and adminRegistryAdapter (declared at file scope
	// below) bridge from the server-typed adapter values to admin's types via
	// field-by-field copy. Cost: O(POOL_SIZE + SESSION_MAX) per snapshot poll,
	// dwarfed by JSON marshalling cost.
	//
	// Per RESEARCH Open Question 3 (RESOLVED): pass time.Now() at wire-up
	// rather than exporting server.Server.start; uptime drifts by a few ms,
	// acceptable per CONTEXT.md.
	//
	// Tailer log path resolution (Phase 8 follow-up — log rotation):
	//   1. LOG_FILE if set (the canonical env the gateway logger writes to)
	//   2. OTTO_LOG (legacy / wrapper-set path, retained for back-compat)
	//   3. ./logs/otto-gateway.log (matches the packaged distribution layout)
	// The tailer's inode-tracking reopen (internal/admin/tail.go) keeps
	// streaming across timberjack's daily rotation without UI interruption.
	var adminPoolDetail admin.PoolDetailSource
	if poolDetailForServer != nil {
		adminPoolDetail = adminPoolDetailAdapter{src: poolDetailForServer}
	}
	var adminRegistry admin.RegistryStatsSource
	if registryForServer != nil {
		adminRegistry = adminRegistryAdapter{src: registryForServer}
	}
	// Quick 260529-ll2: shimmed to the new LogPaths/LogPathOrder Deps
	// shape; the proper multi-source wiring (main, boot-err, chat-trace)
	// lands with Task 4 when the chat-trace rotator is constructed.
	tailerLogPath := envOrDefault("LOG_FILE",
		envOrDefault("OTTO_LOG", "./logs/otto-gateway.log"))
	adminHandler := admin.Handler(admin.Deps{
		Logger:       logger,
		Version:      version.Version,
		Commit:       version.Commit(),
		Start:        time.Now(),
		PoolDetail:   adminPoolDetail,
		Registry:     adminRegistry,
		LogPaths:     map[string]string{"main": tailerLogPath},
		LogPathOrder: []string{"main"},
	})

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
		PoolHealth:           poolHealthForServer, // /health/pool — pool serving-health probe
		Registry:             registryForServer,   // Plan 05-03 D-14/D-16
		AdminHandler:         adminHandler,        // Phase 6.1 admin observability UI
		Hooks:                hooksDescriptionAdapter{chain: chain}, // Phase 8 OBSV-04 — /health/hooks
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

// adminPoolDetailAdapter bridges from server.PoolDetailSource (returning
// []server.AgentSlot) to admin.PoolDetailSource (returning []admin.SnapshotSlot).
// admin owns its own wire types (TRST-04: admin must not import internal/server),
// so this adapter does the per-row field copy at the cmd boundary. Cost is
// O(POOL_SIZE) per snapshot poll — negligible vs JSON marshalling overhead.
type adminPoolDetailAdapter struct {
	src server.PoolDetailSource
}

func (a adminPoolDetailAdapter) Detail() []admin.SnapshotSlot {
	rows := a.src.Detail()
	out := make([]admin.SnapshotSlot, len(rows))
	for i, r := range rows {
		out[i] = admin.SnapshotSlot{
			Label:            r.Label,
			Alive:            r.Alive,
			Busy:             r.Busy,
			CurrentSessionID: r.CurrentSessionID,
		}
	}
	return out
}

// adminRegistryAdapter bridges from server.RegistryStatsSource (returning
// []server.AgentSession) to admin.RegistryStatsSource (returning []admin.SnapshotSess).
// Same TRST-04 rationale and O(SESSION_MAX) cost as adminPoolDetailAdapter.
type adminRegistryAdapter struct {
	src server.RegistryStatsSource
}

func (a adminRegistryAdapter) Detail() []admin.SnapshotSess {
	rows := a.src.Detail()
	out := make([]admin.SnapshotSess, len(rows))
	for i, r := range rows {
		out[i] = admin.SnapshotSess{
			ID:       r.ID,
			Alive:    r.Alive,
			Busy:     r.Busy,
			LastUsed: r.LastUsed,
			Model:    r.Model,
		}
	}
	return out
}

// cmdPoolHealthAdapter wraps *pool.Pool to satisfy
// server.PoolHealthSource. Same one-line bridge pattern as
// poolStatsAdapter — converts pool.HealthSummary's runtime fields into
// server.PoolHealth's JSON-tagged wire fields without importing
// internal/pool into server (TRST-04). The structurally-identical
// types are intentional: server owns the wire shape, pool owns the
// runtime type.
type cmdPoolHealthAdapter struct {
	pool *pool.Pool
}

func (a cmdPoolHealthAdapter) Health() server.PoolHealth {
	h := a.pool.HealthSummary()
	out := server.PoolHealth{
		Size:           h.Size,
		Alive:          h.Alive,
		Busy:           h.Busy,
		Healthy:        h.Healthy,
		LastSpawnError: h.LastSpawnError,
	}
	// Pointer so encoding/json's omitempty actually omits when no
	// spawn failure has been recorded (struct-zero time.Time would
	// serialize as 0001-01-01T00:00:00Z otherwise).
	if !h.LastSpawnErrAt.IsZero() {
		ts := h.LastSpawnErrAt
		out.LastSpawnErrAt = &ts
	}
	return out
}

// hooksDescriptionAdapter wraps plugin.Chain to satisfy
// server.HooksDescriptionSource without importing internal/plugin into
// the server package (TRST-04). Same one-line bridge pattern as
// poolStatsAdapter / poolDetailAdapter — the field-by-field copy
// happens at the cmd-level boundary.
type hooksDescriptionAdapter struct {
	chain plugin.Chain
}

func (h hooksDescriptionAdapter) Describe() (pre, post []server.HookDescription) {
	pluginPre, pluginPost := h.chain.Describe()
	return convertHookDescriptions(pluginPre), convertHookDescriptions(pluginPost)
}

// convertHookDescriptions field-copies []plugin.HookDescription to
// []server.HookDescription. The two types are structurally identical
// (both ship `{Name, Kind, Enabled, Config}` with the same JSON
// tags); the copy preserves the TRST-04 boundary.
func convertHookDescriptions(in []plugin.HookDescription) []server.HookDescription {
	out := make([]server.HookDescription, len(in))
	for i, x := range in {
		out[i] = server.HookDescription{
			Name:    x.Name,
			Kind:    x.Kind,
			Enabled: x.Enabled,
			Config:  x.Config,
		}
	}
	return out
}

// filterRecognizers returns the subset of recognizers whose Name
// appears in entities. Empty entities returns recognizers unchanged
// (default = all six recognizers active per D-02). The PIIRedactionHook
// also internally filters via EnabledEntities at request time; this
// pre-filter is a startup-time efficiency to avoid handing the hook
// recognizers it will only skip — and keeps the /health/hooks
// `config.entities` list and the actual active set in sync.
func filterRecognizers(recognizers []pii.Recognizer, entities []string) []pii.Recognizer {
	if len(entities) == 0 {
		return recognizers
	}
	allow := make(map[string]struct{}, len(entities))
	for _, e := range entities {
		allow[e] = struct{}{}
	}
	out := make([]pii.Recognizer, 0, len(recognizers))
	for _, r := range recognizers {
		if _, ok := allow[r.Name]; ok {
			out = append(out, r)
		}
	}
	return out
}

// envOrDefault returns os.Getenv(key) if non-empty, else def.
// Local helper — keeps internal/config minimal; Phase 6.1's LogPath
// is the only consumer for now.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// buildLogger constructs the root *slog.Logger from the loaded config.
// D-15: never call slog.SetDefault. Logger is constructed once here and
// injected everywhere via package Config structs.
//
// Output sink is selected by the LOG_FILE env var:
//   - LOG_FILE unset → write JSON lines to os.Stdout (terminal/dev path;
//     also the contract the e2e harness depends on for stdout capture).
//   - LOG_FILE set   → write through a timberjack.Logger with daily
//     rotation at 00:00 local time and 7 days of compressed backups.
//
// The wrapper scripts (scripts/otto-gw, scripts/otto-gw.ps1) auto-set
// LOG_FILE to ./logs/otto-gateway.log on `start`/`restart` and leave it
// unset on `run`, so the same binary serves both background and
// foreground UX without a flag. The returned closer drains and closes
// any open rotation handles; the caller MUST defer it.
func buildLogger(cfg config.Config) (*slog.Logger, func()) {
	noop := func() {}
	opts := &slog.HandlerOptions{Level: cfg.LogLevel()}

	logFile := strings.TrimSpace(os.Getenv("LOG_FILE"))
	if logFile == "" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts)), noop
	}

	// Ensure the parent directory exists. The wrapper does this too,
	// but a direct binary invocation with LOG_FILE=/path/that/dne.log
	// should not silently lose every log line.
	if dir := filepath.Dir(logFile); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			// Fall back to stdout rather than silently dropping logs.
			// The error itself surfaces on first write attempt below.
			slog.New(slog.NewJSONHandler(os.Stderr, nil)).
				Warn("LOG_FILE parent mkdir failed; falling back to stdout",
					"path", logFile, "err", err)
			return slog.New(slog.NewJSONHandler(os.Stdout, opts)), noop
		}
	}

	rotator := &timberjack.Logger{
		Filename: logFile,
		// MaxSize is a SAFETY VALVE, not the primary trigger. With daily
		// rotation a low-traffic laptop install will never hit it, but a
		// log flood (chatty client, debug=true left on, attacker traffic)
		// would otherwise grow the active file unboundedly between
		// midnight rolls. 500 MB caps single-day pathology at ~3.5 GB
		// across the 7-day retention window — still finite on any modern
		// laptop disk.
		MaxSize:     500,                        // MB; safety valve only
		MaxAge:      7,                          // keep 7 days of rotated logs
		MaxBackups:  0,                          // age-based pruning only
		LocalTime:   true,                       // laptop-local timestamps
		Compression: "gzip",                     // compress rotated files
		RotateAt:    []string{"00:00"},          // daily at local midnight
		FileMode:    0o644,
	}

	logger := slog.New(slog.NewJSONHandler(rotator, opts))
	return logger, func() { _ = rotator.Close() }
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

// ShortCircuitResponse satisfies anthropic.RunHandle.ShortCircuitResponse
// (Phase 8 SC1). Delegates to the concrete *engine.Run accessor.
func (h anthropicRunHandleAdapter) ShortCircuitResponse() *canonical.ChatResponse {
	return h.run.ShortCircuitResponse()
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

// ShortCircuitResponse satisfies ollama.RunHandle.ShortCircuitResponse
// (Phase 08.1 INTEG-01). Delegates to the concrete *engine.Run accessor.
func (h ollamaRunHandleAdapter) ShortCircuitResponse() *canonical.ChatResponse {
	return h.run.ShortCircuitResponse()
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

// ShortCircuitResponse satisfies openai.RunHandle.ShortCircuitResponse
// (Phase 08.1 INTEG-01). Delegates to the concrete *engine.Run accessor.
func (h openaiRunHandleAdapter) ShortCircuitResponse() *canonical.ChatResponse {
	return h.run.ShortCircuitResponse()
}
