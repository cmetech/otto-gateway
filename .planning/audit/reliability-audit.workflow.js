export const meta = {
  name: 'otto-gateway-reliability-audit',
  description: 'Multi-agent production reliability audit of the OTTO Gateway Go LLM gateway for laptop-deployment robustness',
  phases: [
    { title: 'Find', detail: '12 parallel reviewers scan logical modules through the Go-reliability lens' },
    { title: 'Verify', detail: 'Each finding is independently challenged by 3 adversarial verifiers' },
    { title: 'Synthesize', detail: 'Confirmed findings merged into prioritized PRODUCTION-RELIABILITY-AUDIT.md' },
  ],
}

const REPO = '/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway'
const REPORT_PATH = `${REPO}/.planning/audit/PRODUCTION-RELIABILITY-AUDIT.md`

const RELIABILITY_LENS = `
RELIABILITY LENS — A finding is reportable ONLY if it matches one of these categories:

1. GOROUTINE LEAK — goroutine started without a guaranteed exit path. No context.Done() select, no done channel, blocks forever on send/recv to a channel whose other end can disappear. Especially: per-request goroutines that outlive client disconnect; subprocess readers that don't exit on process death; reaper / heartbeat / watcher loops without shutdown plumbing.
2. CHANNEL HAZARDS — send on closed channel (panic), double-close, close of nil channel, deadlock from circular wait, unbuffered channel where producer blocks if consumer is slow/gone.
3. DATA RACE — map read/write without lock, concurrent write to shared slice/struct, mutex acquired in inconsistent order across paths, atomic vs lock mixing on the same field. Anything 'go test -race' would flag.
4. PANIC ON HOT PATH — nil pointer dereference reachable from a request handler, type assertion without comma-ok, index out of range on user-controlled input, panic in a goroutine that has no recover() and crashes the whole process.
5. SUBPROCESS LIFECYCLE — zombie kiro-cli processes (PGID/process-group not cleaned), dead-process detection misses (exit_watcher gap), hung subprocess not replaced, ResetSignals/SIGINT propagation broken, race between pool replacement and active request using the dying slot.
6. CONTEXT CANCELLATION GAPS — client disconnect / handler context not propagated to the ACP request, ctx.Done() not selected in a blocking loop, child goroutines don't inherit cancellation, session/cancel never fires when HTTP client drops.
7. STREAM CORRUPTION — SSE event boundary broken (missing 'event:' or blank line), NDJSON line split or missing newline, http.ResponseWriter written after handler returns, Flush() called after Hijack, partial chunk forwarded then connection dies leaving downstream parser broken, mixed canonical-chunk encoding to a surface that can't represent it.
8. POOL / SESSION EXHAUSTION — all pool slots stuck waiting on a hung subprocess with no timeout/preemption, sessions registry leaks entries when reap path misses a code branch, idle reaper races a request that's mid-flight on the same session.
9. SURFACE COMPATIBILITY DRIFT — JSON struct tag missing/wrong/case-mismatch that silently drops a field clients depend on (Anthropic 'input' must be object not string; Ollama 'format' literal; OpenAI 'finish_reason' enum), required-by-spec response field omitted on edge cases, error-envelope shape diverges from spec under failure.
10. CONFIG MISVALIDATION — required env var (KIRO_CMD, POOL_SIZE) silently defaults to a broken value, AUTH_TOKEN unset accepted as 'auth disabled' without a loud warning at boot, EMBEDDING_MODEL_DEFAULT path that points at nothing, bad ALLOWED_IPS parse accepted silently.
11. AUTH / IP-ALLOWLIST GAP — handler bypasses AuthHook unintentionally (not the documented carve-outs for /admin and Ollama list stubs), bearer-token compare uses == instead of subtle.ConstantTimeCompare, IP allowlist trusts X-Forwarded-For without proxy verification, admin endpoint exposes data that wasn't intended to be auth-exempt.
12. GRACEFUL-SHUTDOWN GAP — server.Shutdown doesn't drain in-flight requests, kiro-cli children leak on parent exit, session reaper goroutine doesn't stop, plugin chain mid-execution loses state when context is cancelled mid-stream.
13. PANIC / ERROR SWALLOWED — recover() that logs nothing, error returned and ignored, defer that overwrites the named return with a zero value, structured log missing the failing field so operator can't diagnose.

DROP (do not report unless severity is high):
- Style / readability nits
- Test coverage gaps (unless a critical path has NO test and the path is the laptop hot path)
- Performance optimization (unless real DoS vector or laptop fan-spin scenario)
- "Could be more idiomatic" without behavioral consequence
- Linter cleanups not behavior-affecting

DEPLOYMENT POSTURE — single-user laptop:
- One operator. No SRE, no service supervisor — if the process crashes, nothing restarts it. Crash-free matters more than scale.
- POOL_SIZE=4 warm kiro-cli subprocesses. One user across multiple clients (LangFlow + Pi CLI + loop24-client tabs) can drive all 4 slots concurrently.
- Laptop sleep/wake events — TCP connections die, subprocesses may survive but ACP wire state may be stale. The gateway must recover, not wedge.
- No database. State lives in process memory: session registry, pool slot table, plugin chain config.
- Auth defaults to off if AUTH_TOKEN unset (matches Node version). /admin and Ollama list-stubs are intentional auth-exempt carve-outs (see PROJECT.md). Do NOT report these documented carve-outs as findings.
- Three SDKs depend on response shape (@anthropic-ai/sdk, openai, Ollama JS client). Silent JSON drift is a production failure on launch day.
`

