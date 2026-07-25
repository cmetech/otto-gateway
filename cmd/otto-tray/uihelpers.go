//go:build darwin || windows

package main

import "strings"

// informationalStderrPrefixes lists the routine, non-error lines the gw
// wrappers write to stderr on every invocation: scripts/gw load_config and
// scripts/gw.ps1 Initialize-Config. They live on stderr because REL-TRAY-06
// reserves stdout for the support-bundle archive path, so a failing run always
// carries them ahead of the real error. Both wrappers emit these lowercase and
// byte-identical; the prefixes stop at the colon so the one-space and
// two-space spellings both match.
var informationalStderrPrefixes = []string{
	"loaded env file:",
	"loaded overrides:",
}

// firstMeaningfulLine returns the first non-empty trimmed line of s that is
// not one of the wrappers' routine informational lines. When every line is
// informational it falls back to the last non-empty line, and when s holds no
// non-empty line at all it returns "(no stderr)", so notifications always have
// a sensible body.
func firstMeaningfulLine(s string) string {
	lastResort := ""
	for _, line := range strings.Split(s, "\n") {
		// TrimSpace also strips the \r of a CRLF pair, so Windows
		// stderr needs no separate handling.
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lastResort = line
		if !hasInformationalPrefix(line) {
			return line
		}
	}
	if lastResort != "" {
		return lastResort
	}
	return "(no stderr)"
}

// hasInformationalPrefix reports whether the already-trimmed line is one of the
// wrappers' routine informational stderr lines.
func hasInformationalPrefix(line string) bool {
	for _, prefix := range informationalStderrPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

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
