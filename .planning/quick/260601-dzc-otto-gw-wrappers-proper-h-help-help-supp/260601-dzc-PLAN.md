---
phase: 260601-dzc
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - scripts/otto-gw
  - scripts/otto-gw.ps1
  - internal/admin/templates/docs.html.tmpl
autonomous: true
requirements: [QUICK-260601-dzc]
must_haves:
  truths:
    - "Running `bash scripts/otto-gw -h` prints usage to stdout and exits 0"
    - "Running `bash scripts/otto-gw --help` prints usage to stdout and exits 0"
    - "Running `bash scripts/otto-gw help` prints usage to stdout and exits 0"
    - "Running `bash scripts/otto-gw` (no args) still prints to stderr and exits 1"
    - "Running `bash scripts/otto-gw bogus` still prints to stderr and exits 1"
    - "scripts/otto-gw.ps1 param block declares [switch]$Help"
    - "scripts/otto-gw.ps1 has an early-return guard or explicit `help` case that calls Show-Usage and exits 0"
    - "docs.html.tmpl mentions the new -h/--help/-Help invocations in the CLI flags / startup card"
  artifacts:
    - path: scripts/otto-gw
      provides: "POSIX wrapper with usage() accepting exit code + help|-h|--help dispatch case"
      contains: "help|-h|--help) usage 0"
    - path: scripts/otto-gw.ps1
      provides: "PowerShell wrapper with [switch]$Help + early-return help guard"
      contains: "[switch]$Help"
    - path: internal/admin/templates/docs.html.tmpl
      provides: "Docs page prose line announcing -h/--help/-Help"
      contains: "otto-gw -h"
  key_links:
    - from: scripts/otto-gw
      to: usage()
      via: "case dispatch in main argument parser"
      pattern: "help\\|-h\\|--help\\) usage 0"
    - from: scripts/otto-gw.ps1
      to: Show-Usage
      via: "early-return guard before switch dispatch"
      pattern: "\\$Help -or"
---

<objective>
Add proper `-h` / `--help` / `-Help` invocation across the otto-gw POSIX wrapper, PowerShell wrapper, and admin docs template. When a user requests help explicitly, usage must go to stdout and the process must exit 0. Existing failure modes (no args, unknown subcommand) must continue to write to stderr and exit 1.

Purpose: Make the wrappers behave like well-formed CLI tools — `-h`/`--help` are queries, not errors. Today they fall through to the default `*) usage ;;` case, producing stderr output and exit code 1, which trips up shell pipelines, doc tooling, and operator muscle memory.

Output: Modified `scripts/otto-gw`, `scripts/otto-gw.ps1`, and `internal/admin/templates/docs.html.tmpl`. Plus a SUMMARY.md that records the drift check between POSIX usage(), PS1 Show-Usage, and the 26-row CliFlags table in docs.html.tmpl (verify-only — do not fix drift in this change).
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@scripts/otto-gw
@scripts/otto-gw.ps1
@internal/admin/templates/docs.html.tmpl
</context>

<tasks>

