# OTTO Tray — Menu-Bar / System-Tray Launcher

**Date:** 2026-06-06
**Status:** Design — pending implementation plan
**Scope:** New `otto-tray` binary that provides a macOS menu-bar and Windows
system-tray UI for controlling the OTTO Gateway lifecycle. Linux deferred.

## Goal

Give operators a one-click way to start, stop, restart, and inspect the
OTTO Gateway from the macOS menu bar or the Windows system tray, without
introducing a second source of truth for lifecycle, config, or installation.

## Non-goals

- **Replacing `otto-gw` / `otto-gw.ps1`.** Those wrappers stay the canonical
  lifecycle implementation. The tray drives them; it does not duplicate
  their logic.
- **Replacing `/admin`.** All configuration UI (PII mode, hooks, env)
  remains on the existing admin dashboard. The tray menu is intentionally
  lean.
- **Linux support in v1.** Most Linux deployments are headless; the CLI is
  fine there. Adding `libayatana-appindicator` to the cross-compile story
  is deferred until there is concrete demand.
- **Code-signing / notarization.** Unsigned binaries with quarantine-bit
  stripping match the gateway's current distribution model. Apple
  Developer ID signing is a separate, later decision.
- **A full GUI window.** No settings dialog, no log viewer, no embedded
  browser. The menu opens links into the existing dashboard.

## Constraints inherited from the project

- **`otto-gateway` stays cgo-free.** CLAUDE.md is explicit: the moment cgo
  enters the gateway, the cross-compile-from-macOS-with-vanilla-`go build`
  property collapses. The tray therefore ships as a separate binary, and
  cgo is contained to that binary on darwin and windows only.
- **One-line `curl … | sh` install must remain one line.** No extra steps
  for users who don't want the tray. The tray binary is dropped beside the
  gateway binary, ready to launch when wanted, but the installer touches
  no login-items by default.
- **Env-var compatibility with the Node version** (`KIRO_CMD`, `POOL_SIZE`,
  `AUTH_TOKEN`, etc.) is owned by the wrapper scripts. The tray inherits
  this by delegating to them.

## Architecture

A separate `otto-tray` binary lives in the same install root as
`otto-gateway`, alongside the existing wrappers:

```
~/.otto-gw/
├─ bin/
│   ├─ otto-gateway        (existing, pure Go, no cgo)
│   └─ otto-tray           (new, cgo, darwin + windows only)
├─ scripts/
│   ├─ otto-gw             (existing bash wrapper, ~1900 LOC)
│   ├─ otto-gw.ps1         (existing PowerShell wrapper, ~1500 LOC)
│   └─ install.sh / install.ps1
└─ .otto/gw/otto-gateway.pid
```

Component responsibilities:

| Component         | Owns                                                                              |
|-------------------|-----------------------------------------------------------------------------------|
| `otto-gateway`    | HTTP surfaces, ACP worker pool, hooks, PII, `/admin` UI. Unchanged.               |
| `otto-gw[.ps1]`   | Env-file precedence, PII flag mapping, PID file, kiro-cli reaping, readiness wait. Unchanged. |
| `otto-tray` (new) | Menu-bar/tray icon, status FSM (3s poll), subprocess invocation of the wrappers, login-item registration, tray-local prefs at `~/.otto-gw/tray.json`. |

**Library choice:** `energye/systray` (actively maintained fork of
`getlantern/systray`). Mature on macOS (Cocoa `NSStatusBar` via cgo) and
Windows (Win32 `Shell_NotifyIcon`). Linux support exists in the library
but is not built or shipped in v1.

**Boundary rationale.** The cgo cost is real but contained: only the tray
binary carries it, and only for darwin + windows. The gateway's build
matrix is unchanged — vanilla `go build` from the dev box, no platform
runner needed for the core release artifact.

## Menu structure

The tray menu is deliberately minimal. Configuration belongs on `/admin`.

