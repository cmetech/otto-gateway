# Deferred Tool Dispatcher Coercion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recognize a whole-response textual wrapper for a deferred tool and surface it as a structured call to the client-declared Hermes `tool_call` dispatcher, without changing native tool calls, directly declared tool calls, prose, privacy hooks, compression hooks, or ambiguous JSON behavior.

**Architecture:** Extend only the shared explicit-wrapper extractor in `internal/engine/coerce.go`. When a whole response is exactly a `{"tool_call":{"name":...,"arguments":...}}` envelope whose inner name is not directly declared, and the request declares the exact Hermes dispatcher schema, synthesize a structured call to the outer `tool_call` function with the original inner name and arguments. Preserve the existing direct-name and key-overlap paths, and never apply dispatcher nesting to wrappers merely embedded in prose.

**Tech Stack:** Go 1.26.5, canonical gateway chat types, OpenAI SSE/JSON renderers, Ollama NDJSON/JSON renderers, Anthropic Messages/SSE renderers, standard `encoding/json`, table-driven tests, `go test`, `go test -race`, `go vet`, gofumpt, go-arch-lint.

## Global Constraints

- Production scope is limited to `internal/engine/coerce.go`; adapter production files, engine hook orchestration, privacy code, compression code, configuration, and wire decoders must not change.
- The dispatcher name is exactly `tool_call`. Do not add generic meta-tool guessing or configurable dispatcher names in this patch.
- Dispatcher recovery is allowed only for Strategy 1 candidates: a raw whole-response JSON object/array or a single JSON fence wrapping the entire response.
- Strategy 2 prose extraction keeps its current behavior. A directly declared wrapper embedded in prose may still be surfaced, but an unknown inner tool must not become executable merely because `tool_call` is declared.
- A directly declared inner tool always wins and remains a direct call. Do not wrap it in `tool_call`.
- An already nested call whose wrapper name is `tool_call` remains one call. Do not double-wrap it.
- Native `ChunkKindToolCall` handling remains authoritative and continues to bypass textual coercion through the existing idempotency/native-call guards.
- If dispatcher recognition, wrapper validation, or JSON decoding is uncertain, fail open as assistant text. Do not manufacture a call.
- Do not validate the hidden tool's argument schema in the Gateway. The Gateway preserves the inner name/arguments; Hermes owns deferred-tool lookup, scope checks, policy, hooks, approvals, and execution.
- Preserve `CoerceToolCall`'s current no-tools guard, pre-existing-call guard, bare-JSON key-overlap fallback, first-declared tie-break, ID format, bounded streaming buffers, and never-panic behavior.
- Preserve hook ordering exactly. OpenAI/Ollama non-streaming post-hooks continue to run during `Engine.Collect` before adapter coercion; Anthropic non-streaming explicit-wrapper extraction remains inside its adapter-local collector before its post-hooks; streaming coercion continues to build the canonical response that handlers pass to `RunPostHooks` after emission. Do not move coercion into the hook chain.
- Preserve Anthropic's anti-forgery rule: Anthropic may use the explicit wrapper extractor, but must never invoke the ambiguous bare-JSON `CoerceToolCall` path.
- No dependency additions, schema extensions, feature flags, environment variables, version bumps, tags, pushes, or releases are part of this plan.
- Format with `make fmt`; all final verification commands in Task 4 must pass.

## Confirmed Failure Mechanism

The failing Gateway route receives this assistant text from the upstream model:

```json
{"tool_call":{"name":"gitlab_list_group_projects","arguments":{"group":"sd-macs-att-rnam-hosting","recursive":true,"max_groups":50,"max_projects":100}}}
```

Hermes declared only its deferred bridge tools, including:

```json
{
  "name": "tool_call",
  "parameters": {
    "type": "object",
    "properties": {
      "name": {"type": "string"},
      "arguments": {"type": "object"}
    },
    "required": ["name", "arguments"]
  }
}
```

`ExtractToolCallWrappers` currently sees that `gitlab_list_group_projects` is not directly declared, then calls `pickBestTool` with the inner keys `group`, `recursive`, `max_groups`, and `max_projects`. None overlap the outer dispatcher's `name` and `arguments` properties, so the score is zero and the wrapper is returned as text. Direct Hermes-to-Codex succeeds because the provider returns a native structured function call and bypasses this textual compatibility path.

