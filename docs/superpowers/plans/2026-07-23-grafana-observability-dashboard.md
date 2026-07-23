# Grafana Usage and Gateway Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` (recommended) or
> `superpowers:executing-plans` to implement this plan task-by-task.

**Goal:** Add low-cardinality application-outcome, pool-wait, and compression
health telemetry, then reorganize the Grafana dashboard so fleet status, user
activity, user impact, capacity, cost, compression, runtime health, and
inventory appear in descending operational importance.

**Architecture:** Surface adapters classify final LLM outcomes and notify an
optional consumer-owned callback exactly once per request. The gateway command
bridges those observations into Prometheus without making adapters depend on
the metrics package. Pool acquisition is measured inside `Pool.NewSession`.
Compression exposes a single atomic snapshot consumed by pull-style
`CounterFunc` collectors. The Python dashboard generator remains the source of
truth and gains import-safe construction/write functions plus structural and
metric-parity tests.

**Tech Stack:** Go, Prometheus Go client, Python standard library `unittest`,
Grafana dashboard JSON, PromQL.

## Global Constraints

- Follow strict red-green-refactor: add a focused failing test, run it and
  inspect the expected failure, add the minimum implementation, then rerun.
- Every applicable dashboard query must contain the external gateway selector
  `instance=~"$gateway_id"`.
- Do not add raw user text, session IDs, request IDs, error strings, or any
  unbounded label.
- Keep the application-outcome series bounded to
  `surface × outcome × stream × session_mode`.
- Do not change the remote-write allowlist or add backend/alerting work.
- Preserve unrelated worktree changes and do not regenerate the dashboard
  before its generator tests are in place.

---

## Task 1: Add Compression Decision and Recovery Signals

**Files:**

- Modify: `internal/plugin/compress/hook.go`
- Modify: `internal/plugin/compress/hook_test.go`
- Modify: `internal/metrics/metrics.go`
- Modify: `internal/metrics/metrics_test.go`
- Modify: `cmd/otto-gateway/main.go`

### Step 1: Write failing hook snapshot tests

Add tests that exercise:

- one below-trigger request: no counters change;
- one eligible request that saves tokens: `Eligible=1`, `Runs=1`,
  `SavedTokens>0`;
- one eligible request that remains above budget: `BudgetUnmet=1`;
- a panicking compression stage: `PanicRecoveries=1` and the panic remains
  contained.

Replace tuple assertions against `Stats()` with assertions against a snapshot:

```go
type Stats struct {
    Eligible       int64
    Runs           int64
    SavedTokens    int64
    BudgetUnmet    int64
    PanicRecoveries int64
}
```

Run:

```bash
go test ./internal/plugin/compress -run 'TestCompressionHook_(Stats|Panic)' -count=1
```

Expected: FAIL because `Stats()` does not expose eligible, budget-unmet, or
panic-recovery totals.

### Step 2: Implement one atomic compression snapshot

Add atomic fields for eligible decisions and panic recoveries. Increment
eligible immediately before the compression pipeline runs, budget-unmet after
all safe stages, and panic recoveries in the existing recovery path. Return all
five values from a single `Stats()` snapshot. Preserve the current definition
of a successful run and saved-token estimate.

Run:

```bash
go test ./internal/plugin/compress -count=1
```

Expected: PASS.

### Step 3: Write failing Prometheus collector tests

Extend metrics tests to register a deterministic compression snapshot and
assert these five families and values:

```text
gw_compress_eligible_total
gw_compress_runs_total
gw_compress_tokens_saved_estimate_total
gw_compress_budget_unmet_total
gw_compress_panic_recoveries_total
```

Run:

```bash
go test ./internal/metrics -run TestCompression -count=1
```

Expected: FAIL because the metrics package only exports two compression
counters.

### Step 4: Register all compression counters and wire the command

Define a metrics-owned `CompressionStats` value and accept a callback returning
that value. Register all five values as `prometheus.NewCounterFunc` collectors.
In `cmd/otto-gateway/main.go`, adapt the hook snapshot to the metrics snapshot
with a closure, keeping the package dependency one-way.

