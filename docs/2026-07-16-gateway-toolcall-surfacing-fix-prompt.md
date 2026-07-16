# LLM prompt — fix OTTO gateway tool-call surfacing + kiro persona bleed

> Paste everything below the line into a coding session running **in the
> `otto-gateway` repo** (`/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/`).
> It is self-contained. Do not paste it into the hermes-agent or loop24 chat.

---

You are working in **`otto-gateway`** — a Go service that fronts `kiro-cli` (Kilo) over ACP and
exposes OpenAI-, Anthropic-, and Ollama-compatible HTTP surfaces. Two production defects were
reproduced live in a loop24-branded Hermes desktop client (which talks to this gateway's
**OpenAI surface**, `http://127.0.0.1:18080/v1`, `POST /v1/chat/completions`). Fix both, in this
repo only. Read the real code before changing anything; ground every claim in the source and
its `_test.go` files — do not assume behavior.

## Defect 1 — kiro tool calls leak as `[tool: …]` text instead of structured tool_calls

### Live repro (the client asked the agent to run `python -c "print(2+2)"`)

```
[tool: execute] [tool: execute] [tool: execute] [tool: execute] It looks like shell
execution is being blocked by a permission issue in this session. However, the answer is
straightforward: 4
```

The assistant streamed the literal text `[tool: execute]` four times, then gave up and answered
from memory. The host (Hermes agent) offered a real `code_execution` tool; the model tried to
call it, but the gateway rendered kiro's native tool-call event as **narration inside the
message content** rather than surfacing a **structured** tool call. Because no structured
`tool_calls` reached the host, the tool never executed, the model saw no result, retried, and
hallucinated a permission error.

### What "correct" means on each surface

The gateway must convert kiro's ACP tool-call events into the caller's structured tool-call
shape, and must **never** emit `[tool: …]` (or any tool-call marker) into assistant text:

- **OpenAI** (`/v1/chat/completions`):
  - non-stream: `choices[0].message.tool_calls[]`, each
    `{ id, type:"function", function:{ name, arguments /* JSON string */ } }`, and
    `finish_reason:"tool_calls"`.
  - stream: `choices[0].delta.tool_calls[]` with a stable `index`, `id` on first chunk, and
    `function.name` / incrementally-concatenated `function.arguments`; final chunk
    `finish_reason:"tool_calls"`, then `[DONE]`.
- **Ollama** (`/api/chat`): `message.tool_calls[]` → `{ function:{ name, arguments /* object */ } }`.
- **Anthropic** (`/v1/messages`): `content[]` block `{ type:"tool_use", id, name, input }`.
  (This surface appears to already work — verify and keep it working; it is the reference for
  what "surfaced correctly" looks like.)

### Where to look (find the real code; these are landmarks, not gospel)

- The ACP event handling / engine loop that receives kiro frames — kiro streams
  `session/update` events of kind `tool_call`, `tool_call_chunk`, `tool_call_update`, plus
  `agent_message_chunk`. Some kiro builds also emit a `{"tool_call": …}` JSON object **inside**
  an `agent_message_chunk` (prose) rather than as a native tool-call event — handle both.
  (Reference landmark from a prior audit: `internal/engine/engine.go` ~line 251 calls
  `SetModel`; the ACP session/update handling is in the same engine/adapter area.)
- The per-surface renderers that turn engine events into HTTP responses/SSE — look under
  `internal/adapter/openai/…` and `internal/adapter/ollama/…` (mirror of
  `internal/adapter/anthropic/wire.go`). The `[tool: …]` string is being produced somewhere on
  the OpenAI/Ollama render path (grep the repo for `[tool:` / `tool:` formatting) — that is the
  bug site: a native/JSON tool call is being stringified into content instead of mapped to
  structured `tool_calls`.

### Required change

1. On the OpenAI and Ollama surfaces, intercept kiro `tool_call` / `tool_call_chunk` /
   `tool_call_update` events **and** the Track-3b case (`{"tool_call": …}` JSON appearing in an
   `agent_message_chunk`), and emit them as structured tool calls in the shapes above — for
   **both** non-stream and streaming responses.
2. Guarantee no code path writes `[tool: …]` (or any tool-call marker) into
   `message.content` / `delta.content`. If a tool call is detected, it goes to `tool_calls`,
   never to content.
