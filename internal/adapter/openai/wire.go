package openai

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin/compress"
)

// ----------------------------------------------------------------------------
// Wire request shapes (POST /v1/chat/completions)
// ----------------------------------------------------------------------------

// chatCompletionRequest mirrors the OpenAI chat/create request body.
// Content on chatMessage is json.RawMessage because the wire shape accepts
// either a flat string OR an array of content parts (Pitfall 5).
//
// Accept-and-ignore extras: StreamOptions, MaxCompletionTokens, Logprobs,
// Tools, FunctionCall, and any unknown future SDK additions decode without
// error (NO DisallowUnknownFields per decode.go:21-26 invariant). Stream
// bool defaults to false (JSON zero value).
type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream,omitempty"`

	// Accepted-and-ignored extras (decoded so they don't cause 400 on
	// unknown-field strict decoders — but we do NOT use DisallowUnknownFields
	// so plain unknown fields are silently skipped too).
	StreamOptions       json.RawMessage `json:"stream_options,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Logprobs            json.RawMessage `json:"logprobs,omitempty"`
	// Tools is the OpenAI tool catalog (Phase 6 D-13 + iteration-3 MEDIUM #4).
	// Decoded as []json.RawMessage so type-invalid sibling entries can be
	// skipped per-entry without failing the whole request decode. Each entry
	// is unmarshaled into openAIToolSpec independently in wireToChatRequest.
	Tools []json.RawMessage `json:"tools,omitempty"`
	// ToolChoice is the polymorphic OpenAI tool_choice directive (Phase 6
	// D-13). On the wire it can be a string ("auto" | "required" | "none")
	// OR an object ({type:"function", function:{name:"..."}}). Decoded as
	// json.RawMessage and split inside wireToChatRequest.
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
	FunctionCall  json.RawMessage `json:"function_call,omitempty"`
	StopSequences json.RawMessage `json:"stop,omitempty"`
}

// openAIToolSpec is one entry in the OpenAI tools[] field. Structurally
// identical to internal/adapter/ollama/wire.go's ollamaToolSpec — OpenAI
// and Ollama agreed on this wire shape years ago. The duplication is
// intentional per Phase 4 D-08 (no-shared-driver between adapters); each
// adapter owns its wire types.
type openAIToolSpec struct {
	Type     string                  `json:"type,omitempty"`
	Function *openAIToolSpecFunction `json:"function,omitempty"`
}

// openAIToolSpecFunction holds the function declaration. Byte-identical
// shape to ollamaToolSpecFunction (OpenAI/Ollama agreed years ago).
type openAIToolSpecFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// openAIToolChoiceObject is the object form of tool_choice. The string
// forms ("auto" / "required" / "none") are decoded separately.
type openAIToolChoiceObject struct {
	Type     string                          `json:"type"`
	Function *openAIToolChoiceObjectFunction `json:"function,omitempty"`
}

type openAIToolChoiceObjectFunction struct {
	Name string `json:"name"`
}

// chatMessage is one entry of messages[]. Content is json.RawMessage
// because the OpenAI wire shape accepts either a flat string ("hello")
// OR an array of content-part objects ([{"type":"text","text":"hello"}]).
// wireToChatRequest decodes both forms (Pitfall 5 / anthropic/wire.go:43-44).
type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	// ToolCalls carries an assistant turn's prior tool invocations
	// (multi-turn tool calling). Preserved into canonical.Message.ToolCalls
	// so engine.buildBlocks can replay them as [Assistant tool call]
	// sections — without this the assistant turn is dropped and kiro
	// re-invokes the tool. content is null on such turns, so the message is
	// carried on the strength of ToolCalls alone.
	ToolCalls []openAIReqToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a role:"tool" result message back to the assistant
	// tool call it answers. Rendered into the [Tool result (id: …)] header.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// openAIReqToolCall is the inbound assistant tool-call shape
// ({id,type,function:{name,arguments}}). arguments is a JSON-ENCODED
// STRING per the OpenAI spec (not an object); wireToChatRequest parses it
// into the canonical map[string]any.
type openAIReqToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// openAIContentPart is one entry in a messages[].content array. Only the
// "text" type is supported in Phase 3 (image parts are out of scope per
// RESEARCH.md deferred list); other types are silently skipped.
type openAIContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ----------------------------------------------------------------------------
// wireToChatRequest — translate POST /v1/chat/completions body into canonical
// ----------------------------------------------------------------------------

// wireToChatRequest extracts the canonical request from an OpenAI chat
// completions wire payload. Implements:
//   - D-08: content polymorphism (string-or-array; images skipped)
//   - D-04: system/developer role hoist into ChatRequest.System
//   - D-04: model echoed unconditionally (engine skips SetModel on auto/empty)
//   - X-Working-Dir header → WorkingDirOverride (mirror anthropic/wire.go:132)
//
// Returns only *canonical.ChatRequest — translation cannot fail at this
// layer (validation lives in handleChatCompletions which checks messages
// non-empty before calling).
func wireToChatRequest(w *chatCompletionRequest, r *http.Request) *canonical.ChatRequest {
	// +compress/-compress must be stripped before the base model reaches
	// the engine — see compress.SplitCompressDirective doc comment.
	baseModel, compressDir := compress.SplitCompressDirective(w.Model)
	req := &canonical.ChatRequest{
		Model:              baseModel,
		Stream:             w.Stream,
		WorkingDirOverride: r.Header.Get("X-Working-Dir"),
	}

	// Optional fields carried from the wire struct (Phase 3 accepts-and-ignores
	// max_tokens / max_completion_tokens for canonical plumbing; engine uses
	// MaxTokens when non-zero).
	if w.MaxCompletionTokens > 0 {
		req.MaxTokens = w.MaxCompletionTokens
	} else if w.MaxTokens > 0 {
		req.MaxTokens = w.MaxTokens
	}
	req.Temperature = w.Temperature
	req.TopP = w.TopP

	// Walk messages[].
	for _, m := range w.Messages {
		// Map role first so we know whether to hoist to System.
		role := mapOpenAIRole(m.Role)

		// Decode content: try flat string first (cheap); then array-of-parts.
		text := decodeMessageContent(m.Content)

		// system AND developer roles hoist into ChatRequest.System and do NOT
		// appear as canonical Messages[] entries (Pitfall 4). OpenAI carries
		// system as a messages[] entry (unlike Anthropic's top-level field).
		if role == canonical.RoleSystem {
			if req.System != "" {
				// Multiple system/developer messages: join with double-newline
				// (mirrors Anthropic's multi-block system normalization D-08).
				req.System += "\n\n" + text
			} else {
				req.System = text
			}
			continue
		}

		// Multi-turn tool calling: preserve the assistant turn's prior tool
		// calls. arguments is a JSON string on the wire → parse to the
		// canonical map[string]any.
		var toolCalls []canonical.ToolCall
		for _, tc := range m.ToolCalls {
			toolCalls = append(toolCalls, canonical.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: parseToolArguments(tc.Function.Arguments),
			})
		}

		// Carry the message when it has ANY content: text, tool calls
		// (assistant, content null), or a tool result (role:"tool"). Only a
		// wholly empty non-tool message is skipped (accept-and-ignore
		// invariant; mirrors anthropic/wire.go:165).
		if text == "" && len(toolCalls) == 0 && role != canonical.RoleTool {
			continue
		}

		msg := canonical.Message{Role: role, ToolCalls: toolCalls}
		if text != "" {
			msg.Content = []canonical.ContentPart{{
				Kind: canonical.ContentKindText,
				Text: text,
			}}
		}
		if role == canonical.RoleTool {
			msg.ToolCallID = m.ToolCallID
		}
		req.Messages = append(req.Messages, msg)
	}

	// Tools (Phase 6 D-13 + iteration-3 MEDIUM #4): unmarshal each
	// entry independently. Type-invalid entries fail per-entry unmarshal
	// and are skipped with a debug log; decoded-but-semantically-invalid
	// entries (Function nil or Name empty) are skipped silently. Valid
	// siblings are preserved in declaration order. This is the only way
	// to tolerate type-invalid sibling entries without failing the whole
	// request decode (iteration-2 regression).
	for i, raw := range w.Tools {
		var t openAIToolSpec
		if err := json.Unmarshal(raw, &t); err != nil {
			slog.Default().Debug("openai: tools entry decode failed; skipping",
				"index", i,
				"err", err.Error())
			continue
		}
		if t.Function == nil || t.Function.Name == "" {
			continue
		}
		req.Tools = append(req.Tools, canonical.ToolSpec{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}

	// ToolChoice (Phase 6 D-13): polymorphic string-or-object decode.
	// Attempt typed-object decode first (the richer shape); on failure
	// attempt string decode. On second failure (e.g. numeric or unknown
	// type), accept-and-ignore — leave ToolChoice nil. Mirrors the
	// decodeMessageContent string-or-array discipline.
	req.ToolChoice = decodeToolChoice(w.ToolChoice)

	if compressDir != nil {
		if req.Metadata == nil {
			req.Metadata = make(map[string]any, 1)
		}
		req.Metadata[compress.MetadataKey] = *compressDir
	}

	return req
}

// decodeToolChoice polymorphically decodes the OpenAI tool_choice wire
// value (D-13). Returns nil for an absent or unknown-shape value
// (accept-and-ignore per the Phase 3 decode.go:21-26 invariant).
//
// Recognized shapes:
//   - JSON string "auto" → {Type:"auto"}
//   - JSON string "required" → {Type:"required"} (OpenAI spec value;
//     "any" is Anthropic-only and is NOT mapped here)
//   - JSON string "none" → {Type:"none"}
//   - JSON object {"type":"function","function":{"name":"..."}} →
//     {Type:"function", Name:"..."}
//   - Anything else → nil
func decodeToolChoice(raw json.RawMessage) *canonical.ToolChoice {
	if len(raw) == 0 {
		return nil
	}

	// Try typed object first (the richer shape).
	var obj openAIToolChoiceObject
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Type != "" {
		choice := &canonical.ToolChoice{Type: obj.Type}
		if obj.Function != nil {
			choice.Name = obj.Function.Name
		}
		return choice
	}

	// Fall back to string form.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto", "required", "none":
			return &canonical.ToolChoice{Type: s}
		default:
			return nil
		}
	}

	// Unknown shape (numeric, array, etc.) — accept-and-ignore.
	return nil
}

// decodeMessageContent decodes an OpenAI messages[].content field from its
// json.RawMessage form into a flat string. Implements Pitfall 5:
//   - String form: `"hello world"` → "hello world"
//   - Array form: `[{"type":"text","text":"hello"},{"type":"text","text":" world"}]`
//     → joined text parts "hello world"
//   - Malformed or empty: return "" (permissive, no 400 at decode layer)
//
// parseToolArguments decodes an OpenAI tool-call arguments string (a
// JSON-encoded object, e.g. `{"city":"Paris"}`) into the canonical
// map[string]any. An empty or unparseable value yields an empty map so a
// malformed arguments field degrades to a zero-arg call rather than
// aborting the request decode.
func parseToolArguments(s string) map[string]any {
	if s == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		slog.Default().Debug("openai: tool_call arguments not valid JSON object; using empty args",
			"err", err.Error())
		return map[string]any{}
	}
	return m
}

func decodeMessageContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try flat string first (the common path for simple clients).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Array-of-parts form.
	var parts []openAIContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		// Malformed content — return empty (permissive per D-10).
		return ""
	}

	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
		// Non-text parts (image, etc.) are skipped — Phase 3 scope.
	}
	return sb.String()
}

// ----------------------------------------------------------------------------
// Wire request shapes (POST /v1/completions legacy shim — D-03)
// ----------------------------------------------------------------------------

// completionWireRequest mirrors the legacy OpenAI text completions request.
// Prompt is json.RawMessage because it accepts either a string or a []string.
//
// Accept-and-ignore extras (per D-03 / RESEARCH.md §Pattern 4): Logprobs,
// Echo, Suffix, BestOf, N, MaxTokens decode without a 400. Stream bool
// defaults false; handleCompletions silently sets it to false if true
// (JSON-only shim; resolved Open Question 2 — no Phase 3 client drives
// completions streaming). No DisallowUnknownFields per decode.go invariant.
type completionWireRequest struct {
	Model  string          `json:"model"`
	Prompt json.RawMessage `json:"prompt"`           // string OR []string (polymorphic)
	Stream bool            `json:"stream,omitempty"` // silently downgraded to false

	// Accepted-and-ignored advanced params (D-03 — kiro-cli backend ignores them;
	// T-03-23 accept disposition in threat register).
	Logprobs json.RawMessage `json:"logprobs,omitempty"`
	Echo     json.RawMessage `json:"echo,omitempty"`
	Suffix   json.RawMessage `json:"suffix,omitempty"`
	BestOf   json.RawMessage `json:"best_of,omitempty"`
	N        json.RawMessage `json:"n,omitempty"`
	// MaxTokens is json.RawMessage (NOT int) on the legacy /v1/completions
	// path because it is accept-and-ignore (D-03) — kiro-cli does not expose
	// a max_tokens lever on the backend. Using RawMessage lets us decode any
	// shape (int, null, string-int variants) without 400'ing on type drift.
	//
	// WR-08 (Phase 6 review): this is INTENTIONALLY asymmetric with
	// chatCompletionRequest.MaxTokens (which is `int` at line 34) — the
	// chat-completion path propagates max_tokens into the canonical request
	// while the legacy completion path silently drops it. A client that
	// sends {"max_tokens": 100} to /v1/completions sees no enforcement; the
	// same payload to /v1/chat/completions sets req.MaxTokens. This
	// asymmetry is documented (not a bug) — if kiro-cli ever grows a
	// max-tokens lever, hoist completionWireRequest.MaxTokens to `int` and
	// wire it through promptToMessages.
	MaxTokens json.RawMessage `json:"max_tokens,omitempty"`
}

// promptToMessages decodes the polymorphic Prompt field (string or []string)
// into a single canonical.Message with role=RoleUser. Returns an error if
// the prompt is empty or unparseable (→ 400 in handleCompletions).
//
// Array form: elements are joined with "\n" to preserve line boundaries.
func promptToMessages(raw json.RawMessage) ([]canonical.Message, error) {
	if len(raw) == 0 {
		return nil, errors.New("prompt is required")
	}

	// Try string first (common path).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, errors.New("prompt must not be empty")
		}
		return []canonical.Message{
			{
				Role: canonical.RoleUser,
				Content: []canonical.ContentPart{{
					Kind: canonical.ContentKindText,
					Text: s,
				}},
			},
		}, nil
	}

	// Array-of-strings form.
	var parts []string
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, errors.New("prompt must be a string or array of strings")
	}
	if len(parts) == 0 {
		return nil, errors.New("prompt array must not be empty")
	}
	joined := strings.Join(parts, "\n")
	if joined == "" {
		return nil, errors.New("prompt array produced empty content")
	}
	return []canonical.Message{
		{
			Role: canonical.RoleUser,
			Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindText,
				Text: joined,
			}},
		},
	}, nil
}

// mapOpenAIRole translates the OpenAI wire role string into the canonical
// MessageRole enum. Both "system" AND "developer" roles map to RoleSystem
// (Pitfall 4 — Pi may send either depending on compat.supportsDeveloperRole).
// Unknown roles default to RoleUser (canonical zero value, per anthropic analog).
func mapOpenAIRole(s string) canonical.MessageRole {
	switch s {
	case "system", "developer":
		return canonical.RoleSystem
	case "assistant":
		return canonical.RoleAssistant
	case "tool":
		return canonical.RoleTool
	default:
		return canonical.RoleUser
	}
}
