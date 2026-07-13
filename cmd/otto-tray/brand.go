//go:build darwin || windows

package main

import (
	"encoding/json"
	"regexp"
)

// brandIdentity is everything the desktop actions need to know about the
// brand. Defaults are OTTO; refineBrandIdentity may overlay a display name
// from the installed app's brand.json (Plan-6 discoverable descriptor).
type brandIdentity struct {
	DisplayName  string // "OTTO"
	WinExeName   string // "OTTO.exe"
	MacAppName   string // "OTTO.app"
	MacProcMatch string // "OTTO.app/Contents/MacOS/OTTO"
	InstallRepo  string // "cmetech/otto"
}

// displayNameRe bounds a brand display name to safe characters BEFORE it is
// used to build a process name / kill target passed to exec (gosec G204).
var displayNameRe = regexp.MustCompile(`^[A-Za-z0-9 ._-]{1,64}$`)

func validateDisplayName(name string) bool { return displayNameRe.MatchString(name) }

func identityFromDisplayName(name string) brandIdentity {
	return brandIdentity{
		DisplayName:  name,
		WinExeName:   name + ".exe",
		MacAppName:   name + ".app",
		MacProcMatch: name + ".app/Contents/MacOS/" + name,
		InstallRepo:  "cmetech/otto",
	}
}

func defaultBrandIdentity() brandIdentity { return identityFromDisplayName("OTTO") }

// brandJSONDoc is the subset of the Plan-6 brand.json the tray consumes.
type brandJSONDoc struct {
	DisplayName  string `json:"displayName"`
	ReleasesRepo string `json:"releasesRepo"`
}

// refineBrandIdentity overlays a validated brand.json onto the defaults.
// Any error / invalid content keeps the base identity (fail-safe).
func refineBrandIdentity(base brandIdentity, brandJSONPath string, readFile func(string) ([]byte, error)) brandIdentity {
	data, err := readFile(brandJSONPath)
	if err != nil {
		return base
	}
	var doc brandJSONDoc
	if json.Unmarshal(data, &doc) != nil {
		return base
	}
	out := base
	if validateDisplayName(doc.DisplayName) {
		out = identityFromDisplayName(doc.DisplayName)
	}
	if doc.ReleasesRepo != "" {
		out.InstallRepo = doc.ReleasesRepo // used for display only in v1; install URL is constant
	}
	return out
}
