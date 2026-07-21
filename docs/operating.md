# Operating Gateway

This document covers the developer-laptop lifecycle for Gateway:
starting and stopping the gateway in the background, where PID and log
files live, env-var overrides for the wrapper scripts, and how the
`status` subcommand determines whether the gateway is healthy.

The Go binary is a single foreground process. Two wrapper scripts own
process supervision on developer laptops — the binary itself has no
`start`/`stop` subcommands. See
[`scripts/gw`](../scripts/gw) (POSIX) and
[`scripts/gw.ps1`](../scripts/gw.ps1) (PowerShell).

## Quick Start (macOS / Linux)

```bash
make build               # compile bin/gateway

./scripts/gw start   # launch in background
./scripts/gw status  # check PID + /health
./scripts/gw stop    # send SIGTERM, wait for exit
```

Makefile shortcuts delegate to the same script:

```bash
make start
make status
make stop
```

## Quick Start (Windows)

If you extracted a release tarball/zip, double-click `scripts\setup.bat`
once to strip Mark-of-the-Web from the unpacked files and set the
`CurrentUser` PowerShell execution policy to `RemoteSigned` — after that,
PowerShell scripts run without any per-invocation bypass.

```powershell
.\scripts\setup.bat              # one-time, post-extract

.\scripts\gw.ps1 start      # PowerShell wrapper
.\scripts\gw.ps1 status
.\scripts\gw.ps1 stop
```

Three equivalent surfaces ship in every release archive:

- `.\scripts\gw.ps1 <cmd>` — PowerShell wrapper (subcommands: `init`, `start`, `stop`, `status`, `restart`, `logs`, `run`, `env`, `version`).
- `.\scripts\gw.bat <cmd>` — cmd.exe dispatcher. Mirrors the PowerShell wrapper's subcommand surface and passes `-ExecutionPolicy Bypass -File` internally, so it works on a fresh extract even before `setup.bat` has run.
- `.\scripts\start.bat`, `stop.bat`, `status.bat` — Explorer-double-clickable per-command shortcuts that delegate to the dispatcher.

If your organization locks PowerShell `ExecutionPolicy` at the `LocalMachine` or `MachinePolicy` scope via Group Policy, those scopes override anything `setup.bat` writes to `CurrentUser`. The `.bat` dispatcher already uses a per-invocation bypass internally, so it continues to work in Group-Policy-locked environments without any further intervention. As a manual fallback, you can also invoke the PowerShell wrapper directly:
`powershell -ExecutionPolicy Bypass -File .\scripts\gw.ps1 start`

For dev builds (running from a `make build` working tree, not a release
archive), the `setup.bat` step is unnecessary — only release-archive
extracts get tagged with Mark-of-the-Web.

## Auth posture

### Auth posture quick reference

Gateway enforces bearer-token auth via `AuthHook` on all model-execution
routes. Two routes are exempt by intentional design; document them here so
operators don't surface them on untrusted networks.

- **`/admin` UI is auth-exempt by design** (Phase 6.1 D-01). Bind the gateway
  to localhost or front with reverse-proxy auth in production. See
  `### v1 no-auth posture` below (under `## Admin Observability UI`) for full
  rationale and operator guidance.
- **Ollama list-mode stubs bypass `AuthHook`** — `/api/tags`, `/api/ps`,
  `/api/show`, `/api/copy`, `/api/delete`, `/api/pull`, `/api/push`,
  `/api/create` — because they don't route through the canonical engine. The
  IP allowlist (`ALLOWED_IPS`) still applies. Accepted v1 risk — see
  `#### Accepted v1 risks` below (under `### Phase 8 — Plugin chain (hooks)`).

**Do not expose `:11434/admin` or the Ollama list-mode endpoints on untrusted
networks without operator-side mitigation.**

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `start` | Launch gateway in the background; write PID file; append stdout (and stderr on Windows) to log files |
| `stop` | Send SIGTERM (macOS/Linux) or `Kill()` (Windows); wait up to 10 s for clean exit; remove PID file |
| `status` | Check PID file exists, verify process is alive, then GET `/health` and print JSON response |
| `restart` | `stop` then `start` — race-free because `stop` waits for process exit before returning |
| `logs [-f]` | Tail the log file; pass `-f` to follow (macOS/Linux); Windows tails both stdout and stderr files simultaneously |
| `run` | Run gateway in the foreground — equivalent to invoking the binary directly |
| `env` | Print the resolved gateway env (the keys that would be passed to the binary). Secrets are masked by default; pass `--show-secrets` (bash) or `-ShowSecrets` (PowerShell) to print literals. |

## Gateway config flags (flags + .env)

The wrapper script accepts a small set of high-leverage flags so you don't
have to remember the underlying env-var names. Flags are valid on `start`,
`restart`, `run`, and `env`.

| Flag (bash) | Flag (PowerShell) | Underlying env | Notes |
|-------------|-------------------|----------------|-------|
| `--pii MODE` | `-Pii MODE` | `PII_REDACTION_ENABLED` + `PII_REDACTION_MODE` | `MODE` ∈ `off,replace,mask,hash,drop,encrypt`. `off` sets `PII_REDACTION_ENABLED=false`; any other mode sets `=true` and the mode. |
| `--hash-key KEY` | `-HashKey KEY` | `PII_HASH_KEY` | Required when `--pii hash` (boot error otherwise). |
| `--entities LIST` | `-Entities LIST` | `PII_ENABLED_ENTITIES` | Comma list. Empty = all registered recognizers. Accepts the six original + seven telecom (`SIP_URI,IMEI,IMSI,MSISDN,MAC_ADDRESS,COORDINATES,SITE`) + two NER (`PERSON,LOCATION`, requires `PII_NER_ENABLED=true` to fire). |
| `--hooks LIST` | `-Hooks LIST` | `ENABLED_HOOKS` | Allowlist; empty = all hooks. |
| `--auth TOKEN` | `-Auth TOKEN` | `AUTH_TOKEN` | Comma-separated for rotation. |
| `--env-file PATH` | `-EnvFile PATH` | _(loader)_ | Override the .env search. |

