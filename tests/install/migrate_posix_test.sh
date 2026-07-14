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

echo PASS
