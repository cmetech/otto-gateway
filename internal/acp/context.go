package acp

import "context"

// denyBuiltinToolsKey is the unexported context key for the per-turn
// deny-builtin-tools signal (Track 3a).
type denyBuiltinToolsKey struct{}

// WithDenyBuiltinTools marks ctx so the acp permission handler DENIES kiro's
// built-in-tool session/request_permission for this turn (Track 3a). Set by
// engine.Run when the caller supplied tools. Absent/false => auto-grant.
func WithDenyBuiltinTools(ctx context.Context, deny bool) context.Context {
	return context.WithValue(ctx, denyBuiltinToolsKey{}, deny)
}

// DenyBuiltinTools reports whether this turn should deny kiro's built-in
// tools. Absent from ctx (or a non-bool value) reports false, i.e. the
// default auto-grant behavior.
func DenyBuiltinTools(ctx context.Context) bool {
	v, _ := ctx.Value(denyBuiltinToolsKey{}).(bool)
	return v
}
