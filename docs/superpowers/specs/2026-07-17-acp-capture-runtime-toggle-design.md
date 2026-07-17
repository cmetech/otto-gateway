# ACP capture — runtime enable/disable toggle (design)

**Date:** 2026-07-17
**Status:** Approved design, ready for implementation planning
**Topic:** Make `ACP_CAPTURE` toggleable at runtime from the admin UI, without a gateway restart.

## Goal

An operator can turn raw ACP-frame capture on and off from the `/admin` dashboard
without restarting the gateway, gated behind an opt-in startup env so the
capability is only present where the operator allows it.

## Non-goals

- **No persistence** of the runtime on/off state. The env is the startup default;
  a restart resets to it.
- **No CSRF tokens / session machinery.** The admin surface stays a simple
  same-origin HTML+fetch surface (see Security).
- **No generic admin settings-mutation framework.** This adds exactly one scoped
  write endpoint for capture, not a toggle bus for DEBUG/chat-trace/etc.
- **No change to what is captured** or the frame wire shape.

## Current state (what exists today)

- `ACP_CAPTURE` (bool, default false) → `cfg.AcpCapture`; `ACP_CAPTURE_SIZE`
  (default 512) → `cfg.AcpCaptureSize` (`internal/config/config.go:201-207`,
  loaded ~`:566`/`:570`).
- `cmd/otto-gateway/main.go:418-481`: when `cfg.AcpCapture` is true, allocates
  `capture.NewRing(size, capBytes)` and wires it into the pool and session
  registry via `captureRecordFunc(ring)` (`main.go:949`). When capture is off the
  ring is nil, `captureRecordFunc` returns nil, and the ACP `OnRawFrame` hook
  (`internal/acp/client.go:87-92`, called at `:614-618` before dispatch) stays
  unset — **zero cost**.
- `internal/capture/ring.go`: `Ring` is a fixed-size, mutex-guarded circular
  buffer with `Record(method, params)` and `Snapshot()`.
- Admin: `GET /admin/api/acp-capture` (`internal/admin/admin.go:223`,
  `internal/admin/capture.go`) returns `{enabled, frames}`; `enabled` is
  `h.deps.AcpCapture != nil`. **The entire admin surface is `r.Get` only**
  (`admin.go:237` explicitly notes it "uses `r.Get` exclusively") — there are no
  mutation endpoints today.
- `/admin` is auth-exempt and not IP-allowlisted (`docs/operating.md`).
- `ACP_CAPTURE` is documented as **SENSITIVE** in the `/admin/docs` env table
  (`admin.go:585`) because frames contain prompt/response content.

## Design

### 1. Runtime model — always-wire + atomic gate

Represent capture with, alongside the existing ring:
- `enabled atomic.Bool` — whether recording is currently active.
- `allowRuntimeToggle bool` — whether the operator opted into runtime toggling.

Rules:
- The ring is allocated when `ACP_CAPTURE || ACP_CAPTURE_RUNTIME`.
- The `OnRawFrame` hook is wired whenever the ring exists. The hook records only
  when `enabled.Load()` is true. Cost when allocated-but-off is a single atomic
  load per frame (negligible). When neither env is set, the hook stays nil and
  cost is exactly zero, as today.
- `enabled` initial value = `cfg.AcpCapture`. So `ACP_CAPTURE=true` still starts
  capturing immediately; `ACP_CAPTURE_RUNTIME=true` is what permits *changing* it
  at runtime.

Startup matrix:

| `ACP_CAPTURE` | `ACP_CAPTURE_RUNTIME` | Ring | Hook | Initial state | UI toggle |
|---|---|---|---|---|---|
| false | false | none | nil | off (zero cost) | none — **today's behavior** |
| true | false | yes | wired | on, locked | none — **today's behavior** |
| false | true | yes | wired | off | shown |
| true | true | yes | wired | on | shown |

### 2. New env `ACP_CAPTURE_RUNTIME`

`internal/config/config.go`: parse `ACP_CAPTURE_RUNTIME` (bool, default false) →
`cfg.AcpCaptureRuntime`. Document it as SENSITIVE in the `/admin/docs` env table
(`admin.go`) next to `ACP_CAPTURE`, noting it permits enabling capture at runtime
from `/admin` with no restart.

### 3. `Ring.Clear()`

Add `func (r *Ring) Clear()` to `internal/capture/ring.go`: under the mutex, reset
`next`, `count` (and `seq`) so `Snapshot()` returns empty. The backing slice may be
retained (entries become unreachable via `count`).

### 4. Buffer lifecycle on toggle

- **enable** → `Clear()` then `enabled.Store(true)` (fresh buffer per capture
  session; no stale frames bleed into a repro).
- **disable** → `enabled.Store(false)`; buffer kept readable (disable-then-inspect).
- **clear** → `Clear()` (on-demand purge of sensitive frames).

### 5. Admin API

- **New** `POST /admin/api/acp-capture` with body `{"action":"enable"|"disable"|"clear"}`.
  - When `allowRuntimeToggle` is false → **HTTP 403** with an explanatory JSON body
    (endpoint exists but refuses to mutate unless the operator opted in).
  - Unknown/missing action → **HTTP 400**.
  - On success, apply the action and return the updated status JSON (same shape as
    the extended GET below).
  - This is the admin surface's first mutation route. Register it explicitly with
    `r.Post` next to the existing `r.Get("/api/acp-capture", …)`.
