---
phase: quick-260523-gna
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - DEVELOPERS.md
  - scripts/setup-dev.sh
  - scripts/setup-dev.ps1
autonomous: true
requirements:
  - QUICK-260523-GNA-01
must_haves:
  truths:
    - "A new developer on macOS can read DEVELOPERS.md and reach a working `make build && make test && make lint` state by following either the manual or scripted path."
    - "A new developer on Windows can read DEVELOPERS.md and reach a working `make build && make test && make lint` state by following either the manual or scripted path."
    - "Running scripts/setup-dev.sh twice in a row on macOS does not error and does not reinstall already-installed tools."
    - "Running scripts/setup-dev.ps1 twice in a row on Windows does not error and does not reinstall already-installed tools."
    - "DEVELOPERS.md explicitly tells the reader that the `go mod tidy` pre-commit hook fails on a fresh clone until the first dependency is added, so they don't file it as a bug."
    - "DEVELOPERS.md points the reader at the existing Makefile targets rather than restating them, and does not invent new tool versions outside the pins observed in the repo."
  artifacts:
    - path: "DEVELOPERS.md"
      provides: "Step-by-step dev environment setup for macOS + Windows, both manual and scripted paths, including known gotchas."
      contains: "macOS, Windows, setup-dev.sh, setup-dev.ps1, pre-commit, go mod tidy"
    - path: "scripts/setup-dev.sh"
      provides: "Idempotent macOS bootstrap (Homebrew-based) that installs Go 1.23+, golangci-lint, pre-commit, gosec, gofumpt, gitleaks and prints final versions."
      contains: "#!/usr/bin/env bash, brew install, command -v"
    - path: "scripts/setup-dev.ps1"
      provides: "Idempotent Windows bootstrap (winget-first, scoop fallback) that installs the same toolchain and prints final versions."
      contains: "winget, scoop, Get-Command"
  key_links:
    - from: "DEVELOPERS.md"
      to: "scripts/setup-dev.sh"
      via: "documented relative path reference (`./scripts/setup-dev.sh`)"
      pattern: "scripts/setup-dev\\.sh"
    - from: "DEVELOPERS.md"
      to: "scripts/setup-dev.ps1"
      via: "documented relative path reference (`./scripts/setup-dev.ps1`)"
      pattern: "scripts/setup-dev\\.ps1"
    - from: "DEVELOPERS.md"
      to: "Makefile"
      via: "documented `make help` / `make build` / `make test` / `make lint` / `make cross` references"
      pattern: "make (help|build|test|lint|cross|fmt|tidy)"
---

<objective>
Create a single source of truth for "how to set up the loop24-gateway dev environment" that covers both macOS and Windows, both a step-by-step manual path and an automated scripted path. The scripts must be idempotent (re-running them is safe) and must print the final installed tool versions on success.

Purpose: New contributors (including the author working on a fresh machine) should reach `make build && make test && make lint` green within a single document, without spelunking through `docs/briefs/go_port_brief.md` §3.12 to figure out the toolchain. This unblocks Phase 1 onboarding without modifying any production config.

Output:
- `DEVELOPERS.md` at repo root (manual + scripted paths for macOS + Windows, plus known gotchas).
- `scripts/setup-dev.sh` (executable bash, macOS, Homebrew, idempotent).
- `scripts/setup-dev.ps1` (PowerShell, Windows, winget-first with scoop fallback, idempotent).
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@CLAUDE.md
@README.md
@Makefile
@.pre-commit-config.yaml
@.golangci.yml
@go.mod

<observed_toolchain_pins>
These are the EXACT versions observed in the repo. Do NOT invent or upgrade.