**.env auto-load** — the wrapper looks for the first match of:

1. `--env-file PATH` / `-EnvFile PATH` (CLI override)
2. `$GW_ENV_FILE` (env override)
3. `./.env` (project-local)
4. `$GW_HOME/.env` (per-user; default `$HOME/.gw/.env`, or `$env:USERPROFILE\.gw\.env` on Windows)

An `overrides.env` file chains on top the same way (`./overrides.env` →
`$GW_HOME/overrides.env`) and loads SECOND, so its values win on any shared
key — this is the operator-owned layer for secrets/customizations that
survives `gw upgrade-env` / `gw init --force` untouched.

If a match is found it is sourced before the binary starts. Format is the
standard `KEY=value` per line; `#` comments and blank lines are skipped;
`export KEY=value` is also tolerated. A template lives at
`scripts/.env.example` — copy it and uncomment the lines you need.

**Precedence (highest first):** CLI flag → .env file → inherited shell env.
The .env loader only sets keys it actually contains; anything you already
exported in the shell is preserved unless the .env overrides it, and
anything you pass via `--pii` / `--hash-key` / etc. wins over both.

**Examples:**

```bash
# Verify what would be passed to the gateway, no launch.
./scripts/gw env

# Enable hash-mode PII with a fresh key, restart.
./scripts/gw restart --pii hash --hash-key "$(openssl rand -hex 32)"

# Run in foreground, filter the chain to RequestID + Logging only.
./scripts/gw run --hooks RequestIDHook,LoggingHook

# Use a project-local .env (committed) plus override one knob.
./scripts/gw start --env-file ./deploy/local.env --pii replace
```

```powershell
.\scripts\gw.ps1 env
.\scripts\gw.ps1 restart -Pii hash -HashKey (-join ((48..57) + (97..102) | Get-Random -Count 64 | % {[char]$_}))
.\scripts\gw.ps1 run -Hooks "RequestIDHook,LoggingHook"
```

## File Locations

Layout is split across two anchors, resolved independently (see
`scripts/gw` header comments):

- **`GW_INSTALL_DIR`** — code (binary, wrapper scripts). Replaceable on
  upgrade. Defaults to the directory containing the wrapper — i.e. the
  extracted `otto_gateway/` folder when running from a release tarball/zip,
  or the repo root after `make build`. The one-liner installer relocates
  this to a per-OS default: `~/Library/Application Support/Gateway`
  (macOS), `${XDG_DATA_HOME:-~/.local/share}/gateway` (Linux),
  `%LOCALAPPDATA%\Gateway` (Windows).
- **`GW_HOME`** — config + runtime state (`.env`, `overrides.env`, logs,
  PID). Precious; never overwritten by an upgrade. Default `$HOME/.gw`
  (macOS/Linux) or `$env:USERPROFILE\.gw` (Windows), regardless of where
  `GW_INSTALL_DIR` points — this is what lets you replace the binary/scripts
  wholesale on upgrade without touching config.

| File | macOS / Linux default | Windows default |
|------|-----------------------|-----------------|
| Binary | `$GW_INSTALL_DIR/bin/gateway` | `$GW_INSTALL_DIR\bin\gateway.exe` |
| PID file | `$GW_HOME/state/gateway.pid` | `$GwHome\state\gateway.pid` |
| Structured log (rotated) | `$GW_HOME/logs/gateway.log` | `$GwHome\logs\gateway.log` |
| Rotated backups | `$GW_HOME/logs/gateway-<timestamp>.log.gz` | `$GwHome\logs\gateway-<timestamp>.log.gz` |
| Boot/crash sidecar | `$GW_HOME/logs/gateway-boot.log` (stdout+stderr) | `$GwHome\logs\gateway.boot-out.log` + `.boot-err.log` |

The gateway owns the structured log file directly via timberjack
(daily rotation, 7-day retention, gzip). The boot sidecar captures
only pre-logger output and stderr (kiro-cli subprocess + Go panics) —
it's small and rarely consulted, but invaluable on incident.

### Legacy `~/.otto-gw` migration

