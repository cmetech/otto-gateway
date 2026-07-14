---
quick_id: 260713-saf
slug: tray-open-folder-links
date: 2026-07-13
description: "Tray Advanced ▸ submenu with Open App / Data / Gateway folder links (explorer/Finder)"
type: feature
---

# Quick Task — Tray "Advanced ▸ Open Folder" links

## Context
Add a new `Advanced ▸` submenu to the tray with three read-only "open this folder
in Explorer/Finder" convenience links. No destructive actions. (The earlier
tray-driven uninstall design was cancelled.)

Folders (brand-aware, OTTO/LOOP24):
| Item | Windows | macOS |
|---|---|---|
| App Folder | `%LOCALAPPDATA%\Programs\<Brand>` (dir of the resolved exe) | reveal `/Applications/<Brand>.app` in Finder (`open -R`) |
| Data Folder | `%LOCALAPPDATA%\<slug>` (HERMES_HOME) | `~/.<slug>` |
| Gateway Folder | `%USERPROFILE%\.otto-gw` | `~/.otto-gw` |

`slug = strings.ToLower(brandIdentity.DisplayName)` (otto / loop24).

## Key correctness notes
- **`explorer`/`open` are GUI — do NOT `hideConsole` them** (HideWindow would hide
  the window we want to show — same class as the Start-hidden-window bug). Launch
  fire-and-forget with `cmd.Start()` (Explorer returns nonzero even on success, so
  never `Run()`/wait).
- The Windows `reg query` used to read HERMES_HOME **is** a console child → wrap it
  with the existing `hideConsole(cmd)`.
- Purely read/open: if a folder doesn't exist, notify; never create or delete.

## Tasks

### Task 1 — `cmd/otto-tray/openfolder.go` (`//go:build darwin || windows`)
Pure helpers + handlers:
```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// brandSlug lowercases the brand display name → "otto" / "loop24".
func brandSlug(id brandIdentity) string { return strings.ToLower(id.DisplayName) }

// resolveHermesHome mirrors hermes-agent's resolution (main.ts / hermes_constants.py):
// env HERMES_HOME → (windows) HKCU\Environment\HERMES_HOME → default per-OS/brand.
// winReg returns "" off windows / when unset. Pure (all inputs injected) for tests.
func resolveHermesHome(goos string, env func(string) string, home, slug string, winReg func(string) string) string {
	if v := strings.TrimSpace(env("HERMES_HOME")); v != "" {
		return v
	}
	if goos == "windows" {
		if v := strings.TrimSpace(winReg("HERMES_HOME")); v != "" {
			return v
		}
		if la := strings.TrimSpace(env("LOCALAPPDATA")); la != "" {
			return filepath.Join(la, slug)
		}
		if up := strings.TrimSpace(env("USERPROFILE")); up != "" {
			return filepath.Join(up, "AppData", "Local", slug)
		}
		return filepath.Join(home, "AppData", "Local", slug)
	}
	return filepath.Join(home, "."+slug)
}

// appFolderTarget maps the resolved app path to what to open.
// windows: the install dir (dir of the exe), opened directly.
// darwin:  the .app bundle, revealed in Finder (reveal=true → `open -R`).
func appFolderTarget(goos, appPath string) (target string, reveal bool) {
	if goos == "windows" {
		return filepath.Dir(appPath), false
	}
	return appPath, true
}

// fileManagerCommand builds the OS file-manager command. Pure → unit-tested both OSes.
func fileManagerCommand(goos, path string, reveal bool) (string, []string) {
	if goos == "windows" {
		if reveal {
			return "explorer", []string{"/select," + path}
		}
		return "explorer", []string{path}
	}
	if reveal {
		return "open", []string{"-R", path}
	}
	return "open", []string{path}
}

// openInFileManager launches Explorer/Finder fire-and-forget. NO hideConsole:
// these are the GUI we want visible; HideWindow would hide it.
func openInFileManager(path string, reveal bool) error {
	name, args := fileManagerCommand(runtime.GOOS, path, reveal)
	cmd := exec.Command(name, args...) //nolint:gosec // name is constant; path from allowlisted app/known home dirs
	return cmd.Start()
}

func (s *trayState) handleOpenAppFolder() {
	id, appPath := resolveDesktopIdentity(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile)
	if appPath == "" {
		notify("Open App Folder", id.DisplayName+" desktop app not found. Install it first.")
		return
	}
	target, reveal := appFolderTarget(runtime.GOOS, appPath)
	if err := openInFileManager(target, reveal); err != nil {
		notify("Open App Folder", "Could not open: "+err.Error())
	}
}

func (s *trayState) handleOpenDataFolder() {
	id, _ := resolveDesktopIdentity(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile)
	home := resolveHermesHome(runtime.GOOS, os.Getenv, homeDir(), brandSlug(id), readUserEnvVar)
	if !statExists(home) {
		notify("Open Data Folder", "Not found: "+home)
		return
	}
	if err := openInFileManager(home, false); err != nil {
		notify("Open Data Folder", "Could not open: "+err.Error())
	}
}

func (s *trayState) handleOpenGatewayFolder() {
	dir := filepath.Join(homeDir(), ".otto-gw")
	if !statExists(dir) {
		notify("Open Gateway Folder", "Not found: "+dir)
		return
	}
	if err := openInFileManager(dir, false); err != nil {
		notify("Open Gateway Folder", "Could not open: "+err.Error())
	}
}
```

