package pii

import "otto-gateway/internal/privacy"

// UnkeyedHashSentinel marks hash output produced without a configured key.
const UnkeyedHashSentinel = privacy.UnkeyedHashSentinel

// ApplyMode is the standard compatibility wrapper around privacy actions.
func ApplyMode(mode, entity, value string, counter int, hashKey, encryptKey []byte) string {
	return privacy.ApplyAction(privacy.Action(mode), entity, value, counter, hashKey, encryptKey)
}
