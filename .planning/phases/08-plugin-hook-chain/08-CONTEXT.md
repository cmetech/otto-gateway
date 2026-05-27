# Phase 8: Plugin Hook Chain - Context

**Gathered:** 2026-05-27
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 8 wires up the **plugin Pre/Post hook chain** that was forward-designed
in Phase 2 (D-04, REQ-PLUGIN-01). The seam interfaces in
`internal/engine/hooks.go` (`PreHook.Before`, `PostHook.After`) and the
short-circuit semantics in `internal/engine/collect.go` (Codex H-4 / H-5) are
already shipped and stable; Phase 8 lights up four concrete hooks against
them and exposes the chain over a view-only health endpoint.

**Net-new work in Phase 8:**

1. **`internal/plugin` package** populated from its current `.gitkeep`
   placeholder. Holds the four concrete hooks and the `PIIRedactionHook`'s
   `Recognizer` registry. **No registry abstraction** — see D-01.
2. **`RequestIDHook` (Pre+Post).** Generates `X-Request-Id` (or accepts an
   inbound one), attaches it to `ctx` via a typed key, and exposes it to
   the `slog` correlation seam in all spans (pre-hook, engine, ACP,
   post-hook). Closes PLUG-04 + OBSV-03.
3. **`AuthHook` (Pre, short-circuit-capable).** Refactor of bearer-token
   validation out of `internal/auth/bearer.go` HTTP middleware into a
   canonical-typed hook. On failure, short-circuits the engine by returning
   `*canonical.ChatResponse` whose error field the surface adapter renders
   in its native shape (OpenAI `{error:{...}}` vs Ollama `{error:"..."}` vs
   Anthropic `{type:"error", error:{...}}`). Existing IP-allowlist + the
   `/`, `/api/version`, `/health`, `/health/agents` exempt-path middleware
   stays at the HTTP layer (those gates run before the engine and are not
   canonical-typed concerns). Closes PLUG-04 (auth side).
4. **`LoggingHook` (Pre+Post).** Structured `slog.Info` on entry (Pre) and
   exit (Post) with timing. Sees REDACTED content per D-04. Optionally
   emits a one-line redaction summary (`{Email:2, SSN:1}`) — planner's
   discretion. Closes PLUG-04 (logging side).
5. **`PIIRedactionHook` (Pre).** Recognizer-registry based PII scrubber.
   Six initial recognizers (Email, IPv4, IPv6, SSN, Credit Card with Luhn,
   US Phone). Walks `Message.ContentParts[].Text` + `tool_use.Input` +
   `tool_result.Content` recursively per D-03. Mutates the request in
   place; later Pre hooks (Logging) see redacted content. Closes PLUG-06.
6. **Chain runner.** `internal/plugin/chain.go` (or equivalent) — first
   non-nil short-circuit on the Pre chain wins; all Post hooks run
   unconditionally on the assembled response. Already 80% baked in
   `engine/collect.go`; Phase 8 surfaces the explicit `Chain` type that
   `main.go` constructs and `engine.Config` consumes.
7. **`GET /health/hooks` view-only introspection.** Returns the registered
   chain in registration order as JSON: `name`, `kind` (`Pre`, `Post`, or
   `Pre,Post`), `enabled` (reflects `ENABLED_HOOKS` allowlist), `config`
   (safe-to-publish fields only). Auth-exempt like `/health` and
   `/health/agents`. No mutate path — restart to change. Closes OBSV-04.
8. **`cmd/otto-gateway/main.go` wiring.** Constructs the hardcoded
   `plugin.Chain{}` slice per D-01, applies the `ENABLED_HOOKS` allowlist
   filter per D-02, passes to `engine.Config.PreHooks` /
   `engine.Config.PostHooks` (the existing seam from Phase 2).
9. **Env loading + validation.** `PII_REDACTION_ENABLED`,
   `PII_ENABLED_ENTITIES`, `PII_REDACTION_MODE`, `PII_HASH_KEY`,
   `ENABLED_HOOKS`, `LOG_RAW_REQUEST` (if planner adds it — see D-04
   note). Validation includes the hard-fail boot errors in D-02 (unknown
   ENABLED_HOOKS name) and D-05 (mode=hash with no PII_HASH_KEY).

**Requirements covered (per ROADMAP.md):** PLUG-01, PLUG-02, PLUG-03,
PLUG-04, PLUG-05, PLUG-06, OBSV-03, OBSV-04.

**Out of scope (locked):**

- **Runtime hook mutation / hot reload.** No `POST /admin/hooks/...`
  endpoint; no SIGHUP re-read; no config-file watcher. PROJECT.md line 108
  + this phase's SC7. Restart to change config.
