# Developer Setup

This document is the single source of truth for getting a working
`otto-gateway` dev environment on macOS or Windows. It covers a
manual step-by-step path and an automated scripted path. Success
looks like `make build && make test && make lint` all exiting 0 on a
fresh clone.

Audience: new contributors, or the author setting up a fresh machine.

## Required toolchain

The six tools below are required. Versions in parentheses are the
exact pins observed in this repo — they are sourced from
`.pre-commit-config.yaml` and `go.mod`. Do not invent newer
versions; CI uses the pinned set.

- **Go 1.23+** — language toolchain (`go.mod` declares `go 1.23`).
  Required for `log/slog` ergonomics and post-1.22 `net/http`
  routing patterns. No cgo in the main binary so cross-compile
  stays trivial.
- **golangci-lint v1.62.2** — meta-linter; pinned in
  `.pre-commit-config.yaml`. Configured by `.golangci.yml`
  (enables `gosec`, `errcheck`, `staticcheck`, `wrapcheck`, etc.).
- **pre-commit** (any 3.x) — runs the hook set defined in
  `.pre-commit-config.yaml` on every `git commit`. Installed via
  Homebrew on macOS or `pip install --user pre-commit` on
  Windows.
- **gosec** (latest) — security linter; required by
  `.golangci.yml` and useful standalone for investigating
  G204-class findings (subprocess spawn is the highest-risk
  surface in this codebase).
- **gofumpt** (latest) — stricter formatter; `make fmt` prefers
  it and falls back to `gofmt` if absent.
- **gitleaks v8.18.4** — pre-commit secret scanning; pinned in
  `.pre-commit-config.yaml`.
- **shellcheck** (latest) — lints `scripts/setup-dev.sh` via the
  `jumanjihouse/pre-commit-hooks v3.0.0` wrapper pinned in
  `.pre-commit-config.yaml`. The hook is a thin wrapper around
  the system `shellcheck` binary, so it must be installed
  separately.

Two further pins live in `.pre-commit-config.yaml` that you do not
install directly but should be aware of:

- `pre-commit-hooks v4.6.0` — the basic hook set
  (`end-of-file-fixer`, `trailing-whitespace`, `check-yaml`, …).
- The `go-mod-tidy` local hook — runs `go mod tidy && git diff
  --exit-code go.mod go.sum`. See **Known issues** below; this
  one trips up fresh clones.

## Quick start (scripted)

### macOS

```bash
# 1. Install Xcode Command Line Tools (only if missing — Homebrew needs them).
xcode-select --install

# 2. Install Homebrew if not already installed. See https://brew.sh — we don't
#    script this because it requires sudo and a curl-pipe-bash that you should
#    consent to explicitly.

# 3. Run the idempotent bootstrap (safe to re-run).
./scripts/setup-dev.sh

# 4. Wire git hooks (opt-in; the script does not do this for you).
#    This installs the pre-commit hook so lint/secret-scan/format checks run
#    on every commit.
pre-commit install

# 5. Verify.
make build && make test && make lint
```

### Windows

```powershell
# 1. Run the idempotent bootstrap from a non-admin PowerShell (safe to re-run).
#    Requires winget (ships with Windows 10/11) or scoop (https://scoop.sh).
./scripts/setup-dev.ps1

# 2. Wire git hooks (opt-in; the script does not do this for you).
pre-commit install

# 3. Verify (PowerShell uses ; not && for command sequencing).
make build; make test; make lint
```

If PowerShell blocks the script due to execution policy, run it via
`powershell -ExecutionPolicy Bypass -File .\scripts\setup-dev.ps1`.
The script itself does not call `Set-ExecutionPolicy`.

## Manual setup (step-by-step)

### macOS

