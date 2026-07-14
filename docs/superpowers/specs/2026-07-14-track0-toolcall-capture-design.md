# Track 0 — kiro tool-call wire capture — design

**Date:** 2026-07-14
**Status:** Approved (design) — ready to implement.
**Scope:** One implementation plan (multi-commit, TDD). Track 0 of the
[legacy-gateway parity roadmap](../2026-07-14-legacy-gateway-parity-roadmap.md).
**Goal:** capture kiro's actual tool-call wire behavior *through the real
gateway path* so **Track 3 (tool-call robustness)** is scoped to what kiro
genuinely does — not to Node quirks that may never bite this gateway.

## Why (and what changed)

Track 0 originally had two jobs: confirm the context-utilization signal, and
capture kiro's tool-call wire shape. **Job 1 is done** — the 2026-07-14 live
capture confirmed `_kiro.dev/metadata` and shipped Track 2 + the usage metrics.
Track 0 now narrows to **job 2 only**.

The roadmap's guiding principle is *validate before building*: whether Go's
free-text coercion gaps (embedded `{"tool_call":…}` scan, truncated-JSON repair,
invented-name remap, native-call→structured surfacing) matter depends on kiro's
actual output **to this gateway** — native ACP `tool_call` notifications vs
free-text JSON in assistant text, and whether kiro echoes the client's declared
tool names or its own built-ins. We must observe before hardening.

The obstacle: **the gateway is a black box to clients.** A client on
`/v1/messages` or `/api/chat` never sees kiro's raw ACP frames — those live
inside the acp layer. So Track 0 needs a gateway-side capture affordance,
reachable by an out-of-process (possibly remote) harness/skill.

## Decisions (locked with the human)

- **Capture mechanism:** gateway raw-frame capture (observe the exact production
  code path), not a direct-kiro harness or client-only black-box.
- **Affordance:** a bounded, capture-flag-gated **admin ring-buffer endpoint**
  returning raw frames as JSON — reachable over HTTP so a desktop skill can read
  it even when the gateway is on a remote box (e.g. the Windows box). Chosen over
  a DEBUG-log (host-filesystem-bound) or chat-trace file (same problem + feature
  coupling).
- **Deliverable scope in this repo:** the capture endpoint **and** a repo-local
  harness that drives the round-trips and classifies. Track 0 is fully runnable
  from this repo — it produces the Track 3 scoping findings without waiting on
  the Co-Worker desktop app, which later wraps the same documented procedure.
- **Fidelity over minimalism:** capture is wired through **both** the pool
  (stateless) and the session registry (stateful) paths, mirroring the metrics
  recorder wiring. The harness is a **real-kiro integration test** (not in normal
  CI).

## Architecture

Capture reuses the exact hook + shared-recorder boundary the kiro usage-metrics
build established, so acp/pool/session stay free of a new import and the admin
endpoint reads through a consumer-interface seam (TRST-04 preserved).

### 1. acp layer — raw-frame hook

New optional `acp.Config` hook, same pattern as `OnTurnMeter`/`OnContextPct`:

```go
// OnRawFrame, when set, is fired by readLoop for every inbound kiro frame
// (after a successful json.Unmarshal, before dispatch) with the frame's method
// and raw params. Used by the Track 0 capture ring; nil = no capture (one nil
// check on the hot path, zero cost when disabled).
OnRawFrame func(method string, params json.RawMessage)
```

Fired in `client.readLoop` right after the frame unmarshals, capturing **every**
inbound frame (notifications *and* responses). This is deliberate: native tool
calls ride `session/update` notifications, but a *free-text* `{"tool_call":…}`
hides inside `agent_message_chunk` text — capturing all frames sees both. The
hook receives the raw params bytes; truncation is the ring's concern.

### 2. `internal/capture` — the ring buffer (new standalone package)