- **Dynamic plugin loading** (Go `plugin` package, `dlopen`, etc.).
  Violates the static-binary + no-cgo CLAUDE.md constraints. Plugins are
  Go types registered at compile time.
- **Admin listener / admin-only auth surface.** No separate port, no
  `ADMIN_TOKEN`, no audit log. `/health/hooks` is enough for v1.
- **Output-side moderation / safety filtering.** PII redaction is INPUT
  side only (Pre chain on the request). Response-side scrubbing belongs
  to a future content-moderation hook (`PLUG-V2-01`) on the Post chain.
- **Per-request hook config override** (e.g., header `X-Skip-PII: true`).
  Hooks are deployment-level policy; per-request bypasses are a guardrail
  defeat primitive on the network. Defer.
- **NER / language-model-based PII detection.** Recognizer registry is
  regex + post-validator only. Pure Go, no cgo. Adding a Go-native NER
  library (`jdkato/prose/v2`) is a v2 conversation if recall becomes a
  real complaint.
- **`PLUG-V2-01..05`** (moderation, schema validation, budget, semantic
  cache, audit log) — these are the future hooks the Phase 8 chain
  architecture enables. Phase 8 ships the seam + the four day-one hooks;
  the v2 cluster lands when a real customer need surfaces.
- **Tool-call argument PII** (deep scrubbing of OpenAI-style function
  arguments) — D-03 already covers `tool_use.Input` recursively, so this
  is in-scope; calling out only that we don't try to be smart about
  field names (e.g., "this field is named `email` so skip the regex").
  Field-name awareness is a brittle hint; the regex is the source of
  truth.

</domain>

<decisions>
## Implementation Decisions

### Registration architecture

- **D-01: Hardcoded `plugin.Chain{}` slice in `cmd/otto-gateway/main.go`.**
  The single source of truth for what's in the chain is one literal slice
  in `main.go`:
  ```go
  chain := plugin.Chain{
      &plugin.RequestIDHook{},
      &plugin.AuthHook{Tokens: cfg.AuthToken},
      &plugin.PIIRedactionHook{Recognizers: pii.Recognizers, Mode: cfg.PIIMode, ...},
      &plugin.LoggingHook{Logger: slog.Default()},
  }
  ```
  Same pattern one level deeper for PII recognizers:
  ```go
  // internal/plugin/pii/recognizers.go
  var Recognizers = []Recognizer{
      {Name: "Email", Pattern: emailRe, Validate: nil},
      {Name: "IPv4",  Pattern: ipv4Re,  Validate: validateIPv4Octets},
      // ...
  }
  ```
  - **Why not a `Register(name, factory)` registry package:** the
    indirection adds boot-time complexity (factory lookup, name
    validation, init-order surprises) for zero v1 benefit. With a
    hardcoded slice, adding a new hook is one line in `main.go`; with a
    registry, it's a `Register()` call + a `Build()` lookup + an entry in
    `ENABLED_HOOKS` documentation. Bifrost-style hardcoded chains are the
    proven pattern for our scale (`~/Projects/repos/local/bifrost`).
  - **Why not `init()` self-registration:** Go init-order is famously
    surprising and obscures the dependency graph. The compile-time slice
    is grep-friendly and reviewable.
  - **Test isolation:** unit tests construct their own `plugin.Chain{}`
    with fake hooks; they never import `main.go`'s chain.

- **D-02: `ENABLED_HOOKS` is an allowlist; empty/unset = all enabled.**
  - Format: comma-separated hook type names (e.g., `RequestIDHook,LoggingHook`).
  - Unset → every hook in `main.go`'s slice runs (default-permissive,
    matches `AUTH_TOKEN` semantics).
  - Set with a name not present in the slice → boot error
    (`unknown hook in ENABLED_HOOKS: FooHook`). Typo protection is
    load-bearing — a silent typo could disable PII redaction.
  - Filter happens once in `main.go` between slice construction and
    `engine.Config{PreHooks/PostHooks}` assignment.
  - Execution order is registration order in the slice (not
    `ENABLED_HOOKS` list order). SC5 locks this.
  - **Composition with `PII_REDACTION_ENABLED`:** the env knobs operate at
    different layers and don't collapse. `ENABLED_HOOKS` controls
    whether `PIIRedactionHook` is IN THE CHAIN AT ALL.
    `PII_REDACTION_ENABLED` controls whether the hook DOES WORK when
    invoked. Default-state for fresh deploys: hook IS in the chain
    (ENABLED_HOOKS unset → all enabled) but no-ops
    (PII_REDACTION_ENABLED=false default). Operator must explicitly opt
    in to PII scrubbing. `/health/hooks` shows
    `{name: PIIRedactionHook, enabled: true, config:{enabled: false, ...}}`
    which is verbose-but-honest.

### PII content-part scope

