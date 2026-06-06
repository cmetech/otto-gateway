# OTTO Tray — Menu-Bar / System-Tray Launcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a new `otto-tray` binary (darwin + windows) that gives operators a menu-bar / system-tray UI for the OTTO Gateway, driving the existing `otto-gw[.ps1]` wrappers via subprocess and polling `/health` + `/admin/api/snapshot` for status, while keeping the gateway cgo-free.

**Architecture:** Separate Go binary at `cmd/otto-tray/` with a `//go:build darwin || windows` constraint so headless Linux CI never tries to build cgo deps. Library: `github.com/energye/systray`. The tray invokes `otto-gw start|stop|restart` as a subprocess with a detached process group; it reads the PID file and the gateway's `/health`+`/admin/api/snapshot` endpoints to drive a 6-state FSM (`unknown`/`stopped`/`starting`/`running`/`degraded`/`error`) polled every 3 seconds. A tiny read-only dotenv reader resolves `HTTP_ADDR` from `.otto-gw.overrides.env` → `.otto-gw.env` → process env. Tray-local prefs persist at `~/.otto-gw/tray.json`; login-item registration uses a LaunchAgent plist on darwin and an `HKCU\…\Run` value on windows.

**Tech Stack:** Go 1.24 (matches `go.mod`), `github.com/energye/systray` (cgo on darwin/windows), `golang.org/x/sys` (windows registry), stdlib `os/exec`, `net/http`, `encoding/json`. No additions to the gateway's deps — the tray gets its own indirect transitive deps via `go.mod`'s usual flow.

**Specification:** `docs/superpowers/specs/2026-06-06-tray-menubar-launcher-design.md`

**Non-goals for this plan:** Linux build, `.app` bundle, code-signing, embedded settings UI, multi-instance support, auto-update. All explicitly deferred per the spec.

---

## File Structure

New tray-binary files (all under `cmd/otto-tray/`, all with `//go:build darwin || windows` unless noted):

| File                            | Responsibility                                                                                          |
|---------------------------------|---------------------------------------------------------------------------------------------------------|
| `cmd/otto-tray/main.go`         | Entry point. Parses `--uninstall` flag, wires components, calls `systray.Run`.                          |
| `cmd/otto-tray/installroot.go`  | Resolves install root via `os.Executable()` walk-up + `OTTO_HOME` env override.                         |
| `cmd/otto-tray/dotenv.go`       | Read-only dotenv parser. Resolves `HTTP_ADDR` from the wrapper's precedence chain.                       |
| `cmd/otto-tray/pidfile.go`      | Reads `.otto/gw/otto-gateway.pid`, returns alive/stale/missing.                                         |
| `cmd/otto-tray/status.go`       | HTTP client for `/health`, `/health/pool`, `/health/hooks`, `/admin/api/snapshot`. 1s timeout.          |
| `cmd/otto-tray/fsm.go`          | Pure-function combining PID liveness + HTTP responses into the 6 status states.                         |
| `cmd/otto-tray/poller.go`       | 3-second ticker driving the FSM. Exposes a state channel for the UI.                                    |
| `cmd/otto-tray/runner.go`       | Subprocess invocation of `otto-gw` (darwin) / `pwsh otto-gw.ps1` (windows). Detached pgrp. 30s timeout. |
| `cmd/otto-tray/config.go`       | Read/write `~/.otto-gw/tray.json`. Defaults to both toggles off.                                        |
| `cmd/otto-tray/autostart_darwin.go`  | Write/remove LaunchAgent plist. `launchctl bootstrap/bootout`.                                     |
| `cmd/otto-tray/autostart_windows.go` | Set/clear `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\OttoTray`.                          |
| `cmd/otto-tray/tray.go`         | Builds the menu, wires button callbacks, owns the state→UI loop. Calls runner+poller+autostart.        |
| `cmd/otto-tray/onboarding.go`   | First-run toast (no `tray.json` exists) offering login-item registration.                              |
| `cmd/otto-tray/icon/icon.go`    | `//go:embed`-ed PNG. Template image on darwin (auto-adapts to dark/light menu bar).                     |
| `cmd/otto-tray/icon/template.png` | 22×22 monochrome PNG, mostly transparent. Source asset.                                              |

Existing files modified:

| File                            | Change                                                                                                   |
|---------------------------------|----------------------------------------------------------------------------------------------------------|
| `Makefile`                      | Add `cross-otto-tray-darwin-{arm64,amd64}`, `cross-otto-tray-windows-amd64`; wire into `package-*`.     |
| `scripts/install.sh`            | Strip quarantine on `bin/otto-tray` (darwin only). Add footer line pointing at the binary.              |
| `scripts/install.ps1`           | Add footer line showing `Start-Process` for the tray.                                                    |
| `docs/operator-quickstart.md`   | Section: "Launching the tray app."                                                                       |
| `README.md`                     | One-line mention with link to operator-quickstart.                                                       |

No changes to `otto-gateway` source. No changes to `scripts/otto-gw` or `scripts/otto-gw.ps1`. No changes to `.go-arch-lint.yml` (operates only under `internal/`).

---

## Conventions used throughout this plan

- **Build tag on every tray `.go` file** except `icon/icon.go` (icon embed must compile on all platforms so `go build ./...` from any host works):
  ```go
  //go:build darwin || windows
  ```
- **Test files** mirror this tag. Linux CI skips the entire `cmd/otto-tray/` package via the tag.
- **Commit style** matches the repo: `feat(tray): …`, `test(tray): …`, `chore(tray): …`. Each commit ends with the existing `Co-Authored-By` footer the project uses.
- **TDD where there is real logic.** UI glue, Makefile, install scripts, and README changes are edit-verify-commit without a unit test.
- **All test commands assume cwd = repo root.**

---

## Task 1: Scaffold the package and make `make build` stay green on linux

**Files:**
- Create: `cmd/otto-tray/doc.go`
- Create: `cmd/otto-tray/main.go` (minimal stub)
- Create: `cmd/otto-tray/icon/icon.go`
- Create: `cmd/otto-tray/icon/template.png` (binary asset — see Step 3)

- [ ] **Step 1: Create the package doc file (no build tag — pure docs)**

`cmd/otto-tray/doc.go`:
```go
// Package main is the OTTO Tray binary: a menu-bar (darwin) and
// system-tray (windows) launcher for the OTTO Gateway. The gateway
// itself stays cgo-free; cgo is contained here. See
// docs/superpowers/specs/2026-06-06-tray-menubar-launcher-design.md.
package main
```

- [ ] **Step 2: Create the minimal stub `main.go`**

`cmd/otto-tray/main.go`:
```go
//go:build darwin || windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "otto-tray: not yet implemented")
	os.Exit(2)
}
```

- [ ] **Step 3: Create a placeholder icon**

Generate a 22×22 transparent PNG. Run from the repo root:
```bash
mkdir -p cmd/otto-tray/icon
# A 22x22 fully transparent PNG, base64-encoded. Replace with a real
# monochrome glyph in a follow-up commit; this unblocks the build.
python3 -c "import base64,sys; sys.stdout.buffer.write(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAABYAAAAWCAQAAABuhYKvAAAAEElEQVR42mNkYGD4z0AswAEDAQAJSAEU61f2GwAAAABJRU5ErkJggg=='))" > cmd/otto-tray/icon/template.png
ls -la cmd/otto-tray/icon/template.png
```
Expected: file exists, ~120 bytes.

- [ ] **Step 4: Create the icon embed wrapper**

`cmd/otto-tray/icon/icon.go`:
```go
// Package icon embeds the tray status icon. The asset compiles on
// every platform (no build tag) so `go build ./...` from a linux
// CI host still succeeds; the platform-gated main package consumes
// it only when the tray binary is built.
package icon

import _ "embed"

//go:embed template.png
var Template []byte
```

- [ ] **Step 5: Verify the linux build is unaffected**

Run: `GOOS=linux GOARCH=amd64 go build ./...`
Expected: succeeds with no output. The `cmd/otto-tray/main.go` file is excluded by its build tag; the `icon` package compiles fine.

- [ ] **Step 6: Verify the darwin build of the tray package**

Run: `GOOS=darwin GOARCH=arm64 go build ./cmd/otto-tray`
Expected: succeeds. Produces a binary in cwd (or `bin/otto-tray` if `-o` set). Note: until `energye/systray` is imported (Task 12), this build needs no cgo. Cgo enters in Task 12.

- [ ] **Step 7: Verify the existing gateway build is unaffected**

Run: `make build`
Expected: builds `bin/otto-gateway` exactly as before; no new errors.

- [ ] **Step 8: Commit**

```bash
git add cmd/otto-tray/
git commit -m "$(cat <<'EOF'
feat(tray): scaffold cmd/otto-tray with placeholder main + icon embed

The package compiles under darwin/windows build tags; the icon
embed compiles on every platform so headless linux CI is unaffected.
Stub main exits 2 — real wiring comes in later tasks.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Install-root resolution

**Files:**
- Create: `cmd/otto-tray/installroot.go`
- Test: `cmd/otto-tray/installroot_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/otto-tray/installroot_test.go`:
```go
//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveInstallRoot_OTTOHomeWins(t *testing.T) {
	t.Setenv("OTTO_HOME", "/tmp/custom-home")
	got, err := resolveInstallRoot()
	if err != nil {
		t.Fatalf("resolveInstallRoot: %v", err)
	}
	if got != "/tmp/custom-home" {
		t.Fatalf("OTTO_HOME ignored: got %q, want /tmp/custom-home", got)
	}
}

func TestResolveInstallRoot_WalksUpFromExecutable(t *testing.T) {
	// Simulate a packaged layout: <root>/bin/<exe>. We construct the
	// layout in a tempdir and point execPath at the inner exe; the
	// resolver should return the tempdir.
	t.Setenv("OTTO_HOME", "")
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "otto-tray")
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveInstallRootFrom(exe)
	if err != nil {
		t.Fatalf("resolveInstallRootFrom: %v", err)
	}
	// EvalSymlinks because t.TempDir() on darwin is under /var ->
	// /private/var; resolveInstallRootFrom resolves symlinks for the
	// same reason the shell wrapper does (the install root is canonical).
	wantResolved, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Fatalf("install root: got %q, want %q", gotResolved, wantResolved)
	}
}
```

- [ ] **Step 2: Run the test, expect failure**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestResolveInstallRoot -count=1`
Expected: FAIL — `resolveInstallRoot` and `resolveInstallRootFrom` undefined.

