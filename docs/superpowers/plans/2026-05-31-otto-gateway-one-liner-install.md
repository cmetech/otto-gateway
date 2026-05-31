# OTTO Gateway One-Liner Install — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a single copy-paste install command per platform (`curl … | sh`, `irm … | iex`) that downloads, verifies, extracts, configures, and PATH-exposes OTTO Gateway with sensible defaults.

**Architecture:** Two self-contained installer scripts (`scripts/install.sh`, `scripts/install.ps1`) that pull a versioned archive from GitHub Releases, verify its SHA256, overlay-extract into `~/.otto-gw` (overridable via `OTTO_HOME`), run the existing `init --non-interactive` on first install, and symlink/PATH-expose the existing `otto-gw` wrapper. A tag-triggered CI workflow (`release.yml`) publishes the archives `make package-all` already produces as GitHub Release assets so the installers have something to download.

**Tech Stack:** POSIX `sh`, Windows PowerShell 5.1+, GitHub Actions, the existing Go `Makefile` (`make package-all`) and `otto-gw`/`otto-gw.ps1`/`otto-gw.bat` wrappers.

**Spec:** `docs/superpowers/specs/2026-05-31-otto-gateway-one-liner-install-design.md`

---

## File Structure

- **Create** `scripts/install.sh` — POSIX installer (macOS arm64/amd64, Linux amd64). Single linear flow: detect → resolve version → download → verify → extract → unlock → detect kiro → first-run init → symlink → banner.
- **Create** `scripts/install.ps1` — Windows installer (amd64). Same flow plus folding `setup.bat`'s execution-policy + MOTW handling, and PATH-exposing `otto-gw.bat`.
- **Create** `tests/install/smoke_posix.sh` — local end-to-end smoke harness that serves the real `dist/` archives over `python3 -m http.server` and runs `install.sh` against a temp `HOME`/`OTTO_HOME`. The only automated test; everything else is lint + manual VM smoke.
- **Create** `.github/workflows/release.yml` — `v*`-tag-triggered release publisher.
- **Modify** `README.md` — add the one-liner as the recommended install path.
- **Modify** `docs/INSTALL.md` — add a one-liner section above the manual checklists; note the new symlink/PATH uninstall step.

**Operational prerequisite (not a code task):** flip `cmetech/otto-gateway` to public so the raw script URL and release assets fetch anonymously. The one-liners do not work until this is done.

---

## Task 1: POSIX installer (`scripts/install.sh`)

**Files:**
- Create: `scripts/install.sh`

- [ ] **Step 1: Write the full installer script**

Create `scripts/install.sh` with exactly this content:

```sh
#!/bin/sh
# scripts/install.sh — one-liner installer for OTTO Gateway (macOS/Linux).
#   curl -fsSL https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.sh | sh
#
# Env overrides:
#   OTTO_HOME      install dir            (default $HOME/.otto-gw)
#   OTTO_VERSION   release tag to install (default: latest GitHub release)
#   OTTO_BASE_URL  release asset base     (default GitHub releases download URL)
#   OTTO_API_URL   latest-release API URL (default GitHub API)
set -eu

REPO="cmetech/otto-gateway"
OTTO_BASE_URL="${OTTO_BASE_URL:-https://github.com/${REPO}/releases/download}"
OTTO_API_URL="${OTTO_API_URL:-https://api.github.com/repos/${REPO}/releases/latest}"
OTTO_HOME="${OTTO_HOME:-$HOME/.otto-gw}"

info() { printf '  %s\n' "$1"; }
ok()   { printf '\342\234\223 %s\n' "$1"; }
warn() { printf '! %s\n' "$1" >&2; }
err()  { printf 'Error: %s\n' "$1" >&2; exit 1; }

# download URL DEST — fetch URL to DEST via curl or wget.
download() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$2" "$1"
    else
        err "neither curl nor wget found; install one and re-run."
    fi
}

# fetch_stdout URL — print URL body to stdout.
fetch_stdout() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO - "$1"
    else
        err "neither curl nor wget found; install one and re-run."
    fi
}

detect_platform() {
    os=$(uname -s 2>/dev/null || echo unknown)
    arch=$(uname -m 2>/dev/null || echo unknown)
    case "$os" in
        Darwin) os=darwin ;;
        Linux)  os=linux ;;
        *) err "unsupported OS '$os'. Supported: macOS, Linux. See INSTALL.md for manual install." ;;
    esac
    case "$arch" in
        arm64|aarch64) arch=arm64 ;;
        x86_64|amd64)  arch=amd64 ;;
        *) err "unsupported arch '$arch'. Supported: arm64, amd64." ;;
    esac
    PLATFORM_OS="$os"
    PLATFORM="${os}-${arch}"
    case "$PLATFORM" in
        darwin-arm64|darwin-amd64|linux-amd64) ;;
        *) err "no release build for '$PLATFORM'. Supported: darwin-arm64, darwin-amd64, linux-amd64." ;;
    esac
}

resolve_version() {
    if [ -n "${OTTO_VERSION:-}" ]; then
        VERSION="$OTTO_VERSION"
        return 0
    fi
    VERSION=$(fetch_stdout "$OTTO_API_URL" \
        | grep '"tag_name"' \
        | head -n 1 \
        | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')
    [ -n "$VERSION" ] || err "could not resolve latest release from $OTTO_API_URL (set OTTO_VERSION to override)."
}

verify_checksum() {
    # verify_checksum ARCHIVE SUMS NAME
    expected=$(awk -v want="$3" '$2 == want {print $1}' "$2")
    [ -n "$expected" ] || err "no checksum row for $3 in $(basename "$2")."
    if command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "$1" | awk '{print $1}')
    elif command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "$1" | awk '{print $1}')
    else
        err "neither shasum nor sha256sum found; cannot verify download."
    fi
    [ "$expected" = "$actual" ] || err "checksum mismatch for $3 (expected $expected, got $actual)."
}

main() {
    detect_platform
    resolve_version

    archive="otto_gateway-${PLATFORM}-${VERSION}.tar.gz"
    sums="SHA256SUMS-${VERSION}.txt"

    printf 'Installing OTTO Gateway %s (%s) -> %s\n\n' "$VERSION" "$PLATFORM" "$OTTO_HOME"

    tmp=$(mktemp -d "${TMPDIR:-/tmp}/otto-install.XXXXXX")
    trap 'rm -rf "$tmp"' EXIT INT TERM

    info "Downloading $archive ..."
    download "${OTTO_BASE_URL}/${VERSION}/${archive}" "$tmp/$archive"
    download "${OTTO_BASE_URL}/${VERSION}/${sums}" "$tmp/$sums"

    info "Verifying checksum ..."
    verify_checksum "$tmp/$archive" "$tmp/$sums" "$archive"
    ok "checksum verified"

    if [ -x "$OTTO_HOME/scripts/otto-gw" ]; then
        info "Stopping running gateway (if any) ..."
        "$OTTO_HOME/scripts/otto-gw" stop >/dev/null 2>&1 || true
    fi

    info "Extracting to $OTTO_HOME ..."
    mkdir -p "$OTTO_HOME"
    tar -xzf "$tmp/$archive" -C "$OTTO_HOME" --strip-components=1

    if [ "$PLATFORM_OS" = "darwin" ]; then
        xattr -d com.apple.quarantine "$OTTO_HOME/bin/otto-gateway" 2>/dev/null || true
    fi

    if command -v kiro-cli >/dev/null 2>&1; then
        ok "kiro-cli found at $(command -v kiro-cli)"
    else
        warn "kiro-cli not found on PATH."
        warn "  The gateway returns 503 on chat requests until kiro-cli is installed."
        warn "  Install it per your team's instructions, or set KIRO_CMD in ~/.otto-gw.env."
    fi

    if [ -f "$HOME/.otto-gw.env" ] || [ -f "$OTTO_HOME/.env.otto-gw" ]; then
        info "Existing config found — preserving it (skipping init)."
    else
        info "Writing default config (no auth, 127.0.0.1:18080, all hooks, chat-trace off) ..."
        "$OTTO_HOME/scripts/otto-gw" init --non-interactive
    fi

    bindir="$HOME/.local/bin"
    mkdir -p "$bindir"
    link="$bindir/otto-gw"
    if [ -e "$link" ] && [ ! -L "$link" ]; then
        warn "$link exists and is not a symlink — leaving it. Run $OTTO_HOME/scripts/otto-gw directly."
    else
        ln -sf "$OTTO_HOME/scripts/otto-gw" "$link"
        ok "linked otto-gw -> $link"
    fi

    printf '\n'
    ok "OTTO Gateway $VERSION installed to $OTTO_HOME"
    case ":$PATH:" in
        *":$bindir:"*) cmd="otto-gw" ;;
        *)
            cmd="$OTTO_HOME/scripts/otto-gw"
            warn "$bindir is not on PATH. Add to your shell rc to use 'otto-gw' directly:"
            warn "    export PATH=\"\$HOME/.local/bin:\$PATH\""
            ;;
    esac
    printf '\nNext steps:\n'
    printf '  %s start     # launch the gateway\n' "$cmd"
    printf '  %s status    # verify it is up\n' "$cmd"
    printf '  curl -sf http://127.0.0.1:18080/health\n'
}

main "$@"
```

