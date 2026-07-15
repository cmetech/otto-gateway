#!/usr/bin/env python3
"""Generate the OTTO Gateway developer observability Grafana dashboard JSON."""
import json
import os

DS = "${DS_PROM}"
SEL = '{job="otto-gateway", instance=~"$gateway"}'          # base fleet selector
LLM = '{job="otto-gateway", instance=~"$gateway", surface=~"$surface", skill=~"$skill", client=~"$client"}'

_id = [0]
def nid():
    _id[0] += 1
    return _id[0]

def ds():
    return {"type": "prometheus", "uid": DS}

def tgt(expr, legend=None, refid="A", instant=False):
    t = {"datasource": ds(), "editorMode": "code", "expr": expr, "range": not instant,
         "instant": instant, "refId": refid}
    if legend is not None:
        t["legendFormat"] = legend
    return t

def targets(pairs, instant=False):
    out = []
    for i, (expr, legend) in enumerate(pairs):
        out.append(tgt(expr, legend, chr(ord("A") + i), instant))
    return out

def gridpos(x, y, w, h):
    return {"h": h, "w": w, "x": x, "y": y}

def base_fieldconfig(unit=None, decimals=None, thresholds=None, color_mode=None,
                     min_=None, max_=None, overrides=None):
    defaults = {"color": {"mode": color_mode or "palette-classic"}}
    if unit is not None:
        defaults["unit"] = unit
    if decimals is not None:
        defaults["decimals"] = decimals
    if min_ is not None:
        defaults["min"] = min_
    if max_ is not None:
        defaults["max"] = max_
    if thresholds is not None:
        defaults["thresholds"] = {"mode": "absolute", "steps": thresholds}
    else:
        defaults["thresholds"] = {"mode": "absolute", "steps": [{"color": "green", "value": None}]}
    return {"defaults": defaults, "overrides": overrides or []}

def ts_custom():
    return {"drawStyle": "line", "lineInterpolation": "smooth", "lineWidth": 2,
            "fillOpacity": 12, "gradientMode": "opacity", "showPoints": "never",
            "pointSize": 5, "stacking": {"mode": "none", "group": "A"},
            "axisPlacement": "auto", "axisLabel": "", "spanNulls": False}

def panel(ptype, title, x, y, w, h, tpairs, unit=None, desc="", options=None,
          fieldconfig=None, instant=False, transformations=None):
    p = {
        "id": nid(), "type": ptype, "title": title, "datasource": ds(),
        "gridPos": gridpos(x, y, w, h),
        "targets": targets(tpairs, instant=instant),
        "fieldConfig": fieldconfig or base_fieldconfig(unit),
        "options": options or {},
    }
    if desc:
        p["description"] = desc
    if transformations:
        p["transformations"] = transformations
    return p

def row(title, y, collapsed=False, panels=None):
    r = {"id": nid(), "type": "row", "title": title, "collapsed": collapsed,
         "gridPos": gridpos(0, y, 24, 1), "panels": panels or []}
    return r

# ---- reusable option blocks -------------------------------------------------
def stat_opts(graph=True, unit_txt=None):
    return {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
            "orientation": "auto", "textMode": "value_and_name", "colorMode": "value",
            "graphMode": "area" if graph else "none", "justifyMode": "auto"}

def ts_opts():
    return {"legend": {"displayMode": "table", "placement": "bottom", "showLegend": True,
                       "calcs": ["mean", "lastNotNull", "max"]},
            "tooltip": {"mode": "multi", "sort": "desc"}}

def ts_field(unit=None, stacking="none", thresholds=None, color_mode="palette-classic",
             overrides=None, decimals=None, fillOpacity=12, min_=None, max_=None):
    fc = base_fieldconfig(unit, thresholds=thresholds, color_mode=color_mode,
                          overrides=overrides, decimals=decimals, min_=min_, max_=max_)
    cust = ts_custom()
    cust["stacking"] = {"mode": stacking, "group": "A"}
    cust["fillOpacity"] = fillOpacity
    fc["defaults"]["custom"] = cust
    return fc

def pie_opts():
    return {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
            "pieType": "donut", "tooltip": {"mode": "single", "sort": "desc"},
            "legend": {"displayMode": "table", "placement": "right", "showLegend": True,
                       "values": ["value", "percent"]}, "displayLabels": ["percent"]}

