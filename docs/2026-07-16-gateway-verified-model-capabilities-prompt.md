# LLM prompt — verified Kiro model capabilities for OTTO Gateway

You are implementing verified per-model capability discovery in the OTTO Gateway.

## Repository and current state

Work in the existing checkout:

`/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway`

Before acting:

1. Read `CLAUDE.md` completely.
2. Inspect the current branch, worktree status, recent history, and open planning
   artifacts. Do not assume the branch or commit references below are still current.
3. Preserve all existing work. At the time this prompt was prepared, the checkout was
   clean on `quick/gateway-toolcall-surfacing`, based on `main` tag `v2.10.0`, and
   contained recent structured tool-call and Kiro-persona fixes. Do not reset, discard,
   or regress those changes.
4. Follow the repository's required GSD workflow before editing.
5. Do not create a Git worktree.
6. Do not modify the Co-Worker/Hermes client repository from this session.
7. Do not push, release, or open a PR without explicit approval.

Use test-driven development. Add failing contract tests before implementation, then
make them pass. Run the repository's full required Go verification before declaring
completion.

## Product problem

The Co-Worker client currently uses the Gateway as an OpenAI-compatible provider. The
Gateway exposes Kiro's live model IDs through `GET /v1/models`, but the client cannot
tell which listed models are suitable for a particular job:

- Main agent use
- Text-only auxiliary work
- Vision/image analysis
- Tool-oriented use
- Reasoning controls

Kiro currently exposes the following relevant information:

- `session/new` returns `result.models.availableModels[]`, with `modelId` and `name`.
- `initialize` returns agent-wide
  `agentCapabilities.promptCapabilities.image/audio/embeddedContext`.
- `session/set_model` selects a non-`auto` model.

The agent-wide prompt capabilities are NOT per-model evidence. Do not infer that every
model supports vision merely because `promptCapabilities.image` is true.

The Gateway must provide an honest, auditable per-model capability catalog that the
Co-Worker client can use to offer only models whose required capability has been
verified. Unknown must remain unknown.

## Existing code to inspect

Review at least:

- `internal/acp/client.go`
  - `sessionNewResultModelEntry`
  - `NewSession`
  - `AvailableModels`
  - `PromptCapabilities`
  - `SetModel`
- `internal/canonical/model.go`
- `internal/canonical/capabilities.go`
- `internal/pool/pool.go`
  - model discovery, caching, and `Models()`
- `internal/pool/model_discovery_test.go`
- `internal/adapter/openai/adapter.go`
- `internal/adapter/openai/handlers.go`
- `internal/adapter/openai/render.go`
- `internal/adapter/openai/models_test.go`
- `internal/adapter/ollama/handlers.go`
- `internal/adapter/ollama/wire.go`
- `cmd/otto-gateway/main.go`
- `docs/reference/acp_wire_shapes.md`
- `docs/reference/acp_server_node_reference.md`
- `docs/superpowers/specs/2026-07-14-model-discovery-resilience-design.md`
- `tests/e2e/openai_e2e_test.go`
- `tests/e2e/ollama_e2e_test.go`
- `tests/e2e/cmd/fake-kiro-cli/main.go`

Also inspect recent model-discovery and API compatibility history before choosing
placement or changing an existing wire shape.

## Required architecture

Keep `GET /v1/models` backward-compatible. Do not require existing OpenAI-compatible
clients to understand a new field, and do not replace its standard response shape.

Add a separate Gateway-owned endpoint:

`GET /v1/model-capabilities`

This endpoint combines:

1. The live Kiro model catalog, which determines current availability.
2. An embedded, source-controlled capability registry, which records verified
   per-model capabilities and their evidence.

The live Kiro catalog remains authoritative for whether an explicit model is currently
selectable. The registry remains authoritative only for what has been verified about
that model.

Do not:

- Treat a registry entry as currently available when Kiro does not list it.
- Treat a live but unregistered model as capable.
- Guess capabilities from the model name.
- Fuzzy-match unknown model IDs onto known registry entries.
- Convert agent-wide prompt capabilities into per-model declarations.
- Probe models on every HTTP request.
- Perform live billable prompts merely because a client opened a dropdown.

