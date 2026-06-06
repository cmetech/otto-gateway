//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/energye/systray"
)

// setIcon uses a template image so the menu-bar icon auto-adapts to
// dark/light bar themes on macOS.
func setIcon(b []byte) { systray.SetTemplateIcon(b, b) }

func openURL(url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "open", url).Run() //nolint:gosec // url originates from operator-controlled HTTP_ADDR
}

func copyToClipboard(s string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pbcopy")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	_, _ = stdin.Write([]byte(s))
	_ = stdin.Close()
	_ = cmd.Wait()
}

func notify(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := fmt.Sprintf(`display notification "%s" with title "%s"`,
		escapeApplescript(body), escapeApplescript(title))
	_ = exec.CommandContext(ctx, "osascript", "-e", script).Run() //nolint:gosec // script body escaped via escapeApplescript
}

func escapeApplescript(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}

// exeForAutostart returns the canonical binary path to embed in the
// LaunchAgent plist. We resolve symlinks so the plist survives the
// PATH symlink being relinked on upgrade.
func exeForAutostart() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve exe: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

func installAutostart(exe string) error { return installLaunchAgent(exe, false) }
func uninstallAutostart() error         { return uninstallLaunchAgent(false) }
