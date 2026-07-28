#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -P "$(dirname "$0")/../.." && pwd)"
template="$repo_root/scripts/.env.example"

normalized_active_assignments() {
    local source="$1"
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
    ' "$source"
}

assignment_count() {
    local assignment="$1"
    local source="$2"
    normalized_active_assignments "$source" | awk -v assignment="$assignment" '
        $0 == assignment { count++ }
        END { print count + 0 }
    '
}

key_count() {
    local key="$1"
    local source="$2"
    normalized_active_assignments "$source" | awk -v key="$key" '
        index($0, key "=") == 1 { count++ }
        END { print count + 0 }
    '
}

assert_once() {
    local assignment="$1"
    local source="$2"
    local key="${assignment%%=*}"

    if [[ "$(assignment_count "$assignment" "$source")" -ne 1 || "$(key_count "$key" "$source")" -ne 1 ]]; then
        echo "expected exactly one active ${assignment}" >&2
        return 1
    fi
}

assert_key_absent() {
    local key="$1"
    local source="$2"

    if [[ "$(key_count "$key" "$source")" -ne 0 ]]; then
        echo "active ${key} must not ship in defaults" >&2
        return 1
    fi
}

check_contract() {
    local source="$1"

    assert_once 'GW_METRICS_REMOTE_WRITE_URL=https://prometheus-prod-66-prod-us-east-3.grafana.net/api/prom/push' "$source" || return 1
    assert_once 'GW_METRICS_REMOTE_WRITE_USER=3370048' "$source" || return 1
    assert_once 'GW_METRICS_REMOTE_WRITE_INTERVAL_SEC=30' "$source" || return 1

    assert_key_absent 'GW_METRICS_REMOTE_WRITE_TOKEN' "$source" || return 1
    assert_key_absent 'GW_METRICS_REMOTE_WRITE_ENABLED' "$source" || return 1
}

assert_enabled_mutation_rejected() {
    local assignment="$1"
    local mutated
    mutated="$(mktemp)"
    cp "$template" "$mutated"
    printf '\n%s\n' "$assignment" >> "$mutated"
    if check_contract "$mutated" >/dev/null 2>&1; then
        rm -f "$mutated"
        echo "defaults contract accepted active enabled mutation: ${assignment}" >&2
        return 1
    fi
    rm -f "$mutated"
}

check_contract "$template"
assert_enabled_mutation_rejected 'GW_METRICS_REMOTE_WRITE_ENABLED=1'
assert_enabled_mutation_rejected '  export GW_METRICS_REMOTE_WRITE_ENABLED = YeS'
assert_enabled_mutation_rejected $'\texport GW_METRICS_REMOTE_WRITE_ENABLED="on"'
