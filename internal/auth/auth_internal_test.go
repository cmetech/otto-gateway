package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestWriteOllamaError_Shape is a whitebox test — writeOllamaError is
// package-private, so this lives in `package auth` (not `package auth_test`)
// to exercise it directly. The contract is locked verbatim against the Node
// reference: Content-Type=application/json + status + JSON body {"error": "<msg>"}.
func TestWriteOllamaError_Shape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeOllamaError(rec, http.StatusUnauthorized, "Invalid or missing API key")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}

	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v (raw=%q)", err, rec.Body.String())
	}
	want := map[string]string{"error": "Invalid or missing API key"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("body: want %v, got %v", want, got)
	}
}