The corrected canonical call is:

```json
{
  "name": "tool_call",
  "arguments": {
    "name": "gitlab_list_group_projects",
    "arguments": {
      "group": "sd-macs-att-rnam-hosting",
      "recursive": true,
      "max_groups": 50,
      "max_projects": 100
    }
  }
}
```

## File Map

- Modify `internal/engine/coerce.go`: recognize the exact dispatcher schema, track whole-content versus prose wrapper provenance, and synthesize the nested dispatcher call.
- Modify `internal/engine/coerce_test.go`: lock dispatcher-schema recognition, exact nesting, fail-open behavior, direct-call precedence, no double-wrapping, prose safety, array ordering, idempotency, and argument preservation.
- Modify `internal/adapter/openai/integration_test.go`: prove the non-streaming OpenAI HTTP response contains one outer `tool_call` with the exact nested arguments.
- Modify `internal/adapter/openai/sse_golden_test.go`: prove fragmented fenced output becomes one streamed outer call and does not leak wrapper text; preserve native-call and ordinary-text behavior.
- Modify `internal/adapter/ollama/handlers_test.go`: prove non-streaming Ollama renders the outer dispatcher call with object arguments.
- Modify `internal/adapter/ollama/ndjson_test.go`: prove streaming Ollama renders the same nested call and keeps ordinary prose non-executable.
- Modify `internal/adapter/anthropic/handlers_test.go`: prove the shared explicit-wrapper path renders the outer dispatcher as one `tool_use` block while bare JSON remains text.
- Modify `internal/adapter/anthropic/sse_coerce_test.go`: prove the shared streaming wrapper path renders the outer dispatcher as one `tool_use` sequence while prose-embedded hidden wrappers remain text.

---

### Task 1: Implement exact whole-response dispatcher recovery with TDD

**Files:**
- Modify: `internal/engine/coerce.go`
- Modify: `internal/engine/coerce_test.go`

**Interfaces:**
- Consumes: `canonical.ToolSpec`, the existing `ExtractToolCallWrappers` Strategy 1/Strategy 2 split, and `CoerceToolCall(req, resp) bool`.
- Produces: test helper `toolCallDispatcher() canonical.ToolSpec`, unexported `findToolCallDispatcher(tools []canonical.ToolSpec) *canonical.ToolSpec`, unexported schema helpers, and provenance-aware `pushWrapper(m, allowDispatcher)` behavior. The exported `ExtractToolCallWrappers` signature remains unchanged.

- [ ] **Step 1: Add the exact Hermes dispatcher fixture**

Add this helper next to `weatherTool()` and `fileTool()`:

```go
func toolCallDispatcher() canonical.ToolSpec {
	return canonical.ToolSpec{
		Name:        "tool_call",
		Description: "Invoke a deferred tool by name",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string"},
				"arguments": map[string]any{"type": "object"},
			},
			"required": []any{"name", "arguments"},
		},
	}
}
```

- [ ] **Step 2: Add a focused extraction matrix**

Add `TestExtractToolCallWrappers_DeferredDispatcher` with these exact cases:

```go
func TestExtractToolCallWrappers_DeferredDispatcher(t *testing.T) {
	hidden := `{"tool_call":{"name":"gitlab_list_group_projects","arguments":{"group":"sd-macs-att-rnam-hosting","recursive":true,"max_groups":50,"max_projects":100}}}`
	fenced := "```json\n" + hidden + "\n```"
	prose := "Example only: " + hidden
	alreadyNested := `{"tool_call":{"name":"tool_call","arguments":{"name":"gitlab_list_group_projects","arguments":{"group":"sd-macs-att-rnam-hosting"}}}}`

	tests := []struct {
		name      string
		text      string
		tools     []canonical.ToolSpec
		wantCount int
		wantName  string
	}{
		{name: "whole object nests under dispatcher", text: hidden, tools: []canonical.ToolSpec{toolCallDispatcher()}, wantCount: 1, wantName: "tool_call"},
		{name: "whole fence nests under dispatcher", text: fenced, tools: []canonical.ToolSpec{toolCallDispatcher()}, wantCount: 1, wantName: "tool_call"},
		{name: "unknown without dispatcher fails open", text: hidden, tools: []canonical.ToolSpec{weatherTool()}, wantCount: 0},
		{name: "prose hidden wrapper does not dispatch", text: prose, tools: []canonical.ToolSpec{toolCallDispatcher()}, wantCount: 0},
		{name: "already nested is not double wrapped", text: alreadyNested, tools: []canonical.ToolSpec{toolCallDispatcher()}, wantCount: 1, wantName: "tool_call"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractToolCallWrappers(tc.text, tc.tools)
			if len(got) != tc.wantCount {
				t.Fatalf("call count: got %d, want %d; calls=%+v", len(got), tc.wantCount, got)
			}
			if tc.wantCount > 0 && got[0].Name != tc.wantName {
				t.Fatalf("call name: got %q, want %q", got[0].Name, tc.wantName)
			}
		})
	}
}
```

