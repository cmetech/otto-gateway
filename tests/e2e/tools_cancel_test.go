//go:build e2e

// tools_cancel_test.go — Phase 6 Plan 06-05 Task 3 D-17 scenario 12 (mid-
// stream cancel during tool_call) verified per-surface via the fake-kiro
// frame-log seam (REVIEW HIGH #5).
//
// Each subtest:
//  1. defer GoleakVerifyAtEnd(t) as the first line inside t.Run
//     (per CONTEXT D-21).
//  2. Build notifications with a TRAILING NotifText so the stream has time
//     to disconnect mid-flight.
//  3. FakeKiro(t, Script{Notifications: notifs, LogFrames: true}) — the
//     LogFrames=true sets OTTO_FAKE_KIRO_RECEIVED_FRAMES_FILE so the fake
//     logs every received frame for assertion.
//  4. POST streaming request with context.WithCancel.
//  5. Read a few response lines, then cancel the context.
//  6. Sleep to let the gateway propagate cancel.
//  7. ReadFakeKiroFrames + assert a `session/cancel` frame was emitted.
//  8. Make a fresh non-streaming request → asserts the pool slot survived
//     (Phase 5 dead-slot discipline + Phase 4 D-06 watchdog).
package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"
)

func TestE2E_Tools_Cancel(t *testing.T) {
	gateOrSkip(t)
	const auth = "Bearer e2e-token"

	// Notifications: long-running tool_call sandwiched between text frames.
	// The trailing text gives the disconnect a window to land DURING the
	// stream (before the fake's session/prompt response).
	makeNotifs := func() []byte {
		return ConcatNotifs(
			NotifText(ollamaSessionID, "starting"),
			NotifToolCall(ollamaSessionID, "tc_cancel", "search_web", map[string]any{"query": "long running"}),
			NotifText(ollamaSessionID, "more text after tool call"),
		)
	}

	t.Run("Ollama_CancelDuringToolCall", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		cmd, env := FakeKiro(t, Script{Notifications: makeNotifs(), LogFrames: true, StopReason: "end_turn"})
		framesPath := env["OTTO_FAKE_KIRO_RECEIVED_FRAMES_FILE"]
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("query", ToolsCatalog, true)
		ctx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/chat", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatalf("do: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			cancel()
			t.Fatalf("status: got %d, want 200", resp.StatusCode)
		}

		// Read a few NDJSON lines, then cancel.
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		readCount := 0
		for sc.Scan() && readCount < 2 {
			readCount++
		}
		cancel()
		_ = resp.Body.Close()

		// WR-09 (Phase 6 review): wait for session/cancel propagation by
		// polling the frame log instead of a fixed 300ms sleep. The poll
		// loop returns as soon as the cancel frame appears (fast on a
		// quiet dev box) and tolerates a slower loaded CI runner via the
		// longer deadline.
		frames := waitForSessionCancel(t, framesPath, 5*time.Second)
		assertSessionCancelEmitted(t, frames)

		// Slot survival: fresh non-streaming request.
		assertSlotSurvives(t, baseURL, "/api/chat", OllamaToolsRequest("hi", nil, false), auth)
	})

	t.Run("OpenAI_CancelDuringToolCall", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		cmd, env := FakeKiro(t, Script{Notifications: makeNotifs(), LogFrames: true, StopReason: "end_turn"})
		framesPath := env["OTTO_FAKE_KIRO_RECEIVED_FRAMES_FILE"]
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("query", ToolsCatalog, true)
		ctx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatalf("do: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			cancel()
			t.Fatalf("status: got %d, want 200", resp.StatusCode)
		}

		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		readCount := 0
		for sc.Scan() && readCount < 2 {
			readCount++
		}
		cancel()
		_ = resp.Body.Close()
		// WR-09: poll for the cancel frame; see waitForSessionCancel comment.
		frames := waitForSessionCancel(t, framesPath, 5*time.Second)
		assertSessionCancelEmitted(t, frames)
		assertSlotSurvives(t, baseURL, "/v1/chat/completions", OpenAIToolsRequest("hi", nil, false), auth)
	})

	t.Run("Anthropic_CancelDuringToolCall", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		cmd, env := FakeKiro(t, Script{Notifications: makeNotifs(), LogFrames: true, StopReason: "end_turn"})
		framesPath := env["OTTO_FAKE_KIRO_RECEIVED_FRAMES_FILE"]
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := AnthropicToolsRequest("query", ToolsCatalog, true)
		ctx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatalf("do: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			cancel()
			t.Fatalf("status: got %d, want 200", resp.StatusCode)
		}

		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		readCount := 0
		for sc.Scan() && readCount < 2 {
			readCount++
		}
		cancel()
		_ = resp.Body.Close()
		// WR-09: poll for the cancel frame; see waitForSessionCancel comment.
		frames := waitForSessionCancel(t, framesPath, 5*time.Second)
		assertSessionCancelEmitted(t, frames)
		assertSlotSurvives(t, baseURL, "/v1/messages", AnthropicToolsRequest("hi", nil, false), auth)
	})
}

// waitForSessionCancel polls the frame-log file with a tight loop until a
// `session/cancel` frame appears or the deadline elapses. Replaces the
// fixed 300ms sleep with a fast best-case (returns as soon as the frame
// is observed) and a tolerant worst-case (5s deadline by default). This
// avoids both the flake mode (slow CI runner: 300ms is not enough) and
// the waste mode (fast dev box: 300ms is unnecessary). Returns the final
// frame snapshot regardless of whether the cancel was observed — the
// caller's assertSessionCancelEmitted will fail with a useful frame dump
// if it wasn't.
func waitForSessionCancel(t *testing.T, framesPath string, timeout time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var frames []map[string]any
	for time.Now().Before(deadline) {
		frames = ReadFakeKiroFrames(t, framesPath)
		for _, frame := range frames {
			if m, _ := frame["method"].(string); m == "session/cancel" {
				return frames
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return frames
}

// assertSessionCancelEmitted scans the frame-log for a frame whose method
// field is `session/cancel`. The frame-log captures EVERY frame the fake
// kiro received from the gateway, so the assertion is independent of timing.
func assertSessionCancelEmitted(t *testing.T, frames []map[string]any) {
	t.Helper()
	for _, frame := range frames {
		if m, _ := frame["method"].(string); m == "session/cancel" {
			return
		}
	}
	t.Errorf("session/cancel frame was not emitted to fake-kiro (Phase 4 D-06 watchdog regression); frames=%+v", frames)
}

// assertSlotSurvives makes a fresh non-streaming request against the same
// gateway and asserts a successful response within 5s — proves the pool slot
// is still healthy after the mid-stream cancel (Phase 5 dead-slot survival).
func assertSlotSurvives(t *testing.T, baseURL, path string, body []byte, auth string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("assertSlotSurvives: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if path == "/v1/messages" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("assertSlotSurvives: do (slot did not survive): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("assertSlotSurvives: status=%d (slot did not survive after cancel; Phase 5 dead-slot regression)", resp.StatusCode)
	}
}
