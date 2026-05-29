---
phase: quick-260528-tbg
plan: 01
subsystem: distribution
tags: [docs, install, packaging, makefile, windows, operator]
status: complete

requires:
  - phase: quick-260528-rom
    provides: "Windows operator .bat surface (setup.bat, otto-gw.bat, start/stop/status.bat shortcuts) referenced by INSTALL.md"
  - phase: quick-260528-qe9
    provides: "Wrapper naming + version subcommand contract verified by INSTALL.md verification commands"
provides:
  - "docs/INSTALL.md (481 lines) — per-OS install/upgrade reference shipped at otto_gateway/INSTALL.md root in every release archive"
  - "PKG_INSTALL Makefile variable + stage_unix copy + 4 package-* deps wiring INSTALL.md into darwin-arm64/darwin-amd64/linux-amd64/windows-amd64 archives"
  - "operator-quickstart.md → INSTALL.md cross-reference (sibling-relative ./INSTALL.md path works in repo + extracted archive identically)"
affects: [v1.5.1-release, windows-operator-smoke-test, archive-extraction, operator-onboarding]

tech-stack:
  added: []
  patterns:
    - "Distribution-archive doc duo: README.md (happy-path quickstart) + INSTALL.md (per-OS nuance reference) both shipped at archive root"

key-files:
  created:
    - docs/INSTALL.md
  modified:
    - Makefile
    - docs/operator-quickstart.md

key-decisions:
  - "INSTALL.md uses sibling-relative ./INSTALL.md link from operator-quickstart.md so the same path resolves in both repo (docs/) and extracted archive (otto_gateway/ root) layouts"
  - "Persistent .env recommendation: cwd-independent per-user location ($HOME/.otto-gw.env POSIX; $env:USERPROFILE\\.otto-gw.env Windows) chosen as default to immunize operators from the Windows double-click cwd gotcha"
  - "Wrapper choice tradeoff table separates Windows surfaces (.bat for daily/GP-locked, .ps1 for typed-flag automation, start/stop/status.bat for double-click) — each row maps to a real operational tradeoff (execution policy lock, typed flags, cwd sensitivity)"
  - "Upgrade behavior section explicitly names which files extract overwrites vs preserves and warns Expand-Archive -Force callers about the silent overwrite of .env.otto-gw.example"

patterns-established:
  - "Distribution doc duo pattern: README.md ships the happy path; INSTALL.md ships the deep reference. Cross-referenced via sibling-relative link so paths match in repo + archive."
  - "Archive-shipped reference docs: docs that live in docs/ in the repo get copied to archive root via Makefile PKG_* variables + stage_unix copy, with each package-* rule listing them as deps for dirty-tracking."

requirements-completed:
  - QUICK-260528-tbg

duration: 12min
completed: 2026-05-28
---

# Phase quick-260528-tbg Plan 01: Ship docs/INSTALL.md as part of every distribution archive — Summary

**Added a 481-line per-OS install/upgrade reference (docs/INSTALL.md) that ships at otto_gateway/INSTALL.md root in all 4 release archives (darwin-arm64/darwin-amd64/linux-amd64/windows-amd64), with operator-quickstart.md cross-referencing it from above the first install step.**

## What shipped

### docs/INSTALL.md (new — 481 lines)

Sections (slug = anchor target):
- `# OTTO Gateway — Install & Upgrade Reference` (title + value statement)
- `## Table of contents` (anchor-linked TOC)
- `## First-run checklist: macOS` (6 steps)
- `## First-run checklist: Linux` (5 steps)
- `## First-run checklist: Windows` (6 steps — setup.bat lead, MOTW + execution policy explained in prose)
- `## The .env file`
  - `### Load order` (4-step search, cited verbatim from scripts/otto-gw `DEFAULT_ENV_PATHS` + scripts/otto-gw.ps1 `$DefaultEnvPaths`)
  - `### Recommended location per OS` (cwd-independent stable paths)
  - `### The Windows-double-click cwd gotcha` (explicit subsection)
  - `### Precedence summary`
- `## Wrapper choice tradeoff table` (GFM tables — Windows 3-row, POSIX 1-row)
- `## Upgrade behavior` (what is replaced / preserved / Expand-Archive -Force caveat / recommended persistent .env location / Windows-only setup.bat re-run)
- `## Common install pitfalls` (6 subsections: .ps1 blocked, setup.bat GP-locked, macOS Gatekeeper, kiro-cli missing, port in use, hash-mode without key)
- `## Verifying install` (4 verification commands + 1 optional, all with expected output)
- `## Where to go next` (pointers to README.md + docs/operating.md)

Content properties verified:
- UTF-8 no BOM (matches markdown convention)
- 481 lines (target 400-500; within range — accuracy + completeness wins over arbitrary length)
- All TOC `(#anchor)` links resolve to either `##` or `###` heading slugs
- GFM tables with aligned pipes
- Code fences tagged `bash`, `powershell`, `cmd`, `make`
- No emoji (per CLAUDE.md instruction; the only emoji-looking glyphs are the literal `✓` / `⚠` lines reproduced from wrapper script output)
- Required strings present: `Windows-double-click` (×2), `USERPROFILE` (×8), `Expand-Archive -Force` (×3), `/admin/snapshot` (×2)

