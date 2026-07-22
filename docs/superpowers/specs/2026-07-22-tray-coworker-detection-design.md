# Tray Co-Worker Detection Design

**Date:** 2026-07-22  
**Status:** Awaiting written-spec review  
**Scope:** Gateway tray desktop detection, Advanced folder actions, and tray icon state behavior

## Problem

The Gateway tray currently assumes the installed Co-Worker is OTTO. That fixed identity is reused for process detection, Start and Stop, and the Advanced menu's App and Data folder actions. A running LOOP24 desktop is therefore missed or confused with a co-installed OTTO desktop, and the fallback data path incorrectly becomes `%LOCALAPPDATA%\otto`.

The Windows tray also displays legacy hollow-`O` status assets while the gateway is stopped, starting, degraded, or in error. It changes to the blue Gateway icon only after the gateway reaches the running state, which looks like an OTTO-to-Gateway brand switch during startup.

The tray can start before any Co-Worker application. It must not infer a brand merely because a branded app is installed, and it must not leave folder actions enabled with stale paths after the selected process exits.

## Goals

- Use the blue Gateway icon for every gateway state. Status remains visible in the tray header and tooltip.
- Discover compatible branded Co-Worker installations from their packaged `brand.json` descriptors without hardcoding OTTO or LOOP24 as the active brand.
- Select a Co-Worker only when exactly one compatible branded application is running.
- Keep Co-Worker App and Data folder actions disabled until that active application is detected.
- Continuously re-detect the active application and provide a manual refresh action.
- Use one resolved identity snapshot for status, Start, Stop, App Folder, and Data Folder behavior.
- Preserve the always-available Gateway folder action for `GW_HOME` (`~/.gw` by default).
- Fail safely when multiple compatible applications are running or the installed state is ambiguous.

## Non-Goals

- The tray will not adopt a Co-Worker brand icon or visual identity.
- The tray will not add a permanent brand preference or require a new environment variable.
- The tray will not select LOOP24 or OTTO by fixed priority.
- The tray will not recursively scan arbitrary disks or trust paths supplied by `brand.json`.
- The tray will not remove the existing Co-Worker install, Start, or Stop features.
- This change does not redesign gateway process detection, `GW_HOME`, or the gateway wrapper.

## Architecture

The existing desktop poller remains the owner of Co-Worker state. Every three seconds it discovers installed branded candidates, checks their process state, resolves one active candidate if possible, and sends an immutable result to the tray UI loop. The same serialized probe handles manual refresh requests so periodic and user-triggered detection cannot overlap.

The UI loop is the only code that changes menu titles and enabled state. Folder callbacks consume the latest immutable resolution snapshot and revalidate it before opening Explorer or Finder. The snapshot is cleared as soon as the active process is no longer detected.

The generic Gateway icon is independent of this desktop-resolution flow. Brand descriptors influence desktop actions only.

## Descriptor-Backed Discovery

Packaged Hermes brands ship a schema-versioned `resources/brand.json`. The LOOP24 descriptor includes values equivalent to:

```json
{
  "schemaVersion": 1,
  "slug": "loop24",
  "displayName": "LOOP24",
  "homeDir": ".loop24",
  "gateway": "otto"
}
```

The tray searches only immediate application directories beneath the established installation roots.

### Windows roots

- `%LOCALAPPDATA%\Programs\*\resources\brand.json`
- `%LOCALAPPDATA%\*\resources\brand.json`
- `%PROGRAMFILES%\Programs\*\resources\brand.json`
- `%PROGRAMFILES%\*\resources\brand.json`
- `%PROGRAMFILES(X86)%\Programs\*\resources\brand.json`
- `%PROGRAMFILES(X86)%\*\resources\brand.json`

### macOS roots

- `/Applications/*.app/Contents/Resources/brand.json`
- `~/Applications/*.app/Contents/Resources/brand.json`

Discovery is shallow and bounded. Duplicate paths are removed before descriptors are read.

A descriptor is accepted only when:

- `schemaVersion` equals `1`.
- `slug` matches `^[a-z][a-z0-9-]{0,63}$`.
- `displayName` matches the tray's existing bounded safe-name policy.
- `homeDir` is exactly `.` followed by `slug`.
- `gateway` equals `otto` case-insensitively.
- The expected executable exists in the descriptor's owning application directory: `<displayName>.exe` on Windows or the matching executable inside `<displayName>.app` on macOS.

Descriptor text never supplies an executable or directory path. The tray derives paths from the bounded directory it discovered and validates the expected executable there.

Older OTTO installations that predate `brand.json` retain a compatibility candidate at the current well-known OTTO locations. That fallback is used only when no descriptor-backed candidate represents the same executable.

## Resolution and State Model

Each candidate contains, as one value:

```text
display name
slug
home directory name
executable name
absolute application path
absolute descriptor path, when present
running state
```

Resolution uses these rules:

1. Exactly one compatible candidate running: select it as active.
2. No candidate running and exactly one candidate installed: record it as installed but stopped. It may enable Start, but it does not enable either folder action.
3. No candidate running and multiple candidates installed: ambiguous; select none.
4. Multiple compatible candidates running: ambiguous; select none.
5. No valid candidate: not installed; select none.
6. An unexpected read failure beneath an existing search root, or a process-enumeration failure: detection error; select none. A search root that does not exist is normal and is skipped.

The desktop states presented to the UI are:

- `Detecting`: a probe is in progress and no prior active result is trusted.
- `NotInstalled`: no compatible installation was found.
- `Stopped`: exactly one compatible app is installed but not running.
- `Running`: exactly one compatible app is running and selected.
- `Ambiguous`: multiple installed or running candidates prevent a safe selection.
- `DetectionError`: discovery could not produce a trustworthy result.

