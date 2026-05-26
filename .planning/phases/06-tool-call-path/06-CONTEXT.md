# Phase 6: Tool-Call Path - Context

**Gathered:** 2026-05-26
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 6 lights up tool calling **end-to-end across all three surfaces**,
activating the canonical type seams already in place from Phase 2 / 3 / 3.1
and replacing the Phase 1.1 placeholder ACP translation. The deliverable is
correct round-trips in both directions for tool calls — kiro-native AND
the load-bearing `coerceToolCall` LangChain-compat fallback — in each
surface's native wire shape.

**Net-new work in Phase 6:**

1. **`coerceToolCall` (engine helper, opt-in per surface).** Lives in
   `internal/engine/coerce.go`. Detects bare JSON / markdown-fenced JSON
   in assistant text, scores tools via property-overlap (`pickBestTool`),
   replaces text with a synthetic `tool_calls` entry. Invoked by Ollama
   and OpenAI adapters; **NOT** invoked by Anthropic (D-01).
2. **ACP `tool_call` notification → real `canonical.ToolCallChunk`.** Today
   `internal/acp/translate.go:235-242` collapses tool_call notifications to
   `ChunkKindThought` with `[tool: <title>]\n` text — Phase 1.1 placeholder.
   Phase 6 extracts `toolCallId` + `title` + `args` into a proper
   `ToolCallChunk` (canonical type already exists; only the translator
   changes).
3. **`engine.Collect` aggregates `ChunkKindToolCall`** into
   `canonical.ChatResponse.Message.ToolCalls`. Today
   `internal/engine/collect.go:67` intentionally drops them; Phase 6
   wires them through (next to text + thinking aggregators).
4. **Per-surface tool_calls rendering — non-streaming.**
   - **Ollama**: `message.tool_calls[].function.arguments` as plain
     **object** (existing `ollamaToolCall` wire type already correct;
     Phase 2 `chatResponseToWire` doesn't yet populate from
     `Message.ToolCalls`).
   - **OpenAI**: `choices[].message.tool_calls[].function.arguments` as
     **JSON string** (encoded from canonical `map[string]any`).
   - **Anthropic**: native `tool_use` content block with object `input`
     already shipped (Phase 3.1 D-Render + CR-01 fix); Phase 6 verifies
     the path is reachable now that engine.Collect populates ToolUse
     content parts from `Message.ToolCalls` extraction.
5. **Per-surface tool_calls rendering — streaming (atomic event sequences).**
   - **Ollama NDJSON**: final `done:true` line carries
     `message.tool_calls[]` populated (Node parity — atomic on the final
     line, not per-delta).
   - **OpenAI SSE**: one `data:` frame with `choices[].delta.tool_calls[]`
     carrying `id`, `function.name`, full `function.arguments` (JSON
     string); final chunk emits `finish_reason: "tool_calls"`.
   - **Anthropic SSE**: `content_block_start` (tool_use header — id, name,
     input `{}`) → one `content_block_delta` with `input_json_delta`
     carrying the serialized args JSON → `content_block_stop`. The
     `@anthropic-ai/sdk` `MessageStream` parser sees a well-formed
     tool_use sequence and surfaces the block correctly to loop24-client.
6. **Tool-spec normalization** — fill in the per-surface decoders:
   - **Ollama**: already maps `ollamaToolSpec` → `canonical.ToolSpec`
     (Phase 2 forward seam complete; no change).
   - **OpenAI**: today `tools` is `json.RawMessage` and dropped. Phase 6
     decodes `tools[].function` → `canonical.ToolSpec`.
   - **Anthropic**: today `anthropicToolSpec` is decoded for shape-safety
     but never translated to canonical (`TODO(Phase 6)` in
     `internal/adapter/anthropic/wire.go:205`). Phase 6 closes that TODO.
7. **Engine `[Available tools]` bracketed section content** —
   `internal/engine/build_acp.go:64-69` emits a placeholder header only.
   Phase 6 emits the full JSON tool catalog inside the section so kiro-cli
   sees the contract (Node ref `acp_server_node_reference.md` §
   "Bracketed sections" line 159).

**Requirements covered (per ROADMAP.md):** TOOL-01, TOOL-02, TOOL-03.

**Out of scope (locked):**

- **Tool dispatch / execution.** kiro-cli auto-grants permissions and
  runs tools internally (Phase 1 D-04 / Node-parity behavior). The
  gateway does not call tools or maintain a tool registry of its own.
- **Full agent-loop transparency.** kiro's `tool_call_update`
  notifications (intermediate tool output) continue to render as thought
  text (D-04). The gateway is not a tool-loop participant for the kiro
  side; it IS a tool-loop participant for the `coerceToolCall` side
  (where the model emitted JSON the client must execute).
- **Per-tool budget / audit hooks** observing `ChunkKindToolCall` —
  Phase 8. Phase 6 populates the canonical chunk fully so the hook seam
  has structured data to observe.
- **True incremental tool_call streaming** (char-by-char `input_json_delta`
  deltas from a model that progressively emits JSON) — kiro-cli does not
  emit partial args; defer until a backend actually streams them.
- **`/api/generate` tools[] support** — today
  `wireGenerateToChatRequest` doesn't carry tools[]. LangFlow uses
  `/api/chat`. Defer unless a real client needs it.
- **Embeddings** — Phase 7.
- **Real hook chain implementations** (Auth/Audit/Budget hooks) — Phase 8.

