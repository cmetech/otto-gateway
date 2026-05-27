package openai

import (
	"context"
	"errors"
	"net/http"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/session"
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

	// Plan 05-03 D-04..D-11: X-Session-Id branch.
	eng, entry, sErr := a.resolveEngine(r)
	if sErr != nil {
		a.writeSessionError(w, sErr)
		return
	}
	if entry != nil {
		entry.Mu.Lock()
		// CR-01 fix: Unlock registers FIRST (runs LAST), MarkUsed
		// SECOND (runs FIRST). MarkUsed writes Entry.LastUsed and must
		// run UNDER entry.Mu so the reaper's TryLock-guarded read sees
		// the post-stream value.
		defer entry.Mu.Unlock()
		defer entry.MarkUsed()
	}

	if wire.Stream {
		// Streaming path (Pi/SC2 use case — Pi hard-codes stream:true).
		// D-07: create a derived context so that a write failure in
		// runSSEEmitter cancels the derived ctx (via defer cancelFn), which
		// the D-06 watchdog observes and translates into session/cancel.
		//
		// Phase 6 (REVIEW HIGH #1 + iteration-3 sawKiroNativeToolCall):
		// the streaming branch threads the canonical `req` pointer down
		// to runSSEEmitter. Streaming coerce lives in sse.go — see
		// REVIEW HIGH #1. engine.CoerceToolCall must run AFTER all text
		// deltas accumulate, before the terminal finish_reason frame
		// composes. Iteration 3: sse.go also tracks sawKiroNativeToolCall
		// and skips coerce when true (prevents the iteration-2 double-fire
		// regression).
		ctx, cancelFn := context.WithCancel(r.Context())
		defer cancelFn()
		runHandle, err := eng.Run(ctx, req)
		if err != nil {
			// engine.Run failed BEFORE any SSE headers were written — safe to
			// respond with a normal JSON 500 envelope (T-02-33: log raw, generic message).
			a.cfg.Logger.Error("openai: engine.Run error", "err", err)
			writeError(w, http.StatusInternalServerError, errAPI, "internal error")
			return
		}
		if err := runSSEEmitter(ctx, w, runHandle, req, wire.Model, a.cfg.Logger); err != nil {
			// runSSEEmitter has already written SSE headers + at least some frames.
			// We cannot send a JSON 500 after WriteHeader; log at debug and let
			// the truncated stream stand (Pitfall 3 / A5).
			a.cfg.Logger.Debug("openai: sse emitter terminated", "err", err)
		}
		return
	}

	// Non-streaming path (SC1 curl use case).
	resp, err := eng.Collect(r.Context(), req)
	if err != nil {
		// T-02-33: log raw error, respond with generic message.
		a.cfg.Logger.Error("openai: engine.Collect error", "err", err)
		writeError(w, http.StatusInternalServerError, errAPI, "internal error")
		return
	}

	// Phase 6 D-01: invoke CoerceToolCall on the non-streaming path
	// between aggregation and per-surface render. The function mutates
	// resp in place (Pitfall 6 — pass the pointer directly, no
	// pre-copy). Coerce-from-text returns true iff Message.ToolCalls
	// was rewritten; the kiro-native narration path (06-01 Task 2
	// narration aggregator → text like "[tool: <name>]\n") naturally
	// fails the JSON-parse step and is a coerce miss, so the narration
	// text flows through resp.Message.Content into choices[0].message.
	// content as expected.
	if engine.CoerceToolCall(req, resp) {
		// REVIEW LOW #7 defensive length-guard: even though CoerceToolCall
		// only returns true after appending a ToolCall entry, guard the
		// read in case the contract drifts.
		var firstName string
		if len(resp.Message.ToolCalls) > 0 {
			firstName = resp.Message.ToolCalls[0].Name
		}
		a.cfg.Logger.Debug("openai: coerce fired", "tool", firstName)
	}

	writeJSON(w, chatResponseToCompletion(resp, wire.Model))
}

