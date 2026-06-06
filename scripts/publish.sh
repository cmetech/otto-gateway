#!/usr/bin/env bash
# scripts/publish.sh — push the otto-gateway release set to MinIO + Artifactory.
#
# Design: docs/superpowers/specs/2026-05-28-otto-gateway-publish-script-design.md
# Reference: oscar_app/oscar/utils/bulk_upload_verify.sh (lean rewrite — no
# interactive menu, no per-file selection, no retry; otto-gateway always
# ships exactly five files so we publish them as one atomic set).
#
# This is a publish-only tool. It never modifies dist/, never spawns the
# gateway, never installs anything. Operator install side stays manual
# per docs/operator-quickstart.md.
#
# bash 3.2+ compatible (stock macOS). No associative arrays, no mapfile,
# no ${var,,} lowercasing. [[ ... ]] is fine (matches scripts/otto-gw style).
set -euo pipefail

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Hardcoded distribution endpoints — mirror oscar's values. The
# Artifactory generic repo + path are team-owned and stable; pulling
# them into env vars would invite drift between projects shipping to
# the same channel.
ARTIFACTORY_BASE_URL="https://artifactory.rosetta.ericssondevops.com/artifactory/sd-mana-tmo-prepaid-cdp-pristine-generic"
ARTIFACTORY_PATH="Infra/images"

# ---------------------------------------------------------------------------
# State (script-global; populated by Phase 1)
# ---------------------------------------------------------------------------

VERSION=""
SOURCE_DIR=""
REPO_ROOT=""
DEST=""               # minio | artifactory | both (final, post-narrowing)
DEST_REQUESTED=""     # "" if -d omitted, else the explicit value
ARTIFACTORY_KEY=""    # resolved Artifactory API key (CLI > env > profile)
DRY_RUN=0
LOG_FILE=""

# Parallel arrays — bash 3.2 has no assoc arrays. FILE_NAMES[i] is an
# archive filename; FILE_HASHES[i] is the SHA256 computed in Phase 1.7
# and reused in Phase 3 (mc --attr) and Phase 4 (curl --header). Indexes
# are kept in lock-step; we never resize one without the other.
FILE_NAMES=()
FILE_HASHES=()

# Failure counters (Phase 5 exit-code computation)
MINIO_UPLOADS=0
MINIO_UPLOAD_FAILS=0
ARTIFACTORY_UPLOADS=0
ARTIFACTORY_UPLOAD_FAILS=0
ARTIFACTORY_VERIFIES=0
ARTIFACTORY_VERIFY_FAILS=0
ARTIFACTORY_ABORTED=0

LOCK_FILE=""
LOCK_HELD=0

# ---------------------------------------------------------------------------
# usage — print the help block from the spec and exit 0
# ---------------------------------------------------------------------------
usage() {
    cat <<'EOF'
Usage: scripts/publish.sh [flags]

Publish the otto-gateway release set (4 archives + SHA256SUMS) to MinIO
and/or Artifactory at the team's existing distribution paths.

Examples:
  ./scripts/publish.sh                          # current build, both destinations
  ./scripts/publish.sh -v v1.5.0                # tagged release
  ./scripts/publish.sh -d minio                 # MinIO only
  ./scripts/publish.sh -d artifactory -k <key>  # Artifactory only, explicit key
  ./scripts/publish.sh -n                       # dry-run (no uploads)

Flags:
  -v <version>   Version tag to publish (default: git describe --tags --always --dirty)
  -d <dest>      Destination: minio | artifactory | both
                   omitted: auto-select; narrow if a destination is unavailable
                   explicit: any missing prereq is a hard error (no auto-narrow)
  -k <api-key>   Artifactory API key (default: $ARTIFACTORY_API_KEY env or shell-profile)
  -s <dir>       Source dir holding archives + SHA256SUMS (default: dist)
  -n             Dry-run — print plan, no uploads, no lock, no log file
  -h             This help

Environment overrides (all optional):
  ARTIFACTORY_API_KEY   JFrog API key (required if Artifactory is a destination)
  ARTIFACTORY_CERT_PATH Client cert path (default ../secrets/certs/client.pem)
  ARTIFACTORY_KEY_PATH  Client key path  (default ../secrets/certs/client-key.pem)
  MINIO_ALIAS           mc alias name    (default myminio)
  BUCKET_NAME           MinIO bucket     (default images)

Exit codes:
  0   all uploads + verifications passed
  1   bad flags / missing prereqs / manifest disagreement
  2   release set incomplete in source dir
  3   upload failed (any destination, any file)
  4   upload succeeded but verification failed
  130 interrupted (SIGINT/SIGTERM)
EOF
    exit 0
}