- **Extend** `GET /admin/api/acp-capture` response to
  `{enabled, allowRuntimeToggle, count, size, frames}` so the UI renders without
  guessing. `enabled` now reflects the runtime `enabled.Load()` (not merely
  ring-non-nil); `allowRuntimeToggle` drives whether the UI shows controls.

The admin layer reaches the capture state through a small interface (mirroring the
existing `AcpCaptureSource` boundary at `main.go:958-973`) exposing:
`Enabled() bool`, `AllowRuntimeToggle() bool`, `Count() int`, `Size() int`,
`Snapshot() []Frame`, and mutators `Enable()`, `Disable()`, `Clear()`. `main.go`
adapts the concrete capture controller to this interface. When neither env is set
there is no controller and the admin dep is nil — GET reports
`{enabled:false, allowRuntimeToggle:false, frames:[]}` (200, as today), and POST
returns 403.

### 6. UI

A small "ACP Capture (diagnostics)" panel on the admin dashboard
(`internal/admin` HTML template + `admin.js` + static assets):
- A status pill: on/off and the current frame count.
- When `allowRuntimeToggle` is true: an **on/off** button and a **Clear** button.
  Each issues the `POST` then refreshes the panel state.
- When false: read-only status only (as today).
- A link to the raw `GET /admin/api/acp-capture` JSON.
- The panel reads state from the existing 30s `admin.js` snapshot poll where
  possible; button clicks refresh immediately after the POST resolves.

## Security considerations

- **Opt-in gate.** Runtime toggling requires `ACP_CAPTURE_RUNTIME=true` at startup.
  Absent that, behavior is byte-for-byte today's: no UI toggle, POST returns 403.
  The operator decides once, at deploy, that this potentially-sensitive capability
  is allowed — consistent with the existing "capture is SENSITIVE, gate it" intent.
- **CSRF.** This is the first state-changing route on an unauthenticated,
  browser-reachable admin surface. For a localhost/firewalled diagnostic gated
  behind the opt-in env, a same-origin `POST` is accepted without CSRF tokens.
  This is a small, explicit new exposure; documented here rather than mitigated
  with session machinery. If `/admin` is ever exposed beyond localhost, the
  operator should not set `ACP_CAPTURE_RUNTIME=true`.
- **Sensitive content in memory.** Enabling auto-clears, disabling keeps the
  buffer, and the Clear button purges on demand. Frames remain memory-only and are
  lost on restart (unchanged).

## Testing

- `internal/capture`: `Ring.Clear()` empties the ring; `Snapshot()` post-clear is
  empty; a subsequent `Record` starts fresh.
- Capture controller / gate: records only when `enabled` is true; `Enable()`
  auto-clears; `Disable()` stops recording but preserves the buffer.
- `internal/admin`: `POST /admin/api/acp-capture` enable/disable/clear happy paths
  reflect in the subsequent GET; **403 when `allowRuntimeToggle` is false**; 400 on
  unknown action; GET returns the extended shape and reflects runtime `enabled`.
- `internal/config`: `ACP_CAPTURE_RUNTIME` parses (default false; true/1/etc.).
- Regression: with neither env set, the ACP `OnRawFrame` hook stays nil (zero-cost
  path preserved).

## File-by-file change list

- `internal/config/config.go` — parse `ACP_CAPTURE_RUNTIME` → `cfg.AcpCaptureRuntime`.
- `internal/capture/ring.go` — add `Clear()`; (optional) `Count()`/`Cap()` helpers.
- `internal/capture/` — a small controller type owning `*Ring` + `enabled atomic.Bool`
  + `allowRuntimeToggle`, with `Record`/`Enable`/`Disable`/`Clear`/`Enabled`/`Count`
  accessors (or fold onto an existing type if cleaner).
- `cmd/otto-gateway/main.go` — allocate ring when `AcpCapture || AcpCaptureRuntime`;
  wire the gated record func; build the controller; adapt it to the admin interface
  (extend `adminAcpCapture`).
- `internal/acp/client.go` — no change (hook already exists); the gating lives in the
  record func the hook calls.
- `internal/admin/admin.go` — add `r.Post("/api/acp-capture", …)`; add
  `ACP_CAPTURE_RUNTIME` to the env table.
- `internal/admin/capture.go` — extend GET response; add POST handler with the 403
  opt-in guard and 400 unknown-action guard; extend the `AcpCaptureSource`-style
  interface with `Enabled/AllowRuntimeToggle/Count/Size` + `Enable/Disable/Clear`.
- `internal/admin/` templates + `admin.js` + static — the capture panel and buttons.
- `docs/operating.md` — document `ACP_CAPTURE_RUNTIME`, the toggle, and the CSRF/
  exposure note.

## Resolved decisions

- Gate: opt-in env `ACP_CAPTURE_RUNTIME` (default false). ✓
- Buffer: auto-clear on enable; keep on disable; explicit Clear button. ✓
- Persistence: none (ephemeral; env is startup default). ✓
- Endpoint shape: single `POST /admin/api/acp-capture` `{action}`. ✓
- Ring allocation: pre-allocate when runtime toggling is allowed (lazy-alloc
  rejected — adds a race for negligible memory savings on an opt-in path). ✓
