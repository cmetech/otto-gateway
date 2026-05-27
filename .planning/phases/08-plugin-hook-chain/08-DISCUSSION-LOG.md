# Phase 8: Plugin Hook Chain - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-27
**Phase:** 8-Plugin Hook Chain
**Areas discussed:** Registration architecture, PII content-part scope, LoggingHook ordering vs PII redaction, Hash mode key management

---

## Pre-discussion context

User invoked `/gsd-discuss-phase 8` after iterating on Phase 8's ROADMAP entry
across the same session. Prior turns added the PIIRedactionHook (SC6), then
added `/health/hooks` view-only introspection (SC7) and explicitly closed off
runtime hook management. Carrying forward (locked, not asked):

- Hook seam interfaces (`PreHook.Before`, `PostHook.After`) shipped Phase 2 D-04.
- Hot-reload / dynamic plugin registration OUT OF SCOPE (PROJECT.md line 108).
- Six PII recognizers locked (Email, IPv4, IPv6, SSN, CC+Luhn, US Phone).
- Env knobs locked: `PII_REDACTION_ENABLED`, `PII_ENABLED_ENTITIES`, `PII_REDACTION_MODE`.
- Pure-Go, no cgo, no external deps (CLAUDE.md).
- AuthHook refactors from `internal/auth/bearer.go`.
- `goleak` + property tests (TRST-05/06).

User confirmed all four offered gray areas for discussion (single multi-select turn).

---

## Registration architecture

### Round 1: Where to register

| Option | Description | Selected |
|--------|-------------|----------|
| Hardcoded in main.go (Recommended) | `cmd/otto-gateway/main.go` calls `plugin.Chain{&RequestIDHook{}, ...}`. Same pattern for PII Recognizers. Bifrost-style. Simplest. | ✓ |
| Central registry package | `internal/plugin/registry.go` exposes `Register(name, factory)`. More indirection. | |
| Per-hook self-registration via init() | Each hook's `init()` calls `plugin.Register()`. Most extensible but init-order surprises. | |

**User's choice:** Hardcoded in main.go.
**Notes:** None — accepted the recommended option directly. Drives D-01 in CONTEXT.md.

### Round 2: `ENABLED_HOOKS` semantics

| Option | Description | Selected |
|--------|-------------|----------|
| Allowlist; empty = all enabled (Recommended) | Unset → all run. Set → only listed run. Unknown name = boot error. Matches AUTH_TOKEN permissiveness. | ✓ |
| Allowlist; empty = none enabled | Defaults to NO hooks; operator must opt in. Safer fresh-deploy but loud for the common case. | |
| Denylist; empty = all enabled | Set name to DISABLE that one hook. Mixing with PII_ENABLED_ENTITIES (allowlist) is confusing. | |

**User's choice:** Allowlist; empty = all enabled.
**Notes:** None. Drives D-02 in CONTEXT.md. The "unknown name = boot error" sub-rule is locked as load-bearing typo protection.

---

## PII content-part scope

### Round 1: Where the walker looks

| Option | Description | Selected |
|--------|-------------|----------|
| Text only (SC6 literal) | Walk `Message.ContentParts[].Text` only. Cheapest. Risk: tool args with PII bypass redaction. | |
| Text + tool_use.Input + tool_result.Content (Recommended) | Walk all three. Recursive on JSON; string leaves only. Modest cost. | ✓ |
| Text + tool_use.Input only (skip tool_result) | Compromise: scrub inbound payloads but not tool-execution outputs. | |

**User's choice:** Text + tool_use.Input + tool_result.Content.
**Notes:** None. Drives D-03 in CONTEXT.md. Recursive walker semantics (string leaves only; map keys and non-string leaves untouched) added as implementation detail.

---

## LoggingHook ordering vs PII redaction

### Round 1: Log content visibility

| Option | Description | Selected |
|--------|-------------|----------|
| After PII (PII runs first, log second) (Recommended) | Chain: RequestID → Auth → PIIRedaction → Logging. Logs contain redacted tokens. SIEM-safe. | ✓ |
| Before PII (log first, then PII) | Maximum debuggability but compliance-hostile; logs become a PII store. | |
| Both — separate raw-log toggle | Default safe; opt-in `LOG_RAW_REQUEST=true` for debugging. Adds a knob and a foot-gun. | |

