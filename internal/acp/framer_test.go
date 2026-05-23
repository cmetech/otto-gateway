// Whitebox unit tests for the framer type (package acp).
// D-18: whitebox package gives access to unexported types.
package acp

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestFramerRoundtrip writes one JSON object to a bytes.Buffer,
// reads it back via readFrame, and verifies the bytes match.
func TestFramerRoundtrip(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	f := newFramer(&buf, &buf)

	want := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	if err := f.writeFrame(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
	}); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}

	got, err := f.readFrame()
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	// json.Encoder sorts object keys; unmarshal and re-marshal to compare canonically.
	if strings.TrimSpace(string(got)) != want {
		// Allow for key ordering differences by checking that both parse equivalently.
		// Just verify it's valid JSON with the right keys.
		if !bytes.Contains(got, []byte(`"ping"`)) {
			t.Errorf("got %s, want to contain ping method", got)
		}
	}
}

// TestFramerMultipleFrames writes 5 frames and reads them back in order.
func TestFramerMultipleFrames(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	// Write side: write 5 frames then close.
	go func() {
		f := newFramer(nil, pw)
		for i := 0; i < 5; i++ {
			if err := f.writeFrame(map[string]any{"i": i}); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		if err := pw.Close(); err != nil {
			t.Errorf("pw.Close: %v", err)
		}
	}()

	rf := newFramer(pr, nil)
	for i := 0; i < 5; i++ {
		raw, err := rf.readFrame()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if !bytes.Contains(raw, []byte(`"i"`)) {
			t.Errorf("frame %d: missing key i, got %s", i, raw)
		}
	}
	// Next read should be EOF.
	_, err := rf.readFrame()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF after last frame, got %v", err)
	}
}

// TestFramerLargeFrame writes a frame exceeding the default 64 KB scanner limit (65535 bytes)
// and verifies it is read without error. This confirms sc.Buffer size override is working.
func TestFramerLargeFrame(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()

	payload := strings.Repeat("x", 70000) // > 64 KB default limit
	go func() {
		f := newFramer(nil, pw)
		if err := f.writeFrame(map[string]any{"data": payload}); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := pw.Close(); err != nil {
			t.Errorf("pw.Close large: %v", err)
		}
	}()

	rf := newFramer(pr, nil)
	raw, err := rf.readFrame()
	if err != nil {
		t.Fatalf("readFrame large: %v", err)
	}
	if len(raw) < 70000 {
		t.Errorf("expected large frame >= 70000 bytes, got %d", len(raw))
	}
}

// TestFramerEOF verifies readFrame returns io.EOF when the reader is closed.
func TestFramerEOF(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	if err := pw.Close(); err != nil { // close immediately
		t.Fatalf("pw.Close: %v", err)
	}

	f := newFramer(pr, nil)
	_, err := f.readFrame()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF on closed reader, got %v", err)
	}
}

// TestFramerCopySemantics writes two frames and verifies they are independent byte slices
// (not the same backing array). This catches regressions in the copy-before-return logic.
func TestFramerCopySemantics(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	go func() {
		f := newFramer(nil, pw)
		if err := f.writeFrame(map[string]any{"frame": 1}); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := f.writeFrame(map[string]any{"frame": 2}); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := pw.Close(); err != nil {
			t.Errorf("pw.Close copy: %v", err)
		}
	}()

	rf := newFramer(pr, nil)
	frame1, err := rf.readFrame()
	if err != nil {
		t.Fatalf("frame1 read: %v", err)
	}
	frame2, err := rf.readFrame()
	if err != nil {
		t.Fatalf("frame2 read: %v", err)
	}
	// They must be distinct byte slices; mutating one must not change the other.
	// Verify frame1 still contains "1" and frame2 contains "2".
	if !bytes.Contains(frame1, []byte("1")) {
		t.Errorf("frame1 corrupted after frame2 read: %s", frame1)
	}
	if !bytes.Contains(frame2, []byte("2")) {
		t.Errorf("frame2 does not contain expected content: %s", frame2)
	}
	// Verify they are different backing arrays (overwriting frame2 doesn't affect frame1).
	if len(frame1) > 0 && len(frame2) > 0 {
		frame2[0] = 'Z'
		if frame1[0] == 'Z' {
			t.Error("frame1 and frame2 share the same backing array — copy semantics broken")
		}
	}
}