- Go: `1.23` (from `go.mod` line `go 1.23`; CLAUDE.md says "Go 1.23+ required for log/slog ergonomics and post-1.22 net/http routing")
- golangci-lint pre-commit hook: `v1.62.2` (from `.pre-commit-config.yaml`)
- pre-commit-hooks: `v4.6.0` (from `.pre-commit-config.yaml`)
- gitleaks pre-commit: `v8.18.4` (from `.pre-commit-config.yaml`)
- pre-commit (the runner): any 3.x+ (latest from brew/winget is fine)
- gosec: required by `.golangci.yml` (linter is enabled there); install standalone so it can also be invoked directly
- gofumpt: referenced by `Makefile` `fmt` target ("gofumpt preferred; falls back to gofmt") and `docs/briefs/go_port_brief.md` §3.12 item 1
- README "Prerequisites" already lists: Go 1.23+, golangci-lint 1.62+, gofumpt (optional), pre-commit (optional). DEVELOPERS.md is the expanded version of that list — keep alignment.
</observed_toolchain_pins>

<makefile_targets_to_reference>
Do NOT duplicate these in DEVELOPERS.md. Point readers at `make help` and reference by name:

- `make help` — discovery
- `make build` — host build (`bin/loop24-gateway`)
- `make run` — `go run ./cmd/loop24-gateway`
- `make test` — `go test ./...`
- `make test-race` — race detector enabled (matches CI default)
- `make lint` — `golangci-lint run ./...`
- `make fmt` — gofumpt if present, else gofmt
- `make tidy` — `go mod tidy`
- `make cross` — cross-compiles linux/amd64 + windows/amd64 (the headline reason Go was chosen, per CLAUDE.md and brief §2/§3.9)
- `make clean` — remove `bin/`
</makefile_targets_to_reference>

<known_gotchas_to_document>
These have been observed by the orchestrator and MUST be surfaced in DEVELOPERS.md's "Known issues" / "Gotchas" section so new devs aren't confused:

1. **`go mod tidy` pre-commit hook fails on a fresh clone.** The hook (defined in `.pre-commit-config.yaml` as a `local` repo entry) runs `go mod tidy && git diff --exit-code go.mod go.sum`. On a fresh clone there is no `go.sum` yet — the hook will fail until the first dependency is added. This is expected. Workaround: skip the hook on the initial scaffold commit (`SKIP=go-mod-tidy git commit ...`) OR add the first dep before running `pre-commit run --all-files`.

2. **brew vs miniconda PATH ordering for `pre-commit`.** If the user has miniconda's `pre-commit` ahead of brew's on PATH, `pre-commit install` may write a hook that points at the conda version. Surface as a note: run `which pre-commit` to confirm which one is active; both work, but mixing versions across machines causes confusion. (No fix required, just visibility.)

3. **gosec is enabled via golangci-lint AND can be invoked standalone.** Installing the standalone `gosec` binary is recommended so contributors can run `gosec ./...` directly when investigating G204-class findings (subprocess spawn is the highest-risk surface, per CLAUDE.md).

4. **`pre-commit install` is opt-in.** The setup scripts MUST NOT silently run `pre-commit install` for the user. Print the command + a one-line explanation of what it does, and let the user run it themselves. (Constraint from the planner brief: "Scripts should NOT auto-run `pre-commit install` without telling the user — print the command and explain what it does.")
</known_gotchas_to_document>

<windows_package_manager_preference>
- Primary: **winget** (ships with Windows 10/11; no extra install needed).
- Fallback: **scoop** (`https://scoop.sh`) — preferred over chocolatey because it doesn't require admin elevation per install.
- Second fallback: **chocolatey** — mention as acceptable, do not script.

Detection order in `scripts/setup-dev.ps1`:
1. Probe `Get-Command winget -ErrorAction SilentlyContinue` → if found, use winget for all tools.
2. Else probe `Get-Command scoop -ErrorAction SilentlyContinue` → if found, use scoop.
3. Else print clear error: "Install winget or scoop and re-run. See DEVELOPERS.md."

Tool → winget package mapping (use these names verbatim — they are the current canonical IDs at the time of writing; the script should NOT pin versions, just install latest of these IDs):
- Go: `GoLang.Go`
- golangci-lint: `golangci-lint.golangci-lint` (winget) / `main/golangci-lint` (scoop)
- pre-commit: install via `pip install --user pre-commit` (Python ships in winget as `Python.Python.3.12` if needed); winget itself does not have a first-class pre-commit package
- gosec: scoop only via `main/gosec` (no winget package); fallback to `go install github.com/securego/gosec/v2/cmd/gosec@latest` if scoop absent
- gofumpt: `go install mvdan.cc/gofumpt@latest` (works on all platforms; tool itself is a Go binary)
- gitleaks: `gitleaks.gitleaks` (winget) / `main/gitleaks` (scoop)

