# Adversarial code review — OTTO Gateway Privacy Boundary Service

**Reviewer role:** hostile senior Go / application-security reviewer (did not implement the feature)
**Date:** 2026-08-02
**Review type:** read-and-verify, evidence-driven, whole-branch

---

## 1. Scope verification

| Item | Expected | Observed | Result |
|---|---|---|---|
| Worktree | `.worktrees/gateway-privacy-boundary` | same | OK |
| Branch | `feature/gateway-privacy-boundary` | `feature/gateway-privacy-boundary` | OK |
| Implementation tip | `7cbe190` | `7cbe190a835e5463004f09be8924dbcb1e6eb22b` is an ancestor of HEAD (`git merge-base --is-ancestor` → YES) | OK |
| Checkout HEAD | `7cbe190` or a prompt-only descendant | `0ccffbcf667fb21a15370e7c4237a429558e98f1` | OK |
| `7cbe190..HEAD` delta | prompt file only | `A docs/reviews/2026-08-02-privacy-boundary-service-adversarial-review-prompt.md` (single file) | OK |
| Review diff | `22b9da7..7cbe190` | 134 files, +28937/−1994 | reviewed |
| Starting worktree | clean | `git status --porcelain` empty | OK |
| `git diff --check 22b9da7..7cbe190` | clean | no whitespace/conflict errors | OK |

**Files reviewed:** every changed file in `22b9da7..7cbe190` was inspected via `git diff --name-status` (authoritative inventory). The `internal/privacy` core (`service.go`, `store.go`, `technical.go`, `secrets.go`, `classifier.go`, `actions.go`, `context.go`, `walk.go`, `receipt.go`, `triage.go`, `types.go`, `errors.go`) was read in full. Config/wiring, the three adapters, engine, metrics, admin/triage, installers/CLI/redaction shells, Grafana generator, and documentation were reviewed by focused inspection plus five specialised sub-reviews. Generated Grafana JSON was verified against its generator (byte-equal), not hand-reviewed.

**Tools available:** Go 1.26.5, golangci-lint 2.12.2, go-arch-lint, govulncheck, python3, node, pwsh 7.6.4 (macOS). **Unavailable:** a native Windows host (see §9).

**Later-range guard:** `7cbe190..HEAD` contains only the review-prompt document, so no additional implementation code is in scope. Scope check passed; the review proceeded.

---

## 2. Verdict

### SHIP WITH FOLLOW-UPS

**Single most important reason:** every *locked privacy invariant* holds under adversarial probing — credentials are one-way, restoration is provenance-gated to caller input, strict output releases zero bytes before full validation, scopes are isolated/bounded/memory-only, triage is loopback+token+no-store confined, and no operational surface leaks mappings. There is **no Critical and no High finding.** The one item that keeps this from an unconditional SHIP is **M1 (Medium):** on a deliberately weakened gateway (PII redaction disabled *and* `strict` removed from `PRIVACY_REQUEST_PROFILES`), a request carrying `X-GW-Privacy-Profile: strict` is silently ignored instead of returning the spec-mandated `400 privacy_profile_unavailable`. The practical blast radius is contained (it requires an operator to have explicitly turned protection off, and the mandatory workflow-side receipt-absence check still fails closed), so it is a contract-conformance gap to fix before or shortly after ship, not a release blocker.

---

## 3. Findings (severity-sorted)

No Critical. No High.

