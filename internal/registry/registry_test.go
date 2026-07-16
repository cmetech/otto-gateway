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
		"invalid_state":               `[{"id":"m","capabilities":{"completion":"maybe","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{}}]`,
		"invalid_capability_name":     `[{"id":"m","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown","telepathy":"unknown"},"evidence":{}}]`,
		"missing_required_capability": `[{"id":"m","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown"},"evidence":{}}]`,
		"supported_without_evidence":  `[{"id":"m","capabilities":{"completion":"supported","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{}}]`,
		"missing_reference":           `[{"id":"m","capabilities":{"completion":"supported","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{"completion":{"source":"kiro_declared","reference":"","verified_at":"2026-07-16"}}}]`,
		"invalid_date":                `[{"id":"m","capabilities":{"completion":"supported","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{"completion":{"source":"kiro_declared","reference":"x","verified_at":"07-16-2026"}}}]`,
		"bad_source":                  `[{"id":"m","capabilities":{"completion":"supported","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{"completion":{"source":"a_blog_post","reference":"x","verified_at":"2026-07-16"}}}]`,
		"evidence_for_unknown_cap":    `[{"id":"m","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{"completion":{"source":"kiro_declared","reference":"x","verified_at":"2026-07-16"}}}]`,
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