</domain>

<decisions>
## Implementation Decisions

### Where `coerceToolCall` lives

- **D-01: Engine helper, adapter-invoked.** `internal/engine/coerce.go`
  exports `CoerceToolCall(req *canonical.ChatRequest, resp *canonical.ChatResponse) bool`.
  One canonical-typed implementation. Ollama and OpenAI handlers call it
  immediately after `engine.Collect` returns (before per-surface render).
  **Anthropic does NOT call it** — its native `tool_use` block path is
  wire-fluent and Anthropic-native clients (`@anthropic-ai/sdk` /
  loop24-client) do not emit JSON-as-text the way LangChain does on the
  Ollama path. Running coerce on Anthropic would silently rewrite
  assistant text into `tool_use` blocks, surprising the client.
  - **Why not engine-post-stream (in `Collect` itself):** would force
    the rewrite on Anthropic too.
  - **Why not full adapter-side (duplicated algorithm):** the scoring
    + fence-stripping + best-tool selection is non-trivial; duplicating
    invites drift, and silent drift between LangChain code paths is the
    exact failure mode `coerceToolCall` was written to prevent.
  - **Returns bool** so adapters can debug-log when a coerce fired
    (Node has a `coerce` tag in its access log; we mirror it).

- **D-02: `CoerceToolCall` is idempotent.** Running it twice on the same
  response is the same as running it once: a non-empty `Message.ToolCalls`
  short-circuits to no-op. Defensive — protects against double-invocation
  during a refactor and is a property-test invariant (D-12).

### kiro-cli tool-execution surfacing (the counter-intuitive split)

- **D-03: kiro-native `tool_call` notification → fully-populated
  `canonical.ToolCallChunk`, rendered as `[tool: <name>]\n` thought
  text on the wire.** Two layers, two purposes:
  1. **Canonical extraction (Phase 6 NEW):**
     `internal/acp/translate.go` decodes `toolCallId` + `title` + `args`
     into a real `canonical.ToolCallChunk{Name, Args}` (the `ID` field
     extension follows D-08). Closes the Phase 1.1 placeholder TODO.
  2. **Wire rendering (Node parity):** Each per-surface SSE/NDJSON
     emitter consumes the `ChunkKindToolCall` and renders it as a
     `[tool: <name>]\n` thought-text delta. **Same behavior as Node.**

