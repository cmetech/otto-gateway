# Gateway De-brand + `.gw` Config Home + Install Relayout — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebrand "OTTO Gateway" → "Gateway" across UI/tray/docs, rename binaries/wrapper, move the config home to `~/.gw`, split installed code from user data, and auto-migrate existing `~/.otto-gw` installs.

**Architecture:** Introduce a two-anchor path model — `GW_HOME` (user data `~/.gw`) and `GW_INSTALL_DIR` (code dir per-OS) — and rewire every path-resolution seam (tray Go, bash/pwsh wrappers, installers) to it. De-brand is a coordinated string/identifier rename that must land atomically with the relayout; a shared, idempotent migration bridges old installs.

**Tech Stack:** Go 1.26 (tray + gateway), bash + PowerShell (wrappers/installers), embedded HTML/CSS/JS (admin UI), Make (packaging). No new deps; no cgo.

## Global Constraints

- Go toolchain **1.26.5** (`go.mod`); no cgo in the main binaries; trivial cross-compile.
- **Keep unchanged:** repo `cmetech/otto-gateway`, `go.mod` module path `otto-gateway`, release artifact names `otto_gateway-<os>-<arch>.tar.gz|zip` + `otto_gateway/` extract prefix, install one-liner URLs.
- **Never rename (not OTTO-branded):** API model id `auto`, owner `kiro`; env vars `KIRO_CMD KIRO_ARGS KIRO_CWD POOL_SIZE SESSION_TTL_MS AUTH_TOKEN ALLOWED_IPS DEBUG EMBEDDING_MODEL_DEFAULT HTTP_BODY_READ_TIMEOUT_SEC`; Anthropic SSE event names; `X-Request-Id`.
- **Rename map:** `otto-gateway`→`gateway` (binary), `otto-tray`→`gateway-tray` (binary), `otto-gw`→`gw` (wrapper/PATH cmd), `~/.otto-gw*`→`~/.gw`, `OTTO_*`→`GW_*`, `OTTO_ADMIN_CONFIG`→`GW_ADMIN_CONFIG`, CSS `--otto-*`→`--gw-*` / `.otto-*`→`.gw-*` / `otto-theme`→`gw-theme`, autostart `io.cmetech.otto-tray`→`io.cmetech.gateway-tray` + Run-key `OttoTray`→`GatewayTray`, `OTTO Tray.app`→`Gateway Tray.app`. Tray "OTTO Desktop" → `<brandDisplayName> Desktop` (brand-aware).
- **Install dirs:** config `GW_HOME=~/.gw` (all OSes; holds `.env`, `overrides.env`, `tray.json`, `logs/`, `state/gateway.pid`, `.config-error`, `support/`). Code `GW_INSTALL_DIR`: Windows `%LOCALAPPDATA%\Gateway`, macOS `~/Library/Application Support/Gateway`, Linux `${XDG_DATA_HOME:-~/.local/share}/gateway`.
- Tray package files are `//go:build darwin || windows`; CI test-race runs on Linux (tray not built there) — gate the tray on darwin `go test` + `GOOS=windows`/`GOOS=linux` build/vet + `gofumpt`/`golangci-lint`.
- TDD, atomic commits, one deliverable per task. Trailers on every commit: `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>` / `Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2`.

---

## Phase A — Two-anchor path resolution (tray Go core)

### Task A1: `GW_HOME` + `GW_INSTALL_DIR` resolvers

**Files:**
- Create: `cmd/otto-tray/paths.go` (`//go:build darwin || windows`)
- Create: `cmd/otto-tray/paths_test.go`
- Reference (to replace later): `cmd/otto-tray/installroot.go`

**Interfaces:**
- Produces: `resolveGWHome(env func(string) string, home string) string`; `resolveInstallDirFrom(execPath string) (string, error)`; `gwPidFile(gwHome string) string`; `gwSentinel(gwHome string) string`; `gwEnvPath(gwHome string) string`; `gwOverridesPath(gwHome string) string`; `gwTrayConfigPath(gwHome string) string`.

- [ ] **Step 1: Write the failing test**

```go
//go:build darwin || windows

package main

import (
	"path/filepath"
	"testing"
)

func TestResolveGWHome(t *testing.T) {
	home := "/home/u"
	// env override wins
	env := func(k string) string {
		if k == "GW_HOME" {
			return "/custom/gw"
		}
		return ""
	}
	if got := resolveGWHome(env, home); got != "/custom/gw" {
		t.Errorf("override: got %q", got)
	}
	// default → ~/.gw
	if got := resolveGWHome(func(string) string { return "" }, home); got != filepath.Join(home, ".gw") {
		t.Errorf("default: got %q", got)
	}
}

func TestGWSubPaths(t *testing.T) {
	gw := filepath.Join("/home/u", ".gw")
	if gwPidFile(gw) != filepath.Join(gw, "state", "gateway.pid") {
		t.Errorf("pid: %q", gwPidFile(gw))
	}
	if gwSentinel(gw) != filepath.Join(gw, ".config-error") {
		t.Errorf("sentinel: %q", gwSentinel(gw))
	}
	if gwEnvPath(gw) != filepath.Join(gw, ".env") {
		t.Errorf("env: %q", gwEnvPath(gw))
	}
	if gwTrayConfigPath(gw) != filepath.Join(gw, "tray.json") {
		t.Errorf("tray.json: %q", gwTrayConfigPath(gw))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/otto-tray/ -run 'TestResolveGWHome|TestGWSubPaths' -v`
