package canonical

import "testing"

// TestCanonicalTypes_PresenceAndZeroValues asserts that the canonical types
// added in Phase 1.1 (D-01..D-04) exist with the expected fields and
// zero-value semantics.
func TestCanonicalTypes_PresenceAndZeroValues(t *testing.T) {
	// (1) StopReason: zero value must be StopUnknown (D-02 forward-compat).
	var s StopReason
	if s != StopUnknown {
		t.Errorf("zero StopReason: got %v, want StopUnknown (%v)", s, StopUnknown)
	}
	// All seven constants must be distinct (StopError added in Phase 8
	// Plan 08-02 Task 3 for AuthHook's short-circuit envelope).
	all := []StopReason{
		StopUnknown,
		StopEndTurn,
		StopMaxTokens,
		StopMaxTurnRequests,
		StopRefusal,
		StopCancelled,
		StopError,
	}
	seen := make(map[StopReason]bool, len(all))
	for _, v := range all {
		if seen[v] {
			t.Errorf("StopReason value %v appears more than once in {StopUnknown, StopEndTurn, StopMaxTokens, StopMaxTurnRequests, StopRefusal, StopCancelled, StopError}", v)
		}
		seen[v] = true
	}
	if len(seen) != 7 {
		t.Errorf("expected 7 distinct StopReason values, got %d", len(seen))
	}

	// (2) ModelInfo: zero value is empty struct; fields round-trip.
	var m ModelInfo
	if m.ID != "" || m.Name != "" {
		t.Errorf("zero ModelInfo: got {ID:%q Name:%q}, want both empty", m.ID, m.Name)
	}
	m = ModelInfo{ID: "claude-sonnet-4-7", Name: "Claude Sonnet 4.7"}
	if m.ID != "claude-sonnet-4-7" {
		t.Errorf("ModelInfo.ID round-trip: got %q, want %q", m.ID, "claude-sonnet-4-7")
	}
	if m.Name != "Claude Sonnet 4.7" {
		t.Errorf("ModelInfo.Name round-trip: got %q, want %q", m.Name, "Claude Sonnet 4.7")
	}

	// (3) PromptCapabilities: zero value has all flags false; fields round-trip.
	var p PromptCapabilities
	if p.Image || p.Audio || p.EmbeddedContext {
		t.Errorf("zero PromptCapabilities: got {Image:%v Audio:%v EmbeddedContext:%v}, want all false",
			p.Image, p.Audio, p.EmbeddedContext)
	}
	p = PromptCapabilities{Image: true, Audio: false, EmbeddedContext: true}
	if !p.Image {
		t.Error("PromptCapabilities.Image round-trip: got false, want true")
	}
	if p.Audio {
		t.Error("PromptCapabilities.Audio round-trip: got true, want false")
	}
	if !p.EmbeddedContext {
		t.Error("PromptCapabilities.EmbeddedContext round-trip: got false, want true")
	}

	// (4) ResourceLinkBlock.Name is settable and round-trips (D-04 / D-14).
	var b ResourceLinkBlock
	if b.Name != "" {
		t.Errorf("zero ResourceLinkBlock.Name: got %q, want empty", b.Name)
	}
	b.Name = "foo.txt"
	if b.Name != "foo.txt" {
		t.Errorf("ResourceLinkBlock.Name round-trip: got %q, want %q", b.Name, "foo.txt")
	}
}