For tools without a winget package, the script should fall back to `go install …@latest` (assumes Go is already installed earlier in the same script run) — this is consistent with the no-cgo, Go-toolchain-as-installer philosophy of the project.
</windows_package_manager_preference>

<macos_package_strategy>
All tools available via Homebrew. The brief command in the planner background (`brew install go golangci-lint pre-commit gosec gofumpt gitleaks`) is validated. Script uses this exact set, with `command -v <tool>` idempotency checks. Print `--version` (or equivalent) for each tool at the end.
</macos_package_strategy>

<idempotency_pattern>
Both scripts MUST follow this shape per tool:

bash (setup-dev.sh):
```
if command -v go >/dev/null 2>&1; then
  echo "[skip] go already installed: $(go version)"
else
  echo "[install] go"
  brew install go
fi
```

PowerShell (setup-dev.ps1):
```
if (Get-Command go -ErrorAction SilentlyContinue) {
  Write-Host "[skip] go already installed: $(go version)"
} else {
  Write-Host "[install] go"
  winget install --id GoLang.Go --silent --accept-package-agreements --accept-source-agreements
}
```

Final block prints versions for all tools so the user sees a clean summary on success.
</idempotency_pattern>
</context>

<tasks>

<task type="auto">
  <name>Task 1: Write DEVELOPERS.md (manual + scripted paths, both platforms, gotchas)</name>
  <files>DEVELOPERS.md</files>
  <action>Create `DEVELOPERS.md` at repo root. Structure (use exactly these top-level section names so the doc is greppable):

1. `# Developer Setup` — one-paragraph intro: who this is for (new contributors / fresh-machine setup for the author), what success looks like (`make build && make test && make lint` all green).

2. `## Required toolchain` — bullet list of the six tools (Go 1.23+, golangci-lint, pre-commit, gosec, gofumpt, gitleaks) with the EXACT pins from the `<observed_toolchain_pins>` block in this plan's context. Include a one-line "why" for each (e.g. "gitleaks: pre-commit secret scanning; pinned to v8.18.4 in `.pre-commit-config.yaml`"). Do NOT invent pins not present in the repo.

3. `## Quick start (scripted)` — two subsections:
   - `### macOS` — three commands: `xcode-select --install` (note: only if missing — Homebrew needs CLT), install Homebrew (link to brew.sh, don't script the curl), then `./scripts/setup-dev.sh`. After the script: `pre-commit install` (with one-line explanation: "wires the git pre-commit hook so lint/secret-scan/format checks run on every commit"). Verify with `make build && make test && make lint`.
   - `### Windows` — `./scripts/setup-dev.ps1` from a non-admin PowerShell. After the script: `pre-commit install`. Verify with `make build && make test && make lint`. Note: requires winget (ships with Win 10/11) or scoop.

4. `## Manual setup (step-by-step)` — two subsections:
   - `### macOS` — numbered list. Each step: the command + what it installs. Include `brew install go golangci-lint pre-commit gosec gofumpt gitleaks` as the canonical one-liner, then `pre-commit install`, then `make build`, then `make test`, then `make lint`.
   - `### Windows` — numbered list, winget-first. Step per tool with both winget and scoop alternatives where applicable. Use the tool→package mapping from `<windows_package_manager_preference>` in this plan's context. For pre-commit, use `pip install --user pre-commit` (requires Python; note winget package `Python.Python.3.12`). For gosec on Windows, prefer scoop or `go install github.com/securego/gosec/v2/cmd/gosec@latest`. For gofumpt, use `go install mvdan.cc/gofumpt@latest`.

5. `## Daily workflow` — short subsection. Reference Makefile targets by name only (do NOT duplicate their commands — Makefile is the source of truth). Tell the reader to run `make help` to see the full list. Highlight the four they will use most: `make run`, `make test`, `make lint`, `make cross`.

6. `## Known issues / gotchas` — numbered list of the four gotchas from `<known_gotchas_to_document>` in this plan's context. Each entry: one-line headline, two-to-four lines of explanation, the workaround if there is one. The `go mod tidy` transient failure is item #1 (most important — it's the first thing a new dev will hit).

