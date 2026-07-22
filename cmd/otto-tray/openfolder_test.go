//go:build darwin || windows

package main

import (
	"errors"
	"path/filepath"
	"testing"
)

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
	noneExists := func(string) bool { return false }

	// env HERMES_HOME wins on every OS.
	env := func(k string) string {
		if k == "HERMES_HOME" {
			return "/custom/home"
		}
		return ""
	}
	if got := resolveHermesHome("darwin", env, "/Users/me", "otto", ".otto", noReg, noneExists); got != "/custom/home" {
		t.Errorf("env override: got %q", got)
	}

	// darwin default comes from the descriptor-backed home directory.
	if got := resolveHermesHome("darwin", func(string) string { return "" }, "/Users/me", "loop24", ".loop24", noReg, noneExists); got != filepath.Join("/Users/me", ".loop24") {
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
	if got := resolveHermesHome("windows", winEnv, `C:\Users\me`, "otto", ".otto", reg, noneExists); got != `D:\hermes` {
		t.Errorf("windows registry: got %q", got)
	}

	// windows default → LOCALAPPDATA\slug
	if got := resolveHermesHome("windows", winEnv, `C:\Users\me`, "otto", ".otto", noReg, noneExists); got != filepath.Join(`C:\Users\me\AppData\Local`, "otto") {
		t.Errorf("windows default: got %q", got)
	}

	// windows USERPROFILE fallback when LOCALAPPDATA unset
	upEnv := func(k string) string {
		if k == "USERPROFILE" {
			return `C:\Users\me`
		}
		return ""
	}
	if got := resolveHermesHome("windows", upEnv, `C:\Users\me`, "otto", ".otto", noReg, noneExists); got != filepath.Join(`C:\Users\me`, "AppData", "Local", "otto") {
		t.Errorf("windows USERPROFILE fallback: got %q", got)
	}
}

func TestResolveHermesHomeRejectsForeignPopulatedDefault(t *testing.T) {
	local := filepath.Join("C:", "Users", "me", "AppData", "Local")
	foreign := filepath.Join(local, "otto")
	env := func(k string) string {
		if k == "LOCALAPPDATA" {
			return local
		}
		if k == "HERMES_HOME" {
			return foreign
		}
		return ""
	}
	exists := func(path string) bool { return path == filepath.Join(foreign, "hermes-agent") }
	got := resolveHermesHome("windows", env, "", "loop24", ".loop24", func(string) string { return "" }, exists)
	if got != filepath.Join(local, "loop24") {
		t.Fatalf("got %q", got)
	}
}

func TestResolveHermesHomeWindowsBrandSafety(t *testing.T) {
	local := filepath.Join("C:", "Users", "me", "AppData", "Local")
	selected := filepath.Join(local, "loop24")
	foreign := filepath.Join(local, "otto")
	external := filepath.Join("D:", "Hermes", "shared")
	tests := []struct {
		name   string
		env    string
		reg    string
		exists func(string) bool
		want   string
	}{
		{name: "selected local default", env: selected, exists: func(path string) bool { return path == filepath.Join(selected, "hermes-agent") }, want: selected},
		{name: "external environment custom", env: external, exists: func(path string) bool { return path == filepath.Join(external, "hermes-agent") }, want: external},
		{name: "external registry custom", reg: external, exists: func(path string) bool { return path == filepath.Join(external, "hermes-agent") }, want: external},
		{name: "unpopulated foreign environment", env: foreign, exists: func(string) bool { return false }, want: foreign},
		{name: "foreign environment falls through to custom registry", env: foreign, reg: external, exists: func(path string) bool { return path == filepath.Join(foreign, "hermes-agent") }, want: external},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := func(key string) string {
				switch key {
				case "LOCALAPPDATA":
					return local
				case "HERMES_HOME":
					return tc.env
				default:
					return ""
				}
			}
			reg := func(key string) string {
				if key == "HERMES_HOME" {
					return tc.reg
				}
				return ""
			}
			if got := resolveHermesHome("windows", env, "", "loop24", ".loop24", reg, tc.exists); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunningDesktopCandidateRejectsStaleSnapshot(t *testing.T) {
	candidate := &desktopCandidate{Identity: identityFromDisplayName("LOOP24"), Slug: "loop24"}
	if _, err := runningDesktopCandidate(&desktopOutput{State: DesktopStopped, Candidate: candidate}, func(brandIdentity) (bool, error) {
		t.Fatal("stopped snapshot must not invoke liveness probe")
		return false, nil
	}); err == nil {
		t.Fatal("stopped candidate was actionable")
	}
	if _, err := runningDesktopCandidate(&desktopOutput{State: DesktopRunning, Candidate: candidate}, func(brandIdentity) (bool, error) {
		return false, nil
	}); err == nil {
		t.Fatal("stale snapshot was actionable")
	}
	wantErr := errors.New("process enumeration failed")
	if _, err := runningDesktopCandidate(&desktopOutput{State: DesktopRunning, Candidate: candidate}, func(brandIdentity) (bool, error) {
		return false, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("liveness error = %v, want %v", err, wantErr)
	}
}

func TestRunOpenDesktopFolderDoesNotOpenAfterFailedRevalidation(t *testing.T) {
	candidate := &desktopCandidate{Identity: identityFromDisplayName("LOOP24"), Slug: "loop24", HomeDir: ".loop24", AppPath: "/Applications/LOOP24.app"}
	opened := false
	err := runOpenDesktopFolder(
		desktopAppFolder,
		&desktopOutput{State: DesktopRunning, Candidate: candidate},
		"darwin",
		func(string) string { return "" },
		"/Users/me",
		func(string) string { return "" },
		func(string) bool { return true },
		func(brandIdentity) (bool, error) { return false, nil },
		func(string, bool) error { opened = true; return nil },
	)
	if err == nil {
		t.Fatal("expected stale snapshot error")
	}
	if opened {
		t.Fatal("opener called after failed revalidation")
	}
}

func TestRunOpenDesktopFolderUsesResolvedCandidatePaths(t *testing.T) {
	candidate := &desktopCandidate{Identity: identityFromDisplayName("LOOP24"), Slug: "loop24", HomeDir: ".loop24", AppPath: "/Applications/LOOP24.app"}
	out := &desktopOutput{State: DesktopRunning, Candidate: candidate}
	running := func(id brandIdentity) (bool, error) {
		if id.DisplayName != "LOOP24" {
			t.Fatalf("liveness identity = %q", id.DisplayName)
		}
		return true, nil
	}
	var openedPath string
	var reveal bool
	opener := func(path string, shouldReveal bool) error {
		openedPath, reveal = path, shouldReveal
		return nil
	}

	if err := runOpenDesktopFolder(desktopAppFolder, out, "darwin", func(string) string { return "" }, "/Users/me", func(string) string { return "" }, func(string) bool { return true }, running, opener); err != nil {
		t.Fatalf("app folder: %v", err)
	}
	if openedPath != candidate.AppPath || !reveal {
		t.Fatalf("app open = (%q, %v), want (%q, true)", openedPath, reveal, candidate.AppPath)
	}

	dataPath := filepath.Join("/Users/me", candidate.HomeDir)
	openedPath, reveal = "", true
	if err := runOpenDesktopFolder(desktopDataFolder, out, "darwin", func(string) string { return "" }, "/Users/me", func(string) string { return "" }, func(path string) bool { return path == dataPath }, running, opener); err != nil {
		t.Fatalf("data folder: %v", err)
	}
	if openedPath != dataPath || reveal {
		t.Fatalf("data open = (%q, %v), want (%q, false)", openedPath, reveal, dataPath)
	}
}
