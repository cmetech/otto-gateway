# LLM prompt — adversarial review of the model-selection-aware tool contract

> Paste everything below the line into a **fresh** coding session using a capable model or
> agent that did **not** implement this feature. Give it read access to the implementation
> worktree at
> `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/.worktrees/model-selection-aware-tool-contract`.
> This is a hostile, evidence-driven review of the Gateway implementation branch. The reviewer
> may run tests and create disposable test probes, but must not modify production files, commit,
> push, tag, publish, release, deploy, or touch the Hermes repository.

---

## Role

You are a hostile senior Go, protocol, and application-security reviewer. Your job is to
**break and disprove** the OTTO Gateway model-selection-aware tool-calling contract, not to
bless it. Assume passing tests may assert the wrong property, mocks may hide production behavior,
comments may repeat an unproven claim, and locally correct components may fail when composed across
ACP prompting, recovery, streaming adapters, hooks, metrics, and native wire formats.

Review this exact checkout and implementation range:

```text
Repository:         /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/.worktrees/model-selection-aware-tool-contract
Branch:             feat/model-selection-aware-tool-contract
Base:               ff3ca5c65b545329042b88085cbb510514a7b318
Implementation tip: 0e81856c73bb328ace56ebb77f066df7db28926f
Review diff:         ff3ca5c..0e81856
```

The checkout HEAD may be `0e81856` or a descendant whose only additional changes are the review
feedback documentation and resolved debug record. First verify the branch, confirm `0e81856` is an
ancestor, inspect `0e81856..HEAD`, and stop with a scope error if that later range contains any
production or test change or unrelated file. Review **every changed file** in `ff3ca5c..0e81856`.
Planning and documentation files matter where they define behavior or a release gate, but they are
not evidence that the implementation is correct.

Ground every finding in actual source, tests, and command output. Do not accept this prompt, the
design, plan, commit messages, comments, test names, or the implementation report as proof. A
passing test proves only what its assertions observe. Every finding must include a reachable
failure scenario: exact request, header set, canonical message sequence, ACP chunk sequence,
streaming interleaving, cancellation point, or model output leading to a wrong observable result.

This is a read-and-verify review. You may create a disposable `_test.go` probe when static
reasoning is insufficient, but remove it before finishing and prove the worktree returned to its
starting state. Do not edit production code or documentation. Use sanitized tool names and
fixtures only. Never place prompts, model text, schemas, arguments, tool results, credentials, or
raw session identifiers from a real or captured interaction in logs or reports. Sanitized synthetic
requests and model-output fixtures are allowed when needed for a reproducible finding. Do not modify
the original `main` worktree, its pre-existing untracked `.superpowers/` path, Hermes, or any sibling
repository.

## Authoritative contract — read completely before reviewing

Read every applicable `AGENTS.md` first, then read these files completely and in order:

1. `docs/superpowers/specs/2026-08-15-model-selection-aware-tool-contract-design.md`
2. `docs/superpowers/plans/2026-08-15-model-selection-aware-tool-contract.md`
3. `docs/superpowers/specs/2026-08-13-explicit-model-tool-protocol-recovery-design.md`
4. `docs/superpowers/plans/2026-08-13-explicit-model-tool-protocol-recovery.md`

The newer design is the product authority for the v1 request contract. The newer plan defines the
promised implementation and verification sequence. The older design and plan remain authoritative
for inherited lifecycle, buffering, recovery, and selected-model invariants. If implementation and
design disagree, report the disagreement; do not silently reinterpret the design to match code.

## Scope verification

Before looking for bugs:

1. Record the exact branch, HEAD, merge base, and starting worktree state.
2. Confirm the branch commits through the implementation tip are exactly:

   ```text
   4bb70a0 docs: approve model-selection-aware tool contract
   9dd7410 feat: add request-scoped tool contract metadata
   b8e69e2 feat: negotiate tool contract across gateway surfaces
   8f4e3be feat: express v1 tool policy in acp prompts
   7b29c90 feat: detect embedded dispatcher attempts without execution
   dcab525 feat: correct malformed mandatory tool attempts once
   9239f30 feat: frame host tool results as untrusted data
   68d9a74 feat: recover post-tool provenance refusals once
   9d50a70 feat: expose bounded tool contract outcomes
   a28bb69 test: prove tool contract surface equivalence
   5b11861 docs: add model tool contract adversarial review prompt
   2bc9584 fix: preserve anthropic prohibited tool policy
   372d6fe fix: constrain tool result provenance recovery
   28ad252 fix: reject duplicate tool contract headers
   45a9b86 fix: restore bounded wrapper telemetry
   0e81856 test: label fake engine tool contract coverage
   ```

3. Use `git diff --name-status ff3ca5c..0e81856` as the authoritative inventory.
4. Distinguish implementation defects from pre-existing behavior by proving changed-code causality.
5. Confirm no Hermes file or repository is included in the review range.

## Locked invariants to falsify

Produce an explicit **PASS / FAIL / UNPROVEN** matrix for every invariant. A matching test name is
not proof: trace the production path and name the assertion that would fail if the invariant
regressed.

1. `X-Otto-Tool-Contract: v1` is request-scoped. There is no environment, process-global, model,
   or session flag that activates it for another request.
2. Contract absence preserves legacy behavior. Exact `v1` is echoed on the response. Any nonempty
   unsupported version fails closed before engine or worker dispatch and never reflects the raw
   value.
3. `X-Otto-Call-Role` is diagnostic metadata only. Unknown values become a bounded enum and cannot
   enable recovery, change prompts, select a model, or authorize a tool.
4. An explicitly requested model remains authoritative for every initial and corrective ACP call.
   Gateway never silently switches it to `auto`, creates a replacement auto request, or infers a
   concrete downstream model for an auto request.
5. Inherited required/named tool-protocol recovery remains contract-independent for an explicit
   model with offered tools. Contract v1 gates only the enhanced embedded-dispatcher-wrapper
   behavior. Optional-tool turns may return ordinary prose. Tool-less turns and `tool_choice:none`
   remain unchanged.
6. An exact whole-response deferred dispatcher wrapper may surface only through an offered outer
   dispatcher tool. An unknown hidden inner wrapper surrounded by prose is never executed or
   surfaced directly.
7. A narrated hidden wrapper under v1 required/named semantics receives at most one static
   correction. A second invalid response becomes `selected_model_tool_protocol_failed` rather
   than another prompt, prose leakage, direct execution, or model fallback.
8. Initial recovery reuses one model, one ACP session, one prompt sequence, one watchdog, one
   request lifecycle, and at most one corrective prompt.
9. First-attempt and refusal bytes are withheld from streaming clients until Gateway has selected
   corrected output, first-attempt semantic fallback, bounded replay/live bypass, or a typed
   operational error.
10. Buffer-byte and chunk ceilings retain the approved fail-open replay behavior. Eligible bounded
    post-tool guards intentionally delay first-byte delivery until complete-response classification,
    including continuations without a current tool catalog. Cancellation, client disconnect, idle
    timeout, prompt failure, worker death, and terminal-result errors remain bounded and do not leak
    goroutines, sessions, slots, watchdogs, or partial bytes.
11. PreHooks and PostHooks execute exactly once per HTTP request. The model-request counter and
    recovery observer fire once; an internal corrective prompt does not create a second external
    hook or request lifecycle.
12. Under v1 explicit-model post-tool turns, Gateway emits a host-framed JSON event attesting only
    that a tool-result occurrence came from the host runtime. Tool-result `content` remains an
    ordinary JSON string containing untrusted data.
13. Quotes, newlines, apparent section headers, wrapper JSON, and prompt-injection text inside a
    tool result cannot escape its JSON field, change the stable ACP instruction prefix, authorize a
    tool, or become correction instructions.
