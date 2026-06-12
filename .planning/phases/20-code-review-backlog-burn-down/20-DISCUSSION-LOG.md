# Phase 20: Code-review backlog burn-down - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-06-12
**Phase:** 20-code-review-backlog-burn-down
**Areas discussed:** QUAL-01 escape scope, QUAL-03 forceCloseCh fix, commit granularity, QUAL-01 tests

---

## QUAL-01 — escapeApplescript escape scope

| Option | Description | Selected |
|--------|-------------|----------|
| Strip all C0 + DEL, keep \n\r\t as AS escapes | Replace \n→'\n', \r→'\r', \t→'\t' (AppleScript-recognized escapes); drop other 0x00-0x1F and 0x7F. Cleanest and safest — anything else in a notification body is noise anyway. | ✓ |
| Escape only newlines + tab + CR | Only handle \n \r \t plus the existing " and \ . Other control chars pass through. Smaller diff, but leaves the door open for weird operator-controlled strings. | |
| Strip ALL non-printable (including \n\r\t) | Drop everything below 0x20 and 0x7F. Most defensive — but means multi-line stderr in failure dialogs becomes single-line. | |

**User's choice:** Strip all C0 + DEL, keep \n\r\t as AS escapes
**Notes:** Preserves readable multi-line stderr in failure dialogs while removing every other non-printable byte that has no business reaching `osascript`. Encoded as D-20-01 + D-20-02 in CONTEXT.md (test file added because the function backs a `//nolint:gosec G204`).

---

## QUAL-03 — forceCloseCh contract fix

| Option | Description | Selected |
|--------|-------------|----------|
| Relocate allocation to RunUntilSignal; nil select-arm | Remove allocation from New()/NewWithCommit(). RunUntilSignal allocates and assigns s.forceCloseCh before calling s.Run(). Run()-only callers see nil → the select arm never fires (idiomatic Go). Smallest behavioral footprint, removes the dead arm meaningfully. | ✓ |
| Document the current contract; no code change | Add comment on the field explaining 'allocated unconditionally so Run() can select on it; only signaled by RunUntilSignal'. Zero risk of breaking anything; doesn't actually remove the dead arm. | |
| Pass forceCloseCh into Run() as a parameter | Change Run signature to accept forceCloseCh chan struct{}. Most explicit, but changes a public method signature for an Info-level finding — probably overkill. | |

**User's choice:** Relocate allocation to RunUntilSignal; nil select-arm
**Notes:** Uses Go's nil-channel select-never idiom — direct `Run` callers leave the channel nil, the existing select arm in `Run` never fires, and `Run`'s signature stays unchanged. Encoded as D-20-04 + D-20-05 in CONTEXT.md.

---

## Commit granularity

| Option | Description | Selected |
|--------|-------------|----------|
| One atomic commit per QUAL (6 commits) | refactor(20): close QUAL-01 ... close QUAL-06. Matches the per-requirement commit pattern visible in recent phases (Phase 19 commits). Easiest to revert individually. | ✓ |
| Grouped by file/area (~3 commits) | Group QUAL-01+QUAL-02 (otto-tray uihelpers), QUAL-04 (tray.go), QUAL-05+QUAL-06 (pool tests), QUAL-03 (server.go). Fewer commits, but mixes requirement closures. | |
| Single squashed commit | One commit closing all 6. Smallest log noise, but loses per-finding traceability. | |

**User's choice:** One atomic commit per QUAL (6 commits)
**Notes:** Matches established project pattern (Phases 15–19). Encoded as D-20-09 in CONTEXT.md with subject form `refactor(20): close QUAL-XX — <one-line summary>`.

---

## QUAL-01 — tests

| Option | Description | Selected |
|--------|-------------|----------|
| Add escapeApplescript_test.go with table-driven cases | Unit-test the escape function directly: empty, plain, embedded quote, embedded backslash, \n/\r/\t, raw control byte. It's a small security-adjacent function backing a //nolint:gosec G204 — worth a unit test. | ✓ |
| No new tests; refactor-only stance | Roadmap calls this 'refactor-only; no behavior change' even though QUAL-01 technically expands what gets escaped. Lean on existing notify/dialog call-site coverage. | |

**User's choice:** Add escapeApplescript_test.go with table-driven cases
**Notes:** QUAL-01 is the only finding in this batch with an actual behavioral change. The function suppresses a `gosec G204` warning, so direct unit coverage is worth the small file. Encoded as D-20-02 in CONTEXT.md.

---

## Claude's Discretion

- Exact wording of the updated field-level comment on `forceCloseCh` (semantics locked by D-20-05, prose is not).
- Exact wording of the rewritten `removeSlot` comment in QUAL-06 (describe-or-delete at planner/executor discretion).
- File name for the QUAL-01 test (standalone `escapeApplescript_darwin_test.go` vs folding into an existing `*_darwin_test.go` if one exists at plan time).

## Deferred Ideas

None — discussion stayed within the 6 named QUAL findings.
