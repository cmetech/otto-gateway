---
quick_id: 260715-q5m
slug: add-grafana-cloud-metrics-remote-write-a
date: 2026-07-15
status: complete
---

# Quick Task: Tray → Grafana Cloud metrics remote-write agent

## Goal

The always-on otto-tray scrapes the local gateway's `/metrics` on an interval and
remote-writes to Grafana Cloud, controlled by an Advanced-menu enable/disable
toggle. Fails gracefully (gateway down, Grafana errors, panics) without ever
crashing the tray.

## Decisions (locked)

- **RW encoder**: `github.com/castai/promwrite@v0.3.0` (deps `gogo/protobuf` +
  `golang/snappy` only — cgo-free; v0.4.0+ drags in prometheus/prometheus, avoid).
- **Toggle model**: Advanced checkbox persists to `tray.json`
  (`TrayConfig.MetricsRemoteWriteEnabled *bool`, `omitempty`); nil ⇒ fall back to
  env `GW_METRICS_REMOTE_WRITE_ENABLED`. Live via `atomic.Bool` read each cycle.
- **Series scope**: default allowlist `gw_,process_` (skip `go_*`), overridable
  via `GW_METRICS_SERIES_PREFIXES`.

## Design

### New files (build-tagged `darwin || windows`, matching the tray package)

- `remotewrite_config.go` — `remoteWriteConfig{URL,User,Token,Interval,Prefixes,
  EnvEnabled}`; `loadRemoteWriteConfig(gwHome)` reads `GW_METRICS_REMOTE_WRITE_*`
  + `GW_METRICS_SERIES_PREFIXES` via a `dotenvLookup` closure (overrides.env →
  .env → os.Getenv, mirroring `lookupHTTPAddr`). `ready()` = URL+User+Token set.
  Interval default 30s, clamped ≥5s.
- `remotewrite.go` — `remoteWriter{scrapeURL,gwHome,enabled *atomic.Bool,httpc}`.
  `runRemoteWriter(ctx, rw, sleep)`: each cycle re-reads cfg, `sleep(interval)`,
  then `tickOnce`. `tickOnce`: `recover()` guard → bail if `!enabled` → bail if
  `!cfg.ready()` → `scrapeAndConvert` (GET /metrics, `expfmt.TextParser`, filter
  prefixes, expand counter/gauge/untyped/summary/histogram → `promwrite.TimeSeries`
  with `__name__` + metric labels + external `job="otto-gateway"` +
  `instance=<gateway_id from gw_build_info | hostname>`) → `push` (promwrite client
  with `Authorization: Basic base64(user:token)`). Every error path: `slog.Debug`
  (token scrubbed) + return; nothing propagates.
- `remotewrite_test.go` — httptest fake `/metrics` + fake Grafana push endpoint:
  parse+filter+convert (prefix allowlist, histogram expansion, external labels),
  graceful-fail on scrape error / non-200, push-fail is swallowed, panic-recover,
  disabled ⇒ no-op, toggle precedence (tray.json overrides env), token scrub.

### Modified files

- `config.go` — `TrayConfig` gains `MetricsRemoteWriteEnabled *bool
  \`json:"metrics_remote_write_enabled,omitempty"\``.
- `tray.go` — `trayState` gains `miMetricsRW *systray.MenuItem` +
  `metricsRWEnabled atomic.Bool`. In `onReady`: resolve initial enabled
  (tray.json override else env), add `miAdvanced.AddSubMenuItemCheckbox(...)`,
  start `go runRemoteWriter(ctx, newRemoteWriter(dashboardURL+"/metrics", gwHome,
  &metricsRWEnabled), sleepCtx)` (shares poller ctx → dies on exit). Wire
  `Click → toggleMetricsRemoteWrite` (flip atomic, Check/Uncheck, persist *bool to
  tray.json). `sleepCtx(ctx,d)` = context-aware timer.

### Security

- Token read from env only; **never** written to tray.json; **never** logged
  (scrub from every error string). Basic auth over HTTPS is Grafana's expected auth.

## Tasks
1. `go get github.com/castai/promwrite@v0.3.0`; `go mod tidy`.
2. `remotewrite_config.go` + `dotenvLookup` helper.
3. `remotewrite.go` (scrape → convert → push, graceful + recover + scrub).
4. `config.go` TrayConfig field.
5. `tray.go` menu checkbox + goroutine start + toggle handler.
6. `remotewrite_test.go`.
7. Gates: `go build`, cross-compile darwin+windows (CGO_ENABLED=0), `go vet`,
   `gofumpt`, `go test -race ./cmd/otto-tray/...`, `arch-lint` (tray is outside
   internal/ ⇒ unaffected, but run full to confirm no regressions).

## Verification
- Cross-compile darwin/amd64 + windows/amd64 cgo-free (tray package).
- Unit tests green incl. the httptest end-to-end (fake gateway → fake Grafana).
- Cannot run the tray UI on this box headlessly; live Grafana-Cloud confirmation
  happens on the Windows box — deliver a "confirm it's flowing" checklist.

## Non-goals
- Headless-Linux support (tray is darwin/windows only; gateway-side RW is a
  separate future task).
- Backpressure/queueing of failed batches (drop + retry next interval by design).
