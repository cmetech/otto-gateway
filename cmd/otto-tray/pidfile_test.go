//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestReadPIDFile_MissingFile(t *testing.T) {
	got, err := readPIDFile(filepath.Join(t.TempDir(), "absent.pid"))
	if err != nil {
		t.Fatalf("missing should be nil error, got %v", err)
	}
	if got != 0 {
		t.Fatalf("missing pid: want 0, got %d", got)
	}
}

func TestReadPIDFile_ParsesPID(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gw.pid")
	if err := os.WriteFile(path, []byte("12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("readPIDFile: %v", err)
	}
	if got != 12345 {
		t.Fatalf("pid: want 12345, got %d", got)
	}
}

func TestReadPIDFile_GarbageReturnsZero(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gw.pid")
	if err := os.WriteFile(path, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := readPIDFile(path)
	if got != 0 {
		t.Fatalf("garbage pid: want 0, got %d", got)
	}
}

func TestProcessAlive_SelfIsAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatalf("our own pid (%d) should report alive", os.Getpid())
	}
}

func TestProcessAlive_ImplausiblePIDIsDead(t *testing.T) {
	if processAlive(1 << 30) {
		t.Fatalf("implausible pid should report dead")
	}
}

// TestIsGatewayProcessName_MatchesRenamedBinary is the RED/GREEN test
// for Task B3: the gateway binary is being renamed otto-gateway ->
// gateway, so the tray's pidfile-identity matcher must match the new
// name (and no longer match the old one) or a real running gateway
// will be rejected as "not the gateway".
func TestIsGatewayProcessName_MatchesRenamedBinary(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
		cases := []struct {
			name string
			in   string
			want bool
		}{
			{"bare basename matches", "gateway", true},
			{"path-suffixed basename matches", "/usr/local/bin/gateway", true},
			{"old otto-gateway name no longer matches", "otto-gateway", false},
			{"old otto-gateway path no longer matches", "/usr/local/bin/otto-gateway", false},
			{"suffix-only match rejected", "fake-gateway", false},
			{"prefix-only match rejected", "gateway-old", false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := isGatewayProcessName(tc.in); got != tc.want {
					t.Fatalf("isGatewayProcessName(%q): got %v, want %v", tc.in, got, tc.want)
				}
			})
		}
	case "windows":
		cases := []struct {
			name string
			in   string
			want bool
		}{
			{"bare exe name matches", "gateway.exe", true},
			{"full path matches", `C:\opt\otto\bin\gateway.exe`, true},
			{"case-insensitive match", `C:\opt\otto\bin\GATEWAY.EXE`, true},
			{"old otto-gateway.exe name no longer matches", "otto-gateway.exe", false},
			{"old otto-gateway.exe path no longer matches", `C:\opt\otto\bin\otto-gateway.exe`, false},
			{"unrelated exe rejected", "notgateway.exe", false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := isGatewayProcessName(tc.in); got != tc.want {
					t.Fatalf("isGatewayProcessName(%q): got %v, want %v", tc.in, got, tc.want)
				}
			})
		}
	default:
		t.Skip("darwin/windows only")
	}
}

func TestReadPIDFile_RoundTripsOwnPID(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gw.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPIDFile(path)
	if err != nil || got != os.Getpid() {
		t.Fatalf("readPIDFile self: got (%d,%v), want (%d,nil)", got, err, os.Getpid())
	}
}
