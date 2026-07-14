# Prometheus `/metrics` endpoint — design

**Date:** 2026-07-14
**Status:** Proposed (design) — awaiting review
**Scope:** One implementation plan (phased 4a → 4b). Track 4 of the
[legacy-gateway parity roadmap](../2026-07-14-legacy-gateway-parity-roadmap.md).
**New feature** (no Node precedent) — usage & ops insight via scrape → TSDB.

## Problem

The gateway has **zero** runtime metrics today. All signals are computed
on-demand for the JSON `/health*` endpoints; nothing is accumulated as a time
series. Operators cannot scrape usage (request volume/latency by surface, pool
occupancy, session counts, respawn/ping-escalation rates) into a timeseries DB
for dashboards, capacity planning, or alerting.

## Decisions (locked with the human)

- **Library:** `github.com/prometheus/client_golang` — pure-Go, keeps the
  `cmd/otto-gateway` binary cgo-free (the one cgo-ish dep, `energye/systray`, is
  imported only by `cmd/otto-tray`). `promhttp.Handler()` also gives Go-runtime
  + process metrics (`go_*`, `process_*`) for free.
- **Exposure posture:** `GET /metrics` is wrapped in the existing
  `auth.IPAllowlist` middleware — `ALLOWED_IPS` gates scrapers when the gateway
  is bound off-loopback; passthrough when `ALLOWED_IPS` is unset (localhost /
  default just works). NOT fully-exempt like `/health`, because `/metrics` leaks
  usage shape.
- **Metric namespace:** `gw_` prefix (matches the de-brand: `GW_LOG`,
  `GW_INSTALL_DIR`, …).

## Non-goals

- No change to existing `/health*` JSON shapes or the access-log line.
- No separate metrics port (same listener, `/metrics` path). A `METRICS_ADDR`
  split can come later if needed.
- No bearer-token gate on `/metrics` (the API surfaces' `AUTH_TOKEN` path is not
  reused here — allowlist only).

## Architecture

Three sources, one registry, one handler:

1. **Request metrics — a middleware** (`internal/server/middleware.go`, sibling
   to `accessLog`). Wraps every route; status + duration are already computed the
   way `accessLog` does (`middleware.NewWrapResponseWriter` + `time.Since`).
   Emits:
   - `gw_http_requests_total{method,route,status}` — counter.
   - `gw_http_request_duration_seconds{method,route,status}` — histogram
     (default buckets, or a latency-tuned set).
   - `gw_http_in_flight_requests` — gauge (Inc on entry, Dec on defer).
   - **Cardinality guard:** `route` label is chi's `RoutePattern()`
     (`/v1/messages`, `/api/chat`, …), NEVER the raw `r.URL.Path`; unmatched
     routes collapse to `route="other"`.
   - The `/metrics` scrape itself is excluded from these metrics AND from the
     `accessLog` INFO line (skip when `RoutePattern()=="/metrics"`) so
     high-frequency scrapes don't spam logs or self-measure.

2. **Pool + session gauges — a pull `prometheus.Collector`** (new
   `internal/metrics` package). No background goroutine: `Collect` calls
   `PoolHealth.HealthSummary()` and `Registry.Stats()` at scrape time behind the
   existing consumer-defined interfaces (the same seams `/health/pool` and
   `/admin` already use — no new coupling). Emits:
   - `gw_pool_size`, `gw_pool_alive`, `gw_pool_busy` — gauges.
   - `gw_pool_healthy`, `gw_pool_spawn_failing` — gauges (0/1).
   - `gw_pool_last_spawn_error_timestamp_seconds` — gauge (0 if none).
   - `gw_pool_last_progress_timestamp_seconds` — gauge (from
     `LastProgressAt()`; enables a `time()-metric > 30s` stall alert mirroring
     the health "degraded" rule).
   - `gw_sessions_active` — gauge (`Registry.Stats().Active`).