14. OpenAI role-tool and Anthropic tool-result carriers produce equivalent ordered host-event
    envelopes. Ollama preserves its supported role-tool semantics. Empty and error results remain
    explicit rather than disappearing.
15. Post-tool recovery is a separate final-answer policy. Only a high-confidence complete-response
    provenance refusal is corrected: its provenance target and claim must share one sentence, its
    standalone first-person refusal or host-event denial may be at most one sentence adjacent, and
    first-person phrases require lexical boundaries. Quoted/attributed first-person text is an
    accepted limitation pending sanitized live-Kiro evidence. Ordinary prose and original
    legitimate tool calls pass through under their existing semantics.
16. The post-tool correction is static, uses the same explicit model and ACP session, never copies
    user text, model text, tool names, arguments, schemas, or tool output, and asks for final prose
    without another tool call.
17. A corrected post-tool response must be nonempty final prose. If an in-bounds completed
    correction is empty, repeats the refusal, contains a malformed wrapper, or emits a corrective
    tool call, Gateway returns only the buffered first response and records
    `fallback_first_attempt`. Prompt failure, timeout, worker death, terminal stream error, and
    cancellation retain the typed/context error path; corrective buffer bypass retains the second
    stream's replay/live handoff.
18. OpenAI JSON/SSE, Anthropic JSON/SSE, and Ollama JSON/NDJSON expose behaviorally equivalent
    success and typed-error outcomes without leaking suppressed wrapper or refusal text.
19. `unsupported_tool_contract_version`, `mandatory_tool_choice_not_supported`,
    `selected_model_tool_protocol_failed`, and
    `selected_model_tool_result_provenance_failed` use protocol-native, privacy-safe envelopes and
    pre-stream headers.
20. Observability uses closed enums and bounded counters only. Logs and metrics contain no prompts,
    model text, schemas, arguments, connector results, credentials, tool output, or raw session ID.
    Auto requests do not acquire an inferred concrete model label.
21. Gateway does not propagate `X-Hermes-Session-Id`, treat `X-Session-Id` as provenance proof, or
    add session propagation as part of this change.
22. System/tool templates change once for exactly the two approved static clarifications in design
    Sections 8 and 9.2, then remain stable. Dynamic v1 policy is appended only at the request tail;
    prior transcript order, offered-tool order, existing direct-wrapper behavior, and legacy auto
    routing behavior otherwise remain unchanged, with v1 host-result framing request-scoped.

## Change map — verify, do not trust

| Area | Landmarks | Intended responsibility |
|---|---|---|
| Contract metadata | `internal/toolcontract`, `internal/canonical` | Parse exact request headers, carry version/call role, define bounded native errors |
| Surface negotiation | OpenAI, Anthropic, and Ollama handlers/wire code | Echo v1, fail unknown versions before dispatch, normalize policy consistently |
| ACP prompt construction | `internal/engine/build_acp.go` | Apply exactly two approved one-time static clarifications; keep dynamic v1 policy tail-only; add host-framed untrusted result events |
| Wrapper observation | `internal/engine/coerce.go`, `tool_protocol.go` | Distinguish exact, embedded, malformed, and absent wrappers without expanding execution authority |
| Initial recovery | `internal/engine/engine.go`, `tool_protocol.go`, preflight tests | One same-session correction with bounded capture, replay/live handoff, cleanup, and typed failure |
| Post-tool recovery | `internal/engine/tool_result_protocol.go` and recovery tests | Classify provenance refusals, request final prose once, reject corrective tool calls |
| Native errors | canonical error definitions and adapter renderers | Stable safe codes in JSON, pre-SSE, and pre-NDJSON failure paths |
| Observability | engine event metadata, command observer, metrics | Closed contract/model/role/policy/wrapper/correction enums with safe correlation |
| Lifecycle regression | plugin hook regression and adapter integration/golden tests | Exactly-once hooks/counters, native wire equivalence, no suppressed-byte leakage |
| Architecture | `.go-arch-lint.yml` | Permit only the dependencies needed for shared contract parsing and engine behavior |

