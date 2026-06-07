---
phase: 11-gofumpt-tree-wide-cleanup-pre-commit-gate
plan: 01
subsystem: tooling
tags: [gofumpt, pre-commit, ci, formatting, v1.6-close]
requires: [FMT-01, FMT-02, CI-01]
provides:
  - "gofumpt drift gate at git-commit time via .pre-commit-config.yaml"
  - "Contributor-facing pre-commit gate enablement docs in docs/operating.md"
  - "Audit-trail carve-out: govulncheck stdlib CVEs routed to v1.7"
affects:
  - ".pre-commit-config.yaml (new gofumpt hook entry under existing local block)"
  - "scripts/pre-commit-gofumpt.sh (new — hook delegate)"
  - "docs/operating.md (new 'Pre-commit gate' section)"
tech-stack:
  added:
    - "scripts/pre-commit-gofumpt.sh (Bash hook delegate)"
  patterns:
    - "pre-commit framework `local` hook delegating to a repo-tracked script"
    - "Hook scopes to staged files via `files: \\.go$` + `pass_filenames: true`"
key-files:
  created:
    - "scripts/pre-commit-gofumpt.sh"
    - ".planning/phases/11-gofumpt-tree-wide-cleanup-pre-commit-gate/11-01-SUMMARY.md"
  modified:
    - ".pre-commit-config.yaml"
    - "docs/operating.md"
decisions:
  - "D-11-01: FMT-02 govulncheck step is carved out and routed to v1.7 per Phase 10 closure (10-04-SUMMARY.md). On this dev box the govulncheck binary is not even installed; either way the §3.12 sequence steps fmt-check → vet → build → lint → test-race → arch-lint → examples all exit 0."
  - "D-11-02: CI-01 satisfied by extending .pre-commit-config.yaml — not by adding a `make pre-commit` target. The framework already pins golangci-lint/shellcheck/gitleaks/etc.; one local-hook stanza is the lowest-friction reuse path."
  - "D-11-03 (executor): Hook body extracted to scripts/pre-commit-gofumpt.sh because inline bash with `gofumpt not installed — run: go install …` triggers YAML's `mapping values are not allowed` (the inner `: ` looks like a mapping). External script avoids quoting gymnastics and remains greppable/auditable."
metrics:
  duration_minutes: ~10
  tasks_completed: 4
  files_created: 2
  files_modified: 2
  commits: 2
---

# Phase 11 Plan 01: gofumpt + Pre-commit Gate Summary

Close out v1.6 by re-verifying FMT-01 + FMT-02 against the brief §3.12
trust-gate sequence (minus the v1.7-routed govulncheck step) and add a
contributor-installable `gofumpt` gate to the existing `pre-commit`
framework with operator-facing enablement docs.

## What changed

### .pre-commit-config.yaml

Added a new `gofumpt` hook entry under the existing `- repo: local`
block (the same block that already holds `go-mod-tidy`). The hook
delegates to `scripts/pre-commit-gofumpt.sh`:

```yaml
      - id: gofumpt
        name: gofumpt
        language: system
        entry: scripts/pre-commit-gofumpt.sh
        files: \.go$
        pass_filenames: true
```

No other hook entry touched; no `rev:` pin bumped. The diff stays inside
the existing `local` block per D-11-02 and the plan's scope guard.

### scripts/pre-commit-gofumpt.sh (new)

Small Bash delegate (~25 lines, executable) that:

- Detects missing `gofumpt` and prints an install hint
  (`go install mvdan.cc/gofumpt@latest`).
- Runs `gofumpt -l "$@"` against the staged file paths the framework
  passes positionally.
- Exits non-zero on any flagged file, printing the violating paths and
  a `gofumpt -w <file>` remediation hint so the operator can fix the
  files and re-stage.

Created because the inline-bash hook body specified by the plan
contained `: `-bearing strings (e.g., `run: go install …`) that YAML
parses as nested mappings, producing a `mapping values are not allowed
here` ScannerError. The external script keeps the hook body
greppable, single-source, and avoids YAML quoting gymnastics.

### docs/operating.md

Inserted a new top-level section **"Pre-commit gate"** just before
the closing `## Known Limitations` section. Covers (per Task 4 spec):

