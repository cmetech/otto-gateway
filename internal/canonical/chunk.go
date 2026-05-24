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
	// BlockKindImage is an inline image input block (D-09 footnote —
	// Codex M-1). Appended at iota position 2; existing iota values
	// for BlockKindText (0) and BlockKindResourceLink (1) are preserved
	// so Phase 1.1 callers continue to read correctly.
	BlockKindImage
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
	// Image is set when Kind == BlockKindImage.
	Image *ImageBlock
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
	// Name is the human-readable file/resource name (wire field `name`,
	// required by ACP spec on resource_link). When empty, the wire encoder
	// falls back to `path.Base(URI)` per Phase 1.1 D-04.
	Name string
	// Title is the human-readable title for the resource.
	Title string
}

// ImageBlock carries an inline image block. Adapter base64-decodes wire
// data into Data before constructing this block; downstream emit
// translates back to the ACP spec image-block wire shape (source /
// mimeType / data fields per agentclientprotocol.com/protocol/content.md).
type ImageBlock struct {
	// Source is the original wire reference (data URL or http URL).
	// Informational only.
	Source string
	// MIMEType is the image MIME type (e.g., "image/png", "image/jpeg").
	MIMEType string
	// Data is the raw decoded image bytes. Adapter MUST base64-decode
	// the wire form before populating; ACP wire-shape encoder
	// re-base64-encodes for the agent.
	Data []byte
}
