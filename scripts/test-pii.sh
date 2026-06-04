#!/bin/sh
# scripts/test-pii.sh — quick PII / streaming smoke test for OTTO Gateway.
#
# Three scenarios. Default runs all three.
#
#   diag  — print the gateway's PII posture from /health/hooks + /admin/about.
#           No round-trip; useful for "what mode is this install in?"
#   wire  — for each enabled surface, send a 'say hi' request with
#           stream=true AND stream=false and assert the right Content-Type
#           comes back. The v1.9.0 regression check lives here: streaming
#           requests under PII_REDACTION_MODE=encrypt must yield
#           text/event-stream (Anthropic/OpenAI) or application/x-ndjson
#           (Ollama), NOT application/json.
#   pii   — send a PII-rich request to a single surface, capture the
#           response, and verify each PII value round-tripped back to
#           plaintext. Proves the encrypt Pre-hook + decrypt Post-hook
#           pair is working end-to-end.
#
# Usage:
#   ./test-pii.sh [scenario] [flags]
#   ./test-pii.sh diag
#   ./test-pii.sh wire --surface anthropic
#   ./test-pii.sh pii --surface ollama -v
#
# Flags:
#   --surface NAME  anthropic | openai | ollama | all   (default: all)
#   --base URL      Gateway base URL (default: http://127.0.0.1:18080)
#   --auth TOKEN    Bearer token (sets Authorization header on every call)
#   --no-color      Disable ANSI color output
#   -v, --verbose   Print full response bodies on each check
#   -h, --help      Show this usage block
#
# Exit codes:
#   0   all scenarios passed
#   1   at least one scenario failed
#   2   usage error or precondition not met (gateway unreachable, etc.)

set -eu

# ---------------------------------------------------------------------------
# Color + IO helpers. Auto-disable colors when stdout is not a tty.
# ---------------------------------------------------------------------------
COLOR=1
if [ ! -t 1 ]; then COLOR=0; fi

c_reset()  { [ "$COLOR" -eq 1 ] && printf '\033[0m'   || true; }
c_green()  { [ "$COLOR" -eq 1 ] && printf '\033[32m'  || true; }
c_red()    { [ "$COLOR" -eq 1 ] && printf '\033[31m'  || true; }
c_yellow() { [ "$COLOR" -eq 1 ] && printf '\033[33m'  || true; }
c_cyan()   { [ "$COLOR" -eq 1 ] && printf '\033[36m'  || true; }
c_bold()   { [ "$COLOR" -eq 1 ] && printf '\033[1m'   || true; }

ok()    { printf '  %sPASS%s %s\n' "$(c_green)" "$(c_reset)" "$1"; }
fail()  { printf '  %sFAIL%s %s\n' "$(c_red)" "$(c_reset)" "$1"; FAILED=$((FAILED + 1)); }
warn()  { printf '  %s%s%s\n' "$(c_yellow)" "$1" "$(c_reset)"; }
info()  { printf '  %s\n' "$1"; }
section() { printf '\n%s%s== %s ==%s\n' "$(c_bold)" "$(c_cyan)" "$1" "$(c_reset)"; }
verbose() { [ "$VERBOSE" -eq 1 ] && printf '    %s\n' "$1" || true; }

usage() { sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

# ---------------------------------------------------------------------------
# Defaults + arg parsing.
# ---------------------------------------------------------------------------
SCENARIO=""
SURFACE="all"
BASE="${OTTO_BASE_URL:-http://127.0.0.1:18080}"
AUTH=""
VERBOSE=0
FAILED=0
TMPDIR_=$(mktemp -d "${TMPDIR:-/tmp}/otto-test-pii.XXXXXX")
trap 'rm -rf "$TMPDIR_"' EXIT INT TERM

while [ $# -gt 0 ]; do
    case "$1" in
        diag|wire|pii|all) SCENARIO="$1"; shift ;;
        --surface) SURFACE="$2"; shift 2 ;;
        --surface=*) SURFACE="${1#*=}"; shift ;;
        --base) BASE="$2"; shift 2 ;;
        --base=*) BASE="${1#*=}"; shift ;;
        --auth) AUTH="$2"; shift 2 ;;
        --auth=*) AUTH="${1#*=}"; shift ;;
        --no-color) COLOR=0; shift ;;
        -v|--verbose) VERBOSE=1; shift ;;
        -h|--help) usage 0 ;;
        *) printf 'unknown arg: %s\n' "$1" >&2; usage 2 ;;
    esac
