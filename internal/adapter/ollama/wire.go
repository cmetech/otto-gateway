package ollama

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin/compress"
)

// decodeFormat decodes the Ollama wire `format` field (json.RawMessage) into
// a *canonical.Format per D-02's accepted/rejected shape matrix:
//
//   - nil / empty / "null" → (nil, nil)
//   - string "json"        → (&Format{Type:"json"}, nil)
//   - string ""            → (nil, nil)   treat empty string as omitted
//   - any other string     → (nil, error) only "json" is a valid bare string
//   - JSON object          → (&Format{Type:"json_schema", Schema:…}, nil)
//   - number / array / etc → (nil, error)
func decodeFormat(raw json.RawMessage) (*canonical.Format, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	// Try to decode as string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "":
			return nil, nil
		case "json":
			return &canonical.Format{Type: "json"}, nil
		default:
			return nil, fmt.Errorf("ollama: unsupported format string %q (only \"json\" or a schema object is accepted)", s)
		}
	}

	// Try to decode as object (JSON schema).
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		return &canonical.Format{Type: "json_schema", Schema: m}, nil
	}

	return nil, fmt.Errorf("ollama: format must be the string \"json\" or a JSON-schema object; got %s", string(raw))
}

// ----------------------------------------------------------------------------
// Chat wire shape (POST /api/chat)
// ----------------------------------------------------------------------------

// ollamaChatRequest mirrors the public Ollama /api/chat request body
// (RESEARCH.md §"Ollama Wire Shapes" — VERIFIED against the Node reference
// acp-ollama-server.js and the public Ollama API spec). KeepAlive and
// Options are accepted-and-ignored: LangFlow sends them and Phase 2 must
// not 400 on their presence. Format and Options use json.RawMessage as
// forward-design seams (Format also accepts a bare string like "json").
type ollamaChatRequest struct {
	Model     string           `json:"model"`
	Messages  []ollamaMessage  `json:"messages"`
	Tools     []ollamaToolSpec `json:"tools,omitempty"`
	Format    json.RawMessage  `json:"format,omitempty"`
	Stream    *bool            `json:"stream,omitempty"`
	Think     bool             `json:"think,omitempty"`
	KeepAlive json.RawMessage  `json:"keep_alive,omitempty"` // accepted-and-ignored
	Options   json.RawMessage  `json:"options,omitempty"`    // accepted-and-ignored
}

