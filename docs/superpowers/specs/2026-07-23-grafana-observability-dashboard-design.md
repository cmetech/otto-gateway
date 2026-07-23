# Grafana Usage and Gateway Observability Dashboard

**Date:** 2026-07-23
**Status:** Approved design
**Scope:** Gateway Prometheus instrumentation, Grafana dashboard generator/JSON, and parity tests

## Goal

Make the Loop24 Co-worker Grafana dashboard answer, in descending order of
importance:

1. Is the fleet serving users successfully?
2. Who is using the gateway, and how?
3. Which users or gateways are experiencing failures or contention?
4. What is driving cost, context pressure, and compression?
5. Are gateway processes and workers healthy?

The dashboard must remain fleet-safe: every applicable panel can be sliced by
the stable gateway identity, while other filters apply only where their metric
dimensions are semantically valid.

## Current-State Findings

The gateway registers 30 custom `gw_*` metric families. The current dashboard
queries 26. These four registered families are absent:

- `gw_compress_runs_total`
- `gw_compress_tokens_saved_estimate_total`
- `gw_pool_last_progress_timestamp_seconds`
- `gw_pool_last_spawn_error_timestamp_seconds`

The current HTTP error-rate panel is not an application success-rate signal.
Streaming handlers commit HTTP 200 before the stream finishes, so a later idle
timeout, upstream error, write failure, or client cancellation can still be
recorded as `status="200"`.

The current metrics also cannot distinguish pool queue time from model time,
streaming from non-streaming behavior, or stateful from stateless usage.

## Design Principles

- Put fleet status and user impact above implementation internals.
- Record application outcomes at the point where the gateway knows the final
  result; do not infer them only from HTTP status.
- Keep operational labels deliberately low-cardinality.
- Preserve stable gateway slicing through the remote writer's
  `instance=<gateway_id>` label.
- Do not pretend a filter applies to metrics that do not carry that dimension.
- Prefer a small number of metrics that answer operational decisions over a
  broad runtime-metric dump.
- Keep `scripts/gen_grafana_dashboard.py` as the dashboard source of truth; the
  committed JSON is generated output.

## Metric Additions

### LLM application outcomes

Add:

```text
gw_llm_request_outcomes_total{
  surface,
  outcome,
  stream,
  session_mode
}
```

Allowed label values:

| Label | Values |
|---|---|
| `surface` | `anthropic`, `openai`, `ollama` |
| `outcome` | `success`, `invalid_request`, `authentication`, `pool_exhausted`, `stream_idle_timeout`, `upstream_error`, `client_cancelled`, `internal_error` |
| `stream` | `true`, `false`, `unknown` |
| `session_mode` | `stateful`, `stateless`, `unknown` |

Every recognized LLM endpoint records exactly one outcome, including OpenAI
`/v1/completions`, which must also be included in the existing
`gw_llm_requests_total` surface mapping.

The surface adapters own outcome classification because they know whether a
stream completed, emitted a terminal error after HTTP 200, failed validation,
or failed to acquire capacity. Each adapter receives an optional
consumer-defined callback in its configuration. The callback accepts a small
adapter-owned observation value containing `outcome`, `stream`, and
`session_mode`; `cmd/otto-gateway` bridges that value into
`Metrics.RecordLLMOutcome(surface, ...)`. This preserves the existing boundary
pattern: adapters do not import `internal/metrics`.

The handler initializes an observation conservatively as `internal_error`,
updates request shape after successful decoding, and sets the final outcome on
each terminal branch. A deferred callback records exactly once. Expected client
disconnects classify as `client_cancelled`; they do not inflate server-error
counts.

### Pool acquisition

Add:

```text
gw_pool_acquire_duration_seconds{result}
```

Use latency buckets tuned for a local warm pool:

```text
0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30
```

Allowed `result` values:

- `immediate`: a slot was available on the non-blocking fast path.
- `waited`: a slot was acquired after the request parked.
- `timeout`: `AcquireTimeout` elapsed.
- `cancelled`: the caller context ended while waiting.
- `closed`: the pool began or completed shutdown while waiting.

The observation ends when a slot is acquired or acquisition terminates. Worker
respawn and `session/new` execution are not included, so this histogram remains
a queue-pressure signal rather than another end-to-end latency metric.

