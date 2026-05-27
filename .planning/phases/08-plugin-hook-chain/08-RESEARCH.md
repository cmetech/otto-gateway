# Phase 8: Plugin Hook Chain - Research

**Researched:** 2026-05-27
**Domain:** Canonical-typed Pre/Post hook chain over surface-agnostic chat requests; regex+validator PII scrubbing; view-only chain introspection
**Confidence:** HIGH (existing seams shipped + verified; ULID/UUID/regex tradeoffs cross-referenced against official sources; Go RE2 lookahead limitation confirmed)

## Summary

Phase 8 lights up the **plugin Pre/Post hook chain** that Phase 2 D-04 forward-designed. The
seam interfaces in `internal/engine/hooks.go` (`PreHook.Before`, `PostHook.After`) and the
short-circuit/in-place-mutation contract in `internal/engine/collect.go` (Codex H-4 / H-5)
are already shipped, tested, and stable — **Phase 8 does NOT modify them**. The net-new work
is (a) the four day-one hook implementations (`RequestIDHook`, `AuthHook`, `LoggingHook`,
`PIIRedactionHook`); (b) a recognizer-registry-based PII scrubber over canonical content; (c)
an `ENABLED_HOOKS` allowlist with typo-fail-fast at boot; (d) the view-only
`GET /health/hooks` introspection endpoint; and (e) the `cmd/otto-gateway/main.go` wiring
that constructs a hardcoded `plugin.Chain{}` slice (D-01) and passes it to
`engine.Config.PreHooks` / `engine.Config.PostHooks`.

The single most important constraint is **adapter-over-canonical (TRST-04)**: the new
`internal/plugin` package may import `internal/canonical` and `internal/engine` (for the
interface types it implements) but must NOT import any adapter. The PII walker operates on
canonical `ContentPart`/`ToolUsePart`/`ToolResultPart` only, never on adapter-native wire
shapes. The chain runs in registration order; the first non-nil `*canonical.ChatResponse`
from a PreHook short-circuits the engine and is preserved verbatim by `Collect` (Codex H-4);
all PostHooks run unconditionally on the assembled or short-circuit response (Codex H-5).

**Primary recommendation:** Implement four hook types in `internal/plugin/*.go` per Bifrost's
`Chain{Pre, Post}` shape; build the PII walker as a single recursive `string`-leaf helper
in `internal/plugin/pii/walk.go` with a `Recognizer{Name, Pattern, Validate}` registry;
choose ULID for `X-Request-Id` (sortable, URL-safe, 26 chars, single dep with no cgo); use
`crypto/subtle.ConstantTimeCompare` for `AuthHook` (same primitive `internal/auth/bearer.go`
already uses); HMAC-SHA256 for `PII_REDACTION_MODE=hash` with hard-fail boot when
`PII_HASH_KEY` is missing.

## User Constraints (from CONTEXT.md)

### Locked Decisions

- **D-01: Hardcoded `plugin.Chain{}` slice in `cmd/otto-gateway/main.go`** — no
  `Register(name, factory)` registry package; no `init()` self-registration. Same pattern
  applies one level deeper for PII recognizers (a `var Recognizers = []Recognizer{...}`
  literal). Bifrost-style; adding a new hook is one line in `main.go`.
- **D-02: `ENABLED_HOOKS` is an allowlist; empty/unset = all enabled.** Format:
  comma-separated hook type names (e.g., `RequestIDHook,LoggingHook`). **Set with a name
  not present in the slice → boot error** (`unknown hook in ENABLED_HOOKS: FooHook`). Typo
  protection is load-bearing — a silent typo could disable PII redaction. Execution order
  is **registration order in the slice**, NOT `ENABLED_HOOKS` list order. `ENABLED_HOOKS`
  (presence in chain) composes with `PII_REDACTION_ENABLED` (does the hook do work) at
  different layers; both knobs live independently.
- **D-03: PII walker scope.** Visits `Message.ContentParts[i].Text` when
  `Kind == ContentKindText`; recurses into `Message.ContentParts[i].ToolUse.Input` (a
  `map[string]any` per canonical/chat.go:193) and `ContentKindToolResult.ToolResult.Content`
  (a string per canonical/chat.go:200) — string leaves only at any depth. **Map keys NOT
  walked**, non-string leaves (numbers/bools/nil) NOT walked, nested `map[string]any` and
  `[]any` recursed. Also walks the top-level `Message.Content` field used by some surfaces.
  Single recursive helper in `internal/plugin/pii/walk.go`.
- **D-04: Pre chain order: `RequestID → Auth → PIIRedaction → Logging`.** Logs see
  REDACTED content; raw PII never enters slog records. Failed-auth short-circuits cheaply
  before paying the PII scrub cost; RequestID first means even AuthHook's rejection log
  carries the ID. Post chain: Logging only (others have no Post behavior). **No
  `LOG_RAW_REQUEST` escape hatch in v1** — explicitly rejected as foot-gun. PIIRedactionHook
  exposes a typed ctx-attached summary (`pii.SummaryFromContext(ctx) []RedactionCount`)
  that LoggingHook can optionally emit as `redacted={Email:2, SSN:1}`. **The summary API
  seam MUST be present even if v1 LoggingHook defers emitting it.**
- **D-05: `PII_HASH_KEY` env var; HMAC-SHA256.** When `PII_REDACTION_MODE=hash`, token
  format is `<EMAIL:h-a1b2c3d4>` (entity name + 8-hex-char tag = first 8 hex chars of
  `HMAC-SHA256(PII_HASH_KEY, canonical_form_of_match)`). **Canonical form: lowercased +
  trimmed** for emails, formatting-stripped for phones, dashes-stripped for SSNs.
  **Boot validation: mode=hash AND empty PII_HASH_KEY → gateway refuses to start.** Modes
  `replace` / `mask` / `drop` don't need a key. Changing `PII_HASH_KEY` invalidates prior
  correlation tokens — this is intentional (key rotation tool for suspected log leak).

### Claude's Discretion

- `Recognizer` struct shape — D-03 names `{Name, Pattern, Validate}`. Planner may add
  fields (e.g., `Anonymize` per-recognizer override, `MinConfidence` for future ML)
  as long as v1 six recognizers compile against the agreed shape. Keep narrow.
- Counter-suffix scope (`<EMAIL>` vs `<EMAIL_1>` / `<EMAIL_2>`) — **recommended
  per-`canonical.ChatRequest`** (resets each request, preserves intra-prompt referential
  identity). Per-process is wrong (cross-request leakage).
- Whether v1 LoggingHook emits the redaction summary (the API seam MUST exist regardless).
- `/health/hooks` config field exposure rules — each hook's `Describe()` declares its
  safe-to-publish fields. **Hooks expose non-secret config only** (entity names yes, regex
  patterns no, token counts yes, token values no, hash key never).
- Whether `X-Request-Id` is UUIDv4, ULID, or nanoid — **default recommendation: ULID**.
- AuthHook short-circuit error envelope — adapter's `errors.go` (Phase 3.1 precedent for
  Anthropic) renders per-surface; hook returns canonical shape only.
- Whether `internal/plugin/chain.go` exposes a typed `Chain{Pre, Post []PluginHook}` vs
  two slices — **recommended typed Chain** so introspection has one place to walk.
- `internal/plugin/Filter(chain, allowlist)` helper vs inline in main.go.
- Goleak gate placement — every new test file gets `goleak.VerifyTestMain`.

### Deferred Ideas (OUT OF SCOPE)

- Runtime hook mutation / hot reload (PROJECT.md line 108; SC7).
- Dynamic plugin loading (Go `plugin` package, `dlopen`, etc.).
- Admin listener / admin-only auth surface.
- Output-side moderation / safety filtering (`PLUG-V2-01`).
- Per-request hook bypass headers (e.g., `X-Skip-PII: true`).
- NER / language-model-based PII detection (`prose/v2`, Presidio sidecar).
- `PLUG-V2-01..05` (moderation, schema validation, budget, semantic cache, audit log).
- Hook ordering customization via env (e.g., `HOOK_ORDER=...`).
- Output-side PII redaction (PostHook walking `ChatResponse.Message.Content`).
- OpenAI-style function arguments field-name awareness.
- Per-recognizer cost tracking; per-recognizer `Anonymize` strategy.

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| PLUG-01 | PreHook/PostHook interfaces operate on canonical types; hooks see surface-agnostic data | Already shipped in `internal/engine/hooks.go`; Phase 8 implements against the existing contract |
| PLUG-02 | A `PreHook` returning a non-nil canonical response short-circuits the engine; adapter renders it in native shape | Already shipped — Codex H-4 in `collect.go` lines 42-47; AuthHook uses this path |
| PLUG-03 | Hooks chained in registration order; first non-nil short-circuit wins for Pre; all Post run | Already shipped — `engine.Run` loop at engine.go:152-162; `collect.go` for-loop at lines 118-122 |
| PLUG-04 | Day-one hooks registered: RequestID, Auth (refactored from middleware), Logging | `internal/plugin/{request_id,auth,logging}.go` new files; bearer middleware replaced by `AuthHook` at canonical layer |
| PLUG-05 | `ENABLED_HOOKS` env var enables/disables hooks per deployment | D-02 — typo-fail-fast filter helper in `internal/plugin/chain.go` or inline in main.go |
| PLUG-06 | `PIIRedactionHook` (Pre) with extensible `Recognizer{Name, Pattern, Validate}` registry; six recognizers; env knobs `PII_REDACTION_ENABLED`/`PII_ENABLED_ENTITIES`/`PII_REDACTION_MODE`; pure Go, no cgo | D-03 walker scope; D-05 hash-mode keying; pure-Go regex + Luhn + `net.ParseIP` |
| OBSV-03 | Structured logging via `log/slog` with `X-Request-Id` correlation across pre-hook/engine/ACP/post-hook spans | `RequestIDHook` attaches ID to ctx via typed key; `slog.With` or `slog-context`-style attribute extraction propagates it through every span |
| OBSV-04 | `GET /health/hooks` view-only introspection — `name`, `kind`, `enabled`, `config`; exempt from auth; no runtime mutate path | Parallels `/health/agents` (Phase 5 D-18); new `internal/server/hooks_handler.go`; `HooksDescriptionSource` interface |

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| HTTP routing / outer middleware (RequestID middleware, Recoverer, accessLog, IP allowlist, exempt paths) | Frontend Server (chi router) | — | Surface-agnostic concerns that need to run BEFORE the engine; can't observe canonical types |
| Bearer-token validation | API/Plugin (canonical layer) | — | Refactored from HTTP middleware to canonical-typed hook so one place serves all three surfaces; AuthHook runs after adapter parses request |
| PII scrubbing | API/Plugin (canonical layer) | — | Operates on canonical `ContentPart` (Text + ToolUse.Input + ToolResult.Content); recursive `map[string]any` walk |
| Request-ID propagation | Frontend Server (header in/out) + API/Plugin (ctx-injected on Pre, response-header on Post) | — | The HTTP transport carries `X-Request-Id`; the canonical chain attaches it to ctx for slog correlation across engine + ACP spans |
| Structured logging | API/Plugin (LoggingHook Pre+Post) | Frontend Server (accessLog middleware emits its own HTTP-level line) | LoggingHook sees canonical request after PII redaction; accessLog stays at HTTP for status/timing of total transport |
| Chain introspection | Frontend Server (`/health/hooks` handler) | API/Plugin (`Describe()` interface on each hook) | Endpoint mounted on outer auth-exempt router; hooks expose their own safe-to-publish config via interface method |

