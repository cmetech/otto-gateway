package anthropic

import (
	"crypto/rand"
	"encoding/hex"

	"loop24-gateway/internal/canonical"
)

// ----------------------------------------------------------------------------
// Non-streaming response wire shape (POST /v1/messages, stream:false)
// ----------------------------------------------------------------------------

// anthropicMessage is the Anthropic Message response shape per
// docs.anthropic.com/en/api/messages + @anthropic-ai/sdk@^0.90
// type definitions (RESEARCH.md §Code Examples lines 768-810).
//
// StopReason and StopSequence are *string (not string with omitempty)
// because Anthropic's spec marks both as nullable — emitting them as
// JSON null when unknown is part of the contract; omitting them would
// break SDK consumers that key on the field's presence.
type anthropicMessage struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"` // always "message"
	Role         string         `json:"role"` // always "assistant"
	Model        string         `json:"model"`
	Content      []contentBlock `json:"content"`
	StopReason   *string        `json:"stop_reason"`   // nullable
	StopSequence *string        `json:"stop_sequence"` // nullable
	Usage        usage          `json:"usage"`
}

// contentBlock is the superset outbound content block — Phase 3.1
// emits text, thinking, and tool_use only (per ANTH-01 + ANTH-07
// outbound non-streaming + ANTH-03). Field omitempty everywhere — only
// the fields relevant to a given Type populate.
//
// Note on tool_use.input: Input is map[string]any so json.Marshal
// emits a JSON OBJECT, not a string. This is the load-bearing
// difference vs. OpenAI's `arguments` field (ANTH-03 / Pitfall 8 in
// RESEARCH.md).
type contentBlock struct {
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`
	Thinking string         `json:"thinking,omitempty"`
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Input    map[string]any `json:"input,omitempty"`
}

// usage is the per-turn token-accounting envelope. Phase 3.1 emits
// honest zeros for InputTokens/OutputTokens per D-12. The cache fields
// are omitempty (zero-valued by default — they only appear on the wire
// when a future phase populates them via the Anthropic
// prompt-caching extension).
type usage struct {
	InputTokens              int  `json:"input_tokens"`
	OutputTokens             int  `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens,omitempty"`
}

// chatResponseToMessage renders a canonical.ChatResponse into the
// Anthropic Message wire shape. requestedModel takes precedence over
// resp.Model so the client sees back the model name it sent (A3 echo
// opaque). Empty requestedModel falls through to resp.Model.
//
// Walking resp.Message.Content:
//   - ContentKindText → {Type:"text", Text:...}
//   - ContentKindThinking → {Type:"thinking", Thinking:...}
//   - ContentKindToolUse → {Type:"tool_use", ID, Name, Input (object)}
//   - Other ContentKinds skipped (defensive — Phase 3.1 emits only the
//     three Anthropic-compatible outbound block types).
//
// Defensive empty: if no parts produced output, emit
// [{Type:"text", Text:""}] so the response always has at least one
// content block (RESEARCH.md §Code Examples lines 836-839).
func chatResponseToMessage(resp *canonical.ChatResponse, requestedModel string) *anthropicMessage {
	out := &anthropicMessage{
		ID:    genMessageID(),
		Type:  "message",
		Role:  "assistant",
		Usage: usage{InputTokens: 0, OutputTokens: 0}, // D-12 honest zeros
	}

	out.Model = requestedModel
	if out.Model == "" && resp != nil {
		out.Model = resp.Model
	}

	if resp != nil {
		for _, part := range resp.Message.Content {
			switch part.Kind {
			case canonical.ContentKindText:
				out.Content = append(out.Content, contentBlock{
					Type: "text",
					Text: part.Text,
				})
			case canonical.ContentKindThinking:
				out.Content = append(out.Content, contentBlock{
					Type:     "thinking",
					Thinking: part.Text,
				})
			case canonical.ContentKindToolUse:
				if part.ToolUse != nil {
					out.Content = append(out.Content, contentBlock{
						Type:  "tool_use",
						ID:    part.ToolUse.ID,
						Name:  part.ToolUse.Name,
						Input: part.ToolUse.Input,
					})
				}
			}
			// Other kinds (Image, ToolResult) are inbound-only in
			// Phase 3.1 — kiro-cli does not emit them on the response
			// side. Defensive silent skip.
		}
		out.StopReason = mapStopReason(resp.StopReason)
	}

	if len(out.Content) == 0 {
		out.Content = []contentBlock{{Type: "text", Text: ""}}
	}

	// StopSequence stays nil — Phase 3.1 has no stop-sequence support
	// at the engine layer (forward-design seam in canonical.ChatRequest
	// is dormant). JSON renders as null per the *string + no omitempty
	// tag.

	return out
}

// mapStopReason translates the canonical StopReason enum to the
// Anthropic stop_reason wire value. Per Anthropic spec the field is
// nullable — StopUnknown returns nil so the JSON renders as `null`
// (the SDK keys on field-present, not field-truthy).
//
// Mapping per CONTEXT.md A5 / RESEARCH.md §Code Examples lines
// 844-861:
//   - StopEndTurn → "end_turn"
//   - StopMaxTokens → "max_tokens"
//   - StopMaxTurnRequests → "max_tokens" (closest semantic)
//   - StopRefusal → "refusal"
//   - StopCancelled → "end_turn" (planner pick per A5; closest semantic
//     — Anthropic spec has no "cancelled" value)
//   - StopUnknown → nil (null on wire)
func mapStopReason(s canonical.StopReason) *string {
	var r string
	switch s {
	case canonical.StopEndTurn:
		r = "end_turn"
	case canonical.StopMaxTokens:
		r = "max_tokens"
	case canonical.StopMaxTurnRequests:
		r = "max_tokens"
	case canonical.StopRefusal:
		r = "refusal"
	case canonical.StopCancelled:
		r = "end_turn"
	default: // StopUnknown
		return nil
	}
	return &r
}

// genMessageID generates a per-response opaque id. Format `msg_01<hex>`
// for parity with Anthropic's real ID shape (loop24-client treats the
// id as opaque so any prefix works, but `msg_01` is the closest visual
// match and helps when debugging logs side-by-side with real Anthropic
// traffic). 24 hex characters = 12 random bytes from crypto/rand.
//
// crypto/rand.Read is non-blocking on every supported OS (per Go
// documentation), so this is safe to call on every request. The
// fallback path (rand error) returns a fixed string so the response
// is still valid JSON — but the error itself indicates a serious
// system-level failure and would surface in the caller's logs via
// the engine's own error paths.
func genMessageID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "msg_01_rand_unavailable"
	}
	return "msg_01" + hex.EncodeToString(b[:])
}
