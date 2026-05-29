package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"otto-gateway/internal/auth"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin"
	"otto-gateway/internal/plugin/pii"
	"otto-gateway/internal/session"
)

// shortCircuitMessage extracts the user-facing error message from a
// PreHook short-circuit envelope (canonical.StopError). The message
// is in Message.Content[0].Text (slice 2's AuthHook synthesizeAuthError
// pattern). Falls back to a generic string if the envelope is
// malformed. Phase 8 SC1.
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

// stampPluginCtx is the shared per-request ctx-stamp shape used by
// both /api/chat and /api/generate. It honors an inbound X-Request-Id
// header, mints a fresh ULID via plugin.NewRequestID when absent, and
// stamps a fresh per-request *pii.Summary so the PIIRedactionHook
// (populator) and LoggingHook (consumer) share the same pointer via
// ctx. Phase 8 OBSV-03 / D-04 production-path seam (slice 5 Task 4b).
func stampPluginCtx(ctx context.Context, r *http.Request) context.Context {
	reqID := r.Header.Get("X-Request-Id")
	if reqID == "" {
		reqID = plugin.NewRequestID()
	}
	ctx = plugin.WithRequestID(ctx, reqID)
	ctx = pii.WithSummary(ctx, pii.NewSummary())
	return ctx
}

// Body-cap sizes per endpoint (Codex M-5 / threat T-02-29). Chat and
// generate accept large LangFlow transcripts; show is small; stubs are
// envelope-only.
const (
	chatBodyCap     int64 = 4 << 20 // 4 MiB
	generateBodyCap int64 = 4 << 20 // 4 MiB
	showBodyCap     int64 = 1 << 20 // 1 MiB
)

// ----------------------------------------------------------------------------
// handleChat — POST /api/chat
// ----------------------------------------------------------------------------

