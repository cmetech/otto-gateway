// Phase 8 Plan 08-03 Task 1 — Wave 0 scaffold for pii.Summary (D-04 seam).
//
// These tests exercise the contract that LoggingHook (slice 3 consumer) and
// PIIRedactionHook (slice 4 producer) share through the ctx-attached
// Summary value. Tests must FAIL before Task 2 implements summary.go.
//
// Contract being scaffolded:
//   - NewSummary() returns a *Summary with an empty count map.
//   - Summary.Add(entity) increments the counter for entity (race-safe
//     because v1 uses sync.Mutex even though the walker is currently
//     single-threaded — locking down the safety contract before any async
//     hook lands).
//   - Summary.Counts() returns a snapshot map (decoupled from the live map
//     to prevent iteration-vs-mutation races).
//   - WithSummary(ctx, s) + SummaryFromContext(ctx) form a typed-key
//     accessor pair (Pattern B from PATTERNS).
//   - SummaryFromContext(ctx) returns (nil, false) when absent.
//   - Nil-receiver Summary.Add is a no-op (LoggingHook reads via the
//     accessor before any populator has run when PII is disabled).
package pii

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// TestSummary_AddIncrementsCount verifies the basic accumulator contract:
// two Add("Email") calls produce Counts()["Email"] == 2.
func TestSummary_AddIncrementsCount(t *testing.T) {
	s := NewSummary()
	s.Add("Email")
	s.Add("Email")
	s.Add("SSN")

	got := s.Counts()
	if got["Email"] != 2 {
		t.Errorf("Email count: got %d, want 2", got["Email"])
	}
	if got["SSN"] != 1 {
		t.Errorf("SSN count: got %d, want 1", got["SSN"])
	}
	if len(got) != 2 {
		t.Errorf("counts map length: got %d, want 2 (Email + SSN only)", len(got))
	}
}

// TestSummary_AddIsRaceSafe spawns N=100 goroutines each calling Add once
// for the same entity; after all join, the count must equal N. The test is
// run under `-race` so the detector catches any unlocked map writes.
//
// Rationale for retaining the property even though the v1 PII walker is
// single-threaded: locking down the safety contract NOW means future async
// hooks (e.g., a content-moderation PostHook that walks tool outputs in
// parallel) don't have to renegotiate the API to gain concurrency.
func TestSummary_AddIsRaceSafe(t *testing.T) {
	const n = 100
	s := NewSummary()
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			s.Add("Email")
		}()
	}
	wg.Wait()
	if got := s.Counts()["Email"]; got != n {
		t.Errorf("Email count after %d concurrent Adds: got %d, want %d", n, got, n)
	}
}

// TestWithSummary_RoundTrip confirms that a Summary stamped with WithSummary
// is recovered byte-for-byte (pointer-equal) via SummaryFromContext.
func TestWithSummary_RoundTrip(t *testing.T) {
	s := NewSummary()
	s.Add("Email")
	ctx := WithSummary(context.Background(), s)
	got, ok := SummaryFromContext(ctx)
	if !ok {
		t.Fatal("SummaryFromContext: ok=false, want true")
	}
	if got != s {
		t.Errorf("SummaryFromContext: got %p, want %p (pointer identity)", got, s)
	}
}

// TestSummaryFromContext_AbsentReturnsZero confirms the empty-ctx contract:
// no stamped Summary → (nil, false). LoggingHook relies on this to omit the
// "redacted" field when slice 4 hasn't run (or PII is disabled).
func TestSummaryFromContext_AbsentReturnsZero(t *testing.T) {
	got, ok := SummaryFromContext(context.Background())
	if ok {
		t.Errorf("SummaryFromContext on empty ctx: ok=true, want false")
	}
	if got != nil {
		t.Errorf("SummaryFromContext on empty ctx: got %v, want nil", got)
	}
}

// TestSummary_EmptyCountsSerializable confirms T-8-PII-2 accepted threat:
// an empty Summary serializes to "{}" without crashing slog. Operators see
// "redacted={}" (which leaks "no PII was found this request" — accepted in
// threat register per planner CONTEXT).
func TestSummary_EmptyCountsSerializable(t *testing.T) {
	s := NewSummary()
	marshaled, err := json.Marshal(s.Counts())
	if err != nil {
		t.Fatalf("json.Marshal empty Counts: %v", err)
	}
	if string(marshaled) != "{}" {
		t.Errorf("empty Counts JSON: got %q, want %q", string(marshaled), "{}")
	}
}

// TestSummary_NilSafeAdd documents the contract that LoggingHook (and any
// other consumer reading SummaryFromContext) may call Add on a nil pointer
// without panicking. This matters because slice 4 hasn't shipped yet and
// LoggingHook is exercised through paths where no populator has stamped
// the ctx. Nil-receiver Add is a no-op.
func TestSummary_NilSafeAdd(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil-receiver Add panicked: %v", r)
		}
	}()
	var s *Summary // nil
	s.Add("Email") // must not panic
}
