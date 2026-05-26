# Phase 6: Tool-Call Path - Pattern Map

**Mapped:** 2026-05-26
**Files analyzed:** 19 (7 NEW + 12 MODIFIED)
**Analogs found:** 19 / 19

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| **NEW** `internal/engine/coerce.go` | engine helper (pure) | inbound transform (in-place response mutate) | `internal/engine/collect.go` (in-engine canonical mutation) + `internal/engine/build_acp.go` (canonical-typed pure helper) | role-match (no prior coerce-equivalent) |
| **NEW** `internal/engine/coerce_test.go` | test (property + table) | unit + property | `internal/engine/pickcwd_test.go` (testing/quick precedent + Example fn) | exact |
| **NEW** `tests/e2e/tools_ollama_test.go` | E2E harness (real-binary) | request-response + streaming | `tests/e2e/ollama_e2e_test.go` (same package, same helpers) | exact |
| **NEW** `tests/e2e/tools_openai_test.go` | E2E harness | request-response + streaming | `tests/e2e/openai_e2e_test.go` | exact |
| **NEW** `tests/e2e/tools_anthropic_test.go` | E2E harness | SSE streaming | `tests/e2e/anthropic_e2e_test.go` | exact |
| **NEW** `tests/e2e/tools_cancel_test.go` | E2E harness (per-surface) | mid-stream cancel + goleak | `tests/e2e/ollama_e2e_test.go::Chat_DisconnectSmoke` (line 309) | exact |
| **NEW** `tests/e2e/tools_fixtures.go` | shared test fixture (non-test) | data declarations | NO direct analog — fake-kiro fixture pattern in `internal/acp/fakeacp_test.go` is in-package only; no prior cross-test fixture file in tests/e2e/ | partial (fakeACPServer is similar in spirit) |
| **MOD** `internal/canonical/chunk.go` (lines 47-53) | canonical type extension | data | `internal/canonical/chunk.go::ToolCallChunk` (same file, extend in place) | exact (self) |
| **MOD** `internal/acp/translate.go` (lines 235-242) | ACP→canonical translator | inbound | `internal/acp/translate.go::translateUpdate plan branch` (line 250-254 — same fn, real ChunkKind* emission with pointer field) | exact |
| **MOD** `internal/engine/build_acp.go` (lines 64-69) | engine block-builder | outbound (to ACP) | `internal/engine/build_acp.go::Format branch` (line 58-63) + the existing System branch (line 52-54) for fmt.Fprintf pattern | exact (self) |
| **MOD** `internal/engine/collect.go` (lines 56-70) | engine stream aggregator | streaming → response assembly | `internal/engine/collect.go::ChunkKindText/ChunkKindThought aggregators` (line 56-70) — add third sibling aggregator | exact (self — additive) |
| **MOD** `internal/adapter/ollama/handlers.go` | HTTP handler (coerce hook-in) | both | `internal/adapter/ollama/handlers.go::handleChat` Collect path (line 73-83) — insert coerce call between Collect and render | exact (self) |
| **MOD** `internal/adapter/ollama/render.go` | wire renderer (non-streaming) | outbound | `internal/adapter/ollama/render.go::chatResponseToWire` (line 27-74) — extend to populate ollamaChatResponseMessage.ToolCalls | exact (self — but note: wire.go:86 currently lacks ToolCalls on the response message type — see Adaptation Notes) |
| **MOD** `internal/adapter/ollama/ndjson.go` | NDJSON emitter (streaming) | outbound stream | `internal/adapter/ollama/ndjson.go::finalizeNDJSON` (line 197-243) — extend the done:true line composition | exact (self) |
| **MOD** `internal/adapter/openai/handlers.go` | HTTP handler (coerce hook-in) | both | `internal/adapter/openai/handlers.go::handleChatCompletions` Collect path (line 95-103) — insert coerce call between Collect and render | exact (self) |
| **MOD** `internal/adapter/openai/wire.go` (line 38) | wire decoder (tool spec) | inbound | `internal/adapter/ollama/wire.go::ollamaToolSpec → canonical.ToolSpec` (line 46-55 + decode at 321-330) | role-match (cross-adapter Ollama pattern) |
| **MOD** `internal/adapter/openai/render.go` | wire renderer (JSON-string args) | outbound | `internal/adapter/openai/render.go::chatResponseToCompletion` (line 117-153) — extend `responseMessage` and populate from `resp.Message.ToolCalls` | exact (self) |
| **MOD** `internal/adapter/openai/sse.go` (lines 110-116) | SSE emitter | outbound stream | `internal/adapter/openai/sse.go::applyChunk drop branch` (line 110-116) + existing roleSent gate (line 119-128) — extend with tool_call multi-frame | exact (self) |
| **MOD** `internal/adapter/anthropic/wire.go` (lines 202-206/242) | wire decoder (tool spec) | inbound | `internal/adapter/ollama/wire.go:321-330` (proven pattern: walk wire tools[], populate canonical.ToolSpec) | role-match (cross-adapter) |
| **MOD** `internal/adapter/anthropic/sse.go` (lines 204-220) | SSE emitter (state machine) | outbound stream | `internal/adapter/anthropic/sse.go::applyChunk text/thinking branches` (line 204-270) — extend switch with ChunkKindToolCall as 3rd kind | exact (self) |

## Pattern Assignments

### NEW `internal/engine/coerce.go` (engine helper, pure inbound transform)

**Closest analog:** `internal/engine/collect.go` lines 56-83 (engine-owned canonical mutation) + `internal/engine/build_acp.go` lines 44-69 (canonical-typed pure helper signature pattern).

**Package + imports pattern** (`internal/engine/build_acp.go:10-18`):
```go
package engine

import (
	"encoding/base64"
	"fmt"
	"strings"

	"otto-gateway/internal/canonical"
)
```

