# Gateway De-brand + `.gw` Config Home + Install Relayout — Design (Spec 1)

**Date:** 2026-07-14
**Component:** OTTO Gateway repo (`otto-gateway`) — gateway binary, tray, wrappers, installers, docs.
**Status:** Design (awaiting review)
**Follow-on:** Spec 2 (graphical wizard installer) — deferred, depends on this.

## 1. Goal & scope

Remove user-facing "OTTO" branding (product name becomes **"Gateway"**), rename
the config home `~/.otto-gw` → **`~/.gw`**, and **split installed code from user
config**: binaries/scripts install into a per-user code dir (Windows
`%LOCALAPPDATA%\Gateway`, macOS `~/Library/Application Support/Gateway`, Linux
`~/.local/share/gateway`); **only user data lives in `~/.gw`**. Existing
`~/.otto-gw` installs **auto-migrate** on first run. Modeled on hermes-agent's
code-vs-data split, but keeping the gateway's single-static-Go-binary/no-cgo
constraint (the wizard, which would add Tauri/Rust, is Spec 2).

### In scope
- De-brand admin dashboard UI, tray UI, and docs: "OTTO Gateway" → "Gateway".
- Rename binaries `otto-gateway`→`gateway`, `otto-tray`→`gateway-tray`; wrapper/PATH
  command `otto-gw`→`gw`.
- Config home `~/.otto-gw*` → `~/.gw`; `.env.otto-gw` family → `~/.gw/.env`.
- Env vars `OTTO_*`→`GW_*`; admin JS global `OTTO_ADMIN_CONFIG`→`GW_ADMIN_CONFIG`.
- Autostart keys `io.cmetech.otto-tray`→`io.cmetech.gateway-tray`, Run-key
  `OttoTray`→`GatewayTray` (with old-key removal on migrate).
- Split install layout + rewrite all path-resolution seams (two anchors:
  `GW_HOME` = config, `GW_INSTALL_DIR` = code).
- Auto-migration from `~/.otto-gw`.
- Tray "OTTO Desktop" section → **brand-aware** (`<DisplayName> Desktop`).

### Out of scope (kept as-is)
- Repo `cmetech/otto-gateway`, `go.mod` module path `otto-gateway`, release artifact
  names `otto_gateway-<os>-<arch>.tar.gz|zip` and their `otto_gateway/` extract
  prefix, and the install one-liner URLs. (Renaming these breaks installs/CI/imports.)
