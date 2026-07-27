#!/usr/bin/env bash
# Integration coverage for `gw support`: run the real wrapper against
# controlled Gateway, Kiro, and Co-worker homes plus a deterministic HTTP
# server, then inspect the extracted archives.
set -euo pipefail

REPO_ROOT="$(cd -P "$(dirname "$0")/../.." >/dev/null 2>&1 && pwd)"
WRAPPER="$REPO_ROOT/scripts/gw"
if [[ ! -x "$WRAPPER" ]]; then
    echo "FATAL: $WRAPPER not executable" >&2
    exit 1
fi

PASS=0
FAIL=0
FAKE_ROOT=""
EXTRACT_DIR=""
HTTP_PID=""

# cleanup is invoked indirectly via trap; SC2329 fires a false positive.
# shellcheck disable=SC2329
cleanup() {
    if [[ -n "$HTTP_PID" ]]; then
        kill "$HTTP_PID" >/dev/null 2>&1 || true
        wait "$HTTP_PID" 2>/dev/null || true
    fi
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

assert_file() {
    local path="$1" label="$2"
    if [[ -f "$path" ]]; then
        ok "$label"
    else
        fail_with "$label: missing regular file $path"
    fi
}

assert_dir() {
    local path="$1" label="$2"
    if [[ -d "$path" ]]; then
        ok "$label"
    else
        fail_with "$label: missing directory $path"
    fi
}

assert_absent() {
    local path="$1" label="$2"
    if [[ ! -e "$path" && ! -L "$path" ]]; then
        ok "$label"
    else
        fail_with "$label: forbidden path exists $path"
    fi
}

assert_contains() {
    local path="$1" needle="$2" label="$3"
    if grep -Fq -- "$needle" "$path" 2>/dev/null; then
        ok "$label"
    else
        fail_with "$label: [$needle] not found in $path"
    fi
}

assert_not_contains() {
    local path="$1" needle="$2" label="$3"
    if grep -Fq -- "$needle" "$path" 2>/dev/null; then
        fail_with "$label: forbidden [$needle] found in $path"
    else
        ok "$label"
    fi
}

assert_gzip_contains() {
    local path="$1" needle="$2" label="$3"
    if gzip -cd "$path" 2>/dev/null | grep -Fq -- "$needle"; then
        ok "$label"
    else
        fail_with "$label: [$needle] not found in decompressed $path"
    fi
}

assert_gzip_not_contains() {
    local path="$1" needle="$2" label="$3"
    if gzip -cd "$path" 2>/dev/null | grep -Fq -- "$needle"; then
        fail_with "$label: forbidden [$needle] present in decompressed $path"
    else
        ok "$label"
    fi
}

assert_no_capture_file() {
    local root="$1" label="$2" candidate
    candidate=$(find "$root/logs/gateway" -maxdepth 1 -type f -name 'acp-capture-*.json' -print 2>/dev/null | head -n 1)
    if [[ -z "$candidate" ]]; then
        ok "$label"
    else
        fail_with "$label: unexpected capture file $candidate"
    fi
}

extract_bundle() {
    local bundle_path="$1" destination="$2"
    mkdir -p "$destination"
    tar -xzf "$bundle_path" -C "$destination"
    local roots=( "$destination"/gateway-support-* )
    printf '%s\n' "${roots[0]}"
}

# Synthetic values must not appear anywhere in an extracted support tree.
SECRET_TOKEN_LITERAL="realsupersecretXYZ"
SECRET_BEARER_LITERAL="realtoken1234deadbeef"
SECRET_HASH_LITERAL="realHashKeyABC987"
SECRET_ENCRYPT_LITERAL="realEncryptKey555"
SECRET_REMOTE_LITERAL="remotesecretvalue987"
EXTERNAL_SECRET_LITERAL="external-symlink-secret-4455"
EXCLUDED_SECRET_LITERAL="excluded-curator-secret-7788"
DECOY_SECRET_LITERAL="wrong-hermes-home-secret-9911"
SUFFIX_DECOY_SECRET_LITERAL="multi-component-rotation-secret-6633"

FAKE_ROOT=$(mktemp -d)
EXTRACT_DIR=$(mktemp -d)
# macOS exposes /var as a symlink to /private/var. Use physical fixture paths
# so the test's normal sources do not themselves cross an ancestor symlink;
# dedicated cases below introduce the only symlink ancestors.
FAKE_ROOT=$(cd -P "$FAKE_ROOT" && pwd)
EXTRACT_DIR=$(cd -P "$EXTRACT_DIR" && pwd)
GW_HOME_FIXTURE="$FAKE_ROOT/gateway-home"
KIRO_CWD_FIXTURE="$FAKE_ROOT/kiro-cwd"
COWORKER_HOME="$FAKE_ROOT/co-worker-home"
DECOY_HERMES_HOME="$FAKE_ROOT/decoy-hermes-home"
OUTSIDE_SECRET="$FAKE_ROOT/outside-secret.log"
mkdir -p \
    "$GW_HOME_FIXTURE/logs" "$GW_HOME_FIXTURE/state" \
    "$KIRO_CWD_FIXTURE/native" \
    "$COWORKER_HOME/logs/curator" "$COWORKER_HOME/profiles/work/logs" \
    "$DECOY_HERMES_HOME/logs" "$FAKE_ROOT/home"

cat > "$GW_HOME_FIXTURE/logs/gateway.log" <<EOF
gateway current safe
AUTH_TOKEN=$SECRET_TOKEN_LITERAL
Authorization: Bearer $SECRET_BEARER_LITERAL
x-api-key: $SECRET_BEARER_LITERAL
PII_HASH_KEY=$SECRET_HASH_LITERAL
PII_ENCRYPT_KEY=$SECRET_ENCRYPT_LITERAL
GW_METRICS_REMOTE_WRITE_TOKEN=$SECRET_REMOTE_LITERAL
EOF
printf '%s\n' 'gateway boot safe' > "$GW_HOME_FIXTURE/logs/gateway-boot.log"
printf '%s\n' 'gateway trace safe' > "$GW_HOME_FIXTURE/logs/gateway-chat-trace.log"

# A large compressed Gateway rotation keeps the size-cap fixture meaningful;
# support must snapshot, decompress, redact, and recompress it.
dd if=/dev/urandom bs=1024 count=1536 2>/dev/null | base64 | gzip > "$GW_HOME_FIXTURE/logs/gateway-20200101.log.gz"
touch -t 202001010000 "$GW_HOME_FIXTURE/logs/gateway-20200101.log.gz"
printf '%s\n' \
    'gateway compressed safe' \
    "GW_METRICS_REMOTE_WRITE_TOKEN=$SECRET_REMOTE_LITERAL" | \
    gzip > "$GW_HOME_FIXTURE/logs/gateway-20260101.log.gz"
touch -t 202601010000 "$GW_HOME_FIXTURE/logs/gateway-20260101.log.gz"

# The explicit Kiro path is deliberately relative. Runtime interpretation is
# relative to the final KIRO_CWD, and support collection must find that same
# file rather than resolving it from the shell's cwd.
cat > "$KIRO_CWD_FIXTURE/native/kiro-current.log" <<EOF
kiro current safe
AUTH_TOKEN=$SECRET_TOKEN_LITERAL
EOF
dd if=/dev/urandom bs=1024 count=1536 2>/dev/null | base64 > "$KIRO_CWD_FIXTURE/native/kiro-current.log.1"
printf '%s\n' 'kiro newest rotation safe' > "$KIRO_CWD_FIXTURE/native/kiro-current.log.2"
printf '%s\n' "$SUFFIX_DECOY_SECRET_LITERAL" > "$KIRO_CWD_FIXTURE/native/kiro-current.log.backup.99"
printf '%s\n' 'compressed kiro must not be copied' | gzip > "$KIRO_CWD_FIXTURE/native/kiro-current.log.3.gz"
touch -t 202101010000 "$KIRO_CWD_FIXTURE/native/kiro-current.log.1"
touch -t 202301010000 "$KIRO_CWD_FIXTURE/native/kiro-current.log.2"

APPROVED_LOGS="agent.log errors.log gateway.log gui.log desktop.log mcp-stderr.log gateway-shutdown-watchdog.log dashboard-auth.log container-boot.log tool_calls.log"
for name in $APPROVED_LOGS; do
    printf '%s safe\n' "$name" > "$COWORKER_HOME/logs/$name"
done
cat >> "$COWORKER_HOME/logs/agent.log" <<EOF
AUTH_TOKEN=$SECRET_TOKEN_LITERAL
GW_METRICS_REMOTE_WRITE_TOKEN=$SECRET_REMOTE_LITERAL
EOF
dd if=/dev/urandom bs=1024 count=1536 2>/dev/null | base64 > "$COWORKER_HOME/logs/agent.log.1"
printf '%s\n' 'co-worker newest rotation safe' > "$COWORKER_HOME/logs/errors.log.2"
printf '%s\n' "$SUFFIX_DECOY_SECRET_LITERAL" > "$COWORKER_HOME/logs/agent.log.private.99"
printf '%s\n' 'compressed co-worker must not be copied' | gzip > "$COWORKER_HOME/logs/errors.log.3.gz"
touch -t 202201010000 "$COWORKER_HOME/logs/agent.log.1"
touch -t 202401010000 "$COWORKER_HOME/logs/errors.log.2"

printf '%s\n' 'profile errors safe' > "$COWORKER_HOME/profiles/work/logs/errors.log"
printf '%s\n' 'profile rotation safe' > "$COWORKER_HOME/profiles/work/logs/errors.log.1"
printf '%s\n' "$EXCLUDED_SECRET_LITERAL" > "$COWORKER_HOME/logs/unrelated.log"
printf '%s\n' "$EXCLUDED_SECRET_LITERAL" > "$COWORKER_HOME/logs/curator/agent.log"
printf '%s\n' "$EXTERNAL_SECRET_LITERAL" > "$OUTSIDE_SECRET"
ln -s "$OUTSIDE_SECRET" "$COWORKER_HOME/logs/agent.log.9"
printf '%s\n' "$DECOY_SECRET_LITERAL" > "$DECOY_HERMES_HOME/logs/agent.log"

# Deterministic live endpoint. The mode file lets later invocations exercise
# enabled, disabled, and invalid capture responses without changing servers.
HTTP_PORT_FILE="$FAKE_ROOT/http-port"
HTTP_REQUEST_LOG="$FAKE_ROOT/http-requests.log"
HTTP_MODE_FILE="$FAKE_ROOT/http-mode"
printf '%s\n' enabled > "$HTTP_MODE_FILE"
python3 - "$HTTP_PORT_FILE" "$HTTP_REQUEST_LOG" "$HTTP_MODE_FILE" <<'PY' &
import http.server
import json
import sys

port_file, request_log, mode_file = sys.argv[1:]

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def send_body(self, status, content_type, body):
        encoded = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def record(self):
        with open(request_log, "a", encoding="utf-8") as out:
            out.write(f"{self.command} {self.path}\n")

    def do_GET(self):
        self.record()
        if self.path == "/metrics":
            self.send_body(200, "text/plain", "# HELP gateway_up fixture\ngateway_up 1\n")
        elif self.path == "/admin/api/acp-capture?support=redacted":
            with open(mode_file, encoding="utf-8") as src:
                mode = src.read().strip()
            if mode == "enabled":
                body = json.dumps({"enabled": True, "allowRuntimeToggle": True, "count": 1, "size": 8, "frames": [{"seq": 7, "method": "session/update", "params": "{\\\"safe\\\":\\\"capture safe\\\",\\\"token\\\":\\\"[REDACTED]\\\"}", "bytes": 64}]})
                self.send_body(200, "application/json", body)
            elif mode == "disabled":
                self.send_body(200, "application/json", '{"enabled":false,"allowRuntimeToggle":true,"count":0,"size":8,"frames":[]}')
            elif mode == "invalid":
                self.send_body(200, "application/json", '{"enabled":true')
            else:
                self.send_body(200, "application/json", '{"enabled":1,"frames":[]}')
        elif self.path == "/health":
            self.send_body(200, "application/json", '{"status":"ok"}')
        elif self.path == "/admin/api/snapshot":
            self.send_body(200, "application/json", '{"fixture":"snapshot"}')
        else:
            self.send_body(404, "text/plain", "not found")

    def do_POST(self):
        self.record()
        self.send_body(405, "text/plain", "read only")

server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(port_file, "w", encoding="ascii") as out:
    out.write(str(server.server_address[1]))
server.serve_forever()
PY
HTTP_PID=$!

for _attempt in 1 2 3 4 5 6 7 8 9 10; do
    [[ -s "$HTTP_PORT_FILE" ]] && break
    sleep 0.1
done
if [[ ! -s "$HTTP_PORT_FILE" ]]; then
    echo "FATAL: deterministic HTTP endpoint did not start" >&2
    exit 1
fi
HTTP_PORT=$(cat "$HTTP_PORT_FILE")

# A hostile user curl config attempts both a POST override and an extra URL.
# Support probes must disable config loading before curl processes either.
cat > "$FAKE_ROOT/home/.curlrc" <<EOF
request = POST
url = http://127.0.0.1:$HTTP_PORT/mutate
EOF

TEST_GW_LOG="$GW_HOME_FIXTURE/logs/gateway.log"
TEST_GW_LOG_BOOT="$GW_HOME_FIXTURE/logs/gateway-boot.log"
TEST_KIRO_CWD="$KIRO_CWD_FIXTURE"
TEST_KIRO_CHAT_LOG_FILE="native/kiro-current.log"
TEST_COWORKER_HOME="$COWORKER_HOME"
TEST_PATH="$PATH"
TEST_SWAP_SOURCE_ONE=""
TEST_SWAP_TARGET_ONE=""
TEST_SWAP_SOURCE_TWO=""
TEST_SWAP_TARGET_TWO=""
TEST_SWAP_ANCESTOR_ONE=""
TEST_SWAP_ANCESTOR_TARGET_ONE=""
TEST_PYTHONOPTIMIZE=""
TEST_REAL_PERL="$(command -v perl)"

reset_support_inputs() {
    TEST_GW_LOG="$GW_HOME_FIXTURE/logs/gateway.log"
    TEST_GW_LOG_BOOT="$GW_HOME_FIXTURE/logs/gateway-boot.log"
    TEST_KIRO_CWD="$KIRO_CWD_FIXTURE"
    TEST_KIRO_CHAT_LOG_FILE="native/kiro-current.log"
    TEST_COWORKER_HOME="$COWORKER_HOME"
    TEST_PATH="$PATH"
    TEST_SWAP_SOURCE_ONE=""
    TEST_SWAP_TARGET_ONE=""
    TEST_SWAP_SOURCE_TWO=""
    TEST_SWAP_TARGET_TWO=""
    TEST_SWAP_ANCESTOR_ONE=""
    TEST_SWAP_ANCESTOR_TARGET_ONE=""
    TEST_PYTHONOPTIMIZE=""
}

run_support() {
    local mode="$1" out_dir="$2" max_mb="$3" stdout_file="$4" stderr_file="$5"
    local hermes_home=""
    set -- support --out "$out_dir" --max-mb "$max_mb" --log-days 9999
    case "$mode" in
        explicit)
            hermes_home="$DECOY_HERMES_HOME"
            set -- "$@" --co-worker-home "$TEST_COWORKER_HOME"
            ;;
        fallback)
            hermes_home="$TEST_COWORKER_HOME"
            ;;
        missing)
            hermes_home=""
            ;;
        *)
            echo "FATAL: unknown test mode $mode" >&2
            return 99
            ;;
    esac

    mkdir -p "$out_dir"
    set +e
    HOME="$FAKE_ROOT/home" \
        GW_INSTALL_DIR="$FAKE_ROOT" \
        GW_HOME="$GW_HOME_FIXTURE" \
        GW_BIN=/bin/true \
        GW_STATE_DIR="$GW_HOME_FIXTURE/state" \
        GW_PID="$GW_HOME_FIXTURE/state/gateway.pid" \
        GW_LOG="$TEST_GW_LOG" \
        GW_LOG_BOOT="$TEST_GW_LOG_BOOT" \
        GW_ADDR="http://127.0.0.1:$HTTP_PORT" \
        KIRO_CWD="$TEST_KIRO_CWD" \
        KIRO_CHAT_LOG_FILE="$TEST_KIRO_CHAT_LOG_FILE" \
        HERMES_HOME="$hermes_home" \
        AUTH_TOKEN="$SECRET_TOKEN_LITERAL" \
        PII_HASH_KEY="$SECRET_HASH_LITERAL" \
        PII_ENCRYPT_KEY="$SECRET_ENCRYPT_LITERAL" \
        GW_METRICS_REMOTE_WRITE_URL="https://metrics.example.test/api/prom/push" \
        GW_METRICS_REMOTE_WRITE_USER="fixture-user" \
        GW_METRICS_REMOTE_WRITE_TOKEN="$SECRET_REMOTE_LITERAL" \
        GW_METRICS_REMOTE_WRITE_INTERVAL_SEC=45 \
        HTTP_ADDR="127.0.0.1:18080" \
        CHAT_TRACE=true \
        KIRO_WORKER_MAX_TURNS=20 \
        PATH="$TEST_PATH" \
        TASK3_SWAP_SOURCE_ONE="$TEST_SWAP_SOURCE_ONE" \
        TASK3_SWAP_TARGET_ONE="$TEST_SWAP_TARGET_ONE" \
        TASK3_SWAP_SOURCE_TWO="$TEST_SWAP_SOURCE_TWO" \
        TASK3_SWAP_TARGET_TWO="$TEST_SWAP_TARGET_TWO" \
        TASK3_SWAP_ANCESTOR_ONE="$TEST_SWAP_ANCESTOR_ONE" \
        TASK3_SWAP_ANCESTOR_TARGET_ONE="$TEST_SWAP_ANCESTOR_TARGET_ONE" \
        TASK3_REAL_PERL="$TEST_REAL_PERL" \
        PYTHONOPTIMIZE="$TEST_PYTHONOPTIMIZE" \
        "$BASH" "$WRAPPER" "$@" >"$stdout_file" 2>"$stderr_file"
    local rc=$?
    set -e
    return "$rc"
}

