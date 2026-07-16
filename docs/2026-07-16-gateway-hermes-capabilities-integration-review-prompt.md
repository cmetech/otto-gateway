# LLM prompt — cross-repo integration review: gateway `model-capabilities` ↔ Hermes client

> Paste everything below the line into a coding session that can **read both repos** on this
> machine. This is a **read-only integration review** of a producer (the OTTO Gateway) and its
> consumer (the Hermes client) for one HTTP contract. Do **not** modify either repo. Your job is
> to prove — or disprove — that the two sides agree on the wire contract and both honor the
> shared spec, and to surface any way the integration breaks in production.

---

You are reviewing the integration of a new gateway endpoint, **`GET /v1/model-capabilities`**,
across two repositories:

- **Producer (gateway, Go):** `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway`
- **Consumer (Hermes client, Python + TS):** `/Users/coreyellis/code/github.com/cmetech/otto_hermes/hermes-agent`

The endpoint fuses the **live Kiro model catalog** (availability) with an **embedded,
evidence-backed registry** (verified capabilities), using three states —
`supported` / `unsupported` / `unknown`. The client consumes it to decide which models it may
offer for each configuration slot. The whole design rests on two invariants: **availability and
capability are separate gateway-owned facts joined by exact model ID**, and **`unknown` means
unverified — never implicitly supported or unsupported.**

Ground every claim in the actual source and its tests on both sides. Do not trust this prompt's
summaries or either repo's own comments as proof — read the code and run the tests.

## The shared spec (read both sides' specs first)

**Gateway side:**
- Design spec: `otto-gateway/docs/superpowers/specs/2026-07-16-model-capabilities-endpoint-design.md`
- Implementation plan: `otto-gateway/docs/superpowers/plans/2026-07-16-model-capabilities-endpoint.md`
- Client-facing contract doc: `otto-gateway/docs/reference/model_capabilities.md`

**Client side** (the client has NO `docs/superpowers/specs` file for this; its contract lives here):
- `hermes-agent/AGENTS.md` §"Gateway model inventory and auth invariants" (~lines 849-865) — the
  normative client contract. Read it verbatim.
- `hermes-agent/website/docs/developer-guide/gateway-internals.md` (~18-93; HTTP→status table ~59-66)
- `hermes-agent/website/docs/user-guide/configuring-models.md` (~50-99; states ~70-76; the
  required-capability-per-slot matrix ~79-87; reasoning-is-not-required ~89-91)
- `hermes-agent/website/docs/developer-guide/model-provider-plugin.md`,
  `hermes-agent/website/docs/reference/model-catalog.md`

**First deliverable:** confirm the two specs describe the *same* contract (states, capability
keys, exact-ID join, `auto` semantics, `unknown` handling). Flag any place the client spec and
the gateway spec disagree in intent.

## The gateway wire contract (producer)

Produced by `otto-gateway/internal/adapter/openai/modelcaps.go`
(`modelCapabilityList` / `modelCapabilityEntry` / `evidenceWire`, and `capabilityCatalogToWire`),
built by `otto-gateway/internal/registry/registry.go` (`Enrich`), seeded from
`otto-gateway/internal/registry/registry.json`, routed in
`otto-gateway/internal/adapter/openai/adapter.go` (`RegisterRoutes`), wired in
`otto-gateway/cmd/otto-gateway/main.go`, middleware in `otto-gateway/internal/server/server.go`.

Shape:
```json
{
  "object": "list",
  "registry_revision": "sha256-<hex>",
  "generated_at": "<RFC3339 UTC>",
  "data": [
    { "id": "auto", "name": "Automatic", "available": true, "selection_mode": "automatic",
      "capabilities": {"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown"},
      "evidence": {} },
    { "id": "<model>", "name": "<display>", "available": true, "selection_mode": "explicit",
      "capabilities": {"completion":"supported","tools":"...","vision":"...","reasoning":"..."},
      "evidence": {"<cap>": {"source":"...","reference":"...","verified_at":"YYYY-MM-DD","notes":"..."}} }
  ]
}
```
Gateway guarantees to verify: `auto` first; only live models in `data`; unregistered live model →
all-`unknown`; registry-only model omitted; every entry has all four capability keys; `available`
is always `true` for emitted entries; evidence present only for `supported`/`unsupported` caps;
auth is **IP-allowlist only, no bearer** (same middleware as `/v1/models`).

## The client parser contract (consumer)

Enforced by `hermes-agent/hermes_cli/model_capabilities.py`:
- `fetch_model_capability_catalog` (~298-413): GETs `<base_url>/model-capabilities`, sends
  `Authorization: Bearer <api_key>` **only when the key is non-empty**, caches to
  `~/.hermes/model_capabilities_cache.json` (TTL 3600s).
