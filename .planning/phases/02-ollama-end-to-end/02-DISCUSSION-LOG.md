# Phase 2: Ollama End-to-End - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-23
**Phase:** 2-Ollama End-to-End
**Areas discussed:** Engine surface shape, ACP concurrency model, Canonical request type, Model catalog source

Plus an in-discussion discovery: ACP wire-shape gaps in Phase 1 code (10 specific defects) that triggered a roadmap edit to insert Phase 1.1: ACP Wire Alignment before Phase 2 plan execution.

---

## Engine surface shape

### Q1: What should `internal/engine` expose for Phase 2?

| Option | Description | Selected |
|--------|-------------|----------|
| Channel + Chat helper | engine.Run returns *Run with Chunks channel + Result(); engine.Collect helper wraps Run for non-streaming | ✓ |
| Channel-only | engine.Run only; adapter ranges over Chunks itself for non-streaming collection | |
| Collect-only now, Run later | Phase 2 ships Engine.Collect only; Phase 4 adds Run channel and refactors Collect to wrap it | |

**User's choice:** Channel + Chat helper.
**Notes:** Mirrors Phase 1's `acp.Client.Prompt → *Stream` pattern. Phase 4 SSE/NDJSON adapter ranges over Chunks directly; Phase 2 non-streaming adapter calls Collect.

---

### Q2: Where should the canonical-message → ACP-text-block flattening live?

| Option | Description | Selected |
|--------|-------------|----------|
| Inside engine package | engine.buildBlocks single source of truth; all adapters speak canonical | ✓ |
| Per-adapter | Each adapter builds its own []canonical.Block | |
| Shared helper, used by engine | internal/blockbuild package; same outcome as engine-internal but separately testable | |

**User's choice:** Inside engine package.
**Notes:** Single source of truth for `[System]/[User]/[Tool result]/[Available tools]/[Reasoning]/[Output format]` bracket conventions across all 3 surfaces. Forces canonical.ChatRequest to normalize System/Tools/Think/Format scalars.

---

### Q3: How should the engine be constructed and how does it consume ACP?

| Option | Description | Selected |
|--------|-------------|----------|
| Concrete struct + consumer-defined ACPClient interface | engine.Engine concrete; engine.ACPClient interface defined in engine package; *acp.Client satisfies structurally | ✓ |
| Engine interface + private impl | Engine as interface; engine.New returns interface; private *engineImpl | |
| Concrete struct, concrete *acp.Client dep | No interface boundary; engine holds *acp.Client directly | |

**User's choice:** Concrete struct + consumer-defined ACPClient interface.
**Notes:** Stdlib pattern (interface at the consumer side, concrete real impl). Tests inject fakes via the local engine.ACPClient interface. Phase 8 hook fields land in the same struct. Pool will satisfy this same interface in Phase 2 (D-06).

---

### Q4: Should Phase 2 leave a structural seam for the Phase 8 hook chain?

| Option | Description | Selected |
|--------|-------------|----------|
| No seam — add in Phase 8 | Engine.Run goes straight; Phase 8 inserts hook chain | |
| Empty hook slices, seam present | preHooks/postHooks fields empty in Phase 2; PreHook/PostHook interfaces defined now | ✓ |
| Comment placeholder only | Insertion sites marked with `// PHASE_8:` comments but no fields/interfaces | |

**User's choice:** Empty hook slices, seam present.
**Notes:** REVISED after user pushback on initial "no seam" recommendation. User correctly noted that the PreHook/PostHook chain is project-of-record (PLUG-01..05 + PROJECT.md Key Decisions: "one place to enforce policy") — not a speculative future feature. Cost: ~17 lines of no-op scaffolding now. Buys clean Phase 8 diff + visible design. Saved to memory: feedback-locked-design-seams.

---

### Q5: Session lifecycle inside engine.Run for Phase 2?

| Option | Description | Selected |
|--------|-------------|----------|
| New session per request | Each Run calls acp.NewSession(ctx, cwd) → optional SetModel → Prompt | ✓ |
| Persistent gateway-lifetime session | Engine creates one session at boot, reuses for every Run | |
| Cached session with cwd-keyed reuse | Engine maintains cwd → sessionID map | |

**User's choice:** New session per request.
**Notes:** Matches Node's stateless behavior. cwd derived per-request via pickCwd(req). Phase 5 pool slot inherits the pattern. ACP-07 (per-request cwd) requires this.

---

## ACP concurrency model

### Q6: How should Phase 2 handle concurrent /api/chat against a single acp.Client?

| Option | Description | Selected |
|--------|-------------|----------|
| Single-slot adapter satisfying engine.ACPClient | internal/engine/singleslot.go wraps *acp.Client with mutex | |
| Mutex directly inside Engine | Engine has its own sync.Mutex | |
| Pool-of-1 in internal/pool now | Phase 2 creates internal/pool package size=1; Phase 5 bumps default + adds dead-slot detection | ✓ |

