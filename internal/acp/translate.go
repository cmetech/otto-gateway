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
