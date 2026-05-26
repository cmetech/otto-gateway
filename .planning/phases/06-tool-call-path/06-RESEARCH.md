# Phase 6: Tool-Call Path - Research

**Researched:** 2026-05-26
**Domain:** Cross-surface tool-call rendering + LangChain-compat JSON coercion (Go 1.23, stdlib + chi)
**Confidence:** HIGH (algorithm + wire shapes verified against official specs and existing Phase 2/3/3.1/4 code)

## Summary

Phase 6 activates dormant canonical seams (`ToolSpec`, `ToolCall`, `ToolUsePart`, `ToolCallChunk`) and wires three load-bearing paths: (1) kiro-native `tool_call` ACP notifications → `canonical.ToolCallChunk` → per-surface rendering, (2) `coerceToolCall` fallback for LangChain agents emitting JSON-as-text → synthetic `tool_calls[]` entry, (3) per-surface tool-spec normalization (OpenAI `tools[].function` and Anthropic `tools[].input_schema` → `canonical.ToolSpec`). Net code is ~6 new/modified files in `internal/` plus a 4-file E2E test matrix. **Zero new dependencies** — every required library (`testing/quick`, `go.uber.org/goleak`, `encoding/json`) is already imported.

Wire-shape divergence is THE Phase 6 test axis: OpenAI streams `tool_calls[]` across multiple `delta.tool_calls[]` frames (id+name on frame 1, arguments incrementally) and emits `finish_reason: "tool_calls"` on the terminal chunk; Anthropic emits `content_block_start{type:"tool_use",input:{}}` → one or more `input_json_delta` events with `partial_json` strings (which concatenate to a complete JSON object) → `content_block_stop`; Ollama emits `message.tool_calls[]` atomically on the final `done:true` NDJSON line. CONTEXT D-07's "single combined frame / single delta" choice is **spec-compliant per Anthropic** (a single `partial_json` carrying the entire serialized args is explicitly permitted) and **safe per OpenAI** (the SDK parser accumulates whatever shape arrives — but the multi-frame form with id+name first is the canonical wire shape we should mirror for parser compatibility).

The `coerceToolCall` algorithm (D-09) translates directly from the Node reference's narrative spec; the Node source itself is not in this checkout (parent directory `gitlab.rosetta.ericssondevops.com/loop_24/acp_server` does not exist on this machine), but `docs/reference/acp_server_node_reference.md` lines 184-196 and `docs/briefs/go_port_brief.md` §1.4 lines 133-138 + 179-181 are the verified canonical references. The 9-step order-of-checks in CONTEXT D-09 is internally consistent with both narrative descriptions and the "commit log receipts" (`0ead935`, `7569745`, `995c569`) cited in the reference.

**Primary recommendation:** Implement `internal/engine/coerce.go` as a small, pure, side-effect-free function with the 9-step order-of-checks from D-09 (verified against the narrative reference); call from Ollama and OpenAI handlers immediately after `engine.Collect`; skip on Anthropic (D-01). Follow the multi-frame OpenAI streaming pattern (id+name in frame 1, arguments in frame 2) for SDK parser robustness — CONTEXT explicitly allows either, and the multi-frame form is what OpenAI's own examples show. Use `testing/quick` not `pgregory.net/rapid` (established Phase 1.1 precedent at `internal/engine/pickcwd_test.go`; zero new deps).

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| `coerceToolCall` algorithm | engine (`internal/engine/coerce.go`) | — | Single canonical-typed implementation per D-01; adapters call but do not own. |
| Per-surface tool spec decode | adapter (wire.go) | canonical (ToolSpec type) | OpenAI `tools[].function` and Anthropic `input_schema` → `canonical.ToolSpec` happens at the adapter boundary (TRST-04). |
| Per-surface tool_calls rendering (non-streaming) | adapter (render.go) | canonical (ToolCall type) | OpenAI JSON-string args, Ollama object args, Anthropic tool_use block — each adapter owns its native wire shape. |
| Per-surface tool_calls rendering (streaming) | adapter (sse.go / ndjson.go) | engine (chunk channel) | Same adapter as non-streaming, extended to handle `ChunkKindToolCall` in the per-emitter state machine (Phase 4 D-08 no-shared-driver). |
| ACP `tool_call` notification translation | acp (`internal/acp/translate.go`) | canonical (ToolCallChunk) | translate.go owns ACP-wire → canonical mapping; closes Phase 1.1 placeholder. |
| Tool-call chunk aggregation | engine (`internal/engine/collect.go`) | canonical (Message.ToolCalls, ContentKindToolUse) | Engine's Collect is the SINGLE aggregator for stream → response. |
| `[Available tools]` JSON catalog emission | engine (`internal/engine/build_acp.go`) | — | buildBlocks is the single-source-of-truth for ACP block construction (D-16). |
| E2E real-binary verification | tests/e2e | adapter (fake-kiro fixtures) | Real-binary boot pattern from Phase 5; per-surface scenario files. |

## Standard Stack

### Core (already imported — zero new deps)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `encoding/json` (stdlib) | go 1.23 | tools[] decode, args marshal/unmarshal, partial_json | Default Go JSON path; CONTEXT-locked (no third-party JSON lib in this project) |
| `testing/quick` (stdlib) | go 1.23 | Property tests for `coerceToolCall` invariants | TRST-06 + Phase 1.1 precedent at `internal/engine/pickcwd_test.go` [VERIFIED: codebase grep] |
| `go.uber.org/goleak` | v1.3.0 (already in go.mod) | Goroutine-leak gate on handler + E2E tests | Phase 1 / 4 / 5 discipline; CONTEXT D-21 [VERIFIED: codebase grep — 21+ existing usages] |
| `regexp` (stdlib) | go 1.23 | Markdown-fence stripping in `coerceToolCall` | Already used in `internal/adapter/anthropic/wire.go` (claudeModelHyphenVersionRe) for similar purpose [VERIFIED: codebase grep] |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `crypto/rand` (stdlib) | go 1.23 | Synthesized tool_call ID (alternative to unix-nano per D-11) | Already used by `internal/adapter/openai/render.go:genMessageID` and `anthropic/render.go:genMessageID`. If picking hex over unix-nano for synthesized IDs, reuse this pattern. |
| `time` (stdlib) | go 1.23 | `call_<unix-nano>` synthesized tool_call ID (D-11 locked choice) | CONTEXT D-11 locks this format; mirrors Phase 2 `chatcmpl-<nano>`. |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `testing/quick` | `pgregory.net/rapid` | rapid has better shrinking + more readable failure output BUT adds a dependency and breaks the Phase 1.1 `pickcwd_test.go` precedent. SC #5 says "or" — pick stdlib for zero-dep discipline. [VERIFIED: rapid NOT imported anywhere — codebase grep returned zero matches] |
| `call_<unix-nano>` ID format | `call_<hex>` via crypto/rand | Time-based collides per nanosecond at sub-microsecond rates; hex is collision-resistant. BUT D-11 LOCKS unix-nano per Phase 2/3.1 precedent (`chatcmpl-<unix-nano>` / `msg_01<hex>` — note Phase 3.1 already mixes both). Stay with the locked choice unless plan-phase discovers a real collision in tests. |
| Single combined OpenAI delta frame | Multi-frame (id+name → arguments) | Both work per `@openai-sdk`. Multi-frame is OpenAI's own example shape (per WebSearch results) and matches what real OpenAI emits — easier to diff against Bifrost reference. Pick multi-frame for SDK parser robustness. |
| Single `input_json_delta` for full args (Anthropic) | Multiple `input_json_delta` deltas | Both work per Anthropic spec ("partial JSON strings" that concatenate). Since kiro emits args atomically (D-06), there's nothing to chunk — single delta is correct AND wire-spec-compliant. |

**Installation:** No `go get` needed. All required packages are in `go.mod` already.

**Version verification (zero installs):**
```bash
# All packages already in go.mod — verify:
grep -E "goleak|testing/quick" /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/go.mod
# go.uber.org/goleak v1.3.0  ← VERIFIED already present
# testing/quick is stdlib   ← no go.mod entry needed
```

## Package Legitimacy Audit

