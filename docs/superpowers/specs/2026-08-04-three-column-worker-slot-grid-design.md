# Three-Column Worker Slot Grid Design

**Date:** 2026-08-04
**Status:** Approved for implementation planning

## Goal

Make worker cards wide enough to display memory usage and its recycle threshold
on one line, while keeping the dashboard's vacant-capacity signal. The normal
laptop configuration starts two workers and shows one vacant position in a
single three-card row. Larger pools use a second three-card row, with six as
the supported maximum.

## Configuration Contract

- Change the Gateway binary's `POOL_SIZE` default from 4 to 2. The laptop
  wrapper template already sets `POOL_SIZE=2`, so packaged installations keep
  their current worker count while bare-binary launches align with them.
- Accept `POOL_SIZE` values from 0 through 6. Preserve the existing meaning of
  0: no warm pool and the existing dashboard empty state.
- Reject values greater than 6 with a named startup error. The same limit
  applies after command-line flags are resolved, so `--pool-size 7` cannot
  bypass environment validation.
- Keep the internal `pool.Config{}` defensive size default unchanged. This
  design changes the application's resolved configuration, not the pool
  package's zero-value behavior used by package-level callers and tests.
- Update operator-facing defaults in the admin configuration reference,
  operating documentation, README, and maintained architecture descriptions.
  Historical Node-reference documents remain historical and are not rewritten.

## Dashboard Layout Contract

The dashboard derives its display-card count from the real slot count without
mutating the server snapshot:

| Real slots | Cards displayed | Vacant cards | Desktop rows |
|-----------:|----------------:|-------------:|-------------:|
| 0 | Existing empty state | 0 | 0 |
| 1 | 3 | 2 | 1 |
| 2 | 3 | 1 | 1 |
| 3 | 3 | 0 | 1 |
| 4 | 6 | 2 | 2 |
| 5 | 6 | 1 | 2 |
| 6 | 6 | 0 | 2 |

For one through three real slots, the client pads a copied display list to
three. For four through six, it pads the copied list to six. Real slots remain
first and retain stable labels; vacant cards occupy only the unused trailing
positions. A transition such as two to three or five to six replaces the
corresponding vacant card in place without retaining vacant styling.

The UI does not silently truncate an impossible snapshot containing more than
six slots. Configuration validation is the enforcement boundary; rendering all
unexpected rows is safer than hiding workers from an operator.

## Width and Responsive Behavior

- Change the desktop slot grid from four equal columns to three equal columns.
  Each card therefore gains roughly one-third more horizontal space at the
  same page width.
- Keep the existing `minmax(0, 1fr)` track discipline so wide content cannot
  steal width from neighboring cards.
- Preserve the existing non-wrapping memory value. The wider card must display
  values such as `800 MiB / 500 MiB` on one line inside the card boundary.
- Preserve responsive fallbacks: two columns on tablet-width viewports and one
  column on mobile. “Rows of three” is the desktop layout contract; forcing
  three columns onto narrow screens would recreate the overflow problem.
- Vacant styling and text remain unchanged except that comments and tests refer
  to three- or six-card padding instead of four-card padding.

## Data Flow and Boundaries

No server API fields change. The admin snapshot continues to return only real
pool slots. `renderSlots` remains the sole owner of client-only vacant cards,
and performance ingestion continues to consume the unpadded server list so
vacant positions never create CPU, memory, activity, or idle samples.

The pool lifecycle, recycling policy, metrics, session ownership, and worker
labels are unchanged. This is a resolved-configuration limit plus a dashboard
layout change.

## Error Handling

An environment or command-line pool size above 6 fails before worker warmup and
names `POOL_SIZE` or `--pool-size` in the returned configuration error. Negative
values retain their existing invalid-value behavior. No value is clamped: an
operator typo must not silently start a different number of memory-heavy Kiro
processes.

## Test Strategy

Implementation follows red-green-refactor:

1. Configuration tests lock the bare-binary default at 2, accept the inclusive
   maximum of 6, reject 7, and prove the command-line flag cannot bypass the
   cap.
2. Admin JavaScript tests exercise real rendering for pool sizes 1 through 6
   and assert the literal displayed-card/vacant-card table above. Tests also
   protect vacant-to-real in-place transitions across the 2→3 and 5→6
   boundaries.
3. The existing worker lifecycle, idle-memory presentation, and unsupported
   platform cases remain green.
4. Verification includes the focused Go and Node suites, formatting and diff
   checks, the full repository CI gate, and a browser/computed-layout check at
   desktop, tablet, and mobile widths. The desktop check confirms the memory
   value stays on one line and inside its card.

## Non-Goals

- No third pool row or support for more than six warm workers.
- No changes to worker recycling thresholds or scheduling.
- No new dashboard configuration control.
- No change to the meaning of real, vacant, busy, checked-out, recovering, or
  failed slot states.