done
[ -z "$SCENARIO" ] && SCENARIO="all"

case "$SURFACE" in
    anthropic|openai|ollama|all) ;;
    *) printf '--surface must be one of: anthropic, openai, ollama, all\n' >&2; exit 2 ;;
esac

# ---------------------------------------------------------------------------
# Preconditions: curl available + gateway reachable.
# ---------------------------------------------------------------------------
if ! command -v curl >/dev/null 2>&1; then
    printf 'curl is required; install it and re-run\n' >&2
    exit 2
fi

HAS_JQ=0
if command -v jq >/dev/null 2>&1; then HAS_JQ=1; fi

curl_auth() {
    # curl_auth METHOD URL [extra args...]
    method="$1"; url="$2"; shift 2
    if [ -n "$AUTH" ]; then
        curl -fsS -X "$method" -H "Authorization: Bearer $AUTH" "$url" "$@"
    else
        curl -fsS -X "$method" "$url" "$@"
    fi
}

# Probe gateway availability before any scenario.
if ! curl -fsS "$BASE/health" >/dev/null 2>&1; then
    printf '%sgateway not reachable at %s%s\n' "$(c_red)" "$BASE" "$(c_reset)" >&2
    printf 'check: otto-gw status; otto-gw start\n' >&2
    exit 2
fi

# ---------------------------------------------------------------------------
# Scenario: diag — print PII posture from /health/hooks (no round-trip).
# ---------------------------------------------------------------------------
scenario_diag() {
    section "Diagnostic — PII posture at $BASE"
    body="$TMPDIR_/hooks.json"
    if ! curl_auth GET "$BASE/health/hooks" -o "$body"; then
        fail "/health/hooks did not return 200"
        return
    fi
    ok "/health/hooks 200"

    # Hook list (regression detector for the v1.8.3 ENABLED_HOOKS bug).
    if [ "$HAS_JQ" -eq 1 ]; then
        names=$(jq -r '.hooks[].name' "$body" | tr '\n' ',' | sed 's/,$//')
    else
        names=$(grep -o '"name":"[^"]*"' "$body" | sed 's/"name":"\([^"]*\)"/\1/' | tr '\n' ',' | sed 's/,$//')
    fi
    info "active hooks: $names"

    expected="RequestIDHook,AuthHook,JSONFormatSteeringHook,PIIRedactionHook,LoggingHook"
    if [ "$names" = "$expected" ]; then
        ok "hook chain matches expected default (5 hooks, registration order)"
    else
        warn "hook chain differs from default — expected: $expected"
    fi

    if [ "$HAS_JQ" -eq 1 ]; then
        mode=$(jq -r '.hooks[] | select(.name=="PIIRedactionHook") | .config.mode' "$body")
        enabled=$(jq -r '.hooks[] | select(.name=="PIIRedactionHook") | .config.enabled' "$body")
        decrypt=$(jq -r '.hooks[] | select(.name=="PIIRedactionHook") | .config.decrypt_active' "$body")
        entities=$(jq -r '.hooks[] | select(.name=="PIIRedactionHook") | (.config.entities // []) | join(",")' "$body")
        info "PIIRedactionHook.enabled        = $enabled"
        info "PIIRedactionHook.mode           = $mode"
        info "PIIRedactionHook.decrypt_active = $decrypt"
        info "PIIRedactionHook.entities       = ${entities:-(all)}"
    else
        warn "jq not installed — skipping per-field diagnostics"
        info "install jq for richer diag output, or:"
        info "  curl -sf $BASE/health/hooks"
    fi
}

