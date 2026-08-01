#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURE_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/gw-privacy-cli-posix.XXXXXX")"
trap 'rm -rf "$FIXTURE_ROOT"' EXIT

fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
assert_contains() { grep -Fq -- "$2" "$1" || fail "$3"; }
assert_not_contains() { ! grep -Fq -- "$2" "$1" || fail "$3"; }

mkdir -p "$FIXTURE_ROOT/home" "$FIXTURE_ROOT/bin"
cat >"$FIXTURE_ROOT/home/.env" <<'EOF'
HTTP_ADDR=127.0.0.1:18080
PRIVACY_TRIAGE_ENABLED=true
PRIVACY_TRIAGE_TOKEN=base-token-must-not-win
EOF
cat >"$FIXTURE_ROOT/home/overrides.env" <<'EOF'
# overrides win
PRIVACY_TRIAGE_TOKEN=override-triage-token-7788
EOF

cat >"$FIXTURE_ROOT/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
: "${GW_TEST_CURL_LOG:?}" "${GW_TEST_CURL_STATE:?}" "${GW_TEST_EXPECT_TOKEN:?}"
printf '%s\n' "$*" >>"$GW_TEST_CURL_LOG"
output=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --output) output="$2"; shift 2 ;;
        *) shift ;;
    esac
done
config="$(cat)"
url="$(printf '%s\n' "$config" | sed -n 's/^url = "\(.*\)"$/\1/p')"
method="$(printf '%s\n' "$config" | sed -n 's/^request = "\(.*\)"$/\1/p')"
case "$url" in
    */admin/api/snapshot) ;;
    *) printf '%s\n' "$config" | grep -Fqx "header = \"Authorization: Bearer $GW_TEST_EXPECT_TOKEN\"" || { printf 'missing protected header\n' >&2; exit 90; } ;;
esac
case "$GW_TEST_CURL_STATE:$method:$url" in
    unavailable:*) exit 7 ;;
    disabled:*:/admin/api/snapshot|disabled:*:*\/admin\/api\/snapshot) body='{"privacy":{"default_profile":"standard","strict_available":true,"triage_enabled":false,"active_scopes":0,"mapping_entries":0}}'; code=200 ;;
    unauthorized:*) body='{"error":{"code":"unauthorized"}}'; code=401 ;;
    enabled:*:*\/admin\/api\/snapshot) body='{"privacy":{"default_profile":"strict","strict_available":true,"triage_enabled":true,"active_scopes":2,"mapping_entries":4}}'; code=200 ;;
    enabled:GET:*\/admin\/api\/privacy\/scopes) body='[{"id":"scope-safe","profile":"strict","state":"active","entries":2,"in_flight":0}]'; code=200 ;;
    enabled:GET:*\/mapping) body='[{"entity":"IPv4","original":"10.0.0.8","synthetic":"198.18.0.8","provenance":"input"}]'; code=200 ;;
    enabled:DELETE:*\/admin\/api\/privacy\/scopes) body=''; code=204 ;;
    enabled:DELETE:*) body='{"state":"closing"}'; code=202 ;;
    *) body='{"error":{"code":"triage_unavailable"}}'; code=503 ;;
esac
[[ -n "$output" ]] && printf '%s' "$body" >"$output"
printf '%s' "$code"
EOF
chmod +x "$FIXTURE_ROOT/bin/curl"

export PATH="$FIXTURE_ROOT/bin:$PATH"
export GW_HOME="$FIXTURE_ROOT/home"
export GW_ADDR="http://127.0.0.1:18080"
export GW_TEST_CURL_LOG="$FIXTURE_ROOT/curl.argv"
export GW_TEST_EXPECT_TOKEN="override-triage-token-7788"

run_privacy() {
    local name="$1"
    shift
    : >"$FIXTURE_ROOT/$name.out"
    if ! GW_TEST_CURL_STATE="${GW_TEST_CURL_STATE:-enabled}" bash "$REPO_ROOT/scripts/gw" privacy "$@" >"$FIXTURE_ROOT/$name.out" 2>&1; then
        cat "$FIXTURE_ROOT/$name.out" >&2
        return 1
    fi
}

