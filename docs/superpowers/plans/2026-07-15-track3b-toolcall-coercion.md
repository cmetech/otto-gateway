# Track 3b — tool-call wrapper coercion + surfacing — Implementation Plan

> REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Checkbox steps.

**Goal:** coerce kiro's `{"tool_call":{"name","arguments"}}` wrapper JSON (emitted
in assistant text since Track 3a) into structured tool calls on all 3 surfaces,
JS-parity robust. **Design:** `docs/superpowers/specs/2026-07-15-track3b-toolcall-coercion-design.md`.
**Go map (exact seams):** `.superpowers/sdd/track3b-go-map.md`.
**JS reference:** `/Users/coreyellis/code/gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-server-ollama.js` — `coerceToolCall:1139-1200`, `extractToolCallObjects:975-1050`, `repairJsonControlChars:955-973`, `pickBestTool` (JS).

## Global Constraints
- Go 1.26, no cgo (`CGO_ENABLED=0 go build ./cmd/otto-gateway` passes).
- `CoerceToolCall`'s existing bare-`{args}` behavior + idempotency + never-panics + zero-match-no-op MUST be preserved (locked by `coerce_test.go` property tests).
- **Anthropic anti-forgery invariant preserved:** Anthropic coerces the explicit `{"tool_call":…}` wrapper ONLY, never the ambiguous bare-`{args}` heuristic.
- gofumpt-clean, `go vet` clean, `make arch-lint` clean.
- Commit trailer (verbatim, after a blank line):
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2
  ```
- Do NOT `git push`.

---

### Task 1: greenfield `extractToolCallObjects` + `repairControlChars`

**Files:** `internal/engine/coerce.go` (add two unexported/exported helpers); `internal/engine/extract_test.go` (`package engine`).

**Produces (in `internal/engine`):**
- `func repairControlChars(s string) string` — string-aware: walk chars tracking `inStr`/`esc`; when inside a string value, replace raw `\n`→`\\n`, `\r`→`\\r`, `\t`→`\\t`; outside strings, copy verbatim. (JS `repairJsonControlChars:955-973`.)
- `func extractToolCallObjects(text string) []map[string]any` — for each index of `"tool_call"` in text: find enclosing `{` (`strings.LastIndex(text[:idx], "{")`), string-aware balanced-brace scan (track `inStr`/`esc`/`depth`, remember `lastMeaningful`), to matching `}`. Parse `json.Unmarshal(slice)` then `repairControlChars(slice)`. **Truncation repair:** if it runs off the end with `1 <= depth <= 8` and not `inStr`, slice to `lastMeaningful+1`, append `"}"*depth`, retry parse. Collect parsed objects. (JS `extractToolCallObjects:975-1050` — port the algorithm faithfully.)

- [ ] Step 1 — failing tests `TestExtractToolCallObjects`: whole `{"tool_call":{...}}` → 1 obj; wrapper inside a ```` ```json ```` fence → 1; wrapper embedded in prose ("Here you go: {...} done") → 1; TWO wrappers in one text → 2; truncated (missing trailing `}`) → 1 (repaired); a string value containing `}` and `\n` (raw control char) → 1 (not desynced, control-char repaired); no `"tool_call"` → nil. `TestRepairControlChars`: raw newline inside a string value gets escaped; braces/newlines OUTSIDE strings untouched.
- [ ] Step 2 run `go test ./internal/engine/ -run 'ExtractToolCallObjects|RepairControlChars'` → RED.
- [ ] Step 3 implement. Step 4 GREEN (`-race`). Step 5 `go test ./internal/engine/` full → PASS (no regression to existing coerce tests). Commit `feat(engine): balanced-brace tool_call extractor + control-char repair (Track 3b)`.

---

### Task 2: `ExtractToolCallWrappers` + wrapper tier in `CoerceToolCall`

**Files:** `internal/engine/coerce.go`; `internal/engine/coerce_test.go` (extend the `TestCoerceToolCall_AlgorithmCases` table + new `TestExtractToolCallWrappers`).

