# Kiro and Support Observability — Design

**Date:** 2026-07-27
**Status:** Approved design, awaiting specification review
**Scope:** Kiro CLI logging, Gateway dashboard log tailing, support-bundle log organization and live snapshots, Co-worker diagnostic collection, and Grafana remote-write defaults

## Problem

The Gateway launches Kiro CLI in ACP mode but does not currently give Kiro's
own file log a Gateway-managed destination. Kiro stderr is captured in the
Gateway's structured log, but that is not a substitute for Kiro's native ACP
diagnostics. The Gateway dashboard also cannot select that native log.

Support bundles currently place Gateway logs in one flat directory. They omit
Kiro's native log, omit the Co-worker logs that Hermes uses for troubleshooting,
and do not preserve a point-in-time metrics or ACP-capture snapshot. This makes
it harder to identify which application produced a message and loses valuable
runtime evidence.

Grafana remote write also requires users to copy non-secret endpoint settings
into `overrides.env`, even though those settings are common to every
installation. Only the API key is installation-specific and secret.

## Goals

- Put Kiro's native ACP log in the Gateway log directory by default.
- Run Kiro at its normal logging level by default and make debug logging an
  explicit, restart-based override.
- Let a dashboard user select and live-tail Gateway or Kiro logs.
- Keep Co-worker logs out of the Gateway dashboard because Co-worker already
  presents its own logs.
- Organize support-bundle logs by producing application: Gateway, Kiro, and
  Co-worker.
- Collect the Hermes/Co-worker logs most useful for problem investigation,
  including profile-specific logs and rotations.
- Add timestamped Prometheus and ACP-capture snapshots to the Gateway section
  of a support bundle.
- Make Grafana remote write require only the API key in `overrides.env`; keep
  sending opt-in through the tray toggle.
- Preserve best-effort collection, secret redaction, symlink safety, and the
  existing bundle size cap.

## Current-State Findings

### Kiro ACP logging

The Gateway starts Kiro as:

```text
kiro-cli acp --agent acp_proxy
```

The ACP process inherits the parent environment, and the dotenv loaders accept
arbitrary keys. Kiro's documented logging controls are:

- `KIRO_CHAT_LOG_FILE` for the file destination
- `KIRO_LOG_LEVEL=debug` for debug-level output

Without a configured destination, Kiro uses its platform temporary/runtime
directory. The Gateway therefore has the mechanism to enable native logging,
but it does not currently resolve, advertise, surface, or bundle the log.

### Gateway dashboard logging

The dashboard already exposes an allowlisted, multi-source SSE tail endpoint.
The server publishes source IDs in its snapshot and the browser reconnects its
`EventSource` when the selection changes. Gateway main, boot/error, and
optional chat-trace logs are registered today. Kiro should extend this registry
rather than introduce a second tailing mechanism.

### Hermes/Co-worker diagnostics

Hermes' logging setup and debug tooling identify these core troubleshooting
logs:

- `agent.log` — general agent, tool, and session activity
- `errors.log` — warnings and errors for quick triage
- `gateway.log` — Hermes gateway activity
- `gui.log` — dashboard, websocket, PTY, and TUI gateway activity
- `desktop.log` — Electron startup, backend, update, installer, and Python
  traceback activity
- `mcp-stderr.log` — MCP subprocess stderr

Additional useful logs, when present, are:

- `gateway-shutdown-watchdog.log`
- `dashboard-auth.log`
- `container-boot.log`
- `tool_calls.log`
- rotated forms of the selected logs, such as `.1`, `.2`, and `.3`
- the same diagnostic set below `profiles/*/logs/`

`logs/curator/` contains generated reports rather than primary runtime logs and
is excluded by default.

The tray already resolves the actual Co-worker data home for its “open data
folder” action. That resolved path is the authoritative source for tray-created
support bundles.

## Chosen Approach

Use an explicit diagnostic manifest: each producer has a known destination and
an allowlist of files or filename families. Do not recursively archive every
file below the Gateway or Co-worker data directories, and do not depend on
Hermes' own debug-share command being installed or operational.

This gives the bundle a stable cross-platform structure, avoids unrelated or
large data such as curator reports, and lets the collector prioritize current
diagnostics under the existing archive cap.

## Kiro Logging Configuration

### Resolved destination

