# Model-Selection-Aware Tool Contract Pass-3 Follow-Ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Close the confirmed P1/P2 provenance-classifier false positives and make completed semantic correction failures return the safely buffered first response without weakening operational error handling.

**Architecture:** Keep the classifier deterministic and sentence-bounded: bind the provenance target and claim to one sentence, then look only one sentence away for a standalone first-person refusal or host-event denial. Preserve the first bounded replay stream through the single corrective attempt and use it only when that attempt completes normally but fails semantic validation; prompt, capture, cancellation, and buffer-bypass branches retain their existing behavior.

**Tech Stack:** Go 1.23, canonical chat request/stream types, ACP prompt sequences, table-driven Go tests, Prometheus metrics.

## Global Constraints

- X-Otto-Tool-Contract: v1 remains request-scoped; no environment or global feature flag.
- The explicitly requested model remains authoritative; never silently switch an explicit model to auto.
- Never execute an unknown hidden tool wrapper embedded in prose.
- Optional-tool turns may return ordinary prose; tool_choice: none and tool-less turns remain unchanged.
- Allow at most one same-model corrective ACP prompt per HTTP request.
- Tool-result content remains untrusted data and must never be copied into corrective prompts.
- Preserve hook exactly-once behavior, streaming byte suppression, cancellation, watchdogs, buffer ceilings, and replay/live semantics.
- Preserve OpenAI, Anthropic, and Ollama behavioral equivalence.
- Do not propagate X-Hermes-Session-Id, log sensitive content, or modify the Hermes repository.
- P3 is an accepted limitation: quoted or attributed first-person refusal text may still match until sanitized live-Kiro evidence justifies a different classifier.
- Completed semantic correction failures fall back only after bounded capture; operational failures remain typed errors and corrective buffer bypass remains the live second stream.
- Do not push, merge, release, deploy, or tag.

---

### Task 1: Bind Provenance Claims and First-Person Refusals Correctly

**Files:**
- Modify: internal/engine/tool_result_protocol.go
- Test: internal/engine/tool_result_protocol_test.go

**Interfaces:**
- Consumes: isHighConfidenceToolResultProvenanceRefusal(text string) bool and its existing bounded sentence split.
- Produces: containsStandalonePhrase(text, phrase string) bool, used only for bounded first-person refusal phrases; classifier semantics in which target and claim share a sentence while refusal/denial may be immediately adjacent.

- [ ] **Step 1: Add P1 and P2 regression rows to the real classifier table**

Add literal negative cases to TestToolResultProtocolRefusalClassifierRequiresConjunction:

~~~go
{
    name: "adjacent fabricated domain subject does not inherit tool target",
    text: "The tool result lists three invoices. The auditor found they were fabricated, and I cannot use them for the filing.",
    want: false,
},
{
    name: "api suffix is not first person",
    text: "The fabricated tool result means the API cannot use it.",
    want: false,
},
{
    name: "cli suffix is not first person",
    text: "The fabricated tool result means the CLI cannot use it.",
    want: false,
},
{
    name: "ui suffix is not first person",
    text: "The fabricated tool result means the UI cannot use it.",
    want: false,
},
~~~

Keep the existing pass-2 positives for adjacent refusal in both orders and the existing three-sentence-gap negative.

- [ ] **Step 2: Run the focused classifier test and verify RED**

Run:

~~~bash
go test ./internal/engine -run '^TestToolResultProtocolRefusalClassifierRequiresConjunction$' -count=1
~~~

Expected: FAIL because the P1 row and all three P2 rows currently return true.

- [ ] **Step 3: Implement the minimal sentence binding and lexical-boundary rule**

Change the classifier so provenanceTarget and provenanceClaim are evaluated only on the current sentence. Build a refusal window from the current sentence plus at most its immediate previous and next sentences. Use a byte-bounded helper for the fixed ASCII phrases:

~~~go
func containsStandalonePhrase(text, phrase string) bool {
    for offset := 0; offset < len(text); {
        index := strings.Index(text[offset:], phrase)
        if index < 0 {
            return false
        }
        index += offset
        end := index + len(phrase)
        beforeWord := index > 0 && isASCIIWordByte(text[index-1])
        afterWord := end < len(text) && isASCIIWordByte(text[end])
        if !beforeWord && !afterWord {
            return true
        }
        offset = index + 1
    }
    return false
}