- **D-03: Walk Text + tool_use.Input + tool_result.Content recursively;
  string leaves only.** The PII walker visits:
  1. Every `Message.ContentParts[i]` where `Kind == ContentKindText` →
     run all enabled recognizers on `.Text`, replace in place.
  2. Every `Message.ContentParts[i]` where `Kind == ContentKindToolUse` →
     recurse into `.ToolUse.Input` (which is `*map[string]any`); for each
     `string` leaf at any depth, run recognizers, replace in place. **Map
     keys are NOT walked** (they're field names, not user content). **Non-
     string leaves** (numbers, bools, nil) are NOT walked. **Nested
     `map[string]any` / `[]any` are recursed.**
  3. Every `Message.ContentParts[i]` where `Kind == ContentKindToolResult`
     → recurse into `.ToolResult.Content` (same shape as `Input`).
  4. **Top-level `Message.Content` field** (the legacy single-string field
     used by some surfaces — Anthropic in particular) — walk it too.
  - **Why not also walk `Message.Role`, `Message.Name`, tool IDs:** those
    are protocol fields, not user content. Redacting them breaks
    correlation and protocol shape.
  - **Why "string leaves only":** a tool_use input like
    `{"to":"corey@cmetech.io","attachments":[{"path":"/inbox"}]}` has the
    email at a leaf. We don't want to redact `to` (the key) or
    `attachments` (a non-string container).
  - **Recursive walker is a single helper.** One pure-Go function in
    `internal/plugin/pii/walk.go` that takes an `any` and a per-string
    transform function. Reused by all three content-kind branches.
  - **Cost note:** tool_use.Input recursion adds work proportional to the
    JSON depth of the args; in practice tools have shallow flat args
    (1-2 levels). Negligible at our throughput.

### LoggingHook ordering vs PII redaction

- **D-04: Pre chain order: `RequestID → Auth → PIIRedaction → Logging`.**
  Logs see REDACTED content; raw PII never enters slog records.
  - **Why PII before Logging:** logs are the most common PII leak path
    (file disk, SIEM, ELK, cloud log aggregation). Putting Logging last
    makes the privacy story default-safe.
  - **Why Auth before PII:** failed-auth requests should short-circuit
    cheaply without paying the PII scrub cost. AuthHook returns a
    short-circuit response on bad bearer token; the rest of the chain
    never runs.
  - **Why RequestID first:** every other hook's log records should carry
    the same `X-Request-Id`. Putting it first means even AuthHook's
    rejection log carries the ID.
  - **Post chain order:** `Logging` only (the others have no Post
    behavior). LoggingHook's Post entry emits timing + status.
  - **Redaction summary:** LoggingHook MAY emit a structured field like
    `redacted={Email:2, SSN:1}` so operators can see WHAT was redacted
    without seeing the raw values. PIIRedactionHook should expose a
    typed `ctx`-attached summary (e.g.,
    `pii.SummaryFromContext(ctx) []RedactionCount`) that LoggingHook
    reads. **Planner's discretion** on whether to ship this in v1 or
    defer — but the API seam should be present so it can be added later
    without re-plumbing.
  - **No LOG_RAW_REQUEST escape hatch in v1.** The third option ("env
    toggle to log raw") was offered and rejected by omission — adding it
    creates a foot-gun where a debugging operator flips it in prod and
    forgets. If debugging needs raw content, run with
    `PII_REDACTION_ENABLED=false` for that request session (and accept
    the leak risk).

### Hash mode keying

- **D-05: `PII_HASH_KEY` env var; HMAC-SHA256.** When
  `PII_REDACTION_MODE=hash`:
  - Token format: `<EMAIL:h-a1b2c3d4>` (entity name + short hex tag).
    Hex tag is the first N hex chars of `HMAC-SHA256(PII_HASH_KEY,
    canonical_form_of_match)`. N = planner's discretion, default 8 (32
    bits — plenty of correlation entropy, no rainbow-table feasibility
    even with the key).
  - **Canonical form before hashing:** lowercased + trimmed. Emails
    `Corey@CMETECH.io` and `corey@cmetech.io` produce the same token.
    Phone numbers stripped of formatting (`+1-555-0100` and `5550100`
    are the same person). SSN dashes stripped.
  - **Boot validation:** if `PII_REDACTION_MODE=hash` AND
    `PII_HASH_KEY` is empty/unset → gateway refuses to start with a
    clear error. **No silent unkeyed fallback** — the unkeyed mode is a
    security trap (rainbow-table-trivial), so the operator must
    explicitly provide a key.
  - **Key rotation invalidates correlation:** changing `PII_HASH_KEY`
    means `corey@cmetech.io` hashes to a new tag. This is a feature —
    rotate the key to break attacker correlation if a key leak is
    suspected. Document in `docs/operating.md`.
  - **Modes that don't need a key:** `replace` (`<EMAIL>`),
    `mask` (`co***@cm***.io`), `drop` (empty string). Boot validation
    only fires for `hash`.

### Claude's Discretion

The planner/researcher have latitude on:

- **`Recognizer` struct shape.** D-03 names it `Recognizer{Name, Pattern,
  Validate}`. The planner may add fields (e.g., `Anonymize` per-recognizer
  override, `MinConfidence` for future ML hooks) as long as the v1 six
  recognizers compile against the agreed shape. Keep the struct narrow.
- **Counter-suffix scope** (`<EMAIL>` vs `<EMAIL_1>`/`<EMAIL_2>`).
  Recommended: per-`canonical.ChatRequest` (counter resets each request,
  preserves intra-prompt referential identity across multiple messages).
  Per-message is also defensible. Per-process is wrong (cross-request
  leakage). Pick one and document; property test the choice.
- **Whether to emit the LoggingHook redaction summary in v1** (D-04
  note). API seam MUST exist; v1 ship-or-defer is open.
- **`/health/hooks` config field exposure rules.** Each hook's
  `Describe() (kind string, config map[string]any)` method declares what
  fields it considers safe to publish. Recommended: hooks expose
  non-secret config only (entity names yes, regex patterns no — regex
  reveals detection logic to an attacker; token counts yes, token values
  no). Planner formalizes the interface.
- **Whether `X-Request-Id` is generated UUID-v4, ULID, or nanoid.** All
  three work; pick one with low ambiguity (no `=` chars; URL-safe).
  Default suggestion: ULID (sortable + 26 chars).
- **AuthHook short-circuit error envelope.** The hook returns a canonical
  error response; the surface adapter renders it. Existing Phase 3.1
  error path (`internal/adapter/anthropic/errors.go`) and Phase 2/3
  equivalents are the precedent — extend if needed. AuthHook should NOT
  hard-code per-surface error shapes; that's the adapter's job.
- **Whether `internal/plugin/chain.go` exposes a typed `Chain` struct
  vs `[]PreHook` + `[]PostHook` slices.** Recommended typed `Chain{Pre,
  Post []PluginHook}` so introspection (D-01 + OBSV-04) has one place to
  walk. Engine reads `chain.Pre` / `chain.Post` into its existing
  `Config.PreHooks` / `Config.PostHooks` fields.
- **Whether to wire `ENABLED_HOOKS` filter through a separate
  `internal/plugin/Filter(chain, allowlist)` helper vs inline in
  `main.go`.** Helper is cleaner for testing; inline is fewer files.
  Pick one.
- **Goleak gate placement.** Each new test file gets `goleak.VerifyTestMain`
  per Phase 4/5/6 discipline. Standard.

### Folded Todos

No todos were folded.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project context (must-read)

- `.planning/PROJECT.md` — Core value ("one place to enforce policy");
  Key Decisions table row "`PreHook`/`PostHook` plugin chain for
  guardrails"; **Out of Scope line 108: "Hot config reload / dynamic
  plugin registration. Plugins are Go types registered at boot. Restart
  to change config."** This locks the no-mutate-path rule.
- `.planning/REQUIREMENTS.md` — PLUG-01..06 (the phase's primary
  requirements), OBSV-03 (slog X-Request-Id correlation), OBSV-04
  (`/health/hooks` view-only).
- `.planning/ROADMAP.md` § "Phase 8: Plugin Hook Chain" — goal + 7
  success criteria. SC6 locks the six PII recognizers + env knob
  surface; SC7 locks `/health/hooks` view-only + "no runtime mutate
  path in v1".

### Closest prior-phase analogs (must-read)

- `.planning/phases/02-ollama-end-to-end/02-CONTEXT.md` — D-04 PreHook/
  PostHook seam design (which Phase 8 cashes). The Codex H-4 (PreHook
  short-circuit body preservation) and H-5 (PostHook in-place
  mutation) decisions are load-bearing for Phase 8's chain runner.
- `.planning/phases/03.1-anthropic-surface/03.1-CONTEXT.md` — Anthropic
  error envelope shape (`{type:"error", error:{type, message}}`) that
  AuthHook short-circuits through; bearer auth on `x-api-key` OR
  `Authorization: Bearer` dual-header path (Phase 3.1 D-15) the
  AuthHook refactor must preserve.
- `.planning/phases/05-pool-stateful-sessions/05-CONTEXT.md` —
  `/health/agents` precedent for the `/health/hooks` registration
  pattern (auth-exempt outer-router route, separate handler, JSON
  envelope shape). D-14 to D-18 in that CONTEXT are the analog blueprint.
- `.planning/phases/06-tool-call-path/06-CONTEXT.md` — D-04
  "kiro-native tool_call_update stays as thought text" is the upstream
  fact that means PII redaction's tool_result.Content walk (D-03) sees
  thought-rendered tool output, not structured tool_result blocks
  (those land on the inbound side of a real client-driven agent loop).

### Spec of record (must-read)

- `docs/briefs/go_port_brief.md` § 3.14 "Plugin hooks" — the
  PreHook/PostHook contract spec, RequestID/Auth/Logging hook
  enumeration, and the reference to Bifrost's `ChainMiddlewares`
  pattern. M8 milestone definition (`docs/briefs/go_port_brief.md`
  line 1002-1003) explicitly names these three day-one hooks; Phase 8
  adds a fourth (PIIRedactionHook) beyond the brief's M8.
- `docs/briefs/go_port_brief.md` § 3.4 "The embeddings problem" — by
  analogy, the same "no cgo, no Python sidecar" pressure applies to
  PII detection. Pure-Go regex is the equivalent of "good enough
  without the cost."
- `docs/briefs/go_port_brief.md` § 3.12 "Trust gates" — `gosec` G204,
  `golangci-lint` strict, `goleak`, property tests via `testing/quick`
  (mirrors Phase 6 D-12; PII recognizers are a property-test target).
- `docs/briefs/go_port_brief.md` § 3.13 "Adapter-over-canonical
  layout" — PII walker operates on canonical types only; no adapter
  imports the plugin package's per-recognizer regex.

### Existing code (must-read)

- `internal/engine/hooks.go` — **The seam interfaces are already
  shipped and locked.** `PreHook.Before(ctx, req) (*ChatResponse,
  error)` and `PostHook.After(ctx, req, resp) error`. Phase 8 hooks
  implement these as-is; do not modify the interface.
- `internal/engine/engine.go` + `internal/engine/collect.go` — Pre and
  Post traversal logic. The Codex H-4 short-circuit and H-5 in-place
  mutation are tested behaviors; Phase 8 must preserve them. See
  `collect.go:7-12, 43, 114-118`.
- `internal/auth/bearer.go` + `internal/auth/ipallowlist.go` +
  `internal/auth/auth.go` — current bearer-token middleware. AuthHook
  refactor migrates the bearer logic to a canonical-typed hook; the
  IP allowlist + exempt-path gates stay at the HTTP layer (those are
  surface-agnostic concerns that benefit from running BEFORE the
  engine).
- `internal/server/server.go:146, 196-200` — `/health` and
  `/health/agents` auth-exempt registration patterns. `/health/hooks`
  follows the same shape.
- `internal/server/agents_test.go` — handler test pattern for
  registry-stats endpoints. `/health/hooks` handler tests mirror this.
- `internal/canonical/chat.go` — `Message`, `ContentPart`,
  `ContentKind` (Text/Image/ToolUse/ToolResult), `ToolUsePart`,
  `ToolResultPart` shapes that the PII walker traverses (per D-03).
  `ToolUsePart.Input *map[string]any` is the recursion target.
- `internal/config/config.go` — env-loading patterns. Phase 8 adds
  `ENABLED_HOOKS`, `PII_REDACTION_ENABLED`, `PII_ENABLED_ENTITIES`,
  `PII_REDACTION_MODE`, `PII_HASH_KEY`. Boot-validation precedent: the
  `parseCIDRs` error path for `ALLOWED_IPS`.
- `internal/plugin/.gitkeep` — empty placeholder package directory.
  Phase 8 populates this. `.go-arch-lint.yml` should already permit
  the package (verify); only `internal/canonical` is imported from
  hooks. **Hooks MUST NOT import `internal/engine`** — they implement
  interfaces defined there but don't depend on the package.
- `cmd/otto-gateway/main.go` — chain construction site (D-01).
  Currently passes nil PreHooks/PostHooks per Phase 2 D-04.

### Reference architecture (read as needed)

- `~/Projects/repos/local/bifrost/core/plugins/plugins.go` (and the
  per-plugin directories under `~/Projects/repos/local/bifrost/plugins/`)
  — Bifrost's `Plugin` interface + `PreHook`/`PostHook` distinction +
  `ChainMiddlewares` helper. **The shape, not the surface area.** We
  borrow the typed chain pattern; we don't copy the per-request
  override semantics or the Redis-backed plugin state.
- `~/Projects/repos/local/bifrost/plugins/logging/main.go` — Bifrost's
  LoggingHook reference. Cross-check our `LoggingHook` against theirs
  for `slog` correlation patterns. We do NOT borrow their per-token
  cost accounting (out of scope).
- `~/Projects/repos/local/bifrost/plugins/maxim/main.go` — Bifrost's
  request-id-style plugin. Reference for the X-Request-Id propagation
  pattern through `ctx`.

### Wire / behavioral parity (must-read)

- `docs/reference/acp_server_node_reference.md` § "Hooks (pre/post)" —
  the Node implementation's hook surface (which Phase 8 supersedes
  with the Go version). Phase 8 does NOT preserve Node's specific
  hook implementations (Node has no PII hook); we preserve the
  conceptual shape (pre-engine vs post-response).
