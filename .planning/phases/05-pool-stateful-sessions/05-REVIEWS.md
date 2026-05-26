---
phase: 05
reviewers: [codex]
reviewed_at: 2026-05-26T15:20:09Z
plans_reviewed:
  - 05-04-PLAN.md
  - 05-05-PLAN.md
review_target: gap_closure
skipped_reviewers:
  claude: self-identification (running inside Claude Code)
  gemini: CLI not installed
  coderabbit: CLI not installed
  opencode: CLI not installed
  qwen: CLI not installed
  cursor: CLI not installed
  ollama: no local server at http://localhost:11434
  lm_studio: no local server at http://localhost:1234
  llama_cpp: no local server at http://localhost:8080
---

# Cross-AI Plan Review — Phase 05 (Gap Closure)

**Review target:** Plans 05-04 (diagnose & fix SC3 + close SC4) and 05-05 (PHASE5-PERF.md skeleton + manual gates).

**Context provided to reviewers:** CLAUDE.md, ROADMAP phase 5 section, REQUIREMENTS.md (POOL-/SESS-/OBSV-02), 05-VERIFICATION.md (gaps to close), 05-REVIEW-FIX.md (already-applied code-review fixes — explicitly out of scope), 05-CONTEXT.md (locked design decisions), 05-04-PLAN.md, 05-05-PLAN.md.

**Out of scope for reviewers:** Plans 05-01..03 (already shipped), CR-01..04, WR-01,03,04,05,06,07 (already fixed), WR-02 (intentionally deferred per D-18 conflict).

---

## Codex Review

(codex-cli 0.133.0, default model gpt-5.5)

**Summary**

05-04 is mostly strong: it forces a real wire trace before implementation, ties the fix to live e2e verification, and strengthens the weak cancel-in-flight test. It should close the SC3/SC4 gaps if the executor actually follows the diagnostic sequence. The main blind spot is that the plan's leading H-A remediation, "fresh `session/new` per request," may make requests return 200 while weakening true stateful conversation semantics. 05-05 is directionally right, but its manual gates are not truly enforceable unless the plan explicitly says skeleton-only execution is not phase closure and forbids updating `VERIFICATION.md` on skipped measurements.

**Strengths**

- 05-04 correctly treats the live `kiro-cli` failure as a protocol problem, not a unit-test problem.
- The shim approach is realistic because it does not rely on undocumented `kiro-cli` debug flags.
- Capturing both working pool and broken session transcripts against the same binary/environment is the right diagnostic shape.
- The plan explicitly prevents changing the already-verified pool path unless the trace proves it is necessary.
- Strengthening `DeleteSession_CancelsInFlight` to wait for at least one streamed chunk closes the "passes because prompt failed fast" loophole.
- 05-04 keeps live e2e as the final authority, which addresses the original fake-client blind spot better than unit tests alone.
- 05-05 correctly separates skeleton generation from human measurement and records concrete thresholds.

**Concerns**

- **HIGH: H-A remediation may close HTTP 500 while breaking statefulness.**
  Task 4 says `Entry.NewSession` may switch to per-request `Client.NewSession`. That could make every request on the same `X-Session-Id` use the same subprocess but a fresh ACP session. If ACP session id carries conversation history, this satisfies "same child PID" but not "stateful session." Add an e2e two-turn continuity assertion before accepting H-A.

- **HIGH: The plan still gives the executor a path to short-circuit to H-A.**
  Task 3 says H-A is "MOST LIKELY," and Task 4 bakes H-A tests into the likely behavior. A rushed executor could implement the guessed fix after seeing method-order differences without proving that the diff explains `-32603`. Require a negative/confirmatory experiment: reproduce the failing path, then manually replay or patch only the suspected sequence and show `-32603` disappears.

- **HIGH: 05-05 can still "close" with skipped/manual-unavailable results.**
  The success criteria allow latency "SKIPPED with recorded reason" and RSS "SKIPPED if 05-04 is not green," while Task 4 can still write "Satisfied." That conflicts with the actual gap: performance/RSS are unmeasured. Skips should produce `closed-with-notes` only for baseline-only Go data, and `NOT-closed` for missing Node or missing RSS.

