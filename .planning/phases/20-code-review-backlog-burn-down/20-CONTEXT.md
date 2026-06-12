# Phase 20: Code-review backlog burn-down - Context

**Gathered:** 2026-06-12
**Status:** Ready for planning

<domain>
## Phase Boundary

Single-plan mechanical batch closing the 6 Info-level findings deferred from the Phase 16 and Phase 17 code reviews. **Refactor-only**, with one narrow behavioral expansion (QUAL-01 expands the AppleScript escape set). No new features, no API changes, no performance work.

Findings closed:
- **QUAL-01** — `escapeApplescript` (`cmd/otto-tray/uihelpers_darwin.go`): expand escape set
- **QUAL-02** — `tooltipForState` build-tag dedup (move out of `uihelpers_{windows,darwin}.go`)
- **QUAL-03** — `forceCloseCh` contract clarified by relocating allocation
- **QUAL-04** — `tailLines` (`cmd/otto-tray/tray.go`): collect-then-reverse
- **QUAL-05** — dead `sessions` / `sessionsMu` vars in `internal/pool/regression_rel_pool_02_test.go`
- **QUAL-06** — stale `removeSlot` comment in `internal/pool/respawn_ctx_cancel_test.go`

Out of scope: anything not on this list. Scope-creep refactors observed while touching these files belong in their own phase or stay in the review backlog.

</domain>

<decisions>
## Implementation Decisions

### QUAL-01 — escapeApplescript scope
- **D-20-01:** Replace `\n`, `\r`, `\t` with their AppleScript-recognized escape sequences (`\\n`, `\\r`, `\\t` in the emitted script literal) so multi-line stderr in failure dialogs remains readable. **Strip** all other C0 controls (0x00–0x1F) and DEL (0x7F) entirely. Continue escaping `"` and `\` as today. Rationale: defense-in-depth — anything below 0x20 other than the three known whitespace escapes has no business in a notification/dialog body.
- **D-20-02:** Add `cmd/otto-tray/escapeApplescript_darwin_test.go` (or equivalent file co-located with the implementation, `//go:build darwin`) with table-driven cases: empty, plain ASCII, embedded `"`, embedded `\`, embedded `\n`/`\r`/`\t`, raw `\x00`/`\x1F`/`\x7F`, mixed. This function backs a `//nolint:gosec G204` so it's worth a direct unit test rather than relying on call-site coverage.

### QUAL-02 — tooltipForState dedup
- **D-20-03:** Create `cmd/otto-tray/tooltip.go` with `//go:build darwin || windows` containing the single shared `tooltipForState` implementation. Delete the duplicates from `uihelpers_darwin.go:39-46` and `uihelpers_windows.go:35-42`. Both existing copies are byte-identical, so this is a straight extraction.

### QUAL-03 — forceCloseCh contract
- **D-20-04:** Relocate `forceCloseCh` allocation. Remove `forceCloseCh: make(chan struct{})` from both `New()`/`NewWithCommit()` constructors (`internal/server/server.go:208` and `:275`). `RunUntilSignal` allocates and assigns `s.forceCloseCh` before invoking `s.Run`. `Run`-only callers leave it nil; the select arm in `Run()` (`server.go:473`) on a nil channel never fires (idiomatic Go), so the dead arm is removed meaningfully without restructuring `Run`'s signature.
- **D-20-05:** Update the field-level comment on `forceCloseCh` (`server.go:182-187`) to state the new contract: "Allocated by `RunUntilSignal` before calling `Run`; nil when `Run` is called directly. The nil-channel select arm in `Run` is intentional — direct `Run` callers cannot force-close." Update the `close(s.forceCloseCh)` site in `RunUntilSignal` (`server.go:545`) to make the allocation site obvious in context.

### QUAL-04 — tailLines algorithm swap
- **D-20-06:** Replace the O(n²) `kept = append([]string{t}, kept...)` prepend (`cmd/otto-tray/tray.go:417`) with a collect-then-reverse: walk lines back-to-front, append non-empty into `kept` (now grows in reverse order, no copy per insert), break when `len(kept) >= n`, then reverse in place before `strings.Join`. Same I/O behavior, no semantic change. Capacity hint stays at `n`.

### QUAL-05 — dead test vars
- **D-20-07:** Delete `sessions` / `sessionsMu` declarations at `internal/pool/regression_rel_pool_02_test.go:109-110` and their references at `:122-124`. Vet/staticcheck currently flags neither because they're written but never read; the removal is purely a readability fix and a signal that the Phase 17 workaround they once supported is gone.

### QUAL-06 — stale comment
- **D-20-08:** Update the comment at `internal/pool/respawn_ctx_cancel_test.go:119` that still references `removeSlot`. The function was removed in Phase 17-03; rewrite the comment to describe the current mechanism (or delete it if the surrounding code is self-explanatory after `removeSlot` is gone). No code change beyond the comment.

### Commit granularity
- **D-20-09:** Six atomic commits, one per QUAL finding, following the project's per-requirement commit pattern visible across Phases 16–19. Commit subject form: `refactor(20): close QUAL-XX — <one-line summary>`. Each commit touches only the files needed for that finding (QUAL-02 touches three files; the rest touch one or two).

