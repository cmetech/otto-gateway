# Track 0 — Tool-call Wire Capture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture kiro's raw ACP frames through the real gateway path — behind an `ACP_CAPTURE`-gated admin ring-buffer endpoint — and drive a repo-local real-kiro harness that classifies kiro's tool-call wire behavior to scope Track 3.

**Architecture:** A new leaf `internal/capture` package holds a bounded, mutex-guarded ring of raw notification frames. A new optional `acp.Config.OnRawFrame` hook (mirroring the existing `OnTurnMeter`/`OnContextPct`/`OnMCPInit` hooks) feeds it from the readLoop; the pool and session registry forward that hook to the ring exactly as they forward the metrics recorder. A boundary-clean admin endpoint (`GET /admin/api/acp-capture`) reads the ring via a consumer-defined interface. A build-tag-gated integration test drives a tool round-trip on each surface, reads the ring, and writes a findings report.

**Tech Stack:** Go 1.26, stdlib `net/http` + `github.com/go-chi/chi/v5`, existing `slog`/config/admin patterns. No new dependencies.

## Global Constraints

- Go 1.26.x; **no cgo** in the gateway binary — `CGO_ENABLED=0 go build ./cmd/otto-gateway` must pass.
- **Additive-only** ACP wire changes; existing notification handling unchanged.
- Env var names are stable operator contract: `ACP_CAPTURE`, `ACP_CAPTURE_SIZE`.
- `gofumpt`-clean (`go run mvdan.cc/gofumpt@latest -l .` empty), `go vet ./...` clean, `golangci-lint` introduces no new findings on touched packages.
- TRST-04 boundary: `internal/admin` imports only stdlib + chi + `internal/version` (see `.go-arch-lint.yml`). It must NOT import `internal/capture`, `internal/pool`, `internal/session`, or `internal/engine` — cross the boundary with an admin-owned wire type + a `cmd/otto-gateway` adapter, exactly as `RegistryStatsSource`/`SnapshotSess` does today.
- Capture records raw prompt/response content: **off by default**, admin-only, bounded. Never persisted to disk by the gateway.
- Every commit ends with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2
  ```

---

### Task 1: `internal/capture` — bounded raw-frame ring

**Files:**
- Create: `internal/capture/ring.go`
- Test: `internal/capture/ring_test.go`
- Modify: `.go-arch-lint.yml` (register the new leaf component)

**Interfaces:**
- Produces:
  - `type Frame struct { Seq uint64; Ts time.Time; Method string; Params string; Bytes int }`
  - `func NewRing(size, capBytes int) *Ring`
  - `func (r *Ring) Record(method string, params json.RawMessage)` — appends one frame; overwrites the oldest when full; truncates `params` to `capBytes` on a UTF-8 rune boundary; sets `Bytes` to the pre-truncation length.
  - `func (r *Ring) Snapshot() []Frame` — oldest→newest copy.
- Consumes: nothing (leaf package; stdlib only).

- [ ] **Step 1: Write the failing test**

Create `internal/capture/ring_test.go`:

```go
package capture

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestRing_RecordSnapshotOrder(t *testing.T) {
	r := NewRing(4, 1024)
	r.Record("a", json.RawMessage(`{"n":1}`))
	r.Record("b", json.RawMessage(`{"n":2}`))

	got := r.Snapshot()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Method != "a" || got[1].Method != "b" {
		t.Errorf("order = %q,%q; want a,b", got[0].Method, got[1].Method)
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Errorf("seq = %d,%d; want 1,2", got[0].Seq, got[1].Seq)
	}
	if got[0].Params != `{"n":1}` {
		t.Errorf("params = %q; want {\"n\":1}", got[0].Params)
	}
}

func TestRing_OverflowOverwritesOldest(t *testing.T) {
	r := NewRing(2, 1024)
	r.Record("a", json.RawMessage(`1`))
	r.Record("b", json.RawMessage(`2`))
	r.Record("c", json.RawMessage(`3`)) // evicts a

	got := r.Snapshot()
	if len(got) != 2 || got[0].Method != "b" || got[1].Method != "c" {
		t.Fatalf("snapshot = %+v; want [b,c]", got)
	}
	if got[1].Seq != 3 {
		t.Errorf("newest seq = %d; want 3", got[1].Seq)
	}
}

func TestRing_TruncatesOnRuneBoundary(t *testing.T) {
	// A multi-byte rune (é = 2 bytes) straddling the cap must not be split.
	big := `"` + strings.Repeat("é", 20) + `"` // 42 bytes
	r := NewRing(1, 10)
	r.Record("m", json.RawMessage(big))

	f := r.Snapshot()[0]
	if len(f.Params) > 10 {
		t.Errorf("params len = %d, want <= 10 (capBytes)", len(f.Params))
	}
	if !isValidUTF8(f.Params) {
		t.Errorf("truncation split a rune: %q", f.Params)
	}
	if f.Bytes != len(big) {
		t.Errorf("Bytes = %d, want %d (pre-truncation length)", f.Bytes, len(big))
	}
}

