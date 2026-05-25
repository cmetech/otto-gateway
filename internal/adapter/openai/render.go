package openai

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"otto-gateway/internal/canonical"
)

// ----------------------------------------------------------------------------
// GET /v1/models render shapes (RESEARCH.md §Pattern 5)
//
// Field names are validated against Bifrost core/providers/openai/types.go:857-860:
// id, object, created (unix seconds), owned_by.
// ----------------------------------------------------------------------------

// modelList is the OpenAI models/list response object.
// object is always "list"; data contains one entry per model.
type modelList struct {
	Object string      `json:"object"` // "list"
	Data   []modelInfo `json:"data"`
}

// modelInfo is one entry in the models list.
// object is always "model"; created is a fixed unix-seconds timestamp
// (Pitfall 8 style — a stable per-boot value is acceptable);
// owned_by identifies the serving backend.
type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`   // "model"
	Created int64  `json:"created"`  // unix seconds
	OwnedBy string `json:"owned_by"` // "kiro" or "otto-gateway"
}

// catalogToModelList builds the OpenAI modelList from the pool catalog.
// "auto" is always prepended (mirror ollama/handlers.go:108-109 Node parity).
// Each entry gets object:"model", the provided ownedBy and created values.
// A nil or empty models slice returns only the "auto" entry.
//
// SC3: the same pool catalog source as /api/tags → same model set by construction.
func catalogToModelList(models []canonical.ModelInfo, ownedBy string, created int64) modelList {
	data := make([]modelInfo, 0, 1+len(models))
	// Prepend the synthetic "auto" entry (always present — Node parity).
	data = append(data, modelInfo{
		ID:      "auto",
		Object:  "model",
		Created: created,
		OwnedBy: ownedBy,
	})
	for _, m := range models {
		data = append(data, modelInfo{
			ID:      m.ID,
			Object:  "model",
			Created: created,
			OwnedBy: ownedBy,
		})
	}
	return modelList{Object: "list", Data: data}
}

// ----------------------------------------------------------------------------
// Non-streaming response wire shape (POST /v1/chat/completions, stream:false)
//
// Field order is LOAD-BEARING: encoding/json walks struct fields in
// declaration order. Golden-fixture tests compare byte-exact output against
// canonical OpenAI wire bytes. Any reordering here will break those tests;
// reorder the golden file too if you intentionally change a payload shape.
// ----------------------------------------------------------------------------

// chatCompletion is the OpenAI chat.completion response object per
// RESEARCH.md §Pattern 3. Object is always "chat.completion"; Usage
// carries honest zeros per D-12.
type chatCompletion struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`  // "chat.completion"
	Created int64              `json:"created"` // unix seconds
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   completionUsage    `json:"usage"`
}

// completionChoice is one entry of choices[]. Index is always 0 (n>1
// unsupported in Phase 3). FinishReason is a non-null string — OpenAI
// never emits null for the non-streaming terminal finish_reason.
type completionChoice struct {
	Index        int             `json:"index"`
	Message      responseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"` // non-null (not *string)
}

// responseMessage is the message object inside a non-streaming choice.
// Role is always "assistant"; Content is the joined text output.
type responseMessage struct {
	Role    string `json:"role"`    // "assistant"
	Content string `json:"content"` // joined text parts
}

