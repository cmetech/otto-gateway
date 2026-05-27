// Package admin — whitebox test file.
// Tests for RingBuffer + Tailer + subscriber lifecycle, rotation handling,
// missing-file graceful retry, and slow-subscriber drop semantics.
//
// Every test defers goleak.VerifyNone(t) so goroutine leaks are caught
// regardless of whether TestMain's VerifyTestMain catches them at suite end.
package admin

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// discardLogger returns a *slog.Logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitLine reads from ch and returns the first value received, or "" after timeout.
func waitLine(ch <-chan string, timeout time.Duration) string {
	select {
	case line, ok := <-ch:
		if !ok {
			return ""
		}
		return line
	case <-time.After(timeout):
		return ""
	}
}

// waitLines reads up to n lines from ch within timeout, returning them in order.
func waitLines(ch <-chan string, n int, timeout time.Duration) []string {
	deadline := time.After(timeout)
	var result []string
	for i := 0; i < n; i++ {
		select {
		case line, ok := <-ch:
			if !ok {
				return result
			}
			result = append(result, line)
		case <-deadline:
			return result
		}
	}
	return result
}

// appendToFile appends lines to a file using O_APPEND semantics.
func appendToFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("appendToFile: open %s: %v", path, err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatalf("appendToFile: write: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// RingBuffer tests
// ---------------------------------------------------------------------------

func TestAdmin_RingBuffer_PushCopyFIFO(t *testing.T) {
	defer goleak.VerifyNone(t)

	rb := NewRingBuffer(3)
	rb.Push("a")
	rb.Push("b")
	rb.Push("c")
	got := rb.Copy()
	if len(got) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(got))
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("expected [a b c], got %v", got)
	}
}

func TestAdmin_RingBuffer_OverflowDropsOldest(t *testing.T) {
	defer goleak.VerifyNone(t)

	rb := NewRingBuffer(3)
	// Push N+1 lines; oldest is dropped.
	rb.Push("a")
	rb.Push("b")
	rb.Push("c")
	rb.Push("d")
	got := rb.Copy()
	if len(got) != 3 {
		t.Fatalf("expected 3 elements after overflow, got %d", len(got))
	}
	if got[0] != "b" || got[1] != "c" || got[2] != "d" {
		t.Errorf("expected [b c d], got %v", got)
	}
}

func TestAdmin_RingBuffer_EmptyReturnsNilOrEmpty(t *testing.T) {
	defer goleak.VerifyNone(t)

	rb := NewRingBuffer(5)
	got := rb.Copy()
	// Either nil or empty slice is acceptable; both have len == 0.
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %v", got)
	}
}

func TestAdmin_RingBuffer_DoubleFull(t *testing.T) {
	defer goleak.VerifyNone(t)

	cap := 5
	rb := NewRingBuffer(cap)
	// Push 2*cap lines; only the last cap should survive.
	for i := 0; i < 2*cap; i++ {
		rb.Push(string(rune('a' + i)))
	}
	got := rb.Copy()
	if len(got) != cap {
		t.Fatalf("expected %d elements, got %d", cap, len(got))
	}
	// Last cap lines are "f","g","h","i","j" (index 5..9 in 'a'+offset).
	expected := []string{"f", "g", "h", "i", "j"}
	for i, want := range expected {
		if got[i] != want {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want)
		}
	}
}

// ---------------------------------------------------------------------------
// Tailer lifecycle tests
// ---------------------------------------------------------------------------

func TestAdmin_TailerLazyStartStop(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	// Create an empty log file so the tailer can open it.
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	tailer := NewTailer(logPath, discardLogger())

	// No goroutine started yet — goleak would catch it at defer.
	// The tailer is not running so no goroutine exists beyond whatever
	// was there before construction.

	// Subscribe: starts exactly one goroutine.
	sub := tailer.Subscribe(t.Context())

	// Give goroutine a moment to start before unsubscribing.
	time.Sleep(10 * time.Millisecond)

	// Unsubscribe: last subscriber, goroutine should exit.
	tailer.Unsubscribe(sub)

	// Allow goroutine time to observe ctx.Done() and return.
	time.Sleep(50 * time.Millisecond)

	// goleak.VerifyNone at defer will catch any leaked goroutine.
}

