# Tray Menu Stability Design

## Problem

On Windows, the Gateway tray menu can disappear while the pointer is moving
through it. Both the gateway-state poller and the Co-Worker detector emit every
three seconds. Their UI consumers currently call the systray title,
enable/disable, show/hide, icon, and tooltip APIs for every emission, even when
the projected menu is identical to the previous render.

The Windows systray implementation maps those calls to native menu mutation;
in particular, `Hide` removes a menu item and `Show` updates or reinserts one.
Mutating the active native popup dismisses it. Hovering cannot protect the menu
from programmatic mutation.

## Goals

- Keep the tray menu open during ordinary pointer navigation.
- Preserve the three-second gateway and Co-Worker polling cadence.
- Preserve immediate manual Co-Worker refresh behavior.
- Preserve all current menu labels, visibility rules, enabled states, icons,
  tooltips, transition notifications, and click handling.
- Continue applying a real state transition promptly.
- Avoid changing or forking the systray dependency.

## Approaches Considered

### 1. Deduplicate rendered menu models (selected)

Project each poll result into a comparable render model. Cache the last model
successfully applied and perform no systray calls when the next model is equal.
This removes the periodic native-menu mutation while keeping actual state
changes live.

Trade-off: a genuine state transition that occurs while the menu is open may
still cause the native popup to close once. That is preferable to showing stale
actions, and unlike the current bug it does not happen continuously while the
state is stable.

### 2. Defer updates while the popup is open

Override systray's right-click callback, track the blocking `ShowMenu` call,
queue UI changes, and flush after it returns. This can protect even genuine
transitions, but it takes ownership of platform menu presentation and creates a
new cross-goroutine lifecycle that the current tray does not need.

### 3. Stop live menu updates

Render once when the tray starts or only after user actions. This avoids popup
mutation but makes health and Co-Worker controls stale, contradicting the tray's
purpose.

## Design

### Gateway render model

Introduce a comparable gateway menu model containing every value that affects
rendering: state, tooltip, header, subheader, and the enabled state of Start,
Stop, and Restart. `applyState` must continue recording the latest state for
transition notifications, but it calls icon, tooltip, title, and enabled-state
APIs only when the new model differs from the cached applied model.

The cache starts empty, so the first gateway result always renders. A repeated
`running`, `stopped`, degraded, starting, or error result with identical detail
performs zero menu mutations. A change in state or detail renders once.
Transition notifications retain their existing state-based behavior and are
not suppressed by render deduplication.

### Co-Worker render model

Reuse the existing comparable `desktopMenuModel` projection. Cache the last
applied model and return before any SetTitle, Enable/Disable, or Show/Hide call
when it is unchanged.

The cache starts empty, so the initial neutral Detecting state renders once.
Manual Refresh still publishes Detecting followed by the fresh result; each
different model renders once. Repeated periodic results for the same selected
brand and state perform zero native menu mutations.

### Cache ownership

Gateway render cache access remains in the gateway UI loop. Co-Worker render
cache access remains in the Co-Worker UI loop. They do not share model state,
and no new lock is required beyond the tray's existing state lock used for
transition tracking.

## Testing

Tests will use injected render operations rather than a live tray:

- First gateway output applies all expected operations.
- An identical gateway output applies no render operations.
- A gateway detail or state change applies once and retains notification
  semantics.
- First Co-Worker model applies all expected operations.
- An identical Co-Worker model applies no render operations, including no
  Show/Hide calls.
- A Co-Worker brand/state change applies once.
- Manual Detecting then resolved output remains observable.
- Existing tray, race, formatting, vet, native build, and Windows cross-build
  gates remain green.

Interactive Windows acceptance will verify that a stable menu remains open for
longer than two polling intervals while navigating the top-level and Advanced
menus.

## Non-Goals

- Pausing polling while the menu is open.
- Replacing or forking `github.com/energye/systray`.
- Changing the three-second cadence.
- Redesigning menu labels or action visibility.
- Altering Co-Worker discovery, path selection, process control, or Gateway
  lifecycle behavior.
