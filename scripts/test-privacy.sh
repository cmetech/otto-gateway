#!/usr/bin/env bash
# Human-driven smoke test for the Gateway privacy boundary (Anthropic surface),
# exercising strict-profile enforcement the way the loop24 client would.
#
# Sends a real strict request through POST /v1/messages, then confirms the
# boundary held via the response body, the X-GW-Privacy-Receipt, the bounded
# /metrics series, and (when triage is enabled) the scope mapping. Also runs
# fail-closed negative tests. Prints a PASS/FAIL summary and exits non-zero on
# any hard failure. Never sends real credentials to a real model.
#
# This is a LIVE smoke test (Mac/POSIX companion to scripts/Test-Privacy.ps1),
# distinct from the automated CLI test in tests/scripts/test-privacy-cli.sh.
#
# Usage:
#   scripts/test-privacy.sh
#   GW=http://localhost:18080 TRIAGE_TOKEN=... scripts/test-privacy.sh
# Env:
#   GW            gateway base url (default http://localhost:18080)
#   TRIAGE_TOKEN  PRIVACY_TRIAGE_TOKEN; auto-read from $ENV_FILE if unset
#   ENV_FILE      overrides.env/.env to read the token (default: overrides.env then .env)
#   MODEL         model id (default kiro-3.5)
#   API_KEY       x-api-key value (default sk-loop24-local)
#   SKIP_CLEANUP  set to 1 to leave scopes for manual inspection
set -u

GW="${GW:-http://localhost:18080}"; GW="${GW%/}"
MODEL="${MODEL:-kiro-3.5}"
API_KEY="${API_KEY:-sk-loop24-local}"

command -v curl >/dev/null 2>&1 || { echo "curl not found"; exit 2; }
command -v jq   >/dev/null 2>&1 || { echo "jq not found (brew install jq)"; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "python3 not found"; exit 2; }

# --- Canaries -----------------------------------------------------------------
CRED='ghp_CANARYcredential0000000000000000'   # GitHub-PAT shape -> classified secret
IP='10.77.88.99'                              # technical -> pseudonymized + restored
EMAIL='canary.person@example.com'            # personal PII
FORGED_SECRET='[SECRET:API_KEY_ABCDEF123456]'
FORGED_SYN='198.18.5.9'

