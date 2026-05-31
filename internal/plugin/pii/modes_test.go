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

var testHashKey = []byte("test-key-32-bytes-padding-here!!")

// TestApplyMode_Replace asserts the replace mode shape. When counter
// is 0, the token is "[ENTITY]"; when counter > 0, the token is
// "[ENTITY_N]" (counter-suffix path).
func TestApplyMode_Replace(t *testing.T) {
	if got := ApplyMode("replace", "Email", "corey@cmetech.io", 0, nil); got != "[EMAIL]" {
		t.Errorf("replace counter=0: got %q, want %q", got, "[EMAIL]")
	}
	if got := ApplyMode("replace", "Email", "corey@cmetech.io", 1, nil); got != "[EMAIL_1]" {
		t.Errorf("replace counter=1: got %q, want %q", got, "[EMAIL_1]")
	}
	if got := ApplyMode("replace", "Email", "corey@cmetech.io", 7, nil); got != "[EMAIL_7]" {
		t.Errorf("replace counter=7: got %q, want %q", got, "[EMAIL_7]")
	}
}

// TestApplyMode_Mask asserts the mask shape for an email: at-sign
// preserved; first ~2 chars revealed on local-part; domain partially
// masked.
func TestApplyMode_Mask(t *testing.T) {
	got := ApplyMode("mask", "Email", "corey@cmetech.io", 0, nil)
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
	got2 := ApplyMode("mask", "SSN", "123-45-6789", 0, nil)
	if !strings.Contains(got2, "*") {
		t.Errorf("mask non-email: expected masking chars: got %q", got2)
	}
}

// TestApplyMode_Hash_HMAC_SHA256_NotRawSHA256 is the load-bearing
// T-8-HASH negative test: hash output must NOT match raw SHA256.
func TestApplyMode_Hash_HMAC_SHA256_NotRawSHA256(t *testing.T) {
	value := "corey@cmetech.io"
	tag := ApplyMode("hash", "Email", value, 0, testHashKey)
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
	t1 := ApplyMode("hash", "Email", "Corey@CMETECH.io", 0, testHashKey)
	t2 := ApplyMode("hash", "Email", "  corey@cmetech.io  ", 0, testHashKey)
	t3 := ApplyMode("hash", "Email", "corey@cmetech.io", 0, testHashKey)
	if t1 != t2 || t2 != t3 {
		t.Errorf("canonical form not applied:\n  Corey@CMETECH.io      → %q\n  '  corey@cmetech.io  ' → %q\n  corey@cmetech.io        → %q",
			t1, t2, t3)
	}
}

// TestApplyMode_Hash_TagLength asserts the 8-hex-char default tag size
// (D-05). The output shape is "[ENTITY:h-XXXXXXXX]".
func TestApplyMode_Hash_TagLength(t *testing.T) {
	tag := ApplyMode("hash", "Email", "corey@cmetech.io", 0, testHashKey)
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
	tag := ApplyMode("hash", "Email", "corey@cmetech.io", 0, nil)
	if !strings.Contains(strings.ToUpper(tag), "UNKEYED") {
		t.Errorf("empty-key hash: got %q, want token containing 'UNKEYED' (T-8-HASH defensive)", tag)
	}
	tag2 := ApplyMode("hash", "Email", "corey@cmetech.io", 0, []byte{})
	if !strings.Contains(strings.ToUpper(tag2), "UNKEYED") {
		t.Errorf("empty-key hash (zero-len): got %q, want token containing 'UNKEYED'", tag2)
	}
}

// TestApplyMode_Drop asserts the drop mode returns the empty string.
func TestApplyMode_Drop(t *testing.T) {
	if got := ApplyMode("drop", "Email", "corey@cmetech.io", 0, nil); got != "" {
		t.Errorf("drop: got %q, want empty string", got)
	}
}

// TestApplyMode_UnknownMode_FallsBackToReplace asserts that an unknown
// mode falls back to the replace shape (safe-default). The
// implementation MUST NOT panic.
func TestApplyMode_UnknownMode_FallsBackToReplace(t *testing.T) {
	got := ApplyMode("bogus", "Email", "x@x.com", 0, nil)
	if got != "[EMAIL]" {
		t.Errorf("unknown mode: got %q, want %q (replace fallback)", got, "[EMAIL]")
	}
	gotCounter := ApplyMode("bogus", "Email", "x@x.com", 3, nil)
	if gotCounter != "[EMAIL_3]" {
		t.Errorf("unknown mode w/ counter: got %q, want %q", gotCounter, "[EMAIL_3]")
	}
}
