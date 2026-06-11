package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin"
)

// ----------------------------------------------------------------------------
// SSE chunk payload types — OpenAI chat.completion.chunk shape
//
// Field order is LOAD-BEARING: encoding/json walks struct fields in
// declaration order. Golden-fixture tests (sse_golden_test.go) compare
// byte-exact output against canonical OpenAI wire bytes. Any reordering
// here will break those tests; reorder the golden file too if you
// intentionally change a payload shape.
//
// The OpenAI SSE emitter is STRUCTURALLY SIMPLER than Anthropic's:
//   - NO event: lines (data:-only framing)
//   - NO per-block content_block_start/stop state machine
//   - NO ping ticker (OpenAI SDK does not require keepalive)
//   - Single select-loop with exactly two cases: ctx.Done + chunks
//
// Phase 6 (REVIEW HIGH #1 + iteration-3 sawKiroNativeToolCall): the
// emitter state — `textBuffer`, `buffering`, `deferredTextFrames`,
// `coerceHit`, `sawKiroNativeToolCall` — is likewise touched ONLY inside
// the select-loop goroutine. The streaming coerce path runs only when
// buffering accumulated text deltas AND sawKiroNativeToolCall is false at
// stream end (kiro-native fired → SKIP coerce + flush buffered text as
// plain text-delta frames; iteration-3 fix to HIGH #2).
// ----------------------------------------------------------------------------

// chatCompletionChunk is the per-chunk envelope emitted as "data: <json>\n\n"
// per RESEARCH.md §Pattern 2. id and created are FIXED for the lifetime of
// one response (Pitfall 8 — computed once in sseEmitter, reused every frame).
type chatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`  // always "chat.completion.chunk"
	Created int64         `json:"created"` // unix seconds, fixed per response
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
}

// chunkChoice is one entry of choices[]. Index is always 0 (n>1 unsupported).
// FinishReason is *string so it can be null on non-final frames; the final
// pre-[DONE] frame carries a non-null mapped string.
type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"` // null until final frame
}

// chunkDelta carries the incremental content for one chunk. Role is present
// only on the FIRST frame ("assistant"); Content is present on subsequent
// text frames; both are omitempty so they are absent on the final
// finish_reason frame (which has an empty delta={}).
//
// Phase 6: ToolCalls is populated ONLY by the streaming coerce path at
// end-of-stream (per HIGH #2 two-path rule — kiro-native ChunkKindToolCall
// renders as a text-delta narration line, NOT a native delta.tool_calls
// frame). The multi-frame coerce-synthesized emission uses frame B
// (id+name, empty arguments) and frame C (arguments JSON-string).
type chunkDelta struct {
	Role      string               `json:"role,omitempty"`    // "assistant" on first chunk only
	Content   string               `json:"content,omitempty"` // text fragment on text chunks
	ToolCalls []chunkDeltaToolCall `json:"tool_calls,omitempty"`
}

// chunkDeltaToolCall is one entry in the streaming delta.tool_calls slice
// (Phase 6 D-07). Used ONLY by the coerce-synthesized end-of-stream
// emission — kiro-native chunks render as text-delta narration and do NOT
// emit a delta.tool_calls frame.
type chunkDeltaToolCall struct {
	Index    int                        `json:"index"`
	ID       string                     `json:"id,omitempty"`
	Type     string                     `json:"type,omitempty"`
	Function chunkDeltaToolCallFunction `json:"function"`
}

// chunkDeltaToolCallFunction holds the function-call envelope for the
// streaming wire shape. Arguments is a JSON-encoded STRING (NOT
// map[string]any) — same wire-shape divergence canary as the
// non-streaming render path (Phase 6 D-07).
type chunkDeltaToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// ----------------------------------------------------------------------------
// sseEmitter — per-request streaming state machine (OpenAI flat variant)
//
// D-05: w + flusher are touched ONLY by writeData (which is called only
// from the select-loop goroutine). No mutex needed — single-goroutine
// invariant is enforced by construction.
//
// Phase 6 streaming-coerce state (REVIEW HIGH #1 + iteration-3):
//   - textBuffer: accumulates text deltas when buffering=true.
//   - buffering: true once we see JSON-shaped text deltas AND req.Tools
//     is non-empty. Once set, all subsequent text-deltas are buffered
//     (not flushed) until end-of-stream.
//   - deferredTextFrames: the would-be plain text-delta SSE frames,
//     stored so we can release them in order if coerce misses OR if
//     sawKiroNativeToolCall is true at stream end.
//   - sawKiroNativeToolCall: set true on the first ChunkKindToolCall.
//     At stream end this is the SKIP-COERCE flag (iteration-3 HIGH #2
//     fix — prevents the iteration-2 double-fire regression where a
//     kiro-native tool_call followed by JSON-shaped narration text
//     could trigger a spurious coerce-synthesized tool_call).
// ----------------------------------------------------------------------------

