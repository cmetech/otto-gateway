# OTTO Tray — Desktop-App Management — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `otto-tray` so a user can install, start, stop, and see the running state of the OTTO desktop app from the tray menu — alongside the existing gateway controls.

**Architecture:** All new code lives under `cmd/otto-tray/` (keeps the gateway cgo-free; the tray already carries cgo, `//go:build darwin || windows`). A small independent **desktop poller** (own ticker + channel) drives a `DesktopState` machine and a new "OTTO Desktop" menu section — the well-tested gateway FSM/poller is left untouched. Pure logic (brand resolution, path selection, state computation, command construction) is parameterized by `goos` so both Windows and macOS branches are unit-tested on the macOS dev box; only the thin syscalls (process enumeration, exec) live in `_darwin.go`/`_windows.go`.

**Tech Stack:** Go 1.23+, `github.com/energye/systray` v1.0.3 (has `Show/Hide/Enable/Disable`), stdlib `os/exec`/`os`/`encoding/json`. Tests: `go test` (table-driven, `//go:build darwin || windows`).

## Global Constraints

- **Gateway stays cgo-free.** New code only under `cmd/otto-tray/`; no gateway package imports it.
- **`gosec` G204 is a trust gate.** Every `exec` uses a **fixed argv shape**. The **install** command is the **constant** `cmetech/otto` one-liner (no interpolated input). Brand-derived tokens used in exec (process name, app path) are **validated** first: display name must match `^[A-Za-z0-9 ._-]{1,64}$` before deriving `OTTO.exe`/`OTTO.app`/proc-match; app paths come only from the allowlisted well-known locations. Add `//nolint:gosec` **with a justification comment** (matching `runner.go:33`'s style) where a constant-shape spawn is still flagged.
- **Fail-safe detection.** Any stat/exec/parse error → treat as not-installed / not-running / defaults; never crash the poller.
- **Assume OTTO (v1).** Defaults are OTTO; `brand.json` (read from the installed app's **resources**) refines the *display name* + reserves `releasesRepo`. The install URL stays the constant `cmetech/otto` one-liner.
- **Stop confirms.** The Stop action shows a confirm dialog before killing the app.
- **Do not modify the gateway FSM/poller** (`fsm.go`, `poller.go`) — the desktop path is independent.
- **Tests run on macOS** (`go test ./cmd/otto-tray/...`); pure functions branch on a passed `goos` string (NOT `runtime.GOOS`) so the Windows branch is covered on the mac box.

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `cmd/otto-tray/brand.go` (create) | `brandIdentity`, `defaultBrandIdentity()`, `validateDisplayName`, `identityFromDisplayName`, `refineBrandIdentity` (overlay `brand.json`) | 1 |
| `cmd/otto-tray/brand_test.go` (create) | Brand resolver + validation tests | 1 |
| `cmd/otto-tray/desktop.go` (create) | `desktopAppCandidates(goos,id,env,home)`, `installedAppPath(...)`, `brandJSONPathForApp(goos,appPath)`, `resolveDesktopIdentity(...)` | 1 |
| `cmd/otto-tray/desktop_test.go` (create) | Path-candidate + installed-detection + brand.json-path tests (both OSes) | 1 |
| `cmd/otto-tray/desktop_darwin.go` (create) | `platformDesktopRunning(id)` via `pgrep`; assigns the `desktopRunningFn` seam | 2 |
| `cmd/otto-tray/desktop_windows.go` (create) | `platformDesktopRunning(id)` via `tasklist`; assigns the `desktopRunningFn` seam | 2 |
| `cmd/otto-tray/desktoprun.go` (create) | `desktopRunningFn` package-var seam + `isDesktopRunning(id)` | 2 |
| `cmd/otto-tray/desktoprun_test.go` (create) | Running-detection via a stubbed `desktopRunningFn` | 2 |
| `cmd/otto-tray/desktopstate.go` (create) | `DesktopState`, `desktopInput`, `computeDesktopState`, `runDesktopPoller` | 3 |
| `cmd/otto-tray/desktopstate_test.go` (create) | State-mapping + poller tests | 3 |
| `cmd/otto-tray/desktoptray.go` (create) | desktop menu wiring, `applyDesktopState`, handlers, `makeDesktopProbe` | 3,4 |
| `cmd/otto-tray/desktopcmd.go` (create) | `desktopInstallCommand/StartCommand/StopCommand(goos,...)` (pure) + `runCmd`/`spawnDetached` | 4 |
| `cmd/otto-tray/desktopcmd_test.go` (create) | Command-construction tests (both OSes) | 4 |
| `cmd/otto-tray/tray.go` (modify) | Add desktop fields to `trayState`; wire the desktop section + poller in `onReady`/`wireCallbacks` | 3 |
| `cmd/otto-tray/README` or docs section (modify) | Document the desktop section | 5 |

---

### Task 1: Brand identity + installed-detection

**Files:**
- Create: `cmd/otto-tray/brand.go`, `cmd/otto-tray/desktop.go`
- Test: `cmd/otto-tray/brand_test.go`, `cmd/otto-tray/desktop_test.go`

**Interfaces produced:**
- `brandIdentity{ DisplayName, WinExeName, MacAppName, MacProcMatch, InstallRepo string }`
- `defaultBrandIdentity() brandIdentity`
- `validateDisplayName(name string) bool`
- `identityFromDisplayName(name string) brandIdentity` (derives exe/app/proc from a validated name)
- `refineBrandIdentity(base brandIdentity, brandJSONPath string, readFile func(string)([]byte,error)) brandIdentity`
- `desktopAppCandidates(goos string, id brandIdentity, env func(string) string, home string) []string`
- `installedAppPath(goos string, id brandIdentity, env func(string) string, home string, exists func(string) bool) string`
- `brandJSONPathForApp(goos, appPath string) string`
- `resolveDesktopIdentity(goos string, env func(string) string, home string, exists func(string) bool, readFile func(string)([]byte,error)) (id brandIdentity, appPath string)`

- [ ] **Step 1: Write the failing tests** — `cmd/otto-tray/brand_test.go`:

```go
//go:build darwin || windows

package main

import "testing"

func TestValidateDisplayName(t *testing.T) {
	ok := []string{"OTTO", "LOOP24", "My App-1", "a.b_c"}
	bad := []string{"", "bad;name", "a\"b", "x/y", "a`b", strings2(65)}
	for _, s := range ok {
		if !validateDisplayName(s) {
			t.Errorf("expected %q valid", s)
		}
	}
	for _, s := range bad {
		if validateDisplayName(s) {
			t.Errorf("expected %q invalid", s)
		}
	}
}

func strings2(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestIdentityFromDisplayName(t *testing.T) {
	id := identityFromDisplayName("OTTO")
	if id.WinExeName != "OTTO.exe" || id.MacAppName != "OTTO.app" ||
		id.MacProcMatch != "OTTO.app/Contents/MacOS/OTTO" {
		t.Fatalf("bad derivation: %+v", id)
	}
}

func TestRefineBrandIdentity_DefaultsWhenMissingOrBad(t *testing.T) {
	base := defaultBrandIdentity()
	// missing file → defaults
	got := refineBrandIdentity(base, "/nope/brand.json", func(string) ([]byte, error) { return nil, errMissing })
	if got.DisplayName != "OTTO" {
		t.Fatalf("missing file should keep defaults, got %q", got.DisplayName)
	}
	// malformed json → defaults
	got = refineBrandIdentity(base, "x", func(string) ([]byte, error) { return []byte("{"), nil })
	if got.DisplayName != "OTTO" {
		t.Fatalf("malformed json should keep defaults, got %q", got.DisplayName)
	}
	// invalid displayName → defaults (no injection)
	got = refineBrandIdentity(base, "x", func(string) ([]byte, error) { return []byte(`{"displayName":"a;b"}`), nil })
	if got.DisplayName != "OTTO" {
		t.Fatalf("invalid name should keep defaults, got %q", got.DisplayName)
	}
	// valid override
	got = refineBrandIdentity(base, "x", func(string) ([]byte, error) {
		return []byte(`{"displayName":"LOOP24","releasesRepo":"cmetech/loop24"}`), nil
	})
	if got.DisplayName != "LOOP24" || got.WinExeName != "LOOP24.exe" || got.InstallRepo != "cmetech/loop24" {
		t.Fatalf("valid override failed: %+v", got)
	}
}

var errMissing = &fsErr{}

type fsErr struct{}

func (*fsErr) Error() string { return "missing" }
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/otto-tray/ -run 'TestValidateDisplayName|TestIdentity|TestRefine' -v`
Expected: FAIL — undefined: `validateDisplayName`, `identityFromDisplayName`, `refineBrandIdentity`, `defaultBrandIdentity`.

- [ ] **Step 3: Implement `brand.go`**

```go
//go:build darwin || windows

package main

import (
	"encoding/json"
	"regexp"
)

// brandIdentity is everything the desktop actions need to know about the
// brand. Defaults are OTTO; refineBrandIdentity may overlay a display name
// from the installed app's brand.json (Plan-6 discoverable descriptor).
type brandIdentity struct {
	DisplayName  string // "OTTO"
	WinExeName   string // "OTTO.exe"
	MacAppName   string // "OTTO.app"
	MacProcMatch string // "OTTO.app/Contents/MacOS/OTTO"
	InstallRepo  string // "cmetech/otto"
}

// displayNameRe bounds a brand display name to safe characters BEFORE it is
// used to build a process name / kill target passed to exec (gosec G204).
var displayNameRe = regexp.MustCompile(`^[A-Za-z0-9 ._-]{1,64}$`)

func validateDisplayName(name string) bool { return displayNameRe.MatchString(name) }

func identityFromDisplayName(name string) brandIdentity {
	return brandIdentity{
		DisplayName:  name,
		WinExeName:   name + ".exe",
		MacAppName:   name + ".app",
		MacProcMatch: name + ".app/Contents/MacOS/" + name,
		InstallRepo:  "cmetech/otto",
	}
}

func defaultBrandIdentity() brandIdentity { return identityFromDisplayName("OTTO") }

// brandJSONDoc is the subset of the Plan-6 brand.json the tray consumes.
type brandJSONDoc struct {
	DisplayName  string `json:"displayName"`
	ReleasesRepo string `json:"releasesRepo"`
}

// refineBrandIdentity overlays a validated brand.json onto the defaults.
// Any error / invalid content keeps the base identity (fail-safe).
func refineBrandIdentity(base brandIdentity, brandJSONPath string, readFile func(string) ([]byte, error)) brandIdentity {
	data, err := readFile(brandJSONPath)
	if err != nil {
		return base
	}
	var doc brandJSONDoc
	if json.Unmarshal(data, &doc) != nil {
		return base
	}
	out := base
	if validateDisplayName(doc.DisplayName) {
		out = identityFromDisplayName(doc.DisplayName)
	}
	if doc.ReleasesRepo != "" {
		out.InstallRepo = doc.ReleasesRepo // used for display only in v1; install URL is constant
	}
	return out
}
```

- [ ] **Step 4: Write `desktop_test.go`**

```go
//go:build darwin || windows

package main

import (
	"path/filepath"
	"testing"
)

func TestDesktopAppCandidates(t *testing.T) {
	id := defaultBrandIdentity()
	env := func(k string) string {
		switch k {
		case "LOCALAPPDATA":
			return `C:\Users\me\AppData\Local`
		}
		return ""
	}
	win := desktopAppCandidates("windows", id, env, "")
	if len(win) == 0 || filepath.Base(win[0]) != "OTTO.exe" {
		t.Fatalf("windows candidates bad: %v", win)
	}
	mac := desktopAppCandidates("darwin", id, func(string) string { return "" }, "/Users/me")
	if len(mac) < 2 || mac[0] != "/Applications/OTTO.app" {
		t.Fatalf("darwin candidates bad: %v", mac)
	}
}

func TestInstalledAppPath(t *testing.T) {
	id := defaultBrandIdentity()
	present := "/Applications/OTTO.app"
	exists := func(p string) bool { return p == present }
	got := installedAppPath("darwin", id, func(string) string { return "" }, "/Users/me", exists)
	if got != present {
		t.Fatalf("expected %q, got %q", present, got)
	}
	none := installedAppPath("darwin", id, func(string) string { return "" }, "/Users/me", func(string) bool { return false })
	if none != "" {
		t.Fatalf("expected empty, got %q", none)
	}
}

func TestBrandJSONPathForApp(t *testing.T) {
	if p := brandJSONPathForApp("darwin", "/Applications/OTTO.app"); p != "/Applications/OTTO.app/Contents/Resources/brand.json" {
		t.Fatalf("darwin brand.json path: %q", p)
	}
	win := brandJSONPathForApp("windows", `C:\P\OTTO\OTTO.exe`)
	if filepath.Base(win) != "brand.json" || filepath.Base(filepath.Dir(win)) != "resources" {
		t.Fatalf("windows brand.json path: %q", win)
	}
}
```

- [ ] **Step 5: Implement `desktop.go`**

```go
//go:build darwin || windows

package main

import "path/filepath"

// desktopAppCandidates returns launchable-path candidates, most-preferred
// first. Pure (branches on goos) so both OSes are tested on one box.
func desktopAppCandidates(goos string, id brandIdentity, env func(string) string, home string) []string {
	switch goos {
	case "windows":
		var out []string
		for _, k := range []string{"LOCALAPPDATA", "PROGRAMFILES", "PROGRAMFILES(X86)"} {
			if root := env(k); root != "" {
				out = append(out,
					filepath.Join(root, "Programs", id.DisplayName, id.WinExeName),
					filepath.Join(root, id.DisplayName, id.WinExeName),
				)
			}
		}
		return out
	default: // darwin
		return []string{
			filepath.Join("/Applications", id.MacAppName),
			filepath.Join(home, "Applications", id.MacAppName),
		}
	}
}

// installedAppPath returns the first existing candidate, or "".
func installedAppPath(goos string, id brandIdentity, env func(string) string, home string, exists func(string) bool) string {
	for _, c := range desktopAppCandidates(goos, id, env, home) {
		if exists(c) {
			return c
		}
	}
	return ""
}

// brandJSONPathForApp returns where brand.json ships in the packaged app's
// resources (electron-builder extraResources), relative to the launchable
// path returned by installedAppPath.
func brandJSONPathForApp(goos, appPath string) string {
	if goos == "darwin" {
		return filepath.Join(appPath, "Contents", "Resources", "brand.json")
	}
	// windows: appPath = ...\<Brand>\<Brand>.exe → resources\brand.json beside it
	return filepath.Join(filepath.Dir(appPath), "resources", "brand.json")
}

// resolveDesktopIdentity finds the installed app (with OTTO defaults) and, if
// found, refines the identity from its bundled brand.json. Returns the
// (possibly refined) identity and the app path ("" if not installed).
func resolveDesktopIdentity(goos string, env func(string) string, home string, exists func(string) bool, readFile func(string) ([]byte, error)) (brandIdentity, string) {
	id := defaultBrandIdentity()
	appPath := installedAppPath(goos, id, env, home, exists)
	if appPath == "" {
		return id, ""
	}
	id = refineBrandIdentity(id, brandJSONPathForApp(goos, appPath), readFile)
	// Re-resolve the path under the refined identity (name may have changed).
	if p := installedAppPath(goos, id, env, home, exists); p != "" {
		appPath = p
	}
	return id, appPath
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./cmd/otto-tray/ -run 'Brand|Identity|Desktop|InstalledApp|Refine|Validate' -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/otto-tray/brand.go cmd/otto-tray/brand_test.go cmd/otto-tray/desktop.go cmd/otto-tray/desktop_test.go
git commit -m "feat(tray): brand identity + OTTO desktop installed-detection

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: <session URL>"
```

---

### Task 2: Running-detection (process check)

**Files:**
- Create: `cmd/otto-tray/desktoprun.go`, `cmd/otto-tray/desktop_darwin.go`, `cmd/otto-tray/desktop_windows.go`
- Test: `cmd/otto-tray/desktoprun_test.go`

**Interfaces produced:**
- `desktopRunningFn func(id brandIdentity) bool` (package var, overridable in tests; assigned per-platform)
- `isDesktopRunning(id brandIdentity) bool`

- [ ] **Step 1: Write the failing test** — `cmd/otto-tray/desktoprun_test.go`:

```go
//go:build darwin || windows

package main

import "testing"

func TestIsDesktopRunning_UsesSeam(t *testing.T) {
	old := desktopRunningFn
	defer func() { desktopRunningFn = old }()

	desktopRunningFn = func(id brandIdentity) bool { return true }
	if !isDesktopRunning(defaultBrandIdentity()) {
		t.Fatal("expected running=true from stub")
	}
	desktopRunningFn = func(id brandIdentity) bool { return false }
	if isDesktopRunning(defaultBrandIdentity()) {
		t.Fatal("expected running=false from stub")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/otto-tray/ -run TestIsDesktopRunning -v`
Expected: FAIL — undefined `desktopRunningFn` / `isDesktopRunning`.

- [ ] **Step 3: Implement the seam** — `cmd/otto-tray/desktoprun.go`:

```go
//go:build darwin || windows

package main

// desktopRunningFn is the process-liveness probe for the desktop app. It is a
// package var so tests can substitute a stub and so each platform file can
// assign its own implementation in init(). isDesktopRunning is the caller-facing
// entry.
var desktopRunningFn = func(id brandIdentity) bool { return false }

func isDesktopRunning(id brandIdentity) bool { return desktopRunningFn(id) }
```

- [ ] **Step 4: Implement the darwin probe** — `cmd/otto-tray/desktop_darwin.go`:

```go
//go:build darwin

package main

import "os/exec"

func init() { desktopRunningFn = platformDesktopRunning }

// platformDesktopRunning reports whether the packaged desktop process is alive.
// Matches the distinctive bundle path (…/OTTO.app/Contents/MacOS/OTTO) to avoid
// matching an unrelated process merely named "OTTO". The match string is a
// validated brand identity value (see validateDisplayName), so it is safe to
// pass to pgrep.
func platformDesktopRunning(id brandIdentity) bool {
	// #nosec G204 -- id.MacProcMatch derives from a validateDisplayName-checked
	// display name; no unsanitized input reaches exec.
	err := exec.Command("pgrep", "-f", id.MacProcMatch).Run()
	return err == nil // pgrep exits 0 iff ≥1 match
}
```

- [ ] **Step 5: Implement the windows probe** — `cmd/otto-tray/desktop_windows.go`:

```go
//go:build windows

package main

import (
	"os/exec"
	"strings"
)

func init() { desktopRunningFn = platformDesktopRunning }

// platformDesktopRunning reports whether the desktop .exe is running via
// tasklist. id.WinExeName derives from a validated display name.
func platformDesktopRunning(id brandIdentity) bool {
	// #nosec G204 -- id.WinExeName derives from a validateDisplayName-checked
	// display name; the filter value is quoted and bounded.
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+id.WinExeName, "/NH").Output()
	if err != nil {
		return false
	}
	// tasklist prints "INFO: No tasks..." when nothing matches; a real row
	// contains the image name.
	return strings.Contains(string(out), id.WinExeName)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./cmd/otto-tray/ -run TestIsDesktopRunning -v`
Expected: PASS. (The darwin probe file compiles into the darwin test build; the seam test uses the stub.)

- [ ] **Step 7: Commit**

```bash
git add cmd/otto-tray/desktoprun.go cmd/otto-tray/desktoprun_test.go cmd/otto-tray/desktop_darwin.go cmd/otto-tray/desktop_windows.go
git commit -m "feat(tray): desktop running-detection (process check, testable seam)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: <session URL>"
```

---

### Task 3: Desktop state machine + poller + menu wiring

**Files:**
- Create: `cmd/otto-tray/desktopstate.go`, `cmd/otto-tray/desktopstate_test.go`, `cmd/otto-tray/desktoptray.go`
- Modify: `cmd/otto-tray/tray.go` (add fields + wire the section/poller)

**Interfaces:**
- Consumes: `resolveDesktopIdentity` (T1), `isDesktopRunning` (T2), `systray`.
- Produces:
  - `DesktopState` const set: `DesktopNotInstalled/DesktopStopped/DesktopRunning/DesktopInstalling`
  - `desktopInput{ Installed, Running, Installing bool }`
  - `computeDesktopState(desktopInput) DesktopState`
  - `runDesktopPoller(ctx, probe func() desktopInput, tick <-chan time.Time, out chan<- DesktopState)`
  - `(*trayState) makeDesktopProbe() func() desktopInput`, `applyDesktopState(DesktopState)`, `desktopUILoop()`
  - `trayState` gains: `desktopCh chan DesktopState`, `desktopInstalling atomic.Bool`, `desktopAppPath atomic.Pointer[string]`, and menu-item fields.

- [ ] **Step 1: Write the failing state test** — `cmd/otto-tray/desktopstate_test.go`:

```go
//go:build darwin || windows

package main

import (
	"context"
	"testing"
	"time"
)

func TestComputeDesktopState(t *testing.T) {
	cases := []struct {
		in   desktopInput
		want DesktopState
	}{
		{desktopInput{Installing: true}, DesktopInstalling},
		{desktopInput{Installed: false}, DesktopNotInstalled},
		{desktopInput{Installed: true, Running: false}, DesktopStopped},
		{desktopInput{Installed: true, Running: true}, DesktopRunning},
		// installing wins even if already installed
		{desktopInput{Installed: true, Running: true, Installing: true}, DesktopInstalling},
	}
	for _, c := range cases {
		if got := computeDesktopState(c.in); got != c.want {
			t.Errorf("computeDesktopState(%+v)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestRunDesktopPoller_EmitsOnTick(t *testing.T) {
	tick := make(chan time.Time, 1)
	out := make(chan DesktopState, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runDesktopPoller(ctx, func() desktopInput { return desktopInput{Installed: true, Running: true} }, tick, out)
	tick <- time.Now()
	select {
	case s := <-out:
		if s != DesktopRunning {
			t.Fatalf("got %q", s)
		}
	case <-time.After(time.Second):
		t.Fatal("no emission")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/otto-tray/ -run 'ComputeDesktopState|RunDesktopPoller' -v`
Expected: FAIL — undefined types/functions.

- [ ] **Step 3: Implement `desktopstate.go`**

```go
//go:build darwin || windows

package main

import (
	"context"
	"time"
)

type DesktopState string

const (
	DesktopNotInstalled DesktopState = "not-installed"
	DesktopStopped      DesktopState = "stopped"
	DesktopRunning      DesktopState = "running"
	DesktopInstalling   DesktopState = "installing"
)

// desktopInput is the raw per-tick evidence. computeDesktopState is pure.
type desktopInput struct {
	Installed  bool
	Running    bool
	Installing bool // overlaid by the install handler while a run is in-flight
}

// computeDesktopState: installing wins (transient), then not-installed,
// then running vs stopped.
func computeDesktopState(in desktopInput) DesktopState {
	if in.Installing {
		return DesktopInstalling
	}
	if !in.Installed {
		return DesktopNotInstalled
	}
	if in.Running {
		return DesktopRunning
	}
	return DesktopStopped
}

// runDesktopPoller mirrors runPoller's shape but for the desktop: each tick it
// calls probe and emits the computed state. Independent of the gateway FSM.
func runDesktopPoller(ctx context.Context, probe func() desktopInput, tick <-chan time.Time, out chan<- DesktopState) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			s := computeDesktopState(probe())
			select {
			case out <- s:
			case <-ctx.Done():
				return
			}
		}
	}
}
```

- [ ] **Step 4: Add desktop fields to `trayState`** — in `cmd/otto-tray/tray.go`, add to the `trayState` struct (after the existing menu-item fields):

```go
	// desktop-app management (parallel to the gateway controls)
	desktopCh         chan DesktopState
	desktopInstalling atomic.Bool
	desktopAppPath    atomic.Pointer[string]
	miDesktopHeader   *systray.MenuItem
	miDesktopInstall  *systray.MenuItem
	miDesktopStart    *systray.MenuItem
	miDesktopStop     *systray.MenuItem
```

And initialize `desktopCh` in `newTrayState` (add to the struct literal): `desktopCh: make(chan DesktopState, 4),`.

- [ ] **Step 5: Add the menu section in `onReady`** — in `cmd/otto-tray/tray.go`, insert **after** the `s.miCopyHealth` block's trailing `systray.AddSeparator()` and **before** the `prefs := systray.AddMenuItem("Preferences", "")` line:

```go
		systray.AddSeparator()
		s.miDesktopHeader = systray.AddMenuItem("OTTO Desktop · …", "")
		s.miDesktopHeader.Disable()
		s.miDesktopInstall = systray.AddMenuItem("Install OTTO Desktop…", "Download and run the OTTO desktop installer")
		s.miDesktopStart = systray.AddMenuItem("Start OTTO Desktop", "")
		s.miDesktopStop = systray.AddMenuItem("Stop OTTO Desktop", "")
```

- [ ] **Step 6: Wire callbacks + poller** — in `cmd/otto-tray/tray.go`:

In `wireCallbacks`, add:
```go
	s.miDesktopInstall.Click(func() { go s.handleDesktopInstall() })
	s.miDesktopStart.Click(func() { go s.handleDesktopStart() })
	s.miDesktopStop.Click(func() { go s.handleDesktopStop() })
```

In `onReady`, right after the existing `go s.uiLoop()`:
```go
		dtick := time.NewTicker(3 * time.Second).C
		go runDesktopPoller(ctx, s.makeDesktopProbe(), dtick, s.desktopCh)
		go s.desktopUILoop()
```

- [ ] **Step 7: Implement `desktoptray.go`** (probe + apply + UI loop; handlers are stubs here, filled in Task 4)

```go
//go:build darwin || windows

package main

import (
	"os"
	"runtime"
)

// makeDesktopProbe returns a per-tick evidence gatherer: resolve the installed
// app (with brand.json refinement), record its path, and check liveness. The
// Installing flag is overlaid from the tray's atomic bool so an in-flight
// install shows the spinner state.
func (s *trayState) makeDesktopProbe() func() desktopInput {
	env := os.Getenv
	home, _ := os.UserHomeDir()
	readFile := os.ReadFile
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	return func() desktopInput {
		if s.desktopInstalling.Load() {
			return desktopInput{Installing: true}
		}
		id, appPath := resolveDesktopIdentity(runtime.GOOS, env, home, exists, readFile)
		if appPath == "" {
			return desktopInput{Installed: false}
		}
		s.desktopAppPath.Store(&appPath)
		return desktopInput{Installed: true, Running: isDesktopRunning(id)}
	}
}

func (s *trayState) desktopUILoop() {
	for st := range s.desktopCh {
		s.applyDesktopState(st)
	}
}

// applyDesktopState updates the desktop menu section for the given state.
// Install is shown only when not installed; Start/Stop are enabled by state.
func (s *trayState) applyDesktopState(st DesktopState) {
	switch st {
	case DesktopNotInstalled:
		s.miDesktopHeader.SetTitle("OTTO Desktop · not installed")
		s.miDesktopInstall.Show()
		s.miDesktopInstall.Enable()
		s.miDesktopStart.Hide()
		s.miDesktopStop.Hide()
	case DesktopInstalling:
		s.miDesktopHeader.SetTitle("OTTO Desktop · installing…")
		s.miDesktopInstall.Show()
		s.miDesktopInstall.Disable()
		s.miDesktopStart.Hide()
		s.miDesktopStop.Hide()
	case DesktopStopped:
		s.miDesktopHeader.SetTitle("OTTO Desktop · not running")
		s.miDesktopInstall.Hide()
		s.miDesktopStart.Show()
		s.miDesktopStart.Enable()
		s.miDesktopStop.Hide()
	case DesktopRunning:
		s.miDesktopHeader.SetTitle("OTTO Desktop · running")
		s.miDesktopInstall.Hide()
		s.miDesktopStart.Hide()
		s.miDesktopStop.Show()
		s.miDesktopStop.Enable()
	}
}

// Handlers — filled in Task 4. Declared here so wireCallbacks compiles.
func (s *trayState) handleDesktopInstall() {}
func (s *trayState) handleDesktopStart()   {}
func (s *trayState) handleDesktopStop()    {}
```

- [ ] **Step 8: Verify `atomic` + `time` imports** — `tray.go` already imports `sync/atomic` and `time`. Confirm `runtime` is imported in `desktoptray.go` (it is, above). Run the tests:

Run: `go test ./cmd/otto-tray/ -run 'ComputeDesktopState|RunDesktopPoller' -v && go build ./cmd/otto-tray/`
Expected: tests PASS; build succeeds (menu wiring + stub handlers compile).

- [ ] **Step 9: Commit**

```bash
git add cmd/otto-tray/desktopstate.go cmd/otto-tray/desktopstate_test.go cmd/otto-tray/desktoptray.go cmd/otto-tray/tray.go
git commit -m "feat(tray): desktop state machine, poller, and menu section

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: <session URL>"
```

---

### Task 4: Actions — install / start / stop

**Files:**
- Create: `cmd/otto-tray/desktopcmd.go`, `cmd/otto-tray/desktopcmd_test.go`
- Modify: `cmd/otto-tray/desktoptray.go` (replace the three stub handlers)

**Interfaces produced:**
- `desktopInstallCommand(goos string) (name string, args []string)`
- `desktopStartCommand(goos, appPath string) (name string, args []string)`
- `desktopStopCommand(goos string, id brandIdentity, force bool) (name string, args []string)`
- `runCmd(timeout time.Duration, dir, name string, args ...string) runResult`
- `spawnDetached(dir, name string, args ...string) error`

- [ ] **Step 1: Write the failing command-construction tests** — `cmd/otto-tray/desktopcmd_test.go`:

```go
//go:build darwin || windows

package main

import (
	"strings"
	"testing"
)

func TestDesktopInstallCommand(t *testing.T) {
	n, a := desktopInstallCommand("windows")
	if n != "powershell" || !strings.Contains(strings.Join(a, " "), "cmetech/otto/main/install.ps1") {
		t.Fatalf("win install: %s %v", n, a)
	}
	n, a = desktopInstallCommand("darwin")
	if n != "/bin/sh" || !strings.Contains(strings.Join(a, " "), "cmetech/otto/main/install.sh") {
		t.Fatalf("mac install: %s %v", n, a)
	}
}

func TestDesktopStartCommand(t *testing.T) {
	n, a := desktopStartCommand("darwin", "/Applications/OTTO.app")
	if n != "open" || len(a) != 1 || a[0] != "/Applications/OTTO.app" {
		t.Fatalf("mac start: %s %v", n, a)
	}
	n, a = desktopStartCommand("windows", `C:\P\OTTO\OTTO.exe`)
	if n != `C:\P\OTTO\OTTO.exe` || len(a) != 0 {
		t.Fatalf("win start: %s %v", n, a)
	}
}

func TestDesktopStopCommand(t *testing.T) {
	id := defaultBrandIdentity()
	n, a := desktopStopCommand("windows", id, false)
	if n != "taskkill" || strings.Join(a, " ") != "/IM OTTO.exe /T" {
		t.Fatalf("win stop graceful: %s %v", n, a)
	}
	n, a = desktopStopCommand("windows", id, true)
	if strings.Join(a, " ") != "/IM OTTO.exe /T /F" {
		t.Fatalf("win stop force: %v", a)
	}
	n, a = desktopStopCommand("darwin", id, false)
	if n != "osascript" || !strings.Contains(strings.Join(a, " "), `quit app "OTTO"`) {
		t.Fatalf("mac stop graceful: %s %v", n, a)
	}
	n, a = desktopStopCommand("darwin", id, true)
	if n != "pkill" || a[0] != "-f" || a[1] != id.MacProcMatch {
		t.Fatalf("mac stop force: %s %v", n, a)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/otto-tray/ -run 'DesktopInstallCommand|DesktopStartCommand|DesktopStopCommand' -v`
Expected: FAIL — undefined functions.

- [ ] **Step 3: Implement `desktopcmd.go`**

```go
//go:build darwin || windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// desktopInstallCommand returns the CONSTANT OTTO desktop installer one-liner
// per OS (assume-otto, v1). No brand-derived interpolation → no injectable
// input reaches exec (gosec G204).
func desktopInstallCommand(goos string) (string, []string) {
	if goos == "windows" {
		return "powershell", []string{"-NoProfile", "-Command",
			"irm https://raw.githubusercontent.com/cmetech/otto/main/install.ps1 | iex"}
	}
	return "/bin/sh", []string{"-c",
		"curl -fsSL https://raw.githubusercontent.com/cmetech/otto/main/install.sh | sh"}
}

// desktopStartCommand launches the installed app. appPath comes from the
// allowlisted well-known locations (installedAppPath), not user input.
func desktopStartCommand(goos, appPath string) (string, []string) {
	if goos == "darwin" {
		return "open", []string{appPath}
	}
	return appPath, nil // windows: run the exe directly
}

// desktopStopCommand builds a graceful (force=false) or forced (force=true)
// stop. id fields derive from a validateDisplayName-checked name.
func desktopStopCommand(goos string, id brandIdentity, force bool) (string, []string) {
	if goos == "windows" {
		args := []string{"/IM", id.WinExeName, "/T"}
		if force {
			args = append(args, "/F")
		}
		return "taskkill", args
	}
	if force {
		return "pkill", []string{"-f", id.MacProcMatch}
	}
	return "osascript", []string{"-e", `quit app "` + id.DisplayName + `"`}
}

// runCmd runs a command to completion with a timeout and captures output.
// Mirrors runWrapper for arbitrary (constant-shape) desktop commands.
func runCmd(timeout time.Duration, dir, name string, args ...string) runResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name+args are constants or validated brand tokens (see brand.go / callers)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exit := 0
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		err = fmt.Errorf("run %s: %w", name, err)
	}
	return runResult{ExitCode: exit, Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
}

// spawnDetached starts a process in its own group and returns immediately
// (used to launch the desktop app so quitting the tray never signals it).
func spawnDetached(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...) //nolint:gosec // name is an allowlisted app path / "open"; args validated
	if dir != "" {
		cmd.Dir = dir
	}
	detachProcessGroup(cmd) // existing helper (darwin: Setpgid; windows: DETACHED_PROCESS)
	return cmd.Start()
}
```

- [ ] **Step 4: Replace the stub handlers in `desktoptray.go`**

```go
func (s *trayState) handleDesktopInstall() {
	if !confirmDialog("Install OTTO Desktop",
		"Download and run the official OTTO desktop installer now?", "Install", "Cancel") {
		return
	}
	s.desktopInstalling.Store(true)
	defer s.desktopInstalling.Store(false)
	name, args := desktopInstallCommand(runtime.GOOS)
	res := runCmd(10*time.Minute, "", name, args...) // installs are slow (download+unpack+bootstrap)
	if res.ExitCode != 0 || res.Err != nil {
		notify("OTTO Desktop", "Install failed: "+firstLine(res.Stderr))
		return
	}
	notify("OTTO Desktop", "OTTO desktop installed.")
	// next poll re-detects → state flips to stopped/running
}

func (s *trayState) handleDesktopStart() {
	p := s.desktopAppPath.Load()
	if p == nil || *p == "" {
		notify("OTTO Desktop", "Desktop app not found. Install it first.")
		return
	}
	name, args := desktopStartCommand(runtime.GOOS, *p)
	if err := spawnDetached("", name, args...); err != nil {
		notify("OTTO Desktop", "Failed to start: "+err.Error())
	}
}

func (s *trayState) handleDesktopStop() {
	if !confirmDialog("Stop OTTO Desktop",
		"Stop the OTTO desktop app? Any unsaved work in it may be lost.", "Stop", "Cancel") {
		return
	}
	id, _ := resolveDesktopIdentity(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile)
	// graceful first
	name, args := desktopStopCommand(runtime.GOOS, id, false)
	res := runCmd(15*time.Second, "", name, args...)
	// forced fallback if still alive shortly after
	time.Sleep(1500 * time.Millisecond)
	if isDesktopRunning(id) {
		fname, fargs := desktopStopCommand(runtime.GOOS, id, true)
		res = runCmd(15*time.Second, "", fname, fargs...)
	}
	if res.Err != nil && isDesktopRunning(id) {
		notify("OTTO Desktop", "Failed to stop: "+firstLine(res.Stderr))
	}
}

// small helpers reused by handlers
func homeDir() string { h, _ := os.UserHomeDir(); return h }
func statExists(p string) bool { _, err := os.Stat(p); return err == nil }
```

Add `"os"`, `"runtime"`, `"time"` to `desktoptray.go`'s imports (os + runtime already added in Task 3; add `"time"`).

- [ ] **Step 5: Run all desktop tests + build**

Run: `go test ./cmd/otto-tray/ -run 'Desktop|Brand|Identity|Refine|Validate|InstalledApp|IsDesktopRunning' -v && go build ./cmd/otto-tray/`
Expected: all PASS; build succeeds.

- [ ] **Step 6: Commit**

```bash
git add cmd/otto-tray/desktopcmd.go cmd/otto-tray/desktopcmd_test.go cmd/otto-tray/desktoptray.go
git commit -m "feat(tray): desktop install/start/stop actions (stop confirms; graceful→forced)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: <session URL>"
```

---

### Task 5: Trust-gate, cross-build, docs, manual E2E

**Files:**
- Modify: `README.md` (or the tray's doc section)

- [ ] **Step 1: Full package test + vet (macOS box)**

Run: `go test ./cmd/otto-tray/... && go vet ./cmd/otto-tray/...`
Expected: all pass (existing gateway-tray regression tests + the new desktop tests).

- [ ] **Step 2: gosec + golangci (the project trust gate)**

Run: `gosec ./cmd/otto-tray/... ; golangci-lint run ./cmd/otto-tray/...`
Expected: no new findings. Every `exec` in the new code is either a constant command or uses validated brand tokens; each `//nolint:gosec`/`#nosec` carries a justification. Fix any real finding; do not blanket-suppress.

- [ ] **Step 3: Cross-compile both tray targets** (confirm no build breakage on Windows)

Run:
```bash
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=1 go build -o /tmp/otto-tray-darwin  ./cmd/otto-tray/
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 go build -o /tmp/otto-tray.exe    ./cmd/otto-tray/ 2>&1 | tail -5 || echo "(windows cgo cross-build needs mingw; if unavailable, rely on CI windows runner — note it)"
```
Expected: darwin builds locally. If the Windows cgo cross-build is unavailable on this box, note that CI's windows runner covers it (the existing tray already relies on this); the pure `goos`-parameterized tests already exercise the Windows branches on macOS.

- [ ] **Step 4: Verify the Windows resources path assumption** (open item from the spec)

Confirm against a real packaged OTTO install that `brand.json` lands at `%LOCALAPPDATA%\Programs\OTTO\resources\brand.json` (electron-builder extraResources). If the packaged layout differs, adjust `brandJSONPathForApp` — detection still works via defaults if `brand.json` is absent (non-fatal), so this is a refinement, not a blocker.

- [ ] **Step 5: Manual E2E smoke** (record results in the PR/commit message)

macOS: launch `otto-tray` with the desktop **not** installed → menu shows "OTTO Desktop · not installed" + "Install…". Click Install → "installing…" → on success the app appears and the section flips to "not running". Click Start → app launches, section shows "running". Click Stop → confirm dialog → app quits, section returns to "not running". Mirror on Windows.

- [ ] **Step 6: Docs** — add a short "OTTO Desktop" subsection to `README.md` (or the tray's doc) describing the three actions and that the tray installs the desktop via the published `cmetech/otto` one-liner. Note the v1 limitations (assume-OTTO; process-name detection).

- [ ] **Step 7: Commit**

```bash
git add README.md
git commit -m "docs(tray): document the OTTO Desktop menu section

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: <session URL>"
```

---

## Self-Review

**Spec coverage:**
- Install from tray (constant `cmetech/otto` one-liner, confirm, async, spinner) → Task 4 (`desktopInstallCommand`, `handleDesktopInstall`). ✅
- Start/stop + running-state → Task 2 (running-detection), Task 4 (start/stop; **stop confirms**). ✅
- Windows + macOS → platform files (T2) + `goos`-parameterized pure funcs tested on mac (T1/T3/T4). ✅
- Process-name running-detection → Task 2. ✅
- Hardcode OTTO + read brand.json from **app resources** → Task 1 (`resolveDesktopIdentity` + `brandJSONPathForApp`). ✅
- Menu section, state→item mapping → Task 3. ✅
- gosec-clean exec (constant install; validated tokens; justified nolint) → constraints + Task 5 gate. ✅
- Distribution (no install-script change; tray bundled) → covered by the spec; no plan task needed (ship = rebuild+release). ✅
- Gateway FSM untouched → desktop poller is independent (Task 3). ✅

**Placeholder scan:** No "TBD"/"handle appropriately"; every code step is complete. The two spec open-items (Windows cgo cross-build availability; Windows resources path) are explicit verification steps (Task 5.3/5.4) with stated fallbacks, not gaps.

**Type consistency:** `brandIdentity`, `desktopInput`, `DesktopState`, `runResult` (reused from `runner.go`), and the handler/probe signatures are used identically across Tasks 1–4. `desktopRunningFn`/`isDesktopRunning`, `resolveDesktopIdentity`, `desktop{Install,Start,Stop}Command` names match their definitions and call sites. `detachProcessGroup`, `confirmDialog`, `notify`, `firstLine` are existing helpers used with their real signatures.

**Scope:** Single cohesive feature; 5 right-sized tasks (4 code, 1 gate/docs). No decomposition needed.
