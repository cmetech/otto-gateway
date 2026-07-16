# Verified per-model capability discovery — `GET /v1/model-capabilities`

- **Status:** Proposed (design) — awaiting review
- **Date:** 2026-07-16
- **Author:** Gateway team (via handoff `docs/2026-07-16-gateway-verified-model-capabilities-prompt.md`)
- **Supersedes/relates:** builds on `docs/superpowers/specs/2026-07-14-model-discovery-resilience-design.md` (model-catalog self-healing). Does not change that behavior.

## 1. Problem

The Co-Worker client uses the Gateway as an OpenAI-compatible provider and reads the
flat model list from `GET /v1/models`. It has no honest way to tell which listed models
are suitable for a given job — main-agent use, text-only auxiliary work, vision, tool
use, or reasoning controls.

The only capability signal that exists today is agent-wide, not per-model:

- Kiro's `session/new` reports only `{modelId, name}` per model — **zero per-model
  capability metadata** (`internal/acp/client.go:242`).
- `initialize` reports `agentCapabilities.promptCapabilities.{image,audio,embeddedContext}`,
  which is **agent-wide** (`internal/acp/client.go:191-207`). Treating it as per-model
  evidence would be a lie: it does not prove any individual model supports vision.
- `/api/show` returns a hardcoded `capabilities: ["completion","tools"]` for every model
  (`internal/adapter/ollama/handlers.go:743`). This is a legacy Ollama-compatibility
  surface, **not** verified per-model evidence, and must not be documented as such.

We need an honest, auditable, per-model capability catalog the client can use to offer
only models whose required capability has been **verified**. Unknown must remain unknown.

## 2. Goals

- Add a Gateway-owned `GET /v1/model-capabilities` endpoint fusing:
  1. the **live Kiro catalog** (authoritative for *availability*), and
  2. an **embedded, source-controlled registry** (authoritative for *what has been
     verified*).
- Machine-readable three-state capabilities: `supported`, `unsupported`, `unknown`.
- Strict evidence discipline: no name-guessing, no fuzzy-matching, no laundering
  agent-wide capabilities into per-model claims. Unregistered live models render
  all-`unknown`. Registry-only stale models are never shown as available.
- Deterministic response apart from `generated_at`.
- Keep every existing surface byte-compatible.

## 3. Non-goals

- **No change to `GET /v1/models`** response shape or model set.
- **No change to `/api/tags` / `/api/show`** wire shapes or their equality relationship.
- **No request-time probing, no background quota consumer, no network dependency, no
  cgo.** The embedded registry is the baseline.
- **No general live-probing service.** The schema reserves `source: controlled_probe` for
  a *future* maintainer-only tool, but no probe runtime is built here.
- No change to `canonical.ModelInfo`, the `ModelCatalog.Models()` seam, model-discovery
  self-healing, or `auto` selection semantics.
- The pre-existing duplicate `auto` in `/v1/models` (Kiro lists `auto` *and* the adapter
  prepends one) is **out of scope** and left untouched on that endpoint.

## 4. Verified current behavior (baseline)

| Area | Reality | Reference |
|---|---|---|
| Kiro per-model data | `{modelId, name}` only; no capability metadata | `internal/acp/client.go:229-247` |
| Prompt capabilities | Agent-wide, from `initialize` | `internal/acp/client.go:191-207` |
| Canonical model type | `ModelInfo{ID, Name}` — `Name` currently dropped by both adapters | `internal/canonical/model.go:5-12` |
| Catalog seam | `ModelCatalog interface { Models() []canonical.ModelInfo }` | `internal/adapter/openai/adapter.go:110-112` |
| Catalog cache | In-memory snapshot, no TTL; retry+backoff + degrade-not-abort + singleflight lazy self-heal | `internal/pool/pool.go` |
| `auto` | Synthetic, prepended at render time; not stored in pool | `internal/adapter/openai/render.go:43-61` |
| `/v1/models` auth | **IP-allowlist only, no bearer** (accepted risk `T-8-AUTH-BYPASS`) | `internal/server/server.go:408-428` |
| `go:embed` precedent | `internal/admin/assets.go:17`; `internal/embed/` is an empty placeholder | — |

