---
slug: model-tool-review-findings
status: resolved
trigger: "Validate and resolve the completed adversarial review findings for the model-selection-aware tool contract"
created: 2026-08-15T00:00:00-04:00
updated: 2026-08-15T12:15:00-04:00
---

# Debug Session: model-tool-review-findings

## Symptoms

DATA_START
**Expected behavior:** The Gateway feature branch satisfies the approved model-selection-aware
tool-calling contract, preserves OpenAI/Anthropic/Ollama equivalence, and passes every required
repository trust gate.

**Actual behavior:** The adversarial review reports that `make ci` fails with 12 new revive
exported-comment findings, Anthropic `tool_choice:{"type":"none"}` is decoded as nil and becomes
an optional tool policy under contract v1, and the post-tool provenance classifier can suppress
legitimate answers and return a typed 502. It also reports design/plan/prompt contradictions,
unnecessary post-tool buffering, duplicate-header ambiguity, an observability-classification
regression, a misleading fake-engine test rename, and a focused command that matches no tests.

**Error messages:** `make ci` exits 2 after golangci-lint reports 12 `revive` exported-symbol
comment violations. A repeated false-positive provenance classification returns
`selected_model_tool_result_provenance_failed` as HTTP 502.

**Timeline:** Reported by the fresh-session adversarial review after implementation commit
`a28bb69` and documentation-only review-prompt commit `5b11861`. The review says the lint gate
passes at base `ff3ca5c` and fails on the feature branch.

**Reproduction:** Run the reported fresh-cache `make ci`; decode an Anthropic request containing
`tool_choice:{"type":"none"}` under `X-Otto-Tool-Contract: v1`; classify the sanitized sentence
"The audit tool reports that three of the supplier invoices were fabricated, so you cannot use
them to support the quarterly filing" after a host-framed tool result; exercise duplicate
`X-Otto-Tool-Contract` header values; and compare the review prompt and plan invariants to the
approved design.
DATA_END

## Current Focus

hypothesis: Confirmed — F1-F9 are resolved by minimal runtime, test-integrity, lint-comment, and contract-documentation changes.
test: Completed focused contract/engine/plugin verification and the full non-race trust-gate components with fresh caches.
expecting: Satisfied — every executed gate exits 0; the corrected selector visibly runs the shared parser tests.
next_action: Parent split the verified runtime and test changes into atomic commits and advanced the review prompt through implementation tip `0e81856`; commit the reconciled documentation/debug record, then run the complete final gate including the race suite.
reasoning_checkpoint:
  hypothesis: "The 12 missing name-prefixed exported comments cause all of F1 because revive reports exactly those 12 declarations and no other lint failures."
  confirming_evidence:
    - "A fresh lint cache reproduced exactly 12 revive exported-comment findings in the three reported files."
    - "The base passes the same linter, and each reported declaration was introduced on this branch without a revive-compliant comment."
  falsification_test: "After comment-only edits, the same fresh-cache lint run would still fail or report a non-comment issue."
  fix_rationale: "Name-prefixed doc comments satisfy revive at the declaration boundary without changing runtime behavior."
  blind_spots: "The later full non-race suite could expose an unrelated accumulated branch regression; that would not invalidate the F1 mechanism but would block overall verification."
tdd_checkpoint:
  test_file: golangci-lint
  test_name: revive exported-comment gate
  status: red
  failure_output: 'exit 1: exactly 12 revive exported-comment findings in internal/engine/coerce.go, internal/engine/tool_protocol.go, and internal/toolcontract/contract.go'

## Evidence

- timestamp: 2026-08-15T00:00:00-04:00
  observation: The external review artifact reports nine findings and supplies five sanitized reproductions plus full command evidence.
  source: User-supplied adversarial review artifact
- timestamp: 2026-08-15T11:24:23-04:00
  checked: Debug knowledge base and worktree state.
  found: No project knowledge base exists; the isolated worktree is on feat/model-selection-aware-tool-contract and only the active debug session is untracked.
  implication: There is no known-pattern shortcut, and investigation can proceed without overlapping tracked edits.
