# Track 3b — tool-call wrapper coercion + surfacing — design

**Date:** 2026-07-15
**Status:** Approved (design) — ready to implement.
**Scope:** One implementation plan (multi-commit, TDD). Track 3b of the
[legacy-gateway parity roadmap](../2026-07-14-legacy-gateway-parity-roadmap.md).
Depends on **Track 3a** (which makes kiro emit `{"tool_call":{…}}` JSON — live-proven).
**Goal:** recognize the `{"tool_call":{"name","arguments"}}` wrapper JSON kiro now
emits in assistant text, coerce it into **structured tool calls** on all three
surfaces (Ollama, OpenAI, Anthropic), with JS-parity robustness (fences,
embedded/multiple objects, truncated JSON, invented-name remap).

## Why

Track 3a's live capture confirmed kiro emits, on every surface, a fenced
`{"tool_call": {"name":"get_weather","arguments":{"city":"Paris"}}}` block as
`agent_message_chunk` **text**. But the client still receives it as raw text:
Go's `CoerceToolCall` (`internal/engine/coerce.go:92`) only recognizes a **bare
`{args}` object** scored by key-overlap; the wrapper's single top-level key
`"tool_call"` never matches a tool's `properties`, so it scores 0 and no-ops
(map §1). The JS gateway coerces this wrapper (`coerceToolCall` +
`extractToolCallObjects` in `acp-server-ollama.js`); Track 3b ports that.

## The Anthropic asymmetry (key decision)

The map (§2-3) surfaces two facts:
- **Ollama & OpenAI** renderers read `Message.ToolCalls` directly and call
  `CoerceToolCall` — an upgraded coercer round-trips with no wiring change.
- **Anthropic** deliberately does NOT call `CoerceToolCall` (guarded by a
  static-source test `TestAnthropic_DoesNotCallCoerceToolCall`) and its renderer
  reads only `ContentKindToolUse` parts, not `Message.ToolCalls`.

The Anthropic invariant exists to prevent **forging `tool_use` from a legitimate
bare-JSON assistant response** (the ambiguous key-overlap heuristic). **But the
explicit `{"tool_call":{…}}` wrapper is unambiguous** — it is, by construction, a
tool call kiro was told (by the 3a strict prompt) to emit. So:

**Decision:** split coercion into two tiers:
- **Wrapper coercion (unambiguous, ALL 3 surfaces):** a shared helper that finds
  explicit `{"tool_call":{name,arguments}}` objects and yields tool calls.
- **Bare-`{args}` heuristic (ambiguous, Ollama/OpenAI ONLY):** the existing
  `pickBestTool` key-overlap path stays exactly as today and never runs on
  Anthropic.

Anthropic gains wrapper coercion only — closing the real gap without weakening
the anti-forgery invariant. The `TestAnthropic_DoesNotCallCoerceToolCall`
static guard is updated to a deliberate assertion: Anthropic may call the
wrapper extractor but must NOT call the ambiguous `CoerceToolCall`/bare-heuristic.

## Decisions

### D1 — New shared extractor `ExtractToolCallWrappers` (`internal/engine/coerce.go`)

```go
// ExtractToolCallWrappers finds every explicit {"tool_call":{"name","arguments"}}
// object in text and returns them as canonical.ToolCalls, with invented-name
// remap against tools. Unambiguous (only the wrapper shape) — safe on every
// surface incl. Anthropic. Returns nil when none found.
func ExtractToolCallWrappers(text string, tools []canonical.ToolSpec) []canonical.ToolCall
```

Strategy, in order (stop at first that yields ≥1 call), ported from JS
`coerceToolCall` (`acp-server-ollama.js:1139-1200`):
1. **Whole content** parses (raw, then `StripFences`) as the wrapper (object) or
   an array of wrappers → `pushWrapper` each.
2. **Balanced-brace scan** `extractToolCallObjects(text)` (D2) around every
   `"tool_call"` occurrence → `pushWrapper` each.