| # | Sev | File:line | Invariant | Defect | Concrete failure scenario | Minimal fix |
|---|---|---|---|---|---|---|
| M1 | Medium | `internal/privacy/service.go:308` | 2 (unavailable requests must fail, not silently fall back) | `Service.Before` returns `nil,nil` on `!s.config.PIIEnabled` **before** `resolveProfile` (line 312). The requested-profile header is never examined. | Boot with `PII_REDACTION_ENABLED=false` + `PRIVACY_REQUEST_PROFILES=standard` (a valid boot; config only ties PII-enabled to strict *availability*). Send `POST /v1/messages` with `X-GW-Privacy-Profile: strict`. Response: 200, raw content forwarded to worker, **no** `400 privacy_profile_unavailable` (spec §6.3/§14) and **no** receipt. Same on `ENABLED_HOOKS` that drops `PIIRedactionHook` (legal only when strict is unavailable). | Resolve/reject the requested profile before the `PIIEnabled` short-circuit; optionally reject a non-empty profile header adapter-side when the hook is absent. |
| L1 | Low | `internal/privacy/service.go:1098-1102`, `1675-1700` | 11 | Standard aggregated (non-stream) responses stamp `coverage="full"`, but §8.2 defines `full` as *strict* input+output enforcement. | A naive workflow checking only `coverage=full && result=pass` (ignoring `profile`) accepts a standard response as if strictly enforced. Not exploitable against a spec-conformant consumer (the §8.2 gate also requires `profile=strict`, which a standard receipt fails). | Use a distinct coverage value for standard aggregation (e.g. `standard` or keep `input`). |
| L2 | Low | `internal/config/config.go:1454-1464` vs `internal/plugin/pii/ner.go:294-300` | 9/15 | `USPhone` is excluded from `privacyTechnicalPIIEntities` (so `PII_ENTITY_ACTIONS=USPhone:pseudonymize` is refused at boot), yet runtime `categoryForEntity` classifies `USPhone` as `CategoryTechnical`, so `strictAction` applies `PRIVACY_TECHNICAL_ACTION` (default `pseudonymize`) by default. | Under strict, US phone numbers receive reversible scoped aliases + ledger entries by default — the exact action config tells operators is unsupported for that entity. Posture display and effective behavior disagree. (Reversible-within-scope for a telephone identifier matches the design's "telephone" technical treatment, so no leak — a list-consistency defect.) | Make the two hand-coded lists agree: either add `USPhone` to `privacyTechnicalPIIEntities` or drop it from `categoryForEntity`'s technical arm; update the pinned test. |
| L3 | Low | `internal/privacy/service.go:1046-1067`, `1130-1141` | 11 | `setStrictReceipt` always sets `Coverage:"full"`, including the recovered-panic and nil-response paths where outbound validation did not complete. | A strict internal-error receipt reads `coverage=full, result=error`. `result!=pass` prevents acceptance, so it is not exploitable, but the coverage claim is untrue. | Emit `coverage=input`/`unresolved` when outbound validation did not run. |
| L4 | Low | `internal/privacy/receipt.go:28` | 11 | Receipt is unpadded `base64.RawURLEncoding`; spec §8.2 says only "base64url". | A consumer using standard padded base64url decode fails to decode a valid receipt. | Document "unpadded base64url", or accept both padded and unpadded. |
| L5 | Low | `internal/privacy/service.go:1363-1368` (`bareEncryptedPayloadRE`) | 5 (over-broad, fail-closed) | Strict outbound residual blocks any `[A-Za-z0-9_-]{38,}` run that is not an authorized token, to catch bare encrypted payloads. | A strict response legitimately containing a 40-char git SHA, a JWT segment, or a ≥38-char base64 blob is blocked with `502 privacy_output_blocked`. Fail-closed (no leak); likely intended conservatism for the high-assurance path, but it can reject benign long opaque strings. | If false positives matter, tighten the bare-payload heuristic (e.g. require the value to actually AES-GCM-decrypt under a scope key) or document the constraint. |
| L6 | Low | `internal/admin/privacy.go:271-275` | 12/13 (telemetry accuracy) | Triage denial happens before chi routing, so `chi.URLParam(r,"scope-id")` is empty in `privacyTriageOperation`; a denied `DELETE …/scopes/run-1` is audited/counted as `operation="clear_all"`, a denied `GET …/scopes/run-1` as `"list"`. | `gw_privacy_triage_requests_total{operation="clear_all",result="denied"}` inflates, skewing the §18.3 "repeated denied triage" per-operation signal. No security impact (no value leaks). | Distinguish DELETE by path shape (suffix beyond `/scopes`) rather than the not-yet-populated URL param. |
| L7 | Low | `internal/privacy/service.go:363-365` | 15 (posture accuracy) | `lastErrorCode` has one write site (`recoverInboundPanic`); ordinary `privacy_input_blocked`, `privacy_capacity_exceeded`, `privacy_profile_unavailable`, etc. never populate it. | The dashboard / `/admin/privacy` "last bounded privacy error code" tile reads "none" through real enforcement failures, under-delivering §17.1. | Set `lastErrorCode` from the bounded `*Error.Code` in `observeRequestFailure`. |
| L8 | Low | `internal/privacy/actions.go:51-72` | 15 (posture accuracy) | Standard path has no `ActionPseudonymize` case; a valid `PII_ENTITY_ACTIONS=IPv4:pseudonymize` degrades to a replace token via the `default` branch with a per-occurrence `privacy.action.unknown` WARN. | On a standard request the effective action differs from the configured/displayed one, and log volume scales with PII hits. Fail direction is safe (one-way). | Add an explicit `ActionPseudonymize` case in the standard path (deliberate replace, no warn) or display the standard-mode downgrade. |

**Info-level (no reachable wrong outcome; recorded for maintainers):** whitespace-only `PRIVACY_REQUEST_PROFILES` reverts to the default incl. `strict` (more-protective) — `config.go:1648-1665`; `privacy.Observers.Triage` is declared+wired but never emitted (dead second path; no double count today) — `types.go:59`, `main.go:468`; ten privacy `GaugeFunc`s each take a full `Service.Snapshot()` per scrape (cross-gauge skew/efficiency) — `metrics.go:488-501`; `workload` uses a separate `skillLimiter` instance from `gw_llm_requests_total{skill}` (cross-metric join divergence under cap pressure); no `X-Content-Type-Options: nosniff` on admin JSON; Ollama streaming `Run`-failure echoes `err.Error()` to the client (`ollama/handlers.go:357,691`) — privacy errors are intercepted first and `privacy.Error.Error()` excludes `Cause`, so no privacy detail leaks, but it is inconsistent with the generic-message discipline elsewhere.

---

## 4. Top-five reproductions

### R1 — M1: strict header silently ignored on a PII-disabled gateway
- **Config:** `PII_REDACTION_ENABLED=false`, `PRIVACY_REQUEST_PROFILES=standard`, `PRIVACY_DEFAULT_PROFILE=standard` (boots — config validation only requires PII enabled when *strict* is available, `config.go:1391-1397`).
- **Request:** `POST /v1/messages` (or any of the five routes), header `X-GW-Privacy-Profile: strict`, body containing an API key.
- **Path:** `Service.Before` hits `if s == nil || !s.config.PIIEnabled || req == nil { return nil, nil }` (`service.go:308`) and returns before `resolveProfile` (line 312).
- **Wrong observable:** 200 response, raw content dispatched to the worker, **no** `400 privacy_profile_unavailable`, **no** `X-GW-Privacy-Receipt`. Spec §6.3 ("request error, not a silent fallback") and §21 ("fails loudly") are not met server-side. Backstop: a spec-conformant workflow rejects the receipt-less response (§8.2), so it fails closed downstream.

### R2 — Credentials are one-way (verified by disposable probe, then removed)
A temporary `internal/privacy` probe drove the real `Service.Before`/`After` with the production `SecretClassifier` over five credential shapes (bearer header, `sk-proj-…`, `ghp_…`, `postgres://admin:pw@host`, `api_key: …`). For every case: (a) the high-entropy tail was **absent** from worker-bound content after `Before`; (b) `store.Inspect(scope)` contained **no** entry whose `Original`/`Synthetic` held the secret; (c) echoing the emitted `[SECRET:…]` labels back through `After` did **not** restore the secret. All passed. Root cause in source: `strictAction` returns `SecretAction` for `CategorySecret` **before** consulting `PIIEntityActions` (`service.go:977-987`), `SecretAction` is validated to `replace|drop` only (`config.go:1367-1371`), and `OneWaySecretLabel` is a keyed HMAC that never stores the canonical (`secrets.go:182-200`).

### R3 — Forged reserved markers block strict output (disposable probe)
Worker responses containing `[SECRET:API_KEY_ABCDEF123456]`, `[Email_1]`, `[PII:Email:Zm9yZ2Vk…]`, and `[IPv4_7]` — none authorized during inbound — each produced `502 privacy_output_blocked` (`Stage:"output"`). Source: `prepareStrictOutbound`/`verifyStrictOutboundResidual` reject any reserved token that is not `state.tokenAuthorized` (`service.go:1226-1231, 1347-1351`), and `verifyStrictOutboundIntegrity` re-checks (`service.go:1568-1573`).

### R4 — Output-only / cross-scope reserved alias blocks
A worker response `"the server is 198.18.5.9 apparently"` — a value in the reserved synthetic IPv4 pool not mapped in this scope — produced `502 privacy_output_blocked`. Source: `verifyStrictOutboundIntegrity` scans `198.18.0.0/15` addresses and blocks any that is neither `restored` nor a `ProvenanceGenerated` synthetic in *this* scope (`service.go:1575-1586`). This is the structural cross-scope guard: `ResolveSynthetic` only queries the lease's own `scopeState`, so scope B cannot restore scope A's mapping.

### R5 — Zero-byte strict release (real-server test, cross-checked in source)
`tests/privacy/privacy_boundary_test.go:449` (`TestConformanceStrictStreamBlocksBeforeAnyPartialBody`) drives all five routes over a real `httptest.NewServer` and asserts a 502 with **zero** `data:`/`event:`/`"done":true` bytes on an output block, plus a `full/block` receipt. Source ordering confirms it: `beforeStrict` forces `req.Stream=false` (`service.go:557-558`); each adapter re-routes the non-stream strict path through `CollectFromRun`/`RunPostHooks` (which runs privacy `After`) **before** any `WriteHeader` (`ollama/ndjson.go:785`, `openai/sse.go:880`, `anthropic/sse.go:1273`); `After` copies the transformed clone back (`*resp = *working`) only on the pass path (`service.go:1079`).

---

## 5. Invariant matrix (all 16)

| # | Invariant | Result | Evidence |
|---|---|---|---|
| 1 | `PIIRedactionHook` remains the registered compatibility name; standard PII default-on & backward-compatible | **PASS** | `pii.go:41` `Name()="PIIRedactionHook"`; delegates Pre/Post to the shared service (`pii.go:53-88`); `main.go:570` refuses boot if strict-available and the hook is filtered out; standard token formats byte-identical to base (`actions.go:36-80`); `PII_NER_ENABLED` default flipped to `true` per §7.3 (`config.go:752-755`, `TestLoad_PIINEREnabled_Default`). |
| 2 | Strict selectable & configurable minimum; raise-not-lower; unknown/unavailable fails | **PASS\*** | No-downgrade holds: `resolveProfile` returns strict when default or requested is strict, never lowers (`service.go:498-513`); unknown/mixed-case/padded requested profiles → `400 privacy_profile_unavailable`. **\*Exception M1:** on a PII-disabled gateway an unavailable strict request is silently ignored rather than failing (see §3, §4-R1). |
| 3 | Compression before privacy; privacy is final inbound content mutation | **PASS** | Production chain `…→ compress → pii → logging` (`main.go:503-518`), ChatTrace prepended at index 0 (metadata-only), Logging metadata-only; inverts base order per §5.3; `chain.Filter` preserves order and cannot reorder; `TestHookOrderPrivacyBoundary`. |
| 4 | Strict input blocks before prompt/worker dispatch; no earlier pool/session side effect | **PASS** | Engine returns on prehook error at `engine.go:222` **before** `NewSession` (249) and `Prompt` (271); comment + `Run.SessionID`==="" confirm "no ACP session opened". Scope acquisition before block is a designed in-memory privacy step (§12), not worker dispatch. |
| 5 | Strict output completely buffered & validated before any header/body byte | **PASS** | §4-R5; `beforeStrict` forces non-stream; all five adapters validate via `CollectFromRun`/`RunPostHooks` before `WriteHeader`; forced output failure → 502 before any success byte; no scope-lease leak on `Run`-failure paths (`runErrCleanup` drives `After`'s `releaseLease`). |
| 6 | Credentials classified by one shared policy, one-way, never ledgered/exposed/restored | **PASS** | §4-R2 probe; `strictAction` category-first (`service.go:977-987`); `SecretAction∈{replace,drop}` (`config.go:1367-1371`); secrets minted only as `CategorySecret` (`secrets.go:328`); `OneWaySecretLabel` keyed HMAC, no canonical stored (`secrets.go:182-200`); triage `Inspect` returns only `forward` ledger, which never holds secrets. Shell/capture redaction delegates to the same Go classifier (`redact.sh` `redact_stream`; test asserts shared corpus, `test-support-redact.sh:124`). |
| 7 | Only caller-input mappings restore; generated/unknown/malformed/forged cannot | **PASS** | §4-R3/R4; `restoreStrictOutbound` restores technical only when `entry.Provenance==ProvenanceInput && entry.Synthetic==matched` (`service.go:1469-1490`) and encrypted tokens only when `state.tokenAuthorized` (1421-1463); provenance promotion is generated→input only when the value truly arrives as caller input (`store.go:744-758`). |
| 8 | Mappings memory-only, scope-isolated, TTL/capacity-bounded, parallel-safe, clearable, no active eviction | **PASS** | `store.go`: no persistence; atomic `reserveEntry` CAS vs `MaxTotalEntries` (861-872); per-scope check under scope lock; `reapAvailableExpiredLocked` evicts only `inFlight==0 && (closing||expired)` (892-906) — never an active scope; clear marks closing, wipes at last release (`Release`/`finalizeClosed`); tombstones reject reuse; `go test -race` green; `store_race_test.go`, lifecycle tests. |
| 9 | Same value/entity stable in scope, unlinkable across scopes; technical validity/relationships | **PASS** | Alias derivation HMAC includes `scopeID` (`technical.go:593-621`, `secrets.go:182-200`); ledger keyed per scope. Technical validity: IPv4→`198.18.0.0/15` (RFC 2544), IPv6→`2001:db8::/32` (RFC 3849), MAC locally-administered unicast, Luhn-valid IMEI, subnet relationships preserved (`preserveIPHostOffset`); L2 is a config/runtime list mismatch, not a validity break. |
| 10 | Ollama/OpenAI/Anthropic share one policy across five routes; native non-stream/NDJSON/SSE success & error preserved | **PASS** | Adapters contain zero classifier/ledger/restoration logic — only `StampHTTPContext`/`SetReceiptHeader`/`ErrorInfo`/replay helpers; one shared `privacy.Service` via one hook across pool + per-session engines (`main.go:480,751,820`); native NDJSON `done`, SSE `[DONE]`, Anthropic event ordering/indices, and native error envelopes verified; byte-exact envelope tests pass. |
| 11 | Receipts bounded & value-free; strict success claims `strict`/`full`/`pass` only after both boundaries | **PASS** | `receipt.go` fixed 8 fields, 512-byte cap, no values/entities/aliases; strict `full/pass` stamped only after prepare→verify→restore→integrity all succeed (`service.go:1073-1078`); standard stream emits `input`-only; block/error coverage correct. Low notes L1/L3/L4. Consumer fixture rejects missing/non-full receipts (`TestConformanceStrictReceiptConsumer`). |
| 12 | Logs/metrics/errors/receipts/traces/health/dashboards/captures/support expose no mappings/values | **PASS** | Metric labels all closed enums via `privacyLabel()`→`"other"`, `workload` capped 64; audit logs carry only op/result/peer/`scope_hmac`; strict trace summary-only (`AllowSensitiveTrace` false for strict); leakage test scans real handler bodies, `/metrics`, and extracted support archives with high-entropy canaries; support delegates to shared classifier and never calls triage. |
| 13 | Mapping inspection only via disabled-by-default, authed, actual-peer loopback, no-CORS, no-store, clearable triage | **PASS** | Routes registered only inside `if PrivacyTriageEnabled` (404 otherwise, tested); loopback checked on **actual** `RemoteAddr` (`privacy.go:112-122`), XFF/Forwarded/X-Real-IP/Host untrusted (forged-header 403 test); bearer compared via SHA-256 + `subtle.ConstantTimeCompare`; `Cache-Control: no-store` first; no CORS anywhere; encoded-slash/method-override bypasses fail; clear-one 204/202, clear-all requires exact `X-GW-Privacy-Confirm: clear-all`. |
| 14 | POSIX & PowerShell secret/privacy ops behaviorally equivalent; never print secrets or expose them in avoidable argv | **PASS** (native-Windows checks UNAVAILABLE, §9) | POSIX passes the triage token to `curl` only via `--config -` on stdin, never argv (`gw:1910-1928`); PowerShell passes it via an in-process `-Headers` hashtable to `Invoke-RestMethod`, never argv (`gw.ps1:1715-1722`); both use no-redirect (`--config` w/o `-L`; `-MaximumRedirection 0`) and case-sensitive `--all --yes`; upgrade preserves existing `PRIVACY_ALIAS_KEY`/`PRIVACY_TRIAGE_TOKEN` unless placeholder; all POSIX and portable-pwsh suites EXIT 0. |
| 15 | Dashboard read-only; parsing/minimization/routing/receipt-enforcement/schema/tool/final-artifact stay outside the Gateway | **PASS** | Privacy pages/cards contain zero forms/buttons; JS `textContent`-only; the sole admin POST is pre-existing ACP-capture; snapshot exposes posture + presence booleans (no key/token/value); `docs/privacy-boundary.md:11-22` keeps workflow responsibilities out of the Gateway per §20; `test_privacy_docs.py` passes. L7/L8 are posture-accuracy Lows. |
| 16 | No Gateway-wide mutex, unbounded label cardinality, unbounded mapping growth, or payload-driven superlinear DoS | **PASS** | `internal/privacy` is a dependency leaf (only imports `internal/canonical`); arch-lint clean; no package-level locks; `TestPrivacyParallelClassificationNoGlobalLock` runs 100 concurrent strict requests with block-profiling on and asserts exactly 100 classifiers run concurrently + positive barrier samples (a global mutex would deadlock the barrier → fail); benchmarks show linear throughput across 1KiB→64KiB→4MiB with bounded allocations; capacity/cardinality bounded by config. |

**\*** Invariant 2's core "raise-not-lower" property is fully intact; the asterisk marks the M1 sub-clause gap ("unavailable requests fail instead of silently falling back") that is only reachable on an explicitly PII-disabled gateway.

---

## 6. Test-integrity assessment

- **Highest-risk path the suite does *not* prove by itself:** the adapter-package privacy tests use `httptest.NewRecorder`, which cannot detect a header committed *after* the first body byte — the exact failure mode of the zero-byte invariant. This is compensated: `tests/privacy` runs a **real** `httptest.NewServer` across all five routes and asserts no partial body on block (`privacy_boundary_test.go:449`). Without that compensating test the zero-byte property would be UNPROVEN; with it, PASS.
- **Conformance harness:** genuine end-to-end — builds a real chi router, registers the real Ollama/OpenAI/Anthropic adapters, and wires a real `engine.New` with the production `pii.PIIRedactionHook` as pre+post over `httptest.NewServer` (`harness_test.go:128-155`). It does **not** duplicate policy in test code.
- **Leakage:** scans live handler response bodies, the `/metrics` text, and **extracted** real `tar`/`gzip` support archives using high-entropy canaries; asserts the technical canary *is* in the ledger while personal is not — real, not substring-only theater.
- **Non-dispatch:** proven structurally (engine short-circuit before `NewSession`/`Prompt`) rather than "no output returned".
- **Lifecycle:** asserts exact counts and no active eviction under real contention (`store_race_test.go`, lifecycle tests), not just `<= capacity`.
- **Block-profile (`TestPrivacyParallelClassificationNoGlobalLock`):** block profiling enabled (`SetBlockProfileRate(1)`), tested stack is the real `Service.Before`, asserts positive new barrier samples and exactly-100 concurrency — it *would* fail if a Gateway-wide classification mutex were introduced.
- **Benchmarks:** three-sample medians; strict inbound/outbound and standard measured at 1KiB/64KiB/4MiB — throughput is linear (no superlinear ceiling hiding a regression); allocations bounded (hundreds, not millions).
- **CI additivity:** the new `privacy-posix` (ubuntu+macos) job and the extended Windows job are **additive**; the core `lint-test-arch` (lint + test-race + arch-lint + govulncheck), `lint-darwin`, `cross-compile-smoke`, and `publish-dry-run` jobs are all preserved — no existing security/platform gate was replaced.
- **Three flake corrections:**
  - `68ba19e` (pool): **strengthens** — replaces a loose `len==0` check with an exact `warmup NewSession==1` assertion and adds a deterministic single-probe barrier proving exactly one singleflight re-probe; the lazy self-heal race is preserved, not serialized away.
  - `77dd280` (tray): only the Darwin **startup** PID-file poll went 3s→10s; the cancellation assertion (`t.Fatal("wrapper root did not stop after cancellation")`) remains bounded at **3s**.
  - `565d2ff` (ACP): adds a `defer c.Close()` for guaranteed teardown while preserving the explicit normal-close error assertion; safe because `Client.Close` is idempotent via `sync.Once` (`client.go:303`, D-07). The 3s→10s change is a liveness bound on an intentionally CPU-heavy fake's exit, not a correctness assertion.
- **No skipped tests / environment early-returns** were observed in the full or race runs (`grep SKIP` empty).

---

## 7. What I verified safe and why

- **Credential one-way:** category resolves to `SecretAction` before any per-entity override (`strictAction`), `SecretAction` is config-restricted to `replace|drop`, no secret entity name exists in `piiAllowedEntities` (so an override cannot even name one), and the one-way label is a keyed HMAC that never stores the canonical. The disposable probe (§4-R2) empirically confirmed no leak, no ledger entry, no restoration across five credential shapes.
- **Provenance-controlled restoration:** restoration is gated on `Provenance==ProvenanceInput` for technical values and on per-request `tokenAuthorized` for encrypted tokens; promotion is one-directional (generated→input only when the value genuinely arrives as caller input). Forged/unknown/output-only values are rejected (§4-R3/R4).
- **Zero-byte strict output:** buffering is forced at the service (`Stream=false`) and every adapter validates through the post-hook chain before `WriteHeader`; the transformed clone is only swapped in on the pass path. Verified by a real-server test, not a recorder.
- **Cross-scope lifecycle:** scopes are separate `scopeState` maps keyed by scope ID; `ResolveSynthetic` only queries the lease's own scope; active/in-flight scopes are never reaped or evicted; clear/expiry/release races are handled with a documented lock order; `-race` is clean.
- **Native wire formats:** each adapter renders its native non-stream/NDJSON/SSE success and error envelope on the strict path; strict errors precede any body, so JSON error envelopes remain legal even for wire-stream requests.
- **Triage confinement:** disabled-by-default (routes unregistered → 404), actual-peer loopback (proxy headers untrusted), constant-time bearer, `no-store`, no CORS, encoded-path/method-override bypasses fail, clear-all needs an exact confirmation header.

---

## 8. Verification evidence

All commands run from the implementation worktree. No results were cached (`-count=1`; isolated lint cache via `mktemp -d`).

| Command | Result |
|---|---|
| `git status --short` | clean (before and after review) |
| `git diff --check 22b9da7..7cbe190` | clean |
| `git merge-base --is-ancestor 7cbe190 HEAD` | YES |
| `git diff --name-status 7cbe190..HEAD` | prompt doc only |
| `make fmt-check` | EXIT 0 |
| `go vet ./...` | EXIT 0 |
| `go test ./... -count=1` | EXIT 0 (25 packages ok, 0 fail, 0 skip) |
| `go test -race ./... -count=1` | EXIT 0 (25 ok, 0 fail, 0 skip) |
| `make lint` (golangci-lint 2.12.2, isolated cache) | EXIT 0 — 0 issues |
| `make arch-lint` (go-arch-lint) | EXIT 0 — "No warnings found" |
| `make test-privacy` | EXIT 0 |
| `make test-privacy-race` | EXIT 0 (`tests/privacy` 7.35s, `internal/privacy` 1.30s) |
| `govulncheck ./...` | EXIT 0 — 0 called vulns; 1 unreachable module advisory (matches summary) |
| `make ci` (isolated cache) | EXIT 0 |
| `python3 scripts/test_gen_grafana_dashboard.py` | EXIT 0 (16 tests) |
| `python3 scripts/test_privacy_docs.py` | EXIT 0 (17 tests) |
| Grafana regen vs `docs/grafana/otto-gateway-dashboard.json` | **byte-equal** (87 panels) |
| `node --test internal/admin/admin_js_test.js` | EXIT 0 |
| `bash tests/install/privacy_secrets_posix_test.sh` | EXIT 0 |
| `bash tests/scripts/test-privacy-cli.sh` | EXIT 0 |
| `bash tests/scripts/test-support-redact.sh` | EXIT 0 |
| `bash tests/scripts/test-support-bundle.sh` | EXIT 0 |
| `pwsh tests/install/privacy_secrets_windows_test.ps1` | EXIT 0 (portable pwsh 7.6.4/macOS) |
| `pwsh tests/scripts/test-privacy-cli.ps1` | EXIT 0 |
| `pwsh tests/scripts/test-support-redact.ps1` | EXIT 0 |
| `pwsh tests/scripts/test-support-bundle.ps1` | EXIT 0 |

**Benchmark contract** — `go test ./internal/privacy -run '^$' -bench 'Privacy(Standard|Strict|Parallel)' -benchmem -count=3` (three samples), EXIT 0. Representative medians: StandardInbound 1KiB ≈ 0.39 ms, StrictInbound 1KiB ≈ 0.98 ms, StrictOutbound 1KiB ≈ 1.42 ms; scaling to 64KiB and 4MiB is linear (strict ≈ 1.0 MB/s across all sizes, standard ≈ 2.7 MB/s), allocations in the hundreds; `Parallel100Scopes` ≈ 7 ms/op. No superlinear blow-up, no Gateway-wide lock evident.

**Cross-builds (cgo-free)** — into a `mktemp -d`; SHA-256:
```
a59b458bcabc3b84fafdf95038805eb9b61bee323a395f573a3cf02a69bf1ece  otto-gateway-linux-amd64
d47ecb19f90b57f8e170589f960044121454ca953a797157688167f9cc021e9a  otto-gateway-darwin-arm64
4abe3cd7e4da8b3847e776a6924dd99d877cd174419baa3433f500904a725943  otto-gateway-darwin-amd64
d4d7ddc69e66636e7f75526911573a41cea4f35a59a52ea130257ad0eae40511  otto-gateway-windows-amd64.exe
```
All four `CGO_ENABLED=0` builds EXIT 0.

**Disposable probe:** `internal/privacy/zz_adv_probe_test.go` was created to drive the real service against credential one-way, forged-marker, and output-only-alias scenarios (§4-R2/R3/R4) — all passed — then **removed**. `bash scripts/test-pii.sh` / `pwsh scripts/test-pii.ps1` live-instance smoke checks were **not** run against a live Gateway (no isolated live instance was stood up; the boundary was instead exercised deterministically via the probe and the `tests/privacy` real-server harness).

**Final worktree status:** clean — `git status --short` shows only this untracked review document; HEAD unchanged at `0ccffbc`.

---

## 9. Unavailable platform checks

No native Windows host was available; these are recorded as **UNAVAILABLE**, not PASS (portable pwsh on macOS does not prove native NTFS/ACL security):
- Real-Windows DACL / current-SID enforcement on managed-secret and `.env`/`overrides.env` files.
- Continuous-reader-during-replacement atomicity on native Windows filesystem semantics.
- Support-bundle / secret-file **junction-swap** and ancestor-swap defenses on NTFS.

The portable PowerShell suites (secret lifecycle, CLI parity, support redaction/bundle) and the live-route PowerShell parity ran on macOS pwsh 7.6.4 and passed.

---

## 10. Required remediation before shipment

Ordered; each with the focused regression test it needs.

1. **M1 (fix before ship or in the first follow-up):** in `Service.Before`, resolve and reject the requested profile *before* the `!PIIEnabled` short-circuit, so a strict (or unknown) request against a PII-disabled / hook-absent gateway returns `400 privacy_profile_unavailable` (spec §6.3/§14). **Test:** a `PIIEnabled:false`, strict-absent service returning `CodeProfileUnavailable` for a `strict` header, and asserting no worker dispatch.

No other finding is Critical or High. The Low items below are recommended follow-ups (not ship-blockers), each with its regression test:
2. **L1/L3** receipt coverage semantics — give standard aggregation a non-`full` coverage and stop claiming `full` on strict internal-error paths. **Test:** standard aggregated receipt asserts `coverage!="full"`; strict internal-error receipt asserts truthful coverage.
3. **L2** reconcile `USPhone` between `privacyTechnicalPIIEntities` and `categoryForEntity`. **Test:** pin the chosen category and the boot acceptance/rejection of `USPhone:pseudonymize`.
4. **L6** compute the triage `operation` label from path shape, not the pre-routing empty URL param. **Test:** denied `DELETE …/scopes/{id}` records `operation="clear"` (not `clear_all`).
5. **L7** populate `lastErrorCode` from bounded `*Error.Code` in `observeRequestFailure`. **Test:** after an input block, the posture tile reports `privacy_input_blocked`.
6. **L4/L5/L8** low-cost hardening (document unpadded base64url; consider tightening the bare-payload heuristic or documenting the strict long-string constraint; add an explicit standard-mode `pseudonymize`→replace case without WARN noise).

---

*Reviewer's bottom line:* the load-bearing privacy properties are correctly implemented and adversarially defensible. Ship after addressing M1's contract gap; the remaining Lows are telemetry/semantics polish that do not affect the privacy boundary's guarantees.