- timestamp: 2026-08-15T11:26:10-04:00
  checked: Initial repository inventory and common-pattern scan.
  found: Anthropic has dedicated tool-choice decoder tests; provenance classification is implemented in internal/engine/tool_result_protocol.go and explicitly matches the word "fabricated"; branch history includes the implementation and review-prompt commits named in the symptoms.
  implication: The review maps to Data Shape/API Contract and Regex/String-style lexical classification candidates; exact contract artifacts and changed-file scope must now determine whether the observed behaviors are defects.
- timestamp: 2026-08-15T11:33:00-04:00
  checked: Approved 2026-08-15 design/plan/review prompt and inherited 2026-08-13 design/plan.
  found: The authoritative v1 policy table maps Anthropic `none` to canonical Prohibited; the post-tool classifier must require a provenance claim plus denial/refusal for that reason, and generic caution/ordinary prose must not match; inherited buffering explicitly allows bounded fail-open replay and delayed first-token delivery on eligible guards.
  implication: Anthropic nil normalization and the reported false positive would violate explicit contracts if reproduced; buffering itself is approved unless its bounds/order are violated.
- timestamp: 2026-08-15T11:35:20-04:00
  checked: Fresh-cache golangci-lint and existing focused Anthropic/tool-result protocol suites.
  found: golangci-lint fails with exactly 12 revive exported-comment violations in branch-changed declarations; focused Anthropic and provenance suites both pass.
  implication: The lint ship gate is a confirmed changed-code defect. Existing focused tests do not cover the two reported runtime inputs, so direct probes are required.
- timestamp: 2026-08-15T11:39:10-04:00
  checked: Complete Anthropic tool-choice decoder, canonical initial policy, provenance classifier, and relevant tests.
  found: decodeAnthropicToolChoice recognizes only auto, any, and tool, so type none returns nil; toolProtocolPolicyFor treats nil as optional and only disables the guard for canonical type none. The provenance classifier sets provenanceClaim for any occurrence of "fabricated" and refusesUse for any occurrence of "cannot use", without tying either phrase to host-event provenance.
  implication: Both reported High mechanisms are confirmed by direct source tracing and contradict the approved policy/refusal contracts; minimal sanitized regression tests can reproduce them exactly.
- timestamp: 2026-08-15T11:48:00-04:00
  checked: Header parsing, post-tool capture, adapter observation ordering, integration-test commit diff, and plugin focused command.
  found: All surfaces pass only Header.Get's first contract/call-role value into Parse; Go documents Header.Get as returning the first value. Post-tool capture intentionally fully buffers for complete-response classification. Commit a28bb69 removed FakeEngine from a test name while retaining fakeEngine wiring. The unanchored plugin command in the checked-in review prompt does run the two suffixed real-chain tests.
  implication: Duplicate contract handling is order-dependent and security-relevant; full buffering is approved behavior; the rename weakens test labeling; the summarized zero-match-command claim needs its exact regex because the checked-in unanchored form is not zero-match.
- timestamp: 2026-08-15T11:52:30-04:00
  checked: Exact F1-F9 teardown statements against approved design/plan text and live branch behavior.
  found: F1 confirmed runtime gate; F2 confirmed runtime policy bug; F3 confirmed runtime classifier bug; F4 confirmed documentation contradiction because sections 8/9 explicitly approve static template changes while acceptance 21 says only the dynamic tail changes; F5 rejected as runtime defect because sections 10/11 require complete-response classification and withholding; F6 confirmed low runtime fail-closed gap; F7 confirmed low telemetry defect from commit 9d50a70's whole-response-only malformed fallback; F8 confirmed test-name integrity issue; F9 confirmed review-prompt command gap because its regex runs zero internal/toolcontract tests.
  implication: Runtime work must be split into strict RED/GREEN cycles; documentation/test-only corrections must remain separate and no documentation judgment change may occur before the manager checkpoint.
