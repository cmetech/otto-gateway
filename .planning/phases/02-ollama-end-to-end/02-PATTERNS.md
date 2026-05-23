# Phase 2: Ollama End-to-End - Pattern Map

**Mapped:** 2026-05-23
**Files analyzed:** 28 new/modified files
**Analogs found:** 28 / 28 (100% — Phase 1 + Phase 1.1 packages are now in place and
provide strong analogs; the Node reference at
`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-ollama-server.js` is the
empirically-validated behavioural blueprint for all adapter-layer translations.)

---

## Greenfield Notice (Closed)

Phase 1 + Phase 1.1 closed the "no Go to copy from" gap. Every file in Phase 2 has at least
one strong in-repo analog. The Node reference remains the ground truth for **wire
shapes** (Ollama JSON in/out) and **handler-level behaviour** (cwd derivation, stub
endpoints, auth + IP allowlist logic). The Phase 1 Go code is the ground truth for
**idioms** (config struct, context-first, slog injection, sync primitives, goleak).

Where Node and Phase 1 disagree (Node uses regex string-splitting; Go uses `net/url` +
`net/netip`), **prefer the Go idiom** — the Node version's `pickCwd` regex string-split is
explicitly called out in RESEARCH.md Pitfall 3 as broken on Windows. Phase 2 fixes it.

---

## File Classification

| New / Modified File | Role | Data Flow | Closest Analog | Match Quality |
|---------------------|------|-----------|----------------|---------------|
| `internal/canonical/chat.go` (NEW) | model (domain types) | transform | `internal/canonical/chunk.go` (sibling: discriminated-union pattern) | exact (sibling) |
| `internal/canonical/chunk.go` (MODIFY — add `Name` to `ResourceLinkBlock`) | model | — | self (already done in Phase 1.1) | exact (additive) |
| `internal/engine/engine.go` (NEW) | service (orchestrator) | request-response | `internal/acp/client.go` (Config-struct + goroutine + ctx pattern) | role-match |
| `internal/engine/build_acp.go` (NEW) | utility (transform) | transform | Node reference `buildAcpBlocksFromOllama` (acp-ollama-server.js:484-541) | reference (Node behavioural) |
| `internal/engine/pickcwd.go` (NEW) | utility (pure function) | transform | Node reference `pickCwd` (acp-ollama-server.js:85-99) + RESEARCH.md Pattern (idiomatic Go re-implementation) | reference (Node behavioural — Go-idiomatic) |
| `internal/engine/collect.go` (NEW) | utility (stream→result aggregator) | streaming → request-response | `internal/acp/stream.go` (`Result()` blocking wait pattern) | role-match |
| `internal/engine/hooks.go` (NEW) | seam (interface definitions) | — | RESEARCH.md §"Engine Interface Shape" + PROJECT.md PLUG-01..05 | no in-repo analog |
| `internal/engine/engine_test.go` (NEW) | test (whitebox unit) | — | `internal/acp/client_test.go` (testutil.Logger + fake injection) | role-match |
| `internal/engine/pickcwd_test.go` (NEW) | test (property + table) | — | RESEARCH.md §"Property Test for pickCwd" + `internal/acp/translate_test.go` (table-driven) | partial-match |
| `internal/engine/build_acp_test.go` (NEW) | test (table-driven) | — | `internal/acp/translate_test.go` (table-driven wire mapping) | exact |
| `internal/engine/testmain_test.go` (NEW) | test (goleak gate) | — | `internal/acp/testmain_test.go` (verbatim copy + package rename) | exact |
| `internal/pool/pool.go` (NEW) | service (concurrency) | streaming + request-response | `internal/acp/client.go` (lifecycle + close-once + wg pattern) + Node `ACPPool` class (acp-ollama-server.js:330-373) | partial-match |
| `internal/pool/config.go` (NEW) | config | — | `internal/acp/client.go` lines 31-60 (Config struct + applyDefaults) | exact (sibling) |
| `internal/pool/stats.go` (NEW) | model | — | `internal/server/health.go` lines 26-49 (PoolStats already declared) | exact |
| `internal/pool/pool_test.go` (NEW) | test (whitebox unit) | — | `internal/acp/client_test.go` (lifecycle test pattern + fakeacp injection) | role-match |
| `internal/pool/testmain_test.go` (NEW) | test (goleak gate) | — | `internal/acp/testmain_test.go` (verbatim copy + package rename) | exact |
| `internal/auth/auth.go` (NEW) | config (middleware factory) | — | `internal/acp/client.go` lines 31-60 (Config struct pattern) | partial-match |
| `internal/auth/bearer.go` (NEW) | middleware | request-response | RESEARCH.md Pattern 3 + Node reference (acp-ollama-server.js:701-705) | reference (RESEARCH + Node) |
| `internal/auth/ipallowlist.go` (NEW) | middleware | request-response | RESEARCH.md Pattern 4 + Node reference (acp-ollama-server.js:696-699) | reference (RESEARCH + Node) |
| `internal/auth/auth_test.go` (NEW) | test (blackbox middleware) | — | `internal/server/server_test.go` (httptest pattern) | role-match |
| `internal/adapter/ollama/adapter.go` (NEW) | service (chi sub-router factory) | request-response | `internal/server/server.go` lines 22-65 (constructor + chi router build pattern) | exact (sibling) |
| `internal/adapter/ollama/wire.go` (NEW) | utility (wire ↔ canonical) | transform | `internal/acp/translate.go` (wire-struct + translateBlock pattern) | exact (sibling) |
| `internal/adapter/ollama/handlers.go` (NEW) | controller (HTTP handlers) | request-response | `internal/server/health.go` (handler pattern: header + WriteHeader + json.Encoder) + RESEARCH.md §"Ollama /api/chat Handler Skeleton" | exact (sibling) |
| `internal/adapter/ollama/render.go` (NEW) | utility (response builder) | transform | Node reference `makeStats` + `chunksToOllamaMessage` (acp-ollama-server.js:559-611) | reference (Node behavioural) |
| `internal/adapter/ollama/stub.go` (NEW) | controller (stub handlers) | request-response | Node reference (acp-ollama-server.js:1006-1029) + handler conventions from `health.go` | reference + role-match |
| `internal/adapter/ollama/handlers_test.go` (NEW) | test (whitebox + fake engine) | — | `internal/server/server_test.go` (httptest + ServeHTTP pattern) | role-match |
| `internal/adapter/ollama/integration_test.go` (NEW) | test (blackbox real-kiro) | — | `internal/acp/integration_test.go` (resolveKiroCLI auto-skip pattern) | exact (sibling) |
| `internal/config/config.go` (MODIFY) | config | — | self lines 17-72 (extend the Config struct + Load body using existing `getEnvStr`/`getEnvBool`/`getEnvInt`/`getEnvStrSlice` helpers) | exact (self-extension) |
| `internal/server/server.go` (MODIFY) | transport (HTTP) | request-response | self lines 35-59 (extend constructor to accept auth tokens + IP prefixes + Ollama sub-router + pool for /health) | exact (self-extension) |
| `internal/server/health.go` (MODIFY) | model + handler | — | self lines 9-66 (wire `pool.Pool.Stats()` into the existing `PoolStats` struct) | exact (self-extension) |
| `cmd/loop24-gateway/main.go` (MODIFY) | entrypoint (wiring) | — | self (all 70 lines) + Phase 1 pattern + RESEARCH.md §Integration Points | exact (self-rewrite) |

---

## Pattern Assignments

### NEW: `internal/canonical/chat.go` (model, transform)

**Analog (exact, sibling):** `internal/canonical/chunk.go` — same discriminated-union
idiom that Phase 1 + 1.1 established. New file required (not extending `chunk.go`) because
chunk vs chat are orthogonal subject areas; Phase 1.1 already split out
`stop_reason.go`/`model.go`/`capabilities.go` to keep `chunk.go` focused on the
ChunkKind/Block subject. **Add `Name` field to `ResourceLinkBlock`** — already done in
Phase 1.1 per chunk.go line 96; verify it's present.

**File-scoped layers (Phase 1 D-01):** This package stays single-file-per-subject — chat
types in `chat.go`, chunk/block types in `chunk.go`, capability types in
`capabilities.go`, etc. Do not merge into one big `types.go`.

**Imports pattern** (none — leaf package invariant):
```go
// Package canonical defines the typed chunk and block types that flow through
// the Loop24 gateway. This package imports nothing under internal/.
package canonical
```

**Discriminated-union pattern** (from `chunk.go` lines 5-33 — apply verbatim):
```go
// MessageRole is the role of a chat message.
type MessageRole int

const (
    RoleUser MessageRole = iota
    RoleSystem
    RoleAssistant
    RoleTool
)

// ContentKind is the discriminator for a ContentPart.
type ContentKind int

const (
    ContentKindText ContentKind = iota
    ContentKindImage
    ContentKindToolUse    // dormant in Phase 2; populated in Phase 3.1 / 6
    ContentKindToolResult // dormant in Phase 2
    ContentKindThinking   // dormant in Phase 2
)

// Message is a chat message. Content uses []ContentPart from day one (D-09)
// so Phase 3.1 / 6 do not need to widen the type later. Phase 2 only
// populates Kind == ContentKindText (len(Content)==1) and ContentKindImage
// (from Ollama messages[].images).
type Message struct {
    Role       MessageRole
    Content    []ContentPart
    ToolCalls  []ToolCall // dormant in Phase 2
    ToolCallID string     // dormant in Phase 2
}

type ContentPart struct {
    Kind       ContentKind
    Text       string
    Image      *ImagePart
    ToolUse    *ToolUsePart
    ToolResult *ToolResultPart
}
```