After the table, add exact assertions for the first result:

```go
got := ExtractToolCallWrappers(hidden, []canonical.ToolSpec{toolCallDispatcher()})
if got[0].Arguments["name"] != "gitlab_list_group_projects" {
	t.Fatalf("nested name: got %v", got[0].Arguments["name"])
}
inner, ok := got[0].Arguments["arguments"].(map[string]any)
if !ok {
	t.Fatalf("nested arguments type: %T", got[0].Arguments["arguments"])
}
if inner["group"] != "sd-macs-att-rnam-hosting" || inner["recursive"] != true || inner["max_groups"] != float64(50) || inner["max_projects"] != float64(100) {
	t.Fatalf("nested arguments changed: %+v", inner)
}
```

- [ ] **Step 3: Add direct-call precedence and whole-array coverage**

Add subtests proving that a directly declared inner tool is never nested, and that a whole array of hidden wrappers becomes multiple ordered dispatcher calls:

```go
t.Run("direct declaration wins over dispatcher", func(t *testing.T) {
	text := `{"tool_call":{"name":"get_weather","arguments":{"location":"Paris"}}}`
	got := ExtractToolCallWrappers(text, []canonical.ToolSpec{toolCallDispatcher(), weatherTool()})
	if len(got) != 1 || got[0].Name != "get_weather" {
		t.Fatalf("direct call was nested or lost: %+v", got)
	}
})

t.Run("dispatcher wins over unrelated overlap remap", func(t *testing.T) {
	overlap := canonical.ToolSpec{Name: "group_lookup", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"group": map[string]any{"type": "string"}},
	}}
	text := `{"tool_call":{"name":"gitlab_list_group_projects","arguments":{"group":"sd-macs-att-rnam-hosting"}}}`
	got := ExtractToolCallWrappers(text, []canonical.ToolSpec{overlap, toolCallDispatcher()})
	if len(got) != 1 || got[0].Name != "tool_call" || got[0].Arguments["name"] != "gitlab_list_group_projects" {
		t.Fatalf("explicit deferred intent was overlap-remapped: %+v", got)
	}
})

t.Run("whole array preserves hidden call order", func(t *testing.T) {
	text := `[{"tool_call":{"name":"first_hidden","arguments":{"value":1}}},{"tool_call":{"name":"second_hidden","arguments":{"value":2}}}]`
	got := ExtractToolCallWrappers(text, []canonical.ToolSpec{toolCallDispatcher()})
	if len(got) != 2 || got[0].Name != "tool_call" || got[1].Name != "tool_call" {
		t.Fatalf("dispatcher calls: %+v", got)
	}
	if got[0].Arguments["name"] != "first_hidden" || got[1].Arguments["name"] != "second_hidden" {
		t.Fatalf("hidden call order changed: %+v", got)
	}
})
```

- [ ] **Step 4: Add malformed/lookalike dispatcher rejection**

Add a table where the hidden wrapper must produce zero calls for each tool spec:

```go
lookalikes := []canonical.ToolSpec{
	{Name: "dispatch_tool", Parameters: toolCallDispatcher().Parameters},
	{Name: "tool_call", Parameters: map[string]any{"type": "object"}},
	{Name: "tool_call", Parameters: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}}, "required": []any{"name"}}},
	{Name: "tool_call", Parameters: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "object"}, "arguments": map[string]any{"type": "object"}}, "required": []any{"name", "arguments"}}},
	{Name: "tool_call", Parameters: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "string"}}, "required": []any{"name", "arguments"}}},
}
for i, tool := range lookalikes {
	if got := ExtractToolCallWrappers(hidden, []canonical.ToolSpec{tool}); len(got) != 0 {
		t.Fatalf("lookalike %d became executable: %+v", i, got)
	}
}
```

Also exercise `required: []string{"name", "arguments"}` as a positive schema representation because tests and internal callers may construct canonical schemas without JSON decoding.

- [ ] **Step 5: Run the new tests and verify the intended failures**

Run:

```bash
go test ./internal/engine -run 'TestExtractToolCallWrappers_DeferredDispatcher' -count=1 -v
```

Expected before implementation: the whole-object and whole-fence cases fail with zero calls. Existing direct, malformed, and prose safety cases should continue to pass.

- [ ] **Step 6: Add exact dispatcher-schema recognition**

Implement helpers near `extractProperties`:

```go
const toolCallDispatcherName = "tool_call"

func findToolCallDispatcher(tools []canonical.ToolSpec) *canonical.ToolSpec {
	for i := range tools {
		if tools[i].Name == toolCallDispatcherName && isToolCallDispatcherSchema(tools[i].Parameters) {
			return &tools[i]
		}
	}
	return nil
}

func isToolCallDispatcherSchema(parameters map[string]any) bool {
	if parameters == nil {
		return false
	}
	schemaType, ok := parameters["type"].(string)
	if !ok || schemaType != "object" {
		return false
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		return false
	}
	nameSchema, nameOK := properties["name"].(map[string]any)
	argumentsSchema, argumentsOK := properties["arguments"].(map[string]any)
	if !nameOK || !argumentsOK {
		return false
	}
	nameType, nameTypeOK := nameSchema["type"].(string)
	argumentsType, argumentsTypeOK := argumentsSchema["type"].(string)
	if !nameTypeOK || !argumentsTypeOK || nameType != "string" || argumentsType != "object" {
		return false
	}
	return schemaRequires(parameters["required"], "name") && schemaRequires(parameters["required"], "arguments")
}

func schemaRequires(raw any, field string) bool {
	switch required := raw.(type) {
	case []any:
		for _, value := range required {
			if stringValue, ok := value.(string); ok && stringValue == field {
				return true
			}
		}
	case []string:
		for _, value := range required {
			if value == field {
				return true
			}
		}
	}
	return false
}
```

Do not accept a dispatcher based only on its name. The schema check is the capability declaration that authorizes this compatibility rewrite.

- [ ] **Step 7: Preserve argument validity information**

Refactor the local wrapper argument parsing so it retains whether `arguments` was present and decoded as an object:

```go
parseWrapperArguments := func(tc map[string]any) (map[string]any, bool) {
	raw, present := tc["arguments"]
	if !present {
		return map[string]any{}, false
	}
	switch value := raw.(type) {
	case map[string]any:
		return value, true
	case string:
		var parsed map[string]any
		if value == "" || json.Unmarshal([]byte(value), &parsed) != nil || parsed == nil {
			return map[string]any{}, false
		}
		return parsed, true
	default:
		return map[string]any{}, false
	}
}
```

For existing directly declared/remapped wrappers, preserve the current behavior of normalizing invalid/missing arguments to an empty map. Use the returned boolean only to prevent the new dispatcher branch from accepting malformed envelopes.

- [ ] **Step 8: Add an exact-envelope predicate for the new branch**

Implement:

```go
func isExactToolCallEnvelope(outer, inner map[string]any) bool {
	if len(outer) != 1 || len(inner) != 2 {
		return false
	}
	_, hasName := inner["name"]
	_, hasArguments := inner["arguments"]
	return hasName && hasArguments
}
```

This strictness applies only to unknown-name dispatcher recovery. It must not narrow existing directly declared wrapper support.

- [ ] **Step 9: Make wrapper provenance explicit inside the extractor**

Change the local closure signature from:

```go
pushWrapper := func(m map[string]any) (canonical.ToolCall, bool)
```

to:

```go
pushWrapper := func(m map[string]any, allowDispatcher bool) (canonical.ToolCall, bool)
```

