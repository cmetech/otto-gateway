import importlib.util
import json
import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
GENERATOR = ROOT / "scripts" / "gen_grafana_dashboard.py"
DASHBOARD_JSON = ROOT / "docs" / "grafana" / "otto-gateway-dashboard.json"

ROW_ORDER = [
    "Fleet Overview",
    "Privacy Boundary",
    "User Activity and Adoption",
    "User Experience and Failures",
    "Gateway Capacity and Pool Health",
    "Kiro Cost and Context",
    "Compression Effectiveness",
    "Runtime Resources",
    "Fleet Inventory",
]

VARIABLE_ORDER = [
    "Data source",
    "Gateway ID",
    "Surface",
    "Outcome",
    "Streaming",
    "Session Mode",
    "Skill",
    "Client",
    "Model",
]

CUSTOM_METRICS = {
    "gw_acp_ping_escalations_total",
    "gw_acp_ping_suspend_skips_total",
    "gw_build_info",
    "gw_compress_budget_unmet_total",
    "gw_compress_eligible_total",
    "gw_compress_panic_recoveries_total",
    "gw_compress_runs_total",
    "gw_compress_tokens_saved_estimate_total",
    "gw_http_in_flight_requests",
    "gw_http_request_duration_seconds",
    "gw_http_requests_total",
    "gw_kiro_context_usage_percent",
    "gw_kiro_credits_total",
    "gw_kiro_mcp_server_init_total",
    "gw_kiro_turn_duration_seconds",
    "gw_kiro_turns_total",
    "gw_llm_request_outcomes_total",
    "gw_llm_requests_total",
    "gw_model_requests_total",
    "gw_pool_acquire_duration_seconds",
    "gw_pool_alive",
    "gw_pool_busy",
    "gw_pool_healthy",
    "gw_pool_last_progress_timestamp_seconds",
    "gw_pool_last_spawn_error_timestamp_seconds",
    "gw_pool_size",
    "gw_pool_slot_recycles_total",
    "gw_pool_slot_respawns_total",
    "gw_pool_spawn_failing",
    "gw_privacy_blocks_total",
    "gw_privacy_capacity_rejections_total",
    "gw_privacy_errors_total",
    "gw_privacy_mapping_capacity",
    "gw_privacy_mapping_entries",
    "gw_privacy_mapping_operations_total",
    "gw_privacy_mapping_per_scope_capacity",
    "gw_privacy_oldest_scope_age_seconds",
    "gw_privacy_processing_duration_seconds",
    "gw_privacy_receipts_total",
    "gw_privacy_requests_total",
    "gw_privacy_residual_findings_total",
    "gw_privacy_scope_capacity",
    "gw_privacy_scope_events_total",
    "gw_privacy_scope_requests_in_flight",
    "gw_privacy_scope_ttl_seconds",
    "gw_privacy_scopes_active",
    "gw_privacy_transformations_total",
    "gw_privacy_restorations_total",
    "gw_privacy_triage_enabled",
    "gw_privacy_triage_requests_total",
    "gw_sessions_active",
    "gw_sessions_created_total",
    "gw_sessions_reaped_total",
    "gw_sessions_recycled_total",
    "gw_worker_cpu_seconds_total",
    "gw_worker_resident_memory_bytes",
}


def load_generator():
    spec = importlib.util.spec_from_file_location("gen_grafana_dashboard", GENERATOR)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def all_panels(dashboard):
    for panel in dashboard["panels"]:
        yield panel
        yield from panel.get("panels", [])


def metric_family(name):
    for suffix in ("_bucket", "_sum", "_count"):
        if name.endswith(suffix):
            return name[: -len(suffix)]
    return name


def promql_or_vector_zero_label_sets(left_label_sets):
    """Model PromQL `left or vector(0)` label-set union for static fixtures."""
    result = set(left_label_sets)
    result.add(())
    return result


def count_gated_histogram_value(count_rate, quantile_value):
    """Model `quantile and on(...) count_rate > 0` for an idle fixture."""
    return quantile_value if count_rate > 0 else None


class DashboardGeneratorTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        before = DASHBOARD_JSON.stat().st_mtime_ns
        cls.generator = load_generator()
        after = DASHBOARD_JSON.stat().st_mtime_ns
        if before != after:
            raise AssertionError("importing the generator rewrote the committed dashboard")
        cls.dashboard = cls.generator.build_dashboard()

    def test_variable_order(self):
        labels = [variable["label"] for variable in self.dashboard["templating"]["list"]]
        self.assertEqual(labels, VARIABLE_ORDER)

    def test_row_order(self):
        rows = [
            panel["title"]
            for panel in self.dashboard["panels"]
            if panel.get("type") == "row"
        ]
        self.assertEqual(rows, ROW_ORDER)

    def test_required_panels(self):
        titles = {panel["title"] for panel in all_panels(self.dashboard)}
        required = {
            "Active Gateways (range)",
            "LLM Application Success",
            "Gateways with Failures",
            "Pool Acquire p95",
            "Active Gateways Over Time",
            "Requests per Active Gateway",
            "Streaming vs Non-streaming",
            "Stateful vs Stateless",
            "Application Outcomes Over Time",
            "Top Affected Gateways",
            "Pool Utilization by Gateway",
            "Seconds Since Pool Progress",
            "Gateway Health Matrix",
            "Credits per Turn",
            "Compression Success Ratio",
            "Compression Budget Unmet Ratio",
            "Open FD Utilization",
            "Gateway Uptime",
            "Gateways Reporting Now",
            "Privacy Request Results",
            "Privacy Transformations and Restorations",
            "Privacy Blocks and Residual Findings",
            "Privacy Processing Latency",
            "Privacy Scope and Capacity Utilization",
            "Privacy Receipt Outcomes",
            "Privacy Triage Operations",
            "Privacy Internal Errors",
        }
        self.assertTrue(required <= titles, sorted(required - titles))

    def test_privacy_alerts(self):
        titles = {panel["title"] for panel in all_panels(self.dashboard)}
        required = {
            "Alert: Strict Privacy Blocks",
            "Alert: Privacy Residual Findings",
            "Alert: Privacy Capacity Pressure",
            "Alert: Privacy Mapping Growth",
            "Alert: Privacy Internal Errors",
            "Alert: Missing Strict Receipts",
        }
        self.assertTrue(required <= titles, sorted(required - titles))

    def test_privacy_request_results_are_bounded_dimensions(self):
        panel = next(
            panel
            for panel in all_panels(self.dashboard)
            if panel["title"] == "Privacy Request Results"
        )
        expr = panel["targets"][0]["expr"]
        self.assertIn("gw_privacy_requests_total", expr)
        self.assertRegex(expr, r"sum by\(profile, ?surface, ?workload, ?result\)")
        self.assertIn("[$__rate_interval]", expr)

    def test_privacy_capacity_panels_show_current_and_configured_maxima(self):
        panel = next(
            panel
            for panel in all_panels(self.dashboard)
            if panel["title"] == "Privacy Scope and Capacity Utilization"
        )
        exprs = "\n".join(target["expr"] for target in panel["targets"])
        for metric in (
            "gw_privacy_scopes_active",
            "gw_privacy_scope_capacity",
            "gw_privacy_mapping_entries",
            "gw_privacy_mapping_capacity",
            "gw_privacy_mapping_per_scope_capacity",
            "gw_privacy_scope_requests_in_flight",
            "gw_privacy_scope_ttl_seconds",
            "gw_privacy_oldest_scope_age_seconds",
        ):
            self.assertIn(metric, exprs)

    def test_privacy_queries_use_only_bounded_safe_group_labels(self):
        allowed = {
            "action",
            "entity",
            "event",
            "instance",
            "le",
            "operation",
            "profile",
            "reason",
            "resource",
            "result",
            "stage",
            "surface",
            "workload",
        }
        protected = {
            "scope",
            "scope_id",
            "request",
            "request_id",
            "route",
            "error",
            "raw_error",
            "token",
            "alias",
            "original",
            "synthetic",
            "value",
            "session",
            "user",
        }
        for panel in all_panels(self.dashboard):
            for query in panel.get("targets", []):
                expr = query.get("expr", "")
                if "gw_privacy_" not in expr:
                    continue
                for match in re.finditer(r"\b(?:by|without)\s*\(([^)]*)\)", expr):
                    labels = {
                        label.strip()
                        for label in match.group(1).split(",")
                        if label.strip()
                    }
                    self.assertFalse(labels & protected, f'{panel["title"]}: {expr}')
                    self.assertTrue(labels <= allowed, f'{panel["title"]}: {expr}')

    def test_privacy_rates_and_percentages_tolerate_idle_series(self):
        for panel in all_panels(self.dashboard):
            for query in panel.get("targets", []):
                expr = query.get("expr", "")
                if "gw_privacy_" not in expr:
                    continue
                if "rate(" in expr:
                    self.assertIn("[$__rate_interval]", expr, panel["title"])
                if "100 *" in expr:
                    self.assertIn("clamp_min(", expr, panel["title"])
                if panel.get("type") == "stat" and "increase(" in expr:
                    self.assertIn("or vector(0)", expr, panel["title"])

    def test_privacy_dimensioned_reports_do_not_add_unlabeled_zero(self):
        for panel in all_panels(self.dashboard):
            if panel.get("type") == "stat":
                continue
            for query in panel.get("targets", []):
                expr = query.get("expr", "")
                if "gw_privacy_" not in expr:
                    continue
                if "sum by(" in expr or "histogram_quantile(" in expr:
                    self.assertNotRegex(
                        expr,
                        r"\bor\s+vector\(0\)\s*$",
                        f'{panel["title"]} creates an unlabeled ghost series: {expr}',
                    )

    def test_unqualified_zero_fallback_fixture_exposes_ghost_label_semantics(self):
        active = {
            (("profile", "strict"), ("stage", "input")),
        }
        self.assertEqual(promql_or_vector_zero_label_sets(set()), {()})
        self.assertEqual(
            promql_or_vector_zero_label_sets(active),
            active | {()},
            "an unqualified fallback must not be treated as label-compatible",
        )

    def test_privacy_latency_is_no_data_when_matching_count_rate_is_idle(self):
        panel = next(
            panel
            for panel in all_panels(self.dashboard)
            if panel["title"] == "Privacy Processing Latency"
        )
        targets = {target["legendFormat"].split(" / ", 1)[0]: target["expr"] for target in panel["targets"]}
        for name in ("average", "p95"):
            expr = targets[name]
            self.assertIn(
                "gw_privacy_processing_duration_seconds_count",
                expr,
                f"{name} lacks a matching observation-count gate",
            )
            self.assertRegex(expr, r"\band on\(profile, ?stage\)")
            self.assertRegex(expr, r"_count[^\n]+>\s*0")
            self.assertNotRegex(expr, r"\bor\s+vector\(0\)")
        self.assertIsNone(
            count_gated_histogram_value(0, float("nan")),
            "idle p95 must be no-data rather than NaN beside an unrelated zero",
        )
        self.assertEqual(count_gated_histogram_value(1, 0.125), 0.125)

    def test_scalar_privacy_alerts_keep_honest_zero_fallbacks(self):
        fallbacks = []
        for panel in all_panels(self.dashboard):
            privacy_alert = panel.get("title", "").startswith(
                "Alert: Privacy"
            ) or panel.get("title") == "Alert: Strict Privacy Blocks"
            if panel.get("type") != "stat" or not privacy_alert:
                continue
            for query in panel.get("targets", []):
                expr = query.get("expr", "")
                if "gw_privacy_" in expr and re.search(r"\bor\s+vector\(0\)\s*$", expr):
                    fallbacks.append(panel["title"])
        self.assertIn("Alert: Strict Privacy Blocks", fallbacks)
        self.assertIn("Alert: Privacy Capacity Pressure", fallbacks)

    def test_every_metric_panel_has_gateway_selector(self):
        for panel in all_panels(self.dashboard):
            for target in panel.get("targets", []):
                expr = target.get("expr", "")
                if re.search(r"\b(?:gw|process|go)_[a-zA-Z_:][a-zA-Z0-9_:]*", expr):
                    self.assertIn(
                        'instance=~"$gateway_id"',
                        expr,
                        f'{panel["title"]}: {expr}',
                    )

    def test_all_custom_metrics_are_used(self):
        used = set()
        for panel in all_panels(self.dashboard):
            for target in panel.get("targets", []):
                for name in re.findall(r"\bgw_[a-zA-Z_:][a-zA-Z0-9_:]*", target.get("expr", "")):
                    used.add(metric_family(name))
        self.assertEqual(used, CUSTOM_METRICS)

    def test_unhealthy_or_stalled_requires_saturated_pool(self):
        panel = next(
            panel
            for panel in all_panels(self.dashboard)
            if panel["title"] == "Unhealthy or Stalled"
        )
        expr = panel["targets"][0]["expr"]
        self.assertRegex(
            expr,
            r"gw_pool_busy\{[^}]+\}\s*==\s*gw_pool_alive\{",
        )
        self.assertRegex(
            expr,
            r"gw_pool_busy\{[^}]+\}\s*==\s*gw_pool_size\{",
        )
        self.assertRegex(expr, r"gw_pool_size\{[^}]+\}\s*>\s*0")

    def test_generated_json_matches_committed_file(self):
        generated = json.dumps(self.dashboard, indent=2)
        self.assertEqual(generated, DASHBOARD_JSON.read_text())


if __name__ == "__main__":
    unittest.main()