**No JSON tags rule** (D-11, mirroring Phase 1 D-04): zero `json:"..."` tags in this
package. Wire translation lives in `internal/adapter/ollama/wire.go` and
`internal/acp/translate.go`. Tests use cmp.Diff or hand-rolled equality, not JSON
round-trip — matches Phase 1.1's `chunk.go` / `stop_reason.go` discipline.

**Gotcha:** Forward-design `ChatRequest` includes `Tools []ToolSpec`, `ToolChoice
*ToolChoice`, `Format *Format`, `Metadata map[string]any`, `Temperature *float64`,
`TopP *float64`, `MaxTokens int`, `StopSequences []string`, `Stream bool`, `Think bool`,
`WorkingDirOverride string` per D-08 — Phase 2 leaves these zero-valued seams; they
activate in Phase 3 / 3.1 / 4 / 6.

---

### NEW: `internal/engine/engine.go` (service, orchestrator)

**Analog (role-match):** `internal/acp/client.go` — same Config-struct constructor +
goroutine + context-first pattern. Engine differs in that it has **no goroutines of its
own** in Phase 2 (it just calls into the ACPClient interface) and **no subprocess**
(pool/acp own that). But the constructor shape, config struct, error wrapping, and slog
injection are identical.

**Imports pattern** (mirror `internal/acp/client.go` lines 3-18):
```go
package engine

import (
    "context"
    "fmt"
    "log/slog"

    "loop24-gateway/internal/canonical"
)
```

**Consumer-defined interface pattern** (D-03; from RESEARCH.md §"Engine Interface
Shape"):
```go
// ACPClient is the contract the engine requires from the ACP-talking layer.
// Both *acp.Client (direct) and *pool.Pool (slot-routed) structurally satisfy this.
// Consumer-defined here per the Go community idiom — consumers define interfaces,
// producers expose concrete types.
//
// Phase 2 shape: NewSession returns a session id; Prompt streams chunks via Stream;
// Cancel is best-effort.
type ACPClient interface {
    NewSession(ctx context.Context, cwd string) (string, error)
    SetModel(ctx context.Context, sessionID, modelID string) error
    Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (Stream, error)
    Cancel(sessionID string)
}

// Stream mirrors *acp.Stream but is interface-typed so the pool can return a
// pool-aware wrapper that releases the slot on stream close (Phase 5 hook).
type Stream interface {
    Chunks() <-chan canonical.Chunk
    Result() (*canonical.FinalResult, error)
}
```

**Adapter shim required:** `*acp.Client.Prompt` returns `*acp.Stream` (concrete). The
engine's `ACPClient` interface above returns `engine.Stream` (interface). The pool wraps
`*acp.Client` and exposes a method whose return type is `engine.Stream`. A tiny shim
struct around `*acp.Stream` (or a direct field-access adapter) implements `Chunks()` and
`Result()` to satisfy the interface. **Open Question OQ-1 (RESEARCH.md):** planner may
also choose a `WithSlot(ctx, fn) error` callback shape — either is acceptable.

**Config-struct constructor** (Phase 1 D-05, copy from `internal/acp/client.go` lines
31-60):
```go
// Config holds engine configuration.
// Required field: Logger. ACP is required for non-test use; tests may inject a fake.
type Config struct {
    Logger     *slog.Logger
    ACP        ACPClient
    DefaultCWD string // typically cfg.KiroCWD from config.Config — used as pickCwd fallback
    PreHooks   []PreHook  // D-04 seam: empty in Phase 2
    PostHooks  []PostHook // D-04 seam: empty in Phase 2
}

type Engine struct {
    cfg Config
}

func New(cfg Config) *Engine {
    return &Engine{cfg: cfg}
}
```

**Run method pattern** (D-01 + D-05; mirrors `acp.Client.Prompt` shape from
`internal/acp/client.go` lines 600-662):
```go
// Run executes one ChatRequest. Returns a handle whose Chunks channel emits canonical
// chunks until the stream ends, and whose Result() returns the assembled ChatResponse.
// Phase 4 SSE/NDJSON adapter ranges Chunks directly; Phase 2 adapter calls Collect.
func (e *Engine) Run(ctx context.Context, req *canonical.ChatRequest) (*Run, error) {
    // 1. PreHooks (empty in Phase 2 — D-04 seam)
    for _, h := range e.cfg.PreHooks {
        resp, err := h.Before(ctx, req)
        if err != nil { return nil, err }
        if resp != nil { return newCompletedRun(resp), nil } // short-circuit
    }
    // 2. cwd derivation
    cwd := pickCwd(req, e.cfg.DefaultCWD)
    // 3. block flattening
    blocks := buildBlocks(req)
    // 4. session lifecycle (D-05: new session per Run)
    sid, err := e.cfg.ACP.NewSession(ctx, cwd)
    if err != nil { return nil, fmt.Errorf("engine: session/new: %w", err) }
    if req.Model != "" && req.Model != "auto" {
        if err := e.cfg.ACP.SetModel(ctx, sid, req.Model); err != nil {
            e.cfg.ACP.Cancel(sid) // best-effort cleanup (Pitfall 6)
            return nil, fmt.Errorf("engine: set_model: %w", err)
        }
    }
    stream, err := e.cfg.ACP.Prompt(ctx, sid, blocks)
    if err != nil {
        e.cfg.ACP.Cancel(sid)
        return nil, fmt.Errorf("engine: prompt: %w", err)
    }
    return newRun(ctx, e, sid, stream, req), nil
}
```

**Critical pitfalls:**
- **Pitfall 6 from RESEARCH.md:** `Cancel(sid)` on any error after `NewSession` succeeds.
  The `defer func() { if !runOK { e.cfg.ACP.Cancel(sid) }}()` pattern works inside `Run`
  but the cleaner shape is direct-cancel-on-error at each step (above).
- **`req.Model == "" || req.Model == "auto"` → skip SetModel** (D-05).

---

### NEW: `internal/engine/build_acp.go` (utility, transform)

**Analog (Node reference behavioural):** `buildAcpBlocksFromOllama` in
`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-ollama-server.js:484-541`.
**Critical insight:** the Node function takes Ollama-shaped JSON directly; ours takes
`*canonical.ChatRequest` (the adapter has already done the wire→canonical translation).
The bracketed-section ACP text format is the same.

**Pattern source — Node `buildAcpBlocksFromOllama` (lines 484-541) translated to Go:**

```go
// buildBlocks flattens a canonical.ChatRequest into the ACP block list kiro-cli expects.
// The output is a leading text block (the bracketed-section transcript) followed by any
// inline image blocks. This is the SINGLE SOURCE OF TRUTH across all three adapter
// surfaces (Ollama, OpenAI, Anthropic) per D-02.
//
// Bracketed sections (Node reference parity, lines 484-541):
//   [System]\n<text>\n\n
//   [Reasoning] Think through the problem step by step...
//   [Output format] Respond ONLY with JSON...
//   [Available tools]\nEmit a tool_call ACP notification...\n```json\n<tools>\n```\n\n
//   [User]\n<text>\n\n
//   [Assistant]\n<text>\n\n
//   [Assistant tool call: <name>]\n<args-json>\n\n
//   [Tool result (id: <id>)]\n<text>\n\n
//
// Phase 2 only emits [System] / [Reasoning] / [Output format] / [User] / [Assistant]
// because Tools / ToolCalls / ToolResults are deferred to Phase 3.1 / 6. Keep the
// switch arms in place so Phase 6 just fills bodies.
func buildBlocks(req *canonical.ChatRequest) []canonical.Block {
    var b strings.Builder
    if req.System != "" {
        fmt.Fprintf(&b, "[System]\n%s\n\n", req.System)
    }
    if req.Think {
        b.WriteString("[Reasoning] Think through the problem step by step before answering. Show your reasoning.\n\n")
    }
    if req.Format != nil { /* [Output format] block */ }
    if len(req.Tools) > 0 { /* [Available tools] block; Phase 2 path is dormant */ }
    var imageBlocks []canonical.Block
    for _, m := range req.Messages {
        switch m.Role {
        case canonical.RoleSystem:
            // Already extracted above; skip in transcript.
        case canonical.RoleAssistant:
            // Phase 2: text only (ToolCalls is dormant)
            fmt.Fprintf(&b, "[Assistant]\n%s\n\n", joinTextParts(m.Content))
        case canonical.RoleTool:
            // Dormant in Phase 2.
        default: // RoleUser
            fmt.Fprintf(&b, "[User]\n%s\n\n", joinTextParts(m.Content))
        }
        // Image collection — flows out as separate canonical.Block entries.
        for _, part := range m.Content {
            if part.Kind == canonical.ContentKindImage && part.Image != nil {
                imageBlocks = append(imageBlocks, /* canonical image block */)
            }
        }
    }
    text := strings.TrimRight(b.String(), "\n")
    return append([]canonical.Block{
        {Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: text}},
    }, imageBlocks...)
}
```

**Gotcha:** Node's `buildAcpBlocksFromOllama` puts images as ACP `image` blocks AFTER the
text block; Phase 2 does the same but `canonical.Block` doesn't currently have a
`BlockKindImage` variant. **Either**: (a) extend `canonical.Block` with a new variant
(planner discretion — Phase 1.1 D-04 sets the precedent for additive variant kinds), OR
(b) defer image-block support to Phase 3.1 and treat Ollama `messages[].images` as Phase 2
dead code (still parse, but drop with a `logger.Debug` per Assumption A4). The Phase 2
acceptance test from the user is text-only LangFlow chat, so option (b) is the
lower-risk Phase 2 scope. CONTEXT.md D-09 says "Phase 2 only populates Kind == Text
(single part) and Kind == Image" — flag this for planner to decide.

**Example_ function** (TRST-07 + Phase 1 Deferred): add
`func ExampleBuildBlocks()` showing `[System]/[User]/[Assistant]` output. Use the same
runnable-godoc shape Phase 1.1 used for `Example_translateUpdate`.

---

### NEW: `internal/engine/pickcwd.go` (utility, pure function)

**Analog (Node behavioural + Go re-implementation):** Node `pickCwd` at
`acp-ollama-server.js:85-99` defines the algorithm; **the Node implementation is broken
on Windows** (`b.uri.replace(/^file:\/\//, '')` produces `/C:/path` for `file:///C:/path`
which is not a valid Windows path — RESEARCH.md Pitfall 3). Go re-implementation uses
`net/url` + `path/filepath` + `runtime` to do the right thing on both POSIX and Windows.

**Imports pattern:**
```go
package engine

import (
    "net/url"
    "os"
    "path/filepath"
    "runtime"
    "strings"

    "loop24-gateway/internal/canonical"
)
```

**Core algorithm** (RESEARCH.md §"pickCwd Algorithm" lines 998-1108; copy verbatim):
```go
// pickCwd derives the per-request working directory using D-16's priority:
//   1. req.WorkingDirOverride (from X-Working-Dir header)
//   2. Longest common parent of file:// resource_link block URIs
//   3. defaultCwd (typically cfg.DefaultCWD == config.Config.KiroCWD)
//   4. os.Getwd()
//
// Pure function: the only side effect is os.Getwd at the bottom of the fallback chain.
// Property-tested per TRST-06 via testing/quick.
func pickCwd(req *canonical.ChatRequest, defaultCwd string) string {
    if req.WorkingDirOverride != "" {
        return req.WorkingDirOverride
    }
    if parent := longestCommonParent(extractFileURIs(req)); parent != "" {
        return parent
    }
    if defaultCwd != "" {
        return defaultCwd
    }
    if cwd, err := os.Getwd(); err == nil {
        return cwd
    }
    return "."
}

// Critical Windows-path handling — fixes Node's broken regex:
//   p := u.Path                                         // "/C:/foo/bar"
//   if runtime.GOOS == "windows" && len(p) >= 3 &&
//       p[0] == '/' && p[2] == ':' {
//       p = p[1:]                                        // "C:/foo/bar"
//   }
//   p = filepath.FromSlash(p)                            // "C:\foo\bar" on Windows
```

**Example_ function** (TRST-07 + RESEARCH.md Wave 0): add `func ExamplePickCwd()` showing
a 3-URI input → common-parent output. Runnable via `go test -run Example`.

**Gotcha — Phase 2 reality:** the Ollama `/api/chat` wire shape doesn't carry
`resource_link` URIs (those flow in via Anthropic — Phase 3.1). So Phase 2's `pickCwd`
effectively only honors `X-Working-Dir` header → `KIRO_CWD` → `os.Getwd()` in practice.
The longest-common-parent code path is forward-design for Phase 3.1. **Property tests must
still cover all paths** because dormant code that fails the first time it's exercised is
worse than dead code.

---

### NEW: `internal/engine/collect.go` (utility, streaming→request-response)

**Analog (role-match):** `internal/acp/stream.go` — same "block until done" pattern
embodied by `Result()`. `engine.Collect` is the inverse: it ranges over `Chunks` to
completion and assembles the canonical ChatResponse from the accumulated text + final
stop reason.

**Pattern from `internal/acp/stream.go` lines 87-92** (`Result()` blocking shape):
```go
func (s *Stream) Result() (*FinalResult, error) {
    <-s.done
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.result, s.err
}
```

**Phase 2 `Collect` shape:**
```go
// Collect runs a Chat request to completion (non-streaming). Returns the assembled
// ChatResponse. Phase 2 adapter handlers call this; Phase 4 streaming adapter ranges
// stream.Chunks directly.
func (e *Engine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
    run, err := e.Run(ctx, req)
    if err != nil { return nil, err }
    var text strings.Builder
    var chunkCount int
    for ch := range run.Stream.Chunks() {
        switch ch.Kind {
        case canonical.ChunkKindText:
            text.WriteString(ch.Text.Content)
        case canonical.ChunkKindThought:
            // Phase 2: drop unless req.Think (Assumption A4 — planner finalizes)
        }
        chunkCount++
    }
    final, err := run.Stream.Result()
    if err != nil { return nil, fmt.Errorf("engine: collect: %w", err) }
    return assembleChatResponse(req, text.String(), final), nil
}
```

**Gotcha:** `assembleChatResponse` maps `final.StopReason` (canonical enum) to
`canonical.ChatResponse.StopReason`. The mapping is identity (same enum); but if
Phase 1.1 captures stop_reason on `*acp.Stream` via `FinalResult`, ensure the adapter
between `*acp.Stream` and `engine.Stream` preserves it. **Check `internal/acp/stream.go`
FinalResult shape: currently only has `SessionID` and `ChunkCount` — no `StopReason`
field. Phase 2 needs to either:**
- (a) extend `acp.FinalResult` to carry `StopReason canonical.StopReason`, OR
- (b) extract stop_reason in the engine's `acp.Stream` adapter from a different source.

This is a real dependency on Phase 1.1's wire-alignment work (CONTEXT.md hard
dependency). RESEARCH.md D-10 says "StopReason mapped from session/prompt response's
stopReason field per Phase 1.1" — confirm Phase 1.1 plumbed it through to `FinalResult`
before Phase 2 starts.