const PROJECT_CONTEXT = `
PROJECT: OTTO Gateway — Go-based LLM gateway exposing OpenAI, Ollama, and Anthropic API surfaces on one port, routing canonical requests through a guardrails plugin chain to a pool of kiro-cli ACP worker subprocesses.
LOCATION: ${REPO}
STACK: Go 1.23+, stdlib net/http + chi, log/slog, env-driven config, single static binary (no cgo), subprocess pool via ACP protocol over stdio (JSON-RPC framed). No database. In-memory session registry + pool.

CRITICAL READING — read these before scanning your lane:
- ${REPO}/CLAUDE.md (project conventions + security carve-outs)
- ${REPO}/.planning/PROJECT.md (system overview, surface contracts, v1 carve-outs)
- ${REPO}/docs/briefs/go_port_brief.md (spec of record — only the sections relevant to your lane)
- ${REPO}/docs/operating.md (auth posture, accepted v1 risks, operator guidance)

ARCHITECTURE QUICK MAP:
- cmd/otto-gateway/main.go — boot, config load, pool init, server start, shutdown
- internal/canonical/ — provider-agnostic types (chat, chunk, errors, capabilities, model, stop_reason)
- internal/adapter/{ollama,openai,anthropic}/ — wire <-> canonical translation; SSE/NDJSON encoding
- internal/engine/ — accepts canonical requests, drives ACP via the pool, emits canonical chunks
- internal/acp/ — ACP JSON-RPC framing, client, dispatcher, stream demux, cross-platform PGID
- internal/pool/ — warm kiro-cli worker pool, exit watcher, slot lifecycle, stats
- internal/session/ — X-Session-Id stateful session registry + idle reaper
- internal/plugin/ — PreHook/PostHook chain (auth, logging, request_id, surface, trace) + PII redaction subpackage
- internal/server/ — chi router, middleware, /health, /health/agents, /pool, /hooks, sessions delete handler
- internal/admin/ — operator UI (auth-exempt by design), live SSE tail, snapshot, static assets
- internal/auth/ — bearer + IP allowlist primitives
- internal/config/ — env-var-driven config struct, validation

KEY CONVENTIONS:
- Canonical types are the trust boundary. Anything outside internal/canonical is an adapter or transport.
- Plugin hooks run on canonical types so policy applies uniformly across all three surfaces.
- Streaming: Ollama=NDJSON, OpenAI=SSE, Anthropic=SSE with anthropic-specific event names.
- Subprocess pool: pre-warmed before listener accepts; dead processes replaced by exit_watcher.
- v1 intentional carve-outs (NOT findings): /admin auth-exempt + Ollama list-stubs bypass AuthHook (IP allowlist still applies).

OUTPUT CONTRACT:
You MUST call the StructuredOutput tool with a JSON object matching the provided schema.
Do NOT write files. Do NOT add markdown commentary. Return findings only.
Each finding MUST be specific: real file path, real line number(s), real failure scenario, fix that maps to actual code.
If you find no reliability issues in your lane, return an empty findings array. Do not fabricate findings.
`

