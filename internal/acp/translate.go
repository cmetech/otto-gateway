package acp

import (
	"net/url"
	"path"

	"loop24-gateway/internal/canonical"
)

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

// TODO(D-20, Plan 04): delete grantParams together with the session/grant_permission send path in client.go.
// grantParams is the params payload for session/grant_permission requests.
// NOTE: JSON field "optionId" (lowercase d) — must match the exact wire name.
type grantParams struct {
	RequestID string `json:"requestId"`
	OptionID  string `json:"optionId"` // "allow_always"
	Granted   bool   `json:"granted"`
}

// firstNonEmpty returns the first non-empty string from values, or "" if all
// values are empty. Used to absorb wire-shape variance where multiple field
// names may carry the same datum (e.g., sessionId vs id, sessionUpdate vs type).
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseStopReason maps the wire stop-reason string emitted in the
// session/prompt response (per ACP spec §4 / D-02) to the canonical
// StopReason enum. Unknown or empty strings intentionally map to
// canonical.StopUnknown (forward-compatible per D-02) rather than
// returning an error — the prompt response should not fail just because
// kiro-cli emits a new stop reason this build doesn't recognise yet.
func parseStopReason(s string) canonical.StopReason {
	switch s {
	case "end_turn":
		return canonical.StopEndTurn
	case "max_tokens":
		return canonical.StopMaxTokens
	case "max_turn_requests":
		return canonical.StopMaxTurnRequests
	case "refusal":
		return canonical.StopRefusal
	case "cancelled":
		return canonical.StopCancelled
	default:
		return canonical.StopUnknown
	}
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
//
// Phase 1.1 D-14: text wire field is "text" (was "content" in Phase 1);
// resource_link wire frame includes a required "name" field; image fields
// (MIMEType, Data) are present so the encoder is complete — Phase 2 wires
// the canonical.BlockKindImage producer (D-15).
type wireBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`      // present for type=="text"
	URI      string `json:"uri,omitempty"`       // present for type=="resource_link"
	Name     string `json:"name,omitempty"`      // present for type=="resource_link" (REQUIRED by ACP spec)
	Title    string `json:"title,omitempty"`     // present for type=="resource_link"
	MIMEType string `json:"mimeType,omitempty"`  // present for type=="image" (Phase 2 populates)
	Data     string `json:"data,omitempty"`      // present for type=="image" — base64 (Phase 2 populates)
}

// translateBlock converts a canonical.Block to the ACP wire shape.
//
// Mapping (Phase 1.1 D-14):
//
//	BlockKindText         → {"type":"text","text":"..."}
//	BlockKindResourceLink → {"type":"resource_link","uri":"...","name":"...","title":"..."}
//	<unknown>             → {"type":"text"}   (empty text — avoids data loss)
//
// A nil variant pointer for a known Kind produces a wireBlock with only the
// type discriminator set (Text/URI/Name/Title omitted via omitempty).
//
// D-04: when ResourceLink.Name is empty, derive the wire `name` field from
// path.Base(URI). For file:// URIs, net/url.Parse extracts the Path and we
// apply path.Base to that — yielding e.g. "bar.txt" from "file:///foo/bar.txt".
// If the URI fails to parse, Name stays empty (defensive — do not panic).
func translateBlock(b canonical.Block) wireBlock {
	switch b.Kind {
	case canonical.BlockKindText:
		if b.Text == nil {
			return wireBlock{Type: "text"}
		}
		return wireBlock{Type: "text", Text: b.Text.Content}
	case canonical.BlockKindResourceLink:
		if b.ResourceLink == nil {
			return wireBlock{Type: "resource_link"}
		}
		name := b.ResourceLink.Name
		if name == "" {
			// D-04: derive from URI via net/url.Parse + path.Base.
			if u, err := url.Parse(b.ResourceLink.URI); err == nil {
				switch {
				case u.Path != "":
					name = path.Base(u.Path)
				case u.Opaque != "":
					name = path.Base(u.Opaque)
				}
				// path.Base("") returns "." — guard so we don't emit "."
				// as a derived name when both Path and Opaque are empty.
				if name == "." {
					name = ""
				}
			}
		}
		return wireBlock{
			Type:  "resource_link",
			URI:   b.ResourceLink.URI,
			Name:  name,
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
