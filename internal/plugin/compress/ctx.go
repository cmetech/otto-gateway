// internal/plugin/compress/ctx.go
// WithHeaderDirective / HeaderDirectiveFromContext — the X-Compression
// header seam. The per-surface adapters stamp the header value onto ctx in
// stampPluginCtx (mirroring plugin.WithRequestID / pii.WithSummary);
// CompressionHook.Before reads it. Header wins over the model-suffix
// directive and over the process-wide COMPRESSION_ENABLED default.
//
// Key-collision safety: unexported struct key — an external package cannot
// construct an equal key value (same argument as plugin/surface.go).

package compress

import (
	"context"
	"strings"
)

type ctxKey struct{ name string }

var headerDirectiveKey = ctxKey{name: "compress-header"}

// WithHeaderDirective returns a child ctx carrying the X-Compression
// header decision. Adapters call it only when the header is PRESENT —
// absence must fall through to the suffix/env default (tri-state).
func WithHeaderDirective(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, headerDirectiveKey, on)
}

// HeaderDirectiveFromContext returns the stamped header decision.
// ok=false means the header was absent (comma-ok idiom).
func HeaderDirectiveFromContext(ctx context.Context) (bool, bool) {
	v, ok := ctx.Value(headerDirectiveKey).(bool)
	return v, ok
}

// ParseHeaderValue interprets an X-Compression header value as a strict
// tri-state: "1"/"true"/"on" → enable, "0"/"false"/"off" → disable
// (case-insensitive, whitespace-trimmed), anything else → ok=false,
// meaning IGNORE the header and fall through to the suffix/env default.
// NOTE: this vocabulary is deliberately WIDER than config getEnvBool
// (which accepts only 1/true/0/false) — "on"/"off" work in the header
// but NOT in COMPRESSION_ENABLED. Anything-nonzero-means-on would let
// "false"/"off"/"00" silently enable destructive compression.
func ParseHeaderValue(v string) (on bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on":
		return true, true
	case "0", "false", "off":
		return false, true
	}
	return false, false
}