Expected: FAIL — `undefined: resolveGWHome` etc.

- [ ] **Step 3: Write minimal implementation**

```go
//go:build darwin || windows

package main

import (
	"path/filepath"
	"strings"
)

// resolveGWHome returns the user-data config home: env GW_HOME → ~/.gw.
func resolveGWHome(env func(string) string, home string) string {
	if v := strings.TrimSpace(env("GW_HOME")); v != "" {
		return v
	}
	return filepath.Join(home, ".gw")
}

func gwPidFile(gwHome string) string       { return filepath.Join(gwHome, "state", "gateway.pid") }
func gwSentinel(gwHome string) string      { return filepath.Join(gwHome, ".config-error") }
func gwEnvPath(gwHome string) string       { return filepath.Join(gwHome, ".env") }
func gwOverridesPath(gwHome string) string { return filepath.Join(gwHome, "overrides.env") }
func gwTrayConfigPath(gwHome string) string { return filepath.Join(gwHome, "tray.json") }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/otto-tray/ -run 'TestResolveGWHome|TestGWSubPaths' -v`
Expected: PASS

- [ ] **Step 5: Add install-dir resolver test + impl**

Append to `paths_test.go`:

```go
func TestResolveInstallDirFrom(t *testing.T) {
	// bin/ walk-up: <dir>/bin/gateway-tray → <dir>
	got, err := resolveInstallDirFrom(filepath.Join("/opt/Gateway", "bin", "gateway-tray"))
	if err != nil || got != "/opt/Gateway" {
		t.Errorf("bin walk-up: got %q err %v", got, err)
	}
}
```

Append to `paths.go` (port `resolveInstallRootFrom` from `installroot.go`, retarget the `.app` name to `Gateway Tray.app`):

```go
import "os"

// resolveInstallDirFrom returns the code install dir given the tray executable
// path. Walks up from bin/; steps over the macOS "Gateway Tray.app" bundle.
func resolveInstallDirFrom(execPath string) (string, error) {
	resolved, err := os.EvalSymlinks(execPath)
	if err != nil {
		resolved = execPath
	}
	binDir := filepath.Dir(resolved)
	// macOS app bundle: …/Gateway Tray.app/Contents/MacOS/gateway-tray
	if filepath.Base(binDir) == "MacOS" {
		contents := filepath.Dir(binDir)
		if filepath.Base(contents) == "Contents" {
			appDir := filepath.Dir(contents)
			if strings.HasSuffix(appDir, ".app") {
				return filepath.Dir(appDir), nil
			}
		}
	}
	return filepath.Dir(binDir), nil
}
```

(Note: `import "os"` and `"strings"` — merge into one import block.)

- [ ] **Step 6: Run tests, gofumpt, vet**

Run: `go test ./cmd/otto-tray/ -run 'Resolve|GWSub' -v && go run mvdan.cc/gofumpt@latest -l cmd/otto-tray/paths.go cmd/otto-tray/paths_test.go && go vet ./cmd/otto-tray/...`
Expected: PASS, empty gofumpt, vet clean.

- [ ] **Step 7: Commit**

```bash
git add cmd/otto-tray/paths.go cmd/otto-tray/paths_test.go
git commit -m "feat(tray): GW_HOME + GW_INSTALL_DIR two-anchor path resolvers"
```

### Task A2: Migrate tray call sites onto the two anchors

**Files:**
- Modify: `cmd/otto-tray/tray.go` (struct field `installRoot`→`installDir`+`gwHome`; pidfile, config, sentinel, wrapper invocation, open-folder)
- Modify: `cmd/otto-tray/config.go:77-79` (`trayConfigPath` → `gwTrayConfigPath(gwHome)`)
- Modify: `cmd/otto-tray/dotenv.go:70-89` (read `$GW_HOME/.env` + `$GW_HOME/overrides.env`)
- Modify: `cmd/otto-tray/poller.go:35` (sentinel → `gwSentinel(gwHome)`)
- Modify: `cmd/otto-tray/openfolder.go:101` (gateway folder → `gwHome`)
- Modify: `cmd/otto-tray/runner_darwin.go:15` / `runner_windows.go:32` (`scripts/gw` under `installDir`)
- Modify: `cmd/otto-tray/main.go:30` (load tray config from `gwHome`)
- Delete: `cmd/otto-tray/installroot.go` (+ `installroot_test.go`) after callers migrated
- Test: `cmd/otto-tray/paths_test.go` (add dotenv path assertion)