Extend the pool's existing consumer-defined metrics interface with a pool
acquisition recorder. `Pool.NewSession` records exactly once on every acquire
path.

### Compression health

Keep the existing metrics and add:

```text
gw_compress_eligible_total
gw_compress_budget_unmet_total
gw_compress_panic_recoveries_total
```

Definitions:

- `eligible_total`: enabled requests with non-empty messages whose estimated
  size reaches `TriggerTokens` and exceeds `BudgetTokens`, so the compression
  pipeline actually runs.
- `runs_total`: eligible requests that achieve positive estimated token
  savings; existing definition is unchanged.
- `tokens_saved_estimate_total`: estimated tokens saved across successful
  compression runs; existing definition is unchanged.
- `budget_unmet_total`: eligible requests still above `BudgetTokens` after all
  safe compression stages.
- `panic_recoveries_total`: panics recovered by `CompressionHook.Before`.

The hook exposes one atomic stats snapshot. `RegisterCompression` continues to
use pull-style `CounterFunc` collectors and maps the complete snapshot into the
five Prometheus counters.

## Existing Metrics Newly Used

The dashboard must add panels or columns for:

- `gw_pool_last_progress_timestamp_seconds`
- `gw_pool_last_spawn_error_timestamp_seconds`
- `process_start_time_seconds`
- `process_max_fds`

Timestamp queries must exclude a zero timestamp so a never-initialized pool does
not display an epoch-sized age.

## Dashboard Information Hierarchy

Rows appear in this order.

### 1. Fleet Overview

Headline stats only:

- Active gateways in the selected range
- LLM requests in range
- LLM application success rate
- Gateways with application failures
- Active sessions
- Pool acquisition p95
- Kiro credits in range
- Unhealthy or stalled gateways

This row answers whether users are active and whether the fleet is serving them.
The existing “Gateways Reporting” value moves to Fleet Inventory because
reporting installations are not the same as active users.

### 2. User Activity and Adoption

- Active gateways over time
- Requests per active gateway
- Requests by surface
- Requests by skill
- Requests by client
- Requests by model
- Streaming versus non-streaming
- Stateful versus stateless
- Top gateways by request volume
- Top skills
- Top clients by surface
- Attribution completeness for `skill="none"` and `client="none"`

### 3. User Experience and Failures

- Application success/failure rate over time
- Outcomes by type and surface
- Top affected gateways
- HTTP request latency p50/p95/p99
- HTTP latency heatmap
- Request rate by HTTP status
- Request rate by route

Application outcomes lead this row; protocol-level HTTP diagnostics follow.

### 4. Gateway Capacity and Pool Health

- Pool utilization (`busy / size`) by gateway
- Pool acquire p50/p95/p99
- Acquire results by type
- Seconds since pool progress
- Active sessions by gateway
- Session lifecycle rate
- Respawns, scheduled recycles, and ping escalations
- Gateway health matrix with last-progress age and last-spawn-error age

### 5. Kiro Cost and Context

- Credits and turns rate
- Credits per turn
- Credits per LLM request
- Turn-duration p50/p95
- Context p50/p95 and p95 gauge
- MCP initialization outcomes and failure rate

### 6. Compression Effectiveness

- Eligible requests
- Successful compression runs
- Estimated tokens saved
- Estimated tokens saved per successful run
- Compression success ratio (`runs / eligible`)
- Budget-unmet count and ratio
- Panic recoveries

The row description must state that token values are the UTF-8-bytes/4
heuristic, not billing tokens.

### 7. Runtime Resources

- Gateway CPU and RSS
- Worker CPU and RSS
- Open FD utilization (`open / max`)
- Gateway uptime
- Restart count in range
- Optional Go goroutines panel

Worker panels must say that worker process sampling is unavailable on macOS.
The goroutine panel remains explicitly conditional because `go_*` is excluded
from the default remote-write allowlist.

### 8. Fleet Inventory

- Gateways reporting now
- Gateway version/build table
- Lower-priority last-seen and identity context

## Dashboard Variables and Filter Scope

Variables, in order:

1. Data source
2. Gateway ID
3. Surface
4. Outcome
5. Streaming
6. Session Mode
7. Skill
8. Client
9. Model

