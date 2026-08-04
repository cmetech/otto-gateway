# Idle-memory Kiro worker recycling — design

**Date:** 2026-08-04  
**Status:** Approved design; implementation not started  
**Scope:** Stateless pooled Kiro workers governed by `KIRO_WORKER_MAX_TURNS`

## 1. Problem and outcome

The gateway eagerly warms pooled `kiro-cli` workers and currently replaces a
worker only after `KIRO_WORKER_MAX_TURNS` successful `session/new` calls. The
laptop wrapper sets that limit to 20. A sparse user can issue one request, grow a
Kiro process to roughly 800 MiB, and then leave it idle indefinitely. Because the
worker never reaches 20 turns, the process retains that memory until the gateway
stops or another failure causes a respawn.

Add a second, conjunctive recycle policy:

```text
served at least one user request
AND idle since the completed request for at least the configured duration
AND direct Kiro working set is greater than the configured memory threshold
```

When all three conditions hold, the gateway eagerly replaces the worker using
the same scheduled-respawn machinery as turn-based recycling. The replacement
remains warm, so memory growth is discarded without making the next user request
pay cold-start latency.

The feature is Windows-first. Linux uses its existing RSS support. macOS does not
enforce the memory condition until the gateway has a trustworthy cgo-free process
sampler; it reports the policy as unsupported instead of treating unavailable
memory as zero.

## 2. Existing boundaries to preserve

The design extends, rather than replaces, these existing contracts:

- `internal/pool` owns warm-slot acquisition, release, shutdown, and scheduled
  process replacement.
- `Slot.turns` counts every successful pooled `session/new`, including catalog
  probes, because that is the existing turn-recycle budget.
- `releaseOrRecycle` and `recycleSlot` keep a recycled slot out of the free queue
  while replacement is in progress.
- `recyclesInFlight` permits at most one scheduled recycle at a time, preserving
  capacity for small pools.
- `internal/procstat` reads direct-process RSS/working set on Linux and Windows;
  its macOS implementation returns an unavailable sample.
- `gw_worker_resident_memory_bytes{slot}` and the admin snapshot already expose
  the same direct-process memory definition used by this policy.
- Dedicated `X-Session-Id` workers remain owned by `internal/session`. Their idle
  lifecycle is governed by `SESSION_TTL_MS`; this feature does not change it.

## 3. Configuration and rollout

Add two environment variables:

| Variable | Binary default | Laptop wrapper | Meaning |
|---|---:|---:|---|
| `KIRO_WORKER_IDLE_RECYCLE_MS` | `0` | `900000` | Completed-user-request idle duration before a free worker is eligible. Zero disables this policy. Millisecond integers and Go duration strings are accepted. |
| `KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB` | `500` | `500` | Direct Kiro working-set threshold in MiB. Recycling requires memory to be strictly greater than this value. |

The wrapper values give a user a 15-minute follow-up window. The binary remains
backward-compatible for deployments that bypass the wrapper: idle-memory
recycling stays disabled until `KIRO_WORKER_IDLE_RECYCLE_MS` is non-zero. Shared
hosts can override both values in `overrides.env`, which `gw upgrade-env` does
not replace.

Semantics and validation:

- Idle comparison is `idle >= configured duration`.
- Memory comparison is `working set > configured MiB * 1024 * 1024`.
- `KIRO_WORKER_IDLE_RECYCLE_MS=0` is the sole disable switch.
- Negative idle durations fail startup.
- The memory value must be positive. Zero is rejected because it would silently
  turn the policy into idle-only recycling of every used worker.
- The memory parser accepts at most `1_048_576` MiB (1 TiB) before converting
  to bytes, preventing integer overflow and obvious configuration mistakes.
- The two effective values and platform support are visible in the admin
  snapshot and operator documentation.
- The environment-display and support-bundle allowlists in both POSIX and
  PowerShell wrappers include the new keys.

No additional public CLI flags or a separately configurable sweep interval are
needed. This keeps the policy surface small and consistent with
`KIRO_WORKER_MAX_TURNS`.

## 4. Slot state and user-activity definition

Each `pool.Slot` gains state guarded by the existing `Pool.mu`:

- `userRequestsSinceSpawn uint64`
- `lastUserReleaseAt time.Time`

`userRequestsSinceSpawn` increments only when the normal request path completes a
successful `Pool.NewSession`. Warmup catalog capture and lazy catalog self-heal
continue to increment the existing `turns` budget but do not increment the user
counter. This prevents an unused worker from becoming idle-memory eligible merely
because gateway initialization probed it.

