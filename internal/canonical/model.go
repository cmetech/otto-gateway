// Package canonical defines the typed chunk and block types that flow through
// the Gateway. This package imports nothing under internal/.
package canonical

// ModelInfo identifies a model exposed by kiro-cli's session/new response.
// It carries an entry from result.models.availableModels[].
type ModelInfo struct {
	// ID is the model identifier (wire field `modelId`).
	ID string
	// Name is the human-readable display name.
	Name string
}
