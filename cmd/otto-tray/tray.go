//go:build darwin || windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/energye/systray"

	"otto-gateway/cmd/otto-tray/icon"
	"otto-gateway/internal/version"
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
	miSupport    *systray.MenuItem
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
		platformOnReady()

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
		s.miSupport = systray.AddMenuItem("Create Support Bundle…", "Produce a redacted diagnostic archive")
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
	s.miDashboard.Click(func() { go openURL(s.dashboardURL + "/admin") })
	s.miCopyHealth.Click(func() { go copyToClipboard(s.dashboardURL + "/health") })
	s.miSupport.Click(func() { go s.handleSupportBundle() })
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
		if alive {
			alive = verifyGatewayIdentity(pid, "")
		}
		if !alive {
			return false, false, Snapshot{}
		}
		ok := client.healthOK()
		if !ok {
			return true, false, Snapshot{}
		}
		// REL-TRAY-05 (T-5) fix: do not swallow snapshot errors. The
		// pre-fix code (snap, _ := client.snapshot()) discarded JSON
		// decode failures and connection resets; the FSM then saw a
		// zero-value Snapshot with PoolSize=0, which masks the
		// Alive==0 rule and Pool.Status enum — the tray reported
		// StateRunning while the pool was wedged. Now: return
		// (true, false, Snapshot{}) so the FSM treats a snapshot
		// error the same as a failed /health probe — the
		// StartingBudget/HealthFailures path lights up Starting or
		// Error.
		snap, err := client.snapshot()
		if err != nil {
			return true, false, Snapshot{}
		}
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

	// T-3 fix (REL-TRAY-03): always-visible state signal on every FSM transition (D-11).
	// Icon and tooltip are the primary gateway-death signal; notify() is secondary.
	setIconForState(out.State)
	systray.SetTooltip(tooltipForState(out.State, out.Detail))

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

	s.notifyTransition(prev, out.State)
}

