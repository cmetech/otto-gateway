package privacy

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReceiptEncodingHasStableOrderedFieldsAndRawURLAlphabet(t *testing.T) {
	receipt := Receipt{
		Version:     1,
		Profile:     ProfileStandard,
		Scope:       "run-7f29b4d4",
		Coverage:    "input",
		Result:      "pass",
		Transformed: 12,
		Restored:    4,
		Blocked:     0,
	}
	const want = "eyJ2ZXJzaW9uIjoxLCJwcm9maWxlIjoic3RhbmRhcmQiLCJzY29wZSI6InJ1bi03ZjI5YjRkNCIsImNvdmVyYWdlIjoiaW5wdXQiLCJyZXN1bHQiOiJwYXNzIiwidHJhbnNmb3JtZWQiOjEyLCJyZXN0b3JlZCI6NCwiYmxvY2tlZCI6MH0"
	got, err := encodeReceipt(receipt)
	if err != nil {
		t.Fatalf("encodeReceipt: %v", err)
	}
	if got != want {
		t.Fatalf("encoded receipt:\n got %q\nwant %q", got, want)
	}
	if strings.ContainsAny(got, "+/=") {
		t.Fatalf("receipt is not raw URL-safe base64: %q", got)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	const wantJSON = `{"version":1,"profile":"standard","scope":"run-7f29b4d4","coverage":"input","result":"pass","transformed":12,"restored":4,"blocked":0}`
	if string(decoded) != wantJSON {
		t.Fatalf("receipt JSON: got %q, want %q", decoded, wantJSON)
	}
}

func TestReceiptRejectsEncodedValueOver512Bytes(t *testing.T) {
	_, err := encodeReceipt(Receipt{Version: 1, Profile: ProfileStandard, Scope: strings.Repeat("s", 512)})
	if err == nil || err.Error() != "privacy receipt exceeds maximum size" {
		t.Fatalf("encodeReceipt oversized: got %v", err)
	}
}

func TestReceiptSetHeaderCopiesOnlyEncodedReceipt(t *testing.T) {
	state := NewRequestState(RequestMetadata{})
	const raw = "corey@example.com"
	if err := state.setReceipt(Receipt{
		Version:     1,
		Profile:     ProfileStandard,
		Coverage:    "input",
		Result:      "pass",
		Transformed: 1,
	}); err != nil {
		t.Fatalf("setReceipt: %v", err)
	}
	ctx := WithRequestState(context.Background(), state)
	w := httptest.NewRecorder()
	if !SetReceiptHeader(w, ctx) {
		t.Fatal("SetReceiptHeader returned false")
	}
	got := w.Header().Get("X-GW-Privacy-Receipt")
	if got == "" || strings.Contains(got, raw) {
		t.Fatalf("unsafe/missing receipt header %q", got)
	}
}

func TestReceiptSetHeaderReturnsFalseWithoutReceipt(t *testing.T) {
	w := httptest.NewRecorder()
	if SetReceiptHeader(w, context.Background()) {
		t.Fatal("SetReceiptHeader unexpectedly found receipt")
	}
	if got := w.Header().Get("X-GW-Privacy-Receipt"); got != "" {
		t.Fatalf("unexpected receipt header %q", got)
	}
}
