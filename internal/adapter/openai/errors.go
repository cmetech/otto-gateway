package openai

import (
	"encoding/json"
	"net/http"
)

// OpenAI error envelope per docs.openai.com/api-reference/errors and
// RESEARCH.md §Code Examples lines 498-513.
//
// Shape: {"error":{"message":"…","type":"…","param":null,"code":null}}
//
// This is DISTINCT from Anthropic's {"type":"error","error":{...}} shape.
// The outer wrapper has no "type" field; the inner error has Param and Code
// (both null in Phase 3) rather than Anthropic's single Type+Message.

// errorEnvelope is the outer OpenAI error envelope. Only one field —
// "error" — at the top level (no outer "type" field, unlike Anthropic).
type errorEnvelope struct {
	Error errorInner `json:"error"`
}

// errorInner is the inner error object. Param and Code are always null
// in Phase 3 (carry-forward from D-20); future phases may populate Code
// for machine-readable sub-categorization.
type errorInner struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"` // null
	Code    *string `json:"code"`  // null
}

// OpenAI error_type constants per RESEARCH.md §Code Examples lines 507-513
// and public OpenAI error spec. These string values are load-bearing —
// openai SDK clients use them to classify exceptions.
const (
	// errInvalidRequest — HTTP 400. Bad JSON, missing required field,
	// body too large (OpenAI has no distinct 413 type — same string).
	errInvalidRequest = "invalid_request_error"
	// errNotFound — HTTP 404. Unknown endpoint.
	errNotFound = "not_found_error"
	// errRequestTooLarge — HTTP 413. Body cap exceeded (4 MiB per chatBodyCap).
	// OpenAI uses invalid_request_error for this; no distinct type.
	errRequestTooLarge = "invalid_request_error"
	// errAPI — HTTP 500 / 503. Engine errors, pool errors, missing KIRO_CMD.
	// Message is always generic — T-02-33 forbids echoing the raw err string.
	errAPI = "api_error"
)

// writeError writes the OpenAI error envelope with the given HTTP status.
// Sets Content-Type BEFORE WriteHeader (order matters — once the status
// line flushes, header mutations are silently dropped, per Pitfall 2).
//
// The message MUST NOT contain request-body content (T-02-33). Callers
// constructing engine-error messages should log the raw err separately via
// the adapter's *slog.Logger and pass a generic message string here.
func writeError(w http.ResponseWriter, status int, errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoder errors here mean the client disconnected mid-write — the
	// response headers are already flushed so there is nothing useful to
	// recover. Discard explicitly to satisfy errcheck.
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		Error: errorInner{
			Type:    errorType,
			Message: message,
		},
	})
}

// writeJSON writes Content-Type: application/json + status 200 + the
// JSON-encoded body. Every successful (non-streaming) response from this
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
