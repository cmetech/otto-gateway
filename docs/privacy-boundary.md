# Privacy boundary operations

This guide is for an operator or workflow engineer enabling strict model-boundary protection. After reading it, you should be able to configure Gateway, propagate a workflow profile and scope, validate the receipt, monitor enforcement, use local triage, close the scope, and investigate failures without disclosing protected values.

## Boundary and ownership

Gateway applies one privacy service to the Ollama, OpenAI, and Anthropic chat surfaces before content reaches an ACP worker. The registered compatibility name remains `PIIRedactionHook`. The `standard` profile preserves the existing PII behavior. The `strict` profile adds one-way credential handling, scoped technical aliases, independent residual scans, full output validation, and fail-closed receipts.

Any direct worker access bypasses the privacy boundary. Network topology alone does not prove enforcement: a strict workflow must validate `X-GW-Privacy-Receipt` on every response and treat any invalid receipt as a failed call.

Gateway protects canonical text at the model boundary. It is not a workflow engine. Workflow engine owns:

- document parsing and rejection of unsupported binary input;
- data minimization before content reaches Gateway;
- model routing and making every model call through Gateway;
- scope propagation across subflows, retries, and parallel calls;
- receipt enforcement before accepting a result;
- output-schema validation and tool execution policy;
- capture policy, retention, and redaction outside Gateway;
- rendering, storage, distribution, and cleanup of each final artifact.

Gateway does not fetch source documents, decide which source fields are necessary, execute tools or subflows, validate workflow-specific schemas, or store final artifacts. Privacy mappings are not a document store or artifact ledger.

## Profiles and precedence

In the default installation, standard is enabled by default and strict is selectable. Standard retains PII encryption round-trip behavior and is the configured minimum unless an operator selects strict. Strict can also be configured as the minimum.

Profile resolution is monotonic:

```text
effective profile = stronger(configured default, requested profile)
```

A request may raise `standard` to `strict`, but may never lower a strict Gateway to `standard`. Unknown or unavailable profiles return an error; Gateway never silently falls back. Strict availability requires the privacy hook, core PII processing, and an alias key.

## Configuration

Gateway reads privacy configuration once at process start. Every change below is **Restart required**. Put operator customizations in `overrides.env`; it is loaded last and wins over the generated `.env`. `gw upgrade-env` refreshes generated defaults without changing the operator-owned overrides file.

### Privacy settings

| Setting | Default | Meaning |
|---|---|---|
| `PRIVACY_DEFAULT_PROFILE` | `standard` | Minimum effective profile for every request. |
| `PRIVACY_REQUEST_PROFILES` | `standard,strict` | Startup-bounded profiles that requests may name. |
| `PRIVACY_ALIAS_KEY` | `<generated-by-gw-init>` | HMAC input for scope-isolated alias derivation. Never expose or reuse between installations. |
| `PRIVACY_SECRET_ACTION` | `replace` | One-way credential action: `replace` or `drop`. Credentials are never reversible. |
| `PRIVACY_TECHNICAL_ACTION` | `pseudonymize` | Strict technical-identifier action: `pseudonymize` or `drop`. |
| `PRIVACY_SCOPE_TTL` | `1h` | Idle lifetime for a retained scope. |
| `PRIVACY_MAX_SCOPES` | `128` | Maximum retained active or closing scopes. |
| `PRIVACY_MAX_ENTRIES_PER_SCOPE` | `4096` | Maximum reversible entries in one scope. |
| `PRIVACY_MAX_TOTAL_ENTRIES` | `32768` | Maximum reversible entries across all scopes. |
| `PRIVACY_TRIAGE_ENABLED` | `false` | Registers the protected local triage routes when true. |
| `PRIVACY_TRIAGE_TOKEN` | `<generated-by-gw-init>` | Separate bearer capability for triage. Required when triage is enabled. |

Gateway refuses to start for an invalid profile, action, duration, or capacity; inconsistent maxima; strict without core PII and the privacy hook; strict without the alias key; triage without its token; or an unsupported per-entity action. To intentionally disable PII, also remove `strict` from `PRIVACY_REQUEST_PROFILES`.

To make strict the minimum for every request on a pre-privacy installation, use this order. Do not enable strict between the template upgrade and normal re-init: the shipped placeholders are invalid at startup when strict or triage requires them.

