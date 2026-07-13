//go:build darwin || windows

package main

import (
	"os"
	"runtime"
	"time"
)

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
		s.miDesktopHeader.SetTitle("OTTO Desktop · not installed")
		s.miDesktopInstall.Show()
		s.miDesktopInstall.Enable()
		s.miDesktopStart.Hide()
		s.miDesktopStop.Hide()
	case DesktopInstalling:
		s.miDesktopHeader.SetTitle("OTTO Desktop · installing…")
		s.miDesktopInstall.Show()
		s.miDesktopInstall.Disable()
		s.miDesktopStart.Hide()
		s.miDesktopStop.Hide()
	case DesktopStopped:
		s.miDesktopHeader.SetTitle("OTTO Desktop · not running")
		s.miDesktopInstall.Hide()
		s.miDesktopStart.Show()
		s.miDesktopStart.Enable()
		s.miDesktopStop.Hide()
	case DesktopRunning:
		s.miDesktopHeader.SetTitle("OTTO Desktop · running")
		s.miDesktopInstall.Hide()
		s.miDesktopStart.Hide()
		s.miDesktopStop.Show()
		s.miDesktopStop.Enable()
	}
}

func (s *trayState) handleDesktopInstall() {
	if !confirmDialog("Install OTTO Desktop",
		"Download and run the official OTTO desktop installer now?", "Install", "Cancel") {
		return
	}
	s.desktopInstalling.Store(true)
	defer s.desktopInstalling.Store(false)
	name, args := desktopInstallCommand(runtime.GOOS)
	res := runCmd(10*time.Minute, "", name, args...) // installs are slow (download+unpack+bootstrap)
	if res.ExitCode != 0 || res.Err != nil {
		notify("OTTO Desktop", "Install failed: "+firstLine(res.Stderr))
		return
	}
	notify("OTTO Desktop", "OTTO desktop installed.")
	// next poll re-detects → state flips to stopped/running
}

func (s *trayState) handleDesktopStart() {
	p := s.desktopAppPath.Load()
	appPath := ""
	if p != nil {
		appPath = *p
	}
	// Stale-path guard: the cached path may point at an app that was moved
	// or uninstalled since the last poll. Re-resolve before launching so we
	// never spawn a dead path.
	if appPath == "" || !statExists(appPath) {
		_, fresh := resolveDesktopIdentity(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile)
		appPath = fresh
	}
	if appPath == "" {
		notify("OTTO Desktop", "Desktop app not found. Install it first.")
		return
	}
	name, args := desktopStartCommand(runtime.GOOS, appPath)
	if err := spawnDetached("", name, args...); err != nil {
		notify("OTTO Desktop", "Failed to start: "+err.Error())
	}
}

func (s *trayState) handleDesktopStop() {
	if !confirmDialog("Stop OTTO Desktop",
		"Stop the OTTO desktop app? Any unsaved work in it may be lost.", "Stop", "Cancel") {
		return
	}
	id, _ := resolveDesktopIdentity(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile)
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
		notify("OTTO Desktop", "Failed to stop: "+firstLine(res.Stderr))
	}
}

// small helpers reused by handlers
func homeDir() string          { h, _ := os.UserHomeDir(); return h }
func statExists(p string) bool { _, err := os.Stat(p); return err == nil }