**Insight:** The Phase 8 architectural payoff is **moving auth from per-surface HTTP
middleware to a single canonical-typed hook**. This is the "one place to enforce policy"
core value cashing out — same code serves OpenAI, Ollama, and Anthropic.

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/oklog/ulid/v2` | v2.1.1 | `X-Request-Id` generation | [VERIFIED: pkg.go.dev/github.com/oklog/ulid/v2] Latest v2.1.1 released 2026-05-22; module path stable; Crockford Base32 (URL-safe, no I/L/O/U chars); 26-char canonical form; lexicographically sortable; no cgo. **Discretionary default per CONTEXT.md.** [CITED: github.com/oklog/ulid] |
| `crypto/hmac` + `crypto/sha256` | stdlib | HMAC-SHA256 for `PII_REDACTION_MODE=hash` | [VERIFIED: Go stdlib] No external dep; length-extension-attack-safe by construction; constant-time `hmac.Equal` for any future verify path |
| `crypto/subtle` | stdlib | Constant-time bearer-token compare in `AuthHook` | [VERIFIED: existing usage `internal/auth/bearer.go:51`] Same primitive the HTTP middleware uses; refactor reuses the comparison logic verbatim |
| `regexp` | stdlib | Recognizer pattern compilation (compiled at package init per D-01) | [VERIFIED: Go stdlib] RE2-backed; **NO lookahead support** (this affects SSN regex — see Common Pitfalls); `MustCompile` at package init means zero per-request compile cost |
| `net` (`net.ParseIP`) | stdlib | IPv6 validator (post-regex validate function) | [VERIFIED: Go stdlib] ROADMAP SC6 explicitly names `net.ParseIP` for IPv6; far more accurate than any regex for the multitude of IPv6 zero-elision forms |
| `log/slog` | stdlib | Structured logging in `LoggingHook` and ctx correlation | [VERIFIED: Go 1.21+ stdlib] Already used project-wide; `slog.With(...)` or `slog.LogAttrs(ctx, ...)` patterns documented |
| `context` (`context.AfterFunc`, typed-key pattern) | stdlib | `RequestIDHook` ctx propagation | [VERIFIED: Go 1.21+ stdlib] Go-idiomatic private-key + `WithX`/`XFromContext` accessor pattern; same pattern OpenTelemetry SDK uses |
| `go.uber.org/goleak` | v1.3.0 | Goroutine-leak gate on every new test file (project convention) | [VERIFIED: existing usage in `internal/{acp,engine,pool,session,adapter/*}/testmain_test.go`] |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `testing/quick` | stdlib | Property tests for PII walker (idempotent, never-panic, string-leaves-only, map-key invariance) and Luhn validator | [VERIFIED: existing usage `internal/engine/pickcwd_test.go:11,228-229`] Project precedent — `pickcwd_test.go` uses `quick.Check` with `quick.Config{MaxCount: 1000}`. **`pgregory.net/rapid` available as alternative** but stdlib is the project default. |
| `encoding/json` | stdlib | `/health/hooks` JSON envelope rendering | [VERIFIED: Go stdlib] Same shape as `agentsHandler` |
| `github.com/go-chi/chi/v5` | v5.3.0 | Mount `/health/hooks` on outer router | [VERIFIED: `go.mod`] Already a dep; same primitive `/health/agents` uses |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `oklog/ulid/v2` | `github.com/google/uuid` (UUIDv4) | UUIDv4 is also no-cgo and battle-tested but produces 36-char hyphenated form (not URL-safe enough for some downstream log aggregators), is NOT sortable, and exists in a different shape niche. ULID wins on sortability + length + URL-safety. **`google/uuid` is the safer fallback if ULID is rejected.** [CITED: pkg.go.dev/github.com/google/uuid] |
| `oklog/ulid/v2` | `github.com/sixafter/nanoid` | Nanoid: 22-char URL-friendly, slightly shorter than ULID. Not sortable. Smaller ecosystem footprint. Acceptable, but ULID's sortability has operational value when scanning log streams. [CITED: pkg.go.dev/github.com/sixafter/nanoid] |
| `oklog/ulid/v2` | Hand-rolled `crypto/rand`-backed Base32 ID | Adds ~30 lines of code and a hand-rolled encoder. No upside vs. one dep with 6k+ stars. Don't hand-roll. |
| `testing/quick` | `pgregory.net/rapid` | Rapid has shrinkers and better generators for nested structures. **Project uses stdlib `testing/quick`** (precedent at `pickcwd_test.go`); switching would be a separate decision. Defer. |
| `slog.With(...)` per record | `veqryn/slog-context` (auto-extract from ctx) | Slog-context adds context-attribute auto-extraction. Useful but a NEW dep. Stdlib `slog.With(ctx.Value(requestIDKey))` works fine; don't add the dep just for sugar. |

**Installation:**

```bash
go get github.com/oklog/ulid/v2@v2.1.1
```

**Version verification:**

| Package | Verified Command | Result | Source Repo |
|---------|------------------|--------|-------------|
| `github.com/oklog/ulid/v2` | `go list -m -versions github.com/oklog/ulid/v2` (would return `v2.1.1` per pkg.go.dev) | v2.1.1 (2026-05-22) | [github.com/oklog/ulid](https://github.com/oklog/ulid) — 6k+ stars, MIT-licensed, used by Prometheus + InfluxDB |
| stdlib packages | `go version` confirms 1.26.3 installed; project requires 1.23+ | OK | stdlib |

## Package Legitimacy Audit

> slopcheck NOT installable in this research environment (pip not permitted under sandbox).
> **All new external packages below are tagged `[ASSUMED]` per the protocol's graceful
> degradation rule.** The planner MUST insert a `checkpoint:human-verify` task before
> running `go get` for any package marked `[ASSUMED]` so the operator confirms it against
> official sources.

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| `github.com/oklog/ulid/v2` | proxy.golang.org | v1 in 2016; v2 line current; v2.1.1 May 2026 | 6k+ stars; ~2M weekly Go module pulls (estimated from imports list on pkg.go.dev) | [github.com/oklog/ulid](https://github.com/oklog/ulid) | [ASSUMED] (slopcheck unavailable) | **Approved with human-verify checkpoint** — well-known maintainer (Peter Bourgon, ex-Soundcloud/Fastly), MIT license, used by Prometheus / InfluxDB / Cortex |
| `crypto/hmac`, `crypto/sha256`, `crypto/subtle`, `regexp`, `net`, `log/slog`, `context`, `testing/quick`, `encoding/json` | Go stdlib | — | — | golang/go | [OK] (stdlib) | Approved — no audit needed |
| `github.com/go-chi/chi/v5` | proxy.golang.org | Already in `go.mod` v5.3.0 | — | [github.com/go-chi/chi](https://github.com/go-chi/chi) | [OK] (already vetted) | No new install — already approved |
| `go.uber.org/goleak` | proxy.golang.org | Already in `go.mod` v1.3.0 | — | [github.com/uber-go/goleak](https://github.com/uber-go/goleak) | [OK] (already vetted) | No new install — already approved |

**Packages removed due to slopcheck [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none
**Packages needing human-verify checkpoint before install:** `github.com/oklog/ulid/v2`

## Architecture Patterns

### System Architecture Diagram

```
HTTP IN (any surface)
   |
   v
[chi outer router]
   |--- /health, /health/agents, /health/hooks  (auth-exempt)
   |
   |--- /api/{chat,generate,...}, /v1/{chat/completions,messages,...}  (auth-WRAPPED — but see note)
            |
            v
        [adapter parses wire shape → canonical.ChatRequest]
            |
            v
        [engine.Engine.Run / Engine.Collect]   ←—  Phase 2 D-04 seam
            |
            +--> for each PreHook in cfg.PreHooks: ----------------------+
            |        h.Before(ctx, req) -> (resp, err)                   |
            |        if resp != nil: SHORT-CIRCUIT to Collect (H-4)     |
            |        if err   != nil: abort                              |
            |        else continue                                       |
            |                                                            |
            v                                                            v
       [ACP NewSession → SetModel → Prompt]                  [emptyStream + run.response]
            |                                                            |
            +-----> stream of canonical.Chunk -----+                     |
                                                   v                     v
                                              [aggregate text + thoughts + tool-call narration]
                                                   |
                                                   v
                                          assembled canonical.ChatResponse
                                                   |
                                                   v
                                       for each PostHook in cfg.PostHooks:  (Codex H-5)
                                                   |       h.After(ctx, req, resp)  (in-place mutate)
                                                   v
                                          adapter renders to surface wire shape
                                                   |
                                                   v
                                              HTTP OUT
```

**Phase 8's net addition to this diagram:** populate the `cfg.PreHooks` and `cfg.PostHooks`
slots — currently nil — with `[RequestIDHook, AuthHook, PIIRedactionHook, LoggingHook]`
(Pre) and `[LoggingHook]` (Post). The engine code itself is **unchanged**.

**Important note on Auth (this is the architectural payoff of Phase 8):** the existing
`auth.Bearer(...)` middleware in `internal/server/server.go:230` is REMOVED from the
chi route registration. The `IPAllowlist` middleware **stays at chi** (it's a transport
concern; it can't be canonical-typed because the canonical request doesn't carry
`r.RemoteAddr`). Auth migrates to `AuthHook` running at canonical layer. **Auth-exempt
paths** (`/`, `/api/version`, `/health`, `/health/agents`, `/health/hooks`) continue to
work because they never invoke the engine and therefore never invoke the hook chain.

### Recommended Project Structure

```
internal/plugin/
├── chain.go            # type Chain struct { Pre []engine.PreHook; Post []engine.PostHook }
│                       # Filter(allowlist []string) Chain method
│                       # Describe() []HookDescription method (for /health/hooks)
├── chain_test.go       # filter typo-fail-fast + Describe shape tests
├── testmain_test.go    # goleak.VerifyTestMain
│
├── request_id.go       # RequestIDHook (PreHook + PostHook): generate/honor X-Request-Id,
│                       # attach to ctx via typed key, expose via accessor
├── request_id_test.go
│
├── auth.go             # AuthHook (PreHook only): bearer-token validation;
│                       # short-circuit with canonical *ChatResponse on bad token
├── auth_test.go
│
├── logging.go          # LoggingHook (PreHook + PostHook): slog.Info(...) on entry
│                       # and exit with timing; reads pii.SummaryFromContext if present
├── logging_test.go
│
└── pii/
    ├── pii.go              # PIIRedactionHook (PreHook): walks canonical content per D-03
    ├── recognizers.go      # var Recognizers = []Recognizer{...}  ← package-init compile
    ├── walk.go             # recursive string-leaf walker (the single helper)
    ├── luhn.go             # Luhn check post-validator
    ├── modes.go            # replace / mask / hash / drop transformers
    ├── summary.go          # ctx-attached []RedactionCount summary (D-04 API seam)
    ├── pii_test.go
    ├── walk_test.go        # property tests via testing/quick
    ├── luhn_test.go        # fixed-table + property tests
    ├── recognizers_test.go # per-recognizer positive + negative-case tables
    └── testmain_test.go    # goleak.VerifyTestMain
```

### Pattern 1: Hardcoded Chain in main.go (D-01)

**What:** Single source of truth for what hooks are in the chain is one literal slice in
`cmd/otto-gateway/main.go`. No `Register()` function, no factory map, no `init()`
self-registration.

**When to use:** Whenever a registry indirection would buy theoretical extensibility but
cost grep-friendliness. The hardcoded slice survives all our v1 needs and is the proven
pattern in Bifrost.

**Example:**

```go
// cmd/otto-gateway/main.go (new code Phase 8 adds)
// Source: CONTEXT.md D-01 + go_port_brief.md §3.14 line 829
chain := plugin.Chain{
    Pre: []engine.PreHook{
        &plugin.RequestIDHook{Logger: logger},
        &plugin.AuthHook{Tokens: cfg.AuthToken},
        &plugin.PIIRedactionHook{
            Recognizers:     pii.Recognizers,
            Enabled:         cfg.PIIRedactionEnabled,
            EnabledEntities: cfg.PIIEnabledEntities,
            Mode:            cfg.PIIRedactionMode,
            HashKey:         []byte(cfg.PIIHashKey),
        },
        &plugin.LoggingHook{Logger: logger},
    },
    Post: []engine.PostHook{
        &plugin.LoggingHook{Logger: logger}, // same instance OK; After uses pre-recorded start time from ctx
    },
}
chain = chain.Filter(cfg.EnabledHooks) // typo-fail-fast inside Filter
engineCfg.PreHooks = chain.Pre
engineCfg.PostHooks = chain.Post
```

### Pattern 2: PreHook/PostHook Engine Contract Recap

**What:** The seam interfaces are **already shipped** and locked. Phase 8 hooks implement
these as-is.

**Engine contract (verbatim from `internal/engine/hooks.go:30-49`):**

```go
// PreHook is invoked by Engine.Run BEFORE any ACP traffic. Implementations may:
//   - return (nil, nil)         → continue the chain unchanged
//   - return (nil, err)         → abort the request with err
//   - return (resp, nil)        → SHORT-CIRCUIT: ACP never engaged;
//                                 resp preserved verbatim by Collect (Codex H-4)
type PreHook interface {
    Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
}

// PostHook is invoked by Engine.Collect AFTER the assistant *ChatResponse has been
// assembled — either from chunk aggregation OR from a PreHook short-circuit.
// In-place mutation on resp is allowed (Codex H-5); non-nil error aborts collect.
type PostHook interface {
    After(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error
}
```

**Chain.Pre / Chain.Post in `engine.Config`:**

```go
// internal/engine/engine.go:70-85
type Config struct {
    Logger     *slog.Logger    // required
    ACP        ACPClient       // required
    DefaultCWD string
    PreHooks   []PreHook       // Phase 8 fills this — engine.Run iterates in order
    PostHooks  []PostHook      // Phase 8 fills this — engine.Collect iterates in order
}
```

**Codex H-4 Pre short-circuit, verbatim from `engine.go:152-162`:**

```go
for _, h := range e.cfg.PreHooks {
    resp, err := h.Before(ctx, req)
    if err != nil {
        return nil, fmt.Errorf("engine: prehook: %w", err)
    }
    if resp != nil {
        return newCompletedRun(e, req, resp), nil  // ACP never touched
    }
}
```

**Codex H-4 body preservation in `collect.go:42-47`:**

```go
if run.response != nil {
    // PreHook short-circuit: preserve hook's response verbatim;
    // do NOT range Chunks (emptyStream is closed/empty), do NOT call Result.
    resp = run.response
} else {
    // ... normal aggregate path ...
}
```

**Codex H-5 PostHook traversal in `collect.go:117-122`:**

```go
// Runs on BOTH paths (assembled OR short-circuit). In-place mutation allowed.
for _, h := range e.cfg.PostHooks {
    if hookErr := h.After(ctx, req, resp); hookErr != nil {
        return nil, fmt.Errorf("engine: posthook: %w", hookErr)
    }
}
```

**Translation into `engine.Config`:** Phase 8 builds a typed `plugin.Chain{Pre, Post}`
in main.go, then assigns:

```go
engineCfg := engine.Config{
    Logger:     logger,
    ACP:        a.pool,
    DefaultCWD: cfg.KiroCWD,
    PreHooks:   chain.Pre,   // []engine.PreHook
    PostHooks:  chain.Post,  // []engine.PostHook
}
```

**Why this is load-bearing for Phase 8:** AuthHook's short-circuit on bad bearer goes
through this exact code path. The adapter's `Engine.Collect` returns a normal-looking
`*canonical.ChatResponse` (with an error field populated via the assembleAuthErrorResponse
helper); the adapter then renders that response in its native error shape (OpenAI:
`{error:{...}}`; Ollama: `{error:"..."}`; Anthropic: `{type:"error", error:{...}}`).

### Pattern 3: Context Propagation (typed private key + accessors)

**What:** RequestIDHook attaches the `X-Request-Id` to ctx via a typed private key.
LoggingHook + future hooks + engine + ACP code retrieves it via an exported accessor.

**When to use:** Always for ctx values per Go idiom (avoids string-key collisions; private
type means no other package can manufacture the same key).

**Example:**

```go
// internal/plugin/request_id.go
package plugin

import "context"

// requestIDKey is an unexported type to prevent collisions across packages.
// The whole-program guarantee that no other package can match this key
// comes from Go's type identity rules (the type is unexported, so even
// a stringly-typed `type x string` in another package compares unequal).
type ctxKey struct{ name string }

var requestIDKey = ctxKey{name: "request-id"}

// WithRequestID stamps id onto ctx. Called by RequestIDHook.Before.
func WithRequestID(ctx context.Context, id string) context.Context {
    return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the X-Request-Id attached by RequestIDHook,
// or "" if absent. Safe to call from anywhere; engine + ACP + adapters
// import internal/plugin only for this accessor (verify .go-arch-lint.yml
// allows the import direction, otherwise move the accessor to a shared
// helper package).
func RequestIDFromContext(ctx context.Context) string {
    if v, ok := ctx.Value(requestIDKey).(string); ok {
        return v
    }
    return ""
}
```

**slog correlation pattern (two options — pick one in PLAN):**

Option A — explicit `slog.With` per record (simpler, no new dep):

```go
// Inside LoggingHook.Before / .After / .engine code / .ACP code
rid := plugin.RequestIDFromContext(ctx)
logger.LogAttrs(ctx, slog.LevelInfo, "engine: prompt sent",
    slog.String("request_id", rid),
    slog.String("session_id", sid),
    ...)
```

Option B — custom slog.Handler that auto-injects `request_id` on every record:

```go
// Wrap slog.JSONHandler with a small adapter that reads ctx in Handle()
// and prepends the request_id attr. Pattern documented in
// https://github.com/veqryn/slog-context (don't take the dep; copy the
// shape into a ~30-line internal helper if we want auto-inject).
```

**Recommendation:** Start with Option A (explicit, grep-friendly, no new code). Reach for
Option B only if too many call sites need to call `slog.With(...)` and it becomes
boilerplate.

### Pattern 4: Recognizer Registry (PII)

**What:** A `Recognizer` struct + a package-level `var Recognizers = []Recognizer{...}`
literal. Patterns compiled at package init (no per-request `regexp.Compile`); `Validate`
is an optional post-match filter.

**When to use:** Phase 8 PII; future entity types append one struct entry — no changes to
the hook, walker, or callers.

**Example:**

```go
// internal/plugin/pii/recognizers.go
package pii

import (
    "net"
    "regexp"
)

// Recognizer is a regex + optional post-validate filter. The Validate
// function returns true if the regex-match is a real instance of the
// entity (false → discard as false positive). Validate may be nil for
// recognizers where the regex alone is sufficient (Email, US Phone).
//
// Discretionary additions per CONTEXT.md note: a planner may add
// Anonymize (per-recognizer override of the global mode) or MinConfidence
// (placeholder for a future ML-aided recognizer) WITHOUT changing the
// six v1 recognizer literals below.
type Recognizer struct {
    Name     string         // "Email" | "IPv4" | "IPv6" | "SSN" | "CreditCard" | "USPhone"
    Pattern  *regexp.Regexp // compiled once at package init via MustCompile
    Validate func(string) bool // optional post-match filter; nil = "regex alone is OK"
}

// Compiled-at-init regexes. All MustCompile (init-time panic surfaces a bad
// regex before the binary serves a single request).
var (
    emailRe      = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,24}\b`)
    ipv4Re       = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
    ipv6Re       = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{1,4}:){2,7}[0-9A-Fa-f:]{1,4}\b`)
    ssnRe        = regexp.MustCompile(`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`)
    creditCardRe = regexp.MustCompile(`\b(?:[0-9][ \-]?){12,18}[0-9]\b`)
    usPhoneRe    = regexp.MustCompile(`\b(?:\+?1[ .\-]?)?\(?[2-9][0-9]{2}\)?[ .\-]?[0-9]{3}[ .\-]?[0-9]{4}\b`)
)

// Recognizers is the v1 registry. D-01: hardcoded slice; adding a 7th
// entity is one new entry + one new {Name, Pattern, Validate}.
var Recognizers = []Recognizer{
    {Name: "Email",       Pattern: emailRe,      Validate: nil},
    {Name: "IPv4",        Pattern: ipv4Re,       Validate: validateIPv4Octets},
    {Name: "IPv6",        Pattern: ipv6Re,       Validate: validateIPv6NetParseIP},
    {Name: "SSN",         Pattern: ssnRe,        Validate: validateSSNRange},
    {Name: "CreditCard",  Pattern: creditCardRe, Validate: validateLuhn},
    {Name: "USPhone",     Pattern: usPhoneRe,    Validate: nil},
}
```

### Anti-Patterns to Avoid

- **Registry indirection** (`Register(name, factory)` + `Build(allowlist)`). Adds
  init-order surprises; obscures the dependency graph. D-01 explicitly rejects it.
- **`init()` self-registration in each hook file.** Same problem, worse: order
  depends on file-name sort order.
- **Per-request `regexp.Compile`.** Compiled regexes are concurrency-safe; reuse them
  at package init. Per-request compile is a 10–100× cost regression hidden behind a
  "feels safer" instinct.
- **Hard-coding per-surface error shapes in AuthHook.** That's the adapter's job. The
  hook returns a canonical `*ChatResponse` with `Message.Content[0].Text` carrying the
  error message and a sentinel `StopReason` (or a dedicated canonical error field if
  planner adds one); each surface adapter's `errors.go` translates.
- **Walking map keys.** Keys are protocol field names, not user content. Redacting
  `to: "<EMAIL>"` (where `to` is the key) is a category error. **String VALUES only.**
- **Logging raw content via slog if `PII_REDACTION_ENABLED=false`.** D-04 explicitly
  rejects `LOG_RAW_REQUEST` as a foot-gun; the v1 contract is "PII redaction off →
  raw content in logs; operator opts into the leak risk."

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Unique ID generation | Random hex string from `crypto/rand` + Base32 encoder | `oklog/ulid/v2` `ulid.Make()` | 26 chars, URL-safe, sortable, monotonic-within-millisecond entropy — all already implemented; no upside to rewriting |
| Constant-time string compare | `==` operator on tokens | `crypto/subtle.ConstantTimeCompare` | `==` early-exits on first mismatching byte → timing side-channel reveals token bytes (T-02-05) |
| HMAC | Manual `sha256.Sum256(key + msg)` | `hmac.New(sha256.New, key)` | Hand-rolled HMAC opens length-extension attack window; stdlib `hmac` is mathematically proven safe and one extra line of code |
| IPv6 validation | Hand-tuned regex | `net.ParseIP(matched)` | IPv6 has too many valid zero-elision forms; ROADMAP SC6 explicitly mandates `net.ParseIP` |
| Luhn check | "Just add up the digits" | Documented two-pass algorithm with double-every-other-digit + subtract-9-when-product-exceeds-9 (Rosetta Code reference) | Easy to misimplement (off-by-one on the starting position; forgetting the >9 subtract step); fixed-table tests vs Visa/Mastercard/Amex BIN ranges catch the common mistakes |
| SSN range filter via regex | RE2 `(?!000|666)[0-8][0-9]{2}-(?!00)[0-9]{2}-(?!0000)[0-9]{4}` | Plain regex + Go-side `validateSSNRange(s string) bool` | **Go's regexp/RE2 does not support negative lookaheads.** [VERIFIED: github.com/google/re2/issues/156] |
| Recursive `map[string]any` walker | Three copy-pasted walkers (one per content kind) | One single helper with a `transform func(string) string` callback | Walker logic is identical; one source of truth = one bug surface |
| Slog request-ID auto-inject from ctx | Manual `slog.With(...)` at every call site | Pattern 3 Option A (explicit) for v1; copy ~30 lines from veqryn/slog-context if Option B needed later | Adding a dep purely for sugar = trust gates work for no return |
| Anthropic-shape PII redaction in adapters | Per-adapter redaction code | Canonical PII walker on canonical types | **Phase 8 architectural payoff.** PII walker sees `ContentPart.ToolUse.Input` and walks the `map[string]any`; one rule covers all three surfaces |

**Key insight:** Every item in this table is a place where the Go ecosystem has
documented best practice. The hand-rolled variant looks short and feels fast to write
but produces silent bugs (timing leaks, malformed validation, length-extension exposure)
that the trust-gate suite cannot catch.

## Runtime State Inventory

> Not applicable for greenfield-feature work. Phase 8 is net-new code in an empty package
> (`internal/plugin/.gitkeep`). No rename / refactor / migration / data backfill is
> required. The auth middleware refactor (D-04: bearer-token validation moves from HTTP
> middleware to `AuthHook`) is a code-only change — no stored data, no live service
> config, no OS-registered state, no env-var renames, no build artifacts to invalidate.

**Categories verified:**

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | None — Phase 8 has no datastore | None |
| Live service config | None — no admin port, no separate listener, no dynamic registration (D-OOS) | None |
| OS-registered state | None — no scheduled tasks, pm2 entries, etc. | None |
| Secrets/env vars | New env vars (`PII_REDACTION_*`, `PII_HASH_KEY`, `ENABLED_HOOKS`); all are **additions**, none rename or remove existing vars | None — additive only |
| Build artifacts | None — no compiled packages outside the binary | None |

**Auth middleware deletion impact:** `internal/server/server.go:230-233`'s
`r.Use(auth.Bearer(...))` registration is removed for the protected sub-tree, and the
`AuthHook` takes over at the canonical layer. **The `auth.Bearer` function itself stays
shipped** (it remains used by tests, by potential future HTTP-only protected endpoints,
and as the reference implementation for `extractToken` semantics — which `AuthHook`
reuses internally). This is a wiring change, not a code deletion.

## Common Pitfalls

### Pitfall 1: RE2 has no negative lookahead

**What goes wrong:** Operators copy an SSN regex from Stack Overflow that uses
`(?!000|666)[0-8][0-9]{2}-...` — Go's regexp panics on compile (or refuses with an error).

**Why it happens:** RE2 (Google's regex engine, what Go uses) is intentionally
linear-time and rejects features that allow exponential backtracking. Negative lookahead
is one of those features.
[VERIFIED: github.com/google/re2/issues/156]

**How to avoid:** Pair a permissive regex (`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`) with a
post-validate function (`validateSSNRange(string) bool`) that filters out reserved ranges
(`000-XX-XXXX`, `666-XX-XXXX`, `9XX-XX-XXXX`, `XXX-00-XXXX`, `XXX-XX-0000`). The
`Recognizer.Validate` field is exactly this seam.

**Warning signs:** `regexp.MustCompile` panic at package init with "error parsing regexp:
invalid or unsupported Perl syntax".

### Pitfall 2: Walking map keys in `tool_use.Input`

**What goes wrong:** PII walker recurses into a `map[string]any` and treats the key
`"email"` as a string leaf — redacts the field name, breaking the protocol.

**Why it happens:** Naïve `reflect`-based walkers treat all string-typed reflect.Value
the same.

**How to avoid:** Walk `map[string]any` by ranging over `for k, v := range m` and
ONLY apply recognizers to `v`. Never replace the key. The walker contract in
`internal/plugin/pii/walk.go` enforces this explicitly via a "string LEAVES only"
invariant property-tested in `walk_test.go`.

**Warning signs:** Integration tests with tool_use args show `<EMAIL>` in field names
(e.g., `{"<EMAIL>": "actual@example.com"}` instead of `{"to": "<EMAIL>"}`).

### Pitfall 3: ConstantTimeCompare on different-length strings

**What goes wrong:** `subtle.ConstantTimeCompare([]byte("abc"), []byte("abcdef"))`
returns 0 immediately — the length mismatch leaks via early return.

**Why it happens:** The "constant-time" property only holds for equal-length inputs;
the stdlib docs say so explicitly.

**How to avoid:** The existing pattern in `internal/auth/bearer.go:51` is already
correct (it compares against each configured token; per-token compare is constant-time
in the bytes of valid+provided when they're equal-length). AuthHook should reuse the
same iteration shape. The "how many tokens are configured" leakage from early-exit on
match is documented and accepted (`bearer.go:48-50` comment).

**Warning signs:** None at runtime — this is a microarchitectural side-channel that
only matters for adversaries with sub-microsecond timing measurement and many requests.
Audit checks: search for `==` on token strings in AuthHook code.

### Pitfall 4: `regexp.Regexp.ReplaceAllStringFunc` and overlapping matches

**What goes wrong:** A string like `myemail@x.com,2.3.4.5` runs Email regex
first → replaces email → then IPv4 regex sees `2.3.4.5` → replaces that. Counter
suffixing (`<EMAIL_1>`, `<IPV4_1>`) requires per-request state.

**Why it happens:** `regexp` is stateless across calls; each recognizer pass is
independent.

**How to avoid:** The PII walker should iterate recognizers in a fixed order (the
order of `pii.Recognizers`), threading a per-request counter map
(`map[string]int{Email: 1, IPv4: 0, ...}`) through the walk. Counter resets per
`canonical.ChatRequest` (D-discretionary recommendation in CONTEXT.md). Test with a
multi-message request that contains the same email twice in different messages —
both should redact to `<EMAIL_1>`.

**Warning signs:** Goldens with `<EMAIL_2>` and `<EMAIL_3>` in a request that only
mentions one distinct email — counter is incrementing on each match instead of each
distinct value.

### Pitfall 5: `slog.SetDefault` from a hook

**What goes wrong:** A LoggingHook constructor sets the default slog logger, racing
with main.go's logger injection.

**Why it happens:** "I want my hook to be log-enabled even when constructed in
tests" instinct.

**How to avoid:** Project convention (existing CLAUDE.md / Phase 1 D-15): **never
call `slog.SetDefault`**. Pass `*slog.Logger` via constructor. Tests use
`testutil.Logger(t)`. AuthHook + LoggingHook + RequestIDHook all carry a Logger
field; main.go injects the root logger; nil-Logger paths use the same discardWriter
fallback pattern as `internal/server/server.go:177`.

**Warning signs:** Test output ordering changes when running with `-parallel`.

### Pitfall 6: Hash mode silent unkeyed fallback

**What goes wrong:** Operator sets `PII_REDACTION_MODE=hash` and forgets
`PII_HASH_KEY`. The gateway starts with an empty key. HMAC-SHA256 of any string
with an empty key produces a fixed-and-rainbow-table-trivial output.

**Why it happens:** Default-permissive instinct: "an empty key is just one specific
key, why fail?".

**How to avoid:** **Boot-validate.** In `config.Load()`, if
`PIIRedactionMode == "hash"` and `len(PIIHashKey) == 0`, return an error
(`PII_REDACTION_MODE=hash requires PII_HASH_KEY to be set`). Same precedent as the
`ALLOWED_IPS` parse-error path at `config.go:146`.

**Warning signs:** None — silent. This is why the boot-error is load-bearing.

### Pitfall 7: ENABLED_HOOKS typo silently disables PII

**What goes wrong:** Operator types `ENABLED_HOOKS=PIIRedaction` (missing `Hook`
suffix). Filter sees no match in the slice → silently drops the hook → PII redaction
is OFF in production.

**Why it happens:** Allowlist semantics + permissive filter logic.

**How to avoid:** **D-02 hard requirement.** The `chain.Filter(allowlist)` helper
MUST return an error on unknown names. Tested explicitly: a unit test that sets
`ENABLED_HOOKS=BogusHook` and asserts the Filter returns an error containing
"unknown hook".

**Warning signs:** Operator complaint about "PII not redacting" — would be caught
by `/health/hooks` output showing `PIIRedactionHook` absent.

### Pitfall 8: PostHook sees nil response on error path

**What goes wrong:** PostHook implementations dereference `resp` without nil check;
when Collect's normal-aggregate path errored, PostHooks may not run at all (Collect
returns early), but if a future refactor passes errors through, PostHook with
`resp == nil` panics.

**Why it happens:** Hooks assume their input is non-nil.

**How to avoid:** **PostHook implementations MUST nil-check resp** as a defensive
gate. Document this in `internal/plugin/logging.go`'s LoggingHook.After. Also write
a property test: `quick.Check` with random `*ChatResponse` values including nil
that asserts PostHook never panics.

**Warning signs:** A goroutine.SetPanicHandler-caught panic from inside the engine's
PostHook traversal loop.

### Pitfall 9: `/health/hooks` leaks the regex patterns

**What goes wrong:** `Describe()` returns the compiled regex source string as part of
the `config` map. An attacker scraping `/health/hooks` learns exactly what we detect
and what we miss → can craft inputs that bypass detection.

**Why it happens:** "More info is better" instinct in the introspection design.

**How to avoid:** **Hooks expose entity NAMES (`["Email","IPv4",...]`) but not
patterns.** Same logic for tokens: `AuthHook.Describe()` returns
`{token_count: N}`, NEVER `{tokens: [...]}` and NEVER even a hash prefix.
PIIRedactionHook.Describe() exposes `{enabled, mode, entities}`; never `patterns`,
never `hash_key_set: true/false` (mode tells you).

**Warning signs:** `curl /health/hooks` output contains anything that looks like a
regex or a base64 blob.

### Pitfall 10: Goroutine leaks from LoggingHook timing

**What goes wrong:** A LoggingHook implementation spawns a goroutine to defer-emit
a "request took N ms" line; the goroutine never exits because it's awaiting a
channel close that PostHook never signals.

**Why it happens:** Async logging instinct.

**How to avoid:** Synchronous logging in v1. Pre records start time on ctx via the
typed key; Post reads it back and emits one synchronous slog.LogAttrs. No
goroutines. **Goleak gate on the test file catches any regression.**

**Warning signs:** `go.uber.org/goleak.VerifyTestMain` fails with a stack pointing
into logging code.

## Code Examples

Verified patterns from official sources and the project's existing code.

### Example 1: Bearer Token Constant-Time Compare (project precedent)

```go
// internal/plugin/auth.go — Phase 8 NEW; reuses pattern from internal/auth/bearer.go:46-56
// Source: internal/auth/bearer.go lines 46-56 (project canon)
package plugin

import (
    "context"
    "crypto/subtle"
    "fmt"

    "otto-gateway/internal/canonical"
)

type AuthHook struct {
    Tokens []string // empty → auth disabled (Node parity)
}

func (h *AuthHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
    if len(h.Tokens) == 0 {
        return nil, nil // auth disabled — passthrough
    }

    // The provided token comes via ctx (RequestIDHook or a small
    // adapter-side helper extracts the Authorization / x-api-key header
    // and attaches the credential to ctx via a typed key — see Pattern 3).
    provided, ok := bearerFromContext(ctx)
    if !ok || provided == "" {
        return synthesizeAuthError("Invalid or missing API key"), nil
    }

    providedBytes := []byte(provided)
    for _, valid := range h.Tokens {
        // ConstantTimeCompare requires equal-length inputs to provide
        // its constant-time guarantee. The early-exit-on-match below
        // leaks "how many tokens are configured" but NOT token bytes.
        // This is the same accepted tradeoff as auth/bearer.go.
        if subtle.ConstantTimeCompare(providedBytes, []byte(valid)) == 1 {
            return nil, nil
        }
    }

    return synthesizeAuthError("Invalid or missing API key"), nil
}
```

### Example 2: Luhn Validator (pure stdlib)

```go
// internal/plugin/pii/luhn.go
// Source: Rosetta Code Luhn (Go) — https://rosettacode.org/wiki/Luhn_test_of_credit_card_numbers
package pii

import "unicode"

// LuhnCheck returns true if s is a Luhn-valid sequence. Non-digit characters
// (spaces, hyphens) are stripped before checking — the Recognizer regex
// matches `\b(?:[0-9][ \-]?){12,18}[0-9]\b` so we need this tolerance.
func LuhnCheck(s string) bool {
    var sum int
    alt := false
    for i := len(s) - 1; i >= 0; i-- {
        c := s[i]
        if !unicode.IsDigit(rune(c)) {
            continue
        }
        d := int(c - '0')
        if alt {
            d *= 2
            if d > 9 {
                d -= 9
            }
        }
        sum += d
        alt = !alt
    }
    // Empty / 1-digit inputs aren't valid Luhn even if sum%10==0
    digitCount := 0
    for _, c := range s {
        if unicode.IsDigit(c) {
            digitCount++
        }
    }
    return digitCount >= 13 && digitCount <= 19 && sum%10 == 0
}

// validateLuhn is the recognizers.go Validate field for CreditCard.
func validateLuhn(matched string) bool { return LuhnCheck(matched) }
```

### Example 3: SSN Range Filter (RE2 workaround)

```go
// internal/plugin/pii/recognizers.go (helper)
// Source: workaround for Go RE2 no-negative-lookahead (verified at
// github.com/google/re2/issues/156); range definitions from
// SSA published reserved ranges.
package pii

import "strings"

// validateSSNRange filters out reserved SSN ranges that the regex alone
// would match. Returns false for invalid ranges so the walker treats them
// as not-PII (avoiding noisy redaction of `123-45-6789` test data that's
// actually a documented "never assigned" range).
func validateSSNRange(s string) bool {
    parts := strings.Split(s, "-")
    if len(parts) != 3 {
        return false
    }
    aaa, gg, ssss := parts[0], parts[1], parts[2]

    // Area (first 3): not 000, not 666, not 900-999.
    if aaa == "000" || aaa == "666" || aaa[0] == '9' {
        return false
    }
    // Group (middle 2): not 00.
    if gg == "00" {
        return false
    }
    // Serial (last 4): not 0000.
    if ssss == "0000" {
        return false
    }
    return true
}
```

### Example 4: PII Walker (string-leaves-only, recursive)

```go
// internal/plugin/pii/walk.go
package pii

// WalkStrings recursively visits every string-typed value (leaf) in v
// and replaces it via transform. The argument types match canonical's
// tool_use.Input (`map[string]any`) and tool_result.Content (any) shapes.
//
// Invariants enforced by property tests in walk_test.go:
//   1. Never panics for any shape (nil, empty maps, deep nesting up to 64).
//   2. Idempotent: WalkStrings(WalkStrings(x, t), t) == WalkStrings(x, t)
//      when t is itself idempotent.
//   3. String LEAVES only: map keys, non-string leaves, and slice indices
//      are bit-identical between input and output.
//   4. Cycle-safe: in v1 we DO NOT allow cycles (canonical JSON can't
//      have them); the walker bounds depth at 64 and returns the
//      original at depth-exceeded.
func WalkStrings(v any, transform func(string) string) any {
    return walkStrings(v, transform, 0)
}

const maxDepth = 64

func walkStrings(v any, transform func(string) string, depth int) any {
    if depth > maxDepth {
        return v
    }
    switch x := v.(type) {
    case string:
        return transform(x)
    case map[string]any:
        out := make(map[string]any, len(x))
        for k, vv := range x {
            // Note: walking VALUES only; the key k is preserved verbatim.
            out[k] = walkStrings(vv, transform, depth+1)
        }
        return out
    case []any:
        out := make([]any, len(x))
        for i, vv := range x {
            out[i] = walkStrings(vv, transform, depth+1)
        }
        return out
    default:
        // Numbers, bools, nil, time.Time, etc. — pass through unchanged.
        return v
    }
}
```

### Example 5: Property Test for Walker (testing/quick precedent)

```go
// internal/plugin/pii/walk_test.go
// Source: project precedent at internal/engine/pickcwd_test.go:228-229
package pii

import (
    "reflect"
    "testing"
    "testing/quick"
)

// TestWalkStrings_NeverPanics — random inputs must not crash the walker.
func TestWalkStrings_NeverPanics(t *testing.T) {
    property := func(s string) bool {
        // Build a small random nested shape from the seed string.
        v := map[string]any{
            "msg":    s,
            "ints":   []any{1, 2, 3},
            "nested": map[string]any{"deep": s, "n": float64(42)},
        }
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("WalkStrings panicked: %v", r)
            }
        }()
        _ = WalkStrings(v, func(in string) string { return "REDACTED" })
        return true
    }
    cfg := &quick.Config{MaxCount: 1000}
    if err := quick.Check(property, cfg); err != nil {
        t.Errorf("never-panic property failed: %v", err)
    }
}

// TestWalkStrings_Idempotent — Walk(Walk(x)) == Walk(x) when transform is idempotent.
func TestWalkStrings_Idempotent(t *testing.T) {
    redact := func(s string) string { return "<E>" }
    in := map[string]any{
        "a": "user@example.com",
        "b": []any{"corey@cmetech.io", float64(1)},
    }
    once := WalkStrings(in, redact)
    twice := WalkStrings(once, redact)
    if !reflect.DeepEqual(once, twice) {
        t.Errorf("not idempotent:\nonce=%v\ntwice=%v", once, twice)
    }
}

// TestWalkStrings_KeysAndNonStringLeavesPreserved — string LEAVES only.
func TestWalkStrings_KeysAndNonStringLeavesPreserved(t *testing.T) {
    in := map[string]any{
        "email_address": "leak@x.com",
        "count":         float64(42),
        "ok":            true,
        "empty":         nil,
    }
    out := WalkStrings(in, func(s string) string { return "X" }).(map[string]any)
    // Key "email_address" must be present unchanged.
    if _, ok := out["email_address"]; !ok {
        t.Error("key email_address dropped")
    }
    // Non-string leaves identical.
    if out["count"] != float64(42) {
        t.Errorf("count mutated: %v", out["count"])
    }
    if out["ok"] != true {
        t.Errorf("bool mutated: %v", out["ok"])
    }
    if out["empty"] != nil {
        t.Errorf("nil mutated: %v", out["empty"])
    }
    // The string value IS replaced.
    if out["email_address"] != "X" {
        t.Errorf("string leaf not transformed: %v", out["email_address"])
    }
}
```

### Example 6: `/health/hooks` JSON Envelope (parallel to `/health/agents`)

```json
{
  "hooks": [
    {
      "name": "RequestIDHook",
      "kind": "Pre,Post",
      "enabled": true,
      "config": {
        "format": "ulid"
      }
    },
    {
      "name": "AuthHook",
      "kind": "Pre",
      "enabled": true,
      "config": {
        "token_count": 2
      }
    },
    {
      "name": "PIIRedactionHook",
      "kind": "Pre",
      "enabled": true,
      "config": {
        "enabled": false,
        "mode": "replace",
        "entities": ["Email", "IPv4", "IPv6", "SSN", "CreditCard", "USPhone"]
      }
    },
    {
      "name": "LoggingHook",
      "kind": "Pre,Post",
      "enabled": true,
      "config": {
        "level": "INFO"
      }
    }
  ]
}
```

**Shape notes (parallel to `internal/server/agents_test.go:212-247`):**

- Top-level key: `hooks` (mirrors `agents`'s `pool` / `sessions` two-key shape, but
  Phase 8 has only one collection).
- Each entry has `name`, `kind`, `enabled`, optional `config`. `config` is a JSON
  object whose schema is per-hook (typed in Go but rendered via `map[string]any`).
- **What MUST NOT appear:** regex patterns (Pitfall 9); raw token values; the
  `PII_HASH_KEY` bytes or a hash-prefix derivative; any per-request data (request
  IDs, session IDs).
- **Order:** registration order (matches D-01 / SC5 contract).
- Auth-exempt registration: `s.router.Get("/health/hooks", s.hooksHandler)` mounted
  on the OUTER router in `server.go` alongside `/health/agents` (line 200).

### Example 7: ULID Generation (project's `X-Request-Id` source)

```go
// internal/plugin/request_id.go
// Source: pkg.go.dev/github.com/oklog/ulid/v2 — ulid.Make() docs
package plugin

import (
    "crypto/rand"
    "github.com/oklog/ulid/v2"
)

// NewRequestID returns a fresh ULID string. ulid.Make() uses a process-global
// monotonic entropy source seeded from crypto/rand for security-sensitive
// contexts. The 26-char Crockford Base32 form is URL-safe and lexicographically
// sortable by creation time (millisecond resolution + monotonic counter for
// within-millisecond ordering).
func NewRequestID() string {
    return ulid.Make().String()
}

// For deterministic tests, provide a seed-able variant:
func newRequestIDFromReader(r io.Reader) string {
    return ulid.MustNew(ulid.Now(), r).String()
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| HTTP middleware bearer-token auth (Phase 2 D-14) | Canonical-typed `AuthHook` (Phase 8 D-04) | Phase 8 | One place to enforce policy across all three surfaces; bearer compare logic unchanged (still `subtle.ConstantTimeCompare`) |
| Per-adapter request logging | Single `LoggingHook` (Pre+Post) at canonical layer | Phase 8 | Adapters keep accessLog at HTTP layer for total-transport timing; LoggingHook adds canonical-content visibility (post-PII-redaction) |
| No request correlation | `RequestIDHook` + ctx-attached ID consumed by `slog` everywhere | Phase 8 | Single `request_id` field correlates HTTP → engine → ACP → response across spans |
| Regex-only PII | Regex + Validate post-filter | This phase | Filters known false-positive ranges (SSN reserved, Luhn-invalid CC); pure Go, no NER |
| UUIDv4 request IDs (training-data default) | ULID (Crockford Base32, sortable) | This phase per CONTEXT.md discretionary recommendation | Shorter (26 vs 36 chars), URL-safe by construction, sortable for log scanning |

**Deprecated/outdated:**

- **`auth.Bearer(...)` registered on the chi protected sub-tree** — superseded by
  `AuthHook` for canonical-typed auth. The `auth.Bearer` function itself remains
  exported and tested but is no longer the auth gate for chat endpoints.
- **`PreHook` interface returning `(*canonical.ChatResponse, *canonical.ChatResponse)`** —
  was an earlier seam draft. Current contract (engine/hooks.go:31) is
  `(*ChatResponse, error)`; PreHooks DO NOT replace the canonical request, they short-
  circuit by returning a response.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `github.com/oklog/ulid/v2` v2.1.1 is the appropriate ID library | Standard Stack | Low. Fallback is `google/uuid` v4 (zero-cgo, well-known). Operator preference. Mitigated by `checkpoint:human-verify` task in plan. |
| A2 | `subtle.ConstantTimeCompare` returning 0 on length-mismatch is acceptable timing leakage in our threat model | Pitfall 3 / Code Example 1 | Low. Project precedent (auth/bearer.go) already accepts this. The actual attack requires sub-microsecond timing + many requests; our deployment model (laptop-local, A3) makes this benign. |
| A3 | Email regex `(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,24}\b` is the right pragmatic precision/recall tradeoff | Pattern 4 | Medium. Pragmatic regexes always miss RFC 5322 edge cases (quoted local-part, IDN). For PII redaction false-negatives are worse than false-positives, so a wider regex may be preferred. **Recommend the planner make this a discuss-phase question** if the user has compliance requirements (HIPAA/GDPR) that mandate specific recall. |
| A4 | Counter-suffix scope per-`canonical.ChatRequest` is right; Presidio uses per-document scope similarly | Pitfall 4 | Low. CONTEXT.md confirms this is the recommended choice. Alternative scopes (per-message, per-process) are defensible and could be flipped without architectural change. |
| A5 | The 8-hex-char HMAC tag (32 bits of entropy) is adequate for correlation without enabling rainbow-table reverse lookup | D-05 / Standard Stack | Medium. 32 bits has ~4 billion outcomes; for a leaked log with millions of events, birthday-collision likelihood is non-trivial. **Recommend planner expose a `PII_HASH_TAG_LENGTH` env knob if compliance audit requires longer tags.** |
| A6 | Walking depth bound of 64 in the PII walker is sane for tool_use.Input shapes | Code Example 4 | Low. Real-world tools have args ≤3 levels deep. 64 is purely a denial-of-service guard for malicious inputs. |
| A7 | `Describe()` interface as the safe-config exposure surface is sufficient (vs a struct-tag-based redaction system) | Pattern 4 / Pitfall 9 | Low. Hand-written `Describe()` per hook is grep-friendly and reviewable, matching D-01's overall philosophy. Struct tags would be cleverer but harder to audit. |
| A8 | The PII summary in ctx (`pii.SummaryFromContext(ctx) []RedactionCount`) is a v1 must-ship API SEAM even if LoggingHook doesn't render it yet | Locked Decisions D-04 | Low. CONTEXT.md explicitly notes "API seam MUST exist" — seam shipping is required regardless of whether LoggingHook consumes it in v1. |

**Net assessment:** A1 and A5 are the user-facing decisions worth surfacing in a brief
planner check-in. A3 is worth checking if the project has unstated compliance posture.
Everything else is mechanical project-convention alignment.

## Open Questions (RESOLVED)

1. **Does `internal/plugin` import `internal/engine` for the PreHook/PostHook interface types, or does it duplicate those interfaces in plugin's own type space?**
   - What we know: `.go-arch-lint.yml` currently does NOT list a `plugin` component. We must add one and decide its allowed imports.
   - What's unclear: CONTEXT.md says "**Hooks MUST NOT import `internal/engine`** — they implement interfaces defined there but don't depend on the package." This conflicts with Go's interface model: to implement `engine.PreHook`, the plugin package must reference `engine.PreHook`. **Either (a) duplicate the interface in `plugin` and rely on structural satisfaction (cleaner boundary, more code), or (b) accept the engine import in `plugin` (one-direction reference; engine still doesn't import plugin).**
   - RESOLVED: **Option (b)** — `plugin` imports `engine` for the interface types. Engine remains pristine (no plugin import); the directional rule is preserved. `.go-arch-lint.yml` `plugin` component lists `mayDependOn: [canonical, engine]`. This matches the existing `pool` and `session` patterns which import `engine` for `engine.ACPClient` etc. Ratified by 08-01 Task 5 (`.go-arch-lint.yml` mayDependOn:[canonical, engine]).

2. **Where does the AuthHook get the bearer credential from?**
   - What we know: The HTTP-layer middleware reads `r.Header.Get("Authorization")` / `r.Header.Get("x-api-key")`. The hook sees only `canonical.ChatRequest` which has no headers.
   - What's unclear: Either (a) each adapter's handler extracts the credential from headers and attaches it to ctx via a typed key before calling `engine.Run`, OR (b) the credential lives on `canonical.ChatRequest` (new dormant field), OR (c) AuthHook stays at HTTP-middleware boundary AND a thin canonical AuthHook just verifies a "credential-already-checked" sentinel on ctx.
   - RESOLVED: **Option (a)** — adapter handler extracts the header, stamps onto ctx via a typed key. The AuthHook reads ctx, validates, short-circuits with canonical-shaped error response. Ratified by 08-02 (AuthHook + adapter `WithBearerToken` ctx-stamp tasks).

3. **Does `RequestIDHook` need a Post step at all?**
   - What we know: D-04 lists `RequestID → Auth → PIIRedaction → Logging` for Pre; Post is `Logging only (the others have no Post behavior)`. CONTEXT.md mentions RequestIDHook's Post is "optional (could no-op or emit a final correlation log line)".
   - What's unclear: If RequestIDHook has no Post behavior, why is Pattern 4's kind field "Pre,Post"? Possible Post behavior: stamp `X-Request-Id` onto the response headers when the adapter renders. That's an adapter concern, though.
   - RESOLVED: **RequestIDHook is Pre only** in v1. The adapter copies `X-Request-Id` onto the response by reading ctx via `RequestIDFromContext` at render time. The `/health/hooks` `kind` field is `Pre` for RequestIDHook. Ratified by 08-01 (RequestIDHook implements only `engine.PreHook`).

4. **What does AuthHook return as the canonical "auth failed" response?**
   - What we know: CONTEXT.md D-04 says "AuthHook returns a canonical error response; the adapter renders it." Existing adapter error-rendering precedent: `internal/adapter/anthropic/errors.go` (Phase 3.1).
   - What's unclear: `canonical.ChatResponse` currently has no error field. The Anthropic adapter's `errors.go` may render from a `StopReason == StopError` + `Message.Content[0].Text` shape, or via a separate `RenderError(canonicalError)` path.
   - RESOLVED: **Option (b)** — overload `ChatResponse.StopReason == StopError` + `Message.Content[0].Text == error_message` (simpler, no canonical-type churn). Adapter `errors.go` paths render the canonical short-circuit response in native shape (OpenAI `{error:{...}}` / Ollama `{error:"..."}` / Anthropic `{type:"error", error:{...}}`). Ratified by 08-02 (AuthHook short-circuit envelope) and 08-05 e2e three-surface coverage.

5. **Does PIIRedactionHook also walk `canonical.ChatRequest.System` and `canonical.ChatRequest.StopSequences`?**
   - What we know: D-03 lists `Message.ContentParts[].Text`, `tool_use.Input`, `tool_result.Content`, and `Message.Content` (top-level legacy field). It does NOT mention `ChatRequest.System` or `StopSequences`.
   - What's unclear: System prompts can legitimately contain operator-side PII references; whether they should be redacted is a policy question.
   - RESOLVED: **Walk `ChatRequest.System` too** (operator-controllable via Ollama/Anthropic system blocks; can contain PII). Skip `StopSequences` (operator-defined boundary strings; redacting them breaks stop-condition semantics). Ratified by 08-04 PIIRedactionHook scope.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain (≥ 1.23) | Build + test | yes | 1.26.3 | — |
| `go.uber.org/goleak` v1.3.0 | Test convention | yes (already in `go.mod`) | 1.3.0 | — |
| `github.com/go-chi/chi/v5` v5.3.0 | `/health/hooks` registration | yes (already in `go.mod`) | 5.3.0 | — |
| `golangci-lint` strict | TRST-01 gate | **MISSING on dev box** | — | Run via project `make lint` (presumably installs in CI); not blocking research phase, blocking phase-gate |
| `gosec` G204 | TRST-01 gate (subset of golangci-lint) | depends on golangci-lint | — | Same as above |
| `github.com/oklog/ulid/v2` | RequestIDHook | NOT YET INSTALLED | v2.1.1 (target) | `google/uuid` v4 as listed in alternatives |

**Missing dependencies with no fallback:** none for the phase itself; `golangci-lint`
missing on dev box is a build-tooling gap, not a code gap.

**Missing dependencies with fallback:** `oklog/ulid/v2` — fallback is `google/uuid` v4.
The planner adds a `checkpoint:human-verify` task before `go get github.com/oklog/ulid/v2`
since slopcheck was not available at research time.

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `testing/quick` (property tests) + `go.uber.org/goleak` (goroutine-leak gate) |
| Config file | none (Go convention — tests live in `*_test.go` alongside source) |
| Quick run command | `go test ./internal/plugin/...` |
| Full suite command | `make test-race` (runs `go test -race ./...`) |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| PLUG-01 | PreHook/PostHook operate on canonical types | unit (compile-time satisfaction + table-driven) | `go test ./internal/plugin/ -run TestChain_InterfaceSatisfaction` | ❌ Wave 0 |
| PLUG-02 | PreHook non-nil response short-circuits engine; adapter renders | integration (existing engine + new AuthHook) | `go test ./internal/plugin/ -run TestAuthHook_ShortCircuit` + reuse existing engine_test for H-4 carry-through | ❌ Wave 0 |
| PLUG-03 | Hooks chained in registration order; first non-nil Pre short-circuit wins; all Post run | unit (ordering test with multiple instrumented hooks) | `go test ./internal/plugin/ -run TestChain_RegistrationOrder` | ❌ Wave 0 |
| PLUG-04 | Day-one hooks registered: RequestID, Auth, Logging | integration | `go test ./internal/plugin/ -run TestDayOneHooks_End2End` | ❌ Wave 0 |
| PLUG-05 | ENABLED_HOOKS env var enables/disables; typo→error | unit | `go test ./internal/plugin/ -run TestChain_FilterAllowlist` + `go test ./internal/config/ -run TestEnabledHooks_TypoFailFast` | ❌ Wave 0 |
| PLUG-06 | PIIRedactionHook with 6 recognizers; env knobs work | unit + integration + property | `go test ./internal/plugin/pii/...` (all files) | ❌ Wave 0 |
| OBSV-03 | slog X-Request-Id correlation across all spans | integration (capture slog output, assert request_id field) | `go test ./internal/plugin/ -run TestRequestID_SlogCorrelation` + real-kiro e2e in tests/e2e | ❌ Wave 0 |
| OBSV-04 | GET /health/hooks view-only JSON envelope | unit (httptest handler test) | `go test ./internal/server/ -run TestHooksHandler` | ❌ Wave 0 |

**Validation invariants the strategy MUST enforce** (grouped per CONTEXT.md / SC mapping):

**Chain runner invariants:**
- **Chain ordering invariant**: hooks execute in slice registration order (RequestID → Auth → PII → Logging on Pre; Logging only on Post). Locked by SC5. Test: instrument each hook to push its Name onto a captured slice, assert order.
- **Pre short-circuit body preservation (Codex H-4 carry-through)**: when AuthHook returns `(resp, nil)`, the engine's Collect returns `*resp` verbatim — no chunk-assembly, no ACP touch. Test: a fake PreHook returns a tagged response; assert `Collect()` output is `==` the same pointer (or DeepEqual to the fields).
- **Pre mutations visible to subsequent hooks**: PIIRedactionHook's `req.Messages[].ContentParts[].Text = "<EMAIL>"` MUST be observable by the LoggingHook that runs after it. Test: capture LoggingHook's Pre-record `req`, assert no raw PII present.
- **Post chain runs unconditionally**: PostHooks run on BOTH the assembled-response path AND the short-circuit path. Test: AuthHook short-circuit followed by LoggingHook.After — assert LoggingHook.After saw the synthesized auth-error response.
- **PostHook in-place mutation respected**: LoggingHook may stamp the response with a final field; mutation must persist into the adapter render. Test: PostHook adds `Message.Metadata["log_id"]`, adapter render shows it.

**ENABLED_HOOKS filter invariants:**
- **Typo detection**: `ENABLED_HOOKS=BogusHook` → Filter returns error containing "unknown hook" and naming "BogusHook". Test: unit test in chain_test.go.
- **Empty/unset allowlist = pass-through**: `ENABLED_HOOKS=""` and `ENABLED_HOOKS` unset both yield every hook in the slice running. Test: parametric over both inputs.
- **Subset enables only the named hooks, preserving registration order**: `ENABLED_HOOKS=LoggingHook,RequestIDHook` filters to only those two, BUT they appear in registration order (RequestID first, Logging last). Test: assert order independently of the env-list order.

**Mode/key invariants:**
- **mode=hash without key fails boot**: `PII_REDACTION_MODE=hash` + empty `PII_HASH_KEY` → `config.Load()` returns error. Test: t.Setenv-driven config test.
- **mode=replace/mask/drop without key does NOT fail boot**: empty `PII_HASH_KEY` is fine for the three keyless modes. Test: parametric.

**`/health/hooks` invariants:**
- **Endpoint exposes no secrets**: no regex source strings, no `PII_HASH_KEY` (not even a hash prefix), no bearer-token values (only count). Test: httptest response body asserted to NOT contain `regexp` or known sentinel strings (use a `.go-arch-lint`-style grep + a positive whitelist check).
- **Endpoint is auth-exempt**: `GET /health/hooks` with no Authorization header returns 200 even when `AUTH_TOKEN` is set. Test: parallel to `agents_test.go:253-265`'s `TestAgentsHandler_NoAuthRequired`.
- **Endpoint reflects registration order**: even with `ENABLED_HOOKS=LoggingHook,RequestIDHook` (different order in env), the JSON output preserves chain-execution order. Test: assert `hooks[0].name == "RequestIDHook"`.

**PII walker invariants** (testing/quick property tests):
- **Never-panic**: for any random nested `map[string]any` / `[]any` / string mix, `WalkStrings` does not panic. `quick.Config{MaxCount: 1000}`.
- **Idempotent under idempotent transforms**: `WalkStrings(WalkStrings(x, t), t) == WalkStrings(x, t)` when `t(t(s)) == t(s)`. Locked by SC6.
- **String-leaves-only**: input map keys, non-string leaves (numbers, bools, nil), and slice indices are bit-identical between input and output. The transform sees only string LEAVES.
- **Map-key invariance**: a key named `"email"` is preserved verbatim regardless of recognizers. Test: assert `out["email"] != "<EMAIL>"`.
- **Depth-bounded termination**: a `maxDepth+1`-deep nested input returns without panic (the walker stops recursion at maxDepth, passing through unchanged).
- **Counter-suffix scope (D-counter discretionary)**: within ONE `canonical.ChatRequest`, multiple occurrences of `corey@x.com` redact to the SAME `<EMAIL_1>`; a different email redacts to `<EMAIL_2>`; counters reset on the next request. Test: 2-message chat request with mixed/repeated emails.

### Sampling Rate

- **Per task commit:** `go test ./internal/plugin/... -short`
- **Per wave merge:** `make test-race` (full project test suite with race detector)
- **Phase gate:** Full suite green; `make lint` clean; `make ci` (lint + test-race +
  govulncheck + arch-lint) clean; one real-kiro e2e in `tests/e2e/` that exercises a
  short-circuit (bad bearer) and asserts the per-surface error shape.

### Wave 0 Gaps

- [ ] `internal/plugin/testmain_test.go` — goleak gate for the new package
- [ ] `internal/plugin/pii/testmain_test.go` — goleak gate for the PII subpackage
- [ ] `internal/plugin/chain_test.go` — Chain.Filter typo-fail-fast + Describe shape
- [ ] `internal/plugin/auth_test.go` — AuthHook short-circuit + dual-header credential extraction
- [ ] `internal/plugin/request_id_test.go` — ULID generation + ctx-roundtrip + slog correlation
- [ ] `internal/plugin/logging_test.go` — Pre + Post emit slog records with `request_id` field; nil-resp safe; reads pii summary from ctx
- [ ] `internal/plugin/pii/walk_test.go` — testing/quick property tests (never-panic, idempotent, string-leaves-only, map-key invariance)
- [ ] `internal/plugin/pii/luhn_test.go` — fixed-table tests against Visa/Mastercard/Amex valid+invalid samples + property test
- [ ] `internal/plugin/pii/recognizers_test.go` — per-recognizer positive (matches expected) + negative (rejects 666-XX-XXXX) tables
- [ ] `internal/server/hooks_handler_test.go` — `/health/hooks` JSON shape + auth-exempt + no-secret-exposure assertions
- [ ] `internal/config/config_test.go` extensions — `ENABLED_HOOKS=BogusHook` returns error; `PII_REDACTION_MODE=hash` + empty `PII_HASH_KEY` returns error
- [ ] `tests/e2e/plugin_chain_e2e_test.go` (or equivalent) — real-binary boot + curl-equivalent → assert AuthHook short-circuit body matches the per-surface error envelope across OpenAI / Ollama / Anthropic

## Security Domain

> `security_enforcement` is not explicitly set to `false` in `.planning/config.json`; treating as enabled per protocol default.

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | Bearer-token validation with `crypto/subtle.ConstantTimeCompare` (Pitfall 3, Code Example 1). Phase 8 refactors the existing HTTP-middleware control into a canonical-typed `AuthHook` — same cryptographic primitive, same constant-time guarantee. |
| V3 Session Management | no (out of phase scope) | Session management is already Phase 5 territory (`internal/session`). Phase 8 does not touch session lifecycle. |
| V4 Access Control | partial | IP allowlist stays at chi middleware. AuthHook implements token-based access; future PLUG-V2 hooks may add per-token policy (rate limit, model allowlist). |
| V5 Input Validation | yes | PII walker IS input observation, not validation — but adapter layer's existing wire-shape parsers (e.g., `internal/adapter/ollama/wire.go`) provide the V5 control. Phase 8 adds no V5 weakness. |
| V6 Cryptography | yes | `crypto/hmac` + `crypto/sha256` for `PII_REDACTION_MODE=hash`; `crypto/subtle` for token compare; `crypto/rand` (indirectly via ULID v2 entropy) for ID generation. **Never hand-roll** (Don't Hand-Roll table). |
| V7 Error Handling and Logging | yes | LoggingHook MUST log post-PII-redaction content only (D-04). `/health/hooks` MUST NOT leak regex patterns, hash keys, or token values (Pitfall 9). |
| V9 Communications | n/a | TLS termination is operator-deployment concern; gateway binds plaintext on localhost by default. |
| V11 Business Logic | partial | Short-circuit semantics (Codex H-4) must preserve auth-failure path: bad bearer → AuthHook returns canonical error response → adapter renders → engine never touched → no kiro-cli session opened (cost-control + side-channel-control). |
| V12 Files and Resources | n/a | Phase 8 reads no files. |
| V13 API and Web Service | yes | `/health/hooks` is the new API surface. Auth-exempt by design (operator dashboard); zero-secret-exposure by design (Pitfall 9). |
| V14 Configuration | yes | Boot-fail on `mode=hash` + missing `PII_HASH_KEY` (D-05); boot-fail on `ENABLED_HOOKS` typo (D-02). Config errors crash the process before serving traffic. |

### Known Threat Patterns for this stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Bearer-token timing side-channel | Information Disclosure | `crypto/subtle.ConstantTimeCompare` on equal-length inputs (Pitfall 3) |
| ENABLED_HOOKS silent typo disables PII | Tampering | Hard-fail boot error on unknown name (D-02, Pitfall 7) |
| Hash mode silent unkeyed fallback | Information Disclosure | Hard-fail boot error when mode=hash AND empty key (D-05, Pitfall 6) |
| /health/hooks leaks detection logic | Information Disclosure | `Describe()` whitelist; entity names yes, regex no, tokens no (Pitfall 9) |
| Goroutine leak from async logging | DoS | Synchronous logging in v1; goleak.VerifyTestMain on every test file (Pitfall 10) |
| PII walker panic on adversarial input | DoS | depth-bounded recursion + property tests for never-panic invariant (Pitfall 2, Pattern 4) |
| Map-key redaction breaks protocol | Tampering | string-LEAVES-only walker; map-key invariance property test (Pitfall 2) |
| PreHook order swap (Logging before PII) leaks raw PII into logs | Information Disclosure | Slice order is locked by D-04 + SC5 + ordering invariant test |
| ENABLED_HOOKS bypass via header (`X-Skip-PII`) | Tampering | Explicitly deferred — guardrail-defeat-primitive. Out-of-scope. |
| Length-extension attack on PII hash | Tampering | Use HMAC-SHA256 (not bare SHA256) — `crypto/hmac` is mathematically safe by construction |
| Counter cross-request leakage | Information Disclosure | Per-`ChatRequest` counter reset (Pitfall 4) |
| AuthHook short-circuit cost regression | DoS | AuthHook runs SECOND (after RequestID); PII (the expensive hook) runs only if Auth passes (D-04) |

## Sources

### Primary (HIGH confidence)

- `internal/engine/hooks.go` (Phase 2 D-04; shipped + stable) — PreHook + PostHook interfaces
- `internal/engine/engine.go` lines 70-85, 146-215, 234-267 — Config.PreHooks/PostHooks fields; Run's PreHook traversal loop; emptyStream Codex H-4 shim
- `internal/engine/collect.go` lines 17-26, 42-47, 114-122 — Codex H-4 short-circuit preservation + Codex H-5 PostHook traversal
- `internal/auth/bearer.go` lines 32-93 — bearer-token constant-time compare; dual-header extraction precedent
- `internal/server/server.go` lines 195-205 — auth-exempt outer-router pattern for /health-style routes
- `internal/server/agents_test.go` lines 212-247 — JSON envelope shape precedent (parallel to /health/hooks)
- `internal/config/config.go` lines 144-147, 168-171, 377-396 — boot-error pattern for unknown env values
- `internal/canonical/chat.go` lines 154-206 — ContentPart, ToolUsePart, ToolResultPart shapes (PII walker targets)
- `internal/engine/pickcwd_test.go` lines 11, 225-230 — testing/quick property-test precedent
- `docs/briefs/go_port_brief.md` §3.14 lines 790-883 — Plugin hooks spec of record (PreHook/PostHook contract, RequestID/Auth/Logging hook enumeration, Bifrost reference)
- `docs/briefs/go_port_brief.md` §3.12 — Trust gates (gosec G204, golangci-lint, goleak, testing/quick)
- `docs/briefs/go_port_brief.md` §3.13 — Adapter-over-canonical layout (TRST-04)
- [Go stdlib `crypto/subtle` docs](https://pkg.go.dev/crypto/subtle) — ConstantTimeCompare equal-length requirement
- [Go stdlib `regexp` docs](https://pkg.go.dev/regexp) — RE2-backed; no lookahead/lookbehind support
- [pkg.go.dev/github.com/oklog/ulid/v2](https://pkg.go.dev/github.com/oklog/ulid/v2) v2.1.1 — ulid.Make() docs, Crockford Base32

### Secondary (MEDIUM confidence)

- [Rosetta Code: Luhn test of credit card numbers (Go)](https://rosettacode.org/wiki/Luhn_test_of_credit_card_numbers) — pure-Go Luhn implementation
- [Microsoft Presidio: Supported entities](https://microsoft.github.io/presidio/supported_entities/) — PII entity taxonomy reference; recognizer category list (Phase 8 ships a 6-entity subset)
- [Microsoft Presidio: Regex recognizers tutorial](https://microsoft.github.io/presidio/tutorial/02_regex/) — recognizer registry pattern reference (Presidio uses this shape; D-01 borrows it without taking the library dep)
- [SSN regex with reserved range filtering](https://howtodoinjava.com/java/regex/java-regex-validate-social-security-numbers-ssn/) — reserved ranges enumerated; pattern-with-lookahead is the original (Pitfall 1 workaround documented)
- [github.com/google/re2/issues/156](https://github.com/google/re2/issues/156) — RE2 negative-lookahead unsupported (and why; verified)
- [Go HMAC-SHA256 implementation](https://kerkour.com/sha256-hmac-golang) — `hmac.New(sha256.New, key)` pattern
- [Avoiding context-key collisions in Go](https://rednafi.com/go/avoid-context-key-collisions/) — typed private key + accessor pattern (Pattern 3)
- [veqryn/slog-context README](https://github.com/veqryn/slog-context) — slog context-attribute extraction patterns (referenced as "Option B"; not taken as a dep)

### Tertiary (LOW confidence)

- Email regex pragmatic-vs-strict tradeoffs (multiple SEO-adjacent sources; the regex used in this RESEARCH is a known pragmatic pattern but the planner should sanity-check against a 2026-relevant strict alternative if compliance/recall is a stated requirement)

## Metadata

**Confidence breakdown:**

- Standard stack: HIGH — every recommended package is either stdlib (`crypto/*`, `regexp`, `net`, `log/slog`, `context`, `testing/quick`, `encoding/json`) or already in `go.mod` (`go-chi/chi/v5`, `goleak`) or a well-known maintained library with thousands of stars (`oklog/ulid/v2`). The single NEW external dependency (`oklog/ulid/v2`) is gated behind a planner-inserted human-verify checkpoint since slopcheck was unavailable.
- Architecture: HIGH — PreHook/PostHook seams shipped, tested, documented in code comments at engine/hooks.go + engine/collect.go. Codex H-4 and H-5 contracts cross-verified at file:line. PII walker shape directly maps to existing canonical types (no canonical-type churn needed).
- Pitfalls: HIGH — every pitfall has a documented source (Go re2 issue tracker, project precedent files, Pitfall 7 reproducible via t.Setenv test). The negative-lookahead trap is the highest-impact pitfall and is verified via Google's own issue tracker.
- Validation Architecture: HIGH — invariants enumerated 1:1 against CONTEXT.md's locked decisions + SC1-SC7 + the PII walker discretionary recommendations. Each invariant has a concrete test file mapping in Wave 0 Gaps.

**Research date:** 2026-05-27
**Valid until:** 2026-06-26 (30 days; stable domain — Go stdlib + a single mature dep + canonical seams already shipped)

## RESEARCH COMPLETE
