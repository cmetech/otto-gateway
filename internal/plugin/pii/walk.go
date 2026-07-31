package pii

import "otto-gateway/internal/privacy"

// WalkStrings is the standard compatibility wrapper around privacy traversal.
func WalkStrings(value any, transform func(string) string) any {
	return privacy.WalkStrings(value, transform)
}