echo "== enabled support bundle =="
MAIN_OUT="$EXTRACT_DIR/main-out"
MAIN_STDOUT="$EXTRACT_DIR/main.stdout"
MAIN_STDERR="$EXTRACT_DIR/main.stderr"
if run_support fallback "$MAIN_OUT" 50 "$MAIN_STDOUT" "$MAIN_STDERR"; then
    ok "support exits zero with HERMES_HOME fallback"
else
    fail_with "support failed with HERMES_HOME fallback: $(cat "$MAIN_STDERR")"
fi

MAIN_BUNDLE=$(tail -n 1 "$MAIN_STDOUT" 2>/dev/null || true)
if [[ -n "$MAIN_BUNDLE" && -f "$MAIN_BUNDLE" ]]; then
    ok "bundle path is printed and exists"
else
    fail_with "bundle path missing: [$MAIN_BUNDLE]"
    echo "passed: $PASS, failed: $FAIL" >&2
    exit 1
fi
assert_file "$MAIN_OUT/latest.tar.gz" "latest.tar.gz copy exists"
MAIN_ROOT=$(extract_bundle "$MAIN_BUNDLE" "$EXTRACT_DIR/main-tree")
assert_dir "$MAIN_ROOT" "extracted bundle root exists"

for section in env health logs system tray; do
    assert_dir "$MAIN_ROOT/$section" "standard $section section exists"