# fixed categorical colors for the 3 API surfaces (identity, not rank)
SURFACE_OVERRIDES = [
    {"matcher": {"id": "byName", "options": "anthropic"},
     "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": "purple"}}]},
    {"matcher": {"id": "byName", "options": "openai"},
     "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": "green"}}]},
    {"matcher": {"id": "byName", "options": "ollama"},
     "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": "blue"}}]},
]

TH_ERR = [{"color": "green", "value": None}, {"color": "yellow", "value": 1}, {"color": "red", "value": 5}]
TH_CTX = [{"color": "green", "value": None}, {"color": "yellow", "value": 70}, {"color": "red", "value": 90}]
TH_HEALTH_BAD = [{"color": "green", "value": None}, {"color": "red", "value": 1}]  # 0 good, >=1 bad

panels = []
y = 0

# =========================================================================
# ROW 1 — Fleet overview
# =========================================================================
panels.append(row("Fleet Overview", y)); y += 1
panels.append(panel("stat", "Gateways Reporting", 0, y, 3, 4,
    [(f"count(group by(instance)(gw_build_info{SEL}))", "")],
    unit="short", options=stat_opts(graph=False),
    desc="Distinct gateways (≈ user machines) currently sending metrics."))
panels.append(panel("stat", "LLM Requests (range)", 3, y, 3, 4,
    [(f"sum(increase(gw_llm_requests_total{LLM}[$__range]))", "")],
    unit="short", options=stat_opts(),
    desc="Total chat requests across all surfaces in the selected time range."))
panels.append(panel("stat", "Active Sessions", 6, y, 3, 4,
    [(f"sum(gw_sessions_active{SEL})", "")], unit="short", options=stat_opts(),
    desc="Stateful (X-Session-Id) sessions currently registered fleet-wide."))
panels.append(panel("stat", "In-flight Requests", 9, y, 3, 4,
    [(f"sum(gw_http_in_flight_requests{SEL})", "")], unit="short", options=stat_opts()))
panels.append(panel("stat", "Error Rate", 12, y, 3, 4,
    [(f"100 * sum(rate(gw_http_requests_total{{job=\"otto-gateway\", instance=~\"$gateway\", status=~\"5..\"}}[$__rate_interval])) / clamp_min(sum(rate(gw_http_requests_total{SEL}[$__rate_interval])), 0.001)", "")],
    unit="percent",
    fieldconfig=base_fieldconfig("percent", decimals=2, thresholds=TH_ERR, color_mode="thresholds"),
    options=stat_opts(), desc="5xx responses as a share of all HTTP traffic (5m)."))
panels.append(panel("stat", "Kiro Credits (range)", 15, y, 3, 4,
    [(f"sum(increase(gw_kiro_credits_total{SEL}[$__range]))", "")], unit="short", options=stat_opts(),
    desc="kiro credits consumed across the fleet in the range."))
panels.append(panel("stat", "Kiro Turns (range)", 18, y, 3, 4,
    [(f"sum(increase(gw_kiro_turns_total{SEL}[$__range]))", "")], unit="short", options=stat_opts()))
panels.append(panel("stat", "Unhealthy Gateways", 21, y, 3, 4,
    [(f"count(gw_pool_healthy{SEL} == 0) or vector(0)", "")], unit="short",
    fieldconfig=base_fieldconfig("short", thresholds=TH_HEALTH_BAD, color_mode="thresholds"),
    options=stat_opts(graph=False), desc="Gateways whose pool cannot currently serve."))
y += 4

# =========================================================================
# ROW 2 — Usage: who & how
# =========================================================================
panels.append(row("Usage — Who & How Tools Are Used", y)); y += 1
panels.append(panel("timeseries", "LLM Requests by Surface", 0, y, 12, 8,
    [(f"sum by(surface)(rate(gw_llm_requests_total{LLM}[$__rate_interval]))", "{{surface}}")],
    fieldconfig=ts_field("reqps", stacking="normal", overrides=SURFACE_OVERRIDES, fillOpacity=25),
    options=ts_opts(), desc="Request rate split by API surface (anthropic/openai/ollama)."))
