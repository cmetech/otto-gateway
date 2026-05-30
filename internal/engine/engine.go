// Package engine is the canonical-engine orchestrator (D-01..D-05).
//
// Engine converts a *canonical.ChatRequest into:
//
//  1. PreHook traversal (Phase 8 seam) — a PreHook may short-circuit by
//     returning a non-nil response, in which case ACP is never touched.
//  2. cwd derivation (engine.pickCwd, four-step priority chain — D-16).
//  3. block flattening (engine.buildBlocks — bracketed-section transcript
//     plus zero-or-more BlockKindImage blocks).
//  4. ACP session lifecycle: NewSession → optional SetModel → Prompt.
//  5. chunk aggregation back into a *canonical.ChatResponse via
//     engine.Collect, with PostHook traversal AFTER the response is
//     assembled (Codex H-5).
//
// The engine package depends only on canonical types and the consumer-
// defined ACPClient interface declared here. The single boundary file
// that imports internal/acp is acp_adapter.go (Codex H-3 Option B).
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"otto-gateway/internal/canonical"
)

// ACPClient is the consumer-defined interface the engine depends on for
// ACP session lifecycle. Implementations: production-path uses
// NewACPClientAdapter to wrap *acp.Client (acp_adapter.go); tests provide
// fake implementations (engine_test.go); Plan 05's pool will return its
// own implementation that adds slot-release on Cancel/Result. (D-03)
type ACPClient interface {
	// NewSession creates a new kiro-cli session bound to the given cwd
	// and returns its session id.
	NewSession(ctx context.Context, cwd string) (string, error)

	// SetModel switches the agent model for an existing session.
	// Skipped by Run when req.Model is "" or "auto" (D-05).
	SetModel(ctx context.Context, sessionID, modelID string) error

	// Prompt sends the canonical blocks to the agent and returns a
	// Stream the caller ranges to receive chunks.
	Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (Stream, error)

	// Cancel best-effort cancels the session (RESEARCH.md Pitfall 6).
	// Called by Run on any error after NewSession succeeds.
	Cancel(sessionID string)
}

// Stream is the consumer-defined chunk-delivery interface returned by
// ACPClient.Prompt. Mirrors *acp.Stream but exposes Chunks as a METHOD
// (not a field) so an interface can carry it. acp_adapter.acpStreamShim
// adapts *acp.Stream to this interface; tests provide fake streams.
type Stream interface {
	// Chunks returns the receive-only channel of canonical.Chunk values.
	// The channel closes when the stream ends (success or error).
	Chunks() <-chan canonical.Chunk

	// Result blocks until the stream is closed and returns the
	// canonical.FinalResult and any terminal error.
	Result() (*canonical.FinalResult, error)
}

// Config bundles all engine dependencies. New(cfg) validates that the
// required fields (Logger, ACP) are non-nil — DefaultCWD may be empty
// (pickCwd falls back to os.Getwd in that case). PreHooks and PostHooks
// are the Phase 8 seam; in Phase 2 they are nil/empty by default.
type Config struct {
	// Logger is required.
	Logger *slog.Logger
	// ACP is required; the engine routes session lifecycle through it.
	ACP ACPClient
	// DefaultCWD is the third-priority cwd (after WorkingDirOverride and
	// longestCommonParent of file:// resource links). Empty falls
	// through to os.Getwd in pickCwd (D-16).
	DefaultCWD string
	// PreHooks is the Phase 8 PreHook chain. Run iterates in order;
	// the first non-nil response short-circuits ACP.
	PreHooks []PreHook
	// PostHooks is the Phase 8 PostHook chain. Collect iterates in
	// order after the response is assembled; non-nil error propagates.
	PostHooks []PostHook
}

// Engine is the concrete orchestrator. Construct via New.
type Engine struct {
	cfg Config
}

// New constructs an *Engine from cfg. Logger and ACP must be non-nil.
func New(cfg Config) *Engine {
	if cfg.Logger == nil {
		// Defensive — never crash on a misconfigured caller; use
		// the discard handler so tests and callers without a logger
		// still function.
		cfg.Logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{}))
	}
	return &Engine{cfg: cfg}
}

