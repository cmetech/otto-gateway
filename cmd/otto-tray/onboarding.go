//go:build darwin || windows

package main

// offerFirstRunAutostart fires once on the very first launch (no
// tray.json present). It does NOT prompt synchronously — that would
// require a full GUI window. Instead it shows a notification telling
// the user the toggle exists in Preferences, and writes an empty
// tray.json so we never ask again.
func offerFirstRunAutostart(s *trayState) {
	notify("OTTO Gateway",
		"Tip: open the menu → Preferences to launch the tray automatically at login.")
	_ = saveTrayConfig(trayConfigPath(s.installRoot), s.cfg)
}