### Claude's Discretion
- Exact wording of the updated field-level comment on `forceCloseCh` (D-20-05) — semantics are locked, prose is not.
- Exact wording of the rewritten `removeSlot` comment in QUAL-06 — describe-or-delete is at planner/executor discretion based on what reads best in context.
- Whether the QUAL-01 test file is named `escapeApplescript_darwin_test.go` or folded into an existing `*_darwin_test.go` if one exists at plan time.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Requirements & roadmap
- `.planning/REQUIREMENTS.md` §QUAL-01..QUAL-06 (lines 35–40) — the authoritative spec for each finding's closure criterion.
- `.planning/ROADMAP.md` Phase 20 entry (lines 110, 167–177) — confirms single-plan, mechanical-batch scope.

### Source code touch points
- `cmd/otto-tray/uihelpers_darwin.go` §`escapeApplescript` (lines 131–140) — QUAL-01 implementation site; also call sites at lines 88–90, 104–106, 120–124.
- `cmd/otto-tray/uihelpers_darwin.go` §`tooltipForState` (lines 39–46) — QUAL-02 source A.
- `cmd/otto-tray/uihelpers_windows.go` §`tooltipForState` (lines 35–42) — QUAL-02 source B (byte-identical to source A).
- `cmd/otto-tray/tray.go` §`tailLines` (lines 403–423) — QUAL-04 implementation site.
- `internal/server/server.go` §`forceCloseCh` (field at lines 182–187; allocations at 208, 275; select arms at 473, 543–545) — QUAL-03 implementation surface.
- `internal/pool/regression_rel_pool_02_test.go` lines 109–110, 122–124 — QUAL-05 dead-var sites.
- `internal/pool/respawn_ctx_cancel_test.go` line 119 — QUAL-06 stale-comment site.

### Prior review provenance (for traceability only — do not re-execute)
- Phase 16 code review — surfaced QUAL-01..QUAL-04 as Info-level findings, deferred to v1.10.3 closeout.
- Phase 17 code review — surfaced QUAL-05/QUAL-06 as Info-level findings, deferred to v1.10.3 closeout.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- **AppleScript escape function** (`escapeApplescript`): single hot path consumed by `notify`, `infoDialog`, and the yes/no confirmation in `uihelpers_darwin.go`. All three call sites pass operator-derived strings (`title`, `body`, button labels). Test seam for QUAL-01 is the function itself — no need to mock `osascript`.
- **Build-tag pattern**: existing files already use `//go:build darwin` (uihelpers_darwin.go) and `//go:build windows` (uihelpers_windows.go). The new `tooltip.go` for QUAL-02 follows the same convention with the `||` combinator.
- **Server constructor pair** (`New` / `NewWithCommit`): both must be updated symmetrically for QUAL-03. `NewWithCommit` is the real constructor; `New` is a Phase-1 compatibility wrapper that delegates to it via `NewWithCommit(... "unknown")` (verify this delegation chain at plan time — if `New` does its own field init it must also stop allocating `forceCloseCh`).

### Established Patterns
- **Per-requirement commits**: every recent phase (15–19) commits one requirement per commit with `<type>(NN): <summary>` subjects. Phase 20 follows this — see D-20-09.
- **Nil channel select-never**: standard Go idiom (`select { case <-nilCh: ... }` never fires). Used here to remove the dead arm without changing `Run`'s signature.
- **Test files co-located with implementation**: `*_test.go` lives next to its source, with matching `//go:build` constraint when the source is platform-gated. The QUAL-01 test file inherits this.

### Integration Points
- **No cross-package integration.** Every touch point is internal to its package; no exported API changes; no consumer files outside the touched files need updating.
- **CI gates**: `make ci` must stay green (race, vet, staticcheck, gosec, arch-lint). QUAL-03's nil-channel pattern in `Run()` should be verified specifically under `-race` because it changes synchronization shape on the `Run`-only code path.

</code_context>

<specifics>
## Specific Ideas

- **QUAL-01 escape representation**: when emitting `\n` into the AppleScript string literal, the Go source should produce the two-byte sequence `\` + `n` inside the quoted AS string (i.e., `"\\n"` in Go). AppleScript's string literal recognizes `\n`, `\r`, `\t` as escapes; the function's job is to translate raw `\n` bytes into that two-character escape, not to forward the raw byte (which would prematurely terminate the AS string literal on some forms).
- **QUAL-03 nil-channel verification**: the planner should add or extend a `Run`-direct test that asserts `s.Run(ctx)` returns cleanly under normal `ctx.Done()` shutdown after the relocation — guards against accidentally allocating `forceCloseCh` somewhere else and re-introducing the dead arm.

</specifics>

<deferred>
## Deferred Ideas

- None surfaced during discussion — the phase scope is self-contained and the discussion stayed within the 6 named findings.

</deferred>

---

*Phase: 20-code-review-backlog-burn-down*
*Context gathered: 2026-06-12*
