# OTTO Gateway — One-Liner Install — Design

**Date:** 2026-05-31
**Status:** Approved (brainstorming complete, ready for implementation plan)

## Goal

Replace the multi-step manual install (download archive → extract → strip
quarantine / run `setup.bat` → install kiro-cli → run `init` → start) with a
single copy-paste command per platform, in the style of oh-my-pi / rustup /
Scoop:

**macOS / Linux:**
```bash
curl -fsSL https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.sh | sh
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.ps1 | iex
```

The installed gateway must come up with sensible defaults — **no auth, listen
on `127.0.0.1:18080`, all four hooks enabled, chat-trace disabled** — which are
already the cold-start defaults of the existing `init` subcommand, so the
installer does not introduce a new config surface.

A short vanity URL (e.g. `otto.sh/install`) is explicitly **out of scope** for
v1; it can be layered on later as a redirect to the raw URLs above.

## Decisions (from brainstorming)

| Decision | Choice | Rationale |
| --- | --- | --- |
| Download source | **GitHub Releases** on `cmetech/otto-gateway` | oh-my-pi parity; `releases/latest` resolves the current version automatically. |
| Public access | **Make `cmetech/otto-gateway` public** | Anonymous `curl`/`irm` fetch of both the raw install script and release assets requires a public repo (GitHub will not expose releases on a private repo). |
| Install location | Default `~/.otto-gw` (`%USERPROFILE%\.otto-gw`), `OTTO_HOME` env override | Fixed home like oh-my-pi, but redirectable for CI / fleet installs. |
| Post-install | `init --non-interactive` (first run only) + print `start` command | Hands the user a configured-but-not-running gateway; no surprise background process. |
| kiro-cli | **Detect + warn**, never block | Gateway 503s without it, but kiro-cli distribution is per-team; installer cannot fetch it. |
| `otto-gw` on PATH | **Symlink into PATH** | POSIX: symlink into `~/.local/bin`. Windows: add `$OTTO_HOME\scripts` to user PATH, command resolves to `otto-gw.bat`. |
| Release publishing | **CI workflow on `v*` tag push** | `make package-all` → `gh release create`; automatic and reproducible. |

## Deliverables

1. `scripts/install.sh` — POSIX installer (macOS arm64/amd64, Linux amd64).
2. `scripts/install.ps1` — Windows installer (amd64).
3. `.github/workflows/release.yml` — builds the release set and publishes a
   GitHub Release on `v*` tag push.
4. README + `docs/INSTALL.md` updates documenting the one-liner as the
   recommended path (manual extract stays documented as the fallback).
5. **Operational action (not code):** flip `cmetech/otto-gateway` to public.

## Component 1 — `scripts/install.sh` (POSIX)

`set -eu`, POSIX `sh`-compatible (no bashisms — the macOS/Linux target shells
vary). Behaves as a single linear flow:

1. **Detect OS/arch.** `uname -s` → `darwin` | `linux`; `uname -m` → map
   `arm64`/`aarch64` → `arm64`, `x86_64`/`amd64` → `amd64`. Validate against the
   supported set (`darwin-arm64`, `darwin-amd64`, `linux-amd64`). Anything else
   → error with the list of supported targets and a pointer to the manual
   install in `INSTALL.md`.
2. **Resolve version.** `OTTO_VERSION` env override; else GET
   `https://api.github.com/repos/cmetech/otto-gateway/releases/latest` and parse
   `tag_name` (grep/sed — no `jq` dependency). Empty result → actionable error
   (no releases yet / network).
3. **Resolve install dir.** `OTTO_HOME` env override; else `$HOME/.otto-gw`.
4. **Download** to a `mktemp -d` working dir (trap-cleaned on exit):
   - `otto_gateway-<os>-<arch>-<tag>.tar.gz`
   - `SHA256SUMS-<tag>.txt`
   from `https://github.com/cmetech/otto-gateway/releases/download/<tag>/<asset>`.
   Prefer `curl -fsSL`, fall back to `wget -qO` if curl absent.
5. **Verify checksum.** Filter the matching row out of `SHA256SUMS-<tag>.txt`
   and run `shasum -a 256 -c` (fall back to `sha256sum`). Mismatch → abort
   before touching `$OTTO_HOME`.
6. **Stop any running gateway** (best-effort): if `$OTTO_HOME/scripts/otto-gw`
   already exists, run `"$OTTO_HOME/scripts/otto-gw" stop || true`.
