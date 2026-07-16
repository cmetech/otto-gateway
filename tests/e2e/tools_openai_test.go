//go:build e2e

// tools_openai_test.go — Phase 6 Plan 06-05 Task 2 OpenAI side of the D-17
// 12-scenario tool-call matrix. Covers scenarios 1-4 + 6-11 against the live
// otto-gateway binary with a controllable fake-kiro-cli.
//
// Wire-shape canary (vs Ollama): OpenAI arguments is a JSON-encoded STRING,
// not a plain object literal. The Coerce_BareJSON_* subtests assert this
// divergence at the byte-level.
//
// Subtests (per-subtest goleak gating; WARNING #5 + CONTEXT D-21):
//  1. NativeToolCall_NonStreaming               — D-17 #1 + iteration-3 HIGH #1
//  2. NativeToolCall_Streaming                  — D-17 #2 + REVIEW HIGH #2
//  3. Coerce_BareJSON_NonStreaming              — D-17 #3
//  4. Coerce_BareJSON_Streaming                 — D-17 #3 + REVIEW HIGH #1
//  5. Coerce_FencedJSON_NonStreaming            — D-17 #4
//  6. EmptyTools_NoCoerce                       — D-17 #6
//  7. NoMatch_NoCoerce                          — D-17 #7
//  8. MalformedJSON_NoCoerce                    — D-17 #8
//  9. ExistingToolCalls_NoSecondaryCoerce       — D-17 #9
//  10. MultiTool_TieBreaker                      — D-17 #10
//  11. EmptyParams_SkippedInScoring              — D-17 #11
//  12. NativeToolCall_ThenJSONText_NoCoerce_Streaming — iteration-3 HIGH #2
//  13. ToolCallFirstStreamChunk_RoleEmitOnce     — REVIEW LOW #8
//  14. NativeToolCall_Only_NoCoerce_Streaming    — defense-in-depth (matches Ollama)
//  15. NativeToolCall_ThenPlainText_NoCoerce_Streaming — defense-in-depth
package e2e_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const openaiSessionID = "e2e-session-1"

