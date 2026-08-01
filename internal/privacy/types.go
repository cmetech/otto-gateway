// Package privacy classifies and transforms sensitive strings at the
// gateway's canonical boundary.
package privacy

import "time"

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

// Observers are bounded event callbacks owned by the privacy service. Triage
// is wired here for the protected Task 13 surface; this package does not
// implement triage authorization or routing policy.
type Observers struct {
	Request           func(profile Profile, surface, workload, result string)
	Transformation    func(profile Profile, entity string, action Action)
	Restoration       func(profile Profile, entity, result string)
	Block             func(profile Profile, stage, reason string)
	Residual          func(profile Profile, stage, entity string)
	Receipt           func(profile Profile, result string)
	Duration          func(profile Profile, stage string, elapsed time.Duration)
	ScopeEvent        func(event string)
	CapacityRejection func(resource string)
	MappingOperation  func(operation, result string)
	InternalError     func(stage, reason string)
	Triage            func(operation, result string)
}
