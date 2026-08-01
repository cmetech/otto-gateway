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

grep -Eq '^PRIVACY_ALIAS_KEY=(<[^>]+>|replace-.*)$' "$REPO_ROOT/scripts/.env.example" || fail ".env.example alias key is not a placeholder"
grep -Eq '^PRIVACY_TRIAGE_TOKEN=(<[^>]+>|replace-.*)$' "$REPO_ROOT/scripts/.env.example" || fail ".env.example triage token is not a placeholder"
ok ".env.example contains placeholders only"

printf 'PASS: POSIX managed privacy secrets\n'
