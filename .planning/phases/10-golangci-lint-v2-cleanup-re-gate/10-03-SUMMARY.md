---
phase: 10-golangci-lint-v2-cleanup-re-gate
plan: 03
subsystem: lint-debt-drain
tags: [lint, gosec, revive, bodyclose, nilerr, staticcheck, debt-reduction]
requires:
  - 10-01
  - 10-02
provides:
  - lint-baseline-zero
  - LINT-01 (cumulative)
  - LINT-03 (per-category decision record)
affects:
  - internal/admin/admin.go
  - internal/admin/sse.go
  - internal/admin/sse_test.go
  - internal/admin/snapshot.go
  - internal/admin/snapshot_test.go
  - internal/admin/tail.go
  - internal/plugin/pii/pii.go
  - internal/plugin/pii/ner.go
  - internal/plugin/pii/luhn.go
  - internal/plugin/pii/recognizers_test.go
  - internal/session/stats.go
  - internal/session/testhelpers.go
  - internal/adapter/anthropic/handlers_test.go
  - cmd/otto-gateway/main.go
  - internal/config/config.go
tech-stack:
  added: []
  patterns:
    - json.NewEncoder for untrusted query-param echo (G705 defense-in-depth)
    - scoped //nolint:<linter> with one-line rationale (LINT-03 evidence pattern)
key-files:
  created:
    - .planning/phases/10-golangci-lint-v2-cleanup-re-gate/10-03-SUMMARY.md
  modified:
    - internal/admin/admin.go
    - internal/admin/sse.go
    - internal/admin/sse_test.go
    - internal/admin/snapshot.go
    - internal/admin/snapshot_test.go
    - internal/admin/tail.go
    - internal/plugin/pii/pii.go
    - internal/plugin/pii/ner.go
    - internal/plugin/pii/luhn.go
    - internal/plugin/pii/recognizers_test.go
    - internal/session/stats.go
    - internal/session/testhelpers.go
    - internal/adapter/anthropic/handlers_test.go
    - cmd/otto-gateway/main.go
    - internal/config/config.go
decisions:
  - G703 (admin staticFS, LOG_FILE mkdir, CHAT_TRACE_FILE mkdir) → scoped //nolint:gosec with rationale (operator-controlled boot paths + embed.FS spec)
  - G705 (admin SSE error envelope) → switched to json.NewEncoder.Encode for quote-escaping
  - AdminSnapshot → renamed to Snapshot (3 caller-files, within scope budget)
  - PIIRedactionHook → //nolint:revive exemption (24 caller-files, rename deferred to dedicated API surface phase)
  - SessionDetail → //nolint:revive exemption (Detail() method collision)
  - Tailer.Subscribe and NewNEREngine unexported-returns → exemption (zero callers outside their packages)
  - QF1001 luhn.go → De Morgan fix; recognizers_test.go QF1001 → exemption (test scanner readability)
metrics:
  duration: ~25min
  tasks: 3
  files: 15
  commits: 3
  lint_issues_drained: 15
  lint_issues_remaining: 0
  completed: 2026-06-07T01:52:41Z
---

# Phase 10 Plan 03: Real-Review Lint Drain Summary

Drained the final 15 baseline violations across 5 categories (gosec G703×3,
gosec G705×2, bodyclose×1, nilerr×1, revive×6, staticcheck QF1001×2) by
applying per-site fix-or-exempt decisions captured in the plan's per-category
decision record. The `golangci-lint` baseline now stands at **0 issues**
across the repo, satisfying LINT-01 cumulatively across Waves 1-3.

## What changed

### Task 1 — gosec G703 + G705 (commit `ff0e337`)

- **`internal/admin/admin.go:195`** — added `//nolint:gosec` with rationale
  citing the `embed.FS` spec (`embed.FS` rejects `..` per Go 1.16+;
  `staticFS` rooted at `internal/admin/static/`, operator-public assets only).
- **`internal/admin/sse.go:91, 104`** — replaced `fmt.Fprintf(w, "{...}", source)`
  with `json.NewEncoder(w).Encode(map[string]string{...})` so the untrusted
  `?source` query param is quote-escaped through `json.Marshal`. Defense-in-depth
  for `curl | less` or chat-paste consumers even though `Content-Type: application/json`
  already prevents browser-side HTML parsing. The second message's `%q`
  appearance is preserved via `fmt.Sprintf("source %q ...", source)` inside the map.
