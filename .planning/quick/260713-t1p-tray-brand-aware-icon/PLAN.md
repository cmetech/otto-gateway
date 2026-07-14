---
quick_id: 260713-t1p
slug: tray-brand-aware-icon
date: 2026-07-13
description: "Brand-aware tray icon: loop24 mark by default, OTTO icon only when brand.json present + OTTO"
type: feature
---

# Quick Task — Brand-aware tray icon (OTTO vs loop24)

## Rule (from user)
Read brand from the desktop app's `brand.json`. Use the **OTTO** tray icon **only**
when brand.json is present AND names OTTO. Otherwise — brand.json absent (desktop
not installed yet; the tray ships with the gateway and can precede it) or brand ≠
OTTO — use the **loop24** icon.

## Assets (already generated from ~/loop24/svg/loop24-mark.svg — the bright bare mark)
- `cmd/otto-tray/icon/loop24.png` — 44px colored (darwin), matches template.png size.
- `cmd/otto-tray/icon/loop24.ico` — 16/32/48 colored (windows).

## Icon behavior
The tray icon reflects gateway **state**; the **brand glyph** occupies the base
(startup) + Running icon. Warning/Error stay the existing status icons (health
beats brand). loop24 on macOS uses `SetIcon` (colored blue — does NOT adapt to
light/dark bar, deliberate) vs OTTO's `SetTemplateIcon` (monochrome adaptive).

Brand refresh is automatic: the gateway poller emits every ~3s and
`applyState`→`setIconForState` runs each tick, reading an atomic brand flag the
desktop poller updates — single icon-writer, no races, brand flips within ~3s if
the desktop is installed/branded later.

## Tasks

### Task 1 — embed loop24 assets (icon package)
- `icon_darwin.go`: add
  ```go
  //go:embed loop24.png
  var Loop24 []byte
  ```
- `icon_windows.go`: add
  ```go
  //go:embed loop24.ico
  var Loop24 []byte
  ```
- `icon_other.go`: add `var Loop24 []byte` (empty stub, keeps linux `go build ./...` green).

### Task 2 — brand resolver `cmd/otto-tray/brandicon.go` (`//go:build darwin || windows`)
```go
package main

import (
	"encoding/json"
	"strings"
)

// resolveBrandJSON returns the installed desktop app's brand.json DisplayName and
// whether brand.json was actually read. Absent (no app, or no/invalid brand.json)
// → ("", false). Uses the OTTO-default candidate paths (installedAppPath), same as
// resolveDesktopIdentity — a loop24 app named LOOP24.app won't match those, which
// is fine: that path yields (…, false) → loop24 icon per the rule below.
func resolveBrandJSON(goos string, env func(string) string, home string, exists func(string) bool, readFile func(string) ([]byte, error)) (string, bool) {
	base := defaultBrandIdentity()
	appPath := installedAppPath(goos, base, env, home, exists)
	if appPath == "" {
		return "", false
	}
	data, err := readFile(brandJSONPathForApp(goos, appPath))
	if err != nil {
		return "", false
	}
	var doc brandJSONDoc
	if json.Unmarshal(data, &doc) != nil || !validateDisplayName(doc.DisplayName) {
		return "", false
	}
	return doc.DisplayName, true
}

// brandUsesLoop24 implements the rule: OTTO icon only when brand.json present AND
// OTTO; otherwise loop24.
func brandUsesLoop24(goos string, env func(string) string, home string, exists func(string) bool, readFile func(string) ([]byte, error)) bool {
	name, present := resolveBrandJSON(goos, env, home, exists, readFile)
	return !(present && strings.EqualFold(name, "OTTO"))
}
```

### Task 3 — per-OS icon setters (signature change)
`uihelpers_darwin.go`:
```go
// setBaseIcon sets the idle/startup icon for the resolved brand.
func setBaseIcon(loop24 bool) {
	if loop24 {
		systray.SetIcon(icon.Loop24) // colored blue mark (non-template)
		return
	}
	systray.SetTemplateIcon(icon.Template, icon.Template)
}

func setIconForState(state State, loop24 bool) {
	switch state {
	case StateRunning:
		if loop24 {
			systray.SetIcon(icon.Loop24)
		} else {
			systray.SetTemplateIcon(icon.Running, icon.Running)
		}
	case StateStarting, StateDegraded:
		systray.SetIcon(icon.Warning)
	default: // StateError, StateStopped, StateUnknown
		systray.SetIcon(icon.Error)
	}
}
```
`uihelpers_windows.go` (same shape, all `SetIcon`):
```go
func setBaseIcon(loop24 bool) {
	if loop24 {
		systray.SetIcon(icon.Loop24)
		return
	}
	systray.SetIcon(icon.Template)
}

func setIconForState(state State, loop24 bool) {
	switch state {
	case StateRunning:
		if loop24 {
			systray.SetIcon(icon.Loop24)
		} else {
			systray.SetIcon(icon.Running)
		}
	case StateStarting, StateDegraded:
		systray.SetIcon(icon.Warning)
	default:
		systray.SetIcon(icon.Error)
	}
}
```
(Keep the existing `setIcon(b []byte)` if still referenced; the startup call switches to `setBaseIcon`.)

### Task 4 — wire brand flag in `tray.go`
- Add field: `brandLoop24 atomic.Bool` (near desktop fields).
- In `onReady`, replace `setIcon(icon.Template)` (line ~90) with:
  ```go
  s.brandLoop24.Store(brandUsesLoop24(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile))
  setBaseIcon(s.brandLoop24.Load())
  ```
- In `applyState` (line ~239): `setIconForState(out.State, s.brandLoop24.Load())`.
- In `makeDesktopProbe`'s returned closure (desktoptray.go), after resolving identity, add:
  ```go
  s.brandLoop24.Store(brandUsesLoop24(runtime.GOOS, env, home, exists, readFile))
  ```
  (updates the flag every desktop tick so a later desktop install/brand flips the icon within a couple of ticks).

### Task 5 — tests `cmd/otto-tray/brandicon_test.go` (`//go:build darwin || windows`)
Seam-injected (fake env/exists/readFile), table-driven:
- no app installed (exists→false) → brandUsesLoop24 == true (loop24).
- OTTO app + brand.json {"displayName":"OTTO"} → false (OTTO icon).
- OTTO app + brand.json {"displayName":"LOOP24"} → true (loop24).
- OTTO app + brand.json missing (readFile err) → true (loop24).
- OTTO app + invalid brand.json → true (loop24).

## Verification (all must pass)
1. `gofmt -l cmd/otto-tray/*.go` → empty
2. `go run mvdan.cc/gofumpt@latest -l cmd/otto-tray/` → empty
3. `go vet ./cmd/otto-tray/...` (darwin) → clean
4. `go test ./cmd/otto-tray/...` (darwin) → pass
5. `GOOS=windows go build ./cmd/otto-tray/...` → compiles
6. `GOOS=windows go vet ./cmd/otto-tray/...` → clean
7. `GOOS=windows go test -c -o /dev/null ./cmd/otto-tray/` → compiles
8. `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./cmd/otto-tray/...` (darwin) → 0 issues

## Commit
One atomic commit incl. the two new binary assets:
`feat(tray): brand-aware icon — loop24 mark by default, OTTO icon only when brand.json is OTTO`.
Trailers: Co-Authored-By + Claude-Session. Do NOT tag/release (operator cuts a release separately).
