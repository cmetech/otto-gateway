---
quick_id: 260529-ll2
type: summary
status: tasks-1-through-5-complete-task-6-checkpoint-pending
dependency_graph:
  requires: []
  provides:
    - "ChatTraceHook (Pre+Post NDJSON tracer)"
    - "Surface ctx helpers"
    - "Admin Log Tail multi-source dropdown"
    - "TailerRegistry lazy name→*Tailer cache"
    - "Config knobs CHAT_TRACE / CHAT_TRACE_FILE / CHAT_TRACE_MAX_AGE_DAYS"
  affects:
    - "internal/plugin chain composition (ChatTraceHook prepended to Pre, appended to Post when enabled)"
    - "admin /admin/logs/stream HTTP contract (source query param now required-from-allowlist; default main)"
    - "AdminSnapshot JSON shape (new log_sources field)"
tech_stack:
  added:
    - "log/slog + sync.Map pattern carry-forward from LoggingHook"
    - "github.com/DeRuina/timberjack second rotator instance for chat-trace.log"
  patterns:
    - "Slice-prepend wiring for positional invariants (chain.Pre = append([]engine.PreHook{chatTrace}, chain.Pre...))"
    - "Lazy name-keyed registry under mu.Lock (TailerRegistry.Get)"
    - "Defensive-copy on snapshot wire fields (append([]string{}, deps.LogPathOrder...))"
key_files:
  created:
    - internal/plugin/surface.go
    - internal/plugin/surface_test.go
    - internal/plugin/trace.go
    - internal/plugin/trace_test.go
  modified:
    - internal/config/config.go
    - internal/config/config_test.go
    - internal/adapter/openai/handlers.go
    - internal/adapter/ollama/handlers.go
    - internal/adapter/anthropic/handlers.go
    - internal/admin/admin.go
    - internal/admin/tail.go
    - internal/admin/sse.go
    - internal/admin/snapshot.go
    - internal/admin/snapshot_test.go
    - internal/admin/sse_test.go
    - internal/admin/tail_test.go
    - internal/admin/templates/index.html.tmpl
    - internal/admin/static/js/admin.js
    - cmd/otto-gateway/main.go
    - scripts/.env.otto-gw.example
    - docs/INSTALL.md
decisions:
  - "ChatTraceHook is FIRST in Pre via slice-prepend (positional, not content-driven) so a future chain literal refactor cannot silently demote it past PIIRedactionHook. Invariant comment in main.go and TestChatTraceHook_RecordsPreRedactionContent regression both guard T-ll2-07."
  - "Describe() whitelist exposes only {enabled, output_path} — never request content. TestChatTraceHook_DescribeNoSecrets walks the map and fails on any messages/tools/system/content/prompt substring."
  - "TailerRegistry path argument is consulted ONLY on first Get(name, path); subsequent calls return cached pointer regardless of path. Read-only contract — operator must restart the gateway to change a source path (D-10 carry-forward)."
  - "SSE source validation runs BEFORE setting SSE headers; unknown source returns 400 JSON envelope (never opens an empty event-stream connection)."
  - "Config.Load silently drops ChatTraceHook from EnabledHooks when CHAT_TRACE=false so operators can leave the entry in their allowlist for forward-compat without tripping chain.Filter typo-fail-fast."
metrics:
  duration: ~2h
  completed_date: 2026-05-29
---

# Quick 260529-ll2: ChatTraceHook + multi-source admin Log Tail Summary

Add a ChatTraceHook Pre+Post NDJSON tracer that captures the post-adapter canonical request pre-PII-redaction to a dedicated 0o600 chat-trace.log, plus a multi-source admin Log Tail (main / boot-err / chat-trace) with a source dropdown switching between them via the existing SSE infrastructure.

## What Shipped

### Task 1 — ChatTraceHook + Surface ctx helpers
Commit `cbbbb56`. `internal/plugin/surface.go` adds `WithSurface` / `SurfaceFromContext` reusing the unexported `ctxKey` struct from `request_id.go` for cross-package key-collision safety. `internal/plugin/trace.go` implements `ChatTraceHook` as both `engine.PreHook` and `engine.PostHook` with a `sync.Map` Pre→Post duration bridge (LoadAndDelete in After prevents unbounded growth — T-ll2-08), mutex-guarded NDJSON line writes, and a `Describe()` whitelist exposing only `{enabled, output_path}`.

Five tests: `TestSurfaceContextRoundTrip`, `TestSurfaceFromContext_Absent`, `TestChatTraceHook_DisabledNoWrite`, `TestChatTraceHook_SharedRequestID`, `TestChatTraceHook_DescribeNoSecrets`, `TestChatTraceHook_DurationPositive`, and the load-bearing `TestChatTraceHook_RecordsPreRedactionContent` that composes `ChatTraceHook.Before` → `PIIRedactionHook.Before` in chain order and asserts the raw email survives in the NDJSON pre line (T-ll2-07 chain-order guard).

