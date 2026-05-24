// Package canonical defines the typed chunk and block types that flow through
// the Loop24 gateway. This package imports nothing under internal/.
package canonical

// Phase 2: only ContentKindText (single Content part) and ContentKindImage
// are populated by adapters; other ContentKind values and most ChatRequest
// fields are forward-design seams for Phase 3 / 3.1 / 4 / 6 (D-08, D-09).
//
// The tri-surface contract: ChatRequest / ChatResponse / Message /
// ContentPart are designed once here and consumed unchanged by every
// adapter (Ollama in Phase 2, Anthropic in Phase 3.1, OpenAI in Phase 4)
// plus the engine. Dormant fields are zero-valued seams; later phases
// activate them with zero canonical-type churn.

// MessageRole is the role of a chat message. Iota positions are locked
// (Test 2 in chat_test.go) — adapters depend on RoleUser==0 as the zero
// value for parsed-but-empty wire messages.
type MessageRole int

const (
	// RoleUser is a user-authored message. Zero value.
	RoleUser MessageRole = iota
	// RoleSystem is a system-prompt message.
	RoleSystem
	// RoleAssistant is an assistant (kiro-cli) response message.
	RoleAssistant
	// RoleTool is a tool-result message (dormant in Phase 2; populated in
	// Phase 3.1 / 6).
	RoleTool
)

// ContentKind is the discriminator for a ContentPart. Iota positions are
// locked (Test 3 in chat_test.go); future ContentPart additions append
// at the end to preserve adapter wire-mapping stability.
type ContentKind int

const (
	// ContentKindText is plain text content. Zero value. Phase 2
	// populates this for every Ollama text message.
	ContentKindText ContentKind = iota
	// ContentKindImage is an inline image content part. Phase 2
	// populates this from Ollama `messages[].images: [b64]`.
	ContentKindImage
	// ContentKindToolUse is an assistant tool-invocation part (dormant
	// in Phase 2; activated by Phase 3.1 Anthropic adapter).
	ContentKindToolUse
	// ContentKindToolResult is a tool-result part (dormant in Phase 2;
	// activated by Phase 3.1 Anthropic adapter).
	ContentKindToolResult
	// ContentKindThinking is an assistant reasoning part (dormant in
	// Phase 2; activated by Phase 3.1 Anthropic adapter or Phase 6).
	ContentKindThinking
)

// ChatRequest is the canonical tri-surface chat request. Every Phase
// 2 / 3 / 3.1 / 4 / 6 adapter translates its wire shape into this
// type before handing it to the engine (D-08). Phase 2 populates
// Model + System + Messages + Options-derived fields +
// WorkingDirOverride + (in unit tests) ResourceLinks; the rest are
// zero-valued forward-design seams.
type ChatRequest struct {
	// Model is the requested model id (e.g., "claude-sonnet-4-7").
	// Empty or "auto" means do-not-call SetModel on the ACP session.
	Model string
	// System is the system-prompt string. Phase 2 Ollama maps from the
	// first Messages entry with Role == RoleSystem.
	System string
	// Messages is the canonical chat transcript. Phase 2 populates
	// only RoleUser / RoleAssistant / RoleSystem messages with
	// ContentKindText (single Content part) and ContentKindImage.
	Messages []Message
	// Tools is the dormant Phase 6 tool catalog. Zero-valued in Phase 2.
	Tools []ToolSpec
	// ToolChoice is the dormant Phase 6 tool-choice directive. Zero-
	// valued in Phase 2.
	ToolChoice *ToolChoice
	// MaxTokens is the soft cap on assistant output tokens. Zero
	// means no override.
	MaxTokens int
	// Temperature is the optional sampling temperature. nil means
	// no override (kiro-cli default applies).
	Temperature *float64
	// TopP is the optional nucleus-sampling threshold. nil means
	// no override.
	TopP *float64
	// StopSequences is the optional list of stop strings. Dormant
	// in Phase 2.
	StopSequences []string
	// Stream selects streaming response delivery. Phase 2 honours
	// this flag at the Ollama adapter layer.
	Stream bool
	// Think is the dormant Phase 3.1 / 6 thinking-enable flag. Zero-
	// valued in Phase 2.
	Think bool
	// Format is the optional response-format directive (e.g., JSON
	// schema). Dormant in Phase 2.
	Format *Format
	// Metadata is the free-form per-request metadata map. Dormant in
	// Phase 2; bounding lives at the adapter HTTP layer (T-02-04).
	Metadata map[string]any
	// WorkingDirOverride is the per-request cwd override sourced from
	// the X-Working-Dir header. engine.pickCwd consults this as the
	// second-highest-priority cwd source (D-16 step 3).
	WorkingDirOverride string
	// ResourceLinks carries 0..N file:// URIs the adapter knows about
	// ahead of time. engine.pickCwd uses these to derive cwd via
	// longest-common-parent (D-16 step 2). Phase 2 Ollama keeps this
	// zero-valued; Phase 3.1 Anthropic populates it from resource_link
	// content blocks. (D-08 footnote — Codex H-2.)
	ResourceLinks []ResourceLinkBlock
}

