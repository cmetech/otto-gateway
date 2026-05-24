package anthropic

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Anthropic error envelope per docs.anthropic.com/en/api/errors and
// Phase 3.1 D-20. Every error response from this adapter (other than
// the documented 401-from-auth-middleware exception — RESEARCH.md
// §Pattern 3 option 2) renders this shape.
//
//	{
//	  "type": "error",
//	  "error": {
//	    "type": "<error_type>",
//	    "message": "<...>"
//	  }
//	}
//
// The outer Type is ALWAYS the literal string "error"; the inner
// Type is one of the 8 error_type constants below. HTTP status mapping
// is documented at each constant.

// errorEnvelope is the outer envelope. Type is always "error".
type errorEnvelope struct {
	Type  string      `json:"type"`
	Error errorInner  `json:"error"`
}

// errorInner is the inner error object. Type is one of the 8
// error_type constants; Message is a human-readable string.
type errorInner struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Anthropic error_type constants per D-20. The string values are
// load-bearing — @anthropic-ai/sdk parses them to decide which
// JavaScript exception subclass to raise on the client side. Any
// drift from these literals breaks the SDK's error-handling path.
const (
	// errInvalidRequest — HTTP 400. Bad JSON, missing required field,
	// missing anthropic-version header, bad content-block shape.
	errInvalidRequest = "invalid_request_error"
	// errAuthentication — HTTP 401. Bad or missing api key when
	// AUTH_TOKEN is set. NOTE: Phase 3.1 emits this from the adapter
	// handlers ONLY; the auth.Bearer middleware (which actually guards
	// /v1/messages) emits the Ollama envelope per RESEARCH.md
	// §Pattern 3 option 2 (Phase 8 lifts that into a surface-aware
	// hook chain).
	errAuthentication = "authentication_error"
	// errPermission — HTTP 403. IP allowlist denies.
	errPermission = "permission_error"
	// errNotFound — HTTP 404. Unknown endpoint under /v1.
	errNotFound = "not_found_error"
	// errRequestTooLarge — HTTP 413. Body cap exceeded (4 MiB per
	// messagesBodyCap in handlers.go).
	errRequestTooLarge = "request_too_large"
	// errRateLimit — HTTP 429. Forward-design for the Phase 8 budget
	// hook; Phase 3.1 does not rate-limit.
	errRateLimit = "rate_limit_error"
	// errAPI — HTTP 500. Engine errors, pool errors. Message is always
	// generic ("internal error") — T-02-33 forbids echoing the err
	// string which may contain request fragments.
	errAPI = "api_error"
	// errOverloaded — HTTP 529. Pool empty / Acquire blocked beyond a
	// threshold. Phase 5 territory; Phase 3.1 may never emit this.
	errOverloaded = "overloaded_error"
)

// writeError writes the Anthropic error envelope shape with the given
// status. Mirrors Phase 2 Ollama's writeError helper (different
// envelope, same construction pattern). Sets Content-Type BEFORE
// WriteHeader (order matters — once the status line is flushed, header
// mutations are silently dropped).
//
// The message MUST NOT contain request body content (T-02-33). Callers
// constructing engine-error messages should log the raw err separately
// via the adapter's *slog.Logger and pass a generic message here.
func writeError(w http.ResponseWriter, status int, errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoder errors here mean the client disconnected mid-write; the
	// response headers are already flushed, so there is nothing useful
	// to recover. Discard explicitly to satisfy errcheck.
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		Type:  "error",
		Error: errorInner{Type: errorType, Message: message},
	})
}

// writeSSEError writes a mid-stream SSE error frame:
//
//	event: error
//	data: {"type":"error","error":{"type":"<errorType>","message":"<...>"}}
//
// + Flush. Used by the SSE emitter when an error surfaces AFTER
// Content-Type: text/event-stream headers were already written. Defined
// here so Plan 03's sse.go can call it without forcing a cross-file
// constructor for the envelope (errors.go owns the envelope shape).
//
// Encoder errors after WriteHeader cannot be reported to the client —
// they are silently dropped (the client has disconnected by
// definition).
func writeSSEError(w http.ResponseWriter, flusher http.Flusher, errorType, message string) {
	body, err := json.Marshal(errorEnvelope{
		Type:  "error",
		Error: errorInner{Type: errorType, Message: message},
	})
	if err != nil {
		// Marshalling a struct of strings cannot fail in practice;
		// guard with a fallback frame so the client still sees an
		// error event rather than a hung stream.
		body = []byte(`{"type":"error","error":{"type":"api_error","message":"internal error"}}`)
	}
	_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", body)
	flusher.Flush()
}

// writeJSON writes Content-Type: application/json + status 200 + the
// JSON-encoded body. Every successful (non-streaming) response in this
// package is 200; error responses go through writeError which sets the
// appropriate non-200 status.
//
// Encoder errors after WriteHeader cannot be reported to the client —
// they are silently dropped (chi accessLog records the response; the
// client has already disconnected by definition).
func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}