---

### NEW: `internal/engine/hooks.go` (seam, interface definitions)

**Analog:** No in-repo Go analog (PLUG-* interfaces are first-instantiated here). Pattern
source: RESEARCH.md Engine Interface Shape + PROJECT.md PLUG-01..05 + CONTEXT.md D-04.

**Pattern (D-04 seam — empty in Phase 2 but the shapes are locked):**
```go
package engine

import (
    "context"

    "loop24-gateway/internal/canonical"
)

// PreHook runs BEFORE the engine touches ACP. Returning (non-nil, nil) short-circuits
// the engine and returns that response to the caller (e.g., a cache hit). Returning
// (_, non-nil) is a hard error.
// Phase 2 has no PreHook implementations; Phase 8 registers RequestIDHook, AuthHook,
// LoggingHook implementations.
type PreHook interface {
    Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
}

// PostHook runs AFTER the engine assembles the canonical ChatResponse, before it returns
// to the adapter. May mutate the response (e.g., redaction) or return an error to fail
// the request.
type PostHook interface {
    After(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) (*canonical.ChatResponse, error)
}
```

**Gotcha — D-04 seam discipline:** the hook slices are FIELDS on `engine.Config`; Phase 2
leaves them nil. `Run` ranges over both (the `for range nil` is a valid Go no-op). Phase
8 just registers slice elements; engine.Run body is unchanged. Tests in Phase 2 verify
the "empty-slices = pass-through" property explicitly.

---

### NEW: `internal/pool/pool.go` (service, concurrency)

**Analog (partial — closest by lifecycle pattern):** `internal/acp/client.go` lines
246-262, 746-785 (constructor + Close shutdown order). Pool wraps a fixed-size set of
`*acp.Client` instances and routes the engine's `ACPClient` method calls through an
acquired slot.

**Analog (behavioural — Node):** `ACPPool` class at
`acp-ollama-server.js:330-373`. Phase 2 ports the design (channel-of-slots, warmup at
boot, slot acquire/release) using Go idioms. Phase 5 will add dead-slot detection
(POOL-04) and lazy re-spawn; Phase 2 fail-fasts at warmup (D-07a).

**Imports pattern** (mirror `internal/acp/client.go`):
```go
package pool

import (
    "context"
    "errors"
    "fmt"
    "log/slog"
    "sync"

    "loop24-gateway/internal/acp"
    "loop24-gateway/internal/canonical"
    "loop24-gateway/internal/engine"
)
```

