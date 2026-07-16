# Model Capabilities Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Gateway-owned `GET /v1/model-capabilities` endpoint that fuses the live Kiro catalog (availability) with an embedded, evidence-backed registry (verified per-model capabilities), using a three-state model (`supported`/`unsupported`/`unknown`).

**Architecture:** New pure-type layer in `internal/canonical`; new `internal/registry` package owning an embedded `registry.json`, its validation, revision hashing, and live-catalog enrichment; a new consumer-defined seam + handler + wire render in the OpenAI adapter; wiring in `cmd/otto-gateway/main.go`. HTTP rendering stays out of the registry package; the registry stays out of HTTP.

**Tech Stack:** Go 1.23+, stdlib `net/http` + chi, `crypto/sha256`, `go:embed`. No cgo, no runtime network dependency.

## Global Constraints

- **Go 1.23+, no cgo in the main binary.** (`CLAUDE.md`)
- **No runtime network dependency for the registry** — embedded via `go:embed`. (spec §3)
- **`GET /v1/models`, `/api/tags`, `/api/show` wire shapes and model sets are unchanged.** (spec §10)
- **Auth for the new endpoint = IP-allowlist only, no bearer** — identical middleware chain to `/v1/models`. (spec §6.2)
- **Capability states are exactly `supported` / `unsupported` / `unknown`.** Never a bool where the source can be unknown. (spec §2)
- **Required capability keys (every registry entry declares all four):** `completion`, `tools`, `vision`, `reasoning`. (spec §8.1)
- **Evidence source types are exactly `kiro_declared` / `vendor_documentation` / `controlled_probe`.** (spec §8.3)
- **No name-guessing, no fuzzy-matching, exact-ID lookup only.** Unregistered live models → all `unknown`. Registry-only models absent from the live catalog → omitted. (spec §7)
- **Response deterministic apart from `generated_at`.** (spec §6.2)
- **No internal leakage** — response fields limited to id/name/available/selection_mode/capabilities/evidence. (spec §6.2)
- **Verification date format is `YYYY-MM-DD`** (Go layout `2006-01-02`). (spec §8.2)
- **Evidence must not be sourced from LLM memory, blogs, or marketing.** Only exact-ID → official docs / `kiro_declared`. (spec §8.3)
- **TRST-04 boundary:** the OpenAI adapter imports only `internal/canonical` (+ stdlib/chi/slog). It must NOT import `internal/registry` or `internal/pool`. Concrete types are wired in `main.go`. (`internal/adapter/openai/adapter.go` package doc)

Spec: `docs/superpowers/specs/2026-07-16-model-capabilities-endpoint-design.md`.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/canonical/modelcaps.go` (create) | Pure tag-free types: `CapabilityState`, `Evidence`, `ModelCapability`, `CapabilityCatalog`, `RequiredCapabilities`. |
| `internal/registry/registry.json` (create) | Source-controlled, embedded capability data (array of entries). |
| `internal/registry/registry.go` (create) | `go:embed`, `Load`, validation, revision hashing, `Enrich`. |
| `internal/registry/registry_test.go` (create) | Validation, revision, and enrichment unit tests. |
| `internal/adapter/openai/adapter.go` (modify) | Add `ModelCapabilityCatalog` interface, `Config.ModelCapabilities` field, route registration. |
| `internal/adapter/openai/modelcaps.go` (create) | Wire structs + `capabilityCatalogToWire` + `handleModelCapabilities`. |
| `internal/adapter/openai/modelcaps_test.go` (create) | Handler + render tests. |
| `cmd/otto-gateway/main.go` (modify) | Load registry (fatal on error), wire the combiner seam into the OpenAI adapter. |
| `tests/e2e/cmd/fake-kiro-cli/main.go` (modify) | `GW_FAKE_KIRO_MODELS` env knob to script the advertised catalog. |
| `tests/e2e/openai_e2e_test.go` (modify) | `/v1/model-capabilities` E2E assertions. |
| `docs/reference/model_capabilities.md` (create) | Endpoint contract + availability-vs-capability + registry schema + maintenance rule. |

---

## Task 1: Canonical types + registry package (mechanics + completion-only seed)

**Files:**
- Create: `internal/canonical/modelcaps.go`
- Create: `internal/registry/registry.go`
- Create: `internal/registry/registry.json`
- Test: `internal/registry/registry_test.go`

**Interfaces:**
- Produces (canonical):
  - `type CapabilityState string`; consts `CapUnknown = "unknown"`, `CapSupported = "supported"`, `CapUnsupported = "unsupported"`.
  - `var RequiredCapabilities = []string{"completion", "tools", "vision", "reasoning"}`.
  - `type Evidence struct { Source, Reference, VerifiedAt, Notes string }`.
  - `type ModelCapability struct { ID, Name string; Available bool; SelectionMode string; Capabilities map[string]CapabilityState; Evidence map[string]Evidence }`.
  - `type CapabilityCatalog struct { RegistryRevision string; GeneratedAt time.Time; Entries []ModelCapability }`.
- Produces (registry): `func Load() (*Registry, error)`; `func (r *Registry) Revision() string`; `func (r *Registry) Enrich(live []canonical.ModelInfo, now time.Time) canonical.CapabilityCatalog` (Enrich implemented in Task 2).

- [ ] **Step 1: Write the canonical types file**

Create `internal/canonical/modelcaps.go`:

```go
// Package canonical — per-model capability types (spec 2026-07-16). Tag-free;
// wire JSON tags live in the adapter layer.
package canonical

import "time"

// CapabilityState is a machine-readable three-state capability value. A bool is
// never used where the source can be unknown.
type CapabilityState string

const (
	CapUnknown     CapabilityState = "unknown"
	CapSupported   CapabilityState = "supported"
	CapUnsupported CapabilityState = "unsupported"
)

// RequiredCapabilities is the set of capability keys every registry entry must
// declare and every rendered entry carries.
var RequiredCapabilities = []string{"completion", "tools", "vision", "reasoning"}

// Evidence records why a capability is declared supported/unsupported.
// VerifiedAt is a YYYY-MM-DD date string. Notes is optional.
type Evidence struct {
	Source     string
	Reference  string
	VerifiedAt string
	Notes      string
}

// ModelCapability is one entry in a CapabilityCatalog. SelectionMode is
// "automatic" (the synthetic auto entry) or "explicit". Capabilities always
// carries all of RequiredCapabilities. Evidence is keyed only by capabilities
// whose state is supported or unsupported.
type ModelCapability struct {
	ID            string
	Name          string
	Available     bool
	SelectionMode string
	Capabilities  map[string]CapabilityState
	Evidence      map[string]Evidence
}

// CapabilityCatalog is the enriched result: the auto entry first, then the
// explicit live models. RegistryRevision is a content hash; GeneratedAt is the
// only non-deterministic field.
type CapabilityCatalog struct {
	RegistryRevision string
	GeneratedAt      time.Time
	Entries          []ModelCapability
}
```

- [ ] **Step 2: Write the registry loader + validation (Enrich stubbed for Task 2)**

Create `internal/registry/registry.go`:

```go
// Package registry loads and validates the embedded per-model capability
// registry and enriches it against the live Kiro catalog. It owns registry
// data only — no HTTP. (spec 2026-07-16 §5.1, §8)
package registry

