---
created: 2026-06-12T10:29:15.640Z
title: "v1.10.4: ADR for bounded bufio.Reader.ReadString in stderrDrainLoop (WR-01 from Phase 18 review)"
area: reliability
source: Phase 18 code review (WR-01)
files:
  - internal/acp/client.go:395-422
  - .planning/phases/18-reliability-long-tail/18-CONTEXT.md (D-18-04)
  - .planning/phases/18-reliability-long-tail/18-REVIEW.md (WR-01)
  - .planning/phases/18-reliability-long-tail/18-REVIEW-FIX.md (WR-01 deferral rationale)
---

## Problem

Phase 18's D-18-04 replaced `cmd.Stderr = os.Stderr` with a goroutine reading
kiro-cli stderr via `bufio.NewReader(stderrPipe).ReadString('\n')` and logging
each line at `slog.Warn`. This is the locked design.

The Phase 18 code review (WR-01) flagged that `bufio.Reader.ReadString('\n')`
will accumulate unbounded bytes into its internal buffer if a malicious or
runaway kiro-cli child writes a very long line without ever emitting `'\n'`.
The WR-04 fix added a 1MB UTF-8-boundary-aware truncation of the returned
string, but that runs AFTER `ReadString` has already accumulated the bytes.
In the pathological case (multi-GB single line, no newline), `ReadString`
can OOM the gateway before the truncation ever applies.

The Phase 18 fixer skipped this finding because the natural fix —
`bufio.Reader.ReadSlice('\n')` with a fixed scratch buffer, or
`bufio.NewReader(io.LimitReader(stderrPipe, N))` per line — changes the
per-line semantics that CONTEXT.md D-18-04 locked. A design change at that
level needs an ADR, not a fix-pass commit.

WR-01 is the only deferred finding from the entire Phase 18 review. Not
shipping the fix isn't urgent (a malicious kiro-cli child is unlikely in the
local-binary trust model) but the surface should be closed before v1.10.4
GA so the threat model on the gateway side stays clean.

## Solution

**TBD — needs an ADR.** Sketch of the design space the ADR should cover:

1. **Bounded variant: `bufio.Reader.ReadSlice('\n')` + fixed 1MB buffer.**
   Returns `bufio.ErrBufferFull` on overlong lines. Drain to next `'\n'` and
   emit a `truncated: true` line with `dropped_bytes: <N>`. Mirrors the WR-09
   telemetry pattern already in place.

2. **`io.LimitReader` wrapper per-line.** Pre-bound each `ReadString` call to
   1MB at the reader layer. Simpler but allocates a `LimitReader` per line.

3. **`bufio.Scanner` with `Buffer(make([]byte,0,1MB), 1MB)`.** This is what
   CONTEXT.md D-18-04 explicitly REJECTED because Scanner stops on
   `bufio.ErrTooLong`. Re-evaluate whether the stop semantics are actually
   problematic when the truncation point is already 1MB (which is "stop and
   warn anyway" semantics in disguise).

4. **Status quo + DoS-impact downgrade.** Argue that kiro-cli stderr is a
   trust boundary the operator already controls (they ship the binary), so
   the unbounded-accumulation surface is acceptable for v1.10.x and only
   matters if/when kiro-cli ever runs untrusted code (Phase 19's REL-ACP-01
   may be the right time to revisit).

**Recommended ADR title:** "Bound kiro-cli stderr drain to prevent
unbounded memory accumulation (close WR-01)".

**Recommended REQ-ID:** REL-ACP-XX (let Phase 19 / v1.10.4 milestone planning
assign).

**Test plan:** Pipe a 100MB single line (no newline) through a fake
kiro-cli stand-in (`tools/kiro-shim` has the right shape). Assert the
gateway's RSS stays under a tight bound (e.g., 50MB delta) and the line is
logged with `truncated: true` + `dropped_bytes: ~100MB`. Race-clean.
