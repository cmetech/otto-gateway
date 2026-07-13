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
				out = append(out,
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

// brandJSONPathForApp returns where brand.json ships in the packaged app's
// resources (electron-builder extraResources), relative to the launchable
// path returned by installedAppPath.
func brandJSONPathForApp(goos, appPath string) string {
	if goos == "darwin" {
		return filepath.Join(appPath, "Contents", "Resources", "brand.json")
	}
	// windows: appPath = ...\<Brand>\<Brand>.exe → resources\brand.json beside it
	return filepath.Join(filepath.Dir(appPath), "resources", "brand.json")
}

// resolveDesktopIdentity finds the installed app (with OTTO defaults) and, if
// found, refines the identity from its bundled brand.json. Returns the
// (possibly refined) identity and the app path ("" if not installed).
//
//nolint:unparam // goos is runtime.GOOS in production (varies darwin/windows across builds) and is parameterized so both OS branches are unit-tested on one box
func resolveDesktopIdentity(goos string, env func(string) string, home string, exists func(string) bool, readFile func(string) ([]byte, error)) (brandIdentity, string) {
	id := defaultBrandIdentity()
	appPath := installedAppPath(goos, id, env, home, exists)
	if appPath == "" {
		return id, ""
	}
	id = refineBrandIdentity(id, brandJSONPathForApp(goos, appPath), readFile)
	// Re-resolve the path under the refined identity (name may have changed).
	if p := installedAppPath(goos, id, env, home, exists); p != "" {
		appPath = p
	}
	return id, appPath
}
