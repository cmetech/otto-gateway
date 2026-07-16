# Findings — Anthropic-surface tool-call elicitation (`toolcall:anthropic` flaky ~20%)

**Status: DEFERRED / not critical.** Primary clients are Hermes-based and hit the
**OpenAI** surface (`/v1/chat/completions`), which is fully handled (~100% on the
parity suite). This gap is on the **Anthropic** surface (`POST /v1/messages`) only.
Captured here for a future session; no code changed. Companion to the task prompt
`docs/2026-07-16-gateway-anthropic-elicitation-followup-prompt.md`.

## TL;DR

The failing check is `toolcall:anthropic` (a non-streaming `get_weather`
round-trip), measured ~1/5 PASS while OpenAI/Ollama are ~100% on the same gateway.
**Root cause is NOT what the follow-up prompt hypothesized.** The strict
function-caller prompt and the built-in-tool denial ARE applied on `/v1/messages`
(they live in the shared engine path). The real cause is a **text→tool-call
coercion breadth asymmetry**: the Anthropic aggregator recovers only the exact
`{"tool_call":{…}}` wrapper, while OpenAI/Ollama also recover **bare-args** and
**fenced** JSON. Kiro emits bare-args most of the time, so OpenAI recovers and
Anthropic doesn't.

## Verdict on the original hypotheses (verified against the code)

- **(a) strict function-caller prompt "missing on Anthropic" — REFUTED.** It's
  emitted by the shared `internal/engine/build_acp.go:141-154` (`[Available tools]`
  … "you must NOT use your own built-in tools") whenever `req.Tools` is non-empty.
  Not surface-specific.
- **(b) `denyKiroTools` "missing on Anthropic" — REFUTED.** Shared:
  `internal/engine/engine.go:268` — `acp.WithDenyBuiltinTools(ctx, len(req.Tools) > 0)`.
  Not surface-specific.
- Both `wireToChatRequest` funcs (`internal/adapter/anthropic/wire.go:168`,
  `internal/adapter/openai/wire.go:136`) produce **functionally identical canonical
  requests** for this check: same tool, same user message, `model:"auto"` on both
  (`normalizeClaudeModelID("auto")`→`"auto"`; `SetModel` skipped for `"auto"` at
  `engine.go:257`). `MaxTokens` (256 vs 0) is **never sent to kiro** — only mapped
  for stop-reason (`internal/acp/translate.go:194`). So kiro gets the same prompt +
  same deny on both surfaces.

## Actual root cause (file:line)

Text→tool-call recovery differs by surface:

- **OpenAI/Ollama** call `engine.CoerceToolCall` — the full 9-step algorithm
  (`internal/engine/coerce.go:99`): exact `{"tool_call":…}` wrapper **+
  `stripFences` + bare-`{args}` `pickBestTool` key-overlap scoring**.
  - `internal/adapter/openai/handlers.go:227` and `:333`
  - `internal/adapter/ollama/handlers.go:204` and `:340`
- **Anthropic** calls **only** `engine.ExtractToolCallWrappers` — the exact
  `{"tool_call":{"name","arguments"}}` marker, nothing else.
  - non-streaming: `internal/adapter/anthropic/collect.go:253`
  - streaming: `internal/adapter/anthropic/sse.go:819`