## Capability states

Use exactly these machine-readable states:

- `supported`
- `unsupported`
- `unknown`

At minimum, model entries must support these capability keys:

- `completion`
- `tools`
- `vision`
- `reasoning`

Do not use a Boolean where the source can be unknown.

The synthetic `auto` entry is a routing mode, not a concrete model. Include it in the
capability response so clients can identify it, but do not fabricate per-model
capabilities for it. Give it explicit routing metadata and mark unproven capabilities
`unknown`.

## Endpoint contract

Implement a stable response equivalent to:

```json
{
  "object": "list",
  "registry_revision": "sha256-or-version",
  "generated_at": "2026-07-16T12:00:00Z",
  "data": [
    {
      "id": "auto",
      "name": "Automatic",
      "available": true,
      "selection_mode": "automatic",
      "capabilities": {
        "completion": "unknown",
        "tools": "unknown",
        "vision": "unknown",
        "reasoning": "unknown"
      },
      "evidence": {}
    },
    {
      "id": "example-model-id",
      "name": "Example Model",
      "available": true,
      "selection_mode": "explicit",
      "capabilities": {
        "completion": "supported",
        "tools": "supported",
        "vision": "unsupported",
        "reasoning": "supported"
      },
      "evidence": {
        "vision": {
          "source": "vendor_documentation",
          "reference": "https://official-vendor.example/model",
          "verified_at": "2026-07-16",
          "notes": "Official input-modality declaration."
        }
      }
    }
  ]
}
```

The exact Go types and field ordering should follow existing repository conventions,
but preserve these semantics.

The response must:

- Contain `auto` first.
- Contain only explicit models present in the current live Kiro catalog.
- Preserve live Kiro display names where available.
- Return live, unregistered models with every capability set to `unknown`.
- Never expose stale registry-only models as available.
- Be deterministic apart from `generated_at`.
- Use the same authentication and IP-allowlist middleware policy as `/v1/models`.
- Avoid exposing internal paths, environment variables, worker IDs, pool slots, prompts,
  or secret material.

If the pool/catalog is temporarily empty, return `auto` plus an otherwise empty list,
matching the Gateway's existing degraded catalog posture. Do not turn a catalog outage
into invented availability.

## Embedded capability registry

Create a small, focused package for registry loading, validation, and live-catalog
enrichment. Keep HTTP rendering separate from registry logic.

Prefer a source-controlled JSON registry embedded with `go:embed`, unless the existing
repository has a stronger established data-file pattern.

The registry should be keyed by exact Kiro model ID. Each entry should contain:

- Optional expected display name
- Capability state for every required capability
- Evidence for every `supported` or `unsupported` declaration
- Evidence source type
- Evidence reference
- Verification date
- Optional concise notes

Recommended evidence source types:

- `kiro_declared`
- `vendor_documentation`
- `controlled_probe`

Do not allow an unsupported free-form source type to silently pass validation.

Registry validation must reject:

- Missing or duplicate model IDs
- Invalid capability names
- Invalid states
- `supported` or `unsupported` without evidence
- Missing evidence reference
- Invalid verification dates
- Evidence for a capability not present in the entry
- Empty model IDs

Unknown entries do not require evidence.

Research initial model capabilities using this evidence order:

1. Kiro-declared per-model metadata, if the installed Kiro version actually provides it.
2. Official documentation from the underlying model vendor.
3. A controlled, explicitly run synthetic probe with no real or confidential data.

Do not use blogs, marketing summaries, model-name intuition, or an LLM's memory as
verification. If official documentation cannot be unambiguously mapped to the exact
Kiro model ID, leave the capability `unknown`.

Record the source and verification date. Do not claim the entire initial Kiro catalog is
verified merely to make the UI look complete.

## Controlled probes

A general live-probing service is not required for the first implementation. The
embedded registry is the baseline.

Design the registry schema so a later maintenance tool can store
`source: controlled_probe`, but do not add request-time probing or a background quota
consumer unless it is already justified by existing repository architecture.

If you add a deterministic maintainer-only validator or probe helper:

- It must be explicit, never automatic on server start or dropdown access.
- It must use fictional synthetic data.
- It must not execute side-effecting tools.
- It must not print credentials or confidential model output.
- It must clearly distinguish pass, fail, and inconclusive.
- Probe results must still be reviewed before changing the embedded registry.

## Compatibility requirements

- Preserve `GET /v1/models` response compatibility and its model set.
- Preserve `/api/tags` compatibility and its equality relationship with `/v1/models`.
- Preserve `/api/show` wire compatibility. Its legacy
  `capabilities: ["completion", "tools"]` declaration is an Ollama-compatibility
  surface and must not be presented in new documentation as verified per-model
  capability evidence.
- Preserve all OpenAI, Anthropic, and Ollama chat/stream/tool-call behavior.
- Preserve model-discovery self-healing.
- Preserve `auto` selection semantics.
- Preserve the recent structured tool-call surfacing and persona-guard changes on the
  current branch.
- Do not add cgo or a runtime network dependency for the embedded registry.

## Testing requirements

Write failing tests first.

### Registry unit tests

Cover:

- Valid registry loads successfully.
- Invalid state is rejected.
- Missing evidence for `supported`/`unsupported` is rejected.
- Unknown capabilities are accepted without evidence.
- Exact model-ID matching only.
- A live unregistered model becomes all-unknown.
- A registry-only stale model is omitted.
- Live display name wins.
- Input slices/maps are not mutated.
- `auto` is first and explicitly automatic.
- Registry revision is deterministic for identical bytes.

Do not write a snapshot test that freezes today's complete model list. Test relationships
and invariants.

### OpenAI endpoint tests

Cover:

- `/v1/model-capabilities` response shape.
- Auth middleware parity with `/v1/models`.
- Catalog-present behavior.
- Empty/degraded catalog behavior.
- Unknown model behavior.
- No internal information leakage.
- Correct JSON content type and error handling.

### Cross-surface regression tests

Assert:

- `/v1/models` remains unchanged.
- `/api/tags` and `/v1/models` still expose the same model-ID set.
- `/api/show` compatibility remains intact.
- Chat and tool-call tests continue to pass.

### E2E

Extend the fake Kiro fixture only as necessary. Add an E2E assertion that:

1. Fake Kiro advertises at least one registered model and one unknown model.
2. `/v1/model-capabilities` returns both because both are live.
3. The registered model carries its verified states.
4. The unknown model carries only `unknown`.
5. A registry model absent from fake Kiro is not returned.

## Documentation and maintenance

Document:

- The endpoint contract.
- The distinction between availability and capability verification.
- Why agent-wide `promptCapabilities` cannot prove per-model support.
- Registry schema and evidence policy.
- How to research and add a new exact model ID.
- How to retire a removed model.
- How to update evidence and verification dates.
- How to validate registry drift against a live Kiro catalog.
- Why unknown models remain visible to clients as unknown but should not be selected for
  capability-sensitive work.

Add a maintenance rule:

> Adding or changing a Kiro model capability declaration requires an exact model ID,
> evidence, a verification date, registry validation, endpoint contract tests, and a
> review of whether the model is still present in the live Kiro catalog.

Prefer automated schema/invariant validation over documentation alone.

## Expected verification

Run the repository-standard commands discovered from its Makefile, CI, and docs. At
minimum, unless the repository specifies stricter equivalents:

```bash
gofmt -w <changed-go-files>
go test ./...
go test -race ./...
go vet ./...
```

Run the existing security/static-analysis gates required by `CLAUDE.md` and
`docs/briefs/go_port_brief.md`. If a required tool is unavailable, report the exact
command and limitation rather than claiming it passed.

Also run targeted tests for:

- Registry package
- OpenAI models/capability handlers
- Pool model discovery
- E2E OpenAI/Ollama model catalog behavior
- Existing cross-surface tool-call tests affected by the current branch

## Completion report

When complete, report:

1. The verified repository and branch state.
2. The architecture implemented.
3. The endpoint contract.
4. The initial registry entries and evidence sources.
5. Models/capabilities deliberately left unknown.
6. Tests and verification commands with results.
7. Files changed.
8. Any client-facing assumptions or follow-up.
9. Whether the worktree is clean.

Do not push or release.