- API model ids `auto` / owner `kiro` (OpenAI `/v1/models`, Ollama `/api/tags`).
- Node-compat env vars: `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `POOL_SIZE`,
  `SESSION_TTL_MS`, `AUTH_TOKEN`, `ALLOWED_IPS`, `DEBUG`, `EMBEDDING_MODEL_DEFAULT`,
  `HTTP_BODY_READ_TIMEOUT_SEC`.
- Anthropic SSE event names; `X-Request-Id`.
- The graphical wizard (Spec 2).

## 2. Naming map

| From | To | Notes |
|---|---|---|
| "OTTO Gateway" (admin UI title/wordmark/docs, tray labels/tooltip/About/notifications) | **Gateway** | user-visible strings |
| binary `otto-gateway` / `otto-tray` | `gateway` / `gateway-tray` | Makefile `BINARY`/`TRAY_BINARY`, pidfile matchers, wrapper `GW_BIN`, `.app` MacOS exe |
| wrapper + PATH cmd `otto-gw` (`.bat`/`.ps1`) | `gw` | `gw start/stop/status/init` |
| `~/.otto-gw` (dir), `~/.otto-gw.env`, `~/.otto-gw.overrides.env`, `~/.otto-gw.upgrade.log` | `~/.gw/` dir; `~/.gw/.env`, `~/.gw/overrides.env`, `~/.gw/upgrade.log` | config home |
| `.env.otto-gw`, `.otto-gw.overrides.env`, `.env.otto-gw.example` | `.env`, `overrides.env`, `.env.example` (under `~/.gw` / cwd) | |
| state dir `<root>/.otto/gw`, pid `otto-gateway.pid` | `~/.gw/state/`, pid `gateway.pid` | pid moves into `~/.gw` |
| `.config-error` sentinel at `$HOME/.otto-gw/.config-error` | `$HOME/.gw/.config-error` | wrapper + tray must agree |
| env vars `OTTO_HOME/OTTO_INSTALL_ROOT/OTTO_STATE_DIR/OTTO_BIN/OTTO_PID/OTTO_LOG/OTTO_ADDR/OTTO_API_URL/OTTO_BASE_URL/OTTO_VERSION/OTTO_KIRO_BIN/OTTO_LOG_BOOT` | `GW_HOME` (config), `GW_INSTALL_DIR` (code), `GW_STATE_DIR`, `GW_BIN`, `GW_PID`, `GW_LOG`, `GW_ADDR`, `GW_API_URL`, `GW_BASE_URL`, `GW_VERSION`, `GW_KIRO_BIN`, `GW_LOG_BOOT` | rename installer+wrapper+tray **atomically**; `OTTO_HOME` splits into `GW_HOME`+`GW_INSTALL_DIR` |
| admin JS global `OTTO_ADMIN_CONFIG` | `GW_ADMIN_CONFIG` | producer (`assets.go`/templates) + consumer JS together |
| CSS `--otto-*` vars, `.otto-*` classes, `otto-theme` storage key | `--gw-*`, `.gw-*`, `gw-theme` | cosmetic; rename consistently across `assets.go` + 4 `*.html.tmpl` + tests |
| autostart `io.cmetech.otto-tray` (LaunchAgent), Run-key `OttoTray` | `io.cmetech.gateway-tray`, `GatewayTray` | remove old on migrate |
| `OTTO Tray.app` | `Gateway Tray.app` | mac bundle |
| tray "OTTO Desktop …" labels | `<brandDisplayName> Desktop …` | **brand-aware**, not "Gateway" (separate product) |
| **KEPT:** repo/module/artifacts/URLs, `auto`/`kiro`, `KIRO_*`/`AUTH_TOKEN` | — | see §1 out-of-scope |

## 3. Install layout (code split from config)

**Config home `GW_HOME` = `~/.gw`** (all OSes; precious, survives upgrades). Holds:
`.env`, `overrides.env`, `tray.json`, `logs/`, `state/gateway.pid`, `.config-error`,
`support/`, `upgrade.log`.

**Code dir `GW_INSTALL_DIR`** (replaceable on upgrade). Holds: `bin/gateway[.exe]`,
`bin/gateway-tray[.exe]`, `scripts/` (wrappers + `lib/`), `README.md`, `INSTALL.md`,
and macOS `Gateway Tray.app`. Per OS:
- **Windows:** `%LOCALAPPDATA%\Gateway`
- **macOS:** `~/Library/Application Support/Gateway`
- **Linux:** `${XDG_DATA_HOME:-~/.local/share}/gateway`

The release tarball keeps its `otto_gateway/` prefix and artifact name; the
installer now extracts `otto_gateway/{bin,scripts,*.md}` into `GW_INSTALL_DIR`
(not a single co-located root) and generates config into `~/.gw`.

## 4. Path-resolution model (two anchors)

Replaces today's single `installRoot`. Every seam resolves against one of:
- **`GW_HOME`** — env `GW_HOME` → default `~/.gw`. Config, logs, pid, state,
  sentinel, support, tray.json.
- **`GW_INSTALL_DIR`** — env `GW_INSTALL_DIR` → derived from the running
  executable/script location (walk up from `bin/` or `scripts/`; the macOS `.app`
  step-over now targets `Gateway Tray.app` under Application Support). Binaries +
  wrapper scripts only.

The 11 seams from the audit, rewired:
1. bash wrapper `scripts/gw` — resolve `GW_INSTALL_DIR` from script dir; `GW_BIN=$GW_INSTALL_DIR/bin/gateway`; `GW_HOME` from env→`~/.gw`; `GW_STATE_DIR=$GW_HOME/state`; `GW_PID=$GW_STATE_DIR/gateway.pid`; `GW_LOG=$GW_HOME/logs/gateway.log`.
2. pwsh wrapper `scripts/gw.ps1` — same split.
3. tray `installroot.go` → **`resolveInstallDir` + `resolveGWHome`** (two functions). `.app` step-over updated for the new bundle path.
4. tray config lookup `dotenv.go` — read `$GW_HOME/.env` + `$GW_HOME/overrides.env` (unifies today's inconsistent `<installRoot>/.env.otto-gw` vs `~/.otto-gw.env`).
5. sentinel — wrapper + tray both `$GW_HOME/.config-error`.
6. tray pidfile `tray.go` — `$GW_HOME/state/gateway.pid` (matches wrapper).
7. tray→wrapper invocation `runner_*.go` — `$GW_INSTALL_DIR/scripts/gw[.ps1]`, `cmd.Dir = GW_HOME` (so relative logs/state resolve in the data home).
8. `handleOpenGatewayFolder` + menu label → `$GW_HOME` (`~/.gw`).
9. macOS `.app` placement (installer) + tray step-over (both new location).
10. installer extraction targets + PATH wiring + config-existence checks.
11. Makefile packaging (bin names) — see §5f.

## 5. Component changes

### 5a. Admin dashboard UI (`internal/admin/`)
- `templates/base.html.tmpl` (title, `.gw-header-brand` wordmark, `gw-theme` key),
  `about.html.tmpl` (wordmark), `docs.html.tmpl` (prose) — "OTTO Gateway"→"Gateway".
- `assets.go` embedded CSS/JS: `--otto-*`→`--gw-*`, `.otto-*`→`.gw-*`, `OTTO_ADMIN_CONFIG`→`GW_ADMIN_CONFIG`, wordmark string. Rename consistently (templates + CSS are coupled by class name).
- `admin.go` env-doc strings (`CHAT_TRACE_FILE` default path, `otto-gw init`→`gw init`).
- `handlers_test.go` asserts (`"OTTO Gateway"`, `OTTO_ADMIN_CONFIG`, `--otto-bg`, `otto-slot-grid`) — update in lockstep.

### 5b. Tray (`cmd/otto-tray/`)
- All "OTTO Gateway" strings (`tray.go`, `tooltip.go`, `onboarding.go`) → "Gateway".
- `brand.go` — the tray's OWN identity strings are "Gateway"; keep the *desktop-app* brandIdentity (OTTO/LOOP24 via brand.json) for the desktop section + icon.
- "OTTO Desktop" labels (`desktoptray.go`) → `id.DisplayName + " Desktop"` (brand-aware).
- Binary/app: `otto-tray`→`gateway-tray`; `OTTO Tray.app`→`Gateway Tray.app`; pidfile matchers (`pidfile_*.go`) match `gateway`/`gateway.exe`; autostart labels updated.
- Path seams per §4 (installroot split, pidfile, config, sentinel, wrapper invocation, open-folder).

### 5c. Wrappers (`scripts/`)
- `otto-gw`→`gw` (bash), `otto-gw.ps1`→`gw.ps1`, `otto-gw.bat`→`gw.bat`, `start/stop/status.bat` call `gw.bat`. Two-anchor resolution; `.env` search `./.env`, `$GW_HOME/.env`; template `.env.example`; `init` default dest `$GW_HOME/.env`.
- Start/stop unchanged mechanism (nohup / Start-Process), retargeted paths.

### 5d. Installers (`scripts/install.sh` / `install.ps1`)
- Compute `GW_INSTALL_DIR` (per-OS §3) and `GW_HOME=~/.gw`.
- Extract tarball `otto_gateway/{bin,scripts,*.md}` → `GW_INSTALL_DIR`.
- Generate config via `gw init` → `~/.gw/.env` (skip if exists).
- PATH: symlink `~/.local/bin/gw`→`$GW_INSTALL_DIR/scripts/gw` (unix); prepend `$GW_INSTALL_DIR\scripts` to User PATH (win).
- macOS `Gateway Tray.app` built under `GW_INSTALL_DIR`; re-sign; bundle id `io.cmetech.gateway-tray`.
- **Run migration** (§6) before/after extract.

### 5e. Docs
- README, `docs/INSTALL.md`, `operator-quickstart.md`, `operating.md`, DEVELOPERS.md, CLAUDE.md: "OTTO Gateway"→"Gateway", `~/.otto-gw`→`~/.gw`, `otto-gw`→`gw`, new layout, migration note. (Historical `docs/superpowers/**` specs left as-is.)

### 5f. Makefile / packaging
- `BINARY := gateway`, `TRAY_BINARY := gateway-tray`; stage `bin/gateway[.exe]`, `bin/gateway-tray[.exe]`, `scripts/gw*`. **Keep** the `otto_gateway/` prefix, artifact names `otto_gateway-*.tar.gz|zip`, and `SHA256SUMS-*`. ldflags `-X otto-gateway/internal/version.Version` unchanged (module kept).

## 6. Auto-migration (`~/.otto-gw` → `~/.gw`)

Idempotent, run by the installer AND as a tray/wrapper first-run guard (a small
shared step). Steps, each a no-op if already done:
1. If `~/.gw/.env` absent and a legacy config exists (`~/.otto-gw.env`, `~/.otto-gw/.env.otto-gw`, or `<oldroot>/.env.otto-gw`): create `~/.gw`, **move** config → `~/.gw/.env`; `~/.otto-gw.overrides.env`→`~/.gw/overrides.env`; `tray.json`→`~/.gw/tray.json`; preserve any `support/`, rotate `upgrade.log`.
2. Autostart: if the old key exists (`~/Library/LaunchAgents/io.cmetech.otto-tray.plist` / Run-key `OttoTray`), unregister it and register the new one pointing at `gateway-tray`.
3. PATH symlink: remove `~/.local/bin/otto-gw`, add `~/.local/bin/gw` (unix). Windows PATH: add new scripts dir; leave the old dir entry (harmless once old install removed).
4. Do NOT delete the old `~/.otto-gw` code dir automatically (leave for the user / note in output) — only the config/autostart are migrated. Log what moved.

Migration is covered by unit tests with an injected FS (no real HOME writes).

## 7. Testing
- Unit (seam-injected, darwin+windows, table-driven): `resolveGWHome`, `resolveInstallDir`, the two-anchor derivations, migration detect/move/skip (idempotent), autostart key swap, brand-aware desktop labels.
- Update `internal/admin/handlers_test.go` de-brand asserts.
- Wrapper scripts: extend `tests/install/smoke_posix.sh` for `~/.gw`, `gw` symlink, `.env`.
- `GOOS=windows`/`linux` build + `gofumpt`/`vet`/`golangci-lint` green; darwin tray tests pass. (CI test-race is Linux; tray package tests run locally.)
- Exclude `auto`/`kiro`/`KIRO_*`/`AUTH_TOKEN` from every rename — assert they're untouched.

## 8. Risks
- **Atomic cross-component rename** (installer↔wrapper↔tray↔admin): a partial rename breaks upgrades — land as one change; migration bridges old→new.
- **Destructive-ish migration** (moves config): guard with existence checks, move-not-delete, idempotency, and tests; never touch the old code dir.
- **Two-anchor refactor** touches the tray's single-`installRoot` abstraction broadly — highest-churn area; cover with tests.
- **Docs drift** — large doc surface (`INSTALL.md` 127 hits, etc.); mechanical but must stay consistent with the new layout.

## 9. Out of scope / follow-on
- **Spec 2 — graphical wizard:** a Tauri (Rust+React) installer modeled on
  hermes-agent's `apps/bootstrap-installer` (~4.7k LOC), driving `install.sh`/
  `install.ps1` via a `-Manifest` / `-Stage NAME -Json` protocol. Prereq: add that
  staged protocol to the (now de-branded) install scripts. Adds a Rust/Tauri build
  to the release pipeline.
- Repo/module/artifact/URL rename; API/env-var/service-manager changes.