```
●  OTTO Gateway · running        (or ○ stopped · ▲ error · ▲ degraded)
   127.0.0.1:18080 · uptime 1h 23m
   pool 4/4 ready · hooks 5 ok
   ─────────────────────────────
   Start gateway                 (enabled when stopped)
   Stop gateway                  (enabled when running)
   Restart gateway               (enabled when running)
   ─────────────────────────────
   Open dashboard                → http://127.0.0.1:18080/admin
   Copy health URL
   ─────────────────────────────
   Preferences ▸
       ☐ Launch tray at login
       ☐ Start gateway when tray launches
   About OTTO Gateway…
   ─────────────────────────────
   Quit OTTO Tray
```

Log access lives on `/admin`. The tray never opens a log file directly.

## Status FSM

A goroutine polls every 3 seconds:

| State      | Icon          | Detection                                                                       |
|------------|---------------|---------------------------------------------------------------------------------|
| `unknown`  | grey dot      | Initial state, before first probe.                                              |
| `stopped`  | hollow circle | PID file absent, or points at a dead PID.                                       |
| `starting` | spinner       | PID alive, `/health` not yet 2xx. Bounded to a 30s window (kiro warmup budget). |
| `running`  | filled green  | `/health` returns 2xx.                                                          |
| `degraded` | amber         | `/health` 2xx but `/health/pool` reports no ready slots, or `/health/hooks` reports a hook in error. |
| `error`    | red triangle  | PID alive but `/health` 5xx or unreachable after 3 consecutive failures.        |

The poller reads the PID file at
`<install_root>/.otto/gw/otto-gateway.pid` for liveness and uses
`/health` plus `/admin/api/snapshot` for the richer signal. Both already
exist in the gateway — no gateway changes required.

State transitions debounce OS notifications: only `running → error` and
`running → stopped` raise a toast. UI work is dispatched on the tray's
main thread; the poller never blocks it.

## Subprocess contract for lifecycle

