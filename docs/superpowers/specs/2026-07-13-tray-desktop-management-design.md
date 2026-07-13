# OTTO Tray — Desktop-App Management — design spec

**Date:** 2026-07-13
**Status:** Approved design (pending spec review) → next: writing-plans
**Scope:** Extend the existing `otto-tray` (macOS menu-bar / Windows system-tray launcher) so it can **install**, **start**, **stop**, and show the **running state** of the OTTO **desktop app** (the Electron app from `cmetech/otto`) — alongside its existing gateway-lifecycle features.
**Related:** `docs/superpowers/specs/2026-06-06-tray-menubar-launcher-design.md` (the original tray), the OTTO multi-brand `brand.json` (roadmap P4 discovery seam, shipped 2026-07-13 in `cmetech/hermes-agent`), `cmetech/otto` desktop installer.

## Problem

Today `otto-tray` manages only the **gateway** lifecycle (start/stop/restart via the
`otto-gw`/`otto-gw.ps1` wrappers + a 3-second health FSM). It has no awareness of the OTTO
**desktop app** — a separate Electron product installed from `cmetech/otto`. A user who has the
gateway + tray installed still has to install and launch the desktop app by hand, and has no
single place that shows whether the desktop is running.

This is roadmap **P4** ("extend otto-tray to launch the chosen branded desktop + manage
gateway"). The `brand.json` shipped in the hermes-agent multi-brand work is exactly the
discovery seam this phase consumes.

## Goal

From the tray a user who has installed the gateway + tray can, for the OTTO desktop app:
1. **Trigger install** — run the documented OTTO desktop installer one-liner (assume OTTO brand).
2. **Start / stop** it, and see whether it is currently **running**.

## Non-goals (v1)

- **Desktop auto-update from the tray.** The desktop self-updates (release-mode in-app update
  already exists in hermes-agent). The tray only installs and launches.
- **A loop24 brand-switching UI.** `brand.json` makes the tray brand-derivable, but v1 **assumes
  OTTO** (hardcoded defaults + opportunistic `brand.json` read). No brand picker.
- **Coworker / agent management.** The roadmap mentions it; out of scope here.
- **Deep desktop health.** Only `not-installed / stopped / running / installing` — no pool,
  hooks, or version surfaced for the desktop (the gateway FSM keeps its richer signal).
- **Replacing the desktop's own installer.** The tray execs the published one-liner; it does not
  reimplement download/verify/extract.
- **Linux.** The tray is darwin+windows only (unchanged).
- **Changing the gateway.** No gateway binary changes; the gateway stays cgo-free.

## Constraints inherited from the project

- **Gateway stays cgo-free.** All new code lives under `cmd/otto-tray/` (already `//go:build
  darwin || windows`, already carries cgo for the systray). No gateway package imports the tray.
- **`gosec` G204 (subprocess spawn) is a non-negotiable trust gate.** Install/start/stop shell
  out. Every exec uses a **fixed argv shape**; any brand-derived token (process name, app path)
  is **validated against a strict pattern** before use; the install command is the **constant**
  `cmetech/otto` one-liner in v1 (no tainted input). `#nosec G204` annotations carry a written
  justification where the linter still flags a constant-shape spawn.
- **One tray binary, cross-compiled.** No new external runtime deps beyond what a fixed shell-out
  needs (`powershell`/`tasklist`/`taskkill` on Windows; `sh`/`curl`/`open`/`pgrep`/`osascript`
  on macOS — all present by default).
- **Tray is a thin driver, not a second source of truth.** As with the gateway wrappers, the
  tray delegates to the desktop's own installer + the OS to launch/kill; it holds no desktop
  config.

## Decisions (locked in brainstorming, 2026-07-13)

| # | Decision | Choice |
|---|---|---|
| Platforms | **Windows + macOS** (the tray already targets both; detection/launch logic exists for both). |
| Running-detection | **Process-name check** — Windows: is `OTTO.exe` running; macOS: is the `OTTO` app process running. No desktop-side change required. |
| Install mechanism | **Exec the documented one-liner** (`irm …/cmetech/otto/main/install.ps1 | iex` / `curl …/install.sh | sh`). |
| Brand resolution | **Hardcode OTTO + opportunistically read `brand.json` from the installed app's resources** to confirm/derive identity; future-proofs loop24. |
| Stop | **Requires a confirm dialog** (stopping closes the user's app). |
| brand.json location | **App resources** — `OTTO.app/Contents/Resources/brand.json` (mac) / `…\Programs\OTTO\resources\brand.json` (win), NOT `~/.otto`. |
| Packaging | **Tray stays bundled with the gateway — 2 installers total: `[gw+tray]` (via the gateway one-liner) + `[coworker]` (via `cmetech/otto`).** No separate tray repo/installer. The tray (shipped with the gateway) orchestrates the coworker install. Rationale: tray↔gateway is tightly coupled (wrappers/PID/health) so same-repo versioning protects the contract; tray↔coworker is loose and already cross-repo. |

## Architecture

### 1. New menu section — "OTTO Desktop"

Inserted into the existing menu (below the gateway Start/Stop/Restart + dashboard block, above
Preferences), driven by a small desktop state machine independent of the gateway FSM:

```
   ●  OTTO Gateway · running          (existing)
      …gateway items…
      ─────────────────────────────
   ●  OTTO Desktop · running          (or ○ not running · ⬇ not installed · ⟳ installing…)
      Install OTTO Desktop…           (shown ONLY when not installed)
      Start OTTO Desktop              (enabled when installed & stopped)
      Stop OTTO Desktop               (enabled when installed & running)
      ─────────────────────────────
      Preferences ▸                   (existing)
      …
```

Menu-item visibility/enablement is recomputed from the desktop state on each poll tick, mirroring
how `applyState` toggles the gateway items.

### 2. Desktop state machine

```
DesktopState = NotInstalled | Stopped | Running | Installing
```

- **`NotInstalled`** — no installed app found (see detection). Only `Install…` is shown.
- **`Stopped`** — app found on disk, process not running. `Start` enabled.
- **`Running`** — app found and its process is alive. `Stop` enabled.
- **`Installing`** — transient, set while the install subprocess runs (item disabled + spinner);
  cleared when the install completes and the next poll re-detects.

The desktop probe is folded into the **existing 3-second poller tick** (it already runs) so no
second goroutine is added. The probe is cheap: a few path stats + one process lookup. The poll
emits desktop state alongside gateway state; `applyState` updates both sections.

### 3. Detection

**Installed?** Mirror the hermes CLI's `_installed_desktop_app()` (proven logic) — check the
brand's well-known install locations, returning the launchable path or empty:
- **Windows:** `%LOCALAPPDATA%\Programs\OTTO\OTTO.exe`, then `%PROGRAMFILES%`/`%PROGRAMFILES(X86)%`
  `\Programs\OTTO\OTTO.exe` and `\OTTO\OTTO.exe`.
- **macOS:** `/Applications/OTTO.app`, then `~/Applications/OTTO.app`.

The app **name** (`OTTO`) is the hardcoded default; once found, `brand.json` (below) can override
it for a future brand. Detection uses the default name to *find* the app; `brand.json` refines
identity for *subsequent* operations.

**Running?** Process-name check (no desktop-side signal needed):
- **Windows:** `tasklist /FI "IMAGENAME eq OTTO.exe" /NH` and test for a matching row (or a Win32
  `CreateToolhelp32Snapshot` enumeration — implementation choice in the plan; `tasklist` is
  simplest and dependency-free). Alive iff a process named `OTTO.exe` exists.
- **macOS:** `pgrep -f "OTTO.app/Contents/MacOS/OTTO"` (the packaged binary path is
  distinctive — avoids matching an unrelated process literally named "OTTO"). Alive iff a match.

The process name/match string is derived from the resolved brand identity and **validated**
(`^[A-Za-z0-9 ._-]+$`) before being passed to `tasklist`/`pgrep` (gosec G204).

### 4. Brand resolution (`brandIdentity`)

A small resolver returns the identity the desktop actions need:

```
brandIdentity {
  DisplayName   string   // "OTTO"
  WinExeName    string   // "OTTO.exe"
  MacAppName    string   // "OTTO.app"
  MacProcMatch  string   // "OTTO.app/Contents/MacOS/OTTO"
  InstallRepo   string   // "cmetech/otto"  (releasesRepo)
}
```

- **Default** = OTTO (all fields hardcoded), so the tray works **before** the desktop is installed
  (the `Install…` path needs no app on disk).
- **Refinement** = once the app is found on disk, read `brand.json` from the **app's resources**
  (shipped there via electron-builder `extraResources` in the Plan-6 hermes work):
  - **macOS:** `<AppPath>/Contents/Resources/brand.json`
  - **Windows:** `<dir of OTTO.exe>\resources\brand.json`
  Fields consumed: `displayName` (→ names), `releasesRepo` (→ install URL). A malformed/missing
  file → silently keep the OTTO defaults (fail-safe). The read is best-effort and never blocks a
  menu action.

> **v1 note (assume OTTO):** the **install** command uses the **constant** `cmetech/otto`
> one-liner (not derived from a possibly-tainted `brand.json`), matching the "assume OTTO brand"
> decision and keeping the install path free of injectable input. `brand.json` refinement drives
> the *display name* and *detection*, and reserves `releasesRepo`-derived install for a future
> brand phase (guarded by strict `owner/repo` validation when enabled).

### 5. Actions

All three run off the tray's main thread via `go` handlers (like the gateway handlers), report
via notifications, and re-poll on completion.

| Action | macOS | Windows |
|---|---|---|
| **Install** | `/bin/sh -c "curl -fsSL https://raw.githubusercontent.com/cmetech/otto/main/install.sh | sh"` | `powershell -NoProfile -Command "irm https://raw.githubusercontent.com/cmetech/otto/main/install.ps1 | iex"` |
| **Start** | `open "/Applications/OTTO.app"` (or the resolved app path) | run the resolved `OTTO.exe` **detached** (new process group, as the gateway launches do) |
| **Stop** | `osascript -e 'quit app "OTTO"'` → fallback `pkill -f "OTTO.app/Contents/MacOS/OTTO"` | `taskkill /IM OTTO.exe /T` (graceful WM_CLOSE) → fallback `taskkill /IM OTTO.exe /T /F` |

- **Install** — confirm dialog ("Install the OTTO desktop app? This downloads and runs the
  official installer."), then set `Installing`, run the one-liner **async** with a generous
  timeout (installs are slow: fetch + unpack + first-run bootstrap), notify success/failure with
  the tail of stderr on failure, then re-poll. The command is a **constant string** per OS
  (gosec-clean).
- **Start** — mirror `_launch_installed_desktop_app`: macOS `open` the `.app`; Windows spawn the
  `.exe` detached (`CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS`) so quitting the tray never
  signals the desktop. The exe/app path comes from detection (validated is-file on the
  allowlisted locations), not user input.
- **Stop** — **confirm dialog** first ("Stop the OTTO desktop app? Any unsaved work in it may be
  lost."), then graceful quit, then a forced fallback after a short timeout if the process is
  still alive on the next probe. The process name / match string is validated before the spawn.

### 6. Poller & UI integration

- Extend the existing `probeFunc`/poller output to carry a `DesktopState` (+ the resolved app
  path). The desktop probe = `detectInstalled()` + (if installed) `detectRunning()`.
- `applyState` gains a desktop block: set the desktop header line + icon glyph, and
  show/enable the right item (`Install…` xor `Start` xor `Stop`) for the state.
- Notifications: `running → not-running` (unexpected exit) raises a toast, matching the gateway's
  debounced transition rule; install completion always notifies.

## Data flow

```
poll tick (3s)
   ├─ gateway probe  (existing)  ─────────────► gateway FSM/menu (unchanged)
   └─ desktop probe (new):
        detectInstalled(brandIdentity) ── app path ──► read <appResources>/brand.json (refine identity)
             │ not found                         │ found + running?  (tasklist/pgrep)
             ▼                                    ▼
        DesktopState=NotInstalled          Stopped / Running
             │                                    │
             ▼                                    ▼
        [Install OTTO Desktop…]            [Start] / [Stop (confirm)]
             │
   exec constant one-liner (cmetech/otto), async, notify, re-poll
```

## Error handling

- **Detection is best-effort and fail-safe.** A stat/process-list error → treat as
  not-installed / not-running (never crash the poller). A malformed `brand.json` → keep OTTO
  defaults.
- **Actions never wedge the UI.** Handlers run in goroutines; install runs async with a spinner
  state; a non-zero exit surfaces a notification (first/last stderr line) and reverts state on
  the next poll.
- **gosec:** constant install command; validated brand tokens; allowlisted app paths; detached
  spawn. `#nosec G204` only where a constant-shape spawn is still flagged, each with a comment.

## Testing (following the existing `regression_rel_tray_*` + unit style)

- **Installed-detection:** table tests over present/absent app paths per OS (temp dirs), asserting
  the returned path and `NotInstalled` when absent.
- **Running-detection:** inject a stub "process lister" so the check is testable headless; assert
  Running iff the stub reports the (validated) process name.
- **Brand resolver:** default OTTO when no `brand.json`; override `displayName`/`releasesRepo`
  from a fixture `brand.json`; malformed file → defaults (fail-safe).
- **Command construction:** assert the exact argv for install/start/stop per OS **without
  executing** (constant install string; correct app path in start; correct kill target in stop);
  assert the process name/match string passes the validation pattern and a crafted bad name is
  rejected.
- **State/menu mapping:** given each `DesktopState`, assert which item is shown/enabled.
- **gosec/golangci** clean on `cmd/otto-tray/` (the project's trust gate).
- **Manual E2E:** fresh box → install gateway+tray → tray shows "OTTO Desktop · not installed" →
  Install → app appears → Start → "running" → Stop (confirm) → "not running". Mirror on the other
  OS.

## Distribution / rollout

- Work lands on **`github.com/cmetech/otto-gateway`** (`origin`; the repo also dual-pushes to the
  GitLab Ericsson mirror). No install-script changes: `scripts/install.{sh,ps1}` **already**
  package `otto-tray` and already stop a running tray before extraction, so the updated tray
  self-updates on the next gateway install/upgrade.
- Ship by rebuilding the tray (darwin arm64/amd64 + windows amd64) and cutting a gateway release;
  users get it via the documented gateway one-liner
  (`irm …/cmetech/otto-gateway/main/scripts/install.ps1 | iex` / `curl …/install.sh | sh`).
- No release cut of the desktop or hermes side is required — this consumes the already-shipped
  `brand.json`.

## Risks & mitigations

| Risk | Mitigation |
|---|---|
| `gosec` G204 flags the shell-outs | Fixed argv shapes; constant install command; validated brand tokens; allowlisted app paths; justified `#nosec` where a constant spawn still trips the rule |
| Process-name check is coarse (matches an unrelated "OTTO") | macOS matches the distinctive `OTTO.app/Contents/MacOS/OTTO` path; Windows matches the exact `OTTO.exe` image name — collisions are unlikely for a packaged app. Documented as a v1 limitation |
| Install one-liner runs a remote script at click-time | It is the **same** command users run by hand from the README; confirm dialog first; constant URL (no injection). Auditable-asset-download is a documented later option |
| Stop is destructive (closes the user's app) | Confirm dialog + graceful-quit-first, forced only as a fallback |
| `brand.json` not present (older desktop build) | Fail-safe to OTTO defaults; detection still works via hardcoded paths |
| Windows resources path assumption (`…\OTTO\resources\brand.json`) wrong | Verify against a real packaged install in the plan phase; fall back to defaults if absent (non-fatal) |

## Open items (deferred, not blocking)

- Brand-derived install URL from a validated `brand.json releasesRepo` (needed only for a second
  brand; v1 is constant `cmetech/otto`).
- A desktop "version" line / update affordance in the tray (the desktop self-updates today).
- Auditable direct-asset install (vs the remote one-liner) if a hardened-endpoint policy later
  requires it.

## Implementation phases (for writing-plans to expand)

1. **Brand identity + detection** — `brandIdentity` resolver (OTTO defaults + app-resources
   `brand.json` refinement), `detectInstalled` (well-known paths), `detectRunning` (validated
   process check with an injectable lister). Unit-tested headless.
2. **Desktop state + poller/menu wiring** — `DesktopState`, fold the desktop probe into the
   existing tick, add the "OTTO Desktop" menu section, and the state→item mapping in `applyState`.
3. **Actions** — Install (async, constant one-liner, spinner + notify), Start (detached launch),
   Stop (confirm + graceful→forced). Command-construction + state-mapping tests; gosec clean.
4. **E2E + docs** — manual cross-OS smoke; update the tray README/section; note the P4 tie-in.