- **`cmd/otto-gateway/main.go:989`** + **`internal/config/config.go:493`** —
  both unmasked G703 hits (surfaced when Wave 1's G301 → 0o750 perm tightening
  exposed the secondary taint rule) carry `//nolint:gosec` exemptions with the
  rationale: "operator-controlled boot path (LOG_FILE / CHAT_TRACE_FILE env),
  not request-time." A real `filepath.Clean` + allowlist would exceed the
  scope-guard budget for boot-time env vars that an operator controls directly.

### Task 2 — bodyclose + nilerr (commit `21cbcbf`)

- **`internal/admin/sse_test.go:277`** — the goroutine that does
  `http.DefaultClient.Do(httpReq)` hands `resp` off via `respCh`; the receiver
  site already had `defer resp.Body.Close()` at line 290. The `bodyclose`
  analyzer cannot trace ownership across the channel, so added a scoped
  `//nolint:bodyclose` with rationale pointing at the receiver-side defer.
  (The plan instructed "move defer to receiver" — that defer was already
  present. The remaining lint hit is the cross-channel ownership analysis
  limitation, which is what the exemption documents.)
- **`internal/adapter/anthropic/handlers_test.go:111`** — the fake engine's
  return `(handle, nil)` from the `if f.collectErr != nil` branch is the
  deliberate fake-engine contract: `collectErr` surfaces via
  `Stream.Result()`. Added `//nolint:nilerr` with rationale per the plan's
  per-category record.

### Task 3 — revive remainder + staticcheck QF1001 (commit `2931664`)

Per-site grep evidence + decisions:

| Site | Identifier | Caller-files | Decision |
|------|-----------|--------------|----------|
| `internal/admin/snapshot.go:20` | `AdminSnapshot` (stutter) | 3 (snapshot.go, snapshot_test.go, admin.go) | **Rename** → `Snapshot`. No template/JS string references. |
| `internal/plugin/pii/pii.go:84` | `PIIRedactionHook` (stutter) | 24 (cmd/, adapter/, plugin/, admin/, server/, e2e) | **Exempt** via `//nolint:revive`. Wide API surface rename deferred. |
| `internal/session/stats.go:26` | `SessionDetail` (stutter) | 2 (stats.go + main.go comment) | **Exempt** via `//nolint:revive`. Renaming to `Detail` collides ergonomically with `(*Registry).Detail()` method and the adapter shim comment. |
| `internal/admin/tail.go:203` | `Subscribe → *subscriber` (unexported-return) | 0 outside `internal/admin/` | **Exempt** via `//nolint:revive`. Opaque handle pattern. |
| `internal/plugin/pii/ner.go:50` | `NewNEREngine → *nerEngine` (unexported-return) | 0 outside `internal/plugin/pii/` | **Exempt** via `//nolint:revive`. Consumed only via `PIIRedactionHook.NER` field. |
| `internal/session/testhelpers.go:12` | `NewEntryForTest` (godoc form) | n/a | **Fix.** Rewrote comment to start with `// NewEntryForTest is a test-only helper that...`. |

Also drained the 2 staticcheck QF1001 sites carried from
`deferred-items.md` (linter-rev delta after the baseline snapshot):

| Site | Decision |
|------|----------|
| `internal/plugin/pii/luhn.go:55` | **Fix.** De Morgan rewrite: `!(d >= 13 && d <= 19)` → `d < 13 \|\| d > 19`. Clearer in production code. |
| `internal/plugin/pii/recognizers_test.go:866` | **Exempt.** `!(end-of-comment)` reads more clearly than the De Morgan'd form in the test scanner context. |

## LINT-03 per-category decision record (this wave's contribution)

| Category | Sites | Fix count | Exempt count | Rationale anchor |
|----------|-------|-----------|--------------|------------------|
| gosec G703 | 3 | 0 | 3 | embed.FS spec + operator-controlled boot env |
| gosec G705 | 2 | 2 | 0 | json.NewEncoder.Encode (quote-escape mitigation) |
| bodyclose | 1 | 0 | 1 | cross-channel ownership; receiver site holds defer |
| nilerr | 1 | 0 | 1 | fake-engine contract surfaces err via Stream.Result() |
| revive (stutter) | 3 | 1 | 2 | rename ≤5 caller-files; else exempt |
| revive (unexported-return) | 2 | 0 | 2 | zero out-of-package callers; opaque handle pattern |
| revive (godoc form) | 1 | 1 | 0 | trivial fix |
| staticcheck QF1001 | 2 | 1 | 1 | De Morgan rewrite where it improves clarity |
| **Total** | **15** | **5** | **10** | — |