const LANES = [
  {
    key: 'engine-acp-driver',
    title: 'Engine: canonical → ACP driver',
    files: [
      'internal/engine/engine.go',
      'internal/engine/acp_adapter.go',
      'internal/engine/build_acp.go',
      'internal/engine/collect.go',
      'internal/engine/coerce.go',
      'internal/engine/hooks.go',
      'internal/engine/idle.go',
      'internal/engine/pickcwd.go',
    ],
    focus: 'The hot path: canonical request in → ACP RPC over pool slot → canonical chunks out. Context propagation from HTTP handler to ACP cancel. Goroutine leaks when client disconnects mid-stream. Coerce/collect panic surfaces. pickcwd longest-common-parent on edge inputs (empty, single, all distinct). Hooks panic handling — does a panicking hook crash the request or just the hook?',
  },
  {
    key: 'acp-protocol',
    title: 'ACP protocol: framer, client, stream, dispatcher',
    files: [
      'internal/acp/client.go',
      'internal/acp/framer.go',
      'internal/acp/stream.go',
      'internal/acp/dispatcher.go',
      'internal/acp/translate.go',
      'internal/acp/pool_pgid_unix.go',
      'internal/acp/pool_pgid_windows.go',
    ],
    focus: 'JSON-RPC framing edge cases (partial reads, oversized messages, malformed frames). Stream demux race when two sessions reuse a slot. Reader goroutine exit on subprocess EOF/death. session/cancel propagation. PGID/process-group cross-platform: zombie children on Windows, orphaned process groups on Unix. Dispatcher response correlation — what if a response arrives after request context cancel?',
  },
  {
    key: 'pool-worker',
    title: 'Worker pool: kiro-cli lifecycle, slot management, exit watcher',
    files: [
      'internal/pool/pool.go',
      'internal/pool/exit_watcher.go',
      'internal/pool/detail.go',
      'internal/pool/stats.go',
      'internal/pool/config.go',
    ],
    focus: 'Slot acquisition deadlock when all 4 slots stuck on hung subprocesses. Dead-process replacement race with active checkout. Exit watcher missing certain exit codes (signal-killed, OOM). Pool init failure path — if 2 of 4 kiro-cli fail to spawn at boot, does the gateway start in a degraded but visible state or wedge? Subprocess leaked on pool shutdown. ResetSignals / signal forwarding correctness.',
  },
  {
    key: 'session-registry',
    title: 'Session registry + reaper',
    files: [
      'internal/session/registry.go',
      'internal/session/reaper.go',
      'internal/session/entry_acp.go',
      'internal/session/stats.go',
      'internal/session/config.go',
    ],
    focus: 'Registry map concurrency (RWMutex coverage, atomicity of read-modify-write). Reaper TTL boundary race: reaping a session that just received a new request 1ms ago. Reaper goroutine exit on shutdown. Session entry holds subprocess slot — what happens when reap fires while ACP RPC is mid-flight? Memory growth if reaper misses a code branch (e.g., entries created but never accessed). X-Session-Id collision handling.',
  },
  {
    key: 'adapter-openai',
    title: 'OpenAI adapter: surface compat, SSE',
    files: [
      'internal/adapter/openai/adapter.go',
      'internal/adapter/openai/wire.go',
      'internal/adapter/openai/decode.go',
      'internal/adapter/openai/render.go',
      'internal/adapter/openai/sse.go',
      'internal/adapter/openai/handlers.go',
      'internal/adapter/openai/errors.go',
    ],
    focus: 'Surface drift: finish_reason enum, tool_calls shape, role enum, missing required fields. SSE event format: `data: <json>\\n\\n` boundaries, terminal `data: [DONE]\\n\\n`, flush on each chunk. Pi SDK depends on exact shape. Error envelope shape on 4xx/5xx. Streaming abort: what does the client see if engine errors mid-stream?',
  },
  {
    key: 'adapter-anthropic',
    title: 'Anthropic adapter: surface compat, SSE event names',
    files: [
      'internal/adapter/anthropic/adapter.go',
      'internal/adapter/anthropic/wire.go',
      'internal/adapter/anthropic/decode.go',
      'internal/adapter/anthropic/render.go',
      'internal/adapter/anthropic/sse.go',
      'internal/adapter/anthropic/handlers.go',
      'internal/adapter/anthropic/collect.go',
      'internal/adapter/anthropic/errors.go',
    ],
    focus: 'Anthropic-specific SSE event names (message_start, content_block_start/delta/stop, message_delta, message_stop). Tool-use input MUST be object not string. anthropic-version header validation. Both x-api-key AND Authorization: Bearer auth paths must work (loop24-client uses both). thinking blocks pass-through. Error envelope shape matches docs.anthropic.com spec. messages.stream() abort behavior.',
  },
  {
    key: 'adapter-ollama',
    title: 'Ollama adapter: surface compat, NDJSON, list stubs',
    files: [
      'internal/adapter/ollama/adapter.go',
      'internal/adapter/ollama/wire.go',
      'internal/adapter/ollama/decode.go',
      'internal/adapter/ollama/render.go',
      'internal/adapter/ollama/ndjson.go',
      'internal/adapter/ollama/handlers.go',
      'internal/adapter/ollama/stub.go',
    ],
    focus: 'NDJSON line discipline (newline-terminated, no internal newlines, flush each line). LangFlow flows depend on /api/chat shape — silent drift breaks deployed flows. Ollama list-mode stubs (/api/tags, /api/ps, /api/show, etc.) bypass AuthHook by design — confirm they DO honor IP allowlist and do NOT leak data unintentionally. `format` field parity. done_reason vs done boolean.',
  },
  {
    key: 'plugin-chain',
    title: 'Plugin chain + day-one hooks',
    files: [
      'internal/plugin/chain.go',
      'internal/plugin/auth.go',
      'internal/plugin/logging.go',
      'internal/plugin/request_id.go',
      'internal/plugin/surface.go',
      'internal/plugin/trace.go',
      'internal/plugin/jsonformat/jsonformat.go',
    ],
    focus: 'Hook ordering correctness. PreHook error path: does it short-circuit cleanly and return a proper error envelope on the right surface? PostHook on streaming responses — does the hook actually see chunks or only the terminal state? Panic in one hook crashes the request vs. the chain? Auth hook missing on a route that should require it (excluding documented carve-outs). RequestID uniqueness/propagation into logs.',
  },
  {
    key: 'plugin-pii',
    title: 'PII redaction plugin',
    files: [
      'internal/plugin/pii/pii.go',
      'internal/plugin/pii/recognizers.go',
      'internal/plugin/pii/contextual.go',
      'internal/plugin/pii/encrypt.go',
      'internal/plugin/pii/luhn.go',
      'internal/plugin/pii/modes.go',
      'internal/plugin/pii/ner.go',
      'internal/plugin/pii/spans.go',
      'internal/plugin/pii/summary.go',
      'internal/plugin/pii/walk.go',
    ],
    focus: 'PII walking the canonical message tree — does it cover tool_use input objects, content blocks, image data, system prompts? Edge inputs: extremely long strings, deeply nested structures (stack-overflow recursion risk), invalid UTF-8. Mode handling (off / log / redact / block) — does mode=block fail closed with a sensible error envelope on the calling surface? Encrypt path key handling.',
  },
  {
    key: 'server-handlers',
    title: 'HTTP server: routing, middleware, health, ops endpoints',
    files: [
      'internal/server/server.go',
      'internal/server/middleware.go',
      'internal/server/health.go',
      'internal/server/agents.go',
      'internal/server/pool_handler.go',
      'internal/server/hooks_handler.go',
      'internal/server/sessions_delete.go',
    ],
    focus: 'Middleware ordering — auth before request-body parse? Request size limits? http.ResponseWriter timeouts (ReadTimeout/WriteTimeout/IdleTimeout)? /health endpoint surfaces degraded subprocesses? Session delete handler races a session that is currently mid-RPC. Pool/hooks introspection endpoints leak state on misconfigured access. Panic recovery middleware — does it cover streaming handlers too?',
  },
  {
    key: 'admin-observability',
    title: 'Admin UI + observability (SSE tail, snapshot, assets)',
    files: [
      'internal/admin/admin.go',
      'internal/admin/sse.go',
      'internal/admin/tail.go',
      'internal/admin/snapshot.go',
      'internal/admin/assets.go',
    ],
    focus: 'Admin is auth-exempt by design — confirm no sensitive data leak beyond what operator should see locally. SSE tail backpressure when client is slow / disconnects mid-stream — goroutine leak risk. Log tail ring buffer concurrency. Snapshot endpoint heavy under load (single user clicking refresh). Static asset path traversal. Live tail goroutine exit on /admin tab close.',
  },
  {
    key: 'boot-config-shutdown',
    title: 'Boot path: main.go, config, auth primitives, shutdown',
    files: [
      'cmd/otto-gateway/main.go',
      'internal/config/config.go',
      'internal/auth/auth.go',
      'internal/auth/bearer.go',
      'internal/auth/ipallowlist.go',
    ],
    focus: 'Config: required env vars validated at boot or silently default? Pool init failure path. Listener bound before pool ready (race) — Bifrost pattern says pool warm before listen. Signal handling: SIGINT/SIGTERM triggers graceful shutdown with timeout. Subprocess children inherit signal or get orphaned. AUTH_TOKEN unset must log a clear "auth disabled" warning. bearer-token compare must be constant-time. IP allowlist CIDR parsing of malformed input. Reverse proxy / X-Forwarded-For trust posture (operator-deployed behind nginx).',
  },
]

