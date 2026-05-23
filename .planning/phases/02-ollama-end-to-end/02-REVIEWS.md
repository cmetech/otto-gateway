---
phase: 2
reviewers: [codex]
reviewed_at: 2026-05-23T23:42:20Z
plans_reviewed:
  - 02-01-PLAN.md
  - 02-02-PLAN.md
  - 02-03-PLAN.md
  - 02-04-PLAN.md
  - 02-05-PLAN.md
  - 02-06-PLAN.md
reviewer_attempts:
  - reviewer: gemini
    status: failed
    reason: "429 quota exhausted on gemini-cli after 3 retries"
  - reviewer: claude
    status: skipped
    reason: "self-CLI (running inside Claude Code; CLAUDE_CODE_ENTRYPOINT=cli)"
  - reviewer: codex
    status: success
    tokens_used: 125052
  - reviewer: cursor
    status: failed
    reason: "Authentication required. Run 'cursor agent login' or set CURSOR_API_KEY."
  - reviewer: coderabbit
    status: missing
    reason: "binary not installed"
  - reviewer: opencode
    status: missing
    reason: "binary not installed"
  - reviewer: qwen
    status: missing
    reason: "binary not installed"
  - reviewer: ollama
    status: skipped
    reason: "host :11434 is actually the Loop24 Node reference acp-ollama-server.js (no /v1/chat/completions endpoint)"
  - reviewer: lm_studio
    status: missing
    reason: "no server detected on :1234"
  - reviewer: llama_cpp
    status: skipped
    reason: "host :8080 is Docker/Portainer, not a model server"
---

# Cross-AI Plan Review — Phase 2 (Ollama End-to-End)

Only **Codex** returned a substantive review. Gemini hit a 429 quota wall after 3 retries, Cursor requires `cursor agent login`, and Claude was skipped as the self-CLI. Local-server review paths were unusable in this environment (port 11434 is the existing Node ACP reference, port 8080 is Docker).

> **Note on weight:** This is a single-reviewer review, not a multi-AI consensus. Treat Codex's findings as one expert perspective — high-signal but uncorroborated. The "Consensus Summary" at the bottom is a synthesis of Codex's findings only.

---

## Codex Review

## Summary

The phase plan is strong in structure and coverage: it decomposes a large vertical slice into sensible waves, preserves the adapter-over-canonical architecture, and includes real-kiro plus LangFlow verification. However, I would not execute it as-is. There are several contract mismatches around `engine.Stream`, `PreHook` short-circuiting, `resource_link` cwd derivation, model catalog capture, and XFF trust that can cause either compile failures or false confidence against the roadmap success criteria.

## Strengths

- Clear wave ordering: canonical/auth/config first, then engine, pool, adapter/server wiring.
- Good architectural intent: adapter depends on consumer interfaces, canonical stays wire-agnostic, engine owns ACP block flattening.
- Strong verification posture: race tests, goleak gates, integration tests, human LangFlow checkpoint.
- Auth token comparison correctly requires `crypto/subtle.ConstantTimeCompare`.
- Phase 2 explicitly keeps streaming, sessions, tools, embeddings, and hook implementations out of scope.
- `/api/pull` stream-default behavior is called out with Node line references, which is important for LangFlow parity.
- Warmup-before-listen is treated as a first-class acceptance condition.

## Concerns

- **HIGH — Phase 1.1 dependency is not an executable gate.**
  Plans `02-04`, `02-05`, and `02-06` assume Phase 1.1 has already added/fixed `acp.FinalResult.StopReason`, `acp.Client.AvailableModels()`, `ResourceLinkBlock.Name`, and spec-compliant `session/prompt` handling. The phase metadata only depends on other Phase 2 plans, so an executor could start with an incompatible ACP layer.

- **HIGH — Phase 2 cwd success criterion is not actually satisfiable.**
  `02-04 Task 2` says `extractFileURIs(req)` always returns empty because no `ContentPart` variant carries `resource_link`. But roadmap SC #5 requires cwd derivation from `resource_link` block URIs, verified by handler-level tests. As written, only `X-Working-Dir`, `KIRO_CWD`, and `os.Getwd()` can work.

