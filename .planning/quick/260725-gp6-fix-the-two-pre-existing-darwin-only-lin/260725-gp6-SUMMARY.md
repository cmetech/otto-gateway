---
phase: quick-260725-gp6
plan: 01
subsystem: otto-tray
tags: [lint, darwin, error-wrapping, cross-platform]
status: complete
requires:
  - cmd/otto-tray desktop candidate revalidation path
provides:
  - clean golangci-lint v2.12.2 run for ./cmd/otto-tray/... on macOS
affects:
  - cmd/otto-tray/desktop_darwin.go
  - cmd/otto-tray/openfolder.go
tech-stack:
  added: []
  patterns:
    - "justification-first //nolint directive matching paths.go:59 idiom"
    - "%w wrapping of subprocess errors after raw-error sentinel checks"
key-files:
  created: []
  modified:
    - cmd/otto-tray/desktop_darwin.go
    - cmd/otto-tray/openfolder.go
decisions:
  - "Suppress unparam on goos rather than remove the parameter — it is load-bearing for the windows arm and is the test injection seam"
  - "No new test added: the changed pgrep line is only reachable on a non-0/non-1 real pgrep exit, with no injectable command seam, and would need a darwin build tag CI never runs"
metrics:
  duration: ~3 minutes
  completed: 2026-07-25
  tasks: 3
  files: 2
---

# Quick Task 260725-gp6: Fix Two Pre-existing Darwin-only Lint Findings Summary

Cleared the two macOS-only `golangci-lint` findings in `cmd/otto-tray` — a genuine unwrapped `pgrep` error and a per-GOOS `unparam` false positive — so a local lint run on a mac is a trustworthy signal again.

## What Was Built

**Task 1 — wrapcheck fix (`cmd/otto-tray/desktop_darwin.go`)**
`platformDesktopRunning`'s residual error return changed from bare `return false, err` to `return false, fmt.Errorf("pgrep: %w", err)`, with `"fmt"` added to the single stdlib import group. Both behavioral paths above it are byte-unchanged:

- pgrep exit 0 → `(true, nil)`
- pgrep exit 1 (no match) → `(false, nil)`, because the `errors.As(err, &exitErr) && exitErr.ExitCode() == 1` check at line 26 still evaluates the **raw** error, before the wrap at line 29
- any other exit / spawn failure → `(false, "pgrep: <cause>")`

`%w` preserves the chain, so `runningDesktopCandidate`'s `fmt.Errorf("%w: %w", errDesktopRevalidation, err)` and the `errors.Is(err, errDesktopRevalidation)` dispatch in `handleOpenDesktopFolder` behave identically. The only user-visible effect is a `pgrep: ` prefix in notification bodies and `desktopOutput.Detail` — the intended operator context.

**Task 2 — unparam justification (`cmd/otto-tray/openfolder.go`)**
`runOpenDesktopFolder` gained a doc comment (it had none) recording the three facts a future reader needs: `goos` drives the windows branches of `appFolderTarget` and `resolveHermesHome`; the sole runtime caller passes `runtime.GOOS`, a per-build constant, which is why the analyzer sees one value under a darwin build; and it is the platform injection seam `openfolder_test.go` uses. Below it, a blank `//` separator and the block directive `//nolint:unparam // goos varies by build arm — see doc comment`.

The block form (directive on its own line above the declaration) suppressed the finding on the first attempt — neither escalation placement from the plan was needed. The directive names a single linter, never `all`, so `gosec`/`errcheck`/the rest stay active on that function. The parameter, its position, and every call site are unchanged; no test file was touched.

**Task 3 — both-arm gate sweep.** Verification only, no code changes, no commit.

## Verification Results

Baseline before the change was exactly the 2 issues the plan predicted:

```
cmd/otto-tray/openfolder.go:120:2: runOpenDesktopFolder - goos always receives "darwin" (unparam)
cmd/otto-tray/desktop_darwin.go:28:16: error returned from external package is unwrapped: sig: func (*os/exec.Cmd).Run() error (wrapcheck)
2 issues:
```

After both fixes, `golangci-lint` v2.12.2 `run ./cmd/otto-tray/...` reports **`0 issues.`** The cache was cleaned before the baseline run, so no stale worktree paths were involved in either result.

| Gate | Arm | Result |
|------|-----|--------|
| `gofumpt -l` on the two files | — | no output (clean) |
| `go build ./...` | darwin | pass |
| `go vet ./cmd/otto-tray/...` | darwin | pass |
| `go test ./cmd/otto-tray/...` | darwin | `ok otto-gateway/cmd/otto-tray` |
| `GOOS=windows go build ./...` | windows | pass |
| `GOOS=windows go vet ./cmd/otto-tray/...` | windows | pass |
| `GOOS=windows go test -c -o /dev/null ./cmd/otto-tray/` | windows | pass |
| `golangci-lint@v2.12.2 run ./cmd/otto-tray/...` | darwin | **0 issues** |
| diff scope | — | exactly the 2 intended files |

`git diff --name-only HEAD~2 HEAD` lists exactly `cmd/otto-tray/desktop_darwin.go` and `cmd/otto-tray/openfolder.go` — nothing under `.github/`, `scripts/`, no test files, no other package.

### Lint scope caveat

This worktree is based on `695462c`, not current main (`f0eff5a`). The lint therefore covered a slightly older `cmd/otto-tray` — `uihelpers_test.go` and the `firstMeaningfulLine` helper do not exist in this tree. The `0 issues` result is authoritative for **these two findings**, since both live in files main did not touch, but it does not prove main as a whole is lint-clean. The orchestrator re-runs the full gate set on main after merge.

## Deviations from Plan

None — plan executed exactly as written. No deviation rules fired.

## Known Stubs

None.

## Remaining Follow-ups

1. **`GOOS=darwin` lint job in CI.** Explicitly out of scope here (the plan forbids it). CI lints on `ubuntu-latest`, where `desktop_darwin.go` is excluded by build tag and `goos` is not constant across the linux-visible call graph — which is exactly why both of these findings were invisible to CI and only surfaced on a dev's mac. Without such a job, the darwin arm can drift again.

2. **Unwrapped `golang.org/x/sys/windows` errors in `desktop_windows.go`.** `queryWindowsProcessPath` (lines 66-79) returns bare errors from `windows.OpenProcess` (line 69) and `windows.QueryFullProcessImageName` (line 76). A `GOOS=windows` lint job would very likely flag both as wrapcheck findings. Wrapping them needs care: `windowsProcessGone` does `errors.Is(err, windows.ERROR_INVALID_PARAMETER)` on that error and deliberately fails closed on everything else, so any wrap must use `%w` and must not disturb the `ERROR_INVALID_PARAMETER` discrimination that distinguishes "process disappeared" from `ERROR_ACCESS_DENIED`.

## Self-Check: PASSED

- `cmd/otto-tray/desktop_darwin.go` — FOUND
- `cmd/otto-tray/openfolder.go` — FOUND
- commit `dd871d0` — FOUND
- commit `28bf084` — FOUND
