# Explicit-Model Tool Protocol Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve an explicitly selected client model, recover once on the same ACP session when that model fails the caller-tool protocol—including the structured `built_in_tool_denied` signal—and otherwise return a protocol-native HTTP 502 that recommends `model: "auto"`, while publishing an evidence-backed comparison of the legacy and new Gateways.

**Architecture:** Put one bounded recovery policy in the canonical engine, below the existing PreHook chain and above the OpenAI, Anthropic, and Ollama adapters. Eligible explicit-model tool-decision turns are preflighted before response headers commit; a classified failure gets one static same-model corrective prompt on the same session. ACP denial metadata flows structurally through stream results, the pool retains session ownership across the two prompt attempts, and adapters only translate typed canonical errors into their native envelopes. The deep-comparison document is produced from source and test evidence after the behavior is implemented.

**Tech Stack:** Go 1.26.5, ACP JSON-RPC, canonical OpenAI/Anthropic/Ollama request types, HTTP JSON/SSE/NDJSON adapters, ordered PreHook/PostHook chains, Prometheus, table-driven tests, race tests, `go test`, `go vet`, `gofumpt`, and `go-arch-lint`.

## Global Constraints

- An explicit client model remains authoritative. Never switch it to `auto`, retry through `auto`, or silently resolve it to a different model.
- `auto` is a client-facing recommendation only. This plan adds no automatic fallback and no fallback configuration flag.
- Make at most one corrective prompt, on the same ACP session and therefore the same selected model.
- Guard only requests with an explicit model, at least one caller tool, an eligible tool-decision turn, and `tool_choice != none`.
- Do not guard a final-answer turn whose latest message is `RoleTool` or contains `ContentKindToolResult`.
- Treat `built_in_tool_denied` as structured ACP metadata. Do not parse logs or raw error text to discover it.
- Preserve `auto`, tool-less, post-tool final-answer, successful native-call, successful wrapper-call, and buffer-overflow behavior.
- Keep PreHooks and PostHooks once per client request. The corrective ACP prompt is not a second canonical request.
- Keep the corrective prompt static except for a tool name that has already been validated against `req.Tools`. Never include user text, tool arguments, tool output, schemas, credentials, or raw model output.
- Use the existing 1 MiB coercion budget and a fixed chunk-count ceiling. On either ceiling, fail open to replay-plus-live passthrough and do not retry.
- Preserve context cancellation, stream idle timeout, watchdog teardown, response-before-cancel ordering, pool exactly-once release, and response backpressure.
- Return safe HTTP 502 errors with stable codes. Never return raw `SetModel`, ACP, model-output, tool-schema, or tool-argument content.
- Keep metric labels bounded and logs payload-free. Unknown model identifiers collapse through the existing model cardinality limiter.
- No native MCP registration in Kiro, Gateway-side caller-tool execution, Hermes changes, broad natural-language intent parser, dependency additions, version bumps, tags, pushes, or releases.
- Documentation may claim architectural properties proved by code and tests. It must not invent throughput, latency, or resource-usage numbers.
- Use `apply_patch` for edits, `make fmt` for formatting, and commit each task independently after its focused tests pass.

## Confirmed Failure and Recovery Contract

The observed explicit-model failure returned ordinary assistant prose saying the supplied connector tools were unavailable. The same request succeeded through the Gateway with `model: "auto"` and succeeded directly through Hermes/Codex. That isolates the remaining gap to selected-model protocol adherence, not connector registration or Hermes tool execution.

The legacy Gateway at commit `fc3bf26d64e05cc3703ee39e323bbf3c1eaa4cd6` provides three useful safeguards to retain:

1. permission requests for Kiro built-ins are denied explicitly with `reject_always` when caller tools are active;
2. each denial is counted and bounded by a circuit breaker;
3. a denied built-in attempt followed by no caller tool call gets one same-session corrective prompt.

The new Gateway already implements the first two more cleanly in ACP. This plan adds the missing structured denial count and generalized, tri-surface corrective behavior without copying the legacy Gateway's unsafe traits: Anthropic-only recovery, streamed thoughts before the outcome is known, ignored `tool_choice`, swallowed `SetModel` errors, or unknown-model fallback to `auto`.

The two terminal codes are:

```text
selected_model_activation_failed
selected_model_tool_protocol_failed
```

The safe client messages are:

```text
The selected model could not be activated. Retry the request with model `auto`.
The selected model did not produce a valid external tool call after one corrective attempt. Retry the request with model `auto`.
```

## File Map

