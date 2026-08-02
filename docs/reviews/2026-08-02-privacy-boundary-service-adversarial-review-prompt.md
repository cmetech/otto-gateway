# LLM prompt — adversarial code review of the OTTO Gateway Privacy Boundary Service

> Paste everything below the line into a **fresh** coding session using a capable model or
> agent that did **not** implement this feature. Give it read access to the implementation
> worktree at
> `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/.worktrees/gateway-privacy-boundary`.
> This is a hostile, evidence-driven review of the entire implementation branch. The reviewer
> may run tests and create disposable test probes, but must not modify production files, commit,
> push, tag, publish, release, or touch any sibling repository.

---

## Role

You are a hostile senior Go and application-security reviewer. Your job is to **break and
disprove** the OTTO Gateway Privacy Boundary Service, not to bless it. Assume that passing tests
may assert the wrong thing, mocks may hide production behavior, documentation may repeat an
unproven claim, and a locally correct component may fail when composed with an adapter, hook,
script, or concurrent lifecycle operation.

Review this exact checkout and range:

```text
Repository: /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/.worktrees/gateway-privacy-boundary
Branch:     feature/gateway-privacy-boundary
Base:                22b9da73ce1a704426af3d0f36e5372b1e78ed85
Implementation tip:  7cbe190a835e5463004f09be8924dbcb1e6eb22b
Review diff:          22b9da7..7cbe190
```

The checkout HEAD may be `7cbe190` or a descendant whose only additional change is this review
prompt. First verify the branch, confirm `7cbe190` is an ancestor, inspect `7cbe190..HEAD`, and stop
with a scope error if that later range contains anything except
`docs/reviews/2026-08-02-privacy-boundary-service-adversarial-review-prompt.md`. Review **every
changed file** in `22b9da7..7cbe190`. Generated Grafana JSON may be checked against its generator
instead of manually line-reviewed, but it may not be ignored. Planning and documentation files
still matter where they claim behavior or define a release gate.

Ground every finding in the actual source, tests, and command output. Do not accept this prompt,
the design, implementation plan, phase summary, commit messages, comments, or test names as proof.
A passing test proves only the assertion it actually makes. For every finding provide a concrete
failure scenario: exact request, content, environment, byte sequence, route, interleaving, or
filesystem state leading to the wrong behavior.

This is a read-and-verify review. You may create a disposable `_test.go` probe when static
reasoning is insufficient, but remove it before finishing and prove the worktree returned to its
starting state. Do not edit production code or documentation. Do not use a real operator home or
live secrets for wrapper tests. Do not modify `workflow-studio`, `hermes-agent`, or any other
repository.

## Authoritative contract — read these first

Read these files completely, in order:

1. `CLAUDE.md`
2. `docs/superpowers/specs/2026-07-31-privacy-boundary-service-design.md`
3. `docs/superpowers/plans/2026-07-31-gateway-privacy-boundary-service.md`
4. `.planning/phases/21-privacy-boundary-service/21-01-PLAN.md`
5. `.planning/phases/21-privacy-boundary-service/21-01-SUMMARY.md`

The design specification is the product authority. The implementation plan defines the promised
TDD sequence and release evidence. The GSD files are tracking evidence, not independent proof.
If the implementation and design disagree, the design wins. If the summary says a gate passed,
rerun or inspect that gate rather than trusting the summary.

## Locked invariants to falsify

Build an explicit PASS / FAIL / UNPROVEN matrix for every invariant below:

1. `PIIRedactionHook` remains the registered compatibility name and standard PII behavior remains
   default-on and backward-compatible.
2. Strict privacy is selectable and may be the configured minimum. A request can raise but never
   lower the configured profile. Unknown or unavailable requests fail instead of silently falling
   back.
3. Compression runs before privacy, and privacy is the final inbound content mutation.
4. Strict input blocks before prompt/worker dispatch. Identify any earlier pool or session side
   effects and assess them against the design rather than silently treating them as dispatch.
5. Strict output is completely buffered and validated before headers or body bytes are released.
6. Credentials are classified by one shared policy, transformed one-way, never written to the
   reversible ledger, never exposed by triage, and never restored.