1. Preview and apply the current template.

   POSIX:

   ```sh
   gw upgrade-env --dry-run
   gw upgrade-env
   ```

   PowerShell:

   ```powershell
   gw.ps1 upgrade-env -DryRun
   gw.ps1 upgrade-env
   ```

2. Run normal re-init. It mints only missing privacy secrets and preserves existing `AUTH_TOKEN`, `PII_HASH_KEY`, and `PII_ENCRYPT_KEY`. Do not add the regeneration flag during an upgrade.

   POSIX:

   ```sh
   gw init --force --non-interactive
   ```

   PowerShell:

   ```powershell
   gw.ps1 init -Force -NonInteractive
   ```

3. Verify that both privacy values are lowercase 64-hex values without printing either value. These examples inspect the standard per-user `overrides.env`; set `GW_OVERRIDES_FILE` first when the installation uses a project-local or custom overrides file.

   POSIX:

   ```sh
   privacy_overrides="${GW_OVERRIDES_FILE:-${GW_HOME:-$HOME/.gw}/overrides.env}"
   awk -F= '
     ($1 == "PRIVACY_ALIAS_KEY" || $1 == "PRIVACY_TRIAGE_TOKEN") &&
       length($2) == 64 && $2 !~ /[^0-9a-f]/ { present[$1] = 1 }
     END { exit !(present["PRIVACY_ALIAS_KEY"] && present["PRIVACY_TRIAGE_TOKEN"]) }
   ' "$privacy_overrides" && printf '%s\n' 'privacy secrets: present'
   ```

   PowerShell:

   ```powershell
   $privacyOverrides = if ($env:GW_OVERRIDES_FILE) {
     $env:GW_OVERRIDES_FILE
   } elseif ($env:GW_HOME) {
     Join-Path $env:GW_HOME 'overrides.env'
   } else {
     Join-Path (Join-Path $HOME '.gw') 'overrides.env'
   }
   $privacySecrets = @{}
   Get-Content -LiteralPath $privacyOverrides | ForEach-Object {
     if ($_ -cmatch '^(PRIVACY_ALIAS_KEY|PRIVACY_TRIAGE_TOKEN)=([0-9a-f]{64})$') {
       $privacySecrets[$Matches[1]] = $true
     }
   }
   if ($privacySecrets.Count -ne 2) { throw 'privacy secrets missing or placeholder' }
   'privacy secrets: present'
   ```

4. Add this non-secret override to the operator-owned `overrides.env`:

   ```dotenv
   PRIVACY_DEFAULT_PROFILE=strict
   PRIVACY_REQUEST_PROFILES=standard,strict
   ```

Leave both generated privacy values unchanged. Run `gw restart`, then `gw privacy status`; continue only when it reports profile `strict` and strict availability `yes`. To keep standard as the minimum and let individual workflows opt into strict, retain the defaults and send the strict request header instead.

### Retained PII settings

The new profiles do not replace the existing `PII_*` contract:

| Setting | Default | Retained behavior |
|---|---|---|
| `PII_REDACTION_ENABLED` | `true` | Standard protection remains secure by default. An explicit opt-out is allowed only when strict is unavailable and not required. |
| `PII_REDACTION_MODE` | `encrypt` | Existing replace, mask, hash, drop, and AES-256-GCM round-trip actions remain available. |
| `PII_ENABLED_ENTITIES` | empty = all | Selects from the registered recognizers; unknown names fail startup. |
| `PII_HASH_KEY` | generated | Required when hash is active. Rotation breaks prior correlation tokens. |
| `PII_ENCRYPT_KEY` | generated | Required when encrypt is active. Rotation invalidates prior encrypted round-trip tokens. |
| `PII_ENTITY_ACTIONS` | empty | Allowed actions are `replace`, `mask`, `hash`, `drop`, `encrypt`, and `pseudonymize`; pseudonymize is supported only for technical identifiers. Compatible listed overrides win; unlisted personal data uses PII_REDACTION_MODE, and unlisted strict technical data uses PRIVACY_TECHNICAL_ACTION. |
| `PII_NER_ENABLED` | `true` | Enables English PERSON and LOCATION recognition; it remains explicitly configurable under strict. |