done
LOG_TOP=$(find "$MAIN_ROOT/logs" -mindepth 1 -maxdepth 1 -type d -exec basename {} \; | sort)
EXPECTED_LOG_TOP=$(printf '%s\n' co-worker gateway kiro)
if [[ "$LOG_TOP" == "$EXPECTED_LOG_TOP" ]]; then
    ok "logs contains exactly gateway, kiro, and co-worker directories"
else
    fail_with "unexpected logs directories: [$LOG_TOP]"
fi
assert_absent "$MAIN_ROOT/logs/gateway.log" "flat Gateway log layout is absent"

assert_file "$MAIN_ROOT/logs/gateway/gateway.log" "Gateway current log is organized"
assert_file "$MAIN_ROOT/logs/gateway/gateway-boot.log" "Gateway boot log is organized"
assert_file "$MAIN_ROOT/logs/gateway/gateway-chat-trace.log" "Gateway chat trace is organized"
assert_file "$MAIN_ROOT/logs/gateway/gateway-20200101.log.gz" "Gateway compressed rotation is retained"
assert_file "$MAIN_ROOT/logs/gateway/gateway-20260101.log.gz" "Gateway credential-bearing compressed rotation is retained"
assert_gzip_contains "$MAIN_ROOT/logs/gateway/gateway-20260101.log.gz" 'GW_METRICS_REMOTE_WRITE_TOKEN=[REDACTED]' "Gateway compressed rotation is decompressed and redacted"
assert_gzip_not_contains "$MAIN_ROOT/logs/gateway/gateway-20260101.log.gz" "$SECRET_REMOTE_LITERAL" "Gateway compressed rotation excludes raw remote-write token"
assert_contains "$MAIN_ROOT/logs/gateway/gateway.log" 'AUTH_TOKEN=[REDACTED]' "Gateway log assignments are redacted"
assert_contains "$MAIN_ROOT/logs/gateway/gateway.log" 'GW_METRICS_REMOTE_WRITE_TOKEN=[REDACTED]' "Gateway remote-write assignment is redacted"

assert_file "$MAIN_ROOT/logs/kiro/kiro-chat.log" "relative explicit Kiro current log is found and normalized"
assert_file "$MAIN_ROOT/logs/kiro/kiro-chat.log.1" "Kiro numeric rotation is retained"
assert_file "$MAIN_ROOT/logs/kiro/kiro-chat.log.2" "Kiro newest numeric rotation is retained"
assert_absent "$MAIN_ROOT/logs/kiro/kiro-chat.log.3.gz" "compressed Kiro rotation is excluded"
assert_absent "$MAIN_ROOT/logs/kiro/kiro-chat.log.99" "multi-component Kiro suffix is not treated as numeric rotation"
assert_contains "$MAIN_ROOT/logs/kiro/kiro-chat.log" 'AUTH_TOKEN=[REDACTED]' "Kiro current log is redacted"

