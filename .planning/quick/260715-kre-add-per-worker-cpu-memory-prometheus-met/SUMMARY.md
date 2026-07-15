---
quick_id: 260715-kre
slug: add-per-worker-cpu-memory-prometheus-met
date: 2026-07-15
status: complete
---

# Summary: Per-worker CPU/memory metrics + admin UI perf tiles

## What shipped

Two consumers over one cgo-free reader, exposing CPU + memory for the gateway
process and each kiro-cli worker subprocess.

**New package `internal/procstat`** — reads one PID's CPU (cumulative seconds) +
RSS (bytes), cgo-free, per-OS:
- `procstat_linux.go` — vendored `prometheus/procfs` (`CPUTime` + `ResidentMemory`).
- `procstat_windows.go` — `OpenProcess` + `GetProcessTimes` (kernel+user) + psapi
  `GetProcessMemoryInfo` (working set), mirroring the stock collector's Win32 path.
- `procstat_other.go` — darwin/dev no-op (`OK=false`); cgo-free RSS there needs
  mach task_info, explicitly out of scope.

**Prometheus** — `internal/metrics/worker_collector.go`: pull-collector emitting
`gw_worker_cpu_seconds_total{slot}` (counter) + `gw_worker_resident_memory_bytes{slot}`
(gauge), registered through `reggw` so the `gateway_id` constant label rides along.
Labelled by slot (bounded by POOL_SIZE), never by PID. `metrics.New` gained a 4th
`workers` closure param.

**Pool** — `Pool.WorkerProcs() []WorkerProc` snapshots (label, Client) under the
lock, then reads `Pid()` after release (upholds the no-Client-calls-under-`p.mu`
invariant); skips dead/nil/pid≤0 slots.

**Admin UI** — snapshot gained `process_cpu_seconds` / `process_rss_bytes` /
`process_stat_ok` + per-slot `cpu_seconds` / `rss_bytes` / `stat_ok`, filled via a
new nil-safe `ProcSampler` on `admin.Deps.Proc`. Dashboard renders live number
tiles + hand-drawn inline-SVG sparklines (rolling ~15-min window at the 30s poll)
for the gateway and each slot; CPU% derived client-side by diffing polls; `!ok`
renders "n/a". Zero new JS deps.

**cmd wiring** — `metrics.WorkerProc` closure into `metrics.New`; `adminProcSampler`
(over `procstat`) into `admin.Deps.Proc`. Package boundaries preserved: neither
`metrics` nor `admin` imports `internal/pool`.

## Verification

- `go build ./...` + cross-compile linux/amd64, windows/amd64, darwin/arm64 with
  `CGO_ENABLED=0` — all pass (cgo-free confirmed).
- `go test ./...` green; `-race` on procstat/metrics/admin/pool green.
- New tests: worker collector (emits per-slot, skips `!OK`, nil-safe, pid never a
  label), pool `WorkerProcs` (healthy/dead-skip/zero-pid-skip), admin snapshot
  proc-merge (by-label merge, `!OK` → n/a, nil-safe), procstat platform contract.
- `go vet ./...` clean; `gofumpt` clean; `go-arch-lint` clean — also registered
  the pre-existing-unregistered `metrics` component + new `procstat` component.
- Live run (macOS, degraded): `/metrics` 200 with stock `process_*` (gateway_id
  present) and `gw_worker_*` correctly absent-not-erroring (darwin no-op);
  `/admin` 200 with perf tiles; snapshot JSON carries the new fields with
  `stat_ok=false` (graceful "n/a" path). Linux/Windows `OK=true` populate path
  covered by unit tests + CI.

## Notes / follow-ups

- `golangci-lint` not installed locally → not run here; CI covers it.
- Local UI is a live rolling window (no history — that's Prometheus/Grafana). On
  Windows/Linux the tiles + sparklines populate with real numbers; on the macOS
  dev box they show "n/a" by design.
- Not pushed/merged — committed on `main` per the quick-task flow.
