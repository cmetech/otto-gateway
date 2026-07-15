# Track 3 tool-call elicitation + coercion — adversarial review

- **Date:** 2026-07-15
- **Branch:** `feat/track3b-toolcall-coercion`
- **Reviewed range:** `2dbf9d2..f3c661b`
- **Scope:** Track 3a elicitation and Track 3b coercion across the Ollama,
  OpenAI, and Anthropic surfaces

## Verdict

**DON'T SHIP.** The change has three HIGH-severity defects. The most important
fix is to replace `extractToolCallObjects` with a bounded, single-pass scanner:
the current implementation can be forced into quadratic rescanning and
allocation by a multi-megabyte untrusted assistant response.

The Anthropic SSE path also violates the load-bearing passthrough invariant in
two ways: a whitespace-only first chunk prevents a later wrapper from being
coerced, and false-positive buffering changes frame boundaries and can reorder
interleaved content.

## Findings

| File:line | Severity | Defect | Concrete failure scenario | Minimal fix |
|---|---|---|---|---|
| `internal/engine/coerce.go:487` | **HIGH** | The wrapper scanner has quadratic time and allocation behavior on repeated keys. | For `"{" + strings.Repeat("\"tool_call\" ", 400_000)`, each occurrence searches backward from the key, then scans forward from byte zero across most or all of the roughly 5 MB input. Failed candidates also repeatedly build and parse repaired slices. A model-controlled response can consume disproportionate CPU and memory. | Parse in one forward pass with a string-aware brace stack, associate keys with the currently open object, and enforce explicit input, nesting, candidate, and repair budgets. |
| `internal/adapter/anthropic/sse.go:337` | **HIGH** | Buffer eligibility is permanently decided by a whitespace-only first text chunk. | With tools declared, chunks `"\n "` then `{"tool_call":{"name":"get_weather","arguments":{"city":"Paris"}}}` produce ordinary `text_delta` frames containing the raw wrapper and end with `stop_reason:"end_turn"`; no `tool_use` block is emitted. | Decide eligibility on the first non-whitespace text content while retaining any leading whitespace so it can be replayed exactly if coercion does not occur. |
| `internal/adapter/anthropic/sse.go:709` | **HIGH** | False-positive buffering does not preserve the original stream's frame boundaries or ordering. | With tools declared, ordinary fenced code arriving as `"```go\n"`, `"fmt.Println(\"hi\")\n"`, `"```"` is withheld and emitted as one combined delta at EOF instead of the original three deltas. If a thought or native tool chunk arrives between buffered text chunks, that chunk is emitted immediately and the earlier text is emitted later, reordering content. | Store the original text fragments and replay their original block/delta boundaries on a no-wrapper result. Before emitting any non-text chunk, resolve or flush pending buffered text so wire order cannot change. |
| `internal/engine/coerce.go:495` | **MEDIUM** | `strings.LastIndex` can choose a nested sibling object instead of the wrapper's enclosing object, causing valid wrappers to be missed. | `prefix {"meta":{"x":1},"tool_call":{"name":"get_weather","arguments":{"city":"Paris"}}} suffix` selects the `{` before `"x"`. That object closes before the key, so the scanner advances past `"tool_call"` and returns no call. | Use the open-object stack from a single forward scan to select the actual enclosing object for the key. |
| `tests/track3a_elicitation_test.go:230` | **MEDIUM** | The live harness cannot prove that a permission request was denied. | The capture ring records only kiro-to-gateway frames. `permissionDenied` is therefore always false, while seeing `session/request_permission` can still be reported as useful evidence. A regression that responds with `granted:true`, chooses the wrong option, or writes a malformed response can pass this harness. | Capture outbound gateway frames or inspect structured gateway logs, then assert the response ID, selected rejection `optionId`, and `granted:false` shape. |
| `tests/track3b_coercion_test.go:68` | **MEDIUM** | The live Track 3b harness never exercises Anthropic streaming coercion. | It reuses the Track 3a non-streaming probes. The new `sseEmitter` state machine can regress while all three live surface checks still pass. | Add `stream:true` probes per surface; for Anthropic, parse SSE events and assert the exact `tool_use` event sequence, indices, JSON delta, and terminal stop reason. |
| `docs/reviews/2026-07-15-track3-toolcall-adversarial-review-prompt.md:46` | **LOW** | The prompt's relative path to the JS parity reference is incorrect from this repository. | `../gitlab.rosetta.ericssondevops.com/...` resolves under `otto_app`, where the file does not exist. The actual checkout is `/Users/coreyellis/code/gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-server-ollama.js`, so a fresh reviewer may silently skip the source of truth. | Use the correct relative path (`../../../../gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-server-ollama.js`) or a repository-independent locator. |
| `docs/reviews/2026-07-14-track0-toolcall-findings.md:445` | **LOW** | The reviewed branch adds whitespace errors to a tracked review document. | `git diff --check 2dbf9d2..f3c661b` reports trailing whitespace on added blank lines 445, 455, 486, 496, 527, and 537. | Remove the spaces from those blank lines. |

## Top-three reproductions

### 1. Quadratic wrapper scanning

