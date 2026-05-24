//go:build e2e

// This file is part of package e2e_test (same package as e2e_test.go). It adds
// Ollama API contract coverage — the surface LangFlow consumes — mirroring the
// structure of the existing Anthropic subtests.
//
// It REUSES the shared helpers declared in e2e_test.go (gateOrSkip,
// bootGateway, resolveKiro, freePort, readAll, TestMain, moduleRoot) and MUST
// NOT redefine them — doing so would be a redeclaration compile error.
//
// Phase note: the current Ollama contract is NON-STREAMING (NDJSON streaming
// is Phase 4). This file asserts that current contract PLUS the silent
// stream:true → non-stream downgrade guard (handlers.go handleChat/handleGenerate
// set wire.Stream=false for Node parity), so a future Phase-4 NDJSON change is
// forced to update Chat_StreamDowngrade deliberately rather than silently.
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ollamaRequest builds a context-bounded request (60s, cancel registered via
// t.Cleanup to satisfy the noctx trust gate — mirrors postMessages), sets
// Content-Type application/json when a body is supplied, applies an optional
// auth header, and executes via http.DefaultClient. authHeader is the full
// header value (e.g. "Bearer e2e-token"); empty means no Authorization header.
func ollamaRequest(t *testing.T, method, url string, body []byte, authHeader string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		t.Fatalf("ollamaRequest: new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ollamaRequest: do %s %s: %v", method, url, err)
	}
	return resp
}

