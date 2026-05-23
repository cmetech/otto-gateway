<!-- GSD:project-start source:PROJECT.md -->
## Project

**Loop24 Gateway**

Loop24 Gateway is a Go-based LLM gateway that exposes both OpenAI-
and Ollama-compatible HTTP APIs on a single port and routes every
inbound request through a configurable guardrails chain to a pool
of `kiro-cli` ACP worker subprocesses. It replaces an existing
Node.js Ollama proxy (`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server`)
with a single statically-linked cross-platform binary that adds an
OpenAI surface alongside the existing Ollama one. Primary clients
are a Pi-SDK-based chat CLI (OpenAI shape) and an internal LangFlow
deployment (Ollama shape).

**Core Value:** **Both API surfaces serve their respective clients without those
clients knowing kiro-cli exists, with one place to enforce policy.**

If everything else fails, this must hold: a LangFlow flow pointing
at `/api/chat` and a Pi-SDK CLI pointing at `/v1/chat/completions`
both receive correct streamed responses, and any guardrail (auth,
rate-limit, content moderation, schema validation, audit) defined
once on the canonical request type applies uniformly to both. The
gateway being faster than Node and shipping as one binary is bonus —
the surface compatibility and the single governance surface are
the load-bearing properties.

### Constraints

- **Tech stack**: Go 1.23+ — Required for `log/slog` ergonomics and post-1.22 `net/http` routing patterns. No cgo in the main binary (preserves trivial cross-compile).
- **Tech stack**: stdlib `net/http` + `chi` for routing — Rejected `fasthttp` (faster but breaks the `http.Handler` ecosystem; not worth it at our throughput).
- **Compatibility**: Ollama API endpoints and request/response shapes are fixed by existing LangFlow flows. Breaking changes there require a flow migration we're not paying for.
- **Compatibility**: OpenAI API shapes follow public OpenAI spec for the endpoints we serve. Pi SDK will fail on shape drift.
- **Distribution**: Single static binary per OS/arch. Cross-compile from macOS dev box must work with vanilla `go build` plus `GOOS`/`GOARCH` env vars. The instant cgo enters the picture (e.g. in-process ONNX), this collapses — explicit decision in `docs/briefs/go_port_brief.md` §3.4 to avoid that.
- **Performance**: Must not be slower than the Node implementation under concurrent load. Tail latency should improve. Hard numbers: TBD; pre-implementation baseline measurement is in the milestone plan.
- **Security**: Bearer-token auth + IP allowlist, both env-driven. Same defaults as Node version (no auth if env unset). Subprocess spawn is the highest-risk surface — `gosec` G204 and friends required to flag any tainted-input regressions.
- **Backward compat**: Environment variable names must match the Node version (`KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `POOL_SIZE`, `SESSION_TTL_MS`, `AUTH_TOKEN`, `ALLOWED_IPS`, `DEBUG`, `EMBEDDING_MODEL_DEFAULT`, etc.) so deployments can swap binaries.
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
