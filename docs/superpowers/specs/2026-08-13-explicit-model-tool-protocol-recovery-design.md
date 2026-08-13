# Explicit-model tool-protocol recovery — design

**Date:** 2026-08-13  
**Status:** Proposed — awaiting design approval.  
**Scope:** Preserve client-selected models, make one corrective attempt with the
same model when it fails the Gateway's external tool-call protocol, and then
return a protocol-native error that recommends `model: "auto"`. The Gateway
must never switch to `auto` automatically.

## Problem

The Gateway exposes caller tools to Kiro as instructions and converts the
model's explicit `{"tool_call":{"name","arguments"}}` text wrapper into the
native OpenAI, Anthropic, or Ollama tool-call shape. Kiro itself has no MCP
servers or caller tools registered. This is deliberate: Hermes remains the tool
host and the Gateway remains a protocol translator.

That translation works when Kiro's `auto` model route follows the wrapper
instructions. A client-selected model can instead answer that the connector
tools are unavailable. That answer is internally consistent from the selected
model's point of view, but it breaks the caller's tool loop: Hermes supplied the
tools and expected a structured call, while the Gateway returned a capability
refusal as ordinary assistant text.

The successful direct Hermes-to-Codex test proves the connector and Hermes tool
executor are healthy. The successful Gateway test with `model: "auto"` proves
the wrapper/coercion path is healthy. The remaining gap is the Gateway's lack of
a bounded recovery and explicit failure contract when a selected Kiro model
does not honor the external tool protocol.

## Goals

- Honor the exact non-`auto` model requested by the client.
- Recover once when that same model appears capable of correcting its external
  tool-call response.
- Never silently substitute another model, including `auto`.
- If recovery fails, return a stable machine-readable error and recommend that
  the user retry with `model: "auto"`.
- Apply one policy across OpenAI, Anthropic, and Ollama, streaming and
  non-streaming requests.
- Run privacy, compression, logging, and other request hooks exactly once.
- Preserve all current behavior for tool-less requests, `auto`, successful tool
  calls, and final answers after tool results.

## Non-goals

- No automatic retry with `auto`.
- No configuration flag for automatic model fallback.
- No native MCP registration inside Kiro.
- No execution of caller tools inside the Gateway.
- No Hermes/co-worker changes.
- No broad natural-language intent parser.
- No weakening of privacy, compression, tool authorization, or existing
  wrapper validation.

## Decisions

### D1 — Model selection is authoritative

For `req.Model != "" && req.Model != "auto"`, the Gateway calls `SetModel`
exactly as it does today. A corrective attempt reuses the same ACP session and
therefore the same selected model; it does not call `SetModel` with a different
identifier.

The Gateway never substitutes `auto`, even if correction fails. `auto` appears
only in the client-facing recommendation.

If `SetModel` itself fails, return the typed error
`selected_model_activation_failed`. Do not issue a corrective prompt because
the selected model was never activated. The message recommends retrying with
`model: "auto"` without exposing Kiro's raw error.

### D2 — Guard only eligible explicit-model tool-decision turns

The recovery guard activates only when all of these are true:

1. The requested model is explicit (neither empty nor `auto`).
2. The request declares at least one caller tool.
3. The current request is a tool-decision turn, not the final-answer turn after
   a tool result.
4. `tool_choice` is not `none`.

The last canonical message determines turn eligibility. A `RoleTool` message,
or a message containing `ContentKindToolResult`, makes the turn ineligible.
This prevents a perfectly valid post-tool explanation from being mistaken for
a second missing tool call. A subsequent new user-text turn is eligible again.

Requests using `auto`, requests without tools, and ineligible turns follow the
existing live-stream path byte-for-byte.

### D3 — Classify conservatively

The guard recognizes the following successful tool-protocol outputs:

- A Kiro-native tool-call chunk that resolves to a caller-offered tool through
  the existing offered-tool/alias rules.
- One or more explicit wrappers accepted by the existing
  `ExtractToolCallWrappers` validator, including the deferred `tool_call`
  dispatcher shape added in v3.2.1.

The first attempt needs correction only in these cases:

- `tool_choice` is `required`, Anthropic `any`, or a named tool and no valid
  matching tool call was produced.
- The output contains an explicit tool-wrapper marker but the existing parser
  cannot validate it.
- The complete assistant text matches a narrow, high-confidence capability
  refusal: the response says that supplied tools/connectors are unavailable,
  inaccessible, or not executable in the current environment.