- **MEDIUM: Transcript timestamp merge is flawed across subprocesses.**
  Task 1 says timestamps are "nanoseconds since process start," then asks to merge files by timestamp. Multiple shim processes have independent zero points, so cross-process ordering is invalid. Use wall-clock `time.Now().UTC().Format(time.RFC3339Nano)` plus PID, or avoid cross-process merging and keep per-PID traces.

- **MEDIUM: Shim argument contract is underspecified.**
  `KIRO_CMD=/tmp/kiro-shim KIRO_ARGS="$(which kiro-cli) acp"` only works if the shim treats `argv[1]` as the real binary and `argv[2:]` as args. The plan should state this explicitly and verify the shim records the real command line.

- **MEDIUM: Fake ACP tests can regress into the same blind spot.**
  The new fake sequence tests are useful as regression guards, but they cannot prove compatibility with real `kiro-cli`. The plan partially handles this with live e2e, but the unit-test language should be narrowed: "guards the chosen sequence" rather than "proves the fix."

- **MEDIUM: Cancel-in-flight chunk assertion may be too weak if it only counts one line.**
  One NDJSON line could be an error/protocol metadata line unless the test asserts it is a valid Ollama streaming response chunk and not an error envelope. Require parsing the line and checking it contains expected response/message fields and no `"error"`.

- **LOW: `golangci-lint run` may be unavailable.**
  The plan makes it a gate, but prior verification only mentions `go vet`, `go test`, and `go build`. If `golangci-lint` is not installed/configured, this creates avoidable execution ambiguity. Either make it conditional or reference the repo's actual lint target.

- **LOW: 05-04 asks to update `VERIFICATION.md` to `verified` before 05-05.**
  Since 05-05 still closes the manual perf/RSS gate, 05-04 should only mark SC3/SC4 reverified, not global phase `status: verified`.

**Suggestions**

- Add a 05-04 must-have: two requests with the same `X-Session-Id` demonstrate state continuity, not just same process affinity and HTTP 200.
- Change H-A remediation wording from "likely behavior" to "candidate behavior; only valid if transcript plus confirmatory experiment proves ACP state is process-scoped or otherwise preserved."
- Require `05-04-WIRE-DIFF.md` to include a "Rejected Hypotheses" section for H-B/H-C/H-D with evidence from the trace.
- Fix shim timestamping: use RFC3339Nano wall-clock timestamps and include shim PID / child PID on every frame.
- Require the broken transcript to include the full JSON-RPC error object, especially `error.data`, and fail the task if only the gateway's wrapped HTTP error is captured.
- Strengthen `DeleteSession_CancelsInFlight` to parse the pre-DELETE NDJSON chunk and assert it is a non-error assistant/content chunk.
- In 05-05, make skipped Node latency or skipped RSS leave gap 3 open unless a human explicitly records `ACCEPTED-WITH-NOTES` with rationale.
- In 05-05, do not allow "Manual Perf + RSS Gate — Satisfied" when either measurement is still `TBD`, `AWAITING MANUAL MEASUREMENT`, `BLOCKED_ON_05-04`, or `NODE_IMPL_UNAVAILABLE`.

**Risk Assessment**

Overall risk: **MEDIUM-HIGH**.

05-04 has the right diagnostic posture, but the likely H-A fix risks confusing subprocess affinity with true session state. That is the main execution risk. 05-05 has a process risk: it may create the missing report artifact without actually satisfying the manual measurement gate. Tightening those two points before execution would reduce the combined risk to medium.

---

## Consensus Summary

Only one external reviewer (codex) was available in this environment. The "consensus" is therefore the codex view, with the orchestrator (Claude/Opus) implicitly cross-checked against it during plan synthesis. Re-run with `--gemini`, `--opencode`, or `--ollama` once those CLIs/servers are available to broaden adversarial coverage.

### Top Concerns (HIGH severity)

