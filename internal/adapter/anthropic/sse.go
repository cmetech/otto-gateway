package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin"
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

// toolUseBlockHeader is the content_block payload for a tool_use
// block start (Phase 6 Plan 04 Task 2, D-07 Anthropic exception).
//
// Input is *map[string]any (not map[string]any) per the CR-01
// pattern (RESEARCH Pitfall 1) — the pointer-to-empty-map is the
// load-bearing trick that makes encoding/json emit `"input":{}`
// rather than `"input":null` (the default Go encoding of a nil map)
// or dropping the field via omitempty (len==0 maps would otherwise
// vanish). Anthropic's @anthropic-ai/sdk MessageStream parser
// REJECTS null input on tool_use blocks.
type toolUseBlockHeader struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input *map[string]any `json:"input,omitempty"`
}

// inputJSONDelta is the payload for a `content_block_delta` whose
// delta carries tool_use args as a JSON string fragment. PartialJSON
// carries the full serialized args in ONE delta — kiro emits tool
// args atomically per D-06 / D-07, so no chunking is needed.
type inputJSONDelta struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
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
	// toolUseEmitted is set true once a tool_use content_block_start
	// has been written (regardless of whether a populated delta
	// followed). finalizeStream consults this flag and overrides the
	// message_delta stop_reason to "tool_use" — per Anthropic spec,
	// the SDK expects stop_reason: tool_use whenever the response
	// contains a tool_use block, regardless of the engine's mapped
	// StopReason.
	//
	// D-05 single-goroutine invariant: this field is touched ONLY
	// inside the select-loop goroutine (applyChunk -> finalizeStream).
	// No mutex needed.
	toolUseEmitted bool

	// pendingToolUseFlush guards the partial_json concat hazard
	// surfaced by pi-ai's anthropic-shared.ts parser (and the official
	// @anthropic-ai/sdk MessageStream). ACP can emit a placeholder
	// `tool_call` notification (Args=nil) as a block-header
	// announcement, followed by a `tool_call_chunk` carrying the
	// actual args; both translate to canonical.ChunkKindToolCall with
	// the same toolCallId. Emitting `input_json_delta` for BOTH
	// produces `partial_json:"{}"` then `partial_json:"{...}"` on the
	// wire, and the SDK parser tries to parse `{}{...}` as a single
	// JSON value — failing at position 2.
	//
	// Discipline: at most ONE input_json_delta per tool_use content
	// block, carrying the FINAL populated args. Placeholder chunks
	// open the block but defer the delta (this flag goes true). The
	// next populated chunk emits the delta and clears the flag. If
	// the block closes (kind transition OR finalizeStream) with the
	// flag still set — a zero-arg tool, or a stream that only ever
	// sent placeholders — flushPendingToolUseIfNeeded emits one
	// `partial_json:"{}"` so the SDK parser has exactly one
	// well-formed JSON value to parse.
	//
	// D-05 single-goroutine invariant applies.
	pendingToolUseFlush bool

	// Quick 260530-df2 — streaming aggregator state. Mirrors
	// CollectAnthropicChat (collect.go:75-119) so the canonical
	// response handed to RunPostHooks after stream completion has the
	// SAME content shape the non-streaming path produces — text +
	// thinking + tool_use parts on Message.Content, ToolCalls populated
	// per the D-07 Anthropic exception.
	//
	// The load-bearing correctness risk identified in the plan is
	// aggregator richness: if these fields are stop-reason-only,
	// post_chain_out records ship empty content[] and chat-trace.log
	// loses its entire response-side observation product value.
	//
	// D-05 single-goroutine invariant applies — only applyChunk and
	// aggregatedResponse touch these fields, both inside the
	// runSSEEmitterLoop goroutine.
	aggText      strings.Builder
	aggThought   strings.Builder
	aggToolParts []canonical.ContentPart
	aggToolCalls []canonical.ToolCall
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
	case canonical.ChunkKindToolCall:
		// Phase 6 Plan 04 Task 2 (D-07 Anthropic exception): kiro-native
		// tool_call chunks render as NATIVE tool_use content blocks on
		// the Anthropic wire (NOT as `[tool: <name>]\n` narration text
		// like Ollama/OpenAI). Defensive: drop if ToolCall payload is
		// nil (shouldn't happen on canonical chunks but adapters have
		// been bitten by nil pointers before).
		if c.ToolCall == nil {
			e.logger.Debug("anthropic: sse tool_call chunk with nil payload; dropping")
			return nil
		}
		// CR-01 (Pitfall 1): pointer-to-empty-map preserves
		// `"input":{}` through encoding/json.omitempty rather than
		// emitting `"input":null` (default for nil map) or dropping
		// the field (default for len==0 map). The pointer indirection
		// is the load-bearing trick — do NOT shortcut to
		// `map[string]any{}` directly.
		emptyMap := map[string]any{}
		header = toolUseBlockHeader{
			Type:  "tool_use",
			ID:    c.ToolCall.ID,
			Name:  c.ToolCall.Name,
			Input: &emptyMap,
		}
	default:
		// ChunkKindPlan dormant in Phase 6 — drop with debug log. NO
		// state change (block stays open at its current index; next
		// supported chunk resumes the prior block).
		e.logger.Debug("anthropic: sse unsupported chunk kind dropped", "kind", c.Kind)
		return nil
	}

	// Step 2: close + bump on kind transition.
	if e.blockOpen && c.Kind != e.currentKind {
		// Zero-arg / placeholder-only flush: if the closing block is a
		// tool_use that never received populated args, emit one
		// `partial_json:"{}"` delta so the SDK parser doesn't choke on
		// an empty accumulator. No-op for text/thought blocks.
		if err := e.flushPendingToolUseIfNeeded(); err != nil {
			return err
		}
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
		// A tool_use block has now been declared on the wire — the
		// stop_reason finalizer override fires regardless of whether
		// a populated input_json_delta follows. Set the pending-flush
		// flag so an unaccompanied placeholder still flushes "{}".
		if c.Kind == canonical.ChunkKindToolCall {
			e.toolUseEmitted = true
			e.pendingToolUseFlush = true
		}
	}

	// Step 4: emit the kind-specific delta.
	switch c.Kind {
	case canonical.ChunkKindText:
		if c.Text == nil {
			return nil
		}
		if err := e.writeEvent("content_block_delta", contentBlockDelta{
			Type:  "content_block_delta",
			Index: e.blockIndex,
			Delta: textDelta{Type: "text_delta", Text: c.Text.Content},
		}); err != nil {
			return err
		}
		// Quick 260530-df2 — mirror CollectAnthropicChat: accumulate
		// successful text deltas so the canonical response handed to
		// RunPostHooks carries the concatenated content.
		e.aggText.WriteString(c.Text.Content)
		return nil
	case canonical.ChunkKindThought:
		if c.Thought == nil {
			return nil
		}
		if err := e.writeEvent("content_block_delta", contentBlockDelta{
			Type:  "content_block_delta",
			Index: e.blockIndex,
			Delta: thinkingDelta{Type: "thinking_delta", Thinking: c.Thought.Content},
		}); err != nil {
			return err
		}
		// Quick 260530-df2 — mirror CollectAnthropicChat: thinking text
		// accumulates separately from regular text so the canonical
		// response's Content[1] thinking part is populated.
		e.aggThought.WriteString(c.Thought.Content)
		return nil
	case canonical.ChunkKindToolCall:
		// Phase 6 Plan 04 Task 2 (D-07): emit at most ONE
		// content_block_delta carrying the full args as
		// input_json_delta.partial_json per tool_use block. kiro emits
		// tool args atomically per D-06 — but ACP can announce the
		// block via a `tool_call` notification (Args=nil) FIRST and
		// then deliver the payload via `tool_call_chunk` for the same
		// toolCallId. Both translate to ChunkKindToolCall; the SDK
		// parser concatenates partial_json deltas and would parse
		// `{}{...}` as a single value — failing at position 2.
		//
		// Discipline: placeholder chunks (Args nil/empty) defer the
		// delta via pendingToolUseFlush (set in step 3 on block
		// open). The first populated chunk emits the delta and
		// clears the flag. Subsequent populated chunks for the same
		// block are dropped (D-06 atomicity invariant — first wins).
		// Block close (kind transition or finalizeStream) flushes a
		// single `{}` delta if the flag is still set.
		if c.ToolCall == nil {
			return nil
		}
		hasArgs := len(c.ToolCall.Args) > 0
		if !hasArgs {
			// Placeholder/announcement: block opened via step 3 with
			// `"input":{}` in content_block_start. Defer the delta.
			e.logger.Debug("anthropic: sse tool_call placeholder; deferring partial_json",
				"id", c.ToolCall.ID, "name", c.ToolCall.Name)
			return nil
		}
		if !e.pendingToolUseFlush {
			// A populated delta has already been emitted for this
			// block. Emitting another would concatenate on the SDK
			// side and break parsing. kiro is contracted to emit
			// args atomically (D-06); this guards against a
			// misbehaving stream and against future regressions of
			// the placeholder-then-populated shape.
			e.logger.Debug("anthropic: sse tool_call subsequent populated chunk dropped (D-06 atomicity)",
				"id", c.ToolCall.ID, "name", c.ToolCall.Name)
			return nil
		}
		argsJSON, err := json.Marshal(c.ToolCall.Args)
		if err != nil {
			// Defensive: if Args contains a pathological value
			// (chan, function), encoding/json will fail. Log + drop
			// the delta rather than tear down the whole stream.
			// pendingToolUseFlush stays true so a `{}` flushes at
			// block close — the SDK parser never sees a missing
			// delta.
			e.logger.Debug("anthropic: sse tool_call args marshal failed; dropping delta",
				"err", err, "name", c.ToolCall.Name)
			return nil
		}
		if err := e.writeEvent("content_block_delta", contentBlockDelta{
			Type:  "content_block_delta",
			Index: e.blockIndex,
			Delta: inputJSONDelta{Type: "input_json_delta", PartialJSON: string(argsJSON)},
		}); err != nil {
			return err
		}
		e.pendingToolUseFlush = false
		// Quick 260530-df2 — populated tool_use chunk: append a
		// ContentKindToolUse part AND a Message.ToolCall entry so the
		// canonical response handed to RunPostHooks mirrors
		// CollectAnthropicChat's D-07 shape (collect.go:92-115).
		e.aggToolParts = append(e.aggToolParts, canonical.ContentPart{
			Kind: canonical.ContentKindToolUse,
			ToolUse: &canonical.ToolUsePart{
				ID:    c.ToolCall.ID,
				Name:  c.ToolCall.Name,
				Input: c.ToolCall.Args,
			},
		})
		e.aggToolCalls = append(e.aggToolCalls, canonical.ToolCall{
			ID:        c.ToolCall.ID,
			Name:      c.ToolCall.Name,
			Arguments: c.ToolCall.Args,
		})
		return nil
	}
	// Unsupported kinds short-circuited at step 1; this is defensive.
	return nil
}

