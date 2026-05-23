---
phase: quick-260523-gna
plan: 01
subsystem: docs/tooling
tags: [dev-environment, onboarding, scripts, macos, windows]
requires: []
provides:
  - DEVELOPERS.md
  - scripts/setup-dev.sh
  - scripts/setup-dev.ps1
affects:
  - new-contributor-onboarding
  - fresh-machine-bootstrap
tech-stack:
  added: []
  patterns:
    - "idempotent bootstrap scripts (command -v / Get-Command probes)"
    - "winget-first / scoop-fallback / go-install-fallback on Windows"
    - "Homebrew on macOS, no sudo, no profile mutation"
key-files:
  created:
    - DEVELOPERS.md
    - scripts/setup-dev.sh
    - scripts/setup-dev.ps1
  modified: []
decisions:
  - "Documented toolchain pins are a strict subset of repo-observed pins (.pre-commit-config.yaml, go.mod) — no invented versions."
  - "Scripts print `pre-commit install` as a next step rather than auto-running it (planner constraint)."
  - "Windows fallback chain is winget → scoop → `go install` (for gosec/gofumpt only); no Chocolatey scripting."
  - "Did not modify README.md (the optional one-line pointer was permitted but not required; left untouched to keep the change minimal)."
metrics:
  duration: "3 minutes"
  tasks_completed: 3
  files_created: 3
  files_modified: 0
  completed: "2026-05-23T16:07:46Z"
requirements:
  - QUICK-260523-GNA-01
---

# Quick Task 260523-gna: Developer Setup Documentation Summary

Created a single source of truth for "how to set up the loop24-gateway dev
environment" covering both macOS and Windows, both manual and scripted paths.
A new contributor (or the author on a fresh machine) can now reach
`make build && make test && make lint` green by following one document, without
spelunking through `docs/briefs/go_port_brief.md` §3.12.

## Files

| Path | Lines | Purpose |
|------|------:|---------|
| `DEVELOPERS.md` | 251 | Repo-root setup guide — required toolchain, scripted + manual paths for macOS/Windows, daily-workflow Makefile pointers, known gotchas, verification checklist. |
| `scripts/setup-dev.sh` | 104 | macOS bootstrap. Bash, `set -euo pipefail`, Homebrew-only, idempotent (`command -v` probe per tool), prints final versions, no sudo, no profile mutation. Executable (chmod +x). |
| `scripts/setup-dev.ps1` | 154 | Windows bootstrap. PowerShell 5.1+, `Set-StrictMode Latest`, `$ErrorActionPreference = 'Stop'`, winget-first / scoop-fallback / `go install` last-resort, idempotent (`Get-Command` probe), no admin elevation, no execution-policy mutation. |

**Total:** 509 lines across 3 new files; 0 existing files modified.

## Toolchain pins documented (and where each was sourced)

| Tool | Version | Source |
|------|---------|--------|
| Go | `1.23` | `go.mod` line `go 1.23` + CLAUDE.md "Go 1.23+ — required for `log/slog` ergonomics" |
| golangci-lint | `v1.62.2` | `.pre-commit-config.yaml` `golangci/golangci-lint` rev |
| gitleaks | `v8.18.4` | `.pre-commit-config.yaml` `gitleaks/gitleaks` rev |
| pre-commit-hooks | `v4.6.0` | `.pre-commit-config.yaml` `pre-commit/pre-commit-hooks` rev (mentioned as awareness item only — not a directly-installed tool) |
| pre-commit (runner) | latest 3.x | not pinned in repo; brew/pip latest |
| gosec | latest | not pinned in repo; brew latest / scoop / `go install` |
| gofumpt | latest | not pinned in repo; brew latest / scoop / `go install` |

No fabricated versions. Stray numeric tokens like `1.22` (in the phrase
"post-1.22 net/http") and `3.12` (in `Python.Python.3.12` for installing
Python on Windows) are non-toolchain context, not pins.

## Gotchas surfaced in DEVELOPERS.md