**Note on import boundary:** `pool` importing `engine` is fine — the engine declares an
`ACPClient` interface, and the pool's value satisfies it. Pool does NOT export an
interface that engine needs to import. Phase 1's `go-arch-lint` scaffold (Phase 1 Plan
01-03) should permit `pool → engine` for type-satisfaction. Verify the rule allows it
before adding it.

**Wait — re-think:** The cleaner pattern is `pool` does NOT import `engine`. Both
satisfy the same `engine.ACPClient` interface independently. The shim that converts
`*acp.Stream` → `engine.Stream` belongs in `internal/engine/acp_stream.go` (a tiny
adapter file) or in `cmd/loop24-gateway/main.go` (one-line wrapper at wiring time).
Planner decides. **Recommended:** put the shim in `internal/engine/acp_stream.go` so the
pool stays a pure ACP-routing primitive that knows nothing about `engine.Stream`.

**Slot + channel-of-slots pattern** (from RESEARCH.md Pattern 2 + Node
`ACPPool`/`_initSlot`):
```go
type Slot struct {
    Label  string  // "pool-0", "pool-1", ...
    Client *acp.Client
}

type Pool struct {
    cfg    Config
    slots  chan *Slot                    // buffered, size=cfg.Size; acquire=<-slots, release=slots<-slot
    all    []*Slot                       // for Stats and Close (held under mu)
    models []canonical.ModelInfo         // captured once during Warmup (D-13)
    mu     sync.Mutex                    // guards all + closed + models
    closed bool
}

func New(cfg Config) *Pool {
    cfg.applyDefaults()  // PoolSize default 1
    return &Pool{
        cfg:   cfg,
        slots: make(chan *Slot, cfg.Size),
    }
}
```

**Warmup pattern** (D-07a fail-fast sequential; from RESEARCH.md Pattern 2 + Node
`warmup`):
```go
// Warmup spawns + initializes Size kiro-cli subprocesses sequentially.
// On any failure, partially-built slots are closed and the error returned.
// After successful Warmup, the first slot's session/new result.models.availableModels
// is captured for /api/tags (D-13).
func (p *Pool) Warmup(ctx context.Context) error {
    for i := 0; i < p.cfg.Size; i++ {
        slot, err := p.initSlot(ctx, fmt.Sprintf("pool-%d", i))
        if err != nil {
            p.closeAll()
            return fmt.Errorf("pool: warmup slot %d: %w", i, err)
        }
        p.mu.Lock()
        p.all = append(p.all, slot)
        p.mu.Unlock()
        p.slots <- slot  // never blocks (chan has size capacity)

        if i == 0 {
            // D-13: capture model catalog from the first slot only.
            // session/new (which populates AvailableModels per Phase 1.1) was already
            // called inside initSlot via acp.Client.NewSession; reuse the cached value.
            p.mu.Lock()
            p.models = slot.Client.AvailableModels()
            p.mu.Unlock()
        }
    }
    return nil
}
```

**ACPClient interface satisfaction** (the pool routes through an acquired slot):
```go
// Acquire/Release shape — channel-of-slots per POOL-03.
// engine calls NewSession → SetModel → Prompt → Cancel as four separate methods; the
// pool must hold the SAME slot across all four calls for one logical "Run." Two options:
//   (a) Add Acquire(ctx)→*Slot and require engine to defer Release(slot)
//   (b) Internal session→slot map so Pool.NewSession returns sid, pool maps sid→slot,
//       subsequent Pool.{SetModel,Prompt,Cancel}(sid, ...) look up the slot from sid
//
// (b) is simpler for the engine but adds a goroutine-safe map. (a) is leak-prone.
// PLANNER DECISION: pick one, document why. Recommended: (b) because the session ID is
// already the natural correlation key.
```

**Close + idempotency** (mirror `internal/acp/client.go` lines 746-785 `closeOnce` pattern):
```go
func (p *Pool) Close() error {
    p.mu.Lock()
    if p.closed { p.mu.Unlock(); return nil }
    p.closed = true
    p.mu.Unlock()
    return p.closeAll()
}
```

**Gotcha — POOL-02 ordering:** `main.go` MUST call `pool.Warmup(ctx)` synchronously
BEFORE `srv.RunUntilSignal(ctx)`. Going async (warmup in goroutine while server starts)
violates POOL-02 and creates a 5-second-first-request race documented in RESEARCH.md
Pitfall 4.

---

### NEW: `internal/pool/config.go` (config)

**Analog (exact, sibling):** `internal/acp/client.go` lines 31-60 — `Config` struct +
`applyDefaults()` private method. **Pattern is verbatim.**

```go
type Config struct {
    Logger       *slog.Logger
    Size         int           // default 1 (D-07 Phase 2; Phase 5 bumps to 4)
    KiroCmd      string        // from config.Config.KiroCmd
    KiroArgs     []string      // from config.Config.KiroArgs
    KiroCWD      string        // from config.Config.KiroCWD
    PingInterval time.Duration // from config.Config.PingInterval
}

func (c *Config) applyDefaults() {
    if c.Size <= 0 { c.Size = 1 }
    if c.Logger == nil { /* panic? defensive default? planner discretion */ }
    // KiroCmd / KiroArgs / PingInterval already defaulted upstream by acp.Config.applyDefaults
    // when the slot is initialized — no need to double-default here.
}
```

**Discretion (CONTEXT.md):** `Config` value vs `*Config` pointer parameter — pick one and
be consistent. Phase 1 uses value (`acp.Config` is the param); recommend same for pool.

---

### NEW: `internal/pool/stats.go` (model)

**Analog (exact):** `internal/server/health.go` lines 26-49 — `PoolStats` struct already
defined; Phase 2 just produces it.

**Pattern:**
```go
// Stats is a snapshot of the pool's runtime state. Returned by /health.
type Stats struct {
    Size  int
    Alive int
    Busy  int
}

func (p *Pool) Stats() Stats {
    p.mu.Lock()
    defer p.mu.Unlock()
    alive := 0
    for _, s := range p.all {
        if s.Client != nil { alive++ }
    }
    busy := len(p.all) - len(p.slots) // slots in chan = free; rest are busy
    return Stats{
        Size:  p.cfg.Size,
        Alive: alive,
        Busy:  busy,
    }
}
```

**Critical:** `internal/server/health.go.PoolStats` is the OUTPUT struct (JSON-tagged);
`internal/pool/stats.go.Stats` is the INTERNAL struct (no JSON tags — canonical
discipline). The `/health` handler maps `pool.Stats() → server.PoolStats` in
`internal/server/health.go` — this mapping is one-line and keeps the boundary clean.

---

### NEW: `internal/auth/auth.go` (config, middleware factory)

**Analog (partial):** `internal/acp/client.go` lines 31-60 — Config struct pattern. Auth
package is HTTP-focused so the rest of the file is different.

**Pattern:**
```go
package auth

import (
    "net/netip"

    "log/slog"
)

// Config is the auth middleware configuration.
type Config struct {
    Logger          *slog.Logger
    Tokens          []string       // parsed from config.Config.AuthToken (empty = no auth — Node default)
    AllowedPrefixes []netip.Prefix // parsed from config.Config.AllowedIPs (empty = allow-all — Node default)
}
```

**OQ-3 resolved:** RESEARCH.md recommends `internal/auth` as a peer package — keeps
middleware separable; Phase 8 may import auth logic from the engine hook layer.

---

### NEW: `internal/auth/bearer.go` (middleware, request-response)

**Analog (RESEARCH.md + Node behavioural):** RESEARCH.md Pattern 3 lines 605-640 (full
example with constant-time comparison) + Node reference `acp-ollama-server.js:701-705`.

**Pattern (RESEARCH.md Pattern 3 — apply verbatim):**
```go
package auth

import (
    "crypto/subtle"
    "net/http"
    "strings"
)

// Bearer returns a middleware that validates Authorization: Bearer <token> header.
// Empty Tokens disables auth entirely (matches Node default — operator-configured).
// Constant-time comparison defends against timing side-channels per Pattern 3.
func Bearer(cfg Config) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if len(cfg.Tokens) == 0 {
                next.ServeHTTP(w, r); return
            }
            authHeader := r.Header.Get("Authorization")
            const prefix = "Bearer "
            if !strings.HasPrefix(authHeader, prefix) {
                writeOllamaError(w, http.StatusUnauthorized, "Invalid or missing API key")
                return
            }
            provided := []byte(authHeader[len(prefix):])
            for _, valid := range cfg.Tokens {
                if subtle.ConstantTimeCompare(provided, []byte(valid)) == 1 {
                    next.ServeHTTP(w, r); return
                }
            }
            writeOllamaError(w, http.StatusUnauthorized, "Invalid or missing API key")
        })
    }
}
```

**`writeOllamaError`:** small package-private helper that writes `{"error": "<msg>"}` with
the given status — matches the Node `ollamaError` shape verbatim
(`acp-ollama-server.js:101-103`). Place it in `auth.go` (shared by both middlewares).

**Critical (gosec / Phase 1 trust gates):** `subtle.ConstantTimeCompare` is the ONLY
acceptable comparison. Plain `==` triggers no linter today but is a documented
anti-pattern (RESEARCH.md Anti-Patterns line 722).