for name in $APPROVED_LOGS; do
    assert_file "$MAIN_ROOT/logs/co-worker/$name" "approved Co-worker $name is retained"
done
assert_file "$MAIN_ROOT/logs/co-worker/agent.log.1" "Co-worker numeric rotation is retained"
assert_file "$MAIN_ROOT/logs/co-worker/errors.log.2" "Co-worker newest numeric rotation is retained"
assert_file "$MAIN_ROOT/logs/co-worker/profiles/work/logs/errors.log" "profile log path is preserved"
assert_file "$MAIN_ROOT/logs/co-worker/profiles/work/logs/errors.log.1" "profile rotation path is preserved"
assert_absent "$MAIN_ROOT/logs/co-worker/errors.log.3.gz" "compressed Co-worker rotation is excluded"
assert_absent "$MAIN_ROOT/logs/co-worker/unrelated.log" "unapproved Co-worker log is excluded"
assert_absent "$MAIN_ROOT/logs/co-worker/curator" "curator tree is excluded"
assert_absent "$MAIN_ROOT/logs/co-worker/agent.log.9" "matching symlink is not followed or archived"
assert_absent "$MAIN_ROOT/logs/co-worker/agent.log.99" "multi-component Co-worker suffix is not treated as numeric rotation"
assert_contains "$MAIN_ROOT/logs/co-worker/agent.log" 'GW_METRICS_REMOTE_WRITE_TOKEN=[REDACTED]' "Co-worker remote-write assignment is redacted"

METRICS_FILES=( "$MAIN_ROOT"/logs/gateway/metrics-snapshot-*.prom )
CAPTURE_FILES=( "$MAIN_ROOT"/logs/gateway/acp-capture-*.json )
if [[ -f "${METRICS_FILES[0]}" && "${#METRICS_FILES[@]}" -eq 1 ]]; then
    ok "one timestamped metrics snapshot is archived"
else
    fail_with "expected one metrics snapshot"
fi
if [[ -f "${CAPTURE_FILES[0]}" && "${#CAPTURE_FILES[@]}" -eq 1 ]]; then
    ok "one timestamped capture snapshot is archived"
else
    fail_with "expected one capture snapshot"
fi
METRICS_BASE=$(basename "${METRICS_FILES[0]}")
CAPTURE_BASE=$(basename "${CAPTURE_FILES[0]}")
SNAPSHOT_TS="${METRICS_BASE#metrics-snapshot-}"
SNAPSHOT_TS="${SNAPSHOT_TS%.prom}"
if [[ "$SNAPSHOT_TS" =~ ^[0-9]{8}-[0-9]{6}Z$ ]]; then
    ok "snapshot timestamp uses UTC YYYYMMDD-HHMMSSZ"
else
    fail_with "invalid snapshot timestamp: $SNAPSHOT_TS"
fi
if [[ "$CAPTURE_BASE" == "acp-capture-$SNAPSHOT_TS.json" ]]; then
    ok "metrics and capture reuse one snapshot timestamp"
else
    fail_with "snapshot timestamps differ: $METRICS_BASE vs $CAPTURE_BASE"
fi
assert_contains "${METRICS_FILES[0]}" 'gateway_up 1' "metrics snapshot preserves Prometheus content"
if python3 -c 'import json,sys; data=json.load(open(sys.argv[1])); assert data["enabled"] is True and data["frames"][0]["seq"] == 7' "${CAPTURE_FILES[0]}"; then
    ok "capture snapshot is enabled valid JSON with expected frame"
else
    fail_with "capture snapshot is not the deterministic valid JSON response"
fi

assert_contains "$MAIN_ROOT/env/effective.env" 'GW_METRICS_REMOTE_WRITE_URL=https://metrics.example.test/api/prom/push' "effective env includes remote-write URL"
assert_contains "$MAIN_ROOT/env/effective.env" 'GW_METRICS_REMOTE_WRITE_USER=fixture-user' "effective env includes remote-write user"
assert_contains "$MAIN_ROOT/env/effective.env" 'GW_METRICS_REMOTE_WRITE_INTERVAL_SEC=45' "effective env includes remote-write interval"
assert_contains "$MAIN_ROOT/env/effective.env" 'GW_METRICS_REMOTE_WRITE_TOKEN=remo…(20 chars)' "effective env masks remote-write token"
assert_contains "$MAIN_ROOT/MANIFEST.txt" 'metrics: captured' "manifest records captured metrics"
assert_contains "$MAIN_ROOT/MANIFEST.txt" 'capture: captured' "manifest records captured capture"
assert_contains "$MAIN_ROOT/MANIFEST.txt" 'sensitive user content' "manifest warns capture can retain sensitive user content"
assert_contains "$MAIN_ROOT/MANIFEST.txt" 'review before sharing' "manifest tells operator to review before sharing"

echo "== extracted-tree secret scan =="
for needle in \
    "$SECRET_TOKEN_LITERAL" "$SECRET_BEARER_LITERAL" "$SECRET_HASH_LITERAL" \
    "$SECRET_ENCRYPT_LITERAL" "$SECRET_REMOTE_LITERAL" "$EXTERNAL_SECRET_LITERAL" \
    "$EXCLUDED_SECRET_LITERAL" "$DECOY_SECRET_LITERAL" "$SUFFIX_DECOY_SECRET_LITERAL"; do
    if grep -rIF -- "$needle" "$MAIN_ROOT" >/dev/null 2>&1; then
        fail_with "synthetic secret leaked into enabled bundle: $needle"
        grep -rIFln -- "$needle" "$MAIN_ROOT" >&2 || true
    else
        ok "synthetic secret absent: $needle"
    fi
done
while IFS= read -r gzip_artifact; do
    if gzip -cd "$gzip_artifact" 2>/dev/null | grep -Fq -- "$SECRET_REMOTE_LITERAL"; then
        fail_with "remote-write token leaked inside compressed archive artifact: $gzip_artifact"
    else
        ok "remote-write token absent from compressed artifact: ${gzip_artifact##*/}"
    fi
done < <(find "$MAIN_ROOT" -type f -name '*.gz' -print)

echo "== cap priority and HERMES_HOME fallback =="
CAP_OUT="$EXTRACT_DIR/cap-out"
CAP_STDOUT="$EXTRACT_DIR/cap.stdout"
CAP_STDERR="$EXTRACT_DIR/cap.stderr"
if run_support explicit "$CAP_OUT" 4 "$CAP_STDOUT" "$CAP_STDERR"; then
    ok "support exits zero with explicit Co-worker home and cap"
else
    fail_with "capped support failed: $(cat "$CAP_STDERR")"
