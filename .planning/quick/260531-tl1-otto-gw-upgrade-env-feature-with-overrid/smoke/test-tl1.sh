#!/usr/bin/env bash
# .planning/quick/260531-tl1-…/smoke/test-tl1.sh
# Smoke harness for the otto-gw upgrade-env feature (260531-tl1).
# shellcheck disable=SC2016  # assert() eval's its arg → vars are intentionally single-quoted so they expand at eval time
# shellcheck disable=SC2034  # variables consumed inside assert's eval are reported as unused by shellcheck
# Runs against a tmpdir; never touches the operator's real config. The
# gateway binary is stubbed so `start` doesn't try to launch the real one
# (most checks use `env`, which loads config but does not exec the binary).
#
# Exit 0 ⇒ every contract behavior held end-to-end against the wrapper
# under test (the one in scripts/otto-gw, sibling to scripts/.env.otto-gw.example).
set -euo pipefail

# Resolve repo root → wrapper + template anchored under it. The harness lives
# at .planning/quick/<id>/smoke/test-tl1.sh, so ../../../.. is repo root.
REPO_ROOT="$(cd "$(dirname "$0")/../../../.." && pwd)"
WRAPPER="$REPO_ROOT/scripts/otto-gw"
TEMPLATE="$REPO_ROOT/scripts/.env.otto-gw.example"

if [[ ! -x "$WRAPPER" ]]; then
    echo "FAIL: wrapper not executable at $WRAPPER" >&2; exit 1
fi
if [[ ! -f "$TEMPLATE" ]]; then
    echo "FAIL: template missing at $TEMPLATE" >&2; exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"

# Stub binary so `start` doesn't actually launch anything. The smoke harness
# only uses `env` + the two new subcommands, which never exec the binary,
# but having OTTO_BIN point at something executable keeps preflight clean.
mkdir -p bin
cat > bin/otto-gateway <<'STUB'
#!/usr/bin/env bash
echo "stub-binary alive"
sleep 86400
STUB
chmod +x bin/otto-gateway
export OTTO_BIN="$tmp/bin/otto-gateway"
export OTTO_STATE_DIR="$tmp/.otto/gw"
export OTTO_LOG="$tmp/logs/otto.log"
export OTTO_TEMPLATE_FILE="$TEMPLATE"
# Localize the upgrade orphan log so the test never writes to $HOME.
export OTTO_UPGRADE_LOG="$tmp/.otto-gw.upgrade.log"

section() { echo; echo "=== $1 ==="; }
assert() {
    if ! eval "$1"; then
        echo "FAIL: $1" >&2
        echo "      cwd: $(pwd)" >&2
        ls -la >&2 || true
        exit 1
    fi
}
mode_of() {
    # BSD stat (macOS): %A is the numeric mode. GNU stat (Linux): %a.
    # Try BSD first, fall back to GNU. Returns "" if neither works
    # (callers gate via `[[ ... == 600 ]]`).
    stat -f "%A" "$1" 2>/dev/null || stat -c "%a" "$1" 2>/dev/null || echo ""
}

section "1. fresh init writes two files (template-copy + overrides)"
"$WRAPPER" init --here --non-interactive --auth-enabled --pii hash \
    --kiro /bin/true --addr 127.0.0.1:18080 >/dev/null
assert '[[ -f ./.env.otto-gw ]]'
assert '[[ -f ./.otto-gw.overrides.env ]]'
# .otto-gw.env must be a byte-for-byte template copy under the new contract.
assert 'diff -q ./.env.otto-gw "$TEMPLATE" >/dev/null'
# Overrides must contain the secrets + operator-set values.
assert 'grep -q "^AUTH_TOKEN=" ./.otto-gw.overrides.env'
assert 'grep -q "^PII_HASH_KEY=" ./.otto-gw.overrides.env'
assert 'grep -q "^KIRO_CMD=/bin/true$" ./.otto-gw.overrides.env'
assert 'grep -q "^HTTP_ADDR=127.0.0.1:18080$" ./.otto-gw.overrides.env'
# Mode check: overrides must be 0600 (secrets); .env.otto-gw is 0644 (template copy).
overrides_mode="$(mode_of ./.otto-gw.overrides.env)"
env_mode="$(mode_of ./.env.otto-gw)"
assert '[[ "$overrides_mode" == "600" ]]'
assert '[[ "$env_mode" == "644" ]]'

section "2. loader precedence — overrides value wins"
# Append CHAT_TRACE=false to the template-copy and CHAT_TRACE=true to overrides.
# Use the `env --show-secrets` print path to read back the resolved value.
echo 'CHAT_TRACE=false' >> ./.env.otto-gw
echo 'CHAT_TRACE=true'  >> ./.otto-gw.overrides.env
out="$("$WRAPPER" env --show-secrets 2>/dev/null | grep '^CHAT_TRACE=' || true)"
# Note: CHAT_TRACE isn't in the wrapper's print_env key list today, so we
# do an env-export round-trip instead — proves the same precedence.
unset CHAT_TRACE
eval "$( (set +e; set +u; \
    cd "$tmp" && \
    bash -c '
      WRAPPER='"$WRAPPER"'
      LAST=$(grep -n "^CMD=" "$WRAPPER" | head -1 | cut -d: -f1)
      sed -n "1,$((LAST-1))p" "$WRAPPER" > /tmp/tl1-funcs.sh
      set +e; set +u
      source /tmp/tl1-funcs.sh
      load_config 2>/dev/null
      echo "CHAT_TRACE=\"$CHAT_TRACE\""
    ' )
)"
assert '[[ "$CHAT_TRACE" == "true" ]]'