// ollamaMessage mirrors one entry of /api/chat messages[]. Content is a
// flat string per Ollama (NOT the OpenAI content-parts array). Images is
// a slice of base64-encoded payloads (no MIME — wire.go's detectMIME
// peeks the bytes after base64-decode).
type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Images    []string         `json:"images,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

// ollamaToolSpec is a forward-design seam — tools[] from /api/chat. Phase
// 2 accepts but does not act on tools (mapped onto canonical.ToolSpec but
// engine.buildBlocks emits only a placeholder bracketed-section header).
type ollamaToolSpec struct {
	Type     string                  `json:"type,omitempty"`
	Function *ollamaToolSpecFunction `json:"function,omitempty"`
}

type ollamaToolSpecFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ollamaToolCall is the Ollama tool-call shape used in /api/chat responses
// AND in assistant-turn echo back from the client. Phase 2 does not
// populate this on the response side (tool dispatch deferred to Phase 6).
type ollamaToolCall struct {
	Function ollamaToolCallFunction `json:"function"`
}

type ollamaToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ollamaChatResponse is the Ollama /api/chat response shape (RESEARCH.md
// "Response body" — every field is observed in the Node reference
// chunksToOllamaMessage + makeStats output).
type ollamaChatResponse struct {
	Model              string                    `json:"model"`
	CreatedAt          string                    `json:"created_at"`
	Message            ollamaChatResponseMessage `json:"message"`
	Done               bool                      `json:"done"`
	DoneReason         string                    `json:"done_reason"`
	TotalDuration      int64                     `json:"total_duration"`
	LoadDuration       int64                     `json:"load_duration"`
	PromptEvalCount    int                       `json:"prompt_eval_count"`
	PromptEvalDuration int64                     `json:"prompt_eval_duration"`
	EvalCount          int                       `json:"eval_count"`
	EvalDuration       int64                     `json:"eval_duration"`
	// Error carries a free-form error message on the terminal frame for
	// failure paths (e.g. stream-idle timeout). Audit
	// ollama-ndjson-idle-timeout-terminal-frame-missing-fields.
	Error string `json:"error,omitempty"`
}

type ollamaChatResponseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is the assistant's outbound tool invocations on the
	// response side. Phase 6 D-04/D-15: this field carries the Ollama
	// wire shape for tool calls — Arguments is a plain JSON OBJECT
	// (map[string]any), NOT a JSON-encoded string (that is the OpenAI
	// surface's convention). The field is symmetric with the request-side
	// ollamaMessage.ToolCalls.
	//
	// This field is populated for the Ollama surface by engine.CoerceToolCall
	// (the coerce-from-text rescue) AND by kiro-native tool_call chunks
	// surfaced structurally (Defect 1c) — non-streaming via engine.Collect,
	// streaming via ndjson.go's done:true accumulation. Arguments render as a
	// plain JSON object.
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	Thinking  string           `json:"thinking,omitempty"`
}

// ----------------------------------------------------------------------------
// Generate wire shape (POST /api/generate)
// ----------------------------------------------------------------------------

// ollamaGenerateRequest mirrors the Ollama /api/generate request body
// (single-turn variant of /api/chat). Suffix/Raw/KeepAlive/Options are
// accepted-and-ignored (LangFlow may set them).
type ollamaGenerateRequest struct {
	Model     string          `json:"model"`
	Prompt    string          `json:"prompt"`
	System    string          `json:"system,omitempty"`
	Images    []string        `json:"images,omitempty"`
	Format    json.RawMessage `json:"format,omitempty"`
	Stream    *bool           `json:"stream,omitempty"`
	Think     bool            `json:"think,omitempty"`
	Suffix    string          `json:"suffix,omitempty"`     // accepted-and-ignored
	Raw       bool            `json:"raw,omitempty"`        // accepted-and-ignored
	KeepAlive json.RawMessage `json:"keep_alive,omitempty"` // accepted-and-ignored
	Options   json.RawMessage `json:"options,omitempty"`    // accepted-and-ignored
}

// ollamaGenerateResponse mirrors /api/generate — same envelope as
// /api/chat except the assistant text lives in `response` (a string),
// not `message: {...}`.
type ollamaGenerateResponse struct {
	Model              string `json:"model"`
	CreatedAt          string `json:"created_at"`
	Response           string `json:"response"`
	Done               bool   `json:"done"`
	DoneReason         string `json:"done_reason"`
	TotalDuration      int64  `json:"total_duration"`
	LoadDuration       int64  `json:"load_duration"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	PromptEvalDuration int64  `json:"prompt_eval_duration"`
	EvalCount          int    `json:"eval_count"`
	EvalDuration       int64  `json:"eval_duration"`
	// Error mirrors ollamaChatResponse.Error for the /api/generate
	// terminal-error path. Audit
	// ollama-ndjson-idle-timeout-terminal-frame-missing-fields.
	Error string `json:"error,omitempty"`
}

// ----------------------------------------------------------------------------
// Tags / Show / PS wire shapes
// ----------------------------------------------------------------------------

// ollamaModelTag is one entry of GET /api/tags response models[] —
// mirrors the Node reference toOllamaModel (acp-ollama-server.js:735-749).
type ollamaModelTag struct {
	Name       string                `json:"name"`
	Model      string                `json:"model"`
	ModifiedAt string                `json:"modified_at"`
	Size       int64                 `json:"size"`
	Digest     string                `json:"digest"`
	Details    ollamaModelTagDetails `json:"details"`
}