// ChatResponse is the canonical tri-surface chat response (D-10).
// Symmetric to ChatRequest: Phase 2 populates ID + Model +
// Message.Content[text] + StopReason; Usage is best-effort populated
// from kiro-cli's session/prompt response.
type ChatResponse struct {
	// ID is the response identifier (adapter-generated; opaque to
	// the engine).
	ID string
	// Model is the model id that produced the response.
	Model string
	// Message is the assistant response message (Role == RoleAssistant).
	Message Message
	// StopReason classifies why the turn ended. Maps from the ACP
	// session/prompt response's stopReason field per Phase 1.1.
	StopReason StopReason
	// Usage carries per-turn token accounting.
	Usage Usage
}

// Message is a chat message. Content uses []ContentPart from day one
// (D-09) so Phase 3.1 / 6 do not need to widen the type later. Phase 2
// only populates Kind == ContentKindText (len(Content)==1) and
// ContentKindImage (from Ollama messages[].images).
type Message struct {
	// Role identifies the speaker.
	Role MessageRole
	// Content is the ordered list of content parts. Phase 2 produces
	// len==1 with ContentKindText, optionally followed by one or
	// more ContentKindImage parts.
	Content []ContentPart
	// ToolCalls is the assistant's outbound tool invocations. Dormant
	// in Phase 2 (RoleAssistant with tool calls is a Phase 6 path).
	ToolCalls []ToolCall
	// ToolCallID is the tool-call id this Message satisfies when
	// Role == RoleTool. Dormant in Phase 2.
	ToolCallID string
}

// ContentPart is a discriminated-union content fragment. Exactly one
// of the pointer fields is non-nil for non-text Kinds; for
// ContentKindText, Text holds the content directly (no pointer).
type ContentPart struct {
	// Kind identifies which field is populated.
	Kind ContentKind
	// Text is the content when Kind == ContentKindText.
	Text string
	// Image is set when Kind == ContentKindImage.
	Image *ImagePart
	// ToolUse is set when Kind == ContentKindToolUse (dormant in
	// Phase 2).
	ToolUse *ToolUsePart
	// ToolResult is set when Kind == ContentKindToolResult (dormant
	// in Phase 2).
	ToolResult *ToolResultPart
}

// ImagePart carries an inline image content part. The Ollama
// adapter populates DataBase64 directly from `messages[].images: [b64]`
// (no decode at the adapter layer — engine.buildBlocks decodes when
// emitting the canonical ImageBlock for the ACP wire).
type ImagePart struct {
	// MIME is the image MIME type (e.g., "image/png", "image/jpeg").
	// May be empty when the wire shape does not declare it; downstream
	// emit defaults to "image/png".
	MIME string
	// DataBase64 is the base64-encoded image payload as it arrived
	// on the wire.
	DataBase64 string
}

