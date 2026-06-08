# Support Bundle — Design

**Date:** 2026-06-08
**Status:** Approved (awaiting implementation plan)

## Problem

OTTO Gateway runs on user laptops. When users hit issues, screen-share isn't
always feasible. They need a one-click (tray) or one-command (terminal) way to
produce a small, redacted archive containing the artifacts triage needs: logs,
effective configuration, live health snapshot, and system/installer state.
Output must be safe to email (no raw secrets) and discoverable on disk.

## Value

A user can produce a complete, redaction-safe diagnostic archive in seconds
from either the system-tray menu or the existing `otto-gw` CLI, and email it
without exposing tokens or keys. Triage gets the same set of artifacts every
time, in the same layout, regardless of which OS or invocation path produced
it.

## Constraints

- **No raw secrets, ever.** No `--include-secrets` flag, no override. Known
  secret env keys are masked; bearer tokens are scrubbed from logs.
- **Two implementations, one logic.** Bash (`scripts/otto-gw`) and PowerShell
  (`scripts/otto-gw.ps1`) parallel each other, mirroring the existing
  start/stop/status pattern. Redaction regex lives in shared helpers
  (`scripts/lib/redact.sh`, `scripts/lib/redact.ps1`) so the rules don't
  drift.
- **No new binaries.** The tray shells out to the script; it does not
  re-implement collection in Go.
- **No new dependencies.** `curl`, `tar`, `gzip` on POSIX; `Compress-Archive`,
  `Invoke-WebRequest` on Windows. No `jq`, no `python`.
- **Bundle size cap.** Total archive ≤ ~50 MB. Over-cap entries are dropped
  (oldest logs first) with a note in `MANIFEST.txt`.

## Architecture

A new `support` subcommand on the two existing lifecycle scripts, plus a
single new menu item on the tray that invokes that subcommand.

```
+--------------------+         +---------------------+
| otto-tray menu     | ----->  | otto-gw support     |  (bash / pwsh)
| "Create Support…"  |         |  + redact helpers   |
+--------------------+         +----------+----------+
                                          |
                                          v
                          collect → redact → archive → notify
                                          |
                                          v
                     $OTTO_INSTALL_ROOT/support/otto-support-<host>-<ts>.{tar.gz|zip}
                     $OTTO_INSTALL_ROOT/support/latest.{tar.gz|zip}
```

The subcommand reuses existing primitives from `scripts/otto-gw`:

- `load_config` — resolves `.env` + overrides + paths
- `print_env` — already masks `AUTH_TOKEN`, `PII_HASH_KEY`, `PII_ENCRYPT_KEY`;
  the support subcommand extends the mask list (see Redaction below)
- `status` — already probes `/health` and `/admin/api/snapshot`

## Bundle Layout

```
otto-support-<host>-<ts>/
├── MANIFEST.txt          # version, timestamp, host, OS, contents, redaction notice,
│                         # any dropped-for-size entries
├── env/
│   ├── effective.env     # merged + redacted env (otto-gw env equivalent)
│   ├── env-files.txt     # which .env / overrides files were loaded (paths only)
│   └── shell-env.txt     # OTTO_*, KIRO_*, PII_*, AUTH_*, ALLOWED_*, DEBUG,
│                         # HTTP_ADDR — redacted
├── health/
│   ├── status.txt        # `otto-gw status` output
│   ├── health.json       # GET /health body (or "unreachable: <reason>")
│   └── snapshot.json     # GET /admin/api/snapshot body (or "unreachable: <reason>")
├── logs/                 # copied, then run through redaction filter
│   ├── otto-gateway.log
│   ├── otto-gateway-boot.log
│   ├── otto-gateway-chat-trace.log
│   └── otto-gateway-*.log.gz   # rotated logs modified in last 7 days
├── system/
│   ├── system.txt        # OS+version, arch, hostname, timezone, locale,
│   │                     # free disk on install root volume
│   ├── versions.txt      # otto-gateway --version, kiro-cli --version + path,
│   │                     # otto-tray version
│   └── installroot.txt   # 2-level listing of $OTTO_INSTALL_ROOT
└── tray/
    ├── tray-state.txt    # FSM state if tray is running (best-effort)
    ├── pidfile.txt       # PID + pidfile mtime
    └── autostart.txt     # macOS: launchd plist presence + path;
                          # Windows: registry Run-key entry presence
```

## Redaction

**Env keys masked** (value shown as `<first 4 chars>…(<N> chars)`):

- Any key matching `*TOKEN*`, `*KEY*`, `*SECRET*`, `*PASSWORD*`, `*PASSPHRASE*`
  (case-insensitive)
- Explicit list (defense in depth): `AUTH_TOKEN`, `PII_HASH_KEY`,
  `PII_ENCRYPT_KEY`