7. `## Verifying your setup` — final checklist: `go version` (≥1.23), `golangci-lint --version`, `pre-commit --version`, `gosec --version`, `gofumpt --version`, `gitleaks version`, then `make build && make test && make lint`. State the expected outcome (all commands print versions, make targets exit 0).

Cross-link rules:
- Reference `./scripts/setup-dev.sh` and `./scripts/setup-dev.ps1` by relative path so the must-haves `key_links` greps match.
- Reference `make help`, `make build`, `make test`, `make lint`, `make cross` at least once each.
- Do NOT modify README.md. (Optional one-line addition to README is permitted at executor discretion but not required.)

Tone: terse, scannable, no marketing language. Match the existing project doc style (see CLAUDE.md and README.md — short paragraphs, code fences for commands, no emoji).</action>
  <verify>
    <automated>test -f DEVELOPERS.md && grep -q "scripts/setup-dev.sh" DEVELOPERS.md && grep -q "scripts/setup-dev.ps1" DEVELOPERS.md && grep -q "go mod tidy" DEVELOPERS.md && grep -q "make build" DEVELOPERS.md && grep -q "make test" DEVELOPERS.md && grep -q "make lint" DEVELOPERS.md && grep -q "make cross" DEVELOPERS.md && grep -q "winget\|scoop" DEVELOPERS.md && grep -q "Homebrew\|brew" DEVELOPERS.md && grep -q "v1\.62\.2\|1\.62" DEVELOPERS.md && grep -q "v8\.18\.4\|8\.18" DEVELOPERS.md</automated>
  </verify>
  <done>DEVELOPERS.md exists at repo root with all seven top-level sections (`Developer Setup`, `Required toolchain`, `Quick start (scripted)`, `Manual setup (step-by-step)`, `Daily workflow`, `Known issues / gotchas`, `Verifying your setup`), references both scripts by relative path, references Makefile targets without duplicating their command bodies, documents the four known gotchas (with `go mod tidy` transient failure called out explicitly), and includes the observed toolchain pins (Go 1.23, golangci-lint v1.62.2, gitleaks v8.18.4, pre-commit-hooks v4.6.0) without inventing new versions.</done>
</task>

<task type="auto">
  <name>Task 2: Write scripts/setup-dev.sh (macOS, idempotent, Homebrew)</name>
  <files>scripts/setup-dev.sh</files>
  <action>Create `scripts/setup-dev.sh`. Requirements:

- Shebang: `#!/usr/bin/env bash`.
- Strict mode: `set -euo pipefail`.
- After writing, the executor MUST run `chmod +x scripts/setup-dev.sh` so the file is executable.

Behavior, in order:

1. Print a banner: "Loop24 Gateway — macOS dev environment bootstrap". Note: idempotent — safe to re-run.

2. Pre-flight: confirm we are on macOS (`[[ "$(uname -s)" == "Darwin" ]]` — error out otherwise with a pointer to `scripts/setup-dev.ps1` for Windows).

3. Pre-flight: confirm Homebrew is installed (`command -v brew`). If missing, print install instructions (link to `https://brew.sh`) and exit 1. Do NOT auto-install Homebrew (it requires sudo and a curl-pipe-bash that the user should consent to explicitly).

