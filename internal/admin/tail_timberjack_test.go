// Package admin — integration test proving the Tailer keeps emitting
// across a real github.com/DeRuina/timberjack rotation.
//
// The plain TestAdmin_TailerRotation (in tail_test.go) simulates
// logrotate's rename-then-create pattern with raw os.Rename calls.
// Timberjack rotates by closing the active file handle, atomically
// renaming it to a timestamped backup, and re-opening a fresh file at
// the same path with a new inode — the Tailer's os.SameFile inode
// check should detect this and reopen at the new EOF. This test
// drives a real timberjack.Logger so any future change to either
// timberjack's rotation algorithm or the Tailer's detection path
// will fail loudly here, before the admin UI silently stops
// updating in production.
package admin

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DeRuina/timberjack"
	"go.uber.org/goleak"
)

func TestAdmin_TailerSurvivesTimberjackRotate(t *testing.T) {
	defer goleak.VerifyNone(
		t,
		// timberjack starts a background "mill" goroutine for backup
		// pruning; it exits cleanly on Logger.Close() but the goleak
		// snapshot is taken before Close() runs through our defer
		// chain — ignore that one goroutine.
		goleak.IgnoreTopFunction("github.com/DeRuina/timberjack.(*Logger).millRun"),
	)

	dir := t.TempDir()
	logPath := filepath.Join(dir, "gateway.log")

	rotator := &timberjack.Logger{
		Filename:  logPath,
		MaxAge:    7,
		LocalTime: true,
		FileMode:  0o644,
		// No RotateAt — we drive rotation manually via Rotate() so
		// the test is deterministic and fast.
	}
	defer func() { _ = rotator.Close() }()

	// Write one line so the file exists before the Tailer subscribes.
	if _, err := rotator.Write([]byte("pre-subscribe-line\n")); err != nil {
		t.Fatalf("pre-subscribe write: %v", err)
	}

	tailer := NewTailer(logPath, discardLogger())
	sub := tailer.Subscribe(t.Context())
	defer tailer.Unsubscribe(sub)

	// Allow the Tailer to open the file and seek to EOF.
	time.Sleep(400 * time.Millisecond)

	// Write a line BEFORE rotation — Tailer must emit it.
	if _, err := rotator.Write([]byte("before-rotate\n")); err != nil {
		t.Fatalf("before-rotate write: %v", err)
	}
	got1 := waitLine(sub.C, 1500*time.Millisecond)
	if got1 != "before-rotate" {
		t.Fatalf("before-rotate: got %q, want %q", got1, "before-rotate")
	}

	// Drive a real timberjack rotation. This renames the active file
	// to a timestamped backup and creates a fresh logPath inode.
	if err := rotator.Rotate(); err != nil {
		t.Fatalf("timberjack.Rotate: %v", err)
	}

	// Give the Tailer enough ticks (250ms TailPollInterval) to detect
	// the inode change and reopen at the new file's EOF.
	time.Sleep(800 * time.Millisecond)

	// Write a line to the freshly-rotated (new-inode) file.
	if _, err := rotator.Write([]byte("after-rotate\n")); err != nil {
		t.Fatalf("after-rotate write: %v", err)
	}

	got2 := waitLine(sub.C, 2*time.Second)
	if got2 != "after-rotate" {
		t.Fatalf("after-rotate: got %q, want %q", got2, "after-rotate")
	}

	// Confirm the rotated backup file exists on disk (sanity check
	// that we actually exercised the rename-and-recreate path, not
	// just an append).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	hasBackup := false
	for _, e := range entries {
		if e.Name() != "gateway.log" {
			hasBackup = true
			break
		}
	}
	if !hasBackup {
		t.Fatalf("expected at least one rotated backup file in %s, got only the active log", dir)
	}
}
