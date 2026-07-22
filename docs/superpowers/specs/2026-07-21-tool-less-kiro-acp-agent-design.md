# Tool-less Kiro ACP Agent Design

**Date:** 2026-07-21
**Status:** Approved

## Problem

The gateway currently launches `kiro-cli acp` without selecting a custom
agent. Kiro therefore uses its default tool-wielding agent and persona. That
agent can interpret the host application's identity prompt and JSON tool-call
protocol as prompt injection, or it can attempt to use Kiro's own tools instead
of emitting a structured call for the host to execute.

The legacy loop24 ACP server avoided both failure modes by running Kiro under a
workspace-scoped `acp_proxy` agent with no prompt, built-in tools, MCP servers,
resources, or hooks. Restoring that launch mode is the minimal regression fix.

## Goals

- Make `kiro-cli acp --agent acp_proxy` the zero-configuration default.
- Ship the exact tool-less agent configuration inside the static gateway
  binary.
- Give Kiro a deterministic workspace containing
  `.kiro/agents/acp_proxy.json`.
- Preserve all `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `--kiro-args`, and
  `--kiro-cwd` overrides.
- Never replace or rewrite an existing `acp_proxy.json`.
- Keep the identity guard, built-in-tool permission denial, and tool-call
  aliasing defenses unchanged.
- Make the effective Kiro command, arguments, working directory, and agent
  configuration visible in startup logs.

## Non-goals

- Removing any runtime tool-call or identity workaround.
- Adding a brand gate or an opt-in feature flag.
- Seeding the user's global `~/.kiro/agents` directory.
- Changing tool-call extraction, coercion, permission handling, or API response
  formats.
- Automatically modifying an operator-supplied `KIRO_CWD`.

## Architecture

### Embedded asset

The reserved `internal/embed` directory becomes a small asset package. It owns
the embedded `acp_proxy.json`, gateway-home derivation, and write-if-absent
materialization.

The embedded JSON is:

```json
{
  "name": "acp_proxy",
  "description": "Tool-less generator agent for the OTTO gateway: the CALLER executes all tools via tool_call JSON blocks, so kiro's built-in tools and MCP servers are disabled entirely.",
  "prompt": null,
  "mcpServers": {},
  "tools": [],
  "toolAliases": {},
  "allowedTools": [],
  "resources": [],
  "hooks": {},
  "toolsSettings": {},
  "includeMcpJson": false,
  "model": null
}
```

`tools` is explicitly present and empty. `prompt` remains `null`; the host's
`[System]` message and the existing `identityGuardClause` remain the source of
identity.

### Gateway-owned workspace

The default workspace root follows the repository's existing persistent
gateway-home convention:

1. A non-empty `$GW_HOME`.
2. Otherwise `<os.UserConfigDir()>/gateway`.

The asset path is `<gateway-home>/.kiro/agents/acp_proxy.json`. The gateway does
not write to the executable directory or the user's global `~/.kiro` tree.

Materialization creates the parent directories and then creates the agent file
exclusively. If a regular file already exists, it is preserved byte-for-byte.
An existing non-regular target or a directory/write failure returns an
actionable startup error. The configuration is non-secret and may use `0644`
file permissions; created directories use `0755`.

### Configuration defaults and overrides

When unset, configuration resolves to:

```text
KIRO_ARGS=acp --agent acp_proxy
KIRO_CWD=<gateway-home>
```

The default directory is allowed to be absent during configuration parsing
because startup materialization creates it before any Kiro subprocess is
spawned. An explicitly supplied nonexistent `KIRO_CWD` environment value
continues to fail validation as it does today.

The existing environment and flag precedence remains unchanged. Explicit
`KIRO_ARGS` or `--kiro-args` values replace the default argument vector.
Explicit `KIRO_CWD` or `--kiro-cwd` values replace the default workspace. A
custom workspace is operator-owned and is never seeded; an operator who keeps
the default `--agent acp_proxy` argument while changing the workspace must make
that agent discoverable there.

### Startup flow

Immediately before pool construction and warmup, startup checks whether the
effective `KiroCWD` is the derived gateway-owned default. When it is, startup
materializes the embedded agent. Failure aborts startup before any worker is
created.

Startup then logs one structured record containing:

- Kiro command.
- Argument vector.
- Working directory.
- Agent configuration path when the default workspace is in use.
- Whether that file was created or an existing file was preserved.

Custom arguments and working directories are still logged, even when no
embedded asset is materialized. The log precedes pool warmup so failed launches
remain diagnosable.

## Components

### `internal/embed`

- Embed the canonical JSON bytes with `go:embed`.
- Derive the gateway-owned workspace without relying on the process launch
  directory.
- Materialize `.kiro/agents/acp_proxy.json` without overwriting existing data.
- Return the resolved path and whether creation occurred for startup logging.

### `internal/config`

- Change the default Kiro argument vector to
  `[]string{"acp", "--agent", "acp_proxy"}`.
- Change the default Kiro working directory to the derived gateway home.
- Distinguish the derived default from an explicit environment override when
  validating a missing directory.
- Retain current environment and CLI flag precedence.

### `cmd/otto-gateway`

- Materialize the agent before pool construction when using the default
  gateway-owned workspace.
- Abort normal Kiro startup on materialization errors.
- Emit the structured launch diagnostic.

### `internal/admin`

- Update the operator-facing default values and working-directory description
  to match runtime behavior.

## Error handling

- Failure to derive a default gateway home is a configuration error that tells
  the operator to set `GW_HOME` or `KIRO_CWD`.
- Failure to create the workspace or agent file is a startup error before pool
  warmup.
- An existing regular agent file is valid and is never content-validated or
  replaced, allowing user customization.
- An existing non-regular target is rejected because Kiro cannot reliably load
  it as an agent configuration.
- Explicit custom Kiro configuration retains its existing validation and
  subprocess error behavior.

## Testing strategy

Implementation follows red-green-refactor cycles.

1. Asset-package tests verify exact JSON structure, explicit empty `tools`,
   default-root precedence, first creation, repeat preservation, customized-file
   preservation, and invalid-target errors.
2. Configuration tests verify the new defaults and that environment and flag
   overrides still win independently.
3. Startup tests verify default materialization, failure-before-warmup, custom
   workspace non-modification, and the launch diagnostic fields.
4. Admin tests verify the displayed defaults.
5. Repository verification runs focused tests, the full Go test suite, lint or
   vet gates used by the repository, and a production binary build.
6. Live verification records the installed Kiro version and `--agent` support,
   starts the gateway with defaults, confirms the launch path and absence of
   built-in permission requests, then exercises identity, structured tool-call,
   tool-call execution, normal chat, and streaming across Ollama, OpenAI, and
   Anthropic surfaces.

Host-only computer-use or project-skill checks that are unavailable in this
workspace are reported as explicit verification gaps rather than inferred as
passing.

## Compatibility and security

- The binary remains statically distributable; the asset is compiled in with
  `go:embed`.
- No request-controlled value reaches the materialization path.
- Existing subprocess command and path validation remains in place.
- Existing runtime defenses remain unchanged as defense-in-depth.
- No global Kiro configuration is modified.

## Documentation references

- [Kiro ACP mode](https://kiro.dev/docs/cli/acp/)
- [Kiro custom-agent configuration reference](https://kiro.dev/docs/cli/custom-agents/configuration-reference/)
- [Creating Kiro custom agents](https://kiro.dev/docs/cli/custom-agents/creating/)