### Task 2 — Config + adapter Surface stamping
Commit `8e9b9a5`. `internal/config/config.go` gains `ChatTrace`, `ChatTraceFile`, `ChatTraceMaxAgeDays` Config fields with their env loaders. `ChatTraceFile` defaults to a sibling-of-`LOG_FILE` path via `deriveChatTraceFile` (so an operator who sets `LOG_FILE=/var/log/otto/otto-gateway.log` gets `/var/log/otto/otto-gateway-chat-trace.log` for free). Writable-parent check is gated behind `cfg.ChatTrace=true` so disabled installs never touch disk. `Load()` silently drops `ChatTraceHook` from `EnabledHooks` when `CHAT_TRACE=false` to preserve forward-compat allowlisting.

The three adapter handler files (`openai/handlers.go`, `ollama/handlers.go`, `anthropic/handlers.go`) stamp `plugin.WithSurface(ctx, name)` on all five chat-shaped handler entries (`handleChatCompletions`, `handleCompletions`, `handleChat`, `handleGenerate`, `handleMessages`), placed directly after `stampPluginCtx` so request_id is already on ctx when `SurfaceFromContext` fires inside `ChatTraceHook.Before`. `scripts/.env.otto-gw.example` gains an operator-facing block documenting the three knobs.

Six new config tests cover default-disabled, sibling-file default, no-LOG_FILE default, invalid-parse, unwritable-parent, and the EnabledHooks-drop forward-compat.

### Task 3 — TailerRegistry + multi-source SSE + snapshot
Commit `11ddd0b`. `internal/admin/admin.go` replaces `Deps.LogPath string` with `Deps.LogPaths map[string]string` + `Deps.LogPathOrder []string`. The handler struct's single `*Tailer` becomes a `*TailerRegistry`. `internal/admin/tail.go` adds the registry — lazy name→`*Tailer` cache constructed via `mu.Lock`-guarded check+insert (concurrent `Get` on the same name returns identical pointers).

`internal/admin/sse.go` resolves the `source` query param BEFORE writing SSE headers; an unknown source returns 400 JSON `{"error":"unknown source: <name>"}` and the SSE handler exits — operators never see a benign empty event-stream connection (T-ll2-03 mitigation). Default source when absent is `"main"`. `internal/admin/snapshot.go` adds `LogSources []string `json:"log_sources"`` to `AdminSnapshot`, defensive-copied from `Deps.LogPathOrder` so a consumer mutating the slice cannot reach into live `Deps`.

All six existing admin tests that construct `Deps` were updated to the new map/slice shape. New tests: `TestTailerRegistry_LazyCreation`, `TestTailerRegistry_Concurrent` (100-goroutine race), `TestSSEHandler_UnknownSource_400`, `TestSSEHandler_DefaultSourceIsMain`, `TestSSEHandler_SourceSwitchUsesDifferentTailer`, `TestSnapshot_LogSources_PresentAndOrdered` (table-driven 3 cases), `TestSnapshot_LogSources_DefensiveCopy`.

### Task 4 — main.go ChatTraceHook wiring + INSTALL.md pitfall
Commit `1ca1d16`. `cmd/otto-gateway/main.go`: when `cfg.ChatTrace=true`, open a dedicated `*timberjack.Logger` at `cfg.ChatTraceFile` with `FileMode=0o600`, `MaxAge=cfg.ChatTraceMaxAgeDays`, daily 00:00 rotation, gzip. Construct a `ChatTraceHook` bound to it, PREPEND to `chain.Pre` via explicit slice-rebuild (`chain.Pre = append([]engine.PreHook{chatTrace}, chain.Pre...)`), APPEND the same instance to `chain.Post`. The wiring comment states verbatim: "ChatTraceHook is first in Pre to observe pre-redaction content. Do not reorder." The cleanup closure was extended to close `chatTraceRotator` BEFORE the registry/pool close so a crash in those still drains the trace write buffer.

The admin Deps construction now builds three log sources: `"main"` (`LOG_FILE` / `OTTO_LOG` / packaged default), `"boot-err"` (`OTTO_LOG_BOOT` override or `stripExt(mainLogPath)+"-boot.log"`), and `"chat-trace"` (`cfg.ChatTraceFile`, ONLY included when `CHAT_TRACE=true`). `stripExt` helper sits near `envOrDefault`.

`docs/INSTALL.md` gains a "CHAT_TRACE captures raw user content — file permissions and retention" subsection at the top of "Common install pitfalls" covering raw pre-redaction capture, 0o600 file mode, 3-day default retention, the DO-NOT-aggregate-without-redaction-sidecar warning, and the recommended short-window debugging workflow.

### Task 5 — Admin UI source selector
Commit `11c8ca7`. `internal/admin/templates/index.html.tmpl` inserts `<select class="otto-select" data-log-source aria-label="Log file"></select>` directly before the existing level dropdown. `internal/admin/static/js/admin.js` adds multi-source state (`currentLogSource`, `logSourceLastJSON`, `logEventSource`) and four new functions:

