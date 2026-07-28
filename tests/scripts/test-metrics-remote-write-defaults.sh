#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -P "$(dirname "$0")/../.." && pwd)"
template="$repo_root/scripts/.env.example"

assert_once() {
    local line="$1"
    [[ "$(grep -Fxc "$line" "$template")" -eq 1 ]]
}

assert_once 'GW_METRICS_REMOTE_WRITE_URL=https://prometheus-prod-66-prod-us-east-3.grafana.net/api/prom/push'
assert_once 'GW_METRICS_REMOTE_WRITE_USER=3370048'
assert_once 'GW_METRICS_REMOTE_WRITE_INTERVAL_SEC=30'

if grep -Eq '^GW_METRICS_REMOTE_WRITE_TOKEN=' "$template"; then
    echo 'active remote-write token must not ship in defaults' >&2
    exit 1
fi
if grep -Eq '^GW_METRICS_REMOTE_WRITE_ENABLED=true$' "$template"; then
    echo 'remote write must remain opt-in' >&2
    exit 1
fi