- timestamp: 2026-08-15T11:55:00-04:00
  checked: New sanitized Anthropic none regression test before any production change.
  found: TestWireToChatRequest_ToolChoice_None_PreservedVerbatim fails because req.ToolChoice is nil instead of canonical Type none.
  implication: F2 is reproducible through the wire normalization interface and the RED test will guard the minimal decoder fix.
- timestamp: 2026-08-15T12:08:00-04:00
  checked: Minimal F2 production change.
  found: decodeAnthropicToolChoice now recognizes `none` in the same no-name branch as auto and any; no downstream policy or recovery code changed.
  implication: A focused package pass will be a direct counterfactual test of the confirmed normalization-boundary root cause.
- timestamp: 2026-08-15T12:10:00-04:00
  checked: F2 focused GREEN verification with `go test ./internal/adapter/anthropic`.
  found: The full Anthropic adapter package passes, including the new none regression and all adjacent decoder tests.
  implication: The one-variable mapping fixes F2 at the normalization boundary without an observed adapter regression; the next strict RED cycle can begin on F3.
- timestamp: 2026-08-15T12:13:00-04:00
  checked: Complete F3 classifier implementation and test locations.
  found: The classifier independently ORs the bare token `fabricated` into provenanceClaim and the bare phrase `cannot use` into refusesUse; the focused table test lives in internal/engine/tool_result_protocol_test.go.
  implication: A single ordinary-prose false-positive row in that existing table is the smallest unconfounded RED test of the confirmed lexical mechanism.
- timestamp: 2026-08-15T12:15:00-04:00
  checked: Complete existing classifier table and current worktree diff.
  found: The table already covers true provenance refusals and one-sided negatives but lacks an ordinary domain-level sentence containing both lexical tokens; current tracked edits remain limited to the completed F2 cycle.
  implication: Adding one synthetic negative row changes only the missing behavioral dimension and preserves F2 isolation.
- timestamp: 2026-08-15T12:19:00-04:00
  checked: Focused F3 RED command `go test ./internal/engine -run '^TestToolResultProtocolRefusalClassifierRequiresConjunction$' -count=1`.
  found: Only the new `ordinary_data_assessment` subtest fails; the classifier returns true where ordinary prose must return false.
  implication: The exact F3 false-positive mechanism is reproducible in isolation before any classifier production change, and existing positive/negative table cases remain unaffected.
- timestamp: 2026-08-15T12:23:00-04:00
  checked: Minimal F3 GREEN implementation and sanitized regression table.
  found: Classifier conjunctions are now evaluated per sentence-delimited span and require the same span to mention a tool result, transcript, or tool event; the table includes all three adversarial ordinary-domain negatives while retaining the prior true positives.
  implication: The next focused test is an unconfounded counterfactual of the confirmed whole-response, target-free matching cause.
- timestamp: 2026-08-15T12:25:00-04:00
  checked: Focused F3 classifier counterfactual.
  found: `go test ./internal/engine -run '^TestToolResultProtocolRefusalClassifierRequiresConjunction$' -count=1` passes with both original true positives and all three sanitized negatives.
  implication: Span scoping fixes the reproduced false positives, but the generic `result` target remains broader than the approved high-confidence boundary and recovery-level input isolation still needs direct proof before F3 is accepted.
- timestamp: 2026-08-15T12:28:00-04:00
  checked: Complete real-engine tool-result recovery test implementation.
  found: `TestToolResultProtocolRecovery_NormalAnswerDoesNotRetry` already asserts one prompt and one lifecycle for ordinary prose, and its canonical request fixture exposes a single synthetic tool-result content field that can safely carry the adversarial authenticity/refusal tokens.
  implication: Strengthening that existing real-engine case directly proves result content is not scanned, without adding another test harness or changing recovery behavior.
- timestamp: 2026-08-15T12:30:00-04:00
  checked: Tightened F3 implementation and recovery fixture.
  found: The target allowlist is now only exact `tool result`, `transcript`, or `tool event`; a new exact-tool-result refusal is positive, and the real-engine normal-answer test supplies fabricated/refusal language exclusively in canonical tool-result content.
  implication: A combined focused run will test both classifier precision/recall and the recovery input boundary requested for F3 acceptance.