1. **Subprocess affinity ≠ stateful session** — Plan 05-04 may "fix" SC3 by switching `Entry.NewSession` to per-request `Client.NewSession`, returning HTTP 200 while losing conversation continuity. The `requirements/SESS-01` truth in CONTEXT.md is "Requests with X-Session-Id header use a *dedicated* kiro-cli subprocess via SessionRegistry" — but the load-bearing claim is *stateful* continuity, not just affinity. **Action:** Plan 05-04 needs a must-have asserting **two-turn state continuity** (turn 1 sends "remember the number 7"; turn 2 sends "what number did I tell you?" — response references 7) on the same X-Session-Id, in addition to same-PID and HTTP 200.

2. **Hypothesis short-circuit risk** — Task 4's `<behavior>` block ranks H-A as "MOST LIKELY" and prescribes tests aligned with H-A. A rushed executor could skip the wire-diff (Task 3) and patch the H-A symptom. **Action:** Plan 05-04 needs a binding rule that Task 4 GREEN code cannot land unless `05-04-WIRE-DIFF.md` contains a "Rejected Hypotheses" section explicitly explaining (with cited transcript frames) why H-B, H-C, and H-D do *not* explain the `-32603` error.

3. **05-05 gate enforcement** — The skeleton can be created and `VERIFICATION.md` re-stamped without the manual measurements actually being taken. **Action:** 05-05 Task 4 must reject "Manual Perf + RSS Gate — Satisfied" if the latency table or RSS table contains `TBD`, `AWAITING MANUAL MEASUREMENT`, `BLOCKED_ON_05-04`, or `NODE_IMPL_UNAVAILABLE`. The only paths to closure are full measurement OR explicit human `ACCEPTED-WITH-NOTES` with rationale.

### MEDIUM Concerns (worth addressing)

4. **Cross-process timestamp merge invalid** — If the shim uses process-start-relative nanoseconds across multiple `kiro-cli` children, frames can't be globally ordered. **Action:** Use `time.Now().UTC().Format(time.RFC3339Nano)` + PID on every frame, or keep traces per-PID.

5. **Shim argument contract** — Underspecified handling of `KIRO_CMD=/tmp/kiro-shim KIRO_ARGS="$(which kiro-cli) acp"`. **Action:** Plan must explicitly document `argv[1]=real binary, argv[2:]=args` and the shim must log the resolved command line.

6. **Fake-ACP test scope** — Plan should not claim fake-ACP unit tests "prove the fix" — they guard the chosen protocol sequence. Live e2e remains the authority.

7. **Cancel-in-flight chunk parsing** — The new ≥1 chunk assertion is stronger but still weak if a single NDJSON line could be a protocol error envelope. **Action:** Parse the chunk; assert it contains an assistant/content field and no `"error"` key.

### LOW Concerns

8. `golangci-lint` may not be in the project's lint pipeline — either condition the gate on its presence or use the repo's existing target.

9. 05-04 should not re-stamp `VERIFICATION.md status: verified` globally — only SC3/SC4. Phase status flips to `verified` after 05-05's manual gate passes too.

### Areas of Reviewer Agreement with Original Plan

- Diagnostic-before-patch discipline (Tasks 1→2→3 before Task 4) is the right shape.
- Shim approach (no `kiro-cli --debug` reliance) is realistic.
- Strengthening `DeleteSession_CancelsInFlight` is correct in spirit (codex wants it stronger still).
- Out-of-scope discipline (don't touch pool code, don't re-plan CR-01..04 / WR-01..07) is correctly enforced.
- 05-05's separation of skeleton (autonomous) vs measurement (human-action) is correct.

### Divergent Views

None — single reviewer.

---

## Recommended Action

Plans 05-04 and 05-05 are **fundamentally sound** but have **three HIGH-severity tightening opportunities** before execution:

- Add two-turn state-continuity assertion to 05-04 must-haves
- Make 05-04 Task 4 GREEN contingent on `05-04-WIRE-DIFF.md` having a "Rejected Hypotheses" section
- Make 05-05 Task 4 verify reject placeholder/SKIPPED tokens in the measurement tables

To incorporate this feedback into the plans:

```
/gsd-plan-phase 5 --gaps --reviews
```

The planner will read `05-REVIEWS.md` alongside `05-VERIFICATION.md` and revise 05-04 and 05-05 accordingly (preserving existing 05-01..03).
