package ollama

import (
	"encoding/json"
	"net/http"
)

// Body-cap for stub endpoints — Codex M-5 closes the previously
// unbounded path. 64 KiB is generous for envelope-only bodies (the
// largest legitimate /api/pull body is ~200 bytes).
const stubBodyCap int64 = 64 << 10

// ----------------------------------------------------------------------------
// handlePull / handlePush / handleCreate — stream-default-true NDJSON
// ----------------------------------------------------------------------------

func (a *Adapter) handlePull(w http.ResponseWriter, r *http.Request) {
	stubStreaming(w, r, "success", "pulling manifest")
}

func (a *Adapter) handlePush(w http.ResponseWriter, r *http.Request) {
	stubStreaming(w, r, "success", "")
}

func (a *Adapter) handleCreate(w http.ResponseWriter, r *http.Request) {
	stubStreaming(w, r, "success", "")
}

// stubStreaming mirrors the Node reference stubStreaming helper
// (acp-ollama-server.js:1008-1012):
//   - stream defaults to true (Node parity)
//   - stream:true emits NDJSON with optional pre-status (e.g.,
//     "pulling manifest") then the final statusLine (default "success")
//   - stream:false returns a single JSON {status: statusLine}
//
// Body is decoded only to enforce the Codex M-5 size cap; only the
// stream field is read.
func stubStreaming(w http.ResponseWriter, r *http.Request, statusLine, preStatus string) {
	var req ollamaStubStreamRequest
	if err := decodeJSONBody(w, r, stubBodyCap, &req); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		// Defensive: a malformed body still gets the success stub —
		// Node's behaviour. LangFlow sometimes sends an empty body.
	}

	stream := true
	if req.Stream != nil {
		stream = *req.Stream
	}

	if !stream {
		writeJSON(w, ollamaStubStatusLine{Status: statusLine})
		return
	}

	writeNDJSON(w, preStatus, statusLine)
}

// writeNDJSON emits the NDJSON header set and writes one JSON object
// per non-empty status. Each line ends with "\n" per ndjson.org spec.
// Headers match the Node reference (Content-Type application/x-ndjson,
// Transfer-Encoding chunked, Cache-Control no-cache, X-Accel-Buffering
// no — the last is to disable nginx proxy buffering when present).
func writeNDJSON(w http.ResponseWriter, statuses ...string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	for _, s := range statuses {
		if s == "" {
			continue
		}
		_ = enc.Encode(ollamaStubStatusLine{Status: s})
		// Best-effort flush so chunks reach the client incrementally.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// ----------------------------------------------------------------------------
// handleCopy / handleDelete — empty-object response
// ----------------------------------------------------------------------------

// handleCopy returns {} per Node parity (acp-ollama-server.js:1028).
// Body is decoded only for the Codex M-5 size cap; contents discarded.
func (a *Adapter) handleCopy(w http.ResponseWriter, r *http.Request) {
	var req ollamaCopyRequest
	if err := decodeJSONBody(w, r, stubBodyCap, &req); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		// Defensive: continue with the empty-object response even on
		// malformed body — Node parity.
	}
	writeJSON(w, map[string]any{})
}

// handleDelete returns {} per Node parity (acp-ollama-server.js:1029).
// Same body-cap rationale as handleCopy.
func (a *Adapter) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req ollamaDeleteRequest
	if err := decodeJSONBody(w, r, stubBodyCap, &req); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		// Defensive: continue with the empty-object response.
	}
	writeJSON(w, map[string]any{})
}