func isASCIIWordByte(value byte) bool {
    return value >= 'a' && value <= 'z' ||
        value >= '0' && value <= '9' ||
        value == '_'
}
~~~

The classifier loop must bind target and claim before building the refusal window:

~~~go
for index, span := range spans {
    provenanceTarget := containsProvenanceTarget(span)
    provenanceClaim := containsProvenanceClaim(span)
    if !provenanceTarget || !provenanceClaim {
        continue
    }

    refusalWindow := span
    if index > 0 {
        refusalWindow = spans[index-1] + " " + refusalWindow
    }
    if index+1 < len(spans) {
        refusalWindow += " " + spans[index+1]
    }
    if containsFirstPersonRefusal(refusalWindow) || deniesHostEvent(refusalWindow) {
        return true
    }
}
~~~

Keep the vocabulary closed to the already approved targets, claims, refusals, and host-event denials. Do not add P3 quote, attribution, or negation phrase lists.

- [ ] **Step 4: Run focused and package tests and verify GREEN**

Run:

~~~bash
go test ./internal/engine -run '^TestToolResultProtocolRefusalClassifierRequiresConjunction$' -count=1
go test ./internal/engine -count=1
~~~

Expected: PASS. The P1/P2 negatives, N2 adjacent positives in both orders, and the three-sentence bound all hold.

- [ ] **Step 5: Inspect and commit the classifier correction**

Run:

~~~bash
git diff --check
git add internal/engine/tool_result_protocol.go internal/engine/tool_result_protocol_test.go
git diff --cached --check
git diff --cached --stat
git diff --cached
git commit -m "fix: bind provenance refusal classification"
~~~

Expected: the staged diff contains only classifier production code and its focused regression tests.

---

### Task 2: Fall Back to the Buffered First Attempt After Semantic Correction Failure

**Files:**
- Modify: internal/engine/engine.go
- Modify: internal/engine/tool_protocol.go
- Modify: internal/metrics/metrics.go
- Test: internal/engine/tool_result_protocol_recovery_test.go
- Test: internal/metrics/kiro_test.go

**Interfaces:**
- Consumes: toolProtocolAttemptCapture.stream, which is already a bounded replay stream after full capture; correctedToolResultResponseIsFinalProse; finishFullyCaptured; ToolProtocolEvent.
- Produces: OutcomeFallbackFirstAttempt ToolProtocolOutcome = "fallback_first_attempt"; engine behavior that returns first.stream only after a completed, in-bounds second attempt fails semantic validation.

- [ ] **Step 1: Add an observable semantic-fallback recovery test**

Replace the semantic cases in TestToolResultProtocolRecovery_CorrectiveFailuresReturnTypedError with a table-driven TestToolResultProtocolRecovery_CompletedSemanticFailureReturnsFirstAttempt. Cover these second attempts:

~~~go
{
    name: "repeated refusal",
    second: recoveryPromptScript{stream: recoveryTextStream(toolResultRefusal(), nil)},
},
{
    name: "empty correction",
    second: recoveryPromptScript{stream: recoveryTextStream("", nil)},
},
{
    name: "malformed wrapper",
    second: recoveryPromptScript{stream: recoveryTextStream(
        "The caller would send {\"tool_call\":{\"name\":\"get_weather\"",
        nil,
    )},
},
{
    name: "corrective tool call",
    second: recoveryPromptScript{stream: recoveryToolStream("get_weather")},
},
~~~

For every row, call Collect and assert that the answer equals a literal firstResponse, no corrective tool calls appear, two prompts use the same session, SetModel runs once, the prompt sequence finishes once, the session is not canceled, PreHook/PostHook each run once, and one enriched event has literal outcome ToolProtocolOutcome("fallback_first_attempt"), reason ReasonToolResultProvenanceRefusal, the first-attempt wrapper disposition, and CorrectiveAttempts: 1.

