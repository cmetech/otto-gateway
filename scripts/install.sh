#!/bin/sh
# scripts/install.sh — one-liner installer for Gateway (macOS/Linux).
#   curl -fsSL https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.sh | sh
#
# Layout (post de-brand relayout):
#   GW_INSTALL_DIR  code (binaries, scripts) — replaceable on upgrade.
#     macOS:  $HOME/Library/Application Support/Gateway
#     Linux:  ${XDG_DATA_HOME:-$HOME/.local/share}/gateway
#   GW_HOME         config (.env, logs, state) — precious, never overwritten.
#     default $HOME/.gw
#
# Env overrides:
#   GW_INSTALL_DIR install dir             (default per-OS, see above)
#   GW_HOME        config dir              (default $HOME/.gw)
#   GW_VERSION     release tag to install (default: latest GitHub release)
#   GW_BASE_URL    release asset base     (default GitHub releases download URL)
#   GW_API_URL     latest-release API URL (default GitHub API)
set -eu

REPO="cmetech/otto-gateway"
GW_BASE_URL="${GW_BASE_URL:-https://github.com/${REPO}/releases/download}"
GW_API_URL="${GW_API_URL:-https://api.github.com/repos/${REPO}/releases/latest}"
GW_HOME="${GW_HOME:-$HOME/.gw}"

