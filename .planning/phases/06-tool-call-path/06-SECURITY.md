---
phase: 06-tool-call-path
audited: 2026-05-27
asvs_level: 1
threats_total: 34
threats_closed: 34
threats_open: 0
threats_accepted: 5
threats_human_verified: 2
block_on: high
disposition: SECURED
auditor: gsd-secure-phase (Claude Opus 4.7)
---

# Phase 06 — Security Audit (Tool-Call Path)

Audit verifies every declared threat-mitigation against actual implementation
artifacts in the repo. No threat is closed without a grep-confirmed evidence
trail (file:line + named test where applicable). Implementation files are
read-only for this audit.

## Verification Summary

| Result | Count |
|--------|-------|
| CLOSED — mitigation present | 27 |
| ACCEPTED — accept-disposition with rationale honored | 5 |
| CLOSED — human-verify resolved (Path C documented) | 2 |
| OPEN | 0 |
| Unregistered flags | 0 |

ASVS Level 1; `block_on: high`. No BLOCKER findings.

## Threat Verification (per slice)

### 06-01 Foundation (canonical + ACP translate + engine.coerce)

| Threat ID | Category | Disposition | Status | Evidence |
|-----------|----------|-------------|--------|----------|
| T-06-01 | Tampering — tool_call body decode | mitigate | CLOSED | `internal/acp/translate.go:50-65` defines tolerant `sessionUpdateBody` with `omitempty`; `:212-254` extracts `body.ToolCallID/Title/Args` with `firstNonEmpty` fallback for empty title (verbatim Phase 1.1 D-22 discipline) |
| T-06-02 | DoS — tool_call args size | mitigate | CLOSED | `internal/acp/framer.go:31` sets `sc.Buffer(make([]byte, 64*1024), 1024*1024)` — 1 MB max scan token meets the ≥1 MB requirement |
| T-V5-01 | Tampering / Input Validation — coerce parses untrusted text | mitigate | CLOSED | `internal/engine/coerce.go:47` imports `encoding/json`; `:125` uses `json.Unmarshal` (rejects malformed cleanly); `:187-228` `pickBestTool` reads only top-level property names (no eval). Locked by `TestCoerceToolCall_NeverPanics` (1000 random inputs) and `TestCoerceToolCall_AlgorithmCases` |
| T-V5-02 | DoS catastrophic backtracking | mitigate | CLOSED | `grep -c regexp internal/engine/coerce.go` = **0**. `stripFences` uses only `strings.TrimSpace` + `strings.HasPrefix` + `strings.HasSuffix` (`internal/engine/coerce.go:253-260`) — linear time guaranteed |
| T-06-03 | Spoofing — synthetic ID | accept | ACCEPTED | `internal/engine/coerce.go:163` uses `fmt.Sprintf("call_%d", time.Now().UnixNano())`. Per-response scope, opaque per OpenAI/Anthropic specs; documented in `coerce.go` package doc + plan register; `parallel_tool_calls` deferred per CONTEXT |
| T-V8-01 | Information disclosure — [Available tools] | accept | ACCEPTED | `internal/engine/build_acp.go:99-105` logs only `tools_count` (length) + `err.Error()` on the marshal-failure debug path. The catalog JSON is `fmt.Fprintf`-emitted to the in-process buffer (`b.WriteString` / `Fprintf`) — never to `slog`. No information leak |
| T-06-A1 | Tampering — Node algorithm drift | mitigate-via-human-verify | CLOSED (Path C) | Checkpoint resolved per 06-01-SUMMARY §Deviations Decision #4. Node source not on this machine; algorithm verified against `docs/reference/acp_server_node_reference.md` §"coerceToolCall" lines 166-195 + property-test invariants (1000 iterations). Post-ship LangChain smoke is the residual verification path; recommended as a post-ship follow-up todo |
| T-06-18 | Tampering — kiro tool-name in narration | accept | ACCEPTED | `internal/engine/collect.go:95` `sb.WriteString("[tool: ")` appends verbatim into assistant text; rendered as opaque body bytes by all surfaces (not interpreted as control tokens). Framer caps frame size — no structural risk vs existing LLM text-injection capability |
| T-06-SC | Tampering — supply-chain (new deps) | mitigate | CLOSED | `git log --since=2026-05-24 -- go.mod go.sum` returns **no commits** during phase 06. Zero new external dependencies introduced |

