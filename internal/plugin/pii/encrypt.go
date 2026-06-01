// Package pii encrypt — AES-256-GCM round-trip support for the
// PII_REDACTION_MODE=encrypt action (plus per-entity EntityActions
// overrides). Pure helpers; no package-level state.
//
// Spec: docs/superpowers/specs/2026-06-01-pii-encrypt-design.md
//
// Token shape on the wire toward the LLM:
//
//	[PII:Entity:base64url(nonce || ciphertext || tag)]
//
// where Entity is bound as GCM Associated Data so a relabeled token
// fails to decrypt (Open returns an error and the Post hook leaves
// the token verbatim).

package pii

import (
	"crypto/sha256"
	"errors"
)

// DeriveKey returns the 32-byte AES-256-GCM key derived from any
// non-empty string via SHA-256. Deterministic across restarts so the
// same PII_ENCRYPT_KEY env value reproduces the same key — tokens
// encrypted before a restart still decrypt after.
//
// SHA-256 (not scrypt/argon2) is chosen because our threat model is
// "LLM provider sees plaintext", not offline ciphertext brute-force.
// See spec §3.5 for rationale.
func DeriveKey(envValue string) ([]byte, error) {
	if envValue == "" {
		return nil, errors.New("pii: PII_ENCRYPT_KEY is empty")
	}
	sum := sha256.Sum256([]byte(envValue))
	return sum[:], nil
}