**Test pattern (RESEARCH.md §"Validation Architecture"):** `internal/auth/auth_test.go`
tests both empty-token (passthrough) and populated-token (accept-valid, reject-invalid)
paths using `httptest.NewRecorder` + a stub `next` handler. Mirror the structure of
`internal/server/server_test.go`.

---

### NEW: `internal/auth/ipallowlist.go` (middleware, request-response)

**Analog (RESEARCH.md + Node):** RESEARCH.md Pattern 4 lines 656-714 + Node reference
`acp-ollama-server.js:696-699`.

**Pattern (RESEARCH.md Pattern 4 — apply verbatim, see lines 656-714):**
```go
package auth

import (
    "net"
    "net/http"
    "net/netip"
    "strings"
)

func IPAllowlist(cfg Config) func(http.Handler) http.Handler {
    if len(cfg.AllowedPrefixes) == 0 {
        return func(next http.Handler) http.Handler { return next } // allow-all
    }
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            clientIP, ok := extractClientIP(r)
            if !ok {
                writeOllamaError(w, http.StatusForbidden, "could not determine client IP")
                return
            }
            for _, prefix := range cfg.AllowedPrefixes {
                if prefix.Contains(clientIP) {
                    next.ServeHTTP(w, r); return
                }
            }
            writeOllamaError(w, http.StatusForbidden, "IP "+clientIP.String()+" not in allowlist")
        })
    }
}

func extractClientIP(r *http.Request) (netip.Addr, bool) {
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
        if addr, err := netip.ParseAddr(first); err == nil {
            return addr, true
        }
    }
    host, _, err := net.SplitHostPort(r.RemoteAddr)
    if err != nil { return netip.Addr{}, false }
    addr, err := netip.ParseAddr(strings.TrimPrefix(host, "::ffff:"))
    return addr, err == nil
}
```

**Gotcha — XFF trust** (RESEARCH.md Assumption A3): laptop deployment has no proxy →
XFF is attacker-controllable. The pattern above honors XFF when present (Node parity);
the planner should document the deployment-model assumption inline OR add an
`AUTH_TRUST_XFF` env var. For Phase 2 laptop-only, Node parity is fine.

**Critical `::ffff:` strip:** Go's `RemoteAddr` returns `[::ffff:127.0.0.1]:12345` for
IPv4 on dual-stack sockets — must strip before parsing as `netip.Addr` or matches will
fail. Node does the same strip (acp-ollama-server.js:697).

---

### NEW: `internal/adapter/ollama/adapter.go` (service, chi sub-router factory)

**Analog (exact, sibling):** `internal/server/server.go` lines 22-65 — `Server` struct +
constructor + chi router build. Adapter is the same pattern at a smaller scope.

**Pattern (from RESEARCH.md §"Pattern 1: Adapter-over-Canonical with chi Sub-Router"
lines 340-391 + Phase 1 server.go shape):**
```go
package ollama

import (
    "log/slog"

    "github.com/go-chi/chi/v5"

    "loop24-gateway/internal/canonical"
)

// Engine is the contract this adapter requires from the engine layer.
// Adapter does NOT import internal/engine (TRST-04 boundary — go-arch-lint enforces).
// *engine.Engine satisfies this structurally.
type Engine interface {
    Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error)
}

// ModelCatalog is the contract for /api/tags + /api/show. *pool.Pool satisfies this.
type ModelCatalog interface {
    Models() []canonical.ModelInfo
}

type Config struct {
    Logger       *slog.Logger
    Engine       Engine
    ModelCatalog ModelCatalog
    Version      string
    Commit       string
}

type Adapter struct {
    cfg    Config
    router chi.Router
}

func New(cfg Config) *Adapter {
    a := &Adapter{cfg: cfg}
    r := chi.NewRouter()
    r.Get("/version", a.handleVersion)   // D-12: moved here from server
    r.Post("/chat", a.handleChat)
    r.Post("/generate", a.handleGenerate)
    r.Get("/tags", a.handleTags)
    r.Post("/show", a.handleShow)
    r.Get("/ps", a.handlePS)
    r.Post("/pull", a.handlePull)
    r.Post("/push", a.handlePush)
    r.Post("/create", a.handleCreate)
    r.Post("/copy", a.handleCopy)
    r.Delete("/delete", a.handleDelete)
    a.router = r
    return a
}

// Router exposes the chi sub-router for server to mount. Mounted under OLLAMA_PATH_PREFIX
// (default "/api"). The server applies auth middleware to the mount point per D-14.
func (a *Adapter) Router() chi.Router { return a.router }

// HandleVersion is exposed separately so the server can register it on the OUTER
// router (bypassing auth per D-14 exempt-path list). The server registers BOTH:
//   outer.Get("/api/version", adapter.HandleVersion)   // exempt from auth
//   outer.Route("/api", func(r) { r.Use(auth); r.Mount("/", adapter.Router()) })
//   ^^ /version inside Router() shadows but chi most-specific-wins makes outer reg take precedence
//
// Alternative: don't register /version on adapter.Router() at all — only expose
// HandleVersion as a public method and let the server register both routes itself.
// Planner picks; RESEARCH.md notes both are acceptable.
func (a *Adapter) HandleVersion(w http.ResponseWriter, r *http.Request) {
    a.handleVersion(w, r)
}
```

---

### NEW: `internal/adapter/ollama/wire.go` (utility, wire ↔ canonical)

**Analog (exact, sibling):** `internal/acp/translate.go` — same wire-struct + per-kind
mapper pattern (`wireBlock` ↔ `canonical.Block`). Apply the same discipline: JSON tags
ONLY on the wire structs; canonical types stay JSON-tag-free.

**Pattern (from `internal/acp/translate.go` lines 84-138 — sibling) + Node
`buildAcpBlocksFromOllama`/`chunksToOllamaMessage` (behavioural):**
```go
package ollama

import (
    "loop24-gateway/internal/canonical"
)

// --- wire (request) ---

// ollamaChatRequest is the Ollama-shaped /api/chat request body.
// JSON tags live here; canonical types stay JSON-tag-free (D-11).
type ollamaChatRequest struct {
    Model     string            `json:"model"`
    Messages  []ollamaMessage   `json:"messages"`
    Tools     []ollamaToolSpec  `json:"tools"`
    Format    json.RawMessage   `json:"format"`    // string OR object — keep raw
    Stream    bool              `json:"stream"`
    Think     bool              `json:"think"`
    KeepAlive string            `json:"keep_alive"` // ACCEPTED-AND-IGNORED
    Options   json.RawMessage   `json:"options"`    // ACCEPTED-AND-IGNORED
}

type ollamaMessage struct {
    Role      string         `json:"role"`
    Content   string         `json:"content"` // Ollama content is plain string (not array)
    Images    []string       `json:"images"`  // base64 per Phase 2 D-09
    ToolCalls []ollamaToolCall `json:"tool_calls"`
}

// --- wire (response) ---

// ollamaChatResponse is the Ollama-shaped /api/chat response body for stream:false.
type ollamaChatResponse struct {
    Model              string         `json:"model"`
    CreatedAt          string         `json:"created_at"`           // time.RFC3339Nano UTC
    Message            ollamaMessage  `json:"message"`
    Done               bool           `json:"done"`                  // always true (stream:false)
    DoneReason         string         `json:"done_reason"`           // "stop" | "length" | "tool_calls"
    TotalDuration      int64          `json:"total_duration"`        // nanoseconds
    LoadDuration       int64          `json:"load_duration"`         // always 0 for now
    PromptEvalCount    int            `json:"prompt_eval_count"`     // Math.ceil(len/4)
    PromptEvalDuration int64          `json:"prompt_eval_duration"`  // 15% of total
    EvalCount          int            `json:"eval_count"`            // Math.ceil(len/4)
    EvalDuration       int64          `json:"eval_duration"`         // 85% of total
}

// --- mapping (wire → canonical) ---

func wireToChatRequest(w *ollamaChatRequest, r *http.Request) (*canonical.ChatRequest, error) {
    req := &canonical.ChatRequest{
        Model:              w.Model,
        Stream:             w.Stream,
        Think:              w.Think,
        WorkingDirOverride: r.Header.Get("X-Working-Dir"),
    }
    // System extraction (mirrors Node line 486: messages.find(role==='system'))
    for _, m := range w.Messages {
        if m.Role == "system" {
            req.System = m.Content
            continue
        }
        req.Messages = append(req.Messages, canonical.Message{
            Role:    parseRole(m.Role),
            Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: m.Content}},
        })
        // Images Phase 2: defer per Assumption A4 + canonical.Block image variant TBD.
    }
    return req, nil
}
```

**Critical:** the `wire.go` file does NOT import `internal/engine`. It only translates
wire ↔ canonical. The handler (`handlers.go`) is the layer that pulls in `Engine` via
the adapter's `Config.Engine` field.

---

### NEW: `internal/adapter/ollama/handlers.go` (controller, request-response)