1. Rationale — catch gofumpt + lint regressions before push; CI-01.
2. Prerequisites — `brew install pre-commit` / `pip install
   pre-commit` / `pipx install pre-commit`; `go install
   mvdan.cc/gofumpt@latest`.
3. Enable — `pre-commit install` (per-clone, not per-machine).
4. What the gate runs — every hook in `.pre-commit-config.yaml`,
   including the new `gofumpt` entry.
5. Manual whole-tree run — `pre-commit run --all-files` and
   `pre-commit run gofumpt --all-files`.
6. Bypass note — `git commit --no-verify` is discouraged; if used,
   run `make fmt-check lint` by hand first.

Section is 71 lines; slightly over the plan's `~60-line` advisory
because the section spec required six discrete subsections and an
intro paragraph. No "while I'm here" edits.

## Verification evidence

### FMT-01 — gofumpt -d . clean on main

```text
$ ~/go/bin/gofumpt -l . | wc -l | tr -d ' '
0
```

No drift; no Task 1 fix commit needed.

### FMT-02 — brief §3.12 sequence (minus govulncheck)

```text
$ make fmt-check && make vet && make build && make lint && \
    make test-race && make arch-lint && make examples
# … all steps exit 0:
go vet ./...
go build -ldflags="…" -o bin/otto-gateway ./cmd/otto-gateway
golangci-lint run ./...
0 issues.
go test -race ./...  → ok across all packages
go-arch-lint check --project-path .  → OK - No warnings found
go test -run Example ./...  → all ok / no tests to run
```

Exit code 0 end-to-end on the chained command.

### FMT-02 govulncheck carve-out (D-11-01)

The terminal `govulncheck ./...` step in `make ci` does **not** exit 0
in v1.6.

**On this executor's dev box** the failure mode is binary-missing:

```text
/Users/coreyellis/go/bin/govulncheck
make: /Users/coreyellis/go/bin/govulncheck: No such file or directory
make: *** [ci] Error 1
```

This is a degenerate variant of the failure recorded at phase-plan
time, where govulncheck was installed but reported multiple unmasked
Go stdlib CVEs (GO-2026-5039 / -5037 / -4982 / -4980 / -4971 / -4947
/ -4946 / -4870 / …). Either failure mode is the same v1.6 outcome:
the govulncheck step is **not** green on `main`.

**Routing decision (D-11-01).** Per
`.planning/phases/10-golangci-lint-v2-cleanup-re-gate/10-04-SUMMARY.md`,
the unmasked stdlib CVEs require a Go toolchain bump and are routed
to v1.7. v1.6's "narrow-scope, debt-reduction" envelope
(`REQUIREMENTS.md` Out of Scope §1) excludes toolchain bumps; the
Phase 10 closure already adjudicated this. v1.7's vulnerability
sweep will reinstall govulncheck (where missing) and address the
underlying CVEs in one motion.

REQUIREMENTS.md's FMT-02 traceability row will read:
`Phase 11 | 11-01 | Complete (govulncheck routed to v1.7)`.

### CI-01 hook present

```text
$ grep -c '^[[:space:]]*- id: gofumpt' .pre-commit-config.yaml
1
$ python3 -c 'import yaml; yaml.safe_load(open(".pre-commit-config.yaml"))'
# (exits 0 — YAML is well-formed)
```

### CI-01 docs present

```text
$ grep -c 'pre-commit install' docs/operating.md
1
$ grep -c 'gofumpt' docs/operating.md
8
```

### CI-01 live hook smoke-test

`pre-commit run gofumpt --all-files` was **not** executed because the
`pre-commit` framework binary is not installed on this dev box. The
plan permits this fallback and routes live execution to Task 4's
contributor docs walkthrough.

Instead the executor smoke-tested `scripts/pre-commit-gofumpt.sh`
directly:

- **Clean tree path (must exit 0):**
  `./scripts/pre-commit-gofumpt.sh cmd/otto-gateway/main.go` → exit 0.
- **Intentionally-malformed file (must exit 1):**
  feeding a file with `import   "fmt"` and `func main(){…}` →
  exit 1, with the violation listing and `gofumpt -w <file>` hint
  printed to stderr.

