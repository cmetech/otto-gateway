// Phase 8 Plan 08-04 Task 1 — Wave 0 scaffold for the six PII
// recognizers (Email, IPv4, IPv6, SSN, CreditCard, USPhone).
//
// Tests exercise BOTH the regex shape (via r.Pattern.MatchString) AND
// the post-validate filter (r.Validate) separately so failures pinpoint
// which layer rejected. RESEARCH §Pattern 4 + Pitfall 1 (SSN) +
// Don't-Hand-Roll (IPv6 net.ParseIP).
//
// All tests must fail with `undefined: Recognizers` / `undefined:
// SourceAuditNames` before Task 3 implements recognizers.go.
package pii

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// findRecognizer returns the Recognizer with Name == name, or fails the
// test.
func findRecognizer(t *testing.T, name string) Recognizer {
	t.Helper()
	for _, r := range Recognizers {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("recognizer %q not present in Recognizers slice", name)
	return Recognizer{}
}

// regexAndValidate runs the recognizer's regex + Validate filter against
// in. Returns (regexMatched, validatorPassed).
func regexAndValidate(r Recognizer, in string) (bool, bool) {
	loc := r.Pattern.FindString(in)
	if loc == "" {
		return false, false
	}
	if r.Validate == nil {
		return true, true
	}
	return true, r.Validate(loc)
}

// TestEmailRecognizer asserts the Email regex + (nil validator). Case-
// insensitive via (?i) flag in the regex literal.
func TestEmailRecognizer(t *testing.T) {
	r := findRecognizer(t, "Email")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"corey@cmetech.io", true},
		{"corey+gsd@cmetech.io", true},
		{"user@mail.example.co.uk", true},
		{"@cmetech.io", false},
		{"corey@host", false},
		{"Corey@CMETECH.IO", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("Email %q: regex matched=%v, want %v", c.in, got, c.wantMatched)
			}
		})
	}
}

// TestIPv4Recognizer_OctetValidator asserts the IPv4 regex + octet validator.
func TestIPv4Recognizer_OctetValidator(t *testing.T) {
	r := findRecognizer(t, "IPv4")
	cases := []struct {
		in           string
		wantRegex    bool
		wantValidate bool
	}{
		{"192.168.1.1", true, true},
		{"255.255.255.255", true, true},
		{"256.1.1.1", true, false}, // regex matches but validator rejects (octet > 255)
		{"999.999.999.999", true, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			gotR, gotV := regexAndValidate(r, c.in)
			if gotR != c.wantRegex {
				t.Errorf("IPv4 %q: regex matched=%v, want %v", c.in, gotR, c.wantRegex)
			}
			if gotR && gotV != c.wantValidate {
				t.Errorf("IPv4 %q: validator=%v, want %v", c.in, gotV, c.wantValidate)
			}
		})
	}
}

// TestIPv6Recognizer_NetParseIPValidator asserts the IPv6 regex + net.ParseIP
// validator (Don't-Hand-Roll mandate per RESEARCH).
//
// Documented v1 limitation (T-8-PII-BYPASS accepted): the canonical
// regex shape from RESEARCH §Pattern 4 requires {2,7} hex-colon groups
// before the trailing group, so the abbreviated forms `::1` and
// `fe80::1` (one hex group total) do NOT match. Operators needing
// loopback / link-local IPv6 detection upgrade to the v2 NER path
// (deferred per CONTEXT.md "Deferred Ideas"). The test fixtures use
// forms within the regex's coverage envelope.
func TestIPv6Recognizer_NetParseIPValidator(t *testing.T) {
	r := findRecognizer(t, "IPv6")
	cases := []struct {
		in           string
		wantRegex    bool
		wantValidate bool
	}{
		{"2001:db8::1", true, true},
		{"fe80:0:0:0::1", true, true},
		{"gggg::1", false, false},          // regex rejects hex set
		{"1:2:3:4:5:6:7:8:9", true, false}, // regex matches but net.ParseIP rejects
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			gotR, gotV := regexAndValidate(r, c.in)
			if gotR != c.wantRegex {
				t.Errorf("IPv6 %q: regex matched=%v, want %v", c.in, gotR, c.wantRegex)
			}
			if gotR && gotV != c.wantValidate {
				t.Errorf("IPv6 %q: validator=%v, want %v", c.in, gotV, c.wantValidate)
			}
		})
	}
}