7. Only mappings whose originals came from caller input may restore originals. Generated values,
   unknown aliases, malformed tokens, and forged markers cannot obtain restoration authority.
8. Mappings are memory-only, scope-isolated, TTL- and capacity-bounded, parallel-safe, explicitly
   clearable, and never silently evict an active scope.
9. The same source value and entity are stable within a scope and unlinkable across scopes.
   Technical aliases retain the validity and relationships promised by the design.
10. Ollama, OpenAI, and Anthropic use one shared policy across all five routes while preserving
    native non-streaming, NDJSON, and SSE success and error formats.
11. Receipts are bounded and value-free. Strict success claims only `profile=strict`,
    `coverage=full`, and `result=pass` after both boundaries complete.
12. Ordinary logs, metrics, errors, receipts, strict traces, health, dashboards, captures, and
    support bundles expose neither mappings nor protected values.
13. Mapping inspection exists only through the disabled-by-default, authenticated, actual-peer
    loopback, no-CORS, no-store triage surface, and can be explicitly cleared.
14. POSIX and PowerShell secret management and privacy operations are behaviorally equivalent and
    never print secret values or expose them in avoidable process arguments.
15. Dashboard configuration remains read-only. Workflow parsing, minimization, routing, receipt
    enforcement, schema/tool execution, and final-artifact policy remain outside the Gateway.
16. Privacy classification does not introduce a Gateway-wide mutex, unbounded label cardinality,
    unbounded mapping growth, or a payload-driven superlinear denial of service.

Do not mark an invariant PASS because one unit test has a matching name. Trace it through the real
production call path and identify the test that would fail if the invariant regressed.

## Change map — verify, do not trust

The branch changes roughly 29,000 lines across 134 files. Important areas include:

| Area | Landmarks | Intended responsibility |
|---|---|---|
| Configuration and wiring | `internal/config`, `cmd/otto-gateway` | Parse/validate profiles, keys, capacities, triage posture; construct one shared service; preserve hook order |
| Privacy core | `internal/privacy` | Classification, actions, scoped ledgers, aliases, strict input/output, receipts, metrics callbacks, lifecycle and triage views |
| Compatibility facade | `internal/plugin/pii` | Preserve standard PII behavior and the `PIIRedactionHook` identity while delegating policy to the shared service |
| Hook and trace integration | `internal/plugin`, `internal/engine` | Context propagation, compression/privacy ordering, fail-closed dispatch, and strict trace safety |
| Public adapters | `internal/adapter/{ollama,openai,anthropic}` | Apply output privacy and render native wire/error shapes for five routes |
| Operations | `internal/admin`, `internal/metrics` | Protected triage, read-only posture, safe capture, bounded metrics, health and dashboard state |
| Install and CLI | `scripts/gw*`, `scripts/install*`, `scripts/lib/redact*` | Managed secrets, inspect/clear workflows, support/capture redaction, POSIX/PowerShell parity |
| Documentation/Grafana | `docs`, `scripts/gen_grafana_dashboard.py` | Operator/workflow contract, deterministic dashboard, safe observability guidance |
| Release gates | `tests/privacy`, `Makefile`, `.github/workflows/ci.yml` | Cross-surface conformance, leakage, lifecycle stress, race, benchmark and platform gates |
| Test-only release fixes | pool discovery test, Darwin tray cancellation test, ACP stderr regression test | Remove nondeterministic release-gate failures without weakening production assertions |

Use `git diff --name-status 22b9da7..7cbe190` as the authoritative inventory. Inspect correction
commits as well as the planned task commits; a late fix can invalidate an earlier assumption.

## Attack campaign 1 — configuration, precedence, and compatibility

Try to make configuration boot in a state that claims strict privacy but cannot enforce it:

- Exercise empty, duplicated, mixed-case, whitespace-padded, unknown, and contradictory values in
  `PRIVACY_DEFAULT_PROFILE` and `PRIVACY_REQUEST_PROFILES`. Can `strict` be the default while absent
  from the allowed set? Can a request spell a value that is normalized differently by config and
  request handling?
- Try zero, negative, overflow-scale, and internally inconsistent TTL/capacity values. Include
  duration parsing edge cases and totals smaller than per-scope capacity.
- Remove each required key independently. Enable triage without its token. Make strict available
  while removing the compatibility hook or disabling core PII work.