type sseEmitter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	logger   *slog.Logger
	id       string // fixed per response (Pitfall 8)
	created  int64  // fixed per response (Pitfall 8)
	model    string
	roleSent bool // tracks whether the role:assistant delta was already emitted

	// Phase 6 streaming-coerce state (single-goroutine invariant).
	req                   *canonical.ChatRequest
	textBuffer            strings.Builder
	buffering             bool
	deferredTextFrames    [][]byte
	sawKiroNativeToolCall bool
	// WR-01 (Phase 6 review): textFlushed locks the Pitfall 3 "entire
	// text" invariant in the streaming path. Once any non-buffered text
	// has been written to the wire, we MUST NOT start buffering for
	// coerce — coerce requires the JSON to be the entire response, and
	// a split stream (prose first, JSON second) violates that.
	textFlushed bool

	// Quick 260530-df2 — post-stream aggregator. Captures EVERY text
	// fragment written or buffered so the canonical response handed to
	// RunPostHooks after stream completion has populated Content.
	// Without this, LoggingHook.After + ChatTraceHook.After observe an
	// empty resp shell and the post_chain_out NDJSON record loses its
	// product value. The coerce-hit path uses the synthetic resp
	// (already carries Message.ToolCalls) instead of this aggregator.
	//
	// D-05 single-goroutine invariant applies.
	aggregatedText strings.Builder
}

// writeData marshals payload to JSON and writes it as "data: <json>\n\n" +
// Flush. This is the ONLY method that touches e.w / e.flusher (D-05
// single-goroutine invariant). Errors are wrapped with context.
func (e *sseEmitter) writeData(payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("openai: marshal chunk: %w", err)
	}
	if _, err := fmt.Fprintf(e.w, "data: %s\n\n", body); err != nil {
		return fmt.Errorf("openai: write chunk: %w", err)
	}
	e.flusher.Flush()
	return nil
}

// writeRaw writes a pre-marshaled SSE frame (already formatted as
// "data: <json>\n\n") and flushes. Used to release buffered text frames
// in deferred-flush paths.
func (e *sseEmitter) writeRaw(frame []byte) error {
	if _, err := e.w.Write(frame); err != nil {
		return fmt.Errorf("openai: write deferred frame: %w", err)
	}
	e.flusher.Flush()
	return nil
}

// buildChunk constructs the chatCompletionChunk envelope with fixed id and
// created, wrapping the supplied chunkChoice.
func (e *sseEmitter) buildChunk(choice chunkChoice) chatCompletionChunk {
	return chatCompletionChunk{
		ID:      e.id,
		Object:  "chat.completion.chunk",
		Created: e.created,
		Model:   e.model,
		Choices: []chunkChoice{choice},
	}
}

// marshalChunk marshals a chunk into the full "data: <json>\n\n" SSE frame
// byte sequence WITHOUT writing it. Used to build deferred text frames
// for the streaming-coerce buffering path.
func (e *sseEmitter) marshalChunk(choice chunkChoice) ([]byte, error) {
	body, err := json.Marshal(e.buildChunk(choice))
	if err != nil {
		return nil, fmt.Errorf("openai: marshal deferred chunk: %w", err)
	}
	out := make([]byte, 0, len(body)+8)
	out = append(out, []byte("data: ")...)
	out = append(out, body...)
	out = append(out, []byte("\n\n")...)
	return out, nil
}

// ensureRoleSent emits the role:assistant delta if it has not yet been
// emitted. Returns any write error from the flush.
func (e *sseEmitter) ensureRoleSent() error {
	if e.roleSent {
		return nil
	}
	if err := e.writeData(e.buildChunk(chunkChoice{
		Index:        0,
		Delta:        chunkDelta{Role: "assistant"},
		FinishReason: nil,
	})); err != nil {
		return err
	}
	e.roleSent = true
	return nil
}