### Makefile diff (5 hunks)

```
+ PKG_INSTALL := docs/INSTALL.md
                          (line 17, immediately after PKG_README)

+ cp $(PKG_INSTALL) $(DIST_DIR)/otto_gateway/INSTALL.md
                          (inside stage_unix macro, after the cp PKG_README line)

- package-darwin-arm64: cross-darwin-arm64 $(PKG_README) ## ...
+ package-darwin-arm64: cross-darwin-arm64 $(PKG_README) $(PKG_INSTALL) ## ...

- package-darwin-amd64: cross-darwin-amd64 $(PKG_README)
+ package-darwin-amd64: cross-darwin-amd64 $(PKG_README) $(PKG_INSTALL)

- package-linux-amd64: cross-linux-amd64 $(PKG_README)
+ package-linux-amd64: cross-linux-amd64 $(PKG_README) $(PKG_INSTALL)

- package-windows-amd64: cross-windows-amd64 $(PKG_README)
+ package-windows-amd64: cross-windows-amd64 $(PKG_README) $(PKG_INSTALL)
```

### docs/operator-quickstart.md diff (1 hunk, 2 lines added)

```
  ## Install — first-time setup

+ > **Need deeper install/upgrade context?** See [`./INSTALL.md`](./INSTALL.md) (shipped in every release archive) for per-OS first-run checklists, the `.env` cwd-independent location recommendations, the wrapper choice tradeoff table, upgrade behavior, and verification commands with expected output. This README stays focused on the happy-path install — INSTALL.md owns the nuance.
+
  Six steps, ~3 minutes. Each step links to a deeper section if you need details.
```

Path is sibling-relative `./INSTALL.md` — resolves identically in the repo (`docs/INSTALL.md` ↔ `docs/operator-quickstart.md`) and in the extracted archive (`otto_gateway/INSTALL.md` ↔ `otto_gateway/README.md`, since the packager renames operator-quickstart.md → README.md).

## Verification

### `make package-all` end-to-end

```
→ dist/otto_gateway-darwin-arm64-v1.5.1-1-g7073079-dirty.tar.gz
→ dist/otto_gateway-darwin-amd64-v1.5.1-1-g7073079-dirty.tar.gz
→ dist/otto_gateway-linux-amd64-v1.5.1-1-g7073079-dirty.tar.gz
→ dist/otto_gateway-windows-amd64-v1.5.1-1-g7073079-dirty.zip
→ dist/SHA256SUMS-v1.5.1-1-g7073079-dirty.txt
```

All 4 archives + the SHA256SUMS file produced. Exit 0.

### INSTALL.md presence in every archive

```
=== darwin-arm64 ===  otto_gateway/INSTALL.md
=== darwin-amd64 ===  otto_gateway/INSTALL.md
=== linux-amd64 ===   otto_gateway/INSTALL.md
=== windows-amd64 === otto_gateway/INSTALL.md   (23559 bytes)
```

Verified via `tar -tzf` (3 POSIX archives) and `unzip -l` (Windows zip).

### Plan automated verification block — all assertions

```
ALL CHECKS PASSED — line count: 481, package-* deps: 4
```

Asserted:
- `docs/INSTALL.md` exists
- Contains `Windows-double-click`, `USERPROFILE`, `Expand-Archive -Force`, `/admin/snapshot`
- Makefile contains `PKG_INSTALL := docs/INSTALL.md`
- Makefile contains `cp $(PKG_INSTALL) $(DIST_DIR)/otto_gateway/INSTALL.md`
- 4 `package-*` rules carry `$(PKG_INSTALL)` dep (exact match)
- `docs/operator-quickstart.md` references `INSTALL.md`
- `wc -l docs/INSTALL.md` ≥ 350 (actual: 481)
- `make package-all` succeeded
- All 4 archives contain `otto_gateway/INSTALL.md`

### Out-of-scope guard

```
git diff --stat HEAD -- scripts/ bin/ tests/ .github/ docs/operating.md
(empty — no diffs)
```

No edits to `scripts/`, `bin/`, `tests/`, `.github/`, or `docs/operating.md`. Single atomic commit touches exactly 3 files: `docs/INSTALL.md` (new), `Makefile` (modified), `docs/operator-quickstart.md` (modified).

## Deviations from Plan

None — plan executed exactly as written. Line count landed at 481, within the 400-500 soft target band (350-550 hard band per `done` criteria). No auto-fix or auth-gate events.

## Known Stubs

None.

## Self-Check: PASSED

- `docs/INSTALL.md` exists (481 lines, UTF-8 no BOM)
- `Makefile` contains `PKG_INSTALL`, `cp $(PKG_INSTALL)`, and 4 `package-*` deps
- `docs/operator-quickstart.md` contains `INSTALL.md` cross-reference
- `dist/otto_gateway-{darwin-arm64,darwin-amd64,linux-amd64}-*.tar.gz` and `dist/otto_gateway-windows-amd64-*.zip` all contain `otto_gateway/INSTALL.md`
- No diffs in out-of-scope paths (`scripts/`, `bin/`, `tests/`, `.github/`, `docs/operating.md`)