7. **Extract** into `$OTTO_HOME` with `tar -xzf <archive> --strip-components=1`
   (`mkdir -p "$OTTO_HOME"` first). Stripping the leading `otto_gateway/` puts
   the wrapper at `$OTTO_HOME/scripts/otto-gw`. Overlay extraction preserves
   `logs/` and any project-local `.env.otto-gw` on upgrade.
8. **macOS unlock.** On `darwin`: `xattr -d com.apple.quarantine
   "$OTTO_HOME/bin/otto-gateway" 2>/dev/null || true`.
9. **Detect kiro-cli.** `command -v kiro-cli`: if found, print
   `✓ kiro-cli found at <path>` (the wrapper auto-detects it on `start`); if
   missing, print a clear multi-line warning that the gateway will return 503
   on chat requests until kiro-cli is installed, with a pointer to team docs.
   Never abort.
10. **First-run init only.** If neither `$HOME/.otto-gw.env` nor
    `$OTTO_HOME/.env.otto-gw` exists, run
    `"$OTTO_HOME/scripts/otto-gw" init --non-interactive` (writes
    `$HOME/.otto-gw.env`, mode `0600`, with the documented cold-start defaults).
    If a config already exists (upgrade), skip init and print that existing
    config was preserved.
11. **Expose `otto-gw`.** `mkdir -p "$HOME/.local/bin"`; symlink
    `$HOME/.local/bin/otto-gw` → `$OTTO_HOME/scripts/otto-gw` (replace an
    existing symlink; never clobber a non-symlink file — warn instead). If
    `$HOME/.local/bin` is not on `$PATH`, print the exact line to add to the
    shell rc (do **not** edit rc files — that was an explicitly rejected
    option).
12. **Success banner.** Print: installed version + location, kiro-cli status,
    `otto-gw start` to launch, `otto-gw status` to verify, and the
    `curl http://127.0.0.1:18080/health` check.

## Component 2 — `scripts/install.ps1` (Windows)

Mirrors the POSIX flow with Windows idioms. The execution-policy story is the
load-bearing part of this component.

### Why the one-liner is execution-policy-immune

`irm <url> | iex` never executes a `.ps1` *file*: `Invoke-RestMethod` fetches
the script text and `Invoke-Expression` runs it as commands in the current
session. PowerShell execution policy gates script *files* and module loading,
not piped/interactive commands — so the installer runs on a fresh machine at
the default `Restricted` policy with no prep, no `-ExecutionPolicy Bypass`, no
`.bat` bootstrap.

### Folding `setup.bat` into the installer

The *installed* wrapper (`otto-gw.ps1`) is a `.ps1` on disk, so the installer
must do what `setup.bat` does today, once, so later `otto-gw` calls work:

1. `Set-ExecutionPolicy RemoteSigned -Scope CurrentUser -Force` (built-in
   cmdlet; runs fine from `iex`). Skip if effective policy is already permissive
   (`RemoteSigned`/`Unrestricted`/`Bypass`), matching `setup.bat`'s logic.
2. `Get-ChildItem -Recurse -File | Unblock-File` over `$OTTO_HOME` to strip any
   Mark-of-the-Web Zone.Identifier streams.

### Belt-and-suspenders for GPO-locked machines

Where Group Policy pins execution policy at `LocalMachine`/`MachinePolicy`,
`Set-ExecutionPolicy -Scope CurrentUser` is silently overridden. The answer is
the existing **`otto-gw.bat`** dispatcher, which calls
`powershell -NoProfile -ExecutionPolicy Bypass -File otto-gw.ps1` and is immune
because cmd.exe is not subject to execution policy. Therefore the installer puts
**`$OTTO_HOME\scripts` on the user PATH so that the `otto-gw` command resolves
to `otto-gw.bat`** (PATHEXT prioritizes `.bat`/`.cmd` over `.ps1`). Result:
`otto-gw start` works under any policy, including GPO-locked, with zero user
intervention. `setup.bat` keeps shipping for the manual-extract path but is
redundant for one-liner users.

### Flow

1. **Arch.** amd64 (the only Windows target built today); map `$env:PROCESSOR_ARCHITECTURE`, error on non-amd64.
2. **Resolve version.** `$env:OTTO_VERSION` else `Invoke-RestMethod` against the
   `releases/latest` API, read `.tag_name`.
3. **Resolve install dir.** `$env:OTTO_HOME` else `$env:USERPROFILE\.otto-gw`.
4. **Download** `otto_gateway-windows-amd64-<tag>.zip` + `SHA256SUMS-<tag>.txt`
   to a temp dir via `Invoke-WebRequest`.
