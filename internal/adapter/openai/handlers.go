package openai

import (
	"net/http"
)

// handleChatCompletions handles POST /chat/completions.
//
// Flow:
//  1. Nil-engine guard → 503 errAPI ("kiro-cli not configured").
//  2. decodeJSONBody with 4 MiB cap. 413 errRequestTooLarge on cap;
//     400 errInvalidRequest on syntactic error (sanitized — include
//     decoder's syntactic message, NOT raw body content per T-02-33).
//  3. Empty messages → 400 errInvalidRequest.
//  4. wireToChatRequest builds the canonical request.
//  5. Branch on wire.Stream:
//     - true: engine.Run → runSSEEmitter (Pi/SC2 path).
//     - false: engine.Collect → chatResponseToCompletion → JSON.
//  6. T-02-33: engine errors are logged raw via slog.Error then rendered
//     as 500 errAPI with the generic message "internal error" — NEVER
//     echo err.Error() which may contain request fragments.
func (a *Adapter) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Engine == nil {
		writeError(w, http.StatusServiceUnavailable, errAPI, "kiro-cli not configured (set KIRO_CMD)")
		return
	}

	var wire chatCompletionRequest
	if err := decodeJSONBody(w, r, chatBodyCap, &wire); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, errRequestTooLarge, "request body exceeds maximum size")
			return
		}
		// T-02-33: include the decoder's syntactic error (which is safe —
		// e.g., "invalid character 'x' at offset N") but NOT raw body content.
		// json.NewDecoder errors do not embed body content per stdlib invariants.
		writeError(w, http.StatusBadRequest, errInvalidRequest, "invalid JSON: "+err.Error())
		return
	}

	if len(wire.Messages) == 0 {
		writeError(w, http.StatusBadRequest, errInvalidRequest, "`messages` is required and must be a non-empty array")
		return
	}

	req := wireToChatRequest(&wire, r)

	if wire.Stream {
		// Streaming path (Pi/SC2 use case — Pi hard-codes stream:true).
		runHandle, err := a.cfg.Engine.Run(r.Context(), req)
		if err != nil {
			// engine.Run failed BEFORE any SSE headers were written — safe to
			// respond with a normal JSON 500 envelope (T-02-33: log raw, generic message).
			a.cfg.Logger.Error("openai: engine.Run error", "err", err)
			writeError(w, http.StatusInternalServerError, errAPI, "internal error")
			return
		}
		if err := runSSEEmitter(r.Context(), w, runHandle, wire.Model, a.cfg.Logger); err != nil {
			// runSSEEmitter has already written SSE headers + at least some frames.
			// We cannot send a JSON 500 after WriteHeader; log at debug and let
			// the truncated stream stand (Pitfall 3 / A5).
			a.cfg.Logger.Debug("openai: sse emitter terminated", "err", err)
		}
		return
	}

	// Non-streaming path (SC1 curl use case).
	resp, err := a.cfg.Engine.Collect(r.Context(), req)
	if err != nil {
		// T-02-33: log raw error, respond with generic message.
		a.cfg.Logger.Error("openai: engine.Collect error", "err", err)
		writeError(w, http.StatusInternalServerError, errAPI, "internal error")
		return
	}

	writeJSON(w, chatResponseToCompletion(resp, wire.Model))
}