- timestamp: 2026-08-15T12:32:00-04:00
  checked: Combined focused F3 classifier and real-engine recovery counterfactual.
  found: Both tests pass: all three ordinary-domain negatives remain false, all three explicit provenance-refusal positives remain true, and fabricated/refusal language confined to tool-result content produces exactly one prompt with the ordinary model answer returned.
  implication: The confirmed F3 cause is addressed without broad generic-result matching or scanning untrusted result content; adjacent protocol coverage is the remaining F3 verification step.
- timestamp: 2026-08-15T12:34:00-04:00
  checked: Full focused engine protocol verification after F3 GREEN.
  found: `go test ./internal/engine -run 'Test.*(ToolProtocol|ToolResultProtocol|BuildBlocks_HostEvent|ObserveToolCallWrappers)' -count=1` passes.
  implication: F3 is GREEN across classifier, real recovery, host framing, initial/post-tool recovery, and wrapper-observation coverage; the next independent confirmed runtime defect can enter RED.
- timestamp: 2026-08-15T12:38:00-04:00
  checked: Complete shared parser, all three adapter negotiation seams, and all three real-router contract test files.
  found: Every adapter calls `toolcontract.Parse(r.Header.Get(...), r.Header.Get(...))`; the shared parser accepts exact `v1`, and no test sends more than one contract header value.
  implication: A `v1` then `v2` request hides the conflicting second value before shared validation on all surfaces; one real-router RED test isolates the adapter seam without changing production behavior.
- timestamp: 2026-08-15T12:40:00-04:00
  checked: Minimal F6 RED fixture before production changes.
  found: The existing OpenAI real-router contract test now sends sanitized `v1` then `v2` header lines and asserts HTTP 400 plus no engine dispatch.
  implication: Running only this subtest will directly distinguish fail-closed duplicate handling from first-value-only acceptance.
- timestamp: 2026-08-15T12:42:00-04:00
  checked: Focused F6 RED command `go test ./internal/adapter/openai -run '^TestOpenAIToolContract/conflicting_duplicate_versions_fail_closed_before_engine$' -count=1`.
  found: The real-router request returns HTTP 200 instead of 400 when `v1` precedes conflicting `v2`.
  implication: F6 is reproducible before production changes and confirms first-value-only negotiation accepts an ambiguous security-relevant contract request.
- timestamp: 2026-08-15T12:45:00-04:00
  checked: Strengthened F3 result-content isolation fixture and final combined rerun.
  found: The synthetic tool-result content now independently contains exact `tool result`, `fabricated`, and `cannot use` classifier tokens, yet the ordinary model answer still completes with one prompt; the combined classifier/recovery command passes.
  implication: The real recovery guard demonstrably classifies only model response text, not the untrusted tool-result content embedded in the request.
- timestamp: 2026-08-15T12:48:00-04:00
  checked: Minimal F6 implementation and cross-provider test additions.
  found: `toolcontract.ParseHeaders` now rejects more than one contract field value before parsing, all three adapters call it, and OpenAI/Anthropic/Ollama tests cover both v1-v2 orders with no-dispatch assertions; shared tests preserve absent, empty, v1, and diagnostic call-role behavior.
  implication: A single shared cardinality boundary now protects every provider; focused tests are the next counterfactual verification.
- timestamp: 2026-08-15T12:49:00-04:00
  checked: Targeted gofumpt invocation.
  found: The standalone `gofumpt` binary is not installed in this environment, so no file was formatted by that command.
  implication: Use the repository-declared formatter path or Go formatter fallback before testing; this does not affect the F6 hypothesis.
- timestamp: 2026-08-15T12:50:00-04:00
  checked: Repository formatter policy and targeted formatting.
  found: The Makefile explicitly falls back to gofmt when gofumpt is absent; targeted gofmt completed and `git diff --check` reports no whitespace errors.
  implication: The F6 change is formatted according to the available repository path and ready for focused behavioral verification.
