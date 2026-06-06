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
//
// T-5b (PII encrypt streaming gap): the aggregation half of Collect is
// extracted into Engine.CollectFromRun so adapter handlers can re-route
// a streaming request through the aggregated path AFTER eng.Run has
// already returned (e.g., when the PII encrypt Pre hook flipped
// req.Stream=false in its Before method). Collect itself now calls
// Run + CollectFromRun internally and is byte-identical for existing
// consumers.
package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"otto-gateway/internal/canonical"
)

// Collect runs the request through the engine and aggregates the
// resulting stream into a canonical.ChatResponse. PostHooks run after
// the response is assembled; the first non-nil PostHook error aborts
// Collect with a wrapped error.
//
// Refactored under T-5b to delegate aggregation to CollectFromRun.
// Behavior is byte-identical to the previous in-line implementation
// for every existing caller.
func (e *Engine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	run, err := e.Run(ctx, req)
	if err != nil {
		// e.Run already wraps; re-wrap once more so callers can
		// distinguish "collect path" from "run path" in upstream logs.
		return nil, fmt.Errorf("engine: collect: %w", err)
	}
	return e.CollectFromRun(ctx, run, req)
}

// CollectFromRun performs the aggregation half of Collect against an
// existing *Run handle (without re-running). T-5b seam: adapter
// streaming handlers call this after eng.Run when a Pre hook
// (typically PII encrypt) has flipped req.Stream=false post-Run, so
// the same ACP session can be drained into a non-streaming JSON
// response shape instead of leaking ciphertext bytes through the SSE
// emitter ahead of the PII decrypt PostHook.
//
// Aggregation semantics match the pre-T-5b Collect body verbatim:
//
//   - PreHook short-circuit: run.response != nil → return that
//     response directly (Codex H-4); PostHooks still run on it (H-5).
//   - Normal path: range run.stream.Chunks() via
//     RangeChunksWithIdleTimeout, accumulate ChunkKindText and
//     ChunkKindThought into separate builders, render ChunkKindToolCall
//     as `[tool: <name>]\n` narration text (D-03/D-05/D-07 contract —
//     Message.ToolCalls remains untouched here; per-surface coerce is
//     the adapter's concern), call run.stream.Result() for the
//     FinalResult, stop the watchdog, assemble via assembleChatResponse,
//     run PostHooks.
//
// Idle timeout: when e.cfg.StreamIdleTimeout > 0, the chunk loop
// returns canonical.ErrStreamIdleTimeout (wrapped) on a stalled
// stream. Adapter handlers errors.Is-check it and render a 504 on the
// non-streaming JSON path that consumes this method.
//
// PostHook errors propagate as "engine: posthook: <inner>". Same wrap
// shape used by Collect and RunPostHooks so log filters keyed on the
// prefix continue to match.
func (e *Engine) CollectFromRun(ctx context.Context, run *Run, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	if run == nil {
		return nil, fmt.Errorf("engine: collect from run: run is nil")
	}

	var resp *canonical.ChatResponse

	if run.response != nil {
		// Codex H-4: PreHook short-circuit. Preserve the hook's
		// response body verbatim — do NOT range Chunks (the
		// emptyStream is closed/empty anyway) and do NOT call
		// Result (it would also work but is meaningless here).
		resp = run.response
	} else {
		// Normal path: aggregate text AND thoughts from the stream.
		// Phase 3.1 D-02 activates the dormant ContentKindThinking
		// seam — thoughts that Phase 2 dropped now flow into a
		// second content part so the Anthropic adapter can render
		// `{type:"thinking",thinking:"..."}` blocks (ANTH-07
		// foundation). Two builders, one switch — text and thoughts
		// stay independent so order-of-arrival doesn't matter.
		//
		// Quick 260531-ruv: the chunk-range loop now routes through
		// RangeChunksWithIdleTimeout. When e.cfg.StreamIdleTimeout > 0
		// and no chunk arrives within that window, the helper returns
		// ErrStreamIdleTimeout (wrapped) — adapter handlers errors.Is-
		// check it and render a 504 on the non-streaming paths that
		// consume this function (Ollama, OpenAI). When the timeout is 0,
		// the helper degrades to a bare ctx-aware range with zero timer
		// cost.
		//
		// Per-surface Message.ToolCalls contract (D-03/D-05/D-07):
		//   - Generic engine.Collect (this function) does NOT populate
		//     Message.ToolCalls from any chunk source.
		//   - Ollama and OpenAI populate Message.ToolCalls ONLY via
		//     engine.CoerceToolCall (the coerce-from-text path — D-05),
		//     invoked by the adapter handlers AFTER this function
		//     returns.
		//   - Anthropic (D-07 exception) populates Message.ToolCalls via
		//     its adapter-local CollectAnthropicChat from kiro-native
		//     ChunkKindToolCall chunks. That adapter uses engine.Run +
		//     its own aggregator and bypasses this function.
		var sb, thoughtSB strings.Builder
		onChunk := func(chunk canonical.Chunk) error {
			switch chunk.Kind {
			case canonical.ChunkKindText:
				if chunk.Text != nil {
					sb.WriteString(chunk.Text.Content)
				}
			case canonical.ChunkKindThought:
				if chunk.Thought != nil {
					thoughtSB.WriteString(chunk.Thought.Content)
				}
			case canonical.ChunkKindToolCall:
				// ChunkKindToolCall renders as `[tool: <name>]\n`
				// narration text. ChunkKindPlan still drops.
				name := "unknown"
				if chunk.ToolCall != nil && chunk.ToolCall.Name != "" {
					name = chunk.ToolCall.Name
				}
				sb.WriteString("[tool: ")
				sb.WriteString(name)
				sb.WriteString("]\n")
			}
			return nil
		}
		if rangeErr := RangeChunksWithIdleTimeout(ctx, run.stream, e.cfg.StreamIdleTimeout, onChunk); rangeErr != nil {
			if errors.Is(rangeErr, ErrStreamIdleTimeout) {
				e.cfg.Logger.Warn(
					"stream.idle_timeout",
					"surface", "engine.collect",
					"session_id", run.sessionID,
					"elapsed_ms", e.cfg.StreamIdleTimeout.Milliseconds(),
				)
			}
			return nil, fmt.Errorf("engine: collect: %w", rangeErr)
		}
		final, rerr := run.stream.Result()
		if rerr != nil {
			return nil, fmt.Errorf("engine: collect result: %w", rerr)
		}
		// D-06 teardown: stop() prevents the AfterFunc goroutine from firing
		// session/cancel after the stream closed naturally. stop() returning false
		// is expected if ctx was already canceled — Cancel is idempotent
		// (RESEARCH.md Pitfall 4).
		if stop := run.StopWatchdog(); stop != nil {
			stop()
		}
		resp = assembleChatResponse(req, sb.String(), thoughtSB.String(), final)
	}

	// Codex H-5: PostHook traversal happens HERE in Collect (not in
	// Run) so the hooks see the assembled or short-circuit response.
	// In-place mutation is allowed (resp is a pointer to the struct);
	// non-nil error aborts the collect.
	for _, h := range e.cfg.PostHooks {
		if hookErr := e.callPostHookSafe(ctx, h, req, resp); hookErr != nil {
			return nil, fmt.Errorf("engine: posthook: %w", hookErr)
		}
	}

	return resp, nil
}

