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

type desktopMenuModel struct {
	Header          string
	AppFolderTitle  string
	DataFolderTitle string
	FoldersEnabled  bool
	InstallVisible  bool
	InstallEnabled  bool
	StartVisible    bool
	StartEnabled    bool
	StopVisible     bool
	StopEnabled     bool
}

func desktopMenuForOutput(out desktopOutput) desktopMenuModel {
	model := desktopMenuModel{
		Header:          desktopLabel("· detecting…"),
		AppFolderTitle:  "Open Co-Worker App Folder",
		DataFolderTitle: "Open Co-Worker Data Folder",
	}

	switch out.State {
	case DesktopNotInstalled:
		model.Header = desktopLabel("· not installed")
		model.InstallVisible = true
		model.InstallEnabled = true
	case DesktopInstalling:
		model.Header = desktopLabel("· installing…")
		model.InstallVisible = true
	case DesktopStopped:
		model.Header = desktopLabel("· not running")
		if out.Candidate != nil {
			model.Header = desktopLabel("· " + out.Candidate.Identity.DisplayName + " not running")
			model.StartVisible = true
			model.StartEnabled = true
		}
	case DesktopRunning:
		model.Header = desktopLabel("· running")
		if out.Candidate != nil {
			name := out.Candidate.Identity.DisplayName
			model.Header = desktopLabel("· " + name + " running")
			model.AppFolderTitle = "Open " + name + " App Folder"
			model.DataFolderTitle = "Open " + name + " Data Folder"
			model.FoldersEnabled = true
			model.StopVisible = true
			model.StopEnabled = true
		}
	case DesktopAmbiguous:
		model.Header = desktopLabel("· multiple apps detected")
	case DesktopDetectionError:
		model.Header = desktopLabel("· detection error")
	}

	return model
}

func (s *trayState) makeDesktopProbe() func() desktopOutput {
	return func() desktopOutput {
		candidates, err := discoverDesktopCandidates(runtime.GOOS, os.Getenv, homeDir(), productionDesktopDiscoveryDeps)
		if err != nil {
			return desktopOutput{State: DesktopDetectionError, Detail: err.Error()}
		}
		return resolveDesktopCandidates(candidates, isDesktopRunning, s.desktopInstalling.Load())
	}
}

func (s *trayState) desktopUILoop() {
	for out := range s.desktopCh {
		s.applyDesktopOutput(out)
	}
}

func immutableDesktopOutput(out desktopOutput) desktopOutput {
	snapshot := out
	if out.Candidate != nil {
		candidate := *out.Candidate
		snapshot.Candidate = &candidate
	}
	return snapshot
}

func applyDesktopMenuModel(
	cache *menuRenderCache[desktopMenuModel],
	model desktopMenuModel,
	ops desktopMenuRenderOps,
) bool {
	return cache.Apply(model, func(model desktopMenuModel) {
		ops.header.setTitle(model.Header)
		ops.appFolder.setTitle(model.AppFolderTitle)
		ops.dataFolder.setTitle(model.DataFolderTitle)
		ops.appFolder.setEnabled(model.FoldersEnabled)
		ops.dataFolder.setEnabled(model.FoldersEnabled)
		ops.install.setVisible(model.InstallVisible)
		ops.install.setEnabled(model.InstallEnabled)
		ops.start.setVisible(model.StartVisible)
		ops.start.setEnabled(model.StartEnabled)
		ops.stop.setVisible(model.StopVisible)
		ops.stop.setEnabled(model.StopEnabled)
	})
}

func applyDesktopMenuOutput(
	cache *menuRenderCache[desktopMenuModel],
	out desktopOutput,
	store func(*desktopOutput),
	ops desktopMenuRenderOps,
) {
	snapshot := immutableDesktopOutput(out)
	store(&snapshot)
	applyDesktopMenuModel(cache, desktopMenuForOutput(snapshot), ops)
}

func (s *trayState) applyDesktopOutput(out desktopOutput) {
	applyDesktopMenuOutput(&s.desktopMenuCache, out, s.desktopCurrent.Store, s.nativeDesktopMenuRenderOps())
}

