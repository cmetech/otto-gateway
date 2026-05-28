# OTTO Gateway

A single-binary LLM gateway. Exposes OpenAI-, Ollama-, and Anthropic-compatible HTTP APIs on one port and routes every request through a configurable guardrails chain (auth, request-ID, logging, PII redaction) to a pool of `kiro-cli` ACP worker processes.

This README is for **operators running the binary on a laptop**. Developers building the gateway should read the repo's top-level `README.md` and `docs/operating.md` instead.

---

## Install — first-time setup

Six steps, ~3 minutes. Each step links to a deeper section if you need details.

### 1. Verify the download (recommended)

If your release came with a `SHA256SUMS-<version>.txt` file, check the archive before extracting. Recipes for POSIX + Windows are in [Verifying your download](#verifying-your-download) further down. Skip this step at your own risk if you trust the source.

### 2. Extract the archive

```bash
tar -xzf otto_gateway-<os>-<arch>-<version>.tar.gz     # macOS / Linux
cd otto_gateway
```

```powershell
Expand-Archive otto_gateway-windows-amd64-<version>.zip
cd otto_gateway
```

You should now see the layout described in [What's in the box](#whats-in-the-box).

### 3. (macOS only) Remove the quarantine attribute

If you downloaded the archive via a browser, macOS Gatekeeper will refuse to launch the binary the first time. Strip the quarantine attribute once:

```bash
xattr -d com.apple.quarantine bin/otto-gateway
```

Full context in [Troubleshooting → macOS](#macos-otto-gateway-cannot-be-opened-because-apple-cannot-check-it-for-malicious-software).

### 4. Install kiro-cli

The gateway is a router — without `kiro-cli` it boots in a degraded mode that returns `503` on every chat request. Install `kiro-cli` per your team's distribution instructions and confirm:

```bash
command -v kiro    # macOS / Linux
```

```powershell
Get-Command kiro   # Windows
```

If `kiro-cli` lives somewhere not on `PATH`, note its absolute path — `init` will prompt for it in the next step.

### 5. Run `init`

Generates a random `AUTH_TOKEN` + `PII_HASH_KEY`, prompts for `KIRO_CMD`, `HTTP_ADDR`, and PII mode, then writes `~/.otto-gw.env` (mode `0600`):

```bash
./scripts/otto-gw init           # macOS / Linux
```

```powershell
.\scripts\otto-gw.ps1 init       # Windows
```

For non-interactive installs (CI, image baking, etc.) see [`init` — non-interactive form](#init--non-interactive-form-for-scripts--ci).

### 6. Start

```bash
./scripts/otto-gw start          # macOS / Linux
```

```powershell
.\scripts\otto-gw.ps1 start      # Windows
```

The wrapper waits for `/health` to come up before returning. On failure it tails the last 20 lines of the structured log inline so you see the actual error (config typo, hash-mode-without-key, port conflict, etc.) without grepping. Hit `http://localhost:18080/health` in a browser to confirm.

**Day two:** see [Common operator tasks](#common-operator-tasks) for enabling PII, rotating the hash key, changing the listen port, and filtering the hook chain.

---

## What's in the box

```
otto_gateway/
  bin/otto-gateway        the gateway binary (single static executable)
  scripts/otto-gw         POSIX wrapper (init | start | stop | status | restart | logs | run | env)
  scripts/otto-gw.ps1     Windows PowerShell wrapper (same subcommands)
  scripts/.env.otto-gw.example
                          starter template for the persistent config file
  logs/                   the gateway writes its rotated JSON logs here
  README.md               this file
```

---

## Prerequisites

**Required before first start:**

- **`kiro-cli` installed and on `PATH`** (or its absolute path set in `KIRO_CMD`). Step 4 of [Install](#install--first-time-setup) covers this. The gateway is a router — without `kiro-cli` it boots in a degraded mode that returns `503` on every chat request. The wrapper auto-detects `kiro` on `PATH` if `KIRO_CMD` is unset and prints `✓ KIRO_CMD auto-detected: <path>` on `start`; only if both are missing does it warn. Degraded boot is intentional — you can still hit `/health`, `/health/hooks`, and `/health/pool` for diagnostics without `kiro`.

**Required by the binary itself:**

- A free local TCP port. Default is `127.0.0.1:18080`; override via `HTTP_ADDR`.
- For Windows: PowerShell 5.1+ (built into Windows 10/11). If execution policy blocks the script, run `powershell -ExecutionPolicy Bypass -File .\scripts\otto-gw.ps1 <command>`.

**Port-default note for migrators:** the legacy Node Ollama proxy listened on `11434`. The Go gateway picks `18080` instead so it can coexist with a real Ollama install on the same machine. If you're swapping the Node proxy for this gateway and pointing LangFlow (or any Ollama client) at it, set `HTTP_ADDR=127.0.0.1:11434` in your `.env.otto-gw` — the gateway will then take over the Ollama port and your clients need no reconfiguration.

---

## Day-to-day subcommands

Once installed (see [Install](#install--first-time-setup)), the day-to-day surface is the same on every OS:

```bash
./scripts/otto-gw start         # launches; waits for /health to come up
./scripts/otto-gw status        # PID + /health JSON
./scripts/otto-gw logs -f       # follow the structured JSON log
./scripts/otto-gw restart       # stop + start (re-applies flags / .env)
./scripts/otto-gw stop          # SIGTERM, wait for clean exit
./scripts/otto-gw env           # preview what would be passed; secrets masked
```

On Windows substitute `.\scripts\otto-gw.ps1 <command>` and drop `-f` (the PowerShell `logs` always follows).

---

## `init` — non-interactive form for scripts / CI

Both wrappers accept flags so `init` can be driven without prompts:

```bash
./scripts/otto-gw init \
  --non-interactive \
  --kiro /usr/local/bin/kiro \
  --addr 127.0.0.1:11434 \
  --pii hash
```

```powershell
.\scripts\otto-gw.ps1 init `
  -NonInteractive `
  -Kiro "C:\Tools\kiro.exe" `
  -Addr "127.0.0.1:11434" `
  -Pii hash
```

Flags: `--dest PATH` / `-Dest PATH` chooses the output file; `--here` /
`-Here` writes `./.env.otto-gw` instead of the home directory; `--force`
/ `-Force` overwrites; `--auth-token`/`-AuthToken` and `--hash-key`/
`-HashKey` substitute provided values for generated secrets.

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

### macOS: "otto-gateway cannot be opened because Apple cannot check it for malicious software"

The binary is ad-hoc signed but NOT notarized by Apple (we deliberately keep notarization out of v1 distribution — it requires a paid Apple Developer account). The macOS Gatekeeper attaches a `com.apple.quarantine` attribute to anything downloaded via a browser or extracted from a downloaded archive, and refuses to launch quarantined binaries from unidentified developers.

Two ways to resolve:

**Option A — strip the quarantine attribute (recommended):**

```bash
xattr -d com.apple.quarantine bin/otto-gateway
```

This is one-time per install. The wrapper scripts don't need this since shell scripts aren't gated by Gatekeeper.

**Option B — right-click → Open (per binary, one-time):**

In Finder, control-click `bin/otto-gateway` → Open → "Open" in the dialog. macOS records the exception and subsequent launches via the wrapper work normally.

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

## Verifying your download

If the release came with a `SHA256SUMS-<version>.txt` file, verify the archive before extracting:

```bash
# macOS / Linux — POSIX
shasum -a 256 -c SHA256SUMS-<version>.txt 2>&1 | grep otto_gateway-darwin-arm64

# Linux (if shasum isn't installed)
sha256sum -c SHA256SUMS-<version>.txt
```

```powershell
# Windows
$want = (Select-String -Path .\SHA256SUMS-<version>.txt -Pattern 'windows-amd64' | ForEach-Object { ($_.Line -split '\s+')[0] })
$got  = (Get-FileHash .\otto_gateway-windows-amd64-<version>.zip -Algorithm SHA256).Hash.ToLower()
if ($want -eq $got) { "OK" } else { "MISMATCH" }
```

---

## Reference

- Default address: `http://127.0.0.1:18080`
- Health probe: `GET /health` (auth-exempt, returns JSON — "gateway is up")
- Pool serving-health probe: `GET /health/pool` (auth-exempt — the "are workers actually serving requests?" signal). Body:
  ```json
  {"pool": {
    "size": 2, "alive": 2, "busy": 0, "healthy": true,
    "last_spawn_error": "...", "last_spawn_error_at": "2026-05-28T11:42:13Z"
  }}
  ```
  `healthy: false` when `size > 0 && alive == 0` — i.e., the pool was configured but every worker died and failed to respawn. `last_spawn_error*` are present only when at least one respawn has failed; they are historical / forensic (not cleared by subsequent success). Monitor `healthy` for a single-field "page me" signal.
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