// TestE2E_Ollama boots ONE gateway (baseline env: default ENABLED_SURFACES so
// Ollama is mounted, AUTH_TOKEN=e2e-token, real kiro via KIRO_CMD) and runs the
// six Ollama contract cases as subtests sharing that single warmup.
func TestE2E_Ollama(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, nil)
	defer cleanup()

	const auth = "Bearer e2e-token"

	// 1. VersionAuthExempt — GET /api/version with NO auth. /api/version is
	// mounted on the OUTER (unauthenticated) router (Codex M-4 / AUTH-03):
	// LangFlow probes the version without creds, so this MUST be 200 with no
	// Authorization header. Asserts only that "version" and "commit" keys are
	// present strings — the values are build-dependent.
	t.Run("VersionAuthExempt", func(t *testing.T) {
		resp := ollamaRequest(t, http.MethodGet, baseURL+"/api/version", nil, "")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		var v struct {
			Version *string `json:"version"`
			Commit  *string `json:"commit"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatalf("decode version: %v", err)
		}
		if v.Version == nil {
			t.Error("version: key absent, want present string")
		}
		if v.Commit == nil {
			t.Error("commit: key absent, want present string")
		}
	})

	// 2. Unauthorized — POST /api/chat with NO auth → 401. Auth rejects before
	// kiro is touched (no warmup dependency).
	t.Run("Unauthorized", func(t *testing.T) {
		body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, "")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("no-auth status: got %d, want 401", resp.StatusCode)
		}
	})

	// 3. Tags — GET /api/tags (Bearer) → 200, non-empty models[] including an
	// entry named "auto" (always prepended by handleTags). Asserts only stable
	// fields (name, model, details.format, details.family); digest/size/
	// modified_at are intentionally NOT asserted (digest is "", size 0). Does
	// not require the engine.
	t.Run("Tags", func(t *testing.T) {
		resp := ollamaRequest(t, http.MethodGet, baseURL+"/api/tags", nil, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		var tags struct {
			Models []struct {
				Name    string `json:"name"`
				Model   string `json:"model"`
				Details struct {
					Format string `json:"format"`
					Family string `json:"family"`
				} `json:"details"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
			t.Fatalf("decode tags: %v", err)
		}
		if len(tags.Models) == 0 {
			t.Fatal("models: empty, want non-empty")
		}
		var autoEntry *struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Details struct {
				Format string `json:"format"`
				Family string `json:"family"`
			} `json:"details"`
		}
		for i := range tags.Models {
			if tags.Models[i].Name == "auto" {
				autoEntry = &tags.Models[i]
				break
			}
		}
		if autoEntry == nil {
			t.Fatalf("models: no entry named \"auto\"; got %+v", tags.Models)
		}
		if autoEntry.Name == "" {
			t.Error("auto entry name: empty")
		}
		if autoEntry.Model == "" {
			t.Error("auto entry model: empty")
		}
		if autoEntry.Details.Format == "" {
			t.Error("auto entry details.format: empty")
		}
		if autoEntry.Details.Family == "" {
			t.Error("auto entry details.family: empty")
		}
	})

	// 4. Chat_NonStreaming — POST /api/chat (Bearer, stream:false) → 200,
	// Content-Type application/json (NOT application/x-ndjson), a SINGLE JSON
	// object: model=="auto", message.role=="assistant", message.content
	// non-empty, done==true, done_reason ∈ {stop,length}. Real kiro — inherits
	// bootGateway warmup-skip. Durations / *_eval_count are non-deterministic
	// and intentionally NOT asserted.
	t.Run("Chat_NonStreaming", func(t *testing.T) {
		body := []byte(`{"model":"auto","messages":[{"role":"user","content":"say hi"}],"stream":false}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type: got %q, want application/json prefix", ct)
		}
		if strings.HasPrefix(ct, "application/x-ndjson") {
			t.Errorf("Content-Type: got %q, want NON-ndjson (non-streaming contract)", ct)
		}

		dec := json.NewDecoder(resp.Body)
		var chat struct {
			Model   string `json:"model"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			Done       bool   `json:"done"`
			DoneReason string `json:"done_reason"`
		}
		if err := dec.Decode(&chat); err != nil {
			t.Fatalf("decode chat: %v", err)
		}
		// Prove single JSON object (not NDJSON multi-frame): a second Decode
		// must return io.EOF.
		var throwaway json.RawMessage
		if err := dec.Decode(&throwaway); err != io.EOF {
			t.Errorf("second decode: got %v, want io.EOF (response must be a single JSON object)", err)
		}
		if chat.Model != "auto" {
			t.Errorf("model: got %q, want auto", chat.Model)
		}
		if chat.Message.Role != "assistant" {
			t.Errorf("message.role: got %q, want assistant", chat.Message.Role)
		}
		if chat.Message.Content == "" {
			t.Error("message.content: empty")
		}
		if !chat.Done {
			t.Error("done: got false, want true")
		}
		if chat.DoneReason != "stop" && chat.DoneReason != "length" {
			t.Errorf("done_reason: got %q, want one of {stop,length}", chat.DoneReason)
		}
	})

	// 5. Chat_StreamDowngrade — POST /api/chat (Bearer, stream:true) → 200.
	//
	// PHASE-2 PARITY GUARD: handlers.go handleChat silently sets wire.Stream=false
	// (Node parity), so the response goes through writeJSON: Content-Type
	// application/json and a SINGLE JSON object, NOT application/x-ndjson and NOT
	// multi-line NDJSON frames.
	//
	// >>> WHEN PHASE 4 LANDS NDJSON STREAMING, THIS SUBTEST MUST BE CHANGED <<<
	// to expect Content-Type application/x-ndjson and multi-line frames. Its
	// failure on a Phase-4 change is intentional — it forces a deliberate update.
	t.Run("Chat_StreamDowngrade", func(t *testing.T) {
		body := []byte(`{"model":"auto","messages":[{"role":"user","content":"say hi"}],"stream":true}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type: got %q, want application/json prefix (stream:true must be silently downgraded)", ct)
		}
		if strings.HasPrefix(ct, "application/x-ndjson") {
			t.Errorf("Content-Type: got %q — Phase-2 downgrade broken (got NDJSON). If Phase 4 landed, update this test.", ct)
		}

		dec := json.NewDecoder(resp.Body)
		var obj struct {
			Done bool `json:"done"`
		}
		if err := dec.Decode(&obj); err != nil {
			t.Fatalf("decode downgraded chat: %v", err)
		}
		var throwaway json.RawMessage
		if err := dec.Decode(&throwaway); err != io.EOF {
			t.Errorf("second decode: got %v, want io.EOF (downgrade must yield a single JSON object, not NDJSON frames)", err)
		}
		if !obj.Done {
			t.Error("done: got false, want true")
		}
	})

	// 6. Generate_NonStreaming — POST /api/generate (Bearer, stream:false) →
	// 200, a SINGLE JSON object whose assistant text lives in "response" (NOT
	// message{}, per render.go generateResponseToWire / wire.go
	// ollamaGenerateResponse). Asserts response non-empty and done==true.
	t.Run("Generate_NonStreaming", func(t *testing.T) {
		body := []byte(`{"model":"auto","prompt":"say hi","stream":false}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/generate", body, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		dec := json.NewDecoder(resp.Body)
		var gen struct {
			Response string `json:"response"`
			Done     bool   `json:"done"`
		}
		if err := dec.Decode(&gen); err != nil {
			t.Fatalf("decode generate: %v", err)
		}
		var throwaway json.RawMessage
		if err := dec.Decode(&throwaway); err != io.EOF {
			t.Errorf("second decode: got %v, want io.EOF (response must be a single JSON object)", err)
		}
		if gen.Response == "" {
			t.Error("response: empty")
		}
		if !gen.Done {
			t.Error("done: got false, want true")
		}
	})
}