- [ ] **Step 2: Make it executable**

Run: `chmod 755 scripts/install.sh`

- [ ] **Step 3: Lint as POSIX sh, verify clean**

Run: `shellcheck -s sh scripts/install.sh`
Expected: no output, exit 0. If it flags `PLATFORM_OS`/`PLATFORM`/`VERSION` as unused (SC2034), that is a false positive — they cross function boundaries within the same shell; confirm they are genuinely referenced in `main` and only then add a targeted `# shellcheck disable` if shellcheck still complains. Do NOT silence real findings.

- [ ] **Step 4: Smoke the error paths (no network needed)**

Run: `OTTO_API_URL="http://127.0.0.1:9/none" sh scripts/install.sh` on a normal machine (port 9 refuses fast).
Expected: exits non-zero with an `Error:` line about resolving the version or the download — proves the failure path aborts before touching `$OTTO_HOME`.

- [ ] **Step 5: Commit**

```bash
git add scripts/install.sh
git commit -m "feat(install): POSIX one-liner installer (curl | sh)"
```

---

## Task 2: POSIX installer end-to-end smoke test

**Files:**
- Create: `tests/install/smoke_posix.sh`

This test exercises the full happy path (download → checksum → extract → quarantine strip → kiro detect → init → symlink → banner) against the **real** archives in `dist/`, served over a local HTTP server, with a fully isolated `HOME` and `OTTO_HOME` so it never touches your real config. Requires `dist/` to contain a built release set (run `make package` first if empty) and `python3`.

- [ ] **Step 1: Write the test harness**

Create `tests/install/smoke_posix.sh`:

