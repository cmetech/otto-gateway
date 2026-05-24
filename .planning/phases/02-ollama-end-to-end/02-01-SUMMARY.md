---
phase: 02-ollama-end-to-end
plan: 01
subsystem: canonical-types
tags:
  - canonical-types
  - chat-request
  - chat-response
  - discriminated-union
  - tri-surface
  - forward-design

# Dependency graph
requires:
  - phase: 01-foundation
    provides: canonical package leaf-invariant (no internal/ imports) + chunk.go discriminated-union idiom
  - phase: 01.1-acp-wire-alignment
    provides: ResourceLinkBlock.Name field (D-04); StopReason enum; ModelInfo; PromptCapabilities
provides:
  - canonical.ChatRequest with all D-08 tri-surface fields (Model, System, Messages, Tools, ToolChoice, MaxTokens, Temperature, TopP, StopSequences, Stream, Think, Format, Metadata, WorkingDirOverride, ResourceLinks)
  - canonical.ChatResponse (D-10) symmetric to ChatRequest (ID, Model, Message, StopReason, Usage)
  - canonical.Message + ContentPart discriminated union (D-09); MessageRole + ContentKind enums
  - canonical.FinalResult per-stream metadata mirror of acp.FinalResult (consumed by Plan 04 engine.Stream shim)
  - canonical.BlockKindImage + ImageBlock variant on canonical.Block (D-09 footnote, Codex M-1) so Ollama messages[].images survive the canonical → ACP block boundary
  - D-11 reflective sweep test (TestNoJSONTags) that defends the no-JSON-tags invariant against future PRs
affects:
  - 02-02 (Ollama adapter wire.go — consumes ChatRequest as the canonical translation target)
  - 02-03 (engine — calls into ChatRequest / ChatResponse / FinalResult)
  - 02-04 (engine.Stream shim — returns canonical.FinalResult from Result())
  - 02-05 (pool — slot-routes ACPClient via the engine boundary)
  - 02-06 (Ollama HTTP adapter — wire ↔ canonical translation)
  - phase-03 (Anthropic adapter — populates dormant ContentKindToolUse / ContentKindThinking / ResourceLinks fields)
  - phase-04 (OpenAI adapter — same canonical surface, different wire shape)
  - phase-06 (tool catalog — activates ToolSpec / ToolChoice / Tools / ToolCall)

# Tech tracking
tech-stack:
  added: []  # Pure type-shape change; no new dependencies
  patterns:
    - "Forward-design seam idiom — dormant fields zero-valued in current phase, documented as activated by later phase number"
    - "Discriminated-union with pointer fields (Block / ContentPart / Chunk) — exactly one pointer non-nil per Kind value"
    - "Reflective D-11 defense — meta-test walks reflect.TypeOf(...).Field(i).Tag.Get(\"json\") across every exported type"
    - "Canonical mirror type — FinalResult mirrors acp.FinalResult so the engine boundary never leaks ACP wire internals"

key-files:
  created:
    - internal/canonical/chat.go
    - internal/canonical/chat_test.go
    - internal/canonical/chunk_image_test.go
  modified:
    - internal/canonical/chunk.go

key-decisions:
  - "Followed PLAN.md verbatim — no deviations. All D-08/D-09/D-10/D-11 field lists applied as specified."
  - "BlockKindImage appended at iota position 2 (not inserted) so Phase 1.1 callers reading BlockKindText==0 / BlockKindResourceLink==1 are unaffected."
  - "FinalResult.StopReason zero value is StopUnknown (Phase 1.1 D-02 forward-compat default) — locked by TestFinalResult_ZeroValue."
  - "ResourceLinks reuses ResourceLinkBlock from chunk.go (not redeclared) — TestChatRequest_ResourceLinks's element_type subtest enforces this via reflect.TypeOf(...).Elem().Name() == \"ResourceLinkBlock\"."

patterns-established:
  - "Pattern: Forward-design canonical type — declare every downstream-phase field today as zero-valued seam; document phase that activates each (e.g., // Dormant in Phase 2; activated by Phase 3.1)"
  - "Pattern: Reflective D-11 sweep test — every new canonical file pairs with a TestNoJSONTags_* meta-test that fails if any future PR adds json:\"...\" tags"
  - "Pattern: Append-only iota — when extending a discriminator enum, append after the existing values to preserve wire compatibility with prior callers"

requirements-completed:
  - SURF-03
  - SURF-05

# Metrics
duration: 5min
completed: 2026-05-24
---