**Analog (exact, sibling):** `internal/server/health.go` lines 51-84 — handler shape
(`w.Header().Set` → `WriteHeader` → `json.NewEncoder(w).Encode`). RESEARCH.md §"Ollama
/api/chat Handler Skeleton" lines 1163-1195 has the full chat-handler shape.

**Pattern (RESEARCH.md §1163-1195 + Phase 1 health.go):**
```go
package ollama

import (
    "encoding/json"
    "net/http"
    "time"
)

func (a *Adapter) handleChat(w http.ResponseWriter, r *http.Request) {
    r.Body = http.MaxBytesReader(w, r.Body, 8<<20)  // 8 MB cap (Node parity)
    var wire ollamaChatRequest
    if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
        a.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
        return
    }
    if len(wire.Messages) == 0 {
        a.writeError(w, http.StatusBadRequest, "`messages` is required and must be a non-empty array")
        return
    }
    if wire.Stream {  // Phase 2 silent-downgrade per Node parity
        wire.Stream = false
    }
    req, err := wireToChatRequest(&wire, r)
    if err != nil {
        a.writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    start := time.Now()
    resp, err := a.cfg.Engine.Collect(r.Context(), req)
    if err != nil {
        a.writeError(w, http.StatusInternalServerError, err.Error())
        return
    }
    a.writeJSON(w, chatResponseToWire(resp, start, wire.Model))
}

func (a *Adapter) writeJSON(w http.ResponseWriter, v any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    if err := json.NewEncoder(w).Encode(v); err != nil {
        a.cfg.Logger.Error("ollama: encode", "err", err)
    }
}

func (a *Adapter) writeError(w http.ResponseWriter, status int, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
```

**Other handlers** (`handleGenerate`, `handleTags`, `handleShow`, `handlePS`,
`handleVersion`) follow the same shape. Wire shapes from RESEARCH.md §"Ollama Wire
Shapes" (lines 1197-1397) + Node reference (`toOllamaModel` at line 735, `/api/ps` at
line 778, `/api/show` at line 761, `/api/version` at line 712).

---

### NEW: `internal/adapter/ollama/render.go` (utility, response builder)

**Analog (Node behavioural):** `makeStats` (`acp-ollama-server.js:600-611`) and
`chunksToOllamaMessage` (`acp-ollama-server.js:559-595`). Phase 2 Go port — note the
adapter is downstream of `engine.Collect`, which already aggregates chunks → text. So
`chunksToOllamaMessage` is largely a NO-OP for Phase 2 (just wraps the text in the
message struct).

**Pattern:**
```go
package ollama

import (
    "math"
    "time"

    "loop24-gateway/internal/canonical"
)

// chatResponseToWire maps a canonical.ChatResponse + timing context to the Ollama wire
// shape. Mirrors Node makeStats + chunksToOllamaMessage at lines 559-611.
func chatResponseToWire(resp *canonical.ChatResponse, start time.Time, requestedModel string) ollamaChatResponse {
    text := joinTextContent(resp.Message.Content)
    totalNs := time.Since(start).Nanoseconds()
    promptText := /* derived from resp.Usage.InputTokens * 4, or recomputed from req — planner picks */ ""
    return ollamaChatResponse{
        Model:              requestedModel,                              // echo what client sent
        CreatedAt:          time.Now().UTC().Format(time.RFC3339Nano),
        Message:            ollamaMessage{Role: "assistant", Content: text},
        Done:               true,
        DoneReason:         mapStopReason(resp.StopReason),
        TotalDuration:      totalNs,
        LoadDuration:       0,
        PromptEvalCount:    estimateTokens(promptText),
        PromptEvalDuration: int64(math.Floor(float64(totalNs) * 0.15)),
        EvalCount:          estimateTokens(text),
        EvalDuration:       int64(math.Floor(float64(totalNs) * 0.85)),
    }
}

// estimateTokens — Math.ceil(len/4) mirror of Node line 80-83.
func estimateTokens(text string) int {
    if text == "" { return 0 }
    return (len(text) + 3) / 4
}

// mapStopReason: canonical.StopReason → Ollama "done_reason" string.
// canonical → ollama
//   StopEndTurn         → "stop"
//   StopMaxTokens       → "length"
//   StopMaxTurnRequests → "stop"
//   StopRefusal         → "stop"
//   StopCancelled       → "stop"
//   StopUnknown         → "stop"  (default; never block on missing data)
// Phase 6 may add "tool_calls" mapping.
func mapStopReason(r canonical.StopReason) string {
    switch r {
    case canonical.StopMaxTokens:
        return "length"
    default:
        return "stop"
    }
}
```

---

### NEW: `internal/adapter/ollama/stub.go` (controller, stub handlers)

**Analog (Node behavioural):** `acp-ollama-server.js:1006-1029` (stubStreaming,
`/api/pull`, `/api/push`, `/api/create`, `/api/copy`, `/api/delete`).

**Pattern (Phase 2 D-15 — Ollama-shape success):**
```go
// handlePull: honors stream:true (default) by emitting NDJSON; stream:false → JSON.
// Mirrors Node lines 1014-1023.
func (a *Adapter) handlePull(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Stream *bool `json:"stream"`
    }
    _ = json.NewDecoder(r.Body).Decode(&req)  // empty body is fine
    stream := true  // Ollama default
    if req.Stream != nil { stream = *req.Stream }
    if stream {
        a.writeNDJSON(w, []map[string]string{
            {"status": "pulling manifest"},
            {"status": "success"},
        })
        return
    }
    a.writeJSON(w, map[string]string{"status": "success"})
}

// handleCopy + handleDelete: empty object response (Node lines 1028-1029).
func (a *Adapter) handleCopy(w http.ResponseWriter, _ *http.Request)   { a.writeJSON(w, map[string]any{}) }
func (a *Adapter) handleDelete(w http.ResponseWriter, _ *http.Request) { a.writeJSON(w, map[string]any{}) }

// writeNDJSON: Content-Type application/x-ndjson; Transfer-Encoding chunked.
// Matches Node setNdjsonHeaders + ndjsonLine at lines 615 + 654-659.
func (a *Adapter) writeNDJSON(w http.ResponseWriter, lines []map[string]string) {
    w.Header().Set("Content-Type", "application/x-ndjson")
    w.Header().Set("Transfer-Encoding", "chunked")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("X-Accel-Buffering", "no")
    enc := json.NewEncoder(w)
    for _, l := range lines {
        if err := enc.Encode(l); err != nil {
            a.cfg.Logger.Error("ollama: ndjson encode", "err", err)
            return
        }
    }
}
```

**Critical Pitfall 5 (RESEARCH.md):** `/api/pull` MUST default `stream:true` and emit
NDJSON when stream isn't explicitly false. LangFlow sends stream:true; flow hangs if we
return JSON. Match Node verbatim.

---

### NEW: `internal/{engine,pool}/testmain_test.go` (test, goleak gate)

**Analog (exact, sibling):** `internal/acp/testmain_test.go` — verbatim copy with
package rename.

**Pattern (copy from `internal/acp/testmain_test.go`):**
```go
package engine  // OR: package pool — adjust per file

import (
    "testing"

    "go.uber.org/goleak"
)

func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
    // If unexpected goroutine leaks appear, diagnose before suppressing.
}
```

---

### NEW: `internal/engine/pickcwd_test.go` (test, property + table)

**Analog (partial — property test pattern from RESEARCH.md; table-driven from
`internal/acp/translate_test.go`):**

**Property test pattern (RESEARCH.md §"Property Test for pickCwd" lines 1113-1147):**
```go
package engine

import (
    "testing"
    "testing/quick"

    "loop24-gateway/internal/canonical"
)

func TestPickCwd_NeverPanics(t *testing.T) {
    f := func(override string, uris []string) bool {
        req := &canonical.ChatRequest{WorkingDirOverride: override}
        // construct synthetic resource_link blocks from uris...
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("pickCwd panicked: override=%q uris=%v r=%v", override, uris, r)
            }
        }()
        _ = pickCwd(req, "/tmp")
        return true
    }
    if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
        t.Error(err)
    }
}

// Fixed cases for cases testing/quick won't generate (Windows paths).
func TestPickCwd_WindowsFileURI(t *testing.T) {
    // ... assert /C:/foo → C:\foo on Windows ...
}
```

**Table-driven pattern (mirror `internal/acp/translate_test.go`):**
```go
func TestPickCwd_Priority(t *testing.T) {
    tests := []struct {
        name       string
        req        *canonical.ChatRequest
        defaultCwd string
        want       string
    }{
        {"override wins", &canonical.ChatRequest{WorkingDirOverride: "/explicit"}, "/tmp", "/explicit"},
        {"default fallback", &canonical.ChatRequest{}, "/tmp", "/tmp"},
        // ... add common-parent cases ...
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            if got := pickCwd(tt.req, tt.defaultCwd); got != tt.want {
                t.Errorf("got %q, want %q", got, tt.want)
            }
        })
    }
}
```

---

### NEW: `internal/adapter/ollama/integration_test.go` (test, blackbox real-kiro)

**Analog (exact, sibling):** `internal/acp/integration_test.go` — same
`resolveKiroCLI(t)` auto-skip pattern from Phase 1 D-17.

