//go:build darwin || windows

package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWrapperPath_DarwinUsesShellScript(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only path resolution")
	}
	cmd, args := wrapperCommand("/opt/otto", "start")
	if !strings.HasSuffix(cmd, filepath.Join("scripts", "gw")) {
		t.Fatalf("darwin wrapper: got %q, want suffix scripts/gw", cmd)
	}
	if len(args) != 1 || args[0] != "start" {
		t.Fatalf("darwin args: got %v, want [start]", args)
	}
	_, args = wrapperCommand("/opt/otto", "support", supportCoworkerArgs("darwin", "/Users/me/.loop24")...)
	if len(args) != 3 || args[0] != "support" || args[1] != "--co-worker-home" || args[2] != "/Users/me/.loop24" {
		t.Fatalf("darwin support args: got %v, want [support --co-worker-home /Users/me/.loop24]", args)
	}
}

func TestWrapperPath_WindowsUsesPwsh(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only path resolution")
	}
	cmd, args := wrapperCommand(`C:\opt\otto`, "stop")
	if cmd != "pwsh" && cmd != "powershell" {
		t.Fatalf("windows shell: got %q, want pwsh or powershell", cmd)
	}
	if len(args) < 4 || args[len(args)-1] != "stop" {
		t.Fatalf("windows args: got %v, want trailing 'stop'", args)
	}
	_, args = wrapperCommand(`C:\opt\otto`, "support", supportCoworkerArgs("windows", `C:\Users\me\AppData\Local\loop24`)...)
	if len(args) < 6 || args[len(args)-3] != "support" || args[len(args)-2] != "-CoworkerHome" || args[len(args)-1] != `C:\Users\me\AppData\Local\loop24` {
		t.Fatalf("windows support args: got %v, want trailing [support -CoworkerHome C:\\Users\\me\\AppData\\Local\\loop24]", args)
	}
}

func TestSupportCoworkerArgs(t *testing.T) {
	tests := []struct {
		name string
		goos string
		home string
		want []string
	}{
		{name: "darwin", goos: "darwin", home: "/Users/me/.loop24", want: []string{"--co-worker-home", "/Users/me/.loop24"}},
		{name: "windows", goos: "windows", home: `C:\Users\me\AppData\Local\loop24`, want: []string{"-CoworkerHome", `C:\Users\me\AppData\Local\loop24`}},
		{name: "empty", goos: "darwin", home: "  ", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := supportCoworkerArgs(tc.goos, tc.home)
			if len(got) != len(tc.want) {
				t.Fatalf("args = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("args[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
