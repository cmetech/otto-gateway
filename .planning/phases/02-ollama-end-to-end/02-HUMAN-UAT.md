---
status: partial
phase: 02-ollama-end-to-end
source: [02-VERIFICATION.md]
started: 2026-05-24T02:24:00Z
updated: 2026-05-24T02:24:00Z
---

## Current Test

[awaiting human testing]

## Tests

### 1. Real-kiro /api/chat round-trip (SC #1)
expected: `LOOP24_INTEGRATION=1 go test -race -count=1 ./internal/adapter/ollama/... -run TestIntegration -v -timeout 60s` — TestIntegration_ChatEndToEnd returns 200 with non-empty assistant message.content sourced from a real kiro-cli subprocess.
why_human: Requires kiro-cli authenticated on the operator's machine (auth token cannot be re-acquired in CI). Verified handler chain end-to-end in unit tests with fakeEngine; only the real-kiro boundary needs a human to flip the LOOP24_INTEGRATION switch and confirm.
result: [pending]

### 2. LangFlow zero-reconfig (SC #2 — load-bearing)
expected: Open an existing LangFlow flow whose Ollama component already points at `http://localhost:11434/api/chat`. Make NO modifications. Run the flow with a simple chat input. The flow completes successfully and the Ollama component renders the chat response.
why_human: Cannot be programmatically verified without a running LangFlow instance — the contract is that LangFlow itself must accept the wire shape with zero reconfiguration. This is the load-bearing Phase 2 acceptance gate per ROADMAP SC #2.
result: [pending]

### 3. Auth posture smoke test against running binary
expected: `AUTH_TOKEN=s3cret ./bin/loop24-gateway` → `curl http://localhost:11434/api/chat -d '{}'` returns 401; `curl -H 'Authorization: Bearer s3cret' …/api/chat` returns 200 (or whatever the body shape yields); `curl http://localhost:11434/api/version` returns 200 without auth; `curl http://localhost:11434/health` returns 200 without auth.
why_human: Programmatic tests assert the middleware contract; this smoke test confirms the wired binary actually honors the contract once HTTP traffic crosses the chi router boundary in the running process.
result: [pending]

## Summary

total: 3
passed: 0
issues: 0
pending: 3
skipped: 0
blocked: 0

## Gaps