# Phase 02 Plan 01: Canonical Chat Types Summary

**Canonical tri-surface chat types (ChatRequest / ChatResponse / Message / ContentPart / FinalResult) plus the BlockKindImage variant land in internal/canonical as the stable contract every Phase 2..6 adapter and engine compile against — zero JSON tags, zero internal/ imports, every dormant Phase 3 / 3.1 / 4 / 6 field present as a forward-design seam.**

## Performance

- **Duration:** ~5 min (272s)
- **Started:** 2026-05-24T00:31:28Z
- **Completed:** 2026-05-24T00:36:00Z
- **Tasks:** 3 completed
- **Files modified:** 4 (3 created + 1 modified)

## Accomplishments

- Locked the canonical tri-surface chat contract (ChatRequest 15 fields, ChatResponse 5 fields, Message + ContentPart + 13 supporting types) so Phase 2..6 require zero canonical-type churn.
- Reflective D-11 defense (TestNoJSONTags) now blocks any future PR that re-introduces json:"..." tags into canonical — meta-test walks every exported type in chat.go and chunk_image_test.go does the same for ImageBlock.
- Discriminator iota positions for MessageRole, ContentKind, and BlockKind are now locked by table-driven tests; any reordering surfaces as a test failure rather than silently miswiring an adapter's wire mapping.
- BlockKindImage + ImageBlock unblock the Ollama messages[].images → canonical → ACP image-block path (Codex M-1: without it the wire payload would silently drop at the ACP block boundary).
- FinalResult canonical mirror gives Plan 04's engine.Stream shim a stable Result() return type so the engine boundary never leaks internal/acp types into callers.

## Task Commits

Each task was committed atomically:

1. **Task 1: Create internal/canonical/chat.go with tri-surface chat types** — `af70a39` (feat)
2. **Task 2: Create internal/canonical/chat_test.go with zero-value + discriminator coverage tests** — `184040c` (test)
3. **Task 3: Extend internal/canonical/chunk.go with BlockKindImage + ImageBlock variant** — `a1ef71d` (feat — combined edit-chunk.go + add chunk_image_test.go in one commit since the type and its test are atomic from a discriminator-coverage standpoint)

_Note on TDD ordering: PLAN.md sequenced Task 1 (types) before Task 2 (tests), which is the inverse of canonical RED→GREEN. Task 2's verify step then ran the tests against Task 1's already-shipped types as the GREEN gate. Task 3 followed the same plan-prescribed ordering (edit chunk.go + add chunk_image_test.go atomically). This matches the plan's sequencing; no deviation flagged._

## Files Created/Modified

- `internal/canonical/chat.go` (created, 282 lines) — ChatRequest, ChatResponse, Message, ContentPart, ImagePart, ToolUsePart, ToolResultPart, ToolCall, ToolSpec, ToolChoice, Format, Usage, MessageRole, ContentKind, FinalResult. Reuses StopReason and ModelInfo from sibling files; reuses ResourceLinkBlock from chunk.go for the ResourceLinks slice.
- `internal/canonical/chat_test.go` (created, 222 lines) — 7 tests covering zero values, discriminator coverage, populated round-trip via reflect.DeepEqual, no-JSON-tags reflective sweep, FinalResult zero value, and ResourceLinks zero-value + multi-entry round-trip + element-type identity.
- `internal/canonical/chunk.go` (modified) — appended BlockKindImage at iota position 2 (BlockKindText==0 and BlockKindResourceLink==1 preserved), added Block.Image *ImageBlock pointer, added ImageBlock{Source, MIMEType, Data []byte} type with doc comments. No JSON tags added.
- `internal/canonical/chunk_image_test.go` (created, 81 lines) — TestBlockKindImage_Discriminator (locks iota positions), TestImageBlock_ZeroValue, TestBlock_ImageVariant (round-trip via reflect.DeepEqual using PNG magic bytes), TestNoJSONTags_ChunkImageBlock.

## Decisions Made

None beyond what PLAN.md already locked. Every type field, doc-comment intent, iota position, and test assertion came directly from PLAN.md's `<action>` and `<behavior>` blocks plus the cited D-08 / D-09 / D-10 / D-11 / Codex H-2 / Codex M-1 footnote references in 02-CONTEXT.md.

## Deviations from Plan

None — plan executed exactly as written. Every acceptance criterion was met on the first run for all three tasks. No Rule 1/2/3 auto-fixes triggered. No Rule 4 architectural questions surfaced.

## Verification Results

