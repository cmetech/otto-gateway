package anthropic

// CollectAnthropicChat is the Anthropic-local aggregator (Option A1
// per plan 06-04). Per CONTEXT D-07, Anthropic renders kiro-native
// tool_call chunks as native tool_use content blocks — the
// surface-specific exception to the Phase 6 per-surface
// Message.ToolCalls population contract defined in 06-01:
//
//   - Generic engine.Collect does NOT populate Message.ToolCalls from
//     any chunk source (it aggregates ChunkKindToolCall into
//     `[tool: <name>]\n` narration text for Ollama/OpenAI's
//     non-streaming path — iteration-3 06-01 Task 2).
//   - Ollama and OpenAI populate Message.ToolCalls ONLY via
//     engine.CoerceToolCall.
//   - Anthropic (THIS function) is the D-07 exception that populates
//     Message.ToolCalls from kiro-native ChunkKindToolCall chunks via
//     this adapter-local aggregator. Anthropic's wire protocol has
//     tool_use blocks as native first-class elements, so the
//     adapter-local aggregator mirrors that shape rather than going
//     through the generic engine path.
//
// This file isolates the exception so the rest of the engine stays
// clean. Parity with engine.Collect for non-tool-call behavior is
// enforced by collect_test.go's parity test suite (iteration-3
// MEDIUM #5).
//
// Option B (engine-side switch — adding a per-surface branch to
// engine.Collect, or a new canonical flag like
// `IncludeToolCallChunks`) was considered and rejected because it
// leaks adapter concerns into the engine and either expands the
// canonical type surface or branches engine code on adapter identity
// — both violate the per-surface contract's intent.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"otto-gateway/internal/canonical"
)

