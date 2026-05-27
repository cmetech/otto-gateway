// fake-kiro-cli is a controllable substitute for kiro-cli used by tests/e2e/.
// It speaks the same JSON-RPC dialect (initialize, session/new, session/set_model,
// session/prompt, session/cancel, ping) but emits pre-scripted notifications during
// session/prompt instead of actually invoking an LLM. The notification script is
// supplied via env var OTTO_FAKE_KIRO_NOTIFICATIONS_FILE. Received frames are
// optionally logged to OTTO_FAKE_KIRO_RECEIVED_FRAMES_FILE for cancel-path tests.
// Per CLAUDE.md: pure-Go, no cgo, cross-compile clean.
//
// The binary is compiled by tests/e2e/tools_testmain_test.go's TestMain at package
// init (iteration-3 fix to MEDIUM #6 — package-level lifetime, NOT per-test temp dir).
//
// ACP method coverage (REVIEW HIGH #5):
//   1. initialize           → respond with kiro-cli 2.4.1-shaped capabilities
//   2. session/new          → respond with sessionId + availableModels
//   3. session/set_model    → respond {} (no-op success)
//   4. session/prompt       → emit pre-supplied notifications, then respond with stopReason
//   5. session/cancel       → respond {} (id-correlated when id present, else notification)
//   6. ping                 → respond {} (Phase 1 D-05 heartbeat)
//   7. EOF / stdin close    → exit 0 cleanly
//
// Per session/request_permission REQUEST (kiro → gateway path): NOT auto-emitted.
// The fake's notification stream skips it. (Phase 1 gateway-side auto-grant logic
// is exercised by internal/acp tests.)
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

const (
	envNotificationsFile = "OTTO_FAKE_KIRO_NOTIFICATIONS_FILE"
	envReceivedFramesFile = "OTTO_FAKE_KIRO_RECEIVED_FRAMES_FILE"
	envStopReason         = "OTTO_FAKE_KIRO_STOP_REASON"
)

// stdoutMu guards stdout writes — notifications + responses can race when the
// scripted notifications fire from a different code path than the response.
// In practice all writes happen in the same goroutine (the prompt handler) so
// the mutex is belt-and-suspenders, but make it explicit.
var stdoutMu sync.Mutex

func main() {
	// stdin reader with a 1 MB line cap — matches internal/acp/framer.go's
	// scanner buffer so large prompts don't get truncated.
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	// Optional received-frames log: tests set this to inspect what the gateway
	// sent us (used by scenario 12 to assert session/cancel emission).
	var framesLog *os.File
	if path := os.Getenv(envReceivedFramesFile); path != "" {
		// Best-effort open; if it fails, silently continue (don't crash the
		// fake just because the test forgot to create a writable temp dir).
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec
		if err == nil {
			framesLog = f
			defer func() { _ = framesLog.Close() }()
		}
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		// Copy because bufio.Scanner reuses its buffer between calls.
		raw := make([]byte, len(line))
		copy(raw, line)

		// Log received frame to the optional frames-log file.
		if framesLog != nil {
			_, _ = framesLog.Write(raw)
			_, _ = framesLog.Write([]byte{'\n'})
		}

		var frame map[string]json.RawMessage
		if err := json.Unmarshal(raw, &frame); err != nil {
			// Not JSON — ignore the frame and continue.
			continue
		}

		// Distinguish requests (have `method`) from responses (have `id` + `result`).
		methodRaw, hasMethod := frame["method"]
		if !hasMethod {
			// It's a response or unknown frame — drop silently.
			continue
		}

		var method string
		if err := json.Unmarshal(methodRaw, &method); err != nil {
			continue
		}

		var idRaw json.RawMessage
		idRaw, hasID := frame["id"]
		_ = hasID

		switch method {
		case "initialize":
			respond(idRaw, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"promptCapabilities": map[string]any{
						"image":           true,
						"audio":           false,
						"embeddedContext": true,
					},
				},
			})

		case "session/new":
			respond(idRaw, map[string]any{
				"sessionId": "e2e-session-1",
				"models": map[string]any{
					"availableModels": []map[string]any{
						{"modelId": "auto", "name": "Auto"},
						{"modelId": "sonnet", "name": "Sonnet"},
					},
					"currentModelId": "auto",
				},
			})

		case "session/set_model":
			// No-op success — kiro-cli silently accepts unknown model IDs.
			respond(idRaw, map[string]any{})

		case "session/prompt":
			// Emit pre-scripted notifications, then respond with stopReason.
			emitNotifications()
			stopReason := os.Getenv(envStopReason)
			if stopReason == "" {
				stopReason = "end_turn"
			}
			respond(idRaw, map[string]any{
				"stopReason": stopReason,
			})

		case "session/cancel":
			// session/cancel may be a notification (no id) OR a request (id present).
			// kiro-cli treats it as a notification in practice; respond {} if id present.
			if hasID && len(idRaw) > 0 && string(idRaw) != "null" {
				respond(idRaw, map[string]any{})
			}

		case "ping":
			respond(idRaw, map[string]any{})

		default:
			// Unknown method — if it has an id, respond with empty result;
			// otherwise drop. This makes the fake forgiving against future
			// ACP method additions in the gateway.
			if hasID && len(idRaw) > 0 && string(idRaw) != "null" {
				respond(idRaw, map[string]any{})
			}
		}
	}
	// scanner.Err() of nil OR io.EOF → graceful exit.
	os.Exit(0)
}

// respond writes a JSON-RPC response frame to stdout. idRaw is the raw JSON
// representation of the request id (preserves number vs string typing).
func respond(idRaw json.RawMessage, result map[string]any) {
	if len(idRaw) == 0 || string(idRaw) == "null" {
		// No id → no response expected. Drop.
		return
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(idRaw),
		"result":  result,
	}
	writeFrame(resp)
}

// emitNotifications reads OTTO_FAKE_KIRO_NOTIFICATIONS_FILE (if set) and emits
// each line verbatim to stdout. The file is expected to contain pre-built
// JSON-RPC notification frames separated by newlines.
func emitNotifications() {
	path := os.Getenv(envNotificationsFile)
	if path == "" {
		return
	}
	f, err := os.Open(path) //nolint:gosec // test-controlled path under t.TempDir()
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		// Validate it's parseable JSON; if not, skip (don't corrupt the stream).
		var probe any
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		_, _ = os.Stdout.Write(line)
		_, _ = os.Stdout.Write([]byte{'\n'})
	}
}

// writeFrame marshals v and writes a single LF-terminated frame to stdout.
func writeFrame(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		// Encoding error on a hand-built map[string]any is unreachable in
		// practice; print to stderr as a last resort.
		fmt.Fprintf(os.Stderr, "fake-kiro-cli: marshal error: %v\n", err)
		return
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	_, _ = os.Stdout.Write(b)
	_, _ = os.Stdout.Write([]byte{'\n'})
}
