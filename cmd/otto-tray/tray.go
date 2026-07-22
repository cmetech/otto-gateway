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

	"otto-gateway/internal/version"
)

// runTray is the main UI loop. systray.Run blocks until Quit is
// invoked. UI work happens on systray's main thread via menu-item
// callbacks; the poller runs on its own goroutine and forwards
// state updates onto a channel that the uiLoop drains.
func runTray(installDir, gwHome string, cfg TrayConfig, isFirstRun bool) {
	state := newTrayState(installDir, gwHome, cfg)
	systray.Run(state.onReady(isFirstRun), state.onExit)
}

type gatewayMenuModel struct {
	State          State
	Tooltip        string
	Header         string
	Subheader      string
	StartEnabled   bool
	StopEnabled    bool
	RestartEnabled bool
}

func gatewayMenuForOutput(out stateOutput, dashboardURL string) gatewayMenuModel {
	header := fmt.Sprintf("Gateway · %s", out.State)
	if out.Detail != "" {
		header += " (" + out.Detail + ")"
	}
	canStart := out.State == StateStopped || out.State == StateError
	return gatewayMenuModel{
		State:          out.State,
		Tooltip:        tooltipForState(out.State, out.Detail),
		Header:         header,
		Subheader:      dashboardURL,
		StartEnabled:   canStart,
		StopEnabled:    !canStart,
		RestartEnabled: !canStart,
	}
}

func applyGatewayMenuModel(
	cache *menuRenderCache[gatewayMenuModel],
	model gatewayMenuModel,
	ops gatewayMenuRenderOps,
) bool {
	return cache.Apply(model, func(model gatewayMenuModel) {
		ops.setIcon(model.State)
		ops.setTooltip(model.Tooltip)
		ops.header.setTitle(model.Header)
		ops.subheader.setTitle(model.Subheader)
		ops.start.setEnabled(model.StartEnabled)
		ops.stop.setEnabled(model.StopEnabled)
		ops.restart.setEnabled(model.RestartEnabled)
	})
}

type trayState struct {
	mu               sync.Mutex
	installDir       string
	gwHome           string
	cfg              TrayConfig
	dashboardURL     string
	current          State
	gatewayMenuCache menuRenderCache[gatewayMenuModel]
	desktopMenuCache menuRenderCache[desktopMenuModel]
	// startedAt holds the moment the operator last clicked Start /
	// Restart. The poller reads it through a *time.Time alias so a
	// stale read between the click and the next tick still produces
	// a coherent in-budget calculation; updates are atomic-pointer
	// swaps so reader and writer don't need a shared mutex.
	startedAt    atomic.Pointer[time.Time]
	pollerCancel context.CancelFunc
	stateCh      chan stateOutput

	miHeader        *systray.MenuItem
	miSubheader     *systray.MenuItem
	miStart         *systray.MenuItem
	miStop          *systray.MenuItem
	miRestart       *systray.MenuItem
	miDashboard     *systray.MenuItem
	miCopyHealth    *systray.MenuItem
	miCopyGatewayID *systray.MenuItem
	miSupport       *systray.MenuItem
	miPrefsLogin    *systray.MenuItem
	miPrefsStart    *systray.MenuItem
	miAbout         *systray.MenuItem
	miQuit          *systray.MenuItem

	// desktop-app management (parallel to the gateway controls)
	desktopCh         chan desktopOutput
	desktopRefreshCh  chan struct{}
	desktopInstalling atomic.Bool
	desktopCurrent    atomic.Pointer[desktopOutput]
	miDesktopHeader   *systray.MenuItem
	miDesktopInstall  *systray.MenuItem
	miDesktopStart    *systray.MenuItem
	miDesktopStop     *systray.MenuItem
	miDesktopRefresh  *systray.MenuItem

	// advanced ▸ open-folder links + metrics remote-write toggle
	miAdvanced          *systray.MenuItem
	miOpenAppFolder     *systray.MenuItem
	miOpenDataFolder    *systray.MenuItem
	miOpenGatewayFolder *systray.MenuItem
	miMetricsRW         *systray.MenuItem

	// metricsRWEnabled is the live on/off for Grafana Cloud remote-write, read
	// each cycle by runRemoteWriter and flipped by the Advanced-menu checkbox so
	// enable/disable takes effect without a tray restart.
	metricsRWEnabled atomic.Bool
}

func newTrayState(installDir, gwHome string, cfg TrayConfig) *trayState {
	return &trayState{
		installDir:       installDir,
		gwHome:           gwHome,
		cfg:              cfg,
		dashboardURL:     resolveDashboardURL(gwHome),
		current:          StateUnknown,
		stateCh:          make(chan stateOutput, 4),
		desktopCh:        make(chan desktopOutput, 4),
		desktopRefreshCh: make(chan struct{}, 1),
	}
}