- Modify `internal/acp/stream.go`: publish the denial counter in terminal metadata.
- Create `internal/acp/stream_result_test.go`: prove denial metadata is snapshotted safely.
- Modify `internal/acp/stream_testhelpers.go`: let cross-package shim tests record a denial through the production counter.
- Modify `internal/canonical/chat.go`: add `ToolDenials` to the canonical stream result.
- Create `internal/canonical/selected_model_error.go`: define stable selected-model error codes and safe messages shared by engine and adapters.
- Create `internal/canonical/selected_model_error_test.go`: lock matching, wrapping, and non-leaking behavior.
- Modify `internal/engine/acp_adapter.go`: copy `ToolDenials` across the direct ACP shim.
- Modify `internal/engine/acp_adapter_test.go`: prove direct shim propagation.
- Modify `internal/session/entry_acp.go`: copy `ToolDenials` across stateful-session streams.
- Modify `internal/session/entry_acp_test.go`: prove stateful shim propagation.
- Modify `internal/pool/pool.go`: copy `ToolDenials` and retain a pool session across a bounded prompt sequence.
- Modify `internal/pool/pool_test.go`: prove same-session double prompt, cancellation, error, and exactly-once release behavior with a size-one pool.
- Create `internal/engine/tool_protocol.go`: eligibility, requirement normalization, conservative failure classification, corrective prompt construction, and recovery event types.
- Create `internal/engine/tool_protocol_test.go`: pure policy and safe-prompt matrices.
- Create `internal/engine/preflight.go`: bounded attempt capture, replay/live streams, same-session recovery, and typed terminal errors.
- Create `internal/engine/preflight_test.go`: stream ordering, cap, idle, cancellation, retry, and result tests.
- Modify `internal/engine/engine.go`: wire activation errors, sequence retention, guarded preflight, watchdog, hook cleanup, and recovery observation.
- Modify `internal/engine/engine_test.go`: integration-level engine invariants.
- Modify `internal/adapter/openai/errors.go` and `handlers.go`: render selected-model errors as OpenAI HTTP 502 envelopes with `error.code`.
- Modify `internal/adapter/openai/wire_test.go`, `handlers_reroute_test.go`, and `integration_test.go`: cover both codes, streaming header timing, corrected calls, and no regressions.
- Modify `internal/adapter/anthropic/errors.go` and `handlers.go`: render HTTP 502 plus `X-Otto-Error-Code` in the Anthropic envelope.
- Modify `internal/adapter/anthropic/errors_test.go`, `handlers_test.go`, and `integration_test.go`: cover native envelopes and streaming header timing.
- Modify `internal/adapter/ollama/handlers.go`: render HTTP 502 plus `X-Otto-Error-Code` in the Ollama envelope.
- Modify `internal/adapter/ollama/handlers_test.go` and `integration_test.go`: cover native envelopes and NDJSON header timing.
- Modify `internal/adapter/{openai,anthropic,ollama}/observation.go`: classify the two selected-model failures as bounded outcomes.
- Modify `internal/metrics/metrics.go` and `internal/metrics/kiro_test.go`: add bounded recovery counters.
- Modify `cmd/otto-gateway/main.go`: wire one recovery observer into pooled and stateful engines.
- Create `docs/architecture/otto-gateway-architecture-and-reliability.md`: source-backed legacy/new comparison, diagrams, compatibility matrix, and migration guidance.
- Modify `docs/reviews/2026-07-14-track0-toolcall-findings.md`: link the implemented resolution and comparison document without rewriting the historical evidence.

---

### Task 1: Surface `built_in_tool_denied` as terminal stream metadata

**Files:**
- Modify: `internal/acp/stream.go`
- Create: `internal/acp/stream_result_test.go`
- Modify: `internal/acp/stream_testhelpers.go`
- Modify: `internal/canonical/chat.go`
- Modify: `internal/engine/acp_adapter.go`
- Modify: `internal/engine/acp_adapter_test.go`
- Modify: `internal/session/entry_acp.go`
- Modify: `internal/session/entry_acp_test.go`
- Modify: `internal/pool/pool.go`
- Modify: `internal/pool/pool_test.go`

**Contract:** `FinalResult.ToolDenials` is the number of Kiro built-in permission requests denied during that ACP prompt. It is zero for all existing streams and fakes unless the permission handler called `recordDenial`.

- [ ] **Step 1: Add failing ACP snapshot tests**

Add tests that create a stream, call the existing package-private `recordDenial` twice, close it, and assert both the first and repeated `Result()` snapshots report two denials. Also prove a stream with no denial reports zero and that mutating one returned snapshot cannot mutate the next.

```go
func TestStreamResultIncludesToolDenials(t *testing.T) {
	s := newStream("session-denial")
	s.recordDenial()
	s.recordDenial()
	s.close(&FinalResult{StopReason: canonical.StopEndTurn}, nil)

	first, err := s.Result()
	if err != nil {
		t.Fatal(err)
	}
	if first.ToolDenials != 2 {
		t.Fatalf("ToolDenials = %d, want 2", first.ToolDenials)
	}
	first.ToolDenials = 99
	second, _ := s.Result()
	if second.ToolDenials != 2 {
		t.Fatalf("snapshot alias: ToolDenials = %d, want 2", second.ToolDenials)
	}
}
```

- [ ] **Step 2: Run the focused ACP test and observe the compile failure**

```bash
go test ./internal/acp -run 'TestStreamResultIncludesToolDenials' -count=1 -v
```

Expected before implementation: `FinalResult` has no `ToolDenials` field.

- [ ] **Step 3: Add the field and snapshot it under the existing stream lock**

Extend both result structs:

```go
type FinalResult struct {
	SessionID   string
	ChunkCount  int
	StopReason  StopReason
	ToolDenials int
}
```

In `acp.Stream.close`, while `s.mu` is held and before storing the terminal error, copy the per-stream counter:

```go
s.result.ToolDenials = s.denialCount
```

Do not export the mutable stream counter and do not add log parsing.

Add a test-only bridge beside `PushForTest`/`CloseForTest` so cross-package shim tests exercise the real counter rather than injecting terminal metadata directly:

```go
func (s *Stream) RecordDenialForTest() int {
	return s.recordDenial()
}
```

- [ ] **Step 4: Add failing shim-propagation tests**

For the direct ACP adapter, stateful entry shim, and pool wrapper, call `RecordDenialForTest` three times on an underlying `acp.Stream`, close it through the existing test helper, and assert the canonical result reports three. Keep the rest of the result equality assertions unchanged.

- [ ] **Step 5: Copy the field through all three shims**

Add the same mapping to `internal/engine/acp_adapter.go`, `internal/session/entry_acp.go`, and `internal/pool/pool.go`:

```go
return &canonical.FinalResult{
	SessionID:   fr.SessionID,
	ChunkCount:  fr.ChunkCount,
	StopReason:  fr.StopReason,
	ToolDenials: fr.ToolDenials,
}, err
```

- [ ] **Step 6: Verify the complete metadata path**

```bash
go test ./internal/acp ./internal/engine ./internal/session ./internal/pool -run 'ToolDenials|StreamResult|ACPStreamShim|PoolStreamWrapper' -count=1
```

Expected: all focused tests pass; existing zero-value fakes require no changes beyond positional literals, if any.

- [ ] **Step 7: Commit the metadata slice**

```bash
git add internal/acp/stream.go internal/acp/stream_result_test.go internal/acp/stream_testhelpers.go internal/canonical/chat.go internal/engine/acp_adapter.go internal/engine/acp_adapter_test.go internal/session/entry_acp.go internal/session/entry_acp_test.go internal/pool/pool.go internal/pool/pool_test.go
git commit -m "feat: surface denied built-in tool attempts"
```

---

### Task 2: Define the pure recovery policy and safe typed errors

