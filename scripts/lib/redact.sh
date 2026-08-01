#!/usr/bin/env bash
# scripts/lib/redact.sh — shared bash redaction primitives for the support
# bundle subcommand. Sourced (not executed); remains compatible with Bash 3.2.
#
# Surface (per docs/superpowers/specs/2026-06-08-support-bundle-design.md):
#   - redact_stream            stdin -> stdout via Gateway's secret classifier
#   - mask_env_value VALUE     echo "<first4>…(<N> chars)"  (empty in -> empty out)
#   - is_secret_key KEY        returns 0 if KEY names a secret env var
#
# redact_stream delegates all recognition to the hidden Go utility. Output is
# staged until the subprocess exits successfully so callers cannot publish a
# partially redacted artifact.
redact_stream() {
    local redactor="${GW_SUPPORT_REDACTOR_BIN:-${GW_BIN:-}}" output
    [[ -n "$redactor" && -x "$redactor" ]] || return 1
    output="$(mktemp "${TMPDIR:-/tmp}/gw-support-redacted.XXXXXX")"
    if "$redactor" redact-support >"$output"; then
        cat "$output"
        rm -f "$output"
        return 0
    fi
    rm -f "$output"
    return 1
}

# mask_env_value VALUE — echo "<first 4 chars>…(<N> chars)". The literal
# value is NEVER printed. Empty input echoes empty (caller decides whether
# that's an error).
mask_env_value() {
    local v="${1:-}"
    if [[ -z "$v" ]]; then
        echo ""
        return 0
    fi
    # ${v:0:4} returns up to 4 chars even if the string is shorter than 4.
    echo "${v:0:4}…(${#v} chars)"
}

# is_secret_key KEY — returns 0 if KEY identifies a secret env var, 1 otherwise.
# Uppercases KEY first so callers don't need to pre-normalize. Matches either
# (a) an explicit allowlist of known-secret keys, or (b) the substring patterns
# *TOKEN* / *KEY* / *SECRET* / *PASSWORD* / *PASSPHRASE*. The explicit list is
# defense-in-depth — the substring rules already cover them but the explicit
# list survives a future rename that drops the substring marker.
is_secret_key() {
    local k="${1:-}"
    [[ -z "$k" ]] && return 1
    # Use `tr` instead of ${k^^} to stay bash-3-compatible (the macOS
    # /bin/bash is still 3.2 even on Apple Silicon). scripts/gw uses
    # #!/usr/bin/env bash so it picks up Homebrew bash 5+, but this lib
    # should be neutral to either bash major.
    local up
    up=$(printf '%s' "$k" | tr '[:lower:]' '[:upper:]')
    case "$up" in
        AUTH_TOKEN|PII_HASH_KEY|PII_ENCRYPT_KEY|GW_METRICS_REMOTE_WRITE_TOKEN) return 0 ;;
    esac
    case "$up" in
        *TOKEN*|*KEY*|*SECRET*|*PASSWORD*|*PASSPHRASE*) return 0 ;;
    esac
    return 1
}
