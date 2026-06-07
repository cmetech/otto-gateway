// Phase 8 PLUG-06 — Luhn checksum validator for the CreditCard
// recognizer's Validate field. Pure stdlib; no external deps. Strips
// non-digit characters before checking (RESEARCH Pattern 4 + Example 2).
// Length-bounded 13-19 digits per RESEARCH Example 2 line 873.
//
// Algorithm reference: Rosetta Code "Luhn test of credit card numbers"
// (https://rosettacode.org/wiki/Luhn_test_of_credit_card_numbers).
//
// Why both LuhnCheck and validateLuhn: LuhnCheck is the exported
// canonical implementation; validateLuhn is the closure-shape adapter
// that matches the Recognizer.Validate signature (`func(string) bool`)
// so recognizers.go can wire it without using a method expression.

package pii

import "unicode"

// LuhnCheck reports whether s passes the Luhn (mod-10) checksum after
// stripping every non-digit character. Returns false for inputs whose
// digit count falls outside [13, 19] — that range matches the issuer
// network span Visa/MC/Amex/Discover/JCB/UnionPay use in production and
// keeps the recognizer from green-lighting arbitrary 4- or 25-digit
// numeric strings as cards.
//
// The algorithm is the two-pass mod-10:
//  1. Iterate digits right-to-left.
//  2. Every other digit (the second, fourth, ... from the right) is
//     doubled; if the doubled value exceeds 9, subtract 9 (equivalently,
//     sum the two digits of the doubled value).
//  3. Sum every (transformed) digit.
//  4. The number is Luhn-valid iff sum % 10 == 0.
func LuhnCheck(s string) bool {
	sum := 0
	alt := false
	digitCount := 0
	// Right-to-left scan via reverse iteration over runes.
	for i := len(s) - 1; i >= 0; i-- {
		r := rune(s[i])
		if !unicode.IsDigit(r) {
			continue
		}
		digitCount++
		d := int(r - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	// Length gate: digitCount >= 13 && digitCount <= 19 (RESEARCH
	// Example 2 line 873).
	if digitCount < 13 || digitCount > 19 {
		return false
	}
	return sum%10 == 0
}

// validateLuhn is the Recognizer.Validate-shaped closure adapter for the
// CreditCard recognizer. Recognizers literal in recognizers.go wires
// this into the {Name: "CreditCard", ...}.Validate field.
func validateLuhn(matched string) bool {
	return LuhnCheck(matched)
}
