package pii

import "otto-gateway/internal/privacy"

const UnkeyedHashSentinel = privacy.UnkeyedHashSentinel

func canonicalForm(value string) string {
	return privacy.CanonicalForm(value)
}

// ApplyMode is the standard compatibility wrapper around privacy actions.
func ApplyMode(mode, entity, value string, counter int, hashKey, encryptKey []byte) string {
	return privacy.ApplyAction(privacy.Action(mode), entity, value, counter, hashKey, encryptKey)
}