const FINDING_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  properties: {
    lane: { type: 'string' },
    findings: {
      type: 'array',
      items: {
        type: 'object',
        additionalProperties: false,
        properties: {
          id: { type: 'string', description: 'Short stable slug, e.g. acp-stream-demux-cross-session-leak' },
          title: { type: 'string', description: 'One-line headline of the failure mode.' },
          severity: { type: 'string', enum: ['CRITICAL', 'HIGH', 'MEDIUM', 'LOW'] },
          category: {
            type: 'string',
            enum: [
              'GOROUTINE_LEAK',
              'CHANNEL_HAZARD',
              'DATA_RACE',
              'PANIC_HOT_PATH',
              'SUBPROCESS_LIFECYCLE',
              'CONTEXT_CANCELLATION',
              'STREAM_CORRUPTION',
              'POOL_SESSION_EXHAUSTION',
              'SURFACE_COMPAT_DRIFT',
              'CONFIG_MISVALIDATION',
              'AUTH_GAP',
              'SHUTDOWN_GAP',
              'ERROR_SWALLOWED',
            ],
          },
          location: { type: 'string', description: 'file_path:line_number (or :line_start-line_end). Must be a real path under the repo.' },
          scenario: { type: 'string', description: 'Concrete failure scenario — payload shape, network condition, race, sequence of events that triggers the bug.' },
          current_behavior: { type: 'string', description: 'What the code does today when the scenario hits.' },
          recommended_fix: { type: 'string', description: 'Specific change — names of functions, conditions, defaults. Specific enough that /gsd-fast can act on it without re-investigation.' },
          complexity: { type: 'string', enum: ['small', 'medium', 'large'] },
          observability_impact: { type: 'string', description: 'Operator-visibility impact — what slog event / /health field / /admin signal should fire that does not. Use empty string if not an observability finding.' },
        },
        required: ['id', 'title', 'severity', 'category', 'location', 'scenario', 'current_behavior', 'recommended_fix', 'complexity', 'observability_impact'],
      },
    },
    already_hardened: {
      type: 'array',
      description: 'Areas in this lane where defensive coding looks solid — for the "Already Hardened" section. Each entry is a short noun phrase + file ref.',
      items: { type: 'string' },
    },
  },
  required: ['lane', 'findings', 'already_hardened'],
}

