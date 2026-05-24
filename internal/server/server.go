// Package server provides the HTTP server for the Loop24 gateway.
// It wires the chi router, middleware chain, and request handlers.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"otto-gateway/internal/auth"
	"otto-gateway/internal/config"
)

// PoolStatsSource is the consumer-defined interface healthHandler uses
// to render the {pool: {size, alive, busy}} sub-tree without importing
// internal/pool's Stats type into the server's public surface. *pool.Pool
// satisfies it structurally; a nil source is handled by healthHandler.
type PoolStatsSource interface {
	Stats() PoolStats
}

// Config bundles the dependencies and wiring the server needs when
// constructed via NewFromConfig. It replaces the Phase 1 (Config, logger,
// version) constructor signature without removing it — New is kept as a
// thin wrapper so Phase 1 callers (and a degraded "/health only" mode
// when KIRO_CMD is unset) continue to work.
//
// Fields:
//   - Logger / Version / Commit / HTTPAddr — straight propagation.
//   - AuthTokens: bearer-token set; empty disables auth (Node parity).
//   - AllowedPrefixes: IP allowlist CIDRs; empty disables (Node parity).
//   - AuthTrustXFF: Codex H-7 — propagated into auth.IPAllowlist's
//     TrustXForwardedFor flag.
//   - OllamaPath: "/api" (from cfg.OllamaPathPrefix).
//   - OllamaProtectedRouter: chi sub-router mounted under OllamaPath
//     INSIDE the auth-protected sub-tree (Codex M-4 — adapter's
//     ProtectedRouter() output).
//   - OllamaVersionHandler: /api/version handler mounted on the OUTER
//     router (auth-exempt per AUTH-03; Codex M-4 fix).
//   - AnthropicPath: "/v1" (from cfg.AnthropicPathPrefix). Phase 3.1
//     D-17 parallel mount; the block is skipped when AnthropicPath is
//     empty OR AnthropicProtectedRouter is nil.
//   - AnthropicProtectedRouter: chi sub-router mounted under
//     AnthropicPath INSIDE the auth-protected sub-tree. The SAME
//     auth.Bearer + auth.IPAllowlist chain wraps it as the Ollama
//     surface (D-15 "one middleware, one mental model" — D-15 dual-
//     header reading applies to both surfaces). D-18: no Anthropic
//     equivalent of /api/version is exposed on the outer router.
//   - Pool: PoolStatsSource for /health (may be nil when KIRO_CMD unset).
type Config struct {
	Logger                   *slog.Logger
	Version                  string
	Commit                   string
	HTTPAddr                 string
	AuthTokens               []string
	AllowedPrefixes          []netip.Prefix
	AuthTrustXFF             bool
	OllamaPath               string
	OllamaProtectedRouter    chi.Router
	OllamaVersionHandler     http.HandlerFunc
	AnthropicPath            string
	AnthropicProtectedRouter chi.Router
	Pool                     PoolStatsSource
}

// Server wraps the chi router and HTTP server with structured logging.
type Server struct {
	cfg     config.Config // legacy — kept for httpAddr fallback when only the Phase 1 path is used
	logger  *slog.Logger
	router  chi.Router
	version string
	commit  string
	start   time.Time
	pool    PoolStatsSource
	addr    string
}

// New is the Phase 1 compatibility constructor — used when only the
// /health surface is wired (KIRO_CMD unset, or tests that exercise the
// pre-Phase-2 minimal server). For full Phase 2 wiring (auth, ollama
// sub-router, exempt /api/version on the outer router) call NewFromConfig.
func New(cfg config.Config, logger *slog.Logger, version string) *Server {
	return NewWithCommit(cfg, logger, version, "unknown")
}

// NewWithCommit is like New but also accepts a commit hash for the version endpoint.
func NewWithCommit(cfg config.Config, logger *slog.Logger, version, commit string) *Server {
	s := &Server{
		cfg:     cfg,
		logger:  logger,
		version: version,
		commit:  commit,
		start:   time.Now(),
		addr:    cfg.HTTPAddr,
	}
	s.router = chi.NewRouter()

	// Middleware order is non-negotiable: RequestID first, then Recoverer, then accessLog.
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.Recoverer)
	s.router.Use(accessLog(logger))

	s.router.Get("/health", s.healthHandler)
	s.router.Get("/api/version", s.versionHandler)

	return s
}