type ollamaModelTagDetails struct {
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

// ollamaTagsResponse wraps the models[] array (GET /api/tags).
type ollamaTagsResponse struct {
	Models []ollamaModelTag `json:"models"`
}

// ollamaShowRequest is the POST /api/show body (single field).
type ollamaShowRequest struct {
	Model string `json:"model"`
}

// ollamaShowResponse mirrors the Node reference /api/show envelope
// (acp-ollama-server.js:761-776).
type ollamaShowResponse struct {
	Model        string                `json:"model"`
	ModifiedAt   string                `json:"modified_at"`
	Details      ollamaModelTagDetails `json:"details"`
	Capabilities []string              `json:"capabilities"`
	Modelinfo    map[string]any        `json:"modelinfo"`
	Template     string                `json:"template"`
	Parameters   string                `json:"parameters"`
	License      string                `json:"license"`
}

// ollamaPSEntry is one entry of GET /api/ps response models[] — Node
// reference synthetic shape (acp-ollama-server.js:778-789).
type ollamaPSEntry struct {
	Name      string                `json:"name"`
	Model     string                `json:"model"`
	Size      int64                 `json:"size"`
	SizeVRAM  int64                 `json:"size_vram"`
	Details   ollamaModelTagDetails `json:"details"`
	ExpiresAt string                `json:"expires_at"`
}

type ollamaPSResponse struct {
	Models []ollamaPSEntry `json:"models"`
}

// ollamaVersionResponse is the GET /api/version body — OTTO extension
// over the public Ollama spec (which returns only {version}). The
// `commit` field is a non-breaking extension; LangFlow ignores it.
type ollamaVersionResponse struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// ----------------------------------------------------------------------------
// Stub wire shapes
// ----------------------------------------------------------------------------

// ollamaStubStreamRequest is the common body shape for /api/pull /push
// /create — only `stream` is read (default true). Modelname etc. are
// accepted-and-ignored.
type ollamaStubStreamRequest struct {
	Stream *bool `json:"stream,omitempty"`
}

// ollamaStubStatusLine is one NDJSON line emitted by /api/pull (and the
// shape returned by /api/pull?stream=false).
type ollamaStubStatusLine struct {
	Status string `json:"status"`
}

// ollamaCopyRequest is the POST /api/copy body — source / destination are
// accepted-and-ignored; the handler returns `{}` regardless. Body shape is
// decoded only to apply the size cap (Codex M-5).
type ollamaCopyRequest struct {
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination,omitempty"`
}

// ollamaDeleteRequest is the DELETE /api/delete body — `name` is
// accepted-and-ignored. Same Codex M-5 size-cap rationale.
type ollamaDeleteRequest struct {
	Name string `json:"name,omitempty"`
}

// ----------------------------------------------------------------------------
// wireToChatRequest — translate POST /api/chat body into canonical.ChatRequest
// ----------------------------------------------------------------------------

// streamEnabled returns true when the *bool stream field is absent (nil —
// Ollama default is stream:true per Node reference) or explicitly true.
// Returns false only for explicit stream:false. (CONTEXT.md D-03,
// RESEARCH.md Pitfall 1)
func streamEnabled(s *bool) bool { return s == nil || *s }

// wireToChatRequest extracts the canonical request from an Ollama chat
// wire payload. Layout:
//   - first messages[] entry with role=="system" → req.System (then
//     ALSO appended as a RoleSystem message — buildBlocks skips it but
//     the round-trip is preserved for callers inspecting Messages).
//   - other messages map to canonical.Message{Role, Content:[{Kind:Text}]}.
//   - per-message images[] (base64 strings) become additional
//     ContentPart{Kind:Image, Image:&ImagePart{MIME, DataBase64}} entries
//     APPENDED to the message's Content slice (text first, images after).
//     Malformed base64 is skipped silently (Codex M-1 defensive — a single
//     corrupt image must not abort wireToChatRequest).
//   - WorkingDirOverride sourced from the X-Working-Dir header.
//   - Stream / Think copied through.
//   - Format decoded via decodeFormat; caller must propagate the error as HTTP 400.
//
// Returns (*canonical.ChatRequest, error); error is non-nil only when the
// format field has an invalid shape (D-02 rejected shapes). Callers map
// the error to a 400 response.
func wireToChatRequest(w *ollamaChatRequest, r *http.Request) (*canonical.ChatRequest, error) {
	format, err := decodeFormat(w.Format)
	if err != nil {
		return nil, err
	}

	// +compress/-compress must be stripped before the base model reaches
	// the engine — see compress.SplitCompressDirective doc comment.
	baseModel, compressDir := compress.SplitCompressDirective(w.Model)
	req := &canonical.ChatRequest{
		Model:              baseModel,
		Stream:             streamEnabled(w.Stream),
		Think:              w.Think,
		WorkingDirOverride: r.Header.Get("X-Working-Dir"),
		Format:             format,
	}
	if compressDir != nil {
		req.Metadata = map[string]any{compress.MetadataKey: *compressDir}
	}

	// Extract the FIRST system message — the engine's buildBlocks
	// reads req.System (not the Messages-with-RoleSystem path) for the
	// [System] bracketed-section header.
	for _, m := range w.Messages {
		if m.Role == "system" && req.System == "" {
			req.System = m.Content
			break
		}
	}

	for _, m := range w.Messages {
		role := mapRole(m.Role)

		var parts []canonical.ContentPart
		if m.Content != "" {
			parts = append(parts, canonical.ContentPart{
				Kind: canonical.ContentKindText,
				Text: m.Content,
			})
		}

		// Codex M-1: every base64 image string becomes one
		// ContentKindImage part appended after the text. detectMIME
		// peeks the decoded bytes to assign image/png|jpeg|gif|
		// application/octet-stream.
		for _, b64 := range m.Images {
			data, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				// Defensive: malformed base64 is dropped, prompt continues.
				continue
			}
			parts = append(parts, canonical.ContentPart{
				Kind: canonical.ContentKindImage,
				Image: &canonical.ImagePart{
					MIME:       detectMIME(data),
					DataBase64: b64,
				},
			})
		}

		// Multi-turn tool calling: preserve the assistant turn's prior tool
		// calls so engine.buildBlocks can replay them as [Assistant tool
		// call] sections. Ollama arguments are already an object
		// (map[string]any); no string parse needed (unlike OpenAI).
		var toolCalls []canonical.ToolCall
		for _, tc := range m.ToolCalls {
			toolCalls = append(toolCalls, canonical.ToolCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}

		// Skip only wholly empty messages. A tool-call-only assistant turn
		// (no content) is carried on the strength of ToolCalls; a role:"tool"
		// result is carried so buildBlocks can render its [Tool result].
		if len(parts) == 0 && len(toolCalls) == 0 && role != canonical.RoleTool {
			continue
		}

		req.Messages = append(req.Messages, canonical.Message{
			Role:      role,
			Content:   parts,
			ToolCalls: toolCalls,
		})
	}

	// Tools (forward-design seam): copy onto canonical.ToolSpec so
	// engine.buildBlocks emits the placeholder [Available tools]
	// section. Phase 2 does not act on tools beyond bracketing them.
	for _, t := range w.Tools {
		if t.Function == nil {
			continue
		}
		req.Tools = append(req.Tools, canonical.ToolSpec{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}

	return req, nil
}

// wireGenerateToChatRequest converts an Ollama /api/generate body into a
// canonical.ChatRequest. /api/generate is the single-turn variant —
// system field maps to req.System; prompt becomes a single RoleUser
// message; images attach to that message.
//
// Returns (*canonical.ChatRequest, error); error is non-nil only when the
// format field has an invalid shape (D-02 rejected shapes). Callers map
// the error to a 400 response.
func wireGenerateToChatRequest(w *ollamaGenerateRequest, r *http.Request) (*canonical.ChatRequest, error) {
	format, err := decodeFormat(w.Format)
	if err != nil {
		return nil, err
	}

	// +compress/-compress must be stripped before the base model reaches
	// the engine — see compress.SplitCompressDirective doc comment.
	baseModel, compressDir := compress.SplitCompressDirective(w.Model)
	req := &canonical.ChatRequest{
		Model:              baseModel,
		System:             w.System,
		Stream:             streamEnabled(w.Stream),
		Think:              w.Think,
		WorkingDirOverride: r.Header.Get("X-Working-Dir"),
		Format:             format,
	}
	if compressDir != nil {
		req.Metadata = map[string]any{compress.MetadataKey: *compressDir}
	}

	var parts []canonical.ContentPart
	if w.Prompt != "" {
		parts = append(parts, canonical.ContentPart{
			Kind: canonical.ContentKindText,
			Text: w.Prompt,
		})
	}
	for _, b64 := range w.Images {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			continue
		}
		parts = append(parts, canonical.ContentPart{
			Kind: canonical.ContentKindImage,
			Image: &canonical.ImagePart{
				MIME:       detectMIME(data),
				DataBase64: b64,
			},
		})
	}
	if len(parts) > 0 {
		req.Messages = append(req.Messages, canonical.Message{
			Role:    canonical.RoleUser,
			Content: parts,
		})
	}

	return req, nil
}

// mapRole translates the Ollama wire role string into the canonical
// MessageRole enum. Unknown roles default to RoleUser (canonical's zero
// value) so a stray "function" or "developer" role does not crash the
// adapter.
func mapRole(s string) canonical.MessageRole {
	switch s {
	case "system":
		return canonical.RoleSystem
	case "assistant":
		return canonical.RoleAssistant
	case "tool":
		return canonical.RoleTool
	default:
		return canonical.RoleUser
	}
}

// detectMIME inspects the leading bytes of a decoded image payload and
// returns the matching MIME type. Codex M-1: avoids requiring the wire
// shape to carry MIME (Ollama messages[].images does not) and matches
// the engine.buildBlocks ImageBlock.MIMEType contract.
func detectMIME(data []byte) string {
	switch {
	case len(data) >= 4 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4e && data[3] == 0x47:
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 4 && data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 && data[3] == 0x38:
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}