- `_parse_catalog_payload` (~90-158) — the strict validator. It **rejects the entire response**
  (`capability-response-invalid`) unless: `object == "list"`; `registry_revision` and
  `generated_at` are **strings**; `data` is a list; each `id` is a non-empty string and unique;
  `selection_mode ∈ {"automatic","explicit"}`; `available` is a **bool**; `selection_mode ==
  "explicit"` implies `available is True`; `capabilities` contains only keys in
  `("completion","tools","vision","reasoning")` with values in
  `("supported","unsupported","unknown")` (missing keys default to `"unknown"`); every `evidence`
  field value is a string.
- Type aliases + dataclasses (~19-53): `CapabilityState`, `SelectionMode`, `CAPABILITY_KEYS`,
  `VerifiedModelCapability`, `ModelCapabilityCatalog`.
- `join_live_model_capabilities` (~461-494): prepends its OWN synthesized `auto`; joins the live
  `/v1/models` IDs against the catalog by exact ID; synthesizes an `explicit`/all-`unknown` entry
  for any live ID missing from the catalog; computes `mismatch_count` (symmetric difference of
  live-explicit vs catalog-explicit IDs).
- HTTP status mapping (~359-397): 401/403 → `authentication-required`; 404 →
  `gateway-upgrade-required`; 5xx/timeout/conn-error → `gateway-unreachable`; bad schema →
  `capability-response-invalid`.

Eligibility policy — `hermes-agent/hermes_cli/model_eligibility.py`:
- `REQUIRED_CAPABILITIES` (~25-32): `main`=(completion,tools), `fallback`=(completion,tools),
  `auxiliary`=(completion,), `vision`=(completion,vision), `moa-reference`=(completion,),
  `moa-aggregator`=(completion,tools).
- `evaluate_model_eligibility` (~91-165): `auto`+main → eligible before catalog checks; a required
  capability is acceptable **only if `state == "supported"`**; `unknown` and `unsupported` both
  fail a *new* assignment (with distinct messages); existing exact assignments are grandfathered
  (`_ineligible`, ~68-88).
Provider profile (base URL, auth, path): `hermes-agent/plugins/model-providers/loop24/__init__.py`
(`base_url="http://127.0.0.1:18080/v1"`, env `OTTO_BASE_URL`/`OTTO_API_KEY`,
`supports_unauthenticated=True`, `model_capabilities_path="model-capabilities"`).
Inventory enrichment: `hermes-agent/hermes_cli/inventory.py` (`_apply_verified_provider_capabilities`
~339-402; reasoning UI toggle ~391-393). Desktop types:
`hermes-agent/apps/desktop/src/types/hermes.ts` (~247-306).

## Field-by-field alignment — VERIFY each row against BOTH sides' code (do not trust the table)

| Wire field | Gateway emits | Client requires | Check |
|---|---|---|---|
| `object` | `"list"` | must equal `"list"` | exact match? |
| `registry_revision` | `"sha256-…"` string | must be a **string** | is it ever non-string (e.g. null)? |
| `generated_at` | RFC3339 string | must be a **string** | ditto |
| `data` | array | must be a list | — |
| `data[].id` | model id / `"auto"` | non-empty, unique | can the gateway ever emit an empty or duplicate id? |
| `data[].name` | always set | optional, defaults to id | — |
| `data[].available` | always `true` | must be **bool**; explicit⇒true | does the gateway ever omit it or emit non-bool? |
| `data[].selection_mode` | `"automatic"`/`"explicit"` | must be exactly those | any third value possible? |
| `data[].capabilities` | exactly 4 keys | only those 4 keys; extra key ⇒ **whole-response rejection** | forward-compat: what happens if the gateway adds a 5th key? |
| `capabilities.*` values | 3 states | 3 states | any other value possible? |
| `data[].evidence.<cap>.*` | string fields, `notes` omitempty | every value must be a **string** | any non-string evidence value possible? |

## Targeted integration risks — investigate each and give a verdict with evidence

1. **Strict-schema forward-compat coupling.** The client rejects the *entire* catalog if it sees
   any capability key outside the four, or any non-string `registry_revision`/`generated_at`, or a
   non-bool `available`. Confirm the gateway can never emit those today, AND assess the coupling:
   if the gateway later adds a capability key (e.g. `audio`), does every explicit model become
   ineligible on the client (only `auto`/main survives)? Is that failure mode acceptable, and is
   it documented on both sides? Recommend whether the client should ignore-unknown-keys instead of
   hard-rejecting.

2. **Double-`auto` in `/v1/models`.** The gateway's `/v1/models` emits `auto` **twice** (Kiro lists
   it AND the adapter prepends one), while `/v1/model-capabilities` emits `auto` **once** (deduped).
   The client reads live IDs from `/v1/models` (`profile.fetch_models`) and prepends its own `auto`
   in `join_live_model_capabilities`. Trace what the client does with a live list that contains
   `["auto","auto",<models>]`: does it dedupe, double-count, or mis-compute `mismatch_count`? Is
   there a user-visible artifact (a duplicated `auto` row, a spurious mismatch warning)? This is
   the most likely real-world glitch — chase it to a definite yes/no.