The inventory is 16 regex recognizers: Email, IPv4, IPv6, SSN, CreditCard, USPhone, SIP_URI, IMEI, IMSI, MSISDN, MAC_ADDRESS, COORDINATES, SITE, USAddress, USState, and USZIP. PERSON and LOCATION are the two NER recognizers. NER is English-only and has reduced coverage for multilingual names and places. The binary's `PII_NER_ENABLED` compiled default is `true`; an operator can explicitly set it to `false` to avoid the runtime model allocation.

### Managed secrets

The five managed secrets are `AUTH_TOKEN`, `PII_HASH_KEY`, `PII_ENCRYPT_KEY`, `PRIVACY_ALIAS_KEY`, and `PRIVACY_TRIAGE_TOKEN`. A normal forced re-init preserves every usable existing managed secret and mints only a missing or shipped-placeholder privacy alias key or triage token. Explicit `--regenerate-secrets` (or `-RegenerateSecrets`) rotates all five together and warns about mapping loss and restart impact before writing.

Rotating `PRIVACY_ALIAS_KEY` invalidates active aliases. Coordinate the rotation with workflow owners, stop accepting calls, restart Gateway, and start affected workflows with new scope IDs. Never place a real managed secret in source control, examples, tickets, logs, or command arguments.

## Request and receipt contract

All three model surfaces accept the same headers:

```http
X-GW-Privacy-Profile: strict
X-GW-Privacy-Scope: run-7f29b4d4
```

Scope IDs are caller-generated opaque identifiers, not secrets. They may contain `A-Z`, `a-z`, digits, `.`, `_`, `:`, and `-`, with a maximum length of 128 characters. Use one random scope per workflow run. Propagate it through every subflow and parallel request. If the header is omitted, Gateway creates a request-local ephemeral scope, which is unsuitable for a multi-call workflow.

Protected responses carry:

```http
X-GW-Privacy-Receipt: <unpadded-base64url-encoded-JSON>
```

The header uses the RFC 4648 URL-safe alphabet without `=` padding. The version 1 JSON contains bounded fields only: version, effective profile, scope, coverage, result, and transform/restore/block counts. It contains no entity values or key material. A strict consumer accepts only `profile == "strict"`, `coverage == "full"`, and `result == "pass"`.

This Python example decodes and validates a value already obtained from the response header. It does not log the header or decoded scope:

```python
import base64
import binascii
import json

def require_strict_receipt(header_value, expected_scope):
    if not isinstance(header_value, str) or not header_value or len(header_value) > 4096:
        raise RuntimeError("missing receipt")
    try:
        padded = header_value + "=" * (-len(header_value) % 4)
        payload = base64.b64decode(padded, altchars=b"-_", validate=True)
        receipt = json.loads(payload)
    except (binascii.Error, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise RuntimeError("malformed receipt") from exc

    if not isinstance(receipt, dict) or receipt.get("version") != 1:
        raise RuntimeError("malformed receipt")
    if receipt.get("scope") != expected_scope:
        raise RuntimeError("receipt scope mismatch")
    if receipt.get("profile") != "strict":
        raise RuntimeError("non-strict receipt")
    if receipt.get("coverage") != "full":
        raise RuntimeError("non-full receipt")
    if receipt.get("result") != "pass":
        raise RuntimeError("non-pass receipt")
    return receipt
```

Reject a missing receipt, malformed receipt, non-strict receipt, non-full receipt, or non-pass receipt. Apply the same rejection to a direct-worker response, even if its body otherwise looks valid. A receipt proves traversal through the expected local Gateway, not a portable cryptographic attestation.

## Runtime enforcement

Inbound order is:

```text
Request ID → Authentication → JSON steering → Compression → Privacy → Logging → Worker
```

Privacy is the final content-mutating inbound stage. Compression may handle raw canonical content in Gateway memory, but the complete compressed result passes through privacy transformation and an independent residual scan before logging or dispatch. No later pre-hook may mutate model-bound content.

Strict output handling buffers the complete worker response and validates or transforms it before restoring approved aliases and before releasing headers or body bytes. A caller may request streaming, but strict mode emits a native synthetic replay only after full validation. Strict failures use each surface's native error envelope; no partial success has already escaped. Standard streaming remains compatible, except where its existing encrypt round trip requires aggregation.

Strict output also reserves every unbroken run of 38 or more ASCII letters, digits, `_`, or `-` as a possible bare encrypted privacy payload. An unrecognized run is blocked even when it is benign, such as a long Git object ID, base64url blob, or JWT segment. This conservative rule prevents malformed or key-rotated privacy payloads from passing as ordinary text. A workflow that must return such an opaque value should have the model split or escape it with non-reserved delimiters, enforce the strict receipt, then validate and reassemble it downstream.