**Files:**
- Create: `internal/canonical/selected_model_error.go`
- Create: `internal/canonical/selected_model_error_test.go`
- Create: `internal/engine/tool_protocol.go`
- Create: `internal/engine/tool_protocol_test.go`

**Interfaces:**

```go
type SelectedModelError struct {
	Code  string
	Cause error
}

func SelectedModelErrorInfo(err error) (code, message string, ok bool)

type ToolProtocolReason string
type ToolProtocolOutcome string

type ToolProtocolEvent struct {
	Model              string
	Reason             ToolProtocolReason
	Outcome            ToolProtocolOutcome
	CorrectiveAttempts int
	RecommendAuto      bool
}
```

- [ ] **Step 1: Add typed-error tests before the type exists**

Cover direct and wrapped matching, both stable codes, safe public messages, `errors.Unwrap`, and rejection of unrelated errors. Assert that `Error()` and the returned public message do not contain a sentinel raw cause such as `secret-upstream-detail`.

- [ ] **Step 2: Add recovery eligibility and requirement tables**

Use a table that covers:

```text
model: empty, auto, explicit
tools: none, one
latest turn: user text, assistant, RoleTool, ContentKindToolResult
tool_choice: nil, auto, none, required, any, named tool, unknown
```

Required outcomes:

- empty/`auto`, no tools, tool-result final turn, and `none` are ineligible;
- explicit + tools + user decision turn is eligible;
- unknown `tool_choice` is treated as optional for compatibility;
- `required` and Anthropic `any` require some offered call;
- a named choice requires the validated offered name.

- [ ] **Step 3: Add conservative classification tests**

Define an internal attempt observation and cover:

```go
type attemptObservation struct {
	Text         string
	ToolCalls    []canonical.ToolCall
	Final        *canonical.FinalResult
	NativeCall   bool
	BufferBypass bool
}
```

The matrix must include:

- offered native call and aliased native call: success;
- direct validated wrapper and deferred `tool_call` dispatcher wrapper: success;
- `ToolDenials > 0` with no valid caller call: `built_in_tool_denied`;
- required/any with no call: `required_missing`;
- named choice with another valid call: `named_mismatch`;
- explicit whole-response wrapper marker rejected by `ExtractToolCallWrappers`: `malformed_wrapper`;
- the observed “supplied connector tools are not actually available to me here” wording: `capability_refusal`;
- GitLab permission denial, safety refusal, ordinary optional final answer, and tool-result error text: not a protocol failure;
- buffer bypass: no classification and no retry.

- [ ] **Step 4: Add corrective-prompt safety tests**

Assert the prompt:

- contains no original user content, arguments, schemas, tool outputs, or system text;
- lists no arbitrary model output;
- includes a named tool only after exact validation against `req.Tools`;
- says a normal final answer is acceptable only for optional choice;
- requires a call for `required`, `any`, and named choice.

Use byte-for-byte expected strings rather than substring-only tests so the prompt stays static and auditable.

- [ ] **Step 5: Run the policy tests and observe missing symbols**

```bash
go test ./internal/canonical ./internal/engine -run 'SelectedModelError|ToolProtocol|CorrectivePrompt' -count=1 -v
```

Expected before implementation: the new error and policy helpers are undefined.

- [ ] **Step 6: Implement canonical selected-model errors**

Use constants and closed messages:

```go
const (
	CodeSelectedModelActivationFailed   = "selected_model_activation_failed"
	CodeSelectedModelToolProtocolFailed = "selected_model_tool_protocol_failed"
)

var selectedModelMessages = map[string]string{
	CodeSelectedModelActivationFailed:   "The selected model could not be activated. Retry the request with model `auto`.",
	CodeSelectedModelToolProtocolFailed: "The selected model did not produce a valid external tool call after one corrective attempt. Retry the request with model `auto`.",
}
```

`SelectedModelError.Error()` returns only the safe message. `Unwrap()` retains the cause for server-side classification. `SelectedModelErrorInfo` accepts only the two constants; unknown codes fail closed as unrelated errors.

- [ ] **Step 7: Implement pure policy helpers**

Keep them package-private except `ToolProtocolEvent` and its bounded enums. Reuse `ResolveNativeToolName`, `ExtractToolCallWrappers`, and existing tool specs. Do not create a second wrapper parser.

For capability refusal, normalize only the complete bounded text and require both halves:

```go
func isHighConfidenceToolCapabilityRefusal(text string) bool {
	n := strings.ToLower(strings.TrimSpace(text))
	mentionsSuppliedTools := strings.Contains(n, "supplied connector tools") ||
		strings.Contains(n, "requested connector tools") ||
		strings.Contains(n, "tools you supplied")
	claimsUnavailable := strings.Contains(n, "not actually available") ||
		strings.Contains(n, "not available to me") ||
		strings.Contains(n, "can't execute") || strings.Contains(n, "cannot execute")
	return mentionsSuppliedTools && claimsUnavailable
}
```

Keep this narrow; add phrases only alongside a captured failing fixture.

- [ ] **Step 8: Verify policy and error behavior**

```bash
go test ./internal/canonical ./internal/engine -run 'SelectedModelError|ToolProtocol|CorrectivePrompt' -count=1
```

- [ ] **Step 9: Commit the policy slice**

```bash
git add internal/canonical/selected_model_error.go internal/canonical/selected_model_error_test.go internal/engine/tool_protocol.go internal/engine/tool_protocol_test.go
git commit -m "feat: classify selected-model tool protocol failures"
```

---

### Task 3: Retain a pooled ACP session across the corrective prompt

**Files:**
- Modify: `internal/engine/engine.go`
- Modify: `internal/pool/pool.go`
- Modify: `internal/pool/pool_test.go`
- Modify: `internal/pool/export_test.go`

**Why this task is separate:** `poolStreamWrapper.Result()` currently deletes `sessionSlots[sid]` and returns the slot. A second `Prompt` after draining the first attempt would therefore fail with `pool: unknown session`. A size-one pool also makes a naive reacquire deadlock. The recovery path needs an explicit bounded sequence lease, not a hidden second session.

**Optional interface declared by the engine:**

