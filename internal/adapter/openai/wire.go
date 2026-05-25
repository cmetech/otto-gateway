package openai

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"otto-gateway/internal/canonical"
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
	StreamOptions        json.RawMessage `json:"stream_options,omitempty"`
	MaxTokens            int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens  int             `json:"max_completion_tokens,omitempty"`
	Temperature          *float64        `json:"temperature,omitempty"`
	TopP                 *float64        `json:"top_p,omitempty"`
	Logprobs             json.RawMessage `json:"logprobs,omitempty"`
	Tools                json.RawMessage `json:"tools,omitempty"`
	FunctionCall         json.RawMessage `json:"function_call,omitempty"`
	StopSequences        json.RawMessage `json:"stop,omitempty"`
}

// chatMessage is one entry of messages[]. Content is json.RawMessage
// because the OpenAI wire shape accepts either a flat string ("hello")
// OR an array of content-part objects ([{"type":"text","text":"hello"}]).
// wireToChatRequest decodes both forms (Pitfall 5 / anthropic/wire.go:43-44).
type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
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
	req := &canonical.ChatRequest{
		Model:              w.Model,
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

		if text == "" {
			// Empty content after decode — skip the message (permissive per
			// the accept-and-ignore invariant; mirrors anthropic/wire.go:165).
			continue
		}

		req.Messages = append(req.Messages, canonical.Message{
			Role: role,
			Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindText,
				Text: text,
			}},
		})
	}

	return req
}

// decodeMessageContent decodes an OpenAI messages[].content field from its
// json.RawMessage form into a flat string. Implements Pitfall 5:
//   - String form: `"hello world"` → "hello world"
//   - Array form: `[{"type":"text","text":"hello"},{"type":"text","text":" world"}]`
//     → joined text parts "hello world"
//   - Malformed or empty: return "" (permissive, no 400 at decode layer)
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