Retain timeout and worker-death rows under TestToolResultProtocolRecovery_CorrectiveOperationalFailuresReturnTypedError. Add a corrective Prompt error row if no existing test covers it. Keep TestToolResultProtocolRecovery_ContextCancellationWins unchanged.

- [ ] **Step 2: Run the focused recovery tests and verify RED**

Run:

~~~bash
go test ./internal/engine -run '^TestToolResultProtocolRecovery_(CompletedSemanticFailureReturnsFirstAttempt|CorrectiveOperationalFailuresReturnTypedError|ContextCancellationWins)$' -count=1
~~~

Expected: FAIL because every completed semantic-invalid second attempt currently returns selected_model_tool_result_provenance_failed instead of the first response. Operational cases continue to demonstrate their existing typed/context error behavior.

- [ ] **Step 3: Add the bounded fallback outcome and minimal engine branch**

Add the bounded outcome with its exported comment:

~~~go
// OutcomeFallbackFirstAttempt indicates that a completed semantic correction
// was rejected and the bounded first attempt was returned instead.
OutcomeFallbackFirstAttempt ToolProtocolOutcome = "fallback_first_attempt"
~~~

In recoverToolResultProtocol, retain all current error and buffer-bypass branches. Replace only the terminal semantic failure after the existing context check:

~~~go
if ctxErr := ctx.Err(); ctxErr != nil {
    return fail(1, ctxErr)
}
finishFullyCaptured(first.stream)
e.observeToolProtocol(ctx, req, ToolProtocolEvent{
    Model: req.Model, Reason: reason, Outcome: OutcomeFallbackFirstAttempt,
    WrapperDisposition: wrapperDisposition,
    CorrectiveAttempts: 1,
})
return first.stream, nil
~~~

Do not call fail, cancel the session, run error cleanup, release the second response, or execute its tool calls on this branch.

- [ ] **Step 4: Run the focused recovery tests and verify GREEN**

Run:

~~~bash
go test ./internal/engine -run '^TestToolResultProtocolRecovery_(CompletedSemanticFailureReturnsFirstAttempt|CorrectiveOperationalFailuresReturnTypedError|ContextCancellationWins)$' -count=1
go test ./internal/engine -run '^TestToolResultProtocolRecovery_' -count=1
~~~

Expected: PASS. Semantic invalidity returns only the first response; prompt failure, timeout, worker death, and cancellation remain errors.

- [ ] **Step 5: Add the metric acceptance test and verify RED**

Add fallback_first_attempt to the literal outcomes fixture in TestMetrics_ToolProtocolEventsAcceptOnlyClosedReasonsAndOutcomes:

~~~go
outcomes := []string{
    "first_attempt",
    "corrected",
    "failed",
    "buffer_bypass",
    "fallback_first_attempt",
}
~~~

Run:

~~~bash
go test ./internal/metrics -run '^TestMetrics_ToolProtocolEventsAcceptOnlyClosedReasonsAndOutcomes$' -count=1
~~~

Expected: FAIL because the metrics allowlist currently drops fallback_first_attempt.

- [ ] **Step 6: Add the new outcome to the closed metrics allowlist and verify GREEN**

Extend only validToolProtocolOutcome:

~~~go
case "first_attempt", "corrected", "failed", "buffer_bypass", "fallback_first_attempt":
    return true
~~~

Run:

~~~bash
go test ./internal/metrics -run '^TestMetrics_ToolProtocolEventsAcceptOnlyClosedReasonsAndOutcomes$' -count=1
go test ./internal/engine ./internal/metrics -count=1
~~~

Expected: PASS, including the new bounded metric series and all engine recovery tests.

- [ ] **Step 7: Inspect and commit the semantic fallback**

Run:

~~~bash
git diff --check
git add internal/engine/engine.go internal/engine/tool_protocol.go internal/engine/tool_result_protocol_recovery_test.go internal/metrics/metrics.go internal/metrics/kiro_test.go
git diff --cached --check
git diff --cached --stat
git diff --cached
git commit -m "fix: preserve first response after semantic correction failure"
~~~

Expected: the staged diff contains only the fallback outcome, narrow semantic branch, closed metrics handling, and regression coverage.

---