- Combine explicit `PII_ENTITY_ACTIONS` overrides with `PRIVACY_SECRET_ACTION` and
  `PRIVACY_TECHNICAL_ACTION`. Find any path that makes a credential reversible or assigns
  `pseudonymize` to an unsupported entity.
- Verify the compiled defaults, generated environment template, installers, docs, and admin
  posture agree, especially default-on PII/NER behavior and upgrade preservation.
- Compare standard-mode requests and responses against base `22b9da7`. Look for changed error
  timing, streaming, action precedence, hash/encrypt behavior, or hook registration even when no
  privacy headers are supplied.
- Try header duplication, comma-joined values, unusual casing, leading/trailing whitespace,
  non-ASCII bytes, oversized scope IDs, encoded separators, and conflicting profile/scope values.
  Confirm malformed metadata is rejected before worker dispatch.

Any request-driven downgrade below the configured minimum, bootable strict-but-unenforceable
configuration, or standard compatibility regression is at least **High** severity.

## Attack campaign 2 — credential classifier and span arbitration

Credentials are the highest-severity content category because they must be one-way everywhere.
Attack both detection and the promise that runtime, capture, and support redaction share policy:

- Test bearer/basic authorization, API keys, passwords, tokens, DSNs, credentials embedded in
  URLs, quoted/unquoted assignments, JSON/YAML/env syntax, query strings, fragments, punctuation,
  mixed line endings, very long values, adjacent assignments, and a secret at end-of-input.
- Try delimiter smuggling, Unicode lookalikes, percent encoding, escaped quotes/backslashes,
  nested URLs, multiple `@` characters, malformed-but-plausible URLs, and secret names embedded in
  larger identifiers. Distinguish a real high-confidence bypass from intentionally unsupported
  low-confidence prose.
- Create overlapping recognizers: credential inside URL, email inside credential, technical value
  inside a token, and equal-start/equal-length spans. Prove arbitration is deterministic and gives
  the required category precedence independent of recognizer order.
- Feed thousands of near-matches and multi-megabyte unquoted candidates. Look for rescanning,
  repeated slicing, regular-expression blowups, excessive allocation, or classifier-wide locking.
- For every credential case, inspect the ledger and triage response, then echo the synthetic label
  from the worker. The original must never be ledgered or restored.
- Compare Go classification with POSIX and PowerShell support/capture redaction using the shared
  corpus. A shell-only regex fork or silent fallback is a finding even if the current fixtures pass.

A raw credential reaching a worker under strict, entering the reversible ledger, appearing in an
ordinary artifact, or being restored is **Critical**.

## Attack campaign 3 — scoped mapping, provenance, lifecycle, and concurrency

Model exact interleavings rather than relying only on `-race`:

- Same scope/same original/same entity inserted concurrently; same original under different
  entities; different originals deriving the same candidate; and different scopes deriving under
  one shared service.
- Worker-generated value observed before the same value later arrives as caller input. Determine
  whether provenance promotion is deliberate, atomic, and incapable of granting restoration to a
  value that never actually came from the caller.
- Clear while inbound transforms, while the worker is running, during output validation, during
  restoration, and simultaneously with expiry/reaping. An acquired request may finish; new
  acquisition must fail; wipe must occur exactly when the last reference releases.
- Fill per-scope and global capacity exactly, one below, and one above under contention. Reclaim
  expired/closed scopes first, but never evict an active or closing scope to admit another.
- Reuse a just-cleared scope during and after the tombstone interval. Attempt ABA races between
  clear, expiry, recreation, inspection, and release.
- Cancel or panic on every path after acquisition. Prove references and reservations are released
  once, gauges converge, and raw request/response objects are not retained.
- Restart a real in-process server and prove no mapping survives. Search files, caches, logs, and
  serialized admin state for ledger material.
- Hammer `Inspect`, `Clear`, and `Reap` alongside at least 100 scopes and 500 live requests. Check
  exact counts, not only `<= capacity`, and verify eventual zero after clear/expiry.

The race detector cannot prove logical scope isolation. Demonstrate cross-scope restoration
attempts with known canaries. Wrong-original restoration, active-scope eviction, or a mapping
surviving process restart is **Critical**.

