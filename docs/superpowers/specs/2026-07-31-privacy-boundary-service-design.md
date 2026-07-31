# Gateway Privacy Boundary Service — Design

**Date:** 2026-07-31
**Status:** Approved for implementation
**Audience:** Gateway implementers, workflow-engine implementers, operators, and security reviewers
**Post-read action:** Execute `docs/superpowers/plans/2026-07-31-gateway-privacy-boundary-service.md` task-by-task with strict TDD

## 1. Purpose

Gateway already protects recognized personally identifiable information (PII)
at the model boundary. Its canonical `PIIRedactionHook` walks all three API
surfaces, transforms recognized values before the upstream worker sees them,
and restores encrypted values on the response path.

The network-hardening workflow analysis exposed a broader requirement. A
workflow may contain credentials, network identifiers, configuration values,
subflows, and multiple parallel model calls. Protecting that workflow requires
more than isolated regular-expression replacements:

- high-confidence credentials must never reach the model or be restored;
- technical identifiers must remain useful enough for topology and policy
  reasoning;
- the same value must have a stable alias across parallel calls in one
  workflow, but not become linkable across unrelated workflow runs;
- strict requests must fail closed when sanitization or response validation
  cannot be completed;
- a workflow must be able to prove that it passed through the Gateway privacy
  boundary rather than calling a worker directly;
- operators need safe status, metrics, and an explicit local-only triage path;
- the existing PII defaults and three API contracts must remain compatible.

This design turns the existing PII hook into the compatibility-facing adapter
for a modular, concurrency-safe privacy service. The service remains in the
Gateway process. It does not introduce another daemon, port, graph authority,
or persistent mapping database.

## 2. Current State and Gaps

### 2.1 Existing protection

The current PII hook provides:

- one canonical implementation shared by Ollama, OpenAI, and Anthropic;
- request and response walking across system text, messages, tool inputs, tool
  results, and response tool-call arguments;
- replace, mask, hash, drop, and AES-256-GCM encrypt/decrypt actions;
- entity-level action overrides;
- 16 regex recognizers and optional PERSON/LOCATION NER;
- safe hook introspection without key disclosure.

The existing ACP-capture support redactor separately provides useful
key-aware credential recognition for diagnostic JSON and strings.

### 2.2 Gaps this design closes

The current model-boundary path does not provide:

- a shared secret and credential classifier;
- scoped, stable, format-preserving technical aliases;
- a mapping lifecycle for multi-call or parallel workflows;
- an independent residual-content verification pass;
- strict fail-closed input and output handling;
- an output policy for unknown aliases or newly generated sensitive values;
- a machine-readable privacy receipt;
- bounded mapping capacity, expiry, inspection, and explicit cleanup;
- privacy-specific Prometheus metrics;
- one consistent effective privacy posture in the dashboard and documentation.

The current operator surfaces also disagree about parts of the recognizer
inventory and the NER default. Implementation must remove that drift.

## 3. Goals

- Preserve the current PII behavior as the default `standard` profile.
- Add a strict profile that detects credentials, pseudonymizes technical
  identifiers, verifies transformed input and model output, and fails closed.
- Allow configuration to establish a minimum profile while allowing a
  workflow to request an equal or stronger profile.
- Keep aliases stable within one workflow privacy scope and unlinkable across
  unrelated scopes.
- Support parallel requests and subflows without a Gateway-wide privacy lock.
- Keep all mappings memory-only, bounded, expiring, and explicitly clearable.
- Give authorized local operators a break-glass mapping inspection path.
- Return a bounded receipt that lets workflow logic require proof of Gateway
  privacy enforcement.
- Expose safe dashboard status and useful, bounded Prometheus telemetry.
- Reuse shared classifiers for diagnostics without giving diagnostic code
  access to reversible mappings.
- Preserve static, cgo-free, cross-platform Gateway builds.

## 4. Non-goals

- Parsing PDF, DOCX, XLSX, archives, packet captures, or other binary inputs.
- Retrieving source documents or authenticating to source systems.
- Deciding which source fields a workflow actually needs.
- Executing workflow tools, scripts, or subflows.
- Validating workflow-specific output schemas.
- Rendering, storing, distributing, or retaining workflow artifacts.
- Preventing a client from bypassing the Gateway at the network layer. The
  workflow engine detects bypass by requiring a valid Gateway receipt.
- Persisting privacy mappings across Gateway restarts.
- Exposing protected mappings in the browser dashboard, metrics, ordinary
  logs, health responses, or support bundles.

## 5. Chosen Architecture