**Interfaces:**
- Consumes: A1 resolvers.
- Produces: `trayState.installDir string`, `trayState.gwHome string`.

- [ ] **Step 1: Write failing test for dotenv lookup path**

Add to `paths_test.go`:

```go
func TestResolveDashboardURLUsesGWHome(t *testing.T) {
	// lookupHTTPAddr must read $GW_HOME/.env, not installDir.
	// (Adjust to the extracted pure helper introduced in Step 3.)
	got := gwEnvPath(filepath.Join("/home/u", ".gw"))
	if got != filepath.Join("/home/u", ".gw", ".env") {
		t.Fatalf("env path: %q", got)
	}
}
```

- [ ] **Step 2: Run — verify current dotenv reads the wrong path**

Run: `go test ./cmd/otto-tray/ -run TestResolveDashboardURLUsesGWHome -v`
Expected: PASS on the helper, but confirm by inspection that `dotenv.go:70-89` still references `.otto-gw.overrides.env`/`.env.otto-gw` under installRoot (to be fixed Step 3).

- [ ] **Step 3: Rewire call sites**

In `tray.go`: replace the `installRoot` field with `installDir` and `gwHome`. In `newTrayState`/`onReady`, set:

```go
home, _ := os.UserHomeDir()
exe, _ := os.Executable()
installDir, _ := resolveInstallDirFrom(exe)
s.installDir = installDir
s.gwHome = resolveGWHome(os.Getenv, home)
```

Then:
- pidfile (`tray.go:226`): `gwPidFile(s.gwHome)` (replaces `installRootPIDFile(...)` / `filepath.Join(installRoot, ".otto", "gw", "otto-gateway.pid")`).
- `config.go:77-79`: `gwTrayConfigPath(gwHome)`.
- `dotenv.go:70-89`: read `gwOverridesPath(gwHome)` then `gwEnvPath(gwHome)`.
- `poller.go:35`: `gwSentinel(gwHome)`.
- `openfolder.go:101` `handleOpenGatewayFolder`: `dir := s.gwHome` (was `filepath.Join(homeDir(), ".otto-gw")`).
- `runner_darwin.go:15`: `filepath.Join(installDir, "scripts", "gw")`; `runner_windows.go:32`: `...\scripts\gw.ps1`; `runner.go:34` `cmd.Dir = s.gwHome`.

- [ ] **Step 4: Delete `installroot.go` + its test; build**

Run: `git rm cmd/otto-tray/installroot.go cmd/otto-tray/installroot_test.go && go build ./cmd/otto-tray/... && GOOS=windows go build ./cmd/otto-tray/...`
Expected: builds clean (no remaining references to `resolveInstallRoot`).

- [ ] **Step 5: Test + lint**

Run: `go test ./cmd/otto-tray/... && go run mvdan.cc/gofumpt@latest -l cmd/otto-tray/ && go vet ./cmd/otto-tray/... && GOOS=windows go vet ./cmd/otto-tray/...`
Expected: PASS / clean.

- [ ] **Step 6: Commit**

```bash
git add -A cmd/otto-tray/
git commit -m "refactor(tray): rewire all path seams onto GW_HOME + GW_INSTALL_DIR"
```

---

## Phase B — Tray de-brand, binary/app rename, autostart

### Task B1: Tray UI strings "OTTO Gateway" → "Gateway"

**Files:**
- Modify: `cmd/otto-tray/tray.go` (L95,98,127,246,302,355,362,370,443,496,522), `tooltip.go:9`, `onboarding.go:16-17`, `main.go`/`main_other.go` stderr prefixes.

**Interfaces:** none new (string edits).

- [ ] **Step 1: Replace visible strings**

Change every user-visible `"OTTO Gateway"` → `"Gateway"`, tooltip `"OTTO Gateway · %s"` → `"Gateway · %s"`, About title `"About OTTO Gateway"` → `"About Gateway"`, quit `"Quit OTTO Tray"` → `"Quit Gateway Tray"`, onboarding body `"Launch OTTO Tray…"` → `"Launch Gateway Tray…"`, CLI prefixes `otto-tray:` → `gateway-tray:`. Leave the desktop-section labels for B2.

Run to find them: `grep -rn "OTTO Gateway\|OTTO Tray\|otto-tray:" cmd/otto-tray/*.go`

- [ ] **Step 2: Build + darwin test**

Run: `go build ./cmd/otto-tray/... && go test ./cmd/otto-tray/... && GOOS=windows go build ./cmd/otto-tray/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/otto-tray/
git commit -m "feat(tray): de-brand user-visible strings OTTO Gateway -> Gateway"
```

### Task B2: Brand-aware "OTTO Desktop" labels

**Files:**
- Modify: `cmd/otto-tray/desktoptray.go` (L47,53,59,65,74,75,83,86,104,109,114,115,129) and `tray.go:110-114` menu titles.
- Test: `cmd/otto-tray/desktoptray_test.go`