section "3. upgrade-env --dry-run on stale dest (added + orphaned)"
# Build a stale dest: rewrite as template MINUS one key, PLUS a fake orphan.
# Pick the commented STREAM_IDLE_TIMEOUT_SEC line (a real template key) to
# delete and create a fake "FOO_ORPHAN" key.
grep -v "^# STREAM_IDLE_TIMEOUT_SEC=" "$TEMPLATE" > ./.env.otto-gw
echo 'FOO_ORPHAN=banana' >> ./.env.otto-gw
diff_out="$("$WRAPPER" upgrade-env --dry-run --template "$TEMPLATE" --dest ./.env.otto-gw 2>&1)"
assert 'echo "$diff_out" | grep -qE "added:[[:space:]]+1.*STREAM_IDLE_TIMEOUT_SEC"'
assert 'echo "$diff_out" | grep -qE "orphaned:[[:space:]]+1.*FOO_ORPHAN"'
# Dry-run wrote nothing.
assert 'grep -q "^FOO_ORPHAN=banana$" ./.env.otto-gw'

section "4. upgrade-env --yes overwrites + writes orphan log"
"$WRAPPER" upgrade-env --yes --template "$TEMPLATE" --dest ./.env.otto-gw >/dev/null
# After overwrite, dest must equal the template byte-for-byte and the
# orphan must be gone.
assert 'diff -q ./.env.otto-gw "$TEMPLATE" >/dev/null'
assert '! grep -q "^FOO_ORPHAN" ./.env.otto-gw'
# The orphan log was written to $OTTO_UPGRADE_LOG (set at the top of this script).
assert '[[ -f "$OTTO_UPGRADE_LOG" ]]'
assert 'grep -q "^FOO_ORPHAN=banana$" "$OTTO_UPGRADE_LOG"'

section "5. migrate-to-overrides extracts non-default + is idempotent"
# Build a v1.6.10-style single-file install (no overrides, uncommented operator
# customizations in .otto-gw.env).
rm -f ./.otto-gw.overrides.env
cp "$TEMPLATE" ./.env.otto-gw
# Uncomment AUTH_TOKEN with a custom legacy value.
awk '/^# AUTH_TOKEN=/{ print "AUTH_TOKEN=legacy-token-abc"; next } { print }' \
    ./.env.otto-gw > ./.env.otto-gw.t && mv ./.env.otto-gw.t ./.env.otto-gw
# Set KIRO_CMD to a custom path (template has KIRO_CMD= uncommented but empty).
sed -i.bak 's|^KIRO_CMD=$|KIRO_CMD=/custom/path/kiro|' ./.env.otto-gw
rm -f ./.env.otto-gw.bak

"$WRAPPER" migrate-to-overrides --yes --dest ./.env.otto-gw >/dev/null
assert '[[ -f ./.otto-gw.overrides.env ]]'
assert 'grep -q "^AUTH_TOKEN=legacy-token-abc$" ./.otto-gw.overrides.env'
assert 'grep -q "^KIRO_CMD=/custom/path/kiro$" ./.otto-gw.overrides.env'
# Dest is now a pure template copy.
assert 'diff -q ./.env.otto-gw "$TEMPLATE" >/dev/null'
# Backup file present.
assert 'ls .env.otto-gw.pre-migrate.* >/dev/null 2>&1'

# Second invocation: idempotent no-op.
second="$("$WRAPPER" migrate-to-overrides --yes --dest ./.env.otto-gw 2>&1)"
assert 'echo "$second" | grep -q "already migrated"'

section "6. deprecation WARN fires on legacy single-file install"
# Tear down overrides, install the legacy single-file layout, run any
# command that triggers load_config (env is cheapest — no binary exec).
rm -f ./.otto-gw.overrides.env
cp "$TEMPLATE" ./.env.otto-gw
awk '/^# AUTH_TOKEN=/{ print "AUTH_TOKEN=legacy-token-xyz"; next } { print }' \
    ./.env.otto-gw > ./.env.otto-gw.t && mv ./.env.otto-gw.t ./.env.otto-gw
out="$("$WRAPPER" env 2>&1 || true)"
assert 'echo "$out" | grep -q "legacy single-file"'
assert 'echo "$out" | grep -q "otto-gw migrate-to-overrides"'

section "7. deprecation WARN silent when overrides present"
echo 'KIRO_CMD=/whatever' > ./.otto-gw.overrides.env
out="$("$WRAPPER" env 2>&1)"
assert '! echo "$out" | grep -q "legacy single-file"'

echo
echo "ALL TL1 SMOKE CHECKS PASSED"
