// Phase 8 Plan 08-04 Task 1 — Wave 0 scaffold for pii.ApplyMode (D-05).
//
// Tests exercise the four redaction modes:
//   - replace → "[ENTITY]" or counter-suffixed "[ENTITY_N]"
//   - mask    → partial obfuscation (docs/algorithm in modes.go)
//   - hash    → HMAC-SHA256 of canonical(value) keyed by PII_HASH_KEY,
//               truncated to 8 hex chars (D-05 + T-8-HASH mitigation)
//   - drop    → "" (empty string)
//
// CRITICAL negative assertion: hash mode MUST NOT be raw SHA256
// (RESEARCH Don't-Hand-Roll + Pitfall 6). Test 20 includes the
// length-extension-resistant guard.
//
// All tests must fail with `undefined: ApplyMode` before Task 4
// implements modes.go.
package pii

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

// PII-ENCRYPT-03 — ApplyMode encrypt case. Round-trips through
// EncryptValue / DecryptToken via the wire-token regex parsing.

func TestApplyMode_Encrypt(t *testing.T) {
	key, err := DeriveKey("test-key")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	tok := ApplyMode("encrypt", "Email", "corey@cmetech.io", 0, nil, key)
	if !strings.HasPrefix(tok, tokenPrefix+"Email:") {
		t.Fatalf("encrypt token shape: got %q", tok)
	}
	// Round-trip: strip prefix/suffix, decrypt, expect plaintext.
	payload := tok[len(tokenPrefix+"Email:") : len(tok)-len(tokenSuffix)]
	got, err := DecryptToken(key, "Email", payload)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "corey@cmetech.io" {
		t.Errorf("decrypt: got %q, want %q", got, "corey@cmetech.io")
	}
}

func TestApplyMode_Encrypt_EmptyKeyFailSafe(t *testing.T) {
	// Empty key would make EncryptValue fail; ApplyMode logs warn and
	// returns the plaintext (visible rather than silent broken token).
	got := ApplyMode("encrypt", "Email", "corey@cmetech.io", 0, nil, nil)
	if got != "corey@cmetech.io" {
		t.Errorf("encrypt fail-safe: got %q, want plaintext fallback %q", got, "corey@cmetech.io")
	}
}

var testHashKey = []byte("test-key-32-bytes-padding-here!!")

// TestApplyMode_Replace asserts the replace mode shape. When counter
// is 0, the token is "[ENTITY]"; when counter > 0, the token is
// "[ENTITY_N]" (counter-suffix path).
func TestApplyMode_Replace(t *testing.T) {
	if got := ApplyMode("replace", "Email", "corey@cmetech.io", 0, nil, nil); got != "[EMAIL]" {
		t.Errorf("replace counter=0: got %q, want %q", got, "[EMAIL]")
	}
	if got := ApplyMode("replace", "Email", "corey@cmetech.io", 1, nil, nil); got != "[EMAIL_1]" {
		t.Errorf("replace counter=1: got %q, want %q", got, "[EMAIL_1]")
	}
	if got := ApplyMode("replace", "Email", "corey@cmetech.io", 7, nil, nil); got != "[EMAIL_7]" {
		t.Errorf("replace counter=7: got %q, want %q", got, "[EMAIL_7]")
	}
}

// TestApplyMode_Mask asserts the mask shape for an email: at-sign
// preserved; first ~2 chars revealed on local-part; domain partially
// masked.
func TestApplyMode_Mask(t *testing.T) {
	got := ApplyMode("mask", "Email", "corey@cmetech.io", 0, nil, nil)
	if !strings.Contains(got, "@") {
		t.Errorf("mask: at-sign not preserved: got %q", got)
	}
	if !strings.HasPrefix(got, "co") {
		t.Errorf("mask: first 2 chars of local-part should be revealed: got %q", got)
	}
	if !strings.Contains(got, "*") {
		t.Errorf("mask: should contain masking chars: got %q", got)
	}
	// Non-email values: still produce some mask form.
	got2 := ApplyMode("mask", "SSN", "123-45-6789", 0, nil, nil)
	if !strings.Contains(got2, "*") {
		t.Errorf("mask non-email: expected masking chars: got %q", got2)
	}
}

// TestApplyMode_Hash_HMAC_SHA256_NotRawSHA256 is the load-bearing
// T-8-HASH negative test: hash output must NOT match raw SHA256.
func TestApplyMode_Hash_HMAC_SHA256_NotRawSHA256(t *testing.T) {
	value := "corey@cmetech.io"
	tag := ApplyMode("hash", "Email", value, 0, testHashKey, nil)
	wantTag := "[EMAIL:h-5e114e4d]"
	if tag != wantTag {
		t.Errorf("hash tag: got %q, want %q (HMAC-SHA256 oracle)", tag, wantTag)
	}
	// Negative: raw SHA256 of the canonical(value) — must NOT appear
	// inside the tag.
	raw := sha256.Sum256([]byte("corey@cmetech.io"))
	rawHex := hex.EncodeToString(raw[:4]) // 8 hex chars
	if strings.Contains(tag, rawHex) {
		t.Errorf("hash tag %q contains raw-SHA256 prefix %q — must be HMAC, not raw", tag, rawHex)
	}
}