// looksLikeJSONStart returns true if s (after TrimSpace) starts with `{`
// or a triple-backtick fence — the heuristic for "this text might parse
// as a tool_call argument JSON". Mirrors the heuristic used by the
// ollama adapter's streaming-coerce buffering decision.
func looksLikeJSONStart(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "```")
}

// applyChunk processes one canonical.Chunk and emits the appropriate
// data: frame.
//
// Phase 6 dispatch (REVIEW HIGH #2 + iteration-3 HIGH #2 fix):
//   - ChunkKindText: if streaming-coerce buffering applies (req.Tools
//     non-empty AND text looks JSON-shaped), build the would-be text
//     frame and STASH it on e.deferredTextFrames instead of flushing.
//     Otherwise, flush per Phase 4.
//   - ChunkKindToolCall: render as text-delta narration "[tool: <name>]\n"
//     (the OpenAI equivalent of Ollama's narration line). Set
//     sawKiroNativeToolCall = true so end-of-stream coerce is skipped.
//     NO native delta.tool_calls emitted from this path.
//   - Other kinds (ChunkKindThought, ChunkKindPlan): drop silently.
func (e *sseEmitter) applyChunk(c canonical.Chunk) error {
	switch c.Kind {
	case canonical.ChunkKindText:
		return e.applyTextChunk(c)
	case canonical.ChunkKindToolCall:
		return e.applyToolCallChunk(c)
	default:
		e.logger.Debug("openai: sse unsupported chunk kind dropped", "kind", c.Kind)
		return nil
	}
}

// applyTextChunk handles a ChunkKindText. Implements the streaming-coerce
// buffering decision: if req.Tools is non-empty AND the accumulated text
// (existing buffer + this fragment) starts with `{` or a triple-backtick
// fence, the would-be SSE frame is appended to deferredTextFrames instead
// of being flushed. Once buffering is true, ALL subsequent text fragments
// (regardless of shape) are buffered too — they belong to the same
// candidate-JSON sequence.
func (e *sseEmitter) applyTextChunk(c canonical.Chunk) error {
	if c.Text == nil {
		return nil
	}
	if err := e.ensureRoleSent(); err != nil {
		return err
	}
	frag := c.Text.Content

	// Decide whether to buffer. Once buffering has started, keep buffering.
	// Otherwise, start buffering when req.Tools is non-empty AND the
	// accumulated text starts JSON-shaped — BUT only if no non-buffered
	// text has already been flushed (WR-01: split-stream coerce is unsafe
	// per Pitfall 3 "entire text").
	if !e.buffering && e.req != nil && len(e.req.Tools) > 0 && !e.textFlushed {
		probe := e.textBuffer.String() + frag
		if looksLikeJSONStart(probe) {
			e.buffering = true
		}
	}

	if e.buffering {
		e.textBuffer.WriteString(frag)
		frame, err := e.marshalChunk(chunkChoice{
			Index:        0,
			Delta:        chunkDelta{Content: frag},
			FinishReason: nil,
		})
		if err != nil {
			return err
		}
		e.deferredTextFrames = append(e.deferredTextFrames, frame)
		// Quick 260530-df2 — even buffered text aggregates so the
		// post-stream PostHook sees populated Content on the
		// coerce-miss flush path. On coerce-hit, finalize uses the
		// synthetic resp instead and this aggregation is moot.
		e.aggregatedText.WriteString(frag)
		return nil
	}

	// WR-01: record that non-buffered text reached the wire so any
	// future JSON-shaped chunk in this stream cannot retroactively
	// start buffering.
	e.textFlushed = true
	if err := e.writeData(e.buildChunk(chunkChoice{
		Index:        0,
		Delta:        chunkDelta{Content: frag},
		FinishReason: nil,
	})); err != nil {
		return err
	}
	// Quick 260530-df2 — aggregate flushed text for the post-stream
	// PostHook canonical response.
	e.aggregatedText.WriteString(frag)
	return nil
}

