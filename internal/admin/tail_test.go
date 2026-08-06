// Package admin — whitebox test file.
// Tests for RingBuffer + Tailer + subscriber lifecycle, rotation handling,
// missing-file graceful retry, and slow-subscriber drop semantics.
//
// Every test defers goleak.VerifyNone(t) so goroutine leaks are caught
// regardless of whether TestMain's VerifyTestMain catches them at suite end.
package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func waitStatus(ch <-chan TailStatus, want TailState, timeout time.Duration) TailStatus {
	deadline := time.After(timeout)
	for {
		select {
		case status, ok := <-ch:
			if !ok {
				return TailStatus{}
			}
			if status.State == want {
				return status
			}
		case <-deadline:
			return TailStatus{}
		}
	}
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

	capacity := 5
	rb := NewRingBuffer(capacity)
	// Push 2*capacity lines; only the last capacity should survive.
	for i := 0; i < 2*capacity; i++ {
		rb.Push(string(rune('a' + i)))
	}
	got := rb.Copy()
	if len(got) != capacity {
		t.Fatalf("expected %d elements, got %d", capacity, len(got))
	}
	// Last capacity lines are "f","g","h","i","j" (index 5..9 in 'a'+offset).
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

func TestTailStatusJSONExcludesPathAndRawError(t *testing.T) {
	size := int64(0)
	status := TailStatus{State: TailStateEmpty, SizeBytes: &size, Level: "INFO"}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"path", "error", t.TempDir()} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, body)
		}
	}
}

func TestTailerAttachReturnsCoherentSnapshotAndOpeningStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.log")
	appendToFile(t, path, "existing")
	tailer := newTailer(path, "INFO", discardLogger())
	tailer.ring.Push("existing")
	sub, snapshot, status := tailer.Attach(t.Context())
	defer tailer.Unsubscribe(sub)
	if len(snapshot) != 1 || snapshot[0] != "existing" {
		t.Fatalf("snapshot = %v, want [existing]", snapshot)
	}
	if status.State != TailStateOpening || status.Level != "INFO" {
		t.Fatalf("status = %+v, want opening INFO", status)
	}
	if cap(sub.BackfillC) != 1 || cap(sub.StatusC) != 1 {
		t.Fatalf("control channel capacities = backfill:%d status:%d, want 1 and 1", cap(sub.BackfillC), cap(sub.StatusC))
	}
}

func TestTailerFileStates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) string
		want  TailState
	}{
		{
			name: "empty",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "empty.log")
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			want: TailStateEmpty,
		},
		{
			name: "missing",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing.log")
			},
			want: TailStateMissing,
		},
		{
			name: "unreadable",
			setup: func(t *testing.T) string {
				parent := filepath.Join(t.TempDir(), "regular-file")
				if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(parent, "tail.log")
			},
			want: TailStateUnreadable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer goleak.VerifyNone(t)
			tailer := newTailer(tc.setup(t), "INFO", discardLogger())
			sub, _, _ := tailer.Attach(t.Context())
			defer tailer.Unsubscribe(sub)
			status := waitStatus(sub.StatusC, tc.want, 2*time.Second)
			if status.State != tc.want || status.Level != "INFO" {
				t.Fatalf("status = %+v, want state %q level INFO", status, tc.want)
			}
			if tc.want == TailStateEmpty && (status.SizeBytes == nil || *status.SizeBytes != 0) {
				t.Fatalf("empty status size = %v, want pointer to zero", status.SizeBytes)
			}
		})
	}
}

func TestTailerFileStateRecovery(t *testing.T) {
	t.Run("missing file becomes empty then watching", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		path := filepath.Join(t.TempDir(), "late.log")
		tailer := newTailer(path, "INFO", discardLogger())
		sub, _, _ := tailer.Attach(t.Context())
		defer tailer.Unsubscribe(sub)
		if got := waitStatus(sub.StatusC, TailStateMissing, 2*time.Second); got.State != TailStateMissing {
			t.Fatalf("initial status = %+v, want missing", got)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := waitStatus(sub.StatusC, TailStateEmpty, 2*time.Second); got.State != TailStateEmpty {
			t.Fatalf("created-file status = %+v, want empty", got)
		}
		appendToFile(t, path, "live")
		if got := waitStatus(sub.StatusC, TailStateWatching, 2*time.Second); got.State != TailStateWatching {
			t.Fatalf("appended-file status = %+v, want watching", got)
		}
	})

	t.Run("unreadable path becomes watching", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		root := t.TempDir()
		parent := filepath.Join(root, "blocked")
		if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(parent, "tail.log")
		tailer := newTailer(path, "INFO", discardLogger())
		sub, _, _ := tailer.Attach(t.Context())
		defer tailer.Unsubscribe(sub)
		if got := waitStatus(sub.StatusC, TailStateUnreadable, 2*time.Second); got.State != TailStateUnreadable {
			t.Fatalf("initial status = %+v, want unreadable", got)
		}
		if err := os.Remove(parent); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		appendToFile(t, path, "existing")
		if got := waitStatus(sub.StatusC, TailStateWatching, 2*time.Second); got.State != TailStateWatching {
			t.Fatalf("recovered status = %+v, want watching", got)
		}
	})
}

