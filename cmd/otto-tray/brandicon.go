//go:build darwin || windows

package main

import (
	"encoding/json"
	"strings"
)

// resolveBrandJSON returns the installed desktop app's brand.json DisplayName and
// whether brand.json was actually read. Absent (no app, or no/invalid brand.json)
// → ("", false). Uses the OTTO-default candidate paths (installedAppPath), same as
// resolveDesktopIdentity — a loop24 app named LOOP24.app won't match those, which
// is fine: that path yields ("", false) → loop24 icon per brandUsesLoop24 below.
func resolveBrandJSON(goos string, env func(string) string, home string, exists func(string) bool, readFile func(string) ([]byte, error)) (string, bool) {
	base := defaultBrandIdentity()
	appPath := installedAppPath(goos, base, env, home, exists)
	if appPath == "" {
		return "", false
	}
	data, err := readFile(brandJSONPathForApp(goos, appPath))
	if err != nil {
		return "", false
	}
	var doc brandJSONDoc
	if json.Unmarshal(data, &doc) != nil || !validateDisplayName(doc.DisplayName) {
		return "", false
	}
	return doc.DisplayName, true
}

// brandUsesLoop24 reports whether the tray should use the loop24 icon. The OTTO
// icon is used ONLY when brand.json is present AND names OTTO; every other case —
// brand.json absent (desktop not installed yet; the tray ships with the gateway
// and can precede it) or brand != OTTO — uses the loop24 icon.
func brandUsesLoop24(goos string, env func(string) string, home string, exists func(string) bool, readFile func(string) ([]byte, error)) bool {
	name, present := resolveBrandJSON(goos, env, home, exists, readFile)
	return !present || !strings.EqualFold(name, "OTTO")
}