- **D-04: `tool_call_update` (kiro's intermediate tool output) stays as
  thought text.** No change from Phase 1.1 behavior (`output ?? content`
  rendered as thought). **kiro auto-grants permissions and runs the
  tool internally** — by the time the gateway sees the result, the
  agent loop has already happened. There's no meaningful tool_result
  block to emit (the client can't respond to it; kiro already used
  it). Synthesizing a tool_result + user-turn would fake wire shape
  the client never sent.

- **D-05: `coerceToolCall` synthesis → real `Message.ToolCalls[]` entry,
  rendered in each surface's native shape (the OTHER path).** When the
  model returns plain JSON in assistant text and Ollama/OpenAI invoke
  coerce (D-01), the resulting tool_call IS surfaced as a real
  tool_calls entry — the client must execute it. This is the
  load-bearing LangChain-compat behavior.

**The two paths are intentionally different.** D-03/D-04 = "kiro ran
the tool, we narrate." D-05 = "the model wants the client to run a
tool, we surface it." Same canonical type populates differently from
two different sources.

### Tool-call streaming (per-surface atomic emission)

- **D-06: kiro emits complete args atomically; gateway renders the
  full SDK-expected event sequence in one logical burst.** No
  synthesized char-deltas (there's no incremental source).

- **D-07: Per-surface streaming shape:**
  - **Anthropic SSE**:
    ```
    event: content_block_start
    data: {"type":"content_block_start","index":N,
           "content_block":{"type":"tool_use","id":"...","name":"...","input":{}}}

    event: content_block_delta
    data: {"type":"content_block_delta","index":N,
           "delta":{"type":"input_json_delta","partial_json":"<full-args-json>"}}

    event: content_block_stop
    data: {"type":"content_block_stop","index":N}
    ```
    Single `input_json_delta` carrying the entire serialized args. The
    `@anthropic-ai/sdk` MessageStream parser accumulates deltas into the
    final input map — one-shot delta is wire-correct and parser-safe.
    Block index increments per the Phase 3.1 D-04 state machine.
  - **OpenAI SSE**: one `data:` frame with
    `choices[].delta.tool_calls[]` carrying `{index, id, type:"function",
    function:{name, arguments:"<json-string>"}}`. Final pre-`[DONE]`
    chunk: `delta:{}, finish_reason:"tool_calls"`.
  - **Ollama NDJSON**: final `done:true` line carries
    `message.tool_calls[]` populated alongside the stats block. **Atomic
    on the final line, not per-delta** (Node parity — Node's
    `chunksToOllamaMessage` aggregates and emits on done).

- **D-08: Canonical `ToolCallChunk` gains an `ID string` field.**
  Required because all three wire shapes need an ID and kiro provides
  `toolCallId` on the ACP notification. Today
  `canonical.ToolCallChunk{Name, Args}` would lose it. Add the field;
  D-11 from Phase 2 still holds (canonical has no JSON tags).
  - **`coerceToolCall` synthesizes the ID** when the source is text-not-kiro
    (D-13): `call_<unix-nano>` (mirrors the Phase 2 `chatcmpl-<nano>`
    pattern).

### `coerceToolCall` algorithm fidelity

- **D-09: Strict Node parity. Order-of-checks locked:**
  1. Skip if `len(req.Tools) == 0` OR `len(resp.Message.ToolCalls) > 0`.
  2. Extract assistant-text from `resp.Message.Content` (single
     ContentKindText part — Phase 6 always has at most one per
     `collect.go` aggregation).
  3. Try `json.Unmarshal` on the raw text.
  4. If fail, look for markdown-fenced JSON (` ```json … ``` ` OR bare
     ` ``` … ``` `), strip fences, retry parse.
  5. If still fail, return false. Text preserved.
  6. `pickBestTool`: for each `ToolSpec`, count how many keys in
     `parsed.(map[string]any)` appear as **top-level** keys of
     `spec.Parameters["properties"]`. Pick the highest scorer; ties
     broken by **first-declared in `req.Tools` order**.
  7. If the top score is zero → return false. Text preserved. (Node
     behavior — better than firing the wrong tool.)
  8. Replace `Content[0].Text` with `""`; append synthetic `ToolCall`
     entry to `resp.Message.ToolCalls` with synthesized ID (D-08).
  9. Return true.

- **D-10: Edge-case decisions, locked:**
  - **Tie-breaker**: first-declared in `req.Tools` wins. Deterministic
    + Node parity.
  - **Schema nesting**: top-level keys only. Match Node's
    `Object.keys(spec.parameters.properties)` shallow check.
  - **Markdown fences**: support both ` ```json … ``` ` and bare
    ` ``` … ``` ` fences. Strip CRLF too.
  - **Empty parsed object** (`{}`): score = 0 against any non-empty
    schema; coerce returns false. Bare `{}` in text is not a tool call.
  - **Parsed value is not an object** (array, scalar, null): return
    false. Tools have object schemas.
  - **Tool with empty/missing `parameters.properties`**: skip in
    scoring (can't score against unknown shape).

- **D-11: Synthetic tool_call IDs use `call_<unix-nano>` format.**
  Mirrors the Phase 2 `chatcmpl-<unix-nano>` and Phase 3.1 `msg_01<hex>`
  patterns. Opaque to clients. Real-kiro tool_calls keep their
  `toolCallId` verbatim (D-08).

- **D-12: Property tests via `testing/quick`** (TRST-06; precedent:
  `internal/engine/pickcwd_test.go`). Invariants:
  - **Never-panic** for any `(req, resp)` shape including nil pointers.
  - **Always-terminate** (no infinite scoring loop on pathological
    schemas).
  - **Idempotent**: `Coerce(Coerce(x)) == Coerce(x)`.
  - **Round-trip**: synthesized tool_call → adapter render → re-decode
    preserves name + args (per-surface).
  - **No-match no-mutation**: when `pickBestTool` scores all zero, the
    response is bit-identical to input.

### Tools normalization decoders (per surface)

- **D-13: OpenAI `tools[].function.{name, description, parameters}` →
  `canonical.ToolSpec`.** Today `chatCompletionRequest.Tools` is
  `json.RawMessage` (accept-and-ignore). Phase 6 unmarshals into a typed
  `openAIToolSpec` and populates `req.Tools`. `tool_choice` field
  populates `req.ToolChoice` (Phase 2 forward seam already exists).

- **D-14: Anthropic `tools[].{name, description, input_schema}` →
  `canonical.ToolSpec`.** Closes the `TODO(Phase 6)` in
  `internal/adapter/anthropic/wire.go:205`. Anthropic's `input_schema`
  maps directly to `ToolSpec.Parameters`. Anthropic's `tool_choice`
  shape (`{type:"auto"|"any"|"tool", name?}`) maps to
  `canonical.ToolChoice`.

- **D-15: Ollama already decodes `tools[]`** (Phase 2 forward seam at
  `internal/adapter/ollama/wire.go:321-330`). No change.

### Engine `[Available tools]` bracketed section

- **D-16: Emit the full tool catalog as JSON inside `[Available tools]`.**
  Replace the placeholder header at
  `internal/engine/build_acp.go:64-69` with:
  ```
  [Available tools]
  Emit a tool_call ACP notification to invoke any of the registered tools.

  ```json
  [
    {"name": "...", "description": "...", "parameters": {...}}
  ]
  ```

  ```
  Matches the Node reference (`acp_server_node_reference.md` § lines
  155-159 + the load-bearing-weirdness narrative around JSON fences).
  kiro-cli parses this section to decide what tools are available;
  without the actual catalog content, kiro can't tool-call.

### E2E test coverage (load-bearing — three surfaces × success + failure)

- **D-17: Phase 6 ships a full E2E matrix that proves all three surfaces
  handle tools correctly under success AND failure.** Extends the
  existing `tests/e2e/` real-binary harness (Phase 5 / 260524-pee
  pattern). **Matrix (per surface):**

  | # | Scenario | Path | Expected outcome |
  |---|----------|------|------------------|
  | 1 | **Native tool_call, non-streaming** | kiro emits `tool_call` ACP notif | `tool_calls[]` populated in surface's native shape |
  | 2 | **Native tool_call, streaming** | kiro emits `tool_call` ACP notif mid-stream | Atomic event sequence per D-07 (Anthropic content_block_start/delta/stop; OpenAI delta.tool_calls + finish_reason=tool_calls; Ollama tool_calls on done line) |
  | 3 | **Coerce from bare JSON** (Ollama + OpenAI only) | Model returns `{"location":"Boston"}` as text; tools[] has `get_weather` | Synthetic `tool_calls[]` entry; `content` empty; per-surface arg-shape (OpenAI JSON-string, Ollama object) |
  | 4 | **Coerce from fenced JSON** (Ollama + OpenAI only) | Model returns ` ```json\n{...}\n``` ` | Same as #3; fence stripped |
  | 5 | **Anthropic does NOT coerce** | Model returns bare JSON via Anthropic surface | Text preserved verbatim; NO `tool_use` block synthesized (D-01 verification) |
  | 6 | **Empty tools[] — coerce no-op** | Request has no `tools` field; model emits JSON text | Text preserved; no `tool_calls` field |
  | 7 | **No-match (zero overlap) — coerce no-op** | Bare JSON with keys matching no tool | Text preserved; no `tool_calls` field |
  | 8 | **Malformed JSON in text** | Truncated/invalid JSON | Text preserved; no synthesis |
  | 9 | **Existing tool_calls — coerce skipped** | kiro emitted native tool_call AND model also has JSON-like text | tool_calls from kiro preserved; coerce no-ops |
  | 10 | **Multi-tool tie-breaker** | Two tools score equally | First-declared in tools[] wins (deterministic assertion) |
  | 11 | **Tool with empty parameters** | Tool declared with no schema | Skipped in scoring; no crash; rest of matrix passes |
  | 12 | **Mid-stream disconnect during tool_call** | Client drops mid-stream | Phase 4 D-06 watchdog cancels session; slot survives (Phase 5 dead-slot); no goroutine leak (goleak gate) |

  **Cross-surface assertions:**
  - All three surfaces produce **the same `canonical.ChatResponse`** for
    identical input (modulo `model` field). Property test plus
    snapshot-based E2E.
  - Wire-shape divergence is **only** at the adapter render layer:
    OpenAI JSON-string args vs Ollama object args vs Anthropic
    tool_use block.

- **D-18: E2E real-binary boot pattern.** Reuse the `tests/e2e/`
  harness from quick-task 260524-pee: spawn the otto-gateway binary +
  real `kiro-cli`, POST against the live port, assert response shape
  and headers. **No fake-engine for E2E** — fake-engine work belongs
  to unit tests under `internal/adapter/*/`.

- **D-19: Tests grouped by surface, then scenario.** File layout:
  ```
  tests/e2e/
    tools_ollama_test.go      # scenarios 1-4, 6-11
    tools_openai_test.go      # scenarios 1-4, 6-11
    tools_anthropic_test.go   # scenarios 1, 2, 5, 9
    tools_cancel_test.go      # scenario 12 (per-surface subtests)
  ```
  Each scenario uses a controllable fake `kiro-cli` (via `KIRO_CMD`
  env override) that scripts the ACP notifications needed. The
  real-kiro round-trip stays as a smaller "smoke" subset (scenarios
  1 + 5 per surface) on a build tag (Phase 5 e2e precedent).

- **D-20: Property tests under `internal/engine/coerce_test.go`** (per
  SC #5). Already covered by D-12 invariants. Distinct from D-17/D-19
  E2E — these are unit/property-level on the algorithm.

- **D-21: Goroutine-leak gate on E2E tests.** Each E2E test wraps in
  `goleak.VerifyNone(t)` (Phase 4 D-06 + Phase 5 discipline). Tool-call
  streaming + cancel paths add new goroutine surface; the gate is the
  guard.

### Claude's Discretion

The planner/researcher have latitude on:

- Exact file split: whether `coerceToolCall` lives in
  `internal/engine/coerce.go` (recommended) or as a private helper inside
  `collect.go`. The locked contract is "shared, canonical-typed,
  idempotent, returns bool."
- Whether the synthetic-coerced ID uses `call_<unix-nano>`,
  `call_<random-hex>`, or `toolu_<hex>` (Anthropic-style). Pick one and
  document it in the plan. **`call_` prefix preferred** to match OpenAI's
  convention (since OpenAI is the primary coerce consumer alongside
  Ollama).
- The exact `pickBestTool` scoring tie-breaker for tools with identical
  property overlap counts AND identical `properties` keys: first-declared
  is the locked decision (D-10), but if planner finds Node uses a more
  specific algorithm (e.g., total property count as a secondary), match
  Node.
- Whether the OpenAI streaming `tool_calls[]` delta also emits a separate
  `delta.tool_calls[]` chunk to start (with `id` + `function.name`) and
  a second chunk with `arguments`, or single combined frame. The
  `@openai-sdk` parser accepts either; pick the cleaner Go path.
- Whether Anthropic's tool_use streaming block emits the `content_block_start`
  with `input: {}` (empty object) vs `input: null`. Match `@anthropic-ai/sdk`
  SDK expectations; if both work, pick `{}` (matches the non-streaming
  Phase 3.1 render path).
- Test data: synthetic tool schemas can be `get_weather` /
  `read_file` / `search_web` (LangChain canonical examples) or
  project-specific. Stay consistent across the three surface test files
  for diff readability.
- Whether the kiro-`tool_call` thought-text rendering uses
  `[tool: <name>]\n` (Phase 1.1 + Node parity) or grows to
  `[tool: <name>(args)]\n` for operator-DX. D-03 locks the wire shape
  but the planner may opt for the richer form if it adds no real cost.

### Folded Todos

No todos were folded. The cross-reference returned one weak match
(`perf-baseline-vs-node.md`, score 0.6 on keywords "node" + "phase") —
reviewed and deferred (it's a milestone perf-gate, not Phase 6 work).
See Reviewed Todos in `<deferred>`.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project context (must-read)

- `.planning/PROJECT.md` — Core value ("one place to enforce policy"),
  constraints, Key Decisions table. The "coerceToolCall load-bearing"
  framing is rooted in this doc.
- `.planning/REQUIREMENTS.md` — TOOL-01, TOOL-02, TOOL-03 are the
  phase's requirement set. SURF-01..08 set the broader surface contracts
  that the per-surface tool_call rendering must satisfy.
- `.planning/ROADMAP.md` § "Phase 6: Tool-Call Path" — goal + 5 success
  criteria. SC #1/#2 lock the JSON-string vs object argument shape
  contract; SC #3 locks `coerceToolCall`; SC #4 locks tool spec
  normalization; SC #5 locks the property-test discipline.

### Closest prior-phase analogs (must-read)

- `.planning/phases/03.1-anthropic-surface/03.1-CONTEXT.md` — D-09
  (tool_result.content normalization), D-11 (inbound `thinking` block
  preservation), the **`tool_use` outbound render path** (which Phase 6
  newly populates via `Message.ToolCalls` → `ToolUsePart` extraction in
  collect.go), and the CR-01 fix on `Input *map[string]any` pointer that
  preserves "empty input but field present" through encoding/json
  omitempty. Phase 6 must not break that fix.
- `.planning/phases/02-ollama-end-to-end/02-CONTEXT.md` — D-08/D-09/D-10
  canonical types (all the dormant Phase 6 fields), D-02 `buildBlocks`
  bracketed sections (the `[Available tools]` placeholder Phase 6 fills
  per D-16), and the adapter-over-canonical layout invariant.
- `.planning/phases/04-streaming/04-CONTEXT.md` — D-06 engine-owned
  watchdog (Phase 6 mid-stream cancel path inherits this), D-08
  three-emitter no-shared-driver pattern (Phase 6 adds tool_call
  rendering to each emitter without abstracting), Deferred Ideas
  pointer "Tool-call streaming (`input_json_delta` deltas) — Phase 6"
  which this CONTEXT cashes via D-06/D-07.
- `.planning/phases/05-pool-stateful-sessions/05-CONTEXT.md` —
  Slot-survives-cancel discipline (D-01..D-03 dead-slot detection)
  that scenario 12 of the E2E matrix (D-17) exercises.
- `.planning/phases/01.1-acp-wire-alignment/01.1-CONTEXT.md` — Phase 1.1
  intentionally left `tool_call` translation as a placeholder thought.
  Phase 6 closes that loop.

### Spec of record (must-read)

- `docs/briefs/go_port_brief.md` § 1.4 "Request lifecycle" (line 133-138)
  — the `coerceToolCall` load-bearing description: *"This must be
  preserved in the Go port. It exists because LangChain agents rely on
  JSON-as-tool-call. Removing it silently breaks real users."*
- `docs/briefs/go_port_brief.md` § 1.4 "Things that must survive the
  port" (line 179-181) — `coerceToolCall` markdown-fence stripping +
  property-overlap best-tool scoring contract.
- `docs/briefs/go_port_brief.md` § 1.6 (line 266-273) — per-surface
  tool-call argument-shape contract: OpenAI JSON-string vs Ollama
  object vs Anthropic object (different field path). D-07 streaming
  shapes derive from this section.
- `docs/briefs/go_port_brief.md` § 3.12 trust gates — `golangci-lint`
  strict, `gosec` G204 on subprocess paths, `goleak` (D-21), property
  tests via `testing/quick` (D-12).
- `docs/briefs/go_port_brief.md` § 3.13 adapter-over-canonical — Phase
  6 activates the canonical type seams without churning the canonical
  type signatures (modulo the `ToolCallChunk.ID` addition in D-08).

### Behavioral parity (must-read)

- `docs/reference/acp_server_node_reference.md` § "Load-bearing
  weirdness: `coerceToolCall`" (lines 166-195) — algorithm spec,
  pickBestTool scoring narrative, the commit-log receipts (`0ead935`,
  `7569745`, `995c569`). D-09 / D-10 are the Go translation.
- `docs/reference/acp_server_node_reference.md` § "Bracketed sections"
  (around lines 155-159) — `[Available tools]` content format that
  D-16 emits.
- `docs/reference/acp_server_node_reference.md` § "Things that are
  easy to get wrong" (lines 289-290) — "Don't touch `coerceToolCall`
  without a test plan." D-17 IS the test plan.
- `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-ollama-server.js` —
  the Node source itself. Read `coerceToolCall` and `pickBestTool`
  function bodies for ground truth. Where Node's code and the
  narrative doc disagree, **the Node code wins**.
- `docs/reference/acp_wire_shapes.md` § "tool_call / tool_call_chunk"
  + "tool_call_update" — authoritative ACP wire shape for the
  notifications D-03 / D-04 consume. `toolCallId` + `title` + `args`
  is the source schema for D-08's canonical extension.

### Wire spec parity (must-read for D-07 streaming)

- https://docs.anthropic.com/en/api/messages-streaming —
  `content_block_start` / `content_block_delta` / `content_block_stop`
  event sequence for `tool_use` blocks. The `input_json_delta` delta
  type is what D-07 emits. **`@anthropic-ai/sdk` MessageStream parser
  is the conformance test** (loop24-client uses it).
- https://platform.openai.com/docs/api-reference/chat-streaming —
  `choices[].delta.tool_calls[]` shape. `finish_reason:"tool_calls"`
  is the terminal signal.
- Ollama: no formal streaming-tool-call spec exists. **Node reference
  is the ground truth** — `chunksToOllamaMessage` aggregates and emits
  tool_calls on the `done:true` line.

### Reference architecture (read as needed)

- `~/Projects/repos/local/bifrost/core/providers/openai/openai.go`
  + `~/Projects/repos/local/bifrost/transports/bifrost-http/integrations/openai/`
  — Bifrost's `tool_calls[].function.arguments` JSON-string render
  pattern. Cross-check our OpenAI render path against theirs.
- `~/Projects/repos/local/bifrost/core/providers/anthropic/anthropic.go`
  — Bifrost's `content_block_start`/`input_json_delta` SSE emission for
  `tool_use` blocks. Cross-check our Anthropic SSE emitter.
- `~/Projects/repos/local/loop24-client/packages/pi-ai/src/providers/anthropic.ts`
  — loop24-client's `messages.stream()` consumer. The conformance
  target for D-07 Anthropic streaming.

### Existing code (must-read)

- `internal/canonical/chat.go` (lines 73, 145, 183-241) — dormant
  `Tools`, `ToolChoice`, `ToolCall`, `ToolSpec`, `ToolUsePart`,
  `ToolResultPart` types. Phase 6 activates without changing
  signatures (except `ToolCallChunk.ID` per D-08).
- `internal/canonical/chunk.go:47-53` — `ToolCallChunk`. Phase 6 adds
  `ID string` (D-08).
- `internal/engine/build_acp.go:64-69` — placeholder `[Available tools]`.
  D-16 fills.
- `internal/engine/collect.go:67-70` — intentionally-dropped
  `ChunkKindToolCall`. Phase 6 wires the aggregation.
- `internal/acp/translate.go:235-242` — `tool_call`/`tool_call_chunk`
  → thought-text placeholder. D-03 replaces with real `ToolCallChunk`
  extraction.
- `internal/adapter/ollama/wire.go:60-67` (`ollamaToolCall` /
  `ollamaToolCallFunction`) + `render.go` — output rendering target
  for D-04 (Ollama object args).
- `internal/adapter/openai/wire.go:38` — `Tools json.RawMessage`
  accept-and-ignore. D-13 replaces with typed decode.
- `internal/adapter/anthropic/wire.go:202-206` — `TODO(Phase 6)`
  comment to close (D-14).
- `internal/adapter/anthropic/render.go:113-131` — `tool_use` outbound
  render (already shipped + CR-01 fix). Phase 6 must produce
  `ToolUse` content parts in the canonical response so this code path
  is reachable.
- `internal/adapter/anthropic/sse.go:204-220` — tool_use block path
  in the SSE applyChunk state machine (currently dropped). D-07
  Anthropic implementation lives here.
- `internal/adapter/openai/sse.go:110-116` — tool_call path in OpenAI
  SSE applyChunk (currently dropped). D-07 OpenAI implementation
  lives here.
- `internal/adapter/ollama/ndjson.go` — final `done:true` line
  composer. D-07 Ollama implementation extends the existing
  `chatResponseToWire` pattern from `render.go`.
- `tests/e2e/` (from quick-tasks 260524-pee + 260524-pyd) — real-binary
  harness pattern. D-18 + D-19 extend.
- `internal/engine/pickcwd_test.go` — `testing/quick` property test
  precedent. D-12 + D-20 mirror.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- **All canonical tool types already exist** (Phase 2 D-08/D-09/D-10
  forward design): `Tools []ToolSpec`, `ToolChoice *ToolChoice`,
  `Message.ToolCalls []ToolCall`, `ContentKindToolUse`,
  `ContentKindToolResult`, `ToolUsePart{ID, Name, Input}`,
  `ToolResultPart{ToolUseID, Content, IsError}`,
  `ToolCallChunk{Name, Args}` (+ `ID` per D-08). **Zero new types
  except the ID extension.**
- `internal/adapter/anthropic/render.go:113-131` — outbound `tool_use`
  rendering shipped in Phase 3.1 with CR-01 fix (pointer-to-map for
  omitempty preservation of empty-but-present `input` field). Phase 6
  must populate `Message.Content` with `ContentKindToolUse` parts so
  this path actually gets exercised; `Message.ToolCalls` is the
  parallel OpenAI-style shape.
- `internal/adapter/ollama/wire.go:60-67` — `ollamaToolCall` /
  `ollamaToolCallFunction` wire types match Ollama's response shape
  (Phase 2 forward seam). `render.go`'s `chatResponseToWire` adds the
  `tool_calls` field population (D-04).
- `internal/adapter/ollama/wire.go:321-330` — `ollamaToolSpec` →
  `canonical.ToolSpec` decode already in place (Phase 2). No change.
- `internal/engine/collect.go` — aggregator pattern (text + thoughts);
  D-05 adds a third aggregator for `ChunkKindToolCall` (slice of
  `canonical.ToolCall`).
- `internal/engine/pickcwd_test.go` — `testing/quick` property-test
  pattern + Example function. D-12 + D-20 follow exactly.
- `tests/e2e/` harness — real-binary boot + fake-kiro-cli pattern
  (`KIRO_CMD` override). D-17 / D-18 / D-19 extend; the per-scenario
  controllable fake-kiro is the load-bearing test fixture.
- `internal/acp/fakeacp_test.go` — fake ACP server pattern for the
  Anthropic frame-assertion test in Phase 4 D-10. Phase 6 unit tests
  on `translate.go` use the same shape to feed `tool_call`
  notifications.

### Established Patterns

- **Canonical types stay narrow.** D-11 invariant — no JSON tags;
  adapter-side translation only. D-08 `ToolCallChunk.ID` follows
  (just a `string`, no tag).
- **Adapter-over-canonical layout (TRST-04).** No adapter imports
  another adapter; no adapter imports `internal/engine`.
  `internal/engine/coerce.go` is engine-internal; adapters import it
  via `internal/engine` package (Ollama + OpenAI already do, for
  `engine.Run` / `engine.Collect`).
- **`engine.Run` / `engine.Collect` split** (Phase 2 D-01). Phase 6
  changes `Collect` (third aggregator) but adds no new public engine
  methods. Adapters keep their existing call shape.
- **Per-surface SSE/NDJSON emitter independence** (Phase 4 D-08). No
  shared stream driver; each emitter grows tool_call handling locally.
- **Property tests via `testing/quick`** (TRST-06 precedent).
- **`goleak` gate on handler/engine tests** (Phase 1 / Phase 4 / Phase 5
  discipline). D-21 extends to E2E.
- **Auto-grant `session/request_permission`** (Phase 1) — Phase 6
  needs no change here; kiro's tool execution loop continues to flow
  through the auto-grant path, and the tool_call_update output is
  what D-04 narrates as thought text.

### Integration Points

- `internal/canonical/chunk.go` — `ToolCallChunk` gains `ID string`
  (D-08). Plus a doc-comment note that Phase 6 now populates this.
- `internal/acp/translate.go:235-242` — replace placeholder thought
  text with full `canonical.Chunk{Kind: ChunkKindToolCall,
  ToolCall: &ToolCallChunk{ID, Name, Args}}` (D-03).
- `internal/engine/build_acp.go:64-69` — emit JSON tool catalog
  inside `[Available tools]` section (D-16).
- `internal/engine/collect.go:56-70` — add tool_call aggregator
  parallel to text + thoughts. After stream close, populate
  `resp.Message.ToolCalls` AND emit a parallel
  `ContentKindToolUse` part per tool_call so the Anthropic outbound
  render path (already shipped) is reachable.
- `internal/engine/coerce.go` — **new file.** `CoerceToolCall(req, resp) bool`
  + private `pickBestTool` / `stripFences` helpers (D-01, D-09, D-10).
- `internal/adapter/ollama/handlers.go` — invoke
  `engine.CoerceToolCall(req, resp)` after `engine.Collect` (D-01).
- `internal/adapter/ollama/render.go` — populate
  `ollamaChatResponseMessage.ToolCalls` from `resp.Message.ToolCalls`
  (D-04). Plain-object args (no JSON-encoding) per SURF / Node parity.
- `internal/adapter/ollama/ndjson.go` — emit `message.tool_calls` on
  the final `done:true` line (D-07 Ollama).
- `internal/adapter/openai/handlers.go` — invoke `engine.CoerceToolCall`
  (D-01).
- `internal/adapter/openai/wire.go:38` — replace `Tools json.RawMessage`
  with typed `[]openAIToolSpec`; decode into `canonical.ToolSpec`
  (D-13).
- `internal/adapter/openai/render.go` — populate
  `choices[].message.tool_calls[].function.arguments` as **JSON
  string** (use `json.Marshal` on `ToolCall.Arguments map`).
- `internal/adapter/openai/sse.go:110-116` — extend `applyChunk` to
  emit the per-chunk `delta.tool_calls[]` frame; final chunk emits
  `finish_reason:"tool_calls"` (D-07 OpenAI).
- `internal/adapter/anthropic/wire.go:202-206` — close
  `TODO(Phase 6)`; decode `anthropicToolSpec` → `canonical.ToolSpec`
  (D-14).
- `internal/adapter/anthropic/sse.go:204-220` — extend `applyChunk`
  for `ChunkKindToolCall`: emit
  `content_block_start{type:"tool_use",...}` → one
  `content_block_delta{type:"input_json_delta", partial_json: ...}` →
  `content_block_stop`, with block-index advance per Phase 3.1 D-04
  state machine (D-07 Anthropic).
- `tests/e2e/tools_{ollama,openai,anthropic,cancel}_test.go` — **new
  files** for the D-17 matrix.
- `internal/engine/coerce_test.go` — **new file** for D-12 property
  tests.
- `.go-arch-lint.yml` — no change (engine package adds a file but no
  new boundary).

</code_context>

<specifics>
## Specific Ideas

- **The two-path tool-call surfacing rule (D-03/D-04 vs D-05) is the
  most counter-intuitive Phase 6 decision.** Worth a top-line
  PATTERNS.md entry post-execution: "kiro-native tool_call →
  thought-text narration (kiro already ran it); coerce-from-text
  tool_call → real `tool_calls` entry (client must run it)." If a
  future reviewer asks "why do these two paths render differently?",
  the rationale lives in this CONTEXT and the PR description.

- **`@anthropic-ai/sdk` MessageStream is the load-bearing Anthropic
  test fixture.** Real loop24-client streaming against the gateway
  with tools (D-07 Anthropic) is the actual proof D-17 needs. The
  fake-kiro-cli E2E proves the wire shape; a real loop24-client
  round-trip against the binary (HUMAN-UAT or scripted) is the
  conformance vote. Plan should include a quick loop24-client smoke
  in the E2E matrix, gated on the SDK being installed locally (it is).

- **Tool-call argument shape divergence is THE Phase 6 wire-test
  axis.**
  - OpenAI: `arguments` is a **JSON-encoded string** (`"{\"location\":\"Boston\"}"`).
  - Ollama: `arguments` is a **plain object** (`{"location":"Boston"}`).
  - Anthropic: `input` is a **plain object** at a different field path.
  Any cross-surface render code path that produces the wrong shape is
  a Phase 6 regression. E2E scenarios 1-4 of D-17 are the canary.

- **`coerceToolCall` is the load-bearing LangChain test.** Without
  it, LangFlow chains that emit `{"city":"London"}` as plain text
  silently lose tool calls. The Node ref's "commit log receipts"
  (`0ead935`, `7569745`, `995c569`) are the don't-touch warning.
  D-09's order-of-checks must match Node's exactly; if the planner
  finds drift in any edge case (e.g., the regex for fenced blocks),
  match Node and document the divergence with a code comment.

- **`pickBestTool` ties on identical scores are deterministic by
  declaration order.** No alphabetical fallback. If the same client
  declares two tools with the same parameter keys (rare but possible),
  the first wins. Property test verifies this with a fixed-shuffle
  invariant.

- **`canonical.ToolCallChunk.ID` is a Phase 6 type extension, not
  ChatRequest/Response churn** (D-08). The doc comment for the
  field should call out that it's populated from
  `toolCallId` on the ACP wire, OR synthesized by `coerceToolCall`
  when the source is text-not-kiro.

- **Anthropic streaming tool_use block index** (D-07 Anthropic):
  follows the Phase 3.1 D-04 state machine — each ChunkKind change
  is a new block. A tool_use chunk arriving after a text chunk closes
  the text block, opens block N+1 as tool_use. Reuses the existing
  index discipline; no new state machinery.

</specifics>

<deferred>
## Deferred Ideas

- **Full agent-loop transparency (synthetic tool_result + user turn
  for kiro's `tool_call_update`)** — D-04 keeps Node behavior. Add
  if a real client needs the loop surfaced (none does today).
- **True incremental tool_call streaming** (char-by-char
  `input_json_delta`) — kiro doesn't emit partial args. Defer until a
  backend that does emit them appears.
- **`/api/generate` tools[] support** — Phase 2's
  `wireGenerateToChatRequest` doesn't carry `tools`. LangFlow uses
  `/api/chat`. Add if a real client needs it.
- **Per-tool budget / audit hooks** observing `ChunkKindToolCall` —
  Phase 8 hook chain. Phase 6 populates the canonical chunk
  completely so Phase 8 hooks have structured data to observe.
- **Tool-call cancellation mid-execution** (the gateway tells kiro
  to stop a running tool) — kiro-cli doesn't expose a granular
  cancel API beyond `session/cancel`. Whole-session cancel is the
  available primitive. Phase 4 D-06 watchdog covers this; no
  Phase 6 work.
- **Per-tool rate limiting / quotas** — Phase 8 hook chain.
- **OpenAI `parallel_tool_calls` field** (request hint to allow
  multiple tool_calls per assistant turn) — accept-and-ignore today
  (Phase 3 `tool_choice` policy). If a client surfaces a complaint,
  revisit.
- **OpenAI `response_format.json_schema` strict-output mode** — Phase
  6 implements coerce, which is the inverse (turning JSON-as-text
  into a tool_call). Strict JSON output for the regular response
  body is a different concern; Phase 6 D-16 emits `[Output format]`
  section already (Phase 2 D-02 seam).
- **Anthropic `tool_use` block input streaming via multiple
  `input_json_delta` deltas** — D-07 emits one delta with the full
  JSON. Multi-delta streaming requires partial-args from kiro;
  defer until backend supports it.
- **`tool_choice: required` enforcement** (gateway-side validation
  that the model returned a tool call when required) — accept-and-
  ignore; let kiro handle. Revisit if Pi-SDK / LangFlow needs
  enforcement.
- **Visual debug renderer for the tool-call wire matrix** —
  operator-DX nice-to-have; not load-bearing. Phase 9 distribution
  may want a `--dump-tool-call-shapes` flag.

### Reviewed Todos (not folded)

- **perf-baseline-vs-node.md** (score 0.6, weak keyword match on
  "node"+"phase"). It's a milestone-deferral perf-gate (Phase 5
  Accepted-with-Notes carryover), not Phase 6 work. Stays in
  `.planning/todos/pending/` for milestone close.

</deferred>

---

*Phase: 6-Tool-Call Path*
*Context gathered: 2026-05-26*
