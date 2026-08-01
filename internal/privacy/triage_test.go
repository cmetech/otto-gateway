package privacy

import (
	"errors"
	"testing"
)

func TestPrivacyTriageCapabilityListsInspectsAndClearsLifecycle(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{})
	capability := service.TriageCapability()
	lease, err := service.store.Acquire("triage-scope", ProfileStrict)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	entry, _, err := lease.GetOrCreate("IPv4", "10.23.45.67", ProvenanceInput, func(uint32) (string, error) {
		return "192.0.2.44", nil
	})
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	scopes := capability.ListScopes()
	if len(scopes) != 1 || scopes[0].ID != "triage-scope" || scopes[0].Entries != 1 || scopes[0].InFlight != 1 {
		t.Fatalf("ListScopes=%+v, want active triage-scope", scopes)
	}
	entries, err := capability.InspectScope("triage-scope")
	if err != nil || len(entries) != 1 || entries[0] != entry {
		t.Fatalf("InspectScope=(%+v,%v), want %+v", entries, err, entry)
	}
	if result, err := capability.ClearScope("triage-scope"); err != nil || result != ClearClosing {
		t.Fatalf("ClearScope(active)=(%q,%v), want closing", result, err)
	}
	lease.Release()
	if result, err := capability.ClearScope("triage-scope"); err != nil || result != ClearCompleted {
		t.Fatalf("ClearScope(repeated)=(%q,%v), want completed", result, err)
	}
	if _, err := capability.InspectScope("missing"); err == nil {
		t.Fatal("InspectScope(missing) succeeded")
	}
}

func TestPrivacyTriageCapabilityClearAllReportsClosingWhenAnyScopeIsActive(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{})
	capability := service.TriageCapability()
	active, err := service.store.Acquire("active", ProfileStrict)
	if err != nil {
		t.Fatal(err)
	}
	idle, err := service.store.Acquire("idle", ProfileStrict)
	if err != nil {
		t.Fatal(err)
	}
	idle.Release()

	if result := capability.ClearAllScopes(); result != ClearClosing {
		t.Fatalf("ClearAllScopes=%q, want closing", result)
	}
	active.Release()
	if result := capability.ClearAllScopes(); result != ClearCompleted {
		t.Fatalf("repeated ClearAllScopes=%q, want completed", result)
	}
}

func TestPrivacyTriageCapabilityIsNilSafeAndUnavailableWithoutStrictStore(t *testing.T) {
	var nilService *Service
	capability := nilService.TriageCapability()
	if scopes := capability.ListScopes(); len(scopes) != 0 {
		t.Fatalf("nil ListScopes=%+v, want empty", scopes)
	}
	if _, err := capability.InspectScope("missing"); !errors.Is(err, ErrTriageUnavailable) {
		t.Fatalf("nil InspectScope error=%v, want unavailable", err)
	}
	if _, err := capability.ClearScope("missing"); !errors.Is(err, ErrTriageUnavailable) {
		t.Fatalf("nil ClearScope error=%v, want unavailable", err)
	}
	if result := capability.ClearAllScopes(); result != ClearCompleted {
		t.Fatalf("nil ClearAllScopes=%q, want completed", result)
	}
}
