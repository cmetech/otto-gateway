---
phase: 260601-dzc
plan: 01
subsystem: otto-gw-wrappers
tags: [wrapper, ux, docs, cli]
dependency_graph:
  requires: []
  provides:
    - "scripts/otto-gw -h / --help / help -> stdout + exit 0"
    - "scripts/otto-gw.ps1 -Help -> stdout + exit 0 (param-block switch + early-return guard)"
    - "Docs page CLI card announces -h/--help/-Help"
  affects:
    - scripts/otto-gw
    - scripts/otto-gw.ps1
    - internal/admin/templates/docs.html.tmpl
tech_stack:
  added: []
  patterns:
    - "POSIX usage(exit_code) — heredoc-to-stdout when 0, heredoc-to-stderr otherwise"
    - "PowerShell early-return help guard before switch dispatch; Show-Usage -ExitCode param"
key_files:
  created: []
  modified:
    - scripts/otto-gw
    - scripts/otto-gw.ps1
    - internal/admin/templates/docs.html.tmpl
decisions:
  - "Show-Usage in PS1 takes an -ExitCode param so the guard's `exit 0` is reachable (mirror of POSIX usage(exit_code) pattern)"
metrics:
  duration_minutes: 14
  completed_date: 2026-06-01
requirements: [QUICK-260601-dzc]
---

# Phase 260601-dzc Plan 01: otto-gw -h/--help/-Help Support Summary

## One-liner

Added proper `-h` / `--help` / `-Help` invocation to the POSIX wrapper, the PowerShell wrapper, and the admin Docs CLI card — help requests now go to stdout with exit 0; existing no-arg / unknown-subcommand failure modes (stderr + exit 1) preserved.

## What changed

### Edit 1 — `scripts/otto-gw` (POSIX wrapper)

- `usage()` refactored to take an optional exit-code argument (defaults to 1, preserves prior behavior). When called as `usage 0`, the heredoc body is emitted to **stdout**; otherwise to **stderr**. Final `exit "$exit_code"`.
- Heredoc body is byte-identical between the two branches (verified by `diff /tmp/h_out /tmp/na_err -> identical`).
- New case dispatch arm inserted **before** the catch-all `*) usage ;;`:
  ```bash
  help|-h|--help) usage 0 ;;
  ```
- Catch-all left untouched so `no-args` and `bogus` still hit stderr + exit 1.

### Edit 2 — `scripts/otto-gw.ps1` (PowerShell wrapper)

- `[switch]$Help` added to the `param()` block, adjacent to the other wrapper-level switches (placed just before `[switch]$Trace`).
- Early-return guard inserted **before** the main `switch ($Command)`:
  ```powershell
  if ($Help -or $Command -eq "help" -or $Command -eq "-h" -or $Command -eq "--help") {
      Show-Usage -ExitCode 0
      exit 0
  }
  ```
- Explicit `"help" { Show-Usage -ExitCode 0 }` added inside the `switch` for defense-in-depth (defaulted `$Command = "help"` still flows through the guard first; this arm only fires if someone bypasses the guard).
- `Show-Usage` itself given a `param([int]$ExitCode = 1)`; trailing `exit 1` becomes `exit $ExitCode`. Without this the guard's `exit 0` would have been unreachable — Show-Usage exits before returning. Documented in Decisions.
- pwsh not installed on the dev box (mirroring quick 260531-oax policy): visual / static review only. Smoke testing deferred to the operator.

### Edit 3 — `internal/admin/templates/docs.html.tmpl`

- Single new prose line added to the **CLI &amp; startup (otto-gw wrapper)** card, between the existing intro paragraph and the first `<h3>` (POSIX heading):
  ```html
  <p>Run <code>otto-gw -h</code> / <code>otto-gw --help</code> (POSIX) or <code>otto-gw.ps1 -Help</code> (PowerShell) at any time to print the full usage to your terminal.</p>
  ```
- Surrounding HTML and whitespace match the existing card style. `<code>` wrapping matches the rest of the file.

## Verification

All `<verify><automated>` checks pass:

| Check | Result |
| ---- | ---- |
| `bash -n scripts/otto-gw` | clean |
| `shellcheck scripts/otto-gw` | no findings |
| `bash scripts/otto-gw -h` | exit=0, stdout=5034B, stderr=0 |
| `bash scripts/otto-gw --help` | exit=0, stdout=5034B, stderr=0 |
| `bash scripts/otto-gw help` | exit=0, stdout=5034B, stderr=0 |
| `bash scripts/otto-gw` (no args) | exit=1, stderr=5034B (regression preserved) |
| `bash scripts/otto-gw bogus` | exit=1, stderr=5034B (regression preserved) |
| `grep -q '\[switch\]\$Help' scripts/otto-gw.ps1` | match at line 40 |
| `grep -q '\$Help -or' scripts/otto-gw.ps1` | match at line 1428 |
| `grep -q 'otto-gw -h' internal/admin/templates/docs.html.tmpl` | match in CLI card |
| `diff /tmp/h_out /tmp/na_err` | identical (heredoc body byte-equal across both branches) |

Single atomic commit: `1461294 feat(otto-gw): add -h/--help/-Help support to wrappers` — touches exactly the three files in `files_modified`.

## Deviations from Plan

### [Rule 1 - Bug] PS1 Show-Usage `exit 1` shadowed the guard's `exit 0`

- **Found during:** Edit 2 review (before commit)
- **Issue:** The plan's suggested guard `Show-Usage; exit 0` is unreachable in PowerShell because `Show-Usage` has its own `exit 1` at the end of the function. The `exit 0` line after the call could never execute, so a user passing `-Help` would still get exit code 1.
- **Fix:** Added `param([int]$ExitCode = 1)` to `Show-Usage` and changed its trailing `exit 1` to `exit $ExitCode`. The guard now calls `Show-Usage -ExitCode 0` and the `exit 0` afterwards remains as a belt-and-suspenders safety. This mirrors the POSIX `usage(exit_code)` pattern from Edit 1.
- **Files modified:** `scripts/otto-gw.ps1`
- **Commit:** 1461294 (same atomic commit; no separate fix commit)