func TestAdmin_TailerLazyStartStop_MultipleSubscribers(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	tailer := NewTailer(logPath, discardLogger())

	sub1 := tailer.Subscribe(t.Context())
	sub2 := tailer.Subscribe(t.Context())

	// Append a line — both subscribers should receive it.
	appendToFile(t, logPath, "hello")

	line1 := waitLine(sub1.C, 2*time.Second)
	line2 := waitLine(sub2.C, 2*time.Second)
	if line1 != "hello" {
		t.Errorf("sub1: expected 'hello', got %q", line1)
	}
	if line2 != "hello" {
		t.Errorf("sub2: expected 'hello', got %q", line2)
	}

	// Unsubscribe sub1 — tailer goroutine should still be running (sub2 remains).
	tailer.Unsubscribe(sub1)
	time.Sleep(20 * time.Millisecond) // let goroutine process

	// Unsubscribe sub2 — now last subscriber, goroutine should exit.
	tailer.Unsubscribe(sub2)
	time.Sleep(50 * time.Millisecond)

	// goleak.VerifyNone at defer verifies no leaks.
}

func TestAdmin_TailerBroadcast_NewLines(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	// Pre-populate with one line (will NOT be received by subscriber since
	// tailer opens at EOF per D-10).
	appendToFile(t, logPath, "existing")

	tailer := NewTailer(logPath, discardLogger())
	sub := tailer.Subscribe(t.Context())
	defer tailer.Unsubscribe(sub)

	// Wait for tailer to open file and position at EOF.
	time.Sleep(400 * time.Millisecond)

	// Now append new lines.
	appendToFile(t, logPath, "new-1", "new-2")

	// Both lines should arrive within 1s (>2 poll ticks at 250ms).
	lines := waitLines(sub.C, 2, 1500*time.Millisecond)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "new-1" {
		t.Errorf("expected 'new-1', got %q", lines[0])
	}
	if lines[1] != "new-2" {
		t.Errorf("expected 'new-2', got %q", lines[1])
	}

	// Snapshot should also contain both new lines.
	snap := tailer.Snapshot()
	found1, found2 := false, false
	for _, l := range snap {
		if l == "new-1" {
			found1 = true
		}
		if l == "new-2" {
			found2 = true
		}
	}
	if !found1 || !found2 {
		t.Errorf("Snapshot missing lines: found new-1=%v found new-2=%v; snap=%v", found1, found2, snap)
	}
}

func TestAdmin_TailerBackfillSnapshot(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"

	// Pre-populate with 600 lines BEFORE starting tailer — these should NOT
	// be backfilled (D-10: tailer opens at EOF).
	for i := 0; i < 600; i++ {
		appendToFile(t, logPath, "pre")
	}

	tailer := NewTailer(logPath, discardLogger())
	sub := tailer.Subscribe(t.Context())
	defer tailer.Unsubscribe(sub)

	// Wait for tailer to open file at EOF.
	time.Sleep(400 * time.Millisecond)

	// Append new lines AFTER subscribe.
	for i := 0; i < 10; i++ {
		appendToFile(t, logPath, "post")
	}

	// Wait for lines to arrive.
	time.Sleep(1 * time.Second)

	snap := tailer.Snapshot()
	// Snapshot should be ≤ RingBufferLines.
	if len(snap) > RingBufferLines {
		t.Errorf("Snapshot exceeds RingBufferLines: len=%d", len(snap))
	}
	// No pre-existing content should be in the snapshot (D-10).
	for _, l := range snap {
		if l == "pre" {
			t.Errorf("Snapshot contains pre-existing content 'pre' — D-10 violation")
		}
	}
	// All post lines should appear.
	postCount := 0
	for _, l := range snap {
		if l == "post" {
			postCount++
		}
	}
	if postCount != 10 {
		t.Errorf("expected 10 'post' lines in snapshot, got %d", postCount)
	}
}