// TestApplyMode_Hash_CanonicalForm asserts that 'Corey@CMETECH.io',
// '  corey@cmetech.io  ', and 'corey@cmetech.io' all produce the same
// hash tag (canonical = lowercase + trim per D-05).
func TestApplyMode_Hash_CanonicalForm(t *testing.T) {
	t1 := ApplyMode("hash", "Email", "Corey@CMETECH.io", 0, testHashKey, nil)
	t2 := ApplyMode("hash", "Email", "  corey@cmetech.io  ", 0, testHashKey, nil)
	t3 := ApplyMode("hash", "Email", "corey@cmetech.io", 0, testHashKey, nil)
	if t1 != t2 || t2 != t3 {
		t.Errorf("canonical form not applied:\n  Corey@CMETECH.io      → %q\n  '  corey@cmetech.io  ' → %q\n  corey@cmetech.io        → %q",
			t1, t2, t3)
	}
}

// TestApplyMode_Hash_TagLength asserts the 8-hex-char default tag size
// (D-05). The output shape is "[ENTITY:h-XXXXXXXX]".
func TestApplyMode_Hash_TagLength(t *testing.T) {
	tag := ApplyMode("hash", "Email", "corey@cmetech.io", 0, testHashKey, nil)
	re := regexp.MustCompile(`^\[EMAIL:h-[0-9a-f]{8}\]$`)
	if !re.MatchString(tag) {
		t.Errorf("hash tag shape: got %q, want [EMAIL:h-XXXXXXXX] (8 hex)", tag)
	}
}

// TestApplyMode_Hash_EmptyKey_ReturnsError_Or_SentinelToken asserts
// defensive behavior when an empty key is passed (T-8-HASH Pitfall 6).
// The implementation returns a sentinel token containing "UNKEYED" and
// emits a warning, rather than silently producing a fixed-key HMAC.
func TestApplyMode_Hash_EmptyKey_ReturnsError_Or_SentinelToken(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			// Panic is also an acceptable defensive behavior.
			return
		}
	}()
	tag := ApplyMode("hash", "Email", "corey@cmetech.io", 0, nil, nil)
	if !strings.Contains(strings.ToUpper(tag), "UNKEYED") {
		t.Errorf("empty-key hash: got %q, want token containing 'UNKEYED' (T-8-HASH defensive)", tag)
	}
	tag2 := ApplyMode("hash", "Email", "corey@cmetech.io", 0, []byte{}, nil)
	if !strings.Contains(strings.ToUpper(tag2), "UNKEYED") {
		t.Errorf("empty-key hash (zero-len): got %q, want token containing 'UNKEYED'", tag2)
	}
}

// TestApplyMode_Drop asserts the drop mode returns the empty string.
func TestApplyMode_Drop(t *testing.T) {
	if got := ApplyMode("drop", "Email", "corey@cmetech.io", 0, nil, nil); got != "" {
		t.Errorf("drop: got %q, want empty string", got)
	}
}

// TestApplyMode_UnknownMode_FallsBackToReplace asserts that an unknown
// mode falls back to the replace shape (safe-default). The
// implementation MUST NOT panic.
func TestApplyMode_UnknownMode_FallsBackToReplace(t *testing.T) {
	got := ApplyMode("bogus", "Email", "x@x.com", 0, nil, nil)
	if got != "[EMAIL]" {
		t.Errorf("unknown mode: got %q, want %q (replace fallback)", got, "[EMAIL]")
	}
	gotCounter := ApplyMode("bogus", "Email", "x@x.com", 3, nil, nil)
	if gotCounter != "[EMAIL_3]" {
		t.Errorf("unknown mode w/ counter: got %q, want %q", gotCounter, "[EMAIL_3]")
	}
}

// TestApplyMode_NoAngleBrackets_RegressionForKiroHang is the load-bearing
// negative regression test for quick 260531-pt8. The previously-used
// angle-bracketed sentinel shape (LT ENTITY underscore N GT, where LT/GT
// are the ASCII less-than / greater-than characters) caused kiro-cli /
// Claude to treat the token as the opening of an XML tag and block
// waiting for the matching close tag, hanging engine.ACP.Prompt() until
// the 120s client timeout (diagnosed in quick 260531-oox via DEBUG
// markers). The fix is the shape itself — square brackets — and this
// test enforces it negatively:
// ApplyMode output for `replace` mode (with and without a counter
// suffix), `hash` mode, AND `encrypt` mode MUST contain NEITHER '<'
// NOR '>'. Any future change that reintroduces angle brackets to
// those modes trips here.
func TestApplyMode_NoAngleBrackets_RegressionForKiroHang(t *testing.T) {
	value := "corey@cmetech.io"
	encKey, err := DeriveKey("regression-test-key")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	cases := []struct {
		name       string
		mode       string
		counter    int
		hashKey    []byte
		encryptKey []byte
	}{
		{"replace_counter0", "replace", 0, nil, nil},
		{"replace_counter2", "replace", 2, nil, nil},
		{"hash_counter0", "hash", 0, testHashKey, nil},
		{"encrypt_counter0", "encrypt", 0, nil, encKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ApplyMode(tc.mode, "Email", value, tc.counter, tc.hashKey, tc.encryptKey)
			if strings.Contains(got, "<") {
				t.Errorf("%s: output %q contains '<' — kiro-hang regression (use square brackets)", tc.name, got)
			}
			if strings.Contains(got, ">") {
				t.Errorf("%s: output %q contains '>' — kiro-hang regression (use square brackets)", tc.name, got)
			}
		})
	}
}