import (
	"bytes"
	"crypto/sha256"
	_ "embed" // for //go:embed registry.json
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"otto-gateway/internal/canonical"
)

//go:embed registry.json
var registryBytes []byte

// validSources is the closed set of evidence source types (spec §8.3).
var validSources = map[string]struct{}{
	"kiro_declared":        {},
	"vendor_documentation": {},
	"controlled_probe":     {},
}

// validStates is the closed set of capability states (spec §2).
var validStates = map[string]struct{}{
	string(canonical.CapUnknown):     {},
	string(canonical.CapSupported):   {},
	string(canonical.CapUnsupported): {},
}

// fileEvidence is the on-disk evidence shape.
type fileEvidence struct {
	Source     string `json:"source"`
	Reference  string `json:"reference"`
	VerifiedAt string `json:"verified_at"`
	Notes      string `json:"notes,omitempty"`
}

// fileEntry is one on-disk registry entry. The registry file is a JSON ARRAY of
// these (not an object keyed by id) so duplicate/empty ids are detectable.
type fileEntry struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Capabilities map[string]string       `json:"capabilities"`
	Evidence     map[string]fileEvidence `json:"evidence"`
}

// storedEntry is the validated in-memory form (Available/SelectionMode are set
// at enrichment time, not stored).
type storedEntry struct {
	name         string
	capabilities map[string]canonical.CapabilityState
	evidence     map[string]canonical.Evidence
}

// Registry is the validated, indexed capability registry.
type Registry struct {
	revision string
	entries  map[string]storedEntry
}

// Revision returns the deterministic content hash of the embedded registry.
func (r *Registry) Revision() string { return r.revision }

// Load parses and validates the embedded registry.json. It returns an error on
// any validation failure — a build/ship error, surfaced at startup, never a
// runtime 500.
func Load() (*Registry, error) { return load(registryBytes) }

// load is the testable core: it accepts raw bytes so tests can feed invalid
// fixtures without touching the embedded file.
func load(data []byte) (*Registry, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var entries []fileEntry
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("registry: decode: %w", err)
	}

	reg := &Registry{entries: make(map[string]storedEntry, len(entries))}
	for i, e := range entries {
		if e.ID == "" {
			return nil, fmt.Errorf("registry: entry %d: empty id", i)
		}
		if _, dup := reg.entries[e.ID]; dup {
			return nil, fmt.Errorf("registry: duplicate id %q", e.ID)
		}

		// Capabilities: exactly the four required keys, each a valid state.
		if len(e.Capabilities) != len(canonical.RequiredCapabilities) {
			return nil, fmt.Errorf("registry: %q: capabilities must declare exactly %v", e.ID, canonical.RequiredCapabilities)
		}
		caps := make(map[string]canonical.CapabilityState, len(e.Capabilities))
		supportedSet := map[string]struct{}{}
		for _, req := range canonical.RequiredCapabilities {
			state, ok := e.Capabilities[req]
			if !ok {
				return nil, fmt.Errorf("registry: %q: missing required capability %q", e.ID, req)
			}
			if _, valid := validStates[state]; !valid {
				return nil, fmt.Errorf("registry: %q: capability %q has invalid state %q", e.ID, req, state)
			}
			caps[req] = canonical.CapabilityState(state)
			if state == string(canonical.CapSupported) || state == string(canonical.CapUnsupported) {
				supportedSet[req] = struct{}{}
			}
		}
		// Reject any capability key outside the required set.
		for k := range e.Capabilities {
			if !contains(canonical.RequiredCapabilities, k) {
				return nil, fmt.Errorf("registry: %q: unknown capability key %q", e.ID, k)
			}
		}

		// Evidence keys must EXACTLY equal the supported/unsupported set.
		if len(e.Evidence) != len(supportedSet) {
			return nil, fmt.Errorf("registry: %q: evidence keys must equal the supported/unsupported set", e.ID)
		}
		evidence := make(map[string]canonical.Evidence, len(e.Evidence))
		for cap, ev := range e.Evidence {
			if _, want := supportedSet[cap]; !want {
				return nil, fmt.Errorf("registry: %q: evidence for capability %q not in supported/unsupported state", e.ID, cap)
			}
			if _, ok := validSources[ev.Source]; !ok {
				return nil, fmt.Errorf("registry: %q: capability %q: invalid evidence source %q", e.ID, cap, ev.Source)
			}
			if ev.Reference == "" {
				return nil, fmt.Errorf("registry: %q: capability %q: missing evidence reference", e.ID, cap)
			}
			if _, err := time.Parse("2006-01-02", ev.VerifiedAt); err != nil {
				return nil, fmt.Errorf("registry: %q: capability %q: invalid verified_at %q (want YYYY-MM-DD)", e.ID, cap, ev.VerifiedAt)
			}
			evidence[cap] = canonical.Evidence{
				Source: ev.Source, Reference: ev.Reference, VerifiedAt: ev.VerifiedAt, Notes: ev.Notes,
			}
		}

		reg.entries[e.ID] = storedEntry{name: e.Name, capabilities: caps, evidence: evidence}
	}

	sum := sha256.Sum256(data)
	reg.revision = "sha256-" + hex.EncodeToString(sum[:])
	return reg, nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Enrich is implemented in Task 2. Placeholder keeps the package compiling.
