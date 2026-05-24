package anthropic

import (
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
//     - true: Engine.Run → runSSEEmitter (real Plan 03.1-03 emitter
//     in sse.go — Plan 02 stub deleted).
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
			// Engine.Run failed BEFORE any SSE headers were written —
			// respond with a normal JSON 500 envelope (T-02-33: never
			// echo err.Error() which may contain request fragments).
			a.cfg.Logger.Error("anthropic: engine.Run error", "err", err)
			writeError(w, http.StatusInternalServerError, errAPI, "internal error")
			return
		}
		if err := runSSEEmitter(r.Context(), w, runHandle, wire.Model, a.cfg.Logger); err != nil {
			// runSSEEmitter has already written SSE headers + frames
			// (the error path inside the emitter handles its own
			// `event: error` frame on mid-stream Result() errors —
			// see sse.go finalizeStream). Log here for observability;
			// the response body is whatever the emitter produced
			// before the error (we cannot send a JSON 500 envelope
			// after WriteHeader). ctx cancel is a normal disconnect,
			// not an error — but still useful to log at debug.
			a.cfg.Logger.Debug("anthropic: sse emitter terminated", "err", err)
		}
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