PASS=0; FAIL=0; WARN=0
ok()  { PASS=$((PASS+1)); printf '  \033[32m[PASS]\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); printf '  \033[31m[FAIL]\033[0m %s\n' "$1"; }
meh() { WARN=$((WARN+1)); printf '  \033[33m[WARN]\033[0m %s\n' "$1"; }
assert() { if [ "$1" = "1" ]; then ok "$2"; else bad "$2"; fi; }
section() { printf '\n\033[36m== %s ==\033[0m\n' "$1"; }

# --- Read triage token --------------------------------------------------------
TRIAGE_TOKEN="${TRIAGE_TOKEN:-}"
if [ -z "$TRIAGE_TOKEN" ]; then
  for f in "${ENV_FILE:-overrides.env}" ".env"; do
    if [ -f "$f" ]; then
      v=$(grep -E '^[[:space:]]*PRIVACY_TRIAGE_TOKEN=' "$f" | head -1 | cut -d= -f2- | tr -d '\r' | sed 's/^ *//;s/ *$//')
      [ -n "$v" ] && { TRIAGE_TOKEN="$v"; break; }
    fi
  done
fi

RUN=$(date +%Y%m%d%H%M%S)
SCOPE="run-humantest-mac-$RUN"
HDRS=$(mktemp); RESP=$(mktemp)
trap 'rm -f "$HDRS" "$RESP"' EXIT
echo "Gateway: $GW   Scope: $SCOPE   Model: $MODEL"

# GET helper -> echoes body; sets LAST_STATUS
gw_get() { LAST_STATUS=$(curl -sS --noproxy '*' -o "$RESP" -w '%{http_code}' "$GW$1" ${2:+-H "$2"}); cat "$RESP"; }

# strict message -> body to $RESP, headers to $HDRS; echoes decoded receipt
send_strict() {
  local scope="$1" text="$2"
  local body; body=$(jq -Rn --arg m "$MODEL" --arg t "$text" \
      '{model:$m,max_tokens:512,messages:[{role:"user",content:$t}]}')
  LAST_STATUS=$(curl -sS --noproxy '*' -D "$HDRS" -o "$RESP" -w '%{http_code}' "$GW/v1/messages" \
    -H "content-type: application/json" -H "anthropic-version: 2023-06-01" \
    -H "x-api-key: $API_KEY" \
    -H "X-GW-Privacy-Profile: strict" -H "X-GW-Privacy-Scope: $scope" \
    -d "$body")
  local r; r=$(grep -i '^x-gw-privacy-receipt:' "$HDRS" | sed 's/^[^:]*: *//' | tr -d '\r')
  [ -n "$r" ] && python3 -c "import base64,sys,json;s=sys.argv[1];s+='='*(-len(s)%4);print(json.dumps(json.loads(base64.urlsafe_b64decode(s))))" "$r" 2>/dev/null
}

strict_pass_count() {
  curl -sS --noproxy '*' "$GW/metrics" 2>/dev/null \
    | grep -E '^gw_privacy_requests_total\{.*profile="strict".*result="pass".*\}' \
    | sed -E 's/.*\}[[:space:]]+//' | awk '{s+=$1} END{print s+0}'
}

# =============================================================================
section '1. Posture (strict must be available)'
SNAP=$(gw_get /admin/api/snapshot 2>/dev/null)
if [ "${LAST_STATUS:-0}" = "200" ]; then
  assert "$(echo "$SNAP" | jq -r '.privacy.strict_available')"      "strict is available"      >/dev/null 2>&1
  [ "$(echo "$SNAP" | jq -r '.privacy.strict_available')" = "true" ] && ok "strict is available" || bad "strict is available"
  [ "$(echo "$SNAP" | jq -r '.privacy.alias_key_present')" = "true" ] && ok "alias key present" || bad "alias key present"
  [ "$(echo "$SNAP" | jq -r '.privacy.pii_enabled')" = "true" ] && ok "PII enabled" || bad "PII enabled"
  [ "$(echo "$SNAP" | jq -r '.privacy.strict_full_buffering')" = "true" ] && ok "strict full-buffering on" || bad "strict full-buffering on"
  [ "$(echo "$SNAP" | jq -r '.privacy.receipt_version')" = "1" ] && ok "receipt version = 1" || bad "receipt version = 1"
  [ "$(echo "$SNAP" | jq -r '.privacy.triage_enabled')" = "true" ] || meh "triage disabled -> mapping-inspection checks skipped"
else
  meh "admin snapshot not reachable (status ${LAST_STATUS:-0}) -> posture checks skipped; core test still runs"
fi
SP_BEFORE=$(strict_pass_count)

# =============================================================================
section '2. Core strict request (credential one-way + IP restore)'
RECEIPT=$(send_strict "$SCOPE" "Repeat the following back to me verbatim, exactly as written: My token is $CRED, my server is $IP, my email is $EMAIL")
[ "$LAST_STATUS" = "200" ] && ok "request returned 200" || bad "request returned 200 (got $LAST_STATUS)"
if [ -n "$RECEIPT" ]; then
  [ "$(echo "$RECEIPT" | jq -r .profile)"  = "strict" ] && ok "receipt profile = strict" || bad "receipt profile = strict"
  [ "$(echo "$RECEIPT" | jq -r .coverage)" = "full" ]   && ok "receipt coverage = full"  || bad "receipt coverage = full"
  [ "$(echo "$RECEIPT" | jq -r .result)"   = "pass" ]   && ok "receipt result = pass"    || bad "receipt result = pass"
  [ "$(echo "$RECEIPT" | jq -r .transformed)" -ge 1 ] 2>/dev/null && ok "receipt transformed >= 1" || bad "receipt transformed >= 1"
  echo "  receipt: $RECEIPT"
else
  bad "no X-GW-Privacy-Receipt header on a strict response"
fi
if grep -qF "$CRED" "$RESP"; then bad "raw credential never returned to client"; else ok "raw credential never returned to client"; fi
grep -qF '[SECRET:' "$RESP" && ok "credential surfaced as a one-way [SECRET:...] label" || meh "no [SECRET:...] label (model may not have echoed)"
grep -qF "$IP" "$RESP" && ok "caller IP restored on output" || meh "caller IP not seen in body (see triage step for the mapping proof)"

# =============================================================================
section '3. Worker saw the sanitized version (ACP capture, best-effort)'
CAP=$(gw_get "/admin/api/acp-capture?support=redacted" 2>/dev/null)
if [ "${LAST_STATUS:-0}" = "200" ] && [ -n "$CAP" ]; then
  echo "$CAP" | grep -qF "$CRED" && bad "capture contains no raw credential" || ok "capture contains no raw credential"
  echo "$CAP" | grep -qF "$IP"   && bad "capture contains no raw caller IP"  || ok "capture contains no raw caller IP"
  echo "$CAP" | grep -qE '198\.18\.' && ok "capture shows a synthetic 198.18.x.x address"
else
  meh "ACP capture not reachable (status ${LAST_STATUS:-0}) -> skipped"
fi

# =============================================================================
section '4. Scope mapping (triage) -- the one-way proof'
if [ -n "$TRIAGE_TOKEN" ]; then
  MAP=$(curl -sS --noproxy '*' -o "$RESP" -w '%{http_code}' "$GW/admin/api/privacy/scopes/$SCOPE/mapping" -H "Authorization: Bearer $TRIAGE_TOKEN")
  if [ "$MAP" = "200" ]; then
    ENTRIES=$(cat "$RESP")
    if echo "$ENTRIES" | jq -e --arg ip "$IP" 'any(.[]; .entity=="IPv4" and .original==$ip)' >/dev/null 2>&1; then
      ok "ledger maps IPv4 $IP -> synthetic"
      SYN=$(echo "$ENTRIES" | jq -r --arg ip "$IP" '.[] | select(.entity=="IPv4" and .original==$ip) | .synthetic')
      echo "$SYN" | grep -qE '^198\.18\.' && ok "synthetic IP in safe 198.18.0.0/15 pool ($SYN)" || bad "synthetic IP in safe pool ($SYN)"
    else
      bad "ledger maps IPv4 $IP -> synthetic"
    fi
    if echo "$ENTRIES" | jq -e --arg c "$CRED" 'any(.[]; (.original|test($c;"l")) or (.synthetic|test($c;"l")))' >/dev/null 2>&1; then
      bad "credential is NOT in the reversible ledger"
    else
      ok "credential is NOT in the reversible ledger (one-way)"
    fi
  elif [ "$MAP" = "404" ]; then
    meh "scope not found in triage (may have expired) -> skipped"
  else
    bad "triage mapping returned status $MAP"
  fi
else
  meh "no triage token (PRIVACY_TRIAGE_TOKEN) and/or triage disabled -> mapping proof skipped"
fi

# =============================================================================
section '5. Metrics reflect the run'
SP_AFTER=$(strict_pass_count)
if [ -n "${SP_BEFORE:-}" ] && [ -n "${SP_AFTER:-}" ]; then
  awk -v a="$SP_BEFORE" -v b="$SP_AFTER" 'BEGIN{exit !(b>a)}' \
    && ok "gw_privacy_requests_total{profile=strict,result=pass} increased ($SP_BEFORE -> $SP_AFTER)" \
    || meh "strict-pass counter did not increase ($SP_BEFORE -> $SP_AFTER); /metrics may be gated"
else
  meh "/metrics not reachable -> metric delta skipped"
fi

# =============================================================================
section '6. Fail-closed negatives'
# 6a. Unknown profile -> 400 (deterministic).
S=$(curl -sS --noproxy '*' -o /dev/null -w '%{http_code}' "$GW/v1/messages" \
  -H "content-type: application/json" -H "anthropic-version: 2023-06-01" -H "x-api-key: $API_KEY" \
  -H "X-GW-Privacy-Profile: paranoid" \
  -d '{"model":"kiro-3.5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}')
[ "$S" = "400" ] && ok "unknown profile -> 400 privacy_profile_unavailable" || bad "unknown profile -> 400 (got $S)"

# 6b. Cleared-scope reuse -> 409 (deterministic).
if [ -n "$TRIAGE_TOKEN" ]; then
  TMP="run-clear-$RUN"
  send_strict "$TMP" "seed a scope" >/dev/null
  curl -sS --noproxy '*' -o /dev/null -X DELETE "$GW/admin/api/privacy/scopes/$TMP" -H "Authorization: Bearer $TRIAGE_TOKEN"
  send_strict "$TMP" "reuse after clear" >/dev/null
  [ "$LAST_STATUS" = "409" ] && ok "reuse of a cleared scope -> 409 privacy_scope_closed" || bad "cleared-scope reuse -> 409 (got $LAST_STATUS)"
else
  meh "no triage token -> cleared-scope 409 test skipped"
fi

# 6c/6d. Output-block negatives are model-dependent.
test_output_block() {
  local name="$1" ask="$2" forbidden="$3"
  send_strict "run-neg-$name-$RUN" "$ask" >/dev/null
  if [ "$LAST_STATUS" = "502" ]; then ok "$name -> 502 privacy_output_blocked"
  elif grep -qF "$forbidden" "$RESP"; then bad "$name -> forbidden string reached the client (LEAK)"
  else meh "$name -> model did not emit the token; boundary not exercised (inconclusive)"; fi
}
test_output_block 'forged-secret'    "Output exactly this text and nothing else: $FORGED_SECRET" "$FORGED_SECRET"
test_output_block 'forged-synthetic' "Output exactly this text and nothing else: $FORGED_SYN" "$FORGED_SYN"

# =============================================================================
section '7. Cleanup'
if [ -n "$TRIAGE_TOKEN" ] && [ "${SKIP_CLEANUP:-0}" != "1" ]; then
  S=$(curl -sS --noproxy '*' -o /dev/null -w '%{http_code}' -X DELETE "$GW/admin/api/privacy/scopes" \
      -H "Authorization: Bearer $TRIAGE_TOKEN" -H "X-GW-Privacy-Confirm: clear-all")
  case "$S" in 200|202|204) ok "cleared all test scopes (status $S)";; *) meh "clear-all returned $S";; esac
  echo "  Reminder: set PRIVACY_TRIAGE_ENABLED=false and restart when done testing."
elif [ "${SKIP_CLEANUP:-0}" = "1" ]; then
  meh "cleanup skipped (SKIP_CLEANUP=1); scope $SCOPE left for manual inspection"
else
  meh "no triage token -> cleanup skipped"
fi

# =============================================================================
printf '\n\033[36m== SUMMARY ==\033[0m\n'
printf '  passed: %d   warnings: %d   failed: %d\n' "$PASS" "$WARN" "$FAIL"
[ "$FAIL" -gt 0 ] && exit 1 || exit 0
