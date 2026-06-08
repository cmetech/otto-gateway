#!/usr/bin/env bash
# scripts/lib/redact.sh — shared bash redaction primitives for the support
# bundle subcommand. Sourced (not executed); requires bash 4+ (same baseline
# as scripts/otto-gw).
#
# Surface (per docs/superpowers/specs/2026-06-08-support-bundle-design.md):
#   - redact_stream            stdin -> stdout filter applying log-scrub rules
#   - mask_env_value VALUE     echo "<first4>…(<N> chars)"  (empty in -> empty out)
#   - is_secret_key KEY        returns 0 if KEY names a secret env var
#
# Rules MUST be byte-equivalent with scripts/lib/redact.ps1 so behavior does
# not drift between OS wrappers.
#
# Log scrub regexes (applied in order):
#   1. Authorization:<space>.*      -> Authorization: [REDACTED]
#   2. x-api-key:<space>.*           -> x-api-key: [REDACTED]  (case-insensitive)
#   3. Bearer <hex/url-safe-base64>  -> Bearer [REDACTED]
#   4. (^|[^A-Za-z0-9_])(AUTH_TOKEN|PII_HASH_KEY|PII_ENCRYPT_KEY)=<value>
#                                    -> \1KEY=[REDACTED]
#
# Rule (4) intentionally matches the secret-key= form ANYWHERE on the line
# (not just `^`) — slog log entries embed it mid-line as
# `... msg=AUTH_TOKEN=...`. The leading negative-char-class guards against
# matching `MY_AUTH_TOKEN=foo` from a `_AUTH_TOKEN`-suffixed unrelated key.
# Value capture is `\S+` (non-space run) so the next field on the line
# survives. The spec text uses `^` but the integration test (and the spec's
# intent — "lines matching ... -> [REDACTED]") expects the broader match
# to cover real log entries.
#
# Header rules run BEFORE the Bearer rule so that `Authorization: Bearer foo`
# becomes `Authorization: [REDACTED]` (the entire credential, not just the
# token after "Bearer"). The header rules consume the rest of the line, so
# subsequent rules can't fire on a header that's already been collapsed.
#
# Rule (4) is implemented via an explicit character-class pattern instead of
# the GNU-only `I` (case-insensitive) sed flag, because scripts/otto-gw is
# expected to remain BSD-sed-compatible (macOS ships BSD sed). See line ~841
# of scripts/otto-gw for the prior precedent.
#
# `sed -E` (POSIX ERE) is supported by both BSD and GNU sed.

# redact_stream — apply the four log-scrub rules to stdin, write to stdout.
# Idempotent: re-running over an already-redacted stream is a no-op for the
# headers (the placeholder "[REDACTED]" lacks the trigger chars).
redact_stream() {
    sed -E \
        -e 's/(Authorization:[[:space:]]*).*/\1[REDACTED]/g' \
        -e 's/([Xx]-[Aa][Pp][Ii]-[Kk][Ee][Yy]:[[:space:]]*).*/\1[REDACTED]/g' \
        -e 's/Bearer [A-Za-z0-9._-]+/Bearer [REDACTED]/g' \
        -e 's/(^|[^A-Za-z0-9_])(AUTH_TOKEN|PII_HASH_KEY|PII_ENCRYPT_KEY)=[^[:space:]]+/\1\2=[REDACTED]/g'
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
    # /bin/bash is still 3.2 even on Apple Silicon). scripts/otto-gw uses
    # #!/usr/bin/env bash so it picks up Homebrew bash 5+, but this lib
    # should be neutral to either bash major.
    local up
    up=$(printf '%s' "$k" | tr '[:lower:]' '[:upper:]')
    case "$up" in
        AUTH_TOKEN|PII_HASH_KEY|PII_ENCRYPT_KEY) return 0 ;;
    esac
    case "$up" in
        *TOKEN*|*KEY*|*SECRET*|*PASSWORD*|*PASSPHRASE*) return 0 ;;
    esac
    return 1
}
