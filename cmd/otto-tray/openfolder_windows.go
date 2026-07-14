//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// readUserEnvVar reads HKCU\Environment\<name> (the persisted user env the installer
// writes). Inherited process env can be stale for a GUI-launched tray, so query the
// registry directly — same rationale as hermes-agent's readWindowsUserEnvVar.
func readUserEnvVar(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "reg", "query", `HKCU\Environment`, "/v", name) //nolint:gosec // constant reg path + fixed value name
	hideConsole(cmd)                                                                // reg is a console child — suppress the flash
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, name) {
			continue
		}
		// Value may contain spaces, so index off the type marker rather than
		// field-splitting the line.
		for _, marker := range []string{"REG_EXPAND_SZ", "REG_SZ"} {
			if i := strings.Index(line, marker); i >= 0 {
				return expandWinVars(strings.TrimSpace(line[i+len(marker):]))
			}
		}
	}
	return ""
}

// expandWinVars expands the few %VAR% roots a HERMES_HOME value might contain.
func expandWinVars(s string) string {
	for _, v := range []string{"LOCALAPPDATA", "USERPROFILE", "APPDATA"} {
		if val := os.Getenv(v); val != "" {
			s = strings.ReplaceAll(s, "%"+v+"%", val)
		}
	}
	return s
}
