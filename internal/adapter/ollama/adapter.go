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
	"time"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/session"
)

// Engine is the consumer-defined interface the adapter depends on for
// canonical request-response orchestration. The concrete *engine.Engine
// from internal/engine structurally satisfies it. Declared HERE — never
// in internal/engine — so the adapter does not import the engine
// package (TRST-04 boundary). Engine may be nil when KIRO_CMD is unset;
// chat/generate handlers return 503 in that case.
type Engine interface {
	Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
	Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error)
	// RunPostHooks invokes the PostHook chain against an externally-
	// aggregated response. Quick 260530-df2: the NDJSON streaming branch
	// bypasses engine.Collect's chunk loop, so PostHooks never fired on
	// streaming requests. RunPostHooks closes the gap.
	// *engine.Engine.RunPostHooks structurally satisfies this.
	RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error
	// CollectFromRun performs the aggregation half of Collect against an
	// existing RunHandle (without re-running). T-5b seam: when a Pre hook
	// flipped req.Stream=false during eng.Run (e.g. PII encrypt mode),
	// the streaming branches in handleChat / handleGenerate call this to
	// drain the already-running ACP session through the aggregated path
	// and render a non-streaming JSON response, instead of leaking
	// ciphertext bytes through the NDJSON emitter ahead of the PII
	// decrypt PostHook.
	CollectFromRun(ctx context.Context, run RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
}

// RunHandle is the consumer-defined handle the adapter receives from
// Engine.Run. *engine.Run satisfies this structurally (via main.go
// ollamaRunHandleAdapter). StopWatchdog() is called by ndjson.go
// finalizeNDJSON on normal stream completion to prevent the D-06 watchdog
// goroutine from firing a spurious session/cancel (RESEARCH.md Pattern 2
// Option A).
type RunHandle interface {
	// Stream returns the chunk delivery interface for this run.
	Stream() Stream
	// SessionID returns the kiro-cli session id this Run is bound to
	// (empty when a PreHook short-circuited and ACP was never touched).
	SessionID() string
	// StopWatchdog returns the context.AfterFunc stop function for this
	// run. Call it after normal stream completion to prevent the D-06
	// watchdog goroutine from firing a spurious session/cancel. stop()
	// returning false is expected on the disconnect path — Cancel is
	// idempotent. (CONTEXT.md D-06, RESEARCH.md Pattern 2 Option A)
	StopWatchdog() func() bool
	// ShortCircuitResponse returns the *canonical.ChatResponse a
	// PreHook supplied to short-circuit the chain, or nil when ACP was
	// engaged normally. The streaming branch in handlers.go uses this
	// (Phase 08.1 INTEG-01 fix) to convert a PreHook short-circuit into a
	// pre-header 401 + native JSON envelope instead of opening an empty
	// stream. Non-streaming Ollama paths use
	// resp.StopReason == canonical.StopError on the eng.Collect-returned
	// response (handlers.go:147-150) — the dual-discriminator split is
	// documented in 08.1-PATTERNS.md.
	ShortCircuitResponse() *canonical.ChatResponse
}

// Stream is the consumer-defined chunk-delivery interface returned by
// RunHandle.Stream(). Mirrors engine.Stream so *engine.Run.Stream()'s
// concrete return value satisfies it structurally.
type Stream interface {
	// Chunks returns the receive-only channel of canonical.Chunk values.
	// The channel closes when the stream ends (success or error).
	Chunks() <-chan canonical.Chunk
	// Result blocks until the stream is closed and returns the
	// canonical.FinalResult and any terminal error.
	Result() (*canonical.FinalResult, error)
}

// ModelCatalog is the consumer-defined interface used by handleTags and
// handleShow to enumerate the kiro-cli-reported model list. The concrete
// *pool.Pool from internal/pool structurally satisfies it. Declared HERE
// so the adapter does not import internal/pool either. May be nil when
// KIRO_CMD is unset; handlers degrade to the synthetic "auto" entry.
type ModelCatalog interface {
	Models() []canonical.ModelInfo
}

// EngineForSessionFunc is the per-request engine factory used by the
// X-Session-Id branch (Plan 05-03 Task 3). cmd/otto-gateway/main.go
// wires this to a closure that builds a fresh *engine.Engine bound to
// the supplied *session.Entry (which satisfies engine.ACPClient via
// internal/session/entry_acp.go's compile-time gate). The closure
// returns the adapter's local Engine interface so the adapter never
// imports internal/engine (TRST-04 boundary preserved).
//
// When nil (degraded mode or pre-Phase-5 wiring), the X-Session-Id
// branch falls through to the pool path — the handler ignores any
// X-Session-Id header.
type EngineForSessionFunc func(entry *session.Entry) Engine

// SessionRegistry is the consumer-defined interface the adapter calls
// into for the X-Session-Id branch. The concrete *session.Registry
// from internal/session structurally satisfies it (Get(ctx, sid, cwd)
// (*session.Entry, error)). Declaring it HERE rather than depending
// on *session.Registry directly lets unit tests inject a fake without
// standing up the full Registry + ClientFactory stack — the locked
// pattern from Plan 05-03 Task 3 (`fakeSessionRegistry` returning a
// session.NewEntryForTest-constructed Entry).
type SessionRegistry interface {
	Get(ctx context.Context, sid, cwd string) (*session.Entry, error)
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
	// Registry is the dedicated-session registry; non-nil enables the
	// X-Session-Id branch in handleChat / handleGenerate. The handler
	// reads X-Session-Id from the request and, when non-empty, calls
	// Registry.Get to obtain a *session.Entry. (Plan 05-03 D-04..D-11)
	// Typed as the narrow SessionRegistry interface so tests can inject
	// a fake; the production *session.Registry satisfies it structurally.
	Registry SessionRegistry
	// EngineForSession is the per-request engine factory closure called
	// when the X-Session-Id branch takes the registry path. See the
	// type doc. May be nil; X-Session-Id requests fall through to the
	// pool path when EngineForSession is nil OR Registry is nil.
	EngineForSession EngineForSessionFunc
	// KiroCWD is the default working directory passed to Registry.Get
	// when the X-Session-Id branch creates a new session. May be empty.
	KiroCWD string
	// StreamIdleTimeout is the duration to wait for a chunk before
	// tearing down the stream. Zero disables. Loaded from
	// STREAM_IDLE_TIMEOUT_SEC and converted to Duration in main.go
	// (quick 260531-ruv). Read by the NDJSON emitter to bound
	// silent-kiro hangs.
	StreamIdleTimeout time.Duration
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