Use a declared `get_weather` tool and call
`engine.ExtractToolCallWrappers(text, tools)` with:

```go
text := "{" + strings.Repeat("\"tool_call\" ", 400_000)
```

This is roughly 5 MB. For every key occurrence, the implementation at
`internal/engine/coerce.go:487` searches for an opening brace from the start and
rescans forward from that brace. It also repeatedly attempts truncation repair,
allocation, and JSON parsing. The call eventually returns no wrapper, but the
work grows quadratically with the number of repeated key strings. This is a
denial-of-service input because assistant text is untrusted.

### 2. Whitespace chunk disables Anthropic streaming coercion

Declare `get_weather`, start an Anthropic streaming response, and feed these
canonical text chunks in order:

```text
1. "\n "
2. "{\"tool_call\":{\"name\":\"get_weather\",\"arguments\":{\"city\":\"Paris\"}}}"
```

Chunk 1 sets `bufferDecided=true`, but its trimmed content matches neither `{`
nor a fence. Chunk 2 cannot reconsider the decision, so the raw wrapper is sent
as text. The wrong wire result contains two text deltas and ends with
`message_delta.stop_reason = "end_turn"`. The required result is a native
`tool_use` content block followed by `stop_reason = "tool_use"`.

### 3. Fenced-code false positive changes the stream

Declare any tool, then feed a normal fenced-code answer as three canonical text
chunks:

```text
1. "```go\n"
2. "fmt.Println(\"hi\")\n"
3. "```"
```

The first chunk activates buffering. No wrapper exists, so finalization emits
one combined `text_delta` at EOF. Before this change, the wire carried three
deltas as they arrived. The content bytes concatenate to the same string, but
the SSE bytes and delivery timing are not identical, violating the explicit
passthrough invariant. Inserting a thought chunk between chunks 1 and 2 makes
the semantic defect worse: the thought is emitted before the earlier buffered
text, so the content order changes.

## What I verified safe and why

- **Scanner termination:** every outer-loop branch advances `idx` by either
  `keyLen` or `end + 1`; the previously fixed unrelated-object case no longer
  loops forever. The current defect is complexity and selection correctness,
  not a demonstrated infinite loop.
- **Brace and escape state:** the inner scan tracks quoted strings, backslashes,
  and escaped quotes correctly enough that braces inside a JSON string do not
  change depth. `repairControlChars` changes only raw newline, carriage-return,
  and tab bytes while inside a string; already escaped sequences and UTF-8
  continuation bytes are left unchanged.
- **Anthropic anti-forgery:** production Anthropic code calls only
  `ExtractToolCallWrappers`; it does not call the ambiguous `CoerceToolCall`
  heuristic. A bare assistant object such as `{"location":"NYC"}` remains text.
- **Non-streaming Anthropic dual write:** coerced calls populate both a
  `ContentKindToolUse` content part and `Message.ToolCalls`. Pure tool responses
  omit the otherwise empty leading text block, while the no-tool empty-response
  contract remains intact.
- **Coerced SSE wire shape:** on the coercion path, the emitter produces
  `content_block_start` with `tool_use`, one `input_json_delta`, then
  `content_block_stop`; block indices advance between calls. A zero-argument
  call emits exactly one `{}` JSON delta.
- **ID uniqueness:** wrapper calls in a single extraction invocation receive
  distinct `call_<nano>_<seq>` IDs because the sequence increments for each
  appended call.
- **Legacy bare-object behavior:** the existing no-wrapper path remains behind
  the new wrapper tier, and the locked tests continue to cover idempotency,
  no-mutation-on-no-match, never-panic behavior, and first-declared tie-breaking.
- **Track 3a turn scoping:** the deny flag is set before the stream becomes
  active, protected by the stream mutex, and reset per stream. No cross-turn
  leak was found in the inspected state transitions.

The streaming prose-first fast path is unchanged once the first real text chunk
does not look wrapper-shaped. That limited path is safe; the two HIGH findings
above show why the broader byte-identical passthrough invariant is not met.

## Verification evidence

- `go test ./...` — passed.
- `go test -race ./internal/engine ./internal/adapter/... ./internal/acp` — passed.
- `go vet ./...` — passed.
- Native and Linux amd64 `CGO_ENABLED=0` gateway builds — passed.
- `/Users/coreyellis/go/bin/gofumpt -d` over changed Go files — clean.
- `make arch-lint` — failed only on the known pre-existing unmapped
  `internal/metrics` files; both the files and the absent mapping are present at
  base commit `2dbf9d2`, so this is not attributed to Track 3.
- `git diff --check 2dbf9d2..f3c661b` — failed only for the six tracked-document
  whitespace errors recorded as the LOW finding above.

## Required remediation before ship

1. Replace the wrapper extractor with a bounded, single-pass parser and add
   adversarial size/complexity tests plus the nested-sibling regression case.
2. Redesign Anthropic SSE buffering to defer its decision across whitespace,
   replay original fragments on false positives, and preserve order across
   non-text chunks.
3. Add live streaming probes and make the Track 3a live harness observe and
   assert the actual outbound denial response.

After those changes, rerun the full test/race/vet/build gates and the adversarial
scanner/SSE regression cases before reconsidering shipment.