**Interfaces:**
- Consumes: `brandIdentity` / `resolveDesktopIdentity` (existing).
- Produces: `desktopLabel(id brandIdentity, suffix string) string` → `"<DisplayName> Desktop <suffix>"`.

- [ ] **Step 1: Write failing test**

```go
func TestDesktopLabel(t *testing.T) {
	if got := desktopLabel(brandIdentity{DisplayName: "LOOP24"}, "· running"); got != "LOOP24 Desktop · running" {
		t.Errorf("got %q", got)
	}
	if got := desktopLabel(defaultBrandIdentity(), ""); got != "OTTO Desktop" {
		t.Errorf("default: got %q", got)
	}
}
```

- [ ] **Step 2: Run — fails (undefined `desktopLabel`)**

Run: `go test ./cmd/otto-tray/ -run TestDesktopLabel -v` → FAIL.

- [ ] **Step 3: Implement + apply**

```go
import "strings"

func desktopLabel(id brandIdentity, suffix string) string {
	s := id.DisplayName + " Desktop"
	if suffix != "" {
		s += " " + suffix
	}
	return strings.TrimSpace(s)
}
```

Apply in `applyDesktopState`/menu builders: header `SetTitle(desktopLabel(id, "· running"))` etc.; static menu items ("Install OTTO Desktop…", "Start OTTO Desktop", "Stop OTTO Desktop", confirm/notify strings) use `desktopLabel(id, ...)`. Where the menu item is built once at startup, use the resolved `did` identity (as done for the icon/data-folder label).

- [ ] **Step 4: Run test + build**

Run: `go test ./cmd/otto-tray/ -run TestDesktopLabel -v && go build ./cmd/otto-tray/... && GOOS=windows go build ./cmd/otto-tray/...` → PASS/clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/otto-tray/
git commit -m "feat(tray): brand-aware desktop section labels (<DisplayName> Desktop)"
```

### Task B3: Rename tray binary/app, pidfile matchers, autostart keys

**Files:**
- Modify: `cmd/otto-tray/pidfile_darwin.go:28-55` + `pidfile_windows.go:41-55` (match `gateway`/`gateway.exe`)
- Modify: `cmd/otto-tray/autostart_darwin.go:15` (`launchAgentLabel = "io.cmetech.gateway-tray"`), `autostart_windows.go:14` (`runKeyValueName = "GatewayTray"`)
- Modify: `cmd/otto-tray/brand.go` desktop identity comment only if needed (do NOT change desktop brand defaults).
- Test: `cmd/otto-tray/pidfile_test.go` (update expected process name)

- [ ] **Step 1: Update pidfile-matcher test**

In `pidfile_test.go`, change expected matched basename from `otto-gateway` to `gateway` (darwin) / `gateway.exe` (windows) in the relevant cases.

- [ ] **Step 2: Run — fails**

Run: `go test ./cmd/otto-tray/ -run 'Pidfile|Pid' -v` → FAIL.

- [ ] **Step 3: Implement**

In `pidfile_darwin.go`/`pidfile_windows.go`, change the process-name match constant from `otto-gateway`/`otto-gateway.exe` to `gateway`/`gateway.exe`. In `autostart_*.go`, set the new label/run-key values.

- [ ] **Step 4: Run tests + builds**

Run: `go test ./cmd/otto-tray/... && GOOS=windows go build ./cmd/otto-tray/...` → PASS/clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/otto-tray/
git commit -m "feat(tray): rename tray binary/app + pidfile match + autostart keys (gateway-tray)"
```

---

## Phase C — Admin dashboard de-brand

### Task C1: Templates + assets + admin doc strings

**Files:**
- Modify: `internal/admin/templates/base.html.tmpl` (L6 title, L12 wordmark, L7/23 `gw-theme`), `about.html.tmpl:3`, `docs.html.tmpl` (L4,124,207), `assets.go` (CSS `--otto-*`→`--gw-*`, `.otto-*`→`.gw-*`, `OTTO_ADMIN_CONFIG`→`GW_ADMIN_CONFIG`, wordmark), `admin.go` (L521 default path, L533-534 `gw init`).

**Interfaces:** the admin page's producer (`assets.go`/templates) and consumer JS must use the same `GW_ADMIN_CONFIG` / `.gw-*` names.

- [ ] **Step 1: Sweep CSS/JS/HTML identifiers (consistent rename)**

Run (verify each hunk):
```bash
grep -rln "OTTO Gateway\|--otto-\|\botto-\(admin\|header\|card\|slot-grid\|badge\|bg\)\|OTTO_ADMIN_CONFIG\|otto-theme\|otto-gw init" internal/admin/
```
Apply: `--otto-`→`--gw-`, `.otto-`/class `otto-`→`gw-` (CSS + template class attrs together), `OTTO_ADMIN_CONFIG`→`GW_ADMIN_CONFIG` (both producer + JS consumer), `otto-theme`→`gw-theme`, `OTTO Gateway`→`Gateway`, `otto-gw init`→`gw init`, `CHAT_TRACE_FILE` default `otto-gateway-chat-trace.log`→`gateway-chat-trace.log`.