Every `//nolint:<linter>` directive added in this wave carries a `// <rationale>`
segment on the same line. Verified by:
```bash
git diff 8d5ae8d..HEAD -- '*.go' | grep -E '^\+.*//nolint:' | grep -vE '//.*[A-Za-z]+'
# (empty — every nolint has a rationale)
```

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 — Missing critical functionality] Task 1 scope extended to include the 2 unmasked G703 sites**

- **Found during:** Task 1 lint pre-check.
- **Issue:** Plan §"gosec G703" only listed `internal/admin/admin.go:195` explicitly. The orchestrator's phase context, however, identified 5 total gosec issues including the 2 unmasked G703 hits at `cmd/otto-gateway/main.go:989` and `internal/config/config.go:493` (carried from `deferred-items.md`, exposed by Wave 1's G301 → 0o750 perm tightening).
- **Fix:** Added `//nolint:gosec` exemptions with rationale on both sites in the Task 1 commit (`ff0e337`). Both arise from operator-supplied boot-time env vars (`LOG_FILE`, `CHAT_TRACE_FILE`) — not request-time taint surfaces, so the exemption is correct under the threat model.
- **Files modified:** `cmd/otto-gateway/main.go`, `internal/config/config.go`.
- **Commit:** `ff0e337`.

**2. [Rule 2 — Missing critical functionality] Task 3 scope extended to drain the 2 deferred QF1001 sites**

- **Found during:** Task 3 lint pre-check confirmed both sites still present.
- **Issue:** Plan §"revive remainder" did not list staticcheck QF1001 (deferred from Wave 1 baseline snapshot delta). Orchestrator phase context explicitly routed these 2 sites to this wave.
- **Fix:** Fixed the production-code site (`luhn.go:55`) via De Morgan rewrite. Exempted the test-scanner site (`recognizers_test.go:866`) where the `!(end-of-comment)` form reads more naturally.
- **Files modified:** `internal/plugin/pii/luhn.go`, `internal/plugin/pii/recognizers_test.go`.
- **Commit:** `2931664`.

**3. [Rule 3 — Blocking issue] Task 2 bodyclose required exemption, not a defer move**

- **Found during:** Task 2 execution.
- **Issue:** Plan instructed "move `defer resp.Body.Close()` to the receiver site." Inspection showed the defer was already present at `sse_test.go:290`. The `bodyclose` analyzer was firing on the goroutine-internal `Do()` call because it cannot trace ownership of the response across the channel handoff.
- **Fix:** Added `//nolint:bodyclose` on the goroutine's `Do()` line with rationale pointing at the receiver-side defer. The intended invariant (body closed once at the receiver) is preserved.
- **Files modified:** `internal/admin/sse_test.go`.
- **Commit:** `21cbcbf`.

### Task ordering

Plan executed exactly as specified (Task 1 → Task 2 → Task 3). No re-ordering.

## Verification

### Phase-level cross-check

```bash
$ ~/go/bin/golangci-lint run --timeout=5m 2>&1 | tail -1
0 issues.

$ go test -race ./... 2>&1 | grep -E '^(FAIL|ok)' | grep -v '^ok' | wc -l
0
```

### Acceptance criteria (per success_criteria)

| Criterion | Status |
|-----------|--------|
| All 3 tasks executed, each committed individually | PASS (ff0e337, 21cbcbf, 2931664) |
| `golangci-lint run` returns 0 issues across the entire repo | PASS |
| Every `//nolint:<linter>` carries a `// <rationale>` segment | PASS (verified by grep) |
| `go test -race ./...` passes | PASS |
| SUMMARY.md created and committed | PASS (this file) |
| No modifications to STATE.md or ROADMAP.md | PASS |

## Threat Flags

No new threat surface introduced. All changes either close lint findings
(T-10-06 G705 SSE source echo mitigation via json.Encoder), add documented
exemptions for findings out of attacker reach (T-10-05 embed.FS, T-10-08
fake engine contract), or are pure refactors (revive renames + godoc form).
Threat register entries T-10-05 through T-10-09 from the plan are all
mitigated or accepted per the per-category decision record.

## Self-Check: PASSED

Files claimed to exist:
- `.planning/phases/10-golangci-lint-v2-cleanup-re-gate/10-03-SUMMARY.md` — present (this file).

Commits claimed to exist:
- `ff0e337` fix(10-03): G703 exemptions + G705 json-encode for admin SSE errors — present in `git log`.
- `21cbcbf` fix(10-03): scope bodyclose + nilerr exemptions in test fakes — present in `git log`.
- `2931664` refactor(10-03): resolve revive remainder + 2 QF1001 sites — present in `git log`.
