---
quick_id: 260713-t1p
slug: tray-brand-aware-icon
date: 2026-07-13
status: complete
commit: 755b53d
type: feature
---

# Summary — Brand-aware tray icon (OTTO vs loop24)

## Outcome
The tray icon is now brand-aware. Because the tray ships with the gateway and can
be installed before the desktop app, brand can't depend on the desktop being
present. Rule implemented:

- **OTTO icon** only when the desktop's `brand.json` is present **and** names OTTO.
- **loop24 icon** otherwise — `brand.json` absent (desktop not installed yet) or
  brand != OTTO.

## Assets
Generated from `~/loop24/svg/loop24-mark.svg` (bright bare mark) with rsvg-convert
+ magick:
- `cmd/otto-tray/icon/loop24.png` — 44px colored (darwin), matches template.png.
- `cmd/otto-tray/icon/loop24.ico` — 16/32/48 colored (windows).

## Behavior
- Only the **Running/idle** glyph is branded; Warning/Error keep the status icons
  (health beats brand).
- macOS: loop24 uses `SetIcon` (colored blue, does NOT adapt to light/dark bar —
  deliberate); OTTO keeps `SetTemplateIcon` (monochrome adaptive).
- Windows: colored ICOs throughout.
- Brand refresh is automatic and race-free: `brandLoop24 atomic.Bool` is refreshed
  every desktop poll tick and read by the single gateway icon-writer
  (`setIconForState`/`setBaseIcon`), which runs every gateway tick — so a desktop
  installed/branded after the tray started flips the icon within a couple of ticks.
- `brandUsesLoop24`/`resolveBrandJSON` report brand.json **presence** explicitly
  (today's `resolveDesktopIdentity` hides "absent" behind an OTTO default).
- Removed the now-dead `setIcon` helper.

## Files (10)
NEW: `brandicon.go`, `brandicon_test.go`, `icon/loop24.png`, `icon/loop24.ico`.
EDIT: `icon/icon_darwin.go`, `icon/icon_windows.go` (embeds), `uihelpers_darwin.go`,
`uihelpers_windows.go` (setBaseIcon + setIconForState signature), `tray.go`
(field + startup resolve + state call, dropped icon import), `desktoptray.go`
(probe refreshes the flag).

## Verification (all pass)
gofmt / gofumpt clean · `go vet` (darwin) · `go test ./cmd/otto-tray/...` (darwin)
pass · `GOOS=windows` build+vet+test-compile · `GOOS=linux go build` (icon stub) ·
golangci-lint (darwin) 0 issues.

**Runtime confirmation pending:** operator to verify on Windows + macOS — with no
desktop installed the tray should show the loop24 mark; with an OTTO desktop +
brand.json it should show OTTO.

## Follow-up
- Commit `755b53d` on `main`; operator cuts a release separately.
- Known limitation (pre-existing, out of scope): `installedAppPath` only matches
  OTTO-named app candidates, so a `LOOP24.app` install isn't detected for
  desktop-management — but the icon rule still resolves correctly (→ loop24).
