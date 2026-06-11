package ollama

// D-05: w + flusher are touched ONLY from the select-loop goroutine inside
// runNDJSONEmitter. No mutex needed — single-goroutine invariant is enforced
// by construction. The watchdog goroutine (context.AfterFunc) MUST NOT touch
// w or flusher (Pitfall 8 in RESEARCH.md).
//
// Phase 6 (REVIEW HIGH #1 + iteration-3 sawKiroNativeToolCall): the
// emitter state — `textBuffer`, `buffering`, `deferredTextLines`,
// `sawKiroNativeToolCall` — is likewise touched ONLY inside the select-loop
// goroutine in runNDJSONEmitter. Threading req through to the emitter
// signature lets the end-of-stream coerce decision read req.Tools without
// extra goroutines.

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
// Intermediate NDJSON line structs (done:false frames only — RESEARCH.md Pitfall 7)
//
// These are kept separate from the done:true final line shapes produced by
// render.go helpers so intermediate frames never accidentally set done:true
// and the done:true final line has all stats fields.
// ----------------------------------------------------------------------------

// ndjsonChatLine is the per-chunk NDJSON frame emitted for /api/chat
// streaming (done:false frames). Role is set to "assistant" on every frame
// per Ollama Node reference.
type ndjsonChatLine struct {
	Model     string                    `json:"model"`
	CreatedAt string                    `json:"created_at"`
	Message   ollamaChatResponseMessage `json:"message"`
	Done      bool                      `json:"done"`
}

// ndjsonGenerateLine is the per-chunk NDJSON frame emitted for /api/generate
// streaming (done:false frames).
type ndjsonGenerateLine struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	Response  string `json:"response"`
	Done      bool   `json:"done"`
}

// ----------------------------------------------------------------------------
// emitterState — Phase 6 streaming coerce + kiro-native narration state
// ----------------------------------------------------------------------------

// emitterState carries the per-stream accumulators introduced in Phase 6
// Slice 2. Lives on the select-loop goroutine stack — no mutex (D-05
// single-goroutine invariant).
//
// WR-01 (Phase 6 review): textFlushed locks the Pitfall 3 "entire text"
// invariant in the streaming path. Once any non-buffered text has been
// written to the wire, we MUST NOT start buffering for coerce — coerce
// requires the JSON to be the entire response, and a split stream
// (prose first, JSON second) violates that.
type emitterState struct {
	textBuffer            strings.Builder
	buffering             bool
	deferredTextLines     [][]byte
	sawKiroNativeToolCall bool
	textFlushed           bool

	// Quick 260530-df2 — post-stream aggregator. Captures EVERY text
	// fragment written (or buffered) plus every thinking fragment, so
	// the canonical response handed to RunPostHooks after stream
	// completion has the same content shape engine.Collect produces
	// for non-streaming requests. Without this, LoggingHook.After +
	// ChatTraceHook.After observe an empty Message.Content shell and
	// the post_chain_out NDJSON record loses its product value.
	//
	// D-05 single-goroutine invariant applies — only emitNDJSONChunk /
	// finalizeNDJSON touch these fields, both inside the
	// runNDJSONEmitter goroutine.
	aggregatedText     strings.Builder
	aggregatedThinking strings.Builder
}

// shouldBuffer decides whether to start buffering. Returns true when:
//   - req.Tools is non-empty (no tools means no coerce target — never buffer)
//   - no non-buffered text has been flushed yet (Pitfall 3 "entire text"
//     invariant — WR-01 fix)
//   - the accumulated text (existing buffer plus the new chunk) begins with
//     `{` or a triple-backtick fence (the heuristic CoerceToolCall's
//     stripFences will recognize).
func (s *emitterState) shouldBuffer(req *canonical.ChatRequest, newText string) bool {
	if req == nil || len(req.Tools) == 0 {
		return false
	}
	if s.textFlushed {
		// WR-01: refuse to buffer once prose has already been flushed.
		// A split stream (prose then JSON) cannot satisfy Pitfall 3.
		return false
	}
	combined := strings.TrimSpace(s.textBuffer.String() + newText)
	if combined == "" {
		return false
	}
	return strings.HasPrefix(combined, "{") || strings.HasPrefix(combined, "```")
}

