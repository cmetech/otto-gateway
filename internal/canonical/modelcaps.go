// Package canonical — per-model capability types (spec 2026-07-16). Tag-free;
// wire JSON tags live in the adapter layer.
package canonical

import "time"

// CapabilityState is a machine-readable three-state capability value. A bool is
// never used where the source can be unknown.
type CapabilityState string

// The three valid capability states. Unknown is the honest default when a
// capability has not been verified against evidence.
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