4. For each tool in this exact order — `go`, `golangci-lint`, `pre-commit`, `gosec`, `gofumpt`, `gitleaks` — implement the idempotency pattern from `<idempotency_pattern>` in this plan's context:
   - If `command -v <tool>` succeeds, print `[skip] <tool> already installed: $(<tool> --version-or-equivalent)` and continue.
   - Else print `[install] <tool>` and run `brew install <tool>`.

   Per-tool version probe commands (use these — they vary by tool):
   - go: `go version`
   - golangci-lint: `golangci-lint --version`
   - pre-commit: `pre-commit --version`
   - gosec: `gosec --version 2>&1 | head -1` (gosec prints to stderr on some versions)
   - gofumpt: `gofumpt --version`
   - gitleaks: `gitleaks version`

5. After all tools, print a `==== Installed versions ====` summary block invoking each version probe one more time. This is the "prints final tool versions on success" requirement.

6. Print a final next-steps block (NOT auto-run — the constraint says scripts MUST NOT silently `pre-commit install`):
   - `pre-commit install` — wires the git pre-commit hook so lint/secret-scan/format checks run on every commit.
   - `make help` — list available make targets.
   - `make build && make test && make lint` — verify the toolchain.

   Also mention: "On a fresh clone, the `go mod tidy` pre-commit hook will fail until the first dependency is added — see DEVELOPERS.md → Known issues."

Do NOT use `sudo` anywhere. Do NOT modify shell profiles (`.zshrc`, `.bash_profile`). Do NOT install xcode-select Command Line Tools — Homebrew handles that prompt itself; just note it in DEVELOPERS.md.

The script must be readable on a single screen — keep it terse. No fancy progress bars, no color codes (terminals vary), just `[skip]` / `[install]` prefixes.</action>
  <verify>
    <automated>test -x scripts/setup-dev.sh && bash -n scripts/setup-dev.sh && head -1 scripts/setup-dev.sh | grep -q "^#!/usr/bin/env bash" && grep -q "set -euo pipefail" scripts/setup-dev.sh && grep -q 'uname -s' scripts/setup-dev.sh && grep -q "command -v brew" scripts/setup-dev.sh && grep -cE "command -v (go|golangci-lint|pre-commit|gosec|gofumpt|gitleaks)" scripts/setup-dev.sh | awk '{ if ($1 < 6) exit 1 }' && grep -q "brew install go" scripts/setup-dev.sh && grep -q "brew install golangci-lint" scripts/setup-dev.sh && grep -q "brew install pre-commit" scripts/setup-dev.sh && grep -q "brew install gosec" scripts/setup-dev.sh && grep -q "brew install gofumpt" scripts/setup-dev.sh && grep -q "brew install gitleaks" scripts/setup-dev.sh && grep -q "pre-commit install" scripts/setup-dev.sh && ! grep -q "^sudo " scripts/setup-dev.sh</automated>
  </verify>
  <done>`scripts/setup-dev.sh` exists, is executable (chmod +x applied), passes `bash -n` syntax check, contains all six per-tool idempotency blocks (one `command -v` check per required tool), uses `brew install` for each install (no sudo), prints a versions summary at the end, prints `pre-commit install` as a suggested-next-step (not auto-run), and references the `go mod tidy` transient failure gotcha in its next-steps output.</done>
</task>

<task type="auto">
  <name>Task 3: Write scripts/setup-dev.ps1 (Windows, idempotent, winget-first / scoop fallback)</name>
  <files>scripts/setup-dev.ps1</files>
  <action>Create `scripts/setup-dev.ps1`. Requirements:

- PowerShell 5.1+ compatible (ships with Windows 10/11 — do not require PowerShell 7).
- Top-of-file: `#Requires -Version 5.1` and `Set-StrictMode -Version Latest` and `$ErrorActionPreference = 'Stop'`.
- Do NOT require admin elevation. winget and scoop both install per-user.

Behavior, in order:

1. Print a banner: "Loop24 Gateway — Windows dev environment bootstrap". Note: idempotent — safe to re-run.

2. Pre-flight: detect package manager. Probe in order:
   - `Get-Command winget -ErrorAction SilentlyContinue` → `$pm = 'winget'`
   - Else `Get-Command scoop -ErrorAction SilentlyContinue` → `$pm = 'scoop'`
   - Else: write a clear error pointing at `https://scoop.sh` and DEVELOPERS.md, exit 1.

