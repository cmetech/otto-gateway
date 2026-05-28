// Phase 8 Plan 08-04 Task 1 — Wave 0 scaffold for pii.LuhnCheck.
//
// LuhnCheck is the validator wired into the CreditCard recognizer
// (RESEARCH §Pattern 4 + Example 2). The function must:
//   - Accept standard public test-BIN numbers (Visa/MC/Amex).
//   - Reject one-digit-flipped variants.
//   - Strip whitespace and hyphens before checksumming.
//   - Enforce length 13-19 (RESEARCH Example 2 line 873).
//   - Be invariant under spaces/hyphens (property test).
//
// All tests must fail with `undefined: LuhnCheck` before Task 2
// implements luhn.go.
//
// Test BINs are from Stripe (https://stripe.com/docs/testing) and Adyen
// public test-card documentation — non-real account numbers.
package pii

import (
	"strings"
	"testing"
	"testing/quick"
)

// TestLuhnCheck_KnownValid asserts public Visa/MC/Amex test BINs that
// must Luhn-validate, including spaced and hyphenated forms.
func TestLuhnCheck_KnownValid(t *testing.T) {
	cases := []struct{ name, in string }{
		{"visa-16", "4111111111111111"},
		{"mc-16", "5555555555554444"},
		{"amex-15", "378282246310005"},
		{"visa-spaced", "4111 1111 1111 1111"},
		{"visa-hyphen", "4111-1111-1111-1111"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !LuhnCheck(c.in) {
				t.Errorf("LuhnCheck(%q) = false, want true", c.in)
			}
		})
	}
}

// TestLuhnCheck_KnownInvalid asserts one-digit-flipped variants and a
// random non-Luhn 16-digit sequence are rejected. Note: per RESEARCH
// Example 2, the all-zeros 16-digit string is technically Luhn-valid
// (sum=0 mod 10) and is an accepted false positive of the recognizer
// regex bank; we do not assert it here.
func TestLuhnCheck_KnownInvalid(t *testing.T) {
	cases := []struct{ name, in string }{
		{"visa-flipped", "4111111111111112"},
		{"random-16", "1234567890123456"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if LuhnCheck(c.in) {
				t.Errorf("LuhnCheck(%q) = true, want false", c.in)
			}
		})
	}
}

// TestLuhnCheck_LengthBounds asserts the length gate (digitCount must be
// 13-19 inclusive per RESEARCH Example 2 line 873).
func TestLuhnCheck_LengthBounds(t *testing.T) {
	cases := []struct{ name, in string }{
		{"empty", ""},
		{"one-digit", "4"},
		{"two-digit", "41"},
		{"twenty-digit", "41111111111111111111"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if LuhnCheck(c.in) {
				t.Errorf("LuhnCheck(%q) = true, want false (length bound)", c.in)
			}
		})
	}
}

// TestLuhnCheck_Property_StripsNonDigits asserts canonical-form
// invariance: inserting random spaces/hyphens into a digit-only string
// must not change the Luhn result.
func TestLuhnCheck_Property_StripsNonDigits(t *testing.T) {
	property := func(seed uint8) bool {
		// Build a deterministic digit-only string of length 16 from the
		// seed; the value's actual Luhn validity is irrelevant — the
		// invariant is that inserting separators doesn't flip the result.
		digits := buildDigits16(seed)
		spaced := insertSeparators(digits, seed)
		return LuhnCheck(digits) == LuhnCheck(spaced)
	}
	cfg := &quick.Config{MaxCount: 500}
	if err := quick.Check(property, cfg); err != nil {
		t.Errorf("LuhnCheck separator-invariance property failed: %v", err)
	}
}

// buildDigits16 returns a deterministic 16-digit string derived from
// seed, suitable for the separator-invariance property.
func buildDigits16(seed uint8) string {
	const digits = "0123456789"
	out := make([]byte, 16)
	x := uint(seed)
	for i := 0; i < 16; i++ {
		out[i] = digits[x%10]
		x = (x * 17) + 31
	}
	return string(out)
}

// insertSeparators sprinkles spaces and hyphens through s in a
// deterministic pattern keyed by seed.
func insertSeparators(s string, seed uint8) string {
	var b strings.Builder
	x := uint(seed) | 1
	for i, r := range s {
		b.WriteRune(r)
		// Every 3rd position-ish, insert a separator.
		if i > 0 && (i+int(x))%4 == 0 {
			if x%2 == 0 {
				b.WriteRune(' ')
			} else {
				b.WriteRune('-')
			}
			x = (x * 13) + 7
		}
	}
	return b.String()
}