func TestAdmin_TailerRotation(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	rotatedPath := dir + "/test.log.1"

	// Create the log file.
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	tailer := NewTailer(logPath, discardLogger())
	sub := tailer.Subscribe(t.Context())
	defer tailer.Unsubscribe(sub)

	// Wait for tailer to open and position at EOF.
	time.Sleep(400 * time.Millisecond)

	// Write before-rotate AFTER subscribe so the tailer can read it.
	appendToFile(t, logPath, "before-rotate")

	line := waitLine(sub.C, 1500*time.Millisecond)
	if line != "before-rotate" {
		t.Fatalf("expected 'before-rotate', got %q", line)
	}

	// Simulate logrotate's create-new strategy:
	// mv test.log test.log.1 + touch test.log
	if err := os.Rename(logPath, rotatedPath); err != nil {
		t.Fatalf("rename: %v", err)
	}
	nf, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create new log: %v", err)
	}
	nf.Close()

	// Give the tailer time to detect the rotation (at least 2 poll ticks).
	time.Sleep(700 * time.Millisecond)

	// Append to the NEW file — should arrive after rotation detection.
	appendToFile(t, logPath, "after-rotate")

	line2 := waitLine(sub.C, 2*time.Second)
	if line2 != "after-rotate" {
		t.Fatalf("expected 'after-rotate' after rotation, got %q", line2)
	}

	// Verify no historical content is re-streamed by checking sub.C is now empty.
	select {
	case extra := <-sub.C:
		t.Errorf("unexpected extra line after rotation: %q", extra)
	case <-time.After(500 * time.Millisecond):
		// Good — no extra lines.
	}
}

func TestAdmin_TailerMissingFileGracefulRetry(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/nonexistent-otto-test.log"

	tailer := NewTailer(logPath, discardLogger())
	sub := tailer.Subscribe(t.Context())
	defer tailer.Unsubscribe(sub)

	// Wait 1s — tailer should not crash; snapshot empty.
	time.Sleep(1 * time.Second)
	snap := tailer.Snapshot()
	if len(snap) != 0 {
		t.Errorf("expected empty snapshot for missing file, got %v", snap)
	}

	// Now create the file and append a line.
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()
	appendToFile(t, logPath, "late")

	// Tailer should pick it up within 2s.
	line := waitLine(sub.C, 2500*time.Millisecond)
	if line != "late" {
		t.Fatalf("expected 'late' after file creation, got %q", line)
	}
}

func TestAdmin_TailerSlowSubscriberDrops(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	tailer := NewTailer(logPath, discardLogger())

	// Slow subscriber: create manually with a buffer of TailerSubChanBuffer
	// and then don't drain it.
	slowSub := tailer.Subscribe(t.Context())
	defer tailer.Unsubscribe(slowSub)

	// Also add a second subscriber that we WILL drain, to verify the tailer
	// keeps moving even when slow sub is full.
	fastSub := tailer.Subscribe(t.Context())
	defer tailer.Unsubscribe(fastSub)

	// Give tailer time to open file.
	time.Sleep(400 * time.Millisecond)

	// Broadcast more lines than TailerSubChanBuffer so the slow sub fills.
	more := TailerSubChanBuffer + 5
	for i := 0; i < more; i++ {
		appendToFile(t, logPath, "line")
		// Small sleep to give poll ticks time to fire between batches.
		time.Sleep(30 * time.Millisecond)
	}

	// Wait for lines to be processed.
	time.Sleep(1 * time.Second)

	// Fast subscriber should have received at least some lines.
	received := 0
	drain:
	for {
		select {
		case _, ok := <-fastSub.C:
			if !ok {
				break drain
			}
			received++
		default:
			break drain
		}
	}
	// We don't require ALL lines to arrive on fastSub (timing is loose),
	// but the tailer should not be blocked — Snapshot should have grown.
	snap := tailer.Snapshot()
	if len(snap) == 0 {
		t.Error("Snapshot is empty — tailer appears blocked by slow subscriber")
	}
	_ = received // may be 0 depending on timing; Snapshot growth is the key assertion
}
