// Package acp_test — blackbox integration helpers.
// fakeacp_test.go provides a fake ACP server for deterministic integration tests
// without requiring kiro-cli.
// REVIEW FIX (Codex HIGH / SC#4): fake server runs as a goroutine via io.Pipe.
package acp_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

// pipeRWC wraps io.PipeReader + io.PipeWriter as an io.ReadWriteCloser.
// This is what gets passed to acp.NewWithConn.
type pipeRWC struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p *pipeRWC) Read(b []byte) (int, error)  { return p.r.Read(b) }  //nolint:wrapcheck
func (p *pipeRWC) Write(b []byte) (int, error) { return p.w.Write(b) } //nolint:wrapcheck
func (p *pipeRWC) Close() error {
	rerr := p.r.Close()
	werr := p.w.Close()
	if rerr != nil {
		return fmt.Errorf("pipeRWC close read: %w", rerr)
	}
	if werr != nil {
		return fmt.Errorf("pipeRWC close write: %w", werr)
	}
	return nil
}

// fakeACPServer is a scripted JSON-RPC server that responds to ACP requests
// and emits scripted notifications. It runs as a goroutine using io.Pipe pairs.
type fakeACPServer struct {
	// clientRWC is passed to acp.NewWithConn.
	clientRWC io.ReadWriteCloser
	// serverRead / serverWrite are the server's side of the pipes.
	serverRead  *io.PipeReader
	serverWrite *io.PipeWriter
	// done is closed when the server goroutine exits.
	done chan struct{}
	// permissionGranted is closed after a session/grant_permission is received.
	permissionGranted chan struct{}
	// updateEmitted is closed after a session/update is emitted to the client.
	updateEmitted chan struct{}
}

// newFakeACPServer creates a fake ACP server and starts its goroutine.
// The caller must use f.clientRWC with acp.NewWithConn.
// The server emits:
//  1. Respond to initialize
//  2. Respond to session/new with sessionId "test-session-id"
//  3. Respond to ping
//  4. After session/new response: emit session/request_permission
//  5. On receiving grant_permission: emit session/update with type "text", content "hello from fake"
//  6. On receiving session/prompt: emit session/update (content "hello from fake") then a prompt response frame
func newFakeACPServer(t *testing.T) *fakeACPServer {
	t.Helper()

	// Client side: reads from clientRead, writes to clientWrite.
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()

	f := &fakeACPServer{
		clientRWC:         &pipeRWC{r: clientRead, w: clientWrite},
		serverRead:        serverRead,
		serverWrite:       serverWrite,
		done:              make(chan struct{}),
		permissionGranted: make(chan struct{}),
		updateEmitted:     make(chan struct{}),
	}

	go f.serve(t)
	return f
}

// serve is the fake server goroutine.
func (f *fakeACPServer) serve(t *testing.T) {
	t.Helper()
	defer close(f.done)

	scanner := bufio.NewScanner(f.serverRead)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	sessionNewSeen := false

	for scanner.Scan() {
		line := scanner.Bytes()

		var req map[string]json.RawMessage
		if err := json.Unmarshal(line, &req); err != nil {
			t.Logf("fakeACP: malformed line: %v", err)
			continue
		}

		var method string
		if err := json.Unmarshal(req["method"], &method); err != nil {
			t.Logf("fakeACP: no method field")
			continue
		}

		var id *float64
		if raw, ok := req["id"]; ok {
			var idVal float64
			if err := json.Unmarshal(raw, &idVal); err == nil {
				id = &idVal
			}
		}

		switch method {
		case "initialize":
			if id == nil {
				continue
			}
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      *id,
				"result": map[string]any{
					"capabilities": map[string]any{"promptCapabilities": map[string]any{}},
				},
			}
			if err := f.writeJSON(resp); err != nil {
				t.Logf("fakeACP: write initialize response: %v", err)
				return
			}

		case "session/new":
			if id == nil {
				continue
			}
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      *id,
				"result": map[string]any{
					"sessionId":       "test-session-id",
					"availableModels": []string{},
					"currentModel":    "auto",
				},
			}
			if err := f.writeJSON(resp); err != nil {
				t.Logf("fakeACP: write session/new response: %v", err)
				return
			}
			sessionNewSeen = true
			// After session/new response, proactively emit session/request_permission.
			permReqID := "perm-req-1"
			perm := map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/request_permission",
				"params": map[string]any{
					"requestId":  permReqID,
					"permission": map[string]any{"type": "shell_exec"},
				},
			}
			if err := f.writeJSON(perm); err != nil {
				t.Logf("fakeACP: write session/request_permission: %v", err)
				return
			}

		case "session/grant_permission":
			// Client auto-granted — emit the session/update.
			close(f.permissionGranted)
			update := map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "test-session-id",
					"type":      "text",
					"content":   "hello from fake",
				},
			}
			if err := f.writeJSON(update); err != nil {
				t.Logf("fakeACP: write session/update: %v", err)
				return
			}
			close(f.updateEmitted)

		case "session/prompt":
			// SC#4 / CR-02 integration: emit a session/update chunk FIRST,
			// then the prompt response frame. The chunk exercises the active
			// stream path (ACP-05); the response frame exercises CR-02's fix
			// (Prompt success arm closes the stream so Result() returns).
			if id == nil {
				continue
			}
			chunkNotif := map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "test-session-id",
					"type":      "text",
					"content":   "hello from fake",
				},
			}
			if err := f.writeJSON(chunkNotif); err != nil {
				t.Logf("fakeACP: write session/update: %v", err)
				return
			}
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      *id,
				"result":  map[string]any{},
			}
			if err := f.writeJSON(resp); err != nil {
				t.Logf("fakeACP: write session/prompt response: %v", err)
				return
			}

		case "ping":
			if id == nil {
				continue
			}
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      *id,
				"result":  map[string]any{},
			}
			if err := f.writeJSON(resp); err != nil {
				t.Logf("fakeACP: write ping response: %v", err)
				return
			}

		default:
			// Ignore unknown methods; log for debugging.
			if sessionNewSeen {
				t.Logf("fakeACP: unhandled method %q", method)
			}
		}
	}
}

// writeJSON marshals v and writes it as a JSON line to serverWrite.
func (f *fakeACPServer) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("fakeACP: marshal: %w", err)
	}
	data = append(data, '\n')
	_, err = f.serverWrite.Write(data)
	if err != nil {
		return fmt.Errorf("fakeACP: write: %w", err)
	}
	return nil
}

// close stops the fake server by closing the server-side pipes.
func (f *fakeACPServer) close() {
	_ = f.serverWrite.Close()
	_ = f.serverRead.Close()
	<-f.done
}
