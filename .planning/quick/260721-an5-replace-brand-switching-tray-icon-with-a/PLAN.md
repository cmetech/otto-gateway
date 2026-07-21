---
quick_id: 260721-an5
slug: replace-brand-switching-tray-icon-with-a
date: 2026-07-21
status: in-progress
---

# Quick Task 260721-an5: Generic Gateway tray icon + install auto-start + Windows Start Menu shortcut

## Goal
Three tray/desktop changes, decided with the user:
1. **Generic Gateway icon** — retire the brand-switching (loop24 vs OTTO template) icon logic; always show one generic Gateway glyph (derived from the MDI `proxy` icon, Apache-2.0). Keep the colored Warning/Error state icons as the health signal.
2. **Auto-start on install** — the installer already *stops* a running tray mid-install (to unlock files); add the *start* at the end so the tray always ends up running after an install (stop-then-start when it was already up).
3. **Windows Start Menu shortcut** — create a per-user Programs `.lnk` → `gateway-tray.exe`, carrying the new icon (embedded in the exe via a committed `rsrc_windows_amd64.syso` so the shortcut, Explorer, and taskbar all show it). Programmatic pin-to-tile is unsupported on Win10/11; the Start Menu shortcut (All Apps + search) is the deliverable.

## Assets (generated from crisp MDI `proxy` SVG via rsvg-convert)
- `cmd/otto-tray/icon/gateway.png` — 88px `graya` (matches existing template format); black-on-alpha → macOS `SetTemplateIcon` recolors per bar theme.
- `cmd/otto-tray/icon/gateway.ico` — multi-res 16/24/32/48/64/128/256, medium blue `#3B82F6` (reads on dark taskbar AND light Start menu; Windows has no template system).
- `cmd/otto-tray/rsrc_windows_amd64.syso` — icon resource; `go build` auto-links it for windows/amd64 (the only Windows tray arch). No Makefile/CI change.
- Remove `loop24.png` / `loop24.ico` (unused after the switch).

## Tasks

### T1 — Icon assets & resource (done during planning)
Assets + `.syso` generated and copied into `cmd/otto-tray/icon/`. Verified visually: template flips white/black with bar; ico legible on dark + light.

### T2 — Icon package: add `Gateway`, drop `Loop24`
- `icon/icon_darwin.go`: `//go:embed gateway.png var Gateway []byte`; remove `Loop24`.
- `icon/icon_windows.go`: `//go:embed gateway.ico var Gateway []byte`; remove `Loop24`.
- `icon/icon_other.go`: add `var Gateway []byte` stub (keeps `go build ./...` green on linux CI).
- Delete `loop24.png`, `loop24.ico`.

### T3 — Remove brand switching, use Gateway
- `uihelpers_darwin.go`: `setBaseIcon()` (no param) → `SetTemplateIcon(Gateway)`; `setIconForState(state)` → Running: `SetTemplateIcon(Gateway)`, Starting/Degraded: `Warning`, default: `Error`.
- `uihelpers_windows.go`: `setBaseIcon()` → `SetIcon(Gateway)`; `setIconForState(state)` → Running: `SetIcon(Gateway)`, Starting/Degraded: `Warning`, default: `Error`.
- `tray.go`: delete `brandLoop24` field/comment; call `setBaseIcon()` and `setIconForState(out.State)`; drop the `brandUsesLoop24` store. Fix imports.
- `desktoptray.go`: delete the `brandLoop24.Store(...)` line + its comment.
- Delete `brandicon.go` + `brandicon_test.go` (icon-only helpers `resolveBrandJSON`/`brandUsesLoop24`). Keep `brand_test.go` and the shared desktop-identity helpers (still used by desktop start/stop).

### T4 — Verify build/tests
- `go build ./...` (linux/other via icon_other) + `go build ./cmd/otto-tray` (darwin native, cgo) + `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/otto-tray` (links the .syso).
- `go test ./cmd/otto-tray/...`.
- gofumpt + golangci-lint v2.12.2 (per project gate).

### T5 — install.sh: start tray at end
After the "Next steps" print, if `Gateway Tray.app` exists, `open` it. Comment references the earlier `killall gateway-tray` as the stop half.

### T6 — install.ps1: Start Menu shortcut + start tray
After install, if `gateway-tray.exe` exists: create `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Gateway Tray.lnk` (WScript.Shell) → TargetPath=$trayExe, IconLocation=$trayExe, WorkingDirectory=bin; then `Start-Process $trayExe`. The mid-install `Stop-Process gateway-tray` is the stop half.

### T7 — Docs touch-up
Note in `docs/operator-quickstart.md` that the installer now launches the tray automatically (manual `open`/`Start-Process` still documented as the re-launch path).

## Out of scope / follow-ups
- Removing the Start Menu `.lnk` on uninstall (nice-to-have; note it).
- Windows arm64 tray (not built today).

## Verification (UAT)
- macOS: fresh install → tray appears in menu bar with the routing glyph; icon is white on dark bar, black on light. No OTTO/loop24 icon.
- Windows: install → tray in notification area (blue glyph); "Gateway Tray" in Start → All Apps/search with the icon; launching it starts the tray.