Gateway configuration owns a resolved Kiro chat-log path:

1. If `KIRO_CHAT_LOG_FILE` is explicitly configured, use it unchanged.
2. Otherwise use `<GW_HOME>/logs/kiro-chat.log`, which is normally
   `~/.gw/logs/kiro-chat.log`.

The Gateway creates the parent log directory before launching Kiro. The
resolved value is passed as child-process environment to every Kiro ACP process,
including warm-pool workers and stateful sessions. It is not installed globally
with `os.Setenv`, so tests and unrelated child processes are not contaminated.

The resolved destination is logged at Gateway startup without logging any
secret environment values.

### Log level

The generated configuration does not set `KIRO_LOG_LEVEL`. This preserves
Kiro's normal default logging level.

For a Kiro-level investigation, a user adds this to `overrides.env`:

```dotenv
KIRO_LOG_LEVEL=debug
```

The user then restarts the Gateway so newly spawned and pooled Kiro processes
inherit the setting. Removing the override and restarting returns Kiro to its
normal level.

## Gateway Dashboard Log Sources

Register the resolved Kiro log in the existing dashboard source registry. The
selector uses friendly labels while the SSE endpoint continues to accept only
server-issued, allowlisted source IDs.

The visible sources are:

- **Gateway** — the primary Gateway log
- **Gateway boot/errors** — the existing boot/error log source
- **Kiro** — the resolved Kiro native chat log
- **Gateway chat trace** — only when the existing chat-trace configuration is
  enabled

Co-worker is deliberately absent from the Gateway dashboard.

Selecting a source closes the current `EventSource` and opens the existing
stream endpoint with the new source ID. A missing or not-yet-created Kiro log
does not remove the source from the selector. The stream waits/reconnects and
starts producing entries when the file appears. The browser never supplies a
filesystem path.

## Support Bundle Layout

All collected logs move below application-specific subdirectories:

```text
logs/
├── gateway/
│   ├── gateway.log
│   ├── gateway-boot.log
│   ├── gateway-boot-stdout.log
│   ├── gateway-boot-stderr.log
│   ├── gateway-chat-trace.log
│   ├── metrics-snapshot-<YYYYMMDD-HHMMSSZ>.prom
│   ├── acp-capture-<YYYYMMDD-HHMMSSZ>.json
│   └── <selected Gateway rotations>
├── kiro/
│   ├── kiro-chat.log
│   └── <selected Kiro rotations>
└── co-worker/
    ├── agent.log
    ├── errors.log
    ├── gateway.log
    ├── gui.log
    ├── desktop.log
    ├── mcp-stderr.log
    ├── gateway-shutdown-watchdog.log
    ├── dashboard-auth.log
    ├── container-boot.log
    ├── tool_calls.log
    ├── <selected rotations>
    └── profiles/<profile>/logs/<selected diagnostic logs and rotations>
```

Only files that exist are included. Existing bundle sections such as `env/`,
`health/`, `system/`, and `tray/` remain unchanged.

### Co-worker data-home resolution

When the tray creates a bundle, it passes the same resolved Co-worker data home
used by the existing “open data folder” action to the support command. This
avoids duplicating platform and branding discovery logic in the scripts.

For direct command-line collection, the support command accepts an explicit
Co-worker home and otherwise uses `HERMES_HOME` when it is set. It does not scan
or guess among unrelated product data directories. If no home can be resolved,
or its `logs` directory does not exist, Gateway and Kiro collection continues
and the omission is recorded in `MANIFEST.txt`.

### File selection and size policy

- Collect regular files only; never follow or archive symlinks.
- Match only the approved diagnostic basenames and their rotation suffixes.
- Exclude `logs/curator/`.
- Preserve the relative profile path so ownership remains clear.
- Prefer current logs over rotations.
- When the existing size cap requires dropping entries, discard the oldest
  rotations first and record every omission or truncation in the manifest.
- Preserve the manifest and small live snapshots ahead of rotated logs.

The same rules and resulting archive paths apply to the POSIX and PowerShell
implementations.

## Live Gateway Snapshots

One UTC timestamp is computed for the bundle and reused in snapshot filenames.
Snapshot requests are read-only and never change the Gateway's capture or
remote-write state.

### Prometheus snapshot

When the Gateway is reachable, always attempt `GET /metrics` and write the
response to:

