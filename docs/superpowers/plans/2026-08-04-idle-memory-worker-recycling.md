# Idle-memory Kiro Worker Recycling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eagerly replace a pooled Kiro worker after it has served a user request, remained free for at least the configured idle duration, and exceeded the configured direct-process working-set threshold.

**Architecture:** Track user-request activity on each stable pool slot, then run a pool-owned periodic sweep that claims only free slots before sampling memory. Route both max-turn and idle-memory triggers through the existing serialized scheduled-recycle machinery, and expose the new state through bounded Prometheus metrics, admin snapshot/cards, wrapper configuration, and the generated Grafana dashboard.

**Tech Stack:** Go 1.26.5, stdlib concurrency primitives, `golang.org/x/sys/windows`, Prometheus client collectors, Node's built-in test runner, Python `unittest`, generated Grafana JSON, POSIX shell, and PowerShell.

## Global Constraints

- The main gateway binary remains cgo-free and cross-compilable for Windows, Linux, and macOS.
- The policy applies only to stateless pooled workers; do not modify `internal/session` or `SESSION_TTL_MS` behavior.
- Dedicated `X-Session-Id` workers are out of scope and must retain their existing lifecycle.
- Windows and Linux enforce memory recycling; macOS reports it unsupported and performs no memory-triggered recycle.
- Binary defaults are `KIRO_WORKER_IDLE_RECYCLE_MS=0` and `KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB=500`.
- Laptop wrapper defaults are `KIRO_WORKER_IDLE_RECYCLE_MS=900000` and `KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB=500`.
- Idle eligibility uses `idle >= threshold`; memory eligibility uses direct worker working set `> threshold * 1024 * 1024`.
- `KIRO_WORKER_IDLE_RECYCLE_MS=0` disables the policy; memory must be in `1..1_048_576` MiB.
- Catalog probes continue to count toward `Slot.turns` but never count as user requests.
- A worker is never sampled or recycled while checked out; the sweep must own it by receiving it from `Pool.slots`.
- Max-turn and idle-memory triggers share the one-scheduled-recycle-at-a-time capacity guard.
- Do not add session IDs, user IDs, prompts, paths, or other unbounded/sensitive values to metrics or recycle logs.
- Do not aggregate process-tree memory and do not estimate or publish bytes freed; sample only the direct worker process.
- Preserve `gw_pool_slot_recycles_total`; additive metrics and JSON fields must remain backward-compatible.
- `scripts/gen_grafana_dashboard.py` is authoritative; regenerate, do not hand-edit only the JSON.

---

## File Structure and Locked Interfaces

| File | Responsibility in this change |
|---|---|
| `internal/config/config.go` | Parse and validate the two environment knobs. |
| `internal/config/worker_idle_recycle_test.go` | Lock defaults, duration parsing, bounds, and named errors. |
| `internal/procstat/procstat_{linux,windows,other}.go` | Report whether direct-process sampling is supported on the compiled platform. |
| `internal/pool/config.go` | Define clock, memory-reader, capability, thresholds, and recycle-metrics seams. |
| `internal/pool/pool.go` | Record user activity, reset worker state, start/stop the sweep, and generalize scheduled recycle admission. |
| `internal/pool/idle_recycle.go` | Own cadence calculation, loop, free-slot scan, memory eligibility, and trigger types. |
| `internal/pool/detail.go` | Project user requests and idle seconds to admin/metrics adapters. |
| `internal/pool/idle_recycle_test.go` | Deterministic lifecycle, threshold, capability, shutdown, and serialization tests. |
| `internal/pool/export_test.go` | Expose direct sweep/tick/wait seams only to tests. |
| `internal/metrics/metrics.go` | Record bounded recycle reasons and trigger histograms. |
| `internal/metrics/collector.go` | Keep the aggregate scheduled-recycle metric accurate for both trigger reasons. |
| `internal/metrics/worker_collector.go` | Export per-slot user requests and idle seconds alongside CPU/RSS. |
| `cmd/otto-gateway/main.go` | Adapt config/procstat/metrics/pool/admin types without adding package cycles. |
| `internal/admin/snapshot.go` | Add policy and per-slot activity fields to the admin JSON contract. |
| `internal/admin/static/js/admin.js` | Render USER REQS, IDLE, and memory/idle thresholds. |
| `scripts/.env.example`, `scripts/gw`, `scripts/gw.ps1` | Ship laptop defaults and expose them in diagnostics/support bundles. |
| `scripts/gen_grafana_dashboard.py` | Generate the Idle Memory Recycling row and queries. |
| `docs/grafana/otto-gateway-dashboard.json` | Checked-in generated dashboard artifact. |
| `docs/operating.md`, `docs/grafana/README.md` | Document configuration, platform behavior, and new observability. |

The following signatures are locked for all tasks:

```go
// internal/procstat
func Supported() bool

// internal/pool
type WorkerMemoryReader func(pid int) (rssBytes uint64, ok bool)

type RecycleMetricsRecorder interface {
	RecordWorkerRecycle(reason string, rssBytes uint64, idle time.Duration)
}

// Add to pool.Config.
IdleRecycleAfter       time.Duration
IdleRecycleMemoryBytes uint64
WorkerMemorySupported  bool
ReadWorkerMemory       WorkerMemoryReader
RecycleMetrics         RecycleMetricsRecorder
Now                    func() time.Time

// Add to pool.AgentSlot and admin.SnapshotSlot.
UserRequestsSinceSpawn uint64
LastUserReleaseAt      *time.Time

// Add to pool.WorkerProc and metrics.WorkerProc.
UserRequestsSinceSpawn uint64
IdleSeconds            float64
```

Recycle reasons are the byte-exact closed set `max_turns` and `idle_memory`.

---

### Task 1: Configuration and platform capability

**Files:**

- Create: `internal/config/worker_idle_recycle_test.go`
- Modify: `internal/config/config.go:147-149,590-630,1029-1048`
- Modify: `internal/procstat/procstat_linux.go`
- Modify: `internal/procstat/procstat_windows.go`
- Modify: `internal/procstat/procstat_other.go`
- Modify: `internal/procstat/procstat_test.go`

**Interfaces:**

- Consumes: existing `getEnvDuration`, `getEnvInt`, and `config.Load` validation aggregation.
- Produces: `config.Config.KiroWorkerIdleRecycleAfter time.Duration`, `config.Config.KiroWorkerIdleRecycleMemoryMB int`, and `procstat.Supported() bool`.

- [ ] **Step 1: Write failing configuration tests**

