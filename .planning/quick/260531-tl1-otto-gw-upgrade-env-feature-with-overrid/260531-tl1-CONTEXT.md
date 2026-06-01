---
id: 260531-tl1
title: otto-gw upgrade-env (overrides.env split model) — context
date: 2026-06-01
status: ready-for-plan
---

# Context — 260531-tl1

## Task Boundary

Add `otto-gw upgrade-env` (and the supporting wrapper changes) so existing operators can pick up new env vars from the latest template without losing their customizations or hand-editing their .env every release. Decision: adopt the **overrides.env split** pattern that OSCAR uses, instead of in-place merge.

## Locked Decisions (do not revisit)

### Decision 1 — Two-file model

- `.otto-gw.env` (or `~/.otto-gw.env`) becomes the **generated** file: bit-identical to `scripts/.env.otto-gw.example`, all values commented (defaults surfaced). Never edited by the operator. Safe to overwrite on every upgrade.
- `.otto-gw.overrides.env` (or `~/.otto-gw.overrides.env`) is the **operator file**: contains only the keys they want to set. Loaded **second** by the wrapper so its values win.
- CLI flag precedence still tops both (CLI flag > overrides > .env > shell env).

### Decision 2 — Wrapper loader changes

`scripts/otto-gw` already has `load_env_file`. Extend the resolver to chain two files:
1. Generated `.otto-gw.env` (or `~/.otto-gw.env`) — loaded first.
2. `.otto-gw.overrides.env` (or `~/.otto-gw.overrides.env`) — loaded second; same-key entries override.

Search order for the overrides file mirrors the existing `.env` search:
- `./.otto-gw.overrides.env` (project-local)
- `$HOME/.otto-gw.overrides.env` (per-user)
- `--overrides-file PATH` CLI override
- `$OTTO_OVERRIDES_FILE` env override

Equivalent updates in `scripts/otto-gw.ps1`.

### Decision 3 — New subcommand: `upgrade-env`

`otto-gw upgrade-env [--dest PATH] [--dry-run] [--yes]`:
- Reads the current `scripts/.env.otto-gw.example` (or the installed copy at `<install-root>/.env.otto-gw.example`).
- Computes a diff: keys present in template but not in current `.otto-gw.env`, and keys present in current `.otto-gw.env` but not in template (orphans).
- In dry-run: print the diff and exit.
- Otherwise: regenerate `.otto-gw.env` from the template (fully overwritten — safe because operator config lives in overrides). Report `added: N`, `orphaned: M`, `unchanged: K`.
- Orphans are logged to `~/.otto-gw.upgrade.log` (or sibling) with the timestamp + values, so the operator can review or hand-migrate if needed.

### Decision 4 — New subcommand: `migrate-to-overrides`

`otto-gw migrate-to-overrides [--dest PATH] [--overrides-dest PATH] [--dry-run] [--yes]`:
- One-time migration for users upgrading from the single-file model (v1.6.10 and earlier).
- Reads current `.otto-gw.env`, diffs every uncommented key against the template's default value.
- Lines where the value differs from the template default → written to `.otto-gw.overrides.env` (uncommented).
- Lines where the value equals the template default → ignored (would be a no-op override).
- Backs up the original `.otto-gw.env` to `.otto-gw.env.pre-migrate.<timestamp>` before regenerating from the template.
- Idempotent: re-running on an already-migrated file is a no-op (overrides file already has the customizations; the .otto-gw.env file already matches the template).

### Decision 5 — Secrets handling

`AUTH_TOKEN` and `PII_HASH_KEY` (and any other secret) MUST stay in `.otto-gw.overrides.env` (since they're per-install). The template's commented placeholders never get uncommented in the generated `.otto-gw.env`. The `init` command continues to mint these secrets, but writes them to the overrides file from now on, not the generated file.

This means `init` is also touched: it should write secrets to overrides instead of the main file.

### Decision 6 — Backward compatibility

Until 1.7, support **both** models simultaneously:
- If `.otto-gw.overrides.env` exists → use it (new model).
- If only `.otto-gw.env` exists and it has uncommented operator-set values (i.e., not just the template default file) → still works, single-file model honored.
- The wrapper logs a one-line WARN at boot recommending `otto-gw migrate-to-overrides` when it detects the single-file pattern.

In 1.7 we can deprecate the single-file path and require migration. NOT in this task.

### Decision 7 — Cross-platform

Bash AND PowerShell wrapper changes are equal-priority. PS1 is verified by static review (operator's Windows box, no live PS1 test on macOS dev box) per the 260531-oax caveat.

### Decision 8 — Out of scope (deliberate)

- Auto-applying upgrades on start. Always operator-triggered (`upgrade-env`).
- Editor integration ("press U to upgrade"). Operator runs the command explicitly.
- Vault / secrets manager integration for AUTH_TOKEN. Future.
- Interactive prompts during `upgrade-env` (other than `--yes` confirmation). Keep it scriptable for CI.

## Code Context Pointers

### OSCAR references (already reviewed)

- `~/code/github.com/cmetech/oscar_app/oscar/utils/patch.sh:1408-1534` — `merge_env_properties` — destructive in-place merge. We are NOT following this pattern; mentioned here for the executor's awareness.
- OSCAR `overrides.env` loading: `gen_sslcerts.sh` sources `overrides.env` last so per-host detections win. Same pattern in spirit, different surface.

### otto-gw current shape

- `scripts/otto-gw:20-23` documents the precedence: "CLI flags WIN over .env values; .env wins over inherited shell env only for keys the .env actually sets."
- `scripts/otto-gw:58-83` — `DEFAULT_ENV_PATHS`, `resolve_env_file`, `load_env_file` — the existing single-file loader.
- `scripts/otto-gw:147` — `set_env_line` — used by init to write defaults. After this feature lands, `set_env_line` writes to the generated `.otto-gw.env` for template-shape and to the overrides file for secrets / operator-set values.
- `scripts/otto-gw:701-707` — init's flag parser (where `--idle-timeout` was just added in 260531-t8a).
- `scripts/otto-gw:930-942` — init's set_env_line block (where STREAM_IDLE_TIMEOUT_SEC was just added).

### Template

- `scripts/.env.otto-gw.example` — the golden template. Source of truth for all keys + their default values + their comments. The generated `.otto-gw.env` is a byte-for-byte copy.

## Why this matters

- Today: every new env var requires a quick task (`260531-t8a` proved this) to update the init registry, or existing operators silently miss the new key.
- After this feature: a single edit to `.env.otto-gw.example` covers all installs. Operators run `upgrade-env` when they want the new defaults. No registry to keep in sync, no env-merge edge cases.

## Open questions for the planner (must answer)

1. Where does the wrapper find the installed template? In dev it's `scripts/.env.otto-gw.example` (sibling to the wrapper). In a shipped dist tarball it's `<install-root>/scripts/.env.otto-gw.example`. The planner should specify the resolution logic.
2. How does the wrapper detect "single-file model in use" for the warn-on-boot deprecation message? Suggested: presence of any uncommented operator-set value (auth_token, pii mode, etc.) in `.otto-gw.env` AND absence of `.otto-gw.overrides.env`.
3. Init's secret-writing path: should `init` always write secrets to the overrides file going forward, even for fresh installs? OR only when overrides.env already exists? Lean toward "always write to overrides for secrets" so the generated `.otto-gw.env` is a pure template copy.
