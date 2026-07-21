---
quick_id: 260721-an5
slug: replace-brand-switching-tray-icon-with-a
date: 2026-07-21
status: complete
commits: ca95cd1..10387ed
---

# Summary — Generic Gateway tray icon + install auto-start + Windows Start Menu shortcut

## What shipped

### 1. Generic Gateway icon (replaces brand switching)
The tray no longer picks between the loop24 mark and the OTTO template based on a
desktop `brand.json`. It always shows one generic Gateway glyph, derived from the
crisp MDI `proxy` SVG (Apache-2.0, rendered via `rsvg-convert`).

- `cmd/otto-tray/icon/gateway.png` — 88px `graya` (black-on-alpha). macOS
  `SetTemplateIcon` recolors it per bar theme (white on dark, black on light).
- `cmd/otto-tray/icon/gateway.ico` — multi-res (16/24/32/48/64/128/256), medium
  blue `#3B82F6`. Windows has no template system and the default taskbar is dark,
  so the fixed blue was chosen to read on both the dark taskbar and the light
  Start menu.
- `cmd/otto-tray/rsrc_windows_amd64.syso` — embeds the icon as the exe's Windows
  resource, so the Start Menu shortcut, Explorer, and taskbar all show it.
  `go build` auto-links it for windows/amd64 (the only Windows tray arch) — no
  Makefile or release.yml change needed.
- Removed `brandicon.go` + `brandicon_test.go` (the icon-only
  `resolveBrandJSON`/`brandUsesLoop24` helpers), the `brandLoop24` atomic + its
  poller store, and the `loop24.png`/`loop24.ico` assets. The shared
  desktop-identity helpers (`resolveDesktopIdentity`, `installedAppPath`, …) are
  untouched — they still drive desktop start/stop. Warning/Error state icons kept
  as the health signal.

### 2. Auto-start tray on install
Both installers already **stop** a running `gateway-tray` mid-install (to unlock
the files). Added the **start** at the end, so the tray always ends up running
after an install (stop-then-start when it was already up).
- `install.sh`: `open "Gateway Tray.app"` (best-effort).
- `install.ps1`: `Start-Process` the exe.

### 3. Windows Start Menu shortcut
`install.ps1` creates a per-user `Gateway Tray.lnk` in
`%APPDATA%\…\Start Menu\Programs` (WScript.Shell), TargetPath = the exe,
IconLocation = the exe's embedded icon. It appears in *All Apps* and search; the
user can right-click → *Pin to Start* (programmatic pin-to-tile is not supported
on Win10/11 — Microsoft removed the APIs, so this is the correct deliverable).

## Verification
- `go build ./...` (darwin host, cgo), `GOOS=windows GOARCH=amd64 go build
  ./cmd/otto-tray` (links the .syso), `GOOS=linux go build ./...` (icon_other
  stub) — all pass.
- `go test ./cmd/otto-tray/...` — pass.
- `gofumpt -d` clean; `golangci-lint v2.12.2` — 0 issues.
- `bash -n scripts/install.sh` OK; PowerShell parser on `install.ps1` OK.
- Icon 16×16 DIB confirmed present in the linked `.exe`.
- Visual check of both icons: macOS template flips white/black with the bar;
  the blue ico is legible on dark taskbar and light Start menu at 16/32/48px.

## Notes / follow-ups
- **Branch:** committed on `feat/context-compression` (GSD quick config did not
  auto-branch). Unrelated to that feature — consider moving to a dedicated branch
  before the PR. Not pushed.
- Uninstall does not yet remove the Start Menu `.lnk` — nice-to-have follow-up.
- Windows arm64 tray isn't built today, so no arm64 `.syso`. Add
  `rsrc_windows_arm64.syso` if/when that arch ships.
- The "OTTO icon" you were seeing was the `template.png` placeholder, shown
  because a stale OTTO `brand.json` satisfied the old switch — now moot since the
  switch is gone.