> Phase 6 installs **zero new external packages**. All required functionality is satisfied by stdlib + the already-vendored `go.uber.org/goleak`. The legitimacy gate is N/A by construction.

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| (none — phase adds no deps) | — | — | — | — | — | — |

**Packages removed due to slopcheck [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none

If a plan author later proposes `pgregory.net/rapid`, run slopcheck against it before adding — but per recommendation in Alternatives Considered, stdlib `testing/quick` is preferred.

## Architecture Patterns

### System Architecture Diagram

```
                                   ┌────────────────────┐
HTTP request (3 surfaces)          │ adapter/{ollama,   │
─────────────────────────────────▶ │  openai,anthropic} │
                                   │  wire.go (DECODE)  │
                                   └──────────┬─────────┘
                                              │ canonical.ChatRequest
                                              │ (req.Tools, req.ToolChoice)
                                              ▼
                                   ┌────────────────────┐
                                   │ engine.Run         │
                                   │ → ACP session/     │
                                   │   prompt with      │
                                   │   [Available tools]│
                                   │   JSON catalog     │ (D-16)
                                   └──────────┬─────────┘
                                              │
                  ┌───────────────────────────┴────────────────────────────┐
                  │                                                         │
                  ▼ stream chunks                          response sync    ▼
       ┌─────────────────────┐                                ┌──────────────────────┐
       │ acp/translate.go    │                                │ engine.Collect       │
       │ session/update      │  ChunkKindToolCall +           │ aggregates           │
       │ → typed Chunk       │  ChunkKindText/Thought +       │ Text + Thought +     │
       │ (D-03: tool_call)   │  ChunkKindPlan                 │ ToolCall chunks      │
       └──────────┬──────────┘                                │ → Message.ToolCalls  │
                  │                                           │ + Content[ToolUse]   │
                  │                                           └──────────┬───────────┘
                  │                                                      │
                  │ (streaming path: chunks flow live to adapter SSE/    │
                  │  NDJSON emitter; tool_call chunk triggers D-07       │
                  │  per-surface event sequence)                         │
                  │                                                      │
                  ▼ live stream                                          ▼ assembled
       ┌─────────────────────────┐                       ┌─────────────────────────┐
       │ adapter SSE/NDJSON      │                       │ engine.CoerceToolCall   │
       │ emitter (per surface):  │                       │ (Ollama + OpenAI only)  │
       │  - Anthropic SSE        │                       │ (D-01: NOT Anthropic)   │
       │  - OpenAI SSE           │                       │ Detects bare/fenced JSON│
       │  - Ollama NDJSON        │                       │ in assistant text;      │
       │                         │                       │ pickBestTool scoring;   │
       │ D-07 per-surface shape  │                       │ synthesizes ToolCall    │
       └────────────┬────────────┘                       └────────────┬────────────┘
                    │                                                  │
                    │                                                  ▼
                    │                                       ┌─────────────────────────┐
                    │                                       │ adapter render.go       │
                    │                                       │ (non-streaming):        │
                    │                                       │ Message.ToolCalls →     │
                    │                                       │  - Ollama object args   │
                    │                                       │  - OpenAI JSON-string   │
                    │                                       │  - Anthropic tool_use   │
                    │                                       │    block via Content    │
                    │                                       │    ToolUse part         │
                    │                                       └────────────┬────────────┘
                    │                                                    │
                    ▼ live wire bytes                                    ▼ JSON body
              ┌──────────────────────────────────────────────────────────────┐
              │ HTTP response (3 surfaces × {stream, non-stream} × {kiro-   │
              │  native tool_call, coerce, no-tool})                        │
              └──────────────────────────────────────────────────────────────┘
```

### Recommended Project Structure

No new packages. Files added/modified within existing packages:

```
internal/
├── canonical/
│   └── chunk.go              # MODIFY: add ToolCallChunk.ID field (D-08)
├── acp/
│   └── translate.go          # MODIFY: tool_call notification → ToolCallChunk (D-03)
├── engine/
│   ├── coerce.go             # NEW: CoerceToolCall + pickBestTool + stripFences
│   ├── coerce_test.go        # NEW: property tests (D-12, D-20)
│   ├── collect.go            # MODIFY: third aggregator for ToolCall chunks
│   └── build_acp.go          # MODIFY: emit JSON tool catalog in [Available tools]
└── adapter/
    ├── ollama/
    │   ├── wire.go           # NO CHANGE (Phase 2 forward seam already complete)
    │   ├── handlers.go       # MODIFY: call engine.CoerceToolCall after Collect
    │   ├── render.go         # MODIFY: populate Message.ToolCalls in chatResponseToWire
    │   └── ndjson.go         # MODIFY: tool_calls on done:true final line
    ├── openai/
    │   ├── wire.go           # MODIFY: typed Tools decode, ToolChoice (D-13)
    │   ├── handlers.go       # MODIFY: call engine.CoerceToolCall after Collect
    │   ├── render.go         # MODIFY: tool_calls with JSON-string args
    │   └── sse.go            # MODIFY: applyChunk handles ChunkKindToolCall (D-07)
    └── anthropic/
        ├── wire.go           # MODIFY: close TODO(Phase 6), decode tools (D-14)
        └── sse.go            # MODIFY: applyChunk emits content_block_* for tool_use

tests/e2e/                     # NEW files in existing package
├── tools_ollama_test.go      # NEW: D-17 scenarios 1-4, 6-11
├── tools_openai_test.go      # NEW: D-17 scenarios 1-4, 6-11
├── tools_anthropic_test.go   # NEW: D-17 scenarios 1, 2, 5, 9
└── tools_cancel_test.go      # NEW: D-17 scenario 12 (per-surface subtests)
```

### Pattern 1: `coerceToolCall` (engine, canonical-typed, pure)

**What:** Single canonical-typed implementation; returns `bool` so callers can log a `coerce=true` tag (Node parity).

**When to use:** Called by Ollama and OpenAI handlers immediately after `engine.Collect`; NOT called by Anthropic (D-01).

**Example (pseudo-Go — actual implementation lives in plan):**
```go
// Source: docs/reference/acp_server_node_reference.md §"Load-bearing weirdness" + CONTEXT D-09
// CoerceToolCall returns true if it rewrote resp; idempotent.
func CoerceToolCall(req *canonical.ChatRequest, resp *canonical.ChatResponse) bool {
    // Step 1: skip if no tools OR tool_calls already populated
    if req == nil || resp == nil || len(req.Tools) == 0 ||
       len(resp.Message.ToolCalls) > 0 {
        return false
    }

    // Step 2: extract assistant text (single ContentKindText per collect.go)
    text := extractAssistantText(resp.Message.Content)
    if text == "" {
        return false
    }

    // Step 3: try raw parse
    var parsed map[string]any
    if err := json.Unmarshal([]byte(text), &parsed); err != nil {
        // Step 4: try fenced parse (```json ... ``` OR ``` ... ```)
        stripped, ok := stripFences(text)
        if !ok {
            return false
        }
        if err := json.Unmarshal([]byte(stripped), &parsed); err != nil {
            return false  // Step 5: text preserved
        }
    }

    // Steps 6-7: pickBestTool by property-overlap; zero-score → no-op
    tool, score := pickBestTool(parsed, req.Tools)
    if score == 0 {
        return false
    }

    // Steps 8-9: rewrite content, synthesize tool_call
    resp.Message.Content[0].Text = ""
    resp.Message.ToolCalls = append(resp.Message.ToolCalls, canonical.ToolCall{
        ID:        fmt.Sprintf("call_%d", time.Now().UnixNano()),
        Name:      tool.Name,
        Arguments: parsed,
    })
    return true
}
```

### Pattern 2: Per-surface streaming tool_call emission

**What:** Each adapter's `applyChunk` is extended to handle `ChunkKindToolCall`. Phase 4 D-08 "no shared stream driver" — each emitter grows locally.

**When to use:** Inside `sse.go` (OpenAI/Anthropic) and `ndjson.go` (Ollama) `applyChunk` methods, after handling the existing text/thought kinds.

**Anthropic SSE example** (D-07 Anthropic):
```go
// Source: https://platform.claude.com/docs/en/api/messages-streaming (verified)
// On ChunkKindToolCall:
//   1. If a block is open AND kind differs → emit content_block_stop, bump index
//   2. Emit content_block_start with header:
//      {"type":"tool_use","id":"<chunk.ToolCall.ID>","name":"<chunk.ToolCall.Name>","input":{}}
//   3. Marshal chunk.ToolCall.Args to JSON; emit ONE content_block_delta:
//      {"type":"input_json_delta","partial_json":"<the-marshaled-json>"}
//   4. Emit content_block_stop; bump index; mark blockOpen=false
// SDK accumulates the partial_json strings; single delta carrying full args
// is wire-spec-compliant (CONTEXT D-07 Anthropic + verified at platform docs).
```

**OpenAI SSE example** (D-07 OpenAI — recommended multi-frame):
```go
// Source: WebSearch verified — OpenAI docs example showing multi-frame
// On ChunkKindToolCall, emit TWO data: frames:
//   Frame 1 (id + name, empty arguments string):
//     {"choices":[{"index":0,"delta":{"tool_calls":[{
//       "index":0,"id":"<chunk.ToolCall.ID>","type":"function",
//       "function":{"name":"<chunk.ToolCall.Name>","arguments":""}
//     }]},"finish_reason":null}]}
//   Frame 2 (arguments incrementally — for kiro that's a single chunk):
//     {"choices":[{"index":0,"delta":{"tool_calls":[{
//       "index":0,"function":{"arguments":"<json-string>"}
//     }]},"finish_reason":null}]}
// Then terminal finalize frame:
//     {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}
//     data: [DONE]
```

**Ollama NDJSON example** (D-07 Ollama — atomic on done line):
```go
// Source: Node reference chunksToOllamaMessage (acp_server_node_reference.md)
// Per-stream: collect ChunkKindToolCall into a buffer (no per-line emission).
// On stream close, the existing done:true line composer in ndjson.go adds:
//   "message":{"role":"assistant","content":"<accumulated text>",
//             "tool_calls":[{"function":{
//               "name":"<chunk.ToolCall.Name>",
//               "arguments":<chunk.ToolCall.Args as plain object>
//             }}]}
// Object args (NOT JSON string) per Ollama wire spec (CONTEXT D-07 Ollama,
// SC #2, Bifrost ollama integration ref).
```

### Pattern 3: kiro-native vs coerce — the two-path rule (D-03 vs D-05)

**What:** Same canonical `ToolCallChunk` / `ToolCall` type populated from two distinct sources, but rendered differently per CONTEXT:

| Source | Canonical produced | Wire rendering | Why |
|--------|-------------------|----------------|-----|
| ACP `tool_call` notification (kiro ran tool internally) | `ChunkKindToolCall` chunk → `Message.ToolCalls` entry | **Wire renders `[tool: <name>]\n` thought text** per surface (D-03) | kiro auto-grants permission and runs the tool; the chunk is narration, not a callable. Per-surface SSE/NDJSON emitters render the tool_call chunk as thought-text delta. |
| `coerceToolCall` (model emitted JSON-as-text, client must run tool) | `Message.ToolCalls` entry synthesized at non-streaming Collect time | **Wire renders native tool_calls shape** per surface (D-05) | Client must execute the tool. Real `tool_calls[]` entry in the response. |

**This is intentional**, not a bug. Phase 6 PR description should call it out (CONTEXT specifics §1). Critically: Phase 6 ALSO populates `Message.ToolCalls` from `ChunkKindToolCall` aggregation in `collect.go` (so non-streaming requests where kiro fires a tool_call notification produce a real `tool_calls[]` entry — see CONTEXT line 31 "Phase 6 wires them through"). The two-path rule applies SPECIFICALLY to the **streaming** wire shape: kiro-native streaming chunks render as thought-text (no per-frame `tool_calls[]` delta); coerce-synthesized tool_calls live only in the non-streaming Collect path so they render in native shape on the response body.

### Anti-Patterns to Avoid

- **Running `coerceToolCall` on Anthropic:** Would silently rewrite `messages.stream()` consumers' assistant text into `tool_use` blocks. Per D-01: handler invocation only on Ollama + OpenAI. Anthropic-native clients (`@anthropic-ai/sdk` / loop24-client) emit native `tool_use` blocks, not JSON-as-text.
- **Duplicating the `pickBestTool` algorithm in each adapter:** D-01 locks engine-helper placement. Adapter duplication invites silent drift between LangChain paths — the failure mode `coerceToolCall` was written to prevent.
- **Shared stream driver across surfaces:** Phase 4 D-08 invariant — each emitter is independent. Phase 6 grows OpenAI's SSE applyChunk, Anthropic's SSE applyChunk, and Ollama's NDJSON aggregator separately. No abstraction.
- **Synthesizing partial `input_json_delta` chunks for Anthropic:** Per Anthropic spec, partial deltas concatenate to a complete JSON. kiro emits args atomically (D-06), so one delta with the full JSON is correct. Fake-chunking is unnecessary and risks broken JSON if the chunker splits across an escape boundary.
- **Putting `tool_calls[]` per-frame on OpenAI streaming (one per chunk) when kiro emits a single tool_call atomically:** OpenAI's multi-frame design is for *real* incremental argument streaming. kiro emits whole args; emitting frame1(id+name) + frame2(full args) once is correct. Don't pretend to stream what isn't streamed.
- **Adapter importing `internal/engine` for anything beyond `engine.Run` / `engine.Collect` / `engine.CoerceToolCall`:** TRST-04 boundary. Phase 6 expands the public engine API by exactly ONE function (`CoerceToolCall`) — confirm `.go-arch-lint.yml` allows it (it does — engine is a permitted import).

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| JSON parsing | Hand-rolled parser | `encoding/json` `Unmarshal` | Defensive parsing is exactly what `coerceToolCall` needs — `json.Unmarshal` failures cleanly signal "not JSON, preserve text" (Step 5 of D-09). |
| Markdown fence detection | Regex on backticks | A small targeted regex OR a simple `strings.TrimPrefix(s, "```json\n") / TrimSuffix(s, "```")` chain | Node reference's pattern is ` ```json … ``` ` OR ` ``` … ``` `. Two `strings.TrimPrefix` calls with case fallthrough is simpler than a regex and tolerates whitespace/newlines per D-10. |
| Tool-spec validation | Custom JSON Schema validator | None — pass-through | kiro-cli is the consumer. The gateway validates only that `tools[]` decodes to a known shape; semantics are kiro's problem. |
| Random ID generation | Custom RNG | `time.Now().UnixNano()` (D-11) OR `crypto/rand` (existing render.go pattern) | D-11 locks unix-nano per the chatcmpl-/msg_01- precedent. Sub-microsecond collision is theoretically possible but the wall-clock granularity on Linux/Windows is ns and call sites are serialized through a single Collect — collision-free in practice. |
| Per-frame OpenAI tool_calls index assignment | Custom counter | Hardcode `index: 0` for Phase 6 | Phase 6 emits exactly one tool_call per turn (kiro atomic emission). `n>1` parallel tool calls is OpenAI's `parallel_tool_calls` feature — explicitly deferred per CONTEXT `<deferred>`. |

**Key insight:** Every potential hand-roll in Phase 6 has a stdlib or one-liner replacement. The phase is small (~6 modified files, 4 new test files) precisely because we're activating dormant canonical seams, not building infrastructure.

## Common Pitfalls

### Pitfall 1: Anthropic `content_block_start` `input` field missing/wrong type
**What goes wrong:** Wire emits `"input":null` or omits the field entirely; `@anthropic-ai/sdk` MessageStream parser rejects the block as malformed.
**Why it happens:** Go `map[string]any` zero value is nil; default JSON encoding emits `null`. The CR-01 fix in Phase 3.1 used `*map[string]any` pointer to preserve "empty but present" through omitempty — same trick required for tool_use block headers.
**How to avoid:** Header type must coerce nil → `map[string]any{}` BEFORE marshal (mirror Phase 3.1 `render.go:122-124`). Per Anthropic spec line 1093 (verified): `"content_block":{"type":"tool_use","id":"...","name":"...","input":{}}` — empty OBJECT, not null.
**Warning signs:** Golden-fixture test bytes contain `"input":null` instead of `"input":{}`; integration test with real `@anthropic-ai/sdk` throws JSON parse error before block delivery.

### Pitfall 2: OpenAI streaming `delta.tool_calls[]` arguments split across frames at JSON escape boundary
**What goes wrong:** SDK accumulates `arguments` strings; if frame 1 sends `"{\"loc"` and frame 2 sends `"ation\":\"NYC\"}"`, the SDK's parser may try to parse early and fail.
**Why it happens:** Trying too hard to mimic real OpenAI's multi-frame chunking when kiro atomically delivers the full JSON.
**How to avoid:** Emit id+name in frame 1 (with empty `arguments:""`), full JSON in frame 2 (single string atom). Don't split across multiple `arguments` frames since there's no incremental source. CONTEXT explicitly allows single combined frame too — if the multi-frame logic adds complexity, fall back to one frame with everything.
**Warning signs:** OpenAI SDK consumers see truncated tool_call.arguments; partial-args strings logged in test output.

### Pitfall 3: `coerceToolCall` fence-strip regex eats valid JSON-as-text
**What goes wrong:** A response like `Here's some code: \`\`\`json\n{"unrelated":"data"}\n\`\`\`` gets coerced into a tool_call when the JSON in the fence is unrelated narrative.
**Why it happens:** The fence-strip is too aggressive — it should only trigger when the ENTIRE response is a fenced JSON block, not when one appears inline in prose.
**How to avoid:** Per CONTEXT D-09 Step 2, extract assistant-text as the single ContentKindText part; check that AFTER `strings.TrimSpace`, the text either parses as JSON directly (Step 3) OR consists ENTIRELY of a fence-wrapped block (no prose before/after). Match Node's behavior: only try fence-strip if raw parse failed AND the text looks like a pure fence wrap.
**Warning signs:** Property test asserts text-with-inline-JSON is preserved; unit test fixture "Here is JSON: ```json{}```" produces no coerce.

### Pitfall 4: `pickBestTool` tie-breaker non-determinism from map iteration
**What goes wrong:** Two tools tie on property-overlap score; Go map iteration order is random; coerce picks different tools on different runs.
**Why it happens:** Naive impl loops `for _, tool := range req.Tools` over a slice (deterministic) BUT uses a `map[string]int` for scores keyed by tool name, then picks via second iteration — that second iteration is random.
**How to avoid:** Score in a single pass over the `req.Tools` slice, tracking `bestIdx int` and `bestScore int`. On tie (`score == bestScore`), DO NOT update — keep the first-declared (D-10 locked). Property test asserts deterministic pick under fixed-shuffle invariant (D-12).
**Warning signs:** CI test flake on multi-tool scenarios; property test failures with shuffled-input invariant.

### Pitfall 5: Phase 1.1 `[tool: <name>]\n` placeholder removed instead of preserved (D-03)
**What goes wrong:** Plan author "fixes" the placeholder by emitting a `canonical.Chunk{Kind: ChunkKindToolCall}` AND removing the thought-text emission. Per-surface emitters then see ChunkKindToolCall but produce nothing visible to clients that don't render tool_calls inline (most don't — they wait for the response body).
**Why it happens:** D-03 is subtle. The thought-text rendering is preserved as operator-visible narration; the canonical ToolCallChunk emission is ADDITIVE (it populates Message.ToolCalls via the new collect.go aggregator and provides a structured chunk for Phase 8 hooks).
**How to avoid:** Plan must explicitly preserve the per-surface `[tool: <name>]\n` thought-text rendering AS WELL AS emit the canonical ToolCallChunk. CONTEXT D-03 line 135: "Wire rendering (Node parity): Each per-surface SSE/NDJSON emitter consumes the `ChunkKindToolCall` and renders it as a `[tool: <name>]\n` thought-text delta." Per-surface tool_calls in streaming wire shape happen via D-07 — NOT from the kiro-native ChunkKindToolCall, but from the synthetic-coerce path.
**Warning signs:** LangFlow flows lose the visible "[tool: foo]" indicator; operator complaints about lack of progress feedback during tool runs.