Under strict, strict chat trace is metadata-only with a bounded privacy summary. Standard chat trace remains sensitive and opt-in because it retains its existing raw-content behavior. Ordinary logs, health, metrics, dashboard status, receipts, and support bundles never contain mappings or protected values.

### Stable errors

| HTTP | Stable code | Meaning |
|---:|---|---|
| 400 | `privacy_request_invalid` | Invalid profile or scope syntax. |
| 400 | `privacy_profile_unavailable` | Requested profile is not available. |
| 409 | `privacy_scope_closed` | The workflow reused a closed scope. |
| 422 | `privacy_input_blocked` | Input could not be transformed and verified safely. The worker was not called. |
| 502 | `privacy_output_blocked` | Worker output failed strict validation. |
| 503 | `privacy_capacity_exceeded` | Scope or mapping capacity was exhausted. |
| 503 | `privacy_internal_error` | A bounded internal classifier or mapping failure occurred. |

Errors expose only a stable code and bounded counts. They never include protected input, aliases, regexes, raw classifier errors, or key material.

## Scope lifecycle and debugging

Mappings are memory only. TTL is based on inactivity, and an in-flight request prevents expiry. Expired and already-closed scopes are reclaimed before capacity is rejected; active scopes are never silently evicted. `PRIVACY_MAX_SCOPES`, `PRIVACY_MAX_ENTRIES_PER_SCOPE`, and `PRIVACY_MAX_TOTAL_ENTRIES` are hard fail-closed limits.

Restarting Gateway clears every scope and mapping. A workflow cannot resume with old aliases after restart or alias-key rotation; start a new scope and repeat the model calls. If an investigation needs a protected mapping, you must reproduce the failure before restart and inspect it through the authorized local triage path. Capturing ordinary logs or support bundles will not preserve mappings by design.

Clearing marks a scope closed immediately. New acquisitions fail with `privacy_scope_closed`; already-running requests may finish. An inactive scope is wiped immediately. An active scope is wiped when its last in-flight request releases it. Continue the workflow only with a new scope ID.

## Status and protected triage

Use the wrappers on macOS/Linux or their PowerShell equivalents on Windows:

```text
gw privacy status
gw privacy scopes
gw privacy inspect <scope-id>
gw privacy clear <scope-id>
gw privacy clear --all --yes
```

`gw privacy status` reads the ordinary safe snapshot at `GET /admin/api/snapshot`; it works without the triage bearer and reports profile, strict availability, triage posture, active scopes, and aggregate entry counts. The remaining commands require enabled triage and read the separate triage token from the effective `.env` plus `overrides.env`. The POSIX wrapper supplies it to the HTTP client through standard input; PowerShell constructs headers in-process. Neither wrapper prints it, places it in process arguments, follows redirects, accepts a non-loopback target, or honors an ambient proxy for this request.

On successful protected commands, the wrappers print bounded JSON for `scopes` and `inspect`, `privacy: closing` for a `202` clear, and `privacy: cleared` for a `204` clear. Disabled, unauthorized, unavailable, invalid-scope, redirect, and non-loopback-target states exit nonzero with a bounded message. `clear --all` does not contact Gateway unless the arguments are exactly `clear --all --yes`.

The protected API is registered only when `PRIVACY_TRIAGE_ENABLED=true`:

| Operation | Successful result |
|---|---|
| `GET /admin/api/privacy/scopes` | `200` with bounded scope metadata. |
| `GET /admin/api/privacy/scopes/{scope-id}/mapping` | `200` with reversible entity, original, synthetic, provenance, and creation time. Credentials never appear because they are not reversible. |
| `DELETE /admin/api/privacy/scopes/{scope-id}` | `204` when wiped; `202` with `closing` when an active request still holds the scope. |
| `DELETE /admin/api/privacy/scopes` | `204`; requires `X-GW-Privacy-Confirm: clear-all`. |

When triage is disabled, these four routes are not registered and return `404`. Every registered-route outcome requires the separate triage token and the actual TCP peer must be loopback. Gateway ignores `Forwarded` and `X-Forwarded-For`; a forwarded loopback claim cannot authorize a remote peer. Every outcome sends `Cache-Control: no-store` and grants no CORS access. Loopback alone is insufficient because local browsers and processes can originate requests.