// completionUsage is the token-accounting envelope. Phase 3 emits honest
// zeros per D-12; field names differ from Anthropic's (prompt_tokens vs
// input_tokens per the OpenAI spec).
type completionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// chatResponseToCompletion renders a canonical.ChatResponse into the
// OpenAI chat.completion wire shape. requestedModel is echoed back to
// the client (A3 opaque echo); if empty the resp.Model is used.
//
// Content joined via joinTextContent (multi-part text concatenation);
// defensive empty: emit content:"" if no text produced (mirror
// anthropic/render.go:140-142).
// finish_reason is always non-null (mapped string, never *string) per
// the OpenAI spec.
func chatResponseToCompletion(resp *canonical.ChatResponse, requestedModel string) chatCompletion {
	out := chatCompletion{
		ID:      genMessageID("chatcmpl-"),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Usage: completionUsage{
			PromptTokens:     0, // D-12 honest zeros
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	out.Model = requestedModel
	if out.Model == "" && resp != nil {
		out.Model = resp.Model
	}

	text := ""
	stopReason := canonical.StopUnknown
	if resp != nil {
		text = joinTextContent(resp.Message.Content)
		stopReason = resp.StopReason
	}

	out.Choices = []completionChoice{
		{
			Index: 0,
			Message: responseMessage{
				Role:    "assistant",
				Content: text,
			},
			FinishReason: mapFinishReason(stopReason),
		},
	}

	return out
}

// mapFinishReason translates the canonical StopReason enum to the OpenAI
// finish_reason wire value. OpenAI's terminal-chunk finish_reason is
// ALWAYS non-null — return string (not *string). Default is "stop".
//
// Mapping per RESEARCH.md §Pattern 2 + interfaces block:
//   - StopEndTurn → "stop"
//   - StopMaxTokens → "length"
//   - StopMaxTurnRequests → "length" (closest semantic)
//   - StopRefusal → "content_filter" (closest OpenAI enum)
//   - StopCancelled → "stop" (planner pick per A3 — no "cancelled" in OpenAI spec)
//   - StopUnknown → "stop" (safe default; OpenAI never emits null on final chunk)
func mapFinishReason(s canonical.StopReason) string {
	switch s {
	case canonical.StopEndTurn:
		return "stop"
	case canonical.StopMaxTokens:
		return "length"
	case canonical.StopMaxTurnRequests:
		return "length"
	case canonical.StopRefusal:
		return "content_filter"
	case canonical.StopCancelled:
		return "stop"
	default: // StopUnknown
		return "stop"
	}
}

// genMessageID generates a per-response opaque id with the given prefix.
// Format: "<prefix><24 hex chars>" — e.g., "chatcmpl-<hex>" for chat
// completions and "cmpl-<hex>" for /v1/completions responses.
//
// Uses crypto/rand (non-blocking on all supported OS per Go docs). On
// error returns a fixed fallback string — the response is still valid JSON,
// but the error indicates a serious system-level failure.
func genMessageID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "rand_unavailable"
	}
	return prefix + hex.EncodeToString(b[:])
}

// ----------------------------------------------------------------------------
// POST /v1/completions render shapes (RESEARCH.md §Pattern 4)
// ----------------------------------------------------------------------------

// textCompletion is the OpenAI text_completion response object.
// object is always "text_completion"; logprobs in the choices is always null
// (D-03 accept-and-ignore — kiro-cli backend cannot honor logprobs).
type textCompletion struct {
	ID      string       `json:"id"`      // "cmpl-…"
	Object  string       `json:"object"`  // "text_completion"
	Created int64        `json:"created"` // unix seconds
	Model   string       `json:"model"`
	Choices []textChoice `json:"choices"`
	Usage   completionUsage `json:"usage"` // honest zeros (D-12)
}

// textChoice is one entry in the text_completion choices[].
// Text carries the assistant's output directly (not a message object).
// FinishReason is always a non-null mapped string.
// Logprobs is always null (D-03 accept-and-ignore; RESEARCH.md §Pattern 4).
type textChoice struct {
	Index        int       `json:"index"`
	Text         string    `json:"text"`
	FinishReason string    `json:"finish_reason"` // non-null mapped string
	Logprobs     *struct{} `json:"logprobs"`       // always null
}

// chatResponseToTextCompletion renders a canonical.ChatResponse into the
// OpenAI text_completion wire shape. requestedModel is echoed back to the
// client. Uses joinTextContent + mapFinishReason + genMessageID("cmpl-").
// Nil resp is handled defensively (empty text, StopUnknown → "stop").
func chatResponseToTextCompletion(resp *canonical.ChatResponse, requestedModel string) textCompletion {
	out := textCompletion{
		ID:      genMessageID("cmpl-"),
		Object:  "text_completion",
		Created: time.Now().Unix(),
		Model:   requestedModel,
		Usage: completionUsage{
			PromptTokens:     0, // D-12 honest zeros
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	text := ""
	stopReason := canonical.StopUnknown
	if resp != nil {
		text = joinTextContent(resp.Message.Content)
		stopReason = resp.StopReason
		if out.Model == "" {
			out.Model = resp.Model
		}
	}

	out.Choices = []textChoice{
		{
			Index:        0,
			Text:         text,
			FinishReason: mapFinishReason(stopReason),
			Logprobs:     nil, // always null per D-03
		},
	}

	return out
}

// joinTextContent concatenates the Text fields of every ContentPart
// whose Kind == ContentKindText. Non-text parts are skipped.
// Copied verbatim from internal/adapter/ollama/render.go:135-146.
func joinTextContent(parts []canonical.ContentPart) string {
	if len(parts) == 0 {
		return ""
	}
	out := ""
	for _, p := range parts {
		if p.Kind == canonical.ContentKindText {
			out += p.Text
		}
	}
	return out
}