**Produces:**
- `func ExtractToolCallWrappers(text string, tools []canonical.ToolSpec) []canonical.ToolCall` (EXPORTED — Anthropic will call it). Strategy (stop at first yielding ≥1): (1) whole content raw-parse, else `StripFences` then parse, as a wrapper object OR `[]` of wrappers; (2) else `extractToolCallObjects(text)`. For each candidate map, `pushWrapper`: `tc, ok := m["tool_call"].(map[string]any)`; need non-empty `name, _ := tc["name"].(string)`; `args` from `tc["arguments"]` (if a `string`, `json.Unmarshal` it; if nil, `map[string]any{}`; must end as `map[string]any`); if `name` isn't in `tools` (by `Name`), remap via `pickBestTool(args, tools)` (drop if nil/zero); append `canonical.ToolCall{ID: "call_"+unixnano, Name, Arguments: args}`. (JS `pushWrapper` 1147-1170.)
- Extend `CoerceToolCall`: after the existing guards + text-part location, FIRST try `wrappers := ExtractToolCallWrappers(text, req.Tools)`; if non-empty → clear the text part, set `resp.Message.ToolCalls = wrappers`, return true. ELSE fall through to the existing bare-`{args}` `pickBestTool` path UNCHANGED.

- [ ] Step 1 — failing tests: extend `TestCoerceToolCall_AlgorithmCases` with rows (all `wantFired:true`, `wantToolName:"get_weather"`): plain wrapper; fenced wrapper; wrapper-in-prose; array-of-wrappers → 2 calls (add an assertion for count); invented-name (`{"tool_call":{"name":"getWeatherNow","arguments":{"city":"Paris"}}}` with only `get_weather` declared) → remapped to `get_weather`; arguments-as-string (`"arguments":"{\"city\":\"Paris\"}"`) → parsed. Keep ALL existing rows green (bare-`{args}`, narration-no-coerce, inline-fenced-in-prose-no-coerce must still behave as before — note: an inline `{"tool_call":…}` in prose SHOULD now coerce (via extractToolCallObjects), which is a deliberate change from the old "inline no coerce" for the wrapper shape; adjust only the wrapper-relevant expectations, NOT the bare-JSON ones). Add `TestExtractToolCallWrappers` unit cases + idempotency/never-panics still green.
- [ ] Step 2 RED → Step 3 implement → Step 4 GREEN (`-race`) → Step 5 `go test ./internal/engine/` PASS. Commit `feat(engine): coerce {"tool_call":…} wrapper into structured tool_calls (Track 3b)`.

> Ollama & OpenAI need NO production change — their 4 existing `CoerceToolCall` call sites (map §2) now recognize the wrapper automatically. Task 3 proves that on the wire.

---

### Task 3: Ollama + OpenAI surfacing tests (verify; likely no prod change)

**Files:** `internal/adapter/ollama/*_test.go`, `internal/adapter/openai/*_test.go`. Production code likely unchanged; if a call site needs a tweak, keep it minimal.

- [ ] Add non-streaming tests: drive a fake engine returning assistant text = a fenced `{"tool_call":{"name":"get_weather","arguments":{"city":"Paris"}}}` with a `get_weather` tool declared; assert Ollama `/api/chat` response has `message.tool_calls[0].function.name=="get_weather"` and `arguments` is a JSON OBJECT with `city:Paris`; assert OpenAI `/v1/chat/completions` has `choices[0].message.tool_calls[0].function.name=="get_weather"`, `arguments` a JSON STRING, `finish_reason=="tool_calls"`. Mirror the existing handler/render tests (map §6).
- [ ] `go test ./internal/adapter/ollama/ ./internal/adapter/openai/` → PASS. Commit `test(ollama,openai): assert {"tool_call"} wrapper surfaces as structured tool_calls (Track 3b)`.

---

### Task 4: Anthropic non-streaming wrapper coercion + anti-forgery guards

**Files:** `internal/adapter/anthropic/collect.go` (`CollectAnthropicChat`/`assembleAnthropicChatResponse`); `internal/adapter/anthropic/handlers_test.go` (update `TestAnthropic_DoesNotCallCoerceToolCall` + `TestAnthropic_NoCoerce_Behavioral`).

