---
phase: 4
reviewers: [codex]
reviewed_at: 2026-05-25T03:20:00Z
plans_reviewed: [04-01-PLAN.md, 04-02-PLAN.md, 04-03-PLAN.md, 04-04-PLAN.md]
skipped_reviewers:
  - gemini (quota exhausted — "You have exhausted your capacity on this model")
  - cursor (authentication required — run `cursor agent login` or set CURSOR_API_KEY)
  - claude (skipped: self — running inside Claude Code)
---

# Cross-AI Plan Review — Phase 4 (Streaming)

## Codex Review

### Summary

The plan set has the right architecture: canonical chunks stay central, Ollama gets its own NDJSON emitter, cancel flows through `ctx` into an engine-owned watchdog, and OpenAI/Anthropic are mostly ratified rather than rebuilt. However, as written it is not implementation-ready. The main issues are dependency ordering and watchdog teardown: Wave 1 likely does not compile, and enabling the watchdog before every normal completion path can stop it will create spurious `session/cancel` calls across existing surfaces.

### Strengths

- Keeps the load-bearing invariant: all streaming surfaces consume `engine.Run(ctx, req).Stream().Chunks()`.
- Correctly rejects a shared stream driver; independent emitters are safer given different wire contracts.
- Identifies the important Go pitfalls: `*bool` defaulting, `Flusher` before headers, `AfterFunc` stop semantics, and single-writer response handling.
- Includes the right test layers: unit tests for wire/default behavior, fake-ACP cancel assertion, and real-binary disconnect smoke.
- Ollama streaming plan is appropriately scoped: intermediate structs for `done:false`, final render helpers for `done:true`, thoughts only on `/api/chat`.

### Concerns

- **HIGH: Wave 1 is under-specified and likely will not compile.** Changing Ollama `wire.Stream` from `bool` to `*bool` while `handlers.go` still does `if wire.Stream { wire.Stream = false }` is a type error unless handler changes land in the same wave. Also, adding `Run(ctx) (RunHandle, error)` to `ollama.Engine` means `*engine.Engine` no longer satisfies it directly because Go does not allow covariant return types. The plan needs `cmd/otto-gateway/main.go` adapter wrappers, plus integration/fake updates.

- **HIGH: Watchdog teardown is introduced too early.** Plan 01 adds the `context.AfterFunc` watchdog, but OpenAI/Anthropic teardown is delayed until Plan 04. That means Waves 1-2 can emit spurious `session/cancel` after normal OpenAI/Anthropic stream completion because request contexts are canceled when handlers return. This should land atomically with teardown in every current `Run` consumer.

- **HIGH: Non-streaming paths are missing watchdog stop coverage.** `engine.Collect` uses `Run`, so every `stream:false` path can also get a post-response cancel unless `Collect` stops the watchdog after natural stream completion. Plan 04 only mentions SSE finalizers. This risks violating success criterion 3 while trying to satisfy criterion 4.

- **HIGH: OpenAI/Anthropic default-streaming is not addressed.** Current-style `bool Stream` fields mean omitted `stream` defaults to false. If success criterion 2 means `/v1/chat/completions` must stream when `stream` is absent, Plan 04 needs the same `*bool` defaulting change and omitted-field E2E coverage. Same question applies to Anthropic if "all surfaces stream by default" is literal.

- **MEDIUM: Plan 04 interface changes also need wrapper/test updates.** Adding `StopWatchdog()` to OpenAI/Anthropic `RunHandle` requires updates to cmd-level run handle adapters, integration adapters, and fake run handles in unit/golden tests. Those files are not listed.

- **MEDIUM: Write-failure cancel path is only explicit for Ollama.** D-07 says write/flush failure should cancel a derived context so the watchdog is the single cancel path. OpenAI/Anthropic currently pass `r.Context()` directly and Plan 04 does not mention deriving/canceling a child context on emitter write errors.

- **MEDIUM: Pool-of-1 survival smoke may pass without proving "slot did not crash."** A follow-up request returning 200 proves availability, but not necessarily that the original slot survived rather than being restarted. If possible, assert stable worker identity/PID, no slot-exit log, or expose a test-only fake/pool signal.

- **MEDIUM: Anthropic mid-stream error behavior may conflict with D-05.** The plan says no mid-stream error frame, but existing Anthropic SSE behavior emits `event: error` on `Result()` error. Plan 04 should either change that or explicitly document that Anthropic is exempt.

