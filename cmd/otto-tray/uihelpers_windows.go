//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/energye/systray"
)

func setIcon(b []byte) { systray.SetIcon(b) }

func openURL(url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec // url is operator-configured
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

func copyToClipboard(s string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "clip")
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
	script := "[reflection.assembly]::loadwithpartialname('System.Windows.Forms') | Out-Null; " +
		"$n = New-Object System.Windows.Forms.NotifyIcon; " +
		"$n.Icon = [System.Drawing.SystemIcons]::Information; " +
		"$n.Visible = $true; " +
		"$n.ShowBalloonTip(5000, '" + escapePS(title) + "', '" + escapePS(body) + "', 'Info')"
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", script) //nolint:gosec // script body escaped via escapePS
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

func escapePS(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func exeForAutostart() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve exe: %w", err)
	}
	return exe, nil
}

func installAutostart(exe string) error { return installRunKey(exe) }
func uninstallAutostart() error         { return uninstallRunKey() }