- [ ] **Step 3: Implement**

`cmd/otto-tray/installroot.go`:
```go
//go:build darwin || windows

package main

import (
	"errors"
	"os"
	"path/filepath"
)

// resolveInstallRoot returns the install root of the OTTO Gateway
// distribution. Precedence:
//   1. $OTTO_HOME (used by dev/worktree runs)
//   2. <executable>'s parent directory's parent (the "bin/" walk-up)
//
// Symlinks in the executable path are resolved so the result matches
// the canonical install root the shell wrapper computes.
func resolveInstallRoot() (string, error) {
	if v := os.Getenv("OTTO_HOME"); v != "" {
		return v, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return resolveInstallRootFrom(exe)
}

func resolveInstallRootFrom(execPath string) (string, error) {
	if execPath == "" {
		return "", errors.New("empty exec path")
	}
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		// EvalSymlinks fails when the file does not exist on the
		// canonical path (e.g. during tests with a tmpfile). Fall
		// back to the raw exec path; the parent walk still works.
		resolved = execPath
	}
	binDir := filepath.Dir(resolved)
	root := filepath.Dir(binDir)
	if root == "" || root == "." {
		return "", errors.New("cannot resolve install root from " + execPath)
	}
	return root, nil
}
```

- [ ] **Step 4: Run the test, expect pass**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestResolveInstallRoot -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/otto-tray/installroot.go cmd/otto-tray/installroot_test.go
git commit -m "$(cat <<'EOF'
feat(tray): resolve install root via OTTO_HOME or executable walk-up

Mirrors the shell wrapper's symlink-resolving install-root anchor
so the tray and the wrapper agree on which .otto-gw/ they manage.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Dotenv reader + HTTP_ADDR resolver

**Files:**
- Create: `cmd/otto-tray/dotenv.go`
- Test: `cmd/otto-tray/dotenv_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/otto-tray/dotenv_test.go`:
```go
//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseDotenv_ParsesAndIgnoresCommentsAndBlanks(t *testing.T) {
	body := `# top comment
HTTP_ADDR=:18080

# another
AUTH_TOKEN="quoted value"
EMPTY=
KEY_WITH_EQUALS=foo=bar
`
	got, err := parseDotenv([]byte(body))
	if err != nil {
		t.Fatalf("parseDotenv: %v", err)
	}
	want := map[string]string{
		"HTTP_ADDR":       ":18080",
		"AUTH_TOKEN":      "quoted value",
		"EMPTY":           "",
		"KEY_WITH_EQUALS": "foo=bar",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: got %q, want %q", k, got[k], v)
		}
	}
}

func TestResolveDashboardURL_DefaultWhenNothingSet(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	tmp := t.TempDir()
	url := resolveDashboardURL(tmp)
	if url != "http://127.0.0.1:18080" {
		t.Fatalf("default URL: got %q, want http://127.0.0.1:18080", url)
	}
}

func TestResolveDashboardURL_OverridesEnvFileWins(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, ".env.otto-gw"), "HTTP_ADDR=:19000\n")
	writeFile(t, filepath.Join(tmp, ".otto-gw.overrides.env"), "HTTP_ADDR=:19999\n")
	url := resolveDashboardURL(tmp)
	if url != "http://127.0.0.1:19999" {
		t.Fatalf("overrides should win: got %q, want :19999", url)
	}
}

func TestResolveDashboardURL_ProcessEnvLowestPriority(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":20000")
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, ".env.otto-gw"), "HTTP_ADDR=:21000\n")
	url := resolveDashboardURL(tmp)
	if url != "http://127.0.0.1:21000" {
		t.Fatalf("env file should beat process env: got %q, want :21000", url)
	}
}

func TestResolveDashboardURL_StripsLeadingColonOnly(t *testing.T) {
	t.Setenv("HTTP_ADDR", "0.0.0.0:18080")
	tmp := t.TempDir()
	url := resolveDashboardURL(tmp)
	// A bound 0.0.0.0 listener is reachable on 127.0.0.1; we always
	// display the loopback for the operator to click.
	if url != "http://127.0.0.1:18080" {
		t.Fatalf("0.0.0.0:18080 should display as 127.0.0.1:18080, got %q", url)
	}
}
```

- [ ] **Step 2: Run the test, expect failure**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestParseDotenv -count=1`
Expected: FAIL — `parseDotenv` and `resolveDashboardURL` undefined.

- [ ] **Step 3: Implement**

`cmd/otto-tray/dotenv.go`:
```go
//go:build darwin || windows

package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// parseDotenv parses a minimal KEY=VALUE dotenv format. Comments
// (lines beginning with `#`) and blank lines are ignored. Values may
// be wrapped in single or double quotes; quotes are stripped. No
// variable interpolation, no `export ` prefix handling — the wrapper
// scripts own that. This is intentionally read-only and minimal.
func parseDotenv(body []byte) (map[string]string, error) {
	out := map[string]string{}
	s := bufio.NewScanner(bytes.NewReader(body))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if n := len(val); n >= 2 {
			if (val[0] == '"' && val[n-1] == '"') ||
				(val[0] == '\'' && val[n-1] == '\'') {
				val = val[1 : n-1]
			}
		}
		out[key] = val
	}
	return out, s.Err()
}

// readDotenvFile reads and parses a dotenv file. Missing file → nil
// map, nil error (caller treats absence as "no overrides here").
func readDotenvFile(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseDotenv(body)
}

// resolveDashboardURL returns the URL the tray's "Open dashboard"
// action should open. It applies the wrapper's HTTP_ADDR precedence:
//   1. <installRoot>/.otto-gw.overrides.env
//   2. <installRoot>/.env.otto-gw
//   3. $HTTP_ADDR
//   4. ":18080" default
//
// The host portion is always normalized to 127.0.0.1 because a
// bound 0.0.0.0 listener is still reachable on the loopback and
// that's what the operator wants to click.
func resolveDashboardURL(installRoot string) string {
	addr := lookupHTTPAddr(installRoot)
	if addr == "" {
		addr = ":18080"
	}
	// addr may be ":18080", "host:18080", or "0.0.0.0:18080".
	// Split host:port; if no host, default to loopback.
	host := "127.0.0.1"
	port := strings.TrimPrefix(addr, ":")
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		port = addr[i+1:]
	}
	return "http://" + host + ":" + port
}

func lookupHTTPAddr(installRoot string) string {
	for _, name := range []string{".otto-gw.overrides.env", ".env.otto-gw"} {
		m, _ := readDotenvFile(filepath.Join(installRoot, name))
		if v, ok := m["HTTP_ADDR"]; ok {
			return v
		}
	}
	return os.Getenv("HTTP_ADDR")
}
```

- [ ] **Step 4: Run the test, expect pass**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run "TestParseDotenv|TestResolveDashboardURL" -count=1`
Expected: PASS for all 5 sub-tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/otto-tray/dotenv.go cmd/otto-tray/dotenv_test.go
git commit -m "$(cat <<'EOF'
feat(tray): read-only dotenv reader + dashboard URL resolver

Honors the wrapper's HTTP_ADDR precedence
(.otto-gw.overrides.env > .env.otto-gw > process env) so the tray's
'Open dashboard' link always points at the same listener the operator
configured. 127.0.0.1 is forced as the host since 0.0.0.0 binds
reach loopback and we want a clickable URL.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: PID-file liveness reader

**Files:**
- Create: `cmd/otto-tray/pidfile.go`
- Test: `cmd/otto-tray/pidfile_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/otto-tray/pidfile_test.go`:
```go
//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReadPIDFile_MissingFile(t *testing.T) {
	got, err := readPIDFile(filepath.Join(t.TempDir(), "absent.pid"))
	if err != nil {
		t.Fatalf("missing should be nil error, got %v", err)
	}
	if got != 0 {
		t.Fatalf("missing pid: want 0, got %d", got)
	}
}

func TestReadPIDFile_ParsesPID(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gw.pid")
	if err := os.WriteFile(path, []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("readPIDFile: %v", err)
	}
	if got != 12345 {
		t.Fatalf("pid: want 12345, got %d", got)
	}
}

func TestReadPIDFile_GarbageReturnsZero(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gw.pid")
	if err := os.WriteFile(path, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := readPIDFile(path)
	if got != 0 {
		t.Fatalf("garbage pid: want 0, got %d", got)
	}
}

func TestProcessAlive_SelfIsAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatalf("our own pid (%d) should report alive", os.Getpid())
	}
}

func TestProcessAlive_ImplausiblePIDIsDead(t *testing.T) {
	// PID 2^30 is well above any realistic process table.
	if processAlive(1 << 30) {
		t.Fatalf("implausible pid should report dead")
	}
}

func TestReadPIDFile_ResolvesRelative(t *testing.T) {
	// Sanity: we never pass relative paths in production, but a regression
	// here means an installroot bug ends up as a silent "stopped".
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gw.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readPIDFile(path)
	if err != nil || got != os.Getpid() {
		t.Fatalf("readPIDFile self: got (%d,%v), want (%d,nil)", got, err, os.Getpid())
	}
}
```

- [ ] **Step 2: Run the test, expect failure**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run "TestReadPIDFile|TestProcessAlive" -count=1`
Expected: FAIL — `readPIDFile` and `processAlive` undefined.

- [ ] **Step 3: Implement**

`cmd/otto-tray/pidfile.go`:
```go
//go:build darwin || windows

package main

import (
	"os"
	"strconv"
	"strings"
)

// readPIDFile reads the gateway's PID file. Returns (0, nil) when
// the file is absent; (0, nil) when the contents are unparseable
// (treated as "stopped" by the caller — we never want a parse error
// to crash the tray). Returns a non-nil error only on hard read
// failure (permission, IO).
func readPIDFile(path string) (int, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return 0, nil
	}
	pid, perr := strconv.Atoi(s)
	if perr != nil {
		return 0, nil
	}
	return pid, nil
}

// processAlive reports whether a process with the given PID is
// currently running. On darwin we use os.FindProcess + Signal(0)
// which is the standard kill(0) liveness check. On windows
// os.FindProcess returns nil error even for dead PIDs, so we use
// a Signal(0) probe too — windows treats Signal(0) as a probe via
// the syscall layer.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall0()) == nil
}
```

Then create the platform-specific signal-0 helper because windows
does not expose `syscall.Signal(0)` cleanly.

`cmd/otto-tray/pidfile_darwin.go`:
```go
//go:build darwin

