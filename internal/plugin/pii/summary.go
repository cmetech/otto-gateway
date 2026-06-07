// Package pii contains the PIIRedactionHook (Phase 8 slice 4) and the
// redaction-summary API seam (slice 3). Per Phase 8 D-04 (08-CONTEXT.md
// §LoggingHook ordering): LoggingHook reads SummaryFromContext to emit a
// structured 'redacted={Email:2, SSN:1}' field. The seam MUST exist even
// when v1 LoggingHook chooses not to emit, because slice 4 needs a
// concrete consumer to target.
//
// Chain-order invariant (08-CONTEXT.md D-04):
//
//	Pre: RequestID → Auth → PIIRedaction → Logging
//
// PIIRedactionHook (slice 4) is the THIRD Pre hook; it stamps a populated
// Summary on ctx via WithSummary. LoggingHook (slice 3) is the FOURTH Pre
// hook AND the only Post hook; it reads the Summary via
// SummaryFromContext. Reordering hooks (e.g., putting Logging before PII)
// breaks this contract — the LoggingHook will observe raw PII content AND
// an empty Summary.
//
// T-8-PII-2 (accepted): an empty Summary serializes as "{}" — operators
// learn "no PII was found in this request", which is itself a mild
// information disclosure. Accepted: counts are aggregate, not values; the
// auditable-redaction-provenance value outweighs the leak.
//
// T-8-GO-LEAK (mitigated): Summary.Add is sync.Mutex-guarded so any
// future async hook can call it concurrently without a race; the v1
// walker is single-threaded so the lock is overhead-only in v1 but
// API-stable for v2.
package pii

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// RedactionCount is the wire row for a single entity's redaction tally.
// Used as a typed alternative to map[string]int when consumers want a
// slice-of-rows JSON shape (e.g., for an audit-log hook in Phase V2).
// JSON tags are load-bearing for slog serialization.
//
// v1 LoggingHook emits the map[string]int form (via Summary.Counts) for
// compact slog output; RedactionCount is provided as part of the public
// API surface so future consumers (or Phase 8 slice 4's tests) can use
// the typed row form.
type RedactionCount struct {
	Entity string `json:"entity"`
	Count  int    `json:"count"`
}

// Summary aggregates per-entity redaction counts for a single
// canonical.ChatRequest. It is stamped on ctx by PIIRedactionHook (slice
// 4) and read by LoggingHook (slice 3) — see file docstring for the
// chain-order invariant.
//
// Race-safety: Add uses a sync.Mutex even though v1 PIIRedactionHook
// walks content serially. Locking in v1 means a future parallel walker
// (e.g., a content-moderation hook iterating multiple tool outputs at
// once) does not have to renegotiate the API to gain concurrency.
//
// Nil-safety: Add is a no-op on a nil receiver and Counts returns nil
// on a nil receiver. This lets LoggingHook call SummaryFromContext +
// dereference unconditionally without ok-checking the map population
// state (slice 4 stamps before any populator runs are possible).
type Summary struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewSummary returns a fresh, empty Summary ready for Add calls.
// PIIRedactionHook (slice 4) constructs one per request and stamps it
// onto ctx via WithSummary.
func NewSummary() *Summary {
	return &Summary{counts: make(map[string]int)}
}

// Add increments the count for entity. It is safe to call concurrently
// from multiple goroutines (sync.Mutex-guarded) and on a nil receiver
// (no-op).
//
// The nil-receiver no-op exists so LoggingHook + future consumers can do:
//
//	s, _ := pii.SummaryFromContext(ctx)
//	s.Add("Email") // safe even when s is nil
//
// without an explicit ok-check at every call site.
func (s *Summary) Add(entity string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = make(map[string]int)
	}
	s.counts[entity]++
}

// Counts returns a SNAPSHOT copy of the per-entity counts. Returning a
// snapshot (rather than the live map) prevents iteration-vs-mutation
// races at the caller — LoggingHook iterates via slog encoding while a
// concurrent populator MAY still be writing in pathological future
// configurations.
//
// Returns nil on a nil receiver. Returns a non-nil empty map (not nil)
// when the receiver is non-nil but no entities have been added — this
// matches TestSummary_EmptyCountsSerializable's "{}" wire expectation.
func (s *Summary) Counts() map[string]int {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.counts))
	for k, v := range s.counts {
		out[k] = v
	}
	return out
}

// MarshalJSON renders the Summary as a compact JSON object suitable for
// embedding in a slog record. Nil-receiver renders "null".
//
// The wire shape is {"Email":2,"SSN":1} (object keyed by entity name)
// because that's the most operator-readable form for one-line slog
// records. Consumers needing the typed RedactionCount[] form can build
// it from Counts().
func (s *Summary) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	b, err := json.Marshal(s.Counts())
	if err != nil {
		return nil, fmt.Errorf("pii.summary: marshal: %w", err)
	}
	return b, nil
}

// summaryKey is the unexported, struct-typed context key used by
// WithSummary + SummaryFromContext. Mirrors Pattern B from PATTERNS
// (typed-key ctx + accessor pair) — Go type-identity rules prevent
// cross-package key collision (T-8-RID-1-style mitigation; here the
// concern is that another package can't spoof or read the PII summary).
//
// The single name field is for ctx String() debugging only; it does not
// participate in equality (struct-type identity does).
type summaryKey struct{ name string }

// summaryCtxKey is the single canonical key value shared by every
// WithSummary / SummaryFromContext call site in this package.
var summaryCtxKey = summaryKey{name: "pii-redaction-summary"}

// WithSummary returns a child ctx carrying s under the unexported
// summaryCtxKey. PIIRedactionHook (slice 4) calls this once per request
// before mutating Messages so LoggingHook can read the populated counts
// in its After phase.
func WithSummary(ctx context.Context, s *Summary) context.Context {
	return context.WithValue(ctx, summaryCtxKey, s)
}

// SummaryFromContext returns the Summary stamped on ctx by WithSummary,
// or (nil, false) when no Summary is present. LoggingHook uses the bool
// to decide whether to emit the "redacted" attr — the absent case is
// graceful degradation when PII is disabled or slice 4 hasn't run yet.
func SummaryFromContext(ctx context.Context) (*Summary, bool) {
	if v, ok := ctx.Value(summaryCtxKey).(*Summary); ok {
		return v, true
	}
	return nil, false
}
