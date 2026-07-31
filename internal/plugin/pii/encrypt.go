package pii

import "otto-gateway/internal/privacy"

const (
	tokenPrefix = "[PII:"
	tokenSuffix = "]"
)

// DeriveKey is the standard compatibility wrapper around privacy encryption.
func DeriveKey(value string) ([]byte, error) {
	return privacy.DeriveEncryptionKey(value)
}

// EncryptValue is the standard compatibility wrapper around privacy encryption.
func EncryptValue(key []byte, entity, plaintext string) (string, error) {
	return privacy.EncryptValue(key, entity, plaintext)
}

// DecryptToken is the standard compatibility wrapper around privacy restoration.
func DecryptToken(key []byte, entity, payload string) (string, error) {
	return privacy.DecryptToken(key, entity, payload)
}