### 5.1 Privacy service behind the existing hook

One `privacy.Service` is constructed at Gateway startup and shared by all
requests. It owns:

- the immutable profile catalog and resolved configuration;
- the PII, technical-identifier, and secret classifier registry;
- transformation and residual-verification policy;
- the scoped mapping store;
- receipt construction;
- bounded counters and safe runtime snapshots;
- the protected triage operations.

The registered `PIIRedactionHook` name remains stable for backward
compatibility with `ENABLED_HOOKS`, health consumers, tests, and existing
operator guidance. Its inbound and outbound methods delegate to the privacy
service.

The service is modular internally. Profile resolution, classification, span
arbitration, alias generation, scope storage, enforcement, receipts, and
telemetry must remain independently testable rather than becoming one large
hook implementation.

### 5.2 Other consumers

- The admin dashboard and health adapters receive a safe snapshot projection.
- The Prometheus package receives a bounded metrics snapshot or event bridge.
- The localhost triage API receives a capability-scoped interface that can
  reveal or clear mappings only after its own authorization checks.
- ACP capture and support redaction reuse the shared secret classifier only.
  They cannot acquire privacy scopes or reverse mappings.

### 5.3 Hook ordering invariant

The privacy boundary must be the final content-mutating inbound stage before
logging and worker dispatch. The effective order is:

```text
Request ID
→ authentication
→ JSON-format steering
→ context compression
→ privacy transformation and residual validation
→ logging
→ upstream worker
```

Moving compression before privacy eliminates the existing possibility of
truncating an encrypted privacy token after the privacy hook has validated it.
Compression may process raw canonical content in Gateway memory, but its output
is then fully passed through the privacy boundary before logging or worker
dispatch. No subsequent Pre hook may mutate model-bound content.

On the response path, the service validates and transforms the complete worker
response before it restores approved aliases. Logging observes only the final
authorized response representation, but ordinary logging remains metadata-only
and must never serialize restored response bodies.

## 6. Profiles and Precedence

### 6.1 Standard profile

`standard` preserves current behavior:

- `PII_REDACTION_ENABLED` remains enabled by default;
- `PII_REDACTION_MODE=encrypt` remains the default action;
- the existing entity allowlist and action overrides continue to work;
- current round-trip behavior and response bodies remain compatible;
- existing explicit opt-out remains possible when strict is not available or
  required.

The new privacy work does not make current PII protection opt-in.

### 6.2 Strict profile

`strict` includes standard PII handling plus:

- high-confidence secret and credential classification;
- one-way credential replacement or removal;
- scoped pseudonymization of technical identifiers;
- an independent transformed-input residual scan;
- complete response buffering and output validation;
- unknown-token and unauthorized-restoration rejection;
- mandatory privacy receipts;
- fail-closed scope, capacity, and internal-error behavior.

Strict requires the privacy hook and core PII processing to be enabled. NER
remains explicitly configurable because its language coverage and
false-positive profile differ from the high-confidence recognizers; the
dashboard and receipt-adjacent status make its effective state visible.

### 6.3 Precedence

The configured default profile is the minimum:

```text
effective profile = stronger(configured default, requested profile)
```

A workflow may raise `standard` to `strict`. It may never lower a Gateway whose
default is `strict`. An unavailable or unknown requested profile is a request
error, not a silent fallback.

## 7. Configuration Contract

The generated environment template gains these settings:

```dotenv
PRIVACY_DEFAULT_PROFILE=standard
PRIVACY_REQUEST_PROFILES=standard,strict
PRIVACY_ALIAS_KEY=<auto-generated non-empty secret>
PRIVACY_SECRET_ACTION=replace
PRIVACY_TECHNICAL_ACTION=pseudonymize

PRIVACY_SCOPE_TTL=1h
PRIVACY_MAX_SCOPES=128
PRIVACY_MAX_ENTRIES_PER_SCOPE=4096
PRIVACY_MAX_TOTAL_ENTRIES=32768

PRIVACY_TRIAGE_ENABLED=false
PRIVACY_TRIAGE_TOKEN=<auto-generated non-empty secret>
```

### 7.1 Configuration semantics

