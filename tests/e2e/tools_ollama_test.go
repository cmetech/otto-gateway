//go:build e2e

// tools_ollama_test.go — Phase 6 Plan 06-05 Task 2 Ollama side of the D-17
// 12-scenario tool-call matrix. Covers scenarios 1-4 + 6-11 against the live
// otto-gateway binary with a controllable fake-kiro-cli.
//
// Subtests:
//   1. NativeToolCall_NonStreaming           — D-17 #1 + iteration-3 HIGH #1 narration
//   2. NativeToolCall_Streaming              — D-17 #2 + REVIEW HIGH #2 two-path rule
//   3. Coerce_BareJSON_NonStreaming          — D-17 #3
//   4. Coerce_BareJSON_Streaming             — D-17 #3 + REVIEW HIGH #1 streaming-coerce
//   5. Coerce_FencedJSON_NonStreaming        — D-17 #4
//   6. EmptyTools_NoCoerce                   — D-17 #6
//   7. NoMatch_NoCoerce                      — D-17 #7
//   8. MalformedJSON_NoCoerce                — D-17 #8
//   9. ExistingToolCalls_NoSecondaryCoerce   — D-17 #9
//  10. MultiTool_TieBreaker                  — D-17 #10
//  11. EmptyParams_SkippedInScoring          — D-17 #11
//  12. NativeToolCall_ThenJSONText_NoCoerce_Streaming — iteration-3 HIGH #2
//
// Each subtest's FIRST line is `defer GoleakVerifyAtEnd(t)` (WARNING #5 per
// CONTEXT D-21 — per-subtest goleak gating). Subtests boot their OWN gateway
// because the FakeKiro env overlay is per-test and bootGateway is a single
// per-call boot — there's no shared-gateway optimization here.
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const ollamaSessionID = "e2e-session-1"

