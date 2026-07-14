# docs/

Design, architecture, and reference material for Gateway.

These documents are the input context for `/gsd:new-project` and the
phase-planning work that follows. **Read `briefs/go_port_brief.md`
first** — it is the spec of record.

## Layout

### `architecture/`

- **`architecture-overview.png`** — polished infographic showing the
  request flow: clients → API surfaces → guardrails → engine →
  kiro-cli ACP workers. Generated from
  `otto_architecture_infographic_prompt.md`.
- **`otto_architecture_infographic_prompt.md`** — the text-to-image
  prompt used to generate the diagram above. Reproducible; tweak and
  regenerate when the architecture evolves.

### `briefs/`

- **`go_port_brief.md`** — the primary design brief. Covers clients
  to support (Pi CLI + LangFlow), dual API surface (OpenAI + Ollama),
  adapter-over-canonical layering, plugin/hook architecture for
  guardrails, trust gates for AI-assisted development, milestone plan
  (M0–M9), and the Bifrost reference architecture. **This is the
  document phase planning should derive from.**
- **`rust_port_brief.md`** — sibling brief for the Rust alternative.
  Kept for reference; we chose Go (see `go_port_brief.md` §2 for the
  rationale and the trade-offs we accepted).

### `reference/`

- **`acp_server_node_reference.md`** — deep-dive on the existing
  Node.js implementation we are porting. Documents the request
  lifecycle, internal classes, configuration, load-bearing behaviors
  (`coerceToolCall`, NDJSON streaming, auto-grant permissions, pool
  warmup), and the WSL/Windows parity rules. The Go port must
  preserve every "Things that must survive the port" item.

## Reading order for someone new to the project

1. `briefs/go_port_brief.md` §§0–2 (goals, what exists, why Go) —
   ~10 min.
2. `architecture/architecture-overview.png` — visual orientation.
3. `reference/acp_server_node_reference.md` — full spec to mirror.
4. `briefs/go_port_brief.md` §§3–7 — decisions, non-goals, expected
   outputs.

External references cited throughout the briefs:

- Bifrost (`~/Projects/repos/local/bifrost`, `docs.getbifrost.ai`) —
  reference Go LLM gateway architecture.
- "Making AI-Generated Rust Code Trustworthy" by L. Garcia — the
  philosophy behind the trust-gate section (§3.12 of the Go brief
  adapts it to Go tooling).
