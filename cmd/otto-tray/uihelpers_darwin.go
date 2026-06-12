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

	"otto-gateway/cmd/otto-tray/icon"
)

// setIcon uses a template image so the menu-bar icon auto-adapts to
// dark/light bar themes on macOS.
func setIcon(b []byte) { systray.SetTemplateIcon(b, b) }

// setIconForState updates the menu-bar icon to reflect the current FSM state.
// Running uses SetTemplateIcon (adapts to dark/light bar); Starting/Degraded
// use SetIcon with the warning PNG; Error/Stopped/Unknown use SetIcon with the
// error PNG because SetTemplateIcon strips color, which is the primary signal.
// Icon assets: cmd/otto-tray/icon/{Running,Warning,Error}.png (embedded).
func setIconForState(state State) {
	switch state {
	case StateRunning:
		systray.SetTemplateIcon(icon.Running, icon.Running)
	case StateStarting, StateDegraded:
		systray.SetIcon(icon.Warning)
	default: // StateError, StateStopped, StateUnknown
		systray.SetIcon(icon.Error)
	}
}

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

// notifyFn is the package-level seam tests inject to capture or block notify.
// Mirrors the Windows seam (REL-TRAY-04 / T-4) for platform symmetry — the
// applyState dispatch in tray.go runs notifyFn in a goroutine on both
// platforms so the FSM transition path is identical regardless of OS.
var notifyFn = notifyImpl

// notify is the public entrypoint kept for backwards compatibility with
// callers outside applyState (handleStart/Stop/Restart error toasts, support
// bundle dialogs). It routes through notifyFn so test injection covers every
// call path.
func notify(title, body string) { notifyFn(title, body) }

// As of v1.9, icon/tooltip via setIconForState is the primary state signal;
// notification banners are a secondary best-effort signal (LSUIElement agents
// may not receive notification permission — see REL-TRAY-03 / D-12).
func notifyImpl(title, body string) {
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

// escapeApplescript sanitizes operator-derived strings before they are
// interpolated into an AppleScript string literal passed to `osascript -e`.
//
// Per D-20-01 (QUAL-01) the contract is:
//   - `"` and `\` are prefixed with a backslash (AS string-literal escaping).
//   - Raw 0x0A / 0x0D / 0x09 are translated into the two-byte AppleScript
//     escape sequences `\n`, `\r`, `\t` so multi-line content remains
//     readable inside the emitted AS string literal instead of prematurely
//     terminating it.
//   - All other C0 control bytes (0x00..0x1F, excluding 0x09/0x0A/0x0D) and
//     DEL (0x7F) are stripped entirely — they have no business in a tray
//     notification/dialog body and dropping them is defense in depth.
//   - All other bytes (including >= 0x80) pass through unchanged. The function
//     operates byte-wise; multi-byte UTF-8 sequences survive intact because
//     none of their continuation bytes fall into the stripped ranges.
//
// This function backs `//nolint:gosec G204` annotations at three call sites
// (notifyImpl, infoDialog, confirmDialog); the contract is unit-tested in
// escapeApplescript_darwin_test.go.
func escapeApplescript(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			out = append(out, '\\', c)
		case c == '\n':
			out = append(out, '\\', 'n')
		case c == '\r':
			out = append(out, '\\', 'r')
		case c == '\t':
			out = append(out, '\\', 't')
		case c < 0x20 || c == 0x7F:
			// Strip: other C0 controls + DEL.
			continue
		default:
			out = append(out, c)
		}
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

// revealBundle opens Finder with the given path selected. Mirrors openURL's
// shape (5s timeout, best-effort, no error surfacing). `open -R <path>` is
// the documented Finder reveal verb. Path comes from a wrapper subprocess
// we just spawned — same trust boundary as the dashboard URL.
func revealBundle(path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "open", "-R", path).Run() //nolint:gosec // path originates from the wrapper we just ran
}

// bundleExt returns the archive extension that the bash wrapper produces.
// Used for the fallback path when the wrapper's stdout is empty.
func bundleExt() string { return ".tar.gz" }
