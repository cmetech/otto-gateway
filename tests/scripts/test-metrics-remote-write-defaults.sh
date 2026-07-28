#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -P "$(dirname "$0")/../.." && pwd)"
template="$repo_root/scripts/.env.example"

normalized_active_assignments() {
    awk '
        {
            line = $0
            sub(/\r$/, "", line)
            sub(/^[[:space:]]+/, "", line)
            if (line == "" || line ~ /^#/) next
            sub(/^export[[:space:]]+/, "", line)

            equal = index(line, "=")
            if (!equal) next
            key = substr(line, 1, equal - 1)
            value = substr(line, equal + 1)
            sub(/^[[:space:]]+/, "", key)
            sub(/[[:space:]]+$/, "", key)
            sub(/^[[:space:]]+/, "", value)
            sub(/[[:space:]]+$/, "", value)
            if ((value ~ /^".*"$/) || (value ~ /^\047.*\047$/)) {
                value = substr(value, 2, length(value) - 2)
            }
            print key "=" value
        }
    ' "$template"
}

assignment_count() {
    local assignment="$1"
    normalized_active_assignments | awk -v assignment="$assignment" '
        $0 == assignment { count++ }
        END { print count + 0 }
    '
}

key_count() {
    local key="$1"
    normalized_active_assignments | awk -v key="$key" '
        index($0, key "=") == 1 { count++ }
        END { print count + 0 }
    '
}

assert_once() {
    local assignment="$1"
    local key="${assignment%%=*}"

    if [[ "$(assignment_count "$assignment")" -ne 1 || "$(key_count "$key")" -ne 1 ]]; then
        echo "expected exactly one active ${assignment}" >&2
        exit 1
    fi
}

assert_absent() {
    local assignment="$1"

    if [[ "$(assignment_count "$assignment")" -ne 0 ]]; then
        echo "active ${assignment} must not ship in defaults" >&2
        exit 1
    fi
}

assert_key_absent() {
    local key="$1"

    if [[ "$(key_count "$key")" -ne 0 ]]; then
        echo "active ${key} must not ship in defaults" >&2
        exit 1
    fi
}

assert_once 'GW_METRICS_REMOTE_WRITE_URL=https://prometheus-prod-66-prod-us-east-3.grafana.net/api/prom/push'
assert_once 'GW_METRICS_REMOTE_WRITE_USER=3370048'
assert_once 'GW_METRICS_REMOTE_WRITE_INTERVAL_SEC=30'

assert_key_absent 'GW_METRICS_REMOTE_WRITE_TOKEN'
assert_absent 'GW_METRICS_REMOTE_WRITE_ENABLED=true'
