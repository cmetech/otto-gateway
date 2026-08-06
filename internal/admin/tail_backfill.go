package admin

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

// TailerInitialBackfillMaxBytes bounds first-open file I/O independently of
// the 500-record ring. Large or low-newline files therefore cannot force an
// unbounded scan when an operator opens the dashboard.
const TailerInitialBackfillMaxBytes int64 = 8 * 1024 * 1024

func readInitialBackfill(
	f *os.File,
	maxLines int,
	maxBytes int64,
) (lines []string, partial string, size int64, err error) {
	info, err := f.Stat()
	if err != nil {
		return nil, "", 0, fmt.Errorf("stat initial log history: %w", err)
	}
	size = info.Size()
	if size == 0 || maxLines <= 0 || maxBytes <= 0 {
		return nil, "", size, nil
	}

	start := size - maxBytes
	if start < 0 {
		start = 0
	}
	windowSize := size - start
	if int64(int(windowSize)) != windowSize {
		return nil, "", size, fmt.Errorf("initial log history window too large: %d", windowSize)
	}
	window := make([]byte, int(windowSize))
	n, readErr := f.ReadAt(window, start)
	if readErr != nil {
		return nil, "", size, fmt.Errorf("read initial log history: %w", readErr)
	}
	if n != len(window) {
		return nil, "", size, fmt.Errorf("read initial log history: got %d of %d bytes", n, len(window))
	}

	if start > 0 {
		var previous [1]byte
		if _, previousErr := f.ReadAt(previous[:], start-1); previousErr != nil {
			return nil, "", size, fmt.Errorf("inspect initial log history boundary: %w", previousErr)
		}
		if previous[0] != '\n' {
			newline := bytes.IndexByte(window, '\n')
			if newline < 0 {
				return nil, "", size, nil
			}
			window = window[newline+1:]
		}
	}

	complete := window
	if len(window) > 0 && window[len(window)-1] != '\n' {
		lastNewline := bytes.LastIndexByte(window, '\n')
		if lastNewline < 0 {
			partial = string(window)
			complete = nil
		} else {
			partial = string(window[lastNewline+1:])
			complete = window[:lastNewline+1]
		}
	}
	if len(partial) > TailerMaxLineBytes {
		partial = partial[:TailerMaxLineBytes]
	}

	if len(complete) == 0 {
		return nil, partial, size, nil
	}
	complete = bytes.TrimSuffix(complete, []byte{'\n'})
	records := strings.Split(string(complete), "\n")
	if len(records) > maxLines {
		records = records[len(records)-maxLines:]
	}
	for _, record := range records {
		record = strings.TrimSuffix(record, "\r")
		if len(record) > TailerMaxLineBytes {
			record = record[:TailerMaxLineBytes]
		}
		lines = append(lines, record)
	}
	return lines, partial, size, nil
}