<task type="auto">
  <name>Task 1: Add -h/--help/-Help support to otto-gw wrappers + docs note</name>
  <files>scripts/otto-gw, scripts/otto-gw.ps1, internal/admin/templates/docs.html.tmpl</files>
  <action>
    Three coordinated edits in a single atomic commit. All paths repo-relative. POSIX bash 3.2 portable. Do NOT use `git stash`. Do NOT fix pre-existing `go vet` errors. Do NOT restructure usage text or add new flags. Do NOT fix any drift between POSIX usage() and PS1 Show-Usage and the docs CliFlags table — record drift in SUMMARY only.

    Edit 1 — scripts/otto-gw (POSIX wrapper):
    Refactor the existing `usage()` function (currently at ~line 1646). Make it accept an optional exit-code argument that defaults to 1. When the argument is 0, emit the heredoc body to stdout; otherwise emit it to stderr. Then `exit "$exit_code"`. Keep the heredoc body byte-identical to today's text — only the redirection target and exit code change. Shape:
        local exit_code="${1:-1}"
        if [[ "$exit_code" -eq 0 ]]; then cat <<'EOF' ... EOF
        else cat >&2 <<'EOF' ... EOF
        fi
        exit "$exit_code"
    Then, in the main case-dispatch block (~line 1755), insert a new case arm BEFORE the catch-all `*) usage ;;`:
        help|-h|--help) usage 0 ;;
    Leave `*) usage ;;` untouched so unknown subcommands still hit stderr + exit 1. Do not touch any other case arms.

    Edit 2 — scripts/otto-gw.ps1 (PowerShell wrapper):
    In the param() block (~line 33), add `[switch]$Help` adjacent to the other [switch] parameters. Pick a position that keeps alphabetical or visual grouping consistent with existing switches in that block.
    Before the main switch dispatch (~line 1426), insert an early-return guard:
        if ($Help -or $Command -eq "help" -or $Command -eq "-h" -or $Command -eq "--help") {
            Show-Usage
            exit 0
        }
    Then, defensively, add an explicit `"help" { Show-Usage }` case inside the switch statement near other no-arg subcommands. Leave the existing `default { Show-Usage }` alone — unknown commands continue to land there.
    Note: pwsh is not available on the dev box. Mirror the policy from quick 260531-oax: visual/static review only; smoke testing deferred to the operator. Do not attempt to invoke pwsh.

    Edit 3 — internal/admin/templates/docs.html.tmpl:
    Locate the CLI flags / startup card section. Add a single prose line BEFORE the existing subcommand code blocks in that card:
        Run <code>otto-gw -h</code> / <code>otto-gw --help</code> (POSIX) or <code>otto-gw.ps1 -Help</code> (PowerShell) at any time to print the full usage to your terminal.
    Wrap inline code refs in `<code>` tags matching the existing pattern in the file. Keep surrounding HTML/whitespace consistent with the file's current style.

    Drift check (verify-only, recorded in SUMMARY):
    Read the current POSIX usage() heredoc body, the PS1 Show-Usage body, and the 26-row flag/switch table in docs.html.tmpl (rendered from CliFlags in docsHandler). For each flag/switch that appears in one source but not the other two, list it in SUMMARY.md under a "Drift Findings" heading. Do NOT modify any of the three sources to reconcile drift — fix is a follow-up task.

    Validation (run before committing):
    - `bash -n scripts/otto-gw` — must be clean
    - `shellcheck scripts/otto-gw` — no NEW findings vs baseline
    - `bash scripts/otto-gw -h >/tmp/out 2>/tmp/err; echo exit=$?` — exit=0, /tmp/out non-empty, /tmp/err empty
    - `bash scripts/otto-gw --help` — same as above
    - `bash scripts/otto-gw help` — same as above
    - `bash scripts/otto-gw >/tmp/out 2>/tmp/err; echo exit=$?` — exit=1, /tmp/err non-empty (regression check)
    - `bash scripts/otto-gw bogus >/tmp/out 2>/tmp/err; echo exit=$?` — exit=1, /tmp/err non-empty (regression check)
    - PS1: grep for `[switch]$Help` in param block AND grep for `$Help -or` early-return guard
    - docs.html.tmpl: grep for the new `otto-gw -h` prose line

    Commit: single atomic commit covering all three files. Message shape:
        feat(otto-gw): add -h/--help/-Help support to wrappers
        - POSIX usage() now accepts exit-code arg; help|-h|--help -> stdout + exit 0
        - PS1 adds [switch]$Help + early-return guard before switch dispatch
        - docs.html.tmpl announces new help invocations on CLI flags card
        - unknown subcommand + no-arg paths preserved (stderr + exit 1)
  </action>
  <verify>
    <automated>
      bash -n scripts/otto-gw &&
      shellcheck scripts/otto-gw &&
      bash scripts/otto-gw -h >/tmp/otto_h_out 2>/tmp/otto_h_err && [ -s /tmp/otto_h_out ] && [ ! -s /tmp/otto_h_err ] &&
      bash scripts/otto-gw --help >/tmp/otto_lh_out 2>/tmp/otto_lh_err && [ -s /tmp/otto_lh_out ] && [ ! -s /tmp/otto_lh_err ] &&
      bash scripts/otto-gw help >/tmp/otto_help_out 2>/tmp/otto_help_err && [ -s /tmp/otto_help_out ] && [ ! -s /tmp/otto_help_err ] &&
      ( bash scripts/otto-gw >/tmp/otto_na_out 2>/tmp/otto_na_err; rc=$?; [ "$rc" -eq 1 ] && [ -s /tmp/otto_na_err ] ) &&
      ( bash scripts/otto-gw bogus >/tmp/otto_bg_out 2>/tmp/otto_bg_err; rc=$?; [ "$rc" -eq 1 ] && [ -s /tmp/otto_bg_err ] ) &&
      grep -q '\[switch\]\$Help' scripts/otto-gw.ps1 &&
      grep -q '\$Help -or' scripts/otto-gw.ps1 &&
      grep -q 'otto-gw -h' internal/admin/templates/docs.html.tmpl
    </automated>
  </verify>
  <done>
    - `scripts/otto-gw -h`, `--help`, and `help` all print to stdout and exit 0
    - `scripts/otto-gw` (no args) and `scripts/otto-gw bogus` still print to stderr and exit 1
    - `scripts/otto-gw.ps1` param block declares `[switch]$Help` and has an early-return guard calling Show-Usage with exit 0
    - `internal/admin/templates/docs.html.tmpl` CLI flags / startup card contains the new help-invocation prose line with `<code>` wrapping
    - `bash -n scripts/otto-gw` clean and `shellcheck scripts/otto-gw` reports no new findings
    - SUMMARY.md records drift findings (verify-only) between POSIX usage(), PS1 Show-Usage, and the docs CliFlags table
    - Single atomic git commit covering all three files
  </done>