// ----------------------------------------------------------------------------
// emitNDJSONChunk — write one done:false NDJSON line for a canonical.Chunk
// ----------------------------------------------------------------------------

// emitNDJSONChunk marshals and writes one NDJSON chunk line. It handles:
//   - ChunkKindText → done:false line with content (chat) or response (generate)
//   - ChunkKindThought + isChat=true → done:false line with thinking field (D-04)
//   - ChunkKindThought + isChat=false → drop silently (/api/generate has no thinking — D-04)
//   - ChunkKindToolCall + isChat=true → emit `[tool: <name>]\n` narration line
//     per Phase 6 REVIEW HIGH #2 two-path rule. The done:true final line is the
//     SOLE source of Message.ToolCalls (from coerce only) per D-03/D-05.
//   - ChunkKindToolCall + isChat=false → drop silently (/api/generate has no
//     content-block semantics — kiro-native tool_calls cannot meaningfully
//     surface there).
//   - Other chunk kinds → drop silently
//
// On json.Marshal error or write error: calls cancelFn() (D-07 — adapter
// signals write failure to the engine watchdog via derived ctx cancel), then
// returns a wrapped error.
//
// Phase 6 Slice 2 (REVIEW HIGH #1 + iteration-3): when state != nil, this
// function does NOT write text chunks to the wire while state.buffering is
// active or being entered. Instead it appends to state.textBuffer and to
// state.deferredTextLines. The flush happens in finalizeStreamingCoerce.
// state.sawKiroNativeToolCall flips to true on the first ChunkKindToolCall.
func emitNDJSONChunk(w http.ResponseWriter, flusher http.Flusher, c canonical.Chunk, model string, isChat bool, cancelFn context.CancelFunc, state *emitterState, req *canonical.ChatRequest) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	switch c.Kind {
	case canonical.ChunkKindText:
		if c.Text == nil {
			return nil // defensive nil-guard; skip silently
		}
		return emitTextChunk(w, flusher, c.Text.Content, model, isChat, now, cancelFn, state, req)

	case canonical.ChunkKindThought:
		if !isChat {
			// D-04: /api/generate has no thinking field — drop silently.
			return nil
		}
		if c.Thought == nil {
			return nil // defensive nil-guard
		}
		payload := ndjsonChatLine{
			Model:     model,
			CreatedAt: now,
			Message: ollamaChatResponseMessage{
				Role:     "assistant",
				Thinking: c.Thought.Content,
			},
			Done: false,
		}
		if err := marshalAndWrite(w, flusher, payload, cancelFn); err != nil {
			return err
		}
		// Quick 260530-df2 — aggregate thinking for the post-stream
		// canonical response. Mirrors engine.Collect's thoughtSB.
		if state != nil {
			state.aggregatedThinking.WriteString(c.Thought.Content)
		}
		return nil

	case canonical.ChunkKindToolCall:
		// REVIEW HIGH #2 + iteration-3 fix to HIGH #2: kiro-native tool_call
		// emits a `[tool: <name>]\n` thought-text narration line and sets
		// sawKiroNativeToolCall=true on the emitter state (suppresses the
		// end-of-stream coerce). Does NOT accumulate into any tool_calls
		// slice — the two-path rule isolates kiro-native (narration only)
		// from coerce-synthesized (done line only).
		if !isChat {
			return nil // /api/generate has no content-block semantics.
		}
		name := "unknown"
		if c.ToolCall != nil && c.ToolCall.Name != "" {
			name = c.ToolCall.Name
		}
		if state != nil {
			state.sawKiroNativeToolCall = true
		}
		narration := fmt.Sprintf("[tool: %s]\n", name)
		payload := ndjsonChatLine{
			Model:     model,
			CreatedAt: now,
			Message: ollamaChatResponseMessage{
				Role:    "assistant",
				Content: narration,
			},
			Done: false,
		}
		if err := marshalAndWrite(w, flusher, payload, cancelFn); err != nil {
			return err
		}
		// Quick 260530-df2 — aggregate the narration into Content text
		// so the post-stream canonical response carries the kiro-native
		// tool_call visibility for PostHooks. Mirrors engine.Collect's
		// `[tool: name]\n` narration text (collect.go ChunkKindToolCall
		// branch).
		if state != nil {
			state.aggregatedText.WriteString(narration)
		}
		return nil

	default:
		// Unknown chunk kind — drop silently.
		return nil
	}
}