| Setting | Default | Contract |
|---|---:|---|
| `PRIVACY_DEFAULT_PROFILE` | `standard` | Minimum profile for every request; `standard` or `strict` |
| `PRIVACY_REQUEST_PROFILES` | `standard,strict` | Startup-bounded profiles callers may name; never permits downgrade |
| `PRIVACY_ALIAS_KEY` | generated | Key-isolated HMAC input for scoped alias derivation; never exposed |
| `PRIVACY_SECRET_ACTION` | `replace` | `replace` or `drop`; never encrypt-and-restore |
| `PRIVACY_TECHNICAL_ACTION` | `pseudonymize` | `pseudonymize` or `drop` under strict policy |
| `PRIVACY_SCOPE_TTL` | `1h` | Idle duration; active requests prevent expiry |
| `PRIVACY_MAX_SCOPES` | `128` | Maximum retained active/closing scope records |
| `PRIVACY_MAX_ENTRIES_PER_SCOPE` | `4096` | Maximum reversible entries in one scope |
| `PRIVACY_MAX_TOTAL_ENTRIES` | `32768` | Global reversible-entry maximum |
| `PRIVACY_TRIAGE_ENABLED` | `false` | Registers the sensitive triage surface when true |
| `PRIVACY_TRIAGE_TOKEN` | generated | Separate bearer capability for triage; required when triage is enabled |

Existing `PII_*` settings retain their meanings. The per-entity action parser
adds `pseudonymize` only for entity types for which that action is defined.
Strict secret entities never accept a reversible action.

An explicit compatible `PII_ENTITY_ACTIONS` entry continues to override the
category and global defaults, preserving existing configurations. In its
absence, strict technical entities use `PRIVACY_TECHNICAL_ACTION`; remaining
PII uses `PII_REDACTION_MODE`. Every effective action is displayed in the
read-only posture so an override cannot be mistaken for the category default.

### 7.2 Startup validation

Gateway refuses to start when:

- a profile, action, duration, or capacity is invalid;
- a maximum is zero, negative, or internally inconsistent;
- strict is available but the privacy hook or core PII work is disabled;
- strict is available but the alias key is absent;
- triage is enabled without its separate token;
- an entity is assigned an action unsupported by that entity category.

Users who intentionally disable PII must also remove `strict` from the allowed
profiles. This makes a weakening configuration explicit.

### 7.3 Installer and upgrade behavior

`gw init` and its PowerShell equivalent mint `PRIVACY_ALIAS_KEY` and
`PRIVACY_TRIAGE_TOKEN` with the other managed secrets. Reinitialization
preserves existing values unless the operator explicitly requests secret
rotation. Generated `.env` contains defaults; operator customizations remain
in `overrides.env` and take precedence. Changes require the existing restart
flow.

Rotating the alias key invalidates active aliases. The wrapper must warn and
require the same explicit regeneration path used for other managed secrets.

The compiled fallback for `PII_NER_ENABLED`, generated template, admin docs,
and operator docs must be made consistent at `true` in the same release. An
operator may still explicitly set it to `false`, including under strict, with
the documented reduction in PERSON/LOCATION coverage.

## 8. Request and Receipt Contract

### 8.1 Request headers

All three LLM surfaces accept:

```http
X-GW-Privacy-Profile: strict
X-GW-Privacy-Scope: run-7f29b4d4
```

Profile and scope metadata are extracted consistently before canonical engine
execution and placed in request context. The service remains the authority for
effective-profile resolution and scope acquisition.

Scope IDs are opaque caller-generated identifiers with a bounded length and a
restricted printable character set. They are not secrets. Invalid IDs are
rejected before worker dispatch.

When no scope is supplied, Gateway creates an ephemeral request-level scope.
That is sufficient for one model call. A multi-call workflow must generate one
random scope ID per run and propagate it through all subflows and parallel
calls.

### 8.2 Response receipt

Every protected response carries:

```http
X-GW-Privacy-Receipt: <base64url-encoded JSON>
```

The version 1 payload is bounded and contains no values or per-entity details:

```json
{
  "version": 1,
  "profile": "strict",
  "scope": "run-7f29b4d4",
  "coverage": "full",
  "result": "pass",
  "transformed": 12,
  "restored": 4,
  "blocked": 0
}
```

`coverage=full` means strict input and output enforcement completed before the
response was released. A standard-mode stream that does not require response
aggregation may emit an inbound-only receipt before streaming starts with
`coverage=input`; it must not claim strict output validation or restoration
counts. A workflow that requests strict accepts only `profile=strict`,
`coverage=full`, and `result=pass`.

Blocked strict requests return a receipt when enough request context exists to
do so. Receipt absence, invalid encoding, an unexpected profile, or a
non-`pass` result is a workflow-engine failure.

The receipt is evidence that the call traversed the expected local Gateway
boundary; it is not a portable attestation against a malicious replacement
server. No receipt signing scheme is introduced in this phase.