// ToolUsePart is a dormant Phase 6 / Phase 3.1 forward-design seam
// for assistant tool invocations carried as content parts (the
// Anthropic content-block shape).
type ToolUsePart struct {
	// ID is the tool-call identifier (correlates with ToolResultPart.ID).
	ID string
	// Name is the tool name.
	Name string
	// Input is the tool arguments as an object map per the Anthropic
	// spec (tool-use input is an object, not a string).
	Input map[string]any
}

// ToolResultPart is a dormant Phase 6 / Phase 3.1 forward-design seam
// for tool-result content parts.
type ToolResultPart struct {
	// ToolUseID correlates with the ToolUsePart.ID this result satisfies.
	ToolUseID string
	// Content is the tool-result content (free-form string for Phase
	// 3.1 baseline; structured blocks are a later extension).
	Content string
	// IsError flags whether the tool result represents an error.
	IsError bool
}

// ToolCall is a dormant Phase 6 forward-design seam for assistant
// tool invocations carried in Message.ToolCalls (the OpenAI shape).
// Separate from ToolUsePart because the Anthropic content-block shape
// and the OpenAI message-level shape have different transport idioms.
type ToolCall struct {
	// ID is the tool-call identifier.
	ID string
	// Name is the tool name.
	Name string
	// Arguments is the tool arguments as an object map.
	Arguments map[string]any
}

// ToolSpec is a dormant Phase 6 forward-design seam for tool catalog
// entries declared by the client in ChatRequest.Tools.
type ToolSpec struct {
	// Name is the tool name.
	Name string
	// Description is the human-readable tool description.
	Description string
	// Parameters is the tool argument schema (JSON-schema-shaped
	// object map). Phase 6 fills the validation behavior.
	Parameters map[string]any
}

// ToolChoice is a dormant Phase 6 forward-design seam for the
// per-request tool-choice directive (e.g., auto / required / a
// specific tool name).
type ToolChoice struct {
	// Type is the choice type ("auto" | "required" | "tool" | "none").
	Type string
	// Name is the specific tool name when Type == "tool".
	Name string
}

// Format is a dormant Phase 3 / 6 forward-design seam for response-
// format directives (e.g., JSON-mode, JSON-schema, plain).
type Format struct {
	// Type is the format type ("json" | "json_schema" | "text").
	Type string
	// Schema is the optional JSON-schema body when Type == "json_schema".
	Schema map[string]any
}

// Usage carries per-turn token accounting. Phase 2 populates
// InputTokens + OutputTokens best-effort from kiro-cli's session/prompt
// response; the cache fields are dormant Phase 3.1 (Anthropic) seams.
type Usage struct {
	// InputTokens is the prompt-side token count.
	InputTokens int
	// OutputTokens is the response-side token count.
	OutputTokens int
	// CacheCreationInputTokens is the Anthropic prompt-caching write
	// count. Dormant in Phase 2.
	CacheCreationInputTokens int
	// CacheReadInputTokens is the Anthropic prompt-caching read count.
	// Dormant in Phase 2.
	CacheReadInputTokens int
}

// FinalResult is the canonical per-stream metadata type. It mirrors
// internal/acp.FinalResult so the engine boundary never leaks acp
// internals — Plan 04's engine.Stream shim returns this canonical type
// from Result(). Populated when the kiro-cli session/prompt response
// arrives; SessionID identifies the spent ACP session, ChunkCount is
// the number of chunks delivered, StopReason maps from the ACP
// session/prompt response per Phase 1.1.
type FinalResult struct {
	// SessionID is the ACP session id that produced this stream.
	SessionID string
	// ChunkCount is the number of chunks delivered over the stream.
	ChunkCount int
	// StopReason classifies why the turn ended.
	StopReason StopReason
}