- [ ] **Step 2: Build the gateway (embeds regenerate)**

Run: `go build ./internal/admin/... && go build ./cmd/otto-gateway/...`
Expected: builds clean.

- [ ] **Step 3: Commit**

```bash
git add internal/admin/
git commit -m "feat(admin): de-brand dashboard UI (Gateway; --gw-*; GW_ADMIN_CONFIG)"
```

### Task C2: Update admin tests in lockstep

**Files:**
- Modify: `internal/admin/handlers_test.go` (L61-191 asserts `"OTTO Gateway"`, `OTTO_ADMIN_CONFIG`, `--otto-bg`, `otto-slot-grid`).

- [ ] **Step 1: Update assertions to new names**

`"OTTO Gateway"`→`"Gateway"`, `OTTO_ADMIN_CONFIG`→`GW_ADMIN_CONFIG`, `--otto-bg`→`--gw-bg`, `otto-slot-grid`→`gw-slot-grid`.

- [ ] **Step 2: Run admin tests**

Run: `go test ./internal/admin/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/admin/handlers_test.go
git commit -m "test(admin): update de-brand assertions to Gateway / --gw-* / GW_ADMIN_CONFIG"
```

---

## Phase D — Wrappers (`scripts/`)

### Task D1: `gw` bash wrapper (two-anchor + rename)

**Files:**
- Rename: `scripts/otto-gw` → `scripts/gw` (`git mv`)
- Modify: the renamed `scripts/gw` (install-dir walk-up; `GW_*` vars; `.env` search; init dest; sentinel; start/stop/log/pid paths; support dir)
- Rename: `scripts/.env.otto-gw.example` → `scripts/.env.example`
- Test: `tests/install/smoke_posix.sh` (updated in D3)

- [ ] **Step 1: `git mv` + rename identifiers**

```bash
git mv scripts/otto-gw scripts/gw
git mv scripts/.env.otto-gw.example scripts/.env.example
```
In `scripts/gw`: rename all `OTTO_*`→`GW_*`; set `GW_INSTALL_DIR="$(cd -P "$GW_SCRIPT_DIR/.." && pwd)"`, `GW_BIN="${GW_BIN:-$GW_INSTALL_DIR/bin/gateway}"`; `GW_HOME="${GW_HOME:-$HOME/.gw}"`; `GW_STATE_DIR="${GW_STATE_DIR:-$GW_HOME/state}"`; `GW_PID="${GW_PID:-$GW_STATE_DIR/gateway.pid}"`; `GW_LOG="${GW_LOG:-$GW_HOME/logs/gateway.log}"`; `GW_LOG_BOOT="${GW_LOG_BOOT:-${GW_LOG%.log}-boot.log}"`; `DEFAULT_ENV_PATHS=("./.env" "$GW_HOME/.env")`; `DEFAULT_OVERRIDES_PATHS=("./overrides.env" "$GW_HOME/overrides.env")`; template `$GW_SCRIPT_DIR/.env.example`; `init` default dest `$GW_HOME/.env`; sentinel `$HOME/.gw/.config-error`; support dir `$GW_HOME/support`; upgrade log `$GW_HOME/upgrade.log`. Leave `KIRO_*`, `AUTH_TOKEN`, `HTTP_ADDR`, `auto`/`kiro` untouched.

- [ ] **Step 2: Shellcheck + syntax**

Run: `bash -n scripts/gw && shellcheck scripts/gw`
Expected: no errors.

- [ ] **Step 3: Smoke `gw init` to a temp HOME**

Run: `HOME=$(mktemp -d) GW_INSTALL_DIR=$PWD/scripts/.. bash scripts/gw init --non-interactive && ls "$HOME/.gw/.env"` (adjust GW_INSTALL_DIR so the template resolves).
Expected: `.env` created under the temp `~/.gw`.

- [ ] **Step 4: Commit**

```bash
git add scripts/gw scripts/.env.example
git commit -m "feat(scripts): rename otto-gw -> gw with GW_HOME/GW_INSTALL_DIR two-anchor layout"
```

### Task D2: `gw.ps1` + `.bat` shims

**Files:**
- Rename: `scripts/otto-gw.ps1`→`scripts/gw.ps1`, `scripts/otto-gw.bat`→`scripts/gw.bat`
- Modify: `scripts/gw.ps1` (mirror D1 split), `scripts/gw.bat` (call `gw.ps1`), `scripts/start.bat`/`stop.bat`/`status.bat` (call `gw.bat`)

- [ ] **Step 1: `git mv` + mirror the two-anchor split**

