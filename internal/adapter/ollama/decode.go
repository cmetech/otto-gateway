package ollama

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// decodeJSONBody applies http.MaxBytesReader(max) to r.Body then JSON-decodes
// into dst. EVERY adapter handler that reads a request body MUST use this
// helper so the body size cap is uniformly enforced (Codex M-5 — previously
// only chat/generate carried a size cap, leaving stub endpoints + /api/show
// body-unbounded and exposed to OOM via a 1 GiB POST).
//
// Callers should check errors.Is/errors.As for *http.MaxBytesError and
// respond with http.StatusRequestEntityTooLarge (413); other errors render
// as http.StatusBadRequest (400). decodeJSONBody itself never writes to w
// — only the http.MaxBytesReader middleware may write the size-limit
// response header.
//
// Note: this helper deliberately does NOT call dec.DisallowUnknownFields.
// LangFlow / Ollama clients frequently send extra fields (keep_alive,
// options, etc.) that Phase 2 accepts-and-ignores. Failing on unknown
// fields would break SURF-05 (LangFlow zero-reconfig).
func decodeJSONBody[T any](w http.ResponseWriter, r *http.Request, maxBytes int64, dst *T) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return fmt.Errorf("decode: request body exceeds %d bytes: %w", maxBytes, err)
		}
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// isMaxBytesError reports whether err wraps an *http.MaxBytesError. Used
// by handlers to distinguish a 413 (body-cap exceeded) from a 400
// (malformed body) response status.
func isMaxBytesError(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}