package main

import "syscall"

func syscall0() syscall.Signal { return syscall.Signal(0) }
```

`cmd/otto-tray/pidfile_windows.go`:
```go
//go:build windows

package main

import "syscall"

// On windows, Signal(0) is accepted by os.Process.Signal as a
// probe (the runtime translates it through OpenProcess + the
// process exit code check). We use syscall.Signal(0) to keep
// the cross-platform pidfile.go uniform.
func syscall0() syscall.Signal { return syscall.Signal(0) }
```

- [ ] **Step 4: Run the test, expect pass**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run "TestReadPIDFile|TestProcessAlive" -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/otto-tray/pidfile.go cmd/otto-tray/pidfile_darwin.go cmd/otto-tray/pidfile_windows.go cmd/otto-tray/pidfile_test.go
git commit -m "$(cat <<'EOF'
feat(tray): pidfile reader + kill(0) liveness probe

Garbage / missing files map to pid 0 (treated as 'stopped' upstream).
Hard read errors propagate; cosmetic parse errors don't crash the
tray. Per-OS Signal(0) helper accommodates windows' looser semantics.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: HTTP status client

**Files:**
- Create: `cmd/otto-tray/status.go`
- Test: `cmd/otto-tray/status_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/otto-tray/status_test.go`:
```go
//go:build darwin || windows

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStatusClient_HealthOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := newStatusClient(srv.URL, 1*time.Second)
	if !c.healthOK() {
		t.Fatalf("expected healthOK to be true")
	}
}

func TestStatusClient_HealthBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	c := newStatusClient(srv.URL, 1*time.Second)
	if c.healthOK() {
		t.Fatalf("expected healthOK false on 503")
	}
}

func TestStatusClient_HealthUnreachable(t *testing.T) {
	c := newStatusClient("http://127.0.0.1:1", 200*time.Millisecond) // port 1: connection refused
	if c.healthOK() {
		t.Fatalf("expected healthOK false on connection refused")
	}
}

func TestStatusClient_SnapshotParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"uptime_seconds": 4283,
			"pool": {"ready": 4, "total": 4},
			"hooks": [{"name":"auth","status":"ok"},{"name":"pii","status":"ok"}]
		}`))
	}))
	defer srv.Close()
	c := newStatusClient(srv.URL, 1*time.Second)
	snap, err := c.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.UptimeSeconds != 4283 {
		t.Errorf("uptime: got %d, want 4283", snap.UptimeSeconds)
	}
	if snap.PoolReady != 4 || snap.PoolTotal != 4 {
		t.Errorf("pool: got %d/%d, want 4/4", snap.PoolReady, snap.PoolTotal)
	}
	if len(snap.Hooks) != 2 {
		t.Fatalf("hooks: got %d, want 2", len(snap.Hooks))
	}
}
```

- [ ] **Step 2: Run the test, expect failure**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestStatusClient -count=1`
Expected: FAIL — `newStatusClient`, `(.snapshot)`, `(.healthOK)` undefined.

- [ ] **Step 3: Implement**

`cmd/otto-tray/status.go`:
```go
//go:build darwin || windows

package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// Snapshot is the subset of /admin/api/snapshot the tray surfaces.
// Field names track the gateway's JSON shape — see
// internal/admin/snapshot.go for the source of truth. The tray uses
// only what it shows in the menu; new fields are ignored.
type Snapshot struct {
	UptimeSeconds int64       `json:"uptime_seconds"`
	Pool          PoolStats   `json:"pool"`
	Hooks         []HookEntry `json:"hooks"`

	// Convenience fields populated by snapshot() from the nested
	// shapes above so the FSM doesn't have to know the JSON layout.
	PoolReady int `json:"-"`
	PoolTotal int `json:"-"`
}

type PoolStats struct {
	Ready int `json:"ready"`
	Total int `json:"total"`
}

type HookEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type statusClient struct {
	baseURL string
	http    *http.Client
}

func newStatusClient(baseURL string, timeout time.Duration) *statusClient {
	return &statusClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *statusClient) healthOK() bool {
	resp, err := c.http.Get(c.baseURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (c *statusClient) snapshot() (Snapshot, error) {
	var snap Snapshot
	resp, err := c.http.Get(c.baseURL + "/admin/api/snapshot")
	if err != nil {
		return snap, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return snap, err
	}
	snap.PoolReady = snap.Pool.Ready
	snap.PoolTotal = snap.Pool.Total
	return snap, nil
}
```

- [ ] **Step 4: Run the test, expect pass**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestStatusClient -count=1`
Expected: PASS for all 4 sub-tests.

- [ ] **Step 5: Note for plan-phase clarification**

Add a TODO inline in the spec's "Open questions" section that the
snapshot JSON shape is assumed (`uptime_seconds`, `pool.ready`,
`pool.total`, `hooks[].status`). At implementation time, verify
against `internal/admin/snapshot.go`. If field names differ, update
this file in the same commit:

```bash
grep -nE 'json:"' internal/admin/snapshot.go | head -20
```
If the field names don't match, fix `status.go` before committing.

- [ ] **Step 6: Commit**

```bash
git add cmd/otto-tray/status.go cmd/otto-tray/status_test.go
git commit -m "$(cat <<'EOF'
feat(tray): HTTP status client for /health and /admin/api/snapshot

Plain net/http, 1s timeout. Field shape mirrors the gateway's
snapshot.go; new fields are ignored. Connection refused → healthOK
false, never a panic. PoolReady/PoolTotal denormalized for the FSM.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Status FSM

**Files:**
- Create: `cmd/otto-tray/fsm.go`
- Test: `cmd/otto-tray/fsm_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/otto-tray/fsm_test.go`:
```go
//go:build darwin || windows

package main

import "testing"

func TestComputeState_StoppedWhenNoPIDAndNoHealth(t *testing.T) {
	got := computeState(stateInput{PIDAlive: false, HealthOK: false})
	if got.State != StateStopped {
		t.Fatalf("no pid, no health → want %s, got %s", StateStopped, got.State)
	}
}

func TestComputeState_RunningWhenPIDAndHealth(t *testing.T) {
	got := computeState(stateInput{
		PIDAlive: true,
		HealthOK: true,
		Snapshot: Snapshot{PoolReady: 4, PoolTotal: 4, Hooks: []HookEntry{{Name: "auth", Status: "ok"}}},
	})
	if got.State != StateRunning {
		t.Fatalf("pid + health → want %s, got %s", StateRunning, got.State)
	}
}

func TestComputeState_DegradedWhenPoolEmpty(t *testing.T) {
	got := computeState(stateInput{
		PIDAlive: true,
		HealthOK: true,
		Snapshot: Snapshot{PoolReady: 0, PoolTotal: 4},
	})
	if got.State != StateDegraded {
		t.Fatalf("pool empty → want %s, got %s", StateDegraded, got.State)
	}
}

func TestComputeState_DegradedWhenHookError(t *testing.T) {
	got := computeState(stateInput{
		PIDAlive: true,
		HealthOK: true,
		Snapshot: Snapshot{
			PoolReady: 4, PoolTotal: 4,
			Hooks: []HookEntry{{Name: "auth", Status: "ok"}, {Name: "pii", Status: "error"}},
		},
	})
	if got.State != StateDegraded {
		t.Fatalf("hook error → want %s, got %s", StateDegraded, got.State)
	}
}

func TestComputeState_StartingWhenPIDButNoHealthYet(t *testing.T) {
	got := computeState(stateInput{
		PIDAlive:        true,
		HealthOK:        false,
		HealthFailures:  1,
		StartingBudget:  true,
	})
	if got.State != StateStarting {
		t.Fatalf("pid alive within budget → want %s, got %s", StateStarting, got.State)
	}
}

func TestComputeState_ErrorAfterThreeHealthFailures(t *testing.T) {
	got := computeState(stateInput{
		PIDAlive:        true,
		HealthOK:        false,
		HealthFailures:  3,
		StartingBudget:  false, // budget expired
	})
	if got.State != StateError {
		t.Fatalf("pid alive, 3 failures, budget expired → want %s, got %s", StateError, got.State)
	}
}
```

- [ ] **Step 2: Run the test, expect failure**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestComputeState -count=1`
Expected: FAIL — `computeState`, `stateInput`, state constants undefined.

- [ ] **Step 3: Implement**

`cmd/otto-tray/fsm.go`:
```go
//go:build darwin || windows

package main

// State is the displayable status of the gateway as seen by the tray.
type State string

const (
	StateUnknown  State = "unknown"
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateDegraded State = "degraded"
	StateError    State = "error"
)

// stateInput is the raw evidence collected per poll. computeState
// is pure — same input always yields same output — so it's trivial
// to unit-test and trivial to reason about. Side effects (logging,
// notifications, UI updates) live in the poller and tray loop.
type stateInput struct {
	PIDAlive       bool
	HealthOK       bool
	HealthFailures int      // consecutive failures while PID is alive
	StartingBudget bool     // true if we're inside the 30s post-start window
	Snapshot       Snapshot // populated only when HealthOK
}

// stateOutput pairs the resolved state with a short human-readable
// detail line for the menu header.
type stateOutput struct {
	State  State
	Detail string
}

func computeState(in stateInput) stateOutput {
	if !in.PIDAlive {
		return stateOutput{State: StateStopped}
	}
	if !in.HealthOK {
		if in.StartingBudget {
			return stateOutput{State: StateStarting, Detail: "warming up"}
		}
		if in.HealthFailures >= 3 {
			return stateOutput{State: StateError, Detail: "/health unreachable"}
		}
		return stateOutput{State: StateStarting, Detail: "warming up"}
	}
	// Health is OK — check for degraded conditions.
	if in.Snapshot.PoolTotal > 0 && in.Snapshot.PoolReady == 0 {
		return stateOutput{State: StateDegraded, Detail: "pool empty"}
	}
	for _, h := range in.Snapshot.Hooks {
		if h.Status != "ok" {
			return stateOutput{State: StateDegraded, Detail: "hook " + h.Name + ": " + h.Status}
		}
	}
	return stateOutput{State: StateRunning}
}
```

- [ ] **Step 4: Run the test, expect pass**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestComputeState -count=1`
Expected: PASS for all 6 sub-tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/otto-tray/fsm.go cmd/otto-tray/fsm_test.go
git commit -m "$(cat <<'EOF'
feat(tray): pure-function FSM mapping liveness+health to display state

Six states (unknown/stopped/starting/running/degraded/error) with
transition rules driven by an explicit stateInput. Side effects (UI,
notifications, logging) stay out of this file so the FSM is trivially
unit-testable and trivially reasoned about.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Poller

**Files:**
- Create: `cmd/otto-tray/poller.go`
- Test: `cmd/otto-tray/poller_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/otto-tray/poller_test.go`:
```go
//go:build darwin || windows

package main

import (
	"context"
	"testing"
	"time"
)

// fakeProbe lets the test drive what the poller "sees" without
// touching the network or the filesystem. It implements the
// probeFunc signature the poller takes.
type fakeProbe struct {
	pidAlive bool
	healthOK bool
	snap     Snapshot
}

func (f *fakeProbe) probe() (bool, bool, Snapshot) { return f.pidAlive, f.healthOK, f.snap }

func TestPoller_EmitsStateOnEachTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe := &fakeProbe{pidAlive: false, healthOK: false}
	tick := make(chan time.Time, 4)
	out := make(chan stateOutput, 4)

	startedAt := time.Now().Add(-1 * time.Hour) // budget already expired
	go runPoller(ctx, probe.probe, tick, out, &startedAt)

	tick <- time.Now()
	select {
	case s := <-out:
		if s.State != StateStopped {
			t.Fatalf("first emit: got %s, want %s", s.State, StateStopped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first emit")
	}

	probe.pidAlive = true
	probe.healthOK = true
	probe.snap = Snapshot{PoolReady: 4, PoolTotal: 4}
	tick <- time.Now()
	select {
	case s := <-out:
		if s.State != StateRunning {
			t.Fatalf("second emit: got %s, want %s", s.State, StateRunning)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second emit")
	}
}

func TestPoller_ExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	probe := &fakeProbe{}
	tick := make(chan time.Time)
	out := make(chan stateOutput, 1)
	startedAt := time.Now()

	done := make(chan struct{})
	go func() {
		runPoller(ctx, probe.probe, tick, out, &startedAt)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not exit on ctx cancel")
	}
}
```

- [ ] **Step 2: Run the test, expect failure**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestPoller -count=1`
Expected: FAIL — `runPoller` undefined.

- [ ] **Step 3: Implement**

`cmd/otto-tray/poller.go`:
```go
//go:build darwin || windows

package main

import (
	"context"
	"time"
)

// probeFunc returns the raw evidence one tick observes: PID alive,
// /health OK, and the snapshot (zero-value when health is not OK).
type probeFunc func() (pidAlive, healthOK bool, snap Snapshot)

// runPoller blocks until ctx is cancelled. Each tick it calls probe,
// composes a stateInput, computes a state, and emits on out. The
// caller (tray.go) owns the ticker and the startedAt pointer so
// tests can drive ticks directly and starts can be tracked across
// the start/stop button presses.
//
// `tick` is a channel rather than a *time.Ticker so tests can
// inject ticks deterministically. Production wiring creates one
// with time.NewTicker(3*time.Second) and forwards its C channel.
func runPoller(ctx context.Context, probe probeFunc, tick <-chan time.Time, out chan<- stateOutput, startedAt *time.Time) {
	consecutiveFailures := 0
	const startingBudget = 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			pidAlive, healthOK, snap := probe()
			if !healthOK && pidAlive {
				consecutiveFailures++
			} else {
				consecutiveFailures = 0
			}
			inBudget := startedAt != nil && !startedAt.IsZero() && time.Since(*startedAt) < startingBudget
			in := stateInput{
				PIDAlive:       pidAlive,
				HealthOK:       healthOK,
				HealthFailures: consecutiveFailures,
				StartingBudget: inBudget,
				Snapshot:       snap,
			}
			s := computeState(in)
			select {
			case out <- s:
			case <-ctx.Done():
				return
			}
		}
	}
}
```

- [ ] **Step 4: Run the test, expect pass**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestPoller -count=1`
Expected: PASS for both sub-tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/otto-tray/poller.go cmd/otto-tray/poller_test.go
git commit -m "$(cat <<'EOF'
feat(tray): goroutine poller with channel-driven ticks for testability

The poller takes its tick source as a channel and its 'started at'
timestamp by pointer so tests can drive ticks deterministically and
the start button can refresh the 30s starting-budget. Probe + FSM
stay pure; the poller is the only place with goroutine state.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Wrapper subprocess runner

**Files:**
- Create: `cmd/otto-tray/runner.go`
- Create: `cmd/otto-tray/runner_darwin.go`
- Create: `cmd/otto-tray/runner_windows.go`
- Test: `cmd/otto-tray/runner_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/otto-tray/runner_test.go`:
```go
//go:build darwin || windows

package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWrapperPath_DarwinUsesShellScript(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only path resolution")
	}
	cmd, args := wrapperCommand("/opt/otto", "start")
	if !strings.HasSuffix(cmd, filepath.Join("scripts", "otto-gw")) {
		t.Fatalf("darwin wrapper: got %q, want suffix scripts/otto-gw", cmd)
	}
	if len(args) != 1 || args[0] != "start" {
		t.Fatalf("darwin args: got %v, want [start]", args)
	}
}

func TestWrapperPath_WindowsUsesPwsh(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only path resolution")
	}
	cmd, args := wrapperCommand("C:\\opt\\otto", "stop")
	if cmd != "pwsh" && cmd != "powershell" {
		t.Fatalf("windows shell: got %q, want pwsh or powershell", cmd)
	}
	if len(args) < 4 || args[len(args)-1] != "stop" {
		t.Fatalf("windows args: got %v, want trailing 'stop'", args)
	}
}
```

- [ ] **Step 2: Run the test, expect failure**

Run: `go test ./cmd/otto-tray -run TestWrapperPath -count=1`
Expected: FAIL — `wrapperCommand` undefined.

- [ ] **Step 3: Implement the runner**

`cmd/otto-tray/runner.go`:
```go
//go:build darwin || windows

package main

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// runResult captures everything the tray needs to surface an error
// in a notification. Empty Stderr + ExitCode 0 = success.
type runResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Err      error
}

