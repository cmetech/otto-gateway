// span describes a single recognizer match against the ORIGINAL pre-
// redaction text. Start/End are byte offsets into that text (half-open
// [Start,End)). Name is the recognizer Name (e.g., "Email", "PERSON",
// "IMEI") used by ApplyMode to pick replacement shape.
//
// Spans are produced by two collectors:
//   - Regex collector: iterates Recognizers, runs Pattern.FindAllStringIndex,
//     applies Validate + hasContextWithin filters.
//   - NER collector (optional): runs the prose engine when h.NER != nil
//     and emits PERSON/LOCATION spans.
//
// mergeSpansGreedy arbitrates between the two: regex spans are passed
// in as `first` and always win; NER spans in `second` are dropped if
// they intersect any accepted span. Mirrors loop_24's _merge_results
// greedy non-overlap policy.

package pii

import "sort"

type span struct {
	Name  string
	Value string // verbatim slice from the original text — what ApplyMode receives
	Start int
	End   int
}

func (s span) overlaps(other span) bool {
	return s.Start < other.End && other.Start < s.End
}

// mergeSpansGreedy returns the union of first and second with second's
// entries dropped wherever they intersect an accepted entry. Result is
// sorted by Start ascending. Within first, callers must already have
// pre-arbitrated overlaps (regex recognizers are run in registration
// order; an earlier recognizer's span wins).
func mergeSpansGreedy(first, second []span) []span {
	accepted := make([]span, 0, len(first)+len(second))
	accepted = append(accepted, first...)

next:
	for _, cand := range second {
		for _, a := range accepted {
			if cand.overlaps(a) {
				continue next
			}
		}
		accepted = append(accepted, cand)
	}
	sort.SliceStable(accepted, func(i, j int) bool {
		return accepted[i].Start < accepted[j].Start
	})
	return accepted
}
