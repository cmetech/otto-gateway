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
        | sed -n -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p')
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
        # otto-tray ships only in the darwin and windows tarballs;
        # silently no-op if the binary is absent (e.g. on linux installs).
        if [ -f "$OTTO_HOME/bin/otto-tray" ]; then
            xattr -d com.apple.quarantine "$OTTO_HOME/bin/otto-tray" 2>/dev/null || true
            # Build a minimal .app bundle wrapper so `open` reaches the
            # tray as a proper menu-bar agent. Running a plain Mach-O
            # binary via `open` falls back to Terminal.app — the binary
            # then can't reliably take an NSStatusBar item and the
            # terminal window stays open. LSUIElement=true keeps the
            # app out of the Dock and Cmd-Tab list (menu-bar-only).
            # The MacOS/ exec is a symlink to avoid duplicating the
            # ~9 MB binary on disk.
            app_dir="$OTTO_HOME/OTTO Tray.app"
            rm -rf "$app_dir"
            mkdir -p "$app_dir/Contents/MacOS"
            ln -sf "$OTTO_HOME/bin/otto-tray" "$app_dir/Contents/MacOS/otto-tray"
            cat > "$app_dir/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleIdentifier</key>
    <string>io.cmetech.otto-tray</string>
    <key>CFBundleName</key>
    <string>OTTO Tray</string>
    <key>CFBundleExecutable</key>
    <string>otto-tray</string>
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
</dict>
</plist>
PLIST
            xattr -dr com.apple.quarantine "$app_dir" 2>/dev/null || true
        fi
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
        info "Writing default config (no auth, 127.0.0.1:18080, all hooks, PII redaction=encrypt, NER=on, chat-trace off) ..."
        "$OTTO_HOME/scripts/otto-gw" init --non-interactive
    fi

    bindir="$HOME/.local/bin"
    mkdir -p "$bindir"
    link="$bindir/otto-gw"
    if [ -e "$link" ] && [ ! -L "$link" ]; then
        warn "$link exists and is not a symlink — leaving it. Run $OTTO_HOME/scripts/otto-gw directly."
    else
        ln -sf "$OTTO_HOME/scripts/otto-gw" "$link"
        ok "linked $link -> $OTTO_HOME/scripts/otto-gw"
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
    if [ -d "$OTTO_HOME/OTTO Tray.app" ]; then
        printf "  open '%s/OTTO Tray.app'   # or, launch the menu-bar app\n" "$OTTO_HOME"
    fi
    printf '  curl -sf http://127.0.0.1:18080/health\n'
}

main "$@"
