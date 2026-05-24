package ollama

import (
	"math"
	"time"

	"loop24-gateway/internal/canonical"
)

// chatResponseToWire converts a canonical.ChatResponse into the Ollama
// /api/chat wire shape. Behaviour mirrors the Node reference
// chunksToOllamaMessage + makeStats (acp-ollama-server.js:559-611):
//   - created_at: time.Now().UTC().Format(time.RFC3339Nano) (Node uses ISO
//     timestamp with millisecond precision; Go RFC3339Nano gives 9
//     significant digits which is a strict superset — LangFlow does not
//     reject it).
//   - done: true (Phase 2 only does non-streaming completion).
//   - done_reason: mapStopReason(resp.StopReason) — StopEndTurn → "stop",
//     StopMaxTokens → "length", everything else → "stop".
//   - duration split: 15% prompt_eval_duration, 85% eval_duration (Node
//     convention, lines 600-611).
//   - token counts: estimateTokens(text) = (len + 3) / 4 — integer
//     equivalent of Node's Math.ceil(text.length / 4).
//   - requestedModel takes precedence over resp.Model so LangFlow sees
//     back the model name it sent (it may have sent "auto"; resp.Model
//     also tends to be "auto" since SetModel was skipped).
func chatResponseToWire(resp *canonical.ChatResponse, start time.Time, requestedModel string) *ollamaChatResponse {
	totalNs := time.Since(start).Nanoseconds()

	text := ""
	thinking := ""
	if resp != nil {
		text = joinTextContent(resp.Message.Content)
		thinking = joinThinkingContent(resp.Message.Content)
	}

	model := requestedModel
	if model == "" && resp != nil {
		model = resp.Model
	}

	promptTokens := 0
	if resp != nil {
		// Best-effort prompt token estimate from the system + any user
		// turns we can see. Phase 2 does not retain the prompt at
		// render time, so this is approximate — Node uses the same
		// estimator and accepts the approximation.
		promptTokens = estimateTokens("")
	}

	stop := canonical.StopUnknown
	if resp != nil {
		stop = resp.StopReason
	}

	out := &ollamaChatResponse{
		Model:     model,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Message: ollamaChatResponseMessage{
			Role:     "assistant",
			Content:  text,
			Thinking: thinking,
		},
		Done:               true,
		DoneReason:         mapStopReason(stop),
		TotalDuration:      totalNs,
		LoadDuration:       0,
		PromptEvalCount:    promptTokens,
		PromptEvalDuration: int64(math.Floor(float64(totalNs) * 0.15)),
		EvalCount:          estimateTokens(text),
		EvalDuration:       int64(math.Floor(float64(totalNs) * 0.85)),
	}
	return out
}

// generateResponseToWire converts a canonical.ChatResponse into the
// Ollama /api/generate wire shape. Difference vs chatResponseToWire: the
// assistant text lives in `response` (a string), not `message: {...}`.
func generateResponseToWire(resp *canonical.ChatResponse, start time.Time, requestedModel string) *ollamaGenerateResponse {
	totalNs := time.Since(start).Nanoseconds()

	text := ""
	if resp != nil {
		text = joinTextContent(resp.Message.Content)
	}

	model := requestedModel
	if model == "" && resp != nil {
		model = resp.Model
	}

	stop := canonical.StopUnknown
	if resp != nil {
		stop = resp.StopReason
	}

	return &ollamaGenerateResponse{
		Model:              model,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		Response:           text,
		Done:               true,
		DoneReason:         mapStopReason(stop),
		TotalDuration:      totalNs,
		LoadDuration:       0,
		PromptEvalCount:    0,
		PromptEvalDuration: int64(math.Floor(float64(totalNs) * 0.15)),
		EvalCount:          estimateTokens(text),
		EvalDuration:       int64(math.Floor(float64(totalNs) * 0.85)),
	}
}

// mapStopReason translates the canonical StopReason enum to the Ollama
// done_reason string. Matches Node parity (Phase 2's only two
// distinguished values are "stop" and "length").
func mapStopReason(s canonical.StopReason) string {
	switch s {
	case canonical.StopEndTurn:
		return "stop"
	case canonical.StopMaxTokens:
		return "length"
	default:
		return "stop"
	}
}

// estimateTokens returns the Node-parity token estimate Math.ceil(len/4).
// Implemented as integer (len + 3) / 4 — exact ceiling of the JS form
// for non-negative len.
func estimateTokens(text string) int {
	return (len(text) + 3) / 4
}

// joinTextContent concatenates the Text fields of every ContentPart
// whose Kind == ContentKindText. Non-text parts are skipped.
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

// joinThinkingContent concatenates the Text fields of any
// ContentKindThinking parts so that /api/chat responses honor
// think:true round-trip (Assumption A4 in RESEARCH.md). Phase 2 keeps
// this empty for non-thinking requests; the field is omitempty so it
// disappears entirely when there is no thinking content.
func joinThinkingContent(parts []canonical.ContentPart) string {
	if len(parts) == 0 {
		return ""
	}
	out := ""
	for _, p := range parts {
		if p.Kind == canonical.ContentKindThinking {
			out += p.Text
		}
	}
	return out
}
