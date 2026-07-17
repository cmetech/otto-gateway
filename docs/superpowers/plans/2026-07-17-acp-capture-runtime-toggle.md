# ACP Capture Runtime Toggle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator enable/disable ACP raw-frame capture from the `/admin` dashboard with no gateway restart, gated behind an opt-in `ACP_CAPTURE_RUNTIME` env.

**Architecture:** Always-wire the ACP `OnRawFrame` hook to a `capture.Controller` that gates recording on an `atomic.Bool`. The env `ACP_CAPTURE` seeds the initial on/off state; `ACP_CAPTURE_RUNTIME` permits flipping it at runtime. A single new `POST /admin/api/acp-capture {action}` mutates state (enable/disable/clear) behind a 403 opt-in guard; the dashboard renders a small control panel.

**Tech Stack:** Go 1.23+ (stdlib `net/http`, `chi`, `sync/atomic`, `log/slog`), server-rendered HTML templates + vanilla `admin.js`.

## Global Constraints

- Go 1.23+, no cgo in the gateway binary.
- Builds are CI-only: do NOT run `make build`/`make package`. `go build`, `go test`, `go vet`, `gofmt`, `gofumpt` are fine and expected.
- Work on branch `feat/acp-capture-runtime-toggle` (already created). Do not push or tag.
- Env var names are a backward-compat contract; the new var is `ACP_CAPTURE_RUNTIME` exactly.
- Capture content is SENSITIVE (raw prompt/response frames); preserve the "gate it" posture.
- `gofumpt -l` and `gofmt -l` must be empty (CI format gate).
- Frame per-cap byte constant stays `acpCaptureFrameCapBytes = 8 * 1024` (main.go).
- `admin.CaptureFrame` wire type is authoritative for the admin boundary; `internal/capture` must NOT be imported by `internal/admin` (TRST-04 boundary — the cmd layer adapts).

---

### Task 1: `Ring.Clear()` + `Len()`/`Cap()` accessors

**Files:**
- Modify: `internal/capture/ring.go`
- Test: `internal/capture/ring_test.go`

**Interfaces:**
- Produces: `func (r *Ring) Clear()`, `func (r *Ring) Len() int`, `func (r *Ring) Cap() int` on the existing `*capture.Ring`.

- [ ] **Step 1: Write the failing test**

Add to `internal/capture/ring_test.go`:

```go
func TestRing_ClearAndLen(t *testing.T) {
	r := NewRing(4, 1024)
	if r.Cap() != 4 {
		t.Fatalf("Cap: got %d, want 4", r.Cap())
	}
	r.Record("session/update", json.RawMessage(`{"a":1}`))
	r.Record("session/update", json.RawMessage(`{"b":2}`))
	if r.Len() != 2 {
		t.Fatalf("Len after 2 records: got %d, want 2", r.Len())
	}
	r.Clear()
	if r.Len() != 0 {
		t.Fatalf("Len after Clear: got %d, want 0", r.Len())
	}
	if got := r.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot after Clear: got %d frames, want 0", len(got))
	}
	// Recording works after Clear.
	r.Record("session/update", json.RawMessage(`{"c":3}`))
	if r.Len() != 1 {
		t.Fatalf("Len after post-Clear record: got %d, want 1", r.Len())
	}
}
```

Ensure `internal/capture/ring_test.go` imports `encoding/json` and `testing`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/capture/ -run TestRing_ClearAndLen -v`
Expected: FAIL — `r.Cap` / `r.Len` / `r.Clear` undefined.

- [ ] **Step 3: Add the methods**

Append to `internal/capture/ring.go` (after `Snapshot`):

```go
// Clear empties the ring: Snapshot returns nothing until new frames are
// recorded. The backing slice is retained; next/count/seq reset under the lock.
func (r *Ring) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next = 0
	r.count = 0
	r.seq = 0
}

