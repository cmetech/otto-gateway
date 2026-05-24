// Package engine — Collect helper (D-01).
//
// Collect is the request-response inverse of Run's streaming output: it
// orchestrates Engine.Run, ranges the resulting Stream.Chunks() to
// completion, accumulates text into a strings.Builder, calls
// Stream.Result() for stop reason, and assembles a canonical.ChatResponse.
// PostHooks (Phase 8 seam — Codex H-5) run AFTER the response is
// assembled so they see the final assistant turn.
//
// PreHook short-circuit handling (Codex H-4): when Run returns a *Run
// whose response field is non-nil (a PreHook returned a non-nil
// response, e.g. a cached reply), Collect returns *response directly
// WITHOUT ranging stream.Chunks() and WITHOUT calling stream.Result()
// — the hook's payload is preserved verbatim. The prior design (zero
// chunks + chunk-assembly from empty text) silently dropped the
// hook's body; this is the fix.
package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"loop24-gateway/internal/canonical"
)

// Collect runs the request through the engine and aggregates the
// resulting stream into a canonical.ChatResponse. PostHooks run after
// the response is assembled; the first non-nil PostHook error aborts
// Collect with a wrapped error.
func (e *Engine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	run, err := e.Run(ctx, req)
	if err != nil {
		// e.Run already wraps; re-wrap once more so callers can
		// distinguish "collect path" from "run path" in upstream logs.
		return nil, fmt.Errorf("engine: collect: %w", err)
	}

	var resp *canonical.ChatResponse

	if run.response != nil {
		// Codex H-4: PreHook short-circuit. Preserve the hook's
		// response body verbatim — do NOT range Chunks (the
		// emptyStream is closed/empty anyway) and do NOT call
		// Result (it would also work but is meaningless here).
		resp = run.response
	} else {
		// Normal path: aggregate text from the stream.
		var sb strings.Builder
		for chunk := range run.stream.Chunks() {
			if chunk.Kind == canonical.ChunkKindText && chunk.Text != nil {
				sb.WriteString(chunk.Text.Content)
			}
			// ChunkKindThought / ChunkKindToolCall / ChunkKindPlan
			// are intentionally dropped in Phase 2. Phase 3.1 wires
			// Thought through Anthropic's "thinking" content block;
			// Phase 6 wires ToolCall via tool-result content parts.
		}
		final, rerr := run.stream.Result()
		if rerr != nil {
			return nil, fmt.Errorf("engine: collect result: %w", rerr)
		}
		resp = assembleChatResponse(req, sb.String(), final)
	}

	// Codex H-5: PostHook traversal happens HERE in Collect (not in
	// Run) so the hooks see the assembled or short-circuit response.
	// In-place mutation is allowed (resp is a pointer to the struct);
	// non-nil error aborts the collect.
	for _, h := range e.cfg.PostHooks {
		if hookErr := h.After(ctx, req, resp); hookErr != nil {
			return nil, fmt.Errorf("engine: posthook: %w", hookErr)
		}
	}

	return resp, nil
}

// assembleChatResponse builds a canonical.ChatResponse from the
// per-stream text aggregation plus the FinalResult's StopReason. The
// ID is time-based (matches the ID generator convention used by other
// Go-LLM gateways); Model echoes back the request's Model field;
// Usage is zero-valued in Phase 2 (kiro-cli does not yet report token
// counts via session/prompt).
func assembleChatResponse(req *canonical.ChatRequest, text string, final *canonical.FinalResult) *canonical.ChatResponse {
	stop := canonical.StopUnknown
	if final != nil {
		stop = final.StopReason
	}
	model := ""
	if req != nil {
		model = req.Model
	}
	return &canonical.ChatResponse{
		ID:    fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Model: model,
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: text},
			},
		},
		StopReason: stop,
		Usage:      canonical.Usage{},
	}
}
