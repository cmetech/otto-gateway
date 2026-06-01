// PII-ENCRYPT-01 — key derivation tests. DeriveKey hashes any non-empty
// string via SHA-256 into a 32-byte AES-256-GCM key. Empty input is an
// error (encrypt cannot operate without a key).

package pii

import (
	"bytes"
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
	a, _ := DeriveKey("correct-horse-battery-staple")
	b, _ := DeriveKey("correct-horse-battery-staple")
	if !bytes.Equal(a, b) {
		t.Error("DeriveKey is not deterministic across calls with the same input")
	}
}

func TestDeriveKey_DiffersByInput(t *testing.T) {
	a, _ := DeriveKey("hello")
	b, _ := DeriveKey("Hello")
	if bytes.Equal(a, b) {
		t.Error("DeriveKey: case-different inputs produced identical keys")
	}
}