panels.append(panel("timeseries", "LLM Requests by Skill", 12, y, 12, 8,
    [(f"sum by(skill)(rate(gw_llm_requests_total{LLM}[$__rate_interval]))", "{{skill}}")],
    fieldconfig=ts_field("reqps", stacking="normal", fillOpacity=25), options=ts_opts(),
    desc="Which invoking skill (X-GW-Skill / X-Flow-Name) is driving traffic."))
y += 8
panels.append(panel("piechart", "Requests by Client", 0, y, 6, 8,
    [(f"sum by(client)(increase(gw_llm_requests_total{LLM}[$__range]))", "{{client}}")],
    unit="short", options=pie_opts(), desc="X-GW-Client attribution over the range."))
panels.append(panel("piechart", "Requests by Model", 6, y, 6, 8,
    [(f"sum by(model)(increase(gw_model_requests_total{{job=\"otto-gateway\", instance=~\"$gateway\", model=~\"$model\"}}[$__range]))", "{{model}}")],
    unit="short", options=pie_opts(), desc="Requested model (canonical Model; empty/auto→auto)."))
panels.append(panel("barchart", "Requests by Gateway (across users)", 12, y, 12, 8,
    [(f"sum by(instance)(increase(gw_llm_requests_total{LLM}[$__range]))", "{{instance}}")],
    unit="short", instant=True,
    fieldconfig=base_fieldconfig("short", color_mode="palette-classic"),
    options={"orientation": "horizontal", "showValue": "auto",
             "legend": {"showLegend": False, "displayMode": "list", "placement": "bottom"},
             "xTickLabelRotation": 0, "stacking": "none"},
    desc="Total LLM requests per gateway (≈ per user) in the range."))
y += 8
panels.append(panel("table", "Top Skills", 0, y, 12, 8,
    [(f"topk(20, sum by(skill)(increase(gw_llm_requests_total{LLM}[$__range])))", "")],
    unit="short", instant=True,
    options={"showHeader": True, "sortBy": [{"displayName": "Value", "desc": True}]},
    transformations=[{"id": "organize", "options": {
        "excludeByName": {"Time": True},
        "renameByName": {"skill": "Skill", "Value": "Requests"}}}],
    desc="Busiest skills in the range."))
panels.append(panel("table", "Top Clients × Surface", 12, y, 12, 8,
    [(f"topk(20, sum by(client, surface)(increase(gw_llm_requests_total{LLM}[$__range])))", "")],
    unit="short", instant=True,
    options={"showHeader": True, "sortBy": [{"displayName": "Requests", "desc": True}]},
    transformations=[{"id": "organize", "options": {
        "excludeByName": {"Time": True},
        "renameByName": {"client": "Client", "surface": "Surface", "Value": "Requests"}}}]))
y += 8

# =========================================================================
# ROW 3 — HTTP traffic & latency
# =========================================================================
panels.append(row("HTTP Traffic & Latency", y)); y += 1
panels.append(panel("timeseries", "Request Rate by Route", 0, y, 12, 8,
    [(f"sum by(route)(rate(gw_http_requests_total{SEL}[$__rate_interval]))", "{{route}}")],
    fieldconfig=ts_field("reqps", stacking="normal", fillOpacity=25), options=ts_opts()))
panels.append(panel("timeseries", "Request Rate by Status", 12, y, 12, 8,
    [(f"sum by(status)(rate(gw_http_requests_total{SEL}[$__rate_interval]))", "{{status}}")],
    fieldconfig=ts_field("reqps", stacking="normal", fillOpacity=25), options=ts_opts(),
    desc="HTTP status distribution over time."))
y += 8
panels.append(panel("timeseries", "Latency p50 / p95 / p99", 0, y, 12, 8,
    [(f"histogram_quantile(0.50, sum by(le)(rate(gw_http_request_duration_seconds_bucket{SEL}[$__rate_interval])))", "p50"),
     (f"histogram_quantile(0.95, sum by(le)(rate(gw_http_request_duration_seconds_bucket{SEL}[$__rate_interval])))", "p95"),
     (f"histogram_quantile(0.99, sum by(le)(rate(gw_http_request_duration_seconds_bucket{SEL}[$__rate_interval])))", "p99")],
    fieldconfig=ts_field("s"), options=ts_opts(), desc="Request duration quantiles."))
