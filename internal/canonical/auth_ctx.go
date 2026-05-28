// Phase 8 PLUG-03 — canonical ctx helpers for the adapter→AuthHook
// bearer-credential bridge.
//
// Defined in `canonical` (NOT `plugin`) because BOTH the adapter packages
// AND the plugin package must read from it; TRST-04 forbids adapter→plugin
// imports. Per 08-RESEARCH.md Open Question 2 (disposition: Option a —
// "adapter handler extracts the header, stamps onto ctx via a typed key")
// and 08-PATTERNS.md Pattern C ("Private struct ctx key + accessor pair").
//
// The credential travels on `ctx` instead of on `canonical.ChatRequest`
// because (1) auth is not a chat-payload field — it has no semantic
// place on the canonical request type, and (2) keeping it off the
// struct preserves the no-secrets invariant for logging (any Marshal of
// ChatRequest stays secret-free).
//
// `WithBearerToken` distinguishes three states for AuthHook:
//   - stamp absent          (ok=false, token="") — no adapter set the value
//   - stamp present, empty  (ok=true,  token="") — adapter explicitly stamped
//     an empty header (header missing in the wire request)
//   - stamp present, value  (ok=true,  token="s3cret") — credential to validate
//
// AuthHook treats both "absent" and "present-but-empty" as auth failure
// when its Tokens slice is non-empty; the distinction MAY matter for
// future hooks that need to react differently to "adapter forgot to
// stamp" vs "client sent no credential".

package canonical

import "context"

// bearerKey is the unexported, struct-typed context key used to stamp
// the bearer credential onto ctx. T-8-AUTH-4 mitigation (and mirror of
// the T-8-RID-1 pattern from plugin/request_id.go) — Go type-identity
// rules guarantee no other package can construct a key value that
// compares equal to this one, so a malicious adapter / hook cannot
// inject a spoofed credential.
//
// The single `name` field is for debugging only (visible in ctx
// String() output via reflect); equality is by key VALUE (not by name
// string), and the value is package-scoped because the type is
// unexported.
type bearerKey struct{ name string }

// bearerTokenKey is the single canonical key value used by
// WithBearerToken + BearerTokenFromContext. Declared as a package-level
// var (not const — const-of-struct is not a Go thing) so all call
// sites share the same value-typed key.
var bearerTokenKey = bearerKey{name: "bearer-token"}

// WithBearerToken returns a child ctx carrying token under the
// unexported bearerTokenKey. Adapter HTTP handlers call this BEFORE
// invoking engine.Run so the canonical-layer AuthHook (Phase 8 PLUG-03)
// can validate the credential without depending on the per-surface
// header shape (Ollama / OpenAI use Authorization: Bearer; Anthropic
// uses x-api-key OR Authorization: Bearer per Phase 3.1 D-15).
//
// Stamping an empty string ("") is meaningful: it signals "adapter
// observed a missing credential" — distinct from "adapter did not run
// auth resolution at all" (no stamp). BearerTokenFromContext returns
// (token="", ok=true) for the empty-stamp case and (token="", ok=false)
// for the no-stamp case.
func WithBearerToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, bearerTokenKey, token)
}

// BearerTokenFromContext returns the bearer credential stamped on ctx
// by WithBearerToken plus a presence boolean. The boolean is true iff
// SOMETHING was stamped (even an empty string); it is false when no
// adapter ever ran WithBearerToken on this ctx chain.
//
// Safe to call from any layer (adapter, engine, hook); when nothing
// was stamped, returns ("", false) — callers that don't care about
// presence can do `tok, _ := BearerTokenFromContext(ctx)`.
func BearerTokenFromContext(ctx context.Context) (token string, ok bool) {
	v, ok := ctx.Value(bearerTokenKey).(string)
	if !ok {
		return "", false
	}
	return v, true
}
