package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// helper: invoke a stub handler with a JSON body and return the recorder.
func invokeStub(t *testing.T, a *Adapter, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)
	return w
}

// decodeNDJSON splits an NDJSON response into one map per non-empty line.
func decodeNDJSON(t *testing.T, body string) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("NDJSON line %q decode: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// ----------------------------------------------------------------------------
// /api/pull
// ----------------------------------------------------------------------------

// TestHandlePull_StreamTrue asserts D-15 byte-shape parity against Node
// (acp-ollama-server.js:1014-1024): NDJSON with exactly two lines —
// {status:"pulling manifest"} then {status:"success"}.
func TestHandlePull_StreamTrue(t *testing.T) {
	a := newTestAdapter(nil, nil)
	body := `{"name":"llama","stream":true}`
	w := invokeStub(t, a, http.MethodPost, "/pull", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type: got %q, want application/x-ndjson", ct)
	}
	lines := decodeNDJSON(t, w.Body.String())
	if len(lines) != 2 {
		t.Fatalf("NDJSON lines: got %d, want 2 (Node parity 1014-1024)", len(lines))
	}
	if lines[0]["status"] != "pulling manifest" {
		t.Errorf("line[0].status: got %q, want pulling manifest", lines[0]["status"])
	}
	if lines[1]["status"] != "success" {
		t.Errorf("line[1].status: got %q, want success", lines[1]["status"])
	}
}

// TestHandlePull_StreamFalse asserts the single-JSON envelope (Node line 1022).
func TestHandlePull_StreamFalse(t *testing.T) {
	a := newTestAdapter(nil, nil)
	body := `{"name":"llama","stream":false}`
	w := invokeStub(t, a, http.MethodPost, "/pull", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "success" {
		t.Errorf("status: got %q, want success (Node line 1022)", resp["status"])
	}
}

// TestHandlePull_StreamDefault (stream omitted) defaults to true per
// Node parity — same NDJSON two-line shape as StreamTrue.
func TestHandlePull_StreamDefault(t *testing.T) {
	a := newTestAdapter(nil, nil)
	body := `{"name":"llama"}`
	w := invokeStub(t, a, http.MethodPost, "/pull", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type: got %q, want application/x-ndjson (stream defaults to true)", ct)
	}
	lines := decodeNDJSON(t, w.Body.String())
	if len(lines) != 2 {
		t.Errorf("NDJSON lines: got %d, want 2 (stream default true)", len(lines))
	}
}

// ----------------------------------------------------------------------------
// /api/push
// ----------------------------------------------------------------------------

// TestHandlePush_StreamDefault per Node line 1026 + stubStreaming default
// (statusLine='success'). Push does not have the "pulling manifest"
// pre-status, so the NDJSON has exactly one line {status:"success"}.
func TestHandlePush_StreamDefault(t *testing.T) {
	a := newTestAdapter(nil, nil)
	body := `{}`
	w := invokeStub(t, a, http.MethodPost, "/push", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	lines := decodeNDJSON(t, w.Body.String())
	if len(lines) != 1 {
		t.Fatalf("NDJSON lines: got %d, want 1 (push has no pre-status)", len(lines))
	}
	if lines[0]["status"] != "success" {
		t.Errorf("line[0].status: got %q, want success (Node 1008-1012)", lines[0]["status"])
	}
}

// TestHandlePush_StreamFalse — single JSON envelope.
func TestHandlePush_StreamFalse(t *testing.T) {
	a := newTestAdapter(nil, nil)
	body := `{"stream":false}`
	w := invokeStub(t, a, http.MethodPost, "/push", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "success" {
		t.Errorf("status: got %q, want success (Node line 1026)", resp["status"])
	}
}

// ----------------------------------------------------------------------------
// /api/create
// ----------------------------------------------------------------------------

func TestHandleCreate_StreamDefault(t *testing.T) {
	a := newTestAdapter(nil, nil)
	w := invokeStub(t, a, http.MethodPost, "/create", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	lines := decodeNDJSON(t, w.Body.String())
	if len(lines) != 1 || lines[0]["status"] != "success" {
		t.Errorf("create NDJSON: got %+v, want [{status:success}] (Node 1027)", lines)
	}
}

func TestHandleCreate_StreamFalse(t *testing.T) {
	a := newTestAdapter(nil, nil)
	w := invokeStub(t, a, http.MethodPost, "/create", `{"stream":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "success" {
		t.Errorf("status: got %q, want success", resp["status"])
	}
}

// ----------------------------------------------------------------------------
// /api/copy
// ----------------------------------------------------------------------------

// TestHandleCopy_EmptyObject — Node line 1028: res.json({}). Body
// decodes to an empty map.
func TestHandleCopy(t *testing.T) {
	a := newTestAdapter(nil, nil)
	w := invokeStub(t, a, http.MethodPost, "/copy", `{"source":"a","destination":"b"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("body: got %+v, want empty object (Node line 1028)", resp)
	}
}

// ----------------------------------------------------------------------------
// /api/delete
// ----------------------------------------------------------------------------

// TestHandleDelete_EmptyObject — Node line 1029: res.json({}). DELETE
// verb routes via the chi router.
func TestHandleDelete(t *testing.T) {
	a := newTestAdapter(nil, nil)
	w := invokeStub(t, a, http.MethodDelete, "/delete", `{"name":"llama"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("body: got %+v, want empty object (Node line 1029)", resp)
	}
}
