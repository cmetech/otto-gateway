//go:build darwin || windows

package main

import "regexp"

// brandIdentity is everything the desktop actions need to know about the
// installed desktop app. The tray uses fixed OTTO defaults and deliberately
// does NOT read the app's brand.json — that descriptor belongs to the desktop
// Hermes client, which owns and consumes it (the tray reading it used to drive
// a spurious icon swap; see quick task 260721-an5).
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