### Task 3: Reconcile the Normative Contract and Close the Debug Record

**Files:**
- Modify: docs/superpowers/specs/2026-08-15-model-selection-aware-tool-contract-design.md
- Modify: docs/reviews/2026-08-15-model-selection-aware-tool-contract-adversarial-review-prompt.md
- Modify: docs/superpowers/specs/2026-08-15-model-selection-aware-tool-contract-pass3-followups-design.md
- Move: .planning/debug/pass3-classifier-fallback.md to .planning/debug/resolved/pass3-classifier-fallback.md

**Interfaces:**
- Consumes: the verified P1/P2 classifier behavior, fallback_first_attempt, and unchanged operational-failure branches.
- Produces: one non-contradictory review contract and a resolved evidence record that explicitly preserves the accepted P3 limitation.

- [ ] **Step 1: Update normative prose to match verified behavior**

In the base design §§10.2 and 10.4 and review invariant 17, state:

- target and provenance claim must co-occur in one sentence;
- refusal or host-event denial may be in that sentence or one immediately adjacent sentence;
- first-person phrases require lexical boundaries;
- P3 quoted/attributed first-person text remains accepted pending sanitized live-Kiro evidence;
- completed semantic-invalid correction returns the buffered first response with fallback_first_attempt;
- prompt failure, timeout, worker death, cancellation, and corrective buffer bypass retain their prior behavior.

Update adversarial campaigns that currently expect a typed error for every second semantic-invalid response so they instead require no second-response byte or tool-call leakage and exact first-response fallback. Keep initial caller-tool recovery's second-failure typed error unchanged.

- [ ] **Step 2: Resolve the debug evidence record**

Update the debug record with the exact RED/GREEN command evidence produced during Tasks 1 and 2. Set status to resolved, fill root cause, fix, verification, and changed-files fields with literal implementation facts, update the timestamp, and move it to .planning/debug/resolved/pass3-classifier-fallback.md.

- [ ] **Step 3: Inspect and commit the documentation reconciliation**

Run:

~~~bash
git diff --check
git add docs/superpowers/specs/2026-08-15-model-selection-aware-tool-contract-design.md docs/reviews/2026-08-15-model-selection-aware-tool-contract-adversarial-review-prompt.md docs/superpowers/specs/2026-08-15-model-selection-aware-tool-contract-pass3-followups-design.md .planning/debug/pass3-classifier-fallback.md .planning/debug/resolved/pass3-classifier-fallback.md
git diff --cached --check
git diff --cached --stat
git diff --cached
git commit -m "docs: reconcile pass3 recovery behavior"
~~~

Expected: a docs/debug-only commit with no production or test files.

---

### Task 4: Verify the Direct Gateway v1 Release Gate

**Files:**
- Verify only; no new files expected.

**Interfaces:**
- Consumes: all prior task commits.
- Produces: fresh focused and repository-wide evidence suitable for the Gateway v1 release-gate decision.

- [ ] **Step 1: Run focused behavioral verification**

Run:

~~~bash
go test ./internal/engine -run '^TestToolResultProtocolRefusalClassifierRequiresConjunction$' -count=1
go test ./internal/engine -run '^TestToolResultProtocolRecovery_' -count=1
go test ./internal/metrics -run '^TestMetrics_ToolProtocolEventsAcceptOnlyClosedReasonsAndOutcomes$' -count=1
~~~

Expected: PASS.

- [ ] **Step 2: Run the full required Gateway gates**

Run:

~~~bash
go test ./... -count=1
go vet ./...
go test -race ./internal/engine ./internal/adapter/openai ./internal/adapter/anthropic ./internal/adapter/ollama -count=1
make ci
~~~

Expected: every command exits 0, including lint, architecture, vulnerability, and cross-build checks reached by make ci.

- [ ] **Step 3: Inspect final integrity and scope**

Run:

~~~bash
git diff --check
git status --short --branch
git log --oneline 17ab51e..HEAD
git -C /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway status --short --branch
~~~

Expected: the feature worktree is clean; only the intended local commits follow 17ab51e; the main checkout's pre-existing untracked .superpowers/ remains unmodified and uncommitted.
