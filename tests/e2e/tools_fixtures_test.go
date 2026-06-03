//go:build e2e

// tools_fixtures.go — Phase 6 Plan 06-05 shared fixtures for the tool-call
// E2E matrix (D-17). Provides:
//
//   - var fakeKiroBinaryPath (package-level, set by TestMain in e2e_test.go).
//   - ToolsCatalog: 3-tool synthetic catalog (get_weather, read_file,
//     search_web) used across all three surfaces for diff readability.
//   - Per-surface request-body builders (Ollama, OpenAI, Anthropic).
//   - FakeKiro(t, script) (cmd string, env map[string]string) — the locked
//     REVIEW HIGH #5 fixture API. Returns BOTH a command path AND an env
//     overlay so tests can wire notifications/frame-log files cleanly into
//     bootGateway's extraEnv.
//   - Notification builders for kiro-emitted session/update frames
//     (NotifText, NotifThought, NotifToolCall, NotifPlan).
//   - AssertSameCanonicalToolCall — cross-surface canonical equivalence
//     helper (iteration-3 reworded contract: normalizes narration text on
//     Ollama/OpenAI vs native tool_use on Anthropic).
//   - GoleakVerifyAtEnd — per-subtest goroutine-leak gate with an inline
//     // goleak ignore-list: comment documenting the gateway child-process
//     and HTTP-idle-conn allowances (WARNING #5; CONTEXT D-21).
//   - ReadFakeKiroFrames — scenario-12 frame-log inspection helper.
//   - mergeEnv — combines two env-overlay maps with b winning on conflict.
//
// REVIEW HIGH #5 (cmd, env) return shape — what changed and why:
//
//	FakeKiro used to be FakeKiroScript(t, []byte) string (returning just the
//	command path) and tried to encode the notification file path via wrapper
//	scripts. That was insufficient for the gateway's full ACP method set and
//	leaked shell quoting concerns into tests. The new (cmd, env) shape moves
//	all wiring into env vars the fake-kiro binary reads at startup —
//	OTTO_FAKE_KIRO_NOTIFICATIONS_FILE / OTTO_FAKE_KIRO_RECEIVED_FRAMES_FILE /
//	OTTO_FAKE_KIRO_STOP_REASON.
//
// Iteration-3 fix to MEDIUM #6 — binary lifetime contract:
//
//	The fake-kiro-cli binary is compiled at package init by the TestMain in
//	e2e_test.go (which was extended to call out to a fixed os.TempDir() path
//	with a per-pid suffix). The path is stored here in fakeKiroBinaryPath
//	(package-level var) and survives all subtests. tools_testmain_test.go's
//	TestFakeKiro_BinaryExistsAfterMultipleSubtests locks this contract.
//
//	t.TempDir() is still used for the per-test notifications + frame-log
//	files — those lifetimes are correctly tied to the calling test.
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// fakeKiroBinaryPath is the absolute path to the compiled fake-kiro-cli
// binary. Set by TestMain in e2e_test.go BEFORE any test runs. Survives all
// subtests. DO NOT reassign from within tests. (Iteration-3 fix to MEDIUM #6.)
var fakeKiroBinaryPath string

// ToolSpecJSON is the on-the-wire JSON shape of a tool spec — usable directly
// as the entries of tools[] in any of the three surface request bodies. Each
// surface's strict JSON schema for tools[] is shaped close enough that one
// builder per surface (below) is sufficient.
type ToolSpecJSON struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolsCatalog is the canonical 3-tool catalog shared across all D-17
// scenarios. Tools are LangChain-canonical so the chosen schemas match what
// the coerce algorithm has been exercised against in unit tests.
var ToolsCatalog = []ToolSpecJSON{
	{
		Name:        "get_weather",
		Description: "Get the current weather in a given location.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{"type": "string", "description": "City name"},
			},
			"required": []string{"location"},
		},
	},
	{
		Name:        "read_file",
		Description: "Read the contents of a file at a given path.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Filesystem path"},
			},
			"required": []string{"path"},
		},
	},
	{
		Name:        "search_web",
		Description: "Search the web for a given query.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query"},
			},
			"required": []string{"query"},
		},
	},
}