```sh
#!/bin/sh
# tests/install/smoke_posix.sh — end-to-end smoke for scripts/install.sh.
# Serves the real dist/ archives over a local HTTP server and runs the
# installer against a throwaway HOME + OTTO_HOME. Never touches real config.
#
# Usage: sh tests/install/smoke_posix.sh [VERSION]
#   VERSION defaults to the host arch's newest dist archive tag.
set -eu

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo_root"

command -v python3 >/dev/null 2>&1 || { echo "need python3"; exit 1; }

# Host platform → archive name, matching install.sh's mapping.
os=$(uname -s); arch=$(uname -m)
case "$os" in Darwin) os=darwin ;; Linux) os=linux ;; *) echo "unsupported os $os"; exit 1 ;; esac
case "$arch" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) echo "unsupported arch $arch"; exit 1 ;; esac
platform="${os}-${arch}"

version="${1:-}"
if [ -z "$version" ]; then
    # newest matching archive in dist/
    f=$(ls -t dist/otto_gateway-${platform}-*.tar.gz 2>/dev/null | head -n 1) \
        || { echo "no dist/ archive for $platform — run 'make package' first"; exit 1; }
    version=$(basename "$f" | sed -E "s/otto_gateway-${platform}-(.+)\.tar\.gz/\1/")
fi
echo "smoke: platform=$platform version=$version"

archive="otto_gateway-${platform}-${version}.tar.gz"
sums="SHA256SUMS-${version}.txt"
[ -f "dist/$archive" ] || { echo "missing dist/$archive"; exit 1; }
[ -f "dist/$sums" ]    || { echo "missing dist/$sums"; exit 1; }

# Stage a release-shaped tree: <stage>/<version>/<assets> + latest.json.
stage=$(mktemp -d "${TMPDIR:-/tmp}/otto-smoke-stage.XXXXXX")
fakehome=$(mktemp -d "${TMPDIR:-/tmp}/otto-smoke-home.XXXXXX")
mkdir -p "$stage/$version"
cp "dist/$archive" "dist/$sums" "$stage/$version/"
printf '{"tag_name":"%s"}\n' "$version" > "$stage/latest.json"

# Serve and capture the port.
( cd "$stage" && exec python3 -m http.server 0 ) > "$stage/serve.log" 2>&1 &
srv=$!
trap 'kill "$srv" 2>/dev/null || true; rm -rf "$stage" "$fakehome"' EXIT INT TERM
# Wait for the port line, e.g. "Serving HTTP on :: port 54321".
port=""
i=0
while [ "$i" -lt 50 ]; do
    port=$(sed -n 's/.*port \([0-9][0-9]*\).*/\1/p' "$stage/serve.log" 2>/dev/null | head -n1)
    [ -n "$port" ] && break
    i=$((i+1)); sleep 0.1
done
[ -n "$port" ] || { echo "server did not start"; cat "$stage/serve.log"; exit 1; }
base="http://127.0.0.1:$port"

# Run the installer fully isolated.
env HOME="$fakehome" \
    OTTO_HOME="$fakehome/.otto-gw" \
    OTTO_BASE_URL="$base" \
    OTTO_API_URL="$base/latest.json" \
    sh scripts/install.sh

# Assertions.
wrapper="$fakehome/.otto-gw/scripts/otto-gw"
[ -x "$wrapper" ] || { echo "FAIL: wrapper not installed at $wrapper"; exit 1; }
[ -L "$fakehome/.local/bin/otto-gw" ] || { echo "FAIL: symlink not created"; exit 1; }
[ -f "$fakehome/.otto-gw.env" ] || { echo "FAIL: init did not write .otto-gw.env"; exit 1; }
ver_out=$("$wrapper" version 2>/dev/null || true)
echo "installed wrapper version reports: $ver_out"

# Re-run = upgrade path: must NOT clobber the existing env.
cp "$fakehome/.otto-gw.env" "$stage/env.before"
env HOME="$fakehome" OTTO_HOME="$fakehome/.otto-gw" \
    OTTO_BASE_URL="$base" OTTO_API_URL="$base/latest.json" \
    sh scripts/install.sh
cmp -s "$stage/env.before" "$fakehome/.otto-gw.env" \
    || { echo "FAIL: upgrade re-ran init and changed .otto-gw.env"; exit 1; }

echo "PASS: install + upgrade smoke"
```

- [ ] **Step 2: Make it executable**

Run: `chmod 755 tests/install/smoke_posix.sh`

- [ ] **Step 3: Ensure a dist build exists, then run the smoke test**

Run: `make package >/dev/null 2>&1 || true; sh tests/install/smoke_posix.sh`
Expected: ends with `PASS: install + upgrade smoke`. The installer's banner appears mid-run; the `version` line should match the dist tag. If kiro-cli is not installed you will see the kiro warning — that is expected and non-fatal.

