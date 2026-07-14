//go:build darwin || windows

package main

import "log/slog"

// offerFirstRunAutostart fires once on the very first launch (no
// tray.json present). It pops a real OS dialog with Yes / Not now
// buttons asking whether to register the tray as a login item. If
// the user picks Yes, we register and persist the toggle on; either
// way we write tray.json so we never ask again. A degraded dialog
// path (osascript failure, etc.) maps to "Not now" rather than
// blocking the tray from starting.
func offerFirstRunAutostart(s *trayState) {
	const (
		title    = "Gateway"
		body     = "Launch Gateway Tray automatically every time you log in?\n\nYou can change this later from Preferences."
		yesLabel = "Yes"
		noLabel  = "Not now"
	)

	if confirmDialog(title, body, yesLabel, noLabel) {
		exe, err := exeForAutostart()
		if err != nil {
			slog.Error("resolve tray exe", "err", err)
		} else if err := installAutostart(exe); err != nil {
			slog.Error("install autostart", "err", err)
			notify(title, "Could not register login item: "+err.Error())
		} else {
			s.mu.Lock()
			s.cfg.LaunchAtLogin = true
			s.mu.Unlock()
			s.miPrefsLogin.Check()
		}
	}

	if err := saveTrayConfig(gwTrayConfigPath(s.gwHome), s.cfg); err != nil {
		slog.Error("save tray.json", "err", err)
	}
}