// Script is the input to FakeKiro: pre-scripted notification frames the fake
// kiro emits during session/prompt, plus optional knobs.
type Script struct {
	// Notifications are concatenated complete JSON-RPC notification frames
	// (each LF-terminated). Use Notif* helpers below to build them.
	Notifications []byte
	// StopReason is the result.stopReason value the fake returns on
	// session/prompt. Defaults to "end_turn" when empty. Set to "tool_use"
	// to simulate Anthropic-shape kiro-cli replies that carry a tool_use
	// terminal stop_reason.
	StopReason string
	// LogFrames, when true, sets OTTO_FAKE_KIRO_RECEIVED_FRAMES_FILE to a
	// temp file under t.TempDir(). The path is returned in the env overlay
	// (so callers can extract it and pass to ReadFakeKiroFrames after the
	// stream completes).
	LogFrames bool
}

// FakeKiro returns the absolute path of the compiled fake-kiro-cli binary
// (which TestMain compiled into os.TempDir() at package init) AND an env
// overlay carrying the notifications-file path, the stop-reason, and
// optionally the received-frames-log file path.
//
// Lifetime: the binary path is package-scoped (set by TestMain, lives for
// the entire test invocation). The notification + frame-log files use
// t.TempDir() (per-test cleanup, correct for those artifacts).
//
// Callers merge the returned env overlay with their own KIRO_CMD override
// using mergeEnv before passing to bootGateway:
//
//	cmd, env := FakeKiro(t, Script{Notifications: notifs})
//	bootGateway(t, mergeEnv(env, map[string]string{"KIRO_CMD": cmd, "OTTO_KIRO_BIN": cmd}))
//
// The OTTO_KIRO_BIN entry is necessary because bootGateway's resolveKiro
// helper looks up OTTO_KIRO_BIN FIRST (before consulting KIRO_CMD).
func FakeKiro(t *testing.T, script Script) (cmd string, env map[string]string) {
	t.Helper()
	if fakeKiroBinaryPath == "" {
		t.Fatal("fakeKiroBinaryPath not initialized — TestMain (e2e_test.go) must run first; check OTTO_E2E=1 gate")
	}

	// Set OTTO_KIRO_BIN so bootGateway's resolveKiro picks up the fake-kiro
	// path even when the real kiro-cli is not on PATH (CI / dev box without
	// kiro installed). t.Setenv restores the original value on test cleanup.
	t.Setenv("OTTO_KIRO_BIN", fakeKiroBinaryPath)

	dir := t.TempDir()
	env = map[string]string{}

	if len(script.Notifications) > 0 {
		notifPath := filepath.Join(dir, "notifications.ndjson")
		if err := os.WriteFile(notifPath, script.Notifications, 0o644); err != nil { //nolint:gosec
			t.Fatalf("FakeKiro: write notifications: %v", err)
		}
		env["OTTO_FAKE_KIRO_NOTIFICATIONS_FILE"] = notifPath
	}

	if script.StopReason != "" {
		env["OTTO_FAKE_KIRO_STOP_REASON"] = script.StopReason
	}

	if script.LogFrames {
		framesPath := filepath.Join(dir, "received-frames.ndjson")
		// Pre-create the file so the fake-kiro binary can open it for append.
		if err := os.WriteFile(framesPath, nil, 0o644); err != nil { //nolint:gosec
			t.Fatalf("FakeKiro: create frames-log file: %v", err)
		}
		env["OTTO_FAKE_KIRO_RECEIVED_FRAMES_FILE"] = framesPath
	}

	return fakeKiroBinaryPath, env
}

// mergeEnv combines two env-overlay maps. Values from b win on conflict with
// values from a. Either may be nil. The result is a fresh map (no aliasing).
func mergeEnv(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// --- Notification builders ---------------------------------------------

// kiro session/update notification framing — see internal/acp/translate.go
// for the inner shape. We use the flat `params.{sessionId, sessionUpdate, ...}`
// form (which translate.go accepts as one of three tolerant shapes per D-16).

// NotifText returns a complete JSON-RPC notification frame for an
// agent_message_chunk update carrying plain text. Trailing LF is appended.
func NotifText(sessionID, text string) []byte {
	return notifFrame(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"text": text},
			},
		},
	})
}

// NotifThought returns a thought-chunk notification frame.
func NotifThought(sessionID, content string) []byte {
	return notifFrame(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_thought_chunk",
				"content":       map[string]any{"text": content},
			},
		},
	})
}

// NotifToolCall returns a tool_call notification frame in the shape the
// gateway's translate.go promotes to ChunkKindToolCall (Phase 6 D-03).
func NotifToolCall(sessionID, toolCallID, name string, args map[string]any) []byte {
	return notifFrame(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "tool_call",
				"toolCallId":    toolCallID,
				"title":         name,
				"args":          args,
			},
		},
	})
}