Tray actions exec the existing wrappers as subprocesses with a 30-second
timeout (matches the wrapper's own readiness budget). No flags are
passed — env files own all config, exactly as a CLI user would invoke
the wrapper. This is the property that prevents the tray from becoming a
second config surface.

| Action  | macOS                | Windows                                              |
|---------|----------------------|------------------------------------------------------|
| Start   | `otto-gw start`      | `pwsh -NoProfile -File otto-gw.ps1 start`           |
| Stop    | `otto-gw stop`       | `pwsh -NoProfile -File otto-gw.ps1 stop`            |
| Restart | `otto-gw restart`    | `pwsh -NoProfile -File otto-gw.ps1 restart`         |

Wrapper path is resolved relative to the tray executable
(`os.Executable()` → walk up one directory from `bin/`). `OTTO_HOME`
overrides for dev-out-of-worktree runs.

Process spawn detaches the wrapper's process group so quitting the tray
does not signal the gateway:

- **macOS:** `SysProcAttr{Setpgid: true}`.
- **Windows:** `SysProcAttr{CreationFlags: CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS}`.

Captured stdout/stderr from a non-zero wrapper exit become the body of an
OS notification: "Gateway failed to start" + the first stderr line + an
"Open dashboard" button.

**Port discovery.** The dashboard URL defaults to
`http://127.0.0.1:18080`. If the user overrode `HTTP_ADDR`, the tray
reads it from the resolved env files using the wrapper's precedence
chain: `.otto-gw.overrides.env` → `.otto-gw.env` → process env. A small
read-only dotenv reader (~50 LOC) handles this; the tray never writes
env files.

## Tray-local config

A single small file at `~/.otto-gw/tray.json` holds tray-only preferences
(it does NOT shadow gateway config — only UI toggles):

```json
{
  "launch_tray_at_login": false,
  "start_gateway_when_tray_launches": false
}
```

Defaults are both `false`. The file is written when the user toggles
either preference. Missing file = both defaults.

## Autostart & login-item registration

Both toggles default off. The installer makes zero changes to login
behavior; the curl-equivalent install promise is preserved.

**macOS — `~/Library/LaunchAgents/io.cmetech.otto-tray.plist`**

When "Launch tray at login" toggles on:

1. Tray writes a plist with `RunAtLoad=true`, `KeepAlive=false`,
   `ProgramArguments=[<install_root>/bin/otto-tray]`.
2. `launchctl bootstrap gui/<uid> <plist>` (modern macOS).
   Fallback to `launchctl load` on older macOS.

When toggled off:
`launchctl bootout gui/<uid>/io.cmetech.otto-tray` + delete the plist.

No `.app` bundle in v1 — the status icon shows up in the menu bar
without one. A `.app` wrapper is a possible polish-step later, out of
scope for v1.

**Windows — `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`**

When toggled on: write `Run\OttoTray = "<install_root>\bin\otto-tray.exe"`.
When toggled off: delete the value.

`HKCU` is deliberate — no admin elevation, matches the no-admin install
model. Windows Service and Task Scheduler are explicitly avoided for
the same reason.

**"Start gateway when tray launches"**

Read from `tray.json` at startup. If true, the tray fires the same
`Start gateway` action ~500ms after the icon renders. The delay ensures
a failed start does not look like the tray itself failed to launch.

**First-run onboarding.** First time the tray launches and `tray.json`
does not exist, a one-time toast offers: "Launch OTTO Tray automatically
at login? You can change this in Preferences." Two buttons: Yes / Not
now. We never ask again — `tray.json` exists after the answer.

**Race condition handling.** If the tray and an external `otto-gw start`
race on first launch, the wrapper's PID-file guard rejects the second
start with a clear error. The tray surfaces that error in a notification
rather than treating it as fatal.

## Failure surfaces

Failures the user actually sees in the UI:

| Failure                                | UI behavior                                                                                   |
|----------------------------------------|------------------------------------------------------------------------------------------------|
| `start` wrapper exits non-zero         | Notification: "Gateway failed to start" + first stderr line + "Open dashboard" button.        |
| `/health` unreachable for 3 polls      | Header turns red, status line shows "error: connection refused" (or the last HTTP body snippet). |
| Wrapper missing entirely               | Header reads "OTTO Gateway not installed". Start/Stop/Restart disabled. Only Quit enabled.    |
| Port override conflict (HTTP_ADDR set) | Dashboard URL in the menu updates automatically; header reflects the new `host:port`.          |

## Packaging & install

**Release artifacts gain one binary per supported platform:**

```
otto_gateway-darwin-arm64-vX.Y.Z.tar.gz   # adds bin/otto-tray
otto_gateway-darwin-amd64-vX.Y.Z.tar.gz   # adds bin/otto-tray
otto_gateway-windows-amd64-vX.Y.Z.{tar.gz|zip}  # adds bin/otto-tray.exe
otto_gateway-linux-amd64-vX.Y.Z.tar.gz    # unchanged; no tray
```

The Windows archive format (tar.gz vs zip) is whatever the project
already ships and is not changed by this design; verify in the plan
phase.

`SHA256SUMS-vX.Y.Z.txt` gains the new binary's row. The existing
checksum verification in `install.sh` iterates rows by name and stays
correct.

**Build matrix additions:**

| Target            | Toolchain                                  | Notes                                                         |
|-------------------|--------------------------------------------|---------------------------------------------------------------|
| darwin/arm64      | macOS runner, cgo on, Xcode CLT            | Matches dev box.                                              |
| darwin/amd64      | macOS runner, cgo on, `GOARCH=amd64`       | Cross-arch on Apple Silicon works with system SDK.            |
| windows/amd64     | windows runner, cgo on (mingw or msvc)     | energye/systray uses cgo for the win32 message loop.          |

The gateway's own build matrix is **unchanged** — still vanilla
`go build` from macOS, no cgo, no platform runner.

**install.sh deltas (minimal):**

1. After extraction, on darwin only:
   `xattr -d com.apple.quarantine "$OTTO_HOME/bin/otto-tray" 2>/dev/null || true`
   (same treatment the gateway binary already gets).
2. No symlink for `otto-tray` in `~/.local/bin` — it is a GUI launcher,
   not a CLI.
3. Footer message gains one line:

   ```
   Next steps:
     otto-gw start                       # launch the gateway from the terminal
     otto-gw status                      # verify it is up
     open ~/.otto-gw/bin/otto-tray       # or, launch the menu-bar app
     curl -sf http://127.0.0.1:18080/health
   ```

**install.ps1 deltas (minimal):**

Same shape — drops `otto-tray.exe`, prints a footer line showing
`Start-Process "$env:USERPROFILE\.otto-gw\bin\otto-tray.exe"`.

**One-line install promise preserved.** Still one command, still no
admin, still no login-item changes by default.

## Uninstall

The existing uninstall story (`rm -rf ~/.otto-gw` + remove the symlink)
gains two cleanup steps:

- **macOS:** remove the LaunchAgent plist if present
  (`launchctl bootout` + `rm`).
- **Windows:** remove the `HKCU\…\Run\OttoTray` value if present.

These are wrapped in an `otto-tray --uninstall` flag that does only the
login-item cleanup (it does not delete the binary; the user's `rm` or
installer-uninstall does that). The uninstall step is documented in
README.

## Testing

| Area                          | Test                                                                                       |
|-------------------------------|--------------------------------------------------------------------------------------------|
| FSM transitions               | Table-driven unit tests on the poller with a mock `httpClient` and a stubbed PID-file reader. |
| Wrapper resolution            | Unit test for `os.Executable()` → install-root walk with both packaged and worktree layouts. |
| Dotenv reader                 | Unit tests covering precedence (overrides > base > env), comments, blank lines, quoting.   |
| Subprocess invocation         | Integration test (skipped if wrapper missing) that runs `otto-gw env` and asserts the tray captures stdout. |
| LaunchAgent toggle (mac)      | Integration test gated by `runtime.GOOS == "darwin"`: write plist, `launchctl print`, assert presence; toggle off, assert absence. |
| Run-key toggle (win)          | Same shape, gated by `runtime.GOOS == "windows"`: `reg query` after toggle. |
| End-to-end smoke              | Manual: install fresh on a clean macOS user → double-click `otto-tray` → click Start → see green → click Open dashboard → quit. Mirror on Windows. |

The tray binary's tests live under `cmd/otto-tray/` and are excluded
from the gateway's default `go test ./...` only by build tags
(`//go:build darwin || windows`) so headless Linux CI does not try to
build cgo dependencies.

## Open questions to resolve in the plan phase

- Whether the Windows release archive is tar.gz or zip today.
- Exact bundle identifier for the LaunchAgent (`io.cmetech.otto-tray`
  proposed; confirm against any existing `io.cmetech.*` registrations).
- Whether to ship a 16×16 + 32×32 PNG icon set in v1 or use
  energye/systray's default template-image path on mac (which renders
  correctly in both light and dark menu bars).