func (a *Adapter) handleChat(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Engine == nil {
		writeError(w, http.StatusServiceUnavailable, "kiro-cli not configured (set KIRO_CMD)")
		return
	}

	var wire ollamaChatRequest
	if err := decodeJSONBody(w, r, chatBodyCap, &wire); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(wire.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "`messages` is required and must be a non-empty array")
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
	// Phase 8 OBSV-03 / D-04 — stamp the per-request request_id +
	// pii.Summary onto ctx BEFORE engine entry so RequestIDHook
	// honors the inbound id, LoggingHook's sync.Map keyed by
	// request_id stays correlation-safe, and PIIRedactionHook +
	// LoggingHook share one *Summary pointer (slice 5 Task 4b).
	ctx = stampPluginCtx(ctx, r)
	// Quick 260529-ll2 — surface stamp for ChatTraceHook correlation.
	ctx = plugin.WithSurface(ctx, "ollama")

	// Plan 05-03 D-04..D-11: when X-Session-Id is present AND the registry
	// + factory closure are wired, route through a per-request engine bound
	// to the dedicated *session.Entry. Empty sid OR missing wiring falls
	// through to the pool path (unchanged).
	eng, entry, sErr := a.resolveEngine(r)
	if sErr != nil {
		a.writeSessionError(w, sErr)
		return
	}
	if entry != nil {
		entry.Mu.Lock()
		// D-11 (CR-01 fix): Unlock registers FIRST (runs LAST in defer
		// LIFO), MarkUsed registers SECOND (runs FIRST in defer LIFO).
		// This ordering ensures MarkUsed's write to Entry.LastUsed
		// happens UNDER entry.Mu, which is the same mutex the reaper
		// takes via TryLock when reading LastUsed (reaper.go). The
		// previous ordering ran MarkUsed AFTER Unlock — a data race on
		// LastUsed and a logic window where the reaper could kill a
		// session whose stream had just completed but whose LastUsed
		// was still stale.
		defer entry.Mu.Unlock()
		defer entry.MarkUsed()
	}

	if !streamEnabled(wire.Stream) {
		// stream:false — non-streaming path: collect and return a single JSON object.
		start := time.Now()
		resp, err := eng.Collect(ctx, req)
		if err != nil {
			// T-02-33: log the raw error structurally; respond with a
			// neutral generic message that cannot echo request content.
			// Mirrors the discipline used by the Anthropic and OpenAI
			// surfaces (handlers.go:165-167 / handlers.go:107-110).
			a.cfg.Logger.Error("ollama: engine.Collect error", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		// Phase 8 SC1: detect a PreHook short-circuit envelope
		// (StopReason == StopError) and render the per-surface error
		// envelope rather than treating it as a normal assistant
		// response. AuthHook (slice 2) returns this on bad/missing
		// bearer; future Pre hooks (rate-limit, content-mod) will use
		// the same discriminator. Status code is 401 because the only
		// v1 producer is AuthHook; future hooks producing StopError
		// for non-auth reasons would need a status-mapping helper.
		if resp != nil && resp.StopReason == canonical.StopError {
			writeError(w, http.StatusUnauthorized, shortCircuitMessage(resp))
			return
		}
		// Phase 6 D-01: invoke CoerceToolCall on the non-streaming path
		// AFTER Collect, BEFORE render. The function mutates resp in place
		// (Pitfall 6: pass the pointer directly — pre-copying would
		// discard the ToolCalls slice append). REVIEW LOW #7: the
		// `len(resp.Message.ToolCalls) > 0` guard is defensive — coerce
		// returning true implies the slice is non-empty, but the explicit
		// length check makes the debug-log site robust to future algorithm
		// changes (and prevents a nil-deref on `resp.Message.ToolCalls[0]`).
		if engine.CoerceToolCall(req, resp) {
			var firstName string
			if len(resp.Message.ToolCalls) > 0 {
				firstName = resp.Message.ToolCalls[0].Name
			}
			a.cfg.Logger.Debug("ollama: coerce fired", "tool", firstName)
		}
		writeJSON(w, chatResponseToWire(resp, start, wire.Model))
		return
	}

	// stream:true (default when absent) — NDJSON streaming path (Phase 4).
	//
	// Phase 6 (REVIEW HIGH #1 + iteration-3 sawKiroNativeToolCall): coerce
	// for the streaming path lives in ndjson.go, NOT here. The streaming
	// emitter buffers JSON-shaped assistant text, tracks whether any
	// kiro-native ChunkKindToolCall fired during the stream, and at
	// stream end either skips coerce (sawKiroNativeToolCall == true) or
	// runs coerce on the buffered text. The canonical req is threaded
	// through to the emitter so it can call engine.CoerceToolCall with
	// req.Tools available.
	//
	// D-07: derive a cancelFn so emitNDJSONChunk can signal write failure
	// back to the engine watchdog via context.cancellation. Derive from the
	// bearer-stamped ctx so AuthHook still sees the credential when the
	// chain runs inside eng.Run on the streaming path.
	streamCtx, cancelFn := context.WithCancel(ctx)
	defer cancelFn()

	run, err := eng.Run(streamCtx, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Phase 08.1 INTEG-01 D-01..D-04: PreHook short-circuit must be
	// caught BEFORE runNDJSONEmitter opens NDJSON headers, otherwise
	// the bad-bearer case emits a benign empty 200 stream instead of
	// 401. Mirrors the non-streaming sibling at handlers.go:147-150
	// and the canonical template at anthropic/collect.go:66-73.
	if sc := run.ShortCircuitResponse(); sc != nil {
		// Watchdog is nil on the short-circuit path (engine.go:150);
		// guard the deref per D-03 / Pitfall 4. Mirrors
		// anthropic/collect.go:69-71.
		if stop := run.StopWatchdog(); stop != nil {
			stop()
		}
		writeError(w, http.StatusUnauthorized, shortCircuitMessage(sc))
		return
	}
	start := time.Now()
	if emitErr := runNDJSONEmitter(streamCtx, cancelFn, w, run, wire.Model, true, start, a.cfg.Logger, req); emitErr != nil {
		a.cfg.Logger.Debug("ollama: ndjson chat emitter error", "err", emitErr)
	}
}

// resolveEngine implements the X-Session-Id branch (Plan 05-03 Task 3).
//
// Returns (engine, entry, err) where:
//   - engine is the Engine the handler should call (registry-bound when
//     X-Session-Id is present and wired; pool-bound otherwise).
//   - entry is the *session.Entry that was acquired (non-nil only when
//     the registry path was taken); the caller MUST Lock/Unlock entry.Mu
//     and defer entry.MarkUsed when entry != nil.
//   - err is non-nil only when Registry.Get failed; the caller renders
//     it via writeSessionError (translates ErrSessionMaxExceeded to 503).
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

// writeSessionError renders a registry error in the Ollama-shaped error
// envelope. ErrSessionMaxExceeded → 503; other errors → 500.
func (a *Adapter) writeSessionError(w http.ResponseWriter, err error) {
	if errors.Is(err, session.ErrSessionMaxExceeded) {
		writeError(w, http.StatusServiceUnavailable, "session capacity exceeded")
		return
	}
	a.cfg.Logger.Error("ollama: session registry error", "err", err)
	writeError(w, http.StatusInternalServerError, "session registry error")
}

// ----------------------------------------------------------------------------
// handleGenerate — POST /api/generate
// ----------------------------------------------------------------------------

func (a *Adapter) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Engine == nil {
		writeError(w, http.StatusServiceUnavailable, "kiro-cli not configured (set KIRO_CMD)")
		return
	}

	var wire ollamaGenerateRequest
	if err := decodeJSONBody(w, r, generateBodyCap, &wire); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if wire.Prompt == "" {
		writeError(w, http.StatusBadRequest, "`prompt` is required")
		return
	}

	req := wireGenerateToChatRequest(&wire, r)

	// Phase 8 PLUG-03 — stamp bearer credential onto ctx for AuthHook.
	// See handleChat for the migration-boundary rationale (08-PATTERNS
	// Pattern F). T-8-AUTH-4: token never logged.
	ctx := canonical.WithBearerToken(r.Context(), auth.ExtractToken(r))
	// Phase 8 OBSV-03 / D-04 — request_id + pii.Summary ctx-stamp
	// (slice 5 Task 4b). See handleChat for the full rationale.
	ctx = stampPluginCtx(ctx, r)
	// Quick 260529-ll2 — surface stamp for ChatTraceHook correlation.
	ctx = plugin.WithSurface(ctx, "ollama")

	// Plan 05-03: X-Session-Id branch (same shape as handleChat).
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

	if !streamEnabled(wire.Stream) {
		// stream:false — non-streaming path: collect and return a single JSON object.
		start := time.Now()
		resp, err := eng.Collect(ctx, req)
		if err != nil {
			// T-02-33: log the raw error structurally; respond with a
			// neutral generic message that cannot echo request content.
			// Mirrors the discipline used by the Anthropic and OpenAI
			// surfaces (handlers.go:165-167 / handlers.go:107-110).
			a.cfg.Logger.Error("ollama: engine.Collect error", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		// Phase 8 SC1: same StopError short-circuit detection as
		// handleChat above.
		if resp != nil && resp.StopReason == canonical.StopError {
			writeError(w, http.StatusUnauthorized, shortCircuitMessage(resp))
			return
		}
		writeJSON(w, generateResponseToWire(resp, start, wire.Model))
		return
	}

	// stream:true (default when absent) — NDJSON streaming path (Phase 4).
	// D-07: derive a cancelFn so emitNDJSONChunk can signal write failure back
	// to the engine watchdog via context cancellation. Derive from the
	// bearer-stamped ctx so AuthHook sees the credential.
	streamCtx, cancelFn := context.WithCancel(ctx)
	defer cancelFn()

	run, err := eng.Run(streamCtx, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Phase 08.1 INTEG-01 D-01..D-04: PreHook short-circuit must be
	// caught BEFORE runNDJSONEmitter opens NDJSON headers. Mirrors the
	// non-streaming sibling at handlers.go:297-300 and the handleChat
	// site above. Pitfall 6: handleGenerate is an INDEPENDENT streaming
	// branch from handleChat — both must carry the guard.
	if sc := run.ShortCircuitResponse(); sc != nil {
		// D-03 / Pitfall 4: nil-guard the watchdog stop function.
		if stop := run.StopWatchdog(); stop != nil {
			stop()
		}
		writeError(w, http.StatusUnauthorized, shortCircuitMessage(sc))
		return
	}
	start := time.Now()
	// Generate has no tools[] — pass req so the emitter signature stays
	// uniform; the streaming-coerce buffering logic only activates when
	// len(req.Tools) > 0 so this is a no-op for /api/generate in practice.
	if emitErr := runNDJSONEmitter(streamCtx, cancelFn, w, run, wire.Model, false, start, a.cfg.Logger, req); emitErr != nil {
		a.cfg.Logger.Debug("ollama: ndjson generate emitter error", "err", emitErr)
	}
}

// ----------------------------------------------------------------------------
// handleTags — GET /api/tags
// ----------------------------------------------------------------------------

// handleTags renders the catalog as Ollama tags. "auto" is always
// prepended per Node parity (acp-ollama-server.js:1045). When
// ModelCatalog is nil (KIRO_CMD unset), only the synthetic "auto"
// entry is returned so LangFlow still sees a usable models[] array.
func (a *Adapter) handleTags(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	out := ollamaTagsResponse{
		Models: []ollamaModelTag{ollamaModelTagFor("auto", now)},
	}
	if a.cfg.ModelCatalog != nil {
		for _, m := range a.cfg.ModelCatalog.Models() {
			out.Models = append(out.Models, ollamaModelTagFor(m.ID, now))
		}
	}
	writeJSON(w, out)
}

// ollamaModelTagFor renders one ollamaModelTag entry. Field defaults
// match the Node reference toOllamaModel (acp-ollama-server.js:735-749):
// empty digest, size=0, gguf/kiro details, "unknown" parameter_size and
// quantization_level. LangFlow tolerates the empty digest (A2 in
// RESEARCH.md).
func ollamaModelTagFor(id, now string) ollamaModelTag {
	return ollamaModelTag{
		Name:       id,
		Model:      id,
		ModifiedAt: now,
		Size:       0,
		Digest:     "",
		Details: ollamaModelTagDetails{
			Format:            "gguf",
			Family:            "kiro",
			Families:          []string{"kiro"},
			ParameterSize:     "unknown",
			QuantizationLevel: "unknown",
		},
	}
}

// ----------------------------------------------------------------------------
// handleShow — POST /api/show
// ----------------------------------------------------------------------------

func (a *Adapter) handleShow(w http.ResponseWriter, r *http.Request) {
	var req ollamaShowRequest
	if err := decodeJSONBody(w, r, showBodyCap, &req); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "`model` is required")
		return
	}

	if !a.modelExists(req.Model) {
		writeError(w, http.StatusNotFound, "model '"+req.Model+"' not found")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	out := ollamaShowResponse{
		Model:      req.Model,
		ModifiedAt: now,
		Details: ollamaModelTagDetails{
			Format:            "gguf",
			Family:            "kiro",
			Families:          []string{"kiro"},
			ParameterSize:     "unknown",
			QuantizationLevel: "unknown",
		},
		Capabilities: []string{"completion", "tools"},
		Modelinfo:    map[string]any{},
		Template:     "",
		Parameters:   "",
		License:      "",
	}
	writeJSON(w, out)
}

// modelExists reports whether the given model name appears in the
// catalog. "auto" is always considered to exist (Node parity).
func (a *Adapter) modelExists(name string) bool {
	if name == "auto" {
		return true
	}
	if a.cfg.ModelCatalog == nil {
		return false
	}
	for _, m := range a.cfg.ModelCatalog.Models() {
		if m.ID == name {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------------------
// handlePS — GET /api/ps
// ----------------------------------------------------------------------------

// handlePS returns a synthetic single-entry response (Node parity —
// acp-ollama-server.js:778-789). Phase 2 has no real session list, but
// LangFlow probes this endpoint to confirm the server is reachable.
func (a *Adapter) handlePS(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC()
	out := ollamaPSResponse{
		Models: []ollamaPSEntry{
			{
				Name:     "auto",
				Model:    "auto",
				Size:     0,
				SizeVRAM: 0,
				Details: ollamaModelTagDetails{
					Format:   "gguf",
					Family:   "kiro",
					Families: []string{"kiro"},
				},
				ExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339Nano),
			},
		},
	}
	writeJSON(w, out)
}

// ----------------------------------------------------------------------------
// handleVersion — GET /api/version (exposed via HandleVersion accessor,
// registered on the OUTER router per Codex M-4 / AUTH-03)
// ----------------------------------------------------------------------------

func (a *Adapter) handleVersion(w http.ResponseWriter, _ *http.Request) {
	commit := a.cfg.Commit
	if commit == "" {
		commit = extractCommit()
	}
	writeJSON(w, ollamaVersionResponse{
		Version: a.cfg.Version,
		Commit:  commit,
	})
}

// extractCommit walks debug.ReadBuildInfo().Settings looking for the
// vcs.revision entry and returns its first 7 characters. Returns
// "unknown" when build info is unavailable or the revision is missing
// (matches the version.Commit() shape from internal/version). Pitfall 7
// in RESEARCH.md.
func extractCommit() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return s.Value[:7]
			}
		}
	}
	return "unknown"
}

// ----------------------------------------------------------------------------
// Response helpers
// ----------------------------------------------------------------------------

// writeJSON writes Content-Type, status 200, and the JSON-encoded body.
// Every successful response in this package is 200; error responses go
// through writeError which sets the appropriate non-200 status.
// Encoder errors after WriteHeader cannot be reported to the client —
// they are silently dropped (chi's accessLog records the response in
// the access log; the client has already disconnected by definition).
func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError writes the Ollama-compatible error shape: {"error": msg}
// with the given status. Mirrors the Node reference ollamaError helper
// (acp-ollama-server.js:101-103). Error strings come straight from the
// caller — engine errors are wrapped at the engine boundary and DO NOT
// echo the request body (T-02-33 mitigation).
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
