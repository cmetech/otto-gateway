# Phase 8: Plugin Hook Chain - Pattern Map

**Mapped:** 2026-05-27
**Files analyzed:** 13 source files (new + modified) + 11 test files
**Analogs found:** 23 / 24 (one "no analog" — see end)

> Repo conventions (verified across analogs):
> - Package-doc godoc-style banner at top of every `.go` file referencing
>   the originating phase/plan/decision (e.g., `// Package server …`,
>   `// Phase 2 D-04 …`).
> - Consumer-defined interfaces (server-side) — `PoolStatsSource`,
>   `RegistryStatsSource`, `PoolDetailSource` — declared NEXT TO the
>   handler that uses them; structural satisfaction via a thin adapter
>   in `cmd/otto-gateway/main.go`.
> - Boot-validation pattern in `internal/config/config.go`:
>   `errs = append(errs, fmt.Errorf("KEY: %w", err))` then
>   `errors.Join(errs...)` at the end (lines 124, 144-147, 199-201).
> - Every test file in a package with goroutines has a
>   `testmain_test.go` that calls `goleak.VerifyTestMain(m)`.
> - Property tests use stdlib `testing/quick` with `quick.Check(prop,
>   &quick.Config{MaxCount: 1000})`.
> - `errors.Is/As` plus `fmt.Errorf("…: %w", err)` for wrapping;
>   `wrapcheck` is enforced by `.golangci.yml`.
> - E2E tests live under `tests/e2e/` with `//go:build e2e` plus a
>   runtime `OTTO_E2E=1` env gate.