# ---------------------------------------------------------------------------
# Wire-shape probes for one surface (path, payload, stream-true CT, stream-false CT).
# ---------------------------------------------------------------------------
probe_wire() {
    # probe_wire LABEL PATH PAYLOAD STREAM_TRUE_CT STREAM_FALSE_CT [HEADER...]
    label="$1"; path="$2"; payload="$3"
    want_stream_ct="$4"; want_nonstream_ct="$5"
    shift 5
    info "$(c_bold)$label$(c_reset)  $path"

    # stream=true variant.
    stream_payload=$(printf '%s' "$payload" | sed 's/"stream":false/"stream":true/g; s/}$/,"stream":true}/')
    # If payload already has stream, sed above handles it; else append.
    case "$stream_payload" in
        *'"stream":true'*) ;;
        *) stream_payload="${payload%\}},\"stream\":true}" ;;
    esac

    hdr="$TMPDIR_/wire-stream-true.head"
    if [ -n "$AUTH" ]; then auth_args="-H Authorization: Bearer $AUTH"; else auth_args=""; fi
    # shellcheck disable=SC2086
    curl -fsS -D "$hdr" -o /dev/null \
        -H "Content-Type: application/json" \
        $auth_args "$@" \
        "$BASE$path" -d "$stream_payload" 2>/dev/null || true

    ct=$(awk -F': ' '/^[Cc]ontent-[Tt]ype/{print $2}' "$hdr" | tr -d '\r' | head -1)
    if [ -z "$ct" ]; then
        fail "stream=true: no Content-Type returned (gateway error?)"
        return
    fi
    case "$ct" in
        ${want_stream_ct}*) ok "stream=true → Content-Type: $ct" ;;
        *) fail "stream=true → Content-Type: $ct (want prefix $want_stream_ct)" ;;
    esac

    # stream=false variant.
    nonstream_payload=$(printf '%s' "$payload" | sed 's/"stream":true/"stream":false/g')
    case "$nonstream_payload" in
        *'"stream":false'*) ;;
        *) nonstream_payload="${payload%\}},\"stream\":false}" ;;
    esac

    hdr2="$TMPDIR_/wire-stream-false.head"
    # shellcheck disable=SC2086
    curl -fsS -D "$hdr2" -o /dev/null \
        -H "Content-Type: application/json" \
        $auth_args "$@" \
        "$BASE$path" -d "$nonstream_payload" 2>/dev/null || true

    ct2=$(awk -F': ' '/^[Cc]ontent-[Tt]ype/{print $2}' "$hdr2" | tr -d '\r' | head -1)
    if [ -z "$ct2" ]; then
        fail "stream=false: no Content-Type returned (gateway error?)"
        return
    fi
    case "$ct2" in
        ${want_nonstream_ct}*) ok "stream=false → Content-Type: $ct2" ;;
        *) fail "stream=false → Content-Type: $ct2 (want prefix $want_nonstream_ct)" ;;
    esac
}

scenario_wire() {
    section "Wire shape — Content-Type per surface, stream toggle"
    msg='{"model":"auto","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}'

    if [ "$SURFACE" = "all" ] || [ "$SURFACE" = "anthropic" ]; then
        probe_wire "Anthropic /v1/messages" "/v1/messages" "$msg" \
            "text/event-stream" "application/json" \
            -H "anthropic-version: 2023-06-01"
    fi
    if [ "$SURFACE" = "all" ] || [ "$SURFACE" = "openai" ]; then
        probe_wire "OpenAI /v1/chat/completions" "/v1/chat/completions" "$msg" \
            "text/event-stream" "application/json"
    fi
    if [ "$SURFACE" = "all" ] || [ "$SURFACE" = "ollama" ]; then
        probe_wire "Ollama /api/chat" "/api/chat" "$msg" \
            "application/x-ndjson" "application/json"
    fi
}

