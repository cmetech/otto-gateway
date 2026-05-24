---
phase: quick-260524-ldx
plan: 01
subsystem: config
tags: [cli, flags, config, env-parity]
requires:
  - internal/config/config.go Load()
  - internal/version/version.go Version
provides:
  - config.LoadArgs(args []string) (Config, error)
  - config.ErrVersionRequested sentinel
affects:
  - cmd/loop24-gateway/main.go (now calls LoadArgs)
tech-stack:
  added: []
  patterns:
    - "flag-wins-over-env via flag.FlagSet.Visit (overlay only explicitly-passed flags)"
    - "stdlib flag only — no new dependencies (go.mod/go.sum unchanged)"
    - "secret (AUTH_TOKEN) intentionally not exposed as a CLI flag"
key-files:
  created:
    - internal/config/loadargs_test.go
  modified:
    - internal/config/config.go
    - cmd/loop24-gateway/main.go
decisions:
  - "LoadArgs wraps Load() (env-only path unchanged) then overlays set flags; flag-wins precedence."
  - "AUTH_TOKEN remains env-only — no --auth-token flag (secret must not appear in argv)."
  - "--ping-interval is a time.Duration flag (Go duration strings); integer-ms is an env-only nicety not replicated on the flag."
  - "Wrapped fs.Parse error with %w for wrapcheck; errors.Is(err, flag.ErrHelp) still matches in main."
metrics:
  duration: ~12m
  completed: 2026-05-24
  tasks: 2
  files: 3
---

# Phase quick-260524-ldx Plan 01: CLI Flags Summary

Added `config.LoadArgs(os.Args[1:])` — a stdlib-`flag`-only overlay that resolves env+defaults via the unchanged `config.Load()`, then applies flag-wins-over-env precedence by visiting only the flags the operator explicitly passed; `AUTH_TOKEN` stays env-only and `--version`/`--help` exit 0 via main without the config package ever calling `os.Exit`.

## What Was Built

- **`config.LoadArgs(args)` + `ErrVersionRequested`** in `internal/config/config.go`:
  1. `cfg, err := Load()` (env errors surface first; Load() is byte-for-byte unchanged).
  2. Local `flag.NewFlagSet("loop24-gateway", flag.ContinueOnError)` with `io.Discard` output (no global state, no stderr pollution in tests).
  3. 13 Bucket-A flags registered with defaults seeded from the resolved `cfg` (`--http-addr`, `--kiro-cmd`, `--kiro-args`, `--kiro-cwd`, `--debug`, `--ping-interval`, `--pool-size`, `--enabled-surfaces`, `--ollama-path-prefix`, `--anthropic-path-prefix`, `--openai-path-prefix`, `--allowed-ips`, `--auth-trust-xff`) + `--version` meta flag. **No `--auth-token` flag.**
  4. `fs.Visit` overlays ONLY explicitly-passed flags (flag-wins; unset flags fall through to env). Reuses `parseCIDRs`, `validateEnabledSurfaces`, `strings.Fields`, and a new `splitCommaTrim` helper that mirrors `getEnvStrSliceComma`.
  5. Re-validates overridden values (`enabled-surfaces` allow-list, `allowed-ips` CIDR parse) and returns `fmt.Errorf("config: invalid flags: %w", errors.Join(errs...))` on failure.
- **`cmd/loop24-gateway/main.go`** switched from `config.Load()` to `config.LoadArgs(os.Args[1:])`, with `ErrVersionRequested` → print `version.Version` + exit 0, and `flag.ErrHelp` → exit 0, placed before the existing error→exit(1) path. Added the `flag` import and removed the now-redundant `var _ = errors.Is` keep-alive (errors.Is is used directly).
- **`internal/config/loadargs_test.go`** (package `config_test`): flag-wins per type (string/bool/int/duration/comma-slice/whitespace-slice/validated-comma-slice), fall-through, env-only parity (`LoadArgs(nil)`/`LoadArgs([])` `reflect.DeepEqual` to `Load()` under clean and set env), invalid-value table (bad CIDR, unknown surface naming "olama", bad duration, bad int), `--version` sentinel, and the behavioral no-`--auth-token`-flag assertion.

## Acceptance Gate Results

| Gate | Result |
| ---- | ------ |
| `go test ./internal/config/ -race -count=1` | PASS (ok, 1.198s — new + all pre-existing config tests) |
| `go build ./...` | PASS (clean) |
| `go vet ./...` | PASS (clean) |
| `golangci-lint run ./internal/config/... ./cmd/...` | PASS (0 issues) |
| `grep -c 'auth-token' internal/config/config.go` | 0 (no secret flag / no literal in file) |
| `make build && ./bin/loop24-gateway --version` | PASS — prints version, exit 0 |
| `--help` exit code | 0 |
| unknown flag (`--bogus-flag`) exit code | 1 |
| `go.mod` / `go.sum` changed | No (stdlib `flag` only — zero new deps) |

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] wrapcheck flagged the `flag.ErrHelp` early-return**
- **Found during:** Task 2 (GREEN, golangci-lint gate)
- **Issue:** Returning `fs.Parse`'s error unwrapped (`return cfg, err`) for the `flag.ErrHelp` branch tripped the `wrapcheck` linter.
- **Fix:** Collapsed both parse-error branches into a single `fmt.Errorf("config: %w", err)`. `%w` preserves the sentinel so `errors.Is(err, flag.ErrHelp)` still matches in `main` (--help → exit 0) and `errors.Is(err, config.ErrVersionRequested)` is unaffected (the version flag is checked before `fs.Parse` errors via the bool var).
- **Files modified:** internal/config/config.go
- **Commit:** 96c357d

**2. [Rule 3 - Blocking] grep gate counted `auth-token` inside source comments**
- **Found during:** Task 2 (GREEN, `grep -c 'auth-token'` gate)
- **Issue:** Comments documenting the deliberate absence of the flag contained the literal `auth-token`, so the grep gate returned non-zero.
- **Fix:** Reworded both comments to convey intent (token stays env-only) without using the literal `auth-token` string. Gate now returns 0.
- **Files modified:** internal/config/config.go
- **Commit:** 96c357d

### Process note (not a plan deviation)
- Per the parallel-execution instruction, the TDD RED and GREEN steps were collapsed into the single commit 96c357d because the pre-commit hook rejects a RED-only commit whose tests fail to build (mirrors prior plan 03.1-01). RED was verified locally first (`undefined: config.LoadArgs` → `RED-OK`) before implementing GREEN. The commit message records the TDD intent.

## TDD Gate Compliance

RED was verified locally (tests failed to build with `undefined: config.LoadArgs` / `config.ErrVersionRequested`) prior to implementation. RED+GREEN were committed together (single `feat` commit 96c357d) by design — the pre-commit test hook makes a standalone failing-test commit impossible. No separate `test(...)` gate commit exists for this reason; the RED→GREEN sequence is documented in the commit body.

## Self-Check: PASSED

- FOUND: internal/config/loadargs_test.go
- FOUND: commit 96c357d (feat(quick-260524-ldx): add config.LoadArgs CLI flag overlay)
- internal/config/config.go and cmd/loop24-gateway/main.go modified and committed
