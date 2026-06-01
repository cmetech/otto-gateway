---
id: 260531-t8a
title: Add STREAM_IDLE_TIMEOUT_SEC to wrapper init registry (bash + ps1)
date: 2026-06-01
type: quick
status: complete
---

# Summary — 260531-t8a

Single atomic commit closes the install-flow gap from 260531-ruv: STREAM_IDLE_TIMEOUT_SEC was shipped end-to-end but the `init` flow's `set_env_line` registry didn't know about it. New installs worked (template-copy path), but `init --force` re-init had no record of the key, so any operator who deleted the comment line couldn't get it back.

## Changes

### `scripts/otto-gw` (bash)

- Added `--idle-timeout SEC` / `--idle-timeout=SEC` to the init flag parser (`arg_idle_timeout`).
- Added `existing_idle_timeout` local + extraction via `extract_env_any "$dest" STREAM_IDLE_TIMEOUT_SEC` inside the `reinit=1` block, mirroring the AUTH_TOKEN / PII_HASH_KEY / CHAT_TRACE patterns.
- Added resolution block after CHAT_TRACE resolution: precedence is CLI flag > existing-file value > default `30` commented.
- Added `set_env_line "$dest" STREAM_IDLE_TIMEOUT_SEC "$idle_timeout_value" "$idle_timeout_commented"` after the CHAT_TRACE line in `init`.
- Added `STREAM_IDLE_TIMEOUT_SEC` to the `print_env` for-key list so `otto-gw env` surfaces the resolved value.
- Added `--idle-timeout SEC` to the init flags block of the usage help.

### `scripts/otto-gw.ps1` (PowerShell)

- `[int]$IdleTimeout = -1` already exists in the script-level param block (added by 260531-ruv).
- Added `$existingIdleTimeout` to the existing-extract block, with `Test-EnvKeyUncommented` + `$existing['STREAM_IDLE_TIMEOUT_SEC']` (mirrors the CHAT_TRACE pattern).
- Added resolution block after `$chatValue` assignment: precedence is `-IdleTimeout >= 0` > existing-file value > default `30` commented.
- Added `Set-EnvLine -FilePath $destPath -Key 'STREAM_IDLE_TIMEOUT_SEC' -Value $idleValue -Commented $idleCommented` after the CHAT_TRACE Set-EnvLine.

## Resolution semantics

| Input | Written line | Rationale |
|---|---|---|
| `--idle-timeout SEC` (CLI) | `STREAM_IDLE_TIMEOUT_SEC=SEC` uncommented | Explicit operator intent |
| existing file has uncommented value | `STREAM_IDLE_TIMEOUT_SEC=N` uncommented | Preserve prior tuning on re-init |
| no flag, no existing value | `# STREAM_IDLE_TIMEOUT_SEC=30` commented | Default discoverable; binary fallback of 30s still applies |

## Verification

- `shellcheck scripts/otto-gw` — clean
- `bash -n scripts/otto-gw` — clean
- Smoke test 1 (default): `init --non-interactive --force` writes `# STREAM_IDLE_TIMEOUT_SEC=30` ✓
- Smoke test 2 (CLI flag): `init --idle-timeout 60 --non-interactive --force` writes `STREAM_IDLE_TIMEOUT_SEC=60` ✓
- Smoke test 3 (re-init idempotency): first run with `--idle-timeout 45`, second run without the flag — file still contains `STREAM_IDLE_TIMEOUT_SEC=45` (preserved) ✓
- PowerShell static review only (mirrors bash signatures exactly); no parse verification on macOS dev box (260531-oax caveat carried forward).

## Out of scope (deliberate)

- env-merge-on-upgrade command (`otto-gw upgrade-env`) — a separate proper phase, not a quick task. Reads template, diffs against user .env, prompts to add missing keys while preserving existing values.
- Tests — the env-write surface is exercised by manual install testing; the gateway-side env parse test already exists in 260531-ruv.

## Next

Cut v1.6.10 and publish to MinIO.
