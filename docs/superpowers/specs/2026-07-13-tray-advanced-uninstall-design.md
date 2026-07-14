# Tray "Advanced ▸ Uninstall" — Design

**Date:** 2026-07-13
**Component:** `cmd/otto-tray/` (OTTO Gateway repo) — **tray-only**, no changes to `hermes-agent`.
**Status:** Design (awaiting review)

## 1. Goal

Add an **Advanced ▸** submenu to the OTTO/LOOP24 system tray that lets a user
uninstall the OTTO/LOOP24 **desktop app** (and optionally the hermes agent and
its data) from the tray, mirroring the 3 options the desktop app exposes in
Settings → About → Danger Zone. The tray drives the **same** uninstall engine the
desktop uses (`python -m hermes_cli.uninstall --mode <mode>`) and then **validates**
the install is gone.

**Constraint (locked):** Only the tray (this repo) changes. No hermes-agent edits.
The tray reproduces the desktop's orchestration in Go.

## 2. The three options (desktop parity)

Exact labels + semantics from `hermes-agent/apps/desktop/src/app/settings/uninstall-section.tsx`:

| Menu label | mode | Removes | Keeps |
|---|---|---|---|
| Uninstall Chat GUI only | `gui` | desktop app bundle + Electron userData + source-built GUI artifacts | hermes agent, config, chats |
| Uninstall GUI + agent, keep my data | `lite` | above + hermes agent code (`hermes-agent/`) + PATH/symlinks/services/shortcuts | `HERMES_HOME` data (config, `.env`, sessions, secrets) |
| Uninstall everything | `full` | above + **all** `HERMES_HOME` data | nothing |

**Out of scope (confirmed):** none of these remove **OTTO Gateway itself**
(`otto-gw`/tray under `~/.otto-gw`) — that is a separate install with its own
uninstall. The confirm dialog states this explicitly. No gateway self-removal, so
no detached-script self-delete trick is needed.

## 3. Menu / UX

- **Layout (confirmed):** a single `Advanced` item that opens a **submenu**
  (energye/systray `AddSubMenuItem`) containing the 3 options. Brand-aware — the
  submenu title is `Advanced`; items use the labels above.
- **Gating** — driven by the existing 3s desktop poller (`runDesktopPoller`),
  extended to also report agent-installed state:
  - `gui` item: enabled only when the desktop GUI is installed
    (`installedAppPath != ""`, already computed by `resolveDesktopIdentity`).
  - `lite`/`full` items: enabled only when the **agent** is installed
    (`<agentRoot>/hermes_cli/` is a directory, or `<agentRoot>/venv` exists —
    mirrors `agent_is_installed`, `gui_uninstall.py:154-168`).
  - Disabled (greyed) rather than hidden when unavailable, so the section is
    discoverable. If nothing is installed, `Advanced` still shows with all items
    disabled.
- **Confirm dialog** (`confirmDialog(title, body, yes, no)`, existing per-OS
  helper): body enumerates the concrete paths that will be deleted for that mode,
  and for `full` uses stern wording ("permanently deletes all OTTO data …"). The
  dialog notes OTTO Gateway is not removed.
