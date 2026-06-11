---
phase: 15
phase_name: Fix Critical + High
created: 2026-06-11
audience: human (audits, retrospectives) — NOT consumed by downstream agents
---

# Phase 15 Discussion Log

## Areas selected for discussion

Multi-select from 4 candidate gray areas — user selected ALL FOUR:
1. Plan structure & parallelization
2. New env knobs vs hard-coded
3. 503 / error envelope shape
4. T-3 macOS death visibility channel

## Area 1: Plan structure & parallelization

**Q:** How should Phase 15's 9 fixes be split into plans?

Options presented:
- 3 plans by subsystem (Pool, HTTP, Tray)
- 1 plan per finding (9 plans, 3 waves)
- Sequential by severity
- Hybrid: P-1 alone + 3 subsystem plans for the rest

**Selected:** 3 plans by subsystem (Pool, HTTP, Tray)

Rationale captured in D-01: mirrors Phase 14 shape minus the config plan since Phase 15 doesn't touch config/hooks. Each plan owns 3 findings, clean worktree boundaries.

Flag raised in D-01a: `internal/server/server.go` may be touched by both 15-01 (P-2 in-flight cancel during grace) and 15-02 (H-1 long-lived SSE unwind). Planner must confirm `files_modified` is disjoint at plan-time and either merge plans or serialize via `depends_on` if overlap is real.

## Area 2: New env knobs vs hard-coded

**Q1:** P-1 needs a bounded pool acquire timeout. How should it be configured?

Options:
- New env var POOL_ACQUIRE_TIMEOUT_MS (default 5000)
- Hard-coded 5s
- Reuse SESSION_TTL_MS / 2
- New env var with conservative 30s default

**Selected:** New env var with conservative 30s default → D-04.

**Q2:** P-2 second-SIGINT semantics — what shutdown grace?

Options:
- Reuse existing 30s (server.go:377-381) for 1st SIGINT
- New SHUTDOWN_GRACE_MS env (default 30000)
- Two-tier: 5s soft drain, 30s hard cap

**Selected:** Reuse existing 30s → D-05.

Combined effect: only ONE new env var (POOL_ACQUIRE_TIMEOUT_MS), no shutdown grace knob. The two-tier shutdown design is captured in deferred as something to revisit in v1.10 if operator feedback shows the single grace is wrong.

## Area 3: 503 / error envelope shape

**Q1:** P-1 typed 503 — how do clients learn this is pool-exhaustion?

Options:
- Surface-native bodies + Retry-After header
- Surface-native bodies, no Retry-After
- Plain 503 with surface-native error string only

**Selected:** Surface-native bodies + Retry-After → D-07/D-08.

Per-surface envelopes documented for OpenAI, Ollama, and Anthropic. Internal sentinel `pool.ErrPoolExhausted` keeps the adapter layer responsible for surface-specific assembly.

**Q2:** H-3 mid-stream worker death — what does the client receive?

Options:
- Surface-native terminal error frame + WARN log
- TCP close + WARN log only
- Surface-native error frame, no WARN log

**Selected:** Surface-native terminal error frame + WARN log → D-09/D-10.

D-10 makes WARN logging non-negotiable. The detailed per-surface frame format is captured in D-09 for OpenAI SSE, Ollama NDJSON, and Anthropic SSE (the Anthropic-vs-not check is flagged to the planner).

## Area 4: T-3 macOS death visibility channel

**Q:** Replacement for the LSUIElement no-op notification path

Options:
- Tray icon + tooltip state on every FSM transition
- Icon/tooltip + osascript display dialog on Error
- Icon/tooltip + bouncing dock icon (NSApp requestUserAttention)
- Icon/tooltip + terminal-notifier

**Selected:** Tray icon + tooltip state on every FSM transition → D-11/D-12/D-13/D-14.

Notification banner stays as best-effort secondary signal (D-12 — the LSUIElement no-op becomes a documented quirk, not a bug). The pattern is applied cross-platform to Windows too (D-13). Icon asset work is flagged for the researcher to confirm whether `cmd/otto-tray/icon/` already supports tinting (D-14).

Bouncing dock icon and terminal-notifier are captured as v1.10 candidates in `<deferred>`.

## Scope creep redirected

None this session. User stayed inside the 9-finding scope. The only adjacent capability discussed was Q2-related two-tier shutdown design, which became a deferred idea rather than a Phase 15 task.

## Deferred ideas

See `<deferred>` section of CONTEXT.md. Five items captured:
- terminal-notifier for macOS Notification Center (v1.10)
- Bouncing dock icon on Error (v1.10)
- POOL_ACQUIRE_TIMEOUT_MS lower bound enforcement (Phase 16 if natural fit with REL-CFG-01)
- Per-surface error code dictionary doc (v1.10 docs cleanup)
- Two-tier shutdown grace (v1.10 if operator feedback warrants)

## Claude's discretion items

Captured in CONTEXT.md `<decisions>` "Claude's Discretion" subsection — no further user input needed:
- Plan-internal task order: highest-severity finding first per plan, then REL-ID order
- Anthropic surface for H-3: fold in if `internal/adapter/anthropic/sse.go` has same truncation path, document the asymmetry otherwise
- CLAUDE.md env-var list update + REQUIREMENTS.md REL-* fulfillment flips ride the phase-close commit
- PII bracket shape (`[...]` not `<...>`) carried forward from project memory anti-pattern
