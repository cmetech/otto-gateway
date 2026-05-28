# OTTO Gateway

A single-binary LLM gateway. Exposes OpenAI-, Ollama-, and Anthropic-compatible HTTP APIs on one port and routes every request through a configurable guardrails chain (auth, request-ID, logging, PII redaction) to a pool of `kiro-cli` ACP worker processes.

This README is for **operators running the binary on a laptop**. Developers building the gateway should read the repo's top-level `README.md` and `docs/operating.md` instead.

---

## What's in the box

```
otto_gateway/
  bin/otto-gateway        the gateway binary (single static executable)
  scripts/otto-gw         POSIX wrapper (start | stop | status | restart | logs | run | env)
  scripts/otto-gw.ps1     Windows PowerShell wrapper (same subcommands)
  scripts/.env.otto-gw.example
                          starter template for the persistent config file
  logs/                   the gateway writes its rotated JSON logs here
  README.md               this file
```

---

## Prerequisites

- `kiro-cli` installed and on `PATH`, or its absolute path set in `KIRO_CMD`. The gateway is a router — without `kiro-cli` it boots in a degraded mode that returns `503` on every chat request.
- A free local TCP port. Default is `127.0.0.1:18080`; override via `HTTP_ADDR`.
- For Windows: PowerShell 5.1+ (built into Windows 10/11). If execution policy blocks the script, run `powershell -ExecutionPolicy Bypass -File .\scripts\otto-gw.ps1 <command>`.

---

## Quickstart — macOS / Linux

```bash
# One-time setup: copy the config template to your home dir and edit it
cp scripts/.env.otto-gw.example ~/.otto-gw.env
# (open ~/.otto-gw.env, uncomment AUTH_TOKEN, KIRO_CMD, and any PII settings)

./scripts/otto-gw start         # launch in background; auto-loads ~/.otto-gw.env
./scripts/otto-gw status        # check PID and /health
./scripts/otto-gw logs -f       # follow the structured JSON log
./scripts/otto-gw stop          # send SIGTERM, wait for clean exit
```

---

## Quickstart — Windows

```powershell
copy scripts\.env.otto-gw.example "$env:USERPROFILE\.otto-gw.env"
# (edit the file: notepad "$env:USERPROFILE\.otto-gw.env")

.\scripts\otto-gw.ps1 start
.\scripts\otto-gw.ps1 status
.\scripts\otto-gw.ps1 logs        # follow the structured log
.\scripts\otto-gw.ps1 stop
```

---

## Configuration

Three ways to set gateway config. Precedence (highest first):

1. **Wrapper flags** — for ad-hoc overrides on a single launch
2. **`.env` file** — persistent, your day-to-day settings
3. **Shell environment** — inherited from the calling shell

### Wrapper flags (start | restart | run | env)

| Flag (bash) | Flag (PowerShell) | Sets |
|-------------|-------------------|------|
| `--pii MODE` | `-Pii MODE` | `PII_REDACTION_ENABLED` + `PII_REDACTION_MODE`. `MODE` ∈ `off,replace,mask,hash,drop`. `off` disables; any other mode enables the hook with that mode. |
| `--hash-key KEY` | `-HashKey KEY` | `PII_HASH_KEY`. Required when `--pii hash`. |
| `--entities LIST` | `-Entities LIST` | `PII_ENABLED_ENTITIES` (comma list). Empty = all six recognizers. |
| `--hooks LIST` | `-Hooks LIST` | `ENABLED_HOOKS` allowlist (comma list). Empty = all hooks. |
| `--auth TOKEN` | `-Auth TOKEN` | `AUTH_TOKEN` (bearer required from clients). |
| `--env-file PATH` | `-EnvFile PATH` | Override the default `.env` search. |

### `.env` auto-load order

The wrapper looks for the first file that exists:

1. `--env-file PATH` (flag override)
2. `$OTTO_ENV_FILE` (env override)
3. `./.env.otto-gw` (project-local — handy for per-project configs)
4. `$HOME/.otto-gw.env` (macOS/Linux per-user) or `%USERPROFILE%\.otto-gw.env` (Windows)

The file is `KEY=value` per line, `#` for comments, `export ` prefix tolerated. See `scripts/.env.otto-gw.example`.

### Preview without launching

```bash
./scripts/otto-gw env                    # secrets masked
./scripts/otto-gw env --show-secrets     # secrets visible
```

---

## Common operator tasks

### Enable PII redaction (replace mode — easy to read in logs)

```bash
./scripts/otto-gw restart --pii replace
```

### Enable PII redaction (hash mode — for log correlation across requests)