const VERDICT_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  properties: {
    finding_id: { type: 'string' },
    verdict: { type: 'string', enum: ['REAL', 'REFUTED', 'ALREADY_HANDLED'] },
    confidence: { type: 'string', enum: ['HIGH', 'MEDIUM', 'LOW'] },
    reasoning: { type: 'string', description: 'Why REAL or REFUTED. If ALREADY_HANDLED, cite the file:line where the protection exists.' },
    severity_adjustment: { type: 'string', enum: ['NONE', 'RAISE', 'LOWER'], description: 'If REAL but severity should be reconsidered.' },
    suggested_severity: { type: 'string', enum: ['CRITICAL', 'HIGH', 'MEDIUM', 'LOW', 'NONE'] },
  },
  required: ['finding_id', 'verdict', 'confidence', 'reasoning', 'severity_adjustment', 'suggested_severity'],
}

const SYNTHESIS_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  properties: {
    report_written_to: { type: 'string' },
    counts: {
      type: 'object',
      additionalProperties: false,
      properties: {
        critical: { type: 'integer' },
        high: { type: 'integer' },
        medium: { type: 'integer' },
        low: { type: 'integer' },
      },
      required: ['critical', 'high', 'medium', 'low'],
    },
    top_5_for_laptop_launch: {
      type: 'array',
      items: { type: 'string' },
      minItems: 0,
      maxItems: 5,
    },
  },
  required: ['report_written_to', 'counts', 'top_5_for_laptop_launch'],
}

