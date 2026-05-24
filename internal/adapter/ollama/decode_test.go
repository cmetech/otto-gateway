package ollama

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type smallBody struct {
	Field string `json:"field"`
}

// TestDecodeJSONBody_HappyPath proves the helper round-trips a tiny body
// under a tiny cap (Codex M-5 success path).
func TestDecodeJSONBody_HappyPath(t *testing.T) {
	body := `{"field":"hello"}`
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", strings.NewReader(body))
	w := httptest.NewRecorder()

	var dst smallBody
	if err := decodeJSONBody(w, r, 1<<10, &dst); err != nil {
		t.Fatalf("decodeJSONBody: %v", err)
	}
	if dst.Field != "hello" {
		t.Errorf("dst.Field: got %q, want %q", dst.Field, "hello")
	}
}

// TestDecodeJSONBody_ExceedsLimit proves the helper rejects a 5 MiB body
// under a 4 MiB cap with an *http.MaxBytesError — the load-bearing
// guarantee of Codex M-5 / T-02-29 / T-02-45.
//
// We construct a valid JSON envelope containing a 5 MiB string field so
// the decoder must read past the 4 MiB cap before classifying the body —
// the *http.MaxBytesError surfaces during the decoder's Read call. A
// non-JSON 5 MiB blob would short-circuit on the JSON-parser error
// before the cap fires; that is not the cap path under test.
func TestDecodeJSONBody_ExceedsLimit(t *testing.T) {
	// Build a JSON body with a ~5 MiB string field.
	pad := strings.Repeat("a", 5<<20)
	body := `{"field":"` + pad + `"}`
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", strings.NewReader(body))
	w := httptest.NewRecorder()

	var dst smallBody
	err := decodeJSONBody(w, r, 4<<20, &dst)
	if err == nil {
		t.Fatal("decodeJSONBody returned nil error on 5 MiB body under 4 MiB cap (Codex M-5 invariant violated)")
	}
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		t.Errorf("decodeJSONBody err: want *http.MaxBytesError wrapped, got %T: %v", err, err)
	}
	if !isMaxBytesError(err) {
		t.Errorf("isMaxBytesError returned false for *http.MaxBytesError-wrapped err (helper broken)")
	}
}

// TestDecodeJSONBody_MalformedJSON proves the helper distinguishes
// malformed JSON from oversized bodies — the caller responds 400 vs 413.
func TestDecodeJSONBody_MalformedJSON(t *testing.T) {
	body := `{not-valid-json`
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", strings.NewReader(body))
	w := httptest.NewRecorder()

	var dst smallBody
	err := decodeJSONBody(w, r, 1<<10, &dst)
	if err == nil {
		t.Fatal("decodeJSONBody returned nil on malformed JSON")
	}
	if isMaxBytesError(err) {
		t.Errorf("malformed JSON should NOT register as MaxBytesError (helper is mis-classifying decode errors)")
	}
}