// applyToolCallChunk handles a ChunkKindToolCall. Phase 6 REVIEW HIGH #2
// + iteration-3 HIGH #2 fix: emits a single text-delta frame with
// `[tool: <name>]\n` content (the OpenAI equivalent of Ollama's narration
// line) and sets sawKiroNativeToolCall = true so end-of-stream coerce is
// skipped. No delta.tool_calls frame is emitted from this path.
func (e *sseEmitter) applyToolCallChunk(c canonical.Chunk) error {
	if c.ToolCall == nil {
		return nil
	}
	if err := e.ensureRoleSent(); err != nil {
		return err
	}
	name := c.ToolCall.Name
	if name == "" {
		name = "unknown"
	}
	narration := fmt.Sprintf("[tool: %s]\n", name)
	e.sawKiroNativeToolCall = true
	if err := e.writeData(e.buildChunk(chunkChoice{
		Index:        0,
		Delta:        chunkDelta{Content: narration},
		FinishReason: nil,
	})); err != nil {
		return err
	}
	// Quick 260530-df2 — aggregate the narration text into Content so
	// the post-stream PostHook canonical response carries the
	// kiro-native tool_call visibility (mirrors ollama's narration-
	// aggregate path).
	e.aggregatedText.WriteString(narration)
	return nil
}

// runSSEEmitter is the entry point for the SSE streaming branch of
// handleChatCompletions. It:
//  1. Asserts http.Flusher BEFORE writing any bytes (so the caller can
//     still emit a JSON 500 if it's absent — Pitfall 2).
//  2. Sets Content-Type: text/event-stream, Cache-Control: no-cache,
//     Connection: keep-alive BEFORE WriteHeader(200) (Pitfall 2 order).
//  3. Emits the flat OpenAI chunk sequence:
//     a. First chunk: delta={"role":"assistant"}, finish_reason=null
//     b. Per canonical.ChunkKindText: delta={"content":"…"}, finish_reason=null
//     — OR — buffered for streaming-coerce when req.Tools non-empty AND
//     text looks JSON-shaped.
//     c. Per canonical.ChunkKindToolCall: text-delta "[tool: <name>]\n"
//     narration AND sawKiroNativeToolCall = true (REVIEW HIGH #2 +
//     iteration-3 HIGH #2 — skips end-of-stream coerce).
//     d. Final chunk: see finalizeSSE — coerce/skip/flush triage.
//  4. On ctx.Done: debug-logs the disconnect and returns ctx.Err() without [DONE].
//  5. On Result() error after channel close: logs at debug + returns the error
//     WITHOUT emitting [DONE] (truncated stream is acceptable per A5; OpenAI
//     has no error-frame contract — Pitfall 3).
//
// Returns the aggregated canonical response (non-nil even on
// disconnect / mid-stream Result() error so PostHooks observe
// forensics — quick 260530-df2) alongside the error. Error is nil on
// clean completion, ctx.Err() on disconnect, a wrapped emitter error
// otherwise. The Flusher-assertion failure returns (nil, err) BEFORE
// any aggregation.
//
// Phase 6 (REVIEW HIGH #1 + iteration-3 sawKiroNativeToolCall): the
// emitter accepts `req *canonical.ChatRequest` so end-of-stream coerce
// can read req.Tools and call engine.CoerceToolCall on the buffered
// assistant text. Streaming coerce fires ONLY when sawKiroNativeToolCall
// is false at stream close — kiro-native ChunkKindToolCall renders as
// text-delta narration (per HIGH #2 two-path rule) and trips the
// sawKiroNativeToolCall flag so end-of-stream coerce is skipped.
func runSSEEmitter(ctx context.Context, w http.ResponseWriter, run RunHandle, req *canonical.ChatRequest, model string, streamIdle time.Duration, logger *slog.Logger) (*canonical.ChatResponse, error) {
	// Assert Flusher BEFORE any write so the caller can fall back to JSON 500
	// if the ResponseWriter does not support streaming (Pitfall 2 + anthropic analog).
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("openai: response writer is not flusher")
	}

	// Set streaming headers BEFORE WriteHeader(200) — order matters (Pitfall 2).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	e := &sseEmitter{
		w:       w,
		flusher: flusher,
		logger:  logger,
		id:      genMessageID("chatcmpl-"),
		created: time.Now().Unix(), // fixed once (Pitfall 8)
		model:   model,
		req:     req,
	}

	chunks := run.Stream().Chunks()

	// Quick 260531-ruv — idle watchdog arm. Nil idleC is a never-ready
	// channel (disabled). Drain-safe Stop/Reset mirrors
	// engine.RangeChunksWithIdleTimeout.
	var idleTimer *time.Timer
	var idleC <-chan time.Time
	if streamIdle > 0 {
		idleTimer = time.NewTimer(streamIdle)
		defer func() {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
		}()
		idleC = idleTimer.C
	}

	for {
		select {
		case <-ctx.Done():
			// Client disconnected. Debug-log with session context, return ctx error.
			// Do NOT emit [DONE] — the stream tore down before natural end.
			// Quick 260530-df2: return the partial aggregated response so
			// handlers fire PostHooks for forensics + duration_ms.
			// Audit openai-watchdog-stop-leaked-on-error-paths: stop the
			// AfterFunc so it doesn't fire a spurious session/cancel
			// AFTER our explicit error path (Cancel is idempotent at the
			// engine layer; suppressing the redundant audit-event entry
			// is the actual win).
			if stop := run.StopWatchdog(); stop != nil {
				stop()
			}
			e.logger.Debug("openai: sse client disconnect", "session_id", run.SessionID())
			return e.aggregatedResponse(canonical.StopUnknown, nil), fmt.Errorf("openai: sse ctx: %w", ctx.Err())

		case <-idleC:
			// Quick 260531-ruv — stream-idle fire. Emit an OpenAI SSE
			// data-frame error envelope followed by [DONE], WARN-log
			// the canonical marker, and return wrapped
			// canonical.ErrStreamIdleTimeout for handler errors.Is
			// detection. Frame shape matches errorInner in errors.go.
			//
			// H-2 fix (REL-HTTP-02): do NOT call StopWatchdog here.
			// The watchdog's AfterFunc carries the ACP Cancel mechanism.
			// Calling StopWatchdog() suppresses Cancel — leaving the
			// kiro-cli worker generating into a freed slot. Let the
			// deferred cancelFn (set by the handler on handler return)
			// trigger the watchdog AfterFunc naturally so Cancel fires.
			// Mirrors the Ollama NDJSON idle-timeout path which already
			// omits StopWatchdog() on the idleC arm.
			e.logger.Warn(
				"stream.idle_timeout",
				"surface", "openai",
				"session_id", run.SessionID(),
				"elapsed_ms", streamIdle.Milliseconds(),
				"request_id", plugin.RequestIDFromContext(ctx),
			)
			_, _ = fmt.Fprintf(w, "data: {\"error\":{\"message\":\"stream idle timeout\",\"type\":\"api_error\"}}\n\n")
			_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
			e.flusher.Flush()
			return e.aggregatedResponse(canonical.StopUnknown, nil),
				fmt.Errorf("openai: sse %w", canonical.ErrStreamIdleTimeout)

		case c, ok := <-chunks:
			if !ok {
				// Channel closed — stream ended; emit final frames.
				return finalizeSSE(e, run)
			}
			if err := e.applyChunk(c); err != nil {
				// H-2 fix (REL-HTTP-02): do NOT call StopWatchdog on
				// write-error path. Same rationale as the idleC arm: let
				// the deferred cancelFn trigger the watchdog AfterFunc so
				// ACP Cancel fires naturally rather than being suppressed.
				return e.aggregatedResponse(canonical.StopUnknown, nil), err
			}
			if idleTimer != nil {
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(streamIdle)
			}
		}
	}
}