## Out of scope (explicit deferrals)

- Linux tray.
- Code-signing, notarization, Microsoft SmartScreen reputation.
- `.app` bundle on macOS.
- Settings UI inside the tray (PII, port, auth, hooks all stay on
  `/admin`).
- Multi-gateway-instance support (one tray talks to one install root).
- Auto-update of the gateway from the tray.

## Risk register

| Risk                                                                    | Mitigation                                                                                                          |
|-------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------|
| cgo creeping into the gateway via accidental shared package             | Tray code lives entirely under `cmd/otto-tray/`; no gateway package imports it. Go module structure prevents reverse imports. |
| LaunchAgent / Run-key cleanup left behind after uninstall               | Documented `otto-tray --uninstall` step; README and install footer point at it.                                    |
| Wrapper script behavior drift breaking the tray's parsing               | Tray treats wrapper output as opaque except for exit code; only the first stderr line is shown to the user; no structured parsing. |
| Polling cost (3s × snapshot endpoint)                                   | `/admin/api/snapshot` is already designed for 30s dashboard polling; 3s is well within its budget. Tray fetches only on visible-menu transitions if needed. |
| Mac Gatekeeper friction on first launch                                 | Existing `xattr -d com.apple.quarantine` covers it for the install-via-curl path. Document the one-time "downloaded from internet" prompt if a user double-clicks before that runs. |
