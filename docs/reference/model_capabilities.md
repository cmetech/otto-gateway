# Model Capability Discovery — `GET /v1/model-capabilities`

**Status:** Authoritative reference for the endpoint contract, the on-disk
capability registry, and the maintenance procedure for keeping both honest.

**Source of truth:** this doc describes what the code in
`internal/adapter/openai/modelcaps.go`, `internal/registry/registry.go`, and
`internal/registry/registry.json` actually does. Where the original design
spec (`docs/superpowers/specs/2026-07-16-model-capabilities-endpoint-design.md`)
differs from the shipped code, the code wins and the difference is called out
here.

**Why this endpoint exists:** `GET /v1/models` tells a client which model IDs
exist. It says nothing about what each model can *do*. Before this endpoint,
the only capability signals available were agent-wide (`initialize`'s
`promptCapabilities`) or a hardcoded Ollama-compat stub (`/api/show`) — neither
is trustworthy per-model evidence (see §3 and §4). `GET /v1/model-capabilities`
fuses the live Kiro model list with a source-controlled, evidence-backed
registry so a client can ask "has `tools` been verified as supported for
`claude-opus-4.8`?" and get an honest three-state answer instead of a guess.

## 1. The endpoint contract

`GET /v1/model-capabilities` is a Gateway-owned endpoint — **not** part of the
OpenAI spec — mounted on the same `/v1` prefix as `/v1/models` and
`/v1/chat/completions` (`internal/adapter/openai/adapter.go` `RegisterRoutes`).

### Envelope

```json
{
  "object": "list",
  "registry_revision": "sha256-<hex>",
  "generated_at": "2026-07-16T12:00:00Z",
  "data": [ ... ]
}
```

| Field | Type | Meaning |
|---|---|---|
| `object` | string | Always `"list"`. |
| `registry_revision` | string | `"sha256-" + hex(sha256(registry.json bytes))` — a deterministic content hash of the embedded registry file. Two builds with byte-identical `registry.json` produce the same value; any edit to the file changes it. |
| `generated_at` | string | RFC3339, UTC, `time.Now()` at request time. **This is the only non-deterministic field in the response** — repeat the same request against the same registry and live catalog and every other field is identical. |
| `data` | array | Entries, `auto` first (see below), then explicit models in live-catalog order. |

### Entry fields