**Pure-helper signature pattern** (`internal/engine/build_acp.go:44`):
```go
func buildBlocks(req *canonical.ChatRequest) []canonical.Block {
	if req == nil {
		return []canonical.Block{
			{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: ""}},
		}
	}
	// ... per-field walk ...
}
```

**Canonical-mutation pattern** (`internal/engine/collect.go:118-146` — `assembleChatResponse` mutates a fresh `*canonical.ChatResponse`):
```go
content := []canonical.ContentPart{
	{Kind: canonical.ContentKindText, Text: text},
}
if thinking != "" {
	content = append(content, canonical.ContentPart{
		Kind: canonical.ContentKindThinking,
		Text: thinking,
	})
}
return &canonical.ChatResponse{
	ID:    fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
	// ...
}
```

**Adaptation notes:**
- **Stays the same:** Canonical-typed signature, defensive nil guards, no JSON tags on canonical types (D-11 invariant), `fmt.Sprintf("call_%d", time.Now().UnixNano())` for ID synthesis (D-11 — mirrors `chatcmpl-` in `collect.go:137`).
- **Changes vs. `assembleChatResponse`:** in-place mutation of `*canonical.ChatResponse` (NOT fresh allocation). Per CONTEXT D-01 returns `bool` so adapters can debug-log a `coerce=true` tag.
- **New surface area:** export `CoerceToolCall` (capitalized) so `internal/adapter/ollama` and `internal/adapter/openai` can call it; private `pickBestTool` and `stripFences` helpers stay lowercase.
- **Idempotency invariant** (D-02): first guard is `len(resp.Message.ToolCalls) > 0 → return false` BEFORE any text mutation.
- **D-09 9-step order is LOCKED**; see RESEARCH §"`coerceToolCall` algorithm" lines 414-429 for the exact sequence.

---

### NEW `internal/engine/coerce_test.go` (property + table tests)

**Closest analog:** `internal/engine/pickcwd_test.go` lines 1-260 (full file — TRST-06 precedent).

**Property-test pattern** (`internal/engine/pickcwd_test.go:204-240`):
```go
// TestPickCwd_NeverPanics (TRST-06 property test) — pickCwd MUST NOT
// panic for any ChatRequest shape. testing/quick generates random
// requests with random ResourceLinks; the function under test should
// always terminate cleanly.
func TestPickCwd_NeverPanics(t *testing.T) {
	property := func(override string, uris []string, defaultCwd string) bool {
		links := make([]canonical.ResourceLinkBlock, 0, len(uris))
		for _, u := range uris {
			links = append(links, canonical.ResourceLinkBlock{URI: u})
		}
		req := &canonical.ChatRequest{
			WorkingDirOverride: override,
			ResourceLinks:      links,
		}
		_ = pickCwd(req, defaultCwd)
		return true
	}

	cfg := &quick.Config{MaxCount: 1000}
	if err := quick.Check(property, cfg); err != nil {
		t.Errorf("pickCwd property check failed: %v", err)
	}

	// Also defensively assert nil-request behaviour.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("pickCwd panicked on nil request: %v", r)
		}
	}()
	_ = pickCwd(nil, "/some/default")
}
```

**Runnable Example pattern** (`internal/engine/pickcwd_test.go:246-260`) — TRST-07 godoc gate:
```go
func Example_pickCwd() {
	req := &canonical.ChatRequest{ /* ... */ }
	got := pickCwd(req, "/fallback")
	fmt.Println(got)
	// Output: /workspace/proj
}
```

**Table-driven case pattern** (`internal/engine/pickcwd_test.go:19-84`) — for the D-09 9-step matrix (raw parse / fenced parse / no-match / tie-breaker / idempotency).

**Adaptation notes:**
- **Stays the same:** `testing/quick.Config{MaxCount: 1000}`, separate property + table + Example funcs, `t.Helper()` discipline, deferred recover for nil-input panic guard.
- **Changes:** property fn takes `(text string, toolNames []string)` per RESEARCH lines 435-453; assert the five invariants from CONTEXT D-12 (never-panic, always-terminate, idempotent, round-trip, no-match no-mutation).
- **D-20** explicitly names this file path; no flexibility.

---

### NEW `tests/e2e/tools_{ollama,openai,anthropic,cancel}_test.go` (E2E real-binary harness)

**Closest analog:** `tests/e2e/anthropic_e2e_test.go` (shared-helpers reuse pattern — line 7-13 doc-comment is the recipe).

**Package + build-tag + shared-helpers reuse** (`tests/e2e/anthropic_e2e_test.go:1-29`):
```go
//go:build e2e

// This file is part of package e2e_test (same package as e2e_test.go). It adds
// a dedicated TestE2E_Anthropic test function...
//
// It REUSES the shared helpers declared in e2e_test.go (gateOrSkip,
// bootGateway, readAll, postMessages, assertStrictSSE, assertMessageShape) and
// MUST NOT redefine any of them — doing so would be a redeclaration compile error.
package e2e_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)
```

**Shared-gateway boot + subtest pattern** (`tests/e2e/anthropic_e2e_test.go:34-67`):
```go
func TestE2E_Anthropic(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, nil)
	defer cleanup()

	t.Run("Messages_Streaming", func(t *testing.T) {
		body := []byte(`{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"say hi"}],"stream":true}`)
		resp := postMessages(t, baseURL, body, map[string]string{"x-api-key": "e2e-token"})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
		}
		// ... assertions ...
		assertStrictSSE(t, resp)
	})
}
```

**Ollama request helper** (`tests/e2e/ollama_e2e_test.go:32-56`) — `ollamaRequest(t, method, url, body, authHeader)` for `/api/chat`. Already exists; tools tests just call it.

