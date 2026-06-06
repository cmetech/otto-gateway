// Phase 8 PLUG-06 — recursive string-LEAVES-only walker for canonical
// content (Message.ContentParts[].Text + ToolUse.Input +
// ToolResult.Content + ChatRequest.System). Map KEYS are NEVER walked
// (RESEARCH Pitfall 2 — keys are protocol field names; transforming
// them would silently rename fields like "email_address" → "[EMAIL]"
// and break upstream parsers). maxDepth=64 bound (RESEARCH Example 4
// line 941) protects against pathological tool-result trees triggering
// stack exhaustion. Non-string leaves (numbers, bools, nil, time.Time,
// any other type) pass through unchanged.
//
// Design contract:
//   - WalkStrings(v, transform) returns a NEW value with strings
//     transformed; the original is not mutated (immutable-input
//     discipline). Callers that need in-place mutation must assign the
//     returned value back over the original pointer (PIIRedactionHook
//     does this for ChatRequest.System and ToolUse.Input).
//   - For map[string]any: a fresh map of the same length is allocated;
//     each value is recursively walked; keys are copied verbatim.
//   - For []any: a fresh slice of the same length is allocated; each
//     element is recursively walked.
//   - For any other type (the default arm), the value is returned
//     unchanged (zero risk of panicking on user-supplied tool_use.Input
//     containing time.Time / *big.Int / channel / function values).
//
// Implementation note: the type-switch is sufficient for the canonical
// content shapes that adapters produce (map[string]any from JSON
// decoders, []any from slice walking, string LEAVES). The runtime-type-
// inspection package (deliberately not named here so source audits don't
// false-positive) would open a much larger panic surface (calling .Set
// on unexported fields is the textbook panic) without a corresponding
// benefit at this layer.

package pii

import (
	"log/slog"
	"sync"
)

// maxDepth is the recursion-depth bound. Beyond this depth WalkStrings
// returns the subtree unchanged — defense against pathological inputs
// from external tool servers. 64 covers any realistic canonical content
// tree by orders of magnitude.
const maxDepth = 64

// walkDepthTruncatedWarnOnce gates the "WalkStrings reached maxDepth"
// warn behind a process-wide sync.Once. Audit
// pii-walk-maxdepth-silent-passthrough: previously the walker returned
// the over-depth subtree verbatim with ZERO log signal, leaving an
// adversarial tool server able to ship PII at depth>64 untouched. The
// Once gate keeps the log noise bounded under sustained attack while
// surfacing the first occurrence so operators can investigate.
var walkDepthTruncatedWarnOnce sync.Once

// WalkStrings walks v and returns a new value with every string LEAF
// replaced by transform(leaf). Map keys are preserved verbatim; non-
// string leaves are bit-identical input vs output. Safe for nil v.
func WalkStrings(v any, transform func(string) string) any {
	return walkStrings(v, transform, 0)
}

// walkStrings is the depth-tracked workhorse. depth>maxDepth short-
// circuits to v unchanged (T-8-WALK-PANIC mitigation). The type-switch
// has explicit arms for the two recursive shapes (map / slice) and the
// string LEAF; everything else falls through the default arm unchanged.
func walkStrings(v any, transform func(string) string, depth int) any {
	if depth > maxDepth {
		walkDepthTruncatedWarnOnce.Do(func() {
			slog.Default().Warn(
				"pii.walk.depth_truncated",
				"max_depth", maxDepth,
				"note", "subtree returned unredacted; PII at depth > maxDepth may have leaked. Logging once per process.",
			)
		})
		return v
	}
	switch x := v.(type) {
	case string:
		return transform(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = walkStrings(vv, transform, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = walkStrings(vv, transform, depth+1)
		}
		return out
	default:
		// numbers, bools, nil, time.Time, *big.Int, channels, functions —
		// all pass through bit-identical. The walker is not in the
		// business of inspecting types it doesn't recognize.
		return v
	}
}