The protected API returns `403` for a non-loopback TCP peer, `401` for a missing or wrong bearer, `400` for an invalid scope or missing clear-all confirmation, `404` for an unknown scope, `503` when the triage capability is unavailable, and a bounded `500` if a response cannot be encoded within its limit. Error bodies contain a stable triage code only; they never echo the token, scope, mapping, or raw error.

### Break-glass procedure

1. Confirm that the incident requires viewing a reversible mapping and record the approved local operator.
2. Put `PRIVACY_TRIAGE_ENABLED=true` in `overrides.env` and restart Gateway.
3. Run `gw privacy status`, then `gw privacy scopes`. Do not paste output into a ticket or chat.
4. Run `gw privacy inspect <scope-id>` locally. Inspect only the necessary scope and keep terminal capture disabled.
5. Run `gw privacy clear <scope-id>`. A `closing` result means an active request must finish before memory is wiped.
6. For an approved full cleanup only, run `gw privacy clear --all --yes`.
7. Put `PRIVACY_TRIAGE_ENABLED=false` in `overrides.env`, restart, and verify `gw privacy status` reports triage disabled.

Do not use a raw HTTP example when the wrapper is available: the wrapper enforces loopback, proxy, redirect, token, and confirmation rules without exposing the bearer in command arguments.

## Monitoring and troubleshooting

The shipped Grafana dashboard has a **Privacy Boundary** row. It reports request results by bounded profile, surface, workload, and result; transformations and restorations; block and residual reasons; processing latency; current scope/mapping use beside configured limits; receipt outcomes; triage operations; and internal errors.

Alert indicators cover strict blocks, residual findings, capacity pressure, mapping growth, internal privacy errors, and missing successful strict receipts. Privacy PromQL groups only by fixed enums and bounded workload values. It never groups by scope, request ID, route, raw error, token, alias, original value, or synthetic value.

Use this sequence without collecting request bodies:

1. `gw privacy status`: verify effective default, strict availability, triage disabled unless actively investigating, utilization, and the last stable error.
2. Check the Privacy Boundary dashboard row for the failing profile, surface, bounded workload, stage, and stable reason.
3. For `privacy_request_invalid` or `privacy_profile_unavailable`, correct the caller headers or configured allowed profiles.
4. For `privacy_scope_closed`, create and propagate a new random workflow scope.
5. For `privacy_input_blocked` or `privacy_output_blocked`, reproduce with a minimized synthetic payload; never copy the protected production payload to logs or tickets.
6. For `privacy_capacity_exceeded`, close finished workflow scopes or raise the relevant configured maximum and restart. Do not clear a live unrelated workflow.
7. For `privacy_internal_error`, preserve bounded metrics and ordinary logs, then reproduce safely. Enable triage only if a mapping is essential to the diagnosis, and close the break-glass procedure immediately afterward.
8. For missing strict receipts, stop consuming responses and verify every call routes through Gateway before retrying.

## Upgrade and rollback

### Upgrade

Run `gw upgrade-env --dry-run`, `gw upgrade-env`, and then normal `gw init --force --non-interactive` before restart. PowerShell uses `gw.ps1 upgrade-env -DryRun`, `gw.ps1 upgrade-env`, and `gw.ps1 init -Force -NonInteractive`. The generated template introduces all `PRIVACY_*` defaults while `overrides.env` continues to win. Normal re-init preserves the existing auth/PII secrets and independently mints only a missing or shipped-placeholder privacy alias key or triage token. The NER template, admin posture, operator guide, and binary agree that the `PII_NER_ENABLED` compiled default is `true`.

Plan alias-key rotation as a mapping-loss event. Drain workflows, rotate explicitly, restart, and require new scope IDs. Strict buffering also changes response timing: clients still receive native-compatible stream framing, but the first strict byte is delayed until complete validation.

### Rollback

An older binary does not understand the privacy headers or issue strict receipts. Before rollback, drain or fail active strict workflows; rollback clears in-memory scopes. Keep new privacy keys in `overrides.env` for a future forward upgrade, but do not interpret their presence as protection while the older process runs. Because an older binary does not understand the privacy headers, strict consumers must fail closed on the missing receipt. Never point them directly at a worker as a workaround.