func (r *Registry) Enrich(live []canonical.ModelInfo, now time.Time) canonical.CapabilityCatalog {
	_ = sort.Strings // retained for Task 2; remove if unused
	return canonical.CapabilityCatalog{RegistryRevision: r.revision, GeneratedAt: now}
}
```

> Note: remove the `sort` import + `_ = sort.Strings` line if Task 2 does not need it. It is included now only so the file compiles cleanly; `go vet` in Step 5 will flag an unused import if left dangling — delete it then.

- [ ] **Step 3: Write the completion-only seed registry**

Create `internal/registry/registry.json`. Every live model gets `completion: supported` with `kiro_declared` evidence; `tools`/`vision`/`reasoning` are `unknown` (no evidence). Claude-family vision/tools/reasoning evidence is added in Task 6. `auto` is NOT in the registry (it is synthesized by Enrich).

```json
[
  { "id": "claude-opus-4.8",   "name": "Claude Opus 4.8",   "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." } } },
  { "id": "claude-sonnet-5",   "name": "Claude Sonnet 5",   "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." } } },
  { "id": "claude-opus-4.7",   "name": "Claude Opus 4.7",   "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." } } },
  { "id": "claude-opus-4.6",   "name": "Claude Opus 4.6",   "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." } } },
  { "id": "claude-sonnet-4.6", "name": "Claude Sonnet 4.6", "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." } } },
  { "id": "claude-opus-4.5",   "name": "Claude Opus 4.5",   "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." } } },
  { "id": "claude-sonnet-4.5", "name": "Claude Sonnet 4.5", "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." } } },
  { "id": "claude-sonnet-4",   "name": "Claude Sonnet 4",   "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." } } },
  { "id": "claude-haiku-4.5",  "name": "Claude Haiku 4.5",  "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." } } },
  { "id": "gpt-5.6-sol",       "name": "GPT-5.6 Sol",       "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Kiro-internal tier codename; capabilities beyond completion unverified." } } },
  { "id": "gpt-5.6-terra",     "name": "GPT-5.6 Terra",     "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Kiro-internal tier codename; capabilities beyond completion unverified." } } },
  { "id": "gpt-5.6-luna",      "name": "GPT-5.6 Luna",      "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Kiro-internal tier codename; capabilities beyond completion unverified." } } },
  { "id": "deepseek-3.2",      "name": "DeepSeek 3.2",      "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Exact vendor-doc mapping unconfirmed; capabilities beyond completion unverified." } } },
  { "id": "minimax-m2.5",      "name": "MiniMax M2.5",      "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Exact vendor-doc mapping unconfirmed; capabilities beyond completion unverified." } } },
  { "id": "minimax-m2.1",      "name": "MiniMax M2.1",      "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Exact vendor-doc mapping unconfirmed; capabilities beyond completion unverified." } } },
  { "id": "glm-5",             "name": "GLM-5",             "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Exact vendor-doc mapping unconfirmed; capabilities beyond completion unverified." } } },
  { "id": "qwen3-coder-next",  "name": "Qwen3 Coder Next",  "capabilities": { "completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown" }, "evidence": { "completion": { "source": "kiro_declared", "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Rolling '-next' alias; exact vendor-doc mapping unconfirmed; capabilities beyond completion unverified." } } }
]
```

- [ ] **Step 4: Write failing validation + revision tests**

Create `internal/registry/registry_test.go`:

```go
package registry

import (
	"strings"
	"testing"
)

// validEntry returns a minimal valid single-entry registry as JSON bytes,
// with the given overrides spliced in via the caller building the string.
func validRegistryJSON() []byte {
	return []byte(`[
	  {"id":"m1","name":"Model One",
	   "capabilities":{"completion":"supported","tools":"unknown","vision":"unsupported","reasoning":"unknown"},
	   "evidence":{
	     "completion":{"source":"kiro_declared","reference":"live catalog","verified_at":"2026-07-16"},
	     "vision":{"source":"vendor_documentation","reference":"https://docs.example/m1","verified_at":"2026-07-16","notes":"no image input"}
	   }}
	]`)
}

func TestLoad_Valid(t *testing.T) {
	reg, err := load(validRegistryJSON())
	if err != nil {
		t.Fatalf("load valid: %v", err)
	}
	if !strings.HasPrefix(reg.Revision(), "sha256-") {
		t.Errorf("revision: got %q, want sha256- prefix", reg.Revision())
	}
	if _, ok := reg.entries["m1"]; !ok {
		t.Errorf("entry m1 not indexed")
	}
}

func TestLoad_Rejects(t *testing.T) {
	cases := map[string]string{
		"empty_id": `[{"id":"","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{}}]`,
		"duplicate_id": `[
		  {"id":"dup","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{}},
		  {"id":"dup","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{}}
		]`,
		"invalid_state": `[{"id":"m","capabilities":{"completion":"maybe","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{}}]`,
		"invalid_capability_name": `[{"id":"m","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown","telepathy":"unknown"},"evidence":{}}]`,
		"missing_required_capability": `[{"id":"m","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown"},"evidence":{}}]`,
		"supported_without_evidence": `[{"id":"m","capabilities":{"completion":"supported","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{}}]`,
		"missing_reference": `[{"id":"m","capabilities":{"completion":"supported","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{"completion":{"source":"kiro_declared","reference":"","verified_at":"2026-07-16"}}}]`,
		"invalid_date": `[{"id":"m","capabilities":{"completion":"supported","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{"completion":{"source":"kiro_declared","reference":"x","verified_at":"07-16-2026"}}}]`,
		"bad_source": `[{"id":"m","capabilities":{"completion":"supported","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{"completion":{"source":"a_blog_post","reference":"x","verified_at":"2026-07-16"}}}]`,
		"evidence_for_unknown_cap": `[{"id":"m","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{"completion":{"source":"kiro_declared","reference":"x","verified_at":"2026-07-16"}}}]`,
	}
	for name, js := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load([]byte(js)); err == nil {
				t.Errorf("expected rejection for %s, got nil error", name)
			}
		})
	}
}

func TestLoad_UnknownCapsNeedNoEvidence(t *testing.T) {
	js := `[{"id":"m","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{}}]`
	if _, err := load([]byte(js)); err != nil {
		t.Errorf("all-unknown entry should load, got: %v", err)
	}
}

func TestLoad_RevisionDeterministic(t *testing.T) {
	a, err := load(validRegistryJSON())
	if err != nil {
		t.Fatal(err)
	}
	b, err := load(validRegistryJSON())
	if err != nil {
		t.Fatal(err)
	}
	if a.Revision() != b.Revision() {
		t.Errorf("revision not deterministic: %q vs %q", a.Revision(), b.Revision())
	}
}

func TestLoad_EmbeddedRegistryValid(t *testing.T) {
	// The shipped registry.json must always load.
	if _, err := Load(); err != nil {
		t.Fatalf("embedded registry.json invalid: %v", err)
	}
}
```

- [ ] **Step 5: Run tests to verify they fail, then pass**

Run: `go test ./internal/registry/ ./internal/canonical/ -run 'Load|Cap' -v`
Expected: initially FAIL (types/loader absent); after Steps 1–3, PASS. Then `go vet ./internal/registry/` — if it flags the `sort` import, delete the `_ = sort.Strings` line and the import.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/canonical/modelcaps.go internal/registry/registry.go
git add internal/canonical/modelcaps.go internal/registry/registry.go internal/registry/registry.json internal/registry/registry_test.go
git commit -m "feat(registry): embedded capability registry with validation (completion seed)"
```

---

## Task 2: Registry enrichment (`Enrich`)

**Files:**
- Modify: `internal/registry/registry.go` (replace the `Enrich` stub)
- Test: `internal/registry/registry_test.go` (append)

**Interfaces:**
- Consumes: `Registry.entries`, `Registry.revision`, `canonical.ModelInfo`, `canonical.CapabilityCatalog`.
- Produces: `func (r *Registry) Enrich(live []canonical.ModelInfo, now time.Time) canonical.CapabilityCatalog`.

- [ ] **Step 1: Write failing enrichment tests**

Append to `internal/registry/registry_test.go`:

```go
import "otto-gateway/internal/canonical" // add to the existing import block

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := load(validRegistryJSON()) // has entry "m1"
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func findEntry(cat canonical.CapabilityCatalog, id string) (canonical.ModelCapability, bool) {
	for _, e := range cat.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return canonical.ModelCapability{}, false
}

func TestEnrich_AutoFirstAndAutomatic(t *testing.T) {
	cat := testRegistry(t).Enrich([]canonical.ModelInfo{{ID: "m1"}}, time.Unix(0, 0))
	if len(cat.Entries) == 0 || cat.Entries[0].ID != "auto" {
		t.Fatalf("auto not first: %+v", cat.Entries)
	}
	auto := cat.Entries[0]
	if auto.SelectionMode != "automatic" || auto.Name != "Automatic" || !auto.Available {
		t.Errorf("auto entry wrong: %+v", auto)
	}
	for _, k := range canonical.RequiredCapabilities {
		if auto.Capabilities[k] != canonical.CapUnknown {
			t.Errorf("auto cap %q: got %q, want unknown", k, auto.Capabilities[k])
		}
	}
	if len(auto.Evidence) != 0 {
		t.Errorf("auto evidence should be empty, got %+v", auto.Evidence)
	}
}

func TestEnrich_DropsLiveAuto(t *testing.T) {
	cat := testRegistry(t).Enrich([]canonical.ModelInfo{{ID: "auto", Name: "Live Auto"}, {ID: "m1"}}, time.Unix(0, 0))
	autoCount := 0
	for _, e := range cat.Entries {
		if e.ID == "auto" {
			autoCount++
		}
	}
	if autoCount != 1 {
		t.Errorf("auto emitted %d times, want exactly 1", autoCount)
	}
	if cat.Entries[0].Name != "Automatic" {
		t.Errorf("live auto name leaked: %q", cat.Entries[0].Name)
	}
}

func TestEnrich_RegisteredCarriesStates(t *testing.T) {
	cat := testRegistry(t).Enrich([]canonical.ModelInfo{{ID: "m1"}}, time.Unix(0, 0))
	m1, ok := findEntry(cat, "m1")
	if !ok {
		t.Fatal("m1 missing")
	}
	if m1.SelectionMode != "explicit" || !m1.Available {
		t.Errorf("m1 mode/available wrong: %+v", m1)
	}
	if m1.Capabilities["completion"] != canonical.CapSupported {
		t.Errorf("m1 completion: got %q, want supported", m1.Capabilities["completion"])
	}
	if m1.Capabilities["vision"] != canonical.CapUnsupported {
		t.Errorf("m1 vision: got %q, want unsupported", m1.Capabilities["vision"])
	}
	if _, ok := m1.Evidence["vision"]; !ok {
		t.Errorf("m1 vision evidence missing")
	}
}

func TestEnrich_UnregisteredAllUnknown(t *testing.T) {
	cat := testRegistry(t).Enrich([]canonical.ModelInfo{{ID: "ghost", Name: "Ghost"}}, time.Unix(0, 0))
	g, ok := findEntry(cat, "ghost")
	if !ok {
		t.Fatal("ghost missing")
	}
	for _, k := range canonical.RequiredCapabilities {
		if g.Capabilities[k] != canonical.CapUnknown {
			t.Errorf("ghost cap %q: got %q, want unknown", k, g.Capabilities[k])
		}
	}
	if len(g.Evidence) != 0 {
		t.Errorf("ghost evidence should be empty")
	}
	if g.Name != "Ghost" {
		t.Errorf("ghost live name not used: %q", g.Name)
	}
}

func TestEnrich_ExactMatchOnly(t *testing.T) {
	// "m1x" must NOT fuzzy-match registry entry "m1".
	cat := testRegistry(t).Enrich([]canonical.ModelInfo{{ID: "m1x"}}, time.Unix(0, 0))
	e, _ := findEntry(cat, "m1x")
	if e.Capabilities["completion"] != canonical.CapUnknown {
		t.Errorf("m1x fuzzy-matched m1; completion=%q", e.Capabilities["completion"])
	}
}

func TestEnrich_StaleRegistryOnlyOmitted(t *testing.T) {
	cat := testRegistry(t).Enrich([]canonical.ModelInfo{{ID: "other"}}, time.Unix(0, 0))
	if _, ok := findEntry(cat, "m1"); ok {
		t.Errorf("registry-only m1 emitted despite absent from live catalog")
	}
}

func TestEnrich_LiveNameWins(t *testing.T) {
	cat := testRegistry(t).Enrich([]canonical.ModelInfo{{ID: "m1", Name: "Live Name"}}, time.Unix(0, 0))
	m1, _ := findEntry(cat, "m1")
	if m1.Name != "Live Name" {
		t.Errorf("live name should win: got %q", m1.Name)
	}
}

func TestEnrich_EmptyCatalogAutoOnly(t *testing.T) {
	cat := testRegistry(t).Enrich(nil, time.Unix(0, 0))
	if len(cat.Entries) != 1 || cat.Entries[0].ID != "auto" {
		t.Errorf("empty catalog should be auto-only, got %+v", cat.Entries)
	}
}

func TestEnrich_DoesNotMutateInput(t *testing.T) {
	live := []canonical.ModelInfo{{ID: "m1", Name: "orig"}}
	_ = testRegistry(t).Enrich(live, time.Unix(0, 0))
	if live[0].Name != "orig" || live[0].ID != "m1" {
		t.Errorf("input mutated: %+v", live)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/registry/ -run Enrich -v`
Expected: FAIL (stub returns no entries).

- [ ] **Step 3: Implement `Enrich`**

Replace the `Enrich` stub (and drop the `sort` import + placeholder line) in `internal/registry/registry.go`:

```go
// Enrich fuses the live Kiro catalog with the registry. auto is emitted first
// (normalized, all-unknown); any live "auto" is dropped. Each explicit live
// model gets its registered states or all-unknown when unregistered. Registry
// entries absent from the live catalog are omitted. Inputs are never mutated.
func (r *Registry) Enrich(live []canonical.ModelInfo, now time.Time) canonical.CapabilityCatalog {
	out := canonical.CapabilityCatalog{
		RegistryRevision: r.revision,
		GeneratedAt:      now,
		Entries:          make([]canonical.ModelCapability, 0, 1+len(live)),
	}
	out.Entries = append(out.Entries, autoEntry())

	for _, m := range live {
		if m.ID == "" || m.ID == "auto" { // dedupe auto, skip empty
			continue
		}
		entry := canonical.ModelCapability{
			ID:            m.ID,
			Available:     true,
			SelectionMode: "explicit",
			Capabilities:  allUnknown(),
			Evidence:      map[string]canonical.Evidence{},
		}
		if stored, ok := r.entries[m.ID]; ok {
			entry.Name = pickName(m.Name, stored.name, m.ID)
			entry.Capabilities = cloneCaps(stored.capabilities)
			entry.Evidence = cloneEvidence(stored.evidence)
		} else {
			entry.Name = pickName(m.Name, "", m.ID)
		}
		out.Entries = append(out.Entries, entry)
	}
	return out
}

func autoEntry() canonical.ModelCapability {
	return canonical.ModelCapability{
		ID:            "auto",
		Name:          "Automatic",
		Available:     true,
		SelectionMode: "automatic",
		Capabilities:  allUnknown(),
		Evidence:      map[string]canonical.Evidence{},
	}
}

func allUnknown() map[string]canonical.CapabilityState {
	m := make(map[string]canonical.CapabilityState, len(canonical.RequiredCapabilities))
	for _, k := range canonical.RequiredCapabilities {
		m[k] = canonical.CapUnknown
	}
	return m
}

func pickName(live, registry, id string) string {
	if live != "" {
		return live
	}
	if registry != "" {
		return registry
	}
	return id
}

func cloneCaps(src map[string]canonical.CapabilityState) map[string]canonical.CapabilityState {
	out := make(map[string]canonical.CapabilityState, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneEvidence(src map[string]canonical.Evidence) map[string]canonical.Evidence {
	out := make(map[string]canonical.Evidence, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
```