```json
{
  "id": "claude-opus-4.8",
  "name": "Claude Opus 4.8",
  "available": true,
  "selection_mode": "explicit",
  "capabilities": {
    "completion": "supported",
    "tools": "supported",
    "vision": "supported",
    "reasoning": "supported"
  },
  "evidence": {
    "completion": {
      "source": "kiro_declared",
      "reference": "kiro session/new availableModels (live catalog)",
      "verified_at": "2026-07-16",
      "notes": "Listed as a selectable chat model in the live Kiro catalog."
    }
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Exact Kiro model ID (or `"auto"`). |
| `name` | string | Display name: live Kiro name wins, falls back to the registry `name`, falls back to `id`. |
| `available` | bool | Whether the model is present in this response. In the current implementation this is always `true` — a model that isn't in the live catalog (and isn't `auto`) is never emitted at all rather than emitted with `available: false` (see §2). The field exists for forward compatibility; treat it as "present in the live catalog right now," not as an independent liveness probe. |
| `selection_mode` | string | `"automatic"` for the single `auto` entry, `"explicit"` for every named model. |
| `capabilities` | object | Exactly four keys, always present: `completion`, `tools`, `vision`, `reasoning`. Each value is one of the three capability states below. |
| `evidence` | object | Keyed **only** by capabilities whose state is `supported` or `unsupported` — an all-`unknown` model (including `auto`) has `evidence: {}`. See §5 for the evidence object shape. |

### The three capability states

`canonical.CapabilityState` (`internal/canonical/modelcaps.go`) is a closed,
machine-readable three-state enum — deliberately not a bool, because "we
haven't checked" is a distinct, honest answer from "checked and it doesn't
work":

- `"supported"` — verified evidence says the model does this.
- `"unsupported"` — verified evidence says the model does not do this.
- `"unknown"` — no verified evidence either way. This is the default for
  anything not explicitly registered, and it is the only legal state for a
  live-but-unregistered model.

### `auto` is always first

The synthetic `auto` entry (`id: "auto"`, `name: "Automatic"`) is always the
first element of `data`, with `available: true`, `selection_mode:
"automatic"`, all four capabilities `"unknown"`, and empty evidence. Kiro's
live catalog also lists an `auto` entry (mirroring `/v1/models`' pre-existing
duplicate); `Enrich` drops that live copy and emits exactly one normalized
`auto` entry instead of surfacing the live name or a duplicate (`registry.Enrich`,
`internal/registry/registry.go`). Because "automatic" selection defers the
actual model choice to Kiro at request time, no fixed capability claim can be
verified for it up front — `selection_mode: "automatic"` is set unconditionally
for this one entry, never computed from a capability check.

## 2. Availability vs. capability verification

The endpoint fuses two independent sources with two independent jobs:

- **Availability** = the live Kiro catalog (`pool.Models()`, the same source
  `/v1/models` reads). It answers "does this model exist right now."
- **Capability verification** = the embedded registry
  (`internal/registry/registry.json`). It answers "has anyone verified what
  this model can do."

The fusion rule (`(*Registry).Enrich`) is asymmetric by design:

- **A live-but-unregistered model is returned, never omitted**, with all four
  capabilities `"unknown"` and empty evidence. A model that exists but hasn't
  been researched yet is still a real, selectable model — the client needs to
  see it, just honestly labeled as unverified. This is covered by
  `TestModelCapabilities_UnknownModel` (`internal/adapter/openai/modelcaps_test.go`)
  and the registry-level "a live unregistered model becomes all-unknown" test.
- **A registry model absent from the live catalog is omitted, never shown as
  stale-available.** If Kiro stops offering a model, its registry entry (if
  one still exists) is silently dropped from the response rather than
  reported with capabilities the client could act on for a model it can't
  actually select. This is why "retire a removed model" (§6) is a cleanup
  step, not a correctness requirement — a stale entry can't leak into a
  response, but it's dead weight in the source file.

Net effect: `data` (beyond `auto`) is always a subset of the live catalog. The
registry can only narrow what's known about an available model; it can never
add a model that isn't actually there.

## 3. Why agent-wide `promptCapabilities` cannot prove per-model support

During the ACP `initialize` handshake, the agent reports
`agentCapabilities.promptCapabilities.{image,audio,embeddedContext}`
(`internal/acp/client.go`, captured once per client connection). This is
negotiated **once, for the whole agent connection** — it says whether the
agent-as-a-whole can accept image/audio/embedded-context content in a prompt
turn, not whether any particular selectable model (Claude Opus, GPT-5.6-sol,
DeepSeek, …) actually processes that content correctly. Kiro exposes many
models behind one ACP connection; `promptCapabilities` can't distinguish
between them. Treating it as per-model vision or tool-use evidence would be
laundering an agent-wide signal into a claim about a specific model — exactly
what this registry's evidence discipline (§5) exists to prevent. It plays no
role in populating `internal/registry/registry.json`.

## 4. `/api/show`'s legacy `capabilities` field is not verified evidence

`POST /api/show` (`internal/adapter/ollama/handlers.go`, `handleShow`) returns
a hardcoded `"capabilities": ["completion", "tools"]` for **every** model that
passes `modelExists` — the same two-element list regardless of which model was
requested. This is an Ollama-API-compatibility surface preserved for LangFlow
byte-compatibility; it predates the registry and is not derived from it. Do
not read it as, and do not document it as, verified per-model capability
evidence. If a client needs an honest per-model answer, it must call
`GET /v1/model-capabilities` instead. `/api/show`'s wire shape is intentionally
unchanged by this endpoint (no LangFlow migration required).

## 5. Registry schema, location, and evidence policy

**Location:** `internal/registry/registry.json`, embedded into the binary via
`//go:embed` and owned exclusively by `internal/registry/registry.go`. No
network fetch, no runtime file I/O — the registry ships inside the binary and
is validated once at startup (`registry.Load()`, called from
`cmd/otto-gateway/main.go`). A validation failure is a fatal boot error, never
a runtime 500 — bad registry content cannot ship.

**On-disk shape:** a **JSON array** of entries, not an object keyed by ID.
This is deliberate: `encoding/json` decoding into a Go map silently keeps the
last value on a duplicate key, which would defeat duplicate-ID detection. The
array is validated and indexed into an in-memory `map[id]entry` for exact-ID
lookup.

```json
[
  {
    "id": "claude-opus-4.8",
    "name": "Claude Opus 4.8",
    "capabilities": {
      "completion": "supported",
      "tools": "supported",
      "vision": "supported",
      "reasoning": "supported"
    },
    "evidence": {
      "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." },
      "tools":      { "source": "vendor_documentation", "reference": "https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview.md", "verified_at": "2026-07-16", "notes": "..." },
      "vision":     { "source": "vendor_documentation", "reference": "https://platform.claude.com/docs/en/build-with-claude/vision.md", "verified_at": "2026-07-16", "notes": "..." },
      "reasoning":  { "source": "vendor_documentation", "reference": "https://platform.claude.com/docs/en/about-claude/models/overview.md", "verified_at": "2026-07-16", "notes": "..." }
    }
  }
]
```

