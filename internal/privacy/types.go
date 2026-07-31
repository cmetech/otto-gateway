// Package privacy classifies and transforms sensitive strings at the
// gateway's canonical boundary.
package privacy

// MatchKind describes the confidence source for a finding. Higher values have
// higher priority when findings overlap.
type MatchKind uint8

const (
	MatchNER MatchKind = iota + 1
	MatchContextualTechnical
	MatchValidatedRegex
	MatchStructuredAssignment
	MatchHighConfidenceSecret
)

// Category groups findings by the policy family that handles them.
type Category string

const (
	CategorySecret    Category = "secret"
	CategoryTechnical Category = "technical"
	CategoryPersonal  Category = "personal"
)

// Finding identifies a sensitive byte span without retaining its source text.
type Finding struct {
	Entity        string
	Category      Category
	Kind          MatchKind
	Start, End    int
	RegistryOrder int
}

// Classifier detects sensitive spans in a key/value context.
type Classifier interface {
	Classify(key, value string) []Finding
}