**User's choice:** After PII (privacy-preserving order).
**Notes:** None. Drives D-04 in CONTEXT.md. The `LOG_RAW_REQUEST` escape hatch was explicitly rejected by omission — for debug needs, run with `PII_REDACTION_ENABLED=false` and accept the leak risk for that session.

---

## Hash mode key management

### Round 1: Hash keying source

| Option | Description | Selected |
|--------|-------------|----------|
| PII_HASH_KEY env var (HMAC-SHA256) (Recommended) | Operator key. Same email → same hash across requests/restarts as long as key is stable. mode=hash + no key = boot error. | ✓ |
| Per-process random key | Random 32-byte key at startup; never persisted. Correlation within one process only. | |
| Unkeyed SHA256 | Deterministic but rainbow-table-trivial; attacker with logs can brute-force common values. | |

**User's choice:** PII_HASH_KEY env var with HMAC-SHA256.
**Notes:** None. Drives D-05 in CONTEXT.md. The "no silent unkeyed fallback" rule is locked — gateway refuses to start if `PII_REDACTION_MODE=hash` and `PII_HASH_KEY` is empty. Key-rotation-as-feature ("rotate to invalidate prior correlations on suspected leak") elevated to a documented operational pattern.

---

## Wrap-up

| Option | Description | Selected |
|--------|-------------|----------|
| I'm ready for context | Write CONTEXT.md with the five decisions; smaller details to Claude's discretion. | ✓ |
| Explore more gray areas | Surface 2-4 more candidates (counter-suffix scope, `/health/hooks` config exposure rules, X-Request-Id format, auth error envelope). | |

**User's choice:** Ready for context.
**Notes:** Smaller gray areas were folded into "Claude's Discretion" with planner guidance: counter-suffix recommended per-request, `/health/hooks` config exposure via per-hook `Describe()` method, X-Request-Id format defaults to ULID, AuthHook short-circuit error rendering stays on the adapter side.

---

## Claude's Discretion (deferred to planner/researcher)

- `Recognizer` struct extensibility (additional fields beyond `{Name, Pattern, Validate}`)
- Counter-suffix scope (per-request vs per-message — recommendation: per-request)
- Whether LoggingHook ships the redaction-summary structured field in v1 (API seam mandatory; ship-or-defer open)
- `/health/hooks` config field exposure rules (per-hook `Describe()` method; non-secret fields only)
- `X-Request-Id` format (ULID recommended; UUID-v4 and nanoid also defensible)
- AuthHook short-circuit error envelope rendering (canonical error response; adapter renders per-surface — Phase 3.1 errors.go precedent)
- Typed `Chain` struct exposing `Pre []PreHook` + `Post []PostHook` vs bare slices (typed recommended for `/health/hooks` introspection)
- `ENABLED_HOOKS` filter helper placement (`internal/plugin.Filter(...)` vs inline in main.go)

## Deferred Ideas

- Admin API for runtime hook management (PROJECT.md OOS + SC7 reinforces).
- Content-moderation hook (PLUG-V2-01 — needs external API, different ops model).
- Schema-validation hook (PLUG-V2-02 — defer until real failure mode).
- Budget / rate-limit hook (PLUG-V2-03 — needs durable state).
- Semantic-cache hook (PLUG-V2-04 — depends on Phase 7 fate).
- Audit-log hook (PLUG-V2-05 — defer until compliance ask).
- NER-based PII (prose/v2 or Presidio sidecar — v2 if regex recall complaints).
- Per-request hook bypass headers (`X-Skip-PII` — guardrail defeat primitive; intentionally never).
- Hook ordering customization via env (RequestID-first and PII-before-Logging are architectural contracts).
- Output-side PII redaction (PostHook on response — v1 input-only per user; track for v2).
- OpenAI-style function-args field-name awareness (brittle; regex IS the policy).
- Per-recognizer cost tracking metrics (operational nice-to-have).
- Per-recognizer `Anonymize` override (v1 uses global `PII_REDACTION_MODE` uniformly).