Also remove `"sort"` from the import block.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/registry/ -v`
Expected: PASS (all validation + enrichment tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/registry/registry.go
git add internal/registry/registry.go internal/registry/registry_test.go
git commit -m "feat(registry): live-catalog enrichment with exact-match + auto normalization"
```

---

## Task 3: OpenAI adapter — interface, render, handler, route

**Files:**
- Modify: `internal/adapter/openai/adapter.go` (add interface, Config field, route)
- Create: `internal/adapter/openai/modelcaps.go` (wire + render + handler)
- Test: `internal/adapter/openai/modelcaps_test.go`

**Interfaces:**
- Consumes: `canonical.CapabilityCatalog`, `writeJSON` (`errors.go:83`), chi router.
- Produces: `type ModelCapabilityCatalog interface { ModelCapabilities() canonical.CapabilityCatalog }`; `Config.ModelCapabilities ModelCapabilityCatalog`; handler `handleModelCapabilities`; route `GET /model-capabilities`.

- [ ] **Step 1: Add the seam interface, Config field, and route**

In `internal/adapter/openai/adapter.go`, add after the `ModelCatalog` interface (`:112`):

```go
// ModelCapabilityCatalog is the consumer-defined interface used by
// handleModelCapabilities to obtain the enriched per-model capability catalog
// (live Kiro catalog fused with the embedded registry). The concrete combiner
// is wired in cmd/otto-gateway/main.go so this package does not import
// internal/registry or internal/pool (TRST-04). May be nil in a misconfigured
// construction; the handler then returns an empty list.
type ModelCapabilityCatalog interface {
	ModelCapabilities() canonical.CapabilityCatalog
}
```

In the `Config` struct (after `ModelCatalog` field, `:139`), add:

```go
	// ModelCapabilities supplies the enriched capability catalog for
	// GET /model-capabilities. May be nil (handler returns an empty list).
	ModelCapabilities ModelCapabilityCatalog
```

In `RegisterRoutes` (`:186-190`), add the route:

```go
	r.Get("/models", a.handleModels)
	r.Get("/model-capabilities", a.handleModelCapabilities)
```

- [ ] **Step 2: Write failing handler + render tests**

Create `internal/adapter/openai/modelcaps_test.go`:

```go
package openai

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
)

// fakeCapCatalog is a test-local ModelCapabilityCatalog.
type fakeCapCatalog struct{ cat canonical.CapabilityCatalog }

func (f *fakeCapCatalog) ModelCapabilities() canonical.CapabilityCatalog { return f.cat }

func mountedCapAdapter(seam ModelCapabilityCatalog) *httptest.Server {
	a := New(Config{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
		ModelCapabilities: seam,
	})
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) { a.RegisterRoutes(sub) })
	return httptest.NewServer(r)
}

func sampleCatalog() canonical.CapabilityCatalog {
	return canonical.CapabilityCatalog{
		RegistryRevision: "sha256-abc",
		GeneratedAt:      time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
		Entries: []canonical.ModelCapability{
			{ID: "auto", Name: "Automatic", Available: true, SelectionMode: "automatic",
				Capabilities: map[string]canonical.CapabilityState{"completion": "unknown", "tools": "unknown", "vision": "unknown", "reasoning": "unknown"},
				Evidence:     map[string]canonical.Evidence{}},
			{ID: "claude-opus-4.8", Name: "Claude Opus 4.8", Available: true, SelectionMode: "explicit",
				Capabilities: map[string]canonical.CapabilityState{"completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown"},
				Evidence:     map[string]canonical.Evidence{"completion": {Source: "kiro_declared", Reference: "live catalog", VerifiedAt: "2026-07-16"}}},
			{ID: "ghost", Name: "Ghost", Available: true, SelectionMode: "explicit",
				Capabilities: map[string]canonical.CapabilityState{"completion": "unknown", "tools": "unknown", "vision": "unknown", "reasoning": "unknown"},
				Evidence:     map[string]canonical.Evidence{}},
		},
	}
}

// capListWire mirrors the response for decoding in tests.
type capListWire struct {
	Object           string `json:"object"`
	RegistryRevision string `json:"registry_revision"`
	GeneratedAt      string `json:"generated_at"`
	Data             []struct {
		ID            string                       `json:"id"`
		Name          string                       `json:"name"`
		Available     bool                         `json:"available"`
		SelectionMode string                       `json:"selection_mode"`
		Capabilities  map[string]string            `json:"capabilities"`
		Evidence      map[string]map[string]string `json:"evidence"`
	} `json:"data"`
}

func getCaps(t *testing.T, srv *httptest.Server) (*http.Response, capListWire) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/model-capabilities", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	var out capListWire
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp, out
}

func TestModelCapabilities_Shape(t *testing.T) {
	srv := mountedCapAdapter(&fakeCapCatalog{cat: sampleCatalog()})
	defer srv.Close()
	resp, list := getCaps(t, srv)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type: got %q", ct)
	}
	if list.Object != "list" {
		t.Errorf("object: got %q, want list", list.Object)
	}
	if list.RegistryRevision != "sha256-abc" {
		t.Errorf("registry_revision: got %q", list.RegistryRevision)
	}
	if list.GeneratedAt != "2026-07-16T12:00:00Z" {
		t.Errorf("generated_at: got %q, want RFC3339 UTC", list.GeneratedAt)
	}
	if len(list.Data) == 0 || list.Data[0].ID != "auto" || list.Data[0].SelectionMode != "automatic" {
		t.Fatalf("auto not first/automatic: %+v", list.Data)
	}
}

func TestModelCapabilities_RegisteredStates(t *testing.T) {
	srv := mountedCapAdapter(&fakeCapCatalog{cat: sampleCatalog()})
	defer srv.Close()
	resp, list := getCaps(t, srv)
	defer func() { _ = resp.Body.Close() }()

	var found bool
	for _, e := range list.Data {
		if e.ID == "claude-opus-4.8" {
			found = true
			if e.Capabilities["completion"] != "supported" {
				t.Errorf("completion: got %q, want supported", e.Capabilities["completion"])
			}
			if _, ok := e.Evidence["completion"]; !ok {
				t.Errorf("completion evidence missing")
			}
		}
	}
	if !found {
		t.Fatal("claude-opus-4.8 not in response")
	}
}

func TestModelCapabilities_UnknownModel(t *testing.T) {
	srv := mountedCapAdapter(&fakeCapCatalog{cat: sampleCatalog()})
	defer srv.Close()
	resp, list := getCaps(t, srv)
	defer func() { _ = resp.Body.Close() }()

	for _, e := range list.Data {
		if e.ID == "ghost" {
			for k, v := range e.Capabilities {
				if v != "unknown" {
					t.Errorf("ghost cap %q: got %q, want unknown", k, v)
				}
			}
			if len(e.Evidence) != 0 {
				t.Errorf("ghost evidence should be empty")
			}
		}
	}
}

func TestModelCapabilities_NilSeamEmptyList(t *testing.T) {
	srv := mountedCapAdapter(nil)
	defer srv.Close()
	resp, list := getCaps(t, srv)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if list.Object != "list" {
		t.Errorf("object: got %q, want list", list.Object)
	}
	if len(list.Data) != 0 {
		t.Errorf("nil seam should yield empty data, got %d entries", len(list.Data))
	}
}

func TestModelCapabilities_NoLeakage(t *testing.T) {
	srv := mountedCapAdapter(&fakeCapCatalog{cat: sampleCatalog()})
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/model-capabilities", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	for _, banned := range []string{"KIRO_", "worker", "slot", "/Users/", "AUTH_TOKEN", "prompt"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("response leaks %q: %s", banned, raw)
		}
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/adapter/openai/ -run ModelCapabilities -v`
Expected: FAIL (handler/render undefined).

