# Operating otto-gateway

This document covers the developer-laptop lifecycle for otto-gateway:
starting and stopping the gateway in the background, where PID and log
files live, env-var overrides for the wrapper scripts, and how the
`status` subcommand determines whether the gateway is healthy.

The Go binary is a single foreground process. Two wrapper scripts own
process supervision on developer laptops — the binary itself has no
`start`/`stop` subcommands. See
[`scripts/otto-gw`](../scripts/otto-gw) (POSIX) and
[`scripts/otto-gw.ps1`](../scripts/otto-gw.ps1) (PowerShell).

## Quick Start (macOS / Linux)

```bash
make build               # compile bin/otto-gateway

./scripts/otto-gw start   # launch in background
./scripts/otto-gw status  # check PID + /health
./scripts/otto-gw stop    # send SIGTERM, wait for exit
```

Makefile shortcuts delegate to the same script:

```bash
make start
make status
make stop
```

## Quick Start (Windows)

```powershell
make build

.\scripts\otto-gw.ps1 start
.\scripts\otto-gw.ps1 status
.\scripts\otto-gw.ps1 stop
```

If PowerShell blocks execution due to execution policy, run via:
`powershell -ExecutionPolicy Bypass -File .\scripts\otto-gw.ps1 start`

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
| `--pii MODE` | `-Pii MODE` | `PII_REDACTION_ENABLED` + `PII_REDACTION_MODE` | `MODE` ∈ `off,replace,mask,hash,drop`. `off` sets `PII_REDACTION_ENABLED=false`; any other mode sets `=true` and the mode. |
| `--hash-key KEY` | `-HashKey KEY` | `PII_HASH_KEY` | Required when `--pii hash` (boot error otherwise). |
| `--entities LIST` | `-Entities LIST` | `PII_ENABLED_ENTITIES` | Comma list. Empty = all six recognizers. |
| `--hooks LIST` | `-Hooks LIST` | `ENABLED_HOOKS` | Allowlist; empty = all hooks. |
| `--auth TOKEN` | `-Auth TOKEN` | `AUTH_TOKEN` | Comma-separated for rotation. |
| `--env-file PATH` | `-EnvFile PATH` | _(loader)_ | Override the .env search. |

**.env auto-load** — the wrapper looks for the first match of:

1. `--env-file PATH` / `-EnvFile PATH` (CLI override)
2. `$OTTO_ENV_FILE` (env override)
3. `./.env.otto-gw` (project-local)
4. `$HOME/.otto-gw.env` (per-user; `$env:USERPROFILE\.otto-gw.env` on Windows)

If a match is found it is sourced before the binary starts. Format is the
standard `KEY=value` per line; `#` comments and blank lines are skipped;
`export KEY=value` is also tolerated. A template lives at
`scripts/.env.otto-gw.example` — copy it and uncomment the lines you need.

**Precedence (highest first):** CLI flag → .env file → inherited shell env.
The .env loader only sets keys it actually contains; anything you already
exported in the shell is preserved unless the .env overrides it, and
anything you pass via `--pii` / `--hash-key` / etc. wins over both.

**Examples:**

```bash
# Verify what would be passed to the gateway, no launch.
./scripts/otto-gw env

# Enable hash-mode PII with a fresh key, restart.
./scripts/otto-gw restart --pii hash --hash-key "$(openssl rand -hex 32)"

# Run in foreground, filter the chain to RequestID + Logging only.
./scripts/otto-gw run --hooks RequestIDHook,LoggingHook

# Use a project-local .env (committed) plus override one knob.
./scripts/otto-gw start --env-file ./deploy/local.env --pii replace
```

```powershell
.\scripts\otto-gw.ps1 env
.\scripts\otto-gw.ps1 restart -Pii hash -HashKey (-join ((48..57) + (97..102) | Get-Random -Count 64 | % {[char]$_}))
.\scripts\otto-gw.ps1 run -Hooks "RequestIDHook,LoggingHook"
```

## File Locations

