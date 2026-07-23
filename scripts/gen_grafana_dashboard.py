#!/usr/bin/env python3
"""Generate the Loop24 Co-worker Grafana dashboard JSON."""

import json
from pathlib import Path


DS = "${DS_PROM}"
DASHBOARD_PATH = (
    Path(__file__).resolve().parents[1]
    / "docs"
    / "grafana"
    / "otto-gateway-dashboard.json"
)

BASE = 'job="otto-gateway", instance=~"$gateway_id"'
SEL = "{" + BASE + "}"
LLM = (
    "{"
    + BASE
    + ', surface=~"$surface", skill=~"$skill", client=~"$client"}'
)
OUTCOME = (
    "{"
    + BASE
    + ', surface=~"$surface", outcome=~"$outcome", stream=~"$streaming", '
    + 'session_mode=~"$session_mode"}'
)
OUTCOME_SHAPE = (
    "{"
    + BASE
    + ', surface=~"$surface", stream=~"$streaming", '
    + 'session_mode=~"$session_mode"}'
)
MODEL = "{" + BASE + ', model=~"$model"}'


def datasource():
    return {"type": "prometheus", "uid": DS}


def target(expr, legend=None, ref_id="A", instant=False):
    result = {
        "datasource": datasource(),
        "editorMode": "code",
        "expr": expr,
        "range": not instant,
        "instant": instant,
        "refId": ref_id,
    }
    if legend is not None:
        result["legendFormat"] = legend
    return result


def targets(pairs, instant=False):
    return [
        target(expr, legend, chr(ord("A") + index), instant)
        for index, (expr, legend) in enumerate(pairs)
    ]


def grid(x, y, width, height):
    return {"h": height, "w": width, "x": x, "y": y}


def field_config(
    unit=None,
    decimals=None,
    thresholds=None,
    color_mode="palette-classic",
    minimum=None,
    maximum=None,
    overrides=None,
):
    defaults = {
        "color": {"mode": color_mode},
        "thresholds": {
            "mode": "absolute",
            "steps": thresholds or [{"color": "green", "value": None}],
        },
    }
    if unit is not None:
        defaults["unit"] = unit
    if decimals is not None:
        defaults["decimals"] = decimals
    if minimum is not None:
        defaults["min"] = minimum
    if maximum is not None:
        defaults["max"] = maximum
    return {"defaults": defaults, "overrides": overrides or []}


def timeseries_field(
    unit=None,
    stacking="none",
    thresholds=None,
    overrides=None,
    decimals=None,
    minimum=None,
    maximum=None,
    fill=12,
):
    config = field_config(
        unit,
        decimals=decimals,
        thresholds=thresholds,
        minimum=minimum,
        maximum=maximum,
        overrides=overrides,
    )
    config["defaults"]["custom"] = {
        "drawStyle": "line",
        "lineInterpolation": "smooth",
        "lineWidth": 2,
        "fillOpacity": fill,
        "gradientMode": "opacity",
        "showPoints": "never",
        "pointSize": 5,
        "stacking": {"mode": stacking, "group": "A"},
        "axisPlacement": "auto",
        "axisLabel": "",
        "spanNulls": False,
    }
    return config


def stat_options(graph=True):
    return {
        "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
        "orientation": "auto",
        "textMode": "value_and_name",
        "colorMode": "value",
        "graphMode": "area" if graph else "none",
        "justifyMode": "auto",
    }


def timeseries_options():
    return {
        "legend": {
            "displayMode": "table",
            "placement": "bottom",
            "showLegend": True,
            "calcs": ["mean", "lastNotNull", "max"],
        },
        "tooltip": {"mode": "multi", "sort": "desc"},
    }


def pie_options():
    return {
        "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
        "pieType": "donut",
        "tooltip": {"mode": "single", "sort": "desc"},
        "legend": {
            "displayMode": "table",
            "placement": "right",
            "showLegend": True,
            "values": ["value", "percent"],
        },
        "displayLabels": ["percent"],
    }


SURFACE_OVERRIDES = [
    {
        "matcher": {"id": "byName", "options": "anthropic"},
        "properties": [
            {"id": "color", "value": {"mode": "fixed", "fixedColor": "purple"}}
        ],
    },
    {
        "matcher": {"id": "byName", "options": "openai"},
        "properties": [
            {"id": "color", "value": {"mode": "fixed", "fixedColor": "green"}}
        ],
    },
    {
        "matcher": {"id": "byName", "options": "ollama"},
        "properties": [
            {"id": "color", "value": {"mode": "fixed", "fixedColor": "blue"}}
        ],
    },
]

THRESHOLDS_BAD = [
    {"color": "green", "value": None},
    {"color": "yellow", "value": 1},
    {"color": "red", "value": 5},
]
THRESHOLDS_CONTEXT = [
    {"color": "green", "value": None},
    {"color": "yellow", "value": 70},
    {"color": "red", "value": 90},
]
THRESHOLDS_SUCCESS = [
    {"color": "red", "value": None},
    {"color": "yellow", "value": 95},
    {"color": "green", "value": 99},
]