```bash
git mv scripts/otto-gw.ps1 scripts/gw.ps1
git mv scripts/otto-gw.bat scripts/gw.bat
```
In `gw.ps1`: `$InstallDir = Split-Path -Parent $PSScriptRoot`; `$BinPath = Join-Path $InstallDir 'bin\gateway.exe'`; `$GwHome = if ($env:GW_HOME) { $env:GW_HOME } else { Join-Path $env:USERPROFILE '.gw' }`; `$StateDir = Join-Path $GwHome 'state'`; `$PidFile = "$StateDir\gateway.pid"`; `$LogFile = Join-Path $GwHome 'logs\gateway.log'`; `$DefaultEnvPaths = @(".\.env", "$GwHome\.env")`; sentinel `Join-Path $GwHome '.config-error'`; init dest `$GwHome\.env`. `gw.bat:8` → `...-File "%~dp0gw.ps1" %*`; `start/stop/status.bat` → `@call "%~dp0gw.bat" start|stop|status %*`.

- [ ] **Step 2: PowerShell parse check (if pwsh available) + grep leftover OTTO**

Run: `grep -n "OTTO_\|otto-gw\|\.otto-gw" scripts/gw.ps1 scripts/*.bat || echo "clean"`
Expected: `clean` (no stray old identifiers).

- [ ] **Step 3: Commit**

```bash
git add scripts/gw.ps1 scripts/gw.bat scripts/start.bat scripts/stop.bat scripts/status.bat
git commit -m "feat(scripts): rename ps1/bat wrappers to gw with two-anchor layout"
```

---

## Phase E — Migration + installers

### Task E1: Shared migration helper (bash + pwsh) with tests

**Files:**
- Create: `scripts/lib/migrate.sh` (`gw_migrate_from_otto()`), `scripts/lib/migrate.ps1` (`Invoke-GwMigration`)
- Create: `tests/install/migrate_posix_test.sh`

**Interfaces:**
- Produces: `gw_migrate_from_otto` (bash) / `Invoke-GwMigration` (pwsh): idempotent; move `~/.otto-gw.env`→`~/.gw/.env`, `~/.otto-gw.overrides.env`→`~/.gw/overrides.env`, `~/.otto-gw/tray.json`→`~/.gw/tray.json`; leave old code dir; log moves.

- [ ] **Step 1: Write failing bash test**

```bash
#!/usr/bin/env bash
set -euo pipefail
TMP=$(mktemp -d); export HOME="$TMP"
printf 'AUTH_TOKEN=secret\n' > "$HOME/.otto-gw.env"
source "$(dirname "$0")/../../scripts/lib/migrate.sh"
gw_migrate_from_otto
grep -q 'AUTH_TOKEN=secret' "$HOME/.gw/.env" || { echo FAIL: env not migrated; exit 1; }
# idempotent: second run must not error or clobber
gw_migrate_from_otto
grep -q 'AUTH_TOKEN=secret' "$HOME/.gw/.env" || { echo FAIL: idempotency; exit 1; }
echo PASS
```

- [ ] **Step 2: Run — fails (no migrate.sh)**

Run: `bash tests/install/migrate_posix_test.sh` → FAIL (file not found).

- [ ] **Step 3: Implement `scripts/lib/migrate.sh`**

```bash
# gw_migrate_from_otto: one-time, idempotent migration of legacy ~/.otto-gw config
# into ~/.gw. Moves config only; never touches the old code dir. Safe to re-run.
gw_migrate_from_otto() {
	local gw="$HOME/.gw"
	# already migrated?
	[ -f "$gw/.env" ] && return 0
	local legacy_env=""
	if   [ -f "$HOME/.otto-gw.env" ];            then legacy_env="$HOME/.otto-gw.env"
	elif [ -f "$HOME/.otto-gw/.env.otto-gw" ];   then legacy_env="$HOME/.otto-gw/.env.otto-gw"
	fi
	[ -z "$legacy_env" ] && return 0   # nothing to migrate
	mkdir -p "$gw"
	mv "$legacy_env" "$gw/.env" && echo "migrated $legacy_env -> $gw/.env"
	[ -f "$HOME/.otto-gw.overrides.env" ] && mv "$HOME/.otto-gw.overrides.env" "$gw/overrides.env"
	[ -f "$HOME/.otto-gw/tray.json" ]     && mv "$HOME/.otto-gw/tray.json" "$gw/tray.json"
	return 0
}
```

- [ ] **Step 4: Run test — passes**

Run: `bash tests/install/migrate_posix_test.sh` → `PASS`.

- [ ] **Step 5: Implement `scripts/lib/migrate.ps1` (mirror)**

```powershell
function Invoke-GwMigration {
	$gw = Join-Path $env:USERPROFILE '.gw'
	if (Test-Path (Join-Path $gw '.env')) { return }
	$legacy = Join-Path $env:USERPROFILE '.otto-gw.env'
	if (-not (Test-Path $legacy)) { return }
	New-Item -ItemType Directory -Force -Path $gw | Out-Null
	Move-Item $legacy (Join-Path $gw '.env')
	$ov = Join-Path $env:USERPROFILE '.otto-gw.overrides.env'
	if (Test-Path $ov) { Move-Item $ov (Join-Path $gw 'overrides.env') }
}
```

