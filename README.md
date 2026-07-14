<!-- Gateway — the inference + ACP router at the heart of the OTTO stack -->

# Gateway

> The brain of the **OTTO** product family. One process, three API standards, a configurable guardrails chain, and pooled `kiro-cli` ACP workers.

The Gateway is the on-laptop component that every other piece of the OTTO stack talks to when it needs to think. OTTER (the CLI) sends chat-completion calls here. Langflow sends inference calls here. OSCAR's operational data round-trips through here. Whatever speaks Anthropic, OpenAI, or Ollama on one side gets translated into ACP — to local `kiro-cli` workers for inference or to remote OSCAR for ops data — on the other.

---

## Where the Gateway fits

Everything inside the dashed laptop boundary lives on the user's machine. Only OSCAR and the external systems behind it are remote.

![Activity flow — OTTER on the laptop, the Gateway routing the work, OSCAR fetching ops data](docs/architecture/otto_activity_flow_v3.png)

The Gateway is the dark-blue tile at the center of the laptop. Its job in one sentence: **translate standard chat APIs and REST traffic into the right ACP call, apply guardrails on every request, stream the answer back.**

Concretely:

- **OTTER → Gateway** speaks Anthropic / OpenAI / Ollama chat-completion APIs. OTTER never speaks raw ACP — the Gateway does.
- **Langflow → Gateway** speaks the same chat APIs when a flow needs inference.
- **Gateway → kiro-cli pool** speaks ACP over stdio (JSON-RPC 2.0). The pool lives on the same laptop, managed by the Gateway as warm subprocesses.
- **Gateway → OSCAR** speaks ACP over the network. OSCAR holds the credentials for production servers, lab environments, ticket systems, and knowledge bases. The Gateway is the only component that crosses the laptop boundary in this stack.

The architectural value: every LLM token on the laptop egresses through one process. Auth, rate limiting, content moderation, schema validation, and audit logging all live in the Gateway's guardrails chain — configured once, applied to every surface.

---

## Inside the Gateway

The activity diagram above shows the Gateway as one tile. The architecture diagram below shows what is inside that tile.

![Gateway architecture overview — three API surfaces, guardrails, engine + pool](docs/architecture/architecture-overview.jpg)

Reading left-to-right:

1. **Client applications** (yellow) speak Anthropic-, OpenAI-, or Ollama-compatible APIs. OTTER, Langflow, and any drop-in client (Pi CLI, LangChain, Continue.dev, Open WebUI, llama-index) all land here without SDK changes.
2. **API surfaces** (blue) — three thin adapter blocks translate inbound requests into a single canonical request shape. The OpenAI adapter mounts `/v1/chat/completions`, `/v1/embeddings`, `/v1/models`. The Ollama adapter mounts `/api/chat`, `/api/generate`, `/api/embed`, `/api/tags`. The Anthropic adapter mounts `/v1/messages`.
3. **Guardrails / policy chain** (orange — visually elevated because this is the focal point) — a configurable hexagonal chain: Auth → Rate limit → Content moderation → Schema validation → Audit log. Enabled or disabled per deployment. Pass continues; reject returns a 4xx and `kiro-cli` is never invoked.
4. **Engine + pool** (blue) — the canonical engine drives the request lifecycle and streaming. The session pool holds warm `kiro-cli` slots (default 4). The embedding registry serves local ONNX embeddings without invoking `kiro-cli`.
5. **kiro-cli ACP worker pool** (green) — pooled subprocesses speaking JSON-RPC 2.0 over stdio. Stateless requests pull from the warm pool; stateful sessions (`X-Session-Id` header) get a dedicated worker until TTL expires.

The response path streams back to the original surface using the surface-appropriate encoding — SSE for OpenAI/Anthropic, NDJSON for Ollama. Same canonical chunks, surface-specific framing.

---

## Why this matters

**One binary. One port. Three API standards.** No per-client deployment, no per-surface guardrail config. Add a policy once; it covers Anthropic, OpenAI, and Ollama clients alike.

**Configurable governance.** The guardrails chain is the single place to enforce auth, rate limits, content moderation, schema validation, and audit. Each hook is opt-in per deployment. A compliance reviewer audits one chain, not N client integrations.

**Pooled `kiro-cli` workers.** Stateless requests pull from a warm pool. Stateful sessions stick to a dedicated worker until their TTL expires. Pool size, TTL, and ping interval are all configurable.

**Embeddings stay local.** The embedding registry uses local ONNX models. No `kiro-cli` invocation. Useful when an LLM call is not what the request actually needs.

**Cross-compiled.** One Go binary, built for Linux and Windows from the same source.

---

## Status

**Pre-implementation.** The scaffold and design docs are in place; phase planning happens via `/gsd:new-project` and the design docs in `docs/`.

The full design brief — clients, API surfaces, adapter pattern, guardrails plugin model, trust gates, milestone plan — lives in [`docs/briefs/go_port_brief.md`](docs/briefs/go_port_brief.md).

---

## Project layout