Run:

```bash
go test ./internal/metrics ./internal/plugin/compress ./cmd/otto-gateway -count=1
```

Expected: PASS.

### Step 5: Commit

```bash
git add internal/plugin/compress/hook.go internal/plugin/compress/hook_test.go internal/metrics/metrics.go internal/metrics/metrics_test.go cmd/otto-gateway/main.go
git commit -m "feat(metrics): expose compression decision health"
```

---

## Task 2: Measure Pool Acquisition Pressure

**Files:**

- Modify: `internal/metrics/metrics.go`
- Modify: `internal/metrics/metrics_test.go`
- Modify: `internal/pool/config.go`
- Modify: `internal/pool/pool.go`
- Modify: `internal/pool/kiro_metrics_test.go`
- Add: `internal/pool/acquire_metrics_test.go`
- Modify: `cmd/otto-gateway/main.go`

### Step 1: Write failing histogram tests

Add a metrics test that records all allowed results and gathers
`gw_pool_acquire_duration_seconds`. Assert the label set and explicit buckets:

```go
[]float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30}
```

Run:

```bash
go test ./internal/metrics -run TestPoolAcquireDuration -count=1
```

Expected: FAIL because the histogram and recording method do not exist.

### Step 2: Implement the histogram

Add a `HistogramVec` with only `result` as a label and a
`RecordPoolAcquire(duration, result)` method. Register it with the existing
gateway registry.

Run:

```bash
go test ./internal/metrics -run TestPoolAcquireDuration -count=1
```

Expected: PASS.

### Step 3: Write failing pool-path tests

Extend the pool metrics fake with `RecordPoolAcquire`. Add deterministic tests
for:

- an immediately available slot → `immediate`;
- a request that parks and later acquires → `waited`;
- acquire timeout → `timeout`;
- caller cancellation → `cancelled`;
- pool shutdown while waiting → `closed`.

Each test must assert one and only one observation. Avoid timing assertions
beyond non-negative duration.

Run:

```bash
go test ./internal/pool -run TestNewSession_RecordsAcquire -count=1
```

Expected: FAIL because `NewSession` does not report acquisition outcomes.

### Step 4: Instrument `Pool.NewSession`

Extend the existing consumer-defined pool metrics interface. Start timing at
entry, record before returning from each acquisition terminal path, and
distinguish the non-blocking fast path from a successful blocked receive.
Record before respawn or `session/new` so the metric measures only queue wait.

Wire `Metrics.RecordPoolAcquire` through `cmd/otto-gateway`.

Run:

```bash
go test ./internal/pool ./internal/metrics ./cmd/otto-gateway -count=1
```

Expected: PASS.

### Step 5: Commit

```bash
git add internal/metrics/metrics.go internal/metrics/metrics_test.go internal/pool/config.go internal/pool/pool.go internal/pool/kiro_metrics_test.go internal/pool/acquire_metrics_test.go cmd/otto-gateway/main.go
git commit -m "feat(metrics): measure pool acquisition pressure"
```

---

## Task 3: Add Application-Outcome Metric and OpenAI Coverage

**Files:**

- Modify: `internal/metrics/metrics.go`
- Modify: `internal/metrics/metrics_test.go`
- Modify: `internal/adapter/openai/adapter.go`
- Modify: `internal/adapter/openai/handlers.go`
- Modify: `internal/adapter/openai/sse.go`
- Add: `internal/adapter/openai/observation_test.go`
- Modify: `cmd/otto-gateway/main.go`

### Step 1: Write failing metrics and route-mapping tests

Add a metrics test that records representative observations and asserts:

```text
gw_llm_request_outcomes_total{
  surface="openai",
  outcome="success",
  stream="false",
  session_mode="stateless"
}
```

Also assert that `/v1/completions` maps to the existing OpenAI LLM request
surface.

Run:

```bash
go test ./internal/metrics -run 'Test(LLMRequestOutcome|SurfaceForRoute)' -count=1
```

Expected: FAIL because the counter is absent and completions are not mapped.

### Step 2: Implement the bounded counter and route mapping