Requires Homebrew (https://brew.sh) and Xcode Command Line Tools.

1. Install the toolchain.

   ```bash
   brew install go golangci-lint pre-commit gosec gofumpt gitleaks shellcheck
   ```

2. Wire the git pre-commit hook.

   ```bash
   pre-commit install
   ```

3. Build, test, lint.

   ```bash
   make build
   make test
   make lint
   ```

### Windows

Requires winget (ships with Windows 10/11) or scoop
(https://scoop.sh). Chocolatey works too but is not documented
here. Run from a non-admin PowerShell.

1. Install Go.

   ```powershell
   winget install --id GoLang.Go --silent --accept-package-agreements --accept-source-agreements
   # or: scoop install main/go
   ```

2. Install golangci-lint.

   ```powershell
   winget install --id golangci-lint.golangci-lint --silent --accept-package-agreements --accept-source-agreements
   # or: scoop install main/golangci-lint
   ```

3. Install pre-commit. winget does not ship a first-class
   pre-commit package, so install via pip. If pip is missing,
   install Python first:

   ```powershell
   winget install --id Python.Python.3.12 --silent --accept-package-agreements --accept-source-agreements
   pip install --user pre-commit
   ```

4. Install gosec. No winget package; prefer scoop, otherwise use
   the Go toolchain (Step 1 must have run already).

   ```powershell
   scoop install main/gosec
   # or: go install github.com/securego/gosec/v2/cmd/gosec@latest
   ```

5. Install gofumpt. No winget package; same pattern.

   ```powershell
   scoop install main/gofumpt
   # or: go install mvdan.cc/gofumpt@latest
   ```

6. Install gitleaks.

   ```powershell
   winget install --id gitleaks.gitleaks --silent --accept-package-agreements --accept-source-agreements
   # or: scoop install main/gitleaks
   ```

7. Install shellcheck.

   ```powershell
   winget install --id koalaman.shellcheck --silent --accept-package-agreements --accept-source-agreements
   # or: scoop install main/shellcheck
   ```

8. Wire the git pre-commit hook.

   ```powershell
   pre-commit install
   ```

9. Build, test, lint. `make` is not installed by default on
   Windows; get it via `winget install ezwinports.make` or
   `scoop install main/make`.

   ```powershell
   make build
   make test
   make lint
   ```

## Daily workflow

All routine commands go through the Makefile. Run `make help` to
see the full list with descriptions; the targets you will use
most are:

- `make run` — run the gateway locally.
- `make test` — unit + integration tests.
- `make lint` — golangci-lint.
- `make cross` — cross-compile Linux + Windows binaries (the
  headline reason Go was chosen).

`make fmt`, `make tidy`, `make test-race`, `make build`, and
`make clean` round out the set.

## End-to-end (E2E) testing

The `tests/e2e/` suite boots the **real `otto-gateway` binary** against
**real `kiro-cli`** and drives it over HTTP — it automates the Phase 3.1
acceptance checks (health, dual-auth, streaming SSE framing, surface
gating, and the `@anthropic-ai/sdk` round-trip) so you do not have to
`curl` by hand. It is **opt-in**: behind a `//go:build e2e` tag + an
`GW_E2E=1` gate, so `make test` / `make ci` never run it and never need
`kiro-cli` or Node.

```bash
# Steps 1-3 + 6 (real binary + kiro):
make build && GW_E2E=1 make e2e

# Add steps 4-5 (real @anthropic-ai/sdk parser):
make e2e-sdk-setup      # one-time: installs the Node harness
GW_E2E=1 make e2e

# Run a subset (scopes the run + report); discover groups with e2e-list:
make e2e-list
make e2e RUN=TestE2E_Ollama        # e.g. just the Ollama/LangFlow contract
```

Each run writes a markdown report to `tests/e2e/reports/LATEST.md`
(timestamp, version, pass/fail/skip table). Prerequisites: `kiro-cli`
on `PATH` and authenticated (tests **skip** — not fail — if warmup
fails); Node only for steps 4-5. Full reference: **`tests/e2e/README.md`**.

## Known issues / gotchas

1. **`go mod tidy` pre-commit hook fails on a fresh clone.** The
   local hook in `.pre-commit-config.yaml` runs `go mod tidy &&
   git diff --exit-code go.mod go.sum`. On a fresh clone there is
   no `go.sum` yet, so `go mod tidy` creates one and the diff
   check fails. This is expected, not a bug — do not file an
   issue.

   Workaround: skip the hook on the initial scaffold commit
   (`SKIP=go-mod-tidy git commit ...`) or add the first
   dependency before running `pre-commit run --all-files`.

2. **brew vs miniconda PATH ordering for `pre-commit`.** If you
   have miniconda's `pre-commit` ahead of Homebrew's on PATH,
   `pre-commit install` will write a hook that points at the
   conda version. Both work, but mixing versions across machines
   causes confusion. Confirm which is active with `which
   pre-commit`. No fix required — just be aware.

3. **gosec is enabled via golangci-lint AND can be invoked
   standalone.** Installing the standalone `gosec` binary is
   recommended so you can run `gosec ./...` directly when
   investigating G204-class findings. Subprocess spawn is the
   highest-risk surface in this codebase, so this comes up.

4. **`pre-commit install` is opt-in.** Neither `setup-dev.sh`
   nor `setup-dev.ps1` runs `pre-commit install` for you — they
   print it as a suggested next step. Run it yourself once per
   clone to wire the git hook.

## Verifying your setup

After running the setup script (or completing the manual steps),
confirm each tool is on PATH and reports a version:

```bash
go version            # expect: go1.23 or newer
golangci-lint --version
pre-commit --version
gosec --version
gofumpt --version
gitleaks version
shellcheck --version
```

Then verify the project builds, tests, and lints cleanly:

```bash
make build && make test && make lint
```

All six version commands should print without error, and the
three `make` targets should exit 0. If any of them fail, recheck
**Known issues / gotchas** above — the `go mod tidy` transient
failure is the most common.
