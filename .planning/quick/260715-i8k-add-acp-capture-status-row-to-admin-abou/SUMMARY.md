---
quick_id: 260715-i8k
slug: add-acp-capture-status-row-to-admin-abou
date: 2026-07-15
status: complete
---

# Summary: ACP capture status on the admin About page

## What changed
Added an **ACP capture** row to the gateway admin About page's Feature Flags
card (`/admin/about`), reflecting whether `ACP_CAPTURE` is on/off. Renders `on`
with a SENSITIVE badge (like Chat trace) or `off`, because the capture ring
holds raw ACP frames (prompt/response content) exposed at
`/admin/api/acp-capture`.

## Files
- `internal/admin/admin.go` — `aboutData.AcpCaptureEnabled bool`; set in
  `aboutHandler` via `h.deps.AcpCapture != nil` (canonical enabled signal; no
  cmd-layer change needed).
- `internal/admin/templates/about.html.tmpl` — new `<dt>ACP capture</dt>` row in
  the Feature Flags `<dl>`, after Chat trace.
- `internal/admin/capture_test.go` — `TestAbout_AcpCaptureRow` asserts the on
  row (wired source) and off row (nil source) render on `/about`.

## Verification
- `gofumpt -l internal/admin` clean; `go vet ./internal/admin/` clean.
- `go test ./internal/admin/` PASS (incl. new `TestAbout_AcpCaptureRow`).
- `CGO_ENABLED=0 go build ./cmd/otto-gateway` OK.

## Follow-up: discoverability (commit 3a97046)
Made ACP_CAPTURE discoverable alongside the other env vars (minimal scope — no
new CLI flag; operators enable via overrides.env):
- `scripts/.env.example` — commented diagnostics block for ACP_CAPTURE /
  ACP_CAPTURE_SIZE (default off) so `gw init`/`gw upgrade-env` carry the keys
  into the generated `.env`; notes enable-via-overrides.env + SENSITIVE frames.
- `internal/admin/admin.go` docsHandler — ACP_CAPTURE (on/off) + ACP_CAPTURE_SIZE
  rows added to the /admin/docs env table; `TestDocs_AcpCaptureRows` asserts both.

## Notes
- Read-only display; no behavior change. `ACP_CAPTURE` still requires a gateway
  restart to take effect (config loads at startup; no hot-reload).
- Branch `quick/260715-i8k-acp-capture-about-flag` off `main` (post v2.7.0).
  Shipped as v2.7.1.
