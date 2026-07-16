// Package openai implements the OpenAI-shape HTTP surface for the
// Gateway. It exposes routes that mount under the configured
// OPENAI_PATH_PREFIX (default "/v1") and forwards canonical requests
// through the engine to the warm kiro-cli pool.
//
// Architecture (TRST-04 boundary — enforced by .go-arch-lint.yml):
//   - this package declares CONSUMER-defined interfaces (Engine,
//     RunHandle, Stream, ModelCatalog) locally. It MUST NOT import
//     internal/engine — the concrete *engine.Engine structurally
//     satisfies the local Engine interface and is wired in by
//     cmd/otto-gateway/main.go.
//   - this package imports otto-gateway/internal/canonical only
//     (plus stdlib + chi + log/slog).
//
// Phase 3 Plan 01 — skeleton only. RegisterRoutes registers three
// stub handlers (501 Not Implemented). Real handler bodies land in:
//   - Plan 02: POST /chat/completions (streaming SSE)
//   - Plan 03: POST /completions and GET /models
package openai

import (
	"context"
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/session"
)

// Engine is the consumer-defined interface the adapter depends on for
// canonical request-response AND streaming orchestration. The concrete
// *engine.Engine from internal/engine structurally satisfies both
// methods. Declared HERE — never in internal/engine — so the adapter
// does not import the engine package (TRST-04 boundary).
//
// Engine may be nil when KIRO_CMD is unset; handleChatCompletions returns
// 503 in that case.
type Engine interface {
	// Collect runs a non-streaming canonical request to completion.
	// Used by the stream:false branch of handleChatCompletions and by
	// handleCompletions.
	Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
	// Run starts a streaming canonical request and returns a handle the
	// adapter ranges via RunHandle.Stream().Chunks(). Used by the
	// stream:true branch of handleChatCompletions (Plan 03-02).
	Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error)
	// RunPostHooks invokes the PostHook chain against an externally-
	// aggregated response. Quick 260530-df2: the SSE streaming branch
	// bypasses engine.Collect's chunk loop, so PostHooks never fired on
	// streaming requests. RunPostHooks closes the gap.
	// *engine.Engine.RunPostHooks structurally satisfies this.
	RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error
	// CollectFromRun performs the aggregation half of Collect against an
	// existing RunHandle (without re-running). T-5b seam: when a Pre hook
	// flipped req.Stream=false during eng.Run (e.g. PII encrypt mode),
	// the streaming branches in handleChatCompletions / handleCompletions
	// call this to drain the already-running ACP session through the
	// aggregated path and render a non-streaming JSON response, instead
	// of leaking ciphertext bytes through the SSE emitter ahead of the
	// PII decrypt PostHook.
	CollectFromRun(ctx context.Context, run RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
}

// RunHandle is the consumer-defined handle the adapter receives from
// Engine.Run. Mirrors *engine.Run's exported surface (Stream + SessionID)
// without importing the engine package.
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
	// SSE stream. Non-streaming OpenAI paths use
	// resp.StopReason == canonical.StopError on the eng.Collect-returned
	// response (handlers.go:165-168) — the dual-discriminator split is
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

// ModelCatalog is the consumer-defined interface used by handleModels to
// enumerate the kiro-cli-reported model list. The concrete *pool.Pool
// from internal/pool structurally satisfies it. Declared HERE so the
// adapter does not import internal/pool either. May be nil when
// KIRO_CMD is unset; handleModels falls back to the synthetic "auto" entry.
type ModelCatalog interface {
	Models() []canonical.ModelInfo
}

// EngineForSessionFunc is the per-request engine factory used by the
// X-Session-Id branch (Plan 05-03 Task 3). See the ollama adapter for
// the full TRST-04 rationale; the OpenAI wiring is identical.
type EngineForSessionFunc func(entry *session.Entry) Engine

// SessionRegistry is the consumer-defined interface the adapter calls
// into for the X-Session-Id branch. *session.Registry satisfies it
// structurally; tests inject fakes.
type SessionRegistry interface {
	Get(ctx context.Context, sid, cwd string) (*session.Entry, error)
}

// Config bundles the adapter's wiring dependencies. Engine and ModelCatalog
// are nil-tolerant (the degraded-mode behavior covers KIRO_CMD-unset
// deployments).
type Config struct {
	// Logger is required for structured logs from the handlers. A nil
	// Logger is replaced with a discard logger by New so handlers never
	// crash on a misconfigured caller.
	Logger *slog.Logger
	// Engine collects / runs canonical chat requests. May be nil;
	// handleChatCompletions and handleCompletions return 503 when nil.
	Engine Engine
	// ModelCatalog enumerates known models for GET /models.
	// May be nil; handleModels returns only the synthetic "auto" entry.
	ModelCatalog ModelCatalog
	// Registry is the dedicated-session registry; non-nil enables the
	// X-Session-Id branch in handleChatCompletions / handleCompletions.
	// (Plan 05-03 D-04..D-11)
	Registry SessionRegistry
	// EngineForSession is the per-request engine factory closure called
	// when the X-Session-Id branch takes the registry path. May be nil;
	// X-Session-Id requests fall through to the pool path when nil.
	EngineForSession EngineForSessionFunc
	// KiroCWD is the default working directory passed to Registry.Get
	// when the X-Session-Id branch creates a new session.
	KiroCWD string
	// StreamIdleTimeout is the duration to wait for a chunk before
	// tearing down the stream. Zero disables. Loaded from
	// STREAM_IDLE_TIMEOUT_SEC and converted to Duration in main.go
	// (quick 260531-ruv). Read by the SSE emitter to bound
	// silent-kiro hangs.
	StreamIdleTimeout time.Duration
	// ToolAliases maps kiro's native built-in tool name to a caller-offered
	// tool name (alias-primary tool-call design, 2026-07-16). Threaded from
	// cfg.ToolAliases; read by the SSE emitter to resolve native tool calls.
	ToolAliases map[string]string
}

// Adapter wires the OpenAI HTTP surface. Construct via New.
type Adapter struct {
	cfg Config
}

// New constructs an *Adapter. The nil-logger guard ensures structured
// logging is always available without panicking on a misconfigured caller.
func New(cfg Config) *Adapter {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{}))
	}
	return &Adapter{cfg: cfg}
}

// RegisterRoutes implements server.RouteRegistrar for the D-01
// SurfaceMount mechanic. It registers the three OpenAI endpoint paths
// directly onto the provided shared sub-router via r.Post/r.Get —
// never r.Mount("/", …) which would panic when Anthropic shares "/v1".
//
// All handlers are fully implemented:
//   - handleChatCompletions: stream:false (JSON) and stream:true (SSE) — Plan 02
//   - handleCompletions: legacy text completion shim (JSON-only) — Plan 03
//   - handleModels: /v1/models from ModelCatalog — Plan 03
func (a *Adapter) RegisterRoutes(r chi.Router) {
	r.Post("/chat/completions", a.handleChatCompletions)
	r.Post("/completions", a.handleCompletions)
	r.Get("/models", a.handleModels)
}

// chatBodyCap is the maximum request body size for POST /chat/completions
// and POST /completions (4 MiB, mirrors the Anthropic adapter limit).
const chatBodyCap int64 = 4 << 20 // 4 MiB

// handleCompletions is defined in handlers.go (Plan 03-03).

// discardWriter implements io.Writer with a no-op Write so the
// defensive default logger in New() does not allocate. Avoids
// io.Discard import.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