phase('Find')

const finderPromise = (lane) => () => agent(
  `${PROJECT_CONTEXT}

${RELIABILITY_LENS}

YOUR LANE: ${lane.title}
FILES IN SCOPE (read all, plus any close collaborators they import):
${lane.files.map((f) => `  - ${f}`).join('\n')}

LANE-SPECIFIC FOCUS:
${lane.focus}

PROCESS:
1. Read CLAUDE.md and .planning/PROJECT.md briefly for conventions + carve-outs.
2. Read every file in scope. Follow imports into close collaborators when behavior depends on them.
3. For each potential issue, verify it against actual code before reporting.
4. Drop anything outside the reliability lens.
5. Map every finding to file:line — no abstract findings.
6. Skip the documented v1 carve-outs (/admin auth-exempt, Ollama list-stubs bypass) — these are NOT findings.
7. Return structured JSON via StructuredOutput. lane = "${lane.key}".

CALIBRATION: This runs on a single user's laptop. Bias CRITICAL/HIGH toward "would crash the process, leak goroutines/zombies until restart, or break a client SDK silently". A bug that requires 100 concurrent users to manifest is not critical here.`,
  { label: `find:${lane.key}`, phase: 'Find', schema: FINDING_SCHEMA },
)

const verifyOne = async (finding, laneKey) => {
  const lensList = ['correctness', 'already-handled', 'severity-calibration']
  const votes = await parallel(lensList.map((lens) => () => agent(
    `${PROJECT_CONTEXT}

You are an ADVERSARIAL VERIFIER. Default to REFUTED if uncertain. Your bias is to find why the finding is wrong.

FINDING TO CHALLENGE:
  id: ${finding.id}
  title: ${finding.title}
  severity: ${finding.severity}
  category: ${finding.category}
  location: ${finding.location}
  scenario: ${finding.scenario}
  current_behavior: ${finding.current_behavior}
  recommended_fix: ${finding.recommended_fix}

VERIFICATION LENS for this run: ${lens}
${lens === 'correctness' ? '→ Read the cited file and surrounding code. Does the described scenario actually happen given the real code? Trace the goroutine/channel/context flow. If the scenario premise is wrong, REFUTED.' : ''}
${lens === 'already-handled' ? '→ Look for existing protection: recover(), context propagation, mutex coverage, defer cleanup, atomic ops, timeout, retry, validation, signal handling. Grep for related defensive code. If protection already exists, return ALREADY_HANDLED with file:line.' : ''}
${lens === 'severity-calibration' ? '→ Assume the bug is real. Question severity for a SINGLE-USER LAPTOP DEPLOYMENT. Would it crash the process / leak resources until restart (CRITICAL)? Visible to the user without operator notification (HIGH)? Degraded behavior with some visibility (MEDIUM)? Cosmetic gap (LOW)? Suggest a corrected severity. Remember: no SRE, no service supervisor — crashes mean manual restart.' : ''}

Return StructuredOutput JSON. finding_id = "${finding.id}".`,
    { label: `verify:${finding.id}:${lens}`, phase: 'Verify', schema: VERDICT_SCHEMA },
  )))
  const valid = votes.filter(Boolean)
  if (valid.length === 0) return null
  const refuted = valid.filter((v) => v.verdict === 'REFUTED')
  const alreadyHandled = valid.find((v) => v.verdict === 'ALREADY_HANDLED')
  if (refuted.length >= 2) return { finding, kept: false, reason: 'refuted', votes: valid }
  if (alreadyHandled && valid.filter((v) => v.verdict === 'ALREADY_HANDLED').length >= 2) {
    return { finding, kept: false, reason: 'already_handled', votes: valid, evidence: alreadyHandled.reasoning }
  }
  const severityVotes = valid.map((v) => v.suggested_severity).filter((s) => s && s !== 'NONE')
  let finalSeverity = finding.severity
  if (severityVotes.length >= 2) {
    const counts = severityVotes.reduce((a, s) => { a[s] = (a[s] || 0) + 1; return a }, {})
    const top = Object.entries(counts).sort((a, b) => b[1] - a[1])[0]
    if (top[1] >= 2) finalSeverity = top[0]
  }
  return { finding: { ...finding, severity: finalSeverity }, kept: true, votes: valid, laneKey }
}