3. Define a helper function `Install-Tool` that takes: tool command name (for `Get-Command` probe), winget package ID, scoop package name, version probe scriptblock, and an optional `go-install-fallback` URL (e.g. `mvdan.cc/gofumpt@latest`). Behavior:
   - If `Get-Command <toolName> -ErrorAction SilentlyContinue` returns non-null, print `[skip] <tool> already installed: <version probe output>` and return.
   - Else print `[install] <tool>` and:
     - If `$pm -eq 'winget'` and the winget package ID is non-empty: `winget install --id <id> --silent --accept-package-agreements --accept-source-agreements`.
     - Elseif `$pm -eq 'scoop'` and the scoop package name is non-empty: `scoop install <name>`.
     - Elseif the go-install fallback is provided: `go install <url>` (this assumes Go is already installed earlier in the run — the calling order matters).
     - Else: write an error pointing at DEVELOPERS.md → Manual setup.

4. Call `Install-Tool` for each tool in this exact order (order matters because gosec/gofumpt may fall back to `go install`, which requires Go):
   - `go` — winget `GoLang.Go`, scoop `main/go`, version probe `go version`.
   - `golangci-lint` — winget `golangci-lint.golangci-lint`, scoop `main/golangci-lint`, version probe `golangci-lint --version`.
   - `pre-commit` — neither winget nor scoop has a first-class package. Use `pip install --user pre-commit` as the install path. If `pip` is missing, print an error pointing at `Python.Python.3.12` (`winget install --id Python.Python.3.12 --silent`) and DEVELOPERS.md. Version probe `pre-commit --version`.
   - `gosec` — no winget package; scoop `main/gosec`; go-install fallback `github.com/securego/gosec/v2/cmd/gosec@latest`. Version probe `gosec --version`.
   - `gofumpt` — no winget package; scoop `main/gofumpt`; go-install fallback `mvdan.cc/gofumpt@latest`. Version probe `gofumpt --version`.
   - `gitleaks` — winget `gitleaks.gitleaks`, scoop `main/gitleaks`, version probe `gitleaks version`.

5. After all tools, print a `==== Installed versions ====` summary block invoking each version probe one more time.

6. Print a final next-steps block (NOT auto-run):
   - `pre-commit install` — wires the git pre-commit hook so lint/secret-scan/format checks run on every commit.
   - `make help` — list available make targets (note: `make` is available via `winget install ezwinports.make` or scoop `main/make` if not already present — call this out one-line).
   - `make build; make test; make lint` — verify the toolchain (semicolons, not `&&`, in PowerShell).

   Also mention: "On a fresh clone, the `go mod tidy` pre-commit hook will fail until the first dependency is added — see DEVELOPERS.md → Known issues."

Do NOT modify the user's PATH or `$PROFILE`. Do NOT call `Set-ExecutionPolicy`. If the user can't run the script because of execution policy, that's a one-line note in DEVELOPERS.md (run with `powershell -ExecutionPolicy Bypass -File .\scripts\setup-dev.ps1`).</action>
  <verify>
    <automated>test -f scripts/setup-dev.ps1 && grep -q "#Requires -Version 5.1" scripts/setup-dev.ps1 && grep -q "Set-StrictMode -Version Latest" scripts/setup-dev.ps1 && grep -q "ErrorActionPreference = 'Stop'" scripts/setup-dev.ps1 && grep -q "Get-Command winget" scripts/setup-dev.ps1 && grep -q "Get-Command scoop" scripts/setup-dev.ps1 && grep -q "GoLang.Go" scripts/setup-dev.ps1 && grep -q "golangci-lint.golangci-lint" scripts/setup-dev.ps1 && grep -q "gitleaks.gitleaks" scripts/setup-dev.ps1 && grep -q "pip install --user pre-commit" scripts/setup-dev.ps1 && grep -q "mvdan.cc/gofumpt" scripts/setup-dev.ps1 && grep -q "github.com/securego/gosec" scripts/setup-dev.ps1 && grep -q "pre-commit install" scripts/setup-dev.ps1 && grep -q "go mod tidy" scripts/setup-dev.ps1 && ! grep -q "Set-ExecutionPolicy" scripts/setup-dev.ps1</automated>
  </verify>
  <done>`scripts/setup-dev.ps1` exists, declares `#Requires -Version 5.1` + `Set-StrictMode -Version Latest` + `$ErrorActionPreference = 'Stop'`, detects winget-then-scoop in that order, contains install logic for all six required tools (with go-install fallbacks for gosec + gofumpt), uses the documented winget package IDs (`GoLang.Go`, `golangci-lint.golangci-lint`, `gitleaks.gitleaks`), handles pre-commit via `pip install --user`, prints a versions summary at the end, prints `pre-commit install` as a suggested-next-step (not auto-run), references the `go mod tidy` transient failure gotcha, and does not call `Set-ExecutionPolicy` or require admin.</done>