// runWrapper invokes the otto-gw wrapper with the given verb
// ("start" / "stop" / "restart"). The subprocess is detached
// (new process group / DETACHED_PROCESS on win) so quitting the
// tray does not signal the gateway. A 30-second timeout matches
// the wrapper's own readiness wait — anything longer is reported
// as a failure to the user.
func runWrapper(installRoot, verb string) runResult {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmdName, args := wrapperCommand(installRoot, verb)
	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Dir = installRoot
	detachProcessGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exit := 0
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	return runResult{
		ExitCode: exit,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Err:      err,
	}
}
```

- [ ] **Step 4: Implement the per-OS wrapper-path resolver**

`cmd/otto-tray/runner_darwin.go`:
```go
//go:build darwin

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

func wrapperCommand(installRoot, verb string) (string, []string) {
	return filepath.Join(installRoot, "scripts", "otto-gw"), []string{verb}
}

func detachProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
```

`cmd/otto-tray/runner_windows.go`:
```go
//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

// Windows constants — kept inline to avoid pulling x/sys for a
// single integer literal each.
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

func wrapperCommand(installRoot, verb string) (string, []string) {
	script := filepath.Join(installRoot, "scripts", "otto-gw.ps1")
	// Prefer pwsh (PowerShell 7+); the .ps1 wrapper supports both.
	// pwsh is on PATH for any Windows install that ships PowerShell
	// 7; older installs fall through to powershell.exe via the .bat
	// shim the project already ships (scripts/otto-gw.bat).
	return "pwsh", []string{"-NoProfile", "-File", script, verb}
}

func detachProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
	}
}
```

- [ ] **Step 5: Run the test, expect pass**

Run: `go test ./cmd/otto-tray -run TestWrapperPath -count=1`
Expected: PASS (only the host's GOOS-matching sub-test runs; the other skips).

- [ ] **Step 6: Verify cross-build still green**

Run: `GOOS=linux GOARCH=amd64 go build ./...`
Expected: succeeds. Linux build skips the whole `cmd/otto-tray` package.

- [ ] **Step 7: Commit**

```bash
git add cmd/otto-tray/runner.go cmd/otto-tray/runner_darwin.go cmd/otto-tray/runner_windows.go cmd/otto-tray/runner_test.go
git commit -m "$(cat <<'EOF'
feat(tray): subprocess runner for the otto-gw wrappers

30-second timeout matches the wrapper's readiness budget. Detached
process group on both OSes so quitting the tray does not signal the
gateway. stdout/stderr captured for surfacing in notifications.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Tray-local config (`~/.otto-gw/tray.json`)

**Files:**
- Create: `cmd/otto-tray/config.go`
- Test: `cmd/otto-tray/config_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/otto-tray/config_test.go`:
```go
//go:build darwin || windows

package main

import (
	"path/filepath"
	"testing"
)

func TestTrayConfig_DefaultsWhenMissing(t *testing.T) {
	cfg, isFirstRun := loadTrayConfig(filepath.Join(t.TempDir(), "tray.json"))
	if !isFirstRun {
		t.Fatalf("missing file should report first run")
	}
	if cfg.LaunchAtLogin || cfg.StartGatewayOnLaunch {
		t.Fatalf("defaults: got %+v, want both false", cfg)
	}
}

func TestTrayConfig_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tray.json")
	in := TrayConfig{LaunchAtLogin: true, StartGatewayOnLaunch: false}
	if err := saveTrayConfig(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, isFirstRun := loadTrayConfig(path)
	if isFirstRun {
		t.Fatalf("after save should not report first run")
	}
	if out != in {
		t.Fatalf("round trip: got %+v, want %+v", out, in)
	}
}

func TestTrayConfig_CorruptFileFallsBackToDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tray.json")
	// Write garbage so json.Decode fails.
	if err := writeFileSimple(path, "{not json"); err != nil {
		t.Fatal(err)
	}
	cfg, isFirstRun := loadTrayConfig(path)
	if isFirstRun {
		// The file existed (even if corrupt) — don't pretend it's a first run.
		t.Fatalf("corrupt file should not be treated as first run")
	}
	if cfg.LaunchAtLogin || cfg.StartGatewayOnLaunch {
		t.Fatalf("corrupt config should default false, got %+v", cfg)
	}
}

func writeFileSimple(path, body string) error {
	return writeFileAtomic(path, []byte(body))
}
```

