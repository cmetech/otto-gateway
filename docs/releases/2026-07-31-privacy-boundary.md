# Privacy boundary service

Gateway now offers a profile-aware privacy boundary on every Ollama, OpenAI,
and Anthropic chat surface. The existing `PIIRedactionHook` name and `standard`
PII behavior remain compatible. The new `strict` profile adds one-way secret
handling, scope-stable technical pseudonyms, independent residual scans,
complete output validation, and a bounded response receipt.

## Operator impact

- The generated environment gains `PRIVACY_DEFAULT_PROFILE`,
  `PRIVACY_REQUEST_PROFILES`, `PRIVACY_ALIAS_KEY`, `PRIVACY_SECRET_ACTION`,
  `PRIVACY_TECHNICAL_ACTION`, `PRIVACY_SCOPE_TTL`, `PRIVACY_MAX_SCOPES`,
  `PRIVACY_MAX_ENTRIES_PER_SCOPE`, `PRIVACY_MAX_TOTAL_ENTRIES`,
  `PRIVACY_TRIAGE_ENABLED`, and `PRIVACY_TRIAGE_TOKEN`.
- Normal initialization and upgrades preserve all managed secrets. Explicit
  regeneration rotates the privacy alias key and causes in-memory mapping loss.
- Every configuration change requires a Gateway restart. Restart also clears
  all memory-only scopes and mappings.
- The compiled and generated default for `PII_NER_ENABLED` is now consistently
  `true`; operators can still explicitly disable its English PERSON/LOCATION
  recognizer.
- Grafana adds bounded privacy coverage, enforcement, latency, capacity,
  receipt, triage, and internal-error reporting with alert indicators.

## Workflow impact

Strict workflows send `X-GW-Privacy-Profile: strict` and propagate one opaque
`X-GW-Privacy-Scope` value through every model call in a workflow run. They
must reject a missing, malformed, non-strict, non-full, or non-pass
`X-GW-Privacy-Receipt`. Calling an ACP worker directly bypasses the boundary.

Strict responses are fully buffered and validated before any headers or body
bytes are released. Streaming callers retain the native surface framing, but
the first response byte is delayed until validation succeeds. Failures retain
the surface's native error envelope and expose only bounded stable privacy
codes.

See the [privacy boundary operations guide](../privacy-boundary.md) for exact
configuration defaults, safe workflow integration, local triage, metrics,
troubleshooting, and rollback requirements.
