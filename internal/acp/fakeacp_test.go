// Package acp_test — blackbox integration helpers.
// fakeacp_test.go provides a fake ACP server for deterministic integration tests
// without requiring kiro-cli.
//
// Phase 1.1 Plan 04 rewrite (D-21 / D-23): the fake now emits spec-compliant
// shapes only (initialize, session/new, session/prompt, session/update,
// session/request_permission). The four Phase 1 tests
// (TestIntegration_FakeACP_AutoGrantAndTranslation,
// TestIntegration_FakeACP_ChunkTranslation,
// TestIntegration_FakeACP_PromptChunkDelivery, TestIntegration_FakeACP_PingWorks)
// are consolidated into a single TestIntegration_FakeACP_E2E_MixedVariants in
// integration_test.go that drives the fake through one session exercising
// mixed session/update variants and the permission RESPONSE path.
//
// Permission flow (Plan 04 D-20): the fake EMITS a session/request_permission
// REQUEST (with an id), then reads the client's RESPONSE frame (no method field,
// result.optionId == "allow_always"). The legacy grant-permission request
// path is gone.
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

// updateVariant is a single session/update emit shape variant used by emitUpdate.
// Five variants drive the D-23 mixed-variants matrix:
//
//  0. session/update      + flat        + sessionUpdate + snake_case +
//     content:{type:"text", text:"hello"} (agent_message_chunk)
//  1. session/notification + params.update wrap + CamelCase  +
//     content:"world" (string) (agent_message_chunk → AgentMessageChunk)
//  2. _kiro.dev/session/update + flat + snake_case + body.text:"thinking"
//     (agent_thought_chunk)
//  3. session/update + params.update wrap + snake_case + title:"read_file"
//     (tool_call)
//  4. session/update + params.update wrap + snake_case + entries[]
//     (plan)
type updateVariant int

const (
	variantAgentMessageFlat updateVariant = iota
	variantAgentMessageWrappedCamel
	variantAgentThoughtKiroDev
	variantToolCallWrapped
	variantPlanWrapped
)

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
	// permissionResponseReceived is closed once the fake observes the client
	// sending an rpcResponse with result.optionId == "allow_always" — Plan 04
	// D-20 contract. Replaces Phase 1's permissionGranted (which watched for a
	// grant-permission REQUEST that no longer exists).
	permissionResponseReceived chan struct{}
	// promptSeen is closed on the first session/prompt request the fake sees,
	// so the test goroutine can synchronise an update sequence with the
	// active-stream lifecycle on the client.
	promptSeen chan struct{}
	// lastPromptID captures the id from the most recent session/prompt request.
	// emitPromptResult uses it to echo the id back on the response frame.
	lastPromptID float64
	// cancelSeen is closed once the fake observes a session/cancel notification
	// from the client. STRM-04 / Plan 04-03: D-10 contract.
	cancelSeen chan struct{}
	// lastCancelSID holds the sessionId from the most recent session/cancel
	// notification observed by the fake. Protected by the serve() goroutine's
	// sequential scan — read only after cancelSeen is closed.
	lastCancelSID string
}

// newFakeACPServer creates a fake ACP server and starts its goroutine.
// The caller must use f.clientRWC with acp.NewWithConn.
//
// Behaviour:
//  1. Respond to `initialize` with spec-compliant shape (protocolVersion:1 +
//     agentCapabilities.promptCapabilities{image:true, audio:false,
//     embeddedContext:false}).
//  2. Respond to `session/new` with sessionId "test-session-id" and a single
//     availableModels entry (currentModelId:"auto").
//  3. Respond to `ping` (best-effort; preserves Phase 1's PingWorks coverage).
//  4. On `session/prompt`: close promptSeen so the test can drive the
//     mid-stream update sequence. The PROMPT response (with stopReason:end_turn)
//     is emitted by the test goroutine via emitPromptResult AFTER the test has
//     completed its update-sequence emission, to avoid the client closing the
//     stream before the chunks arrive.
//  5. Detect inbound rpcResponse frames (method absent, result.optionId =
//     "allow_always") and close permissionResponseReceived.
func newFakeACPServer(t *testing.T) *fakeACPServer {
	t.Helper()

	// Client side: reads from clientRead, writes to clientWrite.
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()

	f := &fakeACPServer{
		clientRWC:                  &pipeRWC{r: clientRead, w: clientWrite},
		serverRead:                 serverRead,
		serverWrite:                serverWrite,
		done:                       make(chan struct{}),
		permissionResponseReceived: make(chan struct{}),
		promptSeen:                 make(chan struct{}),
		cancelSeen:                 make(chan struct{}),
	}

	go f.serve(t)
	return f
}