* `populateLogSources(sources)` — builds `<option>` via `document.createElement` + `textContent`/`value` (NEVER `innerHTML`; T-6.1-16 invariant preserved). No-op when log_sources unchanged so operator selection survives across snapshot polls.
* `clearLogViewport()` — removes `.otto-log-row` / `.otto-log-row-fallback` children via NodeList→array copy while preserving the sticky header row and re-showing the data-log-empty placeholder.
* `openLogStream()` — closes existing `logEventSource` (if any) and opens a new one against `/admin/logs/stream?source=<currentLogSource>`.
* `initLogSourceSelector()` — wires the dropdown change handler to update `currentLogSource`, `clearLogViewport`, reset `logBackfillEnd` + `logNewestBuffer` + badge, update status text, then `openLogStream`.

`initLogTail` refactored to call `initLogSourceSelector` + `openLogStream` instead of constructing a bare `EventSource`. `onSSEOpen` status text now reads `"Connected — <source>"`. All pre-existing Log Tail invariants survive: T-6.1-16 textContent-only, WR-06 `dataset.level`, WR-03/04 backfill dedup, 1000-row cap, pause/resume, regex grep.

## Task 6 — Browser smoke test (checkpoint:human-verify, gate=blocking)

NOT auto-completed. The 10-step operator browser smoke test must be performed manually before this quick-task is closed. Steps documented in the plan and replayed in the structured checkpoint message returned to the orchestrator.

## Verification Results

All required automated verification commands pass on a clean tree at the tip of `worktree-agent-a56c48fa11c6976c4`:

```
$ go test ./internal/plugin/... ./internal/admin/... ./internal/config/... ./cmd/otto-gateway/... -race -count=1
ok    otto-gateway/internal/plugin     1.239s
ok    otto-gateway/internal/plugin/pii 1.431s
ok    otto-gateway/internal/admin      18.040s
ok    otto-gateway/internal/config     1.595s
ok    otto-gateway/cmd/otto-gateway    1.768s

$ go build ./...
(clean)

$ grep -c "ChatTraceHook is first in Pre" cmd/otto-gateway/main.go
2

$ grep -q "CHAT_TRACE captures raw user content" docs/INSTALL.md
(matched)

$ grep "plugin.WithSurface" internal/adapter/openai/handlers.go internal/adapter/ollama/handlers.go internal/adapter/anthropic/handlers.go | wc -l
5
```

The five surface-stamp sites are: openai `handleChatCompletions` line 102, openai `handleCompletions` line 267, ollama `handleChat` line 102, ollama `handleGenerate` line 283, anthropic `handleMessages` line 153.

## Deviations from Plan

None. The plan executed as written. The five auto tasks committed atomically in order with the expected verification commands passing each step. The Task 6 checkpoint is being returned via structured handoff to the orchestrator per the plan's `gate="blocking"` directive.

Two minor implementation-detail choices that the plan explicitly permitted as discretion calls:

* **Reused `ctxKey` from `request_id.go`** (rather than declaring a fresh struct type in `surface.go`). The plan permitted either path "whichever keeps the request_id.go file untouched"; reuse was the cleaner option since `ctxKey` is package-internal and the name field is the distinguishing identifier.
* **Test pre-seeded `tailers.byName` via map assignment** in the three test sites that previously injected a custom `*Tailer` into the handler. This uses the whitebox-test package access to bypass `Get()` and pre-cache a specific instance; alternative would have been to expose a test-only constructor on the registry, but the map-assignment is strictly local to tests and keeps the production API minimal.

## Authentication Gates

None encountered. This task touches no auth surface — `AuthHook` is upstream of `ChatTraceHook` only on Pre, but `AuthHook` does not run on the bare-`Before` test invocations (the test for chain-order composition deliberately stops at PIIRedactionHook). No CLI logins, no env-var secrets were consulted.

## Known Stubs

None. The output_path field in `ChatTraceHook.Describe()` is hardcoded to `""` rather than reaching into the Writer's filename — this is an explicit Pitfall-9 discipline choice (the hook stays writer-agnostic; the operator can read `CHAT_TRACE_FILE` from `.env.otto-gw` if they need to know where the file lives). Documented in the trace.go Describe docstring.

## Threat Flags

None new beyond the threat register in the plan. The `internal/admin/sse.go` source-validation path is the new untrusted-input surface and it is allowlist-checked via `slices.Contains(LogPathOrder, source)` per the plan's T-ll2-03 mitigation.

## Self-Check: PASSED

* Files created:
  - `internal/plugin/surface.go` — FOUND
  - `internal/plugin/surface_test.go` — FOUND
  - `internal/plugin/trace.go` — FOUND
  - `internal/plugin/trace_test.go` — FOUND
* Files modified (sample):
  - `internal/config/config.go` — modified (3 new fields, deriveChatTraceFile helper)
  - `cmd/otto-gateway/main.go` — modified (ChatTraceHook wiring, multi-source LogPaths)
  - `internal/admin/admin.go` — modified (LogPaths/LogPathOrder, TailerRegistry)
* Commits verified in `git log --oneline`:
  - cbbbb56 FOUND
  - 8e9b9a5 FOUND
  - 11ddd0b FOUND
  - 1ca1d16 FOUND
  - 11c8ca7 FOUND
