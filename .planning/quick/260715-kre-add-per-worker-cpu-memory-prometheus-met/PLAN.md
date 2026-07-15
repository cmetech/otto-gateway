---
quick_id: 260715-kre
slug: add-per-worker-cpu-memory-prometheus-met
date: 2026-07-15
status: complete
---

# Quick Task: Per-worker CPU/memory metrics + admin UI perf tiles

## Goal

Expose CPU and memory usage of (a) the gateway process and (b) each kiro-cli
worker subprocess, in two consumers that share one cgo-free reader:

1. **Prometheus** — new pull-collector emitting `gw_worker_cpu_seconds_total{slot}`
   (counter) and `gw_worker_resident_memory_bytes{slot}` (gauge), inheriting the
   `gateway_id` constant label by registering through `reggw`.
2. **Admin dashboard UI** (`/admin`) — live number tiles + hand-drawn inline-SVG
   sparklines (rolling window) for the gateway process and each pool slot's
   worker, fed by the existing `/admin/api/snapshot` poll.

## Constraints (from CLAUDE.md)

- **No cgo** in the main binary. Cross-compile from macOS with vanilla `go build`
  + GOOS/GOARCH must keep working.
- Linux + Windows are real deploy targets → full support. macOS is dev/build-only
  → graceful no-op (`OK=false`), UI shows "n/a".
- Label workers by **slot label** (bounded by POOL_SIZE), never by PID (churns on
  respawn → unbounded TSDB cardinality).
- Package-boundary discipline: `internal/metrics` and `internal/admin` must not
  import `internal/pool`; bridge via closures/adapters in `cmd/otto-gateway`
  (mirrors existing PoolStats / PoolDetail wiring).
- Pool: no `slot.Client` method calls under `p.mu` (mirror `detail.go` / `stats.go`).

## Design

### New package: `internal/procstat` (the shared cgo-free reader)
- `sample.go` — `type Sample struct { CPUSeconds float64; RSSBytes uint64; OK bool }`;
  `Self() Sample { return Read(os.Getpid()) }`.
- `procstat_linux.go` (`//go:build linux`) — `procfs.NewProc(pid).Stat()` →
  `CPUTime()` (secs) + `ResidentMemory()` (bytes). procfs already vendored.
- `procstat_windows.go` (`//go:build windows`) — `windows.OpenProcess(
  PROCESS_QUERY_LIMITED_INFORMATION)` → `GetProcessTimes` (kernel+user) +
  psapi `GetProcessMemoryInfo` LazyDLL → `WorkingSetSize`. Mirrors the stock
  prometheus process collector's Windows path but for an arbitrary pid.
- `procstat_other.go` (`//go:build !linux && !windows`) — `Read` returns `Sample{}`
  (OK=false). Covers darwin/dev.
- CPU is cumulative seconds (a counter). RSS is instantaneous bytes.

### Pool accessor
- `internal/pool/detail.go` (or new `procs.go`): `type WorkerProc struct { Label
  string; Pid int }`; `func (p *Pool) WorkerProcs() []WorkerProc` — snapshot
  (label, Client) under `p.mu`, release, then call `Client.Pid()`; skip
  dead/nil/pid<=0 slots.

### Prometheus collector
- `internal/metrics/worker_collector.go`: `type WorkerProc struct { Slot string;
  Pid int }`; `workerCollector` reads `procs func() []WorkerProc` + `read
  func(int) procstat.Sample` at scrape time. Descs:
  - `gw_worker_cpu_seconds_total` CounterValue, label `slot`
  - `gw_worker_resident_memory_bytes` GaugeValue, label `slot`
  Skip pids where `!OK`. Register via `reggw.MustRegister(...)` so `gateway_id`
  is inherited.
- `metrics.New` gains a 4th param `workers func() []WorkerProc`.

### Admin snapshot + UI
- `snapshot.go`: `Snapshot` gains `ProcessCPUSeconds float64`, `ProcessRSSBytes
  uint64`, `ProcessStatOK bool`. `SnapshotSlot` gains `CPUSeconds float64`,
  `RSSBytes uint64`, `StatOK bool`. New nil-safe `ProcSampler` interface
  (`Self() ProcSample`, `Workers() map[string]ProcSample`) on `admin.Deps.Proc`.
  Handler fills top-level process fields + per-slot fields by label lookup.
- `dashboard.html.tmpl`: two summary-strip items (Gateway CPU / Mem) each with an
  SVG sparkline slot; slot-card template gains CPU%/RSS + sparkline.
- `admin.js`: per-key ring buffers (gateway + each slot label); compute live CPU%
  as `Δcpu_seconds / Δwall × 100` between polls; render inline-SVG polyline
  sparklines; `StatOK=false` → "n/a".
- `admin.css`: sparkline + perf-tile styling (theme-aware, existing var palette).

### cmd/otto-gateway wiring
- Closure adapting `pool.WorkerProc` → `metrics.WorkerProc`, passed to `metrics.New`.
- `adminProcSampler{pool}` implementing `admin.ProcSampler` via `procstat`, set on
  `admin.Deps.Proc` (Self works even when pool is nil).

## Tasks
1. `internal/procstat` package (+ per-OS files) with an injectable-for-tests seam.
2. `pool.WorkerProcs()` + unit test (fake client pids, dead-slot skip).
3. `metrics` worker collector + `New` signature + tests (fake reader, gateway_id
   label present, slot label, !OK skip). Fix existing `New` call sites.
4. `admin` snapshot fields + `ProcSampler` + handler merge + tests (nil-safe,
   per-slot merge by label, process fields).
5. Frontend: template + admin.js sparklines/tiles + css.
6. `cmd/otto-gateway` wiring (both closures/adapters).
7. Trust gates: `go build` (linux+windows+darwin cross), `go vet`, `go test ./...`,
   gosec/lint. Run gateway locally, curl `/metrics`, load `/admin`.

## Verification
- Cross-compile all three OS/arch (proves cgo-free).
- Unit tests green.
- Local (macOS): `/metrics` still serves; existing `process_*` present; new
  `gw_worker_*` absent-but-not-erroring (darwin OK=false → no samples); `/admin`
  renders tiles showing "n/a" gracefully.
- Behavior contract for Linux/Windows verified by tests with injected readers.

## Non-goals
- Historical time-series in the UI (that's Grafana's job — UI is a live window).
- darwin per-process RSS (needs mach/task_info → cgo; explicitly out).