When kiro is elicited it emits the call as **bare args** `{"city":"Paris"}` (or
fenced) far more often than the exact wrapper. OpenAI's `pickBestTool` matches the
bare args to the sole offered tool → `get_weather` → PASS. Anthropic's wrapper-only
coercer finds no `{"tool_call"` marker → preserves it as text → FAIL. The parity
harness's own classifier (`run_parity.py:591`, `has_toolcall_json = '"tool_call"' in
full_prose`) **also** only greps for the wrapper marker, so it mislabels the
bare-args failure as `track-3a` "prose refusal" — which is what made the apparatus
look "not applied." It was applied; the *recovery* just isn't as broad.

The `shell`-substitution failure mode (`surfacing-gap`) is a **separate, rarer,
both-surface** case: kiro picks its built-in shell tool, the resolver correctly
drops it (it isn't `get_weather`). Coercion can't fix that; it also hits OpenAI, so
it's not the asymmetry. Post-denial, kiro re-emits bare-args JSON, which the coerce
fix *would* recover.

## Why it was built this way (the constraint any fix must respect)

`internal/adapter/anthropic/handlers.go:31-52` + `collect.go:241-250` +
`handlers_test.go:705-734`: D-01 / D-17 scenario 5 **deliberately** forbade
`CoerceToolCall` on Anthropic to avoid "wire-shape forgery" — coercing a legitimate
JSON-shaped assistant text into a `tool_use` block for Anthropic-native clients that
emit JSON content on purpose. Guard tests: `TestAnthropic_NoCoerce_Behavioral`
(behavioral) and `TestAnthropic_DoesNotCallCoerceToolCall` (static-source). So this
is a genuine policy tension, not an oversight — flipping it is a contract reversal.

## Fix options (future work)

1. **Enable full coercion on the Anthropic non-streaming path when tools are
   offered (recommended for a Hermes-only deployment).** In
   `anthropic/collect.go:251-270`, replace the `ExtractToolCallWrappers`-only block
   with `engine.CoerceToolCall` semantics (build a synthetic
   `ChatResponse{Content:[text]}`, call `engine.CoerceToolCall(req, resp)`, lift
   `resp.Message.ToolCalls` into `toolParts`/`toolCalls`). Reuses the exact
   OpenAI/Ollama logic. **Tighten** to limit forgery: only run when the trimmed,
   fence-stripped text is essentially a lone JSON object (`CoerceToolCall` already
   requires `pickBestTool` score > 0, so mixed prose won't coerce).
2. **Gate Option 1 behind a config flag** (e.g. `ANTHROPIC_COERCE_TOOLCALLS`,
   default on since clients are Hermes-based). Preserves the D-01/D-17 contract for
   third-party Anthropic clients that opt out. More code, safer.
3. **Streaming parity (follow-on):** mirror the change at `sse.go:819` so streaming
   Anthropic tool calls (PII mode ≠ encrypt) get the same recovery. The parity check
   is non-streaming, so this isn't required for acceptance but leaves a gap.

Not recommended: strengthening the *shared* elicitation prompt to force the exact
wrapper — it affects OpenAI/Ollama too and kiro compliance is probabilistic
(fixing the coercer is deterministic).

## Tests to add / change (when implemented)

- **Invert** `TestAnthropic_NoCoerce_Behavioral` → assert bare `{"city":"Paris"}`
  **+ `get_weather` offered** surfaces a `tool_use` (name `get_weather`,
  `city:"Paris"`, `stop_reason:"tool_use"`).
- **Remove** `TestAnthropic_DoesNotCallCoerceToolCall` (static-source guard now
  contradicts the fix).
- **Keep a no-coerce case:** bare JSON with **no tools** offered (or keys not
  overlapping any tool → score 0) stays preserved verbatim.
- **Add:** fenced JSON → coerced; exact wrapper → still coerced (regression);
  non-matching JSON with tools → preserved.
- **e2e:** a non-streaming bare-args coercion scenario in
  `tests/e2e/tools_anthropic_test.go` (or `tools_alias_test.go`) via fake-kiro.

## Risks / do-not-regress

- Reverses the deliberate D-01/D-17 "preserve bare JSON verbatim" contract → an
  Anthropic-native client returning tool-arg-shaped JSON content could get it
  mis-surfaced as `tool_use`. Mitigated by the score>0 gate + whole-text-JSON
  tightening (and Option 2's flag).
- Must keep passing: `toolcall-nested-fence:anthropic` (fenced — `CoerceToolCall`
  handles it), `toolcall-invented-name:anthropic` (native structured → resolver,
  untouched), `toolcall-exec:anthropic` (native shell→run_shell match, untouched),
  `identity`, and `--suite all`.

## Acceptance / measurement (when implemented)

`python run_parity.py --suite toolcall --surface anthropic` ×5–10: expect ~1/5 →
~10/10; `--suite all` stays green. Requires the real `kiro-cli` (test box, or a
local real-kiro run — costs a few credits). Harness (canonical):
`otto_hermes/hermes-agent/skills/gateway-toolcall-parity/run_parity.py`
(`anthropic_build1` request, `toolcall_ok` assertion, `classify_capture` diagnosis).

## Related, separate gap

The Anthropic **streaming** SSE state machine (`sse.go`) also does not apply the
alias **resolver** to *native structured* `tool_use` (it surfaces the native name
verbatim, no dedup), and the encrypt-mode reroute (`handlers.go:236` →
`CollectFromRun` → `runSyntheticSSEFromResponse`) renders tool_use only from
`Message.Content` parts, so a resolved `Message.ToolCalls` can be dropped. Distinct
from the coercion-breadth issue above; also non-critical while clients use the
OpenAI surface. See the conversation on 2026-07-16 for the trace.