class DashboardBuilder:
    def __init__(self):
        self.panels = []
        self.y = 0
        self.next_id = 0

    def panel_id(self):
        self.next_id += 1
        return self.next_id

    def row(self, title):
        self.panels.append(
            {
                "id": self.panel_id(),
                "type": "row",
                "title": title,
                "collapsed": False,
                "gridPos": grid(0, self.y, 24, 1),
                "panels": [],
            }
        )
        self.y += 1

    def panel(
        self,
        panel_type,
        title,
        x,
        width,
        height,
        pairs,
        unit=None,
        description="",
        options=None,
        config=None,
        instant=False,
        transformations=None,
    ):
        panel = {
            "id": self.panel_id(),
            "type": panel_type,
            "title": title,
            "datasource": datasource(),
            "gridPos": grid(x, self.y, width, height),
            "targets": targets(pairs, instant=instant),
            "fieldConfig": config or field_config(unit),
            "options": options or {},
        }
        if description:
            panel["description"] = description
        if transformations:
            panel["transformations"] = transformations
        if panel_type == "heatmap":
            for item in panel["targets"]:
                item["format"] = "heatmap"
        self.panels.append(panel)

    def next_line(self, height):
        self.y += height


def table_options(sort_name="Value"):
    return {
        "showHeader": True,
        "sortBy": [{"displayName": sort_name, "desc": True}],
    }


def horizontal_bar_options():
    return {
        "orientation": "horizontal",
        "showValue": "auto",
        "legend": {
            "showLegend": False,
            "displayMode": "list",
            "placement": "bottom",
        },
        "xTickLabelRotation": 0,
        "stacking": "none",
    }


def add_fleet_overview(builder):
    builder.row("Fleet Overview")
    stats = [
        (
            "Active Gateways (range)",
            f"count(sum by(instance)(increase(gw_llm_requests_total{LLM}[$__range])) > 0) or vector(0)",
            "Gateways that served at least one LLM request in the selected range.",
            "short",
            None,
        ),
        (
            "LLM Requests (range)",
            f"sum(increase(gw_llm_requests_total{LLM}[$__range])) or vector(0)",
            "Recognized LLM requests in the selected range.",
            "short",
            None,
        ),
        (
            "LLM Application Success",
            f"100 * sum(increase(gw_llm_request_outcomes_total{OUTCOME_SHAPE[:-1]}, outcome=\"success\"}}[$__range])) "
            f"/ clamp_min(sum(increase(gw_llm_request_outcomes_total{OUTCOME_SHAPE}[$__range])), 1)",
            "Final application outcomes, including failures after HTTP 200 streaming headers.",
            "percent",
            THRESHOLDS_SUCCESS,
        ),
        (
            "Gateways with Failures",
            f"count(sum by(instance)(increase(gw_llm_request_outcomes_total{OUTCOME_SHAPE[:-1]}, outcome!=\"success\"}}[$__range])) > 0) or vector(0)",
            "Distinct gateways with at least one final non-success application outcome.",
            "short",
            THRESHOLDS_BAD,
        ),
        (
            "Active Sessions",
            f"sum(gw_sessions_active{SEL}) or vector(0)",
            "Stateful X-Session-Id sessions currently registered.",
            "short",
            None,
        ),
        (
            "Pool Acquire p95",
            f"histogram_quantile(0.95, sum by(le)(rate(gw_pool_acquire_duration_seconds_bucket{SEL}[$__rate_interval])))",
            "95th percentile wait to acquire a warm worker slot.",
            "s",
            None,
        ),
        (
            "Kiro Credits (range)",
            f"sum(increase(gw_kiro_credits_total{SEL}[$__range])) or vector(0)",
            "Kiro credits consumed in the selected range.",
            "short",
            None,
        ),
        (
            "Unhealthy or Stalled",
            f"count(max by(instance)((gw_pool_healthy{SEL} == 0) or "
            f"((time() - gw_pool_last_progress_timestamp_seconds{SEL} > 300) "
            f"and (gw_pool_last_progress_timestamp_seconds{SEL} > 0)))) or vector(0)",
            "Gateways currently unhealthy or with no pool progress for more than five minutes.",
            "short",
            THRESHOLDS_BAD,
        ),
    ]
    for index, (title, expr, description, unit, thresholds) in enumerate(stats):
        builder.panel(
            "stat",
            title,
            index * 3,
            3,
            4,
            [(expr, "")],
            unit=unit,
            description=description,
            options=stat_options(graph=title not in {"Gateways with Failures", "Unhealthy or Stalled"}),
            config=field_config(
                unit,
                decimals=2 if unit == "percent" else None,
                thresholds=thresholds,
                color_mode="thresholds" if thresholds else "palette-classic",
            ),
        )
    builder.next_line(4)