# ---------------------------------------------------------------------------
# Scenario: pii — send the PII-record prompt, verify decrypt round-trip.
# ---------------------------------------------------------------------------
PII_PROMPT='Please help me draft a brief on-call handover note.\n\nCustomer profile:\n- Customer name: John Smith\n- City: Boston, Massachusetts\n- Email on file: corey@cmetech.io\n- Phone on file: (415) 555-2671\n- Office GPS: 42.3601 N, 71.0589 W\n- Affected source IPv4: 192.168.1.42\n- Affected source IPv6: 2001:db8::1\n\nFor account verification only (do not reference these in the handover):\n- SSN: 123-45-6789\n- Card on file: 4111-1111-1111-1111\n\nWrite a short handover note for the on-call engineer that:\n1. Greets the on-call engineer.\n2. States the customer name (John Smith) and city (Boston, Massachusetts).\n3. Lists both the IPv4 (192.168.1.42) and IPv6 (2001:db8::1) source addresses so the on-call engineer can grep logs on either stack.\n4. Includes the office GPS 42.3601 N, 71.0589 W so the field team can route to the right office if a site visit is needed.\n5. Provides the email (corey@cmetech.io) and phone ((415) 555-2671) for direct callback.\n6. Ends with: \"Customer is awaiting an update; please reach out directly.\"\n\nKeep the handover under 150 words. Do not reference the SSN or the card on file in your reply.'

# Items the response MUST contain for round-trip to be considered successful.
# Skip entries the model is likely to omit (numbers it may "summarize").
PII_EXPECT_NAME="John Smith"           # NER PERSON
PII_EXPECT_CITY="Boston"               # NER LOCATION (substring of 'Boston, Massachusetts')
PII_EXPECT_EMAIL="corey@cmetech.io"    # Email recognizer
PII_EXPECT_PHONE="(415) 555-2671"      # USPhone recognizer
PII_EXPECT_COORDS="42.3601"            # COORDINATES recognizer (substring — robust to formatting drift)
PII_EXPECT_IPV4="192.168.1.42"         # IPv4 recognizer
PII_EXPECT_IPV6="2001:db8::1"          # IPv6 recognizer

extract_response_text() {
    # extract_response_text SURFACE BODY_FILE → stdout: plain text the client sees
    s="$1"; f="$2"
    case "$s" in
        anthropic)
            if [ "$HAS_JQ" -eq 1 ]; then
                # Aggregated SSE: extract every text_delta.text and concatenate.
                grep '^data: ' "$f" | sed 's/^data: //' | \
                    jq -r 'select(.type=="content_block_delta") | .delta.text' 2>/dev/null | tr -d '\n'
            else
                grep -o '"text":"[^"]*"' "$f" | sed 's/"text":"\(.*\)"/\1/' | tr '\n' ' '
            fi
            ;;
        openai)
            if [ "$HAS_JQ" -eq 1 ]; then
                grep '^data: ' "$f" | grep -v '^data: \[DONE\]' | sed 's/^data: //' | \
                    jq -r '.choices[0].delta.content // empty' 2>/dev/null | tr -d '\n'
            else
                grep -o '"content":"[^"]*"' "$f" | sed 's/"content":"\(.*\)"/\1/' | tr '\n' ' '
            fi
            ;;
        ollama)
            if [ "$HAS_JQ" -eq 1 ]; then
                jq -r '.message.content // empty' < "$f" 2>/dev/null | tr -d '\n'
            else
                grep -o '"content":"[^"]*"' "$f" | sed 's/"content":"\(.*\)"/\1/' | tr '\n' ' '
            fi
            ;;
    esac
}