The defaults match the packaged distribution layout (`otto_gateway/`
extracted from the release tarball/zip). All paths are project-local so
the same `./scripts/otto-gw` call works from the package root regardless
of OS.

| File | macOS / Linux default | Windows default |
|------|-----------------------|-----------------|
| Binary | `./bin/otto-gateway` | `.\bin\otto-gateway.exe` |
| PID file | `./.otto/gw/otto-gateway.pid` | `.\.otto\gw\otto-gateway.pid` |
| Structured log (rotated) | `./logs/otto-gateway.log` | `.\logs\otto-gateway.log` |
| Rotated backups | `./logs/otto-gateway-<timestamp>.log.gz` | `.\logs\otto-gateway-<timestamp>.log.gz` |
| Boot/crash sidecar | `./logs/otto-gateway-boot.log` (stdout+stderr) | `.\logs\otto-gateway.boot-out.log` + `.boot-err.log` |

The gateway owns the structured log file directly via timberjack
(daily rotation, 7-day retention, gzip). The boot sidecar captures
only pre-logger output and stderr (kiro-cli subprocess + Go panics) —
it's small and rarely consulted, but invaluable on incident.

## Environment Variable Overrides

These variables control the wrapper scripts. Set them in your shell,
your `.env.otto-gw`, or via wrapper flags (see *Gateway config flags*
above).

| Variable | Default | Description |
|----------|---------|-------------|
| `OTTO_BIN` | `./bin/otto-gateway` (macOS/Linux) / `.\bin\otto-gateway.exe` (Windows) | Path to the gateway binary |
| `OTTO_PID` | `./.otto/gw/otto-gateway.pid` | Full PID file path (overrides `OTTO_STATE_DIR` if both are set). |
| `OTTO_STATE_DIR` | `./.otto/gw` | Directory the wrapper uses for runtime state. PID file lives inside; nested under `.otto/` to share the namespace with the OTTER client without colliding (subdir = `gw/`). Override to relocate state outside the project (e.g., `$HOME/.local/state/otto-gw` for FHS-friendly setups). |
| `OTTO_LOG` | `./logs/otto-gateway.log` | Structured log file path. Auto-exported to the binary as `LOG_FILE` for daily timberjack rotation. |
| `OTTO_LOG_BOOT` | `${OTTO_LOG%.log}-boot.log` (POSIX) | Boot/crash sidecar that captures the gateway's stderr (kiro-cli + panics). |
| `OTTO_LOGOUT` / `OTTO_LOGERR` | `<log>.boot-out.log` / `<log>.boot-err.log` (Windows) | Boot sidecars (Windows requires separate stdout / stderr files). |
| `OTTO_ADDR` | `http://localhost:18080` | Gateway address used by the `status` subcommand for the `/health` probe. |

Example — point logs at a project-specific directory:

```bash
export OTTO_LOG=~/Projects/otto/gateway.log
export OTTO_PID=~/Projects/otto/gateway.pid
./scripts/otto-gw start
```

## Gateway Environment Variables

Reference for what the gateway binary itself accepts. For day-to-day
laptop use, prefer the wrapper flags + `.env.otto-gw` described above —
this table is the underlying contract every knob maps to.

| Variable | Default | Description |
|----------|---------|-------------|
| `HTTP_ADDR` | `127.0.0.1:18080` | Bind address for the HTTP server (e.g., `:18080` to bind all interfaces, or `127.0.0.1:11434` to take over the Ollama port) |
| `KIRO_CMD` | `kiro-cli` | kiro-cli binary name or full path. If unset, the gateway starts without ACP worker processes. |
| `KIRO_ARGS` | `acp` | Arguments passed to kiro-cli (space-separated) |
| `KIRO_CWD` | _(empty)_ | Default working directory for kiro-cli subprocesses |
| `DEBUG` | `false` | Enable debug-level JSON logging. Accepts `1`, `true`, `0`, or `false`. |
| `PING_INTERVAL` | `60000` | ACP ping interval. Default: 60 s. Integer values are treated as milliseconds (e.g., `60000` = 60 s); Go duration strings are also accepted (e.g., `"90s"`, `"2m"`). |