// assembleChatResponse builds a canonical.ChatResponse from the
// per-stream text + thinking aggregations plus the FinalResult's
// StopReason. The ID is time-based (matches the ID generator
// convention used by other Go-LLM gateways); Model echoes back the
// request's Model field; Usage is zero-valued in Phase 2 (kiro-cli
// does not yet report token counts via session/prompt).
//
// Phase 3.1 D-02 — content shape:
//   - The text part is ALWAYS present at Content[0] (may be empty
//     string when the stream produced only thoughts). This keeps the
//     Phase 2 Ollama joinTextContent path stable.
//   - The thinking part is appended at Content[1] ONLY when
//     `thinking != ""`. Phase 2 Ollama tests that never see a
//     ChunkKindThought continue to assert len(Content) == 1.
//   - The thinking part renders into the Anthropic adapter's
//     `{type:"thinking",thinking:"..."}` content block (ANTH-07
//     non-streaming) and into the Ollama
//     `ollamaChatResponseMessage.Thinking` field via the existing
//     joinThinkingContent helper (the omitempty JSON tag drops the
//     field for thought-free responses).
func assembleChatResponse(req *canonical.ChatRequest, text, thinking string, final *canonical.FinalResult) *canonical.ChatResponse {
	stop := canonical.StopUnknown
	if final != nil {
		stop = final.StopReason
	}
	model := ""
	if req != nil {
		model = req.Model
	}
	content := []canonical.ContentPart{
		{Kind: canonical.ContentKindText, Text: text},
	}
	if thinking != "" {
		content = append(content, canonical.ContentPart{
			Kind: canonical.ContentKindThinking,
			Text: thinking,
		})
	}
	return &canonical.ChatResponse{
		ID:    fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Model: model,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: content,
		},
		StopReason: stop,
		Usage:      canonical.Usage{},
	}
}