For a named choice, a valid call to a different tool is a mismatch and requires
correction. Unknown `tool_choice` shapes retain today's accept-and-ignore
behavior.

Ordinary final text remains valid when tool choice is absent or `auto`. Generic
refusals, policy/safety refusals, GitLab permission errors, and a tool's own
error result are not protocol failures. The detector operates only on model
output and uses a short table of anchored phrases; it does not inspect or echo
tool arguments.

### D4 — One same-model corrective prompt

On a classified first-attempt failure, the engine sends exactly one additional
ACP `Prompt` on the same session. The corrective block is static except for an
optional validated named-tool identifier:

> Caller tools are available through the external tool protocol. Do not claim
> they are unavailable. Return either a valid explicit tool-call wrapper for an
> offered tool, or a normal final answer when tool use is optional.

For `required`, `any`, or a named choice, the final sentence instead requires a
valid wrapper and, for a named choice, the validated offered name.

The corrective block contains no user text, tool arguments, tool output,
credentials, or schemas. It is an internal ACP turn and is not appended to the
canonical client transcript. The correction count is a hard invariant of one;
there is no retry loop or behavioral setting.

If the second attempt produces a valid response, only that response is exposed
to the client. If it fails, the engine returns
`selected_model_tool_protocol_failed`.

### D5 — Engine-owned preflight buffering

Implement recovery once in the shared engine, below PreHooks and above the
three wire adapters. Do not duplicate retry policy in OpenAI, Anthropic, and
Ollama handlers.

For an eligible guarded turn, `Engine.Run` preflights the first ACP stream
before returning the `Run` handle:

1. Buffer canonical chunks and aggregate only the data needed for
   classification.
2. If a valid native tool call makes the outcome decisive, return a composite
   replay/live stream without waiting for unrelated trailing chunks.
3. At normal completion, classify the buffered output.
4. On success, return a replay stream containing the chosen attempt.
5. On a correctable failure, prompt once on the same session and preflight the
   second stream.
6. On terminal failure, return a typed engine error before any adapter writes
   response headers.

Preflight is necessary for streaming correctness. Once an adapter sends HTTP
200 or emits a refusal fragment, it cannot retract those bytes and replace them
with a 502 error or a corrected tool call. The tradeoff is delayed first-token
delivery on eligible explicit-model tool-decision turns. All other turns keep
their current streaming latency.

Use the existing 1 MiB streaming-coercion ceiling as the classification buffer
limit. If an attempt exceeds the ceiling before a decision, fail open to the
existing response path: replay the bounded prefix and pass through the
remaining live stream, with no corrective retry. This keeps memory bounded and
does not turn a very large valid answer into a Gateway error.

The replay/composite stream must preserve chunk order, final result, context
cancellation, idle timeout behavior, and exactly-once watchdog cleanup.

### D6 — Hooks execute once per client request

The corrective attempt occurs after `PreHooks` have transformed the canonical
request. It reuses the same transformed request state and must not invoke
PreHooks again. Therefore privacy classification/redaction, compression,
request logging, and request-level accounting occur once.

`PostHooks` execute once:

- after the response actually returned to the client; or
- as cleanup with a nil/diagnostic response when activation or protocol
  recovery fails before a response is returned.

The internal corrective prompt receives a separate retry metric but must not
increment the client-request/model-request counter a second time.

### D7 — Protocol-native terminal errors

Both selected-model errors use HTTP 502 because the Gateway accepted a valid
request but its configured upstream agent could not satisfy it:

- `selected_model_activation_failed`
- `selected_model_tool_protocol_failed`

The protocol-failure message is:

> The selected model did not produce a valid external tool call after one
> corrective attempt. Retry the request with model `auto`.

The activation-failure message is:

> The selected model could not be activated. Retry the request with model
> `auto`.

Render these through each native error contract:

- OpenAI: `error.type = "api_error"`, stable value in `error.code`, and the
  safe message above.
- Anthropic: normal `type: "error"` / `error.type: "api_error"` envelope,
  safe message, and `X-Otto-Error-Code` for the stable Gateway code.
- Ollama: normal `{"error":"..."}` envelope and `X-Otto-Error-Code`.

Because guarded streams are resolved before handlers commit headers, streaming
failures use the same non-streaming HTTP 502 envelopes rather than truncated
SSE/NDJSON. Include the existing request ID header, but never include tool
schemas, arguments, raw model output, or credentials in the error body.

