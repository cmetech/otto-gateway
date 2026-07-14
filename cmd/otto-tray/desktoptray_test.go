//go:build darwin || windows

package main

import (
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
