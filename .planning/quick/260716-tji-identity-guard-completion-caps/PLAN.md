---
quick_id: 260716-tji
slug: identity-guard-completion-caps
status: in-progress
date: 2026-07-16
branch: fix/gateway-identity-guard-and-completion-caps
---

# Quick Task 260716-tji: Identity-guard suppression + completion-caps fix

Two gateway bug fixes found during a model-selection debugging session
(root-caused via ACP_CAPTURE raw-frame evidence), plus a written Hermes
fix-spec deliverable for a separate repo.

## Root causes (confirmed)

- **FIX #0 — identity-narration leak.** On some turns the model emits its
  reconciliation of the `[System]` identity instructions as visible answer
  text ("I need to identify myself according to the system context… 2+2 = 4").
  ACP_CAPTURE proved kiro streams the entire output — deliberation and answer —
  as `agent_message_chunk`; there is NO separate thinking channel, so any
  meta-commentary reaches the client verbatim. Not a routing bug. The only
  lever is the prompt wording in `identityGuardClause`.

- **FIX #1 — completion=unknown greying.** `(*Registry).Enrich` leaves
  `completion=unknown` for any live kiro model whose exact ID is absent from
  `registry.json` (unregistered `else` branch keeps `allUnknown()`). Hermes
  greys those out and 409s MoA selection. But the gateway's own doc
  (`model_capabilities.md:229-232`) states live-catalog membership IS the
  evidence for completion. Fix aligns code with that contract.

## Tasks

1. **build_acp.go** — append a scoped suppression sentence to
   `identityGuardClause` (keep all anti-Kiro content; scope strictly to "these
   identity instructions / this system context" so task reasoning is untouched);
   update the constant's doc comment. No test change — the guard tests assert
   against the `identityGuardClause` variable, and the new sentence contains no
   banned tokens (`<`, `>`, `OTTO`, `LOOP24`).

2. **registry.go `Enrich`** — after the register/unregister branch, force
   `completion=CapSupported` + `kiro_declared` evidence for any live model whose
   completion is still `CapUnknown` (only the unregistered branch is affected;
   registered entries already declare supported; `auto` untouched; tools/vision/
   reasoning stay unknown).

3. **registry_test.go** — update `TestEnrich_UnregisteredAllUnknown`
   (→ completion supported + evidence, others unknown) and
   `TestEnrich_ExactMatchOnly` (assert no fuzzy inheritance of m1's
   vision=unsupported; completion supported via live rule).

4. **docs/reference/model_capabilities.md:116-123** — restate the asymmetric
   fusion rule: live-but-unregistered ⇒ completion supported (kiro_declared),
   other three unknown. Repoint the policy cross-reference to the registry test.
   (The openai adapter `TestModelCapabilities_UnknownModel` uses a fake catalog
   and remains a valid serialization test — `auto` still covers all-unknown — so
   it is left as-is.)

5. **HERMES-FIXES.md** (deliverable, targets separate repo otto_hermes/hermes-agent)
   — Symptom A (resolve_provider_full blind to plugin registry) + Symptom B
   fallback (relax eligibility gate if #1 alone doesn't clear it).

## Constraints
- Gateway builds are CI-only: no `make build/package`. `go test`/`go vet` OK.
- Work on branch `fix/gateway-identity-guard-and-completion-caps`; no push/tag.
- Atomic commits per logical fix.
