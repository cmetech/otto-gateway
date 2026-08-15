---
slug: pass3-classifier-fallback
status: investigating
trigger: "Address confirmed pass-3 P1/P2 classifier false positives, accept P3, and make semantic correction failures non-destructive"
created: 2026-08-15T16:00:00-04:00
updated: 2026-08-15T16:00:00-04:00
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

hypothesis: Confirmed — sentence-pair concatenation loses subject binding, substring refusal checks lack word boundaries, and semantic correction failure discards an already buffered first response.
test: Add strict RED classifier and recovery tests before any production change.
expecting: P1/P2 become false; N2 adjacent refusals remain true; P3 stays a documented accepted limitation; completed semantic correction failures replay only the first response.
next_action: Commit the approved design delta, write the implementation plan, then begin the P1/P2 RED cycle.
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
  status: pending
  failure_output: ""

## Evidence

- timestamp: 2026-08-15T16:00:00-04:00
  checked: Exact HEAD classifier with disposable P1/P2/P3 probes.
  found: P1, API/CLI/UI P2 variants, and P3 all return true.
  implication: All three mechanisms are confirmed; P1/P2 require fixes and P3 must be recorded as an accepted limitation rather than patched with attribution heuristics.
- timestamp: 2026-08-15T16:05:00-04:00
  checked: `recoverToolResultProtocol` and bounded capture ownership.
  found: A fully captured first response is retained in `first.stream` as a replay stream. Operational errors and buffer bypass branch before the completed second response reaches semantic validation.
  implication: A fallback can be limited to completed, in-bounds semantic correction failures while preserving cancellation, timeout, prompt failure, worker death, and buffer-bypass behavior.

## Eliminated

- hypothesis: Add quoted-speech and negation phrase lists to the classifier.
  evidence: This would continue synthetic phrase tuning without live Kiro ground truth and cannot robustly establish speaker attribution.
- hypothesis: Fall back on every corrective failure.
  evidence: Prompt errors, timeouts, worker death, and cancellation are operational failures and must remain explicit rather than being masked by stale prose.

## Resolution

root_cause: ""
fix: ""
verification: ""
files_changed: []