- [ ] **Step 4: Lint the test**

Run: `shellcheck -s sh tests/install/smoke_posix.sh`
Expected: clean (exit 0).

- [ ] **Step 5: Commit**

```bash
git add tests/install/smoke_posix.sh
git commit -m "test(install): end-to-end smoke for POSIX installer against local server"
```

---

## Task 3: Windows installer (`scripts/install.ps1`)

**Files:**
- Create: `scripts/install.ps1`

Cannot be run on the macOS dev box; correctness rests on careful construction + `PSScriptAnalyzer` (if available) + manual VM smoke. The execution-policy handling mirrors `scripts/setup.bat:25-30`.

- [ ] **Step 1: Write the full installer script**

Create `scripts/install.ps1`:

```powershell
#Requires -Version 5.1
<#
  scripts/install.ps1 — one-liner installer for OTTO Gateway (Windows).
    irm https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.ps1 | iex

  Env overrides:
    OTTO_HOME      install dir            (default $env:USERPROFILE\.otto-gw)
    OTTO_VERSION   release tag            (default: latest GitHub release)
    OTTO_BASE_URL  release asset base     (default GitHub releases download URL)
    OTTO_API_URL   latest-release API URL (default GitHub API)
#>
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo     = 'cmetech/otto-gateway'
$BaseUrl  = if ($env:OTTO_BASE_URL) { $env:OTTO_BASE_URL } else { "https://github.com/$Repo/releases/download" }
$ApiUrl   = if ($env:OTTO_API_URL)  { $env:OTTO_API_URL }  else { "https://api.github.com/repos/$Repo/releases/latest" }
$OttoHome = if ($env:OTTO_HOME)     { $env:OTTO_HOME }     else { Join-Path $env:USERPROFILE '.otto-gw' }

function Info($m) { Write-Host "  $m" }
function Ok($m)   { Write-Host "[ok] $m" -ForegroundColor Green }
function Warn($m) { Write-Warning $m }
function Die($m)  { Write-Error $m; exit 1 }

# Arch — only amd64 Windows builds are published today.
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -ne 'AMD64') { Die "unsupported arch '$arch'. Only amd64 Windows builds are published." }
$platform = 'windows-amd64'

# Version.
if ($env:OTTO_VERSION) {
    $version = $env:OTTO_VERSION
} else {
    $rel = Invoke-RestMethod -UseBasicParsing -Uri $ApiUrl
    $version = $rel.tag_name
    if (-not $version) { Die "could not resolve latest release from $ApiUrl (set OTTO_VERSION to override)." }
}

$archive = "otto_gateway-$platform-$version.zip"
$sums    = "SHA256SUMS-$version.txt"
Write-Host "Installing OTTO Gateway $version ($platform) -> $OttoHome`n"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("otto-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
    Info "Downloading $archive ..."
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/$version/$archive" -OutFile (Join-Path $tmp $archive)
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/$version/$sums"    -OutFile (Join-Path $tmp $sums)

    Info "Verifying checksum ..."
    $expected = Select-String -Path (Join-Path $tmp $sums) -Pattern ([regex]::Escape($archive)) |
                ForEach-Object { ($_.Line -split '\s+')[0] } | Select-Object -First 1
    if (-not $expected) { Die "no checksum row for $archive in $sums." }
    $actual = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $archive)).Hash.ToLower()
    if ($expected.ToLower() -ne $actual) { Die "checksum mismatch for $archive (expected $expected, got $actual)." }
    Ok "checksum verified"

    $bat = Join-Path $OttoHome 'scripts\otto-gw.bat'
    if (Test-Path $bat) {
        Info "Stopping running gateway (if any) ..."
        & $bat stop *> $null
    }

    # Expand-Archive has no strip-components: expand to temp, move inner folder up.
    Info "Extracting to $OttoHome ..."
    $exdir = Join-Path $tmp 'extract'
    Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $exdir -Force
    $inner = Join-Path $exdir 'otto_gateway'
    New-Item -ItemType Directory -Path $OttoHome -Force | Out-Null
    Copy-Item -Path (Join-Path $inner '*') -Destination $OttoHome -Recurse -Force

    # The two setup.bat jobs: strip Mark-of-the-Web, then make CurrentUser policy permissive.
    Get-ChildItem -Path $OttoHome -Recurse -File | Unblock-File
    $permissive = @('RemoteSigned','Unrestricted','Bypass')
    if ($permissive -notcontains (Get-ExecutionPolicy)) {
        try {
            Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force
            Ok "set CurrentUser execution policy to RemoteSigned"
        } catch {
            Warn "could not set execution policy (Group Policy?). The otto-gw.bat command bypasses this per-invocation."
        }
    }

    if (Get-Command kiro-cli -ErrorAction SilentlyContinue) {
        Ok "kiro-cli found at $((Get-Command kiro-cli).Source)"
    } else {
        Warn "kiro-cli not found on PATH. The gateway returns 503 on chat requests until it is installed (or set KIRO_CMD in your .env)."
    }

    $userEnv = Join-Path $env:USERPROFILE '.otto-gw.env'
    $projEnv = Join-Path $OttoHome '.env.otto-gw'
    if ((Test-Path $userEnv) -or (Test-Path $projEnv)) {
        Info "Existing config found — preserving it (skipping init)."
    } else {
        Info "Writing default config (no auth, 127.0.0.1:18080, all hooks, chat-trace off) ..."
        & $bat init -NonInteractive
    }

    # PATH: expose otto-gw.bat (PATHEXT prioritizes .bat over .ps1, so 'otto-gw' resolves to the dispatcher).
    $scripts = Join-Path $OttoHome 'scripts'
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $userPath) { $userPath = '' }
    if (($userPath -split ';') -notcontains $scripts) {
        [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ';' + $scripts), 'User')
        Ok "added $scripts to your user PATH (open a new terminal to pick it up)"
    }

    Write-Host ""
    Ok "OTTO Gateway $version installed to $OttoHome"
    Write-Host "`nNext steps (new terminal):"
    Write-Host "  otto-gw start     # launch the gateway"
    Write-Host "  otto-gw status    # verify it is up"
    Write-Host "  Invoke-RestMethod http://127.0.0.1:18080/health"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
