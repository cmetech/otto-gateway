#!/usr/bin/env bash
# tests/scripts/test-support-bundle.sh — integration smoke for
# `scripts/otto-gw support`. Builds a fake install root, drops a synthetic
# log file containing known secret literals, runs the subcommand, then
# extracts the resulting archive and asserts:
#   - exit 0
#   - bundle path printed on stdout
#   - tar contains MANIFEST.txt + env/ + health/ + logs/ + system/ + tray/
#   - none of the synthetic secret literals appear ANYWHERE in the extracted
#     tree (defense-in-depth: log-scrub + env-mask both required)
#   - health/health.json contains the "unreachable:" sentinel (gateway
#     intentionally not running, so the bundle still completes)
#
# Plain bash, no test framework. Mirrors tests/scripts/publish_test.sh.
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

# cleanup is invoked indirectly via trap; SC2329 fires a false positive.
# shellcheck disable=SC2329
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

# Synthetic secret literals. These MUST NOT appear in the bundle anywhere.
SECRET_TOKEN_LITERAL="realsupersecretXYZ"
SECRET_BEARER_LITERAL="realtoken1234deadbeef"
SECRET_HASH_LITERAL="realHashKeyABC987"
SECRET_ENCRYPT_LITERAL="realEncryptKey555"

# Fake install root layout — mirrors the real scripts/otto-gw expectations
# enough that load_config + the support layout build successfully without
# any real binary present.
FAKE_ROOT=$(mktemp -d)
mkdir -p "$FAKE_ROOT/logs" "$FAKE_ROOT/.otto/gw"

# Synthetic log with all four scrub-trigger patterns embedded.
cat > "$FAKE_ROOT/logs/otto-gateway.log" <<EOF
2026-06-08T00:00:00Z info gateway boot ok
2026-06-08T00:00:01Z info AUTH_TOKEN=$SECRET_TOKEN_LITERAL was loaded from env
2026-06-08T00:00:02Z info Authorization: Bearer $SECRET_BEARER_LITERAL on inbound /v1/messages
2026-06-08T00:00:03Z info x-api-key: $SECRET_BEARER_LITERAL on inbound /api/chat
2026-06-08T00:00:04Z info PII_HASH_KEY=$SECRET_HASH_LITERAL active
2026-06-08T00:00:05Z info PII_ENCRYPT_KEY=$SECRET_ENCRYPT_LITERAL active
2026-06-08T00:00:06Z info routine traffic — no secrets here
EOF

# Boot log + chat-trace log so the support function exercises all three
# log-copy paths (and chat-trace's `-` prefix detection from OTTO_LOG).
echo "boot ok" > "$FAKE_ROOT/logs/otto-gateway-boot.log"
echo "trace ok" > "$FAKE_ROOT/logs/otto-gateway-chat-trace.log"

# NOTE: $FAKE_ROOT/.otto/tray/state fixture was removed alongside the
# tray/tray-state.txt bundle row in v1.10.3 (REL-TRAY-09 / D-18-10) —
# the wrapper no longer reads it.

# Output destination: a sibling temp dir so we can list the artifacts cleanly.
EXTRACT_DIR=$(mktemp -d)
OUT_DIR="$EXTRACT_DIR/out"
mkdir -p "$OUT_DIR"

# Run the support subcommand. Point every wrapper-resolved path at the
# fake install root. AUTH_TOKEN / PII_HASH_KEY / PII_ENCRYPT_KEY are set in
# the env so the env/effective.env dump exercises the mask path. OTTO_BIN
# points to /bin/true so the `--version` probe in system/versions.txt runs
# without spawning the real binary. OTTO_ADDR is set to an unreachable port
# so health/health.json captures the "unreachable:" sentinel path.
echo "== running otto-gw support =="
STDOUT_FILE=$(mktemp)
STDERR_FILE=$(mktemp)
set +e
OTTO_INSTALL_ROOT="$FAKE_ROOT" \
    OTTO_BIN=/bin/true \
    OTTO_STATE_DIR="$FAKE_ROOT/.otto/gw" \
    OTTO_PID="$FAKE_ROOT/.otto/gw/otto-gateway.pid" \
    OTTO_LOG="$FAKE_ROOT/logs/otto-gateway.log" \
    OTTO_LOG_BOOT="$FAKE_ROOT/logs/otto-gateway-boot.log" \
    OTTO_ADDR=http://127.0.0.1:1 \
    AUTH_TOKEN="$SECRET_TOKEN_LITERAL" \
    PII_HASH_KEY="$SECRET_HASH_LITERAL" \
    PII_ENCRYPT_KEY="$SECRET_ENCRYPT_LITERAL" \
    HTTP_ADDR="127.0.0.1:18080" \
    bash "$WRAPPER" support --out "$OUT_DIR" >"$STDOUT_FILE" 2>"$STDERR_FILE"