**Required keys per entry (`load` in `internal/registry/registry.go` rejects
anything else at startup):**

- `id` — non-empty, unique across the file (duplicate IDs fail to load).
- `name` — display name; falls back to at render time only if the live catalog
  didn't supply one either.
- `capabilities` — an object with **exactly** the four keys
  `completion`, `tools`, `vision`, `reasoning` (`canonical.RequiredCapabilities`),
  each set to one of `"supported"`, `"unsupported"`, `"unknown"`. Extra keys,
  missing keys, or an out-of-set state value all fail validation.
- `evidence` — an object whose key set must **exactly equal** the set of
  capabilities in `capabilities` whose state is `supported` or `unsupported`.
  This is a single invariant that catches two mistakes at once: a
  `supported`/`unsupported` capability with no evidence, and evidence attached
  to a capability that's `unknown` (or that doesn't exist). `unknown`
  capabilities must carry **no** evidence entry.

**Evidence object fields:**

- `source` — one of the three closed source types (below); anything else
  fails validation.
- `reference` — non-empty string (a URL or a description of the primary
  source); empty fails validation.
- `verified_at` — a date string that must parse as `YYYY-MM-DD`
  (`time.Parse("2006-01-02", ...)`); anything else fails validation.
- `notes` — optional free-text (`omitempty` on the wire).

**The three evidence source types** (`validSources` in
`internal/registry/registry.go`):

- `kiro_declared` — the model appears in Kiro's live catalog
  (`session/new availableModels`). Used exclusively for `completion`: showing
  up as a selectable chat model in Kiro is Kiro declaring completion
  usability.
- `vendor_documentation` — official documentation from the underlying model
  vendor, unambiguously mapped to the exact Kiro model ID. Used for
  `tools`/`vision`/`reasoning` on models (mostly the `claude-*` family) where
  the exact ID maps to a documented vendor model.
- `controlled_probe` — reserved for a future maintainer-only synthetic probe.
  No probe runtime exists yet; do not use this source type until one does.

**Forbidden as evidence:** blogs, marketing pages, model-name intuition, or an
LLM's memory of a model's specs. If official documentation cannot be
unambiguously mapped to the exact Kiro model ID, the capability stays
`unknown` — it is never acceptable to infer a state from a similarly-named
model.

## 6. Maintenance procedures

### Add a new exact model ID

