// PII-ENCRYPT-01 — key derivation tests. DeriveKey hashes any non-empty
// string via SHA-256 into a 32-byte AES-256-GCM key. Empty input is an
// error (encrypt cannot operate without a key).

package pii

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeriveKey_Empty(t *testing.T) {
	_, err := DeriveKey("")
	if err == nil {
		t.Fatal("DeriveKey(\"\"): expected error, got nil")
	}
}

func TestDeriveKey_Length(t *testing.T) {
	k, err := DeriveKey("any-string")
	if err != nil {
		t.Fatalf("DeriveKey: unexpected error: %v", err)
	}
	if len(k) != 32 {
		t.Errorf("DeriveKey length: got %d, want 32", len(k))
	}
}

func TestDeriveKey_Deterministic(t *testing.T) {
	a, err := DeriveKey("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("DeriveKey first call: %v", err)
	}
	b, err := DeriveKey("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("DeriveKey second call: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("DeriveKey is not deterministic across calls with the same input")
	}
}

func TestDeriveKey_DiffersByInput(t *testing.T) {
	a, err := DeriveKey("hello")
	if err != nil {
		t.Fatalf("DeriveKey(\"hello\"): %v", err)
	}
	b, err := DeriveKey("Hello")
	if err != nil {
		t.Fatalf("DeriveKey(\"Hello\"): %v", err)
	}
	if bytes.Equal(a, b) {
		t.Error("DeriveKey: case-different inputs produced identical keys")
	}
}

// PII-ENCRYPT-02 — round-trip + AAD + wrong-key + nonce-randomness.

func TestEncryptValue_TokenShape(t *testing.T) {
	k, err := DeriveKey("test-key")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	tok, err := EncryptValue(k, "Email", "corey@cmetech.io")
	if err != nil {
		t.Fatalf("EncryptValue: %v", err)
	}
	if !strings.HasPrefix(tok, "[PII:Email:") {
		t.Errorf("token prefix: got %q, want [PII:Email:...]", tok)
	}
	if !strings.HasSuffix(tok, "]") {
		t.Errorf("token suffix: got %q, want trailing ]", tok)
	}
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	k, err := DeriveKey("test-key")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	tok, err := EncryptValue(k, "Email", "corey@cmetech.io")
	if err != nil {
		t.Fatalf("EncryptValue: %v", err)
	}
	// Strip "[PII:Email:" prefix and "]" suffix to recover the payload.
	payload := tok[len("[PII:Email:") : len(tok)-1]
	got, err := DecryptToken(k, "Email", payload)
	if err != nil {
		t.Fatalf("DecryptToken: %v", err)
	}
	if got != "corey@cmetech.io" {
		t.Errorf("round-trip: got %q, want %q", got, "corey@cmetech.io")
	}
}

func TestDecryptToken_AADMismatch(t *testing.T) {
	k, err := DeriveKey("test-key")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	tok, err := EncryptValue(k, "Email", "corey@cmetech.io")
	if err != nil {
		t.Fatalf("EncryptValue: %v", err)
	}
	payload := tok[len("[PII:Email:") : len(tok)-1]
	// Attempt decrypt with the WRONG entity name. AAD binding must fail.
	if _, err := DecryptToken(k, "SSN", payload); err == nil {
		t.Error("DecryptToken: AAD mismatch should fail; got nil error")
	}
}

func TestDecryptToken_WrongKey(t *testing.T) {
	k1, err := DeriveKey("key-one")
	if err != nil {
		t.Fatalf("DeriveKey(key-one): %v", err)
	}
	k2, err := DeriveKey("key-two")
	if err != nil {
		t.Fatalf("DeriveKey(key-two): %v", err)
	}
	tok, err := EncryptValue(k1, "Email", "corey@cmetech.io")
	if err != nil {
		t.Fatalf("EncryptValue: %v", err)
	}
	payload := tok[len("[PII:Email:") : len(tok)-1]
	if _, err := DecryptToken(k2, "Email", payload); err == nil {
		t.Error("DecryptToken: wrong key should fail; got nil error")
	}
}

func TestDecryptToken_BadBase64(t *testing.T) {
	k, err := DeriveKey("test-key")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if _, err := DecryptToken(k, "Email", "not-valid-base64!!!"); err == nil {
		t.Error("DecryptToken: malformed payload should fail; got nil error")
	}
}

func TestEncryptValue_SamePlaintextDifferentCiphertext(t *testing.T) {
	k, err := DeriveKey("test-key")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	a, err := EncryptValue(k, "Email", "corey@cmetech.io")
	if err != nil {
		t.Fatalf("EncryptValue (first): %v", err)
	}
	b, err := EncryptValue(k, "Email", "corey@cmetech.io")
	if err != nil {
		t.Fatalf("EncryptValue (second): %v", err)
	}
	if a == b {
		t.Error("nonce should randomize each encryption; got identical tokens for the same plaintext")
	}
}
