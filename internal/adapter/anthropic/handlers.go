package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"otto-gateway/internal/auth"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin"
	"otto-gateway/internal/plugin/pii"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/session"
)

// stampPluginCtx is the shared per-request ctx-stamp for the Anthropic
// surface. Honors inbound X-Request-Id (mints ULID when absent) and
// stamps a fresh *pii.Summary so PIIRedactionHook + LoggingHook share
// one pointer via ctx. Mirrors the ollama / openai helpers. Phase 8
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

// PHASE 6 INVARIANT: Anthropic does NOT call engine.CoerceToolCall.
//
// Per CONTEXT D-01 + D-17 scenario 5, running coerce on the Anthropic
// surface would silently rewrite messages.stream() consumers'
// assistant text into synthesized tool_use blocks — a wire-shape
// forgery that surprises loop24-client and any other Anthropic-native
// client that emits JSON-shaped assistant text legitimately. The
// per-surface Message.ToolCalls population contract (defined in
// 06-01 and implemented for Anthropic in collect.go's
// CollectAnthropicChat per the D-07 exception) is the correct
// mechanism — kiro-native ChunkKindToolCall produces native tool_use
// blocks via the adapter-local aggregator; bare-JSON assistant text
// is preserved verbatim with no synthesis.
//
// Regression guards:
//   - TestAnthropic_NoCoerce_Behavioral (REVIEW LOW #9 — primary):
//     drives a fake engine emitting bare JSON text + tools[] catalog,
//     asserts no tool_use is synthesized and stop_reason stays
//     end_turn.
//   - TestAnthropic_DoesNotCallCoerceToolCall (belt-and-suspenders):
//     static-source assertion that handlers.go contains no
//     `engine.CoerceToolCall` symbol.

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

	// Phase 8 PLUG-03 — stamp the bearer credential onto ctx so AuthHook
	// (canonical-layer Pre hook) can validate. The auth.Bearer chi
	// middleware remains active (defense-in-depth during slice-5 wiring);
	// when AuthHook is wired into the chain in main.go, the middleware
	// will be removed in one atomic commit. See 08-PATTERNS.md Pattern F
	// migration boundary.
	//
	// Anthropic dual-header per Phase 3.1 D-15: x-api-key takes
	// precedence (Anthropic SDK convention), with Bearer fallback via
	// auth.ExtractToken. This MIRRORS the Bearer middleware's
	// precedence (Authorization-wins) but with the per-surface
	// inversion the Anthropic SDK expects on the way IN. AuthHook
	// validates whichever token the adapter resolved here — the
	// per-surface precedence does NOT leak into the canonical layer.
	//
	// T-8-AUTH-4: never log the raw token — the stamp is silent.
	token := r.Header.Get("x-api-key")
	if token == "" {
		token = auth.ExtractToken(r)
	}
	ctx := canonical.WithBearerToken(r.Context(), token)
	// Phase 8 OBSV-03 / D-04 — request_id + pii.Summary ctx-stamp
	// (slice 5 Task 4b). Same shape as ollama / openai stamps.
	ctx = stampPluginCtx(ctx, r)
	// Quick 260529-ll2 — surface stamp for ChatTraceHook correlation.
	ctx = plugin.WithSurface(ctx, "anthropic")

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
		// D-07: create a derived context so that a write failure in
		// runSSEEmitter cancels the derived ctx (via defer cancelFn), which
		// the D-06 watchdog observes and translates into session/cancel.
		// Derive from the bearer-stamped ctx so AuthHook sees the
		// credential when the chain runs inside eng.Run.
		streamCtx, cancelFn := context.WithCancel(ctx)
		defer cancelFn()
		runHandle, err := eng.Run(streamCtx, req)
		if err != nil {
			// Engine.Run failed BEFORE any SSE headers were written —
			// respond with a normal JSON 500 envelope (T-02-33: never
			// echo err.Error() which may contain request fragments).
			// D-07 REL-POOL-01: pool exhaustion maps to 503 with the
			// Anthropic surface-native overloaded_error body.
			if errors.Is(err, pool.ErrPoolExhausted) {
				writePoolExhaustedAnthropic(w)
				return
			}
			a.cfg.Logger.Error("anthropic: engine.Run error", "err", err)
			writeError(w, http.StatusInternalServerError, errAPI, "internal error")
			return
		}
		// Phase 08.1 INTEG-01 D-01..D-04: PreHook short-circuit must be
		// caught BEFORE runSSEEmitter opens SSE headers, otherwise the
		// bad-bearer case emits a benign empty 200 SSE stream instead of
		// 401. Mirrors the non-streaming sibling at handlers.go:220-223
		// (the same surface, non-streaming path) and collect.go:66-73.
		if sc := runHandle.ShortCircuitResponse(); sc != nil {
			// D-03 / Pitfall 4: nil-guard the watchdog stop function
			// (watchdog is nil on a short-circuit Run — engine.go:150).
			if stop := runHandle.StopWatchdog(); stop != nil {
				stop()
			}
			// Audit plugin-chain-streaming-shortcircuit-skips-posthooks:
			// fire PostHooks symmetrically with the non-streaming Collect
			// path (collect.go:179-183) so LoggingHook.After +
			// ChatTraceHook.After observe rejected streaming requests
			// and their startTimes entries don't leak. Errors are
			// swallowed at WARN — a misbehaving audit hook must not block
			// error delivery to the client.
			if pErr := eng.RunPostHooks(streamCtx, req, sc); pErr != nil {
				a.cfg.Logger.Warn("anthropic: posthook error on streaming short-circuit (swallowed)",
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
		// streaming Anthropic clients (loop24-client) — without this
		// re-route the SSE emitter would flush ciphertext bytes ahead of
		// the PII decrypt PostHook.
		//
		// v1 limitation: this path uses the generic engine aggregator
		// (CollectFromRun), NOT the Anthropic-local CollectAnthropicChat.
		// Kiro-native ChunkKindToolCall chunks render as `[tool: <name>]\n`
		// narration text on this path rather than native tool_use content
		// blocks. Plain-text responses round-trip correctly. Documented in
		// docs/operating.md Known Limitations.
		if !req.Stream {
			a.cfg.Logger.Info(
				"stream re-routed to aggregated path",
				"surface", "anthropic",
				"reason", "pre_hook_disabled_streaming",
				"request_id", plugin.RequestIDFromContext(ctx),
			)
			resp, cErr := eng.CollectFromRun(streamCtx, runHandle, req)
			if cErr != nil {
				if errors.Is(cErr, canonical.ErrStreamIdleTimeout) {
					a.cfg.Logger.Warn(
						"stream.idle_timeout",
						"surface", "anthropic",
						"elapsed_ms", a.cfg.StreamIdleTimeout.Milliseconds(),
						"request_id", plugin.RequestIDFromContext(ctx),
					)
					writeError(w, http.StatusGatewayTimeout, errAPI, "upstream stream idle timeout")
					return
				}
				a.cfg.Logger.Error("anthropic: engine.CollectFromRun error", "err", cErr)
				writeError(w, http.StatusInternalServerError, errAPI, "internal error")
				return
			}
			if resp != nil && resp.StopReason == canonical.StopError {
				// Audit anthropic-rerouted-stream-writes-json-on-short-circuit:
				// the client wired up the SDK as SSE (wire.Stream was
				// true at request entry). A JSON 401 envelope here makes
				// @anthropic-ai/sdk's MessageStream parser see "request
				// ended without sending any chunks" — the v1.8.3
				// regression. Emit a 200 SSE error frame instead so the
				// SDK surfaces a proper APIError with the short-circuit
				// message. Headers have NOT been written yet on this
				// branch — set them now.
				flusher, ok := w.(http.Flusher)
				if !ok {
					// No flusher (test harness writer, etc.) — fall back
					// to JSON 401. Same wire-shape mismatch the audit
					// flags, but unavoidable without a flusher.
					writeError(w, http.StatusUnauthorized, errAuthentication, shortCircuitMessage(resp))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.Header().Set("X-Accel-Buffering", "no")
				w.WriteHeader(http.StatusOK)
				writeSSEError(w, flusher, errAuthentication, shortCircuitMessage(resp))
				return
			}
			// The CLIENT asked for stream=true (wire.Stream was true at
			// request entry). Emit a synthetic SSE stream from the
			// aggregated response so the SDK sees text/event-stream and
			// the expected message_start ... message_stop sequence. Writing
			// application/json here would trip Anthropic SDK clients (and
			// any other SSE consumer) with "request ended without sending
			// any chunks" — the v1.8.3 regression that motivated this path.
			// Audit ollama-reroute-double-posthook-fires (applies
			// symmetrically here): CollectFromRun above already fired
			// the PostHook chain (collect.go:179-183). Do NOT call
			// RunPostHooks again on resp — a second pass corrupts
			// non-idempotent hooks (PII decrypt operates on already-
			// decrypted content) and double-logs idempotent ones.
			if err := runSyntheticSSEFromResponse(streamCtx, w, resp, wire.Model, a.cfg.Logger); err != nil {
				a.cfg.Logger.Debug("anthropic: synthetic SSE terminated", "err", err)
			}
			return
		}
		resp, err := runSSEEmitter(streamCtx, w, runHandle, wire.Model, a.cfg.StreamIdleTimeout, a.cfg.Logger)
		if err != nil {
			// Audit anthropic-flusher-assertion-fail-swallowed: the
			// no-flusher branch returns BEFORE any bytes are written,
			// so a JSON 500 envelope is still legal. Render it
			// explicitly instead of falling through (which would leave
			// the client with an empty 200 body and no PostHook
			// observability). All other errors — ctx cancel, mid-stream
			// emitter — happen after WriteHeader and cannot be
			// converted to JSON; debug-log and swallow.
			if errors.Is(err, errNoFlusher) {
				a.cfg.Logger.Error("anthropic: sse emitter no flusher", "err", err)
				writeError(w, http.StatusInternalServerError, errAPI, "internal error")
				return
			}
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
		// Quick 260530-df2 — fire PostHooks on the aggregated response
		// so LoggingHook.After + ChatTraceHook.After + any audit hook
		// observes every streaming request. PostHook errors are logged
		// at WARN and SWALLOWED: the stream is over from the client's
		// perspective (bytes are on the wire), and a misbehaving hook
		// MUST NOT tear down a completed request (T-df2-02). The
		// non-streaming path (CollectAnthropicChat) propagates the
		// error instead — the divergence is documented in
		// collect.go's tail call site.
		if resp != nil {
			if pErr := eng.RunPostHooks(streamCtx, req, resp); pErr != nil {
				a.cfg.Logger.Warn("anthropic: posthook error after streaming completion",
					"err", pErr)
			}
		}
		return
	}

	// Phase 6 Plan 04 Task 2 (D-07 exception to the per-surface
	// Message.ToolCalls population contract): call the Anthropic-local
	// CollectAnthropicChat aggregator instead of eng.Collect. This is
	// what populates Message.ToolCalls + ContentKindToolUse parts
	// from kiro-native ChunkKindToolCall chunks on the non-streaming
	// path — Anthropic's wire protocol has tool_use as a native
	// first-class element and the SDK expects it that way.
	resp, err := CollectAnthropicChat(ctx, eng, req, a.cfg.StreamIdleTimeout)
	if err != nil {
		// D-07 REL-POOL-01: pool exhaustion maps to 503 with Anthropic
		// overloaded_error body on the non-streaming path.
		if errors.Is(err, pool.ErrPoolExhausted) {
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, errOverloaded,
				"all workers busy; retry in 5s")
			return
		}
		// Quick 260531-ruv — idle-timeout maps to 504 Gateway Timeout
		// on the non-streaming branch (no SSE headers written yet).
		if errors.Is(err, canonical.ErrStreamIdleTimeout) {
			a.cfg.Logger.Warn(
				"stream.idle_timeout",
				"surface", "anthropic",
				"session_id", "", // non-streaming path: session id was bound inside CollectAnthropicChat scope
				"elapsed_ms", a.cfg.StreamIdleTimeout.Milliseconds(),
				"request_id", plugin.RequestIDFromContext(ctx),
			)
			writeError(w, http.StatusGatewayTimeout, errAPI, "upstream stream idle timeout")
			return
		}
		// T-02-33: log the raw error structurally; respond with a
		// neutral generic message that cannot echo request content.
		a.cfg.Logger.Error("anthropic: CollectAnthropicChat error", "err", err)
		writeError(w, http.StatusInternalServerError, errAPI, "internal error")
		return
	}
	// Phase 8 SC1: detect a PreHook short-circuit envelope
	// (StopReason == canonical.StopError) and render the Anthropic
	// error envelope. AuthHook is the v1 producer (bad/missing
	// bearer); future Pre hooks use the same discriminator. Status
	// 401 because the only v1 producer is AuthHook.
	if resp != nil && resp.StopReason == canonical.StopError {
		writeError(w, http.StatusUnauthorized, errAuthentication, shortCircuitMessage(resp))
		return
	}

	writeJSON(w, chatResponseToMessage(resp, wire.Model))
}

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

// resolveEngine implements the Plan 05-03 X-Session-Id branch for the
// Anthropic surface. See ollama's resolveEngine for the contract.
func (a *Adapter) resolveEngine(r *http.Request) (Engine, *session.Entry, error) {
	sid := r.Header.Get("X-Session-Id")
	if sid == "" || a.cfg.Registry == nil || a.cfg.EngineForSession == nil {
		return a.cfg.Engine, nil, nil
	}
	entry, err := a.cfg.Registry.Get(r.Context(), sid, a.cfg.KiroCWD)
	if err != nil {
		return nil, nil, fmt.Errorf("anthropic.handlers: session lookup: %w", err)
	}
	return a.cfg.EngineForSession(entry), entry, nil
}

// writeSessionError renders a registry error in the Anthropic error
// envelope. ErrSessionMaxExceeded → 503; other errors → 500.
func (a *Adapter) writeSessionError(w http.ResponseWriter, err error) {
	if errors.Is(err, session.ErrSessionMaxExceeded) {
		writeError(w, http.StatusServiceUnavailable, errOverloaded, "session capacity exceeded")
		return
	}
	a.cfg.Logger.Error("anthropic: session registry error", "err", err)
	writeError(w, http.StatusInternalServerError, errAPI, "internal error")
}