- [ ] **Step 2: Run the test, expect failure**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestTrayConfig -count=1`
Expected: FAIL — `TrayConfig`, `loadTrayConfig`, `saveTrayConfig`, `writeFileAtomic` undefined.

- [ ] **Step 3: Implement**

`cmd/otto-tray/config.go`:
```go
//go:build darwin || windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// TrayConfig holds tray-only UI preferences. It does NOT shadow
// gateway config — only the two login-item toggles live here.
type TrayConfig struct {
	LaunchAtLogin        bool `json:"launch_tray_at_login"`
	StartGatewayOnLaunch bool `json:"start_gateway_when_tray_launches"`
}

// loadTrayConfig reads ~/.otto-gw/tray.json. Missing file → defaults
// + isFirstRun=true so the caller can fire the onboarding toast.
// Corrupt file → defaults + isFirstRun=false (the user already
// answered the prompt; we just lost their answer).
func loadTrayConfig(path string) (TrayConfig, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TrayConfig{}, true
		}
		return TrayConfig{}, false
	}
	var cfg TrayConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return TrayConfig{}, false
	}
	return cfg, false
}

// saveTrayConfig writes atomically (write tmp + rename) so a crash
// mid-write never leaves a half-encoded JSON file.
func saveTrayConfig(path string, cfg TrayConfig) error {
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, body)
}