</task>

</tasks>

<verification>
- All `<verify><automated>` checks pass in one run
- Manual visual inspection of scripts/otto-gw.ps1 changes (pwsh unavailable — smoke deferred to operator, mirroring quick 260531-oax policy)
- `git log -1 --stat` shows a single commit touching exactly the three files in `files_modified`
- SUMMARY.md exists and includes a "Drift Findings" section listing any flags present in POSIX usage but not PS1 Show-Usage, or in either wrapper but not the docs CliFlags table (or "no drift detected" if clean)
</verification>

<success_criteria>
- `bash scripts/otto-gw -h` exits 0 with stdout output and empty stderr
- `bash scripts/otto-gw --help` exits 0 with stdout output and empty stderr
- `bash scripts/otto-gw help` exits 0 with stdout output and empty stderr
- `bash scripts/otto-gw` (no args) exits 1 with stderr output
- `bash scripts/otto-gw bogus` exits 1 with stderr output
- `shellcheck scripts/otto-gw` produces no new findings vs baseline
- `bash -n scripts/otto-gw` exits 0
- `scripts/otto-gw.ps1` contains both `[switch]$Help` (param block) and `$Help -or` (early-return guard)
- `internal/admin/templates/docs.html.tmpl` contains the new prose line referencing `otto-gw -h`, `otto-gw --help`, and `otto-gw.ps1 -Help` with `<code>` wrapping
- Single atomic commit; no `git stash` used; no pre-existing `go vet` errors touched
- SUMMARY.md drift findings recorded (verify-only — no source reconciliation in this change)
</success_criteria>

<output>
Create `.planning/quick/260601-dzc-otto-gw-wrappers-proper-h-help-help-supp/260601-dzc-SUMMARY.md` when done. Include a "Drift Findings" section comparing POSIX usage() text, PS1 Show-Usage text, and the docs CliFlags table.
</output>