fi
CAP_BUNDLE=$(tail -n 1 "$CAP_STDOUT" 2>/dev/null || true)
CAP_ROOT=$(extract_bundle "$CAP_BUNDLE" "$EXTRACT_DIR/cap-tree")
assert_file "$CAP_ROOT/logs/gateway/gateway.log" "cap preserves Gateway current log"
assert_file "$CAP_ROOT/logs/kiro/kiro-chat.log" "cap preserves Kiro current log"
assert_file "$CAP_ROOT/logs/co-worker/agent.log" "explicit Co-worker home collects current log"
assert_contains "$CAP_ROOT/logs/co-worker/agent.log" 'agent.log safe' "explicit Co-worker home wins over HERMES_HOME"
assert_file "$CAP_ROOT/MANIFEST.txt" "cap preserves manifest"
CAP_METRICS=$(find "$CAP_ROOT/logs/gateway" -maxdepth 1 -type f -name 'metrics-snapshot-*.prom' -print | head -n 1)
CAP_CAPTURE=$(find "$CAP_ROOT/logs/gateway" -maxdepth 1 -type f -name 'acp-capture-*.json' -print | head -n 1)
assert_file "$CAP_METRICS" "cap preserves metrics snapshot"
assert_file "$CAP_CAPTURE" "cap preserves capture snapshot"
assert_absent "$CAP_ROOT/logs/gateway/gateway-20200101.log.gz" "cap drops oldest Gateway rotation first"
assert_file "$CAP_ROOT/logs/kiro/kiro-chat.log.1" "actual archive sizing retains the next rotation once under cap"
assert_file "$CAP_ROOT/logs/co-worker/agent.log.1" "cap keeps newer Co-worker rotation once under cap"
assert_contains "$CAP_ROOT/MANIFEST.txt" 'DROPPED FOR SIZE:' "manifest accounts for cap omissions"
assert_contains "$CAP_ROOT/MANIFEST.txt" 'gateway-20200101.log.gz' "manifest names dropped Gateway rotation"
assert_not_contains "$CAP_ROOT/MANIFEST.txt" 'DROPPED FOR SIZE: logs/kiro/kiro-chat.log.1' "manifest does not claim a retained Kiro rotation was dropped"
CAP_BYTES=$(wc -c < "$CAP_BUNDLE" | tr -d '[:space:]')
if [[ "$CAP_BYTES" -le $((4 * 1024 * 1024)) ]]; then
    ok "actual final archive satisfies 4MB cap after the minimum omission"
else
    fail_with "actual final archive exceeds 4MB cap: $CAP_BYTES bytes"
fi

echo "== final-archive cap accounting =="
OVERHEAD_GATEWAY="$FAKE_ROOT/overhead-gateway"
OVERHEAD_KIRO="$FAKE_ROOT/overhead-kiro"
mkdir -p "$OVERHEAD_GATEWAY" "$OVERHEAD_KIRO"
printf '%s\n' 'overhead current Gateway' > "$OVERHEAD_GATEWAY/gateway.log"
printf '%s\n' 'overhead boot Gateway' > "$OVERHEAD_GATEWAY/gateway-boot.log"
printf '%s\n' 'overhead current Kiro' > "$OVERHEAD_KIRO/kiro.log"
dd if=/dev/urandom bs=1024 count=1030 2>/dev/null | base64 | gzip > "$OVERHEAD_GATEWAY/gateway-overhead.log.gz"
touch -t 201901010000 "$OVERHEAD_GATEWAY/gateway-overhead.log.gz"
for rotation in $(seq 100 180); do
    printf 'manifest row %s\n' "$rotation" > "$OVERHEAD_KIRO/kiro.log.$rotation"
    touch -t 202501010000 "$OVERHEAD_KIRO/kiro.log.$rotation"
done
TEST_GW_LOG="$OVERHEAD_GATEWAY/gateway.log"
TEST_GW_LOG_BOOT="$OVERHEAD_GATEWAY/gateway-boot.log"
TEST_KIRO_CWD="$OVERHEAD_KIRO"
TEST_KIRO_CHAT_LOG_FILE="kiro.log"
UNDERREPORT_BIN="$FAKE_ROOT/underreport-du-bin"
mkdir -p "$UNDERREPORT_BIN"
cat > "$UNDERREPORT_BIN/du" <<'EOF'
#!/bin/sh
last_path=""
for last_path do :; done
printf '1\t%s\n' "$last_path"
EOF
chmod +x "$UNDERREPORT_BIN/du"
TEST_PATH="$UNDERREPORT_BIN:$PATH"
OVERHEAD_OUT="$EXTRACT_DIR/overhead-cap-out"
if run_support missing "$OVERHEAD_OUT" 1 "$EXTRACT_DIR/overhead-cap.stdout" "$EXTRACT_DIR/overhead-cap.stderr"; then
    ok "near-cap support exits zero"
else
    fail_with "near-cap support failed: $(cat "$EXTRACT_DIR/overhead-cap.stderr")"
fi
OVERHEAD_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/overhead-cap.stdout" 2>/dev/null || true)
OVERHEAD_BYTES=$(wc -c < "$OVERHEAD_BUNDLE" | tr -d '[:space:]')
if [[ "$OVERHEAD_BYTES" -le 1048576 ]]; then
    ok "actual final tar archive including manifest and metadata satisfies 1MB cap"
else
    fail_with "actual final tar archive exceeds 1MB cap: $OVERHEAD_BYTES bytes"
fi
OVERHEAD_ROOT=$(extract_bundle "$OVERHEAD_BUNDLE" "$EXTRACT_DIR/overhead-cap-tree")
assert_absent "$OVERHEAD_ROOT/logs/gateway/gateway-overhead.log.gz" "final-archive sizing drops oldest near-cap rotation"
assert_file "$OVERHEAD_ROOT/logs/kiro/kiro-chat.log.180" "final-archive sizing preserves newer rotation once under cap"
assert_contains "$OVERHEAD_ROOT/MANIFEST.txt" 'DROPPED FOR SIZE: logs/gateway/gateway-overhead.log.gz' "manifest accounts for overhead-driven omission"
reset_support_inputs

echo "== disabled and unavailable capture states =="
printf '%s\n' disabled > "$HTTP_MODE_FILE"
DISABLED_OUT="$EXTRACT_DIR/disabled-out"
if run_support missing "$DISABLED_OUT" 50 "$EXTRACT_DIR/disabled.stdout" "$EXTRACT_DIR/disabled.stderr"; then
    ok "support continues without a Co-worker home"
else
    fail_with "support failed without Co-worker home: $(cat "$EXTRACT_DIR/disabled.stderr")"
fi
DISABLED_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/disabled.stdout" 2>/dev/null || true)
DISABLED_ROOT=$(extract_bundle "$DISABLED_BUNDLE" "$EXTRACT_DIR/disabled-tree")
assert_file "$DISABLED_ROOT/logs/gateway/gateway.log" "missing Co-worker home does not stop Gateway collection"
assert_file "$DISABLED_ROOT/logs/kiro/kiro-chat.log" "missing Co-worker home does not stop Kiro collection"
assert_no_capture_file "$DISABLED_ROOT" "disabled capture creates no export"
assert_contains "$DISABLED_ROOT/MANIFEST.txt" 'capture: disabled' "manifest records disabled capture"
assert_contains "$DISABLED_ROOT/MANIFEST.txt" 'Co-worker logs unavailable' "manifest records missing Co-worker home"

printf '%s\n' invalid > "$HTTP_MODE_FILE"
INVALID_OUT="$EXTRACT_DIR/invalid-out"
if run_support missing "$INVALID_OUT" 50 "$EXTRACT_DIR/invalid.stdout" "$EXTRACT_DIR/invalid.stderr"; then
    ok "support tolerates invalid capture JSON"
else
    fail_with "support failed for invalid capture JSON: $(cat "$EXTRACT_DIR/invalid.stderr")"