// CollectAnthropicChat runs the request through eng.Run and aggregates
// the resulting stream into a canonical.ChatResponse. Mirrors
// engine.Collect's aggregation loop for non-tool-call behavior (the
// per-surface contract is enforced by the parity test suite); the one
// intentional divergence is the ChunkKindToolCall branch, which
// appends ContentKindToolUse parts to Message.Content AND populates
// Message.ToolCalls — that's the D-07 Anthropic exception.
//
// On any error from eng.Run or the stream's Result(), the error is
// wrapped with the "anthropic: collect" prefix so callers can
// distinguish the Anthropic-local aggregation path from the generic
// engine.Collect path in upstream logs.
func CollectAnthropicChat(ctx context.Context, eng Engine, req *canonical.ChatRequest, streamIdle time.Duration) (*canonical.ChatResponse, error) {
	run, err := eng.Run(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: collect: %w", err)
	}

	// Phase 8 SC1 short-circuit: if a PreHook (e.g., AuthHook) returned
	// a synthesized response, return it verbatim. The chunk-based
	// aggregator below would otherwise see an empty stream and drop
	// the hook's user-facing message — handleMessages then detects
	// StopReason == canonical.StopError and renders the Anthropic
	// error envelope.
	if shortCircuit := run.ShortCircuitResponse(); shortCircuit != nil {
		// Stop the watchdog for parity (it was nil on a short-circuit
		// Run, but the call is nil-safe).
		if stop := run.StopWatchdog(); stop != nil {
			stop()
		}
		// Quick 260530-df2: fire PostHooks on the short-circuit
		// response too — mirrors engine.Collect at collect.go:114-122
		// (Codex H-5). Without this, an AuthHook-synthesized 401 would
		// never reach LoggingHook.After / ChatTraceHook.After. The
		// error IS propagated (non-streaming path holds the bytes —
		// see the normal-tail comment block below for the rationale).
		if pErr := eng.RunPostHooks(ctx, req, shortCircuit); pErr != nil {
			return nil, fmt.Errorf("anthropic: collect (short-circuit): %w", pErr)
		}
		return shortCircuit, nil
	}

	var (
		sb        strings.Builder
		thoughtSB strings.Builder
		toolParts []canonical.ContentPart
		toolCalls []canonical.ToolCall
	)

	// Quick 260531-ruv — adapter-local idle watchdog. TRST-04 forbids
	// importing internal/engine here, so the loop replicates the
	// RangeChunksWithIdleTimeout semantics inline (drain-safe Stop/
	// Reset on each chunk, nil idleC arm when disabled). The returned
	// error wraps canonical.ErrStreamIdleTimeout so the handler can
	// errors.Is-check the sentinel and render 504.
	chunks := run.Stream().Chunks()
	var idleTimer *time.Timer
	var idleC <-chan time.Time
	if streamIdle > 0 {
		idleTimer = time.NewTimer(streamIdle)
		defer func() {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
		}()
		idleC = idleTimer.C
	}
	rangeLoop := func() error {
		for {
			select {
			case <-ctx.Done():
				return fmt.Errorf("anthropic: collect ctx: %w", ctx.Err())
			case <-idleC:
				return fmt.Errorf("anthropic: collect %w", canonical.ErrStreamIdleTimeout)
			case chunk, ok := <-chunks:
				if !ok {
					return nil
				}
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
					// D-07 Anthropic exception: kiro-native tool_call
					// chunks produce ContentKindToolUse parts + populate
					// Message.ToolCalls. The text/thinking builders are
					// NOT touched — no `[tool: <name>]\n` narration.
					if chunk.ToolCall != nil {
						toolParts = append(toolParts, canonical.ContentPart{
							Kind: canonical.ContentKindToolUse,
							ToolUse: &canonical.ToolUsePart{
								ID:    chunk.ToolCall.ID,
								Name:  chunk.ToolCall.Name,
								Input: chunk.ToolCall.Args,
							},
						})
						toolCalls = append(toolCalls, canonical.ToolCall{
							ID:        chunk.ToolCall.ID,
							Name:      chunk.ToolCall.Name,
							Arguments: chunk.ToolCall.Args,
						})
					}
				}
				// ChunkKindPlan still drops (no Phase 6 work; mirrors
				// engine.Collect).
				if idleTimer != nil {
					if !idleTimer.Stop() {
						select {
						case <-idleTimer.C:
						default:
						}
					}
					idleTimer.Reset(streamIdle)
				}
			}
		}
	}
	if loopErr := rangeLoop(); loopErr != nil {
		if errors.Is(loopErr, canonical.ErrStreamIdleTimeout) {
			// WARN-log with the canonical attr set so operators can
			// correlate the timeout against pool slot releases.
			// Logger access lives on the handler; this adapter file
			// stays log-free to preserve its lean test surface — the
			// 504 path in handlers.go logs the marker before writing
			// the wire response.
			return nil, loopErr
		}
		return nil, loopErr
	}

	final, rerr := run.Stream().Result()
	if rerr != nil {
		return nil, fmt.Errorf("anthropic: collect result: %w", rerr)
	}

	// D-06 teardown: stop the watchdog after natural stream completion
	// to prevent a spurious session/cancel from the AfterFunc
	// goroutine. Mirrors engine.Collect's discipline.
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}

	resp := assembleAnthropicChatResponse(req, sb.String(), thoughtSB.String(), toolParts, toolCalls, final)
	// Quick 260530-df2 — non-streaming Anthropic PostHook gap fix.
	// CollectAnthropicChat is the D-07 exception path that bypassed
	// engine.Collect's PostHook traversal. Wiring RunPostHooks here
	// closes the gap so LoggingHook.After + ChatTraceHook.After fire
	// on the non-streaming Anthropic surface just like every other
	// surface.
	//
	// DIVERGENCE from the streaming WARN-and-swallow contract: the
	// non-streaming path holds the response bytes — they have NOT been
	// written to the wire yet, so a PostHook error CAN be propagated
	// to the caller (handlers.go) which then renders a 500. This
	// mirrors engine.Collect at collect.go:118-122 verbatim.
	if pErr := eng.RunPostHooks(ctx, req, resp); pErr != nil {
		return nil, fmt.Errorf("anthropic: collect: %w", pErr)
	}
	return resp, nil
}

// assembleAnthropicChatResponse builds a canonical.ChatResponse from
// the aggregated text/thinking/tool_use parts. The text part is
// ALWAYS present at Content[0] (may be empty when only tool_use
// chunks arrived — mirrors engine.Collect's Phase 3.1 D-02 contract
// so the rest of the canonical-render path stays stable). Thinking
// appends after text when present; tool_use parts append after.
//
// ToolCalls is populated separately on Message — the Anthropic SDK
// non-streaming response path reads either form depending on what
// the renderer chooses to surface.
func assembleAnthropicChatResponse(
	req *canonical.ChatRequest,
	text, thinking string,
	toolParts []canonical.ContentPart,
	toolCalls []canonical.ToolCall,
	final *canonical.FinalResult,
) *canonical.ChatResponse {
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
	content = append(content, toolParts...)

	return &canonical.ChatResponse{
		Model: model,
		Message: canonical.Message{
			Role:      canonical.RoleAssistant,
			Content:   content,
			ToolCalls: toolCalls,
		},
		StopReason: stop,
	}
}
