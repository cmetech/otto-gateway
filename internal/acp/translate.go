package acp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"

	"otto-gateway/internal/canonical"
)

// truncateForLog clips a string to maxLen bytes for safe inclusion in a log
// line. ACP payloads can be megabytes; logging them in full pollutes operator
// dashboards and risks dumping secret-bearing prompt content. Used by
// translateUpdate when reporting malformed inner-update payloads.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}

// sessionUpdateParams is the tolerant outer envelope for a session/update,
// session/notification, or _kiro.dev/session/update notification (D-16, D-17).
//
// Two shapes arrive on the wire:
//
//  1. Wrapped form:
//     `{"sessionId":"...","update":{"sessionUpdate":"agent_message_chunk","content":{"text":"..."}}}`
//     In this case Update is the raw JSON of the inner body; the flat fields
//     stay zero-valued.
//
//  2. Flat form:
//     `{"sessionId":"...","sessionUpdate":"agent_message_chunk","content":{"text":"..."}}`
//     In this case Update is empty and the flat fields hold the payload.
//
// translateUpdate re-unmarshals Update into a sessionUpdateBody when non-empty,
// otherwise copies the flat fields into a sessionUpdateBody. Either path yields
// one populated body downstream.
type sessionUpdateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update,omitempty"`

	// Flat fields — used when params is not wrapped under `update`.
	SessionUpdate string          `json:"sessionUpdate,omitempty"`
	Type          string          `json:"type,omitempty"`
	Content       json.RawMessage `json:"content,omitempty"`
	Text          string          `json:"text,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Title         string          `json:"title,omitempty"`
	Args          map[string]any  `json:"args,omitempty"`
	Output        string          `json:"output,omitempty"`
	Entries       []planEntry     `json:"entries,omitempty"`
}

// sessionUpdateBody mirrors the inner body of a wrapped session/update
// notification — i.e., what lives under `params.update` in the wrapped form.
// Identical to sessionUpdateParams minus SessionID and Update.
type sessionUpdateBody struct {
	SessionUpdate string          `json:"sessionUpdate,omitempty"`
	Type          string          `json:"type,omitempty"`
	Content       json.RawMessage `json:"content,omitempty"`
	Text          string          `json:"text,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Title         string          `json:"title,omitempty"`
	Args          map[string]any  `json:"args,omitempty"`
	Output        string          `json:"output,omitempty"`
	Entries       []planEntry     `json:"entries,omitempty"`
}

// planEntry is a single entry inside a `plan` session/update's entries[] array.
type planEntry struct {
	Content string `json:"content,omitempty"`
}

