//go:build darwin || windows

package main

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestDesktopInstallCommand(t *testing.T) {
	n, a := desktopInstallCommand("windows")
	if n != "powershell" || !strings.Contains(strings.Join(a, " "), "cmetech/otto/main/install.ps1") {
		t.Fatalf("win install: %s %v", n, a)
	}
	n, a = desktopInstallCommand("darwin")
	if n != "/bin/sh" || !strings.Contains(strings.Join(a, " "), "cmetech/otto/main/install.sh") {
		t.Fatalf("mac install: %s %v", n, a)
	}
}

func TestDesktopStartCommand(t *testing.T) {
	n, a := desktopStartCommand("darwin", "/Applications/OTTO.app")
	if n != "open" || len(a) != 1 || a[0] != "/Applications/OTTO.app" {
		t.Fatalf("mac start: %s %v", n, a)
	}
	n, a = desktopStartCommand("windows", `C:\P\OTTO\OTTO.exe`)
	if n != `C:\P\OTTO\OTTO.exe` || len(a) != 0 {
		t.Fatalf("win start: %s %v", n, a)
	}
}

func TestDesktopStopCommand(t *testing.T) {
	candidate := desktopCandidate{
		Identity:       identityFromDisplayName("LOOP.24"),
		AppPath:        "/Applications/LOOP.24.app",
		ExecutablePath: "/Applications/LOOP.24.app/Contents/MacOS/LOOP.24",
	}
	n, a := desktopStopCommand("windows", candidate, []uint32{42, 81}, false)
	if n != "taskkill" || strings.Join(a, " ") != "/PID 42 /PID 81 /T" {
		t.Fatalf("win stop graceful: %s %v", n, a)
	}
	_, a = desktopStopCommand("windows", candidate, []uint32{42}, true)
	if strings.Join(a, " ") != "/PID 42 /T /F" || strings.Contains(strings.Join(a, " "), "/IM") {
		t.Fatalf("win stop force: %v", a)
	}
	n, a = desktopStopCommand("darwin", candidate, nil, false)
	if n != "osascript" || !strings.Contains(strings.Join(a, " "), `application "/Applications/LOOP.24.app"`) {
		t.Fatalf("mac stop graceful: %s %v", n, a)
	}
	n, a = desktopStopCommand("darwin", candidate, nil, true)
	wantPattern := `^/Applications/LOOP\.24\.app/Contents/MacOS/LOOP\.24([[:space:]]|$)`
	if n != "pkill" || a[0] != "-f" || a[1] != wantPattern {
		t.Fatalf("mac stop force: %s %v", n, a)
	}
}

func TestMacExecutablePatternEscapesAndAnchorsExactPath(t *testing.T) {
	path := "/Applications/LOOP.24.app/Contents/MacOS/LOOP.24"
	pattern := macExecutablePattern(path)
	if want := `^/Applications/LOOP\.24\.app/Contents/MacOS/LOOP\.24([[:space:]]|$)`; pattern != want {
		t.Fatalf("mac executable pattern = %q, want %q", pattern, want)
	}
	re := regexp.MustCompile(pattern)
	for _, command := range []string{path, path + " --hidden", path + "\t--hidden"} {
		if !re.MatchString(command) {
			t.Errorf("pattern did not match candidate command %q", command)
		}
	}
	for _, command := range []string{"prefix" + path, path + "-helper", "/Applications/Other.app/Contents/MacOS/LOOP.24"} {
		if re.MatchString(command) {
			t.Errorf("pattern matched non-candidate command %q", command)
		}
	}
}

func TestWindowsCandidateProcessIDsDoesNotQueryUnrelatedImages(t *testing.T) {
	candidate := desktopCandidate{Identity: identityFromDisplayName("LOOP24"), ExecutablePath: `C:\Apps\LOOP24.exe`}
	queried := false
	got, err := windowsCandidateProcessIDs(candidate, []desktopProcessEntry{{PID: 17, ImageName: "OTHER.exe"}}, func(uint32) (string, error) {
		queried = true
		return "", nil
	}, func(error) bool { return false })
	if err != nil || len(got) != 0 || queried {
		t.Fatalf("pids=%v, err=%v, queried=%v; unrelated image must be ignored", got, err, queried)
	}
}

func TestWindowsCandidateProcessIDsReturnsCandidatePathQueryError(t *testing.T) {
	candidate := desktopCandidate{Identity: identityFromDisplayName("LOOP24"), ExecutablePath: `C:\Apps\LOOP24.exe`}
	wantErr := errors.New("access denied")
	got, err := windowsCandidateProcessIDs(candidate, []desktopProcessEntry{{PID: 17, ImageName: "loop24.EXE"}}, func(uint32) (string, error) {
		return "", wantErr
	}, func(error) bool { return false })
	if len(got) != 0 || !errors.Is(err, wantErr) {
		t.Fatalf("pids=%v, err=%v; want candidate query error %v", got, err, wantErr)
	}
}

func TestWindowsCandidateProcessIDsSkipsGoneCandidate(t *testing.T) {
	candidate := desktopCandidate{Identity: identityFromDisplayName("LOOP24"), ExecutablePath: `C:\Apps\LOOP24.exe`}
	wantErr := errors.New("process exited")
	got, err := windowsCandidateProcessIDs(candidate, []desktopProcessEntry{{PID: 17, ImageName: "LOOP24.exe"}}, func(uint32) (string, error) {
		return "", wantErr
	}, func(err error) bool { return errors.Is(err, wantErr) })
	if err != nil || len(got) != 0 {
		t.Fatalf("pids=%v, err=%v; gone candidate must be skipped", got, err)
	}
}

func TestWindowsCandidateProcessIDsSelectsExactPathOnly(t *testing.T) {
	candidate := desktopCandidate{Identity: identityFromDisplayName("LOOP24"), ExecutablePath: `C:\Users\me\AppData\Local\Programs\LOOP24\LOOP24.exe`}
	entries := []desktopProcessEntry{
		{PID: 17, ImageName: "LOOP24.exe"},
		{PID: 23, ImageName: "loop24.EXE"},
		{PID: 29, ImageName: "helper.exe"},
	}
	paths := map[uint32]string{
		17: `C:\Other\LOOP24.exe`,
		23: `c:/users/me/appdata/local/programs/loop24/loop24.EXE`,
	}
	got, err := windowsCandidateProcessIDs(candidate, entries, func(pid uint32) (string, error) {
		path, ok := paths[pid]
		if !ok {
			t.Fatalf("queried unrelated PID %d", pid)
		}
		return path, nil
	}, func(error) bool { return false })
	if err != nil || len(got) != 1 || got[0] != 23 {
		t.Fatalf("matching process IDs = %v, err=%v; want [23]", got, err)
	}
}