// TestSSNRecognizer_ReservedRangeValidator asserts the SSN regex (RE2
// permissive shape per RESEARCH Pitfall 1) + validateSSNRange reserved-
// range filter.
func TestSSNRecognizer_ReservedRangeValidator(t *testing.T) {
	r := findRecognizer(t, "SSN")
	cases := []struct {
		in           string
		wantRegex    bool
		wantValidate bool
	}{
		{"123-45-6789", true, true},
		{"000-12-3456", true, false}, // aaa = 000 reserved
		{"666-12-3456", true, false}, // aaa = 666 reserved
		{"900-12-3456", true, false}, // aaa starts with 9 reserved
		{"123-00-6789", true, false}, // gg = 00 reserved
		{"123-45-0000", true, false}, // ssss = 0000 reserved
		{"12-34-5678", false, false}, // wrong segment lengths
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			gotR, gotV := regexAndValidate(r, c.in)
			if gotR != c.wantRegex {
				t.Errorf("SSN %q: regex matched=%v, want %v", c.in, gotR, c.wantRegex)
			}
			if gotR && gotV != c.wantValidate {
				t.Errorf("SSN %q: validator=%v, want %v", c.in, gotV, c.wantValidate)
			}
		})
	}
}

// TestCreditCardRecognizer_LuhnValidator asserts the credit card regex +
// Luhn validator.
func TestCreditCardRecognizer_LuhnValidator(t *testing.T) {
	r := findRecognizer(t, "CreditCard")
	cases := []struct {
		in           string
		wantRegex    bool
		wantValidate bool
	}{
		{"4111111111111111", true, true},
		{"4111 1111 1111 1111", true, true},
		{"4111111111111112", true, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			gotR, gotV := regexAndValidate(r, c.in)
			if gotR != c.wantRegex {
				t.Errorf("CC %q: regex matched=%v, want %v", c.in, gotR, c.wantRegex)
			}
			if gotR && gotV != c.wantValidate {
				t.Errorf("CC %q: validator=%v, want %v", c.in, gotV, c.wantValidate)
			}
		})
	}
}

// TestUSPhoneRecognizer asserts the US-phone regex (no validator). The
// area code first digit must be 2-9 per RESEARCH §Pattern 4 line 536.
func TestUSPhoneRecognizer(t *testing.T) {
	r := findRecognizer(t, "USPhone")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"+1 (555) 123-4567", true},
		{"555-123-4567", true},
		{"5551234567", true},
		{"(555) 123 4567", true},
		{"123-4567", false},     // missing area code
		{"155-555-5555", false}, // area code starts with 1
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("USPhone %q: regex matched=%v, want %v", c.in, got, c.wantMatched)
			}
		})
	}
}

// TestSIPURIRecognizer — RFC 3261 SIP/SIPS URI shape (sip:user@host[:port]).
// Context-free: pattern is distinctive enough on its own.
func TestSIPURIRecognizer(t *testing.T) {
	r := findRecognizer(t, "SIP_URI")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"sip:alice@atlanta.example.com", true},
		{"sips:bob@biloxi.example.com:5061", true},
		{"contact me at sip:carol@chicago.example.com please", true},
		{"sip:", false},
		{"plain email user@host.com", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("SIP_URI %q: regex matched=%v, want %v", c.in, got, c.wantMatched)
			}
		})
	}
}

// TestIMEIRecognizer — 15-digit subscriber-equipment identifier. Shares
// its raw regex shape with IMSI; context keywords disambiguate. Without
// an "imei" keyword nearby, the regex matches but the redact pipeline
// (TestIMEI_ContextAnchored_Integration) rejects via ContextKeywords +
// hasContextWithin.
func TestIMEIRecognizer(t *testing.T) {
	r := findRecognizer(t, "IMEI")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"490154203237518", true},
		{"49015420323751", false},   // 14 digits
		{"4901542032375180", false}, // 16 digits — \b boundary prevents partial match
		{"id 490154203237518 stop", true},
		{"abc490154203237518xyz", false}, // letters abut digits → no \b
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("IMEI shape %q: regex matched=%v, want %v", c.in, got, c.wantMatched)
			}
		})
	}
	wantKw := []string{"imei", "international mobile equipment identity"}
	if len(r.ContextKeywords) != len(wantKw) {
		t.Fatalf("IMEI ContextKeywords len: got %d, want %d", len(r.ContextKeywords), len(wantKw))
	}
	for i, kw := range wantKw {
		if r.ContextKeywords[i] != kw {
			t.Errorf("IMEI ContextKeywords[%d]: got %q, want %q", i, r.ContextKeywords[i], kw)
		}
	}
}

// TestIMSIRecognizer — same 15-digit shape as IMEI, disambiguated by
// "imsi" / "international mobile subscriber identity" context keyword.
func TestIMSIRecognizer(t *testing.T) {
	r := findRecognizer(t, "IMSI")
	if !r.Pattern.MatchString("310150123456789") {
		t.Error("IMSI shape: 15-digit run must match")
	}
	wantKw := []string{"imsi", "international mobile subscriber identity"}
	if len(r.ContextKeywords) != len(wantKw) {
		t.Fatalf("IMSI ContextKeywords len: got %d, want %d", len(r.ContextKeywords), len(wantKw))
	}
	for i, kw := range wantKw {
		if r.ContextKeywords[i] != kw {
			t.Errorf("IMSI ContextKeywords[%d]: got %q, want %q", i, r.ContextKeywords[i], kw)
		}
	}
}

