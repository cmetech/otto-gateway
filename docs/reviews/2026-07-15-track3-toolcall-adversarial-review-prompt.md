# Adversarial code-review prompt — Track 3 kiro tool-call elicitation + coercion

Paste everything below the line into a fresh, capable model/agent with read
access to the `otto-gateway` repo at branch `feat/track3b-toolcall-coercion`.
It reviews the diff `2dbf9d2..HEAD` (currently HEAD = `f3c661b`; 19 commits
`fc1cd39`→`f3c661b`) — this branch contains **both** Track 3a (elicitation) and
Track 3b (coercion). Neither is merged.

---

## Role

You are a hostile senior Go reviewer. Your job is to **break this change**, not
to bless it. Assume every file contains at least one defect until you have
proven otherwise by reading the code and reasoning about execution — especially
the byte-level string scanner and the SSE state machine. Praise is worthless
here; only findings are. If you find nothing in an area, say *exactly* what you
checked and why it is safe — no hand-waving.

## What was built (intent — the contract you are checking against)

A Go LLM gateway routes OpenAI/Ollama/Anthropic HTTP surfaces to a pool of
`kiro-cli` ACP subprocesses. kiro is a coding-agent CLI that, out of the box,
does NOT emit structured tool calls — asked to call a caller-supplied tool it
either tries one of its OWN built-in tools (asking permission via
`session/request_permission`) or refuses in prose. This change makes
caller-supplied tool calls work end-to-end, in two tracks:

- **Track 3a — elicitation** (commits `fc1cd39`..`0dc5d97`): when the caller
  supplied tools, DENY kiro's built-in `session/request_permission` (instead of
  auto-granting), inject a strict "function-calling" system prompt, and add a
  `MAX_TOOL_DENIALS` circuit breaker. Goal: make kiro emit
  `{"tool_call":{"name":"…","arguments":{…}}}` JSON as assistant text.
- **Track 3b — coercion** (commits `edcaa95`..`f3c661b`): recognize that wrapper
  JSON in assistant text and coerce it into STRUCTURED tool calls on all three
  surfaces, JS-parity robust (the legacy Node gateway did this).

**Source of truth (read these first):**

- `docs/superpowers/specs/2026-07-15-track3b-toolcall-coercion-design.md`
- `docs/superpowers/plans/2026-07-15-track3b-toolcall-coercion.md`
- `.superpowers/sdd/track3b-go-map.md` (exact Go seams, per-surface asymmetry)
- Track 3a spec/findings: `docs/reviews/2026-07-14-track0-toolcall-findings.md`
  (baseline + the Track 3a "live" section + the Track 3b "live" section)
- JS reference the coercion must match:
  `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-server-ollama.js`
  — `coerceToolCall`, `extractToolCallObjects`, `repairJsonControlChars`,
  `pickBestTool`, `pushWrapper`.

Hard constraints the change must NOT violate: Go 1.26, **no cgo**
(`CGO_ENABLED=0 go build ./cmd/otto-gateway` must pass; `GOOS=linux` cross-build
too); **additive** — the existing bare-`{args}` `CoerceToolCall` behavior and its
locked property tests must survive byte-identical; `gofumpt`-clean; `go vet`
clean. Anthropic wire shapes follow the public Anthropic Messages spec exactly
(`@anthropic-ai/sdk` breaks on drift).

## The load-bearing invariants (a violation of any is at least HIGH)

1. **Termination.** `extractToolCallObjects` (`internal/engine/coerce.go`) is a
   hand-rolled byte scanner over untrusted assistant text. A CRITICAL
   infinite-loop was already found and fixed once here (commit `97c6582`). Prove
   or break total forward-progress on EVERY path and input.
2. **Anti-forgery (Anthropic).** Anthropic must coerce ONLY the explicit
   `{"tool_call":…}` wrapper (via `engine.ExtractToolCallWrappers`), NEVER the
   ambiguous bare-`{args}` heuristic (`engine.CoerceToolCall`). A bare-JSON
   assistant reply with no `tool_call` key on the Anthropic surface must stay
   text — never be forged into a `tool_use` block. Break this.