The hook behaves correctly under both paths. Contributors who install
`pre-commit` per the new operating.md section will see the gate fire
on `git commit`.

## Deviations from Plan

### Rule 1 (Bug) — inline bash entry triggered YAML ScannerError

**Found during:** Task 3.

**Issue:** The exact `entry: bash -c '… "gofumpt not installed — run:
go install mvdan.cc/gofumpt@latest" …'` string the plan specified
contains `: ` sequences (`run: go`, `mvdan.cc/gofumpt:`-adjacent
characters) inside a plain YAML scalar. PyYAML's `safe_load` rejected
this with `mapping values are not allowed here` at line 43, col 98.

**Fix:** Extract the hook body to `scripts/pre-commit-gofumpt.sh` and
change the YAML entry to `entry: scripts/pre-commit-gofumpt.sh`. The
script is repo-tracked, chmod +x, and contains the same logic as the
plan's inline bash with no semantic change (still: detect missing
gofumpt with install hint, run `gofumpt -l "$@"`, exit non-zero on
any violation with `gofumpt -w` remediation hint).

**Files modified:** `.pre-commit-config.yaml`,
`scripts/pre-commit-gofumpt.sh` (created).

**Commit:** `0d79ef4` (folded into Task 3 commit since it's how the
task gets to "valid YAML + working hook").

**Audit note:** The plan's hook-entry spec was a verbatim YAML
suggestion; the executor's deviation is in *encoding* (inline vs.
file), not in *behavior*. CI-01's acceptance criteria
(`grep -c '^[[:space:]]*- id: gofumpt' .pre-commit-config.yaml`
returns ≥ 1, hook fires on violations) are satisfied either way.
Recorded as D-11-03 in the frontmatter.

### No other deviations

- No "while I'm here" edits.
- No unrelated commits.
- No `rev:` pins touched on existing pre-commit-config hooks.
- No `make pre-commit` target added (D-11-02 holds).
- No govulncheck fix attempted (D-11-01 routes to v1.7).

## Commits

| Hash      | Type | Message                                                       |
| --------- | ---- | ------------------------------------------------------------- |
| (none)    | —    | Task 1 (FMT-01 verify) — clean on first run, no commit needed |
| (none)    | —    | Task 2 (FMT-02 verify) — verification-only, no commit needed  |
| `0d79ef4` | feat | `feat(11-01-T3): add gofumpt to pre-commit hooks (CI-01)`     |
| `2ec6818` | docs | `docs(11-01-T4): document pre-commit gate enablement (CI-01)` |

## Phase 11 close checklist

- [x] **FMT-01:** `~/go/bin/gofumpt -l .` returns zero lines.
- [x] **FMT-02:** §3.12 chained command exits 0; govulncheck failure
      documented with v1.7 routing citation.
- [x] **CI-01 hook:** `.pre-commit-config.yaml` has a `gofumpt` hook
      that blocks commits on violations (verified via direct
      script smoke-test in both pass + fail modes).
- [x] **CI-01 docs:** `docs/operating.md` has a "Pre-commit gate"
      section with copy-paste enablement steps.
- [x] **D-11-02 (hook-vs-make-target) recorded** here.
- [x] **D-11-01 (govulncheck carve-out) recorded** here.
- [ ] **REQUIREMENTS.md / ROADMAP.md updates** — owned by the
      orchestrator post-wave per the executor prompt; the executor
      did not touch STATE.md or ROADMAP.md.

## Self-Check: PASSED

- `.pre-commit-config.yaml` — present, gofumpt hook entry exists
  (`grep -c '^[[:space:]]*- id: gofumpt' .pre-commit-config.yaml` = 1).
- `scripts/pre-commit-gofumpt.sh` — present, executable, smoke-tested.
- `docs/operating.md` — Pre-commit gate section present
  (`grep -c 'pre-commit install' docs/operating.md` = 1).
- Commit `0d79ef4` — present in `git log`.
- Commit `2ec6818` — present in `git log`.
- No modifications to `.planning/STATE.md` or `.planning/ROADMAP.md`
  (executor scope respected).