```text
logs/gateway/metrics-snapshot-<YYYYMMDD-HHMMSSZ>.prom
```

If the endpoint is unavailable or returns an error, omit the file and record
the reason in `MANIFEST.txt`. Metrics collection failure does not fail the
bundle.

### ACP capture export

Query the existing ACP-capture endpoint. Add a support-export/redaction mode to
that endpoint which snapshots the ring buffer and returns valid JSON with
known secrets removed. The redactor:

- recursively masks values under keys matching token, key, secret, password,
  or passphrase naming conventions;
- scrubs authorization, bearer-token, and API-key patterns inside strings;
- parses and redacts JSON held in captured `params` strings when possible;
- leaves malformed/non-JSON data as a scrubbed string rather than emitting
  invalid JSON.

If the response says capture is enabled, write the complete returned snapshot
to:

```text
logs/gateway/acp-capture-<YYYYMMDD-HHMMSSZ>.json
```

If capture is disabled, do not create an empty export. If the endpoint is
unavailable, omit the file. Record `captured`, `disabled`, or `unavailable` in
the manifest. Exporting does not clear or mutate the in-memory ring.

Capture frames can contain prompts, tool arguments, model output, filesystem
paths, and other user data that cannot be identified as a secret automatically.
The manifest therefore includes an explicit warning that capture exports are
sensitive and should be reviewed before sharing, even after known-secret
redaction.

Applying the existing line-oriented log redactor directly to serialized JSON
is prohibited because regex replacement can corrupt JSON syntax. The endpoint's
support mode provides JSON-preserving redaction; the scripts validate that a
successful response is JSON before archiving it.

## Grafana Remote-Write Defaults

Generated default `.env` files contain these active, non-secret settings:

```dotenv
GW_METRICS_REMOTE_WRITE_URL=https://prometheus-prod-66-prod-us-east-3.grafana.net/api/prom/push
GW_METRICS_REMOTE_WRITE_USER=3370048
GW_METRICS_REMOTE_WRITE_INTERVAL_SEC=30
```

The secret remains absent from generated defaults. The only value a user must
add to `overrides.env` is:

```dotenv
GW_METRICS_REMOTE_WRITE_TOKEN=<Grafana API key>
```

The existing `GW_METRICS_REMOTE_WRITE_TOKEN` name remains the compatibility
contract even though the Grafana credential is described to users as an API
key.

Remote sending remains disabled by default. The user enables or disables it
with the existing tray checkbox. The preference is persisted in tray state,
but the endpoint, user, or token is never copied into `tray.json`. The writer
continues to reload `overrides.env`, `.env`, and process environment using its
existing precedence, so changing the API key does not require storing it in the
tray.

If the user enables sending without a token, the writer remains idle and the
tray presents a missing-API-key notification. The notification occurs when
enabling or on a relevant state transition, not every polling interval. Turning
the checkbox off stops sending without removing configuration.

The effective-environment support artifact includes the non-secret defaults
and only a masked representation of `GW_METRICS_REMOTE_WRITE_TOKEN`. Redaction
helpers treat this key explicitly as a secret in addition to their general
secret-key rules.

## Failure Behavior

- Failure to prepare Kiro's configured log directory is reported clearly at
  startup; the Gateway does not silently substitute a different destination.
- A missing Kiro file is a valid pre-write state for the dashboard tailer.
- Support collection is best-effort. One unreadable file, unreachable endpoint,
  or missing application data directory does not prevent other artifacts from
  being archived.
- Collection warnings and skipped artifacts are listed in `MANIFEST.txt`.
- Metrics or capture snapshot failures do not mutate runtime settings and do
  not cause support-bundle failure.
- Secret values are not printed in tray notifications, error messages, logs,
  or the manifest.

## Testing Strategy

Implementation follows red-green-refactor test-driven development.

### Go tests

- Default Kiro destination resolves below `GW_HOME/logs`.
- Explicit `KIRO_CHAT_LOG_FILE` wins unchanged.
- Resolved Kiro environment reaches every ACP launch path without a global
  environment mutation.
- No default `KIRO_LOG_LEVEL` is injected; an explicit debug override is
  inherited.
- Dashboard snapshots advertise friendly Gateway and Kiro sources, conditionally
  advertise chat trace, and never advertise Co-worker.