// emitTextChunk handles the text-chunk emission with the streaming-coerce
// buffering branch (REVIEW HIGH #1). When buffering kicks in, the line is
// built but stashed in state.deferredTextLines rather than flushed to the
// wire. The buffer is released or discarded at stream close depending on
// the sawKiroNativeToolCall / coerce-hit decision.
func emitTextChunk(w http.ResponseWriter, flusher http.Flusher, text, model string, isChat bool, now string, cancelFn context.CancelFunc, state *emitterState, req *canonical.ChatRequest) error {
	// /api/generate cannot meaningfully coerce — its response shape has no
	// content-block / tool_calls envelope. Stream through unchanged.
	if !isChat || state == nil {
		var payload any
		if isChat {
			payload = ndjsonChatLine{
				Model:     model,
				CreatedAt: now,
				Message: ollamaChatResponseMessage{
					Role:    "assistant",
					Content: text,
				},
				Done: false,
			}
		} else {
			payload = ndjsonGenerateLine{
				Model:     model,
				CreatedAt: now,
				Response:  text,
				Done:      false,
			}
		}
		if err := marshalAndWrite(w, flusher, payload, cancelFn); err != nil {
			return err
		}
		// Quick 260530-df2 — aggregate text for the post-stream
		// canonical response. Even on /api/generate (state == nil)
		// the aggregator would be useful for PostHooks; the state==nil
		// branch here pre-dates df2 and is preserved for parity.
		if state != nil {
			state.aggregatedText.WriteString(text)
		}
		return nil
	}

	// Streaming-coerce buffering decision (REVIEW HIGH #1):
	//   - If we're already buffering, keep buffering (the entire run is
	//     consistent: once we suspect JSON, never half-flush half-buffer).
	//   - Otherwise, decide based on the accumulated trimmed text shape.
	if !state.buffering {
		if !state.shouldBuffer(req, text) {
			// Non-JSON-shaped text — stream directly (Phase 4 behavior).
			// WR-01: record that non-buffered text reached the wire so any
			// future JSON-shaped chunk in this stream cannot retroactively
			// start buffering (split-stream coerce is unsafe per Pitfall 3).
			state.textFlushed = true
			payload := ndjsonChatLine{
				Model:     model,
				CreatedAt: now,
				Message: ollamaChatResponseMessage{
					Role:    "assistant",
					Content: text,
				},
				Done: false,
			}
			if err := marshalAndWrite(w, flusher, payload, cancelFn); err != nil {
				return err
			}
			// Quick 260530-df2 — aggregate non-buffered text for the
			// post-stream canonical response.
			state.aggregatedText.WriteString(text)
			return nil
		}
		// First time we see JSON-shape — enter buffering.
		state.buffering = true
	}

	// Buffering branch: accumulate into the text buffer AND build the
	// would-be NDJSON line for later flush. Do NOT write or flush yet.
	state.textBuffer.WriteString(text)
	// Quick 260530-df2 — also aggregate into the post-stream canonical
	// response builder. If coerce hits at finalizeNDJSON, the post-stream
	// response uses the synthesized resp instead; if coerce misses (or
	// is skipped), this aggregated text is what PostHooks observe.
	state.aggregatedText.WriteString(text)
	payload := ndjsonChatLine{
		Model:     model,
		CreatedAt: now,
		Message: ollamaChatResponseMessage{
			Role:    "assistant",
			Content: text,
		},
		Done: false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		cancelFn() // D-07: signal write failure via derived ctx
		return fmt.Errorf("ollama: ndjson marshal buffered chunk: %w", err)
	}
	// Append newline to preserve the NDJSON line discipline when the buffer
	// is released as-is later.
	line := make([]byte, 0, len(body)+1)
	line = append(line, body...)
	line = append(line, '\n')
	state.deferredTextLines = append(state.deferredTextLines, line)
	return nil
}

