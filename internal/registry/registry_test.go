package registry

import (
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
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
		// A single Decode ignores trailing content; the loader must reject it so a
		// malformed embedded file cannot ship with a silently-dropped second document.
		"trailing_data": `[{"id":"m","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{}}]{"ignored":true}`,
		// A null root decodes to a nil slice; the loader must fail fast rather than
		// certify an empty (all-unknown) registry.
		"null_root": `null`,
		// Whitespace-only id must be rejected like an empty id.
		"whitespace_id": `[{"id":"   ","capabilities":{"completion":"unknown","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{}}]`,
		// A supported capability whose evidence reference is whitespace-only is not
		// auditable — reject it exactly like an empty reference.
		"whitespace_reference": `[{"id":"m","capabilities":{"completion":"supported","tools":"unknown","vision":"unknown","reasoning":"unknown"},"evidence":{"completion":{"source":"kiro_declared","reference":"   ","verified_at":"2026-07-16"}}}]`,
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

// TestLoad_EmptyArrayOK locks the decision that an empty (but present) array is a
// valid degenerate registry — distinct from a null root, which must fail. An empty
// registry means every live model renders all-unknown.
func TestLoad_EmptyArrayOK(t *testing.T) {
	reg, err := load([]byte(`[]`))
	if err != nil {
		t.Fatalf("empty array should load, got: %v", err)
	}
	if len(reg.entries) != 0 {
		t.Errorf("empty array should index zero entries, got %d", len(reg.entries))
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
