---
phase: quick-260528-tbg
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - docs/INSTALL.md
  - Makefile
  - docs/operator-quickstart.md
autonomous: true
---

<objective>
Ship `docs/INSTALL.md` as part of every distribution archive — a focused
installation/upgrade reference that complements the existing operator-quickstart
README. Surfaced from the v1.5.1 Windows operator smoke test: operators need a
clear understanding of per-OS install paths, `.env` file load order with the
Windows-double-click cwd gotcha, the wrapper choice (.bat vs .ps1 vs bash),
upgrade behavior on re-extract, common install pitfalls, and verification steps.
</objective>

<context>
The existing in-zip `README.md` (sourced from `docs/operator-quickstart.md`)
owns the happy path. It's task-oriented and tightly scoped. Operators who hit
nuance — "where should my .env file live?", ".bat or .ps1?", "what does
re-extract preserve?" — need a reference doc, not a quickstart.

INSTALL.md complements the quickstart by owning the nuance. The two ship side
by side in every archive.
</context>

<tasks>

### Task 1 — Write `docs/INSTALL.md` + wire it into the package

**1.1: Write `docs/INSTALL.md`** (target 400-500 lines, UTF-8 no BOM).

Required sections (matching the brief):

1. **Table of Contents** — anchor-linked, hierarchical.
2. **First-run checklist per OS:**
   - macOS — verify SHA → extract → `xattr -d com.apple.quarantine bin/otto-gateway` → install kiro-cli → write .env → start.
   - Linux — verify SHA → extract → install kiro-cli → write .env → start.
   - Windows — verify SHA → `Expand-Archive` → double-click `scripts\setup.bat` → install kiro-cli → write .env → start via `.bat` or `.ps1`.
3. **The `.env` file** — load order (same 4-source precedence from bash `load_env_file` and PowerShell `Resolve-EnvFile`) + per-OS recommended location.
   - MUST cover the Windows-double-click cwd gotcha: when an operator double-clicks `start.bat` from Explorer, cwd is `scripts\` not the `otto_gateway\` parent. Relative `.\.env.otto-gw` resolves to `scripts\.env.otto-gw` which is probably not where the operator put their env file.
   - Recommend `$env:USERPROFILE\.otto-gw.env` on Windows AND `$HOME/.otto-gw.env` on POSIX as the cwd-independent stable location.
4. **Wrapper choice tradeoff table:**
   - Windows surfaces: `.bat` dispatcher (recommended for daily use; cmd.exe is immune to PowerShell execution policy), `.ps1` (PowerShell-native, better for scripted automation with typed flags), Explorer `.bat` shortcuts (`start.bat` / `stop.bat` / `status.bat`).
   - POSIX surface: single bash wrapper (`scripts/otto-gw`).
   - GFM table with concrete tradeoffs (execution policy / MOTW / GP lockdown / subcommand surface / type checking).
5. **Upgrade behavior** — what extracting over an existing install preserves vs replaces:
   - **Preserved:** `.env.otto-gw` (real config; not in zip), `logs/` (operator data), `.otto/gw/` (PID + state).
   - **Replaced:** `bin/`, wrappers, `README.md`, `INSTALL.md`, `.env.otto-gw.example`, `.bat` shortcuts.
   - Explicit note: `Expand-Archive -Force` overwrites `scripts/.env.otto-gw.example` — your real `.env.otto-gw` is never in the zip.
6. **Common install pitfalls:**
   - Windows MOTW + execution policy → point to `setup.bat`.
   - Windows Group Policy lockdown → use `.bat` dispatcher.
   - macOS Gatekeeper → `xattr -d com.apple.quarantine`.
   - kiro-cli not on PATH → degraded boot, 503 on chat (with link to set `KIRO_CMD`).
7. **Verifying install** — concrete one-liners with EXPECTED OUTPUT:
   - `./scripts/otto-gw version` → e.g. `v1.5.2`
   - `./bin/otto-gateway --version` → same
   - `curl http://127.0.0.1:18080/health` → `{"status":"ok",...}`
   - `curl http://127.0.0.1:18080/admin/snapshot` → JSON with `"version":"v1.5.2"` field

**1.2: Update `Makefile`** (~6 lines changed):