```
cmd/otto-gateway/   # binary entrypoint
internal/
  acp/                # ACPSession + JSON-RPC over stdio (kiro-cli)
  adapter/
    anthropic/        # Anthropic API surface (translates ↔ canonical)
    ollama/           # Ollama API surface (translates ↔ canonical)
    openai/           # OpenAI API surface (translates ↔ canonical)
  canonical/          # canonical request/response types
  config/             # env loading
  embed/              # local embeddings (ONNX)
  engine/             # consumes canonical, drives pool/registry/ACP
  plugin/             # PreHook/PostHook interface + chain
  pool/               # ACPPool + SessionRegistry
  server/             # HTTP router, middleware, surface mounting
  version/            # build-time version info
docs/                 # design docs, architecture, reference material
```

Layer invariants enforced by the trust-gate config (see brief §3.8):

- `internal/adapter/*` imports `internal/canonical` + `internal/plugin` only.
- `internal/engine` imports `internal/canonical/pool/acp/embed/plugin`.
- `internal/canonical` imports nothing else under `internal/`.

---

## Running

### Quick install (recommended)

**macOS / Linux:**

```bash
curl -fsSL https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/cmetech/otto-gateway/main/scripts/install.ps1 | iex
```

This downloads the latest release, verifies its checksum, and splits the
install across two anchors: `GW_INSTALL_DIR` for code (binaries + scripts,
replaceable on upgrade — default `~/Library/Application Support/Gateway` on
macOS, `${XDG_DATA_HOME:-~/.local/share}/gateway` on Linux,
`%LOCALAPPDATA%\Gateway` on Windows) and `GW_HOME` for config (`.env`, logs,
state — precious, never overwritten — default `~/.gw`). It writes a default
config into `GW_HOME` (no auth, `127.0.0.1:18080`, all hooks, chat-trace
off), and puts `gw` on your PATH. Pin a version with `GW_VERSION=v1.5.5`.
Install `kiro-cli` separately — the gateway returns 503 on chat requests
without it. Upgrading a pre-relayout install? The installer auto-migrates a
legacy `~/.otto-gw.env` (plus its overrides/tray-config companions) into
`~/.gw/` the first time it runs — nothing to do by hand. See
[`docs/INSTALL.md`](docs/INSTALL.md) for the manual archive install and
per-OS detail.

macOS and Windows installs also drop a menu-bar / system-tray launcher
(macOS: `$GW_INSTALL_DIR/Gateway Tray.app`; Windows:
`$GW_INSTALL_DIR\bin\gateway-tray.exe`) — optional, off by default.
See [Optional: launch the menu-bar / system-tray app](docs/operator-quickstart.md#optional-launch-the-menu-bar--system-tray-app-macos--windows)
in the operator quickstart for what it does and how to remove its login-item
registration.

The tray also has an **OTTO Desktop** section for managing the OTTO desktop
coworker app (separate from the gateway):

- **Install OTTO Desktop…** — shown when the app isn't installed; runs the
  published OTTO desktop installer (`irm …/cmetech/otto/main/install.ps1 | iex`
  on Windows, `curl …/install.sh | sh` on macOS).
- **Start / Stop OTTO Desktop** — launch the installed app, or stop it (Stop
  asks for confirmation first). The header shows whether it's running.

v1 notes: it assumes the **OTTO** brand (identity is refined from the installed
app's `brand.json` when present) and detects "running" by process name.

---

### From source (developers)

Build the binary first, then use the platform wrapper script:

**macOS / Linux:**

```bash
make build
./scripts/gw start    # launch in background
./scripts/gw status   # check PID + /health
./scripts/gw stop     # stop gracefully
```

**Windows (PowerShell):**

```powershell
make build
.\scripts\gw.ps1 start
.\scripts\gw.ps1 status
.\scripts\gw.ps1 stop
```

`make start`, `make status`, and `make stop` are Makefile shortcuts for the POSIX wrapper on macOS/Linux.

See [`docs/operating.md`](docs/operating.md) for full reference: PID and log file locations, env-var overrides (`GW_BIN`, `GW_PID`, `GW_LOG`, `GW_ADDR`, `GW_HOME`, `GW_INSTALL_DIR`), gateway env vars (`HTTP_ADDR`, `KIRO_CMD`, `PING_INTERVAL`, …), and how `status` works.

---

## Development

```bash
make help          # show all targets
make run           # run the gateway locally
make build         # build for host platform
make test          # run tests
make test-race     # tests with race detector (CI default)
make lint          # golangci-lint
make fmt           # format
make cross         # cross-compile Linux + Windows binaries
```

### Prerequisites

- Go 1.23+
- `golangci-lint` 1.62+ (optional locally; required in CI)
- `gofumpt` (optional; `make fmt` falls back to `gofmt`)
- `pre-commit` (optional; `pre-commit install` to enable hooks)

---

## Where to learn more about the rest of the OTTO stack

- **OTTER** (the CLI client) — see the loop24-client repo. OTTER (call it OTTO for short) is the on-laptop entrypoint that drives the Gateway.
- **Langflow** — the low-code automation orchestrator. OTTER calls it via REST; flows inside Langflow call back into the Gateway for any inference they need.
- **OSCAR** — the remote operations agent. Exposes an ACP interface that this Gateway is the only component allowed to talk to.

The activity-flow diagram at the top of this README is the canonical picture. The source prompts for both diagrams live in [`docs/architecture/otto_architecture_infographic_prompt.md`](docs/architecture/otto_architecture_infographic_prompt.md) (architecture) and in the loop24-client `docs/branding/` folder (activity flow).

---

## License

TBD.