- [ ] **Step 6: shellcheck + commit**

Run: `shellcheck scripts/lib/migrate.sh && bash -n scripts/lib/migrate.sh`

```bash
git add scripts/lib/migrate.sh scripts/lib/migrate.ps1 tests/install/migrate_posix_test.sh
git commit -m "feat(install): idempotent ~/.otto-gw -> ~/.gw config migration helper"
```

### Task E2: `install.sh` — relayout + migration + autostart swap

**Files:**
- Modify: `scripts/install.sh` (install-dir per-OS; extract there; config to `~/.gw`; PATH symlink `gw`; `Gateway Tray.app`; source `lib/migrate.sh`; remove old autostart)

- [ ] **Step 1: Compute the two dirs + source migration**

Set `GW_HOME="${GW_HOME:-$HOME/.gw}"`; `GW_INSTALL_DIR` per-OS: macOS `"$HOME/Library/Application Support/Gateway"`, Linux `"${XDG_DATA_HOME:-$HOME/.local/share}/gateway"`. `source "$(dirname "$0")/lib/migrate.sh" 2>/dev/null || true` then `gw_migrate_from_otto` (also try sourcing from the extracted `$GW_INSTALL_DIR/scripts/lib/migrate.sh`).

- [ ] **Step 2: Extract to code dir; config in ~/.gw**

`tar ... -C "$GW_INSTALL_DIR" --strip-components=1` (yields `$GW_INSTALL_DIR/bin/gateway`, `.../bin/gateway-tray`, `.../scripts/gw`). Config-existence check: if no `$GW_HOME/.env`, run `"$GW_INSTALL_DIR/scripts/gw" init --non-interactive`. Build `$GW_INSTALL_DIR/Gateway Tray.app/...` from `$GW_INSTALL_DIR/bin/gateway-tray`, bundle id `io.cmetech.gateway-tray`.

- [ ] **Step 3: PATH symlink + old-autostart cleanup**

`ln -sf "$GW_INSTALL_DIR/scripts/gw" "$HOME/.local/bin/gw"`; remove legacy `~/.local/bin/otto-gw` and legacy LaunchAgent `~/Library/LaunchAgents/io.cmetech.otto-tray.plist` (unload+unlink) if present.

- [ ] **Step 4: shellcheck + dry idempotency (grep no leftover OTTO paths)**

Run: `shellcheck scripts/install.sh && grep -n "\.otto-gw\|OTTO_\|otto-gw" scripts/install.sh || echo clean`
Expected: `clean`.

- [ ] **Step 5: Commit**

```bash
git add scripts/install.sh
git commit -m "feat(install): install.sh relayout to GW_INSTALL_DIR + ~/.gw config + migration"
```

### Task E3: `install.ps1` — relayout + migration + autostart swap

**Files:**
- Modify: `scripts/install.ps1`

- [ ] **Step 1: Two dirs + migration**

`$GwHome = if ($env:GW_HOME) { $env:GW_HOME } else { Join-Path $env:USERPROFILE '.gw' }`; `$InstallDir = if ($env:GW_INSTALL_DIR) { $env:GW_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Gateway' }`. Dot-source `lib\migrate.ps1` and call `Invoke-GwMigration` (before config check).

- [ ] **Step 2: Extract to InstallDir; config in ~/.gw; PATH; Run-key**

`Copy-Item (Join-Path $inner '*') -Destination $InstallDir`. If no `$GwHome\.env`, `& (Join-Path $InstallDir 'scripts\gw.bat') init -NonInteractive`. Append `$InstallDir\scripts` to User PATH; remove the old `.otto-gw\scripts` PATH entry if present. Remove legacy `OttoTray` Run-key if present.

- [ ] **Step 3: grep leftover OTTO**

Run: `grep -n "\.otto-gw\|OTTO_\|otto-gw\|OttoTray" scripts/install.ps1 || echo clean`
Expected: `clean`.

- [ ] **Step 4: Commit**

```bash
git add scripts/install.ps1
git commit -m "feat(install): install.ps1 relayout to %LOCALAPPDATA%\\Gateway + ~/.gw + migration"
```

---

## Phase F — Packaging + docs

### Task F1: Makefile binary rename + staging + tray asset embed

**Files:**
- Modify: `Makefile` (`BINARY := gateway`, `TRAY_BINARY := gateway-tray`; stage `bin/gateway[.exe]`, `bin/gateway-tray[.exe]`, `scripts/gw*`, `scripts/.env.example`, `scripts/lib/migrate.*`; keep `otto_gateway/` prefix + artifact names + `SHA256SUMS-*`)
- Modify: pidfile matcher already handled (B3); ldflags `-X otto-gateway/internal/version.Version` unchanged.

- [ ] **Step 1: Rename binary vars + staged filenames**