- Anthropic error envelope spec (https://docs.anthropic.com/en/api/errors)
  — AuthHook short-circuit response renders through the Anthropic
  adapter's `errors.go` (Phase 3.1) when the surface is Anthropic.
- OpenAI error envelope (https://platform.openai.com/docs/guides/error-codes)
  — same, via OpenAI adapter's error rendering.

### Compliance / privacy refs (read as needed)

- NIST SP 800-122 (Guide to Protecting PII) — informs the recognizer
  set + the "redact by default in logs" posture. Not normative for
  v1 but worth a glance for the planner.
- Microsoft Presidio's built-in recognizers list
  (https://microsoft.github.io/presidio/supported_entities/) — the
  industry-standard PII entity taxonomy our six recognizers are a
  subset of. If we ever expand to a 7th-Nth recognizer, this is the
  reference list to pick from (with regex + Luhn / range-rule
  validators where applicable; spaCy/NER NOT applicable for us per
  PROJECT.md no-cgo constraint).

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- **`internal/engine/hooks.go` (Phase 2 D-04).** The Pre/Post interface
  contract is shipped and stable. **Phase 8 does not modify the
  interfaces** — it implements them.
- **`internal/engine/engine.go` + `collect.go` (Phase 2 H-4/H-5).** The
  chain runner already exists in the engine. Phase 8 just constructs
  the slice that goes into `engine.Config.PreHooks` /
  `engine.Config.PostHooks`.
- **`internal/auth/bearer.go` + `auth.go` + `ipallowlist.go` (Phase 2).**
  AuthHook reuses the constant-time bearer comparison from
  `bearer.go:CompareTokens` (or equivalent). The IP allowlist stays
  at the HTTP layer.
- **`internal/canonical/chat.go` types.** `Message`, `ContentPart`,
  `ContentKind`, `ToolUsePart`, `ToolResultPart` are the walker's
  targets. **No new canonical types needed** (matches Phase 6's
  pattern of "Phase 2 forward-designed everything").
- **`internal/config/config.go` env-loading patterns.** Phase 8 adds
  ~6 new env keys; the `getEnvStrSliceComma`, `getEnvInt`, bool-parsing
  patterns exist. Boot-error path via `errs = append(errs, fmt.Errorf(...))`
  precedent at `config.go:141, 146`.
- **`internal/server/server.go` health-route registration pattern.**
  `/health/hooks` follows `/health/agents` (Phase 5 D-18) exactly:
  outer router, exempt from auth, separate handler. The
  `agentsHandler` shape (`pkg/server/agents.go`) is the template.
- **`internal/engine/pickcwd_test.go` property test (Phase 2 / TRST-06).**
  Mirrors directly to recognizer property tests: never-panic, always-
  terminate, idempotent. Luhn validator gets a fixed-table test
  separately.
- **`tests/e2e/` real-binary harness (Phase 5/6 precedent).** Phase 8
  E2E tests boot the binary with various `ENABLED_HOOKS` /
  `PII_REDACTION_*` envs and assert behavior via curl-equivalent
  requests + `/health/hooks` introspection.
- **`docs/operating.md`** — operator doc. Phase 8 adds a section
  documenting the env knobs, the boot-error conditions (unknown
  ENABLED_HOOKS name; mode=hash without key), and the
  restart-to-apply rule.

### Established Patterns

- **Canonical types stay narrow.** Phase 2 D-11 invariant. PII walker
  observes-and-mutates canonical types but adds no new fields.
- **Adapter-over-canonical (TRST-04).** Plugin package imports only
  `internal/canonical`. It does NOT import `internal/engine` (it
  implements engine's interfaces but doesn't depend on the package);
  it does NOT import any adapter. `.go-arch-lint.yml` enforces.
- **`engine.Config` injection.** Engine takes hooks via Config, not
  globals. Phase 8 preserves; `main.go` builds the chain and passes it
  in.
- **Boot-error on bad config.** `config.go` precedent at
  `ALLOWED_IPS` parse-error path. Phase 8 boot errors: unknown
  ENABLED_HOOKS name (D-02), mode=hash with no PII_HASH_KEY (D-05).
- **`goleak.VerifyTestMain` discipline.** Every Phase 8 test file
  carrying timing/goroutines (LoggingHook timing, async PII walk if
  ever introduced) gets the gate.
- **Property tests via `testing/quick`** (TRST-06). PII walker +
  Luhn + recognizers all get property-test coverage. Phase 6 D-12
  is the precedent.
- **HTTP middleware vs canonical-hook split.** Surface-agnostic
  concerns that run before the engine (IP allowlist, exempt paths,
  CORS) stay as `chi` middleware. Concerns that need canonical
  semantics (auth, request ID, logging, PII) move to hooks. AuthHook
  is the migration boundary case; the answer is "hooks for the
  canonical-typed work, middleware for the IP+exempt-path
  gatekeeping."

### Integration Points

- `cmd/otto-gateway/main.go` — construct `plugin.Chain{}`, apply
  `ENABLED_HOOKS` allowlist filter, assign to
  `engine.Config.PreHooks` / `engine.Config.PostHooks`. Drop the
  bearer-token from the HTTP middleware (AuthHook owns it now); IP
  allowlist + exempt-path middleware stays.
- `internal/plugin/chain.go` — **new file.** `type Chain struct { Pre
  []engine.PreHook; Post []engine.PostHook }`, plus
  `Filter(allowlist []string) Chain` for `ENABLED_HOOKS` and
  `Describe() []HookDescription` for `/health/hooks`.
- `internal/plugin/request_id.go` — **new file.** `RequestIDHook`
  implements both `PreHook` and `PostHook`. Pre generates/honors
  `X-Request-Id`, attaches to ctx; Post is optional (could no-op or
  emit a final correlation log line).
- `internal/plugin/auth.go` — **new file.** `AuthHook` implements
  `PreHook`. Bearer-token validation via `internal/auth` package
  helpers. Short-circuit on failure.
- `internal/plugin/logging.go` — **new file.** `LoggingHook`
  implements both Pre and Post. Reads the PII redaction summary from
  ctx (if present per D-04 note) and emits structured `slog` records.
- `internal/plugin/pii/pii.go` — **new file.** `PIIRedactionHook`
  implements `PreHook`. Walks canonical content per D-03.
- `internal/plugin/pii/recognizers.go` — **new file.** `Recognizer`
  struct + the six initial recognizer literals.
- `internal/plugin/pii/walk.go` — **new file.** The recursive
  string-leaf walker for `tool_use.Input` / `tool_result.Content`.
- `internal/plugin/pii/luhn.go` — **new file.** Luhn check
  post-validator for credit card recognizer.
- `internal/server/hooks_handler.go` — **new file.** `GET /health/hooks`
  handler. Reads the chain via a `HooksDescriptionSource` interface
  (or analog) the way `agentsHandler` reads `RegistryStatsSource`.
- `internal/server/server.go` — register `/health/hooks` on the outer
  (auth-exempt) router alongside `/health` and `/health/agents`.
- `internal/config/config.go` — add `EnabledHooks []string`,
  `PIIRedactionEnabled bool`, `PIIEnabledEntities []string`,
  `PIIRedactionMode string`, `PIIHashKey string` to the Config struct.
  Loading + validation in `Load()`.
- `cmd/otto-gateway/main.go` — wire all the above.
- `.go-arch-lint.yml` — verify the `plugin` package is in the
  allowed-import graph. Expected boundaries: `plugin` may import
  `canonical` and `engine` (for the interfaces); `plugin/pii` may
  import `plugin` and `canonical`. Nothing else imports `plugin`'s
  internals.

</code_context>

<specifics>
## Specific Ideas

- **The four-hook chain is the v1 cash-out on the "one place to
  enforce policy" core value (PROJECT.md).** Without Phase 8, auth lives
  at the HTTP layer (per-surface concern, three places); after Phase 8
  it lives at the canonical layer (one place serving three surfaces).
  Same for logging correlation. This is the architectural payoff;
  emphasize in the PR description.

- **PII hook is the proof point that the chain handles future
  guardrails too.** PII is just a regex-based PreHook. Future
  PLUG-V2-01..05 (moderation, schema validation, budget, semantic
  cache, audit log) drop in via the same `Chain` slice + `Filter`
  pattern. Phase 8's success criteria for "extensibility" are
  satisfied by D-01's slice + D-02's allowlist. No registry needed.

- **The two-knob PII control (`ENABLED_HOOKS` allowlist + per-hook
  `*_ENABLED` bool) is intentional.** It maps to two operator
  concerns: "is this hook present in our policy at all?" (deployment
  decision) vs "is this hook currently doing work?" (runtime knob —
  but in v1, both require restart). Documenting this clearly in
  `docs/operating.md` is worth a few lines. The PII hook is the first
  hook with both knobs; future hooks may or may not have the inner
  bool depending on whether they're always-on by virtue of being in
  the chain.

- **`/health/hooks` is the visibility cash-out on the no-runtime-
  mutation decision.** The operator's complaint about restart-to-
  apply is mitigated by always being able to ASK what's currently
  active. If `/health/hooks` shows `PIIRedactionHook { enabled: true,
  config:{enabled: false} }`, the operator knows the hook is wired
  but inert and can fix env+restart deliberately.

- **Hash-key key-rotation as a feature is worth a callout.** Most
  redaction implementations treat the hash key as a static infra
  config. Naming the rotation case explicitly ("rotate
  PII_HASH_KEY to invalidate prior correlations on suspected log
  leak") elevates it from accidental property to operational tool.
  Document in `docs/operating.md` runbook.

- **The `ENABLED_HOOKS` typo-protection boot error (D-02) is
  load-bearing.** A typo like `ENABLED_HOOKS=PIIRedaction` (missing
  `Hook` suffix) silently disables PII redaction; the boot-error
  refusal-to-start is the structural fix. Test this explicitly: a
  unit test that sets `ENABLED_HOOKS=BogusHook` and asserts the
  Config.Load() returns an error containing "unknown hook".

- **Property test for the PII walker is the Phase 8 D-12 equivalent.**
  Generate arbitrary nested `map[string]any` / `[]any` / string-mix
  structures via `testing/quick`; assert:
  - **Never-panic** for any shape (nil pointers, empty maps, very
    deep nesting up to a sane bound).
  - **Idempotent**: `Walk(Walk(x)) == Walk(x)`.
  - **String-leaves-only**: keys and non-string leaves are
    bit-identical in input vs output (only string-typed values may
    change).
  - **Cycle-safe**: if the planner allows cyclic refs (probably not —
    they shouldn't appear in canonical JSON), the walker terminates.

</specifics>

<deferred>
## Deferred Ideas

- **Admin API for runtime hook management** (`POST /admin/hooks/...`,
  separate listener, ADMIN_TOKEN, audit log, atomic chain swap) —
  PROJECT.md OOS + this phase's SC7. Track as a future PLUG-V2
  requirement if a real customer need surfaces. The minimum bar would
  be: separate listener on 127.0.0.1 or UNIX socket, dedicated auth
  token, audit log on every change, atomic chain swap via
  `atomic.Pointer[Chain]`.
- **Content-moderation hook (`PLUG-V2-01`)** — Post-side scrubbing of
  model responses (CSAM detection, secret leak detection, etc.).
  Needs external API (OpenAI Moderation, Llama Guard, etc.) so it's
  a different operational model than PII (which is local-only).
- **Schema-validation hook (`PLUG-V2-02`)** — Validate tool definitions,
  prompt size, image counts before the engine. Could be local; defer
  until a real failure mode appears.
- **Budget / rate-limit hook (`PLUG-V2-03`)** — Token-bucket per API
  key. Needs durable state (Redis or in-memory + restart-loss). Defer.
- **Semantic cache hook (`PLUG-V2-04`)** — Cache lookup on canonical
  request; short-circuit on hit. Needs an embedding model (which
  collides with the Phase 7 "skip embeddings unless needed" decision).
  Defer until Phase 7's fate is decided.
- **Audit-log hook (`PLUG-V2-05`)** — Post-hook writes request+response
  to a separate audit sink. Defer until a compliance requirement
  surfaces.
- **NER-based PII (`prose/v2` or external Presidio sidecar)** — Phase 8
  ships regex-only. If recall complaints surface for names or
  locations, revisit (likely Phase 9+ or v2).
- **Per-request hook bypass headers** (`X-Skip-PII`, etc.) — Guardrail
  defeat primitive on the network; intentionally never. If an internal
  caller needs unredacted access, run a separate instance with
  `PII_REDACTION_ENABLED=false`.
- **Hook ordering customization via env** (e.g.,
  `HOOK_ORDER=Auth,RequestID,Logging,PIIRedactionHook`) — adds
  surprise; the order is part of the architectural contract (RequestID
  first, PII before Logging). Defer indefinitely.
- **Output-side PII redaction** (PostHook that walks
  `canonical.ChatResponse.Message.Content`) — the model could echo
  user PII back (memory tools, summarization). v1 ships input-side
  only because that's what user requested. Add as PLUG-V2-07 candidate
  if needed.
- **OpenAI-style function arguments field-name awareness** (skip the
  regex for fields named `email`, etc.) — brittle; the regex IS the
  policy. Defer.
- **Per-recognizer cost tracking** (`pii_match_duration_ms` per
  recognizer for ops dashboards) — operational nice-to-have; not v1.
- **Configurable per-recognizer `Anonymize` strategy** (e.g., emails
  get masked but SSNs get hashed) — v1 applies the global
  `PII_REDACTION_MODE` uniformly. Per-recognizer override is a
  defensible v2.

</deferred>

---

*Phase: 8-Plugin Hook Chain*
*Context gathered: 2026-05-27*
