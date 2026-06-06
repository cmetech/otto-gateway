//go:build darwin || windows

package main

import "strings"

// firstLine returns the first non-empty trimmed line of s; falls
// back to "(no stderr)" when s is empty so notifications always have
// a sensible body.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "(no stderr)"
	}
	return s
}
