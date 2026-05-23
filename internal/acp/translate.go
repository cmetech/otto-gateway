package acp

import "loop24-gateway/internal/canonical"

// sessionUpdateParams holds the deserialized fields from a session/update or
// _kiro.dev/session/update notification payload.
// The session/update type field drives the canonical.Chunk variant produced by
// translateUpdate.
type sessionUpdateParams struct {
	SessionID string         `json:"sessionId"`
	Type      string         `json:"type"`
	Content   string         `json:"content"`
	ToolName  string         `json:"toolName"`            // populated for type "tool_call"
	Args      map[string]any `json:"args"`                // populated for type "tool_call"
}

// permissionParams holds the deserialized fields from a session/request_permission
// notification. The RequestID is echoed back in the auto-grant response.
type permissionParams struct {
	RequestID  string `json:"requestId"`
}

// grantParams is the params payload for session/grant_permission requests.
// NOTE: JSON field "optionId" (lowercase d) — must match the exact wire name.
type grantParams struct {
	RequestID string `json:"requestId"`
	OptionID  string `json:"optionId"` // "allow_always"
	Granted   bool   `json:"granted"`
}

// translateUpdate converts a raw session/update or _kiro.dev/session/update
// notification into a typed canonical.Chunk.
//
// Mapping (from ACP protocol reference and RESEARCH.md Canonical Chunk Translation table):
//
//	"text"      → canonical.ChunkKindText    (Content field)
//	"thought"   → canonical.ChunkKindThought  (Content field)
//	"tool_call" → canonical.ChunkKindToolCall (ToolName + Args fields)
//	"plan"      → canonical.ChunkKindPlan     (Content field)
//	<unknown>   → canonical.ChunkKindText     (fallback; avoids data loss)
func translateUpdate(u sessionUpdateParams) canonical.Chunk {
	switch u.Type {
	case "text":
		return canonical.Chunk{
			Kind: canonical.ChunkKindText,
			Text: &canonical.TextChunk{Content: u.Content},
		}
	case "thought":
		return canonical.Chunk{
			Kind:    canonical.ChunkKindThought,
			Thought: &canonical.ThoughtChunk{Content: u.Content},
		}
	case "tool_call":
		return canonical.Chunk{
			Kind:     canonical.ChunkKindToolCall,
			ToolCall: &canonical.ToolCallChunk{Name: u.ToolName, Args: u.Args},
		}
	case "plan":
		return canonical.Chunk{
			Kind: canonical.ChunkKindPlan,
			Plan: &canonical.PlanChunk{Content: u.Content},
		}
	default:
		// Unknown type → fallback to text to avoid data loss.
		return canonical.Chunk{
			Kind: canonical.ChunkKindText,
			Text: &canonical.TextChunk{Content: u.Content},
		}
	}
}

// wireBlock is the ACP wire shape for a prompt input block.
//
// canonical.Block uses a Go discriminated-union (Kind + pointer variants),
// which encodes via Go's default reflect encoder as
// {"Kind":0,"Text":{"Content":"..."},"ResourceLink":null} — NOT the wire shape
// kiro-cli expects. The wire format is a flat object with a "type" string
// discriminator and per-variant fields.
//
// CR-05 fix: translateBlock converts canonical.Block → wireBlock so the
// canonical package stays ACP-wire-format-agnostic (D-04 adapter
// responsibility). If kiro-cli changes its wire format, only translate.go
// changes.
type wireBlock struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"` // present for type=="text"
	URI     string `json:"uri,omitempty"`     // present for type=="resource_link"
	Title   string `json:"title,omitempty"`   // present for type=="resource_link"
}

// translateBlock converts a canonical.Block to the ACP wire shape.
//
// Mapping:
//
//	BlockKindText         → {"type":"text","content":"..."}
//	BlockKindResourceLink → {"type":"resource_link","uri":"...","title":"..."}
//	<unknown>             → {"type":"text"}   (empty text — avoids data loss)
//
// A nil variant pointer for a known Kind produces a wireBlock with only the
// type discriminator set (Content/URI/Title omitted via omitempty).
func translateBlock(b canonical.Block) wireBlock {
	switch b.Kind {
	case canonical.BlockKindText:
		if b.Text == nil {
			return wireBlock{Type: "text"}
		}
		return wireBlock{Type: "text", Content: b.Text.Content}
	case canonical.BlockKindResourceLink:
		if b.ResourceLink == nil {
			return wireBlock{Type: "resource_link"}
		}
		return wireBlock{
			Type:  "resource_link",
			URI:   b.ResourceLink.URI,
			Title: b.ResourceLink.Title,
		}
	default:
		// Unknown kind — fall back to empty text block to avoid data loss.
		return wireBlock{Type: "text"}
	}
}

// translateBlocks converts a slice of canonical.Block values to wire-shape
// structs. A nil or empty input returns a nil slice so the marshaled JSON
// is an explicit empty array via promptParams.Blocks (json.Marshal renders
// a nil []wireBlock as `null`; an empty []wireBlock{} as `[]`).
//
// Callers should pass at least one block — Phase 2 adapters always do.
func translateBlocks(blocks []canonical.Block) []wireBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]wireBlock, len(blocks))
	for i, b := range blocks {
		out[i] = translateBlock(b)
	}
	return out
}
