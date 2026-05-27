package canonical

import "strings"

// JoinTextParts concatenates the Text fields of every ContentPart whose
// Kind == ContentKindText. Non-text parts (images, tool-use, tool-result,
// thinking, etc.) are skipped. Empty result when the message has no text
// parts.
//
// WR-05 (Phase 6 review): hoisted from internal/adapter/{ollama,openai}/render.go
// and internal/engine/build_acp.go to consolidate the three identical
// implementations. Adapter callers previously used `out += p.Text`
// (O(n²) string concatenation); this canonical implementation uses
// strings.Builder for O(n) total work. The function is on the canonical
// package because it operates purely on canonical.ContentPart and is
// reused by both adapter and engine layers — the TRST-04 layering rule
// allows every consumer of canonical to depend on this helper.
func JoinTextParts(parts []ContentPart) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == ContentKindText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// JoinThinkingParts concatenates the Text fields of every ContentPart
// whose Kind == ContentKindThinking. Mirrors JoinTextParts exactly,
// gated on a different Kind discriminator. Used by the Ollama adapter
// to populate `thinking` on /api/chat responses (D-02 follow-on) and
// by the engine to preserve inbound thinking blocks into the
// [Reasoning] ACP wire section (Phase 3.1 D-11).
func JoinThinkingParts(parts []ContentPart) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == ContentKindThinking {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
