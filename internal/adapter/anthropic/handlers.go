package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// messagesBodyCap is the maximum acceptable body size for POST
// /v1/messages — 4 MiB. Matches Phase 2 Ollama chat body cap. Exceed
// → 413 request_too_large per D-20.
const messagesBodyCap int64 = 4 << 20

// handleMessages is the POST /v1/messages handler. Implements ANTH-01
// (response shape), ANTH-04 (header validation + accept-and-ignore
// beta), ANTH-06 (error envelope + 8 status codes), ANTH-05 (system
// normalization via wireToChatRequest), ANTH-07 (inbound thinking
// preservation + non-streaming outbound thinking).
//
// Flow:
//  1. Nil-engine guard → 503 errAPI ("kiro-cli not configured").
//  2. D-07: anthropic-version header check → 400 errInvalidRequest.
//  3. D-10: anthropic-beta header debug-log + ignore.
//  4. decodeJSONBody with 4 MiB cap. 413 errRequestTooLarge on cap;
//     400 errInvalidRequest on syntactic error (sanitized — never
//     echo the raw body content per T-02-33).
//  5. Field validation: model / max_tokens / messages non-empty.
//  6. wireToChatRequest builds canonical request.
//  7. Branch on wire.Stream:
//     - false (or absent): Engine.Collect → chatResponseToMessage → JSON.
//     - true: Engine.Run → runSSEEmitterStub (Plan 03 replaces).
//  8. T-02-33: engine errors are LOGGED via slog.Error and rendered
//     as 500 errAPI with the generic message "internal error" —
//     never echo err.Error() which may contain request fragments.
func (a *Adapter) handleMessages(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Engine == nil {
		writeError(w, http.StatusServiceUnavailable, errAPI, "kiro-cli not configured (set KIRO_CMD)")
		return
	}

	// D-07: anthropic-version required.
	if r.Header.Get("anthropic-version") == "" {
		writeError(w, http.StatusBadRequest, errInvalidRequest, "anthropic-version header is required")
		return
	}

	// D-10: anthropic-beta accept-and-ignore + debug log.
	if beta := r.Header.Get("anthropic-beta"); beta != "" {
		a.cfg.Logger.Debug("anthropic: accepted-and-ignored anthropic-beta", "value", beta)
	}

	// Body decode with 4 MiB cap.
	var wire anthropicMessagesRequest
	if err := decodeJSONBody(w, r, messagesBodyCap, &wire); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, errRequestTooLarge, "request body exceeds maximum size")
			return
		}
		// T-02-33: include the decoder's error (which is syntactic —
		// e.g., "invalid character 'x' at offset N") but NOT the raw
		// body content. json.NewDecoder errors do not embed body
		// content per stdlib invariants.
		writeError(w, http.StatusBadRequest, errInvalidRequest, "invalid JSON: "+err.Error())
		return
	}

	// Field validation per Anthropic spec.
	if wire.Model == "" {
		writeError(w, http.StatusBadRequest, errInvalidRequest, "`model` is required")
		return
	}
	if wire.MaxTokens <= 0 {
		writeError(w, http.StatusBadRequest, errInvalidRequest, "`max_tokens` is required and must be > 0")
		return
	}
	if len(wire.Messages) == 0 {
		writeError(w, http.StatusBadRequest, errInvalidRequest, "`messages` is required and must be a non-empty array")
		return
	}

	req := wireToChatRequest(&wire, r, a.cfg.Logger)

	if wire.Stream {
		runHandle, err := a.cfg.Engine.Run(r.Context(), req)
		if err != nil {
			a.cfg.Logger.Error("anthropic: engine.Run error", "err", err)
			writeError(w, http.StatusInternalServerError, errAPI, "internal error")
			return
		}
		runSSEEmitterStub(r.Context(), w, runHandle, wire.Model, a.cfg.Logger)
		return
	}

	resp, err := a.cfg.Engine.Collect(r.Context(), req)
	if err != nil {
		// T-02-33: log the raw error structurally; respond with a
		// neutral generic message that cannot echo request content.
		a.cfg.Logger.Error("anthropic: engine.Collect error", "err", err)
		writeError(w, http.StatusInternalServerError, errAPI, "internal error")
		return
	}

	writeJSON(w, chatResponseToMessage(resp, wire.Model))
}

// runSSEEmitterStub is the forward-compatible Plan 03.1-02 placeholder
// for the SSE streaming branch. Emits ONLY the message_start event so
// the corresponding handler test (TestHandleMessages_StreamingBranchPlaceholder)
// asserts on properties Plan 03 will preserve byte-for-byte:
//
//   - Content-Type: text/event-stream header set
//   - First emitted SSE line: "event: message_start"
//
// Plan 03.1-03 deletes this entire function and replaces with the real
// sse.go runSSEEmitter — the placeholder test name signals the swap
// site, and Plan 03's emitter also emits message_start first so the
// forward-compatible asserts continue to hold.
//
// runHandle is consumed only by calling Stream().Result() to drain it
// — Plan 03 reads chunks here; Plan 02 ignores them. This drain is
// load-bearing for goleak: an unconsumed Stream would leak the
// engine's chunk-feeding goroutine.
func runSSEEmitterStub(ctx context.Context, w http.ResponseWriter, runHandle RunHandle, model string, logger *slog.Logger) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Defensive — should not happen with httptest.NewRecorder
		// (which does implement Flusher) or production net/http.
		writeError(w, http.StatusInternalServerError, errAPI, "response writer does not support flushing")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Build a minimal-viable message_start payload. Plan 03's real
	// emitter produces a richer payload (real message ID + full
	// metadata) but the event NAME and the framing pattern stay
	// identical, so this placeholder is forward-compatible.
	startPayload := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            "msg_stub",
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	}
	body, err := json.Marshal(startPayload)
	if err != nil {
		// Cannot recover after WriteHeader; flush whatever we can and
		// let the connection close.
		body = []byte(`{"type":"message_start"}`)
	}
	_, _ = fmt.Fprintf(w, "event: message_start\ndata: %s\n\n", body)
	flusher.Flush()

	// Drain the stream so the underlying goroutine completes — this
	// prevents goleak from flagging an unconsumed channel. Plan 03's
	// real emitter does the same drain but as part of its
	// content_block_* event loop.
	stream := runHandle.Stream()
	chunkCount := 0
	for range stream.Chunks() {
		chunkCount++ // discard chunk payload; count for debug log only
	}
	logger.Debug("anthropic: sse stub drained chunks (Plan 02 placeholder)", "count", chunkCount)
	_, _ = stream.Result()

	// Honor ctx cancellation by NOT emitting message_stop when the
	// client has disconnected. (Plan 03 owns the full message_stop
	// path — emitting it here as a "polite" stub frame is risky
	// because the real emitter's payload may differ.)
	if ctx.Err() != nil {
		logger.Debug("anthropic: sse stub — client disconnect during drain", "err", ctx.Err())
	}
}
