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
        }
        self.assertTrue(required <= titles, sorted(required - titles))

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

    def test_generated_json_matches_committed_file(self):
        generated = json.dumps(self.dashboard, indent=2)
        self.assertEqual(generated, DASHBOARD_JSON.read_text())


if __name__ == "__main__":
    unittest.main()