// writeFileAtomic writes body to path via a tmp-and-rename. The
// parent directory is created if it does not exist.
func writeFileAtomic(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func trayConfigPath(installRoot string) string {
	return filepath.Join(installRoot, "tray.json")
}
```

- [ ] **Step 4: Run the test, expect pass**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestTrayConfig -count=1`
Expected: PASS for all 3 sub-tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/otto-tray/config.go cmd/otto-tray/config_test.go
git commit -m "$(cat <<'EOF'
feat(tray): tray-local config (tray.json) with atomic writes

Missing file ⇒ first-run sentinel for the onboarding toast.
Corrupt file ⇒ defaults, no first-run prompt (the answer was lost,
not unasked). Atomic write via tmp+rename so a crash mid-write
never leaves a half-encoded file.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Login-item registration on darwin (LaunchAgent)

**Files:**
- Create: `cmd/otto-tray/autostart_darwin.go`
- Test: `cmd/otto-tray/autostart_darwin_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/otto-tray/autostart_darwin_test.go`:
```go
//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAgentPlist_ContainsExecPath(t *testing.T) {
	body := launchAgentPlist("/opt/otto/bin/otto-tray")
	if !strings.Contains(body, "<string>/opt/otto/bin/otto-tray</string>") {
		t.Fatalf("plist missing exec path:\n%s", body)
	}
	if !strings.Contains(body, "<key>RunAtLoad</key>") {
		t.Fatalf("plist missing RunAtLoad key")
	}
	if !strings.Contains(body, "io.cmetech.otto-tray") {
		t.Fatalf("plist missing bundle id")
	}
}

func TestLaunchAgentInstall_WritesAndRemoves(t *testing.T) {
	// Use a tmp HOME to avoid touching the dev's real LaunchAgents.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := installLaunchAgent("/opt/otto/bin/otto-tray", false /* skipLaunchctl */); err != nil {
		t.Fatalf("install: %v", err)
	}
	plistPath := filepath.Join(tmpHome, "Library", "LaunchAgents", "io.cmetech.otto-tray.plist")
	if _, err := os.Stat(plistPath); err != nil {
		t.Fatalf("plist not written: %v", err)
	}

	if err := uninstallLaunchAgent(false /* skipLaunchctl */); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist still exists after uninstall: %v", err)
	}
}
```

- [ ] **Step 2: Run the test, expect failure**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestLaunchAgent -count=1`
Expected: FAIL — `launchAgentPlist`, `installLaunchAgent`, `uninstallLaunchAgent` undefined.

- [ ] **Step 3: Implement**

`cmd/otto-tray/autostart_darwin.go`:
```go
//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const launchAgentLabel = "io.cmetech.otto-tray"

// launchAgentPlist returns the plist body for a per-user LaunchAgent
// that runs otto-tray at login. KeepAlive is intentionally false:
// if the user quits the tray, launchd should not respawn it.
func launchAgentPlist(execPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
    <key>ProcessType</key>
    <string>Interactive</string>
</dict>
</plist>
`, launchAgentLabel, execPath)
}

func launchAgentPlistPath() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", launchAgentLabel+".plist")
}

// installLaunchAgent writes the plist and, unless skipLaunchctl,
// calls `launchctl bootstrap gui/<uid> <plist>`. Test code passes
// skipLaunchctl=true so unit tests can verify file behavior without
// poking the real launchd.
func installLaunchAgent(execPath string, skipLaunchctl bool) error {
	body := launchAgentPlist(execPath)
	path := launchAgentPlistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	if skipLaunchctl {
		return nil
	}
	uid := strconv.Itoa(os.Getuid())
	// Use bootstrap (modern); fall back to load if bootstrap is unavailable.
	if err := exec.Command("launchctl", "bootstrap", "gui/"+uid, path).Run(); err != nil {
		// Fallback: old macOS or already loaded.
		_ = exec.Command("launchctl", "load", path).Run()
	}
	return nil
}

func uninstallLaunchAgent(skipLaunchctl bool) error {
	path := launchAgentPlistPath()
	if !skipLaunchctl {
		uid := strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", "gui/"+uid+"/"+launchAgentLabel).Run()
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
```

- [ ] **Step 4: Run the test, expect pass**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/otto-tray -run TestLaunchAgent -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/otto-tray/autostart_darwin.go cmd/otto-tray/autostart_darwin_test.go
git commit -m "$(cat <<'EOF'
feat(tray): darwin LaunchAgent install/uninstall for login autostart

Per-user plist under ~/Library/LaunchAgents. RunAtLoad=true,
KeepAlive=false so a user-initiated quit is respected. Uses
`launchctl bootstrap` (modern) with `load` fallback. Test path
skips launchctl to keep CI hermetic.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Login-item registration on windows (HKCU Run key)

**Files:**
- Create: `cmd/otto-tray/autostart_windows.go`
- Test: `cmd/otto-tray/autostart_windows_test.go`

- [ ] **Step 1: Add the registry dependency**

Run from the repo root:
```bash
go get golang.org/x/sys/windows/registry@latest
go mod tidy
```
Expected: `go.mod` gains `golang.org/x/sys`. No changes to existing deps.

- [ ] **Step 2: Write the failing test**

`cmd/otto-tray/autostart_windows_test.go`:
```go
//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestRunKey_InstallSetsAndUninstallClears(t *testing.T) {
	// Use a per-test key path so we do not collide with any real
	// registration on the dev box.
	t.Cleanup(func() { _ = uninstallRunKeyForTest("OttoTrayTest") })

	if err := installRunKeyForTest("OttoTrayTest", `C:\opt\otto\bin\otto-tray.exe`); err != nil {
		t.Fatalf("install: %v", err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("open run key: %v", err)
	}
	defer k.Close()
	got, _, err := k.GetStringValue("OttoTrayTest")
	if err != nil {
		t.Fatalf("read value: %v", err)
	}
	if got != `C:\opt\otto\bin\otto-tray.exe` {
		t.Fatalf("value: got %q, want exec path", got)
	}
	if err := uninstallRunKeyForTest("OttoTrayTest"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, _, err := k.GetStringValue("OttoTrayTest"); err == nil {
		t.Fatalf("value still present after uninstall")
	}
}
```

- [ ] **Step 3: Run the test, expect failure**

Run: `GOOS=windows GOARCH=amd64 go test ./cmd/otto-tray -run TestRunKey -count=1`
Expected: FAIL — `installRunKeyForTest`, `uninstallRunKeyForTest`, `runKeyPath` undefined.

Note: this test only executes on a Windows host. On macOS dev boxes, the
build will fail with "undefined" until Step 4; then the build will
succeed but the test will skip-by-tag.

- [ ] **Step 4: Implement**

`cmd/otto-tray/autostart_windows.go`:
```go
//go:build windows

package main

import (
	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath        = `Software\Microsoft\Windows\CurrentVersion\Run`
	runKeyValueName   = "OttoTray"
)

func installRunKey(execPath string) error {
	return installRunKeyForTest(runKeyValueName, execPath)
}

func uninstallRunKey() error {
	return uninstallRunKeyForTest(runKeyValueName)
}

// installRunKeyForTest / uninstallRunKeyForTest expose the value
// name so tests can use a non-production key without monkey-patching
// the const.
func installRunKeyForTest(name, execPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, execPath)
}

func uninstallRunKeyForTest(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}
```

- [ ] **Step 5: Verify the windows build compiles from macOS**

Run: `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/otto-tray`
Expected: compiles. (CGO_ENABLED=0 is temporary — energye/systray needs cgo, but until Task 12 imports it, the build works without.)

- [ ] **Step 6: Commit**

```bash
git add cmd/otto-tray/autostart_windows.go cmd/otto-tray/autostart_windows_test.go go.mod go.sum
git commit -m "$(cat <<'EOF'
feat(tray): windows HKCU Run key for login autostart

Per-user (no admin elevation). golang.org/x/sys/windows/registry
is the standard stdlib-adjacent path. Test uses a separate value
name to avoid colliding with a real install on dev boxes.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Tray menu wiring (UI) + first-run onboarding

This is the first task that brings cgo into the picture. After this
commit, building `cmd/otto-tray` on darwin requires a working clang
toolchain (Xcode CLT) and on windows requires mingw/msvc.

**Files:**
- Create: `cmd/otto-tray/tray.go`
- Create: `cmd/otto-tray/onboarding.go`

- [ ] **Step 1: Add the systray dependency**

Run from the repo root:
```bash
go get github.com/energye/systray@latest
go mod tidy
```
Expected: `go.mod` gains `github.com/energye/systray`.

- [ ] **Step 2: Implement the tray loop**

`cmd/otto-tray/tray.go`:
```go
//go:build darwin || windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/energye/systray"

	"otto-gateway/cmd/otto-tray/icon"
)

// runTray is the main UI loop. It blocks until the user picks Quit
// (systray.Run returns). All UI work happens on systray's main
// thread via menu-item callbacks; the poller runs on its own
// goroutine and forwards stateOutput onto a channel the UI reads
// in a select.
func runTray(installRoot string, cfg TrayConfig, isFirstRun bool) {
	state := newTrayState(installRoot, cfg)
	systray.Run(state.onReady(isFirstRun), state.onExit)
}

type trayState struct {
	mu            sync.Mutex
	installRoot   string
	cfg           TrayConfig
	dashboardURL  string
	statePID      State
	startedAt     time.Time
	pollerCancel  context.CancelFunc
	stateCh       chan stateOutput

	// Menu item handles, populated in onReady.
	miHeader      *systray.MenuItem
	miSubheader   *systray.MenuItem
	miStart       *systray.MenuItem
	miStop        *systray.MenuItem
	miRestart     *systray.MenuItem
	miDashboard   *systray.MenuItem
	miCopyHealth  *systray.MenuItem
	miPrefsLogin  *systray.MenuItem
	miPrefsStart  *systray.MenuItem
	miAbout       *systray.MenuItem
	miQuit        *systray.MenuItem
}

func newTrayState(installRoot string, cfg TrayConfig) *trayState {
	url := resolveDashboardURL(installRoot)
	return &trayState{
		installRoot:  installRoot,
		cfg:          cfg,
		dashboardURL: url,
		statePID:     StateUnknown,
		stateCh:      make(chan stateOutput, 4),
	}
}

func (s *trayState) onReady(isFirstRun bool) func() {
	return func() {
		systray.SetTemplateIcon(icon.Template, icon.Template)
		systray.SetTooltip("OTTO Gateway")

		s.miHeader = systray.AddMenuItem("OTTO Gateway · starting…", "")
		s.miHeader.Disable()
		s.miSubheader = systray.AddMenuItem("", "")
		s.miSubheader.Disable()
		systray.AddSeparator()
		s.miStart = systray.AddMenuItem("Start gateway", "")
		s.miStop = systray.AddMenuItem("Stop gateway", "")
		s.miRestart = systray.AddMenuItem("Restart gateway", "")
		systray.AddSeparator()
		s.miDashboard = systray.AddMenuItem("Open dashboard", s.dashboardURL)
		s.miCopyHealth = systray.AddMenuItem("Copy health URL", "")
		systray.AddSeparator()
		prefs := systray.AddMenuItem("Preferences", "")
		s.miPrefsLogin = prefs.AddSubMenuItemCheckbox("Launch tray at login", "", s.cfg.LaunchAtLogin)
		s.miPrefsStart = prefs.AddSubMenuItemCheckbox("Start gateway when tray launches", "", s.cfg.StartGatewayOnLaunch)
		s.miAbout = systray.AddMenuItem("About OTTO Gateway…", "")
		systray.AddSeparator()
		s.miQuit = systray.AddMenuItem("Quit OTTO Tray", "")

		// Wire callbacks. systray fires each on its main thread; we
		// spawn goroutines for anything that may block (subprocess,
		// HTTP) so the UI never freezes.
		s.miStart.Click(func() { go s.handleStart() })
		s.miStop.Click(func() { go s.handleStop() })
		s.miRestart.Click(func() { go s.handleRestart() })
		s.miDashboard.Click(func() { go openURL(s.dashboardURL) })
		s.miCopyHealth.Click(func() { go copyToClipboard(s.dashboardURL + "/health") })
		s.miPrefsLogin.Click(func() { go s.toggleLaunchAtLogin() })
		s.miPrefsStart.Click(func() { go s.toggleStartGatewayOnLaunch() })
		s.miAbout.Click(func() { go s.showAbout() })
		s.miQuit.Click(func() { systray.Quit() })

		// Start the poller goroutine.
		ctx, cancel := context.WithCancel(context.Background())
		s.pollerCancel = cancel
		startedAt := time.Time{} // not started yet
		probe := s.makeProbe()
		tick := newTicker(3 * time.Second).C
		go runPoller(ctx, probe, tick, s.stateCh, &startedAt)

		// Apply UI updates as state arrives.
		go s.uiLoop()

		// First-run prompt or autostart-gateway, after the icon renders.
		go func() {
			time.Sleep(500 * time.Millisecond)
			if isFirstRun {
				offerFirstRunAutostart(s)
				return
			}
			if s.cfg.StartGatewayOnLaunch {
				s.handleStart()
			}
		}()
	}
}

func (s *trayState) onExit() {
	if s.pollerCancel != nil {
		s.pollerCancel()
	}
}

func (s *trayState) makeProbe() probeFunc {
	pidPath := installRootPIDFile(s.installRoot)
	client := newStatusClient(s.dashboardURL, 1*time.Second)
	return func() (bool, bool, Snapshot) {
		pid, _ := readPIDFile(pidPath)
		alive := pid > 0 && processAlive(pid)
		if !alive {
			return false, false, Snapshot{}
		}
		ok := client.healthOK()
		if !ok {
			return true, false, Snapshot{}
		}
		snap, _ := client.snapshot()
		return true, true, snap
	}
}

func installRootPIDFile(installRoot string) string {
	return installRoot + "/.otto/gw/otto-gateway.pid"
}

func (s *trayState) uiLoop() {
	for out := range s.stateCh {
		s.applyState(out)
	}
}

func (s *trayState) applyState(out stateOutput) {
	s.mu.Lock()
	prev := s.statePID
	s.statePID = out.State
	s.mu.Unlock()

	header := fmt.Sprintf("OTTO Gateway · %s", out.State)
	if out.Detail != "" {
		header += " (" + out.Detail + ")"
	}
	s.miHeader.SetTitle(header)
	s.miSubheader.SetTitle(s.dashboardURL)

	canStart := out.State == StateStopped || out.State == StateError
	if canStart {
		s.miStart.Enable()
		s.miStop.Disable()
		s.miRestart.Disable()
	} else {
		s.miStart.Disable()
		s.miStop.Enable()
		s.miRestart.Enable()
	}

	// Notification on running→{error,stopped} transitions only.
	if prev == StateRunning && (out.State == StateError || out.State == StateStopped) {
		notify("OTTO Gateway", fmt.Sprintf("Gateway is %s", out.State))
	}
}

func (s *trayState) handleStart() {
	s.mu.Lock()
	s.startedAt = time.Now()
	s.mu.Unlock()
	res := runWrapper(s.installRoot, "start")
	if res.ExitCode != 0 || res.Err != nil {
		notify("OTTO Gateway", "Failed to start: "+firstLine(res.Stderr))
	}
}

func (s *trayState) handleStop() {
	res := runWrapper(s.installRoot, "stop")
	if res.ExitCode != 0 || res.Err != nil {
		notify("OTTO Gateway", "Failed to stop: "+firstLine(res.Stderr))
	}
}

func (s *trayState) handleRestart() {
	s.mu.Lock()
	s.startedAt = time.Now()
	s.mu.Unlock()
	res := runWrapper(s.installRoot, "restart")
	if res.ExitCode != 0 || res.Err != nil {
		notify("OTTO Gateway", "Failed to restart: "+firstLine(res.Stderr))
	}
}

func (s *trayState) toggleLaunchAtLogin() {
	s.mu.Lock()
	s.cfg.LaunchAtLogin = !s.cfg.LaunchAtLogin
	cfg := s.cfg
	s.mu.Unlock()
	exe, _ := exeForAutostart()
	var err error
	if cfg.LaunchAtLogin {
		err = installAutostart(exe)
		s.miPrefsLogin.Check()
	} else {
		err = uninstallAutostart()
		s.miPrefsLogin.Uncheck()
	}
	if err != nil {
		slog.Error("autostart toggle failed", "err", err)
		notify("OTTO Gateway", "Could not change login setting: "+err.Error())
		return
	}
	if err := saveTrayConfig(trayConfigPath(s.installRoot), cfg); err != nil {
		slog.Error("save tray.json", "err", err)
	}
}

func (s *trayState) toggleStartGatewayOnLaunch() {
	s.mu.Lock()
	s.cfg.StartGatewayOnLaunch = !s.cfg.StartGatewayOnLaunch
	cfg := s.cfg
	s.mu.Unlock()
	if cfg.StartGatewayOnLaunch {
		s.miPrefsStart.Check()
	} else {
		s.miPrefsStart.Uncheck()
	}
	if err := saveTrayConfig(trayConfigPath(s.installRoot), cfg); err != nil {
		slog.Error("save tray.json", "err", err)
	}
}

func (s *trayState) showAbout() {
	// Simple notification with version + install root + go version.
	body := fmt.Sprintf("OTTO Gateway install: %s\nGo: %s", s.installRoot, runtime.Version())
	notify("About OTTO Gateway", body)
}

// installAutostart / uninstallAutostart / exeForAutostart are tiny
// per-OS shims defined alongside the autostart_*.go files so this
// UI layer stays platform-neutral. They are added as separate
// commits in steps 5 and 6.
```

- [ ] **Step 3: Implement the per-OS UI helpers and shims**

`cmd/otto-tray/uihelpers.go`:
```go
//go:build darwin || windows

package main

import (
	"strings"
	"time"
)

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "(no stderr)"
	}
	return s
}

// newTicker is a thin wrapper that returns a ticker whose C channel
// can be passed to runPoller. Kept for symmetry with the tests
// which pass a hand-driven channel.
func newTicker(d time.Duration) *time.Ticker { return time.NewTicker(d) }
```

`cmd/otto-tray/uihelpers_darwin.go`:
```go
//go:build darwin

package main

import (
	"os"
	"os/exec"
)

func openURL(url string) { _ = exec.Command("open", url).Run() }

func copyToClipboard(s string) {
	cmd := exec.Command("pbcopy")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	_, _ = stdin.Write([]byte(s))
	_ = stdin.Close()
	_ = cmd.Wait()
}

func notify(title, body string) {
	// osascript is a portable, no-extra-deps way to surface a
	// notification on macOS. The user sees a Notification Center
	// banner identical to other CLI apps.
	script := "display notification \"" + escapeApplescript(body) + "\" with title \"" + escapeApplescript(title) + "\""
	_ = exec.Command("osascript", "-e", script).Run()
}