Register `gw_llm_request_outcomes_total` with the four approved labels. Add
`RecordLLMOutcome(surface, outcome, stream, sessionMode)`. Include
`/v1/completions` in `surfaceForRoute`.

Run:

```bash
go test ./internal/metrics -run 'Test(LLMRequestOutcome|SurfaceForRoute)' -count=1
```

Expected: PASS.

### Step 3: Write failing OpenAI exactly-once tests

Add an adapter-owned observation value:

```go
type RequestObservation struct {
    Outcome     string
    Stream      string
    SessionMode string
}
```

Tests should install a callback collector and cover:

- invalid JSON or validation → `invalid_request`, unknown shape;
- missing/failed authentication → `authentication`;
- capacity/session acquisition failure → `pool_exhausted`;
- successful non-streaming stateless request → `success,false,stateless`;
- successful streaming stateful request → `success,true,stateful`;
- idle timeout after HTTP 200 → `stream_idle_timeout,true,...`;
- cancelled context → `client_cancelled`;
- engine/upstream failure → `upstream_error`;
- local rendering/flushing failure → `internal_error`.

Assert the callback is invoked exactly once for both
`/v1/chat/completions` and `/v1/completions`.

Run:

```bash
go test ./internal/adapter/openai -run TestRequestObservation -count=1
```

Expected: FAIL because the adapter has no observation callback.

### Step 4: Classify OpenAI terminal paths

Add an optional callback to the adapter config. Initialize a conservative
observation at handler entry and defer one callback. Set request shape after
decode and session mode after resolution. Classify every return branch,
including errors emitted after streaming headers have committed.

Wire the OpenAI callback in `cmd/otto-gateway` with the fixed surface
`"openai"`.

Run:

```bash
go test ./internal/adapter/openai ./internal/metrics ./cmd/otto-gateway -count=1
```

Expected: PASS.

### Step 5: Commit

```bash
git add internal/metrics/metrics.go internal/metrics/metrics_test.go internal/adapter/openai/adapter.go internal/adapter/openai/handlers.go internal/adapter/openai/sse.go internal/adapter/openai/observation_test.go cmd/otto-gateway/main.go
git commit -m "feat(metrics): record OpenAI application outcomes"
```

---

## Task 4: Add Anthropic Application-Outcome Coverage

**Files:**

- Modify: `internal/adapter/anthropic/adapter.go`
- Modify: `internal/adapter/anthropic/handlers.go`
- Modify: `internal/adapter/anthropic/sse.go`
- Add: `internal/adapter/anthropic/observation_test.go`
- Modify: `cmd/otto-gateway/main.go`

### Step 1: Write failing Anthropic observation tests

Mirror the OpenAI behavior contract for the Anthropic messages surface.
Include a post-HTTP-200 idle-timeout case and exactly-once assertion.

Run:

```bash
go test ./internal/adapter/anthropic -run TestRequestObservation -count=1
```

Expected: FAIL because the callback contract is not implemented.

### Step 2: Implement and wire Anthropic classification

Add the adapter-owned observation value and optional callback. Classify all
terminal paths using the same bounded vocabulary, then bridge the callback in
`cmd/otto-gateway` with surface `"anthropic"`.

Run:

```bash
go test ./internal/adapter/anthropic ./cmd/otto-gateway -count=1
```

Expected: PASS.

### Step 3: Commit

```bash
git add internal/adapter/anthropic/adapter.go internal/adapter/anthropic/handlers.go internal/adapter/anthropic/sse.go internal/adapter/anthropic/observation_test.go cmd/otto-gateway/main.go
git commit -m "feat(metrics): record Anthropic application outcomes"
```

---

## Task 5: Add Ollama Application-Outcome Coverage

**Files:**

- Modify: `internal/adapter/ollama/adapter.go`
- Modify: `internal/adapter/ollama/handlers.go`
- Modify: `internal/adapter/ollama/ndjson.go`
- Add: `internal/adapter/ollama/observation_test.go`
- Modify: `cmd/otto-gateway/main.go`

### Step 1: Write failing Ollama observation tests

