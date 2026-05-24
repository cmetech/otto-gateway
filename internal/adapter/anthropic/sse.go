package anthropic

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

// PingInterval is the cadence at which the SSE emitter writes
// `event: ping` keepalive frames during idle stretches of the stream
// (W7 — named const so cadence is auditable via grep; tests reference
// this identifier directly so a literal change here flips the
// PingInterval_Constant test). Anthropic's SDK starts a 60-second
// connection idle timer per @anthropic-ai/sdk@^0.90; 15 s is
// comfortably below that ceiling and matches Anthropic's own
// reference behavior (RESEARCH.md §Pattern 2 line 456 +
// CONTEXT.md `<specifics>`).
const PingInterval = 15 * time.Second

// ----------------------------------------------------------------------------
// SSE event payload types — one Go type per Anthropic event variant.
//
// Field order is LOAD-BEARING: encoding/json walks struct fields in
// declaration order. Golden-fixture tests (sse_golden_test.go) compare
// byte-exact output against canonical Anthropic wire bytes. Any
// reordering here will break those tests; reorder the golden file too
// if you intentionally change a payload shape.
// ----------------------------------------------------------------------------

// messageStart is the payload for `event: message_start`. The embedded
// Message reuses the non-streaming anthropicMessage type (render.go) so
// the streaming and non-streaming responses share a single source of
// truth for the message envelope shape.
type messageStart struct {
	Type    string           `json:"type"`
	Message anthropicMessage `json:"message"`
}

// textBlockHeader is the content_block payload for a text block start.
// Distinct type (vs. one polymorphic blockHeader) so the JSON encoder
// emits `{"type":"text","text":""}` without an unwanted `"thinking"`
// field. Type is always the literal string "text".
type textBlockHeader struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// thinkingBlockHeader is the content_block payload for a thinking
// block start. Type is always the literal string "thinking".
type thinkingBlockHeader struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

// contentBlockStart is the payload for `event: content_block_start`.
// ContentBlock is `any` because the inner shape varies per block kind
// (textBlockHeader for text, thinkingBlockHeader for thinking).
type contentBlockStart struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock any    `json:"content_block"`
}

// textDelta is the payload for a `content_block_delta` whose delta is
// an incremental text fragment. Type is always "text_delta".
type textDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// thinkingDelta is the payload for a `content_block_delta` whose
// delta is an incremental thinking fragment. Type is always
// "thinking_delta".
type thinkingDelta struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

// contentBlockDelta is the payload for `event: content_block_delta`.
// Delta is `any` because the inner shape varies per block kind
// (textDelta for text, thinkingDelta for thinking).
type contentBlockDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta any    `json:"delta"`
}

// contentBlockStop is the payload for `event: content_block_stop`.
type contentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