// handleCompletions handles POST /completions (legacy text completion shim — D-03).
//
// Flow:
//  1. Nil-engine guard → 503 errAPI ("kiro-cli not configured").
//  2. decodeJSONBody with 4 MiB cap. 413 errRequestTooLarge on cap;
//     400 errInvalidRequest on syntactic error.
//  3. Silently downgrade stream:true to false (JSON-only shim; D-03 /
//     Open Question 2 resolved — no Phase 3 client drives completions streaming).
//     Mirror ollama/handlers.go:42-45.
//  4. promptToMessages: decode Prompt (string or []string); empty → 400.
//  5. engine.Collect → chatResponseToTextCompletion → writeJSON.
//  6. T-02-33: engine errors logged raw via slog.Error, generic 500 — NEVER echo.
//  7. Accept-and-ignore: logprobs/echo/suffix/best_of/n/max_tokens in wire struct.
//
// T-03-20: decodeJSONBody + MaxBytesReader(4 MiB) → 413.
// T-03-21: engine.Collect errors slog'd raw + generic 500 message (T-02-33 carry-forward).
func (a *Adapter) handleCompletions(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Engine == nil {
		writeError(w, http.StatusServiceUnavailable, errAPI, "kiro-cli not configured (set KIRO_CMD)")
		return
	}

	var wire completionWireRequest
	if err := decodeJSONBody(w, r, chatBodyCap, &wire); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, errRequestTooLarge, "request body exceeds maximum size")
			return
		}
		writeError(w, http.StatusBadRequest, errInvalidRequest, "invalid JSON: "+err.Error())
		return
	}

	// D-03: JSON-only shim; stream is silently downgraded.
	// (Mirror ollama/handlers.go:42-45 pattern.)
	wire.Stream = false

	msgs, err := promptToMessages(wire.Prompt)
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidRequest, err.Error())
		return
	}

	req := &canonical.ChatRequest{
		Model:              wire.Model,
		Messages:           msgs,
		WorkingDirOverride: r.Header.Get("X-Working-Dir"),
	}

	// Plan 05-03: X-Session-Id branch (same shape as handleChatCompletions).
	eng, entry, sErr := a.resolveEngine(r)
	if sErr != nil {
		a.writeSessionError(w, sErr)
		return
	}
	if entry != nil {
		entry.Mu.Lock()
		// CR-01 fix: Unlock registers FIRST (runs LAST), MarkUsed
		// SECOND (runs FIRST). MarkUsed writes Entry.LastUsed and must
		// run UNDER entry.Mu so the reaper's TryLock-guarded read sees
		// the post-stream value.
		defer entry.Mu.Unlock()
		defer entry.MarkUsed()
	}

	resp, err := eng.Collect(r.Context(), req)
	if err != nil {
		// T-02-33: log raw error, respond with generic message.
		a.cfg.Logger.Error("openai: completions engine.Collect error", "err", err)
		writeError(w, http.StatusInternalServerError, errAPI, "internal error")
		return
	}

	writeJSON(w, chatResponseToTextCompletion(resp, wire.Model))
}

// resolveEngine implements the Plan 05-03 X-Session-Id branch for the
// OpenAI surface. See ollama's resolveEngine for the contract.
func (a *Adapter) resolveEngine(r *http.Request) (Engine, *session.Entry, error) {
	sid := r.Header.Get("X-Session-Id")
	if sid == "" || a.cfg.Registry == nil || a.cfg.EngineForSession == nil {
		return a.cfg.Engine, nil, nil
	}
	entry, err := a.cfg.Registry.Get(r.Context(), sid, a.cfg.KiroCWD)
	if err != nil {
		return nil, nil, err
	}
	return a.cfg.EngineForSession(entry), entry, nil
}

// writeSessionError renders a registry error in the OpenAI error
// envelope. ErrSessionMaxExceeded → 503; other errors → 500.
func (a *Adapter) writeSessionError(w http.ResponseWriter, err error) {
	if errors.Is(err, session.ErrSessionMaxExceeded) {
		writeError(w, http.StatusServiceUnavailable, errAPI, "session capacity exceeded")
		return
	}
	a.cfg.Logger.Error("openai: session registry error", "err", err)
	writeError(w, http.StatusInternalServerError, errAPI, "internal error")
}

// handleModels handles GET /models (D-04, SC3).
//
// It renders the OpenAI model list from the injected ModelCatalog —
// the same catalog /api/tags iterates — satisfying SC3 same-set by
// construction. "auto" is always prepended (Node parity).
//
// When ModelCatalog is nil (KIRO_CMD unset), only the synthetic
// "auto" entry is returned so clients still see a usable list.
//
// No body decode (GET). No auth check (prefix middleware owns it).
// T-03-22: modelInfo exposes only id/object/created/owned_by — no
// internal pool slot detail, no env vars, no file paths.
func (a *Adapter) handleModels(w http.ResponseWriter, _ *http.Request) {
	created := time.Now().Unix()
	var catalogModels []canonical.ModelInfo
	if a.cfg.ModelCatalog != nil {
		catalogModels = a.cfg.ModelCatalog.Models()
	}
	writeJSON(w, catalogToModelList(catalogModels, "kiro", created))
}