- [ ] **Step 4: Implement render + handler**

Create `internal/adapter/openai/modelcaps.go`:

```go
package openai

import (
	"net/http"
	"time"

	"otto-gateway/internal/canonical"
)

// ----------------------------------------------------------------------------
// GET /v1/model-capabilities render shapes (spec 2026-07-16 §6.1)
//
// Gateway-owned endpoint (NOT the OpenAI spec). registry_revision is a content
// hash; generated_at is the only non-deterministic field. Field ordering
// mirrors the spec example.
// ----------------------------------------------------------------------------

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
	Evidence      map[string]evidenceWire `json:"evidence"`       // supported/unsupported only
}

type evidenceWire struct {
	Source     string `json:"source"`
	Reference  string `json:"reference"`
	VerifiedAt string `json:"verified_at"`
	Notes      string `json:"notes,omitempty"`
}

// capabilityCatalogToWire maps the canonical catalog to the wire shape. Map
// keys marshal sorted by encoding/json → deterministic output.
func capabilityCatalogToWire(cat canonical.CapabilityCatalog) modelCapabilityList {
	data := make([]modelCapabilityEntry, 0, len(cat.Entries))
	for _, e := range cat.Entries {
		caps := make(map[string]string, len(e.Capabilities))
		for k, v := range e.Capabilities {
			caps[k] = string(v)
		}
		ev := make(map[string]evidenceWire, len(e.Evidence))
		for k, v := range e.Evidence {
			ev[k] = evidenceWire{Source: v.Source, Reference: v.Reference, VerifiedAt: v.VerifiedAt, Notes: v.Notes}
		}
		data = append(data, modelCapabilityEntry{
			ID:            e.ID,
			Name:          e.Name,
			Available:     e.Available,
			SelectionMode: e.SelectionMode,
			Capabilities:  caps,
			Evidence:      ev,
		})
	}
	return modelCapabilityList{
		Object:           "list",
		RegistryRevision: cat.RegistryRevision,
		GeneratedAt:      cat.GeneratedAt.UTC().Format(time.RFC3339),
		Data:             data,
	}
}

// handleModelCapabilities serves GET /model-capabilities. Auth is IP-allowlist
// only (prefix middleware owns it) — identical to /models, no bearer. No body
// decode (GET). When the seam is nil (misconfigured construction) it returns a
// well-formed empty list.
func (a *Adapter) handleModelCapabilities(w http.ResponseWriter, _ *http.Request) {
	if a.cfg.ModelCapabilities == nil {
		writeJSON(w, modelCapabilityList{Object: "list", Data: []modelCapabilityEntry{}})
		return
	}
	writeJSON(w, capabilityCatalogToWire(a.cfg.ModelCapabilities.ModelCapabilities()))
}
```

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/adapter/openai/ -run ModelCapabilities -v`
Expected: PASS. Then run the whole adapter package to confirm no regressions: `go test ./internal/adapter/openai/`.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/adapter/openai/adapter.go internal/adapter/openai/modelcaps.go
git add internal/adapter/openai/adapter.go internal/adapter/openai/modelcaps.go internal/adapter/openai/modelcaps_test.go
git commit -m "feat(openai): GET /v1/model-capabilities handler, render, and seam"
```

---

## Task 4: Wire the registry into `main.go`

**Files:**
- Modify: `cmd/otto-gateway/main.go`

**Interfaces:**
- Consumes: `registry.Load`, `registry.Registry.Enrich`, `openai.ModelCatalog` (`cat`), `openai.Config.ModelCapabilities`.
- Produces: a `modelCapabilityCatalog` combiner wired into the OpenAI adapter config.

- [ ] **Step 1: Add the combiner type**

Add near the other adapter-bridge types in `cmd/otto-gateway/main.go` (e.g. beside `openaiEngineAdapter`):

```go
// modelCapabilityCatalog combines the live pool catalog with the embedded
// capability registry for GET /v1/model-capabilities. It satisfies
// openai.ModelCapabilityCatalog. catalog may be nil (KIRO_CMD unset) → Enrich
// receives an empty live list and returns auto-only.
type modelCapabilityCatalog struct {
	catalog openai.ModelCatalog
	reg     *registry.Registry
}

func (m modelCapabilityCatalog) ModelCapabilities() canonical.CapabilityCatalog {
	var live []canonical.ModelInfo
	if m.catalog != nil {
		live = m.catalog.Models()
	}
	return m.reg.Enrich(live, time.Now())
}
```

Ensure imports include `"otto-gateway/internal/registry"` and `"otto-gateway/internal/canonical"` (canonical is likely already imported; add registry).

- [ ] **Step 2: Load the registry once (fatal on error) and wire it**

In the OpenAI adapter construction block (`main.go:600-628`), after `cat` is resolved and before `openai.New(...)`, load the registry and pass the seam:

```go
		// Load the embedded capability registry once. A load error is a
		// build/ship error (invalid embedded JSON) — fail fast at startup.
		capReg, err := registry.Load()
		if err != nil {
			return fmt.Errorf("model capability registry: %w", err)
		}
		openaiAdapter = openai.New(openai.Config{
			Logger:            logger,
			Engine:            eng,
			ModelCatalog:      cat,
			ModelCapabilities: modelCapabilityCatalog{catalog: cat, reg: capReg},
			Registry:          registryForAdapters,
			EngineForSession:  openaiEngineForSession,
			KiroCWD:           cfg.KiroCWD,
			StreamIdleTimeout: streamIdle,
			ToolAliases:       cfg.ToolAliases,
		})
```

> Confirm the enclosing function returns `error` so `return fmt.Errorf(...)` is valid. If it does not, capture the error and route it through the existing fatal-log path used elsewhere in `main.go` (match the surrounding convention rather than introducing a new one).

- [ ] **Step 3: Build to verify wiring compiles**

Run: `go build ./cmd/otto-gateway/`
Expected: success. Then `go vet ./cmd/otto-gateway/`.

- [ ] **Step 4: Commit**

