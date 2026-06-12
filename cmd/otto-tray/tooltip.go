//go:build darwin || windows

package main

import "fmt"

// tooltipForState returns the tray tooltip string for a given FSM state.
func tooltipForState(state State, detail string) string {
	s := fmt.Sprintf("OTTO Gateway · %s", state)
	if detail != "" {
		s += " (" + detail + ")"
	}
	return s
}
