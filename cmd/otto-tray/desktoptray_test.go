//go:build darwin || windows

package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestDesktopLabel(t *testing.T) {
	// The tray label is brand-neutral regardless of the underlying identity.
	if got := desktopLabel("· running"); got != "Co-Worker · running" {
		t.Errorf("got %q", got)
	}
	if got := desktopLabel(""); got != "Co-Worker" {
		t.Errorf("default: got %q", got)
	}
}

func TestDesktopMenuForOutput(t *testing.T) {
	loop := &desktopCandidate{Identity: identityFromDisplayName("LOOP24"), Slug: "loop24", HomeDir: ".loop24"}
	tests := []struct {
		name           string
		out            desktopOutput
		header         string
		foldersEnabled bool
		appTitle       string
		dataTitle      string
		installVisible bool
		installEnabled bool
		startVisible   bool
		stopVisible    bool
	}{
		{name: "detecting", out: desktopOutput{State: DesktopDetecting}, header: "Co-Worker · detecting…", appTitle: "Open Co-Worker App Folder", dataTitle: "Open Co-Worker Data Folder"},
		{name: "not installed", out: desktopOutput{State: DesktopNotInstalled}, header: "Co-Worker · not installed", appTitle: "Open Co-Worker App Folder", dataTitle: "Open Co-Worker Data Folder", installVisible: true, installEnabled: true},
		{name: "installing", out: desktopOutput{State: DesktopInstalling}, header: "Co-Worker · installing…", appTitle: "Open Co-Worker App Folder", dataTitle: "Open Co-Worker Data Folder", installVisible: true},
		{name: "stopped", out: desktopOutput{State: DesktopStopped, Candidate: loop}, header: "Co-Worker · LOOP24 not running", appTitle: "Open Co-Worker App Folder", dataTitle: "Open Co-Worker Data Folder", startVisible: true},
		{name: "running", out: desktopOutput{State: DesktopRunning, Candidate: loop}, header: "Co-Worker · LOOP24 running", foldersEnabled: true, appTitle: "Open LOOP24 App Folder", dataTitle: "Open LOOP24 Data Folder", stopVisible: true},
		{name: "ambiguous", out: desktopOutput{State: DesktopAmbiguous}, header: "Co-Worker · multiple apps detected", appTitle: "Open Co-Worker App Folder", dataTitle: "Open Co-Worker Data Folder"},
		{name: "detection error", out: desktopOutput{State: DesktopDetectionError}, header: "Co-Worker · detection error", appTitle: "Open Co-Worker App Folder", dataTitle: "Open Co-Worker Data Folder"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := desktopMenuForOutput(tc.out)
			if got.Header != tc.header || got.FoldersEnabled != tc.foldersEnabled ||
				got.AppFolderTitle != tc.appTitle || got.DataFolderTitle != tc.dataTitle ||
				got.InstallVisible != tc.installVisible || got.InstallEnabled != tc.installEnabled ||
				got.StartVisible != tc.startVisible || got.StartEnabled != tc.startVisible ||
				got.StopVisible != tc.stopVisible || got.StopEnabled != tc.stopVisible {
				t.Fatalf("desktopMenuForOutput(%q) = %+v", tc.out.State, got)
			}
		})
	}
}

func TestDesktopUnchangedMenuStillStoresLatestSnapshot(t *testing.T) {
	var cache menuRenderCache[desktopMenuModel]
	var current *desktopOutput
	var events []string
	store := func(snapshot *desktopOutput) {
		current = snapshot
		events = append(events, "store:"+snapshot.Candidate.ExecutablePath)
	}
	render := func(desktopMenuModel) {
		if current == nil || current.Candidate == nil {
			t.Fatal("menu rendered before immutable snapshot was stored")
		}
		events = append(events, "render:"+current.Candidate.ExecutablePath)
	}

	firstCandidate := desktopCandidate{
		Identity:       identityFromDisplayName("LOOP24"),
		ExecutablePath: "/Applications/LOOP24-old.app/Contents/MacOS/LOOP24",
	}
	secondCandidate := desktopCandidate{
		Identity:       identityFromDisplayName("LOOP24"),
		ExecutablePath: "/Applications/LOOP24-new.app/Contents/MacOS/LOOP24",
	}
	applyDesktopMenuOutput(&cache, desktopOutput{State: DesktopRunning, Candidate: &firstCandidate}, store, render)
	applyDesktopMenuOutput(&cache, desktopOutput{State: DesktopRunning, Candidate: &secondCandidate}, store, render)
	secondCandidate.ExecutablePath = "/mutated/after/apply"

	wantEvents := []string{
		"store:/Applications/LOOP24-old.app/Contents/MacOS/LOOP24",
		"render:/Applications/LOOP24-old.app/Contents/MacOS/LOOP24",
		"store:/Applications/LOOP24-new.app/Contents/MacOS/LOOP24",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %v, want %v", events, wantEvents)
	}
	if current == nil || current.Candidate == nil {
		t.Fatalf("stored snapshot = %+v", current)
	}
	if current.Candidate.ExecutablePath != "/Applications/LOOP24-new.app/Contents/MacOS/LOOP24" {
		t.Fatalf("stored executable path = %q", current.Candidate.ExecutablePath)
	}
}