```bash
KEY=$(openssl rand -hex 32)
./scripts/otto-gw restart --pii hash --hash-key "$KEY"
# Same email now shows up as <EMAIL:h-XXXXXXXX> in logs.
# Same key → same hash. Different key → different hash (rotates correlation).
```

Persist by adding `PII_REDACTION_ENABLED=true`, `PII_REDACTION_MODE=hash`, and `PII_HASH_KEY=...` to your `.env` file instead.

### Rotate the hash key (breaks attacker correlation if a log leaks)

```bash
NEW=$(openssl rand -hex 32)
./scripts/otto-gw restart --hash-key "$NEW"
# Update the .env too so the next plain `restart` picks up the new key.
```

### Filter the chain (e.g. disable auth for local dev)

```bash
./scripts/otto-gw restart --hooks RequestIDHook,LoggingHook    # AuthHook + PII off
```

The gateway preserves **registration order** regardless of the allowlist order you provide. Unknown hook names cause the gateway to refuse to start — protects against typos like `PIIRedaction` silently disabling PII (the correct name is `PIIRedactionHook`).

### Change the listen port

```bash
HTTP_ADDR=:11434 ./scripts/otto-gw restart    # take over the Ollama default port
```

or persist via `HTTP_ADDR=127.0.0.1:11434` in your `.env`.

---

## Logs

| File | Role |
|------|------|
| `./logs/otto-gateway.log` | Active structured JSON log (current day). |
| `./logs/otto-gateway-2026-05-28T00-00-00.log.gz` | Rotated backup — one per day, gzip-compressed. |
| `./logs/otto-gateway-boot.log` (POSIX) | Pre-logger / crash / `kiro-cli` stderr sidecar — small, rarely consulted. |
| `./logs/otto-gateway.boot-out.log` + `.boot-err.log` (Windows) | Same role as the POSIX sidecar; Windows requires two files. |

**Rotation contract:** the active log rolls over at `00:00` local time every day. The previous day is gzip-compressed and kept for 7 days; older backups are pruned automatically.

```bash
./scripts/otto-gw logs           # last 50 lines of the structured log
./scripts/otto-gw logs -f        # follow live
tail -f ./logs/otto-gateway-boot.log    # crash / kiro-cli stderr (POSIX)
```

```powershell
.\scripts\otto-gw.ps1 logs       # follow the structured log
Get-Content -Wait .\logs\otto-gateway.boot-err.log    # crash sidecar
```

---

## Troubleshooting

### "otto-gateway started" but no log output appears

Check the boot sidecar — the gateway probably failed before its structured logger started up:

```bash
cat ./logs/otto-gateway-boot.log
```

Common causes: `KIRO_CMD` not set / wrong path, `PII_REDACTION_MODE=hash` without `PII_HASH_KEY`, `ENABLED_HOOKS` contains an unknown name, port already in use.

### "bind: address already in use"

Another process holds the port. Override:

```bash
HTTP_ADDR=:18081 ./scripts/otto-gw restart
```

or find and kill the other process: `lsof -ti :18080 | xargs kill`.

### "kiro-cli not configured" on chat requests

The gateway boots even when `kiro-cli` isn't available, but chat requests return `503`. Set `KIRO_CMD`:

```bash
export KIRO_CMD=/absolute/path/to/kiro
./scripts/otto-gw restart
```

### Hash-mode boot refusal

`PII_REDACTION_MODE=hash` requires `PII_HASH_KEY` — by design, the gateway refuses to start in hash mode without a key (the alternative is rainbow-table-trivial unkeyed HMAC). Set the key:

```bash
./scripts/otto-gw restart --pii hash --hash-key "$(openssl rand -hex 32)"
```

### Configuration not taking effect

The gateway reads env vars once at startup. After any config change, you must `./scripts/otto-gw restart` (not just `start`). The `env` subcommand shows what would actually be passed:

```bash
./scripts/otto-gw env --show-secrets
```

---

## Reference

- Default address: `http://127.0.0.1:18080`
- Health probe: `GET /health` (auth-exempt, returns JSON)
- Hook chain introspection: `GET /health/hooks` (auth-exempt, returns the live chain — no secrets, no regex sources)
- All three API surfaces:
  - **Ollama**: `POST /api/chat`, `POST /api/generate` (and standard companion endpoints)
  - **OpenAI**: `POST /v1/chat/completions`, `POST /v1/embeddings`
  - **Anthropic**: `POST /v1/messages` (requires `anthropic-version` header)
- Binary version: `./bin/otto-gateway --version`
- Binary help: `./bin/otto-gateway --help`

---

## License

See repo `LICENSE`.