# ---------------------------------------------------------------------------
# log_message — tee one line to stdout and to LOG_FILE (if set). A log
# write failure must NOT abort the script (operator may be running with
# logs/ read-only) — warn once to stderr and continue.
# ---------------------------------------------------------------------------
log_message() {
    local msg="$1"
    printf '%s\n' "$msg"
    if [[ -n "$LOG_FILE" ]]; then
        if ! printf '%s\n' "$msg" >> "$LOG_FILE" 2>/dev/null; then
            printf '[warn] log write failed; continuing without log file\n' >&2
            LOG_FILE=""
        fi
    fi
}

# ---------------------------------------------------------------------------
# fail_with — print an error line in the spec format and exit with code
# ---------------------------------------------------------------------------
fail_with() {
    local code="$1"; shift
    printf 'Error: %s\n' "$1" >&2
    exit "$code"
}

# ---------------------------------------------------------------------------
# resolve_version — CLI flag > git describe > error
# ---------------------------------------------------------------------------
resolve_version() {
    if [[ -n "$VERSION" ]]; then
        return 0
    fi
    local v
    v=$(git describe --tags --always --dirty 2>/dev/null || true)
    if [[ -z "$v" ]]; then
        fail_with 1 "version required (-v) or run from a git checkout. Run with -h for usage."
    fi
    VERSION="$v"
}

# ---------------------------------------------------------------------------
# resolve_repo_root — git toplevel; fall back to script's parent. Used to
# anchor cert path resolution and the logs/ dir.
# ---------------------------------------------------------------------------
resolve_repo_root() {
    REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || true)
    if [[ -z "$REPO_ROOT" ]]; then
        # Fall back to script-dir/.. — robust to operator invoking from
        # an unpacked tarball that isn't a git checkout.
        local script_dir
        script_dir=$(cd "$(dirname "$0")" && pwd)
        REPO_ROOT=$(cd "$script_dir/.." && pwd)
    fi
}

# ---------------------------------------------------------------------------
# discover_release_set — build the 5-entry expected file list from VERSION
# and verify each exists in SOURCE_DIR. Missing → print [ok]/[MISSING]
# per row, exit 2 with hint to rebuild.
# ---------------------------------------------------------------------------
discover_release_set() {
    FILE_NAMES=(
        "otto_gateway-darwin-arm64-${VERSION}.tar.gz"
        "otto_gateway-darwin-amd64-${VERSION}.tar.gz"
        "otto_gateway-linux-amd64-${VERSION}.tar.gz"
        "otto_gateway-windows-amd64-${VERSION}.zip"
        "SHA256SUMS-${VERSION}.txt"
    )
    local missing=0 f status
    for f in "${FILE_NAMES[@]}"; do
        if [[ -f "$SOURCE_DIR/$f" ]]; then
            status="[ok]"
        else
            status="[MISSING]"
            missing=$((missing + 1))
        fi
        printf '  %-10s %s\n' "$status" "$f"
    done
    if [[ "$missing" -gt 0 ]]; then
        printf "\nError: %d release file(s) missing from %s. run 'make package-all' to rebuild.\n" \
            "$missing" "$SOURCE_DIR" >&2
        exit 2
    fi
}

