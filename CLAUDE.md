<!-- GSD:project-start source:PROJECT.md -->
## Project

**Gateway**

Gateway is a Go-based LLM gateway that exposes OpenAI-,
Ollama-, and Anthropic-compatible HTTP APIs on a single port and
routes every inbound request through a configurable guardrails
chain to a pool of `kiro-cli` ACP worker subprocesses. It replaces
an existing Node.js Ollama proxy
(`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server`) with a
single statically-linked cross-platform binary that adds OpenAI
and Anthropic surfaces alongside the existing Ollama one. Primary
clients are a Pi-SDK-based chat CLI (OpenAI shape), an internal
LangFlow deployment (Ollama shape), and loop24-client / GSD Pi
(Anthropic shape, via `ANTHROPIC_BASE_URL`).

**Core Value:** **All three API surfaces serve their respective clients without
those clients knowing kiro-cli exists, with one place to enforce
policy.**

If everything else fails, this must hold: a LangFlow flow pointing
at `/api/chat`, a Pi-SDK CLI pointing at `/v1/chat/completions`,
and loop24-client with `ANTHROPIC_BASE_URL=http://localhost:11434`
calling `/v1/messages` all receive correct streamed responses, and
any guardrail (auth, rate-limit, content moderation, schema
validation, audit) defined once on the canonical request type
applies uniformly to all three. The gateway being faster than Node
and shipping as one binary is bonus — the surface compatibility
and the single governance surface are the load-bearing properties.

### Constraints

- **Tech stack**: Go 1.23+ — Required for `log/slog` ergonomics and post-1.22 `net/http` routing patterns. No cgo in the main binary (preserves trivial cross-compile).
- **Tech stack**: stdlib `net/http` + `chi` for routing — Rejected `fasthttp` (faster but breaks the `http.Handler` ecosystem; not worth it at our throughput).
- **Compatibility**: Ollama API endpoints and request/response shapes are fixed by existing LangFlow flows. Breaking changes there require a flow migration we're not paying for.
- **Compatibility**: OpenAI API shapes follow public OpenAI spec for the endpoints we serve. Pi SDK will fail on shape drift.
- **Compatibility**: Anthropic Messages API shapes follow the public Anthropic spec (`docs.anthropic.com/en/api/messages`) — including SSE event names, content block discriminators, tool-use `input` as object (not string), `anthropic-version` header requirement, and error envelope shape. `@anthropic-ai/sdk` will fail on shape drift; loop24-client uses both `x-api-key` and `Authorization: Bearer` auth paths so both must work.
- **Distribution**: Single static binary per OS/arch. Cross-compile from macOS dev box must work with vanilla `go build` plus `GOOS`/`GOARCH` env vars. The instant cgo enters the picture (e.g. in-process ONNX), this collapses — explicit decision in `docs/briefs/go_port_brief.md` §3.4 to avoid that.
- **Performance**: Must not be slower than the Node implementation under concurrent load. Tail latency should improve. Hard numbers: TBD; pre-implementation baseline measurement is in the milestone plan.
- **Security**: Bearer-token auth + IP allowlist, both env-driven. Same defaults as Node version (no auth if env unset). Subprocess spawn is the highest-risk surface — `gosec` G204 and friends required to flag any tainted-input regressions.
- **Backward compat**: Environment variable names must match the Node version (`KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `POOL_SIZE`, `SESSION_TTL_MS`, `AUTH_TOKEN`, `ALLOWED_IPS`, `DEBUG`, `EMBEDDING_MODEL_DEFAULT` (reserved, not yet implemented), `HTTP_BODY_READ_TIMEOUT_SEC` (net-new in v1.9), etc.) so deployments can swap binaries.
- **Trust gates**: The lint/test/audit set in `docs/briefs/go_port_brief.md` §3.12 is non-negotiable from day one, not bolted on later. AI-assisted code without these guardrails generates plausible-looking-but-wrong async code patterns.
<!-- GSD:project-end -->

<!-- GSD:stack-start source:STACK.md -->
## Technology Stack

Technology stack not yet documented. Will populate after codebase mapping or first phase.
<!-- GSD:stack-end -->

<!-- GSD:conventions-start source:CONVENTIONS.md -->
## Conventions

Conventions not yet established. Will populate as patterns emerge during development.
<!-- GSD:conventions-end -->

<!-- GSD:architecture-start source:ARCHITECTURE.md -->
## Architecture

Architecture not yet mapped. Follow existing patterns found in the codebase.
<!-- GSD:architecture-end -->

<!-- GSD:skills-start source:skills/ -->
## Project Skills

No project skills found. Add skills to any of: `.claude/skills/`, `.agents/skills/`, `.cursor/skills/`, `.github/skills/`, or `.codex/skills/` with a `SKILL.md` index file.
<!-- GSD:skills-end -->

<!-- GSD:workflow-start source:GSD defaults -->
## GSD Workflow Enforcement

Before using Edit, Write, or other file-changing tools, start work through a GSD command so planning artifacts and execution context stay in sync.

Use these entry points:
- `/gsd-quick` for small fixes, doc updates, and ad-hoc tasks
- `/gsd-debug` for investigation and bug fixing
- `/gsd-execute-phase` for planned phase work

Do not make direct repo edits outside a GSD workflow unless the user explicitly asks to bypass it.
<!-- GSD:workflow-end -->



<!-- GSD:profile-start -->
## Developer Profile

> Profile not yet configured. Run `/gsd-profile-user` to generate your developer profile.
> This section is managed by `generate-claude-profile` -- do not edit manually.
<!-- GSD:profile-end -->