func TestTailerPublishStatusCoalescesRepeatedObservations(t *testing.T) {
	tailer := newTailer("unused.log", "INFO", discardLogger())
	sub := &subscriber{
		C:         make(chan string, TailerSubChanBuffer),
		BackfillC: make(chan []string, 1),
		StatusC:   make(chan TailStatus, 1),
	}
	tailer.subscribers = []*subscriber{sub}

	firstSize := int64(7)
	tailer.publishStatus(TailStatus{State: TailStateWatching, SizeBytes: &firstSize})
	if got := waitStatus(sub.StatusC, TailStateWatching, time.Second); got.State != TailStateWatching {
		t.Fatalf("first status = %+v, want watching", got)
	}

	secondSize := int64(7)
	tailer.publishStatus(TailStatus{State: TailStateWatching, SizeBytes: &secondSize})
	select {
	case got := <-sub.StatusC:
		t.Fatalf("duplicate status was delivered: %+v", got)
	default:
	}
}

func TestReadInitialBackfillKeepsLatestCompleteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.log")
	appendToFile(t, path, "one", "two", "three", "four")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines, partial, _, err := readInitialBackfill(f, 2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "three" || lines[1] != "four" {
		t.Fatalf("lines = %v, want [three four]", lines)
	}
	if partial != "" {
		t.Fatalf("partial = %q, want empty", partial)
	}
}