**Pattern:** blackbox package, gate at function start.
```go
package ollama_test  // blackbox

import (
    "context"
    "net/http"
    "net/http/httptest"
    "os"
    "os/exec"
    "testing"
)

func resolveKiroCLI(t *testing.T) string {
    t.Helper()
    if bin := os.Getenv("LOOP24_KIRO_BIN"); bin != "" { return bin }
    p, err := exec.LookPath("kiro-cli")
    if err != nil { t.Skip("kiro-cli not found on PATH; set LOOP24_KIRO_BIN to override") }
    return p
}

func TestIntegration_ChatEndToEnd(t *testing.T) {
    bin := resolveKiroCLI(t)
    // Spin up a real pool with size=1 + real engine + real adapter
    // POST /api/chat with a tiny message; assert 200 + non-empty content.
    // ...
}
```

---

### NEW: `internal/adapter/ollama/handlers_test.go` (test, whitebox + fake engine)

**Analog (role-match):** `internal/server/server_test.go` — `httptest.NewRecorder` +
direct `s.ServeHTTP(w, r)` pattern. Phase 2 uses a fake engine (controllable
`<-chan canonical.Chunk` + canned ChatResponse) per "Phase 2's adapter-handler tests
should use a fake engine, not a fake ACP" (CONTEXT.md `<specifics>` line 482).

**Pattern (mirror `internal/server/server_test.go` shape):**
```go
package ollama  // whitebox so we can use fakeEngine inside the package

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "loop24-gateway/internal/canonical"
    "loop24-gateway/internal/testutil"
)

type fakeEngine struct {
    resp *canonical.ChatResponse
    err  error
}

func (f *fakeEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
    return f.resp, f.err
}

func TestHandleChat_Happy(t *testing.T) {
    a := New(Config{
        Logger:       testutil.Logger(t),
        Engine:       &fakeEngine{resp: &canonical.ChatResponse{
            Message: canonical.Message{
                Role: canonical.RoleAssistant,
                Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "hi"}},
            },
            StopReason: canonical.StopEndTurn,
        }},
        ModelCatalog: fakeCatalog{},
    })
    req := httptest.NewRequest("POST", "/chat",
        strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`))
    w := httptest.NewRecorder()
    a.Router().ServeHTTP(w, req)
    if w.Code != 200 { t.Fatalf("status %d", w.Code) }
    var resp ollamaChatResponse
    _ = json.NewDecoder(w.Body).Decode(&resp)
    if resp.Message.Content != "hi" { t.Errorf("content %q", resp.Message.Content) }
}
```

---

### MODIFY: `internal/canonical/chunk.go`

**Analog (self):** lines 89-99 — `ResourceLinkBlock` already has `Name` field (added in
Phase 1.1 per chunk.go line 96). **No change required for Phase 2** unless Phase 1.1
shipped without it; verify.

---

### MODIFY: `internal/config/config.go`

**Analog (self):** existing structure (lines 17-72) is the pattern. Add new fields +
extend `Load()` body using existing helpers.

**Pattern (self-extension):**
```go
// Add to Config struct (after PingInterval):
AuthToken         []string       // from AUTH_TOKEN (comma-split)
AllowedIPs        []netip.Prefix // from ALLOWED_IPS (comma-split; per CIDR/IP)
PoolSize          int            // from POOL_SIZE (default 1)
OllamaPathPrefix  string         // from OLLAMA_PATH_PREFIX (default "/api")
OpenAIPathPrefix  string         // from OPENAI_PATH_PREFIX (default "/v1", unused in P2)

// Add to Load() body:
authTokens := getEnvStrSliceComma("AUTH_TOKEN", nil)                          // empty = no auth
allowedIPs, err := parseCIDRs(getEnvStrSliceComma("ALLOWED_IPS", nil))         // empty = allow-all
if err != nil { errs = append(errs, err) }
poolSize, err := getEnvInt("POOL_SIZE", 1)
if err != nil { errs = append(errs, err) }
ollamaPath := getEnvStr("OLLAMA_PATH_PREFIX", "/api")
openaiPath := getEnvStr("OPENAI_PATH_PREFIX", "/v1")
```

**Critical:** `getEnvStrSliceComma` is a new helper (split on `,` not whitespace; current
`getEnvStrSlice` splits on whitespace for `KIRO_ARGS`). Add it next to the existing
helpers. `parseCIDRs` is also new — uses `netip.ParsePrefix` and falls back to
`netip.ParseAddr` for bare IPs.

**Gotcha — `getEnvInt`:** doesn't exist yet (only `getEnvBool` / `getEnvDuration`). Add
it following the same shape (return `(int, error)` for set-but-invalid).

---

### MODIFY: `internal/server/server.go`

**Analog (self):** existing constructor lines 35-59 — extend to take auth tokens,
allowed IPs, and the Ollama sub-router. Apply RESEARCH.md Pattern 1 §"chi.Route" pattern
inside the constructor.

**Pattern (self-extension):**
```go
type Config struct {  // server config grows — replace plain args
    Logger          *slog.Logger
    Version         string
    Commit          string
    HTTPAddr        string
    AuthTokens      []string
    AllowedPrefixes []netip.Prefix
    OllamaPath      string         // from cfg.OllamaPathPrefix
    OllamaRouter    chi.Router     // from ollama.Adapter.Router()
    OllamaVersion   http.HandlerFunc // from ollama.Adapter.HandleVersion — registered EXEMPT
    Pool            PoolStatsSource  // for /health (interface satisfied by *pool.Pool)
}

func New(cfg Config) *Server {
    s := &Server{ /* ... */ }
    s.router = chi.NewRouter()
    s.router.Use(middleware.RequestID)
    s.router.Use(middleware.Recoverer)
    s.router.Use(accessLog(cfg.Logger))

    // Exempt routes — registered on OUTER router (bypass auth + IP allowlist).
    s.router.Get("/", s.rootHandler)
    s.router.Get("/health", s.healthHandler)
    s.router.Get(cfg.OllamaPath+"/version", cfg.OllamaVersion)  // /api/version exempt

    // Protected sub-tree — mount the Ollama adapter router with auth scoped here.
    s.router.Route(cfg.OllamaPath, func(r chi.Router) {
        r.Use(auth.Bearer(auth.Config{Tokens: cfg.AuthTokens, Logger: cfg.Logger}))
        r.Use(auth.IPAllowlist(auth.Config{AllowedPrefixes: cfg.AllowedPrefixes, Logger: cfg.Logger}))
        r.Mount("/", cfg.OllamaRouter)
    })
    return s
}
```

**Critical — chi route precedence:** the OUTER `r.Get("/api/version", ...)` wins over a
hypothetical `r.Get("/version", ...)` registered inside the `Route("/api", ...)` block.
Confirmed by RESEARCH.md §"Pattern 1" critical note line 415-420.

**`PoolStatsSource` interface:** server defines a consumer interface
`interface { Stats() pool.Stats }` — but importing `internal/pool` from `internal/server`
creates a dep cycle since pool may import canonical too… Actually it's fine; pool ↔
server is acyclic. Verify `go-arch-lint` permits `server → pool` (it should — server is
the wiring layer).

**Alternative:** the `Stats` struct is small enough to declare directly on the
server-side interface using primitive types (`Size int; Alive int; Busy int`) — that
avoids the cross-package type leak. Planner picks.

---

### MODIFY: `internal/server/health.go`

**Analog (self):** existing `healthHandler` lines 51-66 — wire `pool.Stats()` into the
`PoolStats` struct.

**Pattern (self-extension):**
```go
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
    var ps PoolStats
    if s.pool != nil {  // pool is optional; main.go skips it when KIRO_CMD unset
        st := s.pool.Stats()
        ps = PoolStats{Size: st.Size, Alive: st.Alive, Busy: st.Busy}
    }
    resp := HealthResponse{
        Status:        "ok",
        Version:       s.version,
        UptimeSeconds: time.Since(s.start).Seconds(),
        Pool:          ps,
        // Sessions, Embeddings still zero — Phase 5 / 7.
    }
    // ... existing encode pattern unchanged ...
}
```

---

### MODIFY: `cmd/loop24-gateway/main.go`

**Analog (self):** existing wiring (lines 22-62) is the skeleton. Extend with pool +
engine + ollama adapter wiring per CONTEXT.md §"Integration Points" lines 430-440.

**Pattern (full rewrite — mirror Phase 1 D-15 explicit-injection discipline):**
```go
func main() {
    cfg, err := config.Load()
    if err != nil {
        slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("config load failed", "err", err)
        os.Exit(1)
    }
    logger := buildLogger(cfg)

    ctx := context.Background()

    // Pool: warmup-before-listen (POOL-02). Skip if KIRO_CMD unset (Phase 1 review fix).
    var poolPtr *pool.Pool
    if cfg.KiroCmd != "" {
        poolPtr = pool.New(pool.Config{
            Logger:   logger,
            Size:     cfg.PoolSize,
            KiroCmd:  cfg.KiroCmd,
            KiroArgs: cfg.KiroArgs,
            KiroCWD:  cfg.KiroCWD,
            PingInterval: cfg.PingInterval,
        })
        if err := poolPtr.Warmup(ctx); err != nil {
            logger.Error("pool warmup failed", "err", err)
            os.Exit(1)
        }
        defer func() {
            if err := poolPtr.Close(); err != nil { logger.Error("pool close", "err", err) }
        }()
    }

    // Engine: requires ACP. If pool isn't built (KIRO_CMD unset), engine is also nil and
    // adapter handlers return 503 for chat/generate (defer to planner: gracefully degrade
    // OR refuse to start without KIRO_CMD).
    var eng *engine.Engine
    if poolPtr != nil {
        eng = engine.New(engine.Config{Logger: logger, ACP: poolPtr, DefaultCWD: cfg.KiroCWD})
    }

    // Ollama adapter
    ollamaAdapter := ollama.New(ollama.Config{
        Logger:       logger,
        Engine:       eng,
        ModelCatalog: poolPtr,
        Version:      version.Version,
        Commit:       version.Commit(),
    })

    // Server
    srv := server.New(server.Config{
        Logger:          logger,
        Version:         version.Version,
        Commit:          version.Commit(),
        HTTPAddr:        cfg.HTTPAddr,
        AuthTokens:      cfg.AuthToken,
        AllowedPrefixes: cfg.AllowedIPs,
        OllamaPath:      cfg.OllamaPathPrefix,
        OllamaRouter:    ollamaAdapter.Router(),
        OllamaVersion:   ollamaAdapter.HandleVersion,
        Pool:            poolPtr,
    })

    if err := srv.RunUntilSignal(ctx); err != nil {
        logger.Error("server stopped with error", "err", err)
        os.Exit(1)
    }
}
```

**Critical:** preserve Phase 1's `cfg.KiroCmd != ""` conditional — `/health` must start
on machines without kiro-cli (Phase 1 review fix from existing main.go comment lines
32-35).

---

## Shared Patterns

### Structured logging — explicit injection (D-15)

**Source:** Phase 1 PATTERNS.md + `cmd/loop24-gateway/main.go` lines 64-70 + every
existing `Config` struct.

**Apply to:** All new packages (`engine`, `pool`, `auth`, `adapter/ollama`).

```go
// Construction (main.go only):
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel()}))

