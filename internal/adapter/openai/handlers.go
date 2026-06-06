package openai

import (
	"context"
	"errors"
	"net/http"
	"time"

	"otto-gateway/internal/auth"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin"
	"otto-gateway/internal/plugin/pii"
	"otto-gateway/internal/session"
)

// shortCircuitMessage extracts the user-facing error message from a
// canonical.StopError envelope (slice 2 AuthHook synthesizeAuthError
// shape: Message.Content[0].Text). Phase 8 SC1.
func shortCircuitMessage(resp *canonical.ChatResponse) string {
	if resp == nil {
		return "request rejected"
	}
	for _, part := range resp.Message.Content {
		if part.Kind == canonical.ContentKindText && part.Text != "" {
			return part.Text
		}
	}
	return "request rejected"
}

// stampPluginCtx is the shared per-request ctx-stamp for the OpenAI
// surface. Honors inbound X-Request-Id (mints ULID when absent) and
// stamps a fresh *pii.Summary so PIIRedactionHook + LoggingHook share
// one pointer via ctx. Mirrors the ollama-side helper. Phase 8
// OBSV-03 / D-04 slice 5 Task 4b.
func stampPluginCtx(ctx context.Context, r *http.Request) context.Context {
	reqID := r.Header.Get("X-Request-Id")
	if reqID == "" {
		reqID = plugin.NewRequestID()
	}
	ctx = plugin.WithRequestID(ctx, reqID)
	ctx = pii.WithSummary(ctx, pii.NewSummary())
	return ctx
}

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

	// Phase 8 PLUG-03 — stamp the bearer credential onto ctx so AuthHook
	// (canonical-layer Pre hook) can validate. The auth.Bearer chi
	// middleware remains active (defense-in-depth during slice-5 wiring);
	// when AuthHook is wired into the chain in main.go, the middleware
	// will be removed in one atomic commit. See 08-PATTERNS.md Pattern F
	// migration boundary. T-8-AUTH-4: never log the raw token — the
	// stamp is silent.
	ctx := canonical.WithBearerToken(r.Context(), auth.ExtractToken(r))
	// Phase 8 OBSV-03 / D-04 — request_id + pii.Summary ctx-stamp
	// (slice 5 Task 4b). Mirrors handleChat in ollama.
	ctx = stampPluginCtx(ctx, r)
	// Quick 260529-ll2 — surface stamp for ChatTraceHook correlation.
	// Placed AFTER stampPluginCtx so request_id is already on ctx when
	// SurfaceFromContext fires inside ChatTraceHook.Before.
	ctx = plugin.WithSurface(ctx, "openai")

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
		// Derive from the bearer-stamped ctx so AuthHook sees the
		// credential when the chain runs inside eng.Run.
		//
		// Phase 6 (REVIEW HIGH #1 + iteration-3 sawKiroNativeToolCall):
		// the streaming branch threads the canonical `req` pointer down
		// to runSSEEmitter. Streaming coerce lives in sse.go — see
		// REVIEW HIGH #1. engine.CoerceToolCall must run AFTER all text
		// deltas accumulate, before the terminal finish_reason frame
		// composes. Iteration 3: sse.go also tracks sawKiroNativeToolCall
		// and skips coerce when true (prevents the iteration-2 double-fire
		// regression).
		streamCtx, cancelFn := context.WithCancel(ctx)
		defer cancelFn()
		runHandle, err := eng.Run(streamCtx, req)
		if err != nil {
			// engine.Run failed BEFORE any SSE headers were written — safe to
			// respond with a normal JSON 500 envelope (T-02-33: log raw, generic message).
			a.cfg.Logger.Error("openai: engine.Run error", "err", err)
			writeError(w, http.StatusInternalServerError, errAPI, "internal error")
			return
		}
		// Phase 08.1 INTEG-01 D-01..D-04: PreHook short-circuit must be
		// caught BEFORE runSSEEmitter opens SSE headers, otherwise the
		// bad-bearer case emits a benign empty 200 SSE stream instead of
		// 401. Mirrors the non-streaming sibling at handlers.go:165-168.
		if sc := runHandle.ShortCircuitResponse(); sc != nil {
			// D-03 / Pitfall 4: nil-guard the watchdog stop function
			// (watchdog is nil on a short-circuit Run — engine.go:150).
			if stop := runHandle.StopWatchdog(); stop != nil {
				stop()
			}
			// Audit plugin-chain-streaming-shortcircuit-skips-posthooks:
			// fire PostHooks symmetrically with the non-streaming Collect
			// path so LoggingHook.After + ChatTraceHook.After observe
			// rejected streaming requests and their startTimes don't leak.
			if pErr := eng.RunPostHooks(streamCtx, req, sc); pErr != nil {
				a.cfg.Logger.Warn("openai: posthook error on streaming short-circuit (swallowed)",
					"err", pErr,
					"request_id", plugin.RequestIDFromContext(ctx))
			}
			writeError(w, http.StatusUnauthorized, errAuthentication, shortCircuitMessage(sc))
			return
		}
		// T-5b: Pre hooks (notably the PII encrypt hook) may have flipped
		// req.Stream=false during eng.Run. When that happens we abandon
		// the SSE branch and drain the already-running ACP session through
		// eng.CollectFromRun, then render via the surface's non-streaming
		// response shape. This closes the PII encrypt round-trip for
		// streaming OpenAI clients (Pi-SDK) — without this re-route the
		// SSE emitter would flush ciphertext bytes ahead of the PII decrypt
		// PostHook.
		if !req.Stream {
			a.cfg.Logger.Info(
				"stream re-routed to aggregated path",
				"surface", "openai.chat",
				"reason", "pre_hook_disabled_streaming",
				"request_id", plugin.RequestIDFromContext(ctx),
			)
			resp, cErr := eng.CollectFromRun(streamCtx, runHandle, req)
			if cErr != nil {
				if errors.Is(cErr, canonical.ErrStreamIdleTimeout) {
					a.cfg.Logger.Warn(
						"stream.idle_timeout",
						"surface", "openai",
						"elapsed_ms", a.cfg.StreamIdleTimeout.Milliseconds(),
						"request_id", plugin.RequestIDFromContext(ctx),
					)
					writeError(w, http.StatusGatewayTimeout, errAPI, "upstream stream idle timeout")
					return
				}
				a.cfg.Logger.Error("openai: engine.CollectFromRun error", "err", cErr)
				writeError(w, http.StatusInternalServerError, errAPI, "internal error")
				return
			}
			if resp != nil && resp.StopReason == canonical.StopError {
				writeError(w, http.StatusUnauthorized, errAuthentication, shortCircuitMessage(resp))
				return
			}
			// Phase 6 D-01: coerce-from-text fires on the non-streaming
			// path between aggregation and render — same as the
			// eng.Collect site below. The streaming-coerce buffering in
			// sse.go is bypassed when we re-route, so apply the
			// non-streaming coerce here for shape parity.
			if engine.CoerceToolCall(req, resp) {
				var firstName string
				if len(resp.Message.ToolCalls) > 0 {
					firstName = resp.Message.ToolCalls[0].Name
				}
				a.cfg.Logger.Debug("openai: coerce fired (re-route path)", "tool", firstName)
			}
			// The CLIENT asked for stream=true (wire.Stream was true at
			// request entry). Emit a synthetic SSE stream from the
			// aggregated response so the SDK sees text/event-stream and
			// the expected chat.completion.chunk frames. Writing
			// application/json here would trip OpenAI SDK clients with
			// "request ended without sending any chunks" — the v1.8.3
			// regression that motivated this path.
			if err := runSyntheticSSEFromResponse(streamCtx, w, resp, wire.Model, a.cfg.Logger); err != nil {
				a.cfg.Logger.Debug("openai: synthetic SSE terminated", "err", err)
			}
			if resp != nil {
				if pErr := eng.RunPostHooks(streamCtx, req, resp); pErr != nil {
					a.cfg.Logger.Warn(
						"openai: PostHook error (synthetic SSE — swallowed; client already received stream)",
						"err", pErr,
						"request_id", plugin.RequestIDFromContext(ctx),
					)
				}
			}
			return
		}
		resp, err := runSSEEmitter(streamCtx, w, runHandle, req, wire.Model, a.cfg.StreamIdleTimeout, a.cfg.Logger)
		if err != nil {
			// runSSEEmitter has already written SSE headers + at least some frames.
			// We cannot send a JSON 500 after WriteHeader; log at debug and let
			// the truncated stream stand (Pitfall 3 / A5).
			a.cfg.Logger.Debug("openai: sse emitter terminated", "err", err)
		}
		// Quick 260530-df2 — fire PostHooks on the aggregated response.
		// resp is non-nil even on disconnect / mid-stream Result()
		// error so PostHooks observe forensics. Hook errors are logged
		// at WARN and SWALLOWED (T-df2-02): the stream is over from
		// the client's perspective. The /completions shim (which
		// silently downgrades stream:true to false) routes through
		// eng.Collect and so PostHooks fire there via Collect's
		// existing traversal — no change needed for that path.
		if resp != nil {
			if pErr := eng.RunPostHooks(streamCtx, req, resp); pErr != nil {
				a.cfg.Logger.Warn("openai: posthook error after streaming completion",
					"err", pErr, "surface", "openai.chat")
			}
		}
		return
	}

	// Non-streaming path (SC1 curl use case).
	resp, err := eng.Collect(ctx, req)
	if err != nil {
		// Quick 260531-ruv — idle-timeout maps to 504.
		if errors.Is(err, canonical.ErrStreamIdleTimeout) {
			a.cfg.Logger.Warn(
				"stream.idle_timeout",
				"surface", "openai",
				"elapsed_ms", a.cfg.StreamIdleTimeout.Milliseconds(),
				"request_id", plugin.RequestIDFromContext(ctx),
			)
			writeError(w, http.StatusGatewayTimeout, errAPI, "upstream stream idle timeout")
			return
		}
		// T-02-33: log raw error, respond with generic message.
		a.cfg.Logger.Error("openai: engine.Collect error", "err", err)
		writeError(w, http.StatusInternalServerError, errAPI, "internal error")
		return
	}
	// Phase 8 SC1: detect a PreHook short-circuit envelope
	// (StopReason == StopError) and render the per-surface error
	// envelope. AuthHook is the v1 producer (bad/missing bearer);
	// future Pre hooks (rate-limit, content-mod) use the same
	// discriminator. Status 401 because AuthHook is the only v1
	// producer.
	if resp != nil && resp.StopReason == canonical.StopError {
		writeError(w, http.StatusUnauthorized, errAuthentication, shortCircuitMessage(resp))
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

	// Phase 8 PLUG-03 — same bearer-credential ctx-stamp as
	// handleChatCompletions; AuthHook on the chain validates uniformly
	// for both endpoints. See 08-PATTERNS.md Pattern F.
	ctx := canonical.WithBearerToken(r.Context(), auth.ExtractToken(r))
	// Phase 8 OBSV-03 / D-04 — request_id + pii.Summary ctx-stamp
	// (slice 5 Task 4b). Mirrors handleChat in ollama. Both
	// handleChatCompletions and handleCompletions stamp here.
	ctx = stampPluginCtx(ctx, r)
	// Quick 260529-ll2 — surface stamp for ChatTraceHook correlation.
	ctx = plugin.WithSurface(ctx, "openai")

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

	resp, err := eng.Collect(ctx, req)
	if err != nil {
		// Quick 260531-ruv — idle-timeout maps to 504.
		if errors.Is(err, canonical.ErrStreamIdleTimeout) {
			a.cfg.Logger.Warn(
				"stream.idle_timeout",
				"surface", "openai",
				"elapsed_ms", a.cfg.StreamIdleTimeout.Milliseconds(),
				"request_id", plugin.RequestIDFromContext(ctx),
			)
			writeError(w, http.StatusGatewayTimeout, errAPI, "upstream stream idle timeout")
			return
		}
		// T-02-33: log raw error, respond with generic message.
		a.cfg.Logger.Error("openai: completions engine.Collect error", "err", err)
		writeError(w, http.StatusInternalServerError, errAPI, "internal error")
		return
	}
	// Phase 8 SC1: short-circuit detection — same as handleChatCompletions.
	if resp != nil && resp.StopReason == canonical.StopError {
		writeError(w, http.StatusUnauthorized, errAuthentication, shortCircuitMessage(resp))
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