// NewFromConfig constructs a fully-wired Phase 2 server. The router is
// built with:
//   - outer middleware: RequestID → Recoverer → accessLog (D-13;
//     Pitfall 2: accessLog runs BEFORE auth so denied requests appear
//     in logs).
//   - outer exempt routes: / (root) + /health + cfg.OllamaPath+"/version"
//     via cfg.OllamaVersionHandler. Codex M-4: /api/version registered
//     EXACTLY ONCE on the outer router (no inner /version registration);
//     no precedence dance.
//   - protected sub-tree: r.Route(cfg.OllamaPath, ...) applies
//     auth.Bearer + auth.IPAllowlist (with Codex H-7 TrustXForwardedFor
//     threaded from cfg.AuthTrustXFF), then mounts the adapter's
//     protected router via r.Mount("/", cfg.OllamaProtectedRouter).
//
// cfg.Logger must be non-nil for production use; tests pass a t.Log-
// backed logger via testutil.Logger.
func NewFromConfig(cfg Config) *Server {
	s := &Server{
		logger:  cfg.Logger,
		version: cfg.Version,
		commit:  cfg.Commit,
		start:   time.Now(),
		pool:    cfg.Pool,
		addr:    cfg.HTTPAddr,
	}
	s.router = chi.NewRouter()
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.Recoverer)
	s.router.Use(accessLog(cfg.Logger))

	// Exempt outer routes — auth + IP allowlist NOT applied here.
	s.router.Get("/", s.rootHandler)
	s.router.Get("/health", s.healthHandler)
	if cfg.OllamaVersionHandler != nil && cfg.OllamaPath != "" {
		// Codex M-4: register /api/version on the OUTER router so it
		// stays exempt. The adapter does NOT register /version on its
		// protected router; there is exactly one registration site.
		s.router.Get(cfg.OllamaPath+"/version", cfg.OllamaVersionHandler)
	}

	// Protected sub-tree — auth.Bearer + auth.IPAllowlist scoped to
	// cfg.OllamaPath only. /, /health, /api/version remain exempt.
	if cfg.OllamaPath != "" && cfg.OllamaProtectedRouter != nil {
		s.router.Route(cfg.OllamaPath, func(r chi.Router) {
			r.Use(auth.Bearer(auth.Config{
				Logger: cfg.Logger,
				Tokens: cfg.AuthTokens,
			}))
			r.Use(auth.IPAllowlist(auth.Config{
				Logger:             cfg.Logger,
				AllowedPrefixes:    cfg.AllowedPrefixes,
				TrustXForwardedFor: cfg.AuthTrustXFF, // Codex H-7
			}))
			r.Mount("/", cfg.OllamaProtectedRouter)
		})
	}

	// Phase 3.1 D-17: parallel Anthropic mount block. Same auth chain
	// as the Ollama branch — auth.Bearer reads BOTH Authorization:
	// Bearer AND x-api-key via the D-15 extractToken helper, so
	// loop24-client's preferred x-api-key path works without any
	// adapter-specific middleware. D-18: no /v1/version equivalent
	// is exposed on the outer router (Anthropic has no public
	// /version surface).
	if cfg.AnthropicPath != "" && cfg.AnthropicProtectedRouter != nil {
		s.router.Route(cfg.AnthropicPath, func(r chi.Router) {
			r.Use(auth.Bearer(auth.Config{
				Logger: cfg.Logger,
				Tokens: cfg.AuthTokens,
			}))
			r.Use(auth.IPAllowlist(auth.Config{
				Logger:             cfg.Logger,
				AllowedPrefixes:    cfg.AllowedPrefixes,
				TrustXForwardedFor: cfg.AuthTrustXFF, // Codex H-7
			}))
			r.Mount("/", cfg.AnthropicProtectedRouter)
		})
	}

	return s
}

// ServeHTTP implements http.Handler, delegating to the chi router.
// This enables direct handler testing with httptest.NewRecorder without starting a listener.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// rootHandler serves GET / with a tiny JSON identity line. Phase 1's
// "/" was unhandled (404); Phase 2 promotes it to an exempt liveness
// surface (kept simple — operators run /health for the full envelope).
func (s *Server) rootHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"name":"loop24-gateway"}` + "\n"))
}

// Run starts the HTTP server and blocks until ctx is cancelled.
// When ctx is done, it calls http.Server.Shutdown with a 30-second deadline (D-16).
// This is the testable lifecycle method — tests cancel the context to verify shutdown.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second, // mitigates Slowloris (gosec G112)
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("listening", "addr", s.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("server: listen: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.logger.Info("context cancelled; shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server: shutdown: %w", err)
	}
	return nil
}

// RunUntilSignal starts the HTTP server and blocks until SIGINT, SIGTERM, or ctx cancellation.
// It is a thin wrapper around Run that wires OS signal handling.
// D-22: the binary stays foreground-only — no start/stop subcommands.
func (s *Server) RunUntilSignal(ctx context.Context) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	derivedCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-sigCh:
			s.logger.Info("shutdown signal received")
			cancel()
		case <-derivedCtx.Done():
		}
	}()

	return s.Run(derivedCtx)
}