Late commits may deliberately touch files outside the abbreviated plan file lists. Judge whether
those changes are necessary production wiring or unreviewed scope expansion; do not ignore them.

## Attack campaign 1 — negotiation, request scope, and dispatch ordering

Drive every public route with absent, empty, exact `v1`, whitespace-padded, case-variant, duplicate,
comma-joined, oversized, control-character, and unknown contract headers. Include every call-role
value and duplicate call-role headers.

- Prove unsupported versions fail before request normalization side effects, engine invocation,
  session creation, model activation, prompt construction, and streaming headers.
- Prove the echo appears on every v1 success and every v1 error path, including validation and
  mandatory-policy rejection where the contract was recognized.
- Try to make one v1 request affect a following headerless request through shared adapter, engine,
  pool, session, metric, or package state.
- Compare OpenAI chat/completions, OpenAI completions, Anthropic messages, Ollama chat, and Ollama
  generate. Identify intentionally unsupported combinations rather than treating inconsistent
  behavior as parity.
- Confirm Ollama `/generate` required/named v1 requests fail before prompting, while supported
  `/chat` policy forms normalize to the same canonical policy as OpenAI and Anthropic.

Any request-scoping leak, unknown-version dispatch, or missing fail-closed behavior is at least
**High**.

## Attack campaign 2 — explicit-model authority and eligibility

Trace `ChatRequest.Model` from wire decode through engine Run, `SetModel`, the first prompt, the
correction, events, native response, and error.

- Use empty, `auto`, explicit valid-looking, explicit unknown, and whitespace/case variants.
- Force activation failure, first-prompt failure, correction-prompt failure, worker death,
  cancellation, and timeout. Look for any branch that retries through auto or creates another
  session with a different model.
- Verify exactly one `SetModel` occurs for eligible explicit requests and zero for auto.
- Attack required, named, optional, none, no-tools, invalid named tool, and conflicting policy
  forms. Confirm eligibility is derived from normalized structured policy, never user prose.
- Confirm model telemetry records the requested model with bounded cardinality and never infers an
  auto request's concrete downstream model from output, timing, or session behavior.

Silent explicit-to-auto fallback or a corrective call using another model is **Critical**.

## Attack campaign 3 — hidden wrappers, authorization, and parser boundaries

Treat model text as hostile input. Attack `ObserveToolCallWrappers`, wrapper extraction, and their
interaction with existing coercion using sanitized tools such as `tool_call`, `lookup_item`, and
`get_weather`.

- Exact dispatcher wrapper, fenced and unfenced; whitespace before/after; multiple wrappers;
  arrays; truncation; malformed braces; escaped quotes; raw control characters; invalid UTF-8;
  very large responses; and chunk boundaries at every byte.
- Narrated prefix, narrated suffix, both prefix and suffix, quoted documentation examples, wrapper
  text inside a JSON string, markdown prose, thought chunks, and a native tool call plus duplicate
  wrapper text.
- Unknown inner name, offered inner name, offered outer dispatcher absent, multiple possible outer
  tools, named policy selecting a non-dispatcher tool, and malicious arguments containing another
  wrapper.
- Verify whole-response-only authorization. Detection may justify one correction; it must never
  itself grant execution authority.
- Re-run and independently inspect
  `TestStream_ProseEmbeddedHiddenWrapperDoesNotUseDispatcher`. Prove it observes actual executable
  frames/calls, not only absence of one substring.
- Check the Task 8 refinement from substring detection to whole-response marker detection. Find a
  malformed executable wrapper it now misses, or a benign quoted example it still misclassifies.

Unknown hidden-tool execution is **Critical**. A high-confidence false positive that turns optional
prose into a tool call or required correction is **High**.

## Attack campaign 4 — initial correction, buffering, and lifecycle

Model exact two-attempt interleavings through bounded preflight and the same ACP prompt sequence.

