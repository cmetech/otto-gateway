// Package engine — per-request cwd derivation (D-16).
//
// pickCwd derives the working directory the ACP session is bound to, via
// a four-step priority chain matching the Node reference's intent but
// fixing its Windows defect (RESEARCH.md Pitfall 3 — Node's regex
// produces "/C:/path" on Windows, which is not a valid OS path; the Go
// implementation here uses runtime.GOOS plus filepath.FromSlash to do
// the right thing on both POSIX and Windows).
package engine

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"otto-gateway/internal/canonical"
)

// pickCwd derives the per-request working directory using D-16's priority:
//
//  1. req.WorkingDirOverride (from X-Working-Dir header)
//  2. longest common parent of file:// URIs in req.ResourceLinks (Codex
//     H-2 — sourced from the ResourceLinks field; see extractFileURIs
//     doc-comment for the prior-walk-of-content-parts defect this
//     fixes)
//  3. defaultCwd (typically cfg.DefaultCWD == config.Config.KiroCWD)
//  4. os.Getwd()
//
// Pure function: the only side effect is os.Getwd at the bottom of the
// fallback chain. Property-tested per TRST-06 via testing/quick to
// assert it never panics on any input.
func pickCwd(req *canonical.ChatRequest, defaultCwd string) string {
	if req != nil && req.WorkingDirOverride != "" {
		return req.WorkingDirOverride
	}
	if parent := longestCommonParent(extractFileURIs(req)); parent != "" {
		return parent
	}
	if defaultCwd != "" {
		return defaultCwd
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

// extractFileURIs returns the OS-corrected paths of every file:// URI in
// req.ResourceLinks. Phase 2 Ollama keeps req.ResourceLinks zero-valued
// (the Ollama wire has no resource_link content blocks); Phase 3.1
// Anthropic populates it from incoming resource_link content blocks;
// Plan 04 handler-level tests populate it directly to exercise SC #5.
//
// Per Codex review H-2 (2026-05-23): the prior design (which sourced
// URIs from the request's Messages content parts) had no ContentKind
// carrying resource_link, making SC #5 structurally unsatisfiable. The
// fix is to source from the ResourceLinks field that canonical.ChatRequest
// gained in Plan 01 specifically so this walk has a real input.
//
// Non-file URIs (http://, https://, etc.) are silently skipped per
// D-16. Parse failures are also silently skipped (defensive — pickCwd
// must never panic, and a bad URI is not worth aborting cwd derivation
// over).
func extractFileURIs(req *canonical.ChatRequest) []string {
	if req == nil || len(req.ResourceLinks) == 0 {
		return nil
	}
	out := make([]string, 0, len(req.ResourceLinks))
	for _, link := range req.ResourceLinks {
		u, err := url.Parse(link.URI)
		if err != nil || u.Scheme != "file" {
			continue
		}
		p := u.Path
		// Windows file URI handling — fixes the Node defect documented in
		// RESEARCH.md Pitfall 3. file:///C:/foo arrives with u.Path
		// == "/C:/foo"; on Windows the leading slash must be stripped
		// so the result is "C:/foo", which filepath.FromSlash will
		// then convert to "C:\foo". Non-Windows paths pass through
		// unchanged.
		if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		out = append(out, filepath.FromSlash(p))
	}
	return out
}

// longestCommonParent returns the longest directory path shared by every
// path in paths (after path-separator normalisation). Empty input → "".
// Single input → filepath.Dir of that path. Inputs with no common parent
// → "".
//
// Implementation strategy: extract each path's directory via
// filepath.Dir, split on the OS separator, walk components in parallel
// until they diverge. Rejoin via filepath.Join.
func longestCommonParent(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		return filepath.Dir(paths[0])
	}

	// Collect directory-component slices for each path.
	dirs := make([][]string, 0, len(paths))
	for _, p := range paths {
		d := filepath.Dir(p)
		dirs = append(dirs, splitPath(d))
	}

	common := sharedPrefix(dirs)
	if len(common) == 0 {
		return ""
	}

	// Rejoin. On Unix the first element may be "" if the path was
	// absolute (e.g. "/usr/local" → ["", "usr", "local"]); preserve
	// that so the result starts with "/".
	return joinPathComponents(common)
}

// splitPath splits a directory path into its component pieces.
// On Unix "/usr/local" → ["", "usr", "local"] (leading "" preserves
// the absolute-root prefix). On Windows "C:\foo\bar" → ["C:", "foo",
// "bar"] (volume name retained as the first element).
func splitPath(p string) []string {
	// Normalize: trim trailing separator (filepath.Dir already does
	// this, but be defensive).
	p = strings.TrimRight(p, string(filepath.Separator))
	if p == "" {
		return nil
	}
	parts := strings.Split(p, string(filepath.Separator))
	return parts
}

// sharedPrefix returns the longest slice of leading components common
// to every element of slices. Empty input → empty result.
func sharedPrefix(slices [][]string) []string {
	if len(slices) == 0 {
		return nil
	}
	// Find min length.
	minLen := len(slices[0])
	for _, s := range slices[1:] {
		if len(s) < minLen {
			minLen = len(s)
		}
	}
	out := make([]string, 0, minLen)
	for i := 0; i < minLen; i++ {
		ref := slices[0][i]
		ok := true
		for _, s := range slices[1:] {
			if s[i] != ref {
				ok = false
				break
			}
		}
		if !ok {
			break
		}
		out = append(out, ref)
	}
	return out
}

// joinPathComponents rejoins a slice produced by splitPath into a
// filesystem path, preserving an absolute-root prefix when the first
// element is "" (Unix-rooted paths).
func joinPathComponents(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	// Unix-rooted: first element is "", rejoin with a leading separator.
	if parts[0] == "" {
		if len(parts) == 1 {
			// Just the root.
			return string(filepath.Separator)
		}
		return string(filepath.Separator) + filepath.Join(parts[1:]...)
	}
	return filepath.Join(parts...)
}
