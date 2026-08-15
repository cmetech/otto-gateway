# Model-Selection-Aware Tool Contract Review Follow-Ups

**Date:** 2026-08-15
**Status:** Approved design; pending implementation
**Scope:** OTTO Gateway only

## Goal

Close the N1/N2 provenance-classifier gaps from the adversarial re-review, preserve the approved fail-closed corrective-output boundary identified as N3, and make the static prompt-byte contract independently enforceable for N4.

## N1/N2: Bounded Provenance-Refusal Classification

The classifier remains static, deterministic, and conservative. It examines only model response text and never tool-result content.

Classification uses a sliding window containing one sentence and, when present, its immediately adjacent sentence. A window matches only when it contains all of:

- a provenance target such as a tool result, transcript, or tool event;
- a provenance claim such as pre-scripted, fabricated, or not genuine; and
- either a current-speaker refusal such as `I cannot use`, `I can't use`, `I will not use`, `I won't use`, or `I refuse to use`, or an explicit denial that a live host tool event occurred.

Requiring a current-speaker refusal prevents quoted or reported third-person refusals from matching merely because provenance terms appear nearby. The adjacent-sentence window accepts natural refusals where the provenance claim and refusal are split across two sentences in either order. The window must not extend beyond one sentence boundary.

Existing non-provenance negatives remain negative, including domain statements about fabricated invoices, survey data, and certificates. Existing single-sentence provenance refusals remain positive.

## N3: Preserve Fail-Closed Corrective Output

No production behavior changes for N3. The approved contract requires a post-tool corrective attempt to return nonempty final prose and defines malformed corrective output as `selected_model_tool_result_provenance_failed`.

Wrapper-shaped or truncated `{"tool_call"` syntax in the corrective response therefore remains fail-closed, even when surrounded by narration. A focused regression test will make this boundary explicit. The remediation record will no longer describe the widened malformed-wrapper observation as telemetry-only because the same disposition is intentionally consumed by corrective-response validation.

This classification never authorizes or executes the wrapper.

## N4: Independent Prompt Golden

Add a sanitized literal golden fixture for the complete legacy prompt emitted by `buildBlocks`. The expected bytes must not reuse production prompt constants or prompt-building helpers.

The test will verify:

- the full legacy prompt bytes, including the two approved one-time static clarifications;
- the v1 prompt is that exact legacy prompt plus only the expected dynamic tool-policy tail; and
- sanitized tool names and arguments are used throughout the fixture.

## TDD and Verification

Implementation proceeds in three independent RED/GREEN cycles:

1. N1/N2 classifier cases fail, then pass with the bounded adjacent-sentence implementation.
2. N3's intended fail-closed behavior receives a characterization test; because the behavior already exists, mutation/counterfactual verification must demonstrate that the test detects removing the malformed-wrapper rejection.
3. N4's literal golden fails against a deliberately incorrect expected byte before the hand-derived fixture is finalized.

Focused engine tests run after every cycle. Before each atomic commit, run `git diff --check` and inspect the exact staged diff. Final verification repeats the Gateway full test, vet, focused race, lint/CI, and working-tree checks required by the approved implementation plan. No push, merge, release, deployment, tag, or Hermes change is authorized.
