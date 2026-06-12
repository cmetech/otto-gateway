#!/usr/bin/env bash
# tests/scripts/test-support-bundle-rel-tray-09.sh — REL-TRAY-09
# (D-18-10) regression: the macOS support bundle MUST NOT emit two
# rows that have always lied:
#
#   - tray/tray-state.txt — reads $OTTO_INSTALL_ROOT/.otto/tray/state,
#     a file the tray has never written. Pre-fix the row showed
#     "(unavailable: ... does not exist)" on every bundle ever produced.
#
#   - tray/autostart.txt — checks $HOME/Library/LaunchAgents/com.otto.tray.plist,
#     but the actual plist label is io.cmetech.otto-tray (cmd/otto-tray/
#     autostart_darwin.go:15). Pre-fix the row always reported the
#     LaunchAgent absent, even when autostart was correctly installed.
#
# This test runs `scripts/otto-gw support` against a fake install root,
# extracts the archive, and asserts:
#
#   - tray/tray-state.txt is absent (negative assertion)
#   - tray/autostart.txt   is absent (negative assertion)
#   - tray/pidfile.txt     is present (positive control — proves removal
#                                       did not delete the whole section)
#
# Plain bash, no test framework. Mirrors tests/scripts/test-support-bundle.sh.
# Per 18-03-PLAN.md task 2 acceptance criteria — bats not available on the
# dev box and tests/wrappers/ does not exist; using the existing
# tests/scripts/ convention.
#
# Linux/Windows skip: the broken rows are macOS-side only; runs are
# allowed on linux too because the rows are removed from the bash
# wrapper for all uname values now (no platform gate on the removal).
set -euo pipefail

REPO_ROOT="$(cd -P "$(dirname "$0")/../.." >/dev/null 2>&1 && pwd)"
WRAPPER="$REPO_ROOT/scripts/otto-gw"
if [[ ! -x "$WRAPPER" ]]; then
    echo "FATAL: $WRAPPER not executable" >&2
    exit 1
fi

PASS=0
FAIL=0
FAKE_ROOT=""
EXTRACT_DIR=""

# shellcheck disable=SC2329  # trap-invoked cleanup
cleanup() {
    [[ -n "$FAKE_ROOT" && -d "$FAKE_ROOT" ]] && rm -rf "$FAKE_ROOT"
    [[ -n "$EXTRACT_DIR" && -d "$EXTRACT_DIR" ]] && rm -rf "$EXTRACT_DIR"
}
trap cleanup EXIT

fail_with() {
    FAIL=$((FAIL + 1))
    echo "FAIL: $*" >&2
}

ok() {
    PASS=$((PASS + 1))
    echo "  ok: $*"
}

FAKE_ROOT=$(mktemp -d)
mkdir -p "$FAKE_ROOT/logs" "$FAKE_ROOT/.otto/gw"
# NOTE: intentionally NOT writing $FAKE_ROOT/.otto/tray/state — the row
# is being removed precisely because that file has never existed.

# Minimal log fixture so the support flow's log-copy doesn't error on
# missing input.
echo "boot ok" > "$FAKE_ROOT/logs/otto-gateway.log"

EXTRACT_DIR=$(mktemp -d)
OUT_DIR="$EXTRACT_DIR/out"
mkdir -p "$OUT_DIR"

echo "== running otto-gw support =="
STDOUT_FILE=$(mktemp)
STDERR_FILE=$(mktemp)
set +e
OTTO_INSTALL_ROOT="$FAKE_ROOT" \
    OTTO_BIN=/bin/true \
    OTTO_STATE_DIR="$FAKE_ROOT/.otto/gw" \
    OTTO_PID="$FAKE_ROOT/.otto/gw/otto-gateway.pid" \
    OTTO_LOG="$FAKE_ROOT/logs/otto-gateway.log" \
    OTTO_ADDR=http://127.0.0.1:1 \
    HTTP_ADDR="127.0.0.1:18080" \
    bash "$WRAPPER" support --out "$OUT_DIR" >"$STDOUT_FILE" 2>"$STDERR_FILE"
RC=$?
set -e

if [[ "$RC" -ne 0 ]]; then
    fail_with "support exit $RC (stderr: $(cat "$STDERR_FILE"))"
    echo "passed: $PASS, failed: $FAIL" >&2
    rm -f "$STDOUT_FILE" "$STDERR_FILE"
    exit 1
fi

BUNDLE_PATH=$(tail -n 1 "$STDOUT_FILE" || true)
if [[ -z "$BUNDLE_PATH" || ! -f "$BUNDLE_PATH" ]]; then
    fail_with "bundle path missing or file does not exist: [$BUNDLE_PATH]"
    echo "passed: $PASS, failed: $FAIL" >&2
    rm -f "$STDOUT_FILE" "$STDERR_FILE"
    exit 1
fi

# Extract.
EX_TREE="$EXTRACT_DIR/extracted"
mkdir -p "$EX_TREE"
tar -xzf "$BUNDLE_PATH" -C "$EX_TREE"

# Locate the otto-support-* dir inside the extracted tree.
BUNDLE_ROOTS=( "$EX_TREE"/otto-support-* )
BUNDLE_ROOT="${BUNDLE_ROOTS[0]}"
if [[ ! -d "$BUNDLE_ROOT" ]]; then
    fail_with "extracted bundle root not found under $EX_TREE"
    echo "passed: $PASS, failed: $FAIL" >&2
    exit 1
fi

# ---- REL-TRAY-09 assertions ----------------------------------------
# Case B1: tray/tray-state.txt MUST NOT exist (row removed per D-18-10).
if [[ -f "$BUNDLE_ROOT/tray/tray-state.txt" ]]; then
    fail_with "tray/tray-state.txt is present — D-18-10 row removal regressed"
    echo "    contents:" >&2
    cat "$BUNDLE_ROOT/tray/tray-state.txt" >&2 || true
else
    ok "tray/tray-state.txt absent (D-18-10 row removed)"
fi

# Case B2: tray/autostart.txt MUST NOT exist (row removed per D-18-10).
if [[ -f "$BUNDLE_ROOT/tray/autostart.txt" ]]; then
    fail_with "tray/autostart.txt is present — D-18-10 row removal regressed"
    echo "    contents:" >&2
    cat "$BUNDLE_ROOT/tray/autostart.txt" >&2 || true
else
    ok "tray/autostart.txt absent (D-18-10 row removed)"
fi

# Case B3: positive control — tray/pidfile.txt MUST still exist so the
# tray section is not entirely empty after row removal.
if [[ -f "$BUNDLE_ROOT/tray/pidfile.txt" ]]; then
    ok "tray/pidfile.txt present (positive control — section intact)"
else
    fail_with "tray/pidfile.txt missing — row removal accidentally killed the tray section"
fi

rm -f "$STDOUT_FILE" "$STDERR_FILE"

echo
echo "== SUMMARY =="
echo "passed: $PASS"
echo "failed: $FAIL"
if [[ "$FAIL" -gt 0 ]]; then
    exit 1
fi
exit 0