`lastUserReleaseAt` is updated when the winning terminal path removes the request's
session-to-slot mapping. This is the exact-once release point shared by successful
result drainage, request-context cancellation, explicit cancellation, and prompt
failure. Updating it there provides these properties:

- Idle begins only after the worker is free, never while it is streaming.
- Duplicate terminal paths cannot advance the timestamp twice.
- A later completed request resets the idle clock.
- A request that successfully opened a Kiro session but failed during prompt is
  still real user use and starts an idle window after cleanup.

Successful replacement resets `turns`, `userRequestsSinceSpawn`,
`lastUserReleaseAt`, and `spawnedAt` in the same client-swap critical section.
The stable slot label remains unchanged.

## 5. Recycler lifecycle and cadence

After successful pool warmup, start one idle-memory sweep goroutine only when all
of the following are true:

- the idle threshold is non-zero;
- a memory reader is configured; and
- the running platform reports process-memory support.

When the policy is configured on an unsupported platform, do not start the loop.
Emit one startup warning and expose support as false in the admin snapshot. The
gateway otherwise runs normally.

The sweep cadence is derived rather than separately configured:

```text
cadence = clamp(idle threshold / 4, 1 second, 30 seconds)
```

With the 15-minute wrapper threshold, a qualifying worker is found between 15
minutes and approximately 15 minutes 30 seconds after its last release. The loop
uses the pool's existing close signal and a wait group so shutdown joins it before
slot teardown completes.

### 5.1 Claiming free slots safely

The loop must not select a candidate from `Pool.all` and close it later: a request
could acquire that slot between observation and teardown. Instead, each sweep:

1. Takes an advisory snapshot of the number of currently free slots.
2. Non-blockingly receives up to that many slots from `Pool.slots`.
3. Evaluates each dequeued slot while it has exclusive ownership.
4. Requeues an ineligible slot immediately, or transfers an eligible slot to the
   scheduled-recycle path.

A slot in `Pool.slots` cannot be serving a request. Temporarily dequeuing it makes
the memory read and recycle decision race-free with acquisition. Pool sizes are
small (two in the laptop template, four in the binary default), and Win32/Linux
process reads are cheap. Requests may interleave with the scan as non-candidates
are returned.

### 5.2 Eligibility evaluation

For each exclusively held slot:

1. Under `Pool.mu`, reject closed, dead, respawning, never-user-used, or
   not-yet-idle slots. Capture the current client, PID source, user count, and
   last release time.
2. Outside `Pool.mu`, read the PID and process memory. No client or OS call occurs
   while the pool mutex is held.
3. If the PID is invalid or the sample is unavailable, requeue the slot without
   recycling.
4. If working set is at or below the threshold, requeue it.
5. If all conditions hold, submit an `idle_memory` trigger to the centralized
   recycle-admission helper.

The policy measures the direct `kiro-cli` process, matching existing metrics. It
does not sum descendant-process memory. If a future Kiro release moves material
memory into child processes, process-tree accounting requires a separate design.

## 6. Unified recycle admission and replacement

Refactor the existing turn decision just enough to share admission. A small
internal trigger value carries:

- reason: `max_turns` or `idle_memory`;
- turn count;
- user-request count;
- trigger RSS bytes, when applicable; and
- trigger idle duration, when applicable.

The release path constructs `max_turns` triggers exactly where it does today.
The sweep constructs `idle_memory` triggers only after a successful memory read.
Both enter one helper that, under `Pool.mu`:

1. drops the slot if the pool is closed;
2. requeues it when no trigger applies;
3. requeues and defers it when another scheduled recycle is active; or
4. atomically increments `recycleWG` and `recyclesInFlight`, then launches the
   existing eager background replacement.

The single-recycle invariant applies across reasons. For example, an idle-memory
candidate encountered while a max-turn recycle is running stays available and is
retried on the next sweep.

`respawnCauseRecycle` continues to distinguish scheduled maintenance from lazy
crash recovery. The trigger reason is an additional classification used for logs
and metrics, not a third respawn lifecycle. Successful replacement increments the
existing aggregate recycle counter and records the reason-specific event. A
failed replacement follows current behavior: mark the slot dead, requeue it, and
let the next acquisition retry lazy recovery.

## 7. Failure behavior and logging

Memory reclamation is opportunistic and must never reduce serving correctness:

- Unreadable PID, access denial, exited process, or failed OS memory call means
  "sample unavailable," not zero. The slot is returned immediately.
- A single sample failure is not a worker failure and does not affect pool health.
- Unsupported-platform configuration produces one startup warning, not a warning
  on every cadence.