# Per-OS default code install dir. Overridable via $GW_INSTALL_DIR. The
# platform-specific default is only known after detect_platform() runs (it
# needs PLATFORM_OS), so resolution happens at the top of main() rather than
# here at parse time — mirrors how PLATFORM/VERSION are resolved lazily too.
resolve_install_dir() {
    if [ -n "${GW_INSTALL_DIR:-}" ]; then
        return 0
    fi
    if [ "$PLATFORM_OS" = "darwin" ]; then
        GW_INSTALL_DIR="$HOME/Library/Application Support/Gateway"
    else
        GW_INSTALL_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/gateway"
    fi
}

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
    if [ -n "${GW_VERSION:-}" ]; then
        VERSION="$GW_VERSION"
        return 0
    fi
    VERSION=$(fetch_stdout "$GW_API_URL" \
        | grep '"tag_name"' \
        | head -n 1 \
        | sed -n -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p')
    [ -n "$VERSION" ] || err "could not resolve latest release from $GW_API_URL (set GW_VERSION to override)."
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

# migrate_legacy_config — best-effort, idempotent migration of ~/.otto-gw*
# config into $GW_HOME, run BEFORE the config-existence check below so that a
# legacy AUTH_TOKEN etc. is already sitting at $GW_HOME/.env by the time we
# decide whether `gw init --non-interactive` needs to run. On a first-ever
# install there is nothing yet extracted to source from, so we try a
# repo-local copy first (present when this script is run from a checked-out
# repo) and fall back to the copy inside the just-extracted install dir
# (present on every real release install, since it ships at
# scripts/lib/migrate.sh in the tarball). Either miss is silently tolerated —
# gw_migrate_from_otto is advisory only, never required for a fresh install.
migrate_legacy_config() {
    # shellcheck source=/dev/null
    . "$(dirname "$0")/lib/migrate.sh" 2>/dev/null || true
    # shellcheck source=/dev/null
    . "$GW_INSTALL_DIR/scripts/lib/migrate.sh" 2>/dev/null || true
    if command -v gw_migrate_from_otto >/dev/null 2>&1; then
        gw_migrate_from_otto
    fi
}

# cleanup_old_autostart — the old OTTO Tray autostart LaunchAgent
# (io.cmetech.otto-tray) is superseded by the de-branded
# io.cmetech.gateway-tray label; autostart is opt-in via the tray now, so we
# do not install a replacement here — just unload + remove the stale one so
# operators upgrading from an OTTO Gateway install don't end up with a dead
# LaunchAgent pointing at a binary path that no longer exists.
cleanup_old_autostart() {
    [ "$PLATFORM_OS" = "darwin" ] || return 0
    old_plist="$HOME/Library/LaunchAgents/io.cmetech.otto-tray.plist"
    if [ -f "$old_plist" ]; then
        info "Removing legacy OTTO Tray autostart entry ..."
        launchctl unload "$old_plist" >/dev/null 2>&1 || true
        rm -f "$old_plist"
        ok "removed legacy autostart: $old_plist"
    fi
}

main() {
    detect_platform
    resolve_install_dir
    resolve_version

    archive="otto_gateway-${PLATFORM}-${VERSION}.tar.gz"
    sums="SHA256SUMS-${VERSION}.txt"

    printf 'Installing Gateway %s (%s) -> %s\n\n' "$VERSION" "$PLATFORM" "$GW_INSTALL_DIR"

    tmp=$(mktemp -d "${TMPDIR:-/tmp}/gateway-install.XXXXXX")
    trap 'rm -rf "$tmp"' EXIT INT TERM

    info "Downloading $archive ..."
    download "${GW_BASE_URL}/${VERSION}/${archive}" "$tmp/$archive"
    download "${GW_BASE_URL}/${VERSION}/${sums}" "$tmp/$sums"

    info "Verifying checksum ..."
    verify_checksum "$tmp/$archive" "$tmp/$sums" "$archive"
    ok "checksum verified"

    if [ -x "$GW_INSTALL_DIR/scripts/gw" ]; then
        info "Stopping running gateway (if any) ..."
        "$GW_INSTALL_DIR/scripts/gw" stop >/dev/null 2>&1 || true
    fi

    # Stop a running gateway-tray before extraction — the .app bundle's
    # MacOS/gateway-tray copy is in-use otherwise. killall is silent when
    # nothing is running and is in /usr/bin on every supported macOS.
    if [ "$PLATFORM_OS" = "darwin" ]; then
        killall gateway-tray 2>/dev/null || true
        # Give launchd a moment to reap before we overwrite the bundle.
        sleep 1
    fi

    info "Extracting to $GW_INSTALL_DIR ..."
    mkdir -p "$GW_INSTALL_DIR"
    tar -xzf "$tmp/$archive" -C "$GW_INSTALL_DIR" --strip-components=1

    # Run the config migration right after extraction (so the shipped
    # lib/migrate.sh is available to source) and before the config-existence
    # check just below, so a migrated ~/.gw/.env is already in place when we
    # decide whether `gw init` needs to run.
    migrate_legacy_config

    if [ "$PLATFORM_OS" = "darwin" ]; then
        xattr -d com.apple.quarantine "$GW_INSTALL_DIR/bin/gateway" 2>/dev/null || true
        # gateway-tray ships only in the darwin and windows tarballs;
        # silently no-op if the binary is absent (e.g. on linux installs).
        if [ -f "$GW_INSTALL_DIR/bin/gateway-tray" ]; then
            xattr -d com.apple.quarantine "$GW_INSTALL_DIR/bin/gateway-tray" 2>/dev/null || true
            # Build a minimal .app bundle wrapper so `open` reaches the
            # tray as a proper menu-bar agent. Running a plain Mach-O
            # binary via `open` falls back to Terminal.app — the binary
            # then can't reliably take an NSStatusBar item and the
            # terminal window stays open. LSUIElement=true keeps the
            # app out of the Dock and Cmd-Tab list (menu-bar-only).
            #
            # Why we COPY (not symlink) the binary: macOS's runtime
            # signature verification treats Contents/MacOS/<exe> as the
            # bundle's canonical executable and rejects symlinks for it.
            # The published binary was adhoc-signed standalone, so when
            # the bundle wraps it, the embedded signature reports
            # `Info.plist=not bound` and NSStatusBar registration
            # silently no-ops — the v2.0.3 symptom.
            #
            # Why we RE-SIGN the bundle: after the Info.plist is in
            # place, an adhoc codesign --deep --force binds the
            # signature to the bundle's resources (Info.plist + the
            # MacOS/ payload). Without this rebind the Cocoa runtime
            # refuses to attach a menu-bar item.
            app_dir="$GW_INSTALL_DIR/Gateway Tray.app"
            rm -rf "$app_dir"
            mkdir -p "$app_dir/Contents/MacOS"
            cp "$GW_INSTALL_DIR/bin/gateway-tray" "$app_dir/Contents/MacOS/gateway-tray"
            chmod +x "$app_dir/Contents/MacOS/gateway-tray"
            cat > "$app_dir/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleIdentifier</key>
    <string>io.cmetech.gateway-tray</string>
    <key>CFBundleName</key>
    <string>Gateway Tray</string>
    <key>CFBundleExecutable</key>
    <string>gateway-tray</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleVersion</key>
    <string>${VERSION#v}</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION#v}</string>
    <key>LSUIElement</key>
    <true/>
    <key>LSMinimumSystemVersion</key>
    <string>10.13</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSPrincipalClass</key>
    <string>NSApplication</string>
</dict>
</plist>
PLIST
            xattr -dr com.apple.quarantine "$app_dir" 2>/dev/null || true
            # Re-sign the bundle ad-hoc so the signature is bound to
            # the Info.plist and the in-bundle payload. Silently no-op
            # if codesign is missing (e.g. Xcode CLT not installed) —
            # the bundle still functions for users who manually grant
            # Gatekeeper consent, but flag it so the operator knows.
            if command -v codesign >/dev/null 2>&1; then
                codesign --sign - --force --deep --options runtime "$app_dir" 2>/dev/null \
                    && ok "signed Gateway Tray.app (adhoc)" \
                    || warn "codesign on Gateway Tray.app failed — first-launch may need manual Gatekeeper approval"
            else
                warn "codesign not found — skipping ad-hoc sign of Gateway Tray.app"
            fi
        fi
    fi

    if command -v kiro-cli >/dev/null 2>&1; then
        ok "kiro-cli found at $(command -v kiro-cli)"
    else
        warn "kiro-cli not found on PATH."
        warn "  The gateway returns 503 on chat requests until kiro-cli is installed."
        warn "  Install it per your team's instructions, or set KIRO_CMD in ~/.gw/.env."
    fi

    preserved_env=0
    if [ -f "$GW_HOME/.env" ]; then
        info "Existing config found — preserving it (skipping init)."
        preserved_env=1
    else
        info "Writing default config (no auth, 127.0.0.1:18080, all hooks, PII redaction=encrypt, NER=on, chat-trace off) ..."
        "$GW_INSTALL_DIR/scripts/gw" init --non-interactive
    fi

    bindir="$HOME/.local/bin"
    mkdir -p "$bindir"

    # Remove the legacy `otto-gw` PATH symlink — superseded by `gw` below.
    # Only touch it if it's actually our symlink (or missing); a real file
    # named otto-gw left by the operator is untouched, same caution as the
    # `gw` link guard just below.
    legacy_link="$bindir/otto-gw"
    if [ -L "$legacy_link" ]; then
        rm -f "$legacy_link"
        ok "removed legacy $legacy_link"
    fi

    link="$bindir/gw"
    if [ -e "$link" ] && [ ! -L "$link" ]; then
        warn "$link exists and is not a symlink — leaving it. Run $GW_INSTALL_DIR/scripts/gw directly."
    else
        ln -sf "$GW_INSTALL_DIR/scripts/gw" "$link"
        ok "linked $link -> $GW_INSTALL_DIR/scripts/gw"
    fi

    cleanup_old_autostart

    printf '\n'
    ok "Gateway $VERSION installed to $GW_INSTALL_DIR"
    case ":$PATH:" in
        *":$bindir:"*) cmd="gw" ;;
        *)
            cmd="$GW_INSTALL_DIR/scripts/gw"
            warn "$bindir is not on PATH. Add to your shell rc to use 'gw' directly:"
            warn "    export PATH=\"\$HOME/.local/bin:\$PATH\""
            ;;
    esac
    printf '\nNext steps:\n'
    if [ "$preserved_env" = "1" ]; then
        printf '  %s upgrade-env --dry-run   # check for new env keys this build added (then run without --dry-run to apply)\n' "$cmd"
    fi
    printf '  %s start     # launch the gateway\n' "$cmd"
    printf '  %s status    # verify it is up\n' "$cmd"
    if [ -d "$GW_INSTALL_DIR/Gateway Tray.app" ]; then
        printf "  open '%s/Gateway Tray.app'   # re-launch the menu-bar app\n" "$GW_INSTALL_DIR"
    fi
    printf '  curl -sf http://127.0.0.1:18080/health\n'

    # Auto-start the menu-bar tray after install. A running instance was already
    # stopped before extraction ('killall gateway-tray', above), so this is the
    # "start" half of stop-then-start: the tray ends up running whether or not
    # it was before. Best-effort — never fail the install if 'open' errors.
    if [ -d "$GW_INSTALL_DIR/Gateway Tray.app" ]; then
        info "Starting Gateway Tray ..."
        open "$GW_INSTALL_DIR/Gateway Tray.app" 2>/dev/null || true
    fi
}

main "$@"
