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
    ' "$1"
}

kiro_levels="$(normalized_active_assignments "$template" | awk -F= '$1 == "KIRO_LOG_LEVEL" { print substr($0, index($0, "=") + 1) }')"
if [[ "$kiro_levels" != "INFO" ]]; then
    echo "expected one effective KIRO_LOG_LEVEL=INFO template default, got: ${kiro_levels:-<unset>}" >&2
    exit 1
fi