```go
type Frame struct {
    Seq    uint64    // monotonic per-ring sequence
    Ts     time.Time // wall-clock capture time
    Method string
    Params string    // raw params, truncated to CapBytes (UTF-8-safe)
    Bytes  int       // original params length before truncation
}

type Ring struct { /* mutex-guarded fixed-size ring */ }
func NewRing(size, capBytes int) *Ring
func (r *Ring) Record(method string, params json.RawMessage) // hot path: append under mu
func (r *Ring) Snapshot() []Frame                            // oldest→newest copy
```

Standalone — no internal deps (like `internal/version`), so acp never imports it
(it only calls the func hook), and admin reads it via a consumer interface. Ring
overwrites oldest on overflow. Per-frame params truncated to `CapBytes`
(default 8 KiB — enough to see a tool_call structure) on a UTF-8 rune boundary
(reuse the pattern in `stderrDrainLoop`); `Bytes` records the pre-truncation size
so an operator sees when a frame was clipped.

### 3. pool + session — forward the hook

`pool.Config` and `session.Config` each gain an optional
`Capture func(method string, params json.RawMessage)` field (or reuse a small
`FrameRecorder` interface — implementation choice at plan time; the func field is
simplest and matches nothing needing read-back at this layer). `acpSlotConfig`
(pool) and `createEntry` (session) set `acp.Config.OnRawFrame` to it when non-nil.
Same dual-wiring as `Metrics`.

### 4. admin — the endpoint

- New admin consumer seam: `AcpCaptureSource interface { Snapshot() []capture.Frame }`
  (admin already defines `PoolDetailSource`/`RegistryStatsSource` this way — admin
  does **not** import pool/session/capture as concrete deps; `cmd/otto-gateway`
  adapts at the boundary). *If* admin importing `internal/capture` (a leaf package)
  is cleaner than re-declaring `Frame`, that is acceptable and decided at plan
  time; the TRST-04 rule that matters is admin not importing pool/session/engine.
- `GET /admin/acp-capture` → `{"enabled":true,"size":N,"frames":[…]}` JSON,
  behind the existing `auth.IPAllowlist` and admin routing. When capture is
  disabled (no ring wired), returns `{"enabled":false,"frames":[]}` (200, not
  404, so the harness can distinguish "off" from "not found").

### 5. config

- `ACP_CAPTURE` (bool, default `false`) — enables the ring + endpoint wiring.
- `ACP_CAPTURE_SIZE` (int, default `512`) — ring capacity; `<= 0` is a boot error
  (fail-fast, matching the existing numeric-env posture).
- (`CapBytes` per-frame cap is a constant, 8 KiB — not env-exposed unless a need
  appears; YAGNI.)

### 6. `cmd/otto-gateway/main.go`

When `ACP_CAPTURE` is true: construct `ring := capture.NewRing(size, capBytes)`,
pass `ring.Record` into `pool.Config.Capture` + `session.Config.Capture`, and
register the admin endpoint with a `ring`-backed `AcpCaptureSource`. When false:
everything stays nil and the endpoint reports `enabled:false`.

### 7. Repo-local harness (`tests/` integration, real kiro)

A build-tag/env-gated Go test (e.g. `//go:build kirolive`, or skip unless
`KIRO_LIVE=1`) that assumes a gateway running with `ACP_CAPTURE=true` (or starts
one), then for **each surface**:

1. Sends a tool-declaring request that should trigger a call:
   - Anthropic `/v1/messages` — `tools:[{name:"get_weather",input_schema:…}]`.
   - OpenAI `/v1/chat/completions` — `tools:[{type:"function",function:{name:"get_weather",…}}]`.
   - Ollama `/api/chat` — `tools:[…]`.
   Prompt: e.g. *"What's the weather in Paris? Use the tool."*
2. `GET /admin/acp-capture`, then **classify** (see taxonomy below).
3. Writes a findings report to
   `docs/reviews/2026-07-14-track0-toolcall-findings.md` (JSON + prose).

The harness is the Track 0 *product*: its report is the Track 3 scoping input.
The Co-Worker desktop skill later automates the same three steps against a
deployed gateway.

## Classification taxonomy (Track 0 output → Track 3 scope)

For each surface, the harness records and the report answers:

1. **Transport** — did kiro emit a native ACP `tool_call` / `tool_call_chunk`
   `session/update`, a free-text `{"tool_call":…}` inside `agent_message_chunk`
   text, or both/mixed? → decides whether Track 3 needs the free-text extractor
   at all, and whether native calls must be surfaced as structured `tool_calls`.
2. **Tool-name fidelity** — is the emitted name the client's declared name
   (`get_weather`), kiro's `kind`, or an invented/built-in name? → decides whether
   a kiro→client name-reconciliation layer is needed (Node `519c066`).
3. **JSON robustness** — any truncated/unbalanced JSON, multiple tool_call
   objects in one message, or wrapper shapes? → decides truncated-JSON repair
   (Node `14bc655`) and balanced-brace multi-object scan (Node `extractToolCallObjects`).
4. **Per-surface deltas** — does `/v1/messages` differ from `/api/chat`/OpenAI?
   → decides whether Track 3 hardening is per-surface or shared in the engine.

Each answer is tagged `needed | not-needed | uncertain` against the specific
Track 3 sub-item, so Track 3 builds only what kiro requires.

## Security / privacy note

Capture mode records **raw prompt and response content** (potential PII) into an
in-memory ring. It is therefore: **off by default**, admin-only, IP-allowlisted,
bounded (size + per-frame byte cap), and never persisted to disk by the gateway.
It is a diagnostic mode, not an always-on production feature. Documented as such
in `docs/operating.md`.

## Tests (TDD)

- `internal/capture`: `Record`/`Snapshot` ordering, overflow overwrites oldest,
  per-frame truncation on a UTF-8 boundary with `Bytes` preserved, concurrent
  `Record` under `-race`.
- `internal/acp`: `OnRawFrame` fires for a notification frame with the raw method
  + params; nil hook is a no-op (no panic, no cost path taken).
- `internal/pool` + `internal/session`: the capture func is forwarded onto each
  slot/entry `acp.Config.OnRawFrame` when set; unset leaves it nil.
- `internal/admin`: `GET /admin/acp-capture` renders the snapshot as JSON;
  reports `enabled:false` when no ring is wired.
- `internal/config`: `ACP_CAPTURE` bool parse; `ACP_CAPTURE_SIZE` default 512,
  `<= 0` rejected.
- Harness (real-kiro, gated): drives each surface, reads the ring, asserts at
  least one frame captured, and emits the findings report.

## Files touched (anticipated)

- `internal/acp/client.go` (+ `Config`) — `OnRawFrame` hook + fire in `readLoop`.
- `internal/capture/ring.go` (new) + tests.
- `internal/pool/config.go` + `pool.go` — `Capture` field + forward in `acpSlotConfig`.
- `internal/session/config.go` + `registry.go` — `Capture` field + forward in `createEntry`.
- `internal/admin/*` — `AcpCaptureSource` seam + `GET /admin/acp-capture` handler.
- `internal/config/config.go` — `ACP_CAPTURE` (bool) + `ACP_CAPTURE_SIZE` (int).
- `cmd/otto-gateway/main.go` — construct the ring + wire when enabled.
- `tests/` — real-kiro capture+classify harness.
- `docs/operating.md` — document the capture mode + endpoint.
- `docs/reviews/2026-07-14-track0-toolcall-findings.md` — the harness output (the
  Track 3 scoping deliverable).

## Verification gates

`go build ./...`; `go test ./...`; gofumpt-clean; `go vet`; golangci-lint on
touched packages; `CGO_ENABLED=0 go build ./cmd/otto-gateway`; `GOOS=linux/windows`.
**Live:** run with `ACP_CAPTURE=true` + real kiro, drive a tool round-trip on each
surface, confirm `/admin/acp-capture` returns the raw frames, and produce the
tool-call findings report that scopes Track 3.

## Non-goals

- No Track 3 implementation — this only *scopes* it.
- No always-on capture, no disk persistence, no per-request correlation beyond the
  session id already on the frames (the harness drives one round-trip at a time).
- No new auth model — reuses the existing admin IP-allowlist.