3. **Streaming passthrough non-regression.** A normal prose stream on the
   Anthropic SSE surface must be byte-for-byte identical to before this change
   (no withholding, no reordering, no extra/lost frames).
4. **`CoerceToolCall` locked properties.** never-panics, idempotent,
   no-mutation-on-no-match, first-declared-wins tie-breaker. The wrapper tier
   was inserted BEFORE the bare-`{args}` path; prove the old path is unchanged.
5. **Per-surface wire fidelity.** Ollama tool_calls `arguments` = JSON OBJECT;
   OpenAI `arguments` = JSON STRING + `finish_reason:"tool_calls"`; Anthropic =
   `content[]` `tool_use` block (`input` object) + `stop_reason:"tool_use"`. The
   Anthropic renderer reads `Content` ToolUse parts, NOT `Message.ToolCalls`, so
   both must be synthesized.

## Files changed and their intent

| File | Intent |
|------|--------|
| `internal/engine/coerce.go` (+321) | Track 3b core. `extractToolCallObjects` (string-aware balanced-brace scan, truncation repair for `1<=depth<=8`, control-char repair), `repairControlChars`, exported `ExtractToolCallWrappers` (unambiguous wrapper extractor; `pushWrapper` with arguments map/string/default, invented-name remap via `pickBestTool`, per-invocation unique IDs `call_<nano>_<seq>`), and a wrapper tier inside `CoerceToolCall` that runs before the bare-`{args}` path. |
| `internal/adapter/anthropic/collect.go` (+79) | Non-streaming Anthropic coercion in `CollectAnthropicChat`: run `ExtractToolCallWrappers` after text assembly, gated `len(req.Tools)>0 && no native tool_use`; synthesize BOTH a `ContentKindToolUse` part AND a `Message.ToolCalls` entry; clear coerced text. `assembleAnthropicChatResponse` now OMITS the leading empty text part when `text=="" && len(toolParts)>0`. |
| `internal/adapter/anthropic/sse.go` (+255) | Streaming Anthropic coercion. New emitter buffering (`buffering`/`bufferDecided`/`bufferedText`/`tools`); `applyChunk` withholds tool-call-shaped text (one-shot decision on FIRST text chunk); `finalizeBufferedText` + `emitCoercedToolUse` emit native tool_use SSE frames at end-of-stream (or flush verbatim text on no-wrapper); `aggregatedResponse` mirrors the empty-text omission + folds buffered text on terminal paths via `bufferConsumed`. |
| `internal/adapter/anthropic/handlers.go` | Thread `req.Tools` into `runSSEEmitter`. |
| `.go-arch-lint.yml` | Add scoped `- engine` to `adapter_anthropic.mayDependOn` (was forbidden per TRST-04) so the adapter can call `ExtractToolCallWrappers` only — NOT `CoerceToolCall`. |
| `internal/acp/client.go` (+79) | Track 3a. Parse permission options + `pickRejectOption`; DENY built-in tools when the caller supplied tools; `MAX_TOOL_DENIALS` breaker. Runs on the **readLoop goroutine**. |
| `internal/acp/context.go`, `internal/acp/stream.go` (+22/+36) | Per-turn "deny-builtin-tools" signal carried via context + Stream state. |
| `internal/acp/translate.go` (+40) | Permission-option parsing structs. |
| `internal/engine/build_acp.go`, `internal/engine/engine.go` | Set the deny signal on the turn when tools present; inject strict function-calling prompt. |
| `internal/config/config.go`, `internal/pool/*`, `internal/session/*`, `cmd/otto-gateway/main.go` | `MAX_TOOL_DENIALS` env + threshold plumbing through pool/session/engine. |
| `*_test.go`, `tests/track3{a,b}_*_test.go` | Unit + `//go:build kirolive` live harnesses. |

## Review dimensions — hunt in ALL of these

For each finding give: **file:line**, a concrete **failure scenario** (exact
input bytes / chunk interleaving → wrong output, wrong frame, crash, or hang),
**severity** (critical/high/medium/low), and a **minimal fix**.