def add_user_activity(builder):
    builder.row("User Activity and Adoption")
    builder.panel(
        "timeseries",
        "Active Gateways Over Time",
        0,
        8,
        8,
        [
            (
                f"count((sum by(instance)(rate(gw_llm_requests_total{LLM}[$__rate_interval]))) > 0)",
                "active gateways",
            )
        ],
        unit="short",
        description="Gateways actively serving LLM traffic, not merely reporting inventory.",
        options=timeseries_options(),
        config=timeseries_field("short"),
    )
    builder.panel(
        "timeseries",
        "Requests per Active Gateway",
        8,
        8,
        8,
        [
            (
                f"sum(rate(gw_llm_requests_total{LLM}[$__rate_interval])) / "
                f"clamp_min(count((sum by(instance)(rate(gw_llm_requests_total{LLM}[$__rate_interval]))) > 0), 1)",
                "requests / active gateway",
            )
        ],
        unit="reqps",
        options=timeseries_options(),
        config=timeseries_field("reqps"),
    )
    builder.panel(
        "timeseries",
        "Requests by Surface",
        16,
        8,
        8,
        [(f"sum by(surface)(rate(gw_llm_requests_total{LLM}[$__rate_interval]))", "{{surface}}")],
        unit="reqps",
        options=timeseries_options(),
        config=timeseries_field("reqps", stacking="normal", overrides=SURFACE_OVERRIDES, fill=25),
    )
    builder.next_line(8)

    builder.panel(
        "piechart",
        "Streaming vs Non-streaming",
        0,
        6,
        8,
        [(f"sum by(stream)(increase(gw_llm_request_outcomes_total{OUTCOME}[$__range]))", "{{stream}}")],
        unit="short",
        options=pie_options(),
    )
    builder.panel(
        "piechart",
        "Stateful vs Stateless",
        6,
        6,
        8,
        [(f"sum by(session_mode)(increase(gw_llm_request_outcomes_total{OUTCOME}[$__range]))", "{{session_mode}}")],
        unit="short",
        options=pie_options(),
    )
    builder.panel(
        "timeseries",
        "Requests by Skill",
        12,
        6,
        8,
        [(f"sum by(skill)(rate(gw_llm_requests_total{LLM}[$__rate_interval]))", "{{skill}}")],
        unit="reqps",
        options=timeseries_options(),
        config=timeseries_field("reqps", stacking="normal", fill=25),
    )
    builder.panel(
        "piechart",
        "Requests by Client",
        18,
        6,
        8,
        [(f"sum by(client)(increase(gw_llm_requests_total{LLM}[$__range]))", "{{client}}")],
        unit="short",
        options=pie_options(),
    )
    builder.next_line(8)

    builder.panel(
        "piechart",
        "Requests by Model",
        0,
        6,
        8,
        [(f"sum by(model)(increase(gw_model_requests_total{MODEL}[$__range]))", "{{model}}")],
        unit="short",
        options=pie_options(),
    )
    builder.panel(
        "barchart",
        "Top Gateways by Request Volume",
        6,
        6,
        8,
        [(f"topk(20, sum by(instance)(increase(gw_llm_requests_total{LLM}[$__range])))", "{{instance}}")],
        unit="short",
        instant=True,
        options=horizontal_bar_options(),
    )
    builder.panel(
        "table",
        "Top Skills",
        12,
        6,
        8,
        [(f"topk(20, sum by(skill)(increase(gw_llm_requests_total{LLM}[$__range])))", "")],
        unit="short",
        instant=True,
        options=table_options(),
    )
    builder.panel(
        "table",
        "Top Clients by Surface",
        18,
        6,
        8,
        [
            (
                f"topk(20, sum by(client, surface)(increase(gw_llm_requests_total{LLM}[$__range])))",
                "",
            )
        ],
        unit="short",
        instant=True,
        options=table_options(),
    )
    builder.next_line(8)

    builder.panel(
        "timeseries",
        "Attribution Completeness",
        0,
        24,
        7,
        [
            (
                f"100 * sum(rate(gw_llm_requests_total{{{BASE}, surface=~\"$surface\", skill=\"none\", client=~\"$client\"}}[$__rate_interval])) "
                f"/ clamp_min(sum(rate(gw_llm_requests_total{LLM}[$__rate_interval])), 0.001)",
                "skill missing %",
            ),
            (
                f"100 * sum(rate(gw_llm_requests_total{{{BASE}, surface=~\"$surface\", skill=~\"$skill\", client=\"none\"}}[$__rate_interval])) "
                f"/ clamp_min(sum(rate(gw_llm_requests_total{LLM}[$__rate_interval])), 0.001)",
                "client missing %",
            ),
        ],
        unit="percent",
        description='Share of traffic attributed to skill="none" or client="none".',
        options=timeseries_options(),
        config=timeseries_field("percent", minimum=0, maximum=100),
    )
    builder.next_line(7)


