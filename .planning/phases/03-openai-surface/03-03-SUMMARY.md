---
phase: 03-openai-surface
plan: "03"
subsystem: adapter-openai
tags: [openai, models, completions, catalog, tdd, legacy-shim]
dependency_graph:
  requires:
    - internal/adapter/openai skeleton + chat completions (03-01, 03-02)
    - internal/canonical (ModelInfo, ChatRequest/ChatResponse)
    - pool ModelCatalog interface (already declared in adapter.go)
  provides:
    - GET /v1/models (OpenAI model list from pool catalog; "auto" prepended; SC3)
    - POST /v1/completions (legacy text_completion shim; prompt string/array; JSON-only)
    - catalogToModelList render helper (SC3 same-set as /api/tags by construction)
    - chatResponseToTextCompletion render helper
    - promptToMessages wire helper (polymorphic prompt decode)
  affects:
    - internal/adapter/openai/adapter.go (removed stubs; updated docstrings)
    - internal/adapter/openai/handlers.go (two new handlers added)
    - internal/adapter/openai/render.go (modelList/modelInfo/textCompletion/textChoice shapes)
    - internal/adapter/openai/wire.go (completionWireRequest + promptToMessages)
tech_stack:
  added: []
  patterns:
    - TDD RED/GREEN/REFACTOR per task
    - catalogToModelList prepends "auto" (mirror ollama/handlers.go handleTags)
    - stream:true silent downgrade for /v1/completions (JSON-only shim, D-03)
    - T-02-33 engine-error path: slog.Error raw + generic writeError, never echo
    - T-03-23 accept-and-ignore advanced params (logprobs/echo/suffix/best_of/n/max_tokens)
    - T-03-22 modelInfo exposes only id/object/created/owned_by (no internal detail)
key_files:
  created:
    - internal/adapter/openai/models_test.go
    - internal/adapter/openai/completions_test.go
  modified:
    - internal/adapter/openai/handlers.go (handleModels + handleCompletions)
    - internal/adapter/openai/render.go (modelList/modelInfo/catalogToModelList + textCompletion/textChoice/chatResponseToTextCompletion)
    - internal/adapter/openai/wire.go (completionWireRequest + promptToMessages)
    - internal/adapter/openai/adapter.go (stubs removed + docstring updated)
decisions:
  - "promptToMessages joins []string prompt with newline (preserves line boundaries vs space-join)"
  - "handleModels passes nil slice to catalogToModelList when ModelCatalog is nil (not a branch; catalogToModelList handles nil)"
  - "stream:true on /v1/completions is silently set to false before any processing (D-03 JSON-only; no SSE path needed)"
  - "completionWireRequest uses json.RawMessage for logprobs/echo/suffix/best_of/n/max_tokens to accept-and-ignore without 400 on unexpected types"
  - "owned_by is hardcoded to 'kiro' (consistent with the pool backend identity)"
metrics:
  duration: "~30 minutes"
  completed_date: "2026-05-24"
  tasks_completed: 2
  files_changed: 6
---

# Phase 3 Plan 03: OpenAI Models + Completions Summary

GET /v1/models returns the OpenAI model list shape from the injected pool ModelCatalog (same source as /api/tags → SC3 same-set by construction; "auto" always prepended); POST /v1/completions maps a string or array prompt to one canonical user Message, runs the engine, renders the text_completion shape with null logprobs and zero usage, accepts-and-ignores advanced params, silently downgrades stream:true to JSON.

## Tasks Completed

| # | Task | Commit | Status |
|---|------|--------|--------|
| 1 | handleModels + model-list render (D-04, SC3) | cdb5aac | Done |
| 2 | handleCompletions legacy shim + text_completion render (D-03) | 50625d3 | Done |

## What Was Built

**Task 1 — handleModels + model-list render:**
- `render.go`: `modelList{Object string; Data []modelInfo}` and `modelInfo{ID,Object string; Created int64; OwnedBy string}` (RESEARCH.md §Pattern 5; Bifrost-validated field names); `catalogToModelList(models, ownedBy, created)` prepends "auto" entry then maps each canonical.ModelInfo — nil/empty models returns only "auto"
- `handlers.go`: `handleModels(w, _)` — if ModelCatalog non-nil, call Models() and pass to catalogToModelList; else pass nil; writeJSON(modelList). T-03-22: only id/object/created/owned_by exposed
- `models_test.go`: `TestModels` (catalog present → 3 entries in order, nil catalog → only auto), `TestModelListRender` (4 subtests: prepends_auto, catalog_ids_in_order, each_entry_fields, empty_catalog), `TestModels_ModelCatalogSourcing` (proves dynamic sourcing, not static list)

**Task 2 — handleCompletions + text_completion render:**
- `wire.go`: `completionWireRequest{Model, Prompt json.RawMessage, Stream bool, + ignored: Logprobs/Echo/Suffix/BestOf/N/MaxTokens as json.RawMessage}`; `promptToMessages(raw json.RawMessage)` decodes string-first then []string-join-with-newline, returns error on empty/nil (→ 400)
- `render.go`: `textCompletion{ID,Object,Created,Model string; Choices []textChoice; Usage completionUsage}` and `textChoice{Index int; Text,FinishReason string; Logprobs *struct{}}` (always null); `chatResponseToTextCompletion` reuses joinTextContent + mapFinishReason + genMessageID("cmpl-") + zero completionUsage
- `handlers.go`: `handleCompletions` — nil-engine→503; decode+413/400; set stream=false (D-03 downgrade); promptToMessages (err→400); engine.Collect (err→slog raw + generic 500, T-02-33); writeJSON(text_completion)
- `completions_test.go`: `TestCompletions` (8 subtests: basic_string_prompt, array_prompt_joined, ignored_params_no_400, stream_true_downgrade_to_json, nil_engine_503, empty_prompt_400, oversize_body_413, engine_error_500_no_echo), `TestTextCompletionRender` (4 subtests), `TestPromptDecode` (4 subtests)

## Deviations from Plan

None — plan executed exactly as written.

## Known Stubs

None — all three handlers (handleChatCompletions from Plan 02, handleModels and handleCompletions from this plan) are fully implemented. No remaining stubs in the openai adapter.

## Threat Flags

No new threat surface beyond what the plan's threat model covers. All mitigations applied:

- T-03-20 (DoS / body cap): `decodeJSONBody` + 4 MiB `MaxBytesReader` → 413 on /v1/completions — unit tested (`TestCompletions/oversize_body_413`)
- T-03-21 (Info Disclosure / engine error): `slog.Error` raw + `writeError(500, errAPI, "internal error")` — source-verified + unit tested (`TestCompletions/engine_error_500_no_echo`)
- T-03-22 (Info Disclosure / /v1/models response): `modelInfo` struct exposes only id/object/created/owned_by — no internal pool slot, env, or paths
- T-03-23 (Tampering / advanced-param injection): logprobs/echo/best_of/n decode-and-ignore via json.RawMessage fields in completionWireRequest — unit tested (`TestCompletions/ignored_params_no_400`)

## Self-Check: PASSED