Note: the original phrasing in the plan still grep-matches (`$Help -or` gate). The plan's documented acceptance — "calls Show-Usage and exits 0" — only holds with this fix; reporting it here so the deviation is on record per Rule 1.

No other deviations. No architectural changes. No package installs. No CLAUDE.md violations. No `git stash` used. No pre-existing `go vet` errors touched.

## Drift Findings (verify-only — DO NOT fix in this task)

Three sources compared:

1. **POSIX `usage()` heredoc** in `scripts/otto-gw` (lines 1646–1828, both stdout and stderr branches — they are byte-identical so only one was scanned).
2. **PowerShell `Show-Usage` heredoc** in `scripts/otto-gw.ps1` (lines 1341–1424).
3. **Docs `Flag & switch reference` table** in `internal/admin/templates/docs.html.tmpl` (rendered from `CliFlags` in `docsHandler`; lines 84–115).

### POSIX flags vs PS1 switches vs Docs table — full enumeration

| Token | POSIX usage() lists | PS1 Show-Usage lists | Docs CliFlags row | Notes |
| ----- | :-: | :-: | :-: | ----- |
| `--pii` / `-Pii` | yes | yes | yes | aligned |
| `--hash-key` / `-HashKey` | yes | yes | yes | aligned |
| `--entities` / `-Entities` | yes | yes | yes | aligned |
| `--hooks` / `-Hooks` | yes | yes | yes | aligned |
| `--auth` / `-Auth` | yes | yes | yes | aligned |
| `--idle-timeout` / `-IdleTimeout` | yes | yes | yes | aligned |
| `--debug` / `-Debug` | yes | yes | yes | aligned |
| `--trace` / `-Trace` | yes | yes | yes | aligned |
| `--env-file` / `-EnvFile` | yes | yes | yes | aligned |
| `--overrides-file` / `-OverridesFile` | yes | yes | yes | aligned |
| `--template` / `-Template` | yes | yes | yes | aligned |
| `--dest` / `-Dest` | yes | yes | yes | aligned |
| `--overrides-dest` / `-OverridesDest` | yes | yes | yes | aligned |
| `--dry-run` / `-DryRun` | yes | yes | yes | aligned |
| `--yes` / `-Yes` | yes | yes | yes | aligned |
| `--here` / `-Here` | yes | yes | yes | aligned |
| `--force` / `-Force` | yes | yes | yes | aligned |
| `--non-interactive` / `-NonInteractive` | yes | yes | yes | aligned |
| `--auth-enabled` / `-AuthEnabled` | yes | yes | yes | aligned |
| `--chat-trace` / `-ChatTrace` | yes | yes | yes | aligned |
| `--regenerate-secrets` / `-RegenerateSecrets` | yes | yes | yes | aligned |
| `--auth-token` / `-AuthToken` | yes | yes | yes | aligned |
| `--kiro` / `-Kiro` | yes | yes | yes | aligned |
| `--addr` / `-Addr` | yes | yes | yes (column-wise via `init` rows downstream of line 114) | aligned |
| `--show-secrets` / `-ShowSecrets` | **partial** — appears only inline as `env [--show-secrets]` in the heredoc; **no standalone description row** in the "Gateway config flags" block | **partial** — same inline-only treatment as `env [-ShowSecrets]` | yes — full row in docs CliFlags table | **DRIFT:** Docs table promotes show-secrets to a first-class flag row; both wrappers only mention it inline against the `env` subcommand. |
| `-Follow` (PowerShell-only) | n/a — POSIX uses `logs -f` positional, not a flag | yes — `-Follow` documented in PS1 Show-Usage under `logs` | yes — docs table dedicates an em-dash POSIX cell and notes "PowerShell-only" | aligned — drift is documented intentional |
| `-h` / `--help` / `-Help` (this change) | yes — added to dispatch but **not listed in heredoc** | yes — added to param block but **not listed in Show-Usage heredoc**, nor in docs CliFlags table | **no row** (prose-only mention in CLI card intro) | **DRIFT (introduced by this change, by design):** Help invocation is announced in prose but not enumerated in the formal flag tables. Acceptable for this task; the planner explicitly said "Do NOT restructure usage text or add new flags." Follow-up task can add a one-row entry to all three. |

### Drift summary

- **1 pre-existing drift:** `--show-secrets` is a docs-first-class flag but only an inline mention in both wrapper usage texts.
- **1 deliberately-introduced drift:** the new help flags are exposed via dispatch + prose-line announcement but not enumerated in any of the three formal flag tables — by design (planner instruction).
- **0 cross-wrapper drift (POSIX vs PS1):** every flag in one is in the other (modulo the `-Follow` / `logs -f` shape mismatch, which is already documented in the docs table with the em-dash convention).

Fix is a follow-up — this task only records the findings.

## Known Stubs

None.

## Threat Flags

None — no new network endpoints, auth paths, file-access patterns, or schema changes introduced. The change is wrapper/UX surface only and the docs template change is a prose line.

## Self-Check: PASSED

- `[ -f scripts/otto-gw ]` → FOUND
- `[ -f scripts/otto-gw.ps1 ]` → FOUND
- `[ -f internal/admin/templates/docs.html.tmpl ]` → FOUND
- Commit `1461294` → FOUND in `git log --oneline` of `worktree-agent-a09ab32aa0e57c1ec`
- All 11 verification gates above → all green