### 8.3 Streaming

Strict mode buffers the complete worker response before releasing headers or
body content. A strict response therefore cannot stream partially and then
discover a privacy violation. Surface adapters retain their native-compatible
non-streaming response and error shapes.

Standard-mode streaming behavior remains unchanged except where the existing
PII encrypt round-trip already requires aggregation.

## 9. Classifier Registry

### 9.1 Existing entity inventory

The canonical documented inventory is 16 regex recognizers:

1. Email
2. IPv4
3. IPv6
4. SSN
5. CreditCard
6. USPhone
7. SIP_URI
8. IMEI
9. IMSI
10. MSISDN
11. MAC_ADDRESS
12. COORDINATES
13. SITE
14. USAddress
15. USState
16. USZIP

PERSON and LOCATION are added by NER when enabled.

### 9.2 Secret categories

The shared secret classifier adds high-confidence recognition for:

- authorization and proxy-authorization values;
- bearer, basic-auth, OAuth access, and refresh tokens;
- generic and provider-shaped API keys;
- passwords and passphrases in structured assignments;
- PEM, SSH, and service-account private keys;
- cloud access keys and client secrets;
- database and service connection strings containing credentials;
- JSON, YAML-like, dotenv, header, and CLI assignments whose normalized key
  names indicate a credential.

Generic words such as `key` are not sufficient by themselves. Recognition must
use structure, value shape, or credential context to avoid corrupting ordinary
configuration keys and prose.

### 9.3 Span arbitration

All classifiers operate against the original string and produce positional
spans. Accepted spans are resolved deterministically in this order:

1. high-confidence secret or credential;
2. structured exact assignment;
3. validated regex entity;
4. contextual technical identifier;
5. NER entity.

Higher-priority spans win overlaps. Ties use the most specific/longest span and
then registry order. Rewriting occurs once after arbitration so one recognizer
never scans another recognizer's replacement text.

### 9.4 Coverage boundary

The classifier walks all canonical string-bearing locations:

- system content;
- message text parts;
- tool-use inputs;
- tool-result content;
- response text;
- returned tool-call arguments.

It does not parse binary resources. Workflows extract and minimize binary or
document content before calling the Gateway.

## 10. Scoped Pseudonym Mapping

### 10.1 Hybrid derivation and ledger

Aliases combine deterministic scoped derivation with a memory-only two-way
ledger.

The derivation input is conceptually:

```text
HMAC(PRIVACY_ALIAS_KEY, scope ID || entity type || canonical original value)
```

The HMAC output selects or seeds a valid alias for the entity category. The
scope ledger stores both directions:

```text
original value ⇄ synthetic value
```

The keyed derivation makes repeated concurrent discovery converge on the same
candidate. The ledger provides collision detection, reverse restoration, and
entity metadata. Alias insertion is atomic. Each entry records whether its
source was caller input or newly generated worker output; only caller-input
entries are eligible for reverse restoration.

The same value and entity in one scope receive the same alias. The scope ID is
part of derivation, so the same value receives a different alias in another
workflow run.

### 10.2 Technical shape

- IPv4 and IPv6 aliases remain syntactically valid addresses.
- Explicit CIDR blocks are allocated into documented synthetic/private ranges
  while preserving prefix length and host relationships when representable.
- Addresses belonging to the same observed source subnet remain in the same
  synthetic subnet.
- The mapper fails strict processing rather than silently discarding a
  relationship it has promised to preserve.
- MAC aliases are locally administered unicast addresses.
- IMEI, IMSI, MSISDN, and telephone aliases preserve required length and
  checksum/format rules where applicable.
- SIP, site, cell, and device identifiers retain a typed, stable synthetic
  form.
- Coordinates map consistently within the scope without exposing the original
  location.

Exact synthetic pools and format algorithms are implementation decisions that
must be documented and exhaustively tested in the implementation plan. They
must not emit an address that operators could mistake for a known production
endpoint without a documented safe-range rule.

### 10.3 Credentials are never reversible

Secrets use stable one-way labels such as:

```text
[SECRET:API_KEY_1]
[SECRET:PASSWORD_1]
```

Repeated instances may share a request/scope-local label to preserve useful
structure, but the original is never placed in the reversible ledger and is
never restored into model output.

### 10.4 Lifetime and capacity

- TTL is based on inactivity, not total workflow duration.
- Each request acquires a scope reference and releases it when inbound and
  outbound processing has completed.