Call it with `allowDispatcher=true` only for Strategy 1 whole-content candidates, including each element of a whole-content array. Call it with `allowDispatcher=false` for every Strategy 2 object extracted from surrounding prose.

- [ ] **Step 10: Add direct → dispatcher → overlap precedence**

Inside `pushWrapper`, preserve the original inner name before any remap and use this exact order:

```go
declared := toolDeclared(name, tools)
if !declared && allowDispatcher && argsValid && isExactToolCallEnvelope(m, tc) {
	if dispatcher := findToolCallDispatcher(tools); dispatcher != nil {
		args = map[string]any{
			"name":      name,
			"arguments": args,
		}
		name = dispatcher.Name
		declared = true
	}
}
if !declared {
	best, score := pickBestTool(args, tools)
	if best == nil || score == 0 {
		return canonical.ToolCall{}, false
	}
	name = best.Name
}
```

Why dispatcher precedes key-overlap remapping: the wrapper explicitly named a hidden tool, and the declared `tool_call` schema explicitly authorizes deferred dispatch. Guessing a different visible tool from coincidental inner argument keys would discard that intent.

- [ ] **Step 11: Update comments to document the trust boundary**

Update `ExtractToolCallWrappers` and `CoerceToolCall` comments to state:

- Direct declared wrappers preserve current raw/fenced/prose behavior.
- Unknown whole-response wrappers may be nested only under the exact declared `tool_call` dispatcher.
- Unknown prose-embedded wrappers do not use dispatcher recovery.
- Hidden-tool validation and authorization occur in Hermes after the Gateway returns the outer structured call.

- [ ] **Step 12: Run engine tests**

```bash
make fmt
go test ./internal/engine -run 'TestExtractToolCallWrappers|TestCoerceToolCall' -count=1 -v
go test -race ./internal/engine -count=1
```

Expected: all new tests pass; existing wrapper-in-prose, invented-name remap, bare-JSON, idempotency, zero-match no-mutation, tie-break, truncation repair, scan-budget, and never-panic tests remain green.

- [ ] **Step 13: Commit the engine tests and implementation**

```bash
git add internal/engine/coerce.go internal/engine/coerce_test.go
git commit -m "fix(engine): route deferred wrappers through tool_call dispatcher"
```

---

### Task 2: Prove all wire surfaces and non-tool flows remain stable

**Files:**
- Modify: `internal/adapter/openai/integration_test.go`
- Modify: `internal/adapter/openai/sse_golden_test.go`
- Modify: `internal/adapter/ollama/handlers_test.go`
- Modify: `internal/adapter/ollama/ndjson_test.go`
- Modify: `internal/adapter/anthropic/handlers_test.go`
- Modify: `internal/adapter/anthropic/sse_coerce_test.go`

**Interfaces:**
- Consumes: the unchanged adapter production paths and the Task 1 behavior of `ExtractToolCallWrappers`.
- Produces: end-to-end wire-shape regression coverage for the shared change. No production adapter changes are expected or authorized.

- [ ] **Step 1: Add the OpenAI non-streaming control**

Clone the structure of `TestIntegration_FakeEngine_NonStream_ToolCallWrapperCoerce`, but use the exact hidden GitLab wrapper and declare only `tool_call`. Name it `TestIntegration_FakeEngine_NonStream_DeferredWrapperUsesDispatcher`.

The request body must declare:

```json
{"type":"function","function":{"name":"tool_call","parameters":{"type":"object","properties":{"name":{"type":"string"},"arguments":{"type":"object"}},"required":["name","arguments"]}}}
```

Decode `choices[0].message.tool_calls[0].function.arguments` and assert:

```go
if tc.Function.Name != "tool_call" {
	t.Fatalf("outer function name: got %q, want tool_call", tc.Function.Name)
}
var outer struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}
if err := json.Unmarshal([]byte(tc.Function.Arguments), &outer); err != nil {
	t.Fatalf("decode outer dispatcher arguments: %v", err)
}
if outer.Name != "gitlab_list_group_projects" || outer.Arguments["group"] != "sd-macs-att-rnam-hosting" {
	t.Fatalf("nested call changed: %+v", outer)
}
if completion.Choices[0].FinishReason != "tool_calls" || completion.Choices[0].Message.Content != "" {
	t.Fatalf("wire completion did not become a pure tool call: %+v", completion.Choices[0])
}
```