func (s *trayState) onReady(isFirstRun bool) func() {
	return func() {
		setBaseIcon()
		systray.SetTooltip("Gateway")
		platformOnReady()

		s.miHeader = systray.AddMenuItem("Gateway · starting…", "")
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
		s.miCopyGatewayID = systray.AddMenuItem("Copy Gateway ID", "Copy this gateway's ID to quote when contacting support")
		systray.AddSeparator()
		s.miDesktopHeader = systray.AddMenuItem(desktopLabel("· detecting…"), "")
		s.miDesktopHeader.Disable()
		s.miDesktopInstall = systray.AddMenuItem("Install "+desktopLabel("")+"…", "Download and run the Co-Worker installer")
		s.miDesktopStart = systray.AddMenuItem("Start "+desktopLabel(""), "")
		s.miDesktopStop = systray.AddMenuItem("Stop "+desktopLabel(""), "")
		systray.AddSeparator()
		s.miAdvanced = systray.AddMenuItem("Advanced", "")
		s.miOpenAppFolder = s.miAdvanced.AddSubMenuItem("Open Co-Worker App Folder", "Reveal the running Co-Worker app folder")
		s.miOpenDataFolder = s.miAdvanced.AddSubMenuItem("Open Co-Worker Data Folder", "Open the running Co-Worker data folder")
		s.miDesktopRefresh = s.miAdvanced.AddSubMenuItem("Refresh Co-Worker Detection", "Detect installed and running Co-Worker apps now")
		s.applyDesktopOutput(desktopOutput{State: DesktopDetecting})
		s.miOpenGatewayFolder = s.miAdvanced.AddSubMenuItem("Open Gateway Folder (~/.gw)", "Open the Gateway data folder")
		rwInitial := resolveMetricsRWEnabled(s.cfg, s.gwHome)
		s.metricsRWEnabled.Store(rwInitial)
		s.miMetricsRW = s.miAdvanced.AddSubMenuItemCheckbox("Send metrics to Grafana Cloud", "Scrape the gateway's metrics and remote-write them to Grafana Cloud", rwInitial)
		systray.AddSeparator()
		s.miSupport = systray.AddMenuItem("Create Support Bundle…", "Produce a redacted diagnostic archive")
		systray.AddSeparator()
		prefs := systray.AddMenuItem("Preferences", "")
		s.miPrefsLogin = prefs.AddSubMenuItemCheckbox("Launch tray at login", "", s.cfg.LaunchAtLogin)
		s.miPrefsStart = prefs.AddSubMenuItemCheckbox("Start gateway when tray launches", "", s.cfg.StartGatewayOnLaunch)
		s.miAbout = systray.AddMenuItem("About Gateway…", "")
		systray.AddSeparator()
		s.miQuit = systray.AddMenuItem("Quit Gateway Tray", "")

		s.wireCallbacks()

		ctx, cancel := context.WithCancel(context.Background())
		s.pollerCancel = cancel
		probe := s.makeProbe()
		tick := time.NewTicker(3 * time.Second).C
		go runPoller(ctx, probe, tick, s.stateCh, s.getStartedAt, s.gwHome)
		go s.uiLoop()

		dtick := time.NewTicker(3 * time.Second).C
		go runDesktopPoller(ctx, s.makeDesktopProbe(), dtick, s.desktopRefreshCh, s.desktopCh)
		go s.desktopUILoop()
		requestDesktopRefresh(s.desktopRefreshCh)

		// Grafana Cloud metrics remote-write agent (quick 260715-q5m). Shares the
		// poller ctx so it stops on tray exit. Reads its interval/endpoint/token
		// from overrides.env/.env each cycle; the Advanced checkbox gates it live.
		rw := newRemoteWriter(s.dashboardURL+"/metrics", s.gwHome, &s.metricsRWEnabled)
		go runRemoteWriter(ctx, rw, sleepCtx)

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
	s.miCopyGatewayID.Click(func() { go s.copyGatewayID() })
	s.miSupport.Click(func() { go s.handleSupportBundle() })
	s.miPrefsLogin.Click(func() { go s.toggleLaunchAtLogin() })
	s.miPrefsStart.Click(func() { go s.toggleStartGatewayOnLaunch() })
	s.miAbout.Click(func() { go s.showAbout() })
	s.miQuit.Click(func() { systray.Quit() })

	s.miDesktopInstall.Click(func() { go s.handleDesktopInstall() })
	s.miDesktopStart.Click(func() { go s.handleDesktopStart() })
	s.miDesktopStop.Click(func() { go s.handleDesktopStop() })
	s.miDesktopRefresh.Click(func() { requestDesktopRefresh(s.desktopRefreshCh) })

	s.miOpenAppFolder.Click(func() { go s.handleOpenAppFolder() })
	s.miOpenDataFolder.Click(func() { go s.handleOpenDataFolder() })
	s.miOpenGatewayFolder.Click(func() { go s.handleOpenGatewayFolder() })
	s.miMetricsRW.Click(func() { go s.toggleMetricsRemoteWrite() })
}

func (s *trayState) makeProbe() probeFunc {
	pidPath := gwPidFile(s.gwHome)
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

	model := gatewayMenuForOutput(out, s.dashboardURL)
	applyGatewayMenuModel(&s.gatewayMenuCache, model, s.nativeGatewayMenuRenderOps())

	s.notifyTransition(prev, out.State)
}

func (s *trayState) nativeGatewayMenuRenderOps() gatewayMenuRenderOps {
	return gatewayMenuRenderOps{
		// T-3 fix (REL-TRAY-03): the icon and tooltip are the primary
		// gateway-death signal; notify() remains secondary.
		setIcon:    setIconForState,
		setTooltip: systray.SetTooltip,
		header:     nativeMenuItemRenderOps(s.miHeader),
		subheader:  nativeMenuItemRenderOps(s.miSubheader),
		start:      nativeMenuItemRenderOps(s.miStart),
		stop:       nativeMenuItemRenderOps(s.miStop),
		restart:    nativeMenuItemRenderOps(s.miRestart),
	}
}

func nativeMenuItemRenderOps(item *systray.MenuItem) menuItemRenderOps {
	return menuItemRenderOps{
		setTitle: item.SetTitle,
		setEnabled: func(enabled bool) {
			setMenuItemEnabled(item, enabled)
		},
		setVisible: func(visible bool) {
			setMenuItemVisible(item, visible)
		},
	}
}

func setMenuItemEnabled(item *systray.MenuItem, enabled bool) {
	if enabled {
		item.Enable()
		return
	}
	item.Disable()
}

func setMenuItemVisible(item *systray.MenuItem, visible bool) {
	if visible {
		item.Show()
		return
	}
	item.Hide()
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
	title := "Gateway"
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
	res := runWrapper(s.installDir, s.gwHome, "start")
	if res.ExitCode != 0 || res.Err != nil {
		notify("Gateway", "Failed to start: "+firstLine(res.Stderr))
	}
}

func (s *trayState) handleStop() {
	res := runWrapper(s.installDir, s.gwHome, "stop")
	if res.ExitCode != 0 || res.Err != nil {
		notify("Gateway", "Failed to stop: "+firstLine(res.Stderr))
	}
}

func (s *trayState) handleRestart() {
	s.setStartedNow()
	res := runWrapper(s.installDir, s.gwHome, "restart")
	if res.ExitCode != 0 || res.Err != nil {
		notify("Gateway", "Failed to restart: "+firstLine(res.Stderr))
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

	res := runWrapper(s.installDir, s.gwHome, "support")
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
		logDir := filepath.Join(s.installDir, "support")
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
	// last. Companion fix in scripts/gw.ps1 routes informational
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
		path = filepath.Join(s.installDir, "support", "latest"+bundleExt())
	}

	notify("Gateway", "Support bundle saved:\n"+path)
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
		notify("Gateway", "Could not change login setting: "+err.Error())
		return
	}
	if err := saveTrayConfig(gwTrayConfigPath(s.gwHome), cfg); err != nil {
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
	if err := saveTrayConfig(gwTrayConfigPath(s.gwHome), cfg); err != nil {
		slog.Error("save tray.json", "err", err)
	}
}

// resolveMetricsRWEnabled decides the checkbox's initial state: the persisted
// tray.json override wins when present, otherwise the env default
// (GW_METRICS_REMOTE_WRITE_ENABLED). This is the "checkbox persists, env is
// default" model.
func resolveMetricsRWEnabled(cfg TrayConfig, gwHome string) bool {
	if cfg.MetricsRemoteWriteEnabled != nil {
		return *cfg.MetricsRemoteWriteEnabled
	}
	return loadRemoteWriteConfig(gwHome).EnvEnabled
}

// toggleMetricsRemoteWrite flips the live atomic (so the writer goroutine picks
// it up on its next cycle) and persists the concrete on/off to tray.json so the
// choice survives restarts and thereafter wins over the env default.
func (s *trayState) toggleMetricsRemoteWrite() {
	newVal := !s.metricsRWEnabled.Load()
	s.metricsRWEnabled.Store(newVal)
	if newVal {
		s.miMetricsRW.Check()
	} else {
		s.miMetricsRW.Uncheck()
	}
	s.mu.Lock()
	s.cfg.MetricsRemoteWriteEnabled = &newVal
	cfg := s.cfg
	s.mu.Unlock()
	if err := saveTrayConfig(gwTrayConfigPath(s.gwHome), cfg); err != nil {
		slog.Error("save tray.json", "err", err)
	}
}

func (s *trayState) showAbout() {
	gwID := resolveGatewayID(s.gwHome, os.Getenv)
	if gwID == "" {
		gwID = "(unknown — start the gateway once)"
	}
	body := fmt.Sprintf("Version: %s\nCommit: %s\nGateway ID: %s\nInstall: %s\nGo: %s",
		version.Version, version.Commit(), gwID, s.installDir, runtime.Version())
	infoDialog("About Gateway", body)
}

// copyGatewayID copies the persisted Gateway ID to the clipboard so support
// can ask a user to read it off the tray. Silent on success (mirrors the
// "Copy health URL" item); shows a dialog when no id exists yet rather than
// copying an empty string.
func (s *trayState) copyGatewayID() {
	if id := resolveGatewayID(s.gwHome, os.Getenv); id != "" {
		copyToClipboard(id)
	} else {
		infoDialog("Gateway ID", "No Gateway ID found yet.\nStart the gateway once, then try again.")
	}
}