Upgrading from a pre-relayout install (`~/.otto-gw/`, wrapper named
`otto-gw`)? The installer (and the wrapper's own `load_config` path) runs a
one-time, idempotent migration: `~/.otto-gw.env` (or the legacy
`~/.otto-gw/.env.otto-gw` fallback path) moves to `$GW_HOME/.env`,
`~/.otto-gw.overrides.env` moves to `$GW_HOME/overrides.env`, and
`~/.otto-gw/tray.json` moves to `$GW_HOME/tray.json` — a `mv`, not a `cp`,
so secrets like `AUTH_TOKEN` aren't left behind in two places. It is a
no-op once `$GW_HOME/.env` already exists, so it's safe to run on every
invocation. The legacy code directory `~/.otto-gw/` itself (binary,
scripts) is never touched or deleted — only those specific config files are
read out of it.

## Environment Variable Overrides

These variables control the wrapper scripts. Set them in your shell,
your `.env` / `overrides.env` (see `GW_HOME` above), or via wrapper flags
(see *Gateway config flags* above).

| Variable | Default | Description |
|----------|---------|-------------|
| `GW_INSTALL_DIR` | Directory containing the wrapper script | Code anchor — binary + scripts. Replaceable on upgrade. |
| `GW_HOME` | `$HOME/.gw` (macOS/Linux) / `$env:USERPROFILE\.gw` (Windows) | Config anchor — `.env`, `overrides.env`, `logs/`, `state/`. Precious; never overwritten. |
| `GW_BIN` | `$GW_INSTALL_DIR/bin/gateway` (macOS/Linux) / `$GW_INSTALL_DIR\bin\gateway.exe` (Windows) | Path to the gateway binary |
| `GW_PID` | `$GW_HOME/state/gateway.pid` | Full PID file path (overrides `GW_STATE_DIR` if both are set). |
| `GW_STATE_DIR` | `$GW_HOME/state` | Directory the wrapper uses for runtime state. PID file lives inside. Override to relocate state elsewhere. |
| `GW_LOG` | `$GW_HOME/logs/gateway.log` | Structured log file path. Auto-exported to the binary as `LOG_FILE` for daily timberjack rotation. |
| `GW_LOG_BOOT` | `${GW_LOG%.log}-boot.log` (POSIX) | Boot/crash sidecar that captures the gateway's stderr (kiro-cli + panics). |
| `GW_LOGOUT` / `GW_LOGERR` | `<log>.boot-out.log` / `<log>.boot-err.log` (Windows) | Boot sidecars (Windows requires separate stdout / stderr files). |
| `GW_ADDR` | `http://localhost:18080` | Gateway address used by the `status` subcommand for the `/health` probe. |
| `GW_ENV_FILE` / `GW_OVERRIDES_FILE` | _(unset — falls back to the search order above)_ | Override the resolved `.env` / `overrides.env` path. |

Example — point logs at a project-specific directory:

```bash
export GW_LOG=~/Projects/otto/gateway.log
export GW_PID=~/Projects/otto/gateway.pid
./scripts/gw start
```

## Gateway Environment Variables

Reference for what the gateway binary itself accepts. For day-to-day
laptop use, prefer the wrapper flags + `.env` / `overrides.env` described
above — this table is the underlying contract every knob maps to.

| Variable | Default | Description |
|----------|---------|-------------|
| `HTTP_ADDR` | `127.0.0.1:18080` | Bind address for the HTTP server (e.g., `:18080` to bind all interfaces, or `127.0.0.1:11434` to take over the Ollama port) |
| `KIRO_CMD` | `kiro-cli` | kiro-cli binary name or full path. If unset, the gateway starts without ACP worker processes. |
| `KIRO_ARGS` | `acp` | Arguments passed to kiro-cli (space-separated) |
| `KIRO_CWD` | _(empty)_ | Default working directory for kiro-cli subprocesses |
| `DEBUG` | `false` | Enable debug-level JSON logging. Accepts `1`, `true`, `0`, or `false`. |
| `PING_INTERVAL` | `60000` | ACP ping interval. Default: 60 s. Integer values are treated as milliseconds (e.g., `60000` = 60 s); Go duration strings are also accepted (e.g., `"90s"`, `"2m"`). |

### Phase 8 — Plugin chain (hooks)

Phase 8 ships five canonical-layer hooks (RequestIDHook, AuthHook,
JSONFormatSteeringHook, PIIRedactionHook, LoggingHook) wired into a
hardcoded chain in `cmd/otto-gateway/main.go`. The chain runs on
every request that reaches the engine, in registration order:
`RequestID → Auth → JSONFormatSteering → PII → Logging` (Pre), with
LoggingHook also on Post for timing + structured exit records.

| Variable | Default | Description |
|----------|---------|-------------|
| `ENABLED_HOOKS` | _(empty = all enabled)_ | Comma-split allowlist of hook type names enabled at boot. Default empty means every hook in `main.go`'s slice runs (default-permissive, matches `AUTH_TOKEN` semantics). A name that does not match any registered hook causes the gateway to **refuse to start** with stderr/stdout containing `unknown hook: "<name>"`. Typo-fail-fast — `ENABLED_HOOKS=PIIRedaction` (missing the `Hook` suffix) would silently disable PII redaction; the boot error prevents this. **Registration order is preserved**: `ENABLED_HOOKS=LoggingHook,RequestIDHook` runs as `[RequestIDHook, LoggingHook]`, not the allowlist order. |
| `JSON_FORMAT_STEERING_ENABLED` | `true` | Master switch for `JSONFormatSteeringHook`. When `true` (default), the hook appends verbatim GEN_RULES text to `req.System` on any Ollama request carrying `format:"json"` or a JSON-schema object — steering the model to emit raw JSON without markdown fences. Node-shim parity: the original Node proxy applied this unconditionally; the gateway default mirrors that behaviour. Set `false` to disable globally. The hook is still visible via `GET /health/hooks` when disabled. |
| `PII_REDACTION_ENABLED` | `true` | Master switch for `PIIRedactionHook`. Default `true` — redaction is on out of the box (secure-by-default). Set `PII_REDACTION_ENABLED=false` to opt out. Two-knob composition with `ENABLED_HOOKS`: `ENABLED_HOOKS` controls whether the hook is in the chain at all; `PII_REDACTION_ENABLED` controls whether it does work when invoked. |
| `PII_ENABLED_ENTITIES` | _(empty = all active)_ | Comma-split list of recognizer names. Default empty = all registered recognizers active. Allowed names: regex — `Email`, `IPv4`, `IPv6`, `SSN`, `CreditCard`, `USPhone`, `SIP_URI`, `IMEI`, `IMSI`, `MSISDN`, `MAC_ADDRESS`, `COORDINATES`, `SITE`; NER (requires `PII_NER_ENABLED=true`) — `PERSON`, `LOCATION`. Unknown names → boot error. Context-anchored recognizers (`IMEI`, `IMSI`, `MSISDN`, `SITE`) require a recognizer-specific keyword within ±50 bytes of the match. |
| `PII_REDACTION_MODE` | `encrypt` | One of `replace`, `mask`, `hash`, `drop`, `encrypt`. Default `encrypt`: PII is replaced with `[PII:EMAIL:base64url]` AES-256-GCM ciphertext before the worker sees the request, and the response Post-hook decrypts those tokens back to plaintext before the client sees the response (round-trip). Other modes: `replace` substitutes `[EMAIL_N]` tokens with a per-canonical-value counter; `mask` substitutes partial obfuscation (e.g., `co***@cm***.io`); `hash` substitutes `[EMAIL:h-XXXXXXXX]` with the first 8 hex chars of `HMAC-SHA256(PII_HASH_KEY, canonical(value))`; `drop` substitutes an empty string. Unknown values → boot error. |
| `PII_HASH_KEY` | _(empty)_ | HMAC-SHA256 key for `PII_REDACTION_MODE=hash`. **Required when mode is `hash`** — boot error otherwise (no silent unkeyed-HMAC fallback). Rotating this key invalidates prior correlation tokens — feature, not a bug: rotate to break attacker correlation if a key leak is suspected. |
| `PII_ENCRYPT_KEY` | _(empty, but install scripts auto-seed)_ | Key for `PII_REDACTION_MODE=encrypt` (the default) or any per-entity encrypt override via `PII_ENTITY_ACTIONS`. Accepts **any non-empty string** — the gateway derives a 32-byte AES-256-GCM key via SHA-256 at boot. **Required when encrypt is active anywhere** — boot error otherwise (no silent fallback). `gw init` auto-mints this alongside `AUTH_TOKEN` and `PII_HASH_KEY`, and `--regenerate-secrets` / `-RegenerateSecrets` rotates all three. Rotating invalidates prior round-trip tokens (in-flight chat history affected; new requests after restart use the new key). |
| `PII_ENTITY_ACTIONS` | _(empty)_ | Per-entity action overrides. Shape: `Entity:action,Entity:action,...` e.g. `Email:encrypt,SSN:drop,PERSON:mask`. When non-empty, the listed entities use the specified action instead of the global `PII_REDACTION_MODE`. Unlisted entities fall back to the global mode. Allowed entity names: see `PII_ENABLED_ENTITIES`. Allowed actions: `replace`, `mask`, `hash`, `drop`, `encrypt`. Unknown entity names or unknown action values → boot error. |
| `PII_NER_ENABLED` | `true` | Master switch for the `jdkato/prose/v2` NER engine that emits `PERSON` and `LOCATION` spans alongside the regex recognizers. Default `true` (secure-by-default). Set `PII_NER_ENABLED=false` to skip the prose model load — the binary impact (~7 MB, 10 MB → 17 MB) is baked into the build either way; this flag controls the runtime tokenizer/tagger allocation. English-only; accuracy is decent on common Western names and major place names but weaker on Asian / multilingual names — see `docs/superpowers/specs/2026-06-01-pii-encrypt-design.md` §11.2 for the documented accuracy ceiling. |

#### Restart-to-apply rule (SC7)

Hook configuration is read-only at runtime. There is **no admin
endpoint** to mutate hooks, env vars, or chain composition. Restart
the gateway to change configuration. The introspection endpoint
`GET /health/hooks` shows the registered chain in registration order
so operators can confirm policy.

```bash
curl -s http://localhost:18080/health/hooks | jq
```

The response shape (single flat `hooks` array, registration order;
LoggingHook appears once with `kind: "Pre,Post"` per the dedup
convention):

```json
{
  "hooks": [
    {"name": "RequestIDHook", "kind": "Pre", "enabled": true, "config": {...}},
    {"name": "AuthHook", "kind": "Pre", "enabled": true, "config": {"token_count": 1}},
    {"name": "JSONFormatSteeringHook", "kind": "Pre", "enabled": true, "config": {"enabled": true, "default_on": true}},
    {"name": "PIIRedactionHook", "kind": "Pre", "enabled": true, "config": {"enabled": false, "mode": "replace", "entities": [...]}},
    {"name": "LoggingHook", "kind": "Pre,Post", "enabled": true, "config": {"level": "INFO"}}
  ]
}
```

The `config` field is a whitelist — it NEVER contains secrets (no
`AUTH_TOKEN` value, no `PII_HASH_KEY` value, no recognizer regex
sources). This is enforced by each hook's `Describe()` method and
audited end-to-end by `tests/e2e/plugin_chain_test.go`.

#### Boot-error refusal conditions

These conditions cause the gateway to exit non-zero before the
listener accepts:

- `ENABLED_HOOKS` contains a name that does not match any registered
  hook → stderr/stdout contains `unknown hook`.
- `PII_ENABLED_ENTITIES` contains an unknown entity name → boot
  error names `PII_ENABLED_ENTITIES` and the offender.
- `PII_REDACTION_MODE` is not one of `replace`, `mask`, `hash`,
  `drop`, `encrypt` → boot error names `PII_REDACTION_MODE` and the offender.
- `PII_REDACTION_MODE=hash` AND `PII_HASH_KEY` is empty/unset →
  boot error names `PII_HASH_KEY`. The unkeyed mode is a
  rainbow-table-trivial security trap; the operator must
  explicitly provide a key.
- `PII_ENTITY_ACTIONS` contains an unknown entity name or action
  value → boot error names the offending pair.
- `PII_REDACTION_MODE=encrypt` (or `PII_ENTITY_ACTIONS` contains
  `:encrypt`) AND `PII_ENCRYPT_KEY` is empty → boot error names
  `PII_ENCRYPT_KEY`. There is no silent fallback.

#### Hash-key rotation as a feature

Rotating `PII_HASH_KEY` is the operational tool for breaking
attacker correlation if a log leak is suspected:

```bash
# Day 0
export PII_HASH_KEY="initial-32-byte-key-padding-here!!"
./scripts/gw restart
# logs now show <EMAIL:h-5e114e4d> for corey@cmetech.io

# Day N — leak suspected, rotate
export PII_HASH_KEY="rotated-32-byte-key-padding-here!!"
./scripts/gw restart
# logs now show <EMAIL:h-XXXXXXXX> (different tag) for the same value
```

After rotation, prior tags become non-correlating — the attacker
cannot tie pre- and post-rotation log entries to the same canonical
value. Rotate via your secrets management system on any suspected
leak event.

#### Accepted v1 risks

- **T-8-AUTH-BYPASS (non-engine routes lose bearer-token gating).**
  Phase 8 moved bearer-token validation from the `auth.Bearer` chi
  middleware to `plugin.AuthHook` on the canonical engine chain.
  Non-engine routes (e.g., `/api/tags`, `/api/ps`, `/api/show`,
  `DELETE /v1/sessions/:id`) consequently lose bearer-token gating at
  the server layer. The IP allowlist (`ALLOWED_IPS`) still applies.
  These are read-only catalog stubs / direct-registry operations —
  they do NOT reach the engine and have no PII surface. If your
  threat model requires bearer auth on these endpoints, run a
  patched server-layer middleware in a downstream configuration.
- **T-8-AUTH-BYPASS via `ENABLED_HOOKS` without `AuthHook`.** If you
  set `ENABLED_HOOKS=RequestIDHook,LoggingHook` (deliberately
  excluding AuthHook), bearer authentication is DISABLED even when
  `AUTH_TOKEN` is set. The operator's explicit choice; documented
  here so the implication is clear.

Example — run with a custom binary path and debug logging:

```bash
export KIRO_CMD=~/.local/bin/kiro-cli
export DEBUG=true
./scripts/gw start
```

### Context compression (CompressionHook)

| Env | Default | Meaning |
|---|---|---|
| `COMPRESSION_ENABLED` | `false` | Process-wide default for CompressionHook. Per-request overrides: `X-Compression` header (wins; accepts `1`/`true`/`on` and `0`/`false`/`off`, other values ignored), or a `+compress`/`-compress` model-name suffix (e.g. `qwen-2.5+compress` — for callers like LangFlow that cannot send headers). Caveat: a real model id ending in `-compress` is parsed as a disable directive (no escape syntax). `ENABLED_HOOKS` remains the hard kill switch; explicit allowlists must include `CompressionHook`. |
| `COMPRESS_TRIGGER_TOKENS` | `6000` | Below this estimated transcript size (UTF-8 bytes/4) compression is a no-op. |
| `COMPRESS_BUDGET_TOKENS` | `4000` | Target size. Re-checked between stages: once met, no further (lossier) stage runs; a transcript already at/under budget is never modified. **Best-effort**, not guaranteed: protected-tail/pinned messages and tool-call carriers are never elided, and stage 4 elides nothing when no message shares a token with the question — so a run can end still over budget (counted in `/health/hooks` as `budget_unmet`). Must be <= trigger. |
| `COMPRESS_PROTECT_TAIL` | `4` | The last N messages are never modified. Regardless of this value — even at `0` — the following are never modified: system prompt, tool schemas, tool-call pairing, the current inbound turn (including a trailing tool result on any surface), and the most recent user question. |
| `COMPRESS_TOOL_KEEP` | `1200` | Head+tail bytes kept when middle-truncating stale tool results. Bounded 1..4194304. |

Pipeline: blank-line/trailing-space cleanup → stale tool-result truncation → exact-duplicate collapse → local BM25 lexical relevance pruning against the user's most recent question — with the budget re-checked between stages, so later (lossier) stages are skipped the moment the target is met. Everything runs in-process: no network call, external model, or additional configuration. Stage 1 is **low-loss normalization**, not lossless — it strips trailing whitespace and collapses 3+ blank lines (so exact-output fixtures relying on those bytes are altered), but never rewrites interior whitespace, so code indentation in old messages survives byte-for-byte.

Stage 4 ranks by **exact lexical overlap** (identifiers, error strings, names — not synonyms or paraphrases) and has a hard safety rule: if no eligible message shares a single token with the user's question, nothing is elided — the transcript proceeds over budget rather than pruning blind, and `/health/hooks` counts it under `budget_unmet`. Further limitations to know: machine-generated PII tokens — encrypted `[PII:…]`, hashed `[ENTITY:h-…]`, and numbered `[ENTITY_N]` replacements — are stripped before ranking (they are neither noise nor evidence; a question consisting only of redacted values disables stage 4 for that request, as does a question with more than 4,096 unique terms — stage 4 never ranks on a truncated query). The hash and numbered-replacement grammars are constrained to the gateway's recognized PII entity names, so ordinary bracketed identifiers such as `[ISO_9001]` are not stripped. Bare `[ENTITY]` replacements and masked values are not stripped (indistinguishable from ordinary text). Unsegmented CJK text tokenizes into sentence-sized runs (no word segmentation), so stage 4 is largely inert for such history — stages 1–3 still apply.

Failure posture — precise guarantee: compression never fails a request. Stage 4 performs no I/O, so there is no endpoint-failure path; the hook's panic recovery still guarantees `(nil, nil)`, and an internal panic forwards the request with whatever stages had already completed applied (stages run in place). Observability: `/health/hooks` (config + lifetime counters, including `budget_unmet`), `gw_compress_runs_total` and `gw_compress_tokens_saved_estimate_total` on `/metrics`.

Known interaction: with `PII_REDACTION_MODE=encrypt` (or any per-entity encrypt action), middle-truncation of a stale tool result can clip an embedded ciphertext token, which disables round-trip decryption of that token in the response (the request still succeeds). A boot-time warning is logged whenever encrypt mode is active — even if `COMPRESSION_ENABLED=false` — because the header and model-suffix toggles can enable compression per request.

## How `status` Works

The `status` subcommand combines two checks:

1. **PID file check.** If no PID file exists at `$GW_PID`, the
   gateway is stopped. If the file exists but the process is gone
   (stale PID), `status` reports `stopped (stale PID)` and exits
   non-zero.

2. **Process liveness check.** On macOS/Linux, `kill -0 $pid` probes
   whether the process is alive without sending a signal. On Windows,
   `Get-Process -Id $pid` is used.

3. **Health probe.** If the process is alive, `status` sends
   `GET $GW_ADDR/health` and prints the JSON response. The
   response includes gateway version, uptime seconds, and pool/session/
   embedding stats.

Exit codes: 0 if the gateway is running and health check succeeded;
non-zero if stopped or the PID file is stale.

## Logs

Log format is JSON (`log/slog` with `slog.NewJSONHandler`). Each line
is a single JSON object with keys `time`, `level`, `msg`, and
request-scoped keys (`request_id`, `method`, `path`, `status`,
`duration`).

### File layout

| File | Default path | Role |
|------|-------------|------|
| Structured log | `$GW_HOME/logs/gateway.log` | Daily-rotated JSON (`LOG_FILE` env). The file the admin UI tails and the one operators read. |
| Rotated backups | `$GW_HOME/logs/gateway-2026-05-28T00-00-00.log.gz` | One per day; up to 7 retained; gzip-compressed. |
| Boot/crash sidecar (POSIX) | `$GW_HOME/logs/gateway-boot.log` | Captures stderr — kiro-cli subprocess output, pre-logger errors, Go runtime panics. |
| Boot sidecars (Windows) | `$GwHome\logs\gateway.boot-out.log` + `.boot-err.log` | Same role; Windows requires separate stdout / stderr redirection files. |

Override the structured log path with `GW_LOG` (wrapper) or `LOG_FILE`
(direct binary invocation). The wrapper auto-exports `LOG_FILE=$GW_LOG`
and `mkdir -p $(dirname $GW_LOG)` on `start`.

### Rotation contract

- **Trigger:** `00:00` local time (daily). No size-based rotation in v1.
- **Retention:** 7 days. Files older than 7 days are pruned automatically by timberjack's mill goroutine.
- **Compression:** gzip on the rotated backup.
- **Filename pattern:** `<base>-<timestamp>.<ext>.gz` (timberjack default).
- **Live tail safety:** the admin UI's tailer uses `os.Stat` + `os.SameFile` to detect the inode change on rotation and reopens the new active file at EOF without dropping the connection. Verified by `TestAdmin_TailerSurvivesTimberjackRotate`.

### Viewing logs

```bash
./scripts/gw logs        # last 50 lines of the structured log
./scripts/gw logs -f     # follow the structured log

# Crash diagnostics:
tail -f ~/.gw/logs/gateway-boot.log    # macOS/Linux
```

```powershell
.\scripts\gw.ps1 logs    # follow the structured log (Windows)
# Crash diagnostics — paths printed at start time and visible in `logs` output:
Get-Content -Wait "$env:USERPROFILE\.gw\logs\gateway.boot-err.log"
```

### Direct-binary behavior (no wrapper)

Running `./bin/gateway` without `LOG_FILE` keeps the legacy stdout
JSON behavior — useful for `make run`, ad-hoc dev, and the e2e suite
(which captures stdout). Set `LOG_FILE=~/.gw/logs/gateway.log` to
enable rotation when invoking the binary directly.

### Engine prompt log-line semantics (Phase 8.3)

As of Phase 8.3 (ACP `Prompt()` non-blocking refactor),
`engine.prompt.sent` and `engine.prompt.completed` mark the two ends
of a kiro-cli prompt turn separately:

| Line                       | Fires when                                                                                      |
|----------------------------|-------------------------------------------------------------------------------------------------|
| `engine.prompt.sent`       | The gateway writer goroutine accepts the `session/prompt` payload onto its channel (millisecond latency from `engine.new_session.ok`). |
| `engine.prompt.completed`  | The per-prompt goroutine observes the `session/prompt` response, the close-sentinel, or `ctx.Done()`. Carries `session_id`, `chunks`, `stop_reason`. |

**Operator-facing semantic shift.** Before Phase 8.3 `engine.prompt.sent`
fired only after kiro-cli's full `session/prompt` response landed
(gated on the agent's complete LLM turn — typically 5–30 seconds).
Post-Phase-8.3 it fires within milliseconds of request acceptance.
Dashboards that previously measured end-to-end prompt latency as the
interval between `engine.new_session.ok` and `engine.prompt.sent`
should switch to the interval between `engine.new_session.ok` and
`engine.prompt.completed` to preserve the same measurement.

`stop_reason` on `engine.prompt.completed` distinguishes the terminal
arm so dashboards can attribute non-happy-path turns:

| `stop_reason`     | Meaning                                                                       |
|-------------------|-------------------------------------------------------------------------------|
| (kiro-cli string) | Happy path — raw wire value from the `session/prompt` response (e.g. `end_turn`, `max_tokens`). |
| `ctx_canceled`    | Caller cancelled the request context before kiro-cli responded.               |
| `rpc_error`       | kiro-cli returned a JSON-RPC error.                                           |
| `client_closed`   | The gateway closed the kiro-cli connection while the prompt was in flight.    |

## Admin Observability UI

Gateway ships a dark-mode admin page at `GET /admin` that surfaces
`/health` + `/health/agents` data through a styled HTML/CSS/JS bundle
served from `embed.FS` (single static binary; no external runtime deps).

### Endpoints

| Path | Purpose |
|------|---------|
| `GET /admin` | The HTML page (renders summary strip, pool slots grid, active sessions table, log tail panel) |
| `GET /admin/api/snapshot` | Unified JSON snapshot composing pool + registry detail (polled client-side every 30s) |
| `GET /admin/logs/stream` | SSE stream of new log lines from `GW_LOG` (backfill of last ≤500 lines on connect, then live forward) |
| `GET /admin/static/*` | Embedded CSS + JS assets |
| `GET /admin/api/acp-capture` | Track 0 raw-frame capture ring (see [ACP raw-frame capture](#acp-raw-frame-capture-diagnostic) below) |

### ACP raw-frame capture (diagnostic)

`ACP_CAPTURE=true` enables an in-memory ring of the raw kiro ACP frames the
gateway receives, exposed at `GET /admin/api/acp-capture`. `ACP_CAPTURE_SIZE`
bounds the ring (default 512 frames; per-frame params are truncated to 8 KiB).
**Off by default.** Capture records raw prompt/response content, so treat it as
a diagnostic mode: enable it only to investigate wire behavior. Note that
`/admin` — including this route — is **auth-exempt and not IP-allowlisted**
(Phase 6.1 D-01; the `ALLOWED_IPS` allowlist is applied to the surface routes
and `/metrics`, not to `/admin`). Because the captured frames contain raw
prompt/response content, only enable capture when the gateway is bound to
`localhost` or the port is firewalled to trusted hosts. Frames are never
written to disk by the gateway.

### Runtime toggle (no restart)

Set `ACP_CAPTURE_RUNTIME=true` at startup to permit enabling/disabling capture
from the `/admin` dashboard without a restart. When set:

- The dashboard shows an **ACP Capture (diagnostics)** panel with **Enable/Disable**
  and **Clear** buttons.
- `ACP_CAPTURE` seeds the initial state (capture can start on or off).
- Enabling starts a fresh buffer (auto-clear); disabling keeps the buffer
  readable; Clear purges it on demand. Frames remain memory-only (lost on restart).

`POST /admin/api/acp-capture` with `{"action":"enable"|"disable"|"clear"}` drives
this; it returns **403** unless `ACP_CAPTURE_RUNTIME=true`.

**Security:** this is the admin surface's only state-changing route, and `/admin`
is auth-exempt / not IP-allowlisted. Capture records SENSITIVE prompt/response
content. Only set `ACP_CAPTURE_RUNTIME=true` where `/admin` is localhost or
firewalled. Leave it unset (the default) otherwise — capture is then env-only, as
before, requiring a restart to change.

### GW_LOG dependency

The log tail panel reads from the file pointed at by `GW_LOG`
(defaults to `/tmp/gateway.log`). When the gateway is launched
via `scripts/gw start`, the wrapper redirects stdout/stderr via
shell `>>` to this file, and the admin page's tail panel renders
incoming lines within ~1s.

When the gateway is launched directly via `go run ./cmd/otto-gateway`
or `./bin/gateway` without `GW_LOG` or without shell
redirection, the log file is empty/absent and the tail panel shows
"Waiting for log activity…" indefinitely — this is a graceful
degraded mode, not a failure.

Log rotation: the tailer opens the file read-only with no exclusive
lock and re-opens at EOF when it detects rotation (size shrink OR
inode change via `os.SameFile`). `logrotate` create-and-rename
strategies work; truncation (`> file`) also works. Historical
content is NEVER backfilled on rotation.

### v1 no-auth posture

`/admin` and its sub-routes (`/admin/api/snapshot`, `/admin/logs/stream`)
are auth-exempt in v1, regardless of whether `AUTH_TOKEN` is set.
This is intentional (CONTEXT decision D-01): the operator network
is assumed trusted (localhost / private VPC). Anyone with HTTP
access to the gateway can see pool slot labels, session IDs,
last-used timestamps, and live log lines (which may include
DEBUG-level request paths and headers).

**Deployments outside a trusted network MUST either:**
- Wait for Phase 8 (plugin hook chain), which will gate `/admin/*`
  behind the same `AuthHook` that protects `/v1/*` and `/api/*`.
- Add a reverse-proxy-layer auth shim (nginx `auth_basic`, oauth2-proxy,
  Cloudflare Access, etc.) in front of the gateway.

### Supported browsers

Any modern evergreen browser with `EventSource`, `fetch`, and ES2018+
support: Chrome, Firefox, Safari, Edge (releases from 2019 onwards).
Internet Explorer is NOT supported. No transpilation; no polyfills.

### Remote access

`/admin` listens on the same port as the rest of the gateway
(`:11434` by default). To access from a remote machine without
exposing the port:

```
ssh -L 11434:localhost:11434 user@gateway-host
```

Then visit `http://localhost:11434/admin` in your local browser.

## Pre-commit gate

The repo ships a `pre-commit` framework config at `.pre-commit-config.yaml`
that runs `gofumpt`, `golangci-lint`, `gitleaks`, `shellcheck`, and the
standard whitespace/YAML/JSON/large-file checks against staged files on
every `git commit`. The gate exists to catch formatting and lint
regressions locally — `gofumpt` drift in particular has silently landed
on `main` before, and Phase 11's CI-01 requirement closes that gap by
making the gate a one-command local install.

### Prerequisites

Install the `pre-commit` framework once per machine:

```bash
# macOS (Homebrew):
brew install pre-commit

# Cross-platform (pip / pipx):
pip install pre-commit
# Or, isolated:
pipx install pre-commit
```

Install `gofumpt` once per machine (the hook delegates to whichever
`gofumpt` is on the operator's PATH):

```bash
go install mvdan.cc/gofumpt@latest
```

### Enable the gate

From the repo root, in each fresh clone, run once:

```bash
pre-commit install
```

This writes `.git/hooks/pre-commit` for the current clone. It does not
persist across fresh clones — every contributor runs this command once
per checkout.

### What the gate runs

The hooks declared in `.pre-commit-config.yaml`:

- `gofumpt` — strict Go formatting (delegates to
  `scripts/pre-commit-gofumpt.sh`)
- `golangci-lint` — Go linters
- `gitleaks` — secret scanning
- `shellcheck` — shell-script linting
- `end-of-file-fixer`, `trailing-whitespace`, `check-yaml`,
  `check-json`, `check-merge-conflict`, `check-added-large-files`
  — generic hygiene
- `go-mod-tidy` — ensures `go.mod` / `go.sum` are tidy on Go-file edits

### Run on the whole tree manually

```bash
pre-commit run --all-files            # all hooks against the whole tree
pre-commit run gofumpt --all-files    # just the gofumpt hook
```

### Bypass note

`git commit --no-verify` skips the gate. Using it is discouraged: it
defeats the local CI-01 surface and lets formatting/lint regressions
land. If you need to commit urgently, run `make fmt-check lint` by
hand first and only then bypass.

## Known Limitations

### encrypt + streaming clients (fixed in T-5b)

When `PII_REDACTION_MODE=encrypt` (or any entity is configured for
`encrypt` via `PII_ENTITY_ACTIONS`), the PII Pre hook flips
`req.Stream = false` so the response Post hook can decrypt the
aggregated response before any bytes hit the wire. The three adapter
handlers (Anthropic, OpenAI, Ollama) detect the post-Run
`req.Stream == false` state and re-route through the engine's
`CollectFromRun` aggregated path, rendering via the surface's
non-streaming JSON response shape:

- Anthropic `/v1/messages`: renders via `chatResponseToMessage`
  (single `message` envelope).
- OpenAI `/v1/chat/completions`: renders via
  `chatResponseToCompletion` (single `chat.completion` envelope).
- OpenAI `/v1/completions`: always non-streaming on this surface
  (`stream:true` is silently downgraded) — no T-5b re-route needed.
- Ollama `/api/chat`: renders via `chatResponseToWire` (single
  Ollama response object with `done:true`, not an NDJSON record
  stream).
- Ollama `/api/generate`: renders via `generateResponseToWire`
  (single Ollama generate response object).

Streaming clients (Pi-SDK chat CLI, loop24-client via
`ANTHROPIC_BASE_URL`, LangFlow flows that set `stream: true`)
receive a single complete decrypted JSON response when encrypt mode
is active, instead of the streaming SSE/NDJSON they would normally
get. Total wall-clock latency is unchanged (the ACP session runs the
same way) but the response shape switches from streamed to buffered.

**Known limitation: Anthropic `tool_use` rendering on the
encrypt re-route path.** When encrypt mode is active and the
Anthropic surface re-routes a streaming request through the
aggregated path, the response is rendered via the generic engine
aggregator (`CollectFromRun`), NOT via the Anthropic-local
`CollectAnthropicChat` aggregator that handles native `tool_use`
chunks. Plain-text assistant responses round-trip correctly. Native
Anthropic `tool_use` content blocks are not aggregated on this path
— kiro-native `ChunkKindToolCall` chunks render as `[tool: <name>]`
narration text in the assistant message body instead of as discrete
`tool_use` blocks. This is a v1 limitation; clients that require
`tool_use` rendering on the encrypt path can disable encrypt for
those workflows or rely on the non-streaming Anthropic path which
uses `CollectAnthropicChat` natively.

### encrypt mode decrypt WARN volume

The Post-hook decrypt sweep emits one `pii.decrypt.failed` WARN per
malformed token (e.g., when an LLM mangles a ciphertext blob). For a
response echoing many corrupted tokens, the log volume could be high.

**Operational note (encrypt mode):** Decrypt failures emit one
`pii.decrypt.failed` WARN per token (with a `reason` attr — one of
`bad_base64`, `payload_too_short`, `gcm_open`, `decrypt_other`).
Operators triaging unexpected WARN volume can filter by `reason` to
distinguish LLM text mangling (`bad_base64`, `gcm_open`) from key
rotation / corruption (`gcm_open`, `payload_too_short`).
