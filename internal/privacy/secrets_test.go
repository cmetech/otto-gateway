package privacy

import (
	"math/bits"
	"reflect"
	"sort"
	"testing"
)

func TestArbitratePrioritizesConfidenceAcrossOverlaps(t *testing.T) {
	t.Parallel()

	got := Arbitrate([]Finding{
		{Entity: "PERSON", Kind: MatchNER, Start: 11, End: 20, RegistryOrder: 0},
		{Entity: "IPV4", Kind: MatchContextualTechnical, Start: 10, End: 21, RegistryOrder: 1},
		{Entity: "EMAIL", Kind: MatchValidatedRegex, Start: 7, End: 20, RegistryOrder: 2},
		{Entity: "PASSWORD", Kind: MatchStructuredAssignment, Start: 5, End: 22, RegistryOrder: 3},
		{Entity: "AUTHORIZATION", Kind: MatchHighConfidenceSecret, Start: 0, End: 25, RegistryOrder: 9},
	})

	want := []Finding{{Entity: "AUTHORIZATION", Kind: MatchHighConfidenceSecret, Start: 0, End: 25, RegistryOrder: 9}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Arbitrate()=%+v, want %+v", got, want)
	}
}

func TestArbitratePrioritizesCredentialedURLOverContainedIP(t *testing.T) {
	t.Parallel()

	got := Arbitrate([]Finding{
		{Entity: "IPV4", Kind: MatchValidatedRegex, Start: 25, End: 36, RegistryOrder: 1},
		{Entity: "CREDENTIAL_URL", Kind: MatchHighConfidenceSecret, Start: 0, End: 45, RegistryOrder: 8},
	})

	if len(got) != 1 || got[0].Entity != "CREDENTIAL_URL" {
		t.Fatalf("Arbitrate()=%+v, want credential URL only", got)
	}
}

func TestArbitrateUsesLongestSpanThenRegistryOrder(t *testing.T) {
	t.Parallel()

	t.Run("longest span", func(t *testing.T) {
		got := Arbitrate([]Finding{
			{Entity: "SHORT", Kind: MatchValidatedRegex, Start: 4, End: 10, RegistryOrder: 0},
			{Entity: "LONG", Kind: MatchValidatedRegex, Start: 2, End: 12, RegistryOrder: 9},
		})
		if len(got) != 1 || got[0].Entity != "LONG" {
			t.Fatalf("Arbitrate()=%+v, want longest span", got)
		}
	})

	t.Run("registry order", func(t *testing.T) {
		got := Arbitrate([]Finding{
			{Entity: "LATER", Kind: MatchValidatedRegex, Start: 2, End: 12, RegistryOrder: 7},
			{Entity: "EARLIER", Kind: MatchValidatedRegex, Start: 3, End: 13, RegistryOrder: 2},
		})
		if len(got) != 1 || got[0].Entity != "EARLIER" {
			t.Fatalf("Arbitrate()=%+v, want earliest registry entry", got)
		}
	})
}

func TestArbitrateReturnsAcceptedSpansInSourceOrder(t *testing.T) {
	t.Parallel()

	input := []Finding{
		{Entity: "LAST", Kind: MatchHighConfidenceSecret, Start: 30, End: 40, RegistryOrder: 0},
		{Entity: "FIRST", Kind: MatchNER, Start: 0, End: 5, RegistryOrder: 1},
		{Entity: "MIDDLE", Kind: MatchStructuredAssignment, Start: 12, End: 20, RegistryOrder: 2},
	}
	original := append([]Finding(nil), input...)

	got := Arbitrate(input)
	want := []Finding{input[1], input[2], input[0]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Arbitrate()=%+v, want source order %+v", got, want)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("Arbitrate mutated input: got %+v, want %+v", input, original)
	}
}