// Injection (every package Config):
engine.New(engine.Config{Logger: logger, ...})
pool.New(pool.Config{Logger: logger, ...})
ollama.New(ollama.Config{Logger: logger, ...})
auth.Bearer(auth.Config{Logger: logger, ...})

// In tests (every _test.go):
logger := testutil.Logger(t)

// NEVER:
slog.SetDefault(logger)  // D-15 banned
```

### Error wrapping — `fmt.Errorf` with `%w`

**Source:** every existing `internal/acp/client.go` error path (e.g., lines 226, 232,
399, 467, 519).

**Apply to:** Every cross-package error return.

```go
// CORRECT:
return nil, fmt.Errorf("pool: warmup slot %d: %w", i, err)
return nil, fmt.Errorf("engine: prompt: %w", err)
// WRONG (wrapcheck linter flags):
return nil, err
```

### Context as first argument

**Apply to:** Every exported method that does I/O, blocks, or calls into a subprocess —
engine.Run, engine.Collect, pool.Warmup, pool.NewSession, pool.SetModel, pool.Prompt.

```go
// CORRECT:
func (e *Engine) Run(ctx context.Context, req *canonical.ChatRequest) (*Run, error) { ... }
// WRONG:
func (e *Engine) Run(req *canonical.ChatRequest) (*Run, error) { ... }
```

### `goleak.VerifyTestMain` per test package

**Source:** `internal/acp/testmain_test.go` — verbatim copy with package rename.

**Apply to:** Every new internal package (`engine`, `pool`). NOT needed for `auth` /
`canonical` / `adapter/ollama` if they spawn no goroutines, but adding it is cheap
insurance.

### `chan *Slot` / `sync.Mutex` / `sync.Once` primitives

**Source:** `internal/acp/client.go` lines 178-205 (Client struct field declarations) +
lines 746-785 (Close idempotency via `sync.Once`).

**Apply to:** `internal/pool/pool.go` — same `sync.Mutex`-guarded slice + `sync.Once`-guarded close.

### Constant-time bearer comparison

**Source:** RESEARCH.md Pattern 3 + Go stdlib `crypto/subtle.ConstantTimeCompare`.

**Apply to:** `internal/auth/bearer.go` ONLY (no other Phase 2 code compares secrets).

### `wireFoo` struct + `translateFoo` function pattern

**Source:** `internal/acp/translate.go` lines 84-138.

**Apply to:** `internal/adapter/ollama/wire.go` — same JSON-tagged wire structs + per-kind
translator functions. Adapter layer is functionally identical to ACP's wire-translation
layer, just for HTTP↔canonical instead of JSON-RPC↔canonical.

### Goroutine + lifecycle context separation

**Source:** `internal/acp/client.go` lines 296-336 + Codex CR-03 fix (defer
`c.cancel()` from readLoop). Phase 2 pool may not need its own goroutines (slot is just
a connection holder), but if it grows one for dead-slot detection later, follow the same
shape.

### Example_ runnable godoc (TRST-07)

**Source:** Phase 1.1 `internal/acp/translate_test.go` (`Example_translateUpdate`).

**Apply to:** `internal/engine/pickcwd_test.go` + `internal/engine/build_acp_test.go`
(TRST-07 — RESEARCH.md says "Phase 2 adds for `pickCwd` and `buildAcpBlocks`"). Runnable
godoc means the function name is `func ExamplePickCwd()` (no `_t`) and has a `// Output:`
comment block that `go test` validates.

---

## Boundary Rules (TRST-04)

Phase 2 activates the `go-arch-lint` rules that Phase 1 scaffolded
(`.go-arch-lint.yml`). Verify the rules permit:

```
canonical: {}                         # imports nothing under internal/
acp:
  mayDependOn: [canonical, version]
engine:
  mayDependOn: [canonical]            # engine declares ACPClient/Stream interfaces
pool:
  mayDependOn: [canonical, acp]       # pool wraps *acp.Client; structurally satisfies engine.ACPClient
auth:
  mayDependOn: []                     # pure middleware; only stdlib + slog
adapter/ollama:
  mayDependOn: [canonical, version]   # NEVER engine — uses consumer-defined Engine interface
server:
  mayDependOn: [canonical, config, version, pool, auth, adapter/ollama]
```

**Critical:** `adapter/ollama → engine` is FORBIDDEN. The adapter declares its own
`Engine` interface in `adapter.go`. The concrete `*engine.Engine` from `internal/engine`
structurally satisfies it; wiring happens in `main.go`. This is the SAME pattern Phase
1.1 used for `engine.ACPClient`. If you find yourself adding the import, you've drifted
from the design.

**Verify before Phase 2 task 1:** run `go-arch-lint check` against the empty Phase 2
package scaffold to confirm the rules are actually live (Phase 1 plan 01-03 was supposed
to wire them in).

---

## No Analog Found

All 28 files have at least one strong analog. The "RESEARCH.md only" entries (auth
bearer / IP allowlist, hooks interface, ollama wire shapes) are still strong patterns —
RESEARCH.md verified them against Node + Ollama API doc + Go stdlib docs on 2026-05-23.

| File | Reason for "RESEARCH.md only" rather than in-repo |
|------|---------------------------------------------------|
| `internal/auth/bearer.go` | No middleware exists yet in repo; pattern is RESEARCH.md Pattern 3 |
| `internal/auth/ipallowlist.go` | Same; pattern is RESEARCH.md Pattern 4 |
| `internal/engine/hooks.go` | PreHook/PostHook interfaces are first-instantiated here |
| `internal/adapter/ollama/render.go` | Token-estimation + duration-split is Node-specific |

---

## Metadata

**Analog search scope:**
- `/Users/coreyellis/Projects/repos/local/loop24-gateway/internal/{acp,canonical,config,server,version,testutil}/` (Phase 1 + 1.1 in-repo Go)
- `/Users/coreyellis/Projects/repos/local/loop24-gateway/cmd/loop24-gateway/main.go` (Phase 1 wiring)
- `/Users/coreyellis/Projects/repos/local/loop24-gateway/.planning/phases/01.1-acp-wire-alignment/01.1-PATTERNS.md` (Phase 1.1 pattern map)
- `/Users/coreyellis/Projects/repos/gitlab.rosetta.ericssondevops.com/loop_24/acp_server/acp-ollama-server.js` (Node behavioural reference)
- `/Users/coreyellis/Projects/repos/local/bifrost/transports/bifrost-http/integrations/openai.go` (Bifrost adapter-over-canonical layout reference)
- `/Users/coreyellis/Projects/repos/local/bifrost/core/providers/ollama/ollama.go` (Bifrost Ollama provider reference)

**Files scanned:** 14 (in-repo Go) + 1 (Node) + 2 (Bifrost) + 2 (Phase 2 CONTEXT/RESEARCH)
**Pattern extraction date:** 2026-05-23
