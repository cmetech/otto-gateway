# Log Tail Source Status and Kiro Logging Design

**Date:** 2026-08-05
**Status:** Approved for implementation planning
**Scope:** Kiro native file-log configuration, initial log-tail backfill, per-source file health, SSE status delivery, dashboard empty/filter states, and operator documentation

## Problem

The Gateway dashboard can connect successfully to the Kiro log source while showing no entries. A successful `EventSource` connection proves only that the HTTP stream is open; it does not prove that the selected file exists, is readable, contains data, or is receiving writes.

The installed Kiro CLI 2.16.1 creates `KIRO_CHAT_LOG_FILE` but writes zero bytes when `KIRO_LOG_LEVEL` is unset. The Gateway currently passes only the destination to Kiro children, even though the operator documentation describes an unset level as normal Kiro logging.

The tailer also opens every source at EOF. This makes a static source such as the Gateway boot/error log appear empty even when it already contains useful records. The dashboard collapses all of these conditions into `Waiting for log activity…`, so an operator cannot distinguish healthy idleness from a configuration or file problem.

## Goals

- Make Kiro native file logging active by default at `INFO`.
- Let operators explicitly select another supported Kiro log level.
- Ensure every pooled and dedicated Kiro child receives the resolved level and log destination.
- Show up to 500 recent complete records when a source is selected for the first time.
- Preserve live streaming and rotation safety.
- Distinguish opening, missing, unreadable, empty, watching, disconnected, and filter-empty states.
- Deliver source health through the existing SSE connection instead of adding another polling endpoint.
- Prevent missing or unreadable sources from generating a warning every 250 milliseconds.
- Keep filesystem paths out of browser-visible status payloads and messages.

## Non-Goals

- Exposing an `OFF` state for Kiro native file logging.
- Tailing Co-worker logs in the Gateway dashboard.
- Replaying historical data from a newly rotated file.
- Changing Kiro's log format or the Gateway's existing client-side parser.
- Adding authentication or changing the existing strict-loopback boundary for the Kiro source.
- Exposing raw file errors or absolute paths to the browser.

## Chosen Approach

Source health becomes part of the existing log-tail SSE protocol. Each shared tailer owns the state of its backing file and publishes typed status transitions alongside log lines. This keeps the browser's connection state and file state synchronized without waiting for the admin snapshot poll.

Alternatives rejected:

- Adding file metadata to the admin snapshot would make status updates lag behind the file tailer's own observations.
- Inferring health from elapsed time in JavaScript cannot distinguish a healthy empty file from a missing or unreadable file.
- A separate log-status endpoint would duplicate source selection, authorization posture, and lifecycle management already present in the SSE handler.

## Configuration Contract

Add `Config.KiroLogLevel` as the resolved Kiro native log level.

- `KIRO_LOG_LEVEL` unset or empty resolves to `INFO`.
- Explicit values are normalized case-insensitively to uppercase.
- Supported values are `ERROR`, `WARN`, `INFO`, `DEBUG`, and `TRACE`.
- Any other non-empty value is a startup configuration error naming `KIRO_LOG_LEVEL` and the supported values.
- There is no value that disables Kiro native file logging.
- `kiroProcessEnv` passes both `KIRO_CHAT_LOG_FILE` and the resolved `KIRO_LOG_LEVEL` to every child.
- Explicit operator values continue to win over the `INFO` default.

The startup `kiro launch configured` record includes the effective level. The operator guide, generated environment template, and admin configuration reference describe `INFO` as the default and list all supported overrides.

## Tailer Lifecycle and Initial Backfill

The current separate `Subscribe()` and `Snapshot()` calls become one atomic attachment operation. Attachment registers the subscriber and captures a coherent ring snapshot while holding the same lock used by broadcasts. A subscriber therefore cannot miss a line between subscription and snapshot, and a line cannot appear in both paths because of an attachment race.

On the first successful open for a tailer instance:

