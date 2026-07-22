---
phase: quick-260721-tb9
status: complete
completed: 2026-07-21
commits:
  - 629d3e4
  - 5718f93
  - 2dd701a
  - f77b3c1
---

# Quick Task 260721-tb9 Summary

The gateway now defaults to `kiro-cli acp --agent acp_proxy` from its
gateway-owned persistent workspace. The exact tool-less custom-agent JSON is
embedded into the static binary and materialized at
`.kiro/agents/acp_proxy.json` with exclusive create semantics; an existing
regular file is preserved byte-for-byte and a custom `KIRO_CWD` is never
modified.

Configuration retains environment and flag precedence through the new
`KiroCWDIsDefault` ownership bit. Startup materializes the agent before pool
construction and logs the effective command, argv, cwd, agent path, and
created/preserved status. Existing identity, permission-denial, and tool-alias
defenses were left unchanged. Admin documentation and the environment example
now describe the real defaults.

## Verification

- `go test ./... -count=1` — pass.
- `go vet ./...` — pass with no diagnostics.
- `go build ./cmd/otto-gateway` — pass.
- Installed `kiro-cli 2.13.0`; `kiro-cli acp --help` advertises
  `--agent <AGENT>`.
- Live default startup used `acp --agent acp_proxy`, created the expected file,
  and advertised `tools: []` plus `mcpServers: []` in ACP command metadata.
- ACP capture after live requests contained zero
  `session/request_permission` frames and zero identity-refusal frames.
- The live structured-tool-call harness passed on Anthropic, OpenAI, and
  Ollama.
- The original Warp scenario emitted a structured OpenAI `computer` call with
  `{"action":"launch","application":"Warp"}`; a supplied host result was
  accepted and acknowledged.
- Tool-result continuations, normal chat, and streaming passed on all three
  surfaces.
- A second live startup reported `agent_config_status:"preserved"`.

## Verification limitation

No `gateway-toolcall-parity` skill is installed in the surfaced project or
global skill directories, and this environment has no host computer-use
connector capable of actually launching Warp. The equivalent repository live
harness and manual protocol checks ran successfully, but physical host-tool
execution remains an out-of-band integration check.