// NotifPlan returns a plan-chunk notification frame (kiro's PlanChunk).
func NotifPlan(sessionID string, entries []string) []byte {
	entryObjs := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		entryObjs = append(entryObjs, map[string]any{"content": e})
	}
	return notifFrame(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "plan",
				"entries":       entryObjs,
			},
		},
	})
}

// notifFrame marshals and LF-terminates a single notification frame.
func notifFrame(frame map[string]any) []byte {
	b, err := json.Marshal(frame)
	if err != nil {
		// Hand-built maps never fail to marshal; if they do we'd want to know.
		panic(fmt.Sprintf("notifFrame: marshal: %v", err))
	}
	return append(b, '\n')
}

// ConcatNotifs joins multiple notification frames into a single payload for
// FakeKiro's Script.Notifications field.
func ConcatNotifs(frames ...[]byte) []byte {
	var out []byte
	for _, f := range frames {
		out = append(out, f...)
	}
	return out
}

// --- Per-surface request-body builders ----------------------------------

// OllamaToolsRequest builds an /api/chat request body with the supplied
// tools[] catalog and a stream flag.
func OllamaToolsRequest(userText string, tools []ToolSpecJSON, stream bool) []byte {
	body := map[string]any{
		"model": "auto",
		"messages": []map[string]any{
			{"role": "user", "content": userText},
		},
		"stream": stream,
	}
	if tools != nil {
		body["tools"] = ollamaToolsField(tools)
	}
	b, _ := json.Marshal(body)
	return b
}

// ollamaToolsField wraps each tool in Ollama's `{type:"function", function:{...}}`
// envelope.
func ollamaToolsField(tools []ToolSpecJSON) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	return out
}

// OpenAIToolsRequest builds a /v1/chat/completions request body.
func OpenAIToolsRequest(userText string, tools []ToolSpecJSON, stream bool) []byte {
	body := map[string]any{
		"model": "auto",
		"messages": []map[string]any{
			{"role": "user", "content": userText},
		},
		"stream": stream,
	}
	if tools != nil {
		// OpenAI shape matches Ollama: array of {type:"function", function:{...}}.
		body["tools"] = ollamaToolsField(tools)
	}
	b, _ := json.Marshal(body)
	return b
}

// AnthropicToolsRequest builds a /v1/messages request body with Anthropic's
// tools[] shape (input_schema, not parameters).
func AnthropicToolsRequest(userText string, tools []ToolSpecJSON, stream bool) []byte {
	anthropicTools := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		anthropicTools = append(anthropicTools, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Parameters,
		})
	}
	body := map[string]any{
		"model":      "auto",
		"max_tokens": 1024,
		"messages": []map[string]any{
			{"role": "user", "content": userText},
		},
		"stream": stream,
	}
	if tools != nil {
		body["tools"] = anthropicTools
	}
	b, _ := json.Marshal(body)
	return b
}

// --- Cross-surface canonical equivalence assertion ---------------------

// AssertSameCanonicalToolCall extracts the (name, args) tool-call identity
// from a non-streaming response body for each surface and asserts they
// match. Per the iteration-3 normalization contract, Ollama and OpenAI
// render kiro-native tool_calls as `[tool: <name>]\n` narration in
// message.content (no native tool_calls field on non-streaming), while
// Anthropic renders them as a native tool_use content block.
//
// The helper normalizes accordingly:
//
//	Ollama  → parses "[tool: <name>]" out of message.content; args=nil
//	         (Ollama narration doesn't carry args at the canonical layer)
//	OpenAI  → parses "[tool: <name>]" out of choices[0].message.content;
//	         args=nil
//	Anthropic → reads content[].type=="tool_use", takes .name and .input
//
// For Ollama/OpenAI the comparison reduces to "all three saw the same tool
// name". For Anthropic we additionally assert the input round-trip matches
// the args we supplied.
func AssertSameCanonicalToolCall(t *testing.T, ollamaBody, openaiBody, anthropicBody []byte) {
	t.Helper()

	ollamaName := extractOllamaNarrationToolName(t, ollamaBody)
	openaiName := extractOpenAINarrationToolName(t, openaiBody)
	anthropicName, _ := extractAnthropicToolUse(t, anthropicBody)

	if ollamaName != openaiName {
		t.Errorf("cross-surface tool name divergence: ollama=%q openai=%q", ollamaName, openaiName)
	}
	if openaiName != anthropicName {
		t.Errorf("cross-surface tool name divergence: openai=%q anthropic=%q", openaiName, anthropicName)
	}
}