- Busy workers are neither sampled nor interrupted.
- Shutdown wins over a pending sweep or recycle; post-close slots are dropped for
  `closeAll` to own, matching existing behavior.
- Recycle spawn/initialize failures continue to populate existing spawn-failure
  health and metrics.

The trigger log is INFO and contains only bounded operational data:

```text
pool: slot recycling
label=<slot>
trigger=idle_memory|max_turns
pid=<pid>
turns=<count>
user_requests=<count>
idle_for=<duration when applicable>
rss_bytes=<bytes when applicable>
idle_threshold=<duration>
memory_threshold_bytes=<bytes>
```

Do not log a client-supplied session ID, prompt data, user identity, or working
directory from this path.

## 8. Metrics

Preserve these existing metrics unchanged:

- `gw_pool_slot_recycles_total`: aggregate successful scheduled replacements.
- `gw_worker_resident_memory_bytes{slot}`: current direct-worker RSS/working set.
- Existing HTTP, LLM-request, Kiro-turn, pool-health, and spawn-failure metrics.

Add bounded-cardinality metrics:

| Metric | Type | Labels | Semantics |
|---|---|---|---|
| `gw_pool_slot_recycles_by_reason_total` | counter | `reason=max_turns|idle_memory` | Successful scheduled replacements by trigger. The reason set is closed and must not accept arbitrary strings. |
| `gw_worker_user_requests_since_spawn` | gauge | `slot` | Successful user-path `session/new` calls served by the current worker. Resets on replacement; excludes catalog probes. |
| `gw_worker_idle_seconds` | gauge | `slot` | Seconds since the last completed user request while the worker is free. Emits zero before first use and while busy; pair with the user-request gauge to distinguish unused from newly released. |
| `gw_pool_idle_memory_recycle_trigger_rss_bytes` | histogram | none beyond existing constant gateway labels | Direct working set observed for successful `idle_memory` replacements. |
| `gw_pool_idle_memory_recycle_trigger_idle_seconds` | histogram | none beyond existing constant gateway labels | Completed-request idle duration observed for successful `idle_memory` replacements. |

The RSS histogram uses MiB-equivalent buckets at 256, 384, 512, 768, 1024,
1536, 2048, 4096, and 8192 MiB. The idle histogram uses buckets at 60, 300,
900, 1800, 3600, 14400, 43200, and 86400 seconds. Record them only after
replacement succeeds. Do not publish a "bytes freed" metric: an immediate
replacement has a changing warmup working set, and subtracting two point
samples would overstate reclamation.

Extend the pool metrics recorder with one bounded recycle-event callback so the
pool can record reason and trigger observations without importing the metrics
package. Extend the worker projection used by the pull collector with current
user-request and idle state. User/session identifiers never become labels.

Do not add a sweep-check counter. It would primarily measure configured cadence,
not user behavior, and operators can derive policy state from user requests,
idle time, RSS, and recycle events.

## 9. Admin dashboard

Extend the additive admin snapshot contract with pool-policy fields:

- idle threshold;
- memory threshold in bytes; and
- memory-policy support boolean.

Extend each real slot row with:

- user requests since spawn; and
- nullable last user-release timestamp.

The browser derives current idle duration from the snapshot's generated time and
last-release timestamp. It renders zero/active for a busy slot and an em dash for
an unused slot. Slot cards add a third compact row:

- `USER REQS`: current worker's user-request count;
- `IDLE`: current idle duration and configured threshold, such as `12m / 15m`.

When the policy is enabled and supported, the existing memory cell includes its
threshold, such as `812 / 500 MiB`. The existing PID and uptime reset remain the
visual confirmation that eager replacement completed. On macOS, process memory
and the policy render as unavailable rather than zero.

The snapshot changes are additive. Existing consumers that ignore the new fields
remain compatible.

## 10. Grafana dashboard

`scripts/gen_grafana_dashboard.py` is the source of truth. Update it and regenerate
`docs/grafana/otto-gateway-dashboard.json`; never hand-edit only the generated
JSON.

Add an **Idle Memory Recycling** row with these panels:

1. **Worker recycles by reason** — increase/rate of
   `gw_pool_slot_recycles_by_reason_total`, grouped by its bounded reason label.
2. **Idle-memory recycles per 100 LLM requests** — idle-memory recycle increase
   divided by `gw_llm_requests_total`, guarded against a zero denominator.
3. **Worker idle and use since spawn** — per-gateway/slot view using
   `gw_worker_idle_seconds` and `gw_worker_user_requests_since_spawn` with
   unit-appropriate fields.
