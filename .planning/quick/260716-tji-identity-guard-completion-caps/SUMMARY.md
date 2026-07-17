---
quick_id: 260716-tji
slug: identity-guard-completion-caps
status: complete
date: 2026-07-16
branch: fix/gateway-identity-guard-and-completion-caps
commits:
  - 32d6769 fix(engine): suppress model narration of the identity guard as answer text
  - 0a9b3db fix(registry): mark every live kiro model completion=supported in Enrich
  - 8ea8e13 docs: Hermes provider-registry fix-spec + quick-task plan
---

# Summary — 260716-tji: identity-guard suppression + completion-caps

## What shipped (gateway, this branch)

- **FIX #0 — identity-narration leak** (`internal/engine/build_acp.go`).
  Appended a scoped suppression sentence to `identityGuardClause` forbidding the
  model from restating/quoting/explaining/narrating the identity instructions or
  system context. Scoped to the guard only, so genuine task reasoning is
  untouched. Root cause proven via ACP_CAPTURE: kiro streams all output as
  `agent_message_chunk` (no thinking channel), so the meta-commentary reached the
  client verbatim — not a routing bug.

- **FIX #1 — completion=unknown greying** (`internal/registry/registry.go`).
  `Enrich` now forces `completion=supported` (+ `kiro_declared` evidence) for
  every live model whose completion was still unknown — i.e. live models absent
  from `registry.json`. Aligns code with the doc's own contract
  (`model_capabilities.md:229-232`: live-catalog membership is the evidence for
  completion). Only the unregistered branch changes; tools/vision/reasoning stay
  unknown; `auto` untouched. Updated `registry_test.go` (2 tests) and the
  asymmetric-fusion doc section.

## Deliverable

- `docs/2026-07-16-hermes-provider-registry-fixes.md` — hand-off spec for the
  **Hermes** repo covering Symptom A (`resolve_provider_full` blind to the
  plugin registry → "Unknown provider 'loop24'") and Symptom B (MoA snap-back to
  gpt-5.5). Symptom B is expected to be cleared by FIX #1 once deployed; the spec
  documents the verify step and a `model_eligibility.py` fallback.

## Verification

- `go build ./...`, `go vet` — clean.
- `go test ./...` — **ALL PASS** (full suite).
- `gofmt -l` and `gofumpt -l` (CI format gate) — clean.
- Runtime behavior (leak suppression on live kiro; MoA/greying clear) requires a
  CI build + redeploy to confirm — gateway builds are CI-only.

## Follow-ups / open items

1. **Deploy via CI**, then re-run the ACP_CAPTURE "2+2" check on Sonnet 5 to
   confirm the narration is gone. Note: the leak was stochastic (~1/5), so
   "reduced/absent across several tries" is the realistic success bar.
2. After deploy, **re-test MoA (Symptom B)** — expect it cleared by FIX #1.
3. Apply the **Hermes** spec (Symptom A required regardless; B fallback only if
   it persists).
4. **Deferred, separate:** the `translate.go:400` default case swallows unknown
   ACP discriminators into visible text with no log (and its comment falsely
   claims it logs). Did NOT cause this bug, but worth a Debug log for
   observability.
5. **Upstream question:** kiro never emitted a single `agent_thought_chunk` in
   the capture — reasoning is not delivered as a separate channel. If reasoning-
   as-reasoning is wanted, that's a kiro-side capability to confirm, independent
   of this work.
