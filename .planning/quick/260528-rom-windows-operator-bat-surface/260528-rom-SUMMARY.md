---
task: 260528-rom-windows-operator-bat-surface
type: quick
status: complete
files_modified:
  - scripts/setup.bat
  - scripts/otto-gw.bat
  - scripts/start.bat
  - scripts/stop.bat
  - scripts/status.bat
  - Makefile
  - docs/operator-quickstart.md
commit: feat(otto-gw)-ship-bat-shortcuts-for-windows-operators
---

# Summary: Windows operator .bat surface

Shipped the Windows operator `.bat` surface so first-start friction on a
fresh extract disappears. Five new `.bat` files in `scripts/`, one
`stage_unix` Makefile edit so they ride along in every package, and a
Windows-specific subsection plus Group-Policy troubleshooting note in
`docs/operator-quickstart.md`. Single atomic commit. Existing `.ps1` and
POSIX wrappers untouched — this surface is purely additive.

## What was built

- `scripts/setup.bat` — one-time post-extract bootstrap. Calls
  `Get-ChildItem … | Unblock-File` to strip MOTW Zone.Identifier streams
  the OS attached during download, then `Set-ExecutionPolicy -Scope
  CurrentUser -ExecutionPolicy RemoteSigned -Force`. Both calls go
  through `powershell -NoProfile -ExecutionPolicy Bypass -Command` so the
  bootstrap works even before any execution-policy state has been set.
- `scripts/otto-gw.bat` — cmd.exe-friendly dispatcher. Single line:
  `powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0otto-gw.ps1" %*`.
  Mirrors the full subcommand surface of the `.ps1`
  (`init|start|stop|status|restart|logs|run|env|version`) via `%*`
  passthrough — no need to keep two surfaces in sync.
- `scripts/start.bat` / `stop.bat` / `status.bat` — three per-command
  shortcuts, each one line:
  `@call "%~dp0otto-gw.bat" <cmd> %*`. Double-clickable from File
  Explorer; preserve any extra args.
- `Makefile` — `stage_unix` macro `cp` line extended to include all 5
  new `.bat` files alongside the existing `otto-gw` / `otto-gw.ps1` /
  `.env.otto-gw.example`. No new `stage_windows` variant — ship-
  everywhere decision keeps the staging path coherent.
- `docs/operator-quickstart.md` — new `### 3b. (Windows only) Run
  setup.bat once` step inserted between the macOS quarantine step and
  the `kiro-cli` install step, naming all three runtime surfaces
  (`.ps1`, `.bat` dispatcher, per-command shortcuts). New
  troubleshooting entry `### Windows: setup.bat says "Setup hit an
  error" or otto-gw.ps1 still refuses to run` covers the
  Group-Policy-locked `CurrentUser` case and points operators at the
  per-invocation `-ExecutionPolicy Bypass -File` form that the `.bat`
  dispatcher already uses internally.

## Encoding gotcha sidestepped

cmd.exe parses a UTF-8 BOM (`0xEF 0xBB 0xBF`) at the start of a `.bat`
file as the first command, breaking the script. The `Write` tool on
macOS defaulted to UTF-8 no-BOM, which is exactly what we needed. Post-
write verification:

```
$ file scripts/*.bat
scripts/setup.bat:   DOS batch file text, ASCII text
scripts/otto-gw.bat: DOS batch file text, ASCII text
scripts/start.bat:   ASCII text
scripts/stop.bat:    ASCII text
scripts/status.bat:  ASCII text

$ for f in scripts/*.bat; do head -c 3 "$f" | xxd -p; done
406563   # @ec — setup.bat
406563   # @ec — otto-gw.bat
406361   # @ca — start.bat
406361   # @ca — stop.bat
406361   # @ca — status.bat
```

No `efbbbf` anywhere. All five are ASCII / DOS batch text. The pre-
existing `scripts/otto-gw.ps1` keeps its BOM (unchanged); PowerShell
handles BOM correctly, cmd.exe does not.

## Ship-everywhere staging decision

The brief called out a `stage_windows` Makefile variant as premature
complexity for what is essentially zero cost. The 5 `.bat` files ship
inside all four archives (darwin-arm64, darwin-amd64, linux-amd64,
windows-amd64) — POSIX operators get five inert text files, which is the
same outcome as having a Windows VM accidentally pick up the POSIX
`otto-gw` shell script. No harm, no maintenance burden, one staging path
instead of two.

Confirmed across all four archives:

```
darwin-arm64: 5 .bat files
darwin-amd64: 5 .bat files
linux-amd64:  5 .bat files
windows-amd64: 5 .bat files
```

## Group-Policy-locked fallback message — one source of truth in two places

`setup.bat`'s `:err` branch and the operator-quickstart troubleshooting
note carry the same fallback recipe:

```
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\otto-gw.ps1 <command>
```

The form is identical in both surfaces because the `otto-gw.bat`
dispatcher *already* uses this exact form internally — so on a Group-
Policy-locked machine where `setup.bat` cannot persistently change
`CurrentUser`, the operator can use `otto-gw.bat <command>` (or the
per-command `.bat` shortcuts) with no further intervention. The
troubleshooting note explicitly calls this out so operators don't think
they're stuck.

## Existing wrappers untouched

```
$ git diff scripts/otto-gw scripts/otto-gw.ps1
(empty)
```

The `.bat` surface is additive. No existing PowerShell or POSIX
behavior changed.

## Deferred — Windows functional smoke

This change cannot be functionally tested on macOS. The Windows tester
will confirm:

- Double-clicking `setup.bat` runs `Unblock-File` + `Set-ExecutionPolicy`
  successfully on a real Windows 10/11 machine (Home + Pro).
- `otto-gw.bat start` / `stop` / `status` dispatch correctly to the
  `.ps1` and round-trip the gateway lifecycle.
- The Group-Policy-locked CurrentUser fallback message renders correctly
  in the `:err` branch when `Set-ExecutionPolicy -Scope CurrentUser`
  raises a "access denied" / "policy locked by group policy" error.
- Per-command shortcuts (`start.bat`, etc.) work when double-clicked
  from File Explorer (not just `cmd.exe`).

Static checks done on macOS — encoding, packaging, archive contents,
existing-wrapper diff — are sufficient to merge without blocking on a
Windows machine being available.

## Deviations from plan

None. Plan executed exactly as the planner specified, including the
literal file contents for all five `.bat` files and the exact wording
for the docs subsection.

The only tiny addition was a cross-reference link from the `### 3b.`
quick-start subsection to the new `### Windows: setup.bat says "Setup
hit an error"` troubleshooting entry — pure navigation polish, no new
content.

## Self-Check: PASSED

- `scripts/setup.bat`        FOUND, ASCII no-BOM, contains `Unblock-File` and `Set-ExecutionPolicy`
- `scripts/otto-gw.bat`      FOUND, ASCII no-BOM, contains `ExecutionPolicy Bypass -File`
- `scripts/start.bat`        FOUND, ASCII no-BOM, dispatches `start %*`
- `scripts/stop.bat`         FOUND, ASCII no-BOM, dispatches `stop %*`
- `scripts/status.bat`       FOUND, ASCII no-BOM, dispatches `status %*`
- `Makefile`                 stage_unix updated, no `stage_windows` variant
- `docs/operator-quickstart.md` has step 3b + Windows troubleshooting note
- `make package-all`         succeeded; 5 `.bat` files in each of 4 archives
- `git diff scripts/otto-gw scripts/otto-gw.ps1`  empty (existing wrappers untouched)
