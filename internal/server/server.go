// Package server provides the HTTP server for the OTTO Gateway.
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
	"sort"
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

// RouteRegistrar is the interface each adapter implements to register
// its routes onto a shared chi sub-router. Using direct r.Post/r.Get
// calls (rather than r.Mount("/", subrouter)) avoids the chi double-Mount
// panic that occurs when two adapters share the same prefix (e.g. both
// Anthropic and OpenAI default to "/v1"). See D-01 in RESEARCH.md.
type RouteRegistrar interface {
	RegisterRoutes(r chi.Router)
}

// SurfaceMount pairs a URL prefix with a RouteRegistrar. Multiple
// SurfaceMount entries may share the same Prefix; NewFromConfig groups
// them and opens ONE auth-wrapped r.Route block per unique prefix so
// that two surfaces on "/v1" (Anthropic + OpenAI) do not collide.
type SurfaceMount struct {
	// Prefix is the URL path prefix, e.g. "/api" or "/v1".
	Prefix string
	// Router registers the surface's routes onto the shared sub-router
	// via r.Post / r.Get (never r.Mount("/", …)).
	Router RouteRegistrar
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
//   - OllamaVersionHandler: /api/version handler mounted on the OUTER
//     router (auth-exempt per AUTH-03; Codex M-4 fix). Only registered
//     when OllamaVersionPath is non-empty.
//   - OllamaVersionPath: the full outer path for the version handler,
//     e.g. "/api/version". Typically cfg.OllamaPathPrefix+"/version".
//   - Surfaces: the list of adapters to mount. Each entry contributes
//     its routes to a shared auth-wrapped Route block for its Prefix.
//     Entries with the same Prefix share one block (D-01 grouping).
//   - Pool: PoolStatsSource for /health (may be nil when KIRO_CMD unset).
type Config struct {
	Logger               *slog.Logger
	Version              string
	Commit               string
	HTTPAddr             string
	AuthTokens           []string
	AllowedPrefixes      []netip.Prefix
	AuthTrustXFF         bool
	OllamaVersionPath    string       // outer exempt path, e.g. "/api/version"
	OllamaVersionHandler http.HandlerFunc
	Surfaces             []SurfaceMount
	Pool                 PoolStatsSource
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

// NewFromConfig constructs a fully-wired server. The router is built with:
//   - outer middleware: RequestID → Recoverer → accessLog (D-13;
//     Pitfall 2: accessLog runs BEFORE auth so denied requests appear
//     in logs).
//   - outer exempt routes: / (root) + /health + cfg.OllamaVersionPath
//     via cfg.OllamaVersionHandler. Codex M-4: /api/version registered
//     EXACTLY ONCE on the outer router (no inner /version registration);
//     no precedence dance.
//   - protected sub-trees: cfg.Surfaces grouped by Prefix. For each
//     unique Prefix, ONE auth-wrapped r.Route block is opened that
//     applies auth.Bearer + auth.IPAllowlist (Codex H-7 TrustXForwardedFor
//     threaded from cfg.AuthTrustXFF) ONCE. Each SurfaceMount within the
//     group calls sm.Router.RegisterRoutes(r) — direct r.Post/r.Get calls
//     onto the shared sub-router (D-01 mechanic: never r.Mount("/", …)
//     which would panic when two surfaces share one prefix).
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
	if cfg.OllamaVersionHandler != nil && cfg.OllamaVersionPath != "" {
		// Codex M-4: register /api/version on the OUTER router so it
		// stays exempt. The adapter does NOT register /version on its
		// protected router; there is exactly one registration site.
		s.router.Get(cfg.OllamaVersionPath, cfg.OllamaVersionHandler)
	}

	// D-01: group Surfaces by prefix and open ONE auth-wrapped Route
	// block per unique prefix. This avoids the chi double-Mount panic
	// that occurs when two adapters share the same prefix (Anthropic +
	// OpenAI both default to "/v1"). Within each prefix's block, each
	// adapter registers its own routes via RegisterRoutes(r) which
	// calls r.Post/r.Get directly — never r.Mount("/", …).
	byPrefix := make(map[string][]SurfaceMount)
	for _, sm := range cfg.Surfaces {
		byPrefix[sm.Prefix] = append(byPrefix[sm.Prefix], sm)
	}
	// Sort prefixes for deterministic route registration order.
	prefixes := make([]string, 0, len(byPrefix))
	for p := range byPrefix {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	for _, prefix := range prefixes {
		mounts := byPrefix[prefix]
		// Capture prefix for the closure.
		p := prefix
		s.router.Route(p, func(r chi.Router) {
			r.Use(auth.Bearer(auth.Config{
				Logger: cfg.Logger,
				Tokens: cfg.AuthTokens,
			}))
			r.Use(auth.IPAllowlist(auth.Config{
				Logger:             cfg.Logger,
				AllowedPrefixes:    cfg.AllowedPrefixes,
				TrustXForwardedFor: cfg.AuthTrustXFF, // Codex H-7
			}))
			for _, sm := range mounts {
				sm.Router.RegisterRoutes(r)
			}
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
	_, _ = w.Write([]byte(`{"name":"otto-gateway"}` + "\n"))
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
