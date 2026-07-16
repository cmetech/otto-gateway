# LLM prompt — adversarial code review of OTTO gateway verified model capabilities

> Paste everything below the line into a **fresh** coding session running **in the
> `otto-gateway` repo** (`/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/`),
> on a model/agent that did **not** write this feature. It is self-contained. Its job is to
> **try to break and disprove** the feature, not to admire it. Do not paste it into the
> hermes-agent or loop24 chat.

---

You are an adversarial reviewer. A feature was just merged to `main` that adds a
Gateway-owned endpoint, **`GET /v1/model-capabilities`**, which fuses the live Kiro model
catalog (authoritative for *availability*) with an embedded, source-controlled registry
(authoritative for *what has been verified*), using a three-state model
(`supported` / `unsupported` / `unknown`). The whole reason this feature exists is **honesty**:
it must never claim a capability it cannot back with evidence, and "unknown" must stay
unknown. Your job is to assume the author was optimistic, rushed, or subtly wrong, and to
**falsify** their claims with evidence from the real code and the real documentation.

Ground every finding in the source and its `_test.go` files, and in the actual vendor
documentation — **do not assume behavior, and do not take the author's comments, the spec, or
the reference doc as proof of anything.** A passing test proves only what it asserts; read
what it actually asserts.

## What was built (landmarks — verify, don't trust)

- Endpoint + wire render + handler + consumer-defined seam: `internal/adapter/openai/modelcaps.go`,
  route added in `internal/adapter/openai/adapter.go` (`RegisterRoutes`), tests in
  `internal/adapter/openai/modelcaps_test.go`.
- Types (tag-free): `internal/canonical/modelcaps.go`.
- Registry package (embedded JSON, validation, revision hash, live-catalog enrichment):
  `internal/registry/registry.go`, data in `internal/registry/registry.json`, tests in
  `internal/registry/registry_test.go`.
- Wiring (registry loaded at startup, combiner injected): `cmd/otto-gateway/main.go`.
- E2E + fixture knob: `tests/e2e/openai_e2e_test.go`, `tests/e2e/cmd/fake-kiro-cli/main.go`
  (`GW_FAKE_KIRO_MODELS`).
- Architecture boundary: `.go-arch-lint.yml` (a new `registry` component).
- Design intent: `docs/superpowers/specs/2026-07-16-model-capabilities-endpoint-design.md`.
  Client-facing contract: `docs/reference/model_capabilities.md`.

## Primary attack surface — evidence integrity (spend most of your time here)

This is the feature's core promise. **Over-claiming is a Critical defect.** For every
capability in `internal/registry/registry.json` marked `supported` or `unsupported`:

1. **Re-fetch the cited `reference` URL yourself** (WebFetch). The registry cites official
   Anthropic pages — expect `platform.claude.com/docs/en/agents-and-tools/tool-use/overview.md`
   (tools), `.../build-with-claude/vision.md` (vision), and `.../about-claude/models/overview.md`
   (reasoning). If a URL is dead or paywalled, say so and mark those claims **UNVERIFIABLE** —
   do not assume they're fine.
2. **Confirm the fetched page documents THAT capability for THAT EXACT model id**, mapped
   unambiguously from the Kiro id (e.g. `claude-opus-4.8` ↔ Anthropic `claude-opus-4-8`).
   Flag as **over-claim (Critical)** any `supported` state where the page does *not* name the
   model for that capability.
3. **Interrogate the "standard tier / all other models" catch-all.** The author set several
   Claude models' `vision` to `supported` on the strength of a docs row that reads "all other
   models" rather than naming the model. Decide, and argue with evidence, whether that clears
   the spec's bar of *"official documentation unambiguously mapped to the exact Kiro model ID"*
   (spec §8.3) or whether it is an over-claim that should be `unknown`. Do the same for
   `reasoning` claims that rest on **adaptive thinking = Yes** while **extended thinking = No** —
   is "reasoning" honestly supported for those ids?
