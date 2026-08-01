# Requirements: v1.10.4 Privacy Boundary Service

**Defined:** 2026-07-31
**Status:** Approved for implementation
**Design authority:** [Privacy Boundary Service Design](../docs/superpowers/specs/2026-07-31-privacy-boundary-service-design.md)
**Execution authority:** [Gateway Privacy Boundary Service Plan](../docs/superpowers/plans/2026-07-31-gateway-privacy-boundary-service.md)

The approved design is locked. These requirements are a tracking index for Phase 21, not a replacement for the authoritative specification or its 17-task implementation plan.

## Active Requirements

### Policy and classification

- [x] **PRIV-01**: Standard privacy protection remains enabled and backward-compatible by default; strict privacy is selectable; a request may raise but never lower the configured minimum profile.
- [x] **PRIV-02**: High-confidence credentials are detected by one shared classifier, replaced one-way, never written to the reversible mapping ledger, and never restored.
- [x] **PRIV-03**: Strict-mode technical identifiers receive format- and relationship-preserving aliases scoped to the request/session policy defined by the approved design.

### Mapping lifecycle and fail-closed boundaries

- [x] **PRIV-04**: Reversible mappings remain memory-only, scope-isolated, TTL- and capacity-bounded, parallel-safe, explicitly clearable, and restorable only when their originals came from caller input.
- [x] **PRIV-05**: Strict inbound validation runs after compression and as the final inbound content mutation, blocking protected input before worker dispatch.
- [x] **PRIV-06**: Strict outbound responses are fully buffered and validated before any response headers or body bytes are emitted.
- [x] **PRIV-07**: Bounded privacy receipts make policy application and workflow bypass detectable without exposing mappings or protected values.

### Surface parity and safe operations

- [x] **PRIV-08**: Ollama, OpenAI, and Anthropic routes share the same privacy policy and preserve their native streaming and non-streaming wire formats.
- [x] **PRIV-09**: Ordinary logs, metrics, receipts, traces, health, dashboards, captures, and support bundles never expose mappings or protected values.
- [x] **PRIV-10**: Mapping inspection is available only through a disabled-by-default, localhost-only, authenticated, no-store triage API with safe inspect and clear operations.
- [x] **PRIV-11**: Managed-secret handling and privacy inspection/clear workflows have POSIX and PowerShell parity and do not print secret values.
- [x] **PRIV-12**: Read-only dashboard/About status, operator documentation, Grafana assets, cross-surface conformance tests, security checks, race tests, and benchmarks satisfy the approved release gates.

## Out of Scope

- Workflow parsing, source authentication, workflow-level minimization, routing, receipt enforcement, schema validation, tool/script handling, and final artifact policy remain workflow-engine responsibilities.
- Persistent mapping storage, mappings in ordinary telemetry, reversible credential handling, adapter-specific privacy policy, and a Gateway-wide classification mutex are prohibited by the approved design.
- Dashboard configuration remains read-only.
- Previously deferred reliability micro-batch, lint-cache hygiene, performance-vs-Node baseline, ACP demultiplexing, and Authenticode work are not part of this milestone.

## Traceability

| Requirement | Phase | Plan tasks | Status |
|-------------|-------|------------|--------|
| PRIV-01 | Phase 21 | 1, 5, 8 | Complete |
| PRIV-02 | Phase 21 | 2, 5, 6, 7 | Complete |
| PRIV-03 | Phase 21 | 4, 6, 7 | Complete |
| PRIV-04 | Phase 21 | 3, 4, 13, 14 | Complete |
| PRIV-05 | Phase 21 | 6, 8 | Complete |
| PRIV-06 | Phase 21 | 7, 9, 10, 11 | Complete |
| PRIV-07 | Phase 21 | 6, 7, 8, 17 | Complete |
| PRIV-08 | Phase 21 | 8, 9, 10, 11, 17 | Complete |
| PRIV-09 | Phase 21 | 8, 12, 13, 14, 15, 17 | Complete |
| PRIV-10 | Phase 21 | 13, 15, 17 | Complete |
| PRIV-11 | Phase 21 | 14, 17 | Complete |
| PRIV-12 | Phase 21 | 12, 15, 16, 17 | Complete |

**Coverage:** 12/12 active requirements mapped to Phase 21.
