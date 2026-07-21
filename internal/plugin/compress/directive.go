// internal/plugin/compress/directive.go

package compress

import "regexp"

// MetadataKey is the canonical.ChatRequest.Metadata key under which the
// adapters store the parsed model-suffix directive (a bool: true for
// "+compress", false for "-compress"). Metadata is the documented
// free-form per-request seam (canonical/chat.go) — the suffix originates
// in the request BODY (the model name), so it rides the request, not ctx.
const MetadataKey = "compress"

// directiveRe matches a trailing "+compress" / "-compress" directive on a
// model name (case-insensitive). Mirrors the Node gateway's
// /([+-])(skills|compress)$/i — only the compress directive exists here.
var directiveRe = regexp.MustCompile(`(?i)([+-])compress$`)

// SplitCompressDirective strips a trailing +compress/-compress directive
// from a model name. Returns the base model name and a nil directive when
// no suffix is present. LangFlow can select a model name but cannot send
// HTTP headers, so the suffix is its only per-request compression lever.
//
// A directive that would leave an EMPTY base ("+compress" alone) is not a
// directive — the input is returned verbatim as a model name. Stripping
// to "" would slip past surface nonempty-model validation (anthropic
// validates wire.Model BEFORE conversion, handlers.go:113-117) and the
// engine treats "" as do-not-SetModel (engine.go:256-263), silently
// changing semantics instead of erroring.
//
// KNOWN COLLISION (accepted, Node-syntax parity — /([+-])compress$/i): a
// real model id ending in "-compress" is parsed as a disable directive.
// No escape syntax exists; documented in docs/operating.md.
//
// Adapters MUST call this before any surface-specific model normalization
// (e.g. anthropic's normalizeClaudeModelID) — the anthropic hyphen-version
// regex is $-anchored and would not fire on a suffixed name.
func SplitCompressDirective(model string) (string, *bool) {
	m := directiveRe.FindStringSubmatch(model)
	if m == nil {
		return model, nil
	}
	base := model[:len(model)-len(m[0])]
	if base == "" {
		return model, nil
	}
	on := m[1] == "+"
	return base, &on
}