```go
type PromptSequenceClient interface {
	BeginPromptSequence(sessionID string) (finish func(), err error)
}
```

Direct ACP and stateful `session.Entry` clients need no implementation because they do not release ownership per prompt. The pool implements the optional interface.

- [ ] **Step 1: Add failing size-one pool sequence tests**

Cover these cases with a fake pool client and `PoolSize=1`:

1. `NewSession` → `BeginPromptSequence` → first `Prompt`/drain/`Result` → second `Prompt` succeeds with the same session ID.
2. Finishing after the second fully drained prompt returns exactly one slot.
3. Finishing while the current prompt is still live defers release until that wrapper's `Result`.
4. `Cancel(sid)` overrides a hold, clears it, cancels the client once, and returns the slot once.
5. first or second `Prompt` error plus `finish()` releases without a leak.
6. repeated `finish`, `Result`, and `Cancel` races do not double-send a slot.
7. `Close` with a held sequence exits cleanly and leaves no watcher goroutine.

Use the existing `SessionSlotsLen` and add a test-only `SessionHoldsLen` accessor rather than inspecting maps unsafely.

- [ ] **Step 2: Run the sequence tests and observe the current unknown-session failure**

```bash
go test ./internal/pool -run 'PromptSequence|HeldSession' -count=1 -v
```

Expected before implementation: `BeginPromptSequence` is missing or the second prompt returns `pool: unknown session`.

- [ ] **Step 3: Add explicit pool sequence state**

Under `p.mu`, add:

```go
type promptSequenceState struct {
	holds        int
	promptActive bool
}

sessionSequences map[string]*promptSequenceState
```

Initialize it in `New`. `BeginPromptSequence` validates `sessionSlots[sid]`, increments `holds`, and returns a `sync.Once`-guarded closure.

The closure decrements the hold. It releases the session only when the final hold reaches zero and no prompt is active. If a prompt is active, its terminal wrapper owns the later release.

- [ ] **Step 4: Make prompt terminal paths hold-aware**

Set `promptActive=true` only after the underlying `Prompt` succeeds. Replace the wrapper's direct release closure with one helper that:

1. marks `promptActive=false`;
2. keeps `sessionSlots[sid]` when a sequence hold remains;
3. otherwise performs the existing map-delete-first, timestamp, recycle, log, and progress path.

Prompt errors use the same rule. `Cancel` is stronger: clear the sequence entry and release immediately. `closeAll` also clears sequence state.

Never call a pool client, logger, channel send, or recycle operation while `p.mu` is held.

- [ ] **Step 5: Add compile-time interface coverage**

```go
var _ engine.PromptSequenceClient = (*Pool)(nil)
```

Keep `engine.ACPClient` unchanged; sequence retention remains optional.

- [ ] **Step 6: Run focused and race tests**

```bash
go test ./internal/pool -run 'PromptSequence|HeldSession|PoolStreamWrapper|Cancel' -count=1
go test -race ./internal/pool -run 'PromptSequence|HeldSession' -count=1
```

Expected: same-session second prompt succeeds; no double release, deadlock, race, or goroutine leak.

- [ ] **Step 7: Commit the pool lifecycle slice**

```bash
git add internal/engine/engine.go internal/pool/pool.go internal/pool/pool_test.go internal/pool/export_test.go
git commit -m "feat: retain pool sessions across prompt sequences"
```

---

### Task 4: Build bounded preflight, replay, and live-passthrough streams

**Files:**
- Create: `internal/engine/preflight.go`
- Create: `internal/engine/preflight_test.go`

**Constants:**

```go
const (
	maxToolProtocolPreflightBytes  = 1 << 20
	maxToolProtocolPreflightChunks = 4096
)
```

The byte cap matches the existing adapter streaming-coercion cap. The chunk cap prevents an attacker or broken upstream from consuming unbounded memory with many empty/tiny chunks.

- [ ] **Step 1: Add failing replay-stream tests**

Test a fully buffered stream that:

- emits buffered chunks in exact order;
- returns an immutable copied `FinalResult` including `ToolDenials`;
- returns the stored terminal error;
- closes immediately after the final replayed chunk;
- behaves correctly when there are zero chunks.

- [ ] **Step 2: Add failing prefix/live stream tests**

Test a composite stream that:

- emits the already-consumed prefix, then the untouched underlying channel;
- does not duplicate or drop the boundary chunk;
- preserves backpressure by using a small channel and blocking sends;
- delegates terminal `Result` exactly once;
- invokes its sequence-finish callback exactly once after the underlying result;
- stops cleanly when the request context is canceled.

- [ ] **Step 3: Add failing attempt-capture tests**

Cover:

- complete stream below caps;
- decisive matching native call before stream completion returns prefix/live mode;
- named mismatch is not decisive success;
- byte-cap overflow and chunk-cap overflow return bypass mode without reading the next live chunk;
- idle timeout returns `canonical.ErrStreamIdleTimeout`;
- context cancellation returns the wrapped context error;
- terminal result error is preserved;
- text aggregation is bounded and thoughts are retained for replay but not scanned as tool wrappers.

- [ ] **Step 4: Run focused tests before implementation**

```bash
go test ./internal/engine -run 'ReplayStream|PrefixLiveStream|CaptureToolProtocolAttempt' -count=1 -v
```

Expected: preflight stream constructors and capture helper are undefined.

- [ ] **Step 5: Implement immutable replay streams**

The fully drained stream owns a copied chunk slice and final result. `Chunks()` starts one `sync.Once` pump into a bounded channel; `Result()` waits for the pump's terminal state. Copy pointer-bearing chunk payloads if tests show upstream reuse; otherwise document and prove their immutability at the ACP boundary.

- [ ] **Step 6: Implement prefix/live passthrough**

The composite stream sends prefix chunks first, then ranges the original stream. It must not call `Result` until the original chunk channel closes. Wrap the terminal path in `sync.Once` so adapter and cancellation races cannot run sequence cleanup twice.

- [ ] **Step 7: Implement bounded attempt capture**

Use `RangeChunksWithIdleTimeout`. Count conservative retained bytes for every chunk kind and increment a chunk counter before append. At the first cap breach, return the consumed prefix plus untouched live stream and classify as `buffer_bypass`.