fi
INVALID_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/invalid.stdout" 2>/dev/null || true)
INVALID_ROOT=$(extract_bundle "$INVALID_BUNDLE" "$EXTRACT_DIR/invalid-tree")
assert_no_capture_file "$INVALID_ROOT" "invalid capture JSON is not archived"
assert_contains "$INVALID_ROOT/MANIFEST.txt" 'capture: unavailable' "manifest records invalid capture as unavailable"

printf '%s\n' nonboolean > "$HTTP_MODE_FILE"
NO_JQ_BIN="$FAKE_ROOT/no-jq-bin"
mkdir -p "$NO_JQ_BIN"
for tool_path in /bin/* /usr/bin/* /usr/sbin/* /sbin/*; do
    [[ -f "$tool_path" && -x "$tool_path" ]] || continue
    tool_name="${tool_path##*/}"
    [[ "$tool_name" == "jq" || -e "$NO_JQ_BIN/$tool_name" ]] && continue
    ln -s "$tool_path" "$NO_JQ_BIN/$tool_name"
done
TEST_PATH="$NO_JQ_BIN"
TEST_PYTHONOPTIMIZE=1
OPTIMIZED_OUT="$EXTRACT_DIR/optimized-python-out"
if run_support missing "$OPTIMIZED_OUT" 50 "$EXTRACT_DIR/optimized-python.stdout" "$EXTRACT_DIR/optimized-python.stderr"; then
    ok "support tolerates non-Boolean capture state under optimized Python fallback"
else
    fail_with "support failed under optimized Python fallback: $(cat "$EXTRACT_DIR/optimized-python.stderr")"
fi
OPTIMIZED_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/optimized-python.stdout" 2>/dev/null || true)
OPTIMIZED_ROOT=$(extract_bundle "$OPTIMIZED_BUNDLE" "$EXTRACT_DIR/optimized-python-tree")
assert_no_capture_file "$OPTIMIZED_ROOT" "optimized Python fallback rejects non-Boolean capture state"
assert_contains "$OPTIMIZED_ROOT/MANIFEST.txt" 'capture: unavailable' "optimized Python rejection is recorded as unavailable"
reset_support_inputs

echo "== Kiro runtime path parity =="
SMALL_GW_DIR="$FAKE_ROOT/small-gateway-logs"
mkdir -p "$SMALL_GW_DIR"
printf '%s\n' 'small gateway current' > "$SMALL_GW_DIR/gateway.log"
printf '%s\n' 'small gateway boot' > "$SMALL_GW_DIR/gateway-boot.log"
TEST_GW_LOG="$SMALL_GW_DIR/gateway.log"
TEST_GW_LOG_BOOT="$SMALL_GW_DIR/gateway-boot.log"

printf '%s\n' 'default Kiro cwd safe' > "$GW_HOME_FIXTURE/default-relative-kiro.log"
TEST_KIRO_CWD=""
TEST_KIRO_CHAT_LOG_FILE="default-relative-kiro.log"
DEFAULT_CWD_OUT="$EXTRACT_DIR/default-cwd-out"
if run_support missing "$DEFAULT_CWD_OUT" 50 "$EXTRACT_DIR/default-cwd.stdout" "$EXTRACT_DIR/default-cwd.stderr"; then
    ok "support continues with empty KIRO_CWD"
else
    fail_with "support failed with empty KIRO_CWD: $(cat "$EXTRACT_DIR/default-cwd.stderr")"
fi
DEFAULT_CWD_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/default-cwd.stdout" 2>/dev/null || true)
DEFAULT_CWD_ROOT=$(extract_bundle "$DEFAULT_CWD_BUNDLE" "$EXTRACT_DIR/default-cwd-tree")
assert_contains "$DEFAULT_CWD_ROOT/logs/kiro/kiro-chat.log" 'default Kiro cwd safe' "empty KIRO_CWD resolves relative Kiro log from GW_HOME"

mkdir -p "$FAKE_ROOT/home/kiro-tilde/native"
printf '%s\n' 'tilde Kiro cwd safe' > "$FAKE_ROOT/home/kiro-tilde/native/tilde-kiro.log"
TEST_KIRO_CWD=\~/kiro-tilde
TEST_KIRO_CHAT_LOG_FILE="native/tilde-kiro.log"
TILDE_CWD_OUT="$EXTRACT_DIR/tilde-cwd-out"
if run_support missing "$TILDE_CWD_OUT" 50 "$EXTRACT_DIR/tilde-cwd.stdout" "$EXTRACT_DIR/tilde-cwd.stderr"; then
    ok "support continues with tilde KIRO_CWD"
else
    fail_with "support failed with tilde KIRO_CWD: $(cat "$EXTRACT_DIR/tilde-cwd.stderr")"
fi
TILDE_CWD_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/tilde-cwd.stdout" 2>/dev/null || true)
TILDE_CWD_ROOT=$(extract_bundle "$TILDE_CWD_BUNDLE" "$EXTRACT_DIR/tilde-cwd-tree")
assert_contains "$TILDE_CWD_ROOT/logs/kiro/kiro-chat.log" 'tilde Kiro cwd safe' "tilde KIRO_CWD expands before resolving relative Kiro log"
reset_support_inputs

echo "== source failure warnings =="
TEST_GW_LOG="$FAKE_ROOT/missing/gateway.log"
TEST_GW_LOG_BOOT="$FAKE_ROOT/missing/gateway-boot.log"
TEST_KIRO_CWD="$FAKE_ROOT/missing"
TEST_KIRO_CHAT_LOG_FILE="kiro.log"
MISSING_OUT="$EXTRACT_DIR/missing-sources-out"
if run_support missing "$MISSING_OUT" 50 "$EXTRACT_DIR/missing-sources.stdout" "$EXTRACT_DIR/missing-sources.stderr"; then
    ok "support continues when configured sources are missing"
else
    fail_with "support failed for missing sources: $(cat "$EXTRACT_DIR/missing-sources.stderr")"
fi
MISSING_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/missing-sources.stdout" 2>/dev/null || true)
MISSING_ROOT=$(extract_bundle "$MISSING_BUNDLE" "$EXTRACT_DIR/missing-sources-tree")
assert_contains "$MISSING_ROOT/MANIFEST.txt" 'Gateway current log missing' "manifest warns for missing configured Gateway current log"
assert_contains "$MISSING_ROOT/MANIFEST.txt" 'Gateway boot log missing' "manifest warns for missing configured Gateway boot log"
assert_contains "$MISSING_ROOT/MANIFEST.txt" 'Kiro current log missing' "manifest warns for missing configured Kiro current log"

FAILURE_ROOT="$FAKE_ROOT/source-failures"
mkdir -p "$FAILURE_ROOT/gateway" "$FAILURE_ROOT/kiro" "$FAILURE_ROOT/co-worker/logs"
printf '%s\n' 'unreadable Gateway' > "$FAILURE_ROOT/gateway/gateway.log"
printf '%s\n' 'readable Gateway boot' > "$FAILURE_ROOT/gateway/gateway-boot.log"
printf '%s\n' 'unreadable Kiro' > "$FAILURE_ROOT/kiro/kiro.log"
printf '%s\n' 'unreadable Co-worker' > "$FAILURE_ROOT/co-worker/logs/agent.log"
chmod 000 "$FAILURE_ROOT/gateway/gateway.log" "$FAILURE_ROOT/kiro/kiro.log" "$FAILURE_ROOT/co-worker/logs/agent.log"
TEST_GW_LOG="$FAILURE_ROOT/gateway/gateway.log"
TEST_GW_LOG_BOOT="$FAILURE_ROOT/gateway/gateway-boot.log"
TEST_KIRO_CWD="$FAILURE_ROOT/kiro"
TEST_KIRO_CHAT_LOG_FILE="kiro.log"
TEST_COWORKER_HOME="$FAILURE_ROOT/co-worker"
UNREADABLE_OUT="$EXTRACT_DIR/unreadable-out"
if run_support fallback "$UNREADABLE_OUT" 50 "$EXTRACT_DIR/unreadable.stdout" "$EXTRACT_DIR/unreadable.stderr"; then
    ok "support continues when configured sources are unreadable"