def add_user_failures(builder):
    builder.row("User Experience and Failures")
    builder.panel(
        "timeseries",
        "Application Outcomes Over Time",
        0,
        12,
        8,
        [
            (
                f"100 * sum(rate(gw_llm_request_outcomes_total{OUTCOME_SHAPE[:-1]}, outcome=\"success\"}}[$__rate_interval])) "
                f"/ clamp_min(sum(rate(gw_llm_request_outcomes_total{OUTCOME_SHAPE}[$__rate_interval])), 0.001)",
                "success %",
            ),
            (
                f"100 * sum(rate(gw_llm_request_outcomes_total{OUTCOME_SHAPE[:-1]}, outcome!=\"success\"}}[$__rate_interval])) "
                f"/ clamp_min(sum(rate(gw_llm_request_outcomes_total{OUTCOME_SHAPE}[$__rate_interval])), 0.001)",
                "failure %",
            ),
        ],
        unit="percent",
        description="Application-level completion, including failures after an HTTP 200 stream begins.",
        options=timeseries_options(),
        config=timeseries_field("percent", minimum=0, maximum=100),
    )
    builder.panel(
        "timeseries",
        "Outcomes by Type",
        12,
        6,
        8,
        [(f"sum by(outcome)(rate(gw_llm_request_outcomes_total{OUTCOME}[$__rate_interval]))", "{{outcome}}")],
        unit="reqps",
        options=timeseries_options(),
        config=timeseries_field("reqps", stacking="normal", fill=25),
    )
    builder.panel(
        "timeseries",
        "Outcomes by Surface",
        18,
        6,
        8,
        [
            (
                f"sum by(surface, outcome)(rate(gw_llm_request_outcomes_total{OUTCOME}[$__rate_interval]))",
                "{{surface}} / {{outcome}}",
            )
        ],
        unit="reqps",
        options=timeseries_options(),
        config=timeseries_field("reqps"),
    )
    builder.next_line(8)

    builder.panel(
        "barchart",
        "Top Affected Gateways",
        0,
        8,
        8,
        [
            (
                f"topk(20, sum by(instance)(increase(gw_llm_request_outcomes_total{OUTCOME_SHAPE[:-1]}, outcome!=\"success\"}}[$__range])))",
                "{{instance}}",
            )
        ],
        unit="short",
        instant=True,
        options=horizontal_bar_options(),
        description="Gateways with the most final application failures in the selected range.",
    )
    builder.panel(
        "timeseries",
        "HTTP Latency p50 / p95 / p99",
        8,
        8,
        8,
        [
            (
                f"histogram_quantile(0.50, sum by(le)(rate(gw_http_request_duration_seconds_bucket{SEL}[$__rate_interval])))",
                "p50",
            ),
            (
                f"histogram_quantile(0.95, sum by(le)(rate(gw_http_request_duration_seconds_bucket{SEL}[$__rate_interval])))",
                "p95",
            ),
            (
                f"histogram_quantile(0.99, sum by(le)(rate(gw_http_request_duration_seconds_bucket{SEL}[$__rate_interval])))",
                "p99",
            ),
        ],
        unit="s",
        options=timeseries_options(),
        config=timeseries_field("s"),
    )
    builder.panel(
        "heatmap",
        "HTTP Latency Distribution",
        16,
        8,
        8,
        [(f"sum by(le)(rate(gw_http_request_duration_seconds_bucket{SEL}[$__rate_interval]))", "{{le}}")],
        unit="s",
        options={
            "calculate": False,
            "cellGap": 1,
            "color": {"scheme": "Turbo", "mode": "scheme", "steps": 64},
            "yAxis": {"unit": "s"},
            "tooltip": {"show": True, "yHistogram": True},
            "legend": {"show": True},
        },
    )
    builder.next_line(8)

    builder.panel(
        "timeseries",
        "HTTP Request Rate by Status",
        0,
        12,
        8,
        [(f"sum by(status)(rate(gw_http_requests_total{SEL}[$__rate_interval]))", "{{status}}")],
        unit="reqps",
        options=timeseries_options(),
        config=timeseries_field("reqps", stacking="normal", fill=25),
    )
    builder.panel(
        "timeseries",
        "HTTP Request Rate by Route",
        12,
        12,
        8,
        [(f"sum by(route)(rate(gw_http_requests_total{SEL}[$__rate_interval]))", "{{route}}")],
        unit="reqps",
        options=timeseries_options(),
        config=timeseries_field("reqps", stacking="normal", fill=25),
    )
    builder.next_line(8)


