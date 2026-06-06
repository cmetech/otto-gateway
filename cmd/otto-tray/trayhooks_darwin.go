//go:build darwin

package main

import "github.com/energye/systray"

// platformOnReady wires the macOS click handlers. energye/systray
// deliberately leaves the NSStatusBar button's action unset by
// default (systray_darwin.m comments out [statusItem setMenu:menu])
// so the consumer can install per-click handlers. Without this wiring
// clicking the icon does nothing — the v2.0.5 symptom.
//
// We wire both left and right click to ShowMenu so users get the
// standard "click the menu-bar icon, menu drops" behavior on either
// button. The library's documentation notes that ShowMenu() is
// "only supported inside OnRClick" on macOS, but performClick: from
// inside ShowMenu does not actually recurse back into the OnClick
// handler at the AppKit layer in practice — we get a real menu drop
// on left-click too.
func platformOnReady() {
	systray.SetOnClick(func(menu systray.IMenu) { _ = menu.ShowMenu() })
	systray.SetOnRClick(func(menu systray.IMenu) { _ = menu.ShowMenu() })
}