: >"$GW_TEST_CURL_LOG"
GW_TEST_CURL_STATE=enabled run_privacy status status
assert_contains "$FIXTURE_ROOT/status.out" 'profile: strict' 'privacy status did not render the safe profile'
assert_contains "$FIXTURE_ROOT/status.out" 'triage: enabled' 'privacy status did not render enabled triage'

GW_TEST_CURL_STATE=disabled run_privacy disabled status
assert_contains "$FIXTURE_ROOT/disabled.out" 'triage: disabled' 'privacy status did not render disabled triage safely'

GW_TEST_CURL_STATE=enabled run_privacy scopes scopes
assert_contains "$FIXTURE_ROOT/scopes.out" 'scope-safe' 'privacy scopes did not render the safe response'
GW_TEST_CURL_STATE=enabled run_privacy inspect inspect scope-safe
assert_contains "$FIXTURE_ROOT/inspect.out" '198.18.0.8' 'privacy inspect did not render the authorized mapping'
GW_TEST_CURL_STATE=enabled run_privacy closing clear scope-safe
assert_contains "$FIXTURE_ROOT/closing.out" 'closing' 'active clear did not render the closing state'
GW_TEST_CURL_STATE=enabled run_privacy clear_all clear --all --yes
assert_contains "$FIXTURE_ROOT/clear_all.out" 'cleared' 'clear-all success was not rendered safely'

before_count="$(wc -l <"$GW_TEST_CURL_LOG" | tr -d ' ')"
if GW_TEST_CURL_STATE=enabled run_privacy missing_yes clear --all; then
    fail 'clear --all succeeded without exact --yes confirmation'
fi
after_count="$(wc -l <"$GW_TEST_CURL_LOG" | tr -d ' ')"
[[ "$before_count" == "$after_count" ]] || fail 'clear --all without --yes contacted the API'

if GW_TEST_CURL_STATE=unauthorized run_privacy unauthorized scopes; then
    fail 'unauthorized privacy request exited zero'
fi
assert_contains "$FIXTURE_ROOT/unauthorized.out" 'unauthorized' 'unauthorized state was not rendered safely'
if GW_TEST_CURL_STATE=unavailable run_privacy unavailable scopes; then
    fail 'unavailable privacy request exited zero'
fi
assert_contains "$FIXTURE_ROOT/unavailable.out" 'unavailable' 'unavailable state was not rendered safely'

if GW_TEST_CURL_STATE=enabled run_privacy unsafe_scope inspect '../escape'; then
    fail 'unsafe scope identifier was accepted'
fi
assert_contains "$FIXTURE_ROOT/unsafe_scope.out" 'invalid scope' 'unsafe scope rejection was not safe and explicit'

before_count="$(wc -l <"$GW_TEST_CURL_LOG" | tr -d ' ')"
saved_gw_addr="$GW_ADDR"
export GW_ADDR='https://privacy.example.test'
if GW_TEST_CURL_STATE=enabled run_privacy remote_target scopes; then
    fail 'non-loopback privacy target was accepted'
fi
export GW_ADDR="$saved_gw_addr"
after_count="$(wc -l <"$GW_TEST_CURL_LOG" | tr -d ' ')"
[[ "$before_count" == "$after_count" ]] || fail 'non-loopback privacy target contacted curl'
assert_contains "$FIXTURE_ROOT/remote_target.out" 'must be loopback' 'non-loopback privacy target was not rejected explicitly'

for output in "$FIXTURE_ROOT"/*.out; do
    assert_not_contains "$output" "$GW_TEST_EXPECT_TOKEN" 'triage token appeared in command output'
done
assert_not_contains "$GW_TEST_CURL_LOG" "$GW_TEST_EXPECT_TOKEN" 'triage token appeared in curl argv'
assert_not_contains "$GW_TEST_CURL_LOG" '--location' 'privacy CLI enabled redirects'
assert_not_contains "$GW_TEST_CURL_LOG" ' -L' 'privacy CLI enabled redirects'
assert_contains "$GW_TEST_CURL_LOG" '--config -' 'privacy CLI did not pass curl configuration on stdin'

printf 'PASS: POSIX privacy CLI\n'
