# OTTO Gateway Reliability Audit — Fix Prompt

Paste the prompt below into a fresh Claude Code session in the otto-gateway
repo to drive the fixes for findings in
`.planning/audit/PRODUCTION-RELIABILITY-AUDIT.md`.

---

```
Apply the reliability fixes from .planning/audit/PRODUCTION-RELIABILITY-AUDIT.md.

CONTEXT:
- Project: OTTO Gateway — Go-based LLM gateway (Go 1.23+, stdlib net/http + chi,
  log/slog, no cgo). See CLAUDE.md and .planning/PROJECT.md for conventions.
- Deployment posture: single-user laptop. No SRE, no supervisor — crashes mean
  manual restart. Crash-free + leak-free matters more than scale.
- The audit was produced by 12 parallel finders + 3 adversarial verifiers per
  finding. Each surviving finding includes: location (file:line), category,
  scenario, current behavior, recommended fix, complexity.

SCOPE — work the report in this order, stopping after each severity tier for me
to confirm before moving to the next:
  1. CRITICAL  (1 finding)
  2. HIGH      (6 findings)
  3. MEDIUM    (9 findings)
  4. LOW       (19 findings — batch these; one commit per related cluster)

START with the Top 5 list at the bottom of the report (those are the
laptop-launch blockers regardless of severity tier), then sweep remaining
findings in the order above.

PROCESS per finding:
  1. Read the cited file:line and the surrounding code. Verify the scenario
     still matches the current code — if the finding is stale (already fixed,
     code moved, premise no longer holds), say so and skip; do NOT invent work.
  2. Apply the recommended_fix. Prefer the smallest change that closes the
     failure mode. Don't refactor surrounding code or add unrelated hardening.
  3. If the fix touches concurrency (goroutine, channel, mutex, context),
     reason out loud about the new code's exit paths, panic surfaces, and
     race posture before writing.
  4. Verify locally before committing:
       - go build ./...
       - go vet ./...
       - go test -race ./<package>/...   (the package you touched, minimum)
       - If the finding has a reproducible scenario, write a test that fails
         before your change and passes after. For race fixes, a -race test is
         the only credible verification.
  5. Commit atomically with a message like:
       fix(<area>): <one-line>
       Closes <finding-id> from .planning/audit/PRODUCTION-RELIABILITY-AUDIT.md
       <2–3 line "why" — scenario + fix mechanism>
  6. After each commit, paste the finding id, the files changed, and the test
     command you ran. Then move to the next.

CALIBRATION:
  - If a finding's recommended_fix conflicts with an architectural decision in
    .planning/PROJECT.md or docs/briefs/go_port_brief.md, STOP and surface the
    conflict — do not just apply the fix.
  - If the recommended_fix is vague ("add a timeout"), pick a concrete value
    and justify it in the commit message rather than guessing silently.
  - Documented v1 carve-outs (/admin auth-exempt, Ollama list-stubs bypass
    AuthHook) are intentional — never "fix" them.
  - Keep trust gates green: `make ci` (or the project's equivalent) must still
    pass after each commit.

DO NOT:
  - Bundle unrelated fixes into one commit.
  - Skip the -race test on concurrency changes.
  - Add backwards-compat shims for code only you wrote in this run.
  - Touch files outside the finding's cited area unless the fix genuinely
    requires it (and justify in the commit).

Begin with the CRITICAL finding. After committing it, pause and summarize
before starting the HIGH tier.
```

---

## Tuning notes

- **Faster pass:** drop the per-tier pause and let it run through CRITICAL+HIGH
  in one go (7 commits is manageable to review).
- **Existing skill first:** try `/gsd-audit-fix .planning/audit/PRODUCTION-RELIABILITY-AUDIT.md`
  before falling back to this prompt — that skill likely already encodes most of this flow.
- **LOW tier:** the 19 LOW findings are good `/gsd-fast` material; batch them
  in one cleanup pass once Critical+High+Medium are landed rather than driving
  them through this prompt.
