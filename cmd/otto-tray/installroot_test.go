//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveInstallRoot_OTTOHomeWins(t *testing.T) {
	t.Setenv("OTTO_HOME", "/tmp/custom-home")
	got, err := resolveInstallRoot()
	if err != nil {
		t.Fatalf("resolveInstallRoot: %v", err)
	}
	if got != "/tmp/custom-home" {
		t.Fatalf("OTTO_HOME ignored: got %q, want /tmp/custom-home", got)
	}
}

func TestResolveInstallRoot_WalksUpFromExecutable(t *testing.T) {
	t.Setenv("OTTO_HOME", "")
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "otto-tray")
	if err := os.WriteFile(exe, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveInstallRootFrom(exe)
	if err != nil {
		t.Fatalf("resolveInstallRootFrom: %v", err)
	}
	// EvalSymlinks because t.TempDir() on darwin is under /var ->
	// /private/var; resolver resolves symlinks so the result matches
	// the canonical install root the shell wrapper computes.
	wantResolved, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Fatalf("install root: got %q, want %q", gotResolved, wantResolved)
	}
}
