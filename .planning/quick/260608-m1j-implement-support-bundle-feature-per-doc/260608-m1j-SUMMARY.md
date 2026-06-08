---
phase: quick-260608-m1j
plan: 01
subsystem: wrappers + tray
tags: [support-bundle, redaction, tray, lifecycle-wrapper]
dependency_graph:
  requires: []
  provides:
    - scripts/lib/redact.sh
    - scripts/lib/redact.ps1
    - scripts/otto-gw support
    - scripts/otto-gw.ps1 support
    - cmd/otto-tray/tray.go handleSupportBundle
    - cmd/otto-tray/uihelpers_{darwin,windows}.go revealBundle
  affects:
    - operator triage workflow
    - tray menu
    - scripts/otto-gw usage()
    - scripts/otto-gw.ps1 Show-Usage
tech-stack:
  added: []
  patterns:
    - "Wrapper-shared redaction lib (scripts/lib/*) sourced/dot-sourced by support subcommand on each OS"
    - "Tray shells out via runWrapper(..., \"support\") — reuses existing subprocess plumbing"
    - "Build-tag-resolved revealBundle (open -R / explorer.exe /select,) mirrors existing openURL pattern"
key-files:
  created:
    - scripts/lib/redact.sh
    - scripts/lib/redact.ps1
    - tests/scripts/test-support-redact.sh
    - tests/scripts/test-support-redact.ps1
    - tests/scripts/test-support-bundle.sh
    - tests/scripts/test-support-bundle.ps1
  modified:
    - scripts/otto-gw
    - scripts/otto-gw.ps1
    - cmd/otto-tray/tray.go
    - cmd/otto-tray/uihelpers_darwin.go
    - cmd/otto-tray/uihelpers_windows.go
decisions:
  - "Loosened the `^(AUTH_TOKEN|PII_HASH_KEY|PII_ENCRYPT_KEY)=` log scrub anchor to `(^|[^A-Za-z0-9_])` so it fires mid-line on real slog entries like `... msg=AUTH_TOKEN=...`. The spec text uses `^` but the integration test (and the spec's intent — \"no raw secrets, ever\") requires the broader match. Bash + pwsh both updated to stay byte-equivalent."
  - "Captured status() output via `$( ( status 2>&1 ) || true )` rather than `( status 2>&1 || true ) > file` to work around bash's set-e-in-subshell-in-function gotcha — the latter form propagates the subshell's `exit 1` through the enclosing function even with `|| true`."
  - "Reveal helper named `revealBundle` (uniform across build tags) to match the existing `openURL` / `copyToClipboard` / `notify` / `confirmDialog` pattern. Avoids a runtime.GOOS switch in tray.go."
  - "Used `tr` instead of `${k^^}` in is_secret_key so the lib stays neutral to bash major version (macOS /bin/bash is still 3.2; scripts/otto-gw uses `#!/usr/bin/env bash` so it picks up Homebrew bash 5+, but this lib should work either way)."
metrics:
  duration: ~50 minutes
  completed: 2026-06-08
---

# Quick Task 260608-m1j: Support Bundle Summary

A wrapper-side `support` subcommand on both `otto-gw` / `otto-gw.ps1` that
produces a redacted diagnostic archive, paired with a tray menu item that
shells out to that subcommand.

## What was built

- **`scripts/lib/redact.sh` + `scripts/lib/redact.ps1`** — shared redaction
  primitives (`redact_stream` / `Invoke-RedactStream`, `mask_env_value` /
  `Mask-EnvValue`, `is_secret_key` / `Test-IsSecretKey`). Regex rules are
  byte-equivalent across OS wrappers so they cannot drift.
- **`scripts/otto-gw support [--out DIR] [--max-mb N] [--log-days D]`** —
  builds the bundle layout from the design spec exactly (six trees +
  MANIFEST.txt), prints the absolute archive path on stdout, and creates
  a `latest.tar.gz` alias (copy, not symlink — Windows parity).
- **`scripts/otto-gw.ps1 support -Out DIR -MaxMb N -LogDays D`** — pwsh
  mirror producing a `.zip` (via `Compress-Archive`) with the same layout.
- **`cmd/otto-tray/tray.go` `handleSupportBundle`** — confirmation dialog,
  `runWrapper(installRoot, "support")`, notification with bundle path,
  reveal-in-Finder/Explorer; failure path uses `infoDialog` with the tail
  of stderr.
