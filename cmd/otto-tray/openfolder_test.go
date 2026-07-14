//go:build darwin || windows

package main

import (
	"path/filepath"
	"testing"
)

func TestBrandSlug(t *testing.T) {
	for _, tc := range []struct{ display, want string }{
		{"OTTO", "otto"},
		{"LOOP24", "loop24"},
		{"Otto", "otto"},
	} {
		if got := brandSlug(brandIdentity{DisplayName: tc.display}); got != tc.want {
			t.Errorf("brandSlug(%q) = %q, want %q", tc.display, got, tc.want)
		}
	}
}

func TestFileManagerCommand(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		path     string
		reveal   bool
		wantName string
		wantArgs []string
	}{
		{"win dir", "windows", `C:\x\OTTO`, false, "explorer", []string{`C:\x\OTTO`}},
		{"win reveal", "windows", `C:\x\OTTO\OTTO.exe`, true, "explorer", []string{`/select,C:\x\OTTO\OTTO.exe`}},
		{"darwin dir", "darwin", "/Users/me/.otto", false, "open", []string{"/Users/me/.otto"}},
		{"darwin reveal", "darwin", "/Applications/OTTO.app", true, "open", []string{"-R", "/Applications/OTTO.app"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, args := fileManagerCommand(tc.goos, tc.path, tc.reveal)
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tc.wantArgs)
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Errorf("args[%d] = %q, want %q", i, args[i], tc.wantArgs[i])
				}
			}
		})
	}
}

func TestAppFolderTarget(t *testing.T) {
	// Use filepath.Join so the expected dir matches appFolderTarget's host-separator
	// filepath.Dir on any host (in production runtime.GOOS always equals the host).
	winExe := filepath.Join("C:", "x", "OTTO", "OTTO.exe")
	winDir := filepath.Join("C:", "x", "OTTO")
	if target, reveal := appFolderTarget("windows", winExe); target != winDir || reveal {
		t.Errorf("windows: got (%q, %v), want (%q, false)", target, reveal, winDir)
	}
	if target, reveal := appFolderTarget("darwin", "/Applications/OTTO.app"); target != "/Applications/OTTO.app" || !reveal {
		t.Errorf("darwin: got (%q, %v), want (/Applications/OTTO.app, true)", target, reveal)
	}
}

func TestResolveHermesHome(t *testing.T) {
	noReg := func(string) string { return "" }

	// env HERMES_HOME wins on every OS.
	env := func(k string) string {
		if k == "HERMES_HOME" {
			return "/custom/home"
		}
		return ""
	}
	if got := resolveHermesHome("darwin", env, "/Users/me", "otto", noReg); got != "/custom/home" {
		t.Errorf("env override: got %q", got)
	}

	// darwin default → home/.slug
	if got := resolveHermesHome("darwin", func(string) string { return "" }, "/Users/me", "loop24", noReg); got != filepath.Join("/Users/me", ".loop24") {
		t.Errorf("darwin default: got %q", got)
	}

	// windows registry hit wins over default
	reg := func(name string) string {
		if name == "HERMES_HOME" {
			return `D:\hermes`
		}
		return ""
	}
	winEnv := func(k string) string {
		if k == "LOCALAPPDATA" {
			return `C:\Users\me\AppData\Local`
		}
		return ""
	}
	if got := resolveHermesHome("windows", winEnv, `C:\Users\me`, "otto", reg); got != `D:\hermes` {
		t.Errorf("windows registry: got %q", got)
	}

	// windows default → LOCALAPPDATA\slug
	if got := resolveHermesHome("windows", winEnv, `C:\Users\me`, "otto", noReg); got != filepath.Join(`C:\Users\me\AppData\Local`, "otto") {
		t.Errorf("windows default: got %q", got)
	}

	// windows USERPROFILE fallback when LOCALAPPDATA unset
	upEnv := func(k string) string {
		if k == "USERPROFILE" {
			return `C:\Users\me`
		}
		return ""
	}
	if got := resolveHermesHome("windows", upEnv, `C:\Users\me`, "otto", noReg); got != filepath.Join(`C:\Users\me`, "AppData", "Local", "otto") {
		t.Errorf("windows USERPROFILE fallback: got %q", got)
	}
}
