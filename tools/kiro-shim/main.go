// Command kiro-shim is a stdio JSON-RPC recorder for diagnosing the
// Phase 5 SC3 wire-protocol divergence between the working pool path
// and the broken session path.
//
// It accepts the real kiro-cli path as argv[1] and forwards argv[2:]
// to the child verbatim. Every line of stdin (gateway → kiro) and
// stdout (kiro → gateway) is tee'd to per-PID JSON-Lines transcript
// files under $TMPDIR (default /tmp).
//
// Frame format (one JSON object per line):
//
//	{"ts":"<RFC3339Nano>","pid":<shim>,"child_pid":<kiro>,"dir":"OUT"|"IN","frame":<verbatim JSON-RPC>}
//
// "OUT" = gateway → kiro (read from os.Stdin, written to child stdin).
// "IN"  = kiro → gateway (read from child stdout, written to os.Stdout).
//
// The first line of each .out / .in file is a `# kiro-shim invocation: ...`
// header so the resolved command line is auditable; downstream merging
// tools must skip lines beginning with `#`.
//
// Invocation contract (binding, per plan 05-04 MEDIUM-2):
//
//	KIRO_CMD=/tmp/kiro-shim KIRO_ARGS="$(which kiro-cli) acp"
//
// resolves to `kiro-shim <kiro-cli-path> acp` at exec time.
//
// This binary is a diagnostic helper, NOT production code. It MUST NOT
// import internal/* packages; the .go-arch-lint.yml workdir is
// `internal`, so this file (under tools/) is naturally excluded from
// the arch-lint gate.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kiro-shim <kiro-cli-path> [args...]")
		os.Exit(2)
	}
	kiroBin := os.Args[1]
	kiroArgs := os.Args[2:]

	tmpDir := os.TempDir()
	shimPID := os.Getpid()
	outPath := filepath.Join(tmpDir, fmt.Sprintf("kiro-wire-%d.out", shimPID))
	inPath := filepath.Join(tmpDir, fmt.Sprintf("kiro-wire-%d.in", shimPID))

	outFile, err := os.Create(outPath) //nolint:gosec // diagnostic tool; path is shim-controlled
	if err != nil {
		fmt.Fprintf(os.Stderr, "kiro-shim: open out: %v\n", err)
		os.Exit(2)
	}
	defer func() { _ = outFile.Close() }()
	inFile, err := os.Create(inPath) //nolint:gosec // diagnostic tool; path is shim-controlled
	if err != nil {
		fmt.Fprintf(os.Stderr, "kiro-shim: open in: %v\n", err)
		os.Exit(2)
	}
	defer func() { _ = inFile.Close() }()

	// Header line — diagnostic merge tools skip lines starting with `#`.
	header := fmt.Sprintf("# kiro-shim invocation: %s %v\n", kiroBin, kiroArgs)
	_, _ = outFile.WriteString(header)
	_, _ = inFile.WriteString(header)

	cmd := exec.Command(kiroBin, kiroArgs...) //nolint:gosec // diagnostic tool; argv is operator-supplied
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "kiro-shim: stdin pipe: %v\n", err)
		os.Exit(2)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "kiro-shim: stdout pipe: %v\n", err)
		os.Exit(2)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "kiro-shim: start %q: %v\n", kiroBin, err)
		os.Exit(2)
	}
	childPID := cmd.Process.Pid

	// Per-file write mutex so concurrent tee writes never interleave
	// inside a single line. Scanner-driven NDJSON read means each line
	// is one frame, but defensive locking is cheap.
	var outMu, inMu sync.Mutex

	var wg sync.WaitGroup

	// OUT pump: os.Stdin → child stdin, tee to outFile.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { _ = stdin.Close() }()
		teeLines(os.Stdin, stdin, outFile, &outMu, shimPID, childPID, "OUT")
	}()

	// IN pump: child stdout → os.Stdout, tee to inFile.
	wg.Add(1)
	go func() {
		defer wg.Done()
		teeLines(stdout, os.Stdout, inFile, &inMu, shimPID, childPID, "IN")
	}()

	wg.Wait()
	if err := cmd.Wait(); err != nil {
		// Surface a non-zero exit so the gateway notices the child died,
		// but only if it's a real failure (ExitError) — context-driven
		// teardown emits an *exec.ExitError too and that's the normal
		// shutdown path. The shim just forwards whatever happened.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "kiro-shim: cmd wait: %v\n", err)
		os.Exit(1)
	}
}

// teeLines reads NDJSON lines from r, writes them verbatim to dst (the
// real consumer), and also writes one JSON-tagged frame record per line
// to trace. Lines that fail to parse as JSON are still tee'd to dst so
// the real consumer never observes corruption; the trace record carries
// the raw bytes under a synthesized {"raw": ...} value so diagnosis is
// still possible.
func teeLines(r io.Reader, dst io.Writer, trace io.Writer, mu *sync.Mutex, pid, childPID int, dir string) {
	sc := bufio.NewScanner(r)
	// kiro-cli frames can exceed the default 64 KB scanner limit when
	// prompt blocks carry large content. Match internal/acp/framer.go.
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes() // re-used buffer; copy before async use

		// Forward verbatim to the real consumer — including the trailing
		// newline that bufio.Scanner stripped. Write line+'\n' atomically.
		lineCopy := make([]byte, len(line)+1)
		copy(lineCopy, line)
		lineCopy[len(line)] = '\n'
		if _, err := dst.Write(lineCopy); err != nil {
			// Real consumer died — stop teeing.
			return
		}

		// Tee to trace as a structured frame.
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		// Parse the frame body — fall back to {"raw": ...} on parse
		// failure so diagnosis can still see what crossed the wire.
		var frameVal json.RawMessage
		if json.Valid(line) {
			// json.RawMessage holds the verbatim bytes; we still copy
			// because the scanner buffer is reused.
			frameVal = make(json.RawMessage, len(line))
			copy(frameVal, line)
		} else {
			// Wrap the raw bytes as a JSON string for visibility.
			fallback, _ := json.Marshal(map[string]string{"raw": string(line)})
			frameVal = fallback
		}
		record := struct {
			TS       string          `json:"ts"`
			PID      int             `json:"pid"`
			ChildPID int             `json:"child_pid"`
			Dir      string          `json:"dir"`
			Frame    json.RawMessage `json:"frame"`
		}{
			TS:       ts,
			PID:      pid,
			ChildPID: childPID,
			Dir:      dir,
			Frame:    frameVal,
		}
		data, err := json.Marshal(record)
		if err != nil {
			continue
		}
		mu.Lock()
		_, _ = trace.Write(data)
		_, _ = trace.Write([]byte("\n"))
		mu.Unlock()
	}
}