`pushWrapper(obj)`: read `obj["tool_call"]`; require a non-empty `name`; take
`arguments` (if it's a JSON string, parse it; default `{}`); if `name` is not a
declared tool, remap via the existing `pickBestTool(arguments, tools)` (invented-
name rescue, JS `1152-1160`) — drop if no arg-overlap match. Emit
`canonical.ToolCall{ID: "call_<unixnano>", Name, Arguments}`.

### D2 — Greenfield `extractToolCallObjects` (string-aware balanced-brace + repair)

Port from JS `acp-server-ollama.js:975-1050` + `repairJsonControlChars:955-973`.
No such helper exists in Go today (map §5); build it in `coerce.go`:
- Walk each `"tool_call"` index; find the enclosing `{`; string-aware
  balanced-brace scan (tracks `inStr`/`esc`, ignores braces inside string
  values) to the matching `}`.
- Try `json.Unmarshal(slice)`, then `json.Unmarshal(repairControlChars(slice))`.
- **Truncation repair:** if the scan runs off the end with depth `1..8` still
  open (model dropped trailing braces), trim trailing junk to the last
  meaningful char, append `}`×depth, and retry parse.
- `repairControlChars`: escape raw `\n`/`\r`/`\t` that appear INSIDE string
  values (a common kiro defect with multi-line argument strings).

### D3 — Extend `CoerceToolCall` (Ollama/OpenAI) to try wrappers first

`CoerceToolCall` keeps its signature and idempotency guard. New order:
1. Existing guards (nil, no tools, `ToolCalls` already set, locate text part).
2. **Try `ExtractToolCallWrappers(text, req.Tools)`** — if it returns calls,
   clear the text part, set `resp.Message.ToolCalls`, return true.
3. **Else** the existing bare-`{args}` `pickBestTool` path, unchanged.

So Ollama/OpenAI need **no call-site change** (map §2) — the 4 existing call
sites automatically gain wrapper recognition. Idempotency, never-panics, and
zero-match-no-op properties are preserved and re-tested.

### D4 — Anthropic wrapper coercion (non-streaming + streaming)

- **Non-streaming** (`internal/adapter/anthropic/collect.go`,
  `CollectAnthropicChat`/`assembleAnthropicChatResponse`): after the assistant
  text is assembled and before the response is returned, run
  `engine.ExtractToolCallWrappers(assembledText, req.Tools)`; for each call,
  append BOTH a `canonical.ContentPart{Kind: ContentKindToolUse, ToolUse:
  &ToolUsePart{ID, Name, Input: args}}` (the renderer reads this — map §3
  "CRITICAL FINDING") AND a `canonical.ToolCall` to `Message.ToolCalls` (D-07
  parity). Clear/replace the coerced text. Only run when `len(req.Tools) > 0`
  and no native tool_use already present.
- **Streaming** (`internal/adapter/anthropic/sse.go`): at end-of-stream on the
  buffered-text path, run the same extractor; emit the `tool_use`
  content_block_start/delta/stop SSE frames mirroring the native-tool_use
  emitter already there, and set `stop_reason:"tool_use"`.
- **Guards:** update `TestAnthropic_DoesNotCallCoerceToolCall` +
  `TestAnthropic_NoCoerce_Behavioral` to the new contract: Anthropic coerces the
  explicit wrapper only; a bare-JSON assistant response (no `tool_call` wrapper)
  must still NOT be forged into `tool_use`.

### D5 — Surfacing tests + live verification

Per-surface wire tests: the wrapper text now yields Ollama
`message.tool_calls` (args object), OpenAI `tool_calls` (args JSON string,
`finish_reason:"tool_calls"`), Anthropic `tool_use` content block
(`stop_reason:"tool_use"`). Then a live kirolive harness confirms the CLIENT
receives structured tool calls end-to-end on all 3 surfaces (the co-worker
skill's end-to-end assertion).

## Out of scope

- The `` ```tool_call ``` `` **fn(args)** fence syntax (JS strategy 3,
  `parseFnCallArgs`) — kiro emits clean JSON, not Python-call syntax (live
  evidence). Deferred unless a future capture shows kiro using it.
- The corrective-nudge retry (Track 3a leftover) — separate.
- Streaming per-chunk incremental tool_call deltas — Track 3b coerces at
  end-of-stream (matching the existing buffered-coerce design on every surface).

## Verification

- Unit (TDD): `extractToolCallObjects` (whole/fenced/embedded/multiple/truncated/
  control-chars/none); `ExtractToolCallWrappers` (wrapper, array, invented-name
  remap, arguments-as-string); `CoerceToolCall` new wrapper rows + preserved
  bare-`{args}` + idempotency/never-panics/no-op; Anthropic collect+sse wrapper
  coercion + the updated anti-forgery guards; per-surface render tests.
- Gates: `go build ./...`; `go test ./...`; `go test -race ./internal/engine/
  ./internal/adapter/...`; `go vet`; gofumpt-clean; `CGO_ENABLED=0 build`;
  `GOOS=linux build`; `make arch-lint`.
- **Live:** kirolive harness — client receives structured tool calls on all 3
  surfaces; update the findings doc.

## Non-goals

- No change to the Track 3a elicitation apparatus.
- No coercion of ambiguous bare-JSON on Anthropic (anti-forgery invariant kept
  for non-wrapper text).

## Files touched (anticipated)

- `internal/engine/coerce.go` — `ExtractToolCallWrappers`, `extractToolCallObjects`,
  `repairControlChars`, wrapper tier in `CoerceToolCall`; `coerce_test.go`.
- `internal/adapter/anthropic/collect.go` + `sse.go` — wrapper coercion; tests +
  updated anti-forgery guards (`handlers_test.go`).
- `internal/adapter/ollama/*`, `internal/adapter/openai/*` — surface tests (likely
  no production change).
- `tests/track3b_coercion_test.go` (new, `//go:build kirolive`) — live.
- `docs/reviews/2026-07-14-track0-toolcall-findings.md` — Track 3b outcome.
