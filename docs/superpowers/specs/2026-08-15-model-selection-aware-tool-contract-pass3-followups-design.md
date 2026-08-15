# Model-Selection-Aware Tool Contract Pass-3 Follow-Ups

**Date:** 2026-08-15
**Status:** Implemented; direct release-gate verification pending
**Scope:** OTTO Gateway only

## Purpose

This delta supersedes only §§10.2 and 10.4 of `2026-08-15-model-selection-aware-tool-contract-design.md`. All other request scoping, explicit-model authority, hidden-wrapper non-execution, prompt safety, streaming, lifecycle, adapter-equivalence, and privacy requirements remain unchanged.

## Refusal Classification

The classifier remains static, deterministic, bounded, and string-based.

A match requires:

1. one sentence containing both a provenance target (`tool result`, `transcript`, or `tool event`) and a provenance claim (`pre-scripted`, `prescripted`, `fabricated`, `not genuine`, or the existing embedded-transcript conjunction); and
2. either a first-person refusal or explicit host-event denial in that same sentence or one immediately adjacent sentence.

First-person refusal phrases require lexical boundaries on both sides. In particular, `API cannot use`, `CLI cannot use`, and `UI cannot use` do not contain the standalone phrase `I cannot use`.

The provenance target and provenance claim may not be assembled from different sentences. This prevents an adjacent sentence about fabricated invoices or another domain subject from inheriting a prior sentence's `tool result` target. The refusal alone may remain adjacent so all approved N2 phrasings continue to match in either order.

## Accepted P3 Limitation

Quoted or attributed first-person refusal text may still match. Gateway will not add quotation, attribution, or negation phrase lists without sanitized live-Kiro evidence. Such heuristics would be language- and punctuation-dependent while still failing to establish the active speaker reliably.

P3 is accepted because the recovery outcome below makes a false positive non-destructive: it may spend one bounded corrective prompt but cannot convert a fully captured first response into a typed 502 solely because the completed correction is semantically invalid.

## Completed Semantic Correction Fallback

When the first fully captured response matches the classifier, Gateway still sends at most one static corrective prompt on the same ACP session and explicit model.

If the second attempt completes within the existing byte and chunk ceilings but is rejected as final prose because it is empty, repeats the provenance refusal, contains wrapper-shaped malformed output, or emits a corrective tool call, Gateway returns the buffered first response. It never releases or executes the rejected corrective response.

The fallback records a new bounded outcome, `fallback_first_attempt`, with `CorrectiveAttempts: 1`. Hooks and the external model counter still fire exactly once for the client request. The prompt sequence finishes once, and the session is not canceled merely because semantic correction failed.

## Operational Failures Remain Errors

No fallback occurs for:

- context cancellation or client disconnect;
- corrective prompt invocation failure;
- corrective capture timeout;
- worker death or terminal stream error; or
- buffer/chunk ceiling bypass.

Cancellation retains context-error precedence. Prompt, timeout, and worker failures retain `selected_model_tool_result_provenance_failed`. A corrective buffer bypass preserves the existing replay/live handoff of the second stream. These branches must not be reclassified as semantic fallback.

## Safety and Streaming

- No first-response bytes are released before the correction outcome is known.
- The rejected second response is never released and never used to construct or authorize a tool call.
- The first response is already bounded and fully captured; fallback adds no new unbounded buffer.
- The static corrective prompt remains free of user text, model text, tool names, arguments, schemas, and tool output.
- OpenAI, Anthropic, and Ollama must expose equivalent final response and lifecycle behavior.

## Verification Contract

Strict RED/GREEN coverage must prove:

- P1 and the API/CLI/UI P2 variants are negative;
- every existing and pass-2 N2 positive remains positive in both sentence orders;
- a three-sentence gap remains negative;
- P3 is documented rather than expanded into a new heuristic;
- repeated refusal, empty correction, malformed wrapper, and corrective tool call return only the first response;
- rejected corrective bytes and tool calls never leak or execute;
- prompt failure, timeout, worker death, cancellation, and buffer bypass preserve their prior behavior;
- hooks, model counters, correction counts, session reuse, buffer ceilings, replay/live handoff, and all three adapters remain equivalent; and
- the full Gateway test, vet, race, lint, architecture, vulnerability, and cross-build gates remain green.

No deployment, push, merge, release, or tag is part of this work.