## Attack campaign 4 — technical pseudonyms and forged markers

For every supported technical type, verify validity, stability, scope unlinkability, and promised
relationships:

- IPv4/IPv6 boundary values, compressed IPv6, IPv4-mapped IPv6, `/0`, `/31`, `/32`, `/127`, `/128`,
  host-before-CIDR and CIDR-before-host discovery, overlapping subnets, and multiple source subnets.
- Confirm synthetic addresses stay inside documented safe pools and never collide with a different
  live mapping in the same scope. Test whether output-only technical values can steal a caller
  mapping.
- MAC local-admin/unicast bits; IMEI checksum; IMSI/MSISDN/telephone lengths and punctuation; SIP
  user/host structure; site/cell/device typed forms; coordinate boundaries, sign, precision, poles,
  antimeridian, and stable coordinate-pair relationships.
- Parse every emitted alias with an independent parser/checksum implementation rather than the
  generator's own helper.
- Forge every reserved token grammar, including valid-looking unknown tokens, wrong entity types,
  altered indices, nested markers, prefixes/suffixes, case changes, and split tokens across worker
  chunks. Unknown or malformed markers must block, not pass through or restore.
- Look for modulo bias only if it violates a locked validity, relationship, collision, or
  unlinkability property; do not report harmless key-independent distribution skew as a security
  defect without a concrete consequence.

## Attack campaign 5 — inbound enforcement and hook order

Trace the real path from HTTP decode to worker dispatch for all five routes:

```text
Ollama:    POST /api/chat, POST /api/generate
OpenAI:    POST /v1/chat/completions, POST /v1/completions
Anthropic: POST /v1/messages
```

- Put protected values in every canonical string location: system/user/assistant content,
  multipart content, tool names/descriptions/arguments/results, images or metadata represented as
  text, completion prompts, Anthropic blocks, and empty/nil/unknown variants.
- Send compressed requests through every supported route and encoding. Prove decompression occurs
  once before privacy and no later hook can mutate protected content after the independent residual
  scan.
- Reorder, omit, duplicate, or rename enabled hooks through configuration. Strict availability must
  refuse a chain that cannot preserve compression-before-privacy and privacy-final ordering while
  retaining `PIIRedactionHook` as the compatibility name.
- Force classifier, mapping, traversal, observer, and receipt panics/errors. Verify the existing
  hook boundary turns them into safe fail-closed errors rather than dispatching or falling back to
  standard.
- Instrument the fake worker, pool/session creation, and prompt call. A strict input block must
  produce zero worker-side effects, not merely zero returned chunks.
- Compare standard mode carefully: it is not supposed to acquire strict fail-closed semantics or
  lose existing action behavior.

## Attack campaign 6 — outbound validation and zero-byte release

Do not rely solely on `httptest.ResponseRecorder`; it can hide header commitment and flush timing.
Use a real `httptest.Server`, raw TCP client, or equivalent observation of first response bytes.

- Return a protected original, new credential, newly generated personal value, newly generated
  technical value, known caller alias, output-only alias, wrong-scope alias, unknown alias,
  malformed reserved token, and forged valid-looking token from the worker.
- Split protected values and tokens across arbitrary worker chunks, including every byte boundary.
  Validation must operate on the complete logical response, not individual chunks.
- Force output privacy failure after the adapter has enough information to choose a status. Confirm
  no success status, content type, receipt, SSE event, NDJSON line, or body byte was emitted first.
- Force client cancellation, idle timeout, worker error, post-hook error, and panic while strict
  output is buffered. Look for partial output, leaked buffers, double release, or a falsely passing
  receipt.
- Verify only known caller-input mappings restore. Restoration must occur after residual checks and
  must not make the restored original look like an upstream leak.
- Verify standard streaming remains live and compatible except for the pre-existing aggregation
  cases. Strict is intentionally buffered; do not flag the absence of incremental strict streaming.

Any strict response byte released before successful full validation is **Critical**.

## Attack campaign 7 — adapter parity and native wire contracts

Run the same fixtures through all five routes and compare policy outcomes while independently
validating each native protocol:

- Ollama JSON and NDJSON fields, line boundaries, `done`, error shape, and content type.
- OpenAI chat/completions and legacy completions JSON plus SSE `data:` frames, `[DONE]`, error
  envelope, finish reasons, and reroute/downgrade behavior.
- Anthropic Messages JSON and SSE event ordering, content-block indices, `message_delta`, stop
  reasons, tool-use/tool-result replay, and native error envelope.
- Empty output, multiple choices/content blocks, native tool calls, mixed text/tools, non-ASCII,
  maximum accepted payload, and a worker result that becomes unsafe only after aggregation.
- Verify adapters do not implement their own profile, classifier, ledger, or restoration policy.
  Any adapter-specific exception is an architecture and consistency risk.
- Compare route registration and middleware. Privacy headers and errors must be interpreted
  consistently even when a route takes a legacy/reroute path.

A policy discrepancy between surfaces or a malformed native response that breaks a conforming SDK
is at least **High**.

## Attack campaign 8 — receipts and bypass detection

- Decode receipts independently. Check base64url rules, JSON types, version, maximum size, safe
  counts, scope/profile accuracy, and absence of entity names, values, aliases, classifier details,
  errors, and key material.
- Exercise strict success, standard streaming, invalid request, input block, output block, capacity
  rejection, internal failure, client cancellation, and response-render failure. Determine exactly
  which paths emit a receipt and whether any claims `coverage=full` prematurely.
- Try duplicate receipt headers, folded/comma-joined values, stale receipts, replay across scopes,
  and a direct fake-worker response with no receipt. Confirm the consumer fixture rejects missing,
  malformed, non-strict, non-full, and non-pass receipts.
- A receipt is boundary evidence, not a signed attestation. Do not flag the lack of cryptographic
  signing; do flag a receipt that lies about the path actually completed.

## Attack campaign 9 — leakage through operational surfaces

Use unique high-entropy canaries for a personal original, technical original, credential, reversible
alias, one-way secret label, triage token, and both managed keys. Search structured values as well as
plain substrings.

Inspect and exercise:

- ordinary and error logs;
- strict and standard chat traces;
- metrics text and metric labels;
- health, hooks, dashboard, About, Docs, and snapshot responses;
- privacy receipts and native error envelopes;
- ACP capture and redacted capture;
- POSIX and PowerShell support bundles, including extracted compressed members;
- generated Grafana JSON, documentation examples, temporary files, and command output.

Attack support/capture handling with gzip rotations, invalid UTF-8, symlinks, ancestor swaps,
junctions where available, curl config injection, redirects, malicious filenames, oversized files,
and partial redactor failure. Scan the final tar/zip tree, not only source text. Verify support never
calls triage or includes ledger state.

Standard chat trace is intentionally opt-in and may retain raw prompts with warnings. That accepted
behavior is not a defect. Strict trace leakage, any ordinary-artifact leak, or any credential/key
leak is **Critical**.

## Attack campaign 10 — triage API and CLI confinement

- With triage disabled, prove routes are genuinely unregistered/404 and cannot be reached through
  alternate methods, encoded paths, trailing slashes, or method override headers.
- With triage enabled, test IPv4/IPv6 loopback and non-loopback actual peers. Spoof
  `X-Forwarded-For`, `Forwarded`, `X-Real-IP`, and `Host`; these must not grant access.
- Test missing, wrong, duplicated, whitespace-modified, and oversized bearer tokens. Inspect whether
  comparison timing or error text reveals useful token information.
- Check `Cache-Control: no-store`, no permissive CORS, safe content type, and native status on every
  success and error path—not only 200 responses.
- Attempt encoded-slash/path traversal and delimiter injection through scope IDs. Error and audit
  logs may include the bounded safe scope ID, but never mappings, protected values, tokens, or raw
  malformed paths containing secrets.
- Inspect while clear/expiry is racing. Clear-one must return 204 inactive or 202 active; clear-all
  must require the additional exact confirmation and must not become possible through redirect
  following or a CLI argument-parsing ambiguity.
- Prove CLI address validation rejects non-loopback destinations, redirects, DNS rebinding-like host
  forms, userinfo, and unexpected schemes. Ensure the triage token is not printed or passed in a
  command line visible to ordinary process inspection when a safer mechanism exists.

Unauthenticated or non-loopback mapping access is **Critical**.