- First attempt: valid native call, valid direct wrapper, exact dispatcher, narrated dispatcher,
  capability refusal, built-in-tool denial, required missing, named mismatch, malformed wrapper,
  empty response, oversized response, and terminal error.
- Corrective attempt: valid call, another narration, wrong named call, new hidden tool, prose,
  empty response, buffer bypass, blocked stream, prompt error, worker death, and cancellation.
- Prove the first attempt's bytes never reach SSE/NDJSON before a correction decision. Test real
  header commitment and flush behavior rather than relying only on an in-memory recorder.
- At byte/chunk ceiling boundaries, prove the approved replay/live fail-open handoff preserves
  order, final result, cancellation, and PostHook forensics without double delivery.
- Race context cancellation, watchdog timeout, `Stream.Result`, prompt-sequence finish, ACP Cancel,
  and PostHooks. Look for double finish, double cancel, leaked hold, leaked goroutine, or a second
  PostHook.
- Confirm corrective prompts are static and contain none of the first output, request, tool name,
  arguments, schema, or sensitive fixture canaries.

Any unbounded retry, failed-byte release, or same-request model/session change is at least **High**.

## Attack campaign 5 — host tool-result framing and prompt injection

Independently decode the ACP blocks generated from OpenAI, Anthropic, and Ollama tool-result turns.

- Content containing quotes, backslashes, CR/LF, Unicode separators, markdown fences, bracketed
  section headers, apparent `[System]` blocks, embedded wrapper JSON, null bytes, very large values,
  and explicit prompt-injection instructions.
- Multiple results, repeated call IDs, missing IDs, empty content, error content, mixed text blocks,
  and tool results in unexpected transcript positions.
- Verify standard `encoding/json` output parses as one host-event object and the exact content
  round-trips as a string. No content byte may escape into the surrounding ACP instruction stream.
- Verify host framing attests only occurrence. Search the static identity guard and correction for
  any wording that treats content as trusted instructions or proves connector authenticity.
- Compare OpenAI and Anthropic carrier envelopes structurally, including order and error/empty
  markers. Identify any surface-specific field that changes model-visible semantics.
- Confirm legacy and auto requests retain their approved prior framing.

Tool-result content acquiring instruction authority, leaking into static correction text, or
escaping the JSON event is **Critical**.

## Attack campaign 6 — post-tool refusal classification and final-answer correction

Attack the deterministic classifier with both false negatives and dangerous false positives.

- Exact known provenance refusal, paraphrases, mixed case, punctuation, contractions, partial
  conjunctions, generic safety refusal, ordinary caution, result-level error, unrelated use of
  words such as "fabricated", quoted refusal text inside normal prose, and multilingual text.
- Refusal plus native tool call, refusal plus direct wrapper, empty output, thoughts only, and
  oversized output. Original legitimate tool calls must retain existing semantics rather than be
  transformed into final prose.
- Confirm correction eligibility depends on the final canonical tool-result carrier, v1, and an
  explicit model—not merely a `post_tool` diagnostic role.
- Inspect the correction byte-for-byte. It must not copy tool output, user text, model refusal,
  tool identity, arguments, schema, or connector details, and it must explicitly require prose with
  no further tool call.
- Corrective output: ordinary prose, second refusal, empty text, malformed wrapper, exact wrapper,
  native tool call, buffer bypass, timeout, worker death, and prompt failure. Completed semantic
  rejection must return the exact first response without leaking or executing the second response;
  operational failures and buffer bypass must retain their distinct outcomes.
- Prove no first-attempt bytes precede corrected prose, first-attempt fallback selection, bounded
  replay/live bypass, or the typed operational error on any streaming surface.

A false positive that suppresses a legitimate answer or tool call is **High**. A correction that
executes another tool or incorporates untrusted result content is **Critical**.

## Attack campaign 7 — native wire equivalence and terminal errors

For every success and terminal outcome, independently parse native wire output rather than relying
on substring tests.