panels.append(panel("heatmap", "Latency Distribution (heatmap)", 12, y, 12, 8,
    [(f"sum by(le)(rate(gw_http_request_duration_seconds_bucket{SEL}[$__rate_interval]))", "{{le}}")],
    unit="s",
    options={"calculate": False, "cellGap": 1, "color": {"scheme": "Turbo", "mode": "scheme", "steps": 64},
             "yAxis": {"unit": "s"}, "tooltip": {"show": True, "yHistogram": True},
             "legend": {"show": True}},
    desc="Full request-latency histogram over time."))
y += 8

# =========================================================================
# ROW 4 — Kiro engine usage
# =========================================================================
panels.append(row("Kiro Engine Usage", y)); y += 1
panels.append(panel("timeseries", "Credits & Turns Rate", 0, y, 12, 8,
    [(f"sum(rate(gw_kiro_credits_total{SEL}[$__rate_interval]))", "credits/s"),
     (f"sum(rate(gw_kiro_turns_total{SEL}[$__rate_interval]))", "turns/s")],
    fieldconfig=ts_field("short"), options=ts_opts()))
panels.append(panel("timeseries", "Turn Duration p50 / p95", 12, y, 12, 8,
    [(f"histogram_quantile(0.50, sum by(le)(rate(gw_kiro_turn_duration_seconds_bucket{SEL}[$__rate_interval])))", "p50"),
     (f"histogram_quantile(0.95, sum by(le)(rate(gw_kiro_turn_duration_seconds_bucket{SEL}[$__rate_interval])))", "p95")],
    fieldconfig=ts_field("s"), options=ts_opts(), desc="kiro turn wall-time quantiles."))
y += 8
panels.append(panel("gauge", "Context Window Usage p95", 0, y, 6, 8,
    [(f"histogram_quantile(0.95, sum by(le)(rate(gw_kiro_context_usage_percent_bucket{SEL}[$__rate_interval])))", "p95 ctx %")],
    unit="percent",
    fieldconfig=base_fieldconfig("percent", min_=0, max_=100, thresholds=TH_CTX, color_mode="thresholds"),
    options={"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
             "showThresholdLabels": False, "showThresholdMarkers": True},
    desc="End-of-turn context utilization (higher ⇒ closer to a recycle)."))
panels.append(panel("timeseries", "Context Usage p50 / p95", 6, y, 12, 8,
    [(f"histogram_quantile(0.50, sum by(le)(rate(gw_kiro_context_usage_percent_bucket{SEL}[$__rate_interval])))", "p50"),
     (f"histogram_quantile(0.95, sum by(le)(rate(gw_kiro_context_usage_percent_bucket{SEL}[$__rate_interval])))", "p95")],
    fieldconfig=ts_field("percent", thresholds=TH_CTX, min_=0, max_=100), options=ts_opts()))
panels.append(panel("table", "MCP Server Init Outcomes", 18, y, 6, 8,
    [(f"sum by(server, result)(increase(gw_kiro_mcp_server_init_total{SEL}[$__range]))", "")],
    unit="short", instant=True,
    options={"showHeader": True, "sortBy": [{"displayName": "Count", "desc": True}]},
    transformations=[{"id": "organize", "options": {
        "excludeByName": {"Time": True},
        "renameByName": {"server": "Server", "result": "Result", "Value": "Count"}}}],
    desc="ok/fail init counts per MCP server."))
y += 8

# =========================================================================
# ROW 5 — Pool & session health
# =========================================================================
panels.append(row("Pool & Session Health", y)); y += 1
panels.append(panel("timeseries", "Pool Slots (alive / busy) by Gateway", 0, y, 12, 8,
    [(f"sum by(instance)(gw_pool_alive{SEL})", "alive {{instance}}"),
     (f"sum by(instance)(gw_pool_busy{SEL})", "busy {{instance}}")],
    fieldconfig=ts_field("short"), options=ts_opts(),
    desc="Warm slots alive vs checked-out, per gateway."))
panels.append(panel("timeseries", "Active Sessions by Gateway", 12, y, 12, 8,
    [(f"sum by(instance)(gw_sessions_active{SEL})", "{{instance}}")],
    fieldconfig=ts_field("short"), options=ts_opts()))