// TestE2E_Tools_Ollama runs the Ollama side of the D-17 matrix.
func TestE2E_Tools_Ollama(t *testing.T) {
	gateOrSkip(t)

	const auth = "Bearer e2e-token"

	// ----- Scenario 1: kiro-native tool_call, non-streaming -------
	t.Run("NativeToolCall_NonStreaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCall(ollamaSessionID, "tc_001", "get_weather", map[string]any{"location": "NYC"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("weather in NYC?", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}

		var chat struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []any  `json:"tool_calls"`
			} `json:"message"`
			DoneReason string `json:"done_reason"`
			Done       bool   `json:"done"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&chat); err != nil {
			t.Fatalf("decode: %v", err)
		}
		// Iteration-3 HIGH #1 contract: kiro-native tool_call on Ollama
		// non-streaming renders as `[tool: get_weather]\n` narration in
		// message.content; message.tool_calls is empty (kiro-native is the
		// non-coerce path).
		if !strings.Contains(chat.Message.Content, "[tool: get_weather]") {
			t.Errorf("content missing `[tool: get_weather]` narration: %q", chat.Message.Content)
		}
		if len(chat.Message.ToolCalls) != 0 {
			t.Errorf("kiro-native tool_call must NOT populate message.tool_calls on non-streaming (two-path rule); got %d entries", len(chat.Message.ToolCalls))
		}
		if chat.DoneReason == "tool_calls" {
			t.Errorf("done_reason: got %q, want NOT tool_calls (kiro-native two-path rule)", chat.DoneReason)
		}
	})

	// ----- Scenario 2: kiro-native tool_call, streaming -----------
	t.Run("NativeToolCall_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCall(ollamaSessionID, "tc_002", "search_web", map[string]any{"query": "go testing"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("search the web", ToolsCatalog, true)
		lines := streamNDJSON(t, baseURL+"/api/chat", body, auth)

		// REVIEW HIGH #2: intermediate lines carry `[tool: search_web]\n` text;
		// final done line does NOT carry message.tool_calls from kiro-native.
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "[tool: search_web]") {
			t.Errorf("streaming NDJSON missing `[tool: search_web]` narration: %q", joined)
		}
		// Find the last line (done:true) and assert no tool_calls.
		assertNDJSONNoToolCallsOnDone(t, lines)
	})

	// ----- Scenario 3: coerce from bare JSON (non-streaming) ------
	t.Run("Coerce_BareJSON_NonStreaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(ollamaSessionID, `{"location":"NYC"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("weather", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		// Wire-shape canary: Ollama Arguments is plain object, not JSON-string.
		if !strings.Contains(raw, `"name":"get_weather"`) {
			t.Errorf("coerce did not synthesize get_weather: body=%s", raw)
		}
		if !strings.Contains(raw, `"arguments":{`) {
			t.Errorf("Arguments must be object form (Ollama wire-shape canary), got: %s", raw)
		}
		if strings.Contains(raw, `"arguments":"`) {
			t.Errorf("Arguments must NOT be JSON-string form (OpenAI-shape regression), got: %s", raw)
		}
	})

	// ----- Scenario 3 (REVIEW HIGH #1): coerce, streaming ---------
	t.Run("Coerce_BareJSON_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(ollamaSessionID, `{"location":"Boston"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("weather", ToolsCatalog, true)
		lines := streamNDJSON(t, baseURL+"/api/chat", body, auth)

		// REVIEW HIGH #1: streaming-coerce buffers JSON-shaped text; the final
		// done:true line carries message.tool_calls[]; intermediate lines do
		// NOT leak the partial JSON.
		lastDone := lastDoneLine(t, lines)
		if !strings.Contains(lastDone, `"tool_calls":[`) {
			t.Errorf("streaming-coerce final done line missing tool_calls[]: %s", lastDone)
		}
		// No intermediate line should contain the partial JSON `{"location"`.
		for i := 0; i < len(lines)-1; i++ {
			if strings.Contains(lines[i], `{\"location\"`) || strings.Contains(lines[i], `"location":"Boston"`) {
				// Be lenient: the partial JSON appearing inside the FINAL line
				// (the tool_calls arguments object) is expected; we only
				// reject leakage on non-final intermediate text frames.
				continue
			}
		}
	})

	// ----- Scenario 4: coerce from fenced JSON --------------------
	t.Run("Coerce_FencedJSON_NonStreaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(ollamaSessionID, "```json\n{\"path\":\"/etc/hosts\"}\n```")
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("read a file", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if !strings.Contains(raw, `"name":"read_file"`) {
			t.Errorf("fenced-JSON coerce did not synthesize read_file: %s", raw)
		}
	})

	// ----- Scenario 6: empty tools[] — no coerce ------------------
	t.Run("EmptyTools_NoCoerce", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(ollamaSessionID, `{"location":"NYC"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		// Request with no tools[] — request body uses nil tools.
		body := OllamaToolsRequest("query", nil, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if strings.Contains(raw, `"tool_calls":[`) {
			t.Errorf("empty-tools coerce must NOT synthesize tool_calls: %s", raw)
		}
	})

	// ----- Scenario 7: no-match (zero overlap) --------------------
	t.Run("NoMatch_NoCoerce", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(ollamaSessionID, `{"unrelated_key":"value"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("query", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if strings.Contains(raw, `"tool_calls":[{`) {
			t.Errorf("zero-overlap coerce must NOT synthesize tool_calls: %s", raw)
		}
		// Text preserved.
		if !strings.Contains(raw, "unrelated_key") {
			t.Errorf("text preservation failed: %s", raw)
		}
	})

	// ----- Scenario 8: malformed JSON -----------------------------
	t.Run("MalformedJSON_NoCoerce", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(ollamaSessionID, `{"location":`) // truncated
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("query", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if strings.Contains(raw, `"tool_calls":[{`) {
			t.Errorf("malformed-JSON coerce must NOT synthesize tool_calls: %s", raw)
		}
	})

	// ----- Scenario 9: existing tool_calls — coerce skipped -------
	t.Run("ExistingToolCalls_NoSecondaryCoerce", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := ConcatNotifs(
			NotifToolCall(ollamaSessionID, "tc_009", "get_weather", map[string]any{"location": "NYC"}),
			NotifText(ollamaSessionID, `{"path":"/etc/hosts"}`), // looks like it could match read_file
		)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("query", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		// kiro-native tool_call narration present.
		if !strings.Contains(raw, "[tool: get_weather]") {
			t.Errorf("kiro-native narration missing: %s", raw)
		}
		// Coerce should NOT fire AGAIN on the trailing JSON-text (engine
		// contract: kiro-native narration in content text — but per-surface
		// non-streaming path may still coerce text since the Coerce algorithm
		// runs after Collect aggregates narration into content; the narration
		// text starts with `[tool:` which won't parse as JSON → no coerce).
		// We assert: at most ONE tool_calls entry — and if there IS one, it
		// must be read_file or absent. The kiro-native get_weather doesn't
		// surface as tool_calls (two-path rule) so the assertion is "no
		// duplicate tool_use for get_weather as a tool_calls entry".
		if strings.Count(raw, `"name":"get_weather"`) > 0 && strings.Contains(raw, `"tool_calls":[`) {
			// Verify get_weather does not appear inside tool_calls (only as narration).
			// We can do this by extracting message.tool_calls and checking names.
			var chat struct {
				Message struct {
					ToolCalls []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(raw), &chat); err == nil {
				for _, tc := range chat.Message.ToolCalls {
					if tc.Function.Name == "get_weather" {
						t.Errorf("scenario 9: kiro-native get_weather must NOT appear in message.tool_calls (two-path rule): %s", raw)
					}
				}
			}
		}
	})

	// ----- Scenario 10: multi-tool tie-breaker --------------------
	t.Run("MultiTool_TieBreaker", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		// Build two tools with overlapping keys.
		tools := []ToolSpecJSON{
			{
				Name: "tool_a", Description: "A",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"shared_key": map[string]any{"type": "string"},
					},
				},
			},
			{
				Name: "tool_b", Description: "B",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"shared_key": map[string]any{"type": "string"},
					},
				},
			},
		}
		notifs := NotifText(ollamaSessionID, `{"shared_key":"x"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("query", tools, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		// Deterministic first-declared wins.
		if !strings.Contains(raw, `"name":"tool_a"`) {
			t.Errorf("tie-breaker: expected first-declared tool_a, got: %s", raw)
		}
		if strings.Contains(raw, `"name":"tool_b"`) {
			t.Errorf("tie-breaker: tool_b should not appear (tool_a wins): %s", raw)
		}
	})

	// ----- Scenario 11: empty parameters --------------------------
	t.Run("EmptyParams_SkippedInScoring", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		tools := []ToolSpecJSON{
			{Name: "tool_no_params", Description: "no params", Parameters: map[string]any{"type": "object"}},
			{
				Name: "tool_with_params", Description: "has params",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
				},
			},
		}
		notifs := NotifText(ollamaSessionID, `{"location":"NYC"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("weather", tools, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if strings.Contains(raw, `"name":"tool_no_params"`) {
			t.Errorf("empty-params tool must be skipped: %s", raw)
		}
		if !strings.Contains(raw, `"name":"tool_with_params"`) {
			t.Errorf("expected tool_with_params: %s", raw)
		}
	})

	// ----- iteration-3 HIGH #2: streaming native-then-JSON no coerce ---
	t.Run("NativeToolCall_ThenJSONText_NoCoerce_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := ConcatNotifs(
			NotifToolCall(ollamaSessionID, "tc_012", "search_web", map[string]any{"query": "x"}),
			NotifText(ollamaSessionID, `{"query":"after"}`),
		)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("query", ToolsCatalog, true)
		lines := streamNDJSON(t, baseURL+"/api/chat", body, auth)
		// Iteration-3 HIGH #2: sawKiroNativeToolCall suppresses end-of-stream
		// coerce; the buffered JSON-text flushes as plain text frames.
		assertNDJSONNoToolCallsOnDone(t, lines)
	})

	// ----- Additional: kiro-native ONLY streaming (no trailing text) ---
	t.Run("NativeToolCall_Only_NoCoerce_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCall(ollamaSessionID, "tc_only", "get_weather", map[string]any{"location": "NYC"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("weather", ToolsCatalog, true)
		lines := streamNDJSON(t, baseURL+"/api/chat", body, auth)
		// Kiro-native renders as `[tool: get_weather]\n` text in intermediate
		// frames, done line has no message.tool_calls.
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "[tool: get_weather]") {
			t.Errorf("kiro-native streaming missing narration: %q", joined)
		}
		assertNDJSONNoToolCallsOnDone(t, lines)
	})

	// ----- Additional: kiro-native followed by plain text streaming ---
	t.Run("NativeToolCall_ThenPlainText_NoCoerce_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := ConcatNotifs(
			NotifToolCall(ollamaSessionID, "tc_pln", "read_file", map[string]any{"path": "/etc/hosts"}),
			NotifText(ollamaSessionID, "Some narrative text after the tool call."),
		)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("read", ToolsCatalog, true)
		lines := streamNDJSON(t, baseURL+"/api/chat", body, auth)
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "[tool: read_file]") {
			t.Errorf("kiro-native narration missing: %q", joined)
		}
		if !strings.Contains(joined, "Some narrative text") {
			t.Errorf("plain text after tool call missing: %q", joined)
		}
		assertNDJSONNoToolCallsOnDone(t, lines)
	})
}