- **HIGH — `engine.Stream` / `*acp.Client` contract is inconsistent.**
  `02-04 Task 1` says `*acp.Client` structurally satisfies `engine.ACPClient`, but `engine.ACPClient.Prompt` returns `engine.Stream`, while `*acp.Client.Prompt` returns `*acp.Stream`. Since `*acp.Stream` has `Chunks` as a field and `Result()` returns `*acp.FinalResult`, it does not satisfy `engine.Stream`. The proposed `wrapACPStream` helper is not usable through that interface shape.

- **HIGH — PreHook short-circuit loses the response body.**
  `02-04 Task 1/3` says a `PreHook` can return a `*canonical.ChatResponse`, then `newCompletedRun(resp)` emits zero chunks and only returns `FinalResult{StopReason: resp.StopReason}`. `Collect` then assembles from empty text, so the short-circuit response content is discarded.

- **HIGH — PostHooks are defined but not executed.**
  `02-04` must-haves mention `PostHook`, but the task behavior only runs `PreHooks`. CONTEXT D-04 says `Run` ranges over both and Phase 8 should only register implementations without changing `engine.Run`. That seam is not honored.

- **HIGH — Model catalog capture does not match D-13.**
  `02-05 Task 1` captures `slot.Client.AvailableModels()` immediately after `Initialize`. D-13 says models come from the first slot's `session/new` response. Unless Phase 1.1 changed `Initialize` to populate models, `/api/tags` can be empty or wrong.

- **HIGH — IP allowlist trusts spoofable `X-Forwarded-For`.**
  `02-02 Task 2` prefers the first XFF hop before `RemoteAddr`. In a laptop/no-proxy deployment, any client can set `X-Forwarded-For: 127.0.0.1` and bypass `ALLOWED_IPS`. That makes the allowlist unreliable when auth is unset, which is also the default.

- **MEDIUM — Ollama images are silently dropped despite D-09 / image requirements.**
  `02-01` adds `ImagePart`, but `02-04 Task 2` and `02-06 Task 1` drop images because `canonical.Block` has no image variant. This contradicts the stated Phase 2 canonical behavior that Ollama `messages[].images` populate image content parts.

- **MEDIUM — Pool's core slot lifecycle is under-tested.**
  `02-05 Task 3` explicitly skips fake-slot tests because `*acp.Client` is concrete. That leaves session→slot mapping, Prompt error release, Result release, unknown session, and Cancel behavior mostly covered only by real-kiro integration.

- **MEDIUM — Slot release depends too heavily on `Result()`.**
  `02-05 Task 2` releases the slot only when the stream wrapper's `Result()` runs. If `Collect` returns early, a context cancellation path is missed, or a future streaming caller fails to drain, the size-1 pool can wedge.

- **MEDIUM — `/api/version` route is registered both protected and exempt.**
  `02-06 Task 1/2` registers `/version` inside the adapter router and also registers `/api/version` on the outer server. The plan relies on chi precedence. Safer is to split exempt and protected routers so `/api/version` exists only once.

- **MEDIUM — Request body size cap is not applied to all body-reading endpoints.**
  `02-06 Task 1` requires `http.MaxBytesReader` for chat/generate, but `/api/show`, `/api/pull`, `/api/push`, `/api/create`, `/api/copy`, and `/api/delete` also decode bodies or may receive bodies.

## Suggestions

1. Add a Phase 2 preflight gate before Wave 1: assert Phase 1.1 is complete and compile-check the exact ACP contracts Phase 2 consumes.

2. Fix cwd data modeling now. Add either `ChatRequest.ResourceLinks []canonical.ResourceLinkBlock` or `ContentKindResourceLink` so `pickCwd` can actually derive from file URIs in Phase 2 tests.

3. Resolve the stream interface shape. Prefer one of:
   - pool-only ACPClient contract, removing the claim that `*acp.Client` satisfies it directly;
   - an explicit `engine.NewACPClientAdapter(*acp.Client)` wrapper;
   - or change `ACPClient.Prompt` to return a concrete adapter-owned stream type consistently.

4. Make `Run` carry a completed response for PreHook short-circuits, or have `Collect` detect `run.response != nil` and return it directly.