- timestamp: 2026-08-15T12:51:00-04:00
  checked: Shared F6 parser suite with `go test ./internal/toolcontract -count=1`.
  found: The complete toolcontract package passes, including duplicate cardinality, legacy absent/empty, exact v1, and diagnostic call-role cases.
  implication: The shared parser behaves as designed; adapter routing is the remaining F6 counterfactual.
- timestamp: 2026-08-15T12:52:00-04:00
  checked: Exact cross-provider adapter contract suites.
  found: OpenAI, Anthropic, and Ollama contract suites all pass; both conflicting value orders return 400 and each fake engine remains undispatched while existing success, echo, absence, and unsupported-version cases remain green.
  implication: The F6 root cause is counterfactually fixed on all three HTTP surfaces; broader adjacent contract coverage remains before advancing.
- timestamp: 2026-08-15T12:54:00-04:00
  checked: Broader cross-provider adapter `ToolContract` regex.
  found: All adjacent ToolContract-named tests pass for OpenAI, Anthropic, and Ollama after the shared F6 change.
  implication: F6 is verified across shared parsing, provider negotiation, validation, streaming, and recovery-adjacent contract coverage; investigation can advance to independent F7.
- timestamp: 2026-08-15T12:58:00-04:00
  checked: Commit 9d50a70, complete wrapper observation/classification code, and focused observation tests.
  found: Commit 9d50a70 changed only the final malformed observation fallback from quoted-key substring detection to `hasWholeResponseToolCallMarker`; parsed exact and balanced embedded candidates still classify, but narrated truncated JSON produces no candidate and cannot satisfy the start-of-response marker. The existing documentation observation lacks quoted `"tool_call"` text.
  implication: F7 is a read-only wrapper-disposition telemetry regression, not a recovery eligibility, success outcome, adapter, or typed-error defect; a two-row focused test can reproduce it while guarding against broad substring false positives.
- timestamp: 2026-08-15T13:00:00-04:00
  checked: First F7 narrated/truncated fixture through `TestObserveToolCallWrappers`.
  found: The fixture truncated only closing braces after a complete string, so the bounded scanner safely repaired the braces and classified it `WrapperDispatcherEmbedded` rather than exercising the fallback.
  implication: Truncation repair is functioning; the RED fixture must truncate inside a string so parsing and repair both fail before the regressed fallback.
- timestamp: 2026-08-15T13:02:00-04:00
  checked: Focused F7 RED command `go test ./internal/engine -run '^TestObserveToolCallWrappers$' -count=1` with an unclosed-string narrated wrapper and quoted documentation negative.
  found: Only `narrated_truncated_hidden_wrapper` fails: `ObserveToolCallWrappers()` returns `none`, want `malformed`; the quoted documentation case remains `WrapperNone`.
  implication: F7 is reproducible at the read-only telemetry seam, and the test constrains GREEN to distinguish malformed wrapper-shaped syntax from benign quoted prose without changing recovery success behavior.
- timestamp: 2026-08-15T13:07:00-04:00
  checked: Minimal F7 telemetry implementation and focused observer counterfactual.
  found: `ObserveToolCallWrappers` now searches only its bounded scan prefix for the object/key start `{"tool_call"`; `TestObserveToolCallWrappers` passes, including narrated truncation, quoted documentation, and embedded hidden-wrapper non-execution.
  implication: The one-line fallback change fixes the reproduced telemetry classification without changing the executable extraction path; a dedicated combined extraction regression remains before F7 is accepted.
- timestamp: 2026-08-15T13:09:00-04:00
  checked: Combined F7 observation and hidden-wrapper extraction verification.
  found: `go test ./internal/engine -run '^(TestObserveToolCallWrappers|TestExtractToolCallWrappers_DeferredDispatcher)$' -count=1` passes; quoted documentation remains `WrapperNone`, narrated/truncated wrapper telemetry is malformed, and narrated hidden wrappers produce zero executable calls.
  implication: F7 is GREEN as a telemetry-only fix with the execution boundary preserved; no recovery, adapter, HTTP, or typed-error path changed.
