//go:build e2e

// tools_alias_test.go — alias-primary native tool-call surfacing (2026-07-16,
// quick task 260716-bv0). Grounded in a real kiro-cli capture: when the caller
// offers tools, kiro reaches for its OWN built-in shell tool and emits a native
// ACP tool_call whose `kind` is "execute" (args `{"command":…}`) — a name the
// host never offered. The gateway resolves that native name against the
// caller's offered tools + KIRO_TOOL_ALIASES and surfaces it STRUCTURALLY under
// the offered name (execute → run_shell). Built-ins with no alias to an offered
// tool are DROPPED (no unroutable call, coerce/wrapper fallback unclobbered).
//
// The fake-kiro path here does NOT do the permission/deny dance; it emits the
// native tool_call frames directly (native `kind`+`rawInput` shape via
// NotifToolCallNative). That is sufficient to exercise the resolver + dedup
// wiring in every surface's collect/stream path.
//
//	Run: PII_REDACTION_MODE=replace GW_E2E=1 \
//		go test -tags e2e -count=1 -run 'TestE2E_Tools_Alias' ./tests/e2e/
package e2e_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const aliasSessionID = "e2e-session-1"

// RunShellToolCatalog is the single-tool catalog the alias scenarios offer.
// Its `command` schema matches kiro's built-in shell (`kind:"execute"`) args.
var RunShellToolCatalog = []ToolSpecJSON{
	{
		Name:        "run_shell",
		Description: "Run a shell command on the host and return its stdout.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Shell command"},
			},
			"required": []string{"command"},
		},
	},
}

// aliasEnv is the KIRO_TOOL_ALIASES overlay used across the resolve scenarios.
var aliasEnv = map[string]string{"KIRO_TOOL_ALIASES": "execute:run_shell,shell:run_shell"}

