---
phase: quick-260725-h4g
plan: 01
subsystem: ci
tags: [ci, github-actions, golangci-lint, darwin, otto-tray]
status: complete
requires:
  - .github/workflows/ci.yml (existing lint-test-arch job, workflow-level env pins)
provides:
  - ci-darwin-arm-linted
affects:
  - .github/workflows/ci.yml
tech-stack:
  added: []
  patterns:
    - "Second macOS runner in ci.yml, decoupled (no `needs`) so a darwin-only regression reads as its own PR check"
    - "Single workflow-level version pin consumed by two lint steps via `${{ env.GOLANGCI_LINT_VERSION }}`"
key-files:
  created: []
  modified:
    - .github/workflows/ci.yml
decisions:
  - "Runner is macos-latest, not ubuntu-latest with a GOOS=darwin override — the cross-lint is structurally impossible at any CGO_ENABLED setting"
  - "Lint scope is ./... , not ./cmd/otto-tray/... — internal/procstat/procstat_other.go is darwin-tagged and lives outside the tray"
  - "No GOOS=windows lint job in this task; carried forward as a follow-up because the windows findings need non-mechanical fixes"
metrics:
  tasks: 2
  files-changed: 1
  commits: 1
  completed: 2026-07-25
---

# Quick Task 260725-h4g: Add a darwin-arm golangci-lint job to CI — Summary

Added a `lint-darwin` job to `.github/workflows/ci.yml` that runs `golangci-lint`
on `macos-latest` over `./...`, closing the gap where every darwin-tagged file in
the repo was linted by no CI job at all.

## What Changed

One file, one commit, insertions only (45 lines added, 0 deleted).

**`be74593` — `ci: lint the darwin build arm on macos-latest`**

Inserted a `lint-darwin` job between the end of `lint-test-arch` and the
`cross-compile-smoke:` job key:

- `runs-on: macos-latest`, `timeout-minutes: 15`
- `actions/checkout@v4`, then `actions/setup-go@v5` with `go-version-file: go.mod`
- `golangci/golangci-lint-action@v7`, `version: ${{ env.GOLANGCI_LINT_VERSION }}`,
  `args: --timeout=5m`
- No `needs`, no job-level `permissions`, no job-level `env`

Comment blocks explain the runner choice and the scope choice in the file's
existing why-first voice.

## Why This Job Exists

`cmd/otto-tray` is `//go:build darwin || windows`, and seven of its files are
darwin-only, plus `cmd/otto-tray/icon/icon_darwin.go` and
`internal/procstat/procstat_other.go`. The pre-existing `lint-test-arch` job runs
on `ubuntu-latest`, where build tags drop every one of them. Two real findings
survived undetected in that blind spot until a developer happened to run
`make lint` on a mac (quick task 260725-gp6). This job turns that class of
regression into a red X in the PR check list.

## Verification Results

### Structural (Task 1) — all pass

| Check | Result |
|-------|--------|
| `ci.yml` parses as YAML, exactly 4 jobs | PASS |
| `lint-darwin` on `macos-latest` | PASS |
| No `needs` / job-level `permissions` / job-level `env` | PASS |
| `golangci/golangci-lint-action@v7`, `args: --timeout=5m` | PASS |
| `version` sourced from `env.GOLANGCI_LINT_VERSION` | PASS |
| Workflow pin still `v2.12.2`, declared exactly once (`grep -c` = 1) | PASS |
| `git diff --name-only HEAD~1 HEAD` == `.github/workflows/ci.yml` | PASS |
| Commit contains zero deletions | PASS |

### Local darwin-arm lint proxy (Task 2)

Commands run on this macOS arm64 box, against commit `be74593`:

```
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...
go vet ./...
```

`go vet ./...` — **clean, exit 0.**

`golangci-lint` v2.12.2 — **2 issues, exit 1:**

```
cmd/otto-tray/openfolder.go:120:2: runOpenDesktopFolder - goos always receives "darwin" (unparam)
cmd/otto-tray/desktop_darwin.go:28:16: error returned from external package is unwrapped: sig: func (*os/exec.Cmd).Run() error (wrapcheck)
```

**These are a stale-base artifact, not a defect in this work, and not a
`<deviation_rules>` halt condition.** Diagnosis confirmed on four independent
signals before being accepted:

1. This worktree's base is `695462c`, which predates the two gp6 fixes.
2. `git log --oneline -1 -- cmd/otto-tray/desktop_darwin.go` returns `c37a0fe`,
   not `dd871d0` — the wrapcheck fix is demonstrably absent from this tree.