```bash
gofmt -w cmd/otto-gateway/main.go
git add cmd/otto-gateway/main.go
git commit -m "feat(main): load capability registry and wire /v1/model-capabilities"
```

---

## Task 5: E2E — fake-kiro catalog knob + endpoint assertions

**Files:**
- Modify: `tests/e2e/cmd/fake-kiro-cli/main.go`
- Modify: `tests/e2e/openai_e2e_test.go`

**Interfaces:**
- Consumes: `FakeKiro`, `mergeEnv`, `bootGateway`, `ollamaRequest`, `readAll` (e2e helpers).
- Produces: `GW_FAKE_KIRO_MODELS` env knob (format `id:name` comma-separated); a `ModelCapabilities` subtest.

- [ ] **Step 1: Add the catalog env knob to fake-kiro (write test expectation first)**

The E2E test in Step 3 will fail until the fake can advertise a scripted catalog. Add to `tests/e2e/cmd/fake-kiro-cli/main.go`:

Add import `"strings"` and a const:

```go
const envModels = "GW_FAKE_KIRO_MODELS" // "id:name,id:name" — overrides the default catalog
```

Replace the hardcoded `availableModels` in the `session/new` case with a call:

```go
		case "session/new":
			respond(idRaw, map[string]any{
				"sessionId": "e2e-session-1",
				"models": map[string]any{
					"availableModels": availableModels(),
					"currentModelId":  "auto",
				},
			})
```

Add the helper:

```go
// availableModels returns the scripted catalog. GW_FAKE_KIRO_MODELS overrides
// the default ("auto:Auto,sonnet:Sonnet") with a comma-separated list of
// "modelId:name" pairs (name defaults to id when the ":name" half is omitted).
func availableModels() []map[string]any {
	spec := os.Getenv(envModels)
	if spec == "" {
		return []map[string]any{
			{"modelId": "auto", "name": "Auto"},
			{"modelId": "sonnet", "name": "Sonnet"},
		}
	}
	var out []map[string]any
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		id, name, found := strings.Cut(pair, ":")
		if !found || name == "" {
			name = id
		}
		out = append(out, map[string]any{"modelId": id, "name": name})
	}
	return out
}
```

- [ ] **Step 2: Verify the fake still builds**

Run: `go build ./tests/e2e/cmd/fake-kiro-cli/`
Expected: success (default behavior unchanged when the env var is unset — existing e2e suites keep passing).

- [ ] **Step 3: Add the E2E assertions**

Add a new top-level test to `tests/e2e/openai_e2e_test.go` (own `bootGateway` so the fake-kiro catalog knob applies without perturbing the shared `TestE2E_OpenAI` boot):

```go
// TestE2E_OpenAI_ModelCapabilities boots a gateway backed by the fake kiro with
// a scripted catalog: a registered model (claude-sonnet-4.5), an unknown model
// (unknown-model-zzz, absent from the registry). It asserts the
// /v1/model-capabilities contract. (spec §11.4)
func TestE2E_OpenAI_ModelCapabilities(t *testing.T) {
	gateOrSkip(t)
	cmd, env := FakeKiro(t, Script{})
	baseURL, cleanup := bootGateway(t, mergeEnv(env, map[string]string{
		"KIRO_CMD":            cmd,
		"GW_FAKE_KIRO_MODELS": "auto:Auto,claude-sonnet-4.5:Claude Sonnet 4.5,unknown-model-zzz:Unknown Model",
	}))
	defer cleanup()

	resp := ollamaRequest(t, http.MethodGet, baseURL+"/v1/model-capabilities", nil, "Bearer e2e-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, readAll(resp))
	}

	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID            string            `json:"id"`
			SelectionMode string            `json:"selection_mode"`
			Capabilities  map[string]string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Object != "list" || len(list.Data) == 0 || list.Data[0].ID != "auto" {
		t.Fatalf("auto not first / bad envelope: %+v", list)
	}

	byID := map[string]map[string]string{}
	for _, e := range list.Data {
		byID[e.ID] = e.Capabilities
	}

	// 1+2. Registered model present with its verified completion state.
	reg, ok := byID["claude-sonnet-4.5"]
	if !ok {
		t.Fatalf("registered model claude-sonnet-4.5 not returned; got ids %v", idsOf(list.Data))
	}
	if reg["completion"] != "supported" {
		t.Errorf("claude-sonnet-4.5 completion: got %q, want supported", reg["completion"])
	}

	// 3. Unknown model present but all-unknown.
	unk, ok := byID["unknown-model-zzz"]
	if !ok {
		t.Fatalf("unknown model not returned; got ids %v", idsOf(list.Data))
	}
	for _, k := range []string{"completion", "tools", "vision", "reasoning"} {
		if unk[k] != "unknown" {
			t.Errorf("unknown-model-zzz %q: got %q, want unknown", k, unk[k])
		}
	}

	// 4. A registry model absent from the live catalog is NOT returned.
	if _, present := byID["claude-haiku-4.5"]; present {
		t.Errorf("stale registry model claude-haiku-4.5 leaked into response")
	}
}

// idsOf is a small diagnostic helper local to this test.
func idsOf[T any](rows []struct {
	ID            string            `json:"id"`
	SelectionMode string            `json:"selection_mode"`
	Capabilities  map[string]string `json:"capabilities"`
}) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}
```

> If the anonymous-struct generic helper is awkward against the linter, inline the id collection in the test body instead of using `idsOf`. Keep whichever compiles cleanly under `go vet`.

- [ ] **Step 4: Run the E2E test**

Run: `GW_E2E=1 go test -tags e2e ./tests/e2e/ -run ModelCapabilities -v`
Expected: PASS (or a clean skip only if the fake-kiro gate is unmet — it should run since FakeKiro sets `GW_KIRO_BIN`).

- [ ] **Step 5: Commit**

```bash
gofmt -w tests/e2e/cmd/fake-kiro-cli/main.go tests/e2e/openai_e2e_test.go
git add tests/e2e/cmd/fake-kiro-cli/main.go tests/e2e/openai_e2e_test.go
git commit -m "test(e2e): model-capabilities registered/unknown/stale assertions via fake-kiro catalog knob"
```

---

## Task 6: Claude-family vendor-documentation evidence

**Files:**
- Modify: `internal/registry/registry.json`
- Test: `internal/registry/registry_test.go` (the existing `TestLoad_EmbeddedRegistryValid` covers re-validation)

**Interfaces:** none new — data + validation only.

- [ ] **Step 1: Research each Claude model's capabilities from OFFICIAL sources**

For each `claude-*` id in the registry, confirm — using the `claude-api` skill and/or official `docs.anthropic.com` model pages (NOT memory, blogs, or marketing) — whether the exact model supports:
- `vision` (image input),
- `tools` (tool use / function calling),
- `reasoning` (extended thinking).

Capture the official documentation URL for each confirmed capability as the `reference`. **If a specific `claude-*` id cannot be unambiguously mapped to an official Anthropic model page, leave that capability `unknown`** (do not guess from the family name).

Invoke: `Skill(claude-api)` for verified model facts and the canonical docs URLs.