## Attack campaign 11 — installers, secret rotation, and shell parity

Compare POSIX and PowerShell behavior rather than reviewing them independently:

- Cold init, upgrade from a pre-privacy install, re-init, auth-disabled install, missing overrides,
  duplicate keys, CRLF/LF, comments, empty values, paths containing spaces/metacharacters, and
  explicit rotation.
- Existing secrets and unrelated operator settings must survive ordinary upgrade. Explicit rotation
  must atomically replace all managed secrets, warn safely, preserve file permissions/ACL intent,
  and never silently enable auth or triage.
- Interrupt or race a writer and reader during replacement. A reader should observe old or new
  complete content, never a partial secret set. Check symlink/junction and ancestor-swap defenses.
- Run with shell tracing, verbose errors, failed HTTP requests, malformed JSON, and unauthorized
  responses. Search stdout/stderr, process arguments, temporary files, backups, and support output
  for full secret values.
- Compare status/scopes/inspect/clear/clear-all flags, exit codes, HTTP methods, headers, confirmation,
  loopback checks, redirects, JSON rendering, and error redaction across shells.

If a native Windows host is unavailable, mark Windows DACL/current-SID, continuous-reader, and
junction-swap checks **UNAVAILABLE**, not PASS. Portable PowerShell tests on macOS do not prove native
Windows filesystem security.

## Attack campaign 12 — metrics, admin UI, docs, and Grafana

- Enumerate every metric label source. Scope IDs, request/session/user identifiers, originals,
  aliases, token fragments, and raw errors must never become labels. Verify workload labels use the
  existing bounded limiter and every enum is actually closed over all error paths.
- Reconcile counters and gauges against exact lifecycle events under failures, retries, panic,
  clear, expiry, and capacity rejection. Look for double counts, missing terminal results, negative
  gauges, stale gauges, and gauges whose collection races with clear.
- Inspect the privacy page, dashboard, About, health, and snapshot as a hostile local user. They must
  expose posture and bounded counts only; no token, key, original, alias, or mutation control.
- Prove dashboard privacy controls are read-only at HTML, JavaScript, route, and API layers. Check
  keyboard access, focus order, labels, live-region behavior, contrast, narrow viewport, and reduced
  motion without letting accessibility work introduce a state-changing endpoint.
- Regenerate Grafana JSON and require byte equality. Inspect queries for unbounded groupings,
  divide-by-zero/empty-series behavior, incorrect rate windows, misleading percent calculations,
  stale metric names, and alerts that treat every correct privacy block as an outage.
- Cross-check README, install, operating, quickstart, privacy-boundary, release, dashboard, and
  generated env documentation against source defaults and actual CLI behavior. Flag claims that
  move workflow parsing, minimization, tools, schema validation, receipt enforcement, or final
  artifact policy into the Gateway.

## Attack campaign 13 — architecture, performance, and denial of service

- Use dependency inspection and architecture lint to prove `internal/privacy` remains a leaf with
  only its permitted canonical dependency, and that adapters do not gain policy-layer imports.
- Search for package-level locks and trace lock acquisition order across service, store, scope,
  metrics callbacks, clear, reap, and restoration. Construct a deadlock cycle if one exists.
- Audit every loop over payload bytes, spans, mappings, scopes, labels, and aliases for superlinear
  behavior or attacker-controlled unbounded work. Include malformed input with many near-matches and
  maximum-size strict payloads.
- Inspect benchmark ceilings. Are they based on meaningful three-run medians, or so generous that a
  severe regression passes? Do `-benchtime=1x`, caching, shared setup, or payload construction hide
  allocations and lock contention?
- Interrogate `TestPrivacyParallelClassificationNoGlobalLock`: confirm block profiling is enabled,
  the tested stack is the real production classifier, samples are positive, and the assertion would
  fail if a Gateway-wide classification mutex were introduced.
- Run 100 parallel scopes while triage, metrics collection, clear, and expiry operate. Look for lock
  convoys even when the race detector stays silent.

## Attack campaign 14 — test integrity, CI, and release-gate corrections

Assume the tests may be self-fulfilling:

- Does the cross-surface harness route through the real production server, middleware, hook chain,
  adapters, and service, or duplicate the expected policy in test code?
