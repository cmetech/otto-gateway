---
phase: quick-260528-rom
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - scripts/setup.bat
  - scripts/otto-gw.bat
  - scripts/start.bat
  - scripts/stop.bat
  - scripts/status.bat
  - Makefile
  - docs/operator-quickstart.md
autonomous: true
---

<objective>
Give Windows operators a frictionless first-run and double-clickable command
shortcuts that don't require PowerShell execution-policy ceremony or per-file
`Unblock-File`. Five new `.bat` files in `scripts/`, plus Makefile staging and
docs update.
</objective>

<context>
Surfaced from the v1.5.0 Windows operator smoke test. After every fresh extract
of the Windows distribution zip:

1. Windows tags each file with a Mark-of-the-Web (MOTW) Zone.Identifier
   alternate data stream marking it as "downloaded from the internet."
2. PowerShell 5.x's default execution policy refuses to run MOTW'd scripts.
3. Operator has to run `Unblock-File .\scripts\otto-gw.ps1` per script before
   any wrapper command works.

This is a real friction tax on every Windows operator's first run. The fix
must come from a sibling artifact PowerShell allows to run unconditionally —
i.e., a `.bat` (cmd.exe), which is exempt from PowerShell execution policy
because cmd is not PowerShell.
</context>

<tasks>

### Task 1 — Write the 5 .bat files

**`scripts/setup.bat`** — One-time post-extract setup (~39 lines).

cmd.exe shells out to `powershell -NoProfile -ExecutionPolicy Bypass` for two
operations:

1. `Get-ChildItem -Path '%~dp0..' -Recurse -File | Unblock-File` — strip MOTW
   from every extracted file in one pass.
2. `Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force`
   — so subsequent runs don't need Bypass.

Friendly "Setup complete. You can now run X." message at the end. `pause` so
double-clickers see output. `:err` branch surfaces the
`-ExecutionPolicy Bypass -File` workaround for Group-Policy-locked
environments where CurrentUser scope is overridden by GPO.

**`scripts/otto-gw.bat`** — Dispatcher mirroring the `.ps1` subcommand surface
(init | start | stop | status | restart | logs | run | env | version).

```bat
@echo off
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0otto-gw.ps1" %*
```

Self-relocating via `%~dp0`. Forwards all args via `%*`. The `-File` flag
(not `-Command`) is required so PowerShell treats `%~dp0otto-gw.ps1` as a
script path and the Bypass policy actually applies to it — even on a fresh
extract before `setup.bat` has run. This makes the dispatcher itself a
recovery path for Group-Policy-locked environments.

**`scripts/start.bat`** — Explorer double-clickable shortcut:
```bat
@call "%~dp0otto-gw.bat" start %*
```

**`scripts/stop.bat`** — same shape for `stop`.

**`scripts/status.bat`** — same shape for `status`.

**Encoding gate (critical):** `.bat` files MUST be ASCII or UTF-8 NO BOM.
cmd.exe parses BOM as the first command, which breaks the script silently.
Different from `.ps1`, which DOES need BOM for Windows PowerShell 5.x. The
executor MUST verify post-write:

```bash
head -c 3 scripts/setup.bat | xxd      # must NOT show 'efbbbf'
file scripts/*.bat                     # must say "ASCII text" or "DOS batch file text"
```

### Task 2 — Update `Makefile` `stage_unix` macro

At ~line 152 (or wherever `stage_unix` defines the `cp` for `scripts/otto-gw`
and `scripts/otto-gw.ps1`), append the 5 new `.bat` files to that copy:

```make
cp scripts/otto-gw scripts/otto-gw.ps1 scripts/.env.otto-gw.example \
   scripts/setup.bat scripts/otto-gw.bat \
   scripts/start.bat scripts/stop.bat scripts/status.bat \
   $(DIST_DIR)/otto_gateway/scripts/
```

Ship in ALL packages (darwin, linux, windows). They're harmless on POSIX —
operators just ignore the `.bat` files. Don't split into a `stage_windows`
variant; that's premature complexity for 5 tiny text files.