All four planner-supplied gotchas made it into the **Known issues / gotchas**
section verbatim in intent:

1. **`go mod tidy` pre-commit hook fails on fresh clone** — called out as
   item #1 (highest priority, first thing a new dev hits). Includes the
   `SKIP=go-mod-tidy git commit ...` workaround.
2. **brew vs miniconda PATH ordering for `pre-commit`** — surfaced with
   `which pre-commit` diagnostic.
3. **gosec via golangci-lint AND standalone** — explained that standalone
   is recommended for G204 investigation (subprocess-spawn risk per
   CLAUDE.md).
4. **`pre-commit install` is opt-in** — explicit, with the rationale that
   scripts do NOT silently wire git hooks.

The same `go mod tidy` heads-up is also echoed in both scripts' next-steps
output, satisfying the planner constraint that fresh-clone users see the
warning before they hit the failure.

## Commits

| Task | Commit | Files |
|------|--------|-------|
| 1: DEVELOPERS.md | `2fa18db` | `DEVELOPERS.md` |
| 2: setup-dev.sh | `84bf26a` | `scripts/setup-dev.sh` |
| 3: setup-dev.ps1 | `84562ce` | `scripts/setup-dev.ps1` |

Three atomic commits on `worktree-agent-a9589e06e06d7bab2`, each scoped to
exactly one task's file. No SUMMARY.md / STATE.md / PLAN.md changes
included in code commits — those are handled by the orchestrator's
final docs commit per the plan constraints.

## Executor-discretion decisions

- **Did not modify `README.md`.** The plan permitted an optional one-line
  pointer; I left README.md untouched to keep the diff minimal. README.md
  already has a "Prerequisites" list that DEVELOPERS.md is the expanded
  version of — the two stay consistent without cross-referencing.
- **Removed the literal token `Set-ExecutionPolicy` from the PowerShell
  script's comment header.** The plan's automated verify gate is
  `! grep -q "Set-ExecutionPolicy"`, which trips on the token even inside
  a `#` comment. Reworded the comment to "Does NOT change the execution
  policy …" so the gate passes while preserving the operator-facing
  warning. Deviation Rule 3 (blocking issue with verify gate); no
  functional change to the script.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Reworded `Set-ExecutionPolicy` comment in setup-dev.ps1**
- **Found during:** Task 3 verification.
- **Issue:** The Task 3 verify gate (`! grep -q "Set-ExecutionPolicy"
  scripts/setup-dev.ps1`) failed because the script's comment header read
  "Does NOT call Set-ExecutionPolicy". The grep is intentionally strict to
  ensure the script never alters execution policy, but it doesn't
  distinguish comments from code.
- **Fix:** Reworded the comment line to "Does NOT change the execution
  policy …" so the gate passes while keeping the operator-facing
  guidance intact. No code path changed.
- **Files modified:** `scripts/setup-dev.ps1` (comment header only).
- **Commit:** included in `84562ce` (Task 3 commit — caught and fixed
  before commit, so no separate fix commit was needed).

No other deviations. The plan executed as written.

## Authentication gates

None. No external auth required for any task (all work was local file
authoring and `git commit`).

## Known Stubs

None. Both scripts and the documentation are fully functional — there are
no placeholder values, mock data sources, or "TODO" markers that would
prevent the plan's goal (a working `make build && make test && make lint`
state) from being reached.

## Self-Check: PASSED

Verified after writing:

- `[ -f DEVELOPERS.md ]` → FOUND
- `[ -x scripts/setup-dev.sh ]` → FOUND (and `bash -n` clean)
- `[ -f scripts/setup-dev.ps1 ]` → FOUND
- `git log --oneline | grep 2fa18db` → FOUND
- `git log --oneline | grep 84bf26a` → FOUND
- `git log --oneline | grep 84562ce` → FOUND
- Plan-level verification block (file existence, cross-links, Makefile
  refs, toolchain pin allowlist, idempotency probe counts, working-tree
  cleanliness) → all PASSED.