const laneResults = await parallel(LANES.map((lane) => finderPromise(lane)))
const validLaneResults = laneResults.filter(Boolean)
const totalFindings = validLaneResults.reduce((n, r) => n + (r.findings?.length || 0), 0)
log(`Find complete: ${validLaneResults.length}/${LANES.length} lanes returned, ${totalFindings} raw findings`)

phase('Verify')

const verified = []
const verifyTasks = []
for (const laneResult of validLaneResults) {
  for (const finding of laneResult.findings || []) {
    verifyTasks.push({ finding, laneKey: laneResult.lane })
  }
}

const verifiedRaw = await parallel(verifyTasks.map((t) => () => verifyOne(t.finding, t.laneKey)))
for (const v of verifiedRaw) {
  if (v && v.kept) verified.push(v)
}

const dropped = verifiedRaw.filter((v) => v && !v.kept).length
log(`Verify complete: ${verified.length} confirmed, ${dropped} dropped`)

const alreadyHardenedAggregate = []
for (const laneResult of validLaneResults) {
  for (const note of laneResult.already_hardened || []) alreadyHardenedAggregate.push({ lane: laneResult.lane, note })
}

phase('Synthesize')

const synthesisInput = {
  confirmed_findings: verified.map((v) => v.finding),
  already_hardened: alreadyHardenedAggregate,
  total_lanes: LANES.length,
  total_raw_findings: totalFindings,
  total_dropped_in_verify: dropped,
  total_confirmed: verified.length,
}

const synthesis = await agent(
  `${PROJECT_CONTEXT}

You are the SYNTHESIS agent. You receive verified findings and write the final audit report.

CONFIRMED FINDINGS + STATS (JSON):
${JSON.stringify(synthesisInput, null, 2)}

YOUR JOB:
1. Deduplicate findings that describe the same root issue across lanes (merge their fixes, keep the higher severity).
2. Group by severity. Within each severity group, order by category then by file path.
3. Write the final markdown report to ${REPORT_PATH}.
4. Use this exact structure:

\`\`\`
# OTTO Gateway Production Reliability Audit
**Date:** 2026-06-06
**Scope:** OTTO Gateway Go LLM gateway, single-user laptop deployment posture
**Agents spawned:** <fill in: 12 finders + (3 × confirmed-count) verifiers + 1 synthesis>
**Raw findings:** ${totalFindings}  |  **Confirmed:** ${verified.length}  |  **Dropped in verify:** ${dropped}

## Critical (process crash, resource leak until restart, or silent SDK breakage)
<for each CRITICAL finding>
### <id> — <title>
- **Location:** \`<file:line>\`
- **Category:** <category>
- **Scenario:** <scenario>
- **Current behavior:** <current_behavior>
- **Recommended fix:** <recommended_fix>
- **Complexity:** <complexity>
- **Observability impact:** <observability_impact if non-empty>
</for>

## High (user-visible failure without operator notification)
<same shape>

## Medium (degraded behavior with at least some visibility)
<same shape>

## Low (cosmetic observability gaps — not blocking laptop launch)
<same shape>

## Already Hardened (defensive coding worth noting)
<bullet list of de-duplicated notes from already_hardened_aggregate, grouped by lane>

## Top 5 Things To Fix Before Daily-Driver Use
<numbered list — derive these by picking the 5 confirmed findings most likely to bite a single laptop user (process crash, zombie subprocess, goroutine leak across sleep/wake, SDK-breaking surface drift, auth-disabled-without-warning at boot). Each entry: "<id>: <one-sentence reason>".>
\`\`\`

5. After writing the file, return StructuredOutput JSON with the report path, severity counts, and top 5 IDs.

DO NOT invent findings. DO NOT change a finding's location to one you didn't see. ONLY use the confirmed findings provided.`,
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTHESIS_SCHEMA },
)

return {
  report: synthesis?.report_written_to ?? REPORT_PATH,
  counts: synthesis?.counts ?? null,
  top_5: synthesis?.top_5_for_laptop_launch ?? [],
  raw_findings: totalFindings,
  confirmed: verified.length,
  dropped_in_verify: dropped,
}