// aggregatedResponse builds a *canonical.ChatResponse from the post-
// stream aggregator state. Quick 260530-df2: mirrors
// assembleChatResponse (engine/collect.go) — text part always at
// Content[0]. When syntheticToolCalls is non-nil (coerce hit path),
// the synthesized ToolCalls slice is attached to Message.ToolCalls so
// PostHooks observe the same final canonical shape the wire produced.
func (e *sseEmitter) aggregatedResponse(stop canonical.StopReason, syntheticToolCalls []canonical.ToolCall) *canonical.ChatResponse {
	model := ""
	if e.req != nil {
		model = e.req.Model
	}
	content := []canonical.ContentPart{
		{Kind: canonical.ContentKindText, Text: e.aggregatedText.String()},
	}
	return &canonical.ChatResponse{
		Model: model,
		Message: canonical.Message{
			Role:      canonical.RoleAssistant,
			Content:   content,
			ToolCalls: syntheticToolCalls,
		},
		StopReason: stop,
	}
}

// finalizeSSE emits the closing frames after the chunk channel closes.
// Extracted so the chunks-closed branch in runSSEEmitter stays scannable.
//
// Phase 6 iteration-3 skip-or-coerce-or-flush triage:
//   - If sawKiroNativeToolCall == true (iteration-3 HIGH #2 fix): SKIP
//     coerce. RELEASE any deferredTextFrames in order. Emit terminal
//     finish_reason from canonical StopReason (NOT "tool_calls").
//   - Else if buffering == false: emit terminal finish_reason normally.
//     (No buffered text → no coerce candidate.)
//   - Else (buffering == true AND sawKiroNativeToolCall == false): build
//     synthetic *canonical.ChatResponse from textBuffer, call
//     engine.CoerceToolCall. On HIT: discard deferredTextFrames, emit
//     the multi-frame native delta.tool_calls sequence + terminal
//     finish_reason:"tool_calls". On MISS: release deferredTextFrames
//     in order, emit terminal finish_reason from canonical StopReason.
//
// On run.Stream().Result() error: log at debug, stop WITHOUT emitting
// finish_reason or [DONE] (truncated stream — Pitfall 3 / A5).
func finalizeSSE(e *sseEmitter, run RunHandle) (*canonical.ChatResponse, error) {
	final, rerr := run.Stream().Result()
	if rerr != nil {
		// H-3 fix (REL-HTTP-03): mid-stream worker death — emit surface-native
		// terminal error frame so the OpenAI client gets an explicit signal
		// instead of a silent truncated stream. WARN log with all D-09/D-10
		// required fields for operator visibility.
		//
		// Audit openai-watchdog-stop-leaked-on-error-paths: still stop the
		// watchdog (Cancel already had its chance; this just suppresses the
		// redundant audit-event when ctx subsequently cancels).
		if stop := run.StopWatchdog(); stop != nil {
			stop()
		}

		// D-09/D-10 WARN log: session_id, worker_pid, kiro_exit_code, bytes_streamed.
		// worker_pid: RunHandle interface does not expose a PID accessor; log 0
		// as a placeholder until the interface is extended.
		// kiro_exit_code: extract via errors.As if rerr chain contains *exec.ExitError.
		// bytes_streamed: sseEmitter does not yet wire a bytes counter; log 0.
		logArgs := []any{
			"session_id", run.SessionID(),
			"worker_pid", 0, // worker_pid: counter not yet wired in RunHandle interface
			"bytes_streamed", 0, // bytes_streamed: counter not yet wired in sseEmitter
			"err", rerr,
		}
		var exitErr *exec.ExitError
		if errors.As(rerr, &exitErr) {
			logArgs = append(logArgs, "kiro_exit_code", exitErr.ExitCode())
		}
		// kiro_exit_code omitted when rerr chain has no *exec.ExitError
		e.logger.Warn("openai: sse worker terminated mid-stream", logArgs...)

		// Emit D-09 OpenAI surface-native terminal error frame + [DONE].
		// Frame shape: error chunk with upstream_disconnect code followed by [DONE].
		_, _ = fmt.Fprintf(e.w, "data: {\"error\":{\"type\":\"server_error\",\"code\":\"upstream_disconnect\",\"message\":\"worker terminated mid-stream\"}}\n\n")
		_, _ = fmt.Fprintf(e.w, "data: [DONE]\n\n")
		e.flusher.Flush()

		return e.aggregatedResponse(canonical.StopUnknown, nil), fmt.Errorf("openai: sse stream result: %w", rerr)
	}

	// D-06 teardown: stop() prevents the AfterFunc goroutine from emitting a
	// spurious session/cancel after the stream closed naturally. D-08: this is
	// NOT a shared stream driver — each emitter owns its own stop call.
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}

	// If no text chunks arrived at all, the role delta was never sent.
	// Emit it now so the stream is always role-first (API contract).
	if err := e.ensureRoleSent(); err != nil {
		return e.aggregatedResponse(canonical.StopUnknown, nil), err
	}

	stopReason := canonical.StopUnknown
	if final != nil {
		stopReason = final.StopReason
	}

	// Phase 6 iteration-3 skip-or-coerce-or-flush triage. Track the
	// coerce-synthesized ToolCalls for the post-stream aggregator
	// (quick 260530-df2) — if coerce hit, the canonical response
	// handed to RunPostHooks should carry the ToolCalls slice.
	var syntheticToolCalls []canonical.ToolCall
	switch {
	case e.sawKiroNativeToolCall:
		// Iteration-3 HIGH #2 fix: kiro-native ran during the stream.
		// SKIP coerce. Flush any buffered text frames in order, then
		// emit terminal finish_reason from canonical StopReason.
		if err := e.flushDeferred(); err != nil {
			return e.aggregatedResponse(stopReason, nil), err
		}
		if err := e.emitTerminalFrame(stopReason); err != nil {
			return e.aggregatedResponse(stopReason, nil), err
		}
	case !e.buffering:
		// No buffered text → no streaming-coerce candidate.
		if err := e.emitTerminalFrame(stopReason); err != nil {
			return e.aggregatedResponse(stopReason, nil), err
		}
	default:
		// Buffered candidate text + no kiro-native → try coerce.
		// tryStreamingCoerce returns the synthesized ToolCalls when
		// coerce hit so the post-stream aggregator can include them.
		tc, err := e.tryStreamingCoerce(stopReason)
		if err != nil {
			return e.aggregatedResponse(stopReason, nil), err
		}
		syntheticToolCalls = tc
	}

	// Literal [DONE] terminator — no JSON marshalling needed.
	if _, err := fmt.Fprintf(e.w, "data: [DONE]\n\n"); err != nil {
		return e.aggregatedResponse(stopReason, syntheticToolCalls), fmt.Errorf("openai: write [DONE]: %w", err)
	}
	e.flusher.Flush()
	// Quick 260530-df2 — clean completion: hand the aggregated response
	// (with coerce-synthesized ToolCalls when applicable) to the
	// caller, which fires RunPostHooks on it.
	return e.aggregatedResponse(stopReason, syntheticToolCalls), nil
}