RC=$?
set -e

if [[ "$RC" -eq 0 ]]; then
    ok "support exit 0"
else
    fail_with "support exit $RC (stderr: $(cat "$STDERR_FILE"))"
fi

BUNDLE_PATH=$(tail -n 1 "$STDOUT_FILE" || true)
if [[ -n "$BUNDLE_PATH" && -f "$BUNDLE_PATH" ]]; then
    ok "bundle path printed and file exists: $BUNDLE_PATH"
else
    fail_with "bundle path missing or file does not exist: [$BUNDLE_PATH]"
    # Without a bundle there is nothing further we can verify — bail with
    # a summary so the operator sees the partial counters.
    echo "passed: $PASS, failed: $FAIL" >&2
    rm -f "$STDOUT_FILE" "$STDERR_FILE"
    exit 1
fi

# latest.tar.gz alias must point at a real file.
if [[ -f "$OUT_DIR/latest.tar.gz" ]]; then
    ok "latest.tar.gz alias exists"
else
    fail_with "latest.tar.gz alias missing"
fi

# Tar contents must include MANIFEST.txt + the six required trees.
echo "== tar -tzf =="
TAR_LIST=$(tar -tzf "$BUNDLE_PATH")
for required in MANIFEST.txt env/ health/ logs/ system/ tray/; do
    if echo "$TAR_LIST" | grep -q "/$required"; then
        ok "tar contains $required"
    else
        fail_with "tar missing $required"
    fi
done

# Extract and grep for synthetic secrets.
EX_TREE="$EXTRACT_DIR/extracted"
mkdir -p "$EX_TREE"
tar -xzf "$BUNDLE_PATH" -C "$EX_TREE"

echo "== secret-leak grep =="
for needle in "$SECRET_TOKEN_LITERAL" "$SECRET_BEARER_LITERAL" "$SECRET_HASH_LITERAL" "$SECRET_ENCRYPT_LITERAL"; do
    # grep -r returns 1 on no-match, 0 on match. -F treats needle as fixed
    # string (no regex). -I skips binary files (the .tar.gz inside would
    # otherwise count as a binary match for the secrets baked into it —
    # but we extracted, so there's no archive inside the extracted tree.
    # -I keeps the gate robust against any future addition of binaries).
    if grep -rIF -- "$needle" "$EX_TREE" >/dev/null 2>&1; then
        fail_with "synthetic secret leaked into bundle: $needle"
        echo "    leak locations:" >&2
        grep -rIFln -- "$needle" "$EX_TREE" >&2 || true
    else
        ok "synthetic secret absent from bundle: $needle"
    fi
done

# Find the exact bundle root inside the extracted tree (only one entry
# matches otto-support-*). Using a globbing assignment shellcheck-warns;
# resolve it with a single-element array to keep the path quoted everywhere.
BUNDLE_ROOTS=( "$EX_TREE"/otto-support-* )
BUNDLE_ROOT="${BUNDLE_ROOTS[0]}"
if [[ ! -d "$BUNDLE_ROOT" ]]; then
    fail_with "extracted bundle root not found under $EX_TREE"
    echo "passed: $PASS, failed: $FAIL" >&2
    exit 1
fi

# health/health.json must contain the "unreachable:" sentinel (port 1 was
# intentionally unreachable — the bundle still completes).
if grep -q "unreachable:" "$BUNDLE_ROOT/health/health.json" 2>/dev/null; then
    ok "health/health.json captured unreachable sentinel"
else
    fail_with "health/health.json did not capture unreachable sentinel"
fi

# env/effective.env must show the mask format for known secrets.
if grep -q "AUTH_TOKEN=real" "$BUNDLE_ROOT/env/effective.env" 2>/dev/null; then
    ok "env/effective.env masked AUTH_TOKEN (first 4 chars only)"
else
    fail_with "env/effective.env did not capture AUTH_TOKEN at all (or did not mask it correctly)"
fi
if grep -q "AUTH_TOKEN=$SECRET_TOKEN_LITERAL" "$BUNDLE_ROOT/env/effective.env" 2>/dev/null; then
    fail_with "env/effective.env LEAKED the full AUTH_TOKEN literal"
fi

# MANIFEST.txt must declare the redaction notice + list the bundle contents.
if grep -q "Redaction notice" "$BUNDLE_ROOT/MANIFEST.txt" 2>/dev/null; then
    ok "MANIFEST.txt has redaction notice"
else
    fail_with "MANIFEST.txt missing redaction notice"
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
