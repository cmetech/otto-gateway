#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURE_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/gw-privacy-cli-posix.XXXXXX")"
SERVER_PIDS=()
cleanup() {
    local pid
    for pid in "${SERVER_PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
    rm -rf "$FIXTURE_ROOT"
}
trap cleanup EXIT

fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
assert_contains() { grep -Fq -- "$2" "$1" || fail "$3"; }
assert_not_contains() { ! grep -Fq -- "$2" "$1" || fail "$3"; }

mkdir -p "$FIXTURE_ROOT/home" "$FIXTURE_ROOT/bin"
ORIGINAL_PATH="$PATH"
REAL_CURL="$(command -v curl)"
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
if [[ "$GW_TEST_CURL_STATE" == "stalled" ]]; then
    : "${GW_TEST_RESPONSE_PATH_FILE:?}" "${GW_TEST_CURL_PID_FILE:?}"
    printf '[{"entity":"IPv4","original":"10.0.0.8","synthetic":"198.18.0.8"}]' >"$output"
    printf '%s' "$output" >"$GW_TEST_RESPONSE_PATH_FILE"
    printf '%s' "$$" >"$GW_TEST_CURL_PID_FILE"
    while true; do sleep 1; done
fi
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

assert_terminated_privacy_temp_cleanup() {
    local name="$1" response_path_file curl_pid_file wrapper_pid response_path curl_pid attempt
    shift
    response_path_file="$FIXTURE_ROOT/${name}-response.path"
    curl_pid_file="$FIXTURE_ROOT/${name}-curl.pid"
    GW_TEST_CURL_STATE=stalled \
    GW_TEST_RESPONSE_PATH_FILE="$response_path_file" \
    GW_TEST_CURL_PID_FILE="$curl_pid_file" \
    bash "$REPO_ROOT/scripts/gw" privacy "$@" >"$FIXTURE_ROOT/${name}.out" 2>&1 &
    wrapper_pid=$!
    SERVER_PIDS+=("$wrapper_pid")
    for ((attempt = 0; attempt < 100; attempt++)); do
        [[ -s "$response_path_file" && -s "$curl_pid_file" ]] && break
        sleep 0.02
    done
    [[ -s "$response_path_file" && -s "$curl_pid_file" ]] || fail "$name did not publish its cleanup fixture"
    response_path="$(cat "$response_path_file")"
    curl_pid="$(cat "$curl_pid_file")"
    SERVER_PIDS+=("$curl_pid")
    [[ -f "$response_path" ]] || fail "$name response file was not created"
    kill -TERM "$wrapper_pid" 2>/dev/null || true
    sleep 0.1
    kill -TERM "$curl_pid" 2>/dev/null || true
    wait "$wrapper_pid" 2>/dev/null || true
    [[ ! -e "$response_path" ]] || fail "$name left protected content in its response temp file"
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

assert_terminated_privacy_temp_cleanup 'terminated privacy inspect' inspect scope-safe
assert_terminated_privacy_temp_cleanup 'terminated privacy status' status

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

for address_case in \
    'short-ipv4|http://127.1:18080' \
    'integer-ipv4|http://2130706433:18080' \
    'mapped-ipv6|http://[::ffff:127.0.0.1]:18080' \
    'uppercase-scheme|HTTP://127.0.0.1:18080'; do
    address_name="${address_case%%|*}"
    address_value="${address_case#*|}"
    before_count="$(wc -l <"$GW_TEST_CURL_LOG" | tr -d ' ')"
    export GW_ADDR="$address_value"
    if GW_TEST_CURL_STATE=enabled run_privacy "$address_name" scopes; then
        fail "$address_name non-canonical loopback address was accepted"
    fi
    after_count="$(wc -l <"$GW_TEST_CURL_LOG" | tr -d ' ')"
    [[ "$before_count" == "$after_count" ]] || fail "$address_name contacted curl"
    assert_contains "$FIXTURE_ROOT/$address_name.out" 'must be loopback' "$address_name did not report the common loopback grammar"
done
export GW_ADDR="$saved_gw_addr"

for output in "$FIXTURE_ROOT"/*.out; do
    assert_not_contains "$output" "$GW_TEST_EXPECT_TOKEN" 'triage token appeared in command output'
done
assert_not_contains "$GW_TEST_CURL_LOG" "$GW_TEST_EXPECT_TOKEN" 'triage token appeared in curl argv'
assert_not_contains "$GW_TEST_CURL_LOG" '--location' 'privacy CLI enabled redirects'
assert_not_contains "$GW_TEST_CURL_LOG" ' -L' 'privacy CLI enabled redirects'
assert_contains "$GW_TEST_CURL_LOG" '--config -' 'privacy CLI did not pass curl configuration on stdin'

cat >"$FIXTURE_ROOT/http-fixture.py" <<'PY'
import http.server, pathlib, sys, urllib.parse
kind, port_file, request_log, mode_file = sys.argv[1:]
request_log = pathlib.Path(request_log); mode_file = pathlib.Path(mode_file)
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        parsed = urllib.parse.urlsplit(self.path)
        path = parsed.path
        auth = self.headers.get('Authorization', '') != ''
        with request_log.open('a') as stream:
            stream.write(f'{kind} {path} auth={auth}\n')
        if kind == 'proxy':
            self.send_response(200); self.end_headers(); self.wfile.write(b'[{"id":"proxy-escape"}]'); return
        mode = mode_file.read_text().strip()
        if mode == 'redirect' and path == '/admin/api/privacy/scopes':
            port = self.server.server_address[1]
            self.send_response(302)
            self.send_header('Location', f'http://127.0.0.1:{port}/redirect-target')
            self.end_headers(); return
        self.send_response(200); self.end_headers(); self.wfile.write(b'[{"id":"direct-safe"}]')
server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
pathlib.Path(port_file).write_text(str(server.server_address[1]))
server.serve_forever()
PY

wait_port_file() {
    local file="$1" server_pid="$2" attempt server_rc
    for ((attempt = 0; attempt < 500; attempt++)); do
        [[ -s "$file" ]] && return 0
        if ! kill -0 "$server_pid" 2>/dev/null; then
            server_rc=0
            wait "$server_pid" || server_rc=$?
            fail "HTTP fixture exited with status $server_rc before publishing $file"
        fi
        sleep 0.02
    done
    fail "HTTP fixture did not publish $file"
}

hostile_log="$FIXTURE_ROOT/hostile.requests"
hostile_mode="$FIXTURE_ROOT/hostile.mode"
target_port_file="$FIXTURE_ROOT/target.port"
proxy_port_file="$FIXTURE_ROOT/proxy.port"
: >"$hostile_log"
printf 'redirect\n' >"$hostile_mode"
python3 "$FIXTURE_ROOT/http-fixture.py" target "$target_port_file" "$hostile_log" "$hostile_mode" &
target_server_pid=$!
SERVER_PIDS+=("$target_server_pid")
python3 "$FIXTURE_ROOT/http-fixture.py" proxy "$proxy_port_file" "$hostile_log" "$hostile_mode" &
proxy_server_pid=$!
SERVER_PIDS+=("$proxy_server_pid")
wait_port_file "$target_port_file" "$target_server_pid"
wait_port_file "$proxy_port_file" "$proxy_server_pid"
target_port="$(cat "$target_port_file")"
proxy_port="$(cat "$proxy_port_file")"

mkdir -p "$FIXTURE_ROOT/real-bin" "$FIXTURE_ROOT/hostile-home"
ln -s "$REAL_CURL" "$FIXTURE_ROOT/real-bin/curl"
hostile_trace="$FIXTURE_ROOT/hostile.trace"
cat >"$FIXTURE_ROOT/hostile-home/.curlrc" <<EOF
location
trace = "$hostile_trace"
EOF
export PATH="$FIXTURE_ROOT/real-bin:$ORIGINAL_PATH"
export HOME="$FIXTURE_ROOT/hostile-home"
export GW_ADDR="http://127.0.0.1:$target_port"
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY no_proxy || true

if GW_TEST_CURL_STATE=enabled run_privacy hostile_redirect scopes; then
    fail 'hostile curlrc caused the credential-bearing request to follow a redirect'
fi
assert_not_contains "$hostile_log" 'target /redirect-target' 'hostile curlrc caused a second request'
assert_not_contains "$FIXTURE_ROOT/hostile_redirect.out" "$GW_TEST_EXPECT_TOKEN" 'redirect rejection output exposed triage token'
if [[ -f "$hostile_trace" ]]; then
    assert_not_contains "$hostile_trace" "$GW_TEST_EXPECT_TOKEN" 'hostile curlrc trace exposed triage token'
fi

: >"$hostile_log"
rm -f "$hostile_trace"
printf 'direct\n' >"$hostile_mode"
export http_proxy="http://127.0.0.1:$proxy_port"
export https_proxy="$http_proxy"
export ALL_PROXY="$http_proxy"
export NO_PROXY=''
export no_proxy=''
GW_TEST_CURL_STATE=enabled run_privacy hostile_proxy scopes
assert_not_contains "$hostile_log" 'proxy ' 'privacy request inherited a proxy from the environment'
assert_contains "$hostile_log" 'target /admin/api/privacy/scopes auth=True' 'privacy request did not bypass proxies to the loopback target'
assert_not_contains "$FIXTURE_ROOT/hostile_proxy.out" "$GW_TEST_EXPECT_TOKEN" 'proxy-bypass output exposed triage token'
if [[ -f "$hostile_trace" ]]; then
    assert_not_contains "$hostile_trace" "$GW_TEST_EXPECT_TOKEN" 'hostile curlrc trace exposed token during proxy coverage'
fi

assert_contains "$GW_TEST_CURL_LOG" '-q --noproxy * --config -' 'privacy CLI did not put curl config suppression first and bypass proxies'

printf 'PASS: POSIX privacy CLI\n'
