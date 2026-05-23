// Package canonical defines the typed chunk and block types that flow through
// the Loop24 gateway. This package imports nothing under internal/.
package canonical

// ChunkKind is the discriminator for a Chunk value.
type ChunkKind int

const (
	// ChunkKindText is a plain text fragment from kiro-cli.
	ChunkKindText ChunkKind = iota
	// ChunkKindThought is an internal reasoning fragment (not shown to end-users by default).
	ChunkKindThought
	// ChunkKindToolCall is a tool invocation from kiro-cli.
	ChunkKindToolCall
	// ChunkKindPlan is a plan fragment from kiro-cli.
	ChunkKindPlan
)

// Chunk is a discriminated-union value produced by the ACP client and
// consumed by HTTP adapters. Exactly one of the pointer fields is non-nil,
// selected by Kind.
type Chunk struct {
	// Kind identifies which pointer field is populated.
	Kind ChunkKind
	// Text is set when Kind == ChunkKindText.
	Text *TextChunk
	// Thought is set when Kind == ChunkKindThought.
	Thought *ThoughtChunk
	// ToolCall is set when Kind == ChunkKindToolCall.
	ToolCall *ToolCallChunk
	// Plan is set when Kind == ChunkKindPlan.
	Plan *PlanChunk
}

// TextChunk carries a text fragment from kiro-cli.
type TextChunk struct {
	// Content is the text content of the chunk.
	Content string
}

// ThoughtChunk carries an internal reasoning fragment.
type ThoughtChunk struct {
	// Content is the reasoning content.
	Content string
}

// ToolCallChunk carries a tool invocation from kiro-cli.
type ToolCallChunk struct {
	// Name is the tool name.
	Name string
	// Args is the tool arguments as a map.
	Args map[string]any
}

// PlanChunk carries a plan fragment from kiro-cli.
type PlanChunk struct {
	// Content is the plan content.
	Content string
}

// BlockKind is the discriminator for a Block value.
type BlockKind int

const (
	// BlockKindText is a plain text input block.
	BlockKindText BlockKind = iota
	// BlockKindResourceLink is a resource link input block.
	BlockKindResourceLink
)

// Block is a discriminated-union value representing prompt input.
// It is the INPUT type passed to acp.Client.Prompt.
// Exactly one of the pointer fields is non-nil, selected by Kind.
type Block struct {
	// Kind identifies which pointer field is populated.
	Kind BlockKind
	// Text is set when Kind == BlockKindText.
	Text *TextBlock
	// ResourceLink is set when Kind == BlockKindResourceLink.
	ResourceLink *ResourceLinkBlock
}

// TextBlock carries plain text prompt content.
type TextBlock struct {
	// Content is the text content.
	Content string
}

// ResourceLinkBlock carries a URI reference for a prompt resource.
type ResourceLinkBlock struct {
	// URI is the resource URI.
	URI string
	// Title is the human-readable title for the resource.
	Title string
}