### Pitfall 6: `coerceToolCall` rewrites response in-place but caller still has stale Message.Content reference
**What goes wrong:** Handler captures `respCopy := *resp` before coerce; coerce mutates `resp.Message.Content[0].Text` and appends to `resp.Message.ToolCalls`; render uses respCopy and emits the old text + missing tool_call.
**Why it happens:** Slice-of-struct copy in Go is shallow; the underlying ContentPart slice IS shared, BUT ToolCalls is a separate slice and an append on resp.Message.ToolCalls doesn't reflect in respCopy.Message.ToolCalls.
**How to avoid:** Handler MUST pass `resp` directly (pointer) and use it post-coerce. Don't pre-copy. Tests should explicitly check: "after coerce, render(resp) produces tool_calls[] in wire output."
**Warning signs:** Integration test sees text content in response that was supposed to be coerced; tool_calls[] absent despite `coerce=true` log tag.

### Pitfall 7: Anthropic block-index discipline broken when tool_use immediately follows text
**What goes wrong:** Text block at index 0, tool_use chunk arrives; emitter closes text (index 0), opens tool_use at index 1 — but blockIndex was prematurely incremented somewhere, so tool_use opens at index 2, then message_delta references the wrong final block count.
**Why it happens:** The Phase 3.1 D-04 state machine in `anthropic/sse.go:204` bumps blockIndex on every kind transition. Adding ChunkKindToolCall as a third recognized kind must integrate WITHOUT inadvertent double-increment.
**How to avoid:** Carefully extend the switch in `applyChunk` so ChunkKindToolCall produces a header (like text/thinking) and flows through the same close-then-open path. The blockIndex bump happens in step 2 (kind transition close); don't add a second bump in the tool_use branch. Golden fixture test asserts the index sequence 0→1→2 for text→tool_use→text patterns.
**Warning signs:** `@anthropic-ai/sdk` MessageStream throws "block index mismatch" or fails to accumulate the final Message correctly; SSE byte-diff against golden fixture shows wrong index values.

