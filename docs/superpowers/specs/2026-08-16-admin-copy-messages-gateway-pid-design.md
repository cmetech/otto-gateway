# Admin Copy Messages and Gateway PID Design

**Date:** 2026-08-16

## Goal

Make ACP capture diagnostics easier to share and expose the gateway process ID
in the dashboard overview without disturbing the existing summary-widget grid.

## Scope

This change has two independent user-visible behaviors:

1. Add a **Copy Messages** control to the ACP Capture (diagnostics) card.
2. Add a gateway PID header above the Status widget row in the overview card.

No capture API shape or authorization behavior changes. The snapshot API gains
one additive integer field, `gateway_pid`.

## ACP Copy Messages

### Interface

Place a button labeled **Copy Messages** beside **Show Messages**. It uses the
existing `gw-btn gw-btn--icon` control styling and an inline clipboard icon that
matches the other ACP capture controls.

The button has a dedicated label element and the following states:

- `Copy Messages`: idle and ready.
- `Copying…`: the capture response is being fetched or written to the clipboard;
  the button is disabled.
- `Copied`: the clipboard write succeeded.
- `Copy failed`: the response fetch or clipboard write failed.

The terminal success or failure label remains visible for two seconds, then
returns to `Copy Messages`. A new click cancels any older pending reset timer so
stale feedback cannot overwrite a newer operation.

### Data flow

On click, browser JavaScript fetches
`/admin/api/acp-capture?pretty=1` with `Accept: application/json`, reads the
successful response as text, and passes that text unchanged to
`navigator.clipboard.writeText`.

Copying the text unchanged guarantees that **Copy Messages** and
**Show Messages** expose the same full, pretty-printed response, including
capture state, count/capacity, and all frames. The client does not parse and
re-serialize the response.

### Errors and compatibility

An HTTP error, network failure, unavailable Clipboard API, or rejected
clipboard operation produces the bounded `Copy failed` label. Error details
are not rendered into the page. The button is always re-enabled after the
operation settles.

No legacy clipboard fallback is added. The admin UI is served from the local
gateway and the modern Clipboard API is the supported path.

## Gateway PID Header

### Snapshot contract

Add `GatewayPID int` with JSON name `gateway_pid` to `admin.Snapshot`.
`snapshotHandler` sets it from `os.Getpid()` on every response. A running
gateway always publishes a positive value.

The PID is ordinary process metadata, consistent with the worker PIDs already
published in `pool.slots[].pid`.

### Interface

Add a dedicated header row as the first visible child of the overview card,
above `.gw-summary-items`. It displays:

`Gateway PID 12345`

The label uses subdued dashboard-header styling and the value uses monospace
diagnostic styling. A `data-gateway-pid` hook receives the value during normal
snapshot rendering. Until the first successful snapshot it displays an em
dash.

The header spans the full card width and remains structurally separate from the
seven-column summary grid, so Status stays the first widget and existing
responsive grid behavior is unchanged. The existing live-update zone remains
below the header alongside the summary content.

## Testing

Implementation follows a red-green-refactor cycle.

- Go snapshot test: `gateway_pid` is present and positive.
- Go page scaffold test: the overview contains the gateway PID header and
  `data-gateway-pid` hook.
- JavaScript success test: clicking **Copy Messages** fetches the pretty capture
  URL, copies the response text unchanged, shows `Copied`, and resets.
- JavaScript failure tests: non-success HTTP and rejected clipboard writes show
  `Copy failed`, re-enable the control, and reset.
- JavaScript summary test: snapshot rendering writes `gateway_pid` to the PID
  hook.

The focused Node and Go admin tests run first, followed by the repository's
normal build and admin-package test gates.

## Out of Scope

- Copying only the `frames` array.
- Redacting or otherwise transforming the copied capture response.
- Making the gateway PID itself copyable.
- Adding a server-side clipboard or download endpoint.