// aggregatedResponse builds the canonical.ChatResponse from the
// per-stream aggregator state. Mirrors
// assembleAnthropicChatResponse (collect.go:147-181) — text part
// ALWAYS present at Content[0], thinking appended when non-empty,
// tool_use parts appended after; Message.ToolCalls populated
// separately per the D-07 Anthropic exception.
//
// Quick 260530-df2: this method exists so handlers.go can hand
// the aggregated response to RunPostHooks after the streaming branch
// completes. Without it, LoggingHook.After observes an empty resp and
// chat-trace.log's post_chain_out record has no content[].
func (e *sseEmitter) aggregatedResponse(req *canonical.ChatRequest, stop canonical.StopReason) *canonical.ChatResponse {
	model := ""
	if req != nil {
		model = req.Model
	}
	content := []canonical.ContentPart{
		{Kind: canonical.ContentKindText, Text: e.aggText.String()},
	}
	if e.aggThought.Len() > 0 {
		content = append(content, canonical.ContentPart{
			Kind: canonical.ContentKindThinking,
			Text: e.aggThought.String(),
		})
	}
	content = append(content, e.aggToolParts...)
	return &canonical.ChatResponse{
		Model: model,
		Message: canonical.Message{
			Role:      canonical.RoleAssistant,
			Content:   content,
			ToolCalls: e.aggToolCalls,
		},
		StopReason: stop,
	}
}