### Pitfall 8: ACP `tool_call_chunk` notification handled identically to `tool_call` but they may have different field availability
**What goes wrong:** Per `docs/reference/acp_wire_shapes.md` §7, `tool_call_chunk` and `tool_call` are both routed to the same code path in Phase 1.1. But `tool_call_chunk` is for INCREMENTAL args (kiro chunking JSON across multiple notifications) — accumulating them per `toolCallId` is required.
**Why it happens:** Phase 1.1 collapsed both to `[tool: <title>]\n` text — there was no aggregation needed. Phase 6 promotes to real `ChunkKindToolCall`; if kiro ever emits `tool_call_chunk` separately from `tool_call`, the second arrival might create a duplicate canonical chunk rather than extending the first.
**How to avoid:** Per CONTEXT D-06: "kiro emits complete args atomically." Confirm at integration-test time that kiro NEVER sends `tool_call_chunk` for partial args (Phase 1.1 testing observed only `tool_call` from kiro 2.4.1). If a future kiro version DOES emit chunks, that's a separate `tool_call_chunk` aggregation feature — defer per CONTEXT `<deferred>` "True incremental tool_call streaming." For Phase 6, treat both as full-args atomic events.
**Warning signs:** Real-kiro integration test sees both `tool_call` AND `tool_call_chunk` notifications for the same `toolCallId`; assertion fails on duplicate tool_calls in response.

## Runtime State Inventory

> Phase 6 is greenfield additive work activating dormant canonical seams. No rename, refactor, or migration involved. Section omitted per template guidance.

## Code Examples

Verified patterns from official sources and existing in-tree code:

### Anthropic tool_use streaming sequence (verified Anthropic spec)
```
event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01T1x1fJ34qAmk2tNTrN7Up6","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"location\":\"San Francisco, CA\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":89}}
```
Source: https://platform.claude.com/docs/en/api/messages-streaming lines 1093-1117 (verified). Note `input:{}` (object, not null) in start event; `partial_json` carries the entire serialized args in a single delta — spec allows fine-grained chunking but does not require it; concatenation of all partial_json strings yields valid JSON object. Final `message_delta` carries `stop_reason: "tool_use"` (NOT `end_turn`) when the turn ended with a tool call. **Plan must add `StopToolUse` to the canonical StopReason enum** OR map kiro's `StopEndTurn` → `"tool_use"` when `tool_calls` is non-empty on the response.