// Len returns the number of buffered frames.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// Cap returns the ring's fixed frame capacity.
func (r *Ring) Cap() int {
	return len(r.buf)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/capture/ -run TestRing_ClearAndLen -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/capture/ring.go internal/capture/ring_test.go
git commit -m "feat(capture): add Ring.Clear/Len/Cap for runtime toggle"
```

---

### Task 2: `capture.Controller` (gated record + enable/disable/clear)

**Files:**
- Create: `internal/capture/controller.go`
- Test: `internal/capture/controller_test.go`

**Interfaces:**
- Consumes: `NewRing`, `Ring.Record/Clear/Len/Cap/Snapshot`, `capture.Frame` (Task 1).
- Produces:
  - `func NewController(size, capBytes int, initialEnabled, allowToggle bool) *Controller`
  - `func (c *Controller) Record(method string, params json.RawMessage)`
  - `func (c *Controller) Enable()` / `Disable()` / `Clear()`
  - `func (c *Controller) Enabled() bool` / `AllowRuntimeToggle() bool` / `Count() int` / `Size() int`
  - `func (c *Controller) Snapshot() []Frame`

- [ ] **Step 1: Write the failing test**

Create `internal/capture/controller_test.go`:

```go
package capture

import (
	"encoding/json"
	"testing"
)

func TestController_RecordGatedByEnabled(t *testing.T) {
	c := NewController(8, 1024, false /*initialEnabled*/, true /*allowToggle*/)

	// Disabled: Record is a no-op.
	c.Record("session/update", json.RawMessage(`{"a":1}`))
	if c.Count() != 0 {
		t.Fatalf("disabled Record buffered a frame: count=%d", c.Count())
	}
	if c.Enabled() {
		t.Fatal("initialEnabled=false but Enabled()=true")
	}

	// Enable, then record.
	c.Enable()
	if !c.Enabled() {
		t.Fatal("after Enable, Enabled()=false")
	}
	c.Record("session/update", json.RawMessage(`{"b":2}`))
	if c.Count() != 1 {
		t.Fatalf("enabled Record: count=%d, want 1", c.Count())
	}

	// Disable keeps the buffer readable.
	c.Disable()
	if c.Enabled() {
		t.Fatal("after Disable, Enabled()=true")
	}
	if c.Count() != 1 {
		t.Fatalf("Disable dropped the buffer: count=%d, want 1", c.Count())
	}
	c.Record("session/update", json.RawMessage(`{"c":3}`))
	if c.Count() != 1 {
		t.Fatalf("disabled Record after Disable buffered: count=%d, want 1", c.Count())
	}
}

func TestController_EnableAutoClears(t *testing.T) {
	c := NewController(8, 1024, true, true)
	c.Record("session/update", json.RawMessage(`{"a":1}`))
	c.Record("session/update", json.RawMessage(`{"b":2}`))
	if c.Count() != 2 {
		t.Fatalf("pre-enable count=%d, want 2", c.Count())
	}
	// Enable starts a fresh session.
	c.Enable()
	if c.Count() != 0 {
		t.Fatalf("Enable did not auto-clear: count=%d, want 0", c.Count())
	}
}

func TestController_ClearAndMeta(t *testing.T) {
	c := NewController(16, 1024, true, false)
	if c.AllowRuntimeToggle() {
		t.Fatal("allowToggle=false but AllowRuntimeToggle()=true")
	}
	if c.Size() != 16 {
		t.Fatalf("Size: got %d, want 16", c.Size())
	}
	c.Record("session/update", json.RawMessage(`{"a":1}`))
	c.Clear()
	if c.Count() != 0 {
		t.Fatalf("Clear: count=%d, want 0", c.Count())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/capture/ -run TestController -v`
Expected: FAIL — `NewController` undefined.

- [ ] **Step 3: Implement the controller**

Create `internal/capture/controller.go`:

```go
package capture

import (
	"encoding/json"
	"sync/atomic"
)

// Controller wraps a Ring with a runtime on/off gate (enabled) and an opt-in
// flag (allowToggle) that permits flipping that gate at runtime — e.g. from the
// admin UI. The OnRawFrame hook is wired to Record unconditionally whenever a
// Controller exists; the atomic gate makes the disabled path a single load.
type Controller struct {
	ring        *Ring
	enabled     atomic.Bool
	allowToggle bool
}

// NewController builds a Controller over a fresh Ring of the given size/cap.
// initialEnabled seeds the on/off state (from ACP_CAPTURE); allowToggle records
// whether runtime toggling is permitted (from ACP_CAPTURE_RUNTIME).
func NewController(size, capBytes int, initialEnabled, allowToggle bool) *Controller {
	c := &Controller{ring: NewRing(size, capBytes), allowToggle: allowToggle}
	c.enabled.Store(initialEnabled)
	return c
}

// Record forwards to the ring only while capture is enabled.
func (c *Controller) Record(method string, params json.RawMessage) {
	if c.enabled.Load() {
		c.ring.Record(method, params)
	}
}

// Enable clears the buffer (fresh capture session) then turns recording on.
func (c *Controller) Enable() {
	c.ring.Clear()
	c.enabled.Store(true)
}

// Disable turns recording off; the buffer is retained for inspection.
func (c *Controller) Disable() { c.enabled.Store(false) }

// Clear purges the buffered frames on demand.
func (c *Controller) Clear() { c.ring.Clear() }

func (c *Controller) Enabled() bool            { return c.enabled.Load() }
func (c *Controller) AllowRuntimeToggle() bool { return c.allowToggle }
func (c *Controller) Count() int               { return c.ring.Len() }
func (c *Controller) Size() int                { return c.ring.Cap() }
func (c *Controller) Snapshot() []Frame        { return c.ring.Snapshot() }
```

- [ ] **Step 4: Run tests + race detector**

Run: `go test ./internal/capture/ -run TestController -race -v`
Expected: PASS (all three), no race warnings.

- [ ] **Step 5: Commit**

```bash
git add internal/capture/controller.go internal/capture/controller_test.go
git commit -m "feat(capture): add Controller with atomic enable gate + auto-clear-on-enable"
```

---

### Task 3: Config `ACP_CAPTURE_RUNTIME`

**Files:**
- Modify: `internal/config/config.go` (field ~`:204`, load block ~`:566`, assembly ~`:849`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `cfg.AcpCaptureRuntime bool` on the `config.Config` struct, loaded from `ACP_CAPTURE_RUNTIME` (default false).

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go` (follow the file's existing env-set/Load pattern; if a helper like `withEnv`/`t.Setenv` is already used, mirror it):

```go
func TestLoad_AcpCaptureRuntime(t *testing.T) {
	t.Setenv("KIRO_CMD", "true") // minimal valid config; match other Load tests
	t.Setenv("ACP_CAPTURE_RUNTIME", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AcpCaptureRuntime {
		t.Fatal("ACP_CAPTURE_RUNTIME=true did not set cfg.AcpCaptureRuntime")
	}
}

func TestLoad_AcpCaptureRuntime_DefaultsFalse(t *testing.T) {
	t.Setenv("KIRO_CMD", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AcpCaptureRuntime {
		t.Fatal("ACP_CAPTURE_RUNTIME unset should default to false")
	}
}
```

> Note: if `Load()` in this repo takes arguments or a different constructor name, match the signature used by the neighboring tests in `config_test.go`. Do not invent a new one.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoad_AcpCaptureRuntime -v`
Expected: FAIL — `cfg.AcpCaptureRuntime` undefined.

- [ ] **Step 3: Add the field, load, and assembly**

In `internal/config/config.go`, after the `AcpCaptureSize int` field (~`:207`):

```go
	// AcpCaptureRuntime permits enabling/disabling ACP capture at runtime from
	// the /admin dashboard (no restart). Off by default. Loaded from
	// ACP_CAPTURE_RUNTIME. SENSITIVE — see docs/operating.md.
	AcpCaptureRuntime bool
```

In the load block, right after the `acpCaptureSize` validation (~`:576`):

```go
	acpCaptureRuntime, err := getEnvBool("ACP_CAPTURE_RUNTIME", false)
	if err != nil {
		errs = append(errs, err)
	}
```

In the `Config{...}` assembly, after `AcpCaptureSize: acpCaptureSize,` (~`:850`):

```go
		AcpCaptureRuntime:         acpCaptureRuntime,
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestLoad_AcpCaptureRuntime -v`
Expected: PASS (both)

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add ACP_CAPTURE_RUNTIME (default false)"
```

---

### Task 4: Admin interface + GET extension + POST route + env row

**Files:**
- Modify: `internal/admin/admin.go` (interface ~`:66`, env table ~`:585`, snapshot field ~`:436`, route registration ~`:223`)
- Modify: `internal/admin/capture.go` (response struct + GET handler + new POST handler)
- Test: `internal/admin/capture_test.go` (update fake to satisfy the extended interface; add POST tests)

**Interfaces:**
- Consumes: nothing from cmd (uses only the extended `AcpCaptureSource`).
- Produces: extended `admin.AcpCaptureSource` interface (below); `POST /admin/api/acp-capture`; extended GET JSON `{enabled, allowRuntimeToggle, count, size, frames}`.

- [ ] **Step 1: Extend the interface**

In `internal/admin/admin.go`, replace the `AcpCaptureSource` interface (~`:63-68`):

```go
// AcpCaptureSource is the consumer-defined interface the admin handler uses to
// read AND control the raw-frame capture ring. Returns admin's own wire type so
// this package stays boundary-clean. nil in Deps means capture is unavailable
// (neither ACP_CAPTURE nor ACP_CAPTURE_RUNTIME set).
type AcpCaptureSource interface {
	Snapshot() []CaptureFrame
	Enabled() bool
	AllowRuntimeToggle() bool
	Count() int
	Size() int
	Enable()
	Disable()
	Clear()
}
```

- [ ] **Step 2: Update the fake + write failing POST/GET tests**

In `internal/admin/capture_test.go`, replace the `fakeCaptureSource` (~`:15-17`) with a controllable fake and add tests:

```go
type fakeCaptureSource struct {
	frames     []admin.CaptureFrame
	enabled    bool
	allow      bool
	size       int
	enableN    int
	disableN   int
	clearN     int
}

func (f *fakeCaptureSource) Snapshot() []admin.CaptureFrame { return f.frames }
func (f *fakeCaptureSource) Enabled() bool                  { return f.enabled }
func (f *fakeCaptureSource) AllowRuntimeToggle() bool       { return f.allow }
func (f *fakeCaptureSource) Count() int                     { return len(f.frames) }
func (f *fakeCaptureSource) Size() int                      { return f.size }
func (f *fakeCaptureSource) Enable()                        { f.enableN++; f.enabled = true }
func (f *fakeCaptureSource) Disable()                       { f.disableN++; f.enabled = false }
func (f *fakeCaptureSource) Clear()                         { f.clearN++; f.frames = nil }

func doCapturePost(t *testing.T, src admin.AcpCaptureSource, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := admin.Handler(admin.Deps{AcpCapture: src})
	req := httptest.NewRequest(http.MethodPost, "/api/acp-capture", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAcpCapturePost_EnableWhenAllowed(t *testing.T) {
	src := &fakeCaptureSource{allow: true, size: 512}
	rec := doCapturePost(t, src, `{"action":"enable"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if src.enableN != 1 || !src.enabled {
		t.Fatalf("enable not applied: enableN=%d enabled=%v", src.enableN, src.enabled)
	}
}

func TestAcpCapturePost_ForbiddenWhenToggleDisallowed(t *testing.T) {
	src := &fakeCaptureSource{allow: false}
	rec := doCapturePost(t, src, `{"action":"enable"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when allow=false, got %d", rec.Code)
	}
	if src.enableN != 0 {
		t.Fatalf("enable applied despite 403: enableN=%d", src.enableN)
	}
}

func TestAcpCapturePost_UnknownAction400(t *testing.T) {
	src := &fakeCaptureSource{allow: true}
	rec := doCapturePost(t, src, `{"action":"frobnicate"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown action, got %d", rec.Code)
	}
}

func TestAcpCaptureGet_ExtendedShape(t *testing.T) {
	src := &fakeCaptureSource{allow: true, enabled: true, size: 512, frames: []admin.CaptureFrame{{Seq: 1, Method: "session/update"}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})
	req := httptest.NewRequest(http.MethodGet, "/api/acp-capture", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got struct {
		Enabled            bool `json:"enabled"`
		AllowRuntimeToggle bool `json:"allowRuntimeToggle"`
		Count              int  `json:"count"`
		Size               int  `json:"size"`
		Frames             []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Enabled || !got.AllowRuntimeToggle || got.Count != 1 || got.Size != 512 || len(got.Frames) != 1 {
		t.Fatalf("extended GET shape wrong: %+v", got)
	}
}
```

Ensure `capture_test.go` imports include `net/http`, `net/http/httptest`, `strings`, `encoding/json`, `testing`.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/admin/ -run 'TestAcpCapture' -v`
Expected: FAIL — POST route 405/404, extended GET fields zero, fake may not compile until Step 4 handler exists (compile error is an acceptable "fail").

- [ ] **Step 4: Extend the GET handler + add the POST handler**

Replace `internal/admin/capture.go` contents with:

```go
package admin

import (
	"encoding/json"
	"io"
	"net/http"
)

// acpCaptureResponse is the GET /admin/api/acp-capture body (also returned by
// the POST after applying an action).
type acpCaptureResponse struct {
	Enabled            bool           `json:"enabled"`
	AllowRuntimeToggle bool           `json:"allowRuntimeToggle"`
	Count              int            `json:"count"`
	Size               int            `json:"size"`
	Frames             []CaptureFrame `json:"frames"`
}

// acpCaptureActionRequest is the POST body: {"action":"enable"|"disable"|"clear"}.
type acpCaptureActionRequest struct {
	Action string `json:"action"`
}

// acpCaptureHandler serves the capture ring + runtime state as JSON. When no
// source is wired it reports enabled:false / allowRuntimeToggle:false with an
// empty frames array (200, not 404, so a harness can tell "off" from "missing").
func (h *handler) acpCaptureHandler(w http.ResponseWriter, _ *http.Request) {
	resp := acpCaptureResponse{Frames: []CaptureFrame{}}
	if src := h.deps.AcpCapture; src != nil {
		resp.Enabled = src.Enabled()
		resp.AllowRuntimeToggle = src.AllowRuntimeToggle()
		resp.Count = src.Count()
		resp.Size = src.Size()
		if fr := src.Snapshot(); fr != nil {
			resp.Frames = fr
		}
	}
	writeJSONCapture(w, http.StatusOK, resp, h)
}

// acpCapturePostHandler mutates capture state: enable | disable | clear. Guarded
// by the opt-in ACP_CAPTURE_RUNTIME flag (403 when not allowed) — this is the
// admin surface's only write route.
func (h *handler) acpCapturePostHandler(w http.ResponseWriter, req *http.Request) {
	src := h.deps.AcpCapture
	if src == nil {
		writeJSONErr(w, http.StatusForbidden, "capture not available on this gateway")
		return
	}
	if !src.AllowRuntimeToggle() {
		writeJSONErr(w, http.StatusForbidden, "runtime toggle disabled; start the gateway with ACP_CAPTURE_RUNTIME=true")
		return
	}
	var body acpCaptureActionRequest
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<10)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch body.Action {
	case "enable":
		src.Enable()
	case "disable":
		src.Disable()
	case "clear":
		src.Clear()
	default:
		writeJSONErr(w, http.StatusBadRequest, "unknown action; want enable|disable|clear")
		return
	}
	// Echo the updated status so the UI can refresh from one round-trip.
	h.acpCaptureHandler(w, req)
}

func writeJSONCapture(w http.ResponseWriter, status int, v any, h *handler) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.deps.Logger.Warn("admin: acp-capture encode failed", "err", err)
	}
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
```

> Note: `h.deps.Logger` is non-nil (Handler substitutes a no-op logger when Deps.Logger is nil — preserve that assumption; the existing capture.go relied on it).

- [ ] **Step 5: Register the POST route**

In `internal/admin/admin.go`, immediately after the existing GET registration (~`:223`):

```go
	// GET /api/acp-capture — raw kiro frame capture ring + runtime state.
	r.Get("/api/acp-capture", h.acpCaptureHandler)

	// POST /api/acp-capture — enable|disable|clear capture at runtime. The only
	// admin mutation route; 403 unless ACP_CAPTURE_RUNTIME opted in.
	r.Post("/api/acp-capture", h.acpCapturePostHandler)
```

- [ ] **Step 6: Reflect runtime state in the snapshot + env table**

In `internal/admin/admin.go`, update the snapshot field (~`:436`):

```go
		AcpCaptureEnabled:    h.deps.AcpCapture != nil && h.deps.AcpCapture.Enabled(),
```

Update the `ACP_CAPTURE` env-table row's `CurrentValue` (~`:585`) to reflect runtime enabled, and add the new row directly after `ACP_CAPTURE_SIZE`:

```go
		{Name: "ACP_CAPTURE", Default: "false", Description: "SENSITIVE — raw ACP-frame capture ring (Track 0 diagnostics). When true, rings recent kiro↔gateway frames (prompt/response content) in memory, exposed at /admin/api/acp-capture. Diagnostics only; set in overrides.env since .env is regenerated by `gw upgrade-env`.", CurrentValue: boolOnOff(h.deps.AcpCapture != nil && h.deps.AcpCapture.Enabled())},
		{Name: "ACP_CAPTURE_SIZE", Default: "512", Description: "Bounds the ACP capture ring (frames retained). Only used when ACP_CAPTURE=true.", CurrentValue: "(see startup log)"},
		{Name: "ACP_CAPTURE_RUNTIME", Default: "false", Description: "SENSITIVE — when true, permits enabling/disabling ACP capture at runtime from the /admin dashboard (no restart). Leave false unless /admin is localhost/firewalled.", CurrentValue: boolOnOff(h.deps.AcpCapture != nil && h.deps.AcpCapture.AllowRuntimeToggle())},
```

> If `aboutHandler`/dashboard also derives capture state from `Deps.AcpCapture != nil` (see admin.go:326 comment and `:436`), leave those "capability present" checks as-is except the enabled reflection above — "present" (nil check) and "currently on" (`Enabled()`) are now distinct concepts.

- [ ] **Step 7: Run the admin tests**

Run: `go test ./internal/admin/ -run 'TestAcpCapture' -v`
Expected: PASS (enable-when-allowed, 403-when-disallowed, 400-unknown, extended GET)
Then the whole package: `go test ./internal/admin/`
Expected: PASS (the pre-existing `capture_test.go` GET test still passes against the extended shape — it only asserts fields it reads).

- [ ] **Step 8: Commit**

```bash
git add internal/admin/admin.go internal/admin/capture.go internal/admin/capture_test.go
git commit -m "feat(admin): POST /api/acp-capture toggle (enable/disable/clear) with opt-in 403 guard"
```

---

### Task 5: Wire the Controller in `main.go`

**Files:**
- Modify: `cmd/otto-gateway/main.go` (ring construction ~`:421-426`, pool/registry `Capture:` ~`:437`/`:481`, admin dep ~`:790`, helpers ~`:947-978`)

**Interfaces:**
- Consumes: `capture.NewController`/`Controller` (Task 2), `cfg.AcpCaptureRuntime` (Task 3), extended `admin.AcpCaptureSource` (Task 4).
- Produces: no new exported API; correct runtime wiring.

- [ ] **Step 1: Replace the ring construction**

In `cmd/otto-gateway/main.go`, replace the block at ~`:417-426`:

```go
	// Track 0 tool-call wire capture. Construct a Controller when capture is on
	// at startup (ACP_CAPTURE) OR when runtime toggling is permitted
	// (ACP_CAPTURE_RUNTIME) — in the latter case the ring is allocated but the
	// atomic gate starts in ACP_CAPTURE's state. When neither is set the
	// controller is nil, the record func is nil, and the acp OnRawFrame hook
	// stays unset (zero cost). Shared across the pool + session registry; read
	// and toggled by the admin endpoint.
	const acpCaptureFrameCapBytes = 8 * 1024 // per-frame params byte cap
	var acpCapture *capture.Controller
	if cfg.AcpCapture || cfg.AcpCaptureRuntime {
		acpCapture = capture.NewController(cfg.AcpCaptureSize, acpCaptureFrameCapBytes, cfg.AcpCapture, cfg.AcpCaptureRuntime)
		logger.Info("acp raw-frame capture wired",
			"enabled", cfg.AcpCapture, "runtimeToggle", cfg.AcpCaptureRuntime,
			"size", cfg.AcpCaptureSize, "endpoint", "/admin/api/acp-capture")
	}
```

- [ ] **Step 2: Point pool + registry Capture at the controller**

Change both `Capture:` lines (pool ~`:437`, registry ~`:481`) from `captureRecordFunc(acpCaptureRing)` to:

```go
			Capture:        controllerRecordFunc(acpCapture),
```

- [ ] **Step 3: Replace the helper funcs + adapter**

Replace `captureRecordFunc`, `acpCaptureAdapter`, and `adminAcpCapture` (~`:947-978`) with:

```go
// controllerRecordFunc returns the controller's gated Record method, or nil
// when capture is entirely unconfigured (nil controller) so the acp OnRawFrame
// hook stays unset (zero cost). When the controller exists but is currently
// disabled, Record is still wired and the atomic gate makes it a no-op — this
// is what lets the admin endpoint enable capture without re-wiring.
func controllerRecordFunc(c *capture.Controller) func(method string, params json.RawMessage) {
	if c == nil {
		return nil
	}
	return c.Record
}

// acpCaptureAdapter adapts *capture.Controller to admin.AcpCaptureSource,
// converting capture.Frame into admin's own CaptureFrame wire type (TRST-04
// boundary — admin never imports internal/capture).
type acpCaptureAdapter struct{ ctrl *capture.Controller }

func (a acpCaptureAdapter) Snapshot() []admin.CaptureFrame {
	frames := a.ctrl.Snapshot()
	out := make([]admin.CaptureFrame, len(frames))
	for i, f := range frames {
		out[i] = admin.CaptureFrame{
			Seq: f.Seq, Ts: f.Ts, Method: f.Method, Params: f.Params, Bytes: f.Bytes,
		}
	}
	return out
}

func (a acpCaptureAdapter) Enabled() bool            { return a.ctrl.Enabled() }
func (a acpCaptureAdapter) AllowRuntimeToggle() bool { return a.ctrl.AllowRuntimeToggle() }
func (a acpCaptureAdapter) Count() int               { return a.ctrl.Count() }
func (a acpCaptureAdapter) Size() int                { return a.ctrl.Size() }
func (a acpCaptureAdapter) Enable()                  { a.ctrl.Enable() }
func (a acpCaptureAdapter) Disable()                 { a.ctrl.Disable() }
func (a acpCaptureAdapter) Clear()                   { a.ctrl.Clear() }

// adminAcpCapture wraps the controller as an admin.AcpCaptureSource, or returns
// nil when capture is unconfigured so the endpoint renders enabled:false.
func adminAcpCapture(c *capture.Controller) admin.AcpCaptureSource {
	if c == nil {
		return nil
	}
	return acpCaptureAdapter{ctrl: c}
}
```

- [ ] **Step 4: Update the admin dep call site**

Change the admin Deps wiring (~`:790`) from `adminAcpCapture(acpCaptureRing)` to:

```go
		AcpCapture:   adminAcpCapture(acpCapture),
```

- [ ] **Step 5: Build + full suite + vet + fmt**

Run:
```bash
go build ./... && go vet ./cmd/... ./internal/... && go test ./... && gofmt -l cmd/otto-gateway/main.go internal/capture/ internal/admin/ internal/config/ && go run mvdan.cc/gofumpt@latest -l cmd/otto-gateway/main.go internal/capture internal/admin internal/config
```
Expected: build clean; vet clean; `ALL PASS`; both `-l` print nothing.

- [ ] **Step 6: Commit**

```bash
git add cmd/otto-gateway/main.go
git commit -m "feat(cmd): wire capture.Controller (runtime toggle) into pool/registry/admin"
```

---

### Task 6: Dashboard capture panel + `admin.js`

**Files:**
- Modify: `internal/admin/templates/dashboard.html.tmpl` (add a section after the Log Tail section, ~`:164`)
- Modify: `internal/admin/static/js/admin.js` (add capture panel init inside the existing IIFE)

**Interfaces:**
- Consumes: `GET`/`POST /admin/api/acp-capture` (Task 4).
- Produces: UI only. No Go API.

- [ ] **Step 1: Add the panel markup**

In `internal/admin/templates/dashboard.html.tmpl`, after the Log Tail `</section>` (~`:164`), add:

```html
  <!-- Section 5: ACP Capture (diagnostics) — hidden until admin.js confirms availability -->
  <section aria-labelledby="acp-capture-heading" data-acp-capture hidden>
    <h2 id="acp-capture-heading" class="gw-h2">ACP Capture (diagnostics)</h2>
    <div class="gw-acp-capture-card">
      <span class="gw-pill" data-acp-capture-pill>—</span>
      <span data-acp-capture-count>0 / 0 frames</span>
      <span class="gw-acp-capture-controls" data-acp-capture-controls hidden>
        <button type="button" class="gw-btn" data-acp-capture-toggle>Enable</button>
        <button type="button" class="gw-btn" data-acp-capture-clear>Clear</button>
      </span>
      <a href="api/acp-capture" data-acp-capture-raw>raw JSON</a>
      <p class="gw-acp-capture-note" data-acp-capture-note hidden>
        Read-only: start the gateway with <code>ACP_CAPTURE_RUNTIME=true</code> to toggle here.
      </p>
    </div>
  </section>
```

- [ ] **Step 2: Add the panel logic to `admin.js`**

Inside the IIFE in `internal/admin/static/js/admin.js` (before the closing `})();`), add:

```javascript
  // --- ACP Capture (diagnostics) panel ---------------------------------
  // Reads/controls GET+POST /admin/api/acp-capture. The panel stays hidden
  // unless the endpoint reports a capture source; controls stay hidden unless
  // allowRuntimeToggle is true (ACP_CAPTURE_RUNTIME opt-in).
  function acpCaptureUrl() {
    // Same-origin, mount-agnostic: the dashboard is served at /admin, assets at
    // /admin/static, so a relative "api/acp-capture" resolves under /admin.
    return 'api/acp-capture';
  }

  function renderAcpCapture(state) {
    var section = document.querySelector('[data-acp-capture]');
    if (!section) return;
    // Panel stays hidden unless the endpoint reported a capture source
    // (hasSource is set by the fetch/post callbacks below).
    section.hidden = !state || !state.hasSource;
    if (!state || !state.hasSource) return;

    var pill = section.querySelector('[data-acp-capture-pill]');
    if (pill) {
      pill.textContent = state.enabled ? 'CAPTURING' : 'off';
      pill.className = 'gw-pill ' + (state.enabled ? 'gw-pill-on' : 'gw-pill-off');
    }
    var count = section.querySelector('[data-acp-capture-count]');
    if (count) count.textContent = (state.count || 0) + ' / ' + (state.size || 0) + ' frames';

    var controls = section.querySelector('[data-acp-capture-controls]');
    var note = section.querySelector('[data-acp-capture-note]');
    if (controls) controls.hidden = !state.allowRuntimeToggle;
    if (note) note.hidden = !!state.allowRuntimeToggle;

    var toggle = section.querySelector('[data-acp-capture-toggle]');
    if (toggle) toggle.textContent = state.enabled ? 'Disable' : 'Enable';
  }

  function fetchAcpCapture() {
    fetch(acpCaptureUrl(), { headers: { 'Accept': 'application/json' } })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (j) {
        if (!j) { renderAcpCapture({ hasSource: false }); return; }
        j.hasSource = true;
        renderAcpCapture(j);
      })
      .catch(function () { renderAcpCapture({ hasSource: false }); });
  }

  function postAcpCapture(action) {
    fetch(acpCaptureUrl(), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action: action })
    })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (j) {
        if (j) { j.hasSource = true; renderAcpCapture(j); }
        else { fetchAcpCapture(); }
      })
      .catch(function () { fetchAcpCapture(); });
  }

  function initAcpCapture() {
    var section = document.querySelector('[data-acp-capture]');
    if (!section) return;
    var toggle = section.querySelector('[data-acp-capture-toggle]');
    var clear = section.querySelector('[data-acp-capture-clear]');
    if (toggle) toggle.addEventListener('click', function () {
      var capturing = toggle.textContent === 'Disable';
      postAcpCapture(capturing ? 'disable' : 'enable');
    });
    if (clear) clear.addEventListener('click', function () { postAcpCapture('clear'); });
    fetchAcpCapture();
    // Refresh the frame count alongside the existing snapshot cadence.
    setInterval(fetchAcpCapture, 30000);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initAcpCapture);
  } else {
    initAcpCapture();
  }
```

> If `admin.js` already has a single DOMContentLoaded bootstrap, call `initAcpCapture()` from there instead of adding a second listener — match the file's existing bootstrap style. Reuse existing `gw-pill-on`/`gw-pill-off`/`gw-btn` classes if present; if not, the panel still works unstyled and CSS can be added later without blocking.

- [ ] **Step 3: Manual verification (no unit test for UI)**

Because the gateway build is CI-only, verify locally by running the existing binary path used for e2e, or via `go run` against a fake kiro if available. Minimum manual check:

```bash
# Terminal A — run with runtime toggle allowed, capture starting OFF:
ACP_CAPTURE_RUNTIME=true ACP_CAPTURE=false KIRO_CMD=... go run ./cmd/otto-gateway
```
Then in a browser at `http://localhost:<port>/admin`:
1. The "ACP Capture (diagnostics)" panel is visible with pill `off` and an `Enable` button.
2. Click `Enable` → pill flips to `CAPTURING`, button becomes `Disable`.
3. Send a prompt through the gateway; the frame count rises.
4. Click `Clear` → count returns to 0.
5. Click `Disable` → pill `off`, count retained.
6. Restart with `ACP_CAPTURE_RUNTIME` unset → panel shows read-only status, no buttons, and `POST /admin/api/acp-capture` returns 403 (`curl -s -o /dev/null -w '%{http_code}' -X POST localhost:<port>/admin/api/acp-capture -d '{"action":"enable"}'` → `403`).

Record the results inline in the task checklist.

- [ ] **Step 4: Commit**

```bash
git add internal/admin/templates/dashboard.html.tmpl internal/admin/static/js/admin.js
git commit -m "feat(admin-ui): ACP capture toggle panel (enable/disable/clear)"
```

---

### Task 7: Operator docs

**Files:**
- Modify: `docs/operating.md` (the "ACP raw-frame capture (diagnostic)" section, ~`:480-492`)

**Interfaces:** none.

- [ ] **Step 1: Document the runtime toggle**

In `docs/operating.md`, under the existing ACP capture section, add:

```markdown
### Runtime toggle (no restart)

Set `ACP_CAPTURE_RUNTIME=true` at startup to permit enabling/disabling capture
from the `/admin` dashboard without a restart. When set:

- The dashboard shows an **ACP Capture (diagnostics)** panel with **Enable/Disable**
  and **Clear** buttons.
- `ACP_CAPTURE` seeds the initial state (capture can start on or off).
- Enabling starts a fresh buffer (auto-clear); disabling keeps the buffer
  readable; Clear purges it on demand. Frames remain memory-only (lost on restart).

`POST /admin/api/acp-capture` with `{"action":"enable"|"disable"|"clear"}` drives
this; it returns **403** unless `ACP_CAPTURE_RUNTIME=true`.

**Security:** this is the admin surface's only state-changing route, and `/admin`
is auth-exempt / not IP-allowlisted. Capture records SENSITIVE prompt/response
content. Only set `ACP_CAPTURE_RUNTIME=true` where `/admin` is localhost or
firewalled. Leave it unset (the default) otherwise — capture is then env-only, as
before, requiring a restart to change.
```

- [ ] **Step 2: Commit**

```bash
git add docs/operating.md
git commit -m "docs(operating): document ACP_CAPTURE_RUNTIME toggle + security posture"
```

---

## Final verification (after all tasks)

- [ ] `go build ./...` — clean
- [ ] `go vet ./...` — clean
- [ ] `go test ./... -race` — ALL PASS
- [ ] `gofmt -l` and `go run mvdan.cc/gofumpt@latest -l` over changed dirs — empty
- [ ] Manual admin toggle smoke (Task 6 Step 3) recorded
- [ ] Branch `feat/acp-capture-runtime-toggle` holds one commit per task; not pushed/tagged

## Spec coverage map

- Runtime model (always-wire + atomic gate) → Task 2 + Task 5
- `ACP_CAPTURE_RUNTIME` env → Task 3
- `Ring.Clear()` → Task 1
- Buffer lifecycle (enable auto-clears, disable keeps, clear purges) → Task 2 (+ UI Task 6)
- `POST /admin/api/acp-capture {action}` + 403 opt-in guard + 400 unknown → Task 4
- Extended `GET` shape (enabled/allowRuntimeToggle/count/size/frames) → Task 4
- Snapshot `AcpCaptureEnabled` reflects runtime state; env table row → Task 4
- Pre-allocate ring when runtime allowed → Task 5 (construct when `AcpCapture || AcpCaptureRuntime`)
- UI panel with opt-in-gated controls → Task 6
- Security/CSRF/sensitivity docs → Task 7
```