- timestamp: 2026-08-15T13:12:00-04:00
  checked: F1 fresh-cache RED with `GOLANGCI_LINT_CACHE=$(mktemp -d) golangci-lint run ./...` before any comment change.
  found: The command exits 1 with exactly 12 revive exported-comment findings: one `WrapperNone` constant, five exported tool-protocol enum types, five leading constants for their blocks, and `HeaderContract`.
  implication: F1 is deterministically reproduced and confined to the reported three files; the lint failure is not stale cache state or another gate, and no F1 GREEN edit has begun.
- timestamp: 2026-08-15T12:01:00-04:00
  checked: First F1 GREEN fresh-cache lint counterfactual after five type comments and seven first-member const comments.
  found: Revive advanced to the next member in each of the seven const blocks and reported exactly seven exported-comment findings.
  implication: The original 12-line report represented five individual types plus seven undocumented const declaration blocks; the mechanical fix must document each block rather than only its first member.
- timestamp: 2026-08-15T12:04:00-04:00
  checked: Second F1 GREEN fresh-cache lint counterfactual after documenting five exported types and seven const declaration blocks.
  found: `golangci-lint run ./...` exits 0 with `0 issues.` using a new cache directory.
  implication: F1 is GREEN through the identical fresh-cache gate; the authorized behavior-neutral artifact corrections can now proceed independently.
- timestamp: 2026-08-15T12:08:00-04:00
  checked: Authorized F4/F5/F8/F9 diff and whitespace validation.
  found: The design, plan, and review prompt now distinguish exactly two one-time static clarifications from tail-only dynamic v1 policy; prompt invariant 5 preserves contract-independent inherited required/named recovery; eligible bounded post-tool complete-response TTFB and unchanged fail-open caps are explicit; the renamed test contains both ToolContract and FakeEngine; the focused regex includes TestParse* and TestContractHeaderConstants. `git diff --check` passes, and the review prompt implementation-tip/hash references are unchanged.
  implication: The requested non-runtime corrections are mechanically complete and ready for focused/full verification.
- timestamp: 2026-08-15T12:12:00-04:00
  checked: Corrected cross-package focused selector plus focused engine, plugin, and renamed F8 test commands.
  found: All focused commands exit 0. Verbose output proves internal/toolcontract executes TestParseContractVersion, TestParseContractCallRoleAllowlist, TestParseContractUnsupportedErrorDoesNotEchoValue, TestParseHeadersRejectsDuplicateContractValues, TestParseHeadersPreservesSingleValueAndDiagnosticCallRole, and TestContractHeaderConstants; the renamed ToolContract_FakeEngine test also runs and passes.
  implication: F8 and F9 are verified directly, and adjacent contract, selected-model, observation, engine protocol, and real hook-chain focused coverage remains GREEN.
- timestamp: 2026-08-15T12:15:00-04:00
  checked: Full non-race repository trust-gate components and final scope audit.
  found: `make fmt-check vet build`, `go test ./... -count=1`, fresh-cache `golangci-lint run ./...`, `make test-admin-js test-metrics-defaults arch-lint examples`, and `git diff --check` all exit 0. Final tracked diff is 20 files, 256 insertions, and 43 deletions; the debug session remains untracked. The review prompt implementation tip and review hash/range remain unchanged.
  implication: The original lint failure and every authorized runtime/documentation/test-integrity correction are self-verified without a race run, commit, deployment, release, or prompt hash-list finalization.

## Eliminated

- hypothesis: Full-response buffering on every eligible v1 explicit-model post-tool turn is an unnecessary runtime defect.
  evidence: The approved design requires the complete response to match a high-confidence refusal, forbids releasing refusal bytes before correction/error selection, and explicitly says both guards reuse bounded preflight with existing fail-open ceilings. The implementation follows those constraints and remains bounded; requiring a current tools catalog would narrow eligibility beyond the approved canonical tool-result criterion.
  timestamp: 2026-08-15T11:52:30-04:00