else
    fail_with "support failed for unreadable sources: $(cat "$EXTRACT_DIR/unreadable.stderr")"
fi
UNREADABLE_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/unreadable.stdout" 2>/dev/null || true)
UNREADABLE_ROOT=$(extract_bundle "$UNREADABLE_BUNDLE" "$EXTRACT_DIR/unreadable-tree")
assert_contains "$UNREADABLE_ROOT/MANIFEST.txt" 'Gateway current log unreadable' "manifest warns for unreadable Gateway source"
assert_contains "$UNREADABLE_ROOT/MANIFEST.txt" 'Kiro current log unreadable' "manifest warns for unreadable Kiro source"
assert_contains "$UNREADABLE_ROOT/MANIFEST.txt" 'Co-worker agent.log unreadable' "manifest warns for unreadable Co-worker source"

chmod 600 "$FAILURE_ROOT/gateway/gateway.log" "$FAILURE_ROOT/kiro/kiro.log" "$FAILURE_ROOT/co-worker/logs/agent.log"
FAIL_BIN="$FAKE_ROOT/failing-redactor-bin"
mkdir -p "$FAIL_BIN"
cat > "$FAIL_BIN/sed" <<'EOF'
#!/bin/sh
exit 9
EOF
chmod +x "$FAIL_BIN/sed"
TEST_PATH="$FAIL_BIN:$PATH"
REDACTION_FAIL_OUT="$EXTRACT_DIR/redaction-fail-out"
if run_support fallback "$REDACTION_FAIL_OUT" 50 "$EXTRACT_DIR/redaction-fail.stdout" "$EXTRACT_DIR/redaction-fail.stderr"; then
    ok "support continues when log redaction fails"
else
    fail_with "support failed when redaction failed: $(cat "$EXTRACT_DIR/redaction-fail.stderr")"
fi
REDACTION_FAIL_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/redaction-fail.stdout" 2>/dev/null || true)
REDACTION_FAIL_ROOT=$(extract_bundle "$REDACTION_FAIL_BUNDLE" "$EXTRACT_DIR/redaction-fail-tree")
assert_contains "$REDACTION_FAIL_ROOT/MANIFEST.txt" 'Gateway current log redaction failed' "manifest warns for Gateway redaction failure"
assert_contains "$REDACTION_FAIL_ROOT/MANIFEST.txt" 'Kiro current log redaction failed' "manifest warns for Kiro redaction failure"
assert_contains "$REDACTION_FAIL_ROOT/MANIFEST.txt" 'Co-worker agent.log redaction failed' "manifest warns for Co-worker redaction failure"
reset_support_inputs

echo "== unavailable safe-open boundary =="
NO_SAFE_OPEN_ROOT="$FAKE_ROOT/no-safe-open"
mkdir -p "$NO_SAFE_OPEN_ROOT/gateway" "$NO_SAFE_OPEN_ROOT/kiro" "$NO_SAFE_OPEN_ROOT/co-worker/logs" "$NO_SAFE_OPEN_ROOT/bin"
printf '%s\n' 'unavailable Gateway current' > "$NO_SAFE_OPEN_ROOT/gateway/gateway.log"
printf '%s\n' 'unavailable Gateway boot' > "$NO_SAFE_OPEN_ROOT/gateway/gateway-boot.log"
printf '%s\n' 'unavailable Kiro' > "$NO_SAFE_OPEN_ROOT/kiro/kiro.log"
printf '%s\n' 'unavailable Co-worker' > "$NO_SAFE_OPEN_ROOT/co-worker/logs/agent.log"
cat > "$NO_SAFE_OPEN_ROOT/bin/perl" <<'EOF'
#!/bin/sh
exit 127
EOF
chmod +x "$NO_SAFE_OPEN_ROOT/bin/perl"
TEST_GW_LOG="$NO_SAFE_OPEN_ROOT/gateway/gateway.log"
TEST_GW_LOG_BOOT="$NO_SAFE_OPEN_ROOT/gateway/gateway-boot.log"
TEST_KIRO_CWD="$NO_SAFE_OPEN_ROOT/kiro"
TEST_KIRO_CHAT_LOG_FILE="kiro.log"
TEST_COWORKER_HOME="$NO_SAFE_OPEN_ROOT/co-worker"
TEST_PATH="$NO_SAFE_OPEN_ROOT/bin:$PATH"
NO_SAFE_OPEN_OUT="$EXTRACT_DIR/no-safe-open-out"
if run_support fallback "$NO_SAFE_OPEN_OUT" 50 "$EXTRACT_DIR/no-safe-open.stdout" "$EXTRACT_DIR/no-safe-open.stderr"; then
    ok "support continues when atomic safe-open is unavailable"
else
    fail_with "support failed without atomic safe-open: $(cat "$EXTRACT_DIR/no-safe-open.stderr")"
fi
NO_SAFE_OPEN_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/no-safe-open.stdout" 2>/dev/null || true)
NO_SAFE_OPEN_BUNDLE_ROOT=$(extract_bundle "$NO_SAFE_OPEN_BUNDLE" "$EXTRACT_DIR/no-safe-open-tree")
assert_absent "$NO_SAFE_OPEN_BUNDLE_ROOT/logs/gateway/gateway.log" "Gateway source is omitted without atomic safe-open"
assert_absent "$NO_SAFE_OPEN_BUNDLE_ROOT/logs/kiro/kiro-chat.log" "Kiro source is omitted without atomic safe-open"
assert_absent "$NO_SAFE_OPEN_BUNDLE_ROOT/logs/co-worker/agent.log" "Co-worker source is omitted without atomic safe-open"
assert_contains "$NO_SAFE_OPEN_BUNDLE_ROOT/MANIFEST.txt" 'Gateway current log safe-open unavailable' "manifest warns when Gateway safe-open is unavailable"
assert_contains "$NO_SAFE_OPEN_BUNDLE_ROOT/MANIFEST.txt" 'Kiro current log safe-open unavailable' "manifest warns when Kiro safe-open is unavailable"
assert_contains "$NO_SAFE_OPEN_BUNDLE_ROOT/MANIFEST.txt" 'Co-worker agent.log safe-open unavailable' "manifest warns when Co-worker safe-open is unavailable"
reset_support_inputs

echo "== atomic no-follow snapshot boundary =="
RACE_ROOT="$FAKE_ROOT/snapshot race"
mkdir -p "$RACE_ROOT/gateway logs" "$RACE_ROOT/kiro/source parent" \
    "$RACE_ROOT/boot target" "$RACE_ROOT/co-worker/logs" "$RACE_ROOT/bin"