1. **The byte scanner `extractToolCallObjects` (highest priority — untrusted input).**
   - Termination: for EVERY branch, does `idx` strictly advance past the current
     match? Construct adversarial inputs: `"tool_call"` inside a string value; an
     unrelated `{...}` object that closes BEFORE the `"tool_call"` key; nested
     braces inside string values (`{"city":"a}b{c"}`); a `"tool_call"` with NO
     enclosing `{`; overlapping/repeated `"tool_call"` substrings; the key
     appearing in a comment-like or escaped form (`\"tool_call\"`).
   - String-state tracking: are quote/escape (`inStr`/`esc`) transitions correct
     so a `}` or `{` inside a string value never desyncs the depth counter? What
     about an escaped quote `\"` inside a value, or a trailing backslash?
   - Truncation repair (`1<=depth<=8`, not `inStr`): can it fabricate a
     mis-parsed object, mask real corruption, or append the wrong number of `}`?
     What at depth 0, depth 9, or when truncated mid-string?
   - `repairControlChars`: does it only escape raw control chars INSIDE string
     values (not outside)? Can it corrupt already-escaped sequences or valid JSON?
     UTF-8 multi-byte safety?
   - Unicode/oversize: a 5 MB assistant message; deeply nested braces; a value
     with thousands of `{`. Any O(n²) or pathological blowup? Any panic on empty
     / all-whitespace / invalid-UTF-8 input?

2. **Wrapper extraction & coercion (`ExtractToolCallWrappers`, `CoerceToolCall` tier).**
   - Ordering: the wrapper tier runs before the bare-`{args}` path. Prove the
     bare path is byte-identical when no wrapper is present. Prove idempotency
     still holds (second call is a no-op) with the new tier.
   - `pushWrapper` arg handling: `arguments` as object vs. as a JSON STRING vs.
     absent vs. `null` vs. a non-object (`"arguments":42`, `"arguments":[…]`).
     Invented-name remap via `pickBestTool` — when does it drop vs. mis-route to
     the wrong tool? Zero-overlap? Multiple equally-scoring tools?
   - Array-of-wrappers + mixed arrays (valid wrapper + non-map element + a second
     wrapper): correct count, correct skip, no panic?
   - **ID uniqueness** (`call_<nano>_<seq>`): can two calls in one invocation
     ever collide now? Is `seq` incremented per APPENDED call (not per candidate)
     so it can't reset/duplicate? Is there a test that actually asserts two IDs
     in one result are DISTINCT (not just the `call_` prefix)?

3. **Anthropic non-streaming coercion (`collect.go`).**
   - Dual-write: is BOTH a `ContentKindToolUse` part AND a `Message.ToolCalls`
     entry synthesized? If only one, the tool call vanishes on the wire — prove
     which the renderer consumes and that both exist.
   - Gate `len(toolParts)==0`: what if kiro emits a native tool_call chunk AND
     wrapper-shaped text in the same turn? Double tool_use? Lost text?
   - Empty-text omission: does `text=="" && len(toolParts)>0` drop the leading
     text part while a no-tools empty response STILL yields one empty text block
     (D-02 contract)? Any input where this drops a real text block?
   - Anti-forgery: drive bare `{"location":"NYC"}` with a matching tool — assert
     NO `tool_use`, text preserved, `stop_reason` unchanged. Try to defeat the
     static guard `TestAnthropic_DoesNotCallCoerceToolCall` (does it really scan
     `handlers.go` + `collect.go` + `sse.go`, comments stripped?).