3. **Bearer sent to an IP-allowlist-only endpoint.** The gateway endpoint does **not** validate a
   bearer (it never reaches the engine's AuthHook); it is gated by IP-allowlist only. The client
   attaches `Authorization: Bearer <OTTO_API_KEY>` whenever that env is set (it does not special-case
   this endpoint). Confirm: (a) the gateway returns 200 for a request carrying an *ignored* bearer;
   (b) when the gateway is launched with `AUTH_TOKEN` (so the user sets `OTTO_API_KEY`), the client
   still gets 200 (no 401/403 surprise); (c) if `ALLOWED_IPS` blocks the client, the gateway returns
   the status the client maps to `authentication-required` — verify the actual status code the
   gateway's IP-allowlist middleware returns and that the client's 401/403 handling matches.

4. **Honest-`unknown` → unassignable models (product consequence).** The gateway seeds every
   non-Claude model (`gpt-5.6-sol/terra/luna`, `deepseek-3.2`, `minimax-m2.5/m2.1`, `glm-5`,
   `qwen3-coder-next`) as `completion:supported` but `tools/vision/reasoning:unknown`, and
   `claude-sonnet-4` as `tools:supported` / `vision:unknown` / `reasoning:unknown`. The client's
   policy blocks `unknown` for **new** assignments. Enumerate, per slot, which live models become
   *unassignable* under this policy (e.g. can any GPT tier be assigned to `main`, which requires
   `tools`? can any non-Claude model be assigned to `vision`?). Confirm this is the intended spec
   behavior on both sides, and flag it as a **product/UX consequence to surface to the user** (a
   large fraction of the live catalog is main/vision/tools-ineligible until verified), not a bug.

5. **Version-skew / graceful degradation.** Verify the full skew matrix behaves per spec:
   old gateway without the endpoint → 404 → client `gateway-upgrade-required` (does the client still
   let `auto`/main work?); gateway present but catalog empty → `auto`-only → client `catalog-empty`;
   gateway unreachable → client `gateway-unreachable` with `auto`/main still selectable. Confirm the
   client never hard-fails model selection when only the *capability* endpoint is degraded.

6. **Cache coherence.** The client caches the catalog for 3600s keyed by
   (provider, base_url, credential fingerprint). Assess whether a stale cache can misrepresent
   availability or capability after a gateway model set changes (the gateway's `registry_revision`
   changes when the embedded registry changes, but the *live catalog* can change without it).
   Does the client key or invalidate on anything that reflects live-catalog drift, or can a 1-hour
   stale cache offer a model the gateway no longer lists? Note the blast radius.

7. **Exact-ID join fidelity.** Both sides must join on the exact Kiro ID with no normalization.
   Confirm neither side lowercases, trims, or fuzzy-matches IDs (the gateway does exact-map lookup;
   verify the client's join is also exact). A near-miss (`claude-opus-4.8` vs `claude-opus-4-8`)
   must NOT match — confirm both sides agree on the literal ID strings actually in play.

## Run both sides' tests

Gateway (Go):
```
cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway
go test ./internal/registry/ ./internal/adapter/openai/
GW_E2E=1 PII_ENCRYPT_KEY=<any-64-hex> go test -tags e2e ./tests/e2e/ -run ModelCapabilities -v
```
Client (Python — use the repo's venv/pytest):
```
cd /Users/coreyellis/code/github.com/cmetech/otto_hermes/hermes-agent
pytest tests/hermes_cli/test_model_capabilities.py tests/hermes_cli/test_model_eligibility.py \
       tests/hermes_cli/test_inventory.py tests/hermes_cli/test_web_server.py -q
```
Desktop (TS, if you can): `apps/desktop/src/app/settings/model-settings.test.tsx`.

If you can run a live loopback check, boot the gateway and curl `/v1/model-capabilities`, then feed
that exact JSON to the client's `_parse_catalog_payload` to prove the real bytes parse cleanly.

## Deliverable

1. **Spec agreement:** do the gateway spec and the client contract (AGENTS.md + website docs)
   describe the same thing? Any divergence in intent.
2. **Alignment matrix:** the field-by-field table above, each row marked ALIGNED / MISMATCH /
   RISK, with file:line evidence from BOTH repos.
3. **Risk findings (1-7):** a verdict on each, classified:
   - **Blocker** — a wire mismatch that makes the client reject or mis-handle a valid gateway
     response, an auth/status mismatch that breaks fetch, or an exact-ID join that silently
     mismatches.
   - **Important** — a real degradation or coupling that will bite on a plausible change
     (schema forward-compat, double-auto artifact, stale-cache availability drift).
   - **Note** — an intended-but-worth-surfacing product consequence (unknown-blocks-assignment
     scope), or a documentation gap between the two repos.
4. **Integration go/no-go:** would you ship these two together as-is? If not, name exactly what to
   change and on which side. Do not modify either repo — this is a read-and-verify review; a
   throwaway scratch script to parse real gateway bytes through the client parser is fine.
