---
phase: 06-tool-call-path
reviewed: 2026-05-27T00:00:00Z
depth: standard
files_reviewed: 47
files_reviewed_list:
  - .go-arch-lint.yml
  - internal/acp/integration_test.go
  - internal/acp/translate.go
  - internal/acp/translate_test.go
  - internal/adapter/anthropic/collect.go
  - internal/adapter/anthropic/collect_test.go
  - internal/adapter/anthropic/handlers.go
  - internal/adapter/anthropic/handlers_test.go
  - internal/adapter/anthropic/render.go
  - internal/adapter/anthropic/render_test.go
  - internal/adapter/anthropic/sse.go
  - internal/adapter/anthropic/sse_golden_test.go
  - internal/adapter/anthropic/sse_test.go
  - internal/adapter/anthropic/wire.go
  - internal/adapter/anthropic/wire_test.go
  - internal/adapter/ollama/handlers.go
  - internal/adapter/ollama/handlers_test.go
  - internal/adapter/ollama/ndjson.go
  - internal/adapter/ollama/ndjson_test.go
  - internal/adapter/ollama/render.go
  - internal/adapter/ollama/render_test.go
  - internal/adapter/ollama/wire.go
  - internal/adapter/openai/handlers.go
  - internal/adapter/openai/render.go
  - internal/adapter/openai/render_test.go
  - internal/adapter/openai/sse.go
  - internal/adapter/openai/sse_golden_test.go
  - internal/adapter/openai/sse_test.go
  - internal/adapter/openai/wire.go
  - internal/adapter/openai/wire_test.go
  - internal/canonical/chunk.go
  - internal/engine/build_acp.go
  - internal/engine/build_acp_test.go
  - internal/engine/coerce.go
  - internal/engine/coerce_test.go
  - internal/engine/collect.go
  - internal/engine/collect_test.go
  - tests/e2e/cmd/fake-kiro-cli/main.go
  - tests/e2e/e2e_test.go
  - tests/e2e/tools_anthropic_test.go
  - tests/e2e/tools_cancel_test.go
  - tests/e2e/tools_fixtures_test.go
  - tests/e2e/tools_ollama_test.go
  - tests/e2e/tools_openai_test.go
  - tests/e2e/tools_testmain_test.go
findings:
  critical: 2
  warning: 10
  info: 6
  total: 18
status: issues_found
---

# Phase 6: Code Review Report

**Reviewed:** 2026-05-27T00:00:00Z
**Depth:** standard
**Files Reviewed:** 47
**Status:** issues_found

## Summary

Phase 6 adds the kiro-native tool-call pathway across three surfaces, plus
streaming-coerce buffering, a per-surface population contract, and an
extensive E2E matrix. The architecture is sound and the documentation /
test density is unusually high. The findings below focus on edge cases
where the implementation diverges from its own contracts or where defensive
behaviour leaks a regression surface.

Two BLOCKER findings concern (a) a panic risk in `tests/e2e/tools_fixtures_test.go`
when E2E suite runs are misconfigured (FakeKiro called before TestMain),
and (b) a wire-shape inconsistency on the Ollama non-streaming error path
that contradicts the documented T-02-33 mitigation policy used by the
other two surfaces.

WARNINGS cluster around: (a) a streaming-coerce ordering hole where prose
that PRECEDES a JSON object can leak to the wire before buffering engages,
(b) ambiguous nil-slice vs empty-slice marshaling in the Phase 6 wire
shapes, (c) several test fakes that paper over real engine behaviour by
synthesizing chunks from collect responses, and (d) dead-code / minor
style issues in the engine + render layers.

No critical security vulnerabilities found. The "input":{} pointer-to-
empty-map pattern (CR-01 Pitfall 1) is correctly applied everywhere
Anthropic tool_use renders. The OpenAI vs Ollama wire-shape divergence
(arguments-as-string vs arguments-as-object) is consistently enforced and
has byte-level test canaries. The per-surface NO-COERCE asymmetry for
Anthropic (D-01) is locked by both a behavioural test and a static-source
guard.

## Structural Findings (fallow)

No `<structural_findings>` block was supplied to this review pass. The
narrative findings below are derived purely from direct file inspection.

## Narrative Findings (AI reviewer)

## Critical Issues

### CR-01: Ollama non-streaming error path echoes raw engine error string to client (T-02-33 inconsistency)

**File:** `internal/adapter/ollama/handlers.go:78-80, 207-209`
**Severity:** BLOCKER

