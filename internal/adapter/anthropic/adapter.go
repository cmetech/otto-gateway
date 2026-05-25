// Package anthropic implements the Anthropic-shape HTTP surface for the
// OTTO Gateway. It exposes a chi sub-router that mounts under the
// configured ANTHROPIC_PATH_PREFIX (default "/v1") and forwards
// canonical requests through the engine to the warm kiro-cli pool.
//
// Architecture (TRST-04 boundary — enforced by .go-arch-lint.yml):
//   - this package declares CONSUMER-defined interfaces (Engine,
//     RunHandle, Stream) locally. It MUST NOT import internal/engine —
//     the concrete *engine.Engine structurally satisfies the local
//     Engine interface and is wired in by cmd/otto-gateway/main.go.
//   - this package imports otto-gateway/internal/canonical only
//     (plus stdlib + chi + log/slog).
//
// Phase 3.1 D-18 — the protected router registers EXACTLY ONE route:
// POST /messages. count_tokens is out of scope.
package anthropic

import (
	"context"
	"log/slog"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
)

// Engine is the consumer-defined interface the adapter depends on for
// canonical request-response AND streaming orchestration. The concrete
// *engine.Engine from internal/engine structurally satisfies both
// methods. Declared HERE — never in internal/engine — so the adapter
// does not import the engine package (TRST-04 boundary).
//
// Engine may be nil when KIRO_CMD is unset; handleMessages returns 503
// in that case (D-18).
type Engine interface {
	// Collect runs a non-streaming canonical request to completion.
	// Used by the stream:false branch of handleMessages.
	Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
	// Run starts a streaming canonical request and returns a handle the
	// adapter ranges via RunHandle.Stream().Chunks(). Used by the
	// stream:true branch of handleMessages (Plan 03.1-03 wires the real
	// SSE emitter; Plan 03.1-02 ships a forward-compatible stub).
	Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error)
}

// RunHandle is the consumer-defined handle the adapter receives from
// Engine.Run. Mirrors *engine.Run's exported surface (Stream + SessionID)
// without importing the engine package. *engine.Run structurally
// satisfies this interface — but because *engine.Run.Stream returns the
// concrete engine.Stream interface (which is also structurally
// satisfied by our local Stream), Go's structural typing makes the
// wiring transparent at cmd/otto-gateway/main.go.
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

// Config bundles the adapter's wiring dependencies. Engine is
// nil-tolerant (the 503 degraded-mode behavior covers KIRO_CMD-unset
// deployments). D-18: no ModelCatalog, no Version/Commit — Anthropic
// has no /v1/models or /v1/version equivalents in Phase 3.1.
type Config struct {
	// Logger is required for structured logs from the handlers. A nil
	// Logger is replaced with a discard logger by New so handlers never
	// crash on a misconfigured caller.
	Logger *slog.Logger
	// Engine collects / runs canonical chat requests. May be nil;
	// handleMessages returns 503 when nil.
	Engine Engine
}

// Adapter wires the Anthropic HTTP surface. Construct via New.
type Adapter struct {
	cfg             Config
	protectedRouter chi.Router
}

// New constructs an *Adapter and prebuilds the protected chi sub-router
// with the single POST /messages route per D-18.
func New(cfg Config) *Adapter {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{}))
	}
	a := &Adapter{cfg: cfg}

	r := chi.NewRouter()
	r.Post("/messages", a.handleMessages)

	a.protectedRouter = r
	return a
}

// ProtectedRouter returns the chi sub-router carrying the single
// protected /messages route. Kept for any legacy callers; prefer
// RegisterRoutes for the D-01 SurfaceMount mechanic.
func (a *Adapter) ProtectedRouter() chi.Router {
	return a.protectedRouter
}

// RegisterRoutes implements server.RouteRegistrar for the D-01
// SurfaceMount mechanic. It registers POST /messages directly onto the
// provided shared sub-router via r.Post, avoiding the chi double-Mount
// panic that would occur from r.Mount("/", a.protectedRouter) when
// OpenAI shares the same "/v1" prefix.
func (a *Adapter) RegisterRoutes(r chi.Router) {
	r.Post("/messages", a.handleMessages)
}

// discardWriter implements io.Writer with a no-op Write so the
// defensive default logger in New() does not allocate. Avoids
// io.Discard import.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