- **LOW: Timing-based watchdog tests may be flaky.** `50ms sleep` checks are weaker than channel-based assertions. Fake ACP should expose a cancel channel so tests can select with deadlines and assert exact session IDs.

- **LOW: The Wave 1 fake `Run() { return nil, nil }` is brittle.** Prefer returning a clear test error until Plan 02 replaces it, so accidental streaming-path use fails diagnostically instead of panicking.

### Suggestions

- Move watchdog enablement into the same commit/wave as teardown for all existing consumers: `engine.Collect`, OpenAI SSE, Anthropic SSE, and Ollama once added.
- Add `StopWatchdog` plumbing everywhere the interfaces wrap `*engine.Run`: `cmd/otto-gateway/main.go`, adapter integration test wrappers, and fake run handles.
- Make `engine.Collect` stop the watchdog only after natural terminal completion. Do not stop it on `ctx.Done()` cancellation paths; let the watchdog issue `session/cancel`.
- For all streaming handlers, use a derived context (`ctx, cancel := context.WithCancel(r.Context()); defer cancel()`), ensure normal finalizers call `StopWatchdog()` before returning, while write errors return without stopping so `cancel()` triggers the authoritative cancel path.
- Resolve the default-streaming requirement explicitly. If absent means streaming for OpenAI/Anthropic, change those request structs to `*bool`, add `streamEnabled`, and add E2E tests where `stream` is omitted.
- Strengthen the disconnect smoke with a timeout-bound second request and, if available, an assertion that the pool did not restart the worker.
- Update Plan 03's `StopPreventsCancel` test to call `stop()`, then cancel the context, then assert no cancel frame/call is observed.

### Risk Assessment

**Overall risk: HIGH** until ordering and teardown are corrected. The feature design is sound, but the current plan can break compilation and can introduce spurious cancels in normal request paths. After moving watchdog teardown into the same wave as watchdog registration and clarifying default-streaming semantics for OpenAI/Anthropic, the risk drops to **MEDIUM**, mainly around cancellation races and real-binary disconnect flakiness.

---

## Consensus Summary

Only one independent reviewer (Codex) was available this run — Gemini hit its quota and Cursor is unauthenticated, so there is no cross-model consensus to triangulate. Codex's findings should be treated as a single strong signal, not as agreed-upon consensus, and the HIGH items below warrant verification against the actual codebase before replanning.

### Highest-Priority Concerns (Codex, unverified by a second model)

1. **Watchdog ordering / spurious `session/cancel` (HIGH).** Plan 01 registers the `context.AfterFunc` watchdog in `engine.Run` in Wave 1, but the OpenAI/Anthropic `StopWatchdog()` teardown is deferred to Plan 04 (Wave 3). Between Wave 1 and Wave 3 — and on the non-streaming `engine.Collect` path, which also calls `Run` — every normal completion could fire a spurious `session/cancel` when `r.Context()` ends. **This is the single most important item to verify**: does `Collect` go through `Run`? If so, the watchdog teardown must cover `Collect` and all shipped SSE finalizers in the SAME wave the watchdog is introduced.

2. **Wave 1 compilation risk (HIGH).** Whether `*engine.Engine` still satisfies the extended `ollama.Engine` interface (Go has no covariant return types — `engine.Run` returns `*engine.Run`, not `RunHandle`), and whether `cmd/otto-gateway/main.go` / integration wrappers need updating, must be confirmed. The existing OpenAI/Anthropic adapters already declare a `RunHandle`-returning `Engine` interface, so an adapter shim likely already exists — verify it.

3. **OpenAI/Anthropic default-streaming semantics (HIGH).** SC2 says `/v1/chat/completions` "defaults to streaming." Confirm whether those surfaces already default to stream (shipped Phase 3/3.1) or whether they share the same absent-field-defaults-to-false bug the Ollama `*bool` fix targets.

### Divergent Views

None — single reviewer.

### Note on verification

Several HIGH concerns (watchdog-on-Collect, covariant-return compile error) are checkable directly in the codebase in minutes. Recommend verifying them before `/gsd:plan-phase 4 --reviews`, since a few may already be handled by existing adapter shims or by `Collect` not routing through the watchdog path.