### OpenAI streaming tool_calls multi-frame (verified via WebSearch + OpenAI docs)
```
data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}],...}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"location\":\"NYC\"}"}}]},"finish_reason":null}],...}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],...}

data: [DONE]
```
Source: WebSearch verified — OpenAI's own examples show id+name in frame 1, arguments in subsequent frame(s), terminal `finish_reason: "tool_calls"`. The `index: 0` inside `tool_calls[]` is the per-call slot index (allows multi-tool, which we don't use). Note `delta.role:"assistant"` only on frame 1 (already implemented in `internal/adapter/openai/sse.go:119-128` via `roleSent` tracking).

### Ollama final done:true line with tool_calls (Node reference parity)
```ndjson
{"model":"auto","created_at":"2026-05-26T...","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_weather","arguments":{"location":"NYC"}}}]},"done":true,"done_reason":"stop","total_duration":12345,...}
```
Source: Node reference `chunksToOllamaMessage` (per `docs/reference/acp_server_node_reference.md`). `arguments` as plain object, NOT JSON string. Atomically emitted on done line; no per-chunk tool_calls deltas (no Ollama spec for that). Existing `ollamaChatResponseMessage` type at `internal/adapter/ollama/wire.go:86` already has the `Content` field; the `ToolCalls` field already exists on `ollamaMessage` at line 40 — render.go's `chatResponseToWire` just needs to populate it from `resp.Message.ToolCalls`.

### `coerceToolCall` algorithm (CONTEXT D-09, verified against narrative reference)
```
1. Skip if len(req.Tools) == 0 OR len(resp.Message.ToolCalls) > 0       → return false
2. text := single ContentKindText part from resp.Message.Content
3. parsed := try json.Unmarshal(text)
4. If fail: stripped := stripFences(text); parsed := json.Unmarshal(stripped)
5. If still fail                                                         → return false
6. For each spec in req.Tools:
     properties := top-level keys of spec.Parameters["properties"]
     score := count of keys in parsed that appear in properties
     (skip spec if properties is nil/empty)
   bestIdx := argmax(scores), ties broken by FIRST declared (D-10)
7. If bestScore == 0                                                     → return false
8. resp.Message.Content[0].Text = ""
   resp.Message.ToolCalls = append(..., ToolCall{ID:synthID, Name:bestSpec.Name, Arguments:parsed})
9. Return true
```
Source: `docs/reference/acp_server_node_reference.md` §"Load-bearing weirdness: `coerceToolCall`" (lines 166-195) + CONTEXT D-09/D-10. **Caveat:** the actual Node JS source (`acp-ollama-server.js`) is NOT in this checkout (parent dir `gitlab.rosetta.ericssondevops.com/loop_24/acp_server` does not exist locally). The algorithm is verified against the narrative reference, NOT against Node code byte-by-byte. The plan-phase should add a checkpoint to fetch the Node source for byte-level fidelity check before sign-off, per CONTEXT line 471 ("Where Node's code and the narrative doc disagree, the Node code wins"). [ASSUMED: Step ordering matches Node — verified against narrative only]

### Property test pattern (verified — existing pickcwd_test.go precedent)
```go
// Source: internal/engine/pickcwd_test.go:208 (verified in-tree)
func TestCoerceToolCall_NeverPanics(t *testing.T) {
    property := func(text string, toolNames []string) bool {
        req := &canonical.ChatRequest{Tools: makeTools(toolNames)}
        resp := &canonical.ChatResponse{
            Message: canonical.Message{
                Content: []canonical.ContentPart{
                    {Kind: canonical.ContentKindText, Text: text},
                },
            },
        }
        _ = CoerceToolCall(req, resp)
        return true
    }
    cfg := &quick.Config{MaxCount: 1000}
    if err := quick.Check(property, cfg); err != nil {
        t.Errorf("CoerceToolCall property check failed: %v", err)
    }
}
```