y += 8
panels.append(panel("timeseries", "Session Lifecycle Rate", 0, y, 12, 8,
    [(f"sum(rate(gw_sessions_created_total{SEL}[$__rate_interval]))", "created/s"),
     (f"sum(rate(gw_sessions_recycled_total{SEL}[$__rate_interval]))", "recycled/s"),
     (f"sum(rate(gw_sessions_reaped_total{SEL}[$__rate_interval]))", "reaped/s")],
    fieldconfig=ts_field("short"), options=ts_opts(),
    desc="Created / recycled (context threshold) / reaped (idle)."))
panels.append(panel("timeseries", "Slot Respawns & Ping Escalations", 12, y, 12, 8,
    [(f"sum(rate(gw_pool_slot_respawns_total{SEL}[$__rate_interval]))", "respawns/s"),
     (f"sum(rate(gw_acp_ping_escalations_total{SEL}[$__rate_interval]))", "ping escalations/s"),
     (f"sum(rate(gw_acp_ping_suspend_skips_total{SEL}[$__rate_interval]))", "suspend skips/s")],
    fieldconfig=ts_field("short"), options=ts_opts(),
    desc="Pool self-healing activity — spikes flag flapping workers."))
y += 8
panels.append(panel("table", "Gateway Health Matrix", 0, y, 24, 8,
    [(f"gw_pool_healthy{SEL}", "healthy"),
     (f"gw_pool_spawn_failing{SEL}", "spawn_failing"),
     (f"gw_pool_alive{SEL}", "alive"),
     (f"gw_pool_busy{SEL}", "busy"),
     (f"gw_pool_size{SEL}", "size"),
     (f"gw_sessions_active{SEL}", "sessions")],
    unit="short", instant=True,
    options={"showHeader": True, "sortBy": [{"displayName": "healthy", "desc": False}]},
    transformations=[
        {"id": "merge", "options": {}},
        {"id": "organize", "options": {
            "excludeByName": {"Time": True, "job": True, "__name__": True, "commit": True, "version": True, "gateway_id": True},
            "renameByName": {"instance": "Gateway", "Value #A": "Healthy", "Value #B": "Spawn Failing",
                             "Value #C": "Alive", "Value #D": "Busy", "Value #E": "Size", "Value #F": "Sessions"}}}],
    fieldconfig={"defaults": {"custom": {"align": "auto", "cellOptions": {"type": "auto"}}},
                 "overrides": [
        {"matcher": {"id": "byName", "options": "Healthy"}, "properties": [
            {"id": "custom.cellOptions", "value": {"type": "color-background"}},
            {"id": "thresholds", "value": {"mode": "absolute", "steps": [{"color": "red", "value": None}, {"color": "green", "value": 1}]}},
            {"id": "mappings", "value": [{"type": "value", "options": {"0": {"text": "DOWN"}, "1": {"text": "OK"}}}]}]},
        {"matcher": {"id": "byName", "options": "Spawn Failing"}, "properties": [
            {"id": "custom.cellOptions", "value": {"type": "color-background"}},
            {"id": "thresholds", "value": {"mode": "absolute", "steps": [{"color": "green", "value": None}, {"color": "red", "value": 1}]}},
            {"id": "mappings", "value": [{"type": "value", "options": {"0": {"text": "no"}, "1": {"text": "YES"}}}]}]}]},
    desc="One row per gateway — current pool posture at a glance."))
y += 8

# =========================================================================
# ROW 6 — Resource usage (process + workers)
# =========================================================================
panels.append(row("Resource Usage — Gateway Process & Workers", y)); y += 1
panels.append(panel("timeseries", "Gateway Process CPU %", 0, y, 12, 8,
    [(f"rate(process_cpu_seconds_total{SEL}[$__rate_interval]) * 100", "{{instance}}")],
    fieldconfig=ts_field("percent"), options=ts_opts(),
    desc="Whole-gateway-process CPU (does not include worker subprocesses)."))
panels.append(panel("timeseries", "Gateway Process RSS", 12, y, 12, 8,
    [(f"process_resident_memory_bytes{SEL}", "{{instance}}")],
    fieldconfig=ts_field("bytes"), options=ts_opts()))
y += 8
panels.append(panel("timeseries", "Per-Worker CPU %", 0, y, 12, 8,
    [(f"rate(gw_worker_cpu_seconds_total{SEL}[$__rate_interval]) * 100", "{{instance}} / {{slot}}")],
    fieldconfig=ts_field("percent"), options=ts_opts(),
    desc="Each kiro-cli worker subprocess, by gateway and slot."))