### Task 3 — Update `docs/operator-quickstart.md`

Add a Windows-specific quick-start subsection BEFORE the existing "Install
kiro-cli" step (currently step 4 — this becomes step 3b). About 5 lines
telling operators to double-click `scripts\setup.bat` once after extracting,
then mention three usage patterns:

- `.\scripts\otto-gw.ps1 <cmd>` (PowerShell-native)
- `.\scripts\otto-gw.bat <cmd>` (cmd.exe dispatcher, no policy ceremony)
- Double-click `start.bat` / `stop.bat` / `status.bat` from Explorer

Also add a brief troubleshooting note covering the Group-Policy-locked
CurrentUser case — surface the same `-ExecutionPolicy Bypass -File` workaround
the `:err` branch in `setup.bat` surfaces.

</tasks>

<verify>

**Encoding:**

```bash
file scripts/*.bat                                                # all "ASCII text" or "DOS batch file text"
for f in scripts/*.bat; do head -c 3 "$f" | xxd | head -1; done   # never "efbbbf"
```

**Packaging:**

```bash
make package-all                                                              # succeeds
unzip -l dist/otto_gateway-windows-amd64-*.zip | grep '\.bat$' | wc -l        # 5
tar -tzf dist/otto_gateway-linux-amd64-*.tar.gz | grep '\.bat$' | wc -l       # 5
tar -tzf dist/otto_gateway-darwin-arm64-*.tar.gz | grep '\.bat$' | wc -l      # 5
tar -tzf dist/otto_gateway-darwin-amd64-*.tar.gz | grep '\.bat$' | wc -l      # 5
```

**Non-regression on existing wrappers:**

```bash
git diff scripts/otto-gw scripts/otto-gw.ps1 HEAD~1..HEAD                     # MUST be empty
```

**Macos cannot smoke-test the bats functionally** — that's the Windows tester's
job (real double-click of `setup.bat`, lifecycle round-trip, GP-locked
CurrentUser fallback rendering). Static checks (encoding, packaging,
non-regression) are sufficient gate for this commit.

</verify>

<scope_boundaries>

OUT of scope:
- Codesigning the `.ps1` with Authenticode (proper fix, requires paid OV/EV
  cert; revisit when team budget allows).
- Changing the existing `.ps1` or bash wrapper subcommand surface.
- Modifying `scripts/otto-gw` or `scripts/otto-gw.ps1` behavior.
- Adding per-command bats beyond start/stop/status (logs/restart/env/run/
  version/init don't need Explorer shortcuts — operators who care about those
  will use the dispatcher).

</scope_boundaries>

<commit_message>

Single atomic commit covering all 7 files. Subject:

```
feat(otto-gw): ship .bat shortcuts for Windows operators (setup + dispatcher + per-command)

Five new .bat files in scripts/ remove the per-extract PowerShell
execution-policy + Mark-of-the-Web friction that bites every Windows
operator on first run:

  scripts/setup.bat       one-time MOTW strip + CurrentUser policy set
  scripts/otto-gw.bat     dispatcher mirroring .ps1 subcommands
  scripts/start.bat       Explorer double-clickable shortcut
  scripts/stop.bat        same
  scripts/status.bat      same

cmd.exe is exempt from PowerShell execution policy, so the .bat files
always run on a fresh extract. The dispatcher passes -ExecutionPolicy
Bypass -File to powershell, which means it works even before setup.bat
has been run.

Bats are ASCII no-BOM (cmd.exe parses BOM as first command). Existing
.ps1 keeps its BOM (Windows PowerShell 5.x needs it). Existing bash and
PowerShell wrappers are untouched — the .bat surface is purely additive.

Makefile stage_unix updated to ship all 5 bats in every package
(darwin/linux/windows tarballs all include them; POSIX users ignore).

docs/operator-quickstart.md gains a Windows-specific quick-start step
(3b) describing the setup.bat double-click + three usage patterns, plus
a troubleshooting note for Group-Policy-locked CurrentUser
environments.
```

</commit_message>