4. **Hunt for the model the author *should* have left unknown but didn't**, and the inverse.
   The author claims `claude-sonnet-4` was deliberately under-claimed (tools yes; vision +
   reasoning unknown). Verify that is actually the honest call and that no *other* entry got a
   `supported` it hasn't earned. Verify `verified_at` values are real dates in `YYYY-MM-DD`
   form, `source` is always one of `kiro_declared` / `vendor_documentation` / `controlled_probe`,
   and no evidence was fabricated (a plausible-looking URL that does not actually load or does
   not mention the model is a fabrication — treat it as Critical).
5. **Confirm the non-Claude models are honestly unknown.** `gpt-5.6-sol/terra/luna`,
   `deepseek-3.2`, `minimax-m2.5/m2.1`, `glm-5`, `qwen3-coder-next` should be
   `completion: supported` (kiro_declared) with everything else `unknown` and NO
   vendor_documentation evidence. Any capability claim on these is almost certainly a defect.

## Secondary attack surface — validation bypass

The loader (`internal/registry/registry.go`) must reject a bad registry at load time. **Try to
build a `registry.json` that is semantically invalid but loads anyway.** Write throwaway
`_test.go` cases (or feed bytes to the unexported `load`) that attempt each bypass:

- An entry where `evidence` has the right *count* but the wrong *keys* vs the supported/unsupported
  set (does the both-directions check really hold?).
- Evidence attached to a capability whose state is `unknown`.
- A `supported` capability with an evidence object present but an empty `reference`, or a
  `verified_at` that `time.Parse("2006-01-02", …)` would accept but is nonsensical (e.g.
  `2026-13-40` — does Go's parser catch it? confirm).
- Duplicate model ids (the file is a JSON array specifically so duplicates are detectable — is
  the detection actually there, or does a later entry silently win?).
- An entry missing one required capability key but padded with an extra invalid key so the
  count still equals four (which check fires — and is the resulting error honest?).
- An unknown top-level or evidence field (is `DisallowUnknownFields` really on, and does it
  matter?).
- A model id that is empty, whitespace, or differs only in case from a real one.

For each, state whether the loader rejects it and, if it does not, whether that is a real hole.

## Third attack surface — enrichment + determinism + concurrency

`Registry.Enrich(live, now)` fuses the live catalog with the registry. **Feed it adversarial
live catalogs** and assert the result:

- A live entry with id `"Auto"` or `"AUTO"` (not lowercase `"auto"`) — the dedup only drops
  exact `"auto"`. Does a near-miss produce a second automatic-looking entry? Is that a bug or
  acceptable given Kiro only ever emits `"auto"`?
- Duplicate explicit ids in the live catalog; an empty-id entry; a registry id that is NOT in
  the live catalog (must be omitted — verify it cannot leak as "available").
- A registered model — confirm mutating the returned entry's `Capabilities`/`Evidence` maps
  does **not** corrupt the registry's stored maps on a second `Enrich` call (write the test the
  author didn't: the existing no-mutation test only checks the input slice, not returned-map
  aliasing). Confirm the clone is real, not a shared reference.
