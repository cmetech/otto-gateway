//go:build windows

package main

import "github.com/energye/systray"

// platformOnReady runs the per-OS systray.OnReady glue. On Windows
// we wire the left-click handler to show the menu so users can click
// the icon (as well as right-click) to reach the actions. The
// energye/systray default on Windows is right-click only.
func platformOnReady() {
	systray.SetOnClick(func(menu systray.IMenu) {
		_ = menu.ShowMenu()
	})
}
