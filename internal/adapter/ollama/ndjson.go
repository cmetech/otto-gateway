package ollama

// D-05: w + flusher are touched ONLY from the select-loop goroutine inside
// runNDJSONEmitter. No mutex needed — single-goroutine invariant is enforced
// by construction. The watchdog goroutine (context.AfterFunc) MUST NOT touch
// w or flusher (Pitfall 8 in RESEARCH.md).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"otto-gateway/internal/canonical"
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
// emitNDJSONChunk — write one done:false NDJSON line for a canonical.Chunk
// ----------------------------------------------------------------------------

// emitNDJSONChunk marshals and writes one NDJSON chunk line. It handles:
//   - ChunkKindText → done:false line with content (chat) or response (generate)
//   - ChunkKindThought + isChat=true → done:false line with thinking field (D-04)
//   - ChunkKindThought + isChat=false → drop silently (/api/generate has no thinking — D-04)
//   - Other chunk kinds → drop silently
//
// On json.Marshal error or write error: calls cancelFn() (D-07 — adapter
// signals write failure to the engine watchdog via derived ctx cancel), then
// returns a wrapped error.
func emitNDJSONChunk(w http.ResponseWriter, flusher http.Flusher, c canonical.Chunk, model string, isChat bool, cancelFn context.CancelFunc) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var payload any

	switch c.Kind {
	case canonical.ChunkKindText:
		if c.Text == nil {
			return nil // defensive nil-guard; skip silently
		}
		if isChat {
			payload = ndjsonChatLine{
				Model:     model,
				CreatedAt: now,
				Message: ollamaChatResponseMessage{
					Role:    "assistant",
					Content: c.Text.Content,
				},
				Done: false,
			}
		} else {
			payload = ndjsonGenerateLine{
				Model:     model,
				CreatedAt: now,
				Response:  c.Text.Content,
				Done:      false,
			}
		}

	case canonical.ChunkKindThought:
		if !isChat {
			// D-04: /api/generate has no thinking field — drop silently.
			return nil
		}
		if c.Thought == nil {
			return nil // defensive nil-guard
		}
		payload = ndjsonChatLine{
			Model:     model,
			CreatedAt: now,
			Message: ollamaChatResponseMessage{
				Role:     "assistant",
				Thinking: c.Thought.Content,
			},
			Done: false,
		}

	default:
		// Unknown chunk kind — drop silently.
		return nil
	}

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
//  4. On chunk channel close: delegates to finalizeNDJSON.
//  5. On write error from emitNDJSONChunk: cancelFn is already called inside
//     emitNDJSONChunk; returns the error to the caller for debug-logging.
//
// runNDJSONEmitter is the SOLE goroutine touching w and flusher (D-05
// single-goroutine invariant). The watchdog goroutine (context.AfterFunc in
// engine.go) MUST NOT touch these (Pitfall 8).
//
// Returns nil on clean stream completion (done:true emitted), ctx.Err() on
// client disconnect, or a wrapped write/marshal error.
func runNDJSONEmitter(ctx context.Context, cancelFn context.CancelFunc, w http.ResponseWriter, run RunHandle, model string, isChat bool, start time.Time, logger *slog.Logger, req *canonical.ChatRequest) error {
	// req is threaded through so the streaming-coerce path (REVIEW HIGH #1 +
	// iteration-3 sawKiroNativeToolCall) can read req.Tools for the
	// end-of-stream CoerceToolCall invocation. Task 3 wires the buffering
	// + skip-or-coerce-or-flush logic; for now req is plumbed but only used
	// by the Task 3 changes.
	_ = req

	// Assert Flusher BEFORE any write so the caller can fall back to JSON 500
	// when the ResponseWriter does not support streaming (Pitfall 2).
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("ollama: response writer is not flusher")
	}

	// Set streaming headers BEFORE WriteHeader(200) — order matters (Pitfall 2).
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	chunks := run.Stream().Chunks()
	for {
		select {
		case <-ctx.Done():
			// Client disconnected or context canceled. Debug-log with session
			// context. Do NOT call cancelFn or Cancel — the watchdog (D-06)
			// handles ACP.Cancel via context.AfterFunc in engine.go.
			logger.Debug("ollama: ndjson client disconnect", "session_id", run.SessionID())
			return fmt.Errorf("ollama: ndjson ctx: %w", ctx.Err())

		case c, ok := <-chunks:
			if !ok {
				// Channel closed — stream ended naturally; emit final done:true line.
				return finalizeNDJSON(w, flusher, run, model, isChat, start, logger)
			}
			if err := emitNDJSONChunk(w, flusher, c, model, isChat, cancelFn); err != nil {
				// cancelFn was already called inside emitNDJSONChunk on write error.
				return err
			}
		}
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
//  3. Calls chatResponseToWire or generateResponseToWire with nil resp
//     (nil-safe confirmed in render.go: both helpers guard `if resp != nil`),
//     sets Done=true and DoneReason from final.StopReason, marshals and writes
//     the final NDJSON line.
func finalizeNDJSON(w http.ResponseWriter, flusher http.Flusher, run RunHandle, model string, isChat bool, start time.Time, logger *slog.Logger) error {
	final, rerr := run.Stream().Result()
	if rerr != nil {
		// Mid-stream / terminal engine error after headers: cannot send JSON 500.
		// Debug-log and return; no done:true line (D-05 truncation).
		logger.Debug("ollama: ndjson stream result error", "err", rerr)
		return fmt.Errorf("ollama: ndjson stream result: %w", rerr)
	}

	// D-06 teardown: prevent watchdog from emitting spurious Cancel after natural
	// stream completion. stop() returning false means ctx was already cancelled
	// and the goroutine may be executing Cancel — that is safe; Cancel is idempotent.
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}

	// Build done:true final line. chatResponseToWire / generateResponseToWire
	// both nil-guard their *canonical.ChatResponse parameter (render.go lines
	// 32-35 and 82-84), so passing nil is safe.
	stopReason := canonical.StopUnknown
	if final != nil {
		stopReason = final.StopReason
	}

	var payload any
	if isChat {
		out := chatResponseToWire(nil, start, model)
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
		return fmt.Errorf("ollama: ndjson marshal final: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\n", body); err != nil {
		return fmt.Errorf("ollama: ndjson write final: %w", err)
	}
	flusher.Flush()
	return nil
}
