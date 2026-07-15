---
quick_id: 260715-q5m
slug: add-grafana-cloud-metrics-remote-write-a
date: 2026-07-15
status: complete
---

# Summary: Tray → Grafana Cloud metrics remote-write agent

## What shipped

An always-on background agent in otto-tray that scrapes the local gateway's
`/metrics` and remote-writes selected series to Grafana Cloud, gated by an
Advanced-menu checkbox, failing gracefully in every direction.

**New files (`darwin || windows`):**
- `remotewrite_config.go` — `remoteWriteConfig` + `loadRemoteWriteConfig(gwHome)`
  reads `GW_METRICS_REMOTE_WRITE_{URL,USER,TOKEN,INTERVAL_SEC,ENABLED}` +
  `GW_METRICS_SERIES_PREFIXES` via a `dotenvLookup` closure (overrides.env → .env
  → os.Getenv, mirroring `lookupHTTPAddr`). Interval default 30s / floor 5s;
  prefixes default `gw_,process_` (or `*` = all); `ready()` = URL+User+Token set.
- `remotewrite.go` — `remoteWriter` + `runRemoteWriter(ctx, rw, sleep)` loop
  (re-reads cfg each cycle → sleep interval → `tickOnce`). `tickOnce` is
  `recover()`-guarded, bails when disabled/unconfigured, then scrape→convert→push.
  `scrapeAndConvert` GETs `/metrics`, parses with `expfmt.NewTextParser(
  model.UTF8Validation)`, filters by prefix, expands counter/gauge/untyped/
  summary/histogram (incl. dedup'd `+Inf` bucket) into `promwrite.TimeSeries`
  with external `job=otto-gateway` + `instance=<gateway_id from gw_build_info |
  hostname>`. `push` uses `promwrite` (snappy+protobuf) + `Authorization: Basic
  base64(user:token)`. Errors are debug-logged with the token scrubbed.
- `remotewrite_test.go` — 11 tests via httptest fakes: filter/expand/label,
  gateway-down + non-200 → error, end-to-end push asserts basic-auth + snappy
  headers, disabled/unconfigured no-op, push-500 swallowed, panic recovered,
  toggle precedence (tray.json overrides env), config parsers, token scrub.

**Modified:**
- `config.go` — `TrayConfig.MetricsRemoteWriteEnabled *bool` (`omitempty`).
- `tray.go` — `miMetricsRW` checkbox under Advanced + `metricsRWEnabled atomic.Bool`;
  `onReady` resolves initial state (tray.json override else env) and starts
  `runRemoteWriter` on the shared poller ctx; `toggleMetricsRemoteWrite` flips the
  atomic live and persists the concrete bool to tray.json;
  `resolveMetricsRWEnabled` implements the precedence.
- `scripts/.env.example` — documented the new vars (placeholder token only).

**Deps added (all pure-Go / cgo-free):** `castai/promwrite@v0.3.0`,
`gogo/protobuf`, `golang/snappy`. (v0.4.0+ drags in prometheus/prometheus —
deliberately pinned to v0.3.0.)

## Decisions honored
- RW encoder: promwrite@v0.3.0 (lean). Toggle: checkbox persists to tray.json,
  env is the default. Series scope: `gw_,process_` default.

## Failure handling (all verified by test)
Gateway down / non-200 → scrape error → skip tick; Grafana 4xx/5xx/network →
push error swallowed → drop batch, retry next interval; panic → `recover()`
keeps the tray alive; disabled/unconfigured → no scrape at all. Token is
env-only, never in tray.json, scrubbed from logs.

## Verification
- `go build ./...`, `go vet ./...`, `gofumpt`, `go-arch-lint` all clean.
- `go test ./...` green; `-race ./cmd/otto-tray/...` green.
- Cross-build: **Windows tray cgo-free** (`CGO_ENABLED=0`); darwin tray builds
  with cgo (energye/systray needs Cocoa — pre-existing, unchanged); gateway still
  cgo-free. New deps introduce no cgo.
- End-to-end pipeline (scrape → convert → push w/ basic-auth + snappy) proven by
  httptest. **Live Grafana-Cloud confirmation must happen on the Windows box**
  (the tray UI + real endpoint can't run headlessly here).

## Follow-ups / notes
- **Rotate the Grafana token** shared during design — it's in chat history.
- Headless-Linux gateways have no tray → would need gateway-side remote-write
  (separate future task; out of scope here).
- Not pushed/merged — committed on main per the quick-task flow.
