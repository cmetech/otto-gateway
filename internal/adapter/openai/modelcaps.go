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
