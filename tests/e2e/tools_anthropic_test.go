//go:build e2e

// tools_anthropic_test.go — Phase 6 Plan 06-05 Task 2 Anthropic side of the
// D-17 12-scenario matrix. Anthropic is the D-01 NO-COERCE asymmetry surface;
// it covers scenarios 1, 2, 5, 9 (5 is the asymmetry verification).
//
// Subtests (per-subtest goleak gating; WARNING #5 + CONTEXT D-21):
//  1. NativeToolCall_NonStreaming        — D-17 #1 + REVIEW MEDIUM #4 stop_reason
//  2. NativeToolCall_Streaming           — D-17 #2 (SDK-expected event sequence)
//  3. NoCoerce_BareJSON                  — D-17 #5 (D-01 asymmetry verification)
//  4. ExistingToolCalls_NoSecondary      — D-17 #9 (kiro-native tool_use preserved
//     alongside JSON-text content)
//
// Plus the cross-surface canonical equivalence test:
//
//	TestE2E_Tools_CrossSurface_CanonicalEquivalence — iteration-3 normalization
//	(Ollama/OpenAI narration vs Anthropic native tool_use).
package e2e_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const anthropicSessionID = "e2e-session-1"

func TestE2E_Tools_Anthropic(t *testing.T) {
	gateOrSkip(t)

	const auth = "Bearer e2e-token"

	// Scenario 1 — kiro-native tool_call non-streaming. REVIEW MEDIUM #4
	// verifies stop_reason override to "tool_use".
	t.Run("NativeToolCall_NonStreaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCall(anthropicSessionID, "tc_001", "get_weather", map[string]any{"location": "NYC"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := AnthropicToolsRequest("weather", ToolsCatalog, false)
		resp := postMessages(t, baseURL, body, map[string]string{"Authorization": auth})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, readAll(resp))
		}

		var msg struct {
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Type  string         `json:"type"`
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
				Text  string         `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
			t.Fatalf("decode: %v", err)
		}

		// Find the tool_use block.
		var foundToolUse bool
		for _, c := range msg.Content {
			if c.Type == "tool_use" {
				foundToolUse = true
				if c.ID != "tc_001" {
					t.Errorf("tool_use.id: got %q, want tc_001", c.ID)
				}
				if c.Name != "get_weather" {
					t.Errorf("tool_use.name: got %q, want get_weather", c.Name)
				}
				if c.Input == nil {
					t.Errorf("tool_use.input: got nil, want non-nil object (CR-01 Pitfall 1)")
				} else if c.Input["location"] != "NYC" {
					t.Errorf("tool_use.input.location: got %v, want NYC", c.Input["location"])
				}
			}
		}
		if !foundToolUse {
			t.Errorf("no tool_use block in content: %+v", msg.Content)
		}
		// REVIEW MEDIUM #4: stop_reason override to "tool_use".
		if msg.StopReason != "tool_use" {
			t.Errorf("stop_reason: got %q, want tool_use (REVIEW MEDIUM #4)", msg.StopReason)
		}
	})

	// Scenario 2 — streaming tool_use event sequence.
	t.Run("NativeToolCall_Streaming", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCall(anthropicSessionID, "tc_002", "get_weather", map[string]any{"location": "Boston"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := AnthropicToolsRequest("weather", ToolsCatalog, true)
		resp := postMessages(t, baseURL, body, map[string]string{"Authorization": auth})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, readAll(resp))
		}
		sse := readResponseSSE(t, resp)
		// Anthropic streaming tool_use event sequence:
		//   content_block_start{type:tool_use, input:{}} →
		//   content_block_delta{input_json_delta} →
		//   content_block_stop → message_delta{stop_reason:tool_use}
		if !strings.Contains(sse, "content_block_start") {
			t.Errorf("missing content_block_start: %s", sse)
		}
		if !strings.Contains(sse, `"type":"tool_use"`) {
			t.Errorf("missing tool_use type: %s", sse)
		}
		// CR-01 Pitfall 1 — input:{} (NOT input:null).
		if !strings.Contains(sse, `"input":{}`) {
			t.Errorf("missing input:{} (CR-01 Pitfall 1): %s", sse)
		}
		if !strings.Contains(sse, "input_json_delta") {
			t.Errorf("missing input_json_delta: %s", sse)
		}
		if !strings.Contains(sse, "content_block_stop") {
			t.Errorf("missing content_block_stop: %s", sse)
		}
		if !strings.Contains(sse, `"stop_reason":"tool_use"`) {
			t.Errorf("missing stop_reason:tool_use on message_delta: %s", sse)
		}
	})

	// Scenario 5 — D-01 NO-COERCE asymmetry. The defining Anthropic
	// invariant: bare JSON text on /v1/messages must be preserved verbatim
	// even when tools[] is supplied.
	t.Run("NoCoerce_BareJSON", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifText(anthropicSessionID, `{"location":"NYC"}`)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := AnthropicToolsRequest("weather", ToolsCatalog, false)
		resp := postMessages(t, baseURL, body, map[string]string{"Authorization": auth})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, readAll(resp))
		}

		var msg struct {
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
			t.Fatalf("decode: %v", err)
		}

		// D-01: NO tool_use block synthesized; text preserved verbatim.
		for _, c := range msg.Content {
			if c.Type == "tool_use" {
				t.Errorf("D-01 asymmetry violated: tool_use block synthesized on Anthropic: %+v", msg.Content)
			}
		}
		var textBlock string
		for _, c := range msg.Content {
			if c.Type == "text" {
				textBlock = c.Text
				break
			}
		}
		if !strings.Contains(textBlock, `{"location":"NYC"}`) {
			t.Errorf("bare JSON text not preserved: %q", textBlock)
		}
		// stop_reason should be the natural one (end_turn), NOT tool_use.
		if msg.StopReason == "tool_use" {
			t.Errorf("stop_reason: got tool_use, want end_turn (no tool_use produced)")
		}
	})

	// Scenario 9 — existing kiro-native tool_call preserved alongside trailing
	// JSON-shaped text (no secondary coerce on Anthropic).
	t.Run("ExistingToolCalls_NoSecondaryProcessing", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := ConcatNotifs(
			NotifText(anthropicSessionID, "some thoughts"),
			NotifToolCall(anthropicSessionID, "tc_009", "get_weather", map[string]any{"location": "NYC"}),
			NotifText(anthropicSessionID, `{"path":"/etc/hosts"}`),
		)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := AnthropicToolsRequest("query", ToolsCatalog, false)
		resp := postMessages(t, baseURL, body, map[string]string{"Authorization": auth})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, readAll(resp))
		}

		var msg struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
			t.Fatalf("decode: %v", err)
		}

		// Count tool_use blocks — exactly ONE, the kiro-native one.
		var toolUseCount int
		var sawJSONText bool
		for _, c := range msg.Content {
			if c.Type == "tool_use" {
				toolUseCount++
				if c.Name != "get_weather" {
					t.Errorf("unexpected tool_use name: %q", c.Name)
				}
			}
			if c.Type == "text" && strings.Contains(c.Text, `"path":"/etc/hosts"`) {
				sawJSONText = true
			}
		}
		if toolUseCount != 1 {
			t.Errorf("expected exactly 1 tool_use block, got %d: %+v", toolUseCount, msg.Content)
		}
		if !sawJSONText {
			t.Errorf("JSON-shaped text after tool_use must be preserved verbatim (D-01 asymmetry): %+v", msg.Content)
		}
	})
}

// TestE2E_Tools_CrossSurface_CanonicalEquivalence proves the D-17 cross-
// surface assertion: identical input (modulo model field) produces an
// equivalent canonical tool-call identity across all three surfaces. The
// iteration-3 reworded contract scopes equivalence to (name, args) only;
// the helper normalizes the per-surface rendering accordingly:
//   - Ollama   → parses `[tool: <name>]` narration out of message.content
//   - OpenAI   → parses `[tool: <name>]` narration out of choices[0].content
//   - Anthropic → reads content[].type==tool_use, takes name + input
func TestE2E_Tools_CrossSurface_CanonicalEquivalence(t *testing.T) {
	gateOrSkip(t)
	const auth = "Bearer e2e-token"

	t.Run("CrossSurfaceCanonicalToolCall", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCall(anthropicSessionID, "tc_cross", "get_weather", map[string]any{"location": "NYC"})

		// Ollama leg
		cmdO, envO := FakeKiro(t, Script{Notifications: notifs})
		ollamaURL, ollamaCleanup := bootGateway(t, mergeEnv(envO, map[string]string{"KIRO_CMD": cmdO}))
		defer ollamaCleanup()
		ollamaResp := ollamaRequest(t, http.MethodPost, ollamaURL+"/api/chat",
			OllamaToolsRequest("weather", ToolsCatalog, false), auth)
		defer func() { _ = ollamaResp.Body.Close() }()
		ollamaBody := []byte(readAll(ollamaResp))

		// OpenAI leg — second boot.
		cmdP, envP := FakeKiro(t, Script{Notifications: notifs})
		openaiURL, openaiCleanup := bootGateway(t, mergeEnv(envP, map[string]string{"KIRO_CMD": cmdP}))
		defer openaiCleanup()
		openaiResp := ollamaRequest(t, http.MethodPost, openaiURL+"/v1/chat/completions",
			OpenAIToolsRequest("weather", ToolsCatalog, false), auth)
		defer func() { _ = openaiResp.Body.Close() }()
		openaiBody := []byte(readAll(openaiResp))

		// Anthropic leg — third boot.
		cmdA, envA := FakeKiro(t, Script{Notifications: notifs})
		anthropicURL, anthropicCleanup := bootGateway(t, mergeEnv(envA, map[string]string{"KIRO_CMD": cmdA}))
		defer anthropicCleanup()
		anthropicResp := postMessages(t, anthropicURL,
			AnthropicToolsRequest("weather", ToolsCatalog, false),
			map[string]string{"Authorization": auth})
		defer func() { _ = anthropicResp.Body.Close() }()
		anthropicBody := []byte(readAll(anthropicResp))

		AssertSameCanonicalToolCall(t, ollamaBody, openaiBody, anthropicBody)
	})
}

// readResponseSSE reads an already-open SSE response body into a single
// string for substring-matching.
func readResponseSSE(t *testing.T, resp *http.Response) string {
	t.Helper()
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		b.WriteString(sc.Text())
		b.WriteByte('\n')
	}
	return b.String()
}
