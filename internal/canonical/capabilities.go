// Package canonical defines the typed chunk and block types that flow through
// the Gateway. This package imports nothing under internal/.
package canonical

// PromptCapabilities mirrors the agent's promptCapabilities flags returned
// in the initialize response (agentCapabilities.promptCapabilities). Phase
// 1.1 captures these so adapters (Anthropic in Phase 3.1) can gate
// image-block construction.
type PromptCapabilities struct {
	// Image is true when the agent accepts image content blocks.
	Image bool
	// Audio is true when the agent accepts audio content blocks.
	Audio bool
	// EmbeddedContext is true when the agent accepts embedded-resource blocks.
	EmbeddedContext bool
}
