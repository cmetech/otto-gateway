// Phase 8 PLUG-06 (expanded 2026-06-03) — 13 built-in regex PII
// recognizers + 2 NER recognizers (PERSON, LOCATION) emitted by ner.go
// when PII_NER_ENABLED is true.
//
// Original six (Phase 8): Email, IPv4, IPv6, SSN, CreditCard, USPhone.
// Telecom expansion (2026-06-03 plan): SIP_URI, IMEI, IMSI, MSISDN,
// MAC_ADDRESS, COORDINATES, SITE. Context-anchored recognizers (IMEI,
// IMSI, MSISDN, SITE) use Recognizer.ContextKeywords + a ±50-byte
// window check inside the redact pipeline so ambiguous patterns
// (e.g., 15-digit IMEI vs IMSI) only fire when a recognizer-specific
// keyword sits nearby.
//
// Recognizer struct = regex + optional post-validate filter +
// optional context keywords. All regex compiled at package init via
// regexp.MustCompile (zero per-request compile cost; init panic
// surfaces a bad regex before the binary serves traffic). Pattern 4
// from 08-RESEARCH; validators implement RESEARCH Pitfall 1 (SSN RE2
// workaround) + Don't-Hand-Roll (IPv6 via net.ParseIP).
//
// Recall is < perfect by design per CONTEXT.md. T-8-PII-BYPASS accepts
// the residual miss rate; the prose NER (ner.go) closes part of the
// PERSON / LOCATION gap when enabled.
//
// Extension path (NOT shipped v1): Recognizer can grow MinConfidence and
// Anonymize fields if a future hook needs per-recognizer per-match
// confidence scores or custom redaction tokens. v1 leaves these off so
// the API surface stays minimal until a real consumer appears.

package pii