// flushPendingToolUseIfNeeded emits a single `partial_json:"{}"`
// input_json_delta when a tool_use block is closing without ever
// receiving a populated content_block_delta. Two cases trigger this:
//
//  1. Zero-arg tool calls (the tool takes no parameters; kiro emits
//     one `tool_call` notification with Args=nil and never follows
//     up with a `tool_call_chunk`).
//  2. A stream that emits only placeholder chunks for a block — the
//     deferred-delta discipline in applyChunk skipped every one.
//
// Without this flush, pi-ai's anthropic-shared.ts (and the official
// @anthropic-ai/sdk MessageStream) would see an empty accumulator
// on content_block_stop and JSON.parse("") would throw. Emitting
// `{}` gives the parser exactly one well-formed value — an empty
// input object, matching the SDK's expectation for parameterless
// tool calls.
//
// No-op when pendingToolUseFlush is false (a populated delta already
// emitted) OR when the closing block is not a tool_use (defensive —
// the flag is only ever set for tool_use blocks).
func (e *sseEmitter) flushPendingToolUseIfNeeded() error {
	if !e.pendingToolUseFlush {
		return nil
	}
	e.pendingToolUseFlush = false
	if e.currentKind != canonical.ChunkKindToolCall {
		// Defensive: flag should only be set inside a tool_use block.
		return nil
	}
	return e.writeEvent("content_block_delta", contentBlockDelta{
		Type:  "content_block_delta",
		Index: e.blockIndex,
		Delta: inputJSONDelta{Type: "input_json_delta", PartialJSON: "{}"},
	})
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
// Returns the aggregated canonical response (built from chunk
// aggregation; non-nil on both clean completion AND mid-stream errors
// / ctx-cancel so operators can still observe partial results via
// PostHooks — quick 260530-df2) alongside the error.
//
// Error contract: nil error on clean stream completion (message_stop
// emitted), ctx.Err() on client-disconnect, the underlying error on
// Result() failure, or any wrapped writeEvent error. The Flusher-
// assertion failure short-circuits BEFORE any aggregation work, so
// the response is nil in that single case.
func runSSEEmitter(ctx context.Context, w http.ResponseWriter, run RunHandle, model string, streamIdle time.Duration, logger *slog.Logger) (*canonical.ChatResponse, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Caller (handlers.go) is responsible for translating this into
		// a JSON 500 envelope. We have NOT written any bytes yet so the
		// caller is free to call writeError.
		return nil, errors.New("anthropic: response writer is not flusher")
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
		// Even on message_start write failure we return the (empty)
		// aggregated response so handlers can observe the failed
		// request via PostHooks. StopUnknown matches the empty-content
		// case.
		return e.aggregatedResponse(nil, canonical.StopUnknown), err
	}

	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()

	return runSSEEmitterLoop(ctx, e, run, ticker.C, streamIdle)
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
func runSSEEmitterLoop(ctx context.Context, e *sseEmitter, run RunHandle, tickerC <-chan time.Time, streamIdle time.Duration) (*canonical.ChatResponse, error) {
	// firstChunkSeen is a one-shot guard so the anthropic.sse.first_chunk
	// DEBUG marker fires exactly once per stream — a high-throughput
	// response must not flood the log. Stack-local, no cross-stream state.
	var firstChunkSeen bool
	chunks := run.Stream().Chunks()

	// Quick 260531-ruv — idle watchdog arm. A nil idleC is a never-ready
	// channel (stdlib idiom); when streamIdle == 0 the case never fires
	// and the loop matches the legacy 3-arm select exactly. Drain-safe
	// Stop/Reset on chunk arrival; matches engine.RangeChunksWithIdleTimeout
	// semantics verbatim so the per-surface behavior cannot drift.
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
			// Quick 260530-df2 — disconnect path: return the partial
			// aggregated response so handlers can fire PostHooks with
			// whatever was assembled. Operators want forensics +
			// duration_ms even on disconnect (T-df2-03 sync.Map leak
			// mitigation requires the After call to LoadAndDelete).
			e.logger.Debug("anthropic: sse client disconnect", "session_id", run.SessionID())
			return e.aggregatedResponse(nil, canonical.StopUnknown), fmt.Errorf("anthropic: sse ctx: %w", ctx.Err())

		case <-idleC:
			// Quick 260531-ruv — stream-idle fire. Emit an Anthropic-
			// shaped event:error frame, WARN-log with the canonical
			// attr set, and return a wrapped ErrStreamIdleTimeout so
			// errors.Is(err, engine.ErrStreamIdleTimeout) is true at
			// the handler. The aggregated response is non-nil so
			// PostHooks can still observe forensics.
			e.logger.Warn(
				"stream.idle_timeout",
				"surface", "anthropic",
				"session_id", run.SessionID(),
				"elapsed_ms", streamIdle.Milliseconds(),
				"request_id", plugin.RequestIDFromContext(ctx),
			)
			writeSSEError(e.w, e.flusher, errAPI,
				fmt.Sprintf("upstream stream idle for %ds", int(streamIdle.Seconds())))
			return e.aggregatedResponse(nil, canonical.StopUnknown),
				fmt.Errorf("anthropic: sse %w", canonical.ErrStreamIdleTimeout)

		case <-tickerC:
			if err := e.writeEvent("ping", pingEvent{Type: "ping"}); err != nil {
				return e.aggregatedResponse(nil, canonical.StopUnknown), err
			}

		case c, ok := <-chunks:
			if !ok {
				return finalizeStream(e, run)
			}
			if !firstChunkSeen {
				firstChunkSeen = true
				e.logger.Debug("anthropic.sse.first_chunk", "session_id", run.SessionID(), "kind", c.Kind)
			}
			if err := e.applyChunk(c); err != nil {
				return e.aggregatedResponse(nil, canonical.StopUnknown), err
			}
			// Quick 260531-ruv — drain-safe reset on chunk arrival.
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
func finalizeStream(e *sseEmitter, run RunHandle) (*canonical.ChatResponse, error) {
	if e.blockOpen {
		// Best-effort close; the stream is ending either way.
		// Flush a `partial_json:"{}"` first if the closing block is a
		// tool_use that never received populated args — the SDK
		// parser otherwise sees an empty accumulator on
		// content_block_stop and fails.
		_ = e.flushPendingToolUseIfNeeded()
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
		//
		// Quick 260530-df2: still return the partial aggregated
		// response so handlers can fire PostHooks on the terminal-
		// error path. Operators want forensics on partial completion.
		writeSSEError(e.w, e.flusher, errAPI, "stream terminated")
		return e.aggregatedResponse(nil, canonical.StopUnknown), fmt.Errorf("anthropic: sse stream result: %w", rerr)
	}

	// D-06 teardown: prevent watchdog from firing spurious Cancel after natural
	// stream completion. Note: Anthropic is explicitly EXEMPT from D-05
	// (CONTEXT.md) — the Anthropic spec mandates `event: error` on terminal
	// stream errors, so the error path (rerr != nil) above does NOT call
	// stop(). Only the rerr == nil success path calls stop() so the watchdog
	// still fires session/cancel on Anthropic stream errors/truncation.
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}

	stopReason := canonical.StopUnknown
	if final != nil {
		stopReason = final.StopReason
	}

	// Phase 6 Plan 04 Task 2 (D-07): if a tool_use content_block_delta
	// was emitted during this stream, override the wire stop_reason to
	// "tool_use" regardless of the engine's mapped StopReason.
	// Anthropic spec mandates this — the SDK keys its tool-use
	// dispatch on stop_reason:"tool_use" in message_delta, and any
	// other value would break loop24-client's flow control.
	var mappedStop *string
	if e.toolUseEmitted {
		s := "tool_use"
		mappedStop = &s
	} else {
		mappedStop = mapStopReason(stopReason)
	}

	if err := e.writeEvent("message_delta", messageDelta{
		Type: "message_delta",
		Delta: messageDeltaInner{
			StopReason:   mappedStop,
			StopSequence: nil,
		},
		Usage: messageDeltaUsage{OutputTokens: 0}, // D-12 honest zeros
	}); err != nil {
		return e.aggregatedResponse(nil, stopReason), err
	}

	if err := e.writeEvent("message_stop", messageStop{Type: "message_stop"}); err != nil {
		return e.aggregatedResponse(nil, stopReason), err
	}
	// Quick 260530-df2 — clean stream completion: return the fully-
	// aggregated canonical response so handlers.go (after Task 2 step 3)
	// hands it to eng.RunPostHooks. Without this, LoggingHook.After
	// observes nothing and chat-trace.log's post_chain_out is missing.
	return e.aggregatedResponse(nil, stopReason), nil
}

// runSyntheticSSEFromResponse writes the aggregated *canonical.ChatResponse
// as a one-shot synthetic SSE stream. Used by the encrypt-mode re-route
// branch in handlers.go: when a Pre hook flipped req.Stream=false during
// eng.Run but the original wire request had stream=true, the client is
// still expecting text/event-stream. Writing application/json here would
// trip the SDK with "request ended without sending any chunks".
//
// Sequence emitted:
//
//   - message_start (envelope with empty content, null stop_reason, zero
//     usage — same shape as the real streaming emitter)
//   - For each ContentPart in resp.Message.Content:
//   - text: content_block_start(text "") + content_block_delta(text_delta
//     carrying the full text) + content_block_stop
//   - thinking: same shape with thinking blocks
//   - tool_use: content_block_start(tool_use placeholder) + content_block_delta
//     (input_json_delta carrying the full marshaled input) + content_block_stop
//   - message_delta with mapped stop_reason (tool_use override applied
//     when any tool_use block was emitted, matching the streaming emitter)
//   - message_stop
//
// The block index walks 0..N-1 in registration order. Empty content
// degrades to a single empty text block so SDK consumers always see at
// least one content_block_* pair (matches chatResponseToMessage).
//
// PostHooks are NOT fired here — handlers.go (Quick 260530-df2 pattern)
// calls eng.RunPostHooks separately after this returns, so LoggingHook
// and ChatTraceHook still observe every request.
func runSyntheticSSEFromResponse(_ context.Context, w http.ResponseWriter, resp *canonical.ChatResponse, requestedModel string, logger *slog.Logger) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("anthropic: response writer is not flusher")
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
		w:         w,
		flusher:   flusher,
		logger:    logger,
		messageID: genMessageID(),
		model:     model,
	}

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

	stopReason := canonical.StopUnknown
	hasToolUse := false

	if resp != nil {
		stopReason = resp.StopReason
		index := 0
		for _, part := range resp.Message.Content {
			switch part.Kind {
			case canonical.ContentKindText:
				if err := e.writeEvent("content_block_start", contentBlockStart{
					Type:         "content_block_start",
					Index:        index,
					ContentBlock: textBlockHeader{Type: "text", Text: ""},
				}); err != nil {
					return err
				}
				if err := e.writeEvent("content_block_delta", contentBlockDelta{
					Type:  "content_block_delta",
					Index: index,
					Delta: textDelta{Type: "text_delta", Text: part.Text},
				}); err != nil {
					return err
				}
				if err := e.writeEvent("content_block_stop", contentBlockStop{
					Type: "content_block_stop", Index: index,
				}); err != nil {
					return err
				}
				index++
			case canonical.ContentKindThinking:
				if err := e.writeEvent("content_block_start", contentBlockStart{
					Type:         "content_block_start",
					Index:        index,
					ContentBlock: thinkingBlockHeader{Type: "thinking", Thinking: ""},
				}); err != nil {
					return err
				}
				if err := e.writeEvent("content_block_delta", contentBlockDelta{
					Type:  "content_block_delta",
					Index: index,
					Delta: thinkingDelta{Type: "thinking_delta", Thinking: part.Text},
				}); err != nil {
					return err
				}
				if err := e.writeEvent("content_block_stop", contentBlockStop{
					Type: "content_block_stop", Index: index,
				}); err != nil {
					return err
				}
				index++
			case canonical.ContentKindToolUse:
				if part.ToolUse == nil {
					continue
				}
				hasToolUse = true
				emptyInput := map[string]any{}
				if err := e.writeEvent("content_block_start", contentBlockStart{
					Type:  "content_block_start",
					Index: index,
					ContentBlock: toolUseBlockHeader{
						Type:  "tool_use",
						ID:    part.ToolUse.ID,
						Name:  part.ToolUse.Name,
						Input: &emptyInput,
					},
				}); err != nil {
					return err
				}
				inputBytes, mErr := json.Marshal(part.ToolUse.Input)
				if mErr != nil {
					inputBytes = []byte("{}")
				}
				if err := e.writeEvent("content_block_delta", contentBlockDelta{
					Type:  "content_block_delta",
					Index: index,
					Delta: inputJSONDelta{Type: "input_json_delta", PartialJSON: string(inputBytes)},
				}); err != nil {
					return err
				}
				if err := e.writeEvent("content_block_stop", contentBlockStop{
					Type: "content_block_stop", Index: index,
				}); err != nil {
					return err
				}
				index++
			default:
				// Image / ToolResult are inbound-only; defensive skip.
			}
		}
		// chatResponseToMessage degrades empty content to a single empty
		// text block so SDKs always see content_block_start/stop. Match.
		if index == 0 {
			if err := e.writeEvent("content_block_start", contentBlockStart{
				Type:         "content_block_start",
				Index:        0,
				ContentBlock: textBlockHeader{Type: "text", Text: ""},
			}); err != nil {
				return err
			}
			if err := e.writeEvent("content_block_stop", contentBlockStop{
				Type: "content_block_stop", Index: 0,
			}); err != nil {
				return err
			}
		}
	}

	var mappedStop *string
	if hasToolUse {
		// Streaming emitter override: tool_use blocks force stop_reason
		// "tool_use" regardless of the engine's mapped value. Mirror that
		// here so synthetic SSE matches real SSE on this load-bearing
		// field (Anthropic SDK keys on it for tool-use detection).
		s := "tool_use"
		mappedStop = &s
	} else {
		mappedStop = mapStopReason(stopReason)
	}

	if err := e.writeEvent("message_delta", messageDelta{
		Type: "message_delta",
		Delta: messageDeltaInner{
			StopReason:   mappedStop,
			StopSequence: nil,
		},
		Usage: messageDeltaUsage{OutputTokens: 0},
	}); err != nil {
		return err
	}

	return e.writeEvent("message_stop", messageStop{Type: "message_stop"})
}