func TestRing_ConcurrentRecord(t *testing.T) {
	r := NewRing(1024, 64)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Record("m", json.RawMessage(`{}`))
			}
		}()
	}
	wg.Wait()
	if n := len(r.Snapshot()); n != 800 {
		t.Errorf("recorded %d frames, want 800", n)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/capture/`
Expected: FAIL — build error, `NewRing`/`Ring`/`Frame` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/capture/ring.go`:

```go
// Package capture holds a bounded ring buffer of raw ACP notification frames
// for the Track 0 tool-call wire-capture harness. It is a leaf package (stdlib
// only) so the acp/pool/session layers feed it through a plain func hook
// without importing it, and the admin endpoint reads it through a
// consumer-defined interface — the same boundary discipline the metrics
// recorder uses.
package capture

import (
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"
)

// Frame is one captured inbound kiro frame. Params is the raw params JSON,
// truncated to the ring's per-frame byte cap on a UTF-8 rune boundary; Bytes is
// the pre-truncation length so an operator can tell when a frame was clipped.
type Frame struct {
	Seq    uint64    `json:"seq"`
	Ts     time.Time `json:"ts"`
	Method string    `json:"method"`
	Params string    `json:"params"`
	Bytes  int       `json:"bytes"`
}

// Ring is a fixed-size, mutex-guarded circular buffer of Frames. Safe for
// concurrent Record from multiple readLoop goroutines (one per slot/session).
type Ring struct {
	mu       sync.Mutex
	buf      []Frame
	capBytes int
	next     int    // index to write the next frame
	count    int    // number of valid frames (<= len(buf))
	seq      uint64 // monotonic frame counter
}

// NewRing returns a ring holding up to size frames, each with params truncated
// to capBytes. size <= 0 floors to 1; capBytes <= 0 floors to 1.
func NewRing(size, capBytes int) *Ring {
	if size <= 0 {
		size = 1
	}
	if capBytes <= 0 {
		capBytes = 1
	}
	return &Ring{buf: make([]Frame, size), capBytes: capBytes}
}

// Record appends one frame, overwriting the oldest when full.
func (r *Ring) Record(method string, params json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	r.buf[r.next] = Frame{
		Seq:    r.seq,
		Ts:     time.Now(),
		Method: method,
		Params: truncateUTF8(string(params), r.capBytes),
		Bytes:  len(params),
	}
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
}

// Snapshot returns a copy of the buffered frames, oldest first.
func (r *Ring) Snapshot() []Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Frame, 0, r.count)
	start := 0
	if r.count == len(r.buf) {
		start = r.next // full: oldest sits at next
	}
	for i := 0; i < r.count; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}

// truncateUTF8 clips s to at most maxLen bytes without splitting a rune. Walks
// back from the cap to the previous rune-start byte (cost <= utf8.UTFMax-1);
// mirrors the stderrDrainLoop truncation in internal/acp/client.go.
func truncateUTF8(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	n := maxLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/capture/`
Expected: PASS (all four tests, race-clean).

- [ ] **Step 5: Register the component in arch-lint**

Modify `.go-arch-lint.yml`. Under `components:` add:

```yaml
  capture:
    in: capture/**
```

Under `deps:` add:

```yaml
  capture:
    anyVendorDeps: true
```

(Leaf: no internal deps.)

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/capture/ && go run mvdan.cc/gofumpt@latest -w internal/capture/
git add internal/capture/ .go-arch-lint.yml
git commit -m "feat(capture): bounded raw-frame ring for tool-call wire capture

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2"
```

---

### Task 2: acp `OnRawFrame` hook

**Files:**
- Modify: `internal/acp/client.go` (Config struct ~line 60; `readLoop` ~line 572, after `json.Unmarshal(frame, &f)` and before `c.disp.route(f)`)
- Test: `internal/acp/rawframe_test.go` (whitebox, `package acp`)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `acp.Config.OnRawFrame func(method string, params json.RawMessage)` — fired by `readLoop` for every inbound frame that unmarshals, with the frame's method and raw params. nil = no capture.

- [ ] **Step 1: Write the failing test**

Create `internal/acp/rawframe_test.go`:

```go
package acp

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
)

// TestReadLoop_FiresOnRawFrame: every inbound frame that unmarshals is handed to
// OnRawFrame with its method + raw params, before dispatch.
func TestReadLoop_FiresOnRawFrame(t *testing.T) {
	var mu sync.Mutex
	type got struct {
		method string
		params string
	}
	var frames []got

	cfg := Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnRawFrame: func(method string, params json.RawMessage) {
			mu.Lock()
			frames = append(frames, got{method, string(params)})
			mu.Unlock()
		},
	}
	mock := newMockRWC()
	c := NewWithConn(mock, cfg)
	t.Cleanup(func() { _ = c.Close() })

	// Feed one notification frame through the reader side of the mock.
	mock.pushInbound(t, `{"jsonrpc":"2.0","method":"_kiro.dev/metadata","params":{"contextUsagePercentage":5}}`)

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(frames) >= 1
	})

	mu.Lock()
	defer mu.Unlock()
	if frames[0].method != "_kiro.dev/metadata" {
		t.Errorf("method = %q", frames[0].method)
	}
	if frames[0].params != `{"contextUsagePercentage":5}` {
		t.Errorf("params = %q", frames[0].params)
	}
}
```

> **Note for the implementer:** `newMockRWC` already exists in `internal/acp/client_test.go`. Inspect it: if it exposes a way to inject an inbound frame and a wait helper, use those and delete the `pushInbound`/`waitFor` references above. If it does not, add a minimal `pushInbound(t, line string)` method that writes `line+"\n"` to the mock's read side, and a local `waitFor(t, cond, ...)` poll helper (10ms ticks, 2s timeout). Keep the assertion body unchanged.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/acp/ -run TestReadLoop_FiresOnRawFrame`
Expected: FAIL — `unknown field OnRawFrame in struct literal of type Config`.

- [ ] **Step 3a: Add the Config field**

In `internal/acp/client.go`, in the `Config` struct, immediately after the `OnMCPInit` field, add:

```go
	// OnRawFrame, when set, is fired by readLoop for every inbound kiro frame
	// (after a successful json.Unmarshal, before dispatch) with the frame's
	// method and raw params. Used by the Track 0 capture ring to observe kiro's
	// raw wire behavior. nil = no capture (one nil check on the read path, zero
	// cost when disabled).
	OnRawFrame func(method string, params json.RawMessage)
```

- [ ] **Step 3b: Fire the hook in readLoop**

In `internal/acp/client.go`, in `readLoop`, find:

```go
		var f rpcFrame
		if err := json.Unmarshal(frame, &f); err != nil {
			c.cfg.Logger.Warn("acp: malformed frame", "err", err)
			continue // log and continue — don't kill session on parse error (T-02-04)
		}
		c.disp.route(f)
```

Insert the hook call between the unmarshal guard and `c.disp.route(f)`:

```go
		var f rpcFrame
		if err := json.Unmarshal(frame, &f); err != nil {
			c.cfg.Logger.Warn("acp: malformed frame", "err", err)
			continue // log and continue — don't kill session on parse error (T-02-04)
		}
		// Track 0 capture: hand the raw method + params to the capture ring
		// before dispatch. Cheap nil check when capture is disabled.
		if c.cfg.OnRawFrame != nil {
			c.cfg.OnRawFrame(f.Method, f.Params)
		}
		c.disp.route(f)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/acp/ -run TestReadLoop_FiresOnRawFrame`
Expected: PASS.

- [ ] **Step 5: Run the full acp package**

Run: `go test ./internal/acp/`
Expected: PASS (no regression).

- [ ] **Step 6: Commit**

```bash
go run mvdan.cc/gofumpt@latest -w internal/acp/
git add internal/acp/
git commit -m "feat(acp): OnRawFrame hook — hand raw inbound frames to a capture sink

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2"
```

---

### Task 3: pool + session forward the capture hook

**Files:**
- Modify: `internal/pool/config.go` (`Config` struct), `internal/pool/pool.go` (`acpSlotConfig`)
- Modify: `internal/session/config.go` (`Config` struct), `internal/session/registry.go` (`createEntry`'s `acp.Config` literal)
- Test: `internal/pool/capture_test.go` (whitebox, `package pool`), `internal/session/capture_test.go` (blackbox, `package session_test`)

**Interfaces:**
- Consumes: `acp.Config.OnRawFrame` (Task 2).
- Produces:
  - `pool.Config.Capture func(method string, params json.RawMessage)` — forwarded onto every slot's `acp.Config.OnRawFrame`.
  - `session.Config.Capture func(method string, params json.RawMessage)` — forwarded onto every entry's `acp.Config.OnRawFrame`.

- [ ] **Step 1: Write the failing pool test**

Create `internal/pool/capture_test.go`:

```go
package pool

import (
	"encoding/json"
	"testing"
)

// TestAcpSlotConfig_ForwardsCapture: Config.Capture is wired onto each slot's
// acp.Config.OnRawFrame; unset leaves it nil.
func TestAcpSlotConfig_ForwardsCapture(t *testing.T) {
	var gotMethod string
	p := New(Config{Capture: func(method string, _ json.RawMessage) { gotMethod = method }})

	cfg := p.acpSlotConfig()
	if cfg.OnRawFrame == nil {
		t.Fatal("acpSlotConfig must wire OnRawFrame when Config.Capture is set")
	}
	cfg.OnRawFrame("session/update", json.RawMessage(`{}`))
	if gotMethod != "session/update" {
		t.Errorf("capture not forwarded: got %q", gotMethod)
	}

	if New(Config{}).acpSlotConfig().OnRawFrame != nil {
		t.Error("OnRawFrame must be nil when Config.Capture is unset")
	}
}
```

- [ ] **Step 2: Run pool test to verify it fails**

Run: `go test ./internal/pool/ -run TestAcpSlotConfig_ForwardsCapture`
Expected: FAIL — `unknown field Capture in struct literal of type Config`.

- [ ] **Step 3a: Add pool Config.Capture + wire it**

In `internal/pool/config.go`, in the `Config` struct, after the `Metrics MetricsRecorder` field, add:

```go
	// Capture, when set, receives every slot's raw inbound kiro frames
	// (Track 0 tool-call wire capture). Optional; nil leaves the acp
	// OnRawFrame hook unset. Wired in cmd/otto-gateway/main.go to the capture
	// ring's Record method when ACP_CAPTURE is enabled.
	Capture func(method string, params json.RawMessage)
```

Add the import `"encoding/json"` to `internal/pool/config.go` if not already present.

In `internal/pool/pool.go`, in `acpSlotConfig`, inside the `if rec := p.cfg.Metrics; rec != nil { ... }` block's surrounding area, after that block and before `return cfg`, add:

```go
	// Track 0 capture: forward raw frames to the ring when enabled.
	if p.cfg.Capture != nil {
		cfg.OnRawFrame = p.cfg.Capture
	}
```

- [ ] **Step 3b: Run pool test to verify it passes**

Run: `go test ./internal/pool/ -run TestAcpSlotConfig_ForwardsCapture`
Expected: PASS.

- [ ] **Step 4: Write the failing session test**

Create `internal/session/capture_test.go`:

```go
package session_test

import (
	"context"
	"encoding/json"
	"testing"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// TestCreateEntry_ForwardsCapture: Config.Capture is wired onto the entry's
// acp.Config.OnRawFrame in createEntry.
func TestCreateEntry_ForwardsCapture(t *testing.T) {
	var capturedCfg acp.Config
	cf := &capturingFactory{cfgSink: &capturedCfg, client: newFake("kiro-1")}

	var gotMethod string
	r := session.New(session.Config{
		Logger:  testutil.Logger(t),
		Factory: cf,
		Capture: func(method string, _ json.RawMessage) { gotMethod = method },
	})
	t.Cleanup(func() { _ = r.Close() })

	if _, err := r.Get(context.Background(), "sid", "/tmp"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if capturedCfg.OnRawFrame == nil {
		t.Fatal("createEntry did not wire OnRawFrame")
	}
	capturedCfg.OnRawFrame("session/update", json.RawMessage(`{}`))
	if gotMethod != "session/update" {
		t.Errorf("capture not forwarded: got %q", gotMethod)
	}
}
```

> **Note:** `capturingFactory` and `newFake` already exist in `internal/session/recycle_test.go` (same `session_test` package) — reuse them.

- [ ] **Step 5: Run session test to verify it fails**

Run: `go test ./internal/session/ -run TestCreateEntry_ForwardsCapture`
Expected: FAIL — `unknown field Capture in struct literal of type Config`.

- [ ] **Step 6a: Add session Config.Capture + wire it**

In `internal/session/config.go`, in the `Config` struct, after the `Metrics MetricsRecorder` field, add:

```go
	// Capture, when set, receives every session's raw inbound kiro frames
	// (Track 0 tool-call wire capture). Optional; nil leaves the acp OnRawFrame
	// hook unset. Wired in cmd/otto-gateway/main.go to the capture ring.
	Capture func(method string, params json.RawMessage)
```

Add the import `"encoding/json"` to `internal/session/config.go` if not already present.

In `internal/session/registry.go`, in `createEntry`, in the `acp.Config{...}` literal passed to `r.cfg.Factory.Spawn`, after the `OnMCPInit: r.recorderMCPInit(),` line, add:

```go
		OnRawFrame: r.cfg.Capture, // Track 0 capture (nil when disabled)
```

(Passing the nil-or-set func directly is correct — a nil `Capture` yields a nil `OnRawFrame`, which the acp readLoop no-ops.)

- [ ] **Step 6b: Run session test to verify it passes**

Run: `go test ./internal/session/ -run TestCreateEntry_ForwardsCapture`
Expected: PASS.

- [ ] **Step 7: Run both full packages**

Run: `go test -race ./internal/pool/ ./internal/session/`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
go run mvdan.cc/gofumpt@latest -w internal/pool/ internal/session/
git add internal/pool/ internal/session/
git commit -m "feat(pool,session): forward raw-frame capture hook to the ring

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2"
```

---

### Task 4: admin `GET /admin/api/acp-capture` endpoint

**Files:**
- Modify: `internal/admin/admin.go` (add `AcpCaptureSource` interface, `CaptureFrame` type, `Deps.AcpCapture` field, route registration)
- Create: `internal/admin/capture.go` (the handler)
- Test: `internal/admin/capture_test.go` (blackbox, `package admin_test` — match the existing handler tests' package)

**Interfaces:**
- Consumes: nothing (admin stays boundary-clean; the ring is adapted at the cmd layer in Task 6).
- Produces:
  - `type CaptureFrame struct { Seq uint64; Ts time.Time; Method string; Params string; Bytes int }` (admin-owned wire type, JSON-tagged)
  - `type AcpCaptureSource interface { Snapshot() []CaptureFrame }`
  - `Deps.AcpCapture AcpCaptureSource` (optional; nil = capture disabled)
  - `GET /admin/api/acp-capture` → `{"enabled":bool,"frames":[CaptureFrame...]}`

- [ ] **Step 1: Write the failing test**

Create `internal/admin/capture_test.go`. First check `internal/admin/handlers_test.go` / `snapshot_test.go` for the test package name and the helper that builds a `Handler(Deps{...})` and issues a request; mirror it. The test:

```go
package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"otto-gateway/internal/admin"
)