During capture, resolve native calls through `ResolveNativeToolName`. A native call is decisive only if it satisfies the normalized requirement; otherwise continue until terminal classification.

- [ ] **Step 8: Verify preflight primitives with race detection**

```bash
go test ./internal/engine -run 'ReplayStream|PrefixLiveStream|CaptureToolProtocolAttempt' -count=1
go test -race ./internal/engine -run 'ReplayStream|PrefixLiveStream|CaptureToolProtocolAttempt' -count=1
```

- [ ] **Step 9: Commit the preflight primitives**

```bash
git add internal/engine/preflight.go internal/engine/preflight_test.go
git commit -m "feat: add bounded tool protocol preflight streams"
```

---

### Task 5: Wire one same-model corrective prompt into `Engine.Run`

**Files:**
- Modify: `internal/engine/engine.go`
- Modify: `internal/engine/engine_test.go`
- Create: `internal/engine/tool_protocol_recovery_test.go`

**Engine config addition:**

```go
type Config struct {
	// existing fields
	OnToolProtocolEvent func(ToolProtocolEvent)
}
```

- [ ] **Step 1: Add failing activation-error tests**

For an explicit model whose `SetModel` returns a cause containing sensitive text, assert:

- `Engine.Run` returns a typed `selected_model_activation_failed` error;
- ACP `Prompt` is never called;
- `Cancel` is called once;
- completed PreHooks are paired with PostHook cleanup once;
- `OnModelRequest` fires once;
- the safe error string omits the cause.

Retain existing `auto` and empty-model skip tests unchanged.

- [ ] **Step 2: Add the recovery matrix with a recording ACP fake**

Prove:

1. explicit + tools + valid first wrapper: one prompt;
2. explicit + tools + valid first native call: one prompt and early live passthrough;
3. first `built_in_tool_denied`, second valid wrapper: two prompts, same session ID, same selected model, corrected response only;
4. first capability refusal, second valid call: corrected;
5. first malformed wrapper, second valid call: corrected;
6. required missing then valid call: corrected;
7. named mismatch then named match: corrected;
8. two failures: typed protocol error, one correction only;
9. `auto`, no tools, `none`, tool-result final turn: current one-prompt live path;
10. cap bypass: one prompt, no correction, prefix/live response preserved;
11. retry `Prompt` error and retry idle timeout: safe typed protocol error with cleanup;
12. context cancellation during either attempt: cancellation wins, no synthetic recommendation error;
13. PreHooks once, PostHooks once, model request counter once, recovery event once;
14. `OnToolProtocolEvent=nil`: no panic.

- [ ] **Step 3: Run the recovery tests and verify the intended failures**

```bash
go test ./internal/engine -run 'SelectedModel|ToolProtocolRecovery|BuiltInToolDenied' -count=1 -v
```

Expected before wiring: activation errors are generic and no second prompt occurs.

- [ ] **Step 4: Type `SetModel` failures without changing selection**

Replace only the explicit-model error return:

```go
if err := e.cfg.ACP.SetModel(ctx, sid, req.Model); err != nil {
	e.cfg.ACP.Cancel(sid)
	runErrCleanup()
	e.observeToolProtocol(ToolProtocolEvent{
		Model: req.Model, Reason: ReasonActivationFailed,
		Outcome: OutcomeFailed, CorrectiveAttempts: 0, RecommendAuto: true,
	})
	return nil, &canonical.SelectedModelError{
		Code: canonical.CodeSelectedModelActivationFailed,
		Cause: err,
	}
}
```

Do not change the empty/`auto` branch.

- [ ] **Step 5: Start an optional prompt sequence only for eligible turns**

After successful `SetModel` and before the first `Prompt`, type-assert `PromptSequenceClient`. If implemented, call `BeginPromptSequence(sid)`. On error, cancel and run existing hook cleanup. Keep the returned closure in a local `sync.Once` wrapper so every success, bypass, error, and cancellation path resolves the hold exactly once.

- [ ] **Step 6: Preflight and correct in one engine-owned helper**

The helper flow is:

```go
first := captureAttempt(ctx, firstStream, policy)
switch {
case first.Bypass:
	return prefixLiveRun(first), nil
case first.Success:
	return replayOrLiveRun(first), nil
case first.Correctable:
	secondStream, err := e.cfg.ACP.Prompt(ctx, sid, correctiveBlocks(policy))
	if err != nil { /* typed failure */ }
	second := captureAttempt(ctx, secondStream, policy)
	if second.Success || second.Bypass {
		return replayOrLiveRun(second), nil
	}
	return nil, protocolFailure
default:
	return replayRun(first), nil
}
```

The first failed attempt is never exposed when correction starts. The corrective prompt uses the same `sid`; there is no second `SetModel` and no recursive `Engine.Run` call.

- [ ] **Step 7: Preserve watchdog and hook lifecycle**

Register the watchdog before preflight begins so a stalled first attempt is cancellable. On a fully buffered success, return a replay stream with the same watchdog handle. On any pre-return terminal error, stop the watchdog, finish the sequence, cancel the ACP session, and call `runErrCleanup` exactly once.

Do not call PostHooks inside a successful preflight; existing adapter/Collect paths still own the single response PostHook pass.

- [ ] **Step 8: Emit one bounded recovery event**

Emit:

- `first_attempt` for a guarded first-attempt success;
- `corrected` with one corrective attempt;
- `failed` with zero or one attempts;
- `buffer_bypass` with zero attempts.

Never put response text, arguments, schemas, or session IDs in the event.

- [ ] **Step 9: Verify the engine and all existing hook tests**

```bash
go test ./internal/engine -count=1
go test ./internal/plugin/... -count=1
go test -race ./internal/engine -run 'SelectedModel|ToolProtocolRecovery|Hook' -count=1
```

- [ ] **Step 10: Commit the engine integration**

```bash
git add internal/engine/engine.go internal/engine/engine_test.go internal/engine/tool_protocol_recovery_test.go
git commit -m "feat: recover selected-model tool protocol once"
```

---

### Task 6: Render safe protocol-native 502 errors on every API surface

