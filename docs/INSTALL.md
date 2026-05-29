# OTTO Gateway — Install & Upgrade Reference

This file complements `README.md` (the operator quickstart that also ships in every release archive). The quickstart owns the happy path; **this file owns the nuance** — per-OS first-run checklists, the `.env` file load order with cwd-independent location recommendations, the Windows wrapper choice tradeoff table, upgrade behavior, common install pitfalls, and verification commands with expected output.

If you only ever run on one OS and your machine is unsurprising, the quickstart is enough. Read this file when:

- You are installing on Windows for the first time (the cwd gotcha is real).
- You are upgrading an existing install and want to know what will be overwritten.
- An install step failed and the quickstart troubleshooting block did not cover it.
- You are scripting the install (CI, image baking, fleet rollout) and need the non-interactive surface.

---

## Table of contents

- [First-run checklist: macOS](#first-run-checklist-macos)
- [First-run checklist: Linux](#first-run-checklist-linux)
- [First-run checklist: Windows](#first-run-checklist-windows)
- [The .env file](#the-env-file)
  - [init flag reference](#init-flag-reference)
- [Wrapper choice tradeoff table](#wrapper-choice-tradeoff-table)
- [Upgrade behavior](#upgrade-behavior)
  - [How to upgrade (step-by-step)](#how-to-upgrade-step-by-step)
  - [Re-running init on an upgraded install](#re-running-init-on-an-upgraded-install)
- [Uninstall](#uninstall)
- [Common install pitfalls](#common-install-pitfalls)
- [Verifying install](#verifying-install)
- [Where to go next](#where-to-go-next)

---

## First-run checklist: macOS

1. **Verify the download (recommended).** If the release came with a `SHA256SUMS-<version>.txt`, check it before extracting:

   ```bash
   shasum -a 256 -c SHA256SUMS-<version>.txt 2>&1 | grep otto_gateway-darwin
   ```

   Expect a single `OK` line per archive you care about. Skip this step at your own risk if you trust the source.

2. **Extract the archive.** Pick the matching arch — `darwin-arm64` for Apple Silicon (M1/M2/M3/M4), `darwin-amd64` for Intel Macs:

   ```bash
   tar -xzf otto_gateway-darwin-arm64-<version>.tar.gz
   cd otto_gateway
   ```

3. **Strip the macOS quarantine attribute.** If the archive came from a browser download (or anything that flowed through Gatekeeper), the binary carries `com.apple.quarantine` and macOS will refuse to launch it the first time:

   ```bash
   xattr -d com.apple.quarantine bin/otto-gateway
   ```

   One-time per install. The wrapper script (`scripts/otto-gw`) is a shell script and is not subject to Gatekeeper.

4. **Install `kiro-cli` and confirm it is on `PATH`.** The gateway is a router — without `kiro-cli` it boots in a degraded mode and returns `503` on every chat request (the `/health*` endpoints still work). Follow your team's distribution instructions, then confirm:

   ```bash
   command -v kiro-cli
   ```

   If `kiro-cli` lives somewhere not on `PATH`, note the absolute path — `init` will prompt for it in the next step. If `KIRO_CMD` is unset and `kiro-cli` is on `PATH`, the wrapper auto-detects it on `start` and prints `✓ KIRO_CMD auto-detected: <path>`.

5. **Run `init`.** Generates a random `AUTH_TOKEN` and `PII_HASH_KEY`, prompts for `KIRO_CMD`, `HTTP_ADDR`, and PII mode, then writes `$HOME/.otto-gw.env` (mode `0600`):

   ```bash
   ./scripts/otto-gw init
   ```

   For non-interactive installs (CI, image baking), pass flags:

   ```bash
   ./scripts/otto-gw init \
     --non-interactive \
     --kiro /usr/local/bin/kiro \
     --addr 127.0.0.1:11434 \
     --pii hash
   ```

   `--dest PATH` chooses the output file; `--here` writes `./.env.otto-gw` instead of the home directory; `--force` overwrites an existing file.

6. **Start.** The wrapper waits for `/health` to come up before returning. On failure it tails the last 20 lines of the structured log inline so you see the actual error without grepping:

   ```bash
   ./scripts/otto-gw start
   ```

   Confirm with `curl -sf http://127.0.0.1:18080/health` — see [Verifying install](#verifying-install) for the full check.

---

## First-run checklist: Linux

1. **Verify the download (recommended).**

   ```bash
   sha256sum -c SHA256SUMS-<version>.txt 2>&1 | grep otto_gateway-linux
   ```

   On Debian/Ubuntu, `shasum -a 256` also works. Expect `OK` per archive.

2. **Extract the archive.**

   ```bash
   tar -xzf otto_gateway-linux-amd64-<version>.tar.gz
   cd otto_gateway
   ```

3. **Install `kiro-cli` and confirm it is on `PATH`.**

   ```bash
   command -v kiro-cli
   ```

   Same auto-detect behavior on `start` as macOS: if `KIRO_CMD` is unset and `kiro-cli` is on `PATH`, the wrapper sets `KIRO_CMD` for you and prints `✓ KIRO_CMD auto-detected: <path>`.

4. **Run `init`.**

   ```bash
   ./scripts/otto-gw init
   ```

   Writes `$HOME/.otto-gw.env` (mode `0600`) by default. The non-interactive flag form documented under macOS (step 5) applies identically here.

5. **Start.**

   ```bash
   ./scripts/otto-gw start
   ```

   Confirm with the verification commands in [Verifying install](#verifying-install).

---

## First-run checklist: Windows

Windows install has two extra concerns the POSIX OSes do not: **Mark-of-the-Web (MOTW)** Zone.Identifier streams that Windows attaches to every file extracted from a downloaded archive, and **PowerShell execution policy**, which refuses to run untrusted `.ps1` scripts by default. The bundled `setup.bat` handles both.

1. **Verify the download (recommended).** In PowerShell:

   ```powershell
   $want = (Select-String -Path .\SHA256SUMS-<version>.txt -Pattern 'windows-amd64' | ForEach-Object { ($_.Line -split '\s+')[0] })
   $got  = (Get-FileHash .\otto_gateway-windows-amd64-<version>.zip -Algorithm SHA256).Hash.ToLower()
   if ($want -eq $got) { "OK" } else { "MISMATCH" }
   ```

2. **Extract the archive.** Either right-click → Extract All in Explorer, or:

   ```powershell
   Expand-Archive otto_gateway-windows-amd64-<version>.zip
   cd otto_gateway
   ```

3. **Run `scripts\setup.bat` once.** Double-click it from File Explorer, or run from cmd.exe / PowerShell. It does two things:

   - **Strips MOTW Zone.Identifier streams** from every file in the package (`Get-ChildItem -Recurse -File | Unblock-File`). Without this, PowerShell flags every shipped `.ps1` as "untrusted" and refuses to run it.
   - **Sets your PowerShell execution policy to `RemoteSigned` at `CurrentUser` scope** so subsequent `.\scripts\otto-gw.ps1 <cmd>` invocations work without `-ExecutionPolicy Bypass`.

   `cmd.exe` is not subject to PowerShell execution policy, so `setup.bat` works even on a machine where `Set-ExecutionPolicy` has never been called. If your organization locks execution policy via Group Policy (`LocalMachine` or `MachinePolicy` scope override `CurrentUser`), `setup.bat` will report "Setup hit an error" — see [Common install pitfalls](#common-install-pitfalls) for the workaround.

4. **Install `kiro-cli` and confirm it is on `PATH`.**

   ```powershell
   Get-Command kiro-cli
   ```

   Auto-detect on `start` works the same as POSIX. If `kiro-cli` lives off-PATH, set `KIRO_CMD` to its absolute path in your `.env` (next step).

5. **Run `init`.** Pick whichever wrapper surface you prefer — they all reach the same PowerShell `Invoke-Init`:

   ```powershell
   .\scripts\otto-gw.ps1 init
   ```

   or

   ```cmd
   .\scripts\otto-gw.bat init
   ```

   Writes `$env:USERPROFILE\.otto-gw.env` (for example `C:\Users\<you>\.otto-gw.env`) by default. This location is cwd-independent — see [The .env file](#the-env-file) for why that matters when an operator double-clicks `start.bat` from Explorer.

   Non-interactive form:

   ```powershell
   .\scripts\otto-gw.ps1 init `
     -NonInteractive `
     -Kiro "C:\Tools\kiro.exe" `
     -Addr "127.0.0.1:11434" `
     -Pii hash
   ```

6. **Start.** Three equivalent surfaces:

   ```powershell
   .\scripts\otto-gw.ps1 start       # PowerShell wrapper
   .\scripts\otto-gw.bat start       # cmd.exe-friendly dispatcher
   .\scripts\start.bat                # double-clickable shortcut
   ```

   Confirm with the verification commands in [Verifying install](#verifying-install).

---

## The .env file

The `.env` file is the persistent way to set gateway config. The wrappers also accept CLI flags for ad-hoc overrides on a single launch (see the operator-quickstart README).

### Load order

The wrapper searches for an `.env` file in this order (first match wins):

1. `--env-file PATH` / `-EnvFile PATH` — CLI override (highest precedence).
2. `$OTTO_ENV_FILE` / `$env:OTTO_ENV_FILE` — environment override.
3. `./.env.otto-gw` — project-local, relative to the **current working directory**.
4. `$HOME/.otto-gw.env` (POSIX) or `$env:USERPROFILE\.otto-gw.env` (Windows) — per-user.

The loader is data-only: it parses `KEY=value` lines, tolerates blank lines and `#` comments, strips an optional leading `export `, and honors one layer of surrounding single or double quotes. It does NOT execute `$(...)` or backticks — your `.env` is not a shell script.

### Recommended location per OS

A **cwd-independent stable path** is recommended for the persistent `.env`. That means the per-user location at the bottom of the load order:

- **macOS / Linux:** `$HOME/.otto-gw.env`
- **Windows:** `$env:USERPROFILE\.otto-gw.env` (for example `C:\Users\<you>\.otto-gw.env`)

`init` writes to this location by default on every OS. Use `--here` / `-Here` to write project-local `./.env.otto-gw` instead — useful when you want a `.env` to follow a checkout, but at the cost of cwd sensitivity (next section).

### The Windows-double-click cwd gotcha

When an operator double-clicks `start.bat` (or `stop.bat`, `status.bat`) from Explorer, Windows runs the script with the **`scripts\` directory as cwd**, not the `otto_gateway\` parent.

That matters because step 3 of the load order — `./.env.otto-gw` — is resolved relative to cwd. From `scripts\`, `.\.env.otto-gw` resolves to `scripts\.env.otto-gw`, which is almost certainly not where your real `.env.otto-gw` lives. The loader will not find it, will fall through to the per-user location, and (if you also do not have `$env:USERPROFILE\.otto-gw.env`) will launch the gateway with **whatever environment variables are inherited from your shell** — typically nothing relevant. Result: surprising behavior — the gateway boots without your custom `HTTP_ADDR`, `PII_*` settings, `AUTH_TOKEN`, etc.

The cwd-independent `$env:USERPROFILE\.otto-gw.env` location is immune to this — it resolves to the same path no matter what cwd the script was launched in. Two ways to land there:

- Run `.\scripts\otto-gw.ps1 init` (or `.\scripts\otto-gw.bat init`), which writes to `$env:USERPROFILE\.otto-gw.env` by default.
- Copy the example file manually: `Copy-Item .\scripts\.env.otto-gw.example "$env:USERPROFILE\.otto-gw.env"` and edit it.

The same advice applies to POSIX users who run wrappers from a launcher that sets cwd somewhere unexpected (XDG `.desktop` files, macOS Automator workflows, systemd user services with stale `WorkingDirectory=`): use `$HOME/.otto-gw.env`.

### Precedence summary

For any single config key, the value the gateway sees is determined by:

1. **CLI flag** (highest) — e.g. `--pii hash` on the wrapper command line.
2. **`.env` file** — whichever file the loader resolved.
3. **Inherited shell environment** (lowest) — only used for keys neither the CLI nor the `.env` set.

### init flag reference

The `init` subcommand generates an `.env` file with sensible defaults and pregenerated secrets. POSIX names (`--flag`) and PowerShell names (`-Flag`) are listed together; pick the form for your wrapper.

| Flag | What it does |
| --- | --- |
| `--dest PATH` / `-Dest PATH` | Write to a specific path. Default per OS: `$HOME/.otto-gw.env` (POSIX), `$env:USERPROFILE\.otto-gw.env` (Windows). |
| `--here` / `-Here` | Shortcut for `--dest ./.env.otto-gw` — write project-local instead of per-user. |
| `--force` / `-Force` | Overwrite an existing dest. On re-init this triggers **value preservation** (see [Re-running init on an upgraded install](#re-running-init-on-an-upgraded-install)) — your existing values become the prompt defaults instead of cold-starting. |
| `--non-interactive` / `-NonInteractive` | Suppress all prompts; use defaults or existing-file values for anything not supplied via flag. |
| `--kiro PATH` / `-Kiro PATH` | Skip the `KIRO_CMD` prompt. |
| `--addr ADDR` / `-Addr ADDR` | Skip the `HTTP_ADDR` prompt (default `127.0.0.1:18080`). |
| `--pii MODE` / `-Pii MODE` | Skip the PII mode prompt. One of `off`, `replace`, `mask`, `hash`, `drop` (default `off`). |
| `--auth-enabled` / `-AuthEnabled` | Enable bearer-token auth. Default off — when disabled the `AUTH_TOKEN=` line is pregenerated but written commented out, so flipping the leading `#` enables it without re-running init. |
| `--auth-token TOK` / `-AuthToken TOK` | Use TOK instead of generating a random token. Implies `--auth-enabled`. |
| `--chat-trace` / `-ChatTrace` | Enable chat-trace NDJSON tracer. Default off — when enabled, every chat request writes two NDJSON records (pre-redaction request + post-chain response) to a separate `chat-trace.log` (mode `0600`, 3-day retention). Records contain **raw user content** — treat the file as sensitive. |
| `--hash-key KEY` / `-HashKey KEY` | Use KEY instead of generating a random PII hash key. Useful when restoring an install from backup and you need to preserve `hash`-mode log correlation tags across the rebuild. |
| `--regenerate-secrets` / `-RegenerateSecrets` | On re-init (`--force` against an existing dest), mint fresh `AUTH_TOKEN` + `PII_HASH_KEY` instead of reusing the existing ones. Use for post-leak rotation. **Breaks every client carrying the old token and unlinks all prior hash-mode log correlations** — explicit by design. |

**Defaults shipped by init (cold start):**

| Field | Default | Why |
| --- | --- | --- |
| Auth | **disabled** | Laptop-friendly default; pregenerated token in the file as a comment so enabling is one `#` removal. |
| PII redaction | **off** | Trade-free for non-sensitive prototyping. |
| Chat-trace | **disabled** | Records sensitive raw content; opt-in only. |
| `ENABLED_HOOKS` | All four hooks listed | The day-one shipped chain. Listed uncommented so the active surface is discoverable. Disabled hooks are no-op passthroughs via the two-knob design (e.g. `AUTH_TOKEN` empty → `AuthHook` returns immediately). |
| `HTTP_ADDR` | `127.0.0.1:18080` | No collision with anything else common on a dev box. |

---

## Wrapper choice tradeoff table

Multiple ways to invoke the gateway. Pick the one that matches your workflow.

### Windows

| Wrapper | Best for | Notes |
| --- | --- | --- |
| `scripts\otto-gw.bat <cmd>` | Daily use, cmd.exe-friendly | Immune to PowerShell execution policy because cmd.exe is not subject to it. The dispatcher invokes `powershell -NoProfile -ExecutionPolicy Bypass -File scripts\otto-gw.ps1 <cmd>` internally — works on Group-Policy-locked machines without further setup. |
| `scripts\otto-gw.ps1 <cmd>` | Scripted automation with typed flags | Requires execution policy `RemoteSigned` or higher; `setup.bat` sets that on `CurrentUser` scope. Lets you pass typed flags like `-Pii hash -HashKey $key` directly without quoting around batch-file argument parsing. |
| `scripts\start.bat` / `stop.bat` / `status.bat` | Double-click convenience from Explorer | Each is a thin wrapper around `otto-gw.bat <cmd>`. Beware the [Windows-double-click cwd gotcha](#the-windows-double-click-cwd-gotcha) if you rely on a project-local `.env.otto-gw`. For one-shot ops where cwd does not matter (PID file already exists in `.otto\gw\`), these are fine. |

### macOS / Linux

| Wrapper | Best for | Notes |
| --- | --- | --- |
| `./scripts/otto-gw <cmd>` (bash) | Single surface, every use case | Same subcommand set as the PowerShell wrapper: `init \| start \| stop \| status \| restart \| logs \| run \| env \| version`. Loads `./.env.otto-gw` then falls back to `$HOME/.otto-gw.env`. Auto-detects `kiro-cli` on `PATH` if `KIRO_CMD` is unset. |

---

## Upgrade behavior

The supported upgrade path is "extract the new archive over the old install location." The semantics differ subtly between the POSIX `tar` and Windows `Expand-Archive` paths — read both rows for the OS you operate on.

### How to upgrade (step-by-step)

Same pattern on every OS: stop the gateway, extract the new archive **on top of** the existing `otto_gateway/` folder (do not delete the old folder first), restart. The extract overlays the version-locked files (binary, wrappers, READMEs) while leaving your runtime state (`.env`, `logs/`, `.otto/gw/`) untouched. You do **not** re-run `init` — your existing `.env` carries forward.

**macOS / Linux:**

```bash
cd /path/containing/otto_gateway   # the parent dir, NOT otto_gateway/ itself
./otto_gateway/scripts/otto-gw stop          # OK if "not running"
tar -xzf otto_gateway-darwin-arm64-<version>.tar.gz   # overlays into ./otto_gateway/
cd otto_gateway
xattr -d com.apple.quarantine bin/otto-gateway 2>/dev/null || true   # macOS only
./scripts/otto-gw start
./scripts/otto-gw version                    # confirm the new version is live
```

**Windows (PowerShell):**

```powershell
cd C:\path\containing\otto_gateway          # parent of otto_gateway\
.\otto_gateway\scripts\otto-gw.bat stop      # OK if "not running"
Expand-Archive -Force otto_gateway-windows-amd64-<version>.zip
cd otto_gateway
.\scripts\setup.bat                          # re-strip MOTW on newly-extracted files
.\scripts\otto-gw.bat start
.\scripts\otto-gw.bat version                # confirm
```

The `setup.bat` re-run on Windows is necessary because Mark-of-the-Web Zone.Identifier streams are attached to every freshly-extracted file. Execution policy is per-user and persists across upgrades, so only the MOTW strip half of `setup.bat` is doing real work the second time.

### Re-running init on an upgraded install

You normally do **not** need to re-run `init` after an upgrade — your `.env` keeps working as-is. The one case to re-run is when a new version adds a new config knob (like `CHAT_TRACE` in v1.5.6) that you want surfaced in your file with the official commented template above it.

When you do re-run, `init --force` / `init -Force` now preserves your existing values instead of cold-starting:

```bash
./scripts/otto-gw init --force --non-interactive
```

- Existing `AUTH_TOKEN`, `PII_HASH_KEY`, `KIRO_CMD`, `HTTP_ADDR`, `PII_REDACTION_MODE`, `CHAT_TRACE` state — **preserved**.
- New fields introduced by the upgraded wrapper — **added** with sensible defaults (commented if off).
- Comment formatting / section dividers — **refreshed** from the new template.

Secrets are reused bit-for-bit unless you explicitly pass `--regenerate-secrets` / `-RegenerateSecrets`. Use that flag when rotating after a suspected leak; do not use it casually because every client carrying the old `AUTH_TOKEN` will start getting 401s and every prior hash-mode log correlation tag becomes un-linkable to live data.

The interactive form (`init --force` without `--non-interactive`) prompts for every field with the existing value as the default — hit Enter to keep, type to change.

### What is replaced on extract

These files are part of the release archive itself, so they are overwritten by definition when you extract a newer version:

- `bin/otto-gateway` (POSIX) or `bin/otto-gateway.exe` (Windows)
- `scripts/otto-gw` (POSIX wrapper)
- `scripts/otto-gw.ps1` (PowerShell wrapper)
- `scripts/otto-gw.bat` (cmd.exe dispatcher)
- `scripts/setup.bat`
- `scripts/start.bat`, `scripts/stop.bat`, `scripts/status.bat`
- `scripts/.env.otto-gw.example`
- `README.md`
- `INSTALL.md` (this file)

Accept the overwrites — these files carry version-specific behavior that must move forward together.

### What is preserved on extract

These files are NOT in the release archive, so a fresh extract leaves them alone — but only if you extract to the parent directory that already contains an `otto_gateway/` directory (so the new contents merge into the existing one rather than replacing it):

- `.env.otto-gw` (project-local persistent config, if you placed one there)
- `logs/` (rotated structured logs + boot sidecars written by the running gateway)
- `.otto/gw/` (PID file + state)

### Windows `Expand-Archive -Force` caveat

`Expand-Archive` without `-Force` will fail if any extracted file already exists. The fix most operators reach for is `-Force`, which **silently overwrites** every file by name — including `scripts\.env.otto-gw.example` (which the archive ships) but NOT your real `.env.otto-gw` (which the archive does not ship, so there is nothing to overwrite it with). The thing to watch is: **make sure your real persistent config is named `.env.otto-gw` (no `.example` suffix)** — otherwise `Expand-Archive -Force` will overwrite it with the shipped template and you will lose your settings.

If you have edited `scripts\.env.otto-gw.example` locally (e.g. to track your team's defaults in git), back it up before extracting a new archive:

```powershell
Copy-Item .\scripts\.env.otto-gw.example .\scripts\.env.otto-gw.example.bak
Expand-Archive -Force otto_gateway-windows-amd64-<version>.zip
Compare-Object (Get-Content .\scripts\.env.otto-gw.example) (Get-Content .\scripts\.env.otto-gw.example.bak)
```

The same caveat applies to any custom shortcut(s) you have added under `scripts\` — back them up before `-Force` extracts.

### Recommended: keep the persistent .env outside the install

Because the install directory is a moving target across upgrades, the safest persistent `.env` location is **outside the extracted folder entirely**:

- POSIX: `$HOME/.otto-gw.env`
- Windows: `$env:USERPROFILE\.otto-gw.env`

These live in your home directory, not the install. Upgrades cannot touch them regardless of how you extract the archive. `init` writes to these locations by default on every OS.

### Windows-only re-run of setup.bat

After a fresh extract on Windows, re-run `scripts\setup.bat` once. The newly-extracted files carry fresh MOTW Zone.Identifier streams that PowerShell will treat as "untrusted" — `Unblock-File` clears them. (Execution policy is per-user, not per-file, so the second `Set-ExecutionPolicy` line is a no-op once you have already accepted it from the first install — but the MOTW strip is necessary every time.)

---

## Uninstall

OTTO Gateway is not installed via a package manager — there is no installer database to uninstall from. Removal is two-and-a-half steps: stop the gateway, delete the extracted folder, delete the per-user `.env` file. Everything the gateway writes at runtime lives inside the extracted folder (PID file under `.otto\gw\` or `.otto/gw/`, logs under `logs/`, the optional project-local `.env.otto-gw`), so deleting the folder takes all of it with it. The per-user `.env` lives in your home directory and survives folder deletion — you remove it separately.

Below are the per-OS exact-command checklists. They assume a vanilla install with default paths; if you set `OTTO_LOG`, `OTTO_PID`, `OTTO_STATE_DIR`, or `LOG_FILE` to a custom location, also delete from there.

### macOS / Linux

```bash
# 1. Stop the gateway (cleans up its own PID file).
cd /path/to/otto_gateway
./scripts/otto-gw stop                  # OK if it says "not running"

# 2. Delete the extracted install folder.
cd ..
rm -rf otto_gateway/

# 3. Delete the per-user .env file (skip if you want to keep your config).
rm -f ~/.otto-gw.env
```

That is the whole removal. The gateway never writes anywhere else by default — no `launchctl`, no `systemd` unit, no entries under `/usr/local/`, no shell-profile edits.

### Windows

```powershell
# 1. Stop the gateway (cleans up its own PID file).
cd C:\Users\<you>\software\otto_gateway
.\scripts\otto-gw.bat stop              # OK if it says "not running"

# 2. Delete the extracted install folder.
cd ..
Remove-Item -Recurse -Force .\otto_gateway

# 3. Delete the per-user .env file (skip if you want to keep your config).
Remove-Item -Force "$env:USERPROFILE\.otto-gw.env"
```

The `Set-ExecutionPolicy RemoteSigned -Scope CurrentUser` that `setup.bat` applied during install persists across uninstall — it is a per-user PowerShell setting, not part of OTTO. If you want to revert it explicitly:

```powershell
Set-ExecutionPolicy Restricted -Scope CurrentUser    # back to Windows default
```

Most operators leave the policy as-is; `RemoteSigned` is a sensible long-term default and other tools benefit from it too. There are no Start Menu entries, scheduled tasks, services, or registry keys to clean up — none are created by install.

### What is NOT removed by the above

- Any logs you exported elsewhere (e.g. via `OTTO_LOG=D:\splunk\otto.log`).
- Custom env files outside the two default paths (e.g. `--env-file C:\config\otto.env`).
- `kiro-cli` itself — OTTO Gateway is a router, not an installer. Remove `kiro-cli` per its own uninstall instructions if you no longer need it.
- The compressed rotated archives under `logs\` if you copied them to a long-term retention location before deleting the install folder.

---

## Common install pitfalls

### CHAT_TRACE captures raw user content — file permissions and retention

OTTO Gateway ships with an optional `ChatTraceHook` (gated by `CHAT_TRACE=true` in `.env.otto-gw`) that writes one NDJSON `pre_chain_in` record per chat-shaped request to a dedicated `chat-trace.log`. The pre-record captures the **post-adapter canonical request BEFORE PII redaction runs**, which means the file contains the raw prompt the client actually sent — including any email, phone number, SSN, credit-card number, or other PII the user typed. This is the entire point of the feature (operators debugging "what did the client actually ask the gateway") and also its biggest risk.

The gateway mitigates this on the file system: `chat-trace.log` is opened with mode `0o600` (owner read/write only — never group or world) and the timberjack rotator prunes old archives at 3 days by default (`CHAT_TRACE_MAX_AGE_DAYS=3`), rotating daily at local midnight with gzip compression. Setting `CHAT_TRACE=false` (or simply leaving it unset, which is the default) keeps the file from being created on disk at all — no rotator is opened, no records are written.

**Operators MUST NOT ship `chat-trace.log` to centralized log aggregators without a redaction sidecar.** The hash-mode PII redaction (`PII_REDACTION_MODE=hash` + `PII_HASH_KEY`) is the gateway's offered correlation primitive when aggregation is required; running a separate batch redactor on rotated `*.log.gz` archives before they leave the host is the alternative. See `scripts/.env.otto-gw.example` for the full set of `CHAT_TRACE_*` knobs and recommended defaults.

If you only want chat tracing for a short debugging window, the recommended pattern is: enable `CHAT_TRACE=true`, reproduce the issue, copy the relevant NDJSON line out, then flip it back to `CHAT_TRACE=false` and restart the gateway. The 3-day rotation window will prune the captured file on its own; no manual cleanup is required if you don't want it earlier than that.

### Windows: `.ps1` blocked as untrusted, or `setup.bat` did not run

**Cause.** Windows attached MOTW Zone.Identifier streams to every file in the archive (because the archive arrived via a browser or any other download path Gatekeeper considers untrusted). PowerShell refuses to run `.ps1` files with MOTW unless your execution policy is `Bypass` or the files are unblocked.

**Fix.** Run `scripts\setup.bat`. It does `Get-ChildItem -Recurse -File | Unblock-File` on the entire package and sets `Set-ExecutionPolicy RemoteSigned -Scope CurrentUser`. If you cannot run `setup.bat` for any reason, the per-invocation bypass works for any individual command: `powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\otto-gw.ps1 <cmd>`.

### Windows: `setup.bat` says "Setup hit an error"

**Cause.** Your organization locks PowerShell `ExecutionPolicy` at the `LocalMachine` or `MachinePolicy` scope via Group Policy. Those scopes override anything `setup.bat` writes to `CurrentUser`, so the second step of setup fails.

**Fix.** Use `scripts\otto-gw.bat <cmd>` for every operation. It dispatches via cmd.exe using `-ExecutionPolicy Bypass` internally, which works on Group-Policy-locked machines without further intervention. The per-command `start.bat` / `stop.bat` / `status.bat` shortcuts go through the same dispatcher and also work. Equivalently, you can invoke the PowerShell wrapper directly with the same bypass flag:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\otto-gw.ps1 <cmd>
```

The MOTW strip from step 1 of `setup.bat` still ran successfully even if the execution-policy step failed; you can verify by checking for absence of `Zone.Identifier` alternate data streams (`dir /R bin\otto-gateway.exe` should not list `:Zone.Identifier:$DATA`).

### Windows: `boot-err.log` shows `scheduled rotation failed: ... being used by another process`

**Cause.** The gateway uses [timberjack](https://github.com/DeRuina/timberjack) for daily log rotation at local midnight. On Windows, `os.Rename` refuses to rename a file that any process has open for writing — and the gateway itself holds `logs\otto-gateway.log` open continuously. POSIX permits this; Windows does not. So the exact-midnight rotation often fails and timberjack writes a single-line error to stderr, which the wrapper captures to `logs\otto-gateway.boot-err.log`.

**Is it actually broken?** No. timberjack retries on its next mill tick once the handle settles, typically within a few hours, and you will see a `otto-gateway-YYYY-MM-DDTHH-MM-SS.SSS-time.log.gz` appear in `logs\` named with the *actual* rotation time, not the scheduled time. Every log line from the failed-rotation window lands in either the rotated archive or the new active file — no data loss, no retention impact (pruning is age-based on file mtime, not on the embedded timestamp). The 500MB safety valve also still triggers normally if a chatty client floods the file.

**When to worry.** A `boot-err.log` growing by many lines per day (one line per failed retry attempt), no `*-time.log.gz` files appearing across multiple days, or the active `otto-gateway.log` growing past 500MB with no rotation. Any of those indicates the file handle is permanently wedged and the operator should restart the gateway (`.\scripts\otto-gw.bat restart`).

**Verifying the steady state.** Each morning you should see one new `*-time.log.gz` covering the previous day. The first line of `otto-gateway.boot-err.log` can be a one-time rotation error from that day's midnight attempt — that is expected on Windows and self-heals.

### macOS: "otto-gateway cannot be opened because Apple cannot check it for malicious software"

**Cause.** The binary is ad-hoc signed but NOT notarized by Apple (notarization requires a paid Apple Developer ID, which v1 distribution deliberately keeps out of scope). macOS Gatekeeper attaches `com.apple.quarantine` to anything downloaded via a browser or extracted from a downloaded archive, and refuses to launch quarantined binaries from unidentified developers.

**Fix.** Strip the quarantine attribute once per install:

```bash
xattr -d com.apple.quarantine bin/otto-gateway
```

Or right-click `bin/otto-gateway` in Finder → Open → "Open" in the dialog. macOS records the exception and subsequent launches via the wrapper work normally.

### `kiro-cli` not on `PATH` and `KIRO_CMD` is unset

**Cause.** The gateway is a router. Without `kiro-cli` it boots in a degraded mode — `/health`, `/health/hooks`, and `/health/pool` work for diagnostics, but every chat request returns `503`.

**Fix.** Install `kiro-cli` per your team's distribution instructions, OR set `KIRO_CMD` to its absolute path in your `.env`:

```bash
echo 'KIRO_CMD=/absolute/path/to/kiro-cli' >> $HOME/.otto-gw.env
./scripts/otto-gw restart
```

The wrapper auto-detects `kiro-cli` on `PATH` if `KIRO_CMD` is unset (see `preflight_kiro` in `scripts/otto-gw` and `Preflight-Kiro` in `scripts/otto-gw.ps1`), so installing `kiro-cli` into a directory already on `PATH` is the lowest-config fix.

### Port already in use (`bind: address already in use`)

**Cause.** Another process holds `127.0.0.1:18080`. Often it is a previous `otto-gateway` instance that lost its PID file, or a real Ollama install on `:11434` (if you reconfigured `HTTP_ADDR` to that port).

**Fix.** Override `HTTP_ADDR` for a single restart:

```bash
HTTP_ADDR=:18081 ./scripts/otto-gw restart
```

Or find and kill the conflicting process: `lsof -ti :18080 | xargs kill` (POSIX) / `Get-NetTCPConnection -LocalPort 18080` (PowerShell).

### Hash-mode boot refusal (`PII_REDACTION_MODE=hash` without `PII_HASH_KEY`)

**Cause.** Hash mode without a key would be rainbow-table-trivial unkeyed HMAC. The gateway refuses to start in that configuration by design.

**Fix.** Set the key:

```bash
./scripts/otto-gw restart --pii hash --hash-key "$(openssl rand -hex 32)"
```

Persist it in your `.env`:

```
PII_REDACTION_ENABLED=true
PII_REDACTION_MODE=hash
PII_HASH_KEY=<the same 32-byte hex string>
```

---

## Verifying install

Concrete one-liners with expected output for each entry point. Run all four after a fresh install to confirm wrapper + binary + admin surface all agree.

### Wrapper version check

The wrapper's `version` subcommand delegates straight to `bin/otto-gateway --version`, so the wrapper and the binary cannot disagree.

```bash
./scripts/otto-gw version
```

```powershell
.\scripts\otto-gw.ps1 version
```

```cmd
.\scripts\otto-gw.bat version
```

**Expected output:** a single line like `v1.5.1` (or whatever the current build's tag is). A `0.0.0-dev` line means the binary was built from a dirty working tree — fine for development, surprising for a release archive.

### Binary version check

Same string as above, by definition:

```bash
./bin/otto-gateway --version
```

```powershell
.\bin\otto-gateway.exe --version
```

**Expected output:** identical to the wrapper version output.

### Health probe (gateway running)

After `otto-gw start`:

```bash
curl -sf http://127.0.0.1:18080/health
```

```powershell
Invoke-RestMethod -Uri http://127.0.0.1:18080/health
```

**Expected output:** JSON like `{"status":"ok",...}`. `/health` is auth-exempt by design — no `AUTH_TOKEN` needed.

If you set `HTTP_ADDR=:11434` to take over the Ollama default port, adjust the URL.

### Admin snapshot probe (version agreement check)

```bash
curl -sf http://127.0.0.1:18080/admin/snapshot
```

```powershell
Invoke-RestMethod -Uri http://127.0.0.1:18080/admin/snapshot
```

**Expected output:** JSON containing a `"version":"v1.5.1"` field (or whatever the current build's version is). The admin surface is auth-exempt by design (loopback-bound and behind the same `127.0.0.1` listener as `/health` — see admin Phase 6.1). This is the one place that confirms wrapper, binary, and admin surface all agree on the version — useful after an upgrade to catch a stale `bin/` that did not get overwritten.

### Optional: pool serving-health probe

```bash
curl -sf http://127.0.0.1:18080/health/pool
```

**Expected output:** JSON shape `{"pool":{"size":<N>,"alive":<N>,"busy":<N>,"healthy":true,...}}`. `healthy: false` when `size > 0 && alive == 0` — i.e. the pool was configured but every worker died and failed to respawn. Use this as the single-field "page me" signal.

---

## Where to go next

- **Day-2 operator tasks** — enabling PII, rotating the hash key, changing the listen port, filtering the hook chain, tailing logs, troubleshooting beyond install: see [`README.md`](./README.md) (the operator quickstart, also shipped in this archive).
- **Deeper developer-facing operations** — full subcommand reference table, log file role breakdown, rotation contract, log rotation knobs: see `docs/operating.md` in the repo (not shipped in the archive).
- **Publishing a build** — the publisher script and its dry-run / verify / partial-rollback contract: covered in `README.md` under "Publishing a build".