// Run handle is returned by Engine.Run. It carries the live stream for
// adapter ranging, and (when a PreHook short-circuits) the pre-built
// response so Collect can preserve the hook's body verbatim (Codex H-4).
type Run struct {
	engine    *Engine
	sessionID string
	stream    Stream
	req       *canonical.ChatRequest
	// response is non-nil ONLY when a PreHook short-circuited Run by
	// returning (resp, nil) with resp != nil. Collect detects this and
	// returns *response directly, WITHOUT ranging stream.Chunks() and
	// WITHOUT calling stream.Result(). The previous design
	// (newCompletedRun + chunk assembly from empty text) silently
	// dropped the hook's payload — Codex H-4 fix.
	response *canonical.ChatResponse
	// stopWatchdog is the context.AfterFunc stop function. Nil when a
	// PreHook short-circuited Run (no ACP session was opened, no watchdog
	// was registered). Adapters and Collect call StopWatchdog() to retrieve
	// it and invoke it after natural stream completion (RESEARCH.md Pattern
	// 2 Option A; CONTEXT.md D-06).
	stopWatchdog func() bool
}

// Stream exposes the underlying chunk stream so adapters can range
// directly when they need byte-for-byte chunk forwarding.
func (r *Run) Stream() Stream { return r.stream }

// SessionID returns the kiro-cli session id this Run is bound to.
// Empty string when a PreHook short-circuited and ACP was never touched.
func (r *Run) SessionID() string { return r.sessionID }

// ShortCircuitResponse returns the *canonical.ChatResponse a PreHook
// supplied to short-circuit the chain (Codex H-4), or nil when the
// Run was NOT short-circuited (i.e., ACP was engaged normally). This
// is the supported way for non-Collect callers (e.g., adapter-local
// aggregators like CollectAnthropicChat) to recover the PreHook's
// verbatim response body without ranging the empty chunk stream.
// Phase 8 SC1 — Anthropic surface short-circuit rendering.
func (r *Run) ShortCircuitResponse() *canonical.ChatResponse { return r.response }

// StopWatchdog returns the context.AfterFunc stop function registered in
// Run(). Callers (Collect, NDJSON/SSE emitters) invoke the returned
// function after natural stream completion to prevent the watchdog goroutine
// from firing a spurious session/cancel. stop() returning false means the
// context was already canceled — that is expected and safe because Cancel is
// idempotent. Returns nil when a PreHook short-circuited Run (no ACP
// session, no watchdog was registered).
func (r *Run) StopWatchdog() func() bool { return r.stopWatchdog }

// Run orchestrates a request from canonical → ACP. On any error AFTER
// NewSession succeeds, ACPClient.Cancel(sid) is called best-effort
// before returning (D-05; RESEARCH.md Pitfall 6).
func (e *Engine) Run(ctx context.Context, req *canonical.ChatRequest) (*Run, error) {
	if req == nil {
		return nil, fmt.Errorf("engine: run: req is nil")
	}

	// (1) PreHook traversal (Codex H-4 short-circuit).
	for _, h := range e.cfg.PreHooks {
		resp, err := h.Before(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("engine: prehook: %w", err)
		}
		if resp != nil {
			// Short-circuit: carry the response on the Run handle
			// so Collect returns it verbatim. Do NOT touch ACP.
			return newCompletedRun(e, req, resp), nil
		}
	}

	// (2) cwd derivation (D-16).
	cwd := pickCwd(req, e.cfg.DefaultCWD)

	// (3) block flattening (D-02 + D-09 footnote).
	blocks := buildBlocks(req)

	// (4) ACP session lifecycle.
	sid, err := e.cfg.ACP.NewSession(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("engine: new session: %w", err)
	}

	// (5) optional SetModel (D-05).
	if req.Model != "" && req.Model != "auto" {
		if err := e.cfg.ACP.SetModel(ctx, sid, req.Model); err != nil {
			e.cfg.ACP.Cancel(sid)
			return nil, fmt.Errorf("engine: set model: %w", err)
		}
	}

	// (6) Prompt.
	stream, err := e.cfg.ACP.Prompt(ctx, sid, blocks)
	if err != nil {
		e.cfg.ACP.Cancel(sid)
		return nil, fmt.Errorf("engine: prompt: %w", err)
	}

	// D-06 in CONTEXT.md: engine-owned watchdog fires session/cancel if the
	// request ctx terminates before the stream closes naturally.
	// context.AfterFunc is Go 1.21+ (project requires 1.23). stop() returned
	// here is stored on Run so adapters/Collect can prevent the goroutine on
	// normal completion.
	stopWatchdog := context.AfterFunc(ctx, func() {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			e.cfg.Logger.Debug("engine: watchdog: client disconnect — canceling session", "session_id", sid)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			e.cfg.Logger.Debug("engine: watchdog: request timeout — canceling session", "session_id", sid)
		default:
			e.cfg.Logger.Debug("engine: watchdog: context done — canceling session", "session_id", sid, "err", ctx.Err())
		}
		e.cfg.ACP.Cancel(sid)
	})

	return &Run{
		engine:       e,
		sessionID:    sid,
		stream:       stream,
		req:          req,
		stopWatchdog: stopWatchdog,
	}, nil
}