def add_capacity(builder):
    builder.row("Gateway Capacity and Pool Health")
    builder.panel(
        "timeseries",
        "Pool Utilization by Gateway",
        0,
        8,
        8,
        [
            (
                f"100 * gw_pool_busy{SEL} / clamp_min(gw_pool_size{SEL}, 1)",
                "{{instance}}",
            )
        ],
        unit="percent",
        options=timeseries_options(),
        config=timeseries_field("percent", minimum=0, maximum=100),
    )
    builder.panel(
        "timeseries",
        "Pool Acquire p50 / p95 / p99",
        8,
        8,
        8,
        [
            (
                f"histogram_quantile(0.50, sum by(le)(rate(gw_pool_acquire_duration_seconds_bucket{SEL}[$__rate_interval])))",
                "p50",
            ),
            (
                f"histogram_quantile(0.95, sum by(le)(rate(gw_pool_acquire_duration_seconds_bucket{SEL}[$__rate_interval])))",
                "p95",
            ),
            (
                f"histogram_quantile(0.99, sum by(le)(rate(gw_pool_acquire_duration_seconds_bucket{SEL}[$__rate_interval])))",
                "p99",
            ),
        ],
        unit="s",
        options=timeseries_options(),
        config=timeseries_field("s"),
    )
    builder.panel(
        "timeseries",
        "Pool Acquire Results",
        16,
        8,
        8,
        [
            (
                f"sum by(result)(rate(gw_pool_acquire_duration_seconds_count{SEL}[$__rate_interval]))",
                "{{result}}",
            )
        ],
        unit="reqps",
        options=timeseries_options(),
        config=timeseries_field("reqps", stacking="normal", fill=25),
    )
    builder.next_line(8)

    builder.panel(
        "timeseries",
        "Seconds Since Pool Progress",
        0,
        8,
        8,
        [
            (
                f"(time() - gw_pool_last_progress_timestamp_seconds{SEL}) "
                f"and (gw_pool_last_progress_timestamp_seconds{SEL} > 0)",
                "{{instance}}",
            )
        ],
        unit="s",
        description="Zero timestamps are excluded; never-initialized pools do not appear as epoch-sized ages.",
        options=timeseries_options(),
        config=timeseries_field("s"),
    )
    builder.panel(
        "timeseries",
        "Active Sessions by Gateway",
        8,
        8,
        8,
        [(f"gw_sessions_active{SEL}", "{{instance}}")],
        unit="short",
        options=timeseries_options(),
        config=timeseries_field("short"),
    )
    builder.panel(
        "timeseries",
        "Session Lifecycle Rate",
        16,
        8,
        8,
        [
            (f"sum(rate(gw_sessions_created_total{SEL}[$__rate_interval]))", "created/s"),
            (f"sum(rate(gw_sessions_recycled_total{SEL}[$__rate_interval]))", "recycled/s"),
            (f"sum(rate(gw_sessions_reaped_total{SEL}[$__rate_interval]))", "reaped/s"),
        ],
        unit="short",
        options=timeseries_options(),
        config=timeseries_field("short"),
    )
    builder.next_line(8)

    builder.panel(
        "timeseries",
        "Respawns, Recycles and Ping Escalations",
        0,
        16,
        8,
        [
            (f"sum(rate(gw_pool_slot_respawns_total{SEL}[$__rate_interval]))", "respawns/s"),
            (f"sum(rate(gw_pool_slot_recycles_total{SEL}[$__rate_interval]))", "scheduled recycles/s"),
            (f"sum(rate(gw_acp_ping_escalations_total{SEL}[$__rate_interval]))", "ping escalations/s"),
            (f"sum(rate(gw_acp_ping_suspend_skips_total{SEL}[$__rate_interval]))", "suspend skips/s"),
        ],
        unit="short",
        options=timeseries_options(),
        config=timeseries_field("short"),
    )
    builder.panel(
        "timeseries",
        "In-flight HTTP Requests",
        16,
        8,
        8,
        [(f"gw_http_in_flight_requests{SEL}", "{{instance}}")],
        unit="short",
        options=timeseries_options(),
        config=timeseries_field("short"),
    )
    builder.next_line(8)

    builder.panel(
        "table",
        "Gateway Health Matrix",
        0,
        24,
        9,
        [
            (f"gw_pool_healthy{SEL}", "healthy"),
            (f"gw_pool_spawn_failing{SEL}", "spawn failing"),
            (f"gw_pool_alive{SEL}", "alive"),
            (f"gw_pool_busy{SEL}", "busy"),
            (f"gw_pool_size{SEL}", "size"),
            (f"gw_sessions_active{SEL}", "sessions"),
            (
                f"(time() - gw_pool_last_progress_timestamp_seconds{SEL}) "
                f"and (gw_pool_last_progress_timestamp_seconds{SEL} > 0)",
                "progress age",
            ),
            (
                f"(time() - gw_pool_last_spawn_error_timestamp_seconds{SEL}) "
                f"and (gw_pool_last_spawn_error_timestamp_seconds{SEL} > 0)",
                "spawn error age",
            ),
        ],
        unit="short",
        instant=True,
        options=table_options("healthy"),
        transformations=[{"id": "merge", "options": {}}],
        description="Current serving posture per gateway. Timestamp ages exclude zero values.",
    )
    builder.next_line(9)