- The expiry worker never removes a scope with an in-flight reference.
- Expired and closed scopes are reclaimed before new capacity is rejected.
- An active scope is never silently evicted to satisfy another request.
- Per-scope and global entry limits are enforced atomically.
- Strict requests fail closed when a required reservation cannot be made.
- Restart clears every scope and mapping.

### 10.5 Explicit clearing

Clearing marks a scope closed immediately and rejects new acquisitions. Any
already-running request may finish against its acquired state. The ledger is
wiped as soon as its in-flight count reaches zero. A workflow that wants to
continue must use a new scope ID.

Closed-scope tombstones remain only for a bounded interval needed to reject
accidental reuse, then expire with the normal lifecycle.

## 11. Concurrency Contract

The service must support multiple workflows and parallel subflows safely:

- immutable profile and classifier configuration is read without mutation;
- there is no Gateway-wide mutex around classification or transformation;
- unrelated scopes do not block each other's mapping operations;
- same-scope `get-or-create` is atomic and collision-safe;
- counters and capacity reservations remain correct under contention;
- clear, expiry, output restoration, and new alias creation have explicit race
  behavior;
- no goroutine retains a raw request or response beyond its request lifetime;
- a panic inside privacy processing is recovered at the existing hook boundary
  and becomes a strict internal failure, never a bypass.

Focused tests must run under Go's race detector and include parallel same-scope,
parallel different-scope, clear-during-request, expiry-during-request, and
capacity-boundary scenarios.

## 12. Inbound Enforcement

Strict inbound processing is:

```text
resolve effective profile
→ acquire scope
→ classify original canonical content
→ arbitrate spans
→ replace credentials one-way
→ pseudonymize technical identifiers
→ apply configured PII actions
→ independently scan the transformed canonical content
→ allow or reject
```

The independent scan uses a separate traversal pass so a missed field, malformed
replacement, or transform defect cannot be hidden by the first traversal's
state. A finding in strict mode blocks worker dispatch.

Standard mode continues to apply existing PII actions without gaining strict
fail-closed behavior.

## 13. Outbound Enforcement

Strict outbound processing is:

```text
buffer the complete worker response
→ validate every privacy token and alias
→ reject unknown or malformed tokens
→ classify newly generated content
→ reject high-confidence generated credentials
→ safely transform newly generated personal/technical values
→ independently scan again
→ restore only known reversible mappings from this scope
→ build receipt
→ release response
```

Rules:

- a raw protected original found before authorized restoration blocks output;
- a worker cannot invent an alias that causes an unrelated mapping lookup;
- generated credentials are never returned;
- generated technical identifiers remain synthetic;
- generated personal data is redacted rather than trusted as caller data;
- only aliases observed and authorized in this scope may restore originals;
- restoration occurs after all checks that would otherwise mistake an
  intentionally restored original for an upstream leak.

## 14. Error Contract

Errors contain bounded codes and safe counts, never protected values, aliases,
regexes, raw classifier errors, or key material.

| Condition | HTTP status | Stable code |
|---|---:|---|
| Invalid profile or scope syntax | 400 | `privacy_request_invalid` |
| Requested profile unavailable | 400 | `privacy_profile_unavailable` |
| Closed scope | 409 | `privacy_scope_closed` |
| Unsafe/untransformable input | 422 | `privacy_input_blocked` |
| Unsafe worker output | 502 | `privacy_output_blocked` |
| Scope or mapping capacity exhausted | 503 | `privacy_capacity_exceeded` |
| Internal classifier/mapping failure | 503 | `privacy_internal_error` |

Each adapter renders the stable condition in its native-compatible error
envelope. Strict-mode errors occur before any response body has been streamed.

## 15. Diagnostic and Local-Storage Safety

- Logging runs after privacy on inbound content.
- Strict requests do not write raw prompts to chat trace. Their trace entry
  contains request metadata and a privacy summary only.
- Standard chat trace retains its explicitly sensitive, opt-in current
  behavior and warnings.
- ACP capture and support-bundle redaction use the shared secret classifier.
- Support bundles never include privacy ledgers or triage responses.
- Metrics, receipts, health, dashboard status, and normal logs contain only
  bounded counts and safe enum values.
- Raw mappings exist only in Gateway process memory and the explicit triage
  response selected by an authorized local operator.

Memory inspection by a process with equivalent local privileges remains an
accepted local-machine risk. The design reduces persistence and accidental
exposure; it is not an operating-system enclave.

## 16. Protected Triage API

### 16.1 Exposure and authorization

When `PRIVACY_TRIAGE_ENABLED=false`, all triage routes return 404.

