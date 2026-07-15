---
quick_id: 260715-i8k
slug: add-acp-capture-status-row-to-admin-abou
date: 2026-07-15
title: Add ACP capture status row to admin About page Feature Flags
---

# Quick Task: ACP capture status on the admin About page

## Goal
Surface `ACP_CAPTURE` on/off in the gateway admin About page's **Feature Flags**
card, so an operator can see whether raw-frame ACP capture is enabled without
grepping env vars. Mirrors the existing Chat-trace row (with a SENSITIVE badge,
because the capture ring holds raw ACP frames — prompt/response content — exposed
at `/admin/api/acp-capture`).

## Approach (additive, 3 files)
The data is already available to the admin handler: `Deps.AcpCapture` is a non-nil
`AcpCaptureSource` iff `ACP_CAPTURE` is enabled (canonical signal per
`internal/admin/capture.go`). No cmd-layer/main.go change is needed.

1. `internal/admin/admin.go` — add `AcpCaptureEnabled bool` to the `aboutData`
   view-model; populate it in `aboutHandler` via `h.deps.AcpCapture != nil`.
2. `internal/admin/templates/about.html.tmpl` — add an `ACP capture` row to the
   Feature Flags `<dl>` (after Chat trace), rendering on/off with a SENSITIVE
   badge when on.
3. `internal/admin/handlers_test.go` — assert the new label + on-state renders on
   the About page (extend the existing feature-flag render assertion).

## Constraints
- Additive only; no behavior change beyond the new read-only display row.
- gofumpt-clean, `go vet` clean, `go test ./internal/admin/` passes.
- Branch off main (post v2.7.0); do NOT push or merge — stop for review.

## Acceptance
- About page shows `ACP capture: on/off` in Feature Flags, reflecting `ACP_CAPTURE`.
- `go test ./internal/admin/` green; gofumpt/vet clean.
