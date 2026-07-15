# Adversarial code review — kiro usage-metrics parity build

**Date:** 2026-07-14

**Scope:** `bc8f718..HEAD` on `feat/parity-metrics`

**Verdict:** **DON'T SHIP**

## Findings

| Severity | Location | Defect, failure scenario, and minimal fix |
|---|---|---|
| **HIGH** | `internal/session/registry.go:247` | **Recycle can terminate an active stream.** Request A holds `Entry.Mu` while streaming. A metadata frame raises `lastCtxPct` above the threshold. Concurrent request B calls `Get`, which deletes the entry and calls `Cancel`/`Close` without checking `Entry.Mu`. A receives `ErrClientClosed` and a truncated response. The transcript-resend safety argument only protects the next request; it does not make interrupting the current request safe. **Fix:** introduce a registry-visible `recycling` sentinel/channel, then acquire `Entry.Mu`, revalidate map ownership, and only close after the active request finishes. Merely unlocking `r.mu` and acquiring `Entry.Mu` is insufficient because another `Get` could return the doomed entry meanwhile. |
| **MEDIUM** | `internal/acp/client.go:1303`, `internal/metrics/metrics.go:117` | **Context metrics violate the once-per-completed-turn contract.** Every streaming context frame is observed, so busy or long turns dominate the histogram. A turn emitting `10,20,90,40(final)` produces count 4 and average 40 instead of count 1/value 40. This also adds synchronous Prometheus work to the ACP read loop for every frame. Classic histogram buckets cannot recover an exact “last” or “peak” value despite the help text claiming max parity. **Fix:** observe the completed-turn value only; if exact last/peak parity is required, expose explicit gauges/max state rather than claiming the histogram provides it. |
| **MEDIUM** | `internal/config/config.go:517`, `internal/config/config.go:1228` | **`CTX_RECYCLE_PCT` accepts `NaN`, `Inf`, and values above 100.** `strconv.ParseFloat` accepts the special values, and the only validation is `< 0`. `NaN`, `Inf`, or `101` therefore boot successfully but make normal 0–100 context values unable to trip recycling. **Fix:** require `!math.IsNaN(v)`, `!math.IsInf(v, 0)`, and `0 <= v && v <= 100`. |
| **MEDIUM** | `internal/acp/translate.go:111`, `internal/acp/client.go:1306` | **A completed zero-cost turn is silently dropped.** The contract says `OnTurnMeter` fires when `meteringUsage` is present, but `len(meta.MeteringUsage) > 0` treats an explicit empty array like absence. `{"meteringUsage":[],"turnDurationMs":1200}` records neither the turn nor its duration. **Fix:** represent `MeteringUsage` as `*[]meteringEntry` and test for non-nil presence. |
| **MEDIUM** | `cmd/otto-gateway/main.go:936`, `cmd/otto-gateway/main.go:992` | **Required per-session `ctx_pct` observability was not implemented.** The context-recycle design requires it in `/health/agents` and the admin snapshot, but both adapters still copy only ID/alive/busy/last-used/model. Operators cannot see a session approaching the threshold or verify its reset after recycling. **Fix:** add `ctx_pct` through `session.SessionDetail`, `server.AgentSession`, `admin.SnapshotSess`, and both adapters. |
| **LOW** | `internal/session/registry.go:455` | **Every planned recycle can emit a false “subprocess exited unexpectedly” warning.** Real `acp.Client.Close` closes `Done`; the watcher wakes, correctly fails the `cur == e` guard, but still logs the warning unconditionally. The recycle mock hides this because its `Close` does not close `Done` (`internal/session/registry_test.go:90`). **Fix:** log only when the watcher actually removed the current entry, or signal an explicit planned teardown reason; make the fake model real `Done` behavior. |

## Top-three reproductions

### 1. Live-stream truncation

1. Request A calls `Registry.Get("sid")`, then locks `e.Mu` and starts `Prompt`.
2. The ACP read loop receives `contextUsagePercentage:85`; the hook atomically stores 85.
3. Before A finishes, request B calls `Registry.Get("sid")`.
4. B deletes `e`, increments `recycled`, and closes A's client.
5. A's stream terminates with `ErrClientClosed`; B creates a new session.

The existing recycle test never holds `e.Mu` or runs a prompt concurrently.

### 2. Histogram skew

Send four frames for one turn:

```json
{"contextUsagePercentage":10}
{"contextUsagePercentage":20}
{"contextUsagePercentage":90}
{"contextUsagePercentage":40,"meteringUsage":[{"value":0.1,"unit":"credit"}]}
```

Current result: `gw_kiro_context_usage_percent_count == 4`. Contract result:
count `1`, completed-turn value `40`.

### 3. Invalid threshold accepted

These probes reached `config.Load` successfully and exposed the invalid values:

```text
CTX_RECYCLE_PCT=NaN -> RecyclePct got NaN
CTX_RECYCLE_PCT=Inf -> RecyclePct got +Inf
CTX_RECYCLE_PCT=101 -> RecyclePct got 101
```

## What was verified safe and why

- All `lastCtxPct` reads and writes use `atomic.Uint64`; the race-enabled suite is clean.
- The `cur == e` watcher guard prevents an old recycled entry from marking a newly created entry dead.
- `recycled` is incremented once under `r.mu`; concurrent `Get` calls cannot recycle the same mapped entry twice.
- Credit filtering matches the specified `unit=="credit" || unitPlural=="credits"` rule. Float64 drift is insignificant relative to Prometheus's float64 storage.
- Pointer fields correctly distinguish absent from zero for context percentage and duration; `contextUsagePercentage:0` fires the hook.
- Model, skill/flow, client, and MCP-server labels use independent 64-value limiters. All registered series inherit `gateway_id`.
- Production ACP callbacks perform no network or disk I/O: context uses an atomic store plus histogram observation; MCP uses a bounded mutex and counter.
- `OnModelRequest` fires exactly once for every non-nil `Run`, including pre-hook short circuits and ACP error exits.
- ACP changes are additive and the changed diff does not touch OpenAI, Anthropic, or Ollama response types.
- Early metrics construction is nil-safe because scrape closures check `a.pool` and `a.registry`.
- Adding `client` to `gw_llm_requests_total` intentionally changes series identity and splits/reset continuity at deployment. PromQL selectors tolerate the additional label, but dashboards and recording rules should receive a migration note.

## Verification evidence

- `go test ./...` — passed.
- `go test -race ./...` — passed, including real `kiro-cli` integration tests outside the restricted sandbox.
- `go vet ./...` — passed.
- Native, Linux, and Windows `CGO_ENABLED=0` gateway builds — passed.
- `git diff --check` and `gofmt -d` — clean.
- The Go module requires Go `1.26.5`.
- `gofumpt` and `golangci-lint` were not installed, so those two explicit gates were unavailable.

## Ship verdict

**DON'T SHIP.** The single most important fix is serializing proactive recycle
with `Entry.Mu` through a registry-visible recycling state so a
threshold-crossing session cannot be closed while its current stream is live.
