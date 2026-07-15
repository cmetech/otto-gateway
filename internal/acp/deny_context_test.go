package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
)

// TestWithDenyBuiltinTools_RoundTrip verifies the context carrier round-trips
// the deny signal, and that an absent value (bare context.Background())
// defaults to false — the auto-grant default (Track 3a).
func TestWithDenyBuiltinTools_RoundTrip(t *testing.T) {
	ctx := WithDenyBuiltinTools(context.Background(), true)
	if got := DenyBuiltinTools(ctx); !got {
		t.Errorf("DenyBuiltinTools(WithDenyBuiltinTools(ctx,true)) = %v, want true", got)
	}

	if got := DenyBuiltinTools(context.Background()); got {
		t.Errorf("DenyBuiltinTools(bare ctx) = %v, want false", got)
	}
}

// TestPrompt_SetsStreamDenyFromContext verifies Client.Prompt reads the
// Track 3a deny signal off the caller's ctx and stores it on the returned
// Stream before the stream is published as c.activeStream.
func TestPrompt_SetsStreamDenyFromContext(t *testing.T) {
	t.Run("explicit_true", func(t *testing.T) {
		runPromptDenyScenario(t, true, true)
	})
	t.Run("absent_defaults_false", func(t *testing.T) {
		runPromptDenyScenario(t, false, false)
	})
}

// runPromptDenyScenario drives a full initialize -> session/new ->
// session/prompt dialogue against a mockRWC. If wrapCtx is true, the Prompt
// ctx is wrapped with WithDenyBuiltinTools(ctx, deny); otherwise a bare ctx
// is used (exercising the "absent" default). Asserts stream.denyTools()
// matches want immediately after Prompt returns.
func runPromptDenyScenario(t *testing.T, wrapCtx bool, want bool) {
	t.Helper()

	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	serveErr := make(chan error, 1)
	go func() {
		for _, step := range []string{"initialize", "session/new", "session/prompt"} {
			line, err := readLineFromPipe(mock.serverRead)
			if err != nil {
				serveErr <- fmt.Errorf("read %s: %w", step, err)
				return
			}
			var req map[string]any
			if err := json.Unmarshal(line, &req); err != nil {
				serveErr <- fmt.Errorf("unmarshal %s: %w", step, err)
				return
			}
			gotMethod, _ := req["method"].(string)
			if gotMethod != step {
				serveErr <- fmt.Errorf("expected method %q, got %q", step, gotMethod)
				return
			}
			var result map[string]any
			switch step {
			case "initialize":
				result = map[string]any{}
			case "session/new":
				result = map[string]any{"sessionId": "sess-1"}
			case "session/prompt":
				result = map[string]any{"stopReason": "end_turn"}
			}
			if err := mock.serverWriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  result,
			}); err != nil {
				serveErr <- fmt.Errorf("write %s response: %w", step, err)
				return
			}
		}
		serveErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	sid, err := c.NewSession(ctx, "/tmp")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	promptCtx := ctx
	if wrapCtx {
		promptCtx = WithDenyBuiltinTools(ctx, want)
	}

	stream, err := c.Prompt(promptCtx, sid, []canonical.Block{
		{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// The deny flag is set before the wire send, so it's observable
	// immediately after Prompt returns — no need to wait for the response.
	if got := stream.denyTools(); got != want {
		t.Errorf("stream.denyTools() = %v, want %v", got, want)
	}

	// Drain Chunks so the readLoop / awaitPromptResult goroutines finish
	// cleanly (goleak.VerifyNone in the deferred cleanup above).
	for range stream.Chunks {
	}

	if _, err := stream.Result(); err != nil {
		t.Fatalf("stream.Result: %v", err)
	}

	if err := <-serveErr; err != nil {
		t.Fatalf("responder: %v", err)
	}
}
