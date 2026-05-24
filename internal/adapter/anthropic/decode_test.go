package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testBody is a tiny target type for decodeJSONBody unit tests. Using a
// real wire type would couple this test to wire.go (Task 2) — we want
// decode.go to be testable in isolation.
type testBody struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

func TestDecodeJSONBody_Happy(t *testing.T) {
	body := strings.NewReader(`{"name":"ok","value":42}`)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", body)
	w := httptest.NewRecorder()

	var dst testBody
	if err := decodeJSONBody(w, r, 1<<20, &dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dst.Name != "ok" || dst.Value != 42 {
		t.Errorf("dst: got %+v, want {Name:ok Value:42}", dst)
	}
}

// TestDecodeJSONBody_OverCap proves the body-cap path returns an error
// that isMaxBytesError recognises. The cap is set to 8 bytes; the body
// is "{\"name\":\"more-than-eight\"}" which is far longer.
func TestDecodeJSONBody_OverCap(t *testing.T) {
	body := strings.NewReader(`{"name":"more-than-eight-bytes-of-payload"}`)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", body)
	w := httptest.NewRecorder()

	var dst testBody
	err := decodeJSONBody(w, r, 8, &dst)
	if err == nil {
		t.Fatal("decode: expected error, got nil")
	}
	if !isMaxBytesError(err) {
		t.Errorf("isMaxBytesError: false on over-cap body; err=%v", err)
	}
}

// TestDecodeJSONBody_InvalidJSON proves a syntactic error returns a
// non-MaxBytes error wrapped via fmt.Errorf("decode: %w", err).
func TestDecodeJSONBody_InvalidJSON(t *testing.T) {
	body := strings.NewReader(`{not-valid-json`)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", body)
	w := httptest.NewRecorder()

	var dst testBody
	err := decodeJSONBody(w, r, 1<<20, &dst)
	if err == nil {
		t.Fatal("decode: expected error, got nil")
	}
	if isMaxBytesError(err) {
		t.Errorf("isMaxBytesError: true on syntactic error; expected false")
	}
	if !strings.HasPrefix(err.Error(), "decode: ") {
		t.Errorf("err prefix: got %q, want starts with 'decode: '", err.Error())
	}
}

// TestDecodeJSONBody_UnknownFields_Permissive proves D-10 — anthropic-beta
// and cache_control fields (modeled here as any unknown field) do NOT
// cause errors. This is the key contract for SDK forward-compat.
func TestDecodeJSONBody_UnknownFields_Permissive(t *testing.T) {
	body := strings.NewReader(`{"name":"ok","value":42,"anthropic_beta":"foo","cache_control":{"type":"ephemeral"}}`)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", body)
	w := httptest.NewRecorder()

	var dst testBody
	if err := decodeJSONBody(w, r, 1<<20, &dst); err != nil {
		t.Fatalf("decode: %v (unknown fields must be ignored — D-10)", err)
	}
	if dst.Name != "ok" || dst.Value != 42 {
		t.Errorf("dst: got %+v, want known fields populated", dst)
	}
}
