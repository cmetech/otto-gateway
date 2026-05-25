// Package ollama implements the Ollama-shape HTTP surface for the OTTO
// Gateway. It exposes a chi sub-router that mounts under the configured
// OLLAMA_PATH_PREFIX (default "/api") and forwards canonical requests
// through the engine.
//
// Architecture (TRST-04 boundary):
//   - this package declares CONSUMER-defined interfaces Engine and
//     ModelCatalog. It MUST NOT import internal/engine — the concrete
//     *engine.Engine structurally satisfies the local Engine interface
//     and is wired in by cmd/otto-gateway/main.go.
//   - this package imports otto-gateway/internal/canonical only.
//
// Codex M-4 router split: the adapter exposes TWO accessors —
// ProtectedRouter() returns the chi sub-router with the 10 protected
// routes (chat/generate/tags/show/ps + 5 stubs) that the server mounts
// under cfg.OllamaPath behind the auth middleware chain;
// HandleVersion() returns the /api/version handler as a standalone
// http.HandlerFunc that the server registers on the OUTER router so
// /api/version remains auth-exempt (AUTH-03). The adapter does NOT
// register /version internally — avoids any dependency on chi
// inner-vs-outer route precedence.
package ollama

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
)

// Engine is the consumer-defined interface the adapter depends on for
// canonical request-response orchestration. The concrete *engine.Engine
// from internal/engine structurally satisfies it. Declared HERE — never
// in internal/engine — so the adapter does not import the engine
// package (TRST-04 boundary). Engine may be nil when KIRO_CMD is unset;
// chat/generate handlers return 503 in that case.
type Engine interface {
	Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
}

// ModelCatalog is the consumer-defined interface used by handleTags and
// handleShow to enumerate the kiro-cli-reported model list. The concrete
// *pool.Pool from internal/pool structurally satisfies it. Declared HERE
// so the adapter does not import internal/pool either. May be nil when
// KIRO_CMD is unset; handlers degrade to the synthetic "auto" entry.
type ModelCatalog interface {
	Models() []canonical.ModelInfo
}

// Config bundles the adapter's wiring dependencies. Engine and ModelCatalog
// are nil-tolerant (the degraded-mode behavior covers KIRO_CMD-unset
// deployments). Version + Commit are captured at construction so the
// /api/version handler does not re-derive them per request.
type Config struct {
	// Logger is required for structured logs from the handlers. A nil
	// Logger is replaced with a discard logger by New so handlers never
	// crash on a misconfigured caller.
	Logger *slog.Logger
	// Engine collects canonical chat requests. May be nil; chat/generate
	// handlers return 503 when nil.
	Engine Engine
	// ModelCatalog enumerates known models for /api/tags and /api/show.
	// May be nil; handlers fall back to the synthetic "auto" entry.
	ModelCatalog ModelCatalog
	// Version is the build version reported by /api/version (typically
	// version.Version).
	Version string
	// Commit is the VCS commit hash reported by /api/version (typically
	// version.Commit()).
	Commit string
}

// Adapter wires the Ollama HTTP surface. Construct via New.
type Adapter struct {
	cfg             Config
	protectedRouter chi.Router
}

// New constructs an *Adapter and prebuilds the protected chi sub-router
// with all 10 protected routes. /api/version is NOT registered here —
// HandleVersion() exposes that handler separately so server.New can
// mount it on the OUTER (auth-exempt) router (Codex M-4).
func New(cfg Config) *Adapter {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{}))
	}
	a := &Adapter{cfg: cfg}

	r := chi.NewRouter()

	// Canonical chat / generate.
	r.Post("/chat", a.handleChat)
	r.Post("/generate", a.handleGenerate)

	// Catalog endpoints.
	r.Get("/tags", a.handleTags)
	r.Post("/show", a.handleShow)
	r.Get("/ps", a.handlePS)

	// Stub endpoints (Node parity — LangFlow exercises these).
	r.Post("/pull", a.handlePull)
	r.Post("/push", a.handlePush)
	r.Post("/create", a.handleCreate)
	r.Post("/copy", a.handleCopy)
	r.Delete("/delete", a.handleDelete)

	a.protectedRouter = r
	return a
}

// ProtectedRouter returns the chi sub-router carrying the 10 protected
// Ollama routes. Kept for any legacy callers; prefer RegisterRoutes for
// the D-01 SurfaceMount mechanic.
func (a *Adapter) ProtectedRouter() chi.Router {
	return a.protectedRouter
}

// RegisterRoutes implements server.RouteRegistrar for the D-01
// SurfaceMount mechanic. It registers all 10 protected Ollama routes
// directly onto the provided shared sub-router via r.Post/r.Get/r.Delete,
// avoiding the chi double-Mount panic that would occur from
// r.Mount("/", a.protectedRouter) when another surface shares the prefix.
func (a *Adapter) RegisterRoutes(r chi.Router) {
	// Canonical chat / generate.
	r.Post("/chat", a.handleChat)
	r.Post("/generate", a.handleGenerate)

	// Catalog endpoints.
	r.Get("/tags", a.handleTags)
	r.Post("/show", a.handleShow)
	r.Get("/ps", a.handlePS)

	// Stub endpoints (Node parity — LangFlow exercises these).
	r.Post("/pull", a.handlePull)
	r.Post("/push", a.handlePush)
	r.Post("/create", a.handleCreate)
	r.Post("/copy", a.handleCopy)
	r.Delete("/delete", a.handleDelete)
}

// HandleVersion returns the /api/version handler as a standalone
// http.HandlerFunc so server.New can register it on the OUTER router
// (auth-exempt per AUTH-03). Codex M-4: the adapter does NOT register
// /version on its protected router — there is exactly one /api/version
// registration site (the outer router) and no precedence dance.
func (a *Adapter) HandleVersion() http.HandlerFunc {
	return a.handleVersion
}

// discardWriter implements io.Writer with a no-op Write so the
// defensive default logger in New() does not allocate.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