### Phase 8 — Plugin chain (hooks)

Phase 8 ships four canonical-layer hooks (RequestIDHook, AuthHook,
LoggingHook, PIIRedactionHook) wired into a hardcoded chain in
`cmd/otto-gateway/main.go`. The chain runs on every request that
reaches the engine, in registration order:
`RequestID → Auth → PII → Logging` (Pre), with LoggingHook also on
Post for timing + structured exit records.

| Variable | Default | Description |
|----------|---------|-------------|
| `ENABLED_HOOKS` | _(empty = all enabled)_ | Comma-split allowlist of hook type names enabled at boot. Default empty means every hook in `main.go`'s slice runs (default-permissive, matches `AUTH_TOKEN` semantics). A name that does not match any registered hook causes the gateway to **refuse to start** with stderr/stdout containing `unknown hook: "<name>"`. Typo-fail-fast — `ENABLED_HOOKS=PIIRedaction` (missing the `Hook` suffix) would silently disable PII redaction; the boot error prevents this. **Registration order is preserved**: `ENABLED_HOOKS=LoggingHook,RequestIDHook` runs as `[RequestIDHook, LoggingHook]`, not the allowlist order. |
| `PII_REDACTION_ENABLED` | `false` | Master switch for `PIIRedactionHook`. When `false` (default), the hook is present in the chain (visible via `GET /health/hooks`) but is inert — operator must explicitly opt in. Two-knob composition with `ENABLED_HOOKS`: `ENABLED_HOOKS` controls whether the hook is in the chain at all; `PII_REDACTION_ENABLED` controls whether it does work when invoked. |
| `PII_ENABLED_ENTITIES` | _(empty = all six)_ | Comma-split list of recognizer names. Default empty = all six recognizers active (`Email`, `IPv4`, `IPv6`, `SSN`, `CreditCard`, `USPhone`). Unknown names → boot error. |
| `PII_REDACTION_MODE` | `replace` | One of `replace`, `mask`, `hash`, `drop`. `replace` substitutes `<EMAIL_N>` tokens with a per-canonical-value counter; `mask` substitutes a partial obfuscation (e.g., `co***@cm***.io`); `hash` substitutes `<EMAIL:h-XXXXXXXX>` with the first 8 hex chars of `HMAC-SHA256(PII_HASH_KEY, canonical(value))`; `drop` substitutes an empty string. Unknown values → boot error. |
| `PII_HASH_KEY` | _(empty)_ | HMAC-SHA256 key for `PII_REDACTION_MODE=hash`. **Required when mode is `hash`** — boot error otherwise (no silent unkeyed-HMAC fallback). Rotating this key invalidates prior correlation tokens — feature, not a bug: rotate to break attacker correlation if a key leak is suspected. |

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
  `drop` → boot error names `PII_REDACTION_MODE` and the offender.
- `PII_REDACTION_MODE=hash` AND `PII_HASH_KEY` is empty/unset →
  boot error names `PII_HASH_KEY`. The unkeyed mode is a
  rainbow-table-trivial security trap; the operator must
  explicitly provide a key.

#### Hash-key rotation as a feature

Rotating `PII_HASH_KEY` is the operational tool for breaking
attacker correlation if a log leak is suspected:

```bash
# Day 0
export PII_HASH_KEY="initial-32-byte-key-padding-here!!"
./scripts/otto-gw restart
# logs now show <EMAIL:h-5e114e4d> for corey@cmetech.io

# Day N — leak suspected, rotate
export PII_HASH_KEY="rotated-32-byte-key-padding-here!!"
./scripts/otto-gw restart
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
./scripts/otto-gw start
```

## How `status` Works

The `status` subcommand combines two checks:

1. **PID file check.** If no PID file exists at `$OTTO_PID`, the
   gateway is stopped. If the file exists but the process is gone
   (stale PID), `status` reports `stopped (stale PID)` and exits
   non-zero.

