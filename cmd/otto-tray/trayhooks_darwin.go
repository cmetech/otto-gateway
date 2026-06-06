//go:build darwin

package main

// platformOnReady is a per-OS hook that runs at the top of onReady.
// macOS treats menu-bar clicks as standard (single click drops the
// menu) and the energye/systray library documents that ShowMenu()
// is only supported from OnRClick on macOS, so the darwin version
// leaves the default behavior alone.
func platformOnReady() {}
