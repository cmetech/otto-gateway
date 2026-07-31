package privacy

import (
	"reflect"
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
