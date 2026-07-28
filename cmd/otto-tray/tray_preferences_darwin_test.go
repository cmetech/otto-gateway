//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/energye/systray"
)

func TestFirstRunOnboardingSerializesWithConcurrentMetricsPreferenceSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	startedFIFO := filepath.Join(home, "dialog-started")
	releaseFIFO := filepath.Join(home, "dialog-release")
	for _, path := range []string{startedFIFO, releaseFIFO} {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	osascript := filepath.Join(binDir, "osascript")
	const script = `#!/bin/sh
printf x > "$TRAY_TEST_DIALOG_STARTED_FIFO"
read -r _ < "$TRAY_TEST_DIALOG_RELEASE_FIFO"
printf 'button returned:Not now\n'
`
	if err := os.WriteFile(osascript, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TRAY_TEST_DIALOG_STARTED_FIFO", startedFIFO)
	t.Setenv("TRAY_TEST_DIALOG_RELEASE_FIFO", releaseFIFO)
	t.Setenv("GW_METRICS_REMOTE_WRITE_TOKEN", "")

	s := newTrayState("", home, TrayConfig{})
	metricsRendered := make(chan struct{})
	s.setMetricsRWChecked = func(enabled bool) {
		if !enabled {
			t.Error("first metrics action must enable the preference")
		}
		close(metricsRendered)
	}
	oldNotify := notifyFn
	notifyFn = func(string, string) {}
	t.Cleanup(func() { notifyFn = oldNotify })

	// Hold the shared persistence serialization point while both callbacks make
	// their in-memory decisions. Onboarding must queue behind it just like the
	// metrics callback; bypassing it can write tray.json out of order.
	s.preferenceSaveMu.Lock()
	preferenceSaveLocked := true
	defer func() {
		if preferenceSaveLocked {
			s.preferenceSaveMu.Unlock()
		}
	}()

	onboardingDone := make(chan struct{})
	go func() {
		offerFirstRunAutostart(s)
		close(onboardingDone)
	}()

	started, err := os.Open(startedFIFO)
	if err != nil {
		t.Fatal(err)
	}
	var signal [1]byte
	if _, err := started.Read(signal[:]); err != nil {
		_ = started.Close()
		t.Fatal(err)
	}
	if err := started.Close(); err != nil {
		t.Fatal(err)
	}

	metricsDone := make(chan struct{})
	go func() {
		s.toggleMetricsRemoteWrite()
		close(metricsDone)
	}()
	<-metricsRendered

	release, err := os.OpenFile(releaseFIFO, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := release.Write([]byte("release\n")); err != nil {
		_ = release.Close()
		t.Fatal(err)
	}
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}

	completedBeforeSerializationRelease := false
	select {
	case <-onboardingDone:
		completedBeforeSerializationRelease = true
	case <-time.After(250 * time.Millisecond):
	}

	s.preferenceSaveMu.Unlock()
	preferenceSaveLocked = false
	for name, done := range map[string]<-chan struct{}{
		"first-run onboarding": onboardingDone,
		"metrics preference":   metricsDone,
	} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s save", name)
		}
	}

	if completedBeforeSerializationRelease {
		t.Error("first-run onboarding bypassed the shared preference-save serialization point")
	}
	cfg, isFirstRun := loadTrayConfig(gwTrayConfigPath(home))
	if isFirstRun {
		t.Error("Not now onboarding choice must still create tray.json")
	}
	if cfg.LaunchAtLogin {
		t.Error("Not now onboarding choice must leave launch-at-login disabled")
	}
	if cfg.MetricsRemoteWriteEnabled == nil || !*cfg.MetricsRemoteWriteEnabled {
		t.Errorf("metrics preference = %v; want true after concurrent onboarding save", cfg.MetricsRemoteWriteEnabled)
	}
}

func TestTrayPreferenceSavesPreserveConcurrentMetricsUpdate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	startedFIFO := filepath.Join(home, "launchctl-started")
	releaseFIFO := filepath.Join(home, "launchctl-release")
	for _, path := range []string{startedFIFO, releaseFIFO} {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	launchctl := filepath.Join(binDir, "launchctl")
	const script = `#!/bin/sh
printf x > "$TRAY_TEST_STARTED_FIFO"
read -r _ < "$TRAY_TEST_RELEASE_FIFO"
`
	if err := os.WriteFile(launchctl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TRAY_TEST_STARTED_FIFO", startedFIFO)
	t.Setenv("TRAY_TEST_RELEASE_FIFO", releaseFIFO)

	s := newTrayState("", home, TrayConfig{})
	s.miPrefsLogin = systray.AddMenuItemCheckbox("Launch tray at login", "", false)
	oldNotify := notifyFn
	notifyFn = func(string, string) {}
	t.Cleanup(func() { notifyFn = oldNotify })

	loginDone := make(chan struct{})
	go func() {
		s.toggleLaunchAtLogin()
		close(loginDone)
	}()

	started, err := os.Open(startedFIFO)
	if err != nil {
		t.Fatal(err)
	}
	var signal [1]byte
	if _, err := started.Read(signal[:]); err != nil {
		_ = started.Close()
		t.Fatal(err)
	}
	if err := started.Close(); err != nil {
		t.Fatal(err)
	}

	// The launch preference has already snapshotted its new value and is
	// blocked before saving. Persist metrics while that stale snapshot waits.
	s.toggleMetricsRemoteWrite()

	release, err := os.OpenFile(releaseFIFO, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := release.Write([]byte("release\n")); err != nil {
		_ = release.Close()
		t.Fatal(err)
	}
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}
	<-loginDone

	cfg, _ := loadTrayConfig(gwTrayConfigPath(home))
	if !cfg.LaunchAtLogin {
		t.Error("launch-at-login preference was lost")
	}
	if cfg.MetricsRemoteWriteEnabled == nil || !*cfg.MetricsRemoteWriteEnabled {
		t.Errorf("metrics preference = %v; want true after concurrent save", cfg.MetricsRemoteWriteEnabled)
	}
}