// notifyTransition emits a user-facing notification on
// running → error/stopped transitions. Split out of applyState so the
// REL-TRAY-04 regression test can drive it without a live systray.
//
// REL-TRAY-04 (T-4) fix: notifyFn is dispatched in a fire-and-forget
// goroutine so a blocking platform notify (Windows MessageBox waits for
// user click for up to 30s) cannot wedge uiLoop.
//
// WR-04 fix (phase 16 review): implement the 3-attempt/500ms-backoff
// retry semantics that the plan-level T-4 description promised. The
// prior `for i := 0; i < 3; i++ { fn(); break }` was an unconditional
// break-after-first — semantically a single call — that read as a
// half-finished retry. Two safeguards keep this honest:
//
//  1. Attempts are spaced with 500ms backoff via time.Sleep INSIDE
//     the goroutine — the call site still returns immediately (the
//     REL-TRAY-04 regression test asserts <100ms).
//
//  2. Before each retry, the FSM's current state is rechecked under
//     s.mu. If the gateway has already transitioned past `next`
//     (e.g., StateStopped → StateStarting → StateRunning while the
//     backoff slept), the retry is skipped — we don't want to keep
//     notifying about a stale state.
//
// notifyFn is synchronous-by-contract on Windows (MessageBox blocks
// until the user clicks OK) and best-effort fire-and-forget on Darwin
// (osascript dispatch). The retry is mostly defensive against the
// Darwin failure mode where osascript can drop the notification
// silently; on Windows the user-click latency between attempts will
// usually trigger the stale-state guard and short-circuit attempts
// 2 and 3.
func (s *trayState) notifyTransition(prev, next State) {
	if prev != StateRunning || (next != StateError && next != StateStopped) {
		return
	}
	title := "OTTO Gateway"
	body := fmt.Sprintf("Gateway is %s", next)
	// Snapshot notifyFn before goroutine launch. Production code never
	// swaps notifyFn at runtime — but the regression test does (defer
	// notifyFn = oldNotify), and -race flags concurrent access on the
	// package-level var. Capturing once at dispatch time is also the
	// right call semantically: in-flight notifications should target
	// the implementation that was active when the transition fired.
	fn := notifyFn
	go func() {
		const maxAttempts = 3
		const backoff = 500 * time.Millisecond
		for i := 0; i < maxAttempts; i++ {
			if i > 0 {
				time.Sleep(backoff)
				// Stale-state guard: if the FSM has already moved
				// past `next` while we were sleeping, abort the
				// retry. Without this guard a brief Running →
				// Stopped → Starting → Running transition would
				// keep notifying "Gateway is stopped" 500ms and
				// 1000ms after the user already saw the recovery.
				s.mu.Lock()
				current := s.current
				s.mu.Unlock()
				if current != next {
					return
				}
			}
			fn(title, body)
		}
	}()
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

// handleSupportBundle is the click handler for the "Create Support Bundle…"
// menu item. Sequence:
//
//  1. Confirmation dialog (no surprise file creation).
//  2. Shell out to the wrapper's `support` verb, sync. The wrapper does all
//     collection + redaction + archiving and prints the archive path on
//     stdout.
//  3. Show a notification with the path and reveal the file in
//     Finder / Explorer.
//
// On failure: dialog with the tail of stderr. No retries — the wrapper
// either produced the archive or it didn't; iterating wouldn't change
// the failure mode.
//
// The 30s timeout on runWrapper is comfortable: collection completes in
// seconds per design §Tray Integration.
func (s *trayState) handleSupportBundle() {
	if !confirmDialog(
		"Create Support Bundle",
		"Create a redacted support bundle? Secrets will be masked. Continue?",
		"Create", "Cancel",
	) {
		return
	}

	res := runWrapper(s.installRoot, "support")
	if res.ExitCode != 0 || res.Err != nil {
		body := "Failed to create support bundle."
		tail := tailLines(res.Stderr, 20)
		if tail != "" {
			body += "\n\n" + tail
		}
		// Persist full stderr/stdout to disk so the user can attach the
		// file even after dismissing this dialog. Best-effort: write
		// failures are silent (any write error would itself be noise
		// the user cannot act on).
		logDir := filepath.Join(s.installRoot, "support")
		if mkErr := os.MkdirAll(logDir, 0o750); mkErr == nil {
			logPath := filepath.Join(logDir, "last-error.log")
			content := "exit=" + strconv.Itoa(res.ExitCode) + "\n\n" +
				"--- stderr ---\n" + res.Stderr + "\n" +
				"--- stdout ---\n" + res.Stdout + "\n"
			if writeErr := os.WriteFile(logPath, []byte(content), 0o600); writeErr == nil {
				body += "\n\nDetails saved to:\n" + logPath
			}
		}
		infoDialog("Support Bundle Failed", body)
		return
	}

	// REL-TRAY-06 (T-6) fix: the wrapper prints the absolute archive
	// path on stdout, but informational lines from Initialize-Config /
	// env loaders can chatter on stdout too. Parse the LAST non-empty
	// stdout line — the support verb always emits the archive path
	// last. Companion fix in scripts/otto-gw.ps1 routes informational
	// output through Write-Host stderr-redirected so stdout stays
	// clean (operators on PS sessions with prompt customizations or
	// profile.ps1 dumps would otherwise still hit this).
	lastLine := ""
	for _, line := range strings.Split(res.Stdout, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lastLine = trimmed
		}
	}
	path := lastLine
	if path == "" {
		path = filepath.Join(s.installRoot, "support", "latest"+bundleExt())
	}

	notify("OTTO Gateway", "Support bundle saved:\n"+path)
	revealBundle(path)
}

// tailLines returns the last n non-empty lines of s, joined with "\n".
// Used to bound how much stderr the failure dialog shows — full stderr
// can easily exceed what fits in a modal.
//
// Implementation note (QUAL-04 / D-20-06): collect-then-reverse. The earlier
// implementation prepended each kept line via `append([]string{t}, kept...)`
// which copies the entire slice on every iteration (O(n²) over the kept set).
// We instead walk lines back-to-front, appending into kept (most-recent-first)
// until we have n lines, then reverse in place before joining. Same I/O.
func tailLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		kept = append(kept, t)
		if len(kept) >= n {
			break
		}
	}
	// kept is in reverse order (most recent first); reverse in place so the
	// returned string preserves the source's chronological order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return strings.Join(kept, "\n")
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
	body := fmt.Sprintf("Version: %s\nCommit: %s\nInstall: %s\nGo: %s",
		version.Version, version.Commit(), s.installRoot, runtime.Version())
	infoDialog("About OTTO Gateway", body)
}
