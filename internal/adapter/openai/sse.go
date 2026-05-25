package openai

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
type chunkDelta struct {
	Role    string `json:"role,omitempty"`    // "assistant" on first chunk only
	Content string `json:"content,omitempty"` // text fragment on text chunks
}

// ----------------------------------------------------------------------------
// sseEmitter — per-request streaming state machine (OpenAI flat variant)
//
// D-05: w + flusher are touched ONLY by writeData (which is called only
// from the select-loop goroutine). No mutex needed — single-goroutine
// invariant is enforced by construction.
// ----------------------------------------------------------------------------

type sseEmitter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	logger   *slog.Logger
	id       string // fixed per response (Pitfall 8)
	created  int64  // fixed per response (Pitfall 8)
	model    string
	roleSent bool // tracks whether the role:assistant delta was already emitted
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

// applyChunk processes one canonical.Chunk and emits the appropriate
// data: frame. On the first call, prepends a role:assistant delta if not
// yet sent. Only ChunkKindText is handled in Phase 3; other kinds are
// silently dropped (no state change — same as anthropic/sse.go logic for
// unsupported kinds).
func (e *sseEmitter) applyChunk(c canonical.Chunk) error {
	if c.Kind != canonical.ChunkKindText {
		// Unsupported chunk kind (ChunkKindThought, ChunkKindToolCall, etc.)
		// — drop silently. Phase 3 scope.
		e.logger.Debug("openai: sse unsupported chunk kind dropped", "kind", c.Kind)
		return nil
	}

	// First text chunk: emit role:assistant delta first if not yet done.
	if !e.roleSent {
		if err := e.writeData(e.buildChunk(chunkChoice{
			Index:        0,
			Delta:        chunkDelta{Role: "assistant"},
			FinishReason: nil,
		})); err != nil {
			return err
		}
		e.roleSent = true
	}

	// Emit the content delta. Nil Text pointer is defensive; skip silently.
	if c.Text == nil {
		return nil
	}
	return e.writeData(e.buildChunk(chunkChoice{
		Index:        0,
		Delta:        chunkDelta{Content: c.Text.Content},
		FinishReason: nil,
	}))
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
//     c. Final chunk: delta={}, finish_reason="<mapped>", then "data: [DONE]\n\n"
//  4. On ctx.Done: debug-logs the disconnect and returns ctx.Err() without [DONE].
//  5. On Result() error after channel close: logs at debug + returns the error
//     WITHOUT emitting [DONE] (truncated stream is acceptable per A5; OpenAI
//     has no error-frame contract — Pitfall 3).
//
// Returns nil on clean stream completion ([DONE] emitted), ctx.Err() on
// client disconnect, or a wrapped write/marshal error.
func runSSEEmitter(ctx context.Context, w http.ResponseWriter, run RunHandle, model string, logger *slog.Logger) error {
	// Assert Flusher BEFORE any write so the caller can fall back to JSON 500
	// if the ResponseWriter does not support streaming (Pitfall 2 + anthropic analog).
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("openai: response writer is not flusher")
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
	}

	chunks := run.Stream().Chunks()
	for {
		select {
		case <-ctx.Done():
			// Client disconnected. Debug-log with session context, return ctx error.
			// Do NOT emit [DONE] — the stream tore down before natural end.
			e.logger.Debug("openai: sse client disconnect", "session_id", run.SessionID())
			return fmt.Errorf("openai: sse ctx: %w", ctx.Err())

		case c, ok := <-chunks:
			if !ok {
				// Channel closed — stream ended; emit final frames.
				return finalizeSSE(e, run)
			}
			if err := e.applyChunk(c); err != nil {
				return err
			}
		}
	}
}

// finalizeSSE emits the closing frames after the chunk channel closes.
// Extracted so the chunks-closed branch in runSSEEmitter stays scannable.
//
// Sequence:
//  1. If no role delta was emitted (empty stream), emit it now so the
//     stream is always well-formed.
//  2. Emit the finish_reason frame: delta={}, finish_reason=<mapped>.
//  3. Write the literal "data: [DONE]\n\n" terminator + Flush.
//
// On run.Stream().Result() error: log at debug, stop WITHOUT emitting
// finish_reason or [DONE] (truncated stream — Pitfall 3 / A5).
func finalizeSSE(e *sseEmitter, run RunHandle) error {
	final, rerr := run.Stream().Result()
	if rerr != nil {
		// Mid-stream / terminal engine error after headers: cannot send JSON 500.
		// Log at debug (not error — the stream just cut off; the client-side
		// will see a truncated stream, which is acceptable per A5).
		e.logger.Debug("openai: sse stream result error", "err", rerr)
		return fmt.Errorf("openai: sse stream result: %w", rerr)
	}

	// D-06 teardown: stop() prevents the AfterFunc goroutine from emitting a
	// spurious session/cancel after the stream closed naturally. D-08: this is
	// NOT a shared stream driver — each emitter owns its own stop call.
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}

	// If no text chunks arrived at all, the role delta was never sent.
	// Emit it now so the stream is always role-first (API contract).
	if !e.roleSent {
		if err := e.writeData(e.buildChunk(chunkChoice{
			Index:        0,
			Delta:        chunkDelta{Role: "assistant"},
			FinishReason: nil,
		})); err != nil {
			return err
		}
	}

	// Emit the terminal finish_reason frame (delta={}, finish_reason non-null).
	stopReason := canonical.StopUnknown
	if final != nil {
		stopReason = final.StopReason
	}
	fr := mapFinishReason(stopReason)
	if err := e.writeData(e.buildChunk(chunkChoice{
		Index:        0,
		Delta:        chunkDelta{}, // empty delta on final frame
		FinishReason: &fr,
	})); err != nil {
		return err
	}

	// Literal [DONE] terminator — no JSON marshalling needed.
	if _, err := fmt.Fprintf(e.w, "data: [DONE]\n\n"); err != nil {
		return fmt.Errorf("openai: write [DONE]: %w", err)
	}
	e.flusher.Flush()
	return nil
}