func escapeApplescript(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func exeForAutostart() (string, error) { return os.Executable() }

func installAutostart(exe string) error   { return installLaunchAgent(exe, false) }
func uninstallAutostart() error           { return uninstallLaunchAgent(false) }
```

`cmd/otto-tray/uihelpers_windows.go`:
```go
//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func openURL(url string) {
	// rundll32 url.dll,FileProtocolHandler is the historically-stable
	// way to launch the user's default browser without spawning a
	// console window.
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

func copyToClipboard(s string) {
	// `clip.exe` is preinstalled on every supported Windows release.
	cmd := exec.Command("clip")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	_, _ = stdin.Write([]byte(s))
	_ = stdin.Close()
	_ = cmd.Wait()
}

func notify(title, body string) {
	// PowerShell BurntToast / WinRT is the rich path; a plain
	// MessageBox via msg.exe is the lowest-friction fallback. For
	// v1 we use a small PowerShell one-liner that fires a balloon
	// via Windows.UI.Notifications — falls back silently if the
	// shell does not support it.
	script := "[reflection.assembly]::loadwithpartialname('System.Windows.Forms') | Out-Null; " +
		"$n = New-Object System.Windows.Forms.NotifyIcon; " +
		"$n.Icon = [System.Drawing.SystemIcons]::Information; " +
		"$n.Visible = $true; " +
		"$n.ShowBalloonTip(5000, '" + escapePS(title) + "', '" + escapePS(body) + "', 'Info')"
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

func escapePS(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func exeForAutostart() (string, error) { return os.Executable() }

func installAutostart(exe string) error { return installRunKey(exe) }
func uninstallAutostart() error         { return uninstallRunKey() }
```

- [ ] **Step 4: Implement first-run onboarding**

`cmd/otto-tray/onboarding.go`:
```go
//go:build darwin || windows

package main

// offerFirstRunAutostart fires once on the very first launch (no
// tray.json present). It does NOT prompt synchronously — that
// would require a full GUI window. Instead it shows a notification
// telling the user the toggle exists in Preferences, and writes
// an empty tray.json so we never ask again. The "Not now" vs "Yes"
// choice is deferred to the user's explicit Preferences toggle —
// avoiding a real modal keeps v1 dependency-free.
func offerFirstRunAutostart(s *trayState) {
	notify("OTTO Gateway",
		"Tip: open the menu → Preferences to launch the tray automatically at login.")
	// Persist defaults so we never re-ask. cfg defaults are both false.
	_ = saveTrayConfig(trayConfigPath(s.installRoot), s.cfg)
}
```

- [ ] **Step 5: Build the tray binary**

Run: `go build -o bin/otto-tray ./cmd/otto-tray`
Expected: succeeds on macOS (cgo via Xcode CLT). On a fresh box this is the first time cgo is exercised. If it fails with `xcrun: error: invalid active developer path`, the developer needs `xcode-select --install`; surface this in the README.

- [ ] **Step 6: Verify the gateway build is still cgo-free**

Run: `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/otto-gateway-linux ./cmd/otto-gateway && file /tmp/otto-gateway-linux`
Expected: succeeds, produces a static ELF binary. Confirms the cgo cost stays contained to the tray.

- [ ] **Step 7: Smoke test (manual, macOS dev box)**

```bash
# 1. Ensure a gateway is running so the tray has something to talk to.
./scripts/otto-gw start
# 2. Run the tray.
./bin/otto-tray
```
Expected:
- A menu-bar icon appears (template image — adapts to dark/light bar).
- Header reads "OTTO Gateway · running"; sub-header shows http://127.0.0.1:18080.
- Start is disabled; Stop and Restart are enabled.
- "Open dashboard" opens `/admin` in the default browser.
- "Copy health URL" puts `http://127.0.0.1:18080/health` on the clipboard (`pbpaste` to verify).
- "Stop gateway" stops the gateway; within ~3s the header turns red/grey and Start re-enables.
- "Quit OTTO Tray" exits cleanly.

If any of these fail, fix in this commit before moving on.

- [ ] **Step 8: Commit**

```bash
git add cmd/otto-tray/tray.go cmd/otto-tray/uihelpers.go cmd/otto-tray/uihelpers_darwin.go cmd/otto-tray/uihelpers_windows.go cmd/otto-tray/onboarding.go go.mod go.sum
git commit -m "$(cat <<'EOF'
feat(tray): tray menu UI, poller wiring, first-run onboarding

energye/systray with template icon, six-state status FSM, debounced
notifications on running→{error,stopped}. Per-OS helpers for browser
open, clipboard copy, and toast notification (osascript on darwin,
powershell on windows) so there are no extra runtime deps. First-run
toast points the user at Preferences instead of a modal prompt — keeps
v1 dependency-free and the install footprint clean.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: `main.go` entry point + `--uninstall` flag

**Files:**
- Modify: `cmd/otto-tray/main.go` (replace stub)
- Create: `cmd/otto-tray/uninstall.go`

- [ ] **Step 1: Replace the stub main.go**

`cmd/otto-tray/main.go`:
```go
//go:build darwin || windows

package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	uninstall := flag.Bool("uninstall", false, "remove login-item registration (LaunchAgent on darwin, Run key on windows) and exit")
	flag.Parse()

	installRoot, err := resolveInstallRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "otto-tray:", err)
		os.Exit(1)
	}

	if *uninstall {
		if err := runUninstall(installRoot); err != nil {
			fmt.Fprintln(os.Stderr, "otto-tray --uninstall:", err)
			os.Exit(1)
		}
		fmt.Println("otto-tray: login-item removed (binary not deleted)")
		return
	}

	cfg, isFirstRun := loadTrayConfig(trayConfigPath(installRoot))
	runTray(installRoot, cfg, isFirstRun)
}
```

- [ ] **Step 2: Create the uninstall helper**

`cmd/otto-tray/uninstall.go`:
```go
//go:build darwin || windows

package main

// runUninstall removes the login-item registration. It does NOT
// delete the binary or the tray.json — the user's package manager
// or `rm -rf ~/.otto-gw` handles that. Idempotent: succeeds even
// if no registration exists.
func runUninstall(installRoot string) error {
	if err := uninstallAutostart(); err != nil {
		return err
	}
	return nil
}
```

- [ ] **Step 3: Verify the build and flag**

Run: `go build -o bin/otto-tray ./cmd/otto-tray && ./bin/otto-tray --uninstall`
Expected: exits 0 with "login-item removed" line. Running it twice should still exit 0.

- [ ] **Step 4: Commit**

```bash
git add cmd/otto-tray/main.go cmd/otto-tray/uninstall.go
git commit -m "$(cat <<'EOF'
feat(tray): main.go entry + --uninstall flag

--uninstall removes only the login-item (LaunchAgent / Run key);
binary deletion remains the user's job, matching the no-installer
distribution model. Idempotent.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 14: Makefile — cross-compile + package additions

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Inspect the current cross-compile targets**

Run: `grep -nE 'cross-(darwin|windows)-(arm64|amd64):' Makefile`
Expected: shows the existing 4 targets for `otto-gateway`. We will mirror their style for `otto-tray`.

- [ ] **Step 2: Add tray cross-compile targets**

Insert after the existing `cross-windows-amd64:` block. Add `cgo` env handling — Go cross-compile from macOS to windows needs `CC=x86_64-w64-mingw32-gcc` (mingw-w64). The Makefile will document the prerequisite at the top of the new section.

Add this block to `Makefile` immediately after the line `cross-windows-amd64:` block ends (find with `grep -n 'cross-windows-amd64' Makefile`):

```makefile

# ---------------------------------------------------------------------------
# otto-tray cross-compile (darwin + windows ONLY).
#
# Prerequisites:
#   * darwin builds: Xcode Command Line Tools (`xcode-select --install`).
#   * windows builds from macOS: `brew install mingw-w64` and the
#     CC=x86_64-w64-mingw32-gcc setting below. Without mingw, this target
#     fails with a clear cgo link error and the gateway's own build is
#     unaffected.
# ---------------------------------------------------------------------------

TRAY_BINARY := otto-tray
TRAY_PKG    := ./cmd/$(TRAY_BINARY)

cross-otto-tray-darwin-arm64:
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
	    go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(TRAY_BINARY)-darwin-arm64 $(TRAY_PKG)

cross-otto-tray-darwin-amd64:
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 \
	    go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(TRAY_BINARY)-darwin-amd64 $(TRAY_PKG)

cross-otto-tray-windows-amd64:
	@mkdir -p $(BUILD_DIR)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=1 \
	    CC=$${CC:-x86_64-w64-mingw32-gcc} \
	    go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(TRAY_BINARY)-windows-amd64.exe $(TRAY_PKG)

cross-otto-tray: cross-otto-tray-darwin-arm64 cross-otto-tray-darwin-amd64 cross-otto-tray-windows-amd64 ## Cross-compile otto-tray for darwin + windows
```

- [ ] **Step 3: Wire the tray binary into the package targets**

Find each `package-darwin-arm64:`, `package-darwin-amd64:`, `package-windows-amd64:` target. Each currently depends on its `cross-*` counterpart. Add the corresponding `cross-otto-tray-*` as a dependency and copy the binary into the tarball staging directory.

The Makefile already uses a staging pattern (`docs/operator-quickstart.md` is renamed; `scripts/` is shipped wholesale). Add to each of the three package targets:

For `package-darwin-arm64:` (mirror the same edits for darwin-amd64 and windows-amd64):

```makefile
package-darwin-arm64: cross-darwin-arm64 cross-otto-tray-darwin-arm64 $(PKG_README) $(PKG_INSTALL) ## Build otto_gateway-darwin-arm64-<version>.tar.gz
```

And inside the recipe (after the `cp` of `otto-gateway`):
```makefile
	cp $(BUILD_DIR)/$(TRAY_BINARY)-darwin-arm64 $(STAGING)/otto_gateway/bin/$(TRAY_BINARY)
```

Identify the exact recipe by reading the Makefile section first:
```bash
sed -n '170,230p' Makefile
```
Adapt the `cp` line to the existing variable names (`$(STAGING)`, `$(DIST_NAME)`, or whatever the project already uses).

Apply the same shape to `package-darwin-amd64` and `package-windows-amd64`. **Skip `package-linux-amd64`** — the linux tarball intentionally does not include the tray.

- [ ] **Step 4: Verify the host-platform package build**

Run (from a macOS dev box with mingw NOT installed — we only test darwin here):
```bash
make package-darwin-arm64 VERSION=v0.0.0-tray-test
ls dist/
tar -tzf dist/otto_gateway-darwin-arm64-v0.0.0-tray-test.tar.gz | grep otto-tray
```
Expected: tarball contains `otto_gateway/bin/otto-tray`. Cleanup:
```bash
rm dist/otto_gateway-darwin-arm64-v0.0.0-tray-test.tar.gz
```