### Task 2 — `cmd/otto-tray/openfolder_windows.go` (`//go:build windows`)
`readUserEnvVar` via `reg query` (console child → `hideConsole`), value-after-type-marker
parse (paths may contain spaces, so index off `REG_SZ`/`REG_EXPAND_SZ`, don't field-split),
+ `%VAR%` expansion for the common roots:
```go
package main

import (
	"os"
	"os/exec"
	"strings"
)

// readUserEnvVar reads HKCU\Environment\<name> (the persisted user env the installer
// writes). Inherited process env can be stale for a GUI-launched tray, so query the
// registry directly — same rationale as hermes-agent's readWindowsUserEnvVar.
func readUserEnvVar(name string) string {
	cmd := exec.Command("reg", "query", `HKCU\Environment`, "/v", name) //nolint:gosec // constant reg path + fixed value name
	hideConsole(cmd) // reg is a console child — suppress the flash
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, name) {
			continue
		}
		for _, marker := range []string{"REG_EXPAND_SZ", "REG_SZ"} {
			if i := strings.Index(line, marker); i >= 0 {
				return expandWinVars(strings.TrimSpace(line[i+len(marker):]))
			}
		}
	}
	return ""
}

// expandWinVars expands the few %VAR% roots a HERMES_HOME value might contain.
func expandWinVars(s string) string {
	for _, v := range []string{"LOCALAPPDATA", "USERPROFILE", "APPDATA"} {
		if val := os.Getenv(v); val != "" {
			s = strings.ReplaceAll(s, "%"+v+"%", val)
		}
	}
	return s
}
```

### Task 3 — `cmd/otto-tray/openfolder_darwin.go` (`//go:build darwin`)
```go
package main

// readUserEnvVar has no registry equivalent on darwin — HERMES_HOME resolution
// falls through to the env var / default home dir.
func readUserEnvVar(string) string { return "" }
```

### Task 4 — wire the menu in `cmd/otto-tray/tray.go`
- Add struct fields near the `miDesktop*` block:
  ```go
  miAdvanced          *systray.MenuItem
  miOpenAppFolder     *systray.MenuItem
  miOpenDataFolder    *systray.MenuItem
  miOpenGatewayFolder *systray.MenuItem
  ```
- In `onReady`, after the desktop section (`miDesktopStop`) + a separator, add:
  ```go
  systray.AddSeparator()
  did, _ := resolveDesktopIdentity(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile)
  s.miAdvanced = systray.AddMenuItem("Advanced", "")
  s.miOpenAppFolder = s.miAdvanced.AddSubMenuItem("Open App Folder", "Reveal the installed desktop app folder")
  s.miOpenDataFolder = s.miAdvanced.AddSubMenuItem("Open Data Folder (."+brandSlug(did)+")", "Open the "+did.DisplayName+" data folder")
  s.miOpenGatewayFolder = s.miAdvanced.AddSubMenuItem("Open Gateway Folder (~/.otto-gw)", "Open the OTTO Gateway data folder")
  ```
  (ensure `runtime` + `os` are imported in tray.go; add if missing.)
- In `wireCallbacks`, add:
  ```go
  s.miOpenAppFolder.Click(func() { go s.handleOpenAppFolder() })
  s.miOpenDataFolder.Click(func() { go s.handleOpenDataFolder() })
  s.miOpenGatewayFolder.Click(func() { go s.handleOpenGatewayFolder() })
  ```

### Task 5 — `cmd/otto-tray/openfolder_test.go` (`//go:build darwin || windows`)
Table-driven, pure — no real Explorer/Finder/reg:
- `brandSlug`: OTTO→otto, LOOP24→loop24.
- `fileManagerCommand`: windows dir→`explorer <path>`; windows reveal→`explorer /select,<path>`; darwin dir→`open <path>`; darwin reveal→`open -R <path>`.
- `appFolderTarget`: windows → (dir, false); darwin → (appPath, true).
- `resolveHermesHome`: env HERMES_HOME wins; windows winReg hit; windows default `LOCALAPPDATA\slug`; darwin default `home/.slug`; USERPROFILE fallback.

## Verification (all must pass)
1. `gofmt -l cmd/otto-tray/*.go` → empty
2. `go run mvdan.cc/gofumpt@latest -l cmd/otto-tray/` → empty
3. `go vet ./cmd/otto-tray/...` (darwin) → clean
4. `go test ./cmd/otto-tray/...` (darwin) → pass
5. `GOOS=windows go build ./cmd/otto-tray/...` → compiles
6. `GOOS=windows go vet ./cmd/otto-tray/...` → clean
7. `GOOS=windows go test -c -o /dev/null ./cmd/otto-tray/` → compiles

## Commit
One atomic commit: `feat(tray): Advanced ▸ Open App/Data/Gateway folder links`.
Trailers: Co-Authored-By + Claude-Session. Do NOT tag/release (operator cuts a
release separately).
