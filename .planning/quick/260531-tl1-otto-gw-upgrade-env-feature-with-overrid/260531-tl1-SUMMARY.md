---
quick_id: 260531-tl1
type: summary
title: otto-gw upgrade-env feature with overrides.env split model
date: 2026-05-31
status: complete
phase: quick/260531-tl1
plan: 1
tasks_completed: 6
commits:
  - hash: df854d9
    task: 1
    type: feat
    subject: chain overrides.env loader + add upgrade-env subcommand (bash)
  - hash: 8ecc43b
    task: 2
    type: feat
    subject: mirror overrides.env loader + upgrade-env (PS1)
  - hash: a991a93
    task: 3
    type: feat
    subject: init writes overrides + migrate-to-overrides + single-file deprecation warn (bash)
  - hash: 870a81c
    task: 4
    type: feat
    subject: mirror init / migrate-to-overrides / deprecation-warn (PS1)
  - hash: e0efd51
    task: 5
    type: test
    subject: tmpdir smoke harness for upgrade-env / migrate-to-overrides / two-file precedence
  - hash: 30a9c21
    task: 6
    type: docs
    subject: two-file model header in env template + PS1 review pass
files_changed:
  - scripts/otto-gw
  - scripts/otto-gw.ps1
  - scripts/.env.otto-gw.example
  - .planning/quick/260531-tl1-otto-gw-upgrade-env-feature-with-overrid/smoke/test-tl1.sh
---

# 260531-tl1 Summary — otto-gw upgrade-env with overrides.env split model

## One-liner