</task>

</tasks>

<verification>
After all three tasks complete:

1. **Files exist and are well-formed:**
   - `test -f DEVELOPERS.md`
   - `test -x scripts/setup-dev.sh && bash -n scripts/setup-dev.sh`
   - `test -f scripts/setup-dev.ps1` (no syntax-check tool required; if `pwsh` is available, run `pwsh -NoProfile -Command "& { Get-Content scripts/setup-dev.ps1 | Out-Null }"` as a smoke check)

2. **Cross-links resolve:**
   - `grep -q "scripts/setup-dev.sh" DEVELOPERS.md`
   - `grep -q "scripts/setup-dev.ps1" DEVELOPERS.md`

3. **Makefile not duplicated, just referenced:**
   - `grep -E "make (build|test|lint|cross|help)" DEVELOPERS.md` returns at least 5 matches.
   - DEVELOPERS.md does NOT inline the body of any Makefile target (no `go test ./...` or `golangci-lint run ./...` lines outside of "the equivalent of `make test` is …" context).

4. **Toolchain pins not invented:**
   - Versions mentioned in DEVELOPERS.md must be a subset of: `1.23` (Go), `v1.62.2` (golangci-lint pre-commit), `v8.18.4` (gitleaks pre-commit), `v4.6.0` (pre-commit-hooks). No fabricated versions for gosec / gofumpt / pre-commit itself (those are "latest").

5. **Idempotency demonstrated by inspection:**
   - `grep -c "command -v" scripts/setup-dev.sh` ≥ 6 (one probe per tool).
   - `grep -c "Get-Command " scripts/setup-dev.ps1` ≥ 6.

6. **No README.md, Makefile, .pre-commit-config.yaml, .golangci.yml, or go.mod modifications:**
   - `git status --porcelain | grep -v -E "^.. (DEVELOPERS\.md|scripts/setup-dev\.(sh|ps1))$"` returns nothing (the only changed files are the three created by this plan).
</verification>

<success_criteria>
The plan succeeds when:

- A fresh-machine macOS user can run `./scripts/setup-dev.sh && pre-commit install && make build && make test && make lint` and reach green.
- A fresh-machine Windows user can run `./scripts/setup-dev.ps1`, then `pre-commit install`, then `make build; make test; make lint` and reach green.
- Re-running either script is a no-op (prints `[skip]` for every tool, exits 0).
- DEVELOPERS.md explicitly warns about the `go mod tidy` pre-commit transient failure so it doesn't get filed as a bug.
- No production config is touched: Makefile, README.md, go.mod, .pre-commit-config.yaml, .golangci.yml are unchanged.
- All toolchain version pins in DEVELOPERS.md trace back to the actual repo (`.pre-commit-config.yaml`, `go.mod`) — none invented.
</success_criteria>

<output>
Create `.planning/quick/260523-gna-create-developers-md-with-step-by-step-d/260523-gna-SUMMARY.md` when done, capturing:
- Final file list with line counts.
- The exact toolchain pins documented (and where each was sourced).
- A note on which gotchas made it into DEVELOPERS.md.
- Any executor-discretion decisions (e.g. whether the optional one-line README pointer was added).
</output>
