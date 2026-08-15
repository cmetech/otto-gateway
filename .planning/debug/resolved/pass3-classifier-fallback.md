---
slug: pass3-classifier-fallback
status: resolved
trigger: "Address confirmed pass-3 P1/P2 classifier false positives, accept P3, and make semantic correction failures non-destructive"
created: 2026-08-15T13:45:00-04:00
updated: 2026-08-15T14:03:33-04:00
---

# Debug Session: pass3-classifier-fallback

## Symptoms

DATA_START
**Expected behavior:** Contract-v1 post-tool recovery classifies only high-confidence provenance refusals. A classifier false positive may spend one bounded same-model corrective attempt but must not turn an otherwise usable response into a 502.

**Actual behavior:** The adjacent-sentence window can combine a provenance target in one sentence with an unrelated fabricated subject in the next. The first-person refusal check also matches the suffix of API, CLI, and UI. Quoted first-person refusals remain indistinguishable from the current speaker under the approved string-only classifier. Any semantically invalid completed correction currently returns `selected_model_tool_result_provenance_failed`.

**Error messages:** A second refusal, malformed corrective response, or corrective tool call returns `selected_model_tool_result_provenance_failed` as HTTP 502.

**Timeline:** Reported by pass 3 after commits `457f247`, `9507020`, and `ab5c95e` closed the pass-2 N1/N2/N3/N4 findings.

**Reproduction:** Classify the sanitized P1 invoice example, the API/CLI/UI P2 variants, and a quoted first-person P3 refusal. Then run post-tool recovery with a first provenance refusal followed by a fully captured second refusal, malformed wrapper, empty response, or corrective tool call.
DATA_END

## Current Focus

hypothesis: Confirmed and resolved — sentence-pair concatenation lost subject binding, substring refusal checks lacked word boundaries, and semantic correction failure discarded an already buffered first response.
test: Focused classifier, recovery, cancellation, buffer-bypass, and metrics tests exercise the corrected behavior and unchanged operational branches.
expecting: P1/P2 are false; N2 adjacent refusals remain true; P3 stays a documented accepted limitation; completed semantic correction failures replay only the first response.
next_action: Run the full direct Gateway release-gate verification without deployment or cross-repository action.
reasoning_checkpoint:
  hypothesis: "Binding target and provenance claim to one sentence while allowing only the refusal in an adjacent sentence fixes P1 without reopening N2; bounded semantic fallback removes false-positive 502s without hiding operational failures."
  confirming_evidence:
    - "Disposable exact-HEAD probes reproduced P1, all three P2 suffix variants, and P3."
    - "The first fully captured response is retained as a replay stream after classification."
    - "Timeout, worker death, prompt error, cancellation, and buffer bypass have distinct branches before semantic validation."
  falsification_test: "Focused RED/GREEN tests fail to preserve N2, operational failures begin returning fallback prose, or the rejected corrective output leaks or executes."
  fix_rationale: "Constrain lexical relationships at their source and make only completed semantic misclassification non-destructive."
  blind_spots: "P3 remains because a deterministic string classifier cannot reliably resolve quoted speaker attribution without brittle heuristics; live Kiro evidence is still absent."
tdd_checkpoint:
  test_file: internal/engine/tool_result_protocol_test.go
  test_name: TestToolResultProtocolRefusalClassifierRequiresConjunction
  status: green
  failure_output: "RED returned classifier=true for the P1 invoice sentence and API/CLI/UI P2 variants; GREEN passed the focused classifier and full engine package tests."

## Evidence

- timestamp: 2026-08-15T13:45:00-04:00
  checked: Exact HEAD classifier with disposable P1/P2/P3 probes.
  found: P1, API/CLI/UI P2 variants, and P3 all return true.
  implication: All three mechanisms are confirmed; P1/P2 require fixes and P3 must be recorded as an accepted limitation rather than patched with attribution heuristics.
- timestamp: 2026-08-15T13:47:00-04:00
  checked: `recoverToolResultProtocol` and bounded capture ownership.
  found: A fully captured first response is retained in `first.stream` as a replay stream. Operational errors and buffer bypass branch before the completed second response reaches semantic validation.
  implication: A fallback can be limited to completed, in-bounds semantic correction failures while preserving cancellation, timeout, prompt failure, worker death, and buffer-bypass behavior.
- timestamp: 2026-08-15T14:00:00-04:00
  checked: `go test ./internal/engine -run '^TestToolResultProtocolRefusalClassifierRequiresConjunction$' -count=1` before classifier production changes.
  found: The focused test failed on exactly P1 and the API/CLI/UI P2 cases.
  implication: The new fixtures reproduced the confirmed mechanisms rather than failing for setup or compilation reasons.
- timestamp: 2026-08-15T14:01:00-04:00
  checked: Focused classifier test and `go test ./internal/engine -count=1` after sentence binding and lexical boundaries.
  found: Both commands passed; existing N2 sentence-order positives and the three-sentence-gap negative remained green.
  implication: P1/P2 closed without reopening the pass-2 false-negative class.
- timestamp: 2026-08-15T14:02:00-04:00
  checked: Focused semantic-fallback recovery test before the engine production change.
  found: Repeated refusal, empty correction, malformed wrapper, and corrective tool call all returned the typed post-tool error instead of the first response.
  implication: The recovery test observed the old destructive behavior before implementation.
- timestamp: 2026-08-15T14:03:00-04:00
  checked: Focused recovery, cancellation, corrective buffer-bypass, engine-package, and metrics-package tests after implementation.
  found: Semantic-invalid completed corrections returned only the first response with one fallback event; prompt error, timeout, worker death, and cancellation remained errors; corrective buffer bypass streamed the second attempt; the closed metric accepted `fallback_first_attempt`.
  implication: The fallback is restricted to completed semantic rejection and preserves the distinct operational branches.

## Eliminated

- hypothesis: Add quoted-speech and negation phrase lists to the classifier.
  evidence: This would continue synthetic phrase tuning without live Kiro ground truth and cannot robustly establish speaker attribution.
- hypothesis: Fall back on every corrective failure.
  evidence: Prompt errors, timeouts, worker death, and cancellation are operational failures and must remain explicit rather than being masked by stale prose.

## Resolution

root_cause: "The sentence-pair classifier combined unrelated subjects and used unbounded substring checks; completed semantic rejection discarded an already bounded first response."
fix: "Bind the provenance target and claim to one sentence, require standalone first-person refusal phrases, and replay the first response only after completed semantic correction rejection."
verification: "Focused classifier, semantic-fallback, operational-failure, cancellation, corrective-buffer-bypass, engine-package, and metrics-package tests passed. The full direct Gateway release gate remains the next action."
files_changed:
  - internal/engine/tool_result_protocol.go
  - internal/engine/tool_result_protocol_test.go
  - internal/engine/engine.go
  - internal/engine/tool_protocol.go
  - internal/engine/tool_result_protocol_recovery_test.go
  - internal/metrics/metrics.go
  - internal/metrics/kiro_test.go
  - docs/superpowers/specs/2026-08-15-model-selection-aware-tool-contract-design.md
  - docs/superpowers/specs/2026-08-15-model-selection-aware-tool-contract-pass3-followups-design.md
  - docs/reviews/2026-08-15-model-selection-aware-tool-contract-adversarial-review-prompt.md