## Polling and Manual Refresh

The existing three-second desktop poll remains active for the tray lifetime. It detects launches and exits without requiring a tray restart.

Advanced gains an always-enabled `Refresh Co-Worker Detection` item. Activating it requests an immediate probe through the same detector used by the timer. Refresh requests are coalesced while a probe is already running. The menu shows `Co-Worker · detecting…` during an explicit refresh.

Successful refreshes do not display notifications. Ambiguous results and detection errors are explained in the Co-Worker status line; a notification is reserved for a folder or process action the user explicitly requested and that could not be completed.

## Menu Behavior

The Gateway folder is always enabled when its resolved directory exists. Co-Worker folder actions follow the active-running state only.

| State | App Folder | Data Folder | Gateway Folder |
|---|---|---|---|
| Detecting | Disabled | Disabled | Enabled |
| Not installed | Disabled | Disabled | Enabled |
| Installed but stopped | Disabled | Disabled | Enabled |
| Exactly one running | Enabled for selected brand | Enabled for selected brand | Enabled |
| Ambiguous | Disabled | Disabled | Enabled |
| Detection error | Disabled | Disabled | Enabled |

Before an active app is selected, menu titles are neutral:

```text
Open Co-Worker App Folder
Open Co-Worker Data Folder
```

For an active LOOP24 process, they become:

```text
Open LOOP24 App Folder
Open LOOP24 Data Folder
```

When LOOP24 exits, the UI clears the resolution, restores the neutral titles, and disables both items on the next poll.

The existing Start action remains enabled for `Stopped` only when exactly one compatible installation is known. Install remains available for `NotInstalled`. Start, Stop, and folder actions are disabled for `Ambiguous` and `DetectionError`.

## Folder Resolution

### App Folder

The App Folder action opens the parent directory of the selected executable on Windows or reveals the selected `.app` bundle on macOS. It never recomputes a brand from a display string.

### Data Folder

The default Windows data folder is `%LOCALAPPDATA%\<descriptor.slug>`. The default macOS data folder is `~/<descriptor.homeDir>`.

An explicit `HERMES_HOME` from the tray process environment or the live Windows user environment remains authoritative when it is a genuine custom location. To match Hermes multi-brand behavior, a populated `%LOCALAPPDATA%\<other-brand>` value is treated as stale foreign-brand state and ignored when its final path component differs from the selected slug and it contains `hermes-agent`. The selected brand's default is then used. Custom locations outside `%LOCALAPPDATA%` remain honored.

Immediately before opening either folder, the handler revalidates that the snapshot's exact executable is still running. If validation fails, it clears the active snapshot, requests an immediate refresh, leaves the requested folder closed, and reports that the Co-Worker is no longer running.

## Tray Icon Behavior

`setBaseIcon` and every branch of `setIconForState` use the blue Gateway asset. The legacy hollow-`O` running, warning, and error assets are no longer selected at runtime. Gateway state remains represented by the existing tooltip and menu header text.

This rule applies on Windows and macOS so the tray never changes brand-like glyphs as gateway state changes.

## Concurrency and Failure Handling

- Only one desktop probe may execute at a time.
- Timer and manual-refresh triggers coalesce rather than queueing unbounded work.
- The UI loop alone mutates systray menu items.
- Readers receive an immutable active-resolution snapshot.
- Invalid descriptors are ignored and logged without making their paths actionable.
- A probe-level filesystem or process error disables brand-dependent actions instead of retaining stale paths.
- A disappearing executable or process between polling and a click is handled by click-time revalidation.

## Testing

Pure, seam-injected tests will cover:

- LOOP24-only descriptor discovery and exact executable resolution.
- OTTO-only descriptor discovery and the legacy OTTO fallback.
- Invalid schema, slug, display name, home directory, gateway, and missing-executable rejection.
- Duplicate candidate removal.
- Exactly-one-running, installed-but-stopped, multiple-installed, multiple-running, not-installed, and detection-error resolution.
- Menu titles and enabled states for every desktop state.
- Timer and manual refresh sharing one serialized, coalescing detector.
- Active snapshot clearing after process exit.
- Click-time process revalidation before App or Data folder opening.
- `%LOCALAPPDATA%\loop24` and `%LOCALAPPDATA%\otto` default data paths.
- Genuine custom `HERMES_HOME` preservation and stale foreign-brand default rejection.
- Start, Stop, and liveness targeting the selected executable.
- Every gateway state mapping to the Gateway icon.
- Gateway folder and gateway process behavior remaining brand-independent.

Verification will include the focused tray tests, `go test ./...`, `go vet ./...`, macOS tray build, and Windows cross-build. Windows runtime acceptance should confirm this sequence:

1. Start the tray before any Co-Worker: both Co-Worker folder items are disabled.
2. Launch LOOP24: within three seconds both items enable and use LOOP24 titles and paths.
3. Stop LOOP24: within three seconds both items disable and return to neutral titles.
4. Click manual refresh before and after launch: state changes immediately without duplicate probes.
5. Run OTTO instead: the same actions target OTTO paths.
6. Run OTTO and LOOP24 together: the tray reports ambiguity and disables brand-dependent actions.
7. Observe gateway startup and state changes: the tray icon remains the Gateway glyph throughout.

## Compatibility and Rollout

The change requires no new settings and preserves `GW_HOME`, gateway wrapper invocation, current install locations, and the fixed Co-Worker installer command. An upgrade replaces the tray binary; on its next start it begins with neutral disabled Co-Worker folder actions and discovers the running branded desktop automatically.
