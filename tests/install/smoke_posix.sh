#!/bin/sh
# tests/install/smoke_posix.sh — end-to-end smoke for scripts/install.sh.
# Serves the real dist/ archives over a local HTTP server and runs the
# installer against a throwaway HOME + GW_HOME. Never touches real config.
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
    # newest archive in dist/ that also has a matching SHA256SUMS file.
    # Use find+sort by mtime (newest first) to avoid iterating ls output.
    while IFS= read -r f; do
        v=$(basename "$f" | sed -E "s/otto_gateway-${platform}-(.+)\.tar\.gz/\1/")
        if [ -f "dist/SHA256SUMS-${v}.txt" ]; then
            version="$v"
            break
        fi
    done <<EOF
$(find dist -maxdepth 1 -name "otto_gateway-${platform}-*.tar.gz" \
    -exec ls -t {} + 2>/dev/null | head -n 20)
EOF
    [ -n "$version" ] || { echo "no dist/ archive+sums pair for $platform — run 'make package' first"; exit 1; }
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

# Find a free port, then serve on it.
port=$(python3 -c "import socket; s=socket.socket(); s.bind(('127.0.0.1',0)); print(s.getsockname()[1]); s.close()")
[ -n "$port" ] || { echo "could not find a free port"; exit 1; }
( cd "$stage" && exec python3 -m http.server "$port" --bind 127.0.0.1 ) > "$stage/serve.log" 2>&1 &
srv=$!
trap 'kill "$srv" 2>/dev/null || true; rm -rf "$stage" "$fakehome"' EXIT INT TERM
# Wait for the server to be ready.
i=0
while [ "$i" -lt 50 ]; do
    curl -sf "http://127.0.0.1:${port}/" >/dev/null 2>&1 && break
    i=$((i+1)); sleep 0.1
done
curl -sf "http://127.0.0.1:${port}/" >/dev/null 2>&1 || { echo "server did not start on port $port"; cat "$stage/serve.log"; exit 1; }
base="http://127.0.0.1:$port"

# Run the installer fully isolated. Two-anchor layout: GW_INSTALL_DIR
# (code) and GW_HOME (config) are both pinned under the fake HOME so the
# smoke test never touches the real user's install or config.
env HOME="$fakehome" \
    GW_INSTALL_DIR="$fakehome/.gw" \
    GW_HOME="$fakehome/.gw" \
    GW_BASE_URL="$base" \
    GW_API_URL="$base/latest.json" \
    sh scripts/install.sh

# Assertions.
wrapper="$fakehome/.gw/scripts/gw"
[ -x "$wrapper" ] || { echo "FAIL: wrapper not installed at $wrapper"; exit 1; }
[ -L "$fakehome/.local/bin/gw" ] || { echo "FAIL: symlink not created"; exit 1; }
[ -f "$fakehome/.gw/.env" ] || { echo "FAIL: init did not write .gw/.env"; exit 1; }
ver_out=$("$wrapper" version 2>/dev/null || true)
echo "installed wrapper version reports: $ver_out"

# Re-run = upgrade path: must NOT clobber the existing env.
cp "$fakehome/.gw/.env" "$stage/env.before"
env HOME="$fakehome" GW_INSTALL_DIR="$fakehome/.gw" GW_HOME="$fakehome/.gw" \
    GW_BASE_URL="$base" GW_API_URL="$base/latest.json" \
    sh scripts/install.sh
cmp -s "$stage/env.before" "$fakehome/.gw/.env" \
    || { echo "FAIL: upgrade re-ran init and changed .gw/.env"; exit 1; }

echo "PASS: install + upgrade smoke"