import (
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Recognizer is the {regex, optional post-validator} pair that identifies
// a single PII entity class. Name is the canonical entity identifier
// used in the redacted token (<NAME>, <NAME_N>, <NAME:h-XXXX>); it
// MUST match the canonical entity registry used by Summary.Counts.
//
// Pattern is the init-time-compiled regex; nil is a programmer error
// caught at package init by regexp.MustCompile panicking.
//
// Validate is an optional post-match filter. When non-nil, a regex match
// only counts if Validate(match) returns true. Used to (a) reject the
// false positives RE2-can't-express (SSN reserved ranges per Pitfall 1)
// and (b) defer to stdlib validators where the regex would otherwise
// be brittle (IPv6 → net.ParseIP per Don't-Hand-Roll).
type Recognizer struct {
	Name     string
	Pattern  *regexp.Regexp
	Validate func(string) bool
	// ContextKeywords, when non-empty, gates a match: the redact pipeline
	// only accepts a regex hit if at least one keyword (case-insensitive)
	// appears within ±defaultContextWindow bytes of the match. Used to
	// disambiguate ambiguous patterns like IMEI vs IMSI (both 15-digit).
	// nil/empty = no context required (existing recognizers stay nil).
	ContextKeywords []string
}

// Init-time compiled regex literals. regexp.MustCompile panics at
// package load if any literal is malformed — surfaces regressions at
// binary boot before any request is handled.
//
//	emailRe       — case-insensitive; ASCII local-part + dotted domain
//	                with TLD 2-24 chars.
//	ipv4Re        — permissive dotted-quad; validateIPv4Octets bounds
//	                each octet to 0-255.
//	ipv6Re        — permissive hex-colon shape; validateIPv6NetParseIP
//	                defers to net.ParseIP for the actual structural check.
//	ssnRe         — permissive 3-2-4 segment shape per RESEARCH Pitfall 1
//	                (RE2 has no negative lookahead); validateSSNRange
//	                rejects SSA-reserved ranges.
//	creditCardRe  — 13-19 digit runs with optional spaces/hyphens
//	                between groups; validateLuhn (luhn.go) confirms.
//	usPhoneRe     — NANP shape: optional +1 prefix, area code starts
//	                with [2-9] per RESEARCH §Pattern 4 line 536.
var (
	emailRe      = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,24}\b`)
	ipv4Re       = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	ipv6Re       = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{1,4}:){2,7}[0-9A-Fa-f:]{1,4}\b`)
	ssnRe        = regexp.MustCompile(`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`)
	creditCardRe = regexp.MustCompile(`\b(?:[0-9][ \-]?){12,18}[0-9]\b`)
	usPhoneRe    = regexp.MustCompile(`\b(?:\+?1[ .\-]?)?\(?[2-9][0-9]{2}\)?[ .\-]?[0-9]{3}[ .\-]?[0-9]{4}\b`)

	// Telecom-domain recognizers ported from loop_24 Privacy Vault.
	//
	//	sipURIRe — RFC 3261 SIP/SIPS URI shape (sip:user@host[:port]).
	//	           Context-free: distinctive enough on its own.
	sipURIRe = regexp.MustCompile(`sips?:[a-zA-Z0-9_.+\-]+@[a-zA-Z0-9.\-]+(?::\d+)?`)

	//	imeiRe — 15-digit run. Shared shape with IMSI; context keywords
	//	          ("imei" / "imsi") distinguish at the redact pipeline.
	imeiRe = regexp.MustCompile(`\b\d{15}\b`)

	//	msisdnRe — E.164 international phone number (+ followed by 8–15
	//	          digits, leading digit 1–9). Context-anchored to MSISDN/
	//	          subscriber-number keywords so naked +<country>... doesn't
	//	          steal USPhone's territory.
	msisdnRe = regexp.MustCompile(`\+[1-9]\d{7,14}`)

	//	macAddrRe — six pairs of hex separated by ':' or '-'. Context-free.
	macAddrRe = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}[:\-]){5}[0-9A-Fa-f]{2}\b`)

	//	coordinatesRe — decimal-degrees lat/long with N/S and E/W
	//	          hemisphere markers (e.g., "37.7749 N, 122.4194 W"). The
	//	          optional °-sign and whitespace are tolerated. Context-
	//	          free; the hemisphere letters are distinctive.
	//
	//	Word-boundary at start (\b) ensures the integer-degree portion
	//	doesn't bleed into an adjacent number. The trailing E/W letter is
	//	a word char so \b on the tail-end is implicit.
	coordinatesRe = regexp.MustCompile(`\b\d{1,3}\.\d+\s*°?\s*[NS][,\s]+\d{1,3}\.\d+\s*°?\s*[EW]\b`)

	//	siteRe — telecom site / network-element identifier. Two
	//	          alternation arms:
	//	            • site[-_ ]XX[-_]YYY style (literal "site" prefix +
	//	              uppercase/digit code).
	//	            • One of {ENB,BTS,NB,CELL,NODE,RAN,BSC,RNC,MSC,HLR,
	//	              MME,SGW,PGW} + uppercase/digit code.
	//	          The regex itself contains a context keyword so
	//	          hasContextWithin succeeds for any actual match.
	// First-arm trailing class allows '_' and '-' as interior separators
	// so multi-segment site codes (e.g., "site-A12_NYC01") match in one
	// span. Trailing class allows 1–12 chars; combined with the 1–2-char
	// head this admits short codes like "site-A12" too.
	siteRe = regexp.MustCompile(
		`\bsite[-_\s]?[A-Z0-9]{1,2}[A-Z0-9_\-]{1,12}\b` +
			`|\b(?:ENB|BTS|NB|CELL|NODE|RAN|BSC|RNC|MSC|HLR|MME|SGW|PGW)[-_]?[A-Z0-9]{2,12}\b`)
)

// validateIPv4Octets splits the matched dotted-quad and confirms each of
// the four octets parses as an integer in [0, 255]. The regex shape
// already gates "1-3 digits per octet, four octets total", so a Split
// of length 4 with each part in range is the post-filter contract.
func validateIPv4Octets(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

// validateIPv6NetParseIP defers to the stdlib net.ParseIP for IPv6
// structural validation (RESEARCH §Don't-Hand-Roll). The colon-contains
// guard is defense-in-depth: net.ParseIP accepts dotted-quad IPv4
// strings, so we reject those here even though the ipv6Re regex shape
// already requires colons.
func validateIPv6NetParseIP(s string) bool {
	return net.ParseIP(s) != nil && strings.Contains(s, ":")
}

// validateSSNRange rejects SSA reserved-range SSNs per the published
// SSA assignment table (RESEARCH Example 3):
//   - aaa == 000
//   - aaa == 666
//   - aaa starts with '9' (900-999 is reserved for ITIN/etc.)
//   - gg == 00
//   - ssss == 0000
//
// The regex shape guarantees aaa-gg-ssss segments of widths 3-2-4, so
// the parse is segment-position fixed.
func validateSSNRange(s string) bool {
	// Defensive segment-shape check (handles the unlikely case the
	// regex was overridden externally).
	if len(s) != 11 || s[3] != '-' || s[6] != '-' {
		return false
	}
	aaa := s[0:3]
	gg := s[4:6]
	ssss := s[7:11]

	if aaa == "000" || aaa == "666" || aaa[0] == '9' {
		return false
	}
	if gg == "00" {
		return false
	}
	if ssss == "0000" {
		return false
	}
	return true
}

// Recognizers is the canonical, registration-ordered registry of v1 PII
// recognizers. Order matters: redact tokens appear with stable per-entity
// counter suffixes, and operator-side filtering via EnabledEntities
// preserves this order. Adding a recognizer is a one-line edit here +
// (optionally) a new validator function above; no init() side effects
// required.
var Recognizers = []Recognizer{
	{Name: "Email", Pattern: emailRe, Validate: nil},
	{Name: "IPv4", Pattern: ipv4Re, Validate: validateIPv4Octets},
	{Name: "IPv6", Pattern: ipv6Re, Validate: validateIPv6NetParseIP},
	{Name: "SSN", Pattern: ssnRe, Validate: validateSSNRange},
	{Name: "CreditCard", Pattern: creditCardRe, Validate: validateLuhn},
	{Name: "USPhone", Pattern: usPhoneRe, Validate: nil},
	{Name: "SIP_URI", Pattern: sipURIRe, Validate: nil},
	{
		Name:            "IMEI",
		Pattern:         imeiRe,
		Validate:        nil,
		ContextKeywords: []string{"imei", "international mobile equipment identity"},
	},
	// IMSI shares the IMEI regex shape; context-keyword filter at the
	// redact pipeline decides which label applies. When both keywords
	// appear near the same span, registration order (IMEI first) wins.
	{
		Name:            "IMSI",
		Pattern:         imeiRe,
		Validate:        nil,
		ContextKeywords: []string{"imsi", "international mobile subscriber identity"},
	},
	{
		Name:    "MSISDN",
		Pattern: msisdnRe,
		Validate: nil,
		ContextKeywords: []string{
			"msisdn", "subscriber number", "calling number", "called number",
		},
	},
	{Name: "MAC_ADDRESS", Pattern: macAddrRe, Validate: nil},
	{Name: "COORDINATES", Pattern: coordinatesRe, Validate: nil},
	{
		Name:     "SITE",
		Pattern:  siteRe,
		Validate: nil,
		ContextKeywords: []string{
			"site", "cell", "base station", "node", "tower",
			"location code", "enb", "bts", "ran", "network element", "ne id",
		},
	},
}

// SourceAuditNames returns the Recognizers names in registration order.
// Used by recognizers_test.go's RegistryShape assertion and (in slice 5)
// by /health/hooks to publish the active recognizer set on the wire.
func SourceAuditNames() []string {
	out := make([]string, 0, len(Recognizers))
	for _, r := range Recognizers {
		out = append(out, r.Name)
	}
	return out
}
