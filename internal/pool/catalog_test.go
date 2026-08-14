package pool

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

func TestNormalizeCatalog(t *testing.T) {
	got, err := normalizeCatalog([]canonical.ModelInfo{
		{ID: " auto ", Name: "Auto"},
		{ID: " gpt-5.6-sol ", Name: " GPT 5.6 Sol "},
		{ID: "gpt-5.6-sol", Name: "duplicate loses"},
		{ID: "claude-sonnet-5", Name: "Sonnet 5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []canonical.ModelInfo{
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
		{ID: "claude-sonnet-5", Name: "Sonnet 5"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v; want %#v", got, want)
	}
	if _, err := normalizeCatalog([]canonical.ModelInfo{{ID: "   "}}); !errors.Is(err, errInvalidCatalog) {
		t.Fatalf("blank ID error = %v", err)
	}
	if _, err := normalizeCatalog([]canonical.ModelInfo{{ID: "auto"}}); !errors.Is(err, errEmptyCatalog) {
		t.Fatalf("auto-only error = %v", err)
	}
}

func TestNormalizeCatalogReturnsIndependentSlice(t *testing.T) {
	input := []canonical.ModelInfo{{ID: " gpt-5.6-sol ", Name: " GPT 5.6 Sol "}}
	got, err := normalizeCatalog(input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = canonical.ModelInfo{ID: "mutated", Name: "mutated"}
	if want := []canonical.ModelInfo{{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized catalog changed after caller mutation: got %#v; want %#v", got, want)
	}
}

func TestCatalogStoreEqualMembershipIsUnchanged(t *testing.T) {
	s := initializedCatalogStore()
	before := s.snapshot()

	result, err := s.reconcile([]canonical.ModelInfo{
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
		{ID: "claude-sonnet-5", Name: "Sonnet 5"},
	}, time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CatalogUnchanged || result.PreviousCount != 2 || result.CandidateCount != 2 || result.PublishedCount != 2 || result.PendingRemovals != 0 {
		t.Fatalf("result = %+v; want unchanged two-model catalog", result)
	}
	after := s.snapshot()
	if after.Generation != before.Generation || !after.LastUpdatedAt.Equal(before.LastUpdatedAt) || after.LastOutcome != CatalogUnchanged {
		t.Fatalf("snapshot = %+v; want unchanged generation and update time", after)
	}
}

func TestCatalogStoreUpdatesNamesWithoutChangingMembership(t *testing.T) {
	s := initializedCatalogStore()
	result, err := s.reconcile([]canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
	}, time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CatalogMetadataUpdated || result.PublishedCount != 2 {
		t.Fatalf("result = %+v; want metadata update", result)
	}
	snapshot := s.snapshot()
	if snapshot.Generation != 2 || snapshot.Models[0].Name != "Claude Sonnet 5" || !snapshot.LastUpdatedAt.Equal(time.Unix(200, 0)) {
		t.Fatalf("snapshot = %+v; want published name change", snapshot)
	}
}

func TestCatalogStorePublishesExpansion(t *testing.T) {
	s := initializedCatalogStore()
	result, err := s.reconcile([]canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Sonnet 5"},
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
		{ID: "qwen3-coder-next", Name: "Qwen 3 Coder"},
	}, time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CatalogExpanded || result.PreviousCount != 2 || result.CandidateCount != 3 || result.PublishedCount != 3 {
		t.Fatalf("result = %+v; want expansion", result)
	}
	if snapshot := s.snapshot(); snapshot.Generation != 2 || !reflect.DeepEqual(snapshot.Models, []canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Sonnet 5"},
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
		{ID: "qwen3-coder-next", Name: "Qwen 3 Coder"},
	}) {
		t.Fatalf("snapshot = %+v; want expanded candidate", snapshot)
	}
}

func TestCatalogStoreConfirmsShrinkAfterIdenticalSecondCandidate(t *testing.T) {
	s := newCatalogStore(15 * time.Minute)
	s.initialize([]canonical.ModelInfo{{ID: "claude-sonnet-5"}, {ID: "gpt-5.6-sol"}}, time.Unix(100, 0))
	candidate := []canonical.ModelInfo{{ID: "claude-sonnet-5"}}
	first, err := s.reconcile(candidate, time.Unix(200, 0))
	if err != nil || first.Outcome != CatalogPendingShrink || len(s.snapshot().Models) != 2 {
		t.Fatalf("first=%+v err=%v snapshot=%+v", first, err, s.snapshot())
	}
	second, err := s.reconcile(candidate, time.Unix(300, 0))
	if err != nil || second.Outcome != CatalogShrinkConfirmed || len(s.snapshot().Models) != 1 {
		t.Fatalf("second=%+v err=%v snapshot=%+v", second, err, s.snapshot())
	}
}

func TestCatalogStoreStagesDifferentSecondShrinkCandidate(t *testing.T) {
	s := initializedCatalogStore()
	first, err := s.reconcile([]canonical.ModelInfo{{ID: "claude-sonnet-5", Name: "Sonnet 5"}}, time.Unix(200, 0))
	if err != nil || first.Outcome != CatalogPendingShrink {
		t.Fatalf("first = %+v, %v; want pending shrink", first, err)
	}
	second, err := s.reconcile([]canonical.ModelInfo{{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"}}, time.Unix(300, 0))
	if err != nil || second.Outcome != CatalogPendingShrink || second.PendingRemovals != 1 {
		t.Fatalf("second = %+v, %v; want replacement pending shrink", second, err)
	}
	if got := s.snapshot(); len(got.Models) != 2 || got.PendingRemovals != 1 {
		t.Fatalf("snapshot = %+v; want original catalog with one pending removal", got)
	}
	third, err := s.reconcile([]canonical.ModelInfo{{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"}}, time.Unix(400, 0))
	if err != nil || third.Outcome != CatalogShrinkConfirmed || !reflect.DeepEqual(s.snapshot().Models, []canonical.ModelInfo{{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"}}) {
		t.Fatalf("third = %+v, %v snapshot=%+v; want confirmed second candidate", third, err, s.snapshot())
	}
}

func TestCatalogStoreRecoveryClearsPendingShrink(t *testing.T) {
	s := initializedCatalogStore()
	if _, err := s.reconcile([]canonical.ModelInfo{{ID: "claude-sonnet-5", Name: "Sonnet 5"}}, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	result, err := s.reconcile([]canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Sonnet 5"},
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
	}, time.Unix(300, 0))
	if err != nil || result.Outcome != CatalogUnchanged || result.PendingRemovals != 0 {
		t.Fatalf("result = %+v, %v; want recovered unchanged catalog", result, err)
	}
	if snapshot := s.snapshot(); snapshot.PendingRemovals != 0 || snapshot.LastOutcome != CatalogUnchanged {
		t.Fatalf("snapshot = %+v; want cleared pending shrink", snapshot)
	}
}

func TestCatalogStoreInvalidCandidatesRetainPendingShrinkAndCatalog(t *testing.T) {
	for _, candidate := range [][]canonical.ModelInfo{
		nil,
		{{ID: "auto"}},
		{{ID: "   "}},
	} {
		t.Run("invalid", func(t *testing.T) {
			s := initializedCatalogStore()
			if _, err := s.reconcile([]canonical.ModelInfo{{ID: "claude-sonnet-5", Name: "Sonnet 5"}}, time.Unix(200, 0)); err != nil {
				t.Fatal(err)
			}
			result, err := s.reconcile(candidate, time.Unix(300, 0))
			if err == nil || result.Outcome != CatalogFailed || result.PublishedCount != 2 || result.PendingRemovals != 1 {
				t.Fatalf("result = %+v, %v; want failed result retaining pending shrink and published catalog", result, err)
			}
			if snapshot := s.snapshot(); len(snapshot.Models) != 2 || snapshot.PendingRemovals != 1 || snapshot.LastOutcome != CatalogFailed {
				t.Fatalf("snapshot = %+v; want retained catalog and pending shrink", snapshot)
			}
		})
	}
}

func TestCatalogStorePublishesMixedAddRemoveAndStagesExactCandidate(t *testing.T) {
	s := initializedCatalogStore()
	candidate := []canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
		{ID: "qwen3-coder-next", Name: "Qwen 3 Coder"},
	}
	first, err := s.reconcile(candidate, time.Unix(200, 0))
	if err != nil || first.Outcome != CatalogPendingShrink || first.PublishedCount != 3 || first.PendingRemovals != 1 {
		t.Fatalf("first = %+v, %v; want additions published and removal pending", first, err)
	}
	if got := s.snapshot(); !reflect.DeepEqual(got.Models, []canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
		{ID: "qwen3-coder-next", Name: "Qwen 3 Coder"},
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
	}) {
		t.Fatalf("snapshot = %+v; want additions plus retained missing row", got)
	}
	candidate[0].Name = "caller mutation"
	second, err := s.reconcile([]canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
		{ID: "qwen3-coder-next", Name: "Qwen 3 Coder"},
	}, time.Unix(300, 0))
	if err != nil || second.Outcome != CatalogShrinkConfirmed || !reflect.DeepEqual(s.snapshot().Models, []canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
		{ID: "qwen3-coder-next", Name: "Qwen 3 Coder"},
	}) {
		t.Fatalf("second = %+v, %v snapshot=%+v; want exact staged candidate", second, err, s.snapshot())
	}
}

func TestCatalogStoreSnapshotReturnsIndependentSlice(t *testing.T) {
	s := initializedCatalogStore()
	first := s.snapshot()
	first.Models[0].Name = "caller mutation"
	second := s.snapshot()
	if second.Models[0].Name != "Sonnet 5" {
		t.Fatalf("snapshot leaked caller mutation: %+v", second)
	}
}

func initializedCatalogStore() *catalogStore {
	s := newCatalogStore(15 * time.Minute)
	s.initialize([]canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Sonnet 5"},
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
	}, time.Unix(100, 0))
	return s
}