Two new `otto-gw` subcommands (`upgrade-env`, `migrate-to-overrides`) plus a two-file loader contract (`.otto-gw.env` is always a byte-for-byte template copy; `.otto-gw.overrides.env` is the operator's customization layer, loaded second so its values win). After this lands, every new env var added to `scripts/.env.otto-gw.example` is picked up by existing operators via one command — no more per-key registry edits in the wrapper.

## What changed

### `scripts/otto-gw` (bash wrapper)

- **New helpers** (bash-3.2-safe; no associative arrays, no `mapfile`):
  - `resolve_overrides_file`, `resolve_template_file` — symmetric to existing `resolve_env_file`.
  - `template_keys`, `env_keys_present`, `extract_default_value` — key-set + default-value parsers for the upgrade-env diff and migrate-to-overrides delta calc.
  - `set_overrides_line` — writes/updates `KEY=VALUE` in the overrides file (always uncommented; auto-creates with header + mode 0600).
  - `detect_single_file_model` — one-shot deprecation-warn detector for the legacy single-file layout.
- **`load_config` chain** — `.otto-gw.env` first, `.otto-gw.overrides.env` second. `load_env_file`'s `export "$key=$val"` is intentionally last-write-wins so the second load IS the override mechanism. Sets `OTTO_OVERRIDES_FILE_LOADED` for binary-side observability.
- **`parse_flags`** gains: `--overrides-file`, `--template`, `--dest`, `--overrides-dest`, `--dry-run`, `--yes`.
- **`upgrade-env`** subcommand: computes added / orphaned / unchanged keys via `comm` + process substitution; `--dry-run` exits without writing; default flow confirms unless `--yes`; orphan log written BEFORE the `cp` so a SIGKILL between log + cp leaves the operator their old file AND a record of what was about to be lost.
- **`migrate-to-overrides`** subcommand: idempotent (no-op when overrides exists AND dest is byte-identical to template); extracts every uncommented key whose value differs from the template default; `AUTH_TOKEN` + `PII_HASH_KEY` always carry across when uncommented (secrets are per-install, never "default"); backs up the original dest to `.pre-migrate.<UTC>`.
- **`init_cmd` refactor**: the `set_env_line` block against `.otto-gw.env` is gone. The file is now ALWAYS a byte-for-byte `cp` of the template (mode 0644). Operator customizations go to `.otto-gw.overrides.env` (mode 0600) via `set_overrides_line`. Pre-flight migration: `--force` on a legacy single-file install runs the migration silently before the normal init flow and emits one INFO line.
- **Existing-value lookup** (for re-init defaults) now reads from BOTH `dest` AND `overrides_dest` with overrides winning on conflict (matches runtime loader precedence).
- **Deprecation WARN** wired into `load_config` after the resolution step. Fires once per invocation when the legacy single-file pattern is detected.
- **Usage + top-of-file header** updated to document the new subcommands, new flags, and the new precedence ladder (`CLI → overrides → .env → shell env`).

### `scripts/otto-gw.ps1` (PowerShell mirror)

- **1:1 surface parity** with every bash addition above. New functions: `Resolve-OverridesFile`, `Resolve-TemplateFile`, `Get-TemplateKeys`, `Get-EnvKeysPresent`, `Get-DefaultValue`, `Set-OverridesLine`, `Test-SingleFileModel`, `Invoke-UpgradeEnv`, `Invoke-MigrateToOverrides`.
- `Initialize-Config` chains env → overrides. `Test-SingleFileModel` is invoked after resolution; uses `Write-Warning` so the WARN goes to the warning stream (functionally stderr).
- `Invoke-Init` refactor mirrors the bash side: `Copy-Item` for the template-copy, `Set-OverridesLine` for every customization, pre-flight migration on legacy installs, existing-value layering (dest then overrides, overrides wins).
- `Compare-Object` is the PowerShell idiom for the diff (equivalent to bash `comm` + process substitution).
- `Get-FileHash` SHA256 comparison drives `Invoke-MigrateToOverrides`'s idempotency check.
- Param block + Show-Usage updated; Commands list now includes `init` (parity gap fixed in Task 6) and Gateway flags now document `-IdleTimeout` (parity gap fixed in Task 6).
- **Static review only.** No live pwsh test on macOS dev box per CONTEXT.md Decision 7 (the 260531-oax precedent). Braces (422/422) + parens (640/640) balanced. Operator runs the live Windows smoke after merge.

### `scripts/.env.otto-gw.example` (template)

- Header (lines 1–7) replaced with a 10-line block that explains the two-file model: this file is the TEMPLATE, copied byte-for-byte to `.otto-gw.env`; customizations go in `.otto-gw.overrides.env`.
- Every `KEY=value` line below the header is **byte-identical** to pre-task. Verified via `git diff` + key-line extract diff. This is load-bearing because `upgrade-env` overwrites the dest with this file; if the body changed, all installs would see surprise diffs.

### `.planning/quick/260531-tl1-…/smoke/test-tl1.sh` (smoke harness)

- New executable bash script; 7 sections exercising the four critical behaviors end-to-end against a tmpdir:
  1. Fresh init writes both files (template-copy + overrides, correct modes).
  2. Loader precedence — overrides value wins.
  3. `upgrade-env --dry-run` reports added + orphaned without writing.
  4. `upgrade-env --yes` regenerates dest byte-identically and logs orphan values.
  5. `migrate-to-overrides` extracts non-default values; second run is a no-op.
  6. Deprecation WARN fires on legacy single-file install.
  7. Deprecation WARN silent when overrides present.
- Localizes `OTTO_UPGRADE_LOG` to the tmpdir so the test never writes to `$HOME`.
- Stubs `OTTO_BIN` so any subcommand path that touches preflight stays clean. The harness only uses `env` + the two new subcommands — none exec the binary.

## Verification

### Per-task gates (every bash-touching commit)

- `bash -n scripts/otto-gw`: clean.
- `shellcheck scripts/otto-gw`: clean (no new warnings vs. pre-task baseline; `parse_flags` carries one scoped `SC2034` disable for `FLAG_OVERRIDES_DEST` which is consumed by `migrate-to-overrides`).
- `bash -n .planning/quick/260531-tl1-…/smoke/test-tl1.sh`: clean.
- `shellcheck .planning/quick/260531-tl1-…/smoke/test-tl1.sh`: clean (file-level `SC2016` + `SC2034` disables — both are false positives under `assert()`'s `eval` pattern).

### Smoke harness

```
ALL TL1 SMOKE CHECKS PASSED
```

Run against the merged wrapper after every bash-touching commit + once more at Task 6 close. Green every time.

### PS1 parity

Static review only (no pwsh on macOS). Function-name and dispatch-case parity confirmed via grep diff between bash and PS1:

| bash                          | PS1                          |
| ----------------------------- | ---------------------------- |
| `resolve_overrides_file`      | `Resolve-OverridesFile`      |
| `resolve_template_file`       | `Resolve-TemplateFile`       |
| `template_keys`               | `Get-TemplateKeys`           |
| `env_keys_present`            | `Get-EnvKeysPresent`         |
| `extract_default_value`       | `Get-DefaultValue`           |
| `set_overrides_line`          | `Set-OverridesLine`          |
| `detect_single_file_model`    | `Test-SingleFileModel`       |
| `upgrade_env_cmd`             | `Invoke-UpgradeEnv`          |
| `migrate_to_overrides_cmd`    | `Invoke-MigrateToOverrides`  |

Braces 422/422, parens 640/640 balanced. PS1 live verification deferred to operator's Windows box (the **WINDOWS-SMOKE-DEFERRED** marker; matches the 260531-oax / 260531-t8a pattern).

## Honored decisions (no deviations from CONTEXT.md)

- **Decision 1 (two-file model)** — honored: `.otto-gw.env` generated, `.otto-gw.overrides.env` operator-owned, loaded second.
- **Decision 2 (wrapper loader chain)** — honored: bash `load_config` and PS1 `Initialize-Config` both chain env → overrides; last-write-wins is the override mechanism.
- **Decision 3 (`upgrade-env`)** — honored: dry-run + yes + overwrite-template + orphan log to `~/.otto-gw.upgrade.log` (with `OTTO_UPGRADE_LOG` override + project-local fallback per PLAN Risk #8).
- **Decision 4 (`migrate-to-overrides`)** — honored: idempotent re-runs, backs up dest to `.pre-migrate.<ts>`.
- **Decision 5 (init writes secrets to overrides)** — honored: secrets ALWAYS land in overrides; `.otto-gw.env` is a pure template copy.
- **Decision 6 (backward compat + WARN)** — honored: legacy single-file installs still boot; one WARN line at start (and via env subcommand) recommends `otto-gw migrate-to-overrides`.
- **Decision 7 (cross-platform)** — honored: bash + PS1 land in parallel; PS1 is static-reviewed because the dev box has no pwsh.
- **Decision 8 (out-of-scope items deliberately skipped)** — honored: no auto-apply-on-start, no editor integration, no vault integration, no interactive upgrade prompts beyond `--yes` confirmation.

## Auto-fixed issues during execution (Rule 1)

1. **Bug — `template_keys` parser too permissive.**
   - **Found during:** Task 3 functional smoke (the parser was emitting `Syntax: KEY` as a bogus key because the prose comment line `# Syntax: KEY=value, one per line.` happened to match the loose `[A-Za-z_]*` prefix check).
   - **Fix:** Tightened the validity check from a prefix-class case to a full identifier-shape `=~` regex (`^[A-Za-z_][A-Za-z0-9_]*$`).
   - **Files:** `scripts/otto-gw` (lines around `template_keys`).
   - **Commit:** `a991a93` (folded into the Task 3 commit since the parser was only being exercised by Task 3's new code path).

2. **Bug — `STREAM_IDLE_TIMEOUT_SEC` leaked from commented template default to overrides on re-init.**
   - **Found during:** Task 3 hand verification of `init --force` against a legacy single-file install.
   - **Root cause:** `extract_env_any` matches commented forms too, so `existing_idle_timeout` was set to "30" extracted from `# STREAM_IDLE_TIMEOUT_SEC=30` (the template's commented default). The re-init code path then wrote it uncommented to overrides.
   - **Fix:** Gate `existing_idle_timeout` behind `_existing_uncommented STREAM_IDLE_TIMEOUT_SEC` so the commented default leaves `existing_idle_timeout` empty, which lets the idle-timeout block use the cold-start default (commented "30").
   - **Files:** `scripts/otto-gw` (init_cmd existing-value lookup); the PS1 mirror (`Invoke-Init` STREAM_IDLE_TIMEOUT_SEC gating) carries the same fix in commit `870a81c`.
   - **Commit:** `a991a93` (bash) + `870a81c` (PS1).

These are **Rule-1 bug fixes scoped to the new code path** — neither pre-existed. Both surfaced during functional verification of the same task that introduced the surrounding logic.

## Parity gaps fixed during Task 6 review pass

- **PS1 Show-Usage was missing `init [flags]` in the Commands list.** Bash usage has it as the first command; PS1 only mentioned it under "init flags (for the 'init' subcommand):". Added.
- **PS1 Show-Usage was missing `-IdleTimeout INT` documentation.** The switch has been in the param block since 260531-ruv but was never surfaced in help text. Added.

Both fixes land in commit `30a9c21`.

## Out of scope (deferred)

- **Live PS1 smoke (`WINDOWS-SMOKE-DEFERRED`).** macOS dev box has no pwsh. PS1 was verified by static review against the bash counterpart; operator runs the smoke on Windows after merge. Matches the 260531-oax / 260531-t8a / 260531-ruv policy.
- **Operator-quickstart pointer.** PLAN Sub-C carved this out as "optional, low-risk." Skipped per the orchestrator's scope_lock (wrapper + smoke + template-header polish). The wrapper's `--help` is authoritative.
- **PS1 STREAM_IDLE_TIMEOUT_SEC duplicate-prose-key noise.** The same `# CHAT_TRACE=false (default) means...` prose line in the template ends up parsed as a duplicate `CHAT_TRACE` key in the key-set on both sides. `sort -u` (bash) / `Sort-Object -Unique` (PS1) dedupes the upgrade-env diff; migrate-to-overrides considers uncommented-only so duplicates don't matter there. Acceptable; cleaner parsing (skip lines whose value contains whitespace before the first quote) is a v1.7 polish item.

## Risks (carry-forward)

1. **`OTTO_TEMPLATE_FILE` override.** Operators can point upgrade-env at an unreleased template (`--template PATH` / `OTTO_TEMPLATE_FILE=PATH`). No safety net; the orphan log still captures what was overwritten if they regret it.
2. **Hand-edits to `.otto-gw.env` after migrate.** Documented as undefined-but-harmless: edits are overwritten on the next `upgrade-env`. The deprecation WARN doesn't re-fire after migration because the overrides file exists, but if the operator re-introduces an uncommented operator value into `.otto-gw.env`, the WARN does NOT detect this case (the WARN requires absence of overrides file). This is the deliberate v1.6 → v1.7 transition shape; closing the loophole is v1.7 work.

## Success criteria (per PLAN must_haves)

- [x] Wrapper loads `.otto-gw.env` first, then `.otto-gw.overrides.env`; overrides-file values win for any shared key (bash + PS1).
- [x] `otto-gw upgrade-env` regenerates `.otto-gw.env` byte-identical to the installed template and reports added / orphaned / unchanged keys.
- [x] `otto-gw upgrade-env --dry-run` prints the diff and exits 0 without touching disk.
- [x] `otto-gw migrate-to-overrides` extracts non-default values from the current single-file `.otto-gw.env` into `.otto-gw.overrides.env`, backs up the original, and regenerates `.otto-gw.env` from the template.
- [x] Re-running migrate-to-overrides after success is a no-op (idempotent).
- [x] `otto-gw init` writes AUTH_TOKEN and PII_HASH_KEY to `.otto-gw.overrides.env` (not `.otto-gw.env`). The generated `.otto-gw.env` is a pure template copy.
- [x] On `start` (and any path that triggers `load_config`), if the wrapper detects a single-file install, it logs one WARN line recommending `otto-gw migrate-to-overrides` and continues.
- [x] Both bash and PowerShell surfaces support `upgrade-env` and `migrate-to-overrides` with equivalent flag shapes.
- [x] Template resolution finds `scripts/.env.otto-gw.example` in dev (sibling-to-wrapper) and `<install-root>/scripts/.env.otto-gw.example` in a shipped install (anchored to `$OTTO_INSTALL_ROOT`).

**Proof of the registry-killing claim:** adding one new env var to `scripts/.env.otto-gw.example` after this lands now requires ZERO edits to `scripts/otto-gw` or `scripts/otto-gw.ps1` for existing operators to pick it up via `otto-gw upgrade-env`. The `set_env_line` registry blocks inside `init_cmd` are gone; the template is the only source of keys.
