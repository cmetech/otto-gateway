package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"otto-gateway/internal/canonical"
)

// claudeModelHyphenVersionRe matches Anthropic-style Claude model IDs that use
// hyphen-separated version numbers (e.g., claude-sonnet-4-6, claude-haiku-4-5,
// claude-sonnet-4-5-20250514). Group 1 captures the family+major prefix,
// group 2 captures the minor digit; an optional trailing -YYYYMMDD date tag
// is matched and discarded.
var claudeModelHyphenVersionRe = regexp.MustCompile(`^(claude-[a-z]+-\d+)-(\d+)(?:-\d{8})?$`)

// normalizeClaudeModelID converts Anthropic-API-style Claude model IDs (which
// use hyphen-separated version components, e.g. "claude-sonnet-4-6") to the
// dot-separated form kiro-cli advertises and recognises on session/set_model
// (e.g. "claude-sonnet-4.6"). Anthropic SDK clients (loop24-client, otto-cli,
// @anthropic-ai/sdk) send the hyphenated form by convention; kiro-cli's
// session/set_model silently accepts unknown IDs and then fails the subsequent
// session/prompt with JSON-RPC -32603. Translating at the wire boundary keeps
// canonical.ChatRequest.Model in the kiro-cli-recognisable form so the engine's
// SetModel call succeeds end-to-end.
//
// Transformation rules:
//
//	claude-sonnet-4-6              → claude-sonnet-4.6
//	claude-haiku-4-5               → claude-haiku-4.5
//	claude-opus-4-7                → claude-opus-4.7
//	claude-sonnet-4-5-20250514     → claude-sonnet-4.5  (date tag dropped)
//	claude-sonnet-4                → claude-sonnet-4    (no minor; unchanged)
//	auto / "" / non-claude IDs     → unchanged
//
// The response echo path uses the ORIGINAL wire.Model value (not the
// translated canonical Model), so SDK clients see back the exact model
// string they sent.
func normalizeClaudeModelID(id string) string {
	if m := claudeModelHyphenVersionRe.FindStringSubmatch(id); m != nil {
		return m[1] + "." + m[2]
	}
	return id
}

// ----------------------------------------------------------------------------
// Wire request shape (POST /v1/messages)
// ----------------------------------------------------------------------------