```

- [ ] **Step 2: Static-analyze if PSScriptAnalyzer is available**

Run (best-effort — the analyzer is usually only present on Windows/CI):
`pwsh -NoProfile -Command "if (Get-Module -ListAvailable PSScriptAnalyzer) { Invoke-ScriptAnalyzer -Path scripts/install.ps1 -Severity Warning,Error } else { 'PSScriptAnalyzer not installed — skipping' }"`
Expected: no Error/Warning diagnostics, or the "not installed" line. If `pwsh` is absent on the dev box, skip and rely on the manual VM smoke (Step 3).

- [ ] **Step 3: Record the manual VM smoke procedure (no execution here)**

This step is documentation of how a Windows operator verifies; it is not run on the dev box. Add nothing to the repo — just confirm the procedure is sound by re-reading the script:
1. On a fresh Windows VM at default `Restricted` policy, run `irm <raw-url>/scripts/install.ps1 | iex` (or, pre-publish, copy the file over and `Get-Content install.ps1 -Raw | iex`).
2. Confirm it completes without an execution-policy error.
3. Open a NEW terminal; run `otto-gw status` — it must resolve (to `otto-gw.bat`) and run.
4. Re-run the one-liner; confirm `.otto-gw.env` is preserved (init skipped).

- [ ] **Step 4: Commit**

```bash
git add scripts/install.ps1
git commit -m "feat(install): Windows one-liner installer (irm | iex) with execution-policy handling"
```

---

## Task 4: Release-publishing CI workflow (`.github/workflows/release.yml`)

**Files:**
- Create: `.github/workflows/release.yml`

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/release.yml`:

