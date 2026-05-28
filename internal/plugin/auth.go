// Phase 8 PLUG-03 — AuthHook refactors bearer-token validation out of
// HTTP middleware (internal/auth/bearer.go) into a canonical-typed
// PreHook.
//
// On bad token, the hook short-circuits via a canonical
// *ChatResponse envelope that each surface adapter renders in its
// native error shape:
//   - Anthropic: {"type":"error", "error":{...}}
//   - OpenAI:    {"error":{...}}
//   - Ollama:    {"error":"..."}
//
// The same crypto/subtle.ConstantTimeCompare primitive used by
// internal/auth/bearer.go:51 preserves the constant-time guarantee
// (T-8-AUTH mitigation). The early-exit on match across the Tokens
// slice leaks "how many tokens are configured" but NOT token bytes;
// this is the same accepted tradeoff documented at bearer.go:48-50
// and 08-RESEARCH Pitfall 3.
//
// Migration boundary (08-PATTERNS Pattern F): this hook does NOT yet
// replace the auth.Bearer chi middleware — that removal is staged for
// slice 5 after main.go wires AuthHook into the chain. Slice 2 ships
// the hook + the canonical-ctx credential bridge + the per-surface
// adapter ctx-stamping (Task 4); slice 5 removes the middleware in a
// single commit once the chain wiring lands.

package plugin

import (
	"context"
	"crypto/subtle"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// AuthHook validates the bearer credential stamped onto ctx by the
// per-surface adapter handler (Task 4). Empty / nil Tokens means auth
// is disabled (Node parity, mirroring bearer.go:35-38) — the hook
// becomes a passthrough.
//
// Tokens is the configured valid-token set; populated by main.go from
// the AUTH_TOKEN env var (slice 5 wiring).
type AuthHook struct {
	Tokens []string
}

// Compile-time interface check — must satisfy engine.PreHook so the
// hardcoded chain literal in main.go compiles.
var _ engine.PreHook = (*AuthHook)(nil)

// Name reports the hook's filter-discovery name for chain.Filter
// (08-PATTERNS Pattern A — explicit Name() over reflect for
// caller-stable API). main.go's ENABLED_HOOKS allowlist references
// this exact string.
func (h *AuthHook) Name() string { return "AuthHook" }

// Describe publishes the hook's safe-to-publish config for
// /health/hooks (OBSV-04). Kind is "Pre" (this hook never implements
// PostHook). Config carries `token_count` so operators can confirm
// "is auth enabled?" + "how many tokens?" at a glance — NEVER the
// tokens themselves (T-8-LEAK per 08-RESEARCH Pitfall 9). The
// TestAuthHook_Describe_NoSecrets test walks the returned map and
// fails any key whose lowercased name contains "token" other than
// the allowed "token_count".
func (h *AuthHook) Describe() (kind string, config map[string]any) {
	return "Pre", map[string]any{"token_count": len(h.Tokens)}
}

// Before is the PreHook entry point. Algorithm:
//
//  1. Empty / nil Tokens → return (nil, nil) (Node parity — auth
//     disabled when AUTH_TOKEN unset).
//  2. Read the credential from ctx via
//     canonical.BearerTokenFromContext. The stamp is the adapter→hook
//     bridge established by Task 1; Task 4 wires it from the wire
//     request's Authorization / x-api-key header.
//     - !ok (no stamp) → short-circuit (adapter forgot to stamp,
//       defensive default-deny).
//     - ok && provided == "" → short-circuit (header was missing in
//       the wire request — the adapter stamped an empty string to
//       signal "credential observed as absent").
//  3. Loop the Tokens slice. Use subtle.ConstantTimeCompare against
//     each entry. NEVER use `==` on token bytes (T-8-AUTH timing
//     side-channel mitigation; bearer.go:51 precedent).
//  4. Match → return (nil, nil) (passthrough; engine.Run proceeds).
//  5. No match → return short-circuit envelope.
//
// Short-circuit envelopes carry StopReason == canonical.StopError and
// a single ContentKindText part holding the user-facing message —
// engine.Collect preserves the response verbatim (Codex H-4) and the
// per-surface adapter renders the native error shape.
//
// Returns (nil, error) is reserved for genuine plumbing errors; the
// current implementation has no such path (auth failure is a
// short-circuit, NOT an error).
func (h *AuthHook) Before(ctx context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	if len(h.Tokens) == 0 {
		return nil, nil // auth disabled — Node parity (bearer.go:35-38)
	}

	provided, ok := canonical.BearerTokenFromContext(ctx)
	if !ok || provided == "" {
		return synthesizeAuthError("Invalid or missing API key"), nil
	}

	providedBytes := []byte(provided)
	for _, valid := range h.Tokens {
		// Early-exit on match leaks "how many tokens are configured"
		// but NOT token bytes (08-RESEARCH Pitfall 3; bearer.go:48-50
		// documents the same accepted tradeoff).
		if subtle.ConstantTimeCompare(providedBytes, []byte(valid)) == 1 {
			return nil, nil
		}
	}

	return synthesizeAuthError("Invalid or missing API key"), nil
}

// synthesizeAuthError builds the canonical short-circuit envelope.
// Shape mirrors the chat-response shape adapters expect when rendering
// a "normal" assistant turn — engine.Collect preserves it verbatim
// (Codex H-4), and per-surface error renderers detect
// StopReason == canonical.StopError + Message.Content[0].Text and
// translate to the native error envelope.
//
// `canonical.StopError` was added in Phase 8 Plan 08-02 Task 3 as the
// smallest necessary canonical-type extension for the AuthHook
// short-circuit shape (08-PLAN.md Task 3 action block); slice 5 will
// be the first consumer beyond AuthHook (per-surface error rendering).
func synthesizeAuthError(message string) *canonical.ChatResponse {
	return &canonical.ChatResponse{
		StopReason: canonical.StopError,
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: message},
			},
		},
	}
}