The Ollama `handleChat` and `handleGenerate` non-streaming branches
write `err.Error()` directly into the client-visible response body
when `eng.Collect` fails:

```go
resp, err := eng.Collect(r.Context(), req)
if err != nil {
    writeError(w, http.StatusInternalServerError, err.Error())
    return
}
```

This contradicts the T-02-33 policy enforced by the Anthropic
(`internal/adapter/anthropic/handlers.go:165-167`) and OpenAI
(`internal/adapter/openai/handlers.go:107-110`) surfaces, which log
the raw error via `slog.Error` and return a generic `"internal error"`
to the client. The Ollama writeError comment (`handlers.go:417-419`)
asserts that engine errors are wrapped at the engine boundary and do
not echo request bodies — but that is a downstream invariant, not a
guarantee, and ANY future error path that includes a fragment of the
prompt (e.g., a kiro-cli stderr leak, a path-traversal validation
error, an `acp/translate.go` `truncateForLog` payload) would leak
directly to the response body.

The TestHandleChat_EngineError_500 test (`handlers_test.go:194-206`)
locks the current sanitised wrap shape but does not prevent a future
error path from regressing.

**Fix:**

```go
resp, err := eng.Collect(r.Context(), req)
if err != nil {
    a.cfg.Logger.Error("ollama: engine.Collect error", "err", err)
    writeError(w, http.StatusInternalServerError, "internal error")
    return
}
```

Apply the same change to `handleGenerate` (`handlers.go:207-209`).
Update `TestHandleChat_EngineError_500` to assert the body contains
`"internal error"` and does NOT contain `"kiro exploded"`.

---

### CR-02: `FakeKiro` panics on nil binary path when invoked without TestMain initialisation

**File:** `tests/e2e/tools_fixtures_test.go:155-157`
**Severity:** BLOCKER

```go
func FakeKiro(t *testing.T, script Script) (cmd string, env map[string]string) {
    t.Helper()
    if fakeKiroBinaryPath == "" {
        t.Fatal("fakeKiroBinaryPath not initialized — TestMain (e2e_test.go) must run first; check OTTO_E2E=1 gate")
    }
    ...
```

The `t.Fatal` guard correctly handles the OTTO_E2E=0 case, but the
`TestFakeKiro_BinaryExistsAfterMultipleSubtests`
(`tools_testmain_test.go:58-80`) test calls `gateOrSkip(t)` FIRST and
then calls `FakeKiro`. If OTTO_E2E=1 is set but the build of the fake
binary fails inside `TestMain` (the build error path calls
`os.Exit(2)` at `e2e_test.go:111`), the test process never reaches
m.Run() so this code is unreachable — that path is safe.

However, if a future maintainer adds a NEW test file that imports
`FakeKiro` and forgets to drive it through `TestMain`'s build step
(e.g., a misconfigured `go test -tags e2e -run TestSomeOtherSuite`
that imports the fixtures package WITHOUT triggering the e2e suite
TestMain), then `fakeKiroBinaryPath` is empty and the t.Fatal fires
on the first subtest. The test fails loudly, which is the desired
behavior — BUT the surrounding `defer GoleakVerifyAtEnd(t)` (line 37
of `tools_anthropic_test.go` etc.) runs FIRST during the t.Fatal
cleanup, and `goleak.Find` may still detect goroutines from the
half-initialized test harness, producing a confusing double-error.

Additionally, the `os.Exit(2)` path at `e2e_test.go:111` SKIPS the
defer that removes the temp dir (`defer func() { _ = os.RemoveAll(tmp) }()`)
because `os.Exit` bypasses deferred functions. The temp directory
leaks on every e2e suite invocation where the fake-kiro-cli build
fails. Same for the gateway-binary build failure at `e2e_test.go:90`.

**Fix:**

Remove `os.Exit(2)` calls in favour of returning a non-zero code via
`m.Run()` wrap, OR refactor to a helper that builds, defers cleanup,
and only then calls m.Run:

```go
func TestMain(m *testing.M) {
    if os.Getenv("OTTO_E2E") != "1" {
        os.Exit(m.Run())
    }
    code := runE2E(m)
    os.Exit(code)
}

func runE2E(m *testing.M) int {
    tmp, err := os.MkdirTemp("", "otto-e2e-")
    if err != nil {
        fmt.Fprintf(os.Stderr, "e2e: MkdirTemp: %v\n", err)
        return 1
    }
    defer func() { _ = os.RemoveAll(tmp) }()

    out := filepath.Join(tmp, "otto-gateway")
    // ... build steps ...
    if buildErr != nil {
        return 1
    }
    fakeKiroAbs := filepath.Join(os.TempDir(), fmt.Sprintf("fake-kiro-cli-%d", os.Getpid()))
    defer func() { _ = os.Remove(fakeKiroAbs) }()
    // ... fake-kiro build ...
    fakeKiroBinaryPath = fakeKiroAbs

    return m.Run()
}
```