func TestE2E_Tools_Alias(t *testing.T) {
	gateOrSkip(t)

	const auth = "Bearer e2e-token"
	const cmdArgs = `python3 -c "print(2+2)"`

	// --- OpenAI non-streaming: native execute → structured run_shell --------
	t.Run("OpenAI_NonStreaming_AliasResolves", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCallNative(aliasSessionID, "tooluse_a1", "execute", map[string]any{"command": cmdArgs})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(mergeEnv(env, aliasEnv), map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("run the command", RunShellToolCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if strings.Contains(raw, "[tool:") {
			t.Errorf("content must not contain a [tool: marker: %s", raw)
		}
		if strings.Contains(raw, `"name":"execute"`) {
			t.Errorf("native execute name must NOT leak; expected run_shell: %s", raw)
		}
		if !strings.Contains(raw, `"name":"run_shell"`) {
			t.Errorf("alias must surface run_shell: %s", raw)
		}
		if !strings.Contains(raw, `"arguments":"{\"command\":\"python3 -c \\\"print(2+2)\\\"\"}"`) {
			t.Errorf("run_shell arguments (JSON-string) missing/incorrect: %s", raw)
		}
		if !strings.Contains(raw, `"finish_reason":"tool_calls"`) {
			t.Errorf("alias-surfaced call must set finish_reason:tool_calls: %s", raw)
		}
	})

	// --- OpenAI streaming: native execute → structured run_shell -----------
	t.Run("OpenAI_Streaming_AliasResolves", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCallNative(aliasSessionID, "tooluse_a2", "execute", map[string]any{"command": cmdArgs})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(mergeEnv(env, aliasEnv), map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("run the command", RunShellToolCatalog, true)
		sse := readSSEFrames(t, baseURL+"/v1/chat/completions", body, auth)
		if strings.Contains(sse, "[tool:") {
			t.Errorf("SSE must not contain a [tool: marker: %s", sse)
		}
		if strings.Contains(sse, `"name":"execute"`) {
			t.Errorf("native execute name must NOT leak in SSE: %s", sse)
		}
		if !strings.Contains(sse, `"name":"run_shell"`) {
			t.Errorf("SSE missing aliased run_shell delta.tool_calls: %s", sse)
		}
		if !strings.Contains(sse, `"finish_reason":"tool_calls"`) {
			t.Errorf("SSE must set finish_reason:tool_calls: %s", sse)
		}
	})

	// --- Ollama non-streaming: native execute → structured run_shell -------
	t.Run("Ollama_NonStreaming_AliasResolves", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCallNative(aliasSessionID, "tooluse_a3", "execute", map[string]any{"command": cmdArgs})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(mergeEnv(env, aliasEnv), map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OllamaToolsRequest("run the command", RunShellToolCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/api/chat", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if strings.Contains(raw, "[tool:") {
			t.Errorf("content must not contain a [tool: marker: %s", raw)
		}
		if strings.Contains(raw, `"name":"execute"`) {
			t.Errorf("native execute name must NOT leak: %s", raw)
		}
		// Ollama arguments are an object literal (not a JSON-string).
		if !strings.Contains(raw, `"tool_calls":[{"function":{"name":"run_shell"`) {
			t.Errorf("Ollama missing structured run_shell tool_call: %s", raw)
		}
		if !strings.Contains(raw, `"command":"python3 -c \"print(2+2)\""`) {
			t.Errorf("Ollama run_shell arguments object missing/incorrect: %s", raw)
		}
	})

	// --- Anthropic non-streaming: native execute → tool_use run_shell ------
	t.Run("Anthropic_NonStreaming_AliasResolves", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCallNative(aliasSessionID, "tooluse_a4", "execute", map[string]any{"command": cmdArgs})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(mergeEnv(env, aliasEnv), map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := AnthropicToolsRequest("run the command", RunShellToolCatalog, false)
		resp := postMessages(t, baseURL, body, map[string]string{"Authorization": auth})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, readAll(resp))
		}
		var msg struct {
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Type  string         `json:"type"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		var found bool
		for _, c := range msg.Content {
			if c.Type == "tool_use" {
				found = true
				if c.Name != "run_shell" {
					t.Errorf("tool_use.name: got %q, want run_shell (alias)", c.Name)
				}
				if c.Input["command"] != cmdArgs {
					t.Errorf("tool_use.input.command: got %v, want %q", c.Input["command"], cmdArgs)
				}
			}
		}
		if !found {
			t.Errorf("no tool_use block in content: %+v", msg.Content)
		}
		if msg.StopReason != "tool_use" {
			t.Errorf("stop_reason: got %q, want tool_use", msg.StopReason)
		}
	})

	// --- Drop case: unaliased built-in with no matching offered tool -------
	// Native fs_write, offered run_shell, NO alias for fs_write → dropped.
	// Nothing surfaces: no tool_calls, no finish_reason:tool_calls, and the
	// native name never leaks as text.
	t.Run("OpenAI_NonStreaming_UnaliasedBuiltin_Dropped", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := NotifToolCallNative(aliasSessionID, "tooluse_a5", "fs_write",
			map[string]any{"path": "/tmp/x", "content": "y"})
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		// aliasEnv only maps execute/shell → run_shell; fs_write is unaliased.
		baseURL, cleanup := bootGateway(t, mergeEnv(mergeEnv(env, aliasEnv), map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("write a file", RunShellToolCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		if strings.Contains(raw, `"tool_calls"`) && strings.Contains(raw, `"function"`) {
			t.Errorf("unaliased built-in must NOT surface any tool_call: %s", raw)
		}
		if strings.Contains(raw, `"finish_reason":"tool_calls"`) {
			t.Errorf("dropped built-in must NOT set finish_reason:tool_calls: %s", raw)
		}
		if strings.Contains(raw, "fs_write") {
			t.Errorf("native fs_write name must not leak into the response: %s", raw)
		}
		if strings.Contains(raw, "[tool:") {
			t.Errorf("content must not contain a [tool: marker: %s", raw)
		}
	})

	// --- Dedup: chunk + full + denial-retry collapse to ONE call -----------
	// kiro emits a tool_call_chunk (no args), then the full tool_call (args),
	// then RETRIES with a fresh id + identical (name,args) after each denial.
	// DedupToolCalls must collapse all three to a single run_shell.
	t.Run("OpenAI_NonStreaming_DedupChunkFullRetry", func(t *testing.T) {
		defer GoleakVerifyAtEnd(t)
		notifs := ConcatNotifs(
			NotifToolCallChunkNative(aliasSessionID, "tooluse_a6", "execute"),
			NotifToolCallNative(aliasSessionID, "tooluse_a6", "execute", map[string]any{"command": cmdArgs}),
			// Denial-retry: fresh id, identical (name, args).
			NotifToolCallNative(aliasSessionID, "tooluse_a6_retry", "execute", map[string]any{"command": cmdArgs}),
		)
		cmd, env := FakeKiro(t, Script{Notifications: notifs})
		baseURL, cleanup := bootGateway(t, mergeEnv(mergeEnv(env, aliasEnv), map[string]string{"KIRO_CMD": cmd}))
		defer cleanup()

		body := OpenAIToolsRequest("run the command", RunShellToolCatalog, false)
		resp := ollamaRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", body, auth)
		defer func() { _ = resp.Body.Close() }()
		raw := readAll(resp)
		var decoded struct {
			Choices []struct {
				Message struct {
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil || len(decoded.Choices) == 0 {
			t.Fatalf("decode: %v; %s", err, raw)
		}
		tcs := decoded.Choices[0].Message.ToolCalls
		if len(tcs) != 1 {
			t.Fatalf("expected exactly 1 tool_call after dedup, got %d: %s", len(tcs), raw)
		}
		if tcs[0].Function.Name != "run_shell" {
			t.Errorf("deduped call name: got %q, want run_shell: %s", tcs[0].Function.Name, raw)
		}
	})
}