```yaml
name: release

on:
  push:
    tags:
      - 'v*'

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout (full history so git describe sees the tag)
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Build full release set
        run: make package-all

      - name: Publish GitHub Release
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          gh release create "$GITHUB_REF_NAME" \
            dist/otto_gateway-darwin-arm64-"$GITHUB_REF_NAME".tar.gz \
            dist/otto_gateway-darwin-amd64-"$GITHUB_REF_NAME".tar.gz \
            dist/otto_gateway-linux-amd64-"$GITHUB_REF_NAME".tar.gz \
            dist/otto_gateway-windows-amd64-"$GITHUB_REF_NAME".zip \
            dist/SHA256SUMS-"$GITHUB_REF_NAME".txt \
            --title "$GITHUB_REF_NAME" \
            --generate-notes
```

- [ ] **Step 2: Validate the YAML parses**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/release.yml')); print('yaml ok')"`
Expected: `yaml ok`. (actionlint is not installed on the dev box; YAML well-formedness plus the explicit asset names is the local gate. Real validation is Step 4.)

- [ ] **Step 3: Sanity-check the asset names match the Makefile**

Cross-read `Makefile:128` (`package-all`) and `Makefile:166-172`/`178-199`: the four archive names and `SHA256SUMS-$(VERSION).txt` must exactly match the `gh release create` arguments above, with `$GITHUB_REF_NAME` standing in for `$(VERSION)`. Confirm by eye; fix the workflow if any name drifts.

- [ ] **Step 4: Commit (real validation is the next published tag)**

```bash
git add .github/workflows/release.yml
git commit -m "ci: publish GitHub Release with archives on v* tag push"
```

After merge + repo made public, validate out-of-band by pushing a throwaway pre-release tag and confirming the asset set uploads, then deleting it:
```bash
git tag v0.0.0-test && git push origin v0.0.0-test
# inspect: gh release view v0.0.0-test
gh release delete v0.0.0-test --yes && git push --delete origin v0.0.0-test
```

---

## Task 5: Documentation — README + INSTALL.md

**Files:**
- Modify: `README.md` (the `## Running` section, around `README.md:99-123`)
- Modify: `docs/INSTALL.md` (top, after the table of contents; and the Uninstall section)

- [ ] **Step 1: Add the one-liner to README's Running section**

In `README.md`, immediately under the `## Running` heading (before the existing "Build the binary first…" text), insert:

```markdown
### Quick install (recommended)

**macOS / Linux:**

```bash
curl -fsSL https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.ps1 | iex
```

This downloads the latest release, verifies its checksum, installs to `~/.otto-gw`
(override with `OTTO_HOME`), writes a default config (no auth, `127.0.0.1:18080`,
all hooks, chat-trace off), and puts `otto-gw` on your PATH. Pin a version with
`OTTO_VERSION=v1.5.5`. Install `kiro-cli` separately — the gateway returns 503 on
chat requests without it. See [`docs/INSTALL.md`](docs/INSTALL.md) for the manual
archive install and per-OS detail.

---
```

Keep the existing developer "Build the binary first" content below it as the from-source path.

- [ ] **Step 2: Add a one-liner section to INSTALL.md**

In `docs/INSTALL.md`, after the table of contents block (after line ~30, before `## First-run checklist: macOS`), insert a new section:

```markdown
## Quick install (one-liner)

The fastest path on a machine with internet access. It performs the same steps
the per-OS checklists below do manually — download, checksum-verify, extract,
platform-unlock, first-run `init`, PATH-expose — in one command.

**macOS / Linux:**

```bash
curl -fsSL https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.ps1 | iex
```

Environment overrides: `OTTO_HOME` (install dir, default `~/.otto-gw` /
`%USERPROFILE%\.otto-gw`), `OTTO_VERSION` (release tag, default latest).

Re-running the command upgrades in place and preserves your `.env`. The Windows
installer also sets `CurrentUser` execution policy to `RemoteSigned` and unblocks
Mark-of-the-Web, and exposes `otto-gw` via the cmd dispatcher (`otto-gw.bat`) so it
works under any execution policy — `setup.bat` is not needed on the one-liner path.

Prefer the manual archive install (full control, air-gapped, or scripted fleet
rollout)? Use the per-OS checklists below.

---
```

