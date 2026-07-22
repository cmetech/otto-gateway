//go:build darwin || windows

package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

// brandIdentity is everything the desktop actions need to know about the
// installed desktop app. Validated descriptors drive desktop actions only;
// they never influence the tray's fixed Gateway icon.
type brandIdentity struct {
	DisplayName string // "OTTO"
	WinExeName  string // "OTTO.exe"
	MacAppName  string // "OTTO.app"
	InstallRepo string // "cmetech/otto"
}

// displayNameRe bounds a brand display name to safe characters BEFORE it is
// used to build a process name / kill target passed to exec (gosec G204).
var displayNameRe = regexp.MustCompile(`^[A-Za-z0-9 ._-]{1,64}$`)

type brandDescriptor struct {
	SchemaVersion int    `json:"schemaVersion"`
	Slug          string `json:"slug"`
	DisplayName   string `json:"displayName"`
	HomeDir       string `json:"homeDir"`
	Gateway       string `json:"gateway"`
	ReleasesRepo  string `json:"releasesRepo"`
}

var brandSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func validateDisplayName(name string) bool { return displayNameRe.MatchString(name) }

func parseBrandDescriptor(data []byte) (brandDescriptor, bool) {
	var doc brandDescriptor
	if json.Unmarshal(data, &doc) != nil ||
		doc.SchemaVersion != 1 ||
		!brandSlugRe.MatchString(doc.Slug) ||
		!validateDisplayName(doc.DisplayName) ||
		doc.HomeDir != "."+doc.Slug ||
		!strings.EqualFold(doc.Gateway, "otto") {
		return brandDescriptor{}, false
	}
	return doc, true
}

func identityFromDisplayName(name string) brandIdentity {
	return brandIdentity{
		DisplayName: name,
		WinExeName:  name + ".exe",
		MacAppName:  name + ".app",
		InstallRepo: "cmetech/otto",
	}
}

func defaultBrandIdentity() brandIdentity { return identityFromDisplayName("OTTO") }
