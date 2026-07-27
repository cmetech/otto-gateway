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

FAKE_ROOT=$(mktemp -d)
EXTRACT_DIR=$(mktemp -d)
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

# A compressed Gateway rotation is retained verbatim. Random input prevents
# compression from defeating the size-cap fixture.
dd if=/dev/urandom bs=1024 count=1536 2>/dev/null | gzip > "$GW_HOME_FIXTURE/logs/gateway-20200101.log.gz"
touch -t 202001010000 "$GW_HOME_FIXTURE/logs/gateway-20200101.log.gz"

# The explicit Kiro path is deliberately relative. Runtime interpretation is
# relative to the final KIRO_CWD, and support collection must find that same
# file rather than resolving it from the shell's cwd.
cat > "$KIRO_CWD_FIXTURE/native/kiro-current.log" <<EOF
kiro current safe
AUTH_TOKEN=$SECRET_TOKEN_LITERAL
EOF
dd if=/dev/urandom bs=1024 count=1536 2>/dev/null | base64 > "$KIRO_CWD_FIXTURE/native/kiro-current.log.1"
printf '%s\n' 'kiro newest rotation safe' > "$KIRO_CWD_FIXTURE/native/kiro-current.log.2"
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
            else:
                self.send_body(200, "application/json", '{"enabled":true')
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

run_support() {
    local mode="$1" out_dir="$2" max_mb="$3" stdout_file="$4" stderr_file="$5"
    local hermes_home=""
    set -- support --out "$out_dir" --max-mb "$max_mb" --log-days 9999
    case "$mode" in
        explicit)
            hermes_home="$DECOY_HERMES_HOME"
            set -- "$@" --co-worker-home "$COWORKER_HOME"
            ;;
        fallback)
            hermes_home="$COWORKER_HOME"
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
        GW_LOG="$GW_HOME_FIXTURE/logs/gateway.log" \
        GW_LOG_BOOT="$GW_HOME_FIXTURE/logs/gateway-boot.log" \
        GW_ADDR="http://127.0.0.1:$HTTP_PORT" \
        KIRO_CWD="$KIRO_CWD_FIXTURE" \
        KIRO_CHAT_LOG_FILE="native/kiro-current.log" \
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
assert_contains "$MAIN_ROOT/logs/gateway/gateway.log" 'AUTH_TOKEN=[REDACTED]' "Gateway log assignments are redacted"
assert_contains "$MAIN_ROOT/logs/gateway/gateway.log" 'GW_METRICS_REMOTE_WRITE_TOKEN=[REDACTED]' "Gateway remote-write assignment is redacted"

assert_file "$MAIN_ROOT/logs/kiro/kiro-chat.log" "relative explicit Kiro current log is found and normalized"
assert_file "$MAIN_ROOT/logs/kiro/kiro-chat.log.1" "Kiro numeric rotation is retained"
assert_file "$MAIN_ROOT/logs/kiro/kiro-chat.log.2" "Kiro newest numeric rotation is retained"
assert_absent "$MAIN_ROOT/logs/kiro/kiro-chat.log.3.gz" "compressed Kiro rotation is excluded"
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
    "$EXCLUDED_SECRET_LITERAL" "$DECOY_SECRET_LITERAL"; do
    if grep -rIF -- "$needle" "$MAIN_ROOT" >/dev/null 2>&1; then
        fail_with "synthetic secret leaked into enabled bundle: $needle"
        grep -rIFln -- "$needle" "$MAIN_ROOT" >&2 || true
    else
        ok "synthetic secret absent: $needle"
    fi
done

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
assert_absent "$CAP_ROOT/logs/kiro/kiro-chat.log.1" "cap drops next-oldest Kiro rotation"
assert_file "$CAP_ROOT/logs/co-worker/agent.log.1" "cap keeps newer Co-worker rotation once under cap"
assert_contains "$CAP_ROOT/MANIFEST.txt" 'DROPPED FOR SIZE:' "manifest accounts for cap omissions"
assert_contains "$CAP_ROOT/MANIFEST.txt" 'gateway-20200101.log.gz' "manifest names dropped Gateway rotation"
assert_contains "$CAP_ROOT/MANIFEST.txt" 'kiro-chat.log.1' "manifest names dropped Kiro rotation"

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

assert_contains "$HTTP_REQUEST_LOG" 'GET /metrics' "support requests metrics with GET"
assert_contains "$HTTP_REQUEST_LOG" 'GET /admin/api/acp-capture?support=redacted' "support requests redacted capture with GET"
if grep -q '^POST ' "$HTTP_REQUEST_LOG"; then
    fail_with "support mutated live state with POST: $(grep '^POST ' "$HTTP_REQUEST_LOG")"
else
    ok "support never sends POST or mutates live capture state"
fi

echo
echo "== SUMMARY =="
echo "passed: $PASS"
echo "failed: $FAIL"
if [[ "$FAIL" -gt 0 ]]; then
    exit 1
fi
exit 0