// marshalAndWrite is the per-chunk wire-write helper. Centralizes the
// json.Marshal + fmt.Fprintf + flusher.Flush sequence and the D-07
// cancelFn signaling on error.
func marshalAndWrite(w http.ResponseWriter, flusher http.Flusher, payload any, cancelFn context.CancelFunc) error {
	body, err := json.Marshal(payload)
	if err != nil {
		cancelFn() // D-07: signal write failure via derived ctx
		return fmt.Errorf("ollama: ndjson marshal chunk: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\n", body); err != nil {
		cancelFn() // D-07: broken pipe signals the engine watchdog
		return fmt.Errorf("ollama: ndjson write chunk: %w", err)
	}
	flusher.Flush()
	return nil
}

// ----------------------------------------------------------------------------
// runNDJSONEmitter — entry point for the NDJSON streaming branch
// ----------------------------------------------------------------------------

// runNDJSONEmitter is the entry point for the NDJSON streaming branch of
// handleChat and handleGenerate. It:
//  1. Asserts http.Flusher BEFORE writing any bytes (so the caller can still
//     emit a JSON 500 if Flusher is absent — Pitfall 2 in RESEARCH.md).
//  2. Sets Content-Type: application/x-ndjson and Cache-Control: no-cache
//     BEFORE WriteHeader(200) (Pitfall 2 order).
//  3. Runs the core select-loop: ctx.Done | chunk channel.
//  4. On chunk channel close: delegates to finalizeNDJSON with the emitter
//     state — which decides skip-or-coerce-or-flush per iteration-3 logic.
//  5. On write error from emitNDJSONChunk: cancelFn is already called inside
//     emitNDJSONChunk; returns the error to the caller for debug-logging.
//
// runNDJSONEmitter is the SOLE goroutine touching w and flusher AND the
// emitter state (D-05 single-goroutine invariant). The watchdog goroutine
// (context.AfterFunc in engine.go) MUST NOT touch these (Pitfall 8).
//
// Returns the aggregated canonical response (non-nil even on disconnect
// / mid-stream error so PostHooks can observe forensics — quick
// 260530-df2) alongside the error. Error is nil on clean stream
// completion (done:true emitted), ctx.Err() on client disconnect, or a
// wrapped write/marshal error. The Flusher-assertion failure returns
// (nil, err) BEFORE any aggregation.
func runNDJSONEmitter(ctx context.Context, cancelFn context.CancelFunc, w http.ResponseWriter, run RunHandle, model string, isChat bool, start time.Time, logger *slog.Logger, req *canonical.ChatRequest, streamIdle time.Duration) (*canonical.ChatResponse, error) {
	// Assert Flusher BEFORE any write so the caller can fall back to JSON 500
	// when the ResponseWriter does not support streaming (Pitfall 2).
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("ollama: response writer is not flusher")
	}

	// Set streaming headers BEFORE WriteHeader(200) — order matters (Pitfall 2).
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	// Phase 6 Slice 2 emitter state (REVIEW HIGH #1 + iteration-3 HIGH #2).
	// Lives on the goroutine stack — no mutex needed (D-05).
	state := &emitterState{}

	chunks := run.Stream().Chunks()

	// Quick 260531-ruv — idle watchdog arm. Nil idleC means "disabled"
	// (never-ready channel idiom). Drain-safe Stop/Reset on each chunk
	// matches engine.RangeChunksWithIdleTimeout exactly.
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
			// Client disconnected or context canceled. Debug-log with session
			// context. Do NOT call cancelFn or Cancel — the watchdog (D-06)
			// handles ACP.Cancel via context.AfterFunc in engine.go.
			//
			// Quick 260530-df2 — disconnect path: return the partial
			// aggregated response so handlers fire PostHooks with whatever
			// was assembled. Operators want forensics + duration_ms.
			logger.Debug("ollama: ndjson client disconnect", "session_id", run.SessionID())
			return aggregateOllamaResponse(req, state, canonical.StopUnknown), fmt.Errorf("ollama: ndjson ctx: %w", ctx.Err())

		case <-idleC:
			// Quick 260531-ruv — stream-idle fire. Audit
			// ollama-ndjson-idle-timeout-terminal-frame-missing-fields:
			// previously emitted a bare {"error":"...","done":true} with
			// no model / created_at / message — strict Ollama SDK
			// consumers (ollama-js, LangFlow's Ollama loader) rejected
			// or mis-rendered it. Now build via
			// chatResponseToWire / generateResponseToWire so every
			// terminal envelope carries the documented field set, with
			// DoneReason="error" + Error="stream idle timeout".
			logger.Warn(
				"stream.idle_timeout",
				"surface", "ollama",
				"session_id", run.SessionID(),
				"elapsed_ms", streamIdle.Milliseconds(),
				"request_id", plugin.RequestIDFromContext(ctx),
			)
			emptyResp := aggregateOllamaResponse(req, state, canonical.StopUnknown)
			if isChat {
				frame := chatResponseToWire(emptyResp, start, model)
				frame.Done = true
				frame.DoneReason = "error"
				frame.Error = "stream idle timeout"
				_ = marshalAndWrite(w, flusher, frame, cancelFn)
			} else {
				frame := generateResponseToWire(emptyResp, start, model)
				frame.Done = true
				frame.DoneReason = "error"
				frame.Error = "stream idle timeout"
				_ = marshalAndWrite(w, flusher, frame, cancelFn)
			}
			// CR-02 fix (phase 15 review, applied symmetrically to ollama):
			// fire cancelFn() now so the watchdog AfterFunc issues
			// session/cancel + the pool slot returns BEFORE the handler's
			// PostHooks run. Without this, the kiro-cli worker keeps
			// generating into a slot the next acquirer is waiting on; on a
			// 1-slot pool that turns NewSession's bounded acquire into a
			// PostHook-latency-bounded wait followed by ErrPoolExhausted.
			// cancelFn is idempotent — marshalAndWrite may already have
			// fired it on write failure, the watchdog AfterFunc may already
			// have fired Cancel, and Pool.Cancel is itself idempotent.
			cancelFn()
			return emptyResp,
				fmt.Errorf("ollama: ndjson %w", canonical.ErrStreamIdleTimeout)

		case c, ok := <-chunks:
			if !ok {
				// Channel closed — stream ended naturally; emit final done:true line.
				return finalizeNDJSON(w, flusher, run, model, isChat, start, logger, state, req)
			}
			if err := emitNDJSONChunk(w, flusher, c, model, isChat, cancelFn, state, req); err != nil {
				// cancelFn was already called inside emitNDJSONChunk on write error.
				// Quick 260530-df2 — still return the partial aggregation
				// so handlers can fire PostHooks for forensics.
				return aggregateOllamaResponse(req, state, canonical.StopUnknown), err
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

// aggregateOllamaResponse builds a *canonical.ChatResponse from the
// emitter state for the post-stream PostHook chain. Quick 260530-df2:
// mirrors engine.Collect's assembleChatResponse (collect.go:147-175) so
// the canonical response handed to RunPostHooks has the same content
// shape the non-streaming path produces.
//
// Tool-call handling differs from anthropic because ollama's wire shape
// has tool_calls only on the done:true line — ChunkKindToolCall is
// rendered as `[tool: name]\n` narration text per Phase 6 REVIEW HIGH #2.
// When the streaming-coerce path hits (coerce-synthesized ToolCalls
// populated on a synthetic resp built inside finalizeNDJSON), the
// finalize caller uses that synthetic resp instead of this builder.
func aggregateOllamaResponse(req *canonical.ChatRequest, state *emitterState, stop canonical.StopReason) *canonical.ChatResponse {
	model := ""
	if req != nil {
		model = req.Model
	}
	content := []canonical.ContentPart{
		{Kind: canonical.ContentKindText, Text: state.aggregatedText.String()},
	}
	if state.aggregatedThinking.Len() > 0 {
		content = append(content, canonical.ContentPart{
			Kind: canonical.ContentKindThinking,
			Text: state.aggregatedThinking.String(),
		})
	}
	return &canonical.ChatResponse{
		Model: model,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: content,
		},
		StopReason: stop,
	}
}

// ----------------------------------------------------------------------------
// finalizeNDJSON — emit the closing done:true line after channel close
// ----------------------------------------------------------------------------

// finalizeNDJSON is called after the chunk channel closes (normal stream end).
// It:
//  1. Calls run.Stream().Result() — on error, debug-logs and returns without
//     writing a done:true line (D-05 truncated stream; no error JSON line sent).
//  2. Calls run.StopWatchdog() and invokes the stop func to prevent the D-06
//     watchdog goroutine from firing a spurious ACP.Cancel after natural
//     stream completion (Pitfall 3 / RESEARCH.md Pattern 2 Option A).
//  3. Phase 6 Slice 2 iteration-3 skip-or-coerce-or-flush logic on the
//     emitter state:
//     a. If state.sawKiroNativeToolCall == true: SKIP coerce. Release any
//     buffered text as plain NDJSON lines. Done:true line carries NO
//     tool_calls.
//     b. Else if !state.buffering: emit done:true normally (Phase 4
//     behavior, unchanged).
//     c. Else: build synthetic resp from textBuffer + call
//     engine.CoerceToolCall(req, syntheticResp). On hit: DISCARD
//     deferredTextLines, compose done:true via chatResponseToWire which
//     renders Message.ToolCalls. On miss: RELEASE deferredTextLines in
//     order, emit done:true normally.
//  4. Calls chatResponseToWire or generateResponseToWire to build the final
//     line, sets Done=true and DoneReason from final.StopReason, marshals
//     and writes.
func finalizeNDJSON(w http.ResponseWriter, flusher http.Flusher, run RunHandle, model string, isChat bool, start time.Time, logger *slog.Logger, state *emitterState, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	final, rerr := run.Stream().Result()
	if rerr != nil {
		// H-3 fix (REL-HTTP-03): mid-stream worker death — emit surface-native
		// done:true + done_reason:error terminal line so Ollama clients (LangFlow
		// NDJSON aggregator) see an explicit end-of-stream marker instead of a
		// silent truncated body. WARN log with all D-09/D-10 required fields.
		//
		// Quick 260530-df2: still return the partial aggregation so
		// PostHooks fire for forensics + duration_ms.

		// D-09/D-10 WARN log: session_id, worker_pid, kiro_exit_code, bytes_streamed.
		// worker_pid: RunHandle interface does not expose a PID accessor; log 0
		// as a placeholder until the interface is extended.
		// bytes_streamed: ndjson emitter does not yet wire a bytes counter; log 0.
		logArgs := []any{
			"session_id", run.SessionID(),
			"worker_pid", 0, // worker_pid: counter not yet wired in RunHandle interface
			"bytes_streamed", 0, // bytes_streamed: counter not yet wired in ndjson emitter
			"err", rerr,
		}
		var exitErr *exec.ExitError
		if errors.As(rerr, &exitErr) {
			logArgs = append(logArgs, "kiro_exit_code", exitErr.ExitCode())
		}
		// kiro_exit_code omitted when rerr chain has no *exec.ExitError
		logger.Warn("ollama: ndjson worker terminated mid-stream", logArgs...)

		// Emit D-09 Ollama surface-native terminal error line: done:true + done_reason:error.
		// Mirror the idle-timeout path (ndjson.go idleC arm) which already emits this pattern.
		//
		// WR-02 fix (phase 15 review): renamed from `emptyResp` — the
		// variable carries the FULL aggregated text accumulated from chunks
		// that arrived BEFORE the worker died, not an empty response.
		// TODO: PII decrypt PostHook may receive partially-encrypted
		// ciphertext here (the truncated prefix of an encrypted span).
		// The pii.DecryptHook is expected to handle invalid ciphertext
		// defensively; if a "PostHook PII decrypt: invalid ciphertext"
		// log line appears alongside an upstream_disconnect terminal
		// frame, this site is the correlation source.
		partialResp := aggregateOllamaResponse(req, state, canonical.StopUnknown)
		if isChat {
			frame := chatResponseToWire(partialResp, start, model)
			frame.Done = true
			frame.DoneReason = "error"
			frame.Error = "upstream_disconnect: worker terminated mid-stream"
			_ = marshalAndWrite(w, flusher, frame, nil)
		} else {
			frame := generateResponseToWire(partialResp, start, model)
			frame.Done = true
			frame.DoneReason = "error"
			frame.Error = "upstream_disconnect: worker terminated mid-stream"
			_ = marshalAndWrite(w, flusher, frame, nil)
		}

		return partialResp, fmt.Errorf("ollama: ndjson stream result: %w", rerr)
	}

	// D-06 teardown: prevent watchdog from emitting spurious Cancel after natural
	// stream completion. stop() returning false means ctx was already cancelled
	// and the goroutine may be executing Cancel — that is safe; Cancel is idempotent.
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}

	stopReason := canonical.StopUnknown
	if final != nil {
		stopReason = final.StopReason
	}

	// Phase 6 Slice 2 streaming-coerce decision (REVIEW HIGH #1 + iteration-3 HIGH #2).
	// Only runs on chat path (state is nil and unused for /api/generate paths
	// that pass empty req.Tools; the buffering would never have engaged anyway).
	var syntheticResp *canonical.ChatResponse
	coerceFired := false
	if isChat && state != nil {
		if state.sawKiroNativeToolCall {
			// Iteration-3 fix to HIGH #2: kiro-native fired during the
			// stream, so we SKIP coerce entirely and FLUSH any buffered text
			// as plain text lines. The user-visible behavior is "kiro-native
			// ran (narration already emitted), plus any incidental
			// JSON-shaped text that wasn't a coerce target".
			if err := releaseBufferedLines(w, flusher, state.deferredTextLines); err != nil {
				return aggregateOllamaResponse(req, state, stopReason), err
			}
		} else if state.buffering {
			// Coerce path: build a synthetic resp from the accumulated text
			// and run engine.CoerceToolCall. Pointer-direct per Pitfall 6.
			syntheticResp = &canonical.ChatResponse{
				Message: canonical.Message{
					Role: canonical.RoleAssistant,
					Content: []canonical.ContentPart{
						{Kind: canonical.ContentKindText, Text: state.textBuffer.String()},
					},
				},
				StopReason: stopReason,
			}
			if engine.CoerceToolCall(req, syntheticResp) {
				coerceFired = true
				// Defensive length-guard per REVIEW LOW #7.
				var firstName string
				if len(syntheticResp.Message.ToolCalls) > 0 {
					firstName = syntheticResp.Message.ToolCalls[0].Name
				}
				logger.Debug("ollama: streaming coerce fired", "tool", firstName)
				// Discard the buffered text lines — they are superseded by
				// the synthesized tool_calls on the done:true line.
			} else {
				// Coerce missed — release the buffered text lines and fall
				// through to the standard done:true emission.
				if err := releaseBufferedLines(w, flusher, state.deferredTextLines); err != nil {
					return aggregateOllamaResponse(req, state, stopReason), err
				}
			}
		}
	}

	// Build done:true final line. chatResponseToWire / generateResponseToWire
	// both nil-guard their *canonical.ChatResponse parameter, so passing nil
	// is safe in the no-coerce case.
	var payload any
	if isChat {
		var doneResp *canonical.ChatResponse
		if coerceFired {
			// The synthetic resp carries the populated ToolCalls slice —
			// chatResponseToWire's tool_calls populate loop picks it up.
			doneResp = syntheticResp
		}
		out := chatResponseToWire(doneResp, start, model)
		out.Done = true
		out.DoneReason = mapStopReason(stopReason)
		payload = out
	} else {
		out := generateResponseToWire(nil, start, model)
		out.Done = true
		out.DoneReason = mapStopReason(stopReason)
		payload = out
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return aggregateOllamaResponse(req, state, stopReason), fmt.Errorf("ollama: ndjson marshal final: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\n", body); err != nil {
		return aggregateOllamaResponse(req, state, stopReason), fmt.Errorf("ollama: ndjson write final: %w", err)
	}
	flusher.Flush()
	// Quick 260530-df2 — clean stream completion: build the canonical
	// response handed to RunPostHooks. Coerce hit → use the synthetic
	// resp (already has Message.ToolCalls populated). Otherwise use the
	// aggregator (text + thinking).
	var postResp *canonical.ChatResponse
	if coerceFired && syntheticResp != nil {
		postResp = syntheticResp
		if req != nil {
			postResp.Model = req.Model // mirror chatResponseToWire model echo
		}
		postResp.StopReason = stopReason
	} else {
		postResp = aggregateOllamaResponse(req, state, stopReason)
	}
	return postResp, nil
}

// releaseBufferedLines flushes the deferred text lines to the wire in order.
// Used by:
//   - The iteration-3 sawKiroNativeToolCall branch (release without coerce).
//   - The coerce-miss branch (release after coerce returned false).
//
// Never called on the coerce-hit branch (those lines are discarded because
// they are superseded by the synthesized tool_calls on the done line).
func releaseBufferedLines(w http.ResponseWriter, flusher http.Flusher, lines [][]byte) error {
	for _, line := range lines {
		if _, err := w.Write(line); err != nil {
			return fmt.Errorf("ollama: ndjson release buffered line: %w", err)
		}
		flusher.Flush()
	}
	return nil
}

// runSyntheticNDJSONFromResponse writes the aggregated *canonical.ChatResponse
// as a synthetic NDJSON stream. Used by the encrypt-mode re-route branch
// in handlers.go when the CLIENT wire had stream=true but a Pre hook
// flipped req.Stream=false during eng.Run. Without this, the adapter
// would write application/json and trip LangFlow Ollama clients (and any
// other stream-only NDJSON consumer) — the v1.8.3 regression that
// motivated this path.
//
// Sequence emitted:
//   - One done:false line carrying the full text in message.content
//     (chat) or response (generate). Empty content degrades to "" so
//     consumers always see at least one frame before done:true.
//   - One done:true terminal frame built via chatResponseToWire /
//     generateResponseToWire — same shape the non-streaming JSON
//     response would have used, so stats and stop_reason mapping match.
//
// isChat=true selects the /api/chat NDJSON shape; false selects /api/generate.
func runSyntheticNDJSONFromResponse(_ context.Context, w http.ResponseWriter, resp *canonical.ChatResponse, requestedModel string, isChat bool, start time.Time, _ *slog.Logger) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("ollama: response writer is not flusher")
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	model := requestedModel
	if model == "" && resp != nil {
		model = resp.Model
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Concatenate text ContentParts so the synthetic frame carries the
	// full decrypted text in one line. tool_use / thinking parts are
	// dropped on this path (v1 limitation).
	var fullText string
	if resp != nil {
		var sb strings.Builder
		for _, part := range resp.Message.Content {
			if part.Kind == canonical.ContentKindText {
				sb.WriteString(part.Text)
			}
		}
		fullText = sb.String()
	}

	if isChat {
		chunk := ndjsonChatLine{
			Model:     model,
			CreatedAt: now,
			Message: ollamaChatResponseMessage{
				Role:    "assistant",
				Content: fullText,
			},
			Done: false,
		}
		body, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("ollama: marshal synthetic chat chunk: %w", err)
		}
		if _, err := w.Write(append(body, '\n')); err != nil {
			return fmt.Errorf("ollama: write synthetic chat chunk: %w", err)
		}
		flusher.Flush()

		// Terminal done:true via chatResponseToWire so stats + done_reason match.
		terminal := chatResponseToWire(resp, start, requestedModel)
		body, err = json.Marshal(terminal)
		if err != nil {
			return fmt.Errorf("ollama: marshal synthetic chat terminal: %w", err)
		}
		if _, err := w.Write(append(body, '\n')); err != nil {
			return fmt.Errorf("ollama: write synthetic chat terminal: %w", err)
		}
		flusher.Flush()
		return nil
	}

	// /api/generate variant.
	chunk := ndjsonGenerateLine{
		Model:     model,
		CreatedAt: now,
		Response:  fullText,
		Done:      false,
	}
	body, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("ollama: marshal synthetic generate chunk: %w", err)
	}
	if _, err := w.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("ollama: write synthetic generate chunk: %w", err)
	}
	flusher.Flush()

	terminal := generateResponseToWire(resp, start, requestedModel)
	body, err = json.Marshal(terminal)
	if err != nil {
		return fmt.Errorf("ollama: marshal synthetic generate terminal: %w", err)
	}
	if _, err := w.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("ollama: write synthetic generate terminal: %w", err)
	}
	flusher.Flush()
	return nil
}

// writePoolExhaustedOllama writes a 503 response with the D-07 Ollama
// surface-native pool-exhaustion body and a Retry-After: 5 header.
//
// Body: {"error":"pool_exhausted: all workers busy; retry in 5s"}
//
// Called by handlers.go on the streaming and non-streaming paths when
// errors.Is(err, canonical.ErrPoolExhausted) is true.
func writePoolExhaustedOllama(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "pool_exhausted: all workers busy; retry in 5s",
	})
}