// RunPostHooks invokes the PostHook chain against an externally-aggregated
// response. Used by streaming adapters that bypass Collect's chunk loop
// (they range Stream().Chunks() themselves while emitting wire frames)
// and by the Anthropic adapter's CollectAnthropicChat (which has its own
// aggregator per D-07). Iterates in registration order; first non-nil
// error aborts with the same "engine: posthook: ..." wrapping Collect
// uses.
//
// Idempotency contract: callers MUST NOT call this method when also
// calling Collect — Collect already runs PostHooks. The two are
// alternatives. The streaming adapters call Run + RunPostHooks; the
// non-streaming Ollama/OpenAI adapters call Collect; the non-streaming
// Anthropic adapter calls CollectAnthropicChat which calls RunPostHooks
// at its tail. There is no path that invokes both — verified by the
// per-surface double-fire guard tests in 260530-df2 Task 5.
//
// Nil-resp defensive contract: streaming adapters may invoke this on a
// partial/empty aggregation (or nil) if the stream produced zero chunks
// and the adapter elected not to build a synthetic response. Production
// PostHooks (LoggingHook, ChatTraceHook) already nil-guard their resp
// access; this method passes the nil through without panicking.
func (e *Engine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	for _, h := range e.cfg.PostHooks {
		if hookErr := h.After(ctx, req, resp); hookErr != nil {
			return fmt.Errorf("engine: posthook: %w", hookErr)
		}
	}
	return nil
}

// newCompletedRun builds a *Run that carries a PreHook-supplied response
// without engaging ACP. Collect (collect.go) detects r.response != nil
// and returns *response directly. The stream is an empty/closed shim
// so that callers who DO call Run.Stream() (defensive) still get a
// well-formed Stream that returns immediately. (Codex H-4)
func newCompletedRun(e *Engine, req *canonical.ChatRequest, resp *canonical.ChatResponse) *Run {
	return &Run{
		engine:    e,
		sessionID: "",
		stream:    &emptyStream{resp: resp},
		req:       req,
		response:  resp,
	}
}

// emptyStream is the closed/empty stream returned by newCompletedRun.
// Chunks() returns an already-closed channel (no values delivered);
// Result() returns a canonical.FinalResult with StopReason copied from
// the synthesized response (so even defensive Result-calling callers
// see a consistent stop reason). (Codex H-4)
type emptyStream struct {
	resp *canonical.ChatResponse
}

// closedChunkChan is a package-level already-closed receive-only channel
// reused by every emptyStream. Allocating in Chunks() per call would be
// wasteful and racy under high short-circuit rates.
var closedChunkChan = func() <-chan canonical.Chunk {
	ch := make(chan canonical.Chunk)
	close(ch)
	return ch
}()

// Chunks returns an already-closed channel — ranging over it terminates
// immediately with zero iterations.
func (e *emptyStream) Chunks() <-chan canonical.Chunk { return closedChunkChan }

// Result returns a canonical.FinalResult populated from the carried
// response's StopReason. SessionID is empty (no ACP session was used)
// and ChunkCount is zero.
func (e *emptyStream) Result() (*canonical.FinalResult, error) {
	stop := canonical.StopUnknown
	if e.resp != nil {
		stop = e.resp.StopReason
	}
	return &canonical.FinalResult{
		SessionID:  "",
		ChunkCount: 0,
		StopReason: stop,
	}, nil
}

// discardWriter implements io.Writer with a no-op Write so the defensive
// default logger in New() does not allocate. Avoids io.Discard import.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