**Mid-stream disconnect pattern** (`tests/e2e/ollama_e2e_test.go:309-385` — `Chat_DisconnectSmoke`) — load-bearing analog for `tools_cancel_test.go`:
```go
t.Run("Chat_DisconnectSmoke", func(t *testing.T) {
	// ... open streaming request, cancel mid-stream, assert no-leak ...
})
```

**Adaptation notes:**
- **Stays the same:** `//go:build e2e` tag, `gateOrSkip(t)`, `bootGateway(t, nil)` + `defer cleanup()`, sub-test naming convention (`Native…`, `Coerce_…`, `NoCoerce`), JSON decoder + structured assertions.
- **Changes per D-19:** `tools_ollama_test.go` runs scenarios 1–4 and 6–11; `tools_openai_test.go` same set; `tools_anthropic_test.go` runs scenarios 1, 2, 5, 9 (note: no coerce); `tools_cancel_test.go` runs scenario 12 per-surface as nested subtests.
- **NEW capability needed — controllable fake kiro-cli (D-19):** the existing `bootGateway` uses real `kiro-cli` via `OTTO_KIRO_BIN`. Tools tests need to swap `KIRO_CMD` to a scripted fake binary that emits chosen `tool_call` ACP notifications. Use `bootGateway(t, map[string]string{"KIRO_CMD": "<path-to-fake-script>"})` overlay — the helper already supports the map overlay (line 158-160).
- **goleak gate (D-21):** wrap each `TestE2E_Tools_*` in `goleak.VerifyNone(t)` (existing project precedent — 21+ in-tree usages per RESEARCH).

---

### NEW `tests/e2e/tools_fixtures.go` (non-test shared fixture)

**Closest analog:** No exact in-tree precedent (no prior cross-test fixture file in tests/e2e/). Closest spirit: `internal/acp/fakeacp_test.go::fakeACPServer` (scripted-notification pattern, in-package only).

**Reference fake-server emit pattern** (`internal/acp/fakeacp_test.go::emitUpdate` lines 322-360):
```go
func (f *fakeACPServer) emitUpdate(sessionID string, v updateVariant) error {
	var notif map[string]any
	switch v {
	case variantToolCallWrapped:
		notif = map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": sessionID,
				"update": map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    "tc_1",
					"title":         "read_file",
					"args":          map[string]any{ /* ... */ },
				},
			},
		}
	}
	// ...
}
```

**Adaptation notes:**
- **No `_test.go` suffix** (RESEARCH line 608 — must be a regular Go file so multiple `_test.go` files share it without redeclaration).
- **Build tag:** still `//go:build e2e` so the file only compiles in the E2E build (avoids polluting the default build).
- **Contents per RESEARCH line 608:**
  1. The canonical 3-tool catalog (`get_weather`, `read_file`, `search_web` — RESEARCH §Test data discretion).
  2. A `fakeKiroScript(t *testing.T, notifications []byte) string` helper that writes a controllable shell script (or small Go binary) into `t.TempDir()` and returns the path. The script's stdin is JSON-RPC; it emits the supplied `notifications` then a `session/prompt` response.
  3. JSON-RPC notification builder helpers mirroring `fakeacp_test.go` shapes (tool_call notif, agent_message_chunk notif).
- **Why not inline in each test file:** the 3-tool catalog must be byte-identical across the 4 test files for diff readability (CONTEXT discretion §test data).

---

### MOD `internal/canonical/chunk.go` lines 47-53 (canonical type extension)

**Current state** (`internal/canonical/chunk.go:47-53`):
```go
// ToolCallChunk carries a tool invocation from kiro-cli.
type ToolCallChunk struct {
	// Name is the tool name.
	Name string
	// Args is the tool arguments as a map.
	Args map[string]any
}
```

**Pattern to extend** — same file, just add a field with doc comment per D-08 + the Phase 2 D-11 invariant (no JSON tags on canonical):

**Adaptation notes:**
- **Stays the same:** no JSON tags (canonical invariant per Phase 2 D-11), struct in same file, godoc above each field.
- **Changes:** add `ID string` field; doc-comment notes "populated from `toolCallId` on the ACP wire OR synthesized by `coerceToolCall` when source is text-not-kiro" (CONTEXT specifics §`ToolCallChunk.ID`).
- **No breaking change:** Go struct literals with named fields are forward-compatible; existing `canonical.ToolCallChunk{Name: ..., Args: ...}` literals still compile.

---

### MOD `internal/acp/translate.go` lines 235-242 (ACP→canonical translator)

**Current state** (`internal/acp/translate.go:235-242`):
```go
case "tool_call", "tool_call_chunk":
	// CONTEXT.md <deferred>: render as thought with [tool: <title>]\n
	// prefix; Phase 6 emits canonical.ToolCallChunk properly.
	title := firstNonEmpty(body.Title, "unknown")
	return canonical.Chunk{
		Kind:    canonical.ChunkKindThought,
		Thought: &canonical.ThoughtChunk{Content: fmt.Sprintf("[tool: %s]\n", title)},
	}, true
```

**Closest analog (real ChunkKind* return pattern)** — the same function, `plan` branch (line 250-254):
```go
case "plan":
	return canonical.Chunk{
		Kind: canonical.ChunkKindPlan,
		Plan: &canonical.PlanChunk{Content: joinPlanEntries(body.Entries)},
	}, true
```