3. **Event counters — `atomic.Uint64` at today's log-only sites** (phase 4b).
   These events are currently `slog`-only; add a monotonic counter at each site,
   exported via the registry:
   - `gw_pool_slot_respawns_total` — `pool.respawnSlot`.
   - `gw_acp_ping_escalations_total` — `acp.pingTick` (`acp.ping.escalated_to_close`).
   - `gw_acp_ping_suspend_skips_total` — `acp.pingTick` (`acp.ping.skipped_after_resume`).
   - `gw_sessions_reaped_total` — `session.reapOnce`.
   - `gw_sessions_recycled_total` — populated by **Track 2** (context recycle);
     the counter is declared here, incremented there.

Wiring: `main.go` builds a `*prometheus.Registry`, registers the collector +
counters + the `promhttp` Go/process collectors, and passes a `MetricsHandler`
into `server.Config`. `server.go` mounts `GET /metrics` on the outer router
wrapped in `auth.IPAllowlist(...)`, inside `RequestID`/`Recoverer` but with the
metrics-middleware + accessLog skips noted above.

## Phasing

- **4a (this plan's core):** `internal/metrics` package, the request middleware,
  the pool/session pull-collector, the `/metrics` route + allowlist wiring, and
  `promhttp` free metrics. No changes to acp/pool/session internals beyond
  reading existing accessors.
- **4b (follow-on, small):** the five event counters at their log sites. Split
  out so 4a lands low-risk; 4b touches `pool`/`acp`/`session` to add increments.

## Testability

- Request middleware: drive a handler through it via `httptest`; assert the
  counter and histogram observed with the right `{method,route,status}` labels
  (use a test registry, `testutil.ToFloat64`).
- Pull collector: feed a fake `HealthSummary`/`Stats` source; gather the
  registry and assert the gauge values (`prometheus/testutil.GatherAndCompare`).
- Endpoint: `httptest` `GET /metrics` → 200, `text/plain; version=0.0.4`, body
  contains `gw_pool_alive`, `gw_http_requests_total`, `go_goroutines`.
- Allowlist: `GET /metrics` from a non-allowlisted IP with `ALLOWED_IPS` set →
  403; passthrough when unset.

## Tests (TDD)

1. Request middleware increments `gw_http_requests_total` and observes the
   duration histogram with `route=<RoutePattern>` and the real status.
2. `route="other"` collapse for an unmatched path (cardinality guard).
3. `/metrics` and its scrape are excluded from request metrics + access log.
4. Pull collector reports pool gauges from a fake HealthSummary
   (alive/busy/healthy/spawn_failing) and `gw_sessions_active` from fake Stats.
5. `GET /metrics` returns 200 + Prometheus text containing `gw_*` and the free
   `go_*`/`process_*` families.
6. Allowlist gate: 403 for a disallowed IP when `ALLOWED_IPS` set; 200 when unset.
7. (4b) each event counter increments exactly once per event at its site.

## Files touched (anticipated)

- `go.mod` / `go.sum` — add `prometheus/client_golang`.
- `internal/metrics/` — new package: registry builder, pull `Collector`,
  request middleware, counters.
- `internal/server/server.go` — mount `GET /metrics` behind `auth.IPAllowlist`;
  add the metrics middleware to the global chain; accessLog `/metrics` skip.
- `internal/server/middleware.go` — `/metrics` skip in `accessLog`.
- `cmd/otto-gateway/main.go` — build the registry, wire sources, pass handler.
- (4b) `internal/pool/pool.go`, `internal/acp/client.go`,
  `internal/session/reaper.go` — `atomic.Uint64` increments at the log sites.

## Verification

- `go build ./...`, `go vet`, gofumpt-clean; `GOOS=linux/windows` cross-compile
  still cgo-free (`CGO_ENABLED=0 go build ./cmd/otto-gateway` succeeds).
- `go test ./internal/metrics/... ./internal/server/...` green.
- `golangci-lint` on `internal/metrics/... internal/server/...`.
- Manual: `curl http://127.0.0.1:18080/metrics` shows `gw_*` families; point a
  Prometheus scrape at it and confirm series land in the TSDB.