---

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `internal/plugin/chain.go` (new) | service / type-decl | in-memory transform (Filter, Describe) | `internal/server/agents.go` (typed wire-shape + consumer-defined interface) + `internal/auth/auth.go` (Config-as-value pattern) | role-match |
| `internal/plugin/request_id.go` (new) | hook / interface impl | request-response + ctx propagation | `internal/server/middleware.go:14-46` (private struct ctx key + `LoggerFromCtx` accessor) | role-match |
| `internal/plugin/auth.go` (new) | hook / interface impl | request-response, short-circuit-capable | `internal/auth/bearer.go:32-60` (subtle.ConstantTimeCompare loop) | role-match (refactor target) |
| `internal/plugin/logging.go` (new) | hook / interface impl | observability, Pre+Post timing | `internal/server/middleware.go:22-46` (`accessLog` Start/Now/Since pattern + slog.With(request_id)) | role-match |
| `internal/plugin/pii/pii.go` (new) | hook / interface impl | in-place canonical mutation | `internal/engine/collect.go:115-122` (Codex H-5 in-place mutation discipline) | role-match |
| `internal/plugin/pii/recognizers.go` (new) | data / registry literal | package-init compile + slice iteration | (none in repo — pure literal data) | partial-match (use `regexp.MustCompile` per stdlib idiom) |
| `internal/plugin/pii/walk.go` (new) | utility / recursive helper | recursion over canonical content | `internal/engine/pickcwd.go` (pure helper invoked by engine) | partial-match (recursion shape is novel) |
| `internal/plugin/pii/luhn.go` (new) | utility / post-validator | pure function | (none — stdlib idiom) | no-analog |
| `internal/server/hooks_handler.go` (new) | http handler | request-response, JSON envelope | `internal/server/agents.go` (entire file is the template) | exact |
| `internal/server/server.go` (modify) | http wiring | route registration | `internal/server/server.go:196-200` (`/health/agents` exact precedent) | exact |
| `internal/config/config.go` (modify) | config loader | env → Config struct | `internal/config/config.go:141-147, 168-171` (`getEnvStrSliceComma` + `validateEnabledSurfaces` typo-fail-fast pattern) | exact |
| `cmd/otto-gateway/main.go` (modify) | wiring | dependency assembly | `cmd/otto-gateway/main.go:404-418` (`server.NewFromConfig` block) | exact |
| `.go-arch-lint.yml` (modify) | static analysis config | declarative boundary list | `.go-arch-lint.yml:46-126` (per-component `in:` + `mayDependOn:` lists) | exact |
| `internal/plugin/chain_test.go` (new) | unit test | table-driven | `internal/auth/auth_test.go:32-96` (table-driven + named subtests) | role-match |
| `internal/plugin/request_id_test.go` (new) | unit test | header in/out + ctx accessor | `internal/auth/auth_test.go:46-74` (httptest + decode body) | role-match |
| `internal/plugin/auth_test.go` (new) | unit test | short-circuit response shape | `internal/auth/auth_test.go:57-74` (invalid token rejects) | exact |
| `internal/plugin/logging_test.go` (new) | unit test | slog record capture | `internal/server/agents_test.go:42-70` (handler test + decode JSON) | role-match |
| `internal/plugin/pii/walk_test.go` (new) | property test | `testing/quick` quick.Check | `internal/engine/pickcwd_test.go:208-240` (TestPickCwd_NeverPanics, the project's property-test precedent) | exact |
| `internal/plugin/pii/recognizers_test.go` (new) | unit test | table-driven positive/negative | `internal/auth/auth_test.go` table layout | role-match |
| `internal/plugin/pii/modes_test.go` (new) | unit test | table-driven mode dispatch | `internal/config/config_test.go:75-104` (table + subtests + t.Setenv) | role-match |
| `internal/config/plugin_config_test.go` (new) | unit test | t.Setenv + Load() + boot-error | `internal/config/config_test.go:41-73` (TestLoadEnvOverrides) | exact |
| `internal/server/hooks_handler_test.go` (new) | unit test | httptest + JSON decode | `internal/server/agents_test.go:1-118` | exact |
| `tests/e2e/plugin_chain_test.go` (new) | e2e test | real binary + curl-shape | `tests/e2e/e2e_test.go:1-258` (bootGateway helper) | exact |

---

## Pattern Assignments

### `internal/plugin/chain.go` (service / typed slice with helpers)

**Analog:** `internal/server/agents.go` for the consumer-defined-interface pattern; `internal/auth/auth.go` for the value-Config pattern.

**Package-doc + imports pattern** (`internal/auth/auth.go:1-19`):
```go
// Package plugin provides the day-one Pre/Post hooks for OTTO Gateway:
// RequestIDHook, AuthHook, LoggingHook, and PIIRedactionHook (subpackage pii).
//
// Per Phase 8 D-01: there is no Register(name, factory) registry — the
// chain is a hardcoded literal slice in cmd/otto-gateway/main.go. This
// package's Chain type bundles []engine.PreHook + []engine.PostHook +
// Filter + Describe so the wiring + introspection paths share one
// type (D-02 / OBSV-04).
package plugin

import (
    "log/slog"

    "otto-gateway/internal/engine"
)
```

**Filter / typo-fail-fast pattern** (mirrors `internal/config/config.go:377-396 validateEnabledSurfaces`):
```go
// Filter returns a Chain containing only the hooks whose type name appears
// in allowlist. An empty allowlist returns chain unchanged (default-permissive,
// matches AUTH_TOKEN semantics). A name in allowlist that does NOT match any
// hook in the chain is a boot error (Phase 8 D-02 typo protection).
func (c Chain) Filter(allowlist []string) (Chain, error) {
    if len(allowlist) == 0 {
        return c, nil
    }
    allow := make(map[string]struct{}, len(allowlist))
    for _, n := range allowlist {
        allow[n] = struct{}{}
    }
    // ... build filtered chain; track which allowlist names matched ...
    var unknown []error
    for name := range allow {
        if !matched[name] {
            unknown = append(unknown, fmt.Errorf("unknown hook in ENABLED_HOOKS: %q", name))
        }
    }
    if len(unknown) > 0 {
        return Chain{}, errors.Join(unknown...)
    }
    return filtered, nil
}
```

**HookDescription + Describe() interface** (mirrors `server.AgentsResponse` JSON-tagged wire-shape at `internal/server/agents.go:14-17`):
```go
// HookDescription is the GET /health/hooks per-hook wire row (OBSV-04).
// JSON tags are the load-bearing wire contract.
type HookDescription struct {
    Name    string         `json:"name"`
    Kind    string         `json:"kind"`    // "Pre" | "Post" | "Pre,Post"
    Enabled bool           `json:"enabled"` // reflects ENABLED_HOOKS allowlist
    Config  map[string]any `json:"config"`  // safe-to-publish only
}

// Describer is the consumer-defined interface each hook implements to
// publish its non-secret config for /health/hooks. Hooks declare what
// they consider safe to publish (CONTEXT.md Claude's Discretion).
type Describer interface {
    Describe() (kind string, config map[string]any)
}
```

---

### `internal/plugin/request_id.go` (Pre + Post hook, ctx propagation)

**Analog:** `internal/server/middleware.go:12-55` (private struct key + WithLogger / LoggerFromCtx accessor pair).

**Typed-key ctx pattern** (`internal/server/middleware.go:13-14, 26-29, 50-55`):
```go
// loggerKey is the unexported context key for the per-request logger.
// Using a private struct type prevents key collisions with other packages.
type loggerKey struct{}

// (stash)
ctx := context.WithValue(r.Context(), loggerKey{}, reqLogger)

// LoggerFromCtx retrieves the per-request logger stored by accessLog.
// Falls back to fallback if no logger is present in the context.
func LoggerFromCtx(ctx context.Context, fallback *slog.Logger) *slog.Logger {
    if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
        return l
    }
    return fallback
}
```

**Hook signature** (`internal/engine/hooks.go:30-32`):
```go
type PreHook interface {
    Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
}
```

**RequestIDHook.Before skeleton** (apply pattern above to the engine seam):
```go
// requestIDKey is private; expose via RequestIDFromContext accessor only.
type requestIDKey struct{}

func (h *RequestIDHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
    id := h.headerLookup(ctx) // if HTTP middleware already set X-Request-Id, honor it
    if id == "" {
        id = ulid.Make().String() // ULID per RESEARCH.md
    }
    // Note: ctx mutation here only affects later PreHooks reading via accessor.
    // The actual ctx is propagated by engine.Run iteration — see internal/engine/engine.go:152-162.
    return nil, nil
}

func RequestIDFromContext(ctx context.Context) string {
    if v, ok := ctx.Value(requestIDKey{}).(string); ok {
        return v
    }
    return ""
}
```

---

### `internal/plugin/auth.go` (Pre hook, short-circuit-capable)

**Analog:** `internal/auth/bearer.go:32-60` — the refactor target. Phase 8 lifts the constant-time-compare loop out of HTTP middleware into a canonical-typed PreHook.

**Constant-time compare loop pattern** (`internal/auth/bearer.go:46-55`):
```go
providedBytes := []byte(provided)
for _, valid := range cfg.Tokens {
    // Note: this loop is NOT constant-time across the token list
    // (early exit on match leaks "how many tokens are configured",
    // not token bytes). Acceptable per RESEARCH.md Pattern 3.
    if subtle.ConstantTimeCompare(providedBytes, []byte(valid)) == 1 {
        next.ServeHTTP(w, r)
        return
    }
}
```

**Empty-tokens passthrough pattern** (`internal/auth/bearer.go:35-38`):
```go
if len(cfg.Tokens) == 0 {
    next.ServeHTTP(w, r)
    return
}
```

**AuthHook short-circuit pattern** (the canonical-typed translation):
```go
func (h *AuthHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
    if len(h.Tokens) == 0 {
        return nil, nil // auth disabled — Node parity
    }
    // Token must travel via ctx (set by HTTP middleware before engine entry)
    // because canonical.ChatRequest has no auth field by design (TRST-04).
    provided := tokenFromContext(ctx)
    if provided == "" {
        return errorResponse("authentication_error", "Invalid or missing API key"), nil
    }
    providedBytes := []byte(provided)
    for _, valid := range h.Tokens {
        if subtle.ConstantTimeCompare(providedBytes, []byte(valid)) == 1 {
            return nil, nil
        }
    }
    return errorResponse("authentication_error", "Invalid or missing API key"), nil
}
```

**Anthropic short-circuit envelope precedent** (`internal/adapter/anthropic/errors.go:43-71`) — the surface adapter renders the canonical error response in its native shape per CONTEXT.md Claude's Discretion. AuthHook returns canonical shape only.

---

### `internal/plugin/logging.go` (Pre + Post hook, slog timing)

**Analog:** `internal/server/middleware.go:22-46` — `accessLog` is the timing-pattern blueprint.

**Timing pattern** (`internal/server/middleware.go:31, 37-43`):
```go
start := time.Now()
// ... handler runs ...
reqLogger.Info(
    "request",
    "method", r.Method,
    "path", r.URL.Path,
    "status", ww.Status(),
    "duration_ms", time.Since(start).Milliseconds(),
)
```

**slog.With(request_id) correlation pattern** (`internal/server/middleware.go:25-26`):
```go
reqID := middleware.GetReqID(r.Context())
reqLogger := logger.With("request_id", reqID)
```

**LoggingHook structure** (Pre records start time on ctx; Post computes duration):
```go
type loggingStartKey struct{}

func (h *LoggingHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
    reqID := RequestIDFromContext(ctx) // from request_id.go accessor
    h.Logger.With("request_id", reqID).Info("plugin.before",
        "model", req.Model,
        "message_count", len(req.Messages),
    )
    // start time travels on ctx so After can compute duration
    return nil, nil
}

func (h *LoggingHook) After(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
    start, _ := ctx.Value(loggingStartKey{}).(time.Time)
    reqID := RequestIDFromContext(ctx)
    h.Logger.With("request_id", reqID).Info("plugin.after",
        "duration_ms", time.Since(start).Milliseconds(),
        "stop_reason", resp.StopReason,
        // Optional D-04: "redacted", pii.SummaryFromContext(ctx),
    )
    return nil
}
```

---

### `internal/plugin/pii/pii.go` (Pre hook, in-place canonical mutation)

**Analog:** `internal/engine/collect.go:115-122` (Codex H-5 in-place mutation discipline).

**In-place mutation pattern + non-nil-error abort** (`internal/engine/collect.go:118-122`):
```go
// Codex H-5: PostHook traversal happens HERE in Collect (not in
// Run) so the hooks see the assembled or short-circuit response.
// In-place mutation is allowed (resp is a pointer to the struct);
// non-nil error aborts the collect.
for _, h := range e.cfg.PostHooks {
    if hookErr := h.After(ctx, req, resp); hookErr != nil {
        return nil, fmt.Errorf("engine: posthook: %w", hookErr)
    }
}
```

**PIIRedactionHook.Before skeleton** (in-place mutation on `req.Messages`):
```go
func (h *PIIRedactionHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
    if !h.Enabled {
        return nil, nil // ENABLED_HOOKS keeps the hook in the chain but inert
    }
    summary := newSummary()
    for i := range req.Messages {
        // Walk Content (legacy single-string field) — D-03 item 4
        req.Messages[i].Content = applyRecognizers(req.Messages[i].Content, h.Recognizers, summary)
        // Walk ContentParts[] — D-03 items 1-3
        for j := range req.Messages[i].ContentParts {
            // dispatch on ContentKind: Text vs ToolUse vs ToolResult
            // ... mutate Text in place; recurse into ToolUse.Input via walk.go ...
        }
    }
    ctx = withSummary(ctx, summary) // D-04 API seam — must exist even if LoggingHook defers emitting
    return nil, nil
}
```

---

### `internal/plugin/pii/recognizers.go` (data literal)

**Analog:** D-01's pattern at the per-recognizer level. No exact code analog — closest is the
`validateEnabledSurfaces` allowed-map literal (`internal/config/config.go:381-385`).

**Package-init regex compile pattern** (stdlib idiom):
```go
package pii

import "regexp"

// Recognizer is the registry entry the PII walker iterates per CONTEXT.md
// Claude's Discretion. Validate is an optional post-regex check.
type Recognizer struct {
    Name     string
    Pattern  *regexp.Regexp
    Validate func(match string) bool // nil = accept all regex hits
}

// Compiled at package init — zero per-request compile cost.
var (
    emailRe = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
    ipv4Re  = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
    // RE2 has NO lookahead — see RESEARCH.md Common Pitfalls for SSN.
    // ...
)

var Recognizers = []Recognizer{
    {Name: "Email", Pattern: emailRe, Validate: nil},
    {Name: "IPv4",  Pattern: ipv4Re,  Validate: validateIPv4Octets},
    // ...
}
```

---

### `internal/plugin/pii/walk.go` (recursive utility)

**Analog:** No direct repo precedent for recursion. Use stdlib idiom — pure-Go `any` walker.

**Walker signature pattern** (the single helper per D-03):
```go
// WalkStringLeaves visits every string-typed leaf in v (a map[string]any
// or []any from canonical.ToolUsePart.Input / ToolResultPart.Content),
// applies transform, and writes back in place. Map KEYS are not visited
// (they are field names, not user content — D-03). Non-string leaves
// (numbers, bools, nil) pass through unchanged.
func WalkStringLeaves(v any, transform func(string) string) any {
    switch x := v.(type) {
    case string:
        return transform(x)
    case map[string]any:
        for k, child := range x {
            x[k] = WalkStringLeaves(child, transform) // keys NOT walked
        }
        return x
    case []any:
        for i, child := range x {
            x[i] = WalkStringLeaves(child, transform)
        }
        return x
    default:
        return x // numbers, bools, nil — D-03 string-leaves-only invariant
    }
}
```

---

### `internal/plugin/pii/luhn.go` (post-validator)

**Analog:** None in repo — Luhn is a stdlib-shaped pure helper. Use the same doc-comment density
as `internal/auth/bearer.go:62-94` (`extractToken`'s extensive Why+How comment).

```go
// LuhnValid returns true when s passes the Luhn checksum (mod-10).
// Used as Recognizer.Validate for the credit-card recognizer — the regex
// (cardRe) matches 13-19 digit sequences; LuhnValid rejects sequences
// that fail the checksum. Strips non-digits (spaces, dashes) before
// computing so "4111 1111 1111 1111" and "4111-1111-1111-1111" produce
// the same result as "4111111111111111".
func LuhnValid(s string) bool { /* ... */ }
```

---

### `internal/server/hooks_handler.go` (HTTP handler, JSON envelope)

**Analog:** `internal/server/agents.go` — the entire file is the template.

**Wire-shape + nil-safety + JSON encode pattern** (`internal/server/agents.go:14-17, 67-107`):
```go
// HooksResponse is the body returned by GET /health/hooks (OBSV-04).
// Shape is locked by Phase 8 D-01 + Claude's Discretion; introspection
// consumers depend on the snake_case-equivalent wire contract.
type HooksResponse struct {
    Pre  []plugin.HookDescription `json:"pre"`
    Post []plugin.HookDescription `json:"post"`
}

// HooksDescriptionSource is the consumer-defined interface hooksHandler
// uses to fetch the registered chain without importing internal/plugin
// into server's public surface. The cmd/otto-gateway hooksDescriptionAdapter
// wraps plugin.Chain to satisfy this interface; a nil source is handled
// (renders empty shape).
type HooksDescriptionSource interface {
    Describe() (pre, post []HookDescription)
}

func (s *Server) hooksHandler(w http.ResponseWriter, r *http.Request) {
    resp := HooksResponse{}
    if s.hooks != nil {
        resp.Pre, resp.Post = s.hooks.Describe()
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    if err := json.NewEncoder(w).Encode(resp); err != nil {
        LoggerFromCtx(r.Context(), s.logger).Error("hooks encode", "err", err)
    }
}
```

> Note: `server.HookDescription` may need to be redeclared structurally in `server/` (not imported from `plugin/`) to keep `server` from importing `internal/plugin` — same pattern as `AgentSlot` re-declaration at `agents.go:40-45`. The `cmd/otto-gateway` adapter does the field-copy. Verify `.go-arch-lint.yml` `server.mayDependOn:` does NOT include `plugin`.

---

### `internal/server/server.go` (modify — register `/health/hooks`)

**Analog:** `internal/server/server.go:196-200` (`/health/agents` exact precedent).

**Auth-exempt outer-router registration** (`internal/server/server.go:196-200`):
```go
s.router.Get("/", s.rootHandler)
s.router.Get("/health", s.healthHandler)
// D-18: /health/agents is auth-exempt, registered on the OUTER router
// alongside /health. The detail endpoint exposes full session ids
// verbatim (D-17) for operator dashboards.
s.router.Get("/health/agents", s.agentsHandler)
```

**Phase 8 addition (one line, directly after line 200)**:
```go
// Phase 8 OBSV-04: /health/hooks is auth-exempt, registered on the OUTER
// router alongside /health and /health/agents. View-only — no runtime
// mutate path (PROJECT.md line 108; SC7). Restart to change config.
s.router.Get("/health/hooks", s.hooksHandler)
```

**Config struct field addition** (mirrors `internal/server/server.go:91-105`):
```go
type Config struct {
    // ... existing fields ...
    Hooks HooksDescriptionSource // Phase 8 OBSV-04; may be nil (empty render)
}
```

**Server struct field addition** (mirrors `server.go:108-119`):
```go
type Server struct {
    // ... existing fields ...
    hooks HooksDescriptionSource
}
```

**NewFromConfig assignment** (mirrors `server.go:180-188`):
```go
s := &Server{
    // ... existing fields ...
    hooks: cfg.Hooks,
}
```

---

### `internal/config/config.go` (modify — add 5 env keys)

**Analog:** `internal/config/config.go:141-147, 168-171` (typo-fail-fast + boot-error accumulation).

**Env-key field additions** (mirrors `config.go:79-90` `EnabledSurfaces` doc style):
```go
// EnabledHooks is the comma-split allowlist of hook type names enabled at
// boot (Phase 8 D-02). Default empty = all hooks in the chain enabled
// (matches AUTH_TOKEN semantics). A name not present in main.go's chain
// causes Load() to return an error (typo-fail-fast — PII redaction silently
// disabled by ENABLED_HOOKS=PIIRedaction (missing Hook suffix) is the
// load-bearing case). Loaded from ENABLED_HOOKS.
EnabledHooks []string

// PIIRedactionEnabled controls whether PIIRedactionHook does work when
// invoked (Phase 8 D-02). Composes with EnabledHooks: ENABLED_HOOKS
// controls whether the hook IS in the chain; PII_REDACTION_ENABLED
// controls whether the hook DOES work when invoked. Default false
// (operator must opt in to PII scrubbing).
PIIRedactionEnabled bool

// PIIEnabledEntities is the comma-split list of recognizer Names that
// PIIRedactionHook applies. Empty = all six recognizers active. Unknown
// names → boot error (typo-fail-fast). Loaded from PII_ENABLED_ENTITIES.
PIIEnabledEntities []string

// PIIRedactionMode is "replace" | "mask" | "hash" | "drop" (Phase 8 D-05).
// Default "replace". mode=hash with empty PIIHashKey → boot error.
// Loaded from PII_REDACTION_MODE.
PIIRedactionMode string

// PIIHashKey is the HMAC-SHA256 key for PII_REDACTION_MODE=hash (D-05).
// Required when Mode=="hash"; otherwise unused. Loaded from PII_HASH_KEY.
// Rotating this key invalidates prior correlation tokens (intentional —
// key-rotation tool for suspected log leak; see docs/operating.md).
PIIHashKey string
```

**Load() body addition** (mirrors `config.go:141-147, 168-171`):
```go
enabledHooks := getEnvStrSliceComma("ENABLED_HOOKS", nil)
piiEnabled, err := getEnvBool("PII_REDACTION_ENABLED", false)
if err != nil {
    errs = append(errs, err)
}
piiEntities := getEnvStrSliceComma("PII_ENABLED_ENTITIES", nil)
if err := validatePIIEntities(piiEntities); err != nil {
    errs = append(errs, fmt.Errorf("PII_ENABLED_ENTITIES: %w", err))
}
piiMode := getEnvStr("PII_REDACTION_MODE", "replace")
if err := validatePIIMode(piiMode); err != nil {
    errs = append(errs, fmt.Errorf("PII_REDACTION_MODE: %w", err))
}
piiHashKey := getEnvStr("PII_HASH_KEY", "")
// D-05 boot validation: mode=hash with empty key → refuse to start.
if piiMode == "hash" && piiHashKey == "" {
    errs = append(errs, fmt.Errorf("PII_REDACTION_MODE=hash requires PII_HASH_KEY"))
}
```

**`validatePIIMode` / `validatePIIEntities` helpers** (mirror `config.go:377-396`):
```go
// validatePIIMode rejects unknown PII_REDACTION_MODE values fail-fast
// with a clear error naming valid modes (Phase 8 D-05 typo protection).
func validatePIIMode(m string) error {
    allowed := map[string]struct{}{"replace": {}, "mask": {}, "hash": {}, "drop": {}}
    if _, ok := allowed[m]; !ok {
        return fmt.Errorf("unknown mode %q (allowed: replace, mask, hash, drop)", m)
    }
    return nil
}
```

> Note: `ENABLED_HOOKS` typo validation belongs in `chain.Filter` (it needs the runtime
> chain to know what's valid). Config Load() only validates SHAPE; the hook-name validation
> happens at `chain.Filter(cfg.EnabledHooks)` in `main.go`, which propagates the error to
> `newApp` and aborts startup (mirrors `pool.Warmup` failure handling at
> `cmd/otto-gateway/main.go:171-174`).

---

### `cmd/otto-gateway/main.go` (modify — construct + filter + inject chain)

**Analog:** `cmd/otto-gateway/main.go:176-180` (engine.New) + lines 404-418 (server.NewFromConfig).

**Chain construction site** (insert AFTER `a.pool` warmup, BEFORE `engine.New` at line 176):
```go
// Phase 8 D-01: hardcoded chain literal — one source of truth for what
// hooks run. Adding a new hook is one line here. No registry indirection.
chain := plugin.Chain{
    Pre: []engine.PreHook{
        &plugin.RequestIDHook{Logger: logger},
        &plugin.AuthHook{Tokens: cfg.AuthToken},
        &pii.PIIRedactionHook{
            Recognizers:     filterRecognizers(pii.Recognizers, cfg.PIIEnabledEntities),
            Enabled:         cfg.PIIRedactionEnabled,
            Mode:            cfg.PIIRedactionMode,
            HashKey:         []byte(cfg.PIIHashKey),
        },
        &plugin.LoggingHook{Logger: logger},
    },
    Post: []engine.PostHook{
        &plugin.LoggingHook{Logger: logger}, // shared instance; After reads start from ctx
    },
}
// D-02 typo-fail-fast — Filter validates names against the constructed chain.
chain, err = chain.Filter(cfg.EnabledHooks)
if err != nil {
    cleanup()
    return nil, func() {}, fmt.Errorf("chain filter: %w", err)
}
```

**Engine.New change** (modify `main.go:176-180`):
```go
a.engine = engine.New(engine.Config{
    Logger:     logger,
    ACP:        a.pool,
    DefaultCWD: cfg.KiroCWD,
    PreHooks:   chain.Pre,  // Phase 8 — was nil in Phase 2
    PostHooks:  chain.Post, // Phase 8 — was nil in Phase 2
})
// Plus the same Pre/Post on every per-session engine constructed at lines 233-253.
```

**Bearer middleware removal** (per RESEARCH.md "Important note on Auth"):
The `auth.Bearer(...)` middleware call in `internal/server/server.go:230` MUST be removed
once AuthHook fully owns the canonical-layer auth path. `auth.IPAllowlist` stays. Plan this
removal carefully — see Shared Patterns / Migration below.

**`server.NewFromConfig` Config addition** (mirrors `main.go:404-418` — add one field):
```go
a.srv = server.NewFromConfig(server.Config{
    // ... existing fields unchanged ...
    Hooks: hooksDescriptionAdapter{chain: chain}, // Phase 8 OBSV-04
})
```

**hooksDescriptionAdapter pattern** (mirrors `poolStatsAdapter` at `main.go:428-435`):
```go
// hooksDescriptionAdapter wraps plugin.Chain to satisfy
// server.HooksDescriptionSource without importing internal/plugin into
// server. Same one-line bridge pattern as poolStatsAdapter / poolDetailAdapter.
type hooksDescriptionAdapter struct {
    chain plugin.Chain
}

func (h hooksDescriptionAdapter) Describe() (pre, post []server.HookDescription) {
    p, q := h.chain.Describe()
    return convertHookDescriptions(p), convertHookDescriptions(q)
}
```

---

### `.go-arch-lint.yml` (modify — declare plugin + plugin/pii)

**Analog:** `.go-arch-lint.yml:46-126` (every existing component declaration).

**Two new component blocks** (insert in alphabetical order under `components:` per existing convention):
```yaml
  plugin:
    in: plugin/**
  plugin_pii:
    in: plugin/pii/**
```

**Two new deps blocks** (apply TRST-04 — `plugin` may import canonical + engine for the
interfaces it implements, plus the new ULID dep via `anyVendorDeps`):
```yaml
  plugin:
    anyVendorDeps: true
    mayDependOn:
      - canonical
      - engine   # PreHook/PostHook interface types only; engine.PreHook
                 # / engine.PostHook are interfaces — implementing them
                 # without depending on the package's other internals
                 # is impossible in Go, so the dep is honest.
  plugin_pii:
    anyVendorDeps: true
    mayDependOn:
      - canonical
```

**Modify `server` block** to add `plugin` to `mayDependOn` ONLY IF the
`HookDescription` type is imported from `plugin/`. **Recommended:** keep
`server.HookDescription` declared in `server/` (mirror `server.AgentSlot` pattern at
`agents.go:40-45`) so `server.mayDependOn:` does NOT change. The `cmd/otto-gateway`
adapter does the field copy. Verify with `go-arch-lint check` after the change.

**Modify `engine` block?** No — engine already declares only `canonical + acp`. Hooks
implement engine's interfaces; engine does NOT import plugin. Boundary holds.

---

### `internal/plugin/chain_test.go`, `request_id_test.go`, `auth_test.go`, `logging_test.go`

**Analog:** `internal/auth/auth_test.go` (table-driven + named subtests) + `internal/server/agents_test.go` (fake-source pattern).

**Fake-source pattern** (`internal/server/agents_test.go:20-37`):
```go
// fakePoolDetailSource satisfies server.PoolDetailSource with a fixed
// []AgentSlot. Used to inject a known per-slot detail vector into the
// agentsHandler unit tests.
type fakePoolDetailSource struct {
    slots []server.AgentSlot
}

func (f fakePoolDetailSource) Detail() []server.AgentSlot { return f.slots }
```

**Phase 8 chain unit test pattern** — fake Pre/Post hooks track invocation order:
```go
// fakePreHook records Before calls in invocation order. The optional
// short-circuit response models the "first non-nil short-circuit wins"
// contract from internal/engine/engine.go:152-162.
type fakePreHook struct {
    name      string
    shortCircuit *canonical.ChatResponse
    callLog   *[]string
}

func (f *fakePreHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
    *f.callLog = append(*f.callLog, f.name)
    return f.shortCircuit, nil
}
```

**Table-driven test pattern** (`internal/auth/auth_test.go:46-74`):
```go
func TestBearer_ValidToken_PassesThrough(t *testing.T) {
    cfg := auth.Config{Tokens: []string{"s3cret"}}
    rec := httptest.NewRecorder()
    req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
    req.Header.Set("Authorization", "Bearer s3cret")
    auth.Bearer(cfg)(okHandler).ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("status: want 200, got %d", rec.Code)
    }
}
```

**Required: testmain_test.go** (`internal/engine/testmain_test.go:1-19` — same shape):
```go
package plugin

import (
    "testing"
    "go.uber.org/goleak"
)

func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```

---

### `internal/plugin/pii/walk_test.go` (property tests via testing/quick)

**Analog:** `internal/engine/pickcwd_test.go:208-240` — the project's property-test precedent.

**Property test pattern** (`internal/engine/pickcwd_test.go:208-231`):
```go
func TestPickCwd_NeverPanics(t *testing.T) {
    property := func(override string, uris []string, defaultCwd string) bool {
        links := make([]canonical.ResourceLinkBlock, 0, len(uris))
        for _, u := range uris {
            links = append(links, canonical.ResourceLinkBlock{URI: u})
        }
        req := &canonical.ChatRequest{
            WorkingDirOverride: override,
            ResourceLinks:      links,
        }
        _ = pickCwd(req, defaultCwd)
        return true
    }
    cfg := &quick.Config{MaxCount: 1000}
    if err := quick.Check(property, cfg); err != nil {
        t.Errorf("pickCwd property check failed: %v", err)
    }
}
```

**Phase 8 walker properties** (per CONTEXT.md §specifics):
1. **Never-panic** — any input shape (nil pointers, empty maps, deep nesting).
2. **Idempotent** — `Walk(Walk(x))` equals `Walk(x)` byte-for-byte.
3. **String-leaves-only** — keys and non-string leaves bit-identical input vs output.
4. **Cycle-safe** — terminates on cyclic refs (or document non-input).

```go
func TestWalk_NeverPanics(t *testing.T) {
    property := func(_ map[string]string) bool { // quick generates the map
        // build a randomly-nested any from a random seed
        _ = pii.WalkStringLeaves(buildRandomTree(), strings.ToUpper)
        return true
    }
    if err := quick.Check(property, &quick.Config{MaxCount: 1000}); err != nil {
        t.Errorf("Walk property check failed: %v", err)
    }
}

func TestWalk_Idempotent(t *testing.T) {
    property := func(input string) bool {
        tree := map[string]any{"k": input}
        once := pii.WalkStringLeaves(tree, redact)
        twice := pii.WalkStringLeaves(once, redact)
        return reflect.DeepEqual(once, twice)
    }
    if err := quick.Check(property, &quick.Config{MaxCount: 1000}); err != nil {
        t.Errorf("idempotent failed: %v", err)
    }
}
```

---

### `internal/plugin/pii/recognizers_test.go` (fixed-table tests)

**Analog:** `internal/auth/auth_test.go` table layout + `internal/config/config_test.go:75-104`
(table + subtests). Positive + negative cases per recognizer.

```go
func TestEmailRecognizer(t *testing.T) {
    cases := []struct {
        name  string
        input string
        wantMatch bool
    }{
        {"plain", "corey@cmetech.io", true},
        {"plus_alias", "corey+gsd@cmetech.io", true},
        {"subdomain", "user@mail.example.co.uk", true},
        {"not_email_at_alone", "@cmetech.io", false},
        {"not_email_no_tld", "corey@host", false},
    }
    for _, tc := range cases {
        tc := tc
        t.Run(tc.name, func(t *testing.T) {
            got := emailRe.MatchString(tc.input)
            if got != tc.wantMatch {
                t.Errorf("input=%q: got %v, want %v", tc.input, got, tc.wantMatch)
            }
        })
    }
}
```

---

### `internal/config/plugin_config_test.go`

**Analog:** `internal/config/config_test.go:41-73` (TestLoadEnvOverrides — `t.Setenv` + `Load()`
+ assert each Config field). Note: `t.Setenv` precludes `t.Parallel()`.

**Boot-error test pattern** — mode=hash with no key, mirror lines 41-73:
```go
func TestLoad_PIIHashModeRequiresKey(t *testing.T) {
    t.Setenv("PII_REDACTION_MODE", "hash")
    // PII_HASH_KEY deliberately not set.
    _, err := config.Load()
    if err == nil {
        t.Fatal("expected boot error when PII_REDACTION_MODE=hash with no PII_HASH_KEY")
    }
    if !strings.Contains(err.Error(), "PII_HASH_KEY") {
        t.Errorf("error should mention PII_HASH_KEY; got %v", err)
    }
}
```

---

### `internal/server/hooks_handler_test.go`

**Analog:** `internal/server/agents_test.go:1-118` (fakePoolDetailSource pattern + httptest +
decode body). Exact 1:1 mirror — change `AgentsResponse` to `HooksResponse` and the fake
source's interface method.

```go
type fakeHooksSource struct {
    pre, post []server.HookDescription
}
func (f fakeHooksSource) Describe() ([]server.HookDescription, []server.HookDescription) {
    return f.pre, f.post
}

func TestHooksHandler_EmptyChain(t *testing.T) {
    srv := newFromConfigForTest(t, server.Config{})
    r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/hooks", nil)
    w := httptest.NewRecorder()
    srv.ServeHTTP(w, r)
    if w.Code != http.StatusOK {
        t.Fatalf("GET /health/hooks: want 200, got %d", w.Code)
    }
    // ... decode + assert empty shape ...
}
```

---

### `tests/e2e/plugin_chain_test.go`

**Analog:** `tests/e2e/e2e_test.go:1-275` — the entire `bootGateway` harness and `gateOrSkip`
gate. The new test uses `bootGateway(t, map[string]string{"ENABLED_HOOKS": "...", "PII_REDACTION_ENABLED": "true"})`.

```go
//go:build e2e

package e2e_test

import (
    "encoding/json"
    "net/http"
    "testing"
)

func TestE2E_HealthHooks(t *testing.T) {
    gateOrSkip(t)
    baseURL, cleanup := bootGateway(t, nil)
    defer cleanup()

    resp, err := http.Get(baseURL + "/health/hooks")
    if err != nil { t.Fatal(err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        t.Fatalf("/health/hooks: want 200, got %d", resp.StatusCode)
    }
    var body map[string]any
    if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
        t.Fatal(err)
    }
    // Assert chain shape: 4 Pre, 1 Post.
}

func TestE2E_PIIRedaction(t *testing.T) {
    gateOrSkip(t)
    baseURL, cleanup := bootGateway(t, map[string]string{
        "PII_REDACTION_ENABLED": "true",
    })
    defer cleanup()
    // POST a request with an email; assert the upstream kiro-cli never sees it.
    // (Verifiable via the access-log line emitted by LoggingHook.)
}

func TestE2E_UnknownHookBootError(t *testing.T) {
    gateOrSkip(t)
    // bootGateway expects /health to come up. With ENABLED_HOOKS=BogusHook,
    // the gateway should exit non-zero BEFORE /health is reachable, which
    // bootGateway reports via t.Skipf("gateway exited before warmup …").
    // The test detects this in the captured stderr (look for "unknown hook").
}
```

---

## Shared Patterns

### Pattern A: Consumer-defined interface + cmd-level adapter (TRST-04 discipline)

**Source:** `internal/server/server.go:25-42` (`PoolStatsSource`, `RegistryStatsSource`) + `cmd/otto-gateway/main.go:428-490` (`poolStatsAdapter`, `poolDetailAdapter`, `registryStatsAdapter`).

**Apply to:** Every cross-package boundary new in Phase 8 — `HooksDescriptionSource` declared in `server/`, satisfied by a `hooksDescriptionAdapter` in `cmd/otto-gateway/main.go`.

**Excerpt** (`internal/server/server.go:25-31`):
```go
// PoolStatsSource is the consumer-defined interface healthHandler uses
// to render the {pool: {size, alive, busy}} sub-tree without importing
// internal/pool's Stats type into the server's public surface. *pool.Pool
// satisfies it structurally; a nil source is handled by healthHandler.
type PoolStatsSource interface {
    Stats() PoolStats
}
```

### Pattern B: Boot-error accumulation in config.Load

**Source:** `internal/config/config.go:124, 141-147, 169-171, 199-201`.

**Apply to:** All five new env keys + the mode/entity validators.

**Excerpt** (`internal/config/config.go:141-147, 199-201`):
```go
authTokens := getEnvStrSliceComma("AUTH_TOKEN", nil)

allowedIPEntries := getEnvStrSliceComma("ALLOWED_IPS", nil)
allowedIPs, err := parseCIDRs(allowedIPEntries)
if err != nil {
    errs = append(errs, fmt.Errorf("ALLOWED_IPS: %w", err))
}
// ... more env reads ...
if len(errs) > 0 {
    return Config{}, fmt.Errorf("config: invalid env vars: %w", errors.Join(errs...))
}
```

### Pattern C: Private struct ctx key + accessor pair

**Source:** `internal/server/middleware.go:13-14, 26-29, 50-55`.

**Apply to:** `RequestIDHook` (X-Request-Id via `requestIDKey{}`), `PIIRedactionHook` (summary via `piiSummaryKey{}`), `LoggingHook` (start time via `loggingStartKey{}`).

### Pattern D: Goleak gate per test package

**Source:** `internal/engine/testmain_test.go:1-19`.

**Apply to:** `internal/plugin/testmain_test.go` + `internal/plugin/pii/testmain_test.go`.

```go
package plugin // or pii

import (
    "testing"
    "go.uber.org/goleak"
)

func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```

### Pattern E: Property test via testing/quick

**Source:** `internal/engine/pickcwd_test.go:208-240`.

**Apply to:** `internal/plugin/pii/walk_test.go` (never-panic + idempotent + string-leaves-only invariants) + `internal/plugin/pii/luhn_test.go` (property: stripping non-digits is a no-op when string is already digit-only).

### Pattern F: AuthHook migration boundary (load-bearing operational note)

**Source:** `internal/auth/bearer.go:32-60` + `internal/server/server.go:230` (the current Bearer mount site).

**Apply to:** Plan must remove the `auth.Bearer` call at `internal/server/server.go:230` ONLY AFTER `AuthHook` is wired in `main.go`'s chain and the per-surface adapters parse auth from the request and stash it on ctx for AuthHook to read. **Cannot happen in a single commit** — the planner should sequence:
1. Wire AuthHook into chain (Pre).
2. Per-surface adapter writes auth token to ctx before invoking engine.
3. Verify e2e tests still 401 on bad tokens.
4. Then remove `auth.Bearer` from `server.go:230`.

The `auth.IPAllowlist` stays at chi (RESEARCH.md "Important note on Auth"); only Bearer migrates.

### Pattern G: E2E real-binary harness + gate

**Source:** `tests/e2e/e2e_test.go:65-258` (TestMain build + gateOrSkip + bootGateway + freePort + healthOK).

**Apply to:** `tests/e2e/plugin_chain_test.go` — reuse `bootGateway(t, extraEnv)` to inject `ENABLED_HOOKS`, `PII_REDACTION_ENABLED`, etc.

---

## No Analog Found

| File | Role | Data Flow | Reason / Replacement Pattern |
|------|------|-----------|------------------------------|
| `internal/plugin/pii/luhn.go` | utility / post-validator | pure function | No prior validator-style helper in repo. Use stdlib idiom: heavy doc-comment density (like `internal/auth/bearer.go:62-94 extractToken`'s Why+How comment) + table tests + property test (`Luhn(strip_non_digits(s)) == Luhn(s)` for digit-only strings). |

The PII recognizer literal `internal/plugin/pii/recognizers.go` is also "novel data" (no prior package-init regex literal in the repo) but the pattern is so stdlib-idiomatic (`var X = regexp.MustCompile(...)`) that it does not warrant a no-analog flag.

---

## Metadata

**Analog search scope:** `internal/{auth,canonical,config,engine,server,session,pool,adapter}` + `cmd/otto-gateway/` + `tests/e2e/` + `.go-arch-lint.yml`.

**Files scanned (read):** 14 source files (`hooks.go`, `bearer.go`, `auth.go`, `auth_test.go`, `server.go`, `agents.go`, `agents_test.go`, `health.go`, `middleware.go`, `config.go`, `config_test.go`, `engine.go`, `collect.go`, `pickcwd_test.go`, `chat.go`, `errors.go`, `main.go`, `e2e_test.go`, `testmain_test.go`).

**Pattern extraction date:** 2026-05-27.

## PATTERN MAPPING COMPLETE
