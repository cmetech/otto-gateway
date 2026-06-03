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
// insensitively) within window bytes before matchStart or after matchEnd
// in text. nil/empty keywords returns true.
func hasContextWithin(text string, matchStart, matchEnd int, keywords []string, window int) bool {
	if len(keywords) == 0 {
		return true
	}
	lo := matchStart - window
	if lo < 0 {
		lo = 0
	}
	hi := matchEnd + window
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