- **Determinism (spec §6.2):** is the response byte-stable apart from `generated_at`? Confirm
  `capabilities` and `evidence` map ordering is deterministic (it relies on `encoding/json`
  sorting map keys — verify, don't assume), and that `data[]` order follows the live-catalog
  order with `auto` always first. Separately: `registry_revision` hashes the **raw file bytes** —
  prove or disprove that reformatting `registry.json` (whitespace only, identical data) changes
  the revision, and decide whether that is a defect or intended.
- **Concurrency:** `Enrich` runs per request against a shared `*registry.Registry` and a
  per-call `pool.Models()` copy. Run `go test -race` against the openai + registry packages and
  reason about whether the shared registry maps are ever written after `Load`. Prove there is no
  data race, or find one.

## Fourth attack surface — boundary, wiring, auth, leakage, regression

- **TRST-04:** prove `internal/adapter/openai` does not import `internal/registry` or
  `internal/pool` (`go list -deps ./internal/adapter/openai`), and that the seam returns a
  `canonical` type only. Confirm `.go-arch-lint.yml` did not quietly grant `adapter_openai` a
  registry dependency to make the boundary "pass."
- **Wiring:** in `cmd/otto-gateway/main.go`, confirm `registry.Load()` failure is fatal at
  startup (an invalid embedded JSON must not ship as a runtime 500), and that the seam is wired
  **unconditionally** when the OpenAI surface is enabled — so the handler's nil-seam empty-list
  branch is genuinely unreachable in production. If you can reach the nil-seam branch in a real
  run, that is a finding (note it emits empty `registry_revision`/`generated_at`).
- **Auth parity:** confirm the endpoint inherits the **same** middleware as `/v1/models`
  (IP-allowlist only, no bearer) purely by route placement, and that nothing added or removed
  auth. Decide whether exposing a capability catalog under that posture is acceptable or leaks
  more than `/v1/models` already does.
- **Leakage:** the no-leak test is a substring scan and is weak by construction. Independently
  confirm the wire structs expose only `id/name/available/selection_mode/capabilities/evidence`
  (+ evidence `source/reference/verified_at/notes`) and that nothing from pool internals, env,
  worker/slot ids, file paths, or prompts can reach the wire through any field, including
  `name` and evidence `notes`.
- **No regression:** `GET /v1/models` byte-shape unchanged; `/api/tags` and `/v1/models` still
  expose the **same** model-id set; `/api/show` untouched. Confirm the `GW_FAKE_KIRO_MODELS`
  fixture knob returns the *exact* prior default catalog when the env var is unset (so existing
  E2E suites are unperturbed).

## Run the real gates yourself (do not trust a report that says they passed)

The trust-gate tools live in `$(go env GOPATH)/bin`:

```
gofmt -l internal/ cmd/ tests/
go vet ./...
go test ./...
go test -race ./internal/registry/ ./internal/adapter/openai/ ./internal/pool/ ./internal/canonical/
$(go env GOPATH)/bin/golangci-lint run ./...
$(go env GOPATH)/bin/go-arch-lint check --project-path .
$(go env GOPATH)/bin/govulncheck ./...
GW_E2E=1 PII_ENCRYPT_KEY=<any-32-byte-hex> go test -tags e2e ./tests/e2e/ -run ModelCapabilities -v
```

Note: `golangci-lint run ./...` reports **all** issues; CI runs new-issues-only, so a
pre-existing finding is not a branch defect. There are known pre-existing gosec findings in
`cmd/otto-gateway/main.go` device-id file code (~lines 928-937) — these are NOT part of this
feature; do not attribute them to it. Anything *new* that this feature introduced is fair game.

## Explicitly OUT of scope (do not "fix" or flag as defects)

- The `available` field is always `true` for emitted entries (presence in the live catalog *is*
  availability). This is intended per spec §7; it is not a liveness/health flag. Do not flag it
  as a missing feature.
- No request-time probing, no background quota consumer, no live billable prompts. The embedded
  registry is the baseline by design; `controlled_probe` is a reserved future source type, not a
  gap.
- Model normalization / validation of unknown model ids on the chat surfaces is a separate
  product decision — not this feature's concern.

## Deliverable

Report findings ranked most-severe first. For each: **file:line**, a one-line statement of the
defect, and a concrete failure scenario (inputs → wrong output) or, for an evidence over-claim,
the model + capability + the URL you fetched and what it actually says. Classify:

- **Critical** — an over-claim (a `supported` state not backed by the cited page for that exact
  model), a fabricated/dead reference, a validation bypass that admits an invalid registry, a
  TRST-04 boundary breach, a data race, or any internal-data leak on the wire.
- **Important** — a real correctness or honesty gap that should block: a fuzzy/near-miss match,
  a determinism break, a reachable nil-seam path in prod, an auth divergence from `/v1/models`,
  or a regression to `/v1/models` ↔ `/api/tags` ↔ `/api/show`.
- **Minor** — weak test that asserts too little, catch-all-tier evidence you judge defensible
  but thin, docs/wording drift from the code.

End with an explicit verdict: **would you ship this as an honest, auditable capability catalog,
or does it lie somewhere?** If it lies, name exactly where. Do not modify anything outside a
throwaway test file; this is a read-and-verify review, not an implementation task.