### Per-task verify commands

- **Task 1:** `go build ./internal/canonical/...` exit 0; `grep -L 'json:"' internal/canonical/chat.go` returns file path; zero `loop24-gateway/internal` imports; all 5 named types present once each; WorkingDirOverride, Temperature *float64, ResourceLinks []ResourceLinkBlock seams all present.
- **Task 2:** `go test -race -count=1 ./internal/canonical/... -run 'TestChatRequest_ZeroValue|TestMessageRole_DiscriminatorCoverage|TestContentKind_DiscriminatorCoverage|TestChatResponse_AssemblyShape|TestNoJSONTags|TestFinalResult_ZeroValue|TestChatRequest_ResourceLinks' -v` — all 7 PASS (with 3 sub-tests on TestChatRequest_ResourceLinks). Zero `encoding/json` imports in chat_test.go. 25 `t.Errorf` callsites (well above the ≥1 floor for non-fatal-in-loop discipline).
- **Task 3:** All 4 named image tests PASS. BlockKindText (4 refs) and BlockKindResourceLink (4 refs) iota values preserved; BlockKindImage appended at position 2. Zero JSON tags in chunk.go; zero internal/ imports.

### Plan-level success criteria

- [x] `internal/canonical/chat.go`, `internal/canonical/chat_test.go`, modified `internal/canonical/chunk.go`, and `internal/canonical/chunk_image_test.go` all exist and are committed
- [x] All 7 chat_test.go tests + 4 chunk_image_test.go tests pass on `go test -race -count=1 ./internal/canonical/...`
- [x] D-08 ChatRequest field list present verbatim (15 fields including ResourceLinks per H-2 footnote)
- [x] D-09 Message uses []ContentPart with discriminated ContentKind; BlockKindImage + ImageBlock present in chunk.go
- [x] D-10 ChatResponse symmetric (ID + Model + Message + StopReason + Usage)
- [x] FinalResult type present in chat.go
- [x] D-11 zero JSON tags on any new type (verified in chat.go AND in extended chunk.go via sweep)
- [x] Phase 1.1's `ResourceLinkBlock.Name` field verified present (no change required)
- [x] BlockKindText (0) and BlockKindResourceLink (1) iota positions PRESERVED; BlockKindImage appended at position 2

### Regression

- Pre-existing `internal/canonical/types_test.go` still green.
- Whole-repo `go build ./...` exits 0.
- Whole-repo `go test -race -count=1 ./...` exits 0 across acp / canonical / config / server packages.

### Pre-commit gate compliance

Every commit passed pre-commit: golangci-lint, go mod tidy, hardcoded-secrets scan, EOF/whitespace/merge-conflict/large-file checks. No `--no-verify` used.

## TDD Gate Compliance

PLAN.md is `type: execute` (not `type: tdd`), so plan-level RED/GREEN/REFACTOR gates do not apply. The three constituent tasks all declared `tdd="true"`, but the plan's chosen task ordering is types-then-tests (Task 1 ships chat.go → Task 2 ships chat_test.go → Task 3 ships chunk.go edits + chunk_image_test.go in one commit). This is the inverse of within-task RED→GREEN ordering for Tasks 1 and 2 (the test commit `184040c` lands after the type commit `af70a39`). The Task 3 type+test bundle is single-commit. This sequencing was explicit in PLAN.md and was followed as written; flagging here for visibility but no plan-execution deviation.

## Known Stubs

None. This plan ships pure data shapes — every field is either populated in Phase 2 or documented as a forward-design seam consumed by a specific later phase. No placeholder values, no empty implementations that flow to UI/wire output.

## Threat Flags

None. The plan's threat model (T-02-01..04) covers tampering on field-shape drift (mitigated by discriminator coverage tests), JSON-tag re-introduction (mitigated by TestNoJSONTags), information disclosure (accepted — no logging in canonical), and unbounded Metadata DoS (accepted — bounded at Phase 06 HTTP layer via http.MaxBytesReader). No new security-relevant surface introduced beyond what the plan already enumerated.

## Self-Check: PASSED

**Files exist:**
- `internal/canonical/chat.go` — FOUND
- `internal/canonical/chat_test.go` — FOUND
- `internal/canonical/chunk.go` — FOUND (modified)
- `internal/canonical/chunk_image_test.go` — FOUND

**Commits exist:**
- `af70a39` (Task 1 feat) — FOUND
- `184040c` (Task 2 test) — FOUND
- `a1ef71d` (Task 3 feat) — FOUND
