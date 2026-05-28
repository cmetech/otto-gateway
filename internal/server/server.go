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

// RegistryStatsSource is the consumer-defined interface healthHandler +
// agentsHandler use to render the sessions sub-tree without importing
// internal/session into the server's public surface. The cmd/otto-gateway
// registryStatsAdapter wraps *session.Registry to satisfy this interface;
// a nil source is handled by both handlers (sessions render as zero / nil).
// Defined here next to PoolStatsSource so the two stats sources mirror.
type RegistryStatsSource interface {
	Stats() SessionStats
	Detail() []AgentSession
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
//   - PoolDetail: PoolDetailSource for /health/agents per-slot rows
//     (D-15). May be nil; agentsHandler renders empty pool detail.
//   - Registry: RegistryStatsSource for /health sessions.active + the
//     per-session detail rows in /health/agents (D-16). May be nil;
//     both handlers render zero / nil sessions.
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
	PoolDetail           PoolDetailSource
	Registry             RegistryStatsSource
	// AdminHandler is mounted at /admin on the OUTER router (auth-exempt
	// per Phase 6.1 D-01). When nil, /admin is unrouted. Mounting does
	// NOT affect existing exempt or SurfaceMount registrations per
	// D-15/D-16; verified by server_admin_test.go.
	AdminHandler http.Handler

	// Hooks is the read-only chain introspection source consumed by GET
	// /health/hooks (Phase 8 OBSV-04 / SC7). The cmd/otto-gateway
	// hooksDescriptionAdapter wraps *plugin.Chain to satisfy this
	// without importing internal/plugin into server (TRST-04). When
	// nil, the handler renders {"hooks": []}.
	Hooks HooksDescriptionSource
}

// Server wraps the chi router and HTTP server with structured logging.
type Server struct {
	cfg        config.Config // legacy — kept for httpAddr fallback when only the Phase 1 path is used
	logger     *slog.Logger
	router     chi.Router
	version    string
	commit     string
	start      time.Time
	pool       PoolStatsSource
	poolDetail PoolDetailSource
	registry   RegistryStatsSource
	hooks      HooksDescriptionSource // Phase 8 OBSV-04 — GET /health/hooks introspection
	addr       string
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
// backed logger via testutil.Logger. WR-03 fix: when cfg.Logger is
// nil (a zero-value Config — used by some tests that construct the
// server directly without the middleware chain) install a discard
// logger so handler paths that fall back to s.logger never panic on
// a nil deref.
func NewFromConfig(cfg Config) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(serverDiscardWriter{}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	s := &Server{
		logger:     logger,
		version:    cfg.Version,
		commit:     cfg.Commit,
		start:      time.Now(),
		pool:       cfg.Pool,
		poolDetail: cfg.PoolDetail,
		registry:   cfg.Registry,
		hooks:      cfg.Hooks, // Phase 8 OBSV-04
		addr:       cfg.HTTPAddr,
	}
	s.router = chi.NewRouter()
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.Recoverer)
	s.router.Use(accessLog(logger))

	// Exempt outer routes — auth + IP allowlist NOT applied here.
	s.router.Get("/", s.rootHandler)
	s.router.Get("/health", s.healthHandler)
	// D-18: /health/agents is auth-exempt, registered on the OUTER router
	// alongside /health. The detail endpoint exposes full session ids
	// verbatim (D-17) for operator dashboards.
	s.router.Get("/health/agents", s.agentsHandler)
	// Phase 8 OBSV-04 / SC7: /health/hooks is auth-exempt, registered on
	// the OUTER router alongside /health and /health/agents. View-only —
	// no runtime mutate path (PROJECT.md line 108; SC7). Restart to
	// change config. hooksHandler returns 405 on POST/PUT/DELETE as
	// defense-in-depth even though chi's .Get() already restricts to GET.
	s.router.Get("/health/hooks", s.hooksHandler)
	// Phase 8 SC7 belt-and-suspenders: registering mutating verbs
	// explicitly to return 405 (instead of relying on chi's
	// MethodNotAllowedHandler default) so the no-mutate-path contract
	// is visible at the route table.
	s.router.MethodFunc(http.MethodPost, "/health/hooks", s.hooksHandler)
	s.router.MethodFunc(http.MethodPut, "/health/hooks", s.hooksHandler)
	s.router.MethodFunc(http.MethodDelete, "/health/hooks", s.hooksHandler)
	if cfg.OllamaVersionHandler != nil && cfg.OllamaVersionPath != "" {
		// Codex M-4: register /api/version on the OUTER router so it
		// stays exempt. The adapter does NOT register /version on its
		// protected router; there is exactly one registration site.
		s.router.Get(cfg.OllamaVersionPath, cfg.OllamaVersionHandler)
	}

	// Phase 6.1 D-07/D-15/D-16: admin handler mounts on the OUTER router,
	// auth-exempt (D-01), and does NOT participate in ENABLED_SURFACES
	// gating. Mount order: AFTER the existing exempt routes (/health etc.)
	// so chi's route-registration order matches the auth-exempt posture
	// documentation. When nil, /admin is unrouted (no-op).
	if cfg.AdminHandler != nil {
		s.router.Mount("/admin", cfg.AdminHandler)
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
			// Phase 8 PLUG-03 / Pattern F: auth.Bearer middleware
			// REMOVED here. Bearer-token validation now happens at the
			// canonical layer via plugin.AuthHook (slice 2 + slice 5
			// chain wiring in main.go). The adapter handlers stamp the
			// credential onto ctx via canonical.WithBearerToken BEFORE
			// engine entry; AuthHook reads via BearerTokenFromContext
			// and short-circuits on bad/missing token. The per-surface
			// adapter renders the canonical short-circuit envelope as
			// its native error shape.
			//
			// auth.IPAllowlist STAYS — it is surface-agnostic IP
			// gatekeeping that runs BEFORE the engine and does not
			// need canonical semantics (CONTEXT.md PLUG-04).
			//
			// Accepted v1 risk (T-8-AUTH-BYPASS, plan 08-05): non-
			// engine routes (e.g., /api/tags, /api/ps, /api/show) lose
			// bearer-token gating. These are read-only catalog stubs
			// that don't reach the engine; the IPAllowlist still
			// applies. Operators who need bearer auth on these
			// endpoints can restore auth.Bearer in a downstream
			// configuration.
			r.Use(auth.IPAllowlist(auth.Config{
				Logger:             logger,
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

// serverDiscardWriter is a no-op io.Writer used by NewFromConfig when
// cfg.Logger is nil (WR-03 fallback). Mirrors the discardWriter
// pattern in the adapter packages but kept package-local to avoid
// adding an io import just for io.Discard.
type serverDiscardWriter struct{}

func (serverDiscardWriter) Write(p []byte) (int, error) { return len(p), nil }