1. Read backward from the current file, bounded to the most recent 500 complete records and a fixed maximum input window.
2. Discard a leading fragment when the bounded read begins in the middle of a record.
3. Apply the existing one-megabyte per-record cap.
4. Populate the shared ring in FIFO order.
5. Deliver those records through the coherent attachment/backfill path.
6. Preserve any trailing unterminated record as the live reader's partial-line carry so it is emitted only if a later append completes it.

The maximum input window will be a named constant and large enough for ordinary structured logs while preventing a first dashboard connection from scanning an unbounded file. Reaching the byte window may yield fewer than 500 records; the UI contract is therefore "up to 500 recent complete records."

After initial attachment, polling remains at the existing cadence. Rotation or truncation closes the old handle and opens the replacement at EOF. Historical records in the replacement are not replayed, and any partial record from the old inode is discarded.

## Source Status Model

Each tailer maintains a `TailStatus` value with the following browser-safe fields:

- `state`: one of `opening`, `missing`, `unreadable`, `empty`, or `watching`
- `size_bytes`: current observed file size when available
- `modified_at`: current observed modification timestamp when available
- `level`: effective native level for sources that have one, currently Kiro

The status does not contain the path or raw operating-system error.

State meanings:

- `opening`: the source is registered but the first file check has not completed.
- `missing`: the configured file does not exist yet; the tailer continues retrying.
- `unreadable`: the path exists or resolution was attempted but it cannot currently be opened, sought, or read.
- `empty`: the file opened successfully and its size is zero.
- `watching`: the file opened successfully and is being followed. This state may have no rendered rows when the file contains only an incomplete record or when all rows are filtered.

The first subscriber receives the current status immediately. Tailer state transitions are broadcast to every subscriber. Repeated observations of the same state and metadata do not produce duplicate status events.

## Warning and Recovery Logging

A missing or unreadable source logs immediately when the failure state or error class changes. Continued identical failures are rate-limited to a named interval instead of logging once per 250-millisecond poll. A successful reopen after a failure logs one recovery record and resets the warning limiter.

The implementation uses an injectable clock or equivalent deterministic seam so rate limiting and recovery are tested without wall-clock sleeps.

## SSE Contract

The existing endpoint remains:

```text
GET /admin/logs/stream?source=<allowlisted-source-id>
```

It emits three event types:

- `status`: JSON-encoded `TailStatus`
- `log`: one log record using the existing safe multiline framing
- `ping`: the existing empty keepalive

The handler writes and flushes the initial `status` event immediately, even when there is no backfill. This lets `EventSource` reach its open state promptly and prevents an empty source from waiting for the keepalive interval before the dashboard knows it is connected.

Unknown sources and source-registry mismatches retain the existing pre-stream `400` behavior. Missing and unreadable files do not close the SSE stream because the tailer can recover while the dashboard remains connected.

## Dashboard Behavior

Transport state remains in the panel header and uses friendly source labels:

- `Connecting to Kiro…`
- `Connected — Kiro`
- `Log stream disconnected — reconnecting…`

The viewport empty-state copy is driven by the latest file status:

- `opening`: `Checking log source…`
- `missing`: `Log file has not been created yet. Watching for it…`
- `unreadable`: `Log file cannot be read. Check its permissions.`
- `empty`, Kiro: `Kiro log is empty. Logging is configured at {LEVEL}; waiting for the first entry.`
- `empty`, other source: `Log file is empty. Waiting for the first entry.`
- `watching` with no rows: `Connected and watching for new complete log entries.`
- rows received but all hidden: `Log entries were received, but none match the current filters.`

The empty-state element remains inside the existing accessible live region. It is hidden only when at least one rendered row is visible. Changing the level or regex filter recomputes both row visibility and the empty-state copy. Clearing or switching a source resets rows, buffered entries, deduplication state, and the source status before opening the next stream.

Pause/resume behavior and the `{N} new` badge remain unchanged. Receiving a line while paused buffers it normally; the badge communicates buffered activity until resume renders the rows and recomputes the empty state.