- OpenAI JSON and SSE: tool-call argument JSON string, `finish_reason`, frame ordering, `[DONE]`,
  and JSON error before SSE headers.
- Anthropic JSON and SSE: `tool_use` content block, input object, block indices, event ordering,
  `message_delta`, `message_stop`, stop reason, and native error before SSE headers.
- Ollama JSON and NDJSON: object-shaped arguments, line boundaries, `done`, `done_reason`, and JSON
  error before NDJSON headers.
- Exact dispatcher success, narrated-then-corrected success, initial second-failure typed error,
  post-tool semantic fallback, auto unchanged, optional prose, normal post-tool answer, corrected
  provenance answer, prompt-injection result, and provenance operational error in both streaming
  and non-streaming modes.
- Confirm raw wrapper, narrated text, refusal text, causes, schemas, arguments, and tool output are
  absent from error envelopes and suppressed success streams.
- Confirm adapter reroute paths and PostHook aggregation do not double-render, double-hook, or
  change error commitment timing.

Malformed native output that breaks a conforming client or a cross-surface policy discrepancy is
at least **High**.

## Attack campaign 8 — errors, telemetry, privacy, and cardinality

Trace every new error and event from production call site through adapter rendering, logging, and
Prometheus recording.

- Unknown internal error codes, wrapped errors, nil causes, cancellation causes, long model IDs,
  arbitrary request IDs, malformed call roles, wrapper dispositions, and correction paths.
- Confirm error mapping recognizes only the intended canonical selected-model codes and never
  exposes `Cause` text.
- Enumerate every logged field and metric label. Attempt to inject prompts, model output, schemas,
  arguments, tool results, credentials, connector identifiers, raw session IDs, or arbitrary
  high-cardinality strings.
- Confirm contract version, role, selection mode, tool policy, wrapper disposition, correction kind,
  reason, and outcome are closed enums with an explicit safe fallback.
- Verify request IDs use the existing correlation seam. No raw session ID is logged; if no approved
  safe hash exists, session correlation must be omitted.
- Prove each guarded request records one model request and one recovery outcome, not one per ACP
  prompt. Check corrected, first-attempt, failed, buffer-bypass, activation-failure, and nil-observer
  paths.

Sensitive-content logging, request-controlled unbounded labels, or raw session identifiers are
**Critical**.

## Attack campaign 9 — architecture and non-regression

- Run architecture lint and inspect `.go-arch-lint.yml`. Confirm the new `toolcontract` dependency
  permissions are narrow and adapters did not acquire engine, pool, session, or policy-layer
  dependencies outside already approved seams.
- Prove there is no environment variable, package global, or mutable singleton controlling v1.
- Compare ACP system/tool templates and prior transcript blocks between base and feature for
  headerless, v1-ineligible, auto, tool-less, optional, none, and post-tool cases. Account for
  exactly the two approved one-time static clarifications, and reject any additional prefix drift or
  per-attempt dynamic policy outside the request tail.
- Inspect direct declared-wrapper behavior to ensure the new embedded-dispatcher guard did not
  remove an explicitly preserved legacy path.
- Confirm no `X-Hermes-Session-Id` propagation, no new session registry dependency, and no claim that
  `X-Session-Id` solves provenance.
- Verify the feature branch contains no Hermes changes and no deployment/release automation change.

## Attack campaign 10 — test integrity and evidence gaps

Assume the 3,000-plus added lines and green suite still miss the important composition bug.

- Identify which tests exercise a real engine plus adapter and which stop at a fake engine returning
  already-corrected canonical chunks. A fake renderer test does not prove recovery produced them.
- Determine whether streaming tests observe actual first-byte commitment or only final recorder
  content.
- Check whether hook/counter tests use the production observer wiring or merely increment a test
  callback. Trace exactly what remains unproved about Prometheus exposition.
- Confirm injection tests prove JSON structural containment and static-correction independence, not
  merely absence of one canary in the final response.