5. **Verify** with `Get-FileHash -Algorithm SHA256` against the matching row;
   abort on mismatch.
6. **Stop existing** (best-effort) if `$OTTO_HOME\scripts\otto-gw.bat` exists.
7. **Extract.** `Expand-Archive` to a temp dir, then move the contents of the
   inner `otto_gateway\` up into `$OTTO_HOME` (Expand-Archive has no
   strip-components). Overlay; preserve `logs\` / `.env.otto-gw`.
8. **Execution policy + MOTW** (the two `setup.bat` steps above).
9. **Detect kiro-cli** via `Get-Command kiro-cli`; same warn-don't-block
   behavior as POSIX.
10. **First-run init only.** If `$env:USERPROFILE\.otto-gw.env` (and project
    `.env.otto-gw`) absent, run `otto-gw.bat init -NonInteractive`. Skip on
    upgrade.
11. **PATH.** If `$OTTO_HOME\scripts` not on the user PATH
    (`[Environment]::GetEnvironmentVariable('Path','User')`), append it via
    `SetEnvironmentVariable(...,'User')` and note that a new shell is needed to
    pick it up.
12. **Success banner** — same content as POSIX, Windows command forms.

## Component 3 — `.github/workflows/release.yml`

Triggered on `push: tags: ['v*']`. Single job on `ubuntu-latest`:

1. `actions/checkout` with `fetch-depth: 0` (so `git describe` sees the tag).
2. `actions/setup-go` pinned to the repo's Go version.
3. `make package-all` — builds all four archives + `SHA256SUMS-<tag>.txt` into
   `dist/`. `VERSION` derives from the tag via the existing Makefile
   `git describe` default.
4. `gh release create "$GITHUB_REF_NAME"` uploading the four archives + the
   SHA256SUMS file as assets, with `--generate-notes`. Uses the workflow's
   `GITHUB_TOKEN` (needs `contents: write` permission).

Note: macOS ad-hoc codesign in `package-darwin-*` is skipped on the Linux
runner (the Makefile already guards `codesign_adhoc` with a `command -v`
check), matching today's behavior — the darwin binaries are unsigned from CI,
and the installer's `xattr -d` quarantine strip is the operative macOS unlock.

## Defaults shipped (unchanged from existing `init` cold start)

| Field | Default |
| --- | --- |
| Auth | disabled (token pregenerated but commented out) |
| Listen address | `127.0.0.1:18080` |
| Hooks | all four enabled |
| Chat-trace | disabled |
| PII redaction | off |

## Cross-cutting properties

- **Idempotent / upgrade-safe.** Re-running upgrades in place: stop → overlay
  extract → preserve `.env` + `logs/` → skip init. Never clobbers config or
  re-mints secrets.
- **Reversible / uninstall.** `otto-gw stop`; `rm -rf ~/.otto-gw` (or
  `$OTTO_HOME`); remove the `~/.local/bin/otto-gw` symlink (POSIX) or the PATH
  entry (Windows); `rm ~/.otto-gw.env`. Matches the existing INSTALL.md
  uninstall section; document the symlink/PATH removal as the one new step.
- **Failure atomicity.** All validation (arch, version resolution, download,
  checksum) happens before `$OTTO_HOME` is touched. A failed download or
  checksum leaves any existing install intact.
- **No new auth surface.** Defaults identical to today's `init`.

## Testing approach

- **install.sh:** `shellcheck` clean. Manual smoke on macOS arm64 (primary
  target): fresh install, re-run (upgrade path, config preserved), `OTTO_HOME`
  override, missing-kiro warning, checksum-mismatch abort (tamper a local
  asset). Lint-level check that it's POSIX `sh` (no bashisms).
- **install.ps1:** `PSScriptAnalyzer` clean. Manual smoke on a Windows VM at
  default `Restricted` policy: one-liner runs, `otto-gw start` works afterward,
  PATH resolves to `.bat`, upgrade preserves config.
- **release.yml:** validated by pushing a throwaway pre-release tag (e.g.
  `v0.0.0-test`) to confirm the asset set uploads with the expected names, then
  deleting it. The existing CI `publish-dry-run` job already guards
  `make package-all` artifact naming.

## Out of scope (v1)

- Vanity short URL (`otto.sh/install`).
- Installing kiro-cli (detect + warn only).
- Linux arm64 / Windows arm64 targets (not built today).
- macOS notarization (still relies on `xattr -d`, per existing INSTALL.md).
- Editing shell rc files (rejected; print the PATH line instead).
- A package-manager presence (Homebrew tap, winget, Scoop manifest) — possible
  future work, not this change.
