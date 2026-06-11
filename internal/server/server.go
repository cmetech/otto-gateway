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
// to render the {pool: {size, alive, busy, status}} sub-tree without
// importing internal/pool's Stats type into the server's public
// surface. *pool.Pool satisfies it structurally; a nil source is
// handled by healthHandler.
//
// IsExhausted and LastProgressAt feed the D-05 PoolStats.Status enum
// (Plan 16-02 Task 3):
//   - IsExhausted() returning true → Status = "exhausted" (D-05b).
//   - Busy == Alive == Size AND time.Since(LastProgressAt()) >
//     poolDegradedStallThreshold → Status = "degraded" (D-05a).
//   - All other cases → Status = "ok".
//
// Plan 16-01 added both accessors to *pool.Pool (LastProgressAt as an
// atomic.Int64 UnixNano + IsExhausted under p.mu); the
// cmd/otto-gateway poolStatsAdapter forwards them so server stays
// import-clean of internal/pool (TRST-04).
type PoolStatsSource interface {
	Stats() PoolStats
	IsExhausted() bool
	LastProgressAt() time.Time
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
	OllamaVersionPath    string // outer exempt path, e.g. "/api/version"
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

	// PoolHealth is the consumer-defined source for GET /health/pool —
	// the "is the pool actually serving requests?" probe distinct from
	// the basic Stats() surfaced by /health. The cmd/otto-gateway
	// cmdPoolHealthAdapter wraps *pool.Pool to satisfy this without
	// importing internal/pool here (TRST-04). When nil, the handler
	// returns the canonical {"pool": {"size":0,...,"healthy":true}}
	// envelope (no pool wired = degraded by design = healthy).
	PoolHealth PoolHealthSource

	// ShutdownCh is an optional pre-allocated channel shared between the
	// server and the admin handler. When non-nil, NewFromConfig uses it
	// instead of creating a new one — this lets the caller (cmd/otto-gateway
	// newApp) create the channel once, pass it to admin.Deps, and also pass
	// it here so both consumers select on the same signal (REL-HTTP-01).
	// When nil, NewFromConfig allocates its own channel (tests + New path).
	ShutdownCh chan struct{}

	// BodyReadTimeout is the per-request body-read deadline applied to
	// chat-body POST handlers (REL-HTTP-04 / Plan 16-02). Plan 16-05 owns
	// the config-side parsing of HTTP_BODY_READ_TIMEOUT_SEC and populates
	// this field; Plan 16-02 applies the time.AfterFunc-based deadline
	// wrapper that calls r.Body.Close() on expiry so SSE response writes
	// remain unaffected (D-04b). Zero means "use the default" (Plan 16-02
	// will define the default); negative values do not reach here because
	// config.Load() rejects them at boot.
	BodyReadTimeout time.Duration
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
	poolHealth PoolHealthSource // GET /health/pool — pool serving-health probe
	registry   RegistryStatsSource
	hooks      HooksDescriptionSource // Phase 8 OBSV-04 — GET /health/hooks introspection
	addr       string

	// shutdownCh is closed by RegisterOnShutdown (called inside http.Server.Shutdown
	// on graceful shutdown). Long-lived in-process connections (admin SSE log tail)
	// select on this channel to exit within one poll interval rather than blocking
	// Shutdown for the full 30s grace (REL-HTTP-01).
	shutdownCh chan struct{}

	// forceCloseCh is closed by RunUntilSignal on second-signal force-exit.
	// Run() selects on this channel while waiting for srv.Shutdown to return;
	// when closed it calls srv.Close() which immediately tears down in-flight
	// requests so Run unblocks promptly instead of leaking a goroutine
	// parked inside the 30s graceful Shutdown (WR-08).
	forceCloseCh chan struct{}
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
		cfg:        cfg,
		logger:     logger,
		version:    version,
		commit:     commit,
		start:      time.Now(),
		addr:         cfg.HTTPAddr,
		shutdownCh:   make(chan struct{}),
		forceCloseCh: make(chan struct{}),
	}
	s.router = chi.NewRouter()

	// Middleware order is non-negotiable: RequestID first, then Recoverer, then accessLog.
	//
	// CAUTION (audit server-recoverer-blind-to-handler-goroutines): chi
	// Recoverer covers ONLY the goroutine that runs next.ServeHTTP. Any
	// `go func() {...}` spawned inside a handler (background drainer,
	// admin SSE tee, future plugin fan-out) MUST defer-recover locally
	// — a panic there bypasses Recoverer and terminates the whole
	// process. The current adapter/engine/admin code does not spawn
	// such goroutines on the request hot path; this comment is the
	// guardrail for future contributors.
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
	shutdownCh := cfg.ShutdownCh
	if shutdownCh == nil {
		shutdownCh = make(chan struct{})
	}
	s := &Server{
		logger:       logger,
		version:      cfg.Version,
		commit:       cfg.Commit,
		start:        time.Now(),
		pool:         cfg.Pool,
		poolDetail:   cfg.PoolDetail,
		poolHealth:   cfg.PoolHealth,
		registry:     cfg.Registry,
		hooks:        cfg.Hooks, // Phase 8 OBSV-04
		addr:         cfg.HTTPAddr,
		shutdownCh:   shutdownCh,
		forceCloseCh: make(chan struct{}),
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
	// /health/pool — pool serving-health probe (the "is the pool
	// actually serving requests?" signal distinct from basic /health).
	// Same auth-exempt + 405-on-mutate posture as /health/hooks.
	s.router.Get("/health/pool", s.poolHandler)
	s.router.MethodFunc(http.MethodPost, "/health/pool", s.poolHandler)
	s.router.MethodFunc(http.MethodPut, "/health/pool", s.poolHandler)
	s.router.MethodFunc(http.MethodDelete, "/health/pool", s.poolHandler)
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
			// REL-HTTP-04 (H-4) / Plan 16-02: per-request body-read
			// deadline on chat-body POSTs. Path-scoped — admin POSTs
			// and catalog routes (/api/tags, /api/show, …) do NOT get
			// the wrapper (D-04a). Applied here at the per-prefix
			// sub-router level so the deadline runs AFTER IP
			// allowlist (denied requests do not arm the timer).
			r.Use(withBodyReadDeadline(cfg.BodyReadTimeout))
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
//
// REL-HTTP-01: srv.RegisterOnShutdown closes s.shutdownCh so long-lived in-process
// connections (admin SSE log tail) can exit promptly instead of blocking the full
// 30s grace period.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second, // mitigates Slowloris (gosec G112)
		// Audit server-http-no-idle-readtimeout: cap how long keep-alive
		// connections survive on the server side. Without this, laptop
		// sleep/wake cycles + flaky Wi-Fi leave half-open TCP sockets
		// that consume FDs until the kernel TCP-keepalive cleans up.
		// 120s is comfortably above typical chat-client polling and well
		// below the kernel default. WriteTimeout is intentionally OMITTED
		// — setting it would truncate legitimate long-running SSE/NDJSON
		// streams.
		IdleTimeout: 120 * time.Second,
	}

	// REL-HTTP-01: wire shutdownCh into the server so admin SSE connections
	// can unwind immediately on gateway shutdown instead of blocking for the
	// full 30s grace period. RegisterOnShutdown callbacks run before
	// Shutdown drains active connections.
	srv.RegisterOnShutdown(func() {
		select {
		case <-s.shutdownCh:
			// Already closed — idempotent.
		default:
			close(s.shutdownCh)
		}
	})

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
	// WR-08 fix (phase 15 review): run srv.Shutdown in a goroutine and
	// select on forceCloseCh so a second-signal force-exit can call
	// srv.Close() to terminate in-flight requests immediately. Without
	// this, RunUntilSignal's force-exit arm returned to main while
	// s.Run remained blocked inside srv.Shutdown for up to 30s — a
	// goroutine leak masked only by main.go's os.Exit(1).
	shutdownErrCh := make(chan error, 1)
	go func() {
		shutdownErrCh <- srv.Shutdown(shutdownCtx)
	}()
	select {
	case err := <-shutdownErrCh:
		if err != nil {
			return fmt.Errorf("server: shutdown: %w", err)
		}
		return nil
	case <-s.forceCloseCh:
		// Force-close: tear down in-flight requests immediately so the
		// goroutine that called srv.Shutdown unblocks. srv.Close returns
		// quickly; we still drain shutdownErrCh so the goroutine exits.
		s.logger.Info("force-close: closing server immediately")
		_ = srv.Close()
		<-shutdownErrCh // wait for the Shutdown goroutine to return
		return errors.New("server: force-closed")
	}
}