func TestArbitrateMatchesReferenceOnOverlappingCorpus(t *testing.T) {
	t.Parallel()

	findings := []Finding{
		{Entity: "ner-a", Kind: MatchNER, Start: 0, End: 8, RegistryOrder: 0},
		{Entity: "regex-a", Kind: MatchValidatedRegex, Start: 2, End: 9, RegistryOrder: 1},
		{Entity: "secret-a", Kind: MatchHighConfidenceSecret, Start: 4, End: 14, RegistryOrder: 8},
		{Entity: "structured-a", Kind: MatchStructuredAssignment, Start: 13, End: 20, RegistryOrder: 2},
		{Entity: "technical-a", Kind: MatchContextualTechnical, Start: 21, End: 25, RegistryOrder: 4},
		{Entity: "technical-long", Kind: MatchContextualTechnical, Start: 20, End: 26, RegistryOrder: 7},
		{Entity: "regex-earlier", Kind: MatchValidatedRegex, Start: 30, End: 36, RegistryOrder: 3},
		{Entity: "regex-later", Kind: MatchValidatedRegex, Start: 31, End: 37, RegistryOrder: 9},
		{Entity: "secret-final", Kind: MatchHighConfidenceSecret, Start: 40, End: 50, RegistryOrder: 5},
	}

	want := referenceArbitrate(findings)
	got := Arbitrate(findings)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Arbitrate()=%+v, reference=%+v", got, want)
	}
}

func TestArbitrateDenseNonOverlapping(t *testing.T) {
	const findingCount = 60_000
	findings := make([]Finding, findingCount)
	for i := range findings {
		// Reverse source order makes source-ordered slice insertion quadratic;
		// a bounded interval index remains O(N log N).
		start := (findingCount - i) * 3
		findings[i] = Finding{
			Entity:        "DENSE",
			Kind:          MatchHighConfidenceSecret,
			Start:         start,
			End:           start + 1,
			RegistryOrder: i,
		}
	}

	got := Arbitrate(findings)
	if len(got) != findingCount {
		t.Fatalf("accepted=%d, want %d", len(got), findingCount)
	}
	if got[0].Start != 3 || got[len(got)-1].Start != findingCount*3 {
		t.Fatalf("source bounds=(%d,%d), want (3,%d)", got[0].Start, got[len(got)-1].Start, findingCount*3)
	}
}

func TestIntervalTreeHeightIsLogarithmic(t *testing.T) {
	t.Parallel()

	const intervalCount = 4_096
	maxHeight := 2 * bits.Len(uint(intervalCount))
	orders := map[string]func(int) int{
		"ascending":  func(i int) int { return i },
		"descending": func(i int) int { return intervalCount - i - 1 },
	}
	for name, indexAt := range orders {
		t.Run(name, func(t *testing.T) {
			var root *intervalNode
			for i := range intervalCount {
				start := indexAt(i) * 2
				root = insertInterval(root, Finding{Start: start, End: start + 1})
			}
			actualHeight := structuralIntervalHeight(root)
			if actualHeight > maxHeight {
				t.Fatalf("tree height=%d, want <=%d for %d adversarial inserts", actualHeight, maxHeight, intervalCount)
			}
			if root.height != actualHeight {
				t.Fatalf("stored root height=%d, structural height=%d", root.height, actualHeight)
			}
		})
	}
}

func structuralIntervalHeight(root *intervalNode) int {
	if root == nil {
		return 0
	}
	return max(structuralIntervalHeight(root.left), structuralIntervalHeight(root.right)) + 1
}

func BenchmarkArbitrateDenseNonOverlapping(b *testing.B) {
	const findingCount = 60_000
	findings := make([]Finding, findingCount)
	for i := range findings {
		start := (findingCount - i) * 3
		findings[i] = Finding{Kind: MatchHighConfidenceSecret, Start: start, End: start + 1, RegistryOrder: i}
	}
	b.ResetTimer()
	for range b.N {
		Arbitrate(findings)
	}
}

func referenceArbitrate(findings []Finding) []Finding {
	candidates := append([]Finding(nil), findings...)
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Kind != candidates[j].Kind {
			return candidates[i].Kind > candidates[j].Kind
		}
		leftLen := candidates[i].End - candidates[i].Start
		rightLen := candidates[j].End - candidates[j].Start
		if leftLen != rightLen {
			return leftLen > rightLen
		}
		return candidates[i].RegistryOrder < candidates[j].RegistryOrder
	})

	accepted := make([]Finding, 0, len(candidates))
	for _, candidate := range candidates {
		overlaps := false
		for _, existing := range accepted {
			if candidate.Start < existing.End && existing.Start < candidate.End {
				overlaps = true
				break
			}
		}
		if !overlaps {
			accepted = append(accepted, candidate)
		}
	}
	sort.SliceStable(accepted, func(i, j int) bool {
		return accepted[i].Start < accepted[j].Start
	})
	return accepted
}