// anthropicMessagesRequest mirrors the public Anthropic Messages API
// request body verified against @anthropic-ai/sdk@^0.90 type
// definitions (RESEARCH.md §Code Examples lines 768-810). System uses
// json.RawMessage because the wire shape accepts either a string OR a
// []anthropicSystemBlock — both forms are normalized to a flat string
// by wireToChatRequest per D-08. Metadata is accept-and-ignore.
type anthropicMessagesRequest struct {
	Model         string                 `json:"model"`
	MaxTokens     int                    `json:"max_tokens"`
	Messages      []anthropicWireMessage `json:"messages"`
	System        json.RawMessage        `json:"system,omitempty"`
	Tools         []anthropicToolSpec    `json:"tools,omitempty"`
	ToolChoice    json.RawMessage        `json:"tool_choice,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	TopK          *int                   `json:"top_k,omitempty"`
	Metadata      json.RawMessage        `json:"metadata,omitempty"` // accepted-and-ignored
}

// anthropicWireMessage is one entry of messages[]. Content is
// json.RawMessage because the wire shape accepts either a flat string
// OR an array of content blocks — wireToChatRequest decodes both forms.
type anthropicWireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// anthropicContentBlock is the superset of all 8 Anthropic content block
// types (text / image / tool_use / tool_result / thinking /
// redacted_thinking / document / resource_link). Populated fields depend
// on the Type discriminator. JSON-tag omitempty everywhere — only the
// fields relevant to a given Type will be present in the wire.
type anthropicContentBlock struct {
	Type string `json:"type"`

	// text block
	Text string `json:"text,omitempty"`

	// thinking / redacted_thinking blocks
	Thinking string `json:"thinking,omitempty"`
	Data     string `json:"data,omitempty"` // redacted_thinking opaque blob

	// tool_use block
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`

	// tool_result block
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`

	// image block
	Source *anthropicImageSource `json:"source,omitempty"`

	// resource_link block
	URI   string `json:"uri,omitempty"`
	Title string `json:"title,omitempty"`
}

// anthropicImageSource is the base64-source image variant per Anthropic
// spec. URL-source images are out of scope for Phase 3.1 — the canonical
// engine only consumes inline bytes.
type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// anthropicSystemBlock is one entry of system[] when system is an array.
// Only Type == "text" entries are honored — non-text blocks (image,
// tool_use, etc.) are dropped during normalization per D-08.
type anthropicSystemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// anthropicToolSpec is one entry of tools[]. Phase 3.1 decodes the
// shape so the request decode does not 400 when tools are present, but
// does NOT translate to canonical.ToolSpec — Phase 6 owns tool
// dispatch (CONTEXT.md <deferred>). Loop24-client invocations Phase 3.1
// lands rarely carry tools.
type anthropicToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// ----------------------------------------------------------------------------
// wireToChatRequest — translate POST /v1/messages body into canonical.ChatRequest
// ----------------------------------------------------------------------------

// wireToChatRequest extracts the canonical request from an Anthropic
// Messages wire payload. Implements D-08 (system normalization), D-09
// (tool_result.content normalization), D-11 (inbound thinking
// preservation), D-13 (redacted_thinking drop at wire decode), D-14
// (resource_link → ResourceLinks).
//
// Returns only *canonical.ChatRequest — translation cannot fail at this
// layer (validation lives in handleMessages, which checks model /
// max_tokens / messages non-empty before calling).
//
// The logger is required for D-09, D-10, D-13 debug-log paths. Callers
// MUST pass a non-nil logger; New defends against nil at the adapter
// boundary so this parameter is always populated when called from
// handleMessages.
func wireToChatRequest(w *anthropicMessagesRequest, r *http.Request, logger *slog.Logger) *canonical.ChatRequest {
	req := &canonical.ChatRequest{
		Model:              normalizeClaudeModelID(w.Model),
		Stream:             w.Stream,
		MaxTokens:          w.MaxTokens,
		Temperature:        w.Temperature,
		TopP:               w.TopP,
		StopSequences:      w.StopSequences,
		WorkingDirOverride: r.Header.Get("X-Working-Dir"),
	}

	// D-08: System normalization. Try string first; on error try array.
	if len(w.System) > 0 {
		var s string
		if err := json.Unmarshal(w.System, &s); err == nil {
			req.System = s
		} else {
			var blocks []anthropicSystemBlock
			if err := json.Unmarshal(w.System, &blocks); err == nil {
				var parts []string
				for _, b := range blocks {
					if b.Type == "text" && b.Text != "" {
						parts = append(parts, b.Text)
					}
				}
				req.System = strings.Join(parts, "\n\n")
			}
			// If neither shape parses, leave req.System empty —
			// permissive per D-10.
		}
	}

	// Walk messages[].
	for _, m := range w.Messages {
		role := mapAnthropicRole(m.Role)

		// Content is either a flat string or an array of content blocks.
		// Try string first (the cheap path); on failure decode as array.
		var contentStr string
		if err := json.Unmarshal(m.Content, &contentStr); err == nil {
			// Flat-string form — one text part.
			if contentStr == "" {
				continue
			}
			req.Messages = append(req.Messages, canonical.Message{
				Role: role,
				Content: []canonical.ContentPart{{
					Kind: canonical.ContentKindText,
					Text: contentStr,
				}},
			})
			continue
		}

		// Array-of-blocks form.
		var blocks []anthropicContentBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			// Malformed — drop the message but keep going (permissive
			// per D-10; the request did not arrive in a recognized form).
			logger.Debug("anthropic: malformed content; skipping message",
				"role", m.Role, "err", err)
			continue
		}

		parts := decodeContentBlocks(blocks, req, logger)
		if len(parts) == 0 {
			// Empty after decode (e.g., resource_link-only or
			// redacted_thinking-only — both flow to req.ResourceLinks
			// or are dropped). Skip the message itself but keep
			// req.ResourceLinks contributions.
			continue
		}
		req.Messages = append(req.Messages, canonical.Message{
			Role:    role,
			Content: parts,
		})
	}

	// Phase 3.1 does NOT translate tools to canonical.ToolSpec — tool
	// dispatch is Phase 6. Decoder accepts the shape so requests with
	// tools[] do not 400.
	// TODO(Phase 6): translate anthropicToolSpec → canonical.ToolSpec.

	return req
}

// decodeContentBlocks walks one message's content-block array and
// returns the ContentPart slice. Side effect: appends to
// req.ResourceLinks for resource_link blocks (D-14). The logger is used
// for D-09/D-10/D-13 debug-log paths.
func decodeContentBlocks(blocks []anthropicContentBlock, req *canonical.ChatRequest, logger *slog.Logger) []canonical.ContentPart {
	var parts []canonical.ContentPart
	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, canonical.ContentPart{
				Kind: canonical.ContentKindText,
				Text: block.Text,
			})

		case "image":
			if block.Source == nil {
				logger.Debug("anthropic: image block missing source; dropping")
				continue
			}
			// Codex M-1: malformed base64 is dropped (defensive — a
			// single corrupt image must not abort wireToChatRequest).
			if _, err := base64.StdEncoding.DecodeString(block.Source.Data); err != nil {
				logger.Debug("anthropic: malformed base64 image; dropping",
					"err", err, "media_type", block.Source.MediaType)
				continue
			}
			parts = append(parts, canonical.ContentPart{
				Kind: canonical.ContentKindImage,
				Image: &canonical.ImagePart{
					MIME:       block.Source.MediaType,
					DataBase64: block.Source.Data,
				},
			})

		case "tool_use":
			parts = append(parts, canonical.ContentPart{
				Kind: canonical.ContentKindToolUse,
				ToolUse: &canonical.ToolUsePart{
					ID:    block.ID,
					Name:  block.Name,
					Input: block.Input,
				},
			})

		case "tool_result":
			// D-09: content is either a string (verbatim) or an array
			// of blocks (text joined with \n; images dropped).
			normalized := normalizeToolResultContent(block.Content, logger)
			parts = append(parts, canonical.ContentPart{
				Kind: canonical.ContentKindToolResult,
				ToolResult: &canonical.ToolResultPart{
					ToolUseID: block.ToolUseID,
					Content:   normalized,
				},
			})

		case "thinking":
			// D-11: preserve inbound thinking as ContentKindThinking.
			parts = append(parts, canonical.ContentPart{
				Kind: canonical.ContentKindThinking,
				Text: block.Thinking,
			})

		case "redacted_thinking":
			// D-13 + W5: DROP at wire decode. kiro-cli has no equivalent
			// concept and Plan 04 Task 2 verifies kiro-cli does not
			// error on a follow-up turn whose history contained one.
			// Debug-log the block type name for observability.
			logger.Debug("anthropic: dropping redacted_thinking block at wire decode (D-13)",
				"block_type", "redacted_thinking")

		case "resource_link":
			// D-14: populate canonical.ResourceLinks (engine.pickCwd
			// consumes via longest-common-parent). Do NOT append a
			// ContentPart — resource_link is a separate field.
			req.ResourceLinks = append(req.ResourceLinks, canonical.ResourceLinkBlock{
				URI:   block.URI,
				Name:  block.Name,
				Title: block.Title,
			})

		case "document", "search_result":
			// Phase 3.1 scope: accept-and-drop with debug log per
			// <scope_note> in the plan. Not used by loop24-client and
			// not required by ANTH-01..07.
			logger.Debug("anthropic: dropping content block type not used in Phase 3.1",
				"block_type", block.Type)

		default:
			// Unknown / future block types — debug-log and drop
			// (permissive per D-10).
			logger.Debug("anthropic: unknown content block type; dropping",
				"block_type", block.Type)
		}
	}
	return parts
}

// normalizeToolResultContent implements D-09: tool_result.content is
// either a string (verbatim) or an array of content blocks (text
// joined with \n; images dropped with debug log).
func normalizeToolResultContent(raw json.RawMessage, logger *slog.Logger) string {
	if len(raw) == 0 {
		return ""
	}
	// String form (most common).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Array form.
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		logger.Debug("anthropic: tool_result.content malformed; returning empty",
			"err", err)
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			logger.Debug("anthropic: dropping image inside tool_result.content (D-09)",
				"media_type", func() string {
					if b.Source != nil {
						return b.Source.MediaType
					}
					return ""
				}())
		default:
			logger.Debug("anthropic: dropping unknown block inside tool_result.content",
				"block_type", b.Type)
		}
	}
	return strings.Join(parts, "\n")
}

// mapAnthropicRole translates the Anthropic wire role string into the
// canonical MessageRole enum. Anthropic's messages array only carries
// "user" and "assistant" roles — system prompts live in the top-level
// `system` field, and tool_result lives as a content block inside a
// "user" message. Unknown roles default to RoleUser (canonical's zero
// value).
func mapAnthropicRole(s string) canonical.MessageRole {
	switch s {
	case "assistant":
		return canonical.RoleAssistant
	default:
		return canonical.RoleUser
	}
}