This ensures the temp dirs and binaries are always cleaned up even
when the build step fails, and removes the `os.Exit` shortcut that
prevented `defer` from running.

## Warnings

### WR-01: Streaming-coerce buffering misses prose that PRECEDES the JSON object

**File:** `internal/adapter/ollama/ndjson.go:75-84` and `internal/adapter/openai/sse.go:259-281`
**Severity:** WARNING

Both adapters' `shouldBuffer` / `applyTextChunk` decisions use
`textBuffer.String() + frag` to probe for JSON-shaped text:

```go
// ollama/ndjson.go
func (s *emitterState) shouldBuffer(req *canonical.ChatRequest, newText string) bool {
    if req == nil || len(req.Tools) == 0 {
        return false
    }
    combined := strings.TrimSpace(s.textBuffer.String() + newText)
    if combined == "" {
        return false
    }
    return strings.HasPrefix(combined, "{") || strings.HasPrefix(combined, "```")
}
```

But `textBuffer` is only WRITTEN INSIDE the buffering branch. So if
chunk #1 is `"Here's the answer: "` (preamble), `shouldBuffer`
returns false → flushed to wire, NOT written to textBuffer. Chunk #2
is `{"location":"NYC"}` (JSON). `shouldBuffer` probes
`"" + frag = "{\"location\":\"NYC\"}"` → HasPrefix("{") → true →
buffering engages. At stream end, coerce hits, deferredTextLines
(only the JSON portion) is DISCARDED, synthesized tool_calls fires.

Client wire sequence:
1. NDJSON line carrying `"Here's the answer: "` (already flushed)
2. Final `done:true` line with `tool_calls: [get_weather]`

A reasonable LangChain harness that emits prose then a tool-call JSON
sees the preamble text leak BEFORE the tool_call. The Pitfall 3
"entire text" requirement of `stripFences`/`CoerceToolCall` says the
JSON must be the entire response — but this streaming heuristic
allows preamble text to escape, splitting the response into two
semantic shapes.

The OpenAI version (`sse.go:262-267`) has the identical bug —
`probe := e.textBuffer.String() + frag` only considers the
already-buffered text, not the previously-flushed prose.

**Fix:**

Either:
1. Buffer ALL text deltas from the first chunk forward (when
   `req.Tools` non-empty) and defer the buffer/flush decision to
   stream end. Tradeoff: TTFB regression for all tool-enabled
   streaming responses.
2. Document the limitation explicitly: streaming-coerce only fires
   when the FIRST text chunk starts with `{` or ```` ``` ````.
   Add a test that locks the no-leak behaviour for "prose then JSON".
3. Track total emitted text alongside `textBuffer` and refuse to
   buffer once non-buffered text has been flushed — preserves Pitfall
   3 "entire text" by refusing to coerce a split stream.

Option 3 is the closest match to the existing Pitfall 3 invariant.
Add a test in `ndjson_test.go` and `sse_test.go` (OpenAI) that
exercises the prose-then-JSON sequence and asserts no coerce fires.

---

### WR-02: Anthropic `argsJSON == "null"` coerce is conditioned on the wrong predicate

**File:** `internal/adapter/anthropic/sse.go:340-352`
**Severity:** WARNING

```go
argsJSON, err := json.Marshal(c.ToolCall.Args)
if err != nil {
    // ...drop
}
// If Args was nil/empty, json.Marshal produces "null" — coerce
// to "{}" so the wire carries a valid empty object literal
// inside partial_json. (Defensive: ...)
if string(argsJSON) == "null" {
    argsJSON = []byte("{}")
}
```

`json.Marshal(map[string]any(nil))` produces `"null"`.
`json.Marshal(map[string]any{})` produces `"{}"`.

So this coercion only handles the nil-map case. But the COMMENT says
"nil/empty". If a kiro-cli future build emits an explicit
non-allocated zero-element map, this still produces `"{}"`. Fine.
But the comment is misleading: an empty non-nil map already produces
"{}" without the coerce.

More importantly, the same `partial_json` field receives the args
string. If a CR-01-style nil → empty-object discipline applies (and
the surrounding code in this file invokes that discipline for
`content_block_start` via the pointer-to-empty-map trick), then
`partial_json: "null"` would be the visible wire shape WITHOUT this
coerce. Anthropic's `@anthropic-ai/sdk` MessageStream parser
rejects `null` in `partial_json` for the same reason it rejects
`null` in `input` — undefined behaviour.

This is defensible but not symmetric with the `content_block_start`
input field, which uses pointer-to-empty-map. The inconsistency means
two different defensive patterns are in use for the same problem.

**Fix:**

Document the discipline difference in the function header, OR
unify on a single pattern. If unifying, prefer the pointer-to-empty-
map approach (used by `toolUseBlockHeader.Input`) and ALSO use it for
the `partial_json` field's underlying source: instead of
`json.Marshal(c.ToolCall.Args)` then string-coerce, do:

```go
args := c.ToolCall.Args
if args == nil {
    args = map[string]any{}
}
argsJSON, err := json.Marshal(args)
```

This produces `"{}"` for the nil case without a string compare and
documents the discipline in one place.

---

### WR-03: `parityFakeEngine.Run` silently discards the scripted error on Run, drives Stream.Result error path only

**File:** `internal/adapter/anthropic/collect_test.go:107-122`
**Severity:** WARNING

```go
func (f *parityFakeEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
    ch := make(chan canonical.Chunk, len(f.chunks))
    for _, c := range f.chunks {
        ch <- c
    }
    close(ch)
    return &fakeRunHandle{
        stream: &fakeStream{
            chunks: ch,
            final:  f.final,
            err:    f.err,
        },
        sessionID: "parity-fake",
    }, nil
}
```

`parityFakeEngine.Collect` returns `f.err` directly — exercising the
"engine: collect: <err>" wrap path. But `parityFakeEngine.Run` ALWAYS
returns `(handle, nil)` regardless of `f.err`. The `f.err` is then
surfaced via Stream.Result(), exercising the "engine: collect result:
<err>" path inside `CollectAnthropicChat`.

This means the parity test `TestCollectAnthropicChat_ParityWithEngine_ErrorPropagation`
exercises DIFFERENT error paths on the reference (Collect) vs the
SUT (Run + Stream.Result). The `errors.Is(err, sentinel)` check
passes because both paths wrap the same sentinel — but a future
refactor that breaks the wrap shape on ONE path but not the other
would not be caught.

**Fix:**

Add a second fake-engine that returns `f.err` from `Run` itself, and
add a parity test that uses it. Alternatively, parameterise the
existing fake:

```go
type parityFakeEngine struct {
    chunks []canonical.Chunk
    final  *canonical.FinalResult
    err    error
    errOnRun bool // when true, Run returns err; when false, Stream.Result returns err
}

func (f *parityFakeEngine) Run(...) (RunHandle, error) {
    if f.errOnRun && f.err != nil {
        return nil, f.err
    }
    // ... rest unchanged
}
```

Then add a parallel test row that flips `errOnRun=true` and asserts
the same sentinel propagation.

---

### WR-04: `synthesizeRunHandleFromCollectResp` masks real ChunkKind semantics in handler tests

**File:** `internal/adapter/anthropic/handlers_test.go:84-136`
**Severity:** WARNING

```go
func synthesizeRunHandleFromCollectResp(resp *canonical.ChatResponse, scriptedErr error) *fakeRunHandle {
    var chunks []canonical.Chunk
    for _, part := range resp.Message.Content {
        switch part.Kind {
        case canonical.ContentKindText:
            chunks = append(chunks, canonical.Chunk{
                Kind: canonical.ChunkKindText,
                Text: &canonical.TextChunk{Content: part.Text},
            })
        // ... etc
        }
    }
    // Also replay any preexisting ToolCalls (some tests construct
    // these without a corresponding ContentKindToolUse part).
    for _, tc := range resp.Message.ToolCalls {
        chunks = append(chunks, canonical.Chunk{
            Kind: canonical.ChunkKindToolCall,
            ToolCall: &canonical.ToolCallChunk{
                ID:   tc.ID,
                Name: tc.Name,
                Args: tc.Arguments,
            },
        })
    }
    ...
}
```

This shim lets legacy `collectResp`-based tests run against the new
`CollectAnthropicChat` aggregator. But it re-synthesizes chunks from
the response AND from any pre-existing ToolCalls — meaning a test
that constructs `resp.Message.ToolCalls = []ToolCall{...}` and
EXPECTS that to be the canonical post-aggregation shape now receives
TWO copies: the synthesized ChunkKindToolCall (from the loop) plus
the original entry on `resp.Message.ToolCalls`. The Anthropic-local
aggregator then appends a SECOND tool_use part for each scripted
ToolCall.

The TestHandleMessages_Happy_NonStreaming
(`handlers_test.go:351-405`) test still passes because the test
fixture doesn't carry pre-existing ToolCalls. But any future test
that uses both `collectResp.Message.Content` AND
`collectResp.Message.ToolCalls` will see duplication that masks the
real CollectAnthropicChat behaviour.

**Fix:**

Either:
1. Stop synthesizing ChunkKindToolCall from the legacy `ToolCalls`
   slice. Tests that want to assert a pre-populated ToolCalls field
   should bypass the synthesizer and use a custom RunHandle.
2. Document the contract — "if you set collectResp.Message.ToolCalls,
   the synthesizer treats those as kiro-native tool_call chunks; do
   NOT additionally set ContentKindToolUse content parts" — with a
   build-tag/`go:linkname`-style assertion that panics on overlap.

Option 1 is the safer choice. Replace lines 113-122 (the second
loop) with:

```go
// Note: do NOT synthesize chunks from resp.Message.ToolCalls.
// Tests that need pre-populated ToolCalls must construct their
// own RunHandle directly (set runHandle on fakeEngine).
```

Add a test that fails when both Content[...]ContentKindToolUse and
ToolCalls are set on the same `collectResp`, to lock the discipline.

---

### WR-05: `joinThinkingContent` and `joinTextContent` duplicate logic across three adapters

**File:** `internal/adapter/ollama/render.go:163-201`, `internal/adapter/openai/render.go:336-348`
**Severity:** WARNING

The exact same loop pattern is implemented three times (Ollama
`joinTextContent`/`joinThinkingContent`, OpenAI `joinTextContent`,
engine `joinTextParts`/`joinThinkingParts`):

```go
func joinTextContent(parts []canonical.ContentPart) string {
    if len(parts) == 0 {
        return ""
    }
    out := ""
    for _, p := range parts {
        if p.Kind == canonical.ContentKindText {
            out += p.Text
        }
    }
    return out
}
```

Both adapter copies use `out += p.Text` (string concatenation in a
loop — O(n²) memory allocation). The engine version uses
`strings.Builder`. The Phase 6 wire-shape divergence is small enough
that this is fine in absolute terms, but it's a maintenance hazard:
if the engine version gets a bug fix (e.g., a unicode normalization
step), the adapter copies stay stale.

**Fix:**

Either:
1. Hoist `joinTextParts` and `joinThinkingParts` from engine to
   canonical (as `canonical.JoinTextParts` / `JoinThinkingParts`).
   The TRST-04 layering still permits canonical to expose these
   helpers because they operate purely on canonical types.
2. Keep the adapter copies but switch them to `strings.Builder` for
   consistency with the engine version.

Option 1 is cleaner. The `.go-arch-lint.yml` already allows every
adapter to depend on `canonical`, so no boundary change is needed.

---

### WR-06: `chatResponseToWire` `promptTokens := estimateTokens("")` is dead code

**File:** `internal/adapter/ollama/render.go:42-49`
**Severity:** WARNING

```go
promptTokens := 0
if resp != nil {
    // Best-effort prompt token estimate from the system + any user
    // turns we can see. Phase 2 does not retain the prompt at
    // render time, so this is approximate — Node uses the same
    // estimator and accepts the approximation.
    promptTokens = estimateTokens("")
}
```

`estimateTokens("")` returns `(0 + 3) / 4 = 0`. The branch is
guarded on `resp != nil` but the value is unconditionally 0. This
is a confusing dead-code site that obscures intent: the variable
exists but always evaluates to 0.

**Fix:**

Either:
1. Remove the variable and inline `PromptEvalCount: 0` in the struct
   literal.
2. Wire through the real prompt text (probably out of Phase 6 scope —
   needs a `canonical.ChatRequest` parameter at the render layer).

Option 1 is the immediate fix; track option 2 as a Phase 8 task.

---

### WR-07: `chatResponseToTextCompletion` struct field tag alignment looks correct but tests don't cover the textCompletion-specific Usage shape

**File:** `internal/adapter/openai/render.go:275-294`
**Severity:** WARNING

```go
type textCompletion struct {
    ID      string       `json:"id"`
    Object  string       `json:"object"`
    Created int64        `json:"created"`
    Model   string       `json:"model"`
    Choices []textChoice `json:"choices"`
    Usage   completionUsage `json:"usage"`
}
```

The `Usage` field uses the same `completionUsage` shape as chat
completions, but the OpenAI text_completion spec specifies a slightly
different envelope (text_completion endpoint legacy). Neither
TestChatCompletions_NonStream nor any other test under
`internal/adapter/openai/` exercises `chatResponseToTextCompletion`'s
output shape end-to-end. The function is reached only through
`/v1/completions` in `handleCompletions`. The test
`TestChatCompletions_NonStream` covers `chatResponseToCompletion`
(the chat path).

If `/v1/completions` is a Phase 3 D-03 forward-compat shim that
isn't load-bearing for any current client, the lack of coverage is
defensible — but the function exists and is wired up. A render
defect here would surface only via integration tests.

**Fix:**

Add `TestChatResponseToTextCompletion` covering at minimum:
- Object literal is `"text_completion"`.
- ID prefix is `"cmpl-"` (not `"chatcmpl-"`).
- Choices[0].Logprobs renders as JSON null.
- Choices[0].Text carries the joined text.
- FinishReason is mapped correctly.

---

### WR-08: `internal/adapter/openai/wire.go` `completionWireRequest.MaxTokens` is `json.RawMessage` but accept-and-ignore expects an int

**File:** `internal/adapter/openai/wire.go:303`
**Severity:** WARNING

```go
type completionWireRequest struct {
    ...
    MaxTokens json.RawMessage `json:"max_tokens,omitempty"`
}
```

The chat-completion struct (`chatCompletionRequest.MaxTokens`,
line 34) is `int`, the completion struct (`completionWireRequest.MaxTokens`,
line 303) is `json.RawMessage`. The asymmetry is intentional per
the D-03 accept-and-ignore convention (legacy completions endpoint
accepts the field but ignores the value), but a client that sends
`{"max_tokens": 100}` to /v1/completions sees its value silently
dropped while the same payload to /v1/chat/completions sets
`req.MaxTokens = 100` and propagates it into the canonical request.

**Fix:**

Either:
1. Document this asymmetry inline with a code comment, or
2. Make `completionWireRequest.MaxTokens` an `int` and propagate it
   into the canonical request (similar to chat completions).

If the legacy /v1/completions endpoint truly never honors max_tokens
in this gateway (kiro-cli backend doesn't expose it), option 1 is
fine — but the comment at line 303 (`accept-and-ignore`) is
load-bearing for understanding the field's design intent and the
code path needs explicit documentation.

---

### WR-09: `tools_cancel_test.go` `time.Sleep(300 * time.Millisecond)` is a flaky timing dependency

**File:** `tests/e2e/tools_cancel_test.go:84, 128, 170`
**Severity:** WARNING

```go
cancel()
_ = resp.Body.Close()

// Wait for cancel propagation.
time.Sleep(300 * time.Millisecond)

// Assert session/cancel emission via frame-log read.
frames := ReadFakeKiroFrames(t, framesPath)
```

A 300ms sleep is a flaky timing dependency on CI. The actual
cancel-propagation time depends on:
- Goroutine scheduling between the HTTP server, the gateway-side
  watchdog, and the fake-kiro stdin reader.
- The OS pipe buffer behaviour for the JSON-RPC framer.
- Whether the fake-kiro process is paged out at the moment of
  cancel.

On a loaded CI runner with limited cores, 300ms may not be enough.
On a fast dev box it's wasteful. The test passes in steady state but
will flake intermittently.

**Fix:**

Poll the frame-log file with a tight loop instead:

```go
// Poll for session/cancel emission with a tight loop and a longer
// total deadline. Avoids a fixed sleep that's both flaky and wasteful.
deadline := time.Now().Add(5 * time.Second)
var frames []map[string]any
for time.Now().Before(deadline) {
    frames = ReadFakeKiroFrames(t, framesPath)
    found := false
    for _, frame := range frames {
        if m, _ := frame["method"].(string); m == "session/cancel" {
            found = true
            break
        }
    }
    if found {
        break
    }
    time.Sleep(20 * time.Millisecond)
}
assertSessionCancelEmitted(t, frames)
```

This converts the sleep into a polling loop with a longer worst-case
deadline and a faster best-case path.

---

### WR-10: Anthropic `mapAnthropicRole` silently maps `"tool"` and `"system"` to `RoleUser`, swallowing client errors

**File:** `internal/adapter/anthropic/wire.go:446-453`
**Severity:** WARNING

```go
func mapAnthropicRole(s string) canonical.MessageRole {
    switch s {
    case "assistant":
        return canonical.RoleAssistant
    default:
        return canonical.RoleUser
    }
}
```

The Anthropic Messages API allows only `"user"` and `"assistant"` at
the message-level role. The default-to-RoleUser fallback is correct
for unknown roles per the documented permissive-decode policy
(D-10), but it ALSO silently maps `"system"` (which is a client
mistake — system goes in the top-level field) to RoleUser. A
client that incorrectly puts a system message inside the messages
array sees its system content joined into the conversation as a
user turn. The test `TestWire_MapAnthropicRole` (`wire_test.go:338-355`)
explicitly verifies this behavior — meaning it's a deliberate
contract.

But the contract differs from the OpenAI surface, which hoists
`"system"` into the top-level System field
(`internal/adapter/openai/wire.go:142-150`). The asymmetry isn't
documented in either wire.go file.

**Fix:**

Document the deliberate divergence inline:

```go
// mapAnthropicRole ... unknown roles (including "system" and "tool")
// default to RoleUser because Anthropic's Messages API spec
// allows ONLY "user" and "assistant" at the message-level role.
// "system" lives in the top-level system field; "tool_result" lives
// as a content block. A client that sends a message with role:"system"
// is making a request-shape mistake — we treat it as user content
// rather than rejecting, per D-10 permissive decode. This is
// intentionally DIFFERENT from the OpenAI surface, which hoists
// "system" into the top-level System field
// (internal/adapter/openai/wire.go:142).
```

## Info

### IN-01: `internal/engine/coerce.go` `pickBestTool` does not handle a `Parameters` value that is `nil` map literal vs absent key

**File:** `internal/engine/coerce.go:216-229`
**Severity:** INFO

```go
func extractProperties(spec *canonical.ToolSpec) map[string]any {
    if spec == nil || spec.Parameters == nil {
        return nil
    }
    raw, ok := spec.Parameters["properties"]
    if !ok {
        return nil
    }
    props, ok := raw.(map[string]any)
    if !ok {
        return nil
    }
    return props
}
```

The early-return on `spec.Parameters == nil` and the `props == nil`
return are both correct. `len(props) == 0` happens to also return
zero score in the caller, so the function handles the empty-but-
non-nil case via `score == 0` → no update. This is correct but
worth noting: if a tool spec has `parameters: {"type":"object",
"properties":{}}`, it's never selected (zero overlap), which is
the desired behavior. Minor: the function returns `nil` instead of
`map[string]any{}` for the empty case; the caller's `len(props) == 0`
check handles both, but returning `map[string]any{}` would be
more consistent.

**Fix:** Style improvement only — no functional change needed.
Consider documenting that nil-map and empty-map are treated
equivalently.

---

### IN-02: `internal/adapter/anthropic/handlers_test.go` `stripGoComments` does not handle backslash-escape inside string literals

**File:** `internal/adapter/anthropic/handlers_test.go:798-861`
**Severity:** INFO

```go
// String literal — skip past so a "//" or "/*" inside a
// string does not get treated as a comment opening.
if src[i] == '"' {
    sb.WriteByte(src[i])
    i++
    for i < n && src[i] != '"' {
        if src[i] == '\\' && i+1 < n {
            sb.WriteByte(src[i])
            sb.WriteByte(src[i+1])
            i += 2
            continue
        }
        sb.WriteByte(src[i])
        i++
    }
    ...
}
```

The string-literal handling does handle `\\` escapes (well-done),
but the function header explicitly says it doesn't handle
`//`-in-string-literals as a comment. For the current narrow use
case (verifying `engine.CoerceToolCall` isn't called in
`handlers.go`), this is fine. But the test could regress if
`handlers.go` ever contains a string literal that opens a //
comment-style content (e.g., a parsing rule for URLs).

**Fix:** Document the limitation as a known constraint, OR replace
with a proper Go AST walk (`go/ast` package), which is the
correct tool for this kind of static-source assertion. The
go/ast approach is ~10 lines and is the idiomatic Go pattern.

---

### IN-03: `internal/canonical/chunk.go` `ChunkKindPlan` documented but the Anthropic SSE adapter silently drops it without a wire surface

**File:** `internal/canonical/chunk.go:14-17`, `internal/adapter/anthropic/sse.go:274-280`
**Severity:** INFO

```go
const (
    ChunkKindText ChunkKind = iota
    ChunkKindThought
    ChunkKindToolCall
    ChunkKindPlan
)
```

`ChunkKindPlan` is defined and `internal/acp/translate.go`
translates kiro `plan` notifications into ChunkKindPlan chunks
(`translate.go:264-268`). But the Anthropic SSE emitter drops
ChunkKindPlan with a debug log (`sse.go:274-280`), the Ollama
adapter drops it with no log (`ndjson.go:169-172`), and the
OpenAI adapter likewise (`sse.go:236-241`). The plan content is
effectively unreachable from any client.

If kiro-cli starts emitting plan chunks meaningfully (Phase 7+),
those will silently disappear. There's no test that confirms a
plan chunk is processed end-to-end on any surface.

**Fix:** Add a unit test that asserts each adapter drops
ChunkKindPlan with the expected debug log, so a future regression
that accidentally enables plan rendering is caught.

---

### IN-04: `tests/e2e/tools_fixtures_test.go` `notifFrame` panics on marshal error

**File:** `tests/e2e/tools_fixtures_test.go:279-286`
**Severity:** INFO

```go
func notifFrame(frame map[string]any) []byte {
    b, err := json.Marshal(frame)
    if err != nil {
        // Hand-built maps never fail to marshal; if they do we'd want to know.
        panic(fmt.Sprintf("notifFrame: marshal: %v", err))
    }
    return append(b, '\n')
}
```

A panic in test fixture code is acceptable (test code dies, suite
fails loudly), but a `t.Fatal` would be better because it correctly
identifies which test triggered the panic. As written, the panic
unwinds through whichever subtest called the fixture and the stack
trace points to `notifFrame`, not the calling test.

**Fix:** Convert to a helper that takes `t *testing.T` and uses
`t.Fatalf`. The fixture is only called from test code anyway.

---

### IN-05: `internal/adapter/ollama/handlers.go` `extractCommit` returns "unknown" for both the missing-buildinfo case AND missing-vcs.revision case

**File:** `internal/adapter/ollama/handlers.go:388-397`
**Severity:** INFO

```go
func extractCommit() string {
    if info, ok := debug.ReadBuildInfo(); ok {
        for _, s := range info.Settings {
            if s.Key == "vcs.revision" && len(s.Value) >= 7 {
                return s.Value[:7]
            }
        }
    }
    return "unknown"
}
```

Two distinct failure modes both yield `"unknown"`:
1. `debug.ReadBuildInfo()` returns `false` (binary built without
   build info — e.g., `go run` without VCS).
2. `vcs.revision` setting not found, or its value is shorter than 7
   characters (truncated SHA on a freshly-init'd repo).

The same string surfaces in the /api/version response for both.
Diagnostically this is fine — operators see `commit: "unknown"`
either way — but it would be helpful to distinguish via a debug
log so a future "why is my version field empty" investigation
finds the right path quickly.

**Fix:** Add a debug log when the path is taken:

```go
func extractCommit() string {
    info, ok := debug.ReadBuildInfo()
    if !ok {
        slog.Default().Debug("ollama: build info unavailable; commit will be 'unknown'")
        return "unknown"
    }
    for _, s := range info.Settings {
        if s.Key == "vcs.revision" && len(s.Value) >= 7 {
            return s.Value[:7]
        }
    }
    slog.Default().Debug("ollama: vcs.revision not found in build info; commit will be 'unknown'")
    return "unknown"
}
```

---

### IN-06: `internal/adapter/openai/sse.go` `looksLikeJSONStart` is per-surface but duplicates Ollama's `shouldBuffer` heuristic

**File:** `internal/adapter/openai/sse.go:210-216` and `internal/adapter/ollama/ndjson.go:75-84`
**Severity:** INFO

The OpenAI version is a simpler standalone helper:

```go
func looksLikeJSONStart(s string) bool {
    t := strings.TrimSpace(s)
    if t == "" {
        return false
    }
    return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "```")
}
```

The Ollama version embeds the predicate inline in
`emitterState.shouldBuffer`. Both implement the same heuristic
but in different shapes. The WR-01 finding above already touches
the bug surface; this is the maintenance hazard from the
duplication itself.

**Fix:** Hoist `looksLikeJSONStart` to a shared utility package
(e.g., `internal/engine/jsonprefix.go`) and reuse from both
adapters. Mirrors the WR-05 `joinTextContent` consolidation.
Both adapters already depend on `engine` per `.go-arch-lint.yml`
(D-05) so the boundary is already open.

---

_Reviewed: 2026-05-27T00:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