**Log scrubs** (regex replace with `[REDACTED]`):

- `Bearer [A-Za-z0-9._\-]+` → `Bearer [REDACTED]`
- Lines matching `^(AUTH_TOKEN|PII_HASH_KEY|PII_ENCRYPT_KEY)=` →
  `<KEY>=[REDACTED]`
- HTTP header lines `Authorization:\s*\S+` → `Authorization: [REDACTED]`
- `x-api-key:\s*\S+` → `x-api-key: [REDACTED]` (case-insensitive)

Redaction is applied before files are added to the archive. The original
files on disk are not modified. `MANIFEST.txt` declares which redactions ran.

## Output Location and Notification

- Mac: `$OTTO_INSTALL_ROOT/support/otto-support-<hostname>-<YYYYMMDD-HHMMSS>.tar.gz`
- Windows: `%OTTO_INSTALL_ROOT%\support\otto-support-<hostname>-<YYYYMMDD-HHMMSS>.zip`
- A convenience alias `support/latest.<ext>` always points to the most
  recently created bundle (copy, not symlink — Windows compatibility).

**CLI** prints the absolute path on success.

**Tray** shows a notification "Support bundle saved: `<path>`. Click to
reveal." Clicking opens Finder/Explorer with the file selected.

## Tray Integration

In `cmd/otto-tray/tray.go`, add a menu entry near existing diagnostic items:
"Create Support Bundle…". On click:

1. Confirmation dialog: "Create a redacted support bundle? Secrets will be
   masked. Continue?"
2. Shell out (sync) to `otto-gw support` / `otto-gw.ps1 support`, install
   root passed via env
3. On success: notification with path + reveal-in-Finder/Explorer action
4. On failure: error dialog showing the tail of stderr from the script
5. No goroutine/cancellation — collection completes in seconds

Reveal-in-Finder/Explorer helpers go in
`uihelpers_darwin.go` / `uihelpers_windows.go` if not already present
(`open -R <path>` / `explorer.exe /select,<path>`).

## CLI Surface

```
otto-gw support [--out DIR] [--max-mb N] [--log-days D]
```

- `--out DIR` — override output directory (default: `$OTTO_INSTALL_ROOT/support`)
- `--max-mb N` — bundle size cap in MB (default: 50)
- `--log-days D` — include rotated `.log.gz` files modified in last D days
  (default: 7)

`usage()` updated to document the subcommand.

## Testing

- **Unit, bash:** `tests/scripts/test-support-redact.sh` — fixture env and
  log lines through the redaction helper; asserts masked output. Runs in CI
  on POSIX.
- **Unit, pwsh:** `tests/scripts/test-support-redact.ps1` — mirror on the
  PowerShell helper.
- **Integration, bash:** `tests/scripts/test-support-bundle.sh` — start a
  test gateway (or stub), run `otto-gw support`, untar the bundle, assert
  expected files exist and key strings are absent (`grep -L 'AUTH_TOKEN=[^=R]'`
  style — no real secret value should appear).
- **Integration, pwsh:** mirror on Windows runner.
- **Tray:** manual smoke. Tray UI tests are not automated in this codebase;
  the menu item handler is a thin wrapper around the script and a notification
  call, both of which are covered by the script tests and the existing tray
  notification path.

## Files Touched

**Modified:**

- `scripts/otto-gw` — new `support()` function, dispatch case, `usage()`
- `scripts/otto-gw.ps1` — same on the Windows side
- `cmd/otto-tray/tray.go` — new menu item + click handler
- `cmd/otto-tray/uihelpers_darwin.go`, `uihelpers_windows.go` —
  reveal-in-finder/explorer helper if not already present

**New:**

- `scripts/lib/redact.sh` — shared redaction filter for bash
- `scripts/lib/redact.ps1` — shared redaction filter for pwsh
- `tests/scripts/test-support-redact.sh`
- `tests/scripts/test-support-redact.ps1`
- `tests/scripts/test-support-bundle.sh`
- `tests/scripts/test-support-bundle.ps1`

## Out of Scope

- A `--include-secrets` flag (explicitly rejected — Q1)
- Auto-upload (S3, HTTPS POST to a triage endpoint) — out of scope for v1;
  user emails the file
- Auto-opening a mail client with the archive attached — unreliable
  cross-mail-client; not worth the complexity
- A Go-binary `otto-support` (explicitly rejected — Q3)
- Encrypted archives (explicitly rejected — Q1)
- Tray UI automated tests (consistent with existing tray test conventions)

## Open Questions

None at design time. Implementation may surface concrete decisions
(e.g. exact regex for log rotation glob on Windows) that will be resolved
during planning.