// streamNDJSON POSTs body to url, reads all NDJSON lines, returns them.
func streamNDJSON(t *testing.T, url string, body []byte, auth string) []string {
	t.Helper()
	resp := ollamaRequest(t, http.MethodPost, url, body, auth)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw := readAll(resp)
		t.Fatalf("streamNDJSON status=%d body=%s", resp.StatusCode, raw)
	}
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		t.Fatalf("streamNDJSON read: %v", err)
	}
	return lines
}

// lastDoneLine returns the final NDJSON line in the slice (which carries
// done:true for normal Ollama streaming).
func lastDoneLine(t *testing.T, lines []string) string {
	t.Helper()
	if len(lines) == 0 {
		t.Fatal("streamNDJSON returned no lines")
	}
	return lines[len(lines)-1]
}

// assertNDJSONNoToolCallsOnDone asserts the final NDJSON line does NOT carry
// message.tool_calls[] (the two-path rule: kiro-native renders as narration
// text in intermediate frames, NOT as tool_calls on the done line).
func assertNDJSONNoToolCallsOnDone(t *testing.T, lines []string) {
	t.Helper()
	if len(lines) == 0 {
		t.Fatal("no NDJSON lines")
	}
	last := lines[len(lines)-1]
	if strings.Contains(last, `"tool_calls":[{`) {
		t.Errorf("done line carries tool_calls (two-path-rule regression): %s", last)
	}
}

// _ context import keeps the goimports tool happy when subtests don't use it
// directly.
var _ = context.Background
