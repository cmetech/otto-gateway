//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// infoDialog shows a blocking single-button informational dialog. Used
// for About and other "user asked for this, show it modally" surfaces.
// `display dialog` (vs. `display notification`) is the right call here
// because the OTTO Tray.app bundle does not register for the User
// Notifications API — notification banners silently no-op for LSUIElement
// agents that haven't been granted notification permission, which is the
// v2.0.8 "About does nothing" symptom. A modal dialog has no permission
// gate and is the standard idiom for an "About" surface anyway.
func infoDialog(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	script := fmt.Sprintf(`display dialog "%s" with title "%s" buttons {"OK"} default button "OK" with icon note`,
		escapeApplescript(body), escapeApplescript(title))
	_ = exec.CommandContext(ctx, "osascript", "-e", script).Run() //nolint:gosec // script body escaped via escapeApplescript
}

// confirmDialog shows a blocking yes/no dialog. Returns true if the
// user clicked the affirmative button, false if they clicked the
// negative button OR if the dialog could not be shown (we never
// block on a degraded osascript).
func confirmDialog(title, body, yesLabel, noLabel string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// `display dialog` returns "button returned:<label>" on stdout
	// when the user picks a button; the exit code is 0 in both yes
	// and no cases. On cancel via ESC it returns non-zero and the
	// process state's exit code is 1 — treat that as "no".
	script := fmt.Sprintf(`display dialog "%s" with title "%s" buttons {"%s", "%s"} default button "%s" cancel button "%s"`,
		escapeApplescript(body), escapeApplescript(title),
		escapeApplescript(noLabel), escapeApplescript(yesLabel),
		escapeApplescript(yesLabel), escapeApplescript(noLabel))
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).Output() //nolint:gosec // script body escaped via escapeApplescript
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "button returned:"+yesLabel)
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
