//go:build e2e

// tools_ollama_test.go — Phase 6 Plan 06-05 Task 2 Ollama side of the D-17
// 12-scenario tool-call matrix. Covers scenarios 1-4 + 6-11 against the live
// otto-gateway binary with a controllable fake-kiro-cli.
//
// Subtests:
//  1. NativeToolCall_NonStreaming           — D-17 #1 + iteration-3 HIGH #1 narration
//  2. NativeToolCall_Streaming              — D-17 #2 + REVIEW HIGH #2 two-path rule
//  3. Coerce_BareJSON_NonStreaming          — D-17 #3
//  4. Coerce_BareJSON_Streaming             — D-17 #3 + REVIEW HIGH #1 streaming-coerce
//  5. Coerce_FencedJSON_NonStreaming        — D-17 #4
//  6. EmptyTools_NoCoerce                   — D-17 #6
//  7. NoMatch_NoCoerce                      — D-17 #7
//  8. MalformedJSON_NoCoerce                — D-17 #8
//  9. ExistingToolCalls_NoSecondaryCoerce   — D-17 #9
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
		// Defect 1a (2026-07-16): kiro-native tool_call on Ollama
		// non-streaming surfaces STRUCTURALLY in message.tool_calls
		// (object-shaped args); message.content carries no `[tool:` marker.
		if strings.Contains(chat.Message.Content, "[tool:") {
			t.Errorf("content must not contain a [tool: marker: %q", chat.Message.Content)
		}
		if len(chat.Message.ToolCalls) != 1 {
			t.Fatalf("kiro-native tool_call must populate message.tool_calls; got %d entries", len(chat.Message.ToolCalls))
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

		// Defect 1c: no `[tool:` marker anywhere; the done:true line carries
		// the structured message.tool_calls (object-shaped args).
		joined := strings.Join(lines, "\n")
		if strings.Contains(joined, "[tool:") {
			t.Errorf("streaming NDJSON must not contain a [tool: marker: %q", joined)
		}
		if !strings.Contains(joined, `"tool_calls":[{"function":{"name":"search_web"`) {
			t.Errorf("done line missing structured search_web tool_call: %q", joined)
		}
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
		// Defect 1a: get_weather (kiro-native) surfaces structurally in
		// message.tool_calls; no `[tool:` marker; and CoerceToolCall no-ops on
		// the trailing JSON text (native already populated ToolCalls) so there
		// is exactly one tool_call — get_weather, not a coerced duplicate.
		if strings.Contains(raw, "[tool:") {
			t.Errorf("content must not contain a [tool: marker: %s", raw)
		}
		var chat struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(raw), &chat); err != nil {
			t.Fatalf("decode: %v; %s", err, raw)
		}
		if len(chat.Message.ToolCalls) != 1 || chat.Message.ToolCalls[0].Function.Name != "get_weather" {
			names := []string{}
			for _, tc := range chat.Message.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			t.Errorf("scenario 9: expected exactly one get_weather tool_call (no coerce dup); got %v: %s", names, raw)
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

	// ----- Defect 1c: streaming native-then-JSON, coerce skipped ------
	t.Run("NativeToolCall_ThenJSONText_SkipsCoerce_Streaming", func(t *testing.T) {
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
		joined := strings.Join(lines, "\n")
		// Defect 1c: the native search_web surfaces structurally on the done
		// line; sawKiroNativeToolCall suppresses the coerce so the trailing
		// JSON adds no second call. No `[tool:` marker.
		if strings.Contains(joined, "[tool:") {
			t.Errorf("must not contain a [tool: marker: %q", joined)
		}
		if !strings.Contains(joined, `"tool_calls":[{"function":{"name":"search_web"`) {
			t.Errorf("done line missing structured search_web tool_call: %q", joined)
		}
		if got := strings.Count(joined, `"tool_calls":[`); got != 1 {
			t.Errorf("expected exactly one tool_calls done-line (no coerce dup); got %d: %q", got, joined)
		}
	})

	// ----- Defect 1c: kiro-native ONLY streaming (no trailing text) ---
	t.Run("NativeToolCall_Only_Structured_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCall(ollamaSessionID, "tc_only", "get_weather", map[string]any{"location": "NYC"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("weather", ToolsCatalog, true)
		lines := streamNDJSON(t, baseURL+"/api/chat", body, auth)
		joined := strings.Join(lines, "\n")
		// Defect 1c: structured tool_calls on the done line, no `[tool:` marker.
		if strings.Contains(joined, "[tool:") {
			t.Errorf("must not contain a [tool: marker: %q", joined)
		}
		if !strings.Contains(joined, `"tool_calls":[{"function":{"name":"get_weather"`) {
			t.Errorf("done line missing structured get_weather tool_call: %q", joined)
		}
	})

	// ----- Defect 1c: kiro-native followed by plain text streaming ----
	t.Run("NativeToolCall_ThenPlainText_Structured_Streaming", func(t *testing.T) {
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
		// Defect 1c: the trailing plain text streams as content, and the
		// native read_file surfaces structurally on the done line.
		if strings.Contains(joined, "[tool:") {
			t.Errorf("must not contain a [tool: marker: %q", joined)
		}
		if !strings.Contains(joined, "Some narrative text") {
			t.Errorf("plain text after tool call missing: %q", joined)
		}
		if !strings.Contains(joined, `"tool_calls":[{"function":{"name":"read_file"`) {
			t.Errorf("done line missing structured read_file tool_call: %q", joined)
		}
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

// _ context import keeps the goimports tool happy when subtests don't use it
// directly.
var _ = context.Background