### D8 — Observability without sensitive payloads

Log one structured recovery record with:

- normalized requested model;
- failure class (`required_missing`, `named_mismatch`, `malformed_wrapper`,
  `capability_refusal`, or `activation_failed`);
- corrective attempt count (`0` or `1`);
- outcome (`first_attempt`, `corrected`, `failed`, or `buffer_bypass`);
- `recommend_auto` boolean;
- request ID.

Add bounded-cardinality counters for protocol failures and corrective outcomes.
Unknown/custom model identifiers must be normalized to an `other` label rather
than used as arbitrary metric labels. Do not log model output or tool
arguments.

## Request flow

```text
client request
    |
    v
decode -> canonical request -> PreHooks (once)
    |
    v
new ACP session -> SetModel(exact explicit model)
    |
    v
first prompt -> guarded preflight
    |                 |
    | valid           | classified protocol failure
    v                 v
replay response   same-session corrective prompt (once)
                          |                 |
                          | valid           | failed
                          v                 v
                     replay response    typed 502 + recommend auto
    |                         |
    +------------+------------+
                 v
          PostHooks (once)
```

`auto` never enters the guarded branch and is never selected by it.

## Test contract

### Pure policy tests

- Eligibility table: empty/`auto`/explicit model; no tools/tools; initial
  user turn/tool-result turn; `none`/`auto`/`required`/`any`/named choice.
- Tool requirement table: missing required call, named match, named mismatch,
  native offered call, aliased native call, valid wrapper, deferred dispatcher
  wrapper, malformed wrapper.
- Capability-refusal positives for the observed connector-unavailable wording
  and close variants.
- Negative cases for normal answers, safety refusals, GitLab permission errors,
  and tool-result failures.

### Engine tests

- Successful first attempt performs one ACP prompt and no correction.
- Correctable first failure performs exactly two prompts on one session and
  never calls `SetModel` again.
- Corrected second attempt exposes only second-attempt chunks.
- Failed second attempt returns the typed protocol error.
- `SetModel` failure returns the typed activation error and performs no prompt.
- PreHooks and PostHooks each execute once across correction and failure paths.
- `OnModelRequest` executes once.
- Cancellation, idle timeout, watchdog stop, and ACP cancellation remain
  exactly once.
- Buffer overflow uses bounded replay/pass-through and does not retry.
- Tool-less, `auto`, and post-tool turns preserve the existing live stream.

### Adapter tests

For OpenAI, Anthropic, and Ollama, in streaming and non-streaming modes:

- first-attempt and corrected tool calls retain their existing native success
  shapes;
- terminal failures return HTTP 502 before response streaming starts;
- error envelopes and codes match D7;
- no first-attempt refusal/wrapper fragments leak;
- privacy receipts and request IDs remain present;
- valid nested dispatcher calls from v3.2.1 remain unchanged.

### Verification gates

- `go test ./internal/engine/...`
- `go test ./internal/adapter/...`
- `go test ./...`
- `go test -race ./internal/engine/... ./internal/adapter/...`
- `go vet ./...`
- `go build ./...`
- Optional live smoke: explicit model succeeds or returns the typed
  recommendation; `auto` continues to complete the GitLab tool flow.

## Anticipated implementation areas

- `internal/engine/` — eligibility, classification, typed errors, corrective
  prompt, bounded preflight/replay stream, and tests.
- `internal/canonical/` — activate/document tool-choice semantics only if a
  small shared normalization helper is needed; do not add surface-specific
  wire types.
- `internal/adapter/openai/`, `internal/adapter/anthropic/`, and
  `internal/adapter/ollama/` — typed error mapping and protocol tests.
- `internal/metrics/` and `cmd/otto-gateway/main.go` — bounded recovery metrics
  wiring.
- Existing privacy/compression hooks — regression tests only; no production
  behavior change is expected.

## Acceptance criteria

1. A client-selected model is either used exactly as requested or rejected
   explicitly; the Gateway never substitutes `auto`.
2. An eligible protocol failure receives at most one corrective prompt using
   the same model and ACP session.
3. A second failure returns a stable native 502 error recommending `auto`.
4. `auto`, tool-less traffic, successful calls, and post-tool final answers are
   unchanged.
5. No failed first-attempt bytes reach a streaming client.
6. Privacy, compression, logging, PostHooks, and request accounting run once.
7. Memory use for preflight classification is bounded.