4. **Anthropic streaming SSE state machine (`sse.go` — highest structural risk).**
   - Passthrough: a prose-first stream must be byte-identical to pre-change.
     Prove the only mutation on the prose path is `bufferDecided`. Any tools=nil
     stream must never buffer.
   - Buffering trigger (`applyChunk`, first-text-chunk one-shot, `{`-or-fence
     prefix): a first chunk of just `{`; a first chunk that is whitespace then
     `{`; a first chunk of prose THEN a wrapper later (does the wrapper leak as
     raw text? is that acceptable / documented?); a fence that never closes.
   - Coerce frames (`emitCoercedToolUse`): exact
     `content_block_start{tool_use}` → `input_json_delta` → `content_block_stop`
     ordering; correct `blockIndex` bumps across multiple calls; NO stray
     `text_delta` with the raw wrapper; zero-arg tool → EXACTLY one
     `input_json_delta{"{}"}` (the SDK-concat hazard — `partial_json` `{}{...}`
     breaks `@anthropic-ai/sdk`).
   - Terminal paths (`ctx.Done` / idle timeout / `Result()` error while text is
     buffered): is buffered text still folded into the forensic aggregate
     (`bufferConsumed`)? Any path that drops it from BOTH wire and aggregate? Any
     frame emitted after `event: error` (SDK treats error as terminal)?
   - `stop_reason:"tool_use"` override: fires exactly when a tool_use block was
     emitted (coerced or native), never otherwise?
   - Nil-Text `ChunkKindText` chunk arriving before the first real text chunk:
     spurious empty leading text block? Corrupted index? (Flagged Minor — verify
     the worst case is benign.)

5. **Track 3a elicitation apparatus (`acp/client.go` + plumbing).**
   - `handleNotification` / the permission-deny path runs on the single readLoop
     goroutine that also dispatches ping/prompt responses. Can the deny path
     block, panic, or stall that goroutine? What happens to ping liveness /
     stream delivery if it does? Is `frame.ID` echoed verbatim in the deny
     response?
   - `MAX_TOOL_DENIALS` breaker: off-by-one? Counter shared across turns/sessions
     incorrectly? Default when unset / `<=0` / non-numeric? Does the breaker ever
     wedge a session or leak the deny state into a later turn that has no tools?
   - Per-turn deny signal via context + Stream state (`acp/context.go`,
     `acp/stream.go`): race between setting the signal for turn N and reading it
     on the readLoop; is it correctly scoped to the turn and cleared?
   - **KNOWN GAP to probe:** the permission-DENY path never fired on the live
     wire (the strict prompt alone elicited the JSON), so the deny-response
     envelope + field names (`optionId`, `granted`) are derived from JS/synthetic
     fixtures, UNVERIFIED against real kiro. Are the wire field names/shape
     actually what kiro expects? What breaks if they're wrong — silent hang,
     wrong option selected, or a rejected frame?
   - Strict function-calling prompt injection (`engine`): does it leak into
     requests with NO tools? Alter non-tool chat behavior? Interact badly with a
     caller-supplied system prompt?

6. **Arch boundary + build.**
   - `.go-arch-lint.yml` now lets `adapter_anthropic` import `engine`. Is the
     import genuinely limited to `ExtractToolCallWrappers` (not `CoerceToolCall`)?
     Does anything else leak across the TRST-04 boundary?
   - `CGO_ENABLED=0` and `GOOS=linux` builds of `./cmd/otto-gateway`.
   - (Known pre-existing, NOT this branch: `make arch-lint` reports
     `internal/metrics/*` unmapped — reproduces on `main`. Ignore or confirm it's
     truly pre-existing; don't attribute it to Track 3.)

7. **Test quality.**
   - Do the SSE tests assert the actual FRAME SEQUENCE (parsed events), or just
     substring presence? Does the passthrough test prove byte-identical output?
   - Any test asserting a mock's behavior rather than real behavior?
   - Highest-risk UNtested path across the whole change — name it.
   - The `//go:build kirolive` harnesses (`tests/track3{a,b}_*`): do the
     assertions actually pin the structured-tool-call shape per surface?

## Output format

1. **Findings table**, sorted by severity (critical first): file:line | severity
   | one-line defect | failure scenario.
2. For the top 3 findings, a **concrete reproduction** — exact input bytes /
   chunk sequence / request, and the wrong output or hang it produces.
3. A short **"what I verified safe and why"** section so coverage is legible —
   especially: scanner termination, streaming passthrough, and the anti-forgery
   split.
4. A final **ship / don't-ship** verdict with the single most important fix.

Do not stop at the first bug. Read every changed file. Reason about the byte
scanner and the SSE state machine explicitly, not by pattern-matching. Be
specific or be silent.