type fakeCaptureSource struct{ frames []admin.CaptureFrame }

func (f fakeCaptureSource) Snapshot() []admin.CaptureFrame { return f.frames }

func doGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestAcpCapture_Enabled: with a source wired, the endpoint returns enabled:true
// and the frames as JSON.
func TestAcpCapture_Enabled(t *testing.T) {
	src := fakeCaptureSource{frames: []admin.CaptureFrame{
		{Seq: 1, Ts: time.Unix(1700000000, 0).UTC(), Method: "session/update", Params: `{"x":1}`, Bytes: 7},
	}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Enabled bool                 `json:"enabled"`
		Frames  []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Enabled {
		t.Error("enabled = false, want true")
	}
	if len(body.Frames) != 1 || body.Frames[0].Method != "session/update" {
		t.Errorf("frames = %+v", body.Frames)
	}
}

// TestAcpCapture_Disabled: with no source, the endpoint reports enabled:false and
// an empty (non-nil) frames array.
func TestAcpCapture_Disabled(t *testing.T) {
	h := admin.Handler(admin.Deps{})
	rec := doGet(t, h, "/api/acp-capture")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Enabled bool                 `json:"enabled"`
		Frames  []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Enabled {
		t.Error("enabled = true, want false when no source wired")
	}
	if body.Frames == nil {
		t.Error("frames must be a non-nil (empty) array")
	}
}
```

> **Note:** if `handlers_test.go` already defines a `doGet`-style helper, delete the local one and use theirs to avoid a duplicate-symbol collision.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/admin/ -run TestAcpCapture`
Expected: FAIL — `undefined: admin.CaptureFrame` / `unknown field AcpCapture`.

- [ ] **Step 3a: Add the type, interface, and Deps field**

In `internal/admin/admin.go`, next to `RegistryStatsSource` (~line 48), add:

```go
// CaptureFrame is admin's own wire type for one captured raw ACP frame — the
// cmd layer adapts internal/capture.Frame into this, keeping admin free of an
// internal/capture import (TRST-04, same pattern as SnapshotSess).
type CaptureFrame struct {
	Seq    uint64    `json:"seq"`
	Ts     time.Time `json:"ts"`
	Method string    `json:"method"`
	Params string    `json:"params"`
	Bytes  int       `json:"bytes"`
}

// AcpCaptureSource is the consumer-defined interface the admin handler uses to
// read the raw-frame capture ring. Returns admin's own wire type so this
// package stays boundary-clean. nil in Deps means capture is disabled.
type AcpCaptureSource interface {
	Snapshot() []CaptureFrame
}
```

Confirm `"time"` is already imported in `admin.go` (it is — `Deps.Start` uses it).

In the `Deps` struct, add a field (place it near `PoolDetail`/`Registry`):

```go
	// AcpCapture, when non-nil, exposes the raw-frame capture ring at
	// GET /admin/api/acp-capture (Track 0). nil renders enabled:false.
	AcpCapture AcpCaptureSource
```

In `Handler`, after the `r.Get("/api/snapshot", h.snapshotHandler)` line, add:

```go
	// GET /api/acp-capture — raw kiro frame capture ring (Track 0; enabled:false when unset).
	r.Get("/api/acp-capture", h.acpCaptureHandler)
```

- [ ] **Step 3b: Write the handler**

Create `internal/admin/capture.go`:

```go
package admin

import (
	"encoding/json"
	"net/http"
)

// acpCaptureResponse is the GET /admin/api/acp-capture body.
type acpCaptureResponse struct {
	Enabled bool           `json:"enabled"`
	Frames  []CaptureFrame `json:"frames"`
}

// acpCaptureHandler serves the raw-frame capture ring as JSON. When no source is
// wired (ACP_CAPTURE off), it reports enabled:false with an empty frames array —
// a 200 (not 404) so a harness can distinguish "off" from "route missing".
func (h *handler) acpCaptureHandler(w http.ResponseWriter, _ *http.Request) {
	resp := acpCaptureResponse{Frames: []CaptureFrame{}}
	if h.deps.AcpCapture != nil {
		resp.Enabled = true
		if fr := h.deps.AcpCapture.Snapshot(); fr != nil {
			resp.Frames = fr
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.deps.Logger.Warn("admin: acp-capture encode failed", "err", err)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/admin/ -run TestAcpCapture`
Expected: PASS (both sub-tests).

- [ ] **Step 5: Run full admin package + arch-lint**

Run: `go test ./internal/admin/` → PASS.
Run: `make arch-lint` (or the project's arch-lint command) → PASS (admin still imports no forbidden packages).

- [ ] **Step 6: Commit**

```bash
go run mvdan.cc/gofumpt@latest -w internal/admin/
git add internal/admin/
git commit -m "feat(admin): GET /admin/api/acp-capture — raw-frame ring as JSON

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2"
```

---

### Task 5: config — `ACP_CAPTURE` + `ACP_CAPTURE_SIZE`

**Files:**
- Modify: `internal/config/config.go` (`Config` struct, parse block, assignment)
- Test: `internal/config/acp_capture_test.go`

**Interfaces:**
- Produces: `config.Config.AcpCapture bool` (default false), `config.Config.AcpCaptureSize int` (default 512).

- [ ] **Step 1: Write the failing test**

Create `internal/config/acp_capture_test.go`:

```go
package config_test

import (
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

func TestLoad_AcpCaptureDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AcpCapture {
		t.Error("AcpCapture default = true, want false")
	}
	if cfg.AcpCaptureSize != 512 {
		t.Errorf("AcpCaptureSize default = %d, want 512", cfg.AcpCaptureSize)
	}
}

func TestLoad_AcpCaptureEnabled(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("ACP_CAPTURE", "true")
	t.Setenv("ACP_CAPTURE_SIZE", "128")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AcpCapture {
		t.Error("AcpCapture = false, want true")
	}
	if cfg.AcpCaptureSize != 128 {
		t.Errorf("AcpCaptureSize = %d, want 128", cfg.AcpCaptureSize)
	}
}

func TestLoad_AcpCaptureSizeRejectsNonPositive(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("ACP_CAPTURE_SIZE", "0")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "ACP_CAPTURE_SIZE") {
		t.Fatalf("ACP_CAPTURE_SIZE=0 must be rejected, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoad_AcpCapture`
Expected: FAIL — `cfg.AcpCapture undefined`.

- [ ] **Step 3a: Parse the env vars**

In `internal/config/config.go`, near the `CTX_RECYCLE_PCT` parse block, add:

```go
	// Track 0 tool-call wire capture. Off by default; ACP_CAPTURE_SIZE bounds
	// the in-memory ring. Diagnostic mode — records raw prompt/response content,
	// admin-only, never persisted (see docs/operating.md).
	acpCapture, err := getEnvBool("ACP_CAPTURE", false)
	if err != nil {
		errs = append(errs, err)
	}
	acpCaptureSize, err := getEnvInt("ACP_CAPTURE_SIZE", 512)
	if err != nil {
		errs = append(errs, err)
	}
	if acpCaptureSize <= 0 {
		errs = append(errs, fmt.Errorf("ACP_CAPTURE_SIZE: must be > 0, got %d", acpCaptureSize))
	}
```

- [ ] **Step 3b: Add the struct fields**

In `internal/config/config.go`, in the `Config` struct near `RecyclePct`, add:

```go
	// AcpCapture enables the Track 0 raw-frame capture ring + the
	// GET /admin/api/acp-capture endpoint. Off by default (diagnostic mode).
	// Loaded from ACP_CAPTURE.
	AcpCapture bool
	// AcpCaptureSize bounds the capture ring (frames). Default 512. Loaded from
	// ACP_CAPTURE_SIZE; must be > 0.
	AcpCaptureSize int
```

- [ ] **Step 3c: Assign into the returned Config**

In the `Config{...}` literal that `Load` returns, near `RecyclePct: recyclePct,`, add:

```go
		AcpCapture:     acpCapture,
		AcpCaptureSize: acpCaptureSize,
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestLoad_AcpCapture`
Expected: PASS.

- [ ] **Step 5: Run full config package**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
go run mvdan.cc/gofumpt@latest -w internal/config/
git add internal/config/
git commit -m "feat(config): ACP_CAPTURE + ACP_CAPTURE_SIZE (off by default, ring bound)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2"
```

---

### Task 6: wire the ring in `cmd/otto-gateway/main.go` + document

**Files:**
- Modify: `cmd/otto-gateway/main.go` (construct ring, pass `Capture` into pool + session Config, adapt into `admin.Deps.AcpCapture`)
- Modify: `docs/operating.md` (document `ACP_CAPTURE` / `ACP_CAPTURE_SIZE` + the endpoint)

**Interfaces:**
- Consumes: `capture.NewRing` (Task 1), `pool.Config.Capture` + `session.Config.Capture` (Task 3), `admin.Deps.AcpCapture` + `admin.CaptureFrame` (Task 4), `config.Config.AcpCapture`/`AcpCaptureSize` (Task 5).
- Produces: a live `/admin/api/acp-capture` endpoint when `ACP_CAPTURE=true`.

- [ ] **Step 1: Construct the ring (gated) near the metrics wiring**

In `cmd/otto-gateway/main.go`, before the `if cfg.KiroCmd != "" {` block (near where `gwMetrics` is constructed), add:

```go
	// Track 0 tool-call wire capture: construct the ring only when enabled, so
	// the acp OnRawFrame hooks stay nil (zero cost) otherwise. Shared across the
	// pool and the session registry; read by the admin endpoint.
	const acpCaptureFrameCapBytes = 8 * 1024 // per-frame params byte cap
	var acpCaptureRing *capture.Ring
	if cfg.AcpCapture {
		acpCaptureRing = capture.NewRing(cfg.AcpCaptureSize, acpCaptureFrameCapBytes)
		logger.Info("acp raw-frame capture enabled",
			"size", cfg.AcpCaptureSize, "endpoint", "/admin/api/acp-capture")
	}
```

Add `"otto-gateway/internal/capture"` to the import block.

- [ ] **Step 2: Pass Capture into the pool + session Config**

In the `pool.New(pool.Config{...})` literal, after `Metrics: gwMetrics,`, add:

```go
			Capture: captureRecordFunc(acpCaptureRing), // Track 0 (nil ring → nil func → no capture)
```

In the `session.New(session.Config{...})` literal, after `Metrics: gwMetrics,`, add:

```go
			Capture: captureRecordFunc(acpCaptureRing),
```

Add this helper near the other `cmd/otto-gateway` helper funcs (e.g. next to `resolveGatewayID`):

```go
// captureRecordFunc returns the ring's Record method, or nil when capture is
// disabled (nil ring) so the acp OnRawFrame hook stays unset.
func captureRecordFunc(ring *capture.Ring) func(method string, params json.RawMessage) {
	if ring == nil {
		return nil
	}
	return ring.Record
}
```

Ensure `"encoding/json"` is imported in `main.go` (it is used widely; confirm).

- [ ] **Step 3: Adapt the ring into admin.Deps.AcpCapture**

Add an adapter type near the other `cmd/otto-gateway` admin adapters:

```go
// acpCaptureAdapter adapts *capture.Ring to admin.AcpCaptureSource, converting
// capture.Frame into admin's own CaptureFrame wire type (TRST-04 boundary).
type acpCaptureAdapter struct{ ring *capture.Ring }

func (a acpCaptureAdapter) Snapshot() []admin.CaptureFrame {
	frames := a.ring.Snapshot()
	out := make([]admin.CaptureFrame, len(frames))
	for i, f := range frames {
		out[i] = admin.CaptureFrame{
			Seq: f.Seq, Ts: f.Ts, Method: f.Method, Params: f.Params, Bytes: f.Bytes,
		}
	}
	return out
}
```

In the `admin.Handler(admin.Deps{...})` literal, after `Registry: adminRegistry,`, add:

```go
		AcpCapture:   adminAcpCapture(acpCaptureRing),
```

Add the helper (returns nil interface when the ring is nil, so the handler reports enabled:false):

```go
// adminAcpCapture wraps the ring as an admin.AcpCaptureSource, or returns nil
// when capture is disabled so the endpoint renders enabled:false.
func adminAcpCapture(ring *capture.Ring) admin.AcpCaptureSource {
	if ring == nil {
		return nil
	}
	return acpCaptureAdapter{ring: ring}
}
```

- [ ] **Step 4: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no output (success).

- [ ] **Step 5: Manual smoke test (no kiro needed)**

Run in one shell:

```bash
ACP_CAPTURE=true HTTP_ADDR=127.0.0.1:18099 ENABLED_SURFACES=ollama go run ./cmd/otto-gateway &
```

Then:

```bash
sleep 2
curl -s http://127.0.0.1:18099/admin/api/acp-capture
```

Expected: `{"enabled":true,"frames":[]}` (no frames yet — no kiro traffic). Then with `ACP_CAPTURE` unset, the same curl returns `{"enabled":false,"frames":[]}`. Kill the process.

> If `KIRO_CMD` is unset the pool/session are not built; the endpoint still renders from the (empty) ring. That's the intended degraded behavior.

- [ ] **Step 6: Document the mode**

In `docs/operating.md`, add a subsection (match the file's existing heading style):

```markdown
### ACP raw-frame capture (diagnostic)

`ACP_CAPTURE=true` enables an in-memory ring of the raw kiro ACP frames the
gateway receives, exposed at `GET /admin/api/acp-capture` (behind the admin
IP-allowlist). `ACP_CAPTURE_SIZE` bounds the ring (default 512 frames; per-frame
params are truncated to 8 KiB). **Off by default.** Capture records raw
prompt/response content, so treat it as a diagnostic mode: enable it only to
investigate wire behavior, and keep the admin surface allowlisted. Frames are
never written to disk by the gateway.
```

- [ ] **Step 7: Full gate sweep + commit**

Run:
```bash
go test ./... && go vet ./... && go run mvdan.cc/gofumpt@latest -l . && CGO_ENABLED=0 go build ./cmd/otto-gateway && GOOS=linux go build ./...
```
Expected: tests pass, gofumpt prints nothing, builds succeed.

```bash
go run mvdan.cc/gofumpt@latest -w cmd/ docs/ 2>/dev/null; go run mvdan.cc/gofumpt@latest -w cmd/otto-gateway/main.go
git add cmd/otto-gateway/main.go docs/operating.md
git commit -m "feat(gateway): wire ACP capture ring into pool/session + admin endpoint

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2"
```

---

### Task 7: real-kiro capture-and-classify harness + findings report

**Files:**
- Create: `tests/track0_capture_test.go` (build-tag `//go:build kirolive`)
- Create: `docs/reviews/2026-07-14-track0-toolcall-findings.md` (the harness writes/updates this; commit the generated report)

**Interfaces:**
- Consumes: a running gateway with `ACP_CAPTURE=true` reachable at `GW_URL` (default `http://127.0.0.1:18099`), plus real `kiro-cli`.
- Produces: `docs/reviews/2026-07-14-track0-toolcall-findings.md` — the Track 3 scoping deliverable.

- [ ] **Step 1: Confirm the `tests/` layout + module path**

Run: `ls tests/ 2>/dev/null; head -1 go.mod`
Expected: note whether a `tests/` dir + package convention already exists (e.g. `tests/e2e`). If `tests/` has an existing package name, put the file in a matching new subdir `tests/track0/` with `package track0` and adjust the build command paths below accordingly. Module path is `otto-gateway`.

- [ ] **Step 2: Write the harness test**

Create `tests/track0_capture_test.go`:

```go
//go:build kirolive

// Track 0 real-kiro capture harness. NOT run in normal CI — build-tag gated.
//
// Prereqs: a gateway running with ACP_CAPTURE=true and real kiro-cli, reachable
// at GW_URL (default http://127.0.0.1:18099). Run:
//
//	KIRO_CMD=kiro-cli ACP_CAPTURE=true HTTP_ADDR=127.0.0.1:18099 \
//	  ENABLED_SURFACES=ollama,anthropic,openai go run ./cmd/otto-gateway &
//	go test -tags kirolive ./tests/ -run TestTrack0Capture -v
//
// It drives one tool round-trip per surface, reads /admin/api/acp-capture,
// classifies kiro's tool-call transport/name-fidelity/JSON-robustness, and
// writes docs/reviews/2026-07-14-track0-toolcall-findings.md.
package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func gwURL() string {
	if v := os.Getenv("GW_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:18099"
}

type captureFrame struct {
	Seq    uint64 `json:"seq"`
	Method string `json:"method"`
	Params string `json:"params"`
	Bytes  int    `json:"bytes"`
}

type captureResp struct {
	Enabled bool           `json:"enabled"`
	Frames  []captureFrame `json:"frames"`
}

// surfaceProbe is one surface's tool-declaring request.
type surfaceProbe struct {
	name    string
	path    string
	headers map[string]string
	body    string
}

func probes() []surfaceProbe {
	weatherPrompt := "What is the weather in Paris? Use the get_weather tool."
	return []surfaceProbe{
		{
			name: "anthropic", path: "/v1/messages",
			headers: map[string]string{"anthropic-version": "2023-06-01"},
			body: `{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"` + weatherPrompt + `"}],` +
				`"tools":[{"name":"get_weather","description":"Get weather for a city",` +
				`"input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}`,
		},
		{
			name: "openai", path: "/v1/chat/completions",
			body: `{"model":"auto","messages":[{"role":"user","content":"` + weatherPrompt + `"}],` +
				`"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather for a city",` +
				`"parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]}`,
		},
		{
			name: "ollama", path: "/api/chat",
			body: `{"model":"auto","stream":false,"messages":[{"role":"user","content":"` + weatherPrompt + `"}],` +
				`"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather for a city",` +
				`"parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]}`,
		},
	}
}

func TestTrack0Capture(t *testing.T) {
	client := &http.Client{Timeout: 150 * time.Second}

	// Sanity: capture must be enabled.
	if !fetchCapture(t, client).Enabled {
		t.Fatal("capture is not enabled — start the gateway with ACP_CAPTURE=true")
	}

	var report bytes.Buffer
	fmt.Fprintf(&report, "# Track 0 — kiro tool-call wire findings\n\n**Date:** 2026-07-14\n\n")
	fmt.Fprintf(&report, "Generated by tests/track0_capture_test.go (real kiro). Each surface drove a\n"+
		"`get_weather` tool round-trip; the raw kiro frames captured for that turn are\nclassified below.\n\n")

	for _, p := range probes() {
		before := lastSeq(fetchCapture(t, client))
		resp := drive(t, client, p)
		// Give kiro's trailing frames a moment to land in the ring.
		time.Sleep(1 * time.Second)
		frames := newFramesSince(fetchCapture(t, client), before)

		if len(frames) == 0 {
			t.Errorf("[%s] no frames captured for the round-trip", p.name)
		}
		classifyAndWrite(&report, p.name, resp, frames)
	}

	path := "../docs/reviews/2026-07-14-track0-toolcall-findings.md"
	if err := os.WriteFile(path, report.Bytes(), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}
	t.Logf("wrote findings report to %s", path)
}

func fetchCapture(t *testing.T, c *http.Client) captureResp {
	t.Helper()
	resp, err := c.Get(gwURL() + "/admin/api/acp-capture")
	if err != nil {
		t.Fatalf("GET capture: %v", err)
	}
	defer resp.Body.Close()
	var cr captureResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatalf("decode capture: %v", err)
	}
	return cr
}

