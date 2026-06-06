// Package acp implements the JSON-RPC 2.0 client over stdio for kiro-cli.
// File-scoped layers: framer.go (NDJSON I/O), dispatcher.go (id correlation),
// client.go (Client lifecycle), translate.go (chunk translation), stream.go (stream handle).
// D-01: single package with unexported framer and dispatcher; exported surface is Client + Stream.
package acp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// maxFrameSize bounds the largest ACP frame the framer will read. Prior
// value 1 MiB was tripped by any large tool_call_update payload
// (large file read, large code block, image/base64) and turned the
// resulting bufio.ErrTooLong into a slot-killing EOF in the readLoop.
// 16 MiB is generous — kiro-cli's tool outputs rarely exceed a few MiB
// even on file reads of large logs — but bounds the worst case so an
// adversarial output cannot grow memory unbounded.
const maxFrameSize = 16 * 1024 * 1024

// ErrFrameTooLong is returned by readFrame when the inbound frame
// exceeds maxFrameSize. The readLoop logs it as a distinct
// acp.framer.frame_too_long event so the operator can correlate slot
// churn with payload size in /admin tail.
var ErrFrameTooLong = errors.New("acp: frame exceeds max size")

// framer handles NDJSON (newline-delimited JSON) encoding/decoding over an
// io.Reader/io.Writer pair. Typical usage: readFrame reads from subprocess stdout;
// writeFrame writes to subprocess stdin.
type framer struct {
	scanner *bufio.Scanner
	enc     *json.Encoder
	mu      sync.Mutex // protects enc against concurrent writers
}

// newFramer constructs a framer backed by the given reader and writer.
// Scanner is configured with a 64 KiB initial buffer and a 16 MiB cap
// (see maxFrameSize). Initial 64 KiB keeps small-frame allocations
// cheap; the cap absorbs occasional large tool outputs without
// crashing the slot.
func newFramer(r io.Reader, w io.Writer) *framer {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxFrameSize)
	return &framer{
		scanner: sc,
		enc:     json.NewEncoder(w),
	}
}

// readFrame reads the next newline-delimited JSON frame from the underlying reader.
// Returns io.EOF when the reader is exhausted (subprocess exited or pipe closed).
// Returns ErrFrameTooLong when an inbound frame exceeds maxFrameSize so
// callers can distinguish "payload too big" from generic read failure.
//
// CRITICAL: scanner.Bytes() returns a slice into the scanner's internal buffer.
// The buffer is reused on each Scan() call, so readFrame always copies before returning.
func (f *framer) readFrame() (json.RawMessage, error) {
	if !f.scanner.Scan() {
		if err := f.scanner.Err(); err != nil {
			if errors.Is(err, bufio.ErrTooLong) {
				return nil, ErrFrameTooLong
			}
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