Set `BINARY := gateway`, `TRAY_BINARY := gateway-tray`. Update `stage_unix`/windows staging to copy `bin/gateway[.exe]`, `bin/gateway-tray[.exe]`, `scripts/gw`, `scripts/gw.ps1`, `scripts/gw.bat`, `scripts/start.bat`/`stop.bat`/`status.bat`, `scripts/.env.example`, `scripts/lib/redact.*`, `scripts/lib/migrate.*`. Keep the top-level `otto_gateway/` staging dir and archive names.

- [ ] **Step 2: Build all targets + verify archive contents**

Run: `make cross && make package-darwin-arm64 && tar tzf dist/otto_gateway-darwin-arm64-*.tar.gz | grep -E 'bin/gateway$|bin/gateway-tray$|scripts/gw$'`
Expected: the three entries present; archive still named `otto_gateway-darwin-arm64-*.tar.gz`.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "build: rename binaries gateway/gateway-tray; ship gw wrappers + migrate lib (artifact names kept)"
```

### Task F2: Docs de-brand + layout/migration notes

**Files:**
- Modify: `README.md`, `docs/INSTALL.md`, `docs/operator-quickstart.md`, `docs/operating.md`, `docs/README.md`, `DEVELOPERS.md`, `CLAUDE.md`.

- [ ] **Step 1: Sweep docs**

Replace `OTTO Gateway`→`Gateway`, `~/.otto-gw`→`~/.gw`, `.otto-gw.env`/`.env.otto-gw`→`.gw/.env`, `otto-gw <cmd>`→`gw <cmd>`, `OTTO Tray.app`→`Gateway Tray.app`; document the new split layout (code in `%LOCALAPPDATA%\Gateway` / `~/Library/Application Support/Gateway` / `~/.local/share/gateway`, config in `~/.gw`) and the auto-migration behavior. Leave `docs/superpowers/**` historical specs untouched. Keep install one-liner URLs (repo unchanged).

- [ ] **Step 2: Link/consistency check**

Run: `grep -rn "otto-gw\|\.otto-gw\|OTTO Gateway" README.md docs/INSTALL.md docs/operator-quickstart.md docs/operating.md DEVELOPERS.md | grep -v superpowers || echo clean`
Expected: `clean`.

- [ ] **Step 3: Commit**

```bash
git add README.md docs/ DEVELOPERS.md CLAUDE.md
git commit -m "docs: de-brand to Gateway; document ~/.gw + code/config split + migration"
```

---

## Phase G — Full-suite verification

### Task G1: Repo-wide gates + leftover-brand sweep

- [ ] **Step 1: Build/test/lint everything**

Run:
```bash
go build ./... && \
go test ./... && \
go run mvdan.cc/gofumpt@latest -l . && \
go vet ./... && \
GOOS=windows go build ./cmd/otto-tray/... && GOOS=windows go vet ./cmd/otto-tray/... && \
GOOS=linux go build ./... && \
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./cmd/otto-tray/... ./internal/admin/...
```
Expected: all green; gofumpt empty; golangci-lint 0 issues.

- [ ] **Step 2: Leftover-brand sweep (excluding kept identity + historical docs)**

Run:
```bash
grep -rniE "otto[- ]?gw|\.otto-gw|OTTO Gateway|OTTO_|--otto-|OttoTray|otto-tray" \
  --include=*.go --include=*.sh --include=*.ps1 --include=*.bat --include=*.tmpl --include=*.md . \
  | grep -vE "docs/superpowers|\.planning|module otto-gateway|cmetech/otto-gateway|otto_gateway-|otto_gateway/|internal/version" \
  || echo "clean"
```
Expected: `clean` (only the intentionally-kept repo/module/artifact/URL references remain — verify each survivor is one of those).

- [ ] **Step 3: Verify kept-contract untouched**

Run: `grep -rn '"auto"\|"kiro"\|KIRO_CMD\|AUTH_TOKEN' internal/adapter internal/config | head` — confirm these are present and unchanged.

- [ ] **Step 4: Commit any final fixups**

```bash
git add -A && git commit -m "chore: final de-brand sweep + full-suite green" || echo "nothing to commit"
```

---

## Self-Review notes (author)

- **Spec coverage:** §2 map → A/B/C/D/E/F tasks; §3 layout → E2/E3/F1; §4 seams → A1/A2 + D1/D2; §5a admin → C1/C2; §5b tray → B1/B2/B3 + A2; §5c wrappers → D1/D2; §5d installers → E2/E3; §5f Makefile → F1; §6 migration → E1 (+E2/E3 wiring); §7 testing → per-task + G1; §8 risks → atomic land via single branch, migration guarded/tested.
- **Kept-list** (auto/kiro/KIRO_*/AUTH_TOKEN/repo/module/artifacts) explicitly excluded and asserted in G1.
- **Execution note:** land the whole plan on ONE branch and merge together (atomic) — do not ship a half-renamed state.
