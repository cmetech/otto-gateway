#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURE_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/gw-privacy-secrets-posix.XXXXXX")"
trap 'rm -rf "$FIXTURE_ROOT"' EXIT

fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
ok() { printf 'ok: %s\n' "$1"; }

value_of() {
    local file="$1" key="$2"
    awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$file"
}

stored_value_of() {
    local file="$1" key="$2"
    awk -v key="$key" '
        { line=$0; sub(/^[[:space:]]*#[[:space:]]*/, "", line) }
        index(line, key "=") == 1 { sub(/^[^=]*=/, "", line); print line; exit }
    ' "$file"
}

assert_managed_secret() {
    local file="$1" key="$2" value
    value="$(value_of "$file" "$key")"
    [[ "$value" =~ ^[0-9a-f]{64}$ ]] || fail "$key was not generated as 32 cryptographic bytes"
    printf '%s' "$value"
}

replace_value() {
    local file="$1" key="$2" value="$3"
    awk -F= -v key="$key" -v value="$value" '$1 == key { print key "=" value; next } { print }' "$file" >"${file}.tmp"
    mv "${file}.tmp" "$file"
}

set_key_commented() {
    local file="$1" key="$2" commented="$3"
    awk -v key="$key" -v commented="$commented" '
        {
            line=$0
            probe=line
            sub(/^[[:space:]]*#[[:space:]]*/, "", probe)
            if (index(probe, key "=") == 1) {
                if (commented == "1") print "# " probe
                else print probe
                next
            }
            print line
        }
    ' "$file" >"${file}.tmp"
    mv "${file}.tmp" "$file"
}

run_init() {
    local output_file="$1"
    shift
    GW_HOME="$FIXTURE_ROOT/home" \
        GW_TEMPLATE_FILE="$REPO_ROOT/scripts/.env.example" \
        bash "$REPO_ROOT/scripts/gw" init \
        --dest "$FIXTURE_ROOT/home/.env" \
        --auth-enabled --non-interactive "$@" >"$output_file" 2>&1
}

run_disabled_init() {
    local output_file="$1"
    shift
    GW_HOME="$FIXTURE_ROOT/disabled-home" \
        GW_TEMPLATE_FILE="$REPO_ROOT/scripts/.env.example" \
        bash "$REPO_ROOT/scripts/gw" init \
        --dest "$FIXTURE_ROOT/disabled-home/.env" \
        --non-interactive "$@" >"$output_file" 2>&1
}

run_preprivacy_upgrade_flow() {
    local name="$1" auth="$2" hash="$3" encrypt="$4"
    local install_home="$FIXTURE_ROOT/$name" env_file="$FIXTURE_ROOT/$name/.env"
    local overrides_file="$FIXTURE_ROOT/$name/overrides.env" output_file="$FIXTURE_ROOT/$name.out"
    mkdir -p "$install_home"
    cat >"$env_file" <<'EOF'
HTTP_ADDR=127.0.0.1:18080
PII_REDACTION_ENABLED=true
PII_REDACTION_MODE=encrypt
EOF
    cat >"$overrides_file" <<EOF
AUTH_TOKEN=$auth
PII_HASH_KEY=$hash
PII_ENCRYPT_KEY=$encrypt
EOF

    if ! GW_HOME="$install_home" GW_TEMPLATE_FILE="$REPO_ROOT/scripts/.env.example" \
        GW_UPGRADE_LOG="$install_home/upgrade.log" \
        bash "$REPO_ROOT/scripts/gw" upgrade-env --dry-run --dest "$env_file" \
        >"$output_file" 2>&1; then
        cat "$output_file" >&2
        fail "$name upgrade preview failed"
    fi
    if ! GW_HOME="$install_home" GW_TEMPLATE_FILE="$REPO_ROOT/scripts/.env.example" \
        GW_UPGRADE_LOG="$install_home/upgrade.log" \
        bash "$REPO_ROOT/scripts/gw" upgrade-env --yes --dest "$env_file" \
        >>"$output_file" 2>&1; then
        cat "$output_file" >&2
        fail "$name upgrade apply failed"
    fi
    grep -Fqx 'PRIVACY_ALIAS_KEY=<generated-by-gw-init>' "$env_file" || fail "$name upgrade did not apply the shipped alias placeholder"
    grep -Fqx 'PRIVACY_TRIAGE_TOKEN=<generated-by-gw-init>' "$env_file" || fail "$name upgrade did not apply the shipped triage placeholder"
    if ! GW_HOME="$install_home" GW_TEMPLATE_FILE="$REPO_ROOT/scripts/.env.example" \
        bash "$REPO_ROOT/scripts/gw" init --dest "$env_file" \
        --force --non-interactive \
        >>"$output_file" 2>&1; then
        cat "$output_file" >&2
        fail "$name normal re-init failed"
    fi

    [[ "$(value_of "$overrides_file" AUTH_TOKEN)" == "$auth" ]] || fail "$name rotated existing AUTH_TOKEN"
    [[ "$(value_of "$overrides_file" PII_HASH_KEY)" == "$hash" ]] || fail "$name rotated existing PII_HASH_KEY"
    [[ "$(value_of "$overrides_file" PII_ENCRYPT_KEY)" == "$encrypt" ]] || fail "$name rotated existing PII_ENCRYPT_KEY"
    local alias_value triage_value secret mode
    alias_value="$(value_of "$overrides_file" PRIVACY_ALIAS_KEY)"
    triage_value="$(value_of "$overrides_file" PRIVACY_TRIAGE_TOKEN)"
    [[ "$alias_value" =~ ^[0-9a-f]{64}$ ]] || fail "$name preserved the shipped alias placeholder"
    [[ "$triage_value" =~ ^[0-9a-f]{64}$ ]] || fail "$name preserved the shipped triage placeholder"
    [[ "$alias_value" != "$triage_value" ]] || fail "$name minted identical privacy secrets"
    for secret in "$auth" "$hash" "$encrypt" "$alias_value" "$triage_value"; do
        ! grep -Fq "$secret" "$output_file" || fail "$name printed a managed secret"
    done
    if mode="$(stat -c '%a' "$overrides_file" 2>/dev/null)" && [[ "$mode" =~ ^[0-7]{3,4}$ ]]; then
        : # GNU stat
    elif mode="$(stat -f '%Lp' "$overrides_file" 2>/dev/null)" && [[ "$mode" =~ ^[0-7]{3,4}$ ]]; then
        : # BSD stat
    else
        fail "$name could not determine overrides mode"
    fi
    [[ "$mode" == "600" ]] || fail "$name overrides mode is $mode, want 600"
    ! find "$install_home" -maxdepth 1 -name '.managed-secrets-*' -o -name 'overrides.env.tmp.*' | grep -q . || fail "$name left a secret temporary"
}

mkdir -p "$FIXTURE_ROOT/home"
run_init "$FIXTURE_ROOT/cold.out"

overrides="$FIXTURE_ROOT/home/overrides.env"
[[ -f "$overrides" ]] || fail "cold init did not create overrides.env"

auth_before="$(assert_managed_secret "$overrides" AUTH_TOKEN)"
hash_before="$(assert_managed_secret "$overrides" PII_HASH_KEY)"
encrypt_before="$(assert_managed_secret "$overrides" PII_ENCRYPT_KEY)"
alias_before="$(assert_managed_secret "$overrides" PRIVACY_ALIAS_KEY)"
triage_before="$(assert_managed_secret "$overrides" PRIVACY_TRIAGE_TOKEN)"

for secret in "$alias_before" "$triage_before"; do
    ! grep -Fq "$secret" "$FIXTURE_ROOT/cold.out" || fail "privacy secret appeared in init output"
done
ok "cold init generates five managed secrets without exposing privacy values"

{
    printf '\n# operator comment must survive\n'
    printf 'UNRELATED_OPERATOR_SETTING=keep-me\n'
} >>"$overrides"
replace_value "$overrides" PRIVACY_ALIAS_KEY override-alias-value
replace_value "$overrides" PRIVACY_TRIAGE_TOKEN override-triage-value

run_init "$FIXTURE_ROOT/preserve.out" --force

[[ "$(value_of "$overrides" PRIVACY_ALIAS_KEY)" == "override-alias-value" ]] || fail "overrides.env alias key did not win on re-init"
[[ "$(value_of "$overrides" PRIVACY_TRIAGE_TOKEN)" == "override-triage-value" ]] || fail "overrides.env triage token did not win on re-init"
grep -Fq '# operator comment must survive' "$overrides" || fail "re-init removed an operator comment"
grep -Fq 'UNRELATED_OPERATOR_SETTING=keep-me' "$overrides" || fail "re-init removed an unrelated setting"
! grep -Fq 'override-alias-value' "$FIXTURE_ROOT/preserve.out" || fail "preserved alias key appeared in output"
! grep -Fq 'override-triage-value' "$FIXTURE_ROOT/preserve.out" || fail "preserved triage token appeared in output"
ok "normal upgrade preserves effective overrides and unrelated content"

set_key_commented "$overrides" PRIVACY_ALIAS_KEY 1
set_key_commented "$overrides" PRIVACY_TRIAGE_TOKEN 1
run_init "$FIXTURE_ROOT/commented-preserve.out" --force
grep -Fqx '# PRIVACY_ALIAS_KEY=override-alias-value' "$overrides" || fail "re-init enabled a deliberately commented privacy alias key"
grep -Fqx '# PRIVACY_TRIAGE_TOKEN=override-triage-value' "$overrides" || fail "re-init enabled a deliberately commented privacy triage token"
! grep -q '^PRIVACY_ALIAS_KEY=' "$overrides" || fail "re-init wrote a second enabled privacy alias key"
! grep -q '^PRIVACY_TRIAGE_TOKEN=' "$overrides" || fail "re-init wrote a second enabled privacy triage token"
ok "normal re-init preserves deliberately commented privacy secrets"
set_key_commented "$overrides" PRIVACY_ALIAS_KEY 0
set_key_commented "$overrides" PRIVACY_TRIAGE_TOKEN 0

auth_before="$(value_of "$overrides" AUTH_TOKEN)"
hash_before="$(value_of "$overrides" PII_HASH_KEY)"
encrypt_before="$(value_of "$overrides" PII_ENCRYPT_KEY)"
alias_before="$(value_of "$overrides" PRIVACY_ALIAS_KEY)"
triage_before="$(value_of "$overrides" PRIVACY_TRIAGE_TOKEN)"

run_init "$FIXTURE_ROOT/rotate.out" --force --regenerate-secrets

for key in AUTH_TOKEN PII_HASH_KEY PII_ENCRYPT_KEY PRIVACY_ALIAS_KEY PRIVACY_TRIAGE_TOKEN; do
    assert_managed_secret "$overrides" "$key" >/dev/null
done
[[ "$(value_of "$overrides" AUTH_TOKEN)" != "$auth_before" ]] || fail "AUTH_TOKEN did not rotate"
[[ "$(value_of "$overrides" PII_HASH_KEY)" != "$hash_before" ]] || fail "PII_HASH_KEY did not rotate"
[[ "$(value_of "$overrides" PII_ENCRYPT_KEY)" != "$encrypt_before" ]] || fail "PII_ENCRYPT_KEY did not rotate"
[[ "$(value_of "$overrides" PRIVACY_ALIAS_KEY)" != "$alias_before" ]] || fail "PRIVACY_ALIAS_KEY did not rotate"
[[ "$(value_of "$overrides" PRIVACY_TRIAGE_TOKEN)" != "$triage_before" ]] || fail "PRIVACY_TRIAGE_TOKEN did not rotate"
grep -Eiq 'mapping.*loss|mapping.*invalid' "$FIXTURE_ROOT/rotate.out" || fail "rotation did not warn about mapping loss"
grep -Eiq 'restart' "$FIXTURE_ROOT/rotate.out" || fail "rotation did not warn that restart is required"
warning_line="$(grep -Ein 'mapping.*loss|mapping.*invalid' "$FIXTURE_ROOT/rotate.out" | head -n1 | cut -d: -f1)"
write_line="$(grep -En 'wrote .*overrides' "$FIXTURE_ROOT/rotate.out" | head -n1 | cut -d: -f1)"
[[ -n "$warning_line" && -n "$write_line" && "$warning_line" -lt "$write_line" ]] || fail "mapping-loss warning was not printed before config mutation was reported"
for secret in "$alias_before" "$triage_before" "$(value_of "$overrides" PRIVACY_ALIAS_KEY)" "$(value_of "$overrides" PRIVACY_TRIAGE_TOKEN)"; do
    ! grep -Fq "$secret" "$FIXTURE_ROOT/rotate.out" || fail "privacy secret appeared in rotation output"
done
grep -Fq '# operator comment must survive' "$overrides" || fail "rotation removed an operator comment"
grep -Fq 'UNRELATED_OPERATOR_SETTING=keep-me' "$overrides" || fail "rotation removed an unrelated setting"
ok "explicit rotation atomically replaces all five secrets and warns safely"

mkdir -p "$FIXTURE_ROOT/disabled-home"
run_disabled_init "$FIXTURE_ROOT/disabled-cold.out"
disabled_overrides="$FIXTURE_ROOT/disabled-home/overrides.env"
declare -a disabled_keys=(AUTH_TOKEN PII_HASH_KEY PII_ENCRYPT_KEY PRIVACY_ALIAS_KEY PRIVACY_TRIAGE_TOKEN)
declare -a disabled_before=()
for key in "${disabled_keys[@]}"; do
    value="$(stored_value_of "$disabled_overrides" "$key")"
    [[ "$value" =~ ^[0-9a-f]{64}$ ]] || fail "disabled-auth cold init did not store managed $key"
    disabled_before+=("$value")
    ! grep -Fq "$value" "$FIXTURE_ROOT/disabled-cold.out" || fail "disabled-auth cold init printed $key"
done
grep -Eq '^#[[:space:]]*AUTH_TOKEN=[0-9a-f]{64}$' "$disabled_overrides" || fail "disabled-auth token is not stored commented"
! grep -Eq '^AUTH_TOKEN=' "$disabled_overrides" || fail "disabled-auth cold init enabled authentication"

run_disabled_init "$FIXTURE_ROOT/disabled-rotate.out" --force --regenerate-secrets
for i in "${!disabled_keys[@]}"; do
    key="${disabled_keys[$i]}"
    value="$(stored_value_of "$disabled_overrides" "$key")"
    [[ "$value" =~ ^[0-9a-f]{64}$ ]] || fail "disabled-auth regeneration did not store managed $key"
    [[ "$value" != "${disabled_before[$i]}" ]] || fail "disabled-auth regeneration did not rotate $key"
    ! grep -Fq "${disabled_before[$i]}" "$FIXTURE_ROOT/disabled-rotate.out" || fail "disabled-auth regeneration printed prior $key"
    ! grep -Fq "$value" "$FIXTURE_ROOT/disabled-rotate.out" || fail "disabled-auth regeneration printed new $key"
done
grep -Eq '^#[[:space:]]*AUTH_TOKEN=[0-9a-f]{64}$' "$disabled_overrides" || fail "disabled-auth regenerated token is not stored commented"
! grep -Eq '^AUTH_TOKEN=' "$disabled_overrides" || fail "disabled-auth regeneration enabled authentication"
disabled_warning_line="$(grep -Ein 'mapping.*loss|mapping.*invalid' "$FIXTURE_ROOT/disabled-rotate.out" | head -n1 | cut -d: -f1)"
disabled_write_line="$(grep -En 'wrote .*overrides' "$FIXTURE_ROOT/disabled-rotate.out" | head -n1 | cut -d: -f1)"
[[ -n "$disabled_warning_line" && -n "$disabled_write_line" && "$disabled_warning_line" -lt "$disabled_write_line" ]] || fail "disabled-auth mapping-loss warning was not printed before mutation"
ok "disabled auth stores and atomically rotates all five secrets without enabling auth or printing values"

legacy_auth_one=1111111111111111111111111111111111111111111111111111111111111111
legacy_hash_one=2222222222222222222222222222222222222222222222222222222222222222
legacy_encrypt_one=3333333333333333333333333333333333333333333333333333333333333333
legacy_auth_two=4444444444444444444444444444444444444444444444444444444444444444
legacy_hash_two=5555555555555555555555555555555555555555555555555555555555555555
legacy_encrypt_two=6666666666666666666666666666666666666666666666666666666666666666
run_preprivacy_upgrade_flow preprivacy-one "$legacy_auth_one" "$legacy_hash_one" "$legacy_encrypt_one"
run_preprivacy_upgrade_flow preprivacy-two "$legacy_auth_two" "$legacy_hash_two" "$legacy_encrypt_two"
alias_one="$(value_of "$FIXTURE_ROOT/preprivacy-one/overrides.env" PRIVACY_ALIAS_KEY)"
triage_one="$(value_of "$FIXTURE_ROOT/preprivacy-one/overrides.env" PRIVACY_TRIAGE_TOKEN)"
alias_two="$(value_of "$FIXTURE_ROOT/preprivacy-two/overrides.env" PRIVACY_ALIAS_KEY)"
triage_two="$(value_of "$FIXTURE_ROOT/preprivacy-two/overrides.env" PRIVACY_TRIAGE_TOKEN)"
for left in "$alias_one" "$triage_one"; do
    for right in "$alias_two" "$triage_two"; do
        [[ "$left" != "$right" ]] || fail 'independent upgraded installs reused a privacy secret'
    done
done
ok "pre-privacy upgrade and normal re-init mint per-install privacy secrets while preserving the existing three"

grep -Eq '^PRIVACY_ALIAS_KEY=(<[^>]+>|replace-.*)$' "$REPO_ROOT/scripts/.env.example" || fail ".env.example alias key is not a placeholder"
grep -Eq '^PRIVACY_TRIAGE_TOKEN=(<[^>]+>|replace-.*)$' "$REPO_ROOT/scripts/.env.example" || fail ".env.example triage token is not a placeholder"
ok ".env.example contains placeholders only"

printf 'PASS: POSIX managed privacy secrets\n'
