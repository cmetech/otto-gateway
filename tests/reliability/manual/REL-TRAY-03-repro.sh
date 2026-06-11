#!/usr/bin/env bash
# Finding ID: T-3
# REL-* ID: REL-TRAY-03
# Target phase: 14 (verify) / 15 (fix)
# Target OS: macOS
# Expected pre-fix behavior: killing gateway out from under running tray produces no visible signal in menu bar
# Expected post-fix behavior: icon and/or tooltip change to a death-indicating state user can observe at a glance
# Run instructions: 1) Start tray app via scripts/otto-gw start. 2) In a second terminal, run this script. 3) Watch the menu bar icon and tooltip for 30 seconds. 4) Report whether any visible signal appeared.

set -euo pipefail

REPO_ROOT="$(cd -P "$(dirname "$0")/../../.." >/dev/null 2>&1 && pwd)"

# Resolve the pidfile location — mirrors installRootPIDFile in cmd/otto-tray/tray.go
# Default install root is ~/.otto-gw; check OTTO_HOME override.
INSTALL_ROOT="${OTTO_HOME:-${HOME}/.otto-gw}"
PIDFILE="${INSTALL_ROOT}/.otto/gw/otto-gateway.pid"

echo ""
echo "REL-TRAY-03 reproducer — pre-fix: silent gateway death on macOS"
echo "================================================================="
echo "Repo root:    $REPO_ROOT"
echo "Install root: $INSTALL_ROOT"
echo "PID file:     $PIDFILE"
echo ""

# Verify the tray is running (look for OTTO Tray process)
if ! pgrep -qf "OTTO Tray" 2>/dev/null && ! pgrep -qf "otto-tray" 2>/dev/null; then
    echo "ERROR: OTTO Tray process not found. Start the tray first:"
    echo "  scripts/otto-gw start"
    echo "  open -a 'OTTO Tray'  (or however you launch the tray app)"
    exit 1
fi
echo "Tray process detected."

# Read the PID file
if [[ ! -f "$PIDFILE" ]]; then
    echo "ERROR: PID file not found at $PIDFILE"
    echo "  Verify the gateway is running and INSTALL_ROOT is correct."
    echo "  Try: OTTO_HOME=/path/to/install bash $0"
    exit 1
fi

GW_PID="$(cat "$PIDFILE")"
if [[ -z "$GW_PID" ]]; then
    echo "ERROR: PID file is empty"
    exit 1
fi

echo "Gateway PID: $GW_PID"

# Verify the process is alive before we kill it
if ! kill -0 "$GW_PID" 2>/dev/null; then
    echo "ERROR: Gateway process $GW_PID is not alive (already stopped?)"
    exit 1
fi
echo "Gateway process $GW_PID is alive. Killing it now..."
echo ""

# Kill the gateway out from under the running tray
kill -9 "$GW_PID"
echo "kill -9 $GW_PID sent."
echo ""
echo "================================================================="
echo "OBSERVATION WINDOW: 30 seconds"
echo ""
echo "Watch the menu bar icon and tooltip. Report:"
echo "  PRE-FIX:  Icon looks identical to healthy; no banner; no tooltip change."
echo "  POST-FIX: Icon changes to a stopped/error state; tooltip updates."
echo ""
echo "The tray polls every 3s, so allow up to 6 seconds for the first state update."
echo ""

# Poll for 30 seconds and print timestamps to help the observer correlate
for i in $(seq 1 10); do
    sleep 3
    elapsed=$((i * 3))
    echo "  t+${elapsed}s — check menu bar icon now (poll cycle $i / 10)"
done

echo ""
echo "================================================================="
echo "RESULT: Did the menu bar icon or tooltip change to reflect gateway death?"
echo "  - YES → POST-FIX behavior (or finding was already mitigated)"
echo "  - NO  → PRE-FIX CONFIRMED (T-3 still present)"
echo ""
echo "Gateway PID $GW_PID is now dead. Restart with: scripts/otto-gw start"