- [ ] After the assistant text is assembled (and only when `len(req.Tools) > 0` and no native tool_use was already produced), call `engine.ExtractToolCallWrappers(text, req.Tools)`. For each returned `ToolCall`, append a `canonical.ContentPart{Kind: canonical.ContentKindToolUse, ToolUse: &canonical.ToolUsePart{ID: tc.ID, Name: tc.Name, Input: tc.Arguments}}` to the assembled Content AND a matching `canonical.ToolCall` to `Message.ToolCalls` (map §3 CRITICAL — the renderer reads Content's ToolUse parts; ToolCalls alone vanishes). Clear/replace the coerced wrapper text so it doesn't also render as a text block.
- [ ] Update guards to the NEW contract: `TestAnthropic_NoCoerce_Behavioral` — a bare-JSON assistant response (NO `tool_call` wrapper) must STILL NOT synthesize `tool_use` (anti-forgery preserved); ADD a positive test that an explicit `{"tool_call":…}` wrapper DOES now produce a `tool_use` content block with `stop_reason:"tool_use"`. Update/replace `TestAnthropic_DoesNotCallCoerceToolCall` (static-source string match on `engine.CoerceToolCall`): Anthropic must NOT call `engine.CoerceToolCall` (the ambiguous bare-heuristic) but MAY call `engine.ExtractToolCallWrappers` — adjust the assertion to match `CoerceToolCall` specifically, or replace with a behavioral guard.
- [ ] `go test ./internal/adapter/anthropic/` → PASS. Commit `feat(anthropic): coerce {"tool_call"} wrapper text into tool_use blocks (Track 3b)`.

> Anthropic gets the UNAMBIGUOUS wrapper extractor only — never the bare-`{args}` heuristic. This closes the gap while keeping the "no wire-shape forgery for plain JSON" invariant.

---

### Task 5: Anthropic streaming (SSE) wrapper coercion

**Files:** `internal/adapter/anthropic/sse.go`; SSE golden/sequence tests.

- [ ] At end-of-stream on the buffered-text path (mirror where the native tool_use SSE frames are emitted, map §3 `sse.go:299-490` / `917-1029`), run `engine.ExtractToolCallWrappers(bufferedText, req.Tools)`; if it yields calls, emit the `content_block_start{type:tool_use}` / `input_json_delta` / `content_block_stop` frame sequence per call and set `stop_reason:"tool_use"` on `message_delta`. Match the Anthropic SSE contract (event names, `input` as object). Buffer assistant text that looks tool-call-shaped (starts with `{` or a fence) until end-of-stream so the extractor sees the whole thing (mirror the Ollama/OpenAI buffering design).
- [ ] SSE sequence test asserting the tool_use frames + `stop_reason`. `go test ./internal/adapter/anthropic/` → PASS. Commit `feat(anthropic): stream {"tool_call"} wrapper as tool_use SSE frames (Track 3b)`.

> This is the most intricate task; if the SSE buffering seam is too invasive, report DONE_WITH_CONCERNS and we scope streaming Anthropic coercion as a follow-on (non-streaming Anthropic + both Ollama/OpenAI paths already deliver the core value).

---

### Task 6: live verification + findings

**Files:** `tests/track3b_coercion_test.go` (`//go:build kirolive`); update `docs/reviews/2026-07-14-track0-toolcall-findings.md`.

- [ ] Mirror the track3a harness. Drive a `get_weather` round-trip per surface against a live gateway (real kiro, `ACP_CAPTURE=true`, port 18099, PII-neutralize the `go run` only, kill gateway+kiro children after, never touch 18080). Assert the CLIENT RESPONSE now contains a STRUCTURED tool call per surface: Anthropic `content[].type=="tool_use"` name get_weather; OpenAI `choices[0].message.tool_calls[0].function.name=="get_weather"` + `finish_reason:"tool_calls"`; Ollama `message.tool_calls[0].function.name=="get_weather"`. Report honestly per surface.
- [ ] Append a "Track 3b outcome (live)" section: per-surface structured-tool-call result; whether the co-worker skill's end-to-end assertion now passes. Commit `test(track3b): live end-to-end structured tool_call verification + findings (Track 3b)`.

---

## Verification (whole plan)
- [ ] `go build ./...`; `go test ./...`; `go test -race ./internal/engine/ ./internal/adapter/anthropic/ ./internal/adapter/ollama/ ./internal/adapter/openai/`.
- [ ] `go vet ./...`; gofumpt `-l` empty; `CGO_ENABLED=0 build`; `GOOS=linux build`; `make arch-lint`.
- [ ] Existing coerce property tests (never-panics, idempotent, no-op, tie-breaker) still green; Anthropic anti-forgery guard preserved for non-wrapper JSON.
- [ ] LIVE: client receives structured tool calls on all 3 surfaces.

## Notes for the executor
- The wrapper extractor is UNAMBIGUOUS and shared; the bare-`{args}` heuristic stays Ollama/OpenAI-only. Never run the bare heuristic on Anthropic.
- Preserve `CoerceToolCall`'s locked properties (the property tests are the contract).
- Do not push/merge without the human's OK.
