//go:build darwin || windows

package main

import (
	"os"
	"runtime"
	"strings"
	"time"
)

// desktopLabel formats the brand-neutral desktop-section string
// "Co-Worker <suffix>" (suffix may be empty, e.g. "· running"). The tray
// always presents the desktop app as "Co-Worker"; the underlying brand
// identity is still used elsewhere to resolve paths, running status, and
// start/stop commands.
func desktopLabel(suffix string) string {
	s := "Co-Worker"
	if suffix != "" {
		s += " " + suffix
	}
	return strings.TrimSpace(s)
}

// makeDesktopProbe returns a per-tick evidence gatherer: resolve the installed
// app (with brand.json refinement), record its path, and check liveness. The
// Installing flag is overlaid from the tray's atomic bool so an in-flight
// install shows the spinner state.
func (s *trayState) makeDesktopProbe() func() desktopInput {
	env := os.Getenv
	home, _ := os.UserHomeDir()
	readFile := os.ReadFile
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	return func() desktopInput {
		if s.desktopInstalling.Load() {
			return desktopInput{Installing: true}
		}
		id, appPath := resolveDesktopIdentity(runtime.GOOS, env, home, exists, readFile)
		if appPath == "" {
			return desktopInput{Installed: false}
		}
		s.desktopAppPath.Store(&appPath)
		return desktopInput{Installed: true, Running: isDesktopRunning(id)}
	}
}

func (s *trayState) desktopUILoop() {
	for st := range s.desktopCh {
		s.applyDesktopState(st)
	}
}

// applyDesktopState updates the desktop menu section for the given state.
// Install is shown only when not installed; Start/Stop are enabled by state.
func (s *trayState) applyDesktopState(st DesktopState) {
	switch st {
	case DesktopNotInstalled:
		s.miDesktopHeader.SetTitle(desktopLabel("· not installed"))
		s.miDesktopInstall.Show()
		s.miDesktopInstall.Enable()
		s.miDesktopStart.Hide()
		s.miDesktopStop.Hide()
	case DesktopInstalling:
		s.miDesktopHeader.SetTitle(desktopLabel("· installing…"))
		s.miDesktopInstall.Show()
		s.miDesktopInstall.Disable()
		s.miDesktopStart.Hide()
		s.miDesktopStop.Hide()
	case DesktopStopped:
		s.miDesktopHeader.SetTitle(desktopLabel("· not running"))
		s.miDesktopInstall.Hide()
		s.miDesktopStart.Show()
		s.miDesktopStart.Enable()
		s.miDesktopStop.Hide()
	case DesktopRunning:
		s.miDesktopHeader.SetTitle(desktopLabel("· running"))
		s.miDesktopInstall.Hide()
		s.miDesktopStart.Hide()
		s.miDesktopStop.Show()
		s.miDesktopStop.Enable()
	}
}

// desktopInstallDeps carries the injectable side-effects of an install run so
// the ordering (start toast → run → finish toast) is unit-testable without a
// real confirm dialog or a real multi-minute installer download.
type desktopInstallDeps struct {
	confirm func() bool
	run     func() runResult
	notify  func(title, body string)
	label   string
}

// runDesktopInstall orchestrates an install: confirm, then an IMMEDIATE
// "downloading" toast BEFORE the (silent, hidden-console) installer runs — so
// the user gets instant feedback instead of a dead window until the OTTO setup
// GUI finally appears — then a terminal success/failure toast.
func runDesktopInstall(d desktopInstallDeps) {
	if !d.confirm() {
		return
	}
	d.notify(d.label, "Downloading the installer — this can take a minute. You'll be notified when it's done.")
	res := d.run()
	if res.ExitCode != 0 || res.Err != nil {
		d.notify(d.label, "Install failed: "+firstLine(res.Stderr))
		return
	}
	d.notify(d.label, "Co-Worker installed.")
}

func (s *trayState) handleDesktopInstall() {
	s.desktopInstalling.Store(true)
	defer s.desktopInstalling.Store(false)
	name, args := desktopInstallCommand(runtime.GOOS)
	runDesktopInstall(desktopInstallDeps{
		confirm: func() bool {
			return confirmDialog("Install "+desktopLabel("")+"…",
				"Download and run the official Co-Worker installer now?", "Install", "Cancel")
		},
		run:    func() runResult { return runCmd(10*time.Minute, "", name, args...) }, // installs are slow (download+unpack+bootstrap)
		notify: notify,
		label:  desktopLabel(""),
	})
	// next poll re-detects → state flips to stopped/running
}

func (s *trayState) handleDesktopStart() {
	p := s.desktopAppPath.Load()
	appPath := ""
	if p != nil {
		appPath = *p
	}
	_, freshPath := resolveDesktopIdentity(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile)
	// Stale-path guard: the cached path may point at an app that was moved
	// or uninstalled since the last poll. Re-resolve before launching so we
	// never spawn a dead path.
	if appPath == "" || !statExists(appPath) {
		appPath = freshPath
	}
	if appPath == "" {
		notify(desktopLabel(""), "Desktop app not found. Install it first.")
		return
	}
	name, args := desktopStartCommand(runtime.GOOS, appPath)
	if err := spawnDetached("", name, args...); err != nil {
		notify(desktopLabel(""), "Failed to start: "+err.Error())
	}
}

func (s *trayState) handleDesktopStop() {
	id, _ := resolveDesktopIdentity(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile)
	if !confirmDialog("Stop "+desktopLabel(""),
		"Stop the Co-Worker app? Any unsaved work in it may be lost.", "Stop", "Cancel") {
		return
	}
	// graceful first
	name, args := desktopStopCommand(runtime.GOOS, id, false)
	res := runCmd(15*time.Second, "", name, args...)
	// forced fallback if still alive shortly after
	time.Sleep(1500 * time.Millisecond)
	if isDesktopRunning(id) {
		fname, fargs := desktopStopCommand(runtime.GOOS, id, true)
		res = runCmd(15*time.Second, "", fname, fargs...)
	}
	if res.Err != nil && isDesktopRunning(id) {
		notify(desktopLabel(""), "Failed to stop: "+firstLine(res.Stderr))
	}
}

// small helpers reused by handlers
func homeDir() string          { h, _ := os.UserHomeDir(); return h }
func statExists(p string) bool { _, err := os.Stat(p); return err == nil }