When enabled, every triage operation requires:

- the actual TCP peer address to be loopback;
- no trust in `X-Forwarded-For` or similar proxy headers;
- `Authorization: Bearer <PRIVACY_TRIAGE_TOKEN>`;
- no CORS permission;
- `Cache-Control: no-store`;
- safe audit logging of operation, result, peer, and scope identifier only.

The triage token is separate from ordinary Gateway authentication. Loopback by
itself is insufficient because local browsers and processes can originate
requests.

### 16.2 Operations

```text
GET    /admin/api/privacy/scopes
GET    /admin/api/privacy/scopes/{scope-id}/mapping
DELETE /admin/api/privacy/scopes/{scope-id}
DELETE /admin/api/privacy/scopes
```

The scope list exposes scope ID, profile, safe counts, creation/last-use time,
expiry, in-flight count, and lifecycle state. Mapping inspection returns entity
type plus original and synthetic values. It never returns credentials because
credentials are not stored reversibly.

Clearing one inactive scope returns 204. Clearing an active scope returns 202
after marking it closed. Clear-all requires an explicit confirmation header in
addition to authorization.

### 16.3 CLI

POSIX and PowerShell wrappers provide equivalent commands:

```text
gw privacy status
gw privacy scopes
gw privacy inspect <scope-id>
gw privacy clear <scope-id>
gw privacy clear --all --yes
```

The wrapper resolves the token through the existing `.env` and
`overrides.env` precedence. It never prints the token or places it in process
arguments that ordinary diagnostics expose when a safer input mechanism is
available.

Clearing mappings is operational state management. It does not change privacy
configuration and does not violate the read-only dashboard decision.

## 17. Dashboard and Documentation

### 17.1 Main dashboard

Add a compact Privacy Boundary summary containing:

- effective default profile;
- whether strict requests are available;
- protected and blocked requests since startup;
- active scopes;
- retained entries versus capacity;
- oldest scope age;
- triage enabled/disabled;
- last bounded privacy error code.

### 17.2 About and health

The detailed read-only posture contains:

- profiles and precedence;
- current PII mode, recognizers, and entity actions;
- NER state;
- secret and technical actions;
- alias-key and triage-token presence booleans only;
- TTL and capacity limits;
- receipt and strict-buffering behavior;
- scope and entry utilization;
- complete 16-regex-plus-2-NER inventory.

The same safe projection feeds the admin snapshot and hook-health description.
No form or endpoint edits `.env` or `overrides.env`.

### 17.3 Documentation deliverables

Implementation updates:

- the generated environment template;
- operator configuration and upgrade documentation;
- the embedded admin documentation page;
- README privacy and configuration guidance;
- hook-chain and health contracts;
- workflow integration examples for profile, scope, and receipt handling;
- triage security and cleanup guidance;
- Prometheus series, Grafana panels, example queries, and alerts;
- release notes for the new keys, profile behavior, and strict streaming rule.

## 18. Prometheus and Grafana Contract

Privacy telemetry is registered in the existing Gateway Prometheus registry
and exposed by the existing `/metrics` endpoint. The current Grafana
remote-write prefix policy already includes `gw_*` series.

### 18.1 Series

| Metric | Type | Purpose |
|---|---|---|
| `gw_privacy_requests_total{profile,surface,workload,result}` | Counter | Coverage, strict adoption, and pass/block/error outcomes |
| `gw_privacy_transformations_total{profile,entity,action}` | Counter | Protected data categories and applied actions |
| `gw_privacy_restorations_total{profile,entity,result}` | Counter | Authorized restore, miss, and rejection outcomes |
| `gw_privacy_blocks_total{profile,stage,reason}` | Counter | Fail-closed enforcement by inbound/outbound stage |
| `gw_privacy_residual_findings_total{profile,stage,entity}` | Counter | Defense-in-depth scan findings |
| `gw_privacy_receipts_total{profile,result}` | Counter | Receipt success and bounded failure outcomes |
| `gw_privacy_processing_duration_seconds{profile,stage}` | Histogram | Privacy overhead by classify/transform/verify/restore stage |
| `gw_privacy_scopes_active` | Gauge | Retained active scopes |
| `gw_privacy_scope_requests_in_flight` | Gauge | Requests holding scope references |
| `gw_privacy_mapping_entries` | Gauge | Current reversible ledger entries |
| `gw_privacy_scope_capacity` | Gauge | Configured maximum scopes |
| `gw_privacy_mapping_capacity` | Gauge | Configured global entry maximum |
| `gw_privacy_mapping_per_scope_capacity` | Gauge | Configured per-scope entry maximum |
| `gw_privacy_scope_ttl_seconds` | Gauge | Configured idle TTL |
| `gw_privacy_oldest_scope_age_seconds` | Gauge | Oldest retained active scope age |
| `gw_privacy_scope_events_total{event}` | Counter | Created, expired, closed, and manually cleared scopes |
| `gw_privacy_capacity_rejections_total{resource}` | Counter | Scope/per-scope/global capacity failures |
| `gw_privacy_mapping_operations_total{operation,result}` | Counter | Mapping hits, misses, inserts, restores, and collisions |
| `gw_privacy_errors_total{stage,reason}` | Counter | Bounded internal errors |
| `gw_privacy_triage_requests_total{operation,result}` | Counter | List, inspect, clear, denied, and failed triage operations |
| `gw_privacy_triage_enabled` | Gauge | Break-glass triage posture |