- hypothesis: A narrated wrapper missing only closing braces reaches the F7 malformed fallback.
  evidence: The bounded scanner repairs missing closing braces when the JSON string is complete, so the fixture classified as `WrapperDispatcherEmbedded` and did not exercise the fallback.
  timestamp: 2026-08-15T13:00:00-04:00

## Resolution

root_cause: F1 — five exported types and seven exported const declaration blocks lacked revive-compliant comments. F2 — `decodeAnthropicToolChoice` omitted valid Anthropic `none`. F3 — independent whole-response authenticity and refusal substrings matched ordinary domain prose without an explicit provenance target in the same span. F4 — design acceptance 21, plan checks, and review invariant 22 contradicted the two approved one-time static template clarifications; review invariant 5 also incorrectly made inherited required/named recovery v1-dependent. F5 — intentional complete-response TTFB for eligible bounded post-tool classification was not documented, although the runtime behavior and fail-open caps matched the approved design. F6 — every adapter collapsed duplicate contract header lines through `Header.Get` before shared validation. F7 — the read-only malformed observer required a whole-response prefix after bounded parsing failed, so narration before a truncated wrapper-shaped object incorrectly produced `WrapperNone`. F8 — a fake-engine integration test name dropped its honest seam marker. F9 — the focused regex omitted `internal/toolcontract`'s `TestParse*` and `TestContractHeaderConstants` names.
fix: Documented the five exported types and seven const declaration blocks. Added Anthropic `none` to the decoder's recognized no-name tool-choice values. Scoped provenance matching to sentence-delimited spans that explicitly identify a `tool result`, `transcript`, or `tool event`; added sanitized ordinary-domain negatives, exact-target positives, and real-engine result-content isolation. Reconciled the design, plan, and review prompt around exactly two one-time static template clarifications, tail-only dynamic v1 policy, and contract-independent inherited recovery. Documented intentional complete-response TTFB for eligible bounded post-tool attempts without changing eligibility or fail-open caps. Added one shared header-aware parser that rejects duplicate contract values and routed all three adapters through it. Changed only the bounded telemetry fallback to recognize the literal object/key start `{"tool_call"`, leaving extraction untouched. Renamed the integration test to retain both `ToolContract` and `FakeEngine`. Expanded the focused regex to execute `TestParse*` and `TestContractHeaderConstants`; the prompt's implementation tip and commit inventory were then advanced through the remediation commits.
verification: Fresh-cache F1 lint GREEN reports `0 issues.` Corrected focused cross-package, engine, plugin, and exact renamed F8 commands all pass; verbose output proves all six shared parser/header tests execute. Full non-race gates pass: `make fmt-check vet build`; `go test ./... -count=1`; fresh-cache `golangci-lint run ./...`; `make test-admin-js test-metrics-defaults arch-lint examples`; and `git diff --check`. No race suite was run because the checkpoint requested non-race verification.
files_changed:
  - docs/reviews/2026-08-15-model-selection-aware-tool-contract-adversarial-review-prompt.md
  - docs/superpowers/plans/2026-08-15-model-selection-aware-tool-contract.md
  - docs/superpowers/specs/2026-08-15-model-selection-aware-tool-contract-design.md
  - internal/adapter/anthropic/errors.go
  - internal/adapter/anthropic/tool_contract_test.go
  - internal/adapter/anthropic/wire.go
  - internal/adapter/anthropic/wire_test.go
  - internal/adapter/ollama/handlers.go
  - internal/adapter/ollama/tool_contract_test.go
  - internal/adapter/openai/errors.go
  - internal/adapter/openai/integration_test.go
  - internal/adapter/openai/tool_contract_test.go
  - internal/engine/coerce.go
  - internal/engine/coerce_test.go
  - internal/engine/tool_protocol.go
  - internal/engine/tool_result_protocol.go
  - internal/engine/tool_result_protocol_recovery_test.go
  - internal/engine/tool_result_protocol_test.go
  - internal/toolcontract/contract.go
  - internal/toolcontract/contract_test.go