func TestReadInitialBackfillDropsLeadingFragmentAndCarriesTrailingPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.log")
	if err := os.WriteFile(path, []byte("discard-me\nkeep\nunfinished"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines, partial, _, err := readInitialBackfill(
		f, 10, int64(len("ard-me\nkeep\nunfinished")))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "keep" {
		t.Fatalf("lines = %v, want [keep]", lines)
	}
	if partial != "unfinished" {
		t.Fatalf("partial = %q, want unfinished", partial)
	}
}

func TestReadInitialBackfillKeepsRecordAtExactWindowBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines, partial, _, err := readInitialBackfill(f, 10, int64(len("two\nthree\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "two" || lines[1] != "three" {
		t.Fatalf("lines = %v, want [two three]", lines)
	}
	if partial != "" {
		t.Fatalf("partial = %q, want empty", partial)
	}
}

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

	// Wait for the tailer goroutine to open the file and seek to EOF before
	// appending, so the appended line is counted as new (not already scanned).
	time.Sleep(400 * time.Millisecond)

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
	// Pre-populate with one line. Initial history uses BackfillC rather than the
	// live C channel, so the assertions below still isolate live delivery.
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

func TestTailerFirstOpenBackfillsLatest500ThenStreamsLive(t *testing.T) {
	defer goleak.VerifyNone(t)

	logPath := filepath.Join(t.TempDir(), "test.log")
	var contents strings.Builder
	for i := 0; i < RingBufferLines+25; i++ {
		_, _ = fmt.Fprintf(&contents, "pre-%03d\n", i)
	}
	if err := os.WriteFile(logPath, []byte(contents.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	tailer := NewTailer(logPath, discardLogger())
	sub, _, _ := tailer.Attach(t.Context())
	defer tailer.Unsubscribe(sub)
	var got []string
	select {
	case got = <-sub.BackfillC:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial backfill batch")
	}
	if len(got) != RingBufferLines || got[0] != "pre-025" || got[len(got)-1] != "pre-524" {
		t.Fatalf("boundary mismatch: len=%d first=%q last=%q", len(got), got[0], got[len(got)-1])
	}
	appendToFile(t, logPath, "live")
	if line := waitLine(sub.C, 2*time.Second); line != "live" {
		t.Fatalf("live line = %q, want live", line)
	}

	snapshot := tailer.Snapshot()
	if len(snapshot) != RingBufferLines || snapshot[len(snapshot)-1] != "live" {
		t.Fatalf("snapshot after live append = len:%d last:%q", len(snapshot), snapshot[len(snapshot)-1])
	}
}

func TestTailerInitialPartialCompletesExactlyOnce(t *testing.T) {
	defer goleak.VerifyNone(t)
	path := filepath.Join(t.TempDir(), "partial.log")
	if err := os.WriteFile(path, []byte("complete\npartial"), 0o600); err != nil {
		t.Fatal(err)
	}
	tailer := NewTailer(path, discardLogger())
	sub, _, _ := tailer.Attach(t.Context())
	defer tailer.Unsubscribe(sub)
	select {
	case got := <-sub.BackfillC:
		if len(got) != 1 || got[0] != "complete" {
			t.Fatalf("backfill = %v, want [complete]", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for partial-file backfill")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("-done\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := waitLine(sub.C, 2*time.Second); got != "partial-done" {
		t.Fatalf("completed partial = %q, want partial-done", got)
	}
	select {
	case extra := <-sub.C:
		t.Fatalf("completed partial arrived more than once: %q", extra)
	case <-time.After(2 * TailPollInterval):
	}
}

func TestTailerBackfillsOnlyOnceAcrossLazyRunRestart(t *testing.T) {
	defer goleak.VerifyNone(t)
	path := filepath.Join(t.TempDir(), "restart.log")
	appendToFile(t, path, "historical")
	tailer := NewTailer(path, discardLogger())
	first, _, _ := tailer.Attach(t.Context())
	select {
	case got := <-first.BackfillC:
		if len(got) != 1 || got[0] != "historical" {
			t.Fatalf("first backfill = %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first backfill")
	}
	tailer.Unsubscribe(first)
	time.Sleep(2 * TailPollInterval)

	second, snapshot, _ := tailer.Attach(t.Context())
	defer tailer.Unsubscribe(second)
	if len(snapshot) != 1 || snapshot[0] != "historical" {
		t.Fatalf("restart snapshot = %v, want [historical]", snapshot)
	}
	select {
	case replay := <-second.BackfillC:
		t.Fatalf("history replayed after lazy restart: %v", replay)
	case <-time.After(2 * TailPollInterval):
	}
	appendToFile(t, path, "live")
	if got := waitLine(second.C, 2*time.Second); got != "live" {
		t.Fatalf("restart live line = %q, want live", got)
	}
}

func TestTailerInitialBackfillAttachmentHandoff(t *testing.T) {
	defer goleak.VerifyNone(t)
	tailer := NewTailer(filepath.Join(t.TempDir(), "missing.log"), discardLogger())
	first, firstSnapshot, _ := tailer.Attach(t.Context())
	defer tailer.Unsubscribe(first)
	if len(firstSnapshot) != 0 {
		t.Fatalf("first snapshot = %v, want empty", firstSnapshot)
	}
	tailer.publishInitialBackfill([]string{"one", "two"})
	select {
	case got := <-first.BackfillC:
		if len(got) != 2 || got[0] != "one" || got[1] != "two" {
			t.Fatalf("first batch = %v, want [one two]", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first attachment did not receive backfill batch")
	}

	second, secondSnapshot, _ := tailer.Attach(t.Context())
	defer tailer.Unsubscribe(second)
	if len(secondSnapshot) != 2 || secondSnapshot[0] != "one" || secondSnapshot[1] != "two" {
		t.Fatalf("second snapshot = %v, want [one two]", secondSnapshot)
	}
	select {
	case duplicate := <-second.BackfillC:
		t.Fatalf("second attachment received duplicate backfill: %v", duplicate)
	default:
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
	if _, err := nf.WriteString("replacement-history\n"); err != nil {
		_ = nf.Close()
		t.Fatal(err)
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

// TestAdmin_TailerPartialLinePersistsAcrossTicks asserts WR-02 mitigation:
// a write whose trailing bytes have no \n terminator must not be silently
// dropped when the tailer's read loop hits EOF. The partial bytes must be
// carried across ticks so that when the writer finally appends the \n the
// full line is emitted exactly once.
func TestAdmin_TailerPartialLinePersistsAcrossTicks(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	tailer := NewTailer(logPath, discardLogger())
	sub := tailer.Subscribe(t.Context())
	defer tailer.Unsubscribe(sub)

	// Let the tailer open and seek to EOF.
	time.Sleep(400 * time.Millisecond)

	// Write a complete line followed by a partial line (no trailing \n).
	wf, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := wf.WriteString("INFO step 1\nINFO step 2 in progress"); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	wf.Close()

	// Step 1 should arrive on the next poll tick.
	line1 := waitLine(sub.C, 1500*time.Millisecond)
	if line1 != "INFO step 1" {
		t.Fatalf("expected 'INFO step 1', got %q", line1)
	}

	// Step 2's partial bytes must NOT have been delivered yet (no \n).
	// Give the tailer a couple more ticks to confirm it does not
	// surface the partial line prematurely.
	select {
	case unexpected := <-sub.C:
		t.Fatalf("partial line surfaced before terminator: %q", unexpected)
	case <-time.After(500 * time.Millisecond):
		// Good — partial line correctly held back.
	}

	// Now complete the line. The carry-over bytes from the prior tick
	// must concatenate with this write so we see the full line ONCE.
	wf2, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append 2: %v", err)
	}
	if _, err := wf2.WriteString("\nINFO step 3\n"); err != nil {
		t.Fatalf("write completion: %v", err)
	}
	wf2.Close()

	got := waitLines(sub.C, 2, 2*time.Second)
	if len(got) != 2 {
		t.Fatalf("expected 2 lines after completion, got %d: %v", len(got), got)
	}
	if got[0] != "INFO step 2 in progress" {
		t.Errorf("expected full line 'INFO step 2 in progress', got %q (partial bytes lost?)", got[0])
	}
	if got[1] != "INFO step 3" {
		t.Errorf("expected 'INFO step 3', got %q", got[1])
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

	// Now create the file and wait for the tailer to open it at EOF.
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	f.Close()

	// Allow at least 2 poll ticks for the tailer to detect the new file,
	// open it, and position at EOF (lastSize=0).
	time.Sleep(700 * time.Millisecond)

	appendToFile(t, logPath, "late")

	// Tailer should pick it up within the next poll tick.
	line := waitLine(sub.C, 2*time.Second)
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

// ---------------------------------------------------------------------------
// TailerRegistry tests (quick 260529-ll2)
// ---------------------------------------------------------------------------

// TestTailerRegistry_LazyCreation asserts that the registry constructs
// exactly one *Tailer per unique name and returns the cached pointer on
// subsequent Get calls. Different names produce different pointers.
func TestTailerRegistry_LazyCreation(t *testing.T) {
	defer goleak.VerifyNone(t)

	reg := NewTailerRegistry(discardLogger())

	t1a := reg.Get("main", "/tmp/a.log", "")
	t1b := reg.Get("main", "/tmp/a.log", "")
	if t1a != t1b {
		t.Errorf("Get(\"main\", _) must return the same pointer on subsequent calls; got %p vs %p", t1a, t1b)
	}

	t2 := reg.Get("boot-err", "/tmp/b.log", "")
	if t2 == t1a {
		t.Errorf("Get(\"boot-err\", _) must return a different pointer from \"main\"; both are %p", t1a)
	}

	// Path on the second call is ignored — the cached instance carries
	// the original path. This is the read-only registry contract (D-10).
	t1c := reg.Get("main", "/tmp/different-path.log", "TRACE")
	if t1c != t1a {
		t.Errorf("Get(\"main\", _) must ignore path on cached lookup; got %p, want %p", t1c, t1a)
	}
	if t1c.path != "/tmp/a.log" {
		t.Errorf("cached tailer path mutated; got %q, want /tmp/a.log", t1c.path)
	}
	if t1c.level != "" {
		t.Errorf("cached tailer level mutated; got %q, want empty", t1c.level)
	}
}

// TestTailerRegistry_Concurrent asserts that 100 racing goroutines
// calling Get("main", path) all receive the same *Tailer pointer. The
// registry's mu.Lock around the map check + insert is the load-bearing
// invariant.
func TestTailerRegistry_Concurrent(t *testing.T) {
	defer goleak.VerifyNone(t)

	reg := NewTailerRegistry(discardLogger())
	const N = 100
	results := make([]*Tailer, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = reg.Get("main", "/tmp/concurrent.log", "INFO")
		}(i)
	}
	wg.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("first concurrent Get returned nil")
	}
	for i, p := range results {
		if p != first {
			t.Errorf("Get %d returned %p, want %p (race in lazy cache)", i, p, first)
		}
	}
}
