package anthropic

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestErrorTypeConstants pins the 8 error_type literal strings against
// D-20. Drift here breaks @anthropic-ai/sdk's error-class mapping.
func TestErrorTypeConstants(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{errInvalidRequest, "invalid_request_error"},
		{errAuthentication, "authentication_error"},
		{errPermission, "permission_error"},
		{errNotFound, "not_found_error"},
		{errRequestTooLarge, "request_too_large"},
		{errRateLimit, "rate_limit_error"},
		{errAPI, "api_error"},
		{errOverloaded, "overloaded_error"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("error_type constant: got %q, want %q", c.got, c.want)
		}
	}
}

// TestWriteError_EnvelopeShape walks every error_type + status pair from
// D-20 and asserts the wire body decodes to the expected envelope.
func TestWriteError_EnvelopeShape(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		errorType string
		message   string
	}{
		{"invalid_request", http.StatusBadRequest, errInvalidRequest, "bad"},
		{"authentication", http.StatusUnauthorized, errAuthentication, "no creds"},
		{"permission", http.StatusForbidden, errPermission, "ip denied"},
		{"not_found", http.StatusNotFound, errNotFound, "no endpoint"},
		{"request_too_large", http.StatusRequestEntityTooLarge, errRequestTooLarge, "too big"},
		{"rate_limit", http.StatusTooManyRequests, errRateLimit, "slow down"},
		{"api_error", http.StatusInternalServerError, errAPI, "internal error"},
		{"overloaded", 529, errOverloaded, "pool empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, c.status, c.errorType, c.message)

			if w.Code != c.status {
				t.Errorf("status: got %d, want %d", w.Code, c.status)
			}
			ct := w.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type: got %q, want application/json", ct)
			}

			var env errorEnvelope
			if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
				t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
			}
			if env.Type != "error" {
				t.Errorf("envelope.Type: got %q, want %q", env.Type, "error")
			}
			if env.Error.Type != c.errorType {
				t.Errorf("envelope.Error.Type: got %q, want %q", env.Error.Type, c.errorType)
			}
			if env.Error.Message != c.message {
				t.Errorf("envelope.Error.Message: got %q, want %q", env.Error.Message, c.message)
			}
		})
	}
}

// TestWriteError_GoldenBytes pins the exact wire bytes for one
// representative error so future drift in json.NewEncoder behaviour
// (e.g., a Go release that changes whitespace) trips an alarm.
func TestWriteError_GoldenBytes(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, errInvalidRequest, "anthropic-version header is required")

	// json.NewEncoder.Encode appends a trailing newline.
	want := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"anthropic-version header is required"}}` + "\n")
	if !bytes.Equal(w.Body.Bytes(), want) {
		t.Errorf("body bytes:\n got:  %q\n want: %q", w.Body.Bytes(), want)
	}
}

// TestWriteJSON_HappyPath proves Content-Type + 200 + body encoding.
func TestWriteJSON_HappyPath(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, map[string]string{"hello": "world"})

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	var got map[string]string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["hello"] != "world" {
		t.Errorf("body: got %v, want hello=world", got)
	}
}

// flushRecorder wraps httptest.ResponseRecorder to satisfy http.Flusher
// (the recorder itself does not implement Flush). Used by the
// writeSSEError test only.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed int
}

func (f *flushRecorder) Flush() { f.flushed++ }

// TestWriteSSEError_FrameShape proves the mid-stream error frame is
// `event: error\ndata: <json>\n\n` and that Flush is called.
func TestWriteSSEError_FrameShape(t *testing.T) {
	fr := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	writeSSEError(fr, fr, errInvalidRequest, "bad inbound")

	body := fr.Body.String()
	if !strings.HasPrefix(body, "event: error\ndata: ") {
		t.Errorf("body prefix: got %q, want 'event: error\\ndata: '", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("body suffix: got %q, want frame terminator '\\n\\n'", body)
	}
	if fr.flushed != 1 {
		t.Errorf("flushed: got %d, want 1", fr.flushed)
	}
	// The data portion must be a valid envelope.
	dataStart := strings.Index(body, "data: ") + len("data: ")
	dataEnd := strings.Index(body[dataStart:], "\n") + dataStart
	var env errorEnvelope
	if err := json.Unmarshal([]byte(body[dataStart:dataEnd]), &env); err != nil {
		t.Fatalf("data line is not valid JSON: %v; line=%q", err, body[dataStart:dataEnd])
	}
	if env.Type != "error" || env.Error.Type != errInvalidRequest {
		t.Errorf("envelope: got %+v, want type=error, error.type=invalid_request_error", env)
	}
}