- **`cmd/otto-tray/uihelpers_{darwin,windows}.go` `revealBundle`** —
  `open -R <path>` on darwin, `explorer.exe /select,<path>` on windows.
  Build-tag-resolved like the existing `openURL`.
- **Tests** — `tests/scripts/test-support-redact.sh` (38 assertions on
  bash redaction) + `tests/scripts/test-support-bundle.sh` (16-assertion
  integration smoke that builds a fake install root, runs the wrapper,
  and asserts NO synthetic secret literal leaks through). PowerShell
  mirrors (`.ps1`) exist for Windows CI.

## Bundle Layout (verified end-to-end)

```
otto-support-<host>-<ts>/
├── MANIFEST.txt
├── env/{effective.env, env-files.txt, shell-env.txt}
├── health/{status.txt, health.json, snapshot.json}
├── logs/{otto-gateway.log, otto-gateway-boot.log, otto-gateway-chat-trace.log, otto-gateway-*.log.gz}
├── system/{system.txt, versions.txt, installroot.txt}
└── tray/{tray-state.txt, pidfile.txt, autostart.txt}
```

## Commits

| Hash      | Type | Description |
| --------- | ---- | ----------- |
| `7d1b4ac` | feat | `feat(260608-m1j): add support-bundle subcommand to wrapper scripts` — redact.sh + redact.ps1, support()/Invoke-Support, both usage() blocks updated, 4 test scripts. |
| `85c7734` | feat | `feat(260608-m1j): wire support-bundle item into tray menu` — `Create Support Bundle…` menu item, handleSupportBundle, tailLines helper, build-tagged revealBundle + bundleExt. |

## Verification

| Check | Result |
| ----- | ------ |
| `bash tests/scripts/test-support-redact.sh` | 38 / 38 pass |
| `bash tests/scripts/test-support-bundle.sh` | 16 / 16 pass |
| `shellcheck` on otto-gw, redact.sh, both test scripts | clean except SC1091 (informational, dynamic source path) |
| `bash -n scripts/otto-gw` | clean |
| `go build ./...` (darwin) | clean |
| `GOOS=windows GOARCH=amd64 go build ./...` | clean |
| `go vet ./cmd/otto-tray/...` | clean |
| `scripts/otto-gw help \| grep support` | documents the new subcommand in both stdout and stderr usage blocks |
| Manual smoke against real env | bundle produced with all six trees + MANIFEST.txt; `grep -rF mySecretSauce42 <extract>` returned empty; `grep -rF secrettoken99 <extract>` returned empty; log line rewritten to `AUTH_TOKEN=[REDACTED]` and `Bearer [REDACTED]`. |

### Manual smoke command

```bash
SMOKE_ROOT=$(mktemp -d); SMOKE_OUT=$(mktemp -d)
mkdir -p "$SMOKE_ROOT/logs"
cat > "$SMOKE_ROOT/logs/otto-gateway.log" <<'EOF'
2026-06-08T20:00:00Z INFO msg=gateway.boot AUTH_TOKEN=mySecretSauce42
2026-06-08T20:00:01Z INFO method=POST header.authorization="Bearer secrettoken99"
EOF
OTTO_INSTALL_ROOT="$SMOKE_ROOT" OTTO_BIN=/bin/true \
  OTTO_LOG="$SMOKE_ROOT/logs/otto-gateway.log" \
  OTTO_ADDR=http://127.0.0.1:1 AUTH_TOKEN=mySecretSauce42 \
  bash scripts/otto-gw support --out "$SMOKE_OUT"
# prints: $SMOKE_OUT/otto-support-<host>-<ts>.tar.gz
```

Extraction + grep against the bundle confirms no leak.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Log-scrub regex anchor (`^`) was too strict for real log entries**