5. Implement PostHook execution in Phase 2, even if no hooks are registered. Put tests around response replacement and hook error propagation.

6. Capture the model catalog from an actual `NewSession` during warmup, then release the slot cleanly. Add a test proving `/api/tags` contains `auto` plus captured models.

7. Do not trust XFF by default. Add `TrustXForwardedFor bool` / `AUTH_TRUST_XFF`, default false, or only trust XFF from configured trusted proxy CIDRs.

8. Add a pool client factory seam for tests. Keep production default as `acp.New`, but allow tests to inject a fake client implementing the slot methods.

9. Remove `/version` from the protected adapter router or expose separate `ProtectedRouter()` and `HandleVersion()`.

10. Apply `http.MaxBytesReader` through a shared decode helper for every endpoint that reads a body.

## Risk Assessment

**Overall risk: HIGH until the contract gaps are fixed.** The plan is directionally sound and likely close, but several issues are load-bearing: cwd derivation cannot satisfy SC #5, the engine stream interface has a real compile/contract mismatch, PreHook short-circuiting loses responses, PostHooks are not actually wired, and model catalog capture may not work. After those are corrected, the remaining risk drops to **MEDIUM**, mostly around real-kiro behavior, LangFlow shape tolerance, and the deliberate Phase 2 limitation of a size-1 pool with no dead-slot recovery.

---

## Gemini Review

**Status:** Failed. Quota exhausted on `gemini-cli` after 3 retry attempts (HTTP 429). Stderr: `"You have exhausted your capacity on this model"`. Re-run when quota refreshes.

---

## Claude Review

**Status:** Skipped. Self-CLI — this orchestrator is running inside Claude Code (`CLAUDE_CODE_ENTRYPOINT=cli`), so invoking `claude` again would not yield an independent perspective. Skip is intentional per the workflow's self-CLI guard.

---

## Cursor Review

**Status:** Failed. `Error: Authentication required. Please run 'cursor agent login' first, or set CURSOR_API_KEY environment variable.` Re-run after authenticating.

---

## CodeRabbit Review

**Status:** Not installed.

---

## OpenCode Review

**Status:** Not installed.

---

## Qwen Review

**Status:** Not installed.

---

## Ollama Review

**Status:** Skipped. The server on `localhost:11434` is the Loop24 Node reference (`acp-ollama-server.js`), not a generic Ollama install — its `/v1/chat/completions` endpoint does not exist (the Node reference only exposes Ollama-native `/api/*` routes). Using it as a reviewer would proxy through kiro-cli, which is exactly what Phase 2 is being built to replace; results would be self-referential.

---

## LM Studio Review

**Status:** Not running on `localhost:1234`.

---

## llama.cpp Review

**Status:** Skipped. The endpoint on `localhost:8080` is Docker/Portainer (HTTP redirect to `/containers/`), not a llama.cpp server.

---

## Consensus Summary

> Synthesized from a **single reviewer (Codex)** only — read accordingly. The HIGH-severity concerns below are Codex's, not a multi-AI consensus.

### Codex's Top Concerns (synthesized into bucket categories)

**Category 1 — Cross-phase coupling not enforced (1 concern):**
- **Phase 1.1 dependency is silent.** Phase 2 plans assume specific Phase 1.1 API additions (`acp.FinalResult.StopReason`, `AvailableModels()`, `ResourceLinkBlock.Name`, spec-compliant `session/prompt`) but the plan frontmatter only depends on Phase 2 internal plans. An executor running Phase 2 with Phase 1.1 incomplete would compile-fail or behave wrong without a clear "this is the wrong thing" signal. **Suggestion:** Add a Wave 1 preflight task that compile-checks the exact ACP contracts.