## Data Flow

1. Configuration resolves the Kiro log destination and level before pool warmup.
2. Pool and dedicated-session launch paths receive identical resolved environment entries.
3. The admin source registry associates each allowlisted source with its path, friendly label, and optional effective level.
4. The browser selects only the allowlisted source ID.
5. The SSE handler atomically attaches to that source's shared tailer.
6. The handler flushes the current status and coherent ring snapshot.
7. The tailer broadcasts later status transitions and complete records.
8. The browser separately renders transport health, file health, rows, and filter-empty state.

## Error Handling

- Invalid Kiro log levels fail startup before any child process is created.
- Missing files remain recoverable and do not terminate SSE.
- Permission, seek, stat, and read failures transition to `unreadable`, remain recoverable, and do not expose raw errors to the browser.
- Oversized records retain the existing truncation behavior.
- Initial-backfill failures transition to `unreadable`; the tailer retries through the normal poll loop.
- Rotation preserves the connection and does not replay replacement-file history.
- Slow subscribers retain the existing non-blocking drop behavior for log lines. Status delivery is coalesced so the latest state remains observable without blocking the shared tailer.

## Testing Strategy

Implementation follows red-green-refactor cycles for each behavior group.

### Configuration and launch tests

- Unset and empty `KIRO_LOG_LEVEL` resolve to `INFO`.
- Explicit supported values normalize and survive configuration loading.
- Invalid values fail with a configuration-named error.
- Both `KIRO_CHAT_LOG_FILE` and `KIRO_LOG_LEVEL` reach actual child environments.
- Pool and dedicated-session config projections receive identical environment entries.
- Startup records contain the effective non-secret level.

### Tailer tests

- A pre-existing file produces up to the latest 500 complete records on first attachment.
- A bounded read starting mid-record discards the leading fragment.
- A trailing partial record is emitted once when a later append terminates it.
- Empty, missing, and unreadable files produce distinct status states.
- File creation and permission recovery transition to the healthy state.
- Repeated identical failures are warning-rate-limited; changed failures and recovery log immediately.
- Live appends, slow-subscriber behavior, truncation, and rotation continue to work.
- Rotation does not replay historical replacement-file records.
- Atomic attachment cannot miss or duplicate a record across snapshot and live delivery.

### SSE tests

- Initial status is flushed for an empty source without waiting for a ping.
- Status JSON contains only the browser-safe schema.
- Backfill precedes later live records.
- Status transitions share the stream with `log` and `ping` events.
- Unknown-source and registry-mismatch responses remain unchanged.
- Context cancellation and gateway shutdown still release all subscriptions and goroutines.

### Browser tests

- Friendly labels drive connection copy.
- Every file state renders the specified message.
- Kiro empty copy includes the effective level.
- A first visible row hides the file-state placeholder.
- Filters that hide every row show the filter-empty message; relaxing the filter restores rows and hides it.
- Source switching resets status and opens the correct allowlisted stream.
- Disconnect/reconnect copy and backfill deduplication remain correct.

### Verification

- Targeted Go tests for config, main wiring, admin tailer, and SSE.
- Node dashboard tests.
- Full Go test suite.
- Race-enabled admin tests.
- Repository lint/build commands used by the existing project.
- Manual runtime smoke check against the installed Kiro CLI showing non-empty output with the default `INFO` configuration.

## Acceptance Criteria

- A default Gateway launch gives every Kiro child `KIRO_LOG_LEVEL=INFO` and a native log destination.
- Selecting Kiro on a healthy default installation shows existing recent entries or a precise empty/file-health explanation.
- Selecting Gateway boot/errors shows its recent pre-existing records.
- Missing and unreadable sources are distinguishable without inspecting Gateway logs.
- No healthy or failed file state is represented solely as `Waiting for log activity…`.
- No unchanged file-open failure produces warnings at the 250-millisecond polling cadence.
- Source switching, live append delivery, rotation recovery, pause/resume, filters, and reconnect behavior remain functional.
