// Tests for hasContextWithin — verifies the ±N-char window keyword
// check that powers IMEI/IMSI/MSISDN/SITE context disambiguation.
package pii

import "testing"

func TestHasContextWithin_KeywordBefore(t *testing.T) {
	text := "IMEI: 490154203237518 captured at the gateway"
	start := len("IMEI: ")
	end := start + len("490154203237518")
	if !hasContextWithin(text, start, end, []string{"imei"}, 50) {
		t.Error("expected to find 'imei' before the match within 50 chars")
	}
}

func TestHasContextWithin_KeywordAfter(t *testing.T) {
	text := "value 490154203237518 (imei) was observed"
	start := len("value ")
	end := start + len("490154203237518")
	if !hasContextWithin(text, start, end, []string{"imei"}, 50) {
		t.Error("expected to find 'imei' after the match within 50 chars")
	}
}

func TestHasContextWithin_NoKeyword(t *testing.T) {
	text := "a bare 15-digit run 490154203237518 with no context"
	start := len("a bare 15-digit run ")
	end := start + len("490154203237518")
	if hasContextWithin(text, start, end, []string{"imei"}, 50) {
		t.Error("did not expect any keyword to match in plain context")
	}
}

func TestHasContextWithin_CaseInsensitive(t *testing.T) {
	text := "IMSI: 310150123456789 — subscriber number"
	start := len("IMSI: ")
	end := start + len("310150123456789")
	if !hasContextWithin(text, start, end, []string{"imsi"}, 50) {
		t.Error("expected case-insensitive match of 'imsi' against 'IMSI:'")
	}
}

func TestHasContextWithin_OutsideWindow(t *testing.T) {
	prefix := "imei "
	pad := make([]byte, 200)
	for i := range pad {
		pad[i] = 'x'
	}
	text := prefix + string(pad) + " 490154203237518"
	start := len(prefix) + len(pad) + 1
	end := start + len("490154203237518")
	if hasContextWithin(text, start, end, []string{"imei"}, 50) {
		t.Error("keyword sits beyond the 50-char window; must NOT match")
	}
}

func TestHasContextWithin_EmptyKeywords(t *testing.T) {
	text := "anything here 12345"
	if !hasContextWithin(text, 0, 5, nil, 50) {
		t.Error("nil keywords list must short-circuit to true (no context required)")
	}
}