3. The two fix commits map one-to-one onto the two findings:
   `dd871d0 fix(tray): wrap pgrep error in platformDesktopRunning` touches
   `cmd/otto-tray/desktop_darwin.go` (the wrapcheck finding), and
   `28bf084 docs(tray): justify load-bearing goos param on runOpenDesktopFolder`
   touches `cmd/otto-tray/openfolder.go` (the unparam finding).
4. `git merge-base --is-ancestor dd871d0 12b3fa6` → 0 (in main);
   `git merge-base --is-ancestor dd871d0 HEAD` → 1 (not in this tree).

No finding appeared outside those two, and none in `.github/`. Per the execution
briefing, the lint gate is therefore **satisfied-modulo-base**. The gp6 fixes were
deliberately NOT ported, cherry-picked, or re-implemented here, and `.golangci.yml`
was not weakened.

The planner separately recorded `0 issues` at HEAD `488698e` (which contains both
gp6 fixes) on this same box. Combined with signal 3 above, the reasonable inference
is that the darwin arm is clean once this branch merges — but that is an inference
from two separate runs, not a single run I performed on a tree containing both the
fixes and this job.

### Merge safety

`git diff --name-only 695462c 12b3fa6` lists no `.github/` path. This commit's only
file is untouched by every commit between the base and main, so the merge back is a
clean three-way.

### What is NOT verified, and is NOT claimed

The local run is a **proxy, not proof**. It is the same command, same linter version,
same `.golangci.yml`, same GOOS/GOARCH the runner will use — which is why it is
meaningful. It does **not** cover:

- GitHub's `macos-latest` runner image and its preinstalled toolchain
- `actions/setup-go` cache behavior on that image
- `golangci-lint-action@v7`'s own installer resolving `v2.12.2` on macOS
- Network availability and module download under `readonly` mode

**No claim is made anywhere that the `lint-darwin` job passes on GitHub.** It has
never been executed. The first real signal is the CI run triggered when the
orchestrator pushes main.

## Deviations from Plan

**1. [Process] Tree-clean assertion scoped to exclude `.planning/`**

- **Found during:** Task 2 verification
- **Issue:** Task 2's `test -z "$(git status --porcelain)"` would fail because the
  execution briefing required materializing `260725-h4g-PLAN.md` into this worktree
  (it is absent from the stale base), and `.planning/` is not gitignored in this repo
  (`git check-ignore .planning/STATE.md` → exit 1).
- **Resolution:** Ran `git status --porcelain -- ':!.planning'` instead, which returned
  empty. The assertion's intent — that Task 2 modified no source — holds exactly.
- **Files modified:** none

**2. [Docs] Plan cites `desktop_windows.go:69,76`; actual call sites are 67 and 75**

- **Found during:** Follow-up write-up
- **Detail:** `windows.OpenProcess` is at line 67 and `windows.QueryFullProcessImageName`
  at line 75; the plan's 69/76 refer to the unwrapped `return` statements just after.
  Substance unchanged. Corrected below rather than propagated.
- **Files modified:** none

No auto-fixes under Rules 1-3 were needed. No architectural decisions arose.

## Follow-Up: `GOOS=windows` Lint Job

Explicitly out of scope here, and it is **not** a mechanical repeat of this task —
the windows arm is currently dirty, so the job would go red on its first run:

- `cmd/otto-tray/desktop_windows.go` returns unwrapped errors from
  `windows.OpenProcess` (line 67) and `windows.QueryFullProcessImageName` (line 75);
  `wrapcheck` would flag both.
- Wrapping them is not a one-liner: `windowsProcessGone` (line 81) does
  `errors.Is(err, windows.ERROR_INVALID_PARAMETER)` and deliberately **fails closed**
  on anything else. Wrapping the errors upstream must preserve that `errors.Is`
  chain, or the tray silently changes its liveness semantics.

Sequencing should mirror gp6 → h4g: fix the windows findings first, then add the job.
A windows job also cannot reuse this one's runner — it needs `windows-latest`.

## Known Stubs

None. This task added no code paths, only CI configuration.

## Threat Flags

None. No new third-party action entered the repo (`golangci-lint-action@v7` was
already trusted by `lint-test-arch`), the linter version resolves from the single
existing pin, and the job declares no `permissions` block so it inherits the
workflow-level `contents: read` (asserted structurally, so a later widening trips
the same gate). No `go.mod` / `go.sum` change.

## Self-Check: PASSED

- `.github/workflows/ci.yml` exists and contains the `lint-darwin` job — FOUND
- Commit `be74593` exists in `git log` — FOUND
- `git diff --name-only HEAD~1 HEAD` == `.github/workflows/ci.yml` — CONFIRMED
- Commit deletions: none — CONFIRMED
- No Go source, `.golangci.yml`, `release.yml`, or Makefile modified — CONFIRMED
- No tag created, no push performed, no `make build` / `make package` run — CONFIRMED
