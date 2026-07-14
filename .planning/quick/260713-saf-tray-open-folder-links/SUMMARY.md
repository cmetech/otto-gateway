---
quick_id: 260713-saf
slug: tray-open-folder-links
date: 2026-07-13
status: complete
commit: 5890381
type: feature
---

# Summary — Tray "Advanced ▸ Open Folder" links

## Outcome
Added an `Advanced ▸` submenu to the tray with three read-only convenience links
that open the relevant folder in Explorer (Windows) / Finder (macOS):

| Item | Windows | macOS |
|---|---|---|
| Open App Folder | `%LOCALAPPDATA%\Programs\<Brand>` (dir of the resolved exe) | reveal `/Applications/<Brand>.app` (`open -R`) |
| Open Data Folder (.otto) | `%LOCALAPPDATA%\<slug>` (HERMES_HOME) | `~/.<slug>` |
| Open Gateway Folder (~/.otto-gw) | `%USERPROFILE%\.otto-gw` | `~/.otto-gw` |

Brand-aware (`slug = lower(DisplayName)` → otto/loop24). Replaces the cancelled
tray-uninstall design with a much simpler, non-destructive convenience.

## Key correctness decisions
- `explorer`/`open` are the GUI we want visible → launched fire-and-forget
  (`cmd.Start()`, never wait — Explorer returns nonzero even on success) and
  **without** `hideConsole` (HideWindow would hide the window — same class as the
  Start-hidden-window bug fixed in v2.1.2).
- Only the Windows `reg query` (reading `HKCU\Environment\HERMES_HOME`) is a
  console child → wrapped with `hideConsole`.
- HERMES_HOME resolution mirrors hermes-agent: env `HERMES_HOME` → (win) registry
  → per-OS/brand default. Registry value parsed off the `REG_SZ`/`REG_EXPAND_SZ`
  marker (paths may contain spaces) with `%VAR%` expansion.
- Purely read/open — nothing created or deleted; a missing folder just notifies.

## Files (5 changed)
- `cmd/otto-tray/openfolder.go` — NEW: resolution + `openInFileManager` + 3 handlers (`//go:build darwin || windows`)
- `cmd/otto-tray/openfolder_windows.go` — NEW: `readUserEnvVar` (reg query) + `expandWinVars`
- `cmd/otto-tray/openfolder_darwin.go` — NEW: no-op `readUserEnvVar`
- `cmd/otto-tray/openfolder_test.go` — NEW: pure table tests (brandSlug, fileManagerCommand, appFolderTarget, resolveHermesHome)
- `cmd/otto-tray/tray.go` — Advanced submenu + fields + click wiring

## Verification (all pass)
gofmt / gofumpt clean · `go vet` (darwin) · `go test ./cmd/otto-tray/...` (darwin)
pass · `GOOS=windows` build + vet + test-compile clean · golangci-lint (darwin) 0
issues. (golangci-lint can't run a cross-compiled Windows binary on macOS; the
tray package isn't linted in CI's Linux job either — `GOOS=windows go vet` covers
the windows files.)

**Runtime confirmation pending:** operator to verify on Windows + macOS that each
menu item opens the right folder.

## Follow-up
- Commit `5890381` on `main`; operator cuts a release separately after review.
- Design spec for the cancelled uninstall predecessor was removed (see git log).