func (s *trayState) nativeDesktopMenuRenderOps() desktopMenuRenderOps {
	return desktopMenuRenderOps{
		header:     nativeMenuItemRenderOps(s.miDesktopHeader),
		appFolder:  nativeMenuItemRenderOps(s.miOpenAppFolder),
		dataFolder: nativeMenuItemRenderOps(s.miOpenDataFolder),
		install:    nativeMenuItemRenderOps(s.miDesktopInstall),
		start:      nativeMenuItemRenderOps(s.miDesktopStart),
		stop:       nativeMenuItemRenderOps(s.miDesktopStop),
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
	requestDesktopRefresh(s.desktopRefreshCh)
	defer func() {
		s.desktopInstalling.Store(false)
		requestDesktopRefresh(s.desktopRefreshCh)
	}()
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
}

func (s *trayState) handleDesktopStart() {
	out := s.desktopCurrent.Load()
	if out == nil || out.State != DesktopStopped || out.Candidate == nil {
		return
	}
	appPath := out.Candidate.AppPath
	if appPath == "" || !statExists(appPath) {
		s.publishDesktopOutput(desktopOutput{State: DesktopNotInstalled})
		requestDesktopRefresh(s.desktopRefreshCh)
		notify(desktopLabel(""), "Desktop app not found. Install it first.")
		return
	}
	name, args := desktopStartCommand(runtime.GOOS, appPath)
	if err := spawnDetached("", name, args...); err != nil {
		notify(desktopLabel(""), "Failed to start: "+err.Error())
		return
	}
	requestDesktopRefresh(s.desktopRefreshCh)
}

func (s *trayState) handleDesktopStop() {
	out := s.desktopCurrent.Load()
	if out == nil || out.State != DesktopRunning || out.Candidate == nil {
		return
	}
	candidate := *out.Candidate
	if !confirmDialog("Stop "+desktopLabel(""),
		"Stop the Co-Worker app? Any unsaved work in it may be lost.", "Stop", "Cancel") {
		return
	}
	pids, err := desktopStopPIDs(runtime.GOOS, candidate)
	if err != nil {
		s.publishDesktopOutput(desktopOutput{State: DesktopDetectionError, Detail: err.Error()})
		requestDesktopRefresh(s.desktopRefreshCh)
		notify(desktopLabel(""), "Could not verify the Co-Worker process: "+err.Error())
		return
	}
	name, args := desktopStopCommand(runtime.GOOS, candidate, pids, false)
	if name == "" {
		requestDesktopRefresh(s.desktopRefreshCh)
		return
	}
	res := runCmd(15*time.Second, "", name, args...)
	// forced fallback if still alive shortly after
	time.Sleep(1500 * time.Millisecond)
	running, err := isDesktopRunning(candidate)
	if err != nil {
		s.publishDesktopOutput(desktopOutput{State: DesktopDetectionError, Detail: err.Error()})
		requestDesktopRefresh(s.desktopRefreshCh)
		notify(desktopLabel(""), "Could not verify whether the Co-Worker stopped: "+err.Error())
		return
	}
	if running {
		pids, err = desktopStopPIDs(runtime.GOOS, candidate)
		if err != nil {
			s.publishDesktopOutput(desktopOutput{State: DesktopDetectionError, Detail: err.Error()})
			requestDesktopRefresh(s.desktopRefreshCh)
			notify(desktopLabel(""), "Could not verify the Co-Worker process: "+err.Error())
			return
		}
		fname, fargs := desktopStopCommand(runtime.GOOS, candidate, pids, true)
		if fname == "" {
			requestDesktopRefresh(s.desktopRefreshCh)
			return
		}
		res = runCmd(15*time.Second, "", fname, fargs...)
	}
	if res.Err != nil {
		running, err = isDesktopRunning(candidate)
		if err != nil {
			s.publishDesktopOutput(desktopOutput{State: DesktopDetectionError, Detail: err.Error()})
			notify(desktopLabel(""), "Could not verify whether the Co-Worker stopped: "+err.Error())
		} else if running {
			notify(desktopLabel(""), "Failed to stop: "+firstLine(res.Stderr))
		}
	}
	requestDesktopRefresh(s.desktopRefreshCh)
}

func desktopStopPIDs(goos string, candidate desktopCandidate) ([]uint32, error) {
	if goos != "windows" {
		return nil, nil
	}
	return desktopProcessIDs(candidate)
}

func (s *trayState) publishDesktopOutput(out desktopOutput) {
	s.desktopCh <- out
}

// small helpers reused by handlers
func homeDir() string          { h, _ := os.UserHomeDir(); return h }
func statExists(p string) bool { _, err := os.Stat(p); return err == nil }
