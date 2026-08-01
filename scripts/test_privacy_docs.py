#!/usr/bin/env python3
"""Cold-reader contract tests for the Gateway privacy operations guide."""

import re
import unittest
from hashlib import sha256
from html import unescape
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
GUIDE = ROOT / "docs" / "privacy-boundary.md"
REGEX_RECOGNIZERS = (
    "Email",
    "IPv4",
    "IPv6",
    "SSN",
    "CreditCard",
    "USPhone",
    "SIP_URI",
    "IMEI",
    "IMSI",
    "MSISDN",
    "MAC_ADDRESS",
    "COORDINATES",
    "SITE",
    "USAddress",
    "USState",
    "USZIP",
)
NER_RECOGNIZERS = ("PERSON", "LOCATION")
ENTITY_ACTIONS = ("replace", "mask", "hash", "drop", "encrypt", "pseudonymize")
UPGRADE_COMMANDS = {
    "posix": (
        "./scripts/gw upgrade-env --dry-run",
        "./scripts/gw upgrade-env",
        "./scripts/gw init --force --non-interactive",
        "./scripts/gw restart",
    ),
    "powershell": (
        r".\scripts\gw.ps1 upgrade-env -DryRun",
        r".\scripts\gw.ps1 upgrade-env",
        r".\scripts\gw.ps1 init -Force -NonInteractive",
        r".\scripts\gw.ps1 restart",
    ),
}


def read(relative_path):
    path = ROOT / relative_path
    return path.read_text() if path.exists() else ""


def line_containing(text, *needles):
    for line in text.splitlines():
        if all(needle in line for needle in needles):
            return line
    return ""


def mentioned_names(text, names):
    return {
        name
        for name in names
        if re.search(rf"(?<![A-Za-z0-9_]){re.escape(name)}(?![A-Za-z0-9_])", text)
    }


def plain_text(text):
    without_markup = re.sub(
        r"</?[A-Za-z][A-Za-z0-9]*(?:\s+[^>]*)?>|[`#*]", " ", unescape(text)
    )
    collapsed = re.sub(r"\s+", " ", without_markup).strip()
    return re.sub(r"\s+([,.;:])", r"\1", collapsed)


def markdown_section(text, heading):
    start = text.find(heading)
    if start < 0:
        return ""
    level = heading.split(" ", 1)[0]
    match = re.search(rf"(?m)^{re.escape(level)}\s+", text[start + len(heading) :])
    end = start + len(heading) + match.start() if match else len(text)
    return text[start:end]


def owned_upgrade_sections(install, quickstart):
    return {
        "manual-install": markdown_section(install, "### How to upgrade (step-by-step)"),
        "operator-quickstart": markdown_section(
            quickstart, "## Upgrade an existing installation"
        ),
    }


def simulate_preprivacy_upgrade(commands, fixture_name):
    """Walk documented commands using the approved preserve-and-fill behavior."""
    existing = {
        "AUTH_TOKEN": "existing-auth",
        "PII_HASH_KEY": "existing-hash",
        "PII_ENCRYPT_KEY": "existing-encrypt",
    }
    state = dict(existing)
    startable = False
    for command in commands:
        lowered = command.lower()
        if "upgrade-env" in lowered and "dryrun" not in lowered and "dry-run" not in lowered:
            state.setdefault("PRIVACY_ALIAS_KEY", "<generated-by-gw-init>")
            state.setdefault("PRIVACY_TRIAGE_TOKEN", "<generated-by-gw-init>")
        elif " init " in f" {lowered} ":
            for key in ("PRIVACY_ALIAS_KEY", "PRIVACY_TRIAGE_TOKEN"):
                if state.get(key) in (None, "", "<generated-by-gw-init>"):
                    state[key] = sha256(f"{fixture_name}:{key}".encode()).hexdigest()
        elif lowered.endswith(" restart"):
            startable = all(state.get(key) for key in existing) and all(
                re.fullmatch(r"[0-9a-f]{64}", state.get(key, ""))
                for key in ("PRIVACY_ALIAS_KEY", "PRIVACY_TRIAGE_TOKEN")
            )
    return existing, state, startable


class PrivacyDocsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.guide = read("docs/privacy-boundary.md")
        cls.env = read("scripts/.env.example")
        cls.operating = read("docs/operating.md")
        cls.admin = read("internal/admin/templates/docs.html.tmpl")
        cls.install = read("docs/INSTALL.md")
        cls.quickstart = read("docs/operator-quickstart.md")

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

    def test_every_owned_inventory_is_exactly_16_regex_plus_2_ner(self):
        expected = set(REGEX_RECOGNIZERS + NER_RECOGNIZERS)
        env_match = re.search(r"# Comma list from \{([^}]+)\}", self.env, re.DOTALL)
        self.assertIsNotNone(env_match, "environment recognizer inventory is absent")
        surfaces = {
            "environment template": env_match.group(1),
            "operating flag": line_containing(self.operating, "--entities LIST"),
            "operating setting": line_containing(self.operating, "| `PII_ENABLED_ENTITIES` |"),
            "admin flag": line_containing(self.admin, "--entities LIST"),
            "admin setting": line_containing(self.admin, "<dt><code>PII_ENABLED_ENTITIES</code>"),
            "canonical guide": line_containing(self.guide, "The inventory is 16 regex recognizers"),
        }
        for label, inventory in surfaces.items():
            self.assertEqual(
                mentioned_names(inventory, expected),
                expected,
                f"{label} does not enumerate the exact 16 regex + 2 NER inventory",
            )
        for label, source in {
            "environment template": self.env,
            "operating guide": self.operating,
            "admin guide": self.admin,
        }.items():
            self.assertNotRegex(source, r"(?i)13 regex|all 15 recognizers|six original \+ seven telecom")

    def test_entity_action_contract_is_exact_and_strict_technical_only(self):
        expected = set(ENTITY_ACTIONS)
        sections = {
            "environment template": self.env[
                self.env.index("# Per-entity action override map") : self.env.index("# PII_ENTITY_ACTIONS=")
            ],
            "operating guide": line_containing(self.operating, "| `PII_ENTITY_ACTIONS` |"),
            "admin guide": line_containing(self.admin, "<dt><code>PII_ENTITY_ACTIONS</code>"),
            "canonical guide": line_containing(self.guide, "| `PII_ENTITY_ACTIONS`"),
        }
        for label, section in sections.items():
            normalized = plain_text(section)
            self.assertEqual(
                mentioned_names(section, expected),
                expected,
                f"{label} does not enumerate the exact six actions",
            )
            self.assertIn("pseudonymize is supported only for technical identifiers", normalized)
            self.assertIn("Compatible listed overrides win", normalized)
            self.assertIn("unlisted personal data uses PII_REDACTION_MODE", normalized)
            self.assertIn(
                "unlisted strict technical data uses PRIVACY_TECHNICAL_ACTION",
                normalized,
            )

    def test_env_operating_and_admin_agree_on_five_secret_lifecycle(self):
        required = (
            "The five managed secrets are AUTH_TOKEN, PII_HASH_KEY, PII_ENCRYPT_KEY, PRIVACY_ALIAS_KEY, and PRIVACY_TRIAGE_TOKEN.",
            "A normal re-init preserves existing AUTH_TOKEN, PII_HASH_KEY, and PII_ENCRYPT_KEY and mints only missing PRIVACY_ALIAS_KEY and PRIVACY_TRIAGE_TOKEN.",
            "Explicit secret regeneration rotates all five.",
            "The shipped <generated-by-gw-init> placeholders are invalid at startup when strict or triage requires them.",
        )
        for label, source in {
            "environment template": self.env,
            "operating guide": self.operating,
            "admin guide": self.admin,
        }.items():
            normalized = plain_text(source)
            for sentence in required:
                self.assertIn(sentence, normalized, f"{label} is missing lifecycle fact: {sentence}")
            self.assertNotRegex(
                normalized,
                r"(?i)(rotates|mints|managed secrets).{0,80}(all three|three secrets)",
            )

    def test_strict_minimum_upgrade_is_copy_safe_and_value_free(self):
        start = self.guide.index("To make strict the minimum")
        end = self.guide.index("### Retained PII settings")
        procedure = self.guide[start:end]
        commands = (
            "gw upgrade-env --dry-run",
            "gw upgrade-env",
            "gw init --force --non-interactive",
            "gw.ps1 upgrade-env -DryRun",
            "gw.ps1 upgrade-env",
            "gw.ps1 init -Force -NonInteractive",
        )
        for command in commands:
            self.assertIn(command, procedure, f"strict-minimum procedure omits {command}")
        self.assertLess(procedure.index("gw upgrade-env\n"), procedure.index("gw init --force"))
        self.assertLess(procedure.index("gw.ps1 upgrade-env\n"), procedure.index("gw.ps1 init -Force"))
        self.assertLess(procedure.index("gw init --force"), procedure.index("PRIVACY_DEFAULT_PROFILE=strict"))
        self.assertIn("mints only missing privacy secrets", procedure)
        self.assertIn("preserves existing `AUTH_TOKEN`, `PII_HASH_KEY`, and `PII_ENCRYPT_KEY`", procedure)
        self.assertIn("placeholders are invalid at startup", procedure)
        self.assertIn("without printing either value", procedure)
        self.assertIn("length($2) == 64", procedure)
        self.assertIn("[0-9a-f]{64}", procedure)
        self.assertNotIn("--show-secrets", procedure)

    def test_owned_preprivacy_upgrade_paths_have_exact_sequence(self):
        surfaces = owned_upgrade_sections(self.install, self.quickstart)
        for surface_name, section in surfaces.items():
            with self.subTest(surface=surface_name):
                self.assertTrue(section, f"{surface_name} upgrade section is absent")
                command_lines = [line.strip() for line in section.splitlines()]
                normalized = plain_text(section)
                for platform, commands in UPGRADE_COMMANDS.items():
                    positions = []
                    for command in commands:
                        self.assertIn(
                            command,
                            command_lines,
                            f"{surface_name} {platform} path omits exact command: {command}",
                        )
                        positions.append(command_lines.index(command))
                    self.assertEqual(
                        positions,
                        sorted(positions),
                        f"{surface_name} {platform} upgrade order is unsafe",
                    )
                self.assertNotRegex(
                    section,
                    r"(?i)(?:do|normally do|need)\s+not.{0,40}re-run\s+`?init",
                    f"{surface_name} contains an unqualified no-init upgrade path",
                )
                self.assertIn("privacy-boundary.md#privacy-settings", section)
                self.assertIn("value-free privacy-secret presence check", section)
                self.assertIn("prints only privacy secrets: present", normalized)
                self.assertIn(
                    "preserves existing AUTH_TOKEN, PII_HASH_KEY, and PII_ENCRYPT_KEY",
                    normalized,
                )
                self.assertIn(
                    "mints only missing PRIVACY_ALIAS_KEY and PRIVACY_TRIAGE_TOKEN",
                    normalized,
                )
                normal_commands = "\n".join(
                    line
                    for line in command_lines
                    if " upgrade-env" in line
                    or " init " in f" {line} "
                    or line.endswith(" restart")
                )
                self.assertNotRegex(normal_commands, r"(?i)regenerate[- ]?secrets")
        for source_name, source in {
            "install reference": self.install,
            "operator quickstart": self.quickstart,
        }.items():
            self.assertNotRegex(
                plain_text(source),
                r"(?i)(?:do|normally do|need)\s+not.{0,40}re-run\s+`?init",
                f"{source_name} contains an unqualified no-init statement",
            )

    def test_owned_upgrade_paths_are_startable_from_three_secret_fixture(self):
        surfaces = owned_upgrade_sections(self.install, self.quickstart)
        generated_privacy_values = set()
        for surface_name, section in surfaces.items():
            for platform, expected_commands in UPGRADE_COMMANDS.items():
                with self.subTest(surface=surface_name, platform=platform):
                    documented = [
                        command
                        for command in expected_commands
                        if command in [line.strip() for line in section.splitlines()]
                    ]
                    self.assertEqual(documented, list(expected_commands))
                    existing, result, startable = simulate_preprivacy_upgrade(
                        documented, f"{surface_name}:{platform}"
                    )
                    for key, value in existing.items():
                        self.assertEqual(result[key], value, f"{key} rotated during normal upgrade")
                    alias = result.get("PRIVACY_ALIAS_KEY", "")
                    triage = result.get("PRIVACY_TRIAGE_TOKEN", "")
                    self.assertRegex(alias, r"^[0-9a-f]{64}$")
                    self.assertRegex(triage, r"^[0-9a-f]{64}$")
                    self.assertNotEqual(alias, triage)
                    self.assertNotEqual(alias, "<generated-by-gw-init>")
                    self.assertNotEqual(triage, "<generated-by-gw-init>")
                    self.assertTrue(startable, "documented restart would fail config validation")
                    generated_privacy_values.update((alias, triage))
        self.assertEqual(len(generated_privacy_values), 8)

    def test_install_rotation_guidance_has_no_legacy_encrypt_exception(self):
        self.assertIn(
            "Explicit `--regenerate-secrets` / `-RegenerateSecrets` rotates all five managed secrets.",
            self.install,
        )
        self.assertNotRegex(
            plain_text(self.install),
            r"(?i)regenerate-secrets.{0,80}does not.{0,40}rotate",
        )

    def test_restart_override_and_secret_lifecycle(self):
        self.assertGuideContains(
            "overrides.env",
            "loaded last",
            "Restart required",
            "preserves every usable existing managed secret",
            "mints only a missing or shipped-placeholder privacy alias key or triage token",
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