// serve is the fake server goroutine. Reads JSON-RPC lines from the client
// and dispatches by method (or by absence-of-method for the permission
// response).
func (f *fakeACPServer) serve(t *testing.T) {
	t.Helper()
	defer close(f.done)

	scanner := bufio.NewScanner(f.serverRead)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()

		var frame map[string]json.RawMessage
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Logf("fakeACP: malformed line: %v", err)
			continue
		}

		// Detect rpcResponse: no `method` field, has `id` AND `result`.
		// D-20: client auto-grants permission by sending a response on the
		// inbound request's id.
		if _, hasMethod := frame["method"]; !hasMethod {
			if resultRaw, hasResult := frame["result"]; hasResult {
				var result map[string]any
				if err := json.Unmarshal(resultRaw, &result); err == nil {
					if result["optionId"] == "allow_always" {
						// Close the channel idempotently — multiple permission
						// requests in one session would each trigger a response.
						select {
						case <-f.permissionResponseReceived:
							// already closed
						default:
							close(f.permissionResponseReceived)
						}
					}
				}
			}
			continue
		}

		var method string
		if err := json.Unmarshal(frame["method"], &method); err != nil {
			t.Logf("fakeACP: no method field")
			continue
		}

		var id *float64
		if raw, ok := frame["id"]; ok {
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
					"protocolVersion": 1,
					"agentCapabilities": map[string]any{
						"promptCapabilities": map[string]any{
							"image":           true,
							"audio":           false,
							"embeddedContext": false,
						},
					},
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
					"sessionId": "test-session-id",
					"models": map[string]any{
						"availableModels": []map[string]any{
							{"modelId": "claude-sonnet-4-7", "name": "Claude Sonnet 4.7"},
						},
						"currentModelId": "auto",
					},
				},
			}
			if err := f.writeJSON(resp); err != nil {
				t.Logf("fakeACP: write session/new response: %v", err)
				return
			}

		case "session/prompt":
			if id == nil {
				continue
			}
			// Stash the prompt id on the server so the test can drive the
			// emit sequence + the eventual emitPromptResult.
			f.lastPromptID = *id
			select {
			case <-f.promptSeen:
				// already closed (re-entrant prompt — should not happen in
				// our tests but be defensive)
			default:
				close(f.promptSeen)
			}
			// Response is NOT emitted here — the test drives the update
			// sequence first, then calls emitPromptResult to close the turn.

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

		case "session/cancel":
			// session/cancel is a notification (no id, no response expected).
			// STRM-04 / Plan 04-03: capture the sessionId and signal cancelSeen.
			var params struct {
				SessionID string `json:"sessionId"`
			}
			if raw, ok := frame["params"]; ok {
				_ = json.Unmarshal(raw, &params)
			}
			f.lastCancelSID = params.SessionID
			// Close idempotently — same pattern as permissionResponseReceived.
			select {
			case <-f.cancelSeen:
				// already closed
			default:
				close(f.cancelSeen)
			}

		default:
			t.Logf("fakeACP: unhandled method %q", method)
		}
	}
}

// emitPermissionRequest emits a session/request_permission REQUEST (note: an
// RPC request with an id, NOT a notification — per Plan 04 D-20). The client
// should respond on the same id; the fake's serve loop closes
// permissionResponseReceived when that response arrives.
func (f *fakeACPServer) emitPermissionRequest(requestID string, frameID uint64) error {
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      frameID,
		"method":  "session/request_permission",
		"params": map[string]any{
			"requestId":  requestID,
			"permission": map[string]any{"type": "shell_exec"},
		},
	}
	if err := f.writeJSON(req); err != nil {
		return fmt.Errorf("emitPermissionRequest: %w", err)
	}
	return nil
}

// emitUpdate emits a session/update-family notification using the variant's
// specific (method, body wrap, discriminator field, discriminator casing,
// content shape, update type) combination. The five variants cover the D-23
// mixed-variants matrix in a single session.
func (f *fakeACPServer) emitUpdate(sessionID string, v updateVariant) error {
	var notif map[string]any
	switch v {
	case variantAgentMessageFlat:
		// session/update + flat + sessionUpdate + snake_case +
		// content:{type:"text", text:"hello"}
		notif = map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId":     sessionID,
				"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{
					"type": "text",
					"text": "hello",
				},
			},
		}
	case variantAgentMessageWrappedCamel:
		// session/notification + params.update wrap + CamelCase +
		// content:"world" (string)
		notif = map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/notification",
			"params": map[string]any{
				"sessionId": sessionID,
				"update": map[string]any{
					"sessionUpdate": "AgentMessageChunk",
					"content":       "world",
				},
			},
		}
	case variantAgentThoughtKiroDev:
		// _kiro.dev/session/update + flat + snake_case + body.text only
		// (no content field).
		notif = map[string]any{
			"jsonrpc": "2.0",
			"method":  "_kiro.dev/session/update",
			"params": map[string]any{
				"sessionId":     sessionID,
				"sessionUpdate": "agent_thought_chunk",
				"text":          "thinking",
			},
		}
	case variantToolCallWrapped:
		// session/update + params.update wrap + snake_case + title:"read_file".
		notif = map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": sessionID,
				"update": map[string]any{
					"sessionUpdate": "tool_call",
					"title":         "read_file",
				},
			},
		}
	case variantPlanWrapped:
		// session/update + params.update wrap + snake_case + entries[].
		notif = map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": sessionID,
				"update": map[string]any{
					"sessionUpdate": "plan",
					"entries": []map[string]any{
						{"content": "Step 1"},
						{"content": "Step 2"},
					},
				},
			},
		}
	default:
		return fmt.Errorf("emitUpdate: unknown variant %d", v)
	}
	if err := f.writeJSON(notif); err != nil {
		return fmt.Errorf("emitUpdate: %w", err)
	}
	return nil
}

// emitPromptResult emits the session/prompt response frame with the supplied
// stopReason. This closes the turn on the client side (Stream.Result becomes
// readable). Echoes the id captured during the inbound session/prompt request.
func (f *fakeACPServer) emitPromptResult(stopReason string) error {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      f.lastPromptID,
		"result": map[string]any{
			"stopReason": stopReason,
		},
	}
	if err := f.writeJSON(resp); err != nil {
		return fmt.Errorf("emitPromptResult: %w", err)
	}
	return nil
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
