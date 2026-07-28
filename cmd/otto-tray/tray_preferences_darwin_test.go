//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/energye/systray"
)

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