// permissionParams holds the deserialized fields from a session/request_permission
// notification. Phase 1.1 D-20: the gateway responds on the original frame id —
// the request body itself is no longer load-bearing for the response, but the
// type is preserved so handleNotification can debug-log the inbound RequestID
// when DEBUG=1 is set.
type permissionParams struct {
	RequestID string `json:"requestId"`
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

// normalizeUpdateType normalizes a session/update discriminator string to
// snake_case canonical form per D-19. kiro-cli versions vary: some emit
// `agent_message_chunk` (snake), others `AgentMessageChunk` (Camel), and the
// docs sometimes mix `Agent_Message_Chunk` (mixed). One canonical form keeps
// translateUpdate's switch table flat and readable.
//
// Behaviour:
//   - Empty input → empty output (caller treats as "unknown variant").
//   - If s already contains at least one underscore: lowercase it as-is.
//     This covers `agent_message_chunk` and `Agent_Message_Chunk` uniformly.
//   - Otherwise: CamelCase → snake_case. Walk runes; insert `_` before every
//     non-leading uppercase ASCII letter and lowercase the rune; non-letter
//     runes pass through lowercased.
func normalizeUpdateType(s string) string {
	if s == "" {
		return ""
	}
	if strings.ContainsRune(s, '_') {
		return strings.ToLower(s)
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// translateUpdate converts a deserialized session/update notification payload
// (in either the wrapped or flat form) into a typed canonical.Chunk per D-16,
// D-17, D-18, D-19.
//
// Step 1: pick the inner body. If u.Update is non-empty, re-unmarshal it into
// a sessionUpdateBody. Otherwise copy the flat u.* fields into a body.
//
// Step 2: pick the discriminator via firstNonEmpty(body.SessionUpdate,
// body.Type), then normalise it to snake_case via normalizeUpdateType.
//
// Step 3: extract textual content via the D-18 fallback chain:
// `body.content?.text ?? body.content ?? body.text`.
//
// Step 4: switch on the discriminator and build the canonical.Chunk:
//
//	agent_message_chunk       → ChunkKindText    (content)
//	agent_thought_chunk       → ChunkKindThought (content)
//	tool_call, tool_call_chunk → ChunkKindThought ("[tool: <title>]\n")
//	tool_call_update          → ChunkKindThought (output ?? content)
//	plan                       → ChunkKindPlan    (entries joined by \n)
//	default (incl. empty)     → ChunkKindText    (content) — preserve Phase 1
//	                            "fall back to text to avoid data loss" policy
//	                            per CONTEXT.md §Claude's Discretion.
//
// `tool_call`/`tool_call_chunk`/`tool_call_update` are rendered as thought
// text in Phase 1.1 per CONTEXT.md `<deferred>` — Phase 6 (TOOL-01, TOOL-03)
// emits canonical.ToolCallChunk with proper id/title/args.
//
// WR-05 (Phase 1.1 review): the return signature is (canonical.Chunk, bool).
// The bool is true when a chunk should be emitted, false when the caller
// MUST drop the notification (e.g., malformed inner-update payload). The
// previous form ignored inner-unmarshal errors and emitted a phantom empty
// ChunkKindText — invisible noise the consumer could not distinguish from
// a real empty chunk. Returning ok=false on the parse failure (plus a Debug
// log via the supplied logger) makes the failure observable and prevents
// the empty-chunk pollution.
func translateUpdate(logger *slog.Logger, u sessionUpdateParams) (canonical.Chunk, bool) {
	var body sessionUpdateBody
	if len(u.Update) > 0 {
		// WR-05: report inner-unmarshal failures rather than silently
		// dropping them into a zero-valued body. A nil logger is allowed
		// (some tests supply one, some don't); guard with a nil check.
		if err := json.Unmarshal(u.Update, &body); err != nil {
			if logger != nil {
				logger.Debug("acp: session/update inner-unmarshal failed — dropped",
					"err", err,
					"raw", truncateForLog(string(u.Update), 200))
			}
			return canonical.Chunk{}, false
		}
	} else {
		body = sessionUpdateBody{
			SessionUpdate: u.SessionUpdate,
			Type:          u.Type,
			Content:       u.Content,
			Text:          u.Text,
			ToolCallID:    u.ToolCallID,
			Title:         u.Title,
			Args:          u.Args,
			Output:        u.Output,
			Entries:       u.Entries,
		}
	}

	discriminator := normalizeUpdateType(firstNonEmpty(body.SessionUpdate, body.Type))
	content := extractContent(body)

	switch discriminator {
	case "agent_message_chunk":
		return canonical.Chunk{
			Kind: canonical.ChunkKindText,
			Text: &canonical.TextChunk{Content: content},
		}, true
	case "agent_thought_chunk":
		return canonical.Chunk{
			Kind:    canonical.ChunkKindThought,
			Thought: &canonical.ThoughtChunk{Content: content},
		}, true
	case "tool_call", "tool_call_chunk":
		// CONTEXT.md <deferred>: render as thought with [tool: <title>]\n
		// prefix; Phase 6 emits canonical.ToolCallChunk properly.
		title := firstNonEmpty(body.Title, "unknown")
		return canonical.Chunk{
			Kind:    canonical.ChunkKindThought,
			Thought: &canonical.ThoughtChunk{Content: fmt.Sprintf("[tool: %s]\n", title)},
		}, true
	case "tool_call_update":
		// Node behaviour: output ?? content.text — `content` already reflects
		// the D-18 extraction.
		return canonical.Chunk{
			Kind:    canonical.ChunkKindThought,
			Thought: &canonical.ThoughtChunk{Content: firstNonEmpty(body.Output, content)},
		}, true
	case "plan":
		return canonical.Chunk{
			Kind: canonical.ChunkKindPlan,
			Plan: &canonical.PlanChunk{Content: joinPlanEntries(body.Entries)},
		}, true
	default:
		// Preserve Phase 1 "fall back to text to avoid data loss" policy
		// (CONTEXT.md §Claude's Discretion). Unknown discriminators land
		// here; handleNotification logs a Debug. Empty discriminator with
		// non-empty `text` (e.g., a notification carrying only `body.text`)
		// also lands here and surfaces the text.
		return canonical.Chunk{
			Kind: canonical.ChunkKindText,
			Text: &canonical.TextChunk{Content: content},
		}, true
	}
}

// extractContent walks the D-18 fallback chain to pull a single string out of
// a session/update body's content field. The wire shape varies:
//
//	{"content":{"type":"text","text":"hello"}}  → "hello"
//	{"content":{"type":"text","text":""}}        → ""        (NOT body.Text)
//	{"content":"hello"}                          → "hello"
//	{"text":"hello"}                             → "hello"
//
// The probe ignores `content.type` (always "text" when populated) and reads
// `content.text` directly. The presence of `content.text` (even when empty)
// is the load-bearing signal — falling back to `body.text` from a populated
// content object is wrong (WR-04).
//
// WR-04 (Phase 1.1 review): the probe Text field is a *string pointer rather
// than a plain string so the JSON decoder distinguishes "absent" from
// "present with empty value". A struct field of type string lands at "" for
// BOTH cases, so an explicit empty content.text would have leaked through to
// body.text — surfacing an unrelated value when the wire said "empty".
func extractContent(body sessionUpdateBody) string {
	if len(body.Content) > 0 {
		var probe struct {
			Text *string `json:"text"`
		}
		if err := json.Unmarshal(body.Content, &probe); err == nil && probe.Text != nil {
			return *probe.Text
		}
		var s string
		if err := json.Unmarshal(body.Content, &s); err == nil {
			return s
		}
	}
	return body.Text
}

// joinPlanEntries concatenates plan entry contents with "\n" separators.
// A nil or empty slice returns "" so the canonical PlanChunk carries an empty
// string rather than "\n" or similar artefact.
func joinPlanEntries(entries []planEntry) string {
	if len(entries) == 0 {
		return ""
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, e.Content)
	}
	return strings.Join(parts, "\n")
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
	Text     string `json:"text,omitempty"`     // present for type=="text"
	URI      string `json:"uri,omitempty"`      // present for type=="resource_link"
	Name     string `json:"name,omitempty"`     // present for type=="resource_link" (REQUIRED by ACP spec)
	Title    string `json:"title,omitempty"`    // present for type=="resource_link"
	MIMEType string `json:"mimeType,omitempty"` // present for type=="image" (Phase 2 populates)
	Data     string `json:"data,omitempty"`     // present for type=="image" — base64 (Phase 2 populates)
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
