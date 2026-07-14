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
	"runtime/debug"
	"sync"
	"time"

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
	// StreamIdleTimeout is the duration to wait for a chunk before
	// tearing down the stream (quick 260531-ruv). Zero disables.
	// Threaded from cfg.StreamIdleTimeoutSec in main.go. Consumed by
	// Collect (and any future internal chunk loop) via the
	// RangeChunksWithIdleTimeout helper. Adapters get their own copy
	// in their respective Config structs.
	StreamIdleTimeout time.Duration
	// PreHooks is the Phase 8 PreHook chain. Run iterates in order;
	// the first non-nil response short-circuits ACP.
	PreHooks []PreHook
	// PostHooks is the Phase 8 PostHook chain. Collect iterates in
	// order after the response is assembled; non-nil error propagates.
	PostHooks []PostHook
	// HookErrorReporter is called by the engine after every Pre/Post
	// hook invocation. err is non-nil when the hook returned an error
	// (or panicked, recovered by callPreHookSafe / callPostHookSafe);
	// nil when the hook completed successfully. The reporter is
	// expected to record the latest error for surfacing on
	// /health/hooks. A nil reporter is a no-op so tests and callers
	// without a Chain still function. Wired in cmd/otto-gateway/main.go.
	HookErrorReporter func(hook any, err error)
	// OnModelRequest is fired once per Run with the canonical request's Model
	// (kiro usage-metrics parity attribution — gw_model_requests_total). Run
	// is the single choke point every surface passes through, so this is the
	// one place the requested model is known uniformly. A nil hook is a no-op.
	// Wired in cmd/otto-gateway/main.go to the shared recorder.
	OnModelRequest func(model string)
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

	// Kiro usage-metrics parity: attribute this request to its requested
	// model exactly once, at the single choke point every surface passes
	// through. Fired before PreHooks so short-circuited requests still count
	// as an LLM request by model (mirrors the middleware's gw_llm_requests
	// _total, which counts every chat request regardless of outcome).
	if e.cfg.OnModelRequest != nil {
		e.cfg.OnModelRequest(req.Model)
	}

	// Audit plugin-chain-run-error-leaks-starttimes-entries: track whether
	// any PreHook completed so the post-PreHook error paths (NewSession,
	// SetModel, Prompt failures) can fire PostHooks before returning. Each
	// PreHook that completed may have stored state in a per-instance
	// sync.Map keyed by request_id (LoggingHook.startTimes,
	// ChatTraceHook.startTimes). Without this cleanup every transient
	// ACP failure leaks one orphan entry per Pre+Post-stateful hook.
	preHooksCompleted := 0

	// (1) PreHook traversal (Codex H-4 short-circuit).
	for _, h := range e.cfg.PreHooks {
		resp, err := e.callPreHookSafe(ctx, h, req)
		if err != nil {
			// Best-effort cleanup for state-storing PreHooks that ran
			// before this failure. Swallow PostHook errors — the
			// returned engine error must surface to the caller.
			if preHooksCompleted > 0 {
				if pErr := e.RunPostHooks(ctx, req, nil); pErr != nil {
					e.cfg.Logger.Warn("engine: posthook cleanup after prehook error", "err", pErr)
				}
			}
			return nil, fmt.Errorf("engine: prehook: %w", err)
		}
		if resp != nil {
			// Short-circuit: carry the response on the Run handle
			// so Collect returns it verbatim. Do NOT touch ACP.
			return newCompletedRun(e, req, resp), nil
		}
		preHooksCompleted++
	}

	// runErrCleanup is invoked on any post-PreHook error path. By this
	// point every PreHook has stored its state; PostHooks must fire to
	// LoadAndDelete it. resp is intentionally nil — production PostHooks
	// (LoggingHook, ChatTraceHook) nil-guard resp access.
	runErrCleanup := func() {
		if pErr := e.RunPostHooks(ctx, req, nil); pErr != nil {
			e.cfg.Logger.Warn("engine: posthook cleanup after run error", "err", pErr)
		}
	}

	// (2) cwd derivation (D-16).
	cwd := pickCwd(req, e.cfg.DefaultCWD)

	// (3) block flattening (D-02 + D-09 footnote).
	blocks := buildBlocks(req)

	// (4) ACP session lifecycle.
	sid, err := e.cfg.ACP.NewSession(ctx, cwd)
	if err != nil {
		runErrCleanup()
		return nil, fmt.Errorf("engine: new session: %w", err)
	}
	e.cfg.Logger.Debug("engine.new_session.ok", "session_id", sid, "cwd", cwd)

	// (5) optional SetModel (D-05).
	if req.Model != "" && req.Model != "auto" {
		if err := e.cfg.ACP.SetModel(ctx, sid, req.Model); err != nil {
			e.cfg.ACP.Cancel(sid)
			runErrCleanup()
			return nil, fmt.Errorf("engine: set model: %w", err)
		}
	}

	// (6) Prompt.
	stream, err := e.cfg.ACP.Prompt(ctx, sid, blocks)
	if err != nil {
		e.cfg.ACP.Cancel(sid)
		runErrCleanup()
		return nil, fmt.Errorf("engine: prompt: %w", err)
	}
	e.cfg.Logger.Debug("engine.prompt.sent", "session_id", sid, "blocks", len(blocks))

	// D-06 in CONTEXT.md: engine-owned watchdog fires session/cancel if the
	// request ctx terminates before the stream closes naturally.
	// context.AfterFunc is Go 1.21+ (project requires 1.23). stop() returned
	// here is stored on Run so adapters/Collect can prevent the goroutine on
	// normal completion.
	stopWatchdog := context.AfterFunc(ctx, func() {
		// D-18-07 REL-HTTP-07: defense-in-depth panic recovery. The
		// context.AfterFunc callback runs on a runtime-managed goroutine;
		// an unrecovered panic propagates out of the runtime and crashes
		// the gateway. Site name "engine-after-func" is byte-exact per
		// CONTEXT.md §D-18-07. Recovers, logs once, exits cleanly — the
		// e.cfg.ACP.Cancel(sid) call below will NOT run, but the next
		// stream-result error path picks up the slot release.
		defer func() {
			if r := recover(); r != nil && e.cfg.Logger != nil {
				e.cfg.Logger.Error(
					"goroutine panic recovered",
					"site", "engine-after-func",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()),
				)
			}
		}()
		// Test-only seam: tests install via SetAfterFuncPanicProbeForTest
		// to drive the defer-recover branch. Default nil → no-op in
		// production. Goes through fireAfterFuncPanicProbe so the race
		// detector sees the happens-before relationship.
		fireAfterFuncPanicProbe()
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
		if hookErr := e.callPostHookSafe(ctx, h, req, resp); hookErr != nil {
			return fmt.Errorf("engine: posthook: %w", hookErr)
		}
	}
	return nil
}