// RunUntilSignal starts the HTTP server and blocks until SIGINT, SIGTERM, or ctx cancellation.
// It is a thin wrapper around Run that wires OS signal handling.
// D-22: the binary stays foreground-only — no start/stop subcommands.
//
// REL-POOL-02: two-stage signal handler — first SIGINT cancels the derived context
// and initiates graceful shutdown; a second SIGINT (before graceful shutdown completes)
// triggers an immediate force-exit via a forceExitCh send so Run returns immediately,
// allowing main.go's explicit cleanup() + os.Exit(1) path to run.
func (s *Server) RunUntilSignal(ctx context.Context) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	derivedCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// forceExitCh receives a value when a second Ctrl-C arrives during graceful
	// shutdown. Run() selects on forceErrCh to short-circuit Shutdown.
	forceErrCh := make(chan error, 1)

	go func() {
		select {
		case sig := <-sigCh:
			s.logger.Info("shutdown signal received", "signal", sig.String())
			cancel() // initiate graceful shutdown
			// Wait for a second signal — if it arrives, force-exit immediately.
			select {
			case sig2 := <-sigCh:
				s.logger.Info("second shutdown signal received; forcing exit", "signal", sig2.String())
				forceErrCh <- errors.New("force-exit: second signal")
			case <-derivedCtx.Done():
				// Graceful shutdown completed normally — no force needed.
			}
		case <-derivedCtx.Done():
			// ctx cancelled upstream (e.g. by main bootCtx).
		}
	}()

	// Run the server; also observe forceErrCh so a second Ctrl-C terminates
	// the blocking srv.Shutdown call immediately.
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- s.Run(derivedCtx)
	}()

	select {
	case err := <-runErrCh:
		return err
	case err := <-forceErrCh:
		// Force-exit requested — close shutdownCh so SSE connections exit now.
		select {
		case <-s.shutdownCh:
		default:
			close(s.shutdownCh)
		}
		// WR-08 fix (phase 15 review): also close forceCloseCh so the
		// in-flight srv.Shutdown call inside s.Run unblocks via srv.Close
		// instead of leaving the Run() goroutine parked for up to 30s.
		select {
		case <-s.forceCloseCh:
		default:
			close(s.forceCloseCh)
		}
		// Wait for Run() to actually return after force-close so the
		// goroutine does not leak past RunUntilSignal's exit. We
		// deliberately do NOT bind a deadline here: srv.Close() returns
		// immediately and Run() drains shutdownErrCh in a bounded
		// fashion, so a deadline would only mask a real bug.
		<-runErrCh
		return err
	}
}

// serverDiscardWriter is a no-op io.Writer used by NewFromConfig when
// cfg.Logger is nil (WR-03 fallback). Mirrors the discardWriter
// pattern in the adapter packages but kept package-local to avoid
// adding an io import just for io.Discard.
type serverDiscardWriter struct{}

func (serverDiscardWriter) Write(p []byte) (int, error) { return len(p), nil }