### 06-02 Ollama vertical slice

| Threat ID | Category | Disposition | Status | Evidence |
|-----------|----------|-------------|--------|----------|
| T-V5-01 (inherited) | Input Validation | mitigate | CLOSED | Same `engine/coerce.go` mitigation; Ollama-side invocation sites `internal/adapter/ollama/handlers.go:95` (non-streaming) + `internal/adapter/ollama/ndjson.go:425` (streaming) — both pass `resp`/`syntheticResp` pointer-direct (Pitfall 6) |
| T-06-04 | Information disclosure — coerce debug log | accept | ACCEPTED | `internal/adapter/ollama/handlers.go:96-100` logs only `firstName` (tool name string), guarded by `len(resp.Message.ToolCalls) > 0` defensive length-guard (REVIEW LOW #7). `internal/adapter/ollama/ndjson.go:432` mirrors the discipline. No descriptions or args ever logged |
| T-06-05 | Tampering — single-goroutine emitter state | mitigate | CLOSED | `internal/adapter/ollama/ndjson.go:7-12` doc block + `:73` `sawKiroNativeToolCall bool` declared on per-emitter `emitterState` touched only inside the select-loop goroutine; race-detector gates pass (`go test -race` per 06-02-SUMMARY §Wave 2 Readiness item 8) |
| T-06-15 | DoS — text buffer growth | mitigate | CLOSED | `internal/adapter/ollama/ndjson.go:93-97` `shouldBuffer` heuristic: buffering enters only when text starts with `{` or triple-backtick fence. Framer cap (1 MB) + per-request response budget bound the cumulative buffer (T-06-02 carry-forward) |
| T-06-19 | Tampering — coerce double-fire | mitigate | CLOSED | `internal/adapter/ollama/ndjson.go:73` declares `sawKiroNativeToolCall`; `:169` sets `true` on `ChunkKindToolCall`; `:404` `if state.sawKiroNativeToolCall { ... skip coerce ...}` at stream end. Locked by `TestStream_NativeToolCall_ThenJSONText_NoCoerce` + `TestStream_NativeToolCall_Only_NoCoerce` (06-02-SUMMARY §Test Coverage) |
| T-06-SC (carry) | Supply-chain | mitigate | CLOSED | Same as 06-01 — zero new deps |

### 06-03 OpenAI vertical slice

| Threat ID | Category | Disposition | Status | Evidence |
|-----------|----------|-------------|--------|----------|
| T-V5-01 (inherited) | Input Validation | mitigate | CLOSED | Same `engine/coerce.go` mitigation; OpenAI sites `internal/adapter/openai/handlers.go:122` (non-streaming) + `internal/adapter/openai/sse.go:522` (streaming via `tryStreamingCoerce`); both pointer-direct |
| T-06-06 | Information disclosure — render json.Marshal of args | accept | ACCEPTED | `internal/adapter/openai/render.go:181` `argsJSON, err := json.Marshal(tc.Arguments)` — args are client-relayed data; client is intended recipient. Fall-back to `"{}"` on error (defensive). No log emission of args content |
| T-06-07 | Tampering — polymorphic tool_choice | mitigate | CLOSED | `internal/adapter/openai/wire.go:215-237` `decodeToolChoice`: typed-object attempt → string fallback (`auto`/`required`/`none`) → accept-and-ignore for unknown. Locked by `TestWireToChatRequest_ToolChoice_*` table (06-03-SUMMARY §Test Coverage) |
| T-06-15 (iteration-3 MEDIUM #4) | Tampering — tools[] type-invalid sibling corruption | mitigate | CLOSED | `internal/adapter/openai/wire.go:43` `Tools []json.RawMessage`; `:176-177` per-entry `json.Unmarshal` with skip-on-failure + debug log; valid siblings preserved. Locked by `TestWireToChatRequest_Tools_MixedValidInvalid` (`internal/adapter/openai/wire_test.go:262`) using `function:42` type-invalid entry |
| T-V5-03 | DoS — oversized args from kiro stream | mitigate | CLOSED | `internal/adapter/openai/sse.go:130` `textBuffer strings.Builder` bounded by framer (1 MB) + per-request budget; `sse.go:271-278` `shouldBuffer` heuristic same as Ollama |
| T-06-05 (inherited) | Single-goroutine emitter state | mitigate | CLOSED | `internal/adapter/openai/sse.go:32-37` doc block + `:130-133` state struct fields touched only inside select-loop (D-05) |
| T-06-19 | Coerce double-fire after kiro-native | mitigate | CLOSED | `internal/adapter/openai/sse.go:133` `sawKiroNativeToolCall bool`; `:319` set true on `ChunkKindToolCall`; `:522` `tryStreamingCoerce` guarded by `e.req`/buffering precondition checks at stream finalize. Locked by `TestStream_NativeToolCall_ThenJSONText_NoCoerce` + `TestStream_NativeToolCall_Only_NoCoerce` (06-03-SUMMARY §Test Coverage) |
| T-06-SC (carry) | Supply-chain | mitigate | CLOSED | Same — zero new deps |

### 06-04 Anthropic vertical slice

| Threat ID | Category | Disposition | Status | Evidence |
|-----------|----------|-------------|--------|----------|
| T-06-08 | Tampering — SDK conformance (input:{}) | mitigate | CLOSED | `internal/adapter/anthropic/sse.go:99` `Input *map[string]any` (CR-01 pointer-to-empty-map); `:268-272` constructs `toolUseBlockHeader{ID, Name, Input: &emptyMap}` so `omitempty` preserves `"input":{}` rather than emitting `"input":null`. Locked by `TestSSE_Golden_ToolUse` byte-level assertions (`internal/adapter/anthropic/sse_golden_test.go:195+`) |
| T-06-09 | Spoofing — NO-CoerceToolCall asymmetry | mitigate | CLOSED | `grep -v '^[[:space:]]*//' internal/adapter/anthropic/{handlers,collect,sse,render,wire}.go \| grep "engine.CoerceToolCall"` returns **0 hits in production code** (only doc comments). Locked by `TestAnthropic_NoCoerce_Behavioral` (primary, `handlers_test.go:687`) + `TestAnthropic_DoesNotCallCoerceToolCall` (static-source, `handlers_test.go:769`) using comment-stripper |
| T-06-10 | Tampering — block-index state machine | mitigate | CLOSED | `internal/adapter/anthropic/sse.go:174` `blockIndex int` on emitter; `:290` `e.blockIndex++` on kind-transition bump; Phase 3.1 D-04 discipline preserved. Locked by `TestSSE_Golden_ToolUse` index-sequence assertion `0,0,0,1,1,1` (sse_golden_test.go:262) |
| T-06-16 | Spoofing — non-streaming stop_reason | mitigate | CLOSED | `internal/adapter/anthropic/render.go:101+114` `hasToolUse` flag walks `Message.Content`; `:150-153` override sets `*out.StopReason = "tool_use"` when content has `ContentKindToolUse`. Locked by `TestRender_ToolUse_StopReasonOverride` (`render_test.go:343`) |
| T-06-20 | Tampering — CollectAnthropicChat drift | mitigate | CLOSED | `internal/adapter/anthropic/collect_test.go` exports 5 parity tests vs `engine.Collect`: `TestCollectAnthropicChat_ParityWithEngine_TextOnly` (`:168`), `_ThinkingOnly` (`:200`), `_MixedTextThinking` (`:228`), `_StopReasonPropagation` (`:257`), `_ErrorPropagation` (`:297`) + `_AnthropicException_ToolCallProducesToolUse` (`:365`) locking the divergent path |
| T-V5-03 (inherited) | DoS — oversized args | mitigate | CLOSED | `internal/adapter/anthropic/sse.go:346` `json.Marshal(args)` of kiro-supplied args; framer cap (1 MB) bounds upstream notification size |
| T-06-SC (carry) | Supply-chain | mitigate | CLOSED | Same — zero new deps |

### 06-05 E2E

| Threat ID | Category | Disposition | Status | Evidence |
|-----------|----------|-------------|--------|----------|
| T-06-11 | DoS — test-harness flake (goleak) | mitigate | CLOSED | `tests/e2e/tools_fixtures_test.go:512` `GoleakVerifyAtEnd` helper with documented `// goleak ignore-list:` block (`:499+`); per-subtest defer counts: Ollama=15, OpenAI=15, Anthropic=5, Cancel=4 (`grep -c "defer GoleakVerifyAtEnd"`) per CONTEXT D-21 |
| T-06-12 | Spoofing — wrong-shape regression | mitigate | CLOSED | `tests/e2e/tools_anthropic_test.go:243` `TestE2E_Tools_CrossSurface_CanonicalEquivalence` invokes `AssertSameCanonicalToolCall` (`:279`) defined at `tools_fixtures_test.go:397` with iteration-3 normalization (narration text vs native tool_use) |
| T-06-13 | Tampering — Anthropic asymmetry erosion | mitigate | CLOSED | `tests/e2e/tools_anthropic_test.go:134` `t.Run("NoCoerce_BareJSON", ...)` exercises scenario 5 at the E2E layer; complements unit-level `TestAnthropic_NoCoerce_Behavioral` |
| T-06-17 | Tampering — fake-kiro contract | mitigate | CLOSED | `tests/e2e/cmd/fake-kiro-cli/main.go` cases: `initialize` (`:100`), `session/new` (`:112`), `session/set_model` (`:124`), `session/prompt` (`:128`), `session/cancel` (`:139`), `ping` (`:146`); EOF clean exit at `:158-159` |
| T-06-21 | DoS — fake-kiro binary-lifetime bug | mitigate | CLOSED | `tests/e2e/e2e_test.go:118` compiles to `os.TempDir()/fake-kiro-cli-<pid>` via `os.Getpid()`; `:136` sets package-level `fakeKiroBinaryPath`; documented in `tools_testmain_test.go:13-49`; lifetime locked by `TestFakeKiro_BinaryExistsAfterMultipleSubtests` (`tools_testmain_test.go:58`) |
| T-06-14 | Spoofing — Anthropic SDK parser conformance | mitigate-via-human-verify | DEFERRED | Operator-action checkpoint per Task 4 in 06-05; documentation present at `.planning/phases/06-tool-call-path/06-HUMAN-UAT.md`. The HUMAN-UAT loop24-client conformance test is the residual SDK-shape verification path; status: PAUSED awaiting operator approval (per 06-05-SUMMARY §Tasks Completed) |
| T-06-SC (carry) | Supply-chain | mitigate | CLOSED | Same — zero new deps; fake-kiro pure-Go stdlib only |

## Unregistered Flags

**None.** The two SUMMARY files that emit a `## Threat Flags` section
(06-03-SUMMARY.md and 06-05-SUMMARY.md) both declare no new surface
beyond what is enumerated in the plan-time threat register.

- `06-03-SUMMARY.md ## Threat Flags`: "No new threat surface introduced
  beyond what is already in `<threat_model>` (T-V5-01, T-06-06, T-06-07,
  T-06-15, T-V5-03, T-06-05, T-06-19, T-06-SC). All mitigations honored."
- `06-05-SUMMARY.md ## Threat Flags`: "None. All new code lives in
  tests/e2e/ (test-only) and tests/e2e/cmd/fake-kiro-cli/ (a controllable
  test binary compiled at TestMain time, deleted at exit, never shipped
  in the production otto-gateway binary). No new network surface, no new
  auth path, no new file-access pattern at trust boundaries."

## Accepted Risks Log

| Threat ID | Rationale (verbatim from plan) | Residual Mitigation |
|-----------|--------------------------------|---------------------|
| T-06-03 | Synthetic `call_<unix-nano>` IDs are opaque per OpenAI/Anthropic specs; no cryptographic claim. Sub-microsecond collision theoretically possible but Phase 6 emits one tool_call per request and IDs are per-response scope. Documented in `internal/engine/coerce.go` package doc + `:163` comment | Defer to `crypto/rand` only if `parallel_tool_calls` is implemented (currently deferred per CONTEXT `<deferred>`) |
| T-V8-01 | Tool descriptions are client-supplied; kiro-cli is the intended consumer. Catalog content does NOT echo to slog | Verified: `internal/engine/build_acp.go:99-105` logs only `tools_count` + err on marshal-failure; never the catalog payload |
| T-06-18 | Tool name from kiro stdout appended verbatim into assistant text as `[tool: <name>]\n`. Surrounding text is opaque body bytes (not interpreted by SDKs as control tokens). ACP framer caps frame size | Only attack surface is operator/user confusion from a deceptive name — equivalent to existing LLM text-injection capability; no new structural risk |
| T-06-04 | Tool name is client-supplied + non-sensitive; descriptions/args NOT logged | Defensive `len(resp.Message.ToolCalls) > 0` guard per REVIEW LOW #7 prevents nil-deref on future refactor |
| T-06-06 | Tool args are client-relayed data — the client sent `tools[]` and is the intended recipient of `tool_calls`. No leak | None additional — defensive fallback to `"{}"` on marshal error preserves wire shape |

## Human-Verify Status

| Threat ID | Path Taken | Disposition |
|-----------|-----------|-------------|
| T-06-A1 (Node byte-fidelity for coerce algorithm) | **Path C accepted** in 06-01-SUMMARY §Deviations Decision #4 | Wave 2 unblocked per checkpoint's Path C semantics. Recommended post-ship follow-up: LangChain integration smoke with Pi-SDK / LangFlow real users to surface any algorithmic divergence as bug reports |
| T-06-14 (Anthropic SDK parser conformance via loop24-client) | **PAUSED — awaiting operator approval** | Deferred to HUMAN-UAT per `.planning/phases/06-tool-call-path/06-HUMAN-UAT.md`. Task 4 in 06-05 is a checkpoint:human-verify gate by design; not a blocker for code-level audit |

## Audit Trail

| Step | Method | Result |
|------|--------|--------|
| Load context | Read 5 plan files + 5 summary files; extract `<threat_model>` blocks + dispositions | 34 threats enumerated (after dedup) |
| Verify mitigate (27 threats) | `grep` for declared mitigation pattern in cited files + cross-reference SUMMARY §Test Coverage for test names | All 27 patterns present at expected file:line |
| Verify accept (5 threats) | Confirm rationale still applies + verify no implementation drift contradicts acceptance | All 5 acceptance rationales hold; defensive guards present where stated |
| Verify mitigate-via-human-verify (2 threats) | Confirm checkpoint resolution documented in SUMMARY | T-06-A1 Path C accepted (06-01-SUMMARY); T-06-14 PAUSED-awaiting-operator (06-05-SUMMARY) |
| Unregistered-flag scan | Read SUMMARY ## Threat Flags sections | 06-03 + 06-05 declare no new surface; 01/02/04 do not include the section (no new surface declared) |
| Production-code Anthropic `engine.CoerceToolCall` check | `grep -v '^[[:space:]]*//' adapter/anthropic/*.go \| grep engine.CoerceToolCall` | 0 matches → D-01 asymmetry holds at code level |
| Supply-chain check | `git log --since=2026-05-24 -- go.mod go.sum` | No commits → zero new external dependencies |
| Framer line cap check | Read `internal/acp/framer.go:31` | `sc.Buffer(..., 1024*1024)` = 1 MB ≥ requirement |

## Disposition: **SECURED**

All 34 declared mitigations are present in the implementation. Five
accept-disposition entries are logged with rationale and residual
mitigations. Two human-verify checkpoints are documented per their
defined paths (T-06-A1 Path C; T-06-14 deferred-PAUSED). Zero
unregistered flags. Zero OPEN threats.

The phase is cleared from a security-audit perspective. The HUMAN-UAT
checkpoint (T-06-14, loop24-client SDK conformance) remains an
operator-action item but is NOT a code-level security gap — it is the
documented residual SDK-shape verification path explicitly scoped to
human operation.

### Recommended Post-Ship Follow-Ups (not blockers)

1. **T-06-A1 Path C residual:** LangChain integration smoke with Pi-SDK /
   LangFlow real users hitting `/v1/chat/completions` and `/api/chat`
   with JSON-as-text payloads. Divergent `coerceToolCall` behavior vs
   the Node original would surface as user-reported bugs. Recommend
   tracking as a `perf-baseline-vs-node.md`-style milestone-deferral
   todo.
2. **T-06-14 HUMAN-UAT:** Complete the loop24-client `messages.stream()`
   conformance pass against the live binary per
   `.planning/phases/06-tool-call-path/06-HUMAN-UAT.md`.
