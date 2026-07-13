//go:build darwin || windows

package main

import (
	"os"
	"runtime"
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

// Handlers — filled in Task 4. Declared here so wireCallbacks compiles.
func (s *trayState) handleDesktopInstall() {}
func (s *trayState) handleDesktopStart()   {}
func (s *trayState) handleDesktopStop()    {}