**User's choice:** Pool-of-1 in internal/pool now.
**Notes:** REVISED after user pushback on initial "single-slot adapter" recommendation. User correctly applied the locked-design-seams rule: POOL-03 (channel-of-slots API) is project-of-record. Pool satisfies engine.ACPClient interface. Requirements remap: POOL-01 (size=1 part) + POOL-02 + POOL-03 move to Phase 2; POOL-04 (dead-slot detection) stays Phase 5.

---

### Q7: What should the default POOL_SIZE be in Phase 2?

| Option | Description | Selected |
|--------|-------------|----------|
| Default 1, env-override to higher | POOL_SIZE=1 default; env var bumps to 4+ for testing | ✓ |
| Default 4 from day one | Match Node + PROJECT.md default; spawns 4 kiro-cli subprocesses at warmup | |

**User's choice:** Default 1, env-override to higher.
**Notes:** Cheap boot (one kiro-cli subprocess), modest memory. Matches roadmap framing of Phase 2 as "single-session" and Phase 5 as "real warm pool." Phase 5 bumps default to 4.

---

### Q8: How should pool warmup handle slot failures?

| Option | Description | Selected |
|--------|-------------|----------|
| Fail-fast: all slots must initialize | Sequential warmup; any failure → close partials, return error, main.go exits non-zero | ✓ |
| Degraded: keep what works | Warm whatever succeeds, log failures; require at least 1 slot | |
| Parallel warmup, fail-fast | errgroup.Group concurrent warmup; same failure semantics | |

**User's choice:** Fail-fast: all slots must initialize.
**Notes:** Sequential is fine for size=1. Phase 5 may switch to parallel via errgroup when size=4 makes serial spawn visibly slow. POOL-04 (dead-slot re-spawn) takes over for slots that die after warmup.

---

## Canonical request type

### Q9: How forward-designed should canonical.ChatRequest be?

| Option | Description | Selected |
|--------|-------------|----------|
| Tri-surface forward-designed | All fields all 3 surfaces need; Phase 2 only populates active ones | ✓ |
| Phase-2-minimal | Only Model + Messages + Options; extend per phase | |
| Forward-designed scalars, deferred multipart | All scalars now; Message.Content string in Phase 2, []ContentPart in Phase 3.1 | |

**User's choice:** Tri-surface forward-designed.
**Notes:** Messages carry []ContentPart discriminated-union (text/image/tool_use/tool_result/thinking) from day one. Phase 3/3.1/6 activate dormant fields with zero canonical-type churn. Applies the locked-design-seams rule: all 3 surfaces' wire shapes are in REQUIREMENTS, so canonical type can be designed forward.

---

### Q10: How forward-designed should canonical.ChatResponse be?

| Option | Description | Selected |
|--------|-------------|----------|
| Tri-surface forward-designed, symmetric | Mirror ChatRequest's forward design; ID + Model + Message + StopReason + Usage | ✓ |
| Phase-2-minimal | Only Ollama needs: Model + Message + EvalCount + PromptEval | |

**User's choice:** Tri-surface forward-designed, symmetric.
**Notes:** StopReason enum covers all 3 surfaces (end_turn/max_tokens/stop_sequence/tool_use/error). Usage carries Anthropic-specific cache fields too. Adapters render canonical response in their surface shape.

---

### Q11: Should canonical types carry JSON tags?

| Option | Description | Selected |
|--------|-------------|----------|
| No JSON tags — canonical is wire-agnostic | Adapters own all wire-format translation | ✓ |
| JSON tags on canonical types | Stable JSON names for logging/debugging | |

**User's choice:** No JSON tags.
**Notes:** Forces clean separation. JSON tags in this codebase signal "adapter or wire helper." Tests use cmp.Diff, not JSON round-trip. Matches Bifrost's discipline.

---

## In-discussion discovery: ACP wire-shape gap

User asked: "Are our canonical messages compliant with what kiro-cli expects? Has the JS reference been analyzed?"

Investigation steps taken:
1. Read `internal/acp/translate.go` and `client.go` to confirm Phase 1 wire shapes.
2. Read `internal/acp/integration_test.go` — discovered the real-kiro smoke test only exercises `Initialize → NewSession → Ping`, never `session/prompt`.
3. Fetched https://agentclientprotocol.com/protocol/initialization.md, /content.md, /prompt-turn.md — discovered current ACP spec.
4. Fetched https://kiro.dev/docs/cli/acp/ — discovered Kiro docs page contradicts the upstream spec on notification method name.
5. Cross-referenced with `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-ollama-server.js` (the Node code that works against kiro-cli 2.4.1 today) — established ground truth.

**Confirmed 10 wire-shape defects in Phase 1 code:**

