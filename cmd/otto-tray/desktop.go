//go:build darwin || windows

package main

import "path/filepath"

// desktopAppCandidates returns launchable-path candidates, most-preferred
// first. Pure (branches on goos) so both OSes are tested on one box.
func desktopAppCandidates(goos string, id brandIdentity, env func(string) string, home string) []string {
	switch goos {
	case "windows":
		var out []string
		for _, k := range []string{"LOCALAPPDATA", "PROGRAMFILES", "PROGRAMFILES(X86)"} {
			if root := env(k); root != "" {
				out = append(
					out,
					filepath.Join(root, "Programs", id.DisplayName, id.WinExeName),
					filepath.Join(root, id.DisplayName, id.WinExeName),
				)
			}
		}
		return out
	default: // darwin
		return []string{
			filepath.Join("/Applications", id.MacAppName),
			filepath.Join(home, "Applications", id.MacAppName),
		}
	}
}

// installedAppPath returns the first existing candidate, or "".
func installedAppPath(goos string, id brandIdentity, env func(string) string, home string, exists func(string) bool) string {
	for _, c := range desktopAppCandidates(goos, id, env, home) {
		if exists(c) {
			return c
		}
	}
	return ""
}

// resolveDesktopIdentity finds the installed desktop app using fixed OTTO
// defaults and returns that identity plus the app path ("" if not installed).
// It deliberately does NOT read the app's brand.json: that descriptor is owned
// by the desktop Hermes client, and the tray reading it previously caused a
// spurious icon swap (quick task 260721-an5). Discovery already relies on the
// OTTO-default path/exe name, so dropping the brand.json refinement changes no
// behavior for the current app.
//
//nolint:unparam // goos is runtime.GOOS in production (varies darwin/windows across builds) and is parameterized so both OS branches are unit-tested on one box
func resolveDesktopIdentity(goos string, env func(string) string, home string, exists func(string) bool) (brandIdentity, string) {
	id := defaultBrandIdentity()
	return id, installedAppPath(goos, id, env, home, exists)
}