- Does the fake worker prove non-dispatch, or only return no output? Does the zero-byte strict test
  observe real header/body commitment? Do protocol tests parse frames structurally or search
  substrings?
- Do conformance cases cover all five routes, every canonical location, both streaming modes,
  compressed requests, native errors, tools, generated values, profile precedence, and exact scope
  restoration—or do table names exaggerate coverage?
- Do leakage tests scan actual live handler responses and an extracted real support archive? Can
  canary choice produce false confidence because it is transformed before reaching the surface that
  should be tested?
- Do lifecycle tests assert exact counts and no active eviction under real contention, or only loose
  upper bounds after serialized operations?
- Inspect GitHub Actions matrices, OS/shell selection, caching, build tags, path filters, and command
  chaining. Ensure focused privacy jobs are additive and cannot silently replace the full existing
  race, lint, architecture, vulnerability, or cross-build gates.
- Review `68ba19e`: does the pool warm-probe barrier preserve the lazy self-heal race and exact
  probe-count assertion, or merely serialize away the bug?
- Review `77dd280`: only Darwin child **startup** may wait 10 seconds; cancellation and child-reaping
  assertions must remain independently bounded at 3 seconds.
- Review `565d2ff`: deferred ACP `Close` must guarantee cleanup on fatal/timeout without hiding the
  normal close error assertion or double-waiting unsafely. Confirm `Client.Close` is genuinely
  idempotent.
- Search for skipped tests, environment-dependent early returns, broad regex filters, cached results,
  and tests that pass without executing their intended assertion.

A green CI job that does not execute the promised platform or privacy behavior is a release defect.

## Mandatory verification

Run from the implementation worktree. Record exact commands, exit codes, unavailable tools, skips,
and whether results were cached. Do not silently substitute a narrower command.

```bash
git status --short
git diff --check 22b9da7..7cbe190
git diff --name-status 22b9da7..7cbe190
make fmt-check
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
privacy_review_lint_cache="$(mktemp -d)"
PATH="$(go env GOPATH)/bin:$PATH" GOLANGCI_LINT_CACHE="$privacy_review_lint_cache" make lint
make arch-lint
make test-privacy
make test-privacy-race
python3 scripts/test_gen_grafana_dashboard.py
python3 scripts/test_privacy_docs.py
node --test internal/admin/admin_js_test.js
bash tests/install/privacy_secrets_posix_test.sh
bash tests/scripts/test-privacy-cli.sh
bash tests/scripts/test-support-redact.sh
bash tests/scripts/test-support-bundle.sh
"$(go env GOPATH)/bin/govulncheck" ./...
PATH="$(go env GOPATH)/bin:$PATH" GOLANGCI_LINT_CACHE="$privacy_review_lint_cache" make ci
```

When `pwsh` is available, also run:

```powershell
pwsh -NoProfile -File tests/install/privacy_secrets_windows_test.ps1
pwsh -NoProfile -File tests/scripts/test-privacy-cli.ps1
pwsh -NoProfile -File tests/scripts/test-support-redact.ps1
pwsh -NoProfile -File tests/scripts/test-support-bundle.ps1
```

Run the benchmark contract with three samples, not only the one-iteration Make target:

```bash
go test ./internal/privacy -run '^$' \
  -bench 'Privacy(Standard|Strict|Parallel)' -benchmem -count=3
```

Build cgo-free binaries into a new temporary directory and record hashes. Do not delete or overwrite
user files:

```bash
privacy_review_build_dir="$(mktemp -d)"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$privacy_review_build_dir/otto-gateway-linux-amd64" ./cmd/otto-gateway
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o "$privacy_review_build_dir/otto-gateway-darwin-arm64" ./cmd/otto-gateway
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o "$privacy_review_build_dir/otto-gateway-darwin-amd64" ./cmd/otto-gateway
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$privacy_review_build_dir/otto-gateway-windows-amd64.exe" ./cmd/otto-gateway
shasum -a 256 "$privacy_review_build_dir"/*
```

Do not run live wrapper or Gateway smoke tests against the operator's real home/configuration. Use
isolated test-owned install, state, worker, and output directories, or report the live check as
unavailable. Never use real credentials or an external model when a deterministic fake worker can
exercise the boundary.