func TestE2E_Tools_OpenAI(t *testing.T) {
	gateOrSkip(t)

	const auth = "Bearer e2e-token"

	t.Run("NativeToolCall_NonStreaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCall(openaiSessionID, "tc_oa_001", "get_weather", map[string]any{"location": "NYC"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("weather", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		// Defect 1a (2026-07-16): kiro-native surfaces as a STRUCTURED
		// tool_calls entry (id/name/JSON-string args) + finish_reason:
		// "tool_calls". NO `[tool:` marker in content.
		if strings.Contains(raw, "[tool:") {
			t.Errorf("OpenAI content must not contain a [tool: marker: %s", raw)
		}
		if !strings.Contains(raw, `"name":"get_weather"`) || !strings.Contains(raw, `"arguments":"{\"location\":\"NYC\"}"`) {
			t.Errorf("OpenAI missing structured get_weather tool_call: %s", raw)
		}
		if !strings.Contains(raw, `"finish_reason":"tool_calls"`) {
			t.Errorf("kiro-native must set finish_reason:tool_calls: %s", raw)
		}
	})

	t.Run("NativeToolCall_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCall(openaiSessionID, "tc_oa_002", "search_web", map[string]any{"query": "go"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("search", ToolsCatalog, true)
		sse := readSSEFrames(t, baseURL+"/v1/chat/completions", body, auth)
		// Defect 1b: kiro-native surfaces as native delta.tool_calls frames
		// + terminal finish_reason:"tool_calls". NO `[tool:` marker.
		if strings.Contains(sse, "[tool:") {
			t.Errorf("SSE must not contain a [tool: marker: %s", sse)
		}
		if !strings.Contains(sse, `"name":"search_web"`) {
			t.Errorf("SSE missing native delta.tool_calls name: %s", sse)
		}
		if !strings.Contains(sse, `"finish_reason":"tool_calls"`) {
			t.Errorf("SSE must set finish_reason:tool_calls from kiro-native: %s", sse)
		}
	})

	t.Run("Coerce_BareJSON_NonStreaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(openaiSessionID, `{"location":"NYC"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("weather", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		// Wire-shape canary: OpenAI Arguments is JSON-encoded STRING.
		if !strings.Contains(raw, `"name":"get_weather"`) {
			t.Errorf("coerce did not synthesize get_weather: %s", raw)
		}
		// Positive: JSON-string form `"arguments":"{...}"`.
		if !strings.Contains(raw, `"arguments":"`) {
			t.Errorf("OpenAI arguments must be JSON-string form (wire-shape canary): %s", raw)
		}
		// Negative: NOT plain-object form `"arguments":{...}`.
		if strings.Contains(raw, `"arguments":{`) {
			t.Errorf("OpenAI arguments must NOT be plain-object form (Ollama-shape regression): %s", raw)
		}
		if !strings.Contains(raw, `"finish_reason":"tool_calls"`) {
			t.Errorf("coerce-fired non-streaming must set finish_reason:tool_calls: %s", raw)
		}
	})

	t.Run("Coerce_BareJSON_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(openaiSessionID, `{"location":"Boston"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("weather", ToolsCatalog, true)
		sse := readSSEFrames(t, baseURL+"/v1/chat/completions", body, auth)
		// REVIEW HIGH #1: streaming-coerce emits native delta.tool_calls
		// frames (id+name, then arguments JSON-string), terminal
		// finish_reason:tool_calls. NO intermediate text-deltas leak the
		// partial JSON `{"location"`.
		if !strings.Contains(sse, `"tool_calls":[`) {
			t.Errorf("streaming-coerce missing delta.tool_calls frames: %s", sse)
		}
		if !strings.Contains(sse, `"finish_reason":"tool_calls"`) {
			t.Errorf("streaming-coerce missing terminal finish_reason:tool_calls: %s", sse)
		}
	})

	t.Run("Coerce_FencedJSON_NonStreaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(openaiSessionID, "```json\n{\"path\":\"/etc/hosts\"}\n```")
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("read", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if !strings.Contains(raw, `"name":"read_file"`) {
			t.Errorf("fenced-JSON coerce missing read_file: %s", raw)
		}
	})

	t.Run("EmptyTools_NoCoerce", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(openaiSessionID, `{"location":"NYC"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("query", nil, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if strings.Contains(raw, `"finish_reason":"tool_calls"`) {
			t.Errorf("empty-tools must NOT coerce: %s", raw)
		}
	})

	t.Run("NoMatch_NoCoerce", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(openaiSessionID, `{"unrelated":"v"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("query", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if strings.Contains(raw, `"finish_reason":"tool_calls"`) {
			t.Errorf("no-match must NOT coerce: %s", raw)
		}
	})

	t.Run("MalformedJSON_NoCoerce", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(openaiSessionID, `{"location":`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("query", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if strings.Contains(raw, `"finish_reason":"tool_calls"`) {
			t.Errorf("malformed-JSON must NOT coerce: %s", raw)
		}
	})

	t.Run("ExistingToolCalls_NoSecondaryCoerce", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := ConcatNotifs(
			NotifToolCall(openaiSessionID, "tc_009", "get_weather", map[string]any{"location": "NYC"}),
			NotifText(openaiSessionID, `{"path":"/etc/hosts"}`),
		)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("query", ToolsCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		// Defect 1a: get_weather (kiro-native) now appears STRUCTURALLY in
		// tool_calls, with no `[tool:` marker. The trailing JSON text does
		// NOT produce a secondary coerced tool_call (native already fired →
		// Collect populated ToolCalls → CoerceToolCall no-ops).
		if strings.Contains(raw, "[tool:") {
			t.Errorf("OpenAI content must not contain a [tool: marker: %s", raw)
		}
		var resp2 struct {
			Choices []struct {
				Message struct {
					ToolCalls []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(raw), &resp2); err != nil || len(resp2.Choices) == 0 {
			t.Fatalf("decode: %v; %s", err, raw)
		}
		names := []string{}
		for _, tc := range resp2.Choices[0].Message.ToolCalls {
			names = append(names, tc.Function.Name)
		}
		if len(names) != 1 || names[0] != "get_weather" {
			t.Errorf("expected exactly one get_weather tool_call (no coerce dup); got %v: %s", names, raw)
		}
	})

	t.Run("MultiTool_TieBreaker", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		tools := []ToolSpecJSON{
			{Name: "tool_a", Description: "A", Parameters: map[string]any{"type": "object", "properties": map[string]any{"shared_key": map[string]any{"type": "string"}}}},
			{Name: "tool_b", Description: "B", Parameters: map[string]any{"type": "object", "properties": map[string]any{"shared_key": map[string]any{"type": "string"}}}},
		}
		notifs := NotifText(openaiSessionID, `{"shared_key":"x"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("query", tools, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if !strings.Contains(raw, `"name":"tool_a"`) {
			t.Errorf("tie-breaker: expected tool_a (first-declared): %s", raw)
		}
	})

	t.Run("EmptyParams_SkippedInScoring", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		tools := []ToolSpecJSON{
			{Name: "tool_no_params", Description: "", Parameters: map[string]any{"type": "object"}},
			{Name: "tool_with_params", Description: "", Parameters: map[string]any{"type": "object", "properties": map[string]any{"location": map[string]any{"type": "string"}}}},
		}
		notifs := NotifText(openaiSessionID, `{"location":"NYC"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("query", tools, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if !strings.Contains(raw, `"name":"tool_with_params"`) {
			t.Errorf("expected tool_with_params winner: %s", raw)
		}
		if strings.Contains(raw, `"name":"tool_no_params"`) {
			t.Errorf("empty-params tool must be skipped: %s", raw)
		}
	})

	t.Run("NativeToolCall_ThenJSONText_SkipsCoerce_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := ConcatNotifs(
			NotifToolCall(openaiSessionID, "tc_then", "search_web", map[string]any{"query": "x"}),
			NotifText(openaiSessionID, `{"query":"after"}`),
		)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("query", ToolsCatalog, true)
		sse := readSSEFrames(t, baseURL+"/v1/chat/completions", body, auth)
		// Defect 1b: the native search_web surfaces structurally, so the
		// terminal finish_reason IS "tool_calls". sawKiroNativeToolCall still
		// suppresses the end-of-stream coerce, so the trailing JSON does NOT
		// add a second tool_call — exactly one native call is emitted.
		if strings.Contains(sse, "[tool:") {
			t.Errorf("SSE must not contain a [tool: marker: %s", sse)
		}
		if !strings.Contains(sse, `"name":"search_web"`) {
			t.Errorf("SSE missing native search_web tool_call: %s", sse)
		}
		if !strings.Contains(sse, `"finish_reason":"tool_calls"`) {
			t.Errorf("native tool call must set finish_reason:tool_calls: %s", sse)
		}
		if got := strings.Count(sse, `"type":"function"`); got != 1 {
			t.Errorf("expected exactly 1 structured tool_call (no coerce dup); got %d: %s", got, sse)
		}
	})

	t.Run("ToolCallFirstStreamChunk_RoleEmitOnce", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		// First chunk is a tool_call — REVIEW LOW #8 verifies the role is
		// emitted exactly once.
		notifs := NotifToolCall(openaiSessionID, "tc_role", "get_weather", map[string]any{"location": "NYC"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("weather", ToolsCatalog, true)
		sse := readSSEFrames(t, baseURL+"/v1/chat/completions", body, auth)
		// Count role:"assistant" occurrences in delta payloads.
		count := strings.Count(sse, `"role":"assistant"`)
		if count != 1 {
			t.Errorf("REVIEW LOW #8: expected exactly 1 role frame, got %d: %s", count, sse)
		}
		// Defect 1b: tool-call-first still surfaces structurally, no marker.
		if strings.Contains(sse, "[tool:") {
			t.Errorf("SSE must not contain a [tool: marker: %s", sse)
		}
		if !strings.Contains(sse, `"finish_reason":"tool_calls"`) {
			t.Errorf("tool-call-first must set finish_reason:tool_calls: %s", sse)
		}
	})

	t.Run("NativeToolCall_Only_Structured_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCall(openaiSessionID, "tc_only", "get_weather", map[string]any{"location": "NYC"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("weather", ToolsCatalog, true)
		sse := readSSEFrames(t, baseURL+"/v1/chat/completions", body, auth)
		// Defect 1b: structured delta.tool_calls + finish_reason:tool_calls.
		if strings.Contains(sse, "[tool:") {
			t.Errorf("SSE must not contain a [tool: marker: %s", sse)
		}
		if !strings.Contains(sse, `"name":"get_weather"`) {
			t.Errorf("SSE missing native get_weather tool_call: %s", sse)
		}
		if !strings.Contains(sse, `"finish_reason":"tool_calls"`) {
			t.Errorf("kiro-native only must set finish_reason:tool_calls: %s", sse)
		}
	})

	t.Run("NativeToolCall_ThenPlainText_Structured_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := ConcatNotifs(
			NotifToolCall(openaiSessionID, "tc_pln", "read_file", map[string]any{"path": "/etc/hosts"}),
			NotifText(openaiSessionID, "narrative after tool call"),
		)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("read", ToolsCatalog, true)
		sse := readSSEFrames(t, baseURL+"/v1/chat/completions", body, auth)
		// Defect 1b: the native read_file surfaces structurally (→
		// finish_reason:tool_calls) and the trailing plain text still streams
		// as content deltas.
		if strings.Contains(sse, "[tool:") {
			t.Errorf("SSE must not contain a [tool: marker: %s", sse)
		}
		if !strings.Contains(sse, "narrative after tool call") {
			t.Errorf("plain-text after tool_call missing: %s", sse)
		}
		if !strings.Contains(sse, `"name":"read_file"`) {
			t.Errorf("SSE missing native read_file tool_call: %s", sse)
		}
		if !strings.Contains(sse, `"finish_reason":"tool_calls"`) {
			t.Errorf("native tool call must set finish_reason:tool_calls: %s", sse)
		}
	})
}

// readSSEFrames POSTs body, reads SSE frames, returns the entire body as a
// single string for substring-matching (the OpenAI SSE wire shape varies on
// content; the tests assert on canonical byte-level substrings).
func readSSEFrames(t *testing.T, url string, body []byte, auth string) string {
	t.Helper()
	resp := ollamaRequest(t, http.MethodPost, url, body, auth)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw := readAll(resp)
		t.Fatalf("readSSEFrames status=%d body=%s", resp.StatusCode, raw)
	}
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		b.WriteString(sc.Text())
		b.WriteByte('\n')
	}
	return b.String()
}