// callPreHookSafe invokes h.Before with a defer-recover guard. A
// panicking hook (nil deref, map nil-write, unguarded type assertion)
// would otherwise unwind into the HTTP handler — net/http's per-handler
// recover keeps the process alive, but for streaming surfaces that have
// already written response headers the connection is torn down with no
// terminal `message_stop`/`[DONE]`/`done:true` frame and the client
// SDK sees a truncated stream. Treat a recovered panic as a normal
// hook error so the existing engine error-wrapping handles teardown.
//
// Audit engine-hook-panic-no-recover. The same guard is applied to
// every Pre/Post hook iteration in Run + Collect + RunPostHooks.
func (e *Engine) callPreHookSafe(ctx context.Context, h PreHook, req *canonical.ChatRequest) (resp *canonical.ChatResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			e.cfg.Logger.Error("engine.hook.panic",
				"hook", fmt.Sprintf("%T", h),
				"kind", "pre",
				"err", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()))
			resp = nil
			err = fmt.Errorf("engine: hook panic: %v", r)
		}
		if e.cfg.HookErrorReporter != nil {
			e.cfg.HookErrorReporter(h, err)
		}
	}()
	resp, err = h.Before(ctx, req)
	if err != nil {
		return resp, fmt.Errorf("engine: prehook: %w", err)
	}
	return resp, nil
}

// callPostHookSafe is the PostHook twin of callPreHookSafe.
func (e *Engine) callPostHookSafe(ctx context.Context, h PostHook, req *canonical.ChatRequest, resp *canonical.ChatResponse) (err error) {
	defer func() {
		if r := recover(); r != nil {
			e.cfg.Logger.Error("engine.hook.panic",
				"hook", fmt.Sprintf("%T", h),
				"kind", "post",
				"err", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()))
			err = fmt.Errorf("engine: hook panic: %v", r)
		}
		if e.cfg.HookErrorReporter != nil {
			e.cfg.HookErrorReporter(h, err)
		}
	}()
	if err = h.After(ctx, req, resp); err != nil {
		return fmt.Errorf("engine: posthook: %w", err)
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

// afterFuncPanicProbe is a test-only seam (D-18-07 REL-HTTP-07). The
// context.AfterFunc callback at engine.go:255 invokes it once near the
// top of its body; tests install `func() { panic(...) }` to drive the
// defer-recover branch. Default nil → no-op in production.
//
// Reads + writes go through afterFuncPanicProbeMu so the race detector
// observes the happens-before relationship between a test installing a
// probe and the AfterFunc callback reading it.
//
//nolint:gochecknoglobals // package-private test seam, leave nil in production
var (
	afterFuncPanicProbeMu sync.Mutex
	afterFuncPanicProbe   func()
)

// fireAfterFuncPanicProbe atomically reads and invokes the probe.
func fireAfterFuncPanicProbe() {
	afterFuncPanicProbeMu.Lock()
	probe := afterFuncPanicProbe
	afterFuncPanicProbeMu.Unlock()
	if probe != nil {
		probe()
	}
}

// SetAfterFuncPanicProbeForTest installs the probe and returns a
// restore function (use with t.Cleanup). Test-only.
func SetAfterFuncPanicProbeForTest(v func()) func() {
	afterFuncPanicProbeMu.Lock()
	prev := afterFuncPanicProbe
	afterFuncPanicProbe = v
	afterFuncPanicProbeMu.Unlock()
	return func() {
		afterFuncPanicProbeMu.Lock()
		afterFuncPanicProbe = prev
		afterFuncPanicProbeMu.Unlock()
	}
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