// TestMSISDNRecognizer — E.164 international phone number, context-
// anchored. Naked E.164 numbers without context fall through (avoids
// competing with USPhone in informal contexts).
func TestMSISDNRecognizer(t *testing.T) {
	r := findRecognizer(t, "MSISDN")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"+14155552671", true},
		{"+442071838750", true},
		{"+1", false},          // too short (regex requires 8-15 digits after +)
		{"+0123456789", false}, // leading 0 after + disallowed
		{"14155552671", false}, // missing '+'
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("MSISDN %q: regex matched=%v, want %v", c.in, got, c.wantMatched)
			}
		})
	}
	wantKw := []string{"msisdn", "subscriber number", "calling number", "called number"}
	if len(r.ContextKeywords) != len(wantKw) {
		t.Fatalf("MSISDN ContextKeywords len: got %d, want %d", len(r.ContextKeywords), len(wantKw))
	}
}

// TestMACAddressRecognizer — six pairs of hex with either ':' or '-'
// separators. Context-free.
func TestMACAddressRecognizer(t *testing.T) {
	r := findRecognizer(t, "MAC_ADDRESS")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"00:1B:44:11:3A:B7", true},
		{"00-1B-44-11-3A-B7", true},
		{"aa:bb:cc:dd:ee:ff", true},
		{"00:1B:44:11:3A", false},    // 5 pairs
		{"GG:1B:44:11:3A:B7", false}, // invalid hex
		{"mac=00:1B:44:11:3A:B7,then=more", true}, // embedded MAC ok
		{"::::::::::::", false},                   // colons but no hex
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("MAC %q: matched=%v want=%v", c.in, got, c.wantMatched)
			}
		})
	}
}

// TestRecognizers_RegistryShape asserts the registry has the expected
// names in registration order, each with a non-nil Pattern.
func TestRecognizers_RegistryShape(t *testing.T) {
	wantNames := []string{"Email", "IPv4", "IPv6", "SSN", "CreditCard", "USPhone", "SIP_URI", "IMEI", "IMSI", "MSISDN", "MAC_ADDRESS"}
	if got := len(Recognizers); got != len(wantNames) {
		t.Fatalf("len(Recognizers): got %d, want %d", got, len(wantNames))
	}
	gotNames := SourceAuditNames()
	if len(gotNames) != len(wantNames) {
		t.Fatalf("SourceAuditNames len: got %d, want %d", len(gotNames), len(wantNames))
	}
	for i, want := range wantNames {
		if gotNames[i] != want {
			t.Errorf("SourceAuditNames[%d]: got %q, want %q", i, gotNames[i], want)
		}
	}
	for i, r := range Recognizers {
		if r.Pattern == nil {
			t.Errorf("Recognizers[%d] (%s): nil Pattern", i, r.Name)
		}
	}
}

// TestRecognizers_CompiledAtPackageInit_NoPerRequestCompile is a source-
// level guard. recognizers.go MUST use regexp.MustCompile (init-time);
// regexp.Compile inside the file is forbidden (would imply runtime
// compile path).
func TestRecognizers_CompiledAtPackageInit_NoPerRequestCompile(t *testing.T) {
	raw, err := os.ReadFile("recognizers.go")
	if err != nil {
		t.Skipf("recognizers.go not present yet (pre-Task-3 RED state): %v", err)
		return
	}
	src := stripGoCommentsLocal(raw)
	if !regexp.MustCompile(`regexp\.MustCompile\(`).Match(src) {
		t.Error("recognizers.go missing regexp.MustCompile (init-time compile expected)")
	}
	// regexp.Compile (without Must) is a runtime path — forbidden.
	if regexp.MustCompile(`\bregexp\.Compile\(`).Match(src) {
		t.Error("recognizers.go contains regexp.Compile( — must use MustCompile at init")
	}
	// Belt-and-suspenders: at least one MustCompile per UNIQUE pattern.
	// Recognizers may share a compiled regex (IMEI/IMSI both reference
	// imeiRe and are disambiguated by ContextKeywords); count unique
	// pattern pointers, not Recognizers.
	uniquePatterns := make(map[*regexp.Regexp]struct{})
	for _, r := range Recognizers {
		uniquePatterns[r.Pattern] = struct{}{}
	}
	count := strings.Count(string(src), "regexp.MustCompile(")
	if count < len(uniquePatterns) {
		t.Errorf("regexp.MustCompile call count: got %d, want at least %d (unique patterns)",
			count, len(uniquePatterns))
	}
}

// stripGoCommentsLocal is a local copy of the helper from the plugin
// package's logging_test.go — keeping pii whitebox-tests self-contained
// (whitebox package can't import the parent plugin package's test
// helpers).
func stripGoCommentsLocal(src []byte) []byte {
	out := make([]byte, 0, len(src))
	i := 0
	for i < len(src) {
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			if i+1 < len(src) {
				i += 2
			}
			continue
		}
		out = append(out, src[i])
		i++
	}
	return out
}
