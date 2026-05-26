package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"otto-gateway/internal/session"
)

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
		resp, err := eng.Collect(r.Context(), req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, chatResponseToWire(resp, start, wire.Model))
		return
	}

	// stream:true (default when absent) — NDJSON streaming path (Phase 4).
	// D-07: derive a cancelFn so emitNDJSONChunk can signal write failure back
	// to the engine watchdog via context cancellation.
	ctx, cancelFn := context.WithCancel(r.Context())
	defer cancelFn()

	run, err := eng.Run(ctx, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	start := time.Now()
	if emitErr := runNDJSONEmitter(ctx, cancelFn, w, run, wire.Model, true, start, a.cfg.Logger); emitErr != nil {
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
		resp, err := eng.Collect(r.Context(), req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, generateResponseToWire(resp, start, wire.Model))
		return
	}

	// stream:true (default when absent) — NDJSON streaming path (Phase 4).
	// D-07: derive a cancelFn so emitNDJSONChunk can signal write failure back
	// to the engine watchdog via context cancellation.
	ctx, cancelFn := context.WithCancel(r.Context())
	defer cancelFn()

	run, err := eng.Run(ctx, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	start := time.Now()
	if emitErr := runNDJSONEmitter(ctx, cancelFn, w, run, wire.Model, false, start, a.cfg.Logger); emitErr != nil {
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