Cover both chat and generate endpoints, streaming and non-streaming, session
mode, pool failure, upstream failure, cancellation, idle timeout after headers,
and exactly-once notification.

Run:

```bash
go test ./internal/adapter/ollama -run TestRequestObservation -count=1
```

Expected: FAIL because the callback contract is not implemented.

### Step 2: Implement and wire Ollama classification

Add the adapter-owned observation value and optional callback. Classify all
terminal paths and bridge the callback in `cmd/otto-gateway` with surface
`"ollama"`.

Run:

```bash
go test ./internal/adapter/ollama ./cmd/otto-gateway -count=1
```

Expected: PASS.

### Step 3: Verify the end-to-end metric contract

Run:

```bash
go test ./internal/adapter/... ./internal/metrics ./internal/pool ./internal/plugin/compress ./cmd/otto-gateway -count=1
```

Expected: PASS with all recognized LLM endpoints reporting one bounded
application outcome.

### Step 4: Commit

```bash
git add internal/adapter/ollama/adapter.go internal/adapter/ollama/handlers.go internal/adapter/ollama/ndjson.go internal/adapter/ollama/observation_test.go cmd/otto-gateway/main.go
git commit -m "feat(metrics): record Ollama application outcomes"
```

---

## Task 6: Make the Dashboard Generator Import-Safe and Test Its Contract

**Files:**

- Modify: `scripts/gen_grafana_dashboard.py`
- Add: `scripts/test_gen_grafana_dashboard.py`

### Step 1: Write failing generator-structure tests

Using only the Python standard library, import the generator from its path and
assert import does not change the dashboard file. Add tests for:

- exact variable order:
  `Data source`, `Gateway ID`, `Surface`, `Outcome`, `Streaming`,
  `Session Mode`, `Skill`, `Client`, `Model`;
- exact row order from the approved design;
- presence of required headline and diagnostic panels;
- every metric-bearing target includes `instance=~"$gateway_id"`;
- all 35 custom metric families are referenced after normalizing histogram
  `_bucket`, `_sum`, and `_count` suffixes;
- serialized `build_dashboard()` output exactly matches the committed JSON.

Run:

```bash
python3 -m unittest scripts.test_gen_grafana_dashboard
```

Expected: FAIL because importing rewrites JSON, construction is not exposed,
the hierarchy is old, and custom metric coverage is incomplete.

### Step 2: Refactor construction and writing

Expose:

```python
def build_dashboard():
    ...

def write_dashboard(path=DASHBOARD_PATH):
    ...

if __name__ == "__main__":
    write_dashboard()
```

Importing the module may construct immutable constants but must not write.
Keep deterministic IDs, panel order, formatting, and JSON serialization.

Run:

```bash
python3 -m unittest scripts.test_gen_grafana_dashboard
```

Expected: still FAIL only on the new hierarchy/coverage assertions.

### Step 3: Commit the generator seam and tests

```bash
git add scripts/gen_grafana_dashboard.py scripts/test_gen_grafana_dashboard.py
git commit -m "test(grafana): enforce dashboard generation contract"
```

---

## Task 7: Rebuild the Dashboard in Operational Priority Order

**Files:**

- Modify: `scripts/gen_grafana_dashboard.py`
- Regenerate: `docs/grafana/otto-gateway-dashboard.json`

### Step 1: Add variables in approved order

Create chained or independent query variables as appropriate:

- data source;
- gateway ID from `instance`;
- surface, outcome, stream, session mode from the outcome metric;
- skill and client from request attribution metrics;
- model from the model-request metric.

Use `allValue=".*"` where applicable. Do not add unsupported label filters to
metrics that lack those dimensions.

### Step 2: Build rows 1–3

Implement:

1. Fleet Overview headline stats;
2. User Activity and Adoption;
3. User Experience and Failures.

Use the application-outcome metric—not HTTP status—for success/failure.
Use zero-safe denominators with `clamp_min(...)`. Keep HTTP status and latency
as protocol diagnostics beneath outcome panels.

### Step 3: Build rows 4–6

Implement:

4. Gateway Capacity and Pool Health, including acquire quantiles/results and
   zero-filtered progress/spawn-error ages;
5. Kiro Cost and Context;
6. Compression Effectiveness with the UTF-8-bytes/4 heuristic caveat.

Use:

```promql
time() - gw_pool_last_progress_timestamp_seconds{...,} > 0
```

only with an explicit `gw_pool_last_progress_timestamp_seconds > 0` filter (and
the equivalent spawn-error filter), so never-initialized timestamps do not
produce epoch-sized ages.

### Step 4: Build rows 7–8

Implement:

7. Runtime Resources with gateway/worker CPU and RSS, FD utilization, uptime,
   restart count, and conditional goroutines;
8. Fleet Inventory with gateways reporting, version, build, and lower-priority
   fleet context.

State in worker panel descriptions that sampling is unavailable on macOS.
State that goroutines require forwarding `go_*`.

### Step 5: Regenerate and run dashboard tests

Run:

```bash
python3 scripts/gen_grafana_dashboard.py
python3 -m unittest scripts.test_gen_grafana_dashboard
python3 -m json.tool docs/grafana/otto-gateway-dashboard.json >/dev/null
```

Expected: PASS. The committed JSON exactly matches the generator and all custom
metric families are covered.

### Step 6: Commit

```bash
git add scripts/gen_grafana_dashboard.py scripts/test_gen_grafana_dashboard.py docs/grafana/otto-gateway-dashboard.json
git commit -m "feat(grafana): prioritize usage and gateway health insights"
```

---

## Task 8: Correct Dashboard Documentation and Complete Verification

**Files:**

- Modify: `docs/grafana/README.md`

### Step 1: Update operator guidance

Document:

- the eight-row information hierarchy;
- gateway slicing applies to every metric panel;
- surface/outcome/stream/session/skill/client/model filters apply only to
  compatible metrics;
- application outcomes differ from HTTP status;
- compression tokens are estimates;
- worker process metrics are unavailable on macOS;
- goroutine forwarding remains optional;
- the generator command and structural test command.

Remove the inaccurate claim that every dashboard filter affects every panel.

### Step 2: Run focused verification

```bash
gofmt -w internal/metrics/metrics.go internal/metrics/metrics_test.go internal/plugin/compress/hook.go internal/plugin/compress/hook_test.go internal/pool/config.go internal/pool/pool.go internal/pool/kiro_metrics_test.go internal/pool/acquire_metrics_test.go internal/adapter/openai/adapter.go internal/adapter/openai/handlers.go internal/adapter/openai/sse.go internal/adapter/openai/observation_test.go internal/adapter/anthropic/adapter.go internal/adapter/anthropic/handlers.go internal/adapter/anthropic/sse.go internal/adapter/anthropic/observation_test.go internal/adapter/ollama/adapter.go internal/adapter/ollama/handlers.go internal/adapter/ollama/ndjson.go internal/adapter/ollama/observation_test.go cmd/otto-gateway/main.go
go test ./internal/metrics ./internal/plugin/compress ./internal/pool ./internal/adapter/... ./cmd/otto-gateway -count=1
python3 -m unittest scripts.test_gen_grafana_dashboard
```

Expected: PASS.

### Step 3: Run repository-wide verification

```bash
go test ./... -count=1
go vet ./...
python3 scripts/gen_grafana_dashboard.py
python3 -m unittest scripts.test_gen_grafana_dashboard
git diff --check
git status --short
```

Expected:

- all Go tests and vet pass;
- dashboard tests pass;
- regeneration produces no diff;
- no whitespace errors;
- status contains only the intended final documentation change before commit.

### Step 4: Commit

```bash
git add docs/grafana/README.md
git commit -m "docs(grafana): explain observability filters and signals"
```

### Step 5: Final evidence review

Inspect:

```bash
git log --oneline --max-count=10
git diff HEAD~8..HEAD --stat
git status --short
```

Confirm the final handoff names the new metrics, the eight-row hierarchy,
gateway filtering behavior, test commands, and any environment-specific
limitations without claiming production Grafana validation.
