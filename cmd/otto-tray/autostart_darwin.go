//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const launchAgentLabel = "io.cmetech.otto-tray"

// launchAgentPlist returns the plist body for a per-user LaunchAgent
// that runs otto-tray at login. KeepAlive is intentionally false so
// that if the user quits the tray, launchd does not respawn it.
func launchAgentPlist(execPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
    <key>ProcessType</key>
    <string>Interactive</string>
</dict>
</plist>
`, launchAgentLabel, execPath)
}

func launchAgentPlistPath() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", launchAgentLabel+".plist")
}

// installLaunchAgent writes the plist and (unless skipLaunchctl)
// calls launchctl bootstrap. Tests pass skipLaunchctl=true so they
// verify file behavior without touching the real launchd.
func installLaunchAgent(execPath string, skipLaunchctl bool) error {
	body := launchAgentPlist(execPath)
	path := launchAgentPlistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir LaunchAgents: %w", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	if skipLaunchctl {
		return nil
	}
	uid := strconv.Itoa(os.Getuid())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// bootstrap (modern); fall back to load if bootstrap is unavailable.
	if err := exec.CommandContext(ctx, "launchctl", "bootstrap", "gui/"+uid, path).Run(); err != nil { //nolint:gosec // launchctl with constants + uid + computed path
		_ = exec.CommandContext(ctx, "launchctl", "load", path).Run() //nolint:gosec // see above; best-effort fallback
	}
	return nil
}

func uninstallLaunchAgent(skipLaunchctl bool) error {
	path := launchAgentPlistPath()
	if !skipLaunchctl {
		uid := strconv.Itoa(os.Getuid())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "launchctl", "bootout", "gui/"+uid+"/"+launchAgentLabel).Run() //nolint:gosec // launchctl with constants + uid + label
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}