func drive(t *testing.T, c *http.Client, p surfaceProbe) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, gwURL()+p.path, strings.NewReader(p.body))
	if err != nil {
		t.Fatalf("[%s] build req: %v", p.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("[%s] POST %s: %v", p.name, p.path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func lastSeq(cr captureResp) uint64 {
	var max uint64
	for _, f := range cr.Frames {
		if f.Seq > max {
			max = f.Seq
		}
	}
	return max
}

func newFramesSince(cr captureResp, since uint64) []captureFrame {
	var out []captureFrame
	for _, f := range cr.Frames {
		if f.Seq > since {
			out = append(out, f)
		}
	}
	return out
}

// classifyAndWrite answers the taxonomy for one surface and appends to report.
func classifyAndWrite(report *bytes.Buffer, surface, clientResp string, frames []captureFrame) {
	nativeToolCall := false
	freeTextToolCall := false
	sawDeclaredName := false
	sawKind := false
	var toolMethods []string

	for _, f := range frames {
		lower := strings.ToLower(f.Params)
		if strings.Contains(f.Method, "update") || strings.Contains(f.Method, "notification") {
			if strings.Contains(lower, `"sessionupdate":"tool_call"`) ||
				strings.Contains(lower, `"tool_call_chunk"`) ||
				strings.Contains(lower, `"kind"`) {
				nativeToolCall = true
				toolMethods = append(toolMethods, f.Method)
			}
			if strings.Contains(lower, `{"tool_call"`) || strings.Contains(lower, `\"tool_call\"`) {
				freeTextToolCall = true
			}
		}
		if strings.Contains(f.Params, "get_weather") {
			sawDeclaredName = true
		}
		if strings.Contains(lower, `"kind"`) {
			sawKind = true
		}
	}

	fmt.Fprintf(report, "## %s\n\n", surface)
	fmt.Fprintf(report, "- Frames captured this turn: %d\n", len(frames))
	fmt.Fprintf(report, "- **Transport:** native ACP tool_call=%v, free-text {\"tool_call\":…}=%v\n", nativeToolCall, freeTextToolCall)
	fmt.Fprintf(report, "- **Tool-name fidelity:** declared name (get_weather) seen=%v, kiro `kind` field seen=%v\n", sawDeclaredName, sawKind)
	if len(toolMethods) > 0 {
		fmt.Fprintf(report, "- Tool-bearing notification methods: %s\n", strings.Join(toolMethods, ", "))
	}
	fmt.Fprintf(report, "- Client-visible response (first 400 bytes):\n\n```\n%s\n```\n\n", first(clientResp, 400))
	fmt.Fprintf(report, "<details><summary>raw frames</summary>\n\n```json\n")
	for _, f := range frames {
		fmt.Fprintf(report, "%s %s\n", f.Method, first(f.Params, 600))
	}
	fmt.Fprintf(report, "```\n\n</details>\n\n")
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
```

> **Implementer notes:**
> - The classification heuristics (`strings.Contains` probes) are a starting point. After the FIRST live run, read the actual captured frames and refine the probes to match kiro's real field names before writing the final report — the raw-frames `<details>` block exists precisely so you can see what to key on.
> - If `/v1/messages` needs auth headers in this deployment, add them to the anthropic probe's `headers`.

- [ ] **Step 3: Verify the harness compiles under the build tag**

Run: `go vet -tags kirolive ./tests/`
Expected: no output (compiles). It does NOT run without the tag, so normal `go test ./...` is unaffected — verify: `go test ./tests/ 2>&1 | tail -2` shows `no test files` or the package's existing tests only.

- [ ] **Step 4: Live run — capture + classify**

Start the gateway with real kiro + capture:

```bash
KIRO_CMD=kiro-cli KIRO_ARGS=acp KIRO_CWD="$PWD" ACP_CAPTURE=true \
  HTTP_ADDR=127.0.0.1:18099 ENABLED_SURFACES=ollama,anthropic,openai \
  go run ./cmd/otto-gateway &
```

Wait for readiness (`curl -sf http://127.0.0.1:18099/health`), then:

```bash
go test -tags kirolive ./tests/ -run TestTrack0Capture -v
```

Expected: PASS; it writes `docs/reviews/2026-07-14-track0-toolcall-findings.md`. Read that file. If the transport/name classification looks wrong versus the raw-frames blocks, refine the heuristics in Step 2 and re-run. Kill the gateway when done.

- [ ] **Step 5: Finalize the findings report**

Open `docs/reviews/2026-07-14-track0-toolcall-findings.md`. Add a short **"Track 3 scope"** section at the top mapping each observation to a Track 3 sub-item with `needed | not-needed | uncertain`:

- Free-text extractor hardening (embedded `{"tool_call":…}` scan) — needed only if any surface showed free-text transport.
- Kiro→client tool-name reconciliation — needed only if the declared name (`get_weather`) was NOT echoed (kiro used `kind`/a built-in).
- Truncated-JSON repair — needed only if any captured tool frame showed unbalanced/truncated JSON.
- Structured `tool_calls` surfacing on OpenAI/Ollama — needed if native tool_call frames appeared (they currently render as `[tool: …]` narration).

- [ ] **Step 6: Commit**

```bash
git add tests/ docs/reviews/2026-07-14-track0-toolcall-findings.md
git commit -m "test(track0): real-kiro tool-call capture harness + findings report

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Fp4BYLd1ePrHjea1Nc2Ci2"
```

---

## Verification (whole plan)

- [ ] `go build ./...` — success.
- [ ] `go test ./...` — pass (harness excluded without `-tags kirolive`).
- [ ] `go test -race ./internal/capture/ ./internal/pool/ ./internal/session/` — pass.
- [ ] `go vet ./...` — clean.
- [ ] `go run mvdan.cc/gofumpt@latest -l .` — empty.
- [ ] `CGO_ENABLED=0 go build ./cmd/otto-gateway` — success.
- [ ] `GOOS=linux go build ./...` and `GOOS=windows go build ./cmd/otto-tray/...` — success.
- [ ] `make arch-lint` — pass (admin still boundary-clean; `capture` registered as a leaf).
- [ ] golangci-lint on touched packages — no new findings vs baseline.
- [ ] LIVE: `ACP_CAPTURE=true` + real kiro; the harness produces the findings report and each surface captured ≥1 frame.

## Notes for the executor

- The harness's classification is exploratory: its VALUE is the written report, not a green checkmark. Expect to iterate the `strings.Contains` heuristics against the first run's raw frames.
- Do not push or merge without the human's OK (origin dual-pushes to a GitLab mirror needing interactive auth; push to GitHub explicitly when asked).
