package privacy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

const maxEncodedReceiptBytes = 512

// Receipt is the fixed-order value-free privacy receipt payload.
type Receipt struct {
	Version     int     `json:"version"`
	Profile     Profile `json:"profile"`
	Scope       string  `json:"scope"`
	Coverage    string  `json:"coverage"`
	Result      string  `json:"result"`
	Transformed int     `json:"transformed"`
	Restored    int     `json:"restored"`
	Blocked     int     `json:"blocked"`
}

func encodeReceipt(receipt Receipt) (string, error) {
	payload, err := json.Marshal(receipt)
	if err != nil {
		return "", errors.New("privacy receipt encoding failed")
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if len(encoded) > maxEncodedReceiptBytes {
		return "", errors.New("privacy receipt exceeds maximum size")
	}
	return encoded, nil
}
