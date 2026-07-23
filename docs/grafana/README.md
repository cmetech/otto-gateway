# Grafana dashboards

## Loop24 Co-worker

`otto-gateway-dashboard.json` (dashboard title **Loop24 Co-worker**, uid
`gw-dev-obs`) is the fleet usage and gateway-health dashboard for metrics
exposed at `/metrics` and forwarded by the tray.

The dashboard is ordered by operational relevance: current fleet impact first,
then user adoption and failures, followed by capacity, cost, compression,
runtime diagnostics, and inventory.

## Import

1. In Grafana, open **Dashboards → New → Import**.
2. Upload `otto-gateway-dashboard.json` or paste its contents.
3. Select the Prometheus data source, such as
   `grafanacloud-<org>-prom`.
4. Select **Import**.

## Template variables

Variables appear in this order:

`Data source` · `Gateway ID` · `Surface` · `Outcome` · `Streaming` ·
`Session Mode` · `Skill` · `Client` · `Model`

Every metric panel applies the stable external
`instance=<gateway_id>` selector through **Gateway ID**, so the complete
dashboard can be narrowed to one or more gateways.

The remaining filters apply only to metrics that carry the corresponding
dimension:

- Surface, Outcome, Streaming, and Session Mode filter final LLM application
  outcomes.
- Surface, Skill, and Client filter attributed LLM-request panels.
- Model filters model-request panels.
- Pool, process, compression, and Kiro metrics do not carry these request
  dimensions and therefore only use Gateway ID.

This is intentional; adding unsupported label filters would turn valid panels
into empty results.

## Information hierarchy

| Row | Purpose |
|-----|---------|
| Fleet Overview | Active gateways, request volume, final application success, affected gateways, sessions, pool-wait p95, credits, and unhealthy or stalled gateways |
| User Activity and Adoption | Active usage, requests per active gateway, surface/skill/client/model adoption, streaming and session behavior, top users, and attribution completeness |
| User Experience and Failures | Final outcomes first, then affected gateways and HTTP-level latency/status/route diagnostics |
| Gateway Capacity and Pool Health | Utilization, acquisition pressure, progress age, session lifecycle, recovery events, and the per-gateway health matrix |
| Kiro Cost and Context | Credits, turns, cost ratios, turn duration, context pressure, and MCP initialization health |
| Compression Effectiveness | Eligibility, successful runs, estimated savings, budget misses, ratios, and recovered panics |
| Runtime Resources | Gateway and worker CPU/RSS, file-descriptor utilization, uptime, restarts, and optional goroutines |
| Fleet Inventory | Reporting installations and their version/build identity |

## Signal interpretation

- `gw_llm_request_outcomes_total` is the application-success signal. It records
  the final result known by the surface adapter, including idle timeouts,
  upstream failures, and client cancellation after streaming HTTP 200 headers
  have already been committed. HTTP status panels remain useful protocol
  diagnostics but are not a substitute for final application outcomes.
- Pool-acquisition latency ends when a worker slot is acquired or the acquire
  terminates. It excludes worker respawn and `session/new`, keeping it focused
  on queue pressure.
- Compression token values use the gateway's UTF-8-bytes/4 estimate. They are
  not model-tokenizer output or billing tokens.
- Worker process CPU and RSS sampling is unavailable on macOS.
- The Goroutines panel populates only when `GW_METRICS_SERIES_PREFIXES`
  includes `go_`; the default allowlist is `gw_,process_`.
- Usage panels populate as traffic flows. A fresh gateway with no requests may
  show no data in rate and outcome panels.

All forwarded series use `job="otto-gateway"` and the tray-added
`instance=<gateway_id>` external label.

## Regenerating and testing

The Python generator is the source of truth. Do not hand-edit the committed
JSON.

```bash
python3 scripts/gen_grafana_dashboard.py
python3 -m unittest scripts.test_gen_grafana_dashboard
```

The test verifies import safety, row and variable hierarchy, required panels,
Gateway ID slicing, complete custom-metric coverage, and exact parity between
the generator and committed JSON.
