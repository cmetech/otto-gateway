// Package acp implements the JSON-RPC 2.0 client over stdio for kiro-cli.
// File-scoped layers: framer.go (NDJSON I/O), dispatcher.go (id correlation),
// client.go (Client lifecycle), translate.go (chunk translation), stream.go (stream handle).
// D-01: single package with unexported framer and dispatcher; exported surface is Client + Stream.
package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// framer handles NDJSON (newline-delimited JSON) encoding/decoding over an
// io.Reader/io.Writer pair. Typical usage: readFrame reads from subprocess stdout;
// writeFrame writes to subprocess stdin.
type framer struct {
	scanner *bufio.Scanner
	enc     *json.Encoder
	mu      sync.Mutex // protects enc against concurrent writers
}

// newFramer constructs a framer backed by the given reader and writer.
// The scanner buffer is set to 1 MB (sc.Buffer is mandatory — ACP frames can exceed the
// default 64 KB limit for large prompts).
func newFramer(r io.Reader, w io.Writer) *framer {
	sc := bufio.NewScanner(r)
	// CRITICAL: default scanner limit is 64 KB; ACP frames can be larger.
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	return &framer{
		scanner: sc,
		enc:     json.NewEncoder(w),
	}
}

// readFrame reads the next newline-delimited JSON frame from the underlying reader.
// Returns io.EOF when the reader is exhausted (subprocess exited or pipe closed).
//
// CRITICAL: scanner.Bytes() returns a slice into the scanner's internal buffer.
// The buffer is reused on each Scan() call, so readFrame always copies before returning.
func (f *framer) readFrame() (json.RawMessage, error) {
	if !f.scanner.Scan() {
		if err := f.scanner.Err(); err != nil {
			return nil, fmt.Errorf("acp: framer read: %w", err)
		}
		return nil, io.EOF
	}
	// Copy scanner.Bytes() — the internal buffer is reused on the next Scan().
	raw := make([]byte, len(f.scanner.Bytes()))
	copy(raw, f.scanner.Bytes())
	return json.RawMessage(raw), nil
}

// writeFrame encodes v as JSON and appends a newline (json.Encoder appends \n automatically).
// Protected by f.mu to allow concurrent callers from unit tests; in production the writer
// goroutine is the sole caller.
func (f *framer) writeFrame(v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enc.Encode(v); err != nil {
		return fmt.Errorf("acp: framer write: %w", err)
	}
	return nil
}
