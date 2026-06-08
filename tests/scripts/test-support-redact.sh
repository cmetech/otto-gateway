#!/usr/bin/env bash
# tests/scripts/test-support-redact.sh — unit tests for scripts/lib/redact.sh.
#
# Plain bash harness (no bats dependency) mirroring tests/scripts/publish_test.sh:
# manual pass/fail counters, exit non-zero on any failure, subshell-free.
#
# Coverage (per docs/superpowers/specs/2026-06-08-support-bundle-design.md §Redaction):
#   - redact_stream rewrites Bearer / AUTH_TOKEN= / Authorization: / x-api-key:
#     (incl. case-variants); leaves control lines untouched.
#   - mask_env_value emits "<first 4>…(<N> chars)"; empty input -> empty.
#   - is_secret_key matches AUTH_TOKEN, *PASSWORD*, *SECRET*, *KEY*; rejects
#     non-secret keys like HTTP_ADDR / POOL_SIZE.
set -euo pipefail

REPO_ROOT="$(cd -P "$(dirname "$0")/../.." >/dev/null 2>&1 && pwd)"
# shellcheck source=../../scripts/lib/redact.sh
source "$REPO_ROOT/scripts/lib/redact.sh"

PASS=0
FAIL=0

# fail_with MSG — record a failure and print the diagnostic to stderr.
fail_with() {
    FAIL=$((FAIL + 1))
    echo "FAIL: $*" >&2
}

ok() {
    PASS=$((PASS + 1))
    echo "  ok: $*"
}

# assert_eq EXPECTED ACTUAL LABEL — string equality.
assert_eq() {
    local expected="$1" actual="$2" label="$3"
    if [[ "$expected" == "$actual" ]]; then
        ok "$label"
    else
        fail_with "$label: expected [$expected], got [$actual]"
    fi
}

# assert_contains HAYSTACK NEEDLE LABEL
assert_contains() {
    local haystack="$1" needle="$2" label="$3"
    if [[ "$haystack" == *"$needle"* ]]; then
        ok "$label"
    else
        fail_with "$label: [$needle] not found in [$haystack]"
    fi
}

# assert_not_contains HAYSTACK NEEDLE LABEL
assert_not_contains() {
    local haystack="$1" needle="$2" label="$3"
    if [[ "$haystack" != *"$needle"* ]]; then
        ok "$label"
    else
        fail_with "$label: forbidden [$needle] present in [$haystack]"
    fi
}

echo "== redact_stream =="

# Fixture covers all four log-scrub rules + a control line that must be
# left untouched.
FIXTURE=$(printf '%s\n' \
    'Bearer eyJabc.def-ghi_jkl' \
    'AUTH_TOKEN=supersecretvalue' \
    'PII_HASH_KEY=anotherSecret' \
    'PII_ENCRYPT_KEY=thirdSecret' \
    'Authorization: Bearer foo' \
    'x-api-key: bar' \
    'X-API-KEY: BAZ' \
    'hello world')

REDACTED=$(printf '%s' "$FIXTURE" | redact_stream)

assert_contains "$REDACTED" "Bearer [REDACTED]" "Bearer token rewritten"
assert_contains "$REDACTED" "AUTH_TOKEN=[REDACTED]" "AUTH_TOKEN= line rewritten"
assert_contains "$REDACTED" "PII_HASH_KEY=[REDACTED]" "PII_HASH_KEY= line rewritten"
assert_contains "$REDACTED" "PII_ENCRYPT_KEY=[REDACTED]" "PII_ENCRYPT_KEY= line rewritten"
assert_contains "$REDACTED" "Authorization: [REDACTED]" "Authorization header rewritten"
assert_contains "$REDACTED" "x-api-key: [REDACTED]" "x-api-key (lower) rewritten"
assert_contains "$REDACTED" "X-API-KEY: [REDACTED]" "X-API-KEY (upper) rewritten"
assert_contains "$REDACTED" "hello world" "control line preserved"

# Secret literals MUST NOT appear anywhere in the redacted output.
assert_not_contains "$REDACTED" "supersecretvalue" "AUTH_TOKEN secret absent"
assert_not_contains "$REDACTED" "anotherSecret" "PII_HASH_KEY secret absent"
assert_not_contains "$REDACTED" "thirdSecret" "PII_ENCRYPT_KEY secret absent"
assert_not_contains "$REDACTED" "eyJabc.def-ghi_jkl" "Bearer token secret absent"
assert_not_contains "$REDACTED" "Bearer foo" "Authorization Bearer remnant absent"
assert_not_contains "$REDACTED" " bar" "x-api-key value absent"
assert_not_contains "$REDACTED" "BAZ" "X-API-KEY value absent"

# Idempotency: re-redacting must be a no-op.
REDACTED2=$(printf '%s' "$REDACTED" | redact_stream)
assert_eq "$REDACTED" "$REDACTED2" "redact_stream is idempotent"

echo "== mask_env_value =="

assert_eq "abcd…(12 chars)" "$(mask_env_value abcd1234efgh)" "12-char value masked"
assert_eq "" "$(mask_env_value '')" "empty value yields empty"
assert_eq "abc…(3 chars)" "$(mask_env_value abc)" "3-char value (shorter than prefix) handled"
assert_eq "abcd…(4 chars)" "$(mask_env_value abcd)" "4-char value (exact prefix) handled"
# The literal raw value MUST NEVER round-trip through mask_env_value.
masked_full=$(mask_env_value "supersecretvalue")
assert_not_contains "$masked_full" "supersecretvalue" "full secret literal absent from mask"
assert_contains "$masked_full" "supe…(16 chars)" "mask format matches scripts/otto-gw print_env shape"

echo "== is_secret_key =="

for k in AUTH_TOKEN PII_HASH_KEY PII_ENCRYPT_KEY MY_PASSWORD WEBHOOK_SECRET API_KEY MY_TOKEN PASSPHRASE_FOO auth_token; do
    if is_secret_key "$k"; then
        ok "is_secret_key($k) -> 0"
    else
        fail_with "is_secret_key($k) should be secret but returned non-zero"
    fi
done

for k in HTTP_ADDR POOL_SIZE OTTO_ADDR DEBUG ENABLED_HOOKS PII_REDACTION_MODE; do
    if is_secret_key "$k"; then
        fail_with "is_secret_key($k) should NOT be secret but returned 0"
    else
        ok "is_secret_key($k) -> 1"
    fi
done

# Empty key MUST NOT match (defense against `for k in $LIST; is_secret_key "$k"`
# loops where LIST is empty).
if is_secret_key ""; then
    fail_with "is_secret_key(empty) should not be secret"
else
    ok "is_secret_key(empty) -> 1"
fi

echo
echo "== SUMMARY =="
echo "passed: $PASS"
echo "failed: $FAIL"
if [[ "$FAIL" -gt 0 ]]; then
    exit 1
fi
exit 0