def add_kiro(builder):
    builder.row("Kiro Cost and Context")
    builder.panel(
        "timeseries",
        "Credits and Turns Rate",
        0,
        8,
        8,
        [
            (f"sum(rate(gw_kiro_credits_total{SEL}[$__rate_interval]))", "credits/s"),
            (f"sum(rate(gw_kiro_turns_total{SEL}[$__rate_interval]))", "turns/s"),
        ],
        unit="short",
        options=timeseries_options(),
        config=timeseries_field("short"),
    )
    builder.panel(
        "stat",
        "Credits per Turn",
        8,
        4,
        8,
        [
            (
                f"sum(increase(gw_kiro_credits_total{SEL}[$__range])) / "
                f"clamp_min(sum(increase(gw_kiro_turns_total{SEL}[$__range])), 1)",
                "",
            )
        ],
        unit="short",
        options=stat_options(),
    )
    builder.panel(
        "stat",
        "Credits per LLM Request",
        12,
        4,
        8,
        [
            (
                f"sum(increase(gw_kiro_credits_total{SEL}[$__range])) / "
                f"clamp_min(sum(increase(gw_llm_requests_total{LLM}[$__range])), 1)",
                "",
            )
        ],
        unit="short",
        options=stat_options(),
    )
    builder.panel(
        "timeseries",
        "Turn Duration p50 / p95",
        16,
        8,
        8,
        [
            (
                f"histogram_quantile(0.50, sum by(le)(rate(gw_kiro_turn_duration_seconds_bucket{SEL}[$__rate_interval])))",
                "p50",
            ),
            (
                f"histogram_quantile(0.95, sum by(le)(rate(gw_kiro_turn_duration_seconds_bucket{SEL}[$__rate_interval])))",
                "p95",
            ),
        ],
        unit="s",
        options=timeseries_options(),
        config=timeseries_field("s"),
    )
    builder.next_line(8)

    builder.panel(
        "gauge",
        "Context Usage p95",
        0,
        6,
        8,
        [
            (
                f"histogram_quantile(0.95, sum by(le)(rate(gw_kiro_context_usage_percent_bucket{SEL}[$__rate_interval])))",
                "p95",
            )
        ],
        unit="percent",
        options={
            "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
            "showThresholdLabels": False,
            "showThresholdMarkers": True,
        },
        config=field_config(
            "percent",
            thresholds=THRESHOLDS_CONTEXT,
            color_mode="thresholds",
            minimum=0,
            maximum=100,
        ),
    )
    builder.panel(
        "timeseries",
        "Context Usage p50 / p95",
        6,
        10,
        8,
        [
            (
                f"histogram_quantile(0.50, sum by(le)(rate(gw_kiro_context_usage_percent_bucket{SEL}[$__rate_interval])))",
                "p50",
            ),
            (
                f"histogram_quantile(0.95, sum by(le)(rate(gw_kiro_context_usage_percent_bucket{SEL}[$__rate_interval])))",
                "p95",
            ),
        ],
        unit="percent",
        options=timeseries_options(),
        config=timeseries_field(
            "percent",
            thresholds=THRESHOLDS_CONTEXT,
            minimum=0,
            maximum=100,
        ),
    )
    builder.panel(
        "table",
        "MCP Initialization Outcomes",
        16,
        5,
        8,
        [(f"sum by(server, result)(increase(gw_kiro_mcp_server_init_total{SEL}[$__range]))", "")],
        unit="short",
        instant=True,
        options=table_options(),
    )
    builder.panel(
        "stat",
        "MCP Failure Rate",
        21,
        3,
        8,
        [
            (
                f"100 * sum(increase(gw_kiro_mcp_server_init_total{{{BASE}, result=\"fail\"}}[$__range])) / "
                f"clamp_min(sum(increase(gw_kiro_mcp_server_init_total{SEL}[$__range])), 1)",
                "",
            )
        ],
        unit="percent",
        options=stat_options(),
        config=field_config("percent", thresholds=THRESHOLDS_BAD, color_mode="thresholds"),
    )
    builder.next_line(8)