- [ ] **Step 2: Add the fragmented OpenAI SSE control**

Add `TestStream_DeferredWrapperUsesDispatcher` beside the explicit-wrapper SSE tests. Feed the fence and JSON in multiple text chunks using `driveGoldenWithReq`, declare only this adapter-local canonical spec, and assert:

```go
dispatcher := canonical.ToolSpec{Name: "tool_call", Parameters: map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name":      map[string]any{"type": "string"},
		"arguments": map[string]any{"type": "object"},
	},
	"required": []any{"name", "arguments"},
}}
req := &canonical.ChatRequest{Tools: []canonical.ToolSpec{dispatcher}}
```

- exactly one `"type":"function"` frame sequence;
- streamed function name is `tool_call`;
- concatenated streamed arguments decode to outer `{name, arguments}`;
- the inner name is `gitlab_list_group_projects`;
- all four GitLab arguments are preserved;
- terminal finish reason is `tool_calls`;
- neither `"tool_call"` wrapper JSON nor the markdown fence appears as visible assistant content.

Add a negative `TestStream_ProseEmbeddedHiddenWrapperDoesNotUseDispatcher` using:

```go
text := `For documentation: {"tool_call":{"name":"gitlab_list_group_projects","arguments":{"group":"sd-macs-att-rnam-hosting"}}}`
```

Assert zero function frames, `finish_reason:"stop"`, and byte-preserved visible text.

- [ ] **Step 3: Add Ollama non-streaming and NDJSON controls**

Mirror `TestHandleChat_NonStreaming_ToolCallWrapperCoerce` and `TestStream_ProseThenFencedWrapper_SurfacesToolCall`, but declare only the exact dispatcher schema.

For non-streaming, assert the Ollama object form:

```go
call := resp.Message.ToolCalls[0]
if call.Function.Name != "tool_call" {
	t.Fatalf("outer name: got %q", call.Function.Name)
}
if call.Function.Arguments["name"] != "gitlab_list_group_projects" {
	t.Fatalf("inner name: got %v", call.Function.Arguments["name"])
}
inner, ok := call.Function.Arguments["arguments"].(map[string]any)
if !ok || inner["group"] != "sd-macs-att-rnam-hosting" {
	t.Fatalf("inner arguments: %#v", call.Function.Arguments["arguments"])
}
```

For streaming, assert one `message.tool_calls` entry on the terminal NDJSON line, no raw wrapper leakage, and the same nested object. Add the same prose-embedded-hidden-wrapper negative assertion used for OpenAI.

- [ ] **Step 4: Add Anthropic explicit-wrapper controls**

Mirror `TestAnthropic_CoercesToolCallWrapper` and `TestSSE_CoercesToolCallWrapper_EmitsToolUseFrames`, using the exact dispatcher schema and GitLab wrapper.

For non-streaming, assert exactly one `tool_use` block:

```go
if toolUse.Name != "tool_call" {
	t.Fatalf("outer tool_use name: got %q", toolUse.Name)
}
if (*toolUse.Input)["name"] != "gitlab_list_group_projects" {
	t.Fatalf("inner name: got %v", (*toolUse.Input)["name"])
}
inner, ok := (*toolUse.Input)["arguments"].(map[string]any)
if !ok || inner["group"] != "sd-macs-att-rnam-hosting" {
	t.Fatalf("inner arguments: %#v", (*toolUse.Input)["arguments"])
}
```

For SSE, concatenate `input_json_delta.partial_json`, decode it as the outer dispatcher arguments, and make the same assertions. Preserve `stop_reason:"tool_use"` and the current event sequence.

Add `TestSSE_ProseEmbeddedHiddenWrapperDoesNotUseDispatcher` and its non-streaming equivalent. Both must retain text and emit no `tool_use`.

- [ ] **Step 5: Re-run native and directly declared controls explicitly**

Run these existing tests in addition to the new tests:

```bash
go test ./internal/adapter/openai -run 'DeferredWrapper|NativeToolCallPlusDuplicateWrapper|MultipleExplicitWrappers|InvalidExplicitWrappers|ProseThenUnrelatedJSON' -count=1 -v
go test ./internal/adapter/ollama -run 'DeferredWrapper|NativeToolCallPlusDuplicateWrapper|MultipleExplicitWrappers|ProseThenUnrelatedJSON' -count=1 -v
go test ./internal/adapter/anthropic -run 'DeferredWrapper|DoesNotCallCoerceToolCall|BareJSON|MultipleExplicitWrappers|ResultError' -count=1 -v
```

Expected: nested deferred wrappers succeed; native calls remain single; direct wrappers remain direct; malformed/unknown/prose examples remain text; Anthropic bare JSON remains non-executable.

- [ ] **Step 6: Run adapter race tests**

```bash
go test -race ./internal/adapter/openai ./internal/adapter/ollama ./internal/adapter/anthropic -count=1
```

- [ ] **Step 7: Commit the wire regressions**

```bash
git add internal/adapter/openai/integration_test.go internal/adapter/openai/sse_golden_test.go internal/adapter/ollama/handlers_test.go internal/adapter/ollama/ndjson_test.go internal/adapter/anthropic/handlers_test.go internal/adapter/anthropic/sse_coerce_test.go
git commit -m "test(adapters): cover deferred dispatcher wire shapes"
```

---

### Task 3: Verify hook isolation and full compatibility

**Files:**
- Test only: `internal/plugin/compress/`
- Test only: `internal/privacy/`
- Test only: `internal/plugin/pii/`

**Interfaces:**
- Consumes: the completed engine and adapter changes.
- Produces: verification evidence that request hooks and non-tool behavior did not regress.

- [ ] **Step 1: Confirm the production diff is isolated**

Run:

```bash
git diff --name-only HEAD~2
git diff -- internal/engine/engine.go internal/engine/collect.go internal/privacy internal/plugin/pii internal/plugin/compress internal/adapter/openai internal/adapter/ollama internal/adapter/anthropic
```

Expected:

- `internal/engine/coerce.go` is the only modified production `.go` file.
- Adapter changes are tests only.
- `engine.go`, `collect.go`, privacy, PII, and compression production code have no diff.

If another production file changed, stop and remove that change unless a failing test proves the shared extractor cannot solve the case by itself.

- [ ] **Step 2: Run privacy and compression suites unchanged**

```bash
go test ./internal/privacy ./internal/plugin/pii ./internal/plugin/compress -count=1
go test -race ./internal/privacy ./internal/plugin/pii ./internal/plugin/compress -count=1
```

These tests prove the hook implementations still transform request/response strings, tool-call argument leaves, strict output, encrypted tokens, and compression candidates as before. The patch must not alter hook registration or ordering.

- [ ] **Step 3: Run core behavior and property tests**

```bash
go test ./internal/engine -count=1
go test -race ./internal/engine -count=1
```

Confirm the following existing properties remain green:

- `CoerceToolCall` never panics.
- A second coercion pass is a no-op.
- Zero-score input does not mutate text or tool calls.
- Existing tool-name overlap remains deterministic.
- Streaming buffers remain bounded.
- Existing native calls remain authoritative.

- [ ] **Step 4: Run the repository quality gates**

```bash
make fmt
make fmt-check
go vet ./...
go build ./...
CGO_ENABLED=0 go build ./cmd/otto-gateway
go test ./...
go test -race ./internal/engine ./internal/adapter/openai ./internal/adapter/ollama ./internal/adapter/anthropic ./internal/privacy ./internal/plugin/pii ./internal/plugin/compress
make arch-lint
git diff --check
```

If `make arch-lint` reports that `go-arch-lint` is not installed, install the repository-pinned version shown in the Makefile/CI workflow and rerun the command; do not mark the gate passed based on absence of the binary.

- [ ] **Step 5: Review the final diff against the safety matrix**

The final review must explicitly record PASS for every row:

| Input path | Expected result |
|---|---|
| Native structured call | Unchanged native call |
| Directly declared explicit wrapper | Same direct call, no nesting |
| Already nested `tool_call` wrapper | One outer call, no double-wrap |
| Whole/fenced unknown wrapper + exact dispatcher | Outer `tool_call` with preserved inner name/arguments |
| Whole array of unknown wrappers + exact dispatcher | Ordered outer dispatcher calls |
| Unknown wrapper without dispatcher | Assistant text |
| Unknown wrapper + malformed/lookalike dispatcher | Assistant text |
| Unknown wrapper embedded in prose + only the dispatcher | Assistant text |
| Bare argument JSON | Existing key-overlap behavior |
| Plain prose or ordinary JSON | Assistant text |
| Native call plus textual duplicate | Exactly one native call |
| Tool-enabled oversized stream | Existing bounded fail-open behavior |
| Anthropic bare JSON | Assistant text, never `tool_use` |
| Privacy/compression enabled | Existing hook order and results |

## Gotchas and Explicit Non-Goals

1. **Do not route every unknown wrapper through `tool_call`.** The new branch must require the exact declared dispatcher schema. Name-only recognition would turn an unrelated client function named `tool_call` into a privileged compatibility bridge.
2. **Do not enable dispatcher recovery inside Strategy 2 prose scanning.** Existing code deliberately finds directly declared wrappers in prose. Extending that behavior to hidden names would make documentation examples executable.
3. **Do not let key-overlap remapping run before dispatcher recovery for an exact whole hidden wrapper.** The inner arguments describe the hidden tool, not the outer dispatcher. Overlap guessing can select an unrelated visible function.
4. **Do not double-wrap.** When the wrapper already names the declared `tool_call`, the existing direct-declaration branch must return it unchanged.
5. **Do not expose hidden schemas to the Gateway.** Dynamic schema injection would change request prefixes, caching, tool-selection behavior, and security ownership. Hermes remains the sole deferred-tool registry.
6. **Do not execute hidden tools in the Gateway.** The Gateway only emits a structured outer call. Hermes must still perform scoped deferred-name validation, approvals, policy hooks, and the actual connector call.
7. **Do not reorder hooks.** Compression is a request `PreHook`; privacy includes request and response processing; trace/logging are post-hooks. This patch operates at the existing coercion seam and must not move it across `RunPreHooks`, `Collect`, or `RunPostHooks`.
8. **Privacy ordering differs by existing surface path.** OpenAI/Ollama non-streaming `Collect` post-hooks see the textual wrapper before adapter coercion. Anthropic's adapter-local non-streaming collector performs explicit-wrapper extraction before its post-hooks. Streaming post-hooks receive the synthesized canonical tool call after the emitter finalizes. Preserve all three existing behaviors; normalizing them is a separate architectural change.
9. **Anthropic shares `ExtractToolCallWrappers`.** Although Anthropic does not call the ambiguous `CoerceToolCall`, this exact-wrapper change will intentionally allow the same outer dispatcher call on Anthropic's explicit-wrapper path. Its bare-JSON anti-forgery tests must remain green.
10. **String-valued arguments need conservative handling.** Existing direct wrappers accept JSON strings for `arguments`. Dispatcher recovery may accept them only when they decode to a JSON object; malformed strings must not authorize a nested call.
11. **Arrays are whole-response candidates.** Preserve current multi-call order and unique IDs. Do not partially convert prose or mixed outer JSON objects under the new dispatcher rule.
12. **Tool choice behavior is out of scope.** This patch preserves the Gateway's existing handling of `tool_choice`; do not introduce new `none`, forced-tool, or required-tool semantics while fixing deferred dispatch.
13. **Streaming bounds are already load-bearing.** Do not increase `maxStreamingCoerceBytes`, `maxScanBytes`, candidate limits, nesting limits, or repair limits to make the fixture pass; the observed wrapper is well below every bound.
14. **No new telemetry payloads.** Existing debug logs may show the outer tool name `tool_call`. Do not log inner arguments or hidden tool payloads. A future metric distinguishing `direct`, `overlap`, and `dispatcher` coercion can be designed separately.

## Fresh-Session Execution Prompt

Use this prompt from the `otto-gateway` repository root:

```text
Implement docs/superpowers/plans/2026-08-13-deferred-tool-dispatcher-coercion.md task by task. Use superpowers:executing-plans (or superpowers:subagent-driven-development if multi-agent execution is enabled). Follow TDD, preserve the plan's production-file boundary, run every verification gate, and stop for review if any production file other than internal/engine/coerce.go appears necessary.
```
