package privacy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
)

// Action selects how a finding is transformed.
type Action string

// Supported Action values select the standard and strict transformations.
const (
	ActionReplace      Action = "replace"
	ActionMask         Action = "mask"
	ActionHash         Action = "hash"
	ActionDrop         Action = "drop"
	ActionEncrypt      Action = "encrypt"
	ActionPseudonymize Action = "pseudonymize"
)

// Hash and encrypted-token constants define privacy wire-format details.
const (
	hashTagLength        = 8
	UnkeyedHashSentinel  = "UNKEYED"
	encryptedTokenPrefix = "[PII:"
	encryptedTokenSuffix = "]"
)

var (
	emptyHashKeyWarning sync.Once
	encryptedTokenRE    = regexp.MustCompile(`^\[PII:([A-Za-z0-9_]+):([A-Za-z0-9_-]+)\]$`)
)

// CanonicalForm normalizes standard-mode correlation inputs.
func CanonicalForm(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// ApplyAction preserves the five standard PII action wire formats.
func ApplyAction(action Action, entity, value string, counter int, hashKey, encryptKey []byte) string {
	upperEntity := strings.ToUpper(entity)
	switch action {
	case ActionReplace:
		return replacementToken(upperEntity, counter)
	case ActionMask:
		return maskValue(value)
	case ActionHash:
		return fmt.Sprintf("[%s:h-%s]", upperEntity, hashTag(hashKey, value))
	case ActionDrop:
		return ""
	case ActionEncrypt:
		token, err := EncryptValue(encryptKey, entity, value)
		if err != nil {
			slog.Default().Warn("privacy.action.encrypt_failed", "entity", entity, "err", err)
			return replacementToken(upperEntity, counter)
		}
		return token
	default:
		slog.Default().Warn("privacy.action.unknown", "action", action)
		return replacementToken(upperEntity, counter)
	}
}

func replacementToken(entity string, counter int) string {
	if counter > 0 {
		return fmt.Sprintf("[%s_%d]", entity, counter)
	}
	return fmt.Sprintf("[%s]", entity)
}

func hashTag(hashKey []byte, value string) string {
	if len(hashKey) == 0 {
		emptyHashKeyWarning.Do(func() {
			slog.Default().Warn("privacy.action.hash_empty_key", "sentinel", UnkeyedHashSentinel)
		})
		return UnkeyedHashSentinel
	}
	mac := hmac.New(sha256.New, hashKey)
	_, _ = mac.Write([]byte(CanonicalForm(value)))
	return hex.EncodeToString(mac.Sum(nil))[:hashTagLength]
}

func maskValue(value string) string {
	if at := strings.IndexByte(value, '@'); at > 0 {
		local := value[:at]
		domain := value[at+1:]
		maskedLocal := maskPrefix(local, 2)
		dot := strings.LastIndexByte(domain, '.')
		if dot > 0 {
			return maskedLocal + "@" + maskPrefix(domain[:dot], 2) + domain[dot:]
		}
		return maskedLocal + "@" + maskPrefix(domain, 2)
	}
	if len(value) < 5 {
		return "****"
	}
	return value[:2] + strings.Repeat("*", len(value)-4) + value[len(value)-2:]
}

func maskPrefix(value string, length int) string {
	if len(value) <= length {
		return "***"
	}
	return value[:length] + "***"
}

// DeriveEncryptionKey derives the standard AES-256-GCM key.
func DeriveEncryptionKey(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("privacy encryption key is empty")
	}
	sum := sha256.Sum256([]byte(value))
	return sum[:], nil
}

// EncryptValue creates the standard wrapped AES-GCM token.
func EncryptValue(key []byte, entity, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("privacy aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("privacy gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("privacy rand: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), []byte(entity))
	blob := append(nonce, ciphertext...)
	payload := base64.RawURLEncoding.EncodeToString(blob)
	return encryptedTokenPrefix + entity + ":" + payload + encryptedTokenSuffix, nil
}

// DecryptToken restores one standard AES-GCM payload.
func DecryptToken(key []byte, entity, payload string) (string, error) {
	blob, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("privacy bad base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("privacy aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("privacy gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(blob) < nonceSize {
		return "", errors.New("privacy payload too short")
	}
	plaintext, err := gcm.Open(nil, blob[:nonceSize], blob[nonceSize:], []byte(entity))
	if err != nil {
		return "", fmt.Errorf("privacy gcm open: %w", err)
	}
	return string(plaintext), nil
}

// ParseEncryptedToken splits a complete standard wrapped token.
func ParseEncryptedToken(token string) (entity, payload string, ok bool) {
	match := encryptedTokenRE.FindStringSubmatch(token)
	if len(match) != 3 {
		return "", "", false
	}
	return match[1], match[2], true
}

// OneWaySecretLabel returns a scoped label without retaining canonical.
func OneWaySecretLabel(aliasKey []byte, scopeID, entity, canonical string) string {
	mac := hmac.New(sha256.New, aliasKey)
	fields := [][]byte{[]byte(scopeID), []byte(entity), []byte(canonical)}
	for _, field := range fields {
		if writeLengthPrefixed(mac, field) {
			continue
		}

		// Preserve unambiguous framing on 64-bit systems even if a caller
		// supplies a field larger than the ordinary uint32 wire length.
		mac = hmac.New(sha256.New, aliasKey)
		for _, wideField := range fields {
			writeWideLengthPrefixed(mac, wideField)
		}
		break
	}
	tag := strings.ToUpper(hex.EncodeToString(mac.Sum(nil))[:12])
	return fmt.Sprintf("[SECRET:%s_%s]", strings.ToUpper(entity), tag)
}