- [ ] **Step 2: Update the Claude entries with `vendor_documentation` evidence**

For each confirmed capability, change the state from `unknown` to `supported` (or `unsupported`) and add a matching evidence object. Example shape for one model (use the ACTUAL confirmed states + real doc URL from Step 1):

```json
{ "id": "claude-opus-4.8", "name": "Claude Opus 4.8",
  "capabilities": { "completion": "supported", "tools": "supported", "vision": "supported", "reasoning": "supported" },
  "evidence": {
    "completion": { "source": "kiro_declared",        "reference": "kiro session/new availableModels (live catalog)", "verified_at": "2026-07-16", "notes": "Listed as a selectable chat model in the live Kiro catalog." },
    "tools":      { "source": "vendor_documentation", "reference": "<official docs URL>", "verified_at": "2026-07-16", "notes": "Official tool-use support." },
    "vision":     { "source": "vendor_documentation", "reference": "<official docs URL>", "verified_at": "2026-07-16", "notes": "Official image-input support." },
    "reasoning":  { "source": "vendor_documentation", "reference": "<official docs URL>", "verified_at": "2026-07-16", "notes": "Official extended-thinking support." }
  }
}
```

Leave GPT-tier codenames and unmapped other-vendor ids at `completion`-only. Do not add evidence you cannot cite to an exact-id official page.

- [ ] **Step 3: Re-validate the registry**

Run: `go test ./internal/registry/ -run 'EmbeddedRegistryValid|Load' -v`
Expected: PASS — the updated registry still satisfies every validation rule (evidence keys equal the supported/unsupported set, valid sources, dates, references).

- [ ] **Step 4: Commit**

```bash
git add internal/registry/registry.json
git commit -m "feat(registry): Claude-family vendor-doc evidence for vision/tools/reasoning"
```

---

## Task 7: Documentation + maintenance rule

**Files:**
- Create: `docs/reference/model_capabilities.md`

**Interfaces:** none.

- [ ] **Step 1: Write the reference doc**

Create `docs/reference/model_capabilities.md` covering (each as a short section):
- The `GET /v1/model-capabilities` contract (envelope, `registry_revision`, `generated_at`, entry fields, three states, `auto` first).
- Availability (live Kiro catalog) vs. capability verification (registry) — and that a live-but-unregistered model is returned all-`unknown`, never omitted.
- Why agent-wide `promptCapabilities` cannot prove per-model support (it comes from `initialize`, not per model).
- That `/api/show`'s legacy `capabilities: ["completion","tools"]` is an Ollama-compat surface, NOT verified per-model evidence.
- Registry schema (JSON array, required keys, evidence policy, source types, date format) and where it lives (`internal/registry/registry.json`).
- How to research and add a new exact model id; how to retire a removed model; how to update evidence + verification dates; how to validate registry drift against a live Kiro catalog (compare `GET /v1/models` ids to registry ids).
- Why unknown models stay visible but should not be selected for capability-sensitive work.

Include the maintenance rule verbatim:

```markdown
> Adding or changing a Kiro model capability declaration requires an exact model
> ID, evidence, a verification date, registry validation, endpoint contract
> tests, and a review of whether the model is still present in the live Kiro
> catalog.
```

- [ ] **Step 2: Commit**

```bash
git add docs/reference/model_capabilities.md
git commit -m "docs: model-capabilities endpoint contract, registry schema, and maintenance rule"
```

---

## Task 8: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Format, vet, unit + race tests**

```bash
gofmt -l internal/ cmd/ tests/    # expect no output (all formatted)
go vet ./...
go test ./...
go test -race ./internal/registry/ ./internal/adapter/openai/ ./internal/pool/
```
Expected: all PASS, no vet findings.

- [ ] **Step 2: Security/static-analysis gate**

Run the repository's required gate (per `CLAUDE.md` / `docs/briefs/go_port_brief.md` §3.12). Discover the exact command from the Makefile:

```bash
grep -nE 'gosec|govulncheck|staticcheck|golangci' Makefile
```
Then run the discovered target (e.g. `make lint` / `make audit` / `gosec ./...`). Expected: no new G204 or other findings introduced by these changes. If a required tool is not installed, report the exact command and that it could not be run — do NOT claim it passed.

- [ ] **Step 3: Targeted regression + E2E**

```bash
go test ./internal/adapter/openai/ ./internal/adapter/ollama/ ./internal/registry/ ./internal/pool/
GW_E2E=1 go test -tags e2e ./tests/e2e/ -run 'Models|Tags|ModelCapabilities|Tool' -v
```
Expected: `/v1/models`, `/api/tags`↔`/v1/models` equality, `/api/show`, tool-call, and the new capability assertions all PASS. E2E tests may skip cleanly if kiro/fake-kiro gates are unmet; a skip is not a pass — note it in the completion report.

- [ ] **Step 4: Confirm no unintended wire drift**

```bash
go test ./internal/adapter/openai/ -run 'TestModels|TestModelListRender' -v
```
Expected: the existing `/v1/models` tests PASS unchanged (endpoint untouched).

- [ ] **Step 5: Completion report**

Write the completion report required by the handoff §"Completion report": verified branch state; architecture; endpoint contract; initial registry entries + evidence sources; models/capabilities deliberately left `unknown`; tests + verification commands with results; files changed; client-facing assumptions/follow-up; and whether the worktree is clean. Do NOT push or release.

---

## Self-Review

**Spec coverage:** §2 states/keys → Task 1 types + Global Constraints. §5 package layout → Tasks 1,3,4. §6 contract → Task 3. §7 enrichment invariants → Task 2 (auto-first, exact-match, unregistered→unknown, stale omitted, live-name-wins, no-mutation, empty→auto-only). §8 schema+validation → Task 1 (all ten rejection classes + evidence-key-equality). §9 seed → Tasks 1 (completion) + 6 (Claude vendor docs). §10 compatibility → Task 8 Steps 3–4 regression. §11 tests → Tasks 1,2,3,5. §12 docs+maintenance rule → Task 7. §13 verification → Task 8. All sections mapped.

**Placeholder scan:** The only intentional `<official docs URL>` / `<...>` placeholders are in Task 6, which is explicitly a research task that fills them from official sources at execution time — that is the correct place for them, not a plan defect. No "TBD"/"handle edge cases"/"add validation" hand-waves elsewhere; all code blocks are complete.

**Type consistency:** `CapabilityState`/`CapUnknown`/`CapSupported`/`CapUnsupported`, `RequiredCapabilities`, `Evidence`, `ModelCapability`, `CapabilityCatalog` used identically across Tasks 1–4. `Registry.Enrich(live, now)` signature matches between Task 1 stub, Task 2 impl, and Task 4 caller. `ModelCapabilityCatalog.ModelCapabilities()` matches between Task 3 interface, Task 3 handler, and Task 4 combiner. `modelCapabilityList`/`modelCapabilityEntry`/`evidenceWire` consistent between Task 3 impl and its test's decode struct. `GW_FAKE_KIRO_MODELS` knob consistent between Task 5 fake-kiro impl and the E2E test env.