- The SSE source remains allowlisted and cannot be replaced by an arbitrary
  path.
- Missing Kiro files can be selected and tailed once created.
- Support-mode ACP export preserves valid JSON, snapshots without mutation, and
  redacts nested keys, bearer strings, headers, and JSON-encoded params.
- Capture disabled and empty-buffer states are represented unambiguously.
- Tray remote-write configuration uses the default URL, user, and interval,
  keeps the token external to tray state, and reports a missing token without
  recurring notification spam.
- Tray support invocation passes its already-resolved Co-worker data home.

### POSIX and PowerShell support tests

- Archives contain `logs/gateway`, `logs/kiro`, and `logs/co-worker` with
  equivalent structure on both platforms.
- Approved current logs, rotations, ancillary Co-worker logs, and profile logs
  are selected; curator and unrelated files are excluded.
- Symlinks are skipped.
- Explicit Co-worker home and `HERMES_HOME` fallback behave as designed.
- Missing Co-worker data is a manifest warning, not a bundle failure.
- Reachable metrics create a timestamped `.prom` snapshot.
- Enabled capture creates a timestamped, valid, redacted `.json` export;
  disabled or unavailable capture is recorded without an empty file.
- Gateway, Kiro, and Co-worker current logs remain clearly separated even when
  they share a basename such as `gateway.log`.
- The remote-write token is absent from every extracted artifact.
- The archive cap keeps current diagnostics and drops oldest rotations first,
  with manifest accounting.

### Configuration and smoke tests

- Every generated default `.env` template contains the active Grafana URL,
  user, and 30-second interval exactly once.
- Documentation and examples direct the user to put only
  `GW_METRICS_REMOTE_WRITE_TOKEN` in `overrides.env`.
- Manual browser smoke confirms changing the selector starts tailing the chosen
  Gateway or Kiro source without a page reload.
- Manual tray smoke confirms enable, missing-key feedback, successful send,
  disable, and support-bundle creation from the detected Co-worker install.

## Acceptance Criteria

1. A default Gateway launch sends every Kiro ACP process's native log to
   `<GW_HOME>/logs/kiro-chat.log` at Kiro's normal log level.
2. `KIRO_LOG_LEVEL=debug` in `overrides.env` takes effect after restart.
3. The Gateway dashboard can select and tail Gateway and Kiro logs, but exposes
   no Co-worker log source.
4. Support archives group logs under `gateway`, `kiro`, and `co-worker` and
   include the approved Hermes diagnostic families without following symlinks
   or collecting curator reports.
5. A reachable Gateway contributes a timestamped metrics snapshot; an enabled
   capture buffer contributes a timestamped valid JSON export under the
   Gateway folder.
6. Capture export does not mutate the buffer and redacts known secrets while
   warning that captured user content may remain sensitive.
7. Missing optional sources create manifest warnings but do not block a useful
   bundle.
8. Generated defaults provide the Grafana URL, user, and 30-second interval;
   the user supplies only `GW_METRICS_REMOTE_WRITE_TOKEN` and opts in with the
   tray toggle.
9. POSIX and Windows implementations pass equivalent automated coverage.

## Out of Scope

- Displaying Co-worker logs in the Gateway dashboard.
- Enabling Kiro debug logging by default or toggling it without restarting the
  Gateway.
- Recursively archiving the entire Co-worker data directory.
- Including curator reports by default.
- Uploading support bundles automatically.
- Persisting the Grafana API key in tray state.
- Clearing, pausing, enabling, or otherwise mutating ACP capture as part of
  support collection.

## Sources

- Kiro ACP logging: <https://kiro.dev/docs/cli/acp/#logging>
- Hermes logging implementation:
  <https://github.com/NousResearch/hermes-agent/blob/main/hermes_logging.py>
- Hermes CLI log inspection:
  <https://github.com/NousResearch/hermes-agent/blob/main/hermes_cli/logs.py>
- Hermes debug collection:
  <https://github.com/NousResearch/hermes-agent/blob/main/hermes_cli/debug.py>
- Hermes desktop logging and data-home behavior:
  <https://github.com/NousResearch/hermes-agent/blob/main/apps/desktop/electron/main.ts>

## Open Questions

None at design time. Exact file-level task ordering and the smallest safe
interfaces for child-process environment propagation and support export will be
specified in the implementation plan after this document is reviewed.