- [ ] **Step 3: Add the symlink/PATH removal to the Uninstall section**

In `docs/INSTALL.md` Uninstall → macOS / Linux block (around `docs/INSTALL.md:413-426`), add a step after deleting the install folder:

```markdown
# 2b. Remove the PATH symlink created by the one-liner installer (skip if you installed manually).
rm -f ~/.local/bin/otto-gw
```

And in the Windows block (around `docs/INSTALL.md:432-443`), add:

```markdown
# 2b. Remove the scripts dir from your user PATH (one-liner installs only).
#     Settings > Environment Variables > User > Path > remove the ...\.otto-gw\scripts entry,
#     or in PowerShell:
$p = [Environment]::GetEnvironmentVariable('Path','User')
$keep = ($p -split ';' | Where-Object { $_ -and $_ -notlike '*\.otto-gw\scripts' }) -join ';'
[Environment]::SetEnvironmentVariable('Path', $keep, 'User')
```

- [ ] **Step 4: Verify docs render and links resolve**

Run: `grep -n "raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install" README.md docs/INSTALL.md`
Expected: at least two hits each (the `.sh` and `.ps1` URLs). Visually confirm the fenced code blocks are balanced (no stray triple-backtick).

- [ ] **Step 5: Commit**

```bash
git add README.md docs/INSTALL.md
git commit -m "docs: document the one-liner install in README and INSTALL.md"
```

---

## Self-Review

**1. Spec coverage:**
- One-liner UX (curl/irm) → Tasks 1, 3; documented in Task 5. ✓
- GitHub Releases as source + `releases/latest` resolution → `resolve_version` (Task 1), PS equivalent (Task 3); assets published by Task 4. ✓
- Public-repo prerequisite → called out as operational prerequisite (not code). ✓
- Install dir default + `OTTO_HOME` override → Task 1/3 `OTTO_HOME` handling. ✓
- First-run `init` only, defaults preserved on upgrade → Task 1/3 env-file check; asserted in Task 2 upgrade re-run. ✓
- kiro-cli detect + warn → Task 1/3. ✓
- PATH exposure (symlink POSIX / `otto-gw.bat` on Windows PATH) → Task 1/3. ✓
- Windows execution-policy + MOTW folded in → Task 3 Steps 1; explained in INSTALL.md (Task 5). ✓
- Checksum verification before touching install dir → `verify_checksum` ordered before extract (Task 1/3). ✓
- macOS quarantine strip → Task 1 `xattr -d`. ✓
- Idempotent/upgrade-safe + reversible uninstall → Task 2 upgrade assertion; Task 5 uninstall edits. ✓
- Release workflow on `v*` → Task 4. ✓

**2. Placeholder scan:** No TBD/TODO; every code step contains complete content. ✓

**3. Type/name consistency:** Archive name `otto_gateway-<platform>-<version>.tar.gz|.zip`, `SHA256SUMS-<version>.txt`, env vars `OTTO_HOME`/`OTTO_VERSION`/`OTTO_BASE_URL`/`OTTO_API_URL`, and the `${BASE}/${VERSION}/${asset}` URL layout are identical across install.sh, install.ps1, the smoke test, and release.yml. The smoke test stages assets under `<stage>/<version>/` to match that layout. ✓

**Note on TDD adaptation:** Classic test-first does not fit single-file shell/PowerShell installers. The discipline here is: lint gate after authoring (shellcheck/PSScriptAnalyzer/YAML parse), one real end-to-end automated test for the POSIX path (Task 2), and a documented manual VM smoke for Windows (which cannot run on the macOS dev box).