### 18.2 Cardinality and disclosure rules

- Never label by privacy scope, request, session, user, original value, alias,
  token fragment, or raw error.
- `workload` reuses the existing bounded and sanitized `X-GW-Skill` or
  `X-Flow-Name` label, capped at 64 distinct values plus fallback buckets.
- `profile` is startup-validated and bounded.
- `entity`, `action`, `stage`, `reason`, `resource`, `operation`, and `result`
  are fixed enums.
- Metrics record counts, not sample values.

### 18.3 Grafana reporting

Recommended views:

1. coverage and strict-profile percentage by surface and workload;
2. transformations by entity and action;
3. inbound and outbound block rates by bounded reason;
4. scope and mapping utilization, expiry, and rejection rates;
5. privacy-processing average and p95 latency;
6. triage enabled state, inspections, clears, and denied requests.

Recommended alerts:

- scope or mapping utilization above 80 percent;
- any capacity rejection;
- any mapping collision;
- privacy internal errors or receipt failures;
- a material increase in residual findings or output blocks;
- triage left enabled beyond the intended investigation window;
- repeated denied triage requests;
- privacy p95 latency materially above its measured baseline.

An ordinary block may indicate correct enforcement, so alerting should focus on
rate changes and output failures rather than treating every blocked input as a
Gateway outage.

## 19. Security Properties and Threat Controls

| Threat | Control |
|---|---|
| Workflow requests weaker protection | Effective profile cannot fall below configured default |
| Existing hook allowlist omits strict protection | Startup rejects strict availability without the compatibility-facing hook |
| Same identifier correlated across workflow runs | Scope ID is part of keyed alias derivation |
| Alias collision restores the wrong original | Atomic ledger insertion plus collision detection; strict failure on unresolved collision |
| Model invents an alias | Restore only known aliases from the acquired scope |
| Credential reappears in output | Credentials are one-way and output credential findings block |
| Content mutation after validation | Privacy is the final content-mutating inbound hook |
| Parallel cleanup removes an active map | In-flight reference prevents expiry; clear enters closing state |
| Memory exhaustion through many scopes | Per-scope/global limits, expiry, and fail-closed reservations |
| Mapping disclosed through observability | Values excluded from logs, metrics, health, receipts, and dashboard |
| Browser/local process reads mappings casually | Triage disabled by default, loopback peer check, separate bearer token, no CORS/no-store |
| Direct worker call bypasses Gateway | Workflow requires valid privacy receipt and fails on absence |
| Diagnostic capture stores secrets | Shared credential classifier redacts capture and support output |

## 20. Workflow-Engine Responsibilities

After this feature, workflows no longer need generic custom PII
anonymize/de-anonymize nodes. The workflow engine still owns:

1. source-system authentication and retrieval;
2. document, archive, spreadsheet, and structured-file parsing;
3. task-specific data minimization and safe projection;
4. routing every model call through the Gateway;
5. generating and propagating one privacy scope across subflows;
6. requesting the required privacy profile;
7. requiring and validating the Gateway receipt;
8. executing workflow tools and scripts;
9. validating model output against the workflow schema;
10. deciding whether authorized restored values belong in the final artifact;
11. final artifact privacy and schema validation;
12. document rendering, storage, retention, and distribution.

The Gateway validates the model boundary. It cannot decide whether a restored
value is appropriate for a particular final document. That remains a
workflow/business-policy decision.

## 21. Compatibility and Rollout

- `standard` is the upgrade default.
- Existing PII environment names and response bodies remain supported.
- New secrets are generated by normal init/upgrade flows and preserved on
  re-init.