- **Found during:** Task 1 integration test execution.
- **Issue:** The spec's `^(AUTH_TOKEN|PII_HASH_KEY|PII_ENCRYPT_KEY)=` regex
  only fires when the env-style assignment is at the very start of the line.
  Real `slog` log entries embed the pattern mid-line (e.g.
  `... msg=AUTH_TOKEN=value ...`), so the integration test's `grep -rF
  realsupersecretXYZ` gate caught a leak that the spec's anchor would have
  let through.
- **Fix:** Replaced the `^` anchor with `(^|[^A-Za-z0-9_])` in both
  `redact.sh` and `redact.ps1` so the rule fires anywhere the secret-key
  appears in a non-identifier-suffix position. Falsely-suffixed keys like
  `MY_AUTH_TOKEN=foo` are NOT redacted (the leading negative-char-class
  guards against that). The substitution preserves the captured prefix.
- **Files modified:** scripts/lib/redact.sh, scripts/lib/redact.ps1
- **Commit:** 7d1b4ac (included as part of the initial feature commit)

**2. [Rule 3 - Blocker] bash `set -e` propagates through nested subshell + `|| true` inside a function**

- **Found during:** Task 1 initial integration-test run (subprocess exit 1, no output).
- **Issue:** With `set -euo pipefail` active, the form
  `( status 2>&1 || true ) > file` inside `support()` still propagates
  status()'s `exit 1` through the enclosing function — the `|| true`
  fires inside the subshell but the subshell's exit status still trips
  set-e at the calling site (a documented bash gotcha for
  `set -e`-in-subshell-in-function).
- **Fix:** Switched to `local out; out=$( ( status 2>&1 ) || true );
  printf '%s\n' "$out" > file`. Command-substitution always treats the
  subshell exit as a captured value, never as a fail-fast trigger.
- **Files modified:** scripts/otto-gw
- **Commit:** 7d1b4ac

**3. [Rule 3 - Blocker] `ls -t … | tail -r` non-portable + shellcheck SC2012**

- **Found during:** Task 1 shellcheck pass.
- **Issue:** Initial size-cap implementation used `ls -t` to find the
  oldest rotated logs. `tail -r` (BSD reverse) isn't on Linux, and
  shellcheck SC2012 warns against parsing `ls`.
- **Fix:** Built an mtime-prefixed list with `stat -f %m` (BSD) /
  `stat -c %Y` (GNU), tab-paired with the path, sorted numerically,
  then iterated. Portable + shellcheck-clean.
- **Files modified:** scripts/otto-gw
- **Commit:** 7d1b4ac

### Intentional deviations from the plan text

- **Plan said "bash 4+ already required by scripts/otto-gw" for `${k^^}`** —
  in practice the wrapper uses `#!/usr/bin/env bash` and doesn't use `^^`
  anywhere else, so I used `tr '[:lower:]' '[:upper:]'` in `is_secret_key`
  to stay bash-3.2-compatible (macOS `/bin/bash`). Cost: one extra
  subshell per is_secret_key call; benefit: lib is truly bash-version-neutral.
- **Plan listed `otto-gateway.boot-out.log` / `otto-gateway.boot-err.log`
  in the pwsh log set; the bash equivalent only has `otto-gateway-boot.log`** —
  pwsh wrapper writes both stdout and stderr sidecar files (`$LogBootOut` /
  `$LogBootErr`), so Invoke-Support collects both for parity. POSIX side
  has only one sidecar (`$OTTO_LOG_BOOT`).

## Anything Deferred

- **Re-redaction of rotated `*.log.gz` files** — copied verbatim. The gateway
  scrubs at write time (per design §Redaction), and re-redacting would require
  a gunzip/gzip round-trip in the wrapper — not portable to the bash + pwsh
  surface without adding gzip-tooling assumptions. Noted in MANIFEST.txt of
  every produced bundle.
- **PowerShell test execution on the dev box** — `pwsh` is not installed on
  this macOS dev machine. The `.ps1` test files exist and structurally
  mirror the bash tests; they're intended to run on Windows CI.

## Out-of-scope items confirmed NOT implemented

- No `--include-secrets` flag.
- No auto-upload (S3, HTTPS POST).
- No mail-client integration.
- No Go-binary `otto-support`.
- No encrypted archives.
- No automated tray UI tests.

## Self-Check: PASSED

Verified the following exist:

- `scripts/lib/redact.sh` — FOUND
- `scripts/lib/redact.ps1` — FOUND
- `scripts/otto-gw` — FOUND (modified: support() + dispatch + usage)
- `scripts/otto-gw.ps1` — FOUND (modified: Invoke-Support + switch + Show-Usage)
- `cmd/otto-tray/tray.go` — FOUND (modified: miSupport + handleSupportBundle + tailLines)
- `cmd/otto-tray/uihelpers_darwin.go` — FOUND (modified: revealBundle + bundleExt)
- `cmd/otto-tray/uihelpers_windows.go` — FOUND (modified: revealBundle + bundleExt)
- `tests/scripts/test-support-redact.sh` — FOUND
- `tests/scripts/test-support-redact.ps1` — FOUND
- `tests/scripts/test-support-bundle.sh` — FOUND
- `tests/scripts/test-support-bundle.ps1` — FOUND
- Commit `7d1b4ac` — FOUND in git log
- Commit `85c7734` — FOUND in git log
