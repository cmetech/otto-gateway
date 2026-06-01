// AES-256-GCM round-trip support for the PII_REDACTION_MODE=encrypt
// action (plus per-entity EntityActions overrides). Pure helpers;
// no package-level state.
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
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
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

// tokenPrefix and tokenSuffix bracket the wire token. Square brackets
// (NOT angle brackets) per commit a3160e1 — angle brackets cause
// kiro-cli / Claude to treat the marker as an opening XML tag and
// hang the ACP prompt until the 120s timeout.
const (
	tokenPrefix = "[PII:"
	tokenSuffix = "]"
)

// EncryptValue encrypts plaintext under key with AES-256-GCM, binds
// entity as Associated Data (so a relabeled token fails Open), and
// returns the full wire-shaped token "[PII:<entity>:<base64url>]".
// Each call generates a fresh nonce, so two calls with the same
// plaintext produce different tokens (both decrypt correctly).
func EncryptValue(key []byte, entity, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("pii: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("pii: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("pii: rand: %w", err)
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintext), []byte(entity))
	blob := make([]byte, 0, len(nonce)+len(ct))
	blob = append(blob, nonce...)
	blob = append(blob, ct...)
	payload := base64.RawURLEncoding.EncodeToString(blob)
	return tokenPrefix + entity + ":" + payload + tokenSuffix, nil
}

// DecryptToken inverts EncryptValue. payload is the base64url-encoded
// blob between "[PII:<entity>:" and "]". entity is passed as AAD and
// must match what was used at encrypt time. Returns an error on any
// failure: bad base64, payload too short for the nonce, or GCM Open
// failure (wrong key, AAD mismatch, tag corruption).
func DecryptToken(key []byte, entity, payload string) (string, error) {
	blob, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("pii: bad base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("pii: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("pii: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(blob) < ns {
		return "", fmt.Errorf("pii: payload too short")
	}
	nonce, ct := blob[:ns], blob[ns:]
	pt, err := gcm.Open(nil, nonce, ct, []byte(entity))
	if err != nil {
		return "", fmt.Errorf("pii: gcm open: %w", err)
	}
	return string(pt), nil
}