2. **Process liveness check.** On macOS/Linux, `kill -0 $pid` probes
   whether the process is alive without sending a signal. On Windows,
   `Get-Process -Id $pid` is used.

3. **Health probe.** If the process is alive, `status` sends
   `GET $OTTO_ADDR/health` and prints the JSON response. The
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
| Structured log | `./logs/otto-gateway.log` | Daily-rotated JSON (`LOG_FILE` env). The file the admin UI tails and the one operators read. |
| Rotated backups | `./logs/otto-gateway-2026-05-28T00-00-00.log.gz` | One per day; up to 7 retained; gzip-compressed. |
| Boot/crash sidecar (POSIX) | `./logs/otto-gateway-boot.log` | Captures stderr — kiro-cli subprocess output, pre-logger errors, Go runtime panics. |
| Boot sidecars (Windows) | `./logs/otto-gateway.boot-out.log` + `.boot-err.log` | Same role; Windows requires separate stdout / stderr redirection files. |

Override the structured log path with `OTTO_LOG` (wrapper) or `LOG_FILE`
(direct binary invocation). The wrapper auto-exports `LOG_FILE=$OTTO_LOG`
and `mkdir -p $(dirname $OTTO_LOG)` on `start`.

### Rotation contract

- **Trigger:** `00:00` local time (daily). No size-based rotation in v1.
- **Retention:** 7 days. Files older than 7 days are pruned automatically by timberjack's mill goroutine.
- **Compression:** gzip on the rotated backup.
- **Filename pattern:** `<base>-<timestamp>.<ext>.gz` (timberjack default).
- **Live tail safety:** the admin UI's tailer uses `os.Stat` + `os.SameFile` to detect the inode change on rotation and reopens the new active file at EOF without dropping the connection. Verified by `TestAdmin_TailerSurvivesTimberjackRotate`.

### Viewing logs

```bash
./scripts/otto-gw logs        # last 50 lines of the structured log
./scripts/otto-gw logs -f     # follow the structured log

# Crash diagnostics:
tail -f ./logs/otto-gateway-boot.log    # macOS/Linux
```

```powershell
.\scripts\otto-gw.ps1 logs    # follow the structured log (Windows)
# Crash diagnostics — paths printed at start time and visible in `logs` output:
Get-Content -Wait .\logs\otto-gateway.boot-err.log
```

### Direct-binary behavior (no wrapper)

Running `./bin/otto-gateway` without `LOG_FILE` keeps the legacy stdout
JSON behavior — useful for `make run`, ad-hoc dev, and the e2e suite
(which captures stdout). Set `LOG_FILE=./logs/otto-gateway.log` to
enable rotation when invoking the binary directly.

## Admin Observability UI

OTTO Gateway ships a dark-mode admin page at `GET /admin` that surfaces
`/health` + `/health/agents` data through a styled HTML/CSS/JS bundle
served from `embed.FS` (single static binary; no external runtime deps).

### Endpoints

| Path | Purpose |
|------|---------|
| `GET /admin` | The HTML page (renders summary strip, pool slots grid, active sessions table, log tail panel) |
| `GET /admin/api/snapshot` | Unified JSON snapshot composing pool + registry detail (polled client-side every 30s) |
| `GET /admin/logs/stream` | SSE stream of new log lines from `OTTO_LOG` (backfill of last ≤500 lines on connect, then live forward) |
| `GET /admin/static/*` | Embedded CSS + JS assets |

### OTTO_LOG dependency

The log tail panel reads from the file pointed at by `OTTO_LOG`
(defaults to `/tmp/otto-gateway.log`). When the gateway is launched
via `scripts/otto-gw start`, the wrapper redirects stdout/stderr via
shell `>>` to this file, and the admin page's tail panel renders
incoming lines within ~1s.

When the gateway is launched directly via `go run ./cmd/otto-gateway`
or `./bin/otto-gateway` without `OTTO_LOG` or without shell
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