// messageDeltaInner is the inner `delta` object on `event: message_delta`.
// Both StopReason and StopSequence are *string (nullable per Anthropic
// spec — emit JSON null when unknown; the SDK keys on field-present).
type messageDeltaInner struct {
	StopReason   *string `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
}

// messageDeltaUsage is the per-message-delta cumulative usage envelope.
// Phase 3.1 emits honest zeros per D-12; OutputTokens is always 0.
type messageDeltaUsage struct {
	OutputTokens int `json:"output_tokens"`
}

// messageDelta is the payload for `event: message_delta`.
type messageDelta struct {
	Type  string            `json:"type"`
	Delta messageDeltaInner `json:"delta"`
	Usage messageDeltaUsage `json:"usage"`
}

// messageStop is the payload for `event: message_stop`. Type is always
// the literal string "message_stop".
type messageStop struct {
	Type string `json:"type"`
}

// pingEvent is the payload for `event: ping`. Type is always the
// literal string "ping".
type pingEvent struct {
	Type string `json:"type"`
}

// ----------------------------------------------------------------------------
// sseEmitter — per-request streaming state machine.
//
// D-04: blockIndex / currentKind / blockOpen are mutated only inside
// the single select-loop goroutine that runs runSSEEmitterLoop.
// D-05: w + flusher are touched ONLY by writeEvent (which itself is
// only called from runSSEEmitterLoop). No mutex needed.
// ----------------------------------------------------------------------------

type sseEmitter struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	logger      *slog.Logger
	messageID   string
	model       string
	blockIndex  int
	currentKind canonical.ChunkKind
	blockOpen   bool
}

// writeEvent emits one named SSE frame using the canonical
// `event: <name>\ndata: <json>\n\n` framing + Flush. This is the ONLY
// method that touches e.w / e.flusher (D-05 single-goroutine
// invariant; T-3.1-FRAMEERR single-source-of-truth for framing).
//
// Errors are wrapped with the event name so callers can correlate
// log entries with the frame that failed. Encoder errors after
// WriteHeader cannot be reported to the client (the client has
// disconnected by definition) — they propagate up so runSSEEmitterLoop
// can tear down the goroutine cleanly.
func (e *sseEmitter) writeEvent(eventName string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("anthropic: marshal %s: %w", eventName, err)
	}
	if _, err := fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", eventName, body); err != nil {
		return fmt.Errorf("anthropic: write %s: %w", eventName, err)
	}
	e.flusher.Flush()
	return nil
}

// applyChunk processes one canonical.Chunk and emits the corresponding
// content_block_start (if a new block opens), content_block_stop (if
// the kind changed and a block was open), and content_block_delta
// frames per D-03 + D-04. Unsupported chunk kinds (ToolCall, Plan in
// Phase 3.1) are dropped with a debug log WITHOUT bumping the block
// index AND WITHOUT closing the currently-open block — the next
// supported chunk resumes the prior block (RESEARCH.md §Pattern 1
// lines 372-378 default branch + behavior bullet in 03.1-03-PLAN.md:
// "text + drop + text = single text block with content 'ok' then
// 'more'").
//
// Behavior contract:
//
//  1. Identify the block header for this chunk's kind. Unsupported
//     kinds short-circuit here with a debug log + return nil — NO
//     state change at all.
//  2. If a block is open AND the new chunk's kind differs from the
//     open block's kind: emit content_block_stop, bump blockIndex,
//     leave blockOpen=false.
//  3. If no block is open: emit content_block_start, set currentKind,
//     set blockOpen=true.
//  4. Emit content_block_delta with the kind-specific payload. Nil
//     pointer guards: when c.Text or c.Thought is nil, skip the delta
//     silently (defensive — shouldn't happen on canonical chunks but
//     adapters have been bitten before).
//
// The reorder vs. the obvious "close-then-open" flow is load-bearing:
// closing before checking kind support would bump the index on
// dropped chunks and force the next supported chunk into a new block.
func (e *sseEmitter) applyChunk(c canonical.Chunk) error {
	// Step 1: identify block header. Unsupported kinds short-circuit
	// BEFORE any state mutation — no close, no bump, no log-as-open.
	var header any
	switch c.Kind {
	case canonical.ChunkKindText:
		header = textBlockHeader{Type: "text", Text: ""}
	case canonical.ChunkKindThought:
		header = thinkingBlockHeader{Type: "thinking", Thinking: ""}
	default:
		// ChunkKindToolCall / ChunkKindPlan dormant in Phase 3.1 —
		// drop with debug log. NO state change (block stays open at
		// its current index; next supported chunk resumes the prior
		// block).
		e.logger.Debug("anthropic: sse unsupported chunk kind dropped (Phase 3.1)", "kind", c.Kind)
		return nil
	}

	// Step 2: close + bump on kind transition.
	if e.blockOpen && c.Kind != e.currentKind {
		if err := e.writeEvent("content_block_stop", contentBlockStop{
			Type:  "content_block_stop",
			Index: e.blockIndex,
		}); err != nil {
			return err
		}
		e.blockIndex++
		e.blockOpen = false
	}

	// Step 3: open new block if needed.
	if !e.blockOpen {
		if err := e.writeEvent("content_block_start", contentBlockStart{
			Type:         "content_block_start",
			Index:        e.blockIndex,
			ContentBlock: header,
		}); err != nil {
			return err
		}
		e.currentKind = c.Kind
		e.blockOpen = true
	}

	// Step 4: emit the kind-specific delta.
	switch c.Kind {
	case canonical.ChunkKindText:
		if c.Text == nil {
			return nil
		}
		return e.writeEvent("content_block_delta", contentBlockDelta{
			Type:  "content_block_delta",
			Index: e.blockIndex,
			Delta: textDelta{Type: "text_delta", Text: c.Text.Content},
		})
	case canonical.ChunkKindThought:
		if c.Thought == nil {
			return nil
		}
		return e.writeEvent("content_block_delta", contentBlockDelta{
			Type:  "content_block_delta",
			Index: e.blockIndex,
			Delta: thinkingDelta{Type: "thinking_delta", Thinking: c.Thought.Content},
		})
	}
	// Unsupported kinds short-circuited at step 1; this is defensive.
	return nil
}

// runSSEEmitter is the entry point for the SSE streaming branch. It
// asserts the http.Flusher capability BEFORE writing any bytes (a
// missing Flusher returns an error and the caller is responsible for
// rendering a JSON 500 — see handlers.go), then sets the streaming
// headers, emits the initial message_start frame, and hands off to
// runSSEEmitterLoop with the real `time.NewTicker(PingInterval)` (W7
// — uses the named const, NOT a magic literal).
//
// runHandle is consumed via runHandle.Stream() once at the top and the
// resulting channel + Result() callback live entirely inside
// runSSEEmitterLoop. Tests inject a controlled `<-chan time.Time` and
// call runSSEEmitterLoop directly to avoid the 15-second real-time
// wait (see sse_test.go).
//
// Returns nil on a clean stream completion (message_stop emitted),
// ctx.Err() on client-disconnect, the underlying error on Result()
// failure, or any wrapped writeEvent error.
func runSSEEmitter(ctx context.Context, w http.ResponseWriter, run RunHandle, model string, logger *slog.Logger) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Caller (handlers.go) is responsible for translating this into
		// a JSON 500 envelope. We have NOT written any bytes yet so the
		// caller is free to call writeError.
		return errors.New("anthropic: response writer is not flusher")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	e := &sseEmitter{
		w:         w,
		flusher:   flusher,
		logger:    logger,
		messageID: genMessageID(),
		model:     model,
	}

	// message_start: empty content, null stop_reason/stop_sequence,
	// zero usage (D-12). The message envelope SHAPE matches the
	// non-streaming response so SDK consumers see a familiar payload.
	if err := e.writeEvent("message_start", messageStart{
		Type: "message_start",
		Message: anthropicMessage{
			ID:           e.messageID,
			Type:         "message",
			Role:         "assistant",
			Model:        e.model,
			Content:      []contentBlock{},
			StopReason:   nil,
			StopSequence: nil,
			Usage:        usage{InputTokens: 0, OutputTokens: 0},
		},
	}); err != nil {
		return err
	}

	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()

	return runSSEEmitterLoop(ctx, e, run, ticker.C)
}

// runSSEEmitterLoop is the test-injectable inner loop. Production
// callers (runSSEEmitter) construct the real ticker; tests supply a
// fake `<-chan time.Time` via a manual `make(chan time.Time, 1)` and
// `tickerC <- time.Now()` to fire pings on demand without waiting
// 15 real seconds.
//
// D-05: this is the SINGLE goroutine that owns the SSE writer. The
// select on (ctx.Done, tickerC, chunks) ensures no concurrent write
// across the three event sources.
//
// On stream close (`chunks` channel closed):
//   - If a block is open, emit content_block_stop.
//   - Call run.Stream().Result(). On rerr != nil, emit
//     `event: error` via writeSSEError(errors.go) and return rerr
//     WITHOUT message_delta/message_stop. The Anthropic SDK treats
//     an error frame as terminal — emitting message_stop after it
//     confuses the client-side parser.
//   - Otherwise emit message_delta (with mapped stop_reason) +
//     message_stop and return nil.
//
// On ctx.Done: debug-log the disconnect and return ctx.Err(). No
// message_delta/message_stop is emitted — the stream tore down
// before the natural end.
func runSSEEmitterLoop(ctx context.Context, e *sseEmitter, run RunHandle, tickerC <-chan time.Time) error {
	chunks := run.Stream().Chunks()
	for {
		select {
		case <-ctx.Done():
			e.logger.Debug("anthropic: sse client disconnect", "session_id", run.SessionID())
			return fmt.Errorf("anthropic: sse ctx: %w", ctx.Err())

		case <-tickerC:
			if err := e.writeEvent("ping", pingEvent{Type: "ping"}); err != nil {
				return err
			}

		case c, ok := <-chunks:
			if !ok {
				return finalizeStream(e, run)
			}
			if err := e.applyChunk(c); err != nil {
				return err
			}
		}
	}
}

// finalizeStream emits the closing frames after the chunk channel
// closes. Extracted so the chunks-closed branch in runSSEEmitterLoop
// stays scannable.
//
// Behavior contract:
//   - blockOpen → emit content_block_stop (ignore write errors — the
//     close path is best-effort once the stream ended naturally).
//   - run.Stream().Result() error → emit `event: error` via
//     writeSSEError + return the error. No message_delta/message_stop
//     after the error frame (SDK treats error as terminal).
//   - Result() success → emit message_delta (with mapped stop_reason)
//   - message_stop + return nil.
func finalizeStream(e *sseEmitter, run RunHandle) error {
	if e.blockOpen {
		// Best-effort close; the stream is ending either way.
		_ = e.writeEvent("content_block_stop", contentBlockStop{
			Type:  "content_block_stop",
			Index: e.blockIndex,
		})
	}

	final, rerr := run.Stream().Result()
	if rerr != nil {
		// Mid-stream / terminal engine error: emit error frame and
		// return WITHOUT message_delta or message_stop. The Anthropic
		// SDK treats `event: error` as the terminal frame; emitting
		// message_stop after it would race with the SDK's error
		// dispatch and produce a confusing user-visible state.
		writeSSEError(e.w, e.flusher, errAPI, "stream terminated")
		return fmt.Errorf("anthropic: sse stream result: %w", rerr)
	}

	stopReason := canonical.StopUnknown
	if final != nil {
		stopReason = final.StopReason
	}

	if err := e.writeEvent("message_delta", messageDelta{
		Type: "message_delta",
		Delta: messageDeltaInner{
			StopReason:   mapStopReason(stopReason),
			StopSequence: nil,
		},
		Usage: messageDeltaUsage{OutputTokens: 0}, // D-12 honest zeros
	}); err != nil {
		return err
	}

	return e.writeEvent("message_stop", messageStop{Type: "message_stop"})
}
