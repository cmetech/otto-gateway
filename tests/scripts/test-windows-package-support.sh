#!/usr/bin/env bash
# Package-level smoke for the Windows distribution. It builds the real staging
# tree/archive, then runs the packaged gw.ps1 under PowerShell to prove every
# runtime helper needed for support-log collection actually shipped.
set -euo pipefail

repo_root="$(cd -P "$(dirname "$0")/../.." && pwd)"
tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/gw-windows-package-support.XXXXXX")"
# macOS spells its real temporary tree under /private/var while mktemp often
# returns the /var symlink. The packaged safe-open helper correctly rejects a
# symlinked ancestor, so make the fixture's own path canonical first.
tmp_root="$(cd -P "$tmp_root" && pwd)"
trap 'rm -rf "$tmp_root"' EXIT HUP INT TERM

build_dir="$tmp_root/bin"
dist_dir="$tmp_root/dist"
version="v0.0.0-package-support-smoke"

make -s -C "$repo_root" \
    BUILD_DIR="$build_dir" \
    DIST_DIR="$dist_dir" \
    VERSION="$version" \
    package-windows-amd64

archive="$dist_dir/otto_gateway-windows-amd64-$version.zip"
extract_dir="$tmp_root/package"
unzip -q "$archive" -d "$extract_dir"
package_root="$extract_dir/otto_gateway"
helper="$package_root/scripts/lib/support-safe-open.ps1"
support_redactor="$package_root/bin/support-redactor"

# The packaged gateway.exe is intentionally a Windows binary and cannot run on
# this Unix host. Build the same classifier for the host so the package smoke
# exercises the shipped PowerShell wrapper and helpers end to end.
go build -o "$support_redactor" "$repo_root/cmd/otto-gateway"

failures=0
assert_file() {
    if [[ -f "$1" ]]; then
        printf '  ok: %s\n' "$2"
    else
        printf 'FAIL: %s (missing: %s)\n' "$2" "$1" >&2
        failures=$((failures + 1))
    fi
}

assert_file "$archive" 'Windows package archive is produced'
assert_file "$helper" 'Windows package preserves scripts/lib/support-safe-open.ps1'

gateway_home="$tmp_root/gateway-home"
coworker_home="$tmp_root/co-worker-home"
support_out="$tmp_root/support-out"
mkdir -p "$gateway_home/logs" "$gateway_home/state" "$coworker_home/logs" "$support_out"
printf 'packaged gateway log\n' > "$gateway_home/logs/gateway.log"
printf 'packaged boot log\n' > "$gateway_home/logs/gateway-boot.log"
printf 'packaged Kiro log\n' > "$gateway_home/logs/kiro-chat.log"
printf 'packaged Co-worker log\n' > "$coworker_home/logs/agent.log"

set +e
support_stdout="$({
    HOME="$tmp_root/home" \
    USERPROFILE="$tmp_root/home" \
    GW_HOME="$gateway_home" \
    GW_STATE_DIR="$gateway_home/state" \
    GW_PID="$gateway_home/state/gateway.pid" \
    GW_LOG="$gateway_home/logs/gateway.log" \
    GW_LOGOUT="$gateway_home/logs/gateway-boot.log" \
    GW_LOGERR="$gateway_home/logs/missing-boot-error.log" \
    GW_ADDR='http://127.0.0.1:1' \
    GW_ENV_FILE="$tmp_root/missing.env" \
    GW_OVERRIDES_FILE="$tmp_root/missing-overrides.env" \
    GW_SUPPORT_REDACTOR_BIN="$support_redactor" \
    KIRO_CHAT_LOG_FILE="$gateway_home/logs/kiro-chat.log" \
    HERMES_HOME="$coworker_home" \
    pwsh -NoProfile -File "$package_root/scripts/gw.ps1" support \
        -Out "$support_out" -Timeout 30 -LogDays 9999
} 2> "$tmp_root/support.stderr")"
support_rc=$?
set -e
if [[ $support_rc -ne 0 ]]; then
    printf 'FAIL: packaged gw.ps1 support exited %d\n' "$support_rc" >&2
    sed -n '1,120p' "$tmp_root/support.stderr" >&2
    failures=$((failures + 1))
fi

support_bundle="$(printf '%s\n' "$support_stdout" | awk 'NF { last=$0 } END { print last }')"
if [[ -z "$support_bundle" || ! -f "$support_bundle" ]]; then
    printf 'FAIL: packaged gw.ps1 did not publish a support archive (stdout=%q)\n' "$support_stdout" >&2
    failures=$((failures + 1))
else
    support_tree="$tmp_root/support-tree"
    unzip -q "$support_bundle" -d "$support_tree"
    bundle_root="$(find "$support_tree" -mindepth 1 -maxdepth 1 -type d -name 'gateway-support-*' -print -quit)"
    assert_file "$bundle_root/logs/gateway/gateway.log" 'packaged wrapper collects Gateway application log'
    assert_file "$bundle_root/logs/kiro/kiro-chat.log" 'packaged wrapper collects Kiro application log'
    assert_file "$bundle_root/logs/co-worker/agent.log" 'packaged wrapper collects Co-worker application log'
fi

if [[ $failures -ne 0 ]]; then
    printf 'failed: %d\n' "$failures" >&2
    exit 1
fi
printf 'passed: Windows package support helper and runtime smoke\n'