- Inspect every table-driven subtest for duplicated fixtures that make different labels exercise the
  same path without proving distinct behavior.
- Review cap, cancellation, timeout, worker-death, prompt-failure, buffer-bypass, and semantic-
  fallback tests for deterministic synchronization. Sleeps and permissive upper bounds may hide
  logical races.
- Search for skips, environment-gated live tests, cached results, overly broad regex filters, and
  assertions against mocks rather than public behavior.
- Name the single highest-risk behavior not proved by the existing suite.

The following are known evidence gaps, not automatic code defects:

- The plan's Task 10 sanitized real-Gateway session experiment was not performed.
- Deployed direct v1 selected-model probes were not performed.
- A local cgo-free build or synthetic ACP test does not prove real Kiro model behavior.
- Hermes/Gateway end-to-end integration is outside this repository review.

Mark these **UNPROVEN** unless you obtain valid isolated evidence. Do not silently promote them to
PASS, and do not call the implementation defective solely because deployment was intentionally not
authorized.

## Mandatory verification

Run from the implementation worktree. Record exact commands, exit codes, skips, cache use, and
unavailable tools. Do not silently substitute narrower commands.

```bash
git status --short --branch
git merge-base main HEAD
git log --oneline --reverse ff3ca5c..0e81856
git diff --check ff3ca5c..0e81856
git diff --name-status ff3ca5c..0e81856

make fmt-check
go vet ./...
go test ./... -count=1

go test ./internal/toolcontract ./internal/canonical \
  ./internal/adapter/openai ./internal/adapter/anthropic ./internal/adapter/ollama \
  -run 'Test(.*(ToolContract|SelectedModel|Observation)|Parse.*|ContractHeaderConstants)' -count=1

go test ./internal/engine \
  -run 'Test.*(ToolProtocol|ToolResultProtocol|BuildBlocks_HostEvent|ObserveToolCallWrappers)' \
  -count=1

go test ./internal/plugin \
  -run 'TestToolProtocolRecovery_HookOnceRealChain' -count=1

go test -race ./internal/engine ./internal/adapter/openai \
  ./internal/adapter/anthropic ./internal/adapter/ollama -count=1

review_lint_cache="$(mktemp -d)"
PATH="$(go env GOPATH)/bin:$PATH" \
  GOLANGCI_LINT_CACHE="$review_lint_cache" \
  golangci-lint run ./...

"$(go env GOPATH)/bin/go-arch-lint" check --project-path .
"$(go env GOPATH)/bin/govulncheck" ./...
```

Run cgo-free targeted Gateway builds into a new temporary directory. Do not delete or overwrite
user files:

```bash
review_build_dir="$(mktemp -d)"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -o "$review_build_dir/otto-gateway-linux-amd64" ./cmd/otto-gateway

CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  go build -o "$review_build_dir/otto-gateway-darwin-arm64" ./cmd/otto-gateway

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -o "$review_build_dir/otto-gateway-windows-amd64.exe" ./cmd/otto-gateway

shasum -a 256 "$review_build_dir"/*
```

Finally run the canonical repository gate with a fresh lint cache:

```bash
review_ci_cache="$(mktemp -d)"
PATH="$(go env GOPATH)/bin:$PATH" \
  GOLANGCI_LINT_CACHE="$review_ci_cache" \
  make ci
```

Do not run live Gateway, wrapper, release, or deployment commands against the operator's real
configuration. Do not use real credentials, connector results, project listings, or captured model
text. A deterministic disposable probe may use only isolated test-owned paths and sanitized data.

## Known baseline exclusions and accepted scope

Do not attribute these to this branch without proving changed-code causality:

1. The original `main` worktree has a pre-existing untracked `.superpowers/` path. Do not read,
   modify, stage, delete, or report its contents as part of this review.
2. Live Kiro, deployed Gateway, and Hermes integration evidence is intentionally absent from Tasks
   1–9. Record this as UNPROVEN release evidence rather than fabricating a pass.