**Handoff discrepancies reconciled here:**

- The handoff's evidence source #1 ("Kiro-declared per-model metadata *if the installed
  Kiro version provides it*") yields nothing — Kiro provides no per-model metadata. Real
  seeding relies on source #2 (vendor documentation), else `unknown`.
- The handoff says use "the same **authentication** and IP-allowlist middleware policy as
  `/v1/models`." `/v1/models` has **no authentication** — IP-allowlist only. We honor the
  literal instruction ("same as `/v1/models`"): **IP-allowlist only, no bearer.**

## 5. Architecture

### 5.1 Package layout

Respects the existing `canonical` (tag-free) / adapter (wire-tagged) / consumer-defined-seam
boundaries.

- **`internal/canonical`** — new file (e.g. `modelcaps.go`), pure tag-free types:
  - `type CapabilityState string` with constants `CapUnknown`, `CapSupported`,
    `CapUnsupported` (values `"unknown"`, `"supported"`, `"unsupported"`).
  - `type Evidence struct { Source, Reference, VerifiedAt, Notes string }`.
  - `type ModelCapability struct { ID, Name string; Available bool; SelectionMode string;
    Capabilities map[string]CapabilityState; Evidence map[string]Evidence }`.
  - `type CapabilityCatalog struct { RegistryRevision string; GeneratedAt time.Time;
    Entries []ModelCapability }`.
  - Imports nothing under `internal/` (boundary preserved).

- **`internal/registry`** — new package, sibling to `internal/pool`. Sole owner of registry
  logic; **no HTTP**.
  - Embeds `registry.json` via `go:embed` (mirrors `internal/admin/assets.go`).
  - `Load() (*Registry, error)` — parses + validates the embedded bytes once; returns an
    error on any validation failure (fail-fast).
  - Computes `RegistryRevision = "sha256-" + hex(sha256(embeddedBytes))`.
  - `Enrich(live []canonical.ModelInfo, now time.Time) canonical.CapabilityCatalog` —
    the fusion logic (§7).

- **`internal/adapter/openai`**
  - New consumer-defined seam:
    `type ModelCapabilityCatalog interface { ModelCapabilities() canonical.CapabilityCatalog }`.
    Adapter still imports only `canonical` (TRST-04 preserved).
  - `handleModelCapabilities` handler.
  - Wire structs + canonical→wire mapping in `render.go` (same pattern as
    `modelList`/`modelInfo`).
  - Route registration: `r.Get("/model-capabilities", a.handleModelCapabilities)` in
    `RegisterRoutes` (`adapter.go:186-190`).

- **`cmd/otto-gateway/main.go`** — a small combiner implementing the seam, holding the
  `*pool.Pool` (live catalog via `Models()`) and the loaded `*registry.Registry`:
  `ModelCapabilities()` returns `reg.Enrich(pool.Models(), time.Now())`. Injected into the
  OpenAI adapter config. Registry `Load()` runs at startup; a load error is fatal
  (a build/ship error surfaced as a boot failure, never a runtime 500).

### 5.2 Data flow

```
GET /v1/model-capabilities
  → IPAllowlist middleware (same chain as /v1/models)
  → handleModelCapabilities
  → seam.ModelCapabilities()
  → registry.Enrich(pool.Models(), now)   // live catalog + embedded registry
  → canonical.CapabilityCatalog
  → render to wire JSON
```

No per-request probing. No network. Registry validated once at startup.

## 6. Endpoint contract

### 6.1 Wire shape (`render.go`)