```go
package config_test

import (
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/config"
)

func TestLoad_WorkerIdleRecycleDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KIRO_WORKER_IDLE_RECYCLE_MS", "")
	t.Setenv("KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KiroWorkerIdleRecycleAfter != 0 || cfg.KiroWorkerIdleRecycleMemoryMB != 500 {
		t.Fatalf("idle policy = (%v,%d), want (0,500)", cfg.KiroWorkerIdleRecycleAfter, cfg.KiroWorkerIdleRecycleMemoryMB)
	}
}

func TestLoad_WorkerIdleRecycleOverrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KIRO_WORKER_IDLE_RECYCLE_MS", "15m")
	t.Setenv("KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "768")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KiroWorkerIdleRecycleAfter != 15*time.Minute || cfg.KiroWorkerIdleRecycleMemoryMB != 768 {
		t.Fatalf("idle policy = (%v,%d), want (15m,768)", cfg.KiroWorkerIdleRecycleAfter, cfg.KiroWorkerIdleRecycleMemoryMB)
	}
}

func TestLoad_WorkerIdleRecycleMilliseconds(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KIRO_WORKER_IDLE_RECYCLE_MS", "900000")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KiroWorkerIdleRecycleAfter != 15*time.Minute {
		t.Fatalf("idle duration = %v, want 15m", cfg.KiroWorkerIdleRecycleAfter)
	}
}

func TestLoad_WorkerIdleRecycleRejectsInvalidValues(t *testing.T) {
	tests := []struct{ key, value string }{
		{"KIRO_WORKER_IDLE_RECYCLE_MS", "-1"},
		{"KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "0"},
		{"KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "-1"},
		{"KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "1048577"},
		{"KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "9223372036854775808"},
	}
	for _, tc := range tests {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv("HTTP_ADDR", "127.0.0.1:0")
			t.Setenv(tc.key, tc.value)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("Load error = %v, want named %s error", err, tc.key)
			}
		})
	}
}
```

- [ ] **Step 2: Run the configuration tests and confirm RED**

Run:

```bash
go test ./internal/config -run WorkerIdleRecycle -count=1
```

Expected: compile failure because the two `config.Config` fields do not exist.

- [ ] **Step 3: Add the configuration fields, parsing, and validation**

Add to `config.Config`:

```go
KiroWorkerIdleRecycleAfter    time.Duration
KiroWorkerIdleRecycleMemoryMB int
```

Add beside `KIRO_WORKER_MAX_TURNS` parsing in `Load`:

```go
workerIdleRecycleAfter, err := getEnvDuration("KIRO_WORKER_IDLE_RECYCLE_MS", 0)
if err != nil {
	errs = append(errs, err)
}
if workerIdleRecycleAfter < 0 {
	errs = append(errs, fmt.Errorf("KIRO_WORKER_IDLE_RECYCLE_MS: must be >= 0, got %v", workerIdleRecycleAfter))
}

workerIdleRecycleMemoryMB, err := getEnvInt("KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", 500)
if err != nil {
	errs = append(errs, err)
}
if workerIdleRecycleMemoryMB <= 0 {
	errs = append(errs, fmt.Errorf("KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB: must be > 0, got %d", workerIdleRecycleMemoryMB))
}
if workerIdleRecycleMemoryMB > 1_048_576 {
	errs = append(errs, fmt.Errorf("KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB: sanity cap exceeded (max 1048576), got %d", workerIdleRecycleMemoryMB))
}
```

Populate both fields in the returned `Config` literal.

- [ ] **Step 4: Add a compile-time platform capability and its failing test**

Append to `internal/procstat/procstat_test.go`:

```go
func TestSupported_PlatformContract(t *testing.T) {
	want := runtime.GOOS == "linux" || runtime.GOOS == "windows"
	if got := Supported(); got != want {
		t.Fatalf("Supported() on %s = %v, want %v", runtime.GOOS, got, want)
	}
}
```

Run:

```bash
go test ./internal/procstat -run Supported -count=1
```

Expected: compile failure because `Supported` is undefined.

- [ ] **Step 5: Implement the platform capability**

Add this function to both Linux and Windows implementations:

```go
func Supported() bool { return true }
```

Add this function to `procstat_other.go`:

```go
func Supported() bool { return false }
```

- [ ] **Step 6: Run focused tests, format, and confirm GREEN**

Run:

```bash
gofumpt -w internal/config/config.go internal/config/worker_idle_recycle_test.go internal/procstat
go test ./internal/config ./internal/procstat -run 'WorkerIdleRecycle|Supported' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the configuration contract**

```bash
git add internal/config/config.go internal/config/worker_idle_recycle_test.go internal/procstat
git commit -m "feat(config): add idle-memory recycle thresholds"
```

---

### Task 2: Per-worker user activity accounting

**Files:**

- Modify: `internal/pool/config.go:88-165`
- Modify: `internal/pool/pool.go:111-160,330-345,730-745,1330-1380,1400-1570`
- Modify: `internal/pool/detail.go:26-180`
- Modify: `internal/pool/export_test.go`
- Create: `internal/pool/idle_recycle_test.go`

**Interfaces:**

- Consumes: successful request-path `Pool.NewSession`, exact-once session-map deletion, `respawnSlot` swap critical section, and `Pool.Detail`/`WorkerProcs` projections.
- Produces: `Config.Now`, `Slot.userRequestsSinceSpawn`, `Slot.lastUserReleaseAt`, additive `AgentSlot` fields, and additive `WorkerProc` activity fields.

- [ ] **Step 1: Write a failing user-activity lifecycle test**

Add to `internal/pool/idle_recycle_test.go` in `package pool_test`, reusing `fakeClient`, `fakeClientFactory`, `runOneRequest`, and `drainChunks` from existing pool tests:

```go
func TestPool_UserActivityExcludesCatalogAndStartsAtRelease(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 1101}
	p := pool.New(pool.Config{
		Logger:  testutil.Logger(t),
		Size:    1,
		Factory: &fakeClientFactory{clients: []pool.PoolClient{client}},
		Now:     func() time.Time { return now },
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := p.Detail()[0]
	if before.UserRequestsSinceSpawn != 0 || before.LastUserReleaseAt != nil {
		t.Fatalf("warmup activity = %+v, want unused", before)
	}

	now = now.Add(5 * time.Minute)
	runOneRequest(t, p)
	after := p.Detail()[0]
	if after.UserRequestsSinceSpawn != 1 || after.LastUserReleaseAt == nil || !after.LastUserReleaseAt.Equal(now) {
		t.Fatalf("request activity = %+v, want count=1 release=%v", after, now)
	}
}
```

- [ ] **Step 2: Run the lifecycle test and confirm RED**

```bash
go test ./internal/pool -run UserActivityExcludesCatalog -count=1
```

Expected: compile failure for missing `Config.Now` and `AgentSlot` fields.

- [ ] **Step 3: Add the clock seam and slot fields**

Add to `pool.Config`:

```go
Now func() time.Time
```

Default it in `applyDefaults`:

```go
if c.Now == nil {
	c.Now = time.Now
}
```

Add to `Slot`:

```go
userRequestsSinceSpawn uint64
lastUserReleaseAt      time.Time
```

Increment only in the successful normal `Pool.NewSession` registration block:

```go
p.sessionSlots[sid] = slot
slot.turns++
slot.userRequestsSinceSpawn++
```

Do not modify `probeCatalogOnce` beyond its existing `slot.turns++`.

- [ ] **Step 4: Timestamp the exact-once release winner**

In both the stream `release` closure and `releaseSlotForSession`, set the timestamp inside the same `p.mu` block that wins map deletion:

```go
if stillOwned {
	delete(p.sessionSlots, sid)
	s.lastUserReleaseAt = p.cfg.Now().UTC()
}
```

and:

```go
if ok {
	delete(p.sessionSlots, sid)
	slot.lastUserReleaseAt = p.cfg.Now().UTC()
}
```

This must happen before unlocking and before `releaseOrRecycle`.

- [ ] **Step 5: Reset activity atomically with worker replacement**

In `respawnSlot`'s successful swap block, next to the turn and spawn reset:

```go
slot.turns = 0
slot.userRequestsSinceSpawn = 0
slot.lastUserReleaseAt = time.Time{}
slot.spawnedAt = p.cfg.Now()
```

Use `p.cfg.Now()` for initial `spawnedAt` in `initSlot` as well, keeping tests deterministic.

- [ ] **Step 6: Extend detail and worker projections**

Add to `AgentSlot`:

```go
UserRequestsSinceSpawn uint64     `json:"user_requests_since_spawn"`
LastUserReleaseAt      *time.Time `json:"last_user_release_at"`
```

Add to `WorkerProc`:

```go
UserRequestsSinceSpawn uint64
IdleSeconds            float64
```

In `detailSnapshotLocked`, copy the counter and allocate a timestamp pointer only when non-zero. In `WorkerProcs`, build a set of checked-out slots from `sessionSlots`; emit `IdleSeconds=0` for unused or busy workers, otherwise:

```go
idleSeconds := p.cfg.Now().Sub(slot.lastUserReleaseAt).Seconds()
if idleSeconds < 0 {
	idleSeconds = 0
}
```

- [ ] **Step 7: Add terminal-path, projection, and reset coverage**

Add table-driven subtests named `TestPool_UserActivityTerminalPaths` for normal result drainage, request-context cancellation, explicit `Pool.Cancel`, and `Prompt` error. Each subtest must assert count `1`, the exact injected release time, and exactly one free-slot return. Add `TestPool_UserActivityLaterRequestResetsIdle` to advance the clock, run a second request, and require count `2` plus the second release timestamp. Add `TestPool_WorkerProcsActivitySemantics` to require zero idle before first use and while busy, then the injected elapsed seconds after release. Extend the existing successful recycle test to assert both activity fields reset on the replacement.

Use this assertion helper in `idle_recycle_test.go`:

```go
func assertSlotActivity(t *testing.T, p *pool.Pool, requests uint64, releasedAt *time.Time) {
	t.Helper()
	row := p.Detail()[0]
	if row.UserRequestsSinceSpawn != requests {
		t.Fatalf("user requests = %d, want %d", row.UserRequestsSinceSpawn, requests)
	}
	if diff := cmp.Diff(releasedAt, row.LastUserReleaseAt); diff != "" {
		t.Fatalf("last release mismatch (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 8: Run pool tests under the race detector and commit**

```bash
gofumpt -w internal/pool
go test -race ./internal/pool -run 'UserActivity|WorkerRecycleAtThreshold' -count=1
git add internal/pool
git commit -m "feat(pool): track worker user activity"
```

Expected: PASS with goleak clean.

---

### Task 3: Idle-memory sweep and unified scheduled recycling

**Files:**

- Modify: `internal/pool/config.go`
- Modify: `internal/pool/pool.go:180-260,366-465,994-1035,1587-1735`
- Create: `internal/pool/idle_recycle.go`
- Modify: `internal/pool/idle_recycle_test.go`
- Modify: `internal/pool/export_test.go`
- Modify: `internal/pool/worker_recycle_test.go`

**Interfaces:**

- Consumes: activity state from Task 2, `Pool.slots` exclusive-ownership channel, `recycleWG`, `recyclesInFlight`, `respawnSlot`, and pool close signal.
- Produces: `WorkerMemoryReader`, `RecycleMetricsRecorder`, idle policy fields on `pool.Config`, `idleSweepCadence`, `sweepIdleWorkers`, and reason-aware `recycleTrigger` passed through scheduled replacement.

- [ ] **Step 1: Write the primary failing idle-memory recycle test**

```go
func TestPool_IdleMemoryRecycle_UsedIdleAboveThreshold(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	oldClient := &fakeClient{models: []canonical.ModelInfo{{ID: "auto"}}, pid: 2101}
	newClient := &fakeClient{pid: 2102}
	p := pool.New(pool.Config{
		Logger:                 testutil.Logger(t),
		Size:                   1,
		Factory:                &fakeClientFactory{clients: []pool.PoolClient{oldClient, newClient}},
		Now:                    func() time.Time { return now },
		IdleRecycleAfter:       15 * time.Minute,
		IdleRecycleMemoryBytes: 500 << 20,
		WorkerMemorySupported:  true,
		ReadWorkerMemory: func(pid int) (uint64, bool) {
			if pid != 2101 {
				t.Fatalf("memory reader pid = %d, want 2101", pid)
			}
			return 800 << 20, true
		},
	})
	defer func() { _ = p.Close() }()
	if err := p.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOneRequest(t, p)
	now = now.Add(15 * time.Minute)
	p.SweepIdleWorkersForTesting()
	p.WaitForRecyclesForTesting()

	if p.Recycles() != 1 || oldClient.closeCallCount() == 0 {
		t.Fatalf("recycles=%d closes=%d, want 1 and >=1", p.Recycles(), oldClient.closeCallCount())
	}
	row := p.Detail()[0]
	if row.Pid != 2102 || row.UserRequestsSinceSpawn != 0 || row.LastUserReleaseAt != nil {
		t.Fatalf("replacement state = %+v", row)
	}
}
```

- [ ] **Step 2: Run the primary test and confirm RED**

```bash
go test ./internal/pool -run IdleMemoryRecycle_UsedIdleAboveThreshold -count=1
```

Expected: compile failure for missing config fields and test seams.

- [ ] **Step 3: Add pool configuration seams and trigger types**

Add to `internal/pool/config.go`:

```go
type WorkerMemoryReader func(pid int) (rssBytes uint64, ok bool)

type RecycleMetricsRecorder interface {
	RecordWorkerRecycle(reason string, rssBytes uint64, idle time.Duration)
}
```

Add the locked fields from the File Structure section to `pool.Config`. Do not default thresholds or capability in `pool.applyDefaults`; `cmd/otto-gateway` owns production wiring. A nil reader means unsupported/disabled.

In `idle_recycle.go`, define:

```go
type recycleReason string

const (
	recycleReasonMaxTurns  recycleReason = "max_turns"
	recycleReasonIdleMemory recycleReason = "idle_memory"
)

type recycleTrigger struct {
	reason       recycleReason
	turns        int
	userRequests uint64
	pid          int
	rssBytes     uint64
	idle         time.Duration
}
```

- [ ] **Step 4: Implement deterministic cadence and loop lifecycle**

```go
func idleSweepCadence(after time.Duration) time.Duration {
	d := after / 4
	if d < time.Second {
		return time.Second
	}
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

func (p *Pool) startIdleRecycler() {
	if p.cfg.IdleRecycleAfter == 0 {
		return
	}
	if !p.cfg.WorkerMemorySupported || p.cfg.ReadWorkerMemory == nil {
		if p.cfg.Logger != nil {
			p.cfg.Logger.Warn("pool: idle-memory recycling unavailable on this platform")
		}
		return
	}
	p.idleSweepOnce.Do(func() {
		p.idleSweepWG.Add(1)
		go p.idleRecycleLoop()
	})
}
```

Add `idleSweepOnce sync.Once`, `idleSweepWG sync.WaitGroup`, and test-only `idleSweepTicks <-chan time.Time` to `Pool`. The loop uses an injected channel when present; otherwise it owns a ticker at `idleSweepCadence`. It selects only between ticks and `p.closing`, defers `idleSweepWG.Done`, and calls `sweepIdleWorkers` on each tick.

Call `startIdleRecycler()` once at the successful end of `Warmup`. In `Close`, after `close(p.closing)` and before `closeAll`, call:

```go
p.idleSweepWG.Wait()
```

- [ ] **Step 5: Implement free-slot scanning and strict eligibility**

```go
func (p *Pool) sweepIdleWorkers() {
	free := len(p.slots)
	for i := 0; i < free; i++ {
		select {
		case slot := <-p.slots:
			p.inspectIdleWorker(slot)
		default:
			return
		}
	}
}
```

`inspectIdleWorker` must snapshot eligibility under `p.mu`, then call `Client.Pid` and `ReadWorkerMemory` after unlocking. If unused, not idle, dead, respawning, closed, invalid PID, unreadable, or `rssBytes <= threshold`, call `releaseOrRecycle(slot)` so a previously deferred max-turn trigger still gets a chance. For an idle-memory candidate, build `recycleTrigger{reason: recycleReasonIdleMemory}` and call the generalized admission helper.

Compute idle with the injected clock and clamp negative durations to zero:

```go
idle := p.cfg.Now().Sub(lastRelease)
if idle < 0 {
	idle = 0
}
```

- [ ] **Step 6: Generalize recycle admission without changing capacity semantics**

Keep `releaseOrRecycle(slot)` as the request/self-heal entry point. Have it build a max-turn trigger when applicable, then call:

```go
func (p *Pool) returnOrRecycle(slot *Slot, requested *recycleTrigger)
```

Move the current closed check, below-threshold requeue, `recyclesInFlight` deferral, `recycleWG.Add`, launch hook, and goroutine launch into `returnOrRecycle`. Under its lock, max-turn eligibility takes precedence over an idle trigger so a worker already over its original budget remains classified `max_turns`.

Change:

```go
func (p *Pool) recycleSlot(slot *Slot, trigger recycleTrigger)
```

At the start of `recycleSlot`, read `slot.Client.Pid()` while the goroutine exclusively owns the slot when `trigger.pid` is zero. Emit one INFO event named `pool: slot recycling` with the exact bounded keys `label`, `trigger`, `pid`, `turns`, `user_requests`, `idle_for`, `rss_bytes`, `idle_threshold`, and `memory_threshold_bytes`; use zero values for the non-applicable max-turn RSS/idle fields. Do not include session IDs, user IDs, paths, or request content. After `respawnSlot` returns nil, call:

```go
if p.cfg.RecycleMetrics != nil {
	p.cfg.RecycleMetrics.RecordWorkerRecycle(string(trigger.reason), trigger.rssBytes, trigger.idle)
}
```

The existing `p.recycles` increment remains in successful `respawnSlot`. Record the synchronous warmup max-turn replacement through the same callback after it succeeds.

- [ ] **Step 7: Add deterministic test seams**

Append to `export_test.go`:

```go
func (p *Pool) SweepIdleWorkersForTesting() { p.sweepIdleWorkers() }

func (p *Pool) WaitForRecyclesForTesting() { p.recycleWG.Wait() }

func (p *Pool) SetIdleSweepTicksForTesting(ticks <-chan time.Time) {
	p.idleSweepTicks = ticks
}

func IdleSweepCadenceForTesting(after time.Duration) time.Duration {
	return idleSweepCadence(after)
}
```

- [ ] **Step 8: Add the complete threshold and safety matrix**

Add tests with these byte-exact names:

```go
func TestPool_IdleMemoryRecycle_UnusedWorkerNotSampled(t *testing.T)
func TestPool_IdleMemoryRecycle_CatalogProbeNotUserUse(t *testing.T)
func TestPool_IdleMemoryRecycle_BelowIdleNotSampled(t *testing.T)
func TestPool_IdleMemoryRecycle_ExactlyThresholdDoesNotRecycle(t *testing.T)
func TestPool_IdleMemoryRecycle_OneByteOverRecycles(t *testing.T)
func TestPool_IdleMemoryRecycle_UnavailableSampleRequeues(t *testing.T)
func TestPool_IdleMemoryRecycle_InvalidPIDNotSampled(t *testing.T)
func TestPool_IdleMemoryRecycle_BusyWorkerNotClaimed(t *testing.T)
func TestPool_IdleMemoryRecycle_DeferredBehindTurnRecycle(t *testing.T)
func TestPool_IdleMemoryRecycle_LaterRequestResetsEligibility(t *testing.T)
func TestPool_IdleMemoryRecycle_UnsupportedDoesNotStartLoop(t *testing.T)
func TestPool_IdleMemoryRecycle_CloseJoinsLoop(t *testing.T)
func TestPool_IdleMemoryRecycle_CloseDuringSweepPreservesOwnership(t *testing.T)
func TestPool_IdleMemoryRecycle_ConcurrentAcquireReleaseSweep(t *testing.T)
func TestPool_RecycleMetrics_RecordedOnceAfterSuccess(t *testing.T)
func TestPool_RecycleMetrics_NotRecordedAfterFailure(t *testing.T)
func TestIdleSweepCadence_Clamps(t *testing.T)
```

Use injected time, buffered manual tick channels, reader call counters, a fake `RecycleMetricsRecorder`, factory gates, and `WaitForRecyclesForTesting`; do not use `time.Sleep` to prove production cadence. The unsupported test must capture logs and require exactly one unavailable-platform warning even when multiple manual ticks are offered.

- [ ] **Step 9: Preserve existing turn-recycle regression coverage**

Update existing `worker_recycle_test.go` assertions for the reason-aware `recycleSlot` signature and ensure all pre-existing max-turn, failure, concurrent-recycle, and shutdown tests remain green.

Run:

```bash
gofumpt -w internal/pool
go test -race ./internal/pool -run 'IdleMemory|WorkerRecycle|RecycleSerial|Shutdown' -count=1
```

Expected: PASS with no race or goleak report.

- [ ] **Step 10: Commit the pool lifecycle**

```bash
git add internal/pool
git commit -m "feat(pool): recycle idle high-memory workers"
```

---

### Task 4: Prometheus recycle and user-behavior metrics

**Files:**

- Modify: `internal/metrics/metrics.go:80-115,120-185,272-365`
- Modify: `internal/metrics/collector.go`
- Modify: `internal/metrics/worker_collector.go`
- Modify: `internal/metrics/worker_collector_test.go`
- Modify: `internal/metrics/metrics_test.go`

**Interfaces:**

- Consumes: `RecycleMetricsRecorder.RecordWorkerRecycle` and activity fields projected by `pool.WorkerProcs`.
- Produces: the five metric families approved in the spec and a `metrics.WorkerProc` shape consumable by main wiring.

- [ ] **Step 1: Write failing recycle-event metric tests**

```go
func TestRecordWorkerRecycle_ReasonAndIdleHistograms(t *testing.T) {
	m := metrics.New(
		metrics.BuildInfo{GatewayID: "gw-test"},
		func() metrics.PoolStats { return metrics.PoolStats{} },
		func() metrics.SessionStats { return metrics.SessionStats{} },
		nil,
	)
	m.RecordWorkerRecycle("max_turns", 0, 0)
	m.RecordWorkerRecycle("idle_memory", 800<<20, 16*time.Minute)
	m.RecordWorkerRecycle("unbounded-input", 1, time.Second)
	body := scrape(t, m)

	for _, want := range []string{
		`gw_pool_slot_recycles_by_reason_total{gateway_id="gw-test",reason="max_turns"} 1`,
		`gw_pool_slot_recycles_by_reason_total{gateway_id="gw-test",reason="idle_memory"} 1`,
		`gw_pool_idle_memory_recycle_trigger_rss_bytes_count{gateway_id="gw-test"} 1`,
		`gw_pool_idle_memory_recycle_trigger_idle_seconds_count{gateway_id="gw-test"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, `reason="unbounded-input"`) {
		t.Fatalf("unbounded reason escaped validation\n%s", body)
	}
}
```

- [ ] **Step 2: Run the recycle metric test and confirm RED**

```bash
go test ./internal/metrics -run RecordWorkerRecycle -count=1
```

Expected: compile failure because `RecordWorkerRecycle` does not exist.

- [ ] **Step 3: Register bounded counter and histograms**

Add fields to `Metrics` and construct them with these definitions:

```go
workerRecyclesByReason: prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "gw_pool_slot_recycles_by_reason_total",
	Help: "Successful scheduled worker recycles by bounded trigger reason.",
}, []string{"reason"}),
idleRecycleRSS: prometheus.NewHistogram(prometheus.HistogramOpts{
	Name: "gw_pool_idle_memory_recycle_trigger_rss_bytes",
	Help: "Direct worker working set observed at successful idle-memory recycle.",
	Buckets: []float64{256 << 20, 384 << 20, 512 << 20, 768 << 20, 1024 << 20, 1536 << 20, 2048 << 20, 4096 << 20, 8192 << 20},
}),
idleRecycleIdle: prometheus.NewHistogram(prometheus.HistogramOpts{
	Name: "gw_pool_idle_memory_recycle_trigger_idle_seconds",
	Help: "Completed-request idle duration observed at successful idle-memory recycle.",
	Buckets: []float64{60, 300, 900, 1800, 3600, 14400, 43200, 86400},
}),
```

Register all three through `reggw`. Implement:

```go
func (m *Metrics) RecordWorkerRecycle(reason string, rssBytes uint64, idle time.Duration) {
	if reason != "max_turns" && reason != "idle_memory" {
		return
	}
	m.workerRecyclesByReason.WithLabelValues(reason).Inc()
	if reason == "idle_memory" {
		m.idleRecycleRSS.Observe(float64(rssBytes))
		m.idleRecycleIdle.Observe(idle.Seconds())
	}
}
```

Keep `gw_pool_slot_recycles_total`'s name, type, and label set unchanged. Update only its help text in `collector.go` from max-turn-specific wording to `Total successful scheduled worker recycles since start.` because `Pool.Recycles()` now aggregates both bounded reasons. Existing event-counter tests must continue to find the same sample line.

- [ ] **Step 4: Write failing per-worker activity collector tests**

Extend `WorkerProc`:

```go
type WorkerProc struct {
	Slot                   string
	Pid                    int
	UserRequestsSinceSpawn uint64
	IdleSeconds            float64
}
```

Before implementing collection, add expectations:

```go
procs := func() []WorkerProc {
	return []WorkerProc{{
		Slot: "slot-0", Pid: 111,
		UserRequestsSinceSpawn: 1,
		IdleSeconds: 901,
	}}
}
```

Expected scrape lines:

```text
gw_worker_user_requests_since_spawn{slot="slot-0"} 1
gw_worker_idle_seconds{slot="slot-0"} 901
```

Update the unreadable-process test: CPU/RSS remain absent when `procstat.Sample.OK=false`, but user-request and idle gauges remain present because they do not depend on OS sampling.

- [ ] **Step 5: Implement the worker gauges**

Add descriptors:

```go
userRequests *prometheus.Desc
idleSeconds  *prometheus.Desc
```

Construct, describe, and collect them for every projected live worker before calling the process reader:

```go
ch <- prometheus.MustNewConstMetric(c.userRequests, prometheus.GaugeValue, float64(w.UserRequestsSinceSpawn), w.Slot)
ch <- prometheus.MustNewConstMetric(c.idleSeconds, prometheus.GaugeValue, w.IdleSeconds, w.Slot)
```

- [ ] **Step 6: Run metrics tests and commit**

```bash
gofumpt -w internal/metrics
go test ./internal/metrics -run 'RecordWorkerRecycle|WorkerCollector' -count=1
git add internal/metrics
git commit -m "feat(metrics): expose idle-memory recycle behavior"
```

Expected: PASS; no arbitrary reason label appears.

---

### Task 5: Main wiring and additive admin snapshot contract

**Files:**

- Modify: `cmd/otto-gateway/main.go:622-735,1110-1150,1447-1465`
- Modify: `cmd/otto-gateway/main_test.go`
- Modify: `internal/admin/admin.go:107-210`
- Modify: `internal/admin/snapshot.go:90-140,200-245`
- Modify: `internal/admin/snapshot_test.go`
- Modify: `internal/admin/snapshot_proc_test.go`

**Interfaces:**

- Consumes: config/procstat from Task 1, pool fields from Tasks 2-3, and metrics implementation from Task 4.
- Produces: complete production pool wiring and additive `/admin/api/snapshot` policy/activity fields.

- [ ] **Step 1: Write failing admin snapshot contract tests**

Extend a `stubPool` slot in `snapshot_test.go`:

```go
released := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
deps := Deps{
	KiroWorkerIdleRecycleAfter:     15 * time.Minute,
	KiroWorkerIdleRecycleMemoryMB:  500,
	KiroWorkerIdleRecycleSupported: true,
	PoolDetail: &stubPool{slots: []SnapshotSlot{{
		Label: "slot-0", Alive: true,
		UserRequestsSinceSpawn: 1,
		LastUserReleaseAt: &released,
	}}},
}
```

Assert decoded JSON fields:

```go
if snap.Pool.IdleRecycleMS != 900000 || snap.Pool.IdleRecycleMemoryBytes != 500<<20 || !snap.Pool.IdleRecycleSupported {
	t.Fatalf("idle policy snapshot = %+v", snap.Pool)
}
if got := snap.Pool.Slots[0]; got.UserRequestsSinceSpawn != 1 || got.LastUserReleaseAt == nil || !got.LastUserReleaseAt.Equal(released) {
	t.Fatalf("slot activity snapshot = %+v", got)
}
```

- [ ] **Step 2: Run admin tests and confirm RED**

```bash
go test ./internal/admin -run 'Snapshot.*Idle|Snapshot_ProcMerge' -count=1
```

Expected: compile failure for missing `Deps`, `SnapshotPool`, and `SnapshotSlot` fields.

- [ ] **Step 3: Add admin dependency and JSON fields**

Add to `admin.Deps`:

```go
KiroWorkerIdleRecycleAfter     time.Duration
KiroWorkerIdleRecycleMemoryMB  int
KiroWorkerIdleRecycleSupported bool
```

Add to `SnapshotPool`:

```go
IdleRecycleMS          int64  `json:"idle_recycle_ms"`
IdleRecycleMemoryBytes uint64 `json:"idle_recycle_memory_bytes"`
IdleRecycleSupported   bool   `json:"idle_recycle_supported"`
```

Add the locked activity fields to `SnapshotSlot`. Populate policy values before the nil-safe pool-detail block:

```go
snap.Pool.IdleRecycleMS = h.deps.KiroWorkerIdleRecycleAfter.Milliseconds()
snap.Pool.IdleRecycleMemoryBytes = uint64(h.deps.KiroWorkerIdleRecycleMemoryMB) << 20
snap.Pool.IdleRecycleSupported = h.deps.KiroWorkerIdleRecycleSupported
```

- [ ] **Step 4: Write failing command-adapter and wiring tests**

Extract the row copy into this cmd-owned pure adapter so it can be tested without starting Kiro:

```go
func snapshotSlotFromPool(r pool.AgentSlot) admin.SnapshotSlot
```

Add `TestSnapshotSlotFromPool_CopiesIdleRecycleActivity` with a non-zero user count and release timestamp and compare the complete result with `cmp.Diff`. Have `adminPoolDetailAdapter.Detail` call this helper for every row.

Extract the pool-policy assignment into this cmd-owned helper:

```go
func applyIdleMemoryRecyclePoolConfig(dst *pool.Config, cfg config.Config, recorder pool.RecycleMetricsRecorder)
```

Add `TestApplyIdleMemoryRecyclePoolConfig` using a pointer fake recorder. Assert the duration, MiB-to-bytes conversion, compiled-platform support, non-nil memory reader, and exact recorder identity on the resulting `pool.Config`. Call the memory reader with the current process PID and compare its result to `procstat.Read(os.Getpid())`, which is a supported sample on Windows/Linux and an unavailable sample on macOS.

Add `TestApp_IdleMemoryRecycleAdminPolicyFromConfig` in degraded `KiroCmd=""` mode. Construct `newApp` with distinctive idle and memory values, request `/admin/api/snapshot`, and assert the decoded pool policy contains those values and `procstat.Supported()`. This behavior-level integration test proves the command layer forwards all three admin dependencies without inspecting source text.

- [ ] **Step 5: Wire process sampling, recycle metrics, and activity projections**

Add a local adapter near other cmd-layer projections:

```go
func readWorkerMemory(pid int) (uint64, bool) {
	s := procstat.Read(pid)
	return s.RSSBytes, s.OK
}
```

Create the ordinary `pool.Config` literal, call `applyIdleMemoryRecyclePoolConfig` on it, and then pass it to `pool.New`. The helper populates:

```go
IdleRecycleAfter:       cfg.KiroWorkerIdleRecycleAfter,
IdleRecycleMemoryBytes: uint64(cfg.KiroWorkerIdleRecycleMemoryMB) << 20,
WorkerMemorySupported:  procstat.Supported(),
ReadWorkerMemory:       readWorkerMemory,
RecycleMetrics:         gwMetrics,
```

Extend the `metrics.WorkerProc` adapter:

```go
metrics.WorkerProc{
	Slot: w.Label, Pid: w.Pid,
	UserRequestsSinceSpawn: w.UserRequestsSinceSpawn,
	IdleSeconds: w.IdleSeconds,
}
```

Copy activity in `adminPoolDetailAdapter.Detail`, and populate all three `admin.Deps` policy fields when constructing the admin handler.

- [ ] **Step 6: Run focused integration tests and commit**

```bash
gofumpt -w cmd/otto-gateway internal/admin
go test ./cmd/otto-gateway ./internal/admin -run 'IdleRecycle|PoolDetailAdapter|Snapshot_ProcMerge' -count=1
go build ./cmd/otto-gateway
git add cmd/otto-gateway internal/admin
git commit -m "feat(admin): wire idle-memory recycle status"
```

Expected: tests and build PASS.

---

### Task 6: Admin cards, wrapper defaults, support bundles, and operator docs

**Files:**

- Modify: `internal/admin/admin.go:810-845`
- Modify: `internal/admin/handlers_test.go:265-300`
- Modify: `internal/admin/static/js/admin.js:160-285,405-470,640-665`
- Modify: `internal/admin/static/css/admin.css`
- Modify: `internal/admin/admin_js_test.js`
- Modify: `scripts/.env.example:40-50`
- Modify: `scripts/gw:1880-1892,2120-2135`
- Modify: `scripts/gw.ps1:1705-1715,2120-2132`
- Modify: `tests/scripts/test-support-bundle.sh`
- Modify: `tests/scripts/test-support-bundle.ps1`
- Modify: `docs/operating.md:400-430`

**Interfaces:**

- Consumes: additive admin snapshot fields from Task 5.
- Produces: transparent slot-card policy rendering, shipped laptop defaults, diagnostics/support capture, and operator documentation.

- [ ] **Step 1: Write failing JavaScript rendering tests**

Extend the fake DOM `Element` only with methods actually used by slot rendering:

```js
append(...children) { this.children.push(...children); }
replaceChildren(...children) { this.children = children; }
```

Add a slot grid selector to the harness and a snapshot fixture:

```js
pool: {
  size: 1,
  max_turns: 20,
  idle_recycle_ms: 900000,
  idle_recycle_memory_bytes: 500 * 1024 * 1024,
  idle_recycle_supported: true,
  slots: [{
    label: 'slot-0', alive: true, busy: false, stat_ok: true,
    rss_bytes: 800 * 1024 * 1024, turns: 2,
    user_requests_since_spawn: 1,
    last_user_release_at: '2026-08-04T11:44:00Z',
  }],
}
```

With `generated_at='2026-08-04T12:00:00Z'`, assert the rendered card text contains `USER REQS`, `1`, `IDLE`, `16m / 15m`, and both `800 MiB` and `500 MiB`. Add fixtures for unused (`—`), busy (`active`), unsupported (`n/a`), and post-replacement reset (`USER REQS=0`, `IDLE=—`, new PID) states.

- [ ] **Step 2: Run Node tests and confirm RED**

```bash
node --test internal/admin/admin_js_test.js
```

Expected: FAIL because the new activity and threshold text is absent.

- [ ] **Step 3: Implement slot-card activity and threshold rendering**

Add:

```js
function formatIdleCell(slot, pool, generatedAt) {
  if (!slot.user_requests_since_spawn) return '—';
  if (slot.busy) return 'active';
  var released = Date.parse(slot.last_user_release_at || '');
  var generated = Date.parse(generatedAt || '');
  if (!Number.isFinite(released) || !Number.isFinite(generated)) return '—';
  var idleMs = Math.max(0, generated - released);
  var current = formatUptime(idleMs);
  if (pool.idle_recycle_ms > 0) return current + ' / ' + formatUptime(pool.idle_recycle_ms);
  return current;
}
```

Change `buildSlotPerf`, `slotCardChildren`, `buildSlotCard`, `updateSlotCard`, and `renderSlots` to receive the full pool policy and `generated_at`. Add a third perf row for `USER REQS` and `IDLE`. When supported and enabled, render memory as `current / threshold`; preserve `n/a` when `stat_ok` is false. Adjust CSS only as needed to keep three two-cell rows compact and responsive.

- [ ] **Step 4: Add admin Docs environment rows and tests**

Add two rows beside `KIRO_WORKER_MAX_TURNS`:

```go
{
	Name: "KIRO_WORKER_IDLE_RECYCLE_MS", Default: "0 (disabled; laptop template 900000)",
	Description: "Completed-user-request idle time before a free high-memory worker is eligible for eager replacement.",
	CurrentValue: strconv.FormatInt(h.deps.KiroWorkerIdleRecycleAfter.Milliseconds(), 10),
},
{
	Name: "KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", Default: "500",
	Description: "Direct Kiro working-set threshold in MiB; recycling requires a strictly greater sample.",
	CurrentValue: strconv.Itoa(h.deps.KiroWorkerIdleRecycleMemoryMB),
},
```

Extend `TestAdmin_DocsEnvTable_WorkerRecycle` to require both names and distinctive live values.

- [ ] **Step 5: Ship laptop defaults and diagnostic key coverage**

Add to `scripts/.env.example` directly after `KIRO_WORKER_MAX_TURNS=20`:

```text
# Recycle a user-used worker after 15 idle minutes only when its direct working
# set exceeds 500 MiB. Shared hosts can override either value in overrides.env.
KIRO_WORKER_IDLE_RECYCLE_MS=900000
KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB=500
```

Add both keys to the POSIX and PowerShell environment-display and support-bundle key lists. Set both values in support-bundle fixtures and assert they appear unredacted in captured environment output because they are non-secret configuration.

- [ ] **Step 6: Document the operator contract**

Add both variables to `docs/operating.md`, including binary/wrapper defaults, conjunctive semantics, strict memory comparison, Windows/Linux support, macOS no-op warning, direct-process limitation, eager replacement, and one-recycle serialization.

- [ ] **Step 7: Run UI, admin, and wrapper tests**

```bash
node --test internal/admin/admin_js_test.js
go test ./internal/admin -run 'DocsEnvTable_WorkerRecycle|Snapshot' -count=1
bash -n scripts/gw
shellcheck scripts/gw tests/scripts/test-support-bundle.sh
bash tests/scripts/test-support-bundle.sh
if command -v pwsh >/dev/null 2>&1; then pwsh -NoProfile -File tests/scripts/test-support-bundle.ps1; else echo 'pwsh unavailable: PowerShell live test deferred'; fi
```

Expected: all available gates PASS; the only permitted skip is missing `pwsh` on the current host.

- [ ] **Step 8: Commit UI, distribution, and docs**

```bash
git add internal/admin scripts tests/scripts docs/operating.md
git commit -m "feat(ui): surface idle-memory worker recycling"
```

---

### Task 7: Generated Grafana Idle Memory Recycling row

**Files:**

- Modify: `scripts/gen_grafana_dashboard.py`
- Modify: `scripts/test_gen_grafana_dashboard.py`
- Modify: `docs/grafana/otto-gateway-dashboard.json`
- Modify: `docs/grafana/README.md`

**Interfaces:**

- Consumes: five metric families from Task 4 and existing `gw_llm_requests_total`.
- Produces: a generated dashboard row with bounded, gateway-filtered queries and synchronized generator tests/JSON.

- [ ] **Step 1: Write failing generator contract tests**

Insert `Idle Memory Recycling` between capacity and Kiro rows in `ROW_ORDER`. Add all five new metric families to `CUSTOM_METRICS`. Extend required panel titles with:

```python
"Worker Recycles by Reason",
"Idle-memory Recycles per 100 LLM Requests",
"Worker Idle and Use Since Spawn",
"Idle-memory Trigger RSS",
"Idle-memory Trigger Idle Duration",
```

Add a query-specific test:

```python
def test_idle_memory_panels_use_bounded_reason_and_honest_quantiles(self):
    panels = {panel["title"]: panel for panel in all_panels(self.dashboard)}
    reason_expr = panels["Worker Recycles by Reason"]["targets"][0]["expr"]
    self.assertIn("sum by(reason)", reason_expr)
    self.assertIn("gw_pool_slot_recycles_by_reason_total", reason_expr)
    ratio_expr = panels["Idle-memory Recycles per 100 LLM Requests"]["targets"][0]["expr"]
    self.assertIn('reason="idle_memory"', ratio_expr)
    self.assertIn("clamp_min", ratio_expr)
    for title in ("Idle-memory Trigger RSS", "Idle-memory Trigger Idle Duration"):
        for target in panels[title]["targets"]:
            if "histogram_quantile" in target["expr"]:
                self.assertIn("_count", target["expr"])
                self.assertIn(" and ", target["expr"])
```

- [ ] **Step 2: Run generator tests and confirm RED**

```bash
python3 -m unittest scripts.test_gen_grafana_dashboard
```

Expected: FAIL for missing row, panels, and metric inventory.

- [ ] **Step 3: Add the generated dashboard row**

Create `add_idle_memory_recycling(builder)` and call it after `add_capacity(builder)`. Build these panels:

1. `Worker Recycles by Reason`: `sum by(reason)(rate(gw_pool_slot_recycles_by_reason_total{...}[$__rate_interval]))`.
2. `Idle-memory Recycles per 100 LLM Requests`: `100 * sum(increase(...reason="idle_memory"...[$__range])) / clamp_min(sum(increase(gw_llm_requests_total{...}[$__range])), 1)`.
3. `Worker Idle and Use Since Spawn`: two targets using `gw_worker_idle_seconds` and `gw_worker_user_requests_since_spawn`, with field overrides so idle uses seconds and requests use `short`.
4. `Idle-memory Trigger RSS`: p50/p95 from RSS buckets, each gated with `and on() sum(rate(..._count...)) > 0` so an empty range has no fabricated quantile.
5. `Idle-memory Trigger Idle Duration`: the same p50/p95/count gating for idle seconds.

Every query must include `instance=~"$gateway_id"`; reason and slot are bounded labels.

- [ ] **Step 4: Regenerate JSON and document the row**

```bash
python3 scripts/gen_grafana_dashboard.py
```

Update `docs/grafana/README.md` row inventory and signal interpretation: user requests since spawn reset on worker replacement, idle is zero while busy/before use, and trigger histograms describe successful events rather than reclaimed bytes.

- [ ] **Step 5: Run generator tests and confirm GREEN**

```bash
python3 -m unittest scripts.test_gen_grafana_dashboard
```

Expected: PASS, including byte equality between generated and committed JSON.

- [ ] **Step 6: Commit generator, tests, JSON, and docs**

```bash
git add scripts/gen_grafana_dashboard.py scripts/test_gen_grafana_dashboard.py docs/grafana
git commit -m "feat(grafana): visualize idle-memory recycling"
```

---

### Task 8: Cross-platform and full repository verification

**Files:**

- Verify all files changed in Tasks 1-7.
- Modify only files directly implicated by a failing gate; preserve task commit boundaries with a focused follow-up commit.

**Interfaces:**

- Consumes: the complete feature.
- Produces: verification evidence that the approved acceptance criteria and repository trust gates hold.

- [ ] **Step 1: Run the focused behavioral suite**

```bash
go test -race ./internal/pool -run 'UserActivity|IdleMemory|WorkerRecycle' -count=1
go test ./internal/config ./internal/procstat ./internal/metrics ./internal/admin ./cmd/otto-gateway -count=1
node --test internal/admin/admin_js_test.js
python3 -m unittest scripts.test_gen_grafana_dashboard
```

Expected: PASS with no race, goleak, generated-JSON, or label-cardinality failure.

- [ ] **Step 2: Verify cgo-free cross-platform builds**

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
```

Expected: all three builds PASS. macOS compiles the unsupported capability path without adding cgo.

- [ ] **Step 3: Run distribution and documentation checks**

```bash
bash -n scripts/gw
shellcheck scripts/gw tests/scripts/test-support-bundle.sh
bash tests/scripts/test-support-bundle.sh
python3 scripts/test_privacy_docs.py
python3 -m unittest scripts.test_gen_grafana_dashboard
```

Expected: PASS.

- [ ] **Step 4: Run the canonical repository gate**

```bash
make ci
```

Expected: formatting, vet, build, lint, full race suite, admin JavaScript, metrics defaults, architecture lint, and examples all PASS.

- [ ] **Step 5: Inspect acceptance evidence and working tree**

```bash
git status --short
git log --oneline --decorate -8
git diff --check d2c5863..HEAD
```

Expected: clean worktree; seven focused implementation commits (plus only narrowly scoped gate-fix commits if required); no whitespace errors.

---

## Acceptance Walkthrough

After automated gates, verify on a Windows laptop or Windows VM with real `kiro-cli`:

1. Install/upgrade so the wrapper template contains 15 minutes and 500 MiB.
2. Start with `POOL_SIZE=2`, `KIRO_WORKER_MAX_TURNS=20`, and the idle-memory defaults.
3. Send one stateless request and identify its slot/PID in the admin dashboard.
4. Confirm `USER REQS=1`, idle begins only after completion, and current working set is shown against 500 MiB.
5. If the real process is below 500 MiB, temporarily lower only the memory threshold in `overrides.env` to a value below the observed RSS, restart, and repeat the single request.
6. After the configured idle duration plus at most 30 seconds, confirm the PID and uptime reset while the slot label remains stable.
7. Confirm `gw_pool_slot_recycles_total` increments and `gw_pool_slot_recycles_by_reason_total{reason="idle_memory"}` increments once.
8. Confirm the trigger histograms each gain one observation and the Grafana row renders the event.
9. Send a request before the idle threshold and confirm no recycle occurs.
10. Restore the production thresholds after the shortened verification run.

This walkthrough is observational and does not replace automated concurrency and shutdown tests.