3. Preserve **argument fidelity**: the tool name and argument values surfaced must match what
   kiro emitted. (Known adjacent issue: when the PII hook is on, tool-call argument values can
   come back as ciphertext-shaped tokens because the response-side decrypt doesn't restore
   tool_call arguments — if you touch this path, decrypt tool_call arguments on the round-trip
   too. If out of scope for this change, leave a `TODO` and note it.)
4. Add/extend `_test.go` coverage that feeds representative kiro ACP frames (native tool_call,
   tool_call_chunk stream, and the `{"tool_call":…}`-in-prose variant) and asserts structured
   `tool_calls` on each surface with **zero** `[tool:` in content.

## Defect 2 — kiro persona bleed / hallucinated capability boundary

### Live repro (the client asked "list your skills")

The model listed the host-provided skills correctly, then said:

```
These skills belong to your Hermes agent environment. From this Kiro CLI session I can help
with general coding, file operations, AWS, and terminal tasks — but loading and executing
Hermes skills requires the Hermes agent.
```

kiro-cli's built-in identity ("**Kiro CLI**", "**AWS**") is overriding the caller's framing. It
declared a capability boundary that does not exist (it *is* the agent that runs those tools) and
refused. This makes the model decline host tasks even when tool-calling works.

### Required change

- Ensure the **caller's system prompt** (sent by the host on each request) is passed through to
  kiro and takes precedence, and that the gateway's ACP session initialization composes a
  system/persona prompt so the model **does not** self-identify as "Kiro CLI" / an AWS tool and
  **does not** claim that host-provided tools/skills "require a different agent." Locate kiro's
  own baked-in system prompt / persona injection (in the ACP session init or a prompt-assembly
  step) and make sure it does not shadow or contradict the caller identity. The model should
  present as the host agent and treat offered tools as its own to call.
- Do not hardcode a brand name in the gateway; the host supplies identity. The fix is to stop
  kiro's persona from leaking, not to inject "OTTO"/"LOOP24".
- Add a test asserting a benign "who are you?" turn does not return `kiro cli` /
  `requires the hermes agent` / `belongs to … agent environment` wording.

## Explicitly OUT of scope (do not "fix" these)

- **Model normalization.** An arbitrary/unknown model id on the Anthropic surface currently
  forwards to kiro and returns HTTP 500 (`{"type":"error","error":{"type":"api_error"}}`). That
  is the intended contract — OTTO does **no** HTTP-level model validation. Do **not** add model
  validation or normalize unknown ids to `auto`. (This is a separate product decision.)
- All three surfaces must keep working; do not regress Anthropic tool_use.

## Acceptance criteria — how the fix is verified

The host ships a black-box conformance harness (`gateway-toolcall-parity`, stdlib Python,
loopback-only) that grades this gateway over HTTP. Start the gateway with `ACP_CAPTURE=true`,
then from the harness directory run:

```
python run_parity.py --suite all
```

The fix is complete when, against a real `kiro-cli`:

1. `toolcall-exec:openai`, `toolcall-exec:ollama`, `toolcall-exec:anthropic` **PASS** — a
   `run_shell(command)` tool offered with a "run this command" prompt yields a **structured**
   tool call, not `[tool: …]` text.
2. Every `toolcall:*` check PASSES with **zero** `[tool:` leaks (the harness hard-fails on any
   `[tool:` in assistant text).
3. `identity:openai` **PASSES** — a "who are you?" turn does not surface the kiro/AWS persona or
   a capability-boundary refusal.
4. The two live transcripts above no longer reproduce in a real desktop chat.

Note: the tool-call and identity checks are model-dependent (`flaky`) — run each a few times to
confirm the gap is closed, not just momentarily quiet.

## Working method

- Read `internal/engine/…`, `internal/adapter/{openai,ollama,anthropic}/…`, and the ACP frame
  handling first. Grep for `[tool:` and for `tool_call` handling to find the exact render sites.
- Make the smallest change that surfaces structured tool_calls on OpenAI + Ollama and stops the
  persona bleed. Add table-driven `_test.go` cases alongside existing gateway tests.
- Do not modify anything outside `otto-gateway`. Report the files changed, the root cause you
  found in the real code, and the before/after of the render path.