// flushDeferred releases any buffered text frames in order. Used on the
// sawKiroNativeToolCall=true and coerce-miss paths. The frames were
// marshaled in applyTextChunk and have the full "data: ...\n\n" envelope.
func (e *sseEmitter) flushDeferred() error {
	for _, frame := range e.deferredTextFrames {
		if err := e.writeRaw(frame); err != nil {
			return err
		}
	}
	e.deferredTextFrames = nil
	return nil
}

// emitTerminalFrame writes the terminal finish_reason frame mapped from
// the canonical StopReason. Does NOT write [DONE] — finalizeSSE owns
// that terminator.
func (e *sseEmitter) emitTerminalFrame(stopReason canonical.StopReason) error {
	fr := mapFinishReason(stopReason)
	return e.writeData(e.buildChunk(chunkChoice{
		Index:        0,
		Delta:        chunkDelta{}, // empty delta on final frame
		FinishReason: &fr,
	}))
}

// tryStreamingCoerce builds a synthetic *canonical.ChatResponse from
// the buffered text and runs engine.CoerceToolCall. On HIT: emit the
// multi-frame native delta.tool_calls SSE sequence per D-07 OpenAI +
// terminal finish_reason:"tool_calls", and return the synthesized
// ToolCalls slice so the caller (finalizeSSE) can include them on
// the post-stream PostHook canonical response. On MISS: release the
// buffered text frames in order + emit terminal finish_reason from
// StopReason, and return nil ToolCalls.
//
// Quick 260530-df2: returning the ToolCalls slice (rather than just
// nil/error) lets PostHooks observe the same final canonical shape
// the wire produced — the coerce-hit case is what populates
// Message.ToolCalls.
func (e *sseEmitter) tryStreamingCoerce(stopReason canonical.StopReason) ([]canonical.ToolCall, error) {
	syntheticResp := &canonical.ChatResponse{
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: e.textBuffer.String()},
			},
		},
	}

	// Pitfall 6: pass the synthetic resp pointer directly — coerce
	// mutates in place; pre-copying would discard the mutation.
	if !engine.CoerceToolCall(e.req, syntheticResp) {
		// Coerce miss — release buffered frames + emit terminal.
		if err := e.flushDeferred(); err != nil {
			return nil, err
		}
		return nil, e.emitTerminalFrame(stopReason)
	}

	// Coerce hit — discard buffered frames and emit the multi-frame
	// native delta.tool_calls SSE shape (per D-07 + Pitfall 2: do NOT
	// split arguments across multiple deltas; frame C carries the
	// complete args as one string atom).
	e.deferredTextFrames = nil

	// Frame B: id + name, empty arguments.
	if len(syntheticResp.Message.ToolCalls) == 0 {
		// Defensive — CoerceToolCall returning true contractually appends
		// at least one ToolCall, but guard the read anyway (REVIEW LOW #7).
		return nil, e.emitTerminalFrame(stopReason)
	}
	tc := syntheticResp.Message.ToolCalls[0]

	if err := e.writeData(e.buildChunk(chunkChoice{
		Index: 0,
		Delta: chunkDelta{
			ToolCalls: []chunkDeltaToolCall{{
				Index: 0,
				ID:    tc.ID,
				Type:  "function",
				Function: chunkDeltaToolCallFunction{
					Name:      tc.Name,
					Arguments: "",
				},
			}},
		},
		FinishReason: nil,
	})); err != nil {
		return nil, err
	}

	// Frame C: arguments JSON-string (single atom, no splits — Pitfall 2).
	argsJSON, err := json.Marshal(tc.Arguments)
	if err != nil {
		argsJSON = []byte("{}")
	}
	if err := e.writeData(e.buildChunk(chunkChoice{
		Index: 0,
		Delta: chunkDelta{
			ToolCalls: []chunkDeltaToolCall{{
				Index: 0,
				Function: chunkDeltaToolCallFunction{
					Arguments: string(argsJSON),
				},
			}},
		},
		FinishReason: nil,
	})); err != nil {
		return nil, err
	}

	// Terminal finish_reason:"tool_calls" frame.
	fr := "tool_calls"
	if err := e.writeData(e.buildChunk(chunkChoice{
		Index:        0,
		Delta:        chunkDelta{}, // empty delta on final frame
		FinishReason: &fr,
	})); err != nil {
		return nil, err
	}
	return syntheticResp.Message.ToolCalls, nil
}

