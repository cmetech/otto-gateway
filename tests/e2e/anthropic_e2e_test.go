//go:build e2e

// This file is part of package e2e_test (same package as e2e_test.go). It adds
// a dedicated TestE2E_Anthropic test function with Messages_Streaming and
// Messages_NonStreaming subtests that ratify STRM-02, STRM-03, and STRM-05 for
// the Anthropic SSE surface (Phase 4, Plan 04).
//
// It REUSES the shared helpers declared in e2e_test.go (gateOrSkip,
// bootGateway, readAll, postMessages, assertStrictSSE, assertMessageShape) and
// MUST NOT redefine any of them — doing so would be a redeclaration compile
// error. The generic Anthropic round-trip coverage already in TestE2E_SharedGateway
// (NonStreaming_XApiKey, NonStreaming_Bearer, Streaming_SSE) remains intact;
// this file adds the named subtests the plan requires for ratification.
//
// Anthropic D-05 exemption note (documented here per plan + CONTEXT.md):
// Anthropic's finalizeStream emits `event: error` on Result() error. This is
// NOT a D-05 violation. D-05 applies only to surfaces without a spec-mandated
// error frame. The Anthropic public spec explicitly documents `event: error` as
// the terminal frame on stream errors; @anthropic-ai/sdk expects it. Anthropic
// is explicitly exempt from D-05.
package e2e_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestE2E_Anthropic boots ONE gateway (default ENABLED_SURFACES so Anthropic is
// mounted, AUTH_TOKEN=e2e-token, real kiro via KIRO_CMD) and runs the Anthropic
// Messages API contract cases as subtests sharing that single warmup.
func TestE2E_Anthropic(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, nil)
	defer cleanup()

	// Messages_Streaming — POST /v1/messages (x-api-key, stream:true) → 200,
	// Content-Type text/event-stream, well-formed Anthropic SSE sequence:
	// message_start, content_block_delta, message_stop events present.
	//
	// Messages_Streaming ratifies STRM-02 (Anthropic SSE) and STRM-03 (same
	// canonical channel as Ollama and OpenAI — all three surfaces call
	// engine.Run().Stream().Chunks() via the engine adapter).
	//
	// Note: Anthropic mid-stream Result() errors emit event:error per Anthropic
	// spec — this is explicitly NOT a D-05 violation; D-05 applies only to
	// surfaces without spec-mandated error frames (Ollama NDJSON, OpenAI SSE).
	t.Run("Messages_Streaming", func(t *testing.T) {
		body := []byte(`{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"say hi"}],"stream":true}`)
		resp := postMessages(t, baseURL, body, map[string]string{"x-api-key": "e2e-token"})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Errorf("Content-Type: got %q, want text/event-stream prefix", ct)
		}
		// assertStrictSSE walks the full Anthropic SSE event sequence using the
		// strict state machine (expectingEvent→expectingData→expectingBlank) and
		// asserts message_start is first, message_stop is last, and
		// content_block_start / content_block_delta / content_block_stop /
		// message_delta all appear. It also asserts no error event on the happy path.
		assertStrictSSE(t, resp)
	})

	// Messages_NonStreaming — POST /v1/messages without stream field (default
	// → false per Anthropic spec, routes to engine.Collect) → 200,
	// application/json, single JSON Anthropic message object.
	//
	// Messages_NonStreaming ratifies STRM-05 (stream:false regression for
	// Anthropic surface). Anthropic wire.Stream is bool (absent=false); this
	// matches the Anthropic public spec default and is correct behavior — the
	// *bool fix applies only to Ollama where the spec says stream:true is the
	// default.
	t.Run("Messages_NonStreaming", func(t *testing.T) {
		// No stream field — absent=false per Anthropic spec, routes to Collect.
		body := []byte(`{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"say hi"}]}`)
		resp := postMessages(t, baseURL, body, map[string]string{"Authorization": "Bearer e2e-token"})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type: got %q, want application/json prefix", ct)
		}

		dec := json.NewDecoder(resp.Body)
		var msg struct {
			Type       string  `json:"type"`
			Role       string  `json:"role"`
			StopReason *string `json:"stop_reason"`
			Content    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		// Single JSON object (not an SSE stream): a second Decode must return io.EOF.
		var throwaway json.RawMessage
		if err := dec.Decode(&throwaway); err != io.EOF {
			t.Errorf("second decode: got %v, want io.EOF (response must be a single JSON object)", err)
		}
		if msg.Type != "message" {
			t.Errorf("type: got %q, want message", msg.Type)
		}
		if msg.Role != "assistant" {
			t.Errorf("role: got %q, want assistant", msg.Role)
		}
		if msg.StopReason == nil {
			t.Error("stop_reason: got nil, want non-nil")
		} else if *msg.StopReason == "" {
			t.Error("stop_reason: empty string, want non-empty (e.g. end_turn)")
		}
		if len(msg.Content) == 0 {
			t.Fatal("content: empty (kiro-cli returned no blocks)")
		}
		if msg.Content[0].Type != "text" {
			t.Errorf("content[0].type: got %q, want text", msg.Content[0].Type)
		}
		if msg.Content[0].Text == "" {
			t.Error("content[0].text: empty")
		}
	})
}