**Adaptation notes:**
- **Stays the same:** the `case "tool_call", "tool_call_chunk":` discriminator stays (both ACP variants route here — per Pitfall 8, kiro emits atomically so no aggregation needed); `firstNonEmpty(body.Title, ...)` for name fallback; return `(canonical.Chunk, bool)` signature.
- **Changes:** emit `canonical.Chunk{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{ID: body.ToolCallID, Name: body.Title, Args: body.Args}}`. The body fields are ALREADY DECODED on `sessionUpdateBody` (line 61-71) — no new wire-type fields needed.
- **Critical (Pitfall 5):** the Phase 1.1 thought-text `[tool: <name>]\n` rendering MUST be preserved at the per-surface emitter layer (D-03 line 135), NOT here. The translator promotes to real ChunkKindToolCall; each per-surface SSE/NDJSON emitter chooses to render thought-text from the ChunkKindToolCall payload.

---

### MOD `internal/engine/build_acp.go` lines 64-69 (engine block-builder)

**Current state** (`internal/engine/build_acp.go:64-69`):
```go
if len(req.Tools) > 0 {
	// Forward-design seam — Phase 6 will flesh out the tool-catalog
	// emission. Phase 2 emits only a placeholder header so the
	// section exists in the bracketed-section format.
	b.WriteString("[Available tools]\nEmit a tool_call ACP notification to invoke any of the registered tools.\n\n")
}
```

**Closest pattern (same fn) — `[Output format]` branch** (line 58-63):
```go
if req.Format != nil {
	fmt.Fprintf(&b, "[Output format] Respond ONLY in %s.\n\n", req.Format.Type)
}
```

**JSON marshal pattern (same package — `internal/acp/translate.go:117` `json.RawMessage`; `internal/adapter/openai/render.go:142` `json.Marshal`):**
```go
body, err := json.Marshal(payload)
if err != nil {
	return fmt.Errorf("...: %w", err)
}
```

**Adaptation notes:**
- **Stays the same:** the `if len(req.Tools) > 0` guard, the bracketed-section header `[Available tools]`, the trailing `\n\n` for section separation, the `strings.Builder` write pattern.
- **Changes per D-16:** after the header line, emit a fenced ```json ... ``` block containing `json.Marshal(req.Tools)` (a `[]canonical.ToolSpec` slice). Format:
  ```
  [Available tools]
  Emit a tool_call ACP notification to invoke any of the registered tools.

  ```json
  [{"name":"...","description":"...","parameters":{...}}]
  ```
  ```
- **Marshal error handling:** since `buildBlocks` returns `[]canonical.Block` (no error), wrap any marshal failure defensively (skip emission on error — same `continue` discipline as the malformed-image branch line 116-122).
- **New import:** add `encoding/json` to the import block (line 12-18).

---

### MOD `internal/engine/collect.go` lines 56-70 (third aggregator)

**Closest analog (same file, sibling aggregators)** — `internal/engine/collect.go:56-70`:
```go
var sb, thoughtSB strings.Builder
for chunk := range run.stream.Chunks() {
	switch chunk.Kind {
	case canonical.ChunkKindText:
		if chunk.Text != nil {
			sb.WriteString(chunk.Text.Content)
		}
	case canonical.ChunkKindThought:
		if chunk.Thought != nil {
			thoughtSB.WriteString(chunk.Thought.Content)
		}
		// ChunkKindToolCall / ChunkKindPlan still intentionally
		// dropped in Phase 3.1; Phase 6 wires them.
	}
}
```

**Adaptation notes:**
- **Stays the same:** the `switch chunk.Kind` shape, the nil-guards on `chunk.Text` / `chunk.Thought`, the `strings.Builder` for text aggregation.
- **Changes:**
  1. Add a third local variable: `var toolCalls []canonical.ToolCall` (slice — not builder; tool_calls are discrete entries).
  2. New case: `case canonical.ChunkKindToolCall: if chunk.ToolCall != nil { toolCalls = append(toolCalls, canonical.ToolCall{ID: chunk.ToolCall.ID, Name: chunk.ToolCall.Name, Arguments: chunk.ToolCall.Args}) }`
  3. Update `assembleChatResponse` (line 118-146) signature to accept `toolCalls []canonical.ToolCall`; set `Message.ToolCalls = toolCalls`.
  4. **Per CONTEXT line 31 + RESEARCH line 491:** also emit a parallel `ContentKindToolUse` part in `Message.Content` for each tool_call so the Anthropic outbound `tool_use` render path (`internal/adapter/anthropic/render.go:113-131` CR-01 fix) is reachable:
     ```go
     for _, tc := range toolCalls {
         content = append(content, canonical.ContentPart{
             Kind:    canonical.ContentKindToolUse,
             ToolUse: &canonical.ToolUsePart{ID: tc.ID, Name: tc.Name, Input: tc.Arguments},
         })
     }
     ```
- **Remove the dropped-chunks comment** at line 67-68 ("Phase 6 wires them").

---

### MOD `internal/adapter/ollama/handlers.go` (coerce hook-in)

**Closest analog (same file)** — `internal/adapter/ollama/handlers.go:73-83` Collect path:
```go
if !streamEnabled(wire.Stream) {
	// stream:false — non-streaming path: collect and return a single JSON object.
	start := time.Now()
	resp, err := eng.Collect(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, chatResponseToWire(resp, start, wire.Model))
	return
}
```

**Adaptation notes:**
- **Stays the same:** the `if !streamEnabled` branch, `eng.Collect` call, `writeJSON(... chatResponseToWire(resp, start, wire.Model))`.
- **Changes per D-01:** between `Collect` and `chatResponseToWire`, insert:
  ```go
  if engine.CoerceToolCall(req, resp) {
      a.cfg.Logger.Debug("ollama: coerce fired", "tool", resp.Message.ToolCalls[0].Name)
  }
  ```