If you can start a deterministic Gateway and fake worker using only isolated test-owned paths, also
run `bash scripts/test-pii.sh` and `pwsh -NoProfile -File scripts/test-pii.ps1` against that instance.
Otherwise mark those two live surface checks unavailable and explain which isolation prerequisite
was missing.

## Severity

- **Critical** — raw protected data or credentials cross a strict boundary; credentials become
  reversible; wrong-scope/wrong-original restoration; strict output releases any byte before full
  validation; unauthenticated/non-loopback mapping access; mapping or key leakage; active mapping
  state survives restart; exploitable cross-scope race.
- **High** — profile downgrade or bootable unenforceable strict posture; active-scope eviction;
  adapter policy divergence; false full/pass receipt; support/capture leak; native wire corruption;
  capacity race; deadlock; practical payload-driven denial of service; CI silently omits a promised
  security/platform gate.
- **Medium** — bounded telemetry is materially wrong; POSIX/PowerShell behavioral mismatch without
  direct secret exposure; read-only/admin/docs drift that could cause unsafe operation; meaningful
  performance regression below denial-of-service severity.
- **Low** — weak or misleading test, minor documentation ambiguity, inaccessible presentation, or
  maintainability defect without a demonstrated privacy/security consequence.

Do not inflate severity. A theoretical concern without a reachable input and wrong outcome is not a
finding. Conversely, do not downgrade a leak because the triggering input is unusual.

## Explicitly out of scope

Do not flag these locked decisions as missing features:

- Receipt signing or remote attestation. Receipts detect expected local-boundary traversal; they do
  not defend against a malicious replacement server.
- An OS enclave or protection from a process with equivalent local memory-inspection privileges.
- Incremental strict streaming. Full buffering is required so output can fail before any byte.
- Generic document/archive/spreadsheet parsing, workflow minimization, routing enforcement, tool
  execution, schema validation, or final-artifact policy inside the Gateway.
- A writable dashboard. Privacy posture is intentionally read-only; clearing is an authenticated CLI
  and triage operation.
- Persistence of mappings across restart. Restart loss is required.
- Adapter-specific privacy policy or a Gateway-wide classification mutex.
- Raw standard-mode chat-trace suppression. Standard trace retains its pre-existing explicit opt-in
  sensitive behavior and warnings; strict trace is the newly protected path.

Native Windows-only assertions may be unavailable on macOS. That is an evidence gap to record, not
by itself a code defect. Do not claim those properties PASS without a native Windows run.

## Deliverable

Produce one self-contained adversarial review with this exact structure:

1. **Scope verification** — branch, implementation tip ancestry, exact review diff, later prompt-only
   delta, starting worktree status, files reviewed, tests or tools unavailable.
2. **Verdict** — `SHIP`, `SHIP WITH FOLLOW-UPS`, or `DON'T SHIP`, with the single most important
   reason.
3. **Findings table**, sorted by severity: `file:line | severity | invariant violated | defect |
   concrete failure scenario | minimal fix`.
4. **Top-five reproductions** — exact request bytes, worker chunks, shell inputs, filesystem state,
   or goroutine interleavings and the wrong observable result. Include runnable disposable tests
   where practical.
5. **Invariant matrix** — all 16 locked invariants marked PASS / FAIL / UNPROVEN with source and
   test evidence. No omitted rows.
6. **Test-integrity assessment** — the highest-risk path the existing suite does not truly prove,
   plus verdicts on the conformance, leakage, lifecycle, block-profile, benchmark, CI, and three
   test-flake corrections.
7. **What I verified safe and why** — concrete reasoning for areas with no finding, especially
   credential one-way behavior, provenance-controlled restoration, zero-byte strict output,
   cross-scope lifecycle, native wire formats, and triage confinement.
8. **Verification evidence** — exact commands and results, including race output, skips, benchmark
   values, vulnerability result, cross-build hashes, and final worktree status.
9. **Required remediation before ship** — ordered, minimal fixes for every Critical/High finding and
   the focused regression test each fix needs.

Do not stop at the first bug. Do not spend the report praising architecture. Do not report a concern
without tracing it to a reachable wrong outcome. Be specific or be silent.
