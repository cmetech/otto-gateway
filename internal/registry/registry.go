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
		// Defense-in-depth: the exact-count and per-required-key checks above
		// already guarantee only the four required keys are present, so this
		// loop is a redundant safety net rejecting any capability key outside
		// the required set.
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
		for capName, ev := range e.Evidence {
			if _, want := supportedSet[capName]; !want {
				return nil, fmt.Errorf("registry: %q: evidence for capability %q not in supported/unsupported state", e.ID, capName)
			}
			if _, ok := validSources[ev.Source]; !ok {
				return nil, fmt.Errorf("registry: %q: capability %q: invalid evidence source %q", e.ID, capName, ev.Source)
			}
			if ev.Reference == "" {
				return nil, fmt.Errorf("registry: %q: capability %q: missing evidence reference", e.ID, capName)
			}
			if _, err := time.Parse("2006-01-02", ev.VerifiedAt); err != nil {
				return nil, fmt.Errorf("registry: %q: capability %q: invalid verified_at %q (want YYYY-MM-DD)", e.ID, capName, ev.VerifiedAt)
			}
			evidence[capName] = canonical.Evidence{
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
