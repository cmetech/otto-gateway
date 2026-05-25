//go:build e2e

// This file is part of package e2e_test (same package as e2e_test.go). It adds
// Ollama API contract coverage — the surface LangFlow consumes — mirroring the
// structure of the existing Anthropic subtests.
//
// It REUSES the shared helpers declared in e2e_test.go (gateOrSkip,
// bootGateway, resolveKiro, freePort, readAll, TestMain, moduleRoot) and MUST
// NOT redefine them — doing so would be a redeclaration compile error.
//
// Phase 4 Plan 02: NDJSON streaming subtests added (Chat_Streaming, Generate_Streaming,
// Chat_DisconnectSmoke). The Phase-2 non-streaming downgrade guard is defused (Pitfall 5).
package e2e_test

import (
	"bufio"
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

	// 5. Chat_Streaming — POST /api/chat (Bearer, absent stream field → default
	// stream:true) → 200, Content-Type application/x-ndjson, at least 2 NDJSON
	// lines, last line has done:true and done_reason ∈ {stop,length}. Real kiro.
	//
	// Phase 4 Plan 02: handlers.go now streams NDJSON by default. This subtest
	// replaces the old Phase-2 non-streaming downgrade guard (RESEARCH.md Pitfall 5).
	t.Run("Chat_Streaming", func(t *testing.T) {
		// No stream field — tests absent=true default (streamEnabled returns true for nil).
		body := []byte(`{"model":"auto","messages":[{"role":"user","content":"say hi"}]}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/x-ndjson") {
			t.Errorf("Content-Type: got %q, want application/x-ndjson prefix", ct)
		}

		scanner := bufio.NewScanner(resp.Body)
		var lastLine struct {
			Done       bool   `json:"done"`
			DoneReason string `json:"done_reason"`
		}
		lineCount := 0
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			if err := json.Unmarshal(line, &lastLine); err != nil {
				t.Fatalf("malformed NDJSON line: %v — line: %s", err, line)
			}
			lineCount++
		}
		if lineCount < 2 {
			t.Errorf("NDJSON stream: got %d lines, want at least 2 (some done:false + final done:true)", lineCount)
		}
		if !lastLine.Done {
			t.Error("last NDJSON line: done==false, want true")
		}
		if lastLine.DoneReason != "stop" && lastLine.DoneReason != "length" {
			t.Errorf("last NDJSON line done_reason: got %q, want stop or length", lastLine.DoneReason)
		}
	})

	// 5b. Generate_Streaming — POST /api/generate (Bearer, absent stream field) →
	// 200, Content-Type application/x-ndjson, at least 2 NDJSON lines with
	// `response` field (not `message`), last line has done:true.
	t.Run("Generate_Streaming", func(t *testing.T) {
		body := []byte(`{"model":"auto","prompt":"say hi"}`)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/generate", body, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/x-ndjson") {
			t.Errorf("Content-Type: got %q, want application/x-ndjson prefix", ct)
		}

		scanner := bufio.NewScanner(resp.Body)
		var lastLine struct {
			Response string `json:"response"`
			Done     bool   `json:"done"`
		}
		lineCount := 0
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			if err := json.Unmarshal(line, &lastLine); err != nil {
				t.Fatalf("malformed NDJSON line: %v — line: %s", err, line)
			}
			lineCount++
		}
		if lineCount < 2 {
			t.Errorf("NDJSON stream: got %d lines, want at least 2", lineCount)
		}
		if !lastLine.Done {
			t.Error("last NDJSON line: done==false, want true")
		}
	})

	// 5c. Chat_DisconnectSmoke — proves SC4: pool-of-1 slot survives a
	// mid-stream client disconnect. Health check asserts no slot restart
	// (Codex MEDIUM disconnect smoke concern). Full slot-release-on-cancel
	// semantics harden in Phase 5.
	t.Run("Chat_DisconnectSmoke", func(t *testing.T) {
		// Chat_DisconnectSmoke proves SC4: pool-of-1 slot survives session/cancel
		// on mid-stream client disconnect. Health check asserts no slot restart
		// (Codex MEDIUM concern). Full slot-release-on-cancel semantics harden in Phase 5.

		// a. GET /health before starting the stream; record pool.alive count.
		healthBefore := getHealthPoolAlive(t, baseURL)

		// b. Start a streaming /api/chat request (no stream field → stream:true default).
		body := []byte(`{"model":"auto","messages":[{"role":"user","content":"say hi"}]}`)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/chat", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("Chat_DisconnectSmoke: new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Chat_DisconnectSmoke: do request: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			t.Fatalf("Chat_DisconnectSmoke: status: got %d, want 200", resp.StatusCode)
		}

		// c. Read exactly one NDJSON line from the response body, then close
		// the body mid-stream (simulates client disconnect).
		scanner := bufio.NewScanner(resp.Body)
		if scanner.Scan() {
			// Read one line — that's enough to confirm streaming started.
			_ = scanner.Bytes()
		}
		_ = resp.Body.Close() // mid-stream disconnect

		// d. Wait 300ms for kiro-cli to process the cancel and the slot to settle.
		time.Sleep(300 * time.Millisecond)

		// e. GET /health again; assert pool.alive count is the same (slot did NOT
		// crash and restart — proves the pool did not restart the worker, not just
		// that a follow-up request succeeds).
		healthAfter := getHealthPoolAlive(t, baseURL)
		if healthAfter != healthBefore {
			t.Errorf("pool.alive changed after mid-stream disconnect: before=%d after=%d (slot restarted — SC4 violated)", healthBefore, healthAfter)
		}

		// f. Issue a fresh POST /api/chat with stream:false and assert 200 with
		// non-empty response — proves the slot is reusable after cancel.
		followup := []byte(`{"model":"auto","messages":[{"role":"user","content":"say hi again"}],"stream":false}`)
		followResp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", followup, auth)
		defer func() { _ = followResp.Body.Close() }()
		if followResp.StatusCode != http.StatusOK {
			t.Fatalf("Chat_DisconnectSmoke: follow-up request: status=%d (slot not reusable after cancel)", followResp.StatusCode)
		}
		var chat struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done bool `json:"done"`
		}
		if err := json.NewDecoder(followResp.Body).Decode(&chat); err != nil {
			t.Fatalf("Chat_DisconnectSmoke: decode follow-up: %v", err)
		}
		if chat.Message.Content == "" {
			t.Error("Chat_DisconnectSmoke: follow-up response: content empty (slot may be stuck)")
		}
		if !chat.Done {
			t.Error("Chat_DisconnectSmoke: follow-up response: done==false")
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

// getHealthPoolAlive performs a GET /health and returns the pool.alive count.
// It is used by Chat_DisconnectSmoke to assert that pool slots are not restarted
// after a mid-stream client disconnect (SC4).
func getHealthPoolAlive(t *testing.T, baseURL string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		t.Fatalf("getHealthPoolAlive: new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("getHealthPoolAlive: do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("getHealthPoolAlive: status %d", resp.StatusCode)
	}
	var body struct {
		Pool struct {
			Alive int `json:"alive"`
		} `json:"pool"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("getHealthPoolAlive: decode: %v", err)
	}
	return body.Pool.Alive
}
