//go:build darwin || windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/energye/systray"

	"otto-gateway/cmd/otto-tray/icon"
)

// runTray is the main UI loop. systray.Run blocks until Quit is
// invoked. UI work happens on systray's main thread via menu-item
// callbacks; the poller runs on its own goroutine and forwards
// state updates onto a channel that the uiLoop drains.
func runTray(installRoot string, cfg TrayConfig, isFirstRun bool) {
	state := newTrayState(installRoot, cfg)
	systray.Run(state.onReady(isFirstRun), state.onExit)
}

type trayState struct {
	mu           sync.Mutex
	installRoot  string
	cfg          TrayConfig
	dashboardURL string
	current      State
	// startedAt holds the moment the operator last clicked Start /
	// Restart. The poller reads it through a *time.Time alias so a
	// stale read between the click and the next tick still produces
	// a coherent in-budget calculation; updates are atomic-pointer
	// swaps so reader and writer don't need a shared mutex.
	startedAt    atomic.Pointer[time.Time]
	pollerCancel context.CancelFunc
	stateCh      chan stateOutput

	miHeader     *systray.MenuItem
	miSubheader  *systray.MenuItem
	miStart      *systray.MenuItem
	miStop       *systray.MenuItem
	miRestart    *systray.MenuItem
	miDashboard  *systray.MenuItem
	miCopyHealth *systray.MenuItem
	miPrefsLogin *systray.MenuItem
	miPrefsStart *systray.MenuItem
	miAbout      *systray.MenuItem
	miQuit       *systray.MenuItem
}

func newTrayState(installRoot string, cfg TrayConfig) *trayState {
	return &trayState{
		installRoot:  installRoot,
		cfg:          cfg,
		dashboardURL: resolveDashboardURL(installRoot),
		current:      StateUnknown,
		stateCh:      make(chan stateOutput, 4),
	}
}

func (s *trayState) onReady(isFirstRun bool) func() {
	return func() {
		setIcon(icon.Template)
		systray.SetTooltip("OTTO Gateway")

		s.miHeader = systray.AddMenuItem("OTTO Gateway · starting…", "")
		s.miHeader.Disable()
		s.miSubheader = systray.AddMenuItem("", "")
		s.miSubheader.Disable()
		systray.AddSeparator()
		s.miStart = systray.AddMenuItem("Start gateway", "")
		s.miStop = systray.AddMenuItem("Stop gateway", "")
		s.miRestart = systray.AddMenuItem("Restart gateway", "")
		systray.AddSeparator()
		s.miDashboard = systray.AddMenuItem("Open dashboard", s.dashboardURL)
		s.miCopyHealth = systray.AddMenuItem("Copy health URL", "")
		systray.AddSeparator()
		prefs := systray.AddMenuItem("Preferences", "")
		s.miPrefsLogin = prefs.AddSubMenuItemCheckbox("Launch tray at login", "", s.cfg.LaunchAtLogin)
		s.miPrefsStart = prefs.AddSubMenuItemCheckbox("Start gateway when tray launches", "", s.cfg.StartGatewayOnLaunch)
		s.miAbout = systray.AddMenuItem("About OTTO Gateway…", "")
		systray.AddSeparator()
		s.miQuit = systray.AddMenuItem("Quit OTTO Tray", "")

		s.wireCallbacks()

		ctx, cancel := context.WithCancel(context.Background())
		s.pollerCancel = cancel
		probe := s.makeProbe()
		tick := time.NewTicker(3 * time.Second).C
		go runPoller(ctx, probe, tick, s.stateCh, s.getStartedAt)
		go s.uiLoop()

		go func() {
			time.Sleep(500 * time.Millisecond)
			if isFirstRun {
				offerFirstRunAutostart(s)
				return
			}
			if s.cfg.StartGatewayOnLaunch {
				s.handleStart()
			}
		}()
	}
}

func (s *trayState) onExit() {
	if s.pollerCancel != nil {
		s.pollerCancel()
	}
}

func (s *trayState) wireCallbacks() {
	s.miStart.Click(func() { go s.handleStart() })
	s.miStop.Click(func() { go s.handleStop() })
	s.miRestart.Click(func() { go s.handleRestart() })
	s.miDashboard.Click(func() { go openURL(s.dashboardURL) })
	s.miCopyHealth.Click(func() { go copyToClipboard(s.dashboardURL + "/health") })
	s.miPrefsLogin.Click(func() { go s.toggleLaunchAtLogin() })
	s.miPrefsStart.Click(func() { go s.toggleStartGatewayOnLaunch() })
	s.miAbout.Click(func() { go s.showAbout() })
	s.miQuit.Click(func() { systray.Quit() })
}