// runSyntheticSSEFromResponse writes the aggregated *canonical.ChatResponse
// as a one-shot synthetic SSE stream of chat.completion.chunk frames.
// Used by the encrypt-mode re-route branch in handlers.go when the CLIENT
// wire had stream=true but a Pre hook flipped req.Stream=false during
// eng.Run. Without this, the adapter would write application/json and trip
// SDK clients with "request ended without sending any chunks".
//
// Sequence emitted (matches the real per-chunk emitter's frame shape):
//
//   - One frame with delta.role="assistant" (the first-frame role marker
//     OpenAI clients key on).
//   - One frame per ContentPart carrying delta.content with the full text
//     in a single delta. tool_use parts are dropped on this path (the
//     v1 limitation already documented in handlers.go applies — synthetic
//     SSE cannot emit native delta.tool_calls without a coerce pass).
//   - One terminal frame with empty delta and finish_reason set.
//   - "data: [DONE]\n\n" terminator.
func runSyntheticSSEFromResponse(_ context.Context, w http.ResponseWriter, resp *canonical.ChatResponse, requestedModel string, logger *slog.Logger) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("openai: response writer is not flusher")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	model := requestedModel
	if model == "" && resp != nil {
		model = resp.Model
	}

	e := &sseEmitter{
		w:       w,
		flusher: flusher,
		logger:  logger,
		id:      genMessageID("chatcmpl-"),
		created: time.Now().Unix(),
		model:   model,
	}

	// Role marker frame.
	if err := e.writeData(e.buildChunk(chunkChoice{
		Index: 0,
		Delta: chunkDelta{Role: "assistant"},
	})); err != nil {
		return err
	}

	stopReason := canonical.StopUnknown
	if resp != nil {
		stopReason = resp.StopReason
		for _, part := range resp.Message.Content {
			if part.Kind != canonical.ContentKindText || part.Text == "" {
				continue
			}
			if err := e.writeData(e.buildChunk(chunkChoice{
				Index: 0,
				Delta: chunkDelta{Content: part.Text},
			})); err != nil {
				return err
			}
		}
	}

	// Final frame with finish_reason set + empty delta.
	finish := mapFinishReason(stopReason)
	if err := e.writeData(e.buildChunk(chunkChoice{
		Index:        0,
		Delta:        chunkDelta{},
		FinishReason: &finish,
	})); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(e.w, "data: [DONE]\n\n"); err != nil {
		return fmt.Errorf("openai: write [DONE]: %w", err)
	}
	e.flusher.Flush()
	return nil
}
