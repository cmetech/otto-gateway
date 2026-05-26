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

	// Messages_ModelIDForms — kiro-cli advertises dot-separated Claude model
	// IDs (`claude-sonnet-4.6`) but Anthropic-API-compatible clients
	// (loop24-client, otto-cli, @anthropic-ai/sdk) send hyphen-separated IDs
	// (`claude-sonnet-4-6`). kiro-cli's session/set_model silently accepts the
	// unknown hyphen-form ID and the subsequent session/prompt then fails with
	// JSON-RPC -32603 ("Internal error").
	//
	// Regression bug (2026-05-26, debug session anthropic-acp-internal-err):
	// every /v1/messages call from a real otto client returned 500 because the
	// Anthropic adapter forwarded the hyphen-form model ID verbatim. Fix:
	// internal/adapter/anthropic/wire.go:normalizeClaudeModelID translates
	// `claude-FAMILY-MAJOR-MINOR[-YYYYMMDD]` → `claude-FAMILY-MAJOR.MINOR`
	// before storing into canonical.ChatRequest.Model. The response renderer
	// continues to echo the ORIGINAL wire ID so SDK clients see back what
	// they sent.
	//
	// Note: the prior Messages_Streaming / Messages_NonStreaming subtests
	// both use `model:"auto"`, which the engine special-cases to SKIP
	// SetModel entirely (engine/engine.go) — that is why this whole class
	// of failure went undetected against real kiro until a real Anthropic
	// SDK client was wired up.
	t.Run("Messages_ModelIDForms", func(t *testing.T) {
		cases := []struct {
			name      string
			modelWire string // what the client sends on the wire
		}{
			// Hyphen forms — the failure mode before the fix. All three were
			// 500s; all three must now be 200s with the wire ID echoed back.
			{"hyphen_sonnet_4_6", "claude-sonnet-4-6"},
			{"hyphen_haiku_4_5", "claude-haiku-4-5"},
			{"hyphen_dated_sonnet_4_5", "claude-sonnet-4-5-20250514"},
			// Dot forms — kiro-cli's native wire shape; must continue to
			// round-trip cleanly (no over-normalisation regression).
			{"dot_sonnet_4_6", "claude-sonnet-4.6"},
			{"dot_haiku_4_5", "claude-haiku-4.5"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				body := []byte(`{"model":"` + c.modelWire + `","max_tokens":256,"messages":[{"role":"user","content":"say hi"}]}`)
				resp := postMessages(t, baseURL, body, map[string]string{"Authorization": "Bearer e2e-token"})
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status: got %d, want 200 for model=%q (kiro -32603 regression — see debug session anthropic-acp-internal-err); body=%s",
						resp.StatusCode, c.modelWire, readAll(resp))
				}
				if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("Content-Type: got %q, want application/json prefix", ct)
				}
				var msg struct {
					Type    string `json:"type"`
					Role    string `json:"role"`
					Model   string `json:"model"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
					t.Fatalf("decode message: %v", err)
				}
				if msg.Type != "message" {
					t.Errorf("type: got %q, want message", msg.Type)
				}
				if msg.Role != "assistant" {
					t.Errorf("role: got %q, want assistant", msg.Role)
				}
				// A3 echo opaque (chatResponseToMessage): the response model
				// field MUST echo the wire ID verbatim — NOT the translated
				// kiro form. SDK clients pin model strings via equality
				// checks against the value they originally sent.
				if msg.Model != c.modelWire {
					t.Errorf("model echo: got %q, want %q (wire ID must echo back unchanged; canonical translation is internal to the adapter)",
						msg.Model, c.modelWire)
				}
				if len(msg.Content) == 0 || msg.Content[0].Text == "" {
					t.Errorf("content: empty (kiro returned no text — model translation likely broke against real kiro)")
				}
			})
		}
	})
}
