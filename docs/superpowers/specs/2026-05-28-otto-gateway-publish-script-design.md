# Design — otto-gateway publish script (`scripts/publish.sh`)

**Date:** 2026-05-28
**Status:** Approved (pending user spec review)
**Author:** brainstorming session (Corey Ellis + Claude Opus 4.7)
**Reference:** `oscar_app/oscar/utils/bulk_upload_verify.sh` (oscar's interactive bulk uploader; this design is a lean rewrite tailored to otto-gateway)

---

## Context

Phase 08.1 closed v1.5 and `make package-all` produces 5 distribution artifacts in `dist/`:

```
otto_gateway-darwin-arm64-<version>.tar.gz
otto_gateway-darwin-amd64-<version>.tar.gz
otto_gateway-linux-amd64-<version>.tar.gz
otto_gateway-windows-amd64-<version>.zip
SHA256SUMS-<version>.txt
```

The artifacts are correct and operator-friendly (`docs/operator-quickstart.md` documents the 6-step install) but there's no scripted way to push them from a developer laptop to the team's internal distribution channels. Operators today would have to upload each file by hand via Artifactory's web UI or `mc cp` from the shell — error-prone and unrepeatable.

`oscar_app/oscar/utils/bulk_upload_verify.sh` already solves this problem for the oscar project against the same Artifactory + MinIO targets. It provides an interactive menu, batch mode, per-file SHA256 metadata, and a curl-based Artifactory verify step. The pattern is well-tested and battle-proven.

This spec describes a leaner, otto-gateway-specific port that publishes the 5-file release set as an atomic unit, without the interactive menu (which oscar needs because it ships N Docker images; otto-gateway always ships exactly 5 files).

## Goals

1. One command on a developer laptop publishes a complete otto-gateway release set to MinIO and/or Artifactory at the team's existing distribution paths.
2. Failures are loud, specific, and actionable — operator knows exactly what to fix before re-running.
3. Operators familiar with oscar's script find this immediately recognisable; vocabulary, flags, env vars, and cert paths follow the same conventions.
4. The script is a publish-only tool — it never modifies `dist/`, never spawns the gateway binary, never reaches into the host beyond the destination clients.

## Non-Goals

- **No install-side automation.** Operators continue to download + extract + run per `docs/operator-quickstart.md`. (Confirmed scope decision.)
- **No interactive menu, no per-file selection.** Partial releases are bugs, not features.
- **No GitHub release publishing, no git tagging, no version bumping.** Those are upstream of this script.
- **No retry / backoff.** Failures abort the affected file's pipeline; operator re-runs after fixing root cause.
- **No CI auto-release.** Real uploads are a manual operator action. CI gets a dry-run job only.

## Out-of-Scope: The cert provisioning

This script consumes mTLS client certs at `../secrets/certs/{client.pem,client-key.pem}` (resolved from the otto-gateway repo root). The certs are owned outside the repo at `otto_app/secrets/certs/` (one level up). The user has already populated those files by copying from `oscar_app/oscar/secrets/certs/`. The script does not provision certs and does not document how to obtain them — that's a team-level operational concern handled out-of-band.

## Architecture

**One file:** `scripts/publish.sh`, target size ≤300 lines (oscar's `bulk_upload_verify.sh` is 688 lines mostly because of the interactive menu and per-file selection logic).

**Language:** bash 3.2+ for macOS compatibility (Homebrew bash is 5.x, but stock macOS bash is 3.2 and we should not require operators to install a non-default shell). `set -euo pipefail` at the top.

**Helper structure** (same shape as oscar's, leaner internals):

| Function | Purpose | Lines (approx) |
|----------|---------|----------------|
| `usage()` | Print `-h` text and exit 0 | 30 |
| `log_message()` | Tee to stdout + log file | 5 |
| `resolve_version()` | `-v` flag → `git describe` → error | 15 |
| `discover_release_set()` | Build expected file list from version | 10 |
| `validate_local_manifest()` | Cross-check SHA256SUMS vs computed | 20 |
| `check_prereqs()` | mc, jq, certs, API key — report all missing at once | 40 |
| `upload_to_minio()` | Per-file `mc cp --attr` | 25 |
| `upload_to_artifactory()` | Per-file curl PUT with `X-Checksum-Sha256` | 30 |
| `verify_artifactory()` | curl storage-info + jq checksum compare | 25 |
| `acquire_lock()` / `release_lock()` | `dist/.publish.lock` PID-based, signal-safe | 25 |
| `main()` | Orchestrate phases 1–5 | 50 |

## CLI Surface

**Invocation:** `scripts/publish.sh [flags]` from repo root.

**Flags:**

| Flag | Meaning | Default |
|------|---------|---------|
| `-v <version>` | Version tag to publish | `git describe --tags --always --dirty` |
| `-d <dest>` | `minio` \| `artifactory` \| `both` | when `-d` is **omitted**: auto-select `both` if both prereqs satisfied, otherwise auto-narrow to whichever is available, otherwise error. When `-d` is **explicit**: any missing prereq for the chosen destination is a hard error (no auto-narrow) |
| `-k <api-key>` | Artifactory API key | `$ARTIFACTORY_API_KEY` env or shell-profile fallback |
| `-s <dir>` | Source dir holding the archives + SHA256SUMS | `dist` |
| `-n` | Dry-run | off |
| `-h` | Help | — |

**Environment variables (all optional except where noted):**

| Var | Purpose | Default |
|-----|---------|---------|
| `ARTIFACTORY_API_KEY` | JFrog API key for `X-JFrog-Art-Api` header | none (REQUIRED if Artifactory is a destination) |
| `ARTIFACTORY_CERT_PATH` | Override client cert | `../secrets/certs/client.pem` |
| `ARTIFACTORY_KEY_PATH` | Override client key | `../secrets/certs/client-key.pem` |
| `MINIO_ALIAS` | mc alias name | `myminio` |
| `BUCKET_NAME` | MinIO bucket | `images` |

**Hardcoded constants (mirror oscar's values):**

```bash
ARTIFACTORY_BASE_URL="https://artifactory.rosetta.ericssondevops.com/artifactory/sd-mana-tmo-prepaid-cdp-pristine-generic"
ARTIFACTORY_PATH="Infra/images"
```

**Exit codes:**

| Code | Meaning |
|------|---------|
| 0 | All requested uploads + verifications passed |
| 1 | Bad flags / missing prereqs / manifest disagreement |
| 2 | Release set incomplete in source dir |
| 3 | Upload failed (any destination, any file) |
| 4 | Upload succeeded but verification failed |
| 130 | Interrupted (SIGINT/SIGTERM) |

**Usage examples shown by `-h`:**

```bash
./scripts/publish.sh                          # current build, both destinations
./scripts/publish.sh -v v1.5.0                # tagged release
./scripts/publish.sh -d minio                 # MinIO only
./scripts/publish.sh -d artifactory -k <key>  # Artifactory only, explicit key
./scripts/publish.sh -n                       # dry-run (no uploads)
```

## Behaviour Flow

### Phase 1 — Resolve & validate (no network)

1. Parse flags. `-h` → usage + exit 0.
2. Resolve `VERSION`. If `-v` absent, run `git describe --tags --always --dirty 2>/dev/null`. If git fails AND no `-v`, error: `version required (-v) or run from a git checkout` → exit 1.
3. Resolve `SOURCE_DIR` to absolute path. Must be a directory → else exit 1.
4. Build expected file list (5 entries; pattern in Goals section above).
5. Per-file existence check in `SOURCE_DIR`. Missing files → print full list with `[ok]`/`[MISSING]` per row → exit 2 with hint: `run 'make package-all' to rebuild`.
6. Resolve destinations:
   - `-d minio` or `both`: require `mc` on PATH.
   - `-d artifactory` or `both`: require `ARTIFACTORY_API_KEY` (CLI > env > shell profile fallback) AND both cert files readable AND `jq` on PATH.
   - Each missing piece → collect into one error report → exit 1 with all gaps named at once.
   - Default-`-d` (omitted): when at least one destination has its prereqs satisfied, silently narrow and proceed (a single warning line names what was skipped). Error only if neither is available.
   - Explicit-`-d`: any missing prereq for the chosen destination is a hard error. If the operator typed `-d both`, they wanted both; we don't silently downgrade.
7. **Validate SHA256SUMS locally.** Compute SHA256 of each archive in source, cross-check against the matching row in `SHA256SUMS-${VERSION}.txt`. Mismatch → print offending rows (`<filename>: expected <sha>, got <sha>`) → exit 1 with hint: `re-run 'make package-all'`.

### Phase 2 — Plan summary

Print a single block to stdout:

```
otto-gateway publish plan
-------------------------
version:       a5b7bbb
source:        /Users/coreyellis/.../dist
destinations:  minio (myminio/images), artifactory (Infra/images)
archives:      4 + SHA256SUMS (total 41.2 MB)
log:           logs/publish_a5b7bbb_20260528T154700Z.log
```

If `-n` (dry-run): the plan block is printed and the script exits 0. Dry-run skips lock acquisition AND skips creating the log file — the plan block's `log:` line shows the path that WOULD have been used (so operators can confirm the timestamping convention without producing an artifact). Otherwise, acquire `dist/.publish.lock` and continue.

### Phase 3 — Upload to MinIO (if selected)

For each of the 5 files:
1. Use cached SHA256 from phase 1.7.
2. `mc cp --attr sha256sum=<hash> "$file" "$MINIO_ALIAS/$BUCKET_NAME/" --insecure`
3. Log: filename, size MB, duration, MB/s, mc exit code.
4. Continue to next file on individual failure; track failure count for final exit code.

### Phase 4 — Upload to Artifactory (if selected)

For each of the 5 files:
1. `curl -H "X-JFrog-Art-Api:$KEY" -H "X-Checksum-Sha256:$SHA256" --cert <path> --key <path> --max-time 60 -T <file> "<BASE_URL>/<PATH>/<filename>"`. Capture exit + HTTP status.
2. Immediately after each successful PUT: `verify_artifactory()` — `GET <BASE_URL>/api/storage/<path>/<filename>`, parse `.checksums.sha256` via `jq`, compare to local hash.
3. Auth failure (401/403) → log error → abort remaining Artifactory uploads (don't hammer with bad key).
4. TLS handshake failure (curl exit 35/58/60) → log error → abort remaining Artifactory uploads.
5. Any other failure → continue to next file.

### Phase 5 — Final report

```
upload summary
--------------
minio:        5/5 uploaded
artifactory:  5/5 uploaded, 5/5 verified
duration:     8.2s
log:          logs/publish_a5b7bbb_20260528T154700Z.log
```

Exit code reflects the worst observed failure: 3 (upload) > 4 (verify-only). Log file is always preserved.

## Error Handling

| Class | Trigger | Message format | Exit |
|-------|---------|----------------|------|
| Bad invocation | Unknown flag, bad `-d` value, `-s` not a dir, `-v` empty | `Error: <one line>. Run with -h for usage.` | 1 |
| Missing prereq | `mc`/`jq`/cert/`ARTIFACTORY_API_KEY` absent for selected destination | All missing pieces in one report (don't make operator re-run 4 times) | 1 |
| Incomplete release | Phase 1.5 — any file missing from `SOURCE_DIR` | Per-row `[ok]`/`[MISSING]` + hint to rebuild | 2 |
| Manifest disagreement | Phase 1.7 — computed SHA ≠ SHA256SUMS row | Show offending row(s) + rebuild hint | 1 |
| Upload failure (per-file) | `mc cp` non-zero OR `curl` HTTP ≥ 400 | `[FAIL] <dest>: <filename> — <exit/HTTP> <stderr>` | 3 if any |
| Verification failure | Artifactory storage-info SHA ≠ local | `[FAIL] artifactory verify: <filename>` | 4 if any AND no upload failures |
| Auth rejected | curl 401/403 | Abort remaining Artifactory uploads | 3 |
| TLS error | curl 35/58/60 | Abort remaining Artifactory uploads with cert/key paths in message | 3 |
| Interrupted | SIGINT/SIGTERM | Flush log, print `interrupted — partial state may exist`, release lock | 130 |

**Logging discipline:**
- **stdout** — plan block, phase headers, per-file `[ok]`/`[FAIL]`, final report. One-screen-tall on success, no spinners.
- **log file** — stdout content PLUS full curl `-w` timing, full mc/curl stderr, hashes, dest URLs, storage-info JSON responses.
- **Logging failure** (read-only `logs/` etc) — warn to stderr, continue. Don't abort an upload because the log is unwritable.

**Concurrency:** Single instance per source dir. `dist/.publish.lock` contains the PID of the running invocation.
- If the lock file is absent → create it, proceed.
- If the lock file is present AND the recorded PID is **alive** (kill -0 succeeds) → exit 1 with `Error: another publish appears to be running (PID <pid>).`
- If the lock file is present AND the PID is **dead** → print warning `[warn] stale publish lock at dist/.publish.lock (PID <pid> not running) — reclaiming`, overwrite with current PID, proceed.

Trap `SIGINT`/`SIGTERM`/`EXIT` to release the lock on any termination path.

**Idempotency:** Re-running for the same version overwrites at the destination. Artifactory PUT with same checksum is idempotent; `mc cp` overwrites. Operators can re-publish without housekeeping.

## Testing

### Layer 1 — Unit/dry-run (zero network)

Suggested location: `tests/scripts/publish_test.sh` (plain bash harness, no `bats` dependency — keeps the brief §3.12 trust-gate count flat).

Cases:
- `-h` → usage, exit 0.
- `-v doesnotexist` → exit 2 with 5 `[MISSING]` rows.
- Corrupted local archive (one byte flipped on a copy) → exit 1 manifest disagreement.
- `-n` against real `dist/` → plan block, exit 0, no log file.
- `-d minio` with `mc` PATH-hidden → exit 1 with explicit `mc` missing message.
- `-d artifactory` with empty `ARTIFACTORY_API_KEY` → exit 1 with explicit API-key missing message.
- `-d artifactory` with cert files absent → exit 1 with explicit cert-path missing message.
- Lock file present with live PID → exit 1 with PID in message.
- Lock file present with dead PID → script proceeds and reclaims lock (or refuses with clearer hint).

### Layer 2 — Live happy path against throwaway target

- `mc alias` pointing at MinIO `play.min.io` (public sandbox) into a unique bucket per run (`otto-gateway-pubtest-<utc>`).
- Run `./scripts/publish.sh -d minio`. Verify all 5 objects landed with the `sha256sum` user-attr matching local hashes.
- Delete the bucket.
- **Skip Artifactory in this layer** — no public sandbox; the verify logic is a direct port of oscar's. Operators do the first real Artifactory push manually with `-n` then a real run, watching the log.

### Layer 3 — CI integration

- Add a `publish-dry-run` job to `.github/workflows/ci.yml` that runs `make package-all && ./scripts/publish.sh -n` on PRs. Catches regressions in `make package-all` output naming/format.
- **Do not** add a real-upload job. Releases are a separate manual workflow.

### Out-of-scope tests

- Artifactory verify against real server (no public sandbox; direct port of oscar's logic).
- Concurrent invocation beyond a single inspection of `dist/.publish.lock`.
- Performance (5 files, ~10MB each, no realistic throughput concerns).

## Open Questions

None at design approval time. All clarifications were resolved during brainstorming:

- Channel: Artifactory + MinIO, same paths as oscar.
- Install side: out of scope (manual per README).
- Approach: lean rewrite using oscar as reference.
- Cert location: `../secrets/certs/` resolved from otto-gateway repo root (i.e., `otto_app/secrets/certs/`), populated outside the repo.
- `mc` prereq: installed via `brew install minio-mc`.

## Implementation Order (preview for writing-plans)

The implementation plan will likely sequence:
1. Create `scripts/publish.sh` skeleton with `usage()`, flag parsing, `set -euo pipefail`, and the help text.
2. Phase 1 helpers (resolve_version, discover_release_set, validate_local_manifest, check_prereqs).
3. Lock file acquire/release with signal traps.
4. `upload_to_minio()` + `upload_to_artifactory()` + `verify_artifactory()`.
5. `main()` orchestration + final report.
6. `tests/scripts/publish_test.sh` Layer-1 cases.
7. `.github/workflows/ci.yml` `publish-dry-run` job.
8. Update `docs/operator-quickstart.md` with a `## Publishing a build` operator section (one-liner reference, links here).

Each step is an atomic commit. The whole feature lands in ~8 commits, one PR.

---

_Brainstorming session: 2026-05-28._
_Next step: writing-plans skill to produce the implementation plan._