1. Get the **exact** live ID as Kiro reports it (`GET /v1/models`, or
   `session/new`'s `availableModels`) — case-sensitive, no fuzzy matching.
   Adding a registry entry for an ID that never appears in the live catalog
   has no effect on the endpoint response (it will simply be omitted per §2)
   and is dead weight in the file.
2. Set `completion: "supported"` with `source: "kiro_declared"` and
   `reference: "kiro session/new availableModels (live catalog)"` — appearing
   in the selectable catalog is the evidence for `completion`.
3. Research `tools`, `vision`, `reasoning` against **official vendor
   documentation** only. Only set `supported`/`unsupported` when the doc
   unambiguously names this exact model/version. If you can't find an
   unambiguous mapping, leave the capability `unknown` and add **no**
   evidence entry for it — do not guess.
4. Set `verified_at` to the date you actually checked the source (`YYYY-MM-DD`).
5. Add the entry to the `registry.json` array (keep it valid JSON — the file
   is decoded with `DisallowUnknownFields`, so typo'd keys fail loudly).
6. Run `go test ./internal/registry/... ./internal/adapter/openai/...` to
   confirm the file still loads and the endpoint renders it correctly.

### Retire a removed model

Delete its object from the `registry.json` array. Nothing else is required —
if Kiro no longer lists the model, `Enrich` already omits any lingering
registry entry from the response (§2), so a forgotten entry can't leak stale
capability claims. Removing it is still good hygiene: it keeps the file's
content — and therefore `registry_revision` — reflecting only models that
matter, and avoids confusing a future maintainer doing the drift check below.

### Update evidence or verification dates

When a vendor changes its docs, or you re-verify an existing claim, update
that capability's `source`/`reference`/`notes` and bump `verified_at` to the
date of re-verification. If a capability's **state** changes (e.g.
`unknown` → `supported`), you must add a matching evidence entry in the same
edit — the "evidence keys equal the supported/unsupported set" rule (§5) means
a state change without a matching evidence change (in either direction) fails
`Load()` at startup, not silently at request time.

### Validate registry drift against a live Kiro catalog

1. Query the live catalog: `GET /v1/models` (or `/api/tags`, which exposes the
   same ID set) against a gateway pointed at the target Kiro deployment.
2. Diff those IDs against the `id` values in `internal/registry/registry.json`.
3. **Live IDs not in the registry** → these already render correctly as
   all-`unknown` in `/v1/model-capabilities` (no correctness bug), but they're
   a research backlog: add entries per "Add a new exact model ID" above.
4. **Registry IDs not in the live list** → already omitted from the endpoint
   response (no correctness bug), but are candidates for retirement per
   "Retire a removed model" above.

## 7. Auth posture

`GET /v1/model-capabilities` uses **IP-allowlist only, no bearer-token
requirement** — the identical middleware chain applied to `GET /v1/models`
(`internal/server/server.go`, same `r.Use(auth.IPAllowlist(...))` group; both
endpoints are covered by the accepted risk `T-8-AUTH-BYPASS`). Both are
read-only catalog stubs: neither reaches the ACP engine, so bearer-token
gating was intentionally not restored for them (Phase 8 PLUG-03). Operators
who need bearer auth on these endpoints specifically would need to add it
downstream — it is not on by default.

## 8. `unknown` models stay visible but shouldn't be picked for capability-sensitive work

An unregistered or partially-registered model is never hidden from
`/v1/model-capabilities` — hiding it would make the endpoint lie about what
Kiro actually offers, and would silently break any client relying on the
model-ID set matching `/v1/models`. Instead it's shown honestly with
`"unknown"` for whatever hasn't been verified. Clients building capability-
aware model pickers should treat `"unknown"` as "do not route capability-
sensitive work here" — e.g. don't select an `unknown`-for-`vision` model for
an image-attachment turn — while still letting the model appear in a general
model list and be selected explicitly by ID for plain completion. `"unknown"`
is a caller-facing signal, not a Gateway-side selection filter: the Gateway
does not block requests to unknown-capability models; it only reports the
state.

## 9. The maintenance rule

> Adding or changing a Kiro model capability declaration requires an exact model
> ID, evidence, a verification date, registry validation, endpoint contract
> tests, and a review of whether the model is still present in the live Kiro
> catalog.

This is enforced by schema validation (`internal/registry/registry.go`,
fail-fast at `Load()`) and by the endpoint/registry test suites
(`internal/registry/*_test.go`, `internal/adapter/openai/modelcaps_test.go`),
not by this document alone — a registry edit that violates it fails at
startup or in CI, not silently at request time.

## 10. Client integration notes (consumer contract stability)

The primary consumer (the Hermes client) validates this response **strictly** and
joins it against `GET /v1/models` by exact model ID. Two properties of that
coupling are load-bearing and easy to break accidentally:

- **Additive capability keys are a BREAKING change for strict clients.** A
  conformant strict consumer may reject the *entire* response if it sees a
  capability key outside the documented set (`completion`, `tools`, `vision`,
  `reasoning`) — turning "one new capability" into "every model reads as
  unverified" on that client. This gateway emits exactly those four keys
  (`internal/canonical/modelcaps.go`, `RequiredCapabilities`). **Do not add a
  fifth capability key without coordinating a client release first**; treat it as
  a versioned contract change, not an additive one. (The client-side hardening is
  to ignore unknown keys rather than hard-reject, but the gateway must not assume
  every consumer has adopted it.)

- **Duplicate IDs are rejected whole-catalog by strict clients.** `Enrich` emits
  each explicit live ID **at most once** (first occurrence wins) precisely so a
  Kiro double-listing cannot surface a duplicate ID that blanks the client's
  catalog. Kiro is known to double-list `auto` on `/v1/models`; the capability
  endpoint normalizes both `auto` and any repeated explicit ID.

- **Capability freshness vs. availability freshness.** Availability comes from the
  live catalog and is authoritative per request; **capability evidence may be
  cached by the client** (the Hermes client caches this catalog for up to ~1h,
  keyed by provider/base-URL/credentials, while fetching `/v1/models` fresh). A
  registry change (which changes `registry_revision`) can therefore take up to the
  client's cache TTL to be observed — always in the safe direction (a newly
  verified model reads as `unknown`/over-blocked until the cache refreshes, never
  over-permitted). If you ship a registry update that must be observed
  immediately, expect a lag on clients that cache.