**Files:**
- Modify: `internal/adapter/openai/errors.go`
- Modify: `internal/adapter/openai/handlers.go`
- Modify: `internal/adapter/openai/wire_test.go`
- Modify: `internal/adapter/openai/handlers_reroute_test.go`
- Modify: `internal/adapter/openai/integration_test.go`
- Modify: `internal/adapter/anthropic/errors.go`
- Modify: `internal/adapter/anthropic/handlers.go`
- Modify: `internal/adapter/anthropic/errors_test.go`
- Modify: `internal/adapter/anthropic/handlers_test.go`
- Modify: `internal/adapter/anthropic/integration_test.go`
- Modify: `internal/adapter/ollama/handlers.go`
- Modify: `internal/adapter/ollama/handlers_test.go`
- Modify: `internal/adapter/ollama/integration_test.go`
- Modify: `internal/adapter/openai/observation.go`
- Modify: `internal/adapter/anthropic/observation.go`
- Modify: `internal/adapter/ollama/observation.go`

- [ ] **Step 1: Add adapter error-envelope tests first**

For both selected-model codes, assert:

| Surface | Status | Body contract | Stable code location |
|---|---:|---|---|
| OpenAI | 502 | `{"error":{"type":"api_error","message":"…","param":null,"code":"…"}}` | `error.code` |
| Anthropic | 502 | `{"type":"error","error":{"type":"api_error","message":"…"}}` | `X-Otto-Error-Code` |
| Ollama | 502 | `{"error":"…"}` | `X-Otto-Error-Code` |

Assert `Content-Type`, existing request ID header preservation, exact safe message, and absence of an injected raw cause.

- [ ] **Step 2: Add streaming-before-headers integration tests**

For OpenAI SSE, Anthropic SSE, and Ollama NDJSON, make the engine return a selected-model error from `Run` and assert:

- status is 502, not 200;
- content type is JSON, not event-stream/NDJSON;
- no partial assistant/refusal chunk is present;
- the native envelope and stable code are present.

Then make recovery succeed and assert the structured tool call uses the normal streaming surface shape.

- [ ] **Step 3: Run focused adapter tests and observe current 500 mappings**

```bash
go test ./internal/adapter/openai ./internal/adapter/anthropic ./internal/adapter/ollama -run 'SelectedModel|EngineRunError' -count=1 -v
```

Expected before implementation: selected-model errors are treated as generic 500s and stable codes are absent.

- [ ] **Step 4: Add one local mapping helper per adapter**

Adapters cannot import `internal/engine`; use `canonical.SelectedModelErrorInfo`:

```go
func writeSelectedModelError(w http.ResponseWriter, err error) bool {
	code, message, ok := canonical.SelectedModelErrorInfo(err)
	if !ok {
		return false
	}
	// surface-native writer here
	return true
}
```

Check this after privacy errors and pool exhaustion but before the generic 500 mapping at every `Engine.Run`/`Collect` error site where the typed error can surface. Keep raw errors in server logs only.

- [ ] **Step 5: Extend native writers without changing unrelated errors**

- OpenAI: reuse `writeErrorWithCode` with `http.StatusBadGateway`, `errAPI`, safe message, and `&code`.
- Anthropic: set `X-Otto-Error-Code` before `writeError`.
- Ollama: set `X-Otto-Error-Code` before `writeError`.

Do not add a non-native code field to Anthropic or Ollama bodies.

- [ ] **Step 6: Classify request observations with bounded outcomes**

Add these exact `classifyRequestError` branches before the default:

```text
selected_model_activation_failed
selected_model_tool_protocol_failed
```

These are closed constants, not raw error strings.

- [ ] **Step 7: Run complete adapter tests**

```bash
go test ./internal/adapter/openai ./internal/adapter/anthropic ./internal/adapter/ollama -count=1
go test -race ./internal/adapter/openai ./internal/adapter/anthropic ./internal/adapter/ollama -run 'SelectedModel|Streaming' -count=1
```

- [ ] **Step 8: Commit the wire-contract slice**

```bash
git add internal/adapter/openai/errors.go internal/adapter/openai/handlers.go internal/adapter/openai/wire_test.go internal/adapter/openai/handlers_reroute_test.go internal/adapter/openai/integration_test.go internal/adapter/openai/observation.go internal/adapter/anthropic/errors.go internal/adapter/anthropic/handlers.go internal/adapter/anthropic/errors_test.go internal/adapter/anthropic/handlers_test.go internal/adapter/anthropic/integration_test.go internal/adapter/anthropic/observation.go internal/adapter/ollama/handlers.go internal/adapter/ollama/handlers_test.go internal/adapter/ollama/integration_test.go internal/adapter/ollama/observation.go
git commit -m "feat: return native selected-model recovery errors"
```

---

### Task 7: Add bounded observability and hook-once regression coverage

**Files:**
- Modify: `internal/metrics/metrics.go`
- Modify: `internal/metrics/kiro_test.go`
- Modify: `cmd/otto-gateway/main.go`
- Modify: `internal/plugin/regression_rel_hooks_01_test.go`
- Modify: `internal/plugin/compress/invariants_test.go`
- Modify: `internal/plugin/pii/technical_conformance_test.go`

**Metrics:**

```text
gw_selected_model_tool_protocol_failures_total{gateway_id,model,reason}
gw_selected_model_tool_protocol_recovery_total{gateway_id,model,outcome}
```

Allowed `reason` values are `activation_failed`, `required_missing`, `named_mismatch`, `malformed_wrapper`, `capability_refusal`, and `built_in_tool_denied`. Allowed `outcome` values are `first_attempt`, `corrected`, `failed`, and `buffer_bypass`.

- [ ] **Step 1: Add failing metric tests**

Record events for `auto`, one known model, more than `MaxSkillCardinality` custom models, all reasons, all outcomes, and unknown reason/outcome values. Assert:

- empty/auto normalizes to `auto`;
- custom model cardinality collapses to `other`;
- only closed reason/outcome values create counters;
- no model output, prompt text, arguments, or session ID label exists.

- [ ] **Step 2: Implement the metric collectors and recorder**

Add two `CounterVec`s, register them through `reggw`, and expose:

```go
func (m *Metrics) RecordToolProtocolEvent(model, reason, outcome string) {
	if validToolProtocolReason(reason) {
		m.toolProtocolFailures.WithLabelValues(modelBucket(m.models, model), reason).Inc()
	}
	if validToolProtocolOutcome(outcome) {
		m.toolProtocolRecovery.WithLabelValues(modelBucket(m.models, model), outcome).Inc()
	}
}
```

Count `failures` only when the event reason is non-empty; a first-attempt success has only an outcome.

- [ ] **Step 3: Wire the observer into every engine constructor**

In the pooled engine and all three stateful `EngineForSession` closures:

```go
OnToolProtocolEvent: func(event engine.ToolProtocolEvent) {
	gwMetrics.RecordToolProtocolEvent(
		event.Model,
		string(event.Reason),
		string(event.Outcome),
	)
},
```

This is request observation, not a user-facing configuration setting.

- [ ] **Step 4: Add hook-once integration regressions**

Compose real request ID, logging, privacy/PII, compression, and trace hooks around a fake ACP that triggers one correction. Assert:

- each PreHook runs once;
- PII placeholders are stable across both ACP prompts and decrypt once;
- compression counters advance once and the corrective prompt is not compressed as a second request;
- logging and trace start-time entries are reclaimed;
- each PostHook runs once against the corrected response;
- a terminal protocol error calls PostHooks once with the documented nil/diagnostic response;
- the corrective prompt contains none of the original sensitive marker.

- [ ] **Step 5: Run metrics and hook suites**

```bash
go test ./internal/metrics ./internal/plugin/... -count=1
go test -race ./internal/metrics ./internal/plugin/... -run 'ToolProtocol|Recovery|HookOnce|PII|Compress' -count=1
go test ./cmd/otto-gateway -count=1
```

- [ ] **Step 6: Commit observability and hook proofs**

```bash
git add internal/metrics/metrics.go internal/metrics/kiro_test.go cmd/otto-gateway/main.go internal/plugin/regression_rel_hooks_01_test.go internal/plugin/compress/invariants_test.go internal/plugin/pii/technical_conformance_test.go
git commit -m "feat: observe selected-model tool recovery safely"
```

---

### Task 8: Produce the deep legacy/new Gateway architecture comparison

**Files:**
- Create: `docs/architecture/otto-gateway-architecture-and-reliability.md`
- Modify: `docs/reviews/2026-07-14-track0-toolcall-findings.md`

**Purpose:** Produce a standalone document suitable for sharing with engineering and product stakeholders. It explains why the Go Gateway is the preferred foundation, which useful legacy protections were retained, and where claims are architectural rather than benchmarked.

- [ ] **Step 1: Freeze the comparison evidence set**

Record both reviewed revisions at the top of the document:

```text
Legacy loop_24 revision: fc3bf26d64e05cc3703ee39e323bbf3c1eaa4cd6
New otto-gateway revision: the exact output of `git rev-parse HEAD` captured when this document is written
```

Build an evidence table before drafting prose. At minimum inspect and cite:

```text
Legacy:
  acp_server/acp-server-ollama.js

New Gateway:
  internal/canonical/chat.go
  internal/engine/engine.go
  internal/engine/collect.go
  internal/engine/coerce.go
  internal/engine/preflight.go
  internal/acp/client.go
  internal/acp/stream.go
  internal/acp/permission.go
  internal/pool/pool.go
  internal/session/entry_acp.go
  internal/adapter/openai/
  internal/adapter/anthropic/
  internal/adapter/ollama/
  internal/plugin/chain.go
  internal/plugin/pii/pii.go
  internal/plugin/compress/hook.go
  internal/plugin/logging.go
  internal/plugin/trace.go
  internal/metrics/metrics.go
  internal/config/
  internal/*/*_test.go
```

For every significant claim, include a relative file link and, where useful, the exact proving test or reproducible command. Private legacy GitLab links may be accompanied by revision + file + line references so an internal reader can reproduce them.

- [ ] **Step 2: Draft the document with this exact outline**

```markdown
# Why the New OTTO Gateway Is the Preferred Foundation

## Executive summary
## What changed architecturally
## Component architecture
## Request lifecycle and hook ordering
## Client compatibility: OpenAI, Anthropic, and Ollama
## Tool-call handling
## Selected-model behavior and graceful failure
## Reliability and lifecycle controls
## Privacy, PII, compression, logging, and tracing
## Observability, configuration, and operations
## Testing and cross-platform posture
## Legacy safeguards retained
## Capability comparison matrix
## Performance: proved properties versus measurements
## Migration and model-selection guidance
## Known limits and future work
## Evidence and reproduction commands
```

- [ ] **Step 3: Add two Mermaid diagrams**

The component diagram must show:

```text
OpenAI / Anthropic / Ollama clients
        -> surface adapters
        -> canonical request/response
        -> ordered PreHooks
        -> engine + selected-model guard
        -> ACP pool/stateful session
        -> Kiro
        -> ordered PostHooks
        -> native surface response
```

The request-flow diagram must distinguish the unchanged `auto`/tool-less path from the explicit-model guarded path and show the single same-session correction and typed 502 outcome.

- [ ] **Step 4: Build a precise capability matrix**

Use columns `Area`, `Legacy Gateway`, `New Gateway`, `Why it matters`, and `Evidence`. Include:

- implementation structure and canonical narrow waist;
- OpenAI, Anthropic, and Ollama coverage;
- streaming/non-streaming parity;
- native calls, explicit wrappers, deferred nested dispatcher, and multi-turn tool results;
- selected models, structured activation failure, structured denial recovery, and recommend-auto failure;
- bounded denial circuit breaker;
- pool capacity, worker recycling, cancellation, idle timeout, backpressure, and shutdown;
- bounded parsing and fail-open behavior;
- ordered privacy/PII/compression/logging/tracing hooks;
- request IDs, health endpoints, metrics cardinality, and error contracts;
- config validation and test depth;
- cross-platform packaging/release surfaces.

Give the legacy implementation credit for its denial counter, `reject_always`, same-session Anthropic nudge, response-before-cancel discipline, and narrow skill-read exemption. State explicitly which were retained, generalized, or intentionally not copied.