3. Task 10's session experiment is separate and intentionally does not authorize session-header
   propagation.
4. No deployment, release, tag, push, merge, or cross-repository action is part of the review.
5. The feature does not promise a natural-language intent parser, model-based refusal classifier,
   cryptographic content attestation, or trusted connector output.
6. Optional-tool turns are allowed to return ordinary prose. Tool-less and `tool_choice:none` turns
   are intentionally not forced to call a tool.
7. Buffer-cap bypass retains the previously approved fail-open replay semantics; do not call the
   mere existence of that policy a defect. A violation of its bounds or byte-order guarantees is in
   scope.

If a tool or platform is unavailable, record it as unavailable. Do not replace it with a narrower
command and claim equivalence.

## Severity

- **Critical** — an unknown hidden tool executes or surfaces directly from prose; an explicit
  model silently changes to auto or another model; untrusted tool-result content escapes framing,
  becomes correction instructions, or gains execution authority; suppressed bytes reach a client
  before correction/error selection; prompts, credentials, connector results, tool output, or raw
  session IDs leak through ordinary errors/logs/metrics; exploitable data race, deadlock, or
  unbounded retry.
- **High** — unsupported contract dispatches; more than one correction occurs; same-request session
  or hook lifecycle duplicates; post-tool correction accepts another tool call; required/named
  policy is mis-normalized; cancellation/watchdog/buffer/replay behavior breaks; cross-surface
  native wire or policy divergence; a practical high-confidence classifier false positive or
  false negative with a wrong externally observable result.
- **Medium** — bounded telemetry is materially incorrect; safe error code/status drift; call-role or
  wrapper disposition misclassification without authority expansion; meaningful performance or
  allocation regression; architecture or documentation drift that could cause unsafe integration.
- **Low** — weak or misleading test, minor naming/wording ambiguity, maintainability defect, or
  unavailable evidence that is incorrectly described but does not create a current runtime fault.

Do not inflate severity. A theoretical concern without a reachable input and wrong outcome is not
a finding. Conversely, do not downgrade a hidden execution, model fallback, byte leak, or untrusted
content escalation because its triggering model output is unusual.

## Deliverable

Produce one self-contained adversarial review with this exact structure:

1. **Scope verification** — branch, HEAD, ancestry, exact review range, prompt-only descendant,
   starting worktree state, changed-file inventory, and unavailable tools.
2. **Verdict** — `SHIP`, `SHIP WITH FOLLOW-UPS`, or `DON'T SHIP`, with the single most important
   reason.
3. **Findings table**, sorted by severity:
   `file:line | severity | invariant violated | defect | concrete failure scenario | minimal fix`.
4. **Top-five reproductions** — exact HTTP request/headers, canonical transcript, ACP chunks,
   streaming interleaving, cancellation sequence, or runnable disposable test and the wrong
   observable result.
5. **Invariant matrix** — all 22 locked invariants marked PASS / FAIL / UNPROVEN with production
   source and test/command evidence. No omitted rows.
6. **Test-integrity assessment** — distinguish real composition coverage from fake-engine renderer
   coverage; name the highest-risk behavior the suite does not truly prove.
7. **What I verified safe and why** — concrete reasoning for areas with no finding, especially
   request scope, explicit-model authority, hidden-wrapper non-execution, static corrections,
   untrusted result framing, byte suppression, hook counts, and native wire equivalence.
8. **Verification evidence** — exact commands, exit codes, skips, cache use, lint/architecture/
   vulnerability output, build hashes, and final worktree status.
9. **Required remediation before ship** — ordered minimal fixes for every Critical/High finding and
   the focused regression test each fix needs.
10. **Release-evidence gaps** — Task 10 session experiment, deployed direct probes, real Kiro
    behavior, and Hermes integration explicitly separated from code findings.

Do not stop at the first bug. Do not spend the report praising architecture. Do not report a
concern without tracing it to a reachable wrong outcome. Be specific or be silent.