- Near line 16, alongside `PKG_README := docs/operator-quickstart.md`, declare:
  ```make
  PKG_INSTALL := docs/INSTALL.md
  ```
- Inside the `stage_unix` macro (~line 152), after `cp $(PKG_README) $(DIST_DIR)/otto_gateway/README.md`, add:
  ```make
  cp $(PKG_INSTALL) $(DIST_DIR)/otto_gateway/INSTALL.md
  ```
- Update the 4 `package-*` dep lists (lines 176/182/188/193) to add `$(PKG_INSTALL)` alongside `$(PKG_README)`.

**1.3: Update `docs/operator-quickstart.md`** (~2 lines added):

Brief cross-reference at the top, before step 1:

> For deeper guidance on `.env` file locations, wrapper choice (.bat vs .ps1 vs bash), upgrade behavior, and install troubleshooting, see [INSTALL.md](./INSTALL.md). This quickstart focuses on the happy path.

The `./INSTALL.md` link is **sibling-relative** so the same anchor works both in the repo (`docs/INSTALL.md`) and in the extracted archive (`otto_gateway/INSTALL.md` next to `otto_gateway/README.md`) without rewriting at package time.

</tasks>

<verify>

After implementation:

```bash
# Length target
wc -l docs/INSTALL.md     # ~400-500 lines

# Encoding
head -c 3 docs/INSTALL.md | xxd   # must NOT show 'efbbbf' (no BOM)

# Build
make package-all          # exit 0

# Packaging — INSTALL.md in every archive
unzip -l dist/otto_gateway-windows-amd64-*.zip | grep INSTALL.md
tar -tzf dist/otto_gateway-linux-amd64-*.tar.gz | grep INSTALL.md
tar -tzf dist/otto_gateway-darwin-arm64-*.tar.gz | grep INSTALL.md
tar -tzf dist/otto_gateway-darwin-amd64-*.tar.gz | grep INSTALL.md

# Out-of-scope guards
git diff scripts/ bin/ tests/ .github/ docs/operating.md HEAD~1..HEAD   # MUST be empty
```

</verify>

<scope_boundaries>

OUT of scope:
- Modifying any wrapper code (bats, bash, ps1).
- Touching `bin/`, `tests/`, `.github/`.
- Re-updating `docs/operating.md` (already covered in commit `7073079`).
- Tagging v1.5.2 / rebuilding / republishing (orchestrator handles after this task lands).

</scope_boundaries>

<commit_message>

Single atomic commit subject:

```
feat(docs): ship INSTALL.md in distribution archives (Windows-aware install reference)

New docs/INSTALL.md (~481 lines) provides a focused installation/upgrade
reference complementing the operator-quickstart README that already ships
in every archive. Owns the nuance the quickstart deliberately omits:

- Per-OS first-run checklist (macOS / Linux / Windows)
- .env file load order with cwd-independent location recommendations
  (covers the Windows-double-click-from-Explorer cwd gotcha that resolves
   .\.env.otto-gw to scripts\.env.otto-gw instead of the otto_gateway parent)
- Wrapper choice tradeoff table (.bat for daily Windows use, .ps1 for
  scripted automation, Explorer .bat shortcuts for double-click convenience,
  bash wrapper on POSIX)
- Upgrade behavior — what extract-over preserves (.env.otto-gw, logs/,
  .otto/gw/) vs replaces (bin/, wrappers, README, INSTALL, .example,
  .bat shortcuts)
- Common install pitfalls (Windows MOTW + GPO lockdown, macOS Gatekeeper,
  kiro-cli not on PATH)
- Verifying install — concrete commands with expected output across all
  entry points (wrapper version subcommand, binary --version, /health,
  /admin/snapshot)

Makefile declares PKG_INSTALL := docs/INSTALL.md alongside the existing
PKG_README, copies it into the staged package via the stage_unix macro,
and adds it as a dep to all 4 package-* rules.

operator-quickstart.md gains a 2-line cross-reference at the top pointing
operators to INSTALL.md for deeper guidance. Uses ./INSTALL.md
sibling-relative so the link works both in the repo (docs/INSTALL.md ->
docs/operator-quickstart.md) and in the extracted archive
(otto_gateway/INSTALL.md -> otto_gateway/README.md) without rewriting at
package time.

No wrapper code changes. No binary changes.
```

</commit_message>