- [ ] **Step 5: Separate performance properties from benchmark results**

Use a boxed note or table with two categories:

```text
Proved by architecture/tests:
  bounded buffers, worker reuse, stream backpressure, bounded acquisition,
  cancellation, idle timeout, cardinality caps, no duplicated hook pass.

Requires measurement:
  requests/second, p50/p95 latency, memory reduction, CPU reduction,
  cold-start improvement, and comparative throughput.
```

Do not use “faster,” “lower latency,” or percentage claims unless a checked-in benchmark command and output support them. “Designed to reduce process-spawn overhead through warm pooling” is acceptable when linked to pool code; “X% faster” is not.

- [ ] **Step 6: Add verification commands to the document**

Include commands a reviewer can run:

```bash
go test ./internal/engine ./internal/acp ./internal/pool ./internal/session -count=1
go test ./internal/adapter/openai ./internal/adapter/anthropic ./internal/adapter/ollama -count=1
go test ./internal/plugin/... ./internal/privacy ./internal/metrics -count=1
go test -race ./internal/engine ./internal/acp ./internal/pool ./internal/session -count=1
go vet ./...
```

Also include the exact Hermes selected-model repro, the `model: auto` control, and expected safe 502 codes without embedding credentials or private connector data.

- [ ] **Step 7: Link the historical findings to the resolution**

Add a short “Resolution” section to `docs/reviews/2026-07-14-track0-toolcall-findings.md` linking the design, implementation plan, implementation commits, and comparison document. Preserve historical timestamps and original findings verbatim.

- [ ] **Step 8: Review the comparison for evidence quality**

Run:

```bash
rg -n 'faster|more performant|lower latency|higher throughput|% faster|TODO|TBD|FIXME' docs/architecture/otto-gateway-architecture-and-reliability.md
rg -n '\[[^]]+\]\([^)]*\)' docs/architecture/otto-gateway-architecture-and-reliability.md
```

Manually verify every comparative adjective is either linked to proof or rewritten as a design property. Verify both Mermaid blocks render and every relative repository link resolves.

- [ ] **Step 9: Commit the shareable architecture document**

```bash
git add docs/architecture/otto-gateway-architecture-and-reliability.md docs/reviews/2026-07-14-track0-toolcall-findings.md
git commit -m "docs: compare gateway architecture and reliability"
```

---

### Task 9: Run the full verification and selected-model acceptance matrix

**Files:**
- Modify only if verification exposes a defect in files already owned by Tasks 1–8.

- [ ] **Step 1: Format and run static checks**

```bash
make fmt
git diff --check
go vet ./...
```

Expected: all commands exit zero and formatting produces no unexpected unrelated diff.

- [ ] **Step 2: Run the complete unit and integration suite**

```bash
go test ./... -count=1
```

Expected: all packages pass.

- [ ] **Step 3: Run the concurrency-critical suites under the race detector**

```bash
go test -race ./internal/acp ./internal/engine ./internal/pool ./internal/session -count=1
go test -race ./internal/adapter/openai ./internal/adapter/anthropic ./internal/adapter/ollama -count=1
```

Expected: no race, deadlock, watcher leak, or double release.

- [ ] **Step 4: Run repository architecture and build gates**

```bash
make test
make build
```

If the Makefile exposes a separate architecture target, run it as well. Expected: all targets exit zero.

- [ ] **Step 5: Execute the live acceptance matrix without private payloads in logs**

Run the same simple caller-owned test tool through each API surface with:

| Case | Expected |
|---|---|
| `model:auto`, tools | existing successful tool call |
| explicit model, first-attempt valid call | success, one ACP prompt |
| explicit model, denied built-in then corrected call | success, two prompts on one session |
| explicit model, two protocol failures | native HTTP 502 + recommend auto |
| explicit model activation failure | native HTTP 502 + recommend auto |
| explicit model, `tool_choice:none` | ordinary response, no guard |
| explicit model, post-tool final answer | ordinary final response, no guard |
| explicit model, >1 MiB undecided output | fail-open live response, no retry |

Inspect logs and `/metrics` to confirm one client request, one model request, one hook cycle, bounded recovery counters, and no raw arguments/output.

- [ ] **Step 6: Re-run the original Hermes GitLab reproduction**

Use the original prompt and explicit model. Expected behavior is either:

1. the requested GitLab call completes correctly; or
2. a stable `selected_model_tool_protocol_failed` 502 recommends `model: auto`.

It must not claim the connector is unavailable as a normal successful assistant response. Switch back to `auto` as the control and verify the original 4-group/37-project result still succeeds.

- [ ] **Step 7: Inspect final scope and history**

```bash
git status --short
git log --oneline --decorate -12
git diff origin/main...HEAD --stat
```

Expected: only planned source, tests, design/plan, findings, and architecture documentation changed; no credentials, captures, generated binaries, or unrelated workspace edits are included.

- [ ] **Step 8: Commit any verification-only corrections atomically**

If verification required a correction, commit it with a narrow message after rerunning its focused test and the failed gate. If no correction was needed, do not create an empty commit.

## Definition of Done

- Explicit client models are never silently changed or retried through `auto`.
- `built_in_tool_denied` is carried structurally from ACP permission handling to the recovery classifier.
- At most one corrective prompt runs, on the same ACP session and selected model.
- A size-one pool supports the two-prompt sequence without release, deadlock, race, or leak.
- Eligible streaming turns resolve before headers commit; unaffected turns keep their live path.
- Buffer and chunk ceilings fail open without retry.
- Activation and terminal protocol failures render safe native HTTP 502 errors on all three surfaces.
- Privacy, PII, compression, logging, tracing, PostHooks, and model-request accounting run once per client request.
- Recovery logs and metrics contain bounded metadata only.
- `auto`, tool-less, `tool_choice:none`, post-tool final, native call, wrapper call, deferred dispatcher, and existing multi-turn behavior remain covered.
- The comparison document is source-backed, gives appropriate credit to legacy safeguards, distinguishes architecture from benchmarks, and includes diagrams, a capability matrix, migration guidance, and reproducible verification commands.
- Full tests, race tests, static checks, and build gates pass.