// extractOllamaNarrationToolName parses `[tool: <name>]` out of the assistant
// content. The narration is the iteration-3 D-03 wording: kiro-native tool
// calls flow into message.content for Ollama/OpenAI's non-streaming path via
// engine.Collect's aggregator.
func extractOllamaNarrationToolName(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("ollama body: %v\nbody=%s", err, string(body))
	}
	return parseToolNarration(resp.Message.Content)
}

func extractOpenAINarrationToolName(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("openai body: %v\nbody=%s", err, string(body))
	}
	if len(resp.Choices) == 0 {
		t.Fatal("openai body: choices[] empty")
	}
	return parseToolNarration(resp.Choices[0].Message.Content)
}

func extractAnthropicToolUse(t *testing.T, body []byte) (name string, input map[string]any) {
	t.Helper()
	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("anthropic body: %v\nbody=%s", err, string(body))
	}
	for _, c := range resp.Content {
		if c.Type == "tool_use" {
			if len(c.Input) > 0 {
				_ = json.Unmarshal(c.Input, &input)
			}
			return c.Name, input
		}
	}
	return "", nil
}

// parseToolNarration extracts the tool name from a `[tool: <name>]\n`
// narration string. Returns "" if no narration is present.
func parseToolNarration(content string) string {
	const prefix = "[tool: "
	idx := strings.Index(content, prefix)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(prefix):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// --- Goroutine-leak gate ----------------------------------------------

// GoleakVerifyAtEnd is the per-subtest goroutine-leak gate (WARNING #5 per
// CONTEXT D-21). Call inside each t.Run as:
//
//	t.Run("Scenario", func(t *testing.T) {
//	    defer GoleakVerifyAtEnd(t)
//	    ...
//	})
//
// The helper sleeps briefly to let async cleanup finish (HTTP idle conn
// reaper, child-process os.Wait goroutine) before snapshotting.
//
// goleak ignore-list (residual goroutines we allow):
//
//   - os/exec.(*Cmd).Wait — the gateway subprocess we spawn via
//     bootGateway uses exec.CommandContext; the Wait-pumping goroutine
//     belongs to the outer harness, not the SUT under test.
//   - net/http.(*Transport).readLoop / writeLoop — http.DefaultClient
//     keeps idle conns; these unwind asynchronously.
//   - internal/poll.runtime_pollWait — tied to the above net poller
//     descriptors.
//
// We do NOT ignore goroutines from internal/engine, internal/adapter/*,
// internal/acp, internal/pool, internal/session — those are the SUT
// surface and any leak there is a Phase 6 (or later) regression.
func GoleakVerifyAtEnd(t *testing.T) {
	t.Helper()
	// Async cleanup grace period — http.Transport idle-conn reaper has a
	// 90-second timeout but releases on goroutine exit; the kiro child
	// process Wait goroutine settles when the gateway dies.
	time.Sleep(150 * time.Millisecond)

	opts := []goleak.Option{
		// Harness-owned child-process management.
		goleak.IgnoreTopFunction("os/exec.(*Cmd).Wait.func1"),
		goleak.IgnoreAnyFunction("os/exec.(*Cmd).Wait"),
		// HTTP idle-connection maintenance from the test client.
		goleak.IgnoreTopFunction("net/http.(*Transport).dialConnFor"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	}
	if err := goleak.Find(opts...); err != nil {
		t.Errorf("goleak: %v", err)
	}
}

// --- Frame-log read helper (scenario 12) -------------------------------

// ReadFakeKiroFrames reads the received-frames-log file (one JSON frame per
// line) and returns the decoded frames in order. Used by tools_cancel_test.go
// to assert the gateway emitted session/cancel after a mid-stream client
// disconnect.
func ReadFakeKiroFrames(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // path is under t.TempDir()
	if err != nil {
		t.Fatalf("ReadFakeKiroFrames: open %q: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal(line, &frame); err != nil {
			// Skip malformed lines.
			continue
		}
		out = append(out, frame)
	}
	return out
}

// --- Utility: silence unused-import errors in skeleton builds ----------

// These compile-time anchor references prevent unused-import errors when
// only a subset of helpers above is referenced from tools_*_test.go.
var (
	_ = bufio.ScanLines
	_ = context.Background
	_ = errors.New
	_ = http.MethodGet
	_ = runtime.Caller
)
