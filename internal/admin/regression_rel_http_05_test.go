package admin

// Regression test for REL-HTTP-05 (H-5): the admin tailer's 1 MB per-line cap
// is only enforced for *unterminated* lines. A newline-terminated line of any
// size bypasses the cap in readLines and flows into the ring buffer and SSE
// stream unbounded.
//
// Root cause (tail.go:402):
//
//	if len(current) > TailerMaxLineBytes && !strings.HasSuffix(current, "\n") {
//
// The `!strings.HasSuffix(current, "\n")` guard means a complete (terminated)
// line that exceeds TailerMaxLineBytes skips truncation entirely. With
// CHAT_TRACE=true, a single chat prompt can be 4 MiB, producing a 4 MiB NDJSON
// line terminated by `\n` that flows through the ring buffer and SSE stream
// unbounded — up to 500 × multi-MB strings in the ring.
//
// Pre-fix observable: a subscriber receives a line of length > TailerMaxLineBytes
// when a newline-terminated input of 5×TailerMaxLineBytes is written.
//
// Post-fix: the cap is enforced unconditionally regardless of newline terminator.
// Unskip in Phase 16 fix commit and flip the assertion.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// TestRegression_REL_HTTP_05_AdminTailerLineCapBypass demonstrates that the
// readLines per-line cap in Tailer does not truncate newline-terminated lines.
//
// The test constructs a Tailer backed by a real log file, subscribes, appends a
// single line of length 5×TailerMaxLineBytes terminated by '\n', and waits for
// the subscriber channel to deliver it. Pre-fix observable: the received line
// has length > TailerMaxLineBytes (full line delivered, cap not enforced).
func TestRegression_REL_HTTP_05_AdminTailerLineCapBypass(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	logPath := dir + "/test.log"

	// Pre-create the file so the tailer's reopen() can seek-to-EOF on
	// an EMPTY file. The Tailer's D-10 invariant ("never backfill
	// historical content") means a file created AFTER the first poll
	// would have its initial contents skipped — reopen() seeks to EOF
	// of the existing file. Touching it empty first lets the tailer
	// position itself at byte 0, and the append below is then read on
	// the next poll tick.
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatalf("create empty log: %v", err)
	}

	tailer := NewTailer(logPath, discardLogger())

	sub := tailer.Subscribe(context.Background())
	defer tailer.Unsubscribe(sub)

	// Allow the tailer goroutine to start and open the file.
	time.Sleep(400 * time.Millisecond)

	// Build a single line of length 5×TailerMaxLineBytes.
	// It is terminated by '\n' — the cap-bypass trigger.
	longLine := strings.Repeat("x", 5*TailerMaxLineBytes)
	// Use appendToFile to write the line with a trailing newline.
	appendToFile(t, logPath, longLine)

	// Wait up to 3s for the tailer to deliver the line on the subscriber channel.
	received := waitLines(sub.C, 1, 3*time.Second)
	if len(received) == 0 {
		t.Fatal("tailer did not deliver any line within 3s")
	}

	line := received[0]

	// Post-fix (Plan 16-02 Task 2): the cap is enforced unconditionally —
	// regardless of newline termination. A 5×TailerMaxLineBytes line
	// terminated by '\n' must be truncated at TailerMaxLineBytes.
	if len(line) > TailerMaxLineBytes {
		t.Errorf("post-fix invariant violated: received line length %d > TailerMaxLineBytes (%d); "+
			"newline-terminated multi-MB line bypassed the cap",
			len(line), TailerMaxLineBytes)
	} else {
		t.Logf("post-fix confirmed: received line length %d <= TailerMaxLineBytes (%d); "+
			"cap enforced on newline-terminated line",
			len(line), TailerMaxLineBytes)
	}
}