func (s *trayState) makeProbe() probeFunc {
	pidPath := installRootPIDFile(s.installRoot)
	client := newStatusClient(s.dashboardURL, 1*time.Second)
	return func() (bool, bool, Snapshot) {
		pid, _ := readPIDFile(pidPath)
		alive := pid > 0 && processAlive(pid)
		if !alive {
			return false, false, Snapshot{}
		}
		ok := client.healthOK()
		if !ok {
			return true, false, Snapshot{}
		}
		snap, _ := client.snapshot()
		// /health/hooks is a separate endpoint — failures here are
		// silently ignored (the absence of hook data just means the
		// FSM cannot light up "degraded by hook error"; it does not
		// invalidate the running state).
		if hooks, err := client.hooks(); err == nil {
			snap.Hooks = hooks
		}
		return true, true, snap
	}
}

func installRootPIDFile(installRoot string) string {
	return filepath.Join(installRoot, ".otto", "gw", "otto-gateway.pid")
}

func (s *trayState) uiLoop() {
	for out := range s.stateCh {
		s.applyState(out)
	}
}

func (s *trayState) applyState(out stateOutput) {
	s.mu.Lock()
	prev := s.current
	s.current = out.State
	s.mu.Unlock()

	header := fmt.Sprintf("OTTO Gateway · %s", out.State)
	if out.Detail != "" {
		header += " (" + out.Detail + ")"
	}
	s.miHeader.SetTitle(header)
	s.miSubheader.SetTitle(s.dashboardURL)

	canStart := out.State == StateStopped || out.State == StateError
	if canStart {
		s.miStart.Enable()
		s.miStop.Disable()
		s.miRestart.Disable()
	} else {
		s.miStart.Disable()
		s.miStop.Enable()
		s.miRestart.Enable()
	}

	if prev == StateRunning && (out.State == StateError || out.State == StateStopped) {
		notify("OTTO Gateway", fmt.Sprintf("Gateway is %s", out.State))
	}
}

// getStartedAt returns the last-recorded start timestamp, or the
// zero time when the operator has not clicked Start yet.
func (s *trayState) getStartedAt() time.Time {
	if t := s.startedAt.Load(); t != nil {
		return *t
	}
	return time.Time{}
}

// setStartedNow records 'now' as the start moment so the poller's
// 30-second StartingBudget window covers the warm-up.
func (s *trayState) setStartedNow() {
	now := time.Now()
	s.startedAt.Store(&now)
}

func (s *trayState) handleStart() {
	s.setStartedNow()
	res := runWrapper(s.installRoot, "start")
	if res.ExitCode != 0 || res.Err != nil {
		notify("OTTO Gateway", "Failed to start: "+firstLine(res.Stderr))
	}
}

func (s *trayState) handleStop() {
	res := runWrapper(s.installRoot, "stop")
	if res.ExitCode != 0 || res.Err != nil {
		notify("OTTO Gateway", "Failed to stop: "+firstLine(res.Stderr))
	}
}

func (s *trayState) handleRestart() {
	s.setStartedNow()
	res := runWrapper(s.installRoot, "restart")
	if res.ExitCode != 0 || res.Err != nil {
		notify("OTTO Gateway", "Failed to restart: "+firstLine(res.Stderr))
	}
}

func (s *trayState) toggleLaunchAtLogin() {
	s.mu.Lock()
	s.cfg.LaunchAtLogin = !s.cfg.LaunchAtLogin
	cfg := s.cfg
	s.mu.Unlock()
	exe, _ := exeForAutostart()
	var err error
	if cfg.LaunchAtLogin {
		err = installAutostart(exe)
		s.miPrefsLogin.Check()
	} else {
		err = uninstallAutostart()
		s.miPrefsLogin.Uncheck()
	}
	if err != nil {
		slog.Error("autostart toggle failed", "err", err)
		notify("OTTO Gateway", "Could not change login setting: "+err.Error())
		return
	}
	if err := saveTrayConfig(trayConfigPath(s.installRoot), cfg); err != nil {
		slog.Error("save tray.json", "err", err)
	}
}

func (s *trayState) toggleStartGatewayOnLaunch() {
	s.mu.Lock()
	s.cfg.StartGatewayOnLaunch = !s.cfg.StartGatewayOnLaunch
	cfg := s.cfg
	s.mu.Unlock()
	if cfg.StartGatewayOnLaunch {
		s.miPrefsStart.Check()
	} else {
		s.miPrefsStart.Uncheck()
	}
	if err := saveTrayConfig(trayConfigPath(s.installRoot), cfg); err != nil {
		slog.Error("save tray.json", "err", err)
	}
}

func (s *trayState) showAbout() {
	body := fmt.Sprintf("Install: %s\nGo: %s", s.installRoot, runtime.Version())
	notify("About OTTO Gateway", body)
}