pii_probe() {
    # pii_probe SURFACE LABEL PATH PAYLOAD [extra curl headers...]
    s="$1"; label="$2"; path="$3"; payload="$4"
    shift 4
    info "$(c_bold)$label$(c_reset)  $path"

    body="$TMPDIR_/pii-resp.body"
    : > "$body"

    if [ -n "$AUTH" ]; then auth_args="-H Authorization: Bearer $AUTH"; else auth_args=""; fi
    # shellcheck disable=SC2086
    curl -fsS -N -o "$body" \
        -H "Content-Type: application/json" \
        $auth_args "$@" \
        "$BASE$path" -d "$payload" 2>/dev/null || true

    if [ ! -s "$body" ]; then
        fail "empty response (gateway error?)"
        return
    fi

    text=$(extract_response_text "$s" "$body")
    verbose "extracted client-visible text: $text"

    # Check that ciphertext tokens are NOT in the response — if they are,
    # decrypt failed and we have a real bug.
    # Entity group [A-Za-z0-9]+ (NOT just letters) catches IPv4 / IPv6 /
    # IMEI / IMSI / MSISDN tokens — entity names that contain digits.
    # Matches the gateway-side decryptTokenRe at internal/plugin/pii/pii.go
    # which had the same letters-only bug silently dropping these tokens;
    # both fixed in v1.9.5.
    if printf '%s' "$text" | grep -Eq '\[PII:[A-Za-z0-9]+:[A-Za-z0-9_-]+\]'; then
        fail "ciphertext leak: response contains a [PII:Entity:base64url] token (decrypt failed)"
        printf '    leaking text: %s\n' "$text" | head -1
        return
    fi
    ok "no ciphertext tokens in response (decrypt did not leak)"

    # Check each expected plaintext PII value appears in the response.
    # 7 entity types — NER PERSON + NER LOCATION + 5 regex recognizers.
    # SSN + CreditCard are NOT asserted (prompt instructs LLM not to echo
    # them; redactor coverage is verified via the cipher-leak check above
    # and via Go-side TestPIIRedactionHook_NEREncryptRoundTrip).
    missing=""
    for needle in "$PII_EXPECT_NAME" "$PII_EXPECT_CITY" "$PII_EXPECT_EMAIL" \
                  "$PII_EXPECT_PHONE" "$PII_EXPECT_COORDS" \
                  "$PII_EXPECT_IPV4" "$PII_EXPECT_IPV6"; do
        if printf '%s' "$text" | grep -qF "$needle"; then
            ok "round-trip decrypt: '$needle' present in response"
        else
            missing="$missing $needle"
            fail "round-trip decrypt: '$needle' NOT in response"
        fi
    done

    if [ -n "$missing" ]; then
        warn "missing values may indicate the LLM did not echo them — try -v"
    fi
}

scenario_pii() {
    section "PII round-trip — encrypt → worker → decrypt → client"
    payload=$(printf '{"model":"auto","max_tokens":512,"messages":[{"role":"user","content":"%s"}],"stream":true}' "$PII_PROMPT")

    if [ "$SURFACE" = "all" ] || [ "$SURFACE" = "anthropic" ]; then
        pii_probe anthropic "Anthropic /v1/messages" "/v1/messages" "$payload" \
            -H "anthropic-version: 2023-06-01"
    fi
    if [ "$SURFACE" = "all" ] || [ "$SURFACE" = "openai" ]; then
        pii_probe openai "OpenAI /v1/chat/completions" "/v1/chat/completions" "$payload"
    fi
    if [ "$SURFACE" = "all" ] || [ "$SURFACE" = "ollama" ]; then
        pii_probe ollama "Ollama /api/chat" "/api/chat" "$payload"
    fi
}

# ---------------------------------------------------------------------------
# Dispatch.
# ---------------------------------------------------------------------------
case "$SCENARIO" in
    diag) scenario_diag ;;
    wire) scenario_wire ;;
    pii)  scenario_pii ;;
    all)
        scenario_diag
        scenario_wire
        scenario_pii
        ;;
esac

printf '\n'
if [ "$FAILED" -gt 0 ]; then
    printf '%s%d check(s) failed%s\n' "$(c_red)" "$FAILED" "$(c_reset)"
    exit 1
fi
printf '%sall checks passed%s\n' "$(c_green)" "$(c_reset)"