```go
type modelCapabilityList struct {
    Object           string                 `json:"object"`            // "list"
    RegistryRevision string                 `json:"registry_revision"` // "sha256-<hex>"
    GeneratedAt      string                 `json:"generated_at"`      // RFC3339 UTC
    Data             []modelCapabilityEntry `json:"data"`
}
type modelCapabilityEntry struct {
    ID            string                  `json:"id"`
    Name          string                  `json:"name"`
    Available     bool                    `json:"available"`
    SelectionMode string                  `json:"selection_mode"` // "automatic" | "explicit"
    Capabilities  map[string]string       `json:"capabilities"`   // 4 keys, always present
    Evidence      map[string]evidenceWire `json:"evidence"`       // only supported/unsupported keys
}
type evidenceWire struct {
    Source     string `json:"source"`
    Reference  string `json:"reference"`
    VerifiedAt string `json:"verified_at"`
    Notes      string `json:"notes,omitempty"`
}
```

Example (abbreviated):

```json
{
  "object": "list",
  "registry_revision": "sha256-<hex>",
  "generated_at": "2026-07-16T12:00:00Z",
  "data": [
    { "id": "auto", "name": "Automatic", "available": true, "selection_mode": "automatic",
      "capabilities": { "completion": "unknown", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" },
      "evidence": {} },
    { "id": "claude-opus-4.8", "name": "Claude Opus 4.8", "available": true, "selection_mode": "explicit",
      "capabilities": { "completion": "supported", "tools": "supported", "vision": "supported", "reasoning": "supported" },
      "evidence": {
        "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model." },
        "vision":     { "source": "vendor_documentation", "reference": "https://docs.anthropic.com/...", "verified_at": "2026-07-16", "notes": "Official input-modality declaration." }
      } }
  ]
}
```

### 6.2 Guarantees

- `auto` is **first**, `selection_mode:"automatic"`, all caps `unknown`, empty evidence.
- `data` contains **only explicit models present in the current live Kiro catalog**, in
  live-catalog order.
- **Live Kiro display name wins**; falls back to registry `name`, then the ID.
- **Live but unregistered models → every capability `unknown`, empty evidence.**
- **Registry-only models absent from the live catalog are omitted** (never stale-available).
- **Empty/degraded catalog → `auto` only** (matches existing degraded posture).
- **Deterministic apart from `generated_at`** — `registry_revision` is a content hash; Go
  marshals map keys sorted; slice order follows the deterministic live catalog.
- **Auth: IP-allowlist only**, same middleware chain as `/v1/models` (no bearer).
- **No internal leakage** — response carries only id/name/available/selection_mode/
  capabilities/evidence (public reference URLs + notes). No paths, env vars, worker IDs,
  pool slots, prompts, or secrets.
- `Content-Type: application/json`.

## 7. Enrichment rules (`Enrich`)

Given `live []canonical.ModelInfo` and `now`:

1. **Emit `auto` first**: fixed name `"Automatic"`, `available:true`,
   `selection_mode:"automatic"`, all four caps `unknown`, empty evidence. Any `auto` entry
   in `live` (Kiro lists one) is dropped and replaced by this single normalized entry
   (dedupe); the live name for `auto` is intentionally not surfaced.
2. For each **explicit** live model (in live order), look up the registry by **exact ID**:
   - **Registered** → its four verified capability states and evidence.
   - **Unregistered** → all four caps `unknown`, empty evidence.
   - `selection_mode:"explicit"`, `available:true` (presence in the live catalog *is*
     availability).
3. **Display name:** live `ModelInfo.Name` if non-empty, else registry `name`, else ID.
4. **Registry-only models** (in the registry but not in `live`) are **not emitted**.
5. **Empty `live`** → catalog with only the `auto` entry.
6. **No input mutation** — operate on copies; never sort or rewrite the caller's slice/maps.
7. `RegistryRevision` and `GeneratedAt` set on the returned `CapabilityCatalog`.

## 8. Registry schema, validation, and evidence policy

### 8.1 On-disk format — JSON array (deliberate)

The registry is a **JSON array of entries**, not a JSON object keyed by ID. Rationale: a
JSON object cannot express or detect duplicate keys (Go's `encoding/json` silently keeps
the last), which would defeat the "reject duplicate model IDs" rule. The array is validated
into an in-memory `map[id]entry` for exact-ID lookup.