func TestMakeDesktopProbeDiscoversRunningCandidate(t *testing.T) {
	oldDeps := productionDesktopDiscoveryDeps
	oldRunning := desktopRunningFn
	defer func() {
		productionDesktopDiscoveryDeps = oldDeps
		desktopRunningFn = oldRunning
	}()

	var descriptor, executable string
	if runtime.GOOS == "windows" {
		appDir := filepath.Join("C:", "Users", "me", "AppData", "Local", "Programs", "LOOP24")
		descriptor = filepath.Join(appDir, "resources", "brand.json")
		executable = filepath.Join(appDir, "LOOP24.exe")
	} else {
		appDir := filepath.Join(string(filepath.Separator), "Applications", "LOOP24.app")
		descriptor = filepath.Join(appDir, "Contents", "Resources", "brand.json")
		executable = filepath.Join(appDir, "Contents", "MacOS", "LOOP24")
	}
	productionDesktopDiscoveryDeps = desktopDiscoveryDeps{
		glob: func(string) ([]string, error) { return []string{descriptor}, nil },
		readFile: func(string) ([]byte, error) {
			return []byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"otto"}`), nil
		},
		exists: func(path string) bool { return path == executable },
	}
	desktopRunningFn = func(candidate desktopCandidate) (bool, error) {
		return candidate.Identity.DisplayName == "LOOP24" && candidate.ExecutablePath == executable, nil
	}

	got := (&trayState{}).makeDesktopProbe()()
	if got.State != DesktopRunning || got.Candidate == nil || got.Candidate.Slug != "loop24" {
		t.Fatalf("probe output = %+v", got)
	}
}

func TestMakeDesktopProbeReportsDiscoveryError(t *testing.T) {
	oldDeps := productionDesktopDiscoveryDeps
	defer func() { productionDesktopDiscoveryDeps = oldDeps }()
	wantErr := errors.New("descriptor scan failed")
	productionDesktopDiscoveryDeps = desktopDiscoveryDeps{
		glob:     func(string) ([]string, error) { return nil, wantErr },
		readFile: func(string) ([]byte, error) { return nil, nil },
		exists:   func(string) bool { return false },
	}

	got := (&trayState{}).makeDesktopProbe()()
	if got.State != DesktopDetectionError || got.Detail == "" || got.Candidate != nil {
		t.Fatalf("probe output = %+v", got)
	}
}

// TestRunDesktopInstall_ConfirmCancelled_NothingRuns: a cancelled confirm
// dialog must not run the installer and must not toast anything.
func TestRunDesktopInstall_ConfirmCancelled_NothingRuns(t *testing.T) {
	var log []string
	runDesktopInstall(desktopInstallDeps{
		confirm: func() bool { log = append(log, "confirm"); return false },
		run:     func() runResult { log = append(log, "run"); return runResult{} },
		notify:  func(_, body string) { log = append(log, "notify:"+body) },
		label:   "Co-Worker",
	})
	if len(log) != 1 || log[0] != "confirm" {
		t.Fatalf("cancelled install must stop after confirm; got %v", log)
	}
}

// TestRunDesktopInstall_Success_StartToastBeforeRun is the core fix: the
// "Downloading…" toast must fire BEFORE the (silent, blocking) installer run,
// so the user gets immediate feedback; then the success toast on completion.
func TestRunDesktopInstall_Success_StartToastBeforeRun(t *testing.T) {
	var log []string
	runDesktopInstall(desktopInstallDeps{
		confirm: func() bool { return true },
		run:     func() runResult { log = append(log, "run"); return runResult{ExitCode: 0} },
		notify:  func(_, body string) { log = append(log, "notify:"+body) },
		label:   "Co-Worker",
	})
	if len(log) != 3 {
		t.Fatalf("want 3 events (start toast, run, finish toast); got %v", log)
	}
	if !strings.HasPrefix(log[0], "notify:") || !strings.Contains(log[0], "Downloading") {
		t.Errorf("first event must be the Downloading start toast; got %q", log[0])
	}
	if log[1] != "run" {
		t.Errorf("the installer must run AFTER the start toast; got order %v", log)
	}
	if !strings.Contains(log[2], "installed") {
		t.Errorf("final event must be the installed toast; got %q", log[2])
	}
}

// TestRunDesktopInstall_Failure_StartToastThenFailureToast: on a failing run,
// the start toast still fires first, then a failure toast carrying the first
// stderr line.
func TestRunDesktopInstall_Failure_StartToastThenFailureToast(t *testing.T) {
	var log []string
	runDesktopInstall(desktopInstallDeps{
		confirm: func() bool { return true },
		run: func() runResult {
			log = append(log, "run")
			return runResult{ExitCode: 1, Stderr: "boom\nextra detail"}
		},
		notify: func(_, body string) { log = append(log, "notify:"+body) },
		label:  "Co-Worker",
	})
	if len(log) != 3 {
		t.Fatalf("want 3 events (start toast, run, failure toast); got %v", log)
	}
	if !strings.Contains(log[0], "Downloading") {
		t.Errorf("first event must be the start toast; got %q", log[0])
	}
	if log[1] != "run" {
		t.Errorf("the installer must run AFTER the start toast; got order %v", log)
	}
	if !strings.Contains(log[2], "Install failed: boom") {
		t.Errorf("final event must be the failure toast with the first stderr line; got %q", log[2])
	}
}