1. `initialize` params missing `protocolVersion: 1`; uses `capabilities` instead of `clientCapabilities`.
2. `session/new` params missing `mcpServers: []`.
3. `session/new` result reading misses `result.id` fallback AND misses `result.models.availableModels[]` (where model catalog lives).
4. `session/prompt` params uses field name `blocks`; kiro-cli reads `prompt`/`content` — every Phase 2 prompt would arrive empty.
5. Notification method handling missing `session/notification` alias.
6. Notification body parsing missing `params.update` unwrap.
7. Notification discriminator field reads `type`; spec field is `sessionUpdate`.
8. Notification content extraction missing `body.content?.text` (object-form) and `body.text` fallbacks.
9. Notification type vocabulary completely wrong: code expects `text`/`thought`/`tool_call`/`plan`; kiro emits `agent_message_chunk`/`agent_thought_chunk`/`tool_call`/`tool_call_chunk`/`tool_call_update`/`plan`.
10. `session/request_permission` handled as a separate `session/grant_permission` request with new id; spec says respond to the original id with `{result:{optionId,granted}}`. Wrong RPC pattern — deadlocks kiro-cli.

Captured ground-truth reference: `docs/reference/acp_wire_shapes.md`.

### Q-extra: How should we close the gap?

| Option | Description | Selected |
|--------|-------------|----------|
| Insert Phase 1.1: ACP Wire Alignment | New phase between 1 and 2; clean Phase 2 starts after | ✓ |
| Wave 0 of Phase 2 | Bundle alignment work into Phase 2's plan; no roadmap edit | |
| Verification spike first | Capture raw kiro-cli wire frames to confirm reference doc, then re-decide | |

**User's choice:** Insert Phase 1.1: ACP Wire Alignment.
**Notes:** Cleanest separation. Phase 2's success criteria stay focused on "first end-to-end vertical slice" instead of mixing Phase 1 cleanup. Mirrors how Phase 3.1 was inserted into ROADMAP.md. Acceptance gate: spec-compliant initialize/session/new/session/prompt round-trip succeeds against real kiro-cli 2.4.1.

---

## Model catalog source

### Q12: Model catalog source and lifecycle for /api/tags and /api/show?

| Option | Description | Selected |
|--------|-------------|----------|
| Capture once at pool warmup, cache in process | Pool's first slot session/new yields availableModels; pool stores list | ✓ |
| Capture lazily on first /api/tags call | Don't bother kiro at warmup; lazy-init on first tags call | |
| Env-var override list, kiro-cli fallback | OLLAMA_MODELS env var if set, else kiro-cli at warmup | |

**User's choice:** Capture once at pool warmup, cache in process.
**Notes:** Pool stores ModelInfo{ID, Name} once. /api/tags returns Ollama shape with sensible defaults for metadata kiro doesn't provide (size:0, digest:"sha256:placeholder", details:{family:"kiro", parameter_size:"unknown"}). /api/show looks up by name + stubs template/parameters/license/modelfile. Refresh requires gateway restart.

---

## Claude's Discretion

Planner has latitude on:

- Exact chi sub-router composition for D-14 (auth + IP allowlist).
- Exact Ollama metadata default values in D-13 (`modified_at`, `digest`, `size`, `family`).
- Whether `internal/pool.Config` is passed by value or pointer.
- Whether `engine.ChatResponse` assembly lives in `engine/assemble.go` or inline in `engine/collect.go`.
- Test package strategy per file (whitebox vs blackbox).
- Error message wording, log key naming (consistent with Phase 1).
- Exact HTTP status codes for various error conditions (follow Ollama conventions).

## Deferred Ideas

- Streaming for `/api/chat` → Phase 4
- Dead-slot detection + lazy re-spawn → Phase 5 (POOL-04)
- Parallel warmup → Phase 5 (when size=4 default lands)
- Stateful sessions via `X-Session-Id` → Phase 5 (SESS-*)
- `/health/agents` → Phase 5 (OBSV-02)
- `coerceToolCall` → Phase 6 (TOOL-02)
- Tool-call rendering in responses → Phase 6 (TOOL-01)
- Tool spec normalization → Phase 6 (TOOL-03)
- `AuthHook` (engine plugin refactor) → Phase 8 (PLUG-04)
- Property tests for `pickCwd` → start in Phase 2, expand as relevant functions land
- `Example_` documentation functions → added per phase as public funcs land

## Parked gray areas (could be discussed in Phase 2 planning if needed)

- Auth + IP allowlist chi composition specifics — partially captured in D-14 but planner has latitude.
- Stub endpoint exact response shapes (/api/pull, /api/push, /api/create, /api/copy, /api/delete).
- Per-request cwd derivation edge case coverage (Windows-style paths, mixed schemes).
- Test strategy details (fake-engine for adapter tests; real-kiro for end-to-end /api/chat).