- Strict is opt-in per request until an operator sets the global default to
  `strict`.
- A request for strict against an incompatible configuration fails loudly.
- Strict suppresses streaming so output can be validated before release.
- Dashboard configuration remains read-only.
- Triage remains disabled by default.
- All new API additions are additive; existing public endpoint paths remain
  unchanged.

## 22. Verification Strategy

Implementation follows strict red-green-refactor TDD. Required coverage
includes:

### 22.1 Unit behavior

- every current and new classifier, including adversarial false positives;
- structured credential assignments and nested encoded content;
- deterministic overlap arbitration;
- action validation and profile precedence;
- technical alias syntax, checksum rules, and network relationships;
- same-scope stability and cross-scope unlinkability;
- collision handling and unknown-alias rejection;
- receipt encoding and bounded fields;
- fake-clock TTL and capacity behavior.

### 22.2 Concurrency and lifecycle

- parallel aliases in the same scope;
- independent parallel scopes;
- per-scope and global capacity contention;
- clear during inbound classification and outbound restoration;
- expiry while requests are active;
- cleanup and shutdown with no leaked goroutines;
- focused `go test -race` coverage.

### 22.3 Boundary integration

- every canonical string-bearing request and response location;
- Ollama, OpenAI, and Anthropic success and native error envelopes;
- strict stream request converted to validated buffered response;
- no worker dispatch on input block;
- no partial response on output block;
- receipt present and correct across surfaces;
- missing receipt detects a direct-worker bypass in workflow integration tests;
- compression-before-privacy ordering and no post-privacy mutation.

### 22.4 Operator and security surfaces

- config defaults, invalid combinations, init, upgrade, and key preservation;
- dashboard and embedded documentation rendering;
- health snapshot redaction;
- metric values, cardinality bounds, and absence of protected labels;
- triage disabled behavior, loopback enforcement, token auth, no-store, no
  CORS, inspection, active clear, and clear-all confirmation;
- POSIX/PowerShell CLI parity;
- chat trace, ACP capture, support bundle, logs, errors, metrics, receipts, and
  health contain no unintended protected values.

### 22.5 Release gates

- full Go test suite;
- targeted and full race tests in proportion to runtime;
- formatting, vet, repository lint, architecture lint, and vulnerability scan;
- cgo-free macOS and Windows builds, plus existing Linux compile coverage;
- focused concurrent-load benchmark to confirm the service introduces no
  Gateway-wide lock or unbounded allocation;
- cold-read verification of operator, workflow, and Grafana documentation.

## 23. Acceptance Criteria

The feature is complete when:

1. Current default PII protection remains on and compatible.
2. A strict request cannot reach the worker with a recognized credential or
   unhandled protected value.
3. A strict response cannot reach the client before output validation.
4. Technical identifiers remain valid and relationally useful under the
   documented mapping rules.
5. Repeated identifiers are stable within a scope and differ across scopes.
6. Parallel workflows, expiry, clear, and capacity limits are race-safe.
7. Credentials are never reversible or inspectable through the mapping API.
8. An authorized local operator can inspect and clear non-secret mappings.
9. Dashboard, health, docs, metrics, and CLI show one consistent effective
   posture without exposing protected values.
10. A workflow can require a strict receipt and reject Gateway bypass.
11. All three API surfaces pass compatibility and strict-boundary tests.
12. Workflow documentation clearly retains parsing, minimization, routing,
    schema, tool, and artifact responsibilities outside the Gateway.

## 24. Decisions Locked by This Design

- Use a modular in-process privacy service behind the existing
  `PIIRedactionHook` compatibility name.
- Keep current PII enabled by default; make the additional strict layer
  request-selectable and globally configurable.
- Allow requests to raise but never lower the configured profile.
- Replace credentials one-way; never restore them.
- Preserve technical shape and relationships with scoped synthetic aliases.
- Keep reversible mappings memory-only and isolated by workflow scope.
- Support safe parallel requests and subflows.
- Expose TTL and all capacity limits in environment configuration.
- Return machine-readable privacy receipts.
- Keep dashboard configuration read-only.
- Provide a disabled-by-default, authenticated, loopback-only triage API and
  matching CLI, including explicit clear operations.
- Add bounded privacy metrics to the existing `/metrics` and Grafana path.
- Leave parsing, minimization, tool execution, schema validation, and final
  artifact policy in the workflow engine.

There are no unresolved product decisions in this specification. Exact file
touchpoints, concrete types, individual tests, and atomic commit boundaries
belong in the implementation plan created after the written-spec review gate.