```json
[
  {
    "id": "claude-opus-4.8",
    "name": "Claude Opus 4.8",
    "capabilities": { "completion": "supported", "tools": "supported", "vision": "supported", "reasoning": "supported" },
    "evidence": {
      "completion": { "source": "kiro_declared",        "reference": "kiro session/new availableModels", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model." },
      "tools":      { "source": "vendor_documentation", "reference": "https://docs.anthropic.com/...",     "verified_at": "2026-07-16", "notes": "Official tool-use support." },
      "vision":     { "source": "vendor_documentation", "reference": "https://docs.anthropic.com/...",     "verified_at": "2026-07-16", "notes": "Official input-modality declaration." },
      "reasoning":  { "source": "vendor_documentation", "reference": "https://docs.anthropic.com/...",     "verified_at": "2026-07-16", "notes": "Official extended-thinking support." }
    }
  }
]
```

Each entry must declare **all four** required capability keys
(`completion`, `tools`, `vision`, `reasoning`).

### 8.2 Validation (fail-fast at `Load`)

Reject with a descriptive error on any of:

- Empty or missing `id`.
- Duplicate `id`.
- A capability key outside `{completion, tools, vision, reasoning}`.
- A missing required capability key.
- A state value outside `{supported, unsupported, unknown}`.
- A `supported`/`unsupported` capability with **no evidence**.
- Evidence with a missing/empty `reference`.
- An invalid `verified_at` (must parse as `YYYY-MM-DD`).
- An evidence `source` outside `{kiro_declared, vendor_documentation, controlled_probe}`.
- **Evidence keys that do not exactly equal the set of capabilities whose state is
  `supported` or `unsupported`.** (Covers both "missing evidence for a supported cap" and
  "evidence for a cap that isn't in that state / isn't present".)

`unknown` capabilities carry no evidence.

### 8.3 Evidence source types

- `kiro_declared` — the model appears in Kiro's live `session/new availableModels`
  (used for `completion`).
- `vendor_documentation` — official documentation from the underlying model vendor,
  unambiguously mapped to the exact Kiro model ID.
- `controlled_probe` — reserved for a future maintainer-only synthetic probe (not built
  here).

**Forbidden as evidence:** blogs, marketing summaries, model-name intuition, an LLM's
memory. If official documentation cannot be **unambiguously** mapped to the exact Kiro
model ID, the capability stays `unknown`.

## 9. Initial registry content (v1 seed)

Live catalog (from a real deployment, 2026-07-16):
`auto`, `claude-opus-4.8`, `claude-sonnet-5`, `claude-opus-4.7`, `claude-opus-4.6`,
`claude-sonnet-4.6`, `claude-opus-4.5`, `claude-sonnet-4.5`, `claude-sonnet-4`,
`claude-haiku-4.5`, `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`, `deepseek-3.2`,
`minimax-m2.5`, `minimax-m2.1`, `glm-5`, `qwen3-coder-next`.

**Seeding policy (evidence-graded):**

- **`completion: supported` (`kiro_declared`)** for every live model — appearing in Kiro's
  selectable chat catalog is Kiro declaring completion usability.
- **Claude family** (`claude-*`): seed `tools`/`vision`/`reasoning` from **official
  Anthropic documentation** where the exact Kiro ID maps unambiguously to a documented
  Anthropic model. Facts sourced from official docs / the `claude-api` reference during
  implementation — **never model memory**. Any version that cannot be unambiguously mapped
  stays `unknown`.
- **GPT tier codenames** (`gpt-5.6-sol|terra|luna`): "sol/terra/luna" are Kiro-internal
  tiers with no official OpenAI model of that exact ID → `tools`/`vision`/`reasoning`
  remain `unknown`.
- **Other vendors** (`deepseek-3.2`, `minimax-m2.*`, `glm-5`, `qwen3-coder-next`):
  case-by-case; only seed a capability when an official doc maps unambiguously to the exact
  ID and version, otherwise `unknown`.

The initial catalog is **not** claimed fully verified. Models deliberately left `unknown`
beyond `completion` are recorded as such in the completion report.

