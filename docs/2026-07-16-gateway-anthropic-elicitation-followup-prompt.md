# Follow-up — Anthropic-surface tool-call elicitation is inconsistent

> Paste the section below the line into a coding session running **in the
> `otto-gateway` repo**. It is self-contained. This is a *follow-up* to the
> tool-call surfacing + persona fix in
> `docs/2026-07-16-gateway-toolcall-surfacing-fix-prompt.md` — that work is
> verified done; this note is the one remaining gap.

---

You are working in **`otto-gateway`** (Go; fronts `kiro-cli` over ACP; OpenAI-, Anthropic-,
Ollama-compatible surfaces). A prior refactor fixed structured tool-call surfacing and the
kiro persona bleed — verified by the `gateway-toolcall-parity` conformance suite, which now
passes **22/23** (health, streaming, validation, model-normalize, `identity`, and — critically —
`toolcall-exec:{anthropic,openai,ollama}`, `toolcall:openai`, `toolcall:ollama`,
`toolcall-invented-name:anthropic`, `toolcall-nested-fence:anthropic` all PASS).

**One check remains flaky: `toolcall:anthropic`.** On the **Anthropic surface only**
(`POST /v1/messages`), a caller-offered tool is intermittently NOT invoked — kiro escapes it.
Fix the inconsistency so the Anthropic path elicits the offered tool as reliably as the
OpenAI/Ollama paths already do.

**Measured pass rate (5 consecutive runs on the test box): 1 PASS / 4 FAIL (~20%).** So the
apparatus is NOT entirely missing — it works ~1 in 5 — but the Anthropic path elicits the
offered tool far *less reliably* than OpenAI/Ollama (which are effectively 100% here). Frame
this as **"the Anthropic-surface elicitation is present but weak/inconsistent — strengthen it to
match OpenAI/Ollama,"** not "add a missing feature." Target: reliably green across repeated runs.

## The symptom (two failure modes, same surface)

The check offers a single `get_weather(city)` tool and sends *"What is the weather in Paris?
Use the get_weather tool."* to `/v1/messages`, then expects a structured `tool_use` for
`get_weather`. Across repeated runs it fails **two different ways**, which is the tell that this
is an elicitation-consistency problem, not a surfacing bug:

1. **Prose refusal (classifier: `track-3a`).** The response is an `agent_message_chunk` of text
   ("I…") with **no tool call at all**, and no `session/request_permission`. The strict
   function-caller prompt / built-in-tool denial evidently wasn't applied for that request.
   - ACP frame: `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"I"}}`

2. **Built-in `shell` substitution (classifier: `surfacing-gap`).** kiro reaches for its own
   built-in **shell/execute** tool (to "look up" the weather) instead of the offered
   `get_weather`, and that native `tool_call_chunk` is rendered as narration rather than
   surfaced as a structured `tool_use` remapped to the offered tool.
   - ACP frame: `{"sessionUpdate":"tool_call_chunk","toolCallId":"tooluse_…","title":"shell","kind":"execute"}`

Meanwhile, on the SAME gateway/session: `toolcall:openai`, `toolcall:ollama`,
`toolcall-exec:*` (offer `run_shell`, "run echo hi"), `toolcall-invented-name:anthropic`
(kiro emits some other tool name → remapped to the sole offered tool), and
`toolcall-nested-fence:anthropic` all PASS. So the structured-surfacing machinery and the
`pickBestTool` remap **work**; OpenAI/Ollama elicitation is **reliable**. The gap is specific
to the **Anthropic surface's elicitation consistency**.

## How this is tested (the exact wire traffic — reproduce it directly)

The failing check is `toolcall:anthropic` in the `gateway-toolcall-parity` harness — a
stdlib-Python, black-box conformance test that talks to the gateway **over HTTP only** (it never
touches the gateway process). Read the harness for full detail on every prompt/tool/assertion:

- **Canonical source:** `/Users/coreyellis/code/github.com/cmetech/otto_hermes/hermes-agent/skills/gateway-toolcall-parity/run_parity.py`
- **As deployed on the test box (what produced the runs above):** `C:\Users\splunk\AppData\Local\loop24\skills\gateway-toolcall-parity\run_parity.py`
- Config: `GW_URL=http://127.0.0.1:18080` (default), `GW_RETRIES=2` (flaky checks retried),
  `TOOL_RESULT="18°C, sunny"`. Run it with `python run_parity.py --suite toolcall --surface anthropic`.

`toolcall:anthropic` is a **2-phase `get_weather` round-trip**:

**Phase 1** — `POST /v1/messages` (header `anthropic-version: 2023-06-01`):
```json
{
  "model": "auto",
  "max_tokens": 256,
  "messages": [{"role": "user", "content": "What is the weather in Paris? Use the get_weather tool."}],
  "tools": [{
    "name": "get_weather",
    "description": "Get weather for a city",
    "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}
  }]
}
```
**PASS (phase 1)** iff the response carries a `content[]` block
`{"type":"tool_use","name":"get_weather","input":{"city":"Paris"}}` (name == `get_weather`,
`city` == `Paris`, case-insensitive). A prose answer, or kiro's built-in `shell` call, is a FAIL.

**Phase 2** — the harness sends the tool result `"18°C, sunny"` back as a `tool_result` and
expects a coherent final answer mentioning the weather.

**Contrast — why `toolcall-exec:anthropic` PASSES but `toolcall:anthropic` doesn't:** the exec
check offers a single `run_shell(command)` tool with prompt *"Run this command and show me its
exact output: echo hi"*. There, kiro's own **shell/execute** intent *matches the offered tool
name*, so a structured `tool_use` surfaces reliably — no elicitation onto a foreign tool needed.
`get_weather` is a non-execute tool kiro has no native equivalent for, so the gateway must
**elicit** it onto the offered tool (strict function-caller prompt + deny kiro's built-in tools)
rather than ride kiro's shell intent. That elicitation is what's weak on the Anthropic surface.

## Hypothesis to confirm or refute in the code

The elicitation apparatus is applied less consistently on the `/v1/messages` path than on the
OpenAI/Ollama paths. Two cooperating pieces are suspect:

- **(a) Strict function-caller system prompt.** When the caller supplies `tools`, is the
  Anthropic request assembled with the same "you must call one of the provided tools" /
  function-caller system prompt that the OpenAI path uses? The `track-3a` prose refusal
  suggests it's sometimes missing on `/v1/messages`.
- **(b) `denyKiroTools`.** Is kiro's built-in shell/execute toolset denied on the Anthropic
  path? The `title:"shell", kind:"execute"` frame means kiro's shell tool was available and
  chosen. If it were denied (as it presumably is on OpenAI/Ollama), kiro would be forced onto
  the offered `get_weather`.
- **(c) `tool_call_chunk` (streamed) surfacing on Anthropic.** Even when kiro does emit a tool
  call, confirm a **streamed `tool_call_chunk`** (not just a complete `tool_call`) is assembled
  and surfaced as a structured `tool_use` block on `/v1/messages`, and remapped via
  `pickBestTool` to the sole offered tool — not concatenated into text.

## Where to look (verify against the real code; these are landmarks)

- Per-surface request assembly: `internal/adapter/anthropic/…` vs `internal/adapter/openai/…`
  vs `internal/adapter/ollama/…`. Diff how each injects the function-caller system prompt and
  sets tool-denial when the caller offers tools. Find the asymmetry on the Anthropic path.
- `internal/engine/…` (e.g. `engine.go`, `coerce.go` `pickBestTool`) — the elicitation /
  coercion / remap logic. Is it invoked identically regardless of surface, or does the
  Anthropic path skip a step?
- The `denyKiroTools` (or equivalently-named) toggle and the strict function-caller prompt
  string — grep for them and check they're wired on `/v1/messages` requests carrying `tools`.
- ACP translation: `internal/acp/translate.go`, `internal/capture/*` — how `tool_call_chunk`
  frames are assembled/surfaced per surface.

## Acceptance criteria

Start the gateway with `ACP_CAPTURE=true`, then from the parity harness:

```
# repeat 5–10× — must be reliably green, matching the OpenAI/Ollama surfaces
python run_parity.py --suite toolcall --surface anthropic
```

**Baseline before your change: ~1/5 (20%) PASS.** Done when `toolcall:anthropic` **reliably
passes** (structured `get_weather` tool_use + a tool-result round-trip to a coherent final
answer) across repeated runs — target near-100%, matching the OpenAI/Ollama surfaces — with no
`track-3a` prose refusals and no `shell`-substitution `surfacing-gap`s for the offered tool.
`--suite all` stays green (allowing for genuinely model-dependent flaky retries). Report the
before/after pass rate (e.g. "was 1/5, now 10/10").

## Explicitly out of scope / do not regress

- Do **not** regress the already-passing structured surfacing (`toolcall:openai`,
  `toolcall:ollama`, `toolcall-exec:*`) or the `identity` persona fix.
- Do **not** add HTTP-level model validation (unknown model → 500 is the intended contract).
- Keep all three surfaces working.

## Working method

Read the three per-surface adapters and the engine elicitation/coerce path first. Locate where
the function-caller system prompt and `denyKiroTools` are applied, and why the Anthropic path
is inconsistent; make it symmetric with the OpenAI path. Add/adjust `_test.go` coverage.
Report the files changed, the root cause found in the real code, and the parity pass rate
before vs after.
