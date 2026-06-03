// Tests for span overlap arbitration. Regex spans are collected first;
// NER spans are added second. mergeSpansGreedy preserves the existing
// (regex-first) entries and drops any later entry whose [start,end)
// range intersects an already-accepted entry.

package pii

import (
	"reflect"
	"testing"
)

func TestMergeSpansGreedy_NoOverlap(t *testing.T) {
	got := mergeSpansGreedy(
		[]span{{Name: "Email", Start: 0, End: 5}},
		[]span{{Name: "PERSON", Start: 10, End: 15}},
	)
	want := []span{
		{Name: "Email", Start: 0, End: 5},
		{Name: "PERSON", Start: 10, End: 15},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestMergeSpansGreedy_LaterOverlapDropped(t *testing.T) {
	got := mergeSpansGreedy(
		[]span{{Name: "Email", Start: 0, End: 16}},
		[]span{{Name: "PERSON", Start: 5, End: 10}},
	)
	want := []span{
		{Name: "Email", Start: 0, End: 16},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("later overlapping span should be dropped; got %+v want %+v", got, want)
	}
}

func TestMergeSpansGreedy_SortedByStart(t *testing.T) {
	got := mergeSpansGreedy(
		[]span{{Name: "Email", Start: 20, End: 30}},
		[]span{{Name: "PERSON", Start: 0, End: 5}},
	)
	if got[0].Start != 0 || got[1].Start != 20 {
		t.Errorf("expected sorted-by-start ascending, got %+v", got)
	}
}

func TestMergeSpansGreedy_AbuttingNotOverlap(t *testing.T) {
	// [0,5) and [5,10) share no character; they must both survive.
	got := mergeSpansGreedy(
		[]span{{Name: "Email", Start: 0, End: 5}},
		[]span{{Name: "PERSON", Start: 5, End: 10}},
	)
	if len(got) != 2 {
		t.Errorf("abutting spans must both be kept; got %+v", got)
	}
}

func TestMergeSpansGreedy_EmptyInputs(t *testing.T) {
	got := mergeSpansGreedy(nil, nil)
	if len(got) != 0 {
		t.Errorf("two nil inputs must yield empty slice; got %+v", got)
	}
}

func TestMergeSpansGreedy_SecondOnly(t *testing.T) {
	got := mergeSpansGreedy(nil, []span{{Name: "PERSON", Start: 0, End: 5}})
	if len(got) != 1 || got[0].Name != "PERSON" {
		t.Errorf("expected single PERSON span, got %+v", got)
	}
}