panels.append(panel("timeseries", "Per-Worker RSS", 12, y, 12, 8,
    [(f"gw_worker_resident_memory_bytes{SEL}", "{{instance}} / {{slot}}")],
    fieldconfig=ts_field("bytes"), options=ts_opts()))
y += 8
panels.append(panel("timeseries", "Open File Descriptors", 0, y, 12, 8,
    [(f"process_open_fds{SEL}", "{{instance}}")],
    fieldconfig=ts_field("short"), options=ts_opts()))
panels.append(panel("timeseries", "Goroutines (if go_* shipped)", 12, y, 12, 8,
    [(f"go_goroutines{SEL}", "{{instance}}")],
    fieldconfig=ts_field("short"), options=ts_opts(),
    desc="Populated only if GW_METRICS_SERIES_PREFIXES includes go_ (default excludes it)."))
y += 8

# =========================================================================
# ROW 7 — Fleet inventory
# =========================================================================
panels.append(row("Fleet Inventory", y)); y += 1
panels.append(panel("table", "Gateways — Version & Build", 0, y, 24, 8,
    [(f"gw_build_info{SEL}", "")], unit="short", instant=True,
    options={"showHeader": True, "sortBy": [{"displayName": "Gateway", "desc": False}]},
    transformations=[{"id": "organize", "options": {
        "excludeByName": {"Time": True, "Value": True, "job": True, "__name__": True, "instance": False},
        "indexByName": {"instance": 0, "gateway_id": 1, "version": 2, "commit": 3},
        "renameByName": {"instance": "Gateway", "gateway_id": "Gateway ID", "version": "Version", "commit": "Commit"}}}],
    desc="Build identity per reporting gateway."))
y += 8

# =========================================================================
# Templating variables
# =========================================================================
def q_var(name, label, metric_label_expr, multi=True):
    return {
        "name": name, "label": label, "type": "query", "datasource": ds(),
        "query": {"query": metric_label_expr, "refId": name}, "definition": metric_label_expr,
        "includeAll": True, "multi": multi, "allValue": None, "refresh": 2,
        "sort": 1, "current": {"selected": True, "text": ["All"], "value": ["$__all"]},
        "options": [], "regex": "",
    }

templating = {"list": [
    {"name": "DS_PROM", "label": "Data source", "type": "datasource", "query": "prometheus",
     "current": {}, "hide": 0, "refresh": 1, "regex": "", "includeAll": False, "multi": False},
    q_var("gateway", "Gateway", "label_values(gw_build_info, instance)"),
    q_var("surface", "Surface", "label_values(gw_llm_requests_total, surface)"),
    q_var("skill", "Skill", "label_values(gw_llm_requests_total, skill)"),
    q_var("client", "Client", "label_values(gw_llm_requests_total, client)"),
    q_var("model", "Model", "label_values(gw_model_requests_total, model)"),
]}

dashboard = {
    "annotations": {"list": [{"builtIn": 1, "datasource": {"type": "grafana", "uid": "-- Grafana --"},
                              "enable": True, "hide": True, "iconColor": "rgba(0, 211, 255, 1)",
                              "name": "Annotations & Alerts", "type": "dashboard"}]},
    "editable": True, "fiscalYearStartMonth": 0, "graphTooltip": 1, "links": [],
    "liveNow": False, "panels": panels, "refresh": "30s", "schemaVersion": 39,
    "style": "dark", "tags": ["otto-gateway", "observability"],
    "templating": templating,
    "time": {"from": "now-6h", "to": "now"}, "timepicker": {},
    "timezone": "browser",
    "title": "Loop24 Co-worker",
    "uid": "gw-dev-obs", "version": 1, "weekStart": "",
}

# Post-fix: Prometheus-sourced heatmap panels need format=heatmap on their target.
for p in panels:
    if p.get("type") == "heatmap":
        for t in p["targets"]:
            t["format"] = "heatmap"

with open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "docs", "grafana", "otto-gateway-dashboard.json"), "w") as f:
    json.dump(dashboard, f, indent=2)
print(f"panels: {len(panels)}  last id: {_id[0]}")