## 10. Compatibility

- `GET /v1/models` response and model set unchanged.
- `/api/tags` unchanged and still equal to `/v1/models`' model-ID set.
- `/api/show` wire unchanged; its legacy `capabilities:["completion","tools"]` is **not**
  documented anywhere as verified per-model evidence.
- OpenAI/Anthropic/Ollama chat, stream, and tool-call behavior unchanged.
- Model-discovery self-healing and `auto` semantics unchanged.
- Recent structured tool-call surfacing and persona-guard changes preserved.
- No cgo, no runtime network dependency.

## 11. Testing (TDD — failing tests first)

### 11.1 Registry unit tests (`internal/registry`)

- Valid registry loads successfully.
- Each validation rejection (invalid state; missing evidence for supported/unsupported;
  invalid capability name; missing required capability; empty ID; duplicate ID; missing
  reference; invalid date; bad source type; evidence for a non-supported/unsupported cap).
- Unknown capabilities accepted without evidence.
- Exact-ID matching only (no fuzzy match).
- A live unregistered model becomes all-`unknown`.
- A registry-only stale model is omitted.
- Live display name wins over registry name.
- Input slice/maps not mutated.
- `auto` is first and `selection_mode:"automatic"`.
- `RegistryRevision` deterministic for identical bytes.

**No snapshot test freezing today's full model list.** Test relationships and invariants.

### 11.2 OpenAI endpoint tests (`internal/adapter/openai`)

- `/v1/model-capabilities` response shape and content type.
- Auth middleware parity with `/v1/models` (IP-allowlist behavior; no bearer requirement).
- Catalog-present behavior.
- Empty/degraded catalog → `auto` only.
- Unknown (unregistered) model → all `unknown`.
- No internal information leakage.
- Error handling / correct JSON.

### 11.3 Cross-surface regression

- `/v1/models` byte-unchanged.
- `/api/tags` and `/v1/models` expose the same model-ID set.
- `/api/show` compatibility intact.
- Existing chat and tool-call tests still pass.

### 11.4 E2E (`tests/e2e`, extend `fake-kiro-cli` minimally)

Fake Kiro advertises at least one **registered** and one **unknown** model. Assert:

1. `/v1/model-capabilities` returns both (both are live).
2. The registered model carries its verified states.
3. The unknown model carries only `unknown`.
4. A registry model absent from fake Kiro is **not** returned.

## 12. Documentation & maintenance

Add docs (reference + registry README) covering: the endpoint contract; availability vs.
capability verification; why agent-wide `promptCapabilities` cannot prove per-model
support; the registry schema and evidence policy; how to research and add a new exact model
ID; how to retire a removed model; how to update evidence and verification dates; how to
validate registry drift against a live Kiro catalog; and why `unknown` models stay visible
but should not be selected for capability-sensitive work.

**Maintenance rule (enforced by schema/tests, not docs alone):**

> Adding or changing a Kiro model capability declaration requires an exact model ID,
> evidence, a verification date, registry validation, endpoint contract tests, and a review
> of whether the model is still present in the live Kiro catalog.

## 13. Verification commands

```bash
gofmt -w <changed-go-files>
go vet ./...
go test ./...
go test -race ./...
```

Plus the repository's required security/static-analysis gates (`gosec` G204 et al. per
`CLAUDE.md` / `docs/briefs/go_port_brief.md` §3.12) and targeted tests for the registry
package, OpenAI models/capability handlers, pool model discovery, and E2E model-catalog
behavior. If a required tool is unavailable, report the exact command and limitation rather
than claiming a pass.

## 14. Open questions / follow-ups

- Which non-Claude vendor IDs (`deepseek-3.2`, `minimax-m2.*`, `glm-5`, `qwen3-coder-next`)
  can be unambiguously mapped to official docs is determined during implementation; those
  that cannot stay `unknown`.
- A future maintainer-only `controlled_probe` tool (explicit, synthetic-data-only,
  non-side-effecting) may populate `source: controlled_probe` — out of scope here but the
  schema is ready for it.
