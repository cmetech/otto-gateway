// Package engine — Phase 8 hook-chain seam (D-04).
//
// PreHook + PostHook are EMPTY in Phase 2 — Config.PreHooks and
// Config.PostHooks default to nil and Run / Collect treat that as a
// no-op. Phase 8 will register stdlib-equivalent impls (RequestIDHook,
// AuthHook, LoggingHook) against these signatures without touching
// engine.Run or engine.Collect.
package engine

import (
	"context"

	"otto-gateway/internal/canonical"
)

// PreHook is invoked by Engine.Run BEFORE any ACP traffic. Implementations
// may either:
//
//   - return (nil, nil) to continue the chain unchanged;
//   - return (nil, err) to abort the request with err;
//   - return (resp, nil) where resp != nil to SHORT-CIRCUIT the request:
//     ACP is never engaged, and the supplied response is preserved
//     verbatim by Collect (Codex H-4). Use cases include cached
//     responses, rate-limit replies, and admission-controlled rejections
//     synthesized as a normal-shaped ChatResponse.
//
// PreHooks see the canonical request after the HTTP-layer adapter has
// already parsed and validated it. Mutations to req are visible to
// later PreHooks and to the engine.
type PreHook interface {
	Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
}

// PostHook is invoked by Engine.Collect AFTER the assistant
// *canonical.ChatResponse has been assembled — either from chunk
// aggregation (normal path) or from a PreHook short-circuit (Codex H-4).
// Implementations may mutate fields on resp in place (e.g.,
// RequestIDHook adds an ID; LoggingHook records timestamps) since resp
// is a pointer to the underlying struct.
//
// Returning a non-nil error propagates as the Collect error wrapped via
// "engine: posthook: ...". The earlier interface returned a
// (*canonical.ChatResponse, error) pair allowing replacement; per
// Codex H-5 in-place mutation is preferred so the engine's flow stays
// simple. If a PostHook needs to "replace" the response, it copies the
// new contents over *resp.
type PostHook interface {
	After(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error
}