### E2E test scaffold (verified — existing tests/e2e/ pattern)
```go
//go:build e2e

// tools_ollama_test.go reuses the shared helpers in tests/e2e/e2e_test.go
// (gateOrSkip, bootGateway, readAll, postChat) per Phase 4 / 5 precedent.
package e2e_test

import (
    "encoding/json"
    "net/http"
    "testing"
)

func TestE2E_Tools_Ollama(t *testing.T) {
    gateOrSkip(t)
    baseURL, cleanup := bootGateway(t, nil) // default ENABLED_SURFACES + AUTH_TOKEN
    defer cleanup()

    t.Run("NativeToolCall_NonStreaming", func(t *testing.T) { /* D-17 scenario 1 */ })
    t.Run("NativeToolCall_Streaming",    func(t *testing.T) { /* D-17 scenario 2 */ })
    t.Run("Coerce_BareJSON",             func(t *testing.T) { /* D-17 scenario 3 */ })
    t.Run("Coerce_FencedJSON",           func(t *testing.T) { /* D-17 scenario 4 */ })
    // scenarios 6-11 likewise; 5 only in tools_anthropic_test.go (verifies NO coerce)
    // scenario 12 in tools_cancel_test.go (per-surface subtests w/ goleak gate)
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Phase 1.1 thought-text placeholder for tool_call ACP notifications | Phase 6 real ChunkKindToolCall + per-surface render | This phase | Closes Phase 1.1 TODO; thought-text fallback preserved per D-03 |
| Phase 2 `Tools json.RawMessage` accept-and-ignore (OpenAI) | Phase 6 typed `[]openAIToolSpec` → `canonical.ToolSpec` | This phase (D-13) | OpenAI tools[] requests now flow into the engine catalog and the [Available tools] section |
| Phase 3.1 `anthropicToolSpec` decoded but not translated to canonical | Phase 6 `input_schema` → `canonical.ToolSpec.Parameters` | This phase (D-14) | Closes Phase 3.1 TODO at `wire.go:242`; Anthropic clients' tool catalogs reach kiro |
| Single combined chunk frame for OpenAI tool_calls SSE | Multi-frame (id+name → arguments → terminal) | Phase 6 plan choice | Matches real OpenAI SDK example; SDK parser more robust |
| `ChunkKindToolCall` / `ChunkKindPlan` dropped in `engine.Collect` | Third aggregator → `Message.ToolCalls` + `ContentKindToolUse` parts | This phase (CONTEXT line 31) | Non-streaming responses now carry real tool_calls; Anthropic outbound tool_use path reachable |
| Placeholder `[Available tools]\nEmit a tool_call...` header | Full JSON tool catalog inside section | This phase (D-16) | kiro-cli sees the actual tool contract; without it, kiro can't tool-call (per CONTEXT line 287) |

**Deprecated/outdated:**
- Pre-Phase-6 assumption that "tools are accept-and-ignore for the engine path": every dormant tool field is now active. Adapter authors adding new surface-specific knobs should mirror the Phase 6 pattern (decode to canonical, render from canonical, no engine-layer surface-specific logic).
- Pre-Phase-6 absence of `ToolCallChunk.ID`: any code path that constructed a `ToolCallChunk{Name, Args}` literal now compiles fine (Go struct literals with named fields are forward-compat), but tests and golden fixtures that assert byte-equality may need updating.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Node `acp-ollama-server.js` algorithm exactly matches CONTEXT D-09's 9 steps (the Node source is NOT in this checkout — parent dir absent on this machine). Verification against narrative reference only. | Standard Stack, Pattern 1, Code Examples §coerceToolCall algorithm | Algorithm subtly differs from Node; LangChain clients that worked with Node fail against Go gateway. **Mitigation:** plan-phase MUST add a `checkpoint:human-verify` task to clone the Node source (or have user paste relevant function bodies) and diff against the Go implementation BEFORE phase sign-off. CONTEXT line 471 explicitly says "Node code wins" on disagreement. |
| A2 | Anthropic's `MessageStream` parser accepts single `input_json_delta` carrying full args (no requirement to chunk). | Pattern 2 Anthropic, Pitfall 1, Code Examples | If SDK requires multiple deltas, tool_use blocks fail to deliver to client. Verified against official spec lines 416-425 (concatenation of partial_json strings forms the input object) — risk is low but not zero. Integration test with real `@anthropic-ai/sdk` is the conformance gate. |
| A3 | OpenAI's official streaming examples use multi-frame (id+name → arguments) pattern; CONTEXT lets us pick either; multi-frame is more SDK-robust. | Standard Stack Alternatives, Pattern 2 OpenAI | Single-frame might work fine for `@openai-sdk` Pi-SDK CLI; multi-frame adds code complexity. Mitigation: golden-fixture test asserts the exact frame sequence; if Pi-SDK consumes single-frame fine, plan can simplify to single-frame. |
| A4 | kiro-cli 2.4.1 emits `tool_call` notifications atomically (full args in one notification, no `tool_call_chunk` follow-ups). | Pitfall 8 | If kiro emits chunks, Phase 6's aggregator produces duplicate tool_calls. Verified circumstantially via Phase 1.1 integration test (no chunk notifications observed). Plan should add an explicit integration-test assertion: "single tool_call notification per tool invocation; if tool_call_chunk arrives, fail loudly so we can add chunk aggregation." |
| A5 | The `[Available tools]` JSON catalog format exactly matches what kiro-cli expects (D-16 + Node reference §155-159). | Don't Hand-Roll, Code Examples | If catalog format drifts, kiro doesn't tool-call AT ALL — same as current Phase 2 placeholder behavior. Integration test: send request with tools[] populated, assert response includes real tool_call (not thought-text-only response). |
| A6 | The synthesized tool_call ID format `call_<unix-nano>` is sufficient for clients that key on ID equality (Pi-SDK, LangFlow). | Standard Stack Alternatives | If clients reject the format (e.g., regex-validate `call_[a-zA-Z0-9]{24}`), tool_calls don't round-trip. CONTEXT D-11 LOCKS this format per Phase 2 precedent — risk is in the LOCKED decision itself, not our interpretation. |
| A7 | Block-index discipline in Anthropic SSE extends cleanly to ChunkKindToolCall as a third kind alongside text/thinking. | Pitfall 7, Pattern 2 Anthropic | If the state machine breaks under tool_use, real `@anthropic-ai/sdk` consumers see block-index mismatch errors. Golden fixture test (extend existing `sse_golden_test.go`) is the gate. |

**If this table is empty:** N/A — 7 assumptions logged. **Plan-phase MUST address A1 with a checkpoint:human-verify task before code lands.** Other assumptions are validated by the D-17 E2E matrix.

## Open Questions (RESOLVED)

1. **Does kiro-cli ever emit `tool_call_chunk` separately from `tool_call`?**
   - What we know: Phase 1.1 routes both to the same handler; Phase 6 promotes both to canonical ToolCallChunk. CONTEXT D-06 says "kiro emits complete args atomically."
   - What's unclear: Whether kiro 2.4.x ever splits args across notifications.
   - Recommendation: Plan adds an integration-test assertion that the per-tool-invocation count of (`tool_call` + `tool_call_chunk`) notifications equals 1. Fail loudly if not — that's the signal to add chunk-aggregation logic (deferred per CONTEXT).
   - **RESOLVED:** Deferred. Phase 6 does NOT add a fail-loud assertion; if Node ever sends ≥2 chunks at a time we accept all (matches current Phase 1.1 behavior). If this becomes a real issue in production, file a follow-up. No plan task implements the assertion.

2. **Should `StopReason` gain a `StopToolUse` value for Anthropic streaming's `stop_reason: "tool_use"` message_delta?**
   - What we know: Anthropic emits `stop_reason: "tool_use"` when turn ended with a tool call (verified — see Code Examples). Current canonical StopReason has end_turn / max_tokens / max_turn_requests / refusal / cancelled / unknown.
   - What's unclear: Whether plan should add StopToolUse to canonical (touches Phase 1.1 D-02 seam) OR map at the Anthropic adapter layer (when tool_calls is non-empty, override stop_reason to "tool_use" in render).
   - Recommendation: **Add `StopToolUse` to canonical** (additive, no churn). Map to OpenAI's `finish_reason: "tool_calls"` and Ollama's `done_reason: "stop"` (no Ollama equivalent). Anthropic renders `"tool_use"`. This is one canonical concept across all three surfaces.
   - **RESOLVED:** NOT adopted. Plans use the **adapter-layer** `stop_reason` override path instead (06-04 Task 3, `internal/adapter/anthropic/render.go`, plus the streaming finalizer in 06-04 Task 2 `sse.go`) per REVIEW MEDIUM #4. Rationale: adapter-layer keeps the canonical type minimal and is the smaller diff against existing render code. The recommendation's reasoning still has merit; revisit in a future phase if more surfaces need it.

3. **Does the Ollama NDJSON `done_reason` field need updating when tool_calls is populated?**
   - What we know: Phase 2 `chatResponseToWire` populates `DoneReason` from canonical StopReason. Node reference always emits `"stop"` for tool turns (Ollama spec has no specific tool stop reason).
   - What's unclear: Whether Ollama clients (LangFlow) key on done_reason value when tool_calls is present.
   - Recommendation: Match Node — always `"stop"` regardless of tool_calls. If LangFlow breaks, revisit.
   - **RESOLVED:** Adopted. 06-02 Task 3 and 06-03 Task 3 both "always drop" the buffered text on coerce hit (matches Node behavior per Assumption A1). The Node byte-fidelity checkpoint in 06-01 will confirm. Ollama `done_reason` stays `"stop"` regardless of tool_calls per this resolution.

4. **For Anthropic non-streaming, does the response need `stop_reason: "tool_use"` when Message.ToolCalls is populated?**
   - What we know: `mapStopReason` in `anthropic/render.go:166` maps the canonical StopReason; Phase 6 needs to ensure responses with tool_use blocks have correct stop_reason.
   - What's unclear: If kiro returns StopEndTurn but the response has tool_use blocks, do we override to "tool_use"?
   - Recommendation: If Q2 is resolved with `StopToolUse` added to canonical, the engine's Collect aggregator should set `resp.StopReason = StopToolUse` whenever `len(Message.ToolCalls) > 0` at the end of the stream. Single source of truth, no adapter-layer override.
   - **RESOLVED:** NOT adopted (same as Q2). Adapter-layer override is the chosen path: 06-04 Task 3 (`render.go` non-streaming override) plus 06-04 Task 2 (`sse.go` streaming finalizer with `toolUseEmitted`).

5. **Should `coerceToolCall` also handle JSON ARRAY at top level (e.g., parallel tool_calls JSON-as-text)?**
   - What we know: D-09 Step 6 requires `parsed.(map[string]any)`; arrays are rejected per D-10 "Parsed value is not an object: return false."
   - What's unclear: Whether real LangChain agents emit arrays.
   - Recommendation: D-10 locks the behavior — don't expand. If LangChain ever emits arrays, that's a Phase-6.1 or v2 concern. Property test covers the no-array-coerce invariant.
   - **RESOLVED:** All listed edge cases are property-test inputs in 06-01 Task 3 (`testing/quick` Generator + named-case slice). Inline fenced JSON inside prose → no-coerce was added as an explicit case per LOW #6. Top-level JSON arrays remain rejected per D-10.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | All build/test work | ✓ (assumed — Phase 1-5 all built) | 1.23+ | — |
| `go.uber.org/goleak` | E2E goroutine-leak gate (D-21) | ✓ | v1.3.0 (in go.mod, 21+ existing usages) [VERIFIED: codebase grep] | — |
| `kiro-cli` 2.4.1+ | Real-binary E2E + integration tests | ✓ (assumed — Phase 1.1/5 integration tests rely on it) | 2.4.1 | Mock kiro via fake-kiro-cli per D-19 — controllable ACP notification scripts |
| `@anthropic-ai/sdk` (Node) | HUMAN-UAT conformance test for Anthropic streaming (CONTEXT specifics §2) | ✓ (assumed — Phase 3.1 / 5 used it) | ^0.90 | Skip the Node SDK smoke test; rely on golden-fixture byte-equality |
| Node `acp-ollama-server.js` (parent dir) | Byte-level algorithm fidelity check for `coerceToolCall` (Assumption A1) | ✗ NOT FOUND in `gitlab.rosetta.ericssondevops.com/loop_24/acp_server` (parent path doesn't exist locally) | — | **Plan MUST add `checkpoint:human-verify` task** to either: (a) clone the repo, (b) have user paste the `coerceToolCall` + `pickBestTool` function bodies, or (c) ship Phase 6 with explicit ASSUMED-vs-Node-source note and verify against Node behavior post-ship via LangChain integration smoke test |

**Missing dependencies with no fallback:** none

**Missing dependencies with fallback:**
- Node reference source: byte-level fidelity check deferred to checkpoint task; algorithm verified against narrative doc which is itself in-tree.

## Validation Architecture

> Plan-phase will operationalize this section into VALIDATION.md.

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `testing/quick` for property tests + `go.uber.org/goleak` for leak detection |
| Config file | None (Go convention — no config file); test files are `*_test.go` and `tests/e2e/*_test.go` with `//go:build e2e` |
| Quick run command | `go test ./internal/engine/... ./internal/acp/... ./internal/adapter/...` (~10s, excludes e2e) |
| Full suite command | `make ci` (lint + race + govulncheck + tests) followed by `OTTO_E2E=1 go test -tags e2e ./tests/e2e/...` (E2E adds ~60s real-kiro time) |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| TOOL-01 (Ollama object args) | Non-streaming Ollama tool_call returns plain-object arguments | unit + e2e | `go test ./internal/adapter/ollama/... -run TestChatResponseToWire_ToolCalls` + `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_Tools_Ollama/NativeToolCall_NonStreaming` | ❌ Wave 0 (new test files) |
| TOOL-01 (OpenAI JSON-string args) | Non-streaming OpenAI tool_call returns JSON-string arguments | unit + e2e | `go test ./internal/adapter/openai/... -run TestChatResponseToCompletion_ToolCalls` + `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_Tools_OpenAI/NativeToolCall_NonStreaming` | ❌ Wave 0 |
| TOOL-01 (Anthropic tool_use) | Non-streaming Anthropic tool_use block in content[] | unit + e2e | `go test ./internal/adapter/anthropic/... -run TestRender_ToolUse` + `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_Tools_Anthropic/NativeToolCall_NonStreaming` | ❌ Wave 0 |
| TOOL-02 (coerce from bare JSON) | Ollama+OpenAI: bare JSON in text → synthetic tool_call | unit + property + e2e | `go test ./internal/engine -run TestCoerceToolCall` + e2e | ❌ Wave 0 (`coerce_test.go` is new) |
| TOOL-02 (coerce from fenced JSON) | Ollama+OpenAI: markdown-fenced JSON in text → synthetic tool_call; fence stripped | unit + e2e | `go test ./internal/engine -run TestCoerceToolCall_Fenced` + e2e | ❌ Wave 0 |
| TOOL-02 (Anthropic does NOT coerce) | Anthropic surface preserves bare JSON text verbatim | e2e | `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_Tools_Anthropic/NoCoerce` | ❌ Wave 0 |
| TOOL-02 (pickBestTool tie-breaker) | First-declared tool wins on equal scores | property | `go test ./internal/engine -run TestCoerceToolCall_TieBreaker` | ❌ Wave 0 |
| TOOL-02 (never-panic) | Any (req, resp) input shape including nil pointers | property (testing/quick, MaxCount=1000) | `go test ./internal/engine -run TestCoerceToolCall_NeverPanics` | ❌ Wave 0 |
| TOOL-02 (idempotent) | Coerce(Coerce(x)) == Coerce(x) | property | `go test ./internal/engine -run TestCoerceToolCall_Idempotent` | ❌ Wave 0 |
| TOOL-03 (OpenAI tools[] normalization) | OpenAI tools[].function → canonical.ToolSpec | unit | `go test ./internal/adapter/openai -run TestWireToChatRequest_Tools` | ❌ Wave 0 |
| TOOL-03 (Anthropic tools[] normalization) | Anthropic tools[].input_schema → canonical.ToolSpec | unit | `go test ./internal/adapter/anthropic -run TestWireToChatRequest_Tools` | ❌ Wave 0 |
| TOOL-03 (Ollama already works) | Phase 2 forward seam regression | unit | `go test ./internal/adapter/ollama -run TestWireToChatRequest_Tools` | ✅ (Phase 2 — regression-only) |
| All scenarios — goroutine-leak gate | No goroutine leaks under tool_call streaming + cancel | leak | `goleak.VerifyNone(t)` wrapped in each e2e test | ❌ Wave 0 (E2E files don't exist) |
| D-07 OpenAI streaming wire shape | SSE event byte sequence matches frame1(id+name)+frame2(args)+terminal(tool_calls) | golden fixture | `go test ./internal/adapter/openai -run TestSSE_Golden_ToolCall` | ❌ Wave 0 |
| D-07 Anthropic streaming wire shape | SSE event byte sequence matches content_block_start(input={}) + delta(input_json_delta) + stop | golden fixture | `go test ./internal/adapter/anthropic -run TestSSE_Golden_ToolUse` | ❌ Wave 0 (extends existing sse_golden_test.go) |
| D-07 Ollama streaming wire shape | NDJSON final done:true line carries tool_calls[] as plain object | golden | `go test ./internal/adapter/ollama -run TestNDJSON_ToolCalls_DoneLine` | ❌ Wave 0 |
| D-12 round-trip property | synthesized tool_call → adapter render → re-decode preserves name + args | property | `go test ./internal/adapter/{ollama,openai,anthropic} -run TestToolCall_RoundTrip` | ❌ Wave 0 |
| D-16 `[Available tools]` catalog | engine.buildBlocks emits full JSON tool catalog when req.Tools is non-empty | unit | `go test ./internal/engine -run TestBuildBlocks_AvailableTools_JSONCatalog` | ❌ Wave 0 (extends existing build_acp_test.go) |
| D-17 scenario 12 (mid-stream cancel) | Client disconnect during tool_call → session/cancel; slot survives; no leak | e2e + leak | `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_Tools_Cancel` | ❌ Wave 0 |
| TRST-07 Example function | `Example_CoerceToolCall` runnable godoc | example | `go test ./internal/engine -run Example` | ❌ Wave 0 |

### Sampling Rate

- **Per task commit:** `go test ./internal/engine/... ./internal/acp/... ./internal/adapter/...` (≤10s; covers unit + property + golden)
- **Per wave merge:** `make ci` (lint + race + govulncheck + all in-process tests; ~30s)
- **Phase gate:** Full suite green + `OTTO_E2E=1 go test -tags e2e ./tests/e2e/...` (real-kiro, ~60s additional) before `/gsd-verify-work`. HUMAN-UAT with real loop24-client `messages.stream()` against the binary for D-17 Anthropic conformance (CONTEXT specifics §2).

### Wave 0 Gaps

- [ ] `internal/engine/coerce.go` — NEW (per D-01)
- [ ] `internal/engine/coerce_test.go` — NEW (property tests per D-12, D-20)
- [ ] `tests/e2e/tools_ollama_test.go` — NEW (D-17 scenarios 1-4, 6-11)
- [ ] `tests/e2e/tools_openai_test.go` — NEW
- [ ] `tests/e2e/tools_anthropic_test.go` — NEW
- [ ] `tests/e2e/tools_cancel_test.go` — NEW (scenario 12)
- [ ] Golden fixtures for SSE/NDJSON tool_call wire shapes: extend existing `internal/adapter/anthropic/sse_golden_test.go` + new `internal/adapter/openai/sse_golden_test.go` (if it doesn't exist for tool_calls) + new `internal/adapter/ollama/ndjson_test.go` tool_call subtests
- [ ] Framework install: NONE required — all deps already in `go.mod`
- [ ] Test fixture sharing: define a small `tests/e2e/tools_fixtures.go` (non-test file, no `_test.go` suffix) declaring the canonical 3-tool catalog (`get_weather`, `read_file`, `search_web`) and a controllable fake-kiro-cli script template per CONTEXT Claude's Discretion §test data

## Security Domain

> Phase 6 has minimal new security surface but it DOES exist: untrusted tool-args from kiro flow through canonical types into HTTP response bodies, and `coerceToolCall` parses untrusted assistant text. ASVS V5 (Input Validation) applies.

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | No new exposure | Existing Bearer + IP allowlist (AUTH-01..03) — Phase 6 doesn't add auth bypass paths |
| V3 Session Management | No new exposure | Phase 5 SessionRegistry unchanged; Phase 6 tool_call cancel goes through existing Phase 4 D-06 watchdog |
| V4 Access Control | No new exposure | kiro auto-grants permissions — that's a Phase 1 D-04 / SECURITY note, not Phase 6 |
| V5 Input Validation | YES | `encoding/json.Unmarshal` for tool-args parsing; `pickBestTool` only reads top-level keys (D-10); `[Available tools]` JSON catalog uses `json.Marshal` on canonical.ToolSpec (never raw concat) |
| V6 Cryptography | YES (tool_call ID generation) | Either `time.Now().UnixNano()` per D-11 (NOT cryptographic — IDs are opaque, no security claim) OR `crypto/rand` per the existing render.go convention. ID is opaque to clients per OpenAI/Anthropic specs; no cryptographic property is required. Document the non-secret nature in code comments. |
| V7 Error Handling | YES | `coerceToolCall` returning `false` on parse failure preserves text (no information leak); `pickBestTool` zero-score also preserves text. No raw error strings exposed to clients. |
| V8 Data Protection | No new exposure | Tool-args may contain sensitive text (user prompts); rendered through same logging discipline as Phase 5 (truncateForLog pattern) |

### Known Threat Patterns for Go/JSON/HTTP stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Malicious tool-args injection (e.g., kiro returns oversized JSON in `tool_call.args`) | DoS | `encoding/json` decoder has no inherent size limit but `chi`'s body limit middleware applies to the inbound side. Outbound (kiro → gateway) is bounded by JSON-RPC stdio line size in `internal/acp/framer.go`. Verify the existing framer line cap is reasonable (≥1MB). |
| JSON injection via assistant-text in coerce path | Tampering | `json.Unmarshal` rejects malformed JSON cleanly; result is `map[string]any` (not eval'd code); only keys are matched against tool-spec property names (string comparison, no injection vector). |
| Fence-strip regex catastrophic backtracking | DoS | Use simple `strings.HasPrefix`/`HasSuffix` chains (Pattern in Don't Hand-Roll) instead of complex regex; if regex is used, keep it linear-time (no nested quantifiers). |
| Synthetic ID collision allowing tool_call confusion | Spoofing | `call_<unix-nano>` collides at sub-microsecond rates on parallel requests; Phase 6 emits one tool_call per request and the IDs are scoped per-response, so cross-response collision has no security impact. If a request emits multiple tool_calls (deferred per CONTEXT `<deferred>` "OpenAI parallel_tool_calls"), revisit. |
| `[Available tools]` JSON catalog leaks tool descriptions to kiro logs | Information disclosure | Tool descriptions are client-supplied AND kiro is the intended consumer — no leak. Logs at `slog` level should NOT echo `req.Tools` content (verify in plan-phase via grep). |
| Untrusted args mutation in pickBestTool | Tampering | `pickBestTool` reads `spec.Parameters["properties"]` map keys; if a malicious tool-spec includes an `__proto__` or similar key, Go maps are safe (no prototype chain). N/A for Go. |

**Phase-specific security note:** `coerceToolCall` operates on response data (kiro → gateway → client), not request data. The attacker model is "kiro emits malicious JSON-as-text in assistant content" — but kiro is already trusted to invoke the LLM, so this is not a new trust boundary. The mitigations above are defensive-in-depth.

## Sources

### Primary (HIGH confidence)
- `docs/reference/acp_wire_shapes.md` §"tool_call / tool_call_chunk", §"tool_call_update" — authoritative ACP wire shapes for D-03 / D-04 [VERIFIED: in-tree, lines 344-380]
- `docs/reference/acp_server_node_reference.md` §"Load-bearing weirdness: `coerceToolCall`" lines 166-195 — algorithm narrative for D-09 [VERIFIED: in-tree]
- `docs/reference/acp_server_node_reference.md` §"Bracketed sections" lines 145-160 — `[Available tools]` content format for D-16 [VERIFIED: in-tree]
- https://platform.claude.com/docs/en/api/messages-streaming — Anthropic SSE event sequence including tool_use streaming (lines 416-425 + 1093-1117 of fetched content) [VERIFIED: WebFetch 2026-05-26]
- `internal/canonical/chunk.go` — `ToolCallChunk` current shape (lines 47-53) [VERIFIED: in-tree Read]
- `internal/canonical/chat.go` — dormant `Tools`, `ToolChoice`, `ToolCall`, `ToolSpec`, `ToolUsePart`, `ToolResultPart` types (lines 73, 145, 183-241) [VERIFIED: in-tree Read]
- `internal/engine/build_acp.go` — placeholder `[Available tools]` at lines 64-69 [VERIFIED: in-tree Read]
- `internal/engine/collect.go` — intentionally-dropped ChunkKindToolCall at lines 67-70 [VERIFIED: in-tree Read]
- `internal/acp/translate.go` — Phase 1.1 tool_call placeholder at lines 235-242 [VERIFIED: in-tree Read]
- `internal/engine/pickcwd_test.go` — `testing/quick` precedent [VERIFIED: in-tree Read, lines 204-240]
- `internal/adapter/anthropic/sse.go` — applyChunk state machine at lines 204-220 [VERIFIED: in-tree Read]
- `internal/adapter/openai/sse.go` — applyChunk drop path at lines 110-116 [VERIFIED: in-tree Read]
- `internal/adapter/anthropic/render.go:113-131` — tool_use outbound render (CR-01 fix preserved) [VERIFIED: in-tree Read]
- `internal/adapter/anthropic/wire.go:242` — `TODO(Phase 6)` for tool spec translation [VERIFIED: in-tree Read]
- `internal/adapter/openai/wire.go:38` — `Tools json.RawMessage` accept-and-ignore [VERIFIED: in-tree Read]
- `tests/e2e/anthropic_e2e_test.go` — existing E2E pattern for shared-helpers reuse [VERIFIED: in-tree Read]
- `docs/briefs/go_port_brief.md` § 1.4 + 3.12 + 3.13 — coerceToolCall load-bearing description + trust gates + adapter-over-canonical (CONTEXT-cited; not re-fetched but trusted per CONTEXT canonical_refs)
- `.planning/config.json` — `workflow.nyquist_validation: true` confirmed [VERIFIED: in-tree cat]
- `go.mod` — `go.uber.org/goleak v1.3.0` confirmed via 21+ in-tree imports [VERIFIED: codebase grep]

### Secondary (MEDIUM confidence)
- WebSearch — OpenAI streaming tool_calls multi-frame pattern (id+name → arguments) confirmed via OpenAI's own examples [WebSearch 2026-05-26]: https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events

### Tertiary (LOW confidence — flagged for validation)
- The actual `acp-ollama-server.js` Node source bytes (parent directory does NOT exist on this machine; algorithm verified against narrative reference only) — **Assumption A1 in Assumptions Log**

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — every package verified in-tree (`go.mod` + codebase grep); zero new deps
- Architecture patterns: HIGH — every change point grounded in existing files I read directly
- Pitfalls: HIGH — 8 specific pitfalls, each with a warning-sign + how-to-avoid; multiple sourced from Phase 3.1 CR-01 + Phase 4 D-08 + Phase 5 dead-slot discipline
- Code examples: HIGH for Anthropic + OpenAI streaming (verified against official specs/examples); MEDIUM-HIGH for `coerceToolCall` algorithm (verified against narrative — Node source unavailable, Assumption A1)
- E2E test patterns: HIGH — existing `tests/e2e/` infrastructure verified by reading `anthropic_e2e_test.go` and confirming helper-sharing pattern

**Research date:** 2026-05-26
**Valid until:** 2026-06-25 (30 days — Phase 6 is a stable internal-API activation, not fast-moving framework-following)
