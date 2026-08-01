#!/usr/bin/env python3
"""Cold-reader contract tests for the Gateway privacy operations guide."""

import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
GUIDE = ROOT / "docs" / "privacy-boundary.md"


def read(relative_path):
    path = ROOT / relative_path
    return path.read_text() if path.exists() else ""


class PrivacyDocsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.guide = read("docs/privacy-boundary.md")
        cls.env = read("scripts/.env.example")

    def assertGuideContains(self, *needles):
        for needle in needles:
            self.assertIn(needle, self.guide, f"privacy guide missing {needle!r}")

    def test_canonical_guide_exists_and_is_discoverable(self):
        self.assertTrue(GUIDE.is_file(), "docs/privacy-boundary.md is absent")
        entry_points = {
            "README.md": "docs/privacy-boundary.md",
            "docs/operating.md": "privacy-boundary.md",
            "docs/operator-quickstart.md": "privacy-boundary.md",
            "docs/INSTALL.md": "privacy-boundary.md",
            "scripts/.env.example": "docs/privacy-boundary.md",
            "internal/admin/templates/docs.html.tmpl": "docs/privacy-boundary.md",
        }
        for path, link in entry_points.items():
            self.assertIn(link, read(path), f"{path} does not link the canonical guide")

    def test_profiles_precedence_and_every_privacy_default(self):
        defaults = {
            "PRIVACY_DEFAULT_PROFILE": "standard",
            "PRIVACY_REQUEST_PROFILES": "standard,strict",
            "PRIVACY_ALIAS_KEY": "<generated-by-gw-init>",
            "PRIVACY_SECRET_ACTION": "replace",
            "PRIVACY_TECHNICAL_ACTION": "pseudonymize",
            "PRIVACY_SCOPE_TTL": "1h",
            "PRIVACY_MAX_SCOPES": "128",
            "PRIVACY_MAX_ENTRIES_PER_SCOPE": "4096",
            "PRIVACY_MAX_TOTAL_ENTRIES": "32768",
            "PRIVACY_TRIAGE_ENABLED": "false",
            "PRIVACY_TRIAGE_TOKEN": "<generated-by-gw-init>",
        }
        self.assertGuideContains(
            "standard is enabled by default",
            "strict is selectable",
            "may raise",
            "never lower",
        )
        for key, default in defaults.items():
            self.assertRegex(
                self.guide,
                rf"(?s)`{re.escape(key)}`.{{0,240}}`?{re.escape(default)}`?",
                f"guide does not document {key} default {default}",
            )
            self.assertRegex(
                self.env,
                rf"(?m)^{re.escape(key)}={re.escape(default)}$",
                f"environment template does not set {key}={default}",
            )

    def test_retained_pii_contract_and_compiled_ner_default(self):
        self.assertGuideContains(
            "PII_REDACTION_ENABLED",
            "PII_REDACTION_MODE",
            "PII_ENABLED_ENTITIES",
            "PII_HASH_KEY",
            "PII_ENCRYPT_KEY",
            "PII_ENTITY_ACTIONS",
            "PII_NER_ENABLED",
            "16 regex recognizers",
            "PERSON",
            "LOCATION",
            "compiled default is `true`",
            "English-only",
        )
        self.assertRegex(self.guide, r"PII_REDACTION_ENABLED.{0,120}`true`")
        self.assertRegex(self.guide, r"PII_REDACTION_MODE.{0,120}`encrypt`")
        self.assertRegex(self.guide, r"PII_NER_ENABLED.{0,120}`true`")

    def test_restart_override_and_secret_lifecycle(self):
        self.assertGuideContains(
            "overrides.env",
            "loaded last",
            "Restart required",
            "preserves all five managed secrets",
            "--regenerate-secrets",
            "mapping loss",
            "Rotating `PRIVACY_ALIAS_KEY`",
            "active aliases",
        )

    def test_headers_receipt_decode_and_strict_consumer_rejections(self):
        self.assertGuideContains(
            "X-GW-Privacy-Profile",
            "X-GW-Privacy-Scope",
            "X-GW-Privacy-Receipt",
            "base64.b64decode",
            "validate=True",
            'profile == "strict"',
            'coverage == "full"',
            'result == "pass"',
            "missing receipt",
            "malformed receipt",
            "non-strict receipt",
            "non-full receipt",
            "non-pass receipt",
            "direct worker access bypasses the privacy boundary",
        )

    def test_exact_status_triage_clear_and_security_boundary(self):
        self.assertGuideContains(
            "gw privacy status",
            "gw privacy scopes",
            "gw privacy inspect <scope-id>",
            "gw privacy clear <scope-id>",
            "gw privacy clear --all --yes",
            "GET /admin/api/snapshot",
            "GET /admin/api/privacy/scopes",
            "GET /admin/api/privacy/scopes/{scope-id}/mapping",
            "DELETE /admin/api/privacy/scopes/{scope-id}",
            "DELETE /admin/api/privacy/scopes",
            "X-GW-Privacy-Confirm: clear-all",
            "204",
            "202",
            "404",
            "actual TCP peer",
            "X-Forwarded-For",
            "separate triage token",
            "Cache-Control: no-store",
            "no CORS",
        )

    def test_memory_lifecycle_order_streaming_trace_and_errors(self):
        self.assertGuideContains(
            "memory only",
            "TTL is based on inactivity",
            "in-flight request prevents expiry",
            "PRIVACY_MAX_SCOPES",
            "PRIVACY_MAX_ENTRIES_PER_SCOPE",
            "PRIVACY_MAX_TOTAL_ENTRIES",
            "Restarting Gateway clears every scope and mapping",
            "must reproduce the failure before restart",
            "Compression → Privacy → Logging",
            "final content-mutating inbound stage",
            "strict chat trace is metadata-only",
            "Standard chat trace remains sensitive",
            "buffers the complete worker response",
            "before releasing headers or body bytes",
            "native error envelope",
        )
        for code in (
            "privacy_request_invalid",
            "privacy_profile_unavailable",
            "privacy_scope_closed",
            "privacy_input_blocked",
            "privacy_output_blocked",
            "privacy_capacity_exceeded",
            "privacy_internal_error",
        ):
            self.assertIn(code, self.guide, f"stable error {code} is undocumented")

    def test_workflow_ownership_stays_outside_gateway(self):
        self.assertGuideContains(
            "Workflow engine owns",
            "document parsing",
            "data minimization",
            "model routing",
            "scope propagation",
            "receipt enforcement",
            "output-schema validation",
            "tool execution",
            "capture policy",
            "final artifact",
            "Gateway does not",
        )

    def test_upgrade_and_rollback_implications(self):
        self.assertGuideContains(
            "Upgrade",
            "Rollback",
            "older binary",
            "does not understand the privacy headers",
            "strict consumers must fail closed",
            "PII_NER_ENABLED",
            "compiled default is `true`",
        )

    def test_examples_do_not_embed_secrets_and_guide_has_no_source_line_refs(self):
        for key in ("PRIVACY_ALIAS_KEY", "PRIVACY_TRIAGE_TOKEN"):
            assignments = re.findall(rf"(?m)^{key}=(.*)$", self.guide)
            for value in assignments:
                self.assertIn("generated", value, f"guide embeds a value for {key}")
        self.assertNotRegex(
            self.guide,
            r"Authorization:\s*Bearer\s+(?!<|\$)[A-Za-z0-9._~-]{8,}",
        )
        self.assertNotRegex(self.guide, r"(?:internal|cmd)/[^\s`]+\.go(?::\d+)?")
        self.assertNotRegex(self.guide, r"scripts/[^\s`]+(?::\d+)")


if __name__ == "__main__":
    unittest.main()
