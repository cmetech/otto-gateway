#!/usr/bin/env bash
# tests/install/migrate_posix_test.sh — unit test for scripts/lib/migrate.sh's
# gw_migrate_from_otto(). Runs entirely inside a throwaway $HOME; never
# touches the real user's ~/.otto-gw* or ~/.gw.
#
# Covers:
#   1. Legacy ~/.otto-gw.env is MOVED (not copied) to ~/.gw/.env, contents
#      preserved exactly (this is where AUTH_TOKEN lives — losing it on
#      upgrade is the failure this helper prevents).
#   2. Idempotency: a second run is a no-op (already-migrated guard) and does
#      not clobber or error.
#
# Usage: bash tests/install/migrate_posix_test.sh
set -euo pipefail

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
export HOME="$TMP"

printf 'AUTH_TOKEN=secret\n' > "$HOME/.otto-gw.env"

# shellcheck source=../../scripts/lib/migrate.sh
source "$(dirname "$0")/../../scripts/lib/migrate.sh"

gw_migrate_from_otto

grep -q 'AUTH_TOKEN=secret' "$HOME/.gw/.env" || { echo "FAIL: env not migrated"; exit 1; }
[ -f "$HOME/.otto-gw.env" ] && { echo "FAIL: legacy env was copied, not moved (still present)"; exit 1; }

# idempotent: second run must not error or clobber.
gw_migrate_from_otto

grep -q 'AUTH_TOKEN=secret' "$HOME/.gw/.env" || { echo "FAIL: idempotency"; exit 1; }

echo "✓ Primary path + idempotency"

# === Test 2: Companion files (overrides.env + tray.json) ===
TMP2=$(mktemp -d)
trap 'rm -rf "$TMP" "$TMP2"' EXIT
export HOME="$TMP2"

printf 'AUTH_TOKEN=secret\n' > "$HOME/.otto-gw.env"
printf 'OVERRIDE_SETTING=value\n' > "$HOME/.otto-gw.overrides.env"
mkdir -p "$HOME/.otto-gw"
printf '{"version": "1.0"}\n' > "$HOME/.otto-gw/tray.json"

# shellcheck source=../../scripts/lib/migrate.sh
source "$(dirname "$0")/../../scripts/lib/migrate.sh"
gw_migrate_from_otto

# Assert companions were moved (not copied)
grep -q 'OVERRIDE_SETTING=value' "$HOME/.gw/overrides.env" || { echo "FAIL: overrides.env not migrated"; exit 1; }
[ -f "$HOME/.otto-gw.overrides.env" ] && { echo "FAIL: overrides.env was copied, not moved"; exit 1; }

grep -q '"version": "1.0"' "$HOME/.gw/tray.json" || { echo "FAIL: tray.json not migrated"; exit 1; }
[ -f "$HOME/.otto-gw/tray.json" ] && { echo "FAIL: tray.json was copied, not moved"; exit 1; }

echo "✓ Companion files (overrides.env + tray.json)"

# === Test 3: Fallback env path (legacy ~/.otto-gw/.env.otto-gw) ===
TMP3=$(mktemp -d)
trap 'rm -rf "$TMP" "$TMP2" "$TMP3"' EXIT
export HOME="$TMP3"

# Create only the fallback path, NOT the primary path
mkdir -p "$HOME/.otto-gw"
printf 'AUTH_TOKEN=fallback_secret\n' > "$HOME/.otto-gw/.env.otto-gw"

# shellcheck source=../../scripts/lib/migrate.sh
source "$(dirname "$0")/../../scripts/lib/migrate.sh"
gw_migrate_from_otto

# Assert fallback was migrated
grep -q 'AUTH_TOKEN=fallback_secret' "$HOME/.gw/.env" || { echo "FAIL: fallback env not migrated"; exit 1; }
[ -f "$HOME/.otto-gw/.env.otto-gw" ] && { echo "FAIL: fallback env was copied, not moved"; exit 1; }

echo "✓ Fallback env path"

# === Test 4: Never delete old code directory ===
TMP4=$(mktemp -d)
trap 'rm -rf "$TMP" "$TMP2" "$TMP3" "$TMP4"' EXIT
export HOME="$TMP4"

printf 'AUTH_TOKEN=secret\n' > "$HOME/.otto-gw.env"
mkdir -p "$HOME/.otto-gw/scripts"
touch "$HOME/.otto-gw/scripts/some_script.sh"

# shellcheck source=../../scripts/lib/migrate.sh
source "$(dirname "$0")/../../scripts/lib/migrate.sh"
gw_migrate_from_otto

# Assert legacy code directory still exists
[ -d "$HOME/.otto-gw" ] || { echo "FAIL: ~/.otto-gw/ directory was deleted"; exit 1; }
[ -f "$HOME/.otto-gw/scripts/some_script.sh" ] || { echo "FAIL: files in ~/.otto-gw/ were deleted"; exit 1; }

echo "✓ Legacy code directory preserved"

echo PASS
