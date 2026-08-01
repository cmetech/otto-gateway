package pii

import (
	"fmt"

	"otto-gateway/internal/privacy"
)

const (
	tokenPrefix = "[PII:"
	tokenSuffix = "]"
)

// DeriveKey is the standard compatibility wrapper around privacy encryption.
func DeriveKey(value string) ([]byte, error) {
	key, err := privacy.DeriveEncryptionKey(value)
	if err != nil {
		return nil, fmt.Errorf("derive PII encryption key: %w", err)
	}
	return key, nil
}

// EncryptValue is the standard compatibility wrapper around privacy encryption.
func EncryptValue(key []byte, entity, plaintext string) (string, error) {
	token, err := privacy.EncryptValue(key, entity, plaintext)
	if err != nil {
		return "", fmt.Errorf("encrypt PII value: %w", err)
	}
	return token, nil
}

// DecryptToken is the standard compatibility wrapper around privacy restoration.
func DecryptToken(key []byte, entity, payload string) (string, error) {
	plaintext, err := privacy.DecryptToken(key, entity, payload)
	if err != nil {
		return "", fmt.Errorf("decrypt PII token: %w", err)
	}
	return plaintext, nil
}