`Gateway ID` continues to query the remote writer's stable `instance` label and
applies to every panel.

Operational panels use Gateway ID and, where present, Surface, Outcome,
Streaming, and Session Mode. Usage panels additionally use Skill, Client, and
Model only when the underlying metric contains those labels.

The README and dashboard descriptions must not claim every variable affects
every panel. They must explain that filter applicability follows metric
dimensions.

## Cardinality Budget

The new outcome metric has at most:

```text
3 surfaces × 8 outcomes × 3 stream values × 3 session modes = 216
```

possible series per gateway, with normal operation using a much smaller subset.
It does not include skill, client, model, route, raw error, request ID, session
ID, or user-controlled strings.

The pool histogram has five bounded result values. Compression metrics have no
variable labels. Existing skill/client/model cardinality limiters remain
unchanged.

## Failure-Mode Map

| Failure mode | Durable signal |
|---|---|
| Streaming error after HTTP 200 | `gw_llm_request_outcomes_total{outcome!="success"}` |
| Pool saturated and requests waiting | acquire-duration quantiles and `result="waited"` |
| Pool acquisition timeout | `result="timeout"` plus application outcome `pool_exhausted` |
| Pool alive but not making progress | age of `gw_pool_last_progress_timestamp_seconds` |
| Worker spawn recently failed | age of `gw_pool_last_spawn_error_timestamp_seconds` |
| Compression runs but cannot meet budget | `gw_compress_budget_unmet_total` |
| Compression silently recovers a panic | `gw_compress_panic_recoveries_total` |
| Gateway restarts repeatedly | changes in `process_start_time_seconds` |
| File descriptors approach limit | `process_open_fds / process_max_fds` |

Remote-write delivery failures remain tray-local debug logs. A metric sent over
the same failing channel cannot provide reliable real-time delivery status, so
remote-write instrumentation is outside this change.

## Generator and Parity Contract

Refactor `scripts/gen_grafana_dashboard.py` into importable construction and
write functions plus a `main` entry point. Importing it must not rewrite the
dashboard.

Add standard-library Python tests that:

- Assert the exact row order above.
- Assert the variable order and names.
- Assert required new panels exist.
- Extract metric identifiers from every PromQL expression and assert all custom
  `gw_*` families intended for Grafana are referenced.
- Assert the generated dashboard exactly matches
  `docs/grafana/otto-gateway-dashboard.json`.
- Assert every panel contains the Gateway ID selector, except datasource-only
  metadata where no metric query exists.

The parity allowlist may explicitly exclude no custom family after this change.
Histogram families count as covered when their `_bucket`, `_sum`, or `_count`
series is queried.

## Testing Strategy

### Metrics

- Scrape tests for the new outcome counter labels.
- Compression scrape tests for all five counters.
- Existing gateway constant-label assertions remain valid.

### Adapters

- One success and one failure classification test per surface.
- At least one streaming idle-timeout test proving the application outcome is
  failure even though the response status is already 200.
- Validation and pool-exhaustion paths use their explicit outcomes.
- Callback is invoked exactly once per request.

### Pool

- Immediate acquisition observes `result="immediate"`.
- A parked acquisition observes `result="waited"`.
- Timeout and caller cancellation observe their respective results.
- Each path records exactly one histogram observation.

### Dashboard

- Generator contract tests described above.
- JSON parses successfully.
- PromQL uses bounded labels and zero-safe timestamp arithmetic.
- The committed JSON is regenerated only from the tested generator.

### Verification

- `go test ./internal/metrics ./internal/pool ./internal/adapter/...`
- `go test ./cmd/otto-gateway ./cmd/otto-tray`
- `python3 -m unittest scripts/test_gen_grafana_dashboard.py`
- `python3 scripts/gen_grafana_dashboard.py`
- Re-run the Python test to prove generated JSON parity.
- `go test ./...`
- `go vet ./...`
- `git diff --check`

## Non-Goals

- No raw user, request, session, prompt, tool, or error text in metric labels.
- No full cross-product of skill, client, model, and operational outcomes.
- No new metrics backend or Grafana alert provisioning.
- No change to remote-write credentials, endpoint, or default series prefixes.
- No attempt to make per-worker resource metrics available on macOS.
