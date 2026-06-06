//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
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