def add_compression(builder):
    builder.row("Compression Effectiveness")
    description = (
        "Compression token values use an estimated UTF-8-bytes/4 heuristic; "
        "they are not model tokenizer output or billing tokens."
    )
    stats = [
        (
            "Compression Eligible",
            f"sum(increase(gw_compress_eligible_total{SEL}[$__range])) or vector(0)",
            "short",
        ),
        (
            "Successful Compression Runs",
            f"sum(increase(gw_compress_runs_total{SEL}[$__range])) or vector(0)",
            "short",
        ),
        (
            "Estimated Tokens Saved",
            f"sum(increase(gw_compress_tokens_saved_estimate_total{SEL}[$__range])) or vector(0)",
            "short",
        ),
        (
            "Estimated Tokens Saved per Run",
            f"sum(increase(gw_compress_tokens_saved_estimate_total{SEL}[$__range])) / "
            f"clamp_min(sum(increase(gw_compress_runs_total{SEL}[$__range])), 1)",
            "short",
        ),
        (
            "Compression Success Ratio",
            f"100 * sum(increase(gw_compress_runs_total{SEL}[$__range])) / "
            f"clamp_min(sum(increase(gw_compress_eligible_total{SEL}[$__range])), 1)",
            "percent",
        ),
        (
            "Budget Unmet",
            f"sum(increase(gw_compress_budget_unmet_total{SEL}[$__range])) or vector(0)",
            "short",
        ),
        (
            "Compression Budget Unmet Ratio",
            f"100 * sum(increase(gw_compress_budget_unmet_total{SEL}[$__range])) / "
            f"clamp_min(sum(increase(gw_compress_eligible_total{SEL}[$__range])), 1)",
            "percent",
        ),
        (
            "Compression Panic Recoveries",
            f"sum(increase(gw_compress_panic_recoveries_total{SEL}[$__range])) or vector(0)",
            "short",
        ),
    ]
    for index, (title, expr, unit) in enumerate(stats):
        builder.panel(
            "stat",
            title,
            index * 3,
            3,
            5,
            [(expr, "")],
            unit=unit,
            description=description,
            options=stat_options(),
            config=field_config(
                unit,
                decimals=2 if unit == "percent" else None,
                thresholds=THRESHOLDS_BAD if "Unmet" in title or "Panic" in title else None,
                color_mode="thresholds" if "Unmet" in title or "Panic" in title else "palette-classic",
            ),
        )
    builder.next_line(5)

    builder.panel(
        "timeseries",
        "Compression Activity Over Time",
        0,
        24,
        8,
        [
            (f"sum(rate(gw_compress_eligible_total{SEL}[$__rate_interval]))", "eligible/s"),
            (f"sum(rate(gw_compress_runs_total{SEL}[$__rate_interval]))", "successful/s"),
            (f"sum(rate(gw_compress_budget_unmet_total{SEL}[$__rate_interval]))", "budget unmet/s"),
            (f"sum(rate(gw_compress_panic_recoveries_total{SEL}[$__rate_interval]))", "panic recoveries/s"),
        ],
        unit="reqps",
        description=description,
        options=timeseries_options(),
        config=timeseries_field("reqps"),
    )
    builder.next_line(8)


def add_runtime(builder):
    builder.row("Runtime Resources")
    builder.panel(
        "timeseries",
        "Gateway CPU",
        0,
        12,
        8,
        [(f"rate(process_cpu_seconds_total{SEL}[$__rate_interval]) * 100", "{{instance}}")],
        unit="percent",
        options=timeseries_options(),
        config=timeseries_field("percent"),
    )
    builder.panel(
        "timeseries",
        "Gateway RSS",
        12,
        12,
        8,
        [(f"process_resident_memory_bytes{SEL}", "{{instance}}")],
        unit="bytes",
        options=timeseries_options(),
        config=timeseries_field("bytes"),
    )
    builder.next_line(8)

    worker_note = "Worker subprocess sampling is unavailable on macOS."
    builder.panel(
        "timeseries",
        "Worker CPU",
        0,
        12,
        8,
        [(f"rate(gw_worker_cpu_seconds_total{SEL}[$__rate_interval]) * 100", "{{instance}} / {{slot}}")],
        unit="percent",
        description=worker_note,
        options=timeseries_options(),
        config=timeseries_field("percent"),
    )
    builder.panel(
        "timeseries",
        "Worker RSS",
        12,
        12,
        8,
        [(f"gw_worker_resident_memory_bytes{SEL}", "{{instance}} / {{slot}}")],
        unit="bytes",
        description=worker_note,
        options=timeseries_options(),
        config=timeseries_field("bytes"),
    )
    builder.next_line(8)

    builder.panel(
        "timeseries",
        "Open FD Utilization",
        0,
        8,
        8,
        [
            (
                f"100 * process_open_fds{SEL} / clamp_min(process_max_fds{SEL}, 1)",
                "{{instance}}",
            )
        ],
        unit="percent",
        options=timeseries_options(),
        config=timeseries_field("percent", minimum=0, maximum=100),
    )
    builder.panel(
        "timeseries",
        "Gateway Uptime",
        8,
        8,
        8,
        [(f"time() - process_start_time_seconds{SEL}", "{{instance}}")],
        unit="s",
        options=timeseries_options(),
        config=timeseries_field("s"),
    )
    builder.panel(
        "stat",
        "Gateway Restarts (range)",
        16,
        4,
        8,
        [(f"sum(changes(process_start_time_seconds{SEL}[$__range])) or vector(0)", "")],
        unit="short",
        options=stat_options(),
        config=field_config("short", thresholds=THRESHOLDS_BAD, color_mode="thresholds"),
    )
    builder.panel(
        "timeseries",
        "Goroutines (if go_* shipped)",
        20,
        4,
        8,
        [(f"go_goroutines{SEL}", "{{instance}}")],
        unit="short",
        description="Requires GW_METRICS_SERIES_PREFIXES to include go_; the default excludes it.",
        options=timeseries_options(),
        config=timeseries_field("short"),
    )
    builder.next_line(8)


