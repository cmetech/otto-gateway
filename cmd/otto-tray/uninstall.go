//go:build darwin || windows

package main

// runUninstall removes the login-item registration. It does NOT
// delete the binary or tray.json — the user's package manager or
// `rm -rf ~/.gw/` handles that. Idempotent: succeeds even
// if no registration exists.
func runUninstall(_ string) error {
	return uninstallAutostart()
}
