// Contextual keyword matching for context-anchored recognizers (IMEI,
// IMSI, MSISDN, SITE). Go regexp has no variable-width lookbehind, so
// context is checked programmatically: do any of the keywords appear,
// case-insensitively, within a ±window byte range surrounding a regex
// match? Window is bytes not runes for simplicity; all our context
// keywords are ASCII so this is equivalent in practice.
//
// nil/empty keywords short-circuits to true so the same code path
// handles both context-free and context-required recognizers.

package pii

import "strings"

// defaultContextWindow is the default ±byte radius around a regex match
// in which one of the recognizer's ContextKeywords must appear. 50 bytes
// ≈ 8–10 English words on either side, matching the conventional
// presidio "this is an IMEI: 49015…" anchoring style.
const defaultContextWindow = 50

// hasContextWithin reports whether any of keywords appears (case-
// insensitively) within defaultContextWindow bytes before matchStart or
// after matchEnd in text. nil/empty keywords returns true.
//
// Note: the window was previously a parameter but always received
// defaultContextWindow at every call site (production + tests). Dropped
// to satisfy unparam (Phase 10 Wave 2); if a recognizer needs a custom
// window in the future, expose it via a sibling helper rather than
// re-introducing the always-defaultContextWindow parameter here.
func hasContextWithin(text string, matchStart, matchEnd int, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	lo := matchStart - defaultContextWindow
	if lo < 0 {
		lo = 0
	}
	hi := matchEnd + defaultContextWindow
	if hi > len(text) {
		hi = len(text)
	}
	hay := strings.ToLower(text[lo:hi])
	for _, k := range keywords {
		if k == "" {
			continue
		}
		if strings.Contains(hay, strings.ToLower(k)) {
			return true
		}
	}
	return false
}