- **Critical (Pitfall 6):** pass `resp` directly (pointer) — do NOT pre-copy `respCopy := *resp` before calling Coerce, since the slice mutation on `Message.ToolCalls` would not propagate.
- **Streaming branch (line 85-99):** Coerce is NOT called on the streaming path — the streaming chunks are emitted live before Collect runs. Per CONTEXT line 298 "the two-path rule applies SPECIFICALLY to the streaming wire shape": coerce-synthesized tool_calls live ONLY in the non-streaming Collect path.

---

### MOD `internal/adapter/ollama/render.go` (Ollama object-args populate)

**Closest analog (same file)** — `internal/adapter/ollama/render.go:56-73` (the existing `out` construction):
```go
out := &ollamaChatResponse{
	Model:     model,
	CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	Message: ollamaChatResponseMessage{
		Role:     "assistant",
		Content:  text,
		Thinking: thinking,
	},
	Done:               true,
	// ...
}
```

**Adaptation notes:**
- **CRITICAL — wire type fix needed first:** `ollamaChatResponseMessage` at `internal/adapter/ollama/wire.go:86-90` currently has only Role/Content/Thinking. Phase 6 MUST add `ToolCalls []ollamaToolCall \`json:"tool_calls,omitempty"\`` (mirroring `ollamaMessage` at line 40 which already has it).
- **Stays the same:** the assembly pattern; `joinTextContent` for text; `joinThinkingContent` for thinking.
- **Changes:** populate `Message.ToolCalls` by walking `resp.Message.ToolCalls` (canonical) → `[]ollamaToolCall`. Each entry uses `ollamaToolCallFunction{Name, Arguments}` where `Arguments` is the canonical `map[string]any` passed through verbatim (Ollama wire spec: arguments as plain object, NOT JSON string — CONTEXT D-04, SC #2, Node parity).
- **D-07 Ollama**: the same render helper is reused by `ndjson.go::finalizeNDJSON` (line 213-232) for the done:true line — populating `ToolCalls` here automatically lights up the streaming path too.

---

### MOD `internal/adapter/ollama/ndjson.go` (done:true line carries tool_calls)

**Closest analog (same file)** — `internal/adapter/ollama/ndjson.go:213-232`:
```go
var payload any
if isChat {
	out := chatResponseToWire(nil, start, model)
	out.Done = true
	out.DoneReason = mapStopReason(stopReason)
	payload = out
}
```

**Adaptation notes:**
- **Critical:** `chatResponseToWire` is called with `nil` resp here — it nil-guards. Phase 6 must pass the actual aggregated response (or, simpler, change finalizeNDJSON to take `*canonical.ChatResponse` via `run.Stream().Result()` accumulator). Per CONTEXT line 538: "extends the existing `chatResponseToWire` pattern from `render.go`."
- **Per D-07 Ollama:** the tool_calls aggregation across the stream lives in the NDJSON emitter (accumulate ChunkKindToolCall as the stream flows; emit on the done:true line). Simplest implementation: track a `[]ollamaToolCall` slice on the `runNDJSONEmitter` goroutine alongside the existing chunk processing; populate `out.Message.ToolCalls` from that slice in `finalizeNDJSON`.
- **Stays the same:** the `Done = true` + `DoneReason = mapStopReason(stopReason)` post-fixup pattern, the `flusher.Flush()` discipline, the D-05 single-goroutine invariant (the slice is only touched in the select-loop goroutine).
- **Also extend `emitNDJSONChunk` (line 60-123):** add a `case canonical.ChunkKindToolCall:` branch that drops the chunk (no per-line emission — atomic on done:true per D-07 Ollama) OR emits the `[tool: <name>]\n` thought-text variant per D-03 line 135. Recommendation: emit thought-text per-line AND aggregate for the done line — covers both the operator-visible narration (D-03 Wire rendering) AND the structured tool_calls[] payload (D-04 + D-05).

---

### MOD `internal/adapter/openai/handlers.go` (coerce hook-in)

**Closest analog (same file)** — `internal/adapter/openai/handlers.go:95-103` non-streaming Collect path:
```go
resp, err := eng.Collect(r.Context(), req)
if err != nil {
	a.cfg.Logger.Error("openai: engine.Collect error", "err", err)
	writeError(w, http.StatusInternalServerError, errAPI, "internal error")
	return
}

writeJSON(w, chatResponseToCompletion(resp, wire.Model))
```

**Adaptation notes:**
- **Stays the same:** error logging discipline (T-02-33 log raw / respond generic).
- **Changes:** between Collect and render, insert `engine.CoerceToolCall(req, resp)` with a debug log on `true`. Same pointer-semantics caveat as Ollama (Pitfall 6).
- **Streaming branch (line 70-92):** no Coerce call (per D-01 + the "two-path" CONTEXT line 298 reasoning).

---

### MOD `internal/adapter/openai/wire.go` line 38 (typed tools decode)

**Closest analog (cross-adapter)** — `internal/adapter/ollama/wire.go:46-55, 321-330`:
```go
// Wire type (line 46-55):
type ollamaToolSpec struct {
	Type     string                  `json:"type,omitempty"`
	Function *ollamaToolSpecFunction `json:"function,omitempty"`
}

type ollamaToolSpecFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// Decode in wireToChatRequest (line 321-330):
for _, t := range w.Tools {
	if t.Function == nil {
		continue
	}
	req.Tools = append(req.Tools, canonical.ToolSpec{
		Name:        t.Function.Name,
		Description: t.Function.Description,
		Parameters:  t.Function.Parameters,
	})
}
```

**Adaptation notes:**
- **Stays the same:** the per-spec walk + canonical.ToolSpec append pattern (OpenAI's `tools[].function` shape is byte-identical to Ollama's — `{name, description, parameters}` per OpenAI public spec).
- **Changes:**
  1. Replace `Tools json.RawMessage` (line 38) with `Tools []openAIToolSpec \`json:"tools,omitempty"\`` matching the Ollama shape.
  2. Define `openAIToolSpec` + `openAIToolSpecFunction` types (mirror Ollama).
  3. In `wireToChatRequest` (after the message walk, before `return req`), copy the Ollama loop verbatim with renamed types.
- **Also (D-13 second sentence):** decode `tool_choice` from its existing `FunctionCall json.RawMessage` field at line 39 (or add a new `ToolChoice` field). OpenAI's tool_choice shape is `"auto" | "required" | "none" | {type:"function", function:{name:"..."}}` — polymorphic JSON, same pattern as `chatMessage.Content` decode at line 138-164.

---

### MOD `internal/adapter/openai/render.go` (JSON-string args populate)

**Closest analog (same file)** — `internal/adapter/openai/render.go:117-153`:
```go
out.Choices = []completionChoice{
	{
		Index: 0,
		Message: responseMessage{
			Role:    "assistant",
			Content: text,
		},
		FinishReason: mapFinishReason(stopReason),
	},
}
```

**JSON-marshal-to-string idiom** — new for this file; use `encoding/json.Marshal`:
```go
argsJSON, err := json.Marshal(tc.Arguments) // canonical map[string]any → []byte
if err != nil {
    argsJSON = []byte("{}")
}
// argsJSON is the JSON-string value for OpenAI's wire.
```

**Adaptation notes:**
- **CRITICAL — wire type fix needed first:** `responseMessage` (line 94-97) currently has only Role + Content. Phase 6 MUST add `ToolCalls []openAIToolCall \`json:"tool_calls,omitempty"\`` and define `openAIToolCall` + `openAIToolCallFunction` wire types.
- **Stays the same:** the `Choices = []completionChoice{...}` assembly, the `mapFinishReason(stopReason)` mapping (but see Open Question 2 in RESEARCH — Phase 6 may want `mapFinishReason` to return `"tool_calls"` when `Message.ToolCalls != nil`).
- **Per SC #1 + D-07 OpenAI:** `tool_calls[].function.arguments` is a JSON-encoded **string**, NOT an object. The opposite of Ollama (D-04 plain object). This is THE Phase 6 wire-shape divergence (CONTEXT specifics §"Tool-call argument shape divergence is THE Phase 6 wire-test axis").
- **Cross-check:** Bifrost's `core/providers/openai/openai.go` `tool_calls[].function.arguments` JSON-string render — RESEARCH line 495-496.

---

### MOD `internal/adapter/openai/sse.go` lines 110-116 (multi-frame tool_call SSE)

**Current state** (`internal/adapter/openai/sse.go:110-116`):
```go
func (e *sseEmitter) applyChunk(c canonical.Chunk) error {
	if c.Kind != canonical.ChunkKindText {
		// Unsupported chunk kind (ChunkKindThought, ChunkKindToolCall, etc.)
		// — drop silently. Phase 3 scope.
		e.logger.Debug("openai: sse unsupported chunk kind dropped", "kind", c.Kind)
		return nil
	}
	// ...
}
```

**Closest pattern (same file)** — the existing `roleSent` gate for first-frame role injection (line 119-128) — Phase 6's multi-frame tool_call follows the same "emit-once-before-content" shape:
```go
if !e.roleSent {
	if err := e.writeData(e.buildChunk(chunkChoice{
		Index:        0,
		Delta:        chunkDelta{Role: "assistant"},
		FinishReason: nil,
	})); err != nil {
		return err
	}
	e.roleSent = true
}
```

**Adaptation notes:**
- **Per D-07 OpenAI + RESEARCH §"OpenAI streaming tool_calls multi-frame":**
  1. Frame 1: `delta.tool_calls[0] = {index:0, id, type:"function", function:{name, arguments:""}}`
  2. Frame 2: `delta.tool_calls[0] = {index:0, function:{arguments: "<json-string>"}}` (split allows the SDK to accumulate)
  3. Terminal frame: `delta:{}, finish_reason:"tool_calls"` (already wired via `mapFinishReason` if we add tool_calls → "tool_calls" mapping).
- **Wire-type additions needed:** `chunkDelta` (line 55-58) currently has only `Role` and `Content`. Add `ToolCalls []chunkDeltaToolCall \`json:"tool_calls,omitempty"\``; define `chunkDeltaToolCall` + `chunkDeltaToolCallFunction` with `Index int`, `ID string \`json:",omitempty"\``, `Type string \`json:",omitempty"\``, `Function chunkDeltaToolCallFunction`.
- **Pitfall 2 (RESEARCH):** when splitting across multiple frames, do NOT split at a JSON escape boundary — frame 1 has `arguments:""`, frame 2 has the COMPLETE JSON string atom. Single-frame fallback also legal per CONTEXT Discretion §OpenAI streaming.
- **Stays the same:** the writeData → buildChunk → flusher.Flush() framing; the D-05 single-goroutine invariant.

---

### MOD `internal/adapter/anthropic/wire.go` lines 202-206/242 (close Phase 6 TODO)

**Closest analog (cross-adapter)** — `internal/adapter/ollama/wire.go:321-330` — same shape per RESEARCH line 487-489 (Anthropic `input_schema` ≅ Ollama/OpenAI `parameters`).

**Current TODO** (`internal/adapter/anthropic/wire.go:239-243`):
```go
// Phase 3.1 does NOT translate tools to canonical.ToolSpec — tool
// dispatch is Phase 6. Decoder accepts the shape so requests with
// tools[] do not 400.
// TODO(Phase 6): translate anthropicToolSpec → canonical.ToolSpec.
```

**Adaptation notes:**
- **Stays the same:** `anthropicToolSpec` wire type (line 137-141 — already correct with `Name`, `Description`, `InputSchema`).
- **Changes per D-14:**
  1. After the message walk, add a `for _, t := range w.Tools` loop populating `req.Tools = append(req.Tools, canonical.ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.InputSchema})`.
  2. Decode `w.ToolChoice` (currently `json.RawMessage` at line 66) into `canonical.ToolChoice`. Anthropic's shape: `{type:"auto"|"any"|"tool", name?}` — try a typed decode, mirror the polymorphic-decode pattern from `decodeMessageContent` in `internal/adapter/openai/wire.go:138-164`.

---

### MOD `internal/adapter/anthropic/sse.go` lines 204-220 (tool_use block state machine)

**Current state** (`internal/adapter/anthropic/sse.go:204-220`):
```go
func (e *sseEmitter) applyChunk(c canonical.Chunk) error {
	var header any
	switch c.Kind {
	case canonical.ChunkKindText:
		header = textBlockHeader{Type: "text", Text: ""}
	case canonical.ChunkKindThought:
		header = thinkingBlockHeader{Type: "thinking", Thinking: ""}
	default:
		// ChunkKindToolCall / ChunkKindPlan dormant in Phase 3.1 —
		// drop with debug log. NO state change.
		e.logger.Debug("anthropic: sse unsupported chunk kind dropped (Phase 3.1)", "kind", c.Kind)
		return nil
	}
	// Step 2: close + bump on kind transition. ... [load-bearing state machine]
}
```

**Closest pattern (same file)** — the existing text + thinking branches at lines 209-212 + 248-266:
```go
// Header (line 209-212):
case canonical.ChunkKindText:
	header = textBlockHeader{Type: "text", Text: ""}

// Delta (line 248-266):
case canonical.ChunkKindText:
	if c.Text == nil {
		return nil
	}
	return e.writeEvent("content_block_delta", contentBlockDelta{
		Type:  "content_block_delta",
		Index: e.blockIndex,
		Delta: textDelta{Type: "text_delta", Text: c.Text.Content},
	})
```

**CR-01 pointer-to-map fix reference** (`internal/adapter/anthropic/render.go:120-130`) — MUST be preserved:
```go
input := part.ToolUse.Input
if len(input) == 0 {
	input = map[string]any{}
}
out.Content = append(out.Content, contentBlock{
	Type:  "tool_use",
	ID:    part.ToolUse.ID,
	Name:  part.ToolUse.Name,
	Input: &input, // pointer indirection makes omitempty preserve the field
})
```

**Adaptation notes:**
- **Stays the same:** the 3-step state machine (identify header → close+bump on kind transition → open block → emit delta). The blockIndex discipline (Pitfall 7) — bump exactly once per kind transition.
- **Changes per D-07 Anthropic:**
  1. New header type: `toolUseBlockHeader{Type: "tool_use", ID, Name, Input: &map[string]any{}}` — note the pointer-to-empty-map per CR-01 (Pitfall 1).
  2. Add `case canonical.ChunkKindToolCall: header = toolUseBlockHeader{Type: "tool_use", ID: c.ToolCall.ID, Name: c.ToolCall.Name, Input: &emptyMap}` to the header switch (line 209-213).
  3. Add `case canonical.ChunkKindToolCall:` to the delta switch (line 248-266) — marshal `c.ToolCall.Args` to JSON string, emit ONE `content_block_delta` with `inputJSONDelta{Type: "input_json_delta", PartialJSON: string(argsJSON)}`.
  4. New payload type: `inputJSONDelta struct{ Type string \`json:"type"\`; PartialJSON string \`json:"partial_json"\` }`.
- **Pitfall 7 (block-index discipline):** the existing step-2 close-then-bump path at line 222-232 handles the kind transition correctly — DO NOT add a second bump in the tool_use branch.
- **No new state machinery** per CONTEXT specifics §Anthropic streaming tool_use block index — reuses the existing index discipline.

---

## Shared Patterns

### Pattern A: Canonical-typed engine helpers (D-11 invariant — no JSON tags)

**Source:** `internal/canonical/chat.go` (entire file, especially lines 60-241) + `internal/canonical/chunk.go` (entire file).
**Apply to:** `internal/engine/coerce.go` (new file) + `internal/canonical/chunk.go` extension (D-08).

**Pattern:**
- Canonical types have NO JSON tags (adapter-side translation only — TRST-04).
- Field-name doc comments explain wire-source provenance.
- Discriminated-union pattern: `Kind` enum + per-variant pointer fields, exactly one non-nil.

Example (`internal/canonical/chunk.go:47-53`):
```go
type ToolCallChunk struct {
	// Name is the tool name.
	Name string
	// Args is the tool arguments as a map.
	Args map[string]any
}
```

Phase 6 D-08 addition: add `ID string` with doc comment noting "populated from `toolCallId` on the ACP wire OR synthesized by `coerceToolCall` when source is text-not-kiro."

---

### Pattern B: Per-surface SSE/NDJSON emitter independence (Phase 4 D-08)

**Source:** Each of `internal/adapter/{ollama/ndjson.go, openai/sse.go, anthropic/sse.go}` — independent state machines, no shared driver.
**Apply to:** Phase 6 tool_call rendering changes to all three emitters.

**Pattern:**
- Each emitter owns its `applyChunk` method; no abstraction layer.
- `writeData` (OpenAI) / `writeEvent` (Anthropic) / `emitNDJSONChunk` (Ollama) is the SOLE method touching `w` / `flusher` (D-05 single-goroutine invariant).
- Unsupported chunk kinds short-circuit BEFORE any state mutation (no close, no bump, no log-as-open per anthropic/sse.go lines 205-220).
- Streaming headers (`Content-Type`, `Cache-Control`) set BEFORE `WriteHeader(200)` — order matters (Pitfall 2).

Phase 6 changes grow each emitter LOCALLY — no shared `streamDriver.go` abstraction.

---

### Pattern C: Adapter-over-canonical wire-decode (TRST-04)

**Source:** `internal/adapter/ollama/wire.go:321-330` — proven `wireType → canonical.ToolSpec` decode.
**Apply to:** `internal/adapter/openai/wire.go` (D-13) + `internal/adapter/anthropic/wire.go` (D-14).

**Pattern:**
```go
for _, t := range w.Tools {
	if t.Function == nil { // or: if t.Name == "" for Anthropic shape
		continue
	}
	req.Tools = append(req.Tools, canonical.ToolSpec{
		Name:        t.Function.Name,
		Description: t.Function.Description,
		Parameters:  t.Function.Parameters,
	})
}
```

- Per-spec walk, defensive nil/empty guards, no error return (translation layer cannot fail per ollama/wire.go:254-256 invariant).
- Direct field copy — `canonical.ToolSpec.Parameters` is `map[string]any`, matches all three surfaces' JSON-schema-shaped object map.

---

### Pattern D: Property tests + Example funcs (TRST-06 + TRST-07)

**Source:** `internal/engine/pickcwd_test.go` lines 204-260.
**Apply to:** `internal/engine/coerce_test.go` (D-12, D-20).

**Pattern:**
- `testing/quick.Config{MaxCount: 1000}` per property invariant.
- Separate funcs: `TestPickCwd_NeverPanics`, `TestPickCwd_Idempotent` (Phase 6 add), `TestPickCwd_RoundTrip`, `TestPickCwd_NoMatchNoMutation`.
- `defer func() { if r := recover(); r != nil { ... } }()` for nil-input panic guard.
- `Example_CoerceToolCall` runnable godoc (TRST-07 — `go test -run Example`).

---

### Pattern E: E2E real-binary harness (Phase 5 precedent)

**Source:** `tests/e2e/e2e_test.go::TestMain + bootGateway + gateOrSkip` (lines 57-225).
**Apply to:** `tests/e2e/tools_{ollama,openai,anthropic,cancel}_test.go` (D-17, D-18, D-19).

**Pattern:**
- `//go:build e2e` tag + `OTTO_E2E=1` env gate (dual-skip: gate-off vs kiro-missing).
- One shared `TestMain` builds the binary once into `t.TempDir()`-style temp.
- Per-test `bootGateway(t, extraEnv)` returns `(baseURL, cleanup)`; `defer cleanup()` mandatory.
- `gateOrSkip(t)` first line of every E2E test.
- Subtests share one warmup: `TestE2E_Tools_<Surface>` → `t.Run("Scenario_N", ...)`.
- Env overlay via `extraEnv` map (line 158-160) — Phase 6 swaps `KIRO_CMD` to a scripted fake binary for scenarios needing controllable ACP notifications (D-19).

---

### Pattern F: Anthropic CR-01 pointer-to-map fix (preservation gate)

**Source:** `internal/adapter/anthropic/render.go:113-131`.
**Apply to:** `internal/adapter/anthropic/sse.go` (D-07 Anthropic tool_use block header) — Pitfall 1.

**Pattern:**
```go
input := part.ToolUse.Input
if len(input) == 0 {
	input = map[string]any{}
}
// Pointer-to-map preserves "empty but present" through json.Marshal omitempty.
Input: &input,
```

- Wire spec requires `"input":{}` (empty OBJECT), not `"input":null`. Default Go encoding emits `null` for nil maps; the pointer-to-`map[string]any{}` round-trips correctly with omitempty.
- Phase 6 streaming `content_block_start` for tool_use blocks MUST use the same pattern (header `{type:"tool_use", id, name, input:{}}` — RESEARCH line 326-327 Anthropic spec line 1093).

---

## No Analog Found

Files with no close in-tree match (planner should rely on RESEARCH.md patterns + cross-repo references):

| File | Role | Reason |
|------|------|--------|
| `tests/e2e/tools_fixtures.go` (NEW non-test file) | shared test fixture | No prior `tests/e2e/*.go` non-`_test.go` file exists. Closest spirit (`internal/acp/fakeacp_test.go::fakeACPServer`) is in-package only. The pattern is novel for the project. **Use RESEARCH §"E2E test scaffold" line 455-481 + CONTEXT D-19 + the `bootGateway(t, extraEnv)` env-overlay seam (e2e_test.go:158-160) as the design source.** |
| `internal/engine/coerce.go` (`CoerceToolCall` + `pickBestTool` + `stripFences`) | engine helper (Node-derived algorithm) | The Node source `acp-ollama-server.js` is NOT in this checkout (RESEARCH Assumption A1). The algorithm derives from `docs/reference/acp_server_node_reference.md` §"Load-bearing weirdness" lines 166-195 + CONTEXT D-09's 9-step order-of-checks. **Plan-phase must add a `checkpoint:human-verify` task per RESEARCH line 547 to fetch the Node source for byte-level fidelity before sign-off.** |

## Metadata

**Analog search scope:** `internal/canonical/`, `internal/acp/`, `internal/engine/`, `internal/adapter/{ollama,openai,anthropic}/`, `tests/e2e/`.
**Files scanned:** 14 in-tree analog sources read.
**Pattern extraction date:** 2026-05-26
**Key insight:** Every Phase 6 file extends an existing in-tree pattern (no greenfield infrastructure). The single novel surface is the cross-test fixture file (`tools_fixtures.go`) and the algorithm-as-spec `coerce.go`. Both are bounded by canonical/wire-shape contracts already validated in Phase 2/3/3.1/4.
