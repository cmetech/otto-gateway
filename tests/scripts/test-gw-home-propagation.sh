#!/usr/bin/env bash
# Regression test: wrapper-derived GW_HOME must reach the gateway process.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FAKE_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/gw-home-propagation.XXXXXX")"
trap 'rm -rf "$FAKE_ROOT"' EXIT INT TERM

FAKE_HOME="$FAKE_ROOT/home"
FAKE_INSTALL="$FAKE_ROOT/install"
FAKE_GATEWAY="$FAKE_ROOT/fake-gateway"
mkdir -p "$FAKE_HOME" "$FAKE_INSTALL"

cat > "$FAKE_GATEWAY" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "${GW_HOME:-<unset>}"
EOF
chmod +x "$FAKE_GATEWAY"

expected="$FAKE_HOME/.gw"

posix_output="$({
    cd "$FAKE_ROOT"
    env -u GW_HOME \
        HOME="$FAKE_HOME" \
        GW_INSTALL_DIR="$FAKE_INSTALL" \
        GW_BIN="$FAKE_GATEWAY" \
        bash "$REPO_ROOT/scripts/gw" run
})"

if [[ "$posix_output" != "$expected" ]]; then
    printf 'FAIL: POSIX wrapper passed GW_HOME=%q, expected %q\n' "$posix_output" "$expected" >&2
    exit 1
fi

if command -v pwsh >/dev/null 2>&1; then
    powershell_output="$({
        cd "$FAKE_ROOT"
        env -u GW_HOME \
            HOME="$FAKE_HOME" \
            USERPROFILE="$FAKE_HOME" \
            GW_BIN="$FAKE_GATEWAY" \
            pwsh -NoProfile -File "$REPO_ROOT/scripts/gw.ps1" run
    })"

    if [[ "$powershell_output" != "$expected" ]]; then
        printf 'FAIL: PowerShell wrapper passed GW_HOME=%q, expected %q\n' "$powershell_output" "$expected" >&2
        exit 1
    fi
else
    printf 'SKIP: pwsh not available; PowerShell wrapper assertion not run\n'
fi

printf 'PASS: wrappers propagate default GW_HOME to the gateway process\n'