printf '%s\n' 'race Gateway current safe' > "$RACE_ROOT/gateway logs/gateway current.log"
printf '%s\n' 'race Kiro safe' > "$RACE_ROOT/kiro/source parent/kiro.log"
printf '%s\n' 'race Gateway rotation safe' | gzip > "$RACE_ROOT/gateway logs/gateway-race binary.log.gz"
RACE_GZIP_SECRET="race-gzip-external-secret-3355"
STATIC_ANCESTOR_SECRET="static-ancestor-external-secret-5577"
printf '%s\n' "$RACE_GZIP_SECRET" | gzip > "$RACE_ROOT/external-secret.log.gz"
printf '%s\n' "$STATIC_ANCESTOR_SECRET" > "$RACE_ROOT/boot target/gateway boot.log"
ln -s "$RACE_ROOT/boot target" "$RACE_ROOT/boot ancestor link"
ln -s "$RACE_ROOT/external-secret.log" "$RACE_ROOT/co-worker/logs/agent.log"
cat > "$RACE_ROOT/bin/perl" <<'EOF'
#!/bin/sh
task3_copy_mode=false
task3_source_one=false
task3_source_two=false
for task3_arg in "$@"; do
    if [ "$task3_arg" = "copy" ]; then
        task3_copy_mode=true
    fi
    if [ -n "${TASK3_SWAP_SOURCE_ONE:-}" ] && [ "$task3_arg" = "$TASK3_SWAP_SOURCE_ONE" ]; then
        task3_source_one=true
    fi
    if [ -n "${TASK3_SWAP_SOURCE_TWO:-}" ] && [ "$task3_arg" = "$TASK3_SWAP_SOURCE_TWO" ]; then
        task3_source_two=true
    fi
done
if [ "$task3_copy_mode" = true ]; then
    if [ "$task3_source_one" = true ] && [ ! -e "$TASK3_SWAP_SOURCE_ONE.before-swap" ]; then
        mv "$TASK3_SWAP_ANCESTOR_ONE" "$TASK3_SWAP_ANCESTOR_ONE.before-swap"
        ln -s "$TASK3_SWAP_ANCESTOR_TARGET_ONE" "$TASK3_SWAP_ANCESTOR_ONE"
    fi
    if [ "$task3_source_two" = true ] && [ ! -e "$TASK3_SWAP_SOURCE_TWO.before-swap" ]; then
        mv "$TASK3_SWAP_SOURCE_TWO" "$TASK3_SWAP_SOURCE_TWO.before-swap"
        ln -s "$TASK3_SWAP_TARGET_TWO" "$TASK3_SWAP_SOURCE_TWO"
    fi
fi
exec "$TASK3_REAL_PERL" "$@"
EOF
chmod +x "$RACE_ROOT/bin/perl"
TEST_GW_LOG="$RACE_ROOT/gateway logs/gateway current.log"
TEST_GW_LOG_BOOT="$RACE_ROOT/boot ancestor link/gateway boot.log"
TEST_KIRO_CWD="$RACE_ROOT/kiro/source parent"
TEST_KIRO_CHAT_LOG_FILE="kiro.log"
TEST_COWORKER_HOME="$RACE_ROOT/co-worker"
TEST_PATH="$RACE_ROOT/bin:$PATH"
TEST_SWAP_SOURCE_ONE="$RACE_ROOT/kiro/source parent/kiro.log"
TEST_SWAP_TARGET_ONE=""
TEST_SWAP_ANCESTOR_ONE="$RACE_ROOT/kiro/source parent"
TEST_SWAP_ANCESTOR_TARGET_ONE="$RACE_ROOT/kiro/source parent.before-swap"
TEST_SWAP_SOURCE_TWO="$RACE_ROOT/gateway logs/gateway-race binary.log.gz"
TEST_SWAP_TARGET_TWO="$RACE_ROOT/external-secret.log.gz"
RACE_OUT="$EXTRACT_DIR/race-out"
if run_support fallback "$RACE_OUT" 50 "$EXTRACT_DIR/race.stdout" "$EXTRACT_DIR/race.stderr"; then
    ok "support continues when sources change to symlinks during snapshot"
else
    fail_with "support failed during deterministic source swap: $(cat "$EXTRACT_DIR/race.stderr")"
fi
RACE_BUNDLE=$(tail -n 1 "$EXTRACT_DIR/race.stdout" 2>/dev/null || true)
RACE_BUNDLE_ROOT=$(extract_bundle "$RACE_BUNDLE" "$EXTRACT_DIR/race-tree")
if [[ -L "$TEST_SWAP_ANCESTOR_ONE" && -f "$TEST_SWAP_ANCESTOR_ONE.before-swap/kiro.log" && \
      -L "$TEST_SWAP_SOURCE_TWO" && -f "$TEST_SWAP_SOURCE_TWO.before-swap" ]]; then
    ok "race seam swaps a plain source ancestor and gzip source immediately before safe-open copy"
else
    fail_with "race seam did not execute at both safe-open copy boundaries"
fi
assert_contains "$RACE_BUNDLE_ROOT/logs/gateway/gateway.log" 'race Gateway current safe' "unraced plain source remains collected"
assert_absent "$RACE_BUNDLE_ROOT/logs/gateway/gateway-boot.log" "source below a static symlink ancestor is rejected"
assert_absent "$RACE_BUNDLE_ROOT/logs/gateway/gateway-race binary.log.gz" "binary compressed source with spaces swapped to symlink is rejected after snapshot"
assert_absent "$RACE_BUNDLE_ROOT/logs/kiro/kiro-chat.log" "source with swapped symlink ancestor is rejected after snapshot"
assert_absent "$RACE_BUNDLE_ROOT/logs/co-worker/agent.log" "static Co-worker symlink is rejected"
assert_contains "$RACE_BUNDLE_ROOT/MANIFEST.txt" 'Gateway boot log rejected: source replaced before safe-open' "manifest records static ancestor symlink rejection"
assert_contains "$RACE_BUNDLE_ROOT/MANIFEST.txt" 'Gateway rotation gateway-race binary.log.gz rejected: source replaced before safe-open' "manifest records raced compressed source replacement"
assert_contains "$RACE_BUNDLE_ROOT/MANIFEST.txt" 'Kiro current log rejected: source replaced before safe-open' "manifest records raced ancestor replacement"
assert_contains "$RACE_BUNDLE_ROOT/MANIFEST.txt" 'Co-worker agent.log rejected: symlink' "manifest records static Co-worker symlink rejection"
if grep -rIF -- "$RACE_GZIP_SECRET" "$RACE_BUNDLE_ROOT" >/dev/null 2>&1 || \
   grep -rIF -- "$STATIC_ANCESTOR_SECRET" "$RACE_BUNDLE_ROOT" >/dev/null 2>&1; then
    fail_with "deterministic symlink swap leaked external content"
else
    ok "deterministic symlink swap leaks no external content"
fi
reset_support_inputs

assert_contains "$HTTP_REQUEST_LOG" 'GET /metrics' "support requests metrics with GET"
assert_contains "$HTTP_REQUEST_LOG" 'GET /admin/api/acp-capture?support=redacted' "support requests redacted capture with GET"
if grep -q '^POST ' "$HTTP_REQUEST_LOG"; then
    fail_with "support mutated live state with POST: $(grep '^POST ' "$HTTP_REQUEST_LOG")"
else
    ok "support never sends POST or mutates live capture state"
fi
if grep -Fq '/mutate' "$HTTP_REQUEST_LOG"; then
    fail_with "support honored malicious curl config URL: $(grep -F '/mutate' "$HTTP_REQUEST_LOG")"
else
    ok "support ignores malicious curl config URLs"
fi

echo
echo "== SUMMARY =="
echo "passed: $PASS"
echo "failed: $FAIL"
if [[ "$FAIL" -gt 0 ]]; then
    exit 1
fi
exit 0
