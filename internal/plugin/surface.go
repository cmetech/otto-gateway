// Quick 260529-ll2 — surface.go: WithSurface / SurfaceFromContext ctx helpers.
//
// The per-surface adapter (openai / ollama / anthropic) stamps a short surface
// name onto ctx before invoking engine.Run / engine.Collect. ChatTraceHook
// reads this value via SurfaceFromContext when emitting NDJSON records so an
// operator inspecting chat-trace.log can correlate each pre_chain_in /
// post_chain_out pair to the wire-level surface (independent of the model
// id, which says nothing about which HTTP shape the client called).
//
// Key-collision safety: the ctx key reuses the unexported ctxKey struct
// declared in request_id.go (same package). Go type-identity rules
// (struct types are identified by package path + type name) make it
// impossible for an external package to construct a key value equal to
// surfaceCtxKey — so a malicious adapter or hook cannot spoof the
// surface field.

package plugin

import "context"

// surfaceCtxKey is the canonical context key for the surface stamp.
// Declared as a package-level var (not const — const-of-struct is not
// a Go thing) so all call sites share the same value-typed key.
//
// Re-uses the unexported ctxKey struct from request_id.go (same
// package). A separate `name` value ("surface") distinguishes this
// key from requestIDKey at lookup time.
var surfaceCtxKey = ctxKey{name: "surface"}

// WithSurface returns a child ctx carrying name under surfaceCtxKey.
// Adapters call this immediately after stampPluginCtx so request_id
// is already on ctx when SurfaceFromContext fires inside the
// ChatTraceHook Pre/Post emit.
//
// Empty-string name is permitted (records the call but produces an
// empty surface field downstream); callers should pass one of
// "openai" | "ollama" | "anthropic" in practice.
func WithSurface(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, surfaceCtxKey, name)
}

// SurfaceFromContext returns the surface stamp set by WithSurface,
// or "" + false when no stamp is present. The boolean lets callers
// distinguish "absent" from "empty string explicitly stamped"
// (mirrors the comma-ok idiom of map / type-assertion lookups).
//
// Safe to call from any layer; empty-and-ok=false fallback means
// callers can render the surface field unconditionally without
// nil-checking.
func SurfaceFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(surfaceCtxKey).(string)
	if !ok {
		return "", false
	}
	return v, true
}