# ---------------------------------------------------------------------------
# sha256_of — print the hex SHA256 of $1. Prefer shasum (macOS + BSD-style
# Linux), fall back to sha256sum (Linux without shasum). Output is the
# bare hash, no filename.
# ---------------------------------------------------------------------------
sha256_of() {
    local file="$1"
    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$file" | awk '{print $1}'
    elif command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$file" | awk '{print $1}'
    else
        printf 'Error: neither shasum nor sha256sum available; install one and re-run.\n' >&2
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# validate_local_manifest — compute SHA256 of each archive, cross-check
# against the matching row in SHA256SUMS-<version>.txt. Cache the
# computed hashes in FILE_HASHES (parallel to FILE_NAMES) so phases 3
# and 4 don't recompute them.
# ---------------------------------------------------------------------------
validate_local_manifest() {
    local manifest="$SOURCE_DIR/SHA256SUMS-${VERSION}.txt"
    local i f computed expected mismatches=()
    FILE_HASHES=()
    for i in "${!FILE_NAMES[@]}"; do
        f="${FILE_NAMES[$i]}"
        if [[ "$f" == SHA256SUMS-* ]]; then
            # Manifest itself has no SHA row in the file; keep an
            # empty placeholder so indexes stay aligned.
            FILE_HASHES[i]=""
            continue
        fi
        computed=$(sha256_of "$SOURCE_DIR/$f")
        FILE_HASHES[i]="$computed"
        # Look up the row in the manifest. The shasum format is
        # "<hash>  <filename>" (two spaces); awk on $1/$2 is robust.
        expected=$(awk -v want="$f" '$2 == want {print $1}' "$manifest")
        if [[ -z "$expected" ]]; then
            mismatches+=("$f: not listed in $(basename "$manifest")")
        elif [[ "$expected" != "$computed" ]]; then
            mismatches+=("$f: expected $expected, got $computed")
        fi
    done
    if [[ "${#mismatches[@]}" -gt 0 ]]; then
        printf 'Error: local manifest disagreement:\n' >&2
        local m
        for m in "${mismatches[@]}"; do
            printf '  %s\n' "$m" >&2
        done
        printf "Hint: re-run 'make package-all' to regenerate consistent artifacts.\n" >&2
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# resolve_artifactory_key — CLI flag > env > shell-profile fallback.
# Mirrors oscar's loop over ~/.zshrc, ~/.bashrc, ~/.profile grepping for
# `export ARTIFACTORY_API_KEY=`.
# ---------------------------------------------------------------------------
resolve_artifactory_key() {
    if [[ -n "$ARTIFACTORY_KEY" ]]; then
        return 0  # set via -k
    fi
    if [[ -n "${ARTIFACTORY_API_KEY:-}" ]]; then
        ARTIFACTORY_KEY="$ARTIFACTORY_API_KEY"
        return 0
    fi
    local profile line val
    for profile in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile"; do
        [[ -f "$profile" ]] || continue
        # Use grep + tail to find the last assignment; tolerate either
        # `export KEY=value` or `KEY=value`. Strip surrounding quotes.
        line=$(grep -E '^(export[[:space:]]+)?ARTIFACTORY_API_KEY=' "$profile" 2>/dev/null | tail -n 1 || true)
        [[ -z "$line" ]] && continue
        val="${line#*=}"
        # Strip optional surrounding quotes (one layer).
        case "$val" in
            \"*\") val="${val#\"}"; val="${val%\"}" ;;
            \'*\') val="${val#\'}"; val="${val%\'}" ;;
        esac
        if [[ -n "$val" ]]; then
            ARTIFACTORY_KEY="$val"
            return 0
        fi
    done
}

# ---------------------------------------------------------------------------
# check_prereqs — collect every missing piece for the requested
# destination set into one error report. Default-d narrows silently
# with a single warning; explicit -d fails on any gap.
#
# Globals read:  DEST_REQUESTED, MINIO_ALIAS, BUCKET_NAME,
#                ARTIFACTORY_CERT_PATH, ARTIFACTORY_KEY_PATH, REPO_ROOT
# Globals set:   DEST (final, post-narrowing)
# ---------------------------------------------------------------------------
check_prereqs() {
    local want_minio=0 want_artifactory=0
    case "$DEST_REQUESTED" in
        minio)        want_minio=1 ;;
        artifactory)  want_artifactory=1 ;;
        both|"")      want_minio=1; want_artifactory=1 ;;
    esac

    # Resolve cert paths (env override > default), anchored at REPO_ROOT.
    local cert_path key_path
    cert_path="${ARTIFACTORY_CERT_PATH:-$REPO_ROOT/../secrets/certs/client.pem}"
    key_path="${ARTIFACTORY_KEY_PATH:-$REPO_ROOT/../secrets/certs/client-key.pem}"

    local minio_gaps=() arti_gaps=()
    # MinIO prereqs
    if [[ "$want_minio" -eq 1 ]]; then
        if ! command -v mc >/dev/null 2>&1; then
            minio_gaps+=("mc not on PATH (install: brew install minio-mc)")
        else
            # Alias must exist or upload will fail at first cp. Catch
            # this in Phase 1 so the operator gets a single combined
            # report instead of discovering it mid-run.
            local alias_name="${MINIO_ALIAS:-myminio}"
            if ! mc alias list "$alias_name" --json 2>/dev/null | grep -q '"status":"success"'; then
                minio_gaps+=("mc alias '$alias_name' not configured (run: mc alias set $alias_name <url> <access> <secret>)")
            fi
        fi
    fi

    # Artifactory prereqs
    if [[ "$want_artifactory" -eq 1 ]]; then
        resolve_artifactory_key
        if [[ -z "$ARTIFACTORY_KEY" ]]; then
            arti_gaps+=("ARTIFACTORY_API_KEY not set (-k flag, env, or shell-profile export)")
        fi
        if [[ ! -r "$cert_path" ]]; then
            arti_gaps+=("cert not readable: $cert_path")
        fi
        if [[ ! -r "$key_path" ]]; then
            arti_gaps+=("key not readable: $key_path")
        fi
        if ! command -v jq >/dev/null 2>&1; then
            arti_gaps+=("jq not on PATH (install: brew install jq)")
        fi
        if ! command -v curl >/dev/null 2>&1; then
            arti_gaps+=("curl not on PATH")
        fi
    fi

    local minio_ok=0 arti_ok=0
    [[ "$want_minio" -eq 1 && "${#minio_gaps[@]}" -eq 0 ]] && minio_ok=1
    [[ "$want_artifactory" -eq 1 && "${#arti_gaps[@]}" -eq 0 ]] && arti_ok=1

    # bash 3.2 + set -u: iterating an empty array via "${arr[@]}" trips
    # "unbound variable". The "${arr[@]+...}" form expands to nothing
    # when the array is empty/unset, side-stepping the strictness.
    if [[ -z "$DEST_REQUESTED" ]]; then
        # Default-d: narrow silently if at least one destination is OK.
        if [[ "$minio_ok" -eq 1 && "$arti_ok" -eq 1 ]]; then
            DEST="both"
        elif [[ "$minio_ok" -eq 1 ]]; then
            DEST="minio"
            printf '[warn] artifactory unavailable, narrowing to minio only:\n' >&2
            local g
            for g in ${arti_gaps[@]+"${arti_gaps[@]}"}; do printf '  - %s\n' "$g" >&2; done
        elif [[ "$arti_ok" -eq 1 ]]; then
            DEST="artifactory"
            printf '[warn] minio unavailable, narrowing to artifactory only:\n' >&2
            local g
            for g in ${minio_gaps[@]+"${minio_gaps[@]}"}; do printf '  - %s\n' "$g" >&2; done
        else
            printf 'Error: no destination available. Missing prereqs:\n' >&2
            local g
            for g in ${minio_gaps[@]+"${minio_gaps[@]}"}; do printf '  minio: %s\n' "$g" >&2; done
            for g in ${arti_gaps[@]+"${arti_gaps[@]}"}; do printf '  artifactory: %s\n' "$g" >&2; done
            exit 1
        fi
    else
        # Explicit -d: any gap is fatal. Report ALL gaps for whichever
        # destinations were requested — never make the operator re-run.
        local total_gaps=0
        total_gaps=$(( ${#minio_gaps[@]} + ${#arti_gaps[@]} ))
        if [[ "$total_gaps" -gt 0 ]]; then
            printf 'Error: prereqs missing for -d %s:\n' "$DEST_REQUESTED" >&2
            local g
            for g in ${minio_gaps[@]+"${minio_gaps[@]}"}; do printf '  minio: %s\n' "$g" >&2; done
            for g in ${arti_gaps[@]+"${arti_gaps[@]}"}; do printf '  artifactory: %s\n' "$g" >&2; done
            exit 1
        fi
        DEST="$DEST_REQUESTED"
    fi

    # Expose the resolved cert/key paths to the upload phase.
    ARTIFACTORY_CERT_PATH="$cert_path"
    ARTIFACTORY_KEY_PATH="$key_path"
}

# ---------------------------------------------------------------------------
# acquire_lock / release_lock — dist/.publish.lock with PID. Stale-PID
# detection via kill -0. Trap SIGINT/SIGTERM/EXIT to release on every
# termination path.
# ---------------------------------------------------------------------------
acquire_lock() {
    LOCK_FILE="$SOURCE_DIR/.publish.lock"
    if [[ -f "$LOCK_FILE" ]]; then
        local existing
        existing=$(cat "$LOCK_FILE" 2>/dev/null || echo "")
        if [[ -n "$existing" ]] && kill -0 "$existing" 2>/dev/null; then
            printf 'Error: another publish appears to be running (PID %s).\n' "$existing" >&2
            # Don't release on exit — we don't own the lock.
            LOCK_FILE=""
            exit 1
        fi
        printf '[warn] stale publish lock at %s (PID %s not running) — reclaiming\n' \
            "$LOCK_FILE" "${existing:-unknown}" >&2
    fi
    printf '%s\n' "$$" > "$LOCK_FILE"
    LOCK_HELD=1
}

# shellcheck disable=SC2329
# Invoked via `trap release_lock EXIT` — shellcheck can't see it.
release_lock() {
    if [[ "$LOCK_HELD" -eq 1 && -n "$LOCK_FILE" && -f "$LOCK_FILE" ]]; then
        local existing
        existing=$(cat "$LOCK_FILE" 2>/dev/null || echo "")
        # Only remove if we still own it.
        if [[ "$existing" == "$$" ]]; then
            rm -f "$LOCK_FILE"
        fi
        LOCK_HELD=0
    fi
}

# shellcheck disable=SC2329
# Invoked via `trap on_signal INT TERM` — shellcheck can't see it.
on_signal() {
    printf 'interrupted — partial state may exist\n' >&2
    release_lock
    exit 130
}

# ---------------------------------------------------------------------------
# upload_to_minio — per-file `mc cp --attr sha256sum=<hash>`. Captures
# exit code, duration, size (stat -f%z macOS / stat -c%s Linux), MB/s.
# Per-file failure increments MINIO_UPLOAD_FAILS but continues.
# ---------------------------------------------------------------------------
file_size_bytes() {
    local f="$1"
    if stat -f%z "$f" 2>/dev/null; then
        return 0
    fi
    stat -c%s "$f"
}

upload_to_minio() {
    log_message "=== MinIO upload ==="
    local alias_name="${MINIO_ALIAS:-myminio}"
    local bucket="${BUCKET_NAME:-images}"
    local i f hash start_ts end_ts dur bytes mb mbps mc_exit
    for i in "${!FILE_NAMES[@]}"; do
        f="${FILE_NAMES[$i]}"
        hash="${FILE_HASHES[$i]}"
        local local_path="$SOURCE_DIR/$f"
        bytes=$(file_size_bytes "$local_path")
        mb=$(awk -v b="$bytes" 'BEGIN { printf "%.1f", b/1024/1024 }')
        start_ts=$(date +%s)
        if [[ -n "$hash" ]]; then
            mc cp --attr "sha256sum=$hash" "$local_path" "$alias_name/$bucket/" --insecure \
                >> "$LOG_FILE" 2>&1
        else
            # SHA256SUMS file itself has no hash to attach.
            mc cp "$local_path" "$alias_name/$bucket/" --insecure \
                >> "$LOG_FILE" 2>&1
        fi
        mc_exit=$?
        end_ts=$(date +%s)
        dur=$(( end_ts - start_ts ))
        # Avoid div-by-zero for sub-second uploads
        if [[ "$dur" -le 0 ]]; then
            mbps="$mb"
        else
            mbps=$(awk -v m="$mb" -v d="$dur" 'BEGIN { printf "%.1f", m/d }')
        fi
        if [[ "$mc_exit" -eq 0 ]]; then
            log_message "  [ok]   minio: $f (${mb} MB, ${dur}s, ${mbps} MB/s)"
            MINIO_UPLOADS=$((MINIO_UPLOADS + 1))
        else
            log_message "  [FAIL] minio: $f — mc exit $mc_exit"
            MINIO_UPLOAD_FAILS=$((MINIO_UPLOAD_FAILS + 1))
        fi
    done
}

# ---------------------------------------------------------------------------
# upload_to_artifactory — per-file curl PUT with X-JFrog-Art-Api and
# X-Checksum-Sha256 headers + mTLS client cert. On success → call
# verify_artifactory() inline. 401/403 or TLS errors abort remaining
# Artifactory uploads (don't hammer with a bad key).
# ---------------------------------------------------------------------------
upload_to_artifactory() {
    log_message "=== Artifactory upload ==="
    local i f hash start_ts end_ts dur bytes mb mbps
    local http_status curl_exit body_tmp
    for i in "${!FILE_NAMES[@]}"; do
        if [[ "$ARTIFACTORY_ABORTED" -eq 1 ]]; then
            break
        fi
        f="${FILE_NAMES[$i]}"
        hash="${FILE_HASHES[$i]}"
        local local_path="$SOURCE_DIR/$f"
        local target_url="${ARTIFACTORY_BASE_URL}/${ARTIFACTORY_PATH}/${f}"
        bytes=$(file_size_bytes "$local_path")
        mb=$(awk -v b="$bytes" 'BEGIN { printf "%.1f", b/1024/1024 }')
        body_tmp=$(mktemp)
        start_ts=$(date +%s)
        local hash_header
        if [[ -n "$hash" ]]; then
            hash_header="-H X-Checksum-Sha256:$hash"
        else
            hash_header=""
        fi
        # shellcheck disable=SC2086
        # hash_header intentionally word-split to omit empty -H pair
        http_status=$(curl -sS -w '%{http_code}' -o "$body_tmp" \
            -H "X-JFrog-Art-Api:$ARTIFACTORY_KEY" \
            $hash_header \
            --cert "$ARTIFACTORY_CERT_PATH" \
            --key "$ARTIFACTORY_KEY_PATH" \
            --max-time 60 \
            -T "$local_path" \
            "$target_url" 2>>"$LOG_FILE")
        curl_exit=$?
        end_ts=$(date +%s)
        dur=$(( end_ts - start_ts ))
        if [[ "$dur" -le 0 ]]; then
            mbps="$mb"
        else
            mbps=$(awk -v m="$mb" -v d="$dur" 'BEGIN { printf "%.1f", m/d }')
        fi
        if [[ -n "$LOG_FILE" ]]; then
            {
                printf 'artifactory response body for %s:\n' "$f"
                cat "$body_tmp" 2>/dev/null
                printf '\n'
            } >> "$LOG_FILE"
        fi
        rm -f "$body_tmp"
        # TLS handshake failures — abort remaining uploads.
        case "$curl_exit" in
            35|58|60)
                log_message "  [FAIL] artifactory: $f — curl exit $curl_exit TLS handshake (cert=$ARTIFACTORY_CERT_PATH key=$ARTIFACTORY_KEY_PATH)"
                ARTIFACTORY_UPLOAD_FAILS=$((ARTIFACTORY_UPLOAD_FAILS + 1))
                ARTIFACTORY_ABORTED=1
                continue
                ;;
        esac
        if [[ "$curl_exit" -ne 0 ]]; then
            log_message "  [FAIL] artifactory: $f — curl exit $curl_exit"
            ARTIFACTORY_UPLOAD_FAILS=$((ARTIFACTORY_UPLOAD_FAILS + 1))
            continue
        fi
        # Auth failures — abort remaining uploads.
        if [[ "$http_status" == "401" || "$http_status" == "403" ]]; then
            log_message "  [FAIL] artifactory: $f — HTTP $http_status auth rejected"
            ARTIFACTORY_UPLOAD_FAILS=$((ARTIFACTORY_UPLOAD_FAILS + 1))
            ARTIFACTORY_ABORTED=1
            continue
        fi
        # 3xx redirects (302 in particular) always mean Cloudflare Access
        # intercepted the request because the mTLS client cert was
        # expired, missing, or untrusted — Artifactory's JFrog backend
        # itself returns 201/200/204 on a successful PUT and never
        # redirects. Treat ANY 3xx as a hard failure and abort to avoid
        # spending time uploading payload that will be silently dropped.
        # Surface a hint pointing at the cert path so the operator can
        # check expiry with `openssl x509 -in <cert> -noout -dates`.
        if [[ "$http_status" -ge 300 && "$http_status" -lt 400 ]]; then
            log_message "  [FAIL] artifactory: $f — HTTP $http_status (Cloudflare Access redirect; check mTLS cert at $ARTIFACTORY_CERT_PATH for expiry)"
            ARTIFACTORY_UPLOAD_FAILS=$((ARTIFACTORY_UPLOAD_FAILS + 1))
            ARTIFACTORY_ABORTED=1
            continue
        fi
        if [[ "$http_status" -lt 200 || "$http_status" -ge 400 ]]; then
            log_message "  [FAIL] artifactory: $f — HTTP $http_status"
            ARTIFACTORY_UPLOAD_FAILS=$((ARTIFACTORY_UPLOAD_FAILS + 1))
            continue
        fi
        log_message "  [ok]   artifactory: $f (${mb} MB, ${dur}s, ${mbps} MB/s, HTTP $http_status)"
        ARTIFACTORY_UPLOADS=$((ARTIFACTORY_UPLOADS + 1))
        # SHA256SUMS file has no local hash → skip the storage-info verify;
        # JFrog still produces its own checksum and the file is present.
        if [[ -n "$hash" ]]; then
            verify_artifactory "$f" "$hash"
        fi
    done
}

# ---------------------------------------------------------------------------
# verify_artifactory — GET storage-info, parse .checksums.sha256 via jq,
# compare to local hash. Mismatch → increment ARTIFACTORY_VERIFY_FAILS.
# ---------------------------------------------------------------------------
verify_artifactory() {
    local filename="$1" local_hash="$2"
    local url="${ARTIFACTORY_BASE_URL}/api/storage/${ARTIFACTORY_PATH}/${filename}"
    local body remote_hash
    body=$(curl -sf \
        -H "X-JFrog-Art-Api:$ARTIFACTORY_KEY" \
        --cert "$ARTIFACTORY_CERT_PATH" \
        --key "$ARTIFACTORY_KEY_PATH" \
        --max-time 30 \
        "$url" 2>>"$LOG_FILE")
    local curl_exit=$?
    if [[ "$curl_exit" -ne 0 ]]; then
        log_message "  [FAIL] artifactory verify: $filename — curl exit $curl_exit"
        ARTIFACTORY_VERIFY_FAILS=$((ARTIFACTORY_VERIFY_FAILS + 1))
        return 0
    fi
    remote_hash=$(printf '%s' "$body" | jq -r '.checksums.sha256 // empty' 2>/dev/null)
    if [[ -z "$remote_hash" ]]; then
        log_message "  [FAIL] artifactory verify: $filename — no checksum in storage-info"
        ARTIFACTORY_VERIFY_FAILS=$((ARTIFACTORY_VERIFY_FAILS + 1))
        return 0
    fi
    if [[ "$remote_hash" != "$local_hash" ]]; then
        log_message "  [FAIL] artifactory verify: $filename — expected $local_hash got $remote_hash"
        ARTIFACTORY_VERIFY_FAILS=$((ARTIFACTORY_VERIFY_FAILS + 1))
        return 0
    fi
    log_message "  [ok]   artifactory verify: $filename"
    ARTIFACTORY_VERIFIES=$((ARTIFACTORY_VERIFIES + 1))
}

# ---------------------------------------------------------------------------
# main — orchestrate Phases 1–5 from the spec
# ---------------------------------------------------------------------------
main() {
    # Phase 1.1 — parse flags
    while getopts ":v:d:k:s:nh" opt; do
        case "$opt" in
            v)
                if [[ -z "$OPTARG" ]]; then
                    fail_with 1 "-v requires a non-empty value. Run with -h for usage."
                fi
                VERSION="$OPTARG"
                ;;
            d)
                case "$OPTARG" in
                    minio|artifactory|both) DEST_REQUESTED="$OPTARG" ;;
                    *) fail_with 1 "-d must be minio|artifactory|both. Run with -h for usage." ;;
                esac
                ;;
            k) ARTIFACTORY_KEY="$OPTARG" ;;
            s)
                if [[ ! -d "$OPTARG" ]]; then
                    fail_with 1 "-s <dir> is not a directory. Run with -h for usage."
                fi
                SOURCE_DIR="$OPTARG"
                ;;
            n) DRY_RUN=1 ;;
            h) usage ;;
            \?) fail_with 1 "unknown flag. Run with -h for usage." ;;
            :) fail_with 1 "-$OPTARG requires a value. Run with -h for usage." ;;
        esac
    done

    # Phase 1.2 — resolve version
    resolve_version
    # Phase 1.3 — resolve source dir + repo root
    resolve_repo_root
    if [[ -z "$SOURCE_DIR" ]]; then
        SOURCE_DIR="$REPO_ROOT/dist"
    fi
    if [[ ! -d "$SOURCE_DIR" ]]; then
        fail_with 1 "source dir does not exist: $SOURCE_DIR"
    fi
    # Make SOURCE_DIR absolute so log lines / lock file path are stable
    SOURCE_DIR=$(cd "$SOURCE_DIR" && pwd)

    # Phase 1.4 + 1.5 — discover + verify presence
    discover_release_set
    # Phase 1.6 — prereqs (may narrow DEST)
    check_prereqs
    # Phase 1.7 — local manifest cross-check (caches FILE_HASHES)
    validate_local_manifest

    # Phase 2 — plan summary
    local dest_label
    case "$DEST" in
        minio)        dest_label="minio (${MINIO_ALIAS:-myminio}/${BUCKET_NAME:-images})" ;;
        artifactory)  dest_label="artifactory (${ARTIFACTORY_PATH})" ;;
        both)         dest_label="minio (${MINIO_ALIAS:-myminio}/${BUCKET_NAME:-images}), artifactory (${ARTIFACTORY_PATH})" ;;
    esac
    # Compute archive count + total MB for the plan block.
    local total_bytes=0 i f sz
    for i in "${!FILE_NAMES[@]}"; do
        f="${FILE_NAMES[$i]}"
        sz=$(file_size_bytes "$SOURCE_DIR/$f")
        total_bytes=$(( total_bytes + sz ))
    done
    local total_mb
    total_mb=$(awk -v b="$total_bytes" 'BEGIN { printf "%.1f", b/1024/1024 }')

    # Log path (the plan shows it even in dry-run; dry-run does not create the file).
    local ts logs_dir
    ts=$(date -u +%Y%m%dT%H%M%SZ)
    logs_dir="$REPO_ROOT/logs"
    LOG_FILE="$logs_dir/publish_${VERSION}_${ts}.log"

    printf '\n'
    printf 'otto-gateway publish plan\n'
    printf -- '-------------------------\n'
    printf 'version:       %s\n' "$VERSION"
    printf 'source:        %s\n' "$SOURCE_DIR"
    printf 'destinations:  %s\n' "$dest_label"
    printf 'archives:      4 + SHA256SUMS (total %s MB)\n' "$total_mb"
    printf 'log:           %s\n' "$LOG_FILE"
    printf '\n'

    if [[ "$DRY_RUN" -eq 1 ]]; then
        # Spec Phase 2: dry-run prints the plan and exits 0. No lock, no log file.
        LOG_FILE=""
        exit 0
    fi

    # Real run — create log dir + log file. Logging failure is non-fatal
    # (log_message will warn and reset LOG_FILE).
    mkdir -p "$logs_dir" 2>/dev/null || true
    if ! : > "$LOG_FILE" 2>/dev/null; then
        printf '[warn] cannot create log file %s; continuing without log\n' "$LOG_FILE" >&2
        LOG_FILE=""
    fi

    # Install signal handlers BEFORE acquiring the lock so an INT
    # between acquire and the loop still releases.
    trap on_signal INT TERM
    trap release_lock EXIT
    acquire_lock

    local run_start_ts run_end_ts run_dur
    run_start_ts=$(date +%s)

    # Phase 3 — MinIO
    if [[ "$DEST" == "minio" || "$DEST" == "both" ]]; then
        upload_to_minio
    fi
    # Phase 4 — Artifactory
    if [[ "$DEST" == "artifactory" || "$DEST" == "both" ]]; then
        upload_to_artifactory
    fi

    run_end_ts=$(date +%s)
    run_dur=$(( run_end_ts - run_start_ts ))

    # Phase 5 — final report
    printf '\n'
    printf 'upload summary\n'
    printf -- '--------------\n'
    if [[ "$DEST" == "minio" || "$DEST" == "both" ]]; then
        printf 'minio:        %d/%d uploaded\n' "$MINIO_UPLOADS" "${#FILE_NAMES[@]}"
    fi
    if [[ "$DEST" == "artifactory" || "$DEST" == "both" ]]; then
        # Verify denominator: archives only (4), not 5 — SHA256SUMS has no checksum.
        printf 'artifactory:  %d/%d uploaded, %d/4 verified\n' \
            "$ARTIFACTORY_UPLOADS" "${#FILE_NAMES[@]}" "$ARTIFACTORY_VERIFIES"
    fi
    printf 'duration:     %ss\n' "$run_dur"
    printf 'log:          %s\n' "${LOG_FILE:-<unwritable>}"

    # Compute exit code: upload failure > verify failure > 0
    if [[ "$MINIO_UPLOAD_FAILS" -gt 0 || "$ARTIFACTORY_UPLOAD_FAILS" -gt 0 ]]; then
        exit 3
    fi
    if [[ "$ARTIFACTORY_VERIFY_FAILS" -gt 0 ]]; then
        exit 4
    fi
    exit 0
}

main "$@"