- [ ] **Step 5: Commit**

```bash
git add Makefile
git commit -m "$(cat <<'EOF'
chore(tray): Makefile cross-compile + package targets for otto-tray

Adds cross-otto-tray-{darwin-arm64,darwin-amd64,windows-amd64} mirroring
the gateway's cross targets. Wires the tray binary into the three
non-linux package-* targets. Windows cross-build documents the mingw
prerequisite at the top of the new section.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 15: install.sh + install.ps1 footer + quarantine strip

**Files:**
- Modify: `scripts/install.sh`
- Modify: `scripts/install.ps1`

- [ ] **Step 1: Add darwin quarantine strip in install.sh**

Open `scripts/install.sh` and find the existing darwin quarantine block (around line 120 in the file you read earlier). It currently strips quarantine from `otto-gateway` only. Extend it:

```sh
    if [ "$PLATFORM_OS" = "darwin" ]; then
        xattr -d com.apple.quarantine "$OTTO_HOME/bin/otto-gateway" 2>/dev/null || true
        # otto-tray ships only in the darwin and windows tarballs;
        # silently no-op if the binary is absent.
        if [ -f "$OTTO_HOME/bin/otto-tray" ]; then
            xattr -d com.apple.quarantine "$OTTO_HOME/bin/otto-tray" 2>/dev/null || true
        fi
    fi
```

- [ ] **Step 2: Add a footer line in install.sh**

Find the "Next steps:" printf block. Add one line between `status` and the curl-health check:

```sh
    printf '\nNext steps:\n'
    printf '  %s start            # launch the gateway from the terminal\n' "$cmd"
    printf '  %s status           # verify it is up\n' "$cmd"
    # Tray is shipped only on darwin/linux installs of this script;
    # we already gate by PLATFORM_OS above. Linux skip handles itself
    # because the tarball does not ship the binary.
    if [ -f "$OTTO_HOME/bin/otto-tray" ]; then
        printf '  open %s/bin/otto-tray   # or, launch the menu-bar app\n' "$OTTO_HOME"
    fi
    printf '  curl -sf http://127.0.0.1:18080/health\n'
```

- [ ] **Step 3: Add a footer line in install.ps1**

Open `scripts/install.ps1` and find the "Next steps" block (search for `Next steps`). Add:

```powershell
Write-Host ""
Write-Host "Next steps:"
Write-Host "  $cmd start            # launch the gateway from a terminal"
Write-Host "  $cmd status           # verify it is up"
if (Test-Path "$OttoHome\bin\otto-tray.exe") {
    Write-Host "  Start-Process `"$OttoHome\bin\otto-tray.exe`"   # or, launch the tray app"
}
Write-Host "  curl -sf http://127.0.0.1:18080/health"
```

(The exact local variable name may be `$OTTO_HOME`, `$OttoHome`, or `$env:OTTO_HOME` — adapt to what the script already uses. Find it with `grep -nE '\$OTTO|\$Otto' scripts/install.ps1`.)

- [ ] **Step 4: Dry-run the install scripts (syntax check only)**

```bash
sh -n scripts/install.sh && echo install.sh OK
pwsh -NoProfile -Command "Get-Content scripts/install.ps1 -Raw | Out-Null; 'install.ps1 OK'"
```
Expected: both print OK. On a macOS dev box without `pwsh`, skip the second line and rely on CI to catch syntax errors.

- [ ] **Step 5: Commit**

```bash
git add scripts/install.sh scripts/install.ps1
git commit -m "$(cat <<'EOF'
feat(install): drop quarantine on otto-tray + footer line in installers

darwin: extends the existing xattr -d block to cover bin/otto-tray
when present. Linux installs no-op silently — the binary isn't
shipped in that tarball.

Both installers gain a "launch the tray app" line in the Next-Steps
footer, gated on the binary existing. The one-line curl install
remains one line; no login-item changes.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 16: Docs — operator-quickstart + README mention + uninstall guidance

**Files:**
- Modify: `docs/operator-quickstart.md`
- Modify: `README.md`

- [ ] **Step 1: Add a Tray section to operator-quickstart.md**

Locate the existing "Starting / stopping" section (search the file for `start` headings). Add a new section after it:

```markdown
## Launching the menu-bar / system-tray app (macOS + Windows)

The install drops `bin/otto-tray` (alongside `bin/otto-gateway`) on
macOS and Windows. The tray app is **optional** — every operation it
exposes is also available via `otto-gw [.ps1]` on the command line.

To launch it:

- **macOS:** `open ~/.otto-gw/bin/otto-tray`
- **Windows:** double-click `%USERPROFILE%\.otto-gw\bin\otto-tray.exe`,
  or `Start-Process "$env:USERPROFILE\.otto-gw\bin\otto-tray.exe"`.

The icon appears in the menu bar (macOS) or system tray (Windows).
From its menu you can:

- Start, stop, or restart the gateway (drives the same `otto-gw` wrapper
  as the CLI — same env files, same config).
- Open the `/admin` dashboard in your default browser.
- Copy the health-check URL to the clipboard.
- Toggle "Launch tray at login" — when on, the tray re-appears on every
  login. Off by default; the installer never touches this.
- Toggle "Start gateway when tray launches" — when on, the gateway
  starts automatically a moment after the icon appears.

### Removing login-item registration

If you toggled "Launch tray at login" on and later want to remove the
LaunchAgent (macOS) or `HKCU\Run` value (Windows), run:

```sh
~/.otto-gw/bin/otto-tray --uninstall
```

This removes only the login-item registration — the binary itself
stays put (delete it with the rest of `~/.otto-gw/` whenever you wish).
```

- [ ] **Step 2: Add a one-line mention to README.md**

Find the "Installation" or "Quick start" heading and add a single sentence after the install one-liner:

```markdown
After install, macOS and Windows users can also launch a menu-bar /
system-tray app: `open ~/.otto-gw/bin/otto-tray` (or
`Start-Process "$env:USERPROFILE\.otto-gw\bin\otto-tray.exe"` on
Windows). See [docs/operator-quickstart.md](docs/operator-quickstart.md)
for what it does and how to remove its login-item registration.
```

- [ ] **Step 3: Commit**

```bash
git add docs/operator-quickstart.md README.md
git commit -m "$(cat <<'EOF'
docs: document otto-tray launch, autostart toggles, and --uninstall

Adds an "optional tray app" section to the operator quickstart with
launch commands per OS, the four menu actions, both autostart
toggles, and the --uninstall flag for removing login-item state.
README gains a one-line mention pointing into the new section.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Final check

- [ ] **Step 1: Full repo test + cross-build smoke**

```bash
go test ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/otto-gateway   # gateway still cgo-free
go build -o bin/otto-tray ./cmd/otto-tray                            # tray on host
```
Expected: all tests pass; both builds succeed.

- [ ] **Step 2: Verify the gateway tarball was not changed**

```bash
make package-linux-amd64 VERSION=v0.0.0-final-check
tar -tzf dist/otto_gateway-linux-amd64-v0.0.0-final-check.tar.gz | grep -c otto-tray
rm dist/otto_gateway-linux-amd64-v0.0.0-final-check.tar.gz
```
Expected: count = 0 (tray must NOT ship in the linux tarball).

- [ ] **Step 3: Manual end-to-end (macOS)**

1. `make package-darwin-arm64 VERSION=v0.0.0-smoke`.
2. In a scratch dir: extract the tarball, run `bin/otto-tray`.
3. Click Start, see header turn green within ~30s.
4. Click Open Dashboard, verify `/admin` loads.
5. Toggle "Launch tray at login" on; check `~/Library/LaunchAgents/io.cmetech.otto-tray.plist` exists.
6. Run `bin/otto-tray --uninstall`; verify the plist is gone.
7. Click Quit.
8. Verify gateway is still running (`./scripts/otto-gw status`) — the tray quit must not have signalled it.

If any of these fail, the failure points at an issue earlier in the
plan; do not paper over it here.

---

## Self-review

**Spec coverage check (against `docs/superpowers/specs/2026-06-06-tray-menubar-launcher-design.md`):**

- Architecture (separate binary, energye/systray, drives wrappers) → Tasks 1, 8, 12.
- Menu structure (header, Start/Stop/Restart, Open dashboard, Copy health URL, Preferences, About, Quit) → Task 12.
- Status FSM (6 states, 3s poll, debounced notifications) → Tasks 5, 6, 7, 12.
- Subprocess contract (otto-gw via shell on darwin, pwsh on windows, 30s timeout, detached pgrp) → Task 8.
- Port discovery via dotenv precedence → Task 3.
- Tray-local config (tray.json, both toggles off by default) → Task 9.
- Autostart (LaunchAgent / HKCU Run key) → Tasks 10, 11.
- Onboarding toast on first run → Task 12, Step 4.
- `--uninstall` flag → Task 13.
- Packaging (cross targets, package-* additions, no linux tray, quarantine strip) → Tasks 14, 15.
- Documentation → Task 16.

**Placeholder scan:** No "TBD", no "implement later", no "add error handling as appropriate." The only deferral phrases in the plan are in the spec's explicit "Open questions" carry-overs (Step 5 of Task 5 calls out the snapshot JSON shape verification; Step 3 of Task 14 calls out the `$(STAGING)` variable adaptation), both of which are concrete actions the implementer takes at that step.

**Type consistency check:**

- `Snapshot` struct (Task 5) — used in `stateInput.Snapshot` (Task 6), `probeFunc` return (Task 7), `tray.makeProbe` (Task 12). All consistent.
- `State` constants — defined Task 6, consumed Tasks 7, 12.
- `runResult` — defined Task 8, consumed Task 12 (`runWrapper` returns it; the tray reads `ExitCode`, `Stderr`).
- `TrayConfig` — defined Task 9, consumed Tasks 12, 13.
- `installAutostart` / `uninstallAutostart` / `exeForAutostart` — declared via per-OS shims in Task 12 (uihelpers_darwin.go / uihelpers_windows.go), call into Task 10 (`installLaunchAgent`) / Task 11 (`installRunKey`). Names match.
- `processAlive` and `readPIDFile` from Task 4 are called only in Task 12's probe. Consistent signatures.

No drift detected. Plan ready for execution.

---

**Plan complete and saved to `docs/superpowers/plans/2026-06-06-tray-menubar-launcher.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