def add_inventory(builder):
    builder.row("Fleet Inventory")
    builder.panel(
        "stat",
        "Gateways Reporting Now",
        0,
        6,
        6,
        [(f"count(group by(instance)(gw_build_info{SEL}))", "")],
        unit="short",
        description="Reporting installations are inventory, not the same as active gateways serving users.",
        options=stat_options(graph=False),
    )
    builder.panel(
        "table",
        "Gateways — Version and Build",
        6,
        18,
        8,
        [(f"gw_build_info{SEL}", "")],
        unit="short",
        instant=True,
        options=table_options("Gateway"),
        transformations=[
            {
                "id": "organize",
                "options": {
                    "excludeByName": {
                        "Time": True,
                        "Value": True,
                        "job": True,
                        "__name__": True,
                    },
                    "indexByName": {
                        "instance": 0,
                        "gateway_id": 1,
                        "version": 2,
                        "commit": 3,
                    },
                    "renameByName": {
                        "instance": "Gateway",
                        "gateway_id": "Gateway ID",
                        "version": "Version",
                        "commit": "Commit",
                    },
                },
            }
        ],
    )
    builder.next_line(8)


def query_variable(name, label, query):
    return {
        "name": name,
        "label": label,
        "type": "query",
        "datasource": datasource(),
        "query": {"query": query, "refId": name},
        "definition": query,
        "includeAll": True,
        "multi": True,
        "allValue": ".*",
        "refresh": 2,
        "sort": 1,
        "current": {"selected": True, "text": ["All"], "value": ["$__all"]},
        "options": [],
        "regex": "",
    }


def build_templating():
    return {
        "list": [
            {
                "name": "DS_PROM",
                "label": "Data source",
                "type": "datasource",
                "query": "prometheus",
                "current": {},
                "hide": 0,
                "refresh": 1,
                "regex": "",
                "includeAll": False,
                "multi": False,
            },
            query_variable(
                "gateway_id",
                "Gateway ID",
                'label_values(gw_build_info{job="otto-gateway"}, instance)',
            ),
            query_variable(
                "surface",
                "Surface",
                f"label_values(gw_llm_request_outcomes_total{SEL}, surface)",
            ),
            query_variable(
                "outcome",
                "Outcome",
                f"label_values(gw_llm_request_outcomes_total{SEL}, outcome)",
            ),
            query_variable(
                "streaming",
                "Streaming",
                f"label_values(gw_llm_request_outcomes_total{SEL}, stream)",
            ),
            query_variable(
                "session_mode",
                "Session Mode",
                f"label_values(gw_llm_request_outcomes_total{SEL}, session_mode)",
            ),
            query_variable(
                "skill",
                "Skill",
                f"label_values(gw_llm_requests_total{{{BASE}, surface=~\"$surface\"}}, skill)",
            ),
            query_variable(
                "client",
                "Client",
                f"label_values(gw_llm_requests_total{{{BASE}, surface=~\"$surface\"}}, client)",
            ),
            query_variable(
                "model",
                "Model",
                f"label_values(gw_model_requests_total{SEL}, model)",
            ),
        ]
    }


def build_dashboard():
    builder = DashboardBuilder()
    add_fleet_overview(builder)
    add_user_activity(builder)
    add_user_failures(builder)
    add_capacity(builder)
    add_kiro(builder)
    add_compression(builder)
    add_runtime(builder)
    add_inventory(builder)

    return {
        "annotations": {
            "list": [
                {
                    "builtIn": 1,
                    "datasource": {"type": "grafana", "uid": "-- Grafana --"},
                    "enable": True,
                    "hide": True,
                    "iconColor": "rgba(0, 211, 255, 1)",
                    "name": "Annotations & Alerts",
                    "type": "dashboard",
                }
            ]
        },
        "editable": True,
        "fiscalYearStartMonth": 0,
        "graphTooltip": 1,
        "links": [],
        "liveNow": False,
        "panels": builder.panels,
        "refresh": "30s",
        "schemaVersion": 39,
        "style": "dark",
        "tags": ["otto-gateway", "observability", "usage"],
        "templating": build_templating(),
        "time": {"from": "now-6h", "to": "now"},
        "timepicker": {},
        "timezone": "browser",
        "title": "Loop24 Co-worker",
        "uid": "gw-dev-obs",
        "version": 1,
        "weekStart": "",
    }


def write_dashboard(path=DASHBOARD_PATH):
    dashboard = build_dashboard()
    Path(path).write_text(json.dumps(dashboard, indent=2))
    print(f"panels: {len(dashboard['panels'])}")


if __name__ == "__main__":
    write_dashboard()