4. **Idle-memory trigger distributions** — count plus p50/p95 views derived from
   the trigger RSS and idle histograms, gated so an empty range renders no
   fabricated quantile.

Keep current worker memory trends in the existing Runtime Resources row. Update
the generator's metric inventory, row/panel contract tests, and checked-in JSON
in the same change.

## 11. Tests and verification

### 11.1 Configuration

- Binary defaults are idle disabled and memory 500 MiB.
- Millisecond integer and Go-duration parsing succeed.
- The 15-minute wrapper values load as intended.
- Negative idle, zero/negative memory, overflow, and values above 1,048,576 MiB
  fail startup with the environment-variable name in the error.
- Main wiring forwards effective values, support capability, and memory reader.

### 11.2 Pool lifecycle

Use fake clients, an injected clock, an injected memory reader, and a direct
single-sweep test seam. Unit tests must not sleep to await production cadence.

- An unused high-memory worker is not sampled or recycled.
- Catalog probes do not mark a worker user-used.
- A busy worker is not claimable and is never sampled or recycled.
- A used free worker below the idle threshold is not sampled.
- A sufficiently idle worker at exactly the threshold is not recycled.
- A sufficiently idle worker one byte above the threshold is recycled.
- Invalid PID and unavailable sample requeue exactly once.
- Success, request cancellation, explicit cancellation, and prompt failure set
  the release timestamp once.
- A later request resets idle eligibility.
- Successful replacement resets turn, user, idle, and spawn state together.
- Turn and idle triggers cannot admit concurrent scheduled replacements.
- A deferred idle candidate remains available and is retried later.
- Recycle failure, pool close, and close-during-sweep preserve slot ownership and
  goroutine shutdown guarantees.
- The race detector covers concurrent acquire/release/sweep/recycle/close paths.

### 11.3 Platform and process sampling

- Windows and Linux report process-memory support.
- Other platforms report unsupported.
- Existing live-process sampler contracts remain intact.
- Pool tests inject samples and run on every development platform; they do not
  depend on the macOS fallback.

### 11.4 Metrics and UI

- Existing aggregate recycle totals remain byte-compatible.
- Reason metrics accept only the two defined reasons.
- Gauges reset and busy/unused idle semantics are correct.
- Trigger histograms observe successful idle-memory replacements once and do not
  observe failed attempts.
- Admin JSON carries thresholds, capability, user count, and release timestamp.
- JavaScript tests cover unused, busy, eligible, unsupported, and post-recycle
  slot-card states.
- Grafana generator tests require the new row, panels, queries, metrics, units,
  and regenerated JSON equality.

Run focused pool/config/metrics/admin tests, targeted `go test -race` for the
pool, the full Go test suite, JavaScript admin tests, dashboard generator tests,
and the repository's normal lint/trust gates before completion.

## 12. Implementation touchpoints

Expected files include:

- `internal/config/config.go` and config tests
- `internal/pool/config.go`, `pool.go`, `detail.go`, stats/metrics projections,
  and focused recycle tests
- `internal/procstat/*` for an explicit support capability, without changing the
  existing sample definition
- `internal/metrics/metrics.go`, pool/worker collectors, and tests
- `cmd/otto-gateway/main.go` and wiring tests
- `internal/admin/snapshot.go`, admin adapters, static JavaScript, and tests
- `scripts/.env.example`, `scripts/gw`, `scripts/gw.ps1`, and support-bundle tests
- `scripts/gen_grafana_dashboard.py`, its tests, and
  `docs/grafana/otto-gateway-dashboard.json`
- `docs/operating.md` and `docs/grafana/README.md`

The implementation plan must confirm the exact test-file set after inspecting
nearby patterns. It must not introduce unrelated pool, session, metrics, or UI
refactors.

## 13. Non-goals

- Parking workers cold or shrinking the configured pool.
- Recycling dedicated stateful sessions by memory.
- macOS process-memory implementation.
- Process-tree RSS aggregation.
- Changing Kiro's own allocator or forcing it to release memory in place.
- Replacing or weakening the existing max-turn policy.
- Adding user/session identity to metrics or recycle logs.
- Estimating reclaimed bytes.

## 14. Acceptance criteria

The feature is complete when a Windows laptop using the wrapper defaults can
serve one pooled request, leave that worker free for at least 15 minutes, observe
its direct working set above 500 MiB, and eagerly replace it without interrupting
traffic or waiting for 20 turns. The replacement resets per-worker lifecycle
state, all existing pool recovery/shutdown guarantees remain true, macOS degrades
explicitly and safely, and developers can explain each recycle through logs,
Prometheus, the admin slot card, and the checked-in Grafana dashboard.