**Category 2 — Locked-decision implementation gaps (4 concerns):**
- **SC #5 cwd derivation is structurally impossible.** `extractFileURIs(req)` walks `Message.Content` but no `ContentPart` Kind carries `resource_link`. The roadmap SC #5 test (resource-link cwd derivation) cannot pass with the current canonical types. Either (a) extend canonical with `ContentKindResourceLink` or `ChatRequest.ResourceLinks []ResourceLinkBlock`, or (b) restate SC #5 to drop resource-link derivation in Phase 2 and defer to Phase 3.1.
- **PreHook short-circuit drops response body.** The proposed `newCompletedRun(resp)` emits zero chunks and only forwards `StopReason`; `Collect` then assembles an empty response. The hook seam is defined per D-04 but executes incorrectly. **Fix:** carry the full `*ChatResponse` through the Run handle or short-circuit at `Collect` level before chunk-assembly.
- **PostHooks never execute.** Plan 04 declares both `PreHook` and `PostHook` fields per D-04 but only runs `PreHook` in `Run`. Phase 8 expectation is "just register impls — engine.Run unchanged". Fix the seam now.
- **Model catalog capture path is wrong.** Plan 05 calls `slot.Client.AvailableModels()` post-`Initialize`. D-13 explicitly says capture from the first slot's `session/new` result (`result.models.availableModels[]`). These are different ACP calls. Unless Phase 1.1 changed the contract, `/api/tags` will return empty or default models.

**Category 3 — Security defaults too permissive (2 concerns):**
- **HIGH — `X-Forwarded-For` trusted by default in IP allowlist.** A localhost client can set `X-Forwarded-For: 127.0.0.1` and bypass `ALLOWED_IPS` when auth is unset (the Node-default behavior). For laptop deployment (no proxy), XFF should NOT be trusted unless an explicit `AUTH_TRUST_XFF=true` or `TRUSTED_PROXIES` CIDR list is set.
- **MEDIUM — `http.MaxBytesReader` only on `/api/chat` and `/api/generate`.** Stub endpoints, `/api/show`, `/api/pull`, etc. also accept bodies and need the same DoS protection. **Fix:** centralize body-decode in a shared helper that always applies `MaxBytesReader`.

**Category 4 — Engine/pool contract glue (2 concerns):**
- **HIGH — `engine.ACPClient` interface vs `*acp.Client` shape mismatch.** Interface returns `engine.Stream` (proposed), but concrete `*acp.Client.Prompt` returns `*acp.Stream` (with `Chunks` as a field, `Result()` returns `*acp.FinalResult`). The "structurally satisfies" claim is false. **Fix:** either pool-only ACPClient contract, an explicit wrapper, or change `Prompt` signature to use a concrete adapter-owned stream type.
- **MEDIUM — Slot release tied to `Result()` only.** If `Collect` returns early or context-cancels before draining, slot leaks and the size-1 pool wedges. **Fix:** release on stream-close (defer) or via explicit `Release()` call in addition to `Result()`.

**Category 5 — Test coverage gaps (2 concerns):**
- **Pool fake-slot tests skipped** because `*acp.Client` is concrete. session→slot mapping, Prompt error release, Result release, unknown session, Cancel — all only covered by real-kiro integration. **Fix:** introduce an `acp.ClientFactory` interface so pool tests can inject fakes.
- **No test asserting model catalog populates `/api/tags`** — must add as part of fixing Category 2's catalog-capture issue.

**Category 6 — Image-block dropped (1 concern):**
- **MEDIUM — Ollama `messages[].images` silently dropped.** Plan 01 adds `ImagePart` but neither Plan 04 (`buildBlocks`) nor Plan 06 (Ollama adapter) translates them because `canonical.Block` has no image variant. Either (a) add `BlockKindImage` + ACP image-block construction now (Phase 1.1 ResourceLinkBlock.Name precedent), or (b) explicitly defer images to Phase 3.1 in CONTEXT.md deferred ideas and drop `ImagePart` from Plan 01 scope to avoid dead types.

### Divergent Views

No divergence to report — single reviewer.

### Recommended Action

Codex calls this **HIGH risk until contract gaps are fixed**, dropping to **MEDIUM** after. The 7 HIGH-severity items are substantive and should be addressed before execution. Run `/gsd:plan-phase 2 --reviews` to feed this back into the planner for a targeted revision pass.

When quota / auth issues for other reviewers resolve, consider re-running `/gsd:review --phase 2 --gemini --cursor` to corroborate Codex's findings with at least one second opinion before committing the revision approach.