- Result surfaced via `notify(title, body)` (existing): success ("OTTO fully
  removed") or the `LEFT:` leftover list.

## 4. Orchestration flow (per mode)

Faithful port of `runDesktopUninstall` (`hermes-agent/apps/desktop/electron/main.ts`)
and `buildPosixCleanupScript`/`buildWindowsCleanupScript`
(`.../electron/desktop-uninstall.ts`), minus the detached script (the tray is an
external process, so it orchestrates directly):

1. **Confirm** (abort on cancel).
2. **Stop the desktop app** if running: reuse `handleDesktopStop`'s graceful→forced
   path, then poll `isDesktopRunning(id)` until false or ~15s timeout. (Releases
   the app bundle + venv-python locks so removal succeeds.)
3. **Resolve** targets (§5). If any required target is empty/uncertain →
   **abort** with a notification; delete nothing.
4. **Run the Python engine** — the exact CLI the desktop uses:
   ```
   <python> -m hermes_cli.uninstall --mode <gui|lite|full>
   ```
   - env: `HERMES_HOME=<hermesHome>`, `NO_COLOR=1`; for lite/full also
     `PYTHONPATH=<agentRoot>` (prepended).
   - `cwd = <agentRoot>`.
   - `<python>`: **venv python** for `gui`; **system python** for `lite`/`full`
     (so it doesn't delete its own running interpreter), falling back to venv
     python if no system python is found.
   - Bounded timeout (5 min); capture stdout/stderr via existing `runCmd`.
   - This performs the *surgical* cleanup (PATH rc-lines, node symlinks, wrapper
     script, Windows registry PATH/env/deep-link keys, launchd/systemd/Scheduled
     Task services, Start-Menu/Desktop shortcuts). Non-fatal if it partially fails
     — step 5 backstops directory removal.
5. **Directory removal (backstop, the tray does this)** — matches the desktop's
   outer cleanup script:
   - `appPath` (the branded bundle) — **always** when resolved.
   - `agentRoot` — when mode ∈ {lite, full}.
   - `hermesHome` — when mode == full.
   - Each guarded by §6. Windows: retry up to 10× with a short sleep (handles are
     released lazily). Implemented via an injectable remove seam. The Python child
     (step 4) uses `cwd = agentRoot`, but the **tray process itself never chdirs
     into** `agentRoot`/`hermesHome`, so its `removeAll` holds no cwd lock on the
     dirs being deleted (the child has already exited by step 5).
6. **Validate** — for each path that should now be gone (`appPath`;
   `agentRoot` if removed; `hermesHome` if removed): check non-existence. Build a
   `GONE:`/`LEFT:` list. `notify` success or the leftover list. (Mirrors the
   desktop's result-log validation pass.)
7. Desktop poller re-detects on its next tick → menu state updates.

## 5. Discovery (exact rules)

All values reproduce hermes-agent's own logic (cited), computed in the tray.

### 5.1 Brand → slug / home-dir
The tray already resolves `brandIdentity` (`brand.go`) from the installed app's
`brand.json` (defaults OTTO). Extend it with:
- `Slug` = `strings.ToLower(DisplayName)` → `otto` / `loop24`.
- `HomeDirPosix` = `"." + Slug` → `.otto` / `.loop24`.
- `HomeDirWin` = `Slug` → `otto` / `loop24`.

(Matches `hermes-agent/scripts/brand/emitters/home.mjs` `homeNames`: POSIX uses
`homeDir` with dot, Windows uses `slug`.)

### 5.2 HERMES_HOME (`hermesHome`)
Order (reproduces `main.ts:312-342` + `hermes_constants.py:46-123`):
1. env `HERMES_HOME` (if set, non-empty).
2. **Windows only:** `HKCU\Environment\HERMES_HOME` via `reg query HKCU\Environment /v HERMES_HOME` (installer persists it — `install.ps1:2112-2117`; a GUI process's inherited env can be stale, so read the registry). Expand any `%VAR%`.
3. Default: Windows `%LOCALAPPDATA%\<Slug>` (fallback `%USERPROFILE%\AppData\Local\<Slug>`); POSIX `~/.<Slug>` (i.e. `HomeDirPosix`).
4. **Confirm** the result: `<hermesHome>/hermes-agent/hermes_cli/main.py` exists
   (mirrors `isHermesSourceRoot`, `main.ts:1625-1627`). If not, treat the agent
   as not installed (disable lite/full).

### 5.3 agentRoot + venv python
- `agentRoot = <hermesHome>/hermes-agent` (literal segment, **not** brand-renamed).
- venv python = `<agentRoot>/venv/bin/python` (POSIX) or
  `<agentRoot>/venv/Scripts/python.exe` (Windows). (`getVenvPython`, `main.ts:1838-1840`.)

### 5.4 System python (`findSystemPython`, `main.ts:1651-1792`)
- **POSIX:** first of `python3`, `python` found on PATH (`exec.LookPath`); else none.
- **Windows** (supported: 3.11/3.12/3.13):
  1. `reg query HKLM|HKCU\SOFTWARE\Python\PythonCore\<ver>\InstallPath /ve /reg:64`
     → `<installPath>\python.exe` if it exists.
  2. `%ProgramFiles%\Python<311|312|313>\python.exe`, then
     `%LOCALAPPDATA%\Programs\Python\Python<ver>\python.exe`.
  3. `py -3.13|-3.12|-3.11 -c "import sys; print(sys.executable)"` → printed path.
  - Deliberately **not** bare `python.exe` on PATH (Store-stub hazard).
  - None found → return "" → caller falls back to venv python.
- Interpreter selection per mode: gui → venv python; lite/full → system python
  (+`PYTHONPATH=agentRoot`), else venv fallback. (`main.ts:9102-9118`.)

### 5.5 appPath (branded bundle)
Reuse the tray's own resolution (more correct than the Python summary, which has
`Hermes`-hardcoded paths):
- macOS: the `.app` from `installedAppPath` (`/Applications/<Brand>.app` or
  `~/Applications/<Brand>.app`).
- Windows: the **install dir** = `filepath.Dir(installedAppPath)` (e.g. `…\OTTO`),
  matching `resolveRemovableAppPath` (`desktop-uninstall.ts:94-103`).
- If `installedAppPath == ""` → no bundle removal (nothing to remove).

## 6. Safety guards (destructive — mandatory)

`rm` runs **only** if all checks pass; otherwise abort the whole operation with a
notification and delete nothing. Implemented as a pure `validateRemovable(kind, path, id, home)`
predicate (unit-tested).

- **hermesHome:** basename ∈ {`.otto`,`.loop24`,`otto`,`loop24`}; path is under
  `$HOME` (POSIX) or `%LOCALAPPDATA%`/`%USERPROFILE%` (Windows); absolute; length
  above a floor; **never** equal to `$HOME`, `/`, `C:\`, a drive root, or `%LOCALAPPDATA%` itself.
- **agentRoot:** ends with `hermes-agent`; is a subdirectory of the validated
  `hermesHome`; still contains `hermes_cli/` at removal time.
- **appPath:** macOS — ends with `.app` and under `/Applications` or
  `~/Applications`; Windows — basename equals the brand `DisplayName` (or `Slug`)
  and under `%LOCALAPPDATA%\Programs` / `%LOCALAPPDATA%` / `%ProgramFiles%`.
- Empty string, relative path, symlink-escape, or `..` → reject.

## 7. Code layout (tray)

New files under `cmd/otto-tray/` (`//go:build darwin || windows` unless noted):
- `uninstalldesktop.go` — orchestrator `handleDesktopUninstall(mode)`, mode enum,
  the flow in §4, guards in §6, validation in §4 step 6.
- `hermeshome.go` — `resolveHermesHome`, `agentRoot`, `agentInstalled`, brand
  slug/home derivation (extend `brandIdentity` in `brand.go`).
- `syspython.go` + `syspython_windows.go` / `syspython_darwin.go` — system/venv
  python discovery (Windows registry + `py` probe live in the `_windows.go` file).
- `winenv_windows.go` — `readWindowsUserEnvVar("HERMES_HOME")` via `reg query`
  (Windows only).
- Menu wiring in `tray.go` (`Advanced` submenu, 3 items, click handlers) +
  `desktoptray.go` (gating state applied by the poller; extend the desktop probe
  to include `agentInstalled`).
- Seams (interfaces/func vars) for: filesystem `exists`/`removeAll`, `env` getter,
  Windows registry reader, and `runCmd` — so tests inject fakes and never delete.

## 8. Testing

Pure, seam-injected unit tests (no real deletion, no real process spawn), table-driven,
built for darwin **and** windows:
- brand → slug / home-dir derivation (OTTO, LOOP24, refined brand.json).
- `resolveHermesHome`: env set; Windows registry hit; default per-OS/brand; agent-confirm gate.
- `validateRemovable`: accepts valid hermesHome/agentRoot/appPath; **rejects**
  `$HOME`, `/`, drive root, wrong basename, empty, relative, `..`.
- mode → python selection (gui=venv; lite/full=system, fallback venv).
- validation pass → correct `GONE`/`LEFT` for combinations of leftover paths.
- Windows: `hideConsole`/`detachGUIProcess` already covered; new spawns reuse the
  console-hygiene helpers (any console child, e.g. `reg`/`py`, uses `hideConsole`).

CI note: `test-race` runs on Linux, where the `darwin || windows` tray package is
not built — these tests run only on a real darwin/Windows box. Gate locally with
darwin `go test` + `GOOS=windows go build/vet/test-compile`, plus a real Windows
runtime uninstall test before shipping.

## 9. Risks / caveats

- **Destructive.** Mitigated by §6 guards, explicit path enumeration in the confirm
  dialog, abort-on-uncertainty, and pure-function tests of the guard.
- **Windows system-python discovery** is the most intricate piece; venv fallback
  keeps lite/full working (the tray's own `rm` backstops any venv-lock the fallback
  hits).
- **No CI coverage** of the tray package (Linux CI) — relies on `GOOS=windows`
  build/vet + manual Windows runtime verification.
- Depends on the stable hermes contract `python -m hermes_cli.uninstall --mode`
  (unchanged; no hermes edits).
