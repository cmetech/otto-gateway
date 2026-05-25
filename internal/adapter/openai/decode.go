package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// decodeJSONBody applies http.MaxBytesReader(max) to r.Body then JSON-decodes
// into dst. handleChatCompletions uses this helper so the body size cap is
// uniformly enforced (mirrors Codex M-5 from Phase 2 Ollama).
//
// Callers should check errors.Is/errors.As for *http.MaxBytesError via the
// isMaxBytesError helper and respond with http.StatusRequestEntityTooLarge
// (413, request_too_large); other errors render as http.StatusBadRequest
// (400, invalid_request_error). decodeJSONBody itself never writes to w —
// only the http.MaxBytesReader middleware may write the size-limit
// response header.
//
// Note: this helper deliberately does NOT call dec.DisallowUnknownFields.
// Per D-10, stream_options, logprobs, and any new SDK-side additions are
// accepted-and-ignored. Failing on unknown fields would break compatibility
// with openai SDK releases that add new request fields (e.g., future
// stream_options variants).
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
// by handleChatCompletions to distinguish a 413 request_too_large from a
// 400 invalid_request_error response.
func isMaxBytesError(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}
