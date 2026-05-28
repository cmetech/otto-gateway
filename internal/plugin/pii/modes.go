// Phase 8 PLUG-06 D-05 — redaction mode dispatch. Four modes:
//
//	replace : "<ENTITY>" or counter-suffixed "<ENTITY_N>" when counter > 0.
//	mask    : partial obfuscation (algorithm documented on maskValue below).
//	hash    : HMAC-SHA256 of canonical(value) keyed by PII_HASH_KEY,
//	          truncated to 8 hex chars. Output shape "<ENTITY:h-XXXXXXXX>".
//	drop    : empty string.
//
// HMAC NOT raw SHA256 — RESEARCH §Don't-Hand-Roll table + Pitfall 6
// prevent length-extension attack vectors when the hash tag flows to
// log files. Canonical form (lowercase + trim) is applied BEFORE the
// HMAC so 'Corey@CMETECH.io', '  corey@cmetech.io  ', and
// 'corey@cmetech.io' all produce the same tag (D-05 + key-rotation
// correlation tool — rotating PII_HASH_KEY invalidates the correlation
// surface globally without touching application code).
//
// T-8-HASH mitigation lives in hashTag below: empty key returns a
// sentinel containing "UNKEYED" (NEVER a fixed-key fallback HMAC) and
// emits a slog warning. Slice 5 boot validation catches the empty-key
// case earlier; slice-4 defensive behavior here is belt-and-suspenders.

package pii

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
)

// hashTagLen is the truncated hex length of the HMAC-SHA256 tag in the
// hash-mode output. 8 hex chars = 32 bits of entropy per D-05 default —
// enough to disambiguate hundreds of distinct values per logs file while
// keeping the redacted token compact for slog output.
const hashTagLen = 8

// UnkeyedHashSentinel is the literal substring embedded in the hash-mode
// output when PII_HASH_KEY is empty. Slice 5 boot validation should
// prevent reaching this; the sentinel makes the defensive fallback
// observable in logs and unambiguously distinguishable from a real
// HMAC tag.
const UnkeyedHashSentinel = "UNKEYED"

// canonicalForm normalizes value before hashing so case + whitespace
// variants share a tag (D-05). Lowercase + trim is the documented
// canonical form; no Unicode NFKC normalization (v1 scope) so e.g.
// full-width characters remain distinct.
func canonicalForm(s string) string {
	trimmed := strings.TrimSpace(s)
	return strings.ToLower(trimmed)
}

// hashTag is the HMAC-SHA256 keyed tag function. Uses hmac.New +
// sha256.New per the stdlib's standard pattern — the explicit choice
// over the hand-rolled raw-SHA256 append-and-sum form neutralizes the
// length-extension class of attacks (RESEARCH §Don't-Hand-Roll HMAC
// row + Pitfall 6). The forbidden raw form (intentionally not named
// here so source-audit greps don't false-positive) is documented in
// the threat register T-8-HASH.
//
// Empty key returns UnkeyedHashSentinel WITHOUT computing an HMAC
// (computing one with an empty key would silently produce a fixed-key
// HMAC across all callers — exactly the Pitfall 6 footgun). A warning
// is emitted to slog.Default so the misconfiguration is observable
// even when the operator doesn't read the redacted tokens themselves.
func hashTag(hashKey []byte, value string) string {
	if len(hashKey) == 0 {
		slog.Default().Warn(
			"pii.hash: empty key — slice-5 boot validation should prevent this",
			"sentinel", UnkeyedHashSentinel,
		)
		return UnkeyedHashSentinel
	}
	mac := hmac.New(sha256.New, hashKey)
	mac.Write([]byte(canonicalForm(value)))
	sum := mac.Sum(nil)
	full := hex.EncodeToString(sum)
	if len(full) < hashTagLen {
		return full
	}
	return full[:hashTagLen]
}

// maskValue applies a partial-obfuscation mask. Algorithm:
//
//   - If value contains '@' (likely email): split on the first '@';
//     local-part keeps its first 2 chars then "***"; domain keeps its
//     first 2 chars then "***" then the final TLD segment (".tld").
//     Example: "corey@cmetech.io" → "co***@cm***.io".
//   - Otherwise: keep first 2 chars + repeat '*' for the middle +
//     keep last 2 chars. Length < 5 returns "****" to avoid revealing
//     short values.
//
// Documented here so slice 5's e2e tests can pin the exact shape and
// (if ever needed) a future config knob can switch algorithms while
// preserving this default.
func maskValue(value string) string {
	if at := strings.IndexByte(value, '@'); at > 0 {
		local := value[:at]
		domain := value[at+1:]
		maskedLocal := maskPrefix(local, 2)
		dot := strings.LastIndexByte(domain, '.')
		var maskedDomain string
		if dot > 0 {
			maskedDomain = maskPrefix(domain[:dot], 2) + domain[dot:]
		} else {
			maskedDomain = maskPrefix(domain, 2)
		}
		return maskedLocal + "@" + maskedDomain
	}
	if len(value) < 5 {
		return "****"
	}
	return value[:2] + strings.Repeat("*", len(value)-4) + value[len(value)-2:]
}

// maskPrefix returns the first n chars of s followed by "***" so the
// rest is obfuscated. If s is shorter than n, returns "***".
func maskPrefix(s string, n int) string {
	if len(s) <= n {
		return "***"
	}
	return s[:n] + "***"
}

// ApplyMode dispatches on mode and returns the redacted token for a
// single recognizer match. Counter is the per-request occurrence
// number for this entity (1, 2, 3, ...); a counter of 0 emits the
// non-suffixed "<ENTITY>" form.
//
// Unknown modes log a warning and fall back to "replace" — defense in
// depth against a slice-5 config validation regression that might let
// a typo slip through. NEVER panics on an unknown mode.
func ApplyMode(mode, entity, value string, counter int, hashKey []byte) string {
	entUpper := strings.ToUpper(entity)
	switch mode {
	case "replace":
		if counter > 0 {
			return fmt.Sprintf("<%s_%d>", entUpper, counter)
		}
		return fmt.Sprintf("<%s>", entUpper)
	case "mask":
		return maskValue(value)
	case "hash":
		return fmt.Sprintf("<%s:h-%s>", entUpper, hashTag(hashKey, value))
	case "drop":
		return ""
	default:
		slog.Default().Warn(
			"pii.ApplyMode: unknown mode, falling back to replace",
			"mode", mode,
		)
		return ApplyMode("replace", entity, value, counter, hashKey)
	}
}
